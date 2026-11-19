# code-archdoc: 阶段二系统架构与数据模型设计 (SDD) 提示词指令

## 角色定位与工程原则
你是一名资深 Go 全栈系统架构师、静态程序分析与 AI 工程化架构专家。阶段一特性清单与需求边界（[`docs/01-features/01-feature-matrix.md`](../01-features/01-feature-matrix.md) v0.3.1）已达成 P0 范围收敛与项目群协同共识。

开发过程**严禁跳过设计堆砌业务代码**，现在正式进入：
**「阶段二：系统架构、领域引擎与数据模型设计 (`02-design` & `05-adr`)」**。

> **阶段衔接提醒**：
> 1. 当前 [`docs/02-design/01-system-architecture.md`](../02-design/01-system-architecture.md) 仍包含初始化脚手架占位模型（`ARCHDOC_ITEMS` / `ARCHDOC_SETTINGS`）与 §4 阶段一下沉设计输入；本阶段须将其彻底重构为正式设计文档，完整消化 §4 全部设计输入与 4 项待决策技术选型。
> 2. 结合 `F-26` 质量基线要求，内置 5 类 `DocType` 的槽位模型（`slot_id`、必填项、数据源）设计须可直接承接 Golden Sample 人工标准样例与自动化回归评测。

---

## 1. 输入基线与模块契约 (Inputs as SSOT)

设计必须严格以以下文档为唯一真实源（SSOT），**无需重复搬运业务背景，直接聚焦技术架构、算法边界、数据结构与跨模块契约深度**：

1. **特性矩阵与范围边界**：[`docs/01-features/01-feature-matrix.md`](../01-features/01-feature-matrix.md)（v0.3.1：严格贯彻 `KA-01`~`KA-07`、项目群协同改造 `D-01`~`D-04`、`W1`/`W2` P0 交付波次、`F-01`~`F-05`/`F-07`~`F-14`/`F-25`~`F-27` 与 P1 扩展钩子、验收场景 `SC-01`~`SC-13`、`Non-Goals 1~8` 与 `NFR 1~7`）。
2. **阶段一下沉设计输入**：[`docs/02-design/01-system-architecture.md`](../02-design/01-system-architecture.md) §4（`DocType` 目录资产约定、Git 沙箱规范、生成任务状态机、`make lint-arch` 架构防腐及 §4.5 四项待决策选型）。
3. **工程与生态规约**：[`AGENTS.md`](../../AGENTS.md) 与工作区上位准则 [`../AGENTS.md`](../../../AGENTS.md)（双模微前端、共享库表前缀隔离、Design Tokens、Flat BEM、通用分页及 `testdb` 物理隔离单测）。

### 不可违背的模块硬契约速查
- **模块标识 / 端口**：`ModuleKey="archdoc"`，服务名 `Code-Archdoc` / `code-archdoc-server`，后端 **`:8100`** / 前端 Vite **`5182`**，单二进制内嵌 `frontend/dist`。
- **数据库隔离**：与 `code-bench` 共享 PostgreSQL，**只读复用** `users`、`departments`、`repositories`（`OwnerID`/`RelatedMembers`/`Branch`/`URL`）、`architecture_elements`（`RepoID`/`Subdirectory`/`OwnerID`）、`audit_logs`；自有表强制 `archdoc_` 前缀且显式实现 `TableName() string`。
- **权限角色**：管理员角色 `archdoc_admin`（`super_admin` 同权），支持仓库负责人与 Monorepo 子目录负责人分级授权。
- **交付波次边界**：`W1`（最小可用纵向闭环，含 `D-01`~`D-04` 与 C/C++/Proto 主链路）→ `W2`（补齐 Java/Python、Web `DocType` 管理、Chat 精修、双维漂移雷达、远端巡检、资源治理与质量看板）；P1 能力仅在模型与接口层预留低成本扩展点，不提前落地实现。

---

## 2. 阶段二交付物结构体系（五大设计专篇 + 关键选型 ADR）

为彻底解耦**微前端控制面、L1/L2 静态抽取与生成引擎、`DocType`/漂移/发布治理域、人机协同工作台与持久化数据层**，阶段二采用高内聚低耦合的「5 篇设计专篇 + 2 篇关键选型 ADR」矩阵输出：

