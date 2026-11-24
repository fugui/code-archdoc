package services

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/sandbox"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

// RBACService 实现 F-13 三层权限决策引擎（含 SC-12 子目录负责人严格隔离）。
type RBACService struct {
	masterRepo SharedMasterRepo
}

func NewRBACService(masterRepo SharedMasterRepo) *RBACService {
	return &RBACService{masterRepo: masterRepo}
}

// CheckScopeWritePermission 判定当前用户对 (repoID, rawSubdir) 是否具备写/生成/发布权限。
// 严格遵循三层权限顺序：
// 1. super_admin / archdoc_admin 放行全部范围；
// 2. Repository.OwnerID 或 Repository.RelatedMembers 包含当前用户 -> 放行整仓及全部子目录；
// 3. 仅当规范化 subdir 非空时，ArchitectureElement(repo_id, normalized_subdir).OwnerID == user.ID 严格全等放行。
func (s *RBACService) CheckScopeWritePermission(ctx context.Context, user *commonModels.User, repoID uint, rawSubdir string) (bool, error) {
	if user == nil || user.ID == 0 {
		return false, nil
	}

	normSubdir, err := sandbox.NormalizeSubdir(rawSubdir)
	if err != nil {
		return false, err
	}

	// 第 1 层：模块管理员与超级管理员
	if user.HasRole(config.AdminRole) {
		return true, nil
	}

	// 第 2 层：仓库级负责人与关联成员
	repo, err := s.masterRepo.GetRepository(ctx, repoID)
	if err != nil {
		return false, err
	}
	if IsRepoLevelWriter(user, repo) {
		return true, nil
	}

	// 第 3 层：子目录负责人严格全等匹配（整仓 subdir == "" 直接拒绝，严禁前缀模糊匹配）
	if normSubdir == "" {
		return false, nil
	}
	elems, err := s.masterRepo.FindArchElementsByRepoAndOwner(ctx, repoID, user.ID)
	if err != nil {
		return false, err
	}
	return HasSubdirOwnership(elems, normSubdir), nil
}

// IsRepoLevelWriter 判定用户是否为目标仓库的仓库级负责人或关联成员（含管理员）。
func IsRepoLevelWriter(user *commonModels.User, repo *commonModels.Repository) bool {
	if user == nil || user.ID == 0 || repo == nil {
		return false
	}
	if user.HasRole(config.AdminRole) {
		return true
	}
	if repo.OwnerID == user.ID {
		return true
	}
	return ContainsRelatedMember(repo.RelatedMembers, user)
}

// HasSubdirOwnership 判断用户的架构元素列表中是否存在规范化后严格全等于 normSubdir 的绑定记录。
func HasSubdirOwnership(userElems []models.ArchitectureElement, normSubdir string) bool {
	if normSubdir == "" {
		return false
	}
	for _, el := range userElems {
		normEl, err := sandbox.NormalizeSubdir(el.Subdirectory)
		if err == nil && normEl != "" && normEl == normSubdir {
			return true
		}
	}
	return false
}

// ContainsRelatedMember 兼容解析 code-bench / code-shield 中 Repository.RelatedMembers 的多种 JSON 格式（[]uint 或 []string）。
func ContainsRelatedMember(raw datatypes.JSON, user *commonModels.User) bool {
	if len(raw) == 0 || user == nil || user.ID == 0 {
		return false
	}

	var uintIDs []uint
	if err := json.Unmarshal(raw, &uintIDs); err == nil {
		for _, id := range uintIDs {
			if id == user.ID {
				return true
			}
		}
		return false
	}

	var strItems []string
	if err := json.Unmarshal(raw, &strItems); err == nil {
		userIDStr := strconv.FormatUint(uint64(user.ID), 10)
		usernameLower := strings.ToLower(strings.TrimSpace(user.Username))
		emailLower := strings.ToLower(strings.TrimSpace(user.Email))
		for _, item := range strItems {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if trimmed == userIDStr {
				return true
			}
			lower := strings.ToLower(trimmed)
			if (usernameLower != "" && lower == usernameLower) || (emailLower != "" && lower == emailLower) {
				return true
			}
		}
	}
	return false
}

// CheckArchScopeWritePermission 判定当前用户对四层架构树作用域的写权限（ADR-10 D-4，不影响既有 SC-12 CheckScopeWritePermission）。
// - system：仅 super_admin / archdoc_admin；
// - subsystem / group：管理员，或目标节点及其任意祖先节点（沿 ParentID 回溯 <=16 层，visited 防环）的 OwnerID == user.ID；
// - module：满足节点/祖先 OwnerID == user.ID，或已绑定活跃仓库时满足 CheckScopeWritePermission(repoID, module.Subdirectory)；
// - repo：满足 CheckScopeWritePermission(repoID, normSubdir)。
func (s *RBACService) CheckArchScopeWritePermission(
	ctx context.Context,
	user *commonModels.User,
	scopeLevel string,
	archElementID *uint,
	repoID *uint,
	rawSubdir string,
) (bool, error) {
	if user == nil || user.ID == 0 {
		return false, nil
	}
	if user.HasRole(config.AdminRole) {
		return true, nil
	}

	switch strings.TrimSpace(scopeLevel) {
	case models.RefDocScopeSystem:
		return false, nil

	case models.RefDocScopeSubsystem, models.RefDocScopeGroup, models.RefDocScopeModule:
		if archElementID == nil || *archElementID == 0 {
			return false, nil
		}
		el, err := s.masterRepo.GetArchElement(ctx, *archElementID)
		if err != nil {
			return false, err
		}
		if el.Type != strings.TrimSpace(scopeLevel) {
			return false, nil
		}

		// 沿 ParentID 向上回溯至多 16 层（带 visited 防环）
		visited := make(map[uint]bool, 8)
		cur := el
		for depth := 0; depth < 16 && cur != nil; depth++ {
			if visited[cur.ID] {
				break
			}
			visited[cur.ID] = true
			if cur.OwnerID != nil && *cur.OwnerID == user.ID {
				return true, nil
			}
			if cur.ParentID == nil || *cur.ParentID == 0 {
				break
			}
			parent, pErr := s.masterRepo.GetArchElement(ctx, *cur.ParentID)
			if pErr != nil {
				if errors.Is(pErr, models.ErrNotFound) {
					break
				}
				return false, pErr
			}
			cur = parent
		}

		if scopeLevel == models.RefDocScopeModule && el.RepoID != nil && *el.RepoID > 0 {
			allowed, repoErr := s.CheckScopeWritePermission(ctx, user, *el.RepoID, el.Subdirectory)
			if repoErr != nil {
				if errors.Is(repoErr, models.ErrNotFound) {
					return false, nil
				}
				return false, repoErr
			}
			return allowed, nil
		}
		return false, nil

	case models.RefDocScopeRepo:
		if repoID == nil || *repoID == 0 {
			return false, nil
		}
		return s.CheckScopeWritePermission(ctx, user, *repoID, rawSubdir)

	default:
		return false, nil
	}
}
