# 代码设计文档反推 (`code-archdoc`) 特性清单与需求范围界定规范 (Feature Matrix & Scope)

- **文档编号**：`01-feature-matrix.md`
- **文档版本**：v0.1.0 (Stage 1 Draft)
- **编制日期**：2026-11-15
- **所属模块**：`archdoc`（服务名 `Code-Archdoc` / `code-archdoc-server`）
- **当前阶段**：阶段一：特性清单与边界明确（`01-features`）
- **状态**：初稿基线（待补充自有设计文档模板结构后定稿，定稿后进入阶段二 `02-design`）

---

## 0. 模块基础契约 (Module Contract)

| 契约项 | 规范值 | 强约束说明 |
| :--- | :--- | :--- |
| **模块标识 (`ModuleKey`)** | `archdoc` | CodeBench 门户路由前缀 `/archdoc`，Vite Federation `name: 'archdoc'`（避开保留字 `docs`） |
| **服务与二进制名** | `Code-Archdoc` / `code-archdoc-server` | Go Module: `code-archdoc`；内嵌 `frontend/dist` 单二进制交付 |
| **端口分配** | 后端 `:8092` / Vite 调试 `5182` | 避开已分配端口（`bench :8000`、`shield :8080`、`pipeline :8082`、`pdm :8085`、`gate :8088`、`template :8090`、`contract :8091`） |
| **数据库表前缀** | `archdoc_` | 共享 CodeBench PostgreSQL 实例，复用 `users`、`departments`、`repositories`、`audit_logs`，自有表强制以 `archdoc_` 开头 |
| **管理员角色** | `archdoc_admin` | 管理接口挂载 `commonAuth.RequireAdmin("archdoc_admin")`，`super_admin` 拥有同等最高权限 |

---

## 1. 系统愿景与产品定位 (Vision & Positioning)

### 1.1 项目愿景
`code-bench` 中纳管了大量存量业务代码仓，现有设计文档普遍存在**缺失、过时或与代码实现不一致**的问题。**`code-archdoc`（代码设计文档反推与活体知识工作台）** 是挂载于 CodeBench 的平级微前端业务子系统，旨在建立**「代码为唯一事实源（Code as SSOT）+ 模板驱动多视角生成 + 人机协同 Chat 精修 + 持续漂移监控」**的工程化文档治理闭环。

### 1.2 核心价值主张
1. **零幻觉底座优先（Extract Once, Render Many）**：先通过多语言静态/AST 分析抽取不可篡改的工程硬事实（Code IR），再结合分层摘要按需投影渲染多视角文档，杜绝大模型编造接口、表名与配置键。
2. **一仓多视角文档套件（Doc Pack）**：单次仓库快照解析后，默认输出「系统架构与核心链路、数据模型与接口契约、配置与运维部署、代码导读」4 篇互补文档，同时支持团队导入或微调自有设计文档模板。
3. **老文档 Gap 诊断而非盲从**：将仓内/上传的历史过时文档作为对照输入，产出「过时条款与缺失项差异报告（Gap Report）」，谨慎摘录业务背景供人工确认，绝不被错误旧描述污染新文档。
4. **Canvas Chat 协同精修（80 → 99 分）**：提供双栏协同工作台，支持选中指定章节发指令局部修改、源码只读 Tool Calling 核对、Diff 审阅应用、源码行级证据直跳与章节锁定防覆盖。
5. **全平台资产打通**：直接消费 `code-bench` 的 `Repository` 与 `ArchitectureElement` 主数据，定稿后同步发布至 `code-bench` 开发者文档中心，并通过 Commit 对比雷达防止文档再次腐烂。

---

## 2. 关键业务假设 (Key Assumptions，已与业务方确认)

