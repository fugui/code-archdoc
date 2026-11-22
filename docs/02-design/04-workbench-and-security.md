# Canvas Studio 微前端工作台、双轨修订与 RBAC 审计设计

- **文档编号**：`04-workbench-and-security.md`
- **文档版本**：v1.3.0 (ADR-08 新建任务引擎选择器、多文档拆单监控队列与任务中心引擎治理)
- **编制日期**：2026-11-22
- **归属阶段**：阶段二：系统架构、领域引擎与数据模型设计 (`02-design`)
- **关联基线**：`F-01`, `F-03`, `F-08`, `F-09`, `F-10`, `F-12`, `F-13`, `F-14`, `F-15`, `F-26`, `F-32`, `F-33`, `F-35`, `SC-01`, `SC-05`, `SC-10`, `SC-12`, `SC-19`, `SC-20`, `SC-22`, `NFR-7`, [`ADR-01`](../05-adr/01-microfrontend-and-table-prefix-isolation.md), [`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md), [`ADR-05`](../05-adr/05-doctype-blueprint-dual-mode-editor.md), [`ADR-06`](../05-adr/06-document-review-and-refine-studio.md), [`ADR-08`](../05-adr/08-dual-engine-doc-generation.md)

---

## 1. 微前端集成契约、路由表与设计体系复用

### 1.1 Module Federation 联邦契约（`./App` 与 `./menu`）

- **构建契约**：`frontend/vite.config.ts` 保持 `name: 'archdoc'`、`base: '/archdoc/'`、`filename: 'remoteEntry.js'`，共享 `react`、`react-dom`、`react-router-dom`。
- **`./menu` 侧边栏导出（3 组 8 项）**：
  - **文档全景（全员可见）**：
    - `/overview`（全局概览：文档资产覆盖、双维漂移健康度、反推生产任务态势与生成质量度量大盘）
    - `/repos`（仓库文档覆盖，`W1`）
    - `/arch-tree`（架构文档全景，`W2`）
  - **生产与保鲜（全员可见）**：
    - `/tasks`（反推任务中心，`W1`）
    - `/drift`（双维漂移雷达，`W2`）
  - **模块治理（`adminOnly: true`，仅 `archdoc_admin` / `super_admin` 可见）**：
    - `/admin/llm`（LLM 模型配置，`W1`）
    - `/admin/doc-types`（`DocType` 文档类型管理，`W2`）
    - `/admin/ops`（运行与存储运维，`W2`）
- **`./App` 内部路由表（双模同时兼容相对子路径与 `/archdoc/*` 完整前缀）**：

| 路由路径（嵌入态均含 `/archdoc` 前缀兼容） | 视图组件 | 波次 | 角色访问边界 |
| :--- | :--- | :---: | :--- |
| `/` → 重定向至 `/overview` | `QualityMetricsPage` | W1/W2 | 登录全员 |
| `/overview` & `/metrics`（兼容路由） | `QualityMetricsPage`（全局概览大盘：覆盖态势 + 漂移雷达汇总 + 任务态势 + 质量度量与修订纵向对比） | W2 | 登录全员 |
| `/repos` | `RepoCoveragePage`（仓库与子目录文档覆盖列表，Header 保留极简微标） | W1 | 登录全员（生成按钮按负责人 RBAC 动态启禁） |
| `/arch-tree` | `ArchTreeCoveragePage`（架构树文档全景视图，Header 保留极简微标） | W2 | 登录全员 |
| `/tasks` | `TaskCenterPage`（反推任务中心，Header 保留极简状态微标） | W1 | 登录全员 |
| `/studio/:repoId` & `/studio/:repoId/docs/:docId` | `CanvasStudioPage`（双栏协同审校工作台） | W1 / W2 | 全员只读已发布；负责人可查看/编辑草稿 |
| `/docs/:docId/evidence` | `CanvasStudioPage`（**深链直达入口**：自动定位 `docId` 并打开只读源码证据抽屉，读取 Query 参数 `commit`、`file`、`symbol`、`lines` 高亮渲染，承接 `code-bench` `D-03` 跳转） | W1 | 登录全员（仅限已发布文档；草稿仅负责人可看） |
| `/drift` | `DriftRadarPage`（章节级双维漂移雷达，Header 保留极简微标） | W2 | 全员只读；负责人可触发所属范围增量刷新 |
| `/admin/llm` | `AdminLLMConfigPage`（平等模型池管理、唯一默认模型手动切换、单任务预算阈值与探活） | W1 | `archdoc_admin` / `super_admin` |
| `/admin/doc-types` | `AdminDocTypesPage`（`DocType` 注册表 CRUD 与模板编辑器） | W2 | `archdoc_admin` / `super_admin` |
| `/admin/ops` | `AdminOpsPage`（受控本地存储配额治理、手动 LRU 回收与双优先级调度态势） | W2 | `archdoc_admin` / `super_admin` |

### 1.2 `@code/common` 复用与视觉编码硬约束（NFR-7）

严格遵守工作区上位准则与 `Non-Goal 6`（**严禁引入 Ant Design / Material UI 等重型组件库**）：
- **公共组件复用**：
  - 列表分页统一复用 `@code/common` 的 `Pagination` + `usePagination`（URL Search Params 双向同步 `page`、`pageSize` 与筛选条件，默认 **`25` 条/页**，选项 `15 / 25 / 50 / 100`，固定 `[首页][上一页][5页连续滑动窗口][下一页][末页][条/页]`）；
  - 浮层与反馈统一复用 `Drawer`、`Modal`、`ConfirmProvider`、`ToastProvider`、`EmptyState`、`StatusTag`、`MemberSearchSelect`、`ErrorBoundary`、`UnifiedLogin`。
- **双模主题与样式规范**：
  - 全量使用 Vanilla CSS 与 `theme.css` 中的 `--color-*` 语义 Token（`--color-bg-surface`、`--color-bg-muted`、`--color-bg-input`、`--color-bg-hover`、`--color-text-primary`、`--color-text-secondary`、`--color-border-primary`、`--shadow-drawer-right`），**零硬编码 `#fff`/`#000`/`rgba(...)`**，确保亮/暗主题均具备高质感视觉；
  - 样式命名统一遵循扁平化 BEM `.code-archdoc-{block}-{element}--{modifier}`；禁止静态内联 `style={{ ... }}` 与 JS 模拟 hover 伪类；TypeScript 严格零 `any`。

---

## 2. 四大业务工作台 React 组件分层树

### 2.1 仓库与子目录文档覆盖视图 (`RepoCoveragePage`，`F-01`，W1)

```text
RepoCoveragePage
├── PageHeader (标题、全局覆盖率摘要 KPI 卡片、快速筛选重置)
├── RepoFilterBar (URL Search Params 双向同步)
│   ├── SearchInput (仓库名 / 子目录关键词防抖搜索)
│   ├── DepartmentSelect (复用共享部门列表)
│   ├── DocTypeFilterSelect (按 DocType 筛选覆盖情况)
│   └── StatusFilterSelect (全部 | 已发布 | 草稿中 | 存在漂移 | 未生成)
├── RepoCoverageTable (Vanilla CSS 精细表格)
│   └── RepoScopeRow (每行对应 1 个整仓或 Monorepo ArchitectureElement 子目录)
│       ├── RepoIdentityCell (仓库名、子目录标签、分支、Owner、最近 commit_sha)
│       ├── DocTypeStatusMatrix (各启用 DocType 的 StatusTag 徽章，点击直入 Studio)
│       ├── DriftSummaryBadge (W2: Code Drift / Spec Drift 计数标牌)
│       └── ActionGroup (进入工作台 | 触发生成 [RBAC 控制] | ZIP 导出已发布文档)
├── Pagination (@code/common 5 页滑动窗口, 默认 25 条/页)
└── GenerateTaskModal (ADR-08 升级：支持双引擎配置态与多文档拆单增量轮询监控态)
    ├── EngineSelectorCard (请求 GET /api/engines，记忆 localStorage 偏好，不可用置灰并展示原因，选 OpenCode 时展示只读/耗时微提示)
    ├── DocTypeMultiSelectGrid (提交按钮动态显示「开始生成 (1 篇)」/「创建 N 个文档生成任务」/「提交批量编排任务」)
    └── SplitTaskQueueMonitor (多文档拆单返回 tasks.length > 1 时激活：增量轮询未终态任务、单篇 ready 即刻通知刷新并展示「进入工作台」、支持下钻单任务槽位明细与「返回拆单队列」)

TaskCenterPage (F-05 / ADR-08)
├── TaskFilterBar (URL Search Params 双向同步：仓库 / 状态 / 类型 / 执行引擎 [全部 | Native 直连 | OpenCode Agent] / 仅看我)
└── TaskListTable (展示引擎徽标 Native / OpenCode 与单文档 DocType 徽标；展开批量父任务同步展示子任务引擎徽标)
```

### 2.2 Canvas Studio 双栏协同工作台 (`CanvasStudioPage`，`F-08`/`F-09`/`F-10`)

```text
CanvasStudioPage
├── StudioTopBar
│   ├── ScopeBreadcrumb (仓库 / 子目录 / 分支 / commit_sha 短码 / DocTypeRevision 徽标)
│   ├── DocumentStatusBar (Draft | Approved 状态标签、Verifier 汇总灯、双维漂移提示条)
│   └── StudioActions (重新生成 | 撤回发布 | 审批发布 [Verifier 阻断时禁用并悬浮原因] | 下载 Markdown)
├── StudioSplitWorkspace (CSS Grid 三段式自适应布局，支持拖拽调宽)
│   ├── LeftDocTreePanel
│   │   ├── DocTypeSwitcherList (当前范围下多 DocType 导航树 + 覆盖/告警角标)
│   │   └── SlotOutlineTOC (当前文档章节槽位大纲，带 auto/hybrid/locked 图标与漂移红点)
│   ├── CenterDocumentCanvas
│   │   ├── VerifierBanner (展开查看 unverified 符号、Mermaid 告警、凭据拦截详情)
│   │   ├── SelectionReviewPopover (划词浮动气泡：添加批注 / AI 立即修正 / 查证源码，跨章节选区自动降级 global)
│   │   ├── SectionCardList (按 slot_id 渲染每个章节)
│   │   │   └── SectionCard
│   │   │       ├── SectionHeader (标题、slot_id、所有权切换器 auto/hybrid/locked、直编/审校精修入口)
│   │   │       ├── DetachedCommentsBar (失锚待核批注 #N 聚合条，支持编辑/转全局/已解决)
│   │   │       ├── SectionReviewOverlay (存在 pending ChatProposalItem 时原地展开：红绿 Diff / 渲染态对比 / 微调正文[暂存或立即采纳] / 拒绝本节)
│   │   │       ├── SectionMarkdownRenderer (自定义 Markdown + 块级 startLine/endLine + 悬停 + 批注按钮 + 跨行内节点 <mark>/#N 投影高亮 + MermaidZoomViewer)
│   │   │       └── SectionInlineEditor (W1 手工直编 + M12 提案微调复用：支持 initialMarkdown / proposal_tweak 模式)
│   │   └── OrphanSectionsZone (仅当存在 is_orphan=true 章节时展示：支持复制合并或显式确认删除)
│   └── RightUtilityDrawer (可折叠且支持 440px <-> 680px 展宽切换的多标签右侧面板)
│       ├── EvidenceSourceTab (W1: 只读源码查看器，基于 commit_sha + file + lines 精确高亮，顶部显示 fq_symbol 与锚点状态)
│       ├── RevisionHistoryTab (W1: 不可变修订时间线，支持任意两版本章节 Diff 对比与一键回滚)
│       └── ChatRefineTab (M12 升级为「审校精修」双视图工作台，带待处理数字徽标)
│           ├── ReviewBasketPanel (主视图：全局评论与持久约束[+添全局] + 局部指点清单[#N/分类/定位/勾选] + 一键综合精修 CTA + ProposalGroupCard 汇总采纳)
│           └── ChatRefineThreadPanel (辅视图：紧凑会话历史下拉 + 作用域胶囊[全文/指定章节] + 划词引用 + [仅查证源码 Ask]/[发送精修] + 折叠工具轨迹)
```

### 2.3 双维漂移雷达与质量度量看板 (`DriftRadarPage` & `QualityMetricsPage`，`W2`)

```text
DriftRadarPage (F-12)
├── DriftSummaryCards (总漂移文档数 | Code Drift 数 | Spec Drift 数 | 上次巡检时间 + 管理员手动巡检按钮)
├── DriftFilterBar (URL 同步：关键词 | 部门 | 漂移维度 [全部/Code/Spec/双漂移] | DocType)
├── DriftDocumentTable
│   └── DriftExpandableRow
│       ├── DriftMetaColumns (仓库/子目录、DocType、已发布 commit vs 远端 HEAD、绑定 Revision vs 最新 Revision)
│       ├── AffectedSlotsDetailPanel (展开展示每个受影响 slot_id、变动原因 [symbol_modified / slot_added 等] 与变更符号清单)
│       └── IncrementalRefreshButton (仅负责人可见：一键触发受影响章节增量刷新，跳过 locked 章节)
└── Pagination (@code/common)

QualityMetricsPage (F-26)
├── MetricsFilterBar (时间窗口 | DocType 下拉 | DocTypeRevision 多版本对比选择器)
├── KPIGrid (4 大质量指标：平均草稿→定稿修改率 | Chat 采纳率 | Verifier 首次通过率 | 必填槽位完备度)
├── RevisionTrendChart (原生轻量 SVG 百分比折线图，对比同一 DocType 不同 DocTypeRevision 的质量指标演进，严格使用 --color-* Token)
└── SlotEditHeatmapTable (展示每个 slot_id 的历史平均修改比例，帮助管理员精准定位需优化 Prompt 的薄弱槽位)
```

### 2.4 管理员治理台 (`AdminLLMConfigPage` W1 + `AdminDocTypesPage` / `AdminOpsPage` W2)

```text
AdminLLMConfigPage (F-14, W1)
├── LLMModelPoolGrid (内网平等 OpenAI Compatible 模型卡片流：DeepSeek / GLM / Kimi 等；展示默认模型徽章、启停/探活状态、脱敏 api_key_masked)
├── ModelCardActions (设为唯一默认 [手动切换，不做静默自动降级] | 测试连通性 | 编辑 | 删除 [拦截默认/被引用的模型])
└── LLMUpsertModal (新增 / 编辑弹窗：支持留空保留原 API Key、清空已保存 Key、保存前预探活测试、可选配置 OpenCode 原生模型映射 opencode_model)

AdminOpsPage (F-27, W2)
├── StorageQuotaGovernanceCard (sandbox_dir / ir_cache / publish_dir 本地配额水位与手动触发受租约保护的 LRU 回收)
└── DualPrioritySchedulerCard (全局并发信号量 in_flight/max、HighQ/LowQ 4:1 防饥饿调度排队与活跃沙箱租约态势)

AdminDocTypesPage (F-03 / F-32, W2 & M11, ADR-05)
├── DocTypeToolbar (新建 DocType | 从目录强制重同步)
├── DocTypeListTable (slug、display_name、short_name/category、指定模型 [preferred_model 或默认模型]、启用状态、当前 Revision、已发布引用数、操作 [编辑/克隆/启停/删除])
└── DocTypeEditorDrawer (@code/common Drawer, 支持右上角 xl 标准宽度 ↔ full 宽屏编排切换)
    ├── DrawerIdentitySummaryBar (展示 slug / display_name / preferred_model 或默认模型摘要，点击直跳基础信息)
    ├── Tab 1: 文档蓝图 (Blueprint) —— 编辑既有 DocType 默认激活
    │   ├── GlobalPromptsAccordion (折叠面板：generation_prompt.md 默认展开适中高度；refine_prompt.md 默认折叠带字数徽标)
    │   ├── BlueprintModeToolbar (模式切换：[可视化编排] | [参数化源码 DSL v1] | [预览 template.md]；中部指标：槽位数 · 预算总和 · 去重分片数；右侧操作：新增槽位/静态块 或 插入片段/格式化/语法速查)
    │   ├── BlueprintVisualEditor (可视化编排流)
    │   │   ├── StaticBlockCard (文首 # 主标题与章节间固定 Markdown 块，支持编辑/上下移/删除)
    │   │   └── SlotBlockCard (拟真章节卡：H2/H3/H4 或无标题内嵌切换 + 内联标题 + slot_id 既有加锁/确认解锁 + ownership + required + token_budget + enable_source_tools & max_tool_rounds + min_chars + ir_facets 分级 Chip 组 + 多行自适应 instruction_hint)
    │   ├── BlueprintSourceEditor (参数化源码 DSL v1 编辑器：Markdown + ```slot <slot_id> 围栏块 + 200ms 防抖行号级诊断条，存在 error 时阻断切换与保存)
    │   └── BlueprintTemplatePreview (只读实时展示 compileFromBlueprint 产出的 template.md)
    ├── Tab 2: 基础信息与策略 (Metadata & Policy) —— 新建 DocType 默认激活
    │   ├── BasicMetaForm (slug、display_name、short_name、category、description、可选 preferred_model、timeout_seconds、max_output_tokens、is_active)
    │   ├── TopFacetsUnionSection (默认开启「自动聚合自全部章节槽位」，支持切换「手动覆写」与「一键对齐并集」)
    │   └── DocTypePolicyPanel (默认折叠「高级治理策略」：repo_ir_facets 整仓裁剪白名单及预设、context_inputs & module_digest_source、verify_policy、scope_profile)
    ├── Tab 3: 修订历史 (Revisions) —— 仅编辑模式可见，展示历史不可变 DocTypeRevision 聚合哈希、指令哈希与槽位数
    └── DrawerFooterStatusBar (实时展示蓝图校验状态、源码模式既有 persistedSlotIds 变更风险预警徽标，切换/保存前强制弹出 useConfirm 风险确认门禁)
```

---

## 3. 章节级双轨修订状态流与 SSE 精修协议（`F-09`）

### 3.1 统一双轨修订管线（手工直编 W1 + 多槽位协同精修 M12）

两条修订轨道在后端收口至统一的 `services.RevisionService.ApplyMultiSectionChanges()` 事务入口（单槽位 `ApplySectionChange()` 直接委托调用），确保一致的不变量：

1. **严格槽位边界隔离与非内联证据无损保留**：修改提交时仅替换本次选中的目标 `slot_id` 集合正文；服务端从 `base_revision` 的结构化章节数组按原顺序拷贝其余所有未选中及 `locked` 章节，**确保非目标章节字节级（Byte-for-Byte）绝对不变**（满足 `F-09` 验收指标 1）。重跑 `Verifier` 时，未修改槽位从上一修订 `SectionEvidence` 提取已有锚点作为 `CitedAnchors` 兜底保留，修改槽位使用持久化 `CitedAnchors`，彻底杜绝 `module_doc`/`proto_rpc`/`http_route`/`config_key` 锚点丢失。
2. **乐观并发锁**：请求体强制携带 `base_revision_no`。事务内校验 `archdoc_documents.current_revision_no == base_revision_no`，若期间已被他人修改或重生成，返回 **`409 Conflict`** 并附最新版本号，杜绝静默覆盖。
3. **不可变修订、强制 Verifier 重跑与批注闭环**：
   - 合并完整 Markdown 并重跑 `verifier.Verify()`（整仓文档开启 `module_digests` 时同步加载传入 `ModuleDigests`）；
   - 单事务内仅插入 1 条不可变行至 `archdoc_document_revisions`（`revision_no = base + 1`，`source` 取值 `manual_edit` | `chat_refine` | `rollback` | `ai_generate` | `incremental_refresh`）；
   - 同步更新 `archdoc_section_evidences`、`archdoc_documents.current_revision_no`，并将关联已采纳批注置为 `resolved`；若手工直编、新提案采纳、回滚或重生成导致旧 `pending` 提案被 `superseded`，自动将对应 `proposed` 批注回退为 `open`；
   - **一键回滚**：选择任意历史修订 `target_revision_no` 执行回滚时，并非删除历史，而是将历史修订内容作为新快照重跑 Verifier 并生成 `revision_no = current + 1`（`source = 'rollback'`），保证审计时间线完全不可变。

### 3.2 审校精修 SSE 事件流与双段切分协议（`W2` → `M12`）

前端通过 `fetch`（经 `setupFetchInterceptor` 自动带 JWT）发起 `POST /api/documents/:id/chat/refine`（`Accept: text/event-stream`），支持 `AbortController` 随时中止后端 LLM 调用（中止后清理占位消息/未决提案项，并将 `proposed` 批注安全回退为 `open`）。请求支持三条执行路径：
- **`mode = "ask"`（纯源码查证）**：放行含 `locked` 在内的任意章节及 `global` 作用域，开放 `SandboxSourceToolkit`（含 `ADR-04` `ExternalFileWhitelist`），不生成 `ChatProposalItem`、不触发 `EMPTY_PROPOSAL`；
- **`mode = "revise"` + `scope_type = "slot" | "inline"`（单槽位快路径）**：跳过阶段 1 `Plan`，注入 `inline_anchor` 微创约束、`ModuleContext` 与 `ModuleDigests`；
- **`mode = "revise"` + `scope_type = "global" | "review_batch"`（两阶段隔离精修）**：阶段 1 `Plan` 路由确定性补齐非锁定局部批注槽位（≤ 6 个）并将 `locked` 槽位强制移入 `advisory_locked`；阶段 2 按章节序串行隔离精修，外层加 `sync.Mutex` 写保护，每完成 1 槽位即落库 `ChatProposalItem` 并下发 `proposal_item`；后端通过 `StreamSplitFilter` 屏蔽 `<<<ARCHDOC_PAYLOAD_JSON>>>` 后的 JSON 字符（兼容旧版纯 JSON 自动静默缓存并补偿推送 `replyText`）。

| SSE `event` 名称 | 载荷 JSON Schema | 语义与前端交互行为 |
| :--- | :--- | :--- |
| **`meta`** | `{"session_id": 12, "message_id": 88, "slot_id": "...", "mode": "revise", "scope_type": "review_batch", "base_revision_no": 4, "model_name": "DeepSeek-V3"}` | 首包返回会话、消息与执行模式元数据 |
| **`plan`** | `{"target_slots": [...], "skipped_locked_slots": [...]}` | 阶段 1 路由结果与 `locked` 章节人工修改建议 |
| **`stage`** | `{"stage": "planning\|refining_slot\|drafting_diff\|verifying", "slot_id": "...", "index": 1, "total": 2}` | 实时结构化阶段进度指示 |
| **`tool_call`** | `{"round": 1, "tool_name": "read_file_lines", "summary": "...", "is_error": false, "duration_ms": 12, "slot_id": "..."}` | 只读源码工具轨迹（前端默认折叠展示） |
| **`delta`** | `{"text": "已结合源码核实...", "slot_id": "..."}` | 净化后的人类可读中文说明增量，零 JSON 协议噪音 |
| **`proposal_item`** | `ChatProposalItemVO`（含 `item_id`, `slot_id`, `before_md`, `after_md`, `unified_diff`, `cited_anchors`, `verifier_preview`, `addressed_comment_ids`, `status`） | 单个槽位完成即推送，前端可直接在画布原地审阅/微调 |
| **`diff_proposal`** | `{"proposal_id": 88, "slot_id": "...", "before_md": "...", "after_md": "...", "unified_diff": "...", "cited_anchors": [...]}` | 仅产出 1 个槽位提案时同步下发，保持向后兼容 |
| **`verifier_preview`** | `{"can_approve": true, "blocker_count": 0, "error_count": 0, "warning_count": 1, "issues": [...]}` | 合并全部受影响新槽位与其余未变槽位的全篇四重 Verifier 联合预检 |
| **`done`** | `{"message_id": 88, "prompt_tokens": 3200, "cached_prompt_tokens": 1900, "completion_tokens": 680, "latency_ms": 4120}` | 流式结束信号，解除前端加载态 |
| **`error`** | `{"code": "LLM_TIMEOUT", "message": "模型响应超时，请稍后重试"}` | 异常终止信号（不修改文档任何状态） |

- **采纳与拒绝**：
  - 点击汇总卡「采纳所选提案」或画布「立即采纳本节」：调用 `POST /api/documents/:id/chat/messages/:msg_id/apply`（携带 `base_revision_no` 与所选 `{item_id, override_markdown}`），单事务合并生成 1 个不可变 `Revision` 并计入 `F-26` 指标（旧 `messages/:msg_id/accept` 委托至此）；
  - 点击单项「拒绝本节」或整组拒绝：分别调用 `POST /api/documents/:id/chat/proposal-items/:item_id/reject` 或 `messages/:msg_id/reject`，仅置提案 `rejected` 并把关联 `proposed` 批注自动回退为 `open`，文档零变更。

---

## 4. 三层 RBAC 权限决策引擎与字段级审计治理（`F-13`）

### 4.1 三层权限矩阵与判定规则（`SC-01`、`SC-12`）

| 操作类别 | 匿名用户 | 普通登录用户 | 仓库负责人 (`Repo.Owner` / `RelatedMembers`) | 子目录负责人 (`ArchElem.Owner`) | 管理员 (`archdoc_admin` / `super_admin`) |
| :--- | :---: | :---: | :---: | :---: | :---: |
| `GET /api/health` | `200` | `200` | `200` | `200` | `200` |
| 浏览仓库覆盖、已发布文档、证据源码、下载、漂移看板、质量看板 | `401` | ✅ | ✅ | ✅ | ✅ |
| 浏览未发布草稿 & 修订历史 | `401` | `403` | ✅ (所属整仓及旗下子目录) | ✅ (**仅限所属子目录**) | ✅ |
| 触发生成 / 取消 / 重试 / 手工直编 / Chat 精修 / 锁定 / 发布 / 撤回 / 增量刷新 | `401` | `403` | ✅ (所属整仓及旗下子目录) | ✅ (**仅限所属子目录，跨子目录或整仓返回 `403`**) | ✅ |
| 全局 LLM 模型配置与探活、`DocType` 增删改启停、手动全量巡检 | `401` | `403` | `403` | `403` | ✅ |

### 4.2 权限判定引擎 Go 伪代码（精确落地 `SC-12`）

```go
package handlers

// CheckScopeWritePermission 判定当前用户对 (repoID, normalizedSubdir) 是否具备写/生成/发布权限
// 严格执行三层判定与子目录隔离边界（对应 F-13 与 SC-12）
func CheckScopeWritePermission(ctx context.Context, db *gorm.DB, user *commonModels.User, repoID uint, subdir string) (bool, error) {
    // 1. 管理员最高特权：super_admin 或 archdoc_admin 放行全部范围
    if user.HasRole(config.AdminRole) { // HasRole 内部同时兼容 super_admin
        return true, nil
    }

    // 2. 加载共享表 repositories
    var repo commonModels.Repository
    if err := db.WithContext(ctx).First(&repo, repoID).Error; err != nil {
        return false, err
    }

    // 3. 仓库级负责人校验：Repository.OwnerID 或 Repository.RelatedMembers 包含当前用户
    //    仓库负责人拥有该仓库（整仓及下属子目录）的文档治理权限
    if repo.OwnerID == user.ID || containsUserID(repo.RelatedMembers, user.ID) {
        return true, nil
    }

    // 4. 子目录负责人校验（仅当 subdir 非空时生效；整仓 subdir=="" 直接拒绝，满足 SC-12！）
    if subdir == "" {
        return false, nil
    }

    // 精确匹配共享表 architecture_elements 中绑定该 (repo_id, normalized_subdir) 且 owner_id == user.ID 的记录
    // 注意：数据库存储的 subdirectory 需先经 sandbox.NormalizeSubdir 规范化比对，防止 "svc/order/" vs "svc/order" 漏判，
    //      且严禁前缀模糊匹配（确保 "svc/order" 的负责人绝无权操作 "svc/order-v2" 或 "svc/pay"）！
    var elems []models.ArchitectureElement
    if err := db.WithContext(ctx).
        Where("repo_id = ? AND owner_id = ?", repoID, user.ID).
        Find(&elems).Error; err != nil {
        return false, err
    }
    for _, el := range elems {
        normElSubdir, err := sandbox.NormalizeSubdir(el.Subdirectory)
        if err == nil && normElSubdir != "" && normElSubdir == subdir {
            return true, nil
        }
    }

    return false, nil
}
```

### 4.3 全量写操作 `commonAudit` 字段级 Diff 审计埋点清单

全局路由挂载 `commonAudit.Middleware(config.ModuleKey)`，所有写接口在业务执行前调用 `commonAudit.SetBefore`，成功后调用 `commonAudit.SetAuditContext` 记录新旧值字段级 Diff：

| 业务操作 | `audit_action` | 风险等级 | `target_type` | `before` / `after` 审计记录与敏感字段脱敏规则 |
| :--- | :---: | :---: | :--- | :--- |
| 触发生成 / 增量刷新 / 取消任务 | `trigger` / `cancel` | `P1` | `gen_task` | 记录 `repo_id`, `subdir`, `engine`, `doc_types`, `created_count` / `task_ids`, `priority`, `status` |
| 手工直编章节 / Chat Diff `Accept` / 多槽位提案 `Apply` | `update` | `P1` | `document_section` | 记录 `doc_id`, `slot_id`/`slot_ids`, `revision_no`, `before_hash`/`after_hash` 与修改摘要（避免超大正文撑爆审计表） |
| 章节锁定 / 解锁 / 孤立章节处置 | `update` | `P1` | `document_section` | 记录 `slot_id` 的 `ownership` 变更前/后值（`auto` ↔ `hybrid` ↔ `locked`）或 `is_orphan` 处置结果 |
| 版本回滚 | `rollback` | `P1` | `document` | 记录 `from_revision_no` → `to_revision_no` 与新产出的 `revision_no` |
| 审批发布 / 一键撤回 | `publish` / `unpublish` | **`P0`** | `document` | 记录 `status` (`draft` ↔ `approved`)、`published_revision_id`、`published_rel_path`、`commit_sha` |
| `DocType` 新建 / 克隆 / 编辑 / 启停 / 删除 | `create` / `update` / `delete` | **`P0`** | `doc_type` | 记录 `slug`、`is_active`、`latest_revision_id`、`aggregate_hash` 与变更元数据 Diff |
| LLM 模型配置修改 | `update` | **`P0`** | `llm_config` | **强制脱敏红线**：传入 `SetBefore`/`SetAfter` 的结构体必须使用脱敏视图（包含 `opencode_model`，`api_key` 仅记录 `sk-****<last4>` 或 `"[UPDATED]"`，**严禁将明文 `APIKey` 或密文写入审计日志与系统日志**） |
| 管理员手动触发全量远端巡检 | `trigger` | `P2` | `drift_poll` | 记录触发人、巡检目标仓库数 |
