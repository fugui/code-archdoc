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
	RepoID          uint                          `json:"repo_id"`
	RepoName        string                        `json:"repo_name"`
	DepartmentID    uint                          `json:"department_id"`
	DepartmentName  string                        `json:"department_name"`
	Subdirectory    string                        `json:"subdirectory"`
	ScopeType       string                        `json:"scope_type"` // "repo" | "subdir"
	ArchElementID   *uint                         `json:"arch_element_id"`
	ArchElementName string                        `json:"arch_element_name"`
	Branch          string                        `json:"branch"`
	OwnerID         uint                          `json:"owner_id"`
	OwnerName       string                        `json:"owner_name"`
	CanWrite        bool                          `json:"can_write"`
	LatestCommitSHA string                        `json:"latest_commit_sha"`
	RemoteHeadSHA   string                        `json:"remote_head_sha"`
	ActiveTaskID    *uint                         `json:"active_task_id"`
	DocStatuses     map[string]DocStatusSummaryVO `json:"doc_statuses"`
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
	rbacSvc    *RBACService
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

	// 按仓库分组架构元素，并按规范化子目录聚合文档
	elemsByRepo := make(map[uint][]models.ArchitectureElement)
	for _, el := range elems {
		if el.RepoID != nil {
			elemsByRepo[*el.RepoID] = append(elemsByRepo[*el.RepoID], el)
		}
	}
	docsByScope := make(map[scopeKey][]models.Document)
	for _, d := range docs {
		k := scopeKey{repoID: d.RepoID, subdir: d.Subdirectory}
		docsByScope[k] = append(docsByScope[k], d)
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

		// 1. 整仓范围行 (subdirectory == "")
		rows = append(rows, buildSingleScopeRow(
			repo, branch, "", "repo", nil, "",
			repo.OwnerID, repoOwnerName, repoLevelWrite,
			docsByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			revMap, codeDriftByDoc, specDriftByDoc,
			latestCommitByScope[scopeKey{repoID: repo.ID, subdir: ""}],
			remoteHeadByRepo[repo.ID],
			activeTaskByScope[scopeKey{repoID: repo.ID, subdir: ""}],
		))

		// 2. Monorepo 子目录范围行（按规范化子目录去重，严格核算子目录负责人权限）
		repoElems := elemsByRepo[repo.ID]
		// 先收集当前用户在该仓库下负责的所有子目录集合
		userOwnedSubdirs := make(map[string]bool)
		if user != nil && user.ID > 0 {
			for _, el := range repoElems {
				if el.OwnerID != nil && *el.OwnerID == user.ID {
					if ns, normErr := sandbox.NormalizeSubdir(el.Subdirectory); normErr == nil && ns != "" {
						userOwnedSubdirs[ns] = true
					}
				}
			}
		}

		seenSubdirs := make(map[string]bool)
		for _, el := range repoElems {
			normSub, normErr := sandbox.NormalizeSubdir(el.Subdirectory)
			if normErr != nil || normSub == "" || seenSubdirs[normSub] {
				continue
			}
			seenSubdirs[normSub] = true

			elID := el.ID
			elName := el.NameCn
			if elName == "" {
				elName = el.NameEn
			}
			scopeOwnerID := repo.OwnerID
			scopeOwnerName := repoOwnerName
			if el.OwnerID != nil && *el.OwnerID > 0 {
				scopeOwnerID = *el.OwnerID
				if u, ok := elemUsers[*el.OwnerID]; ok {
					if u.Name != "" {
						scopeOwnerName = u.Name
					} else {
						scopeOwnerName = u.Username
					}
				}
			}

			canWrite := repoLevelWrite || userOwnedSubdirs[normSub]
			k := scopeKey{repoID: repo.ID, subdir: normSub}
			rows = append(rows, buildSingleScopeRow(
				repo, branch, normSub, "subdir", &elID, elName,
				scopeOwnerID, scopeOwnerName, canWrite,
				docsByScope[k],
				revMap, codeDriftByDoc, specDriftByDoc,
				latestCommitByScope[k],
				remoteHeadByRepo[repo.ID],
				activeTaskByScope[k],
			))
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