| 文档编号与路径 | 篇目类型 | 核心职责与设计边界 |
| :--- | :--- | :--- |
| `docs/02-design/01-system-architecture.md` | **【总览篇】系统总体架构、跨模块协同与包分层治理** | 双模微前端拓扑、运行时分层架构、项目群协同改造（`D-01`~`D-04`）、Go 包依赖矩阵与 `make lint-arch` 架构防腐、三大端到端 Mermaid 时序图、`W1`/`W2` 切分与 P1 扩展点 |
| `docs/02-design/02-extraction-and-generation-engine.md` | **【核心引擎篇】Git 沙箱、Code IR 静态抽取与 L2 生成/校验引擎** | Git 沙箱与三层降噪、Canonical Code IR 分片 Schema 与多语言 SPI（`W1` C/C++/Proto、`W2` Java/Python）、L2 分层摘要/预算熔断/Prompt 防注入与 KV Cache 优化、任务与槽位状态机、Verifier 四重门禁、LRU 回收与优先级调度（`F-27`） |
| `docs/02-design/03-doctype-drift-and-publishing.md` | **【领域治理篇】`DocType` 注册表、双维漂移雷达、发布挂载与质量度量** | 目录为 SSOT 的 `DocType` 资产规范与 `meta.json` Schema、内置 5 类 `DocType` 槽位设计、`slot_id` 锁定与孤立章节保护、`publish_dir` 原子落盘/撤回/深链渲染、`F-25` 巡检与 `F-12` 章节级双维漂移算法、`F-26` Golden Sample 与度量闭环 |
| `docs/02-design/04-workbench-and-security.md` | **【应用与安全篇】Canvas Studio 微前端工作台、双轨修订与 RBAC 审计** | `./App` 与 `./menu` 联邦契约、四大视图 React 组件分层树、手工直编 + SSE Chat 精修双轨修订与版本回滚、源码证据抽屉与深链入口、三层 RBAC（含子目录负责人）判定引擎与 `commonAudit` 审计埋点 |
| `docs/02-design/05-database-schema.md` | **【数据模型篇】PostgreSQL 物理模型与 `archdoc_*` GORM 建模字典** | 共享表只读引用边界、完整 Mermaid ER 图、全部 `archdoc_*` 核心业务表的 GORM Go Struct 定义（字段类型、GORM Tag、联合唯一索引、JSONB 结构、级联与删除保护、P1 预留字段） |
| `docs/05-adr/02-cpp-parser-and-code-ir-storage.md` | **【ADR-02】C/C++ 容错解析路线与 Code IR 分片存储选型** | 针对 §4.5 议题 1 & 2：对比选型、决定 C/C++ 解析方案（含 CGO 与单二进制交付权衡）及 Code IR 存储介质（PG `jsonb` vs 文件系统+PG 索引与 `F-27` LRU 回收权衡） |
| `docs/05-adr/03-evidence-anchor-and-publish-mount.md` | **【ADR-03】跨提交稳定证据锚点与 `code-bench` 挂载发布契约** | 针对 §4.5 议题 3 & 4：确定跨提交符号定位锚点格式，以及 `publish_dir` 原子更新/撤回布局与 `code-bench` 符号链接子树挂载（`D-01`~`D-03`）契约 |

---

## 3. 各专篇详细设计指令与深度要求

### 3.1 `01-system-architecture.md`：系统总体架构、跨模块协同与包分层治理
1. **双模微前端运行拓扑**：CodeBench 宿主门户（`:8000` 挂载 `/archdoc`，`setupFetchInterceptor` 透传 JWT）与独立调试模式（`:5182` / `:8100`，`StandaloneShell` + `UnifiedLogin`）架构图；`commonServer.Run` 路径前缀剥离与 `go:embed all:frontend/dist` 单二进制交付边界。
2. **系统运行时分层架构**：清晰切分 HTTP/SSE 接入层、领域编排与调度层、L1 静态分析与 L2 生成校验执行层、受控本地文件系统层（`sandbox_dir`、`doc_types/`、`publish_dir`）与 PostgreSQL 共享持久化层。
3. **项目群协同改造设计（`D-01`~`D-04`，严格恪守 `Non-Goal 8` 通用化原则）**：
   - **`D-04` (`code-common/backend/gitsync`)**：抽取 `code-shield/services/runner/git_sync.go` 为公共包，定义导出 API、同路径互斥锁、`.git/*.lock` 残留自愈、`Setpgid` 独立进程组超时/取消强杀机制，并说明 `code-shield` 的无损切换方式；
   - **`D-01`~`D-03` (`code-bench`)**：约定 `docs.path` 下符号链接指向 `publish_dir` 的目录布局；定义 `DeveloperDocs` 通用 YAML Frontmatter 解析隐藏与可选元信息栏渲染契约（保证无 Frontmatter 存量文档零回归）；定义 Markdown 中已挂载模块前缀链接（如 `/archdoc/...`）走门户前端路由的通用判定规则（不改变 `/docs/`、`/api/` 与相对路径行为）。
