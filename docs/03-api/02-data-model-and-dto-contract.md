# 代码设计文档反推 (`code-archdoc`) 数据模型冻结与前后端 DTO 契约

- **文档编号**：`02-data-model-and-dto-contract.md`
- **文档版本**：v1.3.0 (ADR-04, ADR-05, ADR-06 & ADR-07 Frozen Contract)
- **编制日期**：2026-11-22
- **归属阶段**：阶段三：接口与数据契约冻结 (`03-api`)
- **上游基线**：[`docs/02-design/05-database-schema.md`](../02-design/05-database-schema.md) (v1.3.0)、[`01-http-api-contract.md`](./01-http-api-contract.md) (v1.4.0)、[`ADR-05`](../05-adr/05-doctype-blueprint-dual-mode-editor.md)、[`ADR-06`](../05-adr/06-document-review-and-refine-studio.md)、[`ADR-07`](../05-adr/07-single-repo-branch-switch.md)

> **文档定位**：阶段二 [`05-database-schema.md`](../02-design/05-database-schema.md) 已冻结 16 张 `archdoc_*` 表的 GORM Struct 与 ER 图；本文档进一步冻结阶段四编码所需的**表级索引/约束与数据不变量、12 组 JSONB 内部强类型 Schema、全局枚举与状态机合法转移矩阵、`services/` 依赖倒置仓储接口、`config.yaml` 扩展配置模型，以及前端 TypeScript 零 `any` 接口字典**。

---

## 1. 16 张 `archdoc_*` 物理表索引、约束与查询不变量总表

### 1.1 表级索引与唯一约束冻结清单

| # | 表名（强制 `archdoc_` 前缀） | 核心唯一键 (`UNIQUE`) | 高频查询索引 | 删除/保留策略 |
| :-: | :--- | :--- | :--- | :--- |
| 1 | `archdoc_doc_types` | `(slug)` | `(is_active)`, `(latest_revision_id)` | 无已发布引用时可物理删；否则 `409` 改停用 |
| 2 | `archdoc_doc_type_revisions` | `(doc_type_id, revision_no)` | `(doc_type_slug)`, `(aggregate_hash)` | 不可变追加，永不物理删除 |
| 3 | `archdoc_repo_snapshots` | `(repo_id, subdirectory, ref_name, commit_sha, ir_schema_version)` | `(last_accessed_at)` | 元数据保留；缓存文件可 LRU 回收 |
| 4 | `archdoc_code_ir_facets` | `(snapshot_id, facet_name)` | `(last_accessed_at)`, `(evicted_at)` | 文件可回收并标 `evicted_at`，元数据保留 |
| 5 | `archdoc_gen_tasks` | `PK (id)` | `(repo_id, subdirectory)`, `(status)`, `(priority)`, `(target_document_id)`, `(triggered_by)`, `(created_at)` | 不可变审计轨迹，永不物理删除 |
| 6 | `archdoc_gen_slot_tasks` | `(task_id, doc_type_slug, slot_id)` | `(document_id)`, `(status)` | 随主任务保留，支撑重试与 P1 断点恢复 |
| 7 | `archdoc_documents` | `(repo_id, subdirectory, ref_name, doc_type_slug)` | `(status)`, `(doc_type_slug)`, `(published_commit_sha)` | 撤回退回 `draft`，逻辑主记录不删 |
| 8 | `archdoc_document_revisions` | `(document_id, revision_no)` | `(snapshot_id)`, `(doc_type_revision_id)`, `(created_by)` | 不可变追加，回滚亦追加新行 |
| 9 | `archdoc_section_evidences` | `(document_id, revision_id, slot_id)` | `(document_id)`, `(revision_id)`, `(is_orphan)` | 随不可变修订绑定，永不原地覆盖历史修订 |
| 10 | `archdoc_chat_sessions` | `PK (id)` | `(document_id)`, `(target_slot_id)`, `(created_by)` | 持久化会话轨迹 |
| 11 | `archdoc_chat_messages` | `PK (id)` | `(session_id)`, `(proposal_status)` | 持久化消息与 Diff 提案 |
| 12 | `archdoc_repo_remote_heads` | `(repo_id, ref_name)` | `(next_retry_at)` | `UPSERT` 原地更新巡检与退避态 |
| 13 | `archdoc_document_drifts` | `(document_id, drift_type)` | `(has_drift)` | `UPSERT` 原地更新当前双维漂移快照 |
| 14 | `archdoc_llm_configs` | `(name)` | `(is_default)` | 内网平等模型池配置（手动切换全局唯一默认模型） |
| 15 | `archdoc_llm_usage_logs` | `PK (id)` | `(task_id)`, `(document_id)`, `(repo_id)`, `(doc_type_slug)`, `(doc_type_revision_id)`, `(stage)`, `(created_at)` | 只追加观测流水 |
| 16 | `archdoc_quality_metrics` | `PK (id)` | `(document_id)`, `(repo_id)`, `(doc_type_slug, doc_type_revision_id)`, `(metric_event)`, `(created_at)` | 只追加度量事件 |

### 1.2 核心数据不变量（Invariants）
1. **草稿/发布双视角物理隔离不变量**：
   - `Document.CurrentRevisionID` 永远指向最新工作修订（可为未审批草稿）；
   - `Document.PublishedRevisionID` 仅在 `status == "approved"` 时非空；普通用户查询任何文档内容、证据、导出，SQL 必须关联 `PublishedRevisionID`（严禁关联 `CurrentRevisionID`）。
