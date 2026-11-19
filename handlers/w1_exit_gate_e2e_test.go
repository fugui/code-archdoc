package handlers_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	"code-archdoc/pkg/publisher"
	"code-archdoc/pkg/security"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

// TestW1ExitGateFullVerticalLoop 执行 M4 作战卡 AC-M4-04 W1 Exit Gate 五段全链路纵向闭环验收：
// 1. cpp_order_service 真实 Git 仓库同步 & C/C++/Proto Code IR 抽取缓存；
// 2. Verifier 校验生效（伪造符号标 unverified、明文凭据阻断发布返 422）；
// 3. Canvas Studio 证据源码高亮、手工直编去敏、锁定 ADR 槽位并验证重生成字节级保留；
// 4. 审批发布落盘至 publish_dir（含 YAML Frontmatter 与 /archdoc/docs/:id/evidence?... 深链）、ZIP 导出与一键撤回；
// 5. 模拟 code-bench 符号链接挂载浏览验证 Frontmatter 与深链可读。
func TestW1ExitGateFullVerticalLoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "w1-exit-gate-jwt-secret-key-0123456789"
	config.AppConfig.Auth.StandaloneMode = true

	hexKey := hex.EncodeToString([]byte("archdoc-w1-exit-gate-32byte-key!"))
	cipher, err := security.NewCipher(hexKey, "", false)
	if err != nil {
		t.Fatalf("初始化加密器失败: %v", err)
	}

	// 将 cpp_order_service 夹具复制为真实本地 Git 仓库，由 DefaultGitSyncer 执行真实 Git 同步
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixtureSrc := filepath.Clean(filepath.Join(wd, "..", "testdata", "golden", "fixtures", "cpp_order_service"))
	originGitDir := filepath.Join(t.TempDir(), "cpp_order_service_origin")
	copyDirRecursive(t, fixtureSrc, originGitDir)
	initLocalGitRepo(t, originGitDir)

	tmpData := t.TempDir()
	docTypesDir := filepath.Join(tmpData, "doc_types")
	irCacheDir := filepath.Join(tmpData, "ir_cache")
	sandboxDir := filepath.Join(tmpData, "sandbox_dir")
	publishDir := filepath.Join(tmpData, "publish_dir")

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
	publishSink, err := publisher.NewLocalFSPublishSink(publishDir)
	if err != nil {
		t.Fatal(err)
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
	llmSvc := services.NewLLMConfigService(govStore, cipher)
	docTypeSvc := services.NewDocTypeService(reg, docTypeStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, services.DefaultGitSyncer{}, sandboxDir, 1024*1024)
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 10 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, cipher, llmClient, 2,
	)
	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, services.DefaultGitSyncer{}, agg, sandboxDir)
	publishSvc := services.NewPublishService(docStore, docTypeStore, masterStore, govStore, publishSink)

	_ = commonAudit.Close(ctx)
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    256,
		BatchSize:   1,
		FlushPeriod: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = commonAudit.Close(context.Background())
	})

	router := gin.New()
	router.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithDocumentServices(revSvc, previewSvc, publishSvc)
	appHandlers.RegisterRoutes(router, db)

	// 准备测试部门、管理员、仓库负责人、普通用户与 cpp_order_service 仓库记录
	dept := models.Department{Name: "订单核心研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	adminUser := models.User{Username: "w1_admin", Email: "w1_admin@example.com", Name: "平台管理员", Password: "x", Roles: datatypes.JSON(`["user","archdoc_admin"]`), IsActive: true, DepartmentID: &dept.ID}
	ownerUser := models.User{Username: "w1_owner", Email: "w1_owner@example.com", Name: "订单仓负责人", Password: "x", Roles: datatypes.JSON(`["user"]`), IsActive: true, DepartmentID: &dept.ID}
	normalUser := models.User{Username: "w1_reader", Email: "w1_reader@example.com", Name: "普通读者", Password: "x", Roles: datatypes.JSON(`["user"]`), IsActive: true, DepartmentID: &dept.ID}
	for _, u := range []*models.User{&adminUser, &ownerUser, &normalUser} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}

	repo := models.Repository{
		Name:         "cpp-order-service",
		URL:          originGitDir,
		Branch:       "master",
		OwnerID:      ownerUser.ID,
		DepartmentID: dept.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	adminTok := mustSignToken(t, adminUser, false, []string{"user", "archdoc_admin"})
	ownerTok := mustSignToken(t, ownerUser, false, []string{"user"})
	readerTok := mustSignToken(t, normalUser, false, []string{"user"})

	// Mock OpenAI Compatible LLM Server：
	// phase=1：首次生成，在 adr_constraints_debt 故意注入明文 sk- 凭据与伪造符号以验证 Verifier 门禁；
	// phase=2：重生成，输出干净更新文本，验证 locked 章节不被覆盖且 auto 章节已更新。
	var llmPhase atomic.Int32
	llmPhase.Store(1)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		// 探活请求（max_tokens=1）直接回显模型名
		if req.MaxTokens == 1 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    "probe-1",
				"model": req.Model,
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": "ok"}},
				},
			})
			return
		}

		userPrompt := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				userPrompt += m.Content + "\n"
			}
		}

		var contentJSON string
		if llmPhase.Load() == 1 {
			if strings.Contains(userPrompt, "adr_constraints_debt") {
				contentJSON = `{"markdown":"### 架构决策与约束\n当前订单服务依赖测试凭据 sk-proj-LeakedLiveSecretToken1234567890ABCD 与伪造函数 ` + "`order::GhostPayment::NonExistentCall`" + `，用于触发 W1 Verifier 校验。","cited_anchors":[{"file_path":"src/order/order_service.cpp","symbol_kind":"cpp_func","fq_symbol":"order::OrderService::CreateOrder"}]}`
			} else {
				contentJSON = `{"markdown":"### 核心服务边界\n订单核心逻辑由 ` + "`src/order/order_service.cpp`" + ` 中的 ` + "`order::OrderService`" + ` 实现，通过 ` + "`proto/order.proto`" + ` 暴露 RPC 契约。\n\n` + "```mermaid\nflowchart LR\n  Client --> OrderService\n```" + `","cited_anchors":[{"file_path":"src/order/order_service.cpp","symbol_kind":"cpp_class","fq_symbol":"order::OrderService"}]}`
			}
		} else {
			contentJSON = `{"markdown":"### 更新后的架构说明 (v2)\n基于最新代码提交已自动刷新非锁定章节，引用 ` + "`src/order/order_service.cpp`" + ` 与 ` + "`proto/order.proto`" + ` 真实实现。","cited_anchors":[{"file_path":"src/order/order_service.cpp","symbol_kind":"cpp_class","fq_symbol":"order::OrderService"}]}`
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-w1",
			"model": req.Model,
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": contentJSON}},
			},
			"usage": map[string]any{
				"prompt_tokens":     800,
				"completion_tokens": 180,
			},
		})
	}))
	defer mockLLM.Close()

	// 配置并探活 flagship / fast 模型（验证 F-14 脱敏与探活链路）
	for _, tier := range []string{"flagship", "fast"} {
		w := doJSONReq(t, router, http.MethodPut, "/api/admin/llm-configs/"+tier, adminTok, map[string]any{
			"base_url":            mockLLM.URL + "/v1",
			"model_name":          "mock-" + tier + "-model",
			"api_key":             "sk-test-live-key-99887766",
			"timeout_seconds":     60,
			"max_retries":         1,
			"max_tokens_per_task": 300000,
			"max_files_per_task":  2000,
			"is_active":           true,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("配置 %s LLM 失败: status=%d body=%s", tier, w.Code, w.Body.String())
		}
		var cfgVO services.LLMConfigVO
		mustUnmarshal(t, w.Body.Bytes(), &cfgVO)
		if !cfgVO.HasAPIKey || strings.Contains(w.Body.String(), "sk-test-live-key-99887766") {
			t.Fatalf("LLM 配置响应未脱敏: %s", w.Body.String())
		}

		pw := doJSONReq(t, router, http.MethodPost, "/api/admin/llm-configs/"+tier+"/probe", adminTok, map[string]any{})
		if pw.Code != http.StatusOK {
			t.Fatalf("探活 %s 失败: status=%d body=%s", tier, pw.Code, pw.Body.String())
		}
		var probeVO services.LLMProbeResult
		mustUnmarshal(t, pw.Body.Bytes(), &probeVO)
		if !probeVO.Reachable {
			t.Fatalf("预期探活成功，实际失败: %+v", probeVO)
		}
	}

	// ── 步骤 1：触发 cpp_order_service 生成内置 system-architecture，验证 Git 同步 + Code IR 缓存 ──
	genResp := doJSONReq(t, router, http.MethodPost, "/api/tasks/generate", ownerTok, map[string]any{
		"repo_id":      repo.ID,
		"subdirectory": "",
		"ref_name":     "master",
		"doc_types":    []string{"system-architecture"},
		"mode":         "generate",
	})
	if genResp.Code != http.StatusAccepted {
		t.Fatalf("触发生成任务失败: status=%d body=%s", genResp.Code, genResp.Body.String())
	}
	var task1 services.GenTaskDetailVO
	mustUnmarshal(t, genResp.Body.Bytes(), &task1)

	task1Done := waitForTaskTerminal(t, router, ownerTok, task1.ID)
	if task1Done.Status != models.TaskStatusReady {
		t.Fatalf("首次生成任务未就绪: status=%s err=%s", task1Done.Status, task1Done.ErrorMessage)
	}
	if cppCounter.calls.Load() == 0 {
		t.Fatalf("预期 C++ Tree-Sitter 抽取器至少被调用一次")
	}
	var facetCount int64
	if err := db.Model(&models.CodeIRFacet{}).Count(&facetCount).Error; err != nil || facetCount == 0 {
		t.Fatalf("预期 archdoc_code_ir_facets 已落库缓存分片，实际 count=%d err=%v", facetCount, err)
	}

	// ── 步骤 2：验证 Verifier 生效（伪造符号标 unverified + 明文凭据阻断发布返 422） ──
	listDocsResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents?repo_id=%d", repo.ID), ownerTok, nil)
	if listDocsResp.Code != http.StatusOK {
		t.Fatalf("查询文档列表失败: %d %s", listDocsResp.Code, listDocsResp.Body.String())
	}
	var docList services.DocumentListVO
	mustUnmarshal(t, listDocsResp.Body.Bytes(), &docList)
	if len(docList.Items) == 0 {
		t.Fatalf("预期至少生成 1 篇 system-architecture 草稿文档")
	}
	docID := docList.Items[0].ID

	// 普通用户此时不能查看草稿（返回 403 DRAFT_ACCESS_DENIED）
	readerDraftResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d", docID), readerTok, nil)
	if readerDraftResp.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问未发布草稿应返回 403，实际 %d", readerDraftResp.Code)
	}

	docResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d", docID), ownerTok, nil)
	if docResp.Code != http.StatusOK {
		t.Fatalf("获取文档详情失败: %d %s", docResp.Code, docResp.Body.String())
	}
	var docDetail services.DocumentDetailVO
	mustUnmarshal(t, docResp.Body.Bytes(), &docDetail)

	if docDetail.ViewingRevision.CanApprove {
		t.Fatalf("含明文 sk- 凭据的草稿必须被 Verifier 阻断 (can_approve=false)")
	}
	if docDetail.ViewingRevision.VerifierReport.BlockerCount == 0 {
		t.Fatalf("预期 Verifier 检出明文凭据 blocker_count > 0，实际 report=%+v", docDetail.ViewingRevision.VerifierReport)
	}
	foundUnverified := false
	for _, u := range docDetail.ViewingRevision.VerifierReport.UnverifiedRefs {
		if strings.Contains(u.Value, "GhostPayment") {
			foundUnverified = true
			break
		}
	}
	if !foundUnverified {
		t.Fatalf("预期 Verifier 将伪造符号标记为 unverified_refs，实际=%+v", docDetail.ViewingRevision.VerifierReport.UnverifiedRefs)
	}

	// 尝试发布 -> 硬拦截 422 VERIFIER_BLOCKED 且 publish_dir 无文件
	blockedPub := doJSONReq(t, router, http.MethodPost, fmt.Sprintf("/api/documents/%d/publish", docID), ownerTok, map[string]any{})
	if blockedPub.Code != http.StatusUnprocessableEntity {
		t.Fatalf("明文凭据草稿发布应返回 422 VERIFIER_BLOCKED，实际 %d body=%s", blockedPub.Code, blockedPub.Body.String())
	}

	// ── 步骤 3：证据源码高亮预览 + 手工直编去敏 + 锁定 ADR + 重生成不覆盖锁定节 ──
	previewResp := doJSONReq(
		t,
		router,
		http.MethodGet,
		fmt.Sprintf("/api/documents/%d/source-preview?file=src/order/order_service.cpp&commit=%s&line_start=1&line_end=15", docID, docDetail.ViewingRevision.CommitSHA),
		ownerTok,
		nil,
	)
	if previewResp.Code != http.StatusOK {
		t.Fatalf("源码证据预览失败: %d %s", previewResp.Code, previewResp.Body.String())
	}
	var previewVO services.SourcePreviewVO
	mustUnmarshal(t, previewResp.Body.Bytes(), &previewVO)
	if !strings.Contains(previewVO.Content, "OrderService") || previewVO.HighlightStart != 1 {
		t.Fatalf("源码预览内容或高亮行号异常: %+v", previewVO)
	}

	// 验证目录穿越拦截 403 PATH_ESCAPE
	escapeResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d/source-preview?file=../../../etc/passwd", docID), ownerTok, nil)
	if escapeResp.Code != http.StatusForbidden {
		t.Fatalf("路径穿越必须返回 403 PATH_ESCAPE，实际 %d", escapeResp.Code)
	}

	// 手工直编 adr_constraints_debt 去除凭据与伪造符号，并锁定该章节
	lockedADRContent := "### 人工定稿 ADR 决策\n1. 订单状态机严格采用数据库乐观锁防并发超卖。\n2. 严禁在文档与配置中写入明文密钥。"
	editResp := doJSONReq(t, router, http.MethodPut, fmt.Sprintf("/api/documents/%d/sections/adr_constraints_debt", docID), ownerTok, map[string]any{
		"base_revision_no": docDetail.CurrentRevisionNo,
		"markdown":         lockedADRContent,
		"ownership":        "locked",
		"change_summary":   "清除敏感凭据并锁定人工 ADR 决策",
	})
	if editResp.Code != http.StatusOK {
		t.Fatalf("手工直编章节失败: %d %s", editResp.Code, editResp.Body.String())
	}
	var editedDoc services.DocumentDetailVO
	mustUnmarshal(t, editResp.Body.Bytes(), &editedDoc)
	if !editedDoc.ViewingRevision.CanApprove {
		t.Fatalf("清除凭据后 Verifier 应放行审批，实际 report=%+v", editedDoc.ViewingRevision.VerifierReport)
	}

	// 切换 Mock LLM 到阶段 2 并触发重新生成：确认命中 Code IR 缓存且 locked ADR 章节逐字节不被覆盖！
	callsBeforeRegen := cppCounter.calls.Load()
	llmPhase.Store(2)

	regenResp := doJSONReq(t, router, http.MethodPost, "/api/tasks/generate", ownerTok, map[string]any{
		"repo_id":      repo.ID,
		"subdirectory": "",
		"ref_name":     "master",
		"doc_types":    []string{"system-architecture"},
		"mode":         "regenerate",
	})
	if regenResp.Code != http.StatusAccepted {
		t.Fatalf("触发重生成失败: %d %s", regenResp.Code, regenResp.Body.String())
	}
	var task2 services.GenTaskDetailVO
	mustUnmarshal(t, regenResp.Body.Bytes(), &task2)
	task2Done := waitForTaskTerminal(t, router, ownerTok, task2.ID)
	if task2Done.Status != models.TaskStatusReady {
		t.Fatalf("重生成任务未就绪: status=%s err=%s", task2Done.Status, task2Done.ErrorMessage)
	}
	if cppCounter.calls.Load() != callsBeforeRegen {
		t.Fatalf("同一 commit 重生成应直接复用 Code IR 缓存，期望 C++ 解析调用数不变(%d)，实际 %d", callsBeforeRegen, cppCounter.calls.Load())
	}

	afterRegenResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d", docID), ownerTok, nil)
	var afterRegenDoc services.DocumentDetailVO
	mustUnmarshal(t, afterRegenResp.Body.Bytes(), &afterRegenDoc)

	var adrAfterRegen, autoAfterRegen string
	for _, s := range afterRegenDoc.Sections {
		if s.SlotID == "adr_constraints_debt" {
			adrAfterRegen = s.Markdown
		}
		if s.SlotID == "overview_boundaries" {
			autoAfterRegen = s.Markdown
		}
	}
	if adrAfterRegen != lockedADRContent {
		t.Fatalf("重生成覆盖了已锁定 ADR 章节！期望=%q 实际=%q", lockedADRContent, adrAfterRegen)
	}
	if !strings.Contains(autoAfterRegen, "更新后的架构说明 (v2)") {
		t.Fatalf("重生成未刷新 auto 章节，实际=%q", autoAfterRegen)
	}

	// ── 步骤 4 & 5：审批发布落盘至 publish_dir + 符号链接挂载验证 + ZIP 导出 + 一键撤回 ──
	pubResp := doJSONReq(t, router, http.MethodPost, fmt.Sprintf("/api/documents/%d/publish", docID), ownerTok, map[string]any{})
	if pubResp.Code != http.StatusOK {
		t.Fatalf("审批发布失败: %d %s", pubResp.Code, pubResp.Body.String())
	}
	var pubVO services.PublishResultVO
	mustUnmarshal(t, pubResp.Body.Bytes(), &pubVO)
	if pubVO.Status != models.DocStatusApproved || pubVO.PublishedRelPath == "" {
		t.Fatalf("发布返回状态异常: %+v", pubVO)
	}

	// 模拟 code-bench 文档中心符号链接挂载 publish_dir 并验证 Frontmatter 与平台深链
	benchDocsMountRoot := t.TempDir()
	symlinkMountPath := filepath.Join(benchDocsMountRoot, "archdoc")
	if err := os.Symlink(publishDir, symlinkMountPath); err != nil {
		t.Fatalf("创建 code-bench 模拟符号链接失败: %v", err)
	}
	mountedMDPath := filepath.Join(symlinkMountPath, filepath.FromSlash(pubVO.PublishedRelPath))
	publishedBytes, err := os.ReadFile(mountedMDPath)
	if err != nil {
		t.Fatalf("通过 code-bench 符号链接读取发布文档失败: %v", err)
	}
	publishedText := string(publishedBytes)
	if !strings.HasPrefix(publishedText, "---\n") || !strings.Contains(publishedText, `source_module: "archdoc"`) {
		t.Fatalf("发布文档缺失标准 YAML Frontmatter: %s", publishedText)
	}
	expectedDeepLinkPrefix := fmt.Sprintf("/archdoc/docs/%d/evidence?", docID)
	if !strings.Contains(publishedText, expectedDeepLinkPrefix) {
		t.Fatalf("发布文档缺失平台证据深链 %q: %s", expectedDeepLinkPrefix, publishedText)
	}

	// 普通用户在发布后可只读查看文档
	readerPubResp := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d", docID), readerTok, nil)
	if readerPubResp.Code != http.StatusOK {
		t.Fatalf("发布后普通用户应可只读访问文档，实际 %d", readerPubResp.Code)
	}

	// 批量 ZIP 导出包含 INDEX.md 与已发布文档
	zipResp := doJSONReq(t, router, http.MethodPost, "/api/exports/zip", readerTok, map[string]any{
		"document_ids":  []uint{docID},
		"include_index": true,
	})
	if zipResp.Code != http.StatusOK {
		t.Fatalf("ZIP 导出失败: %d %s", zipResp.Code, zipResp.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(zipResp.Body.Bytes()), int64(zipResp.Body.Len()))
	if err != nil {
		t.Fatalf("解析导出 ZIP 失败: %v", err)
	}
	zipNames := make(map[string]bool)
	for _, f := range zr.File {
		zipNames[f.Name] = true
	}
	if !zipNames["INDEX.md"] || !zipNames[pubVO.PublishedRelPath] {
		t.Fatalf("ZIP 缺少 INDEX.md 或目标文档，实际条目=%v", zipNames)
	}

	// 一键撤回发布：落盘文件清除且普通用户再次访问返回 403
	unpubResp := doJSONReq(t, router, http.MethodPost, fmt.Sprintf("/api/documents/%d/unpublish", docID), ownerTok, map[string]any{})
	if unpubResp.Code != http.StatusOK {
		t.Fatalf("撤回发布失败: %d %s", unpubResp.Code, unpubResp.Body.String())
	}
	if _, statErr := os.Stat(mountedMDPath); !os.IsNotExist(statErr) {
		t.Fatalf("撤回发布后 publish_dir 文档应已清除，实际 statErr=%v", statErr)
	}
	readerAfterUnpub := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/documents/%d", docID), readerTok, nil)
	if readerAfterUnpub.Code != http.StatusForbidden {
		t.Fatalf("撤回后普通用户再次访问应返回 403，实际 %d", readerAfterUnpub.Code)
	}
}

