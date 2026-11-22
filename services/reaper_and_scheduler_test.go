package services_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/services"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"
)

// TestResourceReaperProtectsActiveLeasesAndEnforcesQuota 验证 F-27 双级 LRU 配额回收与活跃租约硬保护：
// 1. 活跃快照租约的沙箱目录与分片文件完好无损；
// 2. 非活跃旧草稿分片优先于已发布快照分片被 os.Remove 并标记 evicted_at；
// 3. 磁盘占用降至低水位（75%）以下。
func TestResourceReaperProtectsActiveLeasesAndEnforcesQuota(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("初始化隔离测试库失败: %v", err)
	}
	ctx := context.Background()

	snapStore := models.NewSnapshotStore(db)
	govStore := models.NewGovernanceStore(db)
	leaseMgr := services.NewActiveLeaseManager()

	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "sandbox_dir")
	irCacheDir := filepath.Join(tmpDir, "ir_cache")
	publishDir := filepath.Join(tmpDir, "publish_dir")
	for _, d := range []string{sandboxDir, irCacheDir, publishDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("创建临时目录失败: %v", err)
		}
	}

	// 1. 构造 3 个仓库沙箱目录：repo_101 持活跃租约，repo_102 最旧非活跃，repo_103 较新非活跃（各 10KB）
	payload10KB := make([]byte, 10*1024)
	for i := range payload10KB {
		payload10KB[i] = 'A'
	}
	writeFileWithAge := func(p string, data []byte, age time.Duration) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", p, err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s failed: %v", p, err)
		}
		ts := time.Now().Add(-age)
		_ = os.Chtimes(p, ts, ts)
		_ = os.Chtimes(filepath.Dir(p), ts, ts)
	}

	sbActive := filepath.Join(sandboxDir, "repo_101", "data.bin")
	sbOldest := filepath.Join(sandboxDir, "repo_102", "data.bin")
	sbNewer := filepath.Join(sandboxDir, "repo_103", "data.bin")
	writeFileWithAge(sbActive, payload10KB, 48*time.Hour)
	writeFileWithAge(sbOldest, payload10KB, 36*time.Hour)
	writeFileWithAge(sbNewer, payload10KB, 1*time.Hour)

	// 持有 repo_101 活跃租约
	releaseRepo101 := leaseMgr.AcquireRepo(101)
	defer releaseRepo101()

	// 2. 构造 3 个快照与分片（各 10KB）：
	//    - snapActive: 持有活跃快照租约（绝对保护）
	//    - snapDraftOld: 非活跃旧草稿分片（>24h 未访问，第一优先级回收）
	//    - snapPublished: 被已发布文档引用的分片（第二优先级，若第一优先级清完后低于低水位则应保留）
	dept := commonModels.Department{Name: "测试研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatalf("创建部门失败: %v", err)
	}
	user := commonModels.User{Username: "reaper_owner", Password: "x", Name: "Owner", DepartmentID: &dept.ID}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	repo := commonModels.Repository{Name: "reaper-repo", URL: "ssh://git@example/reaper.git", Branch: "master", DepartmentID: dept.ID, OwnerID: user.ID, IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatalf("创建仓库失败: %v", err)
	}

	now := time.Now()
	mkSnap := func(sha string, accessAge time.Duration) *models.RepoSnapshot {
		snap := &models.RepoSnapshot{
			RepoID:          repo.ID,
			Subdirectory:    "",
			RefName:         "master",
			CommitSHA:       sha,
			IRSchemaVersion: "1.0",
			Status:          "ready",
			LastAccessedAt:  now.Add(-accessAge),
		}
		if err := db.Create(snap).Error; err != nil {
			t.Fatalf("创建快照失败: %v", err)
		}
		return snap
	}

	snapActive := mkSnap("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 72*time.Hour)
	snapDraftOld := mkSnap("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 48*time.Hour)
	snapPublished := mkSnap("cccccccccccccccccccccccccccccccccccccccc", 60*time.Hour)

	releaseSnapActive := leaseMgr.AcquireSnapshot(snapActive.ID)
	defer releaseSnapActive()

	// 为 snapPublished 创建一条已发布文档与修订关联
	doc := &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: 1,
		Title:             "Published Doc",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("创建文档失败: %v", err)
	}
	pubRev := &models.DocumentRevision{
		DocumentID:        doc.ID,
		RevisionNo:        1,
		SnapshotID:        snapPublished.ID,
		CommitSHA:         snapPublished.CommitSHA,
		DocTypeRevisionID: 1,
		Source:            models.RevSourceAIGenerate,
		FullMarkdown:      "# ok",
		VerifierPassed:    true,
		CanApprove:        true,
		CreatedBy:         user.ID,
	}
	if err := db.Create(pubRev).Error; err != nil {
		t.Fatalf("创建发布修订失败: %v", err)
	}
	if err := db.Model(doc).Updates(map[string]any{
		"current_revision_id":   pubRev.ID,
		"published_revision_id": pubRev.ID,
		"published_commit_sha":  snapPublished.CommitSHA,
	}).Error; err != nil {
		t.Fatalf("更新文档发布引用失败: %v", err)
	}

	mkFacet := func(snapID uint, rel string, age time.Duration) (*models.CodeIRFacet, string) {
		absPath := filepath.Join(irCacheDir, rel)
		writeFileWithAge(absPath, payload10KB, age)
		f := &models.CodeIRFacet{
			SnapshotID:      snapID,
			FacetName:       "symbols",
			StoragePath:     absPath,
			ContentSHA256:   "sha256:test",
			ItemCount:       10,
			SizeBytes:       int64(len(payload10KB)),
			CompressedBytes: int64(len(payload10KB)),
			LastAccessedAt:  now.Add(-age),
		}
		if err := db.Create(f).Error; err != nil {
			t.Fatalf("创建分片失败: %v", err)
		}
		return f, absPath
	}

	facetActive, pathActive := mkFacet(snapActive.ID, "active/symbols.json.gz", 72*time.Hour)
	facetDraftOld, pathDraftOld := mkFacet(snapDraftOld.ID, "draft_old/symbols.json.gz", 48*time.Hour)
	facetPublished, pathPublished := mkFacet(snapPublished.ID, "published/symbols.json.gz", 60*time.Hour)

	// 配额设为 28KB（总占用 30KB > 90% 高水位 25.2KB；低水位 75% = 21KB；清理 1 份 10KB 后剩 20KB <= 21KB）
	reaper := services.NewResourceReaper(services.ReaperConfig{
		SandboxDir:      sandboxDir,
		IRCacheDir:      irCacheDir,
		PublishDir:      publishDir,
		SandboxMaxBytes: 28 * 1024,
		IRCacheMaxBytes: 28 * 1024,
		ReapInterval:    50 * time.Millisecond,
	}, snapStore, govStore, leaseMgr, nil)

	res, err := reaper.Reap(ctx)
	if err != nil {
		t.Fatalf("执行 Reap 失败: %v", err)
	}

	// 断言 1：活跃租约的沙箱目录与分片文件完好无损
	if _, err := os.Stat(sbActive); err != nil {
		t.Fatalf("活跃租约沙箱 repo_101 不应被回收: %v", err)
	}
	if _, err := os.Stat(pathActive); err != nil {
		t.Fatalf("活跃租约分片文件不应被回收: %v", err)
	}
	var dbActiveFacet models.CodeIRFacet
	if err := db.First(&dbActiveFacet, facetActive.ID).Error; err != nil {
		t.Fatalf("查询活跃分片失败: %v", err)
	}
	if dbActiveFacet.EvictedAt != nil {
		t.Fatalf("活跃租约分片不应被标记 evicted_at")
	}

	// 断言 2：非活跃最旧沙箱 repo_102 被回收，较新的 repo_103 在降至低水位后保留
	if _, err := os.Stat(filepath.Dir(sbOldest)); !os.IsNotExist(err) {
		t.Fatalf("非活跃最旧沙箱 repo_102 应已被回收")
	}
	if _, err := os.Stat(sbNewer); err != nil {
		t.Fatalf("降至低水位后 repo_103 应保留: %v", err)
	}

	// 断言 3：非活跃旧草稿分片优先于已发布快照分片被 os.Remove 并标 evicted_at
	if _, err := os.Stat(pathDraftOld); !os.IsNotExist(err) {
		t.Fatalf("非活跃旧草稿分片文件应已被 os.Remove")
	}
	var dbDraftFacet models.CodeIRFacet
	if err := db.First(&dbDraftFacet, facetDraftOld.ID).Error; err != nil {
		t.Fatalf("查询旧草稿分片失败: %v", err)
	}
	if dbDraftFacet.EvictedAt == nil {
		t.Fatalf("非活跃旧草稿分片必须标记 evicted_at")
	}

	// 即使 snapPublished 的 last_accessed_at 更旧（60h vs 48h），因属于第二优先级，清理草稿分片后已低于低水位，必须保留！
	if _, err := os.Stat(pathPublished); err != nil {
		t.Fatalf("第一优先级清理后已达低水位，已发布分片不应被误删: %v", err)
	}
	var dbPubFacet models.CodeIRFacet
	if err := db.First(&dbPubFacet, facetPublished.ID).Error; err != nil {
		t.Fatalf("查询已发布分片失败: %v", err)
	}
	if dbPubFacet.EvictedAt != nil {
		t.Fatalf("已发布分片不应标记 evicted_at")
	}

	if res.ReapedSandboxes != 1 || res.ReapedIRFacets != 1 || res.SkippedActiveLeases < 2 || res.FreedBytes < 20*1024 {
		t.Fatalf("回收统计不符合预期: %+v", res)
	}
}

