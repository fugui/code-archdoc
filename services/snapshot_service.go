package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/sandbox"
	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"

	"golang.org/x/sync/singleflight"
	"gorm.io/datatypes"
)

// GitSyncer 抽象代码仓同步行为（默认复用 code-common/backend/gitsync，测试可注入计数桩）。
type GitSyncer interface {
	Sync(ctx context.Context, opts gitsync.SyncOptions) (*gitsync.SyncResult, error)
}

// DefaultGitSyncer 直接复用 code-common/backend/gitsync 公共包，严禁在模块内复制 Git 同步代码。
type DefaultGitSyncer struct{}

func (DefaultGitSyncer) Sync(ctx context.Context, opts gitsync.SyncOptions) (*gitsync.SyncResult, error) {
	return gitsync.Sync(ctx, opts)
}

// SnapshotReady 表示已就绪的快照记录、定位信息与加载好的完整 Code IR（及子模块专属的第 10 分片 ModuleContext）。
type SnapshotReady struct {
	Snapshot      *models.RepoSnapshot
	Loc           codeir.SnapshotLoc
	IR            *codeir.CodeIRBundle
	ModuleContext *codeir.ModuleContextFacet
	Reused        bool
}

// SnapshotService 编排 Git 沙箱同步、L1 Code IR 抽取与落盘缓存、工作树读写互斥及快照级 Glossary 共享复用（singleflight）。
type SnapshotService struct {
	snapRepo        SnapshotRepo
	facetStore      codeir.FacetStore
	aggregator      *codeir.Aggregator
	gitSyncer       GitSyncer
	sandboxDir      string
	maxFileBytes    int64
	maxRepoFiles    int
	maxRepoDuration time.Duration
	leaseMgr        *ActiveLeaseManager
	worktreeLocker  *RepoWorktreeLocker
	onBaseExtracted func(ctx context.Context, repoID uint, commitSHA string, tree *codeir.TreeFacet)

	syncExtractSF singleflight.Group
	glossarySF    singleflight.Group
}

// WithLeaseManager 注入活跃租约管理器，在 Git 同步与 Code IR 读取期间保护目录与分片不被 LRU 回收（F-27）。
func (s *SnapshotService) WithLeaseManager(leaseMgr *ActiveLeaseManager) *SnapshotService {
	s.leaseMgr = leaseMgr
	return s
}

// WithExtractLimits 配置全仓基础抽取降级阈值（超过文件数或耗时则退回按子目录独立抽取 + 轻量依赖索引）。
func (s *SnapshotService) WithExtractLimits(maxRepoFiles int, maxRepoDuration time.Duration) *SnapshotService {
	if maxRepoFiles > 0 {
		s.maxRepoFiles = maxRepoFiles
	}
	if maxRepoDuration > 0 {
		s.maxRepoDuration = maxRepoDuration
	}
	return s
}

// WithOnBaseExtracted 注入全仓基础快照抽取完成后的回调（供 ModuleService 自动刷新模块索引）。
func (s *SnapshotService) WithOnBaseExtracted(fn func(ctx context.Context, repoID uint, commitSHA string, tree *codeir.TreeFacet)) *SnapshotService {
	s.onBaseExtracted = fn
	return s
}

func NewSnapshotService(
	snapRepo SnapshotRepo,
	facetStore codeir.FacetStore,
	aggregator *codeir.Aggregator,
	gitSyncer GitSyncer,
	sandboxDir string,
	maxFileBytes int64,
) *SnapshotService {
	if gitSyncer == nil {
		gitSyncer = DefaultGitSyncer{}
	}
	if maxFileBytes <= 0 {
		maxFileBytes = 1048576
	}
	return &SnapshotService{
		snapRepo:        snapRepo,
		facetStore:      facetStore,
		aggregator:      aggregator,
		gitSyncer:       gitSyncer,
		sandboxDir:      sandboxDir,
		maxFileBytes:    maxFileBytes,
		maxRepoFiles:    50000,
		maxRepoDuration: 600 * time.Second,
		worktreeLocker:  NewRepoWorktreeLocker(),
	}
}

// WithRepoReadLock 持指定仓库的共享读锁执行工作树只读操作（供 SourcePreviewService / DriftOrchestrator 复用，消除与 git reset --hard 竞态）。
func (s *SnapshotService) WithRepoReadLock(repoID uint, fn func() error) error {
	if s == nil || s.worktreeLocker == nil || repoID == 0 {
		return fn()
	}
	rw := s.worktreeLocker.Get(repoID)
	rw.RLock()
	defer rw.RUnlock()
	return fn()
}

