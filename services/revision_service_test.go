package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	"code-common/backend/testdb"

	"gorm.io/datatypes"
)

type revisionTestFixture struct {
	ctx        context.Context
	revSvc     *services.RevisionService
	docStore   *models.DocumentStore
	dtStore    *models.DocTypeStore
	docTypeSvc *services.DocTypeService
	doc        *models.Document
	dtRev      *models.DocTypeRevision
	operatorID uint
}

func setupRevisionTestFixture(t *testing.T) *revisionTestFixture {
	t.Helper()
	ctx := context.Background()

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	dept := models.Department{Name: "架构平台部"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	user := models.User{
		Username:     "arch_owner",
		Name:         "架构负责人",
		Email:        "arch_owner@example.com",
		DepartmentID: &dept.ID,
		Roles:        datatypes.JSON(`["user"]`),
		IsActive:     true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	repo := models.Repository{
		Name:         "order-service",
		URL:          "https://git.internal.local/team/order-service.git",
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      user.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	reg, err := doctype.NewRegistry(filepath.Join(t.TempDir(), "doc_types"))
	if err != nil {
		t.Fatal(err)
	}
	dtStore := models.NewDocTypeStore(db)
	docTypeSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := docTypeSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatalf("SeedAndSyncAll 失败: %v", err)
	}

	_, dtRev, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil || dtRev == nil {
		t.Fatalf("获取内置 system-architecture DocType 失败: %v", err)
	}

	snapStore := models.NewSnapshotStore(db)
	snap := &models.RepoSnapshot{
		RepoID:          repo.ID,
		Subdirectory:    "",
		RefName:         "master",
		CommitSHA:       "1111111111111111111111111111111111111111",
		IRSchemaVersion: "1.0",
		Status:          "ready",
	}
	if err := snapStore.UpsertSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}

	docStore := models.NewDocumentStore(db)
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

	masterStore := models.NewSharedMasterStore(db)
	driftStore := models.NewDriftStore(db)
	revSvc := services.NewRevisionService(docStore, dtStore, masterStore, driftStore, nil)

	// 通过 RegenerateWithMerge 生成初始 5 槽位 Rev 1
	_, err = revSvc.RegenerateWithMerge(ctx, services.RegenerateMergeInput{
		DocID:           doc.ID,
		SnapshotID:      snap.ID,
		CommitSHA:       snap.CommitSHA,
		DocTypeRevision: dtRev,
		Generated: map[string]services.GeneratedSlotPayload{
			"overview_boundaries":  {Markdown: "第 1 章初始内容：系统定位与模块分层边界完整说明，涵盖网关层、领域服务层与持久化仓储层的职责边界和依赖方向。"},
			"runtime_concurrency":  {Markdown: "第 2 章初始内容：运行时线程池、工作队列与互斥锁并发模型详细描述，确保多线程访问订单状态表时的线程安全与低延迟。"},
			"core_request_flows":   {Markdown: "第 3 章初始内容：订单创建、库存预扣与支付回调的端到端核心业务链路说明，包含同步调用与异步状态推进的关键节点。"},
			"fault_tolerance_deps": {Markdown: "第 4 章初始内容：外部依赖拓扑、超时预算、熔断降级与重试幂等矩阵完整说明，明确下游异常时的故障影响面与兜底行为。"},
			"adr_constraints_debt": {Markdown: "第 5 章初始内容：默认架构约束与已知技术债占位说明，待研发负责人手工补充并锁定。"},
		},
		Source:        models.RevSourceAIGenerate,
		ChangeSummary: "初始 AI 生成 Rev 1",
		OperatorID:    user.ID,
	})
	if err != nil {
		t.Fatalf("初始化 Rev 1 失败: %v", err)
	}

	return &revisionTestFixture{
		ctx:        ctx,
		revSvc:     revSvc,
		docStore:   docStore,
		dtStore:    dtStore,
		docTypeSvc: docTypeSvc,
		doc:        doc,
		dtRev:      dtRev,
		operatorID: user.ID,
	}
}

// 1. TestManualEditByteLevelIsolationAndOptimisticLock (F-09 / SC-10)
func TestManualEditByteLevelIsolationAndOptimisticLock(t *testing.T) {
	f := setupRevisionTestFixture(t)

	rev1Detail, err := f.revSvc.GetDocumentDetail(f.ctx, f.doc.ID, nil, true)
	if err != nil {
		t.Fatalf("读取 Rev 1 失败: %v", err)
	}
	if rev1Detail.CurrentRevisionNo != 1 || len(rev1Detail.Sections) != 5 {
		t.Fatalf("期望 Rev 1 含 5 槽位，实际 rev=%d slots=%d", rev1Detail.CurrentRevisionNo, len(rev1Detail.Sections))
	}

	origSHA := make(map[string]string, 5)
	origMD := make(map[string]string, 5)
	for _, s := range rev1Detail.Sections {
		origSHA[s.SlotID] = s.ContentSHA256
		origMD[s.SlotID] = s.Markdown
	}

	// 修改第 2 槽位 (runtime_concurrency)
	newSlot2MD := "第 2 章手工直编内容：补充读写锁分段策略、热点账号分片锁与队列背压参数说明，严格遵循无明文凭据安全约束。"
	rev2Detail, err := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "runtime_concurrency",
		BaseRevisionNo: 1,
		Markdown:       newSlot2MD,
		Ownership:      models.OwnershipHybrid,
		ChangeSummary:  "手工补充并发锁策略",
		Source:         models.RevSourceManualEdit,
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("手工直编第 2 槽位失败: %v", err)
	}

	if rev2Detail.CurrentRevisionNo != 2 || rev2Detail.ViewingRevision.RevisionNo != 2 {
		t.Fatalf("期望修订号递增至 2，实际 current=%d viewing=%d", rev2Detail.CurrentRevisionNo, rev2Detail.ViewingRevision.RevisionNo)
	}
	if rev2Detail.ViewingRevision.Source != models.RevSourceManualEdit {
		t.Fatalf("期望 source=manual_edit，实际 %s", rev2Detail.ViewingRevision.Source)
	}
	if !rev2Detail.ViewingRevision.CanApprove {
		t.Fatalf("正常编辑后 Verifier 应通过，实际 report=%+v", rev2Detail.ViewingRevision.VerifierReport)
	}

	// 断言第 1/3/4/5 槽位 content_sha256 与 Markdown 逐字节完全不变
	for _, s := range rev2Detail.Sections {
		if s.SlotID == "runtime_concurrency" {
			if s.Markdown != newSlot2MD || s.ContentSHA256 == origSHA[s.SlotID] {
				t.Fatalf("目标槽位未更新: md=%q sha=%s", s.Markdown, s.ContentSHA256)
			}
			if !s.ManuallyEdited || s.Ownership != models.OwnershipHybrid {
				t.Fatalf("目标槽位应标记 manually_edited=true & ownership=hybrid，实际 %+v", s)
			}
		} else {
			if s.ContentSHA256 != origSHA[s.SlotID] || s.Markdown != origMD[s.SlotID] {
				t.Fatalf("非目标槽位 %s 发生字节级漂移: wantSHA=%s gotSHA=%s", s.SlotID, origSHA[s.SlotID], s.ContentSHA256)
			}
		}
	}

	// 使用过期 base_revision_no=1 再次提交 -> 必须返回 ErrOptimisticLockConflict (409)
	_, conflictErr := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "runtime_concurrency",
		BaseRevisionNo: 1,
		Markdown:       "并发过期写入",
		OperatorID:     f.operatorID,
	})
	if !errors.Is(conflictErr, models.ErrOptimisticLockConflict) {
		t.Fatalf("使用过期 base_revision_no 期望 ErrOptimisticLockConflict，实际 %v", conflictErr)
	}
	var lockErr *services.OptimisticLockConflictError
	if !errors.As(conflictErr, &lockErr) || lockErr.CurrentRevisionNo != 2 {
		t.Fatalf("乐观锁错误应携带服务端最新版本号 2，实际 %+v", conflictErr)
	}

	// 验证 Verifier 在手工直编时同步重跑：写入疑似明文口令应被 SecretGate 阻断
	rev3Detail, err := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "runtime_concurrency",
		BaseRevisionNo: 2,
		Markdown:       "配置示例：db_password=SuperSecretPassword123456! 仅测试检测门禁。",
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("编辑触发 Verifier 检测失败: %v", err)
	}
	if rev3Detail.ViewingRevision.CanApprove || rev3Detail.ViewingRevision.VerifierReport.BlockerCount == 0 {
		t.Fatalf("手工直编包含明文口令后 Verifier 应拦截 (can_approve=false)，实际 report=%+v", rev3Detail.ViewingRevision.VerifierReport)
	}
}