2. **Spec Drift 基准不变量**：
   - 计算已发布文档的 `Spec Drift` 时，基准修订号必须取 **`PublishedRevision.DocTypeRevisionID`**（而非草稿的 `Document.DocTypeRevisionID`），确保「已发布线上文档落后于最新模板」的判断精准无误。
3. **非目标槽位字节级不变性不变量**：
   - 任何单槽位修订（`manual_edit`、`chat_refine`、`slot_retry`）或增量刷新（`incremental_refresh`），所有非目标 `slot_id` 及所有 `locked` 槽位在新 `DocumentRevision.SectionsJSON` 中的 `content_sha256` 必须与上一修订**严格逐字节相等**。
4. **共享库零 DDL 与测试建表分离不变量**：
   - 生产启动 `models.InitDB()` 仅 `AutoMigrate` 上述 16 张 `archdoc_*` 表，**严禁**对 `users`/`departments`/`repositories`/`architecture_elements` 执行 DDL；
   - 物理隔离单测 `models.SetupTestDB(t, db)` 在 `testdb.SetupIsolatedDB(t)` 空库中先建出 5 张共享只读测试表，再迁移 16 张自有表。

---

## 2. 12 组 JSONB 字段内部强类型 Go Struct 契约

> 所有 JSONB 载荷均定义确定性 Go Struct（放置在 `pkg/` 纯引擎包或 `models/json_types.go` 中），严禁使用无约束 `map[string]any` 存取核心业务状态。

```go
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
	RedactedSecrets int            `json:"redacted_secrets,omitempty"` // 仅 config_keys / runtime_primitives
	ExtraCounts     map[string]int `json:"extra_counts,omitempty"`     // 如 proto_services, cpp_classes
}

// 3. GenTask.BudgetAdviceJSON (预算熔断结构化建议，SC-09)
type BudgetAdvice struct {
	EstimatedTokens  int                    `json:"estimated_tokens"`
	MaxTokensPerTask int                    `json:"max_tokens_per_task"`
	EffectiveFiles   int                    `json:"effective_files"`
	MaxFilesPerTask  int                    `json:"max_files_per_task"`
	TopDirectories   []BudgetDirectoryItem  `json:"top_directories"`
	TopDocTypes      []BudgetDocTypeItem    `json:"top_doc_types"`
	Suggestions      []string               `json:"suggestions"`
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
	Ownership        string `json:"ownership"`          // auto | hybrid | locked
	IsOrphan         bool   `json:"is_orphan"`
	ManuallyEdited   bool   `json:"manually_edited"`
	NeedsDriftReview bool   `json:"needs_drift_review"`
	Markdown         string `json:"markdown"`
	ContentSHA256    string `json:"content_sha256"`     // sha256(markdown) 十六进制，用于字节级不变性断言
}

// 6. DocumentRevision.VerifierReportJSON & ChatMessage.VerifierPreviewJSON (四重门禁报告，SC-04)
type VerifierReport struct {
	Passed               bool              `json:"passed"`
	CanApprove           bool              `json:"can_approve"`
	BlockerCount         int               `json:"blocker_count"`
	ErrorCount           int               `json:"error_count"`
	WarningCount         int               `json:"warning_count"`
	SlotCoverageRate     float64           `json:"slot_coverage_rate"`
	MissingRequiredSlots []string          `json:"missing_required_slots"`
	UnverifiedRefs       []UnverifiedRef   `json:"unverified_refs"`
	MermaidIssues        []MermaidIssue    `json:"mermaid_issues"`
	SecretFindings       []SecretFinding   `json:"secret_findings"`
}

type UnverifiedRef struct {
	SlotID   string `json:"slot_id"`
	RefKind  string `json:"ref_kind"` // file_path | symbol | route | table | config_key
	Value    string `json:"value"`
	Severity string `json:"severity"` // warning | error
}

type MermaidIssue struct {
	SlotID     string `json:"slot_id"`
	BlockIndex int    `json:"block_index"`
	DiagramType string `json:"diagram_type"`
	IssueCode  string `json:"issue_code"` // mermaid_syntax_error | mermaid_disallowed_type | mermaid_complexity_warning
	Severity   string `json:"severity"`   // error | warning
	NodeCount  int    `json:"node_count"`
	EdgeCount  int    `json:"edge_count"`
	Message    string `json:"message"`
}

type SecretFinding struct {
	SlotID       string `json:"slot_id"`
	RuleID       string `json:"rule_id"`        // key_blacklist | pem_private_key | token_prefix | high_entropy
	MatchedMask  string `json:"matched_mask"`   // 仅记录脱敏片段（如 "db_password=sec***"），严禁落库明文！
	LineOffset   int    `json:"line_offset"`
	Severity     string `json:"severity"`       // 固定 "blocker"
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
	Reason         string   `json:"reason"`      // file_removed_or_renamed | file_modified | symbol_removed | symbol_modified | slot_added | slot_removed | slot_modified | prompt_changed
	ChangeKind     string   `json:"change_kind"` // added | modified | removed
	ChangedSymbols []string `json:"changed_symbols,omitempty"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
}

// 10. DocTypeRevision.SlotsSnapshotJSON (对齐 03-doctype-drift-and-publishing.md §1.2 & ADR-05 §5.4)
type SlotDefinition struct {
	SlotID            string   `json:"slot_id"`
	Title             string   `json:"title"`
	Ownership         string   `json:"ownership"` // auto | hybrid | locked
	Required          bool     `json:"required"`
	IRFacets          []string `json:"ir_facets"`
	TokenBudget       int      `json:"token_budget"`
	MinChars          int      `json:"min_chars"`
	InstructionHint   string   `json:"instruction_hint,omitempty"`
	EnableSourceTools bool     `json:"enable_source_tools,omitempty"`
	MaxToolRounds     int      `json:"max_tool_rounds,omitempty"`
}

