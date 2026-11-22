package services_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ArchdocTest",
		"GIT_AUTHOR_EMAIL=test@archdoc.local",
		"GIT_COMMITTER_NAME=ArchdocTest",
		"GIT_COMMITTER_EMAIL=test@archdoc.local",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) 失败: %v\n输出: %s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func createBareGitFixture(t *testing.T, name string, initialFiles map[string]string) (bareURL string, workDir string, firstSHA string) {
	t.Helper()
	root := t.TempDir()
	bareDir := filepath.Join(root, name+".git")
	workDir = filepath.Join(root, name+"-work")

	runGitCmd(t, "", "init", "--bare", bareDir)
	runGitCmd(t, "", "init", workDir)
	runGitCmd(t, workDir, "checkout", "-b", "master")

	for rel, content := range initialFiles {
		full := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", "initial commit")
	runGitCmd(t, workDir, "remote", "add", "origin", bareDir)
	runGitCmd(t, workDir, "push", "-u", "origin", "master")

	firstSHA = runGitCmd(t, workDir, "rev-parse", "HEAD")
	return bareDir, workDir, firstSHA
}

func pushGitUpdate(t *testing.T, workDir string, files map[string]string, msg string) string {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitCmd(t, workDir, "add", ".")
	runGitCmd(t, workDir, "commit", "-m", msg)
	runGitCmd(t, workDir, "push", "origin", "master")
	return runGitCmd(t, workDir, "rev-parse", "HEAD")
}

// TestRemoteHeadPollerBackoffDoesNotBlockHealthyRepos 验证 F-25 与 F-12 联动：
// - 1 个无效远端仓：consecutive_failures == 1 且设置 15m 指数退避 next_retry_at；
// - 2 个健康本地 bare 夹具仓：正常更新 remote_head_sha 并产出精准章节级 Code Drift；
// - 1 个不含已发布文档的仓库：不被巡检（archdoc_repo_remote_heads 零记录）。
func TestRemoteHeadPollerBackoffDoesNotBlockHealthyRepos(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}
	ctx := context.Background()

	dept := commonModels.Department{Name: "基础架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := commonModels.User{Username: "poller_owner", Password: "x", Name: "Owner", DepartmentID: &dept.ID}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}

	v1Cpp := `namespace order {
int Create(int uid) {
    return uid + 1;
}
int Pay(int oid, int amt) {
    return oid + amt;
}
}
`
	// 仅修改 Pay，同时在顶部插注释让 Create 行号下移 -> Create 应零误报，仅 Pay 漂移
	v2Cpp := `// shifted line 1
// shifted line 2
// shifted line 3
namespace order {
int Create(int uid) {
    return uid + 1;
}
int Pay(int oid, int amt) {
    if (amt <= 0) return -1;
    return oid + amt * 2;
}
}
`
	bare1, work1, sha1V1 := createBareGitFixture(t, "repo-healthy-1", map[string]string{"src/order.cpp": v1Cpp})
	bare2, work2, sha2V1 := createBareGitFixture(t, "repo-healthy-2", map[string]string{"src/order.cpp": v1Cpp})
	bareDraftOnly, _, _ := createBareGitFixture(t, "repo-draft-only", map[string]string{"src/order.cpp": v1Cpp})

	// 提前同步健康仓至 sandboxDir，模拟发布后本地沙箱存在
	sandboxDir := filepath.Join(t.TempDir(), "sandbox_dir")
	repoBad := commonModels.Repository{Name: "bad-repo", URL: "/nonexistent/invalid-bare-repo.git", Branch: "master", DepartmentID: dept.ID, OwnerID: owner.ID, IsActive: true}
	repoH1 := commonModels.Repository{Name: "healthy-repo-1", URL: bare1, Branch: "master", DepartmentID: dept.ID, OwnerID: owner.ID, IsActive: true}
	repoH2 := commonModels.Repository{Name: "healthy-repo-2", URL: bare2, Branch: "master", DepartmentID: dept.ID, OwnerID: owner.ID, IsActive: true}
	repoDraft := commonModels.Repository{Name: "draft-only-repo", URL: bareDraftOnly, Branch: "master", DepartmentID: dept.ID, OwnerID: owner.ID, IsActive: true}
	for _, r := range []*commonModels.Repository{&repoBad, &repoH1, &repoH2, &repoDraft} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 将 healthy-repo-1 与 healthy-repo-2 同步到沙箱的 v1 版本，并随后推送 v2 提交至 bare 仓
	for _, pair := range []struct {
		repoID uint
		url    string
	}{
		{repoH1.ID, bare1},
		{repoH2.ID, bare2},
	} {
		if _, err := gitsync.Sync(ctx, gitsync.SyncOptions{
			RepoURL:   pair.url,
			LocalPath: filepath.Join(sandboxDir, "repo_"+itoaUint(pair.repoID)),
			Branch:    "master",
			Depth:     0,
		}); err != nil {
			t.Fatalf("预同步健康仓失败: %v", err)
		}
	}
	sha1V2 := pushGitUpdate(t, work1, map[string]string{"src/order.cpp": v2Cpp}, "modify Pay")
	sha2V2 := pushGitUpdate(t, work2, map[string]string{"src/order.cpp": v2Cpp}, "modify Pay repo2")

	// 计算 v1 符号指纹
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	v1Syms := agg.ExtractSingleFileSymbols(ctx, "src/order.cpp", "cpp", []byte(v1Cpp))
	fpBySym := make(map[string]string)
	for _, s := range v1Syms {
		fpBySym[s.FQSymbol] = s.Meta.ContentFingerprint
	}
	if fpBySym["order::Create"] == "" || fpBySym["order::Pay"] == "" {
		t.Fatalf("未能提取 v1 符号指纹: %+v", fpBySym)
	}

	// 准备 DocType 注册表
	docTypeReg, err := doctype.NewRegistry(filepath.Join(t.TempDir(), "doc_types"))
	if err != nil {
		t.Fatal(err)
	}
	docTypeStore := models.NewDocTypeStore(db)
	docTypeSvc := services.NewDocTypeService(docTypeReg, docTypeStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	_, dtRev, err := docTypeStore.GetBySlug(ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}

	createPublishedDoc := func(repoID uint, commitSHA string, status string) *models.Document {
		doc := &models.Document{
			RepoID:             repoID,
			Subdirectory:       "",
			RefName:            "master",
			DocTypeSlug:        "system-architecture",
			DocTypeRevisionID:  dtRev.ID,
			Title:              "Arch Doc",
			Status:             status,
			CurrentRevisionNo:  1,
			PublishedCommitSHA: commitSHA,
		}
		if err := db.Create(doc).Error; err != nil {
			t.Fatal(err)
		}
		secs := []models.DocumentSectionSnapshot{
			{SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 概览", Ownership: models.OwnershipAuto, Markdown: "Create flow", ContentSHA256: shaHex("Create flow")},
			{SlotID: "core_request_flows", SectionOrder: 2, Title: "3. 链路", Ownership: models.OwnershipHybrid, Markdown: "Pay flow", ContentSHA256: shaHex("Pay flow")},
		}
		secsJSON, _ := json.Marshal(secs)
		rev := &models.DocumentRevision{
			DocumentID:        doc.ID,
			RevisionNo:        1,
			SnapshotID:        1,
			CommitSHA:         commitSHA,
			DocTypeRevisionID: dtRev.ID,
			Source:            models.RevSourceAIGenerate,
			SectionsJSON:      datatypes.JSON(secsJSON),
			FullMarkdown:      "full",
			VerifierPassed:    true,
			CanApprove:        true,
			CreatedBy:         owner.ID,
		}
		if err := db.Create(rev).Error; err != nil {
			t.Fatal(err)
		}
		ev1JSON, _ := json.Marshal([]models.EvidenceAnchor{{
			FilePath: "src/order.cpp", SymbolKind: "cpp_func", FQSymbol: "order::Create", ContentFingerprint: fpBySym["order::Create"], CommitSHA: commitSHA, LineStart: 2, LineEnd: 4,
		}})
		ev2JSON, _ := json.Marshal([]models.EvidenceAnchor{{
			FilePath: "src/order.cpp", SymbolKind: "cpp_func", FQSymbol: "order::Pay", ContentFingerprint: fpBySym["order::Pay"], CommitSHA: commitSHA, LineStart: 5, LineEnd: 7,
		}})
		if err := db.Create(&[]models.SectionEvidence{
			{DocumentID: doc.ID, RevisionID: rev.ID, SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 概览", Ownership: models.OwnershipAuto, ContentSHA256: secs[0].ContentSHA256, EvidencesJSON: datatypes.JSON(ev1JSON)},
			{DocumentID: doc.ID, RevisionID: rev.ID, SlotID: "core_request_flows", SectionOrder: 2, Title: "3. 链路", Ownership: models.OwnershipHybrid, ContentSHA256: secs[1].ContentSHA256, EvidencesJSON: datatypes.JSON(ev2JSON)},
		}).Error; err != nil {
			t.Fatal(err)
		}
		updates := map[string]any{"current_revision_id": rev.ID}
		if status == models.DocStatusApproved {
			updates["published_revision_id"] = rev.ID
		}
		if err := db.Model(doc).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
		return doc
	}

	_ = createPublishedDoc(repoBad.ID, "1111111111111111111111111111111111111111", models.DocStatusApproved)
	docH1 := createPublishedDoc(repoH1.ID, sha1V1, models.DocStatusApproved)
	docH2 := createPublishedDoc(repoH2.ID, sha2V1, models.DocStatusApproved)
	// draft-only-repo 只有草稿，无 approved 文档
	_ = createPublishedDoc(repoDraft.ID, sha1V1, models.DocStatusDraft)

	masterStore := models.NewSharedMasterStore(db)
	docStore := models.NewDocumentStore(db)
	driftStore := models.NewDriftStore(db)
	rbacSvc := services.NewRBACService(masterStore)
	leaseMgr := services.NewActiveLeaseManager()

	driftOrch := services.NewDriftOrchestrator(
		masterStore, docStore, docTypeStore, docTypeSvc, driftStore, rbacSvc, nil, agg, leaseMgr, sandboxDir, 15,
	)

	poller := services.NewRemoteHeadPoller(
		services.PollerConfig{Enabled: false, PollInterval: 15 * time.Minute, Concurrency: 4, RepoTimeout: 10 * time.Second},
		driftStore,
		services.DefaultRemoteHeadFetcher{},
		func(pCtx context.Context, r models.PollableRepoRecord, sha, branch string) {
			if err := driftOrch.EvaluateRepoCodeDrift(pCtx, r, sha, branch); err != nil {
				t.Errorf("评估仓库 #%d Code Drift 失败: %v", r.RepoID, err)
			}
		},
		nil,
	)

	res, err := poller.PollOnce(ctx, false)
	if err != nil {
		t.Fatalf("PollOnce 失败: %v", err)
	}
	if res.MonitoredRepos != 3 || res.SucceededRepos != 2 || res.FailedRepos != 1 {
		t.Fatalf("巡检计数不符预期（应仅监控 3 个含已发布文档的仓，2 成功 1 失败）: %+v", res)
	}

	// 断言 1：无效仓 consecutive_failures == 1 且设置 15m 退避 next_retry_at
	badHeads, err := driftStore.ListRemoteHeadsByRepos(ctx, []uint{repoBad.ID})
	if err != nil || len(badHeads) != 1 {
		t.Fatalf("应记录无效仓退避状态: heads=%+v err=%v", badHeads, err)
	}
	if badHeads[0].ConsecutiveFailures != 1 || badHeads[0].NextRetryAt == nil {
		t.Fatalf("无效仓应设置 consecutive_failures=1 与 next_retry_at: %+v", badHeads[0])
	}
	if time.Until(*badHeads[0].NextRetryAt) < 10*time.Minute {
		t.Fatalf("第 1 次失败退避窗口应为 ~15m，实际 next_retry_at=%v", badHeads[0].NextRetryAt)
	}

	// 断言 2：不含已发布文档的草稿仓绝对不被巡检
	draftHeads, _ := driftStore.ListRemoteHeadsByRepos(ctx, []uint{repoDraft.ID})
	if len(draftHeads) != 0 {
		t.Fatalf("不含已发布文档的仓库不应被巡检，实际记录=%+v", draftHeads)
	}

	// 断言 3：两个健康仓更新 remote_head_sha 且仅 core_request_flows 产生 Code Drift（overview_boundaries 零误报）
	for _, tc := range []struct {
		repoID  uint
		docID   uint
		wantSHA string
	}{
		{repoH1.ID, docH1.ID, sha1V2},
		{repoH2.ID, docH2.ID, sha2V2},
	} {
		heads, _ := driftStore.ListRemoteHeadsByRepos(ctx, []uint{tc.repoID})
		if len(heads) != 1 || heads[0].RemoteHeadSHA != tc.wantSHA || heads[0].ConsecutiveFailures != 0 {
			t.Fatalf("健康仓 #%d remote_head_sha 未正确更新: got=%+v wantSHA=%s", tc.repoID, heads, tc.wantSHA)
		}
		drifts, _ := driftStore.ListDriftsByDocIDs(ctx, []uint{tc.docID})
		if len(drifts) == 0 || !drifts[0].HasDrift || drifts[0].AffectedSlotCount != 1 {
			t.Fatalf("健康仓文档 #%d 应仅 1 个章节发生漂移，实际=%+v", tc.docID, drifts)
		}
		var slots []models.AffectedSlotDrift
		_ = json.Unmarshal(drifts[0].AffectedSlotsJSON, &slots)
		if len(slots) != 1 || slots[0].SlotID != "core_request_flows" || slots[0].Reason != "symbol_modified" {
			t.Fatalf("健康仓文档 #%d 受影响槽位不符预期: %+v", tc.docID, slots)
		}
	}

	// 再次非强制巡检时，无效仓处于退避期内应被跳过
	res2, err := poller.PollOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.SkippedBackoff != 1 || res2.FailedRepos != 0 {
		t.Fatalf("退避期内的无效仓应被跳过: %+v", res2)
	}
}

// TestIncrementalRefreshOnlyRegeneratesAffectedNonLockedSlots 验证 SC-11 单篇文档增量刷新：
// 文档共 5 槽位，第 2 槽位 locked 且受漂移影响、第 3 槽位 auto 且受漂移影响、第 1/4/5 槽位未受影响；
// 触发增量刷新完成后断言：
// 1. 仅第 3 槽位调用了 Mock LLM（总 LLM 调用次数 == 1）；
// 2. 第 1/2/4/5 槽位 content_sha256 字节级不变；
// 3. 第 2 槽位标记 needs_drift_review == true。
func TestIncrementalRefreshOnlyRegeneratesAffectedNonLockedSlots(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}
	ctx := context.Background()

	dept := commonModels.Department{Name: "交易研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := commonModels.User{Username: "inc_owner", Password: "x", Name: "Owner", DepartmentID: &dept.ID}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}

	bareURL, _, commitSHA := createBareGitFixture(t, "inc-refresh-repo", map[string]string{
		"src/order.cpp": `namespace order { int Pay(int a) { return a + 42; } }`,
	})
	repo := commonModels.Repository{Name: "inc-refresh-repo", URL: bareURL, Branch: "master", DepartmentID: dept.ID, OwnerID: owner.ID, IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	// 启动 Mock OpenAI Compatible LLM，记录调用次数与请求内容
	var (
		llmCalls   atomic.Int32
		llmPrompts []string
		promptMu   sync.Mutex
	)
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		llmCalls.Add(1)
		promptMu.Lock()
		for _, m := range body.Messages {
			if m.Role == "user" {
				llmPrompts = append(llmPrompts, m.Content)
			}
		}
		promptMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-inc-1",
			"model": "mock-flagship",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "### 更新后的核心业务链路\n已根据最新 `src/order.cpp` 中 `order::Pay` 实现完成增量刷新。\n```json\n{\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::Pay\"}]}\n```",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     800,
				"completion_tokens": 120,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 400,
				},
			},
		})
	}))
	defer mockLLM.Close()

	// 配置默认 LLM 端点
	govStore := models.NewGovernanceStore(db)
	llmCfgSvc := services.NewLLMConfigService(govStore)
	if _, _, err := llmCfgSvc.CreateConfig(ctx, services.LLMConfigUpsertRequest{
		Name:             "DeepSeek-V3",
		BaseURL:          mockLLM.URL + "/v1",
		ModelName:        "mock-deepseek-v3",
		APIKey:           "sk-test-key-123456",
		TimeoutSeconds:   30,
		MaxRetries:       1,
		MaxTokensPerTask: 200000,
		MaxFilesPerTask:  2000,
	}, owner.ID); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	docTypeReg, err := doctype.NewRegistry(filepath.Join(tmpDir, "doc_types"))
	if err != nil {
		t.Fatal(err)
	}
	docTypeStore := models.NewDocTypeStore(db)
	docTypeSvc := services.NewDocTypeService(docTypeReg, docTypeStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	_, dtRev, err := docTypeStore.GetBySlug(ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}

	facetStore, err := codeir.NewLocalFSFacetStore(filepath.Join(tmpDir, "ir_cache"), 0)
	if err != nil {
		t.Fatal(err)
	}
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	snapStore := models.NewSnapshotStore(db)
	masterStore := models.NewSharedMasterStore(db)
	taskStore := models.NewTaskStore(db)
	docStore := models.NewDocumentStore(db)
	driftStore := models.NewDriftStore(db)
	rbacSvc := services.NewRBACService(masterStore)
	leaseMgr := services.NewActiveLeaseManager()

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, services.DefaultGitSyncer{}, filepath.Join(tmpDir, "sandbox_dir"), 1048576).
		WithLeaseManager(leaseMgr)
	genSvc := services.NewGenerationOrchestrator(masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, llm.NewClient(), 4).
		WithGovernance(leaseMgr, nil)
	driftOrch := services.NewDriftOrchestrator(masterStore, docStore, docTypeStore, docTypeSvc, driftStore, rbacSvc, genSvc, agg, leaseMgr, filepath.Join(tmpDir, "sandbox_dir"), 15)

	// 构造 system-architecture 的 5 个槽位文档：
	// slot 1 (overview_boundaries): auto, 未受影响
	// slot 2 (runtime_concurrency): locked, 受漂移影响 -> 必须保留原字节且标 needs_drift_review=true
	// slot 3 (core_request_flows): auto, 受漂移影响 -> 唯一允许调用 Mock LLM 重生成的槽位！
	// slot 4 (fault_tolerance_deps): auto, 未受影响
	// slot 5 (adr_constraints_debt): locked, 未受影响
	initialSections := []models.DocumentSectionSnapshot{
		{SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 系统定位与模块分层边界", Ownership: models.OwnershipAuto, Markdown: "原始第1章内容-保持字节不变", ContentSHA256: shaHex("原始第1章内容-保持字节不变")},
		{SlotID: "runtime_concurrency", SectionOrder: 2, Title: "2. 运行时并发模型", Ownership: models.OwnershipLocked, Markdown: "人工锁定第2章内容-受漂移但绝不覆盖", ContentSHA256: shaHex("人工锁定第2章内容-受漂移但绝不覆盖")},
		{SlotID: "core_request_flows", SectionOrder: 3, Title: "3. 端到端核心业务链路", Ownership: models.OwnershipAuto, Markdown: "旧版第3章内容-待增量刷新", ContentSHA256: shaHex("旧版第3章内容-待增量刷新")},
		{SlotID: "fault_tolerance_deps", SectionOrder: 4, Title: "4. 外部依赖与容错", Ownership: models.OwnershipAuto, Markdown: "原始第4章内容-保持字节不变", ContentSHA256: shaHex("原始第4章内容-保持字节不变")},
		{SlotID: "adr_constraints_debt", SectionOrder: 5, Title: "5. 架构约束与技术债", Ownership: models.OwnershipLocked, Markdown: "原始第5章内容-保持字节不变", ContentSHA256: shaHex("原始第5章内容-保持字节不变")},
	}
	secsJSON, _ := json.Marshal(initialSections)

	doc := &models.Document{
		RepoID:             repo.ID,
		Subdirectory:       "",
		RefName:            "master",
		DocTypeSlug:        "system-architecture",
		DocTypeRevisionID:  dtRev.ID,
		Title:              "inc-refresh-repo 系统架构",
		Status:             models.DocStatusApproved,
		CurrentRevisionNo:  1,
		PublishedCommitSHA: commitSHA,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	rev1 := &models.DocumentRevision{
		DocumentID:        doc.ID,
		RevisionNo:        1,
		SnapshotID:        0,
		CommitSHA:         commitSHA,
		DocTypeRevisionID: dtRev.ID,
		Source:            models.RevSourceAIGenerate,
		SectionsJSON:      datatypes.JSON(secsJSON),
		FullMarkdown:      "initial full",
		VerifierPassed:    true,
		CanApprove:        true,
		CreatedBy:         owner.ID,
	}
	if err := db.Create(rev1).Error; err != nil {
		t.Fatal(err)
	}
	secEvs := make([]models.SectionEvidence, 0, len(initialSections))
	for _, sn := range initialSections {
		secEvs = append(secEvs, models.SectionEvidence{
			DocumentID:    doc.ID,
			RevisionID:    rev1.ID,
			SlotID:        sn.SlotID,
			SectionOrder:  sn.SectionOrder,
			Title:         sn.Title,
			Ownership:     sn.Ownership,
			ContentSHA256: sn.ContentSHA256,
		})
	}
	if err := db.Create(&secEvs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(doc).Updates(map[string]any{
		"current_revision_id":   rev1.ID,
		"published_revision_id": rev1.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 写入 Code Drift：第 2 槽位（locked）与第 3 槽位（auto）同时受漂移影响
	affectedSlotsJSON, _ := json.Marshal([]models.AffectedSlotDrift{
		{SlotID: "runtime_concurrency", SlotTitle: "2. 运行时并发模型", Ownership: models.OwnershipLocked, Reason: "symbol_modified", ChangeKind: "modified", ChangedSymbols: []string{"order::Pay"}},
		{SlotID: "core_request_flows", SlotTitle: "3. 端到端核心业务链路", Ownership: models.OwnershipAuto, Reason: "symbol_modified", ChangeKind: "modified", ChangedSymbols: []string{"order::Pay"}},
	})
	if err := driftStore.UpsertDocumentDrift(ctx, &models.DocumentDrift{
		DocumentID:        doc.ID,
		DriftType:         models.DriftTypeCode,
		HasDrift:          true,
		Precision:         models.DriftPrecisionSectionLevel,
		BaseCommitSHA:     commitSHA,
		RemoteHeadSHA:     "2222222222222222222222222222222222222222",
		AffectedSlotCount: 2,
		AffectedSlotsJSON: datatypes.JSON(affectedSlotsJSON),
		DetectedAt:        time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// 触发增量刷新并等待后台协程完成
	taskVO, err := driftOrch.TriggerIncrementalRefresh(ctx, doc.ID, services.IncrementalRefreshRequest{}, owner.ID)
	if err != nil {
		t.Fatalf("TriggerIncrementalRefresh 失败: %v", err)
	}
	if taskVO.TaskKind != models.TaskKindIncrementalRefresh {
		t.Fatalf("预期 task_kind=incremental_refresh，实际 %q", taskVO.TaskKind)
	}
	if len(taskVO.TargetSlots) != 1 || taskVO.TargetSlots[0] != "core_request_flows" {
		t.Fatalf("应仅调度非锁定受影响槽位 [core_request_flows]，实际 %+v", taskVO.TargetSlots)
	}

	genSvc.WaitAll()

	// 断言 1：Mock LLM 严格仅被调用 1 次（仅第 3 槽位）
	if gotCalls := llmCalls.Load(); gotCalls != 1 {
		t.Fatalf("预期仅第 3 槽位调用 Mock LLM 1 次，实际调用 %d 次", gotCalls)
	}

	// 断言 2：任务成功就绪并产出 Revision 2
	finalTask, _, err := taskStore.GetTaskWithSlots(ctx, taskVO.ID)
	if err != nil || finalTask.Status != models.TaskStatusReady {
		t.Fatalf("增量刷新任务应为 ready，实际=%+v err=%v", finalTask, err)
	}

	rev2, rev2Evs, err := docStore.GetRevisionWithSections(ctx, doc.ID, 2)
	if err != nil {
		t.Fatalf("读取新修订 rev2 失败: %v", err)
	}
	if rev2.Source != models.RevSourceIncrementalRefresh {
		t.Fatalf("预期 rev2.source=incremental_refresh，实际 %q", rev2.Source)
	}

	var newSnaps []models.DocumentSectionSnapshot
	_ = json.Unmarshal(rev2.SectionsJSON, &newSnaps)
	snapBySlot := make(map[string]models.DocumentSectionSnapshot, len(newSnaps))
	for _, s := range newSnaps {
		snapBySlot[s.SlotID] = s
	}
	evBySlot := make(map[string]models.SectionEvidence, len(rev2Evs))
	for _, e := range rev2Evs {
		evBySlot[e.SlotID] = e
	}

	// 断言 3：第 1/2/4/5 槽位 content_sha256 字节级不变
	for _, unchanged := range []models.DocumentSectionSnapshot{initialSections[0], initialSections[1], initialSections[3], initialSections[4]} {
		got := snapBySlot[unchanged.SlotID]
		if got.ContentSHA256 != unchanged.ContentSHA256 || got.Markdown != unchanged.Markdown {
			t.Fatalf("槽位 %s 必须字节级不变: before=%s after=%s", unchanged.SlotID, unchanged.ContentSHA256, got.ContentSHA256)
		}
	}

	// 断言 4：第 2 槽位（locked 且受漂移影响）标记 needs_drift_review == true
	if !snapBySlot["runtime_concurrency"].NeedsDriftReview || !evBySlot["runtime_concurrency"].NeedsDriftReview {
		t.Fatalf("受漂移影响的锁定槽位 runtime_concurrency 必须标 needs_drift_review=true")
	}

	// 断言 5：第 3 槽位内容已更新
	if snapBySlot["core_request_flows"].ContentSHA256 == initialSections[2].ContentSHA256 ||
		!strings.Contains(snapBySlot["core_request_flows"].Markdown, "增量刷新") {
		t.Fatalf("第 3 槽位 core_request_flows 未更新为新生成内容: %+v", snapBySlot["core_request_flows"])
	}
}

// TestBackgroundWorkersCleanShutdownNoGoroutineLeak 验证 AC-M6-03：
// 后台 RemoteHeadPoller、ResourceReaper 与 TaskScheduler 在 ctx 取消与 Stop 后全部干净退出，无 goroutine 泄漏。
func TestBackgroundWorkersCleanShutdownNoGoroutineLeak(t *testing.T) {
	baseGoroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	leaseMgr := services.NewActiveLeaseManager()
	sched := services.NewTaskScheduler(services.SchedulerConfig{MaxConcurrency: 2, MaxUserConcurrency: 2})
	reaper := services.NewResourceReaper(services.ReaperConfig{
		SandboxDir:   t.TempDir(),
		IRCacheDir:   t.TempDir(),
		PublishDir:   t.TempDir(),
		ReapInterval: 10 * time.Millisecond,
	}, nil, nil, leaseMgr, sched)
	poller := services.NewRemoteHeadPoller(services.PollerConfig{
		Enabled:      true,
		PollInterval: 10 * time.Millisecond,
		Concurrency:  2,
	}, nil, nil, nil, nil)

	sched.Start(ctx)
	reaper.Start(ctx)
	poller.Start(ctx)

	time.Sleep(25 * time.Millisecond)

	cancel()
	poller.Stop()
	reaper.Stop()
	sched.Stop()

	time.Sleep(40 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	if afterGoroutines > baseGoroutines+4 {
		t.Fatalf("停机后存在潜在 goroutine 泄漏: before=%d after=%d", baseGoroutines, afterGoroutines)
	}
}

func shaHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func itoaUint(v uint) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
