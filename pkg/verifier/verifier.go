package verifier

import (
	"strings"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/security"
)

// SectionInput 单个待校验章节的输入。
type SectionInput struct {
	SlotID       string
	Title        string
	Markdown     string
	CitedAnchors []CitedAnchorCandidate
}

// VerifyInput Verifier 四重确定性门禁完整入参。
type VerifyInput struct {
	CommitSHA         string
	IR                *codeir.CodeIRBundle
	ModuleContext     *codeir.ModuleContextFacet // 可选：子模块跨模块外部引用白名单（支撑 scope="repo" 校验）
	ModuleDigests     []codeir.ModuleDigestInput // 可选：整仓汇总引用的子模块摘要集合（支撑 symbol_kind="module_doc" 校验）
	ActiveRefDocs     []RefDocSpec               // 可选：当次生效的架构参考文档快照（ADR-10）
	HistoricalRefDocs []RefDocSpec               // 可选：上一修订已引用但可能已解绑的历史参考文档集合（ADR-10）
	Slots             []doctype.SlotSpec
	Policy            *doctype.VerifyPolicy
	Sections          []SectionInput
}

// SectionVerifierSummary 单章节维度的校验计数汇总（对齐 models.SectionVerifierSummary）。
type SectionVerifierSummary struct {
	UnverifiedCount int  `json:"unverified_count"`
	MermaidErrors   int  `json:"mermaid_errors"`
	MermaidWarnings int  `json:"mermaid_warnings"`
	SecretBlockers  int  `json:"secret_blockers"`
	HasBlocker      bool `json:"has_blocker"`
}

// VerifierReport 文档级四重确定性校验报告（对齐 models.VerifierReport）。
type VerifierReport struct {
	Passed               bool            `json:"passed"`
	CanApprove           bool            `json:"can_approve"`
	BlockerCount         int             `json:"blocker_count"`
	ErrorCount           int             `json:"error_count"`
	WarningCount         int             `json:"warning_count"`
	SlotCoverageRate     float64         `json:"slot_coverage_rate"`
	MissingRequiredSlots []string        `json:"missing_required_slots"`
	UnverifiedRefs       []UnverifiedRef `json:"unverified_refs"`
	MermaidIssues        []MermaidIssue  `json:"mermaid_issues"`
	SecretFindings       []SecretFinding `json:"secret_findings"`
}

// VerifyResult 汇总报告及每个章节对齐后的证据锚点、参考引用与摘要。
type VerifyResult struct {
	Report              VerifierReport
	SectionEvidences    map[string][]EvidenceAnchor
	SectionRefCitations map[string][]RefDocCitation
	SectionSummaries    map[string]SectionVerifierSummary
}

// Verifier 零 LLM 依赖的四重确定性质量与安全门禁引擎（F-07 / SC-04）。
type Verifier struct {
	redactor *security.SecretRedactor
}

func New() *Verifier {
	return &Verifier{
		redactor: security.NewSecretRedactor(),
	}
}

