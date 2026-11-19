package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"

	"github.com/gin-gonic/gin"
)

// ListDocTypes 获取 DocType 列表（GET /api/doc-types?active_only=true|false）。
func (h *AppHandlers) ListDocTypes(c *gin.Context) {
	activeOnly := strings.EqualFold(strings.TrimSpace(c.Query("active_only")), "true")
	items, err := h.docTypeSvc.List(c.Request.Context(), activeOnly)
	if err != nil {
		log.Printf("[%s] 查询 DocType 列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询文档类型列表失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetDocType 获取单个 DocType 详情与当前修订资产（GET /api/doc-types/:slug）。
func (h *AppHandlers) GetDocType(c *gin.Context) {
	slug := c.Param("slug")
	detail, err := h.docTypeSvc.GetDetail(c.Request.Context(), slug)
	if err != nil {
		if errors.Is(err, doctype.ErrInvalidSlug) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSlug, err.Error(), nil)
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档类型不存在", nil)
			return
		}
		log.Printf("[%s] 查询 DocType 详情失败 (slug=%s): %v", config.ServiceName, slug, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询文档类型详情失败", nil)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// ListDocTypeRevisions 查询 DocType 不可变修订历史（GET /api/doc-types/:slug/revisions）。
func (h *AppHandlers) ListDocTypeRevisions(c *gin.Context) {
	slug := c.Param("slug")
	items, err := h.docTypeSvc.ListRevisions(c.Request.Context(), slug)
	if err != nil {
		if errors.Is(err, doctype.ErrInvalidSlug) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSlug, err.Error(), nil)
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档类型不存在", nil)
			return
		}
		log.Printf("[%s] 查询 DocType 修订历史失败 (slug=%s): %v", config.ServiceName, slug, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询文档类型修订历史失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
