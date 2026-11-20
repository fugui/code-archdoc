package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/security"
)

const (
	ToolNameListDir       = "list_dir"
	ToolNameReadFileLines = "read_file_lines"
	ToolNameGrepInSandbox = "grep_in_sandbox"
	ToolNameGetSymbolImpl = "get_symbol_impl"

	defaultToolMaxReadLines   = 200
	hardToolMaxReadLines      = 250
	defaultToolMaxOutputBytes = 12000
	hardToolMaxOutputBytes    = 16000
	defaultToolMaxGrepMatches = 25
	hardToolMaxGrepMatches    = 50
	defaultToolMaxGrepFiles   = 600
	defaultToolMaxListEntries = 60
	hardToolMaxListEntries    = 100
	defaultToolMaxFileBytes   = 1048576 // 1MB
	toolBinarySniffBytes      = 512
)

// ToolExecutor 抽象只读工具集定义与执行接口（供 RunToolLoop / RunToolLoopStream 调用）。
type ToolExecutor interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, call ToolCall) (output string, summary string, isError bool)
}

// SandboxToolkitConfig 配置受限只读源码工具集的安全边界与配额。
type SandboxToolkitConfig struct {
	// ScopeRoot 沙箱分析物理根目录（repoRoot 或 repoRoot/subdir，须已通过 NormalizeSubdir/SafeJoin 校验）。
	// 若为空，则不暴露文件系统工具，仅在提供 IR 时保留符号元数据定位能力。
	ScopeRoot string
	// RepoRoot 可选：用于加载仓库根目录 .archdocignore（Layer 2）；为空时默认同 ScopeRoot。
	RepoRoot string
	// IR 可选：L1 Code IR 事实包，供 get_symbol_impl 精确直达符号源码坐标。
	IR *codeir.CodeIRBundle
	// ExternalFileWhitelist 可选：module_context 声明的跨模块外部契约文件只读白名单（仓库根相对路径 -> true）。
	ExternalFileWhitelist map[string]bool
	// Redactor 三级凭据脱敏器；为空时自动初始化默认 SecretRedactor。
	Redactor *security.SecretRedactor

	MaxReadLines   int
	MaxOutputBytes int
	MaxGrepMatches int
	MaxGrepFiles   int
	MaxFileBytes   int64
}

// SandboxSourceToolkit 实现零副作用、零 Shell、经 SafeJoin + 三级脱敏保护的只读代码探索工具集。
type SandboxSourceToolkit struct {
	scopeRoot         string
	repoRoot          string
	scopeRelFromRepo  string
	hasFS             bool
	ir                *codeir.CodeIRBundle
	externalWhitelist map[string]bool
	redactor          *security.SecretRedactor
	ignoreMatcher     *sandbox.IgnoreMatcher
	maxReadLines      int
	maxOutputBytes    int
	maxGrepMatches    int
	maxGrepFiles      int
	maxFileBytes      int64
}

