package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type refDocTestEnv struct {
	db         *gorm.DB
	router     *gin.Engine
	refDocsDir string
	refDocSvc  *services.RefDocService
}

func setupRefDocTestEnv(t *testing.T) *refDocTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "refdoc-test-jwt-secret-key-0123456789"
	config.AppConfig.Auth.StandaloneMode = true
	config.AppConfig.Storage.RefDocMaxBytes = 524288

	masterStore := models.NewSharedMasterStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)
	moduleStore := models.NewModuleStore(db)
	refDocStore := models.NewReferenceDocStore(db)

	refDocsDir := filepath.Join(t.TempDir(), "ref_docs")
	rbacSvc := services.NewRBACService(masterStore)
	refDocSvc, err := services.NewRefDocService(refDocStore, masterStore, rbacSvc, refDocsDir, 524288)
	if err != nil {
		t.Fatalf("NewRefDocService 失败: %v", err)
	}
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc).
		WithModuleRepo(moduleStore).
		WithRefDocRepo(refDocStore)
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

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, nil, nil).
		WithRefDocService(refDocSvc)
	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)

	return &refDocTestEnv{
		db:         db,
		router:     r,
		refDocsDir: refDocsDir,
		refDocSvc:  refDocSvc,
	}
}

func doRefDocJSONReq(router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestRefDocCRUDOptimisticLockAndPurge_T_ADR10_02(t *testing.T) {
	env := setupRefDocTestEnv(t)
	dept := models.Department{Name: "架构部"}
	if err := env.db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	admin := createTestUser(t, env.db, "ref_admin", "管理员", []string{config.AdminRole}, &dept.ID)
	adminTok := issueTestToken(t, admin)

	// 1. multipart 上传创建 v1.md
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	payloadJSON := `{"title":"全系统总体架构","doc_kind":"arch_design","initial_binding":{"scope_level":"system","inherit_to_children":true}}`
	_ = mw.WriteField("payload", payloadJSON)
	fw, _ := mw.CreateFormFile("file", "system-arch.md")
	_, _ = fw.Write([]byte("# 总体架构\n\n## 1. 系统定位\n支撑电商核心业务。\n"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/ref-docs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+adminTok)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("multipart 创建参考文档期望 201，实际 %d: %s", w.Code, w.Body.String())
	}

	var created services.ReferenceDocDetailVO
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.CurrentVersion != 1 || created.OwnerScopeKey != "system" {
		t.Fatalf("创建结果异常: %+v", created)
	}
	v1Path := filepath.Join(env.refDocsDir, fmt.Sprintf("%d", created.ID), "v1.md")
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("v1.md 未原子落盘: %v", err)
	}

	// 2. 空正文 / 超 512KB / 硬凭据校验
	emptyW := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", adminTok, map[string]any{
		"title":           "空正文",
		"content_md":      "   \n",
		"initial_binding": map[string]any{"scope_level": "system", "inherit_to_children": true},
	})
	if emptyW.Code != http.StatusBadRequest {
		t.Fatalf("空正文期望 400，实际 %d: %s", emptyW.Code, emptyW.Body.String())
	}
	oversizeW := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", adminTok, map[string]any{
		"title":           "超限",
		"content_md":      strings.Repeat("x", 524289),
		"initial_binding": map[string]any{"scope_level": "system", "inherit_to_children": true},
	})
	if oversizeW.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversizeW.Body.String(), handlers.ErrCodePayloadTooLarge) {
		t.Fatalf("超 512KB 期望 413 PAYLOAD_TOO_LARGE，实际 %d: %s", oversizeW.Code, oversizeW.Body.String())
	}
	secretW := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", adminTok, map[string]any{
		"title":           "硬凭据",
		"content_md":      "# Secret\nkey: \"sk-1234567890abcdef\"\n",
		"redact_and_save": true,
		"initial_binding": map[string]any{"scope_level": "system", "inherit_to_children": true},
	})
	if secretW.Code != http.StatusUnprocessableEntity || !strings.Contains(secretW.Body.String(), handlers.ErrCodeSecretDetected) {
		t.Fatalf("硬凭据期望 422 SECRET_DETECTED，实际 %d: %s", secretW.Code, secretW.Body.String())
	}

	// 3. 带正确 expected_version 更新生成 v2.md 并保留 v1.md；旧 expected_version 返 409 VERSION_CONFLICT
	updW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", created.ID), adminTok, map[string]any{
		"expected_version": 1,
		"content_md":       "# 总体架构 V2\n\n## 1. 系统定位\n新增多活架构。\n",
	})
	if updW.Code != http.StatusOK {
		t.Fatalf("PUT 更新期望 200，实际 %d: %s", updW.Code, updW.Body.String())
	}
	v2Path := filepath.Join(env.refDocsDir, fmt.Sprintf("%d", created.ID), "v2.md")
	if _, err := os.Stat(v2Path); err != nil {
		t.Fatalf("v2.md 未落盘: %v", err)
	}
	if _, err := os.Stat(v1Path); err != nil {
		t.Fatalf("v1.md 不应被删除: %v", err)
	}

	conflictW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", created.ID), adminTok, map[string]any{
		"expected_version": 1,
		"title":            "并发覆盖",
	})
	if conflictW.Code != http.StatusConflict || !strings.Contains(conflictW.Body.String(), handlers.ErrCodeVersionConflict) {
		t.Fatalf("旧版本期望 409 VERSION_CONFLICT，实际 %d: %s", conflictW.Code, conflictW.Body.String())
	}

	// 4. GET ?version=1 仍可回溯 v1 正文
	getV1 := doRefDocJSONReq(env.router, http.MethodGet, fmt.Sprintf("/api/ref-docs/%d?version=1", created.ID), adminTok, nil)
	if getV1.Code != http.StatusOK || !strings.Contains(getV1.Body.String(), "支撑电商核心业务") {
		t.Fatalf("读取 v1 历史版本失败: %d %s", getV1.Code, getV1.Body.String())
	}

	// 5. 管理员 purge 物理删盘
	purgeW := doRefDocJSONReq(env.router, http.MethodDelete, fmt.Sprintf("/api/admin/ref-docs/%d/purge", created.ID), adminTok, nil)
	if purgeW.Code != http.StatusOK {
		t.Fatalf("purge 期望 200，实际 %d: %s", purgeW.Code, purgeW.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.refDocsDir, fmt.Sprintf("%d", created.ID))); !os.IsNotExist(err) {
		t.Fatalf("purge 后磁盘目录应被彻底删除")
	}
}

