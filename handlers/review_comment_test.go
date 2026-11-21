package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

func TestReviewCommentsCRUDMonotonicCommentNoAndRBAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}
	config.AppConfig.Auth.JWTSecret = "adr06-review-comments-jwt-secret-0123456789"
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
	reviewStore := models.NewReviewCommentStore(db)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	docTypeSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, nil)
	reviewSvc := services.NewReviewCommentService(reviewStore, docStore)

	router := gin.New()
	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, nil, docTypeSvc, nil).
		WithDocumentServices(revSvc, nil, nil).
		WithReviewCommentService(reviewSvc)
	appHandlers.RegisterRoutes(router, db)

	dept := commonModels.Department{Name: "基础架构部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	orderOwner := createTestUser(t, db, "rc_order_owner", "订单负责人", []string{"user"}, &dept.ID)
	payOwner := createTestUser(t, db, "rc_pay_owner", "支付负责人", []string{"user"}, &dept.ID)
	normalReader := createTestUser(t, db, "rc_reader", "普通成员", []string{"user"}, &dept.ID)
	repoOwner := createTestUser(t, db, "rc_repo_owner", "仓库负责人", []string{"user"}, &dept.ID)

	repo := models.Repository{
		Name:         "trade-monorepo",
		URL:          "https://git.internal/trade/monorepo.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      repoOwner.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]models.ArchitectureElement{
		{Identifier: "svc-order", NameCn: "订单模块", NameEn: "order", Type: "module", RepoID: &repo.ID, Subdirectory: "svc/order", OwnerID: &orderOwner.ID},
		{Identifier: "svc-pay", NameCn: "支付模块", NameEn: "pay", Type: "module", RepoID: &repo.ID, Subdirectory: "svc/pay", OwnerID: &payOwner.ID},
	}).Error; err != nil {
		t.Fatal(err)
	}

	_, dtRev, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}
	snaps := []models.DocumentSectionSnapshot{
		{SlotID: "overview_boundaries", SectionOrder: 1, Title: "1. 概览", Ownership: models.OwnershipAuto, Markdown: "概览正文", ContentSHA256: testSHA256Hex("概览正文")},
		{SlotID: "runtime_concurrency", SectionOrder: 2, Title: "2. 并发", Ownership: models.OwnershipAuto, Markdown: "并发正文", ContentSHA256: testSHA256Hex("并发正文")},
	}
	snapsJSON, _ := json.Marshal(snaps)
	doc := &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "svc/order",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "trade-monorepo (svc/order) 系统架构",
		Status:            models.DocStatusDraft,
		CurrentRevisionNo: 1,
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatal(err)
	}
	rev := &models.DocumentRevision{
		DocumentID:         doc.ID,
		RevisionNo:         1,
		CommitSHA:          strings.Repeat("1", 40),
		DocTypeRevisionID:  dtRev.ID,
		Source:             models.RevSourceAIGenerate,
		SectionsJSON:       datatypes.JSON(snapsJSON),
		FullMarkdown:       "概览正文\n\n并发正文",
		VerifierReportJSON: datatypes.JSON(`{"passed":true,"can_approve":true}`),
		VerifierPassed:     true,
		CanApprove:         true,
		CreatedBy:          orderOwner.ID,
	}
	if err := db.Create(rev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(doc).Update("current_revision_id", rev.ID).Error; err != nil {
		t.Fatal(err)
	}

	orderTok := issueTestToken(t, orderOwner)
	payTok := issueTestToken(t, payOwner)
	readerTok := issueTestToken(t, normalReader)
	baseURL := fmt.Sprintf("/api/documents/%d/review-comments", doc.ID)

	// 1. RBAC 越权拦截：普通用户与跨子目录负责人操作均返回 403
	for _, tc := range []struct {
		name string
		tok  string
	}{
		{name: "normal_user", tok: readerTok},
		{name: "cross_subdir_owner", tok: payTok},
	} {
		wGet := doAdminDocTypeReq(router, http.MethodGet, baseURL, tc.tok, nil)
		if wGet.Code != http.StatusForbidden {
			t.Fatalf("%s GET review-comments 应返回 403，实际 %d", tc.name, wGet.Code)
		}
		wPost := doAdminDocTypeReq(router, http.MethodPost, baseURL, tc.tok, map[string]any{
			"scope_type": "global",
			"content":    "越权批注",
		})
		if wPost.Code != http.StatusForbidden {
			t.Fatalf("%s POST review-comments 应返回 403，实际 %d", tc.name, wPost.Code)
		}
	}

	// 2. 非法参数校验：不存在的 slot_id 或 quote > 500 字符均返回 400
	wBadSlot := doAdminDocTypeReq(router, http.MethodPost, baseURL, orderTok, map[string]any{
		"scope_type": "inline",
		"slot_id":    "nonexistent_slot",
		"content":    "修正内容",
	})
	if wBadSlot.Code != http.StatusBadRequest {
		t.Fatalf("不存在的 slot_id 应返回 400，实际 %d: %s", wBadSlot.Code, wBadSlot.Body.String())
	}
	wLongQuote := doAdminDocTypeReq(router, http.MethodPost, baseURL, orderTok, map[string]any{
		"scope_type": "inline",
		"slot_id":    "overview_boundaries",
		"anchor":     map[string]any{"quote": strings.Repeat("字", 501)},
		"content":    "超长摘录",
	})
	if wLongQuote.Code != http.StatusBadRequest {
		t.Fatalf("quote 超过 500 字符应返回 400，实际 %d: %s", wLongQuote.Code, wLongQuote.Body.String())
	}

	// 3. 连续创建 #1、#2，删除 #1 后再创建 #3 -> 校验单调递增且旧号永不复用
	createComment := func(payload map[string]any) services.ReviewCommentVO {
		w := doAdminDocTypeReq(router, http.MethodPost, baseURL, orderTok, payload)
		if w.Code != http.StatusCreated {
			t.Fatalf("创建批注失败 (%d): %s", w.Code, w.Body.String())
		}
		var vo services.ReviewCommentVO
		if err := json.Unmarshal(w.Body.Bytes(), &vo); err != nil {
			t.Fatal(err)
		}
		return vo
	}

	c1 := createComment(map[string]any{
		"scope_type": "inline",
		"slot_id":    "overview_boundaries",
		"anchor":     map[string]any{"block_index": 0, "block_type": "paragraph", "quote": "概览正文"},
		"category":   "concise",
		"content":    "精简概览措辞",
	})
	c2 := createComment(map[string]any{
		"scope_type":        "global",
		"category":          "global_rule",
		"content":           "全文避免罗列 LOC 统计",
		"persist_for_regen": true,
	})
	if c1.CommentNo != 1 || c2.CommentNo != 2 {
		t.Fatalf("期望 comment_no 为 1 与 2，实际 %d 与 %d", c1.CommentNo, c2.CommentNo)
	}

	// 删除 #1
	wDel := doAdminDocTypeReq(router, http.MethodDelete, fmt.Sprintf("%s/%d", baseURL, c1.ID), orderTok, nil)
	if wDel.Code != http.StatusOK {
		t.Fatalf("删除草稿批注 #1 失败 (%d): %s", wDel.Code, wDel.Body.String())
	}

	// 再新建应分配 #3（不复用 #1，既有 #2 不跳号）
	c3 := createComment(map[string]any{
		"scope_type": "section",
		"slot_id":    "runtime_concurrency",
		"category":   "supplement",
		"content":    "补充分片锁说明",
	})
	if c3.CommentNo != 3 {
		t.Fatalf("删除 #1 后新建批注应分配 #3，实际 #%d", c3.CommentNo)
	}

	// 4. PATCH 更新正文、persist_for_regen 与状态
	wPatch := doAdminDocTypeReq(router, http.MethodPatch, fmt.Sprintf("%s/%d", baseURL, c3.ID), orderTok, map[string]any{
		"content":           "补充读写锁与分片锁说明",
		"persist_for_regen": true,
		"status":            "resolved",
	})
	if wPatch.Code != http.StatusOK {
		t.Fatalf("更新批注失败 (%d): %s", wPatch.Code, wPatch.Body.String())
	}
	var updatedC3 services.ReviewCommentVO
	_ = json.Unmarshal(wPatch.Body.Bytes(), &updatedC3)
	if updatedC3.Content != "补充读写锁与分片锁说明" || !updatedC3.PersistForRegen || updatedC3.Status != models.ReviewStatusResolved {
		t.Fatalf("更新后字段不符预期: %+v", updatedC3)
	}

	// 5. GET 列表与筛选
	wList := doAdminDocTypeReq(router, http.MethodGet, baseURL+"?persist_for_regen=true", orderTok, nil)
	if wList.Code != http.StatusOK {
		t.Fatalf("查询批注列表失败: %d", wList.Code)
	}
	var listRes struct {
		Items []services.ReviewCommentVO `json:"items"`
	}
	_ = json.Unmarshal(wList.Body.Bytes(), &listRes)
	if len(listRes.Items) != 2 || listRes.Items[0].CommentNo != 2 || listRes.Items[1].CommentNo != 3 {
		t.Fatalf("筛选与排序结果异常: %+v", listRes.Items)
	}
}
