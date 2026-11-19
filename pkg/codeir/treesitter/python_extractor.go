package treesitter

import (
	"context"
	"path"
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/security"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// PythonExtractor 基于 tree-sitter-python（CGO 严格隔离）实现 Python 静态事实提取器（F-04 W2）：
// 1. 提取模块级类/函数与方法符号（含归一化内容指纹）；
// 2. 提取 FastAPI (FastAPI / APIRouter prefix 拼接) 与 Flask (Flask / Blueprint url_prefix 拼接) 装饰器 HTTP 路由；
// 3. 提取 SQLAlchemy (__tablename__ + Column / mapped_column) 与 Django ORM (models.Model + Meta.db_table) 实体模型；
// 4. 提取 Celery (@celery_app.task / @shared_task) 与 APScheduler (@scheduler.scheduled_job) 调度任务原语。
type PythonExtractor struct {
	redactor *security.SecretRedactor
}

func NewPythonExtractor() *PythonExtractor {
	return &PythonExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *PythonExtractor) Name() string { return "treesitter-python" }

func (e *PythonExtractor) SupportedLanguages() []string { return []string{"python"} }

func (e *PythonExtractor) ExtractFile(ctx context.Context, file codeir.FileEntry, content []byte) (*codeir.ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_python.Language())
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}

	tree := parser.ParseCtx(ctx, content, nil)
	if tree == nil {
		return &codeir.ExtractionResult{
			Diagnostics: []codeir.ParseDiagnostic{{
				FilePath: file.RelPath,
				Message:  "tree-sitter-python 解析返回空语法树",
			}},
		}, nil
	}
	defer tree.Close()

	w := &pythonWalker{
		src:       content,
		filePath:  file.RelPath,
		modPrefix: pythonModulePrefix(file.RelPath),
		routers:   make(map[string]pyRouterInfo),
		redactor:  e.redactor,
		res:       &codeir.ExtractionResult{},
	}
	// 第一遍收集模块级路由器/Blueprint/FastAPI/Flask 实例变量前缀与框架归属
	w.collectModuleRouters(tree.RootNode())
	// 第二遍完整遍历提取类、函数、路由、ORM 模型与调度任务
	w.walk(tree.RootNode(), nil, nil)
	return w.res, nil
}

type pyRouterInfo struct {
	Framework string // "fastapi" | "flask"
	Prefix    string
}

type pyDecorator struct {
	ObjName    string
	MethodName string
	FullName   string
	PosArgs    []string
	KwArgs     map[string]string
	KwListArgs map[string][]string
	Raw        string
	Node       *tree_sitter.Node
}

type pythonWalker struct {
	src       []byte
	filePath  string
	modPrefix string
	routers   map[string]pyRouterInfo
	redactor  *security.SecretRedactor
	res       *codeir.ExtractionResult
}

func pythonModulePrefix(relPath string) string {
	clean := strings.TrimSuffix(relPath, path.Ext(relPath))
	clean = strings.TrimSuffix(clean, "/__init__")
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.ReplaceAll(clean, "/", ".")
	return strings.Trim(clean, ".")
}

func (w *pythonWalker) fqName(parts ...string) string {
	all := make([]string, 0, len(parts)+1)
	if w.modPrefix != "" {
		all = append(all, w.modPrefix)
	}
	for _, p := range parts {
		if p != "" {
			all = append(all, p)
		}
	}
	return strings.Join(all, ".")
}

func (w *pythonWalker) collectModuleRouters(root *tree_sitter.Node) {
	if root == nil {
		return
	}
	for i := uint(0); i < root.ChildCount(); i++ {
		ch := root.Child(i)
		if ch.Kind() != "expression_statement" {
			continue
		}
		for j := uint(0); j < ch.ChildCount(); j++ {
			assign := ch.Child(j)
			if assign.Kind() != "assignment" {
				continue
			}
			left := assign.ChildByFieldName("left")
			right := assign.ChildByFieldName("right")
			if left == nil || right == nil || right.Kind() != "call" {
				continue
			}
			varName := strings.TrimSpace(left.Utf8Text(w.src))
			fnNode := right.ChildByFieldName("function")
			if fnNode == nil {
				continue
			}
			callee := strings.TrimSpace(fnNode.Utf8Text(w.src))
			if idx := strings.LastIndex(callee, "."); idx >= 0 {
				callee = callee[idx+1:]
			}
			_, kwArgs, _ := w.parsePyCallArgs(right.ChildByFieldName("arguments"))
			switch callee {
			case "APIRouter", "FastAPI":
				w.routers[varName] = pyRouterInfo{
					Framework: "fastapi",
					Prefix:    kwArgs["prefix"],
				}
			case "Flask", "Blueprint":
				w.routers[varName] = pyRouterInfo{
					Framework: "flask",
					Prefix:    kwArgs["url_prefix"],
				}
			}
		}
	}
}

