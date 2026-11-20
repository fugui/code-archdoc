package drift

import (
	"sort"
	"strings"
)

const (
	ReasonModuleDigestChanged = "module_digest_changed"
	ReasonModuleScopeChanged  = "module_scope_changed"
)

// ModuleDocDriftInput 描述整仓已发布文档的模块摘要与范围漂移检测入参（ADR-04 D-4）。
type ModuleDocDriftInput struct {
	Sections        []SectionEvidenceInput
	CurrentDigests  map[string]string // module_path -> 最新 digest_fingerprint
	ActiveModules   []string          // 当前仓库 active 状态模块子目录列表
	OverviewSlotID  string            // 默认 "overview_boundaries"，为空时回退至首个章节
	ForceScopeCheck bool              // 当模块索引发生显式增删/忽略/迁移时强制对比 ActiveModules
}

// IsModuleDriftReason 判断漂移原因是否属于模块级上卷漂移。
func IsModuleDriftReason(reason string) bool {
	return reason == ReasonModuleDigestChanged || reason == ReasonModuleScopeChanged
}

func isModuleDriftSymbolTag(sym string) bool {
	return strings.HasPrefix(sym, "module_doc:") ||
		strings.HasPrefix(sym, "module_added:") ||
		strings.HasPrefix(sym, "module_removed:")
}

// DetectModuleDocDrift 纯函数：检测整仓已发布文档中引用或覆盖的子模块摘要变更与模块范围变更。
func DetectModuleDocDrift(in ModuleDocDriftInput) []AffectedSlot {
	if len(in.Sections) == 0 {
		return nil
	}

	overviewSlotID := strings.TrimSpace(in.OverviewSlotID)
	if overviewSlotID == "" {
		overviewSlotID = "overview_boundaries"
	}
	hasOverviewSlot := false
	for _, sec := range in.Sections {
		if sec.SlotID == overviewSlotID {
			hasOverviewSlot = true
			break
		}
	}
	if !hasOverviewSlot {
		overviewSlotID = in.Sections[0].SlotID
	}

	activeSet := make(map[string]bool, len(in.ActiveModules))
	for _, m := range in.ActiveModules {
		norm := strings.Trim(strings.TrimSpace(m), "/")
		if norm != "" {
			activeSet[norm] = true
		}
	}

	referencedMods := make(map[string]bool)
	slotByID := make(map[string]*AffectedSlot)
	slotOrder := make([]string, 0, len(in.Sections))

	ensureSlot := func(sec SectionEvidenceInput, reason, changeKind string) *AffectedSlot {
		existing, ok := slotByID[sec.SlotID]
		if !ok {
			existing = &AffectedSlot{
				SlotID:         sec.SlotID,
				SlotTitle:      sec.SlotTitle,
				Ownership:      sec.Ownership,
				Reason:         reason,
				ChangeKind:     changeKind,
				ChangedSymbols: []string{},
				ChangedFiles:   []string{},
			}
			slotByID[sec.SlotID] = existing
			slotOrder = append(slotOrder, sec.SlotID)
			return existing
		}
		// module_digest_changed 优先级高于 module_scope_changed
		if existing.Reason == ReasonModuleScopeChanged && reason == ReasonModuleDigestChanged {
			existing.Reason = reason
			existing.ChangeKind = changeKind
		}
		if existing.ChangeKind != "removed" && changeKind == "removed" {
			existing.ChangeKind = "removed"
		}
		return existing
	}

	// 1. 逐章节比对 module_doc 锚点的 ContentFingerprint 与最新子模块摘要指纹
	for _, sec := range in.Sections {
		for _, ev := range sec.Evidences {
			if ev.SymbolKind != "module_doc" {
				continue
			}
			modPath := extractModulePathFromAnchor(ev)
			if modPath == "" {
				continue
			}
			referencedMods[modPath] = true

			curFP, exists := in.CurrentDigests[modPath]
			if len(activeSet) > 0 && !activeSet[modPath] {
				// 该子模块已被忽略或移除
				sl := ensureSlot(sec, ReasonModuleDigestChanged, "removed")
				sl.ChangedSymbols = appendUniqueStr(sl.ChangedSymbols, "module_doc:"+modPath)
				continue
			}
			if !exists || (ev.ContentFingerprint != "" && curFP != "" && ev.ContentFingerprint != curFP) {
				changeKind := "modified"
				if !exists {
					changeKind = "removed"
				}
				sl := ensureSlot(sec, ReasonModuleDigestChanged, changeKind)
				sl.ChangedSymbols = appendUniqueStr(sl.ChangedSymbols, "module_doc:"+modPath)
			}
		}
	}

	// 2. 检查模块集合增删（module_scope_changed）：当文档已含 module_doc 锚点或显式触发模块变更时生效
	if len(activeSet) > 0 && (len(referencedMods) > 0 || in.ForceScopeCheck) {
		var scopeDiffs []string
		for modPath := range activeSet {
			if !referencedMods[modPath] {
				scopeDiffs = append(scopeDiffs, "module_added:"+modPath)
			}
		}
		for modPath := range referencedMods {
			if !activeSet[modPath] {
				scopeDiffs = append(scopeDiffs, "module_removed:"+modPath)
			}
		}
		sort.Strings(scopeDiffs)
		if len(scopeDiffs) > 0 {
			var targetSec SectionEvidenceInput
			for _, sec := range in.Sections {
				if sec.SlotID == overviewSlotID {
					targetSec = sec
					break
				}
			}
			sl := ensureSlot(targetSec, ReasonModuleScopeChanged, "modified")
			for _, tag := range scopeDiffs {
				sl.ChangedSymbols = appendUniqueStr(sl.ChangedSymbols, tag)
			}
		}
	}

	out := make([]AffectedSlot, 0, len(slotOrder))
	for _, sid := range slotOrder {
		if sl, ok := slotByID[sid]; ok {
			out = append(out, *sl)
		}
	}
	return out
}

