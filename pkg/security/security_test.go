package security

import (
	"strings"
	"testing"
)

func TestMaskAPIKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"short", "****"},
		{"1234567", "****"},
		{"sk-test-secret-1234", "sk-****1234"},
		{"sk-test-secret-9999", "sk-****9999"},
		{"token_abcdefgh5678", "****5678"},
	}

	for _, tc := range cases {
		if got := MaskAPIKey(tc.in); got != tc.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSecretRedactorThreeTiers(t *testing.T) {
	redactor := NewSecretRedactor()

	// 1. 第一级：键名黑名单（配置键脱敏与文本键值赋值脱敏）
	val, sensitive, reason := redactor.RedactConfigValue("db_password", "secret123")
	if !sensitive || val != RedactedPlaceholder || reason == "" {
		t.Fatalf("键名黑名单 db_password 应脱敏，实际 val=%q sensitive=%v reason=%q", val, sensitive, reason)
	}

	// 数据库 DSN 保留结构掩码认证段
	dsnVal, dsnSensitive, _ := redactor.RedactConfigValue("database.dsn", "postgres://admin:p@ss@10.0.0.1:5432/orderdb")
	if !dsnSensitive || strings.Contains(dsnVal, "p@ss") || !strings.Contains(dsnVal, "10.0.0.1") {
		t.Fatalf("DSN 脱敏结果不符: val=%q sensitive=%v", dsnVal, dsnSensitive)
	}

	// 2. 第二级 + 第一级文本扫描：PEM 私钥、Token 前缀、URL 内嵌凭据、密码赋值
	sampleText := strings.Join([]string{
		`db_password: "secret123"`,
		`-----BEGIN RSA PRIVATE KEY-----`,
		`MIIEowIBAAKCAQEA7v8s9d0f1g2h3j4k5l6m7n8o9p0q1r2s3t4u5v6w7x8y9z0`,
		`-----END RSA PRIVATE KEY-----`,
		`openai_key = "sk-proj-abcDEF1234567890xyz"`,
		`dsn = "postgres://admin:p@ss@10.0.0.1/db"`,
		`random_secret = "aB9!kL2@mN5#pQ8$rS3%tU6^vW1&xY4*"`,
	}, "\n")

	redacted, findings := redactor.RedactText(sampleText)
	if len(findings) < 4 {
		t.Fatalf("期望至少命中 4 条敏感凭据，实际命中 %d 条: %+v", len(findings), findings)
	}
	for _, leaked := range []string{"secret123", "BEGIN RSA PRIVATE KEY", "sk-proj-abcDEF1234567890xyz", "admin:p@ss@", "aB9!kL2@mN5#pQ8$rS3%tU6^vW1&xY4*"} {
		if strings.Contains(redacted, leaked) {
			t.Errorf("脱敏后文本仍包含敏感明文 %q:\n%s", leaked, redacted)
		}
	}
	for _, f := range findings {
		if strings.Contains(f.MatchedMask, "secret123") || strings.Contains(f.MatchedMask, "sk-proj-abcDEF1234567890xyz") {
			t.Errorf("SecretFinding.MatchedMask 不应包含完整明文: %+v", f)
		}
	}

	// 3. 零误报断言：UUID、40 位 Git SHA、环境变量占位符、常规路径与代码符号
	safeInputs := []string{
		"request_id = 550e8400-e29b-41d4-a716-446655440000",
		"commit_sha = a1b2c3d4e5f6789012345678901234567890abcd",
		"db_password: ${DB_PASSWORD}",
		"api_key: {{OPENAI_API_KEY}}",
		"source_file = src/order/order_service_impl.cpp",
		"fq_symbol = order::OrderService::CalculateDiscountAmount",
	}
	for _, line := range safeInputs {
		out, safeFindings := redactor.RedactText(line)
		if len(safeFindings) != 0 {
			t.Errorf("安全文本不应误报: line=%q findings=%+v", line, safeFindings)
		}
		if out != line {
			t.Errorf("安全文本不应被改写: got=%q want=%q", out, line)
		}
	}

	// 键名在黑名单但值为占位符时，按安全占位符处理或仍识别为敏感键但无泄漏
	placeholderVal, _, _ := redactor.RedactConfigValue("app.timeout_ms", "3000")
	if placeholderVal != "3000" {
		t.Fatalf("非敏感配置键不应被脱敏: %q", placeholderVal)
	}
}
