# Milestone 1 作战卡：跨模块 `gitsync` 底座、全量领域模型、安全沙箱与三层 RBAC 基础 API

- **文档编号**：`02-m1-domain-model-and-api.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**W1（最小可用纵向闭环 — 第 1 站）**
- **前置依赖**：[`docs/02-design/01-system-architecture.md`](../02-design/01-system-architecture.md)、[`05-database-schema.md`](../02-design/05-database-schema.md)、[`docs/03-api/01-http-api-contract.md`](../03-api/01-http-api-contract.md)、[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)
- **覆盖特性与场景**：`D-04`, `F-01`, `F-02`(沙箱与降噪), `F-13`, `F-14`, `NFR-1`~`NFR-4`；`SC-01`, `SC-06`(部分), `SC-12`

---

## 1. 实施目标与工程边界

确立 `code-archdoc` 稳固的底层基础设施与安全防线：
1. 完成项目群首个跨模块改造 **`D-04`**：抽取 `code-common/backend/gitsync` 公共包并将 `code-shield` 无损切换至公共包；
2. 落地 `Makefile` 中的 `make lint-arch`（ArchGuard 5 条红线）与 `CGO_ENABLED=1` 检测，从第一天锁定包依赖防腐边界；
3. 移除脚手架示例 `archdoc_items`/`archdoc_settings`，落地全量 **16 张 `archdoc_*` GORM 模型**、共享只读 `ArchitectureElement` 映射、强类型 JSONB 结构、枚举校验与依赖倒置仓储层；
4. 交付纯引擎基础包 `pkg/security`（AES-256-GCM 加密/脱敏 + 三级凭据扫描）与 `pkg/sandbox`（`NormalizeSubdir` / `SafeJoin` 防符号链接逃逸 / 三层降噪 `FileWalker`）；
5. 交付 `F-01` 仓库/子目录覆盖查询 API、`F-13` 三层 RBAC 权限守卫（含子目录负责人精确隔离）与 `commonAudit` 审计、以及 `F-14` 分层 LLM 配置 CRUD 与连通性探活 API。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及工程 / 文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M1-01`** | 抽取 `code-common/backend/gitsync` 公共包并切换 `code-shield` (`D-04`) | `../code-common/backend/gitsync/{sync,exec,lock,diff}.go`<br>`../code-shield/services/runner/git_sync.go` | 剥离 `code-shield/models` 依赖；实现 `Sync`、`LsRemoteHead`、`DiffNameStatus`、`ShowFileAtCommit`、`FetchCommitMetadata`、`GetPathLock`、`CleanStaleGitLocks`、`IsAuthOrPermissionError` 与 `Setpgid`/`SIGKILL` 进程组强杀；强制注入 `GIT_TERMINAL_PROMPT=0` 与 `BatchMode=yes` 保证鉴权失败 < 30s；将 `code-shield` 改为薄适配层并跑通其全量单测 | 无 |
| **`T-M1-02`** | 扩展 `config/` 与建立 `make lint-arch` 防腐门禁 | `config/config.go`<br>`config.yaml.example`<br>`Makefile` | 新增 `StorageSection`、`SecuritySection`、`SchedulerSection`、`DriftSection` 及默认安全值；在 `Makefile` 增加 `lint-arch` 5 条静态检查目标并串联入 `make lint`；校验 `CGO_ENABLED=1` 与本机 `gcc`/`cc` | 无 |
| **`T-M1-03`** | 落地全量 16 张 `archdoc_*` GORM 模型、JSONB 强类型、枚举与仓储 Store | `models/{db,shared,enums,json_types,doctype,snapshot,task,document,chat,drift,governance,stores,test_helper}.go` | 清除示例 `models/item.go`；实现 16 张表 struct 与 `TableName()` 前缀约束；实现全部 `IsValid*` 枚举与 `CanTransitionTaskStatus`；实现 `SetupWithDB`（仅迁移 16 张自有表，零公共表 DDL）与 `SetupTestDB`（隔离库自动建 5 张共享只读测试表）；实现 `services/` 仓储接口的 GORM `*Store` 适配器 | `T-M1-02` |
| **`T-M1-04`** | 实现 `pkg/security`（密钥加解密、脱敏与三级凭据扫描） | `pkg/security/{crypto,mask,redactor}.go` | AES-256-GCM 加解密（`v1:<base64>` 格式、支持 `ARCHDOC_ENCRYPTION_KEY` 与开发态 HKDF 回退）；`MaskAPIKey`（`sk-****<last4>`）；三级 `SecretRedactor`/`SecretScanner`（键名黑名单 + PEM/Token/URL 凭据正则 + Shannon 熵 `H≥4.2` 且排除 UUID/Git SHA/占位符） | `T-M1-02` |
| **`T-M1-05`** | 实现 `pkg/sandbox`（路径防逃逸与三层降噪 `FileWalker`） | `pkg/sandbox/{path,ignore,walker}.go` | `NormalizeSubdir`（拒 `..`/`\x00`/`\`/前导 `/`）；`SafeJoin`（`Clean` + `Rel` + `EvalSymlinks` 彻底防外部符号链接逃逸）；三层降噪（内置黑名单 `third_party/`/`*.pb.cc`/二进制/`>1MB` + 仓库 `.archdocignore` 解析 + 预留 Layer 3 `scope_profile` 过滤接口） | `T-M1-02` |
| **`T-M1-06`** | 实现三层 RBAC 权限引擎、基础公共路由与仓库覆盖查询 API (`F-01`/`F-13`) | `services/repo_service.go`<br>`handlers/{rbac,error,repo,department}.go`<br>`main.go` | 移除示例 `handlers/item.go` & `settings.go`；实现统一错误输出 `WriteError(c, status, code, msg, details)`；实现 `CheckScopeWritePermission`（三层权限 + 规范化子目录全等匹配）；交付 `GET /api/departments`、`GET /api/repos`（URL 分页默认 25 条/页 + 普通用户过滤草稿 + `can_write` 实时计算）、`GET /api/repos/:repo_id/scopes` | `T-M1-03`<br>`T-M1-05` |
| **`T-M1-07`** | 实现 OpenAI Compatible 分层 LLM 配置管理与连通性探活 API (`F-14`) | `services/llm_config_service.go`<br>`handlers/admin_llm.go` | 交付 `GET /api/admin/llm-configs`、`PUT /api/admin/llm-configs/:tier`（空 `api_key` 保留原密文；`commonAudit` 审计 Diff 强制传脱敏视图）、`POST /api/admin/llm-configs/:tier/probe`（`max_tokens:1`, 15s 超时）；启动自动幂等 Seed `fast`/`flagship` 骨架行 | `T-M1-03`<br>`T-M1-04`<br>`T-M1-06` |

---

## 3. TDD 先行单元测试用例清单 (Test-First Specification)

> **实施纪律**：每个子任务编码前必须先创建对应 `*_test.go` 测试文件并运行确认失败（Red），再编写最小实现至全绿（Green）。

### 3.1 `../code-common/backend/gitsync/gitsync_test.go` (`D-04`)
1. `TestSyncLocalBareRepoAndConcurrentLock`：对本地 `git init --bare` 夹具仓发起 5 协程并发 `Sync`，断言同路径锁串行化无 `.git/index.lock` 冲突且返回一致 40 位 `CommitSHA`。
2. `TestCleanStaleGitLocks`：预先写入残留 `index.lock` 与 `shallow.lock`，调用 `Sync` 后断言自动自愈成功。
3. `TestExecCommandProcessGroupCancelKillsChild`：启动会产生子进程的 shell 命令，100ms 后取消 `ctx`，断言命令立即退出且子进程 PID 已被彻底清理（无孤儿进程）。
4. `TestLsRemoteHeadAndDiffAndShowFile`：在夹具仓推进 2 次提交，验证 `LsRemoteHead` 返回最新 SHA、`DiffNameStatus` 精确返回 `A/M/D`、`ShowFileAtCommit` 精确读取旧提交文件内容。
5. `TestAuthErrorFastFailNoHealRetry`：模拟无效 SSH 地址，断言在 30s 内返回鉴权错误且 `IsAuthOrPermissionError == true`。

### 3.2 `models/models_test.go` (`T-M1-03`, 基于 `testdb.SetupIsolatedDB`)
1. `TestAll16TablesPrefixAndAutoMigrate`：遍历全部 16 个模型，断言 `TableName()` 均以 `archdoc_` 开头，且 `SetupTestDB` 在隔离库上幂等执行两次无报错。
2. `TestEnumsAndTaskStateMachine`：覆盖合法与非法枚举值，验证 `CanTransitionTaskStatus` 放行合法流转、拒绝终态再流转及跨阶段非法跳变。
3. `TestUniqueConstraintsAndIdempotentSeeds`：验证 `DocType.Slug`、`RepoSnapshot` 五元组、`Document` 四元组、`LLMConfig.Tier` 联合唯一约束生效；验证 `LLMConfig` 重复 Seed 幂等不覆盖既有配置。

### 3.3 `pkg/security/security_test.go` (`T-M1-04`)
1. `TestAESGCMRoundTripAndTamperDetect`：加解密往返一致、相同明文两次密文不同（随机 Nonce）、篡改密文认证失败返回 error。
2. `TestMaskAPIKey`：验证长 Key 脱敏为 `sk-****1234`、短 Key（< 8 位）脱敏为 `****`、空串返回 `""`。
3. `TestSecretRedactorThreeTiers`：分别注入键名 `db_password: "secret123"`、PEM 私钥块、`sk-proj-xxxx`、`postgres://admin:p@ss@10.0.0.1/db` 与高熵随机字符串，断言全部被准确脱敏；同时注入标准 UUID、40 位 Git SHA 与 `${DB_PASSWORD}` 占位符，断言 **0 误报**。

