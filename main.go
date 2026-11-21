package main

import (
	"context"
	"embed"
	"flag"
	"log"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/publisher"
	"code-archdoc/pkg/security"
	"code-archdoc/services"
	commonAudit "code-common/backend/audit"
	commonServer "code-common/backend/server"

	"github.com/gin-gonic/gin"
)

//go:embed all:frontend/dist
var frontendFS embed.FS

var (
	Version   = "dev"
	CommitID  = "unknown"
	BuildTime = "unknown"
)

// securityHeadersMiddleware 为响应统一补充浏览器安全防御头
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
		c.Writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		c.Writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Next()
	}
}

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	handlers.AppVersion = Version
	log.Printf("[%s] 启动微前端业务子服务 (Version=%s, Commit=%s, BuildTime=%s)",
		config.ServiceName, Version, CommitID, BuildTime)

	// 1. 加载配置
	if err := config.LoadConfig(*configPath); err != nil {
		log.Fatalf("[%s] 加载配置失败: %v", config.ServiceName, err)
	}

	// 2. 初始化数据库与 18 张 archdoc_* 自有表自动迁移（共享公共表零 DDL）
	if err := models.InitDB(); err != nil {
		log.Fatalf("[%s] 初始化数据库失败: %v", config.ServiceName, err)
	}

	// 3. 初始化异步操作审计引擎
	commonAudit.Init(models.DB)

	// 4. 初始化凭据加解密器（AES-256-GCM；仅独立开发模式允许 HKDF 回退）
	allowDevFallback := config.AppConfig.Auth.StandaloneMode || config.AppConfig.Auth.PasswordLoginEnabled
	keyCipher, err := security.NewCipher(config.AppConfig.Security.EncryptionKey, config.AppConfig.Auth.JWTSecret, allowDevFallback)
	if err != nil {
		log.Printf("[%s] WARNING: 凭据加密器未启用（未配置 ARCHDOC_ENCRYPTION_KEY），写入新 APIKey 将被拒绝: %v", config.ServiceName, err)
	}

	// 5. 初始化纯领域引擎（DocType 目录注册表 + Code IR FacetStore + Tree-Sitter 隔离提取器）
	docTypeRegistry, err := doctype.NewRegistry(config.AppConfig.Storage.DocTypesDir())
	if err != nil {
		log.Fatalf("[%s] 初始化 DocType 注册表失败: %v", config.ServiceName, err)
	}
	facetStore, err := codeir.NewLocalFSFacetStore(config.AppConfig.Storage.IRCacheDir(), 0)
	if err != nil {
		log.Fatalf("[%s] 初始化 Code IR FacetStore 失败: %v", config.ServiceName, err)
	}
	irAggregator := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	llmClient := llm.NewClient()

	// 6. 依赖注入组合根：Store -> Service -> Handler
	masterStore := models.NewSharedMasterStore(models.DB)
	docTypeStore := models.NewDocTypeStore(models.DB)
	docStore := models.NewDocumentStore(models.DB)
	snapStore := models.NewSnapshotStore(models.DB)
	taskStore := models.NewTaskStore(models.DB)
	driftStore := models.NewDriftStore(models.DB)
	govStore := models.NewGovernanceStore(models.DB)
	chatStore := models.NewChatStore(models.DB)
	reviewStore := models.NewReviewCommentStore(models.DB)
	moduleStore := models.NewModuleStore(models.DB)

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc).
		WithModuleRepo(moduleStore)
	llmSvc := services.NewLLMConfigService(govStore, keyCipher)
	docTypeSvc := services.NewDocTypeService(docTypeRegistry, docTypeStore).
		WithSpecDriftNotifier(docStore, driftStore)

	// 启动幂等 Seed 内置 5 类 DocType 并同步至数据库索引（AC-M2-04）
	syncRes, err := docTypeSvc.SeedAndSyncAll(context.Background())
	if err != nil {
		log.Fatalf("[%s] 初始化内置 DocType 注册表失败: %v", config.ServiceName, err)
	}
	log.Printf("[%s] DocType 注册表已同步: synced=%d, new_revisions=%d", config.ServiceName, syncRes.SyncedCount, syncRes.NewRevisions)

	// 7. 装配 M6 活跃租约、双优先级调度器、磁盘 LRU 回收器、双维漂移编排、远端巡检与在线质量度量
	bgCtx, bgCancel := context.WithCancel(context.Background())
	leaseMgr := services.NewActiveLeaseManager()
	taskScheduler := services.NewTaskScheduler(services.SchedulerConfig{
		MaxConcurrency:      config.AppConfig.Scheduler.MaxLLMConcurrency,
		MaxUserConcurrency:  config.AppConfig.Scheduler.MaxUserConcurrency,
		InteractiveQueueCap: config.AppConfig.Scheduler.InteractiveQueueCap,
		BatchQueueCap:       config.AppConfig.Scheduler.BatchQueueCap,
		HighBurstRatio:      4,
	})
	taskScheduler.Start(bgCtx)

	snapSvc := services.NewSnapshotService(
		snapStore,
		facetStore,
		irAggregator,
		services.DefaultGitSyncer{},
		config.AppConfig.Storage.SandboxDir(),
		config.AppConfig.Storage.MaxFileBytes,
	).WithLeaseManager(leaseMgr).
		WithExtractLimits(
			config.AppConfig.Extract.MaxRepoFiles,
			time.Duration(config.AppConfig.Extract.MaxRepoDurationSeconds)*time.Second,
		)
	genSvc := services.NewGenerationOrchestrator(
		masterStore,
		docTypeStore,
		docTypeSvc,
		snapSvc,
		taskStore,
		docStore,
		govStore,
		keyCipher,
		llmClient,
		config.AppConfig.Scheduler.MaxLLMConcurrency,
	).WithGovernance(leaseMgr, taskScheduler).
		WithBatchBudgetLimit(config.AppConfig.Scheduler.MaxTokensPerBatch)
	publishSink, err := publisher.NewLocalFSPublishSink(config.AppConfig.Storage.EffectivePublishDir())
	if err != nil {
		log.Fatalf("[%s] 初始化发布目录 publish_dir 失败: %v", config.ServiceName, err)
	}
	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc).
		WithModuleContext(moduleStore).
		WithReviewHooks(chatStore, reviewStore)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, services.DefaultGitSyncer{}, irAggregator, config.AppConfig.Storage.SandboxDir()).
		WithLeaseManager(leaseMgr).
		WithSnapshotService(snapSvc)
	driftSvc := services.NewDriftOrchestrator(
		masterStore,
		docStore,
		docTypeStore,
		docTypeSvc,
		driftStore,
		rbacSvc,
		genSvc,
		irAggregator,
		leaseMgr,
		config.AppConfig.Storage.SandboxDir(),
		config.AppConfig.Drift.PollIntervalMinutes,
	).WithSnapshotService(snapSvc).
		WithModuleRepo(moduleStore)
	publishSvc := services.NewPublishService(docStore, docTypeStore, masterStore, govStore, publishSink).
		WithDriftOrchestrator(driftSvc)
	moduleSvc := services.NewModuleService(
		masterStore,
		moduleStore,
		docStore,
		docTypeStore,
		taskStore,
		driftStore,
		snapSvc,
		publishSvc,
		driftSvc,
	)
	snapSvc.WithOnBaseExtracted(func(ctx context.Context, repoID uint, commitSHA string, tree *codeir.TreeFacet) {
		if syncErr := moduleSvc.SyncModulesFromTree(ctx, repoID, commitSHA, tree); syncErr != nil {
			log.Printf("[%s] 基础快照抽取后自动同步模块索引失败 (repo_id=%d): %v", config.ServiceName, repoID, syncErr)
		}
	})
	genSvc.WithModuleGovernance(moduleStore, moduleSvc, rbacSvc).
		WithReviewCommentRepo(reviewStore)
	chatSvc := services.NewChatService(chatStore, docStore, docTypeStore, docTypeSvc, govStore, revSvc, snapSvc, previewSvc, keyCipher, llmClient).
		WithReviewCommentRepo(reviewStore).
		WithModuleContext(moduleStore, masterStore)
	reviewSvc := services.NewReviewCommentService(reviewStore, docStore)
	metricsSvc := services.NewQualityMetricsService(govStore, docTypeStore)

	reaperSvc := services.NewResourceReaper(services.ReaperConfig{
		SandboxDir:      config.AppConfig.Storage.SandboxDir(),
		IRCacheDir:      config.AppConfig.Storage.IRCacheDir(),
		PublishDir:      config.AppConfig.Storage.EffectivePublishDir(),
		SandboxMaxBytes: config.AppConfig.Storage.SandboxMaxMB * 1024 * 1024,
		IRCacheMaxBytes: config.AppConfig.Storage.IRCacheMaxMB * 1024 * 1024,
		ReapInterval:    time.Duration(config.AppConfig.Storage.ReapIntervalMins) * time.Minute,
	}, snapStore, govStore, leaseMgr, taskScheduler)
	reaperSvc.Start(bgCtx)

	pollerSvc := services.NewRemoteHeadPoller(services.PollerConfig{
		Enabled:      config.AppConfig.Drift.PollEnabled,
		PollInterval: time.Duration(config.AppConfig.Drift.PollIntervalMinutes) * time.Minute,
		Concurrency:  config.AppConfig.Drift.PollConcurrency,
		RepoTimeout:  20 * time.Second,
	}, driftStore, services.DefaultRemoteHeadFetcher{},
		func(pCtx context.Context, repo models.PollableRepoRecord, sha, branch string) {
			if evalErr := driftSvc.EvaluateRepoCodeDrift(pCtx, repo, sha, branch); evalErr != nil && pCtx.Err() == nil {
				log.Printf("[%s] 计算仓库 #%d Code Drift 异常: %v", config.ServiceName, repo.RepoID, evalErr)
			}
		},
		func(pCtx context.Context) {
			if specErr := driftSvc.RecomputeAllSpecDrifts(pCtx); specErr != nil && pCtx.Err() == nil {
				log.Printf("[%s] 周期重算 Spec Drift 异常: %v", config.ServiceName, specErr)
			}
		},
	)
	pollerSvc.Start(bgCtx)

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithModuleService(moduleSvc).
		WithDocumentServices(revSvc, previewSvc, publishSvc).
		WithChatService(chatSvc).
		WithReviewCommentService(reviewSvc).
		WithGovernanceServices(driftSvc, pollerSvc, metricsSvc, reaperSvc)

	// 8. 基于 code-common/backend/server 启动服务（自动处理 /<ModuleKey> 网关前缀剥离与前端静态产物内嵌托管）
	// TODO(security): 若独立直接暴露于公网环境，请在网关层配置严格 CORS 白名单与全局 API 速率限制
	err = commonServer.Run(commonServer.Options{
		ServiceName:       config.ServiceName,
		Prefix:            config.ModuleKey,
		Port:              config.AppConfig.Server.Port,
		GinLog:            config.AppConfig.Server.GinLog,
		ReadTimeout:       config.AppConfig.Server.ReadTimeout,
		ReadHeaderTimeout: config.AppConfig.Server.ReadHeaderTimeout,
		WriteTimeout:      config.AppConfig.Server.WriteTimeout,
		IdleTimeout:       config.AppConfig.Server.IdleTimeout,
		MaxHeaderBytes:    config.AppConfig.Server.MaxHeaderBytes,
		FrontendFS:        &frontendFS,
		FrontendDistPath:  "frontend/dist",
		CustomMiddlewares: []gin.HandlerFunc{
			securityHeadersMiddleware(),
			commonAudit.Middleware(config.ModuleKey),
		},
		OnShutdown: func(ctx context.Context) {
			// T-M7-03 / AC-M6-03：优雅停机——立即取消运行中任务（强杀关联 Git 进程组）与后台巡检/回收/调度器，并 flush 审计日志
			log.Printf("[%s] 收到停机信号，开始取消运行中任务并清理后台协程...", config.ServiceName)
			bgCancel()
			genSvc.CancelAll()
			pollerSvc.Stop()
			reaperSvc.Stop()
			taskScheduler.Stop()
			if waitErr := genSvc.WaitAllWithContext(ctx); waitErr != nil {
				log.Printf("[%s] 等待运行中生成任务退出超时: %v", config.ServiceName, waitErr)
			}
			docTypeSvc.WaitSpecDriftJobs()
			if closeErr := commonAudit.Close(ctx); closeErr != nil {
				log.Printf("[%s] 关闭审计引擎出现异常: %v", config.ServiceName, closeErr)
			}
		},
		RegisterRoutes: func(r *gin.Engine) {
			appHandlers.RegisterRoutes(r, models.DB)
		},
	})
	if err != nil {
		log.Fatalf("[%s] 服务运行异常退出: %v", config.ServiceName, err)
	}
}
