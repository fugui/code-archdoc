package handlers

import (
	"net/http"

	"code-archdoc/config"

	"github.com/gin-gonic/gin"
)

// AppVersion 由启动入口注入构建版本号
var AppVersion = "dev"

// HealthCheck 提供免密健康探针接口 GET /api/health（对齐 01-http-api-contract.md §3.1）
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": config.ServiceName,
		"module":  config.ModuleKey,
		"version": AppVersion,
	})
}
