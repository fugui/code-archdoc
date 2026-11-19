package treesitter

import (
	"context"
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/security"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
)

// CppExtractor 基于 tree-sitter-cpp（CGO 严格隔离）实现 C++ 容错 CST 提取器（ADR-02）。
// 特性：
// 1. 完整还原 namespace::Class::Method 全限定名；
// 2. 针对 API_EXPORT / CROS_RPC_METHOD 等未展开导出宏产生的 ERROR 节点（含外层误判为 function_definition 的宏修饰类）执行局部下钻恢复；
// 3. 维护 inPreprocBranch 栈对 #ifdef/#if/#elif/#else 子树无求值标注 conditional=true；
// 4. 区分函数体内局部变量初始化（防 C++ Most Vexing Parse 误报）与类/全局函数声明；
// 5. 计算归一化符号体 content_fingerprint 与并发/锁/共享内存/错误码启发式原语。
type CppExtractor struct {
	redactor *security.SecretRedactor
}

func NewCppExtractor() *CppExtractor {
	return &CppExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *CppExtractor) Name() string { return "treesitter-cpp" }

func (e *CppExtractor) SupportedLanguages() []string { return []string{"cpp"} }

func (e *CppExtractor) ExtractFile(ctx context.Context, file codeir.FileEntry, content []byte) (*codeir.ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_cpp.Language())
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}

	tree := parser.ParseCtx(ctx, content, nil)
	if tree == nil {
		return &codeir.ExtractionResult{
			Diagnostics: []codeir.ParseDiagnostic{{
				FilePath: file.RelPath,
				Message:  "tree-sitter-cpp 解析返回空语法树",
			}},
		}, nil
	}
	defer tree.Close()

	w := &cppWalker{
		src:      content,
		filePath: file.RelPath,
		langName: "cpp",
		redactor: e.redactor,
		res:      &codeir.ExtractionResult{},
	}
	w.walk(tree.RootNode(), walkState{})
	return w.res, nil
}

type walkState struct {
	scopes         []string
	preprocStack   []string
	enclosingFQ    string
	visibility     string
	inClassScope   bool
	inFunctionBody bool
}

type cppWalker struct {
	src      []byte
	filePath string
	langName string
	redactor *security.SecretRedactor
	res      *codeir.ExtractionResult
}