// NewSandboxSourceToolkit 构造受控只读源码工具集。
func NewSandboxSourceToolkit(cfg SandboxToolkitConfig) *SandboxSourceToolkit {
	redactor := cfg.Redactor
	if redactor == nil {
		redactor = security.NewSecretRedactor()
	}

	maxRead := clampInt(cfg.MaxReadLines, defaultToolMaxReadLines, 10, hardToolMaxReadLines)
	maxOut := clampInt(cfg.MaxOutputBytes, defaultToolMaxOutputBytes, 1024, hardToolMaxOutputBytes)
	maxGrep := clampInt(cfg.MaxGrepMatches, defaultToolMaxGrepMatches, 1, hardToolMaxGrepMatches)
	maxGrepFiles := cfg.MaxGrepFiles
	if maxGrepFiles <= 0 {
		maxGrepFiles = defaultToolMaxGrepFiles
	}
	maxFileBytes := cfg.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultToolMaxFileBytes
	}

	var scopeRoot string
	hasFS := false
	if strings.TrimSpace(cfg.ScopeRoot) != "" {
		if resolved, err := sandbox.SafeJoin(cfg.ScopeRoot, "."); err == nil {
			if st, statErr := os.Stat(resolved); statErr == nil && st.IsDir() {
				scopeRoot = resolved
				hasFS = true
			}
		}
	}

	var ignoreMatcher *sandbox.IgnoreMatcher
	var scopeRelFromRepo string
	var resolvedRepoRoot string
	ignoreBase := strings.TrimSpace(cfg.RepoRoot)
	if ignoreBase == "" {
		ignoreBase = scopeRoot
	}
	if ignoreBase != "" {
		if resolvedBase, baseErr := sandbox.SafeJoin(ignoreBase, "."); baseErr == nil {
			resolvedRepoRoot = resolvedBase
			if ignorePath, err := sandbox.SafeJoin(resolvedBase, ".archdocignore"); err == nil {
				if data, rErr := os.ReadFile(ignorePath); rErr == nil {
					ignoreMatcher = sandbox.ParseIgnoreRules(string(data))
				}
			}
			if scopeRoot != "" {
				if rel, relErr := filepath.Rel(resolvedBase, scopeRoot); relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					scopeRelFromRepo = filepath.ToSlash(rel)
				}
			}
		}
	}

	var extWhitelist map[string]bool
	if len(cfg.ExternalFileWhitelist) > 0 {
		extWhitelist = make(map[string]bool, len(cfg.ExternalFileWhitelist))
		for k, v := range cfg.ExternalFileWhitelist {
			if v {
				cleanK := strings.TrimPrefix(path.Clean(strings.ReplaceAll(strings.TrimSpace(k), "\\", "/")), "./")
				if cleanK != "" && cleanK != "." && !strings.HasPrefix(cleanK, "..") {
					extWhitelist[cleanK] = true
				}
			}
		}
	}

	return &SandboxSourceToolkit{
		scopeRoot:         scopeRoot,
		repoRoot:          resolvedRepoRoot,
		scopeRelFromRepo:  scopeRelFromRepo,
		hasFS:             hasFS,
		ir:                cfg.IR,
		externalWhitelist: extWhitelist,
		redactor:          redactor,
		ignoreMatcher:     ignoreMatcher,
		maxReadLines:      maxRead,
		maxOutputBytes:    maxOut,
		maxGrepMatches:    maxGrep,
		maxGrepFiles:      maxGrepFiles,
		maxFileBytes:      maxFileBytes,
	}
}

// HasTools 返回当前上下文是否存在至少一个可用只读工具。
func (t *SandboxSourceToolkit) HasTools() bool {
	return len(t.Definitions()) > 0
}

// Definitions 返回 OpenAI Compatible 只读工具 Schema 定义列表。
func (t *SandboxSourceToolkit) Definitions() []ToolDefinition {
	if t == nil {
		return nil
	}
	var defs []ToolDefinition
	if t.hasFS {
		defs = append(defs,
			ToolDefinition{
				Type: "function",
				Function: ToolFunctionDefinition{
					Name:        ToolNameReadFileLines,
					Description: "只读读取沙箱内指定源码文件的行号区间（自动带行号并执行敏感凭据脱敏，单次最多 200 行）。用于核实函数体内部逻辑、状态机分支与锁作用域。",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path": map[string]any{
								"type":        "string",
								"description": "相对于当前仓库/子目录沙箱根的 POSIX 相对路径，例如 src/order/order_service.cpp",
							},
							"start_line": map[string]any{
								"type":        "integer",
								"description": "1-based 起始行号（默认 1）",
							},
							"end_line": map[string]any{
								"type":        "integer",
								"description": "1-based 结束行号（可选，默认从 start_line 读取最多 120 行）",
							},
						},
						"required": []string{"file_path"},
					},
				},
			},
			ToolDefinition{
				Type: "function",
				Function: ToolFunctionDefinition{
					Name:        ToolNameGrepInSandbox,
					Description: "在当前沙箱允许访问的源码文件中执行只读字面量关键词检索，快速定位函数调用点、错误码使用处或跨文件链路。",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{
								"type":        "string",
								"description": "精确检索的关键词或标识符（2~120 字符，纯字面量匹配），例如 OrderService::Pay 或 ERR_TIMEOUT",
							},
							"path_prefix": map[string]any{
								"type":        "string",
								"description": "可选：限定检索的子目录相对路径前缀，例如 src/order",
							},
							"case_sensitive": map[string]any{
								"type":        "boolean",
								"description": "是否区分大小写（默认 true）",
							},
							"max_matches": map[string]any{
								"type":        "integer",
								"description": "最大返回命中条数（默认 25，上限 50）",
							},
						},
						"required": []string{"query"},
					},
				},
			},
			ToolDefinition{
				Type: "function",
				Function: ToolFunctionDefinition{
					Name:        ToolNameListDir,
					Description: "只读列出沙箱内指定相对目录下的可见子目录与源码文件（自动过滤 .git、三方依赖与二进制文件）。",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "相对于沙箱根的目录路径，留空或 '.' 表示沙箱根目录",
							},
							"max_entries": map[string]any{
								"type":        "integer",
								"description": "最多返回条目数（默认 60，上限 100）",
							},
						},
					},
				},
			},
		)
	}

	if t.ir != nil {
		defs = append(defs, ToolDefinition{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        ToolNameGetSymbolImpl,
				Description: "按全限定符号名或方法名查询 L1 Code IR 符号/RPC/HTTP 路由索引，并自动读取其对应源码实现体（含行号与上下文，自动脱敏）。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"symbol": map[string]any{
							"type":        "string",
							"description": "全限定符号名、方法名、RPC 名或路由路径，例如 order::OrderService::Pay 或 CreateOrder",
						},
						"context_lines": map[string]any{
							"type":        "integer",
							"description": "可选：在符号起止行前后额外附带的上下文行数（默认 5，上限 20）",
						},
					},
					"required": []string{"symbol"},
				},
			},
		})
	}
	return defs
}