func (w *pythonWalker) walk(node *tree_sitter.Node, classStack []string, pendingDecorators []pyDecorator) {
	if node == nil {
		return
	}

	switch node.Kind() {
	case "decorated_definition":
		var decs []pyDecorator
		var defNode *tree_sitter.Node
		for i := uint(0); i < node.ChildCount(); i++ {
			ch := node.Child(i)
			switch ch.Kind() {
			case "decorator":
				decs = append(decs, w.parseDecorator(ch))
			case "function_definition", "class_definition":
				defNode = ch
			}
		}
		if defNode != nil {
			w.walk(defNode, classStack, decs)
		}
		return

	case "class_definition":
		w.handleClassDef(node, classStack)
		return

	case "function_definition":
		w.handleFuncDef(node, classStack, pendingDecorators)
		return
	}

	for i := uint(0); i < node.ChildCount(); i++ {
		w.walk(node.Child(i), classStack, nil)
	}
}

func (w *pythonWalker) handleClassDef(node *tree_sitter.Node, classStack []string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := strings.TrimSpace(nameNode.Utf8Text(w.src))
	if className == "" {
		return
	}

	nextStack := append(append([]string(nil), classStack...), className)
	fqSym := w.fqName(nextStack...)
	bases := w.extractPyBaseClasses(node.ChildByFieldName("superclasses"))
	bodyNode := node.ChildByFieldName("body")
	span := nodeSpan(w.filePath, node)
	fp := ComputeNodeFingerprint(node, w.src)

	w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
		FQSymbol:  fqSym,
		Kind:      "class",
		Language:  "python",
		Signature: "class " + className,
		BaseTypes: bases,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceExact,
			Span:               span,
			ContentFingerprint: fp,
		},
	})

	// 检测 SQLAlchemy 与 Django ORM 实体模型
	w.extractPyORMModel(className, bases, bodyNode, span, fp)

	if bodyNode != nil {
		for i := uint(0); i < bodyNode.ChildCount(); i++ {
			ch := bodyNode.Child(i)
			// 跳过 Django Meta 内部配置类的符号噪音
			if ch.Kind() == "class_definition" {
				if cn := ch.ChildByFieldName("name"); cn != nil && strings.TrimSpace(cn.Utf8Text(w.src)) == "Meta" {
					continue
				}
			}
			w.walk(ch, nextStack, nil)
		}
	}
}

func (w *pythonWalker) handleFuncDef(node *tree_sitter.Node, classStack []string, decorators []pyDecorator) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	funcName := strings.TrimSpace(nameNode.Utf8Text(w.src))
	if funcName == "" {
		return
	}

	parts := append(append([]string(nil), classStack...), funcName)
	fqSym := w.fqName(parts...)
	kind := "function"
	if len(classStack) > 0 {
		kind = "method"
	}

	paramsText := "()"
	var paramList []string
	if pNode := node.ChildByFieldName("parameters"); pNode != nil {
		paramsText = strings.TrimSpace(pNode.Utf8Text(w.src))
		paramList = w.extractPyParams(pNode)
	}
	retType := ""
	if rNode := node.ChildByFieldName("return_type"); rNode != nil {
		retType = strings.TrimSpace(rNode.Utf8Text(w.src))
	}

	sig := "def " + funcName + paramsText
	if retType != "" {
		sig += " -> " + retType
	}

	span := nodeSpan(w.filePath, node)
	fp := ComputeNodeFingerprint(node, w.src)

	w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
		FQSymbol:  fqSym,
		Kind:      kind,
		Language:  "python",
		Signature: sig,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceExact,
			Span:               span,
			ContentFingerprint: fp,
		},
	})

	// 解析装饰器：FastAPI / Flask 路由 + Celery / APScheduler 任务
	for _, dec := range decorators {
		w.extractPyRouteFromDecorator(dec, fqSym, paramList, retType, span, fp)
		w.extractPySchedulerFromDecorator(dec, fqSym, span, fp)
	}
}