// 11. QualityMetric.SlotDiffRatiosJSON: map[slot_id]float64 (例如 {"core_request_flows": 0.24})
// 12. DocType.IRFacetsJSON & GenTask.DocTypesJSON & GenTask.TargetSlotsJSON: []string
```

---

## 3. 全局枚举常量与状态机合法转移矩阵

所有后端模型校验函数与前端联合类型共享以下冻结枚举集（全部在 `models/enums.go` 中实现 `IsValid*` 白名单校验）：

| 枚举域 | 合法枚举值集合 | 备注 |
| :--- | :--- | :--- |
| **`FacetName`** | `tree` \| `build` \| `deps` \| `config_keys` \| `symbols` \| `idl_contracts` \| `http_routes` \| `data_models` \| `runtime_primitives` | 9 大 Canonical Code IR 分片 |
| **`SectionOwnership`** | `auto` \| `hybrid` \| `locked` | 章节所有权模式 |
| **`DocumentStatus`** | `draft` \| `approved` | `approved` 仅通过发布门禁进入，撤回退回 `draft` |
| **`RevisionSource`** | `ai_generate` \| `incremental_refresh` \| `manual_edit` \| `chat_refine` \| `rollback` | 修订来源追踪 |
| **`TaskKind`** | `generate` \| `regenerate` \| `incremental_refresh` | 任务类型 |
| **`TaskPriority`** | `interactive` \| `batch` | `F-27` 双优先级调度队列 |
| **`TaskStatus`** | `pending` \| `extracting` \| `generating` \| `verifying` \| `ready` \| `failed` \| `canceled` | 主任务状态机 |
| **`SlotTaskStatus`** | `pending` \| `running` \| `completed` \| `failed` \| `skipped_locked` \| `canceled` | 槽位子任务状态机 |
| **`ProposalStatus`** | `none` \| `pending` \| `accepted` \| `rejected` \| `superseded` | Chat Diff 提案状态 |
| **`DriftType`** | `code` \| `spec` | 双维漂移类型 |
| **`DriftPrecision`** | `section_level` \| `repo_level` | 漂移定位精度 |
| **`MetricEvent`** | `ai_draft_verified` \| `doc_published` \| `chat_proposal_resolved` | `F-26` 度量事件类型 |
| **`SymbolKind`** | `file` \| `config_key` \| `proto_rpc` \| `http_route` \| `data_model` \| `cpp_class` \| `cpp_struct` \| `cpp_func` \| `java_class` \| `java_method` \| `py_class` \| `py_func` \| `runtime_primitive` | `ADR-03` 锚点符号类别 |

### 主任务状态机合法转移校验表 (`CanTransitionTaskStatus(from, to)`)
| 当前状态 (`from`) | 允许转移目标 (`to`) | 非法转移处理 |
| :--- | :--- | :--- |
| `pending` | `extracting`, `canceled`, `failed` | 返回 `ErrInvalidStateTransition` |
| `extracting` | `generating`, `failed`, `canceled` | 返回 `ErrInvalidStateTransition` |
| `generating` | `verifying`, `failed`, `canceled` | 返回 `ErrInvalidStateTransition` |
| `verifying` | `ready`, `failed` | 返回 `ErrInvalidStateTransition` |
| `ready` / `failed` / `canceled` | *(终态，禁止任何状态转移)* | 返回 `ErrInvalidStateTransition` |

---

## 4. `services/` 领域仓储接口契约（依赖倒置防 `models.DB` 耦合）

严格落实 `01-system-architecture.md` §4.2 与 `make lint-arch` 红线 2（`pkg/` 与 `services/` 严禁访问全局 `models.DB`）：`services/` 仅依赖以下仓储接口，由 `models/` 下的 `*Store` 结构体实现并在 `main.go` 组合根注入。

```go
package services

// SharedMasterRepo 只读访问平台共享公共表（users / departments / repositories / architecture_elements）
type SharedMasterRepo interface {
	ListDepartments(ctx context.Context) ([]commonModels.Department, error)
	GetRepository(ctx context.Context, repoID uint) (*commonModels.Repository, error)
	ListActiveRepositories(ctx context.Context, filter RepoQueryFilter) ([]commonModels.Repository, int64, error)
	ListArchElementsByRepo(ctx context.Context, repoID uint) ([]models.ArchitectureElement, error)
	FindArchElementsByRepoAndOwner(ctx context.Context, repoID, ownerID uint) ([]models.ArchitectureElement, error)
}

// DocTypeRepo 管理 archdoc_doc_types 与 archdoc_doc_type_revisions
type DocTypeRepo interface {
	List(ctx context.Context, activeOnly bool) ([]models.DocType, error)
	GetBySlug(ctx context.Context, slug string) (*models.DocType, *models.DocTypeRevision, error)
	ListRevisions(ctx context.Context, slug string) ([]models.DocTypeRevision, error)
	GetRevisionByID(ctx context.Context, revID uint) (*models.DocTypeRevision, error)
	UpsertFromDirectorySync(ctx context.Context, dt *models.DocType, newRev *models.DocTypeRevision) (createdRev bool, err error)
	CountApprovedDocumentsBySlug(ctx context.Context, slug string) (int64, error)
	DeleteIfUnused(ctx context.Context, slug string) error
}

