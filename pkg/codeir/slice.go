package codeir

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"code-archdoc/pkg/sandbox"
)

const (
	maxModuleContextEdges   = 20
	maxModuleContextSymbols = 50
	maxModuleContextBytes   = 12000 // ~4K Token 预算硬上限
)

// SliceByModule 从全仓基础 CodeIRBundle 中零 I/O 确定性切出指定子目录的 9 大标准分片（相对路径化，与独立抽取子目录 Golden 等价）
// 以及第 10 分片 ModuleContextFacet（跨模块上下游依赖边、外部契约符号签名、父级构建挂载指令与只读白名单；ADR-04 D-2）。
func SliceByModule(base *CodeIRBundle, subdir string, modules []string) (*CodeIRBundle, *ModuleContextFacet, error) {
	if base == nil {
		return nil, nil, fmt.Errorf("base CodeIRBundle 不能为空")
	}
	normSub, err := sandbox.NormalizeSubdir(subdir)
	if err != nil {
		return nil, nil, err
	}
	if normSub == "" {
		return nil, nil, fmt.Errorf("SliceByModule 仅用于非空子目录范围")
	}

	prefix := normSub + "/"
	inSubdir := func(relPath string) bool {
		clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(relPath, "\\", "/")), "./")
		return strings.HasPrefix(clean, prefix)
	}
	stripSub := func(relPath string) string {
		clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(relPath, "\\", "/")), "./")
		return strings.TrimPrefix(clean, prefix)
	}
	pyModPrefix := strings.ReplaceAll(normSub, "/", ".") + "."

	sliced := &CodeIRBundle{
		SchemaVersion: SchemaVersion,
		Tree: TreeFacet{
			SchemaVersion:       SchemaVersion,
			LanguageStats:       make(map[string]LangStat),
			SkippedNoiseSummary: base.Tree.SkippedNoiseSummary,
		},
		Build:             BuildFacet{SchemaVersion: SchemaVersion},
		Deps:              DepsFacet{SchemaVersion: SchemaVersion},
		ConfigKeys:        ConfigKeysFacet{SchemaVersion: SchemaVersion},
		Symbols:           SymbolsFacet{SchemaVersion: SchemaVersion},
		IDLContracts:      IDLContractsFacet{SchemaVersion: SchemaVersion},
		HTTPRoutes:        HTTPRoutesFacet{SchemaVersion: SchemaVersion},
		DataModels:        DataModelsFacet{SchemaVersion: SchemaVersion},
		RuntimePrimitives: RuntimePrimitivesFacet{SchemaVersion: SchemaVersion},
		ExtractDurationMs: base.ExtractDurationMs,
	}

	// 1. Tree 分片切片与重聚合
	dirMap := make(map[string]*DirectoryStat)
	for _, f := range base.Tree.Files {
		if !inSubdir(f.RelPath) {
			continue
		}
		rel := stripSub(f.RelPath)
		sliced.Tree.TotalFiles++
		sliced.Tree.TotalLOC += f.LOC
		ls := sliced.Tree.LanguageStats[f.Language]
		ls.Files++
		ls.LOC += f.LOC
		ls.Bytes += f.Size
		sliced.Tree.LanguageStats[f.Language] = ls

		sliced.Tree.Files = append(sliced.Tree.Files, FileSummary{
			RelPath:  rel,
			Language: f.Language,
			Size:     f.Size,
			LOC:      f.LOC,
		})

		dirPath := path.Dir(rel)
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
		ds.LOC += f.LOC
		ds.LanguageCounts[f.Language]++
		if len(ds.Representative) < 5 {
			ds.Representative = append(ds.Representative, rel)
		}
	}
	for _, ds := range dirMap {
		sliced.Tree.Directories = append(sliced.Tree.Directories, *ds)
	}
	sort.Slice(sliced.Tree.Directories, func(i, j int) bool {
		return sliced.Tree.Directories[i].Path < sliced.Tree.Directories[j].Path
	})

	// 2. Build 分片切片
	buildSysSet := make(map[string]bool)
	for _, f := range sliced.Tree.Files {
		baseName := strings.ToLower(path.Base(f.RelPath))
		switch {
		case baseName == "cmakelists.txt" || strings.HasSuffix(baseName, ".cmake"):
			buildSysSet["cmake"] = true
		case baseName == "makefile" || baseName == "gnumakefile":
			buildSysSet["makefile"] = true
		case baseName == "build" || baseName == "build.bazel":
			buildSysSet["bazel"] = true
		case baseName == "pom.xml":
			buildSysSet["maven"] = true
		case baseName == "build.gradle" || baseName == "build.gradle.kts":
			buildSysSet["gradle"] = true
		}
	}
	for _, t := range base.Build.Targets {
		if !inSubdir(t.Meta.Span.FilePath) {
			continue
		}
		cp := t
		cp.Meta.Span.FilePath = stripSub(t.Meta.Span.FilePath)
		if t.BuildSystem != "" {
			buildSysSet[t.BuildSystem] = true
		}
		sliced.Build.Targets = append(sliced.Build.Targets, cp)
	}
	for _, opt := range base.Build.CompileOptions {
		if !inSubdir(opt.Meta.Span.FilePath) {
			continue
		}
		cp := opt
		cp.Meta.Span.FilePath = stripSub(opt.Meta.Span.FilePath)
		sliced.Build.CompileOptions = append(sliced.Build.CompileOptions, cp)
	}
	for _, sc := range base.Build.Scripts {
		if !inSubdir(sc.Meta.Span.FilePath) {
			continue
		}
		cp := sc
		cp.Meta.Span.FilePath = stripSub(sc.Meta.Span.FilePath)
		sliced.Build.Scripts = append(sliced.Build.Scripts, cp)
	}
	for _, sd := range base.Build.Subdirectories {
		if !inSubdir(sd.ParentBuildFile) {
			continue
		}
		cp := sd
		cp.ParentBuildFile = stripSub(sd.ParentBuildFile)
		cp.Meta.Span.FilePath = stripSub(sd.Meta.Span.FilePath)
		if inSubdir(sd.Subdirectory) {
			cp.Subdirectory = stripSub(sd.Subdirectory)
		}
		sliced.Build.Subdirectories = append(sliced.Build.Subdirectories, cp)
	}
	for _, inc := range base.Build.IncludeDirs {
		if !inSubdir(inc.BuildFile) {
			continue
		}
		cp := inc
		cp.BuildFile = stripSub(inc.BuildFile)
		cp.Meta.Span.FilePath = stripSub(inc.Meta.Span.FilePath)
		if inSubdir(inc.IncludeDir) {
			cp.IncludeDir = stripSub(inc.IncludeDir)
		}
		sliced.Build.IncludeDirs = append(sliced.Build.IncludeDirs, cp)
	}
	for bs := range buildSysSet {
		sliced.Build.BuildSystems = append(sliced.Build.BuildSystems, bs)
	}
	sort.Strings(sliced.Build.BuildSystems)

	// 3. Deps 分片切片与子模块内部 InternalModules 重算
	for _, dep := range base.Deps.ExternalDeps {
		if !inSubdir(dep.SourceFile) {
			continue
		}
		cp := dep
		cp.SourceFile = stripSub(dep.SourceFile)
		cp.Meta.Span.FilePath = stripSub(dep.Meta.Span.FilePath)
		sliced.Deps.ExternalDeps = append(sliced.Deps.ExternalDeps, cp)
	}
	internalEdges := make(map[string]int)
	for _, imp := range base.Deps.FileImports {
		if !inSubdir(imp.SourceFile) {
			continue
		}
		cp := imp
		cp.SourceFile = stripSub(imp.SourceFile)
		cp.Meta.Span.FilePath = stripSub(imp.Meta.Span.FilePath)
		sliced.Deps.FileImports = append(sliced.Deps.FileImports, cp)

		if imp.Kind == "cpp_include" {
			fromMod := topModuleDir(cp.SourceFile)
			toMod := path.Dir(imp.RawTarget)
			if toMod == "." || toMod == "" {
				toMod = imp.RawTarget
			}
			if fromMod != toMod {
				internalEdges[fromMod+"->"+toMod]++
			}
		}
	}
	for k, w := range internalEdges {
		parts := strings.SplitN(k, "->", 2)
		if len(parts) == 2 {
			sliced.Deps.InternalModules = append(sliced.Deps.InternalModules, InternalModuleEdge{
				From:   parts[0],
				To:     parts[1],
				Weight: w,
			})
		}
	}
	sort.Slice(sliced.Deps.InternalModules, func(i, j int) bool {
		if sliced.Deps.InternalModules[i].From != sliced.Deps.InternalModules[j].From {
			return sliced.Deps.InternalModules[i].From < sliced.Deps.InternalModules[j].From
		}
		return sliced.Deps.InternalModules[i].To < sliced.Deps.InternalModules[j].To
	})

	// 4. ConfigKeys 分片切片
	for _, ck := range base.ConfigKeys.Keys {
		if !inSubdir(ck.SourceFile) {
			continue
		}
		cp := ck
		cp.SourceFile = stripSub(ck.SourceFile)
		cp.Meta.Span.FilePath = stripSub(ck.Meta.Span.FilePath)
		if cp.IsSensitive {
			sliced.ConfigKeys.RedactedSecrets++
		}
		sliced.ConfigKeys.Keys = append(sliced.ConfigKeys.Keys, cp)
	}

	// 5. Symbols 分片切片（含 Python FQSymbol 子目录前缀剥离与跨模块 DeclSpan 清理）
	for _, sym := range base.Symbols.Symbols {
		if !inSubdir(sym.Meta.Span.FilePath) {
			continue
		}
		cp := sym
		cp.Meta.Span.FilePath = stripSub(sym.Meta.Span.FilePath)
		if cp.Language == "python" && strings.HasPrefix(cp.FQSymbol, pyModPrefix) {
			cp.FQSymbol = strings.TrimPrefix(cp.FQSymbol, pyModPrefix)
		}
		if cp.DeclSpan != nil {
			if inSubdir(cp.DeclSpan.FilePath) {
				ds := *cp.DeclSpan
				ds.FilePath = stripSub(ds.FilePath)
				cp.DeclSpan = &ds
			} else {
				cp.DeclSpan = nil
			}
		}
		sliced.Symbols.Symbols = append(sliced.Symbols.Symbols, cp)
	}
	sort.Slice(sliced.Symbols.Symbols, func(i, j int) bool {
		if sliced.Symbols.Symbols[i].FQSymbol != sliced.Symbols.Symbols[j].FQSymbol {
			return sliced.Symbols.Symbols[i].FQSymbol < sliced.Symbols.Symbols[j].FQSymbol
		}
		return sliced.Symbols.Symbols[i].Meta.Span.FilePath < sliced.Symbols.Symbols[j].Meta.Span.FilePath
	})

	// 6. IDLContracts 分片切片
	for _, pf := range base.IDLContracts.Protos {
		if !inSubdir(pf.FilePath) {
			continue
		}
		cp := pf
		cp.FilePath = stripSub(pf.FilePath)
		cp.Meta.Span.FilePath = stripSub(pf.Meta.Span.FilePath)
		if len(pf.Services) > 0 {
			cp.Services = make([]ProtoServiceFact, len(pf.Services))
			for i, svc := range pf.Services {
				sCopy := svc
				sCopy.Meta.Span.FilePath = stripSub(svc.Meta.Span.FilePath)
				if len(svc.RPCs) > 0 {
					sCopy.RPCs = make([]ProtoRPCFact, len(svc.RPCs))
					for j, rpc := range svc.RPCs {
						rCopy := rpc
						rCopy.Meta.Span.FilePath = stripSub(rpc.Meta.Span.FilePath)
						sCopy.RPCs[j] = rCopy
					}
				}
				cp.Services[i] = sCopy
			}
		}
		if len(pf.Messages) > 0 {
			cp.Messages = make([]ProtoMessageFact, len(pf.Messages))
			for i, msg := range pf.Messages {
				mCopy := msg
				mCopy.Meta.Span.FilePath = stripSub(msg.Meta.Span.FilePath)
				cp.Messages[i] = mCopy
			}
		}
		if len(pf.Enums) > 0 {
			cp.Enums = make([]ProtoEnumFact, len(pf.Enums))
			for i, en := range pf.Enums {
				eCopy := en
				eCopy.Meta.Span.FilePath = stripSub(en.Meta.Span.FilePath)
				cp.Enums[i] = eCopy
			}
		}
		sliced.IDLContracts.Protos = append(sliced.IDLContracts.Protos, cp)
	}

	// 7. HTTPRoutes 分片切片（含 Python HandlerFQSymbol 子目录前缀剥离）
	for _, rt := range base.HTTPRoutes.Routes {
		if !inSubdir(rt.Meta.Span.FilePath) {
			continue
		}
		cp := rt
		cp.Meta.Span.FilePath = stripSub(rt.Meta.Span.FilePath)
		if (cp.Framework == "fastapi" || cp.Framework == "flask") && strings.HasPrefix(cp.HandlerFQSymbol, pyModPrefix) {
			cp.HandlerFQSymbol = strings.TrimPrefix(cp.HandlerFQSymbol, pyModPrefix)
		}
		sliced.HTTPRoutes.Routes = append(sliced.HTTPRoutes.Routes, cp)
	}

	// 8. DataModels 分片切片
	for _, dm := range base.DataModels.Entities {
		if !inSubdir(dm.Meta.Span.FilePath) {
			continue
		}
		cp := dm
		cp.Meta.Span.FilePath = stripSub(dm.Meta.Span.FilePath)
		sliced.DataModels.Entities = append(sliced.DataModels.Entities, cp)
	}

	// 9. RuntimePrimitives 分片切片
	for _, pr := range base.RuntimePrimitives.Primitives {
		if !inSubdir(pr.Meta.Span.FilePath) {
			continue
		}
		cp := pr
		cp.Meta.Span.FilePath = stripSub(pr.Meta.Span.FilePath)
		if strings.HasPrefix(cp.EnclosingFQSymbol, pyModPrefix) {
			cp.EnclosingFQSymbol = strings.TrimPrefix(cp.EnclosingFQSymbol, pyModPrefix)
		}
		sliced.RuntimePrimitives.Primitives = append(sliced.RuntimePrimitives.Primitives, cp)
	}

	for _, dg := range base.Diagnostics {
		if !inSubdir(dg.FilePath) {
			continue
		}
		cp := dg
		cp.FilePath = stripSub(dg.FilePath)
		sliced.Diagnostics = append(sliced.Diagnostics, cp)
	}

	// 10. 构建第 10 分片 ModuleContextFacet（跨模块依赖与外部符号摘要）
	modCtx := buildModuleContext(base, normSub, modules)
	return sliced, modCtx, nil
}

