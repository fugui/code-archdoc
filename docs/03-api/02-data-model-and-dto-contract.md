# 代码设计文档反推 (`code-archdoc`) 数据模型冻结与前后端 DTO 契约

- **文档编号**：`02-data-model-and-dto-contract.md`
- **文档版本**：v1.7.0 (ADR-04 ~ ADR-11 Frozen Contract)
- **编制日期**：2026-11-24
- **归属阶段**：阶段三：接口与数据契约冻结 (`03-api`)
- **上游基线**：[`docs/02-design/05-database-schema.md`](../02-design/05-database-schema.md) (v1.5.0)、[`01-http-api-contract.md`](./01-http-api-contract.md) (v1.8.0)、[`ADR-05`](../05-adr/05-doctype-blueprint-dual-mode-editor.md) ~ [`ADR-11`](../05-adr/11-ref-doc-refine-studio.md)

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

---

## 11. ADR-08 契约增量（`M14`：单文档不变量、双引擎配置与前后端 DTO）

### 11.1 数据库增量列演进与核心不变量（坚守 20 表零新增表）

| 表名 / 模型 | 变更列 | 列定义 | 说明 |
| :--- | :--- | :--- | :--- |
| `archdoc_gen_tasks` (`models.GenTask`) | `Engine` *(新增)* | `gorm:"size:20;not null;default:'native';index" json:"engine"` | 生成引擎标识（`native` \| `opencode`） |
| `archdoc_llm_configs` (`models.LLMConfig`) | `OpenCodeModel` *(新增)* | `gorm:"size:128;not null;default:''" json:"opencode_model"` | 可选模式 B 原生 `provider/model` 映射 |
| `archdoc_llm_usage_logs` (`models.LLMUsageLog`) | `Engine` *(新增)*<br>`Stage` *(扩容)* | `Engine`: `gorm:"size:20;not null;default:'native';index" json:"engine"`<br>`Stage`: `size:24` 扩为 `size:32` | 区分引擎用量并容纳 `doc_gen_opencode`、`doc_gen_oc_continue`、`slot_retry_opencode`、`inc_refresh_opencode` |

**新增硬不变量**：
1. **单文档任务不变量**：非批量任务（`generate`、`regenerate`、`incremental_refresh`）的 `DocTypesJSON` 长度恒为 `1`，严格绑定唯一 `(Scope, DocType)`。
2. **Usage 归一化不变量**：无论 OpenCode Provider 是否将 `cache.read` 计入 `input`，落库前保证 `CachedPromptTokens <= PromptTokens`，确保 `QualityMetricsService` 计算的 `avg_cache_hit_ratio` 恒处于 `[0.0, 1.0]`。
3. **展示名列宽安全不变量**：写入 `GenSlotTask.ModelName` 与 `LLMUsageLog.ModelName`（列宽 `varchar(128)`）的 `[opencode] <label>` 字符串在写入前按 128 runes 安全截断。

### 11.2 领域枚举、过滤结构与 `TaskRepo` / `TaskScheduler` 契约

1. **`models/enums.go` 新增引擎常量与校验**：
   ```go
   const (
       EngineNative   = "native"
       EngineOpenCode = "opencode"
   )
   func IsValidEngine(v string) bool
   func NormalizeEngine(raw string) string
   ```
2. **`models.TaskListFilter` (`models/stores.go`)**：新增 `Engine string`（支持 `native` / `opencode` 筛选）。
3. **`TaskRepo` (`services/repos.go`) 新增方法**：
   - `CreateTasksBatch(ctx context.Context, tasks []*models.GenTask) error`（单 DB 事务批量插入拆单任务并回填自增 ID）；
   - `GetLatestEngineByDocumentID(ctx context.Context, docID uint) (string, error)`（通过 `archdoc_gen_slot_tasks.document_id` 索引关联主任务，返回该文档最近一次有效生成任务的 `engine`）；
   - `RecoverFailedTaskToReady(ctx context.Context, taskID uint, updates map[string]any) error`（仅用于单槽位独立重试成功将全败任务恢复为 `ready` 终态，`WHERE id = ? AND status = 'failed'` 原子条件更新）。
4. **`TaskScheduler` (`services/scheduler_service.go`) 两阶段预留契约**：
   - `ReserveSlots(priority string, count int) (commit func([]ScheduledJob) error, rollback func(), err error)`（检查 `!stopped` 与 `len(q) + reserved + count <= cap`，返回幂等 `commit` / `rollback` 闭包；`commit` 内部二次检查 `stopped` 防止并发停机泄漏）。
5. **`GenerateTaskSubmitResponse` (`services/generation_service.go`)**：
   ```go
   type GenerateTaskSubmitResponse struct {
       GenTaskDetailVO
       CreatedCount int               `json:"created_count"`
       Tasks        []GenTaskDetailVO `json:"tasks"`
   }
   ```

### 11.3 `config/config.go` 配置模型增量

- **`LLMSection` 增量**：
  - `FirstByteTimeoutSeconds int` (`yaml:"first_byte_timeout_seconds"`, 默认 `90`)
  - `IdleTimeoutSeconds int` (`yaml:"idle_timeout_seconds"`, 默认 `60`)
  - 提供 `EffectiveFirstByteTimeoutSeconds()` 与 `EffectiveIdleTimeoutSeconds()`（均校验并回退安全默认值）。
