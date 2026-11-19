package verifier

import (
	"code-archdoc/pkg/security"
)

// SecretFinding 表示 Gate 4 独立凭据扫描命中的阻断性安全告警（MatchedMask 严禁含明文）。
type SecretFinding struct {
	SlotID      string `json:"slot_id"`
	RuleID      string `json:"rule_id"`
	MatchedMask string `json:"matched_mask"`
	LineOffset  int    `json:"line_offset"`
	Severity    string `json:"severity"` // 固定 "blocker"
}

// RunSecretGate 执行 Gate 4：独立于 LLM 的凭据/私钥/高熵 Token 全量扫描门禁（命中即 blocker）。
func RunSecretGate(redactor *security.SecretRedactor, slotID, markdown string) []SecretFinding {
	if redactor == nil {
		redactor = security.NewSecretRedactor()
	}
	rawFindings := redactor.ScanText(markdown)
	if len(rawFindings) == 0 {
		return nil
	}
	out := make([]SecretFinding, 0, len(rawFindings))
	for _, f := range rawFindings {
		out = append(out, SecretFinding{
			SlotID:      slotID,
			RuleID:      f.RuleID,
			MatchedMask: f.MatchedMask,
			LineOffset:  f.LineOffset,
			Severity:    "blocker",
		})
	}
	return out
}