func mustSignToken(t *testing.T, u models.User, isSuperAdmin bool, roles []string) string {
	t.Helper()
	tok, err := commonAuth.GenerateTokenWithEmployeeID(
		u.ID, u.Username, u.Email, u.Name, u.EmployeeID, isSuperAdmin, roles,
		config.AppConfig.Auth.JWTSecret, time.Hour,
	)
	if err != nil {
		t.Fatalf("签发 JWT 失败: %v", err)
	}
	return tok
}

func doJSONReq(t *testing.T, router *gin.Engine, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func mustUnmarshal(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("反序列化 JSON 失败: %v\nbody=%s", err, string(raw))
	}
}

func waitForTaskTerminal(t *testing.T, router *gin.Engine, token string, taskID uint) services.GenTaskDetailVO {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w := doJSONReq(t, router, http.MethodGet, fmt.Sprintf("/api/tasks/%d", taskID), token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("轮询任务详情失败: %d %s", w.Code, w.Body.String())
		}
		var detail services.GenTaskDetailVO
		mustUnmarshal(t, w.Body.Bytes(), &detail)
		switch detail.Status {
		case models.TaskStatusReady, models.TaskStatusFailed, models.TaskStatusCanceled:
			return detail
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待任务 #%d 进入终态超时", taskID)
	return services.GenTaskDetailVO{}
}

func copyDirRecursive(t *testing.T, srcDir, dstDir string) {
	t.Helper()
	if err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	}); err != nil {
		t.Fatalf("复制夹具目录失败: %v", err)
	}
}

func initLocalGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v 失败: %v\n%s", args, err, string(out))
		}
	}
	runGit("init", "-b", "master")
	runGit("config", "user.name", "W1 Gate Tester")
	runGit("config", "user.email", "w1-gate@example.com")
	runGit("add", ".")
	runGit("commit", "-m", "init cpp_order_service golden fixture")
}
