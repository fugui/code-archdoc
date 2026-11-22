package models

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"code-archdoc/config"
	"code-common/backend/testdb"

	"gorm.io/datatypes"
)

func TestAll20TablesPrefixAndAutoMigrate(t *testing.T) {
	ownModels := AllOwnModels()
	if len(ownModels) != 20 {
		t.Fatalf("期望自有 GORM 模型数量为 20，实际为 %d", len(ownModels))
	}

	seen := make(map[string]bool, len(ownModels))
	for _, m := range ownModels {
		tn, ok := m.(interface{ TableName() string })
		if !ok {
			t.Fatalf("模型 %T 未实现 TableName() string", m)
		}
		name := tn.TableName()
		if !strings.HasPrefix(name, config.TablePrefix) {
			t.Fatalf("模型 %T 表名 %q 未遵循前缀 %q", m, name, config.TablePrefix)
		}
		if seen[name] {
			t.Fatalf("检测到重复的表名定义: %q", name)
		}
		seen[name] = true
	}

	// 验证共享只读镜像表名
	if got := (ArchitectureElement{}).TableName(); got != "architecture_elements" {
		t.Fatalf("期望 ArchitectureElement 表名为 architecture_elements，实际为 %q", got)
	}

	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := SetupTestDB(t, db); err != nil {
		t.Fatalf("首次 SetupTestDB 失败: %v", err)
	}
	// 幂等执行第二次无报错
	if err := SetupTestDB(t, db); err != nil {
		t.Fatalf("第二次幂等 SetupTestDB 失败: %v", err)
	}

	// 确认全部 16 张自有表与 5 张共享只读测试表均已就绪
	for name := range seen {
		if !db.Migrator().HasTable(name) {
			t.Fatalf("期望隔离库存在表 %q，但未找到", name)
		}
	}
	for _, shared := range []string{"users", "departments", "repositories", "architecture_elements", (SysAuditLog{}).TableName()} {
		if !db.Migrator().HasTable(shared) {
			t.Fatalf("期望隔离库存在共享测试表 %q，但未找到", shared)
		}
	}
}

