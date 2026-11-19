package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"code-archdoc/pkg/codeir"
)

// CitedAnchorCandidate L2 模型输出的候选证据引用。
type CitedAnchorCandidate struct {
	FilePath   string `json:"file_path"`
	SymbolKind string `json:"symbol_kind,omitempty"`
	FQSymbol   string `json:"fq_symbol,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// EvidenceAnchor 规范化跨提交稳定证据锚点（严格对齐 ADR-03 §2.1）。
type EvidenceAnchor struct {
	AnchorID           string `json:"anchor_id"`
	AnchorKey          string `json:"anchor_key"`
	FilePath           string `json:"file_path"`
	SymbolKind         string `json:"symbol_kind"`
	FQSymbol           string `json:"fq_symbol"`
	ContentFingerprint string `json:"content_fingerprint"`
	CommitSHA          string `json:"commit_sha"`
	LineStart          int    `json:"line_start"`
	LineEnd            int    `json:"line_end"`
	Confidence         string `json:"confidence"` // exact | heuristic
}

// IRIndex 对 L1 Code IR 构建多维度倒排索引，支撑证据对齐与 Gate 1 存在性校验。
type IRIndex struct {
	Files          map[string]codeir.FileSummary
	FileByBase     map[string][]string
	SymbolsByFQ    map[string]codeir.SymbolFact
	SymbolsByShort map[string][]codeir.SymbolFact
	ProtoRPCs      map[string]protoRPCRef
	ProtoTypes     map[string]codeir.SourceSpan
	HTTPRoutes     map[string]codeir.HTTPRouteFact
	DataModels     map[string]codeir.DataModelFact
	ConfigKeys     map[string]codeir.ConfigKeyFact
	Primitives     map[string][]codeir.RuntimePrimitiveFact
	BuildTargets   map[string]codeir.BuildTarget
	CompileOpts    map[string]codeir.CompileOption
	ExternalDeps   map[string]codeir.ExternalDep
}

type protoRPCRef struct {
	fullKey string
	rpc     codeir.ProtoRPCFact
}

// BuildIRIndex 从 CodeIRBundle 构建确定性倒排索引。
func BuildIRIndex(ir *codeir.CodeIRBundle) *IRIndex {
	idx := &IRIndex{
		Files:          make(map[string]codeir.FileSummary),
		FileByBase:     make(map[string][]string),
		SymbolsByFQ:    make(map[string]codeir.SymbolFact),
		SymbolsByShort: make(map[string][]codeir.SymbolFact),
		ProtoRPCs:      make(map[string]protoRPCRef),
		ProtoTypes:     make(map[string]codeir.SourceSpan),
		HTTPRoutes:     make(map[string]codeir.HTTPRouteFact),
		DataModels:     make(map[string]codeir.DataModelFact),
		ConfigKeys:     make(map[string]codeir.ConfigKeyFact),
		Primitives:     make(map[string][]codeir.RuntimePrimitiveFact),
		BuildTargets:   make(map[string]codeir.BuildTarget),
		CompileOpts:    make(map[string]codeir.CompileOption),
		ExternalDeps:   make(map[string]codeir.ExternalDep),
	}
	if ir == nil {
		return idx
	}

	for _, f := range ir.Tree.Files {
		idx.Files[f.RelPath] = f
		base := path.Base(f.RelPath)
		idx.FileByBase[base] = append(idx.FileByBase[base], f.RelPath)
	}

	for _, sym := range ir.Symbols.Symbols {
		idx.SymbolsByFQ[sym.FQSymbol] = sym
		short := lastSymbolSegment(sym.FQSymbol)
		idx.SymbolsByShort[short] = append(idx.SymbolsByShort[short], sym)
		// 同时索引 Class::Method 尾段便于无命名空间前缀匹配
		parts := strings.Split(sym.FQSymbol, "::")
		if len(parts) >= 2 {
			tail2 := parts[len(parts)-2] + "::" + parts[len(parts)-1]
			idx.SymbolsByShort[tail2] = append(idx.SymbolsByShort[tail2], sym)
		}
		for _, fld := range sym.Fields {
			idx.SymbolsByShort[fld.Name] = append(idx.SymbolsByShort[fld.Name], sym)
		}
	}

	for _, pf := range ir.IDLContracts.Protos {
		for _, svc := range pf.Services {
			idx.ProtoTypes[svc.Name] = svc.Meta.Span
			idx.ProtoTypes[svc.FullName] = svc.Meta.Span
			for _, rpc := range svc.RPCs {
				fullKey := svc.FullName + "/" + rpc.MethodName
				ref := protoRPCRef{fullKey: fullKey, rpc: rpc}
				idx.ProtoRPCs[fullKey] = ref
				idx.ProtoRPCs[svc.Name+"/"+rpc.MethodName] = ref
				idx.ProtoRPCs[svc.Name+"."+rpc.MethodName] = ref
				idx.ProtoRPCs[rpc.MethodName] = ref
			}
		}
		for _, msg := range pf.Messages {
			idx.ProtoTypes[msg.Name] = msg.Meta.Span
			idx.ProtoTypes[msg.FullName] = msg.Meta.Span
		}
		for _, en := range pf.Enums {
			idx.ProtoTypes[en.Name] = en.Meta.Span
			idx.ProtoTypes[en.FullName] = en.Meta.Span
		}
	}

	for _, rt := range ir.HTTPRoutes.Routes {
		idx.HTTPRoutes[rt.Path] = rt
		idx.HTTPRoutes[strings.ToUpper(rt.Method)+" "+rt.Path] = rt
	}

	for _, dm := range ir.DataModels.Entities {
		idx.DataModels[dm.TableOrCollection] = dm
		idx.DataModels[dm.EntityName] = dm
		for _, ix := range dm.Indexes {
			idx.DataModels[ix.Name] = dm
		}
	}

	for _, ck := range ir.ConfigKeys.Keys {
		idx.ConfigKeys[ck.KeyPath] = ck
	}

	for _, p := range ir.RuntimePrimitives.Primitives {
		idx.Primitives[p.APIName] = append(idx.Primitives[p.APIName], p)
	}

	for _, tgt := range ir.Build.Targets {
		idx.BuildTargets[tgt.Name] = tgt
	}
	for _, opt := range ir.Build.CompileOptions {
		idx.CompileOpts[opt.Key] = opt
		idx.CompileOpts[strings.TrimPrefix(opt.Key, "-D")] = opt
	}
	for _, dep := range ir.Deps.ExternalDeps {
		idx.ExternalDeps[dep.Name] = dep
	}

	return idx
}

// AlignCandidates 将 L2 输出的候选引用与 L1 Code IR 确定性对齐，返回规范化 EvidenceAnchor 列表及未能对齐的候选。
func AlignCandidates(idx *IRIndex, commitSHA string, candidates []CitedAnchorCandidate) ([]EvidenceAnchor, []CitedAnchorCandidate) {
	if idx == nil || len(candidates) == 0 {
		return nil, candidates
	}
	var anchors []EvidenceAnchor
	var unmatched []CitedAnchorCandidate
	seenKey := make(map[string]bool)

	for _, cand := range candidates {
		anchor, ok := alignSingleCandidate(idx, commitSHA, cand)
		if !ok {
			unmatched = append(unmatched, cand)
			continue
		}
		if seenKey[anchor.AnchorKey] {
			continue
		}
		seenKey[anchor.AnchorKey] = true
		anchors = append(anchors, anchor)
	}
	return anchors, unmatched
}

func alignSingleCandidate(idx *IRIndex, commitSHA string, cand CitedAnchorCandidate) (EvidenceAnchor, bool) {
	normFile := strings.TrimPrefix(strings.TrimSpace(cand.FilePath), "./")
	fq := strings.TrimSpace(cand.FQSymbol)

	// 1. 优先精确匹配 Symbols
	if fq != "" {
		if sym, ok := idx.SymbolsByFQ[fq]; ok {
			return makeSymbolAnchor(sym, commitSHA), true
		}
		if syms := idx.SymbolsByShort[fq]; len(syms) > 0 {
			for _, s := range syms {
				if normFile == "" || s.Meta.Span.FilePath == normFile {
					return makeSymbolAnchor(s, commitSHA), true
				}
			}
			return makeSymbolAnchor(syms[0], commitSHA), true
		}

		// 2. 匹配 Proto RPC
		if ref, ok := idx.ProtoRPCs[fq]; ok {
			return buildAnchor(
				"proto_rpc",
				ref.rpc.Meta.Span.FilePath,
				ref.fullKey,
				ref.rpc.Meta.ContentFingerprint,
				commitSHA,
				ref.rpc.Meta.Span.LineStart,
				ref.rpc.Meta.Span.LineEnd,
				string(ref.rpc.Meta.Confidence),
			), true
		}

		// 3. 匹配 HTTP Route
		if rt, ok := idx.HTTPRoutes[fq]; ok {
			return buildAnchor(
				"http_route",
				rt.Meta.Span.FilePath,
				strings.ToUpper(rt.Method)+" "+rt.Path,
				rt.Meta.ContentFingerprint,
				commitSHA,
				rt.Meta.Span.LineStart,
				rt.Meta.Span.LineEnd,
				string(rt.Meta.Confidence),
			), true
		}

		// 4. 匹配 DataModel
		if dm, ok := idx.DataModels[fq]; ok {
			return buildAnchor(
				"data_model",
				dm.Meta.Span.FilePath,
				dm.TableOrCollection,
				dm.Meta.ContentFingerprint,
				commitSHA,
				dm.Meta.Span.LineStart,
				dm.Meta.Span.LineEnd,
				string(dm.Meta.Confidence),
			), true
		}

		// 5. 匹配 ConfigKey
		if ck, ok := idx.ConfigKeys[fq]; ok {
			return buildAnchor(
				"config_key",
				ck.SourceFile,
				ck.KeyPath,
				ck.Meta.ContentFingerprint,
				commitSHA,
				ck.Meta.Span.LineStart,
				ck.Meta.Span.LineEnd,
				string(ck.Meta.Confidence),
			), true
		}

		// 6. 匹配 RuntimePrimitive
		if prims := idx.Primitives[fq]; len(prims) > 0 {
			p := prims[0]
			return buildAnchor(
				"runtime_primitive",
				p.Meta.Span.FilePath,
				p.APIName,
				p.Meta.ContentFingerprint,
				commitSHA,
				p.Meta.Span.LineStart,
				p.Meta.Span.LineEnd,
				string(p.Meta.Confidence),
			), true
		}
	}

	// 7. 文件级锚点匹配
	if normFile != "" {
		if f, ok := idx.Files[normFile]; ok && fq == "" {
			return buildAnchor(
				"file",
				f.RelPath,
				"",
				hashShort("file::"+f.RelPath),
				commitSHA,
				1,
				maxInt(f.LOC, 1),
				string(codeir.ConfidenceExact),
			), true
		}
	}

	return EvidenceAnchor{}, false
}

func makeSymbolAnchor(sym codeir.SymbolFact, commitSHA string) EvidenceAnchor {
	symKind := mapSymbolKind(sym.Language, sym.Kind)
	return buildAnchor(
		symKind,
		sym.Meta.Span.FilePath,
		sym.FQSymbol,
		sym.Meta.ContentFingerprint,
		commitSHA,
		sym.Meta.Span.LineStart,
		sym.Meta.Span.LineEnd,
		string(sym.Meta.Confidence),
	)
}

func mapSymbolKind(lang, kind string) string {
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

func buildAnchor(symbolKind, filePath, fqSymbol, fingerprint, commitSHA string, lineStart, lineEnd int, confidence string) EvidenceAnchor {
	if lineStart <= 0 {
		lineStart = 1
	}
	if lineEnd < lineStart {
		lineEnd = lineStart
	}
	if confidence == "" {
		confidence = string(codeir.ConfidenceExact)
	}
	anchorKey := fmt.Sprintf("%s::%s::%s", symbolKind, filePath, fqSymbol)
	if fingerprint == "" {
		fingerprint = hashShort(anchorKey)
	}
	sum := sha256.Sum256([]byte(anchorKey + "@" + commitSHA))
	anchorID := "ev_" + hex.EncodeToString(sum[:])[:10]
	return EvidenceAnchor{
		AnchorID:           anchorID,
		AnchorKey:          anchorKey,
		FilePath:           filePath,
		SymbolKind:         symbolKind,
		FQSymbol:           fqSymbol,
		ContentFingerprint: fingerprint,
		CommitSHA:          commitSHA,
		LineStart:          lineStart,
		LineEnd:            lineEnd,
		Confidence:         confidence,
	}
}

func lastSymbolSegment(fq string) string {
	if idx := strings.LastIndex(fq, "::"); idx >= 0 {
		return fq[idx+2:]
	}
	if idx := strings.LastIndex(fq, "."); idx >= 0 {
		return fq[idx+1:]
	}
	return fq
}

func hashShort(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
