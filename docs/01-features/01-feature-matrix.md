# 代码设计文档反推 (`code-archdoc`) 特性清单与需求范围界定规范 (Feature Matrix & Scope)

- **文档编号**：`01-feature-matrix.md`
- **文档版本**：v0.2.0 (Stage 1 Baseline)
- **更新日期**：2026-11-19
- **所属模块**：`archdoc`（服务名 `Code-Archdoc` / `code-archdoc-server`）
- **当前阶段**：阶段一：特性清单与边界明确（`01-features`）
- **状态**：架构/开发者视角特性检视与可管理文档类型修订完成（待补充自有设计文档模板样例后冻结，进入阶段二 `02-design`）

### 修订记录

| 版本 | 日期 | 修订要点 |
| :--- | :--- | :--- |
| v0.1.0 | 2026-11-15 | 完成阶段一特性矩阵初稿（`F-01`~`F-18`、`SC-01`~`SC-07`、`KA-01`~`KA-06` 与 `Non-Goals`）。 |
| v0.2.0 | 2026-11-19 | 依据架构师/开发者双视角检视及对标 `code-shield/tasks` 机制全量升级：<br>1. **重构 `F-03` 为可管理文档类型引擎（`DocType` Registry）**：解耦原子 `DocType` 与组合 `DocPack`，引入 `doc_types/<slug>/` 目录约定 + Web 在线管理双轨落盘、`DocTypeRevision` 快照哈希与按类型批量全仓触达（全局由 `archdoc_admin` 治理，对齐 `KA-06`）；<br>2. **补齐架构师与开发者核心视图深度**：在默认 4 类 `DocType` 中固化运行时并发/资源模型、容错与故障影响面矩阵、端到端核心链路、架构约束/ADR/技术债槽位、本地构建调试与排障映射表、二次开发修改指南；<br>3. **强化工程落地与安全门禁**：增补大仓降噪规则与 Git 变更热点（`F-02`/`F-04`）、Token 规模预估与熔断（`F-05`）、配置敏感值强制脱敏与 Mermaid 复杂度门禁（`F-04`/`F-07`）、Mermaid 缩放/SVG 导出与章节「手工直编 + Chat 精修」双轨修订（`F-08`/`F-09`）、`code-bench` 落盘挂载发布与撤回契约（`KA-05`/`F-11`）、代码+规范双维漂移雷达（`F-12`）；<br>4. **完善 P1 进阶路线与验收场景**：新增 `F-19`~`F-23`（全文/符号检索、`Code IR Diff` 变更对比与静态架构体检、`code-contract` 运行态融合、全员只读 Q&A 与读者纠错反馈、AI Coding Agent 上下文包导出）及 `SC-08`~`SC-10`。 |

---

## 0. 模块基础契约 (Module Contract)

| 契约项 | 规范值 | 强约束说明 |
| :--- | :--- | :--- |
| **模块标识 (`ModuleKey`)** | `archdoc` | CodeBench 门户路由前缀 `/archdoc`，Vite Federation `name: 'archdoc'`（避开保留字 `docs`） |
| **服务与二进制名** | `Code-Archdoc` / `code-archdoc-server` | Go Module: `code-archdoc`；内嵌 `frontend/dist` 单二进制交付 |
| **端口分配** | 后端 `:8092` / Vite 调试 `5182` | 避开已分配端口（`bench :8000`、`shield :8080`、`pipeline :8082`、`pdm :8085`、`gate :8088`、`template :8090`、`contract :8091`） |
| **数据库表前缀** | `archdoc_` | 共享 CodeBench PostgreSQL 实例，复用 `users`、`departments`、`repositories`、`architecture_elements`、`audit_logs`，自有表强制以 `archdoc_` 开头 |
| **管理员角色** | `archdoc_admin` | 管理接口挂载 `commonAuth.RequireAdmin("archdoc_admin")`，`super_admin` 拥有同等最高权限 |

---

## 1. 系统愿景与产品定位 (Vision & Positioning)

### 1.1 项目愿景
`code-bench` 中纳管了大量存量业务代码仓，现有设计文档普遍存在**缺失、过时、流于表面或与代码实现不一致**的问题。**`code-archdoc`（代码设计文档反推与活体知识工作台）** 是挂载于 CodeBench 的平级微前端业务子系统，旨在建立**「代码为唯一事实源（Code as SSOT）+ 可插拔文档类型引擎 + 架构/研发双视角深度反推 + 人机双轨协同精修 + 双维漂移监控」**的工程化文档治理闭环。

### 1.2 核心价值主张
1. **零幻觉底座优先（Extract Once, Render Many）**：先通过多语言静态/AST 分析抽取不可篡改的工程硬事实（Code IR）并自动降噪脱敏，再结合分层摘要按需投影渲染多种文档类型，杜绝大模型编造接口、表名、配置键或泄漏明文凭据。
2. **可管理文档类型引擎（对标 `code-shield/tasks`）**：将文档类型（`DocType`）设计为可插拔、目录约定 + Web 在线可管的一等公民；默认内置 4 类高信息密度架构与研发视角，管理员可自由新增、克隆、启停、在线调整模板与 Prompt，并按类型一键触达全仓。
3. **架构深度与研发实战并重**：超越“类与文件清单”式的浅层翻译，显式覆盖架构师关注的**运行时并发/资源模型、容错故障边界、关键约束/ADR**，以及开发者高频依赖的**端到端请求链路、本地构建排障与二次开发修改指南**。
4. **老文档 Gap 诊断而非盲从**：将仓内/上传的历史过时文档作为对照输入，产出「过时条款与缺失项差异报告（Gap Report）」，谨慎摘录业务背景供人工确认，绝不被错误旧描述污染新文档。
5. **人机双轨精修（80 → 99 分）与双维保鲜**：Canvas 工作台支持**手工直编 + Canvas Chat 局部精修**无缝切换、Diff 审阅、源码行级证据直跳与章节锁定防覆盖；通过「代码变更（Code Drift）+ 模板规范升级（Spec Drift）」双维雷达防止文档再次腐烂。