4. **Go 包分层与架构防腐机制（`make lint-arch`，对应 NFR-1）**：
   - 定义 `config/`、`models/`、`handlers/`、`services/`、`pkg/`（或 `internal/`）目录职责与有向依赖矩阵；
   - 明确 `make lint-arch` 静态检查规则：Code IR 解析器、`DocType` 注册表、LLM 客户端与 Verifier 所在包**禁止直接导入 `models.DB` 全局对象**，通过领域仓储接口实现依赖倒置与纯单元测试。
5. **三大端到端全链路 Mermaid 时序图**：
   - ① **反推生成主链路**：范围规范化与 RBAC -> `gitsync` -> Code IR 缓存命中/抽取 -> Token 预算熔断预检 -> L2 槽位并发生成 -> Verifier 四重校验 -> 草稿落库；
   - ② **双轨精修与章节锁定链路**：手工直编 / SSE Chat 局部精修 -> 章节级 Diff 预览与 `Accept` -> 不可变 `Revision` + 重跑 Verifier -> 代码升级重生成时 `locked` 豁免与孤立章节保护；
   - ③ **定稿发布与双维漂移刷新链路**：`Approved` -> `publish_dir` 原子写盘 -> `code-bench` 符号链接浏览与 `/archdoc` 源码证据深链 -> `F-25` `git ls-remote` 巡检 -> `F-12` Code/Spec 双维漂移定位 -> 单章节增量刷新。
6. **波次切分与 P1 扩展钩子**：以对照表明确 `W1`、`W2` 各模块的交付边界，以及为 P1（`ref_name` 多分支、`DocPack`、`legacy-gap-report`、Chat 只读工具、断点恢复、`code-contract` 运行态融合、跨机网关拉取）预留的字段与接口钩子。

### 3.2 `02-extraction-and-generation-engine.md`：Git 沙箱、Code IR 静态抽取与 L2 生成/校验引擎
1. **Git 沙箱与三层降噪（`F-02`）**：
   - 沙箱按 `(repo_id, subdir)` 组织，所有文件访问经 `filepath.Clean` + `filepath.Rel` 校验并阻断符号链接逃逸（满足 `SC-06`）；
   - 三层降噪规则叠加优先级：系统默认规则（三方目录如 `third_party/`/`vendor/`/`node_modules/`、自动生成文件 `*.pb.cc`/`*.pb.h`、超大二进制）-> 仓库级 `.archdocignore` -> `DocType` 声明式 `scope_profile`。
2. **L1 确定性静态事实抽取与脱敏（`F-04`）**：
   - 定义 Canonical Code IR 分片 Schema（按 `facet` 拆分：`tree`、`build`、`deps`、`config_keys`、`symbols`、`idl_contracts`、`http_routes`、`data_models`、`runtime_primitives`），所有事实统一携带 `confidence`（`exact` / `heuristic`）、源码位置与条件编译 `conditional` 标记；
   - 定义多语言解析器插件接口（SPI）：`W1` 落地通用提取器 + C/C++/Protobuf（含 CROS RPC，贯彻 ADR-02 语法树容错路线，无 `compile_commands.json` 仍可用、存在时增强，说明如何满足 `F-04` 分级准确率目标）；`W2` 落地 Java（Spring/ORM/调度）与 Python（FastAPI/Flask/ORM/调度）提取器；
   - 定义 L1 配置键提取与敏感值预脱敏管线（正则 + 键名黑名单 + 高熵检测），保证硬编码密码/Token 在 Code IR 中 **0 残留**（`SC-09`）；
   - 定义快照缓存键 `(repo_id, subdir, ref_name, commit_sha)` 的幂等命中、单仓防并发重复抽取（Singleflight/锁）与按 `facet` 增量加载机制（贯彻 ADR-02 存储选型）。