- **`OpenCodeSection` 新增（使用 `*bool` 避免 YAML 缺省 `opencode:` 段时 Go `bool` 零值 `false` 误关默认开关）**：
  ```go
  type OpenCodeSection struct {
      Enabled                *bool  `yaml:"enabled"`                  // 默认 true（nil 视为 true）
      BinaryPath             string `yaml:"binary_path"`              // 留空按 PATH -> ~/.opencode/bin/opencode -> /usr/local/bin/opencode 探测
      DefaultEngine          string `yaml:"default_engine"`           // native | opencode，默认 "native"
      MaxConcurrency         int    `yaml:"max_concurrency"`          // 默认 2
      TimeoutSeconds         int    `yaml:"timeout_seconds"`          // 默认 600
      IdleTimeoutSeconds     int    `yaml:"idle_timeout_seconds"`     // 默认 120
      ContinuationEnabled    *bool  `yaml:"continuation_enabled"`     // 默认 true（nil 视为 true）
      ContinuationMaxSeconds int    `yaml:"continuation_max_seconds"` // 默认 180
  }
  ```
  配套提供 `IsEnabled()`、`IsContinuationEnabled()`、`EffectiveDefaultEngine()`、`EffectiveMaxConcurrency()`、`EffectiveTimeoutSeconds()`、`EffectiveIdleTimeoutSeconds()`、`EffectiveContinuationMaxSeconds()`。

### 11.4 前端 TypeScript 强类型契约增量 (`frontend/src/types/` & `utils/`)

```typescript
// frontend/src/types/domain.ts 增量
export type GenEngine = 'native' | 'opencode';
export type TaskEngineFilter = '' | 'all' | GenEngine;

export interface EngineInfoVO {
  id: GenEngine;
  label: string;
  available: boolean;
  version?: string;
  reason?: string;
  default_model?: string;
}

export interface EnginesResponse {
  default_engine: GenEngine;
  engines: EngineInfoVO[];
}

export interface GenerateTaskSubmitResponse extends GenTaskDetailVO {
  created_count: number;
  tasks: GenTaskDetailVO[];
}

// GenTaskDetailVO & GenTaskSummaryVO 新增可选兼容字段：engine?: GenEngine;
// LLMConfigVO 新增可选字段：opencode_model?: string;

// frontend/src/types/api.ts 增量
// ArchdocErrorCode 新增：| 'ENGINE_UNAVAILABLE'
// GenerateTaskPayload 新增：doc_type?: string; engine?: GenEngine;
// BatchGenerateTaskPayload 新增：engine?: GenEngine;
// TaskListQuery 新增：engine?: TaskEngineFilter;
// IncrementalRefreshPayload 新增：engine?: GenEngine;
// UpsertLLMConfigPayload 新增：opencode_model?: string;

// frontend/src/utils/taskSubmitUtils.ts 纯函数契约
export function resolveInitialEngine(enginesResp: EnginesResponse | null, storedEngine: string | null): GenEngine;
export function normalizeGenerateSubmitResponse(resp: GenerateTaskSubmitResponse | GenTaskDetailVO): GenTaskDetailVO[];
export function summarizeSplitTasksProgress(tasks: GenTaskDetailVO[]): {
  total: number; ready: number; failed: number; canceled: number; running: number; allTerminal: boolean; overallPercent: number;
};
```

---

## 12. ADR-09 契约增量（`M15`：架构全景树表 DTO、仓库级未关联枚举与前端纯函数契约）

### 12.1 零数据库结构变更声明（坚守 20 表、共享表零 DDL）

`M15` 完全复用既有的 `architecture_elements`（只读共享表）、`repositories`（只读共享表）、`archdoc_documents`、`archdoc_document_revisions`、`archdoc_repo_modules`、`archdoc_repo_module_policies`、`archdoc_repo_snapshots` 与 `archdoc_gen_tasks`，**零新增表、零修改列、严禁对共享表执行任何 DDL**，100% 遵守 [`ADR-01`](../05-adr/01-microfrontend-and-table-prefix-isolation.md)。

### 12.2 后端冻结 Go 常量与 VO 结构体 (`services/repo_service.go`)

```go
// services/repo_service.go — ADR-09 冻结常量与 VO 契约

const (
	ArchBindingStatusBound        = "bound"
	ArchBindingStatusUnbound      = "unbound"
	ArchBindingStatusRepoInactive = "repo_inactive"

	ArchBindingFilterUnboundRepo = "unbound_repo"
)

type ArchSubsystemOptionVO struct {
	ID          uint   `json:"id"`
	Identifier  string `json:"identifier"`
	NameCn      string `json:"name_cn"`
	NameEn      string `json:"name_en"`
	ModuleCount int    `json:"module_count"`
	ScopeCount  int    `json:"scope_count"` // 向后兼容镜像 ModuleCount；/api/repos 中保持范围数
}

type ArchTreeSummaryVO struct {
	TotalModules        int `json:"total_modules"`
	BoundModules        int `json:"bound_modules"`
	CoveredModules      int `json:"covered_modules"`
	ApprovedDocs        int `json:"approved_docs"`
	DraftDocs           int `json:"draft_docs"`
	DriftedDocs         int `json:"drifted_docs"`
	UnassignedRepoCount int `json:"unassigned_repo_count"` // 按全量活跃仓口径统计，不受当前树筛选影响
}

type ArchTreeStatsVO struct {
	ModuleCount        int `json:"module_count"`
	BoundModuleCount   int `json:"bound_module_count"`
	CoveredModuleCount int `json:"covered_module_count"`
	ApprovedDocCount   int `json:"approved_doc_count"`
	DraftDocCount      int `json:"draft_doc_count"`
	DriftedDocCount    int `json:"drifted_doc_count"`
	BlockedDocCount    int `json:"blocked_doc_count"`
}

type ArchTreeNodeVO struct {
	ID            uint                  `json:"id"`
	Identifier    string                `json:"identifier"`
	NameCn        string                `json:"name_cn"`
	NameEn        string                `json:"name_en"`
	Type          string                `json:"type"` // "subsystem" | "group" | "module"
	ParentID      *uint                 `json:"parent_id"`
	OwnerID       *uint                 `json:"owner_id"`
	OwnerName     string                `json:"owner_name"`
	RepoID        *uint                 `json:"repo_id"`
	Subdirectory  string                `json:"subdirectory"`
	Description   string                `json:"description"`
	BindingStatus string                `json:"binding_status,omitempty"` // 仅 module: "bound" | "unbound" | "repo_inactive"
	Scope         *RepoScopeCoverageVO  `json:"scope,omitempty"`          // 仅 module 且 binding_status=="bound" 时非空
	Stats         ArchTreeStatsVO       `json:"stats"`
	Scopes        []RepoScopeCoverageVO `json:"scopes"`   // @deprecated: 固定返回 []，不再挂载代码仓数组
	Children      []*ArchTreeNodeVO     `json:"children"`
}

type ArchTreeResponse struct {
	Summary          ArchTreeSummaryVO       `json:"summary"`
	Subsystems       []ArchSubsystemOptionVO `json:"subsystems"`
	Nodes            []*ArchTreeNodeVO       `json:"nodes"`
	UnassignedScopes []RepoScopeCoverageVO   `json:"unassigned_scopes"` // @deprecated: 固定返回 []
}
```