// 2. TestRollbackAppendsImmutableRevision (F-09)
func TestRollbackAppendsImmutableRevision(t *testing.T) {
	f := setupRevisionTestFixture(t)

	rev1, err := f.revSvc.GetDocumentDetail(f.ctx, f.doc.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}

	// Rev 2
	if _, err := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "overview_boundaries",
		BaseRevisionNo: 1,
		Markdown:       "Rev 2 的临时修改内容，长度满足必填槽位最低字符要求用于回滚测试。",
		OperatorID:     f.operatorID,
	}); err != nil {
		t.Fatal(err)
	}
	// Rev 3
	if _, err := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "core_request_flows",
		BaseRevisionNo: 2,
		Markdown:       "Rev 3 的误改内容，长度满足必填槽位最低字符要求即将回滚至 Rev 1。",
		OperatorID:     f.operatorID,
	}); err != nil {
		t.Fatal(err)
	}

	// 从 Rev 3 回滚至 Rev 1 -> 追加不可变 Rev 4
	rev4, err := f.revSvc.RollbackRevision(f.ctx, services.RollbackRevisionInput{
		DocID:            f.doc.ID,
		BaseRevisionNo:   3,
		TargetRevisionNo: 1,
		ChangeSummary:    "回滚误改至初始版本 Rev 1",
		OperatorID:       f.operatorID,
	})
	if err != nil {
		t.Fatalf("回滚至 Rev 1 失败: %v", err)
	}

	if rev4.CurrentRevisionNo != 4 || rev4.ViewingRevision.RevisionNo != 4 {
		t.Fatalf("期望生成不可变 Rev 4，实际 %d", rev4.CurrentRevisionNo)
	}
	if rev4.ViewingRevision.Source != models.RevSourceRollback {
		t.Fatalf("期望 source=rollback，实际 %s", rev4.ViewingRevision.Source)
	}
	if rev4.ViewingRevision.SourceRefID == nil || *rev4.ViewingRevision.SourceRefID != rev1.ViewingRevision.ID {
		t.Fatalf("期望 source_ref_id=%d，实际 %v", rev1.ViewingRevision.ID, rev4.ViewingRevision.SourceRefID)
	}

	// 验证 Rev 4 内容与 Rev 1 逐章节完全一致
	for i := range rev1.Sections {
		if rev4.Sections[i].ContentSHA256 != rev1.Sections[i].ContentSHA256 {
			t.Fatalf("回滚后章节 %s 哈希与 Rev 1 不一致", rev1.Sections[i].SlotID)
		}
	}

	// 验证历史 Rev 1~3 完好无损
	revs, err := f.revSvc.ListDocumentRevisions(f.ctx, f.doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 4 {
		t.Fatalf("期望保留全部 4 个不可变修订，实际 %d", len(revs))
	}
	// Diff Rev 3 vs Rev 4 应能清晰展示被回滚的差异
	diffVO, err := f.revSvc.GetRevisionDiff(f.ctx, f.doc.ID, 3, 4)
	if err != nil {
		t.Fatalf("计算 Rev 3->4 Diff 失败: %v", err)
	}
	changedSlots := 0
	for _, sd := range diffVO.SectionDiffs {
		if sd.Changed {
			changedSlots++
		}
	}
	if changedSlots != 2 {
		t.Fatalf("期望 Rev 3->4 有 2 个章节差异，实际 %d", changedSlots)
	}
}

