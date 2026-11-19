package handlers

import (
	"code-archdoc/config"
	"code-archdoc/services"
	commonAuth "code-common/backend/auth"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AppHandlers 聚合本模块全部 HTTP/SSE 处理器及其依赖的编排服务。
type AppHandlers struct {
	rbacSvc *services.RBACService
	repoSvc *services.RepoService
	llmSvc  *services.LLMConfigService
}

// NewAppHandlers 构造处理器聚合对象。
func NewAppHandlers(
	rbacSvc *services.RBACService,
	repoSvc *services.RepoService,
	llmSvc *services.LLMConfigService,
) *AppHandlers {
	return &AppHandlers{
		rbacSvc: rbacSvc,
		repoSvc: repoSvc,
		llmSvc:  llmSvc,
	}
}

// RegisterRoutes 集中注册免密开放路由、受保护业务路由与模块管理员路由（供 main.go 与集成测试统一复用）。
func (h *AppHandlers) RegisterRoutes(r *gin.Engine, authDB *gorm.DB) {
	// 1. 免密开放路由 (Unprotected)
	openAPI := r.Group("/api")
	{
		openAPI.GET("/health", HealthCheck)
		openAPI.GET("/auth/config", GetAuthConfig)
		openAPI.POST("/login", Login)
		openAPI.GET("/oauth2/authorize", StartOAuth2Flow)
		openAPI.GET("/oauth2/callback", OAuth2Callback)
	}

	// 2. 受保护业务路由 (Protected - 需携带合法平台 JWT)
	authCfg := commonAuth.AuthConfig{
		JWTSecretGetter: func() string { return config.AppConfig.Auth.JWTSecret },
		DB:              authDB,
		PreloadAssocs:   []string{"Department"},
	}

	protected := r.Group("/api")
	protected.Use(commonAuth.AuthMiddleware(authCfg))
	{
		// 域 0：当前用户、密码与共享主数据
		protected.GET("/me", GetMe)
		protected.PATCH("/password", UpdatePassword)
		protected.GET("/users", SearchUsers)
		protected.GET("/departments", h.ListDepartments)

		// 域 1：仓库与 Monorepo 子目录覆盖视图 (F-01)
		protected.GET("/repos", h.ListRepos)
		protected.GET("/repos/:repo_id/scopes", h.GetRepoScopes)

		// 3. 模块管理员专属路由 (RequireAdmin - 仅 super_admin 或 archdoc_admin 可访问)
		admin := protected.Group("/admin")
		admin.Use(commonAuth.RequireAdmin(config.AdminRole))
		{
			// 域 9：OpenAI Compatible 分层 LLM 配置管理与探活 (F-14)
			admin.GET("/llm-configs", h.ListLLMConfigs)
			admin.PUT("/llm-configs/:tier", h.UpdateLLMConfig)
			admin.POST("/llm-configs/:tier/probe", h.ProbeLLMConfig)
		}
	}
}
