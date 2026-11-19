package security

import "strings"

// MaskAPIKey 对敏感 APIKey 执行统一不可逆脱敏：
// - 空串返回 ""
// - 长度 < 8 返回 "****"
// - 以 "sk-" 开头返回 "sk-****<last4>"
// - 其他长密钥返回 "****<last4>"
func MaskAPIKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) < 8 {
		return "****"
	}
	last4 := trimmed[len(trimmed)-4:]
	if strings.HasPrefix(trimmed, "sk-") {
		return "sk-****" + last4
	}
	return "****" + last4
}
