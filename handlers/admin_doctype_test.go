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
	"sync"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type adminDocTypeEnv struct {
	db          *gorm.DB
	router      *gin.Engine
	docTypeSvc  *services.DocTypeService
	docTypesDir string
	adminUser   models.User
	adminTok    string
	normalUser  models.User
	normalTok   string
	repo        models.Repository
}

func setupAdminDocTypeEnv(t *testing.T) *adminDocTypeEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m5-admin-doctype-test-secret-0123456789"
	config.AppConfig.Auth.StandaloneMode = true

	docTypesDir := filepath.Join(t.TempDir(), "doc_types")
	reg, err := doctype.NewRegistry(docTypesDir)
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
	llmSvc := services.NewLLMConfigService(govStore)
	docTypeSvc := services.NewDocTypeService(reg, dtStore).
		WithSpecDriftNotifier(docStore, driftStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	commonAudit.Init(db)
	t.Cleanup(func() {
		docTypeSvc.WaitSpecDriftJobs()
		_ = commonAudit.Close(context.Background())
	})

	router := gin.New()
	router.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, nil)
	appHandlers.RegisterRoutes(router, db)

	dept := commonModels.Department{Name: "安全架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	adminUser := createTestUser(t, db, "archdoc_admin_u", "文档管理员", []string{"user", config.AdminRole}, &dept.ID)
	normalUser := createTestUser(t, db, "normal_dev_u", "普通研发", []string{"user"}, &dept.ID)
	repo := models.Repository{
		Name:         "sec-target-repo",
		URL:          "https://git.internal/sec/target.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      adminUser.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	return &adminDocTypeEnv{
		db:          db,
		router:      router,
		docTypeSvc:  docTypeSvc,
		docTypesDir: docTypesDir,
		adminUser:   adminUser,
		adminTok:    issueTestToken(t, adminUser),
		normalUser:  normalUser,
		normalTok:   issueTestToken(t, normalUser),
		repo:        repo,
	}
}

