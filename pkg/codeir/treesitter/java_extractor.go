package treesitter

import (
	"context"
	"regexp"
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/security"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
)

// JavaExtractor 基于 tree-sitter-java（CGO 严格隔离）实现 Java 静态事实提取器（F-04 W2）：
// 1. 提取包名、类/接口/枚举/Record 及其方法/字段符号（含归一化内容指纹）；
// 2. 提取 Spring MVC / Boot 路由（类级 @RequestMapping 前缀 + 方法级 @GetMapping/@PostMapping/@PutMapping/@DeleteMapping/@PatchMapping/@RequestMapping 完整拼接）；
// 3. 提取 JPA @Entity/@Table ORM 实体字段与 MyBatis @Mapper SQL 映射表；
// 4. 提取 Spring @Scheduled 定时调度原语（标注 confidence="heuristic"）。
type JavaExtractor struct {
	redactor *security.SecretRedactor
}

func NewJavaExtractor() *JavaExtractor {
	return &JavaExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *JavaExtractor) Name() string { return "treesitter-java" }

func (e *JavaExtractor) SupportedLanguages() []string { return []string{"java"} }

func (e *JavaExtractor) ExtractFile(ctx context.Context, file codeir.FileEntry, content []byte) (*codeir.ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_java.Language())
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}

	tree := parser.ParseCtx(ctx, content, nil)
	if tree == nil {
		return &codeir.ExtractionResult{
			Diagnostics: []codeir.ParseDiagnostic{{
				FilePath: file.RelPath,
				Message:  "tree-sitter-java 解析返回空语法树",
			}},
		}, nil
	}
	defer tree.Close()

	w := &javaWalker{
		src:      content,
		filePath: file.RelPath,
		redactor: e.redactor,
		res:      &codeir.ExtractionResult{},
	}
	w.walk(tree.RootNode(), nil, "")
	return w.res, nil
}

type javaAnnotation struct {
	Name    string            // 简单注解名，如 RequestMapping / GetMapping / Table / Column / Scheduled
	Raw     string            // 原始文本
	Default string            // 位置单参数字符串值（如 @GetMapping("/invoices/{id}")）
	Attrs   map[string]string // 命名参数键值对（如 name -> billing_invoices, method -> RequestMethod.GET）
	Node    *tree_sitter.Node
}

type javaWalker struct {
	src      []byte
	filePath string
	pkgName  string
	redactor *security.SecretRedactor
	res      *codeir.ExtractionResult
}

func (w *javaWalker) walk(node *tree_sitter.Node, classStack []string, classRoutePrefix string) {
	if node == nil {
		return
	}

	switch node.Kind() {
	case "package_declaration":
		w.pkgName = w.extractPackageName(node)
		return

	case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
		w.handleTypeDecl(node, classStack)
		return

	case "method_declaration":
		w.handleMethodDecl(node, classStack, classRoutePrefix)
		return
	}

	for i := uint(0); i < node.ChildCount(); i++ {
		w.walk(node.Child(i), classStack, classRoutePrefix)
	}
}

func (w *javaWalker) extractPackageName(node *tree_sitter.Node) string {
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		if ch.Kind() == "scoped_identifier" || ch.Kind() == "identifier" {
			return strings.TrimSpace(ch.Utf8Text(w.src))
		}
	}
	return ""
}

func (w *javaWalker) fqName(parts ...string) string {
	all := make([]string, 0, len(parts)+1)
	if w.pkgName != "" {
		all = append(all, w.pkgName)
	}
	for _, p := range parts {
		if p != "" {
			all = append(all, p)
		}
	}
	return strings.Join(all, ".")
}

