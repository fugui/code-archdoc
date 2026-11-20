package models

import (
	"fmt"
	"log"

	"code-archdoc/config"
	commonAuth "code-common/backend/auth"
	"code-common/backend/gormdb"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DB 模块全局 GORM 数据库连接句柄（仅限 main.go 与 handlers 认证/审计兼容使用；pkg/ 与 services/ 严禁直接访问）
var DB *gorm.DB

// AllOwnModels 按基础域到关联域的依赖顺序返回本模块全部 18 张自有 archdoc_* GORM 模型。
func AllOwnModels() []any {
	return []any{
		&DocType{},
		&DocTypeRevision{},
		&RepoModulePolicy{},
		&RepoModule{},
		&RepoSnapshot{},
		&CodeIRFacet{},
		&GenTask{},
		&GenSlotTask{},
		&Document{},
		&DocumentRevision{},
		&SectionEvidence{},
		&ChatSession{},
		&ChatMessage{},
		&RepoRemoteHead{},
		&DocumentDrift{},
		&LLMConfig{},
		&LLMUsageLog{},
		&QualityMetric{},
	}
}

// InitDB 连接 PostgreSQL 数据库并执行本模块自有表迁移与幂等种子初始化。
// 严禁在生产运行时对共享表（users / departments / repositories / architecture_elements）执行 DDL 变更。
// TODO(security): 生产环境请为本服务分配遵循最小权限原则的独立数据库账号，并开启 SSL/mTLS 传输加密
func InitDB() error {
	db, err := gormdb.Connect(config.AppConfig.Database, gormdb.Options{
		ServiceName: config.ServiceName,
	})
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	return SetupWithDB(db)
}

// SetupWithDB 使用外部注入的 *gorm.DB 仅迁移 16 张 archdoc_* 自有表，并幂等 Seed 默认 LLM 配置骨架行。
func SetupWithDB(db *gorm.DB) error {
	DB = db

	if err := DB.AutoMigrate(AllOwnModels()...); err != nil {
		return fmt.Errorf("迁移 archdoc_* 数据表结构失败: %w", err)
	}

	// 仅在独立调试库已存在 users 表时，按需引导默认管理员（避免在共享生产库触碰公共表 DDL）
	if DB.Migrator().HasTable(&User{}) {
		if err := commonAuth.EnsureSeedAdmin(DB, config.AdminRole); err != nil {
			log.Printf("[%s] 提示: 初始化默认管理员账号未完成: %v", config.ServiceName, err)
		}
	}

	if err := SeedDefaultLLMConfigs(DB); err != nil {
		return fmt.Errorf("初始化默认 LLM 配置骨架失败: %w", err)
	}

	return nil
}

// SeedDefaultLLMConfigs 幂等初始化 fast / flagship 两行默认 LLM 配置骨架（若已存在则零修改）。
func SeedDefaultLLMConfigs(db *gorm.DB) error {
	defaults := []LLMConfig{
		{
			Tier:             ModelTierFast,
			TimeoutSeconds:   120,
			MaxRetries:       3,
			MaxTokensPerTask: 200000,
			MaxFilesPerTask:  2000,
			IsActive:         false,
			LastProbeStatus:  "unknown",
		},
		{
			Tier:             ModelTierFlagship,
			TimeoutSeconds:   180,
			MaxRetries:       3,
			MaxTokensPerTask: 200000,
			MaxFilesPerTask:  2000,
			IsActive:         false,
			LastProbeStatus:  "unknown",
		},
	}

	for i := range defaults {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tier"}},
			DoNothing: true,
		}).Create(&defaults[i]).Error; err != nil {
			return err
		}
	}
	return nil
}
