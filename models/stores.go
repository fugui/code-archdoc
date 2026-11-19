package models

import (
	"context"
	"errors"
	"time"

	commonModels "code-common/backend/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─── 查询过滤与聚合记录结构（供 services/ 仓储接口复用，保持 models/ 零反向依赖） ───

type RepoQueryFilter struct {
	DepartmentID *uint
	Search       string
	Offset       int
	Limit        int
}

type TaskListFilter struct {
	RepoID       *uint
	Subdirectory *string
	Status       string
	TriggeredBy  *uint
	Offset       int
	Limit        int
}

type PollableRepoRecord struct {
	RepoID  uint
	RepoURL string
	Branch  string
}

type DriftQueryFilter struct {
	Search       string
	DepartmentID *uint
	DriftType    string
	DocTypeSlug  string
	Offset       int
	Limit        int
}

type DriftAggregateRecord struct {
	Document  Document
	CodeDrift *DocumentDrift
	SpecDrift *DocumentDrift
}

type DriftSummaryRecord struct {
	TotalDriftedDocs int64
	CodeDriftDocs    int64
	SpecDriftDocs    int64
	BothDriftDocs    int64
	MonitoredRepos   int64
	UnhealthyRepos   int64
	LastPollAt       *time.Time
}

type MetricsFilter struct {
	Days              int
	DocTypeSlug       string
	DocTypeRevisionID *uint
}

type MetricsOverviewRecord struct {
	WindowDays            int
	AvgEditDiffRatio      float64
	ChatAcceptRate        float64
	VerifierFirstPassRate float64
	VerifierBlockRate     float64
	AvgSlotCoverageRate   float64
	AvgTokensPerDoc       int64
	AvgCacheHitRatio      float64
	SampleDocCount        int64
}

type RevisionMetricPoint struct {
	DocTypeRevisionID   uint
	RevisionNo          int
	CreatedAt           time.Time
	AvgEditDiffRatio    float64
	ChatAcceptRate      float64
	VerifierBlockRate   float64
	AvgSlotCoverageRate float64
	AvgTokensPerDoc     int64
	SampleCount         int64
}

type SlotHeatmapPoint struct {
	SlotID              string
	Title               string
	AvgEditDiffRatio    float64
	VerifierWarningRate float64
	SampleCount         int64
}

type UsageSummaryRecord struct {
	ModelTier          string
	DocTypeSlug        string
	TotalTokens        int64
	CachedPromptTokens int64
	CallCount          int64
}

// ─── 1. SharedMasterStore ───

type SharedMasterStore struct {
	db *gorm.DB
}

func NewSharedMasterStore(db *gorm.DB) *SharedMasterStore {
	return &SharedMasterStore{db: db}
}

func (s *SharedMasterStore) ListDepartments(ctx context.Context) ([]commonModels.Department, error) {
	var depts []commonModels.Department
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&depts).Error; err != nil {
		return nil, err
	}
	return depts, nil
}

