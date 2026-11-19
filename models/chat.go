package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// ChatSession Canvas Studio 文档协同精修会话
type ChatSession struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DocumentID   uint      `gorm:"not null;index" json:"document_id"`
	TargetSlotID string    `gorm:"size:64;default:'';index" json:"target_slot_id"`
	Title        string    `gorm:"size:255;default:''" json:"title"`
	CreatedBy    uint      `gorm:"not null;index" json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (ChatSession) TableName() string { return config.TablePrefix + "chat_sessions" }

// ChatMessage 精修对话消息与结构化章节 Diff 提案记录
type ChatMessage struct {
	ID                  uint           `gorm:"primaryKey" json:"id"`
	SessionID           uint           `gorm:"not null;index" json:"session_id"`
	Role                string         `gorm:"size:16;not null" json:"role"`
	Content             string         `gorm:"type:text;not null" json:"content"`
	TargetSlotID        string         `gorm:"size:64;default:''" json:"target_slot_id"`
	BaseRevisionNo      int            `gorm:"default:0;not null" json:"base_revision_no"`
	ProposedBeforeMD    string         `gorm:"type:text;default:''" json:"proposed_before_md"`
	ProposedAfterMD     string         `gorm:"type:text;default:''" json:"proposed_after_md"`
	UnifiedDiff         string         `gorm:"type:text;default:''" json:"unified_diff"`
	VerifierPreviewJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_preview"`
	ProposalStatus      string         `gorm:"size:20;default:'none';not null;index" json:"proposal_status"`
	AppliedRevisionID   *uint          `json:"applied_revision_id"`
	ModelTier           string         `gorm:"size:20;default:''" json:"model_tier"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (ChatMessage) TableName() string { return config.TablePrefix + "chat_messages" }
