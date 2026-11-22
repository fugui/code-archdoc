package models

import (
	"context"
	"errors"
	"strings"
	"time"

	"code-archdoc/config"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NormalizeRepoDefaultBranch 规范化共享仓主数据的默认分支名（为空时回退 "master"）。
func NormalizeRepoDefaultBranch(repoBranch string) string {
	b := strings.TrimSpace(repoBranch)
	if b == "" {
		return "master"
	}
	return b
}

// ResolveEffectiveBranch 统一解析仓库在 code-archdoc 中的有效跟踪分支：
// 优先使用 archdoc_repo_module_policies.target_branch，其次使用共享表 repositories.branch，均为空时回退 "master"。
func ResolveEffectiveBranch(repoBranch, targetBranch string) string {
	if tb := strings.TrimSpace(targetBranch); tb != "" {
		return tb
	}
	return NormalizeRepoDefaultBranch(repoBranch)
}

// RepoModulePolicy 仓库级治理策略（含模块发现策略与目标分支覆写；缺省行使用默认策略 merged + MinEffectiveFiles=3，管理员/仓库级写者可配置）。
type RepoModulePolicy struct {
	ID                uint           `gorm:"primaryKey" json:"id"`
	RepoID            uint           `gorm:"not null;uniqueIndex" json:"repo_id"`
	Strategy          string         `gorm:"size:20;not null;default:'merged'" json:"strategy"`     // arch_elements | top_level_dirs | merged
	ExcludeGlobs      datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"exclude_globs"` // 叠加默认排除名单
	MinEffectiveFiles int            `gorm:"not null;default:3" json:"min_effective_files"`         // 降噪后有效文件数下限
	TargetBranch      string         `gorm:"size:128;not null;default:''" json:"target_branch"`     // ADR-07：单仓目标分支覆写（空串表示跟随 repositories.branch）
	UpdatedBy         *uint          `json:"updated_by"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

func (RepoModulePolicy) TableName() string { return config.TablePrefix + "repo_module_policies" }

// RepoModule 派生模块索引（非主数据：负责人与中文名仍以 architecture_elements 为唯一事实源，严禁冗余负责人字段）。
type RepoModule struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	RepoID         uint      `gorm:"not null;uniqueIndex:idx_archdoc_repo_module,priority:1;index" json:"repo_id"`
	Subdirectory   string    `gorm:"size:512;not null;uniqueIndex:idx_archdoc_repo_module,priority:2" json:"subdirectory"` // 规范化路径
	Source         string    `gorm:"size:20;not null" json:"source"`                                                       // arch_element | discovered
	ArchElementID  *uint     `gorm:"index" json:"arch_element_id"`
	Status         string    `gorm:"size:16;not null;default:'active';index" json:"status"` // active | missing | ignored
	EffectiveFiles int       `gorm:"not null;default:0" json:"effective_files"`
	TotalLOC       int       `gorm:"not null;default:0" json:"total_loc"`
	LastSeenCommit string    `gorm:"size:40;default:''" json:"last_seen_commit"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (RepoModule) TableName() string { return config.TablePrefix + "repo_modules" }

// ModuleStore 实现 archdoc_repo_module_policies 与 archdoc_repo_modules 的持久化仓储。
type ModuleStore struct {
	db *gorm.DB
}

func NewModuleStore(db *gorm.DB) *ModuleStore {
	return &ModuleStore{db: db}
}

func (s *ModuleStore) GetPolicy(ctx context.Context, repoID uint) (*RepoModulePolicy, error) {
	var policy RepoModulePolicy
	err := s.db.WithContext(ctx).Where("repo_id = ?", repoID).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func (s *ModuleStore) UpsertPolicy(ctx context.Context, policy *RepoModulePolicy) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"strategy", "exclude_globs", "min_effective_files", "updated_by", "updated_at",
		}),
	}).Create(policy).Error
}

func (s *ModuleStore) ListPoliciesByRepos(ctx context.Context, repoIDs []uint) ([]RepoModulePolicy, error) {
	if len(repoIDs) == 0 {
		return []RepoModulePolicy{}, nil
	}
	var policies []RepoModulePolicy
	if err := s.db.WithContext(ctx).Where("repo_id IN ?", repoIDs).Find(&policies).Error; err != nil {
		return nil, err
	}
	return policies, nil
}

func (s *ModuleStore) UpdateTargetBranch(ctx context.Context, repoID uint, targetBranch string, updatedBy *uint) error {
	row := &RepoModulePolicy{
		RepoID:            repoID,
		Strategy:          ModuleStrategyMerged,
		ExcludeGlobs:      datatypes.JSON("[]"),
		MinEffectiveFiles: 3,
		TargetBranch:      strings.TrimSpace(targetBranch),
		UpdatedBy:         updatedBy,
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"target_branch", "updated_by", "updated_at",
		}),
	}).Create(row).Error
}

func (s *ModuleStore) ListModulesByRepo(ctx context.Context, repoID uint) ([]RepoModule, error) {
	var modules []RepoModule
	if err := s.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Order("subdirectory ASC").
		Find(&modules).Error; err != nil {
		return nil, err
	}
	return modules, nil
}

func (s *ModuleStore) ListModulesByRepos(ctx context.Context, repoIDs []uint) ([]RepoModule, error) {
	if len(repoIDs) == 0 {
		return []RepoModule{}, nil
	}
	var modules []RepoModule
	if err := s.db.WithContext(ctx).
		Where("repo_id IN ?", repoIDs).
		Order("repo_id ASC, subdirectory ASC").
		Find(&modules).Error; err != nil {
		return nil, err
	}
	return modules, nil
}

func (s *ModuleStore) GetModuleByID(ctx context.Context, repoID, moduleID uint) (*RepoModule, error) {
	var mod RepoModule
	err := s.db.WithContext(ctx).
		Where("repo_id = ? AND id = ?", repoID, moduleID).
		First(&mod).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &mod, nil
}

func (s *ModuleStore) UpsertModules(ctx context.Context, repoID uint, discovered []RepoModule, seenAt time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []RepoModule
		if err := tx.Where("repo_id = ?", repoID).Find(&existing).Error; err != nil {
			return err
		}
		existingBySub := make(map[string]RepoModule, len(existing))
		for _, m := range existing {
			existingBySub[m.Subdirectory] = m
		}

		seenSet := make(map[string]bool, len(discovered))
		for i := range discovered {
			mod := discovered[i]
			mod.RepoID = repoID
			mod.LastSeenAt = seenAt
			seenSet[mod.Subdirectory] = true

			if prev, ok := existingBySub[mod.Subdirectory]; ok {
				// 保留用户显式忽略态；其余（active / missing）自动恢复为 active
				nextStatus := ModuleStatusActive
				if prev.Status == ModuleStatusIgnored {
					nextStatus = ModuleStatusIgnored
				}
				if err := tx.Model(&RepoModule{}).Where("id = ?", prev.ID).Updates(map[string]any{
					"source":           mod.Source,
					"arch_element_id":  mod.ArchElementID,
					"status":           nextStatus,
					"effective_files":  mod.EffectiveFiles,
					"total_loc":        mod.TotalLOC,
					"last_seen_commit": mod.LastSeenCommit,
					"last_seen_at":     seenAt,
				}).Error; err != nil {
					return err
				}
			} else {
				mod.Status = ModuleStatusActive
				if err := tx.Create(&mod).Error; err != nil {
					return err
				}
			}
		}

		// 在新基线中消失的非 ignored 模块标为 missing
		for _, prev := range existing {
			if seenSet[prev.Subdirectory] || prev.Status == ModuleStatusIgnored {
				continue
			}
			if prev.Status != ModuleStatusMissing {
				if err := tx.Model(&RepoModule{}).Where("id = ?", prev.ID).Update("status", ModuleStatusMissing).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *ModuleStore) UpdateModuleStatus(ctx context.Context, repoID, moduleID uint, status string) (*RepoModule, error) {
	res := s.db.WithContext(ctx).Model(&RepoModule{}).
		Where("repo_id = ? AND id = ?", repoID, moduleID).
		Update("status", status)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.GetModuleByID(ctx, repoID, moduleID)
}

func (s *ModuleStore) UpsertSingleModule(ctx context.Context, mod *RepoModule) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "repo_id"}, {Name: "subdirectory"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source", "arch_element_id", "status", "effective_files", "total_loc",
			"last_seen_commit", "last_seen_at", "updated_at",
		}),
	}).Create(mod).Error
}

func (s *ModuleStore) MarkSubdirMissing(ctx context.Context, repoID uint, subdir string) error {
	return s.db.WithContext(ctx).Model(&RepoModule{}).
		Where("repo_id = ? AND subdirectory = ?", repoID, subdir).
		Update("status", ModuleStatusMissing).Error
}