| 编号 | 业务假设内容 | 受影响的特性 |
| :--- | :--- | :--- |
| **KA-01** | **P0 主力语言栈**：首期确定性 Code IR 解析优先覆盖 **C/C++（含 Protobuf / CROS RPC 服务）** 与 **Java / Python** 后端服务；Go 与 TypeScript/React 暂列 P1/P2。 | `F-04`, `F-05`, `F-08` |
| **KA-02** | **Git 拉取与凭据假设**：P0 不自建凭据保险库，假定 `code-archdoc-server` 宿主机已配置全局免密只读 SSH Key，直接使用 `Repository.URL`（SSH）执行 `git clone/fetch` 至本地隔离沙箱。 | `F-02`, `F-04` |
| **KA-03** | **LLM 算力接入假设**：P0 直连任意 **OpenAI Compatible HTTP/SSE 接口**（管理员在后台配置 `BaseURL`、`APIKey` 与分层模型名），兼容直连 `code-gate`（`:8088`）或其他推理服务，不绑定外部 CLI 执行器。 | `F-05`, `F-09`, `F-14` |
| **KA-04** | **分析对象粒度假设**：P0 支持「单代码仓（`Repository`）」及「Monorepo 子目录（关联 `code-bench` 的 `ArchitectureElement.RepoID + Subdirectory`）」；跨多仓子系统级文档聚合放至 P1。 | `F-01`, `F-04`, `F-16` |
| **KA-05** | **定稿发布出口假设**：P0 聚焦平台内定稿发布、同步 `code-bench` 开发者文档展示与 Markdown/ZIP 批量导出；向源码仓自动提 Git MR/PR 放至 P1。 | `F-11`, `F-17` |
| **KA-06** | **权限治理假设**：已发布文档全员只读可见；仓库负责人（`Owner` / `RelatedMembers`）与管理员（`archdoc_admin` / `super_admin`）可触发生成、Chat 精修与定稿发布；管理员独占模板套件与 LLM 接口配置权限。 | `F-03`, `F-13`, `F-14` |

---

## 3. 需求功能矩阵 (Feature Matrix)

> 优先级定义：**P0 (Must-Have)** 首期核心闭环且须具备完整 TDD 单测；**P1 (Should-Have)** 重要增强与自动化进阶；**P2 (Nice-to-Have)** 远期扩展。

### 子域 A：主数据集成、模板管理与仓库快照 (Repository & Template Governance)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-01** | **CodeBench 仓库与架构子目录同步视图** | `Backend`<br>`Frontend` | **P0** | 直接读取共享库 `repositories` 与 `architecture_elements`（支持按 `RepoID + Subdirectory` 选定 Monorepo 范围），展示各仓库文档覆盖状态、最近分析 Commit SHA 及新鲜度评分；列表支持关键词/部门/状态筛选与 URL 同步分页（默认 25 条/页）。 | 1. 复用 `repositories` 表无重复建表。<br>2. 分页参数与 URL Search Params 双向同步；Monorepo 子目录路径规范化校验并拦截 `..` 路径穿越（`400`）。 |
| **F-02** | **无状态只读 Git 沙箱同步引擎** | `Backend` | **P0** | 基于宿主机免密 SSH Key 对选定仓库执行浅克隆/增量 `fetch` 至沙箱目录；内置同路径并发互斥锁、残留 `.git/*.lock` 自动自愈、独立进程组超时强杀防孤儿进程，并记录分析对应的 `commit_sha`。 | 1. 同仓并发触发同步时串行排队不冲突。<br>2. 鉴权失败快速阻断并透出明确错误；Context 取消时无残留 `git` 僵尸进程。 |
| **F-03** | **多视角文档套件（Doc Pack）与模板中心** | `Backend`<br>`Frontend`<br>`Models` | **P0** | 内置 4 篇默认标准视角模板：① **系统架构与核心链路**、② **数据模型与接口契约**、③ **配置与运维部署**、④ **代码导读**；以结构化 **Slot Schema** 建模（定义章节标题、必填性、输入数据源类型、图表/表格要求、Prompt 指导），支持管理员导入/编辑 Markdown 模板覆盖或扩展套件。 | 1. 初始化自动 Seed 默认 4 篇套件且幂等。<br>2. 管理员 CRUD 模板与 Slot 校验通过；重复套件编码返回 `409`；非管理员修改返回 `403`。 |

