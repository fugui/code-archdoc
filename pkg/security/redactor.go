package security

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

const (
	RedactedPlaceholder            = "[REDACTED]"
	RedactedHighEntropyPlaceholder = "[REDACTED_HIGH_ENTROPY]"
)

// Finding 描述一次凭据/高熵敏感信息扫描命中结果（MatchedMask 严禁包含完整明文）。
type Finding struct {
	RuleID      string `json:"rule_id"`      // key_blacklist | pem_private_key | token_prefix | url_credential | high_entropy
	MatchedMask string `json:"matched_mask"` // 仅记录脱敏摘要片段
	LineOffset  int    `json:"line_offset"`  // 1-based 行号
	Severity    string `json:"severity"`     // 固定 "blocker"
}

// SecretRedactor 实现三级敏感凭据检测与预脱敏管线（供 L1 Code IR 与独立 Verifier 共用）。
type SecretRedactor struct {
	sensitiveKeyTokens []string
	entropyThreshold   float64
	minEntropyLen      int
}

var (
	rePEMBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	rePEMLine  = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)

	reURLCred = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:@/\s"']+):([^@/\s"']+)@`)

	reTokenPrefix = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|ghp_[A-Za-z0-9]{16,}|glpat-[A-Za-z0-9_-]{12,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_-]+)?)\b`)

	reKeyValueSecret = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|pwd|secret|token|api_?key|access_key|private_key|credential|auth_header|bearer|cookie|jwt_secret)[a-z0-9_.-]*)\b(\s*[:=]\s*)(["'])([^"'\r\n]{4,})(["'])`)

	reCandidateToken = regexp.MustCompile(`[A-Za-z0-9_!@#$%^&*+=~.-]{20,}`)

	reUUID        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reGitSHA      = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	reSHA256Hex   = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	rePlaceholder = regexp.MustCompile(`^(\$\{[A-Za-z0-9_.-]+\}|\{\{[A-Za-z0-9_.-]+\}\}|<[A-Za-z0-9_.-]+>|\$[A-Z][A-Z0-9_]+)$`)
)

// NewSecretRedactor 构建默认三级凭据脱敏扫描器。
func NewSecretRedactor() *SecretRedactor {
	return &SecretRedactor{
		sensitiveKeyTokens: []string{
			"password", "passwd", "pwd", "secret", "token",
			"api_key", "apikey", "access_key", "private_key",
			"credential", "auth_header", "bearer", "cookie",
			"jwt_secret", "dsn", "connection_string",
		},
		entropyThreshold: 4.2,
		minEntropyLen:    20,
	}
}