// Execute 执行单个只读工具调用；所有返回文本均已脱敏并受体积上限保护。
func (t *SandboxSourceToolkit) Execute(ctx context.Context, call ToolCall) (string, string, bool) {
	if ctx != nil && ctx.Err() != nil {
		return "工具执行已取消: " + ctx.Err().Error(), call.Function.Name + " (canceled)", true
	}
	if t == nil {
		return "工具执行器未初始化", call.Function.Name + " (unavailable)", true
	}

	switch call.Function.Name {
	case ToolNameReadFileLines:
		return t.execReadFileLines(ctx, call.Function.Arguments)
	case ToolNameGrepInSandbox:
		return t.execGrepInSandbox(ctx, call.Function.Arguments)
	case ToolNameListDir:
		return t.execListDir(ctx, call.Function.Arguments)
	case ToolNameGetSymbolImpl:
		return t.execGetSymbolImpl(ctx, call.Function.Arguments)
	default:
		return fmt.Sprintf("拒绝调用未授权工具 %q：仅允许只读源码探索工具", call.Function.Name),
			fmt.Sprintf("unknown tool %s", call.Function.Name), true
	}
}

type readFileLinesArgs struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *SandboxSourceToolkit) execReadFileLines(ctx context.Context, rawArgs string) (string, string, bool) {
	if !t.hasFS {
		return "当前上下文未挂载本地代码沙箱，无法直接读取文件内容。", "read_file_lines (no sandbox)", true
	}
	var args readFileLinesArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "参数解析失败，须为合法 JSON 对象: " + err.Error(), "read_file_lines (invalid args)", true
	}
	relClean, safeAbs, err := t.validateReadableFile(args.FilePath)
	if err != nil {
		return err.Error(), fmt.Sprintf("read_file_lines(%s) denied", sanitizeSummaryToken(args.FilePath)), true
	}

	startLine := args.StartLine
	if startLine <= 0 {
		startLine = 1
	}
	endLine := args.EndLine
	if endLine <= 0 {
		endLine = startLine + 119
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine-startLine+1 > t.maxReadLines {
		endLine = startLine + t.maxReadLines - 1
	}

	rendered, actualEnd, totalLines, truncatedByBytes, err := t.readSanitizedLineRange(ctx, safeAbs, relClean, startLine, endLine)
	if err != nil {
		return err.Error(), fmt.Sprintf("read_file_lines(%s) error", relClean), true
	}

	summary := fmt.Sprintf("read_file_lines(%s:%d-%d)", relClean, startLine, actualEnd)
	if truncatedByBytes {
		summary += " [truncated]"
	}
	if totalLines > actualEnd {
		rendered += fmt.Sprintf("\n... (文件共 %d 行，当前显示 %d-%d 行；如需后续内容请再次调用 read_file_lines)", totalLines, startLine, actualEnd)
	}
	return rendered, summary, false
}

type listDirArgs struct {
	Path       string `json:"path"`
	MaxEntries int    `json:"max_entries"`
}