### 子域 B：确定性事实抽取与反推生成引擎 (Code IR & Synthesis Pipeline)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-04** | **L1 确定性静态事实抽取（Code IR）** | `Backend` | **P0** | 对每个 `(repo_id, subdir, commit_sha)` **仅抽取并缓存 1 次**结构化硬事实：<br>• **通用**：目录树职责、构建脚本（`CMake`/`Makefile`/`pom.xml`/`requirements.txt`/`pyproject.toml`）、配置文件键清单；<br>• **C/C++**：头文件/源文件映射、宏/结构体/类定义、`.proto` 与 CROS RPC 服务契约（有 `code-contract` 资产时支持复用）；<br>• **Java / Python**：Spring/FastAPI/Flask 路由签名、Entity/ORM 模型、入口点与关键依赖拓扑。 | 1. 针对 C/C++、Java、Python 测试夹具仓库，确定性提取目录、类/结构体、接口与配置键覆盖率 100%，零 LLM 调用即可产出 Code IR JSON。<br>2. 相同 `(repo_id, subdir, commit_sha)` 命中缓存不重复解析。 |
| **F-05** | **L2 分层语义摘要与多视角并发反推** | `Backend`<br>`Models` | **P0** | 针对大仓按模块/目录做语义分片（Map）生成模块职责摘要与术语表，再按选定 Doc Pack 的各视角模板并发填充章节槽位（Reduce）；支持按视角挂载文件 Scope 过滤器（如运维篇聚焦构建与配置，契约篇聚焦接口与模型）；支持单仓单篇、单仓全套件或批量多仓排队执行与任务取消/断点恢复。 | 1. 跨视角共享同一 `RepoSnapshot` 的 Code IR 与术语表，不重复拉码。<br>2. 任务状态机严格遵循 `pending -> extracting -> generating -> verifying -> ready/failed`，支持取消与异常恢复。 |
| **F-06** | **历史过时文档 Gap 诊断报告** | `Backend`<br>`Frontend` | **P0** | 自动发现仓内既有 `docs/` / `README.md` 或支持用户上传历史设计文档作为对照输入；在代码反推完成后独立比对输出 **Gap Report**（标注过时描述、缺失章节、已变更符号），并谨慎提取仅存于旧文档的业务背景放入 `[待人工确认]` 区块，严禁覆盖源码事实。 | 1. 测试用例注入过时接口名/旧配置项时，Gap Report 准确检出不一致条目。<br>2. 生成草稿中的硬事实现实符号完全来自 Code IR，不被旧文档误导。 |
| **F-07** | **证据锚定与自动化防幻觉校验门禁** | `Backend`<br>`Frontend` | **P0** | 为生成结论绑定代码证据元数据（`file_path`、`line_start~line_end`、`symbol`、`commit_sha`）；落库前执行自动化 Verifier：校验文中提及的文件路径、类名、接口/表名是否存在于 Code IR、校验 Mermaid 语法块合法性、计算模板必填槽位覆盖率并给出可信度标记。 | 1. 若 LLM 输出包含 Code IR 中不存在的伪造路径或符号，Verifier 准确拦截并标记 `unverified` 告警。<br>2. 必填槽位缺失或 Mermaid 语法错误可准确识别并触发局部修复或告警。 |

### 子域 C：Chat 协同工作台与人工审校 (Canvas Chat & Review)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-08** | **双栏协同审校工作台（Canvas Studio）** | `Frontend`<br>`Backend` | **P0** | 左侧渲染多视角文档目录树与结构化章节（支持 Markdown、表格、Mermaid 渲染与 Gap Report 提示），右侧提供 Chat 协同面板及只读源码抽屉；点击任意证据标签可直跳对应 `commit_sha` 的只读源码行高亮预览。 | 1. 亮/暗双模主题完整适配，零硬编码颜色。<br>2. 源码预览接口严格校验路径在沙箱根目录下，拦截 `../` 越权访问（`403`）。 |
| **F-09** | **章节级局部指令、Diff 审阅与只读 Tool Calling** | `Backend`<br>`Frontend` | **P0** | 支持用户对整篇或**选中指定章节（Section）**发起自然语言指令（如补充业务背景、改画时序图、纠正分支逻辑）；AI 挂载只读代码检索工具（`list_files`、`read_file`、`grep_symbol`）实时核对源码，流式（SSE）返回**章节级 Diff**；用户确认 `Accept` 后生成不可变 `Revision` 并自动重跑 Verifier。 | 1. 局部修改仅影响目标 `Section`，未选中章节字节级无变动。<br>2. 每次 `Accept` 产生独立 `Revision` 支持版本对比与一键回滚。 |
| **F-10** | **章节所有权与人工锁定保护（Section Lock）** | `Backend`<br>`Frontend`<br>`Models` | **P0** | 章节支持 `auto`（代码托管）、`hybrid`（混合）与 `locked`（人工锁定）三种模式；用户通过 Chat 注入业务背景或手工修订后可对章节加锁，后续代码变更触发重新反推时，`locked` 章节自动豁免覆盖（仅提示待核对漂移）。 | 重新触发全篇反推后，`locked` 章节内容保持原样不丢失，`auto` 章节按新代码刷新。 |