func (w *pythonWalker) parseDecorator(node *tree_sitter.Node) pyDecorator {
	dec := pyDecorator{
		KwArgs:     make(map[string]string),
		KwListArgs: make(map[string][]string),
		Raw:        strings.TrimSpace(node.Utf8Text(w.src)),
		Node:       node,
	}

	var expr *tree_sitter.Node
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		if ch.Kind() != "@" {
			expr = ch
			break
		}
	}
	if expr == nil {
		return dec
	}

	target := expr
	if expr.Kind() == "call" {
		target = expr.ChildByFieldName("function")
		pos, kw, kwList := w.parsePyCallArgs(expr.ChildByFieldName("arguments"))
		dec.PosArgs = pos
		dec.KwArgs = kw
		dec.KwListArgs = kwList
	}
	if target != nil {
		full := strings.TrimSpace(target.Utf8Text(w.src))
		dec.FullName = full
		if idx := strings.LastIndex(full, "."); idx >= 0 {
			dec.ObjName = full[:idx]
			dec.MethodName = full[idx+1:]
		} else {
			dec.MethodName = full
		}
	}
	return dec
}

func (w *pythonWalker) parsePyCallArgs(argsNode *tree_sitter.Node) ([]string, map[string]string, map[string][]string) {
	var pos []string
	kw := make(map[string]string)
	kwList := make(map[string][]string)
	if argsNode == nil {
		return pos, kw, kwList
	}

	for i := uint(0); i < argsNode.ChildCount(); i++ {
		ch := argsNode.Child(i)
		switch ch.Kind() {
		case "string":
			pos = append(pos, unquotePyString(ch.Utf8Text(w.src)))
		case "keyword_argument":
			kNode := ch.ChildByFieldName("name")
			vNode := ch.ChildByFieldName("value")
			if kNode == nil || vNode == nil {
				continue
			}
			k := strings.TrimSpace(kNode.Utf8Text(w.src))
			if vNode.Kind() == "list" || vNode.Kind() == "tuple" {
				var vals []string
				for j := uint(0); j < vNode.ChildCount(); j++ {
					item := vNode.Child(j)
					if item.Kind() == "string" {
						vals = append(vals, unquotePyString(item.Utf8Text(w.src)))
					}
				}
				kwList[k] = vals
			} else {
				kw[k] = unquotePyString(vNode.Utf8Text(w.src))
			}
		}
	}
	return pos, kw, kwList
}

func (w *pythonWalker) extractPyRouteFromDecorator(
	dec pyDecorator,
	handlerFQ string,
	params []string,
	retType string,
	span codeir.SourceSpan,
	fp string,
) {
	mLower := strings.ToLower(dec.MethodName)
	var httpMethods []string
	switch mLower {
	case "get", "post", "put", "delete", "patch", "head", "options":
		httpMethods = []string{strings.ToUpper(mLower)}
	case "route", "api_route":
		if list, ok := dec.KwListArgs["methods"]; ok && len(list) > 0 {
			for _, v := range list {
				httpMethods = append(httpMethods, strings.ToUpper(strings.TrimSpace(v)))
			}
		} else {
			httpMethods = []string{"GET"}
		}
	default:
		return
	}

	routePath := ""
	if len(dec.PosArgs) > 0 {
		routePath = dec.PosArgs[0]
	} else if p, ok := dec.KwArgs["path"]; ok {
		routePath = p
	} else if p, ok := dec.KwArgs["rule"]; ok {
		routePath = p
	}
	if routePath == "" {
		return
	}

	framework := "fastapi"
	prefix := ""
	if ri, ok := w.routers[dec.ObjName]; ok {
		framework = ri.Framework
		prefix = ri.Prefix
	} else if mLower == "route" {
		framework = "flask"
	}

	fullPath := joinRoutePath(prefix, routePath)
	for _, m := range httpMethods {
		w.res.HTTPRoutes = append(w.res.HTTPRoutes, codeir.HTTPRouteFact{
			Method:          m,
			Path:            fullPath,
			HandlerFQSymbol: handlerFQ,
			RequestParams:   params,
			ResponseType:    retType,
			Framework:       framework,
			Meta: codeir.FactMeta{
				Confidence:         codeir.ConfidenceExact,
				Span:               span,
				ContentFingerprint: fp,
			},
		})
	}
}

