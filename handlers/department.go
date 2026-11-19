package handlers

import (
	"log"
	"net/http"

	"code-archdoc/config"

	"github.com/gin-gonic/gin"
)

// ListDepartments 只读返回共享 departments 列表（GET /api/departments）。
func (h *AppHandlers) ListDepartments(c *gin.Context) {
	items, err := h.repoSvc.ListDepartments(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 查询部门列表失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询部门列表失败", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items": items,
	})
}