func (w *javaWalker) handleTypeDecl(node *tree_sitter.Node, classStack []string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	typeName := strings.TrimSpace(nameNode.Utf8Text(w.src))
	if typeName == "" {
		return
	}

	nextStack := append(append([]string(nil), classStack...), typeName)
	fqSym := w.fqName(nextStack...)

	anns, visibility := w.extractModifiers(node)
	kind := "class"
	switch node.Kind() {
	case "interface_declaration":
		kind = "interface"
	case "enum_declaration":
		kind = "enum"
	}

	baseTypes := w.extractJavaBaseTypes(node)
	bodyNode := node.ChildByFieldName("body")
	fields := w.extractJavaFields(bodyNode)

	span := nodeSpan(w.filePath, node)
	fp := ComputeNodeFingerprint(node, w.src)

	w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
		FQSymbol:   fqSym,
		Kind:       kind,
		Language:   "java",
		Signature:  strings.TrimSpace(kind + " " + typeName),
		Visibility: visibility,
		BaseTypes:  baseTypes,
		Fields:     fields,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceExact,
			Span:               span,
			ContentFingerprint: fp,
		},
	})

	// 1. 类级 Spring @RequestMapping 路由前缀
	classRoutePrefix := ""
	for _, a := range anns {
		if a.Name == "RequestMapping" {
			classRoutePrefix = a.firstPathValue()
		}
	}

	// 2. JPA @Entity / @Table ORM 实体提取
	isEntity := false
	tableName := ""
	for _, a := range anns {
		if a.Name == "Entity" {
			isEntity = true
		}
		if a.Name == "Table" {
			if tn := a.Attrs["name"]; tn != "" {
				tableName = tn
			} else if a.Default != "" {
				tableName = a.Default
			}
		}
	}
	if isEntity || tableName != "" {
		if tableName == "" {
			tableName = toSnakeCase(typeName)
		}
		dmFields := w.extractJPAEntityFields(bodyNode)
		w.res.DataModels = append(w.res.DataModels, codeir.DataModelFact{
			EntityName:        typeName,
			TableOrCollection: tableName,
			StorageKind:       "sql",
			Fields:            dmFields,
			Meta: codeir.FactMeta{
				Confidence:         codeir.ConfidenceExact,
				Span:               span,
				ContentFingerprint: fp,
			},
		})
	}

	// 3. MyBatis @Mapper 接口表引用提取
	if hasAnnotation(anns, "Mapper") || kind == "interface" {
		w.extractMyBatisTables(typeName, bodyNode, span, fp)
	}

	// 递归遍历类/接口体
	if bodyNode != nil {
		for i := uint(0); i < bodyNode.ChildCount(); i++ {
			w.walk(bodyNode.Child(i), nextStack, classRoutePrefix)
		}
	}
}

