package verifier

import (
	"regexp"
	"strings"

	"code-archdoc/pkg/doctype"
)

var (
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reHeadingLine = regexp.MustCompile(`(?m)^\s*#{1,6}\s+.*$`)
)

// RunSlotGate 执行 Gate 2：必填槽位完备度校验（计算 slot_coverage_rate 与缺失槽位列表）。
func RunSlotGate(slots []doctype.SlotSpec, sectionsBySlot map[string]string) (coverageRate float64, missingRequired []string) {
	totalRequired := 0
	completedRequired := 0

	for _, sl := range slots {
		if !sl.Required {
			continue
		}
		totalRequired++
		minChars := sl.MinChars
		if minChars <= 0 {
			minChars = 40
		}

		raw, exists := sectionsBySlot[sl.SlotID]
		if !exists {
			missingRequired = append(missingRequired, sl.SlotID)
			continue
		}
		effective := stripSlotPlaceholders(raw)
		if len([]rune(effective)) < minChars {
			missingRequired = append(missingRequired, sl.SlotID)
			continue
		}
		completedRequired++
	}

	if totalRequired == 0 {
		return 1.0, nil
	}
	return float64(completedRequired) / float64(totalRequired), missingRequired
}

func stripSlotPlaceholders(md string) string {
	s := reHTMLComment.ReplaceAllString(md, "")
	s = reHeadingLine.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if lower == "todo" || lower == "tbd" || lower == "n/a" || lower == "..." {
		return ""
	}
	return s
}