3. **L2 分层摘要、预算熔断与 OpenAI Compatible 客户端（`F-05`、`F-14`）**：
   - 大仓按模块/目录分片摘要 + 统一术语表缓存设计，保证同快照生成多篇 `DocType` 共享同一份 Code IR 与术语表（`SC-02`）；
   - 生成前 Token 与文件规模预估算法、熔断阈值判定与结构化裁剪建议输出（须在**首次 LLM 调用前**熔断，满足 `SC-09`）；
   - 内网 OpenAI Compatible HTTP/SSE 通用客户端：`BaseURL`、加密落库/接口脱敏的 `APIKey`（`sk-****`）、`fast`/`flagship` 分层模型路由、超时重试与连通性探活；
   - **安全与性能 Prompt 编排**：将仓库源码、注释与旧文档封装为显式隔离的数据块防范提示词注入（`SC-13` / NFR-5）；固定系统指令、术语表与 Code IR 前缀顺序以提升推理服务 Prompt KV Cache 命中率（NFR-6）；
   - **任务与槽位状态机**：主任务 `pending -> extracting -> generating -> verifying -> ready | failed | canceled` 与槽位子任务状态机，支持 Context 取消立即阻断后续 LLM 调用及单槽位独立重试。
4. **证据锚定与 Verifier 四重质量门禁（`F-07`）**：
   - 落地 ADR-03 证据锚点结构（`file_path`、`fq_symbol`、`line_start`/`line_end`、`content_fingerprint`、`commit_sha`）；
   - 四重确定性校验管线：① 符号/路径对 Code IR 的存在性校验（标记 `unverified`）、② 必填槽位覆盖率计算、③ Mermaid 语法合法性 + 图类型白名单 + 节点/边数上限告警、④ 独立于 LLM 的凭据/口令/高熵密钥扫描（命中即阻止进入 `Approved`，满足 `SC-04`）。
5. **沙箱回收、任务优先级调度与成本观测（`F-27`）**：
   - 工作区与 Code IR 缓存的磁盘配额监控与 LRU 回收机制（保护运行中任务工作区；已发布快照允许回收源码工作区，查源码证据时按 `commit_sha` 按需重新拉取）；
   - 全局 LLM 并发信号量、按用户限流、双优先级队列（交互任务优先于批量任务）；
   - 调用级观测埋点（Token、耗时、模型档位、`DocTypeRevision`、阶段）。

### 3.3 `03-doctype-drift-and-publishing.md`：`DocType` 注册表、双维漂移雷达、发布挂载与质量度量
1. **`DocType` 目录即唯一事实源注册表（`F-03`）**：
   - 固化 `doc_types/<slug>/` 资产目录结构（`meta.json`、`template.md`、`generation_prompt.md`、可选 `refine_prompt.md`）与 `slug` 白名单正则 `^[a-z0-9][a-z0-9_-]{1,62}$`；
   - 给出完整 `meta.json` JSON Schema（`name`、`display_name`、`is_active`、`scope_profile`、`ir_facets`、`slots[]`（含稳定 `slot_id`、`title`、`ownership`、`required`、`ir_facets`、`token_budget`）、`verify_policy`、`model_tier`、`timeout`），明确写回时保留未知扩展字段的解析策略；
   - 设计启动幂等 Seed（内置 5 类 `DocType`：`system-architecture`、`data-and-contract`、`ops-and-config`、`code-walkthrough`、`agents-context` 的标准槽位规划，其中架构约束/ADR/技术债槽位默认 `locked`、`agents-context` 显式受 Token 预算约束）与确定性有序聚合哈希 `aggregate_hash` -> 不可变 `DocTypeRevision` 同步流程；
   - 设计 `W2` Web 在线 CRUD 写路径：同 `slug` 写互斥锁、临时文件 + 原子 `rename` 写盘后重同步、重名返回 `409`、被已发布文档引用时禁止物理删除返回 `409`（满足 `SC-08`）。
2. **章节所有权、人工锁定与模板升级保护（`F-10`）**：
   - 明确 `auto`、`hybrid`、`locked` 三种模式在整篇重生成与增量刷新时的合并规则；
   - 设计模板修订移除已有被锁 `slot_id` 时的「孤立章节（Orphan Sections）」保留机制（挂载于文档末尾并标记待人工处置，绝不静默丢弃）。
3. **定稿发布、`publish_dir` 原子落盘、撤回与导出（`F-11`）**：
   - 落地 ADR-03 发布目录层级规范与原子覆盖/撤回清理流程（保证未审批草稿零泄露、撤回后发布视图与发布目录双向不可见，满足 `SC-07`/`SC-10`）；
   - 定义写入 Markdown 头部的 YAML Frontmatter 规范（对齐 `D-02`）、跨文档相对链接检查器与源码证据转 `/archdoc/...` 平台深链渲染规则（对齐 `D-03`）；
   - 设计单篇 Markdown 与多文档 ZIP 打包导出（含目录索引生成与防 Zip-Slip 路径校验）。
