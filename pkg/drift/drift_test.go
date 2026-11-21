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

// TestCodeDriftSymbolAnchorSubsumesFileAnchor 验证同一文件同时存在 file 级提及与精确符号锚点时，
// 修改该文件内的其他无关函数不产生 file_modified 误报，仅受修改的符号所在章节触发漂移（ADR-03 / SC-11）。
func TestCodeDriftSymbolAnchorSubsumesFileAnchor(t *testing.T) {
	res := drift.DetectCodeDrift(context.Background(), drift.CodeDriftInput{
		PublishedCommitSHA: "1111111111111111111111111111111111111111",
		RemoteHeadSHA:      "2222222222222222222222222222222222222222",
		ChangedFiles:       []gitsync.ChangedFile{{Status: "M", Path: "src/order.cpp"}},
		Sections: []drift.SectionEvidenceInput{
			{
				SlotID:    "slot_unchanged_fn",
				SlotTitle: "建单说明",
				Ownership: "auto",
				Evidences: []drift.AnchorEvidenceInput{
					{FilePath: "src/order.cpp", SymbolKind: "file", FQSymbol: ""},
					{FilePath: "src/order.cpp", SymbolKind: "cpp_func", FQSymbol: "order::Create", ContentFingerprint: "sha256:create_same"},
				},
			},
			{
				SlotID:    "slot_changed_fn",
				SlotTitle: "支付说明",
				Ownership: "hybrid",
				Evidences: []drift.AnchorEvidenceInput{
					{FilePath: "src/order.cpp", SymbolKind: "file", FQSymbol: ""},
					{FilePath: "src/order.cpp", SymbolKind: "cpp_func", FQSymbol: "order::Pay", ContentFingerprint: "sha256:pay_v1"},
				},
			},
		},
		ReadBlob: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte("mock"), nil
		},
		ExtractSymbols: func(_ context.Context, _ string, _ []byte) (map[string]string, error) {
			return map[string]string{
				"order::Create": "sha256:create_same",
				"order::Pay":    "sha256:pay_v2",
			}, nil
		},
	})

	if !res.HasDrift || len(res.AffectedSlots) != 1 || res.AffectedSlots[0].SlotID != "slot_changed_fn" || res.AffectedSlots[0].Reason != "symbol_modified" {
		t.Fatalf("预期仅 slot_changed_fn 触发 symbol_modified 漂移，实际 %+v", res)
	}
}

// TestSpecDriftHeadingRenameDoesNotTriggerSiblingPromptChanged 验证 T-ADR05-09：
// 仅修改槽位 A 的 title（并同步修改 template.md 中紧邻绑定 ## 标题），兄弟槽位 B/C 与静态文本不变时，
// ComputeSpecDrift 仅返回槽位 A（reason="slot_modified"），兄弟槽位零误报。
func TestSpecDriftHeadingRenameDoesNotTriggerSiblingPromptChanged(t *testing.T) {
	boundSlots := []drift.SpecSlotMeta{
		{SlotID: "slot_a", Title: "1. 模块边界", Ownership: "auto", Required: true, IRFacets: []string{"tree"}},
		{SlotID: "slot_b", Title: "2. 核心链路", Ownership: "auto", Required: true, IRFacets: []string{"symbols"}},
		{SlotID: "slot_c", Title: "3. 约束说明", Ownership: "hybrid", Required: false, IRFacets: []string{"deps"}},
	}
	latestSlots := []drift.SpecSlotMeta{
		{SlotID: "slot_a", Title: "1. 系统职责与模块分层边界", Ownership: "auto", Required: true, IRFacets: []string{"tree"}},
		{SlotID: "slot_b", Title: "2. 核心链路", Ownership: "auto", Required: true, IRFacets: []string{"symbols"}},
		{SlotID: "slot_c", Title: "3. 约束说明", Ownership: "hybrid", Required: false, IRFacets: []string{"deps"}},
	}

	boundTpl := "# 系统架构\n\n## 1. 模块边界\n<!-- slot:slot_a -->\n\n## 2. 核心链路\n<!-- slot:slot_b -->\n\n## 3. 约束说明\n<!-- slot:slot_c -->\n"
	latestTpl := "# 系统架构\n\n## 1. 系统职责与模块分层边界\n<!-- slot:slot_a -->\n\n## 2. 核心链路\n<!-- slot:slot_b -->\n\n## 3. 约束说明\n<!-- slot:slot_c -->\n"

	items := drift.ComputeSpecDrift(boundSlots, latestSlots, "prompt_v1", "prompt_v1", boundTpl, latestTpl)
	if len(items) != 1 {
		t.Fatalf("预期仅 slot_a 触发漂移（兄弟槽位零误报），实际 %+v", items)
	}
	if items[0].SlotID != "slot_a" || items[0].Reason != "slot_modified" || items[0].ChangeKind != "modified" {
		t.Fatalf("预期 slot_a 为 slot_modified，实际 %+v", items[0])
	}
}

