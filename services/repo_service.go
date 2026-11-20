package services

import (
	"context"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/sandbox"
	commonModels "code-common/backend/models"
)

// ─── API 响应视图对象 (VO) ───

type DepartmentVO struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	LeaderID *uint  `json:"leader_id"`
}

type DocStatusSummaryVO struct {
	DocumentID          uint      `json:"document_id"`
	Status              string    `json:"status"`
	CurrentRevisionNo   int       `json:"current_revision_no"`
	PublishedRevisionNo int       `json:"published_revision_no"`
	HasCodeDrift        bool      `json:"has_code_drift"`
	HasSpecDrift        bool      `json:"has_spec_drift"`
	AffectedSlotCount   int       `json:"affected_slot_count"`
	VerifierPassed      bool      `json:"verifier_passed"`
	CanApprove          bool      `json:"can_approve"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type RepoScopeCoverageVO struct {
	RepoID            uint                          `json:"repo_id"`
	RepoName          string                        `json:"repo_name"`
	DepartmentID      uint                          `json:"department_id"`
	DepartmentName    string                        `json:"department_name"`
	Subdirectory      string                        `json:"subdirectory"`
	ScopeType         string                        `json:"scope_type"`   // "repo" | "subdir"
	ScopeStatus       string                        `json:"scope_status"` // "active" | "missing" | "orphan"
	ModuleSource      string                        `json:"module_source,omitempty"`
	EffectiveFiles    int                           `json:"effective_files"`
	TotalLOC          int                           `json:"total_loc"`
	ActiveModuleCount int                           `json:"active_module_count"`
	ModulesScanned    bool                          `json:"modules_scanned"`
	ArchElementID     *uint                         `json:"arch_element_id"`
	ArchElementName   string                        `json:"arch_element_name"`
	Branch            string                        `json:"branch"`
	OwnerID           uint                          `json:"owner_id"`
	OwnerName         string                        `json:"owner_name"`
	CanWrite          bool                          `json:"can_write"`
	LatestCommitSHA   string                        `json:"latest_commit_sha"`
	RemoteHeadSHA     string                        `json:"remote_head_sha"`
	ActiveTaskID      *uint                         `json:"active_task_id"`
	DocStatuses       map[string]DocStatusSummaryVO `json:"doc_statuses"`
}

type RepoCoverageSummaryVO struct {
	TotalScopes   int `json:"total_scopes"`
	CoveredScopes int `json:"covered_scopes"`
	ApprovedDocs  int `json:"approved_docs"`
	DriftedDocs   int `json:"drifted_docs"`
}

type RepoCoverageListResponse struct {
	Items    []RepoScopeCoverageVO `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
	Summary  RepoCoverageSummaryVO `json:"summary"`
}

