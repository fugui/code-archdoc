package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

func parseRepoCoverageFilter(c *gin.Context, withPagination bool) (services.RepoCoverageFilter, bool) {
	page := 1
	pageSize := 25
	if withPagination {
		var pageErr error
		page, pageErr = strconv.Atoi(c.DefaultQuery("page", "1"))
		if pageErr != nil {
			page = 1
		}
		pageSizeRaw := strings.TrimSpace(c.Query("page_size"))
		if pageSizeRaw == "" {
			pageSizeRaw = strings.TrimSpace(c.Query("pageSize"))
		}
		if pageSizeRaw != "" {
			if parsedSize, sizeErr := strconv.Atoi(pageSizeRaw); sizeErr == nil {
				pageSize = parsedSize
			}
		}
	}

	var deptIDPtr *uint
	if deptStr := strings.TrimSpace(c.Query("department_id")); deptStr != "" {
		parsed, err := strconv.ParseUint(deptStr, 10, 64)
		if err != nil || parsed == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "department_id 参数格式非法", nil)
			return services.RepoCoverageFilter{}, false
		}
		v := uint(parsed)
		deptIDPtr = &v
	}

	var subsystemIDPtr *uint
	if subsysStr := strings.TrimSpace(c.Query("subsystem_id")); subsysStr != "" {
		parsed, err := strconv.ParseUint(subsysStr, 10, 64)
		if err != nil || parsed == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "subsystem_id 参数格式非法", nil)
			return services.RepoCoverageFilter{}, false
		}
		v := uint(parsed)
		subsystemIDPtr = &v
	}

	archBinding := strings.ToLower(strings.TrimSpace(c.Query("arch_binding")))
	switch archBinding {
	case "", "all", "bound", "unbound":
	default:
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "arch_binding 参数仅支持 all | bound | unbound", nil)
		return services.RepoCoverageFilter{}, false
	}

	return services.RepoCoverageFilter{
		Page:         page,
		PageSize:     pageSize,
		Search:       c.Query("search"),
		DepartmentID: deptIDPtr,
		DocType:      c.Query("doc_type"),
		Status:       c.Query("status"),
		ScopeType:    c.Query("scope_type"),
		SubsystemID:  subsystemIDPtr,
		ArchBinding:  archBinding,
	}, true
}

// ListRepos 分页查询代码仓与 Monorepo 子目录覆盖矩阵（GET /api/repos）。
func (h *AppHandlers) ListRepos(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	filter, valid := parseRepoCoverageFilter(c, true)
	if !valid {
		return
	}

	resp, err := h.repoSvc.ListRepoCoverage(c.Request.Context(), user, filter)
	if err != nil {
		log.Printf("[%s] 查询仓库覆盖矩阵失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询仓库覆盖视图失败", nil)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetArchTree 只读聚合架构元素层级树（子系统 -> 功能组 -> 模块）与文档覆盖统计（GET /api/arch-tree）。
func (h *AppHandlers) GetArchTree(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	filter, valid := parseRepoCoverageFilter(c, false)
	if !valid {
		return
	}

	resp, err := h.repoSvc.GetArchTree(c.Request.Context(), user, filter)
	if err != nil {
		log.Printf("[%s] 查询架构树覆盖视图失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询架构树覆盖视图失败", nil)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// GetRepoScopes 查询单个仓库（整仓 + 全部 ArchitectureElement 子目录）详细范围与最近任务（GET /api/repos/:repo_id/scopes）。
func (h *AppHandlers) GetRepoScopes(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	repoIDVal, err := strconv.ParseUint(c.Param("repo_id"), 10, 64)
	if err != nil || repoIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "repo_id 参数非法", nil)
		return
	}

	resp, err := h.repoSvc.GetRepoScopes(c.Request.Context(), user, uint(repoIDVal))
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return
		}
		log.Printf("[%s] 查询仓库范围详情失败 (repo_id=%d): %v", config.ServiceName, repoIDVal, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询仓库范围详情失败", nil)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// ListRepoBranches 轻量列举远端代码仓分支清单并标识默认分支与当前生效分支（GET /api/repos/:repo_id/branches）。
func (h *AppHandlers) ListRepoBranches(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}

	refreshRaw := strings.TrimSpace(c.Query("refresh"))
	forceRefresh := strings.EqualFold(refreshRaw, "true") || refreshRaw == "1"

	vo, err := h.branchSvc.ListRepoBranches(c.Request.Context(), repoID, forceRefresh)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
		case errors.Is(err, services.ErrRemoteBranchFetchFailed):
			WriteError(c, http.StatusBadGateway, ErrCodeRemoteBranchFetchFailed, err.Error(), nil)
		default:
			log.Printf("[%s] 查询仓库远端分支失败 (repo_id=%d): %v", config.ServiceName, repoID, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询仓库分支列表失败", nil)
		}
		return
	}

	c.JSON(http.StatusOK, vo)
}

// SwitchRepoBranch 切换单仓有效跟踪分支并事务重绑存量文档与漂移状态（PUT /api/repos/:repo_id/branch，仅仓库级写者/管理员）。
func (h *AppHandlers) SwitchRepoBranch(c *gin.Context) {
	repoID, ok := parseRepoIDParam(c)
	if !ok {
		return
	}
	user, _, allowed := h.CheckScopeWriteOrAbort(c, repoID, "")
	if !allowed {
		return
	}

	var req services.SwitchRepoBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.branchSvc.SwitchRepoBranch(c.Request.Context(), repoID, req, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidBranch), errors.Is(err, services.ErrBranchNotFound):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
		case errors.Is(err, services.ErrActiveTasksBlockingBranchSwitch):
			WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
		case errors.Is(err, models.ErrMigrationConflict):
			WriteError(c, http.StatusConflict, ErrCodeMigrationConflict, "仓库存在跨分支同名文档冲突，请清理后再切换", nil)
		case errors.Is(err, services.ErrRemoteBranchFetchFailed):
			WriteError(c, http.StatusBadGateway, ErrCodeRemoteBranchFetchFailed, err.Error(), nil)
		default:
			log.Printf("[%s] 切换仓库目标分支失败 (repo_id=%d branch=%q): %v", config.ServiceName, repoID, req.Branch, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "切换仓库目标分支失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"switch_branch",
		commonModels.AuditLevelP1,
		fmt.Sprintf("切换仓库 #%d 目标跟踪分支: %s -> %s（重绑 %d 篇文档，漂移 %d 篇）", repoID, vo.PreviousBranch, vo.CurrentBranch, vo.ReboundDocuments, vo.DriftedDocuments),
		"repo_branch",
		strconv.FormatUint(uint64(repoID), 10),
		fmt.Sprintf("repo#%d:%s", repoID, vo.CurrentBranch),
		map[string]string{"branch": vo.PreviousBranch},
		vo,
	)

	c.JSON(http.StatusOK, vo)
}
