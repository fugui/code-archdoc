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
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// GetDriftSummary 返回双维漂移汇总 KPI 与远端巡检健康态（GET /api/drift/summary）。
func (h *AppHandlers) GetDriftSummary(c *gin.Context) {
	summary, err := h.driftSvc.GetSummary(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 查询漂移汇总失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询漂移汇总失败", nil)
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ListDriftedDocuments 分页查询存在 Code/Spec 漂移的已发布文档及受影响槽位明细（GET /api/drift/documents）。
func (h *AppHandlers) ListDriftedDocuments(c *gin.Context) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return
	}

	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		page = 1
	}
	pageSizeRaw := strings.TrimSpace(c.Query("page_size"))
	if pageSizeRaw == "" {
		pageSizeRaw = strings.TrimSpace(c.Query("pageSize"))
	}
	pageSize := 25
	if pageSizeRaw != "" {
		if ps, psErr := strconv.Atoi(pageSizeRaw); psErr == nil {
			pageSize = ps
		}
	}

	filter := models.DriftQueryFilter{
		Search:      strings.TrimSpace(c.Query("search")),
		DocTypeSlug: strings.TrimSpace(c.Query("doc_type")),
		DriftType:   strings.ToLower(strings.TrimSpace(c.DefaultQuery("drift_type", "all"))),
	}
	if deptStr := strings.TrimSpace(c.Query("department_id")); deptStr != "" {
		v, pErr := strconv.ParseUint(deptStr, 10, 64)
		if pErr != nil || v == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "department_id 参数非法", nil)
			return
		}
		id := uint(v)
		filter.DepartmentID = &id
	}
	switch filter.DriftType {
	case "", "all", models.DriftTypeCode, models.DriftTypeSpec, "both":
	default:
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "drift_type 仅支持 all、code、spec 或 both", nil)
		return
	}

	resp, err := h.driftSvc.ListDriftedDocuments(c.Request.Context(), filter, page, pageSize, user)
	if err != nil {
		log.Printf("[%s] 查询漂移文档列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询漂移文档列表失败", nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// TriggerIncrementalRefresh 触发单篇文档受影响非锁定槽位的章节级增量刷新（POST /api/documents/:id/incremental-refresh -> 202）。
func (h *AppHandlers) TriggerIncrementalRefresh(c *gin.Context) {
	doc, user, _, ok := h.resolveDocumentAccess(c, true, false)
	if !ok {
		return
	}
	docID := doc.ID

	var req services.IncrementalRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}

	vo, err := h.driftSvc.TriggerIncrementalRefresh(c.Request.Context(), docID, req, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标文档不存在", nil)
		case errors.Is(err, services.ErrNoRefreshableSlots), errors.Is(err, services.ErrInvalidTaskParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, services.ErrQueueFull):
			WriteError(c, http.StatusTooManyRequests, ErrCodeQueueFull, err.Error(), nil)
		default:
			log.Printf("[%s] 提交增量刷新任务失败 (doc_id=%d): %v", config.ServiceName, docID, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "提交增量刷新任务失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"trigger",
		commonModels.AuditLevelP1,
		fmt.Sprintf("触发文档 #%d 章节级增量刷新任务 #%d (slots=%v)", docID, vo.ID, vo.TargetSlots),
		"gen_task",
		strconv.FormatUint(uint64(vo.ID), 10),
		fmt.Sprintf("doc#%d/incremental#%d", docID, vo.ID),
		nil,
		vo,
	)

	c.JSON(http.StatusAccepted, vo)
}

// TriggerAdminDriftPoll 管理员手动触发新一轮远端变更巡检与双维漂移重算（POST /api/admin/drift/poll -> 202）。
func (h *AppHandlers) TriggerAdminDriftPoll(c *gin.Context) {
	if h.pollerSvc == nil {
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "远端巡检调度服务未启用", nil)
		return
	}
	monitored, err := h.pollerSvc.TriggerPollAsync(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 手动触发远端巡检失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "触发远端变更巡检失败", nil)
		return
	}

	payload := gin.H{
		"triggered":       true,
		"monitored_repos": monitored,
		"message":         "已启动新一轮远端变更巡检与双维漂移重算",
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"trigger",
		commonModels.AuditLevelP2,
		fmt.Sprintf("管理员手动触发全量远端变更巡检（受监控活跃仓库 %d 个）", monitored),
		"drift_poll",
		"global",
		"drift_poll",
		nil,
		payload,
	)

	c.JSON(http.StatusAccepted, payload)
}
