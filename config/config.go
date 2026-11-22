package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"code-common/backend/configutil"
	commonModels "code-common/backend/models"

	"gopkg.in/yaml.v3"
)

// 模块级核心元数据常量（集中声明，便于 scripts/init-module.sh 或手工派生时一站式替换）
const (
	ModuleKey   = "archdoc"
	ServiceName = "Code-Archdoc"
	AdminRole   = "archdoc_admin"
	TablePrefix = "archdoc_"
	DefaultPort = ":8100"
)

// AppVersion 构建期版本号（默认 "dev"，与 main.Version 同步，由 Makefile -ldflags 或启动入口注入）
var AppVersion = "dev"

// LLMSection 定义全局 LLM HTTP 客户端配置
type LLMSection struct {
	UserAgent      string `yaml:"user_agent"` // 可选自定义访问 LLM 的 User-Agent；留空默认使用 "Code-Archdoc/<version>"
	UserAgentAlias string `yaml:"user-agent"` // 兼容误写为 kebab-case 的 user-agent 键名
}

// DefaultLLMUserAgent 按项目构建版本规则生成默认 User-Agent（"Code-Archdoc/<version>"）。
// 若版本号以 "v"/"V" + 数字开头（如 "v1.0.0"、"v1.2.3-4-g8fce409"），归一化去除前导 "v"；为空则回退 "dev"。
func DefaultLLMUserAgent(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	} else if len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9' {
		v = v[1:]
	}
	return ServiceName + "/" + v
}

// EffectiveUserAgent 返回最终生效的 LLM 请求 User-Agent（显式配置优先，留空回退为默认版本标识）。
func (s LLMSection) EffectiveUserAgent() string {
	if ua := strings.TrimSpace(s.UserAgent); ua != "" {
		return ua
	}
	if alias := strings.TrimSpace(s.UserAgentAlias); alias != "" {
		return alias
	}
	return DefaultLLMUserAgent(AppVersion)
}

// AuthSection 定义模块认证与独立运行开关配置
type AuthSection struct {
	JWTSecret            string                    `yaml:"jwt_secret"`
	StandaloneMode       bool                      `yaml:"standalone_mode"`
	PasswordLoginEnabled bool                      `yaml:"password_login_enabled"`
	OAuth2               commonModels.OAuth2Config `yaml:"oauth2"`
}

// StorageSection 定义本地受控存储目录、磁盘配额与文件大小阈值配置
type StorageSection struct {
	DataDir          string `yaml:"data_dir"`           // 统一根数据目录，默认 "./data"
	PublishDir       string `yaml:"publish_dir"`        // 可选独立覆盖 publish_dir 路径，默认 "<data_dir>/publish_dir"
	SandboxMaxMB     int64  `yaml:"sandbox_max_mb"`     // 默认 20480 (20 GB)
	IRCacheMaxMB     int64  `yaml:"ir_cache_max_mb"`    // 默认 10240 (10 GB)
	MaxFileBytes     int64  `yaml:"max_file_bytes"`     // L1 降噪单文件上限，默认 1048576 (1 MB)
	ReapIntervalMins int    `yaml:"reap_interval_mins"` // LRU 回收周期，默认 10 分钟
}

// SandboxDir 返回 Git 沙箱根目录。
func (s StorageSection) SandboxDir() string {
	return filepath.Join(s.effectiveDataDir(), "sandbox_dir")
}

// IRCacheDir 返回 Code IR 压缩分片与术语表缓存根目录。
func (s StorageSection) IRCacheDir() string {
	return filepath.Join(s.effectiveDataDir(), "ir_cache")
}

// DocTypesDir 返回 DocType 唯一事实源根目录。
func (s StorageSection) DocTypesDir() string {
	return filepath.Join(s.effectiveDataDir(), "doc_types")
}

// EffectivePublishDir 返回已审批定稿 Markdown 原子发布根目录。
func (s StorageSection) EffectivePublishDir() string {
	if p := strings.TrimSpace(s.PublishDir); p != "" {
		return p
	}
	return filepath.Join(s.effectiveDataDir(), "publish_dir")
}