func (w *cppWalker) walk(node *tree_sitter.Node, st walkState) {
	if node == nil {
		return
	}
	kind := node.Kind()
	cond := len(st.preprocStack) > 0
	condExpr := strings.Join(st.preprocStack, " && ")

	// 1. 记录语法容错诊断但不阻断，同时对 ERROR 节点执行局部下钻恢复
	if kind == "ERROR" {
		w.res.Diagnostics = append(w.res.Diagnostics, codeir.ParseDiagnostic{
			FilePath: w.filePath,
			Line:     int(node.StartPosition().Row) + 1,
			Message:  "CST 局部容错恢复 (ERROR 节点下钻)",
		})
		w.recoverErrorNode(node, st)
		return
	}

	// 2. 条件编译栈维护（不求值，仅无损记录条件表达式）
	switch kind {
	case "preproc_ifdef", "preproc_if", "preproc_elif", "preproc_else":
		expr := extractPreprocCondition(node, w.src)
		nextSt := st
		if !isHeaderIncludeGuard(node, w.src, expr) {
			if expr == "" {
				expr = kind
			}
			nextSt.preprocStack = append(cloneSlice(st.preprocStack), expr)
		}
		for i := uint(0); i < node.ChildCount(); i++ {
			child := node.Child(i)
			ck := child.Kind()
			if ck == "#ifdef" || ck == "#ifndef" || ck == "#if" || ck == "#elif" || ck == "#else" || ck == "#endif" {
				continue
			}
			if child == node.ChildByFieldName("name") || child == node.ChildByFieldName("condition") {
				continue
			}
			w.walk(child, nextSt)
		}
		return
	}

	// 3. 并发/资源/错误码原语启发式抽取
	extractPrimitivesFromNode(node, w.src, w.filePath, st.enclosingFQ, cond, condExpr, w.redactor, &w.res.RuntimePrimitives)

	// 4. 作用域与符号提取
	switch kind {
	case "namespace_definition":
		nsName := ""
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			nsName = strings.TrimSpace(nameNode.Utf8Text(w.src))
		}
		nextScopes := st.scopes
		if nsName != "" {
			for _, part := range strings.Split(nsName, "::") {
				if p := strings.TrimSpace(part); p != "" {
					nextScopes = append(cloneSlice(nextScopes), p)
				}
			}
		}
		if body := node.ChildByFieldName("body"); body != nil {
			nextSt := st
			nextSt.scopes = nextScopes
			nextSt.enclosingFQ = strings.Join(nextScopes, "::")
			w.walkChildren(body, nextSt)
		}
		return

	case "class_specifier", "struct_specifier":
		symKind := "class"
		defaultVis := "private"
		if kind == "struct_specifier" {
			symKind = "struct"
			defaultVis = "public"
		}
		nameNode := node.ChildByFieldName("name")
		bodyNode := node.ChildByFieldName("body")
		if nameNode != nil && bodyNode != nil {
			typeName := strings.TrimSpace(nameNode.Utf8Text(w.src))
			fq := joinScope(st.scopes, typeName)
			baseTypes := extractBaseTypes(node, w.src)
			fields := extractMemberFields(bodyNode, w.src, defaultVis)

			w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
				FQSymbol:          fq,
				Kind:              symKind,
				Language:          w.langName,
				Signature:         symKind + " " + fq,
				Visibility:        st.visibility,
				BaseTypes:         baseTypes,
				Fields:            fields,
				DocCommentSummary: w.leadingComment(node),
				Meta: codeir.FactMeta{
					Confidence: codeir.ConfidenceExact,
					Span: codeir.SourceSpan{
						FilePath:      w.filePath,
						LineStart:     int(node.StartPosition().Row) + 1,
						LineEnd:       int(node.EndPosition().Row) + 1,
						Conditional:   cond,
						ConditionExpr: condExpr,
					},
					ContentFingerprint: ComputeNodeFingerprint(node, w.src),
				},
			})

			nextSt := st
			nextSt.scopes = append(cloneSlice(st.scopes), typeName)
			nextSt.enclosingFQ = fq
			nextSt.visibility = defaultVis
			nextSt.inClassScope = true
			nextSt.inFunctionBody = false
			w.walkClassBody(bodyNode, nextSt)
			return
		}

	case "enum_specifier":
		nameNode := node.ChildByFieldName("name")
		bodyNode := node.ChildByFieldName("body")
		if nameNode != nil && bodyNode != nil {
			enumName := strings.TrimSpace(nameNode.Utf8Text(w.src))
			fq := joinScope(st.scopes, enumName)
			w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
				FQSymbol:          fq,
				Kind:              "enum",
				Language:          w.langName,
				Signature:         "enum " + fq,
				Visibility:        st.visibility,
				DocCommentSummary: w.leadingComment(node),
				Meta: codeir.FactMeta{
					Confidence: codeir.ConfidenceExact,
					Span: codeir.SourceSpan{
						FilePath:      w.filePath,
						LineStart:     int(node.StartPosition().Row) + 1,
						LineEnd:       int(node.EndPosition().Row) + 1,
						Conditional:   cond,
						ConditionExpr: condExpr,
					},
					ContentFingerprint: ComputeNodeFingerprint(node, w.src),
				},
			})
			return
		}

	case "function_definition":
		// 容错恢复：导出宏修饰的 class/struct（如 API_EXPORT class OrderService { ... };）
		// 会被 tree-sitter-cpp 解析为 function_definition(type=API_EXPORT, ERROR("class"), declarator=OrderService, body=compound_statement)
		if recovered := w.tryRecoverMacroDecoratedClass(node, st); recovered {
			return
		}

		if sym, ok := w.extractFunctionNode(node, st); ok {
			w.res.Symbols = append(w.res.Symbols, sym)
			if body := node.ChildByFieldName("body"); body != nil {
				bodySt := st
				bodySt.enclosingFQ = sym.FQSymbol
				bodySt.inFunctionBody = true
				w.walkChildren(body, bodySt)
			}
			return
		}

	case "declaration", "field_declaration":
		if hasDirectChildKind(node, "class_specifier") || hasDirectChildKind(node, "struct_specifier") || hasDirectChildKind(node, "enum_specifier") {
			w.walkChildren(node, st)
			return
		}
		// 仅在非函数体作用域（全局/命名空间/类体）中提取函数声明，彻底规避 C++ Most Vexing Parse 将局部 lock_guard 误判为函数
		if !st.inFunctionBody && findFunctionDeclarator(node) != nil {
			if sym, ok := w.extractFunctionNode(node, st); ok {
				w.res.Symbols = append(w.res.Symbols, sym)
			}
			// 继续扫描类型子节点以提取原语（如参数或返回类型中的原语）
			if typeNode := node.ChildByFieldName("type"); typeNode != nil {
				w.walk(typeNode, st)
			}
			return
		}
	}

	w.walkChildren(node, st)
}

