package services

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
)

const (
	defaultReapHighWatermark = 0.90
	defaultReapLowWatermark  = 0.75
	draftPriorityAgeWindow   = 24 * time.Hour
)

// ReaperConfig 定义沙箱与 Code IR 缓存配额及周期回收配置（F-27）。
type ReaperConfig struct {
	SandboxDir      string
	IRCacheDir      string
	PublishDir      string
	SandboxMaxBytes int64 // 默认 20 GB
	IRCacheMaxBytes int64 // 默认 10 GB
	ReapInterval    time.Duration
	HighWatermark   float64 // 默认 0.90
	LowWatermark    float64 // 默认 0.75
}

// ReapResultVO 对应 POST /api/admin/storage/reap 响应（01-http-api-contract.md §3.10）。
type ReapResultVO struct {
	ReapedSandboxes     int   `json:"reaped_sandboxes"`
	ReapedIRFacets      int   `json:"reaped_ir_facets"`
	FreedBytes          int64 `json:"freed_bytes"`
	SkippedActiveLeases int   `json:"skipped_active_leases"`
}

// StorageStatsVO 对应 GET /api/admin/storage/stats 响应（01-http-api-contract.md §3.10）。
type StorageStatsVO struct {
	Sandbox      SandboxStorageStatVO `json:"sandbox"`
	IRCache      IRCacheStorageStatVO `json:"ir_cache"`
	PublishDir   PublishStorageStatVO `json:"publish_dir"`
	Scheduler    SchedulerStatsVO     `json:"scheduler"`
	UsageLast30D []UsageSummaryVO     `json:"usage_last_30d"`
}

type SandboxStorageStatVO struct {
	UsedMB       int64 `json:"used_mb"`
	MaxMB        int64 `json:"max_mb"`
	RepoCount    int   `json:"repo_count"`
	ActiveLeases int   `json:"active_leases"`
}

type IRCacheStorageStatVO struct {
	UsedMB       int64 `json:"used_mb"`
	MaxMB        int64 `json:"max_mb"`
	FacetCount   int64 `json:"facet_count"`
	EvictedCount int64 `json:"evicted_count"`
}

type PublishStorageStatVO struct {
	UsedMB         int64 `json:"used_mb"`
	PublishedFiles int   `json:"published_files"`
}

type UsageSummaryVO struct {
	ModelTier          string `json:"model_tier"`
	DocTypeSlug        string `json:"doc_type_slug"`
	TotalTokens        int64  `json:"total_tokens"`
	CachedPromptTokens int64  `json:"cached_prompt_tokens"`
	CallCount          int64  `json:"call_count"`
}

// ResourceReaper 实现受 ActiveLeaseManager 硬保护的双级 LRU 磁盘配额回收与存储/成本观测统计（F-27）。
type ResourceReaper struct {
	cfg       ReaperConfig
	snapRepo  SnapshotRepo
	govRepo   GovernanceRepo
	leaseMgr  *ActiveLeaseManager
	scheduler *TaskScheduler

	reapMu   sync.Mutex
	lifeMu   sync.Mutex
	started  bool
	stopFunc context.CancelFunc
	wg       sync.WaitGroup
}

// NewResourceReaper 构造磁盘配额回收与统计服务。
func NewResourceReaper(
	cfg ReaperConfig,
	snapRepo SnapshotRepo,
	govRepo GovernanceRepo,
	leaseMgr *ActiveLeaseManager,
	scheduler *TaskScheduler,
) *ResourceReaper {
	if cfg.SandboxMaxBytes <= 0 {
		cfg.SandboxMaxBytes = 20480 * 1024 * 1024
	}
	if cfg.IRCacheMaxBytes <= 0 {
		cfg.IRCacheMaxBytes = 10240 * 1024 * 1024
	}
	if cfg.ReapInterval <= 0 {
		cfg.ReapInterval = 10 * time.Minute
	}
	if cfg.HighWatermark <= 0 || cfg.HighWatermark > 1 {
		cfg.HighWatermark = defaultReapHighWatermark
	}
	if cfg.LowWatermark <= 0 || cfg.LowWatermark >= cfg.HighWatermark {
		cfg.LowWatermark = defaultReapLowWatermark
	}
	if leaseMgr == nil {
		leaseMgr = NewActiveLeaseManager()
	}
	return &ResourceReaper{
		cfg:       cfg,
		snapRepo:  snapRepo,
		govRepo:   govRepo,
		leaseMgr:  leaseMgr,
		scheduler: scheduler,
	}
}