func (s *SharedMasterStore) GetRepository(ctx context.Context, repoID uint) (*commonModels.Repository, error) {
	var repo commonModels.Repository
	err := s.db.WithContext(ctx).
		Preload("Department").
		Preload("Owner").
		First(&repo, repoID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (s *SharedMasterStore) ListActiveRepositories(ctx context.Context, filter RepoQueryFilter) ([]commonModels.Repository, int64, error) {
	q := s.db.WithContext(ctx).Model(&commonModels.Repository{}).Where("is_active = ?", true)
	if filter.DepartmentID != nil && *filter.DepartmentID > 0 {
		q = q.Where("department_id = ?", *filter.DepartmentID)
	}
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("LOWER(name) LIKE LOWER(?)", like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var repos []commonModels.Repository
	query := q.Preload("Department").Preload("Owner").Order("id ASC")
	if filter.Limit > 0 {
		query = query.Offset(filter.Offset).Limit(filter.Limit)
	}
	if err := query.Find(&repos).Error; err != nil {
		return nil, 0, err
	}
	return repos, total, nil
}

func (s *SharedMasterStore) ListAllActiveRepositories(ctx context.Context, departmentID *uint) ([]commonModels.Repository, error) {
	q := s.db.WithContext(ctx).Model(&commonModels.Repository{}).Where("is_active = ?", true)
	if departmentID != nil && *departmentID > 0 {
		q = q.Where("department_id = ?", *departmentID)
	}
	var repos []commonModels.Repository
	if err := q.Preload("Department").Preload("Owner").Order("id ASC").Find(&repos).Error; err != nil {
		return nil, err
	}
	return repos, nil
}

func (s *SharedMasterStore) ListArchElementsByRepo(ctx context.Context, repoID uint) ([]ArchitectureElement, error) {
	var elems []ArchitectureElement
	if err := s.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Order("id ASC").
		Find(&elems).Error; err != nil {
		return nil, err
	}
	return elems, nil
}

func (s *SharedMasterStore) ListArchElementsByRepos(ctx context.Context, repoIDs []uint) ([]ArchitectureElement, error) {
	if len(repoIDs) == 0 {
		return []ArchitectureElement{}, nil
	}
	var elems []ArchitectureElement
	if err := s.db.WithContext(ctx).
		Where("repo_id IN ?", repoIDs).
		Order("repo_id ASC, id ASC").
		Find(&elems).Error; err != nil {
		return nil, err
	}
	return elems, nil
}

func (s *SharedMasterStore) FindArchElementsByRepoAndOwner(ctx context.Context, repoID, ownerID uint) ([]ArchitectureElement, error) {
	var elems []ArchitectureElement
	if err := s.db.WithContext(ctx).
		Where("repo_id = ? AND owner_id = ?", repoID, ownerID).
		Find(&elems).Error; err != nil {
		return nil, err
	}
	return elems, nil
}

func (s *SharedMasterStore) GetUsersByIDs(ctx context.Context, ids []uint) (map[uint]commonModels.User, error) {
	res := make(map[uint]commonModels.User, len(ids))
	if len(ids) == 0 {
		return res, nil
	}
	var users []commonModels.User
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, u := range users {
		res[u.ID] = u
	}
	return res, nil
}

// ─── 2. DocTypeStore ───

type DocTypeStore struct {
	db *gorm.DB
}

func NewDocTypeStore(db *gorm.DB) *DocTypeStore {
	return &DocTypeStore{db: db}
}

func (s *DocTypeStore) List(ctx context.Context, activeOnly bool) ([]DocType, error) {
	q := s.db.WithContext(ctx).Model(&DocType{})
	if activeOnly {
		q = q.Where("is_active = ?", true)
	}
	var list []DocType
	if err := q.Order("is_builtin DESC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *DocTypeStore) GetBySlug(ctx context.Context, slug string) (*DocType, *DocTypeRevision, error) {
	var dt DocType
	err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&dt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var rev *DocTypeRevision
	if dt.LatestRevisionID != nil {
		var r DocTypeRevision
		if rErr := s.db.WithContext(ctx).First(&r, *dt.LatestRevisionID).Error; rErr == nil {
			rev = &r
		} else if !errors.Is(rErr, gorm.ErrRecordNotFound) {
			return nil, nil, rErr
		}
	}
	return &dt, rev, nil
}

func (s *DocTypeStore) ListRevisions(ctx context.Context, slug string) ([]DocTypeRevision, error) {
	var revs []DocTypeRevision
	if err := s.db.WithContext(ctx).
		Where("doc_type_slug = ?", slug).
		Order("revision_no DESC").
		Find(&revs).Error; err != nil {
		return nil, err
	}
	return revs, nil
}

func (s *DocTypeStore) GetRevisionByID(ctx context.Context, revID uint) (*DocTypeRevision, error) {
	var rev DocTypeRevision
	err := s.db.WithContext(ctx).First(&rev, revID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

func (s *DocTypeStore) UpsertFromDirectorySync(ctx context.Context, dt *DocType, newRev *DocTypeRevision) (bool, error) {
	var createdRev bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DocType
		findErr := tx.Where("slug = ?", dt.Slug).First(&existing).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			if err := tx.Create(dt).Error; err != nil {
				return err
			}
			newRev.DocTypeID = dt.ID
			newRev.DocTypeSlug = dt.Slug
			newRev.RevisionNo = 1
			if err := tx.Create(newRev).Error; err != nil {
				return err
			}
			revID := newRev.ID
			dt.LatestRevisionID = &revID
			dt.LatestRevisionNo = 1
			dt.LatestAggregateHash = newRev.AggregateHash
			createdRev = true
			return tx.Model(dt).Updates(map[string]any{
				"latest_revision_id":    revID,
				"latest_revision_no":    1,
				"latest_aggregate_hash": newRev.AggregateHash,
			}).Error
		}
		if findErr != nil {
			return findErr
		}

		dt.ID = existing.ID
		updates := map[string]any{
			"display_name":    dt.DisplayName,
			"description":     dt.Description,
			"is_builtin":      dt.IsBuiltin,
			"is_active":       dt.IsActive,
			"model_tier":      dt.ModelTier,
			"timeout_seconds": dt.TimeoutSeconds,
			"ir_facets_json":  dt.IRFacetsJSON,
			"updated_by":      dt.UpdatedBy,
		}

		if existing.LatestAggregateHash != newRev.AggregateHash {
			nextRevNo := existing.LatestRevisionNo + 1
			newRev.DocTypeID = existing.ID
			newRev.DocTypeSlug = existing.Slug
			newRev.RevisionNo = nextRevNo
			if err := tx.Create(newRev).Error; err != nil {
				return err
			}
			revID := newRev.ID
			updates["latest_revision_id"] = revID
			updates["latest_revision_no"] = nextRevNo
			updates["latest_aggregate_hash"] = newRev.AggregateHash
			dt.LatestRevisionID = &revID
			dt.LatestRevisionNo = nextRevNo
			dt.LatestAggregateHash = newRev.AggregateHash
			createdRev = true
		} else {
			dt.LatestRevisionID = existing.LatestRevisionID
			dt.LatestRevisionNo = existing.LatestRevisionNo
			dt.LatestAggregateHash = existing.LatestAggregateHash
		}

		return tx.Model(&existing).Updates(updates).Error
	})
	return createdRev, err
}

func (s *DocTypeStore) CountApprovedDocumentsBySlug(ctx context.Context, slug string) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&Document{}).
		Where("doc_type_slug = ? AND status = ?", slug, DocStatusApproved).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (s *DocTypeStore) DeleteIfUnused(ctx context.Context, slug string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var dt DocType
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("slug = ?", slug).First(&dt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		var count int64
		if err := tx.Model(&Document{}).
			Where("doc_type_slug = ? AND status = ?", slug, DocStatusApproved).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrDocTypeInUse
		}

		return tx.Delete(&dt).Error
	})
}

