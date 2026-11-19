package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/publisher"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// PublishDocument 审批定稿并原子落盘至 publish_dir（POST /api/documents/:id/publish）。
// 若 Verifier 门禁未通过（如命中明文凭据泄漏），硬拦截返回 422 VERIFIER_BLOCKED（SC-04）。
func (h *AppHandlers) PublishDocument(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	var body struct {
		RevisionNo *int `json:"revision_no"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	beforeState := gin.H{
		"status":                doc.Status,
		"published_revision_id": doc.PublishedRevisionID,
		"published_rel_path":    doc.PublishedRelPath,
		"published_commit_sha":  doc.PublishedCommitSHA,
	}

	res, err := h.publishSvc.PublishDocument(c.Request.Context(), doc.ID, body.RevisionNo, user)
	if err != nil {
		var blockedErr *services.VerifierBlockedError
		if errors.As(err, &blockedErr) {
			WriteError(c, http.StatusUnprocessableEntity, ErrCodeVerifierBlocked, blockedErr.Error(), gin.H{
				"verifier_report": blockedErr.Report,
			})
			return
		}
		h.writeRevisionError(c, err, "定稿发布文档失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"publish",
		commonModels.AuditLevelP0,
		fmt.Sprintf("审批发布文档 #%d (%s v%d -> %s)", doc.ID, doc.DocTypeSlug, res.PublishedRevisionNo, res.PublishedRelPath),
		"document",
		strconv.FormatUint(uint64(doc.ID), 10),
		doc.Title,
		beforeState,
		gin.H{
			"status":                res.Status,
			"published_revision_no": res.PublishedRevisionNo,
			"published_rel_path":    res.PublishedRelPath,
			"commit_sha":            res.PublishedCommitSHA,
		},
	)

	c.JSON(http.StatusOK, res)
}

// UnpublishDocument 一键撤回已发布文档，清理落盘文件与空目录并退回 draft（POST /api/documents/:id/unpublish）。
func (h *AppHandlers) UnpublishDocument(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	beforeState := gin.H{
		"status":                doc.Status,
		"published_revision_id": doc.PublishedRevisionID,
		"published_rel_path":    doc.PublishedRelPath,
		"published_commit_sha":  doc.PublishedCommitSHA,
	}

	res, err := h.publishSvc.UnpublishDocument(c.Request.Context(), doc.ID)
	if err != nil {
		h.writeRevisionError(c, err, "撤回已发布文档失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"unpublish",
		commonModels.AuditLevelP0,
		fmt.Sprintf("一键撤回已发布文档 #%d (%s)", doc.ID, res.RemovedRelPath),
		"document",
		strconv.FormatUint(uint64(doc.ID), 10),
		doc.Title,
		beforeState,
		gin.H{
			"status":           res.Status,
			"removed_rel_path": res.RemovedRelPath,
		},
	)

	c.JSON(http.StatusOK, res)
}

// ExportDocument 流式导出单篇带 YAML Frontmatter 与平台深链的 Markdown 文档（GET /api/documents/:id/export）。
func (h *AppHandlers) ExportDocument(c *gin.Context) {
	doc, _, canWrite, ok := h.resolveDocumentAccess(c, false, false)
	if !ok {
		return
	}

	filename, content, err := h.publishSvc.ExportSingleDocument(c.Request.Context(), doc.ID, canWrite)
	if err != nil {
		h.writeRevisionError(c, err, "导出 Markdown 文档失败")
		return
	}

	safeFilename := sanitizeAttachmentFilename(filename)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeFilename))
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", content)
}

// ExportZip 批量打包下载多篇已发布文档 ZIP（POST /api/exports/zip，含 INDEX.md 并防 Zip-Slip）。
func (h *AppHandlers) ExportZip(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	var req services.ExportZipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	filename, data, err := h.publishSvc.ExportZipArchive(c.Request.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidRevisionParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "未找到符合条件的已发布文档", nil)
		case errors.Is(err, publisher.ErrUnsafeZipPath):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		default:
			log.Printf("[%s] 批量导出 ZIP 失败: %v", config.ServiceName, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "批量导出 ZIP 失败", nil)
		}
		return
	}

	safeFilename := sanitizeAttachmentFilename(filename)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeFilename))
	c.Data(http.StatusOK, "application/zip", data)
}

func sanitizeAttachmentFilename(name string) string {
	cleaned := strings.ReplaceAll(name, `"`, "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")
	cleaned = strings.ReplaceAll(cleaned, "\n", "")
	cleaned = strings.ReplaceAll(cleaned, "/", "-")
	cleaned = strings.ReplaceAll(cleaned, `\`, "-")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "document.md"
	}
	return cleaned
}
