package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/services"

	"github.com/gin-gonic/gin"
)

// ListReviewComments 获取文档审阅批注列表（GET /api/documents/:id/review-comments，需 ScopeWrite）。
func (h *AppHandlers) ListReviewComments(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	var persistFilter *bool
	if rawPersist := strings.TrimSpace(c.Query("persist_for_regen")); rawPersist != "" {
		val, err := strconv.ParseBool(rawPersist)
		if err != nil {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "persist_for_regen 必须为布尔值", nil)
			return
		}
		persistFilter = &val
	}

	items, err := h.reviewSvc.ListComments(c.Request.Context(), doc.ID, models.ReviewCommentFilter{
		Status:          strings.TrimSpace(c.Query("status")),
		ScopeType:       strings.TrimSpace(c.Query("scope_type")),
		SlotID:          strings.TrimSpace(c.Query("slot_id")),
		PersistForRegen: persistFilter,
	})
	if err != nil {
		writeReviewServiceError(c, err, "查询审阅批注失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// CreateReviewComment 新建划词、块级、章节或全局审阅批注（POST /api/documents/:id/review-comments，需 ScopeWrite）。
func (h *AppHandlers) CreateReviewComment(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	var req services.CreateReviewCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.reviewSvc.CreateComment(c.Request.Context(), doc.ID, req, user.ID)
	if err != nil {
		writeReviewServiceError(c, err, "创建审阅批注失败")
		return
	}
	c.JSON(http.StatusCreated, vo)
}

// UpdateReviewComment 更新批注内容、语义分类、持久记忆开关或状态（PATCH /api/documents/:id/review-comments/:comment_id，需 ScopeWrite）。
func (h *AppHandlers) UpdateReviewComment(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	commentIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Param("comment_id")), 10, 64)
	if err != nil || commentIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "comment_id 必须为正整数", nil)
		return
	}

	var req services.UpdateReviewCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.reviewSvc.UpdateComment(c.Request.Context(), doc.ID, uint(commentIDVal), req)
	if err != nil {
		writeReviewServiceError(c, err, "更新审阅批注失败")
		return
	}
	c.JSON(http.StatusOK, vo)
}

// DeleteReviewComment 物理删除未关联已采纳修订的草稿批注（DELETE /api/documents/:id/review-comments/:comment_id，需 ScopeWrite）。
func (h *AppHandlers) DeleteReviewComment(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	commentIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Param("comment_id")), 10, 64)
	if err != nil || commentIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "comment_id 必须为正整数", nil)
		return
	}

	if err := h.reviewSvc.DeleteComment(c.Request.Context(), doc.ID, uint(commentIDVal)); err != nil {
		writeReviewServiceError(c, err, "删除审阅批注失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": uint(commentIDVal)})
}

func writeReviewServiceError(c *gin.Context, err error, fallbackMsg string) {
	switch {
	case errors.Is(err, services.ErrInvalidReviewParam):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
	case errors.Is(err, models.ErrReviewCommentConflict):
		WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
	case errors.Is(err, models.ErrNotFound):
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档或审阅批注不存在", nil)
	default:
		log.Printf("[%s] %s: %v", config.ServiceName, fallbackMsg, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, fallbackMsg, nil)
	}
}
