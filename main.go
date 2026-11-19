package main

import (
	"context"
	"embed"
	"flag"
	"log"

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

	// 2. 初始化数据库与 16 张 archdoc_* 自有表自动迁移（共享公共表零 DDL）
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

	rbacSvc := services.NewRBACService(masterStore)
	repoSvc := services.NewRepoService(masterStore, docStore, snapStore, taskStore, driftStore, rbacSvc)
	llmSvc := services.NewLLMConfigService(govStore, keyCipher)
	docTypeSvc := services.NewDocTypeService(docTypeRegistry, docTypeStore).
		WithSpecDriftNotifier(docStore, driftStore)

	// 启动幂等 Seed 内置 5 类 DocType 并同步至数据库索引（AC-M2-04）
	syncRes, err := docTypeSvc.SeedAndSyncAll(context.Background())
	if err != nil {
		log.Fatalf("[%s] 初始化内置 DocType 注册表失败: %v", config.ServiceName, err)
	}
	log.Printf("[%s] DocType 注册表已同步: synced=%d, new_revisions=%d", config.ServiceName, syncRes.SyncedCount, syncRes.NewRevisions)

	snapSvc := services.NewSnapshotService(
		snapStore,
		facetStore,
		irAggregator,
		services.DefaultGitSyncer{},
		config.AppConfig.Storage.SandboxDir(),
		config.AppConfig.Storage.MaxFileBytes,
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
	)
	publishSink, err := publisher.NewLocalFSPublishSink(config.AppConfig.Storage.EffectivePublishDir())
	if err != nil {
		log.Fatalf("[%s] 初始化发布目录 publish_dir 失败: %v", config.ServiceName, err)
	}
	revSvc := services.NewRevisionService(docStore, docTypeStore, masterStore, driftStore, snapSvc)
	previewSvc := services.NewSourcePreviewService(docStore, masterStore, services.DefaultGitSyncer{}, irAggregator, config.AppConfig.Storage.SandboxDir())
	publishSvc := services.NewPublishService(docStore, docTypeStore, masterStore, govStore, publishSink)
	chatSvc := services.NewChatService(chatStore, docStore, docTypeStore, docTypeSvc, govStore, revSvc, snapSvc, previewSvc, keyCipher, llmClient)

	appHandlers := handlers.NewAppHandlers(rbacSvc, repoSvc, llmSvc, docTypeSvc, genSvc).
		WithDocumentServices(revSvc, previewSvc, publishSvc).
		WithChatService(chatSvc)

	// 7. 基于 code-common/backend/server 启动服务（自动处理 /<ModuleKey> 网关前缀剥离与前端静态产物内嵌托管）
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
