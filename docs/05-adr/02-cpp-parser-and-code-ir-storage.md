# ADR-02: C/C++ 容错解析路线与 Code IR 分片存储选型

- **文档编号**：`02-cpp-parser-and-code-ir-storage.md`
- **日期**：2026-11-19
- **状态**：已采纳 (Accepted)
- **关联议题**：[`01-feature-matrix.md`](../01-features/01-feature-matrix.md) (`KA-01`, `F-04`, `F-27`, `Non-Goal 2`) 与阶段一下沉选型议题 1 & 2

---

## 1. 背景与问题陈述 (Context & Problem Statement)

`code-archdoc` 的核心价值主张为 **「先抽取事实、再生成文档（Extract Once, Render Many）」**。在阶段一收敛后的 P0 范围中，L1 静态事实抽取（Code IR）需在 **零 LLM 调用** 条件下同时解决两个高风险工程选型问题：

1. **议题 1 — C/C++ 容错静态解析路线**：
   - 平台存量主力代码仓包含大量 C/C++ 核心服务及 Protobuf / CROS RPC 定义（`W1` 交付），随后在 `W2` 扩展 Java / Python，P2 扩展 Go / TypeScript。
   - 存量 C/C++ 仓库通常**无法在文档服务宿主机上直接编译**（缺乏交叉编译工具链、三方 SDK 头文件或完整的 `compile_commands.json`）。
   - `F-04` 提出明确的分级准确率门禁：目录树/构建文件/`.proto` 服务与消息/路由/ORM 实体 **100%** 准确；C/C++ 类/结构体/函数声明召回率 **≥ 95%**、精确率 **≥ 98%**；并发与资源原语特征召回率 **≥ 90%**（标注 `heuristic`）；条件编译分支不做求值但须标注 `conditional`。
   - 同时须兼顾工作区单二进制交付约束（`code-archdoc-server` 内嵌前端静态产物）。

2. **议题 2 — Code IR 分片存储介质**：
   - 同一个仓库快照 `(repo_id, subdir, ref_name, commit_sha)` 抽取的 Code IR 会被多个 `DocType`、Verifier、Chat 精修及漂移检测复用。
   - 大型 C++ Monorepo 的 `symbols` 分片体量可达 **10~30 MB**，而 `code-archdoc` 与宿主门户 `code-bench` **共享同一 PostgreSQL 实例**。
   - `F-27` 要求支持按磁盘配额与 LRU 机制对 Code IR 缓存执行确定性空间回收，并支持按 `facet` 增量读取以降低内存与 I/O 开销。

---

## 2. 决策 1：C/C++ 采用 `tree-sitter`（CGO 静态内嵌）+ 纯 Go `.proto` 解析器双轨路线

### 2.1 候选方案多维对比

| 评估维度 | 方案 A：`tree-sitter`（Go CGO 静态绑定） + 纯 Go `.proto` 解析器 | 方案 B：`universal-ctags` 外部 CLI 子进程 | 方案 C：`libclang` / `clangd` 语义前端 | 方案 D：WASM 化 `tree-sitter`（`wazero` 纯 Go） |
| :--- | :--- | :--- | :--- | :--- |
| **无编译环境容错性** | **极高**：GLR 容错语法树，缺失头文件/未展开宏时仅局部生成 `ERROR` 节点，不影响其余声明解析 | 较高：基于启发式词法扫描 | **极低**：缺失 `compile_commands.json` 或头文件时 AST 严重退化 | 极高（同方案 A） |
| **嵌套作用域与方法体分析** | **完整 CST**：精准还原 `namespace::Class::Method`，可遍历函数体识别 `std::mutex`/`pthread_create`/`shm_open` 等并发原语 | **不足**：主要提取顶层符号签名，难以穿透函数体提取并发原语与行区间上下文 | 完整语义 AST | 完整 CST |
| **条件编译标注 (`conditional`)** | **原生支持**：CST 直接保留 `preproc_ifdef` / `preproc_if` / `preproc_elif` 祖先节点，可无求值精准标注 `conditional=true` | 弱：无法稳定关联嵌套 `#ifdef` 作用域 | 预处理阶段直接裁剪未激活分支，丢失条件分支事实 | 原生支持 |
| **多语言统一扩展性** | **极佳**：同一套 CST 引擎平滑覆盖 W1 C/C++、W2 Java/Python 及 P2 Go/TS | 需维护多套 tag 文本解析规则 | 仅限 C/C++/ObjC，Java/Python 需另建引擎 | 极佳，但需额外维护各语言 `.wasm` 产物编译链 |
| **单二进制与运行时依赖** | **单一二进制自包含**：Linux 构建启用 `CGO_ENABLED=1` 将 C 语法库静态编译进 `code-archdoc-server`，无外部 CLI 依赖 | 宿主机必须预装特定版本 `universal-ctags` 二进制，破坏自包含性 | 宿主机须安装庞大 LLVM/libclang 动态库，部署运维极重 | 无 CGO，但单文件解析吞吐量比原生 CGO 慢 **5~15 倍**，大仓冷抽取耗时长 |
| **`.proto` 100% 准确率** | 引入纯 Go 解析器（`emicklei/proto` 或 `bufbuild/protocompile`）确保 `.proto` 与 CROS 自定义 option **100%** 准确 | 正则/ctags 对嵌套 message 与 custom option 支持不完整 | 不支持 `.proto` | 同方案 A |