// SnapshotRepo 管理 archdoc_repo_snapshots 与 archdoc_code_ir_facets
type SnapshotRepo interface {
	FindByKey(ctx context.Context, repoID uint, subdir, refName, commitSHA, irSchemaVer string) (*models.RepoSnapshot, error)
	UpsertSnapshot(ctx context.Context, snap *models.RepoSnapshot) error
	UpsertFacet(ctx context.Context, facet *models.CodeIRFacet) error
	ListFacetsBySnapshot(ctx context.Context, snapshotID uint) ([]models.CodeIRFacet, error)
	TouchFacetAccess(ctx context.Context, facetIDs []uint, accessedAt time.Time) error
	ListEvictableFacets(ctx context.Context, excludeSnapshotIDs []uint, limit int) ([]models.CodeIRFacet, error)
	MarkFacetEvicted(ctx context.Context, facetID uint, evictedAt time.Time) error
}

// TaskRepo 管理 archdoc_gen_tasks 与 archdoc_gen_slot_tasks
type TaskRepo interface {
	CreateTask(ctx context.Context, task *models.GenTask) error
	GetTaskWithSlots(ctx context.Context, taskID uint) (*models.GenTask, []models.GenSlotTask, error)
	ListTasks(ctx context.Context, filter TaskListFilter) ([]models.GenTask, int64, error)
	TransitionTaskStatus(ctx context.Context, taskID uint, from, to string, updates map[string]any) error
	CreateSlotTasks(ctx context.Context, slots []models.GenSlotTask) error
	UpdateSlotTask(ctx context.Context, slotTask *models.GenSlotTask) error
	GetSlotTask(ctx context.Context, taskID, slotTaskID uint) (*models.GenSlotTask, error)
}

// DocumentRepo 管理 archdoc_documents / archdoc_document_revisions / archdoc_section_evidences
type DocumentRepo interface {
	GetByID(ctx context.Context, docID uint) (*models.Document, error)
	GetByScope(ctx context.Context, repoID uint, subdir, refName, docTypeSlug string) (*models.Document, error)
	ListByScope(ctx context.Context, repoID uint, subdir, refName string, approvedOnly bool) ([]models.Document, error)
	ListApprovedDocuments(ctx context.Context) ([]models.Document, error)
	GetRevisionWithSections(ctx context.Context, docID uint, revisionNo int) (*models.DocumentRevision, []models.SectionEvidence, error)
	ListRevisions(ctx context.Context, docID uint) ([]models.DocumentRevision, error)
	// AppendRevisionWithOptimisticLock 在同一事务中校验 baseRevisionNo、写入新 Revision & SectionEvidences 并推进 Document.CurrentRevisionNo
	AppendRevisionWithOptimisticLock(ctx context.Context, doc *models.Document, baseRevisionNo int, rev *models.DocumentRevision, sections []models.SectionEvidence) error
	UpdateSectionOwnership(ctx context.Context, docID, revID uint, slotID, ownership string, clearDriftReview bool) error
	MarkPublished(ctx context.Context, docID, revID uint, commitSHA, relPath string, publishedBy uint, publishedAt time.Time) error
	MarkUnpublished(ctx context.Context, docID uint) error
}

// ChatRepo 管理 archdoc_chat_sessions 与 archdoc_chat_messages (W2)
type ChatRepo interface {
	GetOrCreateSession(ctx context.Context, docID uint, slotID string, userID uint, sessionID *uint) (*models.ChatSession, error)
	ListSessionsWithMessages(ctx context.Context, docID uint) ([]models.ChatSession, map[uint][]models.ChatMessage, error)
	CreateMessage(ctx context.Context, msg *models.ChatMessage) error
	GetMessage(ctx context.Context, docID, msgID uint) (*models.ChatMessage, error)
	UpdateProposalStatus(ctx context.Context, msgID uint, status string, appliedRevID *uint) error
	SupersedePendingProposals(ctx context.Context, docID uint, slotID string, exceptMsgID uint) error
}

// DriftRepo 管理 archdoc_repo_remote_heads 与 archdoc_document_drifts (W2)
type DriftRepo interface {
	ListPollableRepos(ctx context.Context, now time.Time) ([]PollableRepoRecord, error)
	UpsertRemoteHead(ctx context.Context, head *models.RepoRemoteHead) error
	UpsertDocumentDrift(ctx context.Context, drift *models.DocumentDrift) error
	ListDriftedDocuments(ctx context.Context, filter DriftQueryFilter) ([]DriftAggregateRecord, int64, error)
	GetDriftSummary(ctx context.Context) (*DriftSummaryRecord, error)
}