func TestRefDocInheritanceOverrideAndArchTreeCounts_T_ADR10_03_06(t *testing.T) {
	env := setupRefDocTestEnv(t)
	dept := models.Department{Name: "交易研发"}
	_ = env.db.Create(&dept).Error
	admin := createTestUser(t, env.db, "inh_admin", "管理员", []string{config.AdminRole}, &dept.ID)
	subsysOwner := createTestUser(t, env.db, "inh_sub_owner", "子系统负责人", []string{"user"}, &dept.ID)
	modOwner := createTestUser(t, env.db, "inh_mod_owner", "模块负责人", []string{"user"}, &dept.ID)

	adminTok := issueTestToken(t, admin)
	subOwnerTok := issueTestToken(t, subsysOwner)
	modOwnerTok := issueTestToken(t, modOwner)

	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "trade-monorepo",
		URL:          "git@git.internal:core/trade.git",
		OwnerID:      subsysOwner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	_ = env.db.Create(&repo).Error

	subsys := models.ArchitectureElement{Identifier: "trade-sys", NameCn: "交易子系统", Type: "subsystem", OwnerID: &subsysOwner.ID}
	_ = env.db.Create(&subsys).Error
	grp := models.ArchitectureElement{Identifier: "order-grp", NameCn: "订单功能组", Type: "group", ParentID: &subsys.ID}
	_ = env.db.Create(&grp).Error
	mod := models.ArchitectureElement{Identifier: "order-mod", NameCn: "订单模块", Type: "module", ParentID: &grp.ID, OwnerID: &modOwner.ID, RepoID: &repo.ID, Subdirectory: "svc/order"}
	_ = env.db.Create(&mod).Error

	// system 传 inherit_to_children=false 返回 400
	badSys := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", adminTok, map[string]any{
		"title":           "非法全系统",
		"content_md":      "# Sys",
		"initial_binding": map[string]any{"scope_level": "system", "inherit_to_children": false},
	})
	if badSys.Code != http.StatusBadRequest {
		t.Fatalf("system 设 inherit_to_children=false 期望 400，实际 %d", badSys.Code)
	}

	// 依次创建 5 层文档：system(1), subsystem(2), group(3), repo(4), module(5)
	wSys := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", adminTok, map[string]any{
		"title":           "L1 全系统规范",
		"content_md":      "# L1\n全系统规范。",
		"initial_binding": map[string]any{"scope_level": "system", "inherit_to_children": true},
	})
	if wSys.Code != http.StatusCreated {
		t.Fatalf("创建 system 文档失败: %d %s", wSys.Code, wSys.Body.String())
	}
	var sysDoc services.ReferenceDocDetailVO
	_ = json.Unmarshal(wSys.Body.Bytes(), &sysDoc)

	// 在 module 上对同一 system 文档增加消费绑定并限定 doc_type_slugs=["data-and-contract"]，覆盖上级 system 策略
	bindOverrideW := doRefDocJSONReq(env.router, http.MethodPost, fmt.Sprintf("/api/ref-docs/%d/bindings", sysDoc.ID), modOwnerTok, map[string]any{
		"scope_level":         "module",
		"arch_element_id":     mod.ID,
		"inherit_to_children": true,
		"doc_type_slugs":      []string{"data-and-contract"},
	})
	if bindOverrideW.Code != http.StatusCreated {
		t.Fatalf("module 覆盖绑定失败: %d %s", bindOverrideW.Code, bindOverrideW.Body.String())
	}

	wSub := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", subOwnerTok, map[string]any{
		"title":           "L2 子系统设计",
		"content_md":      "# L2\n子系统设计。",
		"initial_binding": map[string]any{"scope_level": "subsystem", "arch_element_id": subsys.ID, "inherit_to_children": true},
	})
	if wSub.Code != http.StatusCreated {
		t.Fatalf("创建 subsystem 文档失败: %d %s", wSub.Code, wSub.Body.String())
	}

	// subsysOwner 通过祖先链权限管理无自身 OwnerID 的 group 节点
	wGrp := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", subOwnerTok, map[string]any{
		"title":           "L3 功能组设计",
		"content_md":      "# L3\n功能组设计。",
		"initial_binding": map[string]any{"scope_level": "group", "arch_element_id": grp.ID, "inherit_to_children": true},
	})
	if wGrp.Code != http.StatusCreated {
		t.Fatalf("创建 group 文档失败: %d %s", wGrp.Code, wGrp.Body.String())
	}

	// 已挂树仓库原有 repo 绑定仍应生效
	wRepo := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", modOwnerTok, map[string]any{
		"title":           "L4 仓库子目录补充",
		"content_md":      "# L4\n仓库级补充。",
		"initial_binding": map[string]any{"scope_level": "repo", "repo_id": repo.ID, "subdirectory": "svc/order", "inherit_to_children": true},
	})
	if wRepo.Code != http.StatusCreated {
		t.Fatalf("创建 repo 文档失败: %d %s", wRepo.Code, wRepo.Body.String())
	}

	wMod := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", modOwnerTok, map[string]any{
		"title":           "L5 模块详设",
		"content_md":      "# L5\n模块详设。",
		"initial_binding": map[string]any{"scope_level": "module", "arch_element_id": mod.ID, "inherit_to_children": true},
	})
	if wMod.Code != http.StatusCreated {
		t.Fatalf("创建 module 文档失败: %d %s", wMod.Code, wMod.Body.String())
	}

	// 1. 查询 doc_type_slug="system-architecture" 时：sysDoc 被最具体的 module 绑定 doc_type_slugs=["data-and-contract"] 过滤排除！
	//    其余 4 篇按 subsystem(2) -> group(3) -> repo(4) -> module(5) 排序返回
	effW1 := doRefDocJSONReq(env.router, http.MethodGet, fmt.Sprintf("/api/ref-docs/effective?repo_id=%d&subdirectory=svc/order&doc_type_slug=system-architecture", repo.ID), modOwnerTok, nil)
	if effW1.Code != http.StatusOK {
		t.Fatalf("effective 查询失败: %d %s", effW1.Code, effW1.Body.String())
	}
	var effRes1 services.EffectiveRefDocsResponse
	_ = json.Unmarshal(effW1.Body.Bytes(), &effRes1)
	if effRes1.TotalCount != 4 {
		t.Fatalf("期望最具体 module 覆盖排除 sysDoc 后剩 4 篇，实际 %d: %+v", effRes1.TotalCount, effRes1.Items)
	}
	wantOrder := []string{"subsystem", "group", "repo", "module"}
	for i, wantLvl := range wantOrder {
		if effRes1.Items[i].EffectiveScopeLevel != wantLvl {
			t.Fatalf("第 %d 篇期望层级 %s，实际 %s", i, wantLvl, effRes1.Items[i].EffectiveScopeLevel)
		}
	}

	// 2. 查询 doc_type_slug="data-and-contract" 时 sysDoc 以 module 绑定层级生效（共计 5 篇）
	effW2 := doRefDocJSONReq(env.router, http.MethodGet, fmt.Sprintf("/api/ref-docs/effective?arch_element_id=%d&doc_type_slug=data-and-contract", mod.ID), modOwnerTok, nil)
	var effRes2 services.EffectiveRefDocsResponse
	_ = json.Unmarshal(effW2.Body.Bytes(), &effRes2)
	if effRes2.TotalCount != 5 {
		t.Fatalf("期望 data-and-contract 返回 5 篇，实际 %d", effRes2.TotalCount)
	}

	// 3. GetArchTree 聚合计数验证（T-ADR10-06 部分）
	treeW := doRefDocJSONReq(env.router, http.MethodGet, "/api/arch-tree", modOwnerTok, nil)
	if treeW.Code != http.StatusOK {
		t.Fatalf("GetArchTree 失败: %d %s", treeW.Code, treeW.Body.String())
	}
	var treeRes services.ArchTreeResponse
	_ = json.Unmarshal(treeW.Body.Bytes(), &treeRes)
	if treeRes.Summary.SystemRefDocCount != 1 {
		t.Fatalf("期望 system_ref_doc_count=1，实际 %d", treeRes.Summary.SystemRefDocCount)
	}
	subNode := treeRes.Nodes[0]
	grpNode := subNode.Children[0]
	modNode := grpNode.Children[0]
	if subNode.DirectRefDocCount != 1 || subNode.InheritedRefDocCount != 1 {
		t.Fatalf("subNode 计数异常: direct=%d inherited=%d", subNode.DirectRefDocCount, subNode.InheritedRefDocCount)
	}
	if grpNode.DirectRefDocCount != 1 || grpNode.InheritedRefDocCount != 2 {
		t.Fatalf("grpNode 计数异常: direct=%d inherited=%d", grpNode.DirectRefDocCount, grpNode.InheritedRefDocCount)
	}
	// modNode 直接绑了 sysDoc + wMod (2 篇)，继承 sub + grp + repo (3 篇)
	if modNode.DirectRefDocCount != 2 || modNode.InheritedRefDocCount != 3 {
		t.Fatalf("modNode 计数异常: direct=%d inherited=%d", modNode.DirectRefDocCount, modNode.InheritedRefDocCount)
	}
}

