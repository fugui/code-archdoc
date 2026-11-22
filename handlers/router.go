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
	rbacSvc    *services.RBACService
	repoSvc    *services.RepoService
	moduleSvc  *services.ModuleService
	llmSvc     *services.LLMConfigService
	docTypeSvc *services.DocTypeService
	genSvc     *services.GenerationOrchestrator
	revSvc     *services.RevisionService
	previewSvc *services.SourcePreviewService
	publishSvc *services.PublishService
	chatSvc    *services.ChatService
	reviewSvc  *services.ReviewCommentService
	driftSvc   *services.DriftOrchestrator
	pollerSvc  *services.RemoteHeadPoller
	metricsSvc *services.QualityMetricsService
	reaperSvc  *services.ResourceReaper
}

// NewAppHandlers 构造处理器聚合对象。
func NewAppHandlers(
	rbacSvc *services.RBACService,
	repoSvc *services.RepoService,
	llmSvc *services.LLMConfigService,
	docTypeSvc *services.DocTypeService,
	genSvc *services.GenerationOrchestrator,
) *AppHandlers {
	return &AppHandlers{
		rbacSvc:    rbacSvc,
		repoSvc:    repoSvc,
		llmSvc:     llmSvc,
		docTypeSvc: docTypeSvc,
		genSvc:     genSvc,
	}
}

// WithDocumentServices 注入文档修订、只读源码证据预览与定稿发布导出服务（M3）。
func (h *AppHandlers) WithDocumentServices(
	revSvc *services.RevisionService,
	previewSvc *services.SourcePreviewService,
	publishSvc *services.PublishService,
) *AppHandlers {
	h.revSvc = revSvc
	h.previewSvc = previewSvc
	h.publishSvc = publishSvc
	return h
}

// WithChatService 注入 SSE Chat 局部协同精修服务（M5）。
func (h *AppHandlers) WithChatService(chatSvc *services.ChatService) *AppHandlers {
	h.chatSvc = chatSvc
	return h
}

// WithReviewCommentService 注入 Canvas Studio 审阅批注篮服务（M12）。
func (h *AppHandlers) WithReviewCommentService(reviewSvc *services.ReviewCommentService) *AppHandlers {
	h.reviewSvc = reviewSvc
	return h
}

// WithModuleService 注入模块发现、策略配置与子目录迁移服务（M8）。
func (h *AppHandlers) WithModuleService(moduleSvc *services.ModuleService) *AppHandlers {
	h.moduleSvc = moduleSvc
	return h
}