// WithRepoWriteLock 持指定仓库的独占写锁执行工作树修改操作。
func (s *SnapshotService) WithRepoWriteLock(repoID uint, fn func() error) error {
	if s == nil || s.worktreeLocker == nil || repoID == 0 {
		return fn()
	}
	rw := s.worktreeLocker.Get(repoID)
	rw.Lock()
	defer rw.Unlock()
	return fn()
}

// EnsureSandboxSynced 在独占写锁下同步指定仓库的本地沙箱工作树（供 SourcePreview 等缺失沙箱自动重拉时统一复用，严禁外部绕过锁直接调用 gitsync.Sync）。
func (s *SnapshotService) EnsureSandboxSynced(ctx context.Context, repo *commonModels.Repository, branch string) (string, string, error) {
	if repo == nil || strings.TrimSpace(repo.URL) == "" {
		return "", "", fmt.Errorf("仓库信息或地址为空")
	}
	targetBranch := strings.TrimSpace(branch)
	if targetBranch == "" {
		targetBranch = strings.TrimSpace(repo.Branch)
	}
	if targetBranch == "" {
		targetBranch = "master"
	}

	releaseRepoLease := s.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	localPath := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repo.ID))
	var (
		resolvedPath string
		commitSHA    string
	)
	err := s.WithRepoWriteLock(repo.ID, func() error {
		syncRes, syncErr := s.gitSyncer.Sync(ctx, gitsync.SyncOptions{
			RepoURL:        repo.URL,
			LocalPath:      localPath,
			Branch:         targetBranch,
			Depth:          1,
			AutoHealCache:  true,
			CleanUntracked: true,
		})
		if syncErr != nil {
			return syncErr
		}
		if syncRes != nil {
			resolvedPath = syncRes.LocalPath
			commitSHA = strings.TrimSpace(syncRes.CommitSHA)
		}
		if resolvedPath == "" {
			resolvedPath = localPath
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return resolvedPath, commitSHA, nil
}

// AcquireRepoLease 获取仓库级只读租约（空安全，防止工具读取源码期间目录被 LRU 回收）。
func (s *SnapshotService) AcquireRepoLease(repoID uint) func() {
	if s == nil || s.leaseMgr == nil {
		return func() {}
	}
	return s.leaseMgr.AcquireRepo(repoID)
}

// ResolveSandboxScope 安全解析仓库及 Monorepo 子目录的物理只读根目录（经 SafeJoin 校验且确认目录存在）。
func (s *SnapshotService) ResolveSandboxScope(repoID uint, normSubdir string) (repoRoot string, scopeRoot string, ok bool) {
	if s == nil || strings.TrimSpace(s.sandboxDir) == "" || repoID == 0 {
		return "", "", false
	}
	candidateRepo := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repoID))
	resolvedRepo, err := sandbox.SafeJoin(candidateRepo, ".")
	if err != nil {
		return "", "", false
	}
	st, err := os.Stat(resolvedRepo)
	if err != nil || !st.IsDir() {
		return "", "", false
	}
	if strings.TrimSpace(normSubdir) == "" {
		return resolvedRepo, resolvedRepo, true
	}
	cleanSub, err := sandbox.NormalizeSubdir(normSubdir)
	if err != nil {
		return "", "", false
	}
	if cleanSub == "" {
		return resolvedRepo, resolvedRepo, true
	}
	resolvedScope, err := sandbox.SafeJoin(resolvedRepo, cleanSub)
	if err != nil {
		return "", "", false
	}
	stScope, err := os.Stat(resolvedScope)
	if err != nil || !stScope.IsDir() {
		return "", "", false
	}
	return resolvedRepo, resolvedScope, true
}

// SnapshotKey 构造规范缓存键 (repo_id, subdir, ref_name, commit_sha, ir_schema_version)。
func SnapshotKey(repoID uint, normSubdir, refName, commitSHA string) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s", repoID, normSubdir, refName, commitSHA, codeir.SchemaVersion)
}

