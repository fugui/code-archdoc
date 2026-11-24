package verifier

import (
	"regexp"
	"strings"
	"unicode"

	"code-archdoc/pkg/doctype"
)

// UnverifiedRef 表示 Gate 1 检出的未在 Code IR 中验证存在的路径或符号引用。
type UnverifiedRef struct {
	SlotID   string `json:"slot_id"`
	RefKind  string `json:"ref_kind"` // file_path | symbol | route | table | config_key
	Value    string `json:"value"`
	Severity string `json:"severity"` // warning | error
}

var (
	reFencedBlock   = regexp.MustCompile("(?s)```[^`]*```")
	reInlineCode    = regexp.MustCompile("`([^`\r\n]{2,140})`")
	reKnownFilePath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+\.(?:c|cc|cpp|cxx|h|hpp|hh|hxx|proto|yaml|yml|json|sql|java|py|go|ts|tsx|cmake|txt|ini|toml|conf)$`)
	reQualSymbol    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)+$`)
	reCamelClass    = regexp.MustCompile(`^[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+$`)
)

// commonTechTerms 常见非仓库专有驼峰词白名单（避免 Gate 1 误报）
var commonTechTerms = map[string]bool{
	"CodeBench": true, "PostgreSQL": true, "MySQL": true, "JavaScript": true, "TypeScript": true,
	"GitLab": true, "GitHub": true, "OpenAI": true, "ChatGPT": true, "FastAPI": true,
	"SQLAlchemy": true, "MyBatis": true, "CMakeLists": true, "FrontMatter": true, "DevOps": true,
	"ProtoBuf": true, "GraphQL": true, "WebSocket": true, "ClickHouse": true, "MongoDB": true,
	"SimpleRouter": true,
}

// RunSymbolGate 执行 Gate 1：符号与文件路径存在性校验（02-extraction-and-generation-engine.md §4.2）。
// 同时对正文中验证通过的引用自动补全 EvidenceAnchor。
func RunSymbolGate(
	idx *IRIndex,
	commitSHA string,
	slotID string,
	markdown string,
	unmatchedCandidates []CitedAnchorCandidate,
	policy doctype.VerifyPolicy,
) ([]UnverifiedRef, []EvidenceAnchor) {
	return RunSymbolGateWithTerms(idx, commitSHA, slotID, markdown, unmatchedCandidates, policy, nil)
}

