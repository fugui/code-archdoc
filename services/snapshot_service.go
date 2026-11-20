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

// SnapshotReady 表示已就绪的快照记录、定位信息与加载好的完整 Code IR。
type SnapshotReady struct {
	Snapshot *models.RepoSnapshot
	Loc      codeir.SnapshotLoc
	IR       *codeir.CodeIRBundle
	Reused   bool
}

// SnapshotService 编排 Git 沙箱同步、L1 Code IR 抽取与落盘缓存、以及快照级 Glossary 共享复用（singleflight）。
type SnapshotService struct {
	snapRepo     SnapshotRepo
	facetStore   codeir.FacetStore
	aggregator   *codeir.Aggregator
	gitSyncer    GitSyncer
	sandboxDir   string
	maxFileBytes int64
	leaseMgr     *ActiveLeaseManager

	syncExtractSF singleflight.Group
	glossarySF    singleflight.Group
}

// WithLeaseManager 注入活跃租约管理器，在 Git 同步与 Code IR 读取期间保护目录与分片不被 LRU 回收（F-27）。
func (s *SnapshotService) WithLeaseManager(leaseMgr *ActiveLeaseManager) *SnapshotService {
	s.leaseMgr = leaseMgr
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
		snapRepo:     snapRepo,
		facetStore:   facetStore,
		aggregator:   aggregator,
		gitSyncer:    gitSyncer,
		sandboxDir:   sandboxDir,
		maxFileBytes: maxFileBytes,
	}
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

// EnsureSnapshot 通过进程内 singleflight.Group 确保同仓库同范围并发触发时 Git 同步与 L1 Code IR 抽取仅执行 1 次（SC-02）。
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

	sfKey := fmt.Sprintf("sync_extract:%d|%s|%s", repo.ID, normSubdir, targetBranch)
	val, err, shared := s.syncExtractSF.Do(sfKey, func() (any, error) {
		return s.syncAndExtractOnce(ctx, repo, normSubdir, targetBranch)
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

func (s *SnapshotService) syncAndExtractOnce(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	branch string,
) (*SnapshotReady, error) {
	releaseRepoLease := s.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	localPath := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repo.ID))
	syncRes, err := s.gitSyncer.Sync(ctx, gitsync.SyncOptions{
		RepoURL:        repo.URL,
		LocalPath:      localPath,
		Branch:         branch,
		Depth:          1,
		AutoHealCache:  true,
		CleanUntracked: true,
	})
	if err != nil {
		return nil, fmt.Errorf("Git 沙箱同步失败: %w", err)
	}

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
			return &SnapshotReady{
				Snapshot: existing,
				Loc:      loc,
				IR:       ir,
				Reused:   true,
			}, nil
		}
	}

	// 执行零 LLM 确定性 L1 Code IR 抽取
	irBundle, err := s.aggregator.ExtractDirectory(ctx, syncRes.LocalPath, normSubdir, sandbox.WalkOptions{
		MaxFileBytes: s.maxFileBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("L1 Code IR 抽取失败: %w", err)
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

// HasCachedGlossary 快速检查指定快照是否已有 glossary.json.gz（供预算预检精准扣除 Glossary 成本）。
func (s *SnapshotService) HasCachedGlossary(ctx context.Context, loc codeir.SnapshotLoc) bool {
	_, exists, err := s.facetStore.StatFacet(ctx, loc, "glossary")
	return err == nil && exists
}

// EnsureGlossary 通过 singleflight 确保同快照的共享术语表 glossary.json.gz 严格只调用 LLM 生成 1 次（SC-02）。
func (s *SnapshotService) EnsureGlossary(
	ctx context.Context,
	snap *models.RepoSnapshot,
	loc codeir.SnapshotLoc,
	ir *codeir.CodeIRBundle,
	llmClient *llm.Client,
	fastEndpoint llm.EndpointConfig,
	onUsage func(usage *llm.UsageMetrics, status string, errCode string),
) (*codeir.GlossaryCache, error) {
	if cached, err := s.facetStore.ReadGlossary(ctx, loc); err == nil && cached != nil {
		return cached, nil
	}

	key := SnapshotKey(snap.RepoID, snap.Subdirectory, snap.RefName, snap.CommitSHA)
	val, err, _ := s.glossarySF.Do(key, func() (any, error) {
		// Double-check 磁盘/LRU 缓存，避免并发临界窗口重复调用
		if cached, rErr := s.facetStore.ReadGlossary(ctx, loc); rErr == nil && cached != nil {
			return cached, nil
		}

		glossary, usage, genErr := llm.GenerateGlossary(ctx, llmClient, fastEndpoint, ir)
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

		storagePath, writeErr := s.facetStore.WriteGlossary(ctx, loc, glossary)
		if writeErr != nil {
			return nil, writeErr
		}

		snapCopy := *snap
		snapCopy.GlossaryPath = storagePath
		snapCopy.GlossaryModel = glossary.ModelName
		snapCopy.LastAccessedAt = time.Now()
		_ = s.snapRepo.UpsertSnapshot(ctx, &snapCopy)

		return glossary, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*codeir.GlossaryCache), nil
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