func TestRefDocOwnerRBACAntiLockoutAndZeroBinding_T_ADR10_04(t *testing.T) {
	env := setupRefDocTestEnv(t)
	dept := models.Department{Name: "安全合规"}
	_ = env.db.Create(&dept).Error

	ownerA := createTestUser(t, env.db, "rbac_owner_a", "作者A", []string{"user"}, &dept.ID)
	consumerB := createTestUser(t, env.db, "rbac_consumer_b", "消费方B", []string{"user"}, &dept.ID)
	outsiderC := createTestUser(t, env.db, "rbac_outsider_c", "旁观者C", []string{"user"}, &dept.ID)

	tokA := issueTestToken(t, ownerA)
	tokB := issueTestToken(t, consumerB)
	tokC := issueTestToken(t, outsiderC)

	subA := models.ArchitectureElement{Identifier: "sub-a", NameCn: "子系统A", Type: "subsystem", OwnerID: &ownerA.ID}
	_ = env.db.Create(&subA).Error
	grpA := models.ArchitectureElement{Identifier: "grp-a", NameCn: "功能组A", Type: "group", ParentID: &subA.ID}
	_ = env.db.Create(&grpA).Error
	subB := models.ArchitectureElement{Identifier: "sub-b", NameCn: "子系统B", Type: "subsystem", OwnerID: &consumerB.ID}
	_ = env.db.Create(&subB).Error

	// 1. ownerA（subA 负责人）凭借祖先链权在 grpA 下创建参考文档
	createW := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", tokA, map[string]any{
		"title":           "共享接口契约",
		"content_md":      "# 契约 V1\n初始正文。",
		"initial_binding": map[string]any{"scope_level": "group", "arch_element_id": grpA.ID, "inherit_to_children": true},
	})
	if createW.Code != http.StatusCreated {
		t.Fatalf("ownerA 跨层创建失败: %d %s", createW.Code, createW.Body.String())
	}
	var doc services.ReferenceDocDetailVO
	_ = json.Unmarshal(createW.Body.Bytes(), &doc)
	homeBindingID := doc.Bindings[0].ID

	// 2. consumerB 将该文档消费绑定到 subB
	bindBW := doRefDocJSONReq(env.router, http.MethodPost, fmt.Sprintf("/api/ref-docs/%d/bindings", doc.ID), tokB, map[string]any{
		"scope_level":         "subsystem",
		"arch_element_id":     subB.ID,
		"inherit_to_children": true,
	})
	if bindBW.Code != http.StatusCreated {
		t.Fatalf("consumerB 消费绑定失败: %d %s", bindBW.Code, bindBW.Body.String())
	}
	var bindingB services.RefDocBindingVO
	_ = json.Unmarshal(bindBW.Body.Bytes(), &bindingB)

	// 3. 防互锁：consumerB 绑定后，原作者 ownerA 仍可正常 PUT 更新 v2；consumerB 无权 PUT（返 403）
	putBW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", doc.ID), tokB, map[string]any{
		"expected_version": 1,
		"content_md":       "# 越权篡改",
	})
	if putBW.Code != http.StatusForbidden {
		t.Fatalf("消费方 B 修改他人文档期望 403，实际 %d", putBW.Code)
	}
	putAW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", doc.ID), tokA, map[string]any{
		"expected_version": 1,
		"content_md":       "# 契约 V2\n原作者更新。",
	})
	if putAW.Code != http.StatusOK {
		t.Fatalf("消费绑定后原作者 A 仍应可正常 PUT，实际 %d: %s", putAW.Code, putAW.Body.String())
	}

	// 4. active 状态下禁止直接 DELETE is_home=true 主绑定（返 400 HOME_BINDING_PROTECTED）
	delHomeW := doRefDocJSONReq(env.router, http.MethodDelete, fmt.Sprintf("/api/ref-docs/%d/bindings/%d", doc.ID, homeBindingID), tokA, nil)
	if delHomeW.Code != http.StatusBadRequest || !strings.Contains(delHomeW.Body.String(), handlers.ErrCodeHomeBindingProtected) {
		t.Fatalf("active 下删 is_home 期望 400 HOME_BINDING_PROTECTED，实际 %d: %s", delHomeW.Code, delHomeW.Body.String())
	}

	// 5. consumerB 可以解绑自己节点的消费绑定
	delBW := doRefDocJSONReq(env.router, http.MethodDelete, fmt.Sprintf("/api/ref-docs/%d/bindings/%d", doc.ID, bindingB.ID), tokB, nil)
	if delBW.Code != http.StatusOK {
		t.Fatalf("consumerB 解绑己方节点失败: %d %s", delBW.Code, delBW.Body.String())
	}

	// 6. 零消费绑定防越权：先归档再删光所有绑定，普通旁观者 C 调用 PUT 仍必须返回 403
	archW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", doc.ID), tokA, map[string]any{
		"expected_version": 2,
		"status":           "archived",
	})
	if archW.Code != http.StatusOK {
		t.Fatalf("归档失败: %d %s", archW.Code, archW.Body.String())
	}
	delHomeArchived := doRefDocJSONReq(env.router, http.MethodDelete, fmt.Sprintf("/api/ref-docs/%d/bindings/%d", doc.ID, homeBindingID), tokA, nil)
	if delHomeArchived.Code != http.StatusOK {
		t.Fatalf("归档后允许清理主绑定，实际 %d: %s", delHomeArchived.Code, delHomeArchived.Body.String())
	}
	putCW := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", doc.ID), tokC, map[string]any{
		"expected_version": 2,
		"title":            "零绑定越权",
	})
	if putCW.Code != http.StatusForbidden {
		t.Fatalf("零绑定文档非归属用户 C 写操作期望 403，实际 %d: %s", putCW.Code, putCW.Body.String())
	}
}

