package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// Document 某一 (仓库, 子目录, 分支基线, 文档类型) 下的逻辑文档主记录
type Document struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	RepoID              uint       `gorm:"not null;uniqueIndex:idx_archdoc_doc_scope_unique,priority:1;index" json:"repo_id"`
	Subdirectory        string     `gorm:"size:512;not null;default:'';uniqueIndex:idx_archdoc_doc_scope_unique,priority:2" json:"subdirectory"`
	RefName             string     `gorm:"size:128;not null;default:'master';uniqueIndex:idx_archdoc_doc_scope_unique,priority:3" json:"ref_name"`
	DocTypeSlug         string     `gorm:"size:64;not null;uniqueIndex:idx_archdoc_doc_scope_unique,priority:4;index" json:"doc_type_slug"`
	DocTypeRevisionID   uint       `gorm:"not null;index" json:"doc_type_revision_id"`
	Title               string     `gorm:"size:255;not null" json:"title"`
	Status              string     `gorm:"size:24;not null;default:'draft';index" json:"status"`
	CurrentRevisionNo   int        `gorm:"default:0;not null" json:"current_revision_no"`
	CurrentRevisionID   *uint      `json:"current_revision_id"`
	LastCommentNo       int        `gorm:"default:0;not null" json:"-"`
	PublishedRevisionID *uint      `json:"published_revision_id"`
	PublishedCommitSHA  string     `gorm:"size:40;default:'';index" json:"published_commit_sha"`
	PublishedRelPath    string     `gorm:"size:512;default:''" json:"published_rel_path"`
	PublishedBy         *uint      `json:"published_by"`
	PublishedAt         *time.Time `json:"published_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (Document) TableName() string { return config.TablePrefix + "documents" }

// DocumentRevision 文档不可变修订历史（任何生成/直编/Chat Accept/回滚均追加新行，绝不原地覆写）
type DocumentRevision struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	DocumentID         uint           `gorm:"not null;uniqueIndex:idx_archdoc_doc_rev_unique,priority:1;index" json:"document_id"`
	RevisionNo         int            `gorm:"not null;uniqueIndex:idx_archdoc_doc_rev_unique,priority:2" json:"revision_no"`
	SnapshotID         uint           `gorm:"not null;index" json:"snapshot_id"`
	CommitSHA          string         `gorm:"size:40;not null" json:"commit_sha"`
	DocTypeRevisionID  uint           `gorm:"not null;index" json:"doc_type_revision_id"`
	Source             string         `gorm:"size:32;not null" json:"source"`
	SourceRefID        *uint          `json:"source_ref_id"`
	ChangeSummary      string         `gorm:"size:500;default:''" json:"change_summary"`
	SectionsJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"sections"`
	FullMarkdown       string         `gorm:"type:text;not null" json:"full_markdown"`
	VerifierPassed     bool           `gorm:"default:false;not null" json:"verifier_passed"`
	CanApprove         bool           `gorm:"default:false;not null" json:"can_approve"`
	SlotCoverageRate   float64        `gorm:"default:0;not null" json:"slot_coverage_rate"`
	VerifierReportJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_report"`
	CreatedBy          uint           `gorm:"not null;index" json:"created_by"`
	CreatedAt          time.Time      `json:"created_at"`
}

func (DocumentRevision) TableName() string { return config.TablePrefix + "document_revisions" }

// SectionEvidence 章节槽位所有权、孤立态、校验摘要与稳定证据锚点索引
type SectionEvidence struct {
	ID                  uint           `gorm:"primaryKey" json:"id"`
	DocumentID          uint           `gorm:"not null;uniqueIndex:idx_archdoc_sec_ev_unique,priority:1;index" json:"document_id"`
	RevisionID          uint           `gorm:"not null;uniqueIndex:idx_archdoc_sec_ev_unique,priority:2;index" json:"revision_id"`
	SlotID              string         `gorm:"size:64;not null;uniqueIndex:idx_archdoc_sec_ev_unique,priority:3" json:"slot_id"`
	SectionOrder        int            `gorm:"default:0;not null" json:"section_order"`
	Title               string         `gorm:"size:255;not null" json:"title"`
	Ownership           string         `gorm:"size:16;not null;default:'auto'" json:"ownership"`
	IsOrphan            bool           `gorm:"default:false;not null;index" json:"is_orphan"`
	ManuallyEdited      bool           `gorm:"default:false;not null" json:"manually_edited"`
	NeedsDriftReview    bool           `gorm:"default:false;not null" json:"needs_drift_review"`
	ContentSHA256       string         `gorm:"size:64;not null" json:"content_sha256"`
	VerifierSummaryJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_summary"`
	EvidencesJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"evidences"`
	RefCitationsJSON    datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"ref_citations"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

func (SectionEvidence) TableName() string { return config.TablePrefix + "section_evidences" }
