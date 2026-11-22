package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	"code-common/backend/gitsync"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type countingGitSyncer struct {
	fixtureDir string
	commitSHA  string
	calls      atomic.Int32
	delay      time.Duration
}

func (g *countingGitSyncer) Sync(ctx context.Context, opts gitsync.SyncOptions) (*gitsync.SyncResult, error) {
	g.calls.Add(1)
	if g.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(g.delay):
		}
	}
	return &gitsync.SyncResult{
		LocalPath:      g.fixtureDir,
		ResolvedBranch: opts.Branch,
		CommitSHA:      g.commitSHA,
	}, nil
}

type countingExtractor struct {
	inner codeir.LanguageExtractor
	calls atomic.Int32
}

func (c *countingExtractor) Name() string                 { return "counting-" + c.inner.Name() }
func (c *countingExtractor) SupportedLanguages() []string { return c.inner.SupportedLanguages() }
func (c *countingExtractor) ExtractFile(ctx context.Context, file codeir.FileEntry, content []byte) (*codeir.ExtractionResult, error) {
	c.calls.Add(1)
	return c.inner.ExtractFile(ctx, file, content)
}

type m2TestEnv struct {
	db          *gorm.DB
	router      *gin.Engine
	docTypeSvc  *services.DocTypeService
	genSvc      *services.GenerationOrchestrator
	gitSyncer   *countingGitSyncer
	cppCounter  *countingExtractor
	owner       models.User
	ownerTok    string
	otherUser   models.User
	otherTok    string
	repo        models.Repository
	docTypesDir string
}

func setupM2TestEnv(t *testing.T, maxLLMConcurrency int) *m2TestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m2-unit-test-jwt-secret-key-9876543210"
	config.AppConfig.Auth.StandaloneMode = true

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixtureDir := filepath.Clean(filepath.Join(wd, "..", "testdata", "golden", "fixtures", "cpp_order_service"))

	tmpData := t.TempDir()
	docTypesDir := filepath.Join(tmpData, "doc_types")
	irCacheDir := filepath.Join(tmpData, "ir_cache")
	sandboxDir := filepath.Join(tmpData, "sandbox_dir")

	reg, err := doctype.NewRegistry(docTypesDir)
	if err != nil {
		t.Fatal(err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(irCacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}

	cppCounter := &countingExtractor{inner: treesitter.NewCppExtractor()}
	agg := codeir.NewAggregator(cppCounter, treesitter.NewCExtractor())

	gitSyncer := &countingGitSyncer{
		fixtureDir: fixtureDir,
		commitSHA:  "b1b2b3b4b5b6b7b8b9b0b1b2b3b4b5b6b7b8b9b0",
	}

	masterStore := models.NewSharedMasterStore(db)
	docTypeStore := models.NewDocTypeStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	llmSvc := services.NewLLMConfigService(govStore).WithDocTypeRepo(docTypeStore)
	docTypeSvc := services.NewDocTypeService(reg, docTypeStore)

	if _, err := docTypeSvc.SeedAndSyncAll(context.Background()); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, gitSyncer, sandboxDir, 1048576)
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 10 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, llmClient, maxLLMConcurrency,
	)

	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    256,
		BatchSize:   1,
		FlushPeriod: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		genSvc.WaitAll()
		_ = commonAudit.Close(context.Background())
	})

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc)
	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)

	dept := models.Department{Name: "订单研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "order_owner", "订单负责人", []string{"user"}, &dept.ID)
	other := createTestUser(t, db, "other_dev", "旁观研发", []string{"user"}, &dept.ID)
	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "cpp_order_service",
		URL:          "git@git.internal:core/cpp_order_service.git",
		OwnerID:      owner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	return &m2TestEnv{
		db:          db,
		router:      r,
		docTypeSvc:  docTypeSvc,
		genSvc:      genSvc,
		gitSyncer:   gitSyncer,
		cppCounter:  cppCounter,
		owner:       owner,
		ownerTok:    issueTestToken(t, owner),
		otherUser:   other,
		otherTok:    issueTestToken(t, other),
		repo:        repo,
		docTypesDir: docTypesDir,
	}
}

func configureMockLLMInDB(t *testing.T, env *m2TestEnv, baseURL string, maxTokensPerTask, maxFilesPerTask int) {
	t.Helper()
	cfg := models.LLMConfig{
		Name:             "M2-Default-Model",
		BaseURL:          baseURL,
		ModelName:        "mock-deepseek-v3",
		APIKey:           "sk-mock-llm-token-9999",
		APIKeyMasked:     "sk-****9999",
		TimeoutSeconds:   30,
		MaxRetries:       0,
		MaxTokensPerTask: maxTokensPerTask,
		MaxFilesPerTask:  maxFilesPerTask,
		IsActive:         true,
		IsDefault:        true,
	}
	if err := env.db.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
}

