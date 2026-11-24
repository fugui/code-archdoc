package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func parseUintQueryOptional(c *gin.Context, key string) (*uint, bool) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, key+" 参数必须为正整数", nil)
		return nil, false
	}
	u := uint(v)
	return &u, true
}

func parseRefDocIDParam(c *gin.Context, param string) (uint, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(c.Param(param)), 10, 64)
	if err != nil || v == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, param+" 参数非法", nil)
		return 0, false
	}
	return uint(v), true
}

func writeRefDocServiceError(c *gin.Context, action string, err error) {
	var gateErr *services.RefDocGateError
	var verErr *services.RefDocVersionConflictError
	var budgetErr *services.RefDocTokenBudgetExceededError
	switch {
	case errors.As(err, &gateErr) && gateErr.Failure != nil:
		WriteError(c, gateErr.Failure.HTTPStatus, gateErr.Failure.Code, gateErr.Failure.Message, gateErr.Failure.Details)
	case errors.As(err, &budgetErr):
		WriteError(c, http.StatusUnprocessableEntity, ErrCodeTokenBudgetExceeded, budgetErr.Error(), gin.H{
			"estimated_tokens":       budgetErr.EstimatedTokens,
			"max_full_refine_tokens": budgetErr.MaxFullRefineTokens,
			"suggested_scope":        "section",
		})
	case errors.As(err, &verErr):
		WriteError(c, http.StatusConflict, ErrCodeVersionConflict, verErr.Error(), gin.H{
			"current_version": verErr.CurrentVersion,
		})
	case errors.Is(err, models.ErrVersionConflict):
		WriteError(c, http.StatusConflict, ErrCodeVersionConflict, err.Error(), nil)
	case errors.Is(err, models.ErrAlreadyBound):
		WriteError(c, http.StatusConflict, ErrCodeAlreadyBound, err.Error(), nil)
	case errors.Is(err, models.ErrHomeBindingProtected):
		WriteError(c, http.StatusBadRequest, ErrCodeHomeBindingProtected, err.Error(), nil)
	case errors.Is(err, sandbox.ErrInvalidSubdir):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, err.Error(), nil)
	case errors.Is(err, services.ErrChatLLMNotConfigured):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
	case errors.Is(err, services.ErrInvalidRefDocParam), errors.Is(err, services.ErrRefDocArchivedReadOnly):
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
	case errors.Is(err, services.ErrRefDocForbidden):
		WriteError(c, http.StatusForbidden, ErrCodeForbidden, err.Error(), nil)
	case errors.Is(err, models.ErrNotFound):
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标参考文档或关联作用域不存在", nil)
	default:
		log.Printf("[%s] %s失败: %v", config.ServiceName, action, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, action+"失败", nil)
	}
}