func doAdminDocTypeReq(router *gin.Engine, method, url, token string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestDocTypeLifecycleCreateCloneEditToggleDelete(t *testing.T) {
	env := setupAdminDocTypeEnv(t)

	createPayload := map[string]any{
		"slug":            "security-review",
		"display_name":    "安全架构评审报告",
		"short_name":      "安全评审",
		"category":        "质量与安全",
		"description":     "面向研发安全评审的攻击面分析",
		"preferred_model": "DeepSeek-V3",
		"timeout_seconds": 300,
		"ir_facets":       []string{"tree", "http_routes", "config_keys"},
		"slots": []map[string]any{
			{
				"slot_id":      "attack_surface",
				"title":        "1. 外部入口与攻击面清单",
				"ownership":    "auto",
				"required":     true,
				"ir_facets":    []string{"http_routes"},
				"token_budget": 8000,
				"min_chars":    40,
			},
		},
		"template_md":          "## 1. 外部入口与攻击面清单\n<!-- slot:attack_surface -->\n",
		"generation_prompt_md": "你是资深安全架构师。",
		"refine_prompt_md":     "保持输出简洁。",
	}

	// 1. 非管理员调用返回 403
	wForbidden := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.normalTok, createPayload)
	if wForbidden.Code != http.StatusForbidden {
		t.Fatalf("非管理员新建 DocType 应返回 403，实际 %d: %s", wForbidden.Code, wForbidden.Body.String())
	}

	// 2. 传入非法 slug ../evil 返回 400 INVALID_SLUG
	evilPayload := map[string]any{}
	for k, v := range createPayload {
		evilPayload[k] = v
	}
	evilPayload["slug"] = "../evil"
	wEvil := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, evilPayload)
	if wEvil.Code != http.StatusBadRequest || !strings.Contains(wEvil.Body.String(), handlers.ErrCodeInvalidSlug) {
		t.Fatalf("非法 slug 应返回 400 INVALID_SLUG，实际 %d: %s", wEvil.Code, wEvil.Body.String())
	}

	// 3. 管理员新建 security-review -> 201，磁盘 4 个文件落盘且 revision_no=1
	wCreate := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, createPayload)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("新建 security-review 失败: %d %s", wCreate.Code, wCreate.Body.String())
	}
	var created services.DocTypeDetailVO
	if err := json.Unmarshal(wCreate.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析新建响应失败: %v", err)
	}
	if created.Slug != "security-review" || created.LatestRevisionNo != 1 || created.LatestRevisionID == nil {
		t.Fatalf("新建结果异常: %+v", created)
	}
	if created.PreferredModel != "DeepSeek-V3" {
		t.Fatalf("新建 DocType 应保留 preferred_model=DeepSeek-V3，实际 %q", created.PreferredModel)
	}
	if created.ShortName != "安全评审" || created.Category != "质量与安全" {
		t.Fatalf("新建 DocType 应持久化并返回 short_name/category，实际 short=%q category=%q", created.ShortName, created.Category)
	}
	for _, fn := range doctype.OrderedAssetFiles {
		fp := filepath.Join(env.docTypesDir, "security-review", fn)
		if _, err := os.Stat(fp); err != nil {
			t.Fatalf("磁盘缺少文件 %s: %v", fn, err)
		}
	}

	// 4. 重复新建同 slug 返回 409 CONFLICT
	wDup := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, createPayload)
	if wDup.Code != http.StatusConflict {
		t.Fatalf("重复新建同 slug 应返回 409，实际 %d: %s", wDup.Code, wDup.Body.String())
	}

	// 5. 手工向磁盘 meta.json 注入未知扩展字段，再通过在线 PUT 修改 generation_prompt_md
	metaPath := filepath.Join(env.docTypesDir, "security-review", "meta.json")
	rawMeta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var metaMap map[string]any
	if err := json.Unmarshal(rawMeta, &metaMap); err != nil {
		t.Fatal(err)
	}
	metaMap["p1_custom_policy"] = map[string]any{"strict": true, "level": 3}
	injectedBytes, _ := json.MarshalIndent(metaMap, "", "  ")
	if err := os.WriteFile(metaPath, injectedBytes, 0640); err != nil {
		t.Fatal(err)
	}

	// 创建一篇绑定 security-review revision_no=1 且 status="approved" 的已发布文档，验证 PUT 自动触发 Spec Drift 与 DELETE 409
	rev1ID := *created.LatestRevisionID
	doc := &models.Document{
		RepoID:            env.repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "security-review",
		DocTypeRevisionID: rev1ID,
		Title:             "安全架构评审报告",
		Status:            models.DocStatusApproved,
		CurrentRevisionNo: 1,
	}
	if err := env.db.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	docRev := &models.DocumentRevision{
		DocumentID:         doc.ID,
		RevisionNo:         1,
		SnapshotID:         1,
		CommitSHA:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DocTypeRevisionID:  rev1ID,
		Source:             models.RevSourceAIGenerate,
		FullMarkdown:       "## 1. 外部入口与攻击面清单\n内容",
		SectionsJSON:       datatypes.JSON(`[]`),
		VerifierReportJSON: datatypes.JSON(`{"passed":true,"can_approve":true}`),
		CreatedBy:          env.adminUser.ID,
	}
	if err := env.db.Create(docRev).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := env.db.Model(doc).Updates(map[string]any{
		"current_revision_id":   docRev.ID,
		"published_revision_id": docRev.ID,
		"published_commit_sha":  docRev.CommitSHA,
		"published_at":          now,
		"published_by":          env.adminUser.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 在线 PUT 修改 generation_prompt_md（不传 p1_custom_policy，验证服务端无损保留！）
	putPayload := map[string]any{
		"meta": map[string]any{
			"name":            "security-review",
			"display_name":    "安全架构评审报告",
			"short_name":      "安全基线",
			"category":        "安全治理",
			"description":     "面向研发安全评审的攻击面分析",
			"is_active":       true,
			"preferred_model": "GLM-4",
			"timeout_seconds": 300,
			"ir_facets":       []string{"tree", "http_routes", "config_keys"},
			"slots": []map[string]any{
				{
					"slot_id":      "attack_surface",
					"title":        "1. 外部入口与攻击面清单",
					"ownership":    "auto",
					"required":     true,
					"ir_facets":    []string{"http_routes"},
					"token_budget": 8000,
					"min_chars":    40,
				},
			},
		},
		"template_md":          "## 1. 外部入口与攻击面清单\n<!-- slot:attack_surface -->\n",
		"generation_prompt_md": "你是资深安全架构师，请额外核查鉴权绕过与凭据防泄漏。",
		"refine_prompt_md":     "保持输出简洁。",
	}
	wPut := doAdminDocTypeReq(env.router, http.MethodPut, "/api/admin/doc-types/security-review", env.adminTok, putPayload)
	if wPut.Code != http.StatusOK {
		t.Fatalf("PUT 更新 DocType 失败: %d %s", wPut.Code, wPut.Body.String())
	}
	var updated services.DocTypeUpdateVO
	if err := json.Unmarshal(wPut.Body.Bytes(), &updated); err != nil {
		t.Fatalf("解析 PUT 响应失败: %v", err)
	}
	if !updated.RevisionCreated || updated.LatestRevisionNo != 2 {
		t.Fatalf("期望生成 revision_no=2 且 revision_created=true，实际: rev=%d created=%v", updated.LatestRevisionNo, updated.RevisionCreated)
	}
	if updated.ShortName != "安全基线" || updated.Category != "安全治理" {
		t.Fatalf("PUT 更新后应返回新 short_name/category，实际 short=%q category=%q", updated.ShortName, updated.Category)
	}
	if len(updated.Slots) != 1 || updated.Slots[0].SlotID != "attack_surface" {
		t.Fatalf("未改动槽位 slot_id 应保持不变: %+v", updated.Slots)
	}

	// 验证磁盘 meta.json 未知扩展键 p1_custom_policy 完整保留
	diskMetaAfterPut, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diskMetaAfterPut), `"p1_custom_policy"`) {
		t.Fatalf("PUT 写回磁盘丢失了未知扩展键 p1_custom_policy: %s", string(diskMetaAfterPut))
	}

	// 验证异步 Spec Drift 钩子写入了 archdoc_document_drifts(drift_type="spec")
	env.docTypeSvc.WaitSpecDriftJobs()
	var specDrift models.DocumentDrift
	if err := env.db.Where("document_id = ? AND drift_type = ?", doc.ID, models.DriftTypeSpec).First(&specDrift).Error; err != nil {
		t.Fatalf("期望 PUT 新修订自动触发 Spec Drift 落库，但查询失败: %v", err)
	}
	if !specDrift.HasDrift || specDrift.AffectedSlotCount != 1 {
		t.Fatalf("Spec Drift 结果异常: %+v", specDrift)
	}

	// 6. 克隆与启停切换
	wClone := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types/security-review/clone", env.adminTok, map[string]any{
		"new_slug":     "security-review-v2",
		"display_name": "安全评审 V2",
	})
	if wClone.Code != http.StatusCreated {
		t.Fatalf("克隆失败: %d %s", wClone.Code, wClone.Body.String())
	}
	wToggle := doAdminDocTypeReq(env.router, http.MethodPatch, "/api/admin/doc-types/security-review-v2/status", env.adminTok, map[string]any{
		"is_active": false,
	})
	if wToggle.Code != http.StatusOK {
		t.Fatalf("停用切换失败: %d %s", wToggle.Code, wToggle.Body.String())
	}
	var toggled services.DocTypeDetailVO
	if err := json.Unmarshal(wToggle.Body.Bytes(), &toggled); err != nil || toggled.IsActive {
		t.Fatalf("停用后 is_active 应为 false: %s", wToggle.Body.String())
	}

	// 7. 存在已发布文档引用时 DELETE -> 409 DOC_TYPE_IN_USE，且磁盘与 DB 均未被删
	wDelBlocked := doAdminDocTypeReq(env.router, http.MethodDelete, "/api/admin/doc-types/security-review", env.adminTok, nil)
	if wDelBlocked.Code != http.StatusConflict || !strings.Contains(wDelBlocked.Body.String(), handlers.ErrCodeDocTypeInUse) {
		t.Fatalf("已被已发布文档引用时 DELETE 应返回 409 DOC_TYPE_IN_USE，实际 %d: %s", wDelBlocked.Code, wDelBlocked.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.docTypesDir, "security-review", "meta.json")); err != nil {
		t.Fatalf("拦截删除后磁盘目录不应被删除: %v", err)
	}

	// 8. 将文档撤回为 draft 后再次 DELETE -> 200 且磁盘与 DB 同步清除
	if err := env.db.Model(doc).Updates(map[string]any{
		"status":                models.DocStatusDraft,
		"published_revision_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}
	wDelOK := doAdminDocTypeReq(env.router, http.MethodDelete, "/api/admin/doc-types/security-review", env.adminTok, nil)
	if wDelOK.Code != http.StatusOK {
		t.Fatalf("撤回文档为 draft 后删除应返回 200，实际 %d: %s", wDelOK.Code, wDelOK.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.docTypesDir, "security-review")); !os.IsNotExist(err) {
		t.Fatalf("删除后磁盘目录应被彻底移除，实际 stat err=%v", err)
	}
	var dtCount int64
	env.db.Model(&models.DocType{}).Where("slug = ?", "security-review").Count(&dtCount)
	if dtCount != 0 {
		t.Fatalf("删除后 DB 记录应为 0，实际 %d", dtCount)
	}

	// 9. 手动同步接口 POST /api/admin/doc-types/sync -> 200
	wSync := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types/sync", env.adminTok, nil)
	if wSync.Code != http.StatusOK {
		t.Fatalf("手动同步失败: %d %s", wSync.Code, wSync.Body.String())
	}
}

func TestDocTypeConcurrentWriteSerializedBySlugLock(t *testing.T) {
	env := setupAdminDocTypeEnv(t)

	// 先创建目标 DocType
	initPayload := map[string]any{
		"slug":            "concurrent-tpl",
		"display_name":    "并发模板",
		"timeout_seconds": 300,
		"ir_facets":       []string{"tree"},
		"slots": []map[string]any{
			{
				"slot_id":      "overview",
				"title":        "1. 概览",
				"ownership":    "auto",
				"required":     true,
				"ir_facets":    []string{"tree"},
				"token_budget": 4000,
				"min_chars":    20,
			},
		},
		"template_md":          "## 1. 概览\n<!-- slot:overview -->\n",
		"generation_prompt_md": "初始指令",
		"refine_prompt_md":     "",
	}
	wInit := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, initPayload)
	if wInit.Code != http.StatusCreated {
		t.Fatalf("初始化失败: %d %s", wInit.Code, wInit.Body.String())
	}

	// 并发 5 协程修改同一 slug 的模板文件
	var wg sync.WaitGroup
	errCh := make(chan string, 10)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			payload := map[string]any{
				"meta": map[string]any{
					"name":            "concurrent-tpl",
					"display_name":    fmt.Sprintf("并发模板-W%d", worker),
					"is_active":       true,
					"timeout_seconds": 300,
					"ir_facets":       []string{"tree"},
					"slots": []map[string]any{
						{
							"slot_id":      "overview",
							"title":        fmt.Sprintf("1. 概览 W%d", worker),
							"ownership":    "auto",
							"required":     true,
							"ir_facets":    []string{"tree"},
							"token_budget": 4000,
							"min_chars":    20,
						},
					},
				},
				"template_md":          fmt.Sprintf("## 1. 概览 W%d\n<!-- slot:overview -->\n", worker),
				"generation_prompt_md": fmt.Sprintf("并发指令 W%d", worker),
				"refine_prompt_md":     "",
			}
			w := doAdminDocTypeReq(env.router, http.MethodPut, "/api/admin/doc-types/concurrent-tpl", env.adminTok, payload)
			if w.Code != http.StatusOK {
				errCh <- fmt.Sprintf("worker %d failed: %d %s", worker, w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}

	// 校验无临时文件残留、JSON 可完整解析，且最终磁盘内容与 DB 最新 DocTypeRevision 100% 一致
	diskBundle, err := env.docTypeSvc.Registry().LoadOne("concurrent-tpl")
	if err != nil {
		t.Fatalf("读取磁盘最终资产失败: %v", err)
	}
	detail, err := env.docTypeSvc.GetDetail(context.Background(), "concurrent-tpl")
	if err != nil {
		t.Fatalf("读取 DB 最终详情失败: %v", err)
	}
	if detail.LatestAggregateHash != diskBundle.AggregateHash {
		t.Fatalf("磁盘与 DB 最新修订哈希不一致: disk=%s db=%s", diskBundle.AggregateHash, detail.LatestAggregateHash)
	}
	if detail.TemplateMD != diskBundle.TemplateMD || detail.GenerationPromptMD != diskBundle.GenerationPromptMD {
		t.Fatalf("磁盘与 DB 最终模板/指令文本不一致: diskTpl=%q dbTpl=%q", diskBundle.TemplateMD, detail.TemplateMD)
	}
	entries, err := os.ReadDir(filepath.Join(env.docTypesDir, "concurrent-tpl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".tmp-") {
			t.Fatalf("并发写后残留临时文件: %s", ent.Name())
		}
	}
}

// TestCreateDocTypeWithGovernancePoliciesAndSourceTools 验证 T-ADR05-12：
// POST /api/admin/doc-types 携带 repo_ir_facets、context_inputs、module_digest_source、scope_profile、verify_policy
// 及槽位 enable_source_tools=true, max_tool_rounds=4 创建并回读；传入非法 repo_ir_facets 返回 400 且无目录残留。
func TestCreateDocTypeWithGovernancePoliciesAndSourceTools(t *testing.T) {
	env := setupAdminDocTypeEnv(t)

	validPayload := map[string]any{
		"slug":            "governed-arch",
		"display_name":    "治理增强架构文档",
		"short_name":      "治理架构",
		"category":        "架构设计",
		"description":     "覆盖整仓裁剪、上下文输入与只读源码工具配置",
		"is_active":       true,
		"timeout_seconds": 300,
		"ir_facets":       []string{"tree", "deps", "symbols", "http_routes"},
		"repo_ir_facets":  []string{"tree", "deps", "http_routes"},
		"context_inputs":  []string{"code_ir", "module_context", "module_digests"},
		"module_digest_source": map[string]any{
			"doc_type_slug":  "governed-arch",
			"slot_id":        "runtime_flow",
			"max_chars":      600,
			"fallback_order": []string{"approved", "draft"},
		},
		"scope_profile": map[string]any{
			"include_globs":       []string{"src/**"},
			"exclude_globs":       []string{"vendor/**"},
			"max_depth":           8,
			"include_tests":       true,
			"include_legacy_docs": true,
		},
		"verify_policy": map[string]any{
			"strict_symbol_check":  true,
			"require_mermaid":      true,
			"max_mermaid_nodes":    30,
			"max_mermaid_edges":    50,
			"block_on_secret_leak": true,
		},
		"slots": []map[string]any{
			{
				"slot_id":             "runtime_flow",
				"title":               "1. 核心并发与源码调用链",
				"ownership":           "auto",
				"required":            true,
				"ir_facets":           []string{"tree", "deps", "symbols", "http_routes"},
				"token_budget":        10000,
				"min_chars":           40,
				"enable_source_tools": true,
				"max_tool_rounds":     4,
				"instruction_hint":    "调用只读源码工具核实关键链路。",
			},
		},
		"template_md":          "# 治理增强架构文档\n\n## 1. 核心并发与源码调用链\n<!-- slot:runtime_flow -->\n",
		"generation_prompt_md": "你是架构师。",
		"refine_prompt_md":     "精修保持准确。",
	}

	wCreate := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, validPayload)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("合法高级策略创建应返回 201，实际 %d: %s", wCreate.Code, wCreate.Body.String())
	}

	// 回读 GET /api/doc-types/:slug 校验 detail.slots、latest_revision.slots_snapshot 与 meta 高级策略无损保留
	wDetail := doAdminDocTypeReq(env.router, http.MethodGet, "/api/doc-types/governed-arch", env.adminTok, nil)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("GET 详情失败: %d %s", wDetail.Code, wDetail.Body.String())
	}
	var detail services.DocTypeDetailVO
	if err := json.Unmarshal(wDetail.Body.Bytes(), &detail); err != nil {
		t.Fatalf("解析详情 JSON 失败: %v", err)
	}
	if len(detail.Slots) != 1 || !detail.Slots[0].EnableSourceTools || detail.Slots[0].MaxToolRounds != 4 {
		t.Fatalf("detail.slots 丢失源码工具配置: %+v", detail.Slots)
	}
	if detail.LatestRevision == nil || len(detail.LatestRevision.SlotsSnapshot) != 1 ||
		!detail.LatestRevision.SlotsSnapshot[0].EnableSourceTools || detail.LatestRevision.SlotsSnapshot[0].MaxToolRounds != 4 {
		t.Fatalf("latest_revision.slots_snapshot 丢失源码工具配置: %+v", detail.LatestRevision)
	}
	if len(detail.Meta.RepoIRFacets) != 3 || len(detail.Meta.ContextInputs) != 3 {
		t.Fatalf("meta.repo_ir_facets 或 context_inputs 未完整透传: repo=%+v ctx=%+v", detail.Meta.RepoIRFacets, detail.Meta.ContextInputs)
	}
	if detail.Meta.ModuleDigestSource == nil || detail.Meta.ModuleDigestSource.SlotID != "runtime_flow" || detail.Meta.ModuleDigestSource.MaxChars != 600 {
		t.Fatalf("meta.module_digest_source 未完整透传: %+v", detail.Meta.ModuleDigestSource)
	}
	if detail.Meta.ScopeProfile == nil || detail.Meta.ScopeProfile.MaxDepth != 8 || !detail.Meta.ScopeProfile.IncludeTests || !detail.Meta.ScopeProfile.IncludeLegacyDocs {
		t.Fatalf("meta.scope_profile 未完整透传: %+v", detail.Meta.ScopeProfile)
	}
	if detail.Meta.VerifyPolicy == nil || !detail.Meta.VerifyPolicy.StrictSymbolCheck || !detail.Meta.VerifyPolicy.RequireMermaid || detail.Meta.VerifyPolicy.MaxMermaidNodes != 30 {
		t.Fatalf("meta.verify_policy 未完整透传: %+v", detail.Meta.VerifyPolicy)
	}

	// 核查磁盘 meta.json 落盘完整性
	metaBytes, err := os.ReadFile(filepath.Join(env.docTypesDir, "governed-arch", "meta.json"))
	if err != nil {
		t.Fatalf("读取磁盘 meta.json 失败: %v", err)
	}
	var diskMeta doctype.Meta
	if err := json.Unmarshal(metaBytes, &diskMeta); err != nil {
		t.Fatalf("解析磁盘 meta.json 失败: %v", err)
	}
	if len(diskMeta.RepoIRFacets) != 3 || len(diskMeta.ContextInputs) != 3 || !diskMeta.Slots[0].EnableSourceTools || diskMeta.Slots[0].MaxToolRounds != 4 {
		t.Fatalf("磁盘 meta.json 字段未完整保留: %+v", diskMeta)
	}

	// 非法 repo_ir_facets 请求应返回 400 且无磁盘目录残留
	invalidPayload := map[string]any{
		"slug":                 "invalid-repo-facet",
		"display_name":         "非法分片测试",
		"timeout_seconds":      300,
		"ir_facets":            []string{"tree"},
		"repo_ir_facets":       []string{"not_a_facet"},
		"slots":                validPayload["slots"],
		"template_md":          validPayload["template_md"],
		"generation_prompt_md": "指令",
		"refine_prompt_md":     "",
	}
	wBad := doAdminDocTypeReq(env.router, http.MethodPost, "/api/admin/doc-types", env.adminTok, invalidPayload)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("非法 repo_ir_facets 应返回 400，实际 %d: %s", wBad.Code, wBad.Body.String())
	}
	if _, statErr := os.Stat(filepath.Join(env.docTypesDir, "invalid-repo-facet")); !os.IsNotExist(statErr) {
		t.Fatalf("非法请求不应残留磁盘目录，实际 stat err=%v", statErr)
	}
}