func TestDocTypeSeedSyncAndReadOnlyAPI(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	// 1. 再次 SeedAndSyncAll 幂等（0 条新修订）
	res2, err := env.docTypeSvc.SeedAndSyncAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res2.SyncedCount != 5 || res2.NewRevisions != 0 {
		t.Fatalf("二次启动同步应严格幂等: %+v", res2)
	}

	// 2. GET /api/doc-types 返回 5 类内置 DocType 与完整槽位和修订元信息（AC-M2-04）
	req := httptest.NewRequest(http.MethodGet, "/api/doc-types?active_only=true", nil)
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/doc-types 失败: %d %s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Items []services.DocTypeSummaryVO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Items) != 5 {
		t.Fatalf("期望返回 5 类内置 DocType，实际 %d", len(listResp.Items))
	}
	totalSlots := 0
	for _, item := range listResp.Items {
		if item.LatestRevisionNo != 1 || item.LatestRevisionID == nil || item.LatestAggregateHash == "" {
			t.Fatalf("DocType %s 修订元信息缺失: %+v", item.Slug, item)
		}
		totalSlots += item.SlotCount
	}
	if totalSlots != 19 {
		t.Fatalf("期望总槽位数 19，实际 %d", totalSlots)
	}

	// 3. 修改其中一个目录文件后再次 SyncAll，仅变动类型生成 revision_no=2
	promptFile := filepath.Join(env.docTypesDir, "system-architecture", "generation_prompt.md")
	orig, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptFile, append(orig, []byte("\n// updated prompt\n")...), 0640); err != nil {
		t.Fatal(err)
	}
	res3, err := env.docTypeSvc.SyncAll(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res3.NewRevisions != 1 {
		t.Fatalf("期望仅 1 个 DocType 生成新修订，实际 %+v", res3)
	}

	// 验证 GET /api/doc-types/system-architecture/revisions 返回 2 条修订 (2, 1)
	reqRev := httptest.NewRequest(http.MethodGet, "/api/doc-types/system-architecture/revisions", nil)
	reqRev.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recRev := httptest.NewRecorder()
	env.router.ServeHTTP(recRev, reqRev)
	if recRev.Code != http.StatusOK {
		t.Fatalf("GET revisions 失败: %d %s", recRev.Code, recRev.Body.String())
	}
	var revResp struct {
		Items []services.DocTypeRevisionSummaryVO `json:"items"`
	}
	if err := json.Unmarshal(recRev.Body.Bytes(), &revResp); err != nil {
		t.Fatal(err)
	}
	if len(revResp.Items) != 2 || revResp.Items[0].RevisionNo != 2 || revResp.Items[1].RevisionNo != 1 {
		t.Fatalf("期望修订列表按倒序 [2, 1]，实际 %+v", revResp.Items)
	}

	// 验证 GET /api/doc-types/UpperCase 返回 400 INVALID_SLUG
	reqBad := httptest.NewRequest(http.MethodGet, "/api/doc-types/UpperCase", nil)
	reqBad.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recBad := httptest.NewRecorder()
	env.router.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("非法 slug 期望返回 400，实际 %d", recBad.Code)
	}
}

func TestBudgetCircuitBreakerZeroLLMCallEndToEnd(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	var llmCalls atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"mock","choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer mockLLM.Close()

	// 将 max_tokens_per_task 设为极小值 100（SC-09）
	configureMockLLMInDB(t, env, mockLLM.URL, 100, 2000)

	body, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("期望 POST /api/tasks/generate 返回 202，实际 %d (%s)", rec.Code, rec.Body.String())
	}
	var created services.GenTaskDetailVO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	env.genSvc.WaitAll()

	// 查询任务状态：应为 failed (TOKEN_BUDGET_EXCEEDED)，含 budget_advice，且 Mock LLM 收到请求数严格为 0！
	reqGet := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks/%d", created.ID), nil)
	reqGet.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recGet := httptest.NewRecorder()
	env.router.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks/:id 失败: %d", recGet.Code)
	}
	var detail services.GenTaskDetailVO
	if err := json.Unmarshal(recGet.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != models.TaskStatusFailed || detail.ErrorCode != llm.ErrorCodeTokenBudgetExceeded {
		t.Fatalf("SC-09 期望任务熔断为 failed/TOKEN_BUDGET_EXCEEDED，实际: status=%s code=%s msg=%s",
			detail.Status, detail.ErrorCode, detail.ErrorMessage)
	}
	if detail.BudgetAdvice == nil || len(detail.BudgetAdvice.Suggestions) == 0 || len(detail.BudgetAdvice.TopDirectories) == 0 {
		t.Fatalf("SC-09 期望 budget_advice 非空且含目录与裁剪建议: %+v", detail.BudgetAdvice)
	}
	if llmCalls.Load() != 0 {
		t.Fatalf("SC-09 违规：预算熔断触发后 Mock LLM 收到请求数必须严格为 0，实际 %d", llmCalls.Load())
	}
}

