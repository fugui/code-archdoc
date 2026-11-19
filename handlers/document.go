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
	"code-archdoc/pkg/sandbox"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// ListDocuments 查询指定范围 (repo_id, subdirectory, ref_name) 下的文档导航树（GET /api/documents）。
// 普通登录用户自动过滤未发布草稿，仅可见 status=="approved" 的已发布文档。
func (h *AppHandlers) ListDocuments(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	repoIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Query("repo_id")), 10, 64)
	if err != nil || repoIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "repo_id 必须为正整数", nil)
		return
	}
	repoID := uint(repoIDVal)

	normSubdir, normErr := sandbox.NormalizeSubdir(c.Query("subdirectory"))
	if normErr != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, normErr.Error(), nil)
		return
	}
	refName := strings.TrimSpace(c.Query("ref_name"))

	canWrite, permErr := h.rbacSvc.CheckScopeWritePermission(c.Request.Context(), user, repoID, normSubdir)
	if permErr != nil {
		if errors.Is(permErr, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return
		}
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "权限校验失败", nil)
		return
	}

	resp, err := h.revSvc.ListDocumentsByScope(c.Request.Context(), repoID, normSubdir, refName, canWrite)
	if err != nil {
		log.Printf("[%s] 查询文档列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询文档列表失败", nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetDocument 获取单篇文档完整章节、证据锚点、Verifier 报告与漂移状态（GET /api/documents/:id）。
// 普通用户访问未发布草稿返回 403 DRAFT_ACCESS_DENIED，访问已发布文档强制只看 published_revision_id 快照。
func (h *AppHandlers) GetDocument(c *gin.Context) {
	doc, _, canWrite, ok := h.resolveDocumentAccess(c, false, false)
	if !ok {
		return
	}

	var reqRevNo *int
	if rawRev := strings.TrimSpace(c.Query("revision_no")); rawRev != "" {
		v, pErr := strconv.Atoi(rawRev)
		if pErr != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "revision_no 必须为正整数", nil)
			return
		}
		reqRevNo = &v
	}

	vo, err := h.revSvc.GetDocumentDetail(c.Request.Context(), doc.ID, reqRevNo, canWrite)
	if err != nil {
		h.writeRevisionError(c, err, "获取文档详情失败")
		return
	}
	c.JSON(http.StatusOK, vo)
}

// ListDocumentRevisions 获取文档不可变修订时间线（GET /api/documents/:id/revisions，仅 ScopeWrite 可访问）。
func (h *AppHandlers) ListDocumentRevisions(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, true)
	if !ok {
		return
	}

	items, err := h.revSvc.ListDocumentRevisions(c.Request.Context(), doc.ID)
	if err != nil {
		h.writeRevisionError(c, err, "获取文档修订历史失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// DiffDocumentRevisions 对比任意两个修订版本的章节级 Unified Diff（GET /api/documents/:id/revisions/diff，仅 ScopeWrite 可访问）。
func (h *AppHandlers) DiffDocumentRevisions(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, true)
	if !ok {
		return
	}

	fromRev, err1 := strconv.Atoi(strings.TrimSpace(c.Query("from_rev")))
	toRev, err2 := strconv.Atoi(strings.TrimSpace(c.Query("to_rev")))
	if err1 != nil || err2 != nil || fromRev <= 0 || toRev <= 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "from_rev 与 to_rev 必须为正整数", nil)
		return
	}

	vo, err := h.revSvc.GetRevisionDiff(c.Request.Context(), doc.ID, fromRev, toRev)
	if err != nil {
		h.writeRevisionError(c, err, "计算修订版本差异失败")
		return
	}
	c.JSON(http.StatusOK, vo)
}