func (w *javaWalker) handleMethodDecl(node *tree_sitter.Node, classStack []string, classRoutePrefix string) {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	methodName := strings.TrimSpace(nameNode.Utf8Text(w.src))
	if methodName == "" {
		return
	}

	parts := append(append([]string(nil), classStack...), methodName)
	fqSym := w.fqName(parts...)
	anns, visibility := w.extractModifiers(node)

	retType := ""
	if rtNode := node.ChildByFieldName("type"); rtNode != nil {
		retType = strings.TrimSpace(rtNode.Utf8Text(w.src))
	}
	paramsText := "()"
	var paramNames []string
	if pNode := node.ChildByFieldName("parameters"); pNode != nil {
		paramsText = strings.TrimSpace(pNode.Utf8Text(w.src))
		paramNames = w.extractJavaParamList(pNode)
	}

	span := nodeSpan(w.filePath, node)
	fp := ComputeNodeFingerprint(node, w.src)

	w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
		FQSymbol:   fqSym,
		Kind:       "method",
		Language:   "java",
		Signature:  strings.TrimSpace(retType + " " + methodName + paramsText),
		Visibility: visibility,
		Meta: codeir.FactMeta{
			Confidence:         codeir.ConfidenceExact,
			Span:               span,
			ContentFingerprint: fp,
		},
	})

	// 1. Spring Web 路由注解提取（类前缀 + 方法路径拼接）
	for _, a := range anns {
		methods := springAnnotationHTTPMethods(a)
		if len(methods) == 0 {
			continue
		}
		methodPath := a.firstPathValue()
		fullPath := joinRoutePath(classRoutePrefix, methodPath)
		for _, m := range methods {
			w.res.HTTPRoutes = append(w.res.HTTPRoutes, codeir.HTTPRouteFact{
				Method:          m,
				Path:            fullPath,
				HandlerFQSymbol: fqSym,
				RequestParams:   paramNames,
				ResponseType:    retType,
				Framework:       "spring",
				Meta: codeir.FactMeta{
					Confidence:         codeir.ConfidenceExact,
					Span:               span,
					ContentFingerprint: fp,
				},
			})
		}
	}

	// 2. Spring @Scheduled 定时调度原语提取
	for _, a := range anns {
		if a.Name == "Scheduled" {
			snippet, _ := w.redactor.RedactText(strings.TrimSpace(a.Raw))
			if len(snippet) > 160 {
				snippet = snippet[:160]
			}
			aSpan := span
			if a.Node != nil {
				aSpan = nodeSpan(w.filePath, a.Node)
			}
			w.res.RuntimePrimitives = append(w.res.RuntimePrimitives, codeir.RuntimePrimitiveFact{
				Category:          "scheduler",
				APIName:           "@Scheduled",
				EnclosingFQSymbol: fqSym,
				ContextSnippet:    snippet,
				Meta: codeir.FactMeta{
					Confidence:         codeir.ConfidenceHeuristic,
					Span:               aSpan,
					ContentFingerprint: fp,
				},
			})
		}
	}
}

func (w *javaWalker) extractModifiers(node *tree_sitter.Node) ([]javaAnnotation, string) {
	var anns []javaAnnotation
	visibility := "package"
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		if ch.Kind() != "modifiers" {
			continue
		}
		for j := uint(0); j < ch.ChildCount(); j++ {
			m := ch.Child(j)
			mk := m.Kind()
			switch mk {
			case "public", "private", "protected":
				visibility = mk
			case "marker_annotation", "annotation":
				anns = append(anns, w.parseJavaAnnotation(m))
			}
		}
	}
	return anns, visibility
}

func (w *javaWalker) parseJavaAnnotation(node *tree_sitter.Node) javaAnnotation {
	a := javaAnnotation{
		Raw:   strings.TrimSpace(node.Utf8Text(w.src)),
		Attrs: make(map[string]string),
		Node:  node,
	}
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		rawName := strings.TrimSpace(nameNode.Utf8Text(w.src))
		if idx := strings.LastIndex(rawName, "."); idx >= 0 {
			rawName = rawName[idx+1:]
		}
		a.Name = rawName
	}

	argsNode := node.ChildByFieldName("arguments")
	if argsNode == nil {
		return a
	}

	for i := uint(0); i < argsNode.ChildCount(); i++ {
		ch := argsNode.Child(i)
		switch ch.Kind() {
		case "string_literal":
			if a.Default == "" {
				a.Default = unquoteLiteral(ch.Utf8Text(w.src))
			}
		case "element_value_pair":
			kNode := ch.ChildByFieldName("key")
			vNode := ch.ChildByFieldName("value")
			if kNode != nil && vNode != nil {
				key := strings.TrimSpace(kNode.Utf8Text(w.src))
				val := extractJavaAnnotationValue(vNode, w.src)
				a.Attrs[key] = val
			}
		case "element_value_array_initializer":
			if a.Default == "" {
				a.Default = extractJavaAnnotationValue(ch, w.src)
			}
		}
	}
	return a
}

func extractJavaAnnotationValue(node *tree_sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	if node.Kind() == "string_literal" {
		return unquoteLiteral(node.Utf8Text(src))
	}
	if node.Kind() == "element_value_array_initializer" {
		for i := uint(0); i < node.ChildCount(); i++ {
			ch := node.Child(i)
			if ch.Kind() == "string_literal" {
				return unquoteLiteral(ch.Utf8Text(src))
			}
			if ch.Kind() == "field_access" || ch.Kind() == "identifier" {
				return strings.TrimSpace(ch.Utf8Text(src))
			}
		}
	}
	return strings.TrimSpace(unquoteLiteral(node.Utf8Text(src)))
}