func TestSingleflightMultiDocTypeReuseSnapshotAndGlossary(t *testing.T) {
	env := setupM2TestEnv(t, 4)
	env.gitSyncer.delay = 40 * time.Millisecond

	var glossaryCalls atomic.Int32
	var slotCalls atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userMsg := ""
		if len(payload.Messages) >= 2 {
			userMsg = payload.Messages[1].Content
		}

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(userMsg, "repo_structure_for_glossary") {
			glossaryCalls.Add(1)
			time.Sleep(40 * time.Millisecond)
			content := `{"module_summaries":[{"path":"src/order","summary":"核心订单与支付服务"}],"terms":[{"term":"OrderService","definition":"核心订单处理类"}]}`
			resp, _ := json.Marshal(map[string]any{
				"model":   "mock-fast",
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
				"usage":   map[string]any{"prompt_tokens": 200, "completion_tokens": 50},
			})
			_, _ = w.Write(resp)
			return
		}

		slotCalls.Add(1)
		content := `{"markdown":"核心实现位于 ` + "`src/order/order_service.cpp`" + ` 中的 ` + "`order::OrderService::Pay`" + `，用于完成订单支付状态流转与共享内存上报。","cited_anchors":[{"file_path":"src/order/order_service.cpp","fq_symbol":"order::OrderService::Pay"}]}`
		resp, _ := json.Marshal(map[string]any{
			"model":   "mock-flagship",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 400, "completion_tokens": 120, "prompt_tokens_details": map[string]int{"cached_tokens": 250}},
		})
		_, _ = w.Write(resp)
	}))
	defer mockLLM.Close()

	configureMockLLMInDB(t, env, mockLLM.URL, 500000, 2000)

	// 并发提交两笔请求（system-architecture 与 data-and-contract），验证 Singleflight 严格只做 1 次 Git 同步、1 次 L1 抽取、1 次 Glossary 生成（SC-02）
	var wg sync.WaitGroup
	taskIDs := make([]uint, 2)
	docTypeArgs := [][]string{{"system-architecture"}, {"data-and-contract"}}

	for i := 0; i < 2; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"repo_id":   env.repo.ID,
				"doc_types": docTypeArgs[idx],
			})
			req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+env.ownerTok)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Errorf("任务 %d 提交失败: %d %s", idx, rec.Code, rec.Body.String())
				return
			}
			var vo services.GenTaskDetailVO
			_ = json.Unmarshal(rec.Body.Bytes(), &vo)
			taskIDs[idx] = vo.ID
		}()
	}
	wg.Wait()
	env.genSvc.WaitAll()

	if env.gitSyncer.calls.Load() != 1 {
		t.Fatalf("SC-02 违规：同快照并发触发 Git 同步应仅执行 1 次，实际 %d 次", env.gitSyncer.calls.Load())
	}
	// cpp_order_service 有 5 个 cpp/h 文件，只抽取 1 次应恰好解析 5 次
	if env.cppCounter.calls.Load() != 5 {
		t.Fatalf("SC-02 违规：L1 C++ 抽取应仅执行 1 轮（5 个文件），实际调用 %d 次", env.cppCounter.calls.Load())
	}
	if glossaryCalls.Load() != 1 {
		t.Fatalf("SC-02 违规：glossary.json.gz 应仅调用 LLM 生成 1 次，实际 %d 次", glossaryCalls.Load())
	}

	// 验证两笔任务均进入 ready 且落库了 DocumentRevision 与 LLMUsageLog
	for _, tid := range taskIDs {
		detail, err := env.genSvc.GetTaskDetail(context.Background(), tid)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Status != models.TaskStatusReady {
			t.Fatalf("任务 #%d 期望 status=ready，实际 %s (%s)", tid, detail.Status, detail.ErrorMessage)
		}
	}

	var usageCount int64
	if err := env.db.Model(&models.LLMUsageLog{}).Count(&usageCount).Error; err != nil {
		t.Fatal(err)
	}
	if usageCount < int64(1+slotCalls.Load()) {
		t.Fatalf("期望 archdoc_llm_usage_logs 记录全部调用埋点，实际 %d", usageCount)
	}
}