// Start 启动后台周期水位监控与 LRU 回收协程；当 ctx 取消或调用 Stop() 时干净退出。
func (r *ResourceReaper) Start(parentCtx context.Context) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	r.lifeMu.Lock()
	if r.started {
		r.lifeMu.Unlock()
		return
	}
	r.started = true
	loopCtx, cancel := context.WithCancel(parentCtx)
	r.stopFunc = cancel
	r.lifeMu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.cfg.ReapInterval)
		defer ticker.Stop()

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if _, err := r.Reap(loopCtx); err != nil && loopCtx.Err() == nil {
					log.Printf("[%s] 后台 LRU 磁盘配额回收执行异常: %v", config.ServiceName, err)
				}
			}
		}
	}()
}

// Stop 优雅停止后台回收协程并等待退出（AC-M6-03）。
func (r *ResourceReaper) Stop() {
	r.lifeMu.Lock()
	cancel := r.stopFunc
	r.started = false
	r.lifeMu.Unlock()

	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// Reap 执行一次完整的沙箱与 Code IR 分级 LRU 配额回收（高水位 90% -> 低水位 75%），严格保护全部活跃租约。
func (r *ResourceReaper) Reap(ctx context.Context) (*ReapResultVO, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.reapMu.Lock()
	defer r.reapMu.Unlock()

	res := &ReapResultVO{}

	// 1. 沙箱工作区 (sandbox_dir) LRU 回收
	sbReaped, sbFreed, sbSkipped, err := r.reapSandboxes(ctx)
	if err != nil {
		return nil, err
	}
	res.ReapedSandboxes = sbReaped
	res.FreedBytes += sbFreed
	res.SkippedActiveLeases += sbSkipped

	// 2. Code IR 缓存 (ir_cache) 分级 LRU 回收（先未发布旧草稿分片，再已发布分片；先 os.Remove 再标 evicted_at）
	irReaped, irFreed, irSkipped, err := r.reapIRCache(ctx)
	if err != nil {
		return nil, err
	}
	res.ReapedIRFacets = irReaped
	res.FreedBytes += irFreed
	res.SkippedActiveLeases += irSkipped

	return res, nil
}

func (r *ResourceReaper) reapSandboxes(ctx context.Context) (reaped int, freed int64, skipped int, err error) {
	root := strings.TrimSpace(r.cfg.SandboxDir)
	if root == "" {
		return 0, 0, 0, nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, 0, 0, err
	}
	entries, readErr := os.ReadDir(absRoot)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, readErr
	}

	var repoLastAccess map[uint]time.Time
	if r.snapRepo != nil {
		var accessErr error
		repoLastAccess, accessErr = r.snapRepo.ListLatestAccessByRepo(ctx)
		if accessErr != nil {
			log.Printf("[%s] 读取仓库快照最近访问时间失败，回退为目录 mtime: %v", config.ServiceName, accessErr)
		}
	}

	type sandboxEntry struct {
		repoID     uint
		path       string
		sizeBytes  int64
		lastAccess time.Time
		active     bool
	}

	var items []sandboxEntry
	var totalBytes int64
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dirPath := filepath.Join(absRoot, ent.Name())
		sz, modTime, _ := dirUsageStats(dirPath)
		totalBytes += sz

		repoID := parseRepoIDFromSandboxName(ent.Name())
		lastAccess := modTime
		if repoID > 0 && repoLastAccess != nil {
			if dbAt, ok := repoLastAccess[repoID]; ok && dbAt.After(lastAccess) {
				lastAccess = dbAt
			}
		}

		active := repoID > 0 && r.leaseMgr.IsRepoActive(repoID)
		items = append(items, sandboxEntry{
			repoID:     repoID,
			path:       dirPath,
			sizeBytes:  sz,
			lastAccess: lastAccess,
			active:     active,
		})
	}

	highBytes := int64(float64(r.cfg.SandboxMaxBytes) * r.cfg.HighWatermark)
	lowBytes := int64(float64(r.cfg.SandboxMaxBytes) * r.cfg.LowWatermark)
	if totalBytes <= highBytes {
		return 0, 0, 0, nil
	}

	sort.Slice(items, func(i, j int) bool {
		if !items[i].lastAccess.Equal(items[j].lastAccess) {
			return items[i].lastAccess.Before(items[j].lastAccess)
		}
		return items[i].path < items[j].path
	})

	rootPrefix := absRoot + string(os.PathSeparator)
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		if totalBytes <= lowBytes {
			break
		}
		if it.active {
			skipped++
			continue
		}
		// 严格边界校验，防止越界删除
		cleanTarget := filepath.Clean(it.path)
		if !strings.HasPrefix(cleanTarget, rootPrefix) {
			continue
		}
		if rmErr := os.RemoveAll(cleanTarget); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Printf("[%s] 回收沙箱目录 %s 失败: %v", config.ServiceName, cleanTarget, rmErr)
			continue
		}
		reaped++
		freed += it.sizeBytes
		totalBytes -= it.sizeBytes
	}

	return reaped, freed, skipped, nil
}