func (t *SandboxSourceToolkit) execListDir(ctx context.Context, rawArgs string) (string, string, bool) {
	if !t.hasFS {
		return "当前上下文未挂载本地代码沙箱，无法列出目录。", "list_dir (no sandbox)", true
	}
	var args listDirArgs
	if strings.TrimSpace(rawArgs) != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return "参数解析失败，须为合法 JSON 对象: " + err.Error(), "list_dir (invalid args)", true
		}
	}

	relReq := strings.TrimSpace(args.Path)
	if relReq == "" || relReq == "." || relReq == "./" {
		relReq = "."
	} else {
		norm, err := sandbox.NormalizeSubdir(relReq)
		if err != nil {
			return "目录路径非法，拒绝访问。", "list_dir (invalid path)", true
		}
		if norm == "" {
			relReq = "."
		} else {
			relReq = norm
		}
	}
	if relReq != "." && sandbox.IsHardProtectedPath(relReq) {
		return "禁止访问受保护系统目录。", "list_dir (protected)", true
	}

	safeDir, err := sandbox.SafeJoin(t.scopeRoot, relReq)
	if err != nil {
		return "目标目录越界或指向沙箱外符号链接，已拦截。", "list_dir (path escape)", true
	}
	info, err := os.Stat(safeDir)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("目录不存在或不是有效目录: %s", relReq), fmt.Sprintf("list_dir(%s) not found", relReq), true
	}

	maxEntries := clampInt(args.MaxEntries, defaultToolMaxListEntries, 1, hardToolMaxListEntries)
	entries, err := os.ReadDir(safeDir)
	if err != nil {
		return "读取目录失败: " + err.Error(), fmt.Sprintf("list_dir(%s) error", relReq), true
	}

	type dirEntryItem struct {
		rel   string
		isDir bool
		size  int64
	}
	var visible []dirEntryItem
	for _, entry := range entries {
		if ctx != nil && ctx.Err() != nil {
			return "目录遍历已取消", "list_dir (canceled)", true
		}
		name := entry.Name()
		if name == ".archdocignore" {
			continue
		}
		childRel := name
		if relReq != "." {
			childRel = path.Join(relReq, name)
		}
		if sandbox.IsHardProtectedPath(childRel) {
			continue
		}
		// 拦截指向沙箱外的符号链接
		realChild, joinErr := sandbox.SafeJoin(t.scopeRoot, childRel)
		if joinErr != nil {
			continue
		}
		isDir := entry.IsDir()
		var size int64
		if st, stErr := os.Stat(realChild); stErr == nil {
			isDir = st.IsDir()
			size = st.Size()
		}
		if t.isIgnoredByNoiseRules(childRel, isDir) {
			continue
		}
		visible = append(visible, dirEntryItem{rel: childRel, isDir: isDir, size: size})
	}

	sort.Slice(visible, func(i, j int) bool {
		if visible[i].isDir != visible[j].isDir {
			return visible[i].isDir
		}
		return visible[i].rel < visible[j].rel
	})

	totalVisible := len(visible)
	if len(visible) > maxEntries {
		visible = visible[:maxEntries]
	}

	var out strings.Builder
	fmt.Fprintf(&out, "DIRECTORY: %s (showing %d/%d entries)\n", relReq, len(visible), totalVisible)
	for _, it := range visible {
		if it.isDir {
			fmt.Fprintf(&out, "[DIR]  %s/\n", it.rel)
		} else {
			fmt.Fprintf(&out, "[FILE] %s (%d bytes)\n", it.rel, it.size)
		}
	}
	return t.truncateOutput(out.String()), fmt.Sprintf("list_dir(%s) -> %d entries", relReq, len(visible)), false
}

type grepInSandboxArgs struct {
	Query         string `json:"query"`
	PathPrefix    string `json:"path_prefix"`
	CaseSensitive *bool  `json:"case_sensitive"`
	MaxMatches    int    `json:"max_matches"`
}