- **内部编排上下文不变量 (`coverageBuildContext`)**：
  - `buildCoverageContext(ctx, user, repos)` 统一批量加载活跃仓库映射 `activeRepoByID`、分支覆盖策略、文档/修订/漂移/快照/活跃任务/模块索引及预构建 `rowsByScope map[scopeKey]RepoScopeCoverageVO`；
  - `resolveModuleScope(el, user)` 先校验 `el.RepoID` 与 `activeRepoByID` 判定 `bound` / `unbound` / `repo_inactive`；命中 `rowsByScope` 时**值拷贝独立 `RepoScopeCoverageVO` 并回填当前 `el` 元数据**（防多模块同 Scope 污染）；未命中时（如 `ignored` 或未刷新扫描绑定目录）实时调用 `buildSingleScopeRow` 兜底构造，整仓模块（`normSub == ""`）写权限严格遵循仓库级权限，子目录模块遵循 `repoLevelWrite || (el.OwnerID != nil && *el.OwnerID == user.ID)`，且严格执行 `!canWrite && d.Status != "approved"` 草稿过滤。

### 12.3 前端 TypeScript 强类型与纯函数契约 (`frontend/src/types/` & `frontend/src/utils/`)

```typescript
// frontend/src/types/domain.ts (ADR-09 增量)
export type ArchBindingFilter = '' | 'all' | 'bound' | 'unbound' | 'unbound_repo';
export type ArchModuleBindingStatus = 'bound' | 'unbound' | 'repo_inactive';

export interface ArchSubsystemOptionVO {
  id: number;
  identifier: string;
  name_cn: string;
  name_en: string;
  module_count?: number;
  scope_count: number;
}

export interface ArchTreeSummaryVO {
  total_modules: number;
  bound_modules: number;
  covered_modules: number;
  approved_docs: number;
  draft_docs: number;
  drifted_docs: number;
  unassigned_repo_count: number;
}

export interface ArchTreeStatsVO {
  module_count: number;
  bound_module_count: number;
  covered_module_count: number;
  approved_doc_count: number;
  draft_doc_count: number;
  drifted_doc_count: number;
  blocked_doc_count: number;
}

export interface ArchTreeNodeVO {
  id: number;
  identifier: string;
  name_cn: string;
  name_en: string;
  type: 'subsystem' | 'group' | 'module' | string;
  parent_id: number | null;
  owner_id: number | null;
  owner_name: string;
  repo_id: number | null;
  subdirectory: string;
  description: string;
  binding_status?: ArchModuleBindingStatus;
  scope?: RepoScopeCoverageVO | null;
  stats: ArchTreeStatsVO;
  /** @deprecated 由 scope 替代，固定返回 [] */
  scopes: RepoScopeCoverageVO[];
  children: ArchTreeNodeVO[];
}

export interface ArchTreeResponse {
  summary: ArchTreeSummaryVO;
  subsystems: ArchSubsystemOptionVO[];
  nodes: ArchTreeNodeVO[];
  /** @deprecated 由 /repos?arch_binding=unbound_repo 承接，固定返回 [] */
  unassigned_scopes: RepoScopeCoverageVO[];
}
```

```typescript
// frontend/src/utils/urlQuery.ts (ADR-09 增量)
export interface StudioNavState {
  from?: string;
  fromLabel?: string;
}

export interface SafeStudioBackTarget {
  path: string;
  label: string;
}

export interface RepoFilterFlags {
  advancedFilterCount: number;
  hasActiveFilters: boolean;
}

// 仅允许跳回模块内预设安全列表路径；缺省或非法来源安全回落至 /arch-tree（嵌入态 /archdoc/arch-tree）
export function resolveSafeStudioBackTarget(
  state: unknown,
  isEmbedded: boolean,
): SafeStudioBackTarget;

// 将视图模式（'' / 'all' / 'unbound_repo'）与用户主动筛选解耦；仅保留显式 subsystemId / scopeType / bound / unbound 计数
export function computeRepoFilterFlags(filter: RepoFilterState): RepoFilterFlags;
// 同步修订 serializeRepoFilterParams：archBinding === 'all' 时显式保留 arch_binding=all；'' 或 'unbound_repo' 时省略

// frontend/src/utils/archTreeUtils.ts（纯 TypeScript，零 React/JSX 依赖，由 OverviewMicroPills.tsx 重导出 buildArchTreeMicroPills）
export interface FlattenedArchTreeRow {
  node: ArchTreeNodeVO;
  depth: number;
  hasChildren: boolean;
  isExpanded: boolean;
  rowKey: string; // module:${node.id} 或 node:${node.id}
}

export function flattenArchTreeRows(
  nodes: readonly ArchTreeNodeVO[],
  expandedNodeIds: ReadonlySet<number>,
): FlattenedArchTreeRow[];

export function collectArchTreeExpandableIds(
  nodes: readonly ArchTreeNodeVO[],
  depthLimit?: number,
): number[];

export function computeDefaultExpandedArchTreeIds(
  nodes: readonly ArchTreeNodeVO[],
  totalModules: number,
  hasActiveFilters: boolean,
  threshold?: number, // 默认 50：<= 50 全展开，> 50 仅展第 1 层，筛选态全展开
): number[];

export function buildArchTreeMicroPills(
  summary: ArchTreeSummaryVO,
  activeStatus: string,
  onToggleStatus: (nextStatus: 'approved' | 'drifted' | '') => void,
): MicroStatPillItem[];
```