// BuildCrossModuleGraph 基于全仓基础 CodeIRBundle 与活跃模块列表，确定性构建全仓跨模块依赖有向图（供整仓 Roll-up 使用）。
func BuildCrossModuleGraph(base *CodeIRBundle, modules []string) []CrossModuleEdge {
	if base == nil {
		return nil
	}
	effMods := effectiveModuleList(base, modules)
	resIdx := newImportResolutionIndex(base, effMods)

	type edgeKey struct {
		from string
		to   string
		kind string
	}
	weights := make(map[edgeKey]int)
	confMap := make(map[edgeKey]Confidence)

	for _, imp := range base.Deps.FileImports {
		fromMod := resIdx.ownerModule(imp.SourceFile)
		if fromMod == "" {
			continue
		}
		targetFile, targetMod, conf := resIdx.resolveImport(imp)
		_ = targetFile
		if targetMod == "" || targetMod == fromMod {
			continue
		}
		k := edgeKey{from: fromMod, to: targetMod, kind: imp.Kind}
		weights[k]++
		if confMap[k] != ConfidenceExact {
			confMap[k] = conf
		}
	}

	// 叠加 CMake / Bazel target_link_libraries 跨模块链接边
	targetOwner := make(map[string]string, len(base.Build.Targets))
	for _, t := range base.Build.Targets {
		if mod := resIdx.ownerModule(t.Meta.Span.FilePath); mod != "" {
			targetOwner[t.Name] = mod
		}
	}
	for _, t := range base.Build.Targets {
		fromMod := resIdx.ownerModule(t.Meta.Span.FilePath)
		if fromMod == "" {
			continue
		}
		for _, lib := range t.LinkLibs {
			if toMod := targetOwner[lib]; toMod != "" && toMod != fromMod {
				k := edgeKey{from: fromMod, to: toMod, kind: "cmake_link"}
				weights[k]++
				confMap[k] = ConfidenceExact
			}
		}
	}

	edges := make([]CrossModuleEdge, 0, len(weights))
	for k, w := range weights {
		edges = append(edges, CrossModuleEdge{
			FromModule: k.from,
			ToModule:   k.to,
			EdgeKind:   k.kind,
			Weight:     w,
			Confidence: confMap[k],
		})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Weight != edges[j].Weight {
			return edges[i].Weight > edges[j].Weight
		}
		if edges[i].FromModule != edges[j].FromModule {
			return edges[i].FromModule < edges[j].FromModule
		}
		if edges[i].ToModule != edges[j].ToModule {
			return edges[i].ToModule < edges[j].ToModule
		}
		return edges[i].EdgeKind < edges[j].EdgeKind
	})
	return edges
}

