# Git 沙箱、Code IR 静态抽取与 L2 生成/校验引擎设计

- **文档编号**：`02-extraction-and-generation-engine.md`
- **文档版本**：v1.2.0 (ADR-08 单文档拆单、Native 底座加固与 OpenCode 双引擎架构)
- **编制日期**：2026-11-22
- **归属阶段**：阶段二：系统架构、领域引擎与数据模型设计 (`02-design`)
- **关联基线**：`F-02`, `F-04`, `F-05`, `F-07`, `F-14`, `F-27`, `F-29`~`F-31`, `F-35`, `SC-02`, `SC-04`, `SC-06`, `SC-09`, `SC-13`, `SC-15`~`SC-18`, `SC-22`, `NFR-3`~`NFR-6`, [`ADR-02`](../05-adr/02-cpp-parser-and-code-ir-storage.md), [`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md), [`ADR-04`](../05-adr/04-module-scope-discovery-and-hierarchical-docs.md), [`ADR-08`](../05-adr/08-dual-engine-doc-generation.md)

---

## 1. Git 沙箱、路径逃逸防护与三层降噪（`F-02`）

### 1.1 沙箱目录布局与路径边界防护（`SC-06`）

所有代码仓克隆工作区统一落盘于 `<data_dir>/sandbox_dir/repo_<repo_id>/`（按 `repo_id` 复用同一份 `.git` 本地副本，Monorepo 不同 `subdir` 共享该仓库的 `.git` 克隆，但分析与源码预览的逻辑根目录严格收敛至 `<repo_root>/<clean_subdir>`）。

```go
package sandbox

// NormalizeSubdir 规范化 Monorepo 子目录参数（入口参数校验，非法返回 ErrInvalidSubdir -> HTTP 400）
// 规则：
// 1. 禁止空字节 \x00、反斜杠 \、绝对路径前缀 /、以及任何 ".." 路径段；
// 2. 去除首尾空白与多余斜杠，"." 归一化为 ""（代表整仓根）。
func NormalizeSubdir(raw string) (string, error)

// SafeJoin 将用户或模型提供的相对路径安全解析至沙箱根内（越界返回 ErrPathEscape -> HTTP 403）
// 防护三步曲：
// 1. filepath.Clean(filepath.Join(sandboxRoot, relPath))
// 2. filepath.Rel(sandboxRoot, candidate)：拒绝 rel == ".." 或 strings.HasPrefix(rel, "../") 或 filepath.IsAbs(rel)
// 3. filepath.EvalSymlinks 实路径校验：解析 sandboxRoot 与目标文件（或最近存在父目录）的真实物理路径，
//    再次执行 filepath.Rel(realRoot, realTarget)，彻底阻断仓库内恶意符号链接指向 /etc/passwd 等沙箱外文件！
func SafeJoin(sandboxRoot, relPath string) (string, error)
```

> **符号链接防逃逸说明**：遍历代码仓文件树与提供源码证据预览时，**禁止跟随指向沙箱外的符号链接**；发现外部符号链接记录为警告并跳过，源码预览接口直接返回 `403 Forbidden`（满足 `SC-06` 与 NFR-3）。

### 1.2 三层降噪规则引擎（`F-02`、`SC-09`）

在进入 L1 解析之前，文件发现器（`pkg/sandbox.FileWalker`）按严格优先级叠加三层过滤规则：

| 优先级（从低到高） | 规则来源 | 典型过滤策略 | 可覆盖性 |
| :---: | :--- | :--- | :--- |
| **Layer 1（兜底默认）** | 系统内置默认黑名单 | • 版本控制与 IDE：`.git/`、`.svn/`、`.idea/`、`.vscode/`<br>• 三方依赖：`third_party/`、`3rdparty/`、`vendor/`、`node_modules/`、`Pods/`、`target/`、`build/`、`dist/`、`__pycache__/`<br>• 自动生成代码：`*.pb.cc`、`*.pb.h`、`*.pb.go`、`*_pb2.py`、`*.generated.*`、`*.min.js`、`*.min.css`<br>• 二进制与超大文件：ELF/PE/图片/归档后缀、检测前 512 字节含 `\x00` 的二进制文件、单文件体积 > `max_file_bytes`（默认 1 MB） | 可被 Layer 2 显式 `!pattern` 局部放行（二进制与 `.git/` 硬保护除外） |
| **Layer 2（仓库级）** | 仓库根目录 `.archdocignore` | 兼容 `.gitignore` 语法（通配符、目录斜杠、`!` 取反），由代码仓负责人自定义排除特定测试夹具、压测脚本或遗留目录 | 覆盖 Layer 1 |
| **Layer 3（文档类型级）** | `DocType` 的 `meta.json` `scope_profile` | 声明式 `include_globs`、`exclude_globs`、`max_depth`、`include_tests`（如 `data-and-contract` 聚焦 `*.proto`/`**/model/**`/`**/controller/**`） | 仅作用于 L2 上下文裁剪与按需抽取，不污染仓库级完整 Code IR 缓存 |

> **关键设计区分**：L1 抽取写入快照缓存时仅应用 **Layer 1 + Layer 2**，保证同一快照的 Code IR 对所有 `DocType` 通用完备；**Layer 3 (`scope_profile`)** 在读取 Code IR 为特定 `DocType` 装配 L2 上下文时生效，从而完美实现「Extract Once, Render Many」（`SC-02`）。

---

## 2. L1 确定性静态事实抽取与脱敏（`F-04`、[`ADR-02`](../05-adr/02-cpp-parser-and-code-ir-storage.md)）

### 2.1 Canonical Code IR 九大分片 Schema

每个快照 `(repo_id, subdir, ref_name, commit_sha)` 抽取的 Code IR 按 **9 个确定性 `facet`** 拆分为独立压缩 JSON 文件（`schema_version: "1.0"`），所有事实记录共享统一元数据基座 `FactMeta`：

```go
package codeir

type Confidence string

const (
    ConfidenceExact     Confidence = "exact"     // AST/Proto/确定性文件解析精确命中
    ConfidenceHeuristic Confidence = "heuristic" // 启发式原语或文本模式匹配
)

// SourceSpan 源码精确定位信息
type SourceSpan struct {
    FilePath      string `json:"file_path"`                // 沙箱内 POSIX 相对路径
    LineStart     int    `json:"line_start"`               // 1-based 起始行
    LineEnd       int    `json:"line_end"`                 // 1-based 结束行
    Conditional   bool   `json:"conditional,omitempty"`    // 是否位于 #ifdef/#if 等条件编译块内
    ConditionExpr string `json:"condition_expr,omitempty"` // 原始条件表达式（不求值）
}

// FactMeta 每条 Code IR 事实的公共基座
type FactMeta struct {
    Confidence         Confidence `json:"confidence"`
    Span               SourceSpan `json:"span"`
    ContentFingerprint string     `json:"content_fingerprint,omitempty"` // 归一化符号体 sha256 前 16 位
}
```

#### 九大 Facet 结构总览

| Facet 名称 | 核心数据结构与抽取字段 | 语言/适用范围 | `confidence` |
| :--- | :--- | :--- | :---: |
| **`tree`** | `TreeFacet`: `TotalFiles`, `TotalLOC`, `LanguageStats map[string]LangStat`, `Directories[]`（目录路径、文件数、语言占比、代表性入口文件）, `SkippedNoiseSummary`（降噪统计） | 通用（W1） | `exact` |
| **`build`** | `BuildFacet`: `BuildSystems[]` (`cmake`/`bazel`/`makefile`/`maven`/`gradle`/`poetry`/`pip`), `Targets[]`（可执行/库目标名、源码列表）, `CompileOptions[]`（宏定义 `-D`、C++ 标准、开关；含 `compile_commands.json` 增强）, `Scripts[]`（构建/单测/启动命令） | 通用 + C++（W1）+ Java/Py（W2） | `exact` |
| **`deps`** | `DepsFacet`: `ExternalDeps[]`（库名、版本约束、声明文件、范围 `compile`/`runtime`/`test`）, `InternalModules[]`（子目录/Target 间 `#include` / `import` 有向依赖边 `From -> To`, 权重） | 通用 + C++（W1）+ Java/Py（W2） | `exact` |
| **`config_keys`** | `ConfigKeysFacet`: `Keys[]`（`KeyPath`, `ValueType`, `SafeDefault`, `IsSensitive`, `RedactedReason`, `SourceFile`, `EnvVarBinding`）——**强制经预脱敏管线，敏感值一律替换为 `[REDACTED]`** | 通用 YAML/JSON/INI/TOML/ENV/C++ GFlags/Spring Properties（W1/W2） | `exact` / `heuristic` |
| **`symbols`** | `SymbolsFacet`: `Symbols[]`（`FQSymbol`, `Kind`: `namespace\|class\|struct\|enum\|function\|method\|macro_const`, `Signature`, `Visibility`, `BaseTypes[]`, `Fields[]`, `DocCommentSummary`, `FactMeta`） | C/C++（W1）、Java/Python（W2） | `exact` |
| **`idl_contracts`** | `IDLContractsFacet`: `Protos[]`（`Package`, `Imports[]`, `Services[]` -> `RPCs[]` 含 `MethodName`, `RequestType`, `ResponseType`, `ClientStreaming`, `ServerStreaming`, `CrosOptions map[string]string`, `Messages[]`, `Enums[]`, `FactMeta`）；预留 `RuntimeOverlay`（P1 `F-21`） | Protobuf / CROS RPC（W1） | `exact` |
| **`http_routes`** | `HTTPRoutesFacet`: `Routes[]`（`Method`, `Path`, `HandlerFQSymbol`, `RequestParams[]`, `ResponseType`, `Middlewares[]`, `Framework`: `spring\|fastapi\|flask\|cpp_http`, `FactMeta`） | W1 轻量 C++ 路由；W2 Spring/FastAPI/Flask 100% 精确 | `exact` |
| **`data_models`** | `DataModelsFacet`: `Entities[]`（`EntityName`, `TableOrCollection`, `StorageKind`: `sql\|redis\|mq\|shm`, `Fields[]` 含列名/类型/约束, `Indexes[]`, `FactMeta`） | W1 SQL DDL/`.proto` 存储结构；W2 JPA/MyBatis/SQLAlchemy/Django ORM | `exact` |
| **`runtime_primitives`** | `RuntimePrimitivesFacet`: `Primitives[]`（`Category`: `thread\|lock\|shm_ipc\|signal\|timer\|event_loop\|scheduler\|error_code`, `APIName`, `EnclosingFQSymbol`, `ContextSnippet`（脱敏后 ≤ 160 字符）, `FactMeta`） | C/C++ 并发/资源原语（W1）；Java `@Scheduled`/Python Celery/APScheduler（W2） | `heuristic` |

### 2.2 多语言解析器插件 SPI 与分级准确率保障

```go
package codeir

import "context"

// FileEntry 经沙箱安全校验与降噪后的待分析文件描述符
type FileEntry struct {
    RelPath  string
    AbsPath  string // 仅在沙箱读取器内使用，严禁写入 IR
    Language string // "c" | "cpp" | "proto" | "java" | "python" | "config" | "build" | "other"
    Size     int64
}

// ExtractionResult 单语言提取器向汇聚器提交的分片增量
type ExtractionResult struct {
    Build             []BuildTarget
    Deps              []DependencyEdge
    ConfigKeys        []ConfigKeyFact
    Symbols           []SymbolFact
    IDLContracts      []ProtoFileFact
    HTTPRoutes        []HTTPRouteFact
    DataModels        []DataModelFact
    RuntimePrimitives []RuntimePrimitiveFact
    Diagnostics       []ParseDiagnostic // 语法容错恢复记录，不阻断整体抽取
}

// LanguageExtractor 多语言静态提取插件 SPI（严格无状态、并发安全、零 DB 依赖）
type LanguageExtractor interface {
    Name() string
    SupportedLanguages() []string
    ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error)
}
```

#### 分级准确率工程保障策略（对齐 `F-04` 量化验收）
1. **100% 精确层（目录树、构建、`.proto`、路由、ORM）**：
   - **Protobuf / CROS RPC（W1）**：纯 Go AST 解析器逐节点解析 `proto2`/`proto3`，无损提取自定义 `option`（如 CROS 服务名、超时、路由策略）与嵌套 `message`/`enum`。
   - **构建与依赖（W1/W2）**：针对 `CMakeLists.txt`、`BUILD`/`BUILD.bazel`、`Makefile`、`compile_commands.json`、`pom.xml`、`build.gradle`、`pyproject.toml`/`requirements.txt` 编写结构化解析器。
   - **Java / Python Web & ORM（W2）**：利用 `tree-sitter-java` 与 `tree-sitter-python` 精确匹配类/方法注解树（`@RequestMapping`/`@GetMapping`/`@Entity`/`@Table`/`@Scheduled` 与 `@app.get`/`@router.post`/SQLAlchemy `__tablename__`），拼装类前缀路径 + 方法路径得出完整路由签名。
2. **C/C++ 声明层（召回 ≥ 95%、精确 ≥ 98%，W1）**：
   - 使用 `tree-sitter-cpp`（解析 `.hpp/.hh/.hxx/.cc/.cpp/.cxx` 及含 C++ 关键字的 `.h`）与 `tree-sitter-c`（纯 C `.c`/`.h`）。
   - **头文件/宏容错策略**：针对 C++ 工程中常见的导出宏修饰（如 `API_EXPORT class OrderService`、`CROS_RPC_METHOD void Pay(...)`），若 CST 在 `class_specifier`/`function_definition` 外层产生 `ERROR` 节点，采用**局部子节点下钻恢复算法**，从 `ERROR` 节点内部继续提取合法的 `type_identifier` + `field_declaration_list` 或 `function_declarator`，兼顾高召回与低误报。
   - **声明与定义归并**：以 `fq_symbol` + 归一化参数类型列表归并 `.h` 声明与 `.cpp` 实现，同时保留头文件签名位置与源文件实现体位置（用于计算 `content_fingerprint` 与证据直跳）。
3. **并发与资源原语层（召回 ≥ 90%、全标 `heuristic`，W1/W2）**：
   - 维护按类别整理的声明式 CST Query 与标准符号白名单表（POSIX `pthread_*`/`shm_*`/`mmap`/`epoll_*`/`signal`/`sigaction`/`timer_*`、C++ STL `std::thread`/`std::jthread`/`std::mutex`/`std::shared_mutex`/`std::condition_variable`/`std::atomic`、常见错误码枚举 `enum *Error*`/`enum *ErrCode*`/`#define ERR_*`），在函数与类作用域内匹配并自动关联外层 `EnclosingFQSymbol`。

### 2.3 L1 配置键提取与三级敏感值预脱敏管线（`SC-09`、NFR-4）

为确保代码与配置文件中的硬编码密码/Token 在 Code IR 与后续所有落盘/入库数据中 **0 条残留**，L1 在写入 `config_keys` 及任何代码片段（`ContextSnippet`、`DocCommentSummary`）前必须经过 `pkg/security.SecretRedactor` 三级过滤：

1. **第一级 — 键名黑名单（不区分大小写子串/词边界匹配）**：
   - 命中 `password`、`passwd`、`pwd`、`secret`、`token`、`api_key`、`apikey`、`access_key`、`private_key`、`credential`、`auth_header`、`bearer`、`cookie`、`jwt_secret`、`dsn`、`connection_string` 时，**强制将 `SafeDefault` 置为 `[REDACTED]`、`IsSensitive = true`**；对于数据库连接串（如 `postgres://user:pass@host/db`）仅保留协议与键结构、掩码认证段。
2. **第二级 — 凭据特征正则扫描**：
   - 扫描 PEM 私钥头（`-----BEGIN .* PRIVATE KEY-----`）、常见平台 Token 前缀（`sk-`、`ghp_`、`glpat-`、`AKIA[0-9A-Z]{16}`、`eyJ[A-Za-z0-9_-]+\.eyJ`）、URL 内嵌凭据（`://[^:\s]+:[^@\s]+@`）以及赋值模式（`(?i)(pwd|pass|secret|token)\s*[:=]\s*["'][^"']{4,}["']`）。
3. **第三级 — Shannon 高熵字符串检测**：
   - 对长度 ≥ 20 且包含字母数字/符号混合的字面量计算香农熵（阈值 `H ≥ 4.2` bits/char，排除纯路径、已知 UUID/Git SHA 常量与占位符 `${ENV_VAR}`），命中即替换为 `[REDACTED_HIGH_ENTROPY]`。
4. **单元测试硬门禁**：`F-04` 与 `SC-09` 夹具测试断言：序列化后的全部 Code IR facet 文件全文搜索夹具注入密码（如 `secret123`）出现次数严格为 **`0`**。

### 2.4 快照缓存幂等命中、防并发重复抽取与按 Facet 增量加载

- **规范缓存键**：`SnapshotKey = (repo_id, normalized_subdir, ref_name, commit_sha, ir_schema_version)`。
- **进程内 `singleflight.Group` 去重**：当同一仓库同一提交同时触发多篇 `DocType` 生成（或多位用户并发点击）时，以 `SnapshotKey` 为 key 执行 `singleflight.Do`，确保 Git 同步 + L1 抽取严格只执行 **1 次**（满足 `SC-02`），并发等待方直接复用返回的 `snapshot_id`。
- **按 `facet` 增量加载**：`FacetStore.ReadFacet(ctx, snapshot, facetName)` 仅解压当前 `DocType` 或槽位声明的 `ir_facets` 子集；若该 facet 在极端磁盘压力下已被 LRU 回收（`evicted_at != nil`），则透明触发按 `commit_sha` 重抽单快照后回填。

---

## 3. L2 分层语义摘要、预算熔断与 OpenAI Compatible 客户端（`F-05`、`F-14`）

### 3.1 大仓分层摘要与统一术语表缓存（`SC-02`）

大仓无法将全部 Code IR 与源码一次塞入单次 LLM 上下文。引擎采用两阶段 L2 流水线：

1. **阶段 2A — 模块分层摘要与统一术语表（Snapshot 级共享缓存 `glossary.json.gz`）**：
   - 根据 `tree` 与 `deps` 分片将仓库划分为若干高内聚模块/顶层目录单元（如 `src/order`、`src/payment`、`proto/`）。
   - 调用 **`fast` 档位模型** 并行生成每个模块的轻量职责摘要（≤ 300 tokens/模块）与全仓**统一术语表（Glossary：领域缩写、核心服务名、关键数据结构、通信协议）**。
   - 结果原子落盘为 `<ir_cache>/.../glossary.json.gz`；同一快照生成 `system-architecture`、`data-and-contract`、`code-walkthrough` 等多类文档时**直接复用同一份术语表**，保证跨文档术语一致且大幅节省 Token。
2. **阶段 2B — 槽位并发填充（Slot-Level Generation）**：
   - 读取 `DocType` 的 `slots[]` 定义：
     - 过滤 `ownership == "locked"` 且已有历史内容的槽位（直接标记 `skipped_locked`，零 LLM 消耗）；
     - 为每个待生成槽位仅装配其声明的 `ir_facets` 切片（经 `scope_profile` 裁剪）+ 共享术语表 + 该槽位的代表性源码片段（经脱敏与长度上限截断）；
     - 依据 `DocType.model_tier`（`fast` / `flagship`）路由至对应模型，受全局 LLM 并发信号量约束并行执行。

### 3.2 生成前 Token 与文件规模预算熔断（`SC-09`）

在**发出首次 LLM HTTP 请求之前**，编排器必须执行确定性预算预检 `EstimateAndCheckBudget()`：

```text
1. 文件规模预检：
   effective_files = len(codeIR.Tree.EffectiveFiles after scope_profile)
   if effective_files > llmConfig.MaxFilesPerTask:
       触发熔断 -> error_code = "FILE_BUDGET_EXCEEDED"

2. Token 规模确定性预估（零 LLM 调用，采用保守估算：中文 1.5 char/token，代码/英文 3.5 char/token）：
   glossary_tokens = (if glossary cache miss) sum(module_summary_input_tokens + 400) else 0
   slots_tokens    = sum over active_slots of (
                         system_prompt_tokens
                       + glossary_prompt_tokens
                       + min(facet_slice_tokens, slot.TokenBudget)
                       + slot_expected_output_tokens
                     )
   estimated_total_tokens = glossary_tokens + slots_tokens
   if estimated_total_tokens > llmConfig.MaxTokensPerTask:
       触发熔断 -> error_code = "TOKEN_BUDGET_EXCEEDED"

3. 结构化裁剪建议生成（随错误详情返回前端与任务记录）：
   - Top-5 Token 占用目录（建议通过 Monorepo subdir 拆分分析或加入 .archdocignore）
   - Top-3 Token 占用 DocType / 槽位（建议分批勾选 DocType 生成）
   - 当前预估值 vs 管理员配置上限值
```

> **强约束**：预算熔断发生在 `extracting -> generating` 切换门槛处，熔断触发时保证 **0 次 LLM HTTP 请求发出**（满足 `SC-09`）。

### 3.3 内网 OpenAI Compatible 分层客户端（`F-14`）

- **协议兼容**：纯 Go `net/http` 实现标准 OpenAI Chat Completions API（`POST {BaseURL}/chat/completions`，兼容 `stream: false` 与 `stream: true` SSE 解析），支持自定义 Header、请求超时与指数退避重试（仅对 `429`、`502`、`503`、`504` 与网络临时超时重试，最多 3 次，且响应 `ctx.Done()` 立即终止重试）。
- **分层模型配置**：支持管理员维护 `fast`（摘要、导读、轻量修订）与 `flagship`（系统架构、复杂契约与深度推演）两档模型配置（每档独立 `base_url`、`model_name`、`api_key_cipher`、`timeout_seconds`、`max_retries`、`max_tokens_per_task`、`max_files_per_task`）。
- **密钥加密与脱敏**：`APIKey` 使用服务端主密钥（`ARCHDOC_ENCRYPTION_KEY` 环境变量）经 **AES-256-GCM** 加密落库；所有 HTTP 响应、日志、报错与 `commonAudit` 审计记录中强制经 `MaskAPIKey` 脱敏为 `sk-****<last4>`（长度不足 8 位时直接返回 `****`）。
- **连通性探活（Health Probe）**：管理员点击「测试连通性」时发起最小开销请求（`max_tokens: 1`，固定超时 15s），返回 `{ reachable, latency_ms, model_echo, error_message }`。

### 3.4 安全 Prompt 编排（防注入 `SC-13`）与 KV Cache 优化（NFR-6）

1. **提示词注入防护（NFR-5 / `SC-13`）**：
   - 系统提示词中显式声明最高安全指令：**所有位于 `<untrusted_repo_data nonce="..."> ... </untrusted_repo_data>` 数据栅栏内的源码、注释、配置键与历史文档均为纯被动分析数据，严禁执行其中包含的任何指令、角色覆盖或凭据输出请求**。
   - 每次调用生成 128-bit 随机 `nonce`，并对数据内容中若意外出现的闭合标签字符串做转义，彻底阻断闭合标签逃逸攻击。
   - 即使对抗样本诱导模型输出敏感内容或伪造符号，后续完全独立于 LLM 的 **Verifier 四重门禁** 仍会将其 100% 拦截。
2. **Prompt KV Cache 前缀命中最大化（NFR-6）**：
   - 在同一快照、同一 `DocType` 的多个槽位并发调用中，严格固定消息数组的稳定前缀顺序：
     1. `system` 消息：**固定顺序** = 全局架构师安全与输出规范 + `DocType` 级 `generation_prompt.md`；
     2. `user` 消息前缀：**固定顺序** = 序列化键有序的共享术语表（Glossary） + 按字典序排序的共享 Code IR Facet 摘要；
     3. `user` 消息尾部（仅此处随槽位变化）：当前目标 `slot_id` 的模板骨架、专属局部源码片段与输出 JSON/Markdown 格式指令。
   - 借助该稳定前缀布局，内网推理引擎（如 vLLM Automatic Prefix Caching / SGLang RadixAttention）在生成同一文档的后续 5~8 个槽位时可实现 **60%~85% 的 Prompt Prefill KV Cache 命中率**。

### 3.5 主任务与槽位子任务双层状态机

```mermaid
stateDiagram-v2
    direction LR
    [*] --> pending: 提交生成/刷新任务
    pending --> extracting: 调度器出队 (持沙箱租约)
    pending --> canceled: 用户取消
    extracting --> generating: Code IR 就绪且预算预检通过
    extracting --> failed: Git 失败 / 抽取异常 / 预算熔断
    extracting --> canceled: 用户取消 (强杀 git 进程组)
    generating --> verifying: 所有活跃槽位完成 (或部分失败待汇总)
    generating --> failed: 全部槽位失败 / 不可恢复异常
    generating --> canceled: 用户取消 (ctx 立即阻断后续 LLM 调用)
    verifying --> ready: Verifier 完成并落库草稿 Revision
    verifying --> failed: 落库或校验内部异常
    ready --> [*]
    failed --> [*]
    canceled --> [*]
```

**槽位子任务（`archdoc_gen_slot_tasks`）独立状态机**：
- 状态枚举：`pending` → `running` → `completed` | `failed` | `skipped_locked` | `canceled`。
- **取消语义**：每个运行中任务在调度器内存表注册 `context.CancelFunc`；收到取消请求后立即调用 `cancel()`，阻断尚未发出的 LLM 请求并中止正在进行的 HTTP 流，`git` 子进程组立即被 `SIGKILL` 清理。
- **单槽位独立重试（`F-05`）**：若某文档仅第 `k` 个槽位因模型瞬时抖动失败，用户可对该 `slot_id` 发起单槽位重试——仅重跑该槽位 L2 生成并将结果合入草稿、重跑 Verifier，其余已完成槽位内容保持字节级不变。

### 3.4 受限只读 Tool-Calling（核心流程槽位与 Chat 局部精修按需读源码）

在保留「确定性 L1 Code IR + 单轮直出」作为批量自动化骨架的前提下，Go `pkg/llm` 原生支持标准 OpenAI Compatible Function Calling（非流式与 SSE 流式 `delta.tool_calls` 分片聚合），仅对以下两类高价值深度推理场景开放**纯只读、零 Shell、强沙箱隔离**的按需源码探索循环（`pkg/llm/source_tools.go` + `pkg/llm/tool_loop.go`）：

1. **声明式槽位分级路由（零脚本）**：
   - `DocType` 的 `meta.json` 槽位规格新增可选字段 `enable_source_tools`（默认 `false`）与 `max_tool_rounds`（默认 `4`，校验上限 `6`）。
   - 默认仅对核心业务链路/并发/逐跳导读槽位开启：`system-architecture`（`runtime_concurrency`、`core_request_flows`）与 `code-walkthrough`（`call_chain_guide`）；其余清单、配置、契约、锁定槽位保持单轮 L1 Code IR 直出以最大化 KV Cache 命中与低成本。
   - **Canvas Studio Chat 局部精修**（`services/chat_service.go`）：当文档对应仓库沙箱或快照 Code IR 就绪时默认开放受限只读工具，并在实际触发工具调用时推送 SSE `tool_call` 进度帧（未触发工具时事件序列严格保持 `meta -> delta+ -> diff_proposal -> verifier_preview -> done`）。
2. **四个受限只读工具（纯 Go 进程内执行，零子进程）**：
   - `get_symbol_impl`：基于 L1 Code IR（`Symbols` / `IDLContracts` / `HTTPRoutes`）直达符号坐标并读取脱敏实现体；
   - `read_file_lines`：按行号区间读取沙箱内源码（单次 ≤ 200 行、≤ 12 KB）；
   - `grep_in_sandbox`：纯字面量关键词检索（防 ReDoS，单次 ≤ 600 文件、≤ 50 条命中）；
   - `list_dir`：列出沙箱内可见目录与文件（自动隐藏 `.git/`、Layer 1 黑名单、`.archdocignore` 与外部符号链接）。
3. **五重安全与成本护栏**：
   - **路径边界与降噪**：全部文件访问经 `sandbox.SafeJoin` + `EvalSymlinks` 校验，阻断目录穿越、外部符号链接、`.git/`、二进制文件及仓库 `.archdocignore` 排除目录；读取期间持有 `ActiveLeaseManager` 仓库只读租约。
   - **三级凭据脱敏**：所有工具返回文本强制经过 `security.SecretRedactor.RedactText` 脱敏后再送回模型。
   - **防提示词注入数据栅栏（`SC-13`）**：所有 `role="tool"` 返回内容统一使用本次请求 128-bit 随机 `nonce` 包裹在 `<untrusted_repo_data nonce="...">` 中，并对内部伪造栅栏标签执行转义。
   - **轮次/体积/Token 强制收敛**：硬限制最大工具轮次（≤ 6）、单轮调用数（≤ 4）、累计调用数（≤ 12）、累计工具输出字节（≤ 36 KB）与单槽位 Token 上限；触达上限后移除 `tools` 并注入收敛指令强制输出最终 JSON，同时将多轮 Usage 精确累加至 `archdoc_llm_usage_logs`。
   - **内网模型平滑降级**：若首轮遇到上游网关/模型返回 HTTP 400/422/501 不支持 `tools` 参数，自动降级为无工具单轮调用，保障业务零中断。

---

## 4. 证据锚定与 Verifier 四重确定性质量门禁（`F-07`、`SC-04`）

### 4.1 证据锚定与回溯对齐

L2 生成每个槽位时要求输出结构化载荷（章节 Markdown 正文 + 候选引用列表 `cited_anchors[]` 含 `file_path`、`fq_symbol`）。`pkg/verifier` 首先将模型返回的候选引用与 L1 Code IR 进行确定性对齐，补全规范的 `EvidenceAnchor`（[`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md) §2.1）：`anchor_key`、`content_fingerprint`、`commit_sha`、`line_start`/`line_end`、`confidence`。

### 4.2 四重确定性校验管线（零 LLM 依赖）

每次生成完成、手工直编保存、Chat Diff `Accept` 或版本回滚后，均强制同步执行 `verifier.Verify()`：

| 门禁序号 | 校验器 | 确定性算法逻辑 | 违规级别与门禁动作 |
| :---: | :--- | :--- | :--- |
| **Gate 1** | **符号与路径存在性校验** | 从 Markdown 正文（反引号代码引用、表格路径列、证据标签）提取候选文件路径与代码符号（路径正则 + `A::B`/`pkg.Type` 符号提取），与 L1 Code IR 的 `tree` / `symbols` / `idl_contracts` / `http_routes` / `data_models` / `config_keys` 倒排索引比对；未命中的路径或符号标注为 `unverified` | 产生 `warning`（若 `verify_policy.strict_symbol_check == true` 则升级为 `error`）；工作台高亮标注 `unverified` 徽标（满足 `SC-04`） |
| **Gate 2** | **必填槽位完备度校验** | 比对当前文档有效章节与 `DocType.slots[]` 中所有 `required == true` 的槽位：检查存在性、非空性（剔除模板占位符后有效字符数 ≥ `min_chars`），计算 `slot_coverage_rate = completed_required / total_required` | `slot_coverage_rate < 1.0` 时产生 `error` 告警，并在工作台与质量度量中记录缺口槽位 |
| **Gate 3** | **Mermaid 语法与可读性门禁** | 提取正文全部 ```` ```mermaid ```` 代码块，执行确定性词法/结构校验器：① 首行图类型须在白名单内（`flowchart`/`graph`/`sequenceDiagram`/`stateDiagram-v2`/`stateDiagram`/`classDiagram`/`erDiagram`）；② 校验括号/引号配对、`subgraph`/`end` 配对与基础语法；③ 统计节点数与边数，超过 `verify_policy.max_mermaid_nodes`（默认 35）或 `max_mermaid_edges`（默认 60）输出可读性超限告警 | 非法语法：标记 `mermaid_syntax_error`（默认 `error` 级别阻止发布）；超限：标记 `mermaid_complexity_warning`（`warning` 告警，提示拆分图表） |
| **Gate 4** | **独立凭据防泄漏门禁** | 复用 `pkg/security.SecretScanner`（与 L1 共用规则库 + 扩展明文口令赋值模式），对整篇 Markdown 正文、代码块与 Mermaid 标签执行全量扫描——**完全独立于 LLM 自身判断** | **硬阻断 (`blocker`)**：只要命中 ≥ 1 条疑似明文凭据/私钥/高熵 Token，置 `can_approve = false`，后端发布接口强制拒绝返回 `422 Unprocessable Entity`（满足 `SC-04`、`SC-13`） |

> **Verifier 汇总模型**：`VerifierReport` 输出 `{ passed, can_approve, blocker_count, error_count, warning_count, slot_coverage_rate, unverified_refs[], mermaid_issues[], secret_findings[] }`，持久化至 `archdoc_document_revisions.verifier_report_json`。

---

## 5. 沙箱回收、任务优先级调度与成本观测（`F-27`，`W2`）

### 5.1 磁盘配额监控与双级 LRU 回收器 (`ResourceReaper`)

后台回收协程按配置周期（默认每 10 分钟，及每次大任务完成后触发水位检查）监控 `storage.sandbox_dir` 与 `storage.ir_cache` 磁盘占用：

1. **运行中任务硬保护（Lease Lock）**：
   - `ActiveLeaseManager` 在内存中维护 `repo_id -> refCount` 与 `snapshot_id -> refCount` 租约；任何正在执行 Git 同步、L1 抽取、L2 生成或源码预览读取的目录均持有活跃租约，**回收器绝对跳过所有活跃租约目录**（满足 `F-27` 验收指标 1）。
2. **沙箱工作区（`sandbox_dir`）回收策略**：
   - 当总占用超过 `storage.sandbox_max_mb`（默认 20 GB）的高水位（90%）时，按仓库最后访问时间从旧到新回收直至降至低水位（75%）。
   - **已发布快照源码工作区可安全回收**：即使某仓库存在已发布文档，其本地 Git 源码目录仍可被 LRU 清理；当用户点击源码证据抽屉查看该 `commit_sha` 时，源码预览服务检测目录缺失后自动通过 `gitsync.Sync` + `FetchCommitMetadata` 按需重新拉取（只读、幂等）。
3. **Code IR 缓存（`ir_cache`）分级 LRU 回收策略**：
   - 当总占用超过 `storage.ir_cache_max_mb`（默认 10 GB）高水位时，从 PostgreSQL `archdoc_code_ir_facets` 查询未持活跃租约的分片，按**优先级升序 + `last_accessed_at` 升序**回收：
     - **第一优先级回收**：仅关联草稿/已失败任务且超过 24 小时未访问的旧快照分片；
     - **第二优先级回收**：被已发布文档引用的快照分片（仅当第一优先级清空后仍超配额时触发）；
   - 回收动作：先 `os.Remove(storage_path)` 立即归还操作系统空间，再更新 `archdoc_code_ir_facets.evicted_at = NOW()`。

### 5.2 双优先级调度队列与多维并发限流 (`TaskScheduler`)

```mermaid
flowchart LR
    InteractiveReq["交互任务入口<br/>单仓生成 / 增量刷新 / Chat 精修"] --> HighQ["High Priority Queue<br/>(Interactive)"]
    BatchReq["批量任务入口<br/>全仓巡检触发 / P1 批量触达"] --> LowQ["Low Priority Queue<br/>(Batch)"]
    HighQ --> Dispatcher["防饥饿加权调度器<br/>(High:Low = 4:1, High 非空优先)"]
    LowQ --> Dispatcher
    Dispatcher --> UserLimiter["按用户并发限流器<br/>(默认单用户 ≤ 2 并发任务)"]
    UserLimiter --> GlobalSem["全局 LLM 并发信号量<br/>(llm.max_concurrency, 默认 8)"]
    GlobalSem --> Workers["Worker Pool 执行引擎"]
```

- **交互优先与防饥饿**：当批量队列满载时，新提交的交互任务进入 `HighQ`，调度器总是优先派发 `HighQ`（每连续派发 4 个高优任务后若 `LowQ` 非空且有余量则允许 1 个低优任务防彻底饥饿），满足 `F-27` 验收指标 2。
- **排队而非拒绝**：超过全局 LLM 并发上限或单用户并发上限的请求在调度队列中有界排队（队列满时返回 `429 Too Many Requests` 并附等待建议），已入队任务不会因瞬时并发达上限而失败（满足 `F-27` 验收指标 3）。

### 5.3 调用级成本与性能观测埋点

每次 LLM API 调用（含模块摘要、槽位生成、Chat 精修）结束后，无论成功或失败，异步写入 `archdoc_llm_usage_logs` 表，完整记录：
- 归属维度：`task_id`、`document_id`、`repo_id`、`doc_type_slug`、`doc_type_revision_id`、`slot_id`、`operator_id`；
- 执行维度：`stage`（`glossary` | `slot_gen` | `slot_retry` | `chat_refine`）、`model_tier`（`fast` | `flagship`）、`model_name`、`status`（`success` | `failed` | `canceled`）；
- 成本维度：`prompt_tokens`、`cached_prompt_tokens`（记录推理服务返回的 KV Cache 命中量）、`completion_tokens`、`latency_ms`。


---

## 6. Monorepo 基线切片、工作区可降级读写锁与 `module_context` 扩展（[`ADR-04`](../05-adr/04-module-scope-discovery-and-hierarchical-docs.md)）

### 6.1 Code IR Schema `1.1` 增量与纯函数切片（`pkg/codeir/slice.go`）

- `codeir.SchemaVersion` 升至 **`1.1`**。为支持零 IO 纯函数切片与跨模块上下文构建，基线快照（`subdirectory = ""`）在既有 9 大分片内增量采集：
  - `DepsFacet.FileImports []FileImportFact`：C/C++ `#include "..."`、Java `import`、Python `import`/`from ... import`、Proto `import` 的文件级引用明细；
  - `BuildFacet.Subdirectories []BuildSubdirFact`：CMake `add_subdirectory`、Maven `<modules>`、Bazel 子包引用（含脱敏 `Snippet ≤ 400` 字符）；
  - `BuildFacet.IncludeDirs []BuildIncludeDirFact`：CMake `include_directories`/`target_include_directories` 与 `compile_commands.json` `-I` 目录。
- `SliceByModule(base *CodeIRBundle, subdir string, modules []string) (*CodeIRBundle, *ModuleContextFacet, error)` 为纯函数（零 IO / 零 DB）：
  1. 仅保留 `file_path` 以 `<subdir>/` 为前缀的事实并重写路径为以模块为根（Python `FQSymbol` 同步剥离 `<subdir>.` 前缀，外部 `DeclSpan` 清空防污染）；
  2. 重算 `Tree` 统计与模块内 `Deps.InternalModules`；
  3. 确定性生成第 10 个派生分片 **`module_context`**（`OutboundDeps`、`InboundDeps`、`ExportedSurface`、`ConsumedContracts`、`RepoBuildContext`，超 4K Token 截断并置 `Truncated = true`），以 `facet_name = "module_context"` 仅持久化于切片快照。
- **超大仓阈值降级**：当有效文件数超过 `extract.max_repo_files`（默认 `50000`）或耗时超过 `extract.max_repo_duration_seconds`（默认 `600`）时，退化为按子目录独立抽取并复用既有提取器构建轻量全仓依赖索引，`StatsJSON.degraded_reason` 记录降级原因。

### 6.2 仓库级可降级读写锁与三层 `singleflight`（修复 P-5）

- `SnapshotService` 唯一持有 `repoWorktreeLocks map[uint]*DowngradableRWMutex`：
  - `Sync` 持**写锁**执行；若未命中缓存，**原子降级为读锁（`DowngradeToRLock`）**后再运行 L1 基线抽取，彻底消除 `Unlock -> RLock` 间隙竞态；
  - `SourcePreviewService`、`DriftOrchestrator` 与 `source_tools` 统一经 `SnapshotService.WithRepoReadLock` 持读锁读取工作区；沙箱按需重拉经 `SnapshotService.EnsureSandboxSynced` 持写锁执行。
- 三层 `singleflight` 键：`base:<repo_id>|<branch>`（同步+基线抽取）、`slice:<base_snapshot_id>|<subdir>`（切片）、`glossary:<base_snapshot_id>`（仓库级共享术语表）。

### 6.3 范围感知 Prompt 注入、外部白名单工具与 `scope=repo` 校验

- **模块外部上下文注入**：当 `subdir != ""` 且 `DocType.meta.context_inputs` 含 `"module_context"` 时，在 `nonce` 数据栅栏内注入 `module_context` 块并显式限定其仅用于依赖与集成描述；`SandboxSourceToolkit` 仅允许通过 `ExternalFileWhitelist` 读取 `module_context` 提及的仓库根相对路径（经 `SafeJoin(repoRoot, …)` 校验）。
- **整仓汇总注入**：当 `subdir == ""`、`context_inputs` 含 `"module_digests"` 且存在 `active` 模块时，按 `repo_ir_facets` 收敛整仓 IR，并注入 `module_digests`（已发布优先 → 最新草稿 → 术语表兜底）与跨模块依赖图；单模块非 Monorepo 仓自动回退全量 `ir_facets`。
- **Verifier 扩展**：
  - `scope = "repo"` 候选锚点必须在 `module_context` 白名单内命中，未命中一律判 `error`（`can_approve = false`）；
  - `symbol_kind = "module_doc"` 锚点与 `module_digests` 对齐（记录 `content_fingerprint = content_sha256`）。
- **批次生成与防死锁**：`POST /api/tasks/generate-batch` 创建 `task_kind="batch"` 父任务 + `priority="batch"` 子任务；父任务协调协程不占 `TaskScheduler` 并发槽位；执行前按 `scheduler.max_tokens_per_batch`（默认 `1000000`）做批次预算前置熔断，0 次 LLM 调用。

---

## 7. 单文档粒度编排、Native 底座加固与 OpenCode 双引擎架构 ([`ADR-08`](../05-adr/08-dual-engine-doc-generation.md) / `M14`)

### 7.1 单文档任务不变量与无竞态自动拆单（`D-1`）

1. **硬不变量（`1 GenTask = 1 Scope × 1 DocType`）**：
   - 所有非批量任务（`task_kind` 为 `generate`、`regenerate`、`incremental_refresh`）严格绑定唯一目标范围 `(repo_id, subdirectory, ref_name)` 与唯一 `doc_type_slug`，其 `doc_types_json` 数组长度恒为 `1`。
   - `EstimateAndCheckBudget` 仅对单篇文档计算文件数与 Token 预算，彻底消除多篇文档叠加触发 `TOKEN_BUDGET_EXCEEDED` 的预算误熔断与故障域耦合。
2. **两阶段队列容量预留与原子批量创单**：
   - `TaskScheduler.ReserveSlots(priority string, count int) (commit func([]ScheduledJob) error, rollback func(), err error)`：
     - 在 `s.mu` 锁内校验 `!s.stopped` 且 `len(queue) + reserved + count <= cap`，容量不足直接返回 `ErrQueueFull`（HTTP `429 QUEUE_FULL`），此时 DB 未写入任何记录（**0 脏数据**）；
     - 容量充足时原子递增 `reserved += count` 并返回幂等 `commit` 与 `rollback` 闭包；`commit` 内部二次检查 `s.stopped`，若期间调度器停机则归还预留并对 `jobs` 执行 `OnDrop` 后返回 `ErrSchedulerStopped`。
     - 若单测未注入 `TaskScheduler`（`o.scheduler == nil`），编排器退化为平滑直发。
   - **提交流程**：前置校验（仓库、全部 `dedupSlugs` 启用态、分支、引擎可用性） → `ReserveSlots(Interactive, len(dedupSlugs))` → 单 DB 事务 `TaskRepo.CreateTasksBatch(ctx, tasks)`（失败即 `rollback()`） → 为各 `task.ID` 注册 `taskCtx, cancel` 并 `commit(jobs)`；响应体 `GenerateTaskSubmitResponse` 顶层内嵌首任务 `tasks[0]`（向后兼容 `resp.id`）并扩展 `created_count` 与 `tasks[]`。
3. **批量编排单文档子任务与零重复抽取**：
   - `executeBatchGenerateTask` 阶段 1 为每个 `(subdir, slug)` 创建 1 个单文档子任务，阶段 2（若勾选 `include_repo_scope`）为每个 `("", slug)` 创建 1 个整仓单文档子任务；`BudgetAdvice.ModuleBreakdown` 仍按 `subdirectory` 聚合展示。
   - 同一批拆出的多个任务并发命中 `SnapshotService.EnsureSnapshot` 的进程内 `singleflight` 与 `EnsureGlossary` 的 `glossarySF` 缓存，保证 Git 同步、L1 Code IR 与术语表仍严格只执行 1 次（`SC-02`）。

### 7.2 Native 引擎底座加固（`D-2`，`pkg/llm`）

1. **修复超时零重试 Bug 与升级退避机制 (`pkg/llm/client.go`)**：
   - `executeWithRetry` 显式区分呼叫方传入的 `parentCtx` 与单次尝试 `attemptCtx, cancel := context.WithTimeout(parentCtx, attemptTimeout)`；
   - 仅当 **`parentCtx.Err() != nil`** 时判定为主动取消或父级总超时而中止重试；若 `parentCtx.Err() == nil` 且命中 `attemptCtx` 超时、`isTimeoutErr(err)`、首包/空闲看门狗超时、`io.EOF` / `io.ErrUnexpectedEOF`、`connection reset` / `broken pipe` 或 HTTP `429`/`500`/`502`/`503`/`504`，一律标记 `retryable = true`；
   - `MaxRetries` 硬钳位上限从 `3` 对齐至 **`0..10`（默认 `3`）**；退避间隔采用可注入基准 `c.baseBackoff`（默认 `1s`，单测可通过 `NewClientWithOptions` 注入 `10ms`）× `2^attempt` × `±20%` 随机 Jitter（封顶 `30s`），并优先尊重 `429`/`503` 的整数秒或 HTTP-date `Retry-After`（封顶 `30s` 且响应 `parentCtx.Done()`）。
2. **内部流式聚合、`Content-Type` 双模自适应与三层超时看门狗**：
   - 槽位生成（`executeSlotLLM` 与 `RunToolLoop`）默认以 `stream: true` + `stream_options: {"include_usage": true}` 发起请求并在 `doOnce` 内聚合完整 `ChatResponse`；若网关返回 HTTP `400`/`422`/`501` 且报错提及不支持 `stream` 或 `stream_options`，自动降级为 `stream: false`（或剥离 `stream_options`）重发；
   - **双模自适应解析**：`200 OK` 时检查 `Content-Type` 与首非空字节——SSE 流（`text/event-stream` 或以 `data:`/`:`/`event:` 开头）走 `parseSSEResponse`；`application/json` 或 `{` 开头走同步 JSON 解析（兼容所有非流式内网网关与存量 `httptest` 测试桩）；
   - **三层超时护栏**：**首包超时**（`FirstByteTimeoutSeconds` 默认 `90s`）、**流式空闲超时**（`IdleTimeoutSeconds` 默认 `60s`）与**单次总超时**（`TimeoutSeconds` 默认 `180s`），前两者自动取 `min(..., TimeoutSeconds)`；
   - **已吐流防重复重试护栏**：当 `onDelta != nil`（如 Canvas Studio SSE Chat）且已向调用方推送过至少 1 段非空增量（`emittedAny == true`）后发生流中断或空闲超时，强制置 `retryable = false`，杜绝前端出现重复文本；当 `onDelta == nil`（槽位内部聚合）或尚未推送过增量时，允许全量重试；
   - 单槽位输出配额下限由 `300` 提升至 `max(MaxOutputTokens / len(slots), 4096)`，且 `FinishReason == "length"` 时记录结构化告警日志。
3. **单文档预算校准 (`pkg/llm/budget.go`) 与三级结构化合法 JSON 裁剪 (`pkg/llm/prompt.go`)**：
   - 将 `countEffectiveFilesForDocTypes` 重构为 `filterEffectiveFilesForDocTypes`，使 `effectiveFiles` 与 `computeTopDirectories` 共享同一份按 `DocType.Meta.ScopeProfile` 过滤的有效文件集，消除 `include_tests: false` 下 `test/` 目录误报；
   - 重构 `serializeSlotFacets` 删除硬截断 `serialized[:maxChars]`，改用三级结构化降维：① 清空 `Deps.FileImports` 与 `Tree.Files`；② 按事实优先级缩减大列表切片并注入 `_meta: {"truncated": true, "budget_chars": ..., "omitted_counts": {...}}`；③ 极端小预算返回最小合法 JSON 对象，保证 **100% 满足 `json.Valid == true`**，并在 `sourceToolsGuidancePrompt` 引导模型遇到 `_meta.truncated == true` 时优先调只读工具检索源码。

### 7.3 单槽位独立重试全面加固 (`executeSingleSlotRetry`)

1. **租约、信号量与引擎继承**：全程持有 `AcquireRepo` + `AcquireSnapshot` 租约，统一经 `o.acquireLLMSlot(ctx)` 排队（删除直接写 `o.llmSem`）；继承 `task.Engine`——`native` 走 `executeSlotLLM`（`stage = "slot_retry"`），`opencode` 在 `opencodeSem` + `WithRepoReadLock` 保护下仅将该槽位作为单元素 `ActiveSlots` 跑 OpenCode 会话（`stage = "slot_retry_opencode"`）。
2. **兄弟槽位证据保留与全败任务恢复收敛**：
   - 加载上一修订时执行 `overlaySectionMetadata` 并从 `prevEvs` 还原全部非目标槽位的 `CitedAnchors` 传入 `Verifier`；
   - 若原任务因 `ALL_SLOTS_FAILED` 尚未建档（`GetByScope` 返回 `ErrNotFound` 或 `CurrentRevisionNo == 0`），自动回查该任务全部槽位并调用 `persistGeneratedDocument` 创建文档与初始修订，回填 `slotTask.DocumentID`；
   - 若原任务处于 `failed` 终态且重试落库成功，调用专有仓储方法 `TaskRepo.RecoverFailedTaskToReady`（绕过常规终态校验的原子条件更新）将主任务置为 `ready`（`progress_percent = 100`）。

### 7.4 纯领域包 `pkg/opencode/` 分层与运行机制（`D-3`）

```mermaid
flowchart TB
    Call["services.GenerationOrchestrator<br/>(持 Repo/Snapshot 租约 + TaskScheduler 槽位)"] --> Gate{"task.engine?"}
    Gate -->|"native"| Native["pkg/llm (流式聚合 + 三层超时重试 + ToolLoop)"]
    Gate -->|"opencode"| OCSem["opencodeSem (max_concurrency=2) + EnsureSandboxSynced(若被回收) + WithRepoReadLock"]
    OCSem --> Bridge["config_bridge.go<br/>0700 隔离根 + 0600 opencode.json (Mode A/B) + 全套 XDG/CI 隔离 + 只读 Agent"]
    Bridge --> Proc["process.go<br/>--file (>64KB) + LockOSThread 同线程 Start/Wait + Setpgid 强杀 + Idle 看门狗"]
    Proc --> Parser["event_parser.go & prompt.go<br/>8MB JSONL + Usage 归一化 + 防重 H2 剥离 + 容错 JSON 解析"]
    Parser --> Cont{"超时/限流/缺槽位<br/>且可续跑?"}
    Cont -->|"<= 1 次且剩余 >= 60s"| Resume["同隔离目录 --session 续跑补齐"]
    Cont -->|"完成"| Merge["统一回填 GenSlotTask + Verifier 四重门禁 + DocumentRevision"]
    Resume --> Merge
    Native --> Merge
```

1. **包结构与架构红线**：`pkg/opencode/` 下设 `runner.go`、`config_bridge.go`、`process.go`、`event_parser.go`、`prompt.go`、`opencode_test.go`，严格零导入 `models`/`services`/`handlers`/`gorm`/`gin` 与 `gitsync.Sync`。CLI 探活内置 30s TTL 缓存，依次检查 `opencode.binary_path` → `exec.LookPath("opencode")` → `$HOME/.opencode/bin/opencode` → `/usr/local/bin/opencode`。
2. **双模式配置桥接与强制只读沙箱**：
   - **模式 A（`LLMConfig.OpenCodeModel == ""`）**：在 `0700` 临时目录写入 `0600` `opencode.json`，配置 `@ai-sdk/openai-compatible` Provider 注入 `baseURL`/`apiKey`/`User-Agent` 并启动 `--model archdoc_ep/<model_name>`；明文 `APIKey` 绝不进入 `argv`、环境变量或日志，函数返回时 `defer os.RemoveAll` 彻底销毁。
   - **模式 B（`LLMConfig.OpenCodeModel != ""`）**：直接传 `--model <OpenCodeModel>`，并按需将宿主机 `~/.local/share/opencode/auth.json` 拷贝至隔离 `xdg_data/opencode/auth.json`（`0600`）。
   - **全隔离与只读矩阵**：白名单基础环境变量 + 覆写 `OPENCODE_CONFIG`/`OPENCODE_CONFIG_DIR`/`XDG_{CONFIG,DATA,STATE,CACHE}_HOME`/`CI=true`/`NO_COLOR=1`/`TERM=dumb`（隔离 SQLite 防多进程 `database is locked`）；固定 `--pure --agent archdoc-readonly-docgen`（仅允许 `read`/`grep`/`glob`/`list`，硬禁止 `edit`/`write`/`patch`/`bash`/`webfetch`/`external_directory`，禁用 `--auto`/`--thinking`）。
3. **进程组与线程安全治理**：
   - 规约文本 `> 64KB` 自动落盘至 `0600` `prompt.md` 通过 `--file` 传入；
   - **单一 owner 协程全程 `runtime.LockOSThread()` 执行 `cmd.Start()` → 并发消费 `stdout` + `32KB` 环形 `stderr` + Idle 看门狗 → 等待读写协程排空 → 同线程 `cmd.Wait()`**，彻底防止 Go 调度器销毁 M 线程误触发 Linux `SysProcAttr.Pdeathsig`；取消或超时调用 `syscall.Kill(-pgid, syscall.SIGKILL)` 秒级回收整个进程树。
4. **8MB JSONL、Usage 归一化、防重标题与 `--session` 续跑**：
   - `bufio.Scanner` 缓冲上限 8MB 解析 `--format json` 事件，按 `messageID` 聚合末条助手文本，累计并归一化 `PromptTokens`/`CachedPromptTokens`（保证 `CachedPromptTokens <= PromptTokens`）；
   - `ParseOpenCodeDocOutput` 兼容纯 JSON、`` ```json `` 围栏及顶层 `{slot_id: ...}` 映射，并自动剥离与 `slot.Title` 重复的首行 `#`/`##` 标题；
   - 遇可恢复错误（`timeout`/`idle_timeout`/`rate_limited` 先退避 2.5s/`output_missing`）或部分 `MissingSlotIDs > 0` 且存在 `SessionID`、剩余父时间 `>= 60s` 时，在同一隔离目录执行 **至多 1 次** `--session <SessionID>` 轻量续跑并合并结果。

### 7.5 双层并发、沙箱按需自愈、增量刷新与 Mode B Glossary 降级

- **双层限流**：`GenerationOrchestrator` 新增独立 `opencodeSem`（容量 `opencode.max_concurrency`，默认 `2`），OpenCode 执行前依次持 `TaskScheduler` 槽位与 `opencodeSem`。
- **沙箱按需自愈**：因 `ResourceReaper` 可按 LRU 回收闲置沙箱工作区，OpenCode 执行前若 `ResolveSandboxScope` 发现工作区缺失，先调用 `SnapshotService.EnsureSandboxSynced`（写锁）拉取，再进入 `WithRepoReadLock` 共享读锁执行。
- **Mode B Glossary 降级**：若 `LLMConfig.OpenCodeModel != ""` 且前置 `EnsureGlossary`（Native HTTP）因端点格式差异失败，记录告警并优雅降级为 `glossary = nil` 继续启动 OpenCode 会话（Mode A 保持严格失败校验）。
- **增量刷新承袭引擎**：`IncrementalRefresh` 未显式指定 `engine` 时，经 `TaskRepo.GetLatestEngineByDocumentID` 继承该文档最近一次生成引擎；`engine == "opencode"` 时合并所有受影响非锁定槽位为单次 OpenCode 会话（`stage = "inc_refresh_opencode"`）。Canvas Studio 实时 Chat 精修保持 Native 流式引擎。
- **列宽安全保护**：写入 `GenSlotTask.ModelName` 与 `LLMUsageLog.ModelName` 的 `[opencode] <label>` 展示串按 128 runes 安全截断。