### 2.2 最终决策

采用 **方案 A：`tree-sitter`（官方 Go 绑定，Linux CGO 静态内嵌）+ 纯 Go Protobuf 解析器 + 纯 Go `compile_commands.json` 增强**：

1. **C/C++ 语法树容错抽取（W1）**：
   - 使用官方 `github.com/tree-sitter/go-tree-sitter` 结合 `tree-sitter-c` 与 `tree-sitter-cpp` 语法包解析 `.h/.hpp/.hh/.hxx/.c/.cc/.cpp/.cxx` 文件。
   - **符号全限定名构造**：沿 CST 祖先链收集 `namespace_definition`、`class_specifier`、`struct_specifier`、`enum_specifier` 构造稳定 `fq_symbol`（如 `order::OrderService::Pay`），达到召回率 ≥ 95%、精确率 ≥ 98%。
   - **条件编译无求值标注**：遍历 CST 时维护 `inPreprocBranch` 栈（遇到 `preproc_ifdef`/`preproc_ifndef`/`preproc_if`/`preproc_else`/`preproc_elif` 入栈并记录条件表达式原文），栈非空时该子树下所有事实均标记 `conditional: true` 及 `condition_expr`，严格遵守 `Non-Goal 2`（不做宏展开与条件求值）。
   - **并发与资源原语启发式抽取**：在函数体与类成员声明子树上执行受控 CST Query，匹配线程（`std::thread`/`pthread_create`/`std::async`）、锁（`std::mutex`/`std::shared_mutex`/`pthread_mutex_t`/`std::lock_guard`/`std::unique_lock`）、共享内存与 IPC（`shm_open`/`shmget`/`mmap`/`mq_open`）、事件循环与定时/信号（`epoll_wait`/`timerfd_create`/`signal`/`sigaction`）及错误码枚举/宏，统一标注 `confidence: "heuristic"`。
2. **Protobuf / CROS RPC 100% 精确解析（W1）**：
   - 不依赖正则或 `tree-sitter`，使用纯 Go Protobuf AST 解析器解析 `.proto` 文件，精确提取 `package`、`import`、`service`、`rpc`（含入参/出参类型、流式标记、CROS 扩展 `option`）、`message` 字段及注释、`enum` 值，全部标注 `confidence: "exact"`。
3. **`compile_commands.json` 零编译可选增强（W1）**：
   - 若沙箱中存在 `compile_commands.json`，仅使用 Go 标准库 `encoding/json` 解析其中的 `directory`/`file`/`arguments`，提取编译开关（`-D` 宏定义、`-I` 头文件路径、`-std=` 标准）写入 `build` 分片，用于辅助头文件映射与文档展示，绝不强依赖其存在。
4. **Java / Python 同底座平滑扩展（W2）**：
   - 复用同一 `tree-sitter` 引擎加载 `tree-sitter-java` 与 `tree-sitter-python`，抽取 Spring / FastAPI / Flask 路由装饰器与注解、ORM 实体（JPA/MyBatis/SQLAlchemy/Django ORM）及定时调度注解（`@Scheduled`/`@celery.task`）。
5. **CGO 隔离与架构约束**：
   - 所有依赖 CGO 的 `tree-sitter` 调用**严格封装在 `pkg/codeir/treesitter/` 包内**，对外只暴露纯 Go 的 `LanguageExtractor` SPI 接口（定义于 `pkg/codeir/`）。
   - `Makefile` 明确标注 Linux 构建参数 `CGO_ENABLED=1`，并在 `make build` / `make test` 前置检测本机 `gcc`/`cc` 可用性；跨平台纯逻辑单测可通过 Mock `LanguageExtractor` 在无 CGO 环境下运行。

---

## 3. 决策 2：Code IR 采用「本地压缩对象文件 + PostgreSQL 元数据索引」分片存储

### 3.1 候选方案多维对比

