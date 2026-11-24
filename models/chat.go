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

// ChatMessage 精修对话消息与多槽位提案组头记录
type ChatMessage struct {
	ID                  uint           `gorm:"primaryKey" json:"id"`
	SessionID           uint           `gorm:"not null;index" json:"session_id"`
	Role                string         `gorm:"size:16;not null" json:"role"`
	Intent              string         `gorm:"size:16;not null;default:'revise'" json:"intent"`
	ScopeType           string         `gorm:"size:20;not null;default:'slot'" json:"scope_type"`
	Content             string         `gorm:"type:text;not null" json:"content"`
	TargetSlotID        string         `gorm:"size:64;default:''" json:"target_slot_id"`
	BaseRevisionNo      int            `gorm:"default:0;not null" json:"base_revision_no"`
	ProposedBeforeMD    string         `gorm:"type:text;default:''" json:"proposed_before_md"`
	ProposedAfterMD     string         `gorm:"type:text;default:''" json:"proposed_after_md"`
	UnifiedDiff         string         `gorm:"type:text;default:''" json:"unified_diff"`
	VerifierPreviewJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_preview"`
	PlanSummaryJSON     datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"plan_summary"`
	ContextRefsJSON     datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"context_refs"`
	ProposalStatus      string         `gorm:"size:20;default:'none';not null;index" json:"proposal_status"`
	AppliedRevisionID   *uint          `json:"applied_revision_id"`
	ModelName           string         `gorm:"size:128;default:''" json:"model_name"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (ChatMessage) TableName() string { return config.TablePrefix + "chat_messages" }

// ChatProposalItem 一次精修消息下包含的单槽位 Diff 提案项（第 20 张 archdoc_* 自有表）
type ChatProposalItem struct {
	ID                      uint           `gorm:"primaryKey" json:"id"`
	MessageID               uint           `gorm:"not null;uniqueIndex:idx_archdoc_cpi_msg_slot,priority:1;index" json:"message_id"`
	DocumentID              uint           `gorm:"not null;index:idx_archdoc_cpi_doc_slot_status,priority:1" json:"document_id"`
	SlotID                  string         `gorm:"size:64;not null;uniqueIndex:idx_archdoc_cpi_msg_slot,priority:2;index:idx_archdoc_cpi_doc_slot_status,priority:2" json:"slot_id"`
	BaseRevisionNo          int            `gorm:"not null;default:0" json:"base_revision_no"`
	ProposedBeforeMD        string         `gorm:"type:text;not null;default:''" json:"proposed_before_md"`
	ProposedAfterMD         string         `gorm:"type:text;not null;default:''" json:"proposed_after_md"`
	FinalAppliedMD          string         `gorm:"type:text;not null;default:''" json:"final_applied_md"`
	UnifiedDiff             string         `gorm:"type:text;not null;default:''" json:"unified_diff"`
	CitedAnchorsJSON        datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"cited_anchors"`
	RefCitationsJSON        datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"ref_citations"`
	VerifierPreviewJSON     datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_preview"`
	AddressedCommentIDsJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"addressed_comment_ids"`
	Status                  string         `gorm:"size:20;not null;default:'pending';index:idx_archdoc_cpi_doc_slot_status,priority:3" json:"status"`
	AppliedRevisionID       *uint          `json:"applied_revision_id,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
}

func (ChatProposalItem) TableName() string { return config.TablePrefix + "chat_proposal_items" }
