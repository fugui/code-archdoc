package verifier_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/refdoc"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/verifier"
)

func buildSampleIR() *codeir.CodeIRBundle {
	return &codeir.CodeIRBundle{
		SchemaVersion: codeir.SchemaVersion,
		Tree: codeir.TreeFacet{
			TotalFiles: 2,
			Files: []codeir.FileSummary{
				{RelPath: "src/order/order_service.cpp", Language: "cpp", LOC: 120},
				{RelPath: "include/order/order_service.h", Language: "cpp", LOC: 60},
			},
		},
		Symbols: codeir.SymbolsFacet{
			Symbols: []codeir.SymbolFact{
				{
					FQSymbol: "order::OrderService",
					Kind:     "class",
					Language: "cpp",
					Meta: codeir.FactMeta{
						Confidence:         codeir.ConfidenceExact,
						Span:               codeir.SourceSpan{FilePath: "include/order/order_service.h", LineStart: 20, LineEnd: 55},
						ContentFingerprint: "sha256:1111111111111111",
					},
				},
				{
					FQSymbol: "order::OrderService::Pay",
					Kind:     "method",
					Language: "cpp",
					Meta: codeir.FactMeta{
						Confidence:         codeir.ConfidenceExact,
						Span:               codeir.SourceSpan{FilePath: "src/order/order_service.cpp", LineStart: 42, LineEnd: 65},
						ContentFingerprint: "sha256:2222222222222222",
					},
				},
			},
		},
	}
}

func TestGate1UnverifiedPathAndSymbol(t *testing.T) {
	v := verifier.New()
	res := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots: []doctype.SlotSpec{
			{SlotID: "core_request_flows", Title: "Core", Ownership: "auto", Required: true, MinChars: 10},
		},
		Sections: []verifier.SectionInput{
			{
				SlotID: "core_request_flows",
				Markdown: "真实路径 `src/order/order_service.cpp` 与真实方法 `order::OrderService::Pay` 负责核心支付，" +
					"同时错误编造了 `src/fake_engine.cpp` 与伪造类 `FakeOrderMgr`。",
				CitedAnchors: []verifier.CitedAnchorCandidate{
					{FilePath: "src/order/order_service.cpp", FQSymbol: "order::OrderService::Pay"},
				},
			},
		},
	})

	unverifiedVals := make(map[string]string)
	for _, u := range res.Report.UnverifiedRefs {
		unverifiedVals[u.Value] = u.RefKind
	}

	if unverifiedVals["src/fake_engine.cpp"] != "file_path" {
		t.Errorf("伪造路径 src/fake_engine.cpp 未进入 unverified_refs: %+v", res.Report.UnverifiedRefs)
	}
	if unverifiedVals["FakeOrderMgr"] != "symbol" {
		t.Errorf("伪造类 FakeOrderMgr 未进入 unverified_refs: %+v", res.Report.UnverifiedRefs)
	}
	if _, bad := unverifiedVals["src/order/order_service.cpp"]; bad {
		t.Errorf("真实路径 src/order/order_service.cpp 不应被标为 unverified")
	}
	if _, bad := unverifiedVals["order::OrderService::Pay"]; bad {
		t.Errorf("真实符号 order::OrderService::Pay 不应被标为 unverified")
	}

	// 验证对齐后的 EvidenceAnchor 完整符合 ADR-03
	anchors := res.SectionEvidences["core_request_flows"]
	foundPay := false
	for _, a := range anchors {
		if a.FQSymbol == "order::OrderService::Pay" && a.AnchorKey == "cpp_func::src/order/order_service.cpp::order::OrderService::Pay" && a.LineStart == 42 {
			foundPay = true
		}
	}
	if !foundPay {
		t.Errorf("未对齐规范 ADR-03 EvidenceAnchor: %+v", anchors)
	}
}

