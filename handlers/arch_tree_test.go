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

	// 5. 验证无筛选 GET /api/arch-tree 返回完整树 + 聚合统计 + unassigned_scopes
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
	if len(treeResp.UnassignedScopes) != 1 || treeResp.UnassignedScopes[0].RepoName != "standalone-tool" {
		t.Fatalf("期望 unassigned_scopes 仅含 standalone-tool，实际=%+v", treeResp.UnassignedScopes)
	}
	coreNode := treeResp.Nodes[0]
	// coreNode 下共 4 个范围：order-monorepo 整仓 + order + gateway(挂在父仓所在 subsysCore 下) + settlement-service 整仓
	if coreNode.Stats.ScopeCount != 4 || coreNode.Stats.CoveredScopeCount != 2 ||
		coreNode.Stats.ApprovedDocCount != 1 || coreNode.Stats.DraftDocCount != 1 ||
		coreNode.Stats.DriftedDocCount != 1 || coreNode.Stats.BlockedDocCount != 1 {
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
		viewerTree.Nodes[0].Stats.CoveredScopeCount != 1 {
		t.Fatalf("普通用户架构树视图泄露了草稿统计: %+v", viewerTree.Nodes[0].Stats)
	}

	// 7. 验证带筛选条件时自动剪枝空架构节点（如 status=drifted 应剪除 empty-subsys）
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
	if driftedTree.Nodes[0].Stats.ScopeCount != 1 || len(driftedTree.UnassignedScopes) != 0 {
		t.Fatalf("筛选 status=drifted 期望仅剩 order 1 个范围，实际 stats=%+v unassigned=%d",
			driftedTree.Nodes[0].Stats, len(driftedTree.UnassignedScopes))
	}
}