---

## 13. ADR-10 契约增量（`M16`：22 表索引、独立 `RefCitationsJSON`、`RefDocRepo` 与前端强类型契约）

### 13.1 22 表增量索引、增量列与核心不变量

| # / 表名 | 唯一键 / 变更列 | 索引 / 列定义 | 删除与一致策略 |
| :-: | :--- | :--- | :--- |
| **21** `archdoc_reference_docs` (`models.ReferenceDoc`) | `PK (id)` | `(doc_kind)`, `(owner_scope_key)`, `(owner_scope_level)`, `(owner_arch_element_id)`, `(owner_repo_id)`, `(content_sha256)`, `(status)`, `(created_by)` | 常规业务软归档（`status='archived'`）；仅管理员 `/admin/ref-docs/:id/purge` 物理删除并删磁盘目录 |
| **22** `archdoc_reference_bindings` (`models.ReferenceDocBinding`) | `(doc_id, scope_key)` | `(doc_id)`, `(scope_key)`, `(scope_level)`, `(arch_element_id)`, `(repo_id)` | `active` 文档下 `is_home=true` 禁止单独 `DELETE`；消费绑定可局部自由解绑 |
| `archdoc_gen_tasks` | `ReferenceDocSnapshotJSON` *(新增)* | `gorm:"type:jsonb;not null;default:'[]'" json:"reference_doc_snapshots"` | 任务创单时固化，单槽位重试严格锁死读取原快照版本 |
| `archdoc_section_evidences` | `RefCitationsJSON` *(新增)* | `gorm:"type:jsonb;not null;default:'[]'" json:"ref_citations"` | 与源码 `EvidencesJSON` 物理隔离，零污染发布/预览/漂移 |
| `archdoc_chat_proposal_items` | `RefCitationsJSON` *(新增)* | `gorm:"type:jsonb;not null;default:'[]'" json:"ref_citations"` | 采纳提案时随正文写入新修订的 `SectionEvidence.RefCitationsJSON` |

**新增硬不变量**：
1. **归属单点写权与防孤儿不变量**：`ReferenceDoc.OwnerScopeKey` 对应 `is_home=true` 的主绑定；编辑/归档权唯一校验 `OwnerScopeKey`（与消费绑定数量无关，彻底杜绝零绑定恒真越权与消费方反锁原作者）。
2. **先原子写盘、后事务提交不变量**：`POST` / `PUT`（正文变更）先持 `doc_id` 锁写入 `<RefDocsDir>/<doc_id>/v<N>.md`（临时文件 + `fsync` + `rename`），后执行 DB 条件更新（`WHERE id = ? AND current_version = ?`）；若 DB 失败立即删除刚写入的孤儿文件。
3. **单槽位重试版本锁死不变量**：`RetrySlotTask` 仅读取 `GenTask.ReferenceDocSnapshotJSON` 中的 `doc_id@v<version_no>`，绝不混入重试期间新发布的版本。
4. **源码证据零污染不变量**：`SectionEvidence.EvidencesJSON` 严禁写入 `ref_doc` 锚点；`RefCitationsJSON` 仅作溯源展示，不参与 `source_preview`、`pkg/publisher` 源码深链与 `pkg/drift` 代码漂移计算。
5. **历史解绑引用无损兼容不变量**：上一修订 `RefCitationsJSON` 中的已解绑/归档文档在手工直编或 Chat 重跑 Verifier 时标为 `stale: true` 且至多 `warning`，永不阻断 `can_approve`。
6. **Monorepo 整仓 Roll-up 上限不变量**：整仓汇聚模式下排除叶 `module` 局部文档且生效总数硬上限 `12` 篇。

### 13.2 JSONB Go Struct 与领域枚举 (`models/`)

