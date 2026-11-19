# ADR-03: 跨提交稳定证据锚点与 `code-bench` 挂载发布契约

- **文档编号**：`03-evidence-anchor-and-publish-mount.md`
- **日期**：2026-11-19
- **状态**：已采纳 (Accepted)
- **关联议题**：[`01-feature-matrix.md`](../01-features/01-feature-matrix.md) (`KA-05`, `D-01`~`D-03`, `F-07`, `F-08`, `F-11`, `F-12`, `F-25`, `Non-Goal 8`) 与阶段一下沉选型议题 3 & 4

---

## 1. 背景与问题陈述 (Context & Problem Statement)

`code-archdoc` 旨在构建「可验证、可保鲜、可发布至门户文档中心」的活体架构文档体系，涉及两个贯穿前后端与跨模块协同的核心契约：

1. **议题 3 — 跨提交稳定证据锚点（Evidence Anchor）**：
   - 文档的每条核心架构/接口结论都必须绑定源码证据，既要支持工作台内点击直跳源码行高亮（`F-08`），又要作为 `F-12` 章节级 `Code Drift` 定位的判定依据（`SC-11`：仅修改函数 `OrderService::Pay` 时，仅引用该函数的第 3 章触发漂移）。
   - 若仅使用 `(file_path, line_start, line_end)` 作为锚点，文件顶部增删注释或 `#include` 就会导致下方所有函数行号偏移，产生大面积漂移误报与深链错位。

2. **议题 4 — 发布目录布局与 `code-bench` 符号链接挂载契约**：
   - `KA-05` 确认 P0 同机部署模式下，`code-archdoc` 将定稿文档落盘至自有受控发布目录 `publish_dir`，部署时在 `code-bench` 的 `docs.path` 下建立符号链接子树挂载（`D-01`）。
   - `code-bench` 现有的 `scanDocDir`（`code-bench/handlers/docs.go`）已支持符号链接目录与循环检测，且**自动忽略以 `.` 或 `_` 开头的文件与目录**，并自动隐藏空目录。
   - 必须保证：未审批草稿零泄露、发布/覆盖过程原子不可见中间态、撤回后 `archdoc` 与 `code-bench` 双向彻底清除（含目录重命名或清空后无残影），同时明确与 `D-02`（Frontmatter 隐藏与元信息栏）和 `D-03`（平台路由深链）的通用跨模块契约，并为 P1 跨机部署预留演进路径。

---

## 2. 决策 1：采用「语义符号键 + 内容指纹 + 快照展示坐标」双轨证据锚点

### 2.1 锚点数据契约 (`EvidenceAnchor`)

所有证据锚点统一采用以下结构，同时持久化于 `archdoc_section_evidences` 表与章节结构化元数据中：

```json
{
  "anchor_id": "ev_01j8x9k2m3",
  "anchor_key": "cpp_func::src/order/order_service.cpp::order::OrderService::Pay",
  "file_path": "src/order/order_service.cpp",
  "symbol_kind": "cpp_func",
  "fq_symbol": "order::OrderService::Pay",
  "content_fingerprint": "sha256:9f86d081884c7d65",
  "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "line_start": 142,
  "line_end": 188,
  "confidence": "exact"
}
```

| 字段 | 职责类别 | 跨提交稳定性与规则说明 |
| :--- | :---: | :--- |
| `anchor_key` | **稳定主键** | 确定性拼接 `<symbol_kind>::<normalized_file_path>::<fq_symbol>`；当证据为整文件/配置键/Proto RPC 时，`symbol_kind` 分别取值 `file`、`config_key`、`proto_rpc`、`http_route`、`data_model`、`cpp_class`、`cpp_struct`、`cpp_func`、`java_class`、`java_method`、`py_class`、`py_func`、`runtime_primitive` |
| `file_path` | **稳定定位** | 相对于沙箱根（`subdir`）的 POSIX 正斜杠路径（无前导 `./` 或 `/`） |
| `fq_symbol` | **稳定定位** | 符号全限定名（如 `order::OrderService::Pay`、`order.v1.OrderService/CreateOrder`、`redis.pool_size`）；`symbol_kind == "file"` 时为空字符串 |
| `content_fingerprint` | **语义指纹** | 符号主体归一化后的 `sha256` 前 16 位十六进制值：**移除纯行尾空白与前导缩进差异**（对 CST 节点剔除无关注释节点后取 token 序列哈希），确保上游插入空行/无关函数不改变本符号指纹 |
| `commit_sha` | **快照坐标** | 生成或修订该章节时绑定的精确 Git 提交哈希 |
| `line_start` / `line_end` | **展示坐标** | 仅用于 `commit_sha` 快照下的精确源码行高亮，**不参与跨提交漂移等价判断** |
| `confidence` | **可信度** | `exact`（AST/Proto 精确命中）或 `heuristic`（启发式原语/文本引用） |

