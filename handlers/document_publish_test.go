package handlers_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"code-archdoc/pkg/publisher"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type m3TestEnv struct {
	db         *gorm.DB
	router     *gin.Engine
	revSvc     *services.RevisionService
	publishSvc *services.PublishService
	owner      models.User
	ownerTok   string
	normalUser models.User
	normalTok  string
	repo       models.Repository
	doc        *models.Document
	publishDir string
	sandboxDir string
}

func setupM3TestEnv(t *testing.T) *m3TestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	config.AppConfig.Auth.JWTSecret = "m3-unit-test-jwt-secret-key-0123456789"
	config.AppConfig.Auth.StandaloneMode = true

	tmpData := t.TempDir()
	docTypesDir := filepath.Join(tmpData, "doc_types")
	irCacheDir := filepath.Join(tmpData, "ir_cache")
	sandboxDir := filepath.Join(tmpData, "sandbox_dir")
	publishDir := filepath.Join(tmpData, "publish_dir")

	reg, err := doctype.NewRegistry(docTypesDir)
	if err != nil {
		t.Fatal(err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(irCacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	publishSink, err := publisher.NewLocalFSPublishSink(publishDir)
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
	docTypeSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	snapSvc := services.NewSnapshotService(snapStore, facetStore, agg, nil, sandboxDir, 1024*1024)
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, nil, agg, sandboxDir)
	publishSvc := services.NewPublishService(docStore, dtStore, masterStore, govStore, publishSink)

	commonAudit.Init(db)
	t.Cleanup(func() {
		_ = commonAudit.Close(context.Background())
	})

	router := gin.New()
	router.Use(commonAudit.Middleware(config.ModuleKey))
	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, nil).
		WithDocumentServices(revSvc, previewSvc, publishSvc)
	appHandlers.RegisterRoutes(router, db)

	dept := models.Department{Name: "核心交易研发部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := models.User{
		Username:     "repo_owner_m3",
		Name:         "仓库负责人",
		Email:        "owner_m3@example.com",
		DepartmentID: &dept.ID,
		Roles:        datatypes.JSON(`["user"]`),
		IsActive:     true,
	}
	normalUser := models.User{
		Username:     "normal_reader_m3",
		Name:         "普通只读用户",
		Email:        "reader_m3@example.com",
		DepartmentID: &dept.ID,
		Roles:        datatypes.JSON(`["user"]`),
		IsActive:     true,
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&normalUser).Error; err != nil {
		t.Fatal(err)
	}

	repo := models.Repository{
		Name:         "order-service",
		URL:          "https://git.internal.local/team/order-service.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      owner.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	// 准备本地沙箱源码文件以支撑 source-preview 测试
	repoSandbox := filepath.Join(sandboxDir, fmt.Sprintf("repo_%d", repo.ID))
	srcDir := filepath.Join(repoSandbox, "src", "order")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sampleCpp := `#include <mutex>
namespace order {
class OrderService {
public:
    int Pay(long order_id) {
        std::lock_guard<std::mutex> lk(mu_);
        return order_id > 0 ? 0 : -1;
    }
private:
    std::mutex mu_;
};
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "order_service.cpp"), []byte(sampleCpp), 0o644); err != nil {
		t.Fatal(err)
	}

	_, dtRev, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}
	snap := &models.RepoSnapshot{
		RepoID:          repo.ID,
		Subdirectory:    "",
		RefName:         "master",
		CommitSHA:       "a1b2c3d4e5f6789012345678901234567890abcd",
		IRSchemaVersion: "1.0",
		Status:          "ready",
	}
	if err := snapStore.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	doc, err := docStore.GetOrCreateDocument(ctx, &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "order-service 系统架构与核心链路",
		Status:            models.DocStatusDraft,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 生成可审批的初始 AI 草稿 Rev 1（包含有效字符长度与可引用证据）
	_, err = revSvc.RegenerateWithMerge(ctx, services.RegenerateMergeInput{
		DocID:           doc.ID,
		SnapshotID:      snap.ID,
		CommitSHA:       snap.CommitSHA,
		DocTypeRevision: dtRev,
		Generated: map[string]services.GeneratedSlotPayload{
			"overview_boundaries":  {Markdown: "REV1_PUBLISHED_CONTENT 系统定位与分层架构完整说明，包含网关层、订单核心领域层与数据库仓储层边界以及模块间依赖规则。"},
			"runtime_concurrency":  {Markdown: "运行时并发模型完整说明：采用有界工作线程池、分段互斥锁与无锁队列保护热点订单状态机，彻底防止多线程并发数据竞争问题。"},
			"core_request_flows":   {Markdown: "端到端核心链路说明：支付请求进入接入网关后依次执行分布式幂等键校验、库存预扣与交易流水落库，并触发异步回调通知下游。"},
			"fault_tolerance_deps": {Markdown: "外部依赖与容错矩阵说明：所有下游风控与账务调用均配置严格超时预算、滑动窗口熔断器、重试退避与有损降级兜底策略。"},
			"adr_constraints_debt": {Markdown: "架构约束与已知技术债说明：订单状态机严禁绕过领域服务直改数据库，所有跨服务写操作必须遵循幂等契约。"},
		},
		Source:        models.RevSourceAIGenerate,
		ChangeSummary: "初始 AI 生成 Rev 1",
		OperatorID:    owner.ID,
	})
	if err != nil {
		t.Fatalf("初始化测试草稿 Rev 1 失败: %v", err)
	}

	ownerTok := issueTestToken(t, owner)
	normalTok := issueTestToken(t, normalUser)

	return &m3TestEnv{
		db:         db,
		router:     router,
		revSvc:     revSvc,
		publishSvc: publishSvc,
		owner:      owner,
		ownerTok:   ownerTok,
		normalUser: normalUser,
		normalTok:  normalTok,
		repo:       repo,
		doc:        doc,
		publishDir: publishDir,
		sandboxDir: sandboxDir,
	}
}

func doM3JSONRequest(router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// 1. TestDraftIsolationForNormalUser (F-11 / F-13)
func TestDraftIsolationForNormalUser(t *testing.T) {
	env := setupM3TestEnv(t)

	// 1a. 草稿态：普通用户访问详情、修订历史、单篇导出、源码预览均返 403 DRAFT_ACCESS_DENIED
	for _, endpoint := range []string{
		fmt.Sprintf("/api/documents/%d", env.doc.ID),
		fmt.Sprintf("/api/documents/%d/revisions", env.doc.ID),
		fmt.Sprintf("/api/documents/%d/export", env.doc.ID),
		fmt.Sprintf("/api/documents/%d/source-preview?file=src/order/order_service.cpp", env.doc.ID),
	} {
		w := doM3JSONRequest(env.router, http.MethodGet, endpoint, env.normalTok, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("草稿态普通用户访问 %s 期望 403，实际 %d: %s", endpoint, w.Code, w.Body.String())
		}
		var errBody map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &errBody)
		if errBody["code"] != handlers.ErrCodeDraftAccessDenied {
			t.Fatalf("访问 %s 期望 code=DRAFT_ACCESS_DENIED，实际 %v", endpoint, errBody["code"])
		}
	}

	// 普通用户列表过滤草稿
	listNormal := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents?repo_id=%d", env.repo.ID), env.normalTok, nil)
	if listNormal.Code != http.StatusOK {
		t.Fatalf("普通用户查询文档列表失败: %d %s", listNormal.Code, listNormal.Body.String())
	}
	var listResp services.DocumentListVO
	_ = json.Unmarshal(listNormal.Body.Bytes(), &listResp)
	if len(listResp.Items) != 0 || listResp.CanWrite {
		t.Fatalf("草稿态普通用户文档列表应为空且 can_write=false，实际 %+v", listResp)
	}

	// 1b. 负责人发布 Rev 1
	pubW := doM3JSONRequest(env.router, http.MethodPost, fmt.Sprintf("/api/documents/%d/publish", env.doc.ID), env.ownerTok, map[string]any{})
	if pubW.Code != http.StatusOK {
		t.Fatalf("负责人发布 Rev 1 失败: %d %s", pubW.Code, pubW.Body.String())
	}

	// 发布后普通用户可正常只读访问详情与导出
	getAfterPub := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents/%d", env.doc.ID), env.normalTok, nil)
	if getAfterPub.Code != http.StatusOK {
		t.Fatalf("发布后普通用户访问文档期望 200，实际 %d: %s", getAfterPub.Code, getAfterPub.Body.String())
	}
	var pubDetail services.DocumentDetailVO
	_ = json.Unmarshal(getAfterPub.Body.Bytes(), &pubDetail)
	if pubDetail.ViewingRevision.RevisionNo != 1 || pubDetail.CanWrite {
		t.Fatalf("发布后普通用户应看到 Rev 1 且 can_write=false，实际 rev=%d can_write=%v", pubDetail.ViewingRevision.RevisionNo, pubDetail.CanWrite)
	}

	expAfterPub := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents/%d/export", env.doc.ID), env.normalTok, nil)
	if expAfterPub.Code != http.StatusOK || !strings.Contains(expAfterPub.Body.String(), "REV1_PUBLISHED_CONTENT") {
		t.Fatalf("发布后普通用户导出应成功且包含 Rev 1 内容，实际 %d", expAfterPub.Code)
	}

	// 1c. 负责人继续手工编辑生成未发布草稿 Rev 2
	editW := doM3JSONRequest(env.router, http.MethodPut, fmt.Sprintf("/api/documents/%d/sections/overview_boundaries", env.doc.ID), env.ownerTok, map[string]any{
		"base_revision_no": 1,
		"markdown":         "REV2_UNPUBLISHED_SECRET_DRAFT 未发布的新草稿内容，仅负责人可见，普通用户绝不可读取此段文本。",
		"change_summary":   "编辑未发布草稿 Rev 2",
	})
	if editW.Code != http.StatusOK {
		t.Fatalf("负责人编辑 Rev 2 失败: %d %s", editW.Code, editW.Body.String())
	}

	// 即使普通用户显式传 ?revision_no=2，服务端仍严格只返回已发布 Rev 1 快照！
	normalViewAgain := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents/%d?revision_no=2", env.doc.ID), env.normalTok, nil)
	if normalViewAgain.Code != http.StatusOK {
		t.Fatalf("普通用户查看已发布文档失败: %d", normalViewAgain.Code)
	}
	if strings.Contains(normalViewAgain.Body.String(), "REV2_UNPUBLISHED_SECRET_DRAFT") {
		t.Fatalf("严重安全漏洞：普通用户泄露了未发布草稿 Rev 2 内容！")
	}
	if !strings.Contains(normalViewAgain.Body.String(), "REV1_PUBLISHED_CONTENT") {
		t.Fatalf("普通用户应严格看到已发布 Rev 1 内容")
	}

	// 普通用户访问修订历史仍返 403
	revListNormal := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents/%d/revisions", env.doc.ID), env.normalTok, nil)
	if revListNormal.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问修订历史期望 403，实际 %d", revListNormal.Code)
	}
}

// 2. TestSourcePreviewPathEscapeReturns403 (SC-06)
func TestSourcePreviewPathEscapeReturns403(t *testing.T) {
	env := setupM3TestEnv(t)

	// 在沙箱外创建敏感目标文件，并在仓库内创建指向它的恶意符号链接
	outsideFile := filepath.Join(t.TempDir(), "outside_secret.txt")
	if err := os.WriteFile(outsideFile, []byte("TOP_SECRET_HOST_FILE"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(env.sandboxDir, fmt.Sprintf("repo_%d", env.repo.ID), "src", "order", "evil_symlink.cpp")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Fatalf("创建测试符号链接失败: %v", err)
	}

	// 2a. 合法源码文件读取 + 按符号重定位应返回 200
	okW := doM3JSONRequest(env.router, http.MethodGet,
		fmt.Sprintf("/api/documents/%d/source-preview?file=src/order/order_service.cpp&symbol=order::OrderService::Pay", env.doc.ID),
		env.ownerTok, nil)
	if okW.Code != http.StatusOK {
		t.Fatalf("合法源码预览期望 200，实际 %d: %s", okW.Code, okW.Body.String())
	}
	var preview services.SourcePreviewVO
	_ = json.Unmarshal(okW.Body.Bytes(), &preview)
	if preview.Language != "cpp" || !strings.Contains(preview.Content, "OrderService") || preview.HighlightStart <= 0 {
		t.Fatalf("源码预览内容或符号定位不符合预期: %+v", preview)
	}

	// 2b. 目录穿越 ../../../etc/passwd -> 必须返 403 PATH_ESCAPE
	travW := doM3JSONRequest(env.router, http.MethodGet,
		fmt.Sprintf("/api/documents/%d/source-preview?file=../../../etc/passwd", env.doc.ID),
		env.ownerTok, nil)
	if travW.Code != http.StatusForbidden {
		t.Fatalf("目录穿越期望 403，实际 %d: %s", travW.Code, travW.Body.String())
	}
	var travErr map[string]any
	_ = json.Unmarshal(travW.Body.Bytes(), &travErr)
	if travErr["code"] != handlers.ErrCodePathEscape {
		t.Fatalf("目录穿越期望 code=PATH_ESCAPE，实际 %v", travErr["code"])
	}

	// 2c. 沙箱内指向外部文件的符号链接 -> 必须返 403 PATH_ESCAPE 且严禁泄露内容
	symW := doM3JSONRequest(env.router, http.MethodGet,
		fmt.Sprintf("/api/documents/%d/source-preview?file=src/order/evil_symlink.cpp", env.doc.ID),
		env.ownerTok, nil)
	if symW.Code != http.StatusForbidden {
		t.Fatalf("外部符号链接逃逸期望 403，实际 %d: %s", symW.Code, symW.Body.String())
	}
	if strings.Contains(symW.Body.String(), "TOP_SECRET_HOST_FILE") {
		t.Fatalf("严重安全漏洞：符号链接泄露了沙箱外文件内容！")
	}
	var symErr map[string]any
	_ = json.Unmarshal(symW.Body.Bytes(), &symErr)
	if symErr["code"] != handlers.ErrCodePathEscape {
		t.Fatalf("外部符号链接逃逸期望 code=PATH_ESCAPE，实际 %v", symErr["code"])
	}
}

// 3. TestPublishBlockedByVerifierSecretLeak (SC-04)
func TestPublishBlockedByVerifierSecretLeak(t *testing.T) {
	env := setupM3TestEnv(t)

	// 负责人向草稿写入明文口令 -> Verifier 自动标 can_approve=false, blocker_count>0
	editW := doM3JSONRequest(env.router, http.MethodPut,
		fmt.Sprintf("/api/documents/%d/sections/fault_tolerance_deps", env.doc.ID),
		env.ownerTok,
		map[string]any{
			"base_revision_no": 1,
			"markdown":         "容错配置说明：db_password=ProdSecretPassword987654! 严禁允许该含明文口令草稿发布。",
			"change_summary":   "注入测试用明文口令",
		},
	)
	if editW.Code != http.StatusOK {
		t.Fatalf("编辑草稿失败: %d %s", editW.Code, editW.Body.String())
	}

	// 尝试审批发布 -> 必须硬拦截返 422 VERIFIER_BLOCKED
	pubW := doM3JSONRequest(env.router, http.MethodPost,
		fmt.Sprintf("/api/documents/%d/publish", env.doc.ID),
		env.ownerTok, map[string]any{})
	if pubW.Code != http.StatusUnprocessableEntity {
		t.Fatalf("含明文口令草稿发布期望 422，实际 %d: %s", pubW.Code, pubW.Body.String())
	}
	var errResp map[string]any
	_ = json.Unmarshal(pubW.Body.Bytes(), &errResp)
	if errResp["code"] != handlers.ErrCodeVerifierBlocked {
		t.Fatalf("期望 code=VERIFIER_BLOCKED，实际 %v", errResp["code"])
	}

	// 断言 publish_dir 中无任何 Markdown 文件生成
	var mdFiles []string
	_ = filepath.WalkDir(env.publishDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			mdFiles = append(mdFiles, p)
		}
		return nil
	})
	if len(mdFiles) != 0 {
		t.Fatalf("Verifier 阻断发布时 publish_dir 严禁生成任何文件，实际发现: %v", mdFiles)
	}
}

// 4. TestPublishAndUnpublishEndToEnd (SC-07 / SC-10)
func TestPublishAndUnpublishEndToEnd(t *testing.T) {
	env := setupM3TestEnv(t)

	// 先手工微调一章，以便发布时产生 >0 的 edit_diff_ratio 质量指标
	editW := doM3JSONRequest(env.router, http.MethodPut,
		fmt.Sprintf("/api/documents/%d/sections/core_request_flows", env.doc.ID),
		env.ownerTok,
		map[string]any{
			"base_revision_no": 1,
			"markdown":         "端到端核心链路增强说明：新增分布式幂等校验、事务发件箱异步投递与对账补偿闭环流程描述。",
			"ownership":        models.OwnershipHybrid,
			"change_summary":   "定稿前补充幂等与对账补偿链路",
		},
	)
	if editW.Code != http.StatusOK {
		t.Fatalf("手工微调章节失败: %d %s", editW.Code, editW.Body.String())
	}

	// 审批发布
	pubW := doM3JSONRequest(env.router, http.MethodPost,
		fmt.Sprintf("/api/documents/%d/publish", env.doc.ID),
		env.ownerTok, map[string]any{})
	if pubW.Code != http.StatusOK {
		t.Fatalf("正常发布失败: %d %s", pubW.Code, pubW.Body.String())
	}
	var pubRes services.PublishResultVO
	_ = json.Unmarshal(pubW.Body.Bytes(), &pubRes)
	if pubRes.Status != models.DocStatusApproved || pubRes.PublishedRevisionNo != 2 || pubRes.PublishedRelPath == "" {
		t.Fatalf("发布响应异常: %+v", pubRes)
	}

	// 验证磁盘文件存在且含 YAML Frontmatter
	publishedDiskPath := filepath.Join(env.publishDir, filepath.FromSlash(pubRes.PublishedRelPath))
	diskBytes, err := os.ReadFile(publishedDiskPath)
	if err != nil {
		t.Fatalf("期望磁盘发布文件存在 (%s): %v", publishedDiskPath, err)
	}
	if !strings.HasPrefix(string(diskBytes), "---\n") || !strings.Contains(string(diskBytes), `source_module: "archdoc"`) {
		t.Fatalf("发布文件缺少标准 YAML Frontmatter:\n%s", string(diskBytes))
	}

	// 验证 archdoc_quality_metrics 记录了 doc_published 修改率
	var metrics []models.QualityMetric
	if err := env.db.Where("document_id = ? AND metric_event = ?", env.doc.ID, models.MetricEventDocPublished).Find(&metrics).Error; err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].EditDiffRatio <= 0 {
		t.Fatalf("期望记录 1 条 edit_diff_ratio > 0 的 doc_published 指标，实际 %+v", metrics)
	}

	// 验证 POST /api/exports/zip 正常返回含 INDEX.md 与已发布文档的合法 ZIP
	zipW := doM3JSONRequest(env.router, http.MethodPost, "/api/exports/zip", env.normalTok, map[string]any{
		"repo_ids": []uint{env.repo.ID},
	})
	if zipW.Code != http.StatusOK {
		t.Fatalf("ZIP 导出失败: %d %s", zipW.Code, zipW.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(zipW.Body.Bytes()), int64(zipW.Body.Len()))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("ZIP 内容异常: err=%v files=%d", err, len(zr.File))
	}

	// 一键撤回
	unpubW := doM3JSONRequest(env.router, http.MethodPost,
		fmt.Sprintf("/api/documents/%d/unpublish", env.doc.ID),
		env.ownerTok, map[string]any{})
	if unpubW.Code != http.StatusOK {
		t.Fatalf("撤回发布失败: %d %s", unpubW.Code, unpubW.Body.String())
	}

	// 验证磁盘文件与空仓库目录均已消失
	if _, statErr := os.Stat(publishedDiskPath); !os.IsNotExist(statErr) {
		t.Fatalf("撤回后磁盘发布文件应消失")
	}
	if _, statErr := os.Stat(filepath.Join(env.publishDir, "order-service")); !os.IsNotExist(statErr) {
		t.Fatalf("撤回最后一篇文档后空仓库目录应被彻底清理")
	}

	// 撤回后普通用户再次访问文档返回 403
	afterUnpubNormal := doM3JSONRequest(env.router, http.MethodGet, fmt.Sprintf("/api/documents/%d", env.doc.ID), env.normalTok, nil)
	if afterUnpubNormal.Code != http.StatusForbidden {
		t.Fatalf("撤回后普通用户访问应返回 403，实际 %d", afterUnpubNormal.Code)
	}

	// 等待异步审计引擎落库并断言 audit_logs 中记录了 publish 与 unpublish (P0)
	_ = commonAudit.Close(context.Background())
	time.Sleep(50 * time.Millisecond)

	var auditLogs []commonModels.SysAuditLog
	if err := env.db.Where("module = ? AND target_type = ?", config.ModuleKey, "document").Find(&auditLogs).Error; err != nil {
		t.Fatal(err)
	}
	hasPublish, hasUnpublish := false, false
	for _, al := range auditLogs {
		if al.Action == "publish" && al.Level == commonModels.AuditLevelP0 {
			hasPublish = true
		}
		if al.Action == "unpublish" && al.Level == commonModels.AuditLevelP0 {
			hasUnpublish = true
		}
	}
	if !hasPublish || !hasUnpublish {
		t.Fatalf("期望 audit_logs 记录 P0 级 publish 与 unpublish 审计日志，实际 records=%+v", auditLogs)
	}
}

// 5. TestDocumentRevisionEndpointsAndRBAC 验证所有权切换、Diff、回滚、乐观锁 409 与普通用户写操作 403 拦截
func TestDocumentRevisionEndpointsAndRBAC(t *testing.T) {
	env := setupM3TestEnv(t)

	// 普通用户尝试写接口均返 403 FORBIDDEN
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPut, fmt.Sprintf("/api/documents/%d/sections/overview_boundaries", env.doc.ID), map[string]any{"base_revision_no": 1, "markdown": "hack"}},
		{http.MethodPatch, fmt.Sprintf("/api/documents/%d/sections/overview_boundaries/ownership", env.doc.ID), map[string]any{"ownership": "locked"}},
		{http.MethodPost, fmt.Sprintf("/api/documents/%d/rollback", env.doc.ID), map[string]any{"base_revision_no": 1, "target_revision_no": 1}},
		{http.MethodPost, fmt.Sprintf("/api/documents/%d/publish", env.doc.ID), map[string]any{}},
	} {
		w := doM3JSONRequest(env.router, tc.method, tc.path, env.normalTok, tc.body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("普通用户写接口 %s %s 期望 403，实际 %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 负责人切换所有权为 locked
	ownW := doM3JSONRequest(env.router, http.MethodPatch,
		fmt.Sprintf("/api/documents/%d/sections/adr_constraints_debt/ownership", env.doc.ID),
		env.ownerTok, map[string]any{"ownership": "locked", "clear_drift_review": true})
	if ownW.Code != http.StatusOK {
		t.Fatalf("切换所有权失败: %d %s", ownW.Code, ownW.Body.String())
	}
	var secVO services.SectionVO
	_ = json.Unmarshal(ownW.Body.Bytes(), &secVO)
	if secVO.Ownership != models.OwnershipLocked {
		t.Fatalf("期望 ownership=locked，实际 %s", secVO.Ownership)
	}

	// 编辑生成 Rev 2
	editW := doM3JSONRequest(env.router, http.MethodPut,
		fmt.Sprintf("/api/documents/%d/sections/overview_boundaries", env.doc.ID),
		env.ownerTok, map[string]any{
			"base_revision_no": 1,
			"markdown":         "REV2_UPDATED 系统定位与分层架构更新说明，补充风控与对账服务边界定义及完整职责说明。",
			"change_summary":   "更新第 1 章",
		})
	if editW.Code != http.StatusOK {
		t.Fatalf("编辑 Rev 2 失败: %d %s", editW.Code, editW.Body.String())
	}

	// 使用过期 base_revision_no=1 再次编辑 -> 409 OPTIMISTIC_LOCK_CONFLICT + current_revision_no=2
	conflictW := doM3JSONRequest(env.router, http.MethodPut,
		fmt.Sprintf("/api/documents/%d/sections/overview_boundaries", env.doc.ID),
		env.ownerTok, map[string]any{
			"base_revision_no": 1,
			"markdown":         "stale write",
		})
	if conflictW.Code != http.StatusConflict {
		t.Fatalf("期望 409 乐观锁冲突，实际 %d: %s", conflictW.Code, conflictW.Body.String())
	}
	var cBody map[string]any
	_ = json.Unmarshal(conflictW.Body.Bytes(), &cBody)
	if cBody["code"] != handlers.ErrCodeOptimisticLockConflict {
		t.Fatalf("期望 code=OPTIMISTIC_LOCK_CONFLICT，实际 %v", cBody["code"])
	}

	// 对比 Rev 1 与 Rev 2 章节 Diff
	diffW := doM3JSONRequest(env.router, http.MethodGet,
		fmt.Sprintf("/api/documents/%d/revisions/diff?from_rev=1&to_rev=2", env.doc.ID),
		env.ownerTok, nil)
	if diffW.Code != http.StatusOK {
		t.Fatalf("查询修订 Diff 失败: %d %s", diffW.Code, diffW.Body.String())
	}
	var diffVO services.RevisionDiffVO
	_ = json.Unmarshal(diffW.Body.Bytes(), &diffVO)
	if len(diffVO.SectionDiffs) != 5 || !diffVO.SectionDiffs[0].Changed {
		t.Fatalf("Diff 结果异常: %+v", diffVO)
	}

	// 对非孤立章节调用 orphan-resolve -> 返回 400 INVALID_PARAM
	nonOrphanW := doM3JSONRequest(env.router, http.MethodPost,
		fmt.Sprintf("/api/documents/%d/sections/overview_boundaries/orphan-resolve", env.doc.ID),
		env.ownerTok, map[string]any{"base_revision_no": 2, "action": "delete"})
	if nonOrphanW.Code != http.StatusBadRequest {
		t.Fatalf("对非孤立章节调用 orphan-resolve 期望 400，实际 %d", nonOrphanW.Code)
	}

	// 回滚至 Rev 1 -> 生成 Rev 3
	rbW := doM3JSONRequest(env.router, http.MethodPost,
		fmt.Sprintf("/api/documents/%d/rollback", env.doc.ID),
		env.ownerTok, map[string]any{
			"base_revision_no":   2,
			"target_revision_no": 1,
			"change_summary":     "接口回滚至 Rev 1",
		})
	if rbW.Code != http.StatusOK {
		t.Fatalf("回滚失败: %d %s", rbW.Code, rbW.Body.String())
	}
	var rbDetail services.DocumentDetailVO
	_ = json.Unmarshal(rbW.Body.Bytes(), &rbDetail)
	if rbDetail.CurrentRevisionNo != 3 || rbDetail.ViewingRevision.Source != models.RevSourceRollback {
		t.Fatalf("回滚结果异常: rev=%d source=%s", rbDetail.CurrentRevisionNo, rbDetail.ViewingRevision.Source)
	}
}
