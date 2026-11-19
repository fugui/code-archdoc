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

// TriggerGenerateTask 触发单仓或子目录 1~N 个 DocType 反推生成任务（POST /api/tasks/generate -> 202）。
func (h *AppHandlers) TriggerGenerateTask(c *gin.Context) {
	var req services.GenerateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "请求体 JSON 格式非法", nil)
		return
	}
	if req.RepoID == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "repo_id 必须为正整数", nil)
		return
	}

	user, normSubdir, ok := h.CheckScopeWriteOrAbort(c, req.RepoID, req.Subdirectory)
	if !ok {
		return
	}
	req.Subdirectory = normSubdir

	vo, err := h.genSvc.SubmitGenerateTask(c.Request.Context(), req, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, sandbox.ErrInvalidSubdir):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, err.Error(), nil)
		case errors.Is(err, services.ErrDocTypeInactive):
			WriteError(c, http.StatusBadRequest, ErrCodeDocTypeInactive, err.Error(), nil)
		case errors.Is(err, services.ErrInvalidTaskParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
		default:
			log.Printf("[%s] 提交生成任务失败: %v", config.ServiceName, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "提交生成任务失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"trigger",
		commonModels.AuditLevelP1,
		fmt.Sprintf("触发仓库 #%d (subdir=%q) 文档反推生成任务 #%d (%v)", req.RepoID, req.Subdirectory, vo.ID, vo.DocTypes),
		"gen_task",
		strconv.FormatUint(uint64(vo.ID), 10),
		fmt.Sprintf("task#%d", vo.ID),
		nil,
		vo,
	)

	c.JSON(http.StatusAccepted, vo)
}

// ListTasks 分页查询生成任务历史（GET /api/tasks）。
func (h *AppHandlers) ListTasks(c *gin.Context) {
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

	var filter models.TaskListFilter
	if repoStr := strings.TrimSpace(c.Query("repo_id")); repoStr != "" {
		v, pErr := strconv.ParseUint(repoStr, 10, 64)
		if pErr != nil || v == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "repo_id 参数非法", nil)
			return
		}
		id := uint(v)
		filter.RepoID = &id
	}
	if _, hasSub := c.GetQuery("subdirectory"); hasSub {
		normSub, subErr := sandbox.NormalizeSubdir(c.Query("subdirectory"))
		if subErr != nil {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, subErr.Error(), nil)
			return
		}
		filter.Subdirectory = &normSub
	}
	filter.Status = strings.TrimSpace(c.Query("status"))
	if trigStr := strings.TrimSpace(c.Query("triggered_by")); trigStr != "" {
		v, pErr := strconv.ParseUint(trigStr, 10, 64)
		if pErr != nil || v == 0 {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "triggered_by 参数非法", nil)
			return
		}
		id := uint(v)
		filter.TriggeredBy = &id
	}

	resp, err := h.genSvc.ListTasks(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		log.Printf("[%s] 查询任务列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询任务列表失败", nil)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetTaskDetail 查询主任务进度、预算熔断建议与全部槽位子任务状态（GET /api/tasks/:task_id）。
func (h *AppHandlers) GetTaskDetail(c *gin.Context) {
	taskIDVal, err := strconv.ParseUint(c.Param("task_id"), 10, 64)
	if err != nil || taskIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "task_id 参数非法", nil)
		return
	}

	vo, err := h.genSvc.GetTaskDetail(c.Request.Context(), uint(taskIDVal))
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标任务不存在", nil)
			return
		}
		log.Printf("[%s] 查询任务详情失败 (task_id=%d): %v", config.ServiceName, taskIDVal, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询任务详情失败", nil)
		return
	}
	c.JSON(http.StatusOK, vo)
}

// CancelTask 取消排队中或运行中任务（POST /api/tasks/:task_id/cancel）。
func (h *AppHandlers) CancelTask(c *gin.Context) {
	taskIDVal, err := strconv.ParseUint(c.Param("task_id"), 10, 64)
	if err != nil || taskIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "task_id 参数非法", nil)
		return
	}
	taskID := uint(taskIDVal)

	repoID, subdir, err := h.genSvc.GetTaskScope(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标任务不存在", nil)
			return
		}
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询任务归属范围失败", nil)
		return
	}

	if _, _, ok := h.CheckScopeWriteOrAbort(c, repoID, subdir); !ok {
		return
	}

	vo, err := h.genSvc.CancelTask(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, services.ErrTaskAlreadyTerminal) {
			WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
			return
		}
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标任务不存在", nil)
			return
		}
		log.Printf("[%s] 取消任务失败 (task_id=%d): %v", config.ServiceName, taskID, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "取消任务失败", nil)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"cancel",
		commonModels.AuditLevelP1,
		fmt.Sprintf("取消生成任务 #%d", taskID),
		"gen_task",
		strconv.FormatUint(uint64(taskID), 10),
		fmt.Sprintf("task#%d", taskID),
		nil,
		vo,
	)

	c.JSON(http.StatusOK, vo)
}

// RetrySlotTask 对单个失败槽位发起独立重试（POST /api/tasks/:task_id/slots/:slot_task_id/retry -> 202）。
func (h *AppHandlers) RetrySlotTask(c *gin.Context) {
	taskIDVal, err := strconv.ParseUint(c.Param("task_id"), 10, 64)
	if err != nil || taskIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "task_id 参数非法", nil)
		return
	}
	slotTaskIDVal, err := strconv.ParseUint(c.Param("slot_task_id"), 10, 64)
	if err != nil || slotTaskIDVal == 0 {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, "slot_task_id 参数非法", nil)
		return
	}
	taskID := uint(taskIDVal)
	slotTaskID := uint(slotTaskIDVal)

	repoID, subdir, err := h.genSvc.GetTaskScope(c.Request.Context(), taskID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标任务不存在", nil)
			return
		}
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询任务归属范围失败", nil)
		return
	}

	user, _, ok := h.CheckScopeWriteOrAbort(c, repoID, subdir)
	if !ok {
		return
	}

	vo, err := h.genSvc.RetrySlotTask(c.Request.Context(), taskID, slotTaskID, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrNotFound):
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标槽位子任务不存在", nil)
		case errors.Is(err, services.ErrSlotNotRetryable):
			WriteError(c, http.StatusConflict, ErrCodeConflict, err.Error(), nil)
		case errors.Is(err, services.ErrInvalidTaskParam):
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidParam, err.Error(), nil)
		default:
			log.Printf("[%s] 单槽位重试失败 (task_id=%d slot_task_id=%d): %v", config.ServiceName, taskID, slotTaskID, err)
			WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "触发单槽位重试失败", nil)
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"trigger",
		commonModels.AuditLevelP1,
		fmt.Sprintf("触发任务 #%d 槽位 #%d (%s) 独立重试", taskID, slotTaskID, vo.SlotID),
		"gen_task",
		strconv.FormatUint(uint64(taskID), 10),
		fmt.Sprintf("task#%d/slot#%d", taskID, slotTaskID),
		nil,
		vo,
	)

	c.JSON(http.StatusAccepted, vo)
}
