package publisher

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// FrontmatterData 严格对齐 ADR-03 §3.2 与 ADR-04 D-5 标准 YAML Frontmatter 契约。
type FrontmatterData struct {
	Title           string
	Repo            string
	Subdir          string
	ScopeType       string // "repo" | "module"（可选，非空时输出 scope_type）
	Module          string // 子模块路径（ScopeType == "module" 时输出 module）
	ParentDoc       string // 指向父级整仓同类型文档的相对路径（动态深度，非空时输出 parent_doc）
	DocType         string
	DocTypeName     string
	DocTypeRevision int
	DocID           uint
	RevisionNo      int
	RefName         string
	CommitSHA       string
	PublishedAt     string // RFC3339
	PublishedBy     string
	SourceModule    string // 默认 "archdoc"
}

// ChildModuleNavEntry 描述整仓文档文末自动附加的「## 模块导航」子模块链接项（ADR-04 D-5）。
type ChildModuleNavEntry struct {
	Subdirectory string
	Title        string
	RelLink      string
	CommitSHA    string
}

// EvidenceRef 描述用于渲染平台深链的已验证源码证据锚点。
type EvidenceRef struct {
	Scope      string // "" (默认 module) | "repo"
	FilePath   string
	SymbolKind string
	FQSymbol   string
	CommitSHA  string
	LineStart  int
	LineEnd    int
}

// RenderInput 发布渲染管线入参。
type RenderInput struct {
	Frontmatter       FrontmatterData
	BodyMarkdown      string
	Evidences         []EvidenceRef
	PublishedDocTypes map[string]bool       // 同范围下已发布的 DocType slug 集合
	ChildModuleDocs   []ChildModuleNavEntry // 整仓文档关联的已发布子模块同类型文档列表
}

// RenderOutput 发布渲染管线出参。
type RenderOutput struct {
	Markdown              string
	BrokenLinksDowngraded int
}

// BuildEvidenceDeepLink 构造 ADR-03 §2.2 标准平台路由深链：
// /archdoc/docs/<doc_id>/evidence?commit=<sha>&file=<enc>&symbol=<enc>&lines=<start>-<end>
func BuildEvidenceDeepLink(docID uint, commitSHA, filePath, fqSymbol string, lineStart, lineEnd int) string {
	return BuildEvidenceDeepLinkWithScope(docID, commitSHA, filePath, fqSymbol, lineStart, lineEnd, "")
}

// BuildEvidenceDeepLinkWithScope 构造带可选 scope=repo 参数的平台路由深链（ADR-04 D-3）。
func BuildEvidenceDeepLinkWithScope(docID uint, commitSHA, filePath, fqSymbol string, lineStart, lineEnd int, scope string) string {
	var params []string
	if commitSHA != "" {
		params = append(params, "commit="+url.QueryEscape(commitSHA))
	}
	if filePath != "" {
		params = append(params, "file="+url.QueryEscape(filePath))
	}
	if fqSymbol != "" {
		params = append(params, "symbol="+url.QueryEscape(fqSymbol))
	}
	if lineStart > 0 {
		if lineEnd < lineStart {
			lineEnd = lineStart
		}
		params = append(params, fmt.Sprintf("lines=%d-%d", lineStart, lineEnd))
	}
	if strings.TrimSpace(scope) == "repo" {
		params = append(params, "scope=repo")
	}
	base := fmt.Sprintf("/archdoc/docs/%d/evidence", docID)
	if len(params) == 0 {
		return base
	}
	return base + "?" + strings.Join(params, "&")
}

