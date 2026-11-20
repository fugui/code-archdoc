package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"code-archdoc/config"
	"code-archdoc/services"

	"github.com/gin-gonic/gin"
)

// GetMetricsOverview 聚合在线质量与 Token/KV Cache 成本 KPI 总览（GET /api/metrics/overview）。
func (h *AppHandlers) GetMetricsOverview(c *gin.Context) {
	days := 30
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "days 必须为正整数", nil)
			return
		}
		days = v
	}

	docType := strings.TrimSpace(c.Query("doc_type"))
	var docTypeRevID *uint
	if raw := strings.TrimSpace(c.Query("doc_type_revision_id")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "doc_type_revision_id 参数非法", nil)
			return
		}
		id := uint(v)
		docTypeRevID = &id
	}

	vo, err := h.metricsSvc.GetOverview(c.Request.Context(), days, docType, docTypeRevID)
	if err != nil {
		if errors.Is(err, services.ErrInvalidMetricsParam) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
			return
		}
		log.Printf("[%s] 查询质量度量总览失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询质量度量总览失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}

// CompareMetricsRevisions 按 DocTypeRevision 纵向对比模板/Prompt 演进带来的质量指标变化（GET /api/metrics/revisions-compare）。
func (h *AppHandlers) CompareMetricsRevisions(c *gin.Context) {
	docType := strings.TrimSpace(c.Query("doc_type"))
	if docType == "" {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "doc_type 参数必填", nil)
		return
	}

	days := 90
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "days 必须为正整数", nil)
			return
		}
		days = v
	}

	vo, err := h.metricsSvc.CompareRevisions(c.Request.Context(), docType, days)
	if err != nil {
		if errors.Is(err, services.ErrInvalidMetricsParam) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
			return
		}
		log.Printf("[%s] 查询修订对比指标失败 (doc_type=%s): %v", config.ServiceName, docType, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询修订对比指标失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}

// GetMetricsSlotHeatmap 返回指定 DocType 各章节槽位的平均修改率与 Verifier 告警率热力表（GET /api/metrics/slot-heatmap）。
func (h *AppHandlers) GetMetricsSlotHeatmap(c *gin.Context) {
	docType := strings.TrimSpace(c.Query("doc_type"))
	if docType == "" {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "doc_type 参数必填", nil)
		return
	}

	days := 30
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "days 必须为正整数", nil)
			return
		}
		days = v
	}

	var docTypeRevID *uint
	if raw := strings.TrimSpace(c.Query("doc_type_revision_id")); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "doc_type_revision_id 参数非法", nil)
			return
		}
		id := uint(v)
		docTypeRevID = &id
	}

	vo, err := h.metricsSvc.GetSlotHeatmap(c.Request.Context(), docType, docTypeRevID, days)
	if err != nil {
		if errors.Is(err, services.ErrInvalidMetricsParam) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
			return
		}
		log.Printf("[%s] 查询槽位修改率热力表失败 (doc_type=%s): %v", config.ServiceName, docType, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询槽位热力表失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}