// tryRecoverMacroDecoratedClass 检测并恢复被导出宏修饰的 class/struct 定义（如 API_EXPORT class OrderService { ... };）。
func (w *cppWalker) tryRecoverMacroDecoratedClass(node *tree_sitter.Node, st walkState) bool {
	if findFunctionDeclarator(node) != nil {
		return false
	}
	var classKind string
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		if ch.Kind() == "ERROR" {
			errTxt := strings.TrimSpace(ch.Utf8Text(w.src))
			if errTxt == "class" || strings.HasSuffix(errTxt, " class") {
				classKind = "class"
				break
			}
			if errTxt == "struct" || strings.HasSuffix(errTxt, " struct") {
				classKind = "struct"
				break
			}
		}
	}
	if classKind == "" {
		return false
	}

	declNode := node.ChildByFieldName("declarator")
	bodyNode := node.ChildByFieldName("body")
	if declNode == nil || bodyNode == nil {
		return false
	}
	className := strings.TrimSpace(declNode.Utf8Text(w.src))
	if className == "" || strings.ContainsAny(className, " ()") {
		return false
	}

	defaultVis := "private"
	if classKind == "struct" {
		defaultVis = "public"
	}
	cond := len(st.preprocStack) > 0
	condExpr := strings.Join(st.preprocStack, " && ")
	fq := joinScope(st.scopes, className)

	w.res.Diagnostics = append(w.res.Diagnostics, codeir.ParseDiagnostic{
		FilePath: w.filePath,
		Line:     int(node.StartPosition().Row) + 1,
		Message:  "导出宏修饰类容错恢复: " + fq,
	})

	w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
		FQSymbol:          fq,
		Kind:              classKind,
		Language:          w.langName,
		Signature:         classKind + " " + fq,
		Visibility:        st.visibility,
		DocCommentSummary: w.leadingComment(node),
		Meta: codeir.FactMeta{
			Confidence: codeir.ConfidenceExact,
			Span: codeir.SourceSpan{
				FilePath:      w.filePath,
				LineStart:     int(node.StartPosition().Row) + 1,
				LineEnd:       int(node.EndPosition().Row) + 1,
				Conditional:   cond,
				ConditionExpr: condExpr,
			},
			ContentFingerprint: ComputeNodeFingerprint(node, w.src),
		},
	})

	nextSt := st
	nextSt.scopes = append(cloneSlice(st.scopes), className)
	nextSt.enclosingFQ = fq
	nextSt.visibility = defaultVis
	nextSt.inClassScope = true
	nextSt.inFunctionBody = false
	w.walkRecoveredClassCompound(bodyNode, nextSt)
	return true
}