### 子域 D：发布闭环、新鲜度雷达与平台治理 (Publishing, Drift Radar & RBAC)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-11** | **定稿发布、CodeBench 同步与导出** | `Backend`<br>`Frontend` | **P0** | 支持将审校完成的 Doc Pack 标记为 `Approved`（已发布）；支持一键同步发布至 `code-bench` 开发者文档目录树（含跨文档相对链接与术语一致性检查），并支持单篇 Markdown 与全套件 ZIP 打包下载。 | 1. 未通过负责人/管理员审批的草稿不对外发布为正式版。<br>2. ZIP 导出包含全部视角文档与目录索引，路径安全无越权。 |
| **F-12** | **文档新鲜度与 Commit 漂移雷达** | `Backend`<br>`Frontend` | **P0** | 对比仓库当前分支 `HEAD` 与最近已发布文档的 `commit_sha`，识别自上次生成以来变更的文件类型（如接口/模型/配置目录变动占比），计算并展示仓库维度的「文档完备度」与「漂移度评分（Drift Score）」，支持一键触发增量或全量刷新。 | 1. 仓库无新提交时漂移度为 0；核心代码文件发生变更后准确标出受影响视角与待刷新提示。<br>2. 全员可读雷达看板状态。 |
| **F-13** | **三层 RBAC 权限守卫与字段级审计** | `Backend`<br>`Models` | **P0** | 集成 `commonAuth.AuthMiddleware` 与 `RequireAdmin("archdoc_admin")`：<br>• **普通用户**：仅只读浏览已发布文档、证据源码与漂移看板；<br>• **仓库负责人（`Owner` / `RelatedMembers`）**：可触发生成、使用 Chat 修订、加锁与发布所属仓库文档；<br>• **管理员（`archdoc_admin` / `super_admin`）**：全量仓库文档权限 + 模板中心 + OpenAI LLM 接口配置。<br>所有写操作接入 `commonAudit` 记录字段级 Diff 审计。 | 1. 未登录返回 `401`。<br>2. 非所属仓普通用户发起生成/Chat/发布返回 `403`。<br>3. 非 `archdoc_admin` 修改模板或 LLM 配置返回 `403`。<br>4. 关键操作审计表记录完整 Old/New Diff。 |
| **F-14** | **OpenAI Compatible 多模型分层配置中心** | `Backend`<br>`Frontend` | **P0** | 为 `archdoc_admin` 提供 LLM 推理配置页：配置 OpenAI Compatible `BaseURL`、`APIKey`（服务端脱敏展示且加密落库）、超时重试参数，并支持按阶段配置分层模型（如轻量摘要模型 vs 旗舰架构综合模型）与连通性探活测试。 | 配置保存后返回体 `api_key` 自动脱敏（`sk-****`）；探活接口可验证模型可用性并返回状态。 |

### 子域 E：P1/P2 进阶演进特性 (Post-MVP Roadmap)

| 编号 | 特性名称 | 涉及层级 | 优先级 | 功能描述与技术规范 | TDD 验收指标 / 衡量标准 |
| :--- | :--- | :---: | :---: | :--- | :--- |
| **F-15** | **多条 PR 式批注一键批量修订 & AI 反向澄清** | `Backend`<br>`Frontend` | **P1** | 支持用户在文档多个位置先连续挂载 Review 批注，一键交给 AI 批量收敛修订；生成/精修遇代码歧义时 AI 主动抛出澄清选择题等待用户确认后继续。 | 批注合集修订后各条目准确闭环并产出统一 Revision；澄清状态挂起与恢复正确。 |
| **F-16** | **跨多仓子系统聚合文档反推** | `Backend`<br>`Frontend` | **P1** | 基于 `code-bench` 的 `ArchitectureElement` 树聚合同一子系统下的多个关联代码仓，联合生成系统级总体架构、跨仓接口调用拓扑与联调时序文档。 | 选中子系统节点后可拉取关联多仓快照并生成跨仓服务依赖视图。 |
| **F-17** | **Git 凭据保险库与回推源码仓自动提 MR/PR** | `Backend`<br>`Frontend` | **P1** | 内建 AES-256-GCM 凭据管理（支持 CodeHub/GitLab PAT 与独立 SSH Key），支持将定稿文档自动创建分支写入目标仓 `docs/` 并发起 MR/PR，以及配合 Webhook 在代码合并后自动触发局部文档漂移刷新。 | 凭据加密落库且内存注入不落盘；Mock GitLab API 验证创建分支与提 MR 成功。 |
| **F-18** | **扩展语言支持（Go / TypeScript / React）** | `Backend` | **P2** | 扩展 L1 Code IR 解析器支持 Go（AST/Gin/GORM）与 TypeScript/React（路由/组件树/API 调用提取），覆盖平台自身 `code-*` 微前端全栈工程。 | 针对 `code-template` 示例仓可准确提取路由、GORM 表模型与前端组件树。 |

