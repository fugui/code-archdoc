package treesitter

import (
	"regexp"
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/security"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type primitiveRule struct {
	category string
	apiName  string
}

// standardPrimitiveMap 标准 C/C++ 并发、锁、共享内存 IPC、事件循环、定时器与信号原语白名单
var standardPrimitiveMap = map[string]primitiveRule{
	// thread
	"std::thread":    {category: "thread", apiName: "std::thread"},
	"std::jthread":   {category: "thread", apiName: "std::jthread"},
	"std::async":     {category: "thread", apiName: "std::async"},
	"pthread_create": {category: "thread", apiName: "pthread_create"},
	"pthread_join":   {category: "thread", apiName: "pthread_join"},
	"pthread_detach": {category: "thread", apiName: "pthread_detach"},

	// lock & atomic
	"std::mutex":              {category: "lock", apiName: "std::mutex"},
	"std::shared_mutex":       {category: "lock", apiName: "std::shared_mutex"},
	"std::recursive_mutex":    {category: "lock", apiName: "std::recursive_mutex"},
	"std::timed_mutex":        {category: "lock", apiName: "std::timed_mutex"},
	"std::lock_guard":         {category: "lock", apiName: "std::lock_guard"},
	"std::unique_lock":        {category: "lock", apiName: "std::unique_lock"},
	"std::shared_lock":        {category: "lock", apiName: "std::shared_lock"},
	"std::scoped_lock":        {category: "lock", apiName: "std::scoped_lock"},
	"std::condition_variable": {category: "lock", apiName: "std::condition_variable"},
	"std::atomic":             {category: "lock", apiName: "std::atomic"},
	"pthread_mutex_t":         {category: "lock", apiName: "pthread_mutex_t"},
	"pthread_mutex_init":      {category: "lock", apiName: "pthread_mutex_init"},
	"pthread_mutex_lock":      {category: "lock", apiName: "pthread_mutex_lock"},
	"pthread_mutex_unlock":    {category: "lock", apiName: "pthread_mutex_unlock"},
	"pthread_rwlock_t":        {category: "lock", apiName: "pthread_rwlock_t"},
	"pthread_cond_wait":       {category: "lock", apiName: "pthread_cond_wait"},

	// shm_ipc
	"shm_open":   {category: "shm_ipc", apiName: "shm_open"},
	"shm_unlink": {category: "shm_ipc", apiName: "shm_unlink"},
	"shmget":     {category: "shm_ipc", apiName: "shmget"},
	"shmat":      {category: "shm_ipc", apiName: "shmat"},
	"mmap":       {category: "shm_ipc", apiName: "mmap"},
	"munmap":     {category: "shm_ipc", apiName: "munmap"},
	"mq_open":    {category: "shm_ipc", apiName: "mq_open"},
	"socketpair": {category: "shm_ipc", apiName: "socketpair"},

	// event_loop
	"epoll_create":  {category: "event_loop", apiName: "epoll_create"},
	"epoll_create1": {category: "event_loop", apiName: "epoll_create1"},
	"epoll_ctl":     {category: "event_loop", apiName: "epoll_ctl"},
	"epoll_wait":    {category: "event_loop", apiName: "epoll_wait"},
	"kqueue":        {category: "event_loop", apiName: "kqueue"},

	// timer
	"timerfd_create":  {category: "timer", apiName: "timerfd_create"},
	"timerfd_settime": {category: "timer", apiName: "timerfd_settime"},
	"timer_create":    {category: "timer", apiName: "timer_create"},
	"setitimer":       {category: "timer", apiName: "setitimer"},

	// signal
	"sigaction":   {category: "signal", apiName: "sigaction"},
	"signal":      {category: "signal", apiName: "signal"},
	"sigprocmask": {category: "signal", apiName: "sigprocmask"},
}

var (
	reErrorMacroName = regexp.MustCompile(`^(ERR_|ERRNO_|ECODE_|ERROR_)[A-Z0-9_]+$`)
	reErrorEnumName  = regexp.MustCompile(`(?i)(Error|ErrCode|ErrNo|ErrorCode)$`)
)