func TestTaskCancelStopsSubsequentLLMCalls(t *testing.T) {
	// 并发设为 1，确保槽位按序发出以便精准观察取消后后续槽位零请求
	env := setupM2TestEnv(t, 1)

	firstSlotStarted := make(chan struct{}, 1)
	var slotCalls atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userMsg := ""
		if len(payload.Messages) >= 2 {
			userMsg = payload.Messages[1].Content
		}

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(userMsg, "repo_structure_for_glossary") {
			_, _ = w.Write([]byte(`{"model":"mock-fast","choices":[{"message":{"content":"{\"module_summaries\":[],\"terms\":[]}"}}]}`))
			return
		}

		n := slotCalls.Add(1)
		if n == 1 {
			select {
			case firstSlotStarted <- struct{}{}:
			default:
			}
			// 首槽位阻塞 500ms（响应客户端取消）
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		_, _ = w.Write([]byte(`{"model":"mock-flagship","choices":[{"message":{"content":"{\"markdown\":\"ok\"}"}}]}`))
	}))
	defer mockLLM.Close()

	configureMockLLMInDB(t, env, mockLLM.URL, 500000, 2000)

	// 提交 5 槽位的 system-architecture 任务
	body, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("提交任务失败: %d %s", rec.Code, rec.Body.String())
	}
	var created services.GenTaskDetailVO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// 等待首槽位进入阻塞态后立刻取消任务
	select {
	case <-firstSlotStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("等待首槽位启动超时")
	}

	reqCancel := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/cancel", created.ID), nil)
	reqCancel.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recCancel := httptest.NewRecorder()
	env.router.ServeHTTP(recCancel, reqCancel)
	if recCancel.Code != http.StatusOK {
		t.Fatalf("POST /api/tasks/:id/cancel 期望返回 200，实际 %d (%s)", recCancel.Code, recCancel.Body.String())
	}

	env.genSvc.WaitAll()

	detail, err := env.genSvc.GetTaskDetail(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != models.TaskStatusCanceled {
		t.Fatalf("取消后任务状态应为 canceled，实际 %s", detail.Status)
	}
	if slotCalls.Load() != 1 {
		t.Fatalf("取消后剩余槽位不应再发出 LLM 请求：期望 slotCalls=1，实际 %d", slotCalls.Load())
	}

	// 对已终态任务再次取消应返回 409 CONFLICT
	reqCancelAgain := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/cancel", created.ID), nil)
	reqCancelAgain.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recCancelAgain := httptest.NewRecorder()
	env.router.ServeHTTP(recCancelAgain, reqCancelAgain)
	if recCancelAgain.Code != http.StatusConflict {
		t.Fatalf("重复取消终态任务期望返回 409，实际 %d", recCancelAgain.Code)
	}
}