func TestEnumsAndTaskStateMachine(t *testing.T) {
	// 1. 合法枚举值校验
	if !IsValidFacetName(FacetIDLContracts) || !IsValidFacetName(FacetRuntimePrimitives) || IsValidFacetName("unknown_facet") {
		t.Fatal("FacetName 校验失败")
	}
	if !IsValidSectionOwnership(OwnershipAuto) || !IsValidSectionOwnership(OwnershipHybrid) || !IsValidSectionOwnership(OwnershipLocked) || IsValidSectionOwnership("readonly") {
		t.Fatal("SectionOwnership 校验失败")
	}
	if !IsValidDocumentStatus(DocStatusDraft) || !IsValidDocumentStatus(DocStatusApproved) || IsValidDocumentStatus("published") {
		t.Fatal("DocumentStatus 校验失败")
	}
	if !IsValidRevisionSource(RevSourceManualEdit) || !IsValidRevisionSource(RevSourceRollback) || IsValidRevisionSource("import") {
		t.Fatal("RevisionSource 校验失败")
	}
	if !IsValidTaskKind(TaskKindGenerate) || !IsValidTaskKind(TaskKindIncrementalRefresh) || !IsValidTaskKind(TaskKindBatch) || IsValidTaskKind("sync") {
		t.Fatal("TaskKind 校验失败")
	}
	if !IsValidFacetName(FacetModuleContext) {
		t.Fatal("FacetModuleContext 校验失败")
	}
	if !IsValidSymbolKind(SymbolKindModuleDoc) {
		t.Fatal("SymbolKindModuleDoc 校验失败")
	}
	if !IsValidModuleStrategy(ModuleStrategyMerged) || !IsValidModuleStrategy(ModuleStrategyArchElements) || !IsValidModuleStrategy(ModuleStrategyTopLevelDirs) || IsValidModuleStrategy("custom") {
		t.Fatal("ModuleStrategy 校验失败")
	}
	if !IsValidModuleStatus(ModuleStatusActive) || !IsValidModuleStatus(ModuleStatusMissing) || !IsValidModuleStatus(ModuleStatusIgnored) || IsValidModuleStatus("deleted") {
		t.Fatal("ModuleStatus 校验失败")
	}
	if !CanTransitionBatchTaskStatus(TaskStatusGenerating, TaskStatusReady) || CanTransitionBatchTaskStatus(TaskStatusReady, TaskStatusGenerating) {
		t.Fatal("BatchTaskStatus 转移校验失败")
	}
	if !IsValidTaskPriority(TaskPriorityInteractive) || !IsValidTaskPriority(TaskPriorityBatch) || IsValidTaskPriority("urgent") {
		t.Fatal("TaskPriority 校验失败")
	}
	if !IsValidTaskStatus(TaskStatusPending) || !IsValidTaskStatus(TaskStatusVerifying) || IsValidTaskStatus("running") {
		t.Fatal("TaskStatus 校验失败")
	}
	if !IsValidSlotTaskStatus(SlotTaskSkippedLocked) || IsValidSlotTaskStatus("paused") {
		t.Fatal("SlotTaskStatus 校验失败")
	}
	if !IsValidProposalStatus(ProposalStatusSuperseded) || IsValidProposalStatus("merged") {
		t.Fatal("ProposalStatus 校验失败")
	}
	if !IsValidDriftType(DriftTypeCode) || !IsValidDriftType(DriftTypeSpec) || IsValidDriftType("both") {
		t.Fatal("DriftType 校验失败")
	}
	if !IsValidDriftPrecision(DriftPrecisionSectionLevel) || !IsValidDriftPrecision(DriftPrecisionRepoLevel) || IsValidDriftPrecision("line_level") {
		t.Fatal("DriftPrecision 校验失败")
	}
	if !IsValidMetricEvent(MetricEventDocPublished) || IsValidMetricEvent("unknown") {
		t.Fatal("MetricEvent 校验失败")
	}
	if !IsValidSymbolKind(SymbolKindCppFunc) || !IsValidSymbolKind(SymbolKindProtoRPC) || IsValidSymbolKind("rust_trait") {
		t.Fatal("SymbolKind 校验失败")
	}
	if !IsValidReviewScopeType(ReviewScopeInline) || !IsValidReviewScopeType(ReviewScopeGlobal) || IsValidReviewScopeType("block") {
		t.Fatal("ReviewScopeType 校验失败")
	}
	if !IsValidReviewCategory(ReviewCategoryCorrection) || !IsValidReviewCategory(ReviewCategoryGlobalRule) || IsValidReviewCategory("style") {
		t.Fatal("ReviewCategory 校验失败")
	}
	if !IsValidReviewCommentStatus(ReviewStatusOpen) || !IsValidReviewCommentStatus(ReviewStatusProposed) || IsValidReviewCommentStatus("closed") {
		t.Fatal("ReviewCommentStatus 校验失败")
	}

	// 2. 任务状态机转移校验
	validTransitions := [][2]string{
		{TaskStatusPending, TaskStatusExtracting},
		{TaskStatusPending, TaskStatusCanceled},
		{TaskStatusPending, TaskStatusFailed},
		{TaskStatusExtracting, TaskStatusGenerating},
		{TaskStatusExtracting, TaskStatusFailed},
		{TaskStatusExtracting, TaskStatusCanceled},
		{TaskStatusGenerating, TaskStatusVerifying},
		{TaskStatusGenerating, TaskStatusFailed},
		{TaskStatusGenerating, TaskStatusCanceled},
		{TaskStatusVerifying, TaskStatusReady},
		{TaskStatusVerifying, TaskStatusFailed},
	}
	for _, pair := range validTransitions {
		if !CanTransitionTaskStatus(pair[0], pair[1]) {
			t.Errorf("期望合法状态转移 %s -> %s 被放行", pair[0], pair[1])
		}
	}

	invalidTransitions := [][2]string{
		{TaskStatusPending, TaskStatusGenerating},
		{TaskStatusPending, TaskStatusReady},
		{TaskStatusExtracting, TaskStatusReady},
		{TaskStatusVerifying, TaskStatusCanceled},
		{TaskStatusReady, TaskStatusPending},
		{TaskStatusReady, TaskStatusFailed},
		{TaskStatusFailed, TaskStatusPending},
		{TaskStatusCanceled, TaskStatusExtracting},
	}
	for _, pair := range invalidTransitions {
		if CanTransitionTaskStatus(pair[0], pair[1]) {
			t.Errorf("期望非法状态转移 %s -> %s 被拒绝", pair[0], pair[1])
		}
	}
}

