package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"code-archdoc/models"
	"code-common/backend/gitsync"
)

const defaultBranchCacheTTL = 30 * time.Second

var (
	// ErrInvalidBranch 目标分支名称格式非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidBranch = errors.New("目标分支名称非法")
	// ErrBranchNotFound 远端代码仓不存在目标分支（HTTP 400 INVALID_PARAM）。
	ErrBranchNotFound = errors.New("远端仓库不存在指定分支")
	// ErrRemoteBranchFetchFailed 远端 Git 分支查询或校验失败（HTTP 502 REMOTE_BRANCH_FETCH_FAILED）。
	ErrRemoteBranchFetchFailed = errors.New("查询远端仓库分支失败")
	// ErrActiveTasksBlockingBranchSwitch 目标仓库存在活跃生成任务，阻断分支切换（HTTP 409 CONFLICT）。
	ErrActiveTasksBlockingBranchSwitch = errors.New("当前仓库存在进行中的生成或刷新任务，请等待完成后再切换分支")
)

// RemoteBranchFetcher 抽象远端分支枚举与单分支 HEAD 查询（默认复用 code-common/backend/gitsync）。
type RemoteBranchFetcher interface {
	ListRemoteBranches(ctx context.Context, repoURL string) (branches []gitsync.RemoteBranch, headBranch string, err error)
	LsRemoteHead(ctx context.Context, repoURL, branch string) (commitSHA string, resolvedBranch string, err error)
}

// DefaultRemoteBranchFetcher 生产默认实现：零对象克隆执行 git ls-remote。
type DefaultRemoteBranchFetcher struct{}

func (DefaultRemoteBranchFetcher) ListRemoteBranches(ctx context.Context, repoURL string) ([]gitsync.RemoteBranch, string, error) {
	return gitsync.ListRemoteBranches(ctx, repoURL)
}

func (DefaultRemoteBranchFetcher) LsRemoteHead(ctx context.Context, repoURL, branch string) (string, string, error) {
	return gitsync.LsRemoteHead(ctx, repoURL, branch)
}

// RepoBranchItemVO 对应单个远端分支选项视图。
type RepoBranchItemVO struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commit_sha"`
	IsHead    bool   `json:"is_head"`
	IsDefault bool   `json:"is_default"`
	IsCurrent bool   `json:"is_current"`
}

// RepoBranchesVO 对应 GET /api/repos/:repo_id/branches 响应。
type RepoBranchesVO struct {
	RepoID           uint               `json:"repo_id"`
	DefaultBranch    string             `json:"default_branch"`
	CurrentBranch    string             `json:"current_branch"`
	IsOverridden     bool               `json:"is_overridden"`
	RemoteHeadBranch string             `json:"remote_head_branch"`
	Branches         []RepoBranchItemVO `json:"branches"`
}

// SwitchRepoBranchRequest 对应 PUT /api/repos/:repo_id/branch 入参。
type SwitchRepoBranchRequest struct {
	Branch string `json:"branch"`
}

// SwitchRepoBranchVO 对应 PUT /api/repos/:repo_id/branch 响应。
type SwitchRepoBranchVO struct {
	RepoID           uint   `json:"repo_id"`
	PreviousBranch   string `json:"previous_branch"`
	CurrentBranch    string `json:"current_branch"`
	DefaultBranch    string `json:"default_branch"`
	IsOverridden     bool   `json:"is_overridden"`
	RemoteHeadSHA    string `json:"remote_head_sha"`
	ReboundDocuments int    `json:"rebound_documents"`
	DriftedDocuments int    `json:"drifted_documents"`
	Changed          bool   `json:"changed"`
}

type cachedBranchList struct {
	repoURL    string
	headBranch string
	branches   []gitsync.RemoteBranch
	fetchedAt  time.Time
}

// BranchService 编排单仓远端分支轻量发现（30s TTL 缓存）、目标分支无损重绑及远端 HEAD/漂移同步（ADR-07）。
type BranchService struct {
	masterRepo SharedMasterRepo
	moduleRepo ModuleRepo
	docRepo    DocumentRepo
	taskRepo   TaskRepo
	driftRepo  DriftRepo
	driftSvc   *DriftOrchestrator
	fetcher    RemoteBranchFetcher
	cacheTTL   time.Duration

	mu    sync.Mutex
	cache map[uint]cachedBranchList
}