// walkRecoveredClassCompound 遍历容错恢复的类体 compound_statement（处理 labeled_statement 形式的 public:/private: 标号）。
func (w *cppWalker) walkRecoveredClassCompound(body *tree_sitter.Node, st walkState) {
	curSt := st
	for i := uint(0); i < body.ChildCount(); i++ {
		child := body.Child(i)
		if child.Kind() == "labeled_statement" {
			if lbl := child.ChildByFieldName("label"); lbl != nil {
				vis := strings.TrimSpace(lbl.Utf8Text(w.src))
				if vis == "public" || vis == "protected" || vis == "private" {
					curSt.visibility = vis
				}
			}
			for j := uint(0); j < child.ChildCount(); j++ {
				sub := child.Child(j)
				if sub == child.ChildByFieldName("label") || sub.Kind() == ":" {
					continue
				}
				w.walk(sub, curSt)
			}
			continue
		}
		w.walk(child, curSt)
	}
}

func (w *cppWalker) walkClassBody(body *tree_sitter.Node, st walkState) {
	curSt := st
	for i := uint(0); i < body.ChildCount(); i++ {
		child := body.Child(i)
		if child.Kind() == "access_specifier" {
			v := strings.TrimSuffix(strings.TrimSpace(child.Utf8Text(w.src)), ":")
			if v == "public" || v == "protected" || v == "private" {
				curSt.visibility = v
			}
			continue
		}
		w.walk(child, curSt)
	}
}

func (w *cppWalker) walkChildren(node *tree_sitter.Node, st walkState) {
	for i := uint(0); i < node.ChildCount(); i++ {
		w.walk(node.Child(i), st)
	}
}

// recoverErrorNode 局部下钻恢复因导出宏导致的 CST ERROR 节点。
func (w *cppWalker) recoverErrorNode(node *tree_sitter.Node, st walkState) {
	cond := len(st.preprocStack) > 0
	condExpr := strings.Join(st.preprocStack, " && ")

	recoveredStructured := false
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		ck := ch.Kind()
		if ck == "class_specifier" || ck == "struct_specifier" || ck == "enum_specifier" ||
			ck == "function_definition" || ck == "declaration" || ck == "field_declaration" {
			w.walk(ch, st)
			recoveredStructured = true
		}
	}
	if recoveredStructured {
		return
	}

	var pendingClassKind string
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		ck := ch.Kind()
		txt := strings.TrimSpace(ch.Utf8Text(w.src))
		if txt == "class" || txt == "struct" {
			pendingClassKind = txt
			continue
		}
		if pendingClassKind != "" && (ck == "type_identifier" || ck == "identifier") {
			for j := i + 1; j < node.ChildCount(); j++ {
				cand := node.Child(j)
				if cand.Kind() == "field_declaration_list" {
					defaultVis := "private"
					if pendingClassKind == "struct" {
						defaultVis = "public"
					}
					fq := joinScope(st.scopes, txt)
					w.res.Symbols = append(w.res.Symbols, codeir.SymbolFact{
						FQSymbol:   fq,
						Kind:       pendingClassKind,
						Language:   w.langName,
						Signature:  pendingClassKind + " " + fq,
						Visibility: st.visibility,
						Fields:     extractMemberFields(cand, w.src, defaultVis),
						Meta: codeir.FactMeta{
							Confidence: codeir.ConfidenceExact,
							Span: codeir.SourceSpan{
								FilePath:      w.filePath,
								LineStart:     int(node.StartPosition().Row) + 1,
								LineEnd:       int(node.EndPosition().Row) + 1,
								Conditional:   cond,
								ConditionExpr: condExpr,
							},
							ContentFingerprint: ComputeNodeFingerprint(node, w.src),
						},
					})
					nextSt := st
					nextSt.scopes = append(cloneSlice(st.scopes), txt)
					nextSt.enclosingFQ = fq
					nextSt.visibility = defaultVis
					nextSt.inClassScope = true
					nextSt.inFunctionBody = false
					w.walkClassBody(cand, nextSt)
					return
				}
			}
			pendingClassKind = ""
		}
	}

	if !st.inFunctionBody {
		if fnDecl := findFunctionDeclarator(node); fnDecl != nil {
			if sym, ok := w.extractFunctionNode(node, st); ok {
				w.res.Symbols = append(w.res.Symbols, sym)
				for i := uint(0); i < node.ChildCount(); i++ {
					if node.Child(i).Kind() == "compound_statement" {
						bodySt := st
						bodySt.enclosingFQ = sym.FQSymbol
						bodySt.inFunctionBody = true
						w.walkChildren(node.Child(i), bodySt)
					}
				}
				return
			}
		}
	}

	w.walkChildren(node, st)
}

