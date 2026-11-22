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

type ArchSubsystemOptionVO struct {
	ID         uint   `json:"id"`
	Identifier string `json:"identifier"`
	NameCn     string `json:"name_cn"`
	NameEn     string `json:"name_en"`
	ScopeCount int    `json:"scope_count"`
}

type ArchTreeStatsVO struct {
	ScopeCount        int `json:"scope_count"`
	CoveredScopeCount int `json:"covered_scope_count"`
	ApprovedDocCount  int `json:"approved_doc_count"`
	DraftDocCount     int `json:"draft_doc_count"`
	DriftedDocCount   int `json:"drifted_doc_count"`
	BlockedDocCount   int `json:"blocked_doc_count"`
}

type ArchTreeNodeVO struct {
	ID           uint                  `json:"id"`
	Identifier   string                `json:"identifier"`
	NameCn       string                `json:"name_cn"`
	NameEn       string                `json:"name_en"`
	Type         string                `json:"type"`
	ParentID     *uint                 `json:"parent_id"`
	OwnerID      *uint                 `json:"owner_id"`
	OwnerName    string                `json:"owner_name"`
	RepoID       *uint                 `json:"repo_id"`
	Subdirectory string                `json:"subdirectory"`
	Description  string                `json:"description"`
	Stats        ArchTreeStatsVO       `json:"stats"`
	Scopes       []RepoScopeCoverageVO `json:"scopes"`
	Children     []*ArchTreeNodeVO     `json:"children"`
}