func buildModuleContext(base *CodeIRBundle, subdir string, modules []string) *ModuleContextFacet {
	effMods := effectiveModuleList(base, modules)
	resIdx := newImportResolutionIndex(base, effMods)

	type depAcc struct {
		target     string
		kind       string
		weight     int
		samples    map[string]bool
		confidence Confidence
	}
	upMap := make(map[string]*depAcc)
	downMap := make(map[string]*depAcc)
	referencedExtFiles := make(map[string]Confidence)
	referencedSymHints := make(map[string]bool)

	addEdge := func(m map[string]*depAcc, targetMod, kind, sampleFile string, conf Confidence) {
		key := targetMod + "::" + kind
		acc, ok := m[key]
		if !ok {
			acc = &depAcc{
				target:     targetMod,
				kind:       kind,
				samples:    make(map[string]bool),
				confidence: conf,
			}
			m[key] = acc
		}
		acc.weight++
		if conf == ConfidenceExact {
			acc.confidence = ConfidenceExact
		}
		if sampleFile != "" && len(acc.samples) < 5 {
			acc.samples[sampleFile] = true
		}
	}

	for _, imp := range base.Deps.FileImports {
		fromMod := resIdx.ownerModule(imp.SourceFile)
		targetFile, targetMod, conf := resIdx.resolveImport(imp)
		if fromMod == subdir && targetMod != "" && targetMod != subdir {
			addEdge(upMap, targetMod, imp.Kind, targetFile, conf)
			if targetFile != "" {
				if prev, exists := referencedExtFiles[targetFile]; !exists || prev != ConfidenceExact {
					referencedExtFiles[targetFile] = conf
				}
			}
			if imp.SymbolHint != "" {
				referencedSymHints[imp.SymbolHint] = true
			}
		} else if targetMod == subdir && fromMod != "" && fromMod != subdir {
			addEdge(downMap, fromMod, imp.Kind, imp.SourceFile, conf)
		}
	}

	// C++ .cpp 在本模块但 .h DeclSpan 在外部模块的情况也自动纳入上游外部引用
	for _, sym := range base.Symbols.Symbols {
		if resIdx.ownerModule(sym.Meta.Span.FilePath) == subdir && sym.DeclSpan != nil {
			extFile := sym.DeclSpan.FilePath
			extMod := resIdx.ownerModule(extFile)
			if extMod != "" && extMod != subdir {
				addEdge(upMap, extMod, "cpp_include", extFile, ConfidenceExact)
				referencedExtFiles[extFile] = ConfidenceExact
			}
		}
	}

	// 构建系统 target_link_libraries 跨模块依赖
	targetOwner := make(map[string]string, len(base.Build.Targets))
	targetFileByName := make(map[string]string, len(base.Build.Targets))
	for _, t := range base.Build.Targets {
		if mod := resIdx.ownerModule(t.Meta.Span.FilePath); mod != "" {
			targetOwner[t.Name] = mod
			targetFileByName[t.Name] = t.Meta.Span.FilePath
		}
	}
	for _, t := range base.Build.Targets {
		fromMod := resIdx.ownerModule(t.Meta.Span.FilePath)
		for _, lib := range t.LinkLibs {
			toMod := targetOwner[lib]
			if fromMod == subdir && toMod != "" && toMod != subdir {
				addEdge(upMap, toMod, "cmake_link", targetFileByName[lib], ConfidenceExact)
			} else if toMod == subdir && fromMod != "" && fromMod != subdir {
				addEdge(downMap, fromMod, "cmake_link", t.Meta.Span.FilePath, ConfidenceExact)
			}
		}
	}

	toSortedEdges := func(m map[string]*depAcc) []ModuleDepEdge {
		out := make([]ModuleDepEdge, 0, len(m))
		for _, acc := range m {
			samples := make([]string, 0, len(acc.samples))
			for sf := range acc.samples {
				samples = append(samples, sf)
			}
			sort.Strings(samples)
			out = append(out, ModuleDepEdge{
				TargetModule: acc.target,
				EdgeKind:     acc.kind,
				Weight:       acc.weight,
				SampleFiles:  samples,
				Confidence:   acc.confidence,
			})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Weight != out[j].Weight {
				return out[i].Weight > out[j].Weight
			}
			if out[i].TargetModule != out[j].TargetModule {
				return out[i].TargetModule < out[j].TargetModule
			}
			return out[i].EdgeKind < out[j].EdgeKind
		})
		if len(out) > maxModuleContextEdges {
			out = out[:maxModuleContextEdges]
		}
		return out
	}

	upstream := toSortedEdges(upMap)
	downstream := toSortedEdges(downMap)

	// 汇总外部被引用文件中的符号与 Protobuf 契约签名
	extSymbols := make([]ExternalSymbolRef, 0)
	seenExtSym := make(map[string]bool)

	for _, sym := range base.Symbols.Symbols {
		fileConf, ok := referencedExtFiles[sym.Meta.Span.FilePath]
		if !ok && !referencedSymHints[sym.FQSymbol] {
			continue
		}
		ownerMod := resIdx.ownerModule(sym.Meta.Span.FilePath)
		if ownerMod == "" || ownerMod == subdir {
			continue
		}
		if fileConf == "" {
			fileConf = ConfidenceExact
		}
		key := sym.Meta.Span.FilePath + "::" + sym.FQSymbol
		if seenExtSym[key] {
			continue
		}
		seenExtSym[key] = true
		extSymbols = append(extSymbols, ExternalSymbolRef{
			OwnerModule: ownerMod,
			FilePath:    sym.Meta.Span.FilePath,
			SymbolKind:  mapExtSymbolKind(sym.Language, sym.Kind),
			FQSymbol:    sym.FQSymbol,
			Signature:   sym.Signature,
			Span:        sym.Meta.Span,
			Fingerprint: sym.Meta.ContentFingerprint,
			Confidence:  fileConf,
		})
	}

	for _, pf := range base.IDLContracts.Protos {
		fileConf, ok := referencedExtFiles[pf.FilePath]
		if !ok {
			continue
		}
		ownerMod := resIdx.ownerModule(pf.FilePath)
		if ownerMod == "" || ownerMod == subdir {
			continue
		}
		for _, svc := range pf.Services {
			key := pf.FilePath + "::" + svc.FullName
			if !seenExtSym[key] {
				seenExtSym[key] = true
				extSymbols = append(extSymbols, ExternalSymbolRef{
					OwnerModule: ownerMod,
					FilePath:    pf.FilePath,
					SymbolKind:  "proto_service",
					FQSymbol:    svc.FullName,
					Signature:   "service " + svc.Name,
					Span:        svc.Meta.Span,
					Fingerprint: svc.Meta.ContentFingerprint,
					Confidence:  fileConf,
				})
			}
			for _, rpc := range svc.RPCs {
				fullRPC := svc.FullName + "/" + rpc.MethodName
				rKey := pf.FilePath + "::" + fullRPC
				if !seenExtSym[rKey] {
					seenExtSym[rKey] = true
					extSymbols = append(extSymbols, ExternalSymbolRef{
						OwnerModule: ownerMod,
						FilePath:    pf.FilePath,
						SymbolKind:  "proto_rpc",
						FQSymbol:    fullRPC,
						Signature:   fmt.Sprintf("rpc %s(%s) returns (%s)", rpc.MethodName, rpc.RequestType, rpc.ResponseType),
						Span:        rpc.Meta.Span,
						Fingerprint: rpc.Meta.ContentFingerprint,
						Confidence:  fileConf,
					})
				}
			}
		}
		for _, msg := range pf.Messages {
			key := pf.FilePath + "::" + msg.FullName
			if !seenExtSym[key] {
				seenExtSym[key] = true
				extSymbols = append(extSymbols, ExternalSymbolRef{
					OwnerModule: ownerMod,
					FilePath:    pf.FilePath,
					SymbolKind:  "proto_message",
					FQSymbol:    msg.FullName,
					Signature:   "message " + msg.Name,
					Span:        msg.Meta.Span,
					Fingerprint: msg.Meta.ContentFingerprint,
					Confidence:  fileConf,
				})
			}
		}
	}

	sort.Slice(extSymbols, func(i, j int) bool {
		// exact 优先于 heuristic
		if (extSymbols[i].Confidence == ConfidenceExact) != (extSymbols[j].Confidence == ConfidenceExact) {
			return extSymbols[i].Confidence == ConfidenceExact
		}
		if extSymbols[i].FilePath != extSymbols[j].FilePath {
			return extSymbols[i].FilePath < extSymbols[j].FilePath
		}
		return extSymbols[i].FQSymbol < extSymbols[j].FQSymbol
	})

	truncated := false
	if len(extSymbols) > maxModuleContextSymbols {
		extSymbols = extSymbols[:maxModuleContextSymbols]
		truncated = true
	}

	// 父级构建上下文摘要
	var buildCtx []BuildContextSnippet
	for _, sd := range base.Build.Subdirectories {
		if sd.Subdirectory == subdir && !strings.HasPrefix(sd.ParentBuildFile, subdir+"/") {
			directive := fmt.Sprintf("add_subdirectory(%s)", sd.Subdirectory)
			if sd.BuildSystem == "maven" {
				directive = fmt.Sprintf("<module>%s</module>", sd.Subdirectory)
			}
			buildCtx = append(buildCtx, BuildContextSnippet{
				BuildFile: sd.ParentBuildFile,
				Directive: directive,
				Span:      sd.Meta.Span,
			})
		}
	}

	// 汇总外部只读白名单文件集合
	whitelistSet := make(map[string]bool)
	for f := range referencedExtFiles {
		whitelistSet[f] = true
	}
	for _, sym := range extSymbols {
		if sym.FilePath != "" {
			whitelistSet[sym.FilePath] = true
		}
	}
	for _, e := range upstream {
		for _, sf := range e.SampleFiles {
			whitelistSet[sf] = true
		}
	}
	for _, bc := range buildCtx {
		if bc.BuildFile != "" {
			whitelistSet[bc.BuildFile] = true
		}
	}
	whitelist := make([]string, 0, len(whitelistSet))
	for f := range whitelistSet {
		whitelist = append(whitelist, f)
	}
	sort.Strings(whitelist)

	facet := &ModuleContextFacet{
		SchemaVersion:         SchemaVersion,
		ModulePath:            subdir,
		UpstreamModules:       upstream,
		DownstreamModules:     downstream,
		ReferencedExtSymbols:  extSymbols,
		RepoBuildContext:      buildCtx,
		ExternalFileWhitelist: whitelist,
		Truncated:             truncated,
	}

	// 4K Token (~12KB JSON) 预算硬门禁自动裁剪
	for {
		raw, err := json.Marshal(facet)
		if err != nil || len(raw) <= maxModuleContextBytes {
			break
		}
		facet.Truncated = true
		if len(facet.ReferencedExtSymbols) > 10 {
			facet.ReferencedExtSymbols = facet.ReferencedExtSymbols[:len(facet.ReferencedExtSymbols)/2]
			continue
		}
		if len(facet.DownstreamModules) > 5 {
			facet.DownstreamModules = facet.DownstreamModules[:len(facet.DownstreamModules)/2]
			continue
		}
		if len(facet.UpstreamModules) > 5 {
			facet.UpstreamModules = facet.UpstreamModules[:len(facet.UpstreamModules)/2]
			continue
		}
		break
	}

	return facet
}