---

## 2. 关键业务假设 (Key Assumptions，已与业务方确认)

| 编号 | 业务假设内容 | 受影响的特性 |
| :--- | :--- | :--- |
| **KA-01** | **P0 主力语言栈**：首期确定性 Code IR 解析优先覆盖 **C/C++（含 Protobuf / CROS RPC 服务）** 与 **Java / Python** 后端服务；Go 与 TypeScript/React 暂列 P2。 | `F-04`, `F-05`, `F-18` |
| **KA-02** | **Git 拉取与凭据假设**：P0 不自建凭据保险库，假定 `code-archdoc-server` 宿主机已配置全局免密只读 SSH Key，直接使用 `Repository.URL`（SSH）执行 `git clone/fetch` 至本地隔离沙箱。 | `F-02`, `F-04`, `F-17` |
| **KA-03** | **LLM 算力接入假设**：P0 直连任意 **OpenAI Compatible HTTP/SSE 接口**（管理员在后台配置 `BaseURL`、`APIKey` 与分层模型名），兼容直连 `code-gate`（`:8088`）或其他推理服务，不绑定外部 CLI 执行器。 | `F-05`, `F-09`, `F-14` |
| **KA-04** | **分析对象粒度假设**：P0 支持「单代码仓（`Repository`）」及「Monorepo 子目录（关联 `code-bench` 的 `ArchitectureElement.RepoID + Subdirectory`）」；跨多仓子系统级文档聚合放至 P1。 | `F-01`, `F-04`, `F-16` |
| **KA-05** | **定稿发布与 CodeBench 同步假设**：`code-bench` 开发者文档中心通过扫描本地 `docs.path`（含 symlink 目录）展示 Markdown 文档树。P0 约定采用**受控发布目录落盘静态 Markdown 资产（含 Frontmatter 元数据与版本标识）供 `code-bench` 挂载只读展示**，支持发布更新与撤回，源码行级证据通过平台相对深链跳回 `/archdoc` 预览；同时支持 Markdown/ZIP 下载。向源码仓自动提 Git MR/PR 放至 P1。 | `F-11`, `F-17`, `F-23` |
| **KA-06** | **全局治理与三层权限假设**：已发布文档全员只读可见；仓库负责人（`Owner` / `RelatedMembers`）可在生成时自由勾选任意已启用的 `DocType` 或 `DocPack`，并拥有所属仓库的生成、手工编辑、Chat 精修、加锁与发布权限；**全局 `DocType`、`DocPack`、批量全仓触发与 LLM 接口配置统一由管理员（`archdoc_admin` / `super_admin`）独占治理**（不做个人私有 `DocType` 分叉）。 | `F-03`, `F-09`, `F-13`, `F-14` |
| **KA-07** | **分支基线与大仓降噪假设**：P0 默认追踪仓库主干分支（`Repository.Branch`），但快照与文档数据模型预留 `ref_name`（Branch/Tag）维度供 P1 无损支持多分支版本基线；所有仓库默认开启三方依赖与自动生成代码（`third_party/`、`vendor/`、`*.pb.cc`/`*.pb.h` 等）降噪过滤，支持仓库级 `.archdocignore` 与 `DocType` 级 `scope_profile` 声明式覆盖。 | `F-02`, `F-03`, `F-04`, `F-20` |

---

## 3. 需求功能矩阵 (Feature Matrix)

> 优先级定义：**P0 (Must-Have)** 首期核心闭环且须具备完整 TDD 单测；**P1 (Should-Have)** 重要增强与生态联动进阶；**P2 (Nice-to-Have)** 远期扩展。

