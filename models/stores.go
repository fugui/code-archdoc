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
	TaskKind     string
	TopLevelOnly bool
	TriggeredBy  *uint
	Offset       int
	Limit        int
}

type TaskSummaryRecord struct {
	Total    int64
	Active   int64
	Ready    int64
	Failed   int64
	Canceled int64
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

type SectionVerifierMetricRow struct {
	SlotID              string
	Title               string
	VerifierSummaryJSON []byte
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

func (s *SharedMasterStore) ListAllArchElements(ctx context.Context) ([]ArchitectureElement, error) {
	var elems []ArchitectureElement
	if err := s.db.WithContext(ctx).
		Order("id ASC").
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
			"short_name":      dt.ShortName,
			"category":        dt.Category,
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

func (s *SnapshotStore) GetByID(ctx context.Context, snapshotID uint) (*RepoSnapshot, error) {
	var snap RepoSnapshot
	err := s.db.WithContext(ctx).First(&snap, snapshotID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
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
			"base_snapshot_id", "status", "effective_files", "skipped_files", "total_loc",
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

func (s *SnapshotStore) CountFacetStats(ctx context.Context) (activeCount int64, evictedCount int64, activeBytes int64, err error) {
	if err = s.db.WithContext(ctx).Model(&CodeIRFacet{}).Where("evicted_at IS NULL").Count(&activeCount).Error; err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.WithContext(ctx).Model(&CodeIRFacet{}).Where("evicted_at IS NOT NULL").Count(&evictedCount).Error; err != nil {
		return 0, 0, 0, err
	}
	type sumRow struct {
		Total int64
	}
	var sr sumRow
	if err = s.db.WithContext(ctx).Model(&CodeIRFacet{}).
		Select("COALESCE(SUM(compressed_bytes), 0) AS total").
		Where("evicted_at IS NULL").
		Scan(&sr).Error; err != nil {
		return 0, 0, 0, err
	}
	return activeCount, evictedCount, sr.Total, nil
}

func (s *SnapshotStore) ListPublishedSnapshotIDs(ctx context.Context) (map[uint]bool, error) {
	docTable := Document{}.TableName()
	revTable := DocumentRevision{}.TableName()
	var ids []uint
	if err := s.db.WithContext(ctx).
		Table(docTable+" d").
		Select("DISTINCT r.snapshot_id").
		Joins("JOIN "+revTable+" r ON r.id = d.published_revision_id").
		Where("d.status = ? AND d.published_revision_id IS NOT NULL", DocStatusApproved).
		Pluck("r.snapshot_id", &ids).Error; err != nil {
		return nil, err
	}
	res := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if id > 0 {
			res[id] = true
		}
	}
	return res, nil
}

func (s *SnapshotStore) ListLatestAccessByRepo(ctx context.Context) (map[uint]time.Time, error) {
	type row struct {
		RepoID     uint
		LastAccess time.Time
	}
	var rows []row
	if err := s.db.WithContext(ctx).
		Model(&RepoSnapshot{}).
		Select("repo_id, MAX(last_accessed_at) AS last_access").
		Group("repo_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]time.Time, len(rows))
	for _, r := range rows {
		out[r.RepoID] = r.LastAccess
	}
	return out, nil
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

func (s *TaskStore) applyTaskFilter(ctx context.Context, filter TaskListFilter, includeStatus bool) *gorm.DB {
	q := s.db.WithContext(ctx).Model(&GenTask{})
	if filter.RepoID != nil {
		q = q.Where("repo_id = ?", *filter.RepoID)
	}
	if filter.Subdirectory != nil {
		q = q.Where("subdirectory = ?", *filter.Subdirectory)
	}
	if filter.TaskKind != "" {
		q = q.Where("task_kind = ?", filter.TaskKind)
	}
	if filter.TopLevelOnly {
		q = q.Where("parent_task_id IS NULL")
	}
	if filter.TriggeredBy != nil {
		q = q.Where("triggered_by = ?", *filter.TriggeredBy)
	}
	if includeStatus && filter.Status != "" {
		if filter.Status == "active" {
			q = q.Where("status IN ?", []string{
				TaskStatusPending,
				TaskStatusExtracting,
				TaskStatusGenerating,
				TaskStatusVerifying,
			})
		} else {
			q = q.Where("status = ?", filter.Status)
		}
	}
	return q
}

func (s *TaskStore) ListTasks(ctx context.Context, filter TaskListFilter) ([]GenTask, int64, error) {
	q := s.applyTaskFilter(ctx, filter, true)

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

// CountTaskSummary 按非状态筛选维度聚合统计各任务状态的分布数量。
func (s *TaskStore) CountTaskSummary(ctx context.Context, filter TaskListFilter) (*TaskSummaryRecord, error) {
	type statusCountRow struct {
		Status string
		Cnt    int64
	}
	var rows []statusCountRow
	if err := s.applyTaskFilter(ctx, filter, false).
		Select("status, COUNT(*) AS cnt").
		Group("status").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	summary := &TaskSummaryRecord{}
	for _, row := range rows {
		summary.Total += row.Cnt
		switch row.Status {
		case TaskStatusPending, TaskStatusExtracting, TaskStatusGenerating, TaskStatusVerifying:
			summary.Active += row.Cnt
		case TaskStatusReady:
			summary.Ready += row.Cnt
		case TaskStatusFailed:
			summary.Failed += row.Cnt
		case TaskStatusCanceled:
			summary.Canceled += row.Cnt
		}
	}
	return summary, nil
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

func (s *TaskStore) UpdateTaskFields(ctx context.Context, taskID uint, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&GenTask{}).Where("id = ?", taskID).Updates(updates).Error
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

func (s *TaskStore) ListChildTasks(ctx context.Context, parentTaskID uint) ([]GenTask, error) {
	var children []GenTask
	if err := s.db.WithContext(ctx).
		Where("parent_task_id = ?", parentTaskID).
		Order("id ASC").
		Find(&children).Error; err != nil {
		return nil, err
	}
	return children, nil
}

func (s *TaskStore) TransitionBatchTaskStatus(ctx context.Context, taskID uint, from, to string, updates map[string]any) error {
	if !CanTransitionBatchTaskStatus(from, to) {
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

func (s *TaskStore) HasActiveTasksInSubdirs(ctx context.Context, repoID uint, subdirs []string) (bool, error) {
	if len(subdirs) == 0 {
		return false, nil
	}
	activeStatuses := []string{TaskStatusPending, TaskStatusExtracting, TaskStatusGenerating, TaskStatusVerifying}
	var count int64
	if err := s.db.WithContext(ctx).Model(&GenTask{}).
		Where("repo_id = ? AND subdirectory IN ? AND status IN ?", repoID, subdirs, activeStatuses).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
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

func (s *DocumentStore) GetOrCreateDocument(ctx context.Context, doc *Document) (*Document, error) {
	var existing Document
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND subdirectory = ? AND ref_name = ? AND doc_type_slug = ?",
			doc.RepoID, doc.Subdirectory, doc.RefName, doc.DocTypeSlug).
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if createErr := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "repo_id"},
			{Name: "subdirectory"},
			{Name: "ref_name"},
			{Name: "doc_type_slug"},
		},
		DoNothing: true,
	}).Create(doc).Error; createErr != nil {
		return nil, createErr
	}
	if err := s.db.WithContext(ctx).
		Where("repo_id = ? AND subdirectory = ? AND ref_name = ? AND doc_type_slug = ?",
			doc.RepoID, doc.Subdirectory, doc.RefName, doc.DocTypeSlug).
		First(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
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

func (s *DocumentStore) ListApprovedDocumentsByRepo(ctx context.Context, repoID uint) ([]Document, error) {
	var docs []Document
	if err := s.db.WithContext(ctx).
		Where("repo_id = ? AND status = ?", repoID, DocStatusApproved).
		Order("id ASC").
		Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *DocumentStore) ListByIDs(ctx context.Context, docIDs []uint, approvedOnly bool) ([]Document, error) {
	if len(docIDs) == 0 {
		return []Document{}, nil
	}
	q := s.db.WithContext(ctx).Model(&Document{}).Where("id IN ?", docIDs)
	if approvedOnly {
		q = q.Where("status = ?", DocStatusApproved)
	}
	var docs []Document
	if err := q.Order("id ASC").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (s *DocumentStore) GetRevisionByIDWithSections(ctx context.Context, docID, revID uint) (*DocumentRevision, []SectionEvidence, error) {
	var rev DocumentRevision
	err := s.db.WithContext(ctx).
		Where("document_id = ? AND id = ?", docID, revID).
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

func (s *DocumentStore) GetFirstAIGenerateRevision(ctx context.Context, docID uint) (*DocumentRevision, error) {
	var rev DocumentRevision
	err := s.db.WithContext(ctx).
		Where("document_id = ? AND source = ?", docID, RevSourceAIGenerate).
		Order("revision_no ASC").
		First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
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

// MigrateScopeDocuments 在单个数据库事务中将 (repoID, fromSubdir) 下的全部文档迁移至 toSubdir，
// 目标键存在任意冲突时整批回滚并返回 ErrMigrationConflict。返回迁移前的文档快照（含原 PublishedRelPath，供调用方执行重渲染与旧文件清理）。
func (s *DocumentStore) MigrateScopeDocuments(
	ctx context.Context,
	repoID uint,
	fromSubdir, toSubdir string,
	titleBuilder func(docTypeSlug, oldTitle string) string,
) ([]Document, error) {
	var migratedBefore []Document
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var srcDocs []Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("repo_id = ? AND subdirectory = ?", repoID, fromSubdir).
			Order("id ASC").
			Find(&srcDocs).Error; err != nil {
			return err
		}
		if len(srcDocs) == 0 {
			return nil
		}

		// 冲突前置检查：目标 (repo_id, toSubdir, ref_name, doc_type_slug) 不得已存在文档
		var existingTargets []Document
		if err := tx.Where("repo_id = ? AND subdirectory = ?", repoID, toSubdir).Find(&existingTargets).Error; err != nil {
			return err
		}
		conflictSet := make(map[string]bool, len(existingTargets))
		for _, td := range existingTargets {
			conflictSet[td.RefName+"\x00"+td.DocTypeSlug] = true
		}
		for _, sd := range srcDocs {
			if conflictSet[sd.RefName+"\x00"+sd.DocTypeSlug] {
				return ErrMigrationConflict
			}
		}

		migratedBefore = make([]Document, len(srcDocs))
		for i, sd := range srcDocs {
			migratedBefore[i] = sd
			newTitle := sd.Title
			if titleBuilder != nil {
				if built := titleBuilder(sd.DocTypeSlug, sd.Title); built != "" {
					newTitle = built
				}
			}
			if err := tx.Model(&Document{}).Where("id = ?", sd.ID).Updates(map[string]any{
				"subdirectory": toSubdir,
				"title":        newTitle,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return migratedBefore, nil
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

func (s *ChatStore) UpdateMessage(ctx context.Context, msg *ChatMessage) error {
	return s.db.WithContext(ctx).Save(msg).Error
}

func (s *ChatStore) DeleteMessage(ctx context.Context, msgID uint) error {
	return s.db.WithContext(ctx).Delete(&ChatMessage{}, msgID).Error
}

func (s *ChatStore) TouchSession(ctx context.Context, sessionID uint, title string) error {
	updates := map[string]any{
		"updated_at": time.Now(),
	}
	if title != "" {
		updates["title"] = title
	}
	return s.db.WithContext(ctx).Model(&ChatSession{}).Where("id = ?", sessionID).Updates(updates).Error
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

func (s *DriftStore) ClearDocumentDrifts(ctx context.Context, docID uint) error {
	return s.db.WithContext(ctx).Model(&DocumentDrift{}).
		Where("document_id = ?", docID).
		Updates(map[string]any{
			"has_drift":           false,
			"affected_slot_count": 0,
			"affected_slots_json": "[]",
			"updated_at":          time.Now(),
		}).Error
}

func (s *DriftStore) GetRemoteHead(ctx context.Context, repoID uint, refName string) (*RepoRemoteHead, error) {
	var head RepoRemoteHead
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND ref_name = ?", repoID, refName).
		First(&head).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &head, nil
}

func (s *DriftStore) ListAllRemoteHeads(ctx context.Context) ([]RepoRemoteHead, error) {
	var heads []RepoRemoteHead
	if err := s.db.WithContext(ctx).Order("repo_id ASC, id ASC").Find(&heads).Error; err != nil {
		return nil, err
	}
	return heads, nil
}

// ListPollableRepos 仅查询包含至少 1 篇已发布 (approved) 文档的活跃代码仓（F-25 巡检收敛红线）。
func (s *DriftStore) ListPollableRepos(ctx context.Context) ([]PollableRepoRecord, error) {
	docTable := Document{}.TableName()
	var rows []PollableRepoRecord
	err := s.db.WithContext(ctx).
		Table("repositories r").
		Select("DISTINCT r.id AS repo_id, r.url AS repo_url, COALESCE(NULLIF(r.branch, ''), 'master') AS branch").
		Joins("JOIN "+docTable+" d ON d.repo_id = r.id AND d.status = ?", DocStatusApproved).
		Where("r.is_active = ?", true).
		Order("r.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetDriftSummary 聚合已发布文档的双维漂移 KPI、受监控活跃仓库数与最近巡检时间（GET /api/drift/summary）。
func (s *DriftStore) GetDriftSummary(ctx context.Context) (*DriftSummaryRecord, error) {
	pollable, err := s.ListPollableRepos(ctx)
	if err != nil {
		return nil, err
	}

	monitoredIDs := make([]uint, 0, len(pollable))
	for _, p := range pollable {
		monitoredIDs = append(monitoredIDs, p.RepoID)
	}

	summary := &DriftSummaryRecord{
		MonitoredRepos: int64(len(monitoredIDs)),
	}

	if len(monitoredIDs) > 0 {
		heads, hErr := s.ListRemoteHeadsByRepos(ctx, monitoredIDs)
		if hErr != nil {
			return nil, hErr
		}
		unhealthyRepoSet := make(map[uint]bool)
		for _, h := range heads {
			if h.ConsecutiveFailures > 0 {
				unhealthyRepoSet[h.RepoID] = true
			}
			if h.LastPolledAt != nil {
				if summary.LastPollAt == nil || h.LastPolledAt.After(*summary.LastPollAt) {
					t := *h.LastPolledAt
					summary.LastPollAt = &t
				}
			}
		}
		summary.UnhealthyRepos = int64(len(unhealthyRepoSet))
	}

	// 仅统计已发布文档的有效漂移
	docTable := Document{}.TableName()
	driftTable := DocumentDrift{}.TableName()
	type driftFlagRow struct {
		DocumentID uint
		DriftType  string
	}
	var rows []driftFlagRow
	if err := s.db.WithContext(ctx).
		Table(driftTable+" dr").
		Select("dr.document_id, dr.drift_type").
		Joins("JOIN "+docTable+" d ON d.id = dr.document_id").
		Where("d.status = ? AND dr.has_drift = ?", DocStatusApproved, true).
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	codeDocs := make(map[uint]bool)
	specDocs := make(map[uint]bool)
	allDocs := make(map[uint]bool)
	for _, r := range rows {
		allDocs[r.DocumentID] = true
		switch r.DriftType {
		case DriftTypeCode:
			codeDocs[r.DocumentID] = true
		case DriftTypeSpec:
			specDocs[r.DocumentID] = true
		}
	}
	bothCount := int64(0)
	for docID := range codeDocs {
		if specDocs[docID] {
			bothCount++
		}
	}
	summary.TotalDriftedDocs = int64(len(allDocs))
	summary.CodeDriftDocs = int64(len(codeDocs))
	summary.SpecDriftDocs = int64(len(specDocs))
	summary.BothDriftDocs = bothCount
	return summary, nil
}

// ListDriftedDocuments 按筛选条件分页查询存在 Code/Spec 漂移的已发布文档（GET /api/drift/documents）。
func (s *DriftStore) ListDriftedDocuments(ctx context.Context, filter DriftQueryFilter) ([]DriftAggregateRecord, int64, error) {
	docTable := Document{}.TableName()
	driftTable := DocumentDrift{}.TableName()

	base := s.db.WithContext(ctx).
		Table(docTable+" d").
		Joins("JOIN repositories r ON r.id = d.repo_id").
		Where("d.status = ? AND r.is_active = ?", DocStatusApproved, true)

	if filter.DepartmentID != nil && *filter.DepartmentID > 0 {
		base = base.Where("r.department_id = ?", *filter.DepartmentID)
	}
	if filter.DocTypeSlug != "" {
		base = base.Where("d.doc_type_slug = ?", filter.DocTypeSlug)
	}
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		base = base.Where("(LOWER(r.name) LIKE LOWER(?) OR LOWER(d.subdirectory) LIKE LOWER(?) OR LOWER(d.title) LIKE LOWER(?))", like, like, like)
	}

	switch filter.DriftType {
	case DriftTypeCode:
		base = base.Where("EXISTS (SELECT 1 FROM "+driftTable+" dr WHERE dr.document_id = d.id AND dr.drift_type = ? AND dr.has_drift = ?)", DriftTypeCode, true)
	case DriftTypeSpec:
		base = base.Where("EXISTS (SELECT 1 FROM "+driftTable+" dr WHERE dr.document_id = d.id AND dr.drift_type = ? AND dr.has_drift = ?)", DriftTypeSpec, true)
	case "both":
		base = base.Where("EXISTS (SELECT 1 FROM "+driftTable+" dr1 WHERE dr1.document_id = d.id AND dr1.drift_type = ? AND dr1.has_drift = ?) AND EXISTS (SELECT 1 FROM "+driftTable+" dr2 WHERE dr2.document_id = d.id AND dr2.drift_type = ? AND dr2.has_drift = ?)", DriftTypeCode, true, DriftTypeSpec, true)
	default:
		base = base.Where("EXISTS (SELECT 1 FROM "+driftTable+" dr WHERE dr.document_id = d.id AND dr.has_drift = ?)", true)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []DriftAggregateRecord{}, 0, nil
	}

	var docs []Document
	query := base.Select("d.*").Order("d.updated_at DESC, d.id ASC")
	if filter.Limit > 0 {
		query = query.Offset(filter.Offset).Limit(filter.Limit)
	}
	if err := query.Find(&docs).Error; err != nil {
		return nil, 0, err
	}

	docIDs := make([]uint, 0, len(docs))
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
	}
	drifts, err := s.ListDriftsByDocIDs(ctx, docIDs)
	if err != nil {
		return nil, 0, err
	}
	codeMap := make(map[uint]*DocumentDrift, len(docs))
	specMap := make(map[uint]*DocumentDrift, len(docs))
	for i := range drifts {
		dr := drifts[i]
		if dr.DriftType == DriftTypeCode {
			codeMap[dr.DocumentID] = &dr
		} else if dr.DriftType == DriftTypeSpec {
			specMap[dr.DocumentID] = &dr
		}
	}

	out := make([]DriftAggregateRecord, 0, len(docs))
	for _, d := range docs {
		out = append(out, DriftAggregateRecord{
			Document:  d,
			CodeDrift: codeMap[d.ID],
			SpecDrift: specMap[d.ID],
		})
	}
	return out, total, nil
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

func (s *GovernanceStore) ListQualityMetrics(ctx context.Context, filter MetricsFilter) ([]QualityMetric, error) {
	q := s.db.WithContext(ctx).Model(&QualityMetric{})
	if filter.Days > 0 {
		since := time.Now().AddDate(0, 0, -filter.Days)
		q = q.Where("created_at >= ?", since)
	}
	if filter.DocTypeSlug != "" {
		q = q.Where("doc_type_slug = ?", filter.DocTypeSlug)
	}
	if filter.DocTypeRevisionID != nil && *filter.DocTypeRevisionID > 0 {
		q = q.Where("doc_type_revision_id = ?", *filter.DocTypeRevisionID)
	}
	var list []QualityMetric
	if err := q.Order("created_at ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *GovernanceStore) ListLLMUsageLogs(ctx context.Context, filter MetricsFilter) ([]LLMUsageLog, error) {
	q := s.db.WithContext(ctx).Model(&LLMUsageLog{})
	if filter.Days > 0 {
		since := time.Now().AddDate(0, 0, -filter.Days)
		q = q.Where("created_at >= ?", since)
	}
	if filter.DocTypeSlug != "" {
		q = q.Where("doc_type_slug = ?", filter.DocTypeSlug)
	}
	if filter.DocTypeRevisionID != nil && *filter.DocTypeRevisionID > 0 {
		q = q.Where("doc_type_revision_id = ?", *filter.DocTypeRevisionID)
	}
	var list []LLMUsageLog
	if err := q.Order("created_at ASC, id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (s *GovernanceStore) SummarizeLLMUsageSince(ctx context.Context, since time.Time) ([]UsageSummaryRecord, error) {
	var rows []UsageSummaryRecord
	err := s.db.WithContext(ctx).
		Model(&LLMUsageLog{}).
		Select("model_tier, doc_type_slug, COALESCE(SUM(prompt_tokens + completion_tokens), 0) AS total_tokens, COALESCE(SUM(cached_prompt_tokens), 0) AS cached_prompt_tokens, COUNT(*) AS call_count").
		Where("created_at >= ?", since).
		Group("model_tier, doc_type_slug").
		Order("total_tokens DESC, model_tier ASC, doc_type_slug ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *GovernanceStore) ListSectionVerifierSummaries(ctx context.Context, docTypeSlug string, docTypeRevID *uint, since time.Time) ([]SectionVerifierMetricRow, error) {
	docTable := Document{}.TableName()
	revTable := DocumentRevision{}.TableName()
	secTable := SectionEvidence{}.TableName()

	q := s.db.WithContext(ctx).
		Table(secTable+" se").
		Select("se.slot_id, se.title, se.verifier_summary_json").
		Joins("JOIN "+revTable+" r ON r.id = se.revision_id").
		Joins("JOIN "+docTable+" d ON d.id = se.document_id").
		Where("d.doc_type_slug = ? AND r.created_at >= ?", docTypeSlug, since)

	if docTypeRevID != nil && *docTypeRevID > 0 {
		q = q.Where("r.doc_type_revision_id = ?", *docTypeRevID)
	}

	var rows []SectionVerifierMetricRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