// TestSpecDriftAddRemoveSlotDoesNotTriggerSiblingPromptChanged 验证 T-ADR05-10：
// 新增槽位 E、删除槽位 C 或仅重排槽位顺序（未改静态骨架）时，不触发兄弟槽位 prompt_changed 误报。
func TestSpecDriftAddRemoveSlotDoesNotTriggerSiblingPromptChanged(t *testing.T) {
	baseSlots := []drift.SpecSlotMeta{
		{SlotID: "slot_a", Title: "1. 概览", Ownership: "auto", Required: true, IRFacets: []string{"tree"}},
		{SlotID: "slot_b", Title: "2. 链路", Ownership: "auto", Required: true, IRFacets: []string{"symbols"}},
		{SlotID: "slot_c", Title: "3. 排障", Ownership: "hybrid", Required: false, IRFacets: []string{"config_keys"}},
	}
	baseTpl := "# 文档主标题\n\n## 1. 概览\n<!-- slot:slot_a -->\n\n## 2. 链路\n<!-- slot:slot_b -->\n\n## 3. 排障\n<!-- slot:slot_c -->\n"

	// 1. 新增槽位 E（同步新增绑定标题与占位标记）
	addedSlots := append(append([]drift.SpecSlotMeta(nil), baseSlots...), drift.SpecSlotMeta{
		SlotID: "slot_e", Title: "4. 扩展边界", Ownership: "auto", Required: true, IRFacets: []string{"deps"},
	})
	addedTpl := baseTpl + "\n## 4. 扩展边界\n<!-- slot:slot_e -->\n"
	addedItems := drift.ComputeSpecDrift(baseSlots, addedSlots, "p1", "p1", baseTpl, addedTpl)
	if len(addedItems) != 1 || addedItems[0].SlotID != "slot_e" || addedItems[0].Reason != "slot_added" {
		t.Fatalf("新增槽位 E 预期仅返回 slot_e(slot_added)，实际 %+v", addedItems)
	}

	// 2. 删除槽位 C（同步移除绑定标题与占位标记）
	removedSlots := []drift.SpecSlotMeta{baseSlots[0], baseSlots[1]}
	removedTpl := "# 文档主标题\n\n## 1. 概览\n<!-- slot:slot_a -->\n\n## 2. 链路\n<!-- slot:slot_b -->\n"
	removedItems := drift.ComputeSpecDrift(baseSlots, removedSlots, "p1", "p1", baseTpl, removedTpl)
	if len(removedItems) != 1 || removedItems[0].SlotID != "slot_c" || removedItems[0].Reason != "slot_removed" {
		t.Fatalf("删除槽位 C 预期仅返回 slot_c(slot_removed)，实际 %+v", removedItems)
	}

	// 3. 仅重排槽位顺序（未改静态骨架）
	reorderedSlots := []drift.SpecSlotMeta{baseSlots[2], baseSlots[0], baseSlots[1]}
	reorderedTpl := "# 文档主标题\n\n## 3. 排障\n<!-- slot:slot_c -->\n\n## 1. 概览\n<!-- slot:slot_a -->\n\n## 2. 链路\n<!-- slot:slot_b -->\n"
	reorderedItems := drift.ComputeSpecDrift(baseSlots, reorderedSlots, "p1", "p1", baseTpl, reorderedTpl)
	if len(reorderedItems) != 0 {
		t.Fatalf("纯重排槽位顺序不应产生 Spec Drift，实际 %+v", reorderedItems)
	}
}