// GovernanceRepo 管理 archdoc_llm_configs / archdoc_llm_usage_logs / archdoc_quality_metrics
type GovernanceRepo interface {
	ListLLMConfigs(ctx context.Context) ([]models.LLMConfig, error)
	ListLLMConfigs(ctx context.Context) ([]models.LLMConfig, error)
	GetLLMConfigByID(ctx context.Context, id uint) (*models.LLMConfig, error)
	GetLLMConfigByName(ctx context.Context, name string) (*models.LLMConfig, error)
	GetDefaultLLMConfig(ctx context.Context) (*models.LLMConfig, error)
	CreateLLMConfig(ctx context.Context, cfg *models.LLMConfig) error
	UpdateLLMConfig(ctx context.Context, cfg *models.LLMConfig) error
	SetDefaultLLMConfig(ctx context.Context, id uint) (*models.LLMConfig, error)
	DeleteLLMConfig(ctx context.Context, id uint) error
	UpdateLLMProbeResult(ctx context.Context, id uint, status string, latencyMs int64, probedAt time.Time) error
	RecordLLMUsage(ctx context.Context, log *models.LLMUsageLog) error
	RecordQualityMetric(ctx context.Context, metric *models.QualityMetric) error
	AggregateMetricsOverview(ctx context.Context, filter MetricsFilter) (*MetricsOverviewRecord, error)
	CompareMetricsByRevisions(ctx context.Context, docTypeSlug string, days int) ([]RevisionMetricPoint, error)
	AggregateSlotHeatmap(ctx context.Context, docTypeSlug string, revID *uint, days int) ([]SlotHeatmapPoint, error)
}
```

---

## 5. `config/config.go` 与 `config.yaml` 扩展配置数据模型

为支撑本地存储目录、调度并发、远端巡检与 LLM HTTP 客户端标识，在现有 `Config` 结构体上扩展 4 个配置节（均提供安全默认值，向后兼容既有 `config.yaml`）：

```go
type StorageSection struct {
	DataDir          string `yaml:"data_dir"`           // 统一根数据目录，默认 "./data"（内含 sandbox_dir/ ir_cache/ doc_types/ publish_dir/）
	PublishDir       string `yaml:"publish_dir"`        // 可选独立覆盖 publish_dir 路径，默认 "<data_dir>/publish_dir"
	SandboxMaxMB     int64  `yaml:"sandbox_max_mb"`     // 默认 20480 (20 GB)
	IRCacheMaxMB     int64  `yaml:"ir_cache_max_mb"`    // 默认 10240 (10 GB)
	MaxFileBytes     int64  `yaml:"max_file_bytes"`     // L1 降噪单文件上限，默认 1048576 (1 MB)
	ReapIntervalMins int    `yaml:"reap_interval_mins"` // LRU 回收周期，默认 10 分钟
}

type SchedulerSection struct {
	MaxLLMConcurrency   int `yaml:"max_llm_concurrency"`    // 全局 LLM 并发信号量，默认 8
	MaxUserConcurrency  int `yaml:"max_user_concurrency"`   // 单用户最大并发任务数，默认 2
	InteractiveQueueCap int `yaml:"interactive_queue_cap"`  // 高优交互队列容量，默认 64
	BatchQueueCap       int `yaml:"batch_queue_cap"`        // 低优批量队列容量，默认 256
}

type DriftSection struct {
	PollEnabled         bool `yaml:"poll_enabled"`          // 默认 true（单元测试中可关闭后台 Ticker）
	PollIntervalMinutes int  `yaml:"poll_interval_minutes"` // 默认 15 分钟
	PollConcurrency     int  `yaml:"poll_concurrency"`      // 轻量 ls-remote 并发上限，默认 4
}

type LLMSection struct {
	UserAgent string `yaml:"user_agent"` // 可选自定义访问 LLM 的 User-Agent；留空默认使用 "Code-Archdoc/<version>"
}
```

---

## 6. 前端 TypeScript 强类型契约字典 (`frontend/src/types/`)

严格遵守工作区上位准则：**严禁滥用 `any` 类型**。阶段四前端拆分为 `domain.ts`、`api.ts`、`sse.ts` 三个类型模块，核心冻结定义如下：

```typescript
// frontend/src/types/domain.ts

export type FacetName =
  | 'tree' | 'build' | 'deps' | 'config_keys' | 'symbols'
  | 'idl_contracts' | 'http_routes' | 'data_models' | 'runtime_primitives';
export type SectionOwnership = 'auto' | 'hybrid' | 'locked';
export type DocumentStatus = 'draft' | 'approved';
export type RevisionSource =
  | 'ai_generate' | 'incremental_refresh' | 'manual_edit' | 'chat_refine' | 'rollback';
export type TaskStatus =
  | 'pending' | 'extracting' | 'generating' | 'verifying' | 'ready' | 'failed' | 'canceled';
export type SlotTaskStatus =
  | 'pending' | 'running' | 'completed' | 'failed' | 'skipped_locked' | 'canceled';
export type ProposalStatus = 'none' | 'pending' | 'accepted' | 'rejected' | 'superseded';

export interface PaginatedResponse<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface ApiErrorBody {
  error: string;
  code?: string;
  details?: Record<string, unknown>;
}

export interface EvidenceAnchor {
  anchor_id: string;
  anchor_key: string;
  file_path: string;
  symbol_kind: string;
  fq_symbol: string;
  content_fingerprint: string;
  commit_sha: string;
  line_start: number;
  line_end: number;
  confidence: 'exact' | 'heuristic';
}

export interface VerifierReport {
  passed: boolean;
  can_approve: boolean;
  blocker_count: number;
  error_count: number;
  warning_count: number;
  slot_coverage_rate: number;
  missing_required_slots: string[];
  unverified_refs: Array<{ slot_id: string; ref_kind: string; value: string; severity: 'warning' | 'error' }>;
  mermaid_issues: Array<{
    slot_id: string;
    block_index: number;
    diagram_type: string;
    issue_code: string;
    severity: 'error' | 'warning';
    node_count: number;
    edge_count: number;
    message: string;
  }>;
  secret_findings: Array<{
    slot_id: string;
    rule_id: string;
    matched_mask: string;
    line_offset: number;
    severity: 'blocker';
  }>;
}

export interface SectionVO {
  slot_id: string;
  section_order: number;
  title: string;
  ownership: SectionOwnership;
  is_orphan: boolean;
  manually_edited: boolean;
  needs_drift_review: boolean;
  content_sha256: string;
  markdown: string;
  evidences: EvidenceAnchor[];
}

export interface AffectedSlotDrift {
  slot_id: string;
  slot_title: string;
  ownership: SectionOwnership;
  reason: string;
  change_kind: 'added' | 'modified' | 'removed';
  changed_symbols?: string[];
  changed_files?: string[];
}

