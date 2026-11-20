package drift_test

import (
	"context"
	"path"
	"strings"
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/drift"
	"code-common/backend/gitsync"
)

func buildCppExtractorAdapter() drift.SingleFileSymbolExtractor {
	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	return func(ctx context.Context, relPath string, content []byte) (map[string]string, error) {
		ext := strings.ToLower(path.Ext(relPath))
		lang := "cpp"
		switch ext {
		case ".c":
			lang = "c"
		case ".java":
			lang = "java"
		case ".py":
			lang = "python"
		}
		syms := agg.ExtractSingleFileSymbols(ctx, relPath, lang, content)
		out := make(map[string]string, len(syms))
		for _, s := range syms {
			out[s.FQSymbol] = s.Meta.ContentFingerprint
		}
		return out, nil
	}
}

// TestCodeDriftZeroFalsePositiveOnLineShift 验证 SC-11 黄金场景：
// 同一 .cpp 文件顶部插入 10 行注释（两函数行号均下移 10 行）并仅修改 OrderService::Pay 函数体时，
// 仅第 3 章标为 Code Drift (reason="symbol_modified", changed_symbols=["order::OrderService::Pay"])，第 1 章漂移为 0！
func TestCodeDriftZeroFalsePositiveOnLineShift(t *testing.T) {
	ctx := context.Background()
	extractor := buildCppExtractorAdapter()

	oldCpp := `namespace order {

class OrderService {
public:
    int Create(int user_id);
    int Pay(int order_id, int amount);
};

int OrderService::Create(int user_id) {
    if (user_id <= 0) {
        return -1;
    }
    return user_id * 100;
}

int OrderService::Pay(int order_id, int amount) {
    if (amount <= 0) {
        return -2;
    }
    return order_id + amount;
}

} // namespace order
`

	// 顶部插入 10 行注释，OrderService::Create 逻辑完全不变（仅行号下移 10 行），仅修改 OrderService::Pay 实现体
	newCpp := `// Line 1: Copyright header
// Line 2: Architecture comment
// Line 3: Added documentation block
// Line 4: Extra spacing comment
// Line 5: Extra spacing comment
// Line 6: Extra spacing comment
// Line 7: Extra spacing comment
// Line 8: Extra spacing comment
// Line 9: Extra spacing comment
// Line 10: Extra spacing comment
namespace order {

class OrderService {
public:
    int Create(int user_id);
    int Pay(int order_id, int amount);
};

int OrderService::Create(int user_id) {
    if (user_id <= 0) {
        return -1;
    }
    return user_id * 100;
}

int OrderService::Pay(int order_id, int amount) {
    if (amount <= 0 || order_id <= 0) {
        return -42;
    }
    int fee = amount / 100;
    return order_id + amount - fee;
}

} // namespace order
`

	relPath := "src/order/order_service.cpp"
	oldSyms, err := extractor(ctx, relPath, []byte(oldCpp))
	if err != nil {
		t.Fatalf("提取旧版 C++ 符号失败: %v", err)
	}
	createFP, okCreate := oldSyms["order::OrderService::Create"]
	payFP, okPay := oldSyms["order::OrderService::Pay"]
	if !okCreate || !okPay || createFP == "" || payFP == "" {
		t.Fatalf("未能提取预期 C++ 符号指纹: syms=%+v", oldSyms)
	}

	readCalls := 0
	res := drift.DetectCodeDrift(ctx, drift.CodeDriftInput{
		PublishedCommitSHA: "1111111111111111111111111111111111111111",
		RemoteHeadSHA:      "2222222222222222222222222222222222222222",
		ChangedFiles: []gitsync.ChangedFile{
			{Status: "M", Path: relPath},
			{Status: "M", Path: "src/unrelated/helper.cpp"},
		},
		Sections: []drift.SectionEvidenceInput{
			{
				SlotID:    "overview_boundaries",
				SlotTitle: "1. 系统定位与模块分层边界",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{
					{
						FilePath:           relPath,
						SymbolKind:         "cpp_func",
						FQSymbol:           "order::OrderService::Create",
						ContentFingerprint: createFP,
						LineStart:          9,
						LineEnd:            14,
					},
				},
			},
			{
				SlotID:    "core_request_flows",
				SlotTitle: "3. 端到端核心业务链路与时序图",
				Ownership: "hybrid",
				Evidences: []drift.AnchorEvidenceInput{
					{
						FilePath:           relPath,
						SymbolKind:         "cpp_func",
						FQSymbol:           "order::OrderService::Pay",
						ContentFingerprint: payFP,
						LineStart:          16,
						LineEnd:            21,
					},
				},
			},
		},
		ReadBlob: func(_ context.Context, _ string, p string) ([]byte, error) {
			readCalls++
			if p != relPath {
				t.Fatalf("不应读取未触及证据的文件: %s", p)
			}
			return []byte(newCpp), nil
		},
		ExtractSymbols: extractor,
	})

	if !res.HasDrift {
		t.Fatalf("预期检测到第 3 章 Code Drift，实际 HasDrift=false")
	}
	if res.Precision != "section_level" {
		t.Fatalf("预期 precision=section_level，实际 %q", res.Precision)
	}
	if readCalls != 1 || res.ParsedFiles != 1 {
		t.Fatalf("同一变更文件应仅读取并解析 1 次，实际 readCalls=%d parsedFiles=%d", readCalls, res.ParsedFiles)
	}
	if len(res.AffectedSlots) != 1 {
		t.Fatalf("预期仅第 3 章受影响（第 1 章零误报），实际 affected=%+v", res.AffectedSlots)
	}
	got := res.AffectedSlots[0]
	if got.SlotID != "core_request_flows" {
		t.Fatalf("预期受影响槽位为 core_request_flows，实际 %q", got.SlotID)
	}
	if got.Reason != "symbol_modified" {
		t.Fatalf("预期 reason=symbol_modified，实际 %q", got.Reason)
	}
	if len(got.ChangedSymbols) != 1 || got.ChangedSymbols[0] != "order::OrderService::Pay" {
		t.Fatalf("预期 changed_symbols=[order::OrderService::Pay]，实际 %+v", got.ChangedSymbols)
	}
}

