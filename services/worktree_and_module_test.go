package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/llm"
)

// TestDowngradableRWMutex_ZeroGapAndConcurrency 验证 T-ADR04-12：
// 1. 写锁通过 DowngradeToRLock 原子降级为读锁期间，其他等待写锁的协程绝不能插队抢占（零间隙窗口）；
// 2. 降级后其他等待读锁的协程可并发进入；
// 3. 多个读者并发持有读锁期间，写锁严格阻塞直到全部读者退出。
func TestDowngradableRWMutex_ZeroGapAndConcurrency(t *testing.T) {
	rw := NewDowngradableRWMutex()

	var writerActive int32
	var activeReaders int32
	var raceDetected int32

	// 阶段 1：主协程获取写锁，启动竞争写协程与竞争读协程，然后执行原子降级 DowngradeToRLock
	rw.Lock()
	atomic.StoreInt32(&writerActive, 1)

	competingWriterEntered := make(chan struct{})
	competingReaderEntered := make(chan struct{})
	var wg sync.WaitGroup

	// 竞争写者：尝试获取写锁，绝不能在主协程 DowngradeToRLock + RUnlock 完成前进入
	wg.Add(1)
	go func() {
		defer wg.Done()
		rw.Lock()
		if atomic.LoadInt32(&writerActive) != 0 || atomic.LoadInt32(&activeReaders) != 0 {
			atomic.StoreInt32(&raceDetected, 1)
		}
		atomic.StoreInt32(&writerActive, 1)
		close(competingWriterEntered)
		time.Sleep(15 * time.Millisecond)
		atomic.StoreInt32(&writerActive, 0)
		rw.Unlock()
	}()

	// 给竞争写者一点时间进入等待队列
	time.Sleep(10 * time.Millisecond)

	// 主协程完成写阶段，原子降级为读锁
	atomic.StoreInt32(&writerActive, 0)
	atomic.StoreInt32(&activeReaders, 1)
	rw.DowngradeToRLock()

	// 在主协程仍持有降级后的读锁期间，竞争写者绝对不能进入
	select {
	case <-competingWriterEntered:
		t.Fatalf("P-5 竞态漏洞：竞争写者在 DowngradeToRLock 持有读锁期间提前抢占了工作树！")
	case <-time.After(25 * time.Millisecond):
	}

	// 启动另一个读协程（由于有 waitingWriter，新读者会排队等待写者完成后再进入，防止写者饥饿）
	wg.Add(1)
	go func() {
		defer wg.Done()
		rw.RLock()
		if atomic.LoadInt32(&writerActive) != 0 {
			atomic.StoreInt32(&raceDetected, 1)
		}
		atomic.AddInt32(&activeReaders, 1)
		close(competingReaderEntered)
		atomic.AddInt32(&activeReaders, -1)
		rw.RUnlock()
	}()

	// 主协程释放降级后的读锁 -> 此时竞争写者应当获得锁，随后竞争读者获得锁
	atomic.AddInt32(&activeReaders, -1)
	rw.RUnlock()

	wg.Wait()
	if atomic.LoadInt32(&raceDetected) != 0 {
		t.Fatalf("检测到读写互斥违反（raceDetected=1）")
	}

	// 阶段 2：高并发压力测试（20 个写+降级协程 与 40 个读协程交错执行）
	var stressWG sync.WaitGroup
	for i := 0; i < 20; i++ {
		stressWG.Add(1)
		go func() {
			defer stressWG.Done()
			rw.Lock()
			if atomic.AddInt32(&writerActive, 1) != 1 || atomic.LoadInt32(&activeReaders) != 0 {
				atomic.StoreInt32(&raceDetected, 1)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&writerActive, -1)
			atomic.AddInt32(&activeReaders, 1)
			rw.DowngradeToRLock()
			if atomic.LoadInt32(&writerActive) != 0 {
				atomic.StoreInt32(&raceDetected, 1)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&activeReaders, -1)
			rw.RUnlock()
		}()
	}
	for i := 0; i < 40; i++ {
		stressWG.Add(1)
		go func() {
			defer stressWG.Done()
			rw.RLock()
			if atomic.LoadInt32(&writerActive) != 0 {
				atomic.StoreInt32(&raceDetected, 1)
			}
			atomic.AddInt32(&activeReaders, 1)
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&activeReaders, -1)
			rw.RUnlock()
		}()
	}
	stressWG.Wait()
	if atomic.LoadInt32(&raceDetected) != 0 {
		t.Fatalf("高并发压力测试下检测到读写锁竞态！")
	}
}

// TestSnapshotService_SharedRepoLevelGlossaryReuse 验证 T-ADR04-06：
// 同一仓库下多个子模块快照与整仓快照并发调用 EnsureGlossary 时，统一共享仓库根级 glossary.json.gz（Subdir=""），
// 整个批次严格只发起 1 次 Glossary LLM 调用（P-7 防腐）。
func TestSnapshotService_SharedRepoLevelGlossaryReuse(t *testing.T) {
	var glossaryLLMCalls int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&glossaryLLMCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": `{"terms":[{"symbol_or_term":"OrderService","business_name":"订单服务","description":"核心订单处理"}],"module_summaries":[{"module_path":"order","summary":"订单核心子系统"},{"module_path":"gateway","summary":"API 网关子系统"}]}`,
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	facetStore, err := codeir.NewLocalFSFacetStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	snapSvc := &SnapshotService{
		facetStore: facetStore,
	}

	llmClient := llm.NewClient()
	ep := llm.EndpointConfig{
		BaseURL:   mockServer.URL,
		APIKey:    "test-key",
		ModelName: "fast-model",
	}
	dummyIR := &codeir.CodeIRBundle{
		SchemaVersion: codeir.SchemaVersion,
		Tree:          codeir.TreeFacet{TotalFiles: 10, TotalLOC: 500},
	}

	ctx := context.Background()
	commitSHA := "a1b2c3d4e5f6"
	subdirs := []string{"order", "gateway", ""}

	for _, sub := range subdirs {
		snap := &models.RepoSnapshot{
			ID:           10,
			RepoID:       1,
			Subdirectory: sub,
			RefName:      "main",
			CommitSHA:    commitSHA,
		}
		loc := codeir.SnapshotLoc{RepoID: 1, Subdir: sub, CommitSHA: commitSHA}
		g, gErr := snapSvc.EnsureGlossary(ctx, snap, loc, dummyIR, llmClient, ep, nil)
		if gErr != nil {
			t.Fatalf("EnsureGlossary(subdir=%q) 失败: %v", sub, gErr)
		}
		if g == nil || len(g.ModuleSummaries) != 2 {
			t.Fatalf("期望返回共享的 2 个模块摘要，实际=%+v", g)
		}
		if !snapSvc.HasCachedGlossary(ctx, loc) {
			t.Fatalf("HasCachedGlossary(subdir=%q) 应命中仓库根共享缓存", sub)
		}
	}

	if got := atomic.LoadInt32(&glossaryLLMCalls); got != 1 {
		t.Fatalf("3 个范围共享术语表期望仅触发 1 次 LLM 调用，实际触发 %d 次", got)
	}
}
