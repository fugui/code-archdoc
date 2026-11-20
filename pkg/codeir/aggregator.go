package codeir

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/security"
)

// Aggregator 编排沙箱遍历、多语言插件提取、.h/.cpp 符号归并、脱敏校验与 9 大分片汇聚。
type Aggregator struct {
	walker          *sandbox.FileWalker
	buildExtractor  *BuildExtractor
	configExtractor *ConfigExtractor
	protoExtractor  *ProtoExtractor
	commonExtractor *CommonExtractor
	langExtractors  map[string][]LanguageExtractor
	redactor        *security.SecretRedactor
}

// NewAggregator 构建 Code IR 汇聚引擎（可注册外部 LanguageExtractor 插件如 pkg/codeir/treesitter）。
func NewAggregator(extraExtractors ...LanguageExtractor) *Aggregator {
	agg := &Aggregator{
		walker:          sandbox.NewFileWalker(),
		buildExtractor:  NewBuildExtractor(),
		configExtractor: NewConfigExtractor(),
		protoExtractor:  NewProtoExtractor(),
		commonExtractor: NewCommonExtractor(),
		langExtractors:  make(map[string][]LanguageExtractor),
		redactor:        security.NewSecretRedactor(),
	}
	for _, ext := range extraExtractors {
		agg.RegisterExtractor(ext)
	}
	return agg
}

// RegisterExtractor 注册语言级提取插件（支持同一语言挂载多个插件）。
func (a *Aggregator) RegisterExtractor(ext LanguageExtractor) {
	if ext == nil {
		return
	}
	for _, lang := range ext.SupportedLanguages() {
		a.langExtractors[strings.ToLower(lang)] = append(a.langExtractors[strings.ToLower(lang)], ext)
	}
}

// ExtractSingleFileSymbols 对单个源码文件执行轻量符号抽取（供源码抽屉按符号重定位及 M6 符号级 Code Drift 复用）。
func (a *Aggregator) ExtractSingleFileSymbols(ctx context.Context, relPath, lang string, content []byte) []SymbolFact {
	if ctx == nil {
		ctx = context.Background()
	}
	normLang := strings.ToLower(strings.TrimSpace(lang))
	entry := FileEntry{
		RelPath:  relPath,
		Language: normLang,
		Size:     int64(len(content)),
	}
	var out []SymbolFact
	for _, ext := range a.langExtractors[normLang] {
		res, err := ext.ExtractFile(ctx, entry, content)
		if err != nil || res == nil {
			continue
		}
		out = append(out, res.Symbols...)
	}
	return out
}