type importResolutionIndex struct {
	base       *CodeIRBundle
	modules    []string // 按长度降序排列以便最长前缀优先匹配
	fileSet    map[string]bool
	bySuffix   map[string][]string
	javaSymMap map[string]string
	pySymMap   map[string]string
}

func newImportResolutionIndex(base *CodeIRBundle, modules []string) *importResolutionIndex {
	sortedMods := append([]string(nil), modules...)
	sort.Slice(sortedMods, func(i, j int) bool {
		if len(sortedMods[i]) != len(sortedMods[j]) {
			return len(sortedMods[i]) > len(sortedMods[j])
		}
		return sortedMods[i] < sortedMods[j]
	})

	fileSet := make(map[string]bool, len(base.Tree.Files))
	bySuffix := make(map[string][]string)
	for _, f := range base.Tree.Files {
		clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(f.RelPath, "\\", "/")), "./")
		fileSet[clean] = true
		baseName := path.Base(clean)
		bySuffix[baseName] = append(bySuffix[baseName], clean)
	}

	javaSymMap := make(map[string]string)
	pySymMap := make(map[string]string)
	for _, sym := range base.Symbols.Symbols {
		switch sym.Language {
		case "java":
			if sym.FQSymbol != "" {
				javaSymMap[sym.FQSymbol] = sym.Meta.Span.FilePath
			}
		case "python":
			if sym.FQSymbol != "" {
				pySymMap[sym.FQSymbol] = sym.Meta.Span.FilePath
			}
		}
	}

	return &importResolutionIndex{
		base:       base,
		modules:    sortedMods,
		fileSet:    fileSet,
		bySuffix:   bySuffix,
		javaSymMap: javaSymMap,
		pySymMap:   pySymMap,
	}
}