// TestSchedulerInteractivePreemptsBatchAndQueuesOverConcurrency 验证 F-27 双优先级调度：
// max_concurrency = 2，先塞入 10 个 batch 任务占满执行槽，再提交 2 个 interactive 高优任务，
// 断言高优任务在下一空闲槽优先于剩余 8 个批量任务执行，且全部 12 个入队任务无一失败。
func TestSchedulerInteractivePreemptsBatchAndQueuesOverConcurrency(t *testing.T) {
	sched := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      2,
		MaxUserConcurrency:  2,
		InteractiveQueueCap: 16,
		BatchQueueCap:       32,
		HighBurstRatio:      4,
	})
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	sched.Start(rootCtx)
	defer func() {
		cancelRoot()
		sched.Stop()
	}()

	gate := make(chan struct{})
	var startedFirstTwo sync.WaitGroup
	startedFirstTwo.Add(2)

	var (
		mu              sync.Mutex
		executionOrder  []string
		completedCount  atomic.Int32
		allDone         sync.WaitGroup
		firstTwoRunning atomic.Int32
	)
	allDone.Add(12)

	// 先塞入 10 个 batch 低优任务（前 2 个立即占满 2 个执行槽并阻塞在 gate 上）
	for i := 0; i < 10; i++ {
		idx := i
		jobID := "batch-" + string(rune('0'+idx))
		err := sched.Enqueue(services.ScheduledJob{
			ID:       jobID,
			UserID:   100,
			Priority: models.TaskPriorityBatch,
			Run: func(_ context.Context) {
				defer allDone.Done()
				if firstTwoRunning.Add(1) <= 2 {
					startedFirstTwo.Done()
					<-gate
				}
				mu.Lock()
				executionOrder = append(executionOrder, jobID)
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				completedCount.Add(1)
			},
		})
		if err != nil {
			t.Fatalf("提交 batch 任务 %d 失败: %v", idx, err)
		}
	}

	// 等待前 2 个 batch 任务稳稳占满执行槽
	startedFirstTwo.Wait()

	// 再提交 2 个 interactive 高优任务
	for i := 0; i < 2; i++ {
		idx := i
		jobID := "interactive-" + string(rune('0'+idx))
		err := sched.Enqueue(services.ScheduledJob{
			ID:       jobID,
			UserID:   200,
			Priority: models.TaskPriorityInteractive,
			Run: func(_ context.Context) {
				defer allDone.Done()
				mu.Lock()
				executionOrder = append(executionOrder, jobID)
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				completedCount.Add(1)
			},
		})
		if err != nil {
			t.Fatalf("提交 interactive 任务 %d 失败: %v", idx, err)
		}
	}

	// 放行前 2 个阻塞的 batch 任务，让高优任务抢占下一空闲执行槽
	close(gate)
	allDone.Wait()

	if completedCount.Load() != 12 {
		t.Fatalf("全部 12 个入队任务必须排队完成无一失败，实际完成 %d", completedCount.Load())
	}

	// 验证 2 个 interactive 任务均出现在前 4 位（紧随最初占槽的 2 个 batch 任务，先于剩余 8 个 batch 任务）
	interactivePositions := make([]int, 0, 2)
	for pos, name := range executionOrder {
		if len(name) >= 11 && name[:11] == "interactive" {
			interactivePositions = append(interactivePositions, pos)
		}
	}
	if len(interactivePositions) != 2 {
		t.Fatalf("未记录到 2 个 interactive 任务执行轨迹: %v", executionOrder)
	}
	for _, pos := range interactivePositions {
		if pos > 3 {
			t.Fatalf("高优 interactive 任务应在下一空闲槽优先于剩余 8 个 batch 任务执行，实际位置=%d, 全序=%v", pos, executionOrder)
		}
	}
}