func (w *pythonWalker) extractPySchedulerFromDecorator(
	dec pyDecorator,
	enclosingFQ string,
	span codeir.SourceSpan,
	fp string,
) {
	apiName := ""
	switch {
	case dec.MethodName == "shared_task":
		apiName = "celery.shared_task"
	case dec.MethodName == "task" && dec.ObjName != "":
		apiName = "celery.task"
	case dec.MethodName == "scheduled_job":
		apiName = "apscheduler.scheduled_job"
	default:
		return
	}

	snippet, _ := w.redactor.RedactText(dec.Raw)
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	dSpan := span
	if dec.Node != nil {
		dSpan = nodeSpan(w.filePath, dec.Node)
	}
	w.res.RuntimePrimitives = append(w.res.RuntimePrimitives, codeir.RuntimePrimitiveFact{
		Category:          "scheduler",
		APIName:           apiName,
		EnclosingFQSymbol: enclosingFQ,
		ContextSnippet:    snippet,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceHeuristic,
			Span:               dSpan,
			ContentFingerprint: fp,
		},
	})
}

func (w *pythonWalker) extractPyORMModel(
	className string,
	bases []string,
	body *tree_sitter.Node,
	span codeir.SourceSpan,
	fp string,
) {
	if body == nil {
		return
	}

	tableName := ""
	isDjangoModel := false
	for _, b := range bases {
		if b == "models.Model" || strings.HasSuffix(b, ".Model") || b == "Model" {
			isDjangoModel = true
		}
	}

	var fields []codeir.DataModelField
	for i := uint(0); i < body.ChildCount(); i++ {
		ch := body.Child(i)
		switch ch.Kind() {
		case "expression_statement":
			for j := uint(0); j < ch.ChildCount(); j++ {
				assign := ch.Child(j)
				if assign.Kind() != "assignment" {
					continue
				}
				left := assign.ChildByFieldName("left")
				right := assign.ChildByFieldName("right")
				if left == nil || right == nil {
					continue
				}
				lhs := strings.TrimSpace(left.Utf8Text(w.src))
				if lhs == "__tablename__" {
					tableName = unquotePyString(right.Utf8Text(w.src))
					continue
				}
				if right.Kind() == "call" {
					if f, ok := w.parsePyORMField(lhs, assign.ChildByFieldName("type"), right); ok {
						fields = append(fields, f)
					}
				}
			}
		case "class_definition":
			// Django inner class Meta: db_table = "..."
			if cn := ch.ChildByFieldName("name"); cn != nil && strings.TrimSpace(cn.Utf8Text(w.src)) == "Meta" {
				if metaBody := ch.ChildByFieldName("body"); metaBody != nil {
					if tbl := w.extractDjangoMetaDBTable(metaBody); tbl != "" {
						tableName = tbl
					}
				}
			}
		}
	}

	if tableName == "" && isDjangoModel && len(fields) > 0 {
		tableName = toSnakeCase(className)
	}
	if tableName == "" {
		return
	}

	w.res.DataModels = append(w.res.DataModels, codeir.DataModelFact{
		EntityName:        className,
		TableOrCollection: tableName,
		StorageKind:       "sql",
		Fields:            fields,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceExact,
			Span:               span,
			ContentFingerprint: fp,
		},
	})
}