func (a javaAnnotation) firstPathValue() string {
	if v := a.Attrs["path"]; v != "" {
		return v
	}
	if v := a.Attrs["value"]; v != "" {
		return v
	}
	return a.Default
}

func springAnnotationHTTPMethods(a javaAnnotation) []string {
	switch a.Name {
	case "GetMapping":
		return []string{"GET"}
	case "PostMapping":
		return []string{"POST"}
	case "PutMapping":
		return []string{"PUT"}
	case "DeleteMapping":
		return []string{"DELETE"}
	case "PatchMapping":
		return []string{"PATCH"}
	case "RequestMapping":
		if m, ok := a.Attrs["method"]; ok && m != "" {
			upper := strings.ToUpper(m)
			for _, verb := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
				if strings.Contains(upper, verb) {
					return []string{verb}
				}
			}
		}
		return []string{"ANY"}
	default:
		return nil
	}
}

func (w *javaWalker) extractJavaBaseTypes(node *tree_sitter.Node) []string {
	var bases []string
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		switch ch.Kind() {
		case "superclass", "super_interfaces", "extends_interfaces":
			txt := strings.TrimSpace(ch.Utf8Text(w.src))
			txt = strings.TrimPrefix(txt, "extends")
			txt = strings.TrimPrefix(txt, "implements")
			for _, part := range strings.Split(txt, ",") {
				p := strings.TrimSpace(part)
				if p != "" {
					bases = append(bases, p)
				}
			}
		}
	}
	return bases
}

func (w *javaWalker) extractJavaFields(body *tree_sitter.Node) []codeir.SymbolField {
	if body == nil {
		return nil
	}
	var fields []codeir.SymbolField
	for i := uint(0); i < body.ChildCount(); i++ {
		ch := body.Child(i)
		if ch.Kind() != "field_declaration" {
			continue
		}
		_, vis := w.extractModifiers(ch)
		typ := ""
		if tNode := ch.ChildByFieldName("type"); tNode != nil {
			typ = strings.TrimSpace(tNode.Utf8Text(w.src))
		}
		for j := uint(0); j < ch.ChildCount(); j++ {
			dc := ch.Child(j)
			if dc.Kind() == "variable_declarator" {
				if nNode := dc.ChildByFieldName("name"); nNode != nil {
					fields = append(fields, codeir.SymbolField{
						Name:       strings.TrimSpace(nNode.Utf8Text(w.src)),
						Type:       typ,
						Visibility: vis,
					})
				}
			}
		}
	}
	return fields
}

func (w *javaWalker) extractJPAEntityFields(body *tree_sitter.Node) []codeir.DataModelField {
	if body == nil {
		return nil
	}
	var out []codeir.DataModelField
	for i := uint(0); i < body.ChildCount(); i++ {
		ch := body.Child(i)
		if ch.Kind() != "field_declaration" {
			continue
		}
		anns, _ := w.extractModifiers(ch)
		if hasAnnotation(anns, "Transient") {
			continue
		}
		typ := ""
		if tNode := ch.ChildByFieldName("type"); tNode != nil {
			typ = strings.TrimSpace(tNode.Utf8Text(w.src))
		}
		var colName string
		var constraints []string
		for _, a := range anns {
			switch a.Name {
			case "Id":
				constraints = append(constraints, "PRIMARY KEY")
			case "Column":
				if cn := a.Attrs["name"]; cn != "" {
					colName = cn
				}
				if strings.EqualFold(a.Attrs["nullable"], "false") {
					constraints = append(constraints, "NOT NULL")
				}
				if strings.EqualFold(a.Attrs["unique"], "true") {
					constraints = append(constraints, "UNIQUE")
				}
			case "JoinColumn":
				if cn := a.Attrs["name"]; cn != "" {
					colName = cn
				}
				constraints = append(constraints, "FOREIGN KEY")
			}
		}

		for j := uint(0); j < ch.ChildCount(); j++ {
			dc := ch.Child(j)
			if dc.Kind() != "variable_declarator" {
				continue
			}
			nNode := dc.ChildByFieldName("name")
			if nNode == nil {
				continue
			}
			fieldName := strings.TrimSpace(nNode.Utf8Text(w.src))
			cName := colName
			if cName == "" {
				cName = toSnakeCase(fieldName)
			}
			out = append(out, codeir.DataModelField{
				ColumnName:  cName,
				Type:        typ,
				Constraints: constraints,
			})
		}
	}
	return out
}

