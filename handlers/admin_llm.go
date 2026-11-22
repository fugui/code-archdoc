package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

func parseLLMConfigID(c *gin.Context) (uint, bool) {
	raw := c.Param("id")
	id64, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id64 == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "模型配置 ID 参数非法", nil)
		return 0, false
	}
	return uint(id64), true
}

func writeLLMConfigError(c *gin.Context, action string, id uint, err error) {
	if errors.Is(err, services.ErrInvalidLLMParam) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		return
	}
	if errors.Is(err, services.ErrLLMConfigConflict) {
		WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
		return
	}
	if errors.Is(err, models.ErrNotFound) {
		WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标模型配置不存在", nil)
		return
	}
	log.Printf("[%s] %s失败 (id=%d): %v", config.ServiceName, action, id, err)
	WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, action+"失败", nil)
}

// ListLLMConfigs 获取平等 LLM 模型池列表（GET /api/admin/llm-configs，强制脱敏）。
func (h *AppHandlers) ListLLMConfigs(c *gin.Context) {
	items, err := h.llmSvc.ListConfigs(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 查询 LLM 配置列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询模型配置失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
	})
}

// CreateLLMConfig 新增内网 OpenAI Compatible 模型配置（POST /api/admin/llm-configs）。
func (h *AppHandlers) CreateLLMConfig(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	var req services.LLMConfigUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, afterAudit, err := h.llmSvc.CreateConfig(c.Request.Context(), req, user.ID)
	if err != nil {
		writeLLMConfigError(c, "新增模型配置", 0, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"create",
		commonModels.AuditLevelP0,
		fmt.Sprintf("新增 LLM 模型配置 %s (model=%s, active=%v, default=%v)", vo.Name, vo.ModelName, vo.IsActive, vo.IsDefault),
		"llm_config",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.Name,
		nil,
		afterAudit,
	)

	c.JSON(http.StatusCreated, vo)
}

// UpdateLLMConfig 更新指定模型配置与单任务保护上限（PUT /api/admin/llm-configs/:id）。
func (h *AppHandlers) UpdateLLMConfig(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	id, ok := parseLLMConfigID(c)
	if !ok {
		return
	}

	var req services.LLMConfigUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, beforeAudit, afterAudit, err := h.llmSvc.UpdateConfig(c.Request.Context(), id, req, user.ID)
	if err != nil {
		writeLLMConfigError(c, "更新模型配置", id, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP0,
		fmt.Sprintf("更新 LLM 模型配置 %s (model=%s, active=%v, default=%v)", vo.Name, vo.ModelName, vo.IsActive, vo.IsDefault),
		"llm_config",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.Name,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, vo)
}

// DeleteLLMConfig 删除非唯一默认且未被 DocType 显式引用的模型配置（DELETE /api/admin/llm-configs/:id）。
func (h *AppHandlers) DeleteLLMConfig(c *gin.Context) {
	if _, ok := CurrentUserFromContext(c); !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	id, ok := parseLLMConfigID(c)
	if !ok {
		return
	}

	beforeAudit, err := h.llmSvc.DeleteConfig(c.Request.Context(), id)
	if err != nil {
		writeLLMConfigError(c, "删除模型配置", id, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"delete",
		commonModels.AuditLevelP0,
		fmt.Sprintf("删除 LLM 模型配置 %s (model=%s)", beforeAudit.Name, beforeAudit.ModelName),
		"llm_config",
		strconv.FormatUint(uint64(id), 10),
		beforeAudit.Name,
		beforeAudit,
		nil,
	)

	c.JSON(http.StatusOK, gin.H{
		"deleted": true,
		"id":      id,
	})
}

// SetDefaultLLMConfig 将指定已启用模型切换为全局默认模型（POST /api/admin/llm-configs/:id/default）。
func (h *AppHandlers) SetDefaultLLMConfig(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	id, ok := parseLLMConfigID(c)
	if !ok {
		return
	}

	vo, beforeAudit, afterAudit, err := h.llmSvc.SetDefaultConfig(c.Request.Context(), id, user.ID)
	if err != nil {
		writeLLMConfigError(c, "切换默认模型", id, err)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP0,
		fmt.Sprintf("切换系统默认 LLM 模型为 %s (model=%s)", vo.Name, vo.ModelName),
		"llm_config",
		strconv.FormatUint(uint64(vo.ID), 10),
		vo.Name,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, vo)
}

// ProbeLLMConfig 执行已保存模型的连通性探活（POST /api/admin/llm-configs/:id/probe）。
func (h *AppHandlers) ProbeLLMConfig(c *gin.Context) {
	id, ok := parseLLMConfigID(c)
	if !ok {
		return
	}

	var override *services.ProbeLLMConfigRequest
	if c.Request.Body != nil {
		var body services.ProbeLLMConfigRequest
		if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "探活请求体 JSON 格式非法", nil)
			return
		} else if err == nil {
			override = &body
		}
	}

	res, err := h.llmSvc.ProbeConfig(c.Request.Context(), id, override)
	if err != nil {
		writeLLMConfigError(c, "模型探活", id, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

// ProbeAdhocLLMConfig 在新增或保存前测试临时填写的模型连通性（POST /api/admin/llm-configs/probe）。
func (h *AppHandlers) ProbeAdhocLLMConfig(c *gin.Context) {
	var req services.ProbeLLMConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "探活请求体 JSON 格式非法", nil)
		return
	}

	res, err := h.llmSvc.ProbeAdhoc(c.Request.Context(), req)
	if err != nil {
		writeLLMConfigError(c, "预探活测试", 0, err)
		return
	}

	c.JSON(http.StatusOK, res)
}