### 子域 A：主数据集成、可管理文档类型与仓库快照 (Repository, DocType Registry & Snapshot)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-01** | **CodeBench 仓库与架构子目录同步视图** | `Backend`<br>`Frontend` | **P0** | 直接读取共享库 `repositories` 与 `architecture_elements`（支持按 `RepoID + Subdirectory` 选定 Monorepo 范围），展示各仓库在不同 `DocType` 下的覆盖状态、最近分析 `ref_name`/`commit_sha` 及双维新鲜度评分；列表支持关键词/部门/状态/`DocType` 筛选与 URL 同步分页（默认 25 条/页）。 | 1. 复用 `repositories` 表无重复建表。<br>2. 分页参数与 URL Search Params 双向同步；Monorepo 子目录路径规范化校验并拦截 `..` 路径穿越（`400`）。 |
| **F-02** | **无状态只读 Git 沙箱同步、降噪与热点采集引擎** | `Backend` | **P0** | 基于宿主机免密 SSH Key 对选定仓库执行浅克隆/增量 `fetch` 至沙箱目录；内置同路径并发互斥锁、残留 `.git/*.lock` 自动自愈、独立进程组（`Setpgid`）超时强杀防孤儿进程，记录 `(ref_name, commit_sha)`；同时应用默认降噪规则（自动跳过 `third_party/`、`vendor/`、`node_modules/`、`*.pb.cc`/`*.pb.h`、超大二进制等）及仓库级 `.archdocignore`/路径黑白名单，并轻量提取近 N 次提交的**文件变更热点（Churn Hotspots）与关键提交摘要**。 | 1. 同仓并发触发同步时串行排队不冲突；Context 取消时无残留 `git` 僵尸进程。<br>2. 含 `third_party/` 与 `*.pb.cc` 的夹具仓库被准确过滤，不进入后续分析清单。<br>3. 鉴权失败快速阻断并透出明确错误。 |
| **F-03** | **可管理文档类型注册表（`DocType` Registry，对标 `code-shield/tasks`）与文档套件（`DocPack`）** | `Backend`<br>`Frontend`<br>`Models` | **P0** | **1. 两层解耦模型**：底层为可插拔原子资产 `DocType`，上层为可选组合 `DocPack`（将多个启用中的 `DocType` 编排为标准交付套件，生成时既可按套件一键触发，也可自由勾选任意 `DocType`）。<br>**2. 目录约定 + Web 双轨管理**：服务端约定 `doc_types/<name-with-hyphens>/` 目录，包含 `meta.json`（元数据、`is_active`、声明式 `scope_profile`、订阅的 `ir_facets`、结构化 `Slot Schema`、`verify_policy`、`model_tier`、`timeout`）、`template.md`（文档骨架）、`generation_prompt.md`（反推指令）与可选 `refine_prompt.md`（Chat 精修约束）。启动幂等 Seed 同步至 PG；`archdoc_admin` 可在 Web 端新建（自动脚手架落盘默认文件）、克隆派生、启停、编辑配置与配套 Markdown 文件（安全双向落盘并保留未知扩展字段）、删除（已关联发布文档时禁止物理删除，引导停用）。<br>**3. 内置 4 类高密度默认 `DocType`**：<br>• `system-architecture`（系统架构与核心链路）：含模块分层、**运行时并发/进程/锁/SHM 模型**、**容错与故障模式**、**外部依赖边界与故障影响面矩阵**、**端到端核心链路时序图**，以及默认 `locked` 的**「架构约束、关键决策（ADR）与已知技术债」**槽位；<br>• `data-and-contract`（数据模型与接口契约）：含接口签名、存储/消息模型、协议边界与**核心错误码体系**；<br>• `ops-and-config`（配置、运维部署与研发上手）：含脱敏配置清单、部署拓扑、**本地构建/编译宏/单测与调试手册**、**关键日志与错误码排障映射表**；<br>• `code-walkthrough`（代码导读与改码指南）：含目录职责、核心请求逐跳源码导航与**典型二次开发修改清单（How to Extend）**。<br>**4. 修订快照与按类型全仓触达**：`DocType` 任意资产变更计算 `aggregate_hash` 留存不可变 `DocTypeRevision`；支持按 `DocType` 查看全仓覆盖率，并由管理员按部门/子系统/全量仓库一键批量触发缺失或过时仓库生成。 | 1. 启动幂等 Seed 内置 4 类 `DocType` 与默认 `DocPack`；所有 4 类模板均包含上述必填架构/研发槽位。<br>2. 管理员 CRUD `DocType` 与在线保存 `template.md`/`generation_prompt.md` 双向落盘一致并生成新 `DocTypeRevision`；重复 `name` 返回 `409`；非法 `name`（含 `../` 或非法字符）返回 `400`；非管理员操作返回 `403`。<br>3. 已关联发布文档的 `DocType` 拒绝物理删除返回 `409`。<br>4. 按 `DocType` 批量触达仅对缺失或存在漂移的活跃仓入队，不重复派发正在运行中的任务。 |