func TestGate2RequiredSlotCoverage(t *testing.T) {
	v := verifier.New()
	slots := []doctype.SlotSpec{
		{SlotID: "overview_boundaries", Title: "1. 边界", Ownership: "auto", Required: true, MinChars: 20},
		{SlotID: "runtime_concurrency", Title: "2. 并发", Ownership: "auto", Required: true, MinChars: 20},
		{SlotID: "adr_constraints_debt", Title: "3. ADR", Ownership: "locked", Required: false, MinChars: 20},
	}

	res := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots:     slots,
		Sections: []verifier.SectionInput{
			{
				SlotID:   "overview_boundaries",
				Markdown: "本章节详细描述了模块边界与核心服务拆分结构，满足最小有效字符数要求。",
			},
			{
				SlotID:   "runtime_concurrency",
				Markdown: "<!-- slot:runtime_concurrency -->\nTODO",
			},
		},
	})

	if res.Report.SlotCoverageRate >= 1.0 || res.Report.SlotCoverageRate != 0.5 {
		t.Fatalf("期望 slot_coverage_rate == 0.5，实际 %v", res.Report.SlotCoverageRate)
	}
	if len(res.Report.MissingRequiredSlots) != 1 || res.Report.MissingRequiredSlots[0] != "runtime_concurrency" {
		t.Fatalf("期望 missing_required_slots == [runtime_concurrency]，实际 %+v", res.Report.MissingRequiredSlots)
	}
	if res.Report.Passed {
		t.Fatal("必填槽位缺失时 Passed 应为 false")
	}
}

func TestGate3MermaidSyntaxAndComplexity(t *testing.T) {
	v := verifier.New()
	slot := []doctype.SlotSpec{{SlotID: "s1", Title: "Diagram", Ownership: "auto", Required: true, MinChars: 10}}

	// 1. 合法流程图 -> 通过
	validMD := "正常架构图说明如下：\n```mermaid\nflowchart LR\n    A[\"入口\"] --> B[\"核心服务\"]\n    B --> C[\"存储\"]\n```\n"
	resValid := v.Verify(verifier.VerifyInput{
		IR:       buildSampleIR(),
		Slots:    slot,
		Sections: []verifier.SectionInput{{SlotID: "s1", Markdown: validMD}},
	})
	if len(resValid.Report.MermaidIssues) != 0 || !resValid.Report.Passed {
		t.Fatalf("合法 Mermaid 不应报错: %+v", resValid.Report.MermaidIssues)
	}

	// 2. 非白名单图 (gantt) -> mermaid_disallowed_type
	ganttMD := "非法甘特图说明如下：\n```mermaid\ngantt\n    title Plan\n    section S1\n    Task1 :a1, 2026-01-01, 5d\n```\n"
	resGantt := v.Verify(verifier.VerifyInput{
		IR:       buildSampleIR(),
		Slots:    slot,
		Sections: []verifier.SectionInput{{SlotID: "s1", Markdown: ganttMD}},
	})
	if len(resGantt.Report.MermaidIssues) != 1 || resGantt.Report.MermaidIssues[0].IssueCode != "mermaid_disallowed_type" {
		t.Fatalf("期望 mermaid_disallowed_type，实际: %+v", resGantt.Report.MermaidIssues)
	}

	// 3. 括号未闭合语法错图 -> mermaid_syntax_error
	brokenMD := "语法错误图说明如下：\n```mermaid\nflowchart TD\n    A[未闭合节点 --> B[正常节点]\n```\n"
	resBroken := v.Verify(verifier.VerifyInput{
		IR:       buildSampleIR(),
		Slots:    slot,
		Sections: []verifier.SectionInput{{SlotID: "s1", Markdown: brokenMD}},
	})
	if len(resBroken.Report.MermaidIssues) != 1 || resBroken.Report.MermaidIssues[0].IssueCode != "mermaid_syntax_error" {
		t.Fatalf("期望 mermaid_syntax_error，实际: %+v", resBroken.Report.MermaidIssues)
	}
	if resBroken.Report.CanApprove {
		t.Fatal("Mermaid 语法错误应阻止发布 (can_approve=false)")
	}

	// 4. 40 节点超限图 -> mermaid_complexity_warning
	var bigDiagram strings.Builder
	bigDiagram.WriteString("超大图说明如下：\n```mermaid\nflowchart LR\n")
	for i := 1; i <= 40; i++ {
		bigDiagram.WriteString(fmt.Sprintf("    N%d[\"Node %d\"] --> N%d[\"Node %d\"]\n", i, i, i+1, i+1))
	}
	bigDiagram.WriteString("```\n")
	resBig := v.Verify(verifier.VerifyInput{
		IR:       buildSampleIR(),
		Slots:    slot,
		Sections: []verifier.SectionInput{{SlotID: "s1", Markdown: bigDiagram.String()}},
	})
	if len(resBig.Report.MermaidIssues) != 1 || resBig.Report.MermaidIssues[0].IssueCode != "mermaid_complexity_warning" {
		t.Fatalf("期望 mermaid_complexity_warning，实际: %+v", resBig.Report.MermaidIssues)
	}
	if resBig.Report.MermaidIssues[0].NodeCount <= 35 {
		t.Fatalf("期望统计节点数 > 35，实际 %d", resBig.Report.MermaidIssues[0].NodeCount)
	}
}

