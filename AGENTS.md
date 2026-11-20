# AGENTS.md - 代码设计文档反推 (Code-Archdoc)

> 💡 **上位准则**：工作区通用 SDD+TDD 四阶段研发流水线、`docs/` 双位编号规范、Go/React 编码风格、双模主题 Token、通用分页及自动交付规章，统一遵循 [`../AGENTS.md`](../AGENTS.md)（`code-common/rules/AGENTS.md`），本文档仅定义本模块专属契约与边界。

---

## 1. 模块专属契约 (Module Contract)

| 契约项 | 规范值 | 强约束说明 |
| :--- | :--- | :--- |
| **模块标识 (`ModuleKey`)** | `archdoc` | 门户路由前缀 `/archdoc`，Vite Federation 名称 `name: 'archdoc'` |
| **服务与二进制名** | `Code-Archdoc` / `code-archdoc-server` | Go Module: `code-archdoc` |
| **端口分配** | 后端 `:8100` / 前端 `5182` | 验证完毕后必须主动关闭进程释放端口 |
| **数据库表前缀 (`TablePrefix`)** | `archdoc_` | 本模块所有 GORM 模型 `TableName()` 必须以 `archdoc_` 开头 |
| **管理员角色 (`AdminRole`)** | `archdoc_admin` | 管理接口必须挂载 `commonAuth.RequireAdmin("archdoc_admin")` |

---

## 2. 目录结构与业务边界

```text
code-archdoc/
├── docs/                # SDD + TDD 规范文档库 (00-prompt ~ 06-ops)
├── config/              # 配置加载与模块常量定义
├── models/              # 共享只读映射与 18 张 archdoc_* GORM 数据模型及仓储实现
├── services/            # 领域编排与调度服务（持有仓储接口与纯引擎实例，禁用全局 models.DB）
├── pkg/                 # 纯领域引擎包（sandbox/codeir/doctype/llm/verifier/publisher/drift/security）
├── handlers/            # HTTP/SSE 控制器、三层 RBAC 守卫、审计 Diff 与 *_test.go 隔离单测
└── frontend/            # React 18 微前端工程（暴露 ./App 与 ./menu）
```

* **本模块专有架构红线（由阶段二 `docs/02-design/` 与 `docs/05-adr/` 冻结，纳入 `make lint-arch` 强制检查）**：
  1. **核心引擎零数据库耦合**：`pkg/` 下所有引擎包（Code IR、`DocType` 注册表、LLM 客户端、Verifier、发布渲染器、漂移计算器）严禁导入 `code-archdoc/models`、`gorm.io/gorm` 或 `github.com/gin-gonic/gin`；`pkg/` 与 `services/` 严禁直接访问 `models.DB` 全局单例，必须通过领域仓储接口依赖注入。
  2. **CGO 解析器严格边界隔离（ADR-02）**：`tree-sitter` CGO 依赖必须严格隔离在 `pkg/codeir/treesitter/` 内，对外仅暴露纯 Go `LanguageExtractor` SPI；`.proto` 解析采用纯 Go AST 解析器保障 100% 精确率。
  3. **共享库零膨胀与只读边界（ADR-01 / ADR-02）**：严禁对共享表（`users`、`departments`、`repositories`、`architecture_elements`）执行 DDL 变更或创建冗余仓库主表；Code IR 大载荷必须压缩落盘至本地 `FacetStore`（`ir_cache/`），PG 仅存元数据索引与 LRU 字段。
  4. **单事实源与原子落盘红线（ADR-03）**：`DocType` 以 `doc_types/<slug>/` 目录为唯一事实源（零可执行脚本），写盘须持 `slug` 锁并采用临时文件 + `fsync` + 原子 `rename`；定稿发布仅限 `Approved` 修订写入 `publish_dir/`（经 `.staging/` 原子落位），未审批草稿严禁落盘。
  5. **沙箱路径安全与跨模块复用（D-04 / Non-Goal 8）**：Git 同步统一复用 `code-common/backend/gitsync`，严禁在模块内复制粘贴 Git 同步代码；所有沙箱文件访问须经 `SafeJoin` + `EvalSymlinks` 阻断目录穿越与符号链接逃逸；对 `code-bench` 与 `code-common` 的协同改造严禁引入 `archdoc` 专有业务逻辑。
  6. **凭据防泄漏与不可信数据隔离**：`APIKey` 必须经 AES-256-GCM 加密落库并在接口/日志/审计中脱敏为 `sk-****`；L1 抽取与独立 Verifier 执行双重明文凭据/高熵拦截；仓库源码与注释一律包裹于随机 `nonce` 数据栅栏中防范提示词注入。
  7. **Monorepo 模块发现与工作区锁边界（ADR-04）**：`pkg/codeir/slice.go`、`pkg/codeir/modules.go` 为纯函数零 IO 零 DB 依赖；`archdoc_repo_modules` 严禁冗余负责人字段（负责人唯一事实源为 `architecture_elements`）；仓库级工作区可降级读写锁由 `SnapshotService` 唯一持有，`handlers/` 与 `pkg/` 严禁直接操作沙箱工作区或调用 `gitsync.Sync`。

---

## 3. 常用验证命令

```bash
make test                                                                                      # 后端 fmt + vet + 单元测试
TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=PWD dbname=postgres sslmode=disable" make test  # 物理隔离库全量单测
make lint                                                                                      # 前端 tsc + eslint 检查
make build && make clean                                                                       # 单二进制打包验证并清理产物
```
