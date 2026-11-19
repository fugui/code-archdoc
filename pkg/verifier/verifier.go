package verifier

import (
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
	CommitSHA string
	IR        *codeir.CodeIRBundle
	Slots     []doctype.SlotSpec
	Policy    *doctype.VerifyPolicy
	Sections  []SectionInput
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

// VerifyResult 汇总报告及每个章节对齐后的证据锚点与摘要。
type VerifyResult struct {
	Report           VerifierReport
	SectionEvidences map[string][]EvidenceAnchor
	SectionSummaries map[string]SectionVerifierSummary
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
	idx := BuildIRIndex(input.IR)

	report := VerifierReport{
		MissingRequiredSlots: []string{},
		UnverifiedRefs:       []UnverifiedRef{},
		MermaidIssues:        []MermaidIssue{},
		SecretFindings:       []SecretFinding{},
	}

	sectionEvidences := make(map[string][]EvidenceAnchor, len(input.Sections))
	sectionSummaries := make(map[string]SectionVerifierSummary, len(input.Sections))
	sectionsBySlot := make(map[string]string, len(input.Sections))

	for _, sec := range input.Sections {
		sectionsBySlot[sec.SlotID] = sec.Markdown
		summary := SectionVerifierSummary{}

		// 0. 证据锚点对齐
		aligned, unmatched := AlignCandidates(idx, input.CommitSHA, sec.CitedAnchors)

		// Gate 1: 符号与文件路径存在性校验
		unverified, extraAnchors := RunSymbolGate(idx, input.CommitSHA, sec.SlotID, sec.Markdown, unmatched, policy)
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
		Report:           report,
		SectionEvidences: sectionEvidences,
		SectionSummaries: sectionSummaries,
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