func (t *SandboxSourceToolkit) execGrepInSandbox(ctx context.Context, rawArgs string) (string, string, bool) {
	if !t.hasFS {
		return "当前上下文未挂载本地代码沙箱，无法执行源码检索。", "grep_in_sandbox (no sandbox)", true
	}
	var args grepInSandboxArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "参数解析失败，须为合法 JSON 对象: " + err.Error(), "grep_in_sandbox (invalid args)", true
	}

	query := strings.TrimSpace(args.Query)
	if len(query) < 2 || len(query) > 120 {
		return "query 长度必须在 2~120 字符之间。", "grep_in_sandbox (invalid query)", true
	}

	prefix := strings.TrimSpace(args.PathPrefix)
	if prefix != "" && prefix != "." && prefix != "./" {
		norm, err := sandbox.NormalizeSubdir(prefix)
		if err != nil {
			return "path_prefix 路径非法，已拦截。", "grep_in_sandbox (invalid path_prefix)", true
		}
		if sandbox.IsHardProtectedPath(norm) {
			return "禁止检索受保护目录。", "grep_in_sandbox (protected)", true
		}
		if _, err := sandbox.SafeJoin(t.scopeRoot, norm); err != nil {
			return "path_prefix 越界或指向外部符号链接，已拦截。", "grep_in_sandbox (path escape)", true
		}
		prefix = norm
	} else {
		prefix = ""
	}

	caseSensitive := true
	if args.CaseSensitive != nil {
		caseSensitive = *args.CaseSensitive
	}
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(query)
	}

	maxMatches := clampInt(args.MaxMatches, t.maxGrepMatches, 1, hardToolMaxGrepMatches)

	walker := sandbox.NewFileWalker()
	walkRes, err := walker.Walk(ctx, t.scopeRoot, prefix, sandbox.WalkOptions{
		MaxFileBytes: t.maxFileBytes,
	})
	if err != nil {
		return "遍历沙箱文件失败: " + err.Error(), "grep_in_sandbox (walk error)", true
	}

	type grepHit struct {
		filePath string
		lineNo   int
		text     string
	}
	var hits []grepHit
	scannedFiles := 0
	reachedFileCap := false

	for _, wf := range walkRes.Files {
		if ctx != nil && ctx.Err() != nil {
			return "源码检索已取消", "grep_in_sandbox (canceled)", true
		}
		if scannedFiles >= t.maxGrepFiles {
			reachedFileCap = true
			break
		}
		if len(hits) >= maxMatches {
			break
		}
		scannedFiles++

		relPath := wf.RelPath
		if prefix != "" {
			relPath = path.Join(prefix, wf.RelPath)
		}
		if t.isIgnoredByNoiseRules(relPath, false) {
			continue
		}

		fileHits := t.scanFileForLiteral(ctx, wf.AbsPath, relPath, needle, caseSensitive, maxMatches-len(hits))
		for _, h := range fileHits {
			hits = append(hits, grepHit{filePath: relPath, lineNo: h.lineNo, text: h.text})
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "GREP query=%q prefix=%q matches=%d scanned_files=%d\n", query, prefix, len(hits), scannedFiles)
	if len(hits) == 0 {
		out.WriteString("(未命中任何匹配行)\n")
	} else {
		for _, h := range hits {
			lineText := h.text
			if len(lineText) > 200 {
				lineText = lineText[:200] + "..."
			}
			redactedLine, _ := t.redactor.RedactText(lineText)
			fmt.Fprintf(&out, "%s:%d: %s\n", h.filePath, h.lineNo, redactedLine)
		}
	}
	if reachedFileCap {
		fmt.Fprintf(&out, "... (已达到单次最大扫描文件上限 %d，请通过 path_prefix 缩小检索范围)\n", t.maxGrepFiles)
	}
	return t.truncateOutput(out.String()), fmt.Sprintf("grep_in_sandbox(%q) -> %d hits", sanitizeSummaryToken(query), len(hits)), false
}

type literalHit struct {
	lineNo int
	text   string
}