4. **远端变更巡检与章节级双维漂移雷达（`F-25`、`F-12`）**：
   - `F-25` 后台巡检调度器设计：按可配置周期（默认 15 分钟）仅对已有文档的仓库执行轻量 `git ls-remote`，有界并发、远端不可达指数退避、不触发完整克隆；
   - `F-12` **Code Drift 章节定位算法**：比对已发布文档 `commit_sha` 与远端 `HEAD` 的变更路径/符号集合，匹配各章节证据锚点精准标记受影响章节（满足 `SC-11`）；
   - `F-12` **Spec Drift 槽位定位算法**：比对文档绑定 `DocTypeRevision` 与最新 `DocTypeRevision`，按 `slot_id` 精确识别新增/变更/删除槽位；
   - 单篇文档增量刷新编排：仅对受影响的 `auto`/`hybrid` 章节重新生成，保障未受影响章节与 `locked` 章节字节级不变。
5. **生成质量评测基线与度量闭环（`F-26`）**：
   - 设计 Golden Sample 标准样例存放规范与自动化回归评测脚本架构（验证必填槽位 100% 覆盖、Verifier 门禁与准确率基线）；
   - 定义指标采集与聚合模型：草稿→定稿章节级修改比例（Diff Ratio）、Chat 修订采纳率、Verifier 拦截率、槽位完备度与各 `DocType` Token 成本，支持按 `DocTypeRevision` 纵向对比。

### 3.4 `04-workbench-and-security.md`：Canvas Studio 微前端工作台、双轨修订与 RBAC 审计
1. **微前端集成与公共能力复用**：
   - 明确 `./App`、`./menu` 导出契约与 `/archdoc/*` 内部路由表（含支持来自 `code-bench` 跳转的源码证据深链路由）；
   - 明确复用 `@code/common` 组件（`Pagination` + `usePagination`、`Drawer`、`ConfirmProvider`、`ToastProvider`、`MemberSearchSelect`、`StatusTag`），严格遵循 `--color-*` Design Tokens 双主题、`.code-archdoc-*` Flat BEM、原生 Vanilla CSS 与 URL 同步 5 页滑动分页（默认 25 条/页）。
2. **四大业务工作台 React 组件分层树**：
   - **仓库与子目录文档覆盖视图（`F-01`）**：筛选栏（关键词/部门/状态/`DocType`）+ URL 同步分页表格 + 生成触发弹窗；
   - **Canvas Studio 双栏协同工作台（`F-08`、`F-09`、`F-10`）**：左侧多 `DocType` 导航树与章节画布（Markdown、表格、Mermaid 缩放/平移、Verifier 告警标记、`auto`/`hybrid`/`locked` 状态切换、孤立章节处理区）；右侧多功能抽屉（只读源码证据抽屉与行高亮、不可变修订历史对比与一键回滚、`W2` SSE Chat 精修面板与章节 Diff `Accept`/`Reject`）；
   - **双维漂移雷达与质量度量看板（`F-12`、`F-26`，`W2`）**：按 `Code Drift`/`Spec Drift` 筛选、章节级受影响详情展开、一键增量刷新，以及按 `DocTypeRevision` 对比的质量指标图表；
   - **管理员治理台**：LLM 分层模型配置与探活页（`F-14`，`W1`）+ `DocType` 注册表在线管理与模板/指令编辑器（`F-03`，`W2`）。
3. **章节级双轨修订状态流与 SSE 协议（`F-09`）**：
   - 统一手工直编（`W1`）与 Chat 精修（`W2`）的修订管线：仅改写目标 `slot_id` 章节（其余章节字节级不变）、乐观版本校验防并发覆盖、保存/`Accept` 均产出不可变 `Revision` 并触发 Verifier 重跑；
   - 定义 Chat 精修 SSE 事件流协议（事件类型、增量 chunk、结构化章节 Diff 负载、错误与取消语义）。