func (s StorageSection) effectiveDataDir() string {
	if d := strings.TrimSpace(s.DataDir); d != "" {
		return d
	}
	return "./data"
}

// SchedulerSection 定义任务调度双队列与并发信号量配置
type SchedulerSection struct {
	MaxLLMConcurrency   int `yaml:"max_llm_concurrency"`   // 全局 LLM 并发信号量，默认 8
	MaxUserConcurrency  int `yaml:"max_user_concurrency"`  // 单用户最大并发任务数，默认 2
	InteractiveQueueCap int `yaml:"interactive_queue_cap"` // 高优交互队列容量，默认 64
	BatchQueueCap       int `yaml:"batch_queue_cap"`       // 低优批量队列容量，默认 256
	MaxTokensPerBatch   int `yaml:"max_tokens_per_batch"`  // 批次生成预估 Token 前置熔断上限，默认 1000000
}

// ExtractSection 定义整仓基线抽取阈值与降级配置（ADR-04 §4.3）
type ExtractSection struct {
	MaxRepoFiles           int `yaml:"max_repo_files"`            // 整仓基线抽取有效文件数上限，默认 50000
	MaxRepoDurationSeconds int `yaml:"max_repo_duration_seconds"` // 整仓基线抽取超时降级阈值（秒），默认 600
}

// DriftSection 定义远端变更轻量巡检与漂移重算配置
type DriftSection struct {
	PollEnabled         bool `yaml:"poll_enabled"`          // 默认 true（单元测试中可关闭后台 Ticker）
	PollIntervalMinutes int  `yaml:"poll_interval_minutes"` // 默认 15 分钟
	PollConcurrency     int  `yaml:"poll_concurrency"`      // 轻量 ls-remote 并发上限，默认 4
}

// Config 定义模块全局配置结构
type Config struct {
	Server    configutil.ServerConfig     `yaml:"server"`
	Database  commonModels.DatabaseConfig `yaml:"database"`
	Auth      AuthSection                 `yaml:"auth"`
	Storage   StorageSection              `yaml:"storage"`
	Scheduler SchedulerSection            `yaml:"scheduler"`
	Extract   ExtractSection              `yaml:"extract"`
	Drift     DriftSection                `yaml:"drift"`
	LLM       LLMSection                  `yaml:"llm"`
}

// AppConfig 全局运行时配置单例
var AppConfig Config

// LoadConfig 从指定路径加载 YAML 配置；若不存在则尝试回退至 config.yaml.example 或默认配置
func LoadConfig(filename string) error {
	AppConfig = Config{
		Drift: DriftSection{
			PollEnabled: true,
		},
	}

	actualPath := filename
	if _, err := os.Stat(actualPath); os.IsNotExist(err) {
		if _, exErr := os.Stat("config.yaml.example"); exErr == nil {
			log.Printf("[%s] 未检测到 %s，回退加载 config.yaml.example", ServiceName, filename)
			actualPath = "config.yaml.example"
		} else {
			log.Printf("[%s] 配置文件不存在，启用内置默认安全配置", ServiceName)
			if defErr := applyDefaults(); defErr != nil {
				return defErr
			}
			return nil
		}
	}

	data, err := os.ReadFile(actualPath)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, &AppConfig); err != nil {
		return err
	}

	if err := applyDefaults(); err != nil {
		return err
	}
	return nil
}