// ─── 3. SnapshotStore ───

type SnapshotStore struct {
	db *gorm.DB
}

func NewSnapshotStore(db *gorm.DB) *SnapshotStore {
	return &SnapshotStore{db: db}
}

func (s *SnapshotStore) FindByKey(ctx context.Context, repoID uint, subdir, refName, commitSHA, irSchemaVer string) (*RepoSnapshot, error) {
	var snap RepoSnapshot
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND subdirectory = ? AND ref_name = ? AND commit_sha = ? AND ir_schema_version = ?",
			repoID, subdir, refName, commitSHA, irSchemaVer).
		First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (s *SnapshotStore) ListLatestByRepos(ctx context.Context, repoIDs []uint) ([]RepoSnapshot, error) {
	if len(repoIDs) == 0 {
		return []RepoSnapshot{}, nil
	}
	var snaps []RepoSnapshot
	if err := s.db.WithContext(ctx).
		Where("repo_id IN ? AND status = ?", repoIDs, "ready").
		Order("created_at DESC, id DESC").
		Find(&snaps).Error; err != nil {
		return nil, err
	}
	return snaps, nil
}

func (s *SnapshotStore) UpsertSnapshot(ctx context.Context, snap *RepoSnapshot) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "repo_id"},
			{Name: "subdirectory"},
			{Name: "ref_name"},
			{Name: "commit_sha"},
			{Name: "ir_schema_version"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"status", "effective_files", "skipped_files", "total_loc",
			"glossary_path", "glossary_model", "stats_json", "last_accessed_at", "updated_at",
		}),
	}).Create(snap).Error
}

func (s *SnapshotStore) UpsertFacet(ctx context.Context, facet *CodeIRFacet) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "snapshot_id"},
			{Name: "facet_name"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"storage_path", "content_sha256", "item_count", "size_bytes",
			"compressed_bytes", "summary_json", "last_accessed_at", "evicted_at", "updated_at",
		}),
	}).Create(facet).Error
}

