package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// RepoRemoteHead 记录 F-25 后台轻量 git ls-remote 巡检结果与失败指数退避状态
type RepoRemoteHead struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	RepoID              uint       `gorm:"not null;uniqueIndex:idx_archdoc_remote_head_unique,priority:1" json:"repo_id"`
	RefName             string     `gorm:"size:128;not null;default:'master';uniqueIndex:idx_archdoc_remote_head_unique,priority:2" json:"ref_name"`
	RemoteHeadSHA       string     `gorm:"size:40;not null;default:''" json:"remote_head_sha"`
	LastPolledAt        *time.Time `json:"last_polled_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	ConsecutiveFailures int        `gorm:"default:0;not null" json:"consecutive_failures"`
	LastError           string     `gorm:"type:text;default:''" json:"last_error"`
	NextRetryAt         *time.Time `gorm:"index" json:"next_retry_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (RepoRemoteHead) TableName() string { return config.TablePrefix + "repo_remote_heads" }

// DocumentDrift 已发布文档的章节级 Code Drift / Spec Drift 状态快照
type DocumentDrift struct {
	ID                      uint           `gorm:"primaryKey" json:"id"`
	DocumentID              uint           `gorm:"not null;uniqueIndex:idx_archdoc_drift_unique,priority:1;index" json:"document_id"`
	DriftType               string         `gorm:"size:16;not null;uniqueIndex:idx_archdoc_drift_unique,priority:2;index" json:"drift_type"`
	HasDrift                bool           `gorm:"default:false;not null;index" json:"has_drift"`
	Precision               string         `gorm:"size:20;default:'section_level';not null" json:"precision"`
	BaseCommitSHA           string         `gorm:"size:40;default:''" json:"base_commit_sha"`
	RemoteHeadSHA           string         `gorm:"size:40;default:''" json:"remote_head_sha"`
	BoundDocTypeRevisionID  *uint          `json:"bound_doc_type_revision_id"`
	LatestDocTypeRevisionID *uint          `json:"latest_doc_type_revision_id"`
	AffectedSlotCount       int            `gorm:"default:0;not null" json:"affected_slot_count"`
	AffectedSlotsJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"affected_slots"`
	DetectedAt              time.Time      `gorm:"not null" json:"detected_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
}

func (DocumentDrift) TableName() string { return config.TablePrefix + "document_drifts" }