---

## 4. 核心验收场景矩阵 (Verification Scenarios)

| 场景编号 | 场景名称 | 触发条件与输入特征 | 预期行为与 TDD 断言 |
| :--- | :--- | :--- | :--- |
| **SC-01** | **免密探针与多级 RBAC 越权拦截** | 1. 请求 `GET /api/health`；<br>2. 无 Token 请求 `GET /api/repos`；<br>3. 非仓库 Owner/Member 的普通用户调用 `POST /api/repos/:id/generate` 或 `PUT /api/admin/llm-config`。 | 1. 返回 `200 OK`（`service: "Code-Archdoc"`）；<br>2. 返回 `401 Unauthorized`；<br>3. 越权请求均被拦截返回 `403 Forbidden`。 |
| **SC-02** | **多语言 L1 Code IR 抽取与缓存复用** | 选定含 C/C++（`.proto`）+ Python 的仓库快照，依次触发「系统架构」与「接口契约」两篇文档生成。 | Git 同步与 Code IR 抽取仅执行 1 次；L1 JSON 准确包含 `.proto` 服务/C++ 类/Python 路由，两篇文档共享同一快照实体字典。 |
| **SC-03** | **过时旧文档 Gap 诊断与防污染** | 仓库内存在含错误旧接口 `/v1/legacy_pay` 的 `docs/design.md`，而源码实际已迁移为 `/v2/orders/pay`。 | 新生成的接口契约仅出现 `/v2/orders/pay`；Gap Report 明确告警旧文档存在过时接口 `/v1/legacy_pay`。 |
| **SC-04** | **Verifier 防幻觉符号校验拦截** | Mock LLM 在生成章节中编造不存在的文件 `src/fake_engine.cpp` 或非法 Mermaid 语法。 | 落库前校验器检出未验证路径并标记 `unverified` 警告，阻止静默标为高可信定稿。 |
| **SC-05** | **Canvas Chat 局部修订、Diff 应用与章节锁定** | 仓库 Owner 选中第 2 章输入指令“补充超时重试时序图”，审阅 Diff 后点击 `Accept` 并对第 1 章加 `locked` 锁；随后代码推进新 Commit 并重跑全篇生成。 | 1. 局部修订仅更新第 2 章，第 1/3/4 章字节不变且生成 `Revision`；<br>2. 重跑后第 1 章（`locked`）完整保留，第 2~4 章按新代码刷新。 |
| **SC-06** | **Monorepo 子目录参数校验与路径穿越防护** | 1. 传入关联 `Subdirectory: "../../etc"` 请求生成；<br>2. 在只读源码查看接口传入 `path=../../../etc/passwd`。 | 均在参数/沙箱边界直接拦截，返回 `400` / `403`，严禁读取沙箱外文件。 |
| **SC-07** | **定稿发布、CodeBench 同步与漂移感知** | Owner 将 4 篇文档全部审批为 `Approved` 并触发导出/发布；随后仓库新增含 `models/` 改动的模拟 Commit。 | 发布后普通用户可只读浏览与下载 ZIP；模拟新 Commit 后漂移雷达立即将该仓状态更新为“存在结构变更待刷新”。 |

---

## 5. 非目标边界界定 (Non-Goals)