// ExtractDirectory 扫描目标仓库（或子目录），执行零 LLM 确定性抽取并输出完整 CodeIRBundle。
func (a *Aggregator) ExtractDirectory(ctx context.Context, repoRoot, subdir string, walkOpts sandbox.WalkOptions) (*CodeIRBundle, error) {
	start := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}

	walkRes, err := a.walker.Walk(ctx, repoRoot, subdir, walkOpts)
	if err != nil {
		return nil, err
	}

	bundle := &CodeIRBundle{
		SchemaVersion: SchemaVersion,
		Tree: TreeFacet{
			SchemaVersion:       SchemaVersion,
			LanguageStats:       make(map[string]LangStat),
			SkippedNoiseSummary: walkRes.Stats.SkippedByRule,
		},
		Build:             BuildFacet{SchemaVersion: SchemaVersion},
		Deps:              DepsFacet{SchemaVersion: SchemaVersion},
		ConfigKeys:        ConfigKeysFacet{SchemaVersion: SchemaVersion},
		Symbols:           SymbolsFacet{SchemaVersion: SchemaVersion},
		IDLContracts:      IDLContractsFacet{SchemaVersion: SchemaVersion},
		HTTPRoutes:        HTTPRoutesFacet{SchemaVersion: SchemaVersion},
		DataModels:        DataModelsFacet{SchemaVersion: SchemaVersion},
		RuntimePrimitives: RuntimePrimitivesFacet{SchemaVersion: SchemaVersion},
	}

	dirMap := make(map[string]*DirectoryStat)
	buildSysSet := make(map[string]bool)
	edgeWeight := make(map[string]int)

	for _, wf := range walkRes.Files {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		content, readErr := os.ReadFile(wf.AbsPath)
		if readErr != nil {
			bundle.Diagnostics = append(bundle.Diagnostics, ParseDiagnostic{
				FilePath: wf.RelPath,
				Message:  fmt.Sprintf("读取文件失败: %v", readErr),
			})
			continue
		}

		loc := countLines(content)
		bundle.Tree.TotalFiles++
		bundle.Tree.TotalLOC += loc

		ls := bundle.Tree.LanguageStats[wf.Language]
		ls.Files++
		ls.LOC += loc
		ls.Bytes += wf.Size
		bundle.Tree.LanguageStats[wf.Language] = ls

		bundle.Tree.Files = append(bundle.Tree.Files, FileSummary{
			RelPath:  wf.RelPath,
			Language: wf.Language,
			Size:     wf.Size,
			LOC:      loc,
		})

		dirPath := path.Dir(wf.RelPath)
		if dirPath == "." {
			dirPath = ""
		}
		ds, ok := dirMap[dirPath]
		if !ok {
			ds = &DirectoryStat{
				Path:           dirPath,
				LanguageCounts: make(map[string]int),
			}
			dirMap[dirPath] = ds
		}
		ds.FileCount++
		ds.LOC += loc
		ds.LanguageCounts[wf.Language]++
		if len(ds.Representative) < 5 {
			ds.Representative = append(ds.Representative, wf.RelPath)
		}

		fe := FileEntry{
			RelPath:  wf.RelPath,
			AbsPath:  wf.AbsPath,
			Language: wf.Language,
			Size:     wf.Size,
		}

		// 1. 构建系统与 compile_commands.json 提取
		if a.buildExtractor.CanHandle(wf.RelPath) {
			if r, exErr := a.buildExtractor.ExtractFile(ctx, fe, content); exErr == nil && r != nil {
				mergeResult(bundle, r, buildSysSet, edgeWeight)
			}
		}

		// 2. 配置键提取与三级脱敏（排除 compile_commands.json）
		if wf.Language == "config" && !a.buildExtractor.CanHandle(wf.RelPath) {
			if r, exErr := a.configExtractor.ExtractFile(ctx, fe, content); exErr == nil && r != nil {
				mergeResult(bundle, r, buildSysSet, edgeWeight)
			}
		}

		// 3. 纯 Go Protobuf / CROS RPC 提取
		if wf.Language == "proto" {
			if r, exErr := a.protoExtractor.ExtractFile(ctx, fe, content); exErr == nil && r != nil {
				mergeResult(bundle, r, buildSysSet, edgeWeight)
			}
		}

		// 4. 通用静态提取（SQL DDL、本地 #include 边、轻量 C++ HTTP 路由）
		if wf.Language == "sql" || wf.Language == "c" || wf.Language == "cpp" {
			if r, exErr := a.commonExtractor.ExtractFile(ctx, fe, content); exErr == nil && r != nil {
				mergeResult(bundle, r, buildSysSet, edgeWeight)
			}
		}

		// 5. 多语言插件提取（如 pkg/codeir/treesitter C/C++ 解析器）
		for _, ext := range a.langExtractors[strings.ToLower(wf.Language)] {
			r, exErr := ext.ExtractFile(ctx, fe, content)
			if exErr != nil {
				bundle.Diagnostics = append(bundle.Diagnostics, ParseDiagnostic{
					FilePath: wf.RelPath,
					Message:  fmt.Sprintf("插件 %s 解析失败: %v", ext.Name(), exErr),
				})
				continue
			}
			if r != nil {
				mergeResult(bundle, r, buildSysSet, edgeWeight)
			}
		}
	}

	// 整理目录统计（按路径排序）
	for _, ds := range dirMap {
		bundle.Tree.Directories = append(bundle.Tree.Directories, *ds)
	}
	sort.Slice(bundle.Tree.Directories, func(i, j int) bool {
		return bundle.Tree.Directories[i].Path < bundle.Tree.Directories[j].Path
	})

	// 整理构建系统列表
	for bs := range buildSysSet {
		bundle.Build.BuildSystems = append(bundle.Build.BuildSystems, bs)
	}
	sort.Strings(bundle.Build.BuildSystems)

	// 整理内部模块依赖边
	for k, w := range edgeWeight {
		parts := strings.SplitN(k, "->", 2)
		if len(parts) == 2 {
			bundle.Deps.InternalModules = append(bundle.Deps.InternalModules, InternalModuleEdge{
				From:   parts[0],
				To:     parts[1],
				Weight: w,
			})
		}
	}
	sort.Slice(bundle.Deps.InternalModules, func(i, j int) bool {
		if bundle.Deps.InternalModules[i].From != bundle.Deps.InternalModules[j].From {
			return bundle.Deps.InternalModules[i].From < bundle.Deps.InternalModules[j].From
		}
		return bundle.Deps.InternalModules[i].To < bundle.Deps.InternalModules[j].To
	})

	// 归并 .h 声明与 .cpp 实现并去重排序
	bundle.Symbols.Symbols = mergeHeaderAndSourceSymbols(bundle.Symbols.Symbols)

	// 去重运行时原语并排序
	bundle.RuntimePrimitives.Primitives = dedupPrimitives(bundle.RuntimePrimitives.Primitives)

	// 统计配置敏感项脱敏数
	redactedCount := 0
	for _, ck := range bundle.ConfigKeys.Keys {
		if ck.IsSensitive {
			redactedCount++
		}
	}
	bundle.ConfigKeys.RedactedSecrets = redactedCount

	// 终审防线：对全部文本片段执行兜底脱敏，确保 0 明文凭据残留
	a.sanitizeBundle(bundle)

	bundle.ExtractDurationMs = time.Since(start).Milliseconds()
	return bundle, nil
}

