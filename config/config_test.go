package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	// 1. 写入最小配置文件（省略新增 4 节），验证全部安全默认值生效
	tmpDir := t.TempDir()
	minCfgPath := filepath.Join(tmpDir, "min.yaml")
	minYAML := "server:\n  port: \":8100\"\nauth:\n  jwt_secret: \"unit-test-jwt-secret-value-123456\"\n"
	if err := os.WriteFile(minCfgPath, []byte(minYAML), 0644); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfig(minCfgPath); err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}

	if AppConfig.Storage.DataDir != "./data" {
		t.Fatalf("期望默认 DataDir=./data，实际为 %q", AppConfig.Storage.DataDir)
	}
	if AppConfig.Storage.EffectivePublishDir() != filepath.Join("./data", "publish_dir") {
		t.Fatalf("期望默认 PublishDir=./data/publish_dir，实际为 %q", AppConfig.Storage.EffectivePublishDir())
	}
	if AppConfig.Storage.SandboxMaxMB != 20480 || AppConfig.Storage.IRCacheMaxMB != 10240 || AppConfig.Storage.MaxFileBytes != 1048576 || AppConfig.Storage.ReapIntervalMins != 10 || AppConfig.Storage.EffectiveRefDocMaxBytes() != 524288 {
		t.Fatalf("Storage 默认值异常: %+v", AppConfig.Storage)
	}
	if AppConfig.Storage.RefDocsDir() != filepath.Join("./data", "ref_docs") {
		t.Fatalf("期望默认 RefDocsDir=./data/ref_docs，实际为 %q", AppConfig.Storage.RefDocsDir())
	}
	if AppConfig.Scheduler.MaxLLMConcurrency != 8 || AppConfig.Scheduler.MaxUserConcurrency != 2 || AppConfig.Scheduler.InteractiveQueueCap != 64 || AppConfig.Scheduler.BatchQueueCap != 256 {
		t.Fatalf("Scheduler 默认值异常: %+v", AppConfig.Scheduler)
	}
	if !AppConfig.Drift.PollEnabled || AppConfig.Drift.PollIntervalMinutes != 15 || AppConfig.Drift.PollConcurrency != 4 {
		t.Fatalf("Drift 默认值异常: %+v", AppConfig.Drift)
	}
	if AppConfig.LLM.EffectiveFirstByteTimeoutSeconds() != 90 || AppConfig.LLM.EffectiveIdleTimeoutSeconds() != 60 {
		t.Fatalf("LLM 流式超时默认值异常: fb=%d idle=%d",
			AppConfig.LLM.EffectiveFirstByteTimeoutSeconds(), AppConfig.LLM.EffectiveIdleTimeoutSeconds())
	}
	if !AppConfig.OpenCode.IsEnabled() || !AppConfig.OpenCode.IsContinuationEnabled() ||
		AppConfig.OpenCode.EffectiveDefaultEngine() != "native" ||
		AppConfig.OpenCode.EffectiveMaxConcurrency() != 2 ||
		AppConfig.OpenCode.EffectiveTimeoutSeconds() != 600 ||
		AppConfig.OpenCode.EffectiveIdleTimeoutSeconds() != 120 ||
		AppConfig.OpenCode.EffectiveContinuationMaxSeconds() != 180 {
		t.Fatalf("OpenCode 缺省配置默认值异常: %+v", AppConfig.OpenCode)
	}

	// 2. 验证显式关闭 poll_enabled 及自定义 Storage/Drift 参数覆盖
	customCfgPath := filepath.Join(tmpDir, "custom.yaml")
	customYAML := `
storage:
  data_dir: "/tmp/archdoc-data"
  publish_dir: "/tmp/archdoc-pub"
  max_file_bytes: 524288
drift:
  poll_enabled: false
  poll_interval_minutes: 30
  poll_concurrency: 2
opencode:
  enabled: false
  continuation_enabled: false
  default_engine: "opencode"
  max_concurrency: 4
  timeout_seconds: 450
  idle_timeout_seconds: 90
  continuation_max_seconds: 120
  binary_path: "/opt/bin/opencode"
`
	if err := os.WriteFile(customCfgPath, []byte(customYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(customCfgPath); err != nil {
		t.Fatalf("LoadConfig custom 失败: %v", err)
	}
	if AppConfig.Drift.PollEnabled {
		t.Fatal("显式设置 poll_enabled: false 时应保持 false")
	}
	if AppConfig.OpenCode.IsEnabled() || AppConfig.OpenCode.IsContinuationEnabled() {
		t.Fatal("显式设置 opencode.enabled/continuation_enabled 为 false 时应保持 false")
	}
	if AppConfig.OpenCode.EffectiveDefaultEngine() != "opencode" ||
		AppConfig.OpenCode.EffectiveMaxConcurrency() != 4 ||
		AppConfig.OpenCode.EffectiveTimeoutSeconds() != 450 ||
		AppConfig.OpenCode.EffectiveIdleTimeoutSeconds() != 90 ||
		AppConfig.OpenCode.EffectiveContinuationMaxSeconds() != 120 ||
		AppConfig.OpenCode.EffectiveBinaryPath() != "/opt/bin/opencode" {
		t.Fatalf("OpenCode 自定义配置未生效: %+v", AppConfig.OpenCode)
	}
	if AppConfig.Storage.EffectivePublishDir() != "/tmp/archdoc-pub" {
		t.Fatalf("自定义 PublishDir 未生效: %q", AppConfig.Storage.EffectivePublishDir())
	}
	if AppConfig.Storage.SandboxDir() != "/tmp/archdoc-data/sandbox_dir" ||
		AppConfig.Storage.IRCacheDir() != "/tmp/archdoc-data/ir_cache" ||
		AppConfig.Storage.DocTypesDir() != "/tmp/archdoc-data/doc_types" {
		t.Fatalf("子目录路径计算异常: sandbox=%s ir=%s doctypes=%s",
			AppConfig.Storage.SandboxDir(), AppConfig.Storage.IRCacheDir(), AppConfig.Storage.DocTypesDir())
	}
}

func TestLLMUserAgentDefaultsAndOverrides(t *testing.T) {
	origVersion := AppVersion
	origCfg := AppConfig
	t.Cleanup(func() {
		AppVersion = origVersion
		AppConfig = origCfg
	})

	cases := []struct {
		version string
		want    string
	}{
		{version: "v1.0.0", want: "Code-Archdoc/1.0.0"},
		{version: "V2.1.0-3-g8fce409", want: "Code-Archdoc/2.1.0-3-g8fce409"},
		{version: "1.0.0", want: "Code-Archdoc/1.0.0"},
		{version: "8fce409", want: "Code-Archdoc/8fce409"},
		{version: "dev", want: "Code-Archdoc/dev"},
		{version: "", want: "Code-Archdoc/dev"},
		{version: "   ", want: "Code-Archdoc/dev"},
	}
	for _, tc := range cases {
		if got := DefaultLLMUserAgent(tc.version); got != tc.want {
			t.Fatalf("DefaultLLMUserAgent(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}

	tmpDir := t.TempDir()

	// 1. 未配置 llm.user_agent 时回退为基于 AppVersion 的默认标识
	AppVersion = "v1.0.0"
	omitPath := filepath.Join(tmpDir, "omit-ua.yaml")
	if err := os.WriteFile(omitPath, []byte("auth:\n  jwt_secret: \"unit-test-jwt-secret-value-123456\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(omitPath); err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}
	if AppConfig.LLM.UserAgent != "Code-Archdoc/1.0.0" || AppConfig.LLM.EffectiveUserAgent() != "Code-Archdoc/1.0.0" {
		t.Fatalf("期望默认 UserAgent 为 Code-Archdoc/1.0.0，实际 field=%q effective=%q",
			AppConfig.LLM.UserAgent, AppConfig.LLM.EffectiveUserAgent())
	}

	// 2. 仅填写空白字符串时同样回退为默认标识
	blankPath := filepath.Join(tmpDir, "blank-ua.yaml")
	if err := os.WriteFile(blankPath, []byte("llm:\n  user_agent: \"   \"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(blankPath); err != nil {
		t.Fatalf("LoadConfig 空白 user_agent 失败: %v", err)
	}
	if AppConfig.LLM.UserAgent != "Code-Archdoc/1.0.0" {
		t.Fatalf("空白 user_agent 期望回退 Code-Archdoc/1.0.0，实际 %q", AppConfig.LLM.UserAgent)
	}

	// 3. 显式配置 llm.user_agent 时执行首尾裁剪并覆盖默认值
	customPath := filepath.Join(tmpDir, "custom-ua.yaml")
	if err := os.WriteFile(customPath, []byte("llm:\n  user_agent: \"  MyOrg-Archdoc/2.0  \"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(customPath); err != nil {
		t.Fatalf("LoadConfig 自定义 user_agent 失败: %v", err)
	}
	if AppConfig.LLM.UserAgent != "MyOrg-Archdoc/2.0" || AppConfig.LLM.EffectiveUserAgent() != "MyOrg-Archdoc/2.0" {
		t.Fatalf("期望自定义 UserAgent 为 MyOrg-Archdoc/2.0，实际 field=%q effective=%q",
			AppConfig.LLM.UserAgent, AppConfig.LLM.EffectiveUserAgent())
	}

	// 4. 含 CR/LF 换行符的非法 User-Agent 应被拒绝
	for _, badYAML := range []string{
		"llm:\n  user_agent: \"Bad\\nHeader\"\n",
		"llm:\n  user_agent: \"Bad\\rHeader\"\n",
		"llm:\n  user-agent: \"Bad\\nAlias\"\n",
		"llm:\n  user-agent: \"Bad\\rAlias\"\n",
	} {
		badPath := filepath.Join(tmpDir, "bad-ua.yaml")
		if err := os.WriteFile(badPath, []byte(badYAML), 0644); err != nil {
			t.Fatal(err)
		}
		if err := LoadConfig(badPath); err == nil {
			t.Fatalf("期望包含换行符的 llm user_agent/user-agent 校验失败，但成功加载: %s", badYAML)
		}
	}

	// 5. 兼容 kebab-case 键名 llm.user-agent
	aliasPath := filepath.Join(tmpDir, "alias-ua.yaml")
	if err := os.WriteFile(aliasPath, []byte("llm:\n  user-agent: \"  opencode/1.18.31  \"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(aliasPath); err != nil {
		t.Fatalf("LoadConfig 兼容 user-agent 失败: %v", err)
	}
	if AppConfig.LLM.UserAgent != "opencode/1.18.31" || AppConfig.LLM.EffectiveUserAgent() != "opencode/1.18.31" {
		t.Fatalf("期望兼容 user-agent 为 opencode/1.18.31，实际 field=%q effective=%q",
			AppConfig.LLM.UserAgent, AppConfig.LLM.EffectiveUserAgent())
	}

	// 6. 同时配置 user_agent 与 user-agent 时，规范键名 user_agent 优先
	bothPath := filepath.Join(tmpDir, "both-ua.yaml")
	if err := os.WriteFile(bothPath, []byte("llm:\n  user_agent: \"Canonical-UA/1.0\"\n  user-agent: \"Alias-UA/2.0\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(bothPath); err != nil {
		t.Fatalf("LoadConfig 同时配置 user_agent 与 user-agent 失败: %v", err)
	}
	if AppConfig.LLM.UserAgent != "Canonical-UA/1.0" || AppConfig.LLM.EffectiveUserAgent() != "Canonical-UA/1.0" {
		t.Fatalf("期望 user_agent 优先为 Canonical-UA/1.0，实际 field=%q effective=%q",
			AppConfig.LLM.UserAgent, AppConfig.LLM.EffectiveUserAgent())
	}
}
