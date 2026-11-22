package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type countingBranchFetcher struct {
	inner     services.DefaultRemoteBranchFetcher
	listCalls atomic.Int32
	headCalls atomic.Int32
	listErr   error
}

func (f *countingBranchFetcher) ListRemoteBranches(ctx context.Context, repoURL string) ([]gitsync.RemoteBranch, string, error) {
	f.listCalls.Add(1)
	if f.listErr != nil {
		return nil, "", f.listErr
	}
	return f.inner.ListRemoteBranches(ctx, repoURL)
}

func (f *countingBranchFetcher) LsRemoteHead(ctx context.Context, repoURL, branch string) (string, string, error) {
	f.headCalls.Add(1)
	return f.inner.LsRemoteHead(ctx, repoURL, branch)
}

type branchTestEnv struct {
	db          *gorm.DB
	router      *gin.Engine
	repoSvc     *services.RepoService
	moduleSvc   *services.ModuleService
	genSvc      *services.GenerationOrchestrator
	driftSvc    *services.DriftOrchestrator
	branchSvc   *services.BranchService
	fetcher     *countingBranchFetcher
	masterStore *models.SharedMasterStore
	moduleStore *models.ModuleStore
	docStore    *models.DocumentStore
	taskStore   *models.TaskStore
	driftStore  *models.DriftStore
	repo        models.Repository
	bareDir     string
	mainSHA     string
	featSHA     string
	owner       models.User
	ownerTok    string
	subdirOwner models.User
	subdirTok   string
	otherUser   models.User
	otherTok    string
	adminUser   models.User
	adminTok    string
}

func runBranchTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test Bot",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Bot",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败: %v\n输出: %s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func writeTestFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func createMultiBranchBareRepo(t *testing.T) (bareDir, mainSHA, featSHA string) {
	t.Helper()
	tmp := t.TempDir()
	bareDir = filepath.Join(tmp, "origin.git")
	workDir := filepath.Join(tmp, "work")

	runBranchTestGit(t, "", "init", "--bare", bareDir)
	runBranchTestGit(t, "", "init", "-b", "main", workDir)

	writeTestFiles(t, workDir, map[string]string{
		"go.mod":               "module demo.internal/app\n\ngo 1.22\n",
		"common/pkg/logger.go": "package common\nfunc Log(s string) int { return len(s) }\n",
		"common/pkg/config.go": "package common\nfunc Load() string { return \"ok\" }\n",
		"common/pkg/errors.go": "package common\nvar ErrBad = \"bad\"\n",
	})
	runBranchTestGit(t, workDir, "add", ".")
	runBranchTestGit(t, workDir, "commit", "-m", "feat: initial common module on main")
	mainSHA = runBranchTestGit(t, workDir, "rev-parse", "HEAD")

	runBranchTestGit(t, workDir, "checkout", "-b", "same-sha-branch")
	runBranchTestGit(t, workDir, "checkout", "main")
	runBranchTestGit(t, workDir, "checkout", "-b", "feat/branch-only")

	writeTestFiles(t, workDir, map[string]string{
		"payment/service/pay.go":    "package service\nfunc Pay(id int) int { return id + 1 }\n",
		"payment/service/refund.go": "package service\nfunc Refund(id int) int { return id }\n",
		"payment/model/order.go":    "package model\ntype Order struct{ ID int }\n",
	})
	runBranchTestGit(t, workDir, "add", ".")
	runBranchTestGit(t, workDir, "commit", "-m", "feat: add payment module only on feat branch")
	featSHA = runBranchTestGit(t, workDir, "rev-parse", "HEAD")

	runBranchTestGit(t, workDir, "remote", "add", "origin", "file://"+bareDir)
	runBranchTestGit(t, workDir, "push", "origin", "main", "same-sha-branch", "feat/branch-only")
	runBranchTestGit(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	return bareDir, mainSHA, featSHA
}

func setupBranchTestEnv(t *testing.T) *branchTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m13-branch-switch-jwt-secret-key-987654"
	config.AppConfig.Auth.StandaloneMode = true

	bareDir, mainSHA, featSHA := createMultiBranchBareRepo(t)
	tmpRoot := t.TempDir()
	docTypesDir := filepath.Join(tmpRoot, "doc_types")
	irCacheDir := filepath.Join(tmpRoot, "ir_cache")
	sandboxDir := filepath.Join(tmpRoot, "sandbox")
	publishDir := filepath.Join(tmpRoot, "publish")

	reg, err := doctype.NewRegistry(docTypesDir)
	if err != nil {
		t.Fatal(err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(irCacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
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
	moduleStore := models.NewModuleStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc).
		WithModuleRepo(moduleStore)
	llmSvc := services.NewLLMConfigService(govStore)
	docTypeSvc := services.NewDocTypeService(reg, docTypeStore)
	if _, err := docTypeSvc.SeedAndSyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	leaseMgr := services.NewActiveLeaseManager()
	bgCtx, bgCancel := context.WithCancel(context.Background())
	scheduler := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      2,
		MaxUserConcurrency:  2,
		InteractiveQueueCap: 16,
		BatchQueueCap:       32,
		HighBurstRatio:      4,
	})
	scheduler.Start(bgCtx)

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	// 使用真实 DefaultGitSyncer 对接 file:// bare 仓库，完整验证 gitsync 浅克隆换分支语义
	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, services.DefaultGitSyncer{}, sandboxDir, 1048576).
		WithLeaseManager(leaseMgr)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"mock-fast","choices":[{"message":{"content":"{\"markdown\":\"分支生成内容\",\"cited_anchors\":[]}"}}]}`))
	}))
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 5 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, llmClient, 2,
	).WithGovernance(leaseMgr, scheduler)

	driftSvc := services.NewDriftOrchestrator(
		masterStore, docStore, docTypeStore, docTypeSvc, driftStore, rbacSvc, genSvc, agg, leaseMgr, sandboxDir, 15,
	).WithSnapshotService(snapSvc).WithModuleRepo(moduleStore)

	publishSvc := services.NewPublishService(docStore, docTypeStore, masterStore, govStore, publishSink).
		WithDriftOrchestrator(driftSvc)

	moduleSvc := services.NewModuleService(
		masterStore, moduleStore, docStore, docTypeStore, taskStore, driftStore, snapSvc, publishSvc, driftSvc,
	)
	snapSvc.WithOnBaseExtracted(func(ctx context.Context, repoID uint, commitSHA string, tree *codeir.TreeFacet) {
		_ = moduleSvc.SyncModulesFromTree(ctx, repoID, commitSHA, tree)
	})
	genSvc.WithModuleGovernance(moduleStore, moduleSvc, rbacSvc)

	fetcher := &countingBranchFetcher{}
	branchSvc := services.NewBranchService(masterStore, moduleStore, docStore, taskStore, driftStore, driftSvc, fetcher)

	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, services.DefaultGitSyncer{}, agg, sandboxDir).
		WithLeaseManager(leaseMgr).WithSnapshotService(snapSvc)

	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    256,
		BatchSize:   1,
		FlushPeriod: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		bgCancel()
		genSvc.CancelAll()
		scheduler.Stop()
		genSvc.WaitAll()
		_ = commonAudit.Close(context.Background())
		mockLLM.Close()
	})

	llmCfg := models.LLMConfig{
		Name:             "M13-Mock-LLM",
		BaseURL:          mockLLM.URL,
		ModelName:        "mock-branch-model",
		APIKey:           "sk-m13-mock-0000",
		APIKeyMasked:     "sk-****0000",
		TimeoutSeconds:   15,
		MaxRetries:       0,
		MaxTokensPerTask: 500000,
		MaxFilesPerTask:  5000,
		IsActive:         true,
		IsDefault:        true,
	}
	if err := db.Create(&llmCfg).Error; err != nil {
		t.Fatal(err)
	}

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithModuleService(moduleSvc).
		WithBranchService(branchSvc).
		WithDocumentServices(revSvc, previewSvc, publishSvc).
		WithGovernanceServices(driftSvc, nil, nil, nil)

	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)

	dept := models.Department{Name: "基础架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "branch_owner_m13", "仓库负责人", []string{"user"}, &dept.ID)
	subdirOwner := createTestUser(t, db, "branch_subdir_m13", "子目录负责人", []string{"user"}, &dept.ID)
	other := createTestUser(t, db, "branch_other_m13", "普通成员", []string{"user"}, &dept.ID)
	admin := createTestUser(t, db, "branch_admin_m13", "系统管理员", []string{"archdoc_admin"}, &dept.ID)

	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "arch_demo_app",
		URL:          "file://" + bareDir,
		OwnerID:      owner.ID,
		Branch:       "main",
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Create(&models.ArchitectureElement{
		RepoID:       &repo.ID,
		NameCn:       "公共基础模块",
		NameEn:       "CommonPkg",
		Subdirectory: "common",
		OwnerID:      &subdirOwner.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	return &branchTestEnv{
		db:          db,
		router:      r,
		repoSvc:     repoSvc,
		moduleSvc:   moduleSvc,
		genSvc:      genSvc,
		driftSvc:    driftSvc,
		branchSvc:   branchSvc,
		fetcher:     fetcher,
		masterStore: masterStore,
		moduleStore: moduleStore,
		docStore:    docStore,
		taskStore:   taskStore,
		driftStore:  driftStore,
		repo:        repo,
		bareDir:     bareDir,
		mainSHA:     mainSHA,
		featSHA:     featSHA,
		owner:       owner,
		ownerTok:    issueTestToken(t, owner),
		subdirOwner: subdirOwner,
		subdirTok:   issueTestToken(t, subdirOwner),
		otherUser:   other,
		otherTok:    issueTestToken(t, other),
		adminUser:   admin,
		adminTok:    issueTestToken(t, admin),
	}
}

func doBranchJSONReq(t *testing.T, router *gin.Engine, method, path, tok string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestBranchSwitchListCacheAndErrorMapping 验证 T-ADR07-04 / T-ADR07-05：
// GET /api/repos/:repo_id/branches 分支列表返回、30s 缓存复用、?refresh=true 强制刷新及 502 错误码映射。
func TestBranchSwitchListCacheAndErrorMapping(t *testing.T) {
	env := setupBranchTestEnv(t)

	// 1. 首次查询：枚举 3 个分支，标记 default/current/is_head
	rec1 := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/branches", env.repo.ID), env.otherTok, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("首次查询远端分支期望 200，实际 %d %s", rec1.Code, rec1.Body.String())
	}
	var vo1 services.RepoBranchesVO
	if err := json.Unmarshal(rec1.Body.Bytes(), &vo1); err != nil {
		t.Fatal(err)
	}
	if vo1.DefaultBranch != "main" || vo1.CurrentBranch != "main" || vo1.IsOverridden {
		t.Fatalf("默认分支状态不符合预期: %+v", vo1)
	}
	if len(vo1.Branches) != 3 {
		t.Fatalf("期望枚举 3 个远端分支，实际 %d: %+v", len(vo1.Branches), vo1.Branches)
	}
	if env.fetcher.listCalls.Load() != 1 {
		t.Fatalf("首次查询应调用远端 1 次，实际 %d", env.fetcher.listCalls.Load())
	}

	// 2. 30s TTL 缓存命中：不带 refresh 再次查询不应增加 listCalls
	rec2 := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/branches", env.repo.ID), env.ownerTok, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("缓存查询远端分支期望 200，实际 %d", rec2.Code)
	}
	if env.fetcher.listCalls.Load() != 1 {
		t.Fatalf("30s 缓存期内不应重复触发 ls-remote，实际 calls=%d", env.fetcher.listCalls.Load())
	}

	// 3. ?refresh=true 强制穿透缓存
	rec3 := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/branches?refresh=true", env.repo.ID), env.ownerTok, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("强制刷新分支列表期望 200，实际 %d", rec3.Code)
	}
	if env.fetcher.listCalls.Load() != 2 {
		t.Fatalf("refresh=true 必须穿透缓存重新请求远端，期望 calls=2，实际 %d", env.fetcher.listCalls.Load())
	}

	// 4. 远端不可达时应返回 502 REMOTE_BRANCH_FETCH_FAILED（注意先 refresh=true 穿透缓存）
	env.fetcher.listErr = errors.New("network timeout")
	recErr := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/branches?refresh=true", env.repo.ID), env.ownerTok, nil)
	if recErr.Code != http.StatusBadGateway || !strings.Contains(recErr.Body.String(), "REMOTE_BRANCH_FETCH_FAILED") {
		t.Fatalf("远端分支拉取失败期望 502 REMOTE_BRANCH_FETCH_FAILED，实际 %d %s", recErr.Code, recErr.Body.String())
	}
}

// TestBranchSwitchRBACGuardsAndConflict 验证 T-ADR07-07 ~ T-ADR07-10：
// RBAC（普通用户与仅子目录负责人被拒 403）、活跃任务 409、非法分支名 400、不存在分支 400。
func TestBranchSwitchRBACGuardsAndConflict(t *testing.T) {
	env := setupBranchTestEnv(t)
	url := fmt.Sprintf("/api/repos/%d/branch", env.repo.ID)

	// 1. 普通成员与仅子目录负责人均返回 403 FORBIDDEN
	for _, tc := range []struct {
		name string
		tok  string
	}{
		{"普通成员", env.otherTok},
		{"仅子目录负责人", env.subdirTok},
	} {
		rec := doBranchJSONReq(t, env.router, http.MethodPut, url, tc.tok, map[string]string{"branch": "feat/branch-only"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 尝试切换分支应返回 403 FORBIDDEN，实际 %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	// 2. 参数校验与命令注入防护：--option / .. / 空格 返回 400 INVALID_PARAM
	for _, bad := range []string{"--upload-pack=evil", "-flag", "feat..main", "bad branch"} {
		rec := doBranchJSONReq(t, env.router, http.MethodPut, url, env.ownerTok, map[string]string{"branch": bad})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法分支名 %q 应返回 400，实际 %d %s", bad, rec.Code, rec.Body.String())
		}
	}

	// 3. 远端不存在的分支返回 400 INVALID_PARAM
	recMissing := doBranchJSONReq(t, env.router, http.MethodPut, url, env.ownerTok, map[string]string{"branch": "nonexistent/foo"})
	if recMissing.Code != http.StatusBadRequest {
		t.Fatalf("远端不存在的分支应返回 400，实际 %d %s", recMissing.Code, recMissing.Body.String())
	}

	// 4. 存在活跃任务（running）时阻断切换并精确返回 409 CONFLICT
	activeTask := &models.GenTask{
		RepoID:          env.repo.ID,
		Subdirectory:    "",
		RefName:         "main",
		TaskKind:        models.TaskKindGenerate,
		Priority:        models.TaskPriorityInteractive,
		DocTypesJSON:    datatypes.JSON(`["system-architecture"]`),
		TargetSlotsJSON: datatypes.JSON(`[]`),
		Status:          models.TaskStatusGenerating,
		TriggeredBy:     env.owner.ID,
	}
	if err := env.db.Create(activeTask).Error; err != nil {
		t.Fatal(err)
	}
	recConflict := doBranchJSONReq(t, env.router, http.MethodPut, url, env.adminTok, map[string]string{"branch": "feat/branch-only"})
	if recConflict.Code != http.StatusConflict {
		t.Fatalf("存在活跃任务时切换分支期望 409 CONFLICT，实际 %d %s", recConflict.Code, recConflict.Body.String())
	}

	// 5. 任务终态（ready）后管理员可成功切换
	if err := env.db.Model(&models.GenTask{}).Where("id = ?", activeTask.ID).Update("status", models.TaskStatusReady).Error; err != nil {
		t.Fatal(err)
	}
	recOK := doBranchJSONReq(t, env.router, http.MethodPut, url, env.adminTok, map[string]string{"branch": "feat/branch-only"})
	if recOK.Code != http.StatusOK {
		t.Fatalf("任务结束后切换分支期望 200，实际 %d %s", recOK.Code, recOK.Body.String())
	}
}

// TestBranchSwitchLosslessRebindDriftAndReset 验证 T-ADR07-06 & 恢复默认：
// 1. 不破坏已有模块发现策略（strategy/min_effective_files/exclude_globs），不改动共享表 repositories.branch；
// 2. 存量已发布文档与草稿文档无损重绑 ref_name，保留修订历史、locked 槽位与审校批注；
// 3. 清理旧分支 repo_remote_heads 并写入新分支 HEAD；即时同步 CodeDrift（SHA 不同则漂移，相同则无漂移）；
// 4. 传入 "" 或 defaultBranch 恢复默认分支且 target_branch 归零。
func TestBranchSwitchLosslessRebindDriftAndReset(t *testing.T) {
	env := setupBranchTestEnv(t)

	// 预置自定义模块发现策略，验证 switch branch 不会覆盖这些字段
	if err := env.moduleStore.UpsertPolicy(context.Background(), &models.RepoModulePolicy{
		RepoID:            env.repo.ID,
		Strategy:          models.ModuleStrategyArchElements,
		ExcludeGlobs:      datatypes.JSON(`["vendor/**"]`),
		MinEffectiveFiles: 5,
		UpdatedBy:         &env.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// 预置 1 个已发布文档（含修订历史、locked 人工槽位与审校批注）+ 1 个草稿文档
	var dtRev models.DocTypeRevision
	if err := env.db.Order("id ASC").First(&dtRev).Error; err != nil {
		t.Fatal(err)
	}
	pubAt := time.Now()
	approvedDoc := &models.Document{
		RepoID:             env.repo.ID,
		Subdirectory:       "",
		RefName:            "main",
		DocTypeSlug:        "system-architecture",
		DocTypeRevisionID:  dtRev.ID,
		Title:              "arch_demo_app 系统架构",
		Status:             models.DocStatusApproved,
		CurrentRevisionNo:  2,
		PublishedCommitSHA: env.mainSHA,
		PublishedRelPath:   "docs/arch/system-architecture.md",
		PublishedBy:        &env.owner.ID,
		PublishedAt:        &pubAt,
	}
	if err := env.db.Create(approvedDoc).Error; err != nil {
		t.Fatal(err)
	}
	rev2 := &models.DocumentRevision{
		DocumentID:        approvedDoc.ID,
		RevisionNo:        2,
		SnapshotID:        0,
		CommitSHA:         env.mainSHA,
		DocTypeRevisionID: dtRev.ID,
		Source:            models.RevSourceManualEdit,
		FullMarkdown:      "# 人工锁定章节\n详细架构说明\n",
		VerifierPassed:    true,
		CanApprove:        true,
		CreatedBy:         env.owner.ID,
	}
	if err := env.db.Create(rev2).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Model(&models.Document{}).Where("id = ?", approvedDoc.ID).Updates(map[string]any{
		"current_revision_id":   rev2.ID,
		"published_revision_id": rev2.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	secEv := &models.SectionEvidence{
		DocumentID:    approvedDoc.ID,
		RevisionID:    rev2.ID,
		SlotID:        "architecture-overview",
		SectionOrder:  1,
		Title:         "架构总览",
		Ownership:     models.OwnershipLocked,
		ContentSHA256: "deadbeef",
	}
	if err := env.db.Create(secEv).Error; err != nil {
		t.Fatal(err)
	}
	rc := &models.ReviewComment{
		DocumentID:      approvedDoc.ID,
		CommentNo:       1,
		BaseRevisionNo:  2,
		AuthorID:        env.owner.ID,
		OriginRole:      models.ReviewOriginOwner,
		ScopeType:       models.ReviewScopeSection,
		SlotID:          "architecture-overview",
		Category:        models.ReviewCategoryCorrection,
		Content:         "请保持此章节锁定描述",
		PersistForRegen: true,
		Status:          models.ReviewStatusOpen,
	}
	if err := env.db.Create(rc).Error; err != nil {
		t.Fatal(err)
	}

	draftDoc := &models.Document{
		RepoID:            env.repo.ID,
		Subdirectory:      "common",
		RefName:           "main",
		DocTypeSlug:       "code-walkthrough",
		DocTypeRevisionID: dtRev.ID,
		Title:             "common 代码导读",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := env.db.Create(draftDoc).Error; err != nil {
		t.Fatal(err)
	}

	// 预置旧分支 repo_remote_heads 记录
	if err := env.driftStore.UpsertRemoteHead(context.Background(), &models.RepoRemoteHead{
		RepoID:        env.repo.ID,
		RefName:       "main",
		RemoteHeadSHA: env.mainSHA,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. 切换到 feat/branch-only（Commit SHA 不同于 mainSHA）
	url := fmt.Sprintf("/api/repos/%d/branch", env.repo.ID)
	recSwitch := doBranchJSONReq(t, env.router, http.MethodPut, url, env.ownerTok, map[string]string{"branch": "feat/branch-only"})
	if recSwitch.Code != http.StatusOK {
		t.Fatalf("切换分支期望 200，实际 %d %s", recSwitch.Code, recSwitch.Body.String())
	}
	var vo services.SwitchRepoBranchVO
	if err := json.Unmarshal(recSwitch.Body.Bytes(), &vo); err != nil {
		t.Fatal(err)
	}
	if vo.PreviousBranch != "main" || vo.CurrentBranch != "feat/branch-only" || !vo.IsOverridden ||
		vo.RemoteHeadSHA != env.featSHA || vo.ReboundDocuments != 2 || vo.DriftedDocuments != 1 || !vo.Changed {
		t.Fatalf("切换响应 VO 不符合契约: %+v", vo)
	}

	// 校验共享表 repositories.branch 未被改动，自建策略表持久化目标分支且未覆盖原发现策略
	var dbRepo models.Repository
	if err := env.db.First(&dbRepo, env.repo.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dbRepo.Branch != "main" {
		t.Fatalf("ADR-07 零侵入违规：共享表 repositories.branch 不应被修改，实际=%q", dbRepo.Branch)
	}
	pol, err := env.moduleStore.GetPolicy(context.Background(), env.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pol.TargetBranch != "feat/branch-only" || pol.Strategy != models.ModuleStrategyArchElements || pol.MinEffectiveFiles != 5 {
		t.Fatalf("策略表持久化目标分支时不应覆盖既有发现策略: %+v", pol)
	}

	// 校验两篇文档均无损重绑至 feat/branch-only，且 locked 槽位与批注完整保留
	for _, id := range []uint{approvedDoc.ID, draftDoc.ID} {
		var d models.Document
		if err := env.db.First(&d, id).Error; err != nil {
			t.Fatal(err)
		}
		if d.RefName != "feat/branch-only" {
			t.Fatalf("文档 #%d ref_name 期望 feat/branch-only，实际 %q", id, d.RefName)
		}
	}
	var refreshedApproved models.Document
	_ = env.db.First(&refreshedApproved, approvedDoc.ID).Error
	if refreshedApproved.PublishedRelPath != "docs/arch/system-architecture.md" || refreshedApproved.CurrentRevisionNo != 2 {
		t.Fatalf("重绑不能丢失已发布路径或版本号: %+v", refreshedApproved)
	}
	var refreshedSec models.SectionEvidence
	_ = env.db.First(&refreshedSec, secEv.ID).Error
	if refreshedSec.Ownership != models.OwnershipLocked {
		t.Fatalf("locked 人工槽位不得丢失: %+v", refreshedSec)
	}
	var refreshedRC models.ReviewComment
	_ = env.db.First(&refreshedRC, rc.ID).Error
	if refreshedRC.Content != "请保持此章节锁定描述" {
		t.Fatalf("审校批注不得丢失: %+v", refreshedRC)
	}

	// 校验 repo_remote_heads 旧分支记录已清理、仅剩 feat/branch-only
	heads, err := env.driftStore.ListRemoteHeadsByRepos(context.Background(), []uint{env.repo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].RefName != "feat/branch-only" || heads[0].RemoteHeadSHA != env.featSHA {
		t.Fatalf("远端 HEAD 表期望仅剩 feat/branch-only (%s)，实际=%+v", env.featSHA, heads)
	}

	// 校验已发布文档立即同步 CodeDrift=true，草稿不漂移
	drifts, err := env.driftStore.ListDriftsByDocIDs(context.Background(), []uint{approvedDoc.ID, draftDoc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 || drifts[0].DocumentID != approvedDoc.ID || !drifts[0].HasDrift || drifts[0].RemoteHeadSHA != env.featSHA {
		t.Fatalf("已发布文档应立即标记代码漂移: %+v", drifts)
	}

	// 2. 切换到一个 Commit SHA 与 mainSHA 一致的分支 same-sha-branch -> 代码漂移应自动归零
	recSame := doBranchJSONReq(t, env.router, http.MethodPut, url, env.ownerTok, map[string]string{"branch": "same-sha-branch"})
	if recSame.Code != http.StatusOK {
		t.Fatalf("切换至同 SHA 分支期望 200，实际 %d %s", recSame.Code, recSame.Body.String())
	}
	var sameVO services.SwitchRepoBranchVO
	_ = json.Unmarshal(recSame.Body.Bytes(), &sameVO)
	if sameVO.DriftedDocuments != 0 {
		t.Fatalf("同 SHA 分支不应存在代码漂移，实际 drifted_documents=%d", sameVO.DriftedDocuments)
	}
	driftsAfterSame, _ := env.driftStore.ListDriftsByDocIDs(context.Background(), []uint{approvedDoc.ID})
	if len(driftsAfterSame) == 0 || driftsAfterSame[0].HasDrift {
		t.Fatalf("切回与发布 SHA 一致的分支后 has_drift 应清零: %+v", driftsAfterSame)
	}

	// 3. 传入空字符串恢复默认分支（target_branch=""，is_overridden=false）
	recReset := doBranchJSONReq(t, env.router, http.MethodPut, url, env.ownerTok, map[string]string{"branch": ""})
	if recReset.Code != http.StatusOK {
		t.Fatalf("恢复默认分支期望 200，实际 %d %s", recReset.Code, recReset.Body.String())
	}
	var resetVO services.SwitchRepoBranchVO
	_ = json.Unmarshal(recReset.Body.Bytes(), &resetVO)
	if resetVO.CurrentBranch != "main" || resetVO.IsOverridden || resetVO.ReboundDocuments != 2 {
		t.Fatalf("恢复默认分支 VO 不符合预期: %+v", resetVO)
	}
	polAfterReset, _ := env.moduleStore.GetPolicy(context.Background(), env.repo.ID)
	if polAfterReset.TargetBranch != "" {
		t.Fatalf("恢复默认分支后数据库 target_branch 必须为空，实际=%q", polAfterReset.TargetBranch)
	}

	// 验证审计日志已异步落库（action=switch_branch）
	var auditCount int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = env.db.Model(&commonModels.SysAuditLog{}).Where("action = ?", "switch_branch").Count(&auditCount).Error
		if auditCount >= 1 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if auditCount < 1 {
		t.Fatalf("期望生成 action=switch_branch 的操作审计日志，实际=%d", auditCount)
	}
}

// TestBranchSwitchEndToEndWithShallowCloneAndTaskValidation 验证 T-ADR07-11：
// 1. 切换分支后，基于真实浅克隆（gitsync）触发 RefreshModules 能检出新分支并发现该分支独有的 payment 模块；
// 2. GET /api/repos、GET /api/repos/:repo_id/scopes 及 DriftStore.ListPollableRepos 全链路返回新分支与标志位；
// 3. POST /api/tasks/generate 留空 ref_name 自动使用新分支，传入旧分支返回 400。
func TestBranchSwitchEndToEndWithShallowCloneAndTaskValidation(t *testing.T) {
	env := setupBranchTestEnv(t)

	// 1. 先在默认分支 main 下刷新一次模块索引（仅 common）
	beforeMods, err := env.moduleSvc.RefreshModules(context.Background(), env.repo.ID)
	if err != nil {
		t.Fatalf("main 分支初始模块扫描失败: %v", err)
	}
	if beforeMods.ActiveCount != 1 || beforeMods.Modules[0].Subdirectory != "common" {
		t.Fatalf("main 分支期望仅发现 common 模块，实际=%+v", beforeMods.Modules)
	}

	// 2. 切换仓库目标分支至 feat/branch-only
	recSwitch := doBranchJSONReq(
		t, env.router, http.MethodPut,
		fmt.Sprintf("/api/repos/%d/branch", env.repo.ID),
		env.ownerTok,
		map[string]string{"branch": "feat/branch-only"},
	)
	if recSwitch.Code != http.StatusOK {
		t.Fatalf("切换至 feat/branch-only 失败: %d %s", recSwitch.Code, recSwitch.Body.String())
	}

	// 3. 在同一浅克隆沙箱目录下再次刷新模块：应切到 feat/branch-only 并额外发现 payment 模块
	afterMods, err := env.moduleSvc.RefreshModules(context.Background(), env.repo.ID)
	if err != nil {
		t.Fatalf("feat/branch-only 分支模块扫描失败: %v", err)
	}
	if afterMods.ActiveCount != 2 {
		t.Fatalf("feat/branch-only 期望发现 common + payment 共 2 个模块，实际 %d: %+v", afterMods.ActiveCount, afterMods.Modules)
	}
	seenSubdirs := make(map[string]bool)
	for _, m := range afterMods.Modules {
		seenSubdirs[m.Subdirectory] = true
	}
	if !seenSubdirs["common"] || !seenSubdirs["payment"] {
		t.Fatalf("feat/branch-only 必须发现 common 与 payment 目录，实际=%+v", seenSubdirs)
	}

	// 4. GET /api/repos 与 GET /api/repos/:repo_id/scopes 均反映新分支及权限字段
	recScopesOwner := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/scopes", env.repo.ID), env.ownerTok, nil)
	if recScopesOwner.Code != http.StatusOK {
		t.Fatalf("owner 查询 scopes 失败: %d %s", recScopesOwner.Code, recScopesOwner.Body.String())
	}
	var scopesOwner services.RepoScopesDetailResponse
	_ = json.Unmarshal(recScopesOwner.Body.Bytes(), &scopesOwner)
	if scopesOwner.Branch != "feat/branch-only" || scopesOwner.DefaultBranch != "main" ||
		!scopesOwner.IsBranchOverridden || !scopesOwner.CanSwitchBranch {
		t.Fatalf("owner scopes 分支字段不符合预期: %+v", scopesOwner)
	}

	recScopesSubdir := doBranchJSONReq(t, env.router, http.MethodGet, fmt.Sprintf("/api/repos/%d/scopes", env.repo.ID), env.subdirTok, nil)
	var scopesSubdir services.RepoScopesDetailResponse
	_ = json.Unmarshal(recScopesSubdir.Body.Bytes(), &scopesSubdir)
	if scopesSubdir.CanSwitchBranch {
		t.Fatalf("仅子目录负责人的 can_switch_branch 必须为 false")
	}

	recList := doBranchJSONReq(t, env.router, http.MethodGet, "/api/repos?scope_type=repo&page=1&pageSize=25", env.ownerTok, nil)
	if recList.Code != http.StatusOK {
		t.Fatalf("GET /api/repos 失败: %d", recList.Code)
	}
	var listVO services.RepoCoverageListResponse
	_ = json.Unmarshal(recList.Body.Bytes(), &listVO)
	if len(listVO.Items) == 0 || listVO.Items[0].Branch != "feat/branch-only" ||
		!listVO.Items[0].IsBranchOverridden || !listVO.Items[0].CanSwitchBranch || listVO.Items[0].DefaultBranch != "main" {
		t.Fatalf("GET /api/repos 分支字段未同步: %+v", listVO.Items)
	}

	// 5. 为仓库创建一篇已发布文档（ListPollableRepos 仅轮询含已发布文档的活跃仓库）后，验证返回新分支 feat/branch-only
	var dtRev models.DocTypeRevision
	if err := env.db.Order("id ASC").First(&dtRev).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Create(&models.Document{
		RepoID:            env.repo.ID,
		Subdirectory:      "",
		RefName:           "feat/branch-only",
		DocTypeSlug:       "code-walkthrough",
		DocTypeRevisionID: dtRev.ID,
		Title:             "arch_demo_app 代码导读",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pollable, err := env.driftStore.ListPollableRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pollable) != 1 || pollable[0].Branch != "feat/branch-only" {
		t.Fatalf("ListPollableRepos 期望解析为 feat/branch-only，实际=%+v", pollable)
	}

	// 6. POST /api/tasks/generate：显式传旧分支 main 被拒绝 400；留空 ref_name 自动使用 feat/branch-only
	recOldRef := doBranchJSONReq(t, env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":   env.repo.ID,
		"ref_name":  "main",
		"doc_types": []string{"code-walkthrough"},
	})
	if recOldRef.Code != http.StatusBadRequest {
		t.Fatalf("显式传入未绑定旧分支应返回 400 INVALID_PARAM，实际 %d %s", recOldRef.Code, recOldRef.Body.String())
	}

	recGen := doBranchJSONReq(t, env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":   env.repo.ID,
		"ref_name":  "",
		"doc_types": []string{"code-walkthrough"},
	})
	if recGen.Code != http.StatusAccepted {
		t.Fatalf("留空 ref_name 提交生成任务期望 202，实际 %d %s", recGen.Code, recGen.Body.String())
	}
	var taskVO services.GenTaskDetailVO
	_ = json.Unmarshal(recGen.Body.Bytes(), &taskVO)
	if taskVO.RefName != "feat/branch-only" {
		t.Fatalf("生成任务应自动继承新分支 feat/branch-only，实际=%q", taskVO.RefName)
	}
}