export interface DriftDimensionVO {
  has_drift: boolean;
  precision: 'section_level' | 'repo_level';
  affected_slot_count: number;
  affected_slots: AffectedSlotDrift[];
  detected_at: string;
}

export interface DocStatusSummaryVO {
  document_id: number;
  status: DocumentStatus;
  current_revision_no: number;
  published_revision_no: number;
  has_code_drift: boolean;
  has_spec_drift: boolean;
  affected_slot_count: number;
  verifier_passed: boolean;
  can_approve: boolean;
  updated_at: string;
}

export interface RepoScopeCoverageVO {
  repo_id: number;
  repo_name: string;
  department_id: number;
  department_name: string;
  subdirectory: string;
  scope_type: 'repo' | 'subdir';
  arch_element_id: number | null;
  arch_element_name: string;
  branch: string;
  owner_id: number;
  owner_name: string;
  can_write: boolean;
  latest_commit_sha: string;
  remote_head_sha: string;
  active_task_id: number | null;
  doc_statuses: Record<string, DocStatusSummaryVO>;
}

export interface SlotDefinition {
  slot_id: string;
  title: string;
  ownership: SectionOwnership;
  required: boolean;
  ir_facets: FacetName[];
  token_budget: number;
  min_chars: number;
  instruction_hint?: string;
}

export interface DocTypeSummaryVO {
  id: number;
  slug: string;
  display_name: string;
  description: string;
  is_builtin: boolean;
  is_active: boolean;
  preferred_model: string;
  timeout_seconds: number;
  ir_facets: FacetName[];
  slot_count: number;
  latest_revision_id: number | null;
  latest_revision_no: number;
  latest_aggregate_hash: string;
  published_doc_count: number;
  updated_at: string;
}

export interface LLMConfigVO {
  id: number;
  name: string;
  base_url: string;
  model_name: string;
  api_key_masked: string;
  has_api_key: boolean;
  timeout_seconds: number;
  max_retries: number;
  max_tokens_per_task: number;
  max_files_per_task: number;
  is_active: boolean;
  is_default: boolean;
  last_probe_status: 'unknown' | 'ok' | 'failed';
  last_probe_latency_ms: number;
  last_probed_at: string | null;
}

// frontend/src/types/sse.ts — Chat 精修 SSE 强类型辨识联合
export type ChatRefineSSEEvent =
  | { event: 'meta'; data: { session_id: number; message_id: number; slot_id: string; base_revision_no: number; model_name: string } }
  | { event: 'delta'; data: { text: string } }
  | { event: 'diff_proposal'; data: { proposal_id: number; slot_id: string; before_md: string; after_md: string; unified_diff: string; cited_anchors: EvidenceAnchor[] } }
  | { event: 'verifier_preview'; data: Pick<VerifierReport, 'can_approve' | 'blocker_count' | 'error_count' | 'warning_count'> & { issues: unknown[] } }
  | { event: 'done'; data: { message_id: number; prompt_tokens: number; cached_prompt_tokens: number; completion_tokens: number; latency_ms: number } }
  | { event: 'error'; data: { code: string; message: string } };
```


---

## 7. ADR-04 契约增量（`M8`~`M10`）

### 7.1 新增表与列（16 → 18 张表，+2 列）
- 表 17 `archdoc_repo_module_policies`：唯一键 `(repo_id)`；
- 表 18 `archdoc_repo_modules`：唯一键 `(repo_id, subdirectory)`，索引 `(repo_id)`、`(arch_element_id)`、`(status)`；**严禁含负责人字段**；
- `archdoc_repo_snapshots`：新增可空索引列 `base_snapshot_id`；
- `archdoc_gen_tasks`：新增可空索引列 `parent_task_id`。

### 7.2 新增枚举与 JSONB 字段
- `FacetName`：新增派生分片 `"module_context"`（仅切片快照使用）；
- `TaskKind`：新增 `"batch"`；批次父任务允许 `generating -> ready`；
- `SymbolKind`：新增 `"module_doc"`；
- `ModuleStrategy`：`arch_elements | top_level_dirs | merged`；
- `ModuleStatus`：`active | missing | ignored`；
- `EvidenceAnchor` / `CitedAnchorCandidate`：新增可选字段 `scope?: "module" | "repo"`；
- `SnapshotStats`：新增可选字段 `degraded_reason?: string`；
- `BudgetAdvice`：新增可选字段 `module_breakdown?: BudgetModuleItem[]`；
- `AffectedSlotDrift.Reason`：新增 `external_contract_modified`、`external_contract_removed`、`module_digest_changed`、`module_scope_changed`。

### 7.3 `config/config.go` 新增配置项
- `ExtractSection`（`yaml:"extract"`）：`MaxRepoFiles int`（默认 `50000`）、`MaxRepoDurationSeconds int`（默认 `600`）；
- `SchedulerSection`：新增 `MaxTokensPerBatch int`（默认 `1000000`）。

---

## 8. ADR-05 契约增量（`M11`：蓝图双模融合编辑器与 `Spec Drift` 静态骨架精化）

### 8.1 后端 `SlotDefinition`、`SpecSlotMeta` 与 `CreateDocTypeRequest` 增量

1. **`models.SlotDefinition` (`models/json_types.go`) & `toModelSlotDefs` (`services/doctype_service.go`)**：
   - 新增 `EnableSourceTools bool`（`json:"enable_source_tools,omitempty"`）与 `MaxToolRounds int`（`json:"max_tool_rounds,omitempty"`），并在 `toModelSlotDefs` 中从 `doctype.SlotSpec` 完整拷贝。
2. **`drift.SpecSlotMeta` (`pkg/drift/spec_drift.go`) & `unmarshalSpecSlotMetas` (`services/doctype_service.go`)**：
   - 新增 `EnableSourceTools bool` 与 `MaxToolRounds int`，在 `slotSpecChanged` 中纳入比对（当 `EnableSourceTools == false` 时将 `MaxToolRounds` 归一化为 `0`）；
   - `ComputeSpecDrift` 新增纯函数 `normalizeTemplateStaticSkeleton(templateMD string, slots []SpecSlotMeta) string`，以 `boundSkeleton != latestSkeleton` 替代裸 `boundTemplateMD != latestTemplateMD`。
3. **`services.CreateDocTypeRequest` (`services/doctype_service.go`) & `doctype.Meta.Validate` (`pkg/doctype/schema.go`)**：
   - `CreateDocTypeRequest` 新增可选字段 `RepoIRFacets []string`、`ContextInputs []string`、`ModuleDigestSource *doctype.ModuleDigestSourceSpec`；
   - `Meta.Validate` 的 `context_inputs` 允许值白名单支持 `"code_ir"`、`"module_context"`、`"module_digests"`。

### 8.2 前端 `DocBlueprint` 与 `DocType` 治理策略 TypeScript 契约

```typescript
// frontend/src/types/domain.ts 增量
export type CanonicalFacetName = Exclude<FacetName, 'module_context'>;
export type ContextInputName = 'code_ir' | 'module_context' | 'module_digests';

