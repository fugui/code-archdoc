package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

func boolPtr(v bool) *bool { return &v }

func uintPtr(v uint) *uint { return &v }

func almostEqual(a, b, eps float64) bool {
	return math.Abs(a-b) <= eps
}

// TestQualityMetricsCompareAcrossDocTypeRevisions 验证 F-26 W2 在线质量度量 API：
// 写入同一 DocType Rev 1（高修改率 0.40、低采纳率 0.50）与 Rev 2（低修改率 0.12、高采纳率 0.85）的发布与 Chat 指标
// 及 LLMUsageLog（含 cached_prompt_tokens），断言 /api/metrics/overview、/api/metrics/revisions-compare、
// /api/metrics/slot-heatmap 聚合结果小数精度与分组完全准确，并验证 /api/admin/storage/* 与 /api/drift/* 路由及 RBAC。
func TestQualityMetricsCompareAcrossDocTypeRevisions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config.AppConfig.Auth.JWTSecret = "m6-metrics-test-jwt-secret-key-0123456789"

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}
	ctx := context.Background()

	// 创建用户与角色（普通用户 + 模块管理员）
	dept := commonModels.Department{Name: "架构治理部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	normalUser := createTestUser(t, db, "reader_m6", "Reader", []string{"user"}, &dept.ID)
	adminUser := createTestUser(t, db, "admin_m6", "Admin", []string{"user", config.AdminRole}, &dept.ID)

	repo := commonModels.Repository{
		Name:         "metrics-demo-repo",
		URL:          "ssh://git@example.com/metrics-demo.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      adminUser.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	// 初始化 DocType 并构造同一 DocType (system-architecture) 的 Rev 1 与 Rev 2
	tmpDir := t.TempDir()
	reg, err := doctype.NewRegistry(filepath.Join(tmpDir, "doc_types"))
	if err != nil {
		t.Fatal(err)
	}
	dtStore := models.NewDocTypeStore(db)
	dtSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := dtSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	dt, rev1, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil || rev1 == nil {
		t.Fatalf("获取 Rev 1 失败: %v", err)
	}

	// 写入 Rev 2（优化 Prompt 后产生新修订）
	rev2 := &models.DocTypeRevision{
		DocTypeID:          dt.ID,
		DocTypeSlug:        dt.Slug,
		RevisionNo:         2,
		AggregateHash:      "rev2-hash-0002",
		MetaJSON:           rev1.MetaJSON,
		SlotsSnapshotJSON:  rev1.SlotsSnapshotJSON,
		TemplateMD:         rev1.TemplateMD,
		GenerationPromptMD: rev1.GenerationPromptMD + "\n# v2 improved prompt",
		RefinePromptMD:     rev1.RefinePromptMD,
		PromptHash:         "prompt-hash-v2",
	}
	if err := db.Create(rev2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(dt).Updates(map[string]any{
		"latest_revision_id":    rev2.ID,
		"latest_revision_no":    2,
		"latest_aggregate_hash": rev2.AggregateHash,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 写入 Rev 1 质量指标（doc 1 & doc 2）：
	// - doc_published: avg edit_diff_ratio = (0.35 + 0.45) / 2 = 0.40
	//   slot core_request_flows: (0.50 + 0.60) / 2 = 0.55; overview_boundaries: (0.20 + 0.30) / 2 = 0.25
	// - chat_proposal_resolved: 2 次（1 accept, 1 reject）-> chat_accept_rate = 0.50
	// - ai_draft_verified: 2 次（1 blocked, 1 pass；slot_coverage_rate: 0.80, 1.00 -> avg 0.90）-> verifier_block_rate = 0.50
	// - LLMUsageLog: doc 1 (prompt=10000, cached=4000, comp=2000), doc 2 (prompt=10000, cached=6000, comp=2000)
	//   => avg_tokens_per_doc = 24000/2 = 12000, avg_cache_hit_ratio = 10000/20000 = 0.50
	//
	// 写入 Rev 2 质量指标（doc 3 & doc 4）：
	// - doc_published: avg edit_diff_ratio = (0.10 + 0.14) / 2 = 0.12
	//   slot core_request_flows: (0.15 + 0.25) / 2 = 0.20; overview_boundaries: (0.05 + 0.05) / 2 = 0.05
	// - chat_proposal_resolved: 20 次（17 accept, 3 reject）-> chat_accept_rate = 0.85
	// - ai_draft_verified: 2 次（0 blocked；slot_coverage_rate: 1.00, 1.00 -> avg 1.00）-> verifier_block_rate = 0.00
	// - LLMUsageLog: doc 3 (prompt=8000, cached=6400, comp=1000), doc 4 (prompt=8000, cached=6400, comp=1000)
	//   => avg_tokens_per_doc = 18000/2 = 9000, avg_cache_hit_ratio = 12800/16000 = 0.80
	govStore := models.NewGovernanceStore(db)
	mustRecQM := func(m models.QualityMetric) {
		if err := govStore.RecordQualityMetric(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}
	mustRecUsage := func(u models.LLMUsageLog) {
		if err := govStore.RecordLLMUsage(ctx, &u); err != nil {
			t.Fatal(err)
		}
	}

	// Rev 1 指标
	mustRecQM(models.QualityMetric{
		DocumentID: 1, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventDocPublished, EditDiffRatio: 0.35, SlotCoverageRate: 0.90,
		SlotDiffRatiosJSON: datatypes.JSON(`{"core_request_flows":0.50,"overview_boundaries":0.20}`),
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 2, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventDocPublished, EditDiffRatio: 0.45, SlotCoverageRate: 0.90,
		SlotDiffRatiosJSON: datatypes.JSON(`{"core_request_flows":0.60,"overview_boundaries":0.30}`),
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 1, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventChatProposalResolved, ChatAccepted: boolPtr(true),
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 2, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventChatProposalResolved, ChatAccepted: boolPtr(false),
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 1, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventAIDraftVerified, VerifierBlocked: true, SlotCoverageRate: 0.80,
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 2, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev1.ID,
		MetricEvent: models.MetricEventAIDraftVerified, VerifierBlocked: false, SlotCoverageRate: 1.00,
	})
	mustRecUsage(models.LLMUsageLog{
		DocumentID: uintPtr(1), RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: uintPtr(rev1.ID),
		Stage: "slot_gen", ModelName: "deepseek-v3",
		PromptTokens: 10000, CachedPromptTokens: 4000, CompletionTokens: 2000, LatencyMs: 1200, Status: "success", OperatorID: adminUser.ID,
	})
	mustRecUsage(models.LLMUsageLog{
		DocumentID: uintPtr(2), RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: uintPtr(rev1.ID),
		Stage: "slot_gen", ModelName: "deepseek-v3",
		PromptTokens: 10000, CachedPromptTokens: 6000, CompletionTokens: 2000, LatencyMs: 1100, Status: "success", OperatorID: adminUser.ID,
	})

	// Rev 2 指标
	mustRecQM(models.QualityMetric{
		DocumentID: 3, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
		MetricEvent: models.MetricEventDocPublished, EditDiffRatio: 0.10, SlotCoverageRate: 1.00,
		SlotDiffRatiosJSON: datatypes.JSON(`{"core_request_flows":0.15,"overview_boundaries":0.05}`),
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 4, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
		MetricEvent: models.MetricEventDocPublished, EditDiffRatio: 0.14, SlotCoverageRate: 1.00,
		SlotDiffRatiosJSON: datatypes.JSON(`{"core_request_flows":0.25,"overview_boundaries":0.05}`),
	})
	for i := 0; i < 17; i++ {
		mustRecQM(models.QualityMetric{
			DocumentID: 3, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
			MetricEvent: models.MetricEventChatProposalResolved, ChatAccepted: boolPtr(true),
		})
	}
	for i := 0; i < 3; i++ {
		mustRecQM(models.QualityMetric{
			DocumentID: 4, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
			MetricEvent: models.MetricEventChatProposalResolved, ChatAccepted: boolPtr(false),
		})
	}
	mustRecQM(models.QualityMetric{
		DocumentID: 3, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
		MetricEvent: models.MetricEventAIDraftVerified, VerifierBlocked: false, SlotCoverageRate: 1.00,
	})
	mustRecQM(models.QualityMetric{
		DocumentID: 4, RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: rev2.ID,
		MetricEvent: models.MetricEventAIDraftVerified, VerifierBlocked: false, SlotCoverageRate: 1.00,
	})
	mustRecUsage(models.LLMUsageLog{
		DocumentID: uintPtr(3), RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: uintPtr(rev2.ID),
		Stage: "slot_gen", ModelName: "deepseek-v3",
		PromptTokens: 8000, CachedPromptTokens: 6400, CompletionTokens: 1000, LatencyMs: 900, Status: "success", OperatorID: adminUser.ID,
	})
	mustRecUsage(models.LLMUsageLog{
		DocumentID: uintPtr(4), RepoID: repo.ID, DocTypeSlug: dt.Slug, DocTypeRevisionID: uintPtr(rev2.ID),
		Stage: "slot_gen", ModelName: "deepseek-v3",
		PromptTokens: 8000, CachedPromptTokens: 6400, CompletionTokens: 1000, LatencyMs: 850, Status: "success", OperatorID: adminUser.ID,
	})

	// 装配完整路由器
	masterStore := models.NewSharedMasterStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	llmSvc := services.NewLLMConfigService(govStore)
	leaseMgr := services.NewActiveLeaseManager()
	sched := services.NewTaskScheduler(services.SchedulerConfig{MaxConcurrency: 4, MaxUserConcurrency: 2})
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	genSvc := services.NewGenerationOrchestrator(masterStore, dtStore, dtSvc, nil, taskStore, docStore, govStore, nil, 4)
	driftSvc := services.NewDriftOrchestrator(masterStore, docStore, dtStore, dtSvc, driftStore, rbacSvc, genSvc, agg, leaseMgr, filepath.Join(tmpDir, "sandbox_dir"), 15)
	pollerSvc := services.NewRemoteHeadPoller(services.PollerConfig{Enabled: false, Concurrency: 2}, driftStore, nil, nil, nil)
	metricsSvc := services.NewQualityMetricsService(govStore, dtStore)
	reaperSvc := services.NewResourceReaper(services.ReaperConfig{
		SandboxDir:      filepath.Join(tmpDir, "sandbox_dir"),
		IRCacheDir:      filepath.Join(tmpDir, "ir_cache"),
		PublishDir:      filepath.Join(tmpDir, "publish_dir"),
		SandboxMaxBytes: 1024 * 1024 * 100,
		IRCacheMaxBytes: 1024 * 1024 * 100,
	}, snapStore, govStore, leaseMgr, sched)

	appH := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, dtSvc, nil).
		WithGovernanceServices(driftSvc, pollerSvc, metricsSvc, reaperSvc)

	router := gin.New()
	appH.RegisterRoutes(router, db)

	readerToken := issueTestToken(t, normalUser)
	adminToken := issueTestToken(t, adminUser)

	doReq := func(method, target, token string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. 验证 GET /api/metrics/overview (全量混合窗口)
	w := doReq(http.MethodGet, "/api/metrics/overview?days=30&doc_type=system-architecture", readerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("overview 返回非 200: code=%d body=%s", w.Code, w.Body.String())
	}
	var overview services.MetricsOverviewVO
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	// 全量：avg_edit=(0.35+0.45+0.10+0.14)/4 = 0.26; chat_accept=18/22 = 0.8182; verifier_block=1/4=0.25; first_pass=0.75;
	// slot_cov=(0.8+1+1+1)/4=0.95; avg_tokens=(24000+18000)/4=10500; cache_hit=22800/36000=0.6333; sample=4
	if !almostEqual(overview.AvgEditDiffRatio, 0.26, 0.0005) ||
		!almostEqual(overview.ChatAcceptRate, 0.8182, 0.0005) ||
		!almostEqual(overview.VerifierBlockRate, 0.25, 0.0005) ||
		!almostEqual(overview.VerifierFirstPassRate, 0.75, 0.0005) ||
		!almostEqual(overview.AvgSlotCoverageRate, 0.95, 0.0005) ||
		overview.AvgTokensPerDoc != 10500 ||
		!almostEqual(overview.AvgCacheHitRatio, 0.6333, 0.0005) ||
		overview.SampleDocCount != 4 {
		t.Fatalf("overview 聚合结果不符预期: %+v", overview)
	}

	// 2. 验证 GET /api/metrics/revisions-compare（Rev 1 vs Rev 2 精准分组与精度）
	w = doReq(http.MethodGet, "/api/metrics/revisions-compare?doc_type=system-architecture&days=90", readerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("revisions-compare 返回非 200: code=%d body=%s", w.Code, w.Body.String())
	}
	var compare services.RevisionsCompareVO
	if err := json.Unmarshal(w.Body.Bytes(), &compare); err != nil {
		t.Fatal(err)
	}
	if len(compare.Revisions) != 2 {
		t.Fatalf("预期返回 2 个修订对比点，实际=%+v", compare)
	}
	p1, p2 := compare.Revisions[0], compare.Revisions[1]
	if p1.RevisionNo != 1 || !almostEqual(p1.AvgEditDiffRatio, 0.40, 0.0005) || !almostEqual(p1.ChatAcceptRate, 0.50, 0.0005) ||
		!almostEqual(p1.VerifierBlockRate, 0.50, 0.0005) || p1.AvgTokensPerDoc != 12000 || !almostEqual(p1.AvgCacheHitRatio, 0.50, 0.0005) || p1.SampleCount != 2 {
		t.Fatalf("Rev 1 聚合精度不符预期: %+v", p1)
	}
	if p2.RevisionNo != 2 || !almostEqual(p2.AvgEditDiffRatio, 0.12, 0.0005) || !almostEqual(p2.ChatAcceptRate, 0.85, 0.0005) ||
		!almostEqual(p2.VerifierBlockRate, 0.00, 0.0005) || p2.AvgTokensPerDoc != 9000 || !almostEqual(p2.AvgCacheHitRatio, 0.80, 0.0005) || p2.SampleCount != 2 {
		t.Fatalf("Rev 2 聚合精度不符预期: %+v", p2)
	}

	// 3. 验证 GET /api/metrics/slot-heatmap（按平均修改率降序）
	w = doReq(http.MethodGet, "/api/metrics/slot-heatmap?doc_type=system-architecture&days=30", readerToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("slot-heatmap 返回非 200: code=%d body=%s", w.Code, w.Body.String())
	}
	var heatmap services.SlotHeatmapVO
	if err := json.Unmarshal(w.Body.Bytes(), &heatmap); err != nil {
		t.Fatal(err)
	}
	if len(heatmap.Slots) != 2 {
		t.Fatalf("预期 2 个槽位热力项，实际=%+v", heatmap)
	}
	// core_request_flows: (0.50+0.60+0.15+0.25)/4 = 0.375; overview_boundaries: (0.20+0.30+0.05+0.05)/4 = 0.15
	if heatmap.Slots[0].SlotID != "core_request_flows" || !almostEqual(heatmap.Slots[0].AvgEditDiffRatio, 0.375, 0.0005) || heatmap.Slots[0].SampleCount != 4 {
		t.Fatalf("热力表第 1 槽位不符预期: %+v", heatmap.Slots[0])
	}
	if heatmap.Slots[1].SlotID != "overview_boundaries" || !almostEqual(heatmap.Slots[1].AvgEditDiffRatio, 0.15, 0.0005) || heatmap.Slots[1].SampleCount != 4 {
		t.Fatalf("热力表第 2 槽位不符预期: %+v", heatmap.Slots[1])
	}

	// 4. 验证管理接口 RBAC 与存储统计/手动回收/漂移汇总响应
	if resp := doReq(http.MethodGet, "/api/admin/storage/stats", readerToken, nil); resp.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问 /api/admin/storage/stats 应返回 403，实际 %d", resp.Code)
	}
	if resp := doReq(http.MethodPost, "/api/admin/drift/poll", readerToken, nil); resp.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问 /api/admin/drift/poll 应返回 403，实际 %d", resp.Code)
	}

	statsResp := doReq(http.MethodGet, "/api/admin/storage/stats", adminToken, nil)
	if statsResp.Code != http.StatusOK {
		t.Fatalf("管理员查询 storage stats 应返回 200，实际 %d body=%s", statsResp.Code, statsResp.Body.String())
	}
	var statsVO services.StorageStatsVO
	if err := json.Unmarshal(statsResp.Body.Bytes(), &statsVO); err != nil {
		t.Fatal(err)
	}
	if statsVO.Scheduler.MaxConcurrency != 4 || statsVO.Sandbox.MaxMB <= 0 || statsVO.IRCache.MaxMB <= 0 {
		t.Fatalf("storage stats 调度或配额信息不符预期: %+v", statsVO)
	}

	reapResp := doReq(http.MethodPost, "/api/admin/storage/reap", adminToken, nil)
	if reapResp.Code != http.StatusOK {
		t.Fatalf("管理员执行 storage reap 应返回 200，实际 %d", reapResp.Code)
	}

	summaryResp := doReq(http.MethodGet, "/api/drift/summary", readerToken, nil)
	if summaryResp.Code != http.StatusOK {
		t.Fatalf("GET /api/drift/summary 应返回 200，实际 %d", summaryResp.Code)
	}
	listDriftResp := doReq(http.MethodGet, "/api/drift/documents?page=1&page_size=25&drift_type=all", readerToken, nil)
	if listDriftResp.Code != http.StatusOK {
		t.Fatalf("GET /api/drift/documents 应返回 200，实际 %d", listDriftResp.Code)
	}
	pollResp := doReq(http.MethodPost, "/api/admin/drift/poll", adminToken, nil)
	if pollResp.Code != http.StatusAccepted {
		t.Fatalf("POST /api/admin/drift/poll 应返回 202，实际 %d", pollResp.Code)
	}
	// 5. 验证 POST /api/documents/:id/incremental-refresh RBAC 与参数边界校验
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, nil)
	appH.WithDocumentServices(revSvc, nil, nil)
	incRouter := gin.New()
	appH.RegisterRoutes(incRouter, db)

	testDoc := &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       dt.Slug,
		DocTypeRevisionID: rev2.ID,
		Title:             "RBAC Refresh Doc",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(testDoc).Error; err != nil {
		t.Fatal(err)
	}
	lockedSecJSON, _ := json.Marshal([]models.DocumentSectionSnapshot{
		{SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 概览", Ownership: models.OwnershipLocked, Markdown: "locked"},
	})
	testDocRev := &models.DocumentRevision{
		DocumentID:        testDoc.ID,
		RevisionNo:        1,
		CommitSHA:         "1111111111111111111111111111111111111111",
		DocTypeRevisionID: rev2.ID,
		Source:            models.RevSourceAIGenerate,
		SectionsJSON:      datatypes.JSON(lockedSecJSON),
		FullMarkdown:      "locked",
		VerifierPassed:    true,
		CanApprove:        true,
		CreatedBy:         adminUser.ID,
	}
	if err := db.Create(testDocRev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(testDoc).Updates(map[string]any{
		"current_revision_id":   testDocRev.ID,
		"published_revision_id": testDocRev.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	doIncReq := func(token string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/documents/%d/incremental-refresh", testDoc.ID), bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		incRouter.ServeHTTP(w, req)
		return w
	}

	// 普通非负责人触发增量刷新 -> 403 FORBIDDEN
	if r403 := doIncReq(readerToken, []byte(`{}`)); r403.Code != http.StatusForbidden {
		t.Fatalf("普通用户触发增量刷新应返回 403，实际 %d body=%s", r403.Code, r403.Body.String())
	}
	// 无可刷新非锁槽位（仅存在 locked 槽位且无 drift）-> 400 INVALID_PARAM
	if r400 := doIncReq(adminToken, []byte(`{"slot_ids":["overview_boundaries"]}`)); r400.Code != http.StatusBadRequest {
		t.Fatalf("仅指定 locked 槽位增量刷新应返回 400，实际 %d body=%s", r400.Code, r400.Body.String())
	}

	pollerSvc.Stop()
}