func (r *ResourceReaper) reapIRCache(ctx context.Context) (reaped int, freed int64, skipped int, err error) {
	if r.snapRepo == nil {
		return 0, 0, 0, nil
	}

	root := strings.TrimSpace(r.cfg.IRCacheDir)
	absRoot := ""
	var diskTotalBytes int64
	if root != "" {
		if a, absErr := filepath.Abs(root); absErr == nil {
			absRoot = a
			diskTotalBytes, _, _ = dirUsageStats(absRoot)
		}
	}

	// 查询全部未回收分片以识别活跃租约保护数与总占用
	allActiveFacets, err := r.snapRepo.ListEvictableFacets(ctx, nil, 0)
	if err != nil {
		return 0, 0, 0, err
	}

	var dbTotalBytes int64
	for _, f := range allActiveFacets {
		dbTotalBytes += f.CompressedBytes
	}

	currentBytes := diskTotalBytes
	if dbTotalBytes > currentBytes {
		currentBytes = dbTotalBytes
	}

	highBytes := int64(float64(r.cfg.IRCacheMaxBytes) * r.cfg.HighWatermark)
	lowBytes := int64(float64(r.cfg.IRCacheMaxBytes) * r.cfg.LowWatermark)
	if currentBytes <= highBytes {
		return 0, 0, 0, nil
	}

	publishedSnapIDs, err := r.snapRepo.ListPublishedSnapshotIDs(ctx)
	if err != nil {
		return 0, 0, 0, err
	}

	activeSnapSet := make(map[uint]bool)
	for _, id := range r.leaseMgr.ActiveSnapshotIDs() {
		activeSnapSet[id] = true
	}

	now := time.Now()
	type facetCandidate struct {
		facet    models.CodeIRFacet
		tier     int // 0: >24h 旧草稿分片; 1: 其余未发布草稿分片; 2: 已发布文档引用的分片
		fileSize int64
	}

	candidates := make([]facetCandidate, 0, len(allActiveFacets))
	skippedSnapSet := make(map[uint]bool)
	for _, f := range allActiveFacets {
		if activeSnapSet[f.SnapshotID] {
			if !skippedSnapSet[f.SnapshotID] {
				skippedSnapSet[f.SnapshotID] = true
				skipped++
			}
			continue
		}

		tier := 1
		if publishedSnapIDs[f.SnapshotID] {
			tier = 2
		} else if now.Sub(f.LastAccessedAt) >= draftPriorityAgeWindow {
			tier = 0
		}

		sz := f.CompressedBytes
		if absPath := r.resolveSafeIRPath(absRoot, f.StoragePath); absPath != "" {
			if st, stErr := os.Stat(absPath); stErr == nil && !st.IsDir() {
				sz = st.Size()
			}
		}

		candidates = append(candidates, facetCandidate{
			facet:    f,
			tier:     tier,
			fileSize: sz,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier < candidates[j].tier
		}
		if !candidates[i].facet.LastAccessedAt.Equal(candidates[j].facet.LastAccessedAt) {
			return candidates[i].facet.LastAccessedAt.Before(candidates[j].facet.LastAccessedAt)
		}
		return candidates[i].facet.ID < candidates[j].facet.ID
	})

	for _, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		if currentBytes <= lowBytes {
			break
		}

		// 回收前先 os.Remove 归还操作系统空间，再落库标 evicted_at
		if absPath := r.resolveSafeIRPath(absRoot, cand.facet.StoragePath); absPath != "" {
			if rmErr := os.Remove(absPath); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Printf("[%s] 删除 Code IR 分片文件 %s 失败: %v", config.ServiceName, absPath, rmErr)
				continue
			}
		}
		if markErr := r.snapRepo.MarkFacetEvicted(ctx, cand.facet.ID, now); markErr != nil {
			log.Printf("[%s] 标记 Code IR 分片 evicted_at 失败 (id=%d): %v", config.ServiceName, cand.facet.ID, markErr)
			continue
		}

		reaped++
		freed += cand.fileSize
		currentBytes -= cand.fileSize
	}

	return reaped, freed, skipped, nil
}

