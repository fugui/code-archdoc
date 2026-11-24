package refdoc

import (
	"math"
	"unicode"
)

// EstimateTextTokens 单源保守 Token 估算：中文/非 ASCII 按 1.5 char/token，ASCII 按 3.5 char/token，向上取整。
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