func applyDefaults() error {
	configutil.ApplyServerDefaults(&AppConfig.Server, DefaultPort)

	// 支持环境变量覆盖 JWT_SECRET（生产环境推荐通过环境变量或密钥管理系统注入）
	// TODO(security): 生产环境集群部署时请接入企业 KMS / Secret Manager 注入统一密钥，禁用文件明文落盘
	if envSecret := strings.TrimSpace(os.Getenv("JWT_SECRET")); envSecret != "" {
		AppConfig.Auth.JWTSecret = envSecret
	}
	configutil.EnsureJWTSecret(&AppConfig.Auth.JWTSecret, ServiceName)

	// Storage 默认值
	if strings.TrimSpace(AppConfig.Storage.DataDir) == "" {
		AppConfig.Storage.DataDir = "./data"
	}
	if strings.TrimSpace(AppConfig.Storage.PublishDir) == "" {
		AppConfig.Storage.PublishDir = filepath.Join(AppConfig.Storage.DataDir, "publish_dir")
	}
	if AppConfig.Storage.SandboxMaxMB <= 0 {
		AppConfig.Storage.SandboxMaxMB = 20480
	}
	if AppConfig.Storage.IRCacheMaxMB <= 0 {
		AppConfig.Storage.IRCacheMaxMB = 10240
	}
	if AppConfig.Storage.MaxFileBytes <= 0 {
		AppConfig.Storage.MaxFileBytes = 1048576
	}
	if AppConfig.Storage.ReapIntervalMins <= 0 {
		AppConfig.Storage.ReapIntervalMins = 10
	}

	// Scheduler 默认值
	if AppConfig.Scheduler.MaxLLMConcurrency <= 0 {
		AppConfig.Scheduler.MaxLLMConcurrency = 8
	}
	if AppConfig.Scheduler.MaxUserConcurrency <= 0 {
		AppConfig.Scheduler.MaxUserConcurrency = 2
	}
	if AppConfig.Scheduler.InteractiveQueueCap <= 0 {
		AppConfig.Scheduler.InteractiveQueueCap = 64
	}
	if AppConfig.Scheduler.BatchQueueCap <= 0 {
		AppConfig.Scheduler.BatchQueueCap = 256
	}
	if AppConfig.Scheduler.MaxTokensPerBatch <= 0 {
		AppConfig.Scheduler.MaxTokensPerBatch = 1000000
	}

	// Extract 默认值（ADR-04 §4.3 超大仓降级阈值）
	if AppConfig.Extract.MaxRepoFiles <= 0 {
		AppConfig.Extract.MaxRepoFiles = 50000
	}
	if AppConfig.Extract.MaxRepoDurationSeconds <= 0 {
		AppConfig.Extract.MaxRepoDurationSeconds = 600
	}

	// Drift 默认值
	if AppConfig.Drift.PollIntervalMinutes <= 0 {
		AppConfig.Drift.PollIntervalMinutes = 15
	}
	if AppConfig.Drift.PollConcurrency <= 0 {
		AppConfig.Drift.PollConcurrency = 4
	}

	if AppConfig.Auth.OAuth2.Enabled {
		if len(AppConfig.Auth.OAuth2.Scopes) == 0 {
			AppConfig.Auth.OAuth2.Scopes = []string{"openid", "profile", "email"}
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Username == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Username = "preferred_username"
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Email == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Email = "email"
		}
		if AppConfig.Auth.OAuth2.FieldMapping.Name == "" {
			AppConfig.Auth.OAuth2.FieldMapping.Name = "name"
		}
		if AppConfig.Auth.OAuth2.RedirectURL == "" {
			AppConfig.Auth.OAuth2.RedirectURL = strings.TrimRight(AppConfig.Server.ExternalURL, "/") + "/api/oauth2/callback"
		}
	}

	// LLM 客户端默认值与合法性校验
	if strings.ContainsAny(AppConfig.LLM.UserAgent, "\r\n") || strings.ContainsAny(AppConfig.LLM.UserAgentAlias, "\r\n") {
		return fmt.Errorf("llm.user_agent 非法：不可包含换行符")
	}
	AppConfig.LLM.UserAgent = strings.TrimSpace(AppConfig.LLM.UserAgent)
	if AppConfig.LLM.UserAgent == "" {
		AppConfig.LLM.UserAgent = strings.TrimSpace(AppConfig.LLM.UserAgentAlias)
	}
	AppConfig.LLM.UserAgentAlias = ""
	if AppConfig.LLM.UserAgent == "" {
		AppConfig.LLM.UserAgent = DefaultLLMUserAgent(AppVersion)
	}

	return nil
}
