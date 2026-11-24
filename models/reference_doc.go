package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// ReferenceDoc 架构参考 Markdown 文档主实体（第 21 张 archdoc_* 自有表）
type ReferenceDoc struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	Title              string         `gorm:"size:200;not null" json:"title"`
	DocKind            string         `gorm:"size:32;not null;default:'arch_design';index" json:"doc_kind"` // arch_design | domain_glossary | interface_spec | adr_constraint | other
	Description        string         `gorm:"size:500;default:''" json:"description"`
	Visibility         string         `gorm:"size:16;not null;default:'public'" json:"visibility"`
	OwnerScopeKey      string         `gorm:"size:512;not null;index" json:"owner_scope_key"`
	OwnerScopeLevel    string         `gorm:"size:16;not null;index" json:"owner_scope_level"` // system | subsystem | group | repo | module
	OwnerArchElementID *uint          `gorm:"index" json:"owner_arch_element_id"`
	OwnerRepoID        *uint          `gorm:"index" json:"owner_repo_id"`
	OwnerSubdirectory  string         `gorm:"size:512;not null;default:''" json:"owner_subdirectory"`
	SummaryMD          string         `gorm:"type:text;not null;default:''" json:"summary_md"`
	SummarySource      string         `gorm:"size:16;not null;default:'fallback'" json:"summary_source"` // manual | llm | fallback
	SummaryStatus      string         `gorm:"size:16;not null;default:'ready'" json:"summary_status"`    // ready | pending | failed
	SummaryForVersion  int            `gorm:"not null;default:1" json:"summary_for_version"`
	TOCJSON            datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"toc"`
	DeclaredTerms      datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"declared_terms"`
	CurrentVersion     int            `gorm:"not null;default:1" json:"current_version"`
	StorageRelPath     string         `gorm:"size:512;not null;default:''" json:"-"`
	ContentSHA256      string         `gorm:"size:64;not null;default:'';index" json:"content_sha256"`
	ByteSize           int64          `gorm:"not null;default:0" json:"byte_size"`
	RuneCount          int            `gorm:"not null;default:0" json:"rune_count"`
	EstimatedTokens    int            `gorm:"not null;default:0" json:"estimated_tokens"`
	LineCount          int            `gorm:"not null;default:0" json:"line_count"`
	Status             string         `gorm:"size:16;not null;default:'active';index" json:"status"` // active | archived
	CreatedBy          uint           `gorm:"not null;index" json:"created_by"`
	UpdatedBy          uint           `gorm:"not null" json:"updated_by"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func (ReferenceDoc) TableName() string { return config.TablePrefix + "reference_docs" }

// ReferenceDocBinding 参考文档与四层架构作用域的多对多绑定关系（第 22 张 archdoc_* 自有表）
type ReferenceDocBinding struct {
	ID                uint           `gorm:"primaryKey" json:"id"`
	DocID             uint           `gorm:"not null;uniqueIndex:idx_archdoc_ref_bind_unique,priority:1;index" json:"doc_id"`
	ScopeKey          string         `gorm:"size:512;not null;uniqueIndex:idx_archdoc_ref_bind_unique,priority:2;index" json:"scope_key"`
	ScopeLevel        string         `gorm:"size:16;not null;index" json:"scope_level"` // system | subsystem | group | repo | module
	IsHome            bool           `gorm:"not null;default:false" json:"is_home"`
	ArchElementID     *uint          `gorm:"index" json:"arch_element_id"`
	RepoID            *uint          `gorm:"index" json:"repo_id"`
	Subdirectory      string         `gorm:"size:512;not null;default:''" json:"subdirectory"`
	InheritToChildren bool           `gorm:"not null;default:true" json:"inherit_to_children"`
	DocTypeSlugsJSON  datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"doc_type_slugs"`
	SortOrder         int            `gorm:"not null;default:0" json:"sort_order"`
	CreatedBy         uint           `gorm:"not null" json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

func (ReferenceDocBinding) TableName() string { return config.TablePrefix + "reference_bindings" }

// RefDocListFilter 参考文档分页检索过滤参数。
type RefDocListFilter struct {
	Search        string
	DocKind       string
	ScopeLevel    string
	ArchElementID *uint
	Status        string // active | archived | all
	Offset        int
	Limit         int
}