func (t *SandboxSourceToolkit) scanFileForLiteral(
	ctx context.Context,
	absPath string,
	relPath string,
	needle string,
	caseSensitive bool,
	limit int,
) []literalHit {
	if limit <= 0 {
		return nil
	}
	// 再次确认路径安全，防止符号链接竞态替换
	if _, err := sandbox.SafeJoin(t.scopeRoot, relPath); err != nil {
		return nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 32*1024), 512*1024)
	var out []literalHit
	lineNo := 0
	for scanner.Scan() {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		lineNo++
		line := scanner.Text()
		haystack := line
		if !caseSensitive {
			haystack = strings.ToLower(line)
		}
		if strings.Contains(haystack, needle) {
			out = append(out, literalHit{
				lineNo: lineNo,
				text:   strings.TrimSpace(line),
			})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

type getSymbolImplArgs struct {
	Symbol       string `json:"symbol"`
	ContextLines int    `json:"context_lines"`
}

type matchedSymbolEntry struct {
	score     int
	kind      string
	fqSymbol  string
	signature string
	span      codeir.SourceSpan
	declSpan  *codeir.SourceSpan
}

func (t *SandboxSourceToolkit) execGetSymbolImpl(ctx context.Context, rawArgs string) (string, string, bool) {
	if t.ir == nil {
		return "当前上下文缺少 L1 Code IR 符号索引，请改用 grep_in_sandbox 或 read_file_lines。", "get_symbol_impl (no IR)", true
	}
	var args getSymbolImplArgs
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "参数解析失败，须为合法 JSON 对象: " + err.Error(), "get_symbol_impl (invalid args)", true
	}
	query := strings.TrimSpace(args.Symbol)
	if query == "" || len(query) > 160 {
		return "symbol 参数不能为空且长度不超过 160 字符。", "get_symbol_impl (invalid symbol)", true
	}
	ctxLines := clampInt(args.ContextLines, 5, 0, 20)

	matches := matchSymbolsInIR(t.ir, query)
	if len(matches) == 0 {
		return fmt.Sprintf("在 L1 Code IR 中未找到匹配符号 %q，可尝试使用 grep_in_sandbox 搜索关键词。", query),
			fmt.Sprintf("get_symbol_impl(%s) -> 0 matches", sanitizeSummaryToken(query)), false
	}

	const maxShown = 3
	if len(matches) > maxShown {
		matches = matches[:maxShown]
	}

	var out strings.Builder
	fmt.Fprintf(&out, "SYMBOL LOOKUP query=%q matched=%d\n", query, len(matches))
	for i, m := range matches {
		fmt.Fprintf(&out, "\n### Match %d: %s (%s)\n", i+1, m.fqSymbol, m.kind)
		if m.signature != "" {
			redSig, _ := t.redactor.RedactText(m.signature)
			fmt.Fprintf(&out, "Signature: %s\n", redSig)
		}
		fmt.Fprintf(&out, "Location: %s:%d-%d\n", m.span.FilePath, m.span.LineStart, m.span.LineEnd)
		if m.declSpan != nil && m.declSpan.FilePath != "" {
			fmt.Fprintf(&out, "DeclLocation: %s:%d-%d\n", m.declSpan.FilePath, m.declSpan.LineStart, m.declSpan.LineEnd)
		}

		if t.hasFS && m.span.FilePath != "" {
			relClean, safeAbs, err := t.validateReadableFile(m.span.FilePath)
			if err == nil {
				sLine := m.span.LineStart - ctxLines
				if sLine < 1 {
					sLine = 1
				}
				eLine := m.span.LineEnd + ctxLines
				if eLine < sLine {
					eLine = sLine + 80
				}
				if eLine-sLine+1 > t.maxReadLines {
					eLine = sLine + t.maxReadLines - 1
				}
				snippet, _, _, _, readErr := t.readSanitizedLineRange(ctx, safeAbs, relClean, sLine, eLine)
				if readErr == nil {
					out.WriteString(snippet)
					out.WriteByte('\n')
				}
			}
		}
	}

	return t.truncateOutput(out.String()), fmt.Sprintf("get_symbol_impl(%s) -> %d matches", sanitizeSummaryToken(query), len(matches)), false
}

func matchSymbolsInIR(ir *codeir.CodeIRBundle, query string) []matchedSymbolEntry {
	qLower := strings.ToLower(query)
	var out []matchedSymbolEntry

	scoreName := func(candidate string) int {
		cLower := strings.ToLower(strings.TrimSpace(candidate))
		if cLower == "" {
			return 0
		}
		if cLower == qLower {
			return 100
		}
		if strings.HasSuffix(cLower, "::"+qLower) || strings.HasSuffix(cLower, "."+qLower) || strings.HasSuffix(cLower, "/"+qLower) {
			return 80
		}
		if strings.Contains(cLower, qLower) {
			return 40
		}
		return 0
	}

	for _, sym := range ir.Symbols.Symbols {
		if sc := scoreName(sym.FQSymbol); sc > 0 {
			out = append(out, matchedSymbolEntry{
				score:     sc,
				kind:      sym.Kind,
				fqSymbol:  sym.FQSymbol,
				signature: sym.Signature,
				span:      sym.Meta.Span,
				declSpan:  sym.DeclSpan,
			})
		}
	}
	for _, pf := range ir.IDLContracts.Protos {
		for _, svc := range pf.Services {
			if sc := scoreName(svc.FullName); sc > 0 {
				out = append(out, matchedSymbolEntry{
					score:    sc,
					kind:     "proto_service",
					fqSymbol: svc.FullName,
					span:     svc.Meta.Span,
				})
			}
			for _, rpc := range svc.RPCs {
				fullRPC := svc.FullName + "." + rpc.MethodName
				sc := maxInt(scoreName(fullRPC), scoreName(rpc.MethodName))
				if sc > 0 {
					out = append(out, matchedSymbolEntry{
						score:     sc,
						kind:      "proto_rpc",
						fqSymbol:  fullRPC,
						signature: fmt.Sprintf("rpc %s(%s) returns (%s)", rpc.MethodName, rpc.RequestType, rpc.ResponseType),
						span:      rpc.Meta.Span,
					})
				}
			}
		}
		for _, msg := range pf.Messages {
			if sc := scoreName(msg.FullName); sc > 0 {
				out = append(out, matchedSymbolEntry{
					score:    sc,
					kind:     "proto_message",
					fqSymbol: msg.FullName,
					span:     msg.Meta.Span,
				})
			}
		}
	}
	for _, rt := range ir.HTTPRoutes.Routes {
		sc := maxInt(scoreName(rt.HandlerFQSymbol), scoreName(rt.Path))
		if sc > 0 {
			fq := rt.HandlerFQSymbol
			if fq == "" {
				fq = rt.Method + " " + rt.Path
			}
			out = append(out, matchedSymbolEntry{
				score:     sc,
				kind:      "http_route",
				fqSymbol:  fq,
				signature: fmt.Sprintf("%s %s -> %s", rt.Method, rt.Path, rt.HandlerFQSymbol),
				span:      rt.Meta.Span,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].fqSymbol != out[j].fqSymbol {
			return out[i].fqSymbol < out[j].fqSymbol
		}
		return out[i].span.FilePath < out[j].span.FilePath
	})
	return out
}

func (t *SandboxSourceToolkit) validateReadableFile(rawRel string) (string, string, error) {
	trimmed := strings.TrimSpace(rawRel)
	if trimmed == "" {
		return "", "", fmt.Errorf("file_path 不能为空")
	}
	if strings.ContainsRune(trimmed, '\x00') || strings.Contains(trimmed, "\\") || strings.HasPrefix(trimmed, "/") {
		return "", "", fmt.Errorf("路径非法，禁止绝对路径、反斜杠或控制字符")
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("路径越界，禁止包含 '..'")
		}
	}
	cleanRel := strings.TrimPrefix(path.Clean(trimmed), "./")
	if cleanRel == "." || cleanRel == "" || strings.HasPrefix(cleanRel, "../") {
		return "", "", fmt.Errorf("路径非法")
	}
	if sandbox.IsHardProtectedPath(cleanRel) || cleanRel == ".archdocignore" {
		return "", "", fmt.Errorf("禁止访问受保护系统文件: %s", cleanRel)
	}
	if t.isIgnoredByNoiseRules(cleanRel, false) {
		return "", "", fmt.Errorf("目标文件属于三方依赖、自动生成代码、二进制或 .archdocignore 排除范围，禁止读取: %s", cleanRel)
	}

	safeAbs, err := sandbox.SafeJoin(t.scopeRoot, cleanRel)
	if err != nil {
		return "", "", fmt.Errorf("目标文件越界或指向沙箱外符号链接，已拦截: %s", cleanRel)
	}
	info, err := os.Stat(safeAbs)
	if err != nil && os.IsNotExist(err) && t.repoRoot != "" && t.externalWhitelist[cleanRel] {
		// 若文件不在当前子目录内，但明确登记在 module_context 跨模块外部契约只读白名单中，允许经 SafeJoin(repoRoot) 只读访问
		if repoAbs, rJoinErr := sandbox.SafeJoin(t.repoRoot, cleanRel); rJoinErr == nil {
			if rInfo, rStatErr := os.Stat(repoAbs); rStatErr == nil {
				safeAbs = repoAbs
				info = rInfo
				err = nil
			}
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("文件不存在: %s", cleanRel)
		}
		return "", "", fmt.Errorf("无法访问文件 %s: %v", cleanRel, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("目标是目录而非文件，请改用 list_dir: %s", cleanRel)
	}
	if info.Size() > t.maxFileBytes {
		return "", "", fmt.Errorf("文件体积超过只读上限 (%d bytes): %s", t.maxFileBytes, cleanRel)
	}
	if isBin, err := isBinaryFile(safeAbs); err != nil || isBin {
		return "", "", fmt.Errorf("拒绝读取二进制文件: %s", cleanRel)
	}
	return cleanRel, safeAbs, nil
}

func (t *SandboxSourceToolkit) isIgnoredByNoiseRules(relPOSIX string, isDir bool) bool {
	l1Hit, l1Rule := sandbox.MatchLayer1Builtin(relPOSIX, isDir)
	repoRel := relPOSIX
	if t.scopeRelFromRepo != "" {
		repoRel = path.Join(t.scopeRelFromRepo, relPOSIX)
	}
	l2Matched, l2Ignored := t.ignoreMatcher.Match(repoRel, isDir)
	if !l2Matched && repoRel != relPOSIX {
		l2Matched, l2Ignored = t.ignoreMatcher.Match(relPOSIX, isDir)
	}
	if l2Matched {
		if l2Ignored {
			return true
		}
		return l1Hit && l1Rule == sandbox.RuleBinary
	}
	return l1Hit
}

func (t *SandboxSourceToolkit) readSanitizedLineRange(
	ctx context.Context,
	safeAbs string,
	relPath string,
	startLine int,
	endLine int,
) (rendered string, actualEnd int, totalLines int, truncatedByBytes bool, err error) {
	f, err := os.Open(safeAbs)
	if err != nil {
		return "", 0, 0, false, fmt.Errorf("读取文件失败: %v", err)
	}
	defer func() { _ = f.Close() }()

	// 先读取整文件内容（受 maxFileBytes <= 1MB 限制）进行整块三级脱敏，确保多行 PEM 私钥块也能被完整替换！
	rawBytes, err := io.ReadAll(io.LimitReader(f, t.maxFileBytes+1))
	if err != nil {
		return "", 0, 0, false, fmt.Errorf("读取文件内容失败: %v", err)
	}
	if int64(len(rawBytes)) > t.maxFileBytes {
		return "", 0, 0, false, fmt.Errorf("文件体积超出限制")
	}
	if ctx != nil && ctx.Err() != nil {
		return "", 0, 0, false, ctx.Err()
	}

	normalized := strings.ReplaceAll(string(rawBytes), "\r\n", "\n")
	redactedAll, _ := t.redactor.RedactText(normalized)
	lines := strings.Split(redactedAll, "\n")
	totalLines = len(lines)

	if startLine > totalLines {
		return fmt.Sprintf("FILE: %s (共 %d 行，请求的起始行 %d 超出文件行数)", relPath, totalLines, startLine), totalLines, totalLines, false, nil
	}
	if endLine > totalLines {
		endLine = totalLines
	}

	var out strings.Builder
	fmt.Fprintf(&out, "FILE: %s (lines %d-%d of %d)\n", relPath, startLine, endLine, totalLines)
	actualEnd = startLine - 1
	for ln := startLine; ln <= endLine; ln++ {
		lineFormatted := fmt.Sprintf("%4d | %s\n", ln, lines[ln-1])
		if out.Len()+len(lineFormatted) > t.maxOutputBytes {
			truncatedByBytes = true
			out.WriteString("...(truncated_by_tool_output_limit)\n")
			break
		}
		out.WriteString(lineFormatted)
		actualEnd = ln
	}
	return out.String(), actualEnd, totalLines, truncatedByBytes, nil
}

func (t *SandboxSourceToolkit) truncateOutput(s string) string {
	redacted, _ := t.redactor.RedactText(s)
	if len(redacted) <= t.maxOutputBytes {
		return redacted
	}
	return redacted[:t.maxOutputBytes] + "\n...(truncated_by_tool_output_limit)"
}

func isBinaryFile(absPath string) (bool, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, toolBinarySniffBytes)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

func clampInt(val, defVal, minVal, maxVal int) int {
	if val <= 0 {
		val = defVal
	}
	if val < minVal {
		return minVal
	}
	if val > maxVal {
		return maxVal
	}
	return val
}

func sanitizeSummaryToken(s string) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if len(clean) > 64 {
		return clean[:64] + "..."
	}
	return clean
}