// EnsureSnapshot 通过进程内 singleflight.Group 确保同仓库同范围并发触发时 Git 同步与 L1 Code IR 抽取仅执行 1 次（SC-02 / ADR-04 D-2）：
//   - normSubdir == ""：全仓基础快照同步与 L1 抽取；
//   - normSubdir != ""：优先复用全仓基础快照执行零 I/O SliceByModule 切出 9 大分片 + 第 10 分片 module_context（0 次重复 AST 解析）；
//     若仓库文件数或耗时超过 extract 降级阈值，则自动退化为子目录独立抽取并记录 stats_json.degraded_reason。
func (s *SnapshotService) EnsureSnapshot(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	refName string,
) (*SnapshotReady, error) {
	if repo == nil {
		return nil, fmt.Errorf("仓库信息为空")
	}
	targetBranch := strings.TrimSpace(refName)
	if targetBranch == "" {
		targetBranch = strings.TrimSpace(repo.Branch)
	}
	if targetBranch == "" {
		targetBranch = "master"
	}

	if normSubdir == "" {
		return s.ensureBaseSnapshot(ctx, repo, targetBranch)
	}
	return s.ensureSliceSnapshot(ctx, repo, normSubdir, targetBranch)
}

func (s *SnapshotService) ensureBaseSnapshot(
	ctx context.Context,
	repo *commonModels.Repository,
	targetBranch string,
) (*SnapshotReady, error) {
	sfKey := fmt.Sprintf("base:%d|%s", repo.ID, targetBranch)
	val, err, shared := s.syncExtractSF.Do(sfKey, func() (any, error) {
		return s.syncAndExtractOnce(ctx, repo, "", targetBranch)
	})
	if err != nil {
		return nil, err
	}
	ready := val.(*SnapshotReady)
	cp := *ready
	if ready.Snapshot != nil {
		snapCopy := *ready.Snapshot
		cp.Snapshot = &snapCopy
	}
	if shared {
		cp.Reused = true
	}
	return &cp, nil
}

func (s *SnapshotService) ensureSliceSnapshot(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	targetBranch string,
) (*SnapshotReady, error) {
	baseReady, err := s.ensureBaseSnapshot(ctx, repo, targetBranch)
	if err != nil {
		return nil, err
	}

	sfKey := fmt.Sprintf("slice:%d|%s|%s", repo.ID, normSubdir, baseReady.Snapshot.CommitSHA)
	val, err, shared := s.syncExtractSF.Do(sfKey, func() (any, error) {
		return s.sliceOrDegradeFromBaseOnce(ctx, repo, normSubdir, baseReady)
	})
	if err != nil {
		return nil, err
	}
	ready := val.(*SnapshotReady)
	cp := *ready
	if ready.Snapshot != nil {
		snapCopy := *ready.Snapshot
		cp.Snapshot = &snapCopy
	}
	if shared {
		cp.Reused = true
	}
	return &cp, nil
}

