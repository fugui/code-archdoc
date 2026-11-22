package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ListEngines 查询当前可用的文档生成引擎及其探测状态与默认偏好（GET /api/engines -> 200）。
func (h *AppHandlers) ListEngines(c *gin.Context) {
	if h.genSvc == nil {
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "任务编排服务未初始化", nil)
		return
	}
	resp := h.genSvc.ListEngines(c.Request.Context())
	c.JSON(http.StatusOK, resp)
}