// RunSymbolGateWithTerms 在 RunSymbolGate 基础上支持将参考文档显式声明的外部术语（declaredTerms）降级为 warning 而不阻断审批。
func RunSymbolGateWithTerms(
	idx *IRIndex,
	commitSHA string,
	slotID string,
	markdown string,
	unmatchedCandidates []CitedAnchorCandidate,
	policy doctype.VerifyPolicy,
	declaredTerms map[string]bool,
) ([]UnverifiedRef, []EvidenceAnchor) {
	severity := "warning"
	if policy.StrictSymbolCheck {
		severity = "error"
	}

	var issues []UnverifiedRef
	var extraAnchors []EvidenceAnchor
	seenIssue := make(map[string]bool)
	seenAnchor := make(map[string]bool)

	addIssueWithSeverity := func(refKind, val, sev string) {
		key := refKind + "::" + val
		if seenIssue[key] {
			return
		}
		seenIssue[key] = true
		issues = append(issues, UnverifiedRef{
			SlotID:   slotID,
			RefKind:  refKind,
			Value:    val,
			Severity: sev,
		})
	}
	addIssue := func(refKind, val string) {
		addIssueWithSeverity(refKind, val, severity)
	}

	// 1. 先报告 L2 显式 cited_anchors 中未能对齐 Code IR 的项
	// 安全红线（ADR-04 D-3 / SC-16）：不在 module_context 白名单内的 scope="repo" 越界引用无条件标为 error（阻断 can_approve）！
	for _, c := range unmatchedCandidates {
		candSev := severity
		if strings.TrimSpace(c.Scope) == "repo" || c.SymbolKind == "module_doc" {
			candSev = "error"
		}
		if c.FQSymbol != "" {
			addIssueWithSeverity("symbol", c.FQSymbol, candSev)
		} else if c.FilePath != "" {
			addIssueWithSeverity("file_path", c.FilePath, candSev)
		}
	}

	// 2. 剥离围栏代码块后扫描内联反引号中的路径与符号引用
	cleanedMD := reFencedBlock.ReplaceAllString(markdown, "")
	for _, m := range reInlineCode.FindAllStringSubmatch(cleanedMD, -1) {
		tok := strings.TrimSpace(m[1])
		if tok == "" || strings.ContainsAny(tok, " \t") {
			continue
		}
		// 跳过 URL、命令行开关、占位符与平台路由
		if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") ||
			strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/") ||
			strings.HasPrefix(tok, "[REDACTED") || strings.HasPrefix(tok, "${") ||
			strings.Contains(tok, "://") {
			continue
		}

		// 2a. 候选文件路径
		normPath := strings.TrimPrefix(tok, "./")
		if idxPath, hasLine := splitPathLineSuffix(normPath); hasLine {
			normPath = idxPath
		}
		if reKnownFilePath.MatchString(normPath) {
			if _, ok := idx.Files[normPath]; ok {
				if anc, aligned := alignSingleCandidate(idx, commitSHA, CitedAnchorCandidate{FilePath: normPath}); aligned && !seenAnchor[anc.AnchorKey] {
					seenAnchor[anc.AnchorKey] = true
					extraAnchors = append(extraAnchors, anc)
				}
			} else if idx.ExtFiles[normPath] {
				if anc, aligned := alignSingleCandidate(idx, commitSHA, CitedAnchorCandidate{Scope: "repo", FilePath: normPath}); aligned && !seenAnchor[anc.AnchorKey] {
					seenAnchor[anc.AnchorKey] = true
					extraAnchors = append(extraAnchors, anc)
				}
			} else {
				addIssue("file_path", normPath)
			}
			continue
		}

		// 2b. 候选全限定 C++ 符号（A::B / A::B::C）
		if reQualSymbol.MatchString(tok) {
			// 跳过 C++ 标准库与常见第三方命名空间（std:: / boost:: / protobuf:: / glog:: / Threads::）
			if isStdOrExternalNamespace(tok) {
				if prims, ok := idx.Primitives[tok]; ok && len(prims) > 0 {
					if anc, aligned := alignSingleCandidate(idx, commitSHA, CitedAnchorCandidate{FQSymbol: tok}); aligned && !seenAnchor[anc.AnchorKey] {
						seenAnchor[anc.AnchorKey] = true
						extraAnchors = append(extraAnchors, anc)
					}
				}
				continue
			}
			if matchKnownSymbol(idx, tok) {
				if anc, aligned := alignSingleCandidate(idx, commitSHA, CitedAnchorCandidate{FQSymbol: tok}); aligned && !seenAnchor[anc.AnchorKey] {
					seenAnchor[anc.AnchorKey] = true
					extraAnchors = append(extraAnchors, anc)
				}
			} else {
				addIssue("symbol", tok)
			}
			continue
		}

		// 2c. 候选驼峰类/类型名（如 FakeOrderMgr / OrderService）
		if reCamelClass.MatchString(tok) && !commonTechTerms[tok] && hasAtLeastTwoUpperHumps(tok) {
			if matchKnownSymbol(idx, tok) {
				if anc, aligned := alignSingleCandidate(idx, commitSHA, CitedAnchorCandidate{FQSymbol: tok}); aligned && !seenAnchor[anc.AnchorKey] {
					seenAnchor[anc.AnchorKey] = true
					extraAnchors = append(extraAnchors, anc)
				}
			} else if declaredTerms != nil && declaredTerms[tok] {
				// ADR-10: 显式外部系统/领域术语固定降级为 warning（即使 StrictSymbolCheck=true 亦不阻断审批），同时保持审计可见性
				addIssueWithSeverity("external_term", tok, "warning")
			} else {
				addIssue("symbol", tok)
			}
		}
	}

	return issues, extraAnchors
}

func matchKnownSymbol(idx *IRIndex, tok string) bool {
	if idx == nil {
		return false
	}
	if _, ok := idx.SymbolsByFQ[tok]; ok {
		return true
	}
	if len(idx.SymbolsByShort[tok]) > 0 {
		return true
	}
	if _, ok := idx.ProtoTypes[tok]; ok {
		return true
	}
	if _, ok := idx.ProtoRPCs[tok]; ok {
		return true
	}
	if _, ok := idx.DataModels[tok]; ok {
		return true
	}
	if _, ok := idx.Primitives[tok]; ok {
		return true
	}
	if _, ok := idx.BuildTargets[tok]; ok {
		return true
	}
	if _, ok := idx.ExternalDeps[tok]; ok {
		return true
	}
	if _, ok := idx.ExtSymbolsByFQ[tok]; ok {
		return true
	}
	if len(idx.ExtSymbolsShort[tok]) > 0 {
		return true
	}
	// 处理 enum 值如 order::OrderErrorCode::OK -> 校验前缀 order::OrderErrorCode
	if lastSep := strings.LastIndex(tok, "::"); lastSep > 0 {
		parent := tok[:lastSep]
		if _, ok := idx.SymbolsByFQ[parent]; ok {
			return true
		}
		if len(idx.SymbolsByShort[parent]) > 0 {
			return true
		}
	}
	return false
}

func isStdOrExternalNamespace(tok string) bool {
	for _, prefix := range []string{"std::", "boost::", "protobuf::", "glog::", "Threads::", "google::"} {
		if strings.HasPrefix(tok, prefix) {
			return true
		}
	}
	return false
}

func splitPathLineSuffix(s string) (string, bool) {
	colon := strings.LastIndex(s, ":")
	if colon <= 0 {
		return s, false
	}
	tail := s[colon+1:]
	if tail == "" {
		return s, false
	}
	for _, ch := range tail {
		if !unicode.IsDigit(ch) && ch != '-' {
			return s, false
		}
	}
	return s[:colon], true
}

func hasAtLeastTwoUpperHumps(s string) bool {
	uppers := 0
	for _, r := range s {
		if unicode.IsUpper(r) {
			uppers++
		}
	}
	return uppers >= 2
}