// NewBranchService 构造单仓分支治理服务。
func NewBranchService(
	masterRepo SharedMasterRepo,
	moduleRepo ModuleRepo,
	docRepo DocumentRepo,
	taskRepo TaskRepo,
	driftRepo DriftRepo,
	driftSvc *DriftOrchestrator,
	fetcher RemoteBranchFetcher,
) *BranchService {
	if fetcher == nil {
		fetcher = DefaultRemoteBranchFetcher{}
	}
	return &BranchService{
		masterRepo: masterRepo,
		moduleRepo: moduleRepo,
		docRepo:    docRepo,
		taskRepo:   taskRepo,
		driftRepo:  driftRepo,
		driftSvc:   driftSvc,
		fetcher:    fetcher,
		cacheTTL:   defaultBranchCacheTTL,
		cache:      make(map[uint]cachedBranchList),
	}
}

// ListRepoBranches 轻量列举远端分支清单并标记仓库默认分支与当前生效分支（30s TTL 缓存，支持 forceRefresh）。
func (s *BranchService) ListRepoBranches(ctx context.Context, repoID uint, forceRefresh bool) (*RepoBranchesVO, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	defaultBranch := models.NormalizeRepoDefaultBranch(repo.Branch)
	storedTarget := ""
	if s.moduleRepo != nil {
		if policy, pErr := s.moduleRepo.GetPolicy(ctx, repoID); pErr == nil && policy != nil {
			storedTarget = strings.TrimSpace(policy.TargetBranch)
		}
	}
	currentBranch := models.ResolveEffectiveBranch(repo.Branch, storedTarget)
	isOverridden := storedTarget != "" && storedTarget != defaultBranch

	var (
		rawBranches []gitsync.RemoteBranch
		headBranch  string
		cacheHit    bool
	)

	if !forceRefresh {
		s.mu.Lock()
		if entry, ok := s.cache[repoID]; ok && entry.repoURL == repo.URL && time.Since(entry.fetchedAt) < s.cacheTTL {
			rawBranches = append([]gitsync.RemoteBranch(nil), entry.branches...)
			headBranch = entry.headBranch
			cacheHit = true
		}
		s.mu.Unlock()
	}

	if !cacheHit {
		fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		listed, hb, listErr := s.fetcher.ListRemoteBranches(fetchCtx, repo.URL)
		if listErr != nil {
			return nil, fmt.Errorf("%w: %s", ErrRemoteBranchFetchFailed, gitsync.SanitizeGitOutput(listErr.Error()))
		}
		rawBranches = listed
		headBranch = hb

		s.mu.Lock()
		s.cache[repoID] = cachedBranchList{
			repoURL:    repo.URL,
			headBranch: hb,
			branches:   append([]gitsync.RemoteBranch(nil), listed...),
			fetchedAt:  time.Now(),
		}
		s.mu.Unlock()
	}

	items := make([]RepoBranchItemVO, 0, len(rawBranches))
	for _, rb := range rawBranches {
		items = append(items, RepoBranchItemVO{
			Name:      rb.Name,
			CommitSHA: rb.CommitSHA,
			IsHead:    rb.IsHead,
			IsDefault: rb.Name == defaultBranch,
			IsCurrent: rb.Name == currentBranch,
		})
	}

	return &RepoBranchesVO{
		RepoID:           repo.ID,
		DefaultBranch:    defaultBranch,
		CurrentBranch:    currentBranch,
		IsOverridden:     isOverridden,
		RemoteHeadBranch: headBranch,
		Branches:         items,
	}, nil
}

