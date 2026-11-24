package services_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/publisher"
	"code-archdoc/services"
	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"
	"code-common/backend/testdb"

	"gorm.io/datatypes"
)

func TestPublishSourcePreviewAndDriftZeroRefCitationPollution_T_ADR10_11(t *testing.T) {
	ctx := context.Background()
	db := testdb.SetupIsolatedDB(t, config.ModuleKey)
	if err := models.SetupTestDB(t, db); err != nil {
		t.Fatalf("SetupTestDB 失败: %v", err)
	}

	dept := models.Department{Name: "发布隔离验证"}
	if err := db.Create(&dept).Error; err != nil {
		t.Fatal(err)
	}
	owner := commonModels.User{
		Username:     "pub_owner",
		Name:         "发布负责人",
		Email:        "pub_owner@example.com",
		DepartmentID: &dept.ID,
		Roles:        datatypes.JSON(`["user"]`),
		IsActive:     true,
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}

	cppSrc := "namespace demo {\nint RunJob(int id) {\n  return id + 1;\n}\n}\n"
	bareURL, _, commitSHA := createBareGitFixture(t, "ref-cite-pub", map[string]string{
		"src/job.cpp": cppSrc,
	})

	repo := commonModels.Repository{
		Name:         "ref-cite-service",
		URL:          bareURL,
		Branch:       "master",
		DepartmentID: dept.ID,
		OwnerID:      owner.ID,
		IsActive:     true,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "sandbox")
	publishDir := filepath.Join(tmpDir, "publish")
	if _, err := gitsync.Sync(ctx, gitsync.SyncOptions{
		RepoURL:   bareURL,
		LocalPath: filepath.Join(sandboxDir, "repo_"+itoaUint(repo.ID)),
		Branch:    "master",
	}); err != nil {
		t.Fatalf("同步本地沙箱失败: %v", err)
	}

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	syms := agg.ExtractSingleFileSymbols(ctx, "src/job.cpp", "cpp", []byte(cppSrc))
	var fp string
	for _, s := range syms {
		if s.FQSymbol == "demo::RunJob" {
			fp = s.Meta.ContentFingerprint
		}
	}
	if fp == "" {
		t.Fatalf("未能提取 demo::RunJob 符号指纹: %+v", syms)
	}

	reg, _ := doctype.NewRegistry(filepath.Join(tmpDir, "doc_types"))
	dtStore := models.NewDocTypeStore(db)
	dtSvc := services.NewDocTypeService(reg, dtStore)
	if _, err := dtSvc.SeedAndSyncAll(ctx); err != nil {
		t.Fatal(err)
	}
	_, dtRev, err := dtStore.GetBySlug(ctx, "system-architecture")
	if err != nil || dtRev == nil {
		t.Fatal(err)
	}

	docStore := models.NewDocumentStore(db)
	masterStore := models.NewSharedMasterStore(db)
	govStore := models.NewGovernanceStore(db)
	driftStore := models.NewDriftStore(db)
	rbacSvc := services.NewRBACService(masterStore)

	doc := &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      "",
		RefName:           "master",
		DocTypeSlug:       "system-architecture",
		DocTypeRevisionID: dtRev.ID,
		Title:             "ref-cite-service 系统架构",
		Status:            models.DocStatusDraft,
	}
	if _, err := docStore.GetOrCreateDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}

	snaps := []models.DocumentSectionSnapshot{
		{
			SlotID:        "system_context",
			SectionOrder:  1,
			Title:         "系统上下文",
			Ownership:     models.OwnershipAuto,
			Markdown:      "通过 `src/job.cpp` 执行作业。",
			ContentSHA256: "deadbeef",
		},
	}
	snapsJSON, _ := json.Marshal(snaps)
	vReportJSON, _ := json.Marshal(models.VerifierReport{Passed: true, CanApprove: true, SlotCoverageRate: 1})
	codeEvJSON, _ := json.Marshal([]models.EvidenceAnchor{
		{
			AnchorID:           "ev_runjob",
			AnchorKey:          "cpp_func::src/job.cpp::demo::RunJob",
			FilePath:           "src/job.cpp",
			SymbolKind:         "cpp_func",
			FQSymbol:           "demo::RunJob",
			ContentFingerprint: fp,
			CommitSHA:          commitSHA,
			LineStart:          2,
			LineEnd:            4,
			Confidence:         "exact",
		},
	})
	refCitJSON, _ := json.Marshal([]models.RefDocCitation{
		{
			CitationKey:  "ref_doc:88@v1#sec-1",
			DocID:        88,
			VersionNo:    1,
			SectionID:    "sec-1",
			SectionTitle: "目标定位",
			DocTitle:     "上位规划",
			ScopeLevel:   "system",
			Stale:        false,
		},
	})

	rev := &models.DocumentRevision{
		SnapshotID:         1,
		CommitSHA:          commitSHA,
		DocTypeRevisionID:  dtRev.ID,
		Source:             models.RevSourceAIGenerate,
		ChangeSummary:      "initial draft",
		SectionsJSON:       datatypes.JSON(snapsJSON),
		FullMarkdown:       "# ref-cite-service 系统架构\n\n## 系统上下文\n通过 `src/job.cpp` 执行作业。\n",
		VerifierPassed:     true,
		CanApprove:         true,
		SlotCoverageRate:   1,
		VerifierReportJSON: datatypes.JSON(vReportJSON),
		CreatedBy:          owner.ID,
	}
	evs := []models.SectionEvidence{
		{
			SlotID:              "system_context",
			SectionOrder:        1,
			Title:               "系统上下文",
			Ownership:           models.OwnershipAuto,
			ContentSHA256:       "deadbeef",
			VerifierSummaryJSON: datatypes.JSON(`{}`),
			EvidencesJSON:       datatypes.JSON(codeEvJSON),
			RefCitationsJSON:    datatypes.JSON(refCitJSON),
		},
	}
	if err := docStore.AppendRevisionWithOptimisticLock(ctx, doc, 0, rev, evs); err != nil {
		t.Fatal(err)
	}

	// 1. SourcePreviewService 不得被 ref_citations 污染，且 ref_doc:88 请求应被拦截/报错
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, services.DefaultGitSyncer{}, agg, sandboxDir)
	goodPrev, err := previewSvc.GetSourcePreview(ctx, services.SourcePreviewRequest{
		DocID:    doc.ID,
		File:     "src/job.cpp",
		Commit:   commitSHA,
		CanWrite: true,
	})
	if err != nil || !strings.Contains(goodPrev.Content, "RunJob") {
		t.Fatalf("正常源码预览失败: %+v err=%v", goodPrev, err)
	}
	if _, badErr := previewSvc.GetSourcePreview(ctx, services.SourcePreviewRequest{
		DocID:    doc.ID,
		File:     "ref_doc:88",
		Commit:   commitSHA,
		CanWrite: true,
	}); badErr == nil {
		t.Fatal("ref_doc:88 不应被 SourcePreviewService 当作真实仓内文件读取")
	}

	// 2. PublishService 落盘 Markdown 与索引不得混入 ref_doc 引用或 ref_citations YAML 字段
	sink, err := publisher.NewLocalFSPublishSink(publishDir)
	if err != nil {
		t.Fatal(err)
	}
	pubSvc := services.NewPublishService(docStore, dtStore, masterStore, govStore, sink)
	pubRes, err := pubSvc.PublishDocument(ctx, doc.ID, &rev.RevisionNo, &owner)
	if err != nil {
		t.Fatalf("PublishDocument 失败: %v", err)
	}
	pubBytes, err := os.ReadFile(filepath.Join(publishDir, pubRes.PublishedRelPath))
	if err != nil {
		t.Fatal(err)
	}
	pubText := string(pubBytes)
	if strings.Contains(pubText, "ref_doc:") || strings.Contains(pubText, "ref_citations") {
		t.Fatalf("PublishService 输出文件被参考文档引用污染:\n%s", pubText)
	}

	// 3. DriftOrchestrator 仅比对真实源码锚点，存在 ref_citations 时源码未变零漂移
	driftSvc := services.NewDriftOrchestrator(
		masterStore, docStore, dtStore, dtSvc, driftStore, rbacSvc, nil, agg, services.NewActiveLeaseManager(), sandboxDir, 15,
	)
	if err := driftSvc.EvaluateRepoCodeDrift(ctx, models.PollableRepoRecord{
		RepoID:  repo.ID,
		RepoURL: bareURL,
		Branch:  "master",
	}, commitSHA, "master"); err != nil {
		t.Fatalf("EvaluateRepoCodeDrift 失败: %v", err)
	}
	drifts, _ := driftStore.ListDriftsByDocIDs(ctx, []uint{doc.ID})
	for _, dr := range drifts {
		if dr.DriftType == models.DriftTypeCode && dr.HasDrift {
			t.Fatalf("源码未变更且含 ref_citations 时不应产生 Code Drift: %+v", dr)
		}
	}
}
