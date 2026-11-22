# ADR-08: 单文档粒度任务编排、Native 引擎加固与 OpenCode 双引擎文档反推架构

- **文档编号**：`08-dual-engine-doc-generation.md`
- **日期**：2026-11-22
- **状态**：已冻结 SDD 设计与 M14 作战卡 (SDD Frozen & Ready for TDD Implementation) —— 已同步更新 `01-features` ~ `04-tasks` 并冻结 [`15-m14-dual-engine-doc-generation.md`](../04-tasks/15-m14-dual-engine-doc-generation.md)，待进入阶段四 TDD 实施
- **关联议题**：[`01-feature-matrix.md`](../01-features/01-feature-matrix.md) (`F-05`, `F-07`, `F-09`, `F-12`, `F-14`, `F-27`, `F-31`, `F-35`, `SC-02`, `SC-04`, `SC-09`, `SC-13`, `SC-18`, `SC-22`)；[`02-extraction-and-generation-engine.md`](../02-design/02-extraction-and-generation-engine.md)；[`05-database-schema.md`](../02-design/05-database-schema.md)；[`01-http-api-contract.md`](../03-api/01-http-api-contract.md)；[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)；[`15-m14-dual-engine-doc-generation.md`](../04-tasks/15-m14-dual-engine-doc-generation.md)；[`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md)；[`ADR-04`](./04-module-scope-discovery-and-hierarchical-docs.md)；[`ADR-06`](./06-document-review-and-refine-studio.md)；[`ADR-07`](./07-single-repo-branch-switch.md)

---

## 1. 背景与问题陈述 (Context & Problem Statement)

### 1.1 现状实现基线

`code-archdoc` 现行 L2 文档生成链路完全构建于自研 OpenAI Compatible HTTP 客户端（`pkg/llm`）与任务编排器（`services/generation_service.go`）之上，关键组件代码基线如下：

| 组件 / 环节 | 现状实现 | 代码位置 |
| :--- | :--- | :--- |
| **任务提交与执行粒度** | `SubmitGenerateTask` 将用户一次勾选的 `1..N` 个 `doc_types` 写入同一条 `GenTask`，在 `executeGenerateTask` 内一次性累加全部文档槽位预算并并发调度所有槽位；`SubmitBatchGenerateTask` 同样按 `(subdir)` 合并多 `doc_types` 创建子任务 | `services/generation_service.go`、`services/batch_generation.go` |
| **生成前预算预检** | `EstimateAndCheckBudget` 遍历所选全部 `DocType` 的活跃槽位累加预估 Token 并对比单模型 `max_tokens_per_task`（默认 200,000）；`countEffectiveFilesForDocTypes` 应用了 `scope_profile`，但紧邻的 `computeTopDirectories` 却直接对全仓未过滤 `IR.Tree.Files` 统计目录体积 | `pkg/llm/budget.go` |
| **Native HTTP 客户端** | `Client.executeWithRetry` 将 `MaxRetries` 硬钳位在 `0..3`（与 `LLMConfigService` 允许的 `0..10` 不一致），为每次尝试派生带超时的 `reqCtx` 并在 `doOnce` 内用 `isTimeoutErr(err) && ctx.Err() == nil` 判定是否可重试；槽位生成调用非流式 `ChatCompletion`（`stream: false`） | `pkg/llm/client.go`、`services/llm_config_service.go` |
| **上下文装配与 Tool Loop** | `serializeSlotFacets` 将 IR 分片序列化后在超限时直接按字节切片 `serialized[:maxChars]`；开启 `enable_source_tools` 的槽位经 `RunToolLoop` 开放 4 个受限只读工具（硬上限 4~6 轮、累计输出 36 KB） | `pkg/llm/prompt.go`、`pkg/llm/tool_loop.go` |
| **单槽位独立重试** | `executeSingleSlotRetry` 直接操作内部 `o.llmSem`（未走 `o.acquireLLMSlot` 也未持 `AcquireRepo`/`AcquireSnapshot` 租约），重跑 `Verifier` 时未向非目标章节回填既有 `CitedAnchors`，且当首次生成全部槽位失败尚未创建 `Document` 时重试单槽位无法落库新修订 | `services/generation_service.go` (`executeSingleSlotRetry`) |
| **数据模型与前端弹窗** | 共有 20 张 `archdoc_*` 表（其中 `archdoc_llm_usage_logs.stage` 列宽为 `varchar(24)`）；前端 `GenerateTaskModal` 未提供执行引擎选项与多任务拆单监控视图 | `models/task.go`、`models/governance.go`、`frontend/src/components/GenerateTaskModal.tsx` |

### 1.2 核心问题与根因剖析 (`P-1` ~ `P-7`)

结合近期真实生产任务（如 `fmt` 仓任务 `#3`、`#5`）故障排查与全链路代码审计，当前文档生成链路存在 **7 项结构性瓶颈与代码缺陷**：

| 编号 | 问题现象 | 严重度 | 根因分析 |
| :---: | :--- | :---: | :--- |
| **P-1** | **多文档混杂于单任务，导致预算误熔断与故障域耦合** | 🔴 | 在 `fmt` 仓同时勾选 5 类 `DocType` 提交任务 `#3` 时，因单任务把 5 篇文档全部槽位的保守预估值叠加至 `205,769 Tokens`，直接越过单模型 `201,000` 上限并触发 `TOKEN_BUDGET_EXCEEDED`（0 次 LLM 调用即整体失败）。事实上拆为单文档后每篇仅 1~5.5 万 Tokens，完全可正常执行；且单任务混跑导致一篇失败或需重试/取消时连带拖累其他文档。 |
| **P-2** | **Native 客户端超时零重试 Bug、重试上限不一致与非流式死等** | 🔴 | ① 在 `pkg/llm/client.go:281-283 & 346-348` 中，`executeWithRetry` 创建了带单次超时 `TimeoutSeconds` 的 `reqCtx` 传入 `doOnce`；当该次请求超时时，`reqCtx.Err()` 必为 `context.DeadlineExceeded`，导致 `doOnce` 内的 `isTimeoutErr(err) && ctx.Err() == nil` **恒为 `false`**。实测 `fmt` 仓任务 `#5` 配置了 `max_retries = 3`，3 个槽位遭遇超时后全部 `attempt_count = 1` 直接失败（0 次重试）；<br>② `LLMConfigService` 与前端允许配置 `max_retries` 为 `0..10`，但 `client.go:252` 静默将其截断为 `3`；<br>③ 槽位生成走非流式请求，缺少首包与空闲心跳检测，上游卡死时只能干等满 `TimeoutSeconds`。 |
| **P-3** | **Push 模式暴力字符截断破坏 JSON 语法，且预算目录统计失真** | 🟠 | ① `serializeSlotFacets`（`pkg/llm/prompt.go:291-294`）在超出 `slot.TokenBudget * 3` 字符时直接硬切片 `serialized[:maxChars] + "...(truncated_by_slot_budget)"`，腰斩 JSON 结构并丢失尾部字段；<br>② `computeTopDirectories`（`pkg/llm/budget.go:97 & 239`）未复用 `countEffectiveFilesForDocTypes` 已按 `DocType.scope_profile`（如 `include_tests: false`）过滤的有效文件集，导致预算建议误报已被过滤的 `test/` 目录；<br>③ 按 `MaxOutputTokens / len(slots)` 均分单槽位输出配额时下限仅 `300`，容易触发长章节截断。 |
| **P-4** | **自研 Function Calling 天花板偏低，缺乏长程上下文管理** | 🔴 | `pkg/llm/tool_loop.go` 硬限制 4~6 轮与 36 KB 累计工具输出，且没有**上下文自动压缩（Compaction）**与**会话断点续跑（Session Continuation）**机制。面对大型 C++/Java/Go 仓库跨多层文件追踪调用链、状态机与并发原语时，有限的 Push 切片与短轮次 Tool Loop 无法支撑高质量深度反推。 |
| **P-5** | **批量编排子任务粒度与单文档理念不统一** | 🟡 | `services/batch_generation.go` 仍按 `(subdir)` 为单位将多 `doc_types` 塞入单个子任务，未能贯彻单文档粒度的状态监控、独立预算与失败重跑边界。 |
| **P-6** | **单槽位独立重试存在租约缺失、证据锚点丢失与全败不可恢复缺陷** | 🟠 | `executeSingleSlotRetry`（`services/generation_service.go:1734-1906`）未获取 `AcquireRepo`/`AcquireSnapshot` 租约、直接写 `o.llmSem` 绕过了 `TaskScheduler.AcquireLLMSlot`；重跑 `Verifier` 时对非目标章节漏传已有 `CitedAnchors` 导致非内联证据锚点丢失；且当首次生成 `ALL_SLOTS_FAILED` 尚未创建 `Document` 时，单槽位重试成功后因 `GetByScope` 返回 `ErrNotFound` 而无法创建文档修订，主任务也永久停留在 `failed`。 |
| **P-7** | **前端缺少任务级引擎选择与多文档拆单队列监控体验** | 🟡 | 新建生成任务弹窗仅能选范围、分支、生成模式与 `DocType`，无法在触发时按仓库复杂度自主选择轻量快速的 `Native` 引擎或深度探索型的 `OpenCode` Agent 引擎，也未承载多文档拆单后的并行任务队列监控与单篇就绪即审阅交互。 |

---

## 2. 决策概览与整体架构 (Decision Summary & Architecture)

### 2.1 异构双引擎整体架构

本 ADR 确立**「任务单文档粒度不变量 + Native 底座加固（第一步）+ OpenCode 深度 Agent 双引擎（第二步）」**的一体化演进方案：

- **职责正交定位**：
  - **Native 引擎（Thin / 直连 OpenAI Compatible）**：主打低延迟、KV Cache 前缀命中、流式交互与强数据脱敏边界，负责日常轻量/常规单文档生成、**Canvas Studio 实时流式 Chat 精修**、以及跨文档复用的 **Snapshot 级共享术语表（Glossary）**。
  - **OpenCode 引擎（Thick Agent / 只读 CLI 会话）**：主打 Pull 模式深度代码理解——下发 L1 Code IR 高层骨架、共享术语表与全篇非锁定槽位契约，由 OpenCode 在受限只读沙箱内自主执行 `read` / `grep` / `glob` / `list` 多轮探索、原生上下文压缩及 `--session` 断点续跑，一次性产出该文档全部非锁定槽位的结构化结果。
- **下游质量与治理完全归一**：无论上游采用 Native 还是 OpenCode，输出均统一汇入既有的 `GenSlotTask` 追踪、`locked` 槽位字节级保护、`MergeGeneratedWithLockedAndOrphans`、独立于 LLM 的**四重确定性 Verifier 门禁**以及不可变 `DocumentRevision` 落库流水线。

```mermaid
flowchart TB
    User["用户新建生成任务 (选 N 篇 DocType + 选择引擎 native/opencode)"] --> Split["POST /api/tasks/generate 两阶段预留并拆单为 N 个单文档 GenTask"]
    Split -->|"1 任务 = 1 Scope x 1 DocType"| Sched["TaskScheduler 双优先级队列 + 单文档预算预检"]
    Sched -->|"Singleflight 共享 L1 Code IR + Glossary"| Router{"按 task.engine 路由"}

    Router -->|"engine = native (Thin)"| NativePath["Native 引擎：按槽位并发 + 流式自适应聚合 + 三层超时重试 + 受限只读 ToolLoop"]
    Router -->|"engine = opencode (Thick)"| OpenCodePath["OpenCode 引擎：单文档单只读会话 + 自主探索 + 上下文压缩 + --session 续跑"]

    NativePath --> SlotSync["统一回填 archdoc_gen_slot_tasks (locked 槽位豁免)"]
    OpenCodePath --> SlotSync
    SlotSync --> Verifier["独立四重 Verifier 门禁 (符号/完备度/Mermaid/凭据硬阻断)"]
    Verifier --> Rev["不可变 DocumentRevision + QualityMetric + LLMUsageLog"]

    ChatUI["Canvas Studio 实时流式 Chat 精修"] -->|"固定低延迟路径"| NativeChat["Native 流式引擎 (StreamSplitFilter)"]
```

### 2.2 候选方案评估

| 候选方案 | 核心做法 | 优点 | 缺点与风险 | 结论 |
| :--- | :--- | :--- | :--- | :---: |
| **方案 A：单文档拆单 + Native 加固 + OpenCode 文档级会话双引擎** | 无论勾选几篇文档或批量任务，一律按 `(Scope, DocType)` 拆单为独立任务；修复 Native 超时重试/流式超时/JSON 裁剪/单槽位重试；引入 `pkg/opencode` 按**单文档单会话**执行并统一汇入槽位表与 Verifier；前端新建任务显式可选引擎。 | 彻底解决多文档预算误熔断与故障域耦合；兼顾 Native 低延迟（Chat/Glossary/常规文档）与 OpenCode 深度代码探索能力；单文档会话避免按槽位重复扫仓造成 5 倍成本膨胀；100% 复用既有锁定与 Verifier 体系。 | 需同时推进 Native 修 Bug、OpenCode CLI 进程治理及少量表列演进，分 `M14-A/B/C` 渐进交付。 | ✅ **采纳** |
| **方案 B：仅优化自研 Native 引擎与 Tool Loop，放弃 OpenCode** | 仅修复 `client.go` 与 `budget.go`，继续扩写自研 `tool_loop.go` 支持更多轮次与上下文摘要压缩。 | 无需引入外部 CLI 进程与配置桥接。 | 在复杂 C++/Java 大仓跨文件追查上仍受限于自研轮次上限、缺乏成熟上下文压缩与工具生态，长期重复造轮子且文档深度仍不足。 | ❌ **放弃** |
| **方案 C：全量替换为 OpenCode，并按 Slot 逐个拉起 OpenCode 进程** | 废弃 Native 生成路径，每篇文档的每个槽位各启动一个 `opencode run` 进程。 | 无需维护双引擎路由，代码表面最简。 | 5 槽位文档会拉起 5 个独立进程重复读同一代码仓，耗时与 Token 成本倍增；且无 OpenCode 环境或高敏隔离场景失去兜底；Chat 实时流式体验也会严重退化。 | ❌ **放弃** |

### 2.3 核心决策摘要 (`D-1` ~ `D-5`)

| 决策 | 核心要点 | 解决问题 |
| :--- | :--- | :--- |
| **D-1 单文档任务不变量与无竞态自动拆单** | 确立 `1 GenTask = 1 Scope × 1 DocType` 硬不变量；`POST /api/tasks/generate` 勾选 `N` 篇文档时通过 `TaskScheduler.ReserveInteractiveSlots(N)` + DB 事务 `CreateTasksBatch` + `Commit` 原子创建并入队 `N` 个独立任务（响应体向下兼容首任务字段并扩展 `tasks[]`）；`generate-batch` 按 `(subdir, doc_type)` 拆分子任务并保持 `ModuleBreakdown` 按模块范围聚合兼容；复用 `SnapshotService` `singleflight` 与共享 Glossary 零重复抽取。 | P-1、P-5 |
| **D-2 Native 引擎底座全面加固（第一步）** | 修复 `client.go` `reqCtx` 遮蔽导致超时零重试的 Bug；重试上限对齐 `LLMConfigService` 的 `0..10`；升级为基于可注入 `baseBackoff` 的随机抖动指数退避（含 `Retry-After`）；槽位生成升级内部 `stream: true` 聚合 + `Content-Type` 双模自适应解析 + 首包/空闲/总超时三层护栏；`budget.go` 统一按 `scope_profile` 过滤 `TopDirectories`；`serializeSlotFacets` 改用三级结构化裁剪保持 100% 合法 JSON；修复 `executeSingleSlotRetry` 租约、信号量、非目标槽位证据保留与全败文档恢复。 | P-2、P-3、P-6 |
| **D-3 OpenCode 深度 Agent 引擎架构（第二步）** | 新增纯领域包 `pkg/opencode/`；支持模式 A（零配置自动桥接 `archdoc_llm_configs` 至临时 `0600` `OPENCODE_CONFIG`）与模式 B（原生 `provider/model`）；单文档单只读会话（禁写/禁 Shell + 全套 `XDG_*`/`CI=true`/`NO_COLOR=1` 隔离 + `--file` 大 Prompt + `runtime.LockOSThread` & `Setpgid` 进程组强杀 + 8MB JSONL 缓冲与环形 stderr + Idle 看门狗）；支持 Monorepo 子目录相对路径对齐、防重 H2 标题剥离、Usage 归一化及 `--session` 断点续跑。 | P-4、P-6 |
| **D-4 数据模型增量、配置治理与双引擎 API 契约** | 保持 20 张 `archdoc_*` 表零增表，向 `archdoc_gen_tasks` / `archdoc_llm_usage_logs` 增补 `engine` 列（同步将 `archdoc_llm_usage_logs.stage` 扩至 `varchar(32)`）及 `archdoc_llm_configs` 增补可选 `opencode_model` 列；`config.yaml` 新增 `opencode:` 配置段与独立 `opencodeSem` 限流器；新增 `GET /api/engines` 与 `ENGINE_UNAVAILABLE` 错误码，扩展生成/批量/增量刷新/任务查询契约。 | P-4、P-7 |
| **D-5 前端新建任务引擎选择器与多任务拆单视图** | `GenerateTaskModal` 增加「执行引擎」分段选择器（实时探测可用性 + 记忆偏好 + 只读/耗时提示）；勾选多篇文档时展示「创建 N 个文档生成任务」与多任务增量轮询队列视图（支持单篇就绪即刻跳转工作台审校）；`TaskCenterPage` 展示引擎徽标与筛选器；`AdminLLMConfigPage` 增加可选 `opencode_model` 映射字段。 | P-5、P-7 |

---

## 3. 决策 D-1：单文档任务不变量与多文档自动拆单

### 3.1 单文档任务不变量

无论是 `Native` 还是 `OpenCode`，系统确立统一的任务领域不变量：

1. **单文档唯一绑定**：每一个非批量父任务（`task_kind` 为 `generate`、`regenerate` 或 `incremental_refresh`）严格对应 **1 个目标范围 `(repo_id, subdirectory, ref_name)` × 1 个 `doc_type_slug`**，其 `doc_types_json` 数组长度固定为 `1`。
2. **故障域与重试边界隔离**：`EstimateAndCheckBudget` 仅对单篇文档计算文件数与 Token 预算，彻底消除多文档累加误熔断；若某仓库需生成 5 篇文档，每篇拥有独立的任务 ID、进度条、取消开关、失败原因与重跑生命周期，互不拖累。
3. **共享缓存零重复计算（`SC-02` 不变量）**：同一批次拆出的 `N` 个单文档任务在进入 `extracting` 阶段时，并发命中 `SnapshotService.EnsureSnapshot` 的进程内 `singleflight`（键 `base:<repo_id>|<branch>` 与 `slice:<repo_id>|<subdir>|<commit_sha>`）与 `EnsureGlossary` 的 `glossarySF` 缓存，**Git 同步、L1 Code IR 抽取与全仓共享术语表仍严格只执行 1 次**。

### 3.2 `POST /api/tasks/generate` 两阶段预留无竞态拆单与向下兼容契约

为保持既有 API 调用方与单元测试零破坏，同时彻底消除“半数入库、半数队列满丢弃”或“Worker 在 DB 事务提交前抢跑查不到 `task.ID`”的并发竞态：

1. **请求入参扩展（`GenerateTaskRequest`）**：
   - 保留 `doc_types: string[]`，并新增可选 `doc_type?: string`（两者按出现顺序合并去重，满足 `len(dedupSlugs) >= 1`）与可选 `engine?: "native" | "opencode"`（留空时取服务端 `config.AppConfig.OpenCode.EffectiveDefaultEngine()`）。
2. **两阶段队列容量预留 + DB 批量事务 + 原子入队提交**：
   - **步骤 1（前置校验与队列容量预留）**：完成仓库解析、全部 `dedupSlugs` 启用态校验、分支校验与引擎可用性校验后，调用 `TaskScheduler.ReserveSlots(models.TaskPriorityInteractive, len(dedupSlugs))`：
     - `TaskScheduler` 在互斥锁内检查 `len(s.highQ) + s.reservedHigh + n <= s.cfg.InteractiveQueueCap`；
     - 若容量不足，直接返回 `ErrQueueFull`（HTTP `429 QUEUE_FULL`），**此时数据库尚未写入任何记录（0 脏数据）**；
     - 若容量充足，原子递增 `s.reservedHigh += n` 并返回 `commit(jobs []ScheduledJob)` 与 `rollback()` 闭包。
   - **步骤 2（数据库单事务批量创单）**：调用 `TaskRepo.CreateTasksBatch(ctx, tasks)` 在单个 DB 事务内插入 `N` 条单文档 `GenTask`（每条 `DocTypesJSON` 仅含单个 `[slug]`，`Engine` 为解析后的引擎标识）；若 DB 事务失败，立即调用 `rollback()` 释放预留槽位并返回错误。
   - **步骤 3（注册取消句柄并提交入队）**：DB 事务成功提交、全部 `task.ID` 就绪后，为每个 `task.ID` 注册 `taskCtx, cancel` 并调用 `commit(jobs)` 将 `N` 个 `ScheduledJob` 一次性推入 `highQ`、扣减 `s.reservedHigh` 并唤醒分发协程。
3. **响应体向下兼容结构（`GenerateTaskSubmitResponse`）**：
   ```json
   {
     "id": 101,
     "repo_id": 167,
     "subdirectory": "",
     "ref_name": "main",
     "task_kind": "generate",
     "engine": "opencode",
     "doc_types": ["system-architecture"],
     "status": "pending",
     "created_count": 3,
     "tasks": [
       { "id": 101, "engine": "opencode", "doc_types": ["system-architecture"], "status": "pending" },
       { "id": 102, "engine": "opencode", "doc_types": ["code-walkthrough"], "status": "pending" },
       { "id": 103, "engine": "opencode", "doc_types": ["agents-context"], "status": "pending" }
     ]
   }
   ```
   - `GenerateTaskSubmitResponse` 顶层匿名内嵌 `GenTaskDetailVO`（取自首条任务 `tasks[0]`，使现有仅解析 `resp.id` 的单测与客户端 100% 兼容），同时携带 `created_count: len(tasks)` 与完整 `tasks: []GenTaskDetailVO` 数组供前端展示拆单队列。
   - 存量多文档联合生成 E2E 测试（如 `handlers/e2e_scenarios_test.go` 中同时传 `["system-architecture", "data-and-contract"]` 的场景）只需遍历 `resp.Tasks` 等待全部拆单任务进入终态，即可确定性验证拆单与 `SC-02` 共享缓存。

### 3.3 `POST /api/tasks/generate-batch` 对齐单文档子任务

1. `BatchGenerateRequest` 新增可选 `engine?: "native" | "opencode"`，记录于批量父任务及所有派发子任务的 `Engine` 字段。
2. `executeBatchGenerateTask` 拆单调整：
   - **阶段 1（子模块）**：为每个 `(subdir, slug)` 组合创建 1 个单文档子任务（共 `len(moduleSubdirs) × len(slugs)` 个子任务）；
   - **阶段 2（整仓上卷）**：在所有阶段 1 子任务进入终态后，若勾选 `include_repo_scope`，为每个 `("", slug)` 创建 1 个整仓单文档子任务（共 `len(slugs)` 个子任务）；
   - 当 `len(slugs) == 1` 时，子任务总数仍为 `len(moduleSubdirs) + (includeRepoScope ? 1 : 0)`，与存量 `handlers/module_batch_test.go` 断言 100% 兼容。
3. **批前总预算预检与 `ModuleBreakdown` 兼容契约**：
   - `estimateBatchTokens` 汇总全部 `(subdir, slug)` 及整仓文档的预估 Token 总和对照 `scheduler.max_tokens_per_batch`（默认 `1,000,000`）；
   - `BudgetAdvice.ModuleBreakdown` 继续按目标范围 `subdirectory`（各子模块 + 整仓 `""`）聚合展示每个范围下所选文档的总预估 Token 与有效文件数，保持结构紧凑且与存量测试断言完全兼容。

---

## 4. 决策 D-2：Native 引擎底座全面加固（第一步）

### 4.1 修复超时零重试 Bug、对齐重试上限与升级退避机制 (`pkg/llm/client.go`)

1. **严格区分父级取消与单次 Attempt 超时**：
   - 在 `executeWithRetry` 中保留调用方传入的 `parentCtx`，为每次请求尝试创建独立的 `attemptCtx, cancel := context.WithTimeout(parentCtx, attemptTimeout)`，并将 **`parentCtx` 与 `attemptCtx` 一并传入 `doOnce`**（或在 `doOnce` 返回后基于 `parentCtx.Err()` 判定）；
   - 当 HTTP 请求或流读取返回错误时：
     - 仅当 **`parentCtx.Err() != nil`** 时，判定为调用方主动取消（如用户点击取消任务、服务停机）或父级总超时，此时 `retryable = false` 立即退出；
     - 当 **`parentCtx.Err() == nil`** 且错误属于 `attemptCtx.Err() == context.DeadlineExceeded`、`isTimeoutErr(err)`、首包/空闲看门狗超时、`io.EOF` / `io.ErrUnexpectedEOF`、`connection reset by peer` / `broken pipe` 或 HTTP `429` / `500` / `502` / `503` / `504` 时，一律判定为 **`retryable = true`** 并进入下一次重试。
2. **重试上限对齐 `0..10` 与可注入 `baseBackoff` Jitter 指数退避**：
   - 将 `client.go` 中 `maxRetries` 的硬钳位上限由 `3` 调整为 **`10`（合法范围 `0..10`，默认值 `3`）**，与 `services/llm_config_service.go` 及前端 `AdminLLMConfigPage.tsx` 的 `0..10` 校验规则完全统一；
   - 退避间隔计算采用客户端可注入基准 `c.baseBackoff * (1 << attempt)`（`NewClient()` 默认 `baseBackoff` 设为 `1 * time.Second`，单测通过 `NewClientWithOptions(httpClient, 10*time.Millisecond)` 注入小基准时仍保持毫秒级极速执行），叠加 `±20%` 随机抖动因子（`[0.8, 1.2]`）并限制单次最大退避上限 `30s`；
   - 若上游 HTTP `429` / `503` 携带整数秒或 HTTP-date 格式的 `Retry-After` 响应头，在不超过 `30s` 上限且响应 `parentCtx.Done()` 的前提下优先采用 `Retry-After` 指定的退避时长。

### 4.2 槽位生成内部流式聚合、`Content-Type` 自适应双模解析与三层超时看门狗

1. **槽位生成内部流式读取与 `Content-Type` 双模自适应兼容**：
   - 槽位生成（`executeSlotLLM` 及 `RunToolLoop` 内部轮次）底层支持以 `stream: true` 发起请求并聚合完整 `ChatResponse`；
   - **`Content-Type` 双模自适应解析（保障存量 `httptest` 与非流式网关 100% 零破坏）**：
     - 当 `doOnce` 收到 HTTP `200 OK` 时，检查响应 `Content-Type`（及首非空字节）：
       1. 若 `Content-Type` 包含 `text/event-stream`（或首非空行以 `data:` / `:` / `event:` 开头），进入 `parseSSEResponse` 流式增量聚合；
       2. 若响应为 `application/json`（或首非空字节为 `{`，例如忽略 `stream: true` 直接返回完整 JSON 的内网网关及项目中全部存量 `httptest.Server` 测试桩），**直接走同步 JSON 解析分支**（若 `onDelta != nil` 且解析出非空 `Content` 则回调一次 `onDelta(Content)`），无需报错或额外重发请求；
   - **流式参数不支持自动降级**：若上游网关对 `stream: true` 返回 HTTP `400` / `422` / `501` 且响应体提及 `stream` 不支持，自动降级为 `stream: false` 重发。
2. **三层超时看门狗（`EndpointConfig` 扩展 `FirstByteTimeoutSeconds` 与 `IdleTimeoutSeconds`）**：
   - **首包超时 (`first_byte_timeout_seconds`，默认 `90s`)**：从发出 HTTP 请求到收到响应头及首个有效响应字节的最大等待时间（若 `TimeoutSeconds > 0` 且小于该值，自动取 `min(FirstByteTimeoutSeconds, TimeoutSeconds)`）；超时主动取消当前 attempt 并标记 `retryable = true`；
   - **流式空闲超时 (`idle_timeout_seconds`，默认 `60s`)**：在 `parseSSEResponse` 读取流期间，通过活跃计时器监控相邻两帧数据间隔（自动取 `min(IdleTimeoutSeconds, TimeoutSeconds)`）；一旦超过空闲阈值无任何字节到达（模型推理僵死），主动关闭/取消当前 attempt `reqCtx` 并触发重试；
   - **单次总超时 (`timeout_seconds`，默认 `180s`，取自模型配置)**：单次请求 attempt 的总墙钟时间封顶。
3. **输出长度截断保护**：
   - 在 `GenerationOrchestrator` 为槽位计算 `MaxTokens` 时，当 `DocType.Meta.MaxOutputTokens > 0` 时，单槽位输出配额取 `max(DocType.Meta.MaxOutputTokens / len(slots), 4096)`（取代原先偏小的 `perSlot`），并在 `resp.FinishReason == "length"` 时打印结构化告警日志。

### 4.3 单文档预算校准与三级结构化合法 JSON 裁剪 (`pkg/llm/budget.go` & `pkg/llm/prompt.go`)

1. **`EstimateAndCheckBudget` 统一 `scope_profile` 过滤口径 (`pkg/llm/budget.go`)**：
   - 将 `countEffectiveFilesForDocTypes` 重构为 `filterEffectiveFilesForDocTypes(ir *codeir.CodeIRBundle, docTypes []*doctype.AssetBundle) []codeir.FileSummary`：
     - 遍历 `docTypes`，通过 `sandbox.FilterByScopeProfile(walked, dt.Meta.ScopeProfile.ToSandboxProfile())` 求取当前任务有效文件并集；
     - `effectiveFiles` 与 `computeTopDirectories(effectiveFileSummaries, 5)` **共同基于过滤后的有效文件列表计算**，彻底消除 `include_tests: false` 已排除 `test/` 目录却仍在 `TopDirectories` 预算建议中刷屏的失真问题。
2. **三级结构化合法 JSON 裁剪替代字符串硬截断 (`pkg/llm/prompt.go`)**：
   - 重构 `serializeSlotFacets(ir *codeir.CodeIRBundle, facetNames []string, tokenBudget int) string`，**彻底删除 `serialized[:maxChars] + "...(truncated_by_slot_budget)"` 字符串硬切片**，通过三级结构化降维保证任意预算下返回值 **100% 满足 `json.Valid([]byte(out)) == true`**：
     - **Level 1（剥离内部派生与大体积文件明细）**：当初始 JSON 超出 `maxChars = effectiveTokenBudget * 3` 时，首先清空 `Deps.FileImports`（该字段仅供 `SliceByModule` 内部推导）与 `Tree.Files`（保留 `Tree.Directories`、`TotalFiles`、`TotalLOC`、`LanguageStats`）；
     - **Level 2（按事实优先级收缩大列表切片）**：若仍超出 `maxChars`，按优先级从低到高依次对 `RuntimePrimitives.Primitives`、`Symbols.Symbols`、`DataModels.Entities`、`ConfigKeys.Keys`、`HTTPRoutes.Routes`、`IDLContracts.Protos`、`Deps.ExternalDeps`、`Build.Targets`、`Tree.Directories` 按比例缩减列表元素数量，并在 JSON 顶层注入元数据字段：
       ```json
       "_meta": {
         "truncated": true,
         "budget_chars": 24000,
         "omitted_counts": { "symbols": 142, "runtime_primitives": 68 }
       }
       ```
     - **Level 3（极端小预算最小合法 JSON 兜底）**：即便在单测传入极小 `tokenBudget` 的极端场景下，也至少返回合法的 `{"_meta":{"truncated":true,"budget_chars":...}}` 对象；
   - 当 `EnableSourceTools == true` 时，`sourceToolsGuidancePrompt` 明确引导模型：当 `slot_ir_facets` 中包含 `"_meta": {"truncated": true}` 时，可优先调用 `get_symbol_impl` / `grep_in_sandbox` / `read_file_lines` 按需检索未内联符号。

---

## 5. 决策 D-3：OpenCode 深度 Agent 引擎架构（第二步）

### 5.1 纯领域包 `pkg/opencode/` 分层设计

严格遵守 `code-archdoc/AGENTS.md` 与 `make lint-arch` 架构红线（`pkg/*` 严禁导入 `models`、`services`、`handlers`、`gorm`、`gin`；严禁在 `pkg/` 中调用 `gitsync.Sync`）：

```text
pkg/opencode/
├── runner.go          # Runner 接口、DocGenRequest/DocGenResult/SlotOutput/ErrorClass 与 30s TTL CLI 探活缓存
├── config_bridge.go   # 双模式临时配置目录生成（0700 隔离根 + 0600 opencode.json + 全套 XDG 隔离目录）
├── process.go         # 子进程生命周期管理（LockOSThread + Setpgid 强杀、--file 大 Prompt、环形 stderr、Idle 看门狗）
├── event_parser.go    # OpenCode --format json 8MB 缓冲 JSONL 事件流解析（sessionID / final text / Usage 归一化 / 错误分级）
├── prompt.go          # 单文档多槽位任务规约构造器、防重 H2 标题剥离与多格式容错 JSON 解析器
└── opencode_test.go   # 基于 Fake `opencode` 脚本与临时沙箱的全量零外部依赖单测
```

核心契约定义：

```go
package opencode

type ErrorClass string

const (
	ErrorClassNone             ErrorClass = ""
	ErrorClassCanceled         ErrorClass = "canceled"
	ErrorClassTimeout          ErrorClass = "timeout"
	ErrorClassIdleTimeout      ErrorClass = "idle_timeout"
	ErrorClassRateLimited      ErrorClass = "rate_limited"
	ErrorClassAuth             ErrorClass = "auth"
	ErrorClassNetworkTransient ErrorClass = "network_transient"
	ErrorClassContentFiltered  ErrorClass = "content_filtered"
	ErrorClassOutputMissing    ErrorClass = "output_missing"
	ErrorClassCLINotFound      ErrorClass = "cli_not_found"
	ErrorClassUnknown          ErrorClass = "unknown"
)

type SlotTaskSpec struct {
	SlotID          string
	Title           string
	Ownership       string
	InstructionHint string
	IRFacets        []string
	PriorHybridMD   string
	ReviewGuidance  []string
}

type DocGenRequest struct {
	WorkDir                string                  // 沙箱作用域物理目录（已通过 SafeJoin 校验的 repoRoot 或 repoRoot/subdir）
	RepoRoot               string                  // 仓库根物理目录
	Subdirectory           string                  // 规范化子目录（整仓为空串）
	DocTypeSlug            string
	DocTypeDisplayName     string
	TemplateMD             string
	GenerationPromptMD     string
	ActiveSlots            []SlotTaskSpec          // 仅含非 locked 待生成槽位（locked 已在编排层豁免）
	Glossary               *codeir.GlossaryCache
	IR                     *codeir.CodeIRBundle
	ModuleContext          *codeir.ModuleContextFacet
	ModuleDigests          []codeir.ModuleDigestInput
	CrossModuleGraph       []codeir.CrossModuleEdge
	ModelName              string                  // 对应 LLMConfig.ModelName
	BaseURL                string                  // 模式 A 注入 @ai-sdk/openai-compatible
	APIKey                 string                  // 仅写入 0600 临时配置文件，绝不进 argv/日志
	UserAgent              string
	OpenCodeModelOverride  string                  // 可选模式 B：provider/model
	TimeoutSeconds         int
	IdleTimeoutSeconds     int
	ContinuationEnabled    bool
	ContinuationMaxSeconds int
}

type SlotOutput struct {
	Markdown     string
	CitedAnchors []verifier.CitedAnchorCandidate
}

type DocGenResult struct {
	SessionID         string
	Slots             map[string]SlotOutput // slot_id -> output
	MissingSlotIDs    []string              // 模型漏填或未产出的槽位 ID
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	LatencyMs         int64
	Continuations     int
	TerminationReason string
}
```

### 5.2 双模式模型配置桥接与进程级只读隔离 (`config_bridge.go`)

借鉴 `code-shield/services/invoker/opencode.go` 的工程经验并结合 `code-archdoc` 动态模型池特性，彻底消除部署配置门槛与并发 SQLite 锁冲突：

1. **双模式模型映射（兼容现有 `archdoc_llm_configs` 零门槛开箱即用）**：
   - **模式 A（默认自动桥接模式 — 当 `LLMConfig.OpenCodeModel == ""`）**：
     - 调用 `os.MkdirTemp("", "archdoc-oc-*")` 创建权限为 `0700` 的本次会话专属隔离根目录 `<tmpDir>`（函数退出时由 `defer os.RemoveAll(tmpDir)` 强制销毁）；
     - 根据 `Probe()` 识别的 CLI 主版本自适应写入 `<tmpDir>/opencode.json`（权限 `0600`）：
       - **OpenCode v1.x**：写入 `provider.archdoc_ep`（`npm: "@ai-sdk/openai-compatible"`, `options`, `models`）与 `agent.archdoc-readonly-docgen`（`tools` + `permission`），启动参数：`opencode run --format json --pure --agent archdoc-readonly-docgen --model archdoc_ep/<model_name> --dir <WorkDir> ...`；
       - **OpenCode v2.x+**：写入 `"update": "disable"`、原生 `providers.archdoc_ep`（`package: "aisdk:@ai-sdk/openai-compatible"`, `settings`, `headers`, `models`）与原生有序 `agents.archdoc-readonly-docgen.permissions` 数组，启动参数：`opencode run --standalone --format json --agent archdoc-readonly-docgen --model archdoc_ep/<model_name> --archived --title archdoc-<slug> ... -- <prompt>`（通过 `cmd.Dir` 与 `PWD=<WorkDir>` 双重锁定工作目录）；
     - **优势**：管理员无需登录服务器手工维护 `~/.config/opencode/` 或 `auth.json`；**明文 API Key 仅存在于受 `0600` 保护的短命临时文件中，绝对不会出现在 `ps` 命令行参数（`argv`）、环境变量或系统日志中**。
   - **模式 B（可选原生 Provider 直连 — 当 `LLMConfig.OpenCodeModel != ""`）**：
     - 直接以 `--model <OpenCodeModel>` 启动，向临时 `<tmpDir>/opencode.json` 写入只读 Agent 配置（v2.x 下额外从宿主机配置按 `<provider>` 精准提取对应 `providers[<provider>]` 条目，严禁拷贝无关 `agents`/`plugins`），并将宿主机 `~/.local/share/opencode/auth.json`（若存在）拷贝为 `<tmpDir>/xdg_data/opencode/auth.json`（权限 `0600`）。
2. **全套环境变量与 XDG 状态隔离**：
   - 子进程环境仅继承白名单基础系统变量（`PATH`、`HOME`、`USER`、`LANG`、`LC_ALL`、`TMPDIR`、`SSL_CERT_FILE`、`SSL_CERT_DIR`、`HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`），并显式覆写：
     - `OPENCODE_CONFIG=<tmpDir>/opencode.json`
     - `OPENCODE_CONFIG_DIR=<tmpDir>/xdg_config/opencode`
     - `XDG_CONFIG_HOME=<tmpDir>/xdg_config`
     - `XDG_DATA_HOME=<tmpDir>/xdg_data`
     - `XDG_STATE_HOME=<tmpDir>/xdg_state`
     - `XDG_CACHE_HOME=<tmpDir>/xdg_cache`
     - `CI=true`、`NO_COLOR=1`、`TERM=dumb`（彻底禁用交互式 TUI 确认阻塞与 ANSI 颜色控制符，且使每个会话拥有独立 SQLite 库，根治多进程并发 `database is locked`）；
     - `OPENCODE_DISABLE_PROJECT_CONFIG=1`、`OPENCODE_CONFIG_PROJECT_DISABLE=1`、`OPENCODE_DISABLE_CLAUDE_CODE=1`、`OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1`、`OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1`、`OPENCODE_DISABLE_AUTOUPDATE=1`、`OPENCODE_DISABLE_MODELS_FETCH=1`、`OPENCODE_FILEWATCHER_DISABLE=1`、`OPENCODE_DISABLE_FILEWATCHER=1` 与 `PWD=<WorkDir>`（v2.x 替代 `--pure` 与 `--dir` 实现严格项目隔离与工作目录对齐）。
3. **强制进程级只读权限矩阵（不修改宿主机全局配置）**：
   - **OpenCode v1.x** (`agent.archdoc-readonly-docgen`)：
     ```json
     {
       "$schema": "https://opencode.ai/config.json",
       "agent": {
         "archdoc-readonly-docgen": {
           "description": "Code-Archdoc Read-Only Documentation Reverse-Engineering Agent",
           "tools": {
             "read": true, "grep": true, "glob": true, "list": true,
             "edit": false, "write": false, "patch": false, "bash": false, "webfetch": false
           },
           "permission": {
             "*": "deny",
             "read": "allow", "grep": "allow", "glob": "allow", "list": "allow",
             "edit": "deny", "write": "deny", "bash": "deny", "webfetch": "deny",
             "external_directory": "deny"
           }
         }
       }
     }
     ```
   - **OpenCode v2.x+** (`agents.archdoc-readonly-docgen.permissions`，因 v2 采用 `findLast` 后匹配优先，`* : deny` 必须位于首位，随后依次放行只读工具并覆写禁用 `*.env`/`edit`/`shell`/`webfetch`/`websearch`/`subagent`/`skill`/`browser`/`question`/`external_directory`)：
     ```json
     {
       "$schema": "https://opencode.ai/config.json",
       "update": "disable",
       "agents": {
         "archdoc-readonly-docgen": {
           "description": "Code-Archdoc Read-Only Documentation Reverse-Engineering Agent",
           "mode": "primary",
           "permissions": [
             {"action": "*", "resource": "*", "effect": "deny"},
             {"action": "read", "resource": "*", "effect": "allow"},
             {"action": "read", "resource": "*.env", "effect": "deny"},
             {"action": "read", "resource": "*.env.*", "effect": "deny"},
             {"action": "read", "resource": "*.env.example", "effect": "allow"},
             {"action": "grep", "resource": "*", "effect": "allow"},
             {"action": "glob", "resource": "*", "effect": "allow"},
             {"action": "list", "resource": "*", "effect": "allow"},
             {"action": "edit", "resource": "*", "effect": "deny"},
             {"action": "shell", "resource": "*", "effect": "deny"},
             {"action": "webfetch", "resource": "*", "effect": "deny"},
             {"action": "websearch", "resource": "*", "effect": "deny"},
             {"action": "subagent", "resource": "*", "effect": "deny"},
             {"action": "skill", "resource": "*", "effect": "deny"},
             {"action": "browser", "resource": "*", "effect": "deny"},
             {"action": "question", "resource": "*", "effect": "deny"},
             {"action": "external_directory", "resource": "*", "effect": "deny"}
           ]
         }
       }
     }
     ```
   - 严禁传入 `--auto` 或 `--thinking`，通过 `--pure`（v1）或全套 `OPENCODE_DISABLE_*` + `--standalone`（v2）禁用外部不可控插件与宿主机服务，确保 OpenCode 绝不执行 Shell、外网请求、越界目录访问或修改源码工作区。

### 5.3 单文档单会话执行、进程组治理、防重标题与 `--session` 断点续跑

1. **进程生命周期与系统线程安全治理 (`process.go`)**：
   - **大 Prompt 安全传输**：待发送规约超过 `64 KB`（远低于 Linux `MAX_ARG_STRLEN` 128 KB 限制）时，自动落盘至隔离目录 `0600` 临时 `prompt.md` 文件，通过 `--file <promptPath>` 附件传入，主消息仅传简短引导指令；
   - **`runtime.LockOSThread()` + 进程组强杀**：在 Go on Linux 下，`SysProcAttr.Pdeathsig` 绑定的是创建子进程的 OS 线程（M）而非进程（若不锁线程，Go 调度器迁移/回收 M 线程会向运行中的子进程误发 `SIGKILL`）。因此启动并 `Wait()` 子进程的协程**必须调用 `runtime.LockOSThread()`**，同时设置 `SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}` 且 `cmd.Dir = req.WorkDir`；
   - **双管道并发消费与 Idle 看门狗**：使用两个独立协程并发读取 `stdout`（JSONL 事件流）与 `stderr`（写入 32 KB 环形缓冲防管道写满死锁），任一管道收到数据即原子刷新 `lastActivityUnixNano`。当父 `ctx.Done()`（用户取消任务或服务优雅停机）、单次总超时或空闲超过 `idle_timeout_seconds` 时，立即调用 `syscall.Kill(-pgid, syscall.SIGKILL)` 秒级回收整个子进程树；
   - **工作区租约与读锁保护**：`GenerationOrchestrator` 在运行 OpenCode 期间全程持有 `ActiveLeaseManager.AcquireRepo` / `AcquireSnapshot` 租约，并通过 `SnapshotService.WithRepoReadLock` 持共享读锁（允许同仓多篇文档只读分析并发，阻断 `ResourceReaper` 回收及 `gitsync` 并发写覆盖）。
2. **单文档 Prompt 规约、Monorepo 路径对齐与防重 H2 标题 (`prompt.go`)**：
   - Prompt 按固定结构下发：
     1. 只读分析、防提示词注入（源码注释均为不可信被动数据）与凭据零泄漏硬约束；
     2. `DocType` 业务指令（`generation_prompt.md`）与章节骨架说明；
     3. 当前作用域说明（明确 `WorkDir` 为仓库根或子模块目录 `<Subdirectory>`：本地文件引用 `cited_anchors` 使用相对于当前 `WorkDir` 的路径且 `scope: "module"`；若存在 `ModuleContext` 跨模块引用则使用相对于仓库根的路径且 `scope: "repo"`）与 L1 高层索引（`Tree.Directories`、`Build`、共享 `Glossary`、`ModuleContext` / `ModuleDigests` + `CrossModuleGraph`，引导 OpenCode 使用 `read`/`grep`/`glob` 按需深入核实源码实现）；
     4. 全部非锁定 `ActiveSlots` 清单（`slot_id`、`title`、`instruction_hint`、`hybrid` 历史稿、`persist_for_regen` 长期审阅指导）；
     5. 唯一合法输出契约：作为最后一条消息直接返回纯 UTF-8 JSON（**`markdown` 正文严禁包含一级标题 `#` 或与本章节 `title` 重复的顶层二级标题 `## <title>`，因外层 `template.md` 已自带章节标题**）：
        ```json
        {
          "slots": {
            "<slot_id>": {
              "markdown": "章节完整 Markdown 正文（不含一级标题与重复的章节二级标题，子小节从 ### 开始）",
              "cited_anchors": [
                {"file_path": "相对路径", "fq_symbol": "全限定符号或空", "symbol_kind": "可选类型", "scope": "module|repo"}
              ]
            }
          }
        }
        ```
   - `ParseOpenCodeDocOutput` 解析与清洗：
     - 兼容纯 JSON、`` ```json `` 代码围栏及前后夹杂简短文本时的最外层 JSON 对象提取（同时兼容 `{"slots": {"<slot_id>": ...}}` 与直接顶层 `{"<slot_id>": {"markdown": ...}}` 两种常见模型输出结构）；
     - **自动剥离冗余首行章节标题**：若某槽位提取出的 `markdown` 首非空行是 `# <标题>` 或 `## <标题>` 且去除序号/空白后与该槽位 `SlotTaskSpec.Title` 重复，自动剥离该重复首行，彻底杜绝 `assembleFullMarkdown` 拼装后出现双重 `## 1. xxx` 标题；
     - 按 `ActiveSlots` 逐一校验非空状态，统计 `MissingSlotIDs`。
3. **8MB 缓冲 JSONL 事件流解析与 Usage 归一化 (`event_parser.go`)**：
   - 使用 `scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)` 逐行解析 `--format json` 输出流（彻底防止含多章节长文的 JSONL 事件行超出默认 64KB 触发 `bufio.ErrTooLong`）：
     - 捕获并更新最新 `sessionID`；
     - 按 `messageID` 聚合助手 `type=="text"` 分片，提取末条助手消息作为候选结果；
     - 累加 `step_finish`（或模型 usage 事件）中的 `input` / `output` / `cache.read` Token 数；**归一化 `PromptTokens` 与 `CachedPromptTokens`**（当 Provider 将 `input` 与 `cache.read` 分离计量导致 `cacheRead > input` 时，自动将总输入归一化或钳位 `CachedPromptTokens <= PromptTokens`，确保 `QualityMetricsService` 计算 `avg_cache_hit_ratio` 恒处于 `[0.0, 1.0]`）；
     - 结合进程退出码、stderr 环形缓冲与 stdout 事件完成结构化错误分级（`ErrorClass`）。
4. **`--session` 轻量级断点续跑（Continuation）**：
   - 当满足以下任一可恢复条件，且已捕获有效 `SessionID`、`ContinuationEnabled == true`、剩余父上下文时间 `>= 60s` 时，自动在**同一临时隔离目录**（保留原 `XDG_DATA_HOME` SQLite 会话状态）内使用 `--session <SessionID>` 发起 **至多 1 次轻量续跑**：
     - **场景 A（中断/超时/无 JSON 输出）**：首轮遭遇 `timeout` / `idle_timeout` / `rate_limited`（先退避 2.5s）/ `output_missing`，续跑提示明确禁止重新调用工具扫描仓库，要求直接基于已有会话上下文输出全部 `ActiveSlots` 的 JSON；
     - **场景 B（部分槽位漏填）**：首轮返回了合法 JSON 但 `len(MissingSlotIDs) > 0`，续跑提示仅列出缺失的 `MissingSlotIDs` 要求定向补齐并与首轮已成功槽位合并。

### 5.4 与既有槽位记录、单槽位独立重试加固及增量刷新的融合

1. **槽位级可观测性与部分失败兼容**：
   - OpenCode 执行前同样初始化所有 `archdoc_gen_slot_tasks`（`locked` 标 `skipped_locked`，其余标 `running` 且 `AttemptCount = 1`、`ModelName` 标注 `[opencode] <label>`）；
   - 执行完毕后按 `DocGenResult.Slots` 逐一回填 `completed`（写入 `OutputMarkdown` 与 `CitedAnchorsJSON`）或 `failed`（对 `MissingSlotIDs` 记录明确缺失原因）；只要有 `>= 1` 个活跃槽位成功产出，即可进入 `verifying` 合成初稿并允许对缺失槽位发起单槽位重试。
2. **单槽位独立重试全面加固 (`RetrySlotTask` & `executeSingleSlotRetry`，解决 P-6)**：
   - **租约、读锁与调度器信号量统一**：`executeSingleSlotRetry` 全程持有 `o.leaseMgr.AcquireRepo(task.RepoID)` 与 `o.leaseMgr.AcquireSnapshot(*task.SnapshotID)`，并统一通过 `o.acquireLLMSlot(ctx)` 获取全局信号量；
   - **继承所属任务引擎**：检查 `task.Engine`——若为 `opencode`，在 `opencodeSem` 与 `WithRepoReadLock` 保护下仅将该失败 `targetSlot` 作为单元素 `ActiveSlots` 交由 OpenCode 会话定向生成（`LLMUsageLog.Stage = "slot_retry_opencode"`）；若为 `native`，走加固后的 `executeSlotLLM`（`Stage = "slot_retry"`）；
   - **非目标槽位证据锚点无损保留**：重跑 `Verifier` 构造 `sectionInputs` 时，从上一修订 `prevEvs`（`SectionEvidence.EvidencesJSON`）还原所有非目标槽位的 `CitedAnchors` 传入 `Verifier`，彻底修复单槽位重试后兄弟章节丢失非内联结构化锚点的问题；
   - **全败任务重试文档恢复与状态收敛**：若原任务因 `ALL_SLOTS_FAILED` 尚未创建 `Document`，单槽位重试成功后自动调用 `persistGeneratedDocument` 创建文档与初始修订并回填 `slotTask.DocumentID`；若原任务状态为 `failed` 且本次重试后已有槽位成功落库（或全部非锁定槽位均已 `completed`），自动将主任务 `GenTask.Status` 更新为 `ready`（`progress_percent = 100`）。
3. **章节级增量刷新 (`IncrementalRefresh`)**：
   - `IncrementalRefreshRequest` 与 `IncrementalRefreshTaskInput` 新增可选 `Engine string`（未指定时默认取该文档最近一次生成任务的引擎或系统默认引擎）；
   - 当 `engine == "opencode"` 时，将全部受影响且非锁定的 `targetSlots` 一次性交由单次 OpenCode 会话合并生成，未受影响章节与 `locked` 章节保持 `content_sha256` 字节级不变（`SC-11`）。
4. **Canvas Studio 实时 Chat 精修保持 Native**：
   - `ChatService` 依赖毫秒级 SSE `delta` 实时推送与 `StreamSplitFilter` 交互体验，继续固定走加固后的 Native 流式引擎。

---

## 6. 决策 D-4：数据模型增量、配置治理与 HTTP API 契约

### 6.1 数据模型演进（零新增表，坚守 20 张 `archdoc_*` 表基线）

本 ADR 不新增数据库表，仅对现有 3 张自有表执行向后兼容的增量列扩展与列宽扩容（均带 `default` 值，支持 GORM `AutoMigrate` 无损平滑升级，并通过 `TestAll20TablesPrefixAndAutoMigrate` 校验）：

| 表名 / 模型 | 新增或调整字段 | 列定义与默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `archdoc_gen_tasks` (`models.GenTask`) | `Engine` *(新增)* | `gorm:"size:20;not null;default:'native';index" json:"engine"` | 记录本任务使用的生成引擎（`native` \| `opencode`） |
| `archdoc_llm_usage_logs` (`models.LLMUsageLog`) | `Engine` *(新增)*<br>`Stage` *(列宽扩容)* | `Engine`: `gorm:"size:20;not null;default:'native';index" json:"engine"`<br>`Stage`: 由 `size:24` 扩为 `gorm:"size:32;not null;index" json:"stage"` | 区分 Native 与 OpenCode 成本用量；将 `stage` 扩至 `varchar(32)` 以安全容纳 `doc_gen_opencode`、`doc_gen_oc_continue`、`slot_retry_opencode`、`inc_refresh_opencode` 等阶段名 |
| `archdoc_llm_configs` (`models.LLMConfig`) | `OpenCodeModel` *(新增)* | `gorm:"size:128;not null;default:''" json:"opencode_model"` | 可选原生 OpenCode `provider/model` 覆盖标识；留空时自动通过模式 A 桥接 `base_url` / `model_name` / `api_key` |
| `models/enums.go` | `EngineNative`, `EngineOpenCode`, `IsValidEngine`, `NormalizeEngine` | `"native"` / `"opencode"` | 领域强类型引擎常量与归一化校验函数 |

### 6.2 `config.yaml` 配置扩展 (`config/config.go`)

在 `LLMSection` 增量流式超时配置，并新增顶层 `OpenCodeSection`：

```yaml
llm:
  user_agent: "Code-Archdoc/dev"
  first_byte_timeout_seconds: 90   # Native 流式首包超时（默认 90s）
  idle_timeout_seconds: 60         # Native 流式块间空闲超时（默认 60s）

opencode:
  enabled: true                    # 总开关，false 时禁止选择 opencode 引擎
  binary_path: ""                  # 可选显式路径；留空默认按 PATH 及 ~/.opencode/bin/opencode 查找
  default_engine: "native"         # 未传 engine 参数时的默认引擎 (native | opencode)
  max_concurrency: 2               # OpenCode CLI 子进程最大并发数（默认 2，防 CPU/内存过载）
  timeout_seconds: 600             # 单次 OpenCode 文档生成超时（默认 600s）
  idle_timeout_seconds: 120        # 无 stdout/stderr 活动空闲看门狗超时（默认 120s）
  continuation_enabled: true       # 是否允许 --session 断点续跑（默认 true）
  continuation_max_seconds: 180    # 续跑阶段预算上限（默认 180s）
```

> **二进制探测路径增强**：由于服务器上 OpenCode 常安装在用户目录 `~/.opencode/bin/opencode` 或 `/usr/local/bin/opencode`（不一定在 systemd 服务进程的默认 `PATH` 中），当 `binary_path` 留空时，探测器依次查找 `exec.LookPath("opencode")`、`$HOME/.opencode/bin/opencode`、`/usr/local/bin/opencode`，提升生产环境开箱即用率。

### 6.3 双层信号量并发控制 (`services/scheduler_service.go` / `services/generation_service.go`)

- 保留全局 `TaskScheduler`（`max_llm_concurrency`，默认 `8`）控制总任务/API 并发；
- 在 `GenerationOrchestrator` 新增独立的 `opencodeSem chan struct{}`（容量 `config.AppConfig.OpenCode.EffectiveMaxConcurrency()`，默认 `2`）：每个 OpenCode 文档会话执行前依次获取调度器槽位与 `opencodeSem`，避免重型 CLI 进程并发堆积拖垮宿主机，也防止阻塞 Native 槽位或 Chat 实时响应。

### 6.4 HTTP API 增量契约与错误码

在 `handlers/error.go` 新增机器可读错误码常量 `ErrCodeEngineUnavailable = "ENGINE_UNAVAILABLE"`（HTTP `400`）：

| 方法与路径 | 权限 | 请求 / 查询扩展 | 响应增量与错误码规范 |
| :--- | :---: | :--- | :--- |
| **`GET /api/engines`** *(新增)* | `JWT` | — | 返回可用引擎清单与默认引擎：<br>`{ "default_engine": "native", "engines": [{ "id": "native", "label": "Native 直连引擎", "available": true, "default_model": "DeepSeek-V3" }, { "id": "opencode", "label": "OpenCode 深度 Agent", "available": true, "version": "1.18.21", "reason": "", "default_model": "DeepSeek-V3" }] }`（CLI 二进制版本探活内置 30s TTL 缓存，但 `default_model` 每次实时读取 DB 当前默认模型配置） |
| **`POST /api/tasks/generate`** | `ScopeWrite` | 新增可选 `doc_type?: string`、`engine?: "native" \| "opencode"` | 校验 `engine` 合法性（非法返回 `400 INVALID_PARAM`；选 `opencode` 但未安装/已禁用返回 `400 ENGINE_UNAVAILABLE`）；多文档拆单返回 `202 Accepted`（`GenerateTaskSubmitResponse`：顶层内嵌首任务 `GenTaskDetailVO` + `created_count` + `tasks[]`） |
| **`POST /api/tasks/generate-batch`** | `ScopeWrite` | 新增可选 `engine?: "native" \| "opencode"` | 按 `(Scope, DocType)` 拆单创建子任务并透传 `engine`；`opencode` 不可用时前置拦截返回 `400 ENGINE_UNAVAILABLE` |
| **`GET /api/tasks` & `GET /api/tasks/:task_id`** | `JWT` | `GET /api/tasks` 新增可选 Query `?engine=all\|native\|opencode` | `GenTaskDetailVO`（及 `GenTaskSummaryVO`）新增 `engine` 字段，用于任务中心列表徽标、筛选器与详情抽屉展示 |
| **`POST /api/documents/:id/incremental-refresh`** | `ScopeWrite` | 请求体新增可选 `engine?: "native" \| "opencode"` | 指定后按对应引擎合并生成受影响非锁定槽位；`opencode` 不可用时返回 `400 ENGINE_UNAVAILABLE` |
| **`POST/PUT /api/admin/llm-configs` & `GET`** | `Admin` | 新增可选 `opencode_model?: string` | 校验长度 ≤ 128 且不含空白控制字符；`LLMConfigVO` 与 `LLMConfigAuditSnapshot` 暴露 `opencode_model`（绝不涉及明文凭据） |

---

## 7. 决策 D-5：前端交互设计（可选引擎与多文档拆单监控）

### 7.1 `GenerateTaskModal` 新建生成任务交互

1. **执行引擎选择器（配置态）**：
   - 打开弹窗（非监控态）时请求 `GET /api/engines`（失败时静默降级为仅 `Native` 可用，不阻断提交），渲染双引擎分段卡片选择器：
     - **Native 直连引擎（Thin）**：展示说明「基于 Code IR 分片并行生成，速度快、KV Cache 命中率高、入参严格脱敏」。
     - **OpenCode 深度 Agent（Thick）**：展示版本号（如 `v1.18.21`）与说明「按单文档发起只读沙箱会话，自主读码追查跨文件链路，支持会话压缩与断点续跑，适合复杂核心模块」；若 `available === false`，该选项置灰禁用并展示不可用原因（`reason`）。
   - **安全与耗时微提示**：选中 OpenCode 时在引擎区下方展示轻量提示条（说明“OpenCode 将以只读方式直接在本地沙箱内读取仓库源码并自动经过 Verifier 凭据阻断门禁，单篇文档耗时通常为 1~5 分钟”）。
   - **偏好记忆**：通过纯函数 `resolveInitialEngine(enginesResp, storedEngine)` 结合 `localStorage`（键 `archdoc.preferred_gen_engine`）记忆用户上次选择的可用引擎；若上次偏好引擎当前不可用则自动回退至 `default_engine`。
2. **多文档拆单交互与增量轮询队列监控态**：
   - 提交按钮文案随选中文档数动态更新：单选时显示「开始生成 (1 篇)」，多选 `N > 1` 篇时明确显示「创建 N 个文档生成任务」，批量模式显示「提交批量编排任务」。
   - 当非批量模式下拆单返回 `tasks.length > 1` 时，弹窗切换为**多文档拆单队列监控视图**：
     - **增量按需轮询**：定时器仅对尚未进入终态（`!TERMINAL_STATUSES.has(t.status)`）的任务调用 `getTaskDetail(id)` 更新状态，已就绪/已失败/已取消的任务不再重复请求；
     - **单篇就绪即刻审阅**：展示汇总进度条与逐文档任务行（任务 `#ID`、文档类型名、引擎徽标、状态标签、进度条、独立取消按钮）；**任一单文档任务变为 `ready` 时立即通知父页面刷新列表，并在该行展示「进入工作台」按钮**，用户无需等待其余文档跑完即可先行审校已就绪文档；
     - **下钻槽位明细**：点击某行「槽位明细」可切入该单文档任务详情（查看各槽位状态并支持失败单槽位重试），顶部提供「返回拆单队列（共 N 篇）」导航按钮。

### 7.2 `TaskCenterPage` 与 `AdminLLMConfigPage` 协同更新

1. **任务中心 (`TaskCenterPage.tsx`)**：
   - 筛选栏新增「全部引擎 / Native 直连 / OpenCode Agent」下拉框（同步至 `urlQuery.ts` 的 `parseTaskFilterParams` / `serializeTaskFilterParams` URL Search Params）；
   - 任务列表「任务 ID & 类型」列与批量父任务的子任务表新增紧凑引擎徽标（`Native` / `OpenCode`）与单文档 `DocType` 徽标。
2. **LLM 模型配置 (`AdminLLMConfigPage.tsx`)**：
   - 模型新增/编辑弹窗增加可选字段 `OpenCode 原生模型映射 (provider/model，可选)`，并附清晰提示：“留空时系统自动将上方 Base URL、Model Name 与 API Key 通过隔离配置文件桥接给 OpenCode 使用，无需额外配置”。
3. **样式规范遵守**：全部新增标签、卡片与状态提示严格复用 `@code/common` 基础类及 `theme.css` `--color-*` 语义 Token，严禁静态内联 `style` 与硬编码色值，完整兼容亮/暗双模主题。

---

## 8. 安全合规、权衡边界与架构防腐红线

1. **明示安全权衡（OpenCode 入参边界 vs Native 数据栅栏）**：
   - **差异事实**：Native 引擎在进程内对每次送入 LLM 的源码切片与工具输出均执行 `pkg/security.SecretRedactor` 三级脱敏，并包裹 128-bit 随机 `nonce` 的 `<untrusted_repo_data>` 防注入栅栏；而 OpenCode 通过内置 `read`/`grep`/`glob` 直接读取本地沙箱目录文件，**不会经过 `SecretRedactor` 入参脱敏与 `nonce` 栅栏**，从而直接将沙箱源码发送至所配置的内网模型服务端。
   - **纵深缓解措施**：
     1. **只读沙箱硬隔离**：OpenCode Agent 权限策略白名单仅开放 `read`/`grep`/`glob`/`list`，显式 `deny` 所有 `edit`/`write`/`patch`/`bash`/`webfetch`/`websearch`/`subagent`/`skill`/`browser`/`question`/`external_directory` 及 `*.env` 敏感文件，且工作目录严格经 `SafeJoin` 锁定于当前目标作用域；
     2. **输出侧硬阻断不妥协**：无论上游由哪个引擎生成，全部产出必须经过独立于 LLM 的 **Verifier 四重门禁（Gate 4 `SecretScanner`）**，一旦命中疑似明文密码、私钥或高熵 Token 立即标 `can_approve = false` 并由发布接口返回 `422 VERIFIER_BLOCKED` 硬阻断；
     3. **显式前端知情选择**：高安全合规或严格要求外发前入参脱敏的仓库，管理员与研发可在新建任务时选择 `Native` 引擎或通过 `config.yaml` 关闭 `opencode.enabled`。
2. **临时凭据零泄漏**：
   - 模式 A 生成的含明文 `apiKey` 的 `opencode.json` 仅写入 `0700` 临时目录下权限 `0600` 的随机文件，进程退出后立即在 `defer` 中 `os.RemoveAll` 彻底销毁；绝不写入仓库工作区、命令行参数（`argv`）、环境变量、系统日志或持久化 DB 调试字段。
3. **命令行与环境变量防注入**：
   - `opencode` 进程一律通过 `exec.Command(binary, args...)` 参数数组直接调起（禁止 `sh -c`）；目标工作目录（v1 通过 `--dir` 与 `cmd.Dir`，v2 通过 `cmd.Dir` 与 `PWD`）仅传递已通过 `NormalizeSubdir` + `SafeJoin` 校验的物理路径；v2 强制注入 `--standalone` 与全套 `OPENCODE_DISABLE_*` 隔离环境变量，不连接宿主机后台服务或读取宿主机/目标仓无关配置；日志仅输出经摘要脱敏后的参数概要。
4. **`make lint-arch` 架构红线守护**：
   - `pkg/opencode/` 严禁导入 `code-archdoc/models`、`services`、`handlers`、`gorm` 或 `gin`；
   - `pkg/opencode/` 与 `handlers/` 严禁直接调用 `gitsync.Sync`，所有沙箱同步、工作区锁与租约管理继续收口在 `services.SnapshotService` 与 `services.ActiveLeaseManager`。

---

## 9. 分阶段实施路线图与 TDD 验收清单 (`M14`)

为实现“第一步止血立竿见影、第二步双引擎稳健落地”，M14 划分为 **3 个正交子里程碑（`M14-A` / `M14-B` / `M14-C`）**，全程严格遵循 **SDD 契约先行 + TDD 红绿重构** 推进：

| 子阶段 | 核心交付范围 | 对应问题 / 决策 |
| :--- | :--- | :--- |
| **M14-A（第一步：Native 加固与单文档拆单）** | 1. 修复 `pkg/llm/client.go` 超时不重试 Bug，对齐 `0..10` 重试上限，升级可注入 `baseBackoff` Jitter 指数退避 + `Retry-After`；<br>2. 槽位调用升级内部流式聚合 + `Content-Type` 双模自适应解析 + 首包/空闲/总超时三层护栏与网关自动降级；<br>3. 修复 `pkg/llm/budget.go` 统一按 `scope_profile` 统计 `TopDirectories`；重构 `pkg/llm/prompt.go` 三级结构化裁剪保持 100% 合法 JSON；<br>4. 改造 `GenerationOrchestrator`、`batch_generation.go` 与 `TaskScheduler.ReserveSlots` 落实 `1 Task = 1 Scope × 1 DocType` 无竞态拆单及响应兼容结构；加固 `executeSingleSlotRetry`。 | D-1、D-2<br>(P-1, P-2, P-3, P-5, P-6) |
| **M14-B（第二步后端：OpenCode 引擎与双引擎编排）** | 1. `models` 增量列（`GenTask.Engine`、`LLMUsageLog.Engine` + `Stage` 扩至 `size:32`、`LLMConfig.OpenCodeModel`）与 `config.yaml` `opencode:` 配置段；<br>2. 新增纯领域包 `pkg/opencode/`（双模式配置桥接、全套 XDG/CI 隔离、只读 Agent、8MB JSONL 事件流解析、Usage 归一化、防重 H2 标题剥离、`LockOSThread` + `Setpgid` 强杀、Idle 看门狗与 `--session` 断点续跑）；<br>3. `GenerationOrchestrator` 接入 `opencodeSem`、单文档 OpenCode 执行路径、单槽位重试与增量刷新适配；<br>4. 新增 `GET /api/engines`、`ENGINE_UNAVAILABLE` 错误码，扩展生成/批量/任务/模型配置接口。 | D-3、D-4<br>(P-4, P-6, P-7) |
| **M14-C（第二步前端与全链路交付）** | 1. 更新 `frontend/src/types/`、`src/api/` 与 `src/utils/urlQuery.ts`；新增纯函数 `src/utils/taskSubmitUtils.ts`（含 `node --test` 单测）；<br>2. `GenerateTaskModal.tsx` 增加引擎选择器与多文档拆单增量轮询队列监控视图（支持单篇就绪即刻跳转工作台）；<br>3. `TaskCenterPage.tsx` 增加引擎筛选与徽标；`AdminLLMConfigPage.tsx` 增加 `opencode_model` 配置；<br>4. 全量物理隔离测试、构建与架构门禁验收。 | D-5<br>(P-5, P-7) |

### TDD 验收测试用例清单 (`T-ADR08-01` ~ `T-ADR08-16`)

> 说明：所有涉及 OpenCode CLI 的单测均通过临时目录注入 **Fake `opencode` Shell 脚本**（覆写 `PATH` 或 `BinaryPath`）模拟事件流、超时、限流、`--session` 续跑与信号强杀，**100% 离线可重复执行，零外部模型依赖**。

| 用例编号 | 归属阶段 | 覆盖包 / 模块 | 核心测试场景与硬断言 |
| :--- | :---: | :--- | :--- |
| **`T-ADR08-01`** | `M14-A` | `pkg/llm` | **超时重试回归与 `0..10` 上限验证**：Mock `httptest` Server 首两次请求超时、第 3 次成功，`attempt` 计数严格为 `3`（证明修复 `reqCtx` 遮蔽 Bug）；支持 `MaxRetries > 3`（如 `4` 次重试）与 `Retry-After` 响应头；父 `ctx` 主动取消时立即终止且不触发后续重试。 |
| **`T-ADR08-02`** | `M14-A` | `pkg/llm` | **流式三层超时、`Content-Type` 双模自适应与网关降级**：Mock SSE 慢首包触发 `FirstByteTimeout` 重试；流式中途卡死触发 `IdleTimeout` 重试；对端忽略 `stream:true` 直接返 `application/json` 或返 `400 unsupported stream` 时均能无损提取正文。 |
| **`T-ADR08-03`** | `M14-A` | `pkg/llm` | **三级 JSON 合法结构化裁剪与预算校准**：极大 `Tree.Files` / `Deps.FileImports` / `Symbols.Symbols` 经 `serializeSlotFacets` 裁剪后 `json.Valid == true` 且含 `_meta.truncated == true`；`EstimateAndCheckBudget` 应用 `include_tests: false` 后不再将 `test/` 计入 `TopDirectories`。 |
| **`T-ADR08-04`** | `M14-A` | `handlers` / `services` | **多文档拆单、共享缓存与单槽位重试加固**：`POST /api/tasks/generate` 传入 3 个 `doc_types` 返回 `created_count == 3`、`len(tasks) == 3` 且各自 `len(doc_types) == 1`；全链路 `gitsync` 与 `glossary` 调用次数严格为 `1`（`SC-02`）；单槽位重试保留兄弟章节非内联 `cited_anchors` 且支持全败任务恢复。 |
| **`T-ADR08-05`** | `M14-A` | `services` / `handlers` | **批量拆单与两阶段预留原子排队保护**：`generate-batch` 对 `2 模块 × 2 DocType + 整仓` 准确创建 `4 + 2 = 6` 个单文档子任务且 `ModuleBreakdown` 保持 3 个范围条目；当 `InteractiveQueueCap` 剩余空间不足 `N` 时整体返 `429 QUEUE_FULL` 且 DB 创建 `0` 条任务。 |
| **`T-ADR08-06`** | `M14-B` | `pkg/opencode` | **双模式配置桥接与零凭据泄漏**：模式 A 生成 `0600` 临时 `opencode.json` 含 `@ai-sdk/openai-compatible` 与只读 Agent 白名单，启动 `argv` 与环境打印脱敏摘要中 `0` 次出现明文 `APIKey`，调用返回后临时目录完全清理；模式 B 正确传入 `--model <provider/model>` 并隔离全套 `XDG_*`。 |
| **`T-ADR08-07`** | `M14-B` | `pkg/opencode` | **只读权限与大 Prompt `--file` 传输**：校验 Agent 配置包含 `edit: false, write: false, bash: false` 与 `permission."*": "deny"`；Prompt 超 `64 KB` 时自动走 `--file` 传入，不进入 `argv`。 |
| **`T-ADR08-08`** | `M14-B` | `pkg/opencode` | **8MB JSONL 事件流、Usage 归一化、防重 H2 标题与格式容错**：正确从超长单行分片事件恢复末条助手文本、累计并归一化 `input/output/cache_read` Token（`CachedPromptTokens <= PromptTokens`）；自动剥离与 `slot.Title` 重复的首行 `## 标题`；兼容纯 JSON、`` ```json `` 围栏与顶层映射提取全部 `slots`。 |
| **`T-ADR08-09`** | `M14-B` | `pkg/opencode` | **`--session` 断点续跑与缺槽位补齐**：Fake 脚本首轮返回 `sessionID` 后模拟超时或漏掉 1 个 `slot_id`，验证自动在同一隔离目录以 `--session <id>` 触发续跑并最终补齐全部 `ActiveSlots`（不超 1 次续跑）。 |
| **`T-ADR08-10`** | `M14-B` | `pkg/opencode` | **`LockOSThread` + 进程组取消与 Idle 看门狗**：Fake 脚本启动后台子进程休眠，触发 `ctx.Cancel()` 或 `IdleTimeout` 后，父子进程组在 1 秒内全部被回收（无僵尸/孤儿进程残留）。 |
| **`T-ADR08-11`** | `M14-B` | `handlers` / `services` | **OpenCode 单文档全链路端到端（含锁定与 Verifier）**：`POST /api/tasks/generate` 指定 `engine: "opencode"`：验证 `locked` 槽位不下发给 OpenCode 且保持字节不变、生成结果经过 Verifier 四重门禁（含模拟凭据泄漏触发 `can_approve=false` 硬阻断）、Usage 准确落库 `engine="opencode"` 且 `stage` 不触发列宽截断。 |
| **`T-ADR08-12`** | `M14-B` | `handlers` / `services` | **OpenCode 失败单槽位重试、增量刷新与 `opencodeSem` 并发隔离**：首轮故意让 1 个槽位缺失标 `failed`，调用 `/slots/:id/retry` 后仅以 OpenCode 重跑该缺失槽位并收敛主任务状态；验证 `opencode.max_concurrency=1` 时多任务串行排队不突破上限。 |
| **`T-ADR08-13`** | `M14-B` | `handlers` | **引擎可用性探活与参数校验**：`GET /api/engines` 准确反映 `opencode` 可用性、版本号与最新 `default_model`；显式传 `engine: "opencode"` 且未安装/禁用时返 `400 ENGINE_UNAVAILABLE`；非法 `engine` 返 `400 INVALID_PARAM`；Admin 模型配置支持保存与审计 `opencode_model`。 |
| **`T-ADR08-14`** | `M14-C` | `frontend` (`node --test`) | **前端纯函数单测 (`taskSubmitUtils.ts` & `urlQuery.test.ts`)**：测试引擎默认回退与 `localStorage` 偏好校验、多文档拆单响应归一化（单任务 vs 多任务）、拆单队列进度聚合计算以及 `?engine=` URL 查询参数编解码。 |
| **`T-ADR08-15`** | `M14-C` | `frontend` (`npm run lint`) | **前端类型安全与构建校验**：`tsc --noEmit`、`eslint` 零警告通过，Vite 生产构建成功，亮/暗主题 CSS Token 零硬编码色值。 |
| **`T-ADR08-16`** | 全局 | `code-archdoc` | **架构与全量回归门禁**：`make lint-arch`（确认 `pkg/opencode/` 零 DB/Gin 依赖）、`TEST_DB_DSN="..." make test` 全量物理隔离测试全绿、`make build && make clean` 单二进制构建通过。 |

#### 质量门禁命令清单

```bash
make lint-arch
TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=<PWD> dbname=postgres sslmode=disable" make test
cd frontend && npm test && cd ..
make lint
make build && make clean
```