```go
// models/reference_doc.go & models/json_types.go — ADR-10 增量契约

// TaskReferenceDocSnapshot 固化于 GenTask.ReferenceDocSnapshotJSON 与 ChatContextRefVO.RefDocs
type TaskReferenceDocSnapshot struct {
	DocID               uint     `json:"doc_id"`
	Title               string   `json:"title"`
	DocKind             string   `json:"doc_kind"`
	VersionNo           int      `json:"version_no"`
	ContentSHA256       string   `json:"content_sha256"`
	SummaryMD           string   `json:"summary_md"`
	SummarySHA256       string   `json:"summary_sha256"`
	TOC                 []HeadingNode `json:"toc"`
	DeclaredTerms       []string `json:"declared_terms,omitempty"`
	ScopeLevel          string   `json:"scope_level"` // 生效绑定层级: system | subsystem | group | repo | module
	ScopeKey            string   `json:"scope_key"`
	SourceScopeLabel    string   `json:"source_scope_label"`
	EstimatedTokens     int      `json:"estimated_tokens"`
	IsInherited         bool     `json:"is_inherited"`
}

// RefDocCitation 独立存储于 SectionEvidence.RefCitationsJSON 与 ChatProposalItem.RefCitationsJSON
type RefDocCitation struct {
	CitationKey  string `json:"citation_key"` // "ref_doc:<id>@v<ver>#<sec>" 或 "ref_doc:<id>@v<ver>"
	DocID        uint   `json:"doc_id"`
	VersionNo    int    `json:"version_no"`
	SectionID    string `json:"section_id,omitempty"`
	SectionTitle string `json:"section_title,omitempty"`
	DocTitle     string `json:"doc_title"`
	ScopeLevel   string `json:"scope_level"`
	Stale        bool   `json:"stale"`
}

// HeadingNode 对应 ReferenceDoc.TOCJSON（同 pkg/refdoc.HeadingNode 契约）
type HeadingNode struct {
	SectionID   string `json:"section_id"`
	Title       string `json:"title"`
	Level       int    `json:"level"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	RuneCount   int    `json:"rune_count"`
	IsChunk     bool   `json:"is_chunk,omitempty"`
}

// ChatContextRefVO 扩展可选 RefDocs（保持 []ChatContextRefVO 数组序列化向后兼容）
// RefDocs []TaskReferenceDocSnapshot `json:"ref_docs,omitempty"`
// UnverifiedRef.RefKind 新增："ref_doc" | "external_term"
```

```go
// models/enums.go — ADR-10 枚举常量与规范序
const (
	RefDocScopeSystem    = "system"
	RefDocScopeSubsystem = "subsystem"
	RefDocScopeGroup     = "group"
	RefDocScopeRepo      = "repo"
	RefDocScopeModule    = "module"

	RefDocKindArchDesign     = "arch_design"
	RefDocKindDomainGlossary = "domain_glossary"
	RefDocKindInterfaceSpec  = "interface_spec"
	RefDocKindADRConstraint  = "adr_constraint"
	RefDocKindOther          = "other"

	RefDocStatusActive   = "active"
	RefDocStatusArchived = "archived"

	RefDocSummarySourceManual   = "manual"
	RefDocSummarySourceLLM      = "llm"
	RefDocSummarySourceFallback = "fallback"

	RefDocSummaryStatusReady   = "ready"
	RefDocSummaryStatusPending = "pending"
	RefDocSummaryStatusFailed  = "failed"

	RefDocVisibilityPublic = "public"
)

func RefDocScopeRank(level string) int // system=1, subsystem=2, group=3, repo=4, module=5, 其他=0
func IsValidRefDocScopeLevel(level string) bool
func IsValidRefDocKind(kind string) bool
```

### 13.3 仓储接口与 `pkg/refdoc/` 纯领域契约

1. **`services/repos.go` 新增与扩展**：
   ```go
   // SharedMasterRepo 新增方法（支撑祖先链 RBAC 与范围标签回显，零 N+1）
   GetArchElement(ctx context.Context, id uint) (*models.ArchitectureElement, error)
   GetRepositoriesByIDs(ctx context.Context, ids []uint) (map[uint]commonModels.Repository, error)

   // ReferenceDocRepo 管理 archdoc_reference_docs 与 archdoc_reference_bindings
   type ReferenceDocRepo interface {
   	CreateWithHomeBinding(ctx context.Context, doc *models.ReferenceDoc, homeBinding *models.ReferenceDocBinding) error
   	GetByID(ctx context.Context, docID uint) (*models.ReferenceDoc, error)
   	ListDocs(ctx context.Context, filter models.RefDocListFilter) ([]models.ReferenceDoc, int64, error)
   	UpdateDocWithVersion(ctx context.Context, docID uint, expectedVersion int, updates map[string]any) (*models.ReferenceDoc, error)
   	UpdateLLMSummaryIfCurrent(ctx context.Context, docID uint, versionNo int, summaryMD string) error
   	ListBindingsByDocID(ctx context.Context, docID uint) ([]models.ReferenceDocBinding, error)
   	ListBindingsByDocIDs(ctx context.Context, docIDs []uint) (map[uint][]models.ReferenceDocBinding, error)
   	ListAllActiveDocsAndBindings(ctx context.Context) ([]models.ReferenceDoc, []models.ReferenceDocBinding, error)
   	CreateBinding(ctx context.Context, binding *models.ReferenceDocBinding) error
   	GetBindingByID(ctx context.Context, docID, bindingID uint) (*models.ReferenceDocBinding, error)
   	UpdateBinding(ctx context.Context, docID, bindingID uint, updates map[string]any) (*models.ReferenceDocBinding, error)
   	DeleteBinding(ctx context.Context, docID, bindingID uint) error
   	PurgeDoc(ctx context.Context, docID uint) error
   }
   ```
2. **`pkg/refdoc/` 核心导出签名**：
   - `EstimateTextTokens(s string) int`（全链路唯一 Token 估算实现）；
   - `ValidateAndParse(markdown string, maxBytes int64, redactAndSave bool, docKind string, explicitTerms []string) (*ParsedDoc, *GateFailure, error)`；
   - `BuildFallbackSummary(markdown string, toc []HeadingNode, maxRunes int) string`；
   - `ReadSection(markdown string, toc []HeadingNode, sectionID string, offsetLine, maxLines, maxBytes int) (*SectionExcerpt, error)`；
   - `SearchSections(docs []SearchableDoc, query string, filterDocID uint, maxMatches int) []SearchHit`；
   - `SelectBudgetedExcerptsForSlot(docs []PromptDoc, slotTitle, slotHint string, maxTokens int) []SlotExcerpt`（非工具槽位 `<= 1500 Tokens` 摘录）。

### 13.4 `config/config.go` `StorageSection` 增量

- `RefDocMaxBytes int64`（`yaml:"ref_doc_max_bytes"`，默认 `524288` / `512 KB`）；
- `RefDocsDir() string` → `filepath.Join(s.effectiveDataDir(), "ref_docs")`；
- `EffectiveRefDocMaxBytes() int64` → `<= 0` 时回退 `524288`。

### 13.5 前端 TypeScript 强类型契约增量 (`frontend/src/types/` & `utils/refDocUtils.ts`)

```typescript
// frontend/src/types/domain.ts 增量
export type RefDocScopeLevel = 'system' | 'subsystem' | 'group' | 'repo' | 'module';
export type RefDocKind = 'arch_design' | 'domain_glossary' | 'interface_spec' | 'adr_constraint' | 'other';
export type RefDocStatus = 'active' | 'archived';