func TestGate4SecretLeakBlocker(t *testing.T) {
	v := verifier.New()
	slot := []doctype.SlotSpec{{SlotID: "cfg", Title: "Config", Ownership: "auto", Required: true, MinChars: 10}}

	cases := []struct {
		name      string
		md        string
		plaintext string
	}{
		{
			name:      "plaintext password assignment",
			md:        "数据库连接配置示例：`db_password = \"MyProdPass123!\"`，请妥善保管并配置环境变量。",
			plaintext: "MyProdPass123!",
		},
		{
			name:      "PEM private key",
			md:        "证书私钥片段如下：\n```pem\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234567890\n-----END RSA PRIVATE KEY-----\n```",
			plaintext: "MIIEpAIBAAKCAQEA1234567890",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := v.Verify(verifier.VerifyInput{
				IR:       buildSampleIR(),
				Slots:    slot,
				Sections: []verifier.SectionInput{{SlotID: "cfg", Markdown: tc.md}},
			})
			if res.Report.BlockerCount < 1 {
				t.Fatalf("期望 blocker_count >= 1，实际 %+v", res.Report)
			}
			if res.Report.CanApprove {
				t.Fatalf("命中凭据泄漏时 can_approve 必须为 false")
			}
			for _, sf := range res.Report.SecretFindings {
				if strings.Contains(sf.MatchedMask, tc.plaintext) {
					t.Fatalf("SecretFinding.MatchedMask 严禁包含完整明文: %q", sf.MatchedMask)
				}
			}
		})
	}
}

var reSlotMarker = regexp.MustCompile(`<!--\s*slot:([a-z][a-z0-9_]{1,48})\s*-->`)