// UpdateDocumentSection 手工直编单个章节（PUT /api/documents/:id/sections/:slot_id，乐观锁 + 重跑 Verifier + 不可变修订）。
func (h *AppHandlers) UpdateDocumentSection(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}
	slotID := strings.TrimSpace(c.Param("slot_id"))
	if slotID == "" {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "slot_id 不能为空", nil)
		return
	}

	var body struct {
		BaseRevisionNo int    `json:"base_revision_no"`
		Markdown       string `json:"markdown"`
		Ownership      string `json:"ownership"`
		ChangeSummary  string `json:"change_summary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	// 记录编辑前哈希摘要供字段级 Diff 审计（避免超大正文撑爆审计表）
	beforeMeta := gin.H{
		"doc_id":      doc.ID,
		"slot_id":     slotID,
		"revision_no": doc.CurrentRevisionNo,
	}
	if beforeDetail, bErr := h.revSvc.GetDocumentDetail(c.Request.Context(), doc.ID, nil, true); bErr == nil {
		for _, sec := range beforeDetail.Sections {
			if sec.SlotID == slotID {
				beforeMeta["before_hash"] = sec.ContentSHA256
				beforeMeta["ownership"] = sec.Ownership
				break
			}
		}
	}

	vo, err := h.revSvc.ApplySectionChange(c.Request.Context(), services.ApplySectionChangeInput{
		DocID:          doc.ID,
		SlotID:         slotID,
		BaseRevisionNo: body.BaseRevisionNo,
		Markdown:       body.Markdown,
		Ownership:      body.Ownership,
		ChangeSummary:  body.ChangeSummary,
		Source:         models.RevSourceManualEdit,
		OperatorID:     user.ID,
	})
	if err != nil {
		h.writeRevisionError(c, err, "保存章节修订失败")
		return
	}

	afterMeta := gin.H{
		"doc_id":         doc.ID,
		"slot_id":        slotID,
		"revision_no":    vo.CurrentRevisionNo,
		"change_summary": vo.ViewingRevision.ChangeSummary,
	}
	for _, sec := range vo.Sections {
		if sec.SlotID == slotID {
			afterMeta["after_hash"] = sec.ContentSHA256
			afterMeta["ownership"] = sec.Ownership
			break
		}
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("手工编辑文档 #%d 章节 %s（v%d -> v%d）", doc.ID, slotID, body.BaseRevisionNo, vo.CurrentRevisionNo),
		"document_section",
		fmt.Sprintf("%d:%s", doc.ID, slotID),
		fmt.Sprintf("doc#%d/%s", doc.ID, slotID),
		beforeMeta,
		afterMeta,
	)

	c.JSON(http.StatusOK, vo)
}

// UpdateSectionOwnership 切换章节所有权（auto/hybrid/locked）或清除待核对标记（PATCH /api/documents/:id/sections/:slot_id/ownership）。
func (h *AppHandlers) UpdateSectionOwnership(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}
	slotID := strings.TrimSpace(c.Param("slot_id"))
	if slotID == "" {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "slot_id 不能为空", nil)
		return
	}

	var body struct {
		Ownership        string `json:"ownership"`
		ClearDriftReview bool   `json:"clear_drift_review"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	var beforeOwnership string
	if beforeDetail, bErr := h.revSvc.GetDocumentDetail(c.Request.Context(), doc.ID, nil, true); bErr == nil {
		for _, sec := range beforeDetail.Sections {
			if sec.SlotID == slotID {
				beforeOwnership = sec.Ownership
				break
			}
		}
	}

	secVO, err := h.revSvc.UpdateSectionOwnership(c.Request.Context(), doc.ID, slotID, body.Ownership, body.ClearDriftReview)
	if err != nil {
		h.writeRevisionError(c, err, "更新章节所有权失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("切换文档 #%d 章节 %s 所有权：%s -> %s", doc.ID, slotID, beforeOwnership, secVO.Ownership),
		"document_section",
		fmt.Sprintf("%d:%s", doc.ID, slotID),
		fmt.Sprintf("doc#%d/%s", doc.ID, slotID),
		gin.H{"slot_id": slotID, "ownership": beforeOwnership},
		gin.H{"slot_id": slotID, "ownership": secVO.Ownership, "needs_drift_review": secVO.NeedsDriftReview},
	)

	c.JSON(http.StatusOK, secVO)
}

// ResolveOrphanSection 处置模板升级产生的孤立章节（POST /api/documents/:id/sections/:slot_id/orphan-resolve）。
func (h *AppHandlers) ResolveOrphanSection(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}
	slotID := strings.TrimSpace(c.Param("slot_id"))
	if slotID == "" {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "slot_id 不能为空", nil)
		return
	}

	var body struct {
		BaseRevisionNo int    `json:"base_revision_no"`
		Action         string `json:"action"`
		TargetSlotID   string `json:"target_slot_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.revSvc.ResolveOrphanSection(c.Request.Context(), services.ResolveOrphanInput{
		DocID:          doc.ID,
		SlotID:         slotID,
		BaseRevisionNo: body.BaseRevisionNo,
		Action:         body.Action,
		TargetSlotID:   body.TargetSlotID,
		OperatorID:     user.ID,
	})
	if err != nil {
		h.writeRevisionError(c, err, "处置孤立章节失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("处置文档 #%d 孤立章节 %s（action=%s）", doc.ID, slotID, body.Action),
		"document_section",
		fmt.Sprintf("%d:%s", doc.ID, slotID),
		fmt.Sprintf("doc#%d/%s", doc.ID, slotID),
		gin.H{"slot_id": slotID, "is_orphan": true, "base_revision_no": body.BaseRevisionNo},
		gin.H{"slot_id": slotID, "action": body.Action, "target_slot_id": body.TargetSlotID, "revision_no": vo.CurrentRevisionNo},
	)

	c.JSON(http.StatusOK, vo)
}

// RollbackDocument 一键回滚文档至历史修订（POST /api/documents/:id/rollback，追加不可变修订）。
func (h *AppHandlers) RollbackDocument(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	var body struct {
		BaseRevisionNo   int    `json:"base_revision_no"`
		TargetRevisionNo int    `json:"target_revision_no"`
		ChangeSummary    string `json:"change_summary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.revSvc.RollbackRevision(c.Request.Context(), services.RollbackRevisionInput{
		DocID:            doc.ID,
		BaseRevisionNo:   body.BaseRevisionNo,
		TargetRevisionNo: body.TargetRevisionNo,
		ChangeSummary:    body.ChangeSummary,
		OperatorID:       user.ID,
	})
	if err != nil {
		h.writeRevisionError(c, err, "文档回滚失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"rollback",
		commonModels.AuditLevelP1,
		fmt.Sprintf("回滚文档 #%d：v%d -> v%d（新修订 v%d）", doc.ID, body.BaseRevisionNo, body.TargetRevisionNo, vo.CurrentRevisionNo),
		"document",
		strconv.FormatUint(uint64(doc.ID), 10),
		doc.Title,
		gin.H{"from_revision_no": body.BaseRevisionNo},
		gin.H{"to_revision_no": body.TargetRevisionNo, "new_revision_no": vo.CurrentRevisionNo},
	)

	c.JSON(http.StatusOK, vo)
}