func (s *SnapshotService) sliceOrDegradeFromBaseOnce(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	baseReady *SnapshotReady,
) (*SnapshotReady, error) {
	resolvedBranch := baseReady.Snapshot.RefName
	commitSHA := baseReady.Snapshot.CommitSHA
	loc := codeir.SnapshotLoc{
		RepoID:    repo.ID,
		Subdir:    normSubdir,
		CommitSHA: commitSHA,
	}

	// 1. 命中已落盘的子模块切片缓存 -> 0 次切片/抽取，直接还原 9 大分片 + module_context
	existing, findErr := s.snapRepo.FindByKey(ctx, repo.ID, normSubdir, resolvedBranch, commitSHA, codeir.SchemaVersion)
	if findErr == nil && existing != nil && existing.Status == "ready" {
		if ir, loadErr := s.loadBundleFromStore(ctx, existing, loc); loadErr == nil {
			modCtx := s.LoadModuleContext(ctx, existing, loc)
			return &SnapshotReady{
				Snapshot:      existing,
				Loc:           loc,
				IR:            ir,
				ModuleContext: modCtx,
				Reused:        true,
			}, nil
		}
	}

	// 2. 判断是否超过全仓切片阈值（超阈值降级为独立子目录抽取 + 轻量依赖索引）
	degradedReason := ""
	if s.maxRepoFiles > 0 && baseReady.IR.Tree.TotalFiles > s.maxRepoFiles {
		degradedReason = "max_repo_files_exceeded"
	} else if s.maxRepoDuration > 0 && time.Duration(baseReady.IR.ExtractDurationMs)*time.Millisecond > s.maxRepoDuration {
		degradedReason = "max_repo_duration_exceeded"
	}

	var (
		irBundle *codeir.CodeIRBundle
		modCtx   *codeir.ModuleContextFacet
		err      error
	)

	if degradedReason != "" {
		localPath := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repo.ID))
		err = s.WithRepoReadLock(repo.ID, func() error {
			var exErr error
			irBundle, exErr = s.aggregator.ExtractDirectory(ctx, localPath, normSubdir, sandbox.WalkOptions{
				MaxFileBytes: s.maxFileBytes,
			})
			return exErr
		})
		if err != nil {
			return nil, fmt.Errorf("降级独立抽取子目录 %s 失败: %w", normSubdir, err)
		}
		_, modCtx, _ = codeir.SliceByModule(baseReady.IR, normSubdir, nil)
	} else {
		// 标准零 I/O 确定性子图切片（0 次重复 AST 解析）
		irBundle, modCtx, err = codeir.SliceByModule(baseReady.IR, normSubdir, nil)
		if err != nil {
			return nil, fmt.Errorf("从全仓基础快照切分子模块 %s 失败: %w", normSubdir, err)
		}
	}

	baseID := baseReady.Snapshot.ID
	if modCtx != nil {
		modCtx.BaseSnapshotID = baseID
	}

	metas, summaries, err := codeir.WriteBundle(ctx, s.facetStore, loc, irBundle)
	if err != nil {
		return nil, fmt.Errorf("落盘子模块 Code IR 分片失败: %w", err)
	}

	var (
		mcMeta    *codeir.FacetWriteMeta
		mcSummary codeir.FacetSummaryRecord
	)
	if modCtx != nil {
		mcMeta, mcSummary, err = codeir.WriteModuleContextFacet(ctx, s.facetStore, loc, modCtx)
		if err != nil {
			return nil, fmt.Errorf("落盘子模块 module_context 分片失败: %w", err)
		}
	}

	now := time.Now()
	langBytes := make(map[string]int64, len(irBundle.Tree.LanguageStats))
	langFiles := make(map[string]int, len(irBundle.Tree.LanguageStats))
	for lang, st := range irBundle.Tree.LanguageStats {
		langBytes[lang] = st.Bytes
		langFiles[lang] = st.Files
	}
	skippedTotal := 0
	for _, n := range irBundle.Tree.SkippedNoiseSummary {
		skippedTotal += n
	}
	statsBytes, _ := json.Marshal(models.SnapshotStats{
		LanguageBytes:      langBytes,
		LanguageFiles:      langFiles,
		NoiseSkippedByRule: irBundle.Tree.SkippedNoiseSummary,
		ParseDiagnostics:   len(irBundle.Diagnostics),
		ExtractDurationMs:  irBundle.ExtractDurationMs,
		DegradedReason:     degradedReason,
	})

	var baseIDPtr *uint
	if degradedReason == "" && baseID > 0 {
		baseIDPtr = &baseID
	}

	snap := &models.RepoSnapshot{
		RepoID:          repo.ID,
		Subdirectory:    normSubdir,
		RefName:         resolvedBranch,
		CommitSHA:       commitSHA,
		IRSchemaVersion: codeir.SchemaVersion,
		BaseSnapshotID:  baseIDPtr,
		Status:          "ready",
		EffectiveFiles:  irBundle.Tree.TotalFiles,
		SkippedFiles:    skippedTotal,
		TotalLOC:        irBundle.Tree.TotalLOC,
		StatsJSON:       datatypes.JSON(statsBytes),
		LastAccessedAt:  now,
	}
	if err := s.snapRepo.UpsertSnapshot(ctx, snap); err != nil {
		return nil, fmt.Errorf("保存子模块快照索引失败: %w", err)
	}
	persistedSnap, err := s.snapRepo.FindByKey(ctx, repo.ID, normSubdir, resolvedBranch, commitSHA, codeir.SchemaVersion)
	if err != nil {
		return nil, err
	}

	for _, facetName := range codeir.AllFacetNames {
		meta := metas[facetName]
		sumRec := summaries[facetName]
		sumJSON, _ := json.Marshal(sumRec)
		facetRow := &models.CodeIRFacet{
			SnapshotID:      persistedSnap.ID,
			FacetName:       facetName,
			StoragePath:     meta.StoragePath,
			ContentSHA256:   meta.ContentSHA256,
			ItemCount:       sumRec.ExactCount + sumRec.HeuristicCount,
			SizeBytes:       meta.SizeBytes,
			CompressedBytes: meta.CompressedBytes,
			SummaryJSON:     datatypes.JSON(sumJSON),
			LastAccessedAt:  now,
			EvictedAt:       nil,
		}
		if err := s.snapRepo.UpsertFacet(ctx, facetRow); err != nil {
			return nil, fmt.Errorf("保存分片元数据 %s 失败: %w", facetName, err)
		}
	}
	if mcMeta != nil {
		sumJSON, _ := json.Marshal(mcSummary)
		mcRow := &models.CodeIRFacet{
			SnapshotID:      persistedSnap.ID,
			FacetName:       codeir.FacetModuleContext,
			StoragePath:     mcMeta.StoragePath,
			ContentSHA256:   mcMeta.ContentSHA256,
			ItemCount:       mcSummary.ExactCount + mcSummary.HeuristicCount,
			SizeBytes:       mcMeta.SizeBytes,
			CompressedBytes: mcMeta.CompressedBytes,
			SummaryJSON:     datatypes.JSON(sumJSON),
			LastAccessedAt:  now,
			EvictedAt:       nil,
		}
		if err := s.snapRepo.UpsertFacet(ctx, mcRow); err != nil {
			return nil, fmt.Errorf("保存分片元数据 %s 失败: %w", codeir.FacetModuleContext, err)
		}
	}

	return &SnapshotReady{
		Snapshot:      persistedSnap,
		Loc:           loc,
		IR:            irBundle,
		ModuleContext: modCtx,
		Reused:        false,
	}, nil
}