// TestCodeDriftNoNewCommitReturnsZero 验证 publishedCommitSHA == remoteHeadSHA 时零文件解析直接返回无漂移。
func TestCodeDriftNoNewCommitReturnsZero(t *testing.T) {
	readCalls := 0
	extractCalls := 0
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	res := drift.DetectCodeDrift(context.Background(), drift.CodeDriftInput{
		PublishedCommitSHA: sha,
		RemoteHeadSHA:      sha,
		ChangedFiles:       []gitsync.ChangedFile{{Status: "M", Path: "src/order.cpp"}},
		Sections: []drift.SectionEvidenceInput{
			{
				SlotID:    "s1",
				SlotTitle: "S1",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{
					{FilePath: "src/order.cpp", SymbolKind: "cpp_func", FQSymbol: "Foo::Bar", ContentFingerprint: "sha256:1234"},
				},
			},
		},
		ReadBlob: func(_ context.Context, _, _ string) ([]byte, error) {
			readCalls++
			return nil, nil
		},
		ExtractSymbols: func(_ context.Context, _ string, _ []byte) (map[string]string, error) {
			extractCalls++
			return nil, nil
		},
	})

	if res.HasDrift {
		t.Fatalf("相同 commit SHA 不应产生漂移")
	}
	if readCalls != 0 || extractCalls != 0 || res.ParsedFiles != 0 {
		t.Fatalf("快速路径必须零文件解析，实际 read=%d extract=%d parsed=%d", readCalls, extractCalls, res.ParsedFiles)
	}
}

// TestCodeDriftDetectsRemovedSymbolAndRenamedFile 验证符号删除、文件重命名/删除以及子目录路径映射。
func TestCodeDriftDetectsRemovedSymbolAndRenamedFile(t *testing.T) {
	res := drift.DetectCodeDrift(context.Background(), drift.CodeDriftInput{
		PublishedCommitSHA: "1111111111111111111111111111111111111111",
		RemoteHeadSHA:      "3333333333333333333333333333333333333333",
		Subdirectory:       "svc/order",
		ChangedFiles: []gitsync.ChangedFile{
			{Status: "R", OldPath: "svc/order/src/old.cpp", Path: "svc/order/src/new.cpp"},
			{Status: "M", Path: "svc/order/src/live.cpp"},
		},
		Sections: []drift.SectionEvidenceInput{
			{
				SlotID:    "slot_renamed",
				SlotTitle: "Renamed Slot",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{
					{FilePath: "src/old.cpp", SymbolKind: "cpp_func", FQSymbol: "old::Fn", ContentFingerprint: "sha256:aaa"},
				},
			},
			{
				SlotID:    "slot_removed_sym",
				SlotTitle: "Removed Sym Slot",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{
					{FilePath: "src/live.cpp", SymbolKind: "cpp_func", FQSymbol: "live::DeletedFn", ContentFingerprint: "sha256:bbb"},
				},
			},
		},
		ReadBlob: func(_ context.Context, _, repoRelPath string) ([]byte, error) {
			if repoRelPath != "svc/order/src/live.cpp" {
				t.Fatalf("预期读取 svc/order/src/live.cpp，实际 %s", repoRelPath)
			}
			return []byte("int other() { return 1; }"), nil
		},
		ExtractSymbols: func(_ context.Context, _ string, _ []byte) (map[string]string, error) {
			return map[string]string{"other": "sha256:ccc"}, nil
		},
	})

	if !res.HasDrift || len(res.AffectedSlots) != 2 {
		t.Fatalf("预期检测到 2 个受影响槽位，实际 %+v", res)
	}
	if res.AffectedSlots[0].SlotID != "slot_renamed" || res.AffectedSlots[0].Reason != "file_removed_or_renamed" {
		t.Fatalf("重命名旧文件应标为 file_removed_or_renamed，实际 %+v", res.AffectedSlots[0])
	}
	if res.AffectedSlots[1].SlotID != "slot_removed_sym" || res.AffectedSlots[1].Reason != "symbol_removed" {
		t.Fatalf("删除符号应标为 symbol_removed，实际 %+v", res.AffectedSlots[1])
	}
}