func (s *SnapshotStore) ListFacetsBySnapshot(ctx context.Context, snapshotID uint) ([]CodeIRFacet, error) {
	var facets []CodeIRFacet
	if err := s.db.WithContext(ctx).
		Where("snapshot_id = ?", snapshotID).
		Order("facet_name ASC").
		Find(&facets).Error; err != nil {
		return nil, err
	}
	return facets, nil
}

func (s *SnapshotStore) TouchFacetAccess(ctx context.Context, facetIDs []uint, accessedAt time.Time) error {
	if len(facetIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&CodeIRFacet{}).
		Where("id IN ?", facetIDs).
		Update("last_accessed_at", accessedAt).Error
}

func (s *SnapshotStore) ListEvictableFacets(ctx context.Context, excludeSnapshotIDs []uint, limit int) ([]CodeIRFacet, error) {
	q := s.db.WithContext(ctx).Model(&CodeIRFacet{}).Where("evicted_at IS NULL")
	if len(excludeSnapshotIDs) > 0 {
		q = q.Where("snapshot_id NOT IN ?", excludeSnapshotIDs)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var facets []CodeIRFacet
	if err := q.Order("last_accessed_at ASC").Find(&facets).Error; err != nil {
		return nil, err
	}
	return facets, nil
}

func (s *SnapshotStore) MarkFacetEvicted(ctx context.Context, facetID uint, evictedAt time.Time) error {
	return s.db.WithContext(ctx).Model(&CodeIRFacet{}).
		Where("id = ?", facetID).
		Update("evicted_at", evictedAt).Error
}

// ─── 4. TaskStore ───

type TaskStore struct {
	db *gorm.DB
}

func NewTaskStore(db *gorm.DB) *TaskStore {
	return &TaskStore{db: db}
}

func (s *TaskStore) CreateTask(ctx context.Context, task *GenTask) error {
	return s.db.WithContext(ctx).Create(task).Error
}

func (s *TaskStore) GetTaskWithSlots(ctx context.Context, taskID uint) (*GenTask, []GenSlotTask, error) {
	var task GenTask
	err := s.db.WithContext(ctx).First(&task, taskID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var slots []GenSlotTask
	if err := s.db.WithContext(ctx).Where("task_id = ?", taskID).Order("id ASC").Find(&slots).Error; err != nil {
		return nil, nil, err
	}
	return &task, slots, nil
}

func (s *TaskStore) ListTasks(ctx context.Context, filter TaskListFilter) ([]GenTask, int64, error) {
	q := s.db.WithContext(ctx).Model(&GenTask{})
	if filter.RepoID != nil {
		q = q.Where("repo_id = ?", *filter.RepoID)
	}
	if filter.Subdirectory != nil {
		q = q.Where("subdirectory = ?", *filter.Subdirectory)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.TriggeredBy != nil {
		q = q.Where("triggered_by = ?", *filter.TriggeredBy)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var tasks []GenTask
	query := q.Order("id DESC")
	if filter.Limit > 0 {
		query = query.Offset(filter.Offset).Limit(filter.Limit)
	}
	if err := query.Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

func (s *TaskStore) ListActiveTasksByRepos(ctx context.Context, repoIDs []uint) ([]GenTask, error) {
	if len(repoIDs) == 0 {
		return []GenTask{}, nil
	}
	var tasks []GenTask
	activeStatuses := []string{TaskStatusPending, TaskStatusExtracting, TaskStatusGenerating, TaskStatusVerifying}
	if err := s.db.WithContext(ctx).
		Where("repo_id IN ? AND status IN ?", repoIDs, activeStatuses).
		Order("id DESC").
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *TaskStore) TransitionTaskStatus(ctx context.Context, taskID uint, from, to string, updates map[string]any) error {
	if !CanTransitionTaskStatus(from, to) {
		return ErrInvalidStateTransition
	}
	payload := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		payload[k] = v
	}
	payload["status"] = to

	res := s.db.WithContext(ctx).Model(&GenTask{}).
		Where("id = ? AND status = ?", taskID, from).
		Updates(payload)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInvalidStateTransition
	}
	return nil
}

func (s *TaskStore) CreateSlotTasks(ctx context.Context, slots []GenSlotTask) error {
	if len(slots) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&slots).Error
}

func (s *TaskStore) UpdateSlotTask(ctx context.Context, slotTask *GenSlotTask) error {
	return s.db.WithContext(ctx).Save(slotTask).Error
}

func (s *TaskStore) GetSlotTask(ctx context.Context, taskID, slotTaskID uint) (*GenSlotTask, error) {
	var slot GenSlotTask
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND id = ?", taskID, slotTaskID).
		First(&slot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &slot, nil
}

// ─── 5. DocumentStore ───

type DocumentStore struct {
	db *gorm.DB
}

func NewDocumentStore(db *gorm.DB) *DocumentStore {
	return &DocumentStore{db: db}
}

func (s *DocumentStore) GetByID(ctx context.Context, docID uint) (*Document, error) {
	var doc Document
	err := s.db.WithContext(ctx).First(&doc, docID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *DocumentStore) GetByScope(ctx context.Context, repoID uint, subdir, refName, docTypeSlug string) (*Document, error) {
	var doc Document
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND subdirectory = ? AND ref_name = ? AND doc_type_slug = ?",
			repoID, subdir, refName, docTypeSlug).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *DocumentStore) ListByScope(ctx context.Context, repoID uint, subdir, refName string, approvedOnly bool) ([]Document, error) {
	q := s.db.WithContext(ctx).Model(&Document{}).
		Where("repo_id = ? AND subdirectory = ?", repoID, subdir)
	if refName != "" {
		q = q.Where("ref_name = ?", refName)
	}
	if approvedOnly {
		q = q.Where("status = ?", DocStatusApproved)
	}
	var docs []Document
	if err := q.Order("doc_type_slug ASC").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *DocumentStore) ListByRepos(ctx context.Context, repoIDs []uint) ([]Document, error) {
	if len(repoIDs) == 0 {
		return []Document{}, nil
	}
	var docs []Document
	if err := s.db.WithContext(ctx).
		Where("repo_id IN ?", repoIDs).
		Order("repo_id ASC, subdirectory ASC, doc_type_slug ASC").
		Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *DocumentStore) ListRevisionsByIDs(ctx context.Context, revIDs []uint) (map[uint]DocumentRevision, error) {
	res := make(map[uint]DocumentRevision, len(revIDs))
	if len(revIDs) == 0 {
		return res, nil
	}
	var revs []DocumentRevision
	if err := s.db.WithContext(ctx).
		Select("id", "document_id", "revision_no", "snapshot_id", "commit_sha", "doc_type_revision_id", "verifier_passed", "can_approve", "slot_coverage_rate", "created_at").
		Where("id IN ?", revIDs).
		Find(&revs).Error; err != nil {
		return nil, err
	}
	for _, r := range revs {
		res[r.ID] = r
	}
	return res, nil
}

func (s *DocumentStore) ListApprovedDocuments(ctx context.Context) ([]Document, error) {
	var docs []Document
	if err := s.db.WithContext(ctx).
		Where("status = ?", DocStatusApproved).
		Order("id ASC").
		Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *DocumentStore) GetRevisionWithSections(ctx context.Context, docID uint, revisionNo int) (*DocumentRevision, []SectionEvidence, error) {
	var rev DocumentRevision
	err := s.db.WithContext(ctx).
		Where("document_id = ? AND revision_no = ?", docID, revisionNo).
		First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var sections []SectionEvidence
	if err := s.db.WithContext(ctx).
		Where("document_id = ? AND revision_id = ?", docID, rev.ID).
		Order("section_order ASC, id ASC").
		Find(&sections).Error; err != nil {
		return nil, nil, err
	}
	return &rev, sections, nil
}

func (s *DocumentStore) ListRevisions(ctx context.Context, docID uint) ([]DocumentRevision, error) {
	var revs []DocumentRevision
	if err := s.db.WithContext(ctx).
		Where("document_id = ?", docID).
		Order("revision_no DESC").
		Find(&revs).Error; err != nil {
		return nil, err
	}
	return revs, nil
}

func (s *DocumentStore) AppendRevisionWithOptimisticLock(ctx context.Context, doc *Document, baseRevisionNo int, rev *DocumentRevision, sections []SectionEvidence) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, doc.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if current.CurrentRevisionNo != baseRevisionNo {
			return ErrOptimisticLockConflict
		}

		nextRevNo := current.CurrentRevisionNo + 1
		rev.DocumentID = current.ID
		rev.RevisionNo = nextRevNo
		if err := tx.Create(rev).Error; err != nil {
			return err
		}

		if len(sections) > 0 {
			for i := range sections {
				sections[i].DocumentID = current.ID
				sections[i].RevisionID = rev.ID
			}
			if err := tx.Create(&sections).Error; err != nil {
				return err
			}
		}

		revID := rev.ID
		updates := map[string]any{
			"current_revision_no":  nextRevNo,
			"current_revision_id":  revID,
			"doc_type_revision_id": rev.DocTypeRevisionID,
		}
		if doc.Title != "" {
			updates["title"] = doc.Title
		}
		if err := tx.Model(&current).Updates(updates).Error; err != nil {
			return err
		}
		doc.CurrentRevisionNo = nextRevNo
		doc.CurrentRevisionID = &revID
		return nil
	})
}