### 2.2 跨提交重定位与漂移判定规则

1. **漂移判定优先级（`F-12`）**：
   - 若远端 `HEAD` 与文档 `commit_sha` 之间变更的文件集合 `ChangedFiles` **不含** `anchor.file_path`：直接判为 **无漂移（Unchanged）**，零解析开销。
   - 若 `anchor.file_path` 被删除或重命名：直接判为 **文件删除/重命名漂移（`file_removed`）**。
   - 若 `anchor.file_path` 被修改：仅针对该文件的新版内容运行对应语言的 L1 解析器，按 `fq_symbol` 查找目标符号：
     - 目标符号消失 → 标记 **`symbol_removed`**；
     - 目标符号存在且 `new_fingerprint != anchor.content_fingerprint` → 标记 **`symbol_modified`**；
     - 目标符号存在且 `new_fingerprint == anchor.content_fingerprint`（仅行号上下平移）→ 判为 **无漂移**，彻底消除行号漂移误报（精准满足 `SC-11`）。
   - 对于 `symbol_kind == "file"` 的文件级锚点：文件有变更即标记 **`file_modified`**。
2. **源码证据平台深链契约（`F-08`、`F-11`、`D-03`）**：
   - 在发布的 Markdown 与工作台内部，证据引用统一渲染为平台路由深链：
     ```text
     /archdoc/docs/<doc_id>/evidence?commit=<commit_sha>&file=<url_encoded_file_path>&symbol=<url_encoded_fq_symbol>&lines=<line_start>-<line_end>
     ```
   - 读者在 `code-bench` 开发者文档中心点击该链接时，经 `D-03` 走前端 SPA 路由进入 `/archdoc`，自动打开只读源码证据抽屉并按 `commit` + `file` + `lines` 高亮定位；若用户切换至最新提交浏览，则通过 `symbol` 重新定位当前行号。

---

## 3. 决策 2：`publish_dir` 原子目录布局与 `code-bench` 通用挂载契约

### 3.1 `publish_dir` 物理目录结构与 `code-bench` 符号链接挂载（`D-01`）

部署时在 `code-bench` 的 `docs.path` 目录下建立一条只读语义的符号链接（例如 `ln -s /var/lib/code-archdoc/publish <docs.path>/90-架构反推文档` 或 `<docs.path>/archdoc`）。`code-archdoc` 严格控制 `publish_dir` 内部布局如下：

```text
<publish_dir>/
├── .staging/                                     # 原子写盘暂存区（"." 前缀被 code-bench scanDocDir 自动忽略）
│   └── tmp-<uuid>.md
├── README.md                                     # 自动生成的总览索引（列出已发布仓库与文档入口；无已发布文档时移除）
└── <repo_slug>/                                  # 由 Repository.Name 规范化生成的安全目录名（禁止 . / _ 开头与 ..）
    ├── README.md                                 # 仓库级文档索引（跨文档相对链接导航）
    ├── system-architecture.md                    # 整仓级已发布文档（<doc_type_slug>.md）
    ├── data-and-contract.md
    └── sub/                                      # Monorepo 子目录命名空间（隔离避免与整仓 <doc_type>.md 冲突）
        └── <sanitized_subdir_path>/              # 如 svc/order/
            ├── README.md                         # 子目录级文档索引
            └── system-architecture.md
```

**关键安全与防冲突设计**：
1. **隐藏目录防半成品泄露**：利用 `code-bench/handlers/docs.go` `scanDocDirInternal` 忽略 `.` 与 `_` 前缀条目的既定行为，所有写盘操作均先写入 `<publish_dir>/.staging/tmp-<uuid>.md`，调用 `Sync()` 后执行同文件系统原子 `os.Rename()` 移入目标路径，确保读者与 `code-bench` 永远不会读到截断或未写完的 Markdown。
2. **禁止前缀隐藏误杀**：路径规范化函数 `SanitizePublishSegment` 确保 `<repo_slug>` 与 `<sanitized_subdir_path>` 的每一级目录名均不以 `.` 或 `_` 开头（若源码子目录如 `_internal` 以 `_` 开头，发布路径映射为 `dir_internal`），防止文档被 `code-bench` 静默跳过。
3. **重命名与撤回零残留**：`archdoc_documents.published_rel_path` 持久化当前落盘相对路径。重新发布时若路径因仓库改名发生变化，先落盘新文件再删除旧路径；撤回（Unpublish）时，删除目标 `.md` 文件、重建该仓库/总览 `README.md`（若该范围下已无其他已发布文档，则一并删除 `README.md` 并自底向上清理空目录），确保 `code-bench` 文档树与 `archdoc` 发布视图双向即时消失（满足 `SC-10`）。
4. **草稿物理隔离**：草稿与历史修订仅存于 PostgreSQL，**只有 `status == "approved"` 且触发发布动作的文档修订才允许写入 `publish_dir`**（满足 `SC-07`）。

