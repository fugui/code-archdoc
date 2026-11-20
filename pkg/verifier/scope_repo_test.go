package verifier_test

import (
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/verifier"
)

// TestVerifier_ScopeRepoWhitelistAndModuleDoc 验证 T-ADR04-07：
// 1. 位于 ModuleContextFacet 白名单内的 scope="repo" 引用对齐为 AnchorKey="repo::..."、Scope="repo"，通过门禁；
// 2. 不在 ModuleContextFacet 白名单内的越界 scope="repo" 引用：即使 StrictSymbolCheck=false，也无条件判为 error 并阻断 CanApprove=false；
// 3. 整仓汇总引用的 symbol_kind="module_doc" 与 ModuleDigests 对齐并绑定子模块摘要 ContentSHA256。
func TestVerifier_ScopeRepoWhitelistAndModuleDoc(t *testing.T) {
	ir := &codeir.CodeIRBundle{
		Tree: codeir.TreeFacet{
			Files: []codeir.FileSummary{
				{RelPath: "src/order_service.cpp", Language: "cpp", LOC: 100},
			},
		},
		Symbols: codeir.SymbolsFacet{
			Symbols: []codeir.SymbolFact{
				{
					FQSymbol: "order::OrderService::Pay",
					Kind:     "method",
					Language: "cpp",
					Meta: codeir.FactMeta{
						Confidence:         codeir.ConfidenceExact,
						Span:               codeir.SourceSpan{FilePath: "src/order_service.cpp", LineStart: 10, LineEnd: 30},
						ContentFingerprint: "sha256:order_pay_fp",
					},
				},
			},
		},
	}

	modCtx := &codeir.ModuleContextFacet{
		ModulePath: "order",
		ReferencedExtSymbols: []codeir.ExternalSymbolRef{
			{
				OwnerModule: "common",
				FilePath:    "common/include/types.h",
				SymbolKind:  "cpp_struct",
				FQSymbol:    "common::RpcResult",
				Signature:   "struct RpcResult",
				Span:        codeir.SourceSpan{FilePath: "common/include/types.h", LineStart: 5, LineEnd: 12},
				Fingerprint: "sha256:common_rpc_res",
				Confidence:  codeir.ConfidenceExact,
			},
		},
		ExternalFileWhitelist: []string{"common/include/types.h", "CMakeLists.txt"},
	}

	slots := []doctype.SlotSpec{
		{SlotID: "overview_boundaries", Title: "1. 概览", Ownership: "auto", Required: true, MinChars: 10},
	}
	policy := &doctype.VerifyPolicy{
		StrictSymbolCheck: false, // 故意关闭 strict，验证越界 scope="repo" 依然强制 error！
		RequireMermaid:    false,
	}

	v := verifier.New()

	// 场景 1：合法模块内锚点 + 合法白名单内 scope="repo" 锚点 -> Passed=true, CanApprove=true
	resOK := v.Verify(verifier.VerifyInput{
		CommitSHA:     "aaaa1111",
		IR:            ir,
		ModuleContext: modCtx,
		Slots:         slots,
		Policy:        policy,
		Sections: []verifier.SectionInput{
			{
				SlotID:   "overview_boundaries",
				Title:    "1. 概览",
				Markdown: "订单支付入口 `order::OrderService::Pay` 返回跨模块公共结构 `common::RpcResult`（定义于 `common/include/types.h`）。",
				CitedAnchors: []verifier.CitedAnchorCandidate{
					{FilePath: "src/order_service.cpp", FQSymbol: "order::OrderService::Pay"},
					{Scope: "repo", FilePath: "common/include/types.h", FQSymbol: "common::RpcResult"},
				},
			},
		},
	})
	if !resOK.Report.Passed || !resOK.Report.CanApprove || resOK.Report.ErrorCount != 0 {
		t.Fatalf("合法白名单 scope=repo 锚点应通过门禁，实际 report=%+v", resOK.Report)
	}
	evs := resOK.SectionEvidences["overview_boundaries"]
	foundRepoAnchor := false
	for _, a := range evs {
		if a.Scope == "repo" && a.FQSymbol == "common::RpcResult" && a.AnchorKey == "repo::cpp_struct::common/include/types.h::common::RpcResult" {
			foundRepoAnchor = true
		}
	}
	if !foundRepoAnchor {
		t.Fatalf("未生成预期的 repo:: 前缀跨模块证据锚点: %+v", evs)
	}

	// 场景 2：伪造不在 module_context 白名单内的 scope="repo" 引用 (secret_module/vault.h) -> 必须输出 error 并阻断 CanApprove=false
	resBad := v.Verify(verifier.VerifyInput{
		CommitSHA:     "aaaa1111",
		IR:            ir,
		ModuleContext: modCtx,
		Slots:         slots,
		Policy:        policy,
		Sections: []verifier.SectionInput{
			{
				SlotID:   "overview_boundaries",
				Title:    "1. 概览",
				Markdown: "伪造跨模块引用测试章节正文内容满足最小字数要求。",
				CitedAnchors: []verifier.CitedAnchorCandidate{
					{Scope: "repo", FilePath: "secret_module/vault.h", FQSymbol: "secret::VaultKey"},
				},
			},
		},
	})
	if resBad.Report.CanApprove || resBad.Report.ErrorCount == 0 {
		t.Fatalf("越界伪造的 scope=repo 锚点必须无条件触发 error 并阻断 CanApprove=false，实际 report=%+v", resBad.Report)
	}
	if len(resBad.Report.UnverifiedRefs) == 0 || resBad.Report.UnverifiedRefs[0].Severity != "error" {
		t.Fatalf("越界 scope=repo 的 UnverifiedRef.Severity 必须为 error，实际=%+v", resBad.Report.UnverifiedRefs)
	}

	// 场景 3：整仓汇总引用子模块架构摘要 (symbol_kind="module_doc")
	resRollup := v.Verify(verifier.VerifyInput{
		CommitSHA: "aaaa1111",
		IR:        ir,
		ModuleDigests: []codeir.ModuleDigestInput{
			{
				ModulePath:     "order",
				DisplayName:    "订单模块",
				Source:         "approved",
				OverviewDigest: "订单模块负责交易撮合与支付状态机",
				ContentSHA256:  "sha256_order_overview_slot",
			},
		},
		Slots:  slots,
		Policy: policy,
		Sections: []verifier.SectionInput{
			{
				SlotID:   "overview_boundaries",
				Title:    "1. 概览",
				Markdown: "整仓汇总：核心交易由订单模块负责，详见子模块架构。",
				CitedAnchors: []verifier.CitedAnchorCandidate{
					{SymbolKind: "module_doc", FilePath: "order", FQSymbol: "order::system-architecture"},
				},
			},
		},
	})
	if !resRollup.Report.CanApprove {
		t.Fatalf("合法 module_doc 锚点应通过门禁: %+v", resRollup.Report)
	}
	rollupEvs := resRollup.SectionEvidences["overview_boundaries"]
	if len(rollupEvs) != 1 || rollupEvs[0].SymbolKind != "module_doc" || rollupEvs[0].ContentFingerprint != "sha256_order_overview_slot" {
		t.Fatalf("module_doc 锚点指纹未正确绑定子模块摘要 SHA256: %+v", rollupEvs)
	}
}
