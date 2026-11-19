package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/drift"

	"gorm.io/datatypes"
)

// DocTypeInUseError 表示目标 DocType 被已发布文档引用，拒绝物理删除（HTTP 409 DOC_TYPE_IN_USE）。
type DocTypeInUseError struct {
	Slug              string
	PublishedDocCount int64
}

func (e *DocTypeInUseError) Error() string {
	return fmt.Sprintf("文档类型 %q 已被 %d 篇已发布文档引用，禁止物理删除，请改为停用", e.Slug, e.PublishedDocCount)
}

func (e *DocTypeInUseError) Unwrap() error {
	return models.ErrDocTypeInUse
}

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

// DocTypeUpdateVO 对应 PUT /api/admin/doc-types/:slug 响应（扩展 revision_created 标记）。
type DocTypeUpdateVO struct {
	DocTypeDetailVO
	RevisionCreated bool `json:"revision_created"`
}

// DocTypeSyncResult 汇总一次目录 -> DB 同步结果。
type DocTypeSyncResult struct {
	SyncedCount  int      `json:"synced_count"`
	NewRevisions int      `json:"new_revisions"`
	Slugs        []string `json:"slugs"`
}

// DocTypeAuditSnapshot 面向 P0 审计 Diff 的结构化精简视图（避免大模板撑爆审计日志表）。
type DocTypeAuditSnapshot struct {
	Slug                string   `json:"slug"`
	DisplayName         string   `json:"display_name"`
	Description         string   `json:"description"`
	IsActive            bool     `json:"is_active"`
	IsBuiltin           bool     `json:"is_builtin"`
	ModelTier           string   `json:"model_tier"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	LatestRevisionID    *uint    `json:"latest_revision_id"`
	LatestRevisionNo    int      `json:"latest_revision_no"`
	LatestAggregateHash string   `json:"latest_aggregate_hash"`
	SlotIDs             []string `json:"slot_ids"`
}

// ─── W2 管理员写请求 DTO ───

type CreateDocTypeRequest struct {
	Slug               string                      `json:"slug"`
	DisplayName        string                      `json:"display_name"`
	Description        string                      `json:"description"`
	IsActive           *bool                       `json:"is_active"`
	ModelTier          string                      `json:"model_tier"`
	TimeoutSeconds     int                         `json:"timeout_seconds"`
	MaxOutputTokens    int                         `json:"max_output_tokens,omitempty"`
	ScopeProfile       *doctype.ScopeProfileConfig `json:"scope_profile,omitempty"`
	IRFacets           []string                    `json:"ir_facets"`
	Slots              []doctype.SlotSpec          `json:"slots"`
	VerifyPolicy       *doctype.VerifyPolicy       `json:"verify_policy,omitempty"`
	TemplateMD         string                      `json:"template_md"`
	GenerationPromptMD string                      `json:"generation_prompt_md"`
	RefinePromptMD     string                      `json:"refine_prompt_md"`
}

type CloneDocTypeRequest struct {
	NewSlug     string `json:"new_slug"`
	DisplayName string `json:"display_name"`
}

type UpdateDocTypeRequest struct {
	Meta               doctype.Meta `json:"meta"`
	TemplateMD         string       `json:"template_md"`
	GenerationPromptMD string       `json:"generation_prompt_md"`
	RefinePromptMD     string       `json:"refine_prompt_md"`
}

type ToggleDocTypeStatusRequest struct {
	IsActive bool `json:"is_active"`
}

// DocTypeService 编排 DocType 目录 SSOT 幂等 Seed、目录 -> DB 同步、W2 在线 CRUD 与 Spec Drift 异步通知（F-03）。
type DocTypeService struct {
	registry    *doctype.Registry
	repo        DocTypeRepo
	docRepo     DocumentRepo
	driftRepo   DriftRepo
	slugOpLocks sync.Map // map[string]*sync.Mutex：串行化同 slug 的「原子写盘 + DB 同步」整段临界区
	driftWG     sync.WaitGroup
}

func NewDocTypeService(registry *doctype.Registry, repo DocTypeRepo) *DocTypeService {
	return &DocTypeService{
		registry: registry,
		repo:     repo,
	}
}

// WithSpecDriftNotifier 注入文档与漂移仓储，使 DocType 产生新修订后自动异步重算受影响已发布文档的 Spec Drift。
func (s *DocTypeService) WithSpecDriftNotifier(docRepo DocumentRepo, driftRepo DriftRepo) *DocTypeService {
	s.docRepo = docRepo
	s.driftRepo = driftRepo
	return s
}

// WaitSpecDriftJobs 等待全部异步 Spec Drift 计算钩子执行完毕（供单测与优雅停机使用）。
func (s *DocTypeService) WaitSpecDriftJobs() {
	s.driftWG.Wait()
}

func (s *DocTypeService) lockSlugOp(slug string) *sync.Mutex {
	mu, _ := s.slugOpLocks.LoadOrStore(slug, &sync.Mutex{})
	return mu.(*sync.Mutex)
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
	var driftedSlugs []string
	for _, b := range bundles {
		mu := s.lockSlugOp(b.Slug)
		mu.Lock()
		latestBundle, loadErr := s.registry.LoadOne(b.Slug)
		if loadErr != nil {
			mu.Unlock()
			return nil, fmt.Errorf("加载 DocType %s 失败: %w", b.Slug, loadErr)
		}
		createdRev, syncErr := s.syncBundle(ctx, latestBundle, operatorID)
		mu.Unlock()
		if syncErr != nil {
			return nil, fmt.Errorf("同步 DocType %s 失败: %w", b.Slug, syncErr)
		}
		res.SyncedCount++
		res.Slugs = append(res.Slugs, b.Slug)
		if createdRev {
			res.NewRevisions++
			driftedSlugs = append(driftedSlugs, b.Slug)
		}
	}

	for _, slug := range driftedSlugs {
		s.triggerSpecDriftAsync(slug)
	}
	return res, nil
}

// SyncOne 同步单个 doc_types/<slug>/ 目录至 DB。
func (s *DocTypeService) SyncOne(ctx context.Context, slug string, operatorID *uint) (bool, error) {
	mu := s.lockSlugOp(slug)
	mu.Lock()
	defer mu.Unlock()

	b, err := s.registry.LoadOne(slug)
	if err != nil {
		return false, err
	}
	return s.syncBundle(ctx, b, operatorID)
}

// ─── W2 管理员全生命周期写操作 (F-03 / SC-08) ───

// Create 管理员在线新建 DocType：先校验并原子落盘 4 个资产文件，再同步至 DB 生成 revision_no=1。
func (s *DocTypeService) Create(ctx context.Context, req CreateDocTypeRequest, operatorID uint) (*DocTypeDetailVO, *DocTypeAuditSnapshot, error) {
	slug := strings.TrimSpace(req.Slug)
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, nil, err
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	meta := doctype.Meta{
		Name:            slug,
		DisplayName:     strings.TrimSpace(req.DisplayName),
		Description:     strings.TrimSpace(req.Description),
		IsActive:        isActive,
		IsBuiltin:       false,
		ModelTier:       strings.TrimSpace(req.ModelTier),
		TimeoutSeconds:  req.TimeoutSeconds,
		MaxOutputTokens: req.MaxOutputTokens,
		ScopeProfile:    req.ScopeProfile,
		IRFacets:        req.IRFacets,
		Slots:           req.Slots,
		VerifyPolicy:    req.VerifyPolicy,
	}

	mu := s.lockSlugOp(slug)
	mu.Lock()
	defer mu.Unlock()

	// 先查 DB 与磁盘重名，拦截 409 CONFLICT
	if _, _, dbErr := s.repo.GetBySlug(ctx, slug); dbErr == nil {
		return nil, nil, fmt.Errorf("%w: %s", doctype.ErrDocTypeExists, slug)
	} else if !errors.Is(dbErr, models.ErrNotFound) {
		return nil, nil, dbErr
	}

	bundle, err := s.registry.CreateAsset(slug, meta, req.TemplateMD, req.GenerationPromptMD, req.RefinePromptMD)
	if err != nil {
		return nil, nil, err
	}

	op := operatorID
	if _, err := s.syncBundle(ctx, bundle, &op); err != nil {
		// 同步失败回滚新建目录，保持目录与 DB 一致
		if rbErr := s.registry.DeleteDirectory(slug); rbErr != nil {
			log.Printf("[Code-Archdoc] 回滚新建 DocType 目录 %s 失败: %v", slug, rbErr)
		}
		return nil, nil, fmt.Errorf("同步新建 DocType 到数据库失败: %w", err)
	}

	detail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	return detail, buildDocTypeAuditSnapshot(detail), nil
}

// Clone 管理员克隆既有 DocType 到新 slug，保留全部资产与未知扩展字段并置 is_builtin=false。
func (s *DocTypeService) Clone(ctx context.Context, srcSlug string, req CloneDocTypeRequest, operatorID uint) (*DocTypeDetailVO, *DocTypeAuditSnapshot, error) {
	if err := doctype.ValidateSlug(srcSlug); err != nil {
		return nil, nil, err
	}
	newSlug := strings.TrimSpace(req.NewSlug)
	if err := doctype.ValidateSlug(newSlug); err != nil {
		return nil, nil, err
	}
	if srcSlug == newSlug {
		return nil, nil, fmt.Errorf("%w: %s", doctype.ErrDocTypeExists, newSlug)
	}

	// 按字典序锁双 slug 防止并发互锁
	first, second := srcSlug, newSlug
	if first > second {
		first, second = second, first
	}
	mu1 := s.lockSlugOp(first)
	mu2 := s.lockSlugOp(second)
	mu1.Lock()
	defer mu1.Unlock()
	mu2.Lock()
	defer mu2.Unlock()

	if _, _, dbErr := s.repo.GetBySlug(ctx, newSlug); dbErr == nil {
		return nil, nil, fmt.Errorf("%w: %s", doctype.ErrDocTypeExists, newSlug)
	} else if !errors.Is(dbErr, models.ErrNotFound) {
		return nil, nil, dbErr
	}

	bundle, err := s.registry.CloneAsset(srcSlug, newSlug, req.DisplayName)
	if err != nil {
		return nil, nil, err
	}

	op := operatorID
	if _, err := s.syncBundle(ctx, bundle, &op); err != nil {
		if rbErr := s.registry.DeleteDirectory(newSlug); rbErr != nil {
			log.Printf("[Code-Archdoc] 回滚克隆 DocType 目录 %s 失败: %v", newSlug, rbErr)
		}
		return nil, nil, fmt.Errorf("同步克隆 DocType 到数据库失败: %w", err)
	}

	detail, err := s.GetDetail(ctx, newSlug)
	if err != nil {
		return nil, nil, err
	}
	return detail, buildDocTypeAuditSnapshot(detail), nil
}

// Update 管理员在线更新 DocType：持同 slug 锁串行执行「原子写盘(无损合并 ExtraFields) -> 重同步 DB -> 异步触发 Spec Drift」。
func (s *DocTypeService) Update(ctx context.Context, slug string, req UpdateDocTypeRequest, operatorID uint) (*DocTypeUpdateVO, *DocTypeAuditSnapshot, *DocTypeAuditSnapshot, error) {
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, nil, nil, err
	}
	if req.Meta.Name == "" {
		req.Meta.Name = slug
	}
	if req.Meta.Name != slug {
		return nil, nil, nil, fmt.Errorf("%w: meta.name (%q) 必须与路径 slug (%q) 一致", doctype.ErrInvalidMeta, req.Meta.Name, slug)
	}

	mu := s.lockSlugOp(slug)
	mu.Lock()
	defer mu.Unlock()

	beforeDetail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	beforeAudit := buildDocTypeAuditSnapshot(beforeDetail)

	bundle, err := s.registry.UpdateAsset(slug, req.Meta, req.TemplateMD, req.GenerationPromptMD, req.RefinePromptMD)
	if err != nil {
		return nil, nil, nil, err
	}

	op := operatorID
	createdRev, err := s.syncBundle(ctx, bundle, &op)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("同步更新 DocType 到数据库失败: %w", err)
	}

	afterDetail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	afterAudit := buildDocTypeAuditSnapshot(afterDetail)

	if createdRev {
		s.triggerSpecDriftAsync(slug)
	}

	return &DocTypeUpdateVO{
		DocTypeDetailVO: *afterDetail,
		RevisionCreated: createdRev,
	}, beforeAudit, afterAudit, nil
}

// UpdateStatus 管理员启用或停用 DocType：原子更新 meta.json 中的 is_active 并同步至 DB。
func (s *DocTypeService) UpdateStatus(ctx context.Context, slug string, isActive bool, operatorID uint) (*DocTypeDetailVO, *DocTypeAuditSnapshot, *DocTypeAuditSnapshot, error) {
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, nil, nil, err
	}

	mu := s.lockSlugOp(slug)
	mu.Lock()
	defer mu.Unlock()

	beforeDetail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	beforeAudit := buildDocTypeAuditSnapshot(beforeDetail)

	bundle, err := s.registry.UpdateStatus(slug, isActive)
	if err != nil {
		return nil, nil, nil, err
	}

	op := operatorID
	if _, err := s.syncBundle(ctx, bundle, &op); err != nil {
		return nil, nil, nil, fmt.Errorf("同步 DocType 启停状态到数据库失败: %w", err)
	}

	afterDetail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	return afterDetail, beforeAudit, buildDocTypeAuditSnapshot(afterDetail), nil
}

// Delete 物理删除未被已发布文档引用的 DocType；若存在 approved 引用直接拦截返回 DocTypeInUseError（409），绝不触碰磁盘。
func (s *DocTypeService) Delete(ctx context.Context, slug string) (*DocTypeAuditSnapshot, error) {
	if err := doctype.ValidateSlug(slug); err != nil {
		return nil, err
	}

	mu := s.lockSlugOp(slug)
	mu.Lock()
	defer mu.Unlock()

	beforeDetail, err := s.GetDetail(ctx, slug)
	if err != nil {
		return nil, err
	}
	beforeAudit := buildDocTypeAuditSnapshot(beforeDetail)

	pubCount, err := s.repo.CountApprovedDocumentsBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if pubCount > 0 {
		return nil, &DocTypeInUseError{Slug: slug, PublishedDocCount: pubCount}
	}

	// 在 DB 事务内持锁二次确认并删除，彻底消除竞态
	if err := s.repo.DeleteIfUnused(ctx, slug); err != nil {
		if errors.Is(err, models.ErrDocTypeInUse) {
			cnt, _ := s.repo.CountApprovedDocumentsBySlug(ctx, slug)
			return nil, &DocTypeInUseError{Slug: slug, PublishedDocCount: cnt}
		}
		return nil, err
	}

	if err := s.registry.DeleteDirectory(slug); err != nil {
		return nil, fmt.Errorf("数据库已移除但清理磁盘目录 %s 失败: %w", slug, err)
	}
	return beforeAudit, nil
}

// triggerSpecDriftAsync 异步触发受影响已发布文档的 Spec Drift 计算，不阻塞管理员写接口返回。
func (s *DocTypeService) triggerSpecDriftAsync(slug string) {
	if s.docRepo == nil || s.driftRepo == nil {
		return
	}
	s.driftWG.Add(1)
	go func(targetSlug string) {
		defer s.driftWG.Done()
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.RecomputeSpecDriftForSlug(bgCtx, targetSlug); err != nil {
			log.Printf("[Code-Archdoc] 异步重算 DocType(%s) Spec Drift 失败: %v", targetSlug, err)
		}
	}(slug)
}

// RecomputeSpecDriftForSlug 对绑定该 doc_type_slug 的全部已发布文档执行零网络开销的槽位级 Spec Drift 重算。
// 严格遵循数据不变量 2：以 PublishedRevision.DocTypeRevisionID 作为基准版本。
func (s *DocTypeService) RecomputeSpecDriftForSlug(ctx context.Context, slug string) error {
	if s.docRepo == nil || s.driftRepo == nil {
		return nil
	}
	dt, latestRev, err := s.repo.GetBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil
		}
		return err
	}
	if latestRev == nil || dt.LatestRevisionID == nil {
		return nil
	}

	approvedDocs, err := s.docRepo.ListApprovedDocuments(ctx)
	if err != nil {
		return err
	}

	var matched []models.Document
	var pubRevIDs []uint
	for _, d := range approvedDocs {
		if d.DocTypeSlug == slug && d.PublishedRevisionID != nil {
			matched = append(matched, d)
			pubRevIDs = append(pubRevIDs, *d.PublishedRevisionID)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	pubRevMap, err := s.docRepo.ListRevisionsByIDs(ctx, pubRevIDs)
	if err != nil {
		return err
	}

	latestSlots := unmarshalSpecSlotMetas(latestRev.SlotsSnapshotJSON)
	now := time.Now()
	latestRevID := latestRev.ID

	for _, d := range matched {
		pubRev, ok := pubRevMap[*d.PublishedRevisionID]
		if !ok {
			continue
		}
		boundRevID := pubRev.DocTypeRevisionID
		if boundRevID == latestRevID {
			emptySlots, marshalErr := json.Marshal([]models.AffectedSlotDrift{})
			if marshalErr != nil {
				return marshalErr
			}
			if upsertErr := s.driftRepo.UpsertDocumentDrift(ctx, &models.DocumentDrift{
				DocumentID:              d.ID,
				DriftType:               models.DriftTypeSpec,
				HasDrift:                false,
				Precision:               models.DriftPrecisionSectionLevel,
				BoundDocTypeRevisionID:  &boundRevID,
				LatestDocTypeRevisionID: &latestRevID,
				AffectedSlotCount:       0,
				AffectedSlotsJSON:       datatypes.JSON(emptySlots),
				DetectedAt:              now,
			}); upsertErr != nil {
				return upsertErr
			}
			continue
		}

		boundRev, rErr := s.repo.GetRevisionByID(ctx, boundRevID)
		if rErr != nil {
			continue
		}
		boundSlots := unmarshalSpecSlotMetas(boundRev.SlotsSnapshotJSON)
		items := drift.ComputeSpecDrift(
			boundSlots,
			latestSlots,
			boundRev.PromptHash,
			latestRev.PromptHash,
			boundRev.TemplateMD,
			latestRev.TemplateMD,
		)

		affected := make([]models.AffectedSlotDrift, 0, len(items))
		for _, it := range items {
			affected = append(affected, models.AffectedSlotDrift{
				SlotID:     it.SlotID,
				SlotTitle:  it.SlotTitle,
				Ownership:  it.Ownership,
				Reason:     it.Reason,
				ChangeKind: it.ChangeKind,
			})
		}
		affectedJSON, _ := json.Marshal(affected)
		if err := s.driftRepo.UpsertDocumentDrift(ctx, &models.DocumentDrift{
			DocumentID:              d.ID,
			DriftType:               models.DriftTypeSpec,
			HasDrift:                len(affected) > 0,
			Precision:               models.DriftPrecisionSectionLevel,
			BoundDocTypeRevisionID:  &boundRevID,
			LatestDocTypeRevisionID: &latestRevID,
			AffectedSlotCount:       len(affected),
			AffectedSlotsJSON:       datatypes.JSON(affectedJSON),
			DetectedAt:              now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalSpecSlotMetas(raw datatypes.JSON) []drift.SpecSlotMeta {
	var defs []models.SlotDefinition
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil
	}
	out := make([]drift.SpecSlotMeta, 0, len(defs))
	for _, sl := range defs {
		out = append(out, drift.SpecSlotMeta{
			SlotID:          sl.SlotID,
			Title:           sl.Title,
			Ownership:       sl.Ownership,
			Required:        sl.Required,
			IRFacets:        sl.IRFacets,
			InstructionHint: sl.InstructionHint,
		})
	}
	return out
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

func buildDocTypeAuditSnapshot(d *DocTypeDetailVO) *DocTypeAuditSnapshot {
	if d == nil {
		return nil
	}
	slotIDs := make([]string, 0, len(d.Slots))
	for _, sl := range d.Slots {
		slotIDs = append(slotIDs, sl.SlotID)
	}
	return &DocTypeAuditSnapshot{
		Slug:                d.Slug,
		DisplayName:         d.DisplayName,
		Description:         d.Description,
		IsActive:            d.IsActive,
		IsBuiltin:           d.IsBuiltin,
		ModelTier:           d.ModelTier,
		TimeoutSeconds:      d.TimeoutSeconds,
		LatestRevisionID:    d.LatestRevisionID,
		LatestRevisionNo:    d.LatestRevisionNo,
		LatestAggregateHash: d.LatestAggregateHash,
		SlotIDs:             slotIDs,
	}
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