// ListRefDocs 分页检索参考文档库（GET /api/ref-docs）。
func (h *AppHandlers) ListRefDocs(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	archID, valid := parseUintQueryOptional(c, "arch_element_id")
	if !valid {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSizeRaw := strings.TrimSpace(c.Query("page_size"))
	if pageSizeRaw == "" {
		pageSizeRaw = strings.TrimSpace(c.Query("pageSize"))
	}
	pageSize, _ := strconv.Atoi(pageSizeRaw)

	resp, err := h.refDocSvc.ListRefDocs(c.Request.Context(), services.RefDocListQuery{
		Search:        c.Query("search"),
		DocKind:       c.Query("doc_kind"),
		ScopeLevel:    c.Query("scope_level"),
		ArchElementID: archID,
		Status:        c.DefaultQuery("status", models.RefDocStatusActive),
		Page:          page,
		PageSize:      pageSize,
	}, user)
	if err != nil {
		writeRefDocServiceError(c, "检索参考文档", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetEffectiveRefDocs 解析目标作用域最终生效的参考文档列表（GET /api/ref-docs/effective）。
func (h *AppHandlers) GetEffectiveRefDocs(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	archID, valid := parseUintQueryOptional(c, "arch_element_id")
	if !valid {
		return
	}
	repoID, valid := parseUintQueryOptional(c, "repo_id")
	if !valid {
		return
	}
	if archID == nil && repoID == nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "必须提供 arch_element_id 或 repo_id", nil)
		return
	}

	resp, err := h.refDocSvc.ResolveEffectiveRefDocs(c.Request.Context(), services.ResolveEffectiveRefDocsRequest{
		ArchElementID: archID,
		RepoID:        repoID,
		Subdirectory:  c.Query("subdirectory"),
		DocTypeSlug:   c.Query("doc_type_slug"),
	}, user)
	if err != nil {
		writeRefDocServiceError(c, "解析生效参考文档", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetRefDocDetail 获取参考文档详情、绑定列表及指定版本正文（GET /api/ref-docs/:id）。
func (h *AppHandlers) GetRefDocDetail(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	version := 0
	if verRaw := strings.TrimSpace(c.Query("version")); verRaw != "" {
		parsed, err := strconv.Atoi(verRaw)
		if err != nil || parsed <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "version 参数必须为正整数", nil)
			return
		}
		version = parsed
	}

	detail, err := h.refDocSvc.GetRefDocDetail(c.Request.Context(), docID, version, user)
	if err != nil {
		writeRefDocServiceError(c, "获取参考文档详情", err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// CreateRefDoc 创建参考文档并建立 is_home=true 归属绑定（POST /api/ref-docs，支持 JSON 与 multipart/form-data）。
func (h *AppHandlers) CreateRefDoc(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	var req services.CreateRefDocRequest
	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.Contains(ct, "multipart/form-data") {
		payloadRaw := strings.TrimSpace(c.PostForm("payload"))
		if payloadRaw == "" {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "multipart 上传缺少 payload JSON 字段", nil)
			return
		}
		if err := json.Unmarshal([]byte(payloadRaw), &req); err != nil {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "payload JSON 格式非法", nil)
			return
		}
		if fh, fErr := c.FormFile("file"); fErr == nil && fh != nil {
			maxAllowed := config.AppConfig.Storage.EffectiveRefDocMaxBytes()
			if fh.Size > maxAllowed+1024 {
				WriteError(c, http.StatusRequestEntityTooLarge, ErrCodePayloadTooLarge,
					fmt.Sprintf("上传文件体积 %d 字节超出上限 %d 字节", fh.Size, maxAllowed),
					gin.H{"max_bytes": maxAllowed, "actual_bytes": fh.Size})
				return
			}
			f, oErr := fh.Open()
			if oErr != nil {
				WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "读取上传文件失败", nil)
				return
			}
			defer func() { _ = f.Close() }()
			limited := io.LimitReader(f, maxAllowed+1024)
			bytesRead, rErr := io.ReadAll(limited)
			if rErr != nil {
				WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "读取上传文件内容失败", nil)
				return
			}
			req.ContentMD = string(bytesRead)
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
			return
		}
	}

	detail, afterAudit, err := h.refDocSvc.CreateRefDoc(c.Request.Context(), req, user)
	if err != nil {
		writeRefDocServiceError(c, "创建参考文档", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"create",
		commonModels.AuditLevelP1,
		fmt.Sprintf("创建参考文档 #%d %s (scope=%s)", detail.ID, detail.Title, detail.OwnerScopeKey),
		"reference_doc",
		strconv.FormatUint(uint64(detail.ID), 10),
		detail.Title,
		nil,
		afterAudit,
	)

	c.JSON(http.StatusCreated, detail)
}

// UpdateRefDoc 乐观锁更新参考文档元信息、正文或归档状态（PUT /api/ref-docs/:id）。
func (h *AppHandlers) UpdateRefDoc(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	var req services.UpdateRefDocRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	detail, beforeAudit, afterAudit, err := h.refDocSvc.UpdateRefDoc(c.Request.Context(), docID, req, user)
	if err != nil {
		writeRefDocServiceError(c, "更新参考文档", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("更新参考文档 #%d %s (v%d, status=%s)", detail.ID, detail.Title, detail.CurrentVersion, detail.Status),
		"reference_doc",
		strconv.FormatUint(uint64(detail.ID), 10),
		detail.Title,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, detail)
}

// CreateRefDocBinding 建立跨节点消费绑定（POST /api/ref-docs/:id/bindings）。
func (h *AppHandlers) CreateRefDocBinding(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	var req services.RefDocBindingInput
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, afterAudit, err := h.refDocSvc.CreateBinding(c.Request.Context(), docID, req, user)
	if err != nil {
		writeRefDocServiceError(c, "创建参考文档绑定", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"create",
		commonModels.AuditLevelP1,
		fmt.Sprintf("为参考文档 #%d 添加消费绑定 %s", docID, vo.ScopeKey),
		"reference_binding",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.ScopeKey,
		nil,
		afterAudit,
	)

	c.JSON(http.StatusCreated, vo)
}

// UpdateRefDocBinding 更新消费绑定配置（PATCH /api/ref-docs/:id/bindings/:binding_id）。
func (h *AppHandlers) UpdateRefDocBinding(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	bindingID, ok := parseRefDocIDParam(c, "binding_id")
	if !ok {
		return
	}
	var req services.UpdateRefDocBindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, beforeAudit, afterAudit, err := h.refDocSvc.UpdateBinding(c.Request.Context(), docID, bindingID, req, user)
	if err != nil {
		writeRefDocServiceError(c, "更新参考文档绑定", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP1,
		fmt.Sprintf("更新参考文档 #%d 绑定 #%d (%s)", docID, bindingID, vo.ScopeKey),
		"reference_binding",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.ScopeKey,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, vo)
}

// DeleteRefDocBinding 解除节点消费绑定（DELETE /api/ref-docs/:id/bindings/:binding_id）。
func (h *AppHandlers) DeleteRefDocBinding(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	bindingID, ok := parseRefDocIDParam(c, "binding_id")
	if !ok {
		return
	}

	beforeAudit, err := h.refDocSvc.DeleteBinding(c.Request.Context(), docID, bindingID, user)
	if err != nil {
		writeRefDocServiceError(c, "解绑参考文档", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"delete",
		commonModels.AuditLevelP1,
		fmt.Sprintf("解除参考文档 #%d 绑定 #%d (%s)", docID, bindingID, beforeAudit.ScopeKey),
		"reference_binding",
		strconv.FormatUint(uint64(bindingID), 10),
		beforeAudit.ScopeKey,
		beforeAudit,
		nil,
	)

	c.JSON(http.StatusOK, gin.H{"deleted": true, "binding_id": bindingID})
}

// PurgeRefDoc 管理员物理彻底清除参考文档磁盘版本与数据库记录（DELETE /api/admin/ref-docs/:id/purge）。
func (h *AppHandlers) PurgeRefDoc(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}

	beforeAudit, err := h.refDocSvc.PurgeRefDoc(c.Request.Context(), docID)
	if err != nil {
		writeRefDocServiceError(c, "物理清除参考文档", err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"purge",
		commonModels.AuditLevelP0,
		fmt.Sprintf("管理员物理彻底清除参考文档 #%d %s (sha256=%s)", beforeAudit.DocID, beforeAudit.Title, beforeAudit.ContentSHA256),
		"reference_doc",
		strconv.FormatUint(uint64(docID), 10),
		beforeAudit.Title,
		beforeAudit,
		nil,
	)

	c.JSON(http.StatusOK, gin.H{"purged_id": docID})
}

// RefineRefDoc 执行参考文档无状态 AI 精修提案或问答（POST /api/ref-docs/:id/refine）。
func (h *AppHandlers) RefineRefDoc(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}
	var req services.RefineRefDocRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	resp, err := h.refDocSvc.RefineRefDoc(c.Request.Context(), docID, req, user)
	if err != nil {
		writeRefDocServiceError(c, "参考文档 AI 精修", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ListRefDocVersions 倒序枚举参考文档历史与当前磁盘版本状态（GET /api/ref-docs/:id/versions）。
func (h *AppHandlers) ListRefDocVersions(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}

	resp, err := h.refDocSvc.ListRefDocVersions(c.Request.Context(), docID)
	if err != nil {
		writeRefDocServiceError(c, "获取参考文档版本列表", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetRefDocDiff 计算任意两版本参考文档的 Unified Diff（GET /api/ref-docs/:id/diff）。
func (h *AppHandlers) GetRefDocDiff(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}
	docID, ok := parseRefDocIDParam(c, "id")
	if !ok {
		return
	}

	fromVer := 0
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "from 参数必须为正整数", nil)
			return
		}
		fromVer = v
	}

	toVer := 0
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "to 参数必须为正整数", nil)
			return
		}
		toVer = v
	}

	resp, err := h.refDocSvc.DiffRefDocVersions(c.Request.Context(), docID, fromVer, toVer)
	if err != nil {
		writeRefDocServiceError(c, "计算参考文档版本差异", err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
