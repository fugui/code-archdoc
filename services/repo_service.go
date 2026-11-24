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
	RepoID             uint                          `json:"repo_id"`
	RepoName           string                        `json:"repo_name"`
	DepartmentID       uint                          `json:"department_id"`
	DepartmentName     string                        `json:"department_name"`
	Subdirectory       string                        `json:"subdirectory"`
	ScopeType          string                        `json:"scope_type"`   // "repo" | "subdir"
	ScopeStatus        string                        `json:"scope_status"` // "active" | "missing" | "orphan"
	ModuleSource       string                        `json:"module_source,omitempty"`
	EffectiveFiles     int                           `json:"effective_files"`
	TotalLOC           int                           `json:"total_loc"`
	ActiveModuleCount  int                           `json:"active_module_count"`
	ModulesScanned     bool                          `json:"modules_scanned"`
	ArchElementID      *uint                         `json:"arch_element_id"`
	ArchElementName    string                        `json:"arch_element_name"`
	ArchElementType    string                        `json:"arch_element_type,omitempty"`
	ArchPath           string                        `json:"arch_path,omitempty"`
	SubsystemID        *uint                         `json:"subsystem_id,omitempty"`
	SubsystemName      string                        `json:"subsystem_name,omitempty"`
	Branch             string                        `json:"branch"`
	DefaultBranch      string                        `json:"default_branch"`
	IsBranchOverridden bool                          `json:"is_branch_overridden"`
	CanSwitchBranch    bool                          `json:"can_switch_branch"`
	OwnerID            uint                          `json:"owner_id"`
	OwnerName          string                        `json:"owner_name"`
	CanWrite           bool                          `json:"can_write"`
	LatestCommitSHA    string                        `json:"latest_commit_sha"`
	RemoteHeadSHA      string                        `json:"remote_head_sha"`
	ActiveTaskID       *uint                         `json:"active_task_id"`
	DocStatuses        map[string]DocStatusSummaryVO `json:"doc_statuses"`
}

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
	UnassignedRepoCount int `json:"unassigned_repo_count"`
	SystemRefDocCount   int `json:"system_ref_doc_count"`
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
	ID                   uint                  `json:"id"`
	Identifier           string                `json:"identifier"`
	NameCn               string                `json:"name_cn"`
	NameEn               string                `json:"name_en"`
	Type                 string                `json:"type"`
	ParentID             *uint                 `json:"parent_id"`
	OwnerID              *uint                 `json:"owner_id"`
	OwnerName            string                `json:"owner_name"`
	RepoID               *uint                 `json:"repo_id"`
	Subdirectory         string                `json:"subdirectory"`
	Description          string                `json:"description"`
	BindingStatus        string                `json:"binding_status,omitempty"`
	Scope                *RepoScopeCoverageVO  `json:"scope,omitempty"`
	Stats                ArchTreeStatsVO       `json:"stats"`
	DirectRefDocCount    int                   `json:"direct_ref_doc_count"`
	InheritedRefDocCount int                   `json:"inherited_ref_doc_count"`
	Scopes               []RepoScopeCoverageVO `json:"scopes"` // @deprecated: 固定返回 []
	Children             []*ArchTreeNodeVO     `json:"children"`
}

type ArchTreeResponse struct {
	Summary          ArchTreeSummaryVO       `json:"summary"`
	Subsystems       []ArchSubsystemOptionVO `json:"subsystems"`
	Nodes            []*ArchTreeNodeVO       `json:"nodes"`
	UnassignedScopes []RepoScopeCoverageVO   `json:"unassigned_scopes"` // @deprecated: 固定返回 []
}

type RepoCoverageSummaryVO struct {
	TotalScopes   int `json:"total_scopes"`
	CoveredScopes int `json:"covered_scopes"`
	ApprovedDocs  int `json:"approved_docs"`
	DriftedDocs   int `json:"drifted_docs"`
}