func (s *SnapshotService) syncAndExtractOnce(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	branch string,
) (*SnapshotReady, error) {
	releaseRepoLease := s.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	localPath := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repo.ID))

	// 持独占写锁执行 Git 同步，完成后原子降级为共享读锁再做 L1 抽取（零间隙窗口，彻底消除 P-5 并发竞态）
	rw := s.worktreeLocker.Get(repo.ID)
	rw.Lock()
	syncRes, err := s.gitSyncer.Sync(ctx, gitsync.SyncOptions{
		RepoURL:        repo.URL,
		LocalPath:      localPath,
		Branch:         branch,
		Depth:          1,
		AutoHealCache:  true,
		CleanUntracked: true,
	})
	if err != nil {
		rw.Unlock()
		return nil, fmt.Errorf("Git 沙箱同步失败: %w", err)
	}
	rw.DowngradeToRLock()
	defer rw.RUnlock()

	resolvedBranch := strings.TrimSpace(syncRes.ResolvedBranch)
	if resolvedBranch == "" {
		resolvedBranch = branch
	}
	commitSHA := strings.TrimSpace(syncRes.CommitSHA)
	loc := codeir.SnapshotLoc{
		RepoID:    repo.ID,
		Subdir:    normSubdir,
		CommitSHA: commitSHA,
	}

	// 检查相同 SnapshotKey 是否已就绪且所有 9 大分片未回收 -> 直接命中缓存（0 次重复 L1 抽取）
	existing, findErr := s.snapRepo.FindByKey(ctx, repo.ID, normSubdir, resolvedBranch, commitSHA, codeir.SchemaVersion)
	if findErr == nil && existing != nil && existing.Status == "ready" {
		if ir, loadErr := s.loadBundleFromStore(ctx, existing, loc); loadErr == nil {
			if normSubdir == "" && s.onBaseExtracted != nil {
				s.onBaseExtracted(ctx, repo.ID, commitSHA, &ir.Tree)
			}
			return &SnapshotReady{
				Snapshot: existing,
				Loc:      loc,
				IR:       ir,
				Reused:   true,
			}, nil
		}
	}

	// 执行零 LLM 确定性 L1 Code IR 抽取（期间始终持有共享读锁，阻断并发 git reset --hard）
	irBundle, err := s.aggregator.ExtractDirectory(ctx, syncRes.LocalPath, normSubdir, sandbox.WalkOptions{
		MaxFileBytes: s.maxFileBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("L1 Code IR 抽取失败: %w", err)
	}
	if normSubdir == "" && s.onBaseExtracted != nil {
		s.onBaseExtracted(ctx, repo.ID, commitSHA, &irBundle.Tree)
	}

	// 原子压缩落盘 9 大分片
	metas, summaries, err := codeir.WriteBundle(ctx, s.facetStore, loc, irBundle)
	if err != nil {
		return nil, fmt.Errorf("落盘 Code IR 分片失败: %w", err)
	}

	now := time.Now()
	langBytes := make(map[string]int64, len(irBundle.Tree.LanguageStats))
	langFiles := make(map[string]int, len(irBundle.Tree.LanguageStats))
	for lang, st := range irBundle.Tree.LanguageStats {
		langBytes[lang] = st.Bytes
		langFiles[lang] = st.Files
	}
	skippedTotal := 0
	for _, n := range irBundle.Tree.SkippedNoiseSummary {
		skippedTotal += n
	}
	statsBytes, _ := json.Marshal(models.SnapshotStats{
		LanguageBytes:      langBytes,
		LanguageFiles:      langFiles,
		NoiseSkippedByRule: irBundle.Tree.SkippedNoiseSummary,
		ParseDiagnostics:   len(irBundle.Diagnostics),
		ExtractDurationMs:  irBundle.ExtractDurationMs,
	})

	snap := &models.RepoSnapshot{
		RepoID:          repo.ID,
		Subdirectory:    normSubdir,
		RefName:         resolvedBranch,
		CommitSHA:       commitSHA,
		IRSchemaVersion: codeir.SchemaVersion,
		Status:          "ready",
		EffectiveFiles:  irBundle.Tree.TotalFiles,
		SkippedFiles:    skippedTotal,
		TotalLOC:        irBundle.Tree.TotalLOC,
		StatsJSON:       datatypes.JSON(statsBytes),
		LastAccessedAt:  now,
	}
	if err := s.snapRepo.UpsertSnapshot(ctx, snap); err != nil {
		return nil, fmt.Errorf("保存快照索引失败: %w", err)
	}
	// Upsert 在冲突更新时需确保拿到真实数据库 ID
	persistedSnap, err := s.snapRepo.FindByKey(ctx, repo.ID, normSubdir, resolvedBranch, commitSHA, codeir.SchemaVersion)
	if err != nil {
		return nil, err
	}

	for _, facetName := range codeir.AllFacetNames {
		meta := metas[facetName]
		sumRec := summaries[facetName]
		sumJSON, _ := json.Marshal(sumRec)
		facetRow := &models.CodeIRFacet{
			SnapshotID:      persistedSnap.ID,
			FacetName:       facetName,
			StoragePath:     meta.StoragePath,
			ContentSHA256:   meta.ContentSHA256,
			ItemCount:       sumRec.ExactCount + sumRec.HeuristicCount,
			SizeBytes:       meta.SizeBytes,
			CompressedBytes: meta.CompressedBytes,
			SummaryJSON:     datatypes.JSON(sumJSON),
			LastAccessedAt:  now,
			EvictedAt:       nil,
		}
		if err := s.snapRepo.UpsertFacet(ctx, facetRow); err != nil {
			return nil, fmt.Errorf("保存分片元数据 %s 失败: %w", facetName, err)
		}
	}

	return &SnapshotReady{
		Snapshot: persistedSnap,
		Loc:      loc,
		IR:       irBundle,
		Reused:   false,
	}, nil
}