// Verify 同步执行证据对齐 + 四重确定性门禁（Gate 1 符号路径 / Gate 2 必填槽位 / Gate 3 Mermaid / Gate 4 凭据防泄漏）。
func (v *Verifier) Verify(input VerifyInput) *VerifyResult {
	policy := input.Policy.Effective()
	idx := BuildIRIndexWithContext(input.IR, input.ModuleContext, input.ModuleDigests)

	report := VerifierReport{
		MissingRequiredSlots: []string{},
		UnverifiedRefs:       []UnverifiedRef{},
		MermaidIssues:        []MermaidIssue{},
		SecretFindings:       []SecretFinding{},
	}

	sectionEvidences := make(map[string][]EvidenceAnchor, len(input.Sections))
	sectionRefCitations := make(map[string][]RefDocCitation, len(input.Sections))
	sectionSummaries := make(map[string]SectionVerifierSummary, len(input.Sections))
	sectionsBySlot := make(map[string]string, len(input.Sections))

	declaredTerms := make(map[string]bool)
	for _, rd := range input.ActiveRefDocs {
		for _, term := range rd.DeclaredTerms {
			t := strings.TrimSpace(term)
			if t != "" {
				declaredTerms[t] = true
			}
		}
	}

	for _, sec := range input.Sections {
		sectionsBySlot[sec.SlotID] = sec.Markdown
		summary := SectionVerifierSummary{}

		// 0a. 前置剥离 ref_doc 候选锚点，独立对齐至 SectionRefCitations（零污染源码 EvidencesJSON）
		var codeCands []CitedAnchorCandidate
		var refCands []CitedAnchorCandidate
		for _, cand := range sec.CitedAnchors {
			if strings.TrimSpace(cand.Scope) == "ref_doc" || strings.TrimSpace(cand.SymbolKind) == "ref_doc" {
				refCands = append(refCands, cand)
			} else {
				codeCands = append(codeCands, cand)
			}
		}
		refCits, refUnverified := AlignRefDocCandidates(input.ActiveRefDocs, input.HistoricalRefDocs, sec.SlotID, refCands)
		if len(refCits) > 0 {
			sectionRefCitations[sec.SlotID] = refCits
		}

		// 0b. 源码证据锚点对齐
		aligned, unmatched := AlignCandidates(idx, input.CommitSHA, codeCands)

		// Gate 1: 符号与文件路径存在性校验（含 DeclaredTerms 降级为 external_term warning）
		unverified, extraAnchors := RunSymbolGateWithTerms(idx, input.CommitSHA, sec.SlotID, sec.Markdown, unmatched, policy, declaredTerms)
		unverified = append(unverified, refUnverified...)
		allAnchors := mergeAnchors(aligned, extraAnchors)
		sectionEvidences[sec.SlotID] = allAnchors

		for _, u := range unverified {
			report.UnverifiedRefs = append(report.UnverifiedRefs, u)
			summary.UnverifiedCount++
			if u.Severity == "error" {
				report.ErrorCount++
			} else {
				report.WarningCount++
			}
		}

		// Gate 3: Mermaid 语法与复杂度门禁
		mermaidIssues := RunMermaidGate(sec.SlotID, sec.Markdown, policy)
		for _, mi := range mermaidIssues {
			report.MermaidIssues = append(report.MermaidIssues, mi)
			if mi.Severity == "error" {
				summary.MermaidErrors++
				report.ErrorCount++
			} else {
				summary.MermaidWarnings++
				report.WarningCount++
			}
		}

		// Gate 4: 独立凭据防泄漏硬门禁
		blockOnSecret := policy.BlockOnSecretLeak == nil || *policy.BlockOnSecretLeak
		secrets := RunSecretGate(v.redactor, sec.SlotID, sec.Markdown)
		for _, sf := range secrets {
			report.SecretFindings = append(report.SecretFindings, sf)
			if blockOnSecret {
				summary.SecretBlockers++
				summary.HasBlocker = true
				report.BlockerCount++
			} else {
				report.WarningCount++
			}
		}

		sectionSummaries[sec.SlotID] = summary
	}

	// Gate 2: 必填槽位完备度校验
	coverage, missing := RunSlotGate(input.Slots, sectionsBySlot)
	report.SlotCoverageRate = coverage
	if len(missing) > 0 {
		report.MissingRequiredSlots = missing
		report.ErrorCount += len(missing)
	}

	report.Passed = report.BlockerCount == 0 && report.ErrorCount == 0
	report.CanApprove = report.BlockerCount == 0 && report.ErrorCount == 0

	return &VerifyResult{
		Report:              report,
		SectionEvidences:    sectionEvidences,
		SectionRefCitations: sectionRefCitations,
		SectionSummaries:    sectionSummaries,
	}
}

func mergeAnchors(a, b []EvidenceAnchor) []EvidenceAnchor {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]EvidenceAnchor, 0, len(a)+len(b))
	for _, x := range a {
		if !seen[x.AnchorKey] {
			seen[x.AnchorKey] = true
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !seen[x.AnchorKey] {
			seen[x.AnchorKey] = true
			out = append(out, x)
		}
	}
	return out
}
