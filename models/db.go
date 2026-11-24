package models

import (
	"fmt"
	"log"
	"strings"

	"code-archdoc/config"
	commonAuth "code-common/backend/auth"
	"code-common/backend/gormdb"

	"gorm.io/gorm"
)

// DB 模块全局 GORM 数据库连接句柄（仅限 main.go 与 handlers 认证/审计兼容使用；pkg/ 与 services/ 严禁直接访问）
var DB *gorm.DB

// AllOwnModels 按基础域到关联域的依赖顺序返回本模块全部 22 张自有 archdoc_* GORM 模型。
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
		&ReviewComment{},
		&ChatSession{},
		&ChatMessage{},
		&ChatProposalItem{},
		&RepoRemoteHead{},
		&DocumentDrift{},
		&LLMConfig{},
		&LLMUsageLog{},
		&QualityMetric{},
		&ReferenceDoc{},
		&ReferenceDocBinding{},
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

// SetupWithDB 使用外部注入的 *gorm.DB 仅迁移 20 张 archdoc_* 自有表，并平滑升级历史模型配置表结构。
func SetupWithDB(db *gorm.DB) error {
	DB = db

	if err := migrateLegacyGovernanceSchema(DB); err != nil {
		return fmt.Errorf("迁移历史 LLM 与文档类型字段失败: %w", err)
	}

	if err := DB.AutoMigrate(AllOwnModels()...); err != nil {
		return fmt.Errorf("迁移 archdoc_* 数据表结构失败: %w", err)
	}

	if err := repairInconsistentLLMConfigs(DB); err != nil {
		return fmt.Errorf("修复异常 LLM 模型配置失败: %w", err)
	}

	// 仅在独立调试库已存在 users 表时，按需引导默认管理员（避免在共享生产库触碰公共表 DDL）
	if DB.Migrator().HasTable(&User{}) {
		if err := commonAuth.EnsureSeedAdmin(DB, config.AdminRole); err != nil {
			log.Printf("[%s] 提示: 初始化默认管理员账号未完成: %v", config.ServiceName, err)
		}
	}

	return nil
}

// migrateLegacyGovernanceSchema 幂等将历史 fast/flagship 分层表结构平滑升级为平等模型池结构。
func migrateLegacyGovernanceSchema(db *gorm.DB) error {
	llmTable := (LLMConfig{}).TableName()
	if db.Migrator().HasTable(llmTable) && db.Migrator().HasColumn(&LLMConfig{}, "tier") {
		type legacyLLMRow struct {
			ID        uint
			Tier      string
			ModelName string
			IsActive  bool
			IsDefault bool
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			ddls := []string{
				fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS name varchar(80) NOT NULL DEFAULT ''", llmTable),
				fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS api_key text NOT NULL DEFAULT ''", llmTable),
				fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false", llmTable),
			}
			for _, sqlStmt := range ddls {
				if err := tx.Exec(sqlStmt).Error; err != nil {
					return err
				}
			}

			// 清理旧版自动 Seed 产生的空骨架记录
			if err := tx.Exec(
				fmt.Sprintf("DELETE FROM %s WHERE COALESCE(TRIM(base_url), '') = '' AND COALESCE(TRIM(model_name), '') = ''", llmTable),
			).Error; err != nil {
				return err
			}

			var rows []legacyLLMRow
			if err := tx.Table(llmTable).
				Select("id, tier, model_name, is_active, is_default").
				Order("id ASC").
				Scan(&rows).Error; err != nil {
				return err
			}

			usedNames := make(map[string]bool, len(rows))
			hasDefault := false
			defaultIdx := -1
			for i, row := range rows {
				if row.IsDefault {
					hasDefault = true
				}
				if defaultIdx == -1 && row.Tier == "flagship" && row.IsActive {
					defaultIdx = i
				}
			}
			if defaultIdx == -1 {
				for i, row := range rows {
					if row.IsActive {
						defaultIdx = i
						break
					}
				}
			}
			// 绝不将 is_active=false 的历史记录自动标记为默认模型，避免制造「已停用且默认」的非法状态

			for i, row := range rows {
				candidate := strings.TrimSpace(row.ModelName)
				if candidate == "" {
					candidate = strings.TrimSpace(row.Tier)
				}
				if candidate == "" {
					candidate = fmt.Sprintf("model-%d", row.ID)
				}
				if usedNames[candidate] && strings.TrimSpace(row.Tier) != "" {
					candidate = fmt.Sprintf("%s-%s", candidate, strings.TrimSpace(row.Tier))
				}
				if usedNames[candidate] {
					candidate = fmt.Sprintf("%s-%d", candidate, row.ID)
				}
				usedNames[candidate] = true

				setDefault := row.IsDefault
				if !hasDefault && i == defaultIdx {
					setDefault = true
				}
				if err := tx.Exec(
					fmt.Sprintf("UPDATE %s SET name = ?, is_default = ?, api_key_masked = CASE WHEN COALESCE(api_key, '') = '' THEN '' ELSE api_key_masked END WHERE id = ?", llmTable),
					candidate, setDefault, row.ID,
				).Error; err != nil {
					return err
				}
			}

			cleanupDDLs := []string{
				fmt.Sprintf("DROP INDEX IF EXISTS idx_%s_tier", llmTable),
				fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS tier", llmTable),
				fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS api_key_cipher", llmTable),
			}
			for _, sqlStmt := range cleanupDDLs {
				if err := tx.Exec(sqlStmt).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	legacyModelTierModels := []any{
		&DocType{},
		&LLMUsageLog{},
		&GenSlotTask{},
		&ChatMessage{},
	}
	for _, mdl := range legacyModelTierModels {
		if db.Migrator().HasTable(mdl) && db.Migrator().HasColumn(mdl, "model_tier") {
			if err := db.Migrator().DropColumn(mdl, "model_tier"); err != nil {
				return err
			}
		}
	}
	return nil
}

// repairInconsistentLLMConfigs 幂等清理历史遗留的「已停用但仍标记为默认」异常模型配置，避免前端与更新守卫死锁。
func repairInconsistentLLMConfigs(db *gorm.DB) error {
	res := db.Model(&LLMConfig{}).
		Where("is_default = ? AND is_active = ?", true, false).
		Update("is_default", false)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[%s] 检测到并已自动修复 %d 条「已停用但仍标记为默认」的异常 LLM 配置（已清除默认标记）", config.ServiceName, res.RowsAffected)
	}
	return nil
}