// RenderFrontmatter 生成符合 ADR-03 §3.2 与 D-02 通用解析器的标准 YAML Frontmatter 块。
func RenderFrontmatter(fm FrontmatterData) string {
	sourceModule := strings.TrimSpace(fm.SourceModule)
	if sourceModule == "" {
		sourceModule = "archdoc"
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "title: %s\n", yamlQuote(fm.Title))
	fmt.Fprintf(&sb, "repo: %s\n", yamlQuote(fm.Repo))
	if fm.Subdir != "" {
		fmt.Fprintf(&sb, "subdir: %s\n", yamlQuote(fm.Subdir))
	}
	if fm.ScopeType != "" {
		fmt.Fprintf(&sb, "scope_type: %s\n", yamlQuote(fm.ScopeType))
	}
	if fm.Module != "" {
		fmt.Fprintf(&sb, "module: %s\n", yamlQuote(fm.Module))
	}
	if fm.ParentDoc != "" {
		fmt.Fprintf(&sb, "parent_doc: %s\n", yamlQuote(fm.ParentDoc))
	}
	fmt.Fprintf(&sb, "doc_type: %s\n", yamlQuote(fm.DocType))
	if fm.DocTypeName != "" {
		fmt.Fprintf(&sb, "doc_type_name: %s\n", yamlQuote(fm.DocTypeName))
	}
	if fm.DocTypeRevision > 0 {
		fmt.Fprintf(&sb, "doc_type_revision: %d\n", fm.DocTypeRevision)
	}
	if fm.DocID > 0 {
		fmt.Fprintf(&sb, "doc_id: %d\n", fm.DocID)
	}
	if fm.RevisionNo > 0 {
		fmt.Fprintf(&sb, "revision_no: %d\n", fm.RevisionNo)
	}
	if fm.RefName != "" {
		fmt.Fprintf(&sb, "ref_name: %s\n", yamlQuote(fm.RefName))
	}
	if fm.CommitSHA != "" {
		fmt.Fprintf(&sb, "commit_sha: %s\n", yamlQuote(fm.CommitSHA))
	}
	if fm.PublishedAt != "" {
		fmt.Fprintf(&sb, "published_at: %s\n", yamlQuote(fm.PublishedAt))
	}
	if fm.PublishedBy != "" {
		fmt.Fprintf(&sb, "published_by: %s\n", yamlQuote(fm.PublishedBy))
	}
	fmt.Fprintf(&sb, "source_module: %s\n", yamlQuote(sourceModule))
	sb.WriteString("---\n\n")
	return sb.String()
}

// RenderMarkdown 执行完整定稿发布渲染管线：
// 1. 将已验证的证据引用转换为 /archdoc/docs/:id/evidence?... 平台深链；
// 2. 校验跨文档相对链接，对尚未发布的同仓目标自动降级为纯文本；
// 3. 在文首注入标准 YAML Frontmatter。
func RenderMarkdown(in RenderInput) RenderOutput {
	body := in.BodyMarkdown
	if in.Frontmatter.DocID > 0 && len(in.Evidences) > 0 {
		body = rewriteEvidenceDeepLinks(body, in.Frontmatter.DocID, in.Frontmatter.CommitSHA, in.Evidences)
	}
	checkedBody, downgraded := CheckAndDowngradeCrossDocLinks(body, in.PublishedDocTypes)
	if len(in.ChildModuleDocs) > 0 && !strings.Contains(checkedBody, "## 模块导航") {
		checkedBody = strings.TrimRight(checkedBody, "\r\n") + "\n\n" + renderChildModuleNavSection(in.ChildModuleDocs)
	}
	frontmatter := RenderFrontmatter(in.Frontmatter)
	return RenderOutput{
		Markdown:              frontmatter + strings.TrimLeft(checkedBody, "\r\n"),
		BrokenLinksDowngraded: downgraded,
	}
}

func renderChildModuleNavSection(children []ChildModuleNavEntry) string {
	sorted := make([]ChildModuleNavEntry, len(children))
	copy(sorted, children)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Subdirectory < sorted[j].Subdirectory
	})

	var sb strings.Builder
	sb.WriteString("## 模块导航\n\n")
	sb.WriteString("| 模块路径 | 模块文档 | 提交版本 |\n")
	sb.WriteString("| :--- | :--- | :--- |\n")
	for _, c := range sorted {
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = c.Subdirectory
		}
		shortSHA := strings.TrimSpace(c.CommitSHA)
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		if shortSHA == "" {
			shortSHA = "-"
		}
		fmt.Fprintf(&sb, "| `%s` | [%s](%s) | `%s` |\n", c.Subdirectory, title, c.RelLink, shortSHA)
	}
	return sb.String()
}