| 评估维度 | 方案 A：本地压缩分片文件 + PG 元数据索引 | 方案 B：纯 PostgreSQL `jsonb`（按 facet 分行） | 方案 C：仅本地文件系统无 DB 索引 |
| :--- | :--- | :--- | :--- |
| **共享 PostgreSQL 安全隔离** | **最优**：`archdoc_code_ir_facets` 单行 < 1 KB，彻底杜绝大仓 `symbols` JSON 引发共享库 TOAST 表膨胀与 WAL 写放大，保护宿主 `code-bench` | **高风险**：100 个大仓快照即可产生数 GB TOAST 数据，拖慢共享库 `pg_dump` 与主库 I/O | 无 DB 负载，但无法与任务/文档表联查 |
| **按 `facet` 增量读取性能** | **极快**：每个 facet 独立为 `<facet>.json.gz`（gzip 压缩率约 80%~88%），只加载 `DocType` 声明订阅的 facet 文件，SSD 顺序读 < 20ms | 需从 PG TOAST 解压并经网络传输，大 JSONB 解析占用 DB CPU 与连接池 | 快 |
| **`F-27` 磁盘配额与 LRU 回收** | **确定性实时释放**：PG 记录 `size_bytes`、`compressed_bytes`、`last_accessed_at` 与引用保护标记；LRU 回收器 `os.Remove()` 立即释放操作系统磁盘配额 | **回收滞后**：`DELETE` 行仅标记死元组，依赖 `VACUUM FULL`（锁表）才能真正归还磁盘空间 | 需扫描全盘目录统计体积，高并发易产生竞态 |
| **多节点演进能力（P1/P2）** | 通过 `FacetStore` 接口抽象（`Read`/`Write`/`Delete`/`Stat`），P0 落地 `LocalFSFacetStore`，P1/P2 跨机部署无痛切换 `S3FacetStore` | 天然共享，但大对象放 RDBMS 仍违背存储最佳实践 | 不支持多节点 |

### 3.2 最终决策

采用 **方案 A：本地压缩对象文件（`FacetStore`）+ PostgreSQL 元数据索引（`archdoc_code_ir_facets`）**：

1. **物理存储布局**：
   ```text
   <data_dir>/ir_cache/
   └── repo_<repo_id>/
       └── <subdir_sha1_12>/          # 空 subdir 固定为 "root"
           └── <commit_sha>/
               ├── tree.json.gz
               ├── build.json.gz
               ├── deps.json.gz
               ├── config_keys.json.gz
               ├── symbols.json.gz
               ├── idl_contracts.json.gz
               ├── http_routes.json.gz
               ├── data_models.json.gz
               ├── runtime_primitives.json.gz
               └── glossary.json.gz   # L2 模块摘要与统一术语表缓存（跨 DocType 共享）
   ```
2. **写入原子性与完整性校验**：
   - 每个 facet 先序列化为确定性 JSON，经 gzip 压缩写入同目录临时文件 `.tmp-<facet>-<uuid>.gz`，计算压缩前后的 `content_sha256`、`size_bytes`、`compressed_bytes` 与 `item_count`；
   - `Sync()` 刷盘后通过原子 `os.Rename` 落位，再在 PostgreSQL `archdoc_code_ir_facets` 中 `UPSERT` 元数据行。
3. **读取与内存热缓存**：
   - `DocType` 仅读取其 `ir_facets` 列表中声明的 facet 文件，并异步批量更新 `last_accessed_at`（通过节流队列避免高频读放大 PG 写压力）。
   - 进程内提供有界内存 LRU（默认上限 128 MB）缓存近期高频访问的已解析 facet（供 Verifier 与同快照多 `DocType` 并发生成复用）。
4. **与 `F-27` 回收策略协同**：
   - 运行中任务持有的快照通过内存租约表（`ActiveSnapshotLeases`）禁止回收；
   - 被已发布文档引用的快照：**源码沙箱工作区允许优先回收**（查看证据时按 `commit_sha` 按需重拉），其 **Code IR facet 文件拥有更高保留优先级**；仅当总配额仍超限时按 `last_accessed_at` 回收最久未访问的非活跃 facet，并标记 `evicted_at`（下次访问透明触发按 `commit_sha` 重抽）。

---

## 4. 决策后果与约束 (Consequences & Guardrails)

1. **构建环境约束**：Linux 生产与 CI 构建机需具备标准 C 编译器（`gcc`/`clang`）以支持 `CGO_ENABLED=1` 编译 `tree-sitter` 静态语法库；最终产物仍为单一 `code-archdoc-server` 可执行文件，零外部 CLI 运行时依赖。
2. **配置项新增**：`config.yaml` 需声明 `storage.data_dir`（统一承载 `sandbox_dir`、`ir_cache`、`doc_types`、`publish_dir`）及 `storage.ir_cache_max_mb`、`storage.sandbox_max_mb` 配额参数。
3. **测试隔离约束**：单元测试通过 `t.TempDir()` 注入临时 `FacetStore` 目录，结合 `testdb.SetupIsolatedDB(t)` 验证元数据索引与文件回收一致性，严禁污染宿主机数据目录。
4. **架构防腐约束**：`pkg/codeir/` 及其子包禁止导入 `models` 或 `gorm.io/gorm`，仅操作纯 Go IR 结构体与 `FacetStore` 抽象接口（纳入 `make lint-arch` 强制检查）。
