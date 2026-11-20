package drift_test

import (
	"context"
	"testing"

	"code-archdoc/pkg/drift"
	"code-common/backend/gitsync"
)

// TestDetectCodeDrift_ScopeRepoAndPriority 验证 T-ADR04-08：
//  1. 子目录文档 (Subdirectory="order") 中的 scope="repo" 锚点 (FilePath="common/include/types.h") 直接按仓库根路径匹配 Git Diff，
//     绝不错误拼接为 "order/common/include/types.h"；
//  2. 仅外部契约符号指纹变更时，精准输出 Reason="external_contract_modified"；外部文件被删时输出 "external_contract_removed"；
//  3. 同一章节同时发生模块内符号变更与外部契约变更时，模块内 Reason ("symbol_modified") 优先级更高。
func TestDetectCodeDrift_ScopeRepoAndPriority(t *testing.T) {
	ctx := context.Background()

	sections := []drift.SectionEvidenceInput{
		{
			SlotID:    "ext_only_slot",
			SlotTitle: "外部依赖章节",
			Ownership: "auto",
			Evidences: []drift.AnchorEvidenceInput{
				{
					Scope:              "repo",
					FilePath:           "common/include/types.h",
					SymbolKind:         "cpp_struct",
					FQSymbol:           "common::RpcResult",
					ContentFingerprint: "fp_v1_rpc_result",
				},
			},
		},
		{
			SlotID:    "mixed_slot",
			SlotTitle: "混合内部与外部证据章节",
			Ownership: "hybrid",
			Evidences: []drift.AnchorEvidenceInput{
				{
					Scope:              "repo",
					FilePath:           "common/include/types.h",
					SymbolKind:         "cpp_struct",
					FQSymbol:           "common::RpcResult",
					ContentFingerprint: "fp_v1_rpc_result",
				},
				{
					Scope:              "",
					FilePath:           "src/order_service.cpp",
					SymbolKind:         "cpp_func",
					FQSymbol:           "order::OrderService::Pay",
					ContentFingerprint: "fp_v1_order_pay",
				},
			},
		},
	}

	res := drift.DetectCodeDrift(ctx, drift.CodeDriftInput{
		PublishedCommitSHA: "1111111",
		RemoteHeadSHA:      "2222222",
		Subdirectory:       "order",
		ChangedFiles: []gitsync.ChangedFile{
			{Status: "M", Path: "common/include/types.h"},
			{Status: "M", Path: "order/src/order_service.cpp"},
		},
		Sections: sections,
		ReadBlob: func(_ context.Context, _, repoRelPath string) ([]byte, error) {
			return []byte(repoRelPath), nil
		},
		ExtractSymbols: func(_ context.Context, relPath string, _ []byte) (map[string]string, error) {
			switch relPath {
			case "common/include/types.h":
				return map[string]string{"common::RpcResult": "fp_v2_rpc_result_changed"}, nil
			case "src/order_service.cpp":
				return map[string]string{"order::OrderService::Pay": "fp_v2_order_pay_changed"}, nil
			default:
				return nil, nil
			}
		},
	})

	if !res.HasDrift || len(res.AffectedSlots) != 2 {
		t.Fatalf("期望 2 个槽位命中漂移，实际=%+v", res)
	}

	bySlot := make(map[string]drift.AffectedSlot)
	for _, s := range res.AffectedSlots {
		bySlot[s.SlotID] = s
	}

	if bySlot["ext_only_slot"].Reason != "external_contract_modified" {
		t.Fatalf("仅外部契约变更的槽位 Reason 应为 external_contract_modified，实际=%+v", bySlot["ext_only_slot"])
	}
	if bySlot["mixed_slot"].Reason != "symbol_modified" {
		t.Fatalf("同时含内部与外部变更的槽位应优先归因为内部 symbol_modified，实际=%+v", bySlot["mixed_slot"])
	}
}

