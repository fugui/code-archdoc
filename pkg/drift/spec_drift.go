package drift

import (
	"reflect"
	"sort"
)

// SpecSlotMeta 描述用于 Spec Drift 比对的槽位规范摘要（零数据库耦合）。
type SpecSlotMeta struct {
	SlotID          string
	Title           string
	Ownership       string
	Required        bool
	IRFacets        []string
	InstructionHint string
}

// SpecDriftSlotItem 描述单个受模板/指令变更影响的槽位明细。
type SpecDriftSlotItem struct {
	SlotID     string
	SlotTitle  string
	Ownership  string
	Reason     string // slot_added | slot_removed | slot_modified | prompt_changed
	ChangeKind string // added | modified | removed
}

// ComputeSpecDrift 对比文档已发布修订绑定的 DocTypeRevision 与当前最新 DocTypeRevision，
// 按 slot_id 精确输出新增、删除及变更（含全局 Prompt/模板变更）影响的槽位集合（03-doctype-drift-and-publishing.md §4.3）。
func ComputeSpecDrift(
	boundSlots []SpecSlotMeta,
	latestSlots []SpecSlotMeta,
	boundPromptHash string,
	latestPromptHash string,
	boundTemplateMD string,
	latestTemplateMD string,
) []SpecDriftSlotItem {
	boundMap := make(map[string]SpecSlotMeta, len(boundSlots))
	for _, s := range boundSlots {
		boundMap[s.SlotID] = s
	}
	latestMap := make(map[string]SpecSlotMeta, len(latestSlots))
	for _, s := range latestSlots {
		latestMap[s.SlotID] = s
	}

	globalPromptChanged := boundPromptHash != latestPromptHash || boundTemplateMD != latestTemplateMD
	var affected []SpecDriftSlotItem

	// 1. 新增或修改的槽位（按新模板顺序）
	for _, ls := range latestSlots {
		bs, existed := boundMap[ls.SlotID]
		if !existed {
			affected = append(affected, SpecDriftSlotItem{
				SlotID:     ls.SlotID,
				SlotTitle:  ls.Title,
				Ownership:  ls.Ownership,
				Reason:     "slot_added",
				ChangeKind: "added",
			})
			continue
		}
		if slotSpecChanged(bs, ls) {
			affected = append(affected, SpecDriftSlotItem{
				SlotID:     ls.SlotID,
				SlotTitle:  ls.Title,
				Ownership:  ls.Ownership,
				Reason:     "slot_modified",
				ChangeKind: "modified",
			})
			continue
		}
		if globalPromptChanged && ls.Ownership != "locked" {
			affected = append(affected, SpecDriftSlotItem{
				SlotID:     ls.SlotID,
				SlotTitle:  ls.Title,
				Ownership:  ls.Ownership,
				Reason:     "prompt_changed",
				ChangeKind: "modified",
			})
		}
	}

	// 2. 被新模板移除的旧槽位
	for _, bs := range boundSlots {
		if _, exists := latestMap[bs.SlotID]; !exists {
			affected = append(affected, SpecDriftSlotItem{
				SlotID:     bs.SlotID,
				SlotTitle:  bs.Title,
				Ownership:  bs.Ownership,
				Reason:     "slot_removed",
				ChangeKind: "removed",
			})
		}
	}

	return affected
}

func slotSpecChanged(a, b SpecSlotMeta) bool {
	if a.Title != b.Title || a.Ownership != b.Ownership || a.Required != b.Required || a.InstructionHint != b.InstructionHint {
		return true
	}
	af := append([]string(nil), a.IRFacets...)
	bf := append([]string(nil), b.IRFacets...)
	sort.Strings(af)
	sort.Strings(bf)
	return !reflect.DeepEqual(af, bf)
}