var (
	reInlineCodeSpan = regexp.MustCompile("`([^`\r\n]{2,200})`")
	reMarkdownLink   = regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`)
)

// rewriteEvidenceDeepLinks 在保留围栏代码块与既有 Markdown 链接的前提下，将反引号中的已验证文件/符号引用转为平台深链。
func rewriteEvidenceDeepLinks(markdown string, docID uint, defaultCommit string, evidences []EvidenceRef) string {
	// 按匹配键建立确定性查找表（优先更精确的 file:start-end / fq_symbol，其次 file_path）
	sortedEvs := make([]EvidenceRef, len(evidences))
	copy(sortedEvs, evidences)
	sort.SliceStable(sortedEvs, func(i, j int) bool {
		// 符号级锚点优先于整文件锚点
		if (sortedEvs[i].FQSymbol != "") != (sortedEvs[j].FQSymbol != "") {
			return sortedEvs[i].FQSymbol != ""
		}
		if sortedEvs[i].FilePath != sortedEvs[j].FilePath {
			return sortedEvs[i].FilePath < sortedEvs[j].FilePath
		}
		return sortedEvs[i].LineStart < sortedEvs[j].LineStart
	})

	byFileLines := make(map[string]EvidenceRef)
	byFQSymbol := make(map[string]EvidenceRef)
	byFile := make(map[string]EvidenceRef)

	for _, ev := range sortedEvs {
		if ev.FilePath == "" {
			continue
		}
		if ev.LineStart > 0 {
			end := ev.LineEnd
			if end < ev.LineStart {
				end = ev.LineStart
			}
			keyRange := fmt.Sprintf("%s:%d-%d", ev.FilePath, ev.LineStart, end)
			if _, exists := byFileLines[keyRange]; !exists {
				byFileLines[keyRange] = ev
			}
			keySingle := fmt.Sprintf("%s:%d", ev.FilePath, ev.LineStart)
			if _, exists := byFileLines[keySingle]; !exists {
				byFileLines[keySingle] = ev
			}
		}
		if ev.FQSymbol != "" {
			if _, exists := byFQSymbol[ev.FQSymbol]; !exists {
				byFQSymbol[ev.FQSymbol] = ev
			}
		}
		if _, exists := byFile[ev.FilePath]; !exists {
			byFile[ev.FilePath] = ev
		}
	}

	lookup := func(token string) (EvidenceRef, bool) {
		norm := strings.TrimPrefix(strings.TrimSpace(token), "./")
		if ev, ok := byFileLines[norm]; ok {
			return ev, true
		}
		if ev, ok := byFQSymbol[norm]; ok {
			return ev, true
		}
		if ev, ok := byFile[norm]; ok {
			return ev, true
		}
		// 兼容 file.cpp:10-20 但行号与锚点略有差异时回退到文件级锚点
		if colon := strings.LastIndex(norm, ":"); colon > 0 {
			filePart := norm[:colon]
			if ev, ok := byFile[filePart]; ok {
				return ev, true
			}
		}
		return EvidenceRef{}, false
	}

	lines := strings.Split(markdown, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = replaceInlineEvidenceInLine(line, docID, defaultCommit, lookup)
	}
	return strings.Join(lines, "\n")
}

func replaceInlineEvidenceInLine(
	line string,
	docID uint,
	defaultCommit string,
	lookup func(string) (EvidenceRef, bool),
) string {
	// 找出该行中已存在的 Markdown 链接区间，避免在 [text](url) 内部嵌套链接
	linkRanges := reMarkdownLink.FindAllStringIndex(line, -1)
	isInsideLink := func(start, end int) bool {
		for _, r := range linkRanges {
			if start >= r[0] && end <= r[1] {
				return true
			}
		}
		return false
	}

	matches := reInlineCodeSpan.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line
	}

	var sb strings.Builder
	last := 0
	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		innerStart, innerEnd := m[2], m[3]
		if isInsideLink(fullStart, fullEnd) {
			continue
		}
		token := line[innerStart:innerEnd]
		ev, ok := lookup(token)
		if !ok {
			continue
		}
		commit := ev.CommitSHA
		if commit == "" {
			commit = defaultCommit
		}
		deepLink := BuildEvidenceDeepLinkWithScope(docID, commit, ev.FilePath, ev.FQSymbol, ev.LineStart, ev.LineEnd, ev.Scope)
		label := token
		if ev.LineStart > 0 && !strings.Contains(token, ":") && token == ev.FilePath {
			end := ev.LineEnd
			if end < ev.LineStart {
				end = ev.LineStart
			}
			label = fmt.Sprintf("%s:%d-%d", ev.FilePath, ev.LineStart, end)
		}
		sb.WriteString(line[last:fullStart])
		fmt.Fprintf(&sb, "[%s](%s)", label, deepLink)
		last = fullEnd
	}
	if last == 0 {
		return line
	}
	sb.WriteString(line[last:])
	return sb.String()
}

func yamlQuote(s string) string {
	cleaned := strings.ReplaceAll(s, "\r\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, `\`, `\\`)
	cleaned = strings.ReplaceAll(cleaned, `"`, `\"`)
	return `"` + cleaned + `"`
}