// TestSpecDriftStaticSkeletonAndSourceToolsChange 验证 T-ADR05-11：
// ① 修改文首 H1 / 静态导语或 PromptHash 时，非锁定槽位均标为 prompt_changed（locked 豁免）；
// ② 仅开启槽位 B 的 enable_source_tools 或调整 max_tool_rounds 时，仅槽位 B 标为 slot_modified。
func TestSpecDriftStaticSkeletonAndSourceToolsChange(t *testing.T) {
	slots := []drift.SpecSlotMeta{
		{SlotID: "slot_a", Title: "1. 概览", Ownership: "auto", Required: true, IRFacets: []string{"tree"}},
		{SlotID: "slot_b", Title: "2. 并发", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, EnableSourceTools: false, MaxToolRounds: 0},
		{SlotID: "slot_locked", Title: "3. 锁定说明", Ownership: "locked", Required: false, IRFacets: []string{"deps"}},
	}
	tplV1 := "# 架构文档\n\n> 静态导语 V1\n\n## 1. 概览\n<!-- slot:slot_a -->\n\n## 2. 并发\n<!-- slot:slot_b -->\n\n## 3. 锁定说明\n<!-- slot:slot_locked -->\n"
	tplV2 := "# 架构文档 V2\n\n> 静态导语 V2\n\n## 1. 概览\n<!-- slot:slot_a -->\n\n## 2. 并发\n<!-- slot:slot_b -->\n\n## 3. 锁定说明\n<!-- slot:slot_locked -->\n"

	// ① 静态骨架变更：非锁定槽位均标为 prompt_changed，locked 豁免
	skelDrift := drift.ComputeSpecDrift(slots, slots, "p1", "p1", tplV1, tplV2)
	if len(skelDrift) != 2 {
		t.Fatalf("静态骨架变更预期触发 2 个非锁定槽位 prompt_changed，实际 %+v", skelDrift)
	}
	for _, it := range skelDrift {
		if it.SlotID == "slot_locked" {
			t.Fatalf("locked 槽位应豁免 prompt_changed: %+v", skelDrift)
		}
		if it.Reason != "prompt_changed" {
			t.Fatalf("预期 reason=prompt_changed，实际 %+v", it)
		}
	}

	// ①-b PromptHash 变更同样触发非锁定槽位 prompt_changed
	promptDrift := drift.ComputeSpecDrift(slots, slots, "p1", "p2", tplV1, tplV1)
	if len(promptDrift) != 2 || promptDrift[0].Reason != "prompt_changed" || promptDrift[1].Reason != "prompt_changed" {
		t.Fatalf("PromptHash 变更预期触发 2 个非锁定槽位 prompt_changed，实际 %+v", promptDrift)
	}

	// ② 仅开启槽位 B 的 enable_source_tools=true（默认 4 轮）
	slotsEnabledB := []drift.SpecSlotMeta{
		slots[0],
		{SlotID: "slot_b", Title: "2. 并发", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, EnableSourceTools: true, MaxToolRounds: 4},
		slots[2],
	}
	toolEnableDrift := drift.ComputeSpecDrift(slots, slotsEnabledB, "p1", "p1", tplV1, tplV1)
	if len(toolEnableDrift) != 1 || toolEnableDrift[0].SlotID != "slot_b" || toolEnableDrift[0].Reason != "slot_modified" {
		t.Fatalf("开启 slot_b source_tools 预期仅 slot_b 为 slot_modified，实际 %+v", toolEnableDrift)
	}

	// ②-b 调整已开启源码工具槽位的 max_tool_rounds（4 -> 6）
	slotsRounds6B := []drift.SpecSlotMeta{
		slots[0],
		{SlotID: "slot_b", Title: "2. 并发", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, EnableSourceTools: true, MaxToolRounds: 6},
		slots[2],
	}
	roundsDrift := drift.ComputeSpecDrift(slotsEnabledB, slotsRounds6B, "p1", "p1", tplV1, tplV1)
	if len(roundsDrift) != 1 || roundsDrift[0].SlotID != "slot_b" || roundsDrift[0].Reason != "slot_modified" {
		t.Fatalf("修改 max_tool_rounds 预期仅 slot_b 为 slot_modified，实际 %+v", roundsDrift)
	}

	// ②-c 归一化等价验证：EnableSourceTools=false 时 MaxToolRounds 0 vs 4 不误报；EnableSourceTools=true 时 0 vs 4 不误报
	slotsFalseWithRounds := []drift.SpecSlotMeta{
		slots[0],
		{SlotID: "slot_b", Title: "2. 并发", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, EnableSourceTools: false, MaxToolRounds: 4},
		slots[2],
	}
	if got := drift.ComputeSpecDrift(slots, slotsFalseWithRounds, "p1", "p1", tplV1, tplV1); len(got) != 0 {
		t.Fatalf("EnableSourceTools=false 时不同 MaxToolRounds 应归一化为 0 且零漂移，实际 %+v", got)
	}
	slotsTrueWithZeroRounds := []drift.SpecSlotMeta{
		slots[0],
		{SlotID: "slot_b", Title: "2. 并发", Ownership: "hybrid", Required: true, IRFacets: []string{"symbols"}, EnableSourceTools: true, MaxToolRounds: 0},
		slots[2],
	}
	if got := drift.ComputeSpecDrift(slotsEnabledB, slotsTrueWithZeroRounds, "p1", "p1", tplV1, tplV1); len(got) != 0 {
		t.Fatalf("EnableSourceTools=true 时 MaxToolRounds=0 应归一化为默认 4 且零漂移，实际 %+v", got)
	}
}