// 3. TestRegeneratePreservesLockedAndCreatesOrphanOnSlotRemoval (F-10 / SC-10)
func TestRegeneratePreservesLockedAndCreatesOrphanOnSlotRemoval(t *testing.T) {
	f := setupRevisionTestFixture(t)

	// Step A: 将 adr_constraints_debt 设为 locked 并写入人工内容
	manualLockedMD := "【人工架构决议 ADR-99】严禁跨库分布式事务，订单支付必须走 TCC 最终一致性。"
	rev2, err := f.revSvc.ApplySectionChange(f.ctx, services.ApplySectionChangeInput{
		DocID:          f.doc.ID,
		SlotID:         "adr_constraints_debt",
		BaseRevisionNo: 1,
		Markdown:       manualLockedMD,
		Ownership:      models.OwnershipLocked,
		ChangeSummary:  "人工锁定架构约束章节",
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("写入锁定章节失败: %v", err)
	}
	var lockedSHA string
	for _, s := range rev2.Sections {
		if s.SlotID == "adr_constraints_debt" {
			lockedSHA = s.ContentSHA256
		}
	}

	// Step B: 模拟新提交触发重新生成 -> 断言 locked 原内容字节级保留且 needs_drift_review=true，auto 按新代码刷新
	rev3, err := f.revSvc.RegenerateWithMerge(f.ctx, services.RegenerateMergeInput{
		DocID:           f.doc.ID,
		SnapshotID:      rev2.ViewingRevision.SnapshotID,
		CommitSHA:       "2222222222222222222222222222222222222222",
		DocTypeRevision: f.dtRev,
		Generated: map[string]services.GeneratedSlotPayload{
			"overview_boundaries":  {Markdown: "第 1 章新提交刷新内容：已新增风控网关子模块边界描述。"},
			"runtime_concurrency":  {Markdown: "第 2 章新提交刷新内容：升级为无锁环形队列模型与协程池。"},
			"core_request_flows":   {Markdown: "第 3 章新提交刷新内容：核心链路新增风控前置校验节点。"},
			"fault_tolerance_deps": {Markdown: "第 4 章新提交刷新内容：补充风控服务超时降级策略。"},
			"adr_constraints_debt": {Markdown: "AI 试图覆盖的垃圾占位内容（绝不允许覆盖 locked 章节）"},
		},
		OperatorID: f.operatorID,
	})
	if err != nil {
		t.Fatalf("新提交重生成失败: %v", err)
	}

	secByID := make(map[string]services.SectionVO, len(rev3.Sections))
	for _, s := range rev3.Sections {
		secByID[s.SlotID] = s
	}
	if secByID["adr_constraints_debt"].Markdown != manualLockedMD || secByID["adr_constraints_debt"].ContentSHA256 != lockedSHA {
		t.Fatalf("locked 章节未被字节级保留: got=%q", secByID["adr_constraints_debt"].Markdown)
	}
	if !secByID["adr_constraints_debt"].NeedsDriftReview {
		t.Fatalf("新提交重生成后 locked 章节应标记 needs_drift_review=true")
	}
	if !strings.Contains(secByID["overview_boundaries"].Markdown, "风控网关") {
		t.Fatalf("auto 章节应按新代码刷新，实际=%q", secByID["overview_boundaries"].Markdown)
	}

	// Step C: 模拟新 DocTypeRevision 删除 adr_constraints_debt 槽位后重新生成 -> 原被锁内容以 is_orphan=true 挂载文末
	var newMeta doctype.Meta
	if err := json.Unmarshal(f.dtRev.MetaJSON, &newMeta); err != nil {
		t.Fatal(err)
	}
	filteredSlots := make([]doctype.SlotSpec, 0, len(newMeta.Slots)-1)
	for _, sl := range newMeta.Slots {
		if sl.SlotID != "adr_constraints_debt" {
			filteredSlots = append(filteredSlots, sl)
		}
	}
	newMeta.Slots = filteredSlots
	newMetaBytes, err := json.Marshal(newMeta)
	if err != nil {
		t.Fatal(err)
	}
	slotsSnapBytes, _ := json.Marshal(filteredSlots)
	dtModel, _, err := f.dtStore.GetBySlug(f.ctx, "system-architecture")
	if err != nil {
		t.Fatal(err)
	}
	newDTRev := &models.DocTypeRevision{
		MetaJSON:           datatypes.JSON(newMetaBytes),
		TemplateMD:         strings.ReplaceAll(f.dtRev.TemplateMD, "<!-- slot:adr_constraints_debt -->", ""),
		GenerationPromptMD: f.dtRev.GenerationPromptMD,
		RefinePromptMD:     f.dtRev.RefinePromptMD,
		SlotsSnapshotJSON:  datatypes.JSON(slotsSnapBytes),
		AggregateHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PromptHash:         f.dtRev.PromptHash,
	}
	created, err := f.dtStore.UpsertFromDirectorySync(f.ctx, dtModel, newDTRev)
	if err != nil || !created {
		t.Fatalf("创建删除槽位的新 DocTypeRevision 失败: created=%v err=%v", created, err)
	}

	rev4, err := f.revSvc.RegenerateWithMerge(f.ctx, services.RegenerateMergeInput{
		DocID:           f.doc.ID,
		SnapshotID:      rev3.ViewingRevision.SnapshotID,
		CommitSHA:       "3333333333333333333333333333333333333333",
		DocTypeRevision: newDTRev,
		Generated: map[string]services.GeneratedSlotPayload{
			"overview_boundaries":  {Markdown: "模板升级后第 1 章内容：结构精简后的模块分层说明。"},
			"runtime_concurrency":  {Markdown: "模板升级后第 2 章内容：最新线程与锁机制说明。"},
			"core_request_flows":   {Markdown: "模板升级后第 3 章内容：最新端到端业务链路说明。"},
			"fault_tolerance_deps": {Markdown: "模板升级后第 4 章内容：最新容错与故障影响面矩阵。"},
		},
		OperatorID: f.operatorID,
	})
	if err != nil {
		t.Fatalf("模板删槽位后重生成失败: %v", err)
	}
	if len(rev4.Sections) != 5 {
		t.Fatalf("期望 4 个新槽位 + 1 个孤立章节 = 5，实际 %d", len(rev4.Sections))
	}
	lastSec := rev4.Sections[len(rev4.Sections)-1]
	if lastSec.SlotID != "adr_constraints_debt" || !lastSec.IsOrphan || lastSec.Markdown != manualLockedMD || lastSec.ContentSHA256 != lockedSHA {
		t.Fatalf("被删槽位的 locked 内容应以 is_orphan=true 完整挂载于文末，实际 %+v", lastSec)
	}

	// Step D1: 测试 orphan-resolve 的 keep_locked 动作
	rev5Keep, err := f.revSvc.ResolveOrphanSection(f.ctx, services.ResolveOrphanInput{
		DocID:          f.doc.ID,
		SlotID:         "adr_constraints_debt",
		BaseRevisionNo: 4,
		Action:         "keep_locked",
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("orphan-resolve keep_locked 失败: %v", err)
	}
	kept := rev5Keep.Sections[len(rev5Keep.Sections)-1]
	if kept.IsOrphan || kept.Ownership != models.OwnershipLocked || kept.Markdown != manualLockedMD {
		t.Fatalf("keep_locked 后应转为常规 locked 章节且内容不变，实际 %+v", kept)
	}

	// 回滚至 Rev 4 以便继续验证 merge_into 动作（Rev 6 与 Rev 4 同样包含孤立章节）
	rev6, err := f.revSvc.RollbackRevision(f.ctx, services.RollbackRevisionInput{
		DocID:            f.doc.ID,
		BaseRevisionNo:   5,
		TargetRevisionNo: 4,
		OperatorID:       f.operatorID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step D2: 测试 orphan-resolve 的 merge_into 动作
	rev7Merge, err := f.revSvc.ResolveOrphanSection(f.ctx, services.ResolveOrphanInput{
		DocID:          f.doc.ID,
		SlotID:         "adr_constraints_debt",
		BaseRevisionNo: rev6.CurrentRevisionNo,
		Action:         "merge_into",
		TargetSlotID:   "fault_tolerance_deps",
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("orphan-resolve merge_into 失败: %v", err)
	}
	if len(rev7Merge.Sections) != 4 {
		t.Fatalf("merge_into 后孤立章节应被移除（剩 4 章），实际 %d", len(rev7Merge.Sections))
	}
	mergedTarget := ""
	for _, s := range rev7Merge.Sections {
		if s.SlotID == "fault_tolerance_deps" {
			mergedTarget = s.Markdown
		}
		if s.IsOrphan {
			t.Fatalf("merge_into 后不应再残留孤立章节: %+v", s)
		}
	}
	if !strings.Contains(mergedTarget, manualLockedMD) {
		t.Fatalf("merge_into 后目标章节未包含原孤立内容: %q", mergedTarget)
	}

	// 再回滚至 Rev 4 验证 delete 动作
	rev8, err := f.revSvc.RollbackRevision(f.ctx, services.RollbackRevisionInput{
		DocID:            f.doc.ID,
		BaseRevisionNo:   rev7Merge.CurrentRevisionNo,
		TargetRevisionNo: 4,
		OperatorID:       f.operatorID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step D3: 测试 orphan-resolve 的 delete 动作
	rev9Del, err := f.revSvc.ResolveOrphanSection(f.ctx, services.ResolveOrphanInput{
		DocID:          f.doc.ID,
		SlotID:         "adr_constraints_debt",
		BaseRevisionNo: rev8.CurrentRevisionNo,
		Action:         "delete",
		OperatorID:     f.operatorID,
	})
	if err != nil {
		t.Fatalf("orphan-resolve delete 失败: %v", err)
	}
	if len(rev9Del.Sections) != 4 {
		t.Fatalf("delete 后期望剩 4 章，实际 %d", len(rev9Del.Sections))
	}
	for _, s := range rev9Del.Sections {
		if s.SlotID == "adr_constraints_debt" || s.IsOrphan {
			t.Fatalf("delete 后不应再存在孤立章节: %+v", s)
		}
	}
}