// TestEvalGoldenSamplesVerifierBaseline 作为 make eval-baseline 的组成部分，校验 5 类 Golden Sample 对齐 cpp_order_service IR。
func TestEvalGoldenSamplesVerifierBaseline(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	fixtureDir := filepath.Join(root, "testdata", "golden", "fixtures", "cpp_order_service")
	samplesDir := filepath.Join(root, "testdata", "golden", "samples")

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	irBundle, err := agg.ExtractDirectory(context.Background(), fixtureDir, "", sandbox.WalkOptions{})
	if err != nil {
		t.Fatalf("抽取 cpp_order_service 失败: %v", err)
	}

	regDir := t.TempDir()
	reg, err := doctype.NewRegistry(regDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SeedBuiltin(); err != nil {
		t.Fatal(err)
	}

	v := verifier.New()
	for _, slug := range doctype.BuiltinSlugs {
		asset, err := reg.LoadOne(slug)
		if err != nil {
			t.Fatalf("加载内置 DocType %s 失败: %v", slug, err)
		}
		sampleBytes, err := os.ReadFile(filepath.Join(samplesDir, slug+".golden.md"))
		if err != nil {
			t.Fatalf("读取 Golden Sample %s 失败: %v", slug, err)
		}

		sections := parseGoldenSections(string(sampleBytes))
		res := v.Verify(verifier.VerifyInput{
			CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			IR:        irBundle,
			Slots:     asset.Meta.Slots,
			Policy:    asset.Meta.VerifyPolicy,
			Sections:  sections,
		})

		if res.Report.SlotCoverageRate != 1.0 {
			t.Errorf("[%s] Golden Sample 必填槽位覆盖率必须为 100%%，实际 %.2f (missing=%v)",
				slug, res.Report.SlotCoverageRate*100, res.Report.MissingRequiredSlots)
		}
		if res.Report.BlockerCount != 0 || res.Report.ErrorCount != 0 || !res.Report.CanApprove {
			t.Errorf("[%s] Golden Sample Verifier 未通过: report=%+v", slug, res.Report)
		}
		if len(res.Report.UnverifiedRefs) != 0 {
			t.Errorf("[%s] Golden Sample 出现未验证符号/路径: %+v", slug, res.Report.UnverifiedRefs)
		}
	}
}

func parseGoldenSections(md string) []verifier.SectionInput {
	locs := reSlotMarker.FindAllStringSubmatchIndex(md, -1)
	if len(locs) == 0 {
		return nil
	}
	var out []verifier.SectionInput
	for i, loc := range locs {
		slotID := md[loc[2]:loc[3]]
		bodyStart := loc[1]
		bodyEnd := len(md)
		if i+1 < len(locs) {
			// 截断至下一章节标题 ## 之前
			nextChunk := md[bodyStart:locs[i+1][0]]
			if hIdx := strings.LastIndex(nextChunk, "\n## "); hIdx >= 0 {
				bodyEnd = bodyStart + hIdx
			} else {
				bodyEnd = locs[i+1][0]
			}
		}
		out = append(out, verifier.SectionInput{
			SlotID:   slotID,
			Markdown: strings.TrimSpace(md[bodyStart:bodyEnd]),
		})
	}
	return out
}

func TestVerifierRefDocCitationAlignmentAndStaleCompat_T_ADR10_09(t *testing.T) {
	v := verifier.New()
	res := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots: []doctype.SlotSpec{
			{SlotID: "overview", Title: "概览", Ownership: "auto", Required: true, MinChars: 10},
		},
		ActiveRefDocs: []verifier.RefDocSpec{{
			DocID:      12,
			Title:      "交易域总设",
			VersionNo:  2,
			ScopeLevel: "subsystem",
			TOC: []refdoc.HeadingNode{
				{SectionID: "sec-3", Title: "交易边界", Level: 2, StartLine: 10, EndLine: 30},
			},
		}},
		HistoricalRefDocs: []verifier.RefDocSpec{{
			DocID:      9,
			Title:      "已解绑旧文档",
			VersionNo:  1,
			ScopeLevel: "group",
			TOC: []refdoc.HeadingNode{
				{SectionID: "sec-1", Title: "历史约定", Level: 2, StartLine: 1, EndLine: 15},
			},
		}},
		Sections: []verifier.SectionInput{{
			SlotID:   "overview",
			Markdown: "调用 `src/order/order_service.cpp` 实现交易边界。",
			CitedAnchors: []verifier.CitedAnchorCandidate{
				{FilePath: "src/order/order_service.cpp", FQSymbol: "order::OrderService::Pay"},
				{Scope: "ref_doc", FilePath: "ref_doc:12", FQSymbol: "sec-3", SymbolKind: "ref_doc"},
				{Scope: "ref_doc", FilePath: "ref_doc:9", FQSymbol: "sec-1", SymbolKind: "ref_doc"},
			},
		}},
	})

	// 1. 合法与历史引用进入 SectionRefCitations，零污染 SectionEvidences
	for _, ev := range res.SectionEvidences["overview"] {
		if ev.Scope == "ref_doc" || ev.SymbolKind == "ref_doc" || strings.HasPrefix(ev.FilePath, "ref_doc:") {
			t.Fatalf("SectionEvidences 不应混入 ref_doc 锚点，实际: %+v", ev)
		}
	}
	cits := res.SectionRefCitations["overview"]
	if len(cits) != 2 {
		t.Fatalf("期望对齐 2 条 RefDocCitation，实际 %d: %+v", len(cits), cits)
	}
	if cits[0].CitationKey != "ref_doc:12@v2#sec-3" || cits[0].Stale {
		t.Fatalf("活跃参考引用对齐异常: %+v", cits[0])
	}
	if cits[1].CitationKey != "ref_doc:9@v1#sec-1" || !cits[1].Stale {
		t.Fatalf("历史已解绑引用应保留并标 stale=true: %+v", cits[1])
	}
	if !res.Report.CanApprove || res.Report.ErrorCount != 0 {
		t.Fatalf("历史解绑引用不应阻断审批: report=%+v", res.Report)
	}

	// 2. 伪造 ref_doc:9999 必须标 error 阻断 can_approve
	resFake := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots: []doctype.SlotSpec{
			{SlotID: "overview", Title: "概览", Ownership: "auto", Required: true, MinChars: 10},
		},
		Sections: []verifier.SectionInput{{
			SlotID:   "overview",
			Markdown: "引用伪造参考文档。",
			CitedAnchors: []verifier.CitedAnchorCandidate{
				{Scope: "ref_doc", FilePath: "ref_doc:9999", FQSymbol: "sec-1", SymbolKind: "ref_doc"},
			},
		}},
	})
	if resFake.Report.CanApprove || resFake.Report.ErrorCount == 0 {
		t.Fatalf("伪造 ref_doc:9999 必须阻断 can_approve: %+v", resFake.Report)
	}
	foundRefErr := false
	for _, u := range resFake.Report.UnverifiedRefs {
		if u.RefKind == "ref_doc" && u.Severity == "error" {
			foundRefErr = true
		}
	}
	if !foundRefErr {
		t.Fatalf("期望记录 RefKind=ref_doc Severity=error，实际: %+v", resFake.Report.UnverifiedRefs)
	}
}

