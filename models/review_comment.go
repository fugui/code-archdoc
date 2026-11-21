package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// ReviewScopeType 审阅批注作用域枚举
const (
	ReviewScopeInline  = "inline"  // 画布正文划词或块级（表格/Mermaid/段落）批注，必有 slot_id
	ReviewScopeSection = "section" // 章节级批注，必有 slot_id
	ReviewScopeGlobal  = "global"  // 全局评论、全文口径或跨章补充，slot_id 为空
)

// ReviewCategory 审阅批注语义分类
const (
	ReviewCategoryCorrection = "correction"  // 纠错修正（代码事实有误，优先触发只读源码工具核查）
	ReviewCategorySupplement = "supplement"  // 知识补充（业务背景/隐性约束，忠实融入）
	ReviewCategoryConcise    = "concise"     // 精简改写（篇幅/表述/格式调整，严禁篡改既有技术事实）
	ReviewCategoryGlobalRule = "global_rule" // 全局规范（跨章节统一风格/删减口径）
	ReviewCategoryQuestion   = "question"    // 疑问待核（查证备忘或待人工核实）
)

// ReviewCommentStatus 审阅批注生命周期状态
const (
	ReviewStatusOpen      = "open"      // 待处理
	ReviewStatusProposed  = "proposed"  // 已送入综合精修并产出 pending 提案，待审阅采纳
	ReviewStatusResolved  = "resolved"  // 关联提案已采纳生效或人工确认已解决
	ReviewStatusDismissed = "dismissed" // 已忽略 / 撤回取消
)

// ReviewOriginRole 批注来源角色（首期恒为 owner，为 F-22 读者协审预留）
const (
	ReviewOriginOwner    = "owner"
	ReviewOriginReviewer = "reviewer"
)

func IsValidReviewScopeType(v string) bool {
	return v == ReviewScopeInline || v == ReviewScopeSection || v == ReviewScopeGlobal
}

func IsValidReviewCategory(v string) bool {
	switch v {
	case ReviewCategoryCorrection, ReviewCategorySupplement, ReviewCategoryConcise,
		ReviewCategoryGlobalRule, ReviewCategoryQuestion:
		return true
	default:
		return false
	}
}

func IsValidReviewCommentStatus(v string) bool {
	switch v {
	case ReviewStatusOpen, ReviewStatusProposed, ReviewStatusResolved, ReviewStatusDismissed:
		return true
	default:
		return false
	}
}

// ReviewCommentAnchor 存储在 archdoc_review_comments.anchor_json 中的 W3C TextQuoteSelector 风格定位器
type ReviewCommentAnchor struct {
	BlockIndex      int    `json:"block_index,omitempty"`
	BlockType       string `json:"block_type,omitempty"`
	Quote           string `json:"quote,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
	Suffix          string `json:"suffix,omitempty"`
	SourceLineStart int    `json:"source_line_start,omitempty"`
	SourceLineEnd   int    `json:"source_line_end,omitempty"`
}

// ReviewComment 文档审阅批注实体（第 19 张 archdoc_* 自有表）
type ReviewComment struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	DocumentID         uint           `gorm:"not null;uniqueIndex:idx_archdoc_rc_doc_no,priority:1;index:idx_archdoc_rc_doc_status,priority:1" json:"document_id"`
	CommentNo          int            `gorm:"not null;default:0;uniqueIndex:idx_archdoc_rc_doc_no,priority:2" json:"comment_no"`
	BaseRevisionNo     int            `gorm:"not null;default:0" json:"base_revision_no"`
	AuthorID           uint           `gorm:"not null;index" json:"author_id"`
	OriginRole         string         `gorm:"size:16;not null;default:'owner'" json:"origin_role"`
	ScopeType          string         `gorm:"size:16;not null;default:'inline'" json:"scope_type"`
	SlotID             string         `gorm:"size:64;not null;default:'';index" json:"slot_id"`
	AnchorJSON         datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"anchor"`
	Category           string         `gorm:"size:24;not null;default:'correction'" json:"category"`
	Content            string         `gorm:"type:text;not null" json:"content"`
	PersistForRegen    bool           `gorm:"default:false;not null;index" json:"persist_for_regen"`
	Status             string         `gorm:"size:20;not null;default:'open';index:idx_archdoc_rc_doc_status,priority:2" json:"status"`
	ProposalGroupMsgID *uint          `gorm:"index" json:"proposal_group_msg_id,omitempty"`
	ResolvedRevisionID *uint          `json:"resolved_revision_id,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func (ReviewComment) TableName() string { return config.TablePrefix + "review_comments" }