func TestUniqueConstraintsAndIdempotentSeeds(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	// 1. 验证 LLMConfig 初始为空池（无 fast/flagship 空骨架行），且 Name 具备唯一约束
	var llmConfigs []LLMConfig
	if err := db.Find(&llmConfigs).Error; err != nil {
		t.Fatalf("查询 LLMConfig 失败: %v", err)
	}
	if len(llmConfigs) != 0 {
		t.Fatalf("期望新库默认空模型池（0 行），实际 %d 行", len(llmConfigs))
	}
	firstModel := LLMConfig{
		Name:      "DeepSeek-V3",
		BaseURL:   "http://10.0.0.10:8000/v1",
		ModelName: "deepseek-v3",
		IsActive:  true,
		IsDefault: true,
	}
	if err := db.Create(&firstModel).Error; err != nil {
		t.Fatalf("创建首个 LLMConfig 失败: %v", err)
	}
	if err := db.Create(&LLMConfig{Name: "DeepSeek-V3", BaseURL: "http://10.0.0.11:8000/v1", ModelName: "ds"}).Error; err == nil {
		t.Fatal("期望重复创建 name=DeepSeek-V3 触发唯一约束错误")
	}

	// 2. 验证 DocType.Slug 唯一约束
	dt1 := DocType{Slug: "system-architecture", DisplayName: "系统架构"}
	if err := db.Create(&dt1).Error; err != nil {
		t.Fatalf("创建 DocType 失败: %v", err)
	}
	if err := db.Create(&DocType{Slug: "system-architecture", DisplayName: "重复架构"}).Error; err == nil {
		t.Fatal("期望重复 DocType.Slug 触发唯一约束错误")
	}

	// 3. 验证 RepoSnapshot 五元组唯一约束
	now := time.Now()
	snap := RepoSnapshot{
		RepoID:          1,
		Subdirectory:    "svc/order",
		RefName:         "master",
		CommitSHA:       strings.Repeat("a", 40),
		IRSchemaVersion: "1.0",
		Status:          "ready",
		LastAccessedAt:  now,
	}
	if err := db.Create(&snap).Error; err != nil {
		t.Fatalf("创建 RepoSnapshot 失败: %v", err)
	}
	snapDup := snap
	snapDup.ID = 0
	if err := db.Create(&snapDup).Error; err == nil {
		t.Fatal("期望重复 RepoSnapshot 五元组触发唯一约束错误")
	}

	// 4. 验证 Document 四元组唯一约束与乐观锁 + DocType 删除保护
	doc := Document{
		RepoID:            1,
		Subdirectory:      "svc/order",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: 1,
		Title:             "order-service 系统架构",
		Status:            DocStatusDraft,
		CurrentRevisionNo: 0,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("创建 Document 失败: %v", err)
	}
	docDup := doc
	docDup.ID = 0
	if err := db.Create(&docDup).Error; err == nil {
		t.Fatal("期望重复 Document 四元组触发唯一约束错误")
	}

	// 验证 DocumentStore 乐观锁追加修订与冲突拦截
	docStore := NewDocumentStore(db)
	rev1 := &DocumentRevision{
		DocumentID:         doc.ID,
		RevisionNo:         1,
		SnapshotID:         snap.ID,
		CommitSHA:          snap.CommitSHA,
		DocTypeRevisionID:  1,
		Source:             RevSourceAIGenerate,
		SectionsJSON:       datatypes.JSON("[]"),
		FullMarkdown:       "# v1",
		VerifierReportJSON: datatypes.JSON("{}"),
		CreatedBy:          1,
	}
	if err := docStore.AppendRevisionWithOptimisticLock(context.Background(), &doc, 0, rev1, nil); err != nil {
		t.Fatalf("首次追加修订失败: %v", err)
	}
	// 用过期的 baseRevisionNo=0 再次写入应返回 ErrOptimisticLockConflict
	revConflict := &DocumentRevision{
		DocumentID:         doc.ID,
		RevisionNo:         2,
		SnapshotID:         snap.ID,
		CommitSHA:          snap.CommitSHA,
		DocTypeRevisionID:  1,
		Source:             RevSourceManualEdit,
		SectionsJSON:       datatypes.JSON("[]"),
		FullMarkdown:       "# v2",
		VerifierReportJSON: datatypes.JSON("{}"),
		CreatedBy:          1,
	}
	err := docStore.AppendRevisionWithOptimisticLock(context.Background(), &doc, 0, revConflict, nil)
	if !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("期望返回 ErrOptimisticLockConflict，实际返回: %v", err)
	}

	// 验证已发布文档引用保护：发布后调用 DocTypeStore.DeleteIfUnused 返回 ErrDocTypeInUse
	if err := docStore.MarkPublished(context.Background(), doc.ID, rev1.ID, snap.CommitSHA, "order/system-architecture.md", 1, now); err != nil {
		t.Fatalf("MarkPublished 失败: %v", err)
	}
	dtStore := NewDocTypeStore(db)
	if err := dtStore.DeleteIfUnused(context.Background(), "system-architecture"); !errors.Is(err, ErrDocTypeInUse) {
		t.Fatalf("期望被已发布文档引用的 DocType 删除返回 ErrDocTypeInUse，实际返回: %v", err)
	}
}