### 3.2 YAML Frontmatter 标准化契约（对齐 `D-02`）

每篇落盘至 `publish_dir` 的 Markdown 文件头部固定写入标准 YAML Frontmatter（`---` 包裹）：

```yaml
---
title: "order-service 系统架构与核心链路"
repo: "order-service"
subdir: "svc/order"
doc_type: "system-architecture"
doc_type_name: "系统架构与核心链路"
doc_type_revision: 3
doc_id: 42
revision_no: 5
ref_name: "master"
commit_sha: "a1b2c3d4e5f6789012345678901234567890abcd"
published_at: "2026-11-19T15:30:00+08:00"
published_by: "zhangsan"
source_module: "archdoc"
---
```

**`code-bench` 侧 `D-02` 通用契约（遵循 `Non-Goal 8`）**：
- `DeveloperDocs.tsx` 在调用 `renderMarkdown` 前执行通用 Frontmatter 剥离器 `parseFrontmatter(rawMarkdown)`：仅当文件严格以第一行 `---` 开头且存在配对结束 `---` 行时，解析键值对并返回 `{ metadata, body }`；否则原样返回 `{ metadata: null, body: rawMarkdown }`，保证存量无 Frontmatter 文档 **100% 零回归**。
- 当 `metadata` 非空时，正文仅渲染 `body`（不再出现 YAML 原文），并在文档标题区下方渲染一个通用的、轻量的只读「文档元信息栏」（支持展示常见标准键的友好标签，如 `repo`/`doc_type`/`commit_sha`/`published_at`/`author`/`version` 等，任何文档均可受益，不含 `archdoc` 专有业务逻辑）。

### 3.3 已挂载模块前缀路由契约（对齐 `D-03`）

**`code-bench` 侧 `D-03` 通用契约（遵循 `Non-Goal 8`）**：
- `DeveloperDocs.tsx` 从宿主已有的模块列表（`/api/modules` 返回的 `ModuleMeta[].path`，如 `["/shield", "/pipeline", "/pdm", "/gate", "/contract", "/archdoc"]`）动态获取已挂载微前端模块前缀集合 `mountedModulePrefixes`。
- 在 `resolveMarkdownUrl(url, baseDocPath)` 与 `handleContentClick` 中，按以下严格顺序判定：
  1. 外部协议链接（`http://`/`https://`/`mailto:`）→ 保持外部跳转；
  2. 页内锚点（`#...`）→ 保持页内平滑滚动；
  3. `/api/` → 保持原下载/接口行为；
  4. `/docs/` → 保持原文档中心 SPA 跳转；
  5. **新增通用规则**：若 URL 以任意已挂载模块前缀开头（匹配 `trimmed === prefix || trimmed.startsWith(prefix + '/')`），返回 `{ href: trimmed, isExternal: false, isPortalRoute: true }`，点击时 `e.preventDefault(); navigate(href);` 走门户 React Router 跳转；
  6. 其余相对路径与文档内路径 → 保持原有 `/docs/<encodedPath>` 与附件 `/api/docs/raw` 行为完全不变。

### 3.4 P1 跨机部署演进路径

当未来 `code-archdoc` 与 `code-bench` 需要跨物理机/跨容器部署且不共享卷时：
- `publish_dir` 的落盘渲染逻辑封装在 `pkg/publisher/` 的 `ArtifactRenderer` 与 `PublishSink` 接口后；
- P0 实现 `LocalFSPublishSink`（写盘至 `publish_dir`）；
- P1 新增 `code-archdoc` 只读发布 API（`GET /api/published/tree`、`GET /api/published/content`），`code-bench` 仅需在文档源适配器中支持通用远程 HTTP 文档源拉取，无需改动任何文档内容格式、Frontmatter 或深链契约。

---

## 4. 决策后果与约束 (Consequences & Guardrails)

1. **同文件系统原子性要求**：`.staging/` 必须位于 `<publish_dir>` 目录内部，以确保 `os.Rename()` 始终在同一个挂载点（Filesystem Mount）内完成原子操作，避免 `EXDEV`（跨设备移动失败）。
2. **并发写安全**：发布/撤回服务对 `<publish_dir>` 内的索引重建与同 `(repo_id, subdir, doc_type)` 文件操作施加进程内路径级互斥锁，防止并发发布损坏 `README.md` 或竞态删文件。
3. **跨模块变更最小化**：`code-bench` 的 `D-02` 与 `D-03` 改造均为纯通用文档渲染能力增强，必须附带 `code-bench` 自身的单元测试/组件测试验证存量无 Frontmatter 文档与 `/docs/`、`/api/`、相对路径链接零回归。
4. **证据锚点升级兼容性**：`anchor_key` 格式包含 `symbol_kind` 前缀，未来新增语言（如 `W2` Java/Python、`P2` Go/TS）只需扩展枚举值，不破坏已有 C/C++ 锚点索引。
