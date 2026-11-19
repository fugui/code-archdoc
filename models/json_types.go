package models

// 1. RepoSnapshot.StatsJSON
type SnapshotStats struct {
	LanguageBytes      map[string]int64 `json:"language_bytes"`
	LanguageFiles      map[string]int   `json:"language_files"`
	NoiseSkippedByRule map[string]int   `json:"noise_skipped_by_rule"` // third_party | generated | binary | archdocignore
	ParseDiagnostics   int              `json:"parse_diagnostics"`
	ExtractDurationMs  int64            `json:"extract_duration_ms"`
}

// 2. CodeIRFacet.SummaryJSON (< 512B 顶层轻量统计)
type FacetSummary struct {
	FacetName       string         `json:"facet_name"`
	ExactCount      int            `json:"exact_count"`
	HeuristicCount  int            `json:"heuristic_count"`
	RedactedSecrets int            `json:"redacted_secrets,omitempty"`
	ExtraCounts     map[string]int `json:"extra_counts,omitempty"`
}

// 3. GenTask.BudgetAdviceJSON (预算熔断结构化建议，SC-09)
type BudgetAdvice struct {
	EstimatedTokens  int                   `json:"estimated_tokens"`
	MaxTokensPerTask int                   `json:"max_tokens_per_task"`
	EffectiveFiles   int                   `json:"effective_files"`
	MaxFilesPerTask  int                   `json:"max_files_per_task"`
	TopDirectories   []BudgetDirectoryItem `json:"top_directories"`
	TopDocTypes      []BudgetDocTypeItem   `json:"top_doc_types"`
	Suggestions      []string              `json:"suggestions"`
}

type BudgetDirectoryItem struct {
	Path            string `json:"path"`
	EstimatedTokens int    `json:"estimated_tokens"`
	FileCount       int    `json:"file_count"`
}

type BudgetDocTypeItem struct {
	Slug            string `json:"slug"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

// 4. GenSlotTask.CitedAnchorsJSON (L2 模型输出的候选证据引用，供 Verifier 对齐)
type CitedAnchorCandidate struct {
	FilePath   string `json:"file_path"`
	SymbolKind string `json:"symbol_kind,omitempty"`
	FQSymbol   string `json:"fq_symbol,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// 5. DocumentRevision.SectionsJSON (有序章节快照数组)
type DocumentSectionSnapshot struct {
	SlotID           string `json:"slot_id"`
	SectionOrder     int    `json:"section_order"`
	Title            string `json:"title"`
	Ownership        string `json:"ownership"` // auto | hybrid | locked
	IsOrphan         bool   `json:"is_orphan"`
	ManuallyEdited   bool   `json:"manually_edited"`
	NeedsDriftReview bool   `json:"needs_drift_review"`
	Markdown         string `json:"markdown"`
	ContentSHA256    string `json:"content_sha256"`
}

// 6. DocumentRevision.VerifierReportJSON & ChatMessage.VerifierPreviewJSON (四重门禁报告，SC-04)
type VerifierReport struct {
	Passed               bool            `json:"passed"`
	CanApprove           bool            `json:"can_approve"`
	BlockerCount         int             `json:"blocker_count"`
	ErrorCount           int             `json:"error_count"`
	WarningCount         int             `json:"warning_count"`
	SlotCoverageRate     float64         `json:"slot_coverage_rate"`
	MissingRequiredSlots []string        `json:"missing_required_slots"`
	UnverifiedRefs       []UnverifiedRef `json:"unverified_refs"`
	MermaidIssues        []MermaidIssue  `json:"mermaid_issues"`
	SecretFindings       []SecretFinding `json:"secret_findings"`
}

type UnverifiedRef struct {
	SlotID   string `json:"slot_id"`
	RefKind  string `json:"ref_kind"` // file_path | symbol | route | table | config_key
	Value    string `json:"value"`
	Severity string `json:"severity"` // warning | error
}

type MermaidIssue struct {
	SlotID      string `json:"slot_id"`
	BlockIndex  int    `json:"block_index"`
	DiagramType string `json:"diagram_type"`
	IssueCode   string `json:"issue_code"` // mermaid_syntax_error | mermaid_disallowed_type | mermaid_complexity_warning
	Severity    string `json:"severity"`   // error | warning
	NodeCount   int    `json:"node_count"`
	EdgeCount   int    `json:"edge_count"`
	Message     string `json:"message"`
}

type SecretFinding struct {
	SlotID      string `json:"slot_id"`
	RuleID      string `json:"rule_id"`      // key_blacklist | pem_private_key | token_prefix | high_entropy
	MatchedMask string `json:"matched_mask"` // 仅记录脱敏片段，严禁落库明文
	LineOffset  int    `json:"line_offset"`
	Severity    string `json:"severity"` // 固定 "blocker"
}

// 7. SectionEvidence.VerifierSummaryJSON
type SectionVerifierSummary struct {
	UnverifiedCount int  `json:"unverified_count"`
	MermaidErrors   int  `json:"mermaid_errors"`
	MermaidWarnings int  `json:"mermaid_warnings"`
	SecretBlockers  int  `json:"secret_blockers"`
	HasBlocker      bool `json:"has_blocker"`
}

// 8. SectionEvidence.EvidencesJSON (严格对齐 ADR-03 §2.1)
type EvidenceAnchor struct {
	AnchorID           string `json:"anchor_id"`
	AnchorKey          string `json:"anchor_key"`
	FilePath           string `json:"file_path"`
	SymbolKind         string `json:"symbol_kind"`
	FQSymbol           string `json:"fq_symbol"`
	ContentFingerprint string `json:"content_fingerprint"`
	CommitSHA          string `json:"commit_sha"`
	LineStart          int    `json:"line_start"`
	LineEnd            int    `json:"line_end"`
	Confidence         string `json:"confidence"` // exact | heuristic
}

// 9. DocumentDrift.AffectedSlotsJSON (双维章节级漂移明细，SC-08 / SC-11)
type AffectedSlotDrift struct {
	SlotID         string   `json:"slot_id"`
	SlotTitle      string   `json:"slot_title"`
	Ownership      string   `json:"ownership"`
	Reason         string   `json:"reason"`
	ChangeKind     string   `json:"change_kind"` // added | modified | removed
	ChangedSymbols []string `json:"changed_symbols,omitempty"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
}

// 10. DocTypeRevision.SlotsSnapshotJSON (对齐 03-doctype-drift-and-publishing.md §1.2)
type SlotDefinition struct {
	SlotID          string   `json:"slot_id"`
	Title           string   `json:"title"`
	Ownership       string   `json:"ownership"` // auto | hybrid | locked
	Required        bool     `json:"required"`
	IRFacets        []string `json:"ir_facets"`
	TokenBudget     int      `json:"token_budget"`
	MinChars        int      `json:"min_chars"`
	InstructionHint string   `json:"instruction_hint,omitempty"`
}