export interface RefDocHeadingNode {
  section_id: string;
  title: string;
  level: number;
  start_line: number;
  end_line: number;
  rune_count: number;
  is_chunk?: boolean;
}

export interface RefDocBindingVO {
  id: number;
  doc_id: number;
  scope_key: string;
  scope_level: RefDocScopeLevel;
  scope_label: string;
  is_home: boolean;
  arch_element_id?: number | null;
  repo_id?: number | null;
  subdirectory: string;
  inherit_to_children: boolean;
  doc_type_slugs: string[];
  sort_order: number;
  can_manage_binding?: boolean;
}

export interface EffectiveRefDocVO {
  doc_id: number;
  binding_id: number;
  title: string;
  doc_kind: RefDocKind;
  current_version: number;
  summary_md: string;
  summary_stale: boolean;
  toc: RefDocHeadingNode[];
  estimated_tokens: number;
  is_home: boolean;
  is_inherited: boolean;
  effective_scope_level: RefDocScopeLevel;
  source_scope_label: string;
  inherit_to_children: boolean;
  doc_type_slugs: string[];
  can_edit: boolean;
  can_unbind: boolean;
}

export interface RefDocCitationVO {
  citation_key: string;
  doc_id: number;
  version_no: number;
  section_id?: string;
  section_title?: string;
  doc_title: string;
  scope_level: RefDocScopeLevel;
  stale: boolean;
}

export interface TaskReferenceDocSnapshotVO {
  doc_id: number;
  title: string;
  doc_kind: RefDocKind;
  version_no: number;
  summary_md: string;
  scope_level: RefDocScopeLevel;
  source_scope_label: string;
  estimated_tokens: number;
  is_inherited: boolean;
}

// ArchTreeSummaryVO 新增可选：system_ref_doc_count?: number;
// ArchTreeNodeVO 新增可选：direct_ref_doc_count?: number; inherited_ref_doc_count?: number;
// GenTaskDetailVO 新增可选：reference_docs?: TaskReferenceDocSnapshotVO[];
// SectionVO & ChatProposalItemVO 新增可选：ref_citations?: RefDocCitationVO[];
// ChatContextRefVO 新增可选：ref_docs?: TaskReferenceDocSnapshotVO[];

// frontend/src/types/api.ts 增量
// ArchdocErrorCode 新增：'HOME_BINDING_PROTECTED' | 'VERSION_CONFLICT' | 'ALREADY_BOUND' | 'PAYLOAD_TOO_LARGE' | 'INLINE_BASE64_IMAGE' | 'SECRET_DETECTED' | 'SECRET_CONFIRM_REQUIRED'
// GenerateTaskPayload 新增可选：arch_element_id?: number; exclude_ref_doc_ids?: number[];
// BatchGenerateTaskPayload / IncrementalRefreshPayload 新增可选：exclude_ref_doc_ids?: number[];