type RepoCoverageListResponse struct {
	Items      []RepoScopeCoverageVO   `json:"items"`
	Total      int64                   `json:"total"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	Summary    RepoCoverageSummaryVO   `json:"summary"`
	Subsystems []ArchSubsystemOptionVO `json:"subsystems"`
}

type GenTaskSummaryVO struct {
	ID              uint       `json:"id"`
	RepoID          uint       `json:"repo_id"`
	Subdirectory    string     `json:"subdirectory"`
	RefName         string     `json:"ref_name"`
	TaskKind        string     `json:"task_kind"`
	Engine          string     `json:"engine"`
	Priority        string     `json:"priority"`
	Status          string     `json:"status"`
	ProgressPercent int        `json:"progress_percent"`
	TriggeredBy     uint       `json:"triggered_by"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type RepoScopesDetailResponse struct {
	RepoID             uint                  `json:"repo_id"`
	RepoName           string                `json:"repo_name"`
	DepartmentID       uint                  `json:"department_id"`
	DepartmentName     string                `json:"department_name"`
	Branch             string                `json:"branch"`
	DefaultBranch      string                `json:"default_branch"`
	IsBranchOverridden bool                  `json:"is_branch_overridden"`
	CanSwitchBranch    bool                  `json:"can_switch_branch"`
	OwnerID            uint                  `json:"owner_id"`
	OwnerName          string                `json:"owner_name"`
	Scopes             []RepoScopeCoverageVO `json:"scopes"`
	RecentTasks        []GenTaskSummaryVO    `json:"recent_tasks"`
}

type RepoCoverageFilter struct {
	Page         int
	PageSize     int
	Search       string
	DepartmentID *uint
	DocType      string
	Status       string // all | approved | draft | drifted | missing
	ScopeType    string // all | repo | subdir
	SubsystemID  *uint
	ArchBinding  string // all | bound | unbound | unbound_repo
}

// RepoService 提供共享部门列表、仓库/子目录覆盖全景矩阵与单仓详情查询编排（F-01）。
type RepoService struct {
	masterRepo SharedMasterRepo
	docRepo    DocumentRepo
	snapRepo   SnapshotRepo
	taskRepo   TaskRepo
	driftRepo  DriftRepo
	moduleRepo ModuleRepo
	refDocRepo ReferenceDocRepo
	rbacSvc    *RBACService
}

// WithModuleRepo 注入派生模块索引仓储，使覆盖全景矩阵融合自动发现模块、已消失模块与孤儿文档范围（ADR-04 D-1）。
func (s *RepoService) WithModuleRepo(moduleRepo ModuleRepo) *RepoService {
	s.moduleRepo = moduleRepo
	return s
}

// WithRefDocRepo 注入架构参考文档仓储，使 GetArchTree 批量聚合各层级直接/继承参考文档计数（ADR-10）。
func (s *RepoService) WithRefDocRepo(refDocRepo ReferenceDocRepo) *RepoService {
	s.refDocRepo = refDocRepo
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

func buildBoundModuleRepoIDs(allElems []models.ArchitectureElement) map[uint]bool {
	bound := make(map[uint]bool)
	for _, el := range allElems {
		if el.Type == "module" && el.RepoID != nil && *el.RepoID > 0 {
			bound[*el.RepoID] = true
		}
	}
	return bound
}

func (s *RepoService) ListRepoCoverage(ctx context.Context, user *commonModels.User, filter RepoCoverageFilter) (*RepoCoverageListResponse, error) {
	page, pageSize := NormalizePagination(filter.Page, filter.PageSize)

	repos, err := s.masterRepo.ListAllActiveRepositories(ctx, filter.DepartmentID)
	if err != nil {
		return nil, err
	}

	allRows, idx, err := s.buildCoverageData(ctx, user, repos)
	if err != nil {
		return nil, err
	}

	subsystems := idx.buildSubsystems(allRows)
	boundModuleRepoIDs := buildBoundModuleRepoIDs(idx.allElems)
	filtered := filterCoverageRowsWithArchModules(allRows, boundModuleRepoIDs, filter)

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
		Items:      filtered[start:end],
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		Summary:    summary,
		Subsystems: subsystems,
	}, nil
}

// GetArchTree 按平台 architecture_elements 树形结构聚合功能模块级覆盖范围与统计指标（ADR-09）。
func (s *RepoService) GetArchTree(ctx context.Context, user *commonModels.User, filter RepoCoverageFilter) (*ArchTreeResponse, error) {
	// 严禁按 filter.DepartmentID 预过滤活跃仓库：防止跨部门活跃仓库误判为 repo_inactive 且破坏 UnassignedRepoCount 全量口径
	repos, err := s.masterRepo.ListAllActiveRepositories(ctx, nil)
	if err != nil {
		return nil, err
	}

	bctx, _, err := s.buildCoverageContext(ctx, user, repos)
	if err != nil {
		return nil, err
	}

	boundModuleRepoIDs := buildBoundModuleRepoIDs(bctx.idx.allElems)
	unassignedRepoCount := 0
	for _, r := range repos {
		if !boundModuleRepoIDs[r.ID] {
			unassignedRepoCount++
		}
	}

	nodeByID := make(map[uint]*ArchTreeNodeVO, len(bctx.idx.allElems))
	for _, el := range bctx.idx.allElems {
		node := &ArchTreeNodeVO{
			ID:           el.ID,
			Identifier:   el.Identifier,
			NameCn:       el.NameCn,
			NameEn:       el.NameEn,
			Type:         el.Type,
			ParentID:     el.ParentID,
			OwnerID:      el.OwnerID,
			OwnerName:    bctx.idx.resolveUserName(el.OwnerID, ""),
			RepoID:       el.RepoID,
			Subdirectory: el.Subdirectory,
			Description:  el.Description,
			Scopes:       []RepoScopeCoverageVO{},
			Children:     []*ArchTreeNodeVO{},
		}
		if el.Type == "module" {
			bindingStatus, scopePtr := bctx.resolveModuleScope(el, user)
			node.BindingStatus = bindingStatus
			node.Scope = scopePtr
		}
		nodeByID[el.ID] = node
	}

	roots := make([]*ArchTreeNodeVO, 0)
	for _, el := range bctx.idx.allElems {
		node := nodeByID[el.ID]
		if el.ParentID != nil && *el.ParentID != el.ID {
			if parent, ok := nodeByID[*el.ParentID]; ok && !wouldCreateTreeCycle(el.ID, *el.ParentID, bctx.idx.elemByID) {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	// 剪枝前按全树统计各子系统下的全部功能模块数（含 unbound / repo_inactive）
	subsystems := make([]ArchSubsystemOptionVO, 0)
	for _, el := range bctx.idx.allElems {
		if el.Type != "subsystem" {
			continue
		}
		modCount := countDescendantModules(nodeByID[el.ID])
		subsystems = append(subsystems, ArchSubsystemOptionVO{
			ID:          el.ID,
			Identifier:  el.Identifier,
			NameCn:      el.NameCn,
			NameEn:      el.NameEn,
			ModuleCount: modCount,
			ScopeCount:  modCount,
		})
	}

	searchLower := strings.ToLower(strings.TrimSpace(filter.Search))
	statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
	scopeTypeFilter := strings.ToLower(strings.TrimSpace(filter.ScopeType))
	archBindingFilter := strings.ToLower(strings.TrimSpace(filter.ArchBinding))

	hasNonSearchRestriction := filter.DepartmentID != nil ||
		(statusFilter != "" && statusFilter != "all") ||
		(scopeTypeFilter != "" && scopeTypeFilter != "all") ||
		(archBindingFilter != "" && archBindingFilter != "all")
	hasAnyFilter := hasNonSearchRestriction || searchLower != "" || filter.SubsystemID != nil

	// 先筛选剪枝，后向上聚合统计（保证可见模块与父节点 stats、顶部 summary 100% 一致）
	if hasAnyFilter {
		pruned := make([]*ArchTreeNodeVO, 0, len(roots))
		for _, root := range roots {
			if kept := pruneArchTreeNode(root, filter, searchLower, hasNonSearchRestriction, false, false); kept != nil {
				pruned = append(pruned, kept)
			}
		}
		roots = pruned
	}

	summary := ArchTreeSummaryVO{
		UnassignedRepoCount: unassignedRepoCount,
	}
	for _, root := range roots {
		st := aggregateArchTreeStats(root)
		summary.TotalModules += st.ModuleCount
		summary.BoundModules += st.BoundModuleCount
		summary.CoveredModules += st.CoveredModuleCount
		summary.ApprovedDocs += st.ApprovedDocCount
		summary.DraftDocs += st.DraftDocCount
		summary.DriftedDocs += st.DriftedDocCount
	}

	if s.refDocRepo != nil {
		if sysCount, err := s.populateArchTreeRefDocCounts(ctx, roots); err == nil {
			summary.SystemRefDocCount = sysCount
		}
	}

	return &ArchTreeResponse{
		Summary:          summary,
		Subsystems:       subsystems,
		Nodes:            roots,
		UnassignedScopes: []RepoScopeCoverageVO{},
	}, nil
}

// populateArchTreeRefDocCounts 一次性批量加载活跃参考文档与绑定，在内存中按祖先链 O(N) 一次 DFS 聚合各节点参考文档计数。
func (s *RepoService) populateArchTreeRefDocCounts(ctx context.Context, roots []*ArchTreeNodeVO) (int, error) {
	docs, bindings, err := s.refDocRepo.ListAllActiveDocsAndBindings(ctx)
	if err != nil {
		return 0, err
	}
	activeDocs := make(map[uint]bool, len(docs))
	for _, d := range docs {
		if d.Status == models.RefDocStatusActive {
			activeDocs[d.ID] = true
		}
	}

	systemDocs := make(map[uint]bool)
	archDirectDocs := make(map[uint]map[uint]bool)
	archInheritDocs := make(map[uint]map[uint]bool)
	repoExactDocs := make(map[scopeKey]map[uint]bool)
	repoRootInheritDocs := make(map[uint]map[uint]bool)

	for _, b := range bindings {
		if !activeDocs[b.DocID] {
			continue
		}
		switch b.ScopeLevel {
		case models.RefDocScopeSystem:
			systemDocs[b.DocID] = true
		case models.RefDocScopeSubsystem, models.RefDocScopeGroup, models.RefDocScopeModule:
			if b.ArchElementID == nil || *b.ArchElementID == 0 {
				continue
			}
			aid := *b.ArchElementID
			if archDirectDocs[aid] == nil {
				archDirectDocs[aid] = make(map[uint]bool)
			}
			archDirectDocs[aid][b.DocID] = true
			if b.InheritToChildren {
				if archInheritDocs[aid] == nil {
					archInheritDocs[aid] = make(map[uint]bool)
				}
				archInheritDocs[aid][b.DocID] = true
			}
		case models.RefDocScopeRepo:
			if b.RepoID == nil || *b.RepoID == 0 {
				continue
			}
			normSub, nErr := sandbox.NormalizeSubdir(b.Subdirectory)
			if nErr != nil {
				continue
			}
			k := scopeKey{repoID: *b.RepoID, subdir: normSub}
			if repoExactDocs[k] == nil {
				repoExactDocs[k] = make(map[uint]bool)
			}
			repoExactDocs[k][b.DocID] = true
			if normSub == "" && b.InheritToChildren {
				if repoRootInheritDocs[*b.RepoID] == nil {
					repoRootInheritDocs[*b.RepoID] = make(map[uint]bool)
				}
				repoRootInheritDocs[*b.RepoID][b.DocID] = true
			}
		}
	}

	var walk func(node *ArchTreeNodeVO, inherited map[uint]bool)
	walk = func(node *ArchTreeNodeVO, inherited map[uint]bool) {
		if node == nil {
			return
		}
		direct := archDirectDocs[node.ID]
		node.DirectRefDocCount = len(direct)

		// 计算当前节点实际继承的参考文档数（排除已在当前节点直接绑定的 doc_id，避免重复计数）
		inhSeen := make(map[uint]bool, len(inherited)+4)
		for docID := range inherited {
			if !direct[docID] {
				inhSeen[docID] = true
			}
		}
		if node.Type == models.RefDocScopeModule && node.RepoID != nil && *node.RepoID > 0 {
			if normSub, nErr := sandbox.NormalizeSubdir(node.Subdirectory); nErr == nil {
				for docID := range repoExactDocs[scopeKey{repoID: *node.RepoID, subdir: normSub}] {
					if !direct[docID] {
						inhSeen[docID] = true
					}
				}
				if normSub != "" {
					for docID := range repoRootInheritDocs[*node.RepoID] {
						if !direct[docID] {
							inhSeen[docID] = true
						}
					}
				}
			}
		}
		node.InheritedRefDocCount = len(inhSeen)

		if len(node.Children) == 0 {
			return
		}
		// 仅向子节点传递当前节点可继承的集合
		nextInherited := make(map[uint]bool, len(inherited)+len(archInheritDocs[node.ID]))
		for docID := range inherited {
			nextInherited[docID] = true
		}
		for docID := range archInheritDocs[node.ID] {
			nextInherited[docID] = true
		}
		for _, ch := range node.Children {
			walk(ch, nextInherited)
		}
	}

	for _, root := range roots {
		walk(root, systemDocs)
	}
	return len(systemDocs), nil
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
			Engine:          models.NormalizeEngine(t.Engine),
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
	defaultBranch := models.NormalizeRepoDefaultBranch(repo.Branch)
	targetBranch := ""
	if s.moduleRepo != nil {
		if policy, pErr := s.moduleRepo.GetPolicy(ctx, repoID); pErr == nil && policy != nil {
			targetBranch = strings.TrimSpace(policy.TargetBranch)
		}
	}
	branch := models.ResolveEffectiveBranch(repo.Branch, targetBranch)
	isOverridden := targetBranch != "" && targetBranch != defaultBranch
	canSwitch := IsRepoLevelWriter(user, repo)

	return &RepoScopesDetailResponse{
		RepoID:             repo.ID,
		RepoName:           repo.Name,
		DepartmentID:       repo.DepartmentID,
		DepartmentName:     repo.Department.Name,
		Branch:             branch,
		DefaultBranch:      defaultBranch,
		IsBranchOverridden: isOverridden,
		CanSwitchBranch:    canSwitch,
		OwnerID:            repo.OwnerID,
		OwnerName:          ownerName,
		Scopes:             rows,
		RecentTasks:        recentTasks,
	}, nil
}

type scopeKey struct {
	repoID uint
	subdir string
}

type archIndex struct {
	allElems       []models.ArchitectureElement
	elemByID       map[uint]models.ArchitectureElement
	elemsByRepo    map[uint][]models.ArchitectureElement
	subsystemByKey map[string]models.ArchitectureElement
	elemUsers      map[uint]commonModels.User
}

func archElementDisplayName(el models.ArchitectureElement) string {
	if s := strings.TrimSpace(el.NameCn); s != "" {
		return s
	}
	if s := strings.TrimSpace(el.NameEn); s != "" {
		return s
	}
	return strings.TrimSpace(el.Identifier)
}

func (idx *archIndex) resolveUserName(ownerID *uint, fallback string) string {
	if ownerID != nil && *ownerID > 0 {
		if u, ok := idx.elemUsers[*ownerID]; ok {
			if u.Name != "" {
				return u.Name
			}
			if u.Username != "" {
				return u.Username
			}
		}
	}
	return fallback
}

func (idx *archIndex) resolveHierarchy(el models.ArchitectureElement) (string, *uint, string) {
	parts := make([]string, 0, 4)
	var subsysID *uint
	subsysName := ""

	visited := make(map[uint]bool, 4)
	cur := el
	for {
		if visited[cur.ID] {
			break
		}
		visited[cur.ID] = true

		if name := archElementDisplayName(cur); name != "" {
			parts = append(parts, name)
		}
		if subsysID == nil && cur.Type == "subsystem" {
			id := cur.ID
			subsysID = &id
			subsysName = archElementDisplayName(cur)
		}
		if cur.ParentID == nil || *cur.ParentID == 0 {
			break
		}
		parent, ok := idx.elemByID[*cur.ParentID]
		if !ok {
			break
		}
		cur = parent
	}

	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, " / "), subsysID, subsysName
}

func archElementTypePriority(elemType string) int {
	switch elemType {
	case "module":
		return 3
	case "group":
		return 2
	case "subsystem":
		return 1
	default:
		return 0
	}
}

func (idx *archIndex) resolveRepoElement(repo *commonModels.Repository) *models.ArchitectureElement {
	var best *models.ArchitectureElement
	bestPriority := -1
	for i := range idx.elemsByRepo[repo.ID] {
		el := &idx.elemsByRepo[repo.ID][i]
		ns, normErr := sandbox.NormalizeSubdir(el.Subdirectory)
		if normErr != nil || ns != "" {
			continue
		}
		p := archElementTypePriority(el.Type)
		if best == nil || p > bestPriority {
			best = el
			bestPriority = p
		}
	}
	if best != nil {
		return best
	}

	if sg := strings.ToLower(strings.TrimSpace(repo.ServiceGroup)); sg != "" {
		if el, ok := idx.subsystemByKey[sg]; ok {
			cp := el
			return &cp
		}
	}
	return nil
}

func (idx *archIndex) buildSubsystems(allRows []RepoScopeCoverageVO) []ArchSubsystemOptionVO {
	scopeCounts := make(map[uint]int)
	for _, r := range allRows {
		if r.SubsystemID != nil {
			scopeCounts[*r.SubsystemID]++
		}
	}
	out := make([]ArchSubsystemOptionVO, 0)
	for _, el := range idx.allElems {
		if el.Type != "subsystem" {
			continue
		}
		out = append(out, ArchSubsystemOptionVO{
			ID:         el.ID,
			Identifier: el.Identifier,
			NameCn:     el.NameCn,
			NameEn:     el.NameEn,
			ScopeCount: scopeCounts[el.ID],
		})
	}
	return out
}

func filterCoverageRows(allRows []RepoScopeCoverageVO, filter RepoCoverageFilter) []RepoScopeCoverageVO {
	return filterCoverageRowsWithArchModules(allRows, nil, filter)
}

func filterCoverageRowsWithArchModules(allRows []RepoScopeCoverageVO, boundModuleRepoIDs map[uint]bool, filter RepoCoverageFilter) []RepoScopeCoverageVO {
	filtered := make([]RepoScopeCoverageVO, 0, len(allRows))
	searchLower := strings.ToLower(strings.TrimSpace(filter.Search))
	scopeTypeFilter := strings.ToLower(strings.TrimSpace(filter.ScopeType))
	statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
	docTypeFilter := strings.TrimSpace(filter.DocType)
	archBindingFilter := strings.ToLower(strings.TrimSpace(filter.ArchBinding))

	for _, row := range allRows {
		if scopeTypeFilter != "" && scopeTypeFilter != "all" && row.ScopeType != scopeTypeFilter {
			continue
		}
		if filter.SubsystemID != nil {
			if row.SubsystemID == nil || *row.SubsystemID != *filter.SubsystemID {
				continue
			}
		}
		switch archBindingFilter {
		case "bound":
			if row.ArchElementID == nil {
				continue
			}
		case "unbound":
			if row.ArchElementID != nil {
				continue
			}
		case ArchBindingFilterUnboundRepo:
			if boundModuleRepoIDs[row.RepoID] {
				continue
			}
		}
		if searchLower != "" {
			if !strings.Contains(strings.ToLower(row.RepoName), searchLower) &&
				!strings.Contains(strings.ToLower(row.Subdirectory), searchLower) &&
				!strings.Contains(strings.ToLower(row.ArchElementName), searchLower) &&
				!strings.Contains(strings.ToLower(row.ArchPath), searchLower) &&
				!strings.Contains(strings.ToLower(row.SubsystemName), searchLower) {
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
	return filtered
}

func wouldCreateTreeCycle(nodeID, parentID uint, elemByID map[uint]models.ArchitectureElement) bool {
	visited := map[uint]bool{nodeID: true}
	curID := parentID
	for curID > 0 {
		if visited[curID] {
			return true
		}
		visited[curID] = true
		el, ok := elemByID[curID]
		if !ok || el.ParentID == nil {
			return false
		}
		curID = *el.ParentID
	}
	return false
}

func countDescendantModules(node *ArchTreeNodeVO) int {
	if node == nil {
		return 0
	}
	total := 0
	if node.Type == "module" {
		total = 1
	}
	for _, ch := range node.Children {
		total += countDescendantModules(ch)
	}
	return total
}

func aggregateArchTreeStats(node *ArchTreeNodeVO) ArchTreeStatsVO {
	var stats ArchTreeStatsVO
	if node.Type == "module" {
		stats.ModuleCount = 1
		if node.BindingStatus == ArchBindingStatusBound && node.Scope != nil {
			stats.BoundModuleCount = 1
			if len(node.Scope.DocStatuses) > 0 {
				stats.CoveredModuleCount = 1
			}
			for _, ds := range node.Scope.DocStatuses {
				if ds.Status == models.DocStatusApproved {
					stats.ApprovedDocCount++
				} else {
					stats.DraftDocCount++
					if !ds.CanApprove {
						stats.BlockedDocCount++
					}
				}
				if ds.HasCodeDrift || ds.HasSpecDrift {
					stats.DriftedDocCount++
				}
			}
		}
	}
	for _, child := range node.Children {
		cs := aggregateArchTreeStats(child)
		stats.ModuleCount += cs.ModuleCount
		stats.BoundModuleCount += cs.BoundModuleCount
		stats.CoveredModuleCount += cs.CoveredModuleCount
		stats.ApprovedDocCount += cs.ApprovedDocCount
		stats.DraftDocCount += cs.DraftDocCount
		stats.DriftedDocCount += cs.DriftedDocCount
		stats.BlockedDocCount += cs.BlockedDocCount
	}
	node.Stats = stats
	return stats
}

func archNodeMatchesSearch(node *ArchTreeNodeVO, searchLower string) bool {
	return strings.Contains(strings.ToLower(node.NameCn), searchLower) ||
		strings.Contains(strings.ToLower(node.NameEn), searchLower) ||
		strings.Contains(strings.ToLower(node.Identifier), searchLower)
}

func pruneArchTreeNode(
	node *ArchTreeNodeVO,
	filter RepoCoverageFilter,
	searchLower string,
	hasNonSearchRestriction bool,
	ancestorSearchMatched bool,
	inTargetSubsystem bool,
) *ArchTreeNodeVO {
	effectiveInSubsys := filter.SubsystemID == nil || inTargetSubsystem || (node.Type == "subsystem" && node.ID == *filter.SubsystemID)
	selfSearchMatched := searchLower != "" && archNodeMatchesSearch(node, searchLower)
	nextSearchMatched := ancestorSearchMatched || selfSearchMatched

	if node.Type == "module" {
		if !effectiveInSubsys {
			return nil
		}
		archBindingFilter := strings.ToLower(strings.TrimSpace(filter.ArchBinding))
		switch archBindingFilter {
		case "bound":
			if node.BindingStatus != ArchBindingStatusBound {
				return nil
			}
		case "unbound":
			if node.BindingStatus == ArchBindingStatusBound {
				return nil
			}
		}
		scopeTypeFilter := strings.ToLower(strings.TrimSpace(filter.ScopeType))
		if scopeTypeFilter != "" && scopeTypeFilter != "all" {
			if node.Scope == nil || node.Scope.ScopeType != scopeTypeFilter {
				return nil
			}
		}
		if filter.DepartmentID != nil {
			if node.Scope == nil || node.Scope.DepartmentID != *filter.DepartmentID {
				return nil
			}
		}
		statusFilter := strings.ToLower(strings.TrimSpace(filter.Status))
		if statusFilter != "" && statusFilter != "all" {
			if node.Scope == nil || !matchesStatusFilter(node.Scope.DocStatuses, strings.TrimSpace(filter.DocType), statusFilter) {
				return nil
			}
		}
		if searchLower != "" {
			matched := nextSearchMatched
			if !matched && node.Scope != nil {
				matched = strings.Contains(strings.ToLower(node.Scope.RepoName), searchLower) ||
					strings.Contains(strings.ToLower(node.Scope.Subdirectory), searchLower)
			}
			if !matched {
				return nil
			}
		}
		return node
	}

	keptChildren := make([]*ArchTreeNodeVO, 0, len(node.Children))
	for _, child := range node.Children {
		if kept := pruneArchTreeNode(child, filter, searchLower, hasNonSearchRestriction, nextSearchMatched, effectiveInSubsys); kept != nil {
			keptChildren = append(keptChildren, kept)
		}
	}
	node.Children = keptChildren

	if len(keptChildren) > 0 {
		return node
	}

	if !hasNonSearchRestriction && effectiveInSubsys {
		if searchLower != "" && selfSearchMatched {
			return node
		}
		if searchLower == "" && filter.SubsystemID != nil {
			return node
		}
	}
	return nil
}

func (s *RepoService) loadArchIndex(ctx context.Context) (*archIndex, error) {
	allElems, err := s.masterRepo.ListAllArchElements(ctx)
	if err != nil {
		return nil, err
	}

	elemByID := make(map[uint]models.ArchitectureElement, len(allElems))
	elemsByRepo := make(map[uint][]models.ArchitectureElement)
	subsystemByKey := make(map[string]models.ArchitectureElement)
	ownerIDSet := make(map[uint]struct{})

	for _, el := range allElems {
		elemByID[el.ID] = el
		if el.RepoID != nil {
			elemsByRepo[*el.RepoID] = append(elemsByRepo[*el.RepoID], el)
		}
		if el.Type == "subsystem" {
			for _, key := range []string{el.Identifier, el.NameCn, el.NameEn} {
				k := strings.ToLower(strings.TrimSpace(key))
				if k != "" {
					if _, exists := subsystemByKey[k]; !exists {
						subsystemByKey[k] = el
					}
				}
			}
		}
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

	return &archIndex{
		allElems:       allElems,
		elemByID:       elemByID,
		elemsByRepo:    elemsByRepo,
		subsystemByKey: subsystemByKey,
		elemUsers:      elemUsers,
	}, nil
}

type coverageBuildContext struct {
	idx                   *archIndex
	activeRepoByID        map[uint]*commonModels.Repository
	rowsByScope           map[scopeKey]RepoScopeCoverageVO
	defaultBranchByRepo   map[uint]string
	effectiveBranchByRepo map[uint]string
	overriddenByRepo      map[uint]bool
	docsByScope           map[scopeKey][]models.Document
	revMap                map[uint]models.DocumentRevision
	codeDriftByDoc        map[uint]models.DocumentDrift
	specDriftByDoc        map[uint]models.DocumentDrift
	latestCommitByScope   map[scopeKey]string
	latestSnapByScope     map[scopeKey]models.RepoSnapshot
	remoteHeadByRepo      map[uint]string
	activeTaskByScope     map[scopeKey]uint
	modulesByRepo         map[uint][]models.RepoModule
	userOwnedSubdirs      map[scopeKey]bool
}

func (bctx *coverageBuildContext) resolveModuleScope(el models.ArchitectureElement, user *commonModels.User) (string, *RepoScopeCoverageVO) {
	if el.RepoID == nil || *el.RepoID == 0 {
		return ArchBindingStatusUnbound, nil
	}
	repo, ok := bctx.activeRepoByID[*el.RepoID]
	if !ok {
		return ArchBindingStatusRepoInactive, nil
	}
	normSub, normErr := sandbox.NormalizeSubdir(el.Subdirectory)
	if normErr != nil {
		return ArchBindingStatusUnbound, nil
	}

	k := scopeKey{repoID: repo.ID, subdir: normSub}
	elID := el.ID
	elName := archElementDisplayName(el)
	archPath, subsysID, subsysName := bctx.idx.resolveHierarchy(el)

	if existing, hit := bctx.rowsByScope[k]; hit {
		scopeCopy := existing
		docsCopy := make(map[string]DocStatusSummaryVO, len(existing.DocStatuses))
		for dk, dv := range existing.DocStatuses {
			docsCopy[dk] = dv
		}
		scopeCopy.DocStatuses = docsCopy
		scopeCopy.ArchElementID = &elID
		scopeCopy.ArchElementName = elName
		scopeCopy.ArchElementType = el.Type
		scopeCopy.ArchPath = archPath
		scopeCopy.SubsystemID = subsysID
		scopeCopy.SubsystemName = subsysName
		if el.OwnerID != nil && *el.OwnerID > 0 {
			scopeCopy.OwnerID = *el.OwnerID
			scopeCopy.OwnerName = bctx.idx.resolveUserName(el.OwnerID, existing.OwnerName)
		}
		if scopeCopy.EffectiveFiles == 0 {
			if sn, hasSnap := bctx.latestSnapByScope[k]; hasSnap {
				scopeCopy.EffectiveFiles = sn.EffectiveFiles
				scopeCopy.TotalLOC = sn.TotalLOC
			}
		}
		return ArchBindingStatusBound, &scopeCopy
	}

	// 实时兜底构造：覆盖 repoMods 标为 ignored 或尚未扫描登记的子目录/整仓
	scopeType := "repo"
	if normSub != "" {
		scopeType = "subdir"
	}
	repoLevelWrite := IsRepoLevelWriter(user, repo)
	canWrite := repoLevelWrite
	if normSub != "" && !canWrite && user != nil && user.ID > 0 {
		if (el.OwnerID != nil && *el.OwnerID == user.ID) || bctx.userOwnedSubdirs[k] {
			canWrite = true
		}
	}

	repoOwnerName := repo.Owner.Name
	if repoOwnerName == "" {
		repoOwnerName = repo.Owner.Username
	}
	ownerID := repo.OwnerID
	ownerName := repoOwnerName
	if el.OwnerID != nil && *el.OwnerID > 0 {
		ownerID = *el.OwnerID
		ownerName = bctx.idx.resolveUserName(el.OwnerID, repoOwnerName)
	}

	row := buildSingleScopeRow(
		repo, bctx.effectiveBranchByRepo[repo.ID], normSub, scopeType,
		&elID, elName, el.Type, archPath, subsysID, subsysName,
		ownerID, ownerName, canWrite,
		bctx.docsByScope[k],
		bctx.revMap, bctx.codeDriftByDoc, bctx.specDriftByDoc,
		bctx.latestCommitByScope[k],
		bctx.remoteHeadByRepo[repo.ID],
		bctx.activeTaskByScope[k],
	)
	row.DefaultBranch = bctx.defaultBranchByRepo[repo.ID]
	row.IsBranchOverridden = bctx.overriddenByRepo[repo.ID]
	row.CanSwitchBranch = repoLevelWrite
	row.ScopeStatus = models.ModuleStatusActive
	row.ModuleSource = models.ModuleSourceArchElement

	for _, mod := range bctx.modulesByRepo[repo.ID] {
		if mod.Subdirectory == normSub {
			row.ScopeStatus = mod.Status
			row.EffectiveFiles = mod.EffectiveFiles
			row.TotalLOC = mod.TotalLOC
			row.ModuleSource = mod.Source
			break
		}
	}
	if row.EffectiveFiles == 0 {
		if sn, hasSnap := bctx.latestSnapByScope[k]; hasSnap {
			row.EffectiveFiles = sn.EffectiveFiles
			row.TotalLOC = sn.TotalLOC
		}
	}

	return ArchBindingStatusBound, &row
}

func (s *RepoService) buildCoverageRows(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) ([]RepoScopeCoverageVO, error) {
	rows, _, err := s.buildCoverageData(ctx, user, repos)
	return rows, err
}

func (s *RepoService) buildCoverageData(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) ([]RepoScopeCoverageVO, *archIndex, error) {
	bctx, rows, err := s.buildCoverageContext(ctx, user, repos)
	if err != nil {
		return nil, nil, err
	}
	return rows, bctx.idx, nil
}

func (s *RepoService) buildCoverageContext(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) (*coverageBuildContext, []RepoScopeCoverageVO, error) {
	idx, err := s.loadArchIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(repos) == 0 {
		emptyCtx := &coverageBuildContext{
			idx:                   idx,
			activeRepoByID:        make(map[uint]*commonModels.Repository),
			rowsByScope:           make(map[scopeKey]RepoScopeCoverageVO),
			defaultBranchByRepo:   make(map[uint]string),
			effectiveBranchByRepo: make(map[uint]string),
			overriddenByRepo:      make(map[uint]bool),
			docsByScope:           make(map[scopeKey][]models.Document),
			revMap:                make(map[uint]models.DocumentRevision),
			codeDriftByDoc:        make(map[uint]models.DocumentDrift),
			specDriftByDoc:        make(map[uint]models.DocumentDrift),
			latestCommitByScope:   make(map[scopeKey]string),
			latestSnapByScope:     make(map[scopeKey]models.RepoSnapshot),
			remoteHeadByRepo:      make(map[uint]string),
			activeTaskByScope:     make(map[scopeKey]uint),
			modulesByRepo:         make(map[uint][]models.RepoModule),
			userOwnedSubdirs:      make(map[scopeKey]bool),
		}
		return emptyCtx, []RepoScopeCoverageVO{}, nil
	}

	repoIDs := make([]uint, 0, len(repos))
	activeRepoByID := make(map[uint]*commonModels.Repository, len(repos))
	for i := range repos {
		repoIDs = append(repoIDs, repos[i].ID)
		activeRepoByID[repos[i].ID] = &repos[i]
	}

	targetBranchByRepo := make(map[uint]string, len(repos))
	if s.moduleRepo != nil {
		if policies, pErr := s.moduleRepo.ListPoliciesByRepos(ctx, repoIDs); pErr == nil {
			for _, p := range policies {
				targetBranchByRepo[p.RepoID] = strings.TrimSpace(p.TargetBranch)
			}
		}
	}
	defaultBranchByRepo := make(map[uint]string, len(repos))
	effectiveBranchByRepo := make(map[uint]string, len(repos))
	overriddenByRepo := make(map[uint]bool, len(repos))
	for _, r := range repos {
		defBr := models.NormalizeRepoDefaultBranch(r.Branch)
		tgtBr := targetBranchByRepo[r.ID]
		effBr := models.ResolveEffectiveBranch(r.Branch, tgtBr)
		defaultBranchByRepo[r.ID] = defBr
		effectiveBranchByRepo[r.ID] = effBr
		overriddenByRepo[r.ID] = tgtBr != "" && tgtBr != defBr
	}

	docs, err := s.docRepo.ListByRepos(ctx, repoIDs)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}

	drifts, err := s.driftRepo.ListDriftsByDocIDs(ctx, docIDs)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	snapMatchesBranch := func(repoID uint, refName string) bool {
		eff := effectiveBranchByRepo[repoID]
		if refName == eff {
			return true
		}
		return eff == "master" && refName == "main" && targetBranchByRepo[repoID] == ""
	}
	latestCommitByScope := make(map[scopeKey]string)
	for _, sn := range snaps {
		if !snapMatchesBranch(sn.RepoID, sn.RefName) {
			continue
		}
		k := scopeKey{repoID: sn.RepoID, subdir: sn.Subdirectory}
		if _, exists := latestCommitByScope[k]; !exists {
			latestCommitByScope[k] = sn.CommitSHA
		}
	}

	remoteHeads, err := s.driftRepo.ListRemoteHeadsByRepos(ctx, repoIDs)
	if err != nil {
		return nil, nil, err
	}
	remoteHeadByRepo := make(map[uint]string)
	for _, rh := range remoteHeads {
		if rh.RemoteHeadSHA == "" {
			continue
		}
		if snapMatchesBranch(rh.RepoID, rh.RefName) {
			remoteHeadByRepo[rh.RepoID] = rh.RemoteHeadSHA
		} else if _, exists := remoteHeadByRepo[rh.RepoID]; !exists && targetBranchByRepo[rh.RepoID] == "" {
			remoteHeadByRepo[rh.RepoID] = rh.RemoteHeadSHA
		}
	}

	activeTasks, err := s.taskRepo.ListActiveTasksByRepos(ctx, repoIDs)
	if err != nil {
		return nil, nil, err
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

	// 整仓最新快照文件数/LOC（供整仓行展示，按当前有效分支匹配）
	latestSnapByScope := make(map[scopeKey]models.RepoSnapshot)
	for _, sn := range snaps {
		if !snapMatchesBranch(sn.RepoID, sn.RefName) {
			continue
		}
		k := scopeKey{repoID: sn.RepoID, subdir: sn.Subdirectory}
		if _, exists := latestSnapByScope[k]; !exists {
			latestSnapByScope[k] = sn
		}
	}

	userOwnedScopeKeys := make(map[scopeKey]bool)
	rows := make([]RepoScopeCoverageVO, 0, len(repos)+len(idx.allElems))
	for i := range repos {
		repo := &repos[i]
		branch := effectiveBranchByRepo[repo.ID]
		defBranch := defaultBranchByRepo[repo.ID]
		isOverridden := overriddenByRepo[repo.ID]
		repoOwnerName := repo.Owner.Name
		if repoOwnerName == "" {
			repoOwnerName = repo.Owner.Username
		}
		repoLevelWrite := IsRepoLevelWriter(user, repo)

		repoElems := idx.elemsByRepo[repo.ID]
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
				userOwnedScopeKeys[scopeKey{repoID: repo.ID, subdir: ns}] = true
			}
		}

		// 1. 整仓范围行 (subdirectory == "")：解析整仓绑定架构元素及所属子系统
		var repoArchElemID *uint
		var repoArchElemName, repoArchElemType, repoArchPath string
		var repoSubsysID *uint
		var repoSubsysName string

		if matched := idx.resolveRepoElement(repo); matched != nil {
			id := matched.ID
			repoArchElemID = &id
			repoArchElemName = archElementDisplayName(*matched)
			repoArchElemType = matched.Type
			repoArchPath, repoSubsysID, repoSubsysName = idx.resolveHierarchy(*matched)
		}
		if repoSubsysID == nil {
			if sg := strings.ToLower(strings.TrimSpace(repo.ServiceGroup)); sg != "" {
				if subEl, ok := idx.subsystemByKey[sg]; ok {
					id := subEl.ID
					repoSubsysID = &id
					repoSubsysName = archElementDisplayName(subEl)
				}
			}
		}

		repoSnap := latestSnapByScope[scopeKey{repoID: repo.ID, subdir: ""}]
		repoRow := buildSingleScopeRow(
			repo, branch, "", "repo",
			repoArchElemID, repoArchElemName, repoArchElemType, repoArchPath, repoSubsysID, repoSubsysName,
			repo.OwnerID, repoOwnerName, repoLevelWrite,
			docsByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			revMap, codeDriftByDoc, specDriftByDoc,
			latestCommitByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			remoteHeadByRepo[repo.ID],
			activeTaskByScope[scopeKey{repoID: repo.ID, subdir: ""}],
		)
		repoRow.DefaultBranch = defBranch
		repoRow.IsBranchOverridden = isOverridden
		repoRow.CanSwitchBranch = repoLevelWrite
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
		type subdirMeta struct {
			elID       *uint
			elName     string
			elType     string
			archPath   string
			subsysID   *uint
			subsysName string
			ownerID    uint
			ownerName  string
		}
		resolveSubdirMeta := func(normSub string) subdirMeta {
			meta := subdirMeta{
				subsysID:   repoSubsysID,
				subsysName: repoSubsysName,
				ownerID:    repo.OwnerID,
				ownerName:  repoOwnerName,
			}
			if el, ok := elemBySub[normSub]; ok {
				id := el.ID
				meta.elID = &id
				meta.elName = archElementDisplayName(el)
				meta.elType = el.Type
				var subID *uint
				var subName string
				meta.archPath, subID, subName = idx.resolveHierarchy(el)
				if subID != nil {
					meta.subsysID = subID
					meta.subsysName = subName
				}
				if el.OwnerID != nil && *el.OwnerID > 0 {
					meta.ownerID = *el.OwnerID
					meta.ownerName = idx.resolveUserName(el.OwnerID, meta.ownerName)
				}
			}
			return meta
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
				sm := resolveSubdirMeta(mod.Subdirectory)
				canWrite := repoLevelWrite || userOwnedSubdirs[mod.Subdirectory]
				k := scopeKey{repoID: repo.ID, subdir: mod.Subdirectory}
				row := buildSingleScopeRow(
					repo, branch, mod.Subdirectory, "subdir",
					sm.elID, sm.elName, sm.elType, sm.archPath, sm.subsysID, sm.subsysName,
					sm.ownerID, sm.ownerName, canWrite,
					docsByScope[k],
					revMap, codeDriftByDoc, specDriftByDoc,
					latestCommitByScope[k],
					remoteHeadByRepo[repo.ID],
					activeTaskByScope[k],
				)
				row.DefaultBranch = defBranch
				row.IsBranchOverridden = isOverridden
				row.CanSwitchBranch = repoLevelWrite
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
				sm := resolveSubdirMeta(normSub)
				canWrite := repoLevelWrite || userOwnedSubdirs[normSub]
				k := scopeKey{repoID: repo.ID, subdir: normSub}
				row := buildSingleScopeRow(
					repo, branch, normSub, "subdir",
					sm.elID, sm.elName, sm.elType, sm.archPath, sm.subsysID, sm.subsysName,
					sm.ownerID, sm.ownerName, canWrite,
					docsByScope[k],
					revMap, codeDriftByDoc, specDriftByDoc,
					latestCommitByScope[k],
					remoteHeadByRepo[repo.ID],
					activeTaskByScope[k],
				)
				row.DefaultBranch = defBranch
				row.IsBranchOverridden = isOverridden
				row.CanSwitchBranch = repoLevelWrite
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
			k := scopeKey{repoID: repo.ID, subdir: docSub}
			canWrite := repoLevelWrite || userOwnedSubdirs[docSub]
			sm := resolveSubdirMeta(docSub)
			row := buildSingleScopeRow(
				repo, branch, docSub, "subdir",
				sm.elID, sm.elName, sm.elType, sm.archPath, sm.subsysID, sm.subsysName,
				sm.ownerID, sm.ownerName, canWrite,
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
			row.DefaultBranch = defBranch
			row.IsBranchOverridden = isOverridden
			row.CanSwitchBranch = repoLevelWrite
			row.ScopeStatus = "orphan"
			rows = append(rows, row)
		}
	}

	rowsByScope := make(map[scopeKey]RepoScopeCoverageVO, len(rows))
	for _, r := range rows {
		k := scopeKey{repoID: r.RepoID, subdir: r.Subdirectory}
		if _, exists := rowsByScope[k]; !exists {
			rowsByScope[k] = r
		}
	}

	bctx := &coverageBuildContext{
		idx:                   idx,
		activeRepoByID:        activeRepoByID,
		rowsByScope:           rowsByScope,
		defaultBranchByRepo:   defaultBranchByRepo,
		effectiveBranchByRepo: effectiveBranchByRepo,
		overriddenByRepo:      overriddenByRepo,
		docsByScope:           docsByScope,
		revMap:                revMap,
		codeDriftByDoc:        codeDriftByDoc,
		specDriftByDoc:        specDriftByDoc,
		latestCommitByScope:   latestCommitByScope,
		latestSnapByScope:     latestSnapByScope,
		remoteHeadByRepo:      remoteHeadByRepo,
		activeTaskByScope:     activeTaskByScope,
		modulesByRepo:         modulesByRepo,
		userOwnedSubdirs:      userOwnedScopeKeys,
	}

	return bctx, rows, nil
}

func buildSingleScopeRow(
	repo *commonModels.Repository,
	branch string,
	subdir string,
	scopeType string,
	archElemID *uint,
	archElemName string,
	archElemType string,
	archPath string,
	subsystemID *uint,
	subsystemName string,
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
		ArchElementType: archElemType,
		ArchPath:        archPath,
		SubsystemID:     subsystemID,
		SubsystemName:   subsystemName,
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
