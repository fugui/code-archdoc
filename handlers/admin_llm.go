package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// ListLLMConfigs 获取 fast / flagship 分层模型配置列表（GET /api/admin/llm-configs，强制脱敏）。
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

// UpdateLLMConfig 更新指定档位模型配置与预算上限（PUT /api/admin/llm-configs/:tier）。
// 审计 Diff 强制仅传递脱敏快照，绝不落盘明文 APIKey 或密文。
func (h *AppHandlers) UpdateLLMConfig(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	tier := c.Param("tier")
	if !models.IsValidModelTier(tier) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "tier 参数非法，仅支持 fast 或 flagship", nil)
		return
	}

	var req services.UpdateLLMConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, beforeAudit, afterAudit, err := h.llmSvc.UpdateConfig(c.Request.Context(), tier, req, user.ID)
	if err != nil {
		if errors.Is(err, services.ErrInvalidLLMParam) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标模型档位配置不存在", nil)
			return
		}
		log.Printf("[%s] 更新 LLM 配置失败 (tier=%s): %v", config.ServiceName, tier, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "更新模型配置失败", nil)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"update",
		commonModels.AuditLevelP0,
		fmt.Sprintf("更新 %s 档位 LLM 模型配置 (model=%s, active=%v)", tier, vo.ModelName, vo.IsActive),
		"llm_config",
		tier,
		tier,
		beforeAudit,
		afterAudit,
	)

	c.JSON(http.StatusOK, vo)
}

// ProbeLLMConfig 执行内网 OpenAI Compatible 模型连通性探活（POST /api/admin/llm-configs/:tier/probe）。
func (h *AppHandlers) ProbeLLMConfig(c *gin.Context) {
	tier := c.Param("tier")
	if !models.IsValidModelTier(tier) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "tier 参数非法，仅支持 fast 或 flagship", nil)
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

	res, err := h.llmSvc.ProbeConfig(c.Request.Context(), tier, override)
	if err != nil {
		if errors.Is(err, services.ErrInvalidLLMParam) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标模型档位配置不存在", nil)
			return
		}
		log.Printf("[%s] 模型探活异常 (tier=%s): %v", config.ServiceName, tier, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "模型连通性探活执行异常", nil)
		return
	}

	c.JSON(http.StatusOK, res)
}