type ArchTreeResponse struct {
	Summary          RepoCoverageSummaryVO   `json:"summary"`
	Subsystems       []ArchSubsystemOptionVO `json:"subsystems"`
	Nodes            []*ArchTreeNodeVO       `json:"nodes"`
	UnassignedScopes []RepoScopeCoverageVO   `json:"unassigned_scopes"`
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
	ArchBinding  string // all | bound | unbound
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

	allRows, idx, err := s.buildCoverageData(ctx, user, repos)
	if err != nil {
		return nil, err
	}

	subsystems := idx.buildSubsystems(allRows)
	filtered := filterCoverageRows(allRows, filter)

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

// GetArchTree 按平台 architecture_elements 树形结构聚合各节点下的仓库/子目录覆盖范围与统计指标。
func (s *RepoService) GetArchTree(ctx context.Context, user *commonModels.User, filter RepoCoverageFilter) (*ArchTreeResponse, error) {
	repos, err := s.masterRepo.ListAllActiveRepositories(ctx, filter.DepartmentID)
	if err != nil {
		return nil, err
	}

	allRows, idx, err := s.buildCoverageData(ctx, user, repos)
	if err != nil {
		return nil, err
	}

	subsystems := idx.buildSubsystems(allRows)
	filtered := filterCoverageRows(allRows, filter)
	summary := computeCoverageSummary(filtered)

	// 预先记录每个仓库整仓应挂接的目标节点 ID（兼容仅筛选 scope_type=subdir 时子目录仍能挂入父仓所属节点）
	repoAttachNodeID := make(map[uint]uint, len(repos))
	for _, r := range allRows {
		if r.ScopeType != "repo" {
			continue
		}
		if r.ArchElementID != nil {
			repoAttachNodeID[r.RepoID] = *r.ArchElementID
		} else if r.SubsystemID != nil {
			repoAttachNodeID[r.RepoID] = *r.SubsystemID
		}
	}

	nodeByID := make(map[uint]*ArchTreeNodeVO, len(idx.allElems))
	for _, el := range idx.allElems {
		nodeByID[el.ID] = &ArchTreeNodeVO{
			ID:           el.ID,
			Identifier:   el.Identifier,
			NameCn:       el.NameCn,
			NameEn:       el.NameEn,
			Type:         el.Type,
			ParentID:     el.ParentID,
			OwnerID:      el.OwnerID,
			OwnerName:    idx.resolveUserName(el.OwnerID, ""),
			RepoID:       el.RepoID,
			Subdirectory: el.Subdirectory,
			Description:  el.Description,
			Scopes:       []RepoScopeCoverageVO{},
			Children:     []*ArchTreeNodeVO{},
		}
	}

	roots := make([]*ArchTreeNodeVO, 0)
	for _, el := range idx.allElems {
		node := nodeByID[el.ID]
		if el.ParentID != nil && *el.ParentID != el.ID {
			if parent, ok := nodeByID[*el.ParentID]; ok && !wouldCreateTreeCycle(el.ID, *el.ParentID, idx.elemByID) {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	unassigned := make([]RepoScopeCoverageVO, 0)
	for _, row := range filtered {
		attached := false
		if row.ArchElementID != nil {
			if node, ok := nodeByID[*row.ArchElementID]; ok {
				node.Scopes = append(node.Scopes, row)
				attached = true
			}
		}
		if !attached && row.ScopeType == "subdir" {
			if targetID, ok := repoAttachNodeID[row.RepoID]; ok {
				if node, exists := nodeByID[targetID]; exists {
					node.Scopes = append(node.Scopes, row)
					attached = true
				}
			}
		}
		if !attached && row.SubsystemID != nil {
			if node, ok := nodeByID[*row.SubsystemID]; ok {
				node.Scopes = append(node.Scopes, row)
				attached = true
			}
		}
		if !attached {
			unassigned = append(unassigned, row)
		}
	}

	for _, root := range roots {
		aggregateArchTreeStats(root)
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

	if hasAnyFilter {
		pruned := make([]*ArchTreeNodeVO, 0, len(roots))
		for _, root := range roots {
			if kept := pruneArchTreeNode(root, idx, filter, searchLower, !hasNonSearchRestriction); kept != nil {
				pruned = append(pruned, kept)
			}
		}
		roots = pruned
	}

	return &ArchTreeResponse{
		Summary:          summary,
		Subsystems:       subsystems,
		Nodes:            roots,
		UnassignedScopes: unassigned,
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

func aggregateArchTreeStats(node *ArchTreeNodeVO) ArchTreeStatsVO {
	var stats ArchTreeStatsVO
	for _, sc := range node.Scopes {
		stats.ScopeCount++
		if len(sc.DocStatuses) > 0 {
			stats.CoveredScopeCount++
		}
		for _, ds := range sc.DocStatuses {
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
	for _, child := range node.Children {
		cs := aggregateArchTreeStats(child)
		stats.ScopeCount += cs.ScopeCount
		stats.CoveredScopeCount += cs.CoveredScopeCount
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
	idx *archIndex,
	filter RepoCoverageFilter,
	searchLower string,
	allowSearchOnlyEmpty bool,
) *ArchTreeNodeVO {
	keptChildren := make([]*ArchTreeNodeVO, 0, len(node.Children))
	for _, child := range node.Children {
		if kept := pruneArchTreeNode(child, idx, filter, searchLower, allowSearchOnlyEmpty); kept != nil {
			keptChildren = append(keptChildren, kept)
		}
	}
	node.Children = keptChildren

	if len(keptChildren) > 0 || node.Stats.ScopeCount > 0 {
		return node
	}

	if allowSearchOnlyEmpty && searchLower != "" && archNodeMatchesSearch(node, searchLower) {
		if filter.SubsystemID == nil {
			return node
		}
		if el, ok := idx.elemByID[node.ID]; ok {
			_, subsysID, _ := idx.resolveHierarchy(el)
			if subsysID != nil && *subsysID == *filter.SubsystemID {
				return node
			}
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

func (s *RepoService) buildCoverageRows(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) ([]RepoScopeCoverageVO, error) {
	rows, _, err := s.buildCoverageData(ctx, user, repos)
	return rows, err
}

func (s *RepoService) buildCoverageData(ctx context.Context, user *commonModels.User, repos []commonModels.Repository) ([]RepoScopeCoverageVO, *archIndex, error) {
	idx, err := s.loadArchIndex(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(repos) == 0 {
		return []RepoScopeCoverageVO{}, idx, nil
	}

	repoIDs := make([]uint, 0, len(repos))
	for _, r := range repos {
		repoIDs = append(repoIDs, r.ID)
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

	return rows, idx, nil
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