- **Non-Goal 1（主数据自治禁令）**：不重复自建独立的用户中心、部门树或代码仓主表，统一复用共享库 `users`、`departments`、`repositories` 及 `code-bench` 的 `architecture_elements`。
- **Non-Goal 2（P0 语言边界）**：P0 不承诺支持 Go、TypeScript/React、Rust 等全部语言的高精度 L1 AST 提取（首期聚焦 C/C++（含 Protobuf/CROS RPC）与 Java/Python，其他文件走通用目录与基础文本摘要）。
- **Non-Goal 3（P0 Git 凭据与写操作边界）**：P0 不建设租户凭据保险库 UI，也不直接向远程 Git 仓库推分支/提 MR（均在 P1 演进）。
- **Non-Goal 4（P0 跨仓聚合边界）**：P0 不自动跨多个独立 Git 仓库联合生成系统级文档（仅支持单仓及同仓 `Subdirectory` 子目录）。
- **Non-Goal 5（AI 驱动边界）**：P0 不集成 Claude Code / OpenCode / Codex 等本地有状态 CLI 子进程调度器，统一通过纯 Go OpenAI-compatible HTTP/SSE 客户端调度推理。
- **Non-Goal 6（表前缀与 UI 技术栈红线）**：不在共享 PostgreSQL 中创建无 `archdoc_` 前缀的裸表；前端不引入 Ant Design / Material UI 等重型组件库，全面使用 `@code/common` 与原生 Vanilla CSS。

---

## 6. 非功能性与安全红线 (NFR & Guardrails)

1. **架构防腐与解耦**：
   - 建立类似 `code-shield` 的静态架构检查（`make lint-arch`）：`L1 静态解析器`、`模板引擎`、`LLM HTTP Client` 严禁直接依赖 `models.DB`，必须通过清晰接口与领域对象解耦。
2. **数据库隔离与测试红线**：
   - 所有涉及 PG 读写的单元测试强制使用 `code-common/backend/testdb.SetupIsolatedDB(t)` 动态创建/销毁隔离库；
   - Git 同步、L1 解析、LLM Invoker（使用 `httptest` Mock SSE/JSON 响应）与 Verifier 须具备独立单元测试覆盖正常、边界、异常与越权路径。
3. **沙箱与凭据安全**：
   - 沙箱工作区按 `(repo_id, subdir)` 严格做 `filepath.Rel` 路径穿越阻断；`git` 命令执行使用独立进程组（`Setpgid`）防止取消或超时后残留僵尸进程；
   - LLM `APIKey` 在数据库中加密存储、接口返回脱敏、日志与审计记录禁止打印明文密钥。
4. **可观测与成本控制**：
   - 每次生成/Chat 调用记录 Token 消耗、耗时、模型名及阶段（映射摘要 vs 槽位综合），支持任务级并发上限控制与失败可恢复；优先使用稳定前置上下文顺序以最大化 `code-gate` 等网关的 Prompt KV Cache 命中率。
5. **前端体验与双模规范**：
   - 严格遵循 `code-common/rules/AGENTS.md`：100% 使用 `--color-*` Design Tokens 适配亮/暗双模主题、Flat BEM 命名（`.code-archdoc-*`）、URL 状态同步 5 页滑动分页（默认 25 条/页）、禁止滥用 `any` 与静态内联 `style`。

---

## 7. 术语表 (Glossary)

| 术语 | 定义 |
| :--- | :--- |
| **Code IR (L1 静态事实层)** | 由确定性 AST/文本规则扫描从代码仓直接提取的机器级结构化事实 JSON（含目录树、数据模型、接口签名、配置键、依赖关系），作为防幻觉的唯一客观底座。 |
| **Doc Pack (多视角文档套件)** | 针对同一代码仓（或 Monorepo 子目录）按不同受众视角组织的一组关联设计文档集合（默认含架构、数据与接口、运维配置、代码导读 4 篇）。 |
| **Slot Schema (模板槽位契约)** | 将设计文档模板拆解为结构化章节定义，明确各章节的数据来源、图表/表格结构约束、Prompt 指引及所有权模式。 |
| **Gap Report (过时与缺失差异报告)** | 将代码反推得到的客观事实与既有历史文档比对后产出的诊断清单，标出过时条款、遗漏模块与仅旧文档存在的业务背景线索。 |
| **Canvas Studio (双栏协同审校台)** | 集成了多文档阅读树、章节局部 Chat 指令、Diff 预览应用、章节锁定与源码行级证据直跳的交互式工作台。 |
| **Drift Score (文档漂移度)** | 衡量仓库当前最新提交（`HEAD`）与已发布设计文档所绑定 `commit_sha` 之间实质结构变化程度的指标，用于预警文档过时风险。 |
