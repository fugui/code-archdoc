package models

import (
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
)

// RepoSnapshot 代码仓特定范围与提交的抽取快照（同快照多 DocType 共享 Code IR 与术语表）
type RepoSnapshot struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	RepoID          uint           `gorm:"not null;uniqueIndex:idx_archdoc_snapshot_key,priority:1;index" json:"repo_id"`
	Subdirectory    string         `gorm:"size:512;not null;default:'';uniqueIndex:idx_archdoc_snapshot_key,priority:2" json:"subdirectory"`
	RefName         string         `gorm:"size:128;not null;default:'master';uniqueIndex:idx_archdoc_snapshot_key,priority:3" json:"ref_name"`
	CommitSHA       string         `gorm:"size:40;not null;uniqueIndex:idx_archdoc_snapshot_key,priority:4" json:"commit_sha"`
	IRSchemaVersion string         `gorm:"size:16;not null;default:'1.0';uniqueIndex:idx_archdoc_snapshot_key,priority:5" json:"ir_schema_version"`
	Status          string         `gorm:"size:20;not null;default:'extracting'" json:"status"`
	EffectiveFiles  int            `gorm:"default:0;not null" json:"effective_files"`
	SkippedFiles    int            `gorm:"default:0;not null" json:"skipped_files"`
	TotalLOC        int            `gorm:"default:0;not null" json:"total_loc"`
	GlossaryPath    string         `gorm:"size:512;default:''" json:"glossary_path"`
	GlossaryModel   string         `gorm:"size:120;default:''" json:"glossary_model"`
	StatsJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"stats"`
	LastAccessedAt  time.Time      `gorm:"index;not null" json:"last_accessed_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (RepoSnapshot) TableName() string { return config.TablePrefix + "repo_snapshots" }

// CodeIRFacet 单个 Code IR 分片元数据索引（大 JSON 载荷压缩落盘于 FacetStore，彻底防共享 PG 膨胀）
type CodeIRFacet struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	SnapshotID      uint           `gorm:"not null;uniqueIndex:idx_archdoc_ir_facet_unique,priority:1;index" json:"snapshot_id"`
	FacetName       string         `gorm:"size:32;not null;uniqueIndex:idx_archdoc_ir_facet_unique,priority:2" json:"facet_name"`
	StoragePath     string         `gorm:"size:512;not null" json:"storage_path"`
	ContentSHA256   string         `gorm:"size:64;not null" json:"content_sha256"`
	ItemCount       int            `gorm:"default:0;not null" json:"item_count"`
	SizeBytes       int64          `gorm:"default:0;not null" json:"size_bytes"`
	CompressedBytes int64          `gorm:"default:0;not null" json:"compressed_bytes"`
	SummaryJSON     datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"summary"`
	LastAccessedAt  time.Time      `gorm:"index;not null" json:"last_accessed_at"`
	EvictedAt       *time.Time     `gorm:"index" json:"evicted_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (CodeIRFacet) TableName() string { return config.TablePrefix + "code_ir_facets" }