export interface ScopeProfileConfig {
  include_globs?: string[];
  exclude_globs?: string[];
  max_depth?: number;
  include_tests: boolean;
  include_legacy_docs?: boolean;
}

export interface VerifyPolicyConfig {
  strict_symbol_check: boolean;
  require_mermaid: boolean;
  allowed_mermaid_types?: string[];
  max_mermaid_nodes?: number;
  max_mermaid_edges?: number;
  block_on_secret_leak?: boolean;
}

export interface ModuleDigestSourceConfig {
  doc_type_slug: string;
  slot_id: string;
  max_chars?: number;
  fallback_order?: string[];
}

// frontend/src/types/api.ts 增量
export interface CreateDocTypePayload {
  slug: string;
  display_name: string;
  short_name?: string;
  category?: string;
  description: string;
  is_active?: boolean;
  preferred_model?: string;
  timeout_seconds: number;
  max_output_tokens?: number;
  scope_profile?: ScopeProfileConfig;
  ir_facets: FacetName[];
  repo_ir_facets?: FacetName[];
  context_inputs?: ContextInputName[];
  module_digest_source?: ModuleDigestSourceConfig;
  verify_policy?: VerifyPolicyConfig;
  slots: SlotDefinition[];
  template_md: string;
  generation_prompt_md: string;
  refine_prompt_md: string;
}

// frontend/src/utils/docTypeBlueprint.ts 纯函数契约
export type BlueprintHeadingLevel = 2 | 3 | 4;

export interface StaticBlueprintBlock {
  kind: 'static';
  key: string;
  markdown: string;
}

export interface SlotBlueprintBlock {
  kind: 'slot';
  key: string;
  emitHeading: boolean;
  headingLevel: BlueprintHeadingLevel;
  slot: SlotDefinition;
  divergedTemplateHeading?: string;
}

export type BlueprintBlock = StaticBlueprintBlock | SlotBlueprintBlock;

export interface DocBlueprint {
  blocks: BlueprintBlock[];
}

export interface BlueprintDiagnostic {
  line: number;
  severity: 'error' | 'warning';
  code: string;
  message: string;
}
```

---

## 9. ADR-06 契约增量（`M12`：审阅批注篮、多槽位提案组与 10 事件 SSE）

### 9.1 新增表与索引不变量（18 → 20 张表）

| # | 表名（强制 `archdoc_` 前缀） | 核心唯一键 (`UNIQUE`) | 高频查询索引 | 删除/保留策略 |
| :-: | :--- | :--- | :--- | :--- |
| 19 | `archdoc_review_comments` | `(document_id, comment_no)` | `(document_id, status)`, `(author_id)`, `(slot_id)`, `(persist_for_regen)`, `(proposal_group_msg_id)` | 仅限 `open`/`dismissed` 且 `resolved_revision_id IS NULL` 草稿可物理删；删除后 `Document.LastCommentNo` 单调高水位确保旧号永不复用 |
| 20 | `archdoc_chat_proposal_items` | `(message_id, slot_id)` | `(document_id, slot_id, status)`, `(message_id)` | 不可变审阅记录，按 `pending -> accepted \| rejected \| superseded` 流转 |

- `archdoc_documents`：新增 `last_comment_no`（`int, default 0, not null`）；
- `archdoc_chat_messages`：新增 `intent`（`revise | ask`）、`scope_type`（`slot | inline | global | review_batch`）、`plan_summary_json`（`jsonb default '{}'`）、`context_refs_json`（`jsonb default '[]'`）。

### 9.2 JSONB Schema、状态机与仓储接口契约

1. **`ReviewCommentAnchor` (`archdoc_review_comments.anchor_json`)**：`{ block_index?, block_type?, quote?, prefix?, suffix?, source_line_start?, source_line_end? }`（`quote <= 500` 字符）。
2. **`ReviewComment` 生命周期状态机**：
   - `open -> proposed`（综合精修实际覆盖该批注所属槽位并产出 `ChatProposalItem` 后触发）；
   - `proposed -> resolved`（对应提案项在 `/apply` 或 `/accept` 落盘生效时触发）；
   - `proposed -> open`（对应提案项被 `/apply` 未勾选排除、单项或整组 `/reject`、客户端 SSE 中止回滚，或被手工直编/新提案采纳/回滚/重生成置为 `superseded` 时自动回退）；
   - 手动流转：`open <-> resolved | dismissed`。
3. **新增与扩展仓储接口 (`services/repos.go`)**：
   - `ReviewCommentRepo`：`Create`（行锁 `Document` 分配单调 `comment_no`）、`ListByDocument`、`GetByID`、`ListByIDs`、`Update`、`DeleteDraft`、`MarkProposed`、`MarkResolved`、`RevertProposedByMessageID`、`RevertProposedByCommentIDs`、`ListPersistentByDocument`；
   - `ChatRepo` 新增：`CreateProposalItem`、`ListProposalItemsByMessageIDs`、`ListProposalItemsByMessage`、`GetProposalItem`、`UpdateProposalItem`、`DeleteProposalItemsByMessage`、`SupersedePendingProposalsBySlots`。

### 9.3 前端 TypeScript 类型与 10 事件 SSE 增量

```typescript
// frontend/src/types/domain.ts (ADR-06 增量)
export type ReviewScopeType = 'inline' | 'section' | 'global';
export type ReviewCategory = 'correction' | 'supplement' | 'concise' | 'global_rule' | 'question';
export type ReviewCommentStatus = 'open' | 'proposed' | 'resolved' | 'dismissed';
export type ChatIntentMode = 'revise' | 'ask';
export type ChatScopeType = 'slot' | 'inline' | 'global' | 'review_batch';

