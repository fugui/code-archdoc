package services

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-common/backend/gitsync"
)

// RemoteHeadFetcher 抽象远端 HEAD 轻量查询（默认复用 code-common/backend/gitsync.LsRemoteHead）。
type RemoteHeadFetcher interface {
	LsRemoteHead(ctx context.Context, repoURL, branch string) (commitSHA string, resolvedBranch string, err error)
}

// DefaultRemoteHeadFetcher 生产默认实现：零对象克隆执行 git ls-remote。
type DefaultRemoteHeadFetcher struct{}

func (DefaultRemoteHeadFetcher) LsRemoteHead(ctx context.Context, repoURL, branch string) (string, string, error) {
	return gitsync.LsRemoteHead(ctx, repoURL, branch)
}

// PollerConfig 定义 F-25 后台远端变更轻量巡检配置。
type PollerConfig struct {
	Enabled      bool
	PollInterval time.Duration // 默认 15 分钟
	Concurrency  int           // 独立有界并发信号量，默认 4
	RepoTimeout  time.Duration // 单仓 ls-remote 超时，默认 20 秒
}

// PollRunResult 汇总单轮远端巡检的执行结果。
type PollRunResult struct {
	MonitoredRepos int `json:"monitored_repos"`
	CheckedRepos   int `json:"checked_repos"`
	SkippedBackoff int `json:"skipped_backoff"`
	SucceededRepos int `json:"succeeded_repos"`
	FailedRepos    int `json:"failed_repos"`
}

// BackoffDuration 按 F-25 规范计算失败指数退避间隔：15m -> 30m -> 1h -> 2h -> 4h -> 6h（上限 6 小时）。
func BackoffDuration(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 1 {
		return 15 * time.Minute
	}
	steps := []time.Duration{
		15 * time.Minute,
		30 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		4 * time.Hour,
		6 * time.Hour,
	}
	idx := consecutiveFailures - 1
	if idx >= len(steps) {
		return 6 * time.Hour
	}
	return steps[idx]
}

// RemoteHeadPoller 后台定时仅对含已发布文档的活跃仓库执行有界并发、零对象克隆的 git ls-remote 巡检（F-25）。
type RemoteHeadPoller struct {
	cfg             PollerConfig
	driftRepo       DriftRepo
	fetcher         RemoteHeadFetcher
	onHeadUpdated   func(ctx context.Context, repo models.PollableRepoRecord, remoteHeadSHA, resolvedBranch string)
	onCycleComplete func(ctx context.Context)

	mu       sync.Mutex
	started  bool
	loopCtx  context.Context
	stopFunc context.CancelFunc
	wg       sync.WaitGroup
}

// NewRemoteHeadPoller 构造远端变更巡检调度器。
func NewRemoteHeadPoller(
	cfg PollerConfig,
	driftRepo DriftRepo,
	fetcher RemoteHeadFetcher,
	onHeadUpdated func(ctx context.Context, repo models.PollableRepoRecord, remoteHeadSHA, resolvedBranch string),
	onCycleComplete func(ctx context.Context),
) *RemoteHeadPoller {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 15 * time.Minute
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.RepoTimeout <= 0 {
		cfg.RepoTimeout = 20 * time.Second
	}
	if fetcher == nil {
		fetcher = DefaultRemoteHeadFetcher{}
	}
	return &RemoteHeadPoller{
		cfg:             cfg,
		driftRepo:       driftRepo,
		fetcher:         fetcher,
		onHeadUpdated:   onHeadUpdated,
		onCycleComplete: onCycleComplete,
	}
}

// Start 启动后台周期巡检协程；支持随 parentCtx 取消或 Stop() 干净退出（AC-M6-03）。
func (p *RemoteHeadPoller) Start(parentCtx context.Context) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.loopCtx, p.stopFunc = context.WithCancel(parentCtx)
	enabled := p.cfg.Enabled
	interval := p.cfg.PollInterval
	ctx := p.loopCtx
	p.mu.Unlock()

	if !enabled {
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := p.PollOnce(ctx, false); err != nil && ctx.Err() == nil {
					log.Printf("[%s] 后台远端变更巡检执行异常: %v", config.ServiceName, err)
				}
			}
		}
	}()
}

// Stop 取消后台巡检并等待所有巡检协程与手动触发任务干净退出，零 goroutine 泄漏（AC-M6-03）。
func (p *RemoteHeadPoller) Stop() {
	p.mu.Lock()
	cancel := p.stopFunc
	p.started = false
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	p.wg.Wait()
}

// TriggerPollAsync 供管理员 POST /api/admin/drift/poll 立即异步启动新一轮全量强制巡检（忽略退避窗口）。
func (p *RemoteHeadPoller) TriggerPollAsync(ctx context.Context) (int64, error) {
	repos, err := p.driftRepo.ListPollableRepos(ctx)
	if err != nil {
		return 0, err
	}

	p.mu.Lock()
	runCtx := p.loopCtx
	if runCtx == nil {
		runCtx, p.stopFunc = context.WithCancel(context.Background())
		p.loopCtx = runCtx
	}
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if _, pollErr := p.PollOnce(runCtx, true); pollErr != nil && runCtx.Err() == nil {
			log.Printf("[%s] 手动触发远端巡检异常: %v", config.ServiceName, pollErr)
		}
	}()

	return int64(len(repos)), nil
}