### 子域 B：确定性事实抽取与反推生成引擎 (Code IR & Synthesis Pipeline)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-04** | **L1 确定性静态事实抽取与脱敏（Code IR）** | `Backend` | **P0** | 对每个 `(repo_id, subdir, ref_name, commit_sha)` **仅抽取并缓存 1 次**分片化硬事实 JSON（按 `ir_facets` 组织）：<br>• **通用与构建**：目录树职责、构建脚本（`CMake`/`Makefile`/`pom.xml`/`requirements.txt`/`pyproject.toml`）与编译宏/开关、依赖拓扑（区分 Inbound 供给与 Outbound 外部依赖）、配置文件键清单（**强制敏感值脱敏：仅抽取键名、类型与安全默认值，严禁落盘明文密钥/口令**）；<br>• **C/C++**：头文件/源文件映射、宏/结构体/类定义、`.proto` 与 CROS RPC 服务契约（以 `.proto` 为源头跳过 `*.pb.cc`/`*.pb.h`，有 `code-contract` 资产时支持复用）、**运行时并发与资源原语特征**（线程/锁/SHM/信号处理/定时器/错误码枚举）；<br>• **Java / Python**：Spring/FastAPI/Flask 路由签名、Entity/ORM 模型、入口点、依赖与并发/任务调度注解。 | 1. 针对 C/C++、Java、Python 测试夹具仓库，零 LLM 调用即可 100% 确定性提取目录、类/结构体、接口、运行时特征与脱敏配置键。<br>2. 夹具中注入明文密码/Token 的配置项被自动脱敏，Code IR JSON 中不含明文凭据。<br>3. 相同 `(repo_id, subdir, ref_name, commit_sha)` 命中缓存不重复解析。 |
| **F-05** | **L2 分层语义摘要、预算熔断与多类型并发反推** | `Backend`<br>`Models` | **P0** | 针对大仓按模块/目录做语义分片（Map）生成模块职责摘要与统一术语表，再按选定各 `DocType` 声明的 `ir_facets`、`scope_profile`、`model_tier` 与 `timeout` 并发填充章节槽位（Reduce），并记录生成绑定的 `doc_type_revision_hash`；生成前计算**文件规模与 Token 预估**，超过管理员配置的预算阈值自动触发告警或熔断降级；支持单仓单类型、单仓套件、按类型批量全仓排队执行及任务取消/断点恢复。 | 1. 同快照跨 `DocType` 共享同一 `RepoSnapshot` 的 Code IR 与术语表，不重复拉码与抽取。<br>2. 超过单任务 Token/文件上限预算时触发熔断并给出明确错误原因与裁剪建议。<br>3. 任务状态机严格遵循 `pending -> extracting -> generating -> verifying -> ready/failed`，支持取消与异常恢复。 |
| **F-06** | **历史过时文档 Gap 诊断报告** | `Backend`<br>`Frontend` | **P0** | 自动发现仓内既有 `docs/` / `README.md` 或支持用户上传历史设计文档作为对照输入；在代码反推完成后独立比对输出 **Gap Report**（标注过时描述、缺失章节、已变更符号），并谨慎提取仅存于旧文档的业务背景放入 `[待人工确认]` 区块，严禁覆盖源码事实。 | 1. 测试用例注入过时接口名/旧配置项时，Gap Report 准确检出不一致条目。<br>2. 生成草稿中的硬事实符号完全来自 Code IR，不被旧文档误导。 |
| **F-07** | **证据锚定、防幻觉校验与安全/图表质量门禁** | `Backend`<br>`Frontend` | **P0** | 为生成结论绑定代码证据元数据（`file_path`、`line_start~line_end`、`symbol`、`commit_sha`）；落库前执行多维度自动化 Verifier：<br>1. **符号与路径防幻觉**：校验文中提及的文件路径、类名、接口/表名是否存在于 Code IR；<br>2. **槽位完备度**：计算 `DocType` 必填槽位覆盖率与可信度标记；<br>3. **Mermaid 语法与复杂度护栏**：校验语法合法性、支持图表类型白名单、标签引号转义规范，以及单图节点数/边数复杂度上限（防止生成不可读的“毛线球图”）；<br>4. **敏感凭据防泄漏门禁**：对生成文本执行密钥/口令/内网 Token 正则与高熵扫描，命中即拦截告警。 | 1. 伪造路径或符号被准确标记 `unverified` 告警。<br>2. 非法 Mermaid 语法或超限节点图触发明确可读性告警。<br>3. 正文/示例配置中混入疑似明文密钥或口令时被 Verifier 拦截，禁止静默定稿。 |

### 子域 C：双栏工作台、人机双轨修订与人工审校 (Canvas Studio & Dual-Track Review)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-08** | **双栏协同审校工作台与图表交互（Canvas Studio）** | `Frontend`<br>`Backend` | **P0** | 左侧渲染多 `DocType` 文档树与结构化章节（支持 Markdown、表格、Mermaid 渲染、Gap Report 与 Verifier 告警标记），右侧提供 Chat 协同面板、修订历史及只读源码抽屉；点击任意证据标签或时序图步骤锚点直跳对应 `commit_sha` 的只读源码行高亮预览；**Mermaid 图表支持画布缩放、平移、全屏查看与一键导出 SVG/PNG**。 | 1. 亮/暗双模主题完整适配，零硬编码颜色。<br>2. 源码预览接口严格校验路径在沙箱根目录下，拦截 `../` 越权访问（`403`）。<br>3. 图表画布支持缩放与 SVG 导出无样式丢失。 |
| **F-09** | **章节级「手工直编 + Chat 精修」双轨修订与只读 Tool Calling** | `Backend`<br>`Frontend` | **P0** | 支持仓库负责人与管理员对任意章节无缝切换两种修订模式：<br>1. **手工直编模式**：直接在章节内编辑 Markdown（快速修正错字、补充业务公式/表格或填写 ADR 决策），保存时生成不可变 `Revision` 并自动重跑 Verifier；<br>2. **AI Chat 精修模式**：对整篇或选中指定章节发起自然语言指令（结合该 `DocType` 的 `refine_prompt.md`），AI 挂载只读检索工具（`list_files`、`read_file`、`grep_symbol`）实时核对源码，流式（SSE）返回**章节级 Diff**，用户确认 `Accept` 后生成 `Revision` 并重跑 Verifier。双轨共享统一的修订时间线、版本对比与一键回滚。 | 1. 手工直编与 Chat 局部修订均仅影响目标 `Section`，未选中章节字节级无变动。<br>2. 每次保存/`Accept` 均产生不可变 `Revision`、触发 Verifier 并支持任意版本对比与一键回滚。 |
| **F-10** | **章节所有权与人工锁定保护（Section Lock）** | `Backend`<br>`Frontend`<br>`Models` | **P0** | 章节支持 `auto`（代码托管）、`hybrid`（混合）与 `locked`（人工锁定）三种模式；「架构约束 / 关键决策（ADR）/ 已知技术债」类槽位默认 `locked` 或人工主导；用户手工直编或通过 Chat 注入业务背景后可一键加锁，后续代码或模板更新触发重新反推时，`locked` 章节自动豁免覆盖（仅提示待核对漂移）。 | 重新触发全篇反推后，`locked` 章节内容（含手工维护的 ADR）完整保留不丢失，`auto` 章节按新代码与新规范刷新。 |