func (idx *importResolutionIndex) ownerModule(repoRelPath string) string {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(repoRelPath, "\\", "/")), "./")
	if clean == "" || clean == "." {
		return ""
	}
	for _, mod := range idx.modules {
		if clean == mod || strings.HasPrefix(clean, mod+"/") {
			return mod
		}
	}
	if slash := strings.IndexByte(clean, '/'); slash > 0 {
		return clean[:slash]
	}
	return ""
}

func (idx *importResolutionIndex) resolveImport(imp FileImportFact) (targetFile string, targetMod string, conf Confidence) {
	raw := strings.TrimSpace(imp.RawTarget)
	if raw == "" {
		return "", "", ConfidenceHeuristic
	}

	switch imp.Kind {
	case "cpp_include", "proto_import":
		// 1. 当前源文件所在目录相对解析
		srcDir := path.Dir(imp.SourceFile)
		if srcDir != "." && srcDir != "" {
			cand := path.Clean(path.Join(srcDir, raw))
			if idx.fileSet[cand] {
				return cand, idx.ownerModule(cand), ConfidenceExact
			}
		}
		// 2. 构建系统声明的 IncludeDirs 搜索路径
		for _, inc := range idx.base.Build.IncludeDirs {
			cand := path.Clean(path.Join(inc.IncludeDir, raw))
			if idx.fileSet[cand] {
				return cand, idx.ownerModule(cand), ConfidenceExact
			}
		}
		// 3. 仓库根目录直连（如 #include "common/include/types.h"）
		candRoot := strings.TrimPrefix(path.Clean(raw), "./")
		if idx.fileSet[candRoot] {
			return candRoot, idx.ownerModule(candRoot), ConfidenceExact
		}
		// 4. 在每个模块及其 include/ 子目录下尝试解析（如 #include "types.h" 或 "common/types.h"）
		for _, mod := range idx.modules {
			for _, subCand := range []string{
				path.Clean(path.Join(mod, raw)),
				path.Clean(path.Join(mod, "include", raw)),
			} {
				if idx.fileSet[subCand] {
					return subCand, mod, ConfidenceHeuristic
				}
			}
		}
		// 5. 文件名后缀唯一匹配兜底
		baseName := path.Base(raw)
		if cands := idx.bySuffix[baseName]; len(cands) > 0 {
			var suffixMatches []string
			wantSuffix := "/" + strings.TrimPrefix(raw, "/")
			for _, c := range cands {
				if c == raw || strings.HasSuffix(c, wantSuffix) {
					suffixMatches = append(suffixMatches, c)
				}
			}
			if len(suffixMatches) == 1 {
				return suffixMatches[0], idx.ownerModule(suffixMatches[0]), ConfidenceHeuristic
			}
		}

	case "java_import":
		if fp, ok := idx.javaSymMap[raw]; ok {
			return fp, idx.ownerModule(fp), ConfidenceExact
		}
		// 尝试转换为 .java 路径后缀匹配
		slashPath := strings.ReplaceAll(strings.TrimSuffix(raw, ".*"), ".", "/") + ".java"
		baseName := path.Base(slashPath)
		for _, c := range idx.bySuffix[baseName] {
			if strings.HasSuffix(c, slashPath) {
				return c, idx.ownerModule(c), ConfidenceExact
			}
		}

	case "py_import":
		if imp.SymbolHint != "" {
			if fp, ok := idx.pySymMap[imp.SymbolHint]; ok {
				return fp, idx.ownerModule(fp), ConfidenceExact
			}
		}
		if fp, ok := idx.pySymMap[raw]; ok {
			return fp, idx.ownerModule(fp), ConfidenceExact
		}
		relPy := strings.ReplaceAll(raw, ".", "/")
		for _, cand := range []string{relPy + ".py", path.Join(relPy, "__init__.py")} {
			if idx.fileSet[cand] {
				return cand, idx.ownerModule(cand), ConfidenceExact
			}
		}
	}

	return "", "", ConfidenceHeuristic
}