var reMyBatisSQLTable = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+` + "`?" + `([a-zA-Z_][a-zA-Z0-9_]*)` + "`?")

func (w *javaWalker) extractMyBatisTables(entityName string, body *tree_sitter.Node, span codeir.SourceSpan, fp string) {
	if body == nil {
		return
	}
	seenTables := make(map[string]bool)
	for i := uint(0); i < body.ChildCount(); i++ {
		ch := body.Child(i)
		if ch.Kind() != "method_declaration" {
			continue
		}
		anns, _ := w.extractModifiers(ch)
		for _, a := range anns {
			if a.Name != "Select" && a.Name != "Insert" && a.Name != "Update" && a.Name != "Delete" {
				continue
			}
			sqlText := a.firstPathValue()
			if sqlText == "" {
				sqlText = a.Raw
			}
			for _, m := range reMyBatisSQLTable.FindAllStringSubmatch(sqlText, -1) {
				if len(m) == 2 {
					tbl := strings.TrimSpace(m[1])
					if tbl != "" && !seenTables[tbl] {
						seenTables[tbl] = true
						w.res.DataModels = append(w.res.DataModels, codeir.DataModelFact{
							EntityName:        entityName,
							TableOrCollection: tbl,
							StorageKind:       "sql",
							Meta: codeir.FactMeta{
								Confidence:         codeir.ConfidenceExact,
								Span:               span,
								ContentFingerprint: fp,
							},
						})
					}
				}
			}
		}
	}
}

func (w *javaWalker) extractJavaParamList(pNode *tree_sitter.Node) []string {
	var params []string
	for i := uint(0); i < pNode.ChildCount(); i++ {
		ch := pNode.Child(i)
		if ch.Kind() != "formal_parameter" && ch.Kind() != "spread_parameter" {
			continue
		}
		tNode := ch.ChildByFieldName("type")
		nNode := ch.ChildByFieldName("name")
		if nNode != nil {
			entry := strings.TrimSpace(nNode.Utf8Text(w.src))
			if tNode != nil {
				entry = strings.TrimSpace(tNode.Utf8Text(w.src)) + " " + entry
			}
			params = append(params, entry)
		}
	}
	return params
}

func hasAnnotation(anns []javaAnnotation, name string) bool {
	for _, a := range anns {
		if a.Name == name {
			return true
		}
	}
	return false
}

func nodeSpan(filePath string, node *tree_sitter.Node) codeir.SourceSpan {
	return codeir.SourceSpan{
		FilePath:  filePath,
		LineStart: int(node.StartPosition().Row) + 1,
		LineEnd:   int(node.EndPosition().Row) + 1,
	}
}

func joinRoutePath(prefix, sub string) string {
	p := strings.TrimSpace(prefix)
	s := strings.TrimSpace(sub)
	if p == "" && s == "" {
		return "/"
	}
	p = strings.TrimRight(p, "/")
	if p != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if s == "" {
		if p == "" {
			return "/"
		}
		return p
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	joined := p + s
	for strings.Contains(joined, "//") {
		joined = strings.ReplaceAll(joined, "//", "/")
	}
	return joined
}

func unquoteLiteral(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func toSnakeCase(s string) string {
	var out strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r + ('a' - 'A'))
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