// GetSourcePreview 只读源码证据抽屉（GET /api/documents/:id/source-preview，经 SafeJoin 严格防穿越 403 PATH_ESCAPE）。
func (h *AppHandlers) GetSourcePreview(c *gin.Context) {
	doc, _, canWrite, ok := h.resolveDocumentAccess(c, false, false)
	if !ok {
		return
	}

	fileParam := c.Query("file")
	commitParam := c.Query("commit")
	symbolParam := c.Query("symbol")

	var lineStart, lineEnd int
	if rawStart := strings.TrimSpace(c.Query("line_start")); rawStart != "" {
		v, err := strconv.Atoi(rawStart)
		if err != nil || v < 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "line_start 参数非法", nil)
			return
		}
		lineStart = v
	}
	if rawEnd := strings.TrimSpace(c.Query("line_end")); rawEnd != "" {
		v, err := strconv.Atoi(rawEnd)
		if err != nil || v < 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "line_end 参数非法", nil)
			return
		}
		lineEnd = v
	}

	vo, err := h.previewSvc.GetSourcePreview(c.Request.Context(), services.SourcePreviewRequest{
		DocID:     doc.ID,
		Commit:    commitParam,
		File:      fileParam,
		LineStart: lineStart,
		LineEnd:   lineEnd,
		Symbol:    symbolParam,
		CanWrite:  canWrite,
	})
	if err != nil {
		h.writeRevisionError(c, err, "读取源码证据预览失败")
		return
	}
	c.JSON(http.StatusOK, vo)
}

