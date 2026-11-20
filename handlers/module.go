package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

func parseRepoIDParam(c *gin.Context) (uint, bool) {
	v, err := strconv.ParseUint(c.Param("repo_id"), 10, 64)
	if err != nil || v == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "repo_id 参数非法", nil)
		return 0, false
	}
	return uint(v), true
}

// ListRepoModules 查询仓库派生模块索引及当前策略（GET /api/repos/:repo_id/modules）。
func (h *AppHandlers) ListRepoModules(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	vo, err := h.moduleSvc.ListModules(c.Request.Context(), repoID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return
		}
		log.Printf("[%s] 查询仓库模块索引失败 (repo_id=%d): %v", config.ServiceName, repoID, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询仓库模块索引失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}

// GetRepoModulePolicy 查询仓库级模块发现策略（GET /api/repos/:repo_id/module-policy）。
func (h *AppHandlers) GetRepoModulePolicy(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	vo, err := h.moduleSvc.GetPolicy(c.Request.Context(), repoID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return
		}
		log.Printf("[%s] 查询模块发现策略失败 (repo_id=%d): %v", config.ServiceName, repoID, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询模块发现策略失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}

// UpdateRepoModulePolicy 更新仓库级模块发现策略（PUT /api/repos/:repo_id/module-policy，仅仓库级写者/管理员）。
func (h *AppHandlers) UpdateRepoModulePolicy(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	user, _, allowed := h.CheckScopeWriteOrAbort(c, repoID, "")
	if !allowed {
		return
	}

	var req services.UpdateModulePolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.moduleSvc.UpdatePolicy(c.Request.Context(), repoID, req, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidModuleParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
		default:
			log.Printf("[%s] 更新模块发现策略失败 (repo_id=%d): %v", config.ServiceName, repoID, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "更新模块发现策略失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("更新仓库 #%d 模块发现策略为 %s", repoID, vo.Strategy),
		"repo_module_policy",
		strconv.FormatUint(uint64(repoID), 10),
		fmt.Sprintf("repo#%d", repoID),
		nil,
		vo,
	)

	c.JSON(http.StatusOK, vo)
}

// RefreshRepoModules 手动同步最新分支并刷新仓库模块索引（POST /api/repos/:repo_id/modules/refresh，仅仓库级写者/管理员）。
func (h *AppHandlers) RefreshRepoModules(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	if _, _, allowed := h.CheckScopeWriteOrAbort(c, repoID, ""); !allowed {
		return
	}

	vo, err := h.moduleSvc.RefreshModules(c.Request.Context(), repoID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return
		}
		log.Printf("[%s] 刷新仓库模块索引失败 (repo_id=%d): %v", config.ServiceName, repoID, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "刷新仓库模块索引失败: "+err.Error(), nil)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"refresh",
		commonModels.AuditLevelP2,
		fmt.Sprintf("刷新仓库 #%d 模块索引，发现 %d 个活跃模块", repoID, vo.ActiveCount),
		"repo_modules",
		strconv.FormatUint(uint64(repoID), 10),
		fmt.Sprintf("repo#%d", repoID),
		nil,
		vo,
	)

	c.JSON(http.StatusOK, vo)
}

// UpdateRepoModuleStatus 切换单个模块的忽略/活跃状态（PATCH /api/repos/:repo_id/modules/:module_id/status）。
func (h *AppHandlers) UpdateRepoModuleStatus(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	modIDVal, err := strconv.ParseUint(c.Param("module_id"), 10, 64)
	if err != nil || modIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "module_id 参数非法", nil)
		return
	}
	if _, _, allowed := h.CheckScopeWriteOrAbort(c, repoID, ""); !allowed {
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.moduleSvc.UpdateModuleStatus(c.Request.Context(), repoID, uint(modIDVal), req.Status)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidModuleParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标模块或代码仓库不存在", nil)
		default:
			log.Printf("[%s] 更新模块状态失败 (repo_id=%d module_id=%d): %v", config.ServiceName, repoID, modIDVal, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "更新模块状态失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("切换仓库 #%d 模块 %s 状态为 %s", repoID, vo.Subdirectory, vo.Status),
		"repo_module",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.Subdirectory,
		nil,
		vo,
	)

	c.JSON(http.StatusOK, vo)
}

// MigrateRepoModule 将源子目录下的全部文档、修订及已发布产物迁移至目标子目录（POST /api/repos/:repo_id/modules/migrate）。
func (h *AppHandlers) MigrateRepoModule(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	if _, _, allowed := h.CheckScopeWriteOrAbort(c, repoID, ""); !allowed {
		return
	}

	var req services.MigrateModuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.moduleSvc.MigrateModule(c.Request.Context(), repoID, req)
	if err != nil {
		switch {
		case errors.Is(err, sandbox.ErrInvalidSubdir):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, err.Error(), nil)
		case errors.Is(err, services.ErrInvalidModuleParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
		case errors.Is(err, models.ErrMigrationConflict):
			WriteError(c, http.StatusConflict, ErrCodeMigrationConflict, err.Error(), nil)
		case errors.Is(err, services.ErrActiveTasksBlockingMigration):
			WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
		default:
			log.Printf("[%s] 迁移模块文档失败 (repo_id=%d %s -> %s): %v", config.ServiceName, repoID, req.FromSubdir, req.ToSubdir, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "迁移模块文档失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"migrate",
		commonModels.AuditLevelP0,
		fmt.Sprintf("迁移仓库 #%d 模块文档: %s -> %s（共 %d 篇，重发布 %d 篇）", repoID, vo.FromSubdir, vo.ToSubdir, vo.MigratedDocuments, vo.RepublishedPublish),
		"repo_module",
		strconv.FormatUint(uint64(repoID), 10),
		fmt.Sprintf("%s->%s", vo.FromSubdir, vo.ToSubdir),
		nil,
		vo,
	)

	c.JSON(http.StatusOK, vo)
}