// WithGovernanceServices 注入 M6 双维漂移编排、远端变更巡检、在线质量度量与磁盘配额治理服务。
func (h *AppHandlers) WithGovernanceServices(
	driftSvc *services.DriftOrchestrator,
	pollerSvc *services.RemoteHeadPoller,
	metricsSvc *services.QualityMetricsService,
	reaperSvc *services.ResourceReaper,
) *AppHandlers {
	h.driftSvc = driftSvc
	h.pollerSvc = pollerSvc
	h.metricsSvc = metricsSvc
	h.reaperSvc = reaperSvc
	return h
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

		// 域 1：仓库与 Monorepo 子目录覆盖视图及架构树只读视图 (F-01 / F-28)
		protected.GET("/repos", h.ListRepos)
		protected.GET("/arch-tree", h.GetArchTree)
		protected.GET("/repos/:repo_id/scopes", h.GetRepoScopes)
		if h.moduleSvc != nil {
			protected.GET("/repos/:repo_id/modules", h.ListRepoModules)
			protected.GET("/repos/:repo_id/module-policy", h.GetRepoModulePolicy)
			protected.PUT("/repos/:repo_id/module-policy", h.UpdateRepoModulePolicy)
			protected.POST("/repos/:repo_id/modules/refresh", h.RefreshRepoModules)
			protected.PATCH("/repos/:repo_id/modules/:module_id/status", h.UpdateRepoModuleStatus)
			protected.POST("/repos/:repo_id/modules/migrate", h.MigrateRepoModule)
		}

		// 域 2：可管理文档类型注册表 W1 只读查询 (F-03)
		if h.docTypeSvc != nil {
			protected.GET("/doc-types", h.ListDocTypes)
			protected.GET("/doc-types/:slug", h.GetDocType)
			protected.GET("/doc-types/:slug/revisions", h.ListDocTypeRevisions)
		}

		// 域 3：反推生成任务编排与监控 (F-05 / F-31)
		if h.genSvc != nil {
			protected.POST("/tasks/generate", h.TriggerGenerateTask)
			protected.POST("/tasks/generate-batch", h.TriggerBatchGenerateTask)
			protected.GET("/tasks", h.ListTasks)
			protected.GET("/tasks/:task_id", h.GetTaskDetail)
			protected.POST("/tasks/:task_id/cancel", h.CancelTask)
			protected.POST("/tasks/:task_id/slots/:slot_task_id/retry", h.RetrySlotTask)
		}

		// 域 4：Canvas Studio 文档审校、手工直编、锁定/孤立保护与只读源码预览 (F-08/F-09/F-10)
		if h.revSvc != nil {
			protected.GET("/documents", h.ListDocuments)
			protected.GET("/documents/:id", h.GetDocument)
			protected.GET("/documents/:id/revisions", h.ListDocumentRevisions)
			protected.GET("/documents/:id/revisions/diff", h.DiffDocumentRevisions)
			protected.PUT("/documents/:id/sections/:slot_id", h.UpdateDocumentSection)
			protected.PATCH("/documents/:id/sections/:slot_id/ownership", h.UpdateSectionOwnership)
			protected.POST("/documents/:id/sections/:slot_id/orphan-resolve", h.ResolveOrphanSection)
			protected.POST("/documents/:id/rollback", h.RollbackDocument)
		}
		if h.revSvc != nil && h.previewSvc != nil {
			protected.GET("/documents/:id/source-preview", h.GetSourcePreview)
		}

		// 域 5：SSE Chat 局部协同精修、审阅批注篮与多槽位提案组 (F-09 / F-33)
		if h.revSvc != nil && h.reviewSvc != nil {
			protected.GET("/documents/:id/review-comments", h.ListReviewComments)
			protected.POST("/documents/:id/review-comments", h.CreateReviewComment)
			protected.PATCH("/documents/:id/review-comments/:comment_id", h.UpdateReviewComment)
			protected.DELETE("/documents/:id/review-comments/:comment_id", h.DeleteReviewComment)
		}
		if h.revSvc != nil && h.chatSvc != nil {
			protected.GET("/documents/:id/chat/sessions", h.ListChatSessions)
			protected.POST("/documents/:id/chat/refine", h.StreamChatRefine)
			protected.POST("/documents/:id/chat/messages/:msg_id/accept", h.AcceptChatProposal)
			protected.POST("/documents/:id/chat/messages/:msg_id/reject", h.RejectChatProposal)
			protected.POST("/documents/:id/chat/messages/:msg_id/apply", h.ApplyChatProposalItems)
			protected.POST("/documents/:id/chat/proposal-items/:item_id/reject", h.RejectChatProposalItem)
		}

		// 域 6：定稿发布、一键撤回与 Markdown/ZIP 导出 (F-11)
		if h.revSvc != nil && h.publishSvc != nil {
			protected.POST("/documents/:id/publish", h.PublishDocument)
			protected.POST("/documents/:id/unpublish", h.UnpublishDocument)
			protected.GET("/documents/:id/export", h.ExportDocument)
			protected.POST("/exports/zip", h.ExportZip)
		}

		// 域 7：远端巡检与章节级双维漂移雷达、增量刷新 (F-12 / F-25 W2)
		if h.driftSvc != nil {
			protected.GET("/drift/summary", h.GetDriftSummary)
			protected.GET("/drift/documents", h.ListDriftedDocuments)
			if h.revSvc != nil {
				protected.POST("/documents/:id/incremental-refresh", h.TriggerIncrementalRefresh)
			}
		}

		// 域 8：在线生成质量度量与修订纵向对比 (F-26 W2)
		if h.metricsSvc != nil {
			protected.GET("/metrics/overview", h.GetMetricsOverview)
			protected.GET("/metrics/revisions-compare", h.CompareMetricsRevisions)
			protected.GET("/metrics/slot-heatmap", h.GetMetricsSlotHeatmap)
		}

		// 3. 模块管理员专属路由 (RequireAdmin - 仅 super_admin 或 archdoc_admin 可访问)
		admin := protected.Group("/admin")
		admin.Use(commonAuth.RequireAdmin(config.AdminRole))
		{
			// 域 2：DocType 管理员全生命周期写操作 (F-03 W2)
			if h.docTypeSvc != nil {
				admin.POST("/doc-types", h.CreateDocType)
				admin.POST("/doc-types/sync", h.SyncDocTypes)
				admin.POST("/doc-types/:slug/clone", h.CloneDocType)
				admin.PUT("/doc-types/:slug", h.UpdateDocType)
				admin.PATCH("/doc-types/:slug/status", h.ToggleDocTypeStatus)
				admin.DELETE("/doc-types/:slug", h.DeleteDocType)
			}

			// 域 7：管理员手动触发全量远端变更巡检 (F-25 W2)
			if h.pollerSvc != nil {
				admin.POST("/drift/poll", h.TriggerAdminDriftPoll)
			}

			// 域 9：OpenAI Compatible 平等模型池配置与探活 + 磁盘配额治理 (F-27 W2)
			admin.GET("/llm-configs", h.ListLLMConfigs)
			admin.POST("/llm-configs", h.CreateLLMConfig)
			admin.POST("/llm-configs/probe", h.ProbeAdhocLLMConfig)
			admin.PUT("/llm-configs/:id", h.UpdateLLMConfig)
			admin.DELETE("/llm-configs/:id", h.DeleteLLMConfig)
			admin.POST("/llm-configs/:id/default", h.SetDefaultLLMConfig)
			admin.POST("/llm-configs/:id/probe", h.ProbeLLMConfig)
			if h.reaperSvc != nil {
				admin.GET("/storage/stats", h.GetAdminStorageStats)
				admin.POST("/storage/reap", h.TriggerAdminStorageReap)
			}
		}
	}
}
