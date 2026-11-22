package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func setupM1TestEnv(t *testing.T) (*gorm.DB, *gin.Engine, *services.RBACService) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m1-unit-test-jwt-secret-key-9876543210"
	config.AppConfig.Auth.StandaloneMode = true

	reg, err := doctype.NewRegistry(filepath.Join(t.TempDir(), "doc_types"))
	if err != nil {
		t.Fatal(err)
	}

	masterStore := models.NewSharedMasterStore(db)
	dtStore := models.NewDocTypeStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	llmSvc := services.NewLLMConfigService(govStore).WithDocTypeRepo(dtStore)
	docTypeSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := docTypeSvc.SeedAndSyncAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 初始化异步审计写入器（短刷新周期便于测试落库断言）
	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{
		QueueCap:    256,
		BatchSize:   1,
		FlushPeriod: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = commonAudit.Close(context.Background())
	})

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, nil)
	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)
	return db, r, rbacSvc
}

func issueTestToken(t *testing.T, u models.User) string {
	t.Helper()
	tok, err := commonAuth.GenerateToken(
		u.ID,
		u.Username,
		u.Email,
		u.Name,
		u.IsSuperAdmin(),
		u.GetRoles(),
		config.AppConfig.Auth.JWTSecret,
		time.Hour,
	)
	if err != nil {
		t.Fatalf("签发测试 Token 失败: %v", err)
	}
	return tok
}

func createTestUser(t *testing.T, db *gorm.DB, username, name string, roles []string, deptID *uint) models.User {
	t.Helper()
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		t.Fatal(err)
	}
	u := models.User{
		Username:     username,
		Email:        username + "@example.com",
		Name:         name,
		Password:     "hashed-placeholder",
		IsActive:     true,
		Roles:        datatypes.JSON(rolesJSON),
		DepartmentID: deptID,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("创建测试用户 %s 失败: %v", username, err)
	}
	return u
}

func TestHealthAndAuthBoundaries(t *testing.T) {
	db, router, _ := setupM1TestEnv(t)

	// 1. GET /api/health 返回 200 且 service == "Code-Archdoc"
	reqHealth := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	recHealth := httptest.NewRecorder()
	router.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("期望 /api/health 返回 200，实际返回 %d", recHealth.Code)
	}
	var healthResp map[string]any
	if err := json.Unmarshal(recHealth.Body.Bytes(), &healthResp); err != nil {
		t.Fatalf("解析 /api/health 失败: %v", err)
	}
	if healthResp["service"] != config.ServiceName || healthResp["module"] != config.ModuleKey || healthResp["status"] != "ok" {
		t.Fatalf("/api/health 契约不符: %+v", healthResp)
	}

	// 2. 无 Token 请求 GET /api/repos 返回 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	recUnauth := httptest.NewRecorder()
	router.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("无 Token 请求 /api/repos 期望返回 401，实际 %d", recUnauth.Code)
	}

	// 3. 普通用户请求 GET /api/admin/llm-configs 返回 403
	normalUser := createTestUser(t, db, "dev_normal", "普通研发", []string{"user"}, nil)
	normalTok := issueTestToken(t, normalUser)

	reqAdmin := httptest.NewRequest(http.MethodGet, "/api/admin/llm-configs", nil)
	reqAdmin.Header.Set("Authorization", "Bearer "+normalTok)
	recAdmin := httptest.NewRecorder()
	router.ServeHTTP(recAdmin, reqAdmin)
	if recAdmin.Code != http.StatusForbidden {
		t.Fatalf("普通用户请求 /api/admin/llm-configs 期望返回 403，实际 %d", recAdmin.Code)
	}

	// 4. 普通用户请求 GET /api/departments 返回 200
	dept := models.Department{Name: "基础架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	reqDept := httptest.NewRequest(http.MethodGet, "/api/departments", nil)
	reqDept.Header.Set("Authorization", "Bearer "+normalTok)
	recDept := httptest.NewRecorder()
	router.ServeHTTP(recDept, reqDept)
	if recDept.Code != http.StatusOK {
		t.Fatalf("GET /api/departments 期望返回 200，实际 %d (%s)", recDept.Code, recDept.Body.String())
	}
}