// extractPrimitivesFromNode 在当前 CST 节点及外层作用域上下文中启发式抽取并发/资源/错误码原语。
func extractPrimitivesFromNode(
	node *tree_sitter.Node,
	src []byte,
	filePath string,
	enclosingFQ string,
	cond bool,
	condExpr string,
	redactor *security.SecretRedactor,
	out *[]codeir.RuntimePrimitiveFact,
) {
	if node == nil {
		return
	}
	kind := node.Kind()

	switch kind {
	case "call_expression":
		fnNode := node.ChildByFieldName("function")
		if fnNode != nil {
			fnText := cleanWhitespace(fnNode.Utf8Text(src))
			if rule, ok := matchPrimitiveToken(fnText); ok {
				*out = append(*out, buildPrimitiveFact(rule, filePath, enclosingFQ, node, src, cond, condExpr, redactor))
			}
		}
	case "qualified_identifier", "template_type", "type_identifier", "primitive_type":
		txt := cleanWhitespace(node.Utf8Text(src))
		// 去除模板参数 <...> 以匹配 std::lock_guard<std::mutex> -> std::lock_guard
		baseType := stripTemplateArgs(txt)
		if rule, ok := matchPrimitiveToken(baseType); ok {
			// 若父节点是 template_type 且当前是其子节点 qualified_identifier，则避免在父子两层重复发射
			if kind == "qualified_identifier" && node.Parent() != nil && node.Parent().Kind() == "template_type" {
				return
			}
			*out = append(*out, buildPrimitiveFact(rule, filePath, enclosingFQ, node, src, cond, condExpr, redactor))
		}
	case "preproc_def":
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			macroName := strings.TrimSpace(nameNode.Utf8Text(src))
			if reErrorMacroName.MatchString(macroName) {
				*out = append(*out, buildPrimitiveFact(
					primitiveRule{category: "error_code", apiName: macroName},
					filePath, enclosingFQ, node, src, cond, condExpr, redactor,
				))
			}
		}
	case "enum_specifier":
		nameNode := node.ChildByFieldName("name")
		if nameNode != nil {
			enumName := strings.TrimSpace(nameNode.Utf8Text(src))
			if reErrorEnumName.MatchString(enumName) {
				*out = append(*out, buildPrimitiveFact(
					primitiveRule{category: "error_code", apiName: enumName},
					filePath, enclosingFQ, node, src, cond, condExpr, redactor,
				))
			}
		}
	}
}

func matchPrimitiveToken(raw string) (primitiveRule, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return primitiveRule{}, false
	}
	if r, ok := standardPrimitiveMap[trimmed]; ok {
		return r, true
	}
	// 处理 ::std::mutex 等前导全局作用域符号
	trimmed = strings.TrimPrefix(trimmed, "::")
	if r, ok := standardPrimitiveMap[trimmed]; ok {
		return r, true
	}
	return primitiveRule{}, false
}

func stripTemplateArgs(s string) string {
	if idx := strings.Index(s, "<"); idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

func buildPrimitiveFact(
	rule primitiveRule,
	filePath string,
	enclosingFQ string,
	node *tree_sitter.Node,
	src []byte,
	cond bool,
	condExpr string,
	redactor *security.SecretRedactor,
) codeir.RuntimePrimitiveFact {
	startLine := int(node.StartPosition().Row) + 1
	endLine := int(node.EndPosition().Row) + 1
	snippet := cleanWhitespace(node.Utf8Text(src))
	if redactor != nil {
		snippet, _ = redactor.RedactText(snippet)
	}
	if len([]rune(snippet)) > 160 {
		snippet = string([]rune(snippet)[:160])
	}
	return codeir.RuntimePrimitiveFact{
		Category:          rule.category,
		APIName:           rule.apiName,
		EnclosingFQSymbol: enclosingFQ,
		ContextSnippet:    snippet,
		Meta: codeir.FactMeta{
			Confidence: codeir.ConfidenceHeuristic,
			Span: codeir.SourceSpan{
				FilePath:      filePath,
				LineStart:     startLine,
				LineEnd:       endLine,
				Conditional:   cond,
				ConditionExpr: condExpr,
			},
			ContentFingerprint: ComputeNodeFingerprint(node, src),
		},
	}
}

func cleanWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
