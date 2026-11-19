package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/doctype"

	"gorm.io/datatypes"
)

// DocTypeSummaryVO 对应 GET /api/doc-types 列表条目（对齐 01-http-api-contract.md §3.3）。
type DocTypeSummaryVO struct {
	ID                  uint      `json:"id"`
	Slug                string    `json:"slug"`
	DisplayName         string    `json:"display_name"`
	Description         string    `json:"description"`
	IsBuiltin           bool      `json:"is_builtin"`
	IsActive            bool      `json:"is_active"`
	ModelTier           string    `json:"model_tier"`
	TimeoutSeconds      int       `json:"timeout_seconds"`
	IRFacets            []string  `json:"ir_facets"`
	SlotCount           int       `json:"slot_count"`
	LatestRevisionID    *uint     `json:"latest_revision_id"`
	LatestRevisionNo    int       `json:"latest_revision_no"`
	LatestAggregateHash string    `json:"latest_aggregate_hash"`
	PublishedDocCount   int64     `json:"published_doc_count"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// DocTypeRevisionSummaryVO 对应 GET /api/doc-types/:slug/revisions 条目。
type DocTypeRevisionSummaryVO struct {
	ID            uint                    `json:"id"`
	RevisionNo    int                     `json:"revision_no"`
	AggregateHash string                  `json:"aggregate_hash"`
	PromptHash    string                  `json:"prompt_hash"`
	SlotsSnapshot []models.SlotDefinition `json:"slots_snapshot"`
	CreatedBy     *uint                   `json:"created_by"`
	CreatedAt     time.Time               `json:"created_at"`
}

// DocTypeDetailVO 对应 GET /api/doc-types/:slug 详情响应。
type DocTypeDetailVO struct {
	DocTypeSummaryVO
	Meta               doctype.Meta              `json:"meta"`
	Slots              []models.SlotDefinition   `json:"slots"`
	TemplateMD         string                    `json:"template_md"`
	GenerationPromptMD string                    `json:"generation_prompt_md"`
	RefinePromptMD     string                    `json:"refine_prompt_md"`
	LatestRevision     *DocTypeRevisionSummaryVO `json:"latest_revision"`
}

// DocTypeSyncResult 汇总一次目录 -> DB 同步结果。
type DocTypeSyncResult struct {
	SyncedCount  int      `json:"synced_count"`
	NewRevisions int      `json:"new_revisions"`
	Slugs        []string `json:"slugs"`
}

// DocTypeService 编排 DocType 目录 SSOT 幂等 Seed、目录 -> DB 同步与 W1 只读查询（F-03）。
type DocTypeService struct {
	registry *doctype.Registry
	repo     DocTypeRepo
}

func NewDocTypeService(registry *doctype.Registry, repo DocTypeRepo) *DocTypeService {
	return &DocTypeService{
		registry: registry,
		repo:     repo,
	}
}

// Registry 返回底层目录注册表实例（供生成编排器读取资产）。
func (s *DocTypeService) Registry() *doctype.Registry {
	return s.registry
}

// SeedAndSyncAll 服务启动幂等初始化：先幂等 Seed 5 类内置 DocType，再同步全量目录至数据库索引。
func (s *DocTypeService) SeedAndSyncAll(ctx context.Context) (*DocTypeSyncResult, error) {
	if _, err := s.registry.SeedBuiltin(); err != nil {
		return nil, fmt.Errorf("初始化内置 DocType 失败: %w", err)
	}
	return s.SyncAll(ctx, nil)
}

// SyncAll 扫描 doc_types/ 根目录并幂等同步至 archdoc_doc_types / archdoc_doc_type_revisions。
func (s *DocTypeService) SyncAll(ctx context.Context, operatorID *uint) (*DocTypeSyncResult, error) {
	bundles, err := s.registry.LoadAll()
	if err != nil {
		return nil, err
	}

	res := &DocTypeSyncResult{
		Slugs: make([]string, 0, len(bundles)),
	}
	for _, b := range bundles {
		createdRev, syncErr := s.syncBundle(ctx, b, operatorID)
		if syncErr != nil {
			return nil, fmt.Errorf("同步 DocType %s 失败: %w", b.Slug, syncErr)
		}
		res.SyncedCount++
		res.Slugs = append(res.Slugs, b.Slug)
		if createdRev {
			res.NewRevisions++
		}
	}
	return res, nil
}

// SyncOne 同步单个 doc_types/<slug>/ 目录至 DB。
func (s *DocTypeService) SyncOne(ctx context.Context, slug string, operatorID *uint) (bool, error) {
	b, err := s.registry.LoadOne(slug)
	if err != nil {
		return false, err
	}
	return s.syncBundle(ctx, b, operatorID)
}

func (s *DocTypeService) syncBundle(ctx context.Context, b *doctype.AssetBundle, operatorID *uint) (bool, error) {
	irFacetsJSON, err := json.Marshal(b.Meta.IRFacets)
	if err != nil {
		return false, err
	}
	metaJSON, err := json.Marshal(b.Meta)
	if err != nil {
		return false, err
	}
	slotDefs := toModelSlotDefs(b.Meta.Slots)
	slotsJSON, err := json.Marshal(slotDefs)
	if err != nil {
		return false, err
	}

	dt := &models.DocType{
		Slug:           b.Slug,
		DisplayName:    b.Meta.DisplayName,
		Description:    b.Meta.Description,
		IsBuiltin:      b.Meta.IsBuiltin,
		IsActive:       b.Meta.IsActive,
		ModelTier:      b.Meta.ModelTier,
		TimeoutSeconds: b.Meta.TimeoutSeconds,
		IRFacetsJSON:   datatypes.JSON(irFacetsJSON),
		UpdatedBy:      operatorID,
	}

	newRev := &models.DocTypeRevision{
		DocTypeSlug:        b.Slug,
		AggregateHash:      b.AggregateHash,
		MetaJSON:           datatypes.JSON(metaJSON),
		SlotsSnapshotJSON:  datatypes.JSON(slotsJSON),
		TemplateMD:         b.TemplateMD,
		GenerationPromptMD: b.GenerationPromptMD,
		RefinePromptMD:     b.RefinePromptMD,
		PromptHash:         b.PromptHash,
		CreatedBy:          operatorID,
	}

	return s.repo.UpsertFromDirectorySync(ctx, dt, newRev)
}

// List 查询 DocType 列表（支持 active_only）。
func (s *DocTypeService) List(ctx context.Context, activeOnly bool) ([]DocTypeSummaryVO, error) {
	list, err := s.repo.List(ctx, activeOnly)
	if err != nil {
		return nil, err
	}
	items := make([]DocTypeSummaryVO, 0, len(list))
	for i := range list {
		dt := &list[i]
		slotCount := 0
		if dt.LatestRevisionID != nil {
			if rev, revErr := s.repo.GetRevisionByID(ctx, *dt.LatestRevisionID); revErr == nil {
				var slots []models.SlotDefinition
				if json.Unmarshal(rev.SlotsSnapshotJSON, &slots) == nil {
					slotCount = len(slots)
				}
			}
		}
		pubCount, _ := s.repo.CountApprovedDocumentsBySlug(ctx, dt.Slug)
		items = append(items, toDocTypeSummaryVO(dt, slotCount, pubCount))
	}
	return items, nil
}

// GetDetail 获取单个 DocType 完整元数据、槽位与最新修订资产。
func (s *DocTypeService) GetDetail(ctx context.Context, slug string) (*DocTypeDetailVO, error) {
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, err
	}
	dt, rev, err := s.repo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	pubCount, _ := s.repo.CountApprovedDocumentsBySlug(ctx, dt.Slug)

	var meta doctype.Meta
	var slots []models.SlotDefinition
	var templateMD, genPromptMD, refinePromptMD string
	var latestRevVO *DocTypeRevisionSummaryVO

	if rev != nil {
		_ = json.Unmarshal(rev.MetaJSON, &meta)
		_ = json.Unmarshal(rev.SlotsSnapshotJSON, &slots)
		templateMD = rev.TemplateMD
		genPromptMD = rev.GenerationPromptMD
		refinePromptMD = rev.RefinePromptMD
		vo := toDocTypeRevisionVO(rev)
		latestRevVO = &vo
	}

	return &DocTypeDetailVO{
		DocTypeSummaryVO:   toDocTypeSummaryVO(dt, len(slots), pubCount),
		Meta:               meta,
		Slots:              slots,
		TemplateMD:         templateMD,
		GenerationPromptMD: genPromptMD,
		RefinePromptMD:     refinePromptMD,
		LatestRevision:     latestRevVO,
	}, nil
}

// ListRevisions 查询 DocType 不可变修订历史（按 revision_no DESC 排序）。
func (s *DocTypeService) ListRevisions(ctx context.Context, slug string) ([]DocTypeRevisionSummaryVO, error) {
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, err
	}
	if _, _, err := s.repo.GetBySlug(ctx, slug); err != nil {
		return nil, err
	}
	revs, err := s.repo.ListRevisions(ctx, slug)
	if err != nil {
		return nil, err
	}
	out := make([]DocTypeRevisionSummaryVO, 0, len(revs))
	for i := range revs {
		out = append(out, toDocTypeRevisionVO(&revs[i]))
	}
	return out, nil
}

func toModelSlotDefs(specs []doctype.SlotSpec) []models.SlotDefinition {
	out := make([]models.SlotDefinition, 0, len(specs))
	for _, sl := range specs {
		out = append(out, models.SlotDefinition{
			SlotID:          sl.SlotID,
			Title:           sl.Title,
			Ownership:       sl.Ownership,
			Required:        sl.Required,
			IRFacets:        sl.IRFacets,
			TokenBudget:     sl.TokenBudget,
			MinChars:        sl.MinChars,
			InstructionHint: sl.InstructionHint,
		})
	}
	return out
}

func toDocTypeSummaryVO(dt *models.DocType, slotCount int, pubCount int64) DocTypeSummaryVO {
	var facets []string
	_ = json.Unmarshal(dt.IRFacetsJSON, &facets)
	if facets == nil {
		facets = []string{}
	}
	return DocTypeSummaryVO{
		ID:                  dt.ID,
		Slug:                dt.Slug,
		DisplayName:         dt.DisplayName,
		Description:         dt.Description,
		IsBuiltin:           dt.IsBuiltin,
		IsActive:            dt.IsActive,
		ModelTier:           dt.ModelTier,
		TimeoutSeconds:      dt.TimeoutSeconds,
		IRFacets:            facets,
		SlotCount:           slotCount,
		LatestRevisionID:    dt.LatestRevisionID,
		LatestRevisionNo:    dt.LatestRevisionNo,
		LatestAggregateHash: dt.LatestAggregateHash,
		PublishedDocCount:   pubCount,
		UpdatedAt:           dt.UpdatedAt,
	}
}

func toDocTypeRevisionVO(rev *models.DocTypeRevision) DocTypeRevisionSummaryVO {
	var slots []models.SlotDefinition
	_ = json.Unmarshal(rev.SlotsSnapshotJSON, &slots)
	if slots == nil {
		slots = []models.SlotDefinition{}
	}
	return DocTypeRevisionSummaryVO{
		ID:            rev.ID,
		RevisionNo:    rev.RevisionNo,
		AggregateHash: rev.AggregateHash,
		PromptHash:    rev.PromptHash,
		SlotsSnapshot: slots,
		CreatedBy:     rev.CreatedBy,
		CreatedAt:     rev.CreatedAt,
	}
}