// TestSpecDriftDetectsAddedModifiedRemovedSlots 验证 SC-08 槽位级 Spec Drift 精确计算与无改动返回 false。
func TestSpecDriftDetectsAddedModifiedRemovedSlots(t *testing.T) {
	oldSlots := []drift.SpecSlotMeta{
		{SlotID: "overview", Title: "1. 概览", Ownership: "auto", Required: true, IRFacets: []string{"tree", "deps"}},
		{SlotID: "flows", Title: "2. 链路", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, InstructionHint: "old hint"},
		{SlotID: "legacy", Title: "3. 旧槽位", Ownership: "auto", Required: false, IRFacets: []string{"tree"}},
		{SlotID: "locked_notes", Title: "4. 锁定说明", Ownership: "locked", Required: false, IRFacets: []string{"tree"}},
	}

	// 1. 无模板改动时返回 has_drift=false
	noDrift := drift.DetectSpecDrift(drift.SpecDriftInput{
		BoundRevisionID:  1,
		LatestRevisionID: 2,
		BoundSlots:       oldSlots,
		LatestSlots:      oldSlots,
		BoundPromptHash:  "hash_v1",
		LatestPromptHash: "hash_v1",
		BoundTemplateMD:  "tpl_v1",
		LatestTemplateMD: "tpl_v1",
	})
	if noDrift.HasDrift || len(noDrift.AffectedSlots) != 0 {
		t.Fatalf("无模板变动不应触发 Spec Drift，实际 %+v", noDrift)
	}

	// 2. 新增 security_boundary、修改 flows、删除 legacy
	newSlots := []drift.SpecSlotMeta{
		{SlotID: "overview", Title: "1. 概览", Ownership: "auto", Required: true, IRFacets: []string{"deps", "tree"}}, // 顺序不同但集合相同 -> 未变更
		{SlotID: "flows", Title: "2. 链路", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, InstructionHint: "new hint"},
		{SlotID: "locked_notes", Title: "4. 锁定说明", Ownership: "locked", Required: false, IRFacets: []string{"tree"}},
		{SlotID: "security_boundary", Title: "5. 安全边界", Ownership: "auto", Required: true, IRFacets: []string{"config_keys"}},
	}

	got := drift.DetectSpecDrift(drift.SpecDriftInput{
		BoundRevisionID:  1,
		LatestRevisionID: 2,
		BoundSlots:       oldSlots,
		LatestSlots:      newSlots,
		BoundPromptHash:  "hash_v1",
		LatestPromptHash: "hash_v1",
		BoundTemplateMD:  "tpl_v1",
		LatestTemplateMD: "tpl_v1",
	})
	if !got.HasDrift {
		t.Fatalf("预期检测到 Spec Drift")
	}
	if len(got.AddedSlots) != 1 || got.AddedSlots[0] != "security_boundary" {
		t.Fatalf("预期 added_slots=[security_boundary]，实际 %+v", got.AddedSlots)
	}
	if len(got.ModifiedSlots) != 1 || got.ModifiedSlots[0] != "flows" {
		t.Fatalf("预期 modified_slots=[flows]，实际 %+v", got.ModifiedSlots)
	}
	if len(got.RemovedSlots) != 1 || got.RemovedSlots[0] != "legacy" {
		t.Fatalf("预期 removed_slots=[legacy]，实际 %+v", got.RemovedSlots)
	}
}
