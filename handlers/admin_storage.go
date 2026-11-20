package handlers

import (
	"fmt"
	"log"
	"net/http"

	"code-archdoc/config"
	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
)

// GetAdminStorageStats 返回沙箱/IR 缓存/发布目录磁盘占用、双队列调度状态及近 30 天 LLM Token 成本（GET /api/admin/storage/stats）。
func (h *AppHandlers) GetAdminStorageStats(c *gin.Context) {
	stats, err := h.reaperSvc.GetStorageStats(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 查询存储配额与调度观测数据失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "查询存储配额与调度状态失败", nil)
		return
	}
	c.JSON(http.StatusOK, stats)
}

// TriggerAdminStorageReap 管理员手动执行一次受租约保护的双级 LRU 配额回收（POST /api/admin/storage/reap）。
func (h *AppHandlers) TriggerAdminStorageReap(c *gin.Context) {
	res, err := h.reaperSvc.Reap(c.Request.Context())
	if err != nil {
		log.Printf("[%s] 执行 LRU 配额回收失败: %v", config.ServiceName, err)
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "执行存储空间回收失败", nil)
		return
	}

	commonAudit.SetAuditContext(
		c,
		config.ModuleKey,
		"trigger",
		commonModels.AuditLevelP2,
		fmt.Sprintf("管理员执行沙箱与 IR 缓存 LRU 回收（回收沙箱 %d、分片 %d、释放 %d 字节、跳过活跃租约 %d）",
			res.ReapedSandboxes, res.ReapedIRFacets, res.FreedBytes, res.SkippedActiveLeases),
		"storage_reap",
		"global",
		"storage_reap",
		nil,
		res,
	)

	c.JSON(http.StatusOK, res)
}