type refDocTaskEnv struct {
	db        *gorm.DB
	router    *gin.Engine
	refDocSvc *services.RefDocService
	genSvc    *services.GenerationOrchestrator
	driftSvc  *services.DriftOrchestrator
	owner     models.User
	ownerTok  string
	repo      models.Repository
}

func setupRefDocTaskEnv(t *testing.T, llmURL string) *refDocTaskEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "refdoc-task-jwt-secret-key-0123456789"
	config.AppConfig.Auth.StandaloneMode = true
	config.AppConfig.Storage.RefDocMaxBytes = 524288

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixtureDir := filepath.Clean(filepath.Join(wd, "..", "testdata", "golden", "fixtures", "cpp_order_service"))

	tmpData := t.TempDir()
	docTypesDir := filepath.Join(tmpData, "doc_types")
	irCacheDir := filepath.Join(tmpData, "ir_cache")
	sandboxDir := filepath.Join(tmpData, "sandbox_dir")
	refDocsDir := filepath.Join(tmpData, "ref_docs")

	reg, err := doctype.NewRegistry(docTypesDir)
	if err != nil {
		t.Fatal(err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(irCacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	agg := codeir.NewAggregator(treesitter.NewCppExtractor(), treesitter.NewCExtractor())
	gitSyncer := &countingGitSyncer{
		fixtureDir: fixtureDir,
		commitSHA:  "a1b2c3d4e5f6a7b8c9d0a1b2c3d4e5f6a7b8c9d0",
	}

	masterStore := models.NewSharedMasterStore(db)
	docTypeStore := models.NewDocTypeStore(db)
	docStore := models.NewDocumentStore(db)
	snapStore := models.NewSnapshotStore(db)
	taskStore := models.NewTaskStore(db)
	driftStore := models.NewDriftStore(db)
	govStore := models.NewGovernanceStore(db)
	moduleStore := models.NewModuleStore(db)
	refDocStore := models.NewReferenceDocStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	refDocSvc, err := services.NewRefDocService(refDocStore, masterStore, rbacSvc, refDocsDir, 524288)
	if err != nil {
		t.Fatal(err)
	}
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc).
		WithModuleRepo(moduleStore).
		WithRefDocRepo(refDocStore)
	llmSvc := services.NewLLMConfigService(govStore).WithDocTypeRepo(docTypeStore)
	docTypeSvc := services.NewDocTypeService(reg, docTypeStore).WithSpecDriftNotifier(docStore, driftStore)
	if _, err := docTypeSvc.SeedAndSyncAll(context.Background()); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, gitSyncer, sandboxDir, 1048576)
	llmClient := llm.NewClientWithOptions(&http.Client{Timeout: 10 * time.Second}, 10*time.Millisecond)
	genSvc := services.NewGenerationOrchestrator(
		masterStore, docTypeStore, docTypeSvc, snapSvc, taskStore, docStore, govStore, llmClient, 4,
	).WithModuleGovernance(moduleStore, nil, rbacSvc).
		WithRefDocService(refDocSvc)
	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc).
		WithModuleContext(moduleStore).
		WithRefDocService(refDocSvc)
	driftSvc := services.NewDriftOrchestrator(
		masterStore, docStore, docTypeStore, docTypeSvc, driftStore, rbacSvc, genSvc, agg, services.NewActiveLeaseManager(), sandboxDir, 15,
	).WithSnapshotService(snapSvc).WithModuleRepo(moduleStore)

	_ = commonAudit.Close(context.Background())
	commonAudit.Init(db, commonAudit.InitOptions{QueueCap: 128, BatchSize: 1, FlushPeriod: 20 * time.Millisecond})
	t.Cleanup(func() {
		genSvc.WaitAll()
		_ = commonAudit.Close(context.Background())
	})

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithRefDocService(refDocSvc).
		WithDocumentServices(revSvc, nil, nil).
		WithGovernanceServices(driftSvc, nil, nil, nil)
	r := gin.New()
	r.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers.RegisterRoutes(r, db)

	dept := models.Department{Name: "订单平台部"}
	_ = db.Create(&dept).Error
	owner := createTestUser(t, db, "ref_task_owner", "任务负责人", []string{"user"}, &dept.ID)
	repo := models.Repository{
		DepartmentID: dept.ID,
		Name:         "cpp_order_service",
		URL:          "git@git.internal:core/cpp_order_service.git",
		OwnerID:      owner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	_ = db.Create(&repo).Error

	llmCfg := models.LLMConfig{
		Name:             "RefDoc-Test-Model",
		BaseURL:          llmURL,
		ModelName:        "mock-deepseek",
		APIKey:           "sk-mock-refdoc-test-0001",
		APIKeyMasked:     "sk-****0001",
		TimeoutSeconds:   15,
		MaxTokensPerTask: 200000,
		MaxFilesPerTask:  2000,
		IsActive:         true,
		IsDefault:        true,
	}
	_ = db.Create(&llmCfg).Error

	return &refDocTaskEnv{
		db:        db,
		router:    r,
		refDocSvc: refDocSvc,
		genSvc:    genSvc,
		driftSvc:  driftSvc,
		owner:     owner,
		ownerTok:  issueTestToken(t, owner),
		repo:      repo,
	}
}

func newMockRefDocTaskLLMServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sysText := ""
		if len(req.Messages) > 0 {
			sysText = req.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(sysText, "glossary") || (len(req.Messages) > 1 && strings.Contains(req.Messages[1].Content, "术语表")) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"domain_terms":[],"module_summaries":[{"path":"src","summary":"订单核心模块"}]}`}}},
				"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 20},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"markdown":"基于架构参考生成章节正文。","cited_anchors":[]}`}}},
			"usage":   map[string]any{"prompt_tokens": 80, "completion_tokens": 30},
		})
	}))
}

func waitTaskDone(t *testing.T, db *gorm.DB, taskID uint) models.GenTask {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var task models.GenTask
		if err := db.First(&task, taskID).Error; err == nil {
			if task.Status == models.TaskStatusReady || task.Status == models.TaskStatusFailed || task.Status == models.TaskStatusCanceled {
				return task
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	var task models.GenTask
	_ = db.First(&task, taskID).Error
	t.Fatalf("等待任务 #%d 完成超时，当前状态=%s msg=%s", taskID, task.Status, task.ErrorMessage)
	return task
}

func TestRefDocUpdateZeroDriftAndIncrementalRefreshSnapshot_T_ADR10_05(t *testing.T) {
	srv := newMockRefDocTaskLLMServer()
	defer srv.Close()
	env := setupRefDocTaskEnv(t, srv.URL)

	// 1. 创建参考文档 v1 并绑定到 repo
	cw := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", env.ownerTok, map[string]any{
		"title":           "订单领域背景",
		"content_md":      "# 领域背景 v1\n\n订单域核心目标 v1。",
		"initial_binding": map[string]any{"scope_level": "repo", "repo_id": env.repo.ID},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("创建参考文档 v1 失败: %d %s", cw.Code, cw.Body.String())
	}
	var refDoc services.ReferenceDocDetailVO
	_ = json.Unmarshal(cw.Body.Bytes(), &refDoc)

	// 2. 基于 v1 生成文档并直接标为 approved 已发布状态
	gw1 := doRefDocJSONReq(env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
	})
	if gw1.Code != http.StatusAccepted {
		t.Fatalf("生成任务 1 提交失败: %d %s", gw1.Code, gw1.Body.String())
	}
	var genRes1 services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(gw1.Body.Bytes(), &genRes1)
	t1 := waitTaskDone(t, env.db, genRes1.ID)
	if t1.Status != models.TaskStatusReady {
		t.Fatalf("任务 1 失败: %s %s", t1.ErrorCode, t1.ErrorMessage)
	}

	var doc models.Document
	if err := env.db.Where("repo_id = ? AND doc_type_slug = ?", env.repo.ID, "system-architecture").First(&doc).Error; err != nil {
		t.Fatalf("未生成目标文档: %v", err)
	}
	var rev models.DocumentRevision
	_ = env.db.Where("document_id = ? AND revision_no = ?", doc.ID, doc.CurrentRevisionNo).First(&rev).Error
	_ = env.db.Model(&doc).Updates(map[string]any{
		"status":                models.DocStatusApproved,
		"published_revision_id": rev.ID,
		"published_commit_sha":  rev.CommitSHA,
	}).Error

	// 3. 更新参考文档到 v2，重算 Spec Drift 与 Code Drift -> 零漂移产生
	uw := doRefDocJSONReq(env.router, http.MethodPut, fmt.Sprintf("/api/ref-docs/%d", refDoc.ID), env.ownerTok, map[string]any{
		"expected_version": 1,
		"content_md":       "# 领域背景 v2\n\n订单域核心目标更新至 v2。",
	})
	if uw.Code != http.StatusOK {
		t.Fatalf("更新参考文档 v2 失败: %d %s", uw.Code, uw.Body.String())
	}
	if err := env.driftSvc.RecomputeAllSpecDrifts(context.Background()); err != nil {
		t.Fatalf("RecomputeAllSpecDrifts 失败: %v", err)
	}
	_ = env.driftSvc.EvaluateRepoCodeDrift(context.Background(), models.PollableRepoRecord{
		RepoID:  env.repo.ID,
		RepoURL: env.repo.URL,
		Branch:  env.repo.Branch,
	}, rev.CommitSHA, "master")

	var driftedCount int64
	_ = env.db.Model(&models.DocumentDrift{}).Where("document_id = ? AND has_drift = ?", doc.ID, true).Count(&driftedCount).Error
	if driftedCount != 0 {
		t.Fatalf("更新参考文档不得触发已有已发布文档的 Spec/Code Drift，实际 has_drift=true 记录数=%d", driftedCount)
	}

	// 4. 新发起 generate 与 incremental-refresh 均绑定 v2 快照
	gw2 := doRefDocJSONReq(env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
	})
	if gw2.Code != http.StatusAccepted {
		t.Fatalf("生成任务 2 失败: %d %s", gw2.Code, gw2.Body.String())
	}
	var genRes2 services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(gw2.Body.Bytes(), &genRes2)
	if len(genRes2.ReferenceDocs) != 1 || genRes2.ReferenceDocs[0].VersionNo != 2 {
		t.Fatalf("生成任务 2 期望快照 v2，实际 %+v", genRes2.ReferenceDocs)
	}
	t2 := waitTaskDone(t, env.db, genRes2.ID)
	if t2.Status != models.TaskStatusReady {
		t.Fatalf("任务 2 失败: %s", t2.ErrorMessage)
	}

	irw := doRefDocJSONReq(env.router, http.MethodPost, fmt.Sprintf("/api/documents/%d/incremental-refresh", doc.ID), env.ownerTok, map[string]any{
		"slot_ids": []string{"overview_boundaries"},
	})
	if irw.Code != http.StatusAccepted {
		t.Fatalf("增量刷新提交失败: %d %s", irw.Code, irw.Body.String())
	}
	var irRes services.GenTaskDetailVO
	_ = json.Unmarshal(irw.Body.Bytes(), &irRes)
	if len(irRes.ReferenceDocs) != 1 || irRes.ReferenceDocs[0].VersionNo != 2 {
		t.Fatalf("增量刷新任务期望捕获 v2 快照，实际 %+v", irRes.ReferenceDocs)
	}
	t3 := waitTaskDone(t, env.db, irRes.ID)
	if t3.Status != models.TaskStatusReady {
		t.Fatalf("增量刷新执行失败: %s", t3.ErrorMessage)
	}
}

