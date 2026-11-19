package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// DocType 对应目录 doc_types/<slug>/ 的数据库只读同步索引（目录为唯一事实源）
type DocType struct {
	ID                  uint           `gorm:"primaryKey" json:"id"`
	Slug                string         `gorm:"size:64;uniqueIndex;not null" json:"slug"`
	DisplayName         string         `gorm:"size:120;not null" json:"display_name"`
	Description         string         `gorm:"type:text;default:''" json:"description"`
	IsBuiltin           bool           `gorm:"default:false;not null" json:"is_builtin"`
	IsActive            bool           `gorm:"default:true;not null;index" json:"is_active"`
	ModelTier           string         `gorm:"size:20;default:'flagship';not null" json:"model_tier"`
	TimeoutSeconds      int            `gorm:"default:300;not null" json:"timeout_seconds"`
	IRFacetsJSON        datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"ir_facets"`
	LatestRevisionID    *uint          `gorm:"index" json:"latest_revision_id"`
	LatestRevisionNo    int            `gorm:"default:0;not null" json:"latest_revision_no"`
	LatestAggregateHash string         `gorm:"size:64;not null;default:''" json:"latest_aggregate_hash"`
	UpdatedBy           *uint          `json:"updated_by"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (DocType) TableName() string { return config.TablePrefix + "doc_types" }

// DocTypeRevision 文档类型不可变修订快照（任一资产文件变更即追加一条，驱动 Spec Drift 与质量对比）
type DocTypeRevision struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	DocTypeID          uint           `gorm:"not null;uniqueIndex:idx_archdoc_dt_rev_unique,priority:1;index" json:"doc_type_id"`
	DocTypeSlug        string         `gorm:"size:64;not null;index" json:"doc_type_slug"`
	RevisionNo         int            `gorm:"not null;uniqueIndex:idx_archdoc_dt_rev_unique,priority:2" json:"revision_no"`
	AggregateHash      string         `gorm:"size:64;not null;index" json:"aggregate_hash"`
	MetaJSON           datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"meta_json"`
	SlotsSnapshotJSON  datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"slots_snapshot"`
	TemplateMD         string         `gorm:"type:text;not null" json:"template_md"`
	GenerationPromptMD string         `gorm:"type:text;not null" json:"generation_prompt_md"`
	RefinePromptMD     string         `gorm:"type:text;default:''" json:"refine_prompt_md"`
	PromptHash         string         `gorm:"size:64;not null" json:"prompt_hash"`
	CreatedBy          *uint          `json:"created_by"`
	CreatedAt          time.Time      `json:"created_at"`
}

func (DocTypeRevision) TableName() string { return config.TablePrefix + "doc_type_revisions" }
