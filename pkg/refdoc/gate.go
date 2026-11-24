package refdoc

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"code-archdoc/pkg/security"
)

var reInlineBase64Image = regexp.MustCompile(`(?is)data:image/[a-z0-9.+-]+;base64,([A-Za-z0-9+/=\r\n]+)`)

// ValidateGate 执行纯 UTF-8 Markdown 校验、体积上限检查、内嵌 Base64 大图拦截与分级凭据检测。
func ValidateGate(markdown string, maxBytes int64, redactAndSave bool) (string, bool, *GateFailure) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDocBytes
	}
	if !utf8.ValidString(markdown) {
		return "", false, &GateFailure{
			HTTPStatus: http.StatusBadRequest,
			Code:       "INVALID_PARAM",
			Message:    "参考文档必须为合法的 UTF-8 编码纯 Markdown 文本",
		}
	}

	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		return "", false, &GateFailure{
			HTTPStatus: http.StatusBadRequest,
			Code:       "INVALID_PARAM",
			Message:    "参考文档 Markdown 正文不能为空",
		}
	}

	actualBytes := int64(len(normalized))
	if actualBytes > maxBytes {
		return "", false, &GateFailure{
			HTTPStatus: http.StatusRequestEntityTooLarge,
			Code:       "PAYLOAD_TOO_LARGE",
			Message:    fmt.Sprintf("参考文档体积 %d 字节超出上限 %d 字节", actualBytes, maxBytes),
			Details: map[string]any{
				"max_bytes":    maxBytes,
				"actual_bytes": actualBytes,
			},
		}
	}

	// 内嵌 Base64 大图拦截（> 4 KB）
	if locs := reInlineBase64Image.FindAllStringSubmatchIndex(normalized, -1); len(locs) > 0 {
		for _, loc := range locs {
			if len(loc) < 4 {
				continue
			}
			b64Payload := normalized[loc[2]:loc[3]]
			compact := strings.Map(func(r rune) rune {
				if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
					return -1
				}
				return r
			}, b64Payload)
			if len(compact) > DefaultMaxBase64Bytes {
				lineOffset := 1 + strings.Count(normalized[:loc[0]], "\n")
				return "", false, &GateFailure{
					HTTPStatus: http.StatusUnprocessableEntity,
					Code:       "INLINE_BASE64_IMAGE",
					Message:    fmt.Sprintf("第 %d 行包含超过 4 KB 的内嵌 Base64 图片（%d 字节），请移除图片并改用文字或 Mermaid 图表说明", lineOffset, len(compact)),
					Details: map[string]any{
						"line_offset":      lineOffset,
						"max_base64_bytes": DefaultMaxBase64Bytes,
						"actual_b64_bytes": len(compact),
					},
				}
			}
		}
	}

	// 分级凭据门禁（复用 pkg/security.SecretRedactor）
	redactor := security.NewSecretRedactor()
	redactedText, rawFindings := redactor.RedactText(normalized)
	if len(rawFindings) == 0 {
		return normalized, false, nil
	}

	var hardFindings []GateFinding
	var softFindings []GateFinding
	for _, f := range rawFindings {
		switch f.RuleID {
		case "pem_private_key", "token_prefix", "url_credential":
			hardFindings = append(hardFindings, GateFinding{
				RuleID:      f.RuleID,
				MatchedMask: f.MatchedMask,
				LineOffset:  f.LineOffset,
				Severity:    "hard",
			})
		default:
			softFindings = append(softFindings, GateFinding{
				RuleID:      f.RuleID,
				MatchedMask: f.MatchedMask,
				LineOffset:  f.LineOffset,
				Severity:    "confirm_required",
			})
		}
	}

	if len(hardFindings) > 0 {
		all := append(hardFindings, softFindings...)
		return "", false, &GateFailure{
			HTTPStatus: http.StatusUnprocessableEntity,
			Code:       "SECRET_DETECTED",
			Message:    fmt.Sprintf("检测到 %d 处高危明文凭据（私钥/Token/URL 密码），已硬阻断入库，请彻底清除后重新提交", len(hardFindings)),
			Details: map[string]any{
				"findings": all,
			},
		}
	}

	if !redactAndSave {
		return "", false, &GateFailure{
			HTTPStatus: http.StatusUnprocessableEntity,
			Code:       "SECRET_CONFIRM_REQUIRED",
			Message:    fmt.Sprintf("检测到 %d 处疑似配置凭据或高熵示例字段，请确认是否自动脱敏后入库", len(softFindings)),
			Details: map[string]any{
				"findings": softFindings,
			},
		}
	}

	return redactedText, true, nil
}