// TestSchedulerQueueFullAndGracefulShutdown 验证队列满返回 ErrQueueFull (429) 以及优雅停机零 goroutine 泄漏 (AC-M6-03)。
func TestSchedulerQueueFullAndGracefulShutdown(t *testing.T) {
	baseGoroutines := runtime.NumGoroutine()

	sched := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      1,
		MaxUserConcurrency:  1,
		InteractiveQueueCap: 1,
		BatchQueueCap:       1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	block := make(chan struct{})
	running := make(chan struct{})

	if err := sched.Enqueue(services.ScheduledJob{
		ID:       "hold",
		UserID:   1,
		Priority: models.TaskPriorityInteractive,
		Run: func(_ context.Context) {
			close(running)
			<-block
		},
	}); err != nil {
		t.Fatalf("第 1 个任务入队失败: %v", err)
	}
	<-running

	// 填满容量为 1 的 HighQ
	if err := sched.Enqueue(services.ScheduledJob{
		ID:       "queued-1",
		UserID:   2,
		Priority: models.TaskPriorityInteractive,
		Run:      func(_ context.Context) {},
	}); err != nil {
		t.Fatalf("第 2 个任务入队失败: %v", err)
	}

	// 再入队应触发 ErrQueueFull
	err := sched.Enqueue(services.ScheduledJob{
		ID:       "overflow",
		UserID:   3,
		Priority: models.TaskPriorityInteractive,
		Run:      func(_ context.Context) {},
	})
	if !errors.Is(err, services.ErrQueueFull) {
		t.Fatalf("队列满时应返回 ErrQueueFull，实际: %v", err)
	}

	close(block)
	cancel()
	sched.Stop()

	// 验证后台调度协程全部干净退出
	time.Sleep(30 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	if afterGoroutines > baseGoroutines+4 {
		t.Fatalf("检测到潜在 goroutine 泄漏: before=%d after=%d", baseGoroutines, afterGoroutines)
	}
}

// TestSchedulerReserveSlotsAtomicAndStopOnDrop 验证两阶段预留的容量校验、回滚释放、并发提交与停机二次保护（T-ADR08-05）。
func TestSchedulerReserveSlotsAtomicAndStopOnDrop(t *testing.T) {
	sched := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      1,
		MaxUserConcurrency:  1,
		InteractiveQueueCap: 3,
		BatchQueueCap:       2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	block := make(chan struct{})
	started := make(chan struct{})
	if err := sched.Enqueue(services.ScheduledJob{
		ID:       "hold",
		UserID:   1,
		Priority: models.TaskPriorityInteractive,
		Run: func(_ context.Context) {
			close(started)
			<-block
		},
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	// 1. 预留 2 个交互槽位，此时剩余仅 1 个；尝试再预留 2 个应直接返 ErrQueueFull
	_, rollback1, err := sched.ReserveSlots(models.TaskPriorityInteractive, 2)
	if err != nil {
		t.Fatalf("首次预留 2 槽位失败: %v", err)
	}
	if _, _, err := sched.ReserveSlots(models.TaskPriorityInteractive, 2); !errors.Is(err, services.ErrQueueFull) {
		t.Fatalf("期望容量不足返 ErrQueueFull，实际: %v", err)
	}

	// 2. 回滚首次预留后应释放 2 个槽位，重新可预留 3 个
	rollback1()
	rollback1() // 幂等
	commit2, rollback2, err := sched.ReserveSlots(models.TaskPriorityInteractive, 3)
	if err != nil {
		t.Fatalf("回滚后应可重新预留 3 槽位，实际: %v", err)
	}
	defer rollback2()

	var ran atomic.Int32
	var done sync.WaitGroup
	done.Add(3)
	jobs := make([]services.ScheduledJob, 3)
	for i := 0; i < 3; i++ {
		jobs[i] = services.ScheduledJob{
			ID:       "r-" + string(rune('a'+i)),
			UserID:   uint(10 + i),
			Priority: models.TaskPriorityInteractive,
			Run: func(_ context.Context) {
				defer done.Done()
				ran.Add(1)
			},
		}
	}
	if err := commit2(jobs); err != nil {
		t.Fatalf("commit2 失败: %v", err)
	}
	close(block)
	done.Wait()
	if ran.Load() != 3 {
		t.Fatalf("期望 3 个预留任务全部执行，实际 %d", ran.Load())
	}

	// 3. 停机前已预留但停机后 commit 应返回 ErrSchedulerStopped 并触发全部 OnDrop
	commit3, _, err := sched.ReserveSlots(models.TaskPriorityInteractive, 2)
	if err != nil {
		t.Fatalf("预留 2 槽位失败: %v", err)
	}
	cancel()
	sched.Stop()

	var dropped atomic.Int32
	stopJobs := []services.ScheduledJob{
		{ID: "d1", OnDrop: func() { dropped.Add(1) }, Run: func(_ context.Context) {}},
		{ID: "d2", OnDrop: func() { dropped.Add(1) }, Run: func(_ context.Context) {}},
	}
	if err := commit3(stopJobs); !errors.Is(err, services.ErrSchedulerStopped) {
		t.Fatalf("停机后 commit 期望返 ErrSchedulerStopped，实际: %v", err)
	}
	if dropped.Load() != 2 {
		t.Fatalf("停机后 commit 期望全部执行 OnDrop (2)，实际: %d", dropped.Load())
	}
}
