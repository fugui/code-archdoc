package handlers

import (
	"encoding/json"
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

// ListChatSessions 获取文档精修会话列表与消息/提案历史（GET /api/documents/:id/chat/sessions，需 ScopeWrite）。
func (h *AppHandlers) ListChatSessions(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	items, err := h.chatSvc.ListSessions(c.Request.Context(), doc.ID)
	if err != nil {
		log.Printf("[%s] 查询 Chat 会话历史失败 (doc_id=%d): %v", config.ServiceName, doc.ID, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询精修会话历史失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// StreamChatRefine 发起章节 SSE 流式精修（POST /api/documents/:id/chat/refine，需 ScopeWrite）。
// 前置校验（锁定槽位 409、乐观锁 409、参数非法 400）在写入 SSE 响应头前同步返回 JSON 错误；
// 流式启动后按序推送 meta -> delta* -> diff_proposal -> verifier_preview -> done（或 error），支持客户端 AbortController 秒级取消上游 LLM。
func (h *AppHandlers) StreamChatRefine(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	var req services.ChatRefineRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	prep, err := h.chatSvc.PrepareRefine(c.Request.Context(), doc.ID, req, user.ID)
	if err != nil {
		writeChatServiceError(c, err, "初始化 SSE 局部精修失败")
		return
	}

	// 设置标准 SSE 响应头并禁用反代缓冲
	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	emit := func(event string, payload any) error {
		if c.Request.Context().Err() != nil {
			return c.Request.Context().Err()
		}
		dataBytes, mErr := json.Marshal(payload)
		if mErr != nil {
			return mErr
		}
		if _, wErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(dataBytes)); wErr != nil {
			return wErr
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}

	h.chatSvc.ExecuteRefineStream(c.Request.Context(), prep, emit)
}

// AcceptChatProposal 采纳 Chat Diff 提案（POST /api/documents/:id/chat/messages/:msg_id/accept，需 ScopeWrite + P1 审计）。
func (h *AppHandlers) AcceptChatProposal(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	msgIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Param("msg_id")), 10, 64)
	if err != nil || msgIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "msg_id 必须为正整数", nil)
		return
	}

	var req services.AcceptChatProposalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	detail, auditSnap, err := h.chatSvc.AcceptProposal(c.Request.Context(), doc.ID, uint(msgIDVal), req.BaseRevisionNo, user.ID)
	if err != nil {
		writeChatServiceError(c, err, "采纳 Chat 精修提案失败")
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("采纳 Chat 精修提案 #%d 更新章节 %s (rev v%d -> v%d)", auditSnap.MessageID, auditSnap.SlotID, auditSnap.BaseRevisionNo, auditSnap.NewRevisionNo),
		"document_section",
		fmt.Sprintf("%d:%s", doc.ID, auditSnap.SlotID),
		auditSnap.SlotID,
		map[string]any{"revision_no": auditSnap.BaseRevisionNo, "content_sha256": auditSnap.BeforeHash},
		map[string]any{"revision_no": auditSnap.NewRevisionNo, "content_sha256": auditSnap.AfterHash, "message_id": auditSnap.MessageID},
	)

	c.JSON(http.StatusOK, detail)
}

// RejectChatProposal 拒绝 Chat Diff 提案（POST /api/documents/:id/chat/messages/:msg_id/reject，需 ScopeWrite）。
func (h *AppHandlers) RejectChatProposal(c *gin.Context) {
	doc, _, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}

	msgIDVal, err := strconv.ParseUint(strings.TrimSpace(c.Param("msg_id")), 10, 64)
	if err != nil || msgIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "msg_id 必须为正整数", nil)
		return
	}

	res, err := h.chatSvc.RejectProposal(c.Request.Context(), doc.ID, uint(msgIDVal))
	if err != nil {
		writeChatServiceError(c, err, "拒绝 Chat 精修提案失败")
		return
	}
	c.JSON(http.StatusOK, res)
}

func writeChatServiceError(c *gin.Context, err error, fallbackMsg string) {
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
	case errors.Is(err, services.ErrSlotLocked), errors.Is(err, services.ErrProposalNotPending):
		WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
	case errors.Is(err, services.ErrInvalidChatParam), errors.Is(err, services.ErrChatLLMNotConfigured), errors.Is(err, services.ErrInvalidRevisionParam):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
	case errors.Is(err, models.ErrNotFound):
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档、章节槽位或 Chat 提案不存在", nil)
	default:
		log.Printf("[%s] %s: %v", config.ServiceName, fallbackMsg, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, fallbackMsg, nil)
	}
}