// PollOnce 同步执行一轮远端变更轻量巡检：
// - 仅查询含至少 1 篇已发布文档的活跃仓库；
// - 独立有界并发信号量（默认 4）控制并发，单仓失败指数退避绝不阻塞其他健康仓库；
// - 成功后更新 archdoc_repo_remote_heads 并触发 Code Drift 计算；周期末尾触发 Spec Drift 兜底重算。
func (p *RemoteHeadPoller) PollOnce(ctx context.Context, force bool) (*PollRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.driftRepo == nil {
		return &PollRunResult{}, nil
	}
	repos, err := p.driftRepo.ListPollableRepos(ctx)
	if err != nil {
		return nil, err
	}

	result := &PollRunResult{
		MonitoredRepos: len(repos),
	}
	if len(repos) == 0 {
		if p.onCycleComplete != nil && ctx.Err() == nil {
			p.onCycleComplete(ctx)
		}
		return result, nil
	}

	repoIDs := make([]uint, 0, len(repos))
	for _, r := range repos {
		repoIDs = append(repoIDs, r.RepoID)
	}
	existingHeads, err := p.driftRepo.ListRemoteHeadsByRepos(ctx, repoIDs)
	if err != nil {
		return nil, err
	}
	headByRepo := make(map[uint]models.RepoRemoteHead, len(existingHeads))
	for _, h := range existingHeads {
		headByRepo[h.RepoID] = h
	}

	sem := make(chan struct{}, p.cfg.Concurrency)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)

	now := time.Now()
	for _, repo := range repos {
		if ctx.Err() != nil {
			break
		}

		prevHead, hasPrev := headByRepo[repo.RepoID]
		if !force && hasPrev && prevHead.NextRetryAt != nil && now.Before(*prevHead.NextRetryAt) {
			mu.Lock()
			result.SkippedBackoff++
			mu.Unlock()
			continue
		}

		select {
		case <-ctx.Done():
			break
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			<-sem
			break
		}

		repoCopy := repo
		prevCopy := prevHead
		hasPrevCopy := hasPrev
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			ok := p.pollSingleRepo(ctx, repoCopy, prevCopy, hasPrevCopy)
			mu.Lock()
			result.CheckedRepos++
			if ok {
				result.SucceededRepos++
			} else {
				result.FailedRepos++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if p.onCycleComplete != nil && ctx.Err() == nil {
		p.onCycleComplete(ctx)
	}

	return result, nil
}

func (p *RemoteHeadPoller) pollSingleRepo(
	ctx context.Context,
	repo models.PollableRepoRecord,
	prev models.RepoRemoteHead,
	hasPrev bool,
) bool {
	branch := strings.TrimSpace(repo.Branch)
	if branch == "" {
		branch = "master"
	}

	pollCtx, cancel := context.WithTimeout(ctx, p.cfg.RepoTimeout)
	defer cancel()

	sha, resolvedBranch, err := p.fetcher.LsRemoteHead(pollCtx, repo.RepoURL, branch)
	now := time.Now()

	if err != nil {
		failures := 1
		if hasPrev {
			failures = prev.ConsecutiveFailures + 1
		}
		nextRetry := now.Add(BackoffDuration(failures))
		refName := branch
		if hasPrev && strings.TrimSpace(prev.RefName) != "" {
			refName = prev.RefName
		}
		headRec := &models.RepoRemoteHead{
			RepoID:              repo.RepoID,
			RefName:             refName,
			RemoteHeadSHA:       prev.RemoteHeadSHA,
			LastPolledAt:        &now,
			LastSuccessAt:       prev.LastSuccessAt,
			ConsecutiveFailures: failures,
			LastError:           err.Error(),
			NextRetryAt:         &nextRetry,
		}
		if saveErr := p.driftRepo.UpsertRemoteHead(ctx, headRec); saveErr != nil {
			log.Printf("[%s] 保存仓库 #%d 巡检失败退避状态异常: %v", config.ServiceName, repo.RepoID, saveErr)
		}
		return false
	}

	if strings.TrimSpace(resolvedBranch) == "" {
		resolvedBranch = branch
	}
	headRec := &models.RepoRemoteHead{
		RepoID:              repo.RepoID,
		RefName:             resolvedBranch,
		RemoteHeadSHA:       sha,
		LastPolledAt:        &now,
		LastSuccessAt:       &now,
		ConsecutiveFailures: 0,
		LastError:           "",
		NextRetryAt:         nil,
	}
	if saveErr := p.driftRepo.UpsertRemoteHead(ctx, headRec); saveErr != nil {
		log.Printf("[%s] 保存仓库 #%d 远端 HEAD 失败: %v", config.ServiceName, repo.RepoID, saveErr)
		return false
	}

	if p.onHeadUpdated != nil && ctx.Err() == nil {
		p.onHeadUpdated(ctx, repo, sha, resolvedBranch)
	}
	return true
}