### 子域 D：发布闭环、双维漂移雷达与平台治理 (Publishing, Dual-Drift Radar & RBAC)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-11** | **定稿发布、`code-bench` 挂载同步、撤回与导出** | `Backend`<br>`Frontend` | **P0** | 支持将审校完成的单篇文档或全套 `DocPack` 标记为 `Approved`（已发布）；支持一键同步落盘至与 `code-bench` 开发者文档中心约定的只读挂载目录（自动写入包含 `repo`、`doc_type`、`commit_sha`、`published_at` 的 YAML Frontmatter、执行跨文档相对链接检查，并将源码行级证据渲染为跳回 `/archdoc` 源码抽屉的平台相对深链），支持**发布覆盖与一键撤回下线**；支持单篇 Markdown 与全套件 ZIP 打包下载。 | 1. 未审批通过的草稿不落盘至对外发布目录。<br>2. 发布落盘文件包含完整 Frontmatter 与有效平台深链；执行撤回后目标发布文件被安全移除且对外不可见。<br>3. ZIP 导出包含全部视角文档与目录索引，路径安全无越权。 |
| **F-12** | **文档新鲜度与「代码 + 规范」双维漂移雷达** | `Backend`<br>`Frontend` | **P0** | 同时追踪两个维度的漂移并计算仓库与 `DocType` 维度的健康度：<br>1. **代码漂移（Code Drift）**：对比仓库当前分支 `HEAD` 与已发布文档的 `commit_sha`，识别核心源码/接口/模型/配置变更及受影响的 `DocType`；<br>2. **规范漂移（Spec Drift）**：对比当前启用 `DocType.aggregate_hash` 与已发布文档的 `doc_type_revision_hash`，精准识别模板/Prompt 升级后尚未对齐新规范的历史文档。<br>看板支持按漂移类型筛选并一键触发增量或全量刷新。 | 1. 代码无新提交且 `DocType` 未变更时双维漂移均为 0。<br>2. 核心代码变更触发 `Code Drift` 并定位受影响 `DocType`；管理员修订模板后既有文档立即标出 `Spec Drift`。<br>3. 全员可读雷达看板状态。 |
| **F-13** | **三层 RBAC 权限守卫与字段级审计** | `Backend`<br>`Models` | **P0** | 集成 `commonAuth.AuthMiddleware` 与 `RequireAdmin("archdoc_admin")`（遵循 `KA-06` 全局治理选项 A）：<br>• **普通用户**：只读浏览已发布文档、证据源码、导出下载与查看漂移看板；<br>• **仓库负责人（`Owner` / `RelatedMembers`）**：自由选用已启用 `DocType`/`DocPack` 触发生成、手工直编、Chat 精修、加锁、发布与撤回所属仓库文档；<br>• **管理员（`archdoc_admin` / `super_admin`）**：全量仓库文档权限 + 全局 `DocType`/`DocPack` 管理 + 按类型批量全仓触达 + OpenAI LLM 接口配置。<br>所有写操作接入 `commonAudit` 记录字段级 Diff 审计。 | 1. 未登录返回 `401`。<br>2. 非所属仓普通用户发起生成/直编/Chat/发布返回 `403`。<br>3. 非 `archdoc_admin` 修改 `DocType`、`DocPack`、批量触达或 LLM 配置返回 `403`。<br>4. 关键操作审计表记录完整 Old/New Diff。 |
| **F-14** | **OpenAI Compatible 多模型分层配置中心** | `Backend`<br>`Frontend` | **P0** | 为 `archdoc_admin` 提供 LLM 推理配置页：配置 OpenAI Compatible `BaseURL`、`APIKey`（服务端脱敏展示且加密落库）、超时重试参数、**单任务 Token/文件预算上限**，并支持按模型档位（如 `fast` 轻量摘要/导读模型 vs `flagship` 旗舰架构综合模型，供 `DocType.model_tier` 引用）与连通性探活测试。 | 配置保存后返回体 `api_key` 自动脱敏（`sk-****`）；探活接口可验证模型可用性并返回延迟与状态。 |