// resolveDocumentAccess 解析路径 :id、加载文档记录并判定当前用户对该文档所属范围的写权限。
// requireWrite=true 时非负责人直接拦截；useDraftDeniedCode=true 时返回 DRAFT_ACCESS_DENIED，否则返回 FORBIDDEN。
func (h *AppHandlers) resolveDocumentAccess(c *gin.Context, requireWrite bool, useDraftDeniedCode bool) (*models.Document, *commonModels.User, bool, bool) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return nil, nil, false, false
	}

	docIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || docIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "id 参数非法", nil)
		return nil, nil, false, false
	}

	doc, err := h.revSvc.GetDocumentScope(c.Request.Context(), uint(docIDVal))
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档不存在", nil)
			return nil, nil, false, false
		}
		log.Printf("[%s] 查询文档元信息失败 (doc_id=%d): %v", config.ServiceName, docIDVal, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询文档失败", nil)
		return nil, nil, false, false
	}

	canWrite, permErr := h.rbacSvc.CheckScopeWritePermission(c.Request.Context(), user, doc.RepoID, doc.Subdirectory)
	if permErr != nil {
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "权限校验失败", nil)
		return nil, nil, false, false
	}

	if requireWrite && !canWrite {
		if useDraftDeniedCode {
			WriteError(c, http.StatusForbidden, ErrCodeDraftAccessDenied, "普通用户无权访问文档草稿或修订历史", nil)
		} else {
			WriteError(c, http.StatusForbidden, ErrCodeForbidden, "无权编辑或治理该文档所属范围", nil)
		}
		return nil, nil, false, false
	}

	return doc, user, canWrite, true
}

func (h *AppHandlers) writeRevisionError(c *gin.Context, err error, fallbackMsg string) {
	var lockErr *services.OptimisticLockConflictError
	if errors.As(err, &lockErr) {
		WriteError(c, http.StatusConflict, ErrCodeOptimisticLockConflict, lockErr.Error(), gin.H{
			"current_revision_no": lockErr.CurrentRevisionNo,
		})
		return
	}
	switch {
	case errors.Is(err, models.ErrOptimisticLockConflict):
		WriteError(c, http.StatusConflict, ErrCodeOptimisticLockConflict, err.Error(), nil)
	case errors.Is(err, services.ErrDraftAccessDenied):
		WriteError(c, http.StatusForbidden, ErrCodeDraftAccessDenied, err.Error(), nil)
	case errors.Is(err, sandbox.ErrPathEscape):
		WriteError(c, http.StatusForbidden, ErrCodePathEscape, err.Error(), nil)
	case errors.Is(err, sandbox.ErrInvalidSubdir):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, err.Error(), nil)
	case errors.Is(err, services.ErrInvalidRevisionParam), errors.Is(err, services.ErrSectionNotOrphan):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
	case errors.Is(err, models.ErrNotFound):
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档、修订或源码文件不存在", nil)
	default:
		log.Printf("[%s] %s: %v", config.ServiceName, fallbackMsg, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, fallbackMsg, nil)
	}
}