// SwitchRepoBranch 校验分支安全性与远端存在性后，原子持久化 target_branch、无损重绑存量文档并即时对齐远端 HEAD 与代码漂移。
func (s *BranchService) SwitchRepoBranch(
	ctx context.Context,
	repoID uint,
	req SwitchRepoBranchRequest,
	operatorID uint,
) (*SwitchRepoBranchVO, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	reqBranch := strings.TrimSpace(req.Branch)
	if reqBranch != "" {
		if valErr := gitsync.ValidateBranchName(reqBranch); valErr != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidBranch, valErr.Error())
		}
	}

	defaultBranch := models.NormalizeRepoDefaultBranch(repo.Branch)
	prevStoredTarget := ""
	if s.moduleRepo != nil {
		if policy, pErr := s.moduleRepo.GetPolicy(ctx, repoID); pErr == nil && policy != nil {
			prevStoredTarget = strings.TrimSpace(policy.TargetBranch)
		}
	}
	prevEffective := models.ResolveEffectiveBranch(repo.Branch, prevStoredTarget)

	requestedBranch := reqBranch
	if requestedBranch == "" {
		requestedBranch = defaultBranch
	}

	if s.taskRepo != nil {
		hasActive, tErr := s.taskRepo.HasActiveTasksInRepo(ctx, repoID)
		if tErr != nil {
			return nil, tErr
		}
		if hasActive {
			return nil, ErrActiveTasksBlockingBranchSwitch
		}
	}

	pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	remoteHeadSHA, resolvedBranch, lsErr := s.fetcher.LsRemoteHead(pollCtx, repo.URL, requestedBranch)
	if lsErr != nil {
		if strings.Contains(lsErr.Error(), "远端仓库未找到分支") {
			return nil, fmt.Errorf("%w: %s", ErrBranchNotFound, requestedBranch)
		}
		return nil, fmt.Errorf("%w: %s", ErrRemoteBranchFetchFailed, gitsync.SanitizeGitOutput(lsErr.Error()))
	}

	effectiveBranch := strings.TrimSpace(resolvedBranch)
	if effectiveBranch == "" {
		effectiveBranch = requestedBranch
	}

	// 若用户恢复默认分支（入参为空或显式选回仓库默认分支），持久化 target_branch="" 维持跟随态
	storedTargetBranch := effectiveBranch
	if reqBranch == "" || reqBranch == defaultBranch {
		storedTargetBranch = ""
	}
	isOverridden := storedTargetBranch != "" && storedTargetBranch != defaultBranch
	if !isOverridden {
		storedTargetBranch = ""
	}

	changed := effectiveBranch != prevEffective || storedTargetBranch != prevStoredTarget

	reboundCount := 0
	if s.docRepo != nil {
		reboundDocs, rErr := s.docRepo.RebindRepoBranch(ctx, repoID, effectiveBranch)
		if rErr != nil {
			return nil, rErr
		}
		reboundCount = len(reboundDocs)
	}

	var updatedByPtr *uint
	if operatorID > 0 {
		uid := operatorID
		updatedByPtr = &uid
	}
	if s.moduleRepo != nil {
		if uErr := s.moduleRepo.UpdateTargetBranch(ctx, repoID, storedTargetBranch, updatedByPtr); uErr != nil {
			return nil, fmt.Errorf("保存目标分支配置失败: %w", uErr)
		}
	}

	if s.driftRepo != nil {
		now := time.Now()
		if hErr := s.driftRepo.ReplaceRepoRemoteHead(ctx, &models.RepoRemoteHead{
			RepoID:              repoID,
			RefName:             effectiveBranch,
			RemoteHeadSHA:       remoteHeadSHA,
			LastPolledAt:        &now,
			LastSuccessAt:       &now,
			ConsecutiveFailures: 0,
			LastError:           "",
			NextRetryAt:         nil,
		}); hErr != nil {
			return nil, fmt.Errorf("更新远端 HEAD 记录失败: %w", hErr)
		}
	}

	driftedDocs := 0
	if s.driftSvc != nil {
		driftedDocs = s.driftSvc.SyncCodeDriftAfterBranchSwitch(ctx, repoID, effectiveBranch, remoteHeadSHA)
	}

	s.mu.Lock()
	delete(s.cache, repoID)
	s.mu.Unlock()

	return &SwitchRepoBranchVO{
		RepoID:           repoID,
		PreviousBranch:   prevEffective,
		CurrentBranch:    effectiveBranch,
		DefaultBranch:    defaultBranch,
		IsOverridden:     isOverridden,
		RemoteHeadSHA:    remoteHeadSHA,
		ReboundDocuments: reboundCount,
		DriftedDocuments: driftedDocs,
		Changed:          changed,
	}, nil
}