### 子域 E：P1/P2 进阶演进特性 (Post-MVP Roadmap)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-15** | **多条 PR 式批注一键批量修订 & AI 反向澄清** | `Backend`<br>`Frontend` | **P1** | 支持用户在文档多个位置先连续挂载 Review 批注，一键交给 AI 批量收敛修订；生成/精修遇代码歧义时 AI 主动抛出澄清选择题等待用户确认后继续。 | 批注合集修订后各条目准确闭环并产出统一 Revision；澄清状态挂起与恢复正确。 |
| **F-16** | **跨多仓子系统聚合文档反推** | `Backend`<br>`Frontend` | **P1** | 基于 `code-bench` 的 `ArchitectureElement` 树聚合同一子系统下的多个关联代码仓，联合生成系统级总体架构、跨仓接口调用拓扑与联调时序文档。 | 选中子系统节点后可拉取关联多仓快照并生成跨仓服务依赖视图。 |
| **F-17** | **Git 凭据保险库与回推源码仓自动提 MR/PR** | `Backend`<br>`Frontend` | **P1** | 内建 AES-256-GCM 凭据管理（支持 CodeHub/GitLab PAT 与独立 SSH Key），支持将定稿文档自动创建分支写入目标仓 `docs/` 并发起 MR/PR，以及配合 Webhook 在代码合并后自动触发局部文档漂移刷新。 | 凭据加密落库且内存注入不落盘；Mock GitLab API 验证创建分支与提 MR 成功。 |
| **F-18** | **扩展语言支持（Go / TypeScript / React）** | `Backend` | **P2** | 扩展 L1 Code IR 解析器支持 Go（AST/Gin/GORM）与 TypeScript/React（路由/组件树/API 调用提取），覆盖平台自身 `code-*` 微前端全栈工程。 | 针对 `code-template` 示例仓可准确提取路由、GORM 表模型与前端组件树。 |
| **F-19** | **跨文档与符号级全文检索** | `Backend`<br>`Frontend` | **P1** | 基于已发布文档正文与 Code IR 符号索引提供全局搜索，支持按接口名、类名/结构体、配置键、错误码与关键词跨仓/跨 `DocType` 毫秒级直达对应章节与源码证据行。 | 输入接口名、错误码或配置键可准确返回匹配的已发布文档章节，未发布草稿对普通用户严格不可见。 |
| **F-20** | **基于 `Code IR Diff` 的跨版本架构变更对比、静态体检与多分支基线** | `Backend`<br>`Frontend` | **P1** | 1. 基于两次 `RepoSnapshot` 的结构化 **Code IR Diff** 确定性生成语义级架构变更清单（新增/变更/废弃接口、数据模型、配置键、外部依赖）；<br>2. 基于 L1 依赖图与 `F-02` Git 变更热点输出**静态架构体检卡片**（`#include`/包循环依赖、跨层反向引用、超大源文件/上帝类、高频变更热点）；<br>3. 开放按 `ref_name`（Release 分支 / Tag）创建与切换多版本文档基线。 | 1. 夹具仓库两次提交间新增接口与删除配置键可被零幻觉准确 Diff 检出。<br>2. 含双向 `#include` 环的夹具可准确标出循环依赖。<br>3. 不同分支/Tag 的快照与文档互不覆盖。 |
| **F-21** | **与 `code-contract` 运行态契约/拓扑双向融合（Live-Enriched Archdoc）** | `Backend`<br>`Frontend` | **P1** | 当仓库/子目录已关联 `code-contract` 的模块或 `.pb` 描述集时，反推引擎按需消费其权威三级架构元素、Proto 字段注释、`MULTI_IMPL` 多实现契约与线网实测调用边，在文档接口契约与调用拓扑中自动标注 **「线网已验证 / 僵尸未覆盖 / 存在架构违规调用」**，弥补 C/C++ 纯静态分析盲区。 | Mock `code-contract` 契约/拓扑数据源时，生成的接口清单与链路准确附带运行态校验徽标；无关联数据时优雅降级为纯静态模式。 |
| **F-22** | **面向全员的只读智能问答（Reader Q&A）与读者纠错反馈闭环** | `Backend`<br>`Frontend`<br>`Models` | **P1** | 1. **只读 Q&A**：普通开发者可针对任意已发布仓库文档发起提问，后端基于已发布 Doc Pack + Code IR + 只读源码检索流式作答并附带源码行级证据引用，严格只读绝不修改文档；<br>2. **读者纠错反馈**：任意用户在阅读文档章节时可一键“标记过时/提交纠错”，反馈记录绑定章节 ID 与 `Revision` 并流转至仓库 Owner 的待处理队列。 | 1. 普通用户发起 Q&A 返回带源码行引用的回答且数据库文档内容零变更。<br>2. 读者提交章节纠错后仓库 Owner 列表可见并支持标记已处理。 |
| **F-23** | **面向 AI Coding Agent 的结构化上下文包导出** | `Backend`<br>`Frontend` | **P1** | 在发布与导出出口新增面向 AI 编程助手的紧凑格式：一键导出精炼的 `ARCHITECTURE.md` / `AGENTS.md` 架构上下文摘要（含模块职责、核心约束、改码清单与高频坑点）及标准化只读 `Code IR JSON`，直接赋能团队日常 AI 研发提效。 | 导出的 AI 上下文包控制在可配置 Token 预算内且所有符号与路径均通过 Verifier 校验，无敏感凭据泄漏。 |

---

## 4. 核心验收场景矩阵 (Verification Scenarios)

