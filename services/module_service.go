package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/sandbox"

	"gorm.io/datatypes"
)

var (
	// ErrInvalidModuleParam 模块策略/状态/迁移参数非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidModuleParam = errors.New("模块治理参数非法")
	// ErrActiveTasksBlockingMigration 源或目标子目录存在活跃生成任务，阻断迁移（HTTP 409 CONFLICT）。
	ErrActiveTasksBlockingMigration = errors.New("源或目标子目录存在进行中的生成任务，请等待完成后再迁移")
)

// RepoModulePolicyVO 对应 GET/PUT /api/repos/:repo_id/module-policy 响应。
type RepoModulePolicyVO struct {
	RepoID            uint       `json:"repo_id"`
	Strategy          string     `json:"strategy"`
	ExcludeGlobs      []string   `json:"exclude_globs"`
	MinEffectiveFiles int        `json:"min_effective_files"`
	UpdatedBy         *uint      `json:"updated_by,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

// UpdateModulePolicyRequest 对应 PUT /api/repos/:repo_id/module-policy 请求体。
type UpdateModulePolicyRequest struct {
	Strategy          string   `json:"strategy"`
	ExcludeGlobs      []string `json:"exclude_globs"`
	MinEffectiveFiles int      `json:"min_effective_files"`
}

// RepoModuleVO 对应模块索引视图（负责人姓名与中文名从 architecture_elements / repositories 实时聚合）。
type RepoModuleVO struct {
	ID              uint      `json:"id"`
	RepoID          uint      `json:"repo_id"`
	Subdirectory    string    `json:"subdirectory"`
	Source          string    `json:"source"`
	Status          string    `json:"status"`
	ArchElementID   *uint     `json:"arch_element_id"`
	ArchElementName string    `json:"arch_element_name"`
	OwnerID         uint      `json:"owner_id"`
	OwnerName       string    `json:"owner_name"`
	EffectiveFiles  int       `json:"effective_files"`
	TotalLOC        int       `json:"total_loc"`
	LastSeenCommit  string    `json:"last_seen_commit"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

// RepoModulesListVO 对应 GET /api/repos/:repo_id/modules 响应。
type RepoModulesListVO struct {
	RepoID  uint               `json:"repo_id"`
	Policy  RepoModulePolicyVO `json:"policy"`
	Modules []RepoModuleVO     `json:"modules"`
}

// RefreshModulesResultVO 对应 POST /api/repos/:repo_id/modules/refresh 响应。
type RefreshModulesResultVO struct {
	RepoID      uint           `json:"repo_id"`
	CommitSHA   string         `json:"commit_sha"`
	ActiveCount int            `json:"active_count"`
	Modules     []RepoModuleVO `json:"modules"`
}

// MigrateModuleRequest 对应 POST /api/repos/:repo_id/modules/migrate 请求体。
type MigrateModuleRequest struct {
	FromSubdir string `json:"from_subdir"`
	ToSubdir   string `json:"to_subdir"`
}

// MigrateModuleResultVO 对应 POST /api/repos/:repo_id/modules/migrate 响应。
type MigrateModuleResultVO struct {
	RepoID             uint   `json:"repo_id"`
	FromSubdir         string `json:"from_subdir"`
	ToSubdir           string `json:"to_subdir"`
	MigratedDocuments  int    `json:"migrated_documents"`
	RepublishedPublish int    `json:"republished_documents"`
}

// ModuleService 编排仓库模块发现策略、派生模块索引刷新、忽略/恢复切换与子目录重命名文档迁移（ADR-04 D-1）。
type ModuleService struct {
	masterRepo  SharedMasterRepo
	moduleRepo  ModuleRepo
	docRepo     DocumentRepo
	docTypeRepo DocTypeRepo
	taskRepo    TaskRepo
	driftRepo   DriftRepo
	snapSvc     *SnapshotService
	publishSvc  *PublishService
	driftSvc    *DriftOrchestrator
}

func NewModuleService(
	masterRepo SharedMasterRepo,
	moduleRepo ModuleRepo,
	docRepo DocumentRepo,
	docTypeRepo DocTypeRepo,
	taskRepo TaskRepo,
	driftRepo DriftRepo,
	snapSvc *SnapshotService,
	publishSvc *PublishService,
	driftSvc *DriftOrchestrator,
) *ModuleService {
	return &ModuleService{
		masterRepo:  masterRepo,
		moduleRepo:  moduleRepo,
		docRepo:     docRepo,
		docTypeRepo: docTypeRepo,
		taskRepo:    taskRepo,
		driftRepo:   driftRepo,
		snapSvc:     snapSvc,
		publishSvc:  publishSvc,
		driftSvc:    driftSvc,
	}
}

// GetPolicy 返回仓库级模块发现策略（若未显式配置则返回默认 merged / MinEffectiveFiles=3）。
func (s *ModuleService) GetPolicy(ctx context.Context, repoID uint) (*RepoModulePolicyVO, error) {
	if _, err := s.masterRepo.GetRepository(ctx, repoID); err != nil {
		return nil, err
	}
	return s.loadEffectivePolicyVO(ctx, repoID)
}

func (s *ModuleService) loadEffectivePolicyVO(ctx context.Context, repoID uint) (*RepoModulePolicyVO, error) {
	p, err := s.moduleRepo.GetPolicy(ctx, repoID)
	if errors.Is(err, models.ErrNotFound) {
		return &RepoModulePolicyVO{
			RepoID:            repoID,
			Strategy:          models.ModuleStrategyMerged,
			ExcludeGlobs:      []string{},
			MinEffectiveFiles: 3,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var globs []string
	_ = json.Unmarshal(p.ExcludeGlobs, &globs)
	if globs == nil {
		globs = []string{}
	}
	updatedAt := p.UpdatedAt
	return &RepoModulePolicyVO{
		RepoID:            p.RepoID,
		Strategy:          p.Strategy,
		ExcludeGlobs:      globs,
		MinEffectiveFiles: p.MinEffectiveFiles,
		UpdatedBy:         p.UpdatedBy,
		UpdatedAt:         &updatedAt,
	}, nil
}

// UpdatePolicy 校验并持久化仓库级模块发现策略。
func (s *ModuleService) UpdatePolicy(ctx context.Context, repoID uint, req UpdateModulePolicyRequest, operatorID uint) (*RepoModulePolicyVO, error) {
	if _, err := s.masterRepo.GetRepository(ctx, repoID); err != nil {
		return nil, err
	}
	strategy := strings.TrimSpace(req.Strategy)
	if strategy == "" {
		strategy = models.ModuleStrategyMerged
	}
	if !models.IsValidModuleStrategy(strategy) {
		return nil, fmt.Errorf("%w: strategy 仅支持 arch_elements | top_level_dirs | merged", ErrInvalidModuleParam)
	}
	minFiles := req.MinEffectiveFiles
	if minFiles <= 0 {
		minFiles = 3
	}
	if minFiles > 10000 {
		return nil, fmt.Errorf("%w: min_effective_files 超出合理范围", ErrInvalidModuleParam)
	}

	cleanedGlobs := make([]string, 0, len(req.ExcludeGlobs))
	seen := make(map[string]bool, len(req.ExcludeGlobs))
	for _, g := range req.ExcludeGlobs {
		t := strings.TrimSpace(g)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		cleanedGlobs = append(cleanedGlobs, t)
	}
	globsJSON, err := json.Marshal(cleanedGlobs)
	if err != nil {
		return nil, err
	}

	var updatedBy *uint
	if operatorID > 0 {
		uid := operatorID
		updatedBy = &uid
	}
	row := &models.RepoModulePolicy{
		RepoID:            repoID,
		Strategy:          strategy,
		ExcludeGlobs:      datatypes.JSON(globsJSON),
		MinEffectiveFiles: minFiles,
		UpdatedBy:         updatedBy,
	}
	if err := s.moduleRepo.UpsertPolicy(ctx, row); err != nil {
		return nil, err
	}
	return s.loadEffectivePolicyVO(ctx, repoID)
}

// ListModules 返回仓库当前模块发现策略及派生模块索引列表（实时关联 architecture_elements 负责人与中文名）。
func (s *ModuleService) ListModules(ctx context.Context, repoID uint) (*RepoModulesListVO, error) {
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	policyVO, err := s.loadEffectivePolicyVO(ctx, repoID)
	if err != nil {
		return nil, err
	}
	mods, err := s.moduleRepo.ListModulesByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	modVOs, err := s.enrichModules(ctx, repo, mods)
	if err != nil {
		return nil, err
	}
	return &RepoModulesListVO{
		RepoID:  repoID,
		Policy:  *policyVO,
		Modules: modVOs,
	}, nil
}

// RefreshModules 同步全仓基础快照并基于策略重新发现、持久化仓库模块索引，并联动通知整仓文档漂移评估。
func (s *ModuleService) RefreshModules(ctx context.Context, repoID uint) (*RefreshModulesResultVO, error) {
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	if s.snapSvc == nil {
		return nil, fmt.Errorf("快照服务未初始化")
	}

	snapReady, err := s.snapSvc.EnsureSnapshot(ctx, repo, "", "")
	if err != nil {
		return nil, err
	}
	if err := s.SyncModulesFromTree(ctx, repoID, snapReady.Snapshot.CommitSHA, &snapReady.IR.Tree); err != nil {
		return nil, err
	}

	mods, err := s.moduleRepo.ListModulesByRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	modVOs, err := s.enrichModules(ctx, repo, mods)
	if err != nil {
		return nil, err
	}
	activeCount := 0
	for _, m := range modVOs {
		if m.Status == models.ModuleStatusActive {
			activeCount++
		}
	}
	return &RefreshModulesResultVO{
		RepoID:      repoID,
		CommitSHA:   snapReady.Snapshot.CommitSHA,
		ActiveCount: activeCount,
		Modules:     modVOs,
	}, nil
}

// SyncModulesFromTree 基于已提取的全仓 TreeFacet 执行确定性模块发现并回写 archdoc_repo_modules。
func (s *ModuleService) SyncModulesFromTree(ctx context.Context, repoID uint, commitSHA string, tree *codeir.TreeFacet) error {
	if s == nil || s.moduleRepo == nil || tree == nil {
		return nil
	}
	policyVO, err := s.loadEffectivePolicyVO(ctx, repoID)
	if err != nil {
		return err
	}
	elems, err := s.masterRepo.ListArchElementsByRepo(ctx, repoID)
	if err != nil {
		return err
	}

	archScopes := make([]codeir.ArchElementScope, 0, len(elems))
	for _, el := range elems {
		archScopes = append(archScopes, codeir.ArchElementScope{
			ID:           el.ID,
			Subdirectory: el.Subdirectory,
		})
	}
	discovered := codeir.DiscoverModules(tree, codeir.ModuleDiscoveryPolicy{
		Strategy:          policyVO.Strategy,
		ExcludeGlobs:      policyVO.ExcludeGlobs,
		MinEffectiveFiles: policyVO.MinEffectiveFiles,
	}, archScopes)

	now := time.Now()
	rows := make([]models.RepoModule, 0, len(discovered))
	for _, d := range discovered {
		rows = append(rows, models.RepoModule{
			RepoID:         repoID,
			Subdirectory:   d.Subdirectory,
			Source:         d.Source,
			ArchElementID:  d.ArchElementID,
			Status:         models.ModuleStatusActive,
			EffectiveFiles: d.EffectiveFiles,
			TotalLOC:       d.TotalLOC,
			LastSeenCommit: commitSHA,
			LastSeenAt:     now,
		})
	}
	if err := s.moduleRepo.UpsertModules(ctx, repoID, rows, now); err != nil {
		return err
	}
	if s.driftSvc != nil {
		s.driftSvc.OnRepoModulesChanged(ctx, repoID)
	}
	return nil
}

// UpdateModuleStatus 切换单个模块状态（仅允许 active | ignored），状态变化后通知整仓文档漂移评估。
func (s *ModuleService) UpdateModuleStatus(ctx context.Context, repoID, moduleID uint, status string) (*RepoModuleVO, error) {
	status = strings.TrimSpace(status)
	if status != models.ModuleStatusActive && status != models.ModuleStatusIgnored {
		return nil, fmt.Errorf("%w: status 仅支持 active 或 ignored", ErrInvalidModuleParam)
	}
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	updated, err := s.moduleRepo.UpdateModuleStatus(ctx, repoID, moduleID, status)
	if err != nil {
		return nil, err
	}
	if s.driftSvc != nil {
		s.driftSvc.OnRepoModulesChanged(ctx, repoID)
	}
	vos, err := s.enrichModules(ctx, repo, []models.RepoModule{*updated})
	if err != nil || len(vos) == 0 {
		return nil, err
	}
	return &vos[0], nil
}

// MigrateModule 将源子目录下的全部文档、草稿与修订迁移至目标子目录，对已发布文档重渲染并清理旧发布文件（ADR-04 D-1）。
func (s *ModuleService) MigrateModule(ctx context.Context, repoID uint, req MigrateModuleRequest) (*MigrateModuleResultVO, error) {
	fromSub, err := sandbox.NormalizeSubdir(req.FromSubdir)
	if err != nil {
		return nil, err
	}
	toSub, err := sandbox.NormalizeSubdir(req.ToSubdir)
	if err != nil {
		return nil, err
	}
	if fromSub == "" || toSub == "" {
		return nil, fmt.Errorf("%w: from_subdir 与 to_subdir 均必须为非空子目录路径", ErrInvalidModuleParam)
	}
	if fromSub == toSub {
		return nil, fmt.Errorf("%w: from_subdir 与 to_subdir 不能相同", ErrInvalidModuleParam)
	}

	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	// 阻断存在活跃任务的源或目标范围
	if s.taskRepo != nil {
		hasActive, tErr := s.taskRepo.HasActiveTasksInSubdirs(ctx, repoID, []string{fromSub, toSub})
		if tErr != nil {
			return nil, tErr
		}
		if hasActive {
			return nil, ErrActiveTasksBlockingMigration
		}
	}

	// 预加载 DocType 展示名以便同步重写 Document.Title
	dtNameBySlug := make(map[string]string)
	if s.docTypeRepo != nil {
		if dts, dtErr := s.docTypeRepo.List(ctx, false); dtErr == nil {
			for _, dt := range dts {
				dtNameBySlug[dt.Slug] = dt.DisplayName
			}
		}
	}

	migratedBefore, err := s.docRepo.MigrateScopeDocuments(ctx, repoID, fromSub, toSub, func(docTypeSlug, _ string) string {
		displayName := dtNameBySlug[docTypeSlug]
		if displayName == "" {
			displayName = docTypeSlug
		}
		return buildDocumentTitle(repo.Name, toSub, displayName)
	})
	if err != nil {
		return nil, err
	}

	republishedCount := 0
	for _, before := range migratedBefore {
		// 对原已发布 (approved) 文档按新子目录重渲染 Frontmatter/深链、写入新路径、删除旧文件并重建 README.md
		if before.Status == models.DocStatusApproved && s.publishSvc != nil {
			if repErr := s.publishSvc.RepublishAfterMigrate(ctx, before.ID, before.PublishedRelPath); repErr != nil {
				return nil, fmt.Errorf("迁移文档 #%d 后重新发布失败: %w", before.ID, repErr)
			}
			republishedCount++
		}
		// 清理旧路径残留漂移记录
		if s.driftRepo != nil {
			if cErr := s.driftRepo.ClearDocumentDrifts(ctx, before.ID); cErr != nil {
				log.Printf("[%s] 迁移后清理文档 #%d 漂移记录失败: %v", config.ServiceName, before.ID, cErr)
			}
		}
	}

	// 同步更新派生模块索引：源目录标为 missing，目标目录注册/更新为 active
	_ = s.moduleRepo.MarkSubdirMissing(ctx, repoID, fromSub)
	_ = s.moduleRepo.UpsertSingleModule(ctx, &models.RepoModule{
		RepoID:       repoID,
		Subdirectory: toSub,
		Source:       models.ModuleSourceDiscovered,
		Status:       models.ModuleStatusActive,
		LastSeenAt:   time.Now(),
	})

	if s.driftSvc != nil {
		s.driftSvc.OnRepoModulesChanged(ctx, repoID)
	}

	return &MigrateModuleResultVO{
		RepoID:             repoID,
		FromSubdir:         fromSub,
		ToSubdir:           toSub,
		MigratedDocuments:  len(migratedBefore),
		RepublishedPublish: republishedCount,
	}, nil
}

func (s *ModuleService) enrichModules(ctx context.Context, repo *models.Repository, mods []models.RepoModule) ([]RepoModuleVO, error) {
	elems, err := s.masterRepo.ListArchElementsByRepo(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	elemBySub := make(map[string]models.ArchitectureElement, len(elems))
	ownerIDSet := make(map[uint]struct{})
	for _, el := range elems {
		if el.OwnerID != nil && *el.OwnerID > 0 {
			ownerIDSet[*el.OwnerID] = struct{}{}
		}
		if ns, nErr := sandbox.NormalizeSubdir(el.Subdirectory); nErr == nil && ns != "" {
			if _, exists := elemBySub[ns]; !exists {
				elemBySub[ns] = el
			}
		}
	}
	ownerIDs := make([]uint, 0, len(ownerIDSet))
	for id := range ownerIDSet {
		ownerIDs = append(ownerIDs, id)
	}
	elemUsers, err := s.masterRepo.GetUsersByIDs(ctx, ownerIDs)
	if err != nil {
		return nil, err
	}

	repoOwnerName := repo.Owner.Name
	if repoOwnerName == "" {
		repoOwnerName = repo.Owner.Username
	}

	out := make([]RepoModuleVO, 0, len(mods))
	for _, m := range mods {
		ownerID := repo.OwnerID
		ownerName := repoOwnerName
		archElemID := m.ArchElementID
		archElemName := ""

		if el, ok := elemBySub[m.Subdirectory]; ok {
			elID := el.ID
			archElemID = &elID
			archElemName = el.NameCn
			if archElemName == "" {
				archElemName = el.NameEn
			}
			if el.OwnerID != nil && *el.OwnerID > 0 {
				ownerID = *el.OwnerID
				if u, uOK := elemUsers[*el.OwnerID]; uOK {
					if u.Name != "" {
						ownerName = u.Name
					} else {
						ownerName = u.Username
					}
				}
			}
		}

		out = append(out, RepoModuleVO{
			ID:              m.ID,
			RepoID:          m.RepoID,
			Subdirectory:    m.Subdirectory,
			Source:          m.Source,
			Status:          m.Status,
			ArchElementID:   archElemID,
			ArchElementName: archElemName,
			OwnerID:         ownerID,
			OwnerName:       ownerName,
			EffectiveFiles:  m.EffectiveFiles,
			TotalLOC:        m.TotalLOC,
			LastSeenCommit:  m.LastSeenCommit,
			LastSeenAt:      m.LastSeenAt,
		})
	}
	return out, nil
}
