package handlers_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type chatTestEnv struct {
	db         *gorm.DB
	router     *gin.Engine
	owner      models.User
	ownerTok   string
	normalUser models.User
	normalTok  string
	doc        *models.Document
	slot1Hash  string
	slot3Hash  string
}

func testSHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func setupChatTestEnv(t *testing.T, llmBaseURL string) *chatTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m5-chat-refine-test-jwt-secret-0123456789"
	config.AppConfig.Auth.StandaloneMode = true

	hexKey := hex.EncodeToString([]byte("archdoc-m5-chat-32byte-key-00005"))
	cipher, err := security.NewCipher(hexKey, "", false)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	reg, err := doctype.NewRegistry(filepath.Join(tmpDir, "doc_types"))
	if err != nil {
		t.Fatal(err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(filepath.Join(tmpDir, "ir_cache"), 0)
	if err != nil {
		t.Fatal(err)
	}
	publishSink, err := publisher.NewLocalFSPublishSink(filepath.Join(tmpDir, "publish_dir"))
	if err != nil {
		t.Fatal(err)
	}
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)

	masterStore := models.NewSharedMasterStore(db)
	dtStore := models.NewDocTypeStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)
	chatStore := models.NewChatStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	llmSvc := services.NewLLMConfigService(govStore, cipher)
	docTypeSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}

	// 配置 flagship / fast LLM 指向 Mock httptest.Server
	encKey, err := cipher.Encrypt("sk-test-chat-key-12345678")
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{models.ModelTierFlagship, models.ModelTierFast} {
		cfg := models.LLMConfig{
			Tier:             tier,
			BaseURL:          llmBaseURL,
			ModelName:        "mock-qwen-72b",
			APIKeyCipher:     encKey,
			APIKeyMasked:     "sk-****5678",
			TimeoutSeconds:   30,
			MaxRetries:       0,
			MaxTokensPerTask: 200000,
			MaxFilesPerTask:  1000,
			IsActive:         true,
		}
		if err := db.Where("tier = ?", tier).Assign(cfg).FirstOrCreate(&cfg).Error; err != nil {
			t.Fatal(err)
		}
	}

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, nil, filepath.Join(tmpDir, "sandbox_dir"), 1024*1024)
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, nil, agg, filepath.Join(tmpDir, "sandbox_dir"))
	publishSvc := services.NewPublishService(docStore, dtStore, masterStore, govStore, publishSink)
	llmClient := llm.NewClientWithOptions(&http.Client{}, 10*time.Millisecond)
	chatSvc := services.NewChatService(chatStore, docStore, dtStore, docTypeSvc, govStore, revSvc, snapSvc, previewSvc, cipher, llmClient)

	commonAudit.Init(db)
	t.Cleanup(func() {
		_ = commonAudit.Close(context.Background())
	})

	router := gin.New()
	router.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, nil).
		WithDocumentServices(revSvc, previewSvc, publishSvc).
		WithChatService(chatSvc)
	appHandlers.RegisterRoutes(router, db)

	dept := commonModels.Department{Name: "架构研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "chat_owner", "负责人", []string{"user"}, &dept.ID)
	normal := createTestUser(t, db, "chat_reader", "普通用户", []string{"user"}, &dept.ID)
	repo := models.Repository{
		Name:         "order-core",
		URL:          "https://git.internal/order/core.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      owner.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	_, dtRev, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}

	slot1MD := "第一章节原始正文内容：系统定位与模块分层边界，保持字节级不可变。"
	slot2MD := "第二章节原始正文内容：运行时并发、锁与共享内存模型初稿。"
	slot3MD := "第三章节原始正文内容：端到端核心业务链路说明，保持字节级不可变。"
	slot1Hash := testSHA256Hex(slot1MD)
	slot2Hash := testSHA256Hex(slot2MD)
	slot3Hash := testSHA256Hex(slot3MD)

	snaps := []models.DocumentSectionSnapshot{
		{
			SlotID:        "overview_boundaries",
			SectionOrder:  1,
			Title:         "1. 系统定位与模块分层边界",
			Ownership:     models.OwnershipAuto,
			Markdown:      slot1MD,
			ContentSHA256: slot1Hash,
		},
		{
			SlotID:        "runtime_concurrency",
			SectionOrder:  2,
			Title:         "2. 运行时并发、进程/线程、锁与共享内存模型",
			Ownership:     models.OwnershipAuto,
			Markdown:      slot2MD,
			ContentSHA256: slot2Hash,
		},
		{
			SlotID:        "core_request_flows",
			SectionOrder:  3,
			Title:         "3. 端到端核心业务链路与时序图",
			Ownership:     models.OwnershipHybrid,
			Markdown:      slot3MD,
			ContentSHA256: slot3Hash,
		},
	}
	snapsJSON, _ := json.Marshal(snaps)

	doc := &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "order-core 系统架构与核心链路",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	rev := &models.DocumentRevision{
		DocumentID:         doc.ID,
		RevisionNo:         1,
		SnapshotID:         0,
		CommitSHA:          "1111111111111111111111111111111111111111",
		DocTypeRevisionID:  dtRev.ID,
		Source:             models.RevSourceAIGenerate,
		FullMarkdown:       slot1MD + "\n\n" + slot2MD + "\n\n" + slot3MD,
		SectionsJSON:       datatypes.JSON(snapsJSON),
		VerifierReportJSON: datatypes.JSON(`{"passed":true,"can_approve":true}`),
		VerifierPassed:     true,
		CanApprove:         true,
		SlotCoverageRate:   1.0,
		CreatedBy:          owner.ID,
	}
	if err := db.Create(rev).Error; err != nil {
		t.Fatal(err)
	}
	for _, sn := range snaps {
		secEv := &models.SectionEvidence{
			DocumentID:          doc.ID,
			RevisionID:          rev.ID,
			SlotID:              sn.SlotID,
			SectionOrder:        sn.SectionOrder,
			Title:               sn.Title,
			Ownership:           sn.Ownership,
			ContentSHA256:       sn.ContentSHA256,
			EvidencesJSON:       datatypes.JSON(`[]`),
			VerifierSummaryJSON: datatypes.JSON(`{}`),
		}
		if err := db.Create(secEv).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(doc).Update("current_revision_id", rev.ID).Error; err != nil {
		t.Fatal(err)
	}

	return &chatTestEnv{
		db:         db,
		router:     router,
		owner:      owner,
		ownerTok:   issueTestToken(t, owner),
		normalUser: normal,
		normalTok:  issueTestToken(t, normal),
		doc:        doc,
		slot1Hash:  slot1Hash,
		slot3Hash:  slot3Hash,
	}
}

type parsedSSEEvent struct {
	Event string
	Data  map[string]any
}

func parseSSEStream(raw string) []parsedSSEEvent {
	var events []parsedSSEEvent
	scanner := bufio.NewScanner(strings.NewReader(raw))
	curEvent := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var obj map[string]any
			_ = json.Unmarshal([]byte(dataStr), &obj)
			events = append(events, parsedSSEEvent{Event: curEvent, Data: obj})
		}
	}
	return events
}