func effectiveModuleList(base *CodeIRBundle, modules []string) []string {
	if len(modules) > 0 {
		out := make([]string, 0, len(modules))
		seen := make(map[string]bool, len(modules))
		for _, m := range modules {
			norm, err := sandbox.NormalizeSubdir(m)
			if err == nil && norm != "" && !seen[norm] {
				seen[norm] = true
				out = append(out, norm)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	discovered := DiscoverModules(&base.Tree, ModuleDiscoveryPolicy{
		Strategy:          "top_level_dirs",
		MinEffectiveFiles: 1,
	}, nil)
	out := make([]string, 0, len(discovered))
	for _, d := range discovered {
		out = append(out, d.Subdirectory)
	}
	return out
}

func mapExtSymbolKind(lang, kind string) string {
	switch lang {
	case "java":
		if kind == "method" || kind == "function" {
			return "java_method"
		}
		return "java_class"
	case "python":
		if kind == "function" || kind == "method" {
			return "py_func"
		}
		return "py_class"
	default:
		switch kind {
		case "class":
			return "cpp_class"
		case "struct", "enum":
			return "cpp_struct"
		default:
			return "cpp_func"
		}
	}
}

// WriteModuleContextFacet 将子目录快照专属的第 10 分片 module_context.json.gz 写入 FacetStore 并返回落盘元数据与统计摘要。
func WriteModuleContextFacet(ctx context.Context, store FacetStore, loc SnapshotLoc, mc *ModuleContextFacet) (*FacetWriteMeta, FacetSummaryRecord, error) {
	if store == nil || mc == nil {
		return nil, FacetSummaryRecord{}, fmt.Errorf("store 与 module_context 不能为空")
	}
	meta, err := store.WriteFacet(ctx, loc, FacetModuleContext, mc)
	if err != nil {
		return nil, FacetSummaryRecord{}, err
	}
	exactCnt := 0
	heurCnt := 0
	for _, e := range mc.UpstreamModules {
		if e.Confidence == ConfidenceExact {
			exactCnt++
		} else {
			heurCnt++
		}
	}
	for _, s := range mc.ReferencedExtSymbols {
		if s.Confidence == ConfidenceExact {
			exactCnt++
		} else {
			heurCnt++
		}
	}
	summary := FacetSummaryRecord{
		FacetName:      FacetModuleContext,
		ExactCount:     exactCnt,
		HeuristicCount: heurCnt,
		ExtraCounts: map[string]int{
			"upstream_modules":       len(mc.UpstreamModules),
			"downstream_modules":     len(mc.DownstreamModules),
			"referenced_ext_symbols": len(mc.ReferencedExtSymbols),
		},
	}
	return meta, summary, nil
}