func TestSingleSlotRetryPreservesOtherSlotsByteForByte(t *testing.T) {
	env := setupM2TestEnv(t, 1)

	var failSecondSlot atomic.Bool
	failSecondSlot.Store(true)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userMsg := ""
		if len(payload.Messages) >= 2 {
			userMsg = payload.Messages[1].Content
		}

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(userMsg, "repo_structure_for_glossary") {
			_, _ = w.Write([]byte(`{"model":"mock-fast","choices":[{"message":{"content":"{\"module_summaries\":[],\"terms\":[]}"}}]}`))
			return
		}

		if strings.Contains(userMsg, "slot_id: `call_chain_guide`") {
			if failSecondSlot.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"transient upstream failure"}`))
				return
			}
			content := `{"markdown":"【重试补齐】核心调用链路经 ` + "`src/main.cpp`" + ` 进入 ` + "`order::OrderService::Pay`" + ` 完成支付状态流转与共享内存写入。","cited_anchors":[{"file_path":"src/order/order_service.cpp","fq_symbol":"order::OrderService::Pay"}]}`
			resp, _ := json.Marshal(map[string]any{
				"model":   "mock-fast",
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
				"usage":   map[string]any{"prompt_tokens": 180, "completion_tokens": 60},
			})
			_, _ = w.Write(resp)
			return
		}

		// 第 1 / 第 3 槽位正常返回唯一内容
		var md string
		if strings.Contains(userMsg, "slot_id: `directory_map`") {
			md = "第 1 槽位固定内容：仓库包含 `CMakeLists.txt`、`proto/order.proto`、`src/order/order_service.cpp` 与 `src/main.cpp`。"
		} else {
			md = "第 3 槽位固定内容：新增接口需先修改 `proto/order.proto`，再更新 `include/order/order_service.h` 与 `src/order/order_service.cpp`。"
		}
		contentJSON, _ := json.Marshal(map[string]any{
			"markdown": md,
			"cited_anchors": []map[string]string{
				{"file_path": "src/order/order_service.cpp", "fq_symbol": "order::OrderService::Pay"},
			},
		})
		resp, _ := json.Marshal(map[string]any{
			"model":   "mock-fast",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": string(contentJSON)}}},
			"usage":   map[string]any{"prompt_tokens": 150, "completion_tokens": 50},
		})
		_, _ = w.Write(resp)
	}))
	defer mockLLM.Close()

	configureMockLLMInDB(t, env, mockLLM.URL, 500000, 2000)

	// 1. 生成 3 槽位的 code-walkthrough，其中第 2 槽位失败、第 1/3 槽位成功
	body, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"code-walkthrough"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("提交生成任务失败: %d %s", rec.Code, rec.Body.String())
	}
	var created services.GenTaskDetailVO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	env.genSvc.WaitAll()

	detail1, err := env.genSvc.GetTaskDetail(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var failedSlotID uint
	var completedSlotID uint
	var docID uint
	for _, st := range detail1.SlotTasks {
		if st.DocumentID != nil {
			docID = *st.DocumentID
		}
		if st.SlotID == "call_chain_guide" {
			if st.Status != models.SlotTaskFailed {
				t.Fatalf("期望第 2 槽位为 failed，实际 %s", st.Status)
			}
			failedSlotID = st.ID
		} else {
			if st.Status != models.SlotTaskCompleted {
				t.Fatalf("期望槽位 %s 为 completed，实际 %s", st.SlotID, st.Status)
			}
			completedSlotID = st.ID
		}
	}
	if failedSlotID == 0 || docID == 0 {
		t.Fatalf("未找到失败槽位或生成文档: slots=%+v", detail1.SlotTasks)
	}

	// 读取 rev 1 的第 1/3 槽位内容与 SHA256 基线
	docStore := models.NewDocumentStore(env.db)
	rev1, _, err := docStore.GetRevisionWithSections(context.Background(), docID, 1)
	if err != nil {
		t.Fatal(err)
	}
	var rev1Sections []models.DocumentSectionSnapshot
	_ = json.Unmarshal(rev1.SectionsJSON, &rev1Sections)
	rev1BySlot := make(map[string]models.DocumentSectionSnapshot)
	for _, s := range rev1Sections {
		rev1BySlot[s.SlotID] = s
	}

	// 尝试对已成功槽位发起重试应返回 409 CONFLICT
	reqBadRetry := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/slots/%d/retry", created.ID, completedSlotID), nil)
	reqBadRetry.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recBadRetry := httptest.NewRecorder()
	env.router.ServeHTTP(recBadRetry, reqBadRetry)
	if recBadRetry.Code != http.StatusConflict {
		t.Fatalf("对已完成槽位重试期望返回 409，实际 %d", recBadRetry.Code)
	}

	// 2. 解除 Mock 故障，对第 2 槽位发起独立重试
	failSecondSlot.Store(false)
	reqRetry := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/slots/%d/retry", created.ID, failedSlotID), nil)
	reqRetry.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recRetry := httptest.NewRecorder()
	env.router.ServeHTTP(recRetry, reqRetry)
	if recRetry.Code != http.StatusAccepted {
		t.Fatalf("单槽位重试期望返回 202，实际 %d (%s)", recRetry.Code, recRetry.Body.String())
	}
	env.genSvc.WaitAll()

	// 3. 读取 rev 2，断言第 2 槽位已补齐且第 1/3 槽位内容与 content_sha256 严格字节级不变！
	rev2, _, err := docStore.GetRevisionWithSections(context.Background(), docID, 2)
	if err != nil {
		t.Fatalf("重试后未生成新修订 rev 2: %v", err)
	}
	var rev2Sections []models.DocumentSectionSnapshot
	_ = json.Unmarshal(rev2.SectionsJSON, &rev2Sections)
	rev2BySlot := make(map[string]models.DocumentSectionSnapshot)
	for _, s := range rev2Sections {
		rev2BySlot[s.SlotID] = s
	}

	if !strings.Contains(rev2BySlot["call_chain_guide"].Markdown, "重试补齐") {
		t.Fatalf("第 2 槽位重试内容未落库: %+v", rev2BySlot["call_chain_guide"])
	}
	for _, preservedSlot := range []string{"directory_map", "modification_recipes"} {
		before := rev1BySlot[preservedSlot]
		after := rev2BySlot[preservedSlot]
		if before.Markdown != after.Markdown || before.ContentSHA256 != after.ContentSHA256 {
			t.Fatalf("单槽位重试破坏了其他槽位 %s 的字节级不变性！beforeSHA=%s afterSHA=%s",
				preservedSlot, before.ContentSHA256, after.ContentSHA256)
		}
	}
	if rev2.SlotCoverageRate != 1.0 || !rev2.VerifierPassed {
		t.Fatalf("重试补齐后 Verifier 期望全绿 (coverage=1.0)，实际 coverage=%v passed=%v report=%s",
			rev2.SlotCoverageRate, rev2.VerifierPassed, string(rev2.VerifierReportJSON))
	}
}

func TestGenerateTaskRBACAndParamValidation(t *testing.T) {
	env := setupM2TestEnv(t, 2)

	// 1. 非负责人发起生成返回 403 FORBIDDEN (SC-12)
	body1, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body1))
	req1.Header.Set("Authorization", "Bearer "+env.otherTok)
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	env.router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusForbidden {
		t.Fatalf("非负责人发起生成期望 403，实际 %d", rec1.Code)
	}

	// 2. 子目录传入 ../../etc 返回 400 INVALID_SUBDIR (SC-06)
	body2, _ := json.Marshal(map[string]any{
		"repo_id":      env.repo.ID,
		"subdirectory": "../../etc",
		"doc_types":    []string{"system-architecture"},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	env.router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest || !strings.Contains(rec2.Body.String(), handlers.ErrCodeInvalidSubdir) {
		t.Fatalf("非法子目录期望 400 INVALID_SUBDIR，实际 %d (%s)", rec2.Code, rec2.Body.String())
	}

	// 3. 勾选不存在或已停用的 DocType 返回 400 DOC_TYPE_INACTIVE
	body3, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"non-existent-doctype"},
	})
	req3 := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body3))
	req3.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	env.router.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest || !strings.Contains(rec3.Body.String(), handlers.ErrCodeDocTypeInactive) {
		t.Fatalf("不存在的 DocType 期望 400 DOC_TYPE_INACTIVE，实际 %d (%s)", rec3.Code, rec3.Body.String())
	}
}

func TestListTasksFilterSummaryAndEnrichment(t *testing.T) {
	env := setupM2TestEnv(t, 2)

	// 1. 构造父批量任务、子任务、成功/失败/运行中任务，覆盖多用户与不同任务类型
	parentTask := models.GenTask{
		RepoID:       env.repo.ID,
		Subdirectory: "",
		RefName:      "master",
		TaskKind:     models.TaskKindBatch,
		Priority:     models.TaskPriorityBatch,
		DocTypesJSON: []byte(`["system-architecture"]`),
		Status:       models.TaskStatusGenerating,
		TriggeredBy:  env.owner.ID,
	}
	if err := env.db.Create(&parentTask).Error; err != nil {
		t.Fatal(err)
	}

	childTask := models.GenTask{
		ParentTaskID: &parentTask.ID,
		RepoID:       env.repo.ID,
		Subdirectory: "services/order",
		RefName:      "master",
		TaskKind:     models.TaskKindGenerate,
		Priority:     models.TaskPriorityBatch,
		DocTypesJSON: []byte(`["system-architecture"]`),
		Status:       models.TaskStatusReady,
		TriggeredBy:  env.owner.ID,
	}
	failedTask := models.GenTask{
		RepoID:       env.repo.ID,
		Subdirectory: "",
		RefName:      "master",
		TaskKind:     models.TaskKindRegenerate,
		Priority:     models.TaskPriorityInteractive,
		DocTypesJSON: []byte(`["code-walkthrough"]`),
		Status:       models.TaskStatusFailed,
		ErrorCode:    "LLM_TIMEOUT",
		ErrorMessage: "上游推理超时",
		TriggeredBy:  env.otherUser.ID,
	}
	for _, item := range []*models.GenTask{&childTask, &failedTask} {
		if err := env.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}

	doList := func(tok, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks"+query, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	// 2. owner 查询 top_level_only=true&status=active：仅返回父批量任务且富化 repo_name/triggered_by_name/can_write/batch_summary/summary
	recActive := doList(env.ownerTok, "?top_level_only=true&status=active")
	if recActive.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?top_level_only=true&status=active 期望 200，实际 %d (%s)", recActive.Code, recActive.Body.String())
	}
	var activeResp services.TaskListResponse
	if err := json.Unmarshal(recActive.Body.Bytes(), &activeResp); err != nil {
		t.Fatal(err)
	}
	if activeResp.Total != 1 || len(activeResp.Items) != 1 {
		t.Fatalf("期望 active 顶层任务 1 条，实际 total=%d items=%d", activeResp.Total, len(activeResp.Items))
	}
	if activeResp.Summary == nil || activeResp.Summary.Total != 2 || activeResp.Summary.Active != 1 || activeResp.Summary.Failed != 1 {
		t.Fatalf("Summary 统计不准确: %+v", activeResp.Summary)
	}
	item := activeResp.Items[0]
	if item.ID != parentTask.ID || item.RepoName != env.repo.Name || item.TriggeredByName != env.owner.Name || !item.CanWrite {
		t.Fatalf("任务富化字段不符合预期: %+v", item)
	}
	if item.BatchSummary == nil || item.BatchSummary.TotalChildren != 1 || item.BatchSummary.CompletedChildren != 1 {
		t.Fatalf("批量子任务摘要未聚合: %+v", item.BatchSummary)
	}

	// 3. 非负责人 otherUser 查询 mine=true：仅返回由自己触发的 failedTask，且 can_write 为 false
	recMine := doList(env.otherTok, "?mine=true")
	if recMine.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?mine=true 期望 200，实际 %d (%s)", recMine.Code, recMine.Body.String())
	}
	var mineResp services.TaskListResponse
	if err := json.Unmarshal(recMine.Body.Bytes(), &mineResp); err != nil {
		t.Fatal(err)
	}
	if mineResp.Total != 1 || len(mineResp.Items) != 1 || mineResp.Items[0].ID != failedTask.ID {
		t.Fatalf("mine=true 过滤结果不符合预期: %+v", mineResp)
	}
	if mineResp.Items[0].CanWrite {
		t.Fatalf("非负责人查看整仓任务 can_write 应为 false")
	}
	if mineResp.Items[0].TriggeredByName != env.otherUser.Name {
		t.Fatalf("期望 triggered_by_name=%s，实际 %s", env.otherUser.Name, mineResp.Items[0].TriggeredByName)
	}

	// 4. task_kind 过滤
	recKind := doList(env.ownerTok, "?task_kind=regenerate")
	if recKind.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?task_kind=regenerate 期望 200，实际 %d", recKind.Code)
	}
	var kindResp services.TaskListResponse
	if err := json.Unmarshal(recKind.Body.Bytes(), &kindResp); err != nil {
		t.Fatal(err)
	}
	if kindResp.Total != 1 || kindResp.Items[0].ID != failedTask.ID {
		t.Fatalf("task_kind=regenerate 过滤结果不符合预期: %+v", kindResp)
	}

	// 5. 非法 status / task_kind 白名单拦截返回 400 INVALID_PARAM
	for _, badQuery := range []string{"?status=bogus", "?task_kind=unknown_kind"} {
		recBad := doList(env.ownerTok, badQuery)
		if recBad.Code != http.StatusBadRequest || !strings.Contains(recBad.Body.String(), handlers.ErrCodeInvalidParam) {
			t.Fatalf("非法查询 %s 期望 400 INVALID_PARAM，实际 %d (%s)", badQuery, recBad.Code, recBad.Body.String())
		}
	}
}

func TestModelRoutingPreferredModelAndStrictNoFallback(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	var defaultCalls, preferredCalls atomic.Int32
	mockDefault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultCalls.Add(1)
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userMsg := ""
		if len(payload.Messages) >= 2 {
			userMsg = payload.Messages[1].Content
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(userMsg, "repo_structure_for_glossary") {
			_, _ = w.Write([]byte(`{"model":"deepseek-v3","choices":[{"message":{"content":"{\"module_summaries\":[],\"terms\":[]}"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"deepseek-v3","choices":[{"message":{"content":"{\"markdown\":\"默认模型生成内容 ` + "`src/order/order_service.cpp`" + `\",\"cited_anchors\":[]}"}}]}`))
	}))
	defer mockDefault.Close()

	mockPreferred := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preferredCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"glm-4-plus","choices":[{"message":{"content":"{\"markdown\":\"GLM指定模型生成内容 ` + "`src/order/order_service.cpp`" + `\",\"cited_anchors\":[]}"}}]}`))
	}))
	defer mockPreferred.Close()

	if err := env.db.Create(&models.LLMConfig{
		Name: "DeepSeek-V3", BaseURL: mockDefault.URL, ModelName: "deepseek-v3",
		TimeoutSeconds: 30, MaxRetries: 0, MaxTokensPerTask: 500000, MaxFilesPerTask: 2000,
		IsActive: true, IsDefault: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Create(&models.LLMConfig{
		Name: "GLM-4", BaseURL: mockPreferred.URL, ModelName: "glm-4-plus",
		TimeoutSeconds: 30, MaxRetries: 0, MaxTokensPerTask: 500000, MaxFilesPerTask: 2000,
		IsActive: true, IsDefault: false,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 将 agents-context 显式绑定到 GLM-4
	dtDetail, err := env.docTypeSvc.GetDetail(context.Background(), "agents-context")
	if err != nil {
		t.Fatal(err)
	}
	meta := dtDetail.Meta
	meta.PreferredModel = "GLM-4"
	if _, _, _, err := env.docTypeSvc.Update(context.Background(), "agents-context", services.UpdateDocTypeRequest{
		Meta:               meta,
		TemplateMD:         dtDetail.TemplateMD,
		GenerationPromptMD: dtDetail.GenerationPromptMD,
		RefinePromptMD:     dtDetail.RefinePromptMD,
	}, env.owner.ID); err != nil {
		t.Fatal(err)
	}

	// 1. 触发 agents-context 生成：术语表走默认模型 DeepSeek-V3，3 个槽位走指定的 GLM-4
	body1, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"agents-context"},
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body1))
	req1.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	env.router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("提交指定模型生成任务失败: %d %s", rec1.Code, rec1.Body.String())
	}
	var task1 services.GenTaskDetailVO
	_ = json.Unmarshal(rec1.Body.Bytes(), &task1)
	env.genSvc.WaitAll()

	if defaultCalls.Load() != 1 || preferredCalls.Load() == 0 {
		t.Fatalf("期望术语表调用默认模型 1 次、槽位调用指定模型 GLM-4，实际 default=%d preferred=%d", defaultCalls.Load(), preferredCalls.Load())
	}

	// 验证槽位子任务记录了 model_name="GLM-4"
	reqGet1 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks/%d", task1.ID), nil)
	reqGet1.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recGet1 := httptest.NewRecorder()
	env.router.ServeHTTP(recGet1, reqGet1)
	var detail1 services.GenTaskDetailVO
	_ = json.Unmarshal(recGet1.Body.Bytes(), &detail1)
	if detail1.Status != models.TaskStatusReady || len(detail1.SlotTasks) == 0 || detail1.SlotTasks[0].ModelName != "GLM-4" {
		t.Fatalf("期望任务成功且槽位 model_name=GLM-4，实际 status=%s slots=%+v", detail1.Status, detail1.SlotTasks)
	}

	// 2. 停用默认模型 DeepSeek-V3（GLM-4 仍启用）：触发跟随默认模型的 ops-and-config，必须明确失败且绝不回退到 GLM-4！
	if err := env.db.Model(&models.LLMConfig{}).Where("name = ?", "DeepSeek-V3").Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	prevPreferredCalls := preferredCalls.Load()
	body2, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"ops-and-config"},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	env.router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("提交任务返回非 202: %d %s", rec2.Code, rec2.Body.String())
	}
	var task2 services.GenTaskDetailVO
	_ = json.Unmarshal(rec2.Body.Bytes(), &task2)
	env.genSvc.WaitAll()

	reqGet2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks/%d", task2.ID), nil)
	reqGet2.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recGet2 := httptest.NewRecorder()
	env.router.ServeHTTP(recGet2, reqGet2)
	var detail2 services.GenTaskDetailVO
	_ = json.Unmarshal(recGet2.Body.Bytes(), &detail2)
	if detail2.Status != models.TaskStatusFailed || detail2.ErrorCode != "LLM_CONFIG_INVALID" {
		t.Fatalf("默认模型停用后应明确失败 LLM_CONFIG_INVALID，实际 status=%s code=%s", detail2.Status, detail2.ErrorCode)
	}
	if preferredCalls.Load() != prevPreferredCalls {
		t.Fatalf("严禁默认模型停用后静默回退到 GLM-4！新增调用=%d", preferredCalls.Load()-prevPreferredCalls)
	}
}
