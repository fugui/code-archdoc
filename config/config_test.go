package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	t.Setenv("ARCHDOC_ENCRYPTION_KEY", "")

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
	if AppConfig.Storage.SandboxMaxMB != 20480 || AppConfig.Storage.IRCacheMaxMB != 10240 || AppConfig.Storage.MaxFileBytes != 1048576 || AppConfig.Storage.ReapIntervalMins != 10 {
		t.Fatalf("Storage 默认值异常: %+v", AppConfig.Storage)
	}
	if AppConfig.Scheduler.MaxLLMConcurrency != 8 || AppConfig.Scheduler.MaxUserConcurrency != 2 || AppConfig.Scheduler.InteractiveQueueCap != 64 || AppConfig.Scheduler.BatchQueueCap != 256 {
		t.Fatalf("Scheduler 默认值异常: %+v", AppConfig.Scheduler)
	}
	if !AppConfig.Drift.PollEnabled || AppConfig.Drift.PollIntervalMinutes != 15 || AppConfig.Drift.PollConcurrency != 4 {
		t.Fatalf("Drift 默认值异常: %+v", AppConfig.Drift)
	}

	// 2. 验证显式关闭 poll_enabled 及环境变量覆盖 ARCHDOC_ENCRYPTION_KEY
	t.Setenv("ARCHDOC_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	customCfgPath := filepath.Join(tmpDir, "custom.yaml")
	customYAML := `
storage:
  data_dir: "/tmp/archdoc-data"
  publish_dir: "/tmp/archdoc-pub"
  max_file_bytes: 524288
security:
  encryption_key: "from-yaml-should-be-overridden"
drift:
  poll_enabled: false
  poll_interval_minutes: 30
  poll_concurrency: 2
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
	if AppConfig.Security.EncryptionKey != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("环境变量 ARCHDOC_ENCRYPTION_KEY 未覆盖 YAML 配置: %q", AppConfig.Security.EncryptionKey)
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