func TestVerifierDeclaredTermsWarningDowngradeAndLegacyClassBlock_T_ADR10_10(t *testing.T) {
	v := verifier.New()
	res := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots: []doctype.SlotSpec{
			{SlotID: "overview", Title: "概览", Ownership: "auto", Required: true, MinChars: 10},
		},
		Policy: &doctype.VerifyPolicy{StrictSymbolCheck: true},
		ActiveRefDocs: []verifier.RefDocSpec{{
			DocID:         15,
			Title:         "领域术语表",
			VersionNo:     1,
			ScopeLevel:    "system",
			DeclaredTerms: []string{"RiskCenter"},
		}},
		Sections: []verifier.SectionInput{{
			SlotID:   "overview",
			Markdown: "本模块对接外部 `RiskCenter` 平台，未移除旧类 `LegacyOrderMgr`。",
		}},
	})

	if res.Report.CanApprove {
		t.Fatalf("StrictSymbolCheck 下过时类名 LegacyOrderMgr 仍必须阻断审批")
	}
	var gotLegacyErr, gotExtWarn bool
	for _, u := range res.Report.UnverifiedRefs {
		if u.Value == "LegacyOrderMgr" && u.RefKind == "symbol" && u.Severity == "error" {
			gotLegacyErr = true
		}
		if u.Value == "RiskCenter" && u.RefKind == "external_term" && u.Severity == "warning" {
			gotExtWarn = true
		}
	}
	if !gotLegacyErr || !gotExtWarn {
		t.Fatalf("期望 LegacyOrderMgr=error 且 RiskCenter=external_term/warning，实际: %+v", res.Report.UnverifiedRefs)
	}

	// 仅包含 DeclaredTerms 外部名词时，即使 StrictSymbolCheck=true 亦允许审批
	resExtOnly := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IR:        buildSampleIR(),
		Slots: []doctype.SlotSpec{
			{SlotID: "overview", Title: "概览", Ownership: "auto", Required: true, MinChars: 10},
		},
		Policy: &doctype.VerifyPolicy{StrictSymbolCheck: true},
		ActiveRefDocs: []verifier.RefDocSpec{{
			DocID:         15,
			Title:         "领域术语表",
			VersionNo:     1,
			ScopeLevel:    "system",
			DeclaredTerms: []string{"RiskCenter"},
		}},
		Sections: []verifier.SectionInput{{
			SlotID:   "overview",
			Markdown: "调用 `src/order/order_service.cpp` 并通知外部系统 `RiskCenter`。",
		}},
	})
	if !resExtOnly.Report.CanApprove || resExtOnly.Report.WarningCount == 0 {
		t.Fatalf("仅命中 DeclaredTerms 外部术语应降级为 warning 且不阻断审批: %+v", resExtOnly.Report)
	}
}