// frontend/src/utils/refDocUtils.ts（纯 TypeScript，零 React/JSX 依赖，node --test 直接运行）
export function groupEffectiveRefDocsByScope(docs: readonly EffectiveRefDocVO[]): { level: RefDocScopeLevel; label: string; items: EffectiveRefDocVO[] }[];
export function buildExcludedRefDocIds(docs: readonly EffectiveRefDocVO[], checkedDocIds: ReadonlySet<number>): number[];
export function formatRefDocSummaryStaleHint(summaryForVersion: number, currentVersion: number): string;
export function formatRefCitationBadgeLabel(citation: RefDocCitationVO): string;
```

---

## 14. ADR-11 契约增量（`M17`：零新增表声明、`pkg/refdoc` 精修契约、Service DTO 与前端强类型契约）

### 14.1 零数据库结构变更声明与 `LLMUsageLog` 复用契约

`M17`（`ADR-11` 一期轻量方案）严格保持 `ADR-10` 冻结的 **22 张 `archdoc_*` 自有表与既有列结构 100% 不变，零新增表、零表字段变更、共享表零 DDL**：
- **无状态精修**：AI 精修会话与提案在前端内存中流转，采纳后复用 `PUT /api/ref-docs/:id` 原子升版；
- **LLM 用量记账复用**：`POST /api/ref-docs/:id/refine` 复用既有 `archdoc_llm_usage_logs`（`models.LLMUsageLog`）：
  - `TaskID = nil`、`DocumentID = nil`；
  - `RepoID = *doc.OwnerRepoID`（若归属范围为 `system`/`subsystem`/`group`/未绑仓模块则写 `0`）；
  - `SlotID = fmt.Sprintf("ref_doc:%d", doc.ID)`；
  - `Engine = models.EngineNative`（`"native"`）；
  - `Stage = "ref_doc_refine"`（`revise`）或 `"ref_doc_ask"`（`ask`），长度严格 `<= 32` 字符（匹配 `varchar(32)`）。

### 14.2 `pkg/refdoc/` 纯领域包增量契约（严格叶包零 `pkg/llm` / `pkg/drift` / DB 依赖）

1. **`pkg/refdoc/parser.go` 导出**：
   ```go
   // ParseTOC 归一化换行符并执行带围栏状态机、Setext/编号标题与 >250 行分块兜底的目录切片。
   func ParseTOC(markdown string) []HeadingNode
   ```
2. **`pkg/refdoc/refine.go`（新建，仅允许导入 Go 标准库与 `pkg/security`）**：
   ```go
   package refdoc

   const (
   	RefineScopeSelection = "selection"
   	RefineScopeSection   = "section"
   	RefineScopeFull      = "full"

   	RefineModeRevise = "revise"
   	RefineModeAsk    = "ask"

   	RefinePresetPolish         = "polish"
   	RefinePresetMermaid        = "mermaid"
   	RefinePresetRefreshSummary = "refresh_summary"

   	DefaultFullRefineMaxTokens     = 16000
   	DefaultSummaryExtractMaxTokens = 24000
   )

   // RefinePromptMessage 零依赖纯消息结构（与 pkg/llm.Message 字段同构，由 services 层映射转换）。
   type RefinePromptMessage struct {
   	Role    string `json:"role"`    // system | user
   	Content string `json:"content"`
   }

   type RefineSelectionRange struct {
   	StartLine int    `json:"start_line"`
   	EndLine   int    `json:"end_line"`
   	QuoteText string `json:"quote_text,omitempty"`
   }

   type RefinePromptInput struct {
   	Title           string
   	DocKind         string
   	TOC             []HeadingNode
   	FullMarkdown    string
   	ScopeType       string                // selection | section | full
   	Mode            string                // revise | ask
   	PresetAction    string                // "" | polish | mermaid | refresh_summary
   	UserPrompt      string
   	TargetSection   *HeadingNode
   	TargetFragment  string
   	Selection       *RefineSelectionRange
   	CurrentTerms    []string
   	MaxContextBytes int64
   }

   type RefineLLMOutput struct {
   	AnswerMD               string   `json:"answer_md"`
   	RevisedMarkdown        string   `json:"revised_markdown"`
   	SuggestedSummaryMD     string   `json:"suggested_summary_md,omitempty"`
   	SuggestedDeclaredTerms []string `json:"suggested_declared_terms,omitempty"`
   }

   // CheckFullRefineBudget 评估 full 模式重写正文是否超出单轮安全上下文上限。
   func CheckFullRefineBudget(markdown string, maxTokens int) (estimatedTokens int, effectiveLimit int, exceeded bool)

   // BuildRefinePrompt 构造带 128-bit 随机 nonce 数据栅栏 (<untrusted_ref_doc nonce="...">) 的提示词消息。
   // 当 preset_action=="refresh_summary" 且全文超过 DefaultSummaryExtractMaxTokens 时自动按 TOC + 各节首段浓缩上下文。
   func BuildRefinePrompt(in RefinePromptInput) (messages []RefinePromptMessage, nonce string, err error)

   // ParseRefineLLMOutput 兼容纯 JSON、```json 围栏及纯文本兜底解析 LLM 精修响应。
   func ParseRefineLLMOutput(rawContent string, mode string, presetAction string) (*RefineLLMOutput, error)

   // ApplyLineRangeReplacement 先归一化 \r\n -> \n，再对 1-based 闭区间 [startLine, endLine] 执行精准行替换，非目标行保持字节级不变。
   func ApplyLineRangeReplacement(fullMarkdown string, startLine, endLine int, replacement string) (string, error)
   ```

### 14.3 `services/` 领域 DTO 与装配契约 (`services/ref_doc_service.go` & `services/ref_doc_refine_service.go`)

```go
// services/ref_doc_service.go 扩展可选注入方法（仅 RefineRefDoc 需要 LLM 与 UsageLog，Versions/Diff 零 LLM 依赖）
func (s *RefDocService) WithRefineEngine(govRepo GovernanceRepo, llmClient *llm.Client) *RefDocService

// services/ref_doc_refine_service.go — ADR-11 增量请求与视图 DTO
type RefDocRefineSelection struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	QuoteText string `json:"quote_text,omitempty"`
}

type RefineRefDocRequest struct {
	ScopeType      string                 `json:"scope_type"`                 // selection | section | full
	Mode           string                 `json:"mode"`                       // revise | ask
	PresetAction   string                 `json:"preset_action,omitempty"`    // polish | mermaid | refresh_summary
	Prompt         string                 `json:"prompt"`
	SectionID      string                 `json:"section_id,omitempty"`
	Selection      *RefDocRefineSelection `json:"selection,omitempty"`
	DraftContentMD string                 `json:"draft_content_md,omitempty"`
}

type RefDocRefineGateError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RefDocRefinePreflight struct {
	CanSave               bool                   `json:"can_save"`
	SecretConfirmRequired bool                   `json:"secret_confirm_required"`
	SecretIssues          []refdoc.GateFinding   `json:"secret_issues"`
	MermaidIssues         []verifier.MermaidIssue `json:"mermaid_issues"`
	GateError             *RefDocRefineGateError `json:"gate_error,omitempty"`
}