func (r *ResourceReaper) resolveSafeIRPath(absRoot, storagePath string) string {
	trimmed := strings.TrimSpace(storagePath)
	if trimmed == "" || absRoot == "" {
		return ""
	}
	var candidate string
	if filepath.IsAbs(trimmed) {
		candidate = filepath.Clean(trimmed)
	} else {
		candidate = filepath.Clean(filepath.Join(absRoot, trimmed))
	}
	rootPrefix := absRoot + string(os.PathSeparator)
	if !strings.HasPrefix(candidate, rootPrefix) {
		return ""
	}
	return candidate
}

// GetStorageStats 汇总 sandbox_dir、ir_cache、publish_dir、TaskScheduler 与近 30 天 LLM Token 消耗统计。
func (r *ResourceReaper) GetStorageStats(ctx context.Context) (*StorageStatsVO, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	sbBytes, sbRepos := scanSandboxOverview(r.cfg.SandboxDir)
	irBytes, _, _ := dirUsageStats(r.cfg.IRCacheDir)
	pubBytes, pubFiles := scanPublishedFiles(r.cfg.PublishDir)

	var activeFacets, evictedFacets int64
	if r.snapRepo != nil {
		ac, ec, dbBytes, err := r.snapRepo.CountFacetStats(ctx)
		if err != nil {
			return nil, err
		}
		activeFacets = ac
		evictedFacets = ec
		if dbBytes > irBytes {
			irBytes = dbBytes
		}
	}

	var schedStats SchedulerStatsVO
	if r.scheduler != nil {
		schedStats = r.scheduler.Stats()
	}

	usageList := make([]UsageSummaryVO, 0)
	if r.govRepo != nil {
		since := time.Now().AddDate(0, 0, -30)
		rows, err := r.govRepo.SummarizeLLMUsageSince(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			usageList = append(usageList, UsageSummaryVO{
				ModelTier:          row.ModelTier,
				DocTypeSlug:        row.DocTypeSlug,
				TotalTokens:        row.TotalTokens,
				CachedPromptTokens: row.CachedPromptTokens,
				CallCount:          row.CallCount,
			})
		}
	}

	return &StorageStatsVO{
		Sandbox: SandboxStorageStatVO{
			UsedMB:       bytesToMB(sbBytes),
			MaxMB:        bytesToMB(r.cfg.SandboxMaxBytes),
			RepoCount:    sbRepos,
			ActiveLeases: r.leaseMgr.ActiveRepoCount(),
		},
		IRCache: IRCacheStorageStatVO{
			UsedMB:       bytesToMB(irBytes),
			MaxMB:        bytesToMB(r.cfg.IRCacheMaxBytes),
			FacetCount:   activeFacets,
			EvictedCount: evictedFacets,
		},
		PublishDir: PublishStorageStatVO{
			UsedMB:         bytesToMB(pubBytes),
			PublishedFiles: pubFiles,
		},
		Scheduler:    schedStats,
		UsageLast30D: usageList,
	}, nil
}

func parseRepoIDFromSandboxName(name string) uint {
	if !strings.HasPrefix(name, "repo_") {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(name, "repo_"), 10, 64)
	if err != nil {
		return 0
	}
	return uint(v)
}

func dirUsageStats(dir string) (totalBytes int64, latestMod time.Time, fileCount int) {
	if strings.TrimSpace(dir) == "" {
		return 0, time.Time{}, 0
	}
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
		}
		if !d.IsDir() {
			totalBytes += info.Size()
			fileCount++
		}
		return nil
	})
	return totalBytes, latestMod, fileCount
}

func scanSandboxOverview(dir string) (totalBytes int64, repoCount int) {
	if strings.TrimSpace(dir) == "" {
		return 0, 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		repoCount++
		sz, _, _ := dirUsageStats(filepath.Join(dir, ent.Name()))
		totalBytes += sz
	}
	return totalBytes, repoCount
}

func scanPublishedFiles(dir string) (totalBytes int64, mdCount int) {
	if strings.TrimSpace(dir) == "" {
		return 0, 0
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() && strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr == nil {
			totalBytes += info.Size()
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") && !strings.EqualFold(name, "README.md") {
			mdCount++
		}
		return nil
	})
	return totalBytes, mdCount
}

func bytesToMB(b int64) int64 {
	if b <= 0 {
		return 0
	}
	const mb = 1024 * 1024
	if b < mb {
		return 1
	}
	return (b + mb/2) / mb
}