func TestSubdirOwnerRBACIsolation(t *testing.T) {
	db, router, rbacSvc := setupM1TestEnv(t)

	dept := models.Department{Name: "核心交易平台部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}

	u1 := createTestUser(t, db, "u1_repo_owner", "仓负责人U1", []string{"user"}, &dept.ID)
	u2 := createTestUser(t, db, "u2_order_owner", "子目录负责人U2", []string{"user"}, &dept.ID)
	u3 := createTestUser(t, db, "u3_other_owner", "子目录负责人U3", []string{"user"}, &dept.ID)
	uAdmin := createTestUser(t, db, "u_archdoc_admin", "模块管理员", []string{"user", config.AdminRole}, &dept.ID)

	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "order-monorepo",
		URL:          "git@git.internal:core/order-monorepo.git",
		OwnerID:      u1.ID,
		Branch:       "master",
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	repoID := repo.ID
	elems := []models.ArchitectureElement{
		{Identifier: "svc-order", NameCn: "订单服务", NameEn: "order-svc", Type: "module", OwnerID: &u2.ID, RepoID: &repoID, Subdirectory: "svc/order/"},
		{Identifier: "svc-order-v2", NameCn: "订单服务V2", NameEn: "order-svc-v2", Type: "module", OwnerID: &u3.ID, RepoID: &repoID, Subdirectory: "svc/order-v2"},
		{Identifier: "svc-pay", NameCn: "支付服务", NameEn: "pay-svc", Type: "module", OwnerID: &u3.ID, RepoID: &repoID, Subdirectory: "svc/pay"},
	}
	if err := db.Create(&elems).Error; err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 断言 U2 对 (R, "svc/order") 有写权限（含尾斜杠规范化匹配）
	if ok, err := rbacSvc.CheckScopeWritePermission(ctx, &u2, repo.ID, "svc/order"); err != nil || !ok {
		t.Fatalf("期望 U2 对 svc/order 具备写权限: ok=%v err=%v", ok, err)
	}
	// 断言 U2 对 (R, "svc/order-v2")、(R, "svc/pay")、(R, "") 整仓均无写权限（SC-12 精确隔离）
	for _, forbiddenSubdir := range []string{"svc/order-v2", "svc/pay", ""} {
		if ok, err := rbacSvc.CheckScopeWritePermission(ctx, &u2, repo.ID, forbiddenSubdir); err != nil || ok {
			t.Fatalf("SC-12 越权漏洞：U2 不应对 %q 具备写权限: ok=%v err=%v", forbiddenSubdir, ok, err)
		}
	}

	// 断言 U1（仓库 Owner）与 archdoc_admin 对全部范围均为 true
	for _, actor := range []models.User{u1, uAdmin} {
		for _, subdir := range []string{"", "svc/order", "svc/order-v2", "svc/pay"} {
			if ok, err := rbacSvc.CheckScopeWritePermission(ctx, &actor, repo.ID, subdir); err != nil || !ok {
				t.Fatalf("期望 %s 对 %q 具备写权限: ok=%v err=%v", actor.Username, subdir, ok, err)
			}
		}
	}

	// 验证通过 HTTP GET /api/repos/:repo_id/scopes 返回的 can_write 矩阵与 U2 严格一致
	u2Tok := issueTestToken(t, u2)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/repos/%d/scopes", repo.ID), nil)
	req.Header.Set("Authorization", "Bearer "+u2Tok)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/repos/%d/scopes 失败: status=%d body=%s", repo.ID, rec.Code, rec.Body.String())
	}

	var scopeResp struct {
		Scopes []services.RepoScopeCoverageVO `json:"scopes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &scopeResp); err != nil {
		t.Fatalf("解析 scopes 响应失败: %v", err)
	}
	if len(scopeResp.Scopes) != 4 {
		t.Fatalf("期望返回 1 整仓 + 3 子目录 = 4 个范围，实际 %d", len(scopeResp.Scopes))
	}
	permBySubdir := make(map[string]bool, len(scopeResp.Scopes))
	for _, sc := range scopeResp.Scopes {
		permBySubdir[sc.Subdirectory] = sc.CanWrite
	}
	if !permBySubdir["svc/order"] || permBySubdir["svc/order-v2"] || permBySubdir["svc/pay"] || permBySubdir[""] {
		t.Fatalf("U2 scopes can_write 矩阵不符 SC-12: %+v", permBySubdir)
	}
}

func TestRepoCoveragePaginationAndDraftHiding(t *testing.T) {
	db, router, _ := setupM1TestEnv(t)

	deptA := models.Department{Name: "支付研发部"}
	deptB := models.Department{Name: "基础架构部"}
	if err := db.Create(&deptA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deptB).Error; err != nil {
		t.Fatal(err)
	}

	owner := createTestUser(t, db, "owner_a", "负责人A", []string{"user"}, &deptA.ID)
	viewer := createTestUser(t, db, "viewer_b", "普通成员B", []string{"user"}, &deptB.ID)

	// 创建 28 个仓库用于测试默认 25 条/页分页
	var targetRepo models.Repository
	for i := 1; i <= 28; i++ {
		dID := deptB.ID
		if i == 1 {
			dID = deptA.ID
		}
		r := models.Repository{
			DepartmentID: dID,
			Name:         fmt.Sprintf("svc-repo-%02d", i),
			URL:          fmt.Sprintf("git@git.internal:org/svc-repo-%02d.git", i),
			OwnerID:      owner.ID,
			Branch:       "master",
			IsActive:     true,
		}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			targetRepo = r
		}
	}

	// 为 targetRepo 写入 1 篇已发布文档 + 1 篇未发布草稿
	now := time.Now()
	approvedDoc := models.Document{
		RepoID:            targetRepo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: 1,
		Title:             "系统架构",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 2,
		PublishedAt:       &now,
	}
	draftDoc := models.Document{
		RepoID:            targetRepo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "data-and-contract",
		DocTypeRevisionID: 1,
		Title:             "数据与契约草稿",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(&approvedDoc).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&draftDoc).Error; err != nil {
		t.Fatal(err)
	}

	// 1. 验证默认 page_size == 25 与分页总数 28
	viewerTok := issueTestToken(t, viewer)
	reqPage1 := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	reqPage1.Header.Set("Authorization", "Bearer "+viewerTok)
	recPage1 := httptest.NewRecorder()
	router.ServeHTTP(recPage1, reqPage1)
	if recPage1.Code != http.StatusOK {
		t.Fatalf("GET /api/repos 失败: %d %s", recPage1.Code, recPage1.Body.String())
	}

	var page1Resp services.RepoCoverageListResponse
	if err := json.Unmarshal(recPage1.Body.Bytes(), &page1Resp); err != nil {
		t.Fatal(err)
	}
	if page1Resp.PageSize != 25 || page1Resp.Page != 1 || page1Resp.Total != 28 || len(page1Resp.Items) != 25 {
		t.Fatalf("分页默认契约不符: page=%d pageSize=%d total=%d len=%d",
			page1Resp.Page, page1Resp.PageSize, page1Resp.Total, len(page1Resp.Items))
	}

	// 2. 普通用户按部门与关键词检索 targetRepo：仅可见 approved，严禁暴露 draft
	reqViewer := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/repos?department_id=%d&search=svc-repo-01", deptA.ID), nil)
	reqViewer.Header.Set("Authorization", "Bearer "+viewerTok)
	recViewer := httptest.NewRecorder()
	router.ServeHTTP(recViewer, reqViewer)
	if recViewer.Code != http.StatusOK {
		t.Fatalf("筛选查询失败: %d", recViewer.Code)
	}
	var viewerResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recViewer.Body.Bytes(), &viewerResp); err != nil {
		t.Fatal(err)
	}
	if len(viewerResp.Items) != 1 {
		t.Fatalf("期望命中 1 条仓库，实际 %d", len(viewerResp.Items))
	}
	if viewerResp.Items[0].CanWrite {
		t.Fatal("普通成员对 targetRepo 不应具备 can_write")
	}
	if _, ok := viewerResp.Items[0].DocStatuses["system-architecture"]; !ok {
		t.Fatal("普通成员应可见已发布文档 system-architecture")
	}
	if _, leaked := viewerResp.Items[0].DocStatuses["data-and-contract"]; leaked {
		t.Fatal("普通成员视图泄露了未发布草稿 data-and-contract！")
	}

	// 3. 仓库 Owner 检索同一仓库：can_write == true 且同时可见 approved 与 draft
	ownerTok := issueTestToken(t, owner)
	reqOwner := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/repos?department_id=%d&search=svc-repo-01", deptA.ID), nil)
	reqOwner.Header.Set("Authorization", "Bearer "+ownerTok)
	recOwner := httptest.NewRecorder()
	router.ServeHTTP(recOwner, reqOwner)
	var ownerResp services.RepoCoverageListResponse
	if err := json.Unmarshal(recOwner.Body.Bytes(), &ownerResp); err != nil {
		t.Fatal(err)
	}
	if !ownerResp.Items[0].CanWrite {
		t.Fatal("仓库 Owner 应具备 can_write == true")
	}
	if _, ok := ownerResp.Items[0].DocStatuses["data-and-contract"]; !ok {
		t.Fatal("仓库 Owner 应可见未发布草稿 data-and-contract")
	}
}

func TestLLMConfigPoolCRUDMaskAuditDefaultAndProbe(t *testing.T) {
	db, router, _ := setupM1TestEnv(t)

	adminUser := createTestUser(t, db, "archdoc_admin_u", "架构文档管理员", []string{"user", config.AdminRole}, nil)
	adminTok := issueTestToken(t, adminUser)

	// 1. 构造 Mock OpenAI /chat/completions 服务
	var failProbe bool
	var receivedAuthHeader string
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		receivedAuthHeader = r.Header.Get("Authorization")
		if failProbe {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","model":"deepseek-v3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer mockLLM.Close()

	// 2. 初始模型池为空
	reqList0 := httptest.NewRequest(http.MethodGet, "/api/admin/llm-configs", nil)
	reqList0.Header.Set("Authorization", "Bearer "+adminTok)
	recList0 := httptest.NewRecorder()
	router.ServeHTTP(recList0, reqList0)
	if recList0.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/llm-configs 失败: %d", recList0.Code)
	}
	var list0 struct {
		Items []services.LLMConfigVO `json:"items"`
	}
	if err := json.Unmarshal(recList0.Body.Bytes(), &list0); err != nil {
		t.Fatal(err)
	}
	if len(list0.Items) != 0 {
		t.Fatalf("期望新环境模型池为空，实际 %d", len(list0.Items))
	}

	// 3. 保存前临时预探活 POST /api/admin/llm-configs/probe
	adhocBody, _ := json.Marshal(map[string]any{
		"base_url":   mockLLM.URL + "/v1",
		"model_name": "deepseek-v3",
		"api_key":    "sk-adhoc-key-1234",
	})
	reqAdhoc := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs/probe", bytes.NewReader(adhocBody))
	reqAdhoc.Header.Set("Authorization", "Bearer "+adminTok)
	reqAdhoc.Header.Set("Content-Type", "application/json")
	recAdhoc := httptest.NewRecorder()
	router.ServeHTTP(recAdhoc, reqAdhoc)
	if recAdhoc.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/llm-configs/probe 失败: %d %s", recAdhoc.Code, recAdhoc.Body.String())
	}
	var adhocRes services.LLMProbeResult
	if err := json.Unmarshal(recAdhoc.Body.Bytes(), &adhocRes); err != nil || !adhocRes.Reachable {
		t.Fatalf("预探活结果异常: %+v err=%v", adhocRes, err)
	}

	// 4. 管理员 POST /api/admin/llm-configs 新增首个模型：自动设为默认模型，明文落库但响应与审计 100% 脱敏
	secretPlain := "sk-test-secret-9999"
	createBody1, _ := json.Marshal(map[string]any{
		"name":                "DeepSeek-V3",
		"base_url":            mockLLM.URL + "/v1",
		"model_name":          "deepseek-v3",
		"api_key":             secretPlain,
		"timeout_seconds":     120,
		"max_retries":         3,
		"max_tokens_per_task": 200000,
		"max_files_per_task":  2000,
		"is_active":           true,
	})
	reqCreate1 := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs", bytes.NewReader(createBody1))
	reqCreate1.Header.Set("Authorization", "Bearer "+adminTok)
	reqCreate1.Header.Set("Content-Type", "application/json")
	recCreate1 := httptest.NewRecorder()
	router.ServeHTTP(recCreate1, reqCreate1)
	if recCreate1.Code != http.StatusCreated {
		t.Fatalf("POST /api/admin/llm-configs 失败: status=%d body=%s", recCreate1.Code, recCreate1.Body.String())
	}

	rawBody := recCreate1.Body.String()
	if strings.Contains(rawBody, secretPlain) || strings.Contains(rawBody, `"api_key"`) {
		t.Fatalf("响应 JSON 绝对不可包含明文 APIKey: %s", rawBody)
	}
	var cfgResp services.LLMConfigVO
	if err := json.Unmarshal(recCreate1.Body.Bytes(), &cfgResp); err != nil {
		t.Fatal(err)
	}
	if cfgResp.APIKeyMasked != "sk-****9999" || !cfgResp.HasAPIKey || !cfgResp.IsDefault || cfgResp.Name != "DeepSeek-V3" {
		t.Fatalf("新增首个模型返回不符合预期: %+v", cfgResp)
	}

	// 验证数据库中直接存储明文 api_key
	var dbCfg models.LLMConfig
	if err := db.First(&dbCfg, cfgResp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dbCfg.APIKey != secretPlain {
		t.Fatalf("数据库应直接明文持久化 APIKey，实际 %q", dbCfg.APIKey)
	}

	// 5. 刷新审计队列并验证 sys_audit_logs 存在 llm_config 记录且绝对不含明文 APIKey
	if err := commonAudit.Close(context.Background()); err != nil {
		t.Fatalf("刷新审计队列失败: %v", err)
	}
	var auditLogs []models.SysAuditLog
	if err := db.Where("target_type = ?", "llm_config").Find(&auditLogs).Error; err != nil {
		t.Fatalf("查询审计表失败: %v", err)
	}
	if len(auditLogs) == 0 {
		t.Fatal("期望在 sys_audit_logs 中写入至少一条 llm_config 审计记录")
	}
	for _, al := range auditLogs {
		combined := al.Summary + " " + al.BeforeData + " " + al.AfterData + " " + al.DiffSummary
		if strings.Contains(combined, secretPlain) {
			t.Fatalf("严重安全漏洞：审计日志泄露了明文 APIKey！log=%+v", al)
		}
	}
	// 重新开启审计供后续用例使用
	commonAudit.Init(db, commonAudit.InitOptions{QueueCap: 256, BatchSize: 1, FlushPeriod: 20 * time.Millisecond})

	// 6. 重复名称创建返回 409 CONFLICT
	reqDup := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs", bytes.NewReader(createBody1))
	reqDup.Header.Set("Authorization", "Bearer "+adminTok)
	reqDup.Header.Set("Content-Type", "application/json")
	recDup := httptest.NewRecorder()
	router.ServeHTTP(recDup, reqDup)
	if recDup.Code != http.StatusConflict {
		t.Fatalf("重复名称期望返回 409，实际 %d (%s)", recDup.Code, recDup.Body.String())
	}

	// 7. 再次更新且 api_key == ""，断言原密钥未被清空
	putBody2, _ := json.Marshal(map[string]any{
		"name":                "DeepSeek-V3",
		"base_url":            mockLLM.URL + "/v1/",
		"model_name":          "deepseek-v3",
		"api_key":             "",
		"timeout_seconds":     150,
		"max_retries":         3,
		"max_tokens_per_task": 250000,
		"max_files_per_task":  2500,
		"is_active":           true,
	})
	reqPut2 := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/llm-configs/%d", cfgResp.ID), bytes.NewReader(putBody2))
	reqPut2.Header.Set("Authorization", "Bearer "+adminTok)
	reqPut2.Header.Set("Content-Type", "application/json")
	recPut2 := httptest.NewRecorder()
	router.ServeHTTP(recPut2, reqPut2)
	if recPut2.Code != http.StatusOK {
		t.Fatalf("二次 PUT 失败: %d %s", recPut2.Code, recPut2.Body.String())
	}
	var dbCfgAfter models.LLMConfig
	if err := db.First(&dbCfgAfter, cfgResp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dbCfgAfter.APIKey != secretPlain {
		t.Fatalf("空 api_key 更新时原密钥应保持不变，实际 %q", dbCfgAfter.APIKey)
	}

	// 8. 禁止直接停用当前默认模型（400 INVALID_PARAM）
	disableBody, _ := json.Marshal(map[string]any{
		"name":                "DeepSeek-V3",
		"base_url":            mockLLM.URL + "/v1",
		"model_name":          "deepseek-v3",
		"timeout_seconds":     150,
		"max_retries":         3,
		"max_tokens_per_task": 250000,
		"max_files_per_task":  2500,
		"is_active":           false,
	})
	reqDisable := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/llm-configs/%d", cfgResp.ID), bytes.NewReader(disableBody))
	reqDisable.Header.Set("Authorization", "Bearer "+adminTok)
	reqDisable.Header.Set("Content-Type", "application/json")
	recDisable := httptest.NewRecorder()
	router.ServeHTTP(recDisable, reqDisable)
	if recDisable.Code != http.StatusBadRequest {
		t.Fatalf("停用当前默认模型期望返回 400，实际 %d (%s)", recDisable.Code, recDisable.Body.String())
	}

	// 9. 测试已保存模型探活成功 & 上游 500 探活失败
	reqProbeOK := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/llm-configs/%d/probe", cfgResp.ID), nil)
	reqProbeOK.Header.Set("Authorization", "Bearer "+adminTok)
	recProbeOK := httptest.NewRecorder()
	router.ServeHTTP(recProbeOK, reqProbeOK)
	if recProbeOK.Code != http.StatusOK {
		t.Fatalf("探活请求失败: %d %s", recProbeOK.Code, recProbeOK.Body.String())
	}
	var probeOK services.LLMProbeResult
	if err := json.Unmarshal(recProbeOK.Body.Bytes(), &probeOK); err != nil {
		t.Fatal(err)
	}
	if !probeOK.Reachable || probeOK.LatencyMs < 0 || probeOK.ModelEcho != "deepseek-v3" {
		t.Fatalf("探活成功响应异常: %+v", probeOK)
	}
	if receivedAuthHeader != "Bearer "+secretPlain {
		t.Fatalf("探活请求未正确携带 Bearer Token: got %q", receivedAuthHeader)
	}

	failProbe = true
	reqProbeFail := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/llm-configs/%d/probe", cfgResp.ID), nil)
	reqProbeFail.Header.Set("Authorization", "Bearer "+adminTok)
	recProbeFail := httptest.NewRecorder()
	router.ServeHTTP(recProbeFail, reqProbeFail)
	if recProbeFail.Code != http.StatusOK {
		t.Fatalf("探活 500 场景仍应返回 200 状态结构体，实际 %d", recProbeFail.Code)
	}
	var probeFail services.LLMProbeResult
	if err := json.Unmarshal(recProbeFail.Body.Bytes(), &probeFail); err != nil {
		t.Fatal(err)
	}
	if probeFail.Reachable || probeFail.ErrorMessage == "" {
		t.Fatalf("上游 500 应返回 reachable=false 与错误说明: %+v", probeFail)
	}

	// 10. 新增第二个模型 GLM-4，验证多模型共存、不可直接删默认模型、切换默认后可删及 DocType 引用保护
	createBody2, _ := json.Marshal(map[string]any{
		"name":                "GLM-4",
		"base_url":            mockLLM.URL + "/v1",
		"model_name":          "glm-4-plus",
		"api_key":             "sk-glm-secret-1111",
		"timeout_seconds":     180,
		"max_retries":         2,
		"max_tokens_per_task": 200000,
		"max_files_per_task":  2000,
		"is_active":           true,
	})
	reqCreate2 := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs", bytes.NewReader(createBody2))
	reqCreate2.Header.Set("Authorization", "Bearer "+adminTok)
	reqCreate2.Header.Set("Content-Type", "application/json")
	recCreate2 := httptest.NewRecorder()
	router.ServeHTTP(recCreate2, reqCreate2)
	if recCreate2.Code != http.StatusCreated {
		t.Fatalf("创建第二个模型 GLM-4 失败: %d %s", recCreate2.Code, recCreate2.Body.String())
	}
	var cfg2 services.LLMConfigVO
	_ = json.Unmarshal(recCreate2.Body.Bytes(), &cfg2)
	if cfg2.IsDefault {
		t.Fatal("新增第二个模型时不应抢占已有默认模型")
	}

	// 存在多个模型时直接删除默认模型 DeepSeek-V3 -> 409 CONFLICT
	reqDelDef := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/llm-configs/%d", cfgResp.ID), nil)
	reqDelDef.Header.Set("Authorization", "Bearer "+adminTok)
	recDelDef := httptest.NewRecorder()
	router.ServeHTTP(recDelDef, reqDelDef)
	if recDelDef.Code != http.StatusConflict {
		t.Fatalf("非唯一默认模型删除期望返回 409，实际 %d (%s)", recDelDef.Code, recDelDef.Body.String())
	}

	// 手动切换默认为 GLM-4 -> 200
	reqSetDef := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/llm-configs/%d/default", cfg2.ID), nil)
	reqSetDef.Header.Set("Authorization", "Bearer "+adminTok)
	recSetDef := httptest.NewRecorder()
	router.ServeHTTP(recSetDef, reqSetDef)
	if recSetDef.Code != http.StatusOK {
		t.Fatalf("切换默认模型失败: %d %s", recSetDef.Code, recSetDef.Body.String())
	}

	// 绑定某个 DocType 到 GLM-4，验证删除 GLM-4 时被引用保护拦截 -> 409 CONFLICT
	if err := db.Model(&models.DocType{}).Where("slug = ?", "agents-context").Update("preferred_model", "GLM-4").Error; err != nil {
		t.Fatal(err)
	}
	// 先把 DeepSeek-V3 设回默认以便检验引用拦截（即使单看非默认也应拦截）
	reqDelRef := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/llm-configs/%d", cfg2.ID), nil)
	reqDelRef.Header.Set("Authorization", "Bearer "+adminTok)
	recDelRef := httptest.NewRecorder()
	router.ServeHTTP(recDelRef, reqDelRef)
	if recDelRef.Code != http.StatusConflict {
		t.Fatalf("被 DocType 指定的模型删除期望返回 409，实际 %d (%s)", recDelRef.Code, recDelRef.Body.String())
	}

	// 删除非默认的 DeepSeek-V3 -> 200
	reqDel1 := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/admin/llm-configs/%d", cfgResp.ID), nil)
	reqDel1.Header.Set("Authorization", "Bearer "+adminTok)
	recDel1 := httptest.NewRecorder()
	router.ServeHTTP(recDel1, reqDel1)
	if recDel1.Code != http.StatusOK {
		t.Fatalf("删除非默认模型失败: %d %s", recDel1.Code, recDel1.Body.String())
	}

	// 测试 clear_api_key 清空 GLM-4 密钥
	clearBody, _ := json.Marshal(map[string]any{
		"name":                "GLM-4",
		"base_url":            mockLLM.URL + "/v1",
		"model_name":          "glm-4-plus",
		"clear_api_key":       true,
		"timeout_seconds":     180,
		"max_retries":         2,
		"max_tokens_per_task": 200000,
		"max_files_per_task":  2000,
		"is_active":           true,
	})
	reqClear := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/admin/llm-configs/%d", cfg2.ID), bytes.NewReader(clearBody))
	reqClear.Header.Set("Authorization", "Bearer "+adminTok)
	reqClear.Header.Set("Content-Type", "application/json")
	recClear := httptest.NewRecorder()
	router.ServeHTTP(recClear, reqClear)
	if recClear.Code != http.StatusOK {
		t.Fatalf("clear_api_key 请求失败: %d %s", recClear.Code, recClear.Body.String())
	}
	var clearedVO services.LLMConfigVO
	_ = json.Unmarshal(recClear.Body.Bytes(), &clearedVO)
	if clearedVO.HasAPIKey || clearedVO.APIKeyMasked != "" {
		t.Fatalf("clear_api_key 后期望 has_api_key=false，实际 %+v", clearedVO)
	}

	// 非法 ID 参数返回 400 INVALID_PARAM
	reqBadID := httptest.NewRequest(http.MethodPut, "/api/admin/llm-configs/invalid", bytes.NewReader(putBody2))
	reqBadID.Header.Set("Authorization", "Bearer "+adminTok)
	reqBadID.Header.Set("Content-Type", "application/json")
	recBadID := httptest.NewRecorder()
	router.ServeHTTP(recBadID, reqBadID)
	if recBadID.Code != http.StatusBadRequest {
		t.Fatalf("非法 ID 期望返回 400，实际 %d", recBadID.Code)
	}
}