func (w *pythonWalker) parsePyORMField(colName string, typeAnn *tree_sitter.Node, callNode *tree_sitter.Node) (codeir.DataModelField, bool) {
	fnNode := callNode.ChildByFieldName("function")
	if fnNode == nil {
		return codeir.DataModelField{}, false
	}
	callee := strings.TrimSpace(fnNode.Utf8Text(w.src))
	short := callee
	if idx := strings.LastIndex(callee, "."); idx >= 0 {
		short = callee[idx+1:]
	}

	isSQLAlchemy := short == "Column" || short == "mapped_column"
	isDjangoField := strings.HasSuffix(short, "Field") || short == "ForeignKey" || short == "OneToOneField" || short == "ManyToManyField"
	if !isSQLAlchemy && !isDjangoField {
		return codeir.DataModelField{}, false
	}

	argsNode := callNode.ChildByFieldName("arguments")
	_, kwArgs, _ := w.parsePyCallArgs(argsNode)

	fieldType := short
	if isSQLAlchemy {
		// 优先取首个非 keyword 参数类型（如 Integer / String(64)）或类型注解
		if firstArg := firstPositionalArgText(argsNode, w.src); firstArg != "" {
			fieldType = firstArg
		} else if typeAnn != nil {
			fieldType = strings.TrimSpace(typeAnn.Utf8Text(w.src))
		}
	}

	var constraints []string
	if strings.EqualFold(kwArgs["primary_key"], "True") {
		constraints = append(constraints, "PRIMARY KEY")
	}
	if strings.EqualFold(kwArgs["nullable"], "False") {
		constraints = append(constraints, "NOT NULL")
	}
	if strings.EqualFold(kwArgs["unique"], "True") {
		constraints = append(constraints, "UNIQUE")
	}
	if strings.EqualFold(kwArgs["index"], "True") {
		constraints = append(constraints, "INDEX")
	}

	return codeir.DataModelField{
		ColumnName:  colName,
		Type:        fieldType,
		Constraints: constraints,
	}, true
}

func firstPositionalArgText(argsNode *tree_sitter.Node, src []byte) string {
	if argsNode == nil {
		return ""
	}
	for i := uint(0); i < argsNode.ChildCount(); i++ {
		ch := argsNode.Child(i)
		k := ch.Kind()
		if k == "(" || k == ")" || k == "," || k == "keyword_argument" {
			continue
		}
		return strings.TrimSpace(ch.Utf8Text(src))
	}
	return ""
}

func (w *pythonWalker) extractDjangoMetaDBTable(metaBody *tree_sitter.Node) string {
	for i := uint(0); i < metaBody.ChildCount(); i++ {
		ch := metaBody.Child(i)
		if ch.Kind() != "expression_statement" {
			continue
		}
		for j := uint(0); j < ch.ChildCount(); j++ {
			assign := ch.Child(j)
			if assign.Kind() != "assignment" {
				continue
			}
			left := assign.ChildByFieldName("left")
			right := assign.ChildByFieldName("right")
			if left != nil && right != nil && strings.TrimSpace(left.Utf8Text(w.src)) == "db_table" {
				return unquotePyString(right.Utf8Text(w.src))
			}
		}
	}
	return ""
}

func (w *pythonWalker) extractPyBaseClasses(argList *tree_sitter.Node) []string {
	if argList == nil {
		return nil
	}
	var bases []string
	for i := uint(0); i < argList.ChildCount(); i++ {
		ch := argList.Child(i)
		k := ch.Kind()
		if k == "(" || k == ")" || k == "," {
			continue
		}
		txt := strings.TrimSpace(ch.Utf8Text(w.src))
		if txt != "" {
			bases = append(bases, txt)
		}
	}
	return bases
}

func (w *pythonWalker) extractPyParams(pNode *tree_sitter.Node) []string {
	var out []string
	for i := uint(0); i < pNode.ChildCount(); i++ {
		ch := pNode.Child(i)
		k := ch.Kind()
		if k == "(" || k == ")" || k == "," {
			continue
		}
		txt := strings.TrimSpace(ch.Utf8Text(w.src))
		if txt != "" && txt != "self" && txt != "cls" {
			out = append(out, txt)
		}
	}
	return out
}

func unquotePyString(raw string) string {
	s := strings.TrimSpace(raw)
	for _, prefix := range []string{"f", "r", "b", "u", "F", "R", "B", "U"} {
		if strings.HasPrefix(s, prefix+`"`) || strings.HasPrefix(s, prefix+`'`) {
			s = s[1:]
			break
		}
	}
	if len(s) >= 6 && (strings.HasPrefix(s, `"""`) && strings.HasSuffix(s, `"""`) ||
		strings.HasPrefix(s, `'''`) && strings.HasSuffix(s, `'''`)) {
		return s[3 : len(s)-3]
	}
	return unquoteLiteral(s)
}