func TestSSEChatRefineStreamAndAcceptPreservesOtherSlots(t *testing.T) {
	// 1. 启动本地 httptest.NewServer 模拟 OpenAI 流式 SSE 响应
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		refinedJSON := `{"reply":"已为运行时并发章节补充锁粒度与读写锁说明。","markdown":"### 运行时并发与锁模型（精修版）\n\n采用分片互斥锁保护核心状态表，避免全局大锁争抢，并补充超时退避参数说明。","cited_anchors":[]}`
		chunks := []string{refinedJSON[:50], refinedJSON[50:120], refinedJSON[120:]}
		for _, ch := range chunks {
			payload, _ := json.Marshal(map[string]any{
				"model": "mock-qwen-72b",
				"choices": []map[string]any{
					{"delta": map[string]any{"content": ch}},
				},
			})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(payload))
			if flusher != nil {
				flusher.Flush()
			}
		}
		usagePayload, _ := json.Marshal(map[string]any{
			"model":   "mock-qwen-72b",
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     1250,
				"completion_tokens": 240,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 800,
				},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", string(usagePayload))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockLLM.Close()

	env := setupChatTestEnv(t, mockLLM.URL)
	docURL := fmt.Sprintf("/api/documents/%d", env.doc.ID)

	// 2. 普通无权用户调用返回 403
	w403 := doAdminDocTypeReq(env.router, http.MethodPost, docURL+"/chat/refine", env.normalTok, map[string]any{
		"target_slot_id":   "runtime_concurrency",
		"base_revision_no": 1,
		"prompt":           "补充锁粒度说明",
	})
	if w403.Code != http.StatusForbidden {
		t.Fatalf("普通用户调用 chat/refine 应返回 403，实际 %d", w403.Code)
	}

	// 3. 负责人对第 2 槽位发起 SSE 流式精修
	wSSE := doAdminDocTypeReq(env.router, http.MethodPost, docURL+"/chat/refine", env.ownerTok, map[string]any{
		"target_slot_id":   "runtime_concurrency",
		"base_revision_no": 1,
		"prompt":           "请补充锁粒度与避免全局大锁争抢的说明",
	})
	if wSSE.Code != http.StatusOK {
		t.Fatalf("SSE chat/refine 应返回 200，实际 %d: %s", wSSE.Code, wSSE.Body.String())
	}
	if !strings.Contains(wSSE.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("响应 Content-Type 应为 text/event-stream，实际 %q", wSSE.Header().Get("Content-Type"))
	}

	events := parseSSEStream(wSSE.Body.String())
	var eventNames []string
	var msgID uint
	var sessionID uint
	for _, ev := range events {
		eventNames = append(eventNames, ev.Event)
		if ev.Event == "meta" {
			msgID = uint(ev.Data["message_id"].(float64))
			sessionID = uint(ev.Data["session_id"].(float64))
		}
	}
	// 断言严格按序收到 meta -> delta+ -> diff_proposal -> verifier_preview -> done
	if len(eventNames) < 5 ||
		eventNames[0] != "meta" ||
		eventNames[1] != "delta" ||
		eventNames[len(eventNames)-3] != "diff_proposal" ||
		eventNames[len(eventNames)-2] != "verifier_preview" ||
		eventNames[len(eventNames)-1] != "done" {
		t.Fatalf("SSE 事件顺序不符契约: got %v (raw=%s)", eventNames, wSSE.Body.String())
	}
	if msgID == 0 || sessionID == 0 {
		t.Fatalf("meta 缺少有效 session_id/message_id: session=%d msg=%d", sessionID, msgID)
	}

	// 验证 archdoc_llm_usage_logs 记录了 stage="chat_refine" Token 成本
	var usageLog models.LLMUsageLog
	if err := env.db.Where("document_id = ? AND stage = ?", env.doc.ID, "chat_refine").First(&usageLog).Error; err != nil {
		t.Fatalf("未记录 archdoc_llm_usage_logs(stage=chat_refine): %v", err)
	}
	if usageLog.PromptTokens != 1250 || usageLog.CompletionTokens != 240 || usageLog.CachedPromptTokens != 800 {
		t.Fatalf("Token 成本记录不准确: %+v", usageLog)
	}

	// 4. 调用 Accept 采纳提案
	wAccept := doAdminDocTypeReq(env.router, http.MethodPost,
		fmt.Sprintf("%s/chat/messages/%d/accept", docURL, msgID), env.ownerTok,
		map[string]any{"base_revision_no": 1})
	if wAccept.Code != http.StatusOK {
		t.Fatalf("Accept 失败: %d %s", wAccept.Code, wAccept.Body.String())
	}
	var afterAccept services.DocumentDetailVO
	if err := json.Unmarshal(wAccept.Body.Bytes(), &afterAccept); err != nil {
		t.Fatalf("解析 Accept 响应失败: %v", err)
	}
	if afterAccept.CurrentRevisionNo != 2 || afterAccept.ViewingRevision.Source != models.RevSourceChatRefine {
		t.Fatalf("Accept 后修订号或 source 异常: rev=%d source=%q", afterAccept.CurrentRevisionNo, afterAccept.ViewingRevision.Source)
	}

	// 核心不变量断言：仅第 2 槽位更新，其余第 1、3 槽位 content_sha256 严格字节级不变！
	secBySlot := make(map[string]services.SectionVO, len(afterAccept.Sections))
	for _, s := range afterAccept.Sections {
		secBySlot[s.SlotID] = s
	}
	if secBySlot["overview_boundaries"].ContentSHA256 != env.slot1Hash {
		t.Fatalf("第 1 槽位内容被意外改动: got %s, want %s", secBySlot["overview_boundaries"].ContentSHA256, env.slot1Hash)
	}
	if secBySlot["core_request_flows"].ContentSHA256 != env.slot3Hash {
		t.Fatalf("第 3 槽位内容被意外改动: got %s, want %s", secBySlot["core_request_flows"].ContentSHA256, env.slot3Hash)
	}
	if !strings.Contains(secBySlot["runtime_concurrency"].Markdown, "分片互斥锁") {
		t.Fatalf("第 2 槽位未应用精修正文: %q", secBySlot["runtime_concurrency"].Markdown)
	}

	// 断言 archdoc_quality_metrics 写入 chat_accepted=true
	var acceptMetric models.QualityMetric
	if err := env.db.Where("document_id = ? AND metric_event = ?", env.doc.ID, models.MetricEventChatProposalResolved).First(&acceptMetric).Error; err != nil {
		t.Fatalf("未写入采纳质量指标: %v", err)
	}
	if acceptMetric.ChatAccepted == nil || !*acceptMetric.ChatAccepted {
		t.Fatalf("期望 chat_accepted=true，实际 %+v", acceptMetric.ChatAccepted)
	}

	// 5. 锁定第 1 槽位后对其调用 chat/refine 返回 409 CONFLICT
	wLock := doAdminDocTypeReq(env.router, http.MethodPatch,
		docURL+"/sections/overview_boundaries/ownership", env.ownerTok,
		map[string]any{"ownership": "locked"})
	if wLock.Code != http.StatusOK {
		t.Fatalf("锁定第 1 槽位失败: %d %s", wLock.Code, wLock.Body.String())
	}
	wLockedRefine := doAdminDocTypeReq(env.router, http.MethodPost, docURL+"/chat/refine", env.ownerTok, map[string]any{
		"target_slot_id":   "overview_boundaries",
		"base_revision_no": 2,
		"prompt":           "尝试修改已锁定章节",
	})
	if wLockedRefine.Code != http.StatusConflict {
		t.Fatalf("对已锁定槽位调用 chat/refine 应返回 409，实际 %d: %s", wLockedRefine.Code, wLockedRefine.Body.String())
	}

	// 6. 对第 2 槽位发起另一提案后调用 Reject -> 修订号保持 v2 不变且写入 chat_accepted=false
	wSSE2 := doAdminDocTypeReq(env.router, http.MethodPost, docURL+"/chat/refine", env.ownerTok, map[string]any{
		"session_id":       sessionID,
		"target_slot_id":   "runtime_concurrency",
		"base_revision_no": 2,
		"prompt":           "再精修一次测试拒绝",
	})
	if wSSE2.Code != http.StatusOK {
		t.Fatalf("第二次 SSE 精修失败: %d %s", wSSE2.Code, wSSE2.Body.String())
	}
	events2 := parseSSEStream(wSSE2.Body.String())
	var msgID2 uint
	for _, ev := range events2 {
		if ev.Event == "meta" {
			msgID2 = uint(ev.Data["message_id"].(float64))
		}
	}

	wReject := doAdminDocTypeReq(env.router, http.MethodPost,
		fmt.Sprintf("%s/chat/messages/%d/reject", docURL, msgID2), env.ownerTok, nil)
	if wReject.Code != http.StatusOK {
		t.Fatalf("Reject 失败: %d %s", wReject.Code, wReject.Body.String())
	}

	var refreshedDoc models.Document
	if err := env.db.First(&refreshedDoc, env.doc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshedDoc.CurrentRevisionNo != 2 {
		t.Fatalf("Reject 后文档修订号不应变化，期望 2 实际 %d", refreshedDoc.CurrentRevisionNo)
	}

	var rejectCount int64
	env.db.Model(&models.QualityMetric{}).
		Where("document_id = ? AND metric_event = ? AND chat_accepted = ?", env.doc.ID, models.MetricEventChatProposalResolved, false).
		Count(&rejectCount)
	if rejectCount != 1 {
		t.Fatalf("期望写入 1 条 chat_accepted=false 指标，实际 %d", rejectCount)
	}

	// 7. GET /api/documents/:id/chat/sessions 返回完整历史
	wSess := doAdminDocTypeReq(env.router, http.MethodGet, docURL+"/chat/sessions", env.ownerTok, nil)
	if wSess.Code != http.StatusOK {
		t.Fatalf("查询会话历史失败: %d %s", wSess.Code, wSess.Body.String())
	}
}

func TestSSEChatRefineClientAbortCancelsUpstream(t *testing.T) {
	upstreamStarted := make(chan struct{}, 1)
	var upstreamCanceled atomic.Bool
	upstreamDone := make(chan struct{})

	// 模拟上游慢流 LLM，持续等待直到上游请求 ctx 被取消
	slowLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"model\":\"mock-slow\",\"choices\":[{\"delta\":{\"content\":\"开始生成...\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case upstreamStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			upstreamCanceled.Store(true)
		case <-time.After(10 * time.Second):
		}
	}))
	defer slowLLM.Close()

	env := setupChatTestEnv(t, slowLLM.URL)
	apiServer := httptest.NewServer(env.router)
	defer apiServer.Close()

	clientCtx, cancelClient := context.WithCancel(context.Background())
	defer cancelClient()

	bodyBytes, _ := json.Marshal(map[string]any{
		"target_slot_id":   "runtime_concurrency",
		"base_revision_no": 1,
		"prompt":           "模拟客户端中途取消的慢流请求",
	})
	req, err := http.NewRequestWithContext(
		clientCtx,
		http.MethodPost,
		fmt.Sprintf("%s/api/documents/%d/chat/refine", apiServer.URL, env.doc.ID),
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req.Header.Set("Accept", "text/event-stream")

	doneReq := make(chan struct{})
	go func() {
		defer close(doneReq)
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	// 等待上游 LLM 收到请求并进入流式输出后，客户端主动 Abort
	select {
	case <-upstreamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("等待上游慢流启动超时")
	}
	cancelClient()

	select {
	case <-upstreamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("客户端取消后上游 LLM 连接未及时中止")
	}
	<-doneReq

	if !upstreamCanceled.Load() {
		t.Fatal("期望上游 LLM 请求因客户端 Abort 而被立即取消，但未触发 ctx.Done()")
	}

	// 短暂等待后台清理落库完成，断言 DB 中不存在任何无效 pending 提案
	time.Sleep(150 * time.Millisecond)
	var pendingCount int64
	env.db.Model(&models.ChatMessage{}).Where("proposal_status = ?", models.ProposalStatusPending).Count(&pendingCount)
	if pendingCount != 0 {
		t.Fatalf("客户端中止后不应落库任何无效 pending 提案，实际 pending=%d", pendingCount)
	}
}