func (w *cppWalker) extractFunctionNode(node *tree_sitter.Node, st walkState) (codeir.SymbolFact, bool) {
	fnDecl := findFunctionDeclarator(node)
	if fnDecl == nil {
		return codeir.SymbolFact{}, false
	}

	declNameNode := fnDecl.ChildByFieldName("declarator")
	if declNameNode == nil {
		return codeir.SymbolFact{}, false
	}
	rawName := strings.TrimSpace(declNameNode.Utf8Text(w.src))
	if rawName == "" {
		return codeir.SymbolFact{}, false
	}
	// 处理宏修饰自定义返回类型时 declarator 混入前置类型名（如 "OrderErrorCode CreateOrder" -> "CreateOrder"）
	if fields := strings.Fields(rawName); len(fields) > 1 {
		rawName = fields[len(fields)-1]
	}
	rawName = strings.TrimLeft(rawName, "*&")
	if strings.HasPrefix(rawName, "~") || strings.Contains(rawName, "::~") {
		return codeir.SymbolFact{}, false
	}
	if isConstructorName(rawName, st.scopes) {
		return codeir.SymbolFact{}, false
	}

	fq := qualifyCallableName(st.scopes, rawName)
	symKind := "function"
	if strings.Contains(rawName, "::") || st.inClassScope || isInsideClassScope(node) {
		symKind = "method"
	}

	sig := buildCallableSignature(node, fnDecl, w.src)
	cond := len(st.preprocStack) > 0
	condExpr := strings.Join(st.preprocStack, " && ")

	return codeir.SymbolFact{
		FQSymbol:          fq,
		Kind:              symKind,
		Language:          w.langName,
		Signature:         sig,
		Visibility:        st.visibility,
		DocCommentSummary: w.leadingComment(node),
		Meta: codeir.FactMeta{
			Confidence: codeir.ConfidenceExact,
			Span: codeir.SourceSpan{
				FilePath:      w.filePath,
				LineStart:     int(node.StartPosition().Row) + 1,
				LineEnd:       int(node.EndPosition().Row) + 1,
				Conditional:   cond,
				ConditionExpr: condExpr,
			},
			ContentFingerprint: ComputeNodeFingerprint(node, w.src),
		},
	}, true
}

func (w *cppWalker) leadingComment(node *tree_sitter.Node) string {
	if node == nil {
		return ""
	}
	prev := node.PrevSibling()
	if prev == nil || prev.Kind() != "comment" {
		return ""
	}
	if int(node.StartPosition().Row)-int(prev.EndPosition().Row) > 1 {
		return ""
	}
	raw := strings.TrimSpace(prev.Utf8Text(w.src))
	raw = strings.TrimPrefix(raw, "//")
	raw = strings.TrimPrefix(raw, "/*")
	raw = strings.TrimSuffix(raw, "*/")
	raw = cleanWhitespace(raw)
	if w.redactor != nil {
		raw, _ = w.redactor.RedactText(raw)
	}
	if len([]rune(raw)) > 160 {
		raw = string([]rune(raw)[:160])
	}
	return raw
}

func findFunctionDeclarator(node *tree_sitter.Node) *tree_sitter.Node {
	if node == nil {
		return nil
	}
	if node.Kind() == "function_declarator" {
		return node
	}
	for i := uint(0); i < node.ChildCount(); i++ {
		ch := node.Child(i)
		ck := ch.Kind()
		if ck == "compound_statement" || ck == "field_declaration_list" || ck == "parameter_list" {
			continue
		}
		if found := findFunctionDeclarator(ch); found != nil {
			return found
		}
	}
	return nil
}

func hasDirectChildKind(node *tree_sitter.Node, kind string) bool {
	for i := uint(0); i < node.ChildCount(); i++ {
		if node.Child(i).Kind() == kind {
			return true
		}
	}
	return false
}

func isInsideClassScope(node *tree_sitter.Node) bool {
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "class_specifier" || p.Kind() == "struct_specifier" || p.Kind() == "field_declaration_list" {
			return true
		}
	}
	return false
}