| 场景编号 | 场景名称 | 触发条件与输入特征 | 预期行为与 TDD 断言 |
| :--- | :--- | :--- | :--- |
| **SC-01** | **免密探针与多级 RBAC 越权拦截** | 1. 请求 `GET /api/health`；<br>2. 无 Token 请求 `GET /api/repos`；<br>3. 非仓库 Owner/Member 的普通用户调用生成/直编/发布，或非 `archdoc_admin` 调用 `DocType`/LLM 配置接口。 | 1. 返回 `200 OK`（`service: "Code-Archdoc"`）；<br>2. 返回 `401 Unauthorized`；<br>3. 越权请求均被拦截返回 `403 Forbidden`。 |
| **SC-02** | **多语言 L1 Code IR 抽取与缓存复用** | 选定含 C/C++（`.proto`、多线程锁、错误码宏）+ Python 的仓库快照，依次触发「系统架构」与「数据与接口契约」两类 `DocType` 生成。 | Git 同步与 Code IR 抽取仅执行 1 次；L1 JSON 准确包含 `.proto` 服务/C++ 并发特征/Python 路由，两类文档共享同一快照实体字典。 |
| **SC-03** | **过时旧文档 Gap 诊断与防污染** | 仓库内存在含错误旧接口 `/v1/legacy_pay` 的 `docs/design.md`，而源码实际已迁移为 `/v2/orders/pay`。 | 新生成的接口契约仅出现 `/v2/orders/pay`；Gap Report 明确告警旧文档存在过时接口 `/v1/legacy_pay`。 |
| **SC-04** | **Verifier 防幻觉、图表复杂度与凭据拦截** | Mock LLM 在生成章节中编造不存在文件 `src/fake_engine.cpp`、输出超限节点/非法 Mermaid 语法，或输出含明文口令的配置示例。 | 落库前校验器分别检出未验证路径（`unverified`）、Mermaid 语法/复杂度超限告警与敏感凭据拦截告警，阻止静默标为高可信定稿。 |
| **SC-05** | **Canvas Chat 局部修订、Diff 应用与章节锁定** | 仓库 Owner 选中第 2 章输入指令“补充超时重试时序图”，审阅 Diff 后点击 `Accept` 并对第 1 章加 `locked` 锁；随后代码推进新 Commit 并重跑全篇生成。 | 1. 局部修订仅更新第 2 章，第 1/3/4 章字节不变且生成 `Revision`；<br>2. 重跑后第 1 章（`locked`）完整保留，第 2~4 章按新代码刷新。 |
| **SC-06** | **Monorepo 子目录参数校验与路径穿越防护** | 1. 传入关联 `Subdirectory: "../../etc"` 请求生成；<br>2. 在只读源码查看接口传入 `path=../../../etc/passwd`。 | 均在参数/沙箱边界直接拦截，返回 `400` / `403`，严禁读取沙箱外文件。 |
| **SC-07** | **定稿发布、CodeBench 挂载同步与双维漂移感知** | Owner 将文档审批为 `Approved` 并触发发布；随后仓库新增含 `models/` 改动的模拟 Commit。 | 发布后生成带 Frontmatter 与平台深链的 Markdown 并供普通用户只读浏览与 ZIP 下载；新 Commit 后漂移雷达立即标出 `Code Drift` 与受影响 `DocType`。 |
| **SC-08** | **可管理 `DocType` 全生命周期、在线编辑与规范漂移联动** | 1. `archdoc_admin` 新建自定义 `DocType` `security-review` 并在线修改其 `template.md`；<br>2. 尝试提交非法名称 `../evil` 或删除已关联发布文档的 `DocType`；<br>3. 修改内置 `system-architecture` 的 `generation_prompt.md` 并触发按类型批量补齐。 | 1. 磁盘 `doc_types/security-review/` 与数据库双向落盘一致并生成新 `DocTypeRevision`；<br>2. 非法名称返回 `400`，引用中的类型禁止删除返回 `409`；<br>3. 修改模板后历史已发布文档立即在雷达标出 `Spec Drift`，批量触达仅对缺失/漂移仓入队。 |
| **SC-09** | **大仓自动降噪、敏感配置脱敏与 Token 预算熔断** | 1. 夹具仓包含 `third_party/zlib/`、`proto/order.pb.cc`、`config.yaml`（含 `db_password: "secret123"`）；<br>2. 将单任务 Token 预算设为极低阈值并触发生成。 | 1. L1 扫描跳过 `third_party/` 与 `*.pb.cc` 且不落盘 `secret123` 明文值；<br>2. 触发预算熔断拦截并返回明确超限提示，无静默超支调用。 |
| **SC-10** | **章节手工直编、默认 ADR 锁定与发布撤回闭环** | Owner 直接手工编辑某章节与默认 `locked` 的「架构约束/ADR」槽位并保存，发布至 `code-bench` 挂载目录后再执行一键撤回。 | 1. 手工直编生成独立 `Revision` 并重跑 Verifier；重跑反推时 ADR 槽位不被覆盖；<br>2. 发布后目标目录生成规范 Markdown，撤回后目标文件被安全清理且对普通用户不可见。 |

---

## 5. 非目标边界界定 (Non-Goals)

- **Non-Goal 1（主数据自治禁令）**：不重复自建独立的用户中心、部门树或代码仓主表，统一复用共享库 `users`、`departments`、`repositories` 及 `code-bench` 的 `architecture_elements`。
- **Non-Goal 2（P0 语言边界）**：P0 不承诺支持 Go、TypeScript/React、Rust 等全部语言的高精度 L1 AST 提取（首期聚焦 C/C++（含 Protobuf/CROS RPC）与 Java/Python，其他文件走通用目录与基础文本摘要）。
- **Non-Goal 3（P0 Git 凭据与写操作边界）**：P0 不建设租户凭据保险库 UI，也不直接向远程 Git 仓库推分支/提 MR（均在 P1 演进）。
- **Non-Goal 4（P0 跨仓聚合与多分支 UI 边界）**：P0 不自动跨多个独立 Git 仓库联合生成系统级文档（仅支持单仓及同仓 `Subdirectory` 子目录），且前端首期默认单主干分支视图（数据模型预留 `ref_name` 供 P1 开放多分支切换）。
- **Non-Goal 5（AI 驱动边界）**：P0 不集成 Claude Code / OpenCode / Codex 等本地有状态 CLI 子进程调度器，统一通过纯 Go OpenAI-compatible HTTP/SSE 客户端调度推理。
- **Non-Goal 6（表前缀与 UI 技术栈红线）**：不在共享 PostgreSQL 中创建无 `archdoc_` 前缀的裸表；前端不引入 Ant Design / Material UI 等重型组件库，全面使用 `@code/common` 与原生 Vanilla CSS。
- **Non-Goal 7（`DocType` 零任意脚本红线）**：`DocType` 的文件裁剪（`scope_profile`）、槽位结构（`Slot Schema`）与校验策略（`verify_policy`）全部采用**声明式 JSON/Markdown 配置**，严禁在 Web 配置中引入任意可执行 Shell/Python 脚本，杜绝服务端代码注入风险。

