# ADR-06: Canvas Studio 审阅批注篮、多槽位综合精修与文档级审校工作台架构

- **文档编号**：`06-document-review-and-refine-studio.md`
- **日期**：2026-11-21
- **状态**：提议中 (Proposed) —— 待评审确认后同步更新阶段一~三 SDD 规范与 M12 作战卡（暂不实施）
- **关联议题**：[`01-feature-matrix.md`](../01-features/01-feature-matrix.md) (`F-08`, `F-09`, `F-10`, `F-13`, `F-15`, `F-26`, `SC-05`, `SC-12`, `SC-13`, `Non-Goal 6`)；[`04-workbench-and-security.md`](../02-design/04-workbench-and-security.md)；[`05-database-schema.md`](../02-design/05-database-schema.md)；[`01-http-api-contract.md`](../03-api/01-http-api-contract.md)；[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)；[`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md)；[`ADR-03`](./03-evidence-anchor-and-publish-mount.md)；[`ADR-05`](./05-doctype-blueprint-dual-mode-editor.md)

---

## 1. 背景与问题陈述 (Context & Problem Statement)

### 1.1 现状实现基线

当前 `code-archdoc` 已在 `W2 (M5)` 落地了基于「单槽位对话 + SSE Diff 提案」的 `Chat 精修` 能力，整体技术架构围绕 `CanvasStudioPage` 展开，现行关键实现如下：

| 维度 / 环节 | 现状实现 | 代码 / 文档位置 |
| :--- | :--- | :--- |
| **前端右侧辅助抽屉** | `RightUtilityDrawer` 固定 `440px` 宽，含 `源码证据`、`修订历史`、`Chat 精修` 3 个 Tab，顶栏按钮点击直跳目标章节 | `frontend/src/components/studio/RightUtilityDrawer.tsx`、`frontend/src/pages/CanvasStudioPage.css` |
| **Chat 精修面板** | `ChatRefineTab` 顶部放置「目标精修章节 `<select>` + 会话 `<select>`」，流式等待期间直接以 `<pre>` 展示 LLM 原始文本输出 | `frontend/src/components/studio/ChatRefineTab.tsx` |
| **提案审阅卡片** | `DiffProposalCard` 在 `max-height: 240px` 源码框中展示单槽位 `UnifiedDiff` 与 `Verifier` 预检，仅支持「全盘拒绝 / 全盘采纳 (Accept)」 | `frontend/src/components/studio/DiffProposalCard.tsx` |
| **画布正文渲染** | `SectionMarkdownRenderer` 基于零 `dangerouslySetInnerHTML` 纯函数 AST 解析器渲染标题、段落、表格、代码块与 `MermaidZoomViewer` | `frontend/src/components/studio/SectionMarkdownRenderer.tsx`、`frontend/src/utils/markdown.ts`、`frontend/src/components/studio/SectionCard.tsx` |
| **后端精修编排** | `PrepareRefine` 强制校验并绑定单一 `target_slot_id`（遇 `locked` 返 `409`）；`ExecuteRefineStream` 调用 `pkg/llm/chat_refine.go` 与 `SandboxSourceToolkit`，当 `afterMD` 为空时直接报 `EMPTY_PROPOSAL` | `handlers/chat.go`、`services/chat_service.go`、`pkg/llm/chat_refine.go`、`pkg/llm/source_tools.go` |
| **持久化与修订管线** | 共有 18 张 `archdoc_*` 表；Chat 模型为 `archdoc_chat_sessions` 与 `archdoc_chat_messages`（每条助手消息仅存 1 个槽位 Diff）；`AcceptProposal` 调 `RevisionService.ApplySectionChange` 生成 1 个单槽位不可变修订 | `models/db.go`、`models/chat.go`、`services/revision_service.go` |

### 1.2 核心问题清单

在真实「通读生成文档 → 多处指正修改 → 补充业务背景 → 审阅确认定稿」的审核场景下，现行单点问答式设计暴露出以下 **6 项核心摩擦**：

| 编号 | 问题 | 严重度 | 根因分析 |
| :--- | :--- | :---: | :--- |
| **P-1** | **手眼割裂，缺乏“指哪改哪”的正文锚定** | 🔴 | 用户在中央画布阅读时划选一段文本（如某句冗余统计或错误表述）或悬停某张 Mermaid 图/表格，界面无任何审阅交互；必须切到右侧顶部下拉框选 `target_slot_id`，再用文字反复复述原句位置。 |
| **P-2** | **单点单轮阻塞阅读心流，产生碎片修订** | 🔴 | 通读一篇技术文档通常发现 4~8 处修改意见（措辞精简、源码纠错、知识补充、图表补全）。现有模式每发现一处就必须中断阅读、发起一次 SSE 请求、等待完成并点一次 `Accept`，导致频繁打断且堆出大量碎片化 `Revision`。 |
| **P-3** | **强绑定单槽位 + 强制产出 Diff，缺全局意见与纯查证问答** | 🔴 | ① `PrepareRefine` 强绑定单个 `target_slot_id`，无法处理跨章节/全局性评论（如“全文去除 LOC/bytes 统计”、“整篇补充越权与降级边界”）；<br>② 若用户仅想划选某句向 AI 查证源码一致性（不改稿），`ExecuteRefineStream` 一旦未提取出非空 `afterMD` 即报错 `EMPTY_PROPOSAL`（`services/chat_service.go`）。 |
| **P-4** | **窄栏源码 Diff 难审阅，流式阶段裸露原始 JSON** | 🟠 | ① `440px` 窄栏内的 `240px` 高行级 Diff 框难以审阅长段、Markdown 表格或 Mermaid 图变更，且采纳前无法针对 10% 细节进行微调；<br>② LLM 输出为 `{"reply": ..., "markdown": ..., "cited_anchors": ...}`，流式 `delta` 事件直接把 JSON 协议字符与转义换行渲染在 `<pre>` 气泡中，阅读体验割裂。 |
| **P-5** | **人工业务经验与全局规范无法沉淀复用** | 🟠 | 审校过程中负责人常补充源码中无法体现的隐性业务约束或全文口径规范。当前精修仅作用于单次对话，下次重生成或增量刷新时若未将整章置为 `locked`，人工经验极易被覆盖流失。 |
| **P-6** | **缺乏结构化审阅记录载体** | 🟡 | 修改意见散落在多轮对话气泡中，缺少按“待处理 / 已出提案 / 已解决 / 已忽略”追踪的批注清单，也缺少向后续 `F-22` 全员只读读者纠错反馈平滑演进的实体底座。 |

---

## 2. 决策概览与候选方案对比 (Decision Summary & Alternatives)

### 2.1 整体架构视图

结合用户确认的**「审阅批注篮为主视图、精修对话为辅；首期仅面向文档负责人/管理员（`ScopeWrite`）自审与 AI 精修」**核心产品方向，本 ADR 将右侧面板与中央画布联动升级为**「文档审校精修台（Review & Refine Studio）」**：

```mermaid
flowchart LR
    Canvas["中央画布 (划词悬浮条 / 块级 + 入口)"] -->|"捕获 anchor_json 与 scope_type"| Basket["审阅批注篮主视图 (archdoc_review_comments)"]
    ChatUI["精修对话辅视图 (Ask 查证 / 单点快修)"] -->|"引用锚点或挂载上下文"| Engine
    Basket -->|"一键综合精修 (局部批注 + 全局评论)"| Engine["两阶段隔离精修引擎 (Plan -> Scoped Slot Refine)"]

    Engine -->|"mode = ask"| AskOut["只读源码查证回答 (不强求 Diff / 一键转修订)"]
    Engine -->|"单槽位快路径"| SlotLoop["单槽位隔离精修 (含微创提示 + 只读源码工具)"]
    Engine -->|"多槽位 / 全局批注"| PlanStage["阶段 1：Plan 路由 (命中非 locked 槽位)"]

    PlanStage -->|"locked 槽位硬降级"| Advisory["advisory_locked 人工修改建议 (零篡改)"]
    PlanStage -->|"逐个命中槽位隔离执行"| SlotLoop
    SlotLoop --> StreamSplit["双段流式切分器 (正文说明实时推送 + Payload 屏蔽 JSON)"]
    StreamSplit --> Verifier["全篇联合四重 Verifier 预检"]
    Verifier --> ProposalGroup["多槽位提案组 (archdoc_chat_proposal_items)"]

    ProposalGroup -->|"画布原地审阅 (红绿对比 / 渲染态 / 微调后采纳)"| ApplyTx["ApplyMultiSectionChanges (乐观锁 + 原子单 Revision)"]
    ApplyTx -->|"关联批注 -> resolved"| Basket
    Basket -->|"persist_for_regen = true"| RegenInject["未来重生成 / 增量刷新尾部上下文注入 (不破坏 KV Cache 前缀)"]
```

### 2.2 候选方案评估

| 候选方案 | 核心做法 | 优点 | 缺点与风险 | 结论 |
| :--- | :--- | :--- | :--- | :---: |
| **方案 A：审阅批注篮为主 + 对话为辅 + 两阶段隔离精修引擎** | 新增结构化 `archdoc_review_comments`（支持划词/块级/章节/全局）与 `archdoc_chat_proposal_items`（多槽位提案组）；后端采用 `Plan -> Scoped Slot Refine` 保证 `locked` 与未命中槽位字节级不变；前端画布支持划词/块级批注、原地渲染对比与采纳前微调。 | 贴合“先通读批注、再批量综合精修、集中审阅一次落版”的真实审校心流；100% 兼容既有 `slot_id` 隔离、`locked` 保护、乐观锁与 Verifier 体系；纯自研零重型依赖，完美契合 `Non-Goal 6`。 | 新增 2 张 `archdoc_*` 表及多阶段 SSE 事件状态管理，需分 `M12-A / B / C` 渐进交付。 | ✅ **采纳** |
| **方案 B：仅增加划词引用 + 将整篇全文一次性送 LLM 重写** | 保留纯 Chat 对话为主，划词只把文本塞进输入框；遇全局修改时把整篇 `FullMarkdown` 送给单次 LLM 请求重生成全部内容。 | 实现最简单，无需建批注表与多槽位提案表。 | 仍强迫单点逐条问答、打断通读；整篇重写极易发生模型漏改、越权篡改 `locked` 章节、超出单次输出 Token 上限及破坏非目标章节字节级不变量。 | ❌ **放弃** |
| **方案 C：引入富文本协同编辑器（TipTap / ProseMirror）重造画布** | 用富文本编辑器框架重构整个 `SectionMarkdownRenderer`，内置原生 Track Changes 与划词评论插件。 | 编辑与划词体验原生化，类似 Google Docs。 | 严重违背 `Non-Goal 6` 与现有自定义安全 AST + `MermaidZoomViewer` + 证据深链架构；Markdown 双向序列化失真风险高，且无法直接与 `slot_id` / `Code IR` / `Verifier` 解耦协作。 | ❌ **放弃** |

### 2.3 核心决策摘要

| 决策 | 核心要点 | 解决问题 |
| :--- | :--- | :--- |
| **D-1 结构化审阅批注与前端鲁棒锚定** | 新增第 19 张表 `archdoc_review_comments`（支持 `inline` / `section` / `global` 与 4 类语义标签 + `persist_for_regen`）；前端扩展 `markdown.ts` 块级行号边界并沉淀纯函数 `reviewAnchor.ts`，实现划词与 Mermaid/表格块级捕获、三级降级定位与安全高亮。 | P-1、P-2、P-5、P-6 |
| **D-2 两阶段隔离精修引擎与去 JSON 化流式协议** | 扩展 `ChatRefineRequest` 兼容 `mode` (`revise|ask`) 与批注批量入口；实现 `Ask` 纯查证模式、单槽位微创快路径、全局/多批注 `Plan -> Scoped Slot Refine` 两阶段引擎（`locked` 槽位强制降级为人工建议）；引入双段流式切分器屏蔽原始 JSON 裸露。 | P-2、P-3、P-4 |
| **D-3 多槽位提案组、微调采纳与原子批量提交** | 新增第 20 张表 `archdoc_chat_proposal_items` 并微扩展 `archdoc_chat_messages`；新增多提案组按子集/人工微调后一次采纳接口，后端收口于 `RevisionService.ApplyMultiSectionChanges`（单事务 + 乐观锁 + 全篇 Verifier + 单次新 Revision + 关联批注自动 `resolved`）。 | P-2、P-4 |
| **D-4 「批注篮为主、对话为辅」双视图与画布联动** | 重构 `ChatRefineTab` 为 `审校精修` Tab（主视图：审阅批注篮；辅视图：精修对话；抽屉支持 `440px <-> 680px` 展宽）；画布实现划词悬浮条、块级 `+` 入口、正文 `#N` 编号双向定位与章节内原地审阅态（红绿 Diff / 渲染态对比 / 微调后采纳）。 | P-1、P-2、P-4 |
| **D-5 审校记忆沉淀与首期 RBAC 边界** | 首期全部审阅与精修 API 严格复用 `ScopeWrite`（仓库/子目录负责人与管理员），预留 `origin_role` 兼容后续 `F-22` 协作；支持将 `persist_for_regen` 规范/背景注入未来重生成尾部上下文，绝不破坏 `BuildSlotMessages` 的 KV Cache 前缀（NFR-6）。 | P-5、P-6 |

---

## 3. 决策 D-1：结构化审阅批注模型与前端鲁棒锚定机制

### 3.1 新增数据模型：`archdoc_review_comments`（第 19 张自有表）

遵循 `ADR-01` 表前缀规范，在 `models/` 中新增 `ReviewComment`（表名 `archdoc_review_comments`），作为文档审核意见、划词锚点、全局补充与持久约束的统一事实源：

```go
package models

import (
	"time"
	"code-archdoc/config"
	"gorm.io/datatypes"
)

// ReviewScopeType 审阅批注作用域枚举
const (
	ReviewScopeInline  = "inline"  // 画布正文划词或块级（表格/Mermaid/段落）批注，必有 slot_id
	ReviewScopeSection = "section" // 章节级批注，必有 slot_id
	ReviewScopeGlobal  = "global"  // 全局评论、全文口径或跨章补充，slot_id 为空
)

// ReviewCategory 审阅批注语义分类（指导 LLM 权重与后续沉淀）
const (
	ReviewCategoryCorrection = "correction"  // 纠错修正（代码事实有误，优先触发只读源码工具核查）
	ReviewCategorySupplement = "supplement"  // 知识补充（业务背景/隐性约束，忠实融入且建议关注 hybrid）
	ReviewCategoryConcise    = "concise"     // 精简改写（篇幅/表述/格式调整，严禁篡改既有技术事实）
	ReviewCategoryGlobalRule = "global_rule" // 全局规范（跨章节统一风格/删减口径）
	ReviewCategoryQuestion   = "question"    // 疑问待核（查证备忘或待人工核实）
)

// ReviewCommentStatus 审阅批注生命周期状态
const (
	ReviewStatusOpen      = "open"      // 待处理（仍在审阅批注篮激活区）
	ReviewStatusProposed  = "proposed"  // 已送入综合精修并产出 pending 提案，待审阅采纳
	ReviewStatusResolved  = "resolved"  // 关联提案已采纳生效或手工直编确认已解决
	ReviewStatusDismissed = "dismissed" // 已忽略 / 撤回取消
)

// ReviewOriginRole 批注来源角色（首期恒为 owner，为 P1 F-22 读者协审预留零破坏演进位）
const (
	ReviewOriginOwner    = "owner"
	ReviewOriginReviewer = "reviewer"
)

// ReviewCommentAnchor 存储在 archdoc_review_comments.anchor_json 中的 W3C TextQuoteSelector 风格定位器
type ReviewCommentAnchor struct {
	BlockIndex      int    `json:"block_index,omitempty"`       // 对应 SectionMarkdownRenderer 的块序（0-based）
	BlockType       string `json:"block_type,omitempty"`        // paragraph | list | table | mermaid | code | blockquote | heading
	Quote           string `json:"quote,omitempty"`             // 划选原文摘录（纯文本/源码片段，长度上限 500 字符）
	Prefix          string `json:"prefix,omitempty"`            // 选区前导上下文（最多 48 字符，消除同词歧义）
	Suffix          string `json:"suffix,omitempty"`            // 选区后继上下文（最多 48 字符）
	SourceLineStart int    `json:"source_line_start,omitempty"` // 该块在章节原始 Markdown 中的起止行号（1-based）
	SourceLineEnd   int    `json:"source_line_end,omitempty"`
}

// ReviewComment 文档审阅批注实体（第 19 张 archdoc_* 自有表）
type ReviewComment struct {
	ID                 uint           `gorm:"primaryKey" json:"id"`
	DocumentID         uint           `gorm:"not null;index:idx_archdoc_rc_doc_status,priority:1" json:"document_id"`
	BaseRevisionNo     int            `gorm:"not null;default:0" json:"base_revision_no"`
	AuthorID           uint           `gorm:"not null;index" json:"author_id"`
	OriginRole         string         `gorm:"size:16;not null;default:'owner'" json:"origin_role"`
	ScopeType          string         `gorm:"size:16;not null;default:'inline'" json:"scope_type"`
	SlotID             string         `gorm:"size:64;not null;default:'';index" json:"slot_id"`
	AnchorJSON         datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"anchor"`
	Category           string         `gorm:"size:24;not null;default:'correction'" json:"category"`
	Content            string         `gorm:"type:text;not null" json:"content"`
	PersistForRegen    bool           `gorm:"default:false;not null;index" json:"persist_for_regen"`
	Status             string         `gorm:"size:20;not null;default:'open';index:idx_archdoc_rc_doc_status,priority:2" json:"status"`
	ProposalGroupMsgID *uint          `gorm:"index" json:"proposal_group_msg_id,omitempty"`
	ResolvedRevisionID *uint          `json:"resolved_revision_id,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func (ReviewComment) TableName() string { return config.TablePrefix + "review_comments" }
```

**批注状态机核心不变量**：
1. 新建批注默认状态为 `open`，支持编辑正文、切换 `category`、`persist_for_regen`、标记为 `dismissed` 或物理删除（仅限 `open`/`dismissed` 状态的本人草稿）；
2. 发起 `review_batch` 综合精修并成功生成提案组后，本次携带的 `open` 批注原子更新为 `proposed` 并关联 `proposal_group_msg_id`；若中途取消 SSE 或模型报错，状态回滚/保持为 `open`；
3. 当关联提案项被 **采纳（Apply）** 时，该提案项覆盖的批注全部转为 `resolved` 并记录 `resolved_revision_id`；若用户采纳「全局约束」但部分槽位未被包含或被 **拒绝（Reject）**，其关联的批注自动从 `proposed` **回退为 `open`**，确保审阅意见绝不悄无声息丢失。

### 3.2 前端零依赖锚定与三级降级匹配 (`frontend/src/utils/reviewAnchor.ts`)

为保持 [markdown.ts](file:///home/fugui/codes/code-archdoc/frontend/src/utils/markdown.ts) 零第三方重型编辑器与零 `dangerouslySetInnerHTML` 的安全基线，采用轻量、可单测的纯函数锚定体系：

1. **块级 AST 增补行号元信息**：在 `parseMarkdownBlocks` 的 `MarkdownBlockNode` 各分支上补充可选元数据 `startLine` / `endLine`（1-based 章节内源码行号）。对现有渲染逻辑 100% 向后兼容。
2. **双入口精准捕获**：
   - **划词捕获**：在 `SectionMarkdownRenderer` 外层监听 `onMouseUp`，通过 `window.getSelection()` 判断选区起止节点必须落在同一 `data-slot-id` 容器内（若用户跨章节拖拽选区，弹窗提示「跨章节选区已自动转为全局意见」并降级为 `global`）；从最近的 `[data-block-index]` DOM 节点读取 `block_index`、`block_type`、`startLine`、`endLine`，从该块渲染纯文本中提取 `quote`（≤ 500 字符）及前后各 48 字符 `prefix` / `suffix`。
   - **块级悬浮 `+` 入口**：针对 `mermaid` 图、`table` 表格、`code` 代码块等不易划选文字的块，在每个块级容器渲染 `data-block-index`、`data-block-type` 及悬浮 `+ 批注` 微按钮；点击后直接生成 `quote = "[Mermaid 图表]"` 或 `"[表格: 列1 / 列2...]"` 的块级锚点。
3. **三级降级定位与高亮渲染**：
   - 渲染章节时，根据当前激活且未解决（`open` / `proposed`）的批注，按以下顺序定位：
     1. **Level 1（精确上下文命中）**：在 `block_index` 对应块文本中同时匹配 `prefix + quote + suffix` 或唯一 `quote`；
     2. **Level 2（块重排容错）**：遍历同 `slot_id` 全部块，按 `prefix + quote + suffix` 模糊滑窗匹配（兼容人工直编插删段落导致 `block_index` 偏移的情况）；
     3. **Level 3（块级整体 / 失锚降级）**：仅带 `block_index` 却无文本 `quote` 的图表/表格，高亮对应块外框并显示 `#N` 角标；若因章节大幅改写导致文本彻底失配，不强行错位高亮，在章节顶部以 `失锚待核批注 (#N)` 徽标聚合呈现，仍可定位与一键删除。
   - 文本高亮严格在 `SectionMarkdownRenderer` 的 React 文本节点拆分阶段插入安全 `<mark className="code-archdoc-review-mark">` 与编号角标 `<sup className="code-archdoc-review-pin">#1</sup>`，**严禁任何 HTML 字符串拼接**。

---

## 4. 决策 D-2：两阶段隔离精修引擎 (`Plan → Scoped Slot Refine`)、`Ask` 查证模式与去 JSON 化流式协议

### 4.1 `ChatRefineRequest` 契约向后兼容扩展

保留既有字段零破坏，在 `services.ChatRefineRequest` 与前端 `ChatRefinePayload` 中增量扩展：

```go
type ChatRefineRequest struct {
	SessionID      *uint                        `json:"session_id"`
	Mode           string                       `json:"mode"`            // "revise" (默认，兼容旧请求) | "ask" (纯查证讨论)
	ScopeType      string                       `json:"scope_type"`      // "slot" (默认) | "inline" | "global" | "review_batch"
	TargetSlotID   string                       `json:"target_slot_id"`  // slot/inline 必填；global/review_batch 可为空
	BaseRevisionNo int                          `json:"base_revision_no"`
	Prompt         string                       `json:"prompt"`          // 用户补充指令（review_batch 下可为空，由批注篮汇总驱动）
	CommentIDs     []uint                       `json:"comment_ids"`     // review_batch 时指定要综合处理的 open 批注 ID 列表
	InlineAnchor   *models.ReviewCommentAnchor  `json:"inline_anchor"`   // inline 快修或划词查证时携带的选区上下文
}
```

### 4.2 三条执行路径与硬安全守卫

```mermaid
flowchart TD
    Req["PrepareRefine 接收请求"] --> ModeCheck{"判断 Mode 与 ScopeType"}

    ModeCheck -->|"mode == ask"| PathAsk["路径 1：Ask 只读源码查证模式"]
    PathAsk --> AskTools["开放 SandboxSourceToolkit 只读工具 + 引用选区/全篇摘要"]
    AskTools --> AskSave["保存助手纯文本/Markdown 回复 (ProposalStatus = none, 不报 EMPTY_PROPOSAL)"]

    ModeCheck -->|"revise + 单槽位 (slot / inline)"| PathSingle["路径 2：单槽位快路径 (零 Plan 延迟)"]
    PathSingle --> SingleLock{"目标槽位是否 locked?"}
    SingleLock -->|"是"| Reject409["返回 409 CONFLICT (ErrSlotLocked)"]
    SingleLock -->|"否"| SingleRefine["注入 inline_anchor 微创约束 + IRFacets + 证据 + 只读工具 -> 单提案"]

    ModeCheck -->|"revise + global / review_batch"| PathBatch["路径 3：两阶段隔离精修管线"]
    PathBatch --> P1["阶段 1：Plan 路由 (输入各槽位元信息/前300字摘要/所有权 + 批注列表)"]
    P1 --> Guard["后端确定性合并与硬拦截：1) 补齐局部批注所属非锁定槽位；2) locked 槽位强制转 advisory_locked"]
    Guard --> P2["阶段 2：逐槽位严格隔离精修 (仅加载该槽位原文 + 命中批注 + 全局约束 + 该槽位 IRFacets + 源码工具)"]
    P2 --> JointVerify["合并全部修改后槽位 + 未命中/locked 槽位原文 -> 执行全篇四重 Verifier 预检"]
```

核心硬规则说明：
1. **`locked` 章节不可被 AI 改写的绝对红线**：
   - 在单槽位直修路径中，保持现有 `ErrSlotLocked`（HTTP `409`）硬拦截不变；
   - 在 `global` / `review_batch` 综合精修中，即便 Plan 阶段认为某个 `locked` 槽位受全局批注影响，后端也**绝不生成写入该槽位的 `Diff` 提案**，而是将其归类为 `advisory_locked` 写入 `plan_summary_json` 与回复说明（提示负责人：“以下内容涉及已锁定章节 `xxx`，已保留原文未改，建议您手工直编调整”），从而确保批量/全局精修同样 100% 遵守 `F-10` 锁定豁免。
2. **Plan 阶段防漏改确定性兜底**：
   - 阶段 1 `Plan`（在 `pkg/llm/review_plan.go` 中实现纯函数解析与 Prompt 组装）仅向模型传递文档大纲（`slot_id`、`title`、`ownership`、每章前 300 字摘录与统计）+ 批注篮内容，输出 `target_slots: [{slot_id, reason, comment_ids}]`。
   - 为避免模型路由漏掉用户明确标在某章上的批注，后端执行**确定性并集兜底**：凡用户提交的 `inline` / `section` 批注指向某非 `locked` 槽位，强制将其纳入 `target_slots`；仅当只有 `global` 意见时由 `Plan` 自动筛选相关非锁定槽位（单次最多允许并发精修 ≤ 6 个槽位，超限时提示分批并优先处理直接命中槽位）。
3. **单槽位微创约束**：
   - 当请求携带 `inline_anchor` 或局部 `inline` 批注时，Prompt 明确注入指令：“仅围绕定位摘录与上下文实施精准修订，严禁随意改写、重排或删减本章节其他无关段落”，避免 AI 为改一句话而重写整节引发巨大 Diff 噪音。
4. **`Ask` 纯查证模式闭环**：
   - 允许在任何槽位（包括 `locked` 章节）或全文作用域下以 `mode = "ask"` 发起提问；流式推送 `delta` 与 `tool_call` 轨迹，结束时不解析 `afterMD`、不写入任何 `ProposalItem`，彻底消除 `EMPTY_PROPOSAL` 报错。前端在查证气泡底部提供 **「按此结论生成修正提案」** 按钮，点击后一键以当前结论作为补充指令发起 `revise`。

### 4.3 双段流式输出协议与增量切分器 (`pkg/llm/stream_splitter.go`)

针对当前流式阶段直接向用户 `<pre>` 暴露 `{"reply": "...", "markdown": "..."}` 原始 JSON 的体验缺陷（**P-4**），统一约定新版分隔式输出协议并保持对内网弱指令遵循模型的 100% 向下兼容：

1. **LLM 双段分隔输出格式**：
   ```text
   <<<ARCHDOC_REPLY>>>
   （面向负责人的清晰中文修改综述或查证说明，逐字通过 SSE delta 实时流式推送给前端渲染）
   <<<ARCHDOC_PAYLOAD_JSON>>>
   {"markdown": "修订后的章节完整 Markdown 正文", "cited_anchors": [{"file_path": "...", "fq_symbol": "..."}]}
   ```
2. **纯 Go 增量切分状态机 (`StreamSplitFilter`)**：
   - 在 `pkg/llm/stream_splitter.go` 实现带边界缓冲区的流式过滤器，能够正确处理分隔符 `<<<ARCHDOC_PAYLOAD_JSON>>>` 被任意拆分跨越多个 SSE chunk 的边界情况；
   - 在遇到 `<<<ARCHDOC_PAYLOAD_JSON>>>` 之前：自动剥离前导 `<<<ARCHDOC_REPLY>>>`，将人类可读的修改说明实时通过 `emit("delta", ...)` 推送；
   - 一旦检测到 `<<<ARCHDOC_PAYLOAD_JSON>>>`：立即停止向用户气泡推送 JSON 原文，改为切换推送轻量状态事件 `emit("stage", {"stage": "drafting_diff", "slot_id": ...})`，后端在内存中继续收齐 JSON 载荷直至完成；
   - **向后兼容降级**：若部分内网模型未遵守分隔符而直接从首字符输出 `{`（旧版 JSON）或直接输出纯 Markdown，状态机检测到首非空字符为 `{` 时暂缓推送原始 JSON 到 `delta`（改为推送 `stage` 进度），结束后统一交由 `ParseChatRefineOutput` 解析出 `reply` 与 `markdown` 后补发展示，彻底杜绝界面出现乱码 JSON。
3. **升级后的 SSE 事件契约**（完全兼容既有 `meta` / `tool_call` / `delta` / `diff_proposal` / `verifier_preview` / `done` / `error`）：

| SSE `event` 名称 | 新增 / 现有 | 核心载荷字段与前端语义 |
| :--- | :---: | :--- |
| **`meta`** | 现有增强 | `session_id`, `message_id`, `mode`, `scope_type`, `base_revision_no`, `model_tier` |
| **`plan`** | **新增** | `target_slots: [{slot_id, title, comment_ids, reason}]`, `skipped_locked_slots: [{slot_id, title, advice}]` |
| **`stage`** | **新增** | `stage` (`planning` \| `refining_slot` \| `drafting_diff` \| `verifying`), `slot_id`, `index`, `total` |
| **`tool_call`** | 现有 | `round`, `tool_name`, `summary`, `is_error`, `duration_ms`（前端默认折叠展示） |
| **`delta`** | 现有净化 | `text`, `slot_id`（仅含人类可读说明文本，零 JSON 转义噪音） |
| **`proposal_item`** | **新增** | 单个 `ChatProposalItemVO`（`item_id`, `slot_id`, `before_md`, `after_md`, `unified_diff`, `verifier_preview`, `addressed_comment_ids`） |
| **`diff_proposal`** | 现有保留 | 当仅产出 1 个槽位提案时同步下发，确保既有单槽位调用方零破坏 |
| **`verifier_preview`** | 现有增强 | 全篇联合预检结果 `ChatVerifierPreviewVO`（基于所有受影响新槽位 + 其余未变槽位共同验证） |
| **`done`** | 现有 | `message_id`, `prompt_tokens`, `cached_prompt_tokens`, `completion_tokens`, `latency_ms` |
| **`error`** | 现有 | `code`, `message` |

---

## 5. 决策 D-3：多槽位提案组、采纳前微调与单事务原子批量采纳

### 5.1 新增数据模型与 `archdoc_chat_messages` 增量扩展

1. **现有表增量列（`models/chat.go` `ChatMessage`，100% 向后兼容）**：
   - `Intent` (`string`, `size:16`, `default:'revise'`): `revise` | `ask`
   - `ScopeType` (`string`, `size:20`, `default:'slot'`): `slot` | `inline` | `global` | `review_batch`
   - `PlanSummaryJSON` (`datatypes.JSON`, `type:jsonb`, `default:'{}'`): 记录阶段 1 路由目标槽位、跳过原因及对 `locked` 槽位的 `advisory_locked` 建议
   - `ContextRefsJSON` (`datatypes.JSON`, `type:jsonb`, `default:'[]'`): 记录本次提问引用的选区、槽位与 `comment_ids`
   > 说明：单槽位旧路径下继续写入 `ProposedBeforeMD` / `ProposedAfterMD` / `UnifiedDiff` 字段并同步落一条 `archdoc_chat_proposal_items` 记录（或在读取时统一聚合 `ProposalItems []ChatProposalItemVO`），确保历史数据无须全表迁移即可完美展示。

2. **新增表 `archdoc_chat_proposal_items`（第 20 张自有表）**：

```go
// ChatProposalItem 一次综合精修消息下包含的单槽位 Diff 提案项（第 20 张 archdoc_* 自有表）
type ChatProposalItem struct {
	ID                     uint           `gorm:"primaryKey" json:"id"`
	MessageID              uint           `gorm:"not null;uniqueIndex:idx_archdoc_cpi_msg_slot,priority:1;index" json:"message_id"`
	DocumentID             uint           `gorm:"not null;index" json:"document_id"`
	SlotID                 string         `gorm:"size:64;not null;uniqueIndex:idx_archdoc_cpi_msg_slot,priority:2;index" json:"slot_id"`
	BaseRevisionNo         int            `gorm:"not null;default:0" json:"base_revision_no"`
	ProposedBeforeMD       string         `gorm:"type:text;not null;default:''" json:"proposed_before_md"`
	ProposedAfterMD        string         `gorm:"type:text;not null;default:''" json:"proposed_after_md"`
	FinalAppliedMD         string         `gorm:"type:text;not null;default:''" json:"final_applied_md"` // 记录人工微调后的实际落库内容
	UnifiedDiff            string         `gorm:"type:text;not null;default:''" json:"unified_diff"`
	CitedAnchorsJSON       datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"cited_anchors"`
	VerifierPreviewJSON    datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"verifier_preview"`
	AddressedCommentIDsJSON datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'" json:"addressed_comment_ids"`
	Status                 string         `gorm:"size:20;not null;default:'pending';index" json:"status"` // pending | accepted | rejected | superseded
	AppliedRevisionID      *uint          `json:"applied_revision_id,omitempty"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

func (ChatProposalItem) TableName() string { return config.TablePrefix + "chat_proposal_items" }
```

### 5.2 HTTP API 增量契约（均挂载 `ScopeWrite` 守卫）

在保留原有 `/api/documents/:id/chat/*` 4 个接口 100% 向后兼容的基础上，新增以下 REST 接口：

| 方法 | 路径 | 权限 | 审计等级 | 职责说明 |
| :--- | :--- | :---: | :---: | :--- |
| `GET` | `/api/documents/:id/review-comments` | `ScopeWrite` | — | 获取文档审阅批注列表（支持按 `status` / `scope_type` / `slot_id` / `persist_for_regen` 筛选） |
| `POST` | `/api/documents/:id/review-comments` | `ScopeWrite` | — | 新建划词、块级、章节或全局审阅批注（校验 `slot_id` 合法性、`quote <= 500` 字符、`content` 非空） |
| `PATCH` | `/api/documents/:id/review-comments/:comment_id` | `ScopeWrite` | — | 修改批注正文、分类标签、`persist_for_regen` 开关或手动流转状态（`resolved` / `dismissed` / `open`） |
| `DELETE` | `/api/documents/:id/review-comments/:comment_id` | `ScopeWrite` | — | 删除未进入不可变历史关联的批注草稿（`open` / `dismissed`） |
| `POST` | `/api/documents/:id/chat/messages/:msg_id/apply` | `ScopeWrite` | `P1` | **统一提案组批量/微调采纳**：请求体 `{ base_revision_no, items: [{ item_id, override_markdown?, change_summary? }] }`，支持勾选部分或全部提案项、可选携带微调正文，单事务合并生成 1 个新修订 |
| `POST` | `/api/documents/:id/chat/proposal-items/:item_id/reject` | `ScopeWrite` | — | 拒绝提案组内的单个槽位提案项（其余未处置项保持 `pending`），关联批注回退 `open` |

> 旧版 `POST /api/documents/:id/chat/messages/:msg_id/accept` 与 `.../reject` 内部转发或同步更新对应单条 `ChatProposalItem`，确保现有前端/测试无任何破坏。

### 5.3 `RevisionService.ApplyMultiSectionChanges` 统一事务核心

在 [revision_service.go](file:///home/fugui/codes/code-archdoc/services/revision_service.go) 中抽象统一的多槽位原子更新方法 `ApplyMultiSectionChanges`（现有 `ApplySectionChange` 直接委托给单元素切片调用，**消除重复逻辑**）：

1. **前置校验与去重**：校验 `base_revision_no > 0` 及目标 `slot_id` 列表中无重复槽位且全部属于当前文档；
2. **乐观锁并发拦截**：事务内校验 `doc.CurrentRevisionNo == base_revision_no`，不一致立即返回 `409 OPTIMISTIC_LOCK_CONFLICT`；
3. **非目标槽位字节级不变**：深拷贝 `prevRev` 章节切片，仅替换本次采纳的 `slot_id` 列表对应的 `Markdown`、`ContentSHA256`、`ManuallyEdited = true`，**其余所有未选中及 `locked` 槽位逐字节原样拷贝**；
4. **联合重跑四重 Verifier 与单一修订落盘**：合并全篇重新运行 `verifier.Verify()`，生成 **仅 1 条** `revision_no = base + 1` 的不可变 `DocumentRevision`（`source = 'chat_refine'`，`source_ref_id = &msg.ID`）；
5. **状态机与指标联动**：
   - 将本次采纳的 `ChatProposalItem` 标记为 `accepted` 并记录 `FinalAppliedMD` 与 `AppliedRevisionID`；
   - 若该消息下所有 `ChatProposalItem` 均已终态（全部 `accepted` 或 `rejected`），同步收敛父 `ChatMessage.ProposalStatus`；
   - 同文档同受影响槽位的其他旧 `pending` 提案项标记为 `superseded`；
   - 将本次采纳提案项覆盖的 `archdoc_review_comments` 置为 `resolved`（未选中或被拒绝的提案项所关联批注退回 `open`）；
   - 按实际采纳/拒绝的提案项粒度写入 `F-26` `QualityMetric`（`chat_proposal_resolved`），并记录一条包含变更槽位清单与前后哈希摘要的 `P1` 审计日志。

---

## 6. 决策 D-4：「审阅批注篮为主、精修对话为辅」双视图交互与画布联动

### 6.1 工作台整体视觉与组件结构设计

将右侧抽屉原 `Chat 精修` 标签升级为 **`审校精修`**（存在待处理 `open` 批注或 `pending` 提案时展示数字徽标，如 `审校精修 (3)`），并在抽屉顶栏右侧增加 **`440px ↔ 680px` 展宽切换按钮**（同步扩展 `.code-archdoc-studio-grid--with-drawer-wide`）：

```text
┌─────────────────── 中央文档画布 (Center Canvas) ──────────────────┐┌────────── 右侧：审校精修台 (440px <-> 680px) ─────────────┐
│  1.2 核心模块与文件职责                                            ││ [源码证据]  [修订历史]  [● 审校精修 (3)]        [⤢ 展宽] [×] │
│  • 页面层：src/pages 共 9 个文件。DeveloperDocs.tsx               │├────────────────────────────────────────────────────────────┤
│    ┌───────────────────────────────────────────────────┐          ││  [ 审阅批注篮 (3) ]   [ 精修对话 ]         [🕒 会话历史 ▾]  │
│    │ 💬 添加批注  |  ✨ AI 立即修正  |  ❓ 查证源码       │          │├────────────────────────────────────────────────────────────┤
│    └────────────────────────┬──────────────────────────┘          ││  ● 全局评论与约束 · 1 条                       [+ 添全局]   │
│    【体量最大（1,919 LOC / 74,901 bytes）】 [ #1 ] ;     (+)       ││  ┌──────────────────────────────────────────────────────┐ │
│    FeedbackCenter.tsx (1,142 LOC)...                              ││  │ [全局规范] 全文不要罗列 LOC 和 bytes 数值，改为职责描述  │ │
│                                                                   ││  │ ☑ 记为文档持久约束（重生成时自动保留）        [编辑][×] │ │
│  • 组件层：DynamicRemoteApp.tsx ...                      (+)       ││  └──────────────────────────────────────────────────────┘ │
│                                                                   ││                                                            │
│  [ Mermaid 图表 / 表格区域 ]                             (+)       ││  ● 局部指点清单 · 2 条                       按章节 / 顺序 ▾ │
│    ↑ 鼠标悬停任何段落/表格/Mermaid 时右侧出现 (+) 块级批注按钮       ││  ┌──────────────────────────────────────────────────────┐ │
│                                                                   ││  │ #1  1. 仓库目录职责 · [精简改写 ▾]            [定位]  │ │
│  ────────── 当存在待审阅提案时，章节顶栏展开审阅条 ──────────       ││  │ ┃ “体量最大（1,919 LOC / 74,901 bytes）”              │ │
│  [🔍 有 1 项 AI 提案待审阅 (来自批注 #1)]                          ││  │ 改为说明该页面承载了文档展示与编辑编排，不必强调体积。     │ │
│  切换视图：[红绿 Diff] [渲染态对比] [微调后采纳]                     ││  └──────────────────────────────────────────────────────┘ │
│  [拒绝]                                          [采纳本节提案]     ││  ┌──────────────────────────────────────────────────────┐ │
│                                                                   ││  │ #2  2. 核心请求链路 · [知识补充 ▾]            [定位]  │ │
│                                                                   ││  │ ┃ [Mermaid 时序图]                                   │ │
│                                                                   ││  │ 补充 401 Token 过期时的自动跳转登录逻辑。               │ │
│                                                                   ││  └──────────────────────────────────────────────────────┘ │
│                                                                   │├────────────────────────────────────────────────────────────┤
│                                                                   ││  补充汇总指令（可选）：如优先保留简洁表格形式...             │
│                                                                   ││  [仅查证源码]              [✨ 一键综合精修 (处理 3 条意见)] │
└───────────────────────────────────────────────────────────────────┘└────────────────────────────────────────────────────────────┘
```

### 6.2 核心交互流转明细

1. **主视图：审阅批注篮 (`ReviewBasketPanel.tsx`)**
   - **进入即用**：打开右侧「审校精修」默认进入审阅批注篮，顶部按「全局评论与补充」与「局部指点清单（按阅读序 `#1, #2...` 排列）」分层展示；
   - **快捷画布采集**：用户在左侧或中央通读全文，**划选文本**弹出轻气泡（`💬 添加批注` / `✨ AI 修正` / `❓ 查证源码`），或悬停 Mermaid/表格点击 **`+`** 直接就地填入意见回车入篮，视线无需离开正文；
   - **全局评论入口**：随时点击顶部 **`+ 添全局`** 添加全局评论/背景补充，可勾选 **`记为文档持久约束`**（`persist_for_regen = true`）；
   - **批注卡片操作**：每张卡片支持点击「定位」平滑滚动并闪烁画布对应锚点、切换分类胶囊（`纠错修正` / `知识补充` / `精简改写` / `全局规范`）、直接编辑/删除，或勾选子集；
   - **一键综合精修（主 CTA）**：点击底部 **`✨ 一键综合精修 (处理 N 条意见)`**，自动切换至「精修对话」实时追踪 `Plan 路由 -> 逐章节修改说明流式推送 -> 只读源码工具轨迹（可折叠） -> 多槽位提案组 + Verifier 联合预检`；
   - **顶部汇总审阅栏**：综合精修产出多槽位提案后，流内展示 **提案组汇总卡（Proposal Group Card）**，支持勾选全部分片或子集，一键执行 **「采纳所选提案 (共 M 个章节 · 合并生成 1 个修订)」**。

2. **辅视图：精修对话 (`ChatRefineThreadPanel.tsx`)**
   - **释放垂直空间**：将原本占高 60px+ 的会话 `<select>` 收纳到右上角轻量 **`🕒 会话历史 ▾`** 弹层与 **`+ 新会话`** 按钮；
   - **作用域胶囊栏**：输入框上方以紧凑胶囊切换 `[🌐 全文自动路由]` / `[§ 当前章节]` + 可挂载多个划词引用标签 `[📎 "体量最大..." ×]`；
   - **双意图按钮**：底部并列 **`[仅查证源码]`**（触发 `mode = "ask"`）与 **`[发送精修]`**（触发 `mode = "revise"`），附带常用快捷芯片（如「精简冗余数字」「核对源码一致性」「补充异常处理链路」）；
   - **可折叠工具轨迹与结构化进度**：工具调用与 Stage 进度以紧凑时间条呈现，完全告别裸露 JSON。

3. **画布原地审阅态 (`SectionReviewOverlay` 集成于 `SectionCard.tsx`)**
   - 只要某章节存在 `pending` 状态提案，该章节卡片顶栏自动出现「待审阅提案」状态条，支持在宽阔的中央画布区**原地切换 3 种审阅视角**：
     - **`红绿 Diff`**：自动换行的行/词级差异对比；
     - **`渲染态对比`**：以 `SectionMarkdownRenderer` 并排或切换渲染修改前后的真实效果（**重点解决 Mermaid 图表与 Markdown 表格无法从源码 Diff 直观审查的痛点**）；
     - **`微调后采纳`**：一键将 `proposed_after_md` 载入已有的 `SectionInlineEditor`，人工微调细节后点击「保存并采纳」，经 `ApplyMultiSectionChanges` 携带 `override_markdown` 落库为单次修订并同步关闭对应批注。

### 6.3 视觉与前端工程合规约束

严格遵循工作区 `AGENTS.md` 与 `Non-Goal 6`：
- **零重型 UI 库**：全部由原生 React 18 + Vanilla CSS（`CanvasStudioPage.css` 及拆分样式）+ `@code/common` 基础组件 + `lucide-react` 图标构建；
- **100% 双模主题 Token**：划词气泡、高亮 `<mark>`、编号角标、提案卡片必须使用 `--color-*` 语义变量（如 `--color-bg-surface`、`--color-warning-subtle`、`--color-primary-subtle`、`--color-border-primary`、`--shadow-lg`），禁止任何硬编码 `#fff`、`#000` 或 `rgba(...)`；
- **Flat BEM 与类型安全**：新增样式统一归属 `.code-archdoc-review-*` 命名空间，禁止静态内联 `style={{ ... }}`（仅真动态定位坐标 `left`/`top` 例外）、禁止 JS 模拟 hover 伪类、TypeScript 严格零 `any`。

---

## 7. 决策 D-5：文档级审校记忆沉淀与三层 RBAC 边界

### 7.1 首期权限边界与未来协作预留

1. **首期严格对齐 `ScopeWrite`**：按照当前决策范围，所有新增接口（`/api/documents/:id/review-comments*`、`/api/documents/:id/chat/messages/:msg_id/apply`、`/api/documents/:id/chat/proposal-items/:item_id/reject`）统一复用 `handlers/document.go` 中的 `resolveDocumentAccess(c, true, false)`，即**仅允许所属仓库负责人（`Repository.Owner` / `RelatedMembers`）、对应子目录负责人（`ArchitectureElement.Owner`）及管理员（`archdoc_admin` / `super_admin`）访问与操作**；
2. **普通用户红线不变**：普通登录用户在未发布草稿上依然返回 `403 DRAFT_ACCESS_DENIED`，在已发布文档上依然只读浏览且不展示审校精修入口；
3. **平滑演进位**：`archdoc_review_comments.origin_role`（`owner | reviewer`）与 `author_id` 字段在数据库层预先就位，未来迭代 `F-22`（全员只读读者纠错反馈）时，仅需放开已发布文档上普通用户的只读批注 `POST` 权限并加标 `origin_role = "reviewer"`，无需重构表结构与主流程。

### 7.2 文档级持久审校约束 (`persist_for_regen`) 与知识回流

1. **严格作用域隔离**：标记了 `persist_for_regen = true` 的审阅批注（典型为 `global_rule` 全局口径规则或 `supplement` 业务约束）**仅绑定于当前 `DocumentID`**，绝不反向污染全局 `DocType` 模板，也绝不绕过目标章节的 `locked` 豁免机制（仅注入给 `auto` 与 `hybrid` 槽位）；
2. **保护 KV Cache 前缀命中率 (`NFR-6`)**：
   - 在 [pkg/llm/prompt.go](file:///home/fugui/codes/code-archdoc/pkg/llm/prompt.go) 的 `SlotPromptInput` 中新增可选字段 `ReviewGuidance string`；
   - 在 `BuildSlotMessages` 中**严禁修改前序 `system` 与共享 `shared_glossary` / `shared_ir_overview` 稳定前缀**，必须将 `ReviewGuidance` 追加至用户消息第 3 段（槽位专属尾部，采用 `<untrusted_repo_data>` 数据栅栏包裹，硬截断上限 `2000` 字符，且按时间升序确定性拼接），确保跨文档/同快照槽位并发生成的 Prompt KV Cache 前缀完全不受影响；
3. **所有生成物仍受 Verifier 硬门禁控制**：即使人工注入了补充背景或规则，L2 产出依然必须通过四重确定性 Verifier（符号路径存在性、Mermaid 门禁、凭据零泄漏等），杜绝因不当全局指令引发幻觉或凭据外泄；
4. **`hybrid` 所有权友好联动**：当负责人采纳了包含 `category == "supplement"` 的提案项，且目标章节当前为 `auto` 模式时，前端在成功 Toast 后弹出轻量非强制提醒，提供一键将本章节切换为 `人机协同 (hybrid)` 的快捷动作；并在批注篮顶部提供「持久约束管理」查看与关闭入口。

---

## 8. 决策后果、风险缓解与架构红线 (Consequences & Guardrails)

### 8.1 正向收益

1. **审校心流无缝闭环**：实现类似 GitHub PR Review + 飞书划词批注的“边通读边标记、全局+局部统一综合精修、画布原地渲染对比、一键单修订落库”完整体验。
2. **精确度与可控性跃升**：划词与块级锚定将 AI 修订聚焦到具体段落或 Mermaid 图；`Ask` 模式实现零改稿纯源码查证；双段流式协议彻底消除流式阶段裸露 JSON。
3. **版本历史干净可溯**：一次综合审校处理 5~10 条意见只生成 **1 个** 清晰的 `DocumentRevision`，并与解决的 `archdoc_review_comments` 双向可追溯。
4. **知识可持续积累**：文档级持久约束（`persist_for_regen`）让负责人踩过的坑与业务隐性认知跨重生成保留，减少重复劳动。

### 8.2 代价与风险缓解

| 风险点 | 潜在影响 | 缓解措施 |
| :--- | :--- | :--- |
| **多槽位并发精修触发 LLM 超时或成本陡增** | 一条全局评论若命中所有 8 个槽位并全部开启源码工具，可能引发单次请求时间过长与 Token 消耗过高。 | ① Plan 阶段强制限制单次综合精修最多命中非锁定槽位 **≤ 6 个**（优先直连批注槽位，超出提示分批）；<br>② 槽位精修默认按有限并发（受控于 `max_concurrency = 2`）串联/小并发执行并受原 `AbortController` 秒级中断保护；<br>③ 每次槽位调用独立记录 `LLMUsageLog`，支持单槽位完成即推送 `proposal_item` 供先看先审。 |
| **全局指令诱导模型篡改 `locked` 章节** | 负责人提交全局规则后误以为锁定章节也被改写或担心模型越界篡改。 | 后端 `PrepareRefine` 与 `Plan` 后置校验对 `locked` 槽位实行**硬隔离**，只生成 `advisory_locked` 纯文字提醒而不产生任何 `ChatProposalItem`；`ApplyMultiSectionChanges` 再次校验禁止修改 `locked` 内容或保持未选定槽位字节级不变。 |
| **划词锚点因用户直编或重生成而失配** | 文档内容改动后，旧 `quote` / `block_index` 可能无法匹配到高亮位置。 | 采用 `prefix + quote + suffix` 三级滑窗降级定位；无法命中时不崩溃、不乱标，退化至章节顶部「失锚批注」条供用户确认或清理。 |
| **部分内网小模型不严格输出分隔符 `<<<ARCHDOC_PAYLOAD_JSON>>>`** | 切分状态机误判或漏解析导致丢失 Diff。 | `StreamSplitFilter` 识别首个非空字符：若为 `{` 直接进入静默缓存转交 `ParseChatRefineOutput`；若全流无 Payload 分隔符则平滑退化为既有 JSON/纯 Markdown 解析逻辑，保持 100% 兼容。 |
| **批注篮多条意见同时修改同一章节产生语义冲突** | 用户在同一章节添加两条互相矛盾的批注。 | 同章节多条批注按列表顺序合并传入同一轮槽位精修 Prompt，明确提示 AI 综合权衡并在 `reply` 中说明取舍；人工最终在画布原地审阅并可微调。 |
| **表数量从 18 增至 20 对迁移与单测的影响** | `models/db.go` `AllOwnModels()` 须注册新模型，部分检查表数量的单测或文档需同步更新。 | 仅新增 `archdoc_review_comments` 与 `archdoc_chat_proposal_items` 两张带 `archdoc_` 前缀的新表及 `archdoc_chat_messages` 非空默认值可空/默认兼容列；不修改任何公共共享表。 |

### 8.3 架构红线（不可妥协）

1. **包边界隔离红线（`code-archdoc/AGENTS.md` 红线 1）**：`pkg/llm/stream_splitter.go`、`pkg/llm/review_plan.go`、`pkg/llm/chat_refine.go` 严禁导入 `models`、`services`、`handlers`、`gorm` 或 `gin`；`services/chat_service.go` 与 `services/revision_service.go` 严禁直接访问全局 `models.DB`，必须新增/扩展仓储接口注入。
2. **字节级边界隔离红线（`F-09` / `F-10`）**：`ApplyMultiSectionChanges` 必须确保所有未被采纳的槽位与所有 `locked` 槽位在 `DocumentRevision` 中保持 **SHA256 字节级绝对不变**；任何 AI 生成分支不得直接写入 `locked` 槽位正文。
3. **XSS 与样式安全红线（`Non-Goal 6`）**：`frontend/src/utils/reviewAnchor.ts` 与 `SectionMarkdownRenderer` 严禁引入 `dangerouslySetInnerHTML` 或第三方富文本重依赖；全量新增 UI 仅使用 `--color-*` Token 与 `.code-archdoc-review-*` BEM 类名。
4. **安全脱敏与注入防线（`SC-13`）**：批注正文、划词摘录与持久审校约束在注入 LLM 时，必须包裹于带随机 128-bit `nonce` 的 `<untrusted_repo_data>` 数据栅栏中（防止用户误粘贴含恶意指令的外部代码片段形成二次注入），并继续强制通过四重 Verifier 才允许发布。
5. **向后兼容红线**：既有的单槽位 `POST /api/documents/:id/chat/refine`（不传 `mode` 与 `scope_type`）及 `.../accept`、`.../reject` 契约与语义必须保持 100% 向后兼容。

---

## 9. TDD 先行测试清单 (T-ADR06-01 ~ T-ADR06-14)

实施阶段严格遵守「先写失败测试 (Red) → 编写实现通过测试 (Green) → 重构优化 (Refactor)」：

| 编号 | 测试范围 | 测试场景 | 预期断言 |
| :--- | :--- | :--- | :--- |
| **T-ADR06-01** | 前端纯函数<br>(`markdown.ts` / `reviewAnchor.ts`) | 解析含标题、列表、GFM 表格、Mermaid 围栏、代码块的 Markdown 并构建 `ReviewCommentAnchor` | 所有块均返回准确 1-based `startLine`/`endLine`；同 `slot_id` 选区正确截取 `quote`/`prefix`/`suffix`（`quote <= 500` 字符）；块级锚点准确记录 `block_index` 与 `block_type` |
| **T-ADR06-02** | 前端纯函数<br>(`reviewAnchor.ts`) | 三级定位降级：① 同块精确上下文匹配；② 章节前部插入新段落导致 `block_index` 后移；③ 目标文本被完全删改 | ① Level 1 精确切分高亮范围；② Level 2 在新块索引上准确重新绑定高亮；③ Level 3 判定为 `detached` 而不错误高亮其他文本 |
| **T-ADR06-03** | Go 纯函数<br>(`pkg/llm/stream_splitter.go`) | ① 标准双段输出，且 `<<<ARCHDOC_PAYLOAD_JSON>>>` 被任意切割跨越 2~3 个相邻 chunk；② 旧版模型从首字节输出 `{...}` JSON；③ 模型仅输出纯文本无 Payload 标记 | ① `delta` 仅收到干净中文说明（零 JSON 片段），`payload` 完整提取；② 兼容态屏蔽原始 JSON 并通过 `ParseChatRefineOutput` 解析成功；③ `Ask` 态完整输出 Markdown |
| **T-ADR06-04** | Go 纯函数<br>(`pkg/llm/review_plan.go`) | 全局批注 + 局部批注混合 Plan 解析：LLM 路由漏掉某局部批注槽位，且把一个 `locked` 槽位放入了 `target_slots` | 解析器确定性补齐局部批注所属非锁定槽位，同时强制把 `locked` 槽位从可执行列表移除并移入 `advisory_locked` 列表 |
| **T-ADR06-05** | Go `handlers` / `services`<br>(`testdb`) | 审阅批注 CRUD 全流程及三层 RBAC：`POST/GET/PATCH/DELETE /api/documents/:id/review-comments`；普通登录用户或跨子目录负责人提交请求 | 合法负责人增删改查及持久化成功；普通用户或跨子目录负责人操作均返回 `403`；引用不存在的 `slot_id` 或超长 `quote` 返回 `400` |
| **T-ADR06-06** | Go `services`<br>(`chat_service.go`) | 发起 `mode = "ask"` 纯源码查证请求（含指向 `locked` 章节的查证），Mock `httptest` LLM 触发 1 轮只读源码工具后返回纯 Markdown 回答 | SSE 不报 `EMPTY_PROPOSAL`，不写入任何 `ChatProposalItem`，助手消息 `intent="ask"`, `proposal_status="none"`，文档修订零变化 |
| **T-ADR06-07** | Go `services`<br>(`chat_service.go`) | 兼容老接口：不传 `mode` 与 `scope_type`，仅传 `target_slot_id` + `prompt` 发起单槽位精修（并覆盖带 `inline_anchor` 微创场景） | 跳过阶段 1 Plan（Mock LLM 仅收到 1 次精修调用），Prompt 含选区锚定与微创指令，同时下发 `proposal_item` 与 `diff_proposal` |
| **T-ADR06-08** | Go `services`<br>(`chat_service.go`) | 批注篮综合精修（`scope_type="review_batch"`）：包含 1 条全局意见 + 2 条非锁定局部批注 + 1 个命中 `locked` 章节 | 阶段 1 Plan 后仅对命中非锁定槽位执行阶段 2；生成 1 条父消息带多条 `ChatProposalItem`；`locked` 槽位写入 `plan_summary.advisory_locked` 且无提案项；批注状态流转为 `proposed` |
| **T-ADR06-09** | Go `services`<br>(`chat_service.go`) | 客户端在 `review_batch` SSE 执行中途触发 `context.Canceled`（模拟中止生成） | 立即取消上游 LLM 请求，清理占位消息与未完成提案项，关联批注状态回滚/维持 `open` |
| **T-ADR06-10** | Go `handlers` / `services`<br>(`revision_service.go`) | `POST /api/documents/:id/chat/messages/:msg_id/apply` 选中 2 个提案项，其中 1 项携带人工微调后的 `override_markdown`，第 3 项未选 | 单事务仅生成 **1 条** 新 `DocumentRevision`（`revision_no = base + 1`）；未选中槽位与 `locked` 槽位 SHA256 字节级不变；已采纳项关联批注转 `resolved`，未采纳项关联批注回退 `open`；`F-26` 指标与 `P1` 审计日志记录准确 |
| **T-ADR06-11** | Go `handlers` / `services` | 并发乐观锁冲突测试：在提交 `apply` 前文档已被手工编辑推进至 `base + 1`；以及调用 `proposal-items/:item_id/reject` 拒绝单项 | 过期 `apply` 返回 `409 OPTIMISTIC_LOCK_CONFLICT` 且不改动任何批注状态；`reject` 仅把对应项置 `rejected`、同消息其余项仍为 `pending`、关联批注回退 `open` 且写入 `chat_accepted=false` |
| **T-ADR06-12** | Go `pkg/llm` & `services` | 将 1 条 `global_rule` 与 1 条 `supplement` 标记 `persist_for_regen=true`，触发文档重新生成与增量刷新 | ① `BuildSlotMessages` 稳定 `system` 与 `shared_glossary` / `shared_ir_overview` 前缀字节级完全不变；② `ReviewGuidance` 注入至 `auto`/`hybrid` 槽位尾部且包裹随机 Nonce 数据栅栏；③ `locked` 槽位仍被字节级跳过 |
| **T-ADR06-13** | Go 架构防腐测试<br>(`make lint-arch`) | 在全部新增 `pkg/llm/*`、`models/*`、`services/*` 落地后执行架构检查与全量隔离测试 | `pkg/llm` 零数据库/HTTP 依赖，表均含 `archdoc_` 前缀，`services` 无 `models.DB` 引用，`make test` 100% 通过 |
| **T-ADR06-14** | 前端构建与类型检查<br>(`npm test` & `make lint`) | 执行纯函数单测、`tsc --noEmit`、`eslint` 与 `make build` | 零 TypeScript `any`、零静态内联 `style`、零硬编码色值、双模主题样式通过且单二进制打包成功 |

---

## 10. 建议分阶段实施规划（接续 M11 → M12）

本 ADR 评审通过后，建议同步修订阶段一~三 SDD 文档并拆分为 **3 个可独立验收交付的 M12 子波次**：

| 里程碑 | 范围 | 核心交付内容 | 前置依赖 |
| :---: | :--- | :--- | :---: |
| **M12-A** | **批注底座、前端锚定与双段流式净化** | 1. 新增第 19 张表 `archdoc_review_comments`、仓储接口与 `review-comments` CRUD API（`ScopeWrite` + `T-ADR06-05`）；<br>2. 扩展 `markdown.ts` 行号元信息与 `reviewAnchor.ts` 纯函数（`T-ADR06-01`~`02`）；<br>3. 实现 `pkg/llm/stream_splitter.go`（`T-ADR06-03`）、`mode = "ask"` 纯查证模式（`T-ADR06-06`）与单槽位 `inline_anchor` 微创精修（`T-ADR06-07`）。 | M11 |
| **M12-B** | **审阅批注篮 UI、多槽位综合精修与原地审阅** | 1. 新增第 20 张表 `archdoc_chat_proposal_items`、`Plan -> Scoped Slot Refine` 两阶段引擎（`T-ADR06-04`、`08`、`09`）与 `ApplyMultiSectionChanges` 统一事务批量/微调采纳（`T-ADR06-10`、`11`）；<br>2. 重构 `ChatRefineTab` 为「审阅批注篮为主、精修对话为辅」双视图 + `440px <-> 680px` 抽屉展宽；<br>3. 落地画布划词悬浮条、Mermaid/表格块级 `+` 入口、正文 `#N` 高亮双向定位与画布原地三态审阅（红绿 Diff / 渲染态对比 / 微调后采纳）。 | M12-A |
| **M12-C** | **持久审校记忆沉淀与全链路门禁收口** | 1. 打通 `persist_for_regen` 文档级审校约束注入 `GenerationOrchestrator` 与增量刷新（`T-ADR06-12`）；<br>2. 补充采纳 `supplement` 后一键切 `hybrid` 轻量提示与持久约束管理开关；<br>3. 执行 `T-ADR06-13`~`14` 全量隔离库单测、`make lint-arch && make test && make lint && make build`。 | M12-B |