func (s *DocumentStore) UpdateSectionOwnership(ctx context.Context, docID, revID uint, slotID, ownership string, clearDriftReview bool) error {
	updates := map[string]any{
		"ownership": ownership,
	}
	if clearDriftReview {
		updates["needs_drift_review"] = false
	}
	res := s.db.WithContext(ctx).Model(&SectionEvidence{}).
		Where("document_id = ? AND revision_id = ? AND slot_id = ?", docID, revID, slotID).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DocumentStore) MarkPublished(ctx context.Context, docID, revID uint, commitSHA, relPath string, publishedBy uint, publishedAt time.Time) error {
	res := s.db.WithContext(ctx).Model(&Document{}).
		Where("id = ?", docID).
		Updates(map[string]any{
			"status":                DocStatusApproved,
			"published_revision_id": revID,
			"published_commit_sha":  commitSHA,
			"published_rel_path":    relPath,
			"published_by":          publishedBy,
			"published_at":          publishedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DocumentStore) MarkUnpublished(ctx context.Context, docID uint) error {
	res := s.db.WithContext(ctx).Model(&Document{}).
		Where("id = ?", docID).
		Updates(map[string]any{
			"status":                DocStatusDraft,
			"published_revision_id": nil,
			"published_commit_sha":  "",
			"published_rel_path":    "",
			"published_by":          nil,
			"published_at":          nil,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── 6. ChatStore (W2 仓储实现就绪) ───

type ChatStore struct {
	db *gorm.DB
}

func NewChatStore(db *gorm.DB) *ChatStore {
	return &ChatStore{db: db}
}

func (s *ChatStore) GetOrCreateSession(ctx context.Context, docID uint, slotID string, userID uint, sessionID *uint) (*ChatSession, error) {
	if sessionID != nil && *sessionID > 0 {
		var sess ChatSession
		err := s.db.WithContext(ctx).Where("id = ? AND document_id = ?", *sessionID, docID).First(&sess).Error
		if err == nil {
			return &sess, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	sess := &ChatSession{
		DocumentID:   docID,
		TargetSlotID: slotID,
		CreatedBy:    userID,
	}
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *ChatStore) ListSessionsWithMessages(ctx context.Context, docID uint) ([]ChatSession, map[uint][]ChatMessage, error) {
	var sessions []ChatSession
	if err := s.db.WithContext(ctx).Where("document_id = ?", docID).Order("updated_at DESC, id DESC").Find(&sessions).Error; err != nil {
		return nil, nil, err
	}
	msgMap := make(map[uint][]ChatMessage, len(sessions))
	if len(sessions) == 0 {
		return sessions, msgMap, nil
	}
	ids := make([]uint, 0, len(sessions))
	for _, sess := range sessions {
		ids = append(ids, sess.ID)
	}
	var msgs []ChatMessage
	if err := s.db.WithContext(ctx).Where("session_id IN ?", ids).Order("id ASC").Find(&msgs).Error; err != nil {
		return nil, nil, err
	}
	for _, m := range msgs {
		msgMap[m.SessionID] = append(msgMap[m.SessionID], m)
	}
	return sessions, msgMap, nil
}

func (s *ChatStore) CreateMessage(ctx context.Context, msg *ChatMessage) error {
	return s.db.WithContext(ctx).Create(msg).Error
}

func (s *ChatStore) GetMessage(ctx context.Context, docID, msgID uint) (*ChatMessage, error) {
	var msg ChatMessage
	err := s.db.WithContext(ctx).
		Joins("JOIN "+ChatSession{}.TableName()+" cs ON cs.id = "+ChatMessage{}.TableName()+".session_id").
		Where(ChatMessage{}.TableName()+".id = ? AND cs.document_id = ?", msgID, docID).
		First(&msg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func (s *ChatStore) UpdateProposalStatus(ctx context.Context, msgID uint, status string, appliedRevID *uint) error {
	return s.db.WithContext(ctx).Model(&ChatMessage{}).
		Where("id = ?", msgID).
		Updates(map[string]any{
			"proposal_status":     status,
			"applied_revision_id": appliedRevID,
		}).Error
}

func (s *ChatStore) SupersedePendingProposals(ctx context.Context, docID uint, slotID string, exceptMsgID uint) error {
	subQuery := s.db.WithContext(ctx).Model(&ChatSession{}).Select("id").Where("document_id = ?", docID)
	q := s.db.WithContext(ctx).Model(&ChatMessage{}).
		Where("session_id IN (?) AND proposal_status = ? AND id <> ?", subQuery, ProposalStatusPending, exceptMsgID)
	if slotID != "" {
		q = q.Where("target_slot_id = ?", slotID)
	}
	return q.Update("proposal_status", ProposalStatusSuperseded).Error
}

// ─── 7. DriftStore ───

type DriftStore struct {
	db *gorm.DB
}

func NewDriftStore(db *gorm.DB) *DriftStore {
	return &DriftStore{db: db}
}

func (s *DriftStore) ListRemoteHeadsByRepos(ctx context.Context, repoIDs []uint) ([]RepoRemoteHead, error) {
	if len(repoIDs) == 0 {
		return []RepoRemoteHead{}, nil
	}
	var heads []RepoRemoteHead
	if err := s.db.WithContext(ctx).Where("repo_id IN ?", repoIDs).Find(&heads).Error; err != nil {
		return nil, err
	}
	return heads, nil
}

func (s *DriftStore) ListDriftsByDocIDs(ctx context.Context, docIDs []uint) ([]DocumentDrift, error) {
	if len(docIDs) == 0 {
		return []DocumentDrift{}, nil
	}
	var drifts []DocumentDrift
	if err := s.db.WithContext(ctx).Where("document_id IN ?", docIDs).Find(&drifts).Error; err != nil {
		return nil, err
	}
	return drifts, nil
}

func (s *DriftStore) UpsertRemoteHead(ctx context.Context, head *RepoRemoteHead) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_id"}, {Name: "ref_name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"remote_head_sha", "last_polled_at", "last_success_at",
			"consecutive_failures", "last_error", "next_retry_at", "updated_at",
		}),
	}).Create(head).Error
}

func (s *DriftStore) UpsertDocumentDrift(ctx context.Context, drift *DocumentDrift) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "document_id"}, {Name: "drift_type"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"has_drift", "precision", "base_commit_sha", "remote_head_sha",
			"bound_doc_type_revision_id", "latest_doc_type_revision_id",
			"affected_slot_count", "affected_slots_json", "detected_at", "updated_at",
		}),
	}).Create(drift).Error
}