type RefineRefDocResponse struct {
	AnswerMD               string                `json:"answer_md"`
	Mode                   string                `json:"mode"`
	ScopeType              string                `json:"scope_type"`
	TargetSectionID        string                `json:"target_section_id,omitempty"`
	OriginalFragment       string                `json:"original_fragment"`
	ProposedFragment       string                `json:"proposed_fragment"`
	ProposedFullMarkdown   string                `json:"proposed_full_markdown"`
	UnifiedDiff            string                `json:"unified_diff"`
	Preflight              RefDocRefinePreflight `json:"preflight"`
	SuggestedSummaryMD     string                `json:"suggested_summary_md,omitempty"`
	SuggestedDeclaredTerms []string              `json:"suggested_declared_terms,omitempty"`
}

type RefDocVersionItemVO struct {
	VersionNo  int   `json:"version_no"`
	ByteSize   int64 `json:"byte_size"`
	IsCurrent  bool  `json:"is_current"`
	FileExists bool  `json:"file_exists"`
}

type RefDocVersionsResponse struct {
	DocID          uint                  `json:"doc_id"`
	CurrentVersion int                   `json:"current_version"`
	Versions       []RefDocVersionItemVO `json:"versions"`
}

type RefDocDiffResponse struct {
	DocID        uint   `json:"doc_id"`
	FromVersion  int    `json:"from_version"`
	ToVersion    int    `json:"to_version"`
	UnifiedDiff  string `json:"unified_diff"`
	AddedLines   int    `json:"added_lines"`
	RemovedLines int    `json:"removed_lines"`
}

// RefDocTokenBudgetExceededError 对应 HTTP 422 TOKEN_BUDGET_EXCEEDED。
type RefDocTokenBudgetExceededError struct {
	EstimatedTokens     int
	MaxFullRefineTokens int
}
```

### 14.4 前端 TypeScript 强类型与纯函数契约 (`frontend/src/types/` & `utils/refDocStudioUtils.ts`)

```typescript
// frontend/src/types/domain.ts (ADR-11 增量)
export type RefDocRefineScopeType = 'selection' | 'section' | 'full';
export type RefDocRefineMode = 'revise' | 'ask';
export type RefDocRefinePresetAction = 'polish' | 'mermaid' | 'refresh_summary';

export interface RefDocRefineGateErrorVO {
  code: string;
  message: string;
}

export interface RefDocGateFindingVO {
  rule_id: string;
  matched_mask: string;
  line_offset: number;
  severity: 'hard' | 'confirm_required' | string;
}

export interface RefDocMermaidIssueVO {
  slot_id: string;
  block_index: number;
  diagram_type: string;
  issue_code: string;
  severity: 'error' | 'warning' | string;
  node_count: number;
  edge_count: number;
  message: string;
}

export interface RefDocRefinePreflightVO {
  can_save: boolean;
  secret_confirm_required: boolean;
  secret_issues: RefDocGateFindingVO[];
  mermaid_issues: RefDocMermaidIssueVO[];
  gate_error?: RefDocRefineGateErrorVO;
}

export interface RefineRefDocResponse {
  answer_md: string;
  mode: RefDocRefineMode;
  scope_type: RefDocRefineScopeType;
  target_section_id?: string;
  original_fragment: string;
  proposed_fragment: string;
  proposed_full_markdown: string;
  unified_diff: string;
  preflight: RefDocRefinePreflightVO;
  suggested_summary_md?: string;
  suggested_declared_terms?: string[];
}

export interface RefDocVersionItemVO {
  version_no: number;
  byte_size: number;
  is_current: boolean;
  file_exists: boolean;
}

export interface RefDocVersionsResponse {
  doc_id: number;
  current_version: number;
  versions: RefDocVersionItemVO[];
}

export interface RefDocDiffResponse {
  doc_id: number;
  from_version: number;
  to_version: number;
  unified_diff: string;
  added_lines: number;
  removed_lines: number;
}

// frontend/src/types/api.ts (ADR-11 增量)
export interface RefineRefDocSelectionPayload {
  start_line: number;
  end_line: number;
  quote_text?: string;
}

export interface RefineRefDocPayload {
  scope_type: RefDocRefineScopeType;
  mode: RefDocRefineMode;
  preset_action?: RefDocRefinePresetAction;
  prompt: string;
  section_id?: string;
  selection?: RefineRefDocSelectionPayload;
  draft_content_md?: string;
}

// frontend/src/utils/urlQuery.ts (ADR-11 增量)
export function buildRefDocStudioPath(isEmbedded: boolean, docId: number): string;

// frontend/src/utils/errorMessages.ts (ADR-11 增量)
// FormattedApiError 新增可选字段：currentVersion?: number（解析 details.current_version）

// frontend/src/utils/refDocStudioUtils.ts（纯 TypeScript，零 React/JSX 依赖，node --test 直接运行）
export interface RefDocDisplaySection {
  section_id: string;
  title: string;
  level: number;
  start_line: number;
  end_line: number;
  markdown: string;
  is_preamble?: boolean;
  is_chunk?: boolean;
}

export function parseLocalRefDocTOC(markdown: string): RefDocHeadingNode[];
export function splitRefDocIntoDisplaySections(markdown: string, toc: readonly RefDocHeadingNode[]): RefDocDisplaySection[];
export function replaceLinesInDraft(fullMarkdown: string, startLine: number, endLine: number, replacement: string): string;
export function computeAbsoluteSelectionLines(
  sectionStartLine: number,
  relStartLine: number | undefined,
  relEndLine: number | undefined,
  sectionEndLine: number,
): { startLine: number; endLine: number };
export function hasUnsavedRefDocDraft(savedContent: string, draftContent: string): boolean;
export function mergeRefDocRefineProposal(currentDraft: string, proposal: RefineRefDocResponse): string;
export function classifyRefDocDiffLine(line: string): string;
```