// SplitGitAndModuleDriftSlots 从既有 AffectedSlot 列表中无损分离出纯 Git-diff 槽位与模块级漂移槽位。
func SplitGitAndModuleDriftSlots(existing []AffectedSlot) (gitSlots, moduleSlots []AffectedSlot) {
	for _, sl := range existing {
		if IsModuleDriftReason(sl.Reason) {
			moduleSlots = append(moduleSlots, sl)
			continue
		}
		var gitSyms []string
		var modSyms []string
		for _, sym := range sl.ChangedSymbols {
			if isModuleDriftSymbolTag(sym) {
				modSyms = append(modSyms, sym)
			} else {
				gitSyms = append(gitSyms, sym)
			}
		}
		gitCopy := sl
		if gitSyms == nil {
			gitSyms = []string{}
		}
		gitCopy.ChangedSymbols = gitSyms
		gitSlots = append(gitSlots, gitCopy)

		if len(modSyms) > 0 {
			modReason := ReasonModuleDigestChanged
			for _, ms := range modSyms {
				if strings.HasPrefix(ms, "module_added:") || strings.HasPrefix(ms, "module_removed:") {
					modReason = ReasonModuleScopeChanged
					break
				}
			}
			moduleSlots = append(moduleSlots, AffectedSlot{
				SlotID:         sl.SlotID,
				SlotTitle:      sl.SlotTitle,
				Ownership:      sl.Ownership,
				Reason:         modReason,
				ChangeKind:     "modified",
				ChangedSymbols: modSyms,
				ChangedFiles:   []string{},
			})
		}
	}
	return gitSlots, moduleSlots
}

// MergeCodeAndModuleDriftSlots 将 Git-diff 代码漂移槽位与模块级漂移槽位无损合并，确保两者互不覆盖（ADR-04 D-4）。
func MergeCodeAndModuleDriftSlots(gitSlots, moduleSlots []AffectedSlot) []AffectedSlot {
	if len(gitSlots) == 0 && len(moduleSlots) == 0 {
		return []AffectedSlot{}
	}

	bySlot := make(map[string]*AffectedSlot, len(gitSlots)+len(moduleSlots))
	order := make([]string, 0, len(gitSlots)+len(moduleSlots))

	for _, gs := range gitSlots {
		cp := gs
		cp.ChangedSymbols = append([]string{}, gs.ChangedSymbols...)
		cp.ChangedFiles = append([]string{}, gs.ChangedFiles...)
		cleanSyms := make([]string, 0, len(cp.ChangedSymbols))
		for _, sym := range cp.ChangedSymbols {
			if !isModuleDriftSymbolTag(sym) {
				cleanSyms = append(cleanSyms, sym)
			}
		}
		cp.ChangedSymbols = cleanSyms
		bySlot[cp.SlotID] = &cp
		order = append(order, cp.SlotID)
	}

	for _, ms := range moduleSlots {
		if existing, ok := bySlot[ms.SlotID]; ok {
			// 同一章节同时存在 Git-diff 漂移与模块漂移时，保留更高优先级的 Git-diff Reason，并追加模块漂移符号标签
			for _, sym := range ms.ChangedSymbols {
				existing.ChangedSymbols = appendUniqueStr(existing.ChangedSymbols, sym)
			}
			for _, f := range ms.ChangedFiles {
				existing.ChangedFiles = appendUniqueStr(existing.ChangedFiles, f)
			}
			continue
		}
		cp := ms
		cp.ChangedSymbols = append([]string{}, ms.ChangedSymbols...)
		cp.ChangedFiles = append([]string{}, ms.ChangedFiles...)
		bySlot[cp.SlotID] = &cp
		order = append(order, cp.SlotID)
	}

	out := make([]AffectedSlot, 0, len(order))
	for _, sid := range order {
		if sl, ok := bySlot[sid]; ok {
			out = append(out, *sl)
		}
	}
	return out
}

func extractModulePathFromAnchor(ev AnchorEvidenceInput) string {
	if fq := strings.Trim(strings.TrimSpace(ev.FQSymbol), "/"); fq != "" {
		return fq
	}
	fp := strings.TrimSpace(ev.FilePath)
	fp = strings.TrimPrefix(fp, "./")
	fp = strings.TrimPrefix(fp, "sub/")
	if idx := strings.LastIndex(fp, "/"); idx > 0 {
		return strings.Trim(fp[:idx], "/")
	}
	return strings.Trim(strings.TrimSuffix(fp, ".md"), "/")
}

func appendUniqueStr(slice []string, val string) []string {
	for _, item := range slice {
		if item == val {
			return slice
		}
	}
	return append(slice, val)
}