func mergeResult(b *CodeIRBundle, r *ExtractionResult, buildSysSet map[string]bool, edgeWeight map[string]int) {
	for _, bs := range r.BuildSystems {
		buildSysSet[bs] = true
	}
	b.Build.Targets = append(b.Build.Targets, r.Build...)
	b.Build.CompileOptions = append(b.Build.CompileOptions, r.CompileOptions...)
	b.Build.Scripts = append(b.Build.Scripts, r.Scripts...)
	b.Build.Subdirectories = append(b.Build.Subdirectories, r.Subdirectories...)
	b.Build.IncludeDirs = append(b.Build.IncludeDirs, r.IncludeDirs...)
	b.Deps.ExternalDeps = append(b.Deps.ExternalDeps, r.ExternalDeps...)
	for _, edge := range r.InternalModules {
		edgeWeight[edge.From+"->"+edge.To] += edge.Weight
	}
	b.Deps.FileImports = append(b.Deps.FileImports, r.FileImports...)
	b.ConfigKeys.Keys = append(b.ConfigKeys.Keys, r.ConfigKeys...)
	b.Symbols.Symbols = append(b.Symbols.Symbols, r.Symbols...)
	b.IDLContracts.Protos = append(b.IDLContracts.Protos, r.IDLContracts...)
	b.HTTPRoutes.Routes = append(b.HTTPRoutes.Routes, r.HTTPRoutes...)
	b.DataModels.Entities = append(b.DataModels.Entities, r.DataModels...)
	b.RuntimePrimitives.Primitives = append(b.RuntimePrimitives.Primitives, r.RuntimePrimitives...)
	b.Diagnostics = append(b.Diagnostics, r.Diagnostics...)
}