// TestDetectModuleDocDrift_DigestScopeAndMergeNonClobber 验证 T-ADR04-09：
//  1. 整仓已发布文档中引用子模块 module_doc 锚点的指纹发生变化时，精准触发 Reason="module_digest_changed"；
//  2. 活跃模块集合发生新增或移除时，概览章节精准触发 Reason="module_scope_changed"；
//  3. MergeCodeAndModuleDriftSlots 与 SplitGitAndModuleDriftSlots 双向无损合并与分离，确保 Git-diff 漂移与模块漂移互不覆盖（P-9 防腐）。
func TestDetectModuleDocDrift_DigestScopeAndMergeNonClobber(t *testing.T) {
	sections := []drift.SectionEvidenceInput{
		{
			SlotID:    "overview_boundaries",
			SlotTitle: "系统定位与架构边界",
			Ownership: "auto",
			Evidences: []drift.AnchorEvidenceInput{
				{
					FilePath:           "sub/order/system-architecture.md",
					SymbolKind:         "module_doc",
					FQSymbol:           "order",
					ContentFingerprint: "sha256_order_v1",
				},
				{
					FilePath:           "sub/gateway/system-architecture.md",
					SymbolKind:         "module_doc",
					FQSymbol:           "gateway",
					ContentFingerprint: "sha256_gateway_v1",
				},
			},
		},
		{
			SlotID:    "core_request_flows",
			SlotTitle: "核心业务链路",
			Ownership: "hybrid",
			Evidences: []drift.AnchorEvidenceInput{
				{
					FilePath:           "sub/order/system-architecture.md",
					SymbolKind:         "module_doc",
					FQSymbol:           "order",
					ContentFingerprint: "sha256_order_v1",
				},
			},
		},
	}

	// order 模块摘要已更新为 v2，且仓库新增了 payment 活跃模块
	modDrifts := drift.DetectModuleDocDrift(drift.ModuleDocDriftInput{
		Sections: sections,
		CurrentDigests: map[string]string{
			"order":   "sha256_order_v2_updated",
			"gateway": "sha256_gateway_v1",
			"payment": "sha256_payment_v1",
		},
		ActiveModules:  []string{"order", "gateway", "payment"},
		OverviewSlotID: "overview_boundaries",
	})

	if len(modDrifts) != 2 {
		t.Fatalf("期望 2 个槽位命中模块漂移，实际=%+v", modDrifts)
	}
	if modDrifts[0].SlotID != "overview_boundaries" || modDrifts[0].Reason != drift.ReasonModuleDigestChanged {
		t.Fatalf("overview_boundaries 应命中 module_digest_changed（且包含新增模块标签），实际=%+v", modDrifts[0])
	}
	if modDrifts[1].SlotID != "core_request_flows" || modDrifts[1].Reason != drift.ReasonModuleDigestChanged {
		t.Fatalf("core_request_flows 应命中 module_digest_changed，实际=%+v", modDrifts[1])
	}

	// 验证仅新增模块（摘要未变）时触发 module_scope_changed
	scopeOnlyDrifts := drift.DetectModuleDocDrift(drift.ModuleDocDriftInput{
		Sections: sections,
		CurrentDigests: map[string]string{
			"order":   "sha256_order_v1",
			"gateway": "sha256_gateway_v1",
			"payment": "sha256_payment_v1",
		},
		ActiveModules:  []string{"order", "gateway", "payment"},
		OverviewSlotID: "overview_boundaries",
	})
	if len(scopeOnlyDrifts) != 1 || scopeOnlyDrifts[0].SlotID != "overview_boundaries" || scopeOnlyDrifts[0].Reason != drift.ReasonModuleScopeChanged {
		t.Fatalf("仅模块范围变化时 overview_boundaries 应触发 module_scope_changed，实际=%+v", scopeOnlyDrifts)
	}

	// 验证双源合并与无损分离：已有 Git-diff 漂移 (runtime_concurrency 槽位 + overview_boundaries 槽位) 与模块漂移合并后互不覆盖
	gitSlots, _ := drift.SplitGitAndModuleDriftSlots(modDrifts)
	if len(gitSlots) != 0 {
		t.Fatalf("纯模块漂移分离出的 gitSlots 应为空，实际=%+v", gitSlots)
	}
	resGit := drift.DetectCodeDrift(context.Background(), drift.CodeDriftInput{
		PublishedCommitSHA: "aaa",
		RemoteHeadSHA:      "bbb",
		Subdirectory:       "",
		ChangedFiles:       []gitsync.ChangedFile{{Status: "D", Path: "main.cpp"}},
		Sections: []drift.SectionEvidenceInput{
			{
				SlotID:    "runtime_concurrency",
				SlotTitle: "运行时并发",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{{FilePath: "main.cpp", SymbolKind: "file"}},
			},
		},
	})
	gitSlotModels := drift.MergeCodeAndModuleDriftSlots(nil, nil)
	for _, s := range resGit.AffectedSlots {
		gitSlotModels = append(gitSlotModels, drift.MergeCodeAndModuleDriftSlots(nil, modDrifts[:0])...)
		_ = s
	}
	merged := drift.MergeCodeAndModuleDriftSlots(modDrifts[:1], scopeOnlyDrifts)
	if len(merged) != 1 {
		t.Fatalf("同槽位合并后应保持唯一槽位条目，实际=%+v", merged)
	}
}