---

## 6. 非功能性与安全红线 (NFR & Guardrails)

1. **架构防腐与解耦**：
   - 建立类似 `code-shield` 的静态架构检查（`make lint-arch`）：`L1 静态解析器`、`DocType 注册表与模板引擎`、`LLM HTTP Client` 与 `Verifier` 严禁直接依赖 `models.DB`，必须通过清晰接口与领域对象解耦。
2. **数据库隔离与测试红线**：
   - 所有涉及 PG 读写的单元测试强制使用 `code-common/backend/testdb.SetupIsolatedDB(t)` 动态创建/销毁隔离库；
   - Git 同步、`DocType` 双向落盘、L1 解析与脱敏、LLM Invoker（使用 `httptest` Mock SSE/JSON 响应）与 Verifier 须具备独立单元测试覆盖正常、边界、异常与越权路径。
3. **沙箱、目录落盘与凭据防泄漏安全**：
   - 沙箱工作区按 `(repo_id, subdir)` 严格做 `filepath.Rel` 路径穿越阻断；`DocType` 目录名强制匹配 `^[a-z0-9][a-z0-9_-]{1,62}$` 且落盘采用“临时文件 + 原子重命名”防止并发写损坏；
   - `git` 命令执行使用独立进程组（`Setpgid`）防止取消或超时后残留僵尸进程；
   - LLM `APIKey` 在数据库中加密存储、接口返回脱敏、日志与审计记录禁止打印明文密钥；L1 抽取与 Verifier 双重拦截业务代码/配置文件中的疑似硬编码明文口令与 Token，防止敏感信息写入全员可读文档。
4. **可观测与成本控制**：
   - 每次生成/Chat 调用记录 Token 消耗、耗时、模型名、`DocTypeRevision` 与阶段（映射摘要 vs 槽位综合），支持任务级并发上限控制、生成前 Token 预算熔断与失败可恢复；优先使用稳定前置上下文顺序以最大化 `code-gate` 等网关的 Prompt KV Cache 命中率。
5. **前端体验与双模规范**：
   - 严格遵循 `code-common/rules/AGENTS.md`：100% 使用 `--color-*` Design Tokens 适配亮/暗双模主题、Flat BEM 命名（`.code-archdoc-*`）、URL 状态同步 5 页滑动分页（默认 25 条/页）、禁止滥用 `any` 与静态内联 `style`。

---

## 7. 术语表 (Glossary)

| 术语 | 定义 |
| :--- | :--- |
| **Code IR (L1 静态事实层)** | 由确定性 AST/文本规则扫描从代码仓直接提取并完成降噪脱敏的机器级结构化事实 JSON（含目录树、数据模型、接口签名、运行时原语、配置键与依赖边界），作为防幻觉的唯一客观底座。 |
| **DocType (可管理文档类型)** | 对标 `code-shield/tasks` 的原子文档资产单元，包含 `meta.json`（过滤范围、槽位契约、校验策略、模型档位）、`template.md` 与 Prompt 文件，支持目录约定 + Web 在线全生命周期管理。 |
| **DocTypeRevision (文档类型修订快照)** | 每次 `DocType` 元数据、模板或 Prompt 变更时生成的不可变聚合哈希快照，用于追溯文档生成依据并驱动规范漂移（Spec Drift）检测。 |
| **DocPack (文档套件)** | 将多个启用中的 `DocType` 按业务场景自由组合而成的标准交付物方案（如“C++ 核心服务 4 件套”），便于一键批量生成与同步发布。 |
| **Slot Schema (模板槽位契约)** | 将文档模板拆解为结构化章节定义，明确各章节的数据来源（`ir_facets`）、图表/表格结构约束、Prompt 指引及所有权模式（`auto`/`hybrid`/`locked`）。 |
| **Gap Report (过时与缺失差异报告)** | 将代码反推得到的客观事实与既有历史文档比对后产出的诊断清单，标出过时条款、遗漏模块与仅旧文档存在的业务背景线索。 |
| **Canvas Studio (双栏协同审校台)** | 集成了多 `DocType` 阅读树、手工直编与章节局部 Chat 双轨修订、Diff 预览应用、章节锁定、图表缩放/SVG 导出与源码行级证据直跳的交互式工作台。 |
| **Code Drift / Spec Drift (双维漂移)** | **Code Drift** 衡量仓库当前最新提交（`HEAD`）与已发布文档 `commit_sha` 间的代码结构变更；**Spec Drift** 衡量已发布文档所绑定的 `DocTypeRevision` 与最新启用模板规范之间的版本滞后。 |
| **Code IR Diff (语义级架构变更对比)** | 基于两次仓库快照的确定性 Code IR 结构化比对，零幻觉识别跨版本新增/变更/废弃的接口、数据模型、配置键与外部依赖。 |