// HasCachedGlossary 快速检查指定快照（或其所属仓库根基础快照）是否已有 glossary.json.gz（供预算预检精准扣除 Glossary 成本）。
func (s *SnapshotService) HasCachedGlossary(ctx context.Context, loc codeir.SnapshotLoc) bool {
	if s == nil || s.facetStore == nil {
		return false
	}
	baseLoc := codeir.SnapshotLoc{RepoID: loc.RepoID, Subdir: "", CommitSHA: loc.CommitSHA}
	if _, exists, err := s.facetStore.StatFacet(ctx, baseLoc, "glossary"); err == nil && exists {
		return true
	}
	if loc.Subdir != "" {
		_, exists, err := s.facetStore.StatFacet(ctx, loc, "glossary")
		return err == nil && exists
	}
	return false
}

// ReadCachedGlossary 读取已缓存的仓库级共享术语表（优先解析仓库根基础快照，兼容子快照直存路径）。
func (s *SnapshotService) ReadCachedGlossary(ctx context.Context, snap *models.RepoSnapshot, loc codeir.SnapshotLoc) (*codeir.GlossaryCache, error) {
	if s == nil || s.facetStore == nil {
		return nil, codeir.ErrFacetNotFound
	}
	baseLoc := codeir.SnapshotLoc{RepoID: loc.RepoID, Subdir: "", CommitSHA: loc.CommitSHA}
	if s.snapRepo != nil && snap != nil && snap.BaseSnapshotID != nil && *snap.BaseSnapshotID > 0 {
		if baseSnap, err := s.snapRepo.GetByID(ctx, *snap.BaseSnapshotID); err == nil && baseSnap != nil {
			baseLoc = codeir.SnapshotLoc{RepoID: baseSnap.RepoID, Subdir: baseSnap.Subdirectory, CommitSHA: baseSnap.CommitSHA}
		}
	}
	if cached, err := s.facetStore.ReadGlossary(ctx, baseLoc); err == nil && cached != nil {
		return cached, nil
	}
	if loc.Subdir != "" {
		return s.facetStore.ReadGlossary(ctx, loc)
	}
	return nil, codeir.ErrFacetNotFound
}