// IsSensitiveKey 判断配置键名是否命中第一级黑名单。
func (r *SecretRedactor) IsSensitiveKey(keyPath string) bool {
	lower := strings.ToLower(strings.TrimSpace(keyPath))
	if lower == "" {
		return false
	}
	for _, tok := range r.sensitiveKeyTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// RedactConfigValue 对单个配置项的键值执行三级预脱敏。
func (r *SecretRedactor) RedactConfigValue(keyPath, value string) (safeValue string, isSensitive bool, reason string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || isSafePlaceholder(trimmed) {
		return value, r.IsSensitiveKey(keyPath), ""
	}

	// 数据库连接串特殊处理：保留协议与主机结构，掩码认证段
	if reURLCred.MatchString(value) {
		return reURLCred.ReplaceAllString(value, "${1}***:***@"), true, "url_credential"
	}

	if r.IsSensitiveKey(keyPath) {
		return RedactedPlaceholder, true, "key_blacklist"
	}

	redacted, findings := r.RedactText(value)
	if len(findings) > 0 {
		return redacted, true, findings[0].RuleID
	}
	return value, false, ""
}

// RedactText 对任意文本（源码片段、注释、Markdown 正文）执行三级脱敏并返回命中清单。
func (r *SecretRedactor) RedactText(input string) (string, []Finding) {
	if input == "" {
		return "", nil
	}

	var findings []Finding
	text := input

	// 1. 多行 PEM 私钥块扫描与脱敏
	if locs := rePEMBlock.FindAllStringIndex(text, -1); len(locs) > 0 {
		for _, loc := range locs {
			lineNo := 1 + strings.Count(text[:loc[0]], "\n")
			findings = append(findings, Finding{
				RuleID:      "pem_private_key",
				MatchedMask: "-----BEGIN *** PRIVATE KEY-----",
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
		}
		text = rePEMBlock.ReplaceAllString(text, RedactedPlaceholder)
	}

	lines := strings.Split(text, "\n")
	for idx, line := range lines {
		lineNo := idx + 1
		updated := line

		// 1b. 单行残留 PEM 头检测
		if rePEMLine.MatchString(updated) {
			findings = append(findings, Finding{
				RuleID:      "pem_private_key",
				MatchedMask: "-----BEGIN *** PRIVATE KEY-----",
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
			updated = rePEMLine.ReplaceAllString(updated, RedactedPlaceholder)
		}

		// 2a. URL 内嵌凭据（postgres://user:pass@host/db；跳过已脱敏的 ***:*** 或占位符）
		updated = reURLCred.ReplaceAllStringFunc(updated, func(match string) string {
			sub := reURLCred.FindStringSubmatch(match)
			if len(sub) < 4 {
				return match
			}
			scheme, userPart, passPart := sub[1], sub[2], sub[3]
			if (userPart == "***" && passPart == "***") ||
				(isSafePlaceholder(userPart) && isSafePlaceholder(passPart)) ||
				passPart == RedactedPlaceholder {
				return match
			}
			findings = append(findings, Finding{
				RuleID:      "url_credential",
				MatchedMask: "://***:***@",
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
			return scheme + "***:***@"
		})

		// 2b. 平台 Token 前缀（sk- / ghp_ / glpat- / AKIA / JWT）
		updated = reTokenPrefix.ReplaceAllStringFunc(updated, func(match string) string {
			findings = append(findings, Finding{
				RuleID:      "token_prefix",
				MatchedMask: maskSnippet(match),
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
			return RedactedPlaceholder
		})

		// 2c. 键名黑名单赋值模式（db_password: "secret123"）
		updated = reKeyValueSecret.ReplaceAllStringFunc(updated, func(match string) string {
			sub := reKeyValueSecret.FindStringSubmatch(match)
			if len(sub) < 6 {
				return match
			}
			keyName, sep, openQ, val, closeQ := sub[1], sub[2], sub[3], sub[4], sub[5]
			if val == RedactedPlaceholder || val == RedactedHighEntropyPlaceholder || isSafePlaceholder(val) || strings.Contains(val, "***:***@") {
				return match
			}
			findings = append(findings, Finding{
				RuleID:      "key_blacklist",
				MatchedMask: keyName + "=***",
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
			return keyName + sep + openQ + RedactedPlaceholder + closeQ
		})

		// 3. Shannon 高熵字符串检测（H >= 4.2, len >= 20, 混合字符集, 排除 UUID/Git SHA/占位符/路径/代码符号）
		updated = reCandidateToken.ReplaceAllStringFunc(updated, func(tok string) string {
			if tok == RedactedPlaceholder || tok == RedactedHighEntropyPlaceholder {
				return tok
			}
			if !r.isHighEntropySecret(tok) {
				return tok
			}
			findings = append(findings, Finding{
				RuleID:      "high_entropy",
				MatchedMask: maskSnippet(tok),
				LineOffset:  lineNo,
				Severity:    "blocker",
			})
			return RedactedHighEntropyPlaceholder
		})

		lines[idx] = updated
	}

	return strings.Join(lines, "\n"), findings
}

// ScanText 仅扫描返回命中结果（供 Verifier 调用）。
func (r *SecretRedactor) ScanText(input string) []Finding {
	_, findings := r.RedactText(input)
	return findings
}

func (r *SecretRedactor) isHighEntropySecret(tok string) bool {
	if len(tok) < r.minEntropyLen {
		return false
	}
	if isSafePlaceholder(tok) || reUUID.MatchString(tok) || reGitSHA.MatchString(tok) || reSHA256Hex.MatchString(tok) {
		return false
	}
	// 排除纯路径、包名、常见代码标识符（如 OrderService::CalculateDiscountAmount 或 foo.bar.baz）
	if strings.Contains(tok, "/") || strings.Contains(tok, "::") {
		return false
	}
	if strings.Count(tok, ".") >= 2 && !hasDigit(tok) {
		return false
	}
	// 排除不含数字且不含特殊符号的纯英文字母串（如长驼峰标识符或蛇形常量名）
	if !hasDigit(tok) && !hasSpecialSecretChar(tok) {
		return false
	}
	// 要求至少具备 3 类字符（大写、小写、数字、符号中的 3 类）以极大降低误报
	classes := 0
	var hasUpper, hasLower, hasNum, hasSym bool
	for _, ch := range tok {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasNum = true
		default:
			if ch != '_' && ch != '-' && ch != '.' {
				hasSym = true
			}
		}
	}
	if hasUpper {
		classes++
	}
	if hasLower {
		classes++
	}
	if hasNum {
		classes++
	}
	if hasSym {
		classes++
	}
	if classes < 3 {
		return false
	}
	return shannonEntropy(tok) >= r.entropyThreshold
}

func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	counts := make(map[rune]int, len(s))
	total := 0
	for _, ch := range s {
		counts[ch]++
		total++
	}
	var entropy float64
	fTotal := float64(total)
	for _, c := range counts {
		p := float64(c) / fTotal
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func isSafePlaceholder(val string) bool {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return true
	}
	return rePlaceholder.MatchString(trimmed)
}

func hasDigit(s string) bool {
	for _, ch := range s {
		if unicode.IsDigit(ch) {
			return true
		}
	}
	return false
}

func hasSpecialSecretChar(s string) bool {
	for _, ch := range s {
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '_' && ch != '-' && ch != '.' {
			return true
		}
	}
	return false
}

func maskSnippet(s string) string {
	if len(s) <= 6 {
		return "****"
	}
	return s[:3] + "***"
}
