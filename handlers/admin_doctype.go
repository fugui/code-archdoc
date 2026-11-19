package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// CreateDocType 管理员在线新建 DocType（POST /api/admin/doc-types，P0 审计）。
func (h *AppHandlers) CreateDocType(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	var req services.CreateDocTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	detail, afterAudit, err := h.docTypeSvc.Create(c.Request.Context(), req, user.ID)
	if err != nil {
		writeDocTypeServiceError(c, "新建 DocType", req.Slug, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"create",
		commonModels.AuditLevelP0,
		fmt.Sprintf("新建文档类型 %s (%s)", detail.Slug, detail.DisplayName),
		"doc_type",
		detail.Slug,
		detail.DisplayName,
		nil,
		afterAudit,
	)

	c.JSON(http.StatusCreated, detail)
}

// CloneDocType 管理员克隆既有 DocType（POST /api/admin/doc-types/:slug/clone，P0 审计）。
func (h *AppHandlers) CloneDocType(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	srcSlug := c.Param("slug")
	var req services.CloneDocTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	detail, afterAudit, err := h.docTypeSvc.Clone(c.Request.Context(), srcSlug, req, user.ID)
	if err != nil {
		writeDocTypeServiceError(c, "克隆 DocType", srcSlug, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"create",
		commonModels.AuditLevelP0,
		fmt.Sprintf("从 %s 克隆文档类型 %s (%s)", srcSlug, detail.Slug, detail.DisplayName),
		"doc_type",
		detail.Slug,
		detail.DisplayName,
		nil,
		afterAudit,
	)

	c.JSON(http.StatusCreated, detail)
}

// UpdateDocType 管理员在线更新 DocType 元数据与模板指令（PUT /api/admin/doc-types/:slug，P0 审计）。
func (h *AppHandlers) UpdateDocType(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	slug := c.Param("slug")
	var req services.UpdateDocTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	updated, beforeAudit, afterAudit, err := h.docTypeSvc.Update(c.Request.Context(), slug, req, user.ID)
	if err != nil {
		writeDocTypeServiceError(c, "更新 DocType", slug, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP0,
		fmt.Sprintf("更新文档类型 %s (rev=%d, new_rev=%v)", updated.Slug, updated.LatestRevisionNo, updated.RevisionCreated),
		"doc_type",
		updated.Slug,
		updated.DisplayName,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, updated)
}

// ToggleDocTypeStatus 管理员启用或停用 DocType（PATCH /api/admin/doc-types/:slug/status，P0 审计）。
func (h *AppHandlers) ToggleDocTypeStatus(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	slug := c.Param("slug")
	var req services.ToggleDocTypeStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	detail, beforeAudit, afterAudit, err := h.docTypeSvc.UpdateStatus(c.Request.Context(), slug, req.IsActive, user.ID)
	if err != nil {
		writeDocTypeServiceError(c, "切换 DocType 启停状态", slug, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP0,
		fmt.Sprintf("切换文档类型 %s 启用状态为 %v", detail.Slug, detail.IsActive),
		"doc_type",
		detail.Slug,
		detail.DisplayName,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, detail)
}

// DeleteDocType 管理员物理删除未被已发布文档引用的 DocType（DELETE /api/admin/doc-types/:slug，P0 审计）。
func (h *AppHandlers) DeleteDocType(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	slug := c.Param("slug")
	beforeAudit, err := h.docTypeSvc.Delete(c.Request.Context(), slug)
	if err != nil {
		writeDocTypeServiceError(c, "删除 DocType", slug, err)
		return
	}

	targetName := slug
	if beforeAudit != nil && beforeAudit.DisplayName != "" {
		targetName = beforeAudit.DisplayName
	}
	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"delete",
		commonModels.AuditLevelP0,
		fmt.Sprintf("物理删除文档类型 %s", slug),
		"doc_type",
		slug,
		targetName,
		beforeAudit,
		nil,
	)

	c.JSON(http.StatusOK, gin.H{
		"deleted": true,
		"slug":    slug,
	})
}

// SyncDocTypes 管理员手动触发目录 -> DB 全量幂等同步（POST /api/admin/doc-types/sync，P2 审计）。
func (h *AppHandlers) SyncDocTypes(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	op := user.ID
	res, err := h.docTypeSvc.SyncAll(c.Request.Context(), &op)
	if err != nil {
		log.Printf("[%s] 手动同步 DocType 目录失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "同步文档类型目录失败", nil)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"sync",
		commonModels.AuditLevelP2,
		fmt.Sprintf("手动全量同步 DocType 目录 (synced=%d, new_revisions=%d)", res.SyncedCount, res.NewRevisions),
		"doc_type",
		"all",
		"doc_types",
		nil,
		res,
	)

	c.JSON(http.StatusOK, res)
}

func writeDocTypeServiceError(c *gin.Context, action, slug string, err error) {
	if errors.Is(err, doctype.ErrInvalidSlug) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidSlug, err.Error(), nil)
		return
	}
	if errors.Is(err, doctype.ErrInvalidMeta) || errors.Is(err, doctype.ErrInvalidTemplate) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		return
	}
	if errors.Is(err, doctype.ErrDocTypeExists) {
		WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
		return
	}
	var inUseErr *services.DocTypeInUseError
	if errors.As(err, &inUseErr) {
		WriteError(c, http.StatusConflict, ErrCodeDocTypeInUse, inUseErr.Error(), gin.H{
			"published_doc_count": inUseErr.PublishedDocCount,
		})
		return
	}
	if errors.Is(err, models.ErrDocTypeInUse) {
		WriteError(c, http.StatusConflict, ErrCodeDocTypeInUse, "目标文档类型已被已发布文档引用，禁止物理删除", nil)
		return
	}
	if errors.Is(err, models.ErrNotFound) || errors.Is(err, doctype.ErrDocTypeNotFound) {
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档类型不存在", nil)
		return
	}

	log.Printf("[%s] %s 失败 (slug=%s): %v", config.ServiceName, action, slug, err)
	WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, action+"失败", nil)
}