func TestMigrateLegacyInactiveLLMAndRepairInconsistentDefault(t *testing.T) {
	// 场景 A：历史表仅含 is_active = false 记录时，迁移不应将其强制标记为默认模型
	dbLegacy := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := EnsureSharedTablesForTest(dbLegacy); err != nil {
		t.Fatalf("初始化共享表失败: %v", err)
	}

	llmTable := (LLMConfig{}).TableName()
	createLegacySQL := `
		CREATE TABLE ` + llmTable + ` (
			id BIGSERIAL PRIMARY KEY,
			tier varchar(24) NOT NULL DEFAULT '',
			base_url varchar(512) NOT NULL DEFAULT '',
			model_name varchar(128) NOT NULL DEFAULT '',
			api_key_cipher text NOT NULL DEFAULT '',
			api_key_masked varchar(64) NOT NULL DEFAULT '',
			timeout_seconds bigint NOT NULL DEFAULT 180,
			max_retries bigint NOT NULL DEFAULT 3,
			max_tokens_per_task bigint NOT NULL DEFAULT 200000,
			max_files_per_task bigint NOT NULL DEFAULT 2000,
			is_active boolean NOT NULL DEFAULT true,
			last_probe_status varchar(20) DEFAULT 'unknown',
			last_probe_latency_ms bigint DEFAULT 0,
			last_probed_at timestamptz,
			updated_by bigint,
			created_at timestamptz,
			updated_at timestamptz
		);`
	if err := dbLegacy.Exec(createLegacySQL).Error; err != nil {
		t.Fatalf("构造历史表失败: %v", err)
	}
	if err := dbLegacy.Exec(
		`INSERT INTO `+llmTable+` (tier, base_url, model_name, api_key_cipher, api_key_masked, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
		"flagship", "https://api.deepseek.com", "deepseek-flash", "cipher-placeholder", "sk-****18c2", false,
	).Error; err != nil {
		t.Fatalf("插入历史停用模型失败: %v", err)
	}

	if err := SetupWithDB(dbLegacy); err != nil {
		t.Fatalf("SetupWithDB 迁移失败: %v", err)
	}

	var migrated LLMConfig
	if err := dbLegacy.First(&migrated).Error; err != nil {
		t.Fatalf("查询迁移后模型失败: %v", err)
	}
	if migrated.Name != "deepseek-flash" || migrated.IsActive || migrated.IsDefault || migrated.APIKeyMasked != "" {
		t.Fatalf("停用历史模型迁移结果异常（期望 is_default=false 且清空无效掩码）: %+v", migrated)
	}
	if dbLegacy.Migrator().HasColumn(&LLMConfig{}, "tier") || dbLegacy.Migrator().HasColumn(&LLMConfig{}, "api_key_cipher") {
		t.Fatal("迁移后不应保留历史 tier 或 api_key_cipher 列")
	}

	// 场景 B：启动时自动修复库中已存在的「is_default=true 且 is_active=false」异常记录，且不影响正常默认记录
	dbRepair := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := SetupTestDB(t, dbRepair); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	broken := LLMConfig{Name: "broken-inactive-default", BaseURL: "https://a.example.com/v1", ModelName: "m1"}
	normalInactive := LLMConfig{Name: "normal-inactive", BaseURL: "https://b.example.com/v1", ModelName: "m2"}
	normalActive := LLMConfig{Name: "normal-active", BaseURL: "https://c.example.com/v1", ModelName: "m3"}
	for _, item := range []*LLMConfig{&broken, &normalInactive, &normalActive} {
		if err := dbRepair.Create(item).Error; err != nil {
			t.Fatalf("插入测试模型失败: %v", err)
		}
	}
	if err := dbRepair.Model(&LLMConfig{}).Where("id = ?", broken.ID).Updates(map[string]any{"is_active": false, "is_default": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbRepair.Model(&LLMConfig{}).Where("id = ?", normalInactive.ID).Updates(map[string]any{"is_active": false, "is_default": false}).Error; err != nil {
		t.Fatal(err)
	}

	if err := SetupWithDB(dbRepair); err != nil {
		t.Fatalf("SetupWithDB 启动修复失败: %v", err)
	}

	var gotBroken, gotNormalInactive, gotNormalActive LLMConfig
	if err := dbRepair.First(&gotBroken, broken.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbRepair.First(&gotNormalInactive, normalInactive.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := dbRepair.First(&gotNormalActive, normalActive.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotBroken.IsDefault || gotBroken.IsActive {
		t.Fatalf("期望异常停用默认记录被清除 is_default 标记，实际 %+v", gotBroken)
	}
	if gotNormalInactive.IsDefault || gotNormalInactive.IsActive {
		t.Fatalf("普通停用记录状态不应变更: %+v", gotNormalInactive)
	}
	if gotNormalActive.IsDefault || !gotNormalActive.IsActive {
		t.Fatalf("正常非默认启用记录状态不应变更: %+v", gotNormalActive)
	}

	activeDefault := LLMConfig{Name: "active-default", BaseURL: "https://d.example.com/v1", ModelName: "m4", IsActive: true, IsDefault: true}
	if err := dbRepair.Create(&activeDefault).Error; err != nil {
		t.Fatal(err)
	}
	if err := SetupWithDB(dbRepair); err != nil {
		t.Fatalf("再次 SetupWithDB 失败: %v", err)
	}
	var gotActiveDefault LLMConfig
	if err := dbRepair.First(&gotActiveDefault, activeDefault.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !gotActiveDefault.IsDefault || !gotActiveDefault.IsActive {
		t.Fatalf("正常已启用默认模型不应被清除默认标记: %+v", gotActiveDefault)
	}
}
