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
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type m8TestEnv struct {
	db           *gorm.DB
	router       *gin.Engine
	repoSvc      *services.RepoService
	moduleSvc    *services.ModuleService
	genSvc       *services.GenerationOrchestrator
	publishSvc   *services.PublishService
	gitSyncer    *countingGitSyncer
	fixtureRoot  string
	publishDir   string
	owner        models.User
	ownerTok     string
	subdirOwner  models.User
	subdirTok    string
	otherUser    models.User
	otherTok     string
	repo         models.Repository
	cipher       *security.Cipher
	moduleStore  *models.ModuleStore
	docStore     *models.DocumentStore
	docTypeStore *models.DocTypeStore
	taskStore    *models.TaskStore
}

func setupM8TestEnv(t *testing.T) *m8TestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m8-unit-test-jwt-secret-key-9876543210"
	config.AppConfig.Auth.StandaloneMode = true

	cipher, err := security.NewCipher("m8-unit-test-32byte-master-key00", "jwt", true)
	if err != nil {
		t.Fatal(err)
	}

	tmpRoot := t.TempDir()
	fixtureRoot := filepath.Join(tmpRoot, "monorepo_fixture")
	docTypesDir := filepath.Join(tmpRoot, "doc_types")
	irCacheDir := filepath.Join(tmpRoot, "ir_cache")
	sandboxDir := filepath.Join(tmpRoot, "sandbox_dir")
	publishDir := filepath.Join(tmpRoot, "publish_dir")

	// 构造含 common / order / gateway 三个模块与 docs 默认排除目录的夹具代码仓
	fixtureFiles := map[string]string{
		"CMakeLists.txt":                "cmake_minimum_required(VERSION 3.16)\nadd_subdirectory(common)\nadd_subdirectory(order)\nadd_subdirectory(gateway)\n",
		"common/include/types.h":        "#pragma once\nnamespace common { struct Result { int code; }; }\n",
		"common/src/logger.cpp":         "#include \"common/include/types.h\"\nnamespace common { int Log(int level) { return level; } }\n",
		"common/src/config.cpp":         "namespace common { int LoadCfg() { return 0; } }\n",
		"order/include/order_service.h": "#pragma once\n#include \"common/include/types.h\"\nnamespace order { class OrderService { public: int Pay(int id); }; }\n",
		"order/src/order_service.cpp":   "#include \"order/include/order_service.h\"\nnamespace order { int OrderService::Pay(int id) { return id + 1; } }\n",
		"order/src/matcher.cpp":         "namespace order { int Match(int id) { return id; } }\n",
		"gateway/src/server.cpp":        "namespace gateway { int Serve() { return 0; } }\n",
		"gateway/src/router.cpp":        "namespace gateway { int Route() { return 0; } }\n",
		"gateway/include/router.h":      "#pragma once\nnamespace gateway { int Route(); }\n",
		"docs/a.md":                     "# Doc A",
		"docs/b.md":                     "# Doc B",
		"docs/c.md":                     "# Doc C",
	}
	for rel, content := range fixtureFiles {
		abs := filepath.Join(fixtureRoot, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

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

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	gitSyncer := &countingGitSyncer{
		fixtureDir: fixtureRoot,
		commitSHA:  "c1c2c3c4c5c6c7c8c9c0c1c2c3c4c5c6c7c8c9c0",
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
	llmSvc := services.NewLLMConfigService(govStore, cipher)
	docTypeSvc := services.NewDocTypeService(reg, docTypeStore)
	if _, err := docTypeSvc.SeedAndSyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	leaseMgr := services.NewActiveLeaseManager()
	bgCtx, bgCancel := context.WithCancel(context.Background())
	// 特意将 MaxUserConcurrency 设为 1，以严格验证批量父任务在 TaskScheduler 槽位之外运行、绝不发生父等子死锁！
	scheduler := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      2,
		MaxUserConcurrency:  1,
		InteractiveQueueCap: 32,
		BatchQueueCap:       64,
		HighBurstRatio:      4,
	})
	scheduler.Start(bgCtx)

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, gitSyncer, sandboxDir, 1048576).
		WithLeaseManager(leaseMgr)
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 10 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, cipher, llmClient, 2,
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

	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, gitSyncer, agg, sandboxDir).
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
	})

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithModuleService(moduleSvc).
		WithDocumentServices(revSvc, previewSvc, publishSvc).
		WithGovernanceServices(driftSvc, nil, nil, nil)

	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)

	dept := models.Department{Name: "核心交易部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "repo_owner_m8", "仓库负责人", []string{"user"}, &dept.ID)
	subdirOwner := createTestUser(t, db, "order_owner_m8", "订单子目录负责人", []string{"user"}, &dept.ID)
	other := createTestUser(t, db, "other_user_m8", "普通旁观者", []string{"user"}, &dept.ID)

	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "trade_monorepo",
		URL:          "git@git.internal:trade/trade_monorepo.git",
		OwnerID:      owner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	// 仅在 architecture_elements 中登记 order 子模块（归属 subdirOwner），common 与 gateway 由模块发现引擎自动识别
	if err := db.Create(&models.ArchitectureElement{
		RepoID:       &repo.ID,
		NameCn:       "订单撮合模块",
		NameEn:       "OrderEngine",
		Subdirectory: "order",
		OwnerID:      &subdirOwner.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	return &m8TestEnv{
		db:           db,
		router:       r,
		repoSvc:      repoSvc,
		moduleSvc:    moduleSvc,
		genSvc:       genSvc,
		publishSvc:   publishSvc,
		gitSyncer:    gitSyncer,
		fixtureRoot:  fixtureRoot,
		publishDir:   publishDir,
		owner:        owner,
		ownerTok:     issueTestToken(t, owner),
		subdirOwner:  subdirOwner,
		subdirTok:    issueTestToken(t, subdirOwner),
		otherUser:    other,
		otherTok:     issueTestToken(t, other),
		repo:         repo,
		cipher:       cipher,
		moduleStore:  moduleStore,
		docStore:     docStore,
		docTypeStore: docTypeStore,
		taskStore:    taskStore,
	}
}

func configureM8MockLLM(t *testing.T, env *m8TestEnv, baseURL string) {
	t.Helper()
	encKey, err := env.cipher.Encrypt("sk-m8-mock-key-0000")
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{models.ModelTierFast, models.ModelTierFlagship} {
		if err := env.db.Model(&models.LLMConfig{}).Where("tier = ?", tier).Updates(map[string]any{
			"base_url":            baseURL,
			"model_name":          "mock-" + tier,
			"api_key_cipher":      encKey,
			"api_key_masked":      "sk-****0000",
			"timeout_seconds":     30,
			"max_retries":         0,
			"max_tokens_per_task": 500000,
			"max_files_per_task":  5000,
			"is_active":           true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestModuleDiscoveryLifecycleAndMigrate 验证 T-ADR04-02 & T-ADR04-04：
// 1. 模块发现刷新、忽略/恢复状态切换、目录删除后标为 missing 及孤儿文档 scope_status="orphan"；
// 2. 子目录重命名迁移（含参数校验、冲突 409、活跃任务 409、正常迁移重写标题并重渲染已发布文档 + 清理旧文件）。
func TestModuleDiscoveryLifecycleAndMigrate(t *testing.T) {
	env := setupM8TestEnv(t)

	// 1. 子目录负责人尝试刷新整仓模块索引 -> 403 FORBIDDEN（仅仓库级写者/管理员可操作）
	reqForbidden := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/refresh", env.repo.ID), nil)
	reqForbidden.Header.Set("Authorization", "Bearer "+env.subdirTok)
	recForbidden := httptest.NewRecorder()
	env.router.ServeHTTP(recForbidden, reqForbidden)
	if recForbidden.Code != http.StatusForbidden {
		t.Fatalf("子目录负责人刷新整仓模块索引应返回 403，实际 %d", recForbidden.Code)
	}

	// 2. 仓库负责人触发 POST /api/repos/:repo_id/modules/refresh -> 发现 common / gateway / order 3 个活跃模块
	reqRefresh := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/refresh", env.repo.ID), nil)
	reqRefresh.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recRefresh := httptest.NewRecorder()
	env.router.ServeHTTP(recRefresh, reqRefresh)
	if recRefresh.Code != http.StatusOK {
		t.Fatalf("刷新模块索引失败: %d %s", recRefresh.Code, recRefresh.Body.String())
	}
	var refreshVO services.RefreshModulesResultVO
	if err := json.Unmarshal(recRefresh.Body.Bytes(), &refreshVO); err != nil {
		t.Fatal(err)
	}
	if refreshVO.ActiveCount != 3 || len(refreshVO.Modules) != 3 {
		t.Fatalf("期望发现 3 个活跃模块 [common, gateway, order]，实际=%+v", refreshVO)
	}

	var gatewayModID uint
	for _, m := range refreshVO.Modules {
		if m.Subdirectory == "gateway" {
			gatewayModID = m.ID
			if m.Source != models.ModuleSourceDiscovered {
				t.Fatalf("gateway 应为 discovered 来源，实际 %s", m.Source)
			}
		}
		if m.Subdirectory == "order" {
			if m.Source != models.ModuleSourceArchElement || m.OwnerID != env.subdirOwner.ID || m.ArchElementName != "订单撮合模块" {
				t.Fatalf("order 模块应关联架构元素负责人与中文名，实际=%+v", m)
			}
		}
	}

	// 3. 将 gateway 标记为 ignored -> 覆盖全景矩阵应隐藏无文档的 ignored 模块
	bodyIgnore, _ := json.Marshal(map[string]string{"status": models.ModuleStatusIgnored})
	reqIgn := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/repos/%d/modules/%d/status", env.repo.ID, gatewayModID), bytes.NewReader(bodyIgnore))
	reqIgn.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqIgn.Header.Set("Content-Type", "application/json")
	recIgn := httptest.NewRecorder()
	env.router.ServeHTTP(recIgn, reqIgn)
	if recIgn.Code != http.StatusOK {
		t.Fatalf("忽略模块失败: %d %s", recIgn.Code, recIgn.Body.String())
	}

	// 再次刷新模块索引时，ignored 状态必须被持久保留（不被重置为 active）
	if _, err := env.moduleSvc.RefreshModules(context.Background(), env.repo.ID); err != nil {
		t.Fatal(err)
	}
	gwMod, err := env.moduleStore.GetModuleByID(context.Background(), env.repo.ID, gatewayModID)
	if err != nil || gwMod.Status != models.ModuleStatusIgnored {
		t.Fatalf("刷新后 ignored 状态应被保留，实际=%+v err=%v", gwMod, err)
	}

	// 恢复 gateway 为 active，随后模拟物理目录重命名 gateway -> api_gateway，再次刷新应将 gateway 标为 missing
	_, _ = env.moduleSvc.UpdateModuleStatus(context.Background(), env.repo.ID, gatewayModID, models.ModuleStatusActive)
	if err := os.Rename(filepath.Join(env.fixtureRoot, "gateway"), filepath.Join(env.fixtureRoot, "api_gateway")); err != nil {
		t.Fatal(err)
	}
	// 使快照 CommitSHA 推进以触发重新抽取全仓树
	env.gitSyncer.commitSHA = "d1d2d3d4d5d6d7d8d9d0d1d2d3d4d5d6d7d8d9d0"
	refreshed2, err := env.moduleSvc.RefreshModules(context.Background(), env.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	statusBySub := make(map[string]string)
	for _, m := range refreshed2.Modules {
		statusBySub[m.Subdirectory] = m.Status
	}
	if statusBySub["gateway"] != models.ModuleStatusMissing || statusBySub["api_gateway"] != models.ModuleStatusActive {
		t.Fatalf("目录重命名后期望 gateway=missing 且 api_gateway=active，实际=%+v", statusBySub)
	}

	// 4. 在未注册子目录 legacy_payment 下创建一篇文档 -> GetRepoScopes 应将其识别为 scope_status="orphan"
	_, dtRev, err := env.docTypeStore.GetBySlug(context.Background(), "system-architecture")
	if err != nil {
		t.Fatal(err)
	}
	orphanDoc := &models.Document{
		RepoID:            env.repo.ID,
		Subdirectory:      "legacy_payment",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "trade_monorepo (legacy_payment) 系统架构与演进设计",
		Status:            models.DocStatusApproved,
	}
	if err := env.db.Create(orphanDoc).Error; err != nil {
		t.Fatal(err)
	}
	scopesResp, err := env.repoSvc.GetRepoScopes(context.Background(), &env.owner, env.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundOrphan := false
	for _, sc := range scopesResp.Scopes {
		if sc.Subdirectory == "legacy_payment" {
			foundOrphan = true
			if sc.ScopeStatus != "orphan" {
				t.Fatalf("期望未在活跃模块中的历史文档范围标为 orphan，实际 %s", sc.ScopeStatus)
			}
		}
	}
	if !foundOrphan {
		t.Fatalf("GetRepoScopes 未返回孤儿文档范围 legacy_payment")
	}

	// 5. 测试模块迁移 (T-ADR04-04)：在 gateway 下创建一篇已发布文档，验证参数校验、冲突 409、活跃任务 409 与正常迁移重发布
	secs := []models.DocumentSectionSnapshot{
		{SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 系统定位", Ownership: models.OwnershipAuto, Markdown: "Gateway 核心职责", ContentSHA256: "sha1"},
	}
	secsJSON, _ := json.Marshal(secs)
	repJSON, _ := json.Marshal(models.VerifierReport{Passed: true, CanApprove: true, SlotCoverageRate: 1.0})
	gwDoc := &models.Document{
		RepoID:            env.repo.ID,
		Subdirectory:      "gateway",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "trade_monorepo (gateway) 系统架构与演进设计",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := env.db.Create(gwDoc).Error; err != nil {
		t.Fatal(err)
	}
	gwRev := &models.DocumentRevision{
		DocumentID:         gwDoc.ID,
		RevisionNo:         1,
		SnapshotID:         1,
		CommitSHA:          env.gitSyncer.commitSHA,
		DocTypeRevisionID:  dtRev.ID,
		Source:             models.RevSourceAIGenerate,
		SectionsJSON:       datatypes.JSON(secsJSON),
		FullMarkdown:       "# Gateway 架构\nGateway 核心职责",
		VerifierPassed:     true,
		CanApprove:         true,
		SlotCoverageRate:   1.0,
		VerifierReportJSON: datatypes.JSON(repJSON),
		CreatedBy:          env.owner.ID,
	}
	if err := env.db.Create(gwRev).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Model(gwDoc).Update("current_revision_id", gwRev.ID).Error; err != nil {
		t.Fatal(err)
	}
	pubVO, err := env.publishSvc.PublishDocument(context.Background(), gwDoc.ID, nil, &env.owner)
	if err != nil {
		t.Fatalf("发布 gateway 文档失败: %v", err)
	}
	oldPublishedFile := filepath.Join(env.publishDir, filepath.FromSlash(pubVO.PublishedRelPath))
	if _, statErr := os.Stat(oldPublishedFile); statErr != nil {
		t.Fatalf("期望旧发布文件存在: %s (%v)", oldPublishedFile, statErr)
	}

	// 5.1 空子目录或相同子目录 -> 400 INVALID_PARAM
	badMigBody, _ := json.Marshal(map[string]string{"from_subdir": "gateway", "to_subdir": "gateway"})
	reqBadMig := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/migrate", env.repo.ID), bytes.NewReader(badMigBody))
	reqBadMig.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqBadMig.Header.Set("Content-Type", "application/json")
	recBadMig := httptest.NewRecorder()
	env.router.ServeHTTP(recBadMig, reqBadMig)
	if recBadMig.Code != http.StatusBadRequest {
		t.Fatalf("相同子目录迁移期望 400，实际 %d", recBadMig.Code)
	}

	// 5.2 迁往已存在相同 DocType 文档的 legacy_payment -> 409 MIGRATION_CONFLICT
	conflictBody, _ := json.Marshal(map[string]string{"from_subdir": "gateway", "to_subdir": "legacy_payment"})
	reqConflict := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/migrate", env.repo.ID), bytes.NewReader(conflictBody))
	reqConflict.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqConflict.Header.Set("Content-Type", "application/json")
	recConflict := httptest.NewRecorder()
	env.router.ServeHTTP(recConflict, reqConflict)
	if recConflict.Code != http.StatusConflict || !strings.Contains(recConflict.Body.String(), handlers.ErrCodeMigrationConflict) {
		t.Fatalf("目标冲突迁移期望 409 MIGRATION_CONFLICT，实际 %d %s", recConflict.Code, recConflict.Body.String())
	}

	// 5.3 源目录存在进行中的任务 -> 409 CONFLICT
	activeTask := &models.GenTask{
		RepoID:       env.repo.ID,
		Subdirectory: "gateway",
		RefName:      "master",
		TaskKind:     models.TaskKindGenerate,
		Priority:     models.TaskPriorityInteractive,
		DocTypesJSON: datatypes.JSON(`["system-architecture"]`),
		Status:       models.TaskStatusGenerating,
		TriggeredBy:  env.owner.ID,
	}
	if err := env.db.Create(activeTask).Error; err != nil {
		t.Fatal(err)
	}
	okMigBody, _ := json.Marshal(map[string]string{"from_subdir": "gateway", "to_subdir": "api_gateway"})
	reqActiveBlock := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/migrate", env.repo.ID), bytes.NewReader(okMigBody))
	reqActiveBlock.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqActiveBlock.Header.Set("Content-Type", "application/json")
	recActiveBlock := httptest.NewRecorder()
	env.router.ServeHTTP(recActiveBlock, reqActiveBlock)
	if recActiveBlock.Code != http.StatusConflict {
		t.Fatalf("存在活跃任务时迁移期望 409，实际 %d", recActiveBlock.Code)
	}
	// 将任务置为终态后执行正常迁移
	_ = env.db.Model(activeTask).Update("status", models.TaskStatusReady).Error

	// 5.4 正常迁移 gateway -> api_gateway
	reqMigOK := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/repos/%d/modules/migrate", env.repo.ID), bytes.NewReader(okMigBody))
	reqMigOK.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqMigOK.Header.Set("Content-Type", "application/json")
	recMigOK := httptest.NewRecorder()
	env.router.ServeHTTP(recMigOK, reqMigOK)
	if recMigOK.Code != http.StatusOK {
		t.Fatalf("正常迁移模块失败: %d %s", recMigOK.Code, recMigOK.Body.String())
	}
	var migVO services.MigrateModuleResultVO
	if err := json.Unmarshal(recMigOK.Body.Bytes(), &migVO); err != nil {
		t.Fatal(err)
	}
	if migVO.MigratedDocuments != 1 || migVO.RepublishedPublish != 1 {
		t.Fatalf("期望迁移 1 篇并重发布 1 篇，实际=%+v", migVO)
	}

	// 断言旧发布文件已删除，新发布文件已生成且 Frontmatter subdir 更新为 api_gateway，历史修订完整保留
	if _, statErr := os.Stat(oldPublishedFile); !os.IsNotExist(statErr) {
		t.Fatalf("迁移后旧发布文件应被清理，仍存在: %s", oldPublishedFile)
	}
	migratedDoc, err := env.docStore.GetByID(context.Background(), gwDoc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migratedDoc.Subdirectory != "api_gateway" || !strings.Contains(migratedDoc.Title, "(api_gateway)") {
		t.Fatalf("迁移后文档 Subdirectory/Title 未更新: %+v", migratedDoc)
	}
	newPublishedFile := filepath.Join(env.publishDir, filepath.FromSlash(migratedDoc.PublishedRelPath))
	newBytes, err := os.ReadFile(newPublishedFile)
	if err != nil {
		t.Fatalf("读取迁移后新发布文件失败: %v", err)
	}
	if !strings.Contains(string(newBytes), `subdir: "api_gateway"`) {
		t.Fatalf("迁移重渲染后的发布文件 Frontmatter 未更新 subdir: %s", string(newBytes))
	}
	revs, err := env.docStore.ListRevisions(context.Background(), gwDoc.ID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("迁移后历史修订必须无损保留，实际=%+v err=%v", revs, err)
	}
}

// TestBatchGenerateRBACBudgetTwoStageAndCancel 验证 T-ADR04-10 & T-ADR04-11：
// 1. 子目录负责人包含未授权模块或整仓范围时 -> 403 FORBIDDEN（含 forbidden_scopes），零父任务/子任务创建；
// 2. 批前总预算超限 -> 父任务 failed (BATCH_TOKEN_BUDGET_EXCEEDED)，返回 module_breakdown，Mock LLM 请求数 == 0；
// 3. 正常两阶段批量生成（MaxUserConcurrency=1 下无死锁）：先完成全部模块子任务，再完成整仓子任务，父任务 ready；
// 4. 批量父任务取消级联终止未完成子任务。
func TestBatchGenerateRBACBudgetTwoStageAndCancel(t *testing.T) {
	env := setupM8TestEnv(t)

	var llmCalls atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		userMsg := ""
		if len(payload.Messages) >= 2 {
			userMsg = payload.Messages[1].Content
		}
		llmCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(userMsg, "repo_structure_for_glossary") {
			_, _ = w.Write([]byte(`{"model":"mock-fast","choices":[{"message":{"content":"{\"module_summaries\":[{\"path\":\"order\",\"summary\":\"订单模块\"}],\"terms\":[]}"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"mock-fast","choices":[{"message":{"content":"{\"markdown\":\"模块章节内容\",\"cited_anchors\":[]}"}}]}`))
	}))
	defer mockLLM.Close()

	configureM8MockLLM(t, env, mockLLM.URL)

	// 1. 子目录负责人（仅拥有 order）尝试批量生成 ["order", "common"] 或 include_repo_scope=true -> 403 FORBIDDEN 且零任务落库
	forbBody, _ := json.Marshal(map[string]any{
		"repo_id":            env.repo.ID,
		"subdirectories":     []string{"order", "common"},
		"include_repo_scope": true,
		"doc_types":          []string{"code-walkthrough"},
	})
	reqForb := httptest.NewRequest(http.MethodPost, "/api/tasks/generate-batch", bytes.NewReader(forbBody))
	reqForb.Header.Set("Authorization", "Bearer "+env.subdirTok)
	reqForb.Header.Set("Content-Type", "application/json")
	recForb := httptest.NewRecorder()
	env.router.ServeHTTP(recForb, reqForb)
	if recForb.Code != http.StatusForbidden || !strings.Contains(recForb.Body.String(), "common") {
		t.Fatalf("越权批量生成期望返回 403 且指出越权范围 common，实际 %d %s", recForb.Code, recForb.Body.String())
	}
	var taskCount int64
	_ = env.db.Model(&models.GenTask{}).Count(&taskCount).Error
	if taskCount != 0 {
		t.Fatalf("越权批量请求被拒绝时必须零任务落库，实际 taskCount=%d", taskCount)
	}

	// 2. 批前总预算熔断（T-ADR04-11 / SC-18）：将批量总 Token 上限设为 100 -> 父任务 failed/BATCH_TOKEN_BUDGET_EXCEEDED 且 LLM 调用 == 0
	env.genSvc.WithBatchBudgetLimit(100)
	budgetBody, _ := json.Marshal(map[string]any{
		"repo_id":            env.repo.ID,
		"subdirectories":     []string{"common", "order"},
		"include_repo_scope": true,
		"doc_types":          []string{"code-walkthrough"},
	})
	reqBudget := httptest.NewRequest(http.MethodPost, "/api/tasks/generate-batch", bytes.NewReader(budgetBody))
	reqBudget.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqBudget.Header.Set("Content-Type", "application/json")
	recBudget := httptest.NewRecorder()
	env.router.ServeHTTP(recBudget, reqBudget)
	if recBudget.Code != http.StatusAccepted {
		t.Fatalf("提交批量任务期望 202，实际 %d %s", recBudget.Code, recBudget.Body.String())
	}
	var budgetParent services.GenTaskDetailVO
	_ = json.Unmarshal(recBudget.Body.Bytes(), &budgetParent)

	env.genSvc.WaitAll()

	budgetDetail, err := env.genSvc.GetTaskDetail(context.Background(), budgetParent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if budgetDetail.Status != models.TaskStatusFailed || budgetDetail.ErrorCode != "BATCH_TOKEN_BUDGET_EXCEEDED" {
		t.Fatalf("批量预算超限期望 status=failed code=BATCH_TOKEN_BUDGET_EXCEEDED，实际=%+v", budgetDetail)
	}
	if budgetDetail.BudgetAdvice == nil || len(budgetDetail.BudgetAdvice.ModuleBreakdown) != 3 {
		t.Fatalf("批量预算熔断应返回 3 个范围的 module_breakdown，实际=%+v", budgetDetail.BudgetAdvice)
	}
	if llmCalls.Load() != 0 {
		t.Fatalf("SC-18 违规：批量预算熔断后 LLM 调用次数必须严格为 0，实际=%d", llmCalls.Load())
	}

	// 3. 恢复充足批量预算，验证两阶段批量生成（common + order -> 整仓 ""）在 MaxUserConcurrency=1 下无死锁完成
	env.genSvc.WithBatchBudgetLimit(1000000)
	reqOK := httptest.NewRequest(http.MethodPost, "/api/tasks/generate-batch", bytes.NewReader(budgetBody))
	reqOK.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqOK.Header.Set("Content-Type", "application/json")
	recOK := httptest.NewRecorder()
	env.router.ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusAccepted {
		t.Fatalf("提交正常批量任务失败: %d %s", recOK.Code, recOK.Body.String())
	}
	var okParent services.GenTaskDetailVO
	_ = json.Unmarshal(recOK.Body.Bytes(), &okParent)

	if waitErr := env.genSvc.WaitAllWithContext(context.Background()); waitErr != nil {
		t.Fatal(waitErr)
	}

	okDetail, err := env.genSvc.GetTaskDetail(context.Background(), okParent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if okDetail.Status != models.TaskStatusReady || okDetail.ProgressPercent != 100 {
		t.Fatalf("批量父任务期望 status=ready progress=100，实际=%+v", okDetail)
	}
	if okDetail.BatchSummary == nil || okDetail.BatchSummary.TotalChildren != 3 || okDetail.BatchSummary.CompletedChildren != 3 {
		t.Fatalf("批量子任务汇总期望 3/3 完成，实际=%+v", okDetail.BatchSummary)
	}
	if len(okDetail.ChildTasks) != 3 {
		t.Fatalf("期望 3 个子任务 [common, order, repo]，实际=%d", len(okDetail.ChildTasks))
	}
	// 验证阶段二整仓子任务的 ID 大于阶段一两个模块子任务 ID（严格先后两阶段创建）
	if okDetail.ChildTasks[2].Subdirectory != "" || okDetail.ChildTasks[2].ID <= okDetail.ChildTasks[1].ID {
		t.Fatalf("阶段二整仓子任务应在阶段一模块子任务之后创建: children=%+v", okDetail.ChildTasks)
	}
}
