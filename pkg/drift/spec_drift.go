package drift

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
)

var (
	slotMarkerLineRe   = regexp.MustCompile(`^\s*<!--\s*slot:([a-z][a-z0-9_]{1,48})\s*-->\s*$`)
	boundHeadingLineRe = regexp.MustCompile(`^#{2,4}\s+.+$`)
)

// SpecSlotMeta 描述用于 Spec Drift 比对的槽位规范摘要（零数据库耦合）。
type SpecSlotMeta struct {
	SlotID            string
	Title             string
	Ownership         string
	Required          bool
	IRFacets          []string
	InstructionHint   string
	EnableSourceTools bool
	MaxToolRounds     int
}

// SpecDriftSlotItem 描述单个受模板/指令变更影响的槽位明细。
type SpecDriftSlotItem struct {
	SlotID     string `json:"slot_id"`
	SlotTitle  string `json:"slot_title"`
	Ownership  string `json:"ownership"`
	Reason     string `json:"reason"`      // slot_added | slot_removed | slot_modified | prompt_changed
	ChangeKind string `json:"change_kind"` // added | modified | removed
}

// SpecDriftInput 定义槽位级 Spec Drift 纯计算输入。
type SpecDriftInput struct {
	BoundRevisionID  uint
	LatestRevisionID uint
	BoundSlots       []SpecSlotMeta
	LatestSlots      []SpecSlotMeta
	BoundPromptHash  string
	LatestPromptHash string
	BoundTemplateMD  string
	LatestTemplateMD string
}

// SpecDriftResult 输出槽位级 Spec Drift 结构化集合差结果（SC-08）。
type SpecDriftResult struct {
	HasDrift      bool                `json:"has_drift"`
	AddedSlots    []string            `json:"added_slots"`
	RemovedSlots  []string            `json:"removed_slots"`
	ModifiedSlots []string            `json:"modified_slots"`
	AffectedSlots []SpecDriftSlotItem `json:"affected_slots"`
}

// DetectSpecDrift 对比新旧两版 DocTypeRevision，返回 added_slots / removed_slots / modified_slots 及完整明细。
func DetectSpecDrift(in SpecDriftInput) SpecDriftResult {
	res := SpecDriftResult{
		AddedSlots:    []string{},
		RemovedSlots:  []string{},
		ModifiedSlots: []string{},
		AffectedSlots: []SpecDriftSlotItem{},
	}
	if in.BoundRevisionID > 0 && in.LatestRevisionID > 0 && in.BoundRevisionID == in.LatestRevisionID {
		return res
	}

	items := ComputeSpecDrift(
		in.BoundSlots,
		in.LatestSlots,
		in.BoundPromptHash,
		in.LatestPromptHash,
		in.BoundTemplateMD,
		in.LatestTemplateMD,
	)
	if len(items) == 0 {
		return res
	}

	res.HasDrift = true
	res.AffectedSlots = items
	for _, it := range items {
		switch it.ChangeKind {
		case "added":
			res.AddedSlots = append(res.AddedSlots, it.SlotID)
		case "removed":
			res.RemovedSlots = append(res.RemovedSlots, it.SlotID)
		default:
			res.ModifiedSlots = append(res.ModifiedSlots, it.SlotID)
		}
	}
	return res
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

	boundSkeleton := normalizeTemplateStaticSkeleton(boundTemplateMD, boundSlots)
	latestSkeleton := normalizeTemplateStaticSkeleton(latestTemplateMD, latestSlots)
	globalPromptChanged := boundPromptHash != latestPromptHash || boundSkeleton != latestSkeleton
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

// normalizeTemplateStaticSkeleton 剥离绑定到已声明槽位的 <!-- slot:id --> 占位符及其紧邻 ##~#### 标题行，
// 仅保留文档级静态骨架文本，避免局部槽位改名/增删/重排误触发兄弟槽位 prompt_changed。
func normalizeTemplateStaticSkeleton(templateMD string, slots []SpecSlotMeta) string {
	normalized := strings.ReplaceAll(templateMD, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	declaredSlots := make(map[string]bool, len(slots))
	for _, sl := range slots {
		if sl.SlotID != "" {
			declaredSlots[sl.SlotID] = true
		}
	}
	seenSlots := make(map[string]bool, len(slots))
	removed := make([]bool, len(lines))

	for i, line := range lines {
		m := slotMarkerLineRe.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		slotID := m[1]
		if !declaredSlots[slotID] || seenSlots[slotID] {
			continue
		}
		seenSlots[slotID] = true
		removed[i] = true

		for j := i - 1; j >= 0; j-- {
			if removed[j] {
				break
			}
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if boundHeadingLineRe.MatchString(lines[j]) {
				removed[j] = true
			}
			break
		}
	}

	var kept []string
	prevBlank := false
	for i, line := range lines {
		if removed[i] {
			continue
		}
		isBlank := strings.TrimSpace(line) == ""
		if isBlank {
			if prevBlank {
				continue
			}
			prevBlank = true
			kept = append(kept, "")
		} else {
			prevBlank = false
			kept = append(kept, line)
		}
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func normalizeMaxToolRounds(enabled bool, rounds int) int {
	if !enabled {
		return 0
	}
	if rounds <= 0 {
		return 4
	}
	return rounds
}

func slotSpecChanged(a, b SpecSlotMeta) bool {
	if a.Title != b.Title || a.Ownership != b.Ownership || a.Required != b.Required || a.InstructionHint != b.InstructionHint {
		return true
	}
	if a.EnableSourceTools != b.EnableSourceTools {
		return true
	}
	if normalizeMaxToolRounds(a.EnableSourceTools, a.MaxToolRounds) != normalizeMaxToolRounds(b.EnableSourceTools, b.MaxToolRounds) {
		return true
	}
	af := append([]string(nil), a.IRFacets...)
	bf := append([]string(nil), b.IRFacets...)
	sort.Strings(af)
	sort.Strings(bf)
	return !reflect.DeepEqual(af, bf)
}
