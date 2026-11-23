package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupArchTreeTestEnv(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "arch-tree-test-jwt-secret-key-9876543210"
	config.AppConfig.Auth.StandaloneMode = true

	masterStore := models.NewSharedMasterStore(db)
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

	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    128,
		BatchSize:   1,
		FlushPeriod: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = commonAudit.Close(context.Background())
	})

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, nil, nil)
	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)
	return db, r
}

func TestRepoCoverageArchAssociationAndArchTreeAPI(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "核心交易研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}

	owner := createTestUser(t, db, "arch_owner", "仓负责人", []string{"user"}, &dept.ID)
	subOwner := createTestUser(t, db, "arch_sub_owner", "模块负责人", []string{"user"}, &dept.ID)
	viewer := createTestUser(t, db, "arch_viewer", "普通研发", []string{"user"}, &dept.ID)

	// 1. 构建两棵架构元素分支：
	//    - core-platform (subsystem) -> trade-group (group) -> order-mod (module, 绑定 order-monorepo/order)
	//                                                        -> repo-direct-mod (module, 绑定 direct-repo 整仓)
	//    - empty-subsystem (subsystem, 无任何绑定仓库)
	subsysCore := models.ArchitectureElement{
		Identifier: "core-platform",
		NameCn:     "核心平台",
		NameEn:     "Core Platform",
		Type:       "subsystem",
		OwnerID:    &owner.ID,
	}
	if err := db.Create(&subsysCore).Error; err != nil {
		t.Fatal(err)
	}

	groupTrade := models.ArchitectureElement{
		Identifier: "trade-group",
		NameCn:     "交易域组",
		NameEn:     "Trade Group",
		Type:       "group",
		ParentID:   &subsysCore.ID,
		OwnerID:    &owner.ID,
	}
	if err := db.Create(&groupTrade).Error; err != nil {
		t.Fatal(err)
	}

	emptySubsys := models.ArchitectureElement{
		Identifier: "empty-subsys",
		NameCn:     "独立闲置子系统",
		NameEn:     "Empty Subsystem",
		Type:       "subsystem",
	}
	if err := db.Create(&emptySubsys).Error; err != nil {
		t.Fatal(err)
	}

	// 仓库 A：通过 ServiceGroup 归属 core-platform，且有 1 个已绑定子目录 order 与 1 个自动发现未绑定子目录 gateway
	repoA := models.Repository{
		DepartmentID: dept.ID,
		Name:         "order-monorepo",
		URL:          "git@git.internal:core/order-monorepo.git",
		OwnerID:      owner.ID,
		Branch:       "master",
		ServiceGroup: "core-platform",
		IsActive:     true,
	}
	if err := db.Create(&repoA).Error; err != nil {
		t.Fatal(err)
	}
	// 仓库 B：整仓直绑到 groupTrade 下的 module 节点（subdir=""）
	repoB := models.Repository{
		DepartmentID: dept.ID,
		Name:         "settlement-service",
		URL:          "git@git.internal:core/settlement.git",
		OwnerID:      owner.ID,
		Branch:       "main",
		IsActive:     true,
	}
	if err := db.Create(&repoB).Error; err != nil {
		t.Fatal(err)
	}
	// 仓库 C：完全未关联任何架构元素
	repoC := models.Repository{
		DepartmentID: dept.ID,
		Name:         "standalone-tool",
		URL:          "git@git.internal:tools/standalone-tool.git",
		OwnerID:      owner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	if err := db.Create(&repoC).Error; err != nil {
		t.Fatal(err)
	}

	modOrder := models.ArchitectureElement{
		Identifier:   "order-mod",
		NameCn:       "订单撮合",
		NameEn:       "Order Engine",
		Type:         "module",
		ParentID:     &groupTrade.ID,
		OwnerID:      &subOwner.ID,
		RepoID:       &repoA.ID,
		Subdirectory: "order",
	}
	modDirectRepo := models.ArchitectureElement{
		Identifier:   "settlement-mod",
		NameCn:       "清结算服务",
		NameEn:       "Settlement Service",
		Type:         "module",
		ParentID:     &groupTrade.ID,
		OwnerID:      &owner.ID,
		RepoID:       &repoB.ID,
		Subdirectory: "",
	}
	if err := db.Create(&modOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&modDirectRepo).Error; err != nil {
		t.Fatal(err)
	}

	// 为 repoA 注入派生模块索引（order 为 arch_element，gateway 为 discovered 未关联架构元素）
	now := time.Now()
	if err := db.Create([]models.RepoModule{
		{
			RepoID:         repoA.ID,
			Subdirectory:   "order",
			Source:         models.ModuleSourceArchElement,
			ArchElementID:  &modOrder.ID,
			Status:         models.ModuleStatusActive,
			EffectiveFiles: 12,
			TotalLOC:       1200,
			LastSeenAt:     now,
		},
		{
			RepoID:         repoA.ID,
			Subdirectory:   "gateway",
			Source:         models.ModuleSourceDiscovered,
			Status:         models.ModuleStatusActive,
			EffectiveFiles: 8,
			TotalLOC:       640,
			LastSeenAt:     now,
		},
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 写入测试文档与漂移状态：
	// - repoA/order: 1 篇已发布且有代码漂移
	// - repoB(""): 1 篇未发布阻断草稿（can_approve=false）
	revApproved := &models.DocumentRevision{
		DocumentID:         0,
		RevisionNo:         1,
		SnapshotID:         1,
		CommitSHA:          "1111111111111111111111111111111111111111",
		DocTypeRevisionID:  1,
		Source:             models.RevSourceAIGenerate,
		FullMarkdown:       "# order",
		SectionsJSON:       []byte("[]"),
		VerifierReportJSON: []byte(`{"passed":true,"can_approve":true}`),
		CreatedBy:          owner.ID,
	}
	docOrder := &models.Document{
		RepoID:            repoA.ID,
		Subdirectory:      "order",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: 1,
		Title:             "订单撮合架构",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 1,
		PublishedAt:       &now,
	}
	if err := db.Create(docOrder).Error; err != nil {
		t.Fatal(err)
	}
	revApproved.DocumentID = docOrder.ID
	if err := db.Create(revApproved).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(docOrder).Updates(map[string]any{
		"current_revision_id":   revApproved.ID,
		"published_revision_id": revApproved.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DocumentDrift{
		DocumentID:        docOrder.ID,
		DriftType:         models.DriftTypeCode,
		HasDrift:          true,
		Precision:         models.DriftPrecisionSectionLevel,
		AffectedSlotCount: 2,
		AffectedSlotsJSON: []byte("[]"),
		DetectedAt:        now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	docSettlementDraft := &models.Document{
		RepoID:            repoB.ID,
		Subdirectory:      "",
		RefName:           "main",
		DocTypeSlug:       "code-walkthrough",
		DocTypeRevisionID: 1,
		Title:             "清结算导读草稿",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(docSettlementDraft).Error; err != nil {
		t.Fatal(err)
	}
	revDraft := &models.DocumentRevision{
		DocumentID:         docSettlementDraft.ID,
		RevisionNo:         1,
		SnapshotID:         2,
		CommitSHA:          "2222222222222222222222222222222222222222",
		DocTypeRevisionID:  1,
		Source:             models.RevSourceAIGenerate,
		FullMarkdown:       "# draft",
		SectionsJSON:       []byte("[]"),
		VerifierReportJSON: []byte(`{"passed":false,"can_approve":false,"blocker_count":1}`),
		CreatedBy:          owner.ID,
	}
	if err := db.Create(revDraft).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(docSettlementDraft).Update("current_revision_id", revDraft.ID).Error; err != nil {
		t.Fatal(err)
	}

	ownerTok := issueTestToken(t, owner)
	viewerTok := issueTestToken(t, viewer)

	// 2. 校验 GET /api/repos 返回整仓架构关联、层级路径、subsystems 列表与筛选能力
	reqRepos := httptest.NewRequest(http.MethodGet, "/api/repos?page=1&pageSize=25", nil)
	reqRepos.Header.Set("Authorization", "Bearer "+ownerTok)
	recRepos := httptest.NewRecorder()
	router.ServeHTTP(recRepos, reqRepos)
	if recRepos.Code != http.StatusOK {
		t.Fatalf("GET /api/repos 失败: %d %s", recRepos.Code, recRepos.Body.String())
	}
	var reposResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recRepos.Body.Bytes(), &reposResp); err != nil {
		t.Fatal(err)
	}
	if len(reposResp.Subsystems) != 2 {
		t.Fatalf("期望返回 2 个子系统筛选项，实际=%d (%+v)", len(reposResp.Subsystems), reposResp.Subsystems)
	}

	rowByKey := make(map[string]services.RepoScopeCoverageVO)
	for _, it := range reposResp.Items {
		rowByKey[fmt.Sprintf("%s:%s", it.RepoName, it.Subdirectory)] = it
	}
	// repoA 整仓应通过 ServiceGroup 关联到 core-platform
	if rowByKey["order-monorepo:"].ArchElementID == nil || *rowByKey["order-monorepo:"].ArchElementID != subsysCore.ID {
		t.Fatalf("order-monorepo 整仓应关联到 subsysCore，实际=%+v", rowByKey["order-monorepo:"])
	}
	// repoB 整仓应直绑到 settlement-mod，且 arch_path 含完整层级
	repoBRow := rowByKey["settlement-service:"]
	if repoBRow.ArchElementID == nil || *repoBRow.ArchElementID != modDirectRepo.ID ||
		repoBRow.ArchElementType != "module" ||
		repoBRow.ArchPath != "核心平台 / 交易域组 / 清结算服务" ||
		repoBRow.SubsystemID == nil || *repoBRow.SubsystemID != subsysCore.ID {
		t.Fatalf("settlement-service 整仓架构元数据异常: %+v", repoBRow)
	}
	// repoA/gateway 属于 discovered 未绑定子目录，arch_element_id 为 nil，但继承 subsystem_id=subsysCore.ID
	gwRow := rowByKey["order-monorepo:gateway"]
	if gwRow.ArchElementID != nil || gwRow.SubsystemID == nil || *gwRow.SubsystemID != subsysCore.ID {
		t.Fatalf("order-monorepo:gateway 元数据异常: %+v", gwRow)
	}
	// repoC 完全未关联
	if rowByKey["standalone-tool:"].ArchElementID != nil || rowByKey["standalone-tool:"].SubsystemID != nil {
		t.Fatalf("standalone-tool 不应关联架构元素: %+v", rowByKey["standalone-tool:"])
	}

	// 3. 验证 subsystem_id 与 arch_binding=unbound 筛选
	reqUnbound := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/api/repos?subsystem_id=%d&arch_binding=unbound", subsysCore.ID),
		nil,
	)
	reqUnbound.Header.Set("Authorization", "Bearer "+ownerTok)
	recUnbound := httptest.NewRecorder()
	router.ServeHTTP(recUnbound, reqUnbound)
	if recUnbound.Code != http.StatusOK {
		t.Fatalf("筛选未绑定架构元素失败: %d %s", recUnbound.Code, recUnbound.Body.String())
	}
	var unboundResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recUnbound.Body.Bytes(), &unboundResp); err != nil {
		t.Fatal(err)
	}
	if len(unboundResp.Items) != 1 || unboundResp.Items[0].Subdirectory != "gateway" {
		t.Fatalf("期望仅命中 order-monorepo/gateway，实际=%+v", unboundResp.Items)
	}

	// 4. 验证非法 query 参数返回 400 INVALID_PARAM
	reqBad := httptest.NewRequest(http.MethodGet, "/api/arch-tree?arch_binding=unknown", nil)
	reqBad.Header.Set("Authorization", "Bearer "+ownerTok)
	recBad := httptest.NewRecorder()
	router.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("非法 arch_binding 期望返回 400，实际=%d", recBad.Code)
	}

	// 5. 验证无筛选 GET /api/arch-tree 返回完整树 + 模块级聚合统计（unassigned_scopes 固定为 []）
	reqTree := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	reqTree.Header.Set("Authorization", "Bearer "+ownerTok)
	recTree := httptest.NewRecorder()
	router.ServeHTTP(recTree, reqTree)
	if recTree.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree 失败: %d %s", recTree.Code, recTree.Body.String())
	}
	var treeResp services.ArchTreeResponse
	if err := json.Unmarshal(recTree.Body.Bytes(), &treeResp); err != nil {
		t.Fatal(err)
	}
	if len(treeResp.Nodes) != 2 {
		t.Fatalf("无筛选时期望 2 个根节点（含闲置子系统），实际=%d", len(treeResp.Nodes))
	}
	if treeResp.UnassignedScopes == nil || len(treeResp.UnassignedScopes) != 0 {
		t.Fatalf("ADR-09 期望 unassigned_scopes 固定为非 nil 空数组 []，实际=%+v", treeResp.UnassignedScopes)
	}
	if treeResp.Summary.TotalModules != 2 || treeResp.Summary.BoundModules != 2 ||
		treeResp.Summary.CoveredModules != 2 || treeResp.Summary.ApprovedDocs != 1 ||
		treeResp.Summary.DraftDocs != 1 || treeResp.Summary.DriftedDocs != 1 ||
		treeResp.Summary.UnassignedRepoCount != 1 {
		t.Fatalf("ArchTreeSummaryVO 不符预期: %+v", treeResp.Summary)
	}
	if len(treeResp.Subsystems) != 2 {
		t.Fatalf("期望返回 2 个子系统选项，实际=%d", len(treeResp.Subsystems))
	}
	for _, sub := range treeResp.Subsystems {
		if sub.ID == subsysCore.ID && (sub.ModuleCount != 2 || sub.ScopeCount != 2) {
			t.Fatalf("subsysCore 选项计数异常: %+v", sub)
		}
		if sub.ID == emptySubsys.ID && (sub.ModuleCount != 0 || sub.ScopeCount != 0) {
			t.Fatalf("emptySubsys 选项计数异常: %+v", sub)
		}
	}
	coreNode := treeResp.Nodes[0]
	// coreNode 下仅聚合功能模块：order-mod + settlement-mod（共 2 个模块，非功能模块零挂仓）
	if len(coreNode.Scopes) != 0 || coreNode.Scope != nil {
		t.Fatalf("subsystem 节点严禁挂载代码仓: scopes=%d scope=%+v", len(coreNode.Scopes), coreNode.Scope)
	}
	if coreNode.Stats.ModuleCount != 2 || coreNode.Stats.BoundModuleCount != 2 ||
		coreNode.Stats.CoveredModuleCount != 2 || coreNode.Stats.ApprovedDocCount != 1 ||
		coreNode.Stats.DraftDocCount != 1 || coreNode.Stats.DriftedDocCount != 1 ||
		coreNode.Stats.BlockedDocCount != 1 {
		t.Fatalf("核心平台节点向上聚合统计不符预期: %+v", coreNode.Stats)
	}

	// 6. 验证普通用户在 /api/arch-tree 下严禁看到草稿统计与草稿详情
	reqViewerTree := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	reqViewerTree.Header.Set("Authorization", "Bearer "+viewerTok)
	recViewerTree := httptest.NewRecorder()
	router.ServeHTTP(recViewerTree, reqViewerTree)
	if recViewerTree.Code != http.StatusOK {
		t.Fatalf("普通用户 GET /api/arch-tree 失败: %d", recViewerTree.Code)
	}
	var viewerTree services.ArchTreeResponse
	if err := json.Unmarshal(recViewerTree.Body.Bytes(), &viewerTree); err != nil {
		t.Fatal(err)
	}
	if viewerTree.Nodes[0].Stats.DraftDocCount != 0 || viewerTree.Nodes[0].Stats.BlockedDocCount != 0 ||
		viewerTree.Nodes[0].Stats.CoveredModuleCount != 1 ||
		viewerTree.Summary.DraftDocs != 0 || viewerTree.Summary.CoveredModules != 1 {
		t.Fatalf("普通用户架构树视图泄露了草稿统计: stats=%+v summary=%+v", viewerTree.Nodes[0].Stats, viewerTree.Summary)
	}

	// 7. 验证带筛选条件时自动剪枝空架构节点并先剪枝后聚合（status=drifted 应剪除 empty-subsys）
	reqDriftedTree := httptest.NewRequest(http.MethodGet, "/api/arch-tree?status=drifted", nil)
	reqDriftedTree.Header.Set("Authorization", "Bearer "+ownerTok)
	recDriftedTree := httptest.NewRecorder()
	router.ServeHTTP(recDriftedTree, reqDriftedTree)
	if recDriftedTree.Code != http.StatusOK {
		t.Fatalf("带筛选 GET /api/arch-tree 失败: %d", recDriftedTree.Code)
	}
	var driftedTree services.ArchTreeResponse
	if err := json.Unmarshal(recDriftedTree.Body.Bytes(), &driftedTree); err != nil {
		t.Fatal(err)
	}
	if len(driftedTree.Nodes) != 1 || driftedTree.Nodes[0].ID != subsysCore.ID {
		t.Fatalf("筛选 status=drifted 后应剪除闲置节点，实际=%+v", driftedTree.Nodes)
	}
	if driftedTree.Nodes[0].Stats.ModuleCount != 1 || driftedTree.Summary.TotalModules != 1 ||
		len(driftedTree.UnassignedScopes) != 0 {
		t.Fatalf("筛选 status=drifted 期望仅剩 order 1 个模块，实际 stats=%+v summary=%+v unassigned=%d",
			driftedTree.Nodes[0].Stats, driftedTree.Summary, len(driftedTree.UnassignedScopes))
	}
}

func TestArchTreeNonModuleZeroScopesAndAuxDirIsolation(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "基础架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t01_owner", "负责人", []string{"user"}, &dept.ID)

	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "code-bench",
		URL:          "git@git.internal:infra/code-bench.git",
		OwnerID:      owner.ID,
		Branch:       "main",
		ServiceGroup: "RND_AI",
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	subsys := models.ArchitectureElement{
		Identifier: "RND_AI",
		NameCn:     "AI辅助研发子系统",
		NameEn:     "AI R&D",
		Type:       "subsystem",
		OwnerID:    &owner.ID,
	}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatal(err)
	}
	// group 历史上误填了整仓 RepoID
	group := models.ArchitectureElement{
		Identifier: "BENCH",
		NameCn:     "门户功能组",
		NameEn:     "Bench Group",
		Type:       "group",
		ParentID:   &subsys.ID,
		OwnerID:    &owner.ID,
		RepoID:     &repo.ID,
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	modFront := models.ArchitectureElement{
		Identifier:   "BENCH_FRONT",
		NameCn:       "门户前端",
		NameEn:       "Bench Frontend",
		Type:         "module",
		ParentID:     &group.ID,
		OwnerID:      &owner.ID,
		RepoID:       &repo.ID,
		Subdirectory: "frontend",
	}
	if err := db.Create(&modFront).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := db.Create([]models.RepoModule{
		{RepoID: repo.ID, Subdirectory: "frontend", Source: models.ModuleSourceArchElement, ArchElementID: &modFront.ID, Status: models.ModuleStatusActive, EffectiveFiles: 41, TotalLOC: 14866, LastSeenAt: now},
		{RepoID: repo.ID, Subdirectory: "models", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusActive, EffectiveFiles: 3, TotalLOC: 542, LastSeenAt: now},
		{RepoID: repo.ID, Subdirectory: "templates", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusActive, EffectiveFiles: 3, TotalLOC: 9, LastSeenAt: now},
	}).Error; err != nil {
		t.Fatal(err)
	}

	tok := issueTestToken(t, owner)
	req := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree 失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp services.ArchTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.UnassignedScopes) != 0 {
		t.Fatalf("期望 unassigned_scopes 固定为 [], 实际=%+v", resp.UnassignedScopes)
	}
	if len(resp.Nodes) != 1 || len(resp.Nodes[0].Children) != 1 {
		t.Fatalf("树结构不符预期: %+v", resp.Nodes)
	}
	subNode := resp.Nodes[0]
	grpNode := subNode.Children[0]
	for _, nonLeaf := range []*services.ArchTreeNodeVO{subNode, grpNode} {
		if len(nonLeaf.Scopes) != 0 || nonLeaf.Scope != nil || nonLeaf.BindingStatus != "" {
			t.Fatalf("非功能模块 %s 不应挂载代码仓: scopes=%d scope=%+v binding=%q", nonLeaf.Identifier, len(nonLeaf.Scopes), nonLeaf.Scope, nonLeaf.BindingStatus)
		}
	}
	if grpNode.Stats.ModuleCount != 1 || resp.Summary.TotalModules != 1 || resp.Summary.UnassignedRepoCount != 0 {
		t.Fatalf("辅助目录 models/templates 不应混入功能组计数: groupStats=%+v summary=%+v", grpNode.Stats, resp.Summary)
	}
}

