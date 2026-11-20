package drift

import (
	"context"
	"path"
	"sort"
	"strings"

	"code-common/backend/gitsync"
)

// AnchorEvidenceInput 描述章节关联的单个稳定证据锚点（零数据库耦合）。
type AnchorEvidenceInput struct {
	Scope              string // "" (默认 module) | "repo"
	FilePath           string
	SymbolKind         string
	FQSymbol           string
	ContentFingerprint string
	LineStart          int
	LineEnd            int
}

// SectionEvidenceInput 描述已发布文档中单个章节及其全部证据锚点。
type SectionEvidenceInput struct {
	SlotID    string
	SlotTitle string
	Ownership string
	Evidences []AnchorEvidenceInput
}

// AffectedSlot 描述受 Code Drift 或 Spec Drift 影响的单个章节槽位明细。
type AffectedSlot struct {
	SlotID         string   `json:"slot_id"`
	SlotTitle      string   `json:"slot_title"`
	Ownership      string   `json:"ownership"`
	Reason         string   `json:"reason"`
	ChangeKind     string   `json:"change_kind"` // added | modified | removed
	ChangedSymbols []string `json:"changed_symbols,omitempty"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
}

// FileBlobReader 按提交哈希与仓库相对路径读取单文件字节流。
type FileBlobReader func(ctx context.Context, commitSHA, repoRelPath string) ([]byte, error)

// SingleFileSymbolExtractor 对单个变更源码文件运行轻量 L1 抽取，返回 fq_symbol -> content_fingerprint 映射。
type SingleFileSymbolExtractor func(ctx context.Context, relPath string, content []byte) (map[string]string, error)

// CodeDriftInput 定义两阶段章节级 Code Drift 纯计算引擎入参。
type CodeDriftInput struct {
	PublishedCommitSHA string
	RemoteHeadSHA      string
	Subdirectory       string
	ChangedFiles       []gitsync.ChangedFile
	Sections           []SectionEvidenceInput
	ReadBlob           FileBlobReader
	ExtractSymbols     SingleFileSymbolExtractor
}

// CodeDriftResult 输出章节级 Code Drift 检测结果与实际解析文件计数。
type CodeDriftResult struct {
	HasDrift      bool           `json:"has_drift"`
	Precision     string         `json:"precision"` // section_level | repo_level
	AffectedSlots []AffectedSlot `json:"affected_slots"`
	ParsedFiles   int            `json:"parsed_files"`
}

// DetectCodeDrift 执行两阶段章节级 Code Drift 精确定位（03-doctype-drift-and-publishing.md §4.2 / SC-11）：
// 阶段 1：快速路径比较 SHA -> 构建变更文件索引，零开销跳过未触及任何证据锚点的变更文件；
// 阶段 2：仅对触及证据锚点的变更文件读取新版 blob 运行单文件 L1 提取器，按 fq_symbol + content_fingerprint
// 精准区分 file_removed_or_renamed / symbol_removed / symbol_modified；仅行号平移（指纹一致）判定无漂移。
func DetectCodeDrift(ctx context.Context, in CodeDriftInput) CodeDriftResult {
	if ctx == nil {
		ctx = context.Background()
	}

	pubSHA := strings.TrimSpace(in.PublishedCommitSHA)
	headSHA := strings.TrimSpace(in.RemoteHeadSHA)
	if pubSHA == headSHA || headSHA == "" {
		return CodeDriftResult{
			HasDrift:      false,
			Precision:     "section_level",
			AffectedSlots: []AffectedSlot{},
			ParsedFiles:   0,
		}
	}

	if len(in.ChangedFiles) == 0 || len(in.Sections) == 0 {
		return CodeDriftResult{
			HasDrift:      false,
			Precision:     "section_level",
			AffectedSlots: []AffectedSlot{},
			ParsedFiles:   0,
		}
	}

	// 构建文件变更索引（对重命名同时索引 OldPath，确保旧证据路径被精准识别为 file_removed_or_renamed）
	changedMap := make(map[string]gitsync.ChangedFile, len(in.ChangedFiles)*2)
	for _, cf := range in.ChangedFiles {
		normPath := normalizeRelPath(cf.Path)
		if normPath != "" {
			changedMap[normPath] = cf
		}
		if strings.ToUpper(cf.Status) == "R" && strings.TrimSpace(cf.OldPath) != "" {
			normOld := normalizeRelPath(cf.OldPath)
			if normOld != "" {
				changedMap[normOld] = cf
			}
		}
	}

	normSubdir := normalizeRelPath(in.Subdirectory)

	// 按需单文件解析缓存，避免同一文件多个证据锚点重复读取/解析
	type parsedFileCache struct {
		symbols map[string]string
		ok      bool
	}
	fileCache := make(map[string]parsedFileCache)
	parsedCount := 0

	affected := make([]AffectedSlot, 0)

	for _, sec := range in.Sections {
		if len(sec.Evidences) == 0 {
			continue
		}

		slotReason := ""
		slotChangeKind := ""
		symSet := make(map[string]bool)
		fileSet := make(map[string]bool)

		recordImpact := func(reason, changeKind, symbol, filePath string) {
			if slotReason == "" || reasonPriority(reason) > reasonPriority(slotReason) {
				slotReason = reason
				slotChangeKind = changeKind
			}
			if symbol != "" {
				symSet[symbol] = true
			}
			if filePath != "" {
				fileSet[filePath] = true
			}
		}

		// 统计本章节已具备细粒度符号级锚点的文件集合：
		// 若同一文件同时存在 file 级提及与精确符号锚点（如 cpp_func），文件修改时仅以细粒度符号指纹判定漂移，
		// 避免修改同文件内其他无关函数导致整文件级 file_modified 误报（ADR-03 / SC-11）。
		symbolCoveredFiles := make(map[string]bool)
		for _, anchor := range sec.Evidences {
			if anchor.SymbolKind != "file" && strings.TrimSpace(anchor.FQSymbol) != "" {
				if rel := normalizeRelPath(anchor.FilePath); rel != "" {
					symbolCoveredFiles[rel] = true
				}
			}
		}

		for _, anchor := range sec.Evidences {
			if anchor.SymbolKind == "module_doc" {
				// module_doc 锚点由 ModuleDigestDrift 独立评估并与 Git Diff 漂移合并
				continue
			}
			anchorRel := normalizeRelPath(anchor.FilePath)
			if anchorRel == "" {
				continue
			}
			isRepoScopeAnchor := strings.TrimSpace(anchor.Scope) == "repo"
			repoRel := anchorRel
			if !isRepoScopeAnchor && normSubdir != "" {
				repoRel = normalizeRelPath(path.Join(normSubdir, anchorRel))
			}

			cf, touched := changedMap[repoRel]
			if !touched && !isRepoScopeAnchor && normSubdir != "" {
				// 兜底兼容锚点中已自带子目录前缀的历史数据
				cf, touched = changedMap[anchorRel]
				if touched {
					repoRel = anchorRel
				}
			}
			if !touched {
				continue
			}

			removedReason := "file_removed_or_renamed"
			symRemovedReason := "symbol_removed"
			symModifiedReason := "symbol_modified"
			fileModifiedReason := "file_modified"
			if isRepoScopeAnchor {
				removedReason = "external_contract_removed"
				symRemovedReason = "external_contract_removed"
				symModifiedReason = "external_contract_modified"
				fileModifiedReason = "external_contract_modified"
			}

			status := strings.ToUpper(strings.TrimSpace(cf.Status))
			if status == "D" || (status == "R" && normalizeRelPath(cf.OldPath) == repoRel) {
				recordImpact(removedReason, "removed", anchor.FQSymbol, anchorRel)
				continue
			}

			if anchor.SymbolKind == "file" || strings.TrimSpace(anchor.FQSymbol) == "" {
				if symbolCoveredFiles[anchorRel] {
					continue
				}
				recordImpact(fileModifiedReason, "modified", "", anchorRel)
				continue
			}

			// 符号级精准比对：仅解析触及的变更文件
			cached, seen := fileCache[repoRel]
			if !seen {
				if in.ReadBlob == nil || in.ExtractSymbols == nil {
					cached = parsedFileCache{ok: false}
				} else {
					blob, rErr := in.ReadBlob(ctx, headSHA, repoRel)
					if rErr != nil {
						cached = parsedFileCache{ok: false}
					} else {
						parsedCount++
						syms, exErr := in.ExtractSymbols(ctx, anchorRel, blob)
						if exErr != nil {
							cached = parsedFileCache{ok: false}
						} else {
							cached = parsedFileCache{symbols: syms, ok: true}
						}
					}
				}
				fileCache[repoRel] = cached
			}

			if !cached.ok {
				recordImpact(fileModifiedReason, "modified", anchor.FQSymbol, anchorRel)
				continue
			}

			newFP, exists := lookupSymbolFingerprint(cached.symbols, anchor.FQSymbol)
			if !exists {
				recordImpact(symRemovedReason, "removed", anchor.FQSymbol, anchorRel)
				continue
			}
			if anchor.ContentFingerprint != "" && newFP != "" && newFP != anchor.ContentFingerprint {
				recordImpact(symModifiedReason, "modified", anchor.FQSymbol, anchorRel)
			}
			// 若指纹完全一致（如仅顶部插入注释导致行号下移），判定无实质漂移，不误报！
		}

		if slotReason != "" {
			syms := mapKeysSorted(symSet)
			files := mapKeysSorted(fileSet)
			affected = append(affected, AffectedSlot{
				SlotID:         sec.SlotID,
				SlotTitle:      sec.SlotTitle,
				Ownership:      sec.Ownership,
				Reason:         slotReason,
				ChangeKind:     slotChangeKind,
				ChangedSymbols: syms,
				ChangedFiles:   files,
			})
		}
	}

	return CodeDriftResult{
		HasDrift:      len(affected) > 0,
		Precision:     "section_level",
		AffectedSlots: affected,
		ParsedFiles:   parsedCount,
	}
}

func lookupSymbolFingerprint(symMap map[string]string, fqSymbol string) (string, bool) {
	if len(symMap) == 0 {
		return "", false
	}
	if fp, ok := symMap[fqSymbol]; ok {
		return fp, true
	}
	// 兜底处理无命名空间前缀的短符号（仅当唯一命中时才匹配，防止同名误判）
	suffix1 := "::" + fqSymbol
	suffix2 := "." + fqSymbol
	var matchedFP string
	matchCount := 0
	for k, fp := range symMap {
		if strings.HasSuffix(k, suffix1) || strings.HasSuffix(k, suffix2) {
			matchedFP = fp
			matchCount++
		}
	}
	if matchCount == 1 {
		return matchedFP, true
	}
	return "", false
}

func reasonPriority(reason string) int {
	switch reason {
	case "file_removed_or_renamed":
		return 6
	case "symbol_removed":
		return 5
	case "symbol_modified":
		return 4
	case "file_modified":
		return 3
	case "external_contract_removed":
		return 2
	case "external_contract_modified":
		return 1
	default:
		return 0
	}
}

func normalizeRelPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	cleaned := path.Clean(strings.TrimPrefix(trimmed, "./"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func mapKeysSorted(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
