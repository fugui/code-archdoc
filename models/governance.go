package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// LLMConfig 内网 OpenAI Compatible 模型池配置（支持多条平等模型与唯一默认模型）
type LLMConfig struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:80;uniqueIndex;not null" json:"name"`
	BaseURL            string     `gorm:"size:512;not null;default:''" json:"base_url"`
	ModelName          string     `gorm:"size:128;not null;default:''" json:"model_name"`
	APIKey             string     `gorm:"type:text;not null;default:''" json:"-"`
	APIKeyMasked       string     `gorm:"size:64;not null;default:''" json:"api_key_masked"`
	TimeoutSeconds     int        `gorm:"default:180;not null" json:"timeout_seconds"`
	MaxRetries         int        `gorm:"default:3;not null" json:"max_retries"`
	MaxTokensPerTask   int        `gorm:"default:200000;not null" json:"max_tokens_per_task"`
	MaxFilesPerTask    int        `gorm:"default:2000;not null" json:"max_files_per_task"`
	IsActive           bool       `gorm:"default:true;not null" json:"is_active"`
	IsDefault          bool       `gorm:"default:false;not null;index" json:"is_default"`
	LastProbeStatus    string     `gorm:"size:20;default:'unknown'" json:"last_probe_status"`
	LastProbeLatencyMs int64      `gorm:"default:0" json:"last_probe_latency_ms"`
	LastProbedAt       *time.Time `json:"last_probed_at"`
	UpdatedBy          *uint      `json:"updated_by"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func (LLMConfig) TableName() string { return config.TablePrefix + "llm_configs" }

// LLMUsageLog 每次模型调用的延迟与观测流水（F-27）
type LLMUsageLog struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	TaskID             *uint     `gorm:"index" json:"task_id"`
	DocumentID         *uint     `gorm:"index" json:"document_id"`
	RepoID             uint      `gorm:"not null;index" json:"repo_id"`
	DocTypeSlug        string    `gorm:"size:64;default:'';index" json:"doc_type_slug"`
	DocTypeRevisionID  *uint     `gorm:"index" json:"doc_type_revision_id"`
	SlotID             string    `gorm:"size:64;default:''" json:"slot_id"`
	Stage              string    `gorm:"size:24;not null;index" json:"stage"`
	ModelName          string    `gorm:"size:128;not null" json:"model_name"`
	PromptTokens       int       `gorm:"default:0;not null" json:"prompt_tokens"`
	CachedPromptTokens int       `gorm:"default:0;not null" json:"cached_prompt_tokens"`
	CompletionTokens   int       `gorm:"default:0;not null" json:"completion_tokens"`
	LatencyMs          int64     `gorm:"default:0;not null" json:"latency_ms"`
	Status             string    `gorm:"size:20;not null" json:"status"`
	ErrorCode          string    `gorm:"size:64;default:''" json:"error_code"`
	OperatorID         uint      `gorm:"not null;index" json:"operator_id"`
	CreatedAt          time.Time `gorm:"index" json:"created_at"`
}

func (LLMUsageLog) TableName() string { return config.TablePrefix + "llm_usage_logs" }

// QualityMetric 生成质量与审批修改度量事件（按 DocTypeRevision 纵向对比，F-26）
type QualityMetric struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	DocumentID         uint           `gorm:"not null;index" json:"document_id"`
	RepoID             uint           `gorm:"not null;index" json:"repo_id"`
	DocTypeSlug        string         `gorm:"size:64;not null;index:idx_archdoc_qm_dt_rev,priority:1" json:"doc_type_slug"`
	DocTypeRevisionID  uint           `gorm:"not null;index:idx_archdoc_qm_dt_rev,priority:2" json:"doc_type_revision_id"`
	MetricEvent        string         `gorm:"size:32;not null;index" json:"metric_event"`
	EditDiffRatio      float64        `gorm:"default:0;not null" json:"edit_diff_ratio"`
	SlotDiffRatiosJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"slot_diff_ratios"`
	SlotCoverageRate   float64        `gorm:"default:0;not null" json:"slot_coverage_rate"`
	VerifierBlocked    bool           `gorm:"default:false;not null" json:"verifier_blocked"`
	ChatAccepted       *bool          `json:"chat_accepted"`
	TotalTokens        int            `gorm:"default:0;not null" json:"total_tokens"`
	CreatedAt          time.Time      `gorm:"index" json:"created_at"`
}

func (QualityMetric) TableName() string { return config.TablePrefix + "quality_metrics" }