func TestArchTreeThreeBindingStatesAndFallbackScope(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "平台工程部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	repoOwner := createTestUser(t, db, "t02_repo_owner", "仓主", []string{"user"}, &dept.ID)
	subOwner := createTestUser(t, db, "t02_sub_owner", "子目录主", []string{"user"}, &dept.ID)
	otherUser := createTestUser(t, db, "t02_other", "路人", []string{"user"}, &dept.ID)

	activeMono := models.Repository{
		DepartmentID: dept.ID, Name: "mono-app", URL: "git@git.internal:p/mono.git",
		OwnerID: repoOwner.ID, Branch: "main", IsActive: true,
	}
	activeWhole := models.Repository{
		DepartmentID: dept.ID, Name: "whole-svc", URL: "git@git.internal:p/whole.git",
		OwnerID: repoOwner.ID, Branch: "master", IsActive: true,
	}
	inactiveRepo := models.Repository{
		DepartmentID: dept.ID, Name: "legacy-repo", URL: "git@git.internal:p/legacy.git",
		OwnerID: repoOwner.ID, Branch: "master", IsActive: true,
	}
	for _, r := range []*models.Repository{&activeMono, &activeWhole, &inactiveRepo} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&inactiveRepo).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	subsys := models.ArchitectureElement{Identifier: "PLAT", NameCn: "平台域", Type: "subsystem", OwnerID: &repoOwner.ID}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatal(err)
	}

	// 1) ignored 目录仍绑定 module（需实时兜底构造）
	// 2) 未在 repo_modules 扫描登记的新子目录 module（需实时兜底构造）
	// 3) 整仓绑定 module（即使设置了子目录 owner，写权限仍严格认仓库级权限）
	// 4) unbound module (repo_id == nil)
	// 5) repo_inactive module (is_active == false)
	modIgnored := models.ArchitectureElement{
		Identifier: "MOD_IGNORED", NameCn: "被忽略目录模块", Type: "module",
		ParentID: &subsys.ID, OwnerID: &subOwner.ID, RepoID: &activeMono.ID, Subdirectory: "ignored_dir",
	}
	modUnscanned := models.ArchitectureElement{
		Identifier: "MOD_UNSCANNED", NameCn: "未扫描目录模块", Type: "module",
		ParentID: &subsys.ID, OwnerID: &subOwner.ID, RepoID: &activeMono.ID, Subdirectory: "new_dir",
	}
	modWhole := models.ArchitectureElement{
		Identifier: "MOD_WHOLE", NameCn: "整仓模块", Type: "module",
		ParentID: &subsys.ID, OwnerID: &subOwner.ID, RepoID: &activeWhole.ID, Subdirectory: "",
	}
	modUnbound := models.ArchitectureElement{
		Identifier: "MOD_UNBOUND", NameCn: "待建仓模块", Type: "module",
		ParentID: &subsys.ID, OwnerID: &otherUser.ID, RepoID: nil, Subdirectory: "",
	}
	modInactive := models.ArchitectureElement{
		Identifier: "MOD_INACTIVE", NameCn: "停用仓模块", Type: "module",
		ParentID: &subsys.ID, OwnerID: &repoOwner.ID, RepoID: &inactiveRepo.ID, Subdirectory: "",
	}
	for _, m := range []*models.ArchitectureElement{&modIgnored, &modUnscanned, &modWhole, &modUnbound, &modInactive} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	if err := db.Create([]models.RepoModule{
		{RepoID: activeMono.ID, Subdirectory: "ignored_dir", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusIgnored, EffectiveFiles: 5, TotalLOC: 120, LastSeenAt: now},
		{RepoID: activeMono.ID, Subdirectory: "other_dir", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusActive, EffectiveFiles: 2, TotalLOC: 40, LastSeenAt: now},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RepoModulePolicy{
		RepoID: activeMono.ID, TargetBranch: "develop", Strategy: models.ModuleStrategyMerged, MinEffectiveFiles: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 为 ignored_dir 与 new_dir 写入文档（1 个已发布、1 个草稿），以及快照
	if err := db.Create(&models.RepoSnapshot{
		RepoID: activeMono.ID, Subdirectory: "new_dir", RefName: "develop", CommitSHA: "abcdef1234567890abcdef1234567890abcdef12",
		IRSchemaVersion: "v1", Status: "ready", EffectiveFiles: 9, TotalLOC: 450, StatsJSON: []byte("{}"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	docIgnored := &models.Document{
		RepoID: activeMono.ID, Subdirectory: "ignored_dir", RefName: "develop",
		DocTypeSlug: "system-architecture", DocTypeRevisionID: 1, Title: "ignored doc",
		Status: models.DocStatusDraft, CurrentRevisionNo: 1,
	}
	if err := db.Create(docIgnored).Error; err != nil {
		t.Fatal(err)
	}
	docNewDir := &models.Document{
		RepoID: activeMono.ID, Subdirectory: "new_dir", RefName: "develop",
		DocTypeSlug: "code-walkthrough", DocTypeRevisionID: 1, Title: "new dir doc",
		Status: models.DocStatusApproved, CurrentRevisionNo: 1, PublishedAt: &now,
	}
	if err := db.Create(docNewDir).Error; err != nil {
		t.Fatal(err)
	}
	revNew := &models.DocumentRevision{
		DocumentID: docNewDir.ID, RevisionNo: 1, SnapshotID: 1, CommitSHA: "abcdef1234567890abcdef1234567890abcdef12",
		DocTypeRevisionID: 1, Source: models.RevSourceAIGenerate, FullMarkdown: "# ok", SectionsJSON: []byte("[]"),
		VerifierReportJSON: []byte(`{"passed":true,"can_approve":true}`), CreatedBy: repoOwner.ID,
	}
	if err := db.Create(revNew).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(docNewDir).Updates(map[string]any{"current_revision_id": revNew.ID, "published_revision_id": revNew.ID}).Error; err != nil {
		t.Fatal(err)
	}

	// 子目录负责人 subOwner 查询：对 ignored_dir/new_dir 具有子目录写权限，但对 whole-svc 整仓不可越权写
	tokSub := issueTestToken(t, subOwner)
	req := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	req.Header.Set("Authorization", "Bearer "+tokSub)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree 失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp services.ArchTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.TotalModules != 5 || resp.Summary.BoundModules != 3 || resp.Summary.CoveredModules != 2 {
		t.Fatalf("三态与兜底统计不符预期: %+v", resp.Summary)
	}

	byIdent := make(map[string]*services.ArchTreeNodeVO)
	for _, ch := range resp.Nodes[0].Children {
		byIdent[ch.Identifier] = ch
	}

	ignNode := byIdent["MOD_IGNORED"]
	if ignNode.BindingStatus != services.ArchBindingStatusBound || ignNode.Scope == nil {
		t.Fatalf("ignored_dir 绑定模块必须兜底构造 scope: %+v", ignNode)
	}
	if !ignNode.Scope.CanWrite || len(ignNode.Scope.DocStatuses) != 1 || ignNode.Scope.Branch != "develop" || !ignNode.Scope.IsBranchOverridden {
		t.Fatalf("MOD_IGNORED 兜底 scope 字段异常: %+v", ignNode.Scope)
	}

	newNode := byIdent["MOD_UNSCANNED"]
	if newNode.BindingStatus != services.ArchBindingStatusBound || newNode.Scope == nil {
		t.Fatalf("未扫描绑定目录必须兜底构造 scope: %+v", newNode)
	}
	if newNode.Scope.EffectiveFiles != 9 || newNode.Scope.LatestCommitSHA == "" || !newNode.Scope.CanWrite {
		t.Fatalf("MOD_UNSCANNED 兜底 scope 缺快照或权限: %+v", newNode.Scope)
	}

	wholeNode := byIdent["MOD_WHOLE"]
	if wholeNode.BindingStatus != services.ArchBindingStatusBound || wholeNode.Scope == nil || wholeNode.Scope.CanWrite {
		t.Fatalf("整仓模块对非仓主 subOwner 不应授予 can_write: %+v", wholeNode.Scope)
	}

	unboundNode := byIdent["MOD_UNBOUND"]
	if unboundNode.BindingStatus != services.ArchBindingStatusUnbound || unboundNode.Scope != nil {
		t.Fatalf("MOD_UNBOUND 状态异常: %+v", unboundNode)
	}

	inactiveNode := byIdent["MOD_INACTIVE"]
	if inactiveNode.BindingStatus != services.ArchBindingStatusRepoInactive || inactiveNode.Scope != nil {
		t.Fatalf("MOD_INACTIVE 状态异常: %+v", inactiveNode)
	}
}

func TestArchTreePruneBeforeAggregateAndCrossDept(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	deptA := models.Department{Name: "部门A"}
	deptB := models.Department{Name: "部门B"}
	if err := db.Create(&deptA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deptB).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t03_owner", "负责人", []string{"user"}, &deptA.ID)

	repoDeptA := models.Repository{DepartmentID: deptA.ID, Name: "svc-a", URL: "git@git.internal:a/svc-a.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	repoDeptB := models.Repository{DepartmentID: deptB.ID, Name: "svc-b", URL: "git@git.internal:b/svc-b.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	repoUnassignedDeptB := models.Repository{DepartmentID: deptB.ID, Name: "orphan-b", URL: "git@git.internal:b/orphan-b.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	for _, r := range []*models.Repository{&repoDeptA, &repoDeptB, &repoUnassignedDeptB} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	subsys := models.ArchitectureElement{Identifier: "CROSS", NameCn: "跨部门子系统", Type: "subsystem", OwnerID: &owner.ID}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatal(err)
	}
	modA := models.ArchitectureElement{Identifier: "MOD_A", NameCn: "模块A", Type: "module", ParentID: &subsys.ID, RepoID: &repoDeptA.ID}
	modB := models.ArchitectureElement{Identifier: "MOD_B", NameCn: "模块B", Type: "module", ParentID: &subsys.ID, RepoID: &repoDeptB.ID}
	modGap := models.ArchitectureElement{Identifier: "MOD_GAP", NameCn: "未绑模块", Type: "module", ParentID: &subsys.ID, RepoID: nil}
	for _, m := range []*models.ArchitectureElement{&modA, &modB, &modGap} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	docA := &models.Document{RepoID: repoDeptA.ID, Subdirectory: "", RefName: "main", DocTypeSlug: "system-architecture", DocTypeRevisionID: 1, Title: "A", Status: models.DocStatusApproved, CurrentRevisionNo: 1, PublishedAt: &now}
	if err := db.Create(docA).Error; err != nil {
		t.Fatal(err)
	}
	revA := &models.DocumentRevision{DocumentID: docA.ID, RevisionNo: 1, SnapshotID: 1, CommitSHA: "3333333333333333333333333333333333333333", DocTypeRevisionID: 1, Source: models.RevSourceAIGenerate, FullMarkdown: "# A", SectionsJSON: []byte("[]"), VerifierReportJSON: []byte(`{"passed":true,"can_approve":true}`), CreatedBy: owner.ID}
	if err := db.Create(revA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(docA).Updates(map[string]any{"current_revision_id": revA.ID, "published_revision_id": revA.ID}).Error; err != nil {
		t.Fatal(err)
	}

	tok := issueTestToken(t, owner)

	// 1. 按 department_id=deptA 筛选：仅保留 MOD_A，summary.unassigned_repo_count 仍保持全量 1（repoUnassignedDeptB），subsystems[0].module_count 仍为全树 3
	reqDept := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/arch-tree?department_id=%d", deptA.ID), nil)
	reqDept.Header.Set("Authorization", "Bearer "+tok)
	recDept := httptest.NewRecorder()
	router.ServeHTTP(recDept, reqDept)
	if recDept.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree?department_id 失败: %d %s", recDept.Code, recDept.Body.String())
	}
	var respDept services.ArchTreeResponse
	if err := json.Unmarshal(recDept.Body.Bytes(), &respDept); err != nil {
		t.Fatal(err)
	}
	if respDept.Summary.TotalModules != 1 || respDept.Summary.BoundModules != 1 ||
		respDept.Summary.UnassignedRepoCount != 1 ||
		respDept.Nodes[0].Stats.ModuleCount != 1 ||
		respDept.Subsystems[0].ModuleCount != 3 {
		t.Fatalf("跨部门筛选与先剪枝后聚合口径异常: summary=%+v stats=%+v subsys=%+v",
			respDept.Summary, respDept.Nodes[0].Stats, respDept.Subsystems)
	}

	// 2. 按 department_id=deptB 筛选：MOD_B 必须仍为 bound（不误判为 repo_inactive）
	reqDeptB := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/arch-tree?department_id=%d", deptB.ID), nil)
	reqDeptB.Header.Set("Authorization", "Bearer "+tok)
	recDeptB := httptest.NewRecorder()
	router.ServeHTTP(recDeptB, reqDeptB)
	var respDeptB services.ArchTreeResponse
	if err := json.Unmarshal(recDeptB.Body.Bytes(), &respDeptB); err != nil {
		t.Fatal(err)
	}
	if len(respDeptB.Nodes) != 1 || len(respDeptB.Nodes[0].Children) != 1 ||
		respDeptB.Nodes[0].Children[0].Identifier != "MOD_B" ||
		respDeptB.Nodes[0].Children[0].BindingStatus != services.ArchBindingStatusBound {
		t.Fatalf("部门B 模块被误剪或误判状态: %+v", respDeptB.Nodes)
	}

	// 3. status=approved 筛选：仅保留 MOD_A，MOD_GAP 与 MOD_B 均被剪除
	reqApp := httptest.NewRequest(http.MethodGet, "/api/arch-tree?status=approved", nil)
	reqApp.Header.Set("Authorization", "Bearer "+tok)
	recApp := httptest.NewRecorder()
	router.ServeHTTP(recApp, reqApp)
	var respApp services.ArchTreeResponse
	if err := json.Unmarshal(recApp.Body.Bytes(), &respApp); err != nil {
		t.Fatal(err)
	}
	if respApp.Summary.TotalModules != 1 || respApp.Nodes[0].Stats.ModuleCount != 1 || len(respApp.Nodes[0].Children) != 1 {
		t.Fatalf("status=approved 剪枝后计数异常: %+v", respApp)
	}
}

func TestArchTreeViewerDraftIsolationIncludingFallback(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "风控部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t04_owner", "仓主", []string{"user"}, &dept.ID)
	viewer := createTestUser(t, db, "t04_viewer", "普通用户", []string{"user"}, &dept.ID)

	repo := models.Repository{DepartmentID: dept.ID, Name: "risk-repo", URL: "git@git.internal:r/risk.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	subsys := models.ArchitectureElement{Identifier: "RISK", NameCn: "风控域", Type: "subsystem", OwnerID: &owner.ID}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatal(err)
	}
	modNormal := models.ArchitectureElement{Identifier: "RISK_CORE", NameCn: "风控引擎", Type: "module", ParentID: &subsys.ID, OwnerID: &owner.ID, RepoID: &repo.ID, Subdirectory: "core"}
	modFallback := models.ArchitectureElement{Identifier: "RISK_IGN", NameCn: "兜底模块", Type: "module", ParentID: &subsys.ID, OwnerID: &owner.ID, RepoID: &repo.ID, Subdirectory: "ignored_sub"}
	if err := db.Create(&modNormal).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&modFallback).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := db.Create(&models.RepoModule{
		RepoID: repo.ID, Subdirectory: "ignored_sub", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusIgnored, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 两个模块各自仅有一篇未发布且阻断的草稿
	for _, sub := range []string{"core", "ignored_sub"} {
		doc := &models.Document{
			RepoID: repo.ID, Subdirectory: sub, RefName: "main",
			DocTypeSlug: "system-architecture", DocTypeRevisionID: 1, Title: "draft-" + sub,
			Status: models.DocStatusDraft, CurrentRevisionNo: 1,
		}
		if err := db.Create(doc).Error; err != nil {
			t.Fatal(err)
		}
		rev := &models.DocumentRevision{
			DocumentID: doc.ID, RevisionNo: 1, SnapshotID: 1, CommitSHA: "4444444444444444444444444444444444444444",
			DocTypeRevisionID: 1, Source: models.RevSourceAIGenerate, FullMarkdown: "# secret draft", SectionsJSON: []byte("[]"),
			VerifierReportJSON: []byte(`{"passed":false,"can_approve":false,"blocker_count":1}`), CreatedBy: owner.ID,
		}
		if err := db.Create(rev).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(doc).Update("current_revision_id", rev.ID).Error; err != nil {
			t.Fatal(err)
		}
	}

	viewerTok := issueTestToken(t, viewer)
	req := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	req.Header.Set("Authorization", "Bearer "+viewerTok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree 失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp services.ArchTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.DraftDocs != 0 || resp.Summary.CoveredModules != 0 ||
		resp.Nodes[0].Stats.DraftDocCount != 0 || resp.Nodes[0].Stats.BlockedDocCount != 0 ||
		resp.Nodes[0].Stats.CoveredModuleCount != 0 {
		t.Fatalf("普通用户草稿隔离红线被击穿: summary=%+v stats=%+v", resp.Summary, resp.Nodes[0].Stats)
	}
	for _, ch := range resp.Nodes[0].Children {
		if ch.Scope == nil || len(ch.Scope.DocStatuses) != 0 {
			t.Fatalf("模块 %s 泄露了草稿 doc_statuses: %+v", ch.Identifier, ch.Scope)
		}
	}
}

func TestArchTreeBindingFilterNonLeafSearchAndInvalidParam(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "研发效能部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t05_owner", "负责人", []string{"user"}, &dept.ID)
	repo := models.Repository{DepartmentID: dept.ID, Name: "eff-repo", URL: "git@git.internal:e/eff.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	inactive := models.Repository{DepartmentID: dept.ID, Name: "old-repo", URL: "git@git.internal:e/old.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&inactive).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&inactive).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	sub := models.ArchitectureElement{Identifier: "EFF", NameCn: "效能子系统", Type: "subsystem"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	grp := models.ArchitectureElement{Identifier: "PORTAL_GRP", NameCn: "门户功能组", Type: "group", ParentID: &sub.ID}
	emptyGrp := models.ArchitectureElement{Identifier: "EMPTY_PORTAL", NameCn: "空白门户占位组", Type: "group", ParentID: &sub.ID}
	if err := db.Create(&grp).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&emptyGrp).Error; err != nil {
		t.Fatal(err)
	}
	modBound := models.ArchitectureElement{Identifier: "EFF_UI", NameCn: "前端面板", Type: "module", ParentID: &grp.ID, RepoID: &repo.ID}
	modUnbound := models.ArchitectureElement{Identifier: "EFF_API", NameCn: "未绑接口", Type: "module", ParentID: &grp.ID, RepoID: nil}
	modInactive := models.ArchitectureElement{Identifier: "EFF_OLD", NameCn: "停用仓服务", Type: "module", ParentID: &grp.ID, RepoID: &inactive.ID}
	for _, m := range []*models.ArchitectureElement{&modBound, &modUnbound, &modInactive} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	tok := issueTestToken(t, owner)

	// 1. arch_binding=unbound_repo 在 /api/arch-tree 返回 400 INVALID_PARAM
	reqBad := httptest.NewRequest(http.MethodGet, "/api/arch-tree?arch_binding=unbound_repo", nil)
	reqBad.Header.Set("Authorization", "Bearer "+tok)
	recBad := httptest.NewRecorder()
	router.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("/api/arch-tree 传入 unbound_repo 期望 400，实际=%d %s", recBad.Code, recBad.Body.String())
	}
	var errBody struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(recBad.Body.Bytes(), &errBody)
	if errBody.Code != handlers.ErrCodeInvalidParam {
		t.Fatalf("期望错误码 INVALID_PARAM，实际=%q", errBody.Code)
	}

	// 2. arch_binding=unbound 仅返 unbound + repo_inactive 缺口模块
	reqUnbound := httptest.NewRequest(http.MethodGet, "/api/arch-tree?arch_binding=unbound", nil)
	reqUnbound.Header.Set("Authorization", "Bearer "+tok)
	recUnbound := httptest.NewRecorder()
	router.ServeHTTP(recUnbound, reqUnbound)
	var respUnbound services.ArchTreeResponse
	if err := json.Unmarshal(recUnbound.Body.Bytes(), &respUnbound); err != nil {
		t.Fatal(err)
	}
	if respUnbound.Summary.TotalModules != 2 || respUnbound.Summary.BoundModules != 0 {
		t.Fatalf("arch_binding=unbound 期望仅保留 2 个缺口模块: %+v", respUnbound.Summary)
	}

	// 3. arch_binding=bound 仅返 bound 模块
	reqBound := httptest.NewRequest(http.MethodGet, "/api/arch-tree?arch_binding=bound", nil)
	reqBound.Header.Set("Authorization", "Bearer "+tok)
	recBound := httptest.NewRecorder()
	router.ServeHTTP(recBound, reqBound)
	var respBound services.ArchTreeResponse
	if err := json.Unmarshal(recBound.Body.Bytes(), &respBound); err != nil {
		t.Fatal(err)
	}
	if respBound.Summary.TotalModules != 1 || respBound.Summary.BoundModules != 1 {
		t.Fatalf("arch_binding=bound 期望仅保留 1 个 bound 模块: %+v", respBound.Summary)
	}

	// 4. search 命中非叶功能组名称时向下继承匹配其子模块，且仅 search 时保留命中搜索的空非叶占位组
	reqSearch := httptest.NewRequest(http.MethodGet, "/api/arch-tree?search=门户", nil)
	reqSearch.Header.Set("Authorization", "Bearer "+tok)
	recSearch := httptest.NewRecorder()
	router.ServeHTTP(recSearch, reqSearch)
	var respSearch services.ArchTreeResponse
	if err := json.Unmarshal(recSearch.Body.Bytes(), &respSearch); err != nil {
		t.Fatal(err)
	}
	if len(respSearch.Nodes) != 1 || len(respSearch.Nodes[0].Children) != 2 || respSearch.Summary.TotalModules != 3 {
		t.Fatalf("search 命中非叶节点应保留 2 个组及其全部 3 个子模块: nodes=%+v summary=%+v", respSearch.Nodes, respSearch.Summary)
	}
}

func TestReposUnboundRepoMECE(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	deptA := models.Department{Name: "交易部门"}
	deptB := models.Department{Name: "基础部门"}
	if err := db.Create(&deptA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deptB).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t06_owner", "负责人", []string{"user"}, &deptA.ID)

	// repoManaged: 有子目录模块绑在 deptB 的子系统，同时存在辅助目录 models/templates
	repoManaged := models.Repository{DepartmentID: deptA.ID, Name: "managed-mono", URL: "git@git.internal:a/mono.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	// repoGroupOnly: 仅设了 ServiceGroup 且被 group 节点引用整仓，但无任何 module 绑定 -> 必须进入 unbound_repo
	repoGroupOnly := models.Repository{DepartmentID: deptA.ID, Name: "group-only-repo", URL: "git@git.internal:a/grp.git", OwnerID: owner.ID, Branch: "main", ServiceGroup: "SUB_B", IsActive: true}
	// repoPureUnbound: 完全未绑定（属于部门 B）
	repoPureUnbound := models.Repository{DepartmentID: deptB.ID, Name: "pure-unbound", URL: "git@git.internal:b/pure.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	for _, r := range []*models.Repository{&repoManaged, &repoGroupOnly, &repoPureUnbound} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	subB := models.ArchitectureElement{Identifier: "SUB_B", NameCn: "子系统B", Type: "subsystem"}
	if err := db.Create(&subB).Error; err != nil {
		t.Fatal(err)
	}
	grpB := models.ArchitectureElement{Identifier: "GRP_B", NameCn: "功能组B", Type: "group", ParentID: &subB.ID, RepoID: &repoGroupOnly.ID}
	if err := db.Create(&grpB).Error; err != nil {
		t.Fatal(err)
	}
	modCore := models.ArchitectureElement{Identifier: "MOD_CORE", NameCn: "核心子模块", Type: "module", ParentID: &grpB.ID, RepoID: &repoManaged.ID, Subdirectory: "core"}
	if err := db.Create(&modCore).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := db.Create([]models.RepoModule{
		{RepoID: repoManaged.ID, Subdirectory: "core", Source: models.ModuleSourceArchElement, ArchElementID: &modCore.ID, Status: models.ModuleStatusActive, LastSeenAt: now},
		{RepoID: repoManaged.ID, Subdirectory: "models", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusActive, LastSeenAt: now},
		{RepoID: repoManaged.ID, Subdirectory: "templates", Source: models.ModuleSourceDiscovered, Status: models.ModuleStatusActive, LastSeenAt: now},
	}).Error; err != nil {
		t.Fatal(err)
	}

	tok := issueTestToken(t, owner)

	// 1. 全量 GET /api/repos?arch_binding=unbound_repo 应排除 managed-mono（含整仓、core、models、templates），仅保留 group-only-repo 与 pure-unbound
	reqUnbound := httptest.NewRequest(http.MethodGet, "/api/repos?arch_binding=unbound_repo&page=1&pageSize=25", nil)
	reqUnbound.Header.Set("Authorization", "Bearer "+tok)
	recUnbound := httptest.NewRecorder()
	router.ServeHTTP(recUnbound, reqUnbound)
	if recUnbound.Code != http.StatusOK {
		t.Fatalf("GET /api/repos?arch_binding=unbound_repo 失败: %d %s", recUnbound.Code, recUnbound.Body.String())
	}
	var reposResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recUnbound.Body.Bytes(), &reposResp); err != nil {
		t.Fatal(err)
	}
	uniqueRepos := make(map[uint]string)
	for _, it := range reposResp.Items {
		uniqueRepos[it.RepoID] = it.RepoName
	}
	if len(uniqueRepos) != 2 || uniqueRepos[repoGroupOnly.ID] == "" || uniqueRepos[repoPureUnbound.ID] == "" {
		t.Fatalf("unbound_repo MECE 集合异常: %+v", uniqueRepos)
	}

	// 2. 与 GET /api/arch-tree summary.unassigned_repo_count 严格对账一致
	reqTree := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	reqTree.Header.Set("Authorization", "Bearer "+tok)
	recTree := httptest.NewRecorder()
	router.ServeHTTP(recTree, reqTree)
	var treeResp services.ArchTreeResponse
	if err := json.Unmarshal(recTree.Body.Bytes(), &treeResp); err != nil {
		t.Fatal(err)
	}
	if treeResp.Summary.UnassignedRepoCount != len(uniqueRepos) {
		t.Fatalf("unassigned_repo_count(%d) 与 unbound_repo 仓库数(%d) 不一致",
			treeResp.Summary.UnassignedRepoCount, len(uniqueRepos))
	}

	// 3. 按部门 A 筛选 unbound_repo 时，managed-mono 仍被全量排除，仅返回 group-only-repo
	reqDeptA := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/repos?arch_binding=unbound_repo&department_id=%d", deptA.ID), nil)
	reqDeptA.Header.Set("Authorization", "Bearer "+tok)
	recDeptA := httptest.NewRecorder()
	router.ServeHTTP(recDeptA, reqDeptA)
	var deptAResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recDeptA.Body.Bytes(), &deptAResp); err != nil {
		t.Fatal(err)
	}
	if len(deptAResp.Items) != 1 || deptAResp.Items[0].RepoID != repoGroupOnly.ID {
		t.Fatalf("跨部门过滤 unbound_repo 异常: %+v", deptAResp.Items)
	}
}

func TestArchTreeArbitraryDepthAndDuplicateScopeModules(t *testing.T) {
	db, router := setupArchTreeTestEnv(t)

	dept := models.Department{Name: "中间件部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := createTestUser(t, db, "t07_owner", "负责人", []string{"user"}, &dept.ID)
	owner2 := createTestUser(t, db, "t07_owner2", "模块二主", []string{"user"}, &dept.ID)

	repo := models.Repository{DepartmentID: dept.ID, Name: "mq-suite", URL: "git@git.internal:m/mq.git", OwnerID: owner.ID, Branch: "main", IsActive: true}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	// 1. 跳过 group：subsystem -> module 直挂
	// 2. 两模块指向同一个 (repo_id, "broker")，需各自保留独立 ArchElement* 与独立计数
	sub := models.ArchitectureElement{Identifier: "MW", NameCn: "中间件子系统", Type: "subsystem", OwnerID: &owner.ID}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	mod1 := models.ArchitectureElement{Identifier: "MQ_PRODUCER", NameCn: "消息生产端", Type: "module", ParentID: &sub.ID, OwnerID: &owner.ID, RepoID: &repo.ID, Subdirectory: "broker"}
	mod2 := models.ArchitectureElement{Identifier: "MQ_CONSUMER", NameCn: "消息消费端", Type: "module", ParentID: &sub.ID, OwnerID: &owner2.ID, RepoID: &repo.ID, Subdirectory: "broker"}
	if err := db.Create(&mod1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mod2).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	doc := &models.Document{RepoID: repo.ID, Subdirectory: "broker", RefName: "main", DocTypeSlug: "system-architecture", DocTypeRevisionID: 1, Title: "broker arch", Status: models.DocStatusApproved, CurrentRevisionNo: 1, PublishedAt: &now}
	if err := db.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	rev := &models.DocumentRevision{DocumentID: doc.ID, RevisionNo: 1, SnapshotID: 1, CommitSHA: "5555555555555555555555555555555555555555", DocTypeRevisionID: 1, Source: models.RevSourceAIGenerate, FullMarkdown: "# broker", SectionsJSON: []byte("[]"), VerifierReportJSON: []byte(`{"passed":true,"can_approve":true}`), CreatedBy: owner.ID}
	if err := db.Create(rev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(doc).Updates(map[string]any{"current_revision_id": rev.ID, "published_revision_id": rev.ID}).Error; err != nil {
		t.Fatal(err)
	}

	tok := issueTestToken(t, owner)
	req := httptest.NewRequest(http.MethodGet, "/api/arch-tree", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/arch-tree 失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp services.ArchTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Summary.TotalModules != 2 || resp.Summary.BoundModules != 2 || resp.Summary.CoveredModules != 2 ||
		resp.Nodes[0].Stats.ModuleCount != 2 || len(resp.Nodes[0].Children) != 2 {
		t.Fatalf("直挂与同 Scope 双模块计数异常: summary=%+v node=%+v", resp.Summary, resp.Nodes[0])
	}

	ch0 := resp.Nodes[0].Children[0]
	ch1 := resp.Nodes[0].Children[1]
	if ch0.Scope == nil || ch1.Scope == nil || ch0.Scope == ch1.Scope {
		t.Fatalf("同 Scope 双模块必须持有独立 Scope 拷贝指针")
	}
	if ch0.Scope.ArchElementID == nil || ch1.Scope.ArchElementID == nil ||
		*ch0.Scope.ArchElementID != mod1.ID || *ch1.Scope.ArchElementID != mod2.ID ||
		ch0.Scope.ArchElementName == ch1.Scope.ArchElementName ||
		ch0.Scope.OwnerName == ch1.Scope.OwnerName {
		t.Fatalf("同 Scope 双模块元数据发生互相污染: ch0=%+v ch1=%+v", ch0.Scope, ch1.Scope)
	}
}