### 3.4 `pkg/sandbox/sandbox_test.go` (`T-M1-05`)
1. `TestNormalizeSubdir`：验证 `""` / `"."` → `""`、`"svc/order/"` → `"svc/order"`；验证 `"../../etc"`、`"/etc/passwd"`、`"a\x00b"`、`"a\\b"` 全部返回 `ErrInvalidSubdir`。
2. `TestSafeJoinBlocksTraversalAndSymlinkEscape`：在 `t.TempDir()` 内创建指向沙箱外 `/etc/hosts`（或临时外部文件）的符号链接，断言 `SafeJoin` 对 `../outside` 与外部符号链接均返回 `ErrPathEscape`（`SC-06`）。
3. `TestFileWalkerThreeLayerNoiseFilter`：夹具含 `third_party/zlib/z.c`、`proto/order.pb.cc`、含 `\x00` 二进制文件、`>max_file_bytes` 大文件以及 `.archdocignore` 排除项与 `!` 放行项，断言过滤清单与统计计数 100% 精准（`SC-09`）。

### 3.5 `handlers/rbac_repo_llm_test.go` (`T-M1-06` & `T-M1-07`, 基于 `testdb.SetupIsolatedDB`)
1. `TestHealthAndAuthBoundaries`（`SC-01`）：`GET /api/health` 返回 `200`（`service: "Code-Archdoc"`）；无 Token 请求 `GET /api/repos` 返回 `401`；普通用户请求 `GET /api/admin/llm-configs` 返回 `403`。
2. `TestSubdirOwnerRBACIsolation`（`SC-12`）：构造仓库 `R`（Owner=`U1`），子目录 `svc/order`（`ArchitectureElement.Owner=U2`）、`svc/order-v2`（`Owner=U3`）、`svc/pay`（`Owner=U3`）：
   - 断言 `U2` 对 `(R, "svc/order")` `can_write == true`；
   - 断言 `U2` 对 `(R, "svc/order-v2")`、`(R, "svc/pay")`、`(R, "")` 整仓均为 `false`（写接口返回 `403`）；
   - 断言 `U1`（仓库 Owner）与 `archdoc_admin` 对上述全部范围均为 `true`。