func isConstructorName(rawName string, scopes []string) bool {
	if strings.Contains(rawName, "::") {
		parts := strings.Split(rawName, "::")
		if len(parts) >= 2 && parts[len(parts)-1] == parts[len(parts)-2] {
			return true
		}
	}
	if len(scopes) > 0 && rawName == scopes[len(scopes)-1] {
		return true
	}
	return false
}

func qualifyCallableName(scopes []string, rawName string) string {
	rawName = strings.TrimPrefix(rawName, "::")
	if len(scopes) == 0 {
		return rawName
	}
	prefix := strings.Join(scopes, "::")
	if strings.HasPrefix(rawName, prefix+"::") {
		return rawName
	}
	return prefix + "::" + rawName
}

func joinScope(scopes []string, name string) string {
	if len(scopes) == 0 {
		return name
	}
	return strings.Join(scopes, "::") + "::" + name
}

func buildCallableSignature(node, fnDecl *tree_sitter.Node, src []byte) string {
	startByte := node.StartByte()
	endByte := fnDecl.EndByte()
	if endByte > startByte && endByte <= uint(len(src)) {
		return cleanWhitespace(string(src[startByte:endByte]))
	}
	return cleanWhitespace(fnDecl.Utf8Text(src))
}

func extractBaseTypes(classNode *tree_sitter.Node, src []byte) []string {
	var bases []string
	for i := uint(0); i < classNode.ChildCount(); i++ {
		ch := classNode.Child(i)
		if ch.Kind() == "base_class_clause" {
			for j := uint(0); j < ch.ChildCount(); j++ {
				bc := ch.Child(j)
				if bc.Kind() == "type_identifier" || bc.Kind() == "qualified_identifier" {
					bases = append(bases, strings.TrimSpace(bc.Utf8Text(src)))
				}
			}
		}
	}
	return bases
}

func extractMemberFields(bodyNode *tree_sitter.Node, src []byte, defaultVis string) []codeir.SymbolField {
	var fields []codeir.SymbolField
	curVis := defaultVis
	for i := uint(0); i < bodyNode.ChildCount(); i++ {
		ch := bodyNode.Child(i)
		if ch.Kind() == "access_specifier" {
			v := strings.TrimSuffix(strings.TrimSpace(ch.Utf8Text(src)), ":")
			if v == "public" || v == "protected" || v == "private" {
				curVis = v
			}
			continue
		}
		if ch.Kind() != "field_declaration" {
			continue
		}
		if findFunctionDeclarator(ch) != nil {
			continue
		}
		typeNode := ch.ChildByFieldName("type")
		declNode := ch.ChildByFieldName("declarator")
		if typeNode != nil && declNode != nil {
			fields = append(fields, codeir.SymbolField{
				Name:       cleanWhitespace(declNode.Utf8Text(src)),
				Type:       cleanWhitespace(typeNode.Utf8Text(src)),
				Visibility: curVis,
			})
		}
	}
	return fields
}

func extractPreprocCondition(node *tree_sitter.Node, src []byte) string {
	if condNode := node.ChildByFieldName("condition"); condNode != nil {
		return cleanWhitespace(condNode.Utf8Text(src))
	}
	if nameNode := node.ChildByFieldName("name"); nameNode != nil {
		return cleanWhitespace(nameNode.Utf8Text(src))
	}
	return ""
}

func isHeaderIncludeGuard(node *tree_sitter.Node, src []byte, expr string) bool {
	if node == nil || expr == "" {
		return false
	}
	if node.Kind() != "preproc_ifdef" {
		return false
	}
	if node.Parent() == nil || node.Parent().Kind() != "translation_unit" {
		return false
	}
	if node.ChildCount() == 0 || node.Child(0).Utf8Text(src) != "#ifndef" {
		return false
	}
	up := strings.ToUpper(expr)
	return strings.HasSuffix(up, "_H") || strings.HasSuffix(up, "_HPP") || strings.HasSuffix(up, "_H_") || strings.HasSuffix(up, "_INCLUDED")
}

func cloneSlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	return cp
}