type GenTaskSummaryVO struct {
	ID              uint       `json:"id"`
	RepoID          uint       `json:"repo_id"`
	Subdirectory    string     `json:"subdirectory"`
	RefName         string     `json:"ref_name"`
	TaskKind        string     `json:"task_kind"`
	Priority        string     `json:"priority"`
	Status          string     `json:"status"`
	ProgressPercent int        `json:"progress_percent"`
	TriggeredBy     uint       `json:"triggered_by"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type RepoScopesDetailResponse struct {
	RepoID         uint                  `json:"repo_id"`
	RepoName       string                `json:"repo_name"`
	DepartmentID   uint                  `json:"department_id"`
	DepartmentName string                `json:"department_name"`
	Branch         string                `json:"branch"`
	OwnerID        uint                  `json:"owner_id"`
	OwnerName      string                `json:"owner_name"`
	Scopes         []RepoScopeCoverageVO `json:"scopes"`
	RecentTasks    []GenTaskSummaryVO    `json:"recent_tasks"`
}

type RepoCoverageFilter struct {
	Page         int
	PageSize     int
	Search       string
	DepartmentID *uint
	DocType      string
	Status       string // all | approved | draft | drifted | missing
	ScopeType    string // all | repo | subdir
}

// RepoService 提供共享部门列表、仓库/子目录覆盖全景矩阵与单仓详情查询编排（F-01）。
type RepoService struct {
	masterRepo SharedMasterRepo
	docRepo    DocumentRepo
	snapRepo   SnapshotRepo
	taskRepo   TaskRepo
	driftRepo  DriftRepo
	moduleRepo ModuleRepo
	rbacSvc    *RBACService
}

// WithModuleRepo 注入派生模块索引仓储，使覆盖全景矩阵融合自动发现模块、已消失模块与孤儿文档范围（ADR-04 D-1）。
func (s *RepoService) WithModuleRepo(moduleRepo ModuleRepo) *RepoService {
	s.moduleRepo = moduleRepo
	return s
}

func NewRepoService(
	masterRepo SharedMasterRepo,
	docRepo DocumentRepo,
	snapRepo SnapshotRepo,
	taskRepo TaskRepo,
	driftRepo DriftRepo,
	rbacSvc *RBACService,
) *RepoService {
	return &RepoService{
		masterRepo: masterRepo,
		docRepo:    docRepo,
		snapRepo:   snapRepo,
		taskRepo:   taskRepo,
		driftRepo:  driftRepo,
		rbacSvc:    rbacSvc,
	}
}

// NormalizePagination 规范化标准分页参数（默认 25 条/页，白名单 15|25|50|100）。
func NormalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	switch pageSize {
	case 15, 25, 50, 100:
	default:
		pageSize = 25
	}
	return page, pageSize
}

func (s *RepoService) ListDepartments(ctx context.Context) ([]DepartmentVO, error) {
	depts, err := s.masterRepo.ListDepartments(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]DepartmentVO, 0, len(depts))
	for _, d := range depts {
		items = append(items, DepartmentVO{
			ID:       d.ID,
			Name:     d.Name,
			LeaderID: d.LeaderID,
		})
	}
	return items, nil
}

func (s *RepoService) ListRepoCoverage(ctx context.Context, user *commonModels.User, filter RepoCoverageFilter) (*RepoCoverageListResponse, error) {
	page, pageSize := NormalizePagination(filter.Page, filter.PageSize)

	repos, err := s.masterRepo.ListAllActiveRepositories(ctx, filter.DepartmentID)
	if err != nil {
		return nil, err
	}

	allRows, err := s.buildCoverageRows(ctx, user, repos)
	if err != nil {
		return nil, err
	}

	// 应用筛选条件（scope_type, search, status + doc_type）
	filtered := make([]RepoScopeCoverageVO, 0, len(allRows))
	searchLower := strings.ToLower(strings.TrimSpace(filter.Search))
	scopeTypeFilter := strings.ToLower(strings.TrimSpace(filter.ScopeType))
	statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
	docTypeFilter := strings.TrimSpace(filter.DocType)

	for _, row := range allRows {
		if scopeTypeFilter != "" && scopeTypeFilter != "all" && row.ScopeType != scopeTypeFilter {
			continue
		}
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(row.RepoName), searchLower) &&
				!strings.Contains(strings.ToLower(row.Subdirectory), searchLower) &&
				!strings.Contains(strings.ToLower(row.ArchElementName), searchLower) {
				continue
			}
		}
		if statusFilter != "" && statusFilter != "all" {
			if !matchesStatusFilter(row.DocStatuses, docTypeFilter, statusFilter) {
				continue
			}
		}
		filtered = append(filtered, row)
	}

	summary := computeCoverageSummary(filtered)
	total := int64(len(filtered))

	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}

	return &RepoCoverageListResponse{
		Items:    filtered[start:end],
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Summary:  summary,
	}, nil
}

func (s *RepoService) GetRepoScopes(ctx context.Context, user *commonModels.User, repoID uint) (*RepoScopesDetailResponse, error) {
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	rows, err := s.buildCoverageRows(ctx, user, []commonModels.Repository{*repo})
	if err != nil {
		return nil, err
	}

	tasks, _, err := s.taskRepo.ListTasks(ctx, models.TaskListFilter{
		RepoID: &repoID,
		Limit:  5,
	})
	if err != nil {
		return nil, err
	}

	recentTasks := make([]GenTaskSummaryVO, 0, len(tasks))
	for _, t := range tasks {
		recentTasks = append(recentTasks, GenTaskSummaryVO{
			ID:              t.ID,
			RepoID:          t.RepoID,
			Subdirectory:    t.Subdirectory,
			RefName:         t.RefName,
			TaskKind:        t.TaskKind,
			Priority:        t.Priority,
			Status:          t.Status,
			ProgressPercent: t.ProgressPercent,
			TriggeredBy:     t.TriggeredBy,
			StartedAt:       t.StartedAt,
			FinishedAt:      t.FinishedAt,
			CreatedAt:       t.CreatedAt,
		})
	}

	ownerName := repo.Owner.Name
	if ownerName == "" {
		ownerName = repo.Owner.Username
	}
	branch := strings.TrimSpace(repo.Branch)
	if branch == "" {
		branch = "master"
	}

	return &RepoScopesDetailResponse{
		RepoID:         repo.ID,
		RepoName:       repo.Name,
		DepartmentID:   repo.DepartmentID,
		DepartmentName: repo.Department.Name,
		Branch:         branch,
		OwnerID:        repo.OwnerID,
		OwnerName:      ownerName,
		Scopes:         rows,
		RecentTasks:    recentTasks,
	}, nil
}

type scopeKey struct {
	repoID uint
	subdir string
}

func (s *RepoService) buildCoverageRows(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) ([]RepoScopeCoverageVO, error) {
	if len(repos) == 0 {
		return []RepoScopeCoverageVO{}, nil
	}

	repoIDs := make([]uint, 0, len(repos))
	for _, r := range repos {
		repoIDs = append(repoIDs, r.ID)
	}

	elems, err := s.masterRepo.ListArchElementsByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}

	// 收集架构元素关联负责人 ID 批量加载姓名
	ownerIDSet := make(map[uint]struct{})
	for _, el := range elems {
		if el.OwnerID != nil && *el.OwnerID > 0 {
			ownerIDSet[*el.OwnerID] = struct{}{}
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

	docs, err := s.docRepo.ListByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}

	revIDs := make([]uint, 0, len(docs)*2)
	docIDs := make([]uint, 0, len(docs))
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
		if d.CurrentRevisionID != nil {
			revIDs = append(revIDs, *d.CurrentRevisionID)
		}
		if d.PublishedRevisionID != nil {
			revIDs = append(revIDs, *d.PublishedRevisionID)
		}
	}
	revMap, err := s.docRepo.ListRevisionsByIDs(ctx, revIDs)
	if err != nil {
		return nil, err
	}

	drifts, err := s.driftRepo.ListDriftsByDocIDs(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	codeDriftByDoc := make(map[uint]models.DocumentDrift)
	specDriftByDoc := make(map[uint]models.DocumentDrift)
	for _, dr := range drifts {
		if dr.DriftType == models.DriftTypeCode {
			codeDriftByDoc[dr.DocumentID] = dr
		} else if dr.DriftType == models.DriftTypeSpec {
			specDriftByDoc[dr.DocumentID] = dr
		}
	}

	snaps, err := s.snapRepo.ListLatestByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	latestCommitByScope := make(map[scopeKey]string)
	for _, sn := range snaps {
		k := scopeKey{repoID: sn.RepoID, subdir: sn.Subdirectory}
		if _, exists := latestCommitByScope[k]; !exists {
			latestCommitByScope[k] = sn.CommitSHA
		}
	}

	remoteHeads, err := s.driftRepo.ListRemoteHeadsByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	remoteHeadByRepo := make(map[uint]string)
	for _, rh := range remoteHeads {
		if rh.RemoteHeadSHA != "" {
			remoteHeadByRepo[rh.RepoID] = rh.RemoteHeadSHA
		}
	}

	activeTasks, err := s.taskRepo.ListActiveTasksByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	activeTaskByScope := make(map[scopeKey]uint)
	for _, tk := range activeTasks {
		k := scopeKey{repoID: tk.RepoID, subdir: tk.Subdirectory}
		if _, exists := activeTaskByScope[k]; !exists {
			activeTaskByScope[k] = tk.ID
		}
	}

	// 批量加载派生模块索引（可选：未注入或尚未刷新的仓库保持架构元素向后兼容）
	modulesByRepo := make(map[uint][]models.RepoModule)
	if s.moduleRepo != nil {
		if mods, mErr := s.moduleRepo.ListModulesByRepos(ctx, repoIDs); mErr == nil {
			for _, m := range mods {
				modulesByRepo[m.RepoID] = append(modulesByRepo[m.RepoID], m)
			}
		}
	}

	// 按仓库分组架构元素，并按规范化子目录聚合文档
	elemsByRepo := make(map[uint][]models.ArchitectureElement)
	for _, el := range elems {
		if el.RepoID != nil {
			elemsByRepo[*el.RepoID] = append(elemsByRepo[*el.RepoID], el)
		}
	}
	docsByScope := make(map[scopeKey][]models.Document)
	docSubdirsByRepo := make(map[uint][]string)
	seenDocSubdir := make(map[scopeKey]bool)
	for _, d := range docs {
		k := scopeKey{repoID: d.RepoID, subdir: d.Subdirectory}
		docsByScope[k] = append(docsByScope[k], d)
		if d.Subdirectory != "" && !seenDocSubdir[k] {
			seenDocSubdir[k] = true
			docSubdirsByRepo[d.RepoID] = append(docSubdirsByRepo[d.RepoID], d.Subdirectory)
		}
	}

	// 整仓最新快照文件数/LOC（供整仓行展示）
	latestSnapByScope := make(map[scopeKey]models.RepoSnapshot)
	for _, sn := range snaps {
		k := scopeKey{repoID: sn.RepoID, subdir: sn.Subdirectory}
		if _, exists := latestSnapByScope[k]; !exists {
			latestSnapByScope[k] = sn
		}
	}

	rows := make([]RepoScopeCoverageVO, 0, len(repos)+len(elems))
	for i := range repos {
		repo := &repos[i]
		branch := strings.TrimSpace(repo.Branch)
		if branch == "" {
			branch = "master"
		}
		repoOwnerName := repo.Owner.Name
		if repoOwnerName == "" {
			repoOwnerName = repo.Owner.Username
		}
		repoLevelWrite := IsRepoLevelWriter(user, repo)

		repoElems := elemsByRepo[repo.ID]
		repoMods := modulesByRepo[repo.ID]

		// 构建该仓库下架构元素索引与当前用户子目录写权限集合（供整仓行模块计数与子目录行共用）
		elemBySub := make(map[string]models.ArchitectureElement, len(repoElems))
		userOwnedSubdirs := make(map[string]bool)
		for _, el := range repoElems {
			ns, normErr := sandbox.NormalizeSubdir(el.Subdirectory)
			if normErr != nil || ns == "" {
				continue
			}
			if _, exists := elemBySub[ns]; !exists {
				elemBySub[ns] = el
			}
			if user != nil && user.ID > 0 && el.OwnerID != nil && *el.OwnerID == user.ID {
				userOwnedSubdirs[ns] = true
			}
		}

		// 1. 整仓范围行 (subdirectory == "")
		repoSnap := latestSnapByScope[scopeKey{repoID: repo.ID, subdir: ""}]
		repoRow := buildSingleScopeRow(
			repo, branch, "", "repo", nil, "",
			repo.OwnerID, repoOwnerName, repoLevelWrite,
			docsByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			revMap, codeDriftByDoc, specDriftByDoc,
			latestCommitByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			remoteHeadByRepo[repo.ID],
			activeTaskByScope[scopeKey{repoID: repo.ID, subdir: ""}],
		)
		repoRow.ScopeStatus = models.ModuleStatusActive
		repoRow.EffectiveFiles = repoSnap.EffectiveFiles
		repoRow.TotalLOC = repoSnap.TotalLOC
		if len(repoMods) > 0 {
			repoRow.ModulesScanned = true
			for _, mod := range repoMods {
				if mod.Status == models.ModuleStatusActive && mod.Subdirectory != "" {
					repoRow.ActiveModuleCount++
				}
			}
		} else {
			repoRow.ModulesScanned = repoSnap.ID > 0
			repoRow.ActiveModuleCount = len(elemBySub)
		}
		rows = append(rows, repoRow)

		// 2. 合并生成子目录行元数据解析函数
		resolveSubdirMeta := func(normSub string) (*uint, string, uint, string) {
			scopeOwnerID := repo.OwnerID
			scopeOwnerName := repoOwnerName
			var elIDPtr *uint
			elName := ""
			if el, ok := elemBySub[normSub]; ok {
				id := el.ID
				elIDPtr = &id
				elName = el.NameCn
				if elName == "" {
					elName = el.NameEn
				}
				if el.OwnerID != nil && *el.OwnerID > 0 {
					scopeOwnerID = *el.OwnerID
					if u, uOK := elemUsers[*el.OwnerID]; uOK {
						if u.Name != "" {
							scopeOwnerName = u.Name
						} else {
							scopeOwnerName = u.Username
						}
					}
				}
			}
			return elIDPtr, elName, scopeOwnerID, scopeOwnerName
		}

		seenSubdirs := make(map[string]bool)
		ignoredSubdirs := make(map[string]bool)

		if len(repoMods) > 0 {
			// 已刷新模块索引的仓库：展示 active / missing 模块（ignored 隐藏，若仍有文档则下面以 orphan 兜底）
			for _, mod := range repoMods {
				if mod.Status == models.ModuleStatusIgnored {
					ignoredSubdirs[mod.Subdirectory] = true
					continue
				}
				if mod.Subdirectory == "" || seenSubdirs[mod.Subdirectory] {
					continue
				}
				seenSubdirs[mod.Subdirectory] = true
				elIDPtr, elName, scopeOwnerID, scopeOwnerName := resolveSubdirMeta(mod.Subdirectory)
				canWrite := repoLevelWrite || userOwnedSubdirs[mod.Subdirectory]
				k := scopeKey{repoID: repo.ID, subdir: mod.Subdirectory}
				row := buildSingleScopeRow(
					repo, branch, mod.Subdirectory, "subdir", elIDPtr, elName,
					scopeOwnerID, scopeOwnerName, canWrite,
					docsByScope[k],
					revMap, codeDriftByDoc, specDriftByDoc,
					latestCommitByScope[k],
					remoteHeadByRepo[repo.ID],
					activeTaskByScope[k],
				)
				row.ScopeStatus = mod.Status
				row.ModuleSource = mod.Source
				row.EffectiveFiles = mod.EffectiveFiles
				row.TotalLOC = mod.TotalLOC
				rows = append(rows, row)
			}
		} else {
			// 尚未刷新模块索引的仓库：保持 100% 向后兼容，继续展示 architecture_elements 声明的子目录
			for _, el := range repoElems {
				normSub, normErr := sandbox.NormalizeSubdir(el.Subdirectory)
				if normErr != nil || normSub == "" || seenSubdirs[normSub] {
					continue
				}
				seenSubdirs[normSub] = true
				elIDPtr, elName, scopeOwnerID, scopeOwnerName := resolveSubdirMeta(normSub)
				canWrite := repoLevelWrite || userOwnedSubdirs[normSub]
				k := scopeKey{repoID: repo.ID, subdir: normSub}
				row := buildSingleScopeRow(
					repo, branch, normSub, "subdir", elIDPtr, elName,
					scopeOwnerID, scopeOwnerName, canWrite,
					docsByScope[k],
					revMap, codeDriftByDoc, specDriftByDoc,
					latestCommitByScope[k],
					remoteHeadByRepo[repo.ID],
					activeTaskByScope[k],
				)
				row.ScopeStatus = models.ModuleStatusActive
				row.ModuleSource = models.ModuleSourceArchElement
				if sn, ok := latestSnapByScope[k]; ok {
					row.EffectiveFiles = sn.EffectiveFiles
					row.TotalLOC = sn.TotalLOC
				}
				rows = append(rows, row)
			}
		}

		// 3. 孤儿文档范围兜底：对不在活跃/缺失模块集合内但已有历史文档的子目录，标注 scope_status="orphan" 保障可见可迁移
		for _, docSub := range docSubdirsByRepo[repo.ID] {
			if seenSubdirs[docSub] {
				continue
			}
			// 若已被显式忽略且当前用户看不到任何可见文档则跳过
			k := scopeKey{repoID: repo.ID, subdir: docSub}
			canWrite := repoLevelWrite || userOwnedSubdirs[docSub]
			elIDPtr, elName, scopeOwnerID, scopeOwnerName := resolveSubdirMeta(docSub)
			row := buildSingleScopeRow(
				repo, branch, docSub, "subdir", elIDPtr, elName,
				scopeOwnerID, scopeOwnerName, canWrite,
				docsByScope[k],
				revMap, codeDriftByDoc, specDriftByDoc,
				latestCommitByScope[k],
				remoteHeadByRepo[repo.ID],
				activeTaskByScope[k],
			)
			if len(row.DocStatuses) == 0 && ignoredSubdirs[docSub] {
				continue
			}
			seenSubdirs[docSub] = true
			row.ScopeStatus = "orphan"
			rows = append(rows, row)
		}
	}

	return rows, nil
}

func buildSingleScopeRow(
	repo *commonModels.Repository,
	branch string,
	subdir string,
	scopeType string,
	archElemID *uint,
	archElemName string,
	ownerID uint,
	ownerName string,
	canWrite bool,
	scopeDocs []models.Document,
	revMap map[uint]models.DocumentRevision,
	codeDriftByDoc map[uint]models.DocumentDrift,
	specDriftByDoc map[uint]models.DocumentDrift,
	latestCommitSHA string,
	remoteHeadSHA string,
	activeTaskID uint,
) RepoScopeCoverageVO {
	docStatuses := make(map[string]DocStatusSummaryVO)
	for _, d := range scopeDocs {
		// 草稿可见性隔离红线：普通用户严禁看到 status == "draft" 的未发布文档
		if !canWrite && d.Status != models.DocStatusApproved {
			continue
		}

		var pubRevNo int
		var verifierPassed bool
		var canApprove bool

		if d.PublishedRevisionID != nil {
			if pubRev, ok := revMap[*d.PublishedRevisionID]; ok {
				pubRevNo = pubRev.RevisionNo
				if !canWrite {
					verifierPassed = pubRev.VerifierPassed
					canApprove = pubRev.CanApprove
				}
			}
		}
		if canWrite && d.CurrentRevisionID != nil {
			if curRev, ok := revMap[*d.CurrentRevisionID]; ok {
				verifierPassed = curRev.VerifierPassed
				canApprove = curRev.CanApprove
			}
		}

		cd := codeDriftByDoc[d.ID]
		sd := specDriftByDoc[d.ID]
		affectedSlots := cd.AffectedSlotCount + sd.AffectedSlotCount

		curRevNo := d.CurrentRevisionNo
		if !canWrite && pubRevNo > 0 {
			curRevNo = pubRevNo
		}

		docStatuses[d.DocTypeSlug] = DocStatusSummaryVO{
			DocumentID:          d.ID,
			Status:              d.Status,
			CurrentRevisionNo:   curRevNo,
			PublishedRevisionNo: pubRevNo,
			HasCodeDrift:        cd.HasDrift,
			HasSpecDrift:        sd.HasDrift,
			AffectedSlotCount:   affectedSlots,
			VerifierPassed:      verifierPassed,
			CanApprove:          canApprove,
			UpdatedAt:           d.UpdatedAt,
		}
	}

	var activeTaskPtr *uint
	if activeTaskID > 0 {
		id := activeTaskID
		activeTaskPtr = &id
	}

	return RepoScopeCoverageVO{
		RepoID:          repo.ID,
		RepoName:        repo.Name,
		DepartmentID:    repo.DepartmentID,
		DepartmentName:  repo.Department.Name,
		Subdirectory:    subdir,
		ScopeType:       scopeType,
		ArchElementID:   archElemID,
		ArchElementName: archElemName,
		Branch:          branch,
		OwnerID:         ownerID,
		OwnerName:       ownerName,
		CanWrite:        canWrite,
		LatestCommitSHA: latestCommitSHA,
		RemoteHeadSHA:   remoteHeadSHA,
		ActiveTaskID:    activeTaskPtr,
		DocStatuses:     docStatuses,
	}
}

func matchesStatusFilter(docStatuses map[string]DocStatusSummaryVO, docType, status string) bool {
	if docType != "" {
		ds, exists := docStatuses[docType]
		switch status {
		case "missing":
			return !exists
		case "approved":
			return exists && ds.Status == models.DocStatusApproved
		case "draft":
			return exists && ds.Status == models.DocStatusDraft
		case "drifted":
			return exists && (ds.HasCodeDrift || ds.HasSpecDrift)
		default:
			return true
		}
	}

	switch status {
	case "missing":
		return len(docStatuses) == 0
	case "approved":
		for _, ds := range docStatuses {
			if ds.Status == models.DocStatusApproved {
				return true
			}
		}
		return false
	case "draft":
		for _, ds := range docStatuses {
			if ds.Status == models.DocStatusDraft {
				return true
			}
		}
		return false
	case "drifted":
		for _, ds := range docStatuses {
			if ds.HasCodeDrift || ds.HasSpecDrift {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func computeCoverageSummary(rows []RepoScopeCoverageVO) RepoCoverageSummaryVO {
	summary := RepoCoverageSummaryVO{
		TotalScopes: len(rows),
	}
	for _, r := range rows {
		if len(r.DocStatuses) > 0 {
			summary.CoveredScopes++
		}
		for _, ds := range r.DocStatuses {
			if ds.Status == models.DocStatusApproved {
				summary.ApprovedDocs++
			}
			if ds.HasCodeDrift || ds.HasSpecDrift {
				summary.DriftedDocs++
			}
		}
	}
	return summary
}
