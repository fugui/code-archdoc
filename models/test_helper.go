package models

import (
	"fmt"
	"testing"

	"gorm.io/gorm"
)

// EnsureSharedTablesForTest 仅用于物理隔离临时测试库：建出 5 张平台共享只读测试表，严禁在生产 InitDB 中调用。
func EnsureSharedTablesForTest(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&Department{},
		&User{},
		&Repository{},
		&ArchitectureElement{},
		&SysAuditLog{},
	); err != nil {
		return fmt.Errorf("初始化测试共享表失败: %w", err)
	}
	return nil
}

// SetupTestDB 在 testdb.SetupIsolatedDB(t) 创建的独立临时库中初始化 5 张共享只读测试表 + 16 张 archdoc_* 自有表。
func SetupTestDB(t *testing.T, db *gorm.DB) error {
	t.Helper()
	if err := EnsureSharedTablesForTest(db); err != nil {
		return err
	}
	return SetupWithDB(db)
}