// EnsureGlossary 通过仓库根级 singleflight 确保同仓库（含全部子模块与整仓任务）共享同一份 glossary.json.gz 且严格只调用 LLM 生成 1 次（SC-02 / ADR-04 D-4 §6.2）。
func (s *SnapshotService) EnsureGlossary(
	ctx context.Context,
	snap *models.RepoSnapshot,
	loc codeir.SnapshotLoc,
	ir *codeir.CodeIRBundle,
	llmClient *llm.Client,
	fastEndpoint llm.EndpointConfig,
	onUsage func(usage *llm.UsageMetrics, status string, errCode string),
) (*codeir.GlossaryCache, error) {
	if cached, err := s.ReadCachedGlossary(ctx, snap, loc); err == nil && cached != nil {
		return cached, nil
	}

	targetSnap := snap
	targetLoc := codeir.SnapshotLoc{RepoID: loc.RepoID, Subdir: "", CommitSHA: loc.CommitSHA}
	targetIR := ir
	if s.snapRepo != nil && snap != nil && snap.BaseSnapshotID != nil && *snap.BaseSnapshotID > 0 {
		if bSnap, bLoc, bIR, bErr := s.LoadIRBundleBySnapshotID(ctx, *snap.BaseSnapshotID); bErr == nil && bSnap != nil && bIR != nil {
			targetSnap = bSnap
			targetLoc = bLoc
			targetIR = bIR
		}
	} else if s.snapRepo != nil && loc.Subdir != "" && snap != nil {
		if bSnap, findErr := s.snapRepo.FindByKey(ctx, snap.RepoID, "", snap.RefName, snap.CommitSHA, codeir.SchemaVersion); findErr == nil && bSnap != nil {
			bLoc := codeir.SnapshotLoc{RepoID: bSnap.RepoID, Subdir: "", CommitSHA: bSnap.CommitSHA}
			if bIR, lErr := s.loadBundleFromStore(ctx, bSnap, bLoc); lErr == nil && bIR != nil {
				targetSnap = bSnap
				targetLoc = bLoc
				targetIR = bIR
			}
		}
	}

	key := SnapshotKey(targetLoc.RepoID, "", snap.RefName, targetLoc.CommitSHA)
	val, err, _ := s.glossarySF.Do(key, func() (any, error) {
		// Double-check 磁盘/LRU 缓存，避免并发临界窗口重复调用
		if cached, rErr := s.facetStore.ReadGlossary(ctx, targetLoc); rErr == nil && cached != nil {
			return cached, nil
		}

		glossary, usage, genErr := llm.GenerateGlossary(ctx, llmClient, fastEndpoint, targetIR)
		if genErr != nil {
			if onUsage != nil {
				st := "failed"
				if errors.Is(genErr, context.Canceled) {
					st = "canceled"
				}
				onUsage(&llm.UsageMetrics{ModelName: fastEndpoint.ModelName}, st, "GLOSSARY_GEN_FAILED")
			}
			return nil, genErr
		}
		if onUsage != nil {
			onUsage(usage, "success", "")
		}

		storagePath, writeErr := s.facetStore.WriteGlossary(ctx, targetLoc, glossary)
		if writeErr != nil {
			return nil, writeErr
		}

		if s.snapRepo != nil && targetSnap != nil {
			snapCopy := *targetSnap
			snapCopy.GlossaryPath = storagePath
			snapCopy.GlossaryModel = glossary.ModelName
			snapCopy.LastAccessedAt = time.Now()
			_ = s.snapRepo.UpsertSnapshot(ctx, &snapCopy)
		}

		return glossary, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*codeir.GlossaryCache), nil
}

// LoadModuleContext 按快照记录读取可选的第 10 分片 module_context.json.gz（整仓快照或缺失时返回 nil）。
func (s *SnapshotService) LoadModuleContext(ctx context.Context, snap *models.RepoSnapshot, loc codeir.SnapshotLoc) *codeir.ModuleContextFacet {
	if s == nil || snap == nil || snap.ID == 0 || snap.Subdirectory == "" {
		return nil
	}
	facetRows, err := s.snapRepo.ListFacetsBySnapshot(ctx, snap.ID)
	if err != nil {
		return nil
	}
	for _, fr := range facetRows {
		if fr.FacetName == codeir.FacetModuleContext && fr.EvictedAt == nil {
			var mc codeir.ModuleContextFacet
			if rErr := s.facetStore.ReadFacet(ctx, loc, codeir.FacetModuleContext, fr.ContentSHA256, &mc); rErr == nil {
				_ = s.snapRepo.TouchFacetAccess(ctx, []uint{fr.ID}, time.Now())
				return &mc
			}
		}
	}
	return nil
}

// LoadIRBundleBySnapshotID 按快照 ID 从 FacetStore 增量还原完整 CodeIRBundle（供单槽位独立重试复用）。
func (s *SnapshotService) LoadIRBundleBySnapshotID(ctx context.Context, snapshotID uint) (*models.RepoSnapshot, codeir.SnapshotLoc, *codeir.CodeIRBundle, error) {
	snap, err := s.snapRepo.GetByID(ctx, snapshotID)
	if err != nil {
		return nil, codeir.SnapshotLoc{}, nil, err
	}
	loc := codeir.SnapshotLoc{
		RepoID:    snap.RepoID,
		Subdir:    snap.Subdirectory,
		CommitSHA: snap.CommitSHA,
	}
	ir, err := s.loadBundleFromStore(ctx, snap, loc)
	if err != nil {
		return nil, codeir.SnapshotLoc{}, nil, err
	}
	return snap, loc, ir, nil
}

func (s *SnapshotService) loadBundleFromStore(ctx context.Context, snap *models.RepoSnapshot, loc codeir.SnapshotLoc) (*codeir.CodeIRBundle, error) {
	releaseSnapLease := s.leaseMgr.AcquireSnapshot(snap.ID)
	defer releaseSnapLease()

	facetRows, err := s.snapRepo.ListFacetsBySnapshot(ctx, snap.ID)
	if err != nil {
		return nil, err
	}
	shaByName := make(map[string]string, len(facetRows))
	facetIDs := make([]uint, 0, len(facetRows))
	for _, fr := range facetRows {
		if fr.EvictedAt != nil {
			return nil, codeir.ErrFacetNotFound
		}
		shaByName[fr.FacetName] = fr.ContentSHA256
		facetIDs = append(facetIDs, fr.ID)
	}
	if len(shaByName) < len(codeir.AllFacetNames) {
		return nil, codeir.ErrFacetNotFound
	}

	bundle := &codeir.CodeIRBundle{SchemaVersion: codeir.SchemaVersion}
	targets := map[string]any{
		codeir.FacetTree:              &bundle.Tree,
		codeir.FacetBuild:             &bundle.Build,
		codeir.FacetDeps:              &bundle.Deps,
		codeir.FacetConfigKeys:        &bundle.ConfigKeys,
		codeir.FacetSymbols:           &bundle.Symbols,
		codeir.FacetIDLContracts:      &bundle.IDLContracts,
		codeir.FacetHTTPRoutes:        &bundle.HTTPRoutes,
		codeir.FacetDataModels:        &bundle.DataModels,
		codeir.FacetRuntimePrimitives: &bundle.RuntimePrimitives,
	}
	for _, name := range codeir.AllFacetNames {
		if err := s.facetStore.ReadFacet(ctx, loc, name, shaByName[name], targets[name]); err != nil {
			return nil, err
		}
	}
	_ = s.snapRepo.TouchFacetAccess(ctx, facetIDs, time.Now())
	return bundle, nil
}
