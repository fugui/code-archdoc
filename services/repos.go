package services

import (
	"context"
	"time"

	"code-archdoc/models"
	commonModels "code-common/backend/models"
)

// SharedMasterRepo 只读访问平台共享公共表（users / departments / repositories / architecture_elements）
type SharedMasterRepo interface {
	ListDepartments(ctx context.Context) ([]commonModels.Department, error)
	GetRepository(ctx context.Context, repoID uint) (*commonModels.Repository, error)
	ListActiveRepositories(ctx context.Context, filter models.RepoQueryFilter) ([]commonModels.Repository, int64, error)
	ListAllActiveRepositories(ctx context.Context, departmentID *uint) ([]commonModels.Repository, error)
	ListArchElementsByRepo(ctx context.Context, repoID uint) ([]models.ArchitectureElement, error)
	ListArchElementsByRepos(ctx context.Context, repoIDs []uint) ([]models.ArchitectureElement, error)
	FindArchElementsByRepoAndOwner(ctx context.Context, repoID, ownerID uint) ([]models.ArchitectureElement, error)
	GetUsersByIDs(ctx context.Context, ids []uint) (map[uint]commonModels.User, error)
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
	GetByID(ctx context.Context, snapshotID uint) (*models.RepoSnapshot, error)
	FindByKey(ctx context.Context, repoID uint, subdir, refName, commitSHA, irSchemaVer string) (*models.RepoSnapshot, error)
	ListLatestByRepos(ctx context.Context, repoIDs []uint) ([]models.RepoSnapshot, error)
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
	ListTasks(ctx context.Context, filter models.TaskListFilter) ([]models.GenTask, int64, error)
	ListActiveTasksByRepos(ctx context.Context, repoIDs []uint) ([]models.GenTask, error)
	TransitionTaskStatus(ctx context.Context, taskID uint, from, to string, updates map[string]any) error
	UpdateTaskFields(ctx context.Context, taskID uint, updates map[string]any) error
	CreateSlotTasks(ctx context.Context, slots []models.GenSlotTask) error
	UpdateSlotTask(ctx context.Context, slotTask *models.GenSlotTask) error
	GetSlotTask(ctx context.Context, taskID, slotTaskID uint) (*models.GenSlotTask, error)
}

// DocumentRepo 管理 archdoc_documents / archdoc_document_revisions / archdoc_section_evidences
type DocumentRepo interface {
	GetByID(ctx context.Context, docID uint) (*models.Document, error)
	GetOrCreateDocument(ctx context.Context, doc *models.Document) (*models.Document, error)
	GetByScope(ctx context.Context, repoID uint, subdir, refName, docTypeSlug string) (*models.Document, error)
	ListByScope(ctx context.Context, repoID uint, subdir, refName string, approvedOnly bool) ([]models.Document, error)
	ListByRepos(ctx context.Context, repoIDs []uint) ([]models.Document, error)
	ListRevisionsByIDs(ctx context.Context, revIDs []uint) (map[uint]models.DocumentRevision, error)
	ListApprovedDocuments(ctx context.Context) ([]models.Document, error)
	ListByIDs(ctx context.Context, docIDs []uint, approvedOnly bool) ([]models.Document, error)
	GetRevisionWithSections(ctx context.Context, docID uint, revisionNo int) (*models.DocumentRevision, []models.SectionEvidence, error)
	GetRevisionByIDWithSections(ctx context.Context, docID, revID uint) (*models.DocumentRevision, []models.SectionEvidence, error)
	GetFirstAIGenerateRevision(ctx context.Context, docID uint) (*models.DocumentRevision, error)
	ListRevisions(ctx context.Context, docID uint) ([]models.DocumentRevision, error)
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
	UpdateMessage(ctx context.Context, msg *models.ChatMessage) error
	DeleteMessage(ctx context.Context, msgID uint) error
	TouchSession(ctx context.Context, sessionID uint, title string) error
	GetMessage(ctx context.Context, docID, msgID uint) (*models.ChatMessage, error)
	UpdateProposalStatus(ctx context.Context, msgID uint, status string, appliedRevID *uint) error
	SupersedePendingProposals(ctx context.Context, docID uint, slotID string, exceptMsgID uint) error
}

// DriftRepo 管理 archdoc_repo_remote_heads 与 archdoc_document_drifts
type DriftRepo interface {
	ListRemoteHeadsByRepos(ctx context.Context, repoIDs []uint) ([]models.RepoRemoteHead, error)
	ListDriftsByDocIDs(ctx context.Context, docIDs []uint) ([]models.DocumentDrift, error)
	UpsertRemoteHead(ctx context.Context, head *models.RepoRemoteHead) error
	UpsertDocumentDrift(ctx context.Context, drift *models.DocumentDrift) error
}

// GovernanceRepo 管理 archdoc_llm_configs / archdoc_llm_usage_logs / archdoc_quality_metrics
type GovernanceRepo interface {
	ListLLMConfigs(ctx context.Context) ([]models.LLMConfig, error)
	GetLLMConfigByTier(ctx context.Context, tier string) (*models.LLMConfig, error)
	SaveLLMConfig(ctx context.Context, cfg *models.LLMConfig) error
	UpdateLLMProbeResult(ctx context.Context, tier, status string, latencyMs int64, probedAt time.Time) error
	RecordLLMUsage(ctx context.Context, log *models.LLMUsageLog) error
	RecordQualityMetric(ctx context.Context, metric *models.QualityMetric) error
}
