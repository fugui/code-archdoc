package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"unicode"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/sandbox"
)

const (
	ErrorCodeFileBudgetExceeded  = "FILE_BUDGET_EXCEEDED"
	ErrorCodeTokenBudgetExceeded = "TOKEN_BUDGET_EXCEEDED"
)

// BudgetDirectoryItem 按目录统计的预估 Token 占用。
type BudgetDirectoryItem struct {
	Path            string `json:"path"`
	EstimatedTokens int    `json:"estimated_tokens"`
	FileCount       int    `json:"file_count"`
}

// BudgetDocTypeItem 按文档类型统计的预估 Token 占用。
type BudgetDocTypeItem struct {
	Slug            string `json:"slug"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

// BudgetAdvice 生成前预算熔断的结构化裁剪建议（对齐 models.BudgetAdvice & SC-09）。
type BudgetAdvice struct {
	EstimatedTokens  int                   `json:"estimated_tokens"`
	MaxTokensPerTask int                   `json:"max_tokens_per_task"`
	EffectiveFiles   int                   `json:"effective_files"`
	MaxFilesPerTask  int                   `json:"max_files_per_task"`
	TopDirectories   []BudgetDirectoryItem `json:"top_directories"`
	TopDocTypes      []BudgetDocTypeItem   `json:"top_doc_types"`
	Suggestions      []string              `json:"suggestions"`
}

// BudgetCheckInput 确定性预算预检入参。
type BudgetCheckInput struct {
	IR               *codeir.CodeIRBundle
	DocTypes         []*doctype.AssetBundle
	GlossaryCached   bool
	MaxTokensPerTask int
	MaxFilesPerTask  int
	// LockedSlotsByDoc 可选：记录已存在历史内容且被锁定的槽位（跳过不计入预算）
	LockedSlotsByDoc map[string]map[string]bool
}

// BudgetCheckResult 确定性预算预检结果。
type BudgetCheckResult struct {
	Exceeded     bool
	ErrorCode    string
	ErrorMessage string
	Advice       BudgetAdvice
}

// EstimateTextTokens 保守 Token 估算：中文/非 ASCII 1.5 char/token，英文/代码 3.5 char/token（零 LLM 调用）。
func EstimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	var asciiCount, nonASCIICount float64
	for _, r := range s {
		if r <= unicode.MaxASCII {
			asciiCount++
		} else {
			nonASCIICount++
		}
	}
	est := asciiCount/3.5 + nonASCIICount/1.5
	return int(math.Ceil(est))
}

// EstimateAndCheckBudget 在发出首次 LLM HTTP 请求之前执行文件数与 Token 规模预检（SC-09）。
func EstimateAndCheckBudget(in BudgetCheckInput) BudgetCheckResult {
	maxTokens := in.MaxTokensPerTask
	if maxTokens <= 0 {
		maxTokens = 200000
	}
	maxFiles := in.MaxFilesPerTask
	if maxFiles <= 0 {
		maxFiles = 2000
	}

	// 1. 统计各目录规模与有效文件数（应用 DocType scope_profile 取并集，未设 scope_profile 则为全量）
	effectiveFiles := 0
	var topDirs []BudgetDirectoryItem
	if in.IR != nil {
		effectiveFiles = countEffectiveFilesForDocTypes(in.IR, in.DocTypes)
		topDirs = computeTopDirectories(in.IR, 5)
	}

	// 2. 预估每个分片的序列化 Token 规模
	facetTokens := estimateFacetTokens(in.IR)

	// 3. 预估 Glossary 阶段消耗（命中缓存则为 0）
	glossaryTokens := 0
	if !in.GlossaryCached && in.IR != nil {
		modCount := len(in.IR.Tree.Directories)
		if modCount == 0 {
			modCount = 1
		}
		glossaryTokens = facetTokens[codeir.FacetTree] + facetTokens[codeir.FacetDeps] + modCount*400
	}

	// 4. 逐 DocType 与活跃槽位累加预估 Token
	const glossaryPromptOverhead = 600
	const slotExpectedOutputTokens = 1200

	totalSlotsTokens := 0
	docTypeItems := make([]BudgetDocTypeItem, 0, len(in.DocTypes))

	for _, dt := range in.DocTypes {
		if dt == nil {
			continue
		}
		sysTokens := EstimateTextTokens(globalSystemSecurityPrompt) + EstimateTextTokens(dt.GenerationPromptMD)
		lockedMap := in.LockedSlotsByDoc[dt.Slug]
		dtTotal := 0

		for _, slot := range dt.Meta.Slots {
			if lockedMap != nil && lockedMap[slot.SlotID] {
				continue
			}
			sliceTok := 0
			for _, fName := range slot.IRFacets {
				sliceTok += facetTokens[fName]
			}
			slotCap := slot.TokenBudget
			if slotCap <= 0 {
				slotCap = 8000
			}
			if sliceTok > slotCap {
				sliceTok = slotCap
			}
			expectedOut := slotExpectedOutputTokens
			if dt.Meta.MaxOutputTokens > 0 && dt.Meta.MaxOutputTokens/maxInt(len(dt.Meta.Slots), 1) < expectedOut {
				expectedOut = dt.Meta.MaxOutputTokens / maxInt(len(dt.Meta.Slots), 1)
			}
			dtTotal += sysTokens + glossaryPromptOverhead + sliceTok + expectedOut
		}
		totalSlotsTokens += dtTotal
		docTypeItems = append(docTypeItems, BudgetDocTypeItem{
			Slug:            dt.Slug,
			EstimatedTokens: dtTotal,
		})
	}

	sort.Slice(docTypeItems, func(i, j int) bool {
		if docTypeItems[i].EstimatedTokens != docTypeItems[j].EstimatedTokens {
			return docTypeItems[i].EstimatedTokens > docTypeItems[j].EstimatedTokens
		}
		return docTypeItems[i].Slug < docTypeItems[j].Slug
	})
	if len(docTypeItems) > 3 {
		docTypeItems = docTypeItems[:3]
	}

	totalEstimated := glossaryTokens + totalSlotsTokens

	advice := BudgetAdvice{
		EstimatedTokens:  totalEstimated,
		MaxTokensPerTask: maxTokens,
		EffectiveFiles:   effectiveFiles,
		MaxFilesPerTask:  maxFiles,
		TopDirectories:   topDirs,
		TopDocTypes:      docTypeItems,
		Suggestions:      buildBudgetSuggestions(topDirs, docTypeItems, effectiveFiles > maxFiles, totalEstimated > maxTokens),
	}

	if effectiveFiles > maxFiles {
		return BudgetCheckResult{
			Exceeded:  true,
			ErrorCode: ErrorCodeFileBudgetExceeded,
			ErrorMessage: fmt.Sprintf(
				"有效分析文件数 %d 超出单任务上限 %d，已在首次 LLM 调用前熔断",
				effectiveFiles, maxFiles,
			),
			Advice: advice,
		}
	}

	if totalEstimated > maxTokens {
		return BudgetCheckResult{
			Exceeded:  true,
			ErrorCode: ErrorCodeTokenBudgetExceeded,
			ErrorMessage: fmt.Sprintf(
				"预估消耗 %d Tokens 超出单任务上限 %d Tokens，已在首次 LLM 调用前熔断且未发出 LLM 请求",
				totalEstimated, maxTokens,
			),
			Advice: advice,
		}
	}

	return BudgetCheckResult{
		Exceeded: false,
		Advice:   advice,
	}
}

func countEffectiveFilesForDocTypes(ir *codeir.CodeIRBundle, docTypes []*doctype.AssetBundle) int {
	if ir == nil {
		return 0
	}
	if len(docTypes) == 0 {
		return ir.Tree.TotalFiles
	}
	walked := make([]sandbox.WalkedFile, 0, len(ir.Tree.Files))
	for _, f := range ir.Tree.Files {
		walked = append(walked, sandbox.WalkedFile{
			RelPath:  f.RelPath,
			Size:     f.Size,
			Language: f.Language,
		})
	}
	union := make(map[string]bool, len(walked))
	for _, dt := range docTypes {
		if dt == nil {
			continue
		}
		filtered := sandbox.FilterByScopeProfile(walked, dt.Meta.ScopeProfile.ToSandboxProfile())
		for _, f := range filtered {
			union[f.RelPath] = true
		}
	}
	if len(union) == 0 {
		return ir.Tree.TotalFiles
	}
	return len(union)
}

func computeTopDirectories(ir *codeir.CodeIRBundle, topN int) []BudgetDirectoryItem {
	type dirAgg struct {
		bytes int64
		files int
	}
	byDir := make(map[string]*dirAgg)
	for _, f := range ir.Tree.Files {
		dir := path.Dir(f.RelPath)
		if dir == "." {
			dir = "(root)"
		}
		agg, ok := byDir[dir]
		if !ok {
			agg = &dirAgg{}
			byDir[dir] = agg
		}
		agg.bytes += f.Size
		agg.files++
	}

	items := make([]BudgetDirectoryItem, 0, len(byDir))
	for d, agg := range byDir {
		est := int(math.Ceil(float64(agg.bytes) / 3.5))
		if est < 10 {
			est = 10
		}
		items = append(items, BudgetDirectoryItem{
			Path:            d,
			EstimatedTokens: est,
			FileCount:       agg.files,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EstimatedTokens != items[j].EstimatedTokens {
			return items[i].EstimatedTokens > items[j].EstimatedTokens
		}
		return items[i].Path < items[j].Path
	})
	if len(items) > topN {
		items = items[:topN]
	}
	return items
}

func estimateFacetTokens(ir *codeir.CodeIRBundle) map[string]int {
	res := make(map[string]int, len(codeir.AllFacetNames))
	if ir == nil {
		return res
	}
	facets := map[string]any{
		codeir.FacetTree:              ir.Tree,
		codeir.FacetBuild:             ir.Build,
		codeir.FacetDeps:              ir.Deps,
		codeir.FacetConfigKeys:        ir.ConfigKeys,
		codeir.FacetSymbols:           ir.Symbols,
		codeir.FacetIDLContracts:      ir.IDLContracts,
		codeir.FacetHTTPRoutes:        ir.HTTPRoutes,
		codeir.FacetDataModels:        ir.DataModels,
		codeir.FacetRuntimePrimitives: ir.RuntimePrimitives,
	}
	for name, val := range facets {
		b, err := json.Marshal(val)
		if err != nil {
			continue
		}
		res[name] = EstimateTextTokens(string(b))
	}
	return res
}

func buildBudgetSuggestions(topDirs []BudgetDirectoryItem, topDocs []BudgetDocTypeItem, fileExceeded, tokenExceeded bool) []string {
	var suggestions []string
	if len(topDirs) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("建议按 Monorepo 子目录拆分分析范围（当前最大目录 %s 含 %d 文件，预估 %d Tokens）",
			topDirs[0].Path, topDirs[0].FileCount, topDirs[0].EstimatedTokens))
	}
	suggestions = append(suggestions, "建议在仓库根目录补充 .archdocignore 过滤测试夹具、历史归档或自动生成目录")
	if tokenExceeded && len(topDocs) > 1 {
		suggestions = append(suggestions, fmt.Sprintf("建议分批勾选 DocType 生成（当前消耗最高的文档类型为 %s，预估 %d Tokens）",
			topDocs[0].Slug, topDocs[0].EstimatedTokens))
	}
	if fileExceeded {
		suggestions = append(suggestions, "可联系管理员适当调高 max_files_per_task 或通过 scope_profile 收敛扫描范围")
	}
	return suggestions
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