4. **三层 RBAC 权限决策引擎与审计治理（`F-13`）**：
   - 给出精确的权限判定规则与伪代码（匿名 `401`、普通用户只读已发布文档与漂移看板、仓库级 `Repository.OwnerID` / `RelatedMembers`、子目录级额外校验对应 `ArchitectureElement(RepoID, Subdirectory).OwnerID` 且严格阻断跨子目录与整仓越权 `SC-12`、管理员 `archdoc_admin`/`super_admin` 独占全局配置与 `DocType` 管理）；
   - 列出全量写操作的 `commonAudit` 字段级 Diff 审计埋点清单，明文规定 `APIKey` 等凭据字段脱敏后入审计日志。

### 3.5 `05-database-schema.md`：PostgreSQL 物理模型与 `archdoc_*` GORM 建模字典
1. **共享主数据只读契约**：列出只读引用的公共表（`users`、`departments`、`repositories`、`architecture_elements`、`audit_logs`）及关联键规范，严禁对共享表做 DDL 变更或创建冗余仓库主表。
2. **完整 Mermaid ER 图与全量 `archdoc_*` GORM Struct 定义**：
   提供带完整 GORM Tag、字段类型、中文注释、主键、联合唯一索引、JSONB 类型定义、软/硬删除策略与 P1 预留字段（如 `ref_name`）的 Go 结构体代码，覆盖全部核心表：
   - **文档类型索引域**：`archdoc_doc_types`、`archdoc_doc_type_revisions`；
   - **快照与 Code IR 域**：`archdoc_repo_snapshots`、`archdoc_code_ir_facets`（对齐 ADR-02，含 LRU 回收所需 `size_bytes`/`last_accessed_at`）；
   - **任务编排域**：`archdoc_gen_tasks`、`archdoc_gen_slot_tasks`；
   - **文档与修订域**：`archdoc_documents`、`archdoc_document_revisions`、`archdoc_section_evidences`（承载 `slot_id`、`ownership`、`is_orphan`、Verifier 报告摘要与证据锚点）；
   - **协同精修域（`W2`）**：`archdoc_chat_sessions`、`archdoc_chat_messages`；
   - **巡检与漂移域（`W2`）**：`archdoc_repo_remote_heads`、`archdoc_document_drifts`；
   - **治理与观测域**：`archdoc_llm_configs`（含加密 `api_key_cipher`）、`archdoc_llm_usage_logs`、`archdoc_quality_metrics`。
3. **一致性与测试约束**：说明 `AutoMigrate` 初始化顺序、幂等 Seed 规则、被引用 `DocType` 删除保护约束、`APIKey` 对称加密密钥来源规范，以及所有模型在 `code-common/backend/testdb.SetupIsolatedDB(t)` 中零外部依赖建表迁移验证要求。

---

## 4. 阶段二工程质量门禁与实施红线
1. **SSOT 严格一致与全量可追溯**：严禁引入与 `01-feature-matrix.md`（v0.3.1）冲突的自定义方案或突破 `Non-Goals 1~8`（特别注意：内网部署不引入已移除的外发合规白名单；`D-01`~`D-04` 改造不得污染 `code-bench`/`code-common` 通用性）。全部 P0 特性（`F-01`~`F-05`、`F-07`~`F-14`、`F-25`~`F-27`）与 P0 验收场景（`SC-01`~`SC-02`、`SC-04`~`SC-13`）须在设计文档中有明确落点，`F-06`/`SC-03` 等 P1 能力仅明确扩展钩子。
2. **四项待决策选型必须闭环**：不得将 `01-system-architecture.md` §4.5 的 4 项关键技术选型悬空至阶段三，必须在 `docs/05-adr/02-*.md` 与 `03-*.md` 中给出清晰决策、权衡分析与约束后果，并在正文中保持 100% 一致。
3. **清除脚手架占位内容**：重构 `docs/02-design/01-system-architecture.md` 时彻底移除占位模型 `ARCHDOC_ITEMS` / `ARCHDOC_SETTINGS` 与 §4 临时下沉区，确保知识库无新旧冲突。
4. **代码纯粹性红线**：本阶段**严禁新增或修改任何 `.go`、`.tsx` 或 `.css` 业务实现代码**，所有技术细节仅以标准 Markdown、Go Struct 声明、JSON Schema、算法伪代码及规范 Mermaid 图呈现。
5. **文档编号与索引同步**：严格遵守 `docs/02-design/01-`~`05-` 与 `docs/05-adr/02-`~`03-` 双位编号规范；阶段二文档编制完成后，同步更新 [`docs/README.md`](../README.md) 索引目录，并将确定的模块专有架构防腐红线回写至 [`AGENTS.md`](../../AGENTS.md) §2。