3. `TestRepoCoveragePaginationAndDraftHiding`（`F-01`）：验证 `page_size` 默认 25、关键词/部门/状态筛选生效；验证普通用户视图隐藏 `status == "draft"` 的未发布文档。
4. `TestLLMConfigEncryptMaskAuditAndProbe`（`F-14`）：
   - 管理员 `PUT /api/admin/llm-configs/fast` 写入明文 `sk-test-secret-9999`，断言响应返回 `api_key_masked: "sk-****9999"` 且 JSON 中不含明文或密文；
   - 断言 `audit_logs` 中写入了 `llm_config` 审计记录且新旧值 Diff 中**绝对不含 `sk-test-secret-9999`**；
   - 再次更新 `base_url` 且 `api_key: ""`，断言数据库密文未被清空；
   - 使用 `httptest.NewServer` Mock OpenAI `/chat/completions` 端点测试 `POST /api/admin/llm-configs/fast/probe`，验证成功返回 `reachable: true, latency_ms >= 0` 以及超时/500 返回 `reachable: false`。

---

## 4. 自动化验收标准与执行命令 (Acceptance Criteria)

- [x] **AC-M1-01**：`cd ../code-common/backend && go test ./gitsync/...` 与 `cd ../code-shield && go test ./...` 100% 全绿，`code-archdoc` 内部零 Git 同步复制代码。
- [x] **AC-M1-02**：`make lint-arch` 5 条架构防腐红线 100% 通过。
- [x] **AC-M1-03**：`TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=<PWD> dbname=postgres sslmode=disable" make test` 全量通过（含 `models`、`pkg/security`、`pkg/sandbox`、`handlers` 物理隔离单测）。
- [x] **AC-M1-04**：`SC-01`、`SC-12` 及 `SC-06` 路径校验全部在自动化单测中稳定通过，审计表无明文密钥泄露。