// mergeHeaderAndSourceSymbols 按 fq_symbol + 归一化参数签名归并 .h 声明与 .cpp 定义（ADR-02）。
func mergeHeaderAndSourceSymbols(raw []SymbolFact) []SymbolFact {
	if len(raw) == 0 {
		return nil
	}

	type mergedEntry struct {
		sym SymbolFact
	}
	index := make(map[string]int, len(raw))
	out := make([]SymbolFact, 0, len(raw))

	for _, sym := range raw {
		key := symbolMergeKey(sym)
		existingIdx, exists := index[key]
		if !exists {
			index[key] = len(out)
			out = append(out, sym)
			continue
		}

		cur := &out[existingIdx]
		curIsHeader := isHeaderFile(cur.Meta.Span.FilePath)
		newIsHeader := isHeaderFile(sym.Meta.Span.FilePath)

		if curIsHeader && !newIsHeader {
			// 新记录是 .cpp 定义，旧记录是 .h 声明：保留定义为主坐标 + .h 为 DeclSpan
			declSpan := cur.Meta.Span
			mergedCond := cur.Meta.Span.Conditional || sym.Meta.Span.Conditional
			condExpr := sym.Meta.Span.ConditionExpr
			if condExpr == "" {
				condExpr = cur.Meta.Span.ConditionExpr
			}
			docSummary := cur.DocCommentSummary
			if docSummary == "" {
				docSummary = sym.DocCommentSummary
			}
			visibility := cur.Visibility
			if visibility == "" {
				visibility = sym.Visibility
			}
			kind := cur.Kind
			if kind == "" || kind == "function" && sym.Kind == "method" {
				kind = sym.Kind
			}
			*cur = sym
			cur.Kind = kind
			cur.Visibility = visibility
			cur.DocCommentSummary = docSummary
			cur.DeclSpan = &declSpan
			cur.Meta.Span.Conditional = mergedCond
			cur.Meta.Span.ConditionExpr = condExpr
		} else if !curIsHeader && newIsHeader {
			// 旧记录已是 .cpp 定义，补充 .h 声明坐标与可见性/条件编译标注
			declSpan := sym.Meta.Span
			cur.DeclSpan = &declSpan
			if sym.Meta.Span.Conditional {
				cur.Meta.Span.Conditional = true
				if cur.Meta.Span.ConditionExpr == "" {
					cur.Meta.Span.ConditionExpr = sym.Meta.Span.ConditionExpr
				}
			}
			if cur.Visibility == "" {
				cur.Visibility = sym.Visibility
			}
			if cur.DocCommentSummary == "" {
				cur.DocCommentSummary = sym.DocCommentSummary
			}
			if cur.Kind == "function" && sym.Kind == "method" {
				cur.Kind = "method"
			}
		} else {
			// 同类文件（如重定义或更完整结构体），保留字段更多者
			if len(sym.Fields) > len(cur.Fields) {
				*cur = sym
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].FQSymbol != out[j].FQSymbol {
			return out[i].FQSymbol < out[j].FQSymbol
		}
		return out[i].Meta.Span.FilePath < out[j].Meta.Span.FilePath
	})
	return out
}

func symbolMergeKey(sym SymbolFact) string {
	kindGroup := sym.Kind
	if kindGroup == "function" || kindGroup == "method" {
		kindGroup = "callable"
	}
	return kindGroup + "::" + sym.FQSymbol
}

func isHeaderFile(relPath string) bool {
	ext := strings.ToLower(path.Ext(relPath))
	return ext == ".h" || ext == ".hpp" || ext == ".hh" || ext == ".hxx"
}

func dedupPrimitives(raw []RuntimePrimitiveFact) []RuntimePrimitiveFact {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]RuntimePrimitiveFact, 0, len(raw))
	for _, p := range raw {
		key := fmt.Sprintf("%s|%s|%s|%s|%d", p.Category, p.APIName, p.EnclosingFQSymbol, p.Meta.Span.FilePath, p.Meta.Span.LineStart)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.Meta.Confidence = ConfidenceHeuristic
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		if out[i].APIName != out[j].APIName {
			return out[i].APIName < out[j].APIName
		}
		return out[i].Meta.Span.FilePath < out[j].Meta.Span.FilePath
	})
	return out
}

func (a *Aggregator) sanitizeBundle(b *CodeIRBundle) {
	for i := range b.Symbols.Symbols {
		if b.Symbols.Symbols[i].DocCommentSummary != "" {
			b.Symbols.Symbols[i].DocCommentSummary, _ = a.redactor.RedactText(b.Symbols.Symbols[i].DocCommentSummary)
		}
		if b.Symbols.Symbols[i].Signature != "" {
			b.Symbols.Symbols[i].Signature, _ = a.redactor.RedactText(b.Symbols.Symbols[i].Signature)
		}
	}
	for i := range b.RuntimePrimitives.Primitives {
		if b.RuntimePrimitives.Primitives[i].ContextSnippet != "" {
			redacted, _ := a.redactor.RedactText(b.RuntimePrimitives.Primitives[i].ContextSnippet)
			b.RuntimePrimitives.Primitives[i].ContextSnippet = truncateString(redacted, 160)
		}
	}
	for i := range b.Build.CompileOptions {
		if b.Build.CompileOptions[i].Value != "" {
			b.Build.CompileOptions[i].Value, _ = a.redactor.RedactText(b.Build.CompileOptions[i].Value)
		}
	}
}