func TestRefDocTaskExcludeAndArchElementValidation_T_ADR10_06(t *testing.T) {
	srv := newMockRefDocTaskLLMServer()
	defer srv.Close()
	env := setupRefDocTaskEnv(t, srv.URL)

	// 创建两篇参考文档 A 与 B
	var docA, docB services.ReferenceDocDetailVO
	for idx, ptr := range []*services.ReferenceDocDetailVO{&docA, &docB} {
		cw := doRefDocJSONReq(env.router, http.MethodPost, "/api/ref-docs", env.ownerTok, map[string]any{
			"title":           fmt.Sprintf("参考文档-%d", idx+1),
			"content_md":      fmt.Sprintf("# 正文 %d\n内容详情。", idx+1),
			"initial_binding": map[string]any{"scope_level": "repo", "repo_id": env.repo.ID},
		})
		if cw.Code != http.StatusCreated {
			t.Fatalf("创建参考文档 %d 失败: %d %s", idx+1, cw.Code, cw.Body.String())
		}
		_ = json.Unmarshal(cw.Body.Bytes(), ptr)
	}

	// 创建另一个不属于 env.repo 的架构节点，用于验证 arch_element_id 归属校验
	otherRepo := models.Repository{
		DepartmentID: env.repo.DepartmentID,
		Name:         "other_repo",
		URL:          "git@git.internal:core/other_repo.git",
		OwnerID:      env.owner.ID,
		Branch:       "master",
		IsActive:     true,
	}
	_ = env.db.Create(&otherRepo).Error
	otherElem := models.ArchitectureElement{
		Identifier: "other-mod",
		NameCn:     "其他模块",
		Type:       "module",
		RepoID:     &otherRepo.ID,
		OwnerID:    &env.owner.ID,
	}
	_ = env.db.Create(&otherElem).Error

	// 1. 传入不匹配的 arch_element_id 返回 400
	badW := doRefDocJSONReq(env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":         env.repo.ID,
		"doc_types":       []string{"system-architecture"},
		"arch_element_id": otherElem.ID,
	})
	if badW.Code != http.StatusBadRequest {
		t.Fatalf("不匹配 arch_element_id 期望 400，实际 %d: %s", badW.Code, badW.Body.String())
	}

	// 2. 单仓生成传 exclude_ref_doc_ids: [docB.ID]，仅冻结 docA
	gw := doRefDocJSONReq(env.router, http.MethodPost, "/api/tasks/generate", env.ownerTok, map[string]any{
		"repo_id":             env.repo.ID,
		"doc_types":           []string{"system-architecture"},
		"exclude_ref_doc_ids": []uint{docB.ID},
	})
	if gw.Code != http.StatusAccepted {
		t.Fatalf("单任务排除提交失败: %d %s", gw.Code, gw.Body.String())
	}
	var genRes services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(gw.Body.Bytes(), &genRes)
	if len(genRes.ReferenceDocs) != 1 || genRes.ReferenceDocs[0].DocID != docA.ID {
		t.Fatalf("exclude_ref_doc_ids 应只保留 docA(%d)，实际 %+v", docA.ID, genRes.ReferenceDocs)
	}
	t1 := waitTaskDone(t, env.db, genRes.ID)
	if t1.Status != models.TaskStatusReady {
		t.Fatalf("任务执行失败: %s", t1.ErrorMessage)
	}

	// 3. generate-batch 透传 exclude_ref_doc_ids 至子任务
	bw := doRefDocJSONReq(env.router, http.MethodPost, "/api/tasks/generate-batch", env.ownerTok, map[string]any{
		"repo_id":             env.repo.ID,
		"include_repo_scope":  true,
		"doc_types":           []string{"system-architecture"},
		"exclude_ref_doc_ids": []uint{docB.ID},
	})
	if bw.Code != http.StatusAccepted {
		t.Fatalf("批量生成提交失败: %d %s", bw.Code, bw.Body.String())
	}
	var batchRes services.GenTaskDetailVO
	_ = json.Unmarshal(bw.Body.Bytes(), &batchRes)
	tb := waitTaskDone(t, env.db, batchRes.ID)
	if tb.Status != models.TaskStatusReady {
		t.Fatalf("批量任务失败: %s", tb.ErrorMessage)
	}
	var children []models.GenTask
	_ = env.db.Where("parent_task_id = ?", tb.ID).Find(&children).Error
	if len(children) == 0 {
		t.Fatal("期望至少产生 1 个批量子任务")
	}
	var childSnaps []models.TaskReferenceDocSnapshot
	_ = json.Unmarshal(children[0].ReferenceDocSnapshotJSON, &childSnaps)
	if len(childSnaps) != 1 || childSnaps[0].DocID != docA.ID {
		t.Fatalf("批量子任务期望排除 docB 仅剩 docA(%d)，实际 %+v", docA.ID, childSnaps)
	}

	// 4. incremental-refresh 透传 exclude_ref_doc_ids
	var doc models.Document
	_ = env.db.Where("repo_id = ? AND doc_type_slug = ?", env.repo.ID, "system-architecture").First(&doc).Error
	irw := doRefDocJSONReq(env.router, http.MethodPost, fmt.Sprintf("/api/documents/%d/incremental-refresh", doc.ID), env.ownerTok, map[string]any{
		"slot_ids":            []string{"overview_boundaries"},
		"exclude_ref_doc_ids": []uint{docA.ID},
	})
	if irw.Code != http.StatusAccepted {
		t.Fatalf("增量刷新排除提交失败: %d %s", irw.Code, irw.Body.String())
	}
	var irRes services.GenTaskDetailVO
	_ = json.Unmarshal(irw.Body.Bytes(), &irRes)
	if len(irRes.ReferenceDocs) != 1 || irRes.ReferenceDocs[0].DocID != docB.ID {
		t.Fatalf("增量刷新排除 docA 后应仅剩 docB(%d)，实际 %+v", docB.ID, irRes.ReferenceDocs)
	}
	_ = waitTaskDone(t, env.db, irRes.ID)
}