// ─── 8. GovernanceStore ───

type GovernanceStore struct {
	db *gorm.DB
}

func NewGovernanceStore(db *gorm.DB) *GovernanceStore {
	return &GovernanceStore{db: db}
}

func (s *GovernanceStore) ListLLMConfigs(ctx context.Context) ([]LLMConfig, error) {
	var list []LLMConfig
	if err := s.db.WithContext(ctx).Order("tier ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *GovernanceStore) GetLLMConfigByTier(ctx context.Context, tier string) (*LLMConfig, error) {
	var cfg LLMConfig
	err := s.db.WithContext(ctx).Where("tier = ?", tier).First(&cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *GovernanceStore) SaveLLMConfig(ctx context.Context, cfg *LLMConfig) error {
	return s.db.WithContext(ctx).Save(cfg).Error
}

func (s *GovernanceStore) UpdateLLMProbeResult(ctx context.Context, tier, status string, latencyMs int64, probedAt time.Time) error {
	res := s.db.WithContext(ctx).Model(&LLMConfig{}).
		Where("tier = ?", tier).
		Updates(map[string]any{
			"last_probe_status":     status,
			"last_probe_latency_ms": latencyMs,
			"last_probed_at":        probedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *GovernanceStore) RecordLLMUsage(ctx context.Context, logItem *LLMUsageLog) error {
	return s.db.WithContext(ctx).Create(logItem).Error
}

func (s *GovernanceStore) RecordQualityMetric(ctx context.Context, metric *QualityMetric) error {
	return s.db.WithContext(ctx).Create(metric).Error
}