// WriteBundle 将全部 9 大分片原子压缩落盘至 FacetStore，返回每个分片的落盘元数据与摘要。
func WriteBundle(ctx context.Context, store FacetStore, loc SnapshotLoc, bundle *CodeIRBundle) (map[string]*FacetWriteMeta, map[string]FacetSummaryRecord, error) {
	if store == nil || bundle == nil {
		return nil, nil, fmt.Errorf("store 与 bundle 不可为空")
	}

	payloads := map[string]any{
		FacetTree:              bundle.Tree,
		FacetBuild:             bundle.Build,
		FacetDeps:              bundle.Deps,
		FacetConfigKeys:        bundle.ConfigKeys,
		FacetSymbols:           bundle.Symbols,
		FacetIDLContracts:      bundle.IDLContracts,
		FacetHTTPRoutes:        bundle.HTTPRoutes,
		FacetDataModels:        bundle.DataModels,
		FacetRuntimePrimitives: bundle.RuntimePrimitives,
	}

	metas := make(map[string]*FacetWriteMeta, len(AllFacetNames))
	for _, name := range AllFacetNames {
		meta, err := store.WriteFacet(ctx, loc, name, payloads[name])
		if err != nil {
			return nil, nil, err
		}
		metas[name] = meta
	}

	summaries := BuildFacetSummaries(bundle)
	return metas, summaries, nil
}

// BuildFacetSummaries 为 9 大分片生成轻量元数据统计（写入 archdoc_code_ir_facets.summary_json）。
func BuildFacetSummaries(b *CodeIRBundle) map[string]FacetSummaryRecord {
	protoRPCs := 0
	for _, p := range b.IDLContracts.Protos {
		for _, svc := range p.Services {
			protoRPCs += len(svc.RPCs)
		}
	}
	return map[string]FacetSummaryRecord{
		FacetTree: {
			FacetName:  FacetTree,
			ExactCount: b.Tree.TotalFiles,
			ExtraCounts: map[string]int{
				"total_loc":   b.Tree.TotalLOC,
				"directories": len(b.Tree.Directories),
			},
		},
		FacetBuild: {
			FacetName:  FacetBuild,
			ExactCount: len(b.Build.Targets),
			ExtraCounts: map[string]int{
				"compile_options": len(b.Build.CompileOptions),
				"build_systems":   len(b.Build.BuildSystems),
			},
		},
		FacetDeps: {
			FacetName:  FacetDeps,
			ExactCount: len(b.Deps.ExternalDeps) + len(b.Deps.InternalModules),
			ExtraCounts: map[string]int{
				"external_deps":    len(b.Deps.ExternalDeps),
				"internal_modules": len(b.Deps.InternalModules),
			},
		},
		FacetConfigKeys: {
			FacetName:       FacetConfigKeys,
			ExactCount:      len(b.ConfigKeys.Keys),
			RedactedSecrets: b.ConfigKeys.RedactedSecrets,
		},
		FacetSymbols: {
			FacetName:  FacetSymbols,
			ExactCount: len(b.Symbols.Symbols),
		},
		FacetIDLContracts: {
			FacetName:  FacetIDLContracts,
			ExactCount: len(b.IDLContracts.Protos),
			ExtraCounts: map[string]int{
				"proto_rpcs": protoRPCs,
			},
		},
		FacetHTTPRoutes: {
			FacetName:  FacetHTTPRoutes,
			ExactCount: len(b.HTTPRoutes.Routes),
		},
		FacetDataModels: {
			FacetName:  FacetDataModels,
			ExactCount: len(b.DataModels.Entities),
		},
		FacetRuntimePrimitives: {
			FacetName:      FacetRuntimePrimitives,
			HeuristicCount: len(b.RuntimePrimitives.Primitives),
		},
	}
}
