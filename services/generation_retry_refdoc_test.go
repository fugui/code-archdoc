package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/services"
	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"gorm.io/datatypes"
)

type retryStaticGitSyncer struct {
	dir string
	sha string
}

func (g retryStaticGitSyncer) Sync(_ context.Context, opts gitsync.SyncOptions) (*gitsync.SyncResult, error) {
	return &gitsync.SyncResult{
		LocalPath:      g.dir,
		ResolvedBranch: opts.Branch,
		CommitSHA:      g.sha,
	}, nil
}

func TestSlotRetryVersionLockAndUnboundManualChatPreserve_T_ADR10_12(t *testing.T) {
	ctx := context.Background()
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	var mu sync.Mutex
	var capturedPrompts []string
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var joined strings.Builder
		for _, m := range req.Messages {
			joined.WriteString(m.Content)
			joined.WriteByte('\n')
		}
		bodyStr := joined.String()
		mu.Lock()
		capturedPrompts = append(capturedPrompts, bodyStr)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(bodyStr, "domain_terms") && !strings.Contains(bodyStr, "cited_anchors") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"domain_terms":[],"module_summaries":[]}`}}},
				"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 10},
			})
			return
		}
		if strings.Contains(r.URL.Path, "chat") && strings.Contains(bodyStr, llm.MarkerReply) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": llm.MarkerReply + "\n说明\n" + llm.MarkerPayloadJSON + `{"reply":"ok","markdown":"Chat 精修后内容","cited_anchors":[]}`}}},
				"usage":   map[string]any{"prompt_tokens": 30, "completion_tokens": 20},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"markdown":"重试补齐后的架构内容。","cited_anchors":[{"scope":"ref_doc","file_path":"ref_doc:1","fq_symbol":"sec-1","symbol_kind":"ref_doc"}]}`}}},
			"usage":   map[string]any{"prompt_tokens": 60, "completion_tokens": 25},
		})
	}))
	defer mockLLM.Close()

	dept := models.Department{Name: "重试锁定验证部"}
	_ = db.Create(&dept).Error
	owner := commonModels.User{
		Username:     "retry_owner",
		Name:         "负责人",
		Email:        "retry@example.com",
		DepartmentID: &dept.ID,
		Roles:        datatypes.JSON(`["user"]`),
		IsActive:     true,
	}
	_ = db.Create(&owner).Error

	wd, _ := os.Getwd()
	fixtureDir := filepath.Clean(filepath.Join(wd, "..", "testdata", "golden", "fixtures", "cpp_order_service"))
	repo := commonModels.Repository{
		Name:         "retry-order-svc",
		URL:          "git@git.internal:core/retry-order-svc.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      owner.ID,
		IsActive:     true,
	}
	_ = db.Create(&repo).Error

	tmpDir := t.TempDir()
	reg, _ := doctype.NewRegistry(filepath.Join(tmpDir, "doc_types"))
	facetStore, _ := codeir.NewLocalFSFacetStore(filepath.Join(tmpDir, "ir_cache"), 0)
	agg := codeir.NewAggregator(treesitter.NewCppExtractor(), treesitter.NewCExtractor())

	masterStore := models.NewSharedMasterStore(db)
	dtStore := models.NewDocTypeStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)
	moduleStore := models.NewModuleStore(db)
	chatStore := models.NewChatStore(db)
	reviewStore := models.NewReviewCommentStore(db)
	refDocStore := models.NewReferenceDocStore(db)

	_ = db.Create(&models.LLMConfig{
		Name:             "Retry-Model",
		BaseURL:          mockLLM.URL,
		ModelName:        "mock-retry",
		APIKey:           "sk-mock-retry-0001",
		APIKeyMasked:     "sk-****0001",
		TimeoutSeconds:   15,
		MaxTokensPerTask: 200000,
		MaxFilesPerTask:  2000,
		IsActive:         true,
		IsDefault:        true,
	}).Error

	rbacSvc := services.NewRBACService(masterStore)
	refDocSvc, err := services.NewRefDocService(refDocStore, masterStore, rbacSvc, filepath.Join(tmpDir, "ref_docs"), 524288)
	if err != nil {
		t.Fatal(err)
	}
	dtSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := dtSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, retryStaticGitSyncer{dir: fixtureDir, sha: "c1c2c3d4e5f6a7b8c9d0a1b2c3d4e5f6a7b8c9d0"}, filepath.Join(tmpDir, "sandbox"), 1048576)
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 10 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, dtStore, dtSvc, snapSvc, taskStore, docStore, govStore, llmClient, 2,
	).WithModuleGovernance(moduleStore, nil, rbacSvc).
		WithRefDocService(refDocSvc)
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, snapSvc).
		WithReviewHooks(chatStore, reviewStore).
		WithRefDocService(refDocSvc)
	chatSvc := services.NewChatService(chatStore, docStore, dtStore, dtSvc, govStore, revSvc, snapSvc, nil, llmClient).
		WithReviewCommentRepo(reviewStore).
		WithModuleContext(moduleStore, masterStore).
		WithRefDocService(refDocSvc)
	t.Cleanup(func() { genSvc.WaitAll() })

	// 1. 创建参考文档 v1 并绑定到 repo
	refDetail, _, err := refDocSvc.CreateRefDoc(ctx, services.CreateRefDocRequest{
		Title:     "订单演进规范",
		DocKind:   models.RefDocKindArchDesign,
		ContentMD: "# 演进背景 {#sec-1}\n\nREFDOC_V1_MARKER 严禁越级调用。",
		InitialBinding: services.RefDocBindingInput{
			ScopeLevel: models.RefDocScopeRepo,
			RepoID:     &repo.ID,
		},
	}, &owner)
	if err != nil {
		t.Fatalf("CreateRefDoc 失败: %v", err)
	}

	// 2. 准备快照与带失败槽位的已入库任务（ReferenceDocSnapshotJSON 冻结 v1）
	snapReady, err := snapSvc.EnsureSnapshot(ctx, &repo, "", "master")
	if err != nil {
		t.Fatalf("EnsureSnapshot 失败: %v", err)
	}
	_, dtRev, _ := dtStore.GetBySlug(ctx, "system-architecture")
	eff, err := refDocSvc.ResolveEffectiveRefDocs(ctx, services.ResolveEffectiveRefDocsRequest{
		RepoID:      &repo.ID,
		DocTypeSlug: "system-architecture",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapBytes, _ := json.Marshal(eff.Snapshots)
	task := &models.GenTask{
		RepoID:                   repo.ID,
		Subdirectory:             "",
		RefName:                  "master",
		SnapshotID:               &snapReady.Snapshot.ID,
		TaskKind:                 models.TaskKindGenerate,
		Engine:                   models.EngineNative,
		Priority:                 models.TaskPriorityInteractive,
		DocTypesJSON:             datatypes.JSON(`["system-architecture"]`),
		ReferenceDocSnapshotJSON: datatypes.JSON(snapBytes),
		Status:                   models.TaskStatusFailed,
		ErrorCode:                "PARTIAL_FAILED",
		TriggeredBy:              owner.ID,
	}
	if err := taskStore.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	slotRow := models.GenSlotTask{
		TaskID:            task.ID,
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		SlotID:            "overview_boundaries",
		Status:            models.SlotTaskFailed,
		ErrorMessage:      "timeout",
	}
	if err := taskStore.CreateSlotTasks(ctx, []models.GenSlotTask{slotRow}); err != nil {
		t.Fatal(err)
	}
	_, slots, _ := taskStore.GetTaskWithSlots(ctx, task.ID)
	failedSlotID := slots[0].ID

	// 3. 在重试前将参考文档升级为 v2，写入磁盘 v2.md
	_, _, _, err = refDocSvc.UpdateRefDoc(ctx, refDetail.ID, services.UpdateRefDocRequest{
		ExpectedVersion: 1,
		ContentMD:       ptrStr("# 演进背景 {#sec-1}\n\nREFDOC_V2_MARKER 最新规范内容。"),
	}, &owner)
	if err != nil {
		t.Fatalf("UpdateRefDoc v2 失败: %v", err)
	}

	// 4. 调用 RetrySlotTask -> 严格锁死 v1，不读 v2
	if _, err := genSvc.RetrySlotTask(ctx, task.ID, failedSlotID, owner.ID); err != nil {
		t.Fatalf("RetrySlotTask 失败: %v", err)
	}
	genSvc.WaitAll()

	mu.Lock()
	allPrompts := strings.Join(capturedPrompts, "\n---\n")
	mu.Unlock()
	if !strings.Contains(allPrompts, "REFDOC_V1_MARKER") {
		t.Fatalf("单槽位重试必须读取任务冻结的 v1.md，实际 Prompt 未见 V1 标记:\n%s", allPrompts)
	}
	if strings.Contains(allPrompts, "REFDOC_V2_MARKER") {
		t.Fatalf("单槽位重试严禁越界读取 v2.md:\n%s", allPrompts)
	}
	reloadedTask, _, _ := taskStore.GetTaskWithSlots(ctx, task.ID)
	if strings.Contains(string(reloadedTask.ReferenceDocSnapshotJSON), `"version_no":2`) {
		t.Fatalf("RetrySlotTask 不得篡改任务 ReferenceDocSnapshotJSON: %s", string(reloadedTask.ReferenceDocSnapshotJSON))
	}

	// 验证目标文档已落库并在 system_context 槽位写入了 v1 RefCitationsJSON
	genDoc, err := docStore.GetByScope(ctx, repo.ID, "", "master", "system-architecture")
	if err != nil {
		t.Fatalf("重试补齐后未创建文档: %v", err)
	}
	// 先确保另一个槽位也存在于修订中并有 RefCitationsJSON，方便后续解绑测试
	// 给 system_context 保留既有 RefCitationsJSON，验证手工编辑另一个槽位（或新增 module_responsibilities）及 Chat 后仍保留未改章节引用快照
	_, rev1Evs, _ := docStore.GetRevisionWithSections(ctx, genDoc.ID, genDoc.CurrentRevisionNo)
	foundSysRef := false
	for _, ev := range rev1Evs {
		if ev.SlotID != "overview_boundaries" {
			continue
		}
		var cits []models.RefDocCitation
		if json.Unmarshal(ev.RefCitationsJSON, &cits) == nil {
			for _, c := range cits {
				if c.DocID == refDetail.ID && c.VersionNo == 1 {
					foundSysRef = true
				}
			}
		}
	}
	if !foundSysRef {
		t.Fatalf("期望重试生成的 overview_boundaries 写入 version_no=1 的 RefCitationsJSON，实际 %+v", rev1Evs)
	}

	// 5. 归档并解绑参考文档，验证既有文档另一槽位的手工直编仍成功且保留未改槽位的引用快照
	expectedV2 := 2
	_, _, _, err = refDocSvc.UpdateRefDoc(ctx, refDetail.ID, services.UpdateRefDocRequest{
		ExpectedVersion: expectedV2,
		Status:          ptrStr(models.RefDocStatusArchived),
	}, &owner)
	if err != nil {
		t.Fatalf("归档参考文档失败: %v", err)
	}
	detailAfterArchive, err := refDocSvc.GetRefDocDetail(ctx, refDetail.ID, 0, &owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range detailAfterArchive.Bindings {
		if _, err := refDocSvc.DeleteBinding(ctx, refDetail.ID, b.ID, &owner); err != nil {
			t.Fatalf("删除绑定 %d 失败: %v", b.ID, err)
		}
	}

	curDocDetail, err := revSvc.GetDocumentDetail(ctx, genDoc.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	otherSlotID := ""
	for _, sec := range curDocDetail.Sections {
		if sec.SlotID != "overview_boundaries" {
			otherSlotID = sec.SlotID
			break
		}
	}
	if otherSlotID == "" {
		t.Fatal("文档缺少第二个槽位")
	}

	afterManual, err := revSvc.ApplySectionChange(ctx, services.ApplySectionChangeInput{
		DocID:          genDoc.ID,
		SlotID:         otherSlotID,
		BaseRevisionNo: curDocDetail.CurrentRevisionNo,
		Markdown:       "人工直编更新后的另一章节。",
		Source:         models.RevSourceManualEdit,
		OperatorID:     owner.ID,
	})
	if err != nil {
		t.Fatalf("参考文档解绑后手工直编应正常保存，实际失败: %v", err)
	}
	preservedSysCite := false
	for _, sec := range afterManual.Sections {
		if sec.SlotID == "overview_boundaries" && len(sec.RefCitations) > 0 && sec.RefCitations[0].DocID == refDetail.ID {
			preservedSysCite = true
		}
	}
	if !preservedSysCite {
		t.Fatalf("解绑后手工编辑其他槽位应保留未改章节 overview_boundaries 的 RefCitations: %+v", afterManual.Sections)
	}

	// 6. 解绑后通过 Chat 局部精修另一槽位，采纳后同样不报错且保留 overview_boundaries 引用快照
	prep, err := chatSvc.PrepareRefine(ctx, genDoc.ID, services.ChatRefineRequest{
		Mode:           models.ChatModeRevise,
		ScopeType:      models.ChatScopeSlot,
		TargetSlotID:   otherSlotID,
		BaseRevisionNo: afterManual.CurrentRevisionNo,
		Prompt:         "精修该章节",
	}, owner.ID)
	if err != nil {
		t.Fatalf("解绑后 PrepareRefine 失败: %v", err)
	}
	// 用模拟双段响应直调单槽精修（复用了顶部 mockLLM，但 URL 里未含 chat，重写测试专用 server 更稳妥）
	_ = prep
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + llm.MarkerReply + `\n精修说明\n` + llm.MarkerPayloadJSON + `{\"reply\":\"ok\",\"markdown\":\"Chat 精修保存成功\",\"cited_anchors\":[]}"}}]}`))
	}))
	defer chatSrv.Close()
	_ = db.Model(&models.LLMConfig{}).Where("is_default = ?", true).Update("base_url", chatSrv.URL).Error

	prep2, err := chatSvc.PrepareRefine(ctx, genDoc.ID, services.ChatRefineRequest{
		Mode:           models.ChatModeRevise,
		ScopeType:      models.ChatScopeSlot,
		TargetSlotID:   otherSlotID,
		BaseRevisionNo: afterManual.CurrentRevisionNo,
		Prompt:         "精修该章节2",
	}, owner.ID)
	if err != nil {
		t.Fatalf("PrepareRefine 失败: %v", err)
	}
	evs := collectRetrySSE(ctx, chatSvc, prep2)
	var gotProposal bool
	for _, ev := range evs {
		if ev.name == "proposal_item" {
			gotProposal = true
		}
	}
	if !gotProposal {
		t.Fatalf("期望生成 Chat 精修提案，实际事件: %+v", evs)
	}
	acceptedDetail, _, err := chatSvc.AcceptProposal(ctx, genDoc.ID, prep2.AssistantMsg.ID, afterManual.CurrentRevisionNo, owner.ID)
	if err != nil {
		t.Fatalf("参考文档解绑后采纳 Chat 精修失败: %v", err)
	}
	preservedAfterChat := false
	for _, sec := range acceptedDetail.Sections {
		if sec.SlotID == "overview_boundaries" && len(sec.RefCitations) > 0 && sec.RefCitations[0].DocID == refDetail.ID {
			preservedAfterChat = true
		}
	}
	if !preservedAfterChat {
		t.Fatalf("解绑后采纳 Chat 精修应保留未改槽位 overview_boundaries 的 RefCitations: %+v", acceptedDetail.Sections)
	}
}

func ptrStr(s string) *string { return &s }

type retryRecordedSSE struct {
	name string
}

func collectRetrySSE(ctx context.Context, svc *services.ChatService, prep *services.PreparedChatRefine) []retryRecordedSSE {
	var events []retryRecordedSSE
	svc.ExecuteRefineStream(ctx, prep, func(ev string, _ any) error {
		events = append(events, retryRecordedSSE{name: ev})
		return nil
	})
	return events
}