export interface ReviewCommentAnchor {
  block_index?: number;
  block_type?: string;
  quote?: string;
  prefix?: string;
  suffix?: string;
  source_line_start?: number;
  source_line_end?: number;
}

export interface ReviewCommentVO {
  id: number;
  document_id: number;
  comment_no: number;
  base_revision_no: number;
  author_id: number;
  origin_role: 'owner' | 'reviewer';
  scope_type: ReviewScopeType;
  slot_id: string;
  anchor: ReviewCommentAnchor;
  category: ReviewCategory;
  content: string;
  persist_for_regen: boolean;
  status: ReviewCommentStatus;
  proposal_group_msg_id?: number | null;
  resolved_revision_id?: number | null;
  created_at: string;
  updated_at: string;
}

export interface ChatProposalItemVO {
  item_id: number;
  message_id: number;
  slot_id: string;
  slot_title?: string;
  base_revision_no: number;
  before_md: string;
  after_md: string;
  final_applied_md?: string;
  unified_diff: string;
  cited_anchors: EvidenceAnchor[];
  verifier_preview?: ChatVerifierPreviewVO | null;
  addressed_comment_ids: number[];
  status: ProposalStatus;
  applied_revision_id?: number | null;
}

// frontend/src/types/sse.ts (10 事件联合成员增量)
// 新增 'plan' | 'stage' | 'proposal_item'；现有 'meta' / 'tool_call' / 'delta' / 'verifier_preview' 同步扩展可选字段。
```

---

## 10. ADR-07 契约增量（`M13`：单仓目标分支切换与远端分支发现）

### 10.1 表结构与仓储接口契约 (`models/` & `services/repos.go`)

1. **`archdoc_repo_module_policies` 扩展列**：
   - 新增 `target_branch`（`varchar(128), not null, default ''`），通过 `models.ResolveEffectiveBranch(repoBranch, targetBranch)` 与 `models.NormalizeRepoDefaultBranch(repoBranch)` 统一解析有效分支。
2. **新增仓储接口方法**：
   - `ModuleRepo`：`ListPoliciesByRepos(ctx, repoIDs)`、`UpdateTargetBranch(ctx, repoID, targetBranch, updatedBy)`（`OnConflict(repo_id)` 仅更新分支相关字段，保留既有发现策略）；
   - `TaskRepo`：`HasActiveTasksInRepo(ctx, repoID)`；
   - `DocumentRepo`：`RebindRepoBranch(ctx, repoID, newRefName)`（事务 `FOR UPDATE` 锁定整仓文档、冲突检出返回 `ErrMigrationConflict`、统一更新 `ref_name`）；
   - `DriftRepo`：`ReplaceRepoRemoteHead(ctx, head)`（事务清理同仓非目标分支历史 HEAD 记录并写入新记录）；`ListPollableRepos` 关联 `archdoc_repo_module_policies` 解析有效分支 `COALESCE(NULLIF(p.target_branch, ''), NULLIF(r.branch, ''), 'master')`。

### 10.2 前端 TypeScript 强类型契约 (`frontend/src/types/`)

```typescript
// domain.ts 增量
export interface RepoBranchItemVO {
  name: string;
  commit_sha: string;
  is_head: boolean;
  is_default: boolean;
  is_current: boolean;
}

export interface RepoBranchesVO {
  repo_id: number;
  default_branch: string;
  current_branch: string;
  is_overridden: boolean;
  remote_head_branch: string;
  branches: RepoBranchItemVO[];
}

export interface SwitchRepoBranchVO {
  repo_id: number;
  previous_branch: string;
  current_branch: string;
  default_branch: string;
  is_overridden: boolean;
  remote_head_sha: string;
  rebound_documents: number;
  drifted_documents: number;
  changed: boolean;
}

// RepoScopeCoverageVO 与 RepoScopesDetailResponse 新增可选兼容字段：
// default_branch?: string; is_branch_overridden?: boolean; can_switch_branch?: boolean;

// api.ts 增量
export interface SwitchRepoBranchPayload {
  branch: string;
}
```
