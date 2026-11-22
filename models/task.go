package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// GenTask 文档生成或增量刷新主任务（支持状态机流转、预算熔断报告与优先级调度）
type GenTask struct {
	ID               uint           `gorm:"primaryKey" json:"id"`
	RepoID           uint           `gorm:"not null;index:idx_archdoc_task_scope,priority:1" json:"repo_id"`
	Subdirectory     string         `gorm:"size:512;not null;default:'';index:idx_archdoc_task_scope,priority:2" json:"subdirectory"`
	RefName          string         `gorm:"size:128;not null;default:'master'" json:"ref_name"`
	SnapshotID       *uint          `gorm:"index" json:"snapshot_id"`
	ParentTaskID     *uint          `gorm:"index" json:"parent_task_id"`
	TaskKind         string         `gorm:"size:32;not null;default:'generate'" json:"task_kind"`
	Priority         string         `gorm:"size:20;not null;default:'interactive';index" json:"priority"`
	DocTypesJSON     datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"doc_types"`
	TargetDocumentID *uint          `gorm:"index" json:"target_document_id"`
	TargetSlotsJSON  datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"target_slots"`
	Status           string         `gorm:"size:24;not null;default:'pending';index" json:"status"`
	ProgressPercent  int            `gorm:"default:0;not null" json:"progress_percent"`
	EstimatedTokens  int            `gorm:"default:0;not null" json:"estimated_tokens"`
	ErrorCode        string         `gorm:"size:64;default:''" json:"error_code"`
	ErrorMessage     string         `gorm:"type:text;default:''" json:"error_message"`
	BudgetAdviceJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"budget_advice"`
	TriggeredBy      uint           `gorm:"not null;index" json:"triggered_by"`
	StartedAt        *time.Time     `json:"started_at"`
	FinishedAt       *time.Time     `json:"finished_at"`
	CreatedAt        time.Time      `gorm:"index" json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func (GenTask) TableName() string { return config.TablePrefix + "gen_tasks" }

// GenSlotTask 槽位级生成子任务（支持单槽位独立重试与 P1 断点恢复）
type GenSlotTask struct {
	ID                uint           `gorm:"primaryKey" json:"id"`
	TaskID            uint           `gorm:"not null;uniqueIndex:idx_archdoc_slot_task_unique,priority:1;index" json:"task_id"`
	DocTypeSlug       string         `gorm:"size:64;not null;uniqueIndex:idx_archdoc_slot_task_unique,priority:2" json:"doc_type_slug"`
	DocTypeRevisionID uint           `gorm:"not null" json:"doc_type_revision_id"`
	SlotID            string         `gorm:"size:64;not null;uniqueIndex:idx_archdoc_slot_task_unique,priority:3" json:"slot_id"`
	DocumentID        *uint          `gorm:"index" json:"document_id"`
	Status            string         `gorm:"size:24;not null;default:'pending';index" json:"status"`
	AttemptCount      int            `gorm:"default:0;not null" json:"attempt_count"`
	ModelName         string         `gorm:"size:128;default:''" json:"model_name"`
	OutputMarkdown    string         `gorm:"type:text;default:''" json:"output_markdown"`
	CitedAnchorsJSON  datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"cited_anchors"`
	ErrorMessage      string         `gorm:"type:text;default:''" json:"error_message"`
	StartedAt         *time.Time     `json:"started_at"`
	FinishedAt        *time.Time     `json:"finished_at"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

func (GenSlotTask) TableName() string { return config.TablePrefix + "gen_slot_tasks" }
