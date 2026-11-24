package refdoc

// DefaultMaxDocBytes 单篇参考 Markdown 文档默认体积上限（512 KB）。
const DefaultMaxDocBytes int64 = 524288

// DefaultMaxBase64Bytes 内嵌 Base64 图片单块拦截上限（4 KB）。
const DefaultMaxBase64Bytes = 4096

// HeadingNode 描述参考文档切片后的单个标题或虚拟分块节点。
type HeadingNode struct {
	SectionID string `json:"section_id"`
	Title     string `json:"title"`
	Level     int    `json:"level"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	RuneCount int    `json:"rune_count"`
	IsChunk   bool   `json:"is_chunk,omitempty"`
}

// GateFinding 描述入库前安全门禁检出的脱敏命中条目。
type GateFinding struct {
	RuleID      string `json:"rule_id"`
	MatchedMask string `json:"matched_mask"`
	LineOffset  int    `json:"line_offset"`
	Severity    string `json:"severity"` // hard | confirm_required
}

// GateFailure 描述可映射至 HTTP 400/413/422 的校验阻断响应。
type GateFailure struct {
	HTTPStatus int            `json:"-"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
}

// ParsedDoc 表示通过门禁并完成结构化切片的参考文档结果。
type ParsedDoc struct {
	Markdown        string        `json:"markdown"`
	ContentSHA256   string        `json:"content_sha256"`
	ByteSize        int64         `json:"byte_size"`
	RuneCount       int           `json:"rune_count"`
	LineCount       int           `json:"line_count"`
	EstimatedTokens int           `json:"estimated_tokens"`
	TOC             []HeadingNode `json:"toc"`
	DeclaredTerms   []string      `json:"declared_terms"`
	Redacted        bool          `json:"redacted"`
}

// SectionExcerpt 表示按节只读读取返回的片段结果。
type SectionExcerpt struct {
	SectionID  string `json:"section_id"`
	Title      string `json:"title"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
	Content    string `json:"content"`
}

// SearchableDoc 表示参与关键词检索的参考文档上下文。
type SearchableDoc struct {
	DocID     uint
	Title     string
	VersionNo int
	Markdown  string
	TOC       []HeadingNode
}

// SearchHit 表示参考文档章节关键词检索命中结果。
type SearchHit struct {
	DocID        uint   `json:"doc_id"`
	DocTitle     string `json:"doc_title"`
	VersionNo    int    `json:"version_no"`
	SectionID    string `json:"section_id"`
	SectionTitle string `json:"section_title"`
	LineNumber   int    `json:"line_number"`
	Snippet      string `json:"snippet"`
}

// PromptDoc 表示注入生成/精修/校验上下文的参考文档完整快照视图。
type PromptDoc struct {
	DocID            uint          `json:"doc_id"`
	Title            string        `json:"title"`
	DocKind          string        `json:"doc_kind"`
	VersionNo        int           `json:"version_no"`
	ScopeLevel       string        `json:"scope_level"`
	ScopeRank        int           `json:"scope_rank"`
	SourceScopeLabel string        `json:"source_scope_label"`
	IsInherited      bool          `json:"is_inherited"`
	SummaryMD        string        `json:"summary_md"`
	TOC              []HeadingNode `json:"toc"`
	DeclaredTerms    []string      `json:"declared_terms,omitempty"`
	EstimatedTokens  int           `json:"estimated_tokens"`
	Markdown         string        `json:"-"`
}

// SlotExcerpt 表示为非工具槽位按预算选取的相关参考文档章节摘录。
type SlotExcerpt struct {
	DocID            uint   `json:"doc_id"`
	DocTitle         string `json:"doc_title"`
	VersionNo        int    `json:"version_no"`
	ScopeLevel       string `json:"scope_level"`
	SourceScopeLabel string `json:"source_scope_label"`
	SectionID        string `json:"section_id"`
	SectionTitle     string `json:"section_title"`
	StartLine        int    `json:"start_line"`
	EndLine          int    `json:"end_line"`
	Content          string `json:"content"`
	EstimatedTokens  int    `json:"estimated_tokens"`
}
