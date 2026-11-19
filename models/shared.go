package models

import (
	"time"

	commonModels "code-common/backend/models"
)

// 共享只读平台主数据模型别名（严格复用 code-common/backend/models，生产运行时零 DDL 变更）
type (
	Department  = commonModels.Department
	User        = commonModels.User
	Repository  = commonModels.Repository
	SysAuditLog = commonModels.SysAuditLog
)

// ArchitectureElement 映射 code-bench 维护的共享只读表 architecture_elements（本模块只读不写）
type ArchitectureElement struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Identifier   string    `gorm:"not null" json:"identifier"`
	NameCn       string    `gorm:"not null" json:"name_cn"`
	NameEn       string    `gorm:"not null" json:"name_en"`
	Type         string    `gorm:"not null" json:"type"` // "subsystem" | "group" | "module"
	ParentID     *uint     `json:"parent_id"`
	OwnerID      *uint     `json:"owner_id"`
	RepoID       *uint     `gorm:"index" json:"repo_id"`
	Subdirectory string    `gorm:"default:''" json:"subdirectory"`
	Description  string    `gorm:"default:''" json:"description"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (ArchitectureElement) TableName() string {
	return "architecture_elements"
}
