# 代码设计文档反推 (`code-archdoc`) HTTP & SSE 接口与权限契约规范

- **文档编号**：`01-http-api-contract.md`
- **文档版本**：v1.6.0 (ADR-09 演进：`GET /api/arch-tree` 模块级三态树表契约冻结与 `GET /api/repos` 新增 `arch_binding=unbound_repo` 仓库级未关联筛选)
- **编制日期**：2026-11-23
- **归属阶段**：阶段三：接口与数据契约冻结 (`03-api`)
- **上游基线**：[`docs/01-features/01-feature-matrix.md`](../01-features/01-feature-matrix.md) (v0.9.0)、[`docs/02-design/01-system-architecture.md`](../02-design/01-system-architecture.md) ~ [`05-database-schema.md`](../02-design/05-database-schema.md) (v1.4.0)、[`ADR-01`](../05-adr/01-microfrontend-and-table-prefix-isolation.md) ~ [`ADR-09`](../05-adr/09-arch-tree-primary-view-and-unassigned-repos.md)
- **配套契约**：[`02-data-model-and-dto-contract.md`](./02-data-model-and-dto-contract.md)（JSONB Schema、枚举状态机、仓储接口与 TypeScript 类型字典）

---

## 1. 全局通信、路由、鉴权与错误契约

### 1.1 路由前缀与双模访问约定
- **独立调试路径**：`/api/<endpoint>`（后端默认监听 `:8100`，Vite 调试 `:5182` 代理 `/api` 与 `/archdoc/api`）
- **门户网关代理路径**：`/archdoc/api/<endpoint>`（由 `commonServer.Run` 自动剥离 `/archdoc` 前缀，Gin Handler 统一注册于 `/api/*`）
- **认证头**：受保护接口必须携带 `Authorization: Bearer <JWT_TOKEN>`；SSE 接口通过 `fetch`（经 `setupFetchInterceptor` 自动注入 Bearer JWT）发起，严禁通过 URL Query 明文传递 Token。

### 1.2 四级鉴权级别定义（对齐 `F-13`、`SC-01`、`SC-12`）

| 级别简称 | 准入条件 | 越权 / 未认证响应 |
| :--- | :--- | :--- |
| **`Public`** | 无需认证（免密探针与独立模式登录） | — |
| **`JWT`** | 携带有效平台 JWT 的任意已激活用户（普通登录用户） | 无/非法 Token → `401 Unauthorized` |
| **`ScopeWrite`** | 满足三层范围写权限判定（`CheckScopeWritePermission`）：<br>1. `super_admin` 或 `archdoc_admin`；<br>2. 目标仓库 `Repository.OwnerID == user.ID` 或 `Repository.RelatedMembers` 包含 `user.ID`（涵盖整仓及所有子目录）；<br>3. 当 `subdirectory != ""` 时，`ArchitectureElement(repo_id, normalized_subdir).OwnerID == user.ID`（**严格全等匹配，子目录负责人访问同仓其他子目录或整仓返回 `403`**）。 | 未认证 → `401`<br>非负责人 → `403 Forbidden` |
| **`Admin`** | 挂载 `commonAuth.RequireAdmin("archdoc_admin")`（`archdoc_admin` / `super_admin` 独占） | 未认证 → `401`<br>非管理员 → `403 Forbidden` |

> **草稿可读隔离红线（`F-11` / `04-workbench-and-security.md` §4.1）**：
> - 未发布草稿（`status == "draft"`）及其修订历史、Diff、源码草稿上下文仅 **`ScopeWrite`** 可见，普通用户访问返回 **`403 Forbidden`**；
> - 已发布文档（`status == "approved"`）对所有 `JWT` 用户只读可见；当负责人正在编辑未审批的新修订（`current_revision_id != published_revision_id`）时，普通用户调用文档详情、导出、源码预览接口**严格仅返回 `published_revision_id` 快照**，绝不泄露草稿内容。

### 1.3 标准分页契约（对齐工作区 `AGENTS.md`）
- **Query 入参**：`page`（默认 `1`，`< 1` 自动归为 `1`）、`page_size`（允许值 `15 | 25 | 50 | 100`，缺省默认 **`25`**，非法值回退 `25`）。
- **标准响应体 (`200 OK`)**：
```json
{
  "items": [],
  "total": 42,
  "page": 1,
  "page_size": 25
}
```

### 1.4 统一错误响应与机器可读错误码表
所有非 `2xx` 错误统一保持向后兼容的 `error` 字段（简体中文说明），并补充稳定机器可读 `code` 与可选 `details`：

```json
{
  "error": "子目录参数非法：禁止包含 '..' 或绝对路径前缀",
  "code": "INVALID_SUBDIR",
  "details": {}
}
```

| HTTP 状态码 | `code` 常量 | 触发场景与 `details` 约定 |
| :---: | :--- | :--- |
| `400` | `INVALID_PARAM` | 通用参数格式/枚举/JSON 校验失败 |
| `400` | `INVALID_SUBDIR` | `subdirectory` 含 `..`、`\x00`、`\` 或前导 `/`（`SC-06`） |
| `400` | `INVALID_SLUG` | `DocType` `slug` 不满足 `^[a-z0-9][a-z0-9_-]{1,62}$`（`SC-08`） |
| `400` | `DOC_TYPE_INACTIVE` | 触发生成时勾选了已停用或不存在的 `DocType` |
| `400` | `ENGINE_UNAVAILABLE` | 请求显式指定 `engine="opencode"` 但服务端未启用（`opencode.enabled=false`）或未检测到可用 OpenCode CLI，`details: {"engine": "opencode", "reason": "..."}` |
| `401` | `UNAUTHORIZED` | 缺失或无效 JWT |
| `403` | `FORBIDDEN` | 非负责人操作范围资源、子目录负责人越权（`SC-12`）、非管理员调用管理接口（`SC-01`） |
| `403` | `DRAFT_ACCESS_DENIED` | 普通用户尝试访问未发布草稿或修订历史 |
| `403` | `PATH_ESCAPE` | 源码预览或沙箱路径发生目录穿越或沙箱外符号链接逃逸（`SC-06`） |
| `404` | `NOT_FOUND` | 仓库、文档、修订、任务、`DocType` 或源码文件不存在 |
| `409` | `CONFLICT` | `DocType` 新建/克隆重名 |
| `409` | `DOC_TYPE_IN_USE` | 尝试物理删除已被 `approved` 已发布文档引用的 `DocType`（`SC-08`），`details: {"published_doc_count": 3}` |
| `409` | `OPTIMISTIC_LOCK_CONFLICT` | 手工直编 / Chat Accept / 回滚的 `base_revision_no` 落后于服务端当前版本，`details: {"current_revision_no": 5}` |
| `422` | `VERIFIER_BLOCKED` | 发布时目标修订未通过 Verifier 硬门禁（如命中明文凭据泄漏 `blocker_count > 0`），`details: {"verifier_report": {...}}`（`SC-04`） |
| `429` | `QUEUE_FULL` | 调度队列或单用户并发达到上限，`details: {"retry_after_seconds": 10}` |
| `502` | `REMOTE_BRANCH_FETCH_FAILED` | 查询或校验 Git 远端分支失败（远端不可达、超时或鉴权异常，输出已脱敏） |
| `500` | `INTERNAL_ERROR` | 服务端内部未预期异常（严禁泄露明文 `APIKey` 或底层密码） |

---

## 2. 全量接口契约全景矩阵（54 Endpoints）

> **波次标注**：`W1` 为最小可用纵向闭环首期必须落地接口；`W2` 为 P0 全量补齐接口（`W1` Exit Gate 通过后交付）。

| # | 方法 | 路由路径 | 鉴权级别 | 波次 | 业务功能摘要 | 审计 (`action / target / level`) |
| :-: | :-: | :--- | :-: | :-: | :--- | :--- |
| **域 0：公共探针、认证与共享主数据** | | | | | | |
| 1 | `GET` | `/api/health` | `Public` | W1 | 服务存活与版本健康探针（`SC-01`） | - |
| 2 | `GET` | `/api/auth/config` | `Public` | W1 | 独立调试模式登录方式配置 | - |
| 3 | `POST` | `/api/login` | `Public` | W1 | 独立调试模式本地账密登录（与 `main.go` 保持一致） | `login / auth / P2` |
| 4 | `GET` | `/api/oauth2/authorize` | `Public` | W1 | 独立模式 OAuth2 授权跳转 | - |
| 5 | `GET` | `/api/oauth2/callback` | `Public` | W1 | 独立模式 OAuth2 回调签发 JWT | `login / auth / P2` |
| 6 | `GET` | `/api/me` | `JWT` | W1 | 获取当前用户信息及 `is_module_admin` 标识 | - |
| 7 | `PATCH` | `/api/password` | `JWT` | W1 | 独立模式修改本地密码 | `update / user_password / P1` |
| 8 | `GET` | `/api/users` | `JWT` | W1 | 只读检索共享 `users`（供成员选择器复用） | - |
| 9 | `GET` | `/api/departments` | `JWT` | W1 | 只读获取共享 `departments` 列表（筛选下拉用） | - |
| **域 1：仓库与 Monorepo 子目录覆盖视图（`F-01` / `F-36`）** | | | | | | |
| 10 | `GET` | `/api/repos` | `JWT` | W1 / M15 | 分页查询仓库及子目录文档覆盖与漂移矩阵（M15 新增 `arch_binding=unbound_repo` 仓库级未关联筛选） | - |
| 11 | `GET` | `/api/repos/:repo_id/scopes` | `JWT` | W1 | 查询单仓（整仓 + 全部 `ArchitectureElement` 子目录）详细范围视图 | - |
| 11a | `GET` | `/api/arch-tree` | `JWT` | W2 / M15 | 按平台 `architecture_elements` 聚合功能模块级架构树与三态绑定/统计指标（主视图数据源） | - |
| **域 2：可管理文档类型注册表（`F-03`）** | | | | | | |
| 12 | `GET` | `/api/doc-types` | `JWT` | W1 | 获取 `DocType` 列表（支持 `active_only`） | - |
| 13 | `GET` | `/api/doc-types/:slug` | `JWT` | W1 | 获取单个 `DocType` 元数据、槽位与当前修订资产内容 | - |
| 14 | `GET` | `/api/doc-types/:slug/revisions` | `JWT` | W1 | 查询 `DocType` 不可变修订历史列表 | - |
| 15 | `POST` | `/api/admin/doc-types` | `Admin` | W2 | 新建 `DocType`（原子生成目录资产并同步 DB） | `create / doc_type / P0` |
| 16 | `POST` | `/api/admin/doc-types/:slug/clone` | `Admin` | W2 | 克隆既有 `DocType` 为新 `slug` | `create / doc_type / P0` |
| 17 | `PUT` | `/api/admin/doc-types/:slug` | `Admin` | W2 | 在线更新元数据/模板/指令（原子写盘 + 生成新修订 + 触发 `Spec Drift`） | `update / doc_type / P0` |
| 18 | `PATCH` | `/api/admin/doc-types/:slug/status` | `Admin` | W2 | 启用或停用 `DocType` | `update / doc_type / P0` |
| 19 | `DELETE` | `/api/admin/doc-types/:slug` | `Admin` | W2 | 物理删除未被已发布文档引用的 `DocType`（被引用返 `409`） | `delete / doc_type / P0` |
| 20 | `POST` | `/api/admin/doc-types/sync` | `Admin` | W2 | 手动触发目录 → DB 全量幂等同步 | `sync / doc_type / P2` |
| **域 3：反推生成任务编排与监控（`F-02`/`F-04`/`F-05`/`F-27`）** | | | | | | |
| 21 | `POST` | `/api/tasks/generate` | `ScopeWrite` | W1 | 触发单仓/子目录 1~N 个 `DocType` 反推生成任务 (`202`) | `trigger / gen_task / P1` |
| 22 | `GET` | `/api/tasks` | `JWT` | W1 | 分页查询任务执行历史与队列状态 | - |
| 23 | `GET` | `/api/tasks/:task_id` | `JWT` | W1 | 查询主任务进度、预算熔断建议及全部槽位子任务状态 | - |
| 24 | `POST` | `/api/tasks/:task_id/cancel` | `ScopeWrite` | W1 | 取消排队或运行中任务（终止 LLM 并强杀 `git` 进程组） | `cancel / gen_task / P1` |
| 25 | `POST` | `/api/tasks/:task_id/slots/:slot_task_id/retry` | `ScopeWrite` | W1 | 对单个失败槽位发起独立重试（其余槽位字节不变） | `trigger / gen_task / P1` |
| **域 4：Canvas Studio 文档审校、手工直编、锁定与证据预览（`F-07`~`F-10`）** | | | | | | |
| 26 | `GET` | `/api/documents` | `JWT` | W1 | 查询指定范围 `(repo_id, subdirectory)` 下的文档导航树（普通用户过滤草稿） | - |
| 27 | `GET` | `/api/documents/:id` | `JWT` *(草稿需 `ScopeWrite`)* | W1 | 获取单篇文档完整章节、证据、Verifier 报告与漂移状态 | - |
| 28 | `GET` | `/api/documents/:id/revisions` | `ScopeWrite` | W1 | 获取文档不可变修订时间线列表 | - |
| 29 | `GET` | `/api/documents/:id/revisions/diff` | `ScopeWrite` | W1 | 对比任意两个修订版本的章节级 Diff 与校验差异 | - |
| 30 | `PUT` | `/api/documents/:id/sections/:slot_id` | `ScopeWrite` | W1 | 手工直编单个章节（乐观锁 + 重跑 Verifier + 生成不可变 Revision） | `update / document_section / P1` |
| 31 | `PATCH` | `/api/documents/:id/sections/:slot_id/ownership` | `ScopeWrite` | W1 | 切换章节所有权 (`auto`/`hybrid`/`locked`) 或清除待核对标记 | `update / document_section / P1` |
| 32 | `POST` | `/api/documents/:id/sections/:slot_id/orphan-resolve` | `ScopeWrite` | W1 | 处置模板升级产生的孤立章节（保留锁定/合并至指定槽位/确认删除） | `update / document_section / P1` |
| 33 | `POST` | `/api/documents/:id/rollback` | `ScopeWrite` | W1 | 一键回滚至历史修订（以 `source=rollback` 追加新不可变修订） | `rollback / document / P1` |
| 34 | `GET` | `/api/documents/:id/source-preview` | `JWT` *(草稿需 `ScopeWrite`)* | W1 | 只读源码证据抽屉（校验 `SafeJoin` `403`、按 `commit_sha` 读取并高亮行区间） | - |
| **域 5：SSE Chat 局部协同精修（`F-09` W2）** | | | | | | |
| 35 | `GET` | `/api/documents/:id/chat/sessions` | `ScopeWrite` | W2 | 获取文档精修会话列表与消息/提案历史 | - |
| 36 | `POST` | `/api/documents/:id/chat/refine` | `ScopeWrite` | W2 | 发起章节/整篇 SSE 流式精修（推送 `diff_proposal` 与 `verifier_preview`） | - |
| 37 | `POST` | `/api/documents/:id/chat/messages/:msg_id/accept` | `ScopeWrite` | W2 | 采纳 Chat Diff 提案（乐观锁 + 单槽位更新 + Verifier + 指标记录） | `update / document_section / P1` |
| 38 | `POST` | `/api/documents/:id/chat/messages/:msg_id/reject` | `ScopeWrite` | W2 | 拒绝 Chat Diff 提案（标记 `rejected` 并记录指标，文档零变更） | - |
| **域 6：定稿发布、撤回与导出（`F-11`）** | | | | | | |
| 39 | `POST` | `/api/documents/:id/publish` | `ScopeWrite` | W1 | 审批定稿并原子落盘 `publish_dir`（Verifier 阻断返 `422`） | `publish / document / P0` |
| 40 | `POST` | `/api/documents/:id/unpublish` | `ScopeWrite` | W1 | 一键撤回已发布文档（删除落盘文件、重建/清理目录与索引） | `unpublish / document / P0` |
| 41 | `GET` | `/api/documents/:id/export` | `JWT` *(草稿需 `ScopeWrite`)* | W1 | 流式下载单篇带 Frontmatter 的 Markdown 文件 | - |
| 42 | `POST` | `/api/exports/zip` | `JWT` | W1 | 批量流式打包下载多篇已发布文档 ZIP（含 `INDEX.md`，防 Zip-Slip） | - |
| **域 7：远端变更巡检与章节级双维漂移雷达（`F-12`/`F-25` W2）** | | | | | | |
| 43 | `GET` | `/api/drift/summary` | `JWT` | W2 | 全局漂移概览与远端巡检健康汇总 | - |
| 44 | `GET` | `/api/drift/documents` | `JWT` | W2 | 分页查询漂移文档及受影响 `slot_id` 变更明细 | - |
| 45 | `POST` | `/api/documents/:id/incremental-refresh` | `ScopeWrite` | W2 | 对单篇漂移文档触发章节级增量刷新（仅刷新受影响非锁槽位，`202`） | `trigger / gen_task / P1` |
| 46 | `POST` | `/api/admin/drift/poll` | `Admin` | W2 | 手动触发一轮全量活跃已发布仓库的远端 `ls-remote` 巡检与漂移重算 | `trigger / drift_poll / P2` |
| **域 8：生成质量度量与修订纵向对比（`F-26` W2）** | | | | | | |
| 47 | `GET` | `/api/metrics/overview` | `JWT` | W2 | 按时间窗口/`DocType`/`DocTypeRevision` 聚合质量与成本 KPI | - |
| 48 | `GET` | `/api/metrics/revisions-compare` | `JWT` | W2 | 同一 `DocType` 不同 `DocTypeRevision` 的质量与成本演进对比 | - |
| 49 | `GET` | `/api/metrics/slot-heatmap` | `JWT` | W2 | 各 `slot_id` 历史平均草稿→定稿修改率与 Verifier 告警率热力表 | - |
| **域 9：管理员 LLM 配置与运行存储运维（`F-14` W1 / `F-27` W2）** | | | | | | |
| 50 | `GET` | `/api/admin/llm-configs` | `Admin` | W1 | 获取内网平等 LLM 模型池列表（`api_key` 强制脱敏） | - |
| 51 | `POST` | `/api/admin/llm-configs` | `Admin` | W1 | 新增内网 OpenAI Compatible 模型配置 | `create / llm_config / P0` |
| 52 | `PUT` | `/api/admin/llm-configs/:id` | `Admin` | W1 | 更新指定模型配置与单任务预算上限 | `update / llm_config / P0` |
| 53 | `DELETE` | `/api/admin/llm-configs/:id` | `Admin` | W1 | 删除模型配置（默认模型与被 DocType 指定模型受保护） | `delete / llm_config / P0` |
| 54 | `POST` | `/api/admin/llm-configs/:id/default` | `Admin` | W1 | 手动将指定已启用模型切换为全局唯一默认模型 | `update / llm_config / P0` |
| 55 | `POST` | `/api/admin/llm-configs/probe` & `/:id/probe` | `Admin` | W1 | 执行预检或已保存模型连通性探活（`max_tokens=1`, 15s） | - |
| 56 | `GET` | `/api/admin/storage/stats` | `Admin` | W2 | 查询磁盘配额水位、活跃租约与双优先级调度队列态势 | - |
| 57 | `POST` | `/api/admin/storage/reap` | `Admin` | W2 | 手动触发一次受租约保护的 `sandbox_dir`/`ir_cache` LRU 空间回收 | `trigger / storage_reap / P2` |

> *注：表内共列出 54 条独立路由（含 OAuth2 授权与回调拆分为 2 条）。脚手架占位路由 `/api/items*` 与 `/api/admin/settings` 将在 M1 实施时彻底清除。*

---

## 3. 分域接口详细 Schema 契约

### 3.1 域 0：公共探针、认证与共享主数据

#### `GET /api/health` (`Public`, W1)
- **响应 (`200 OK`)**：
```json
{
  "status": "ok",
  "service": "Code-Archdoc",
  "module": "archdoc",
  "version": "1.0.0"
}
```

#### `GET /api/me` (`JWT`, W1)
- **响应 (`200 OK`)**：
```json
{
  "id": 1001,
  "username": "zhangsan",
  "name": "张三",
  "email": "zhangsan@example.com",
  "department_id": 10,
  "department_name": "核心交易平台部",
  "roles": ["user", "archdoc_admin"],
  "is_module_admin": true
}
```

#### `GET /api/departments` (`JWT`, W1)
- **响应 (`200 OK`)**：
```json
{
  "items": [
    { "id": 10, "name": "核心交易平台部", "leader_id": 1001 }
  ]
}
```

---

### 3.2 域 1：仓库与 Monorepo 子目录覆盖视图（`F-01`, W1）

#### `GET /api/repos` (`JWT`, W1 / M15 扩展)
- **Query 参数**：
  - `page` (int, 默认 `1`)、`page_size` (int, `15|25|50|100`, 默认 `25`)
  - `search` (string, 模糊匹配仓库名、子目录路径、架构元素名、层级路径或子系统名)
  - `department_id` (uint, 可选)
  - `subsystem_id` (uint, 可选，按归属架构子系统 ID 筛选)
  - `arch_binding` (string, 可选：`all` | `bound` | `unbound` | `unbound_repo`，非法值返回 `400 INVALID_PARAM`；其中 `unbound_repo` 为 M15 新增的仓库级未关联 MECE 筛选，详规见 §9.2)
  - `doc_type` (string, 可选 `DocType.slug`，仅在搭配 `status` 筛选时限定该类型的状态)
  - `status` (string, 可选：`all` | `approved` | `draft` | `drifted` | `missing`)
  - `scope_type` (string, 可选：`all` | `repo` | `subdir`，默认 `all`)
- **响应 (`200 OK`)**：
```json
{
  "items": [
    {
      "repo_id": 101,
      "repo_name": "order-service",
      "department_id": 10,
      "department_name": "核心交易平台部",
      "subdirectory": "svc/order",
      "scope_type": "subdir",
      "arch_element_id": 502,
      "arch_element_name": "订单核心服务",
      "arch_element_type": "module",
      "arch_path": "核心平台 / 交易域组 / 订单核心服务",
      "subsystem_id": 500,
      "subsystem_name": "核心平台",
      "branch": "master",
      "owner_id": 2001,
      "owner_name": "李四",
      "can_write": true,
      "latest_commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
      "remote_head_sha": "f9e8d7c6b5a4321098765432109876543210fedc",
      "active_task_id": null,
      "doc_statuses": {
        "system-architecture": {
          "document_id": 42,
          "status": "approved",
          "current_revision_no": 5,
          "published_revision_no": 5,
          "has_code_drift": true,
          "has_spec_drift": false,
          "affected_slot_count": 1,
          "verifier_passed": true,
          "can_approve": true,
          "updated_at": "2026-11-19T14:20:00+08:00"
        },
        "data-and-contract": {
          "document_id": 43,
          "status": "draft",
          "current_revision_no": 1,
          "published_revision_no": 0,
          "has_code_drift": false,
          "has_spec_drift": false,
          "affected_slot_count": 0,
          "verifier_passed": false,
          "can_approve": false,
          "updated_at": "2026-11-19T15:00:00+08:00"
        }
      }
    }
  ],
  "total": 128,
  "page": 1,
  "page_size": 25,
  "summary": {
    "total_scopes": 128,
    "covered_scopes": 84,
    "approved_docs": 210,
    "drifted_docs": 19
  },
  "subsystems": [
    {
      "id": 500,
      "identifier": "core-platform",
      "name_cn": "核心平台",
      "name_en": "Core Platform",
      "scope_count": 18
    }
  ]
}
```
> **草稿可见性规则**：当当前用户对某行 `can_write == false` 时，`doc_statuses` 中仅包含 `status == "approved"` 的文档条目，未发布草稿对该用户显示为缺失（不暴露 `document_id` 与草稿状态）。

#### `GET /api/arch-tree` (`JWT`, W2 / M15 冻结契约见 §9.1)
- **Query 参数**：`search`、`department_id`、`subsystem_id`、`arch_binding`（仅允许 `all | bound | unbound`，传 `unbound_repo` 等非法值返 `400 INVALID_PARAM`）、`doc_type`、`status`、`scope_type`（不分页）。
- **响应 (`200 OK`)**：自 `ADR-09` (`M15`) 起升级为模块级三态树表契约，返回 `{ "summary": ArchTreeSummaryVO, "subsystems": ArchSubsystemOptionVO[], "nodes": ArchTreeNodeVO[], "unassigned_scopes": [] }`：非功能模块（`subsystem`/`group`）`scopes` 恒为 `[]`、`scope` 为 `null`；仅功能模块（`module`）承载唯一 `scope` 与 `binding_status`（`"bound" | "unbound" | "repo_inactive"`），严格遵循「先筛选剪枝、后向上聚合」及 `SC-12` 草稿隔离，详细契约与 JSON 示例见 **§9.1**。

#### `GET /api/repos/:repo_id/scopes` (`JWT`, W1)
- **响应 (`200 OK`)**：返回仓库基础元数据与 `scopes: RepoScopeCoverageVO[]`（首条为整仓 `subdirectory: ""`，其余为关联的 `ArchitectureElement` 子目录），并附带该仓库最近 5 条活跃或历史 `GenTask` 摘要；同时通过 `branch`、`default_branch`、`is_branch_overridden`、`can_switch_branch` 暴露有效跟踪分支状态。

---

### 3.3 域 2：可管理文档类型注册表（`F-03`）

#### `GET /api/doc-types` (`JWT`, W1)
- **Query**：`active_only` (`bool`, 默认 `false`)
- **响应 (`200 OK`)**：
```json
{
  "items": [
    {
      "id": 1,
      "slug": "system-architecture",
      "display_name": "系统架构与核心链路",
      "short_name": "系统架构",
      "category": "架构设计",
      "description": "面向架构师与研发骨干的运行时分层、并发、容错与核心链路文档",
      "is_builtin": true,
      "is_active": true,
      "preferred_model": "",
      "timeout_seconds": 300,
      "ir_facets": ["tree", "deps", "build", "runtime_primitives", "symbols", "idl_contracts", "http_routes", "config_keys"],
      "slot_count": 5,
      "latest_revision_id": 3,
      "latest_revision_no": 3,
      "latest_aggregate_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "published_doc_count": 37,
      "updated_at": "2026-11-19T12:00:00+08:00"
    }
  ]
}
```

#### `GET /api/doc-types/:slug` (`JWT`, W1)
- **响应 (`200 OK`)**：在列表字段基础上返回 `meta`（完整 `meta.json` 对象，保留未知扩展字段）、`slots`（完整 `SlotDefinition[]`，含 `enable_source_tools` 与 `max_tool_rounds`）、`template_md`、`generation_prompt_md`、`refine_prompt_md`、`latest_revision`。

#### `GET /api/doc-types/:slug/revisions` (`JWT`, W1)
- **响应 (`200 OK`)**：返回 `{ "items": DocTypeRevisionSummaryVO[] }`，按 `revision_no DESC` 排序，包含 `id`, `revision_no`, `aggregate_hash`, `prompt_hash`, `slots_snapshot`（含 `enable_source_tools` 与 `max_tool_rounds`）, `created_by`, `created_at`。

#### `POST /api/admin/doc-types` (`Admin`, W2 / M11 ADR-05 扩展)
- **请求体**：
```json
{
  "slug": "security-review",
  "display_name": "安全架构评审报告",
  "short_name": "安全评审",
  "category": "质量与安全",
  "description": "面向研发安全评审的入口面、鉴权与敏感数据流分析",
  "is_active": true,
  "preferred_model": "DeepSeek-V3",
  "timeout_seconds": 300,
  "max_output_tokens": 16000,
  "ir_facets": ["tree", "http_routes", "idl_contracts", "config_keys", "data_models"],
  "repo_ir_facets": ["tree", "http_routes", "idl_contracts", "config_keys"],
  "context_inputs": ["module_context", "module_digests"],
  "module_digest_source": {
    "doc_type_slug": "security-review",
    "slot_id": "attack_surface",
    "max_chars": 600
  },
  "scope_profile": {
    "max_depth": 12,
    "include_tests": false,
    "include_legacy_docs": false
  },
  "verify_policy": {
    "strict_symbol_check": false,
    "require_mermaid": true,
    "max_mermaid_nodes": 35,
    "max_mermaid_edges": 60,
    "block_on_secret_leak": true
  },
  "slots": [
    {
      "slot_id": "attack_surface",
      "title": "1. 外部入口与攻击面清单",
      "ownership": "auto",
      "required": true,
      "ir_facets": ["http_routes", "idl_contracts"],
      "token_budget": 8000,
      "min_chars": 40,
      "enable_source_tools": true,
      "max_tool_rounds": 4,
      "instruction_hint": "梳理全部外部 HTTP/RPC 入口及其鉴权中间件。"
    }
  ],
  "template_md": "# 安全架构评审报告\n\n## 1. 外部入口与攻击面清单\n<!-- slot:attack_surface -->\n",
  "generation_prompt_md": "你是资深应用安全架构师...",
  "refine_prompt_md": ""
}
```
  - 可选高级治理字段：`max_output_tokens`、`repo_ir_facets`（9 大规范分片白名单校验）、`context_inputs`（允许 `"code_ir"` | `"module_context"` | `"module_digests"`）、`module_digest_source`、`scope_profile`、`verify_policy`；槽位可选 `enable_source_tools`（bool）与 `max_tool_rounds`（`0~6`）。
- **状态码**：`201 Created`（返回 `DocTypeDetailVO`）；非法 `slug` → `400 INVALID_SLUG`；非法分片/枚举/缺槽位标记 → `400 INVALID_PARAM`（且保证磁盘无残留目录）；重名 → `409 CONFLICT`；非管理员 → `403 FORBIDDEN`。

#### `POST /api/admin/doc-types/:slug/clone` (`Admin`, W2)
- **请求体**：`{ "new_slug": "security-review-v2", "display_name": "安全架构评审报告 V2" }`
- **状态码**：`201 Created`；源不存在 → `404`；`new_slug` 非法 → `400 INVALID_SLUG`；重名 → `409 CONFLICT`。

#### `PUT /api/admin/doc-types/:slug` (`Admin`, W2)
- **请求体**：`{ "meta": {...}, "template_md": "...", "generation_prompt_md": "...", "refine_prompt_md": "..." }`（其中 `meta.name` 必须等于路径 `:slug`，未知扩展字段由服务端合并保留）。
- **校验规则**：校验 `meta.slots[]` 中每个 `slot_id` 均匹配 `^[a-z][a-z0-9_]{1,48}$` 且无重复、并在 `template_md` 中存在对应的 `<!-- slot:<slot_id> -->` 标记。
- **状态码**：`200 OK`（返回更新后 `DocTypeDetailVO` 及 `revision_created: bool`）。

#### `PATCH /api/admin/doc-types/:slug/status` (`Admin`, W2)
- **请求体**：`{ "is_active": false }`
- **状态码**：`200 OK`（同时将 `is_active` 原子写回 `meta.json` 并同步 DB）。

#### `DELETE /api/admin/doc-types/:slug` (`Admin`, W2)
- **状态码**：
  - `200 OK`：`{ "deleted": true, "slug": "security-review" }`（目录与 DB 同步移除）；
  - `409 DOC_TYPE_IN_USE`：当存在 ≥ 1 篇 `status == "approved"` 文档引用该 `slug` 时硬拦截（`SC-08`）。

#### `POST /api/admin/doc-types/sync` (`Admin`, W2)
- **响应 (`200 OK`)**：`{ "synced_count": 5, "new_revisions": 1, "slugs": ["system-architecture", ...] }`。

---

### 3.4 域 3：反推生成任务编排与监控（`F-02`/`F-04`/`F-05`/`F-27`）

#### `POST /api/tasks/generate` (`ScopeWrite`, W1)
- **请求体**：
```json
{
  "repo_id": 101,
  "subdirectory": "svc/order",
  "ref_name": "",
  "doc_types": ["system-architecture", "data-and-contract"],
  "mode": "generate"
}
```
  - `subdirectory`：可为空字符串（代表整仓），经 `sandbox.NormalizeSubdir` 规范化，含 `..` 等非法路径返回 `400 INVALID_SUBDIR`（`SC-06`）。
  - `ref_name`：P0 留空自动取 `Repository.Branch`（默认 `master`），P1 开放自定义分支/Tag。
  - `doc_types`：必填，非空数组，所有元素须为启用中的 `DocType.slug`。
  - `mode`：`generate`（默认）| `regenerate`。
- **响应 (`202 Accepted`)**：
```json
{
  "id": 901,
  "repo_id": 101,
  "subdirectory": "svc/order",
  "ref_name": "master",
  "task_kind": "generate",
  "priority": "interactive",
  "doc_types": ["system-architecture", "data-and-contract"],
  "status": "pending",
  "progress_percent": 0,
  "estimated_tokens": 0,
  "triggered_by": 2001,
  "created_at": "2026-11-19T15:10:00+08:00"
}
```
- **边界错误**：`400 INVALID_SUBDIR`、`400 DOC_TYPE_INACTIVE`、`403 FORBIDDEN`（含 `SC-12`）、`404 NOT_FOUND`（仓库不存在）、`429 QUEUE_FULL`。

#### `GET /api/tasks` (`JWT`, W1)
- **Query**：`page`, `page_size`, `repo_id`, `subdirectory`, `status`, `triggered_by`
- **响应 (`200 OK`)**：标准分页 `{ items: GenTaskSummaryVO[], total, page, page_size }`。

#### `GET /api/tasks/:task_id` (`JWT`, W1)
- **响应 (`200 OK`)**：
```json
{
  "id": 901,
  "repo_id": 101,
  "repo_name": "order-service",
  "subdirectory": "svc/order",
  "ref_name": "master",
  "snapshot_id": 312,
  "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "task_kind": "generate",
  "priority": "interactive",
  "doc_types": ["system-architecture"],
  "status": "failed",
  "progress_percent": 20,
  "estimated_tokens": 385000,
  "error_code": "TOKEN_BUDGET_EXCEEDED",
  "error_message": "预估消耗 385000 Tokens 超出单任务上限 200000 Tokens，已熔断且未发出 LLM 请求",
  "budget_advice": {
    "estimated_tokens": 385000,
    "max_tokens_per_task": 200000,
    "effective_files": 1240,
    "max_files_per_task": 2000,
    "top_directories": [
      { "path": "src/legacy_engine", "estimated_tokens": 182000, "file_count": 410 }
    ],
    "top_doc_types": [
      { "slug": "system-architecture", "estimated_tokens": 210000 }
    ],
    "suggestions": [
      "建议按 Monorepo 子目录拆分分析范围（如排除 src/legacy_engine）",
      "建议在仓库根目录补充 .archdocignore 过滤测试夹具或生成代码"
    ]
  },
  "slot_tasks": [
    {
      "id": 5001,
      "doc_type_slug": "system-architecture",
      "doc_type_revision_id": 3,
      "slot_id": "overview_boundaries",
      "document_id": 42,
      "status": "completed",
      "attempt_count": 1,
      "model_name": "DeepSeek-V3",
      "error_message": "",
      "started_at": "2026-11-19T15:10:05+08:00",
      "finished_at": "2026-11-19T15:10:18+08:00"
    }
  ],
  "triggered_by": 2001,
  "started_at": "2026-11-19T15:10:02+08:00",
  "finished_at": "2026-11-19T15:10:04+08:00"
}
```

#### `POST /api/tasks/:task_id/cancel` (`ScopeWrite`, W1)
- **响应 (`200 OK`)**：`{ "id": 901, "status": "canceled" }`；若任务已处于终态（`ready`/`failed`/`canceled`）返回 `409 CONFLICT`。

#### `POST /api/tasks/:task_id/slots/:slot_task_id/retry` (`ScopeWrite`, W1)
- **前提**：仅允许对 `status == "failed"` 的槽位子任务发起重试。
- **响应 (`202 Accepted`)**：返回更新后的 `GenSlotTaskVO`（`status: "pending"`, `attempt_count: prev + 1`）。重试成功后仅替换目标槽位内容、保持其余已完成槽位字节级不变并重跑 Verifier。

---

### 3.5 域 4：Canvas Studio 文档审校、手工直编、锁定与证据预览（`F-07`~`F-10`）

#### `GET /api/documents` (`JWT`, W1)
- **Query**：`repo_id` (必填, uint)、`subdirectory` (可选, 默认 `""`, 经 `NormalizeSubdir` 校验)、`ref_name` (可选)
- **响应 (`200 OK`)**：返回 `{ "can_write": true, "items": DocumentNavItemVO[] }`（普通用户仅返回 `status == "approved"` 条目）。

#### `GET /api/documents/:id` (`JWT` / 草稿需 `ScopeWrite`, W1)
- **Query**：`revision_no` (可选 int；普通用户忽略该参数，强制返回 `published_revision_id` 对应版本；`ScopeWrite` 用户不传时默认返回最新 `current_revision_id` 对应版本)。
- **响应 (`200 OK`)**：
```json
{
  "id": 42,
  "repo_id": 101,
  "repo_name": "order-service",
  "subdirectory": "svc/order",
  "ref_name": "master",
  "doc_type_slug": "system-architecture",
  "doc_type_name": "系统架构与核心链路",
  "doc_type_revision_id": 3,
  "doc_type_revision_no": 3,
  "title": "order-service (svc/order) 系统架构与核心链路",
  "status": "approved",
  "can_write": true,
  "current_revision_no": 5,
  "published_revision_no": 5,
  "published_commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "published_at": "2026-11-19T14:20:00+08:00",
  "viewing_revision": {
    "id": 150,
    "revision_no": 5,
    "snapshot_id": 312,
    "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
    "source": "manual_edit",
    "change_summary": "补充订单超时重试机制说明",
    "verifier_passed": true,
    "can_approve": true,
    "slot_coverage_rate": 1.0,
    "verifier_report": {
      "passed": true,
      "can_approve": true,
      "blocker_count": 0,
      "error_count": 0,
      "warning_count": 1,
      "slot_coverage_rate": 1.0,
      "missing_required_slots": [],
      "unverified_refs": [],
      "mermaid_issues": [],
      "secret_findings": []
    },
    "created_by": 2001,
    "created_at": "2026-11-19T14:15:00+08:00"
  },
  "sections": [
    {
      "slot_id": "overview_boundaries",
      "section_order": 1,
      "title": "1. 系统定位与模块分层边界",
      "ownership": "auto",
      "is_orphan": false,
      "manually_edited": false,
      "needs_drift_review": false,
      "content_sha256": "9c1185a5c5e9fc54612808977ee8f548b2258d31...",
      "markdown": "### 模块边界\n...",
      "evidences": [
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
      ]
    }
  ],
  "drift": {
    "code_drift": null,
    "spec_drift": null
  }
}
```

#### `GET /api/documents/:id/revisions` (`ScopeWrite`, W1)
- **响应 (`200 OK`)**：返回 `{ "items": DocumentRevisionSummaryVO[] }`（按 `revision_no DESC` 排序，不含 `full_markdown` 大正文）。普通用户访问返回 `403 DRAFT_ACCESS_DENIED`。

#### `GET /api/documents/:id/revisions/diff` (`ScopeWrite`, W1)
- **Query**：`from_rev` (int, 必填)、`to_rev` (int, 必填)
- **响应 (`200 OK`)**：
```json
{
  "document_id": 42,
  "from_rev": 4,
  "to_rev": 5,
  "section_diffs": [
    {
      "slot_id": "core_request_flows",
      "title": "3. 端到端核心业务链路与时序图",
      "changed": true,
      "unified_diff": "@@ -10,3 +10,9 @@\n...",
      "added_lines": 6,
      "removed_lines": 0
    }
  ]
}
```

#### `PUT /api/documents/:id/sections/:slot_id` (`ScopeWrite`, W1)
- **请求体**：
```json
{
  "base_revision_no": 5,
  "markdown": "### 端到端核心链路\n...",
  "ownership": "hybrid",
  "change_summary": "手工补充降级熔断分支说明"
}
```
  - `ownership` 可选（不传则保持当前所有权不变）。
- **状态码**：
  - `200 OK`：返回新生成的 `DocumentDetailVO`（`current_revision_no = 6`, `source = "manual_edit"`），其余非目标槽位字节级不变；
  - `409 OPTIMISTIC_LOCK_CONFLICT`：`base_revision_no != current_revision_no`；
  - `404 NOT_FOUND`：文档或 `slot_id` 不存在。

#### `PATCH /api/documents/:id/sections/:slot_id/ownership` (`ScopeWrite`, W1)
- **请求体**：
```json
{
  "ownership": "locked",
  "clear_drift_review": true
}
```
- **校验**：`ownership` 必须为 `auto | hybrid | locked` 之一。
- **响应 (`200 OK`)**：返回更新后的 `SectionVO`。

#### `POST /api/documents/:id/sections/:slot_id/orphan-resolve` (`ScopeWrite`, W1)
- **请求体**：
```json
{
  "base_revision_no": 6,
  "action": "merge_into",
  "target_slot_id": "adr_constraints_debt"
}
```
  - `action` 枚举：`keep_locked`（转为常规锁定章节、`is_orphan = false`）| `merge_into`（将内容追加至 `target_slot_id` 并移除孤立节）| `delete`（负责人显式确认删除该孤立章节）。
- **状态码**：`200 OK`（生成新不可变修订并返回 `DocumentDetailVO`）；目标章节非孤立状态返回 `400 INVALID_PARAM`；版本冲突返回 `409 OPTIMISTIC_LOCK_CONFLICT`。

#### `POST /api/documents/:id/rollback` (`ScopeWrite`, W1)
- **请求体**：`{ "base_revision_no": 6, "target_revision_no": 4, "change_summary": "回退误改章节" }`
- **状态码**：`200 OK`（以 `revision_no = 7`, `source = "rollback"`, `source_ref_id = target_rev.id` 落库并重跑 Verifier）；版本冲突返回 `409 OPTIMISTIC_LOCK_CONFLICT`。

#### `GET /api/documents/:id/source-preview` (`JWT` / 草稿需 `ScopeWrite`, W1)
- **Query**：
  - `commit` (string, 可选，默认使用所查看文档修订的 `commit_sha`；必须匹配 `^[0-9a-f]{7,40}$`)
  - `file` (string, 必填，沙箱相对路径)
  - `line_start` (int, 可选)、`line_end` (int, 可选)
  - `symbol` (string, 可选，用于跨提交重定位行号)
- **安全与响应**：
  - 若 `file` 包含 `..`、绝对路径或经 `SafeJoin` + `EvalSymlinks` 指向沙箱外，强制返回 **`403 PATH_ESCAPE`**（`SC-06`）；
  - 响应 (`200 OK`)：
```json
{
  "repo_id": 101,
  "subdirectory": "svc/order",
  "commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "file_path": "src/order/order_service.cpp",
  "language": "cpp",
  "total_lines": 320,
  "highlight_start": 142,
  "highlight_end": 188,
  "relocated_by_symbol": false,
  "content": "// 源码文本内容（超过 512KB 自动按高亮行上下文窗口截取）..."
}
```

---

### 3.6 域 5：SSE Chat 局部协同精修（`F-09`, W2）

#### `GET /api/documents/:id/chat/sessions` (`ScopeWrite`, W2)
- **响应 (`200 OK`)**：返回 `{ "items": ChatSessionWithMessagesVO[] }`，按 `updated_at DESC` 排序。

#### `POST /api/documents/:id/chat/refine` (`ScopeWrite`, W2)
- **请求头**：`Accept: text/event-stream`、`Content-Type: application/json`
- **请求体**：
```json
{
  "session_id": 12,
  "target_slot_id": "core_request_flows",
  "base_revision_no": 5,
  "prompt": "请补充调用支付下游失败后的超时重试时序图与退避参数说明"
}
```
  - 若目标槽位为 `locked`，返回 `409 CONFLICT`（提示需先解锁或改用手工直编）。
- **响应头**：`Content-Type: text/event-stream; charset=utf-8`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`。
- **SSE 事件流契约（严格对齐 `04-workbench-and-security.md` §3.2）**：
```text
event: meta
data: {"session_id":12,"message_id":88,"slot_id":"core_request_flows","base_revision_no":5,"model_name":"DeepSeek-V3"}

event: delta
data: {"text":"已结合 src/order/order_service.cpp 中的重试逻辑生成更新方案..."}

event: diff_proposal
data: {"proposal_id":88,"slot_id":"core_request_flows","before_md":"...","after_md":"...","unified_diff":"@@ -12,4 +12,18 @@...","cited_anchors":[...]}

event: verifier_preview
data: {"can_approve":true,"blocker_count":0,"error_count":0,"warning_count":0,"issues":[]}

event: done
data: {"message_id":88,"prompt_tokens":3200,"cached_prompt_tokens":1900,"completion_tokens":680,"latency_ms":4120}
```
  - 异常时发送 `event: error` + `{"code": "LLM_TIMEOUT", "message": "..."}` 后立即关闭流，文档状态零变更。

#### `POST /api/documents/:id/chat/messages/:msg_id/accept` (`ScopeWrite`, W2)
- **请求体**：`{ "base_revision_no": 5 }`
- **状态码**：
  - `200 OK`：返回更新后的 `DocumentDetailVO`（`source = "chat_refine"`, `source_ref_id = msg_id`），标记消息 `proposal_status = "accepted"`，并将该会话中基于旧版本的其他 `pending` 提案标为 `superseded`，写入 `QualityMetric(chat_proposal_resolved, chat_accepted=true)`；
  - `409 OPTIMISTIC_LOCK_CONFLICT`：文档版本已变更。

#### `POST /api/documents/:id/chat/messages/:msg_id/reject` (`ScopeWrite`, W2)
- **响应 (`200 OK`)**：`{ "message_id": 88, "proposal_status": "rejected" }`，写入 `QualityMetric(chat_proposal_resolved, chat_accepted=false)`，文档零变更。

---

### 3.7 域 6：定稿发布、撤回与导出（`F-11`, W1）

#### `POST /api/documents/:id/publish` (`ScopeWrite`, W1)
- **请求体**：`{ "revision_no": 5 }`（可选，省略时发布 `current_revision_no`）。
- **发布前置校验与落盘**：
  1. 校验目标修订 `can_approve == true`（`blocker_count == 0`），否则返回 **`422 VERIFIER_BLOCKED`**（`SC-04`）；
  2. 注入 YAML Frontmatter（`D-02`）、证据锚点转 `/archdoc/docs/:id/evidence?...` 深链（`D-03`）、降级未发布跨文档相对链接；
  3. 通过 `<publish_dir>/.staging/tmp-<uuid>.md` + `fsync` + 原子 `rename` 落盘，重建 `README.md`；
  4. DB 事务置 `status = "approved"`、回写 `published_revision_id`/`published_commit_sha`/`published_rel_path`/`published_at`/`published_by`，首次发布或重新发布时计算草稿→定稿章节级 `edit_diff_ratio` 写入 `archdoc_quality_metrics`。
- **响应 (`200 OK`)**：
```json
{
  "document_id": 42,
  "status": "approved",
  "published_revision_no": 5,
  "published_commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "published_rel_path": "order-service/sub/svc/order/system-architecture.md",
  "published_at": "2026-11-19T15:30:00+08:00",
  "broken_links_downgraded": 0
}
```

#### `POST /api/documents/:id/unpublish` (`ScopeWrite`, W1)
- **响应 (`200 OK`)**：
```json
{
  "document_id": 42,
  "status": "draft",
  "removed_rel_path": "order-service/sub/svc/order/system-architecture.md"
}
```
  - 撤回后 `publish_dir` 中目标文档与空目录均被清除，`/archdoc` 发布视图与 `code-bench` 文档中心均不再可见（`SC-10`）。

#### `GET /api/documents/:id/export` (`JWT` / 草稿需 `ScopeWrite`, W1)
- **响应 (`200 OK`)**：
  - Header：`Content-Type: text/markdown; charset=utf-8`、`Content-Disposition: attachment; filename="<repo_slug>-<doc_type_slug>.md"`
  - Body：包含完整 YAML Frontmatter 与平台深链的 Markdown 文档。

#### `POST /api/exports/zip` (`JWT`, W1)
- **请求体**：
```json
{
  "repo_ids": [101],
  "document_ids": [42, 43],
  "include_index": true
}
```
- **规则**：`repo_ids` 与 `document_ids` 至少提供一个；普通用户仅允许打包 `status == "approved"` 文档（若结果集为空返回 `404 NOT_FOUND`）；ZIP 条目经 `SanitizeZipEntryPath` 严格过滤防 Zip-Slip。
- **响应 (`200 OK`)**：`Content-Type: application/zip`、`Content-Disposition: attachment; filename="archdoc-export-20261119.zip"`。

---

### 3.8 域 7：远端巡检与章节级双维漂移雷达（`F-12`/`F-25`, W2）

#### `GET /api/drift/summary` (`JWT`, W2)
- **响应 (`200 OK`)**：
```json
{
  "total_drifted_docs": 19,
  "code_drift_docs": 14,
  "spec_drift_docs": 8,
  "both_drift_docs": 3,
  "monitored_repos": 42,
  "unhealthy_repos": 1,
  "last_poll_at": "2026-11-19T15:30:00+08:00",
  "poll_interval_minutes": 15
}
```

#### `GET /api/drift/documents` (`JWT`, W2)
- **Query**：`page`, `page_size` (默认 `25`), `search`, `department_id`, `drift_type` (`all`|`code`|`spec`|`both`), `doc_type`
- **响应 (`200 OK`)**：标准分页，单条 `DriftDocumentVO` 示例：
```json
{
  "document_id": 42,
  "repo_id": 101,
  "repo_name": "order-service",
  "subdirectory": "svc/order",
  "doc_type_slug": "system-architecture",
  "doc_type_name": "系统架构与核心链路",
  "can_write": true,
  "published_commit_sha": "a1b2c3d4e5f6789012345678901234567890abcd",
  "remote_head_sha": "f9e8d7c6b5a4321098765432109876543210fedc",
  "bound_doc_type_revision_no": 2,
  "latest_doc_type_revision_no": 3,
  "code_drift": {
    "has_drift": true,
    "precision": "section_level",
    "affected_slot_count": 1,
    "affected_slots": [
      {
        "slot_id": "core_request_flows",
        "slot_title": "3. 端到端核心业务链路与时序图",
        "ownership": "hybrid",
        "reason": "symbol_modified",
        "change_kind": "modified",
        "changed_symbols": ["order::OrderService::Pay"]
      }
    ],
    "detected_at": "2026-11-19T15:30:05+08:00"
  },
  "spec_drift": {
    "has_drift": true,
    "precision": "section_level",
    "affected_slot_count": 1,
    "affected_slots": [
      {
        "slot_id": "fault_tolerance_deps",
        "slot_title": "4. 外部依赖拓扑、容错机制与故障影响面矩阵",
        "ownership": "auto",
        "reason": "slot_modified",
        "change_kind": "modified",
        "changed_symbols": []
      }
    ],
    "detected_at": "2026-11-19T15:12:00+08:00"
  }
}
```

#### `POST /api/documents/:id/incremental-refresh` (`ScopeWrite`, W2)
- **请求体**：`{ "slot_ids": [] }`（可选：留空时自动取该文档 `Code Drift ∪ Spec Drift` 的所有非 `locked` 受影响槽位；若显式传入则仅刷新传入的非 `locked` 槽位）。
- **响应 (`202 Accepted`)**：返回新建的 `GenTaskDetailVO`（`task_kind: "incremental_refresh"`, `target_document_id: id`, `target_slots: [...]`）。若无需要刷新的非锁槽位返回 `400 INVALID_PARAM`。

#### `POST /api/admin/drift/poll` (`Admin`, W2)
- **响应 (`202 Accepted`)**：`{ "triggered": true, "monitored_repos": 42, "message": "已启动新一轮远端变更巡检与双维漂移重算" }`。

---

### 3.9 域 8：生成质量度量与修订纵向对比（`F-26`, W2）

#### `GET /api/metrics/overview` (`JWT`, W2)
- **Query**：`days` (int, 默认 `30`, 范围 `1..365`), `doc_type` (可选), `doc_type_revision_id` (可选)
- **响应 (`200 OK`)**：
```json
{
  "window_days": 30,
  "avg_edit_diff_ratio": 0.184,
  "chat_accept_rate": 0.76,
  "verifier_first_pass_rate": 0.88,
  "verifier_block_rate": 0.04,
  "avg_slot_coverage_rate": 0.992,
  "avg_tokens_per_doc": 42800,
  "avg_cache_hit_ratio": 0.715,
  "sample_doc_count": 64
}
```

#### `GET /api/metrics/revisions-compare` (`JWT`, W2)
- **Query**：`doc_type` (必填, slug), `days` (可选, 默认 `90`)
- **响应 (`200 OK`)**：返回 `{ "doc_type_slug": "system-architecture", "revisions": RevisionMetricPointVO[] }`，按 `revision_no ASC` 排列，每点含 `doc_type_revision_id`, `revision_no`, `created_at`, `avg_edit_diff_ratio`, `chat_accept_rate`, `verifier_block_rate`, `avg_slot_coverage_rate`, `avg_tokens_per_doc`, `sample_count`。

#### `GET /api/metrics/slot-heatmap` (`JWT`, W2)
- **Query**：`doc_type` (必填, slug), `doc_type_revision_id` (可选), `days` (默认 `30`)
- **响应 (`200 OK`)**：返回 `{ "doc_type_slug": "...", "slots": [{ "slot_id": "core_request_flows", "title": "...", "avg_edit_diff_ratio": 0.27, "verifier_warning_rate": 0.12, "sample_count": 32 }] }`。

---

### 3.10 域 9：管理员 LLM 配置与运行存储运维（`F-14` W1 / `F-27` W2）

#### `GET /api/admin/llm-configs` (`Admin`, W1)
- **响应 (`200 OK`)**：
```json
{
  "items": [
    {
      "id": 1,
      "name": "DeepSeek-V3",
      "base_url": "http://10.0.10.20:8000/v1",
      "model_name": "deepseek-chat",
      "api_key_masked": "sk-****9f2a",
      "has_api_key": true,
      "timeout_seconds": 180,
      "max_retries": 3,
      "max_tokens_per_task": 200000,
      "max_files_per_task": 2000,
      "is_active": true,
      "is_default": true,
      "last_probe_status": "ok",
      "last_probe_latency_ms": 342,
      "last_probed_at": "2026-11-19T14:00:00+08:00"
    }
  ]
}
```
> **脱敏强约束**：响应体绝不包含明文 `api_key`；审计日志仅记录 `api_key_masked` 与 `api_key_changed`。

#### `POST /api/admin/llm-configs` & `PUT /api/admin/llm-configs/:id` (`Admin`, W1)
- **请求体 (`LLMConfigUpsertRequest`)**：
```json
{
  "name": "DeepSeek-V3",
  "base_url": "http://10.0.10.20:8000/v1",
  "model_name": "deepseek-chat",
  "api_key": "",
  "clear_api_key": false,
  "timeout_seconds": 180,
  "max_retries": 3,
  "max_tokens_per_task": 250000,
  "max_files_per_task": 2500,
  "is_active": true,
  "is_default": true
}
```
  - `api_key` 语义：`PUT` 时留空 `""` 且 `clear_api_key=false` 表示**保留原密钥不变**；`clear_api_key=true` 表示清空已保存密钥；非空时更新存储并刷新 `api_key_masked`；审计日志严禁写入明文。
- **响应**：新增返回 `201 Created`、更新返回 `200 OK`，均为脱敏 `LLMConfigVO`。

#### `POST /api/admin/llm-configs/:id/default` & `DELETE /api/admin/llm-configs/:id` (`Admin`, W1)
- `POST /:id/default`：手动将已启用模型切换为全局唯一默认模型（同步事务取消原默认标记）。
- `DELETE /:id`：若目标为非唯一默认模型或仍被某个 `DocType.preferred_model` 显式引用，返回 `409 CONFLICT`。

#### `POST /api/admin/llm-configs/probe` & `POST /api/admin/llm-configs/:id/probe` (`Admin`, W1)
- **请求体（可选）**：允许传入临时未保存的 `{ "base_url", "model_name", "api_key" }` 做预测试（`/:id/probe` 模式下 `api_key` 留空则复用已保存密钥调用）。
- **响应 (`200 OK`)**：
```json
{
  "id": 1,
  "name": "DeepSeek-V3",
  "reachable": true,
  "latency_ms": 418,
  "model_echo": "deepseek-chat",
  "error_message": ""
}
```

#### `GET /api/admin/storage/stats` (`Admin`, W2)
- **响应 (`200 OK`)**：
```json
{
  "sandbox": { "used_mb": 4210, "max_mb": 20480, "repo_count": 38, "active_leases": 2 },
  "ir_cache": { "used_mb": 1120, "max_mb": 10240, "facet_count": 412, "evicted_count": 15 },
  "publish_dir": { "used_mb": 18, "published_files": 210 },
  "scheduler": { "interactive_queue_len": 1, "batch_queue_len": 4, "running_llm_calls": 3, "max_concurrency": 8 }
}
```

#### `POST /api/admin/storage/reap` (`Admin`, W2)
- **响应 (`200 OK`)**：
```json
{
  "reaped_sandboxes": 3,
  "reaped_ir_facets": 28,
  "freed_bytes": 1648394240,
  "skipped_active_leases": 2
}
```


---

## 5. ADR-04 增量接口契约（`M8`~`M10`，新增 6 Endpoints，总计 60 Endpoints）

### 5.1 新增接口清单

| # | 方法 | 路由路径 | 鉴权级别 | 里程碑 | 业务功能摘要 | 审计 (`action / target / level`) |
| :-: | :-: | :--- | :-: | :-: | :--- | :--- |
| 55 | `GET` | `/api/repos/:repo_id/modules` | `JWT` | M8 | 查询仓库发现策略与派生模块索引列表（含 `active`/`missing`/`ignored`） | - |
| 56 | `GET` | `/api/repos/:repo_id/module-policy` | `JWT` | M8 | 获取仓库模块发现策略（缺省返回默认 `merged` 策略） | - |
| 57 | `PUT` | `/api/repos/:repo_id/module-policy` | `RepoWrite` | M8 | 更新仓库模块发现策略（`strategy`/`exclude_globs`/`min_effective_files`） | `update / repo_module_policy / P1` |
| 58 | `POST` | `/api/repos/:repo_id/modules/refresh` | `RepoWrite` | M8 | 触发仓库零 LLM 整仓 L1 抽取并刷新派生模块索引 | `refresh / repo_module / P1` |
| 59 | `PATCH` | `/api/repos/:repo_id/modules/:module_id/status` | `RepoWrite` | M8 | 切换模块状态（`active` ↔ `ignored`） | `update / repo_module / P1` |
| 60 | `POST` | `/api/repos/:repo_id/modules/migrate` | `RepoWrite` | M8 | 模块范围原子迁移（`from_subdir` → `to_subdir`，含发布文件重渲染迁移） | `migrate / repo_module / P0` |
| 61 | `POST` | `/api/tasks/generate-batch` | `BatchScopeWrite` | M8 | 触发多模块批次生成（全量范围前置权限校验 + 批次预算前置熔断，`202`） | `trigger / gen_task_batch / P1` |

> **`RepoWrite` 鉴权说明**：仅仓库级写者（`IsRepoLevelWriter`：`Repository.OwnerID` / `RelatedMembers` / `archdoc_admin` / `super_admin`）放行；子目录负责人或普通用户返回 `403 FORBIDDEN`。
> **`BatchScopeWrite` 鉴权说明**：逐一对目标每个范围执行 `CheckScopeWritePermission`，任一无权整批返回 `403 FORBIDDEN` 及 `details.forbidden_scopes: string[]`，无任何任务落库。

### 5.2 关键请求与响应 Schema

#### `PUT /api/repos/:repo_id/module-policy` (`RepoWrite`, M8)
- 请求体：`{ "strategy": "merged", "exclude_globs": ["legacy*"], "min_effective_files": 3 }`
- 响应 (`200 OK`)：`RepoModulePolicyVO`

#### `POST /api/repos/:repo_id/modules/refresh` (`RepoWrite`, M8)
- 响应 (`200 OK`)：`{ "repo_id": 12, "commit_sha": "...", "policy": {...}, "modules": RepoModuleVO[], "active_count": 5, "missing_count": 1 }`

#### `PATCH /api/repos/:repo_id/modules/:module_id/status` (`RepoWrite`, M8)
- 请求体：`{ "status": "ignored" }`（允许值：`active` | `ignored`）
- 响应 (`200 OK`)：`RepoModuleVO`

#### `POST /api/repos/:repo_id/modules/migrate` (`RepoWrite`, M8)
- 请求体：`{ "from_subdir": "order", "to_subdir": "trade/order" }`
- 响应 (`200 OK`)：`{ "repo_id": 12, "from_subdir": "order", "to_subdir": "trade/order", "migrated_documents": 2, "republished_documents": 1 }`
- 错误码：`400 INVALID_SUBDIR` / `400 INVALID_PARAM`（空路径或相同路径）、`403 FORBIDDEN`、`404 NOT_FOUND`（源范围无文档）、`409 CONFLICT`（目标存在同名文档或任一范围存在运行中任务）。

#### `POST /api/tasks/generate-batch` (`BatchScopeWrite`, M8)
- 请求体：
```json
{
  "repo_id": 12,
  "subdirectories": ["order", "pay"],
  "all_modules": false,
  "include_repo_scope": true,
  "ref_name": "",
  "doc_types": ["system-architecture", "data-and-contract"],
  "mode": "generate"
}
```
- 响应 (`202 Accepted`)：`GenTaskDetailVO`（`task_kind = "batch"`，含 `child_tasks` 与 `batch_summary`）。
- 边界错误：
  - `403 FORBIDDEN`：`details: { "forbidden_scopes": ["pay"] }`，无任何任务落库；
  - 任务异步预检触发批次预算熔断：父任务状态置 `failed`，`error_code = "BATCH_TOKEN_BUDGET_EXCEEDED"`，`budget_advice.module_breakdown` 返回按模块分解的估算明细，LLM 请求数为 0。

#### 存量接口向后兼容扩展
- `GET /api/repos` 与 `GET /api/repos/:repo_id/scopes`：`RepoScopeCoverageVO` 新增 `scope_status`（`"active" | "missing" | "orphan"`）、`module_source`（`"repo" | "arch_element" | "discovered" | "orphan"`）、`effective_files`、`total_loc`；
- `GET /api/documents/:id/source-preview`：新增可选查询参数 `scope=module|repo`（默认 `module`；`scope=repo` 严格受当前文档 `scope=repo` 证据锚点 / 快照 `module_context` 白名单保护，越界返 `403 PATH_ESCAPE`）；
- `GET /api/tasks/:task_id`：新增 `parent_task_id`、`child_tasks`、`batch_summary`。

---

## 6. ADR-06 增量接口契约（`M12`，新增 6 Endpoints）

### 6.1 新增接口清单（均挂载 `ScopeWrite` 守卫）

| # | 方法 | 路由路径 | 鉴权级别 | 里程碑 | 业务功能摘要 | 审计 (`action / target / level`) |
| :-: | :-: | :--- | :-: | :-: | :--- | :--- |
| 62 | `GET` | `/api/documents/:id/review-comments` | `ScopeWrite` | M12-A | 获取文档审阅批注列表（按 `comment_no ASC` 排序，支持 `status`/`scope_type`/`slot_id`/`persist_for_regen` 筛选） | - |
| 63 | `POST` | `/api/documents/:id/review-comments` | `ScopeWrite` | M12-A | 新建划词/块级/章节/全局批注（事务分配文档内单调 `comment_no`） | - |
| 64 | `PATCH` | `/api/documents/:id/review-comments/:comment_id` | `ScopeWrite` | M12-A | 更新批注正文、分类、`persist_for_regen` 或流转状态（`resolved`/`dismissed`/`open`） | - |
| 65 | `DELETE` | `/api/documents/:id/review-comments/:comment_id` | `ScopeWrite` | M12-A | 物理删除未绑定已采纳修订的批注草稿（仅限 `open`/`dismissed`） | - |
| 66 | `POST` | `/api/documents/:id/chat/messages/:msg_id/apply` | `ScopeWrite` | M12-B | 提案组多槽位批量/微调采纳，单事务合并生成 1 个新修订并无损继承未变槽位证据锚点 | `update / document_section / P1` |
| 67 | `POST` | `/api/documents/:id/chat/proposal-items/:item_id/reject` | `ScopeWrite` | M12-B | 拒绝提案组内单个槽位提案项并自动回退关联 `proposed` 批注为 `open` | - |

### 6.2 请求、响应与向后兼容契约

1. **扩展 `POST /api/documents/:id/chat/refine` 入参**：
   - 新增可选字段：`mode` (`"revise"` 默认 | `"ask"`)、`scope_type` (`"slot"` 默认 | `"inline"` | `"global"` | `"review_batch"`)、`comment_ids?: number[]`（`review_batch` 必填，去重 1~20 条）、`inline_anchor?: ReviewCommentAnchor`；
   - `mode="ask"` 放行 `locked` 槽位与 `global` 作用域，不写 `ChatProposalItem`、不报 `EMPTY_PROPOSAL`；
   - `scope_type="review_batch"` 允许空 `prompt`（自动合成可读消息标题与正文）；
   - SSE 事件流升级为 10 类事件：`meta` / `plan` / `stage` / `tool_call` / `delta`（经 `StreamSplitFilter` 净化，零 JSON 噪音）/ `proposal_item` / `diff_proposal`（单槽位兼容下发）/ `verifier_preview` / `done` / `error`。
2. **新增 `POST /api/documents/:id/chat/messages/:msg_id/apply`**：
   - 请求体：`{ "base_revision_no": 5, "change_summary": "...", "items": [{ "item_id": 101, "slot_id": "core_request_flows", "override_markdown": "..." }] }`（`items` 为空时默认采纳该消息下全部 `pending` 提案项）；
   - 校验：`base_revision_no > 0` 乐观锁拦截 `409 OPTIMISTIC_LOCK_CONFLICT`；目标 `locked` 槽位返 `409 CONFLICT`；
   - 响应 (`200 OK`)：返回 `DocumentDetailVO`；单事务仅生成 1 条 `DocumentRevision`，未修改/未选中槽位 SHA256 与非内联证据锚点无损保留，被取代同槽位旧提案项关联批注自动回退 `open`。
3. **新增 `POST /api/documents/:id/chat/proposal-items/:item_id/reject`**：
   - 响应 (`200 OK`)：`{ "item_id": 101, "message_id": 88, "item_status": "rejected", "message_proposal_status": "pending|rejected|accepted" }`。
4. **存量 `accept` / `reject` 向后兼容**：
   - `POST /api/documents/:id/chat/messages/:msg_id/accept` 委托给 `ApplyProposalItems`（采纳全部 `pending` 项）；
   - `POST /api/documents/:id/chat/messages/:msg_id/reject` 拒绝该消息下全部 `pending` 项并将关联 `proposed` 批注回退为 `open`。

---

## 7. ADR-07 增量接口契约（`M13`，新增 2 Endpoints）

### 7.1 新增接口清单

| # | 方法 | 路由路径 | 鉴权级别 | 里程碑 | 业务功能摘要 | 审计 (`action / target / level`) |
| :-: | :-: | :--- | :-: | :-: | :--- | :--- |
| 68 | `GET` | `/api/repos/:repo_id/branches` | `JWT` | M13 | 轻量枚举远端 Git 分支清单并标注默认分支与当前生效分支（30s TTL 缓存，支持 `?refresh=true`） | - |
| 69 | `PUT` | `/api/repos/:repo_id/branch` | `RepoWrite` | M13 | 切换单仓有效跟踪分支，事务重绑存量文档 `ref_name`、更新远端 HEAD 并同步已发布文档代码漂移 | `switch_branch / repo_branch / P1` |

### 7.2 请求、响应与向后兼容扩展

1. **`GET /api/repos/:repo_id/branches` (`JWT`, M13)**：
   - **Query 参数**：`refresh=true`（可选，穿透 30 秒缓存强制执行 `git ls-remote --symref`）。
   - **响应 (`200 OK`)**：
     ```json
     {
       "repo_id": 101,
       "default_branch": "main",
       "current_branch": "develop",
       "is_overridden": true,
       "remote_head_branch": "main",
       "branches": [
         { "name": "main", "commit_sha": "a1b2c3...", "is_head": true, "is_default": true, "is_current": false },
         { "name": "develop", "commit_sha": "d4e5f6...", "is_head": false, "is_default": false, "is_current": true }
       ]
     }
     ```
   - **错误码**：`400 INVALID_PARAM`、`404 NOT_FOUND`、`502 REMOTE_BRANCH_FETCH_FAILED`。

2. **`PUT /api/repos/:repo_id/branch` (`RepoWrite`, M13)**：
   - **权限守卫**：`CheckScopeWriteOrAbort(c, repoID, "")`（仅 `super_admin`、`archdoc_admin`、仓库 `OwnerID` / `RelatedMembers` 可操作；仅子目录负责人与普通用户返回 `403 FORBIDDEN`）。
   - **请求体**：`{ "branch": "develop" }`（传入 `""` 或等于 `default_branch` 表示恢复跟随共享表默认分支）。
   - **响应 (`200 OK`)**：
     ```json
     {
       "repo_id": 101,
       "previous_branch": "main",
       "current_branch": "develop",
       "default_branch": "main",
       "is_overridden": true,
       "remote_head_sha": "d4e5f6...",
       "rebound_documents": 3,
       "drifted_documents": 1,
       "changed": true
     }
     ```
   - **错误码**：`400 INVALID_PARAM`（分支名非法或远端不存在）、`403 FORBIDDEN`、`404 NOT_FOUND`、`409 CONFLICT`（存在活跃生成任务）、`409 MIGRATION_CONFLICT`、`502 REMOTE_BRANCH_FETCH_FAILED`。

3. **存量接口向后兼容扩展**：
   - `GET /api/repos`、`GET /api/arch-tree` 与 `GET /api/repos/:repo_id/scopes`：`RepoScopeCoverageVO` 新增可选/兼容字段 `default_branch`、`is_branch_overridden`、`can_switch_branch`；`RepoScopesDetailResponse` 同步新增 `default_branch`、`is_branch_overridden`、`can_switch_branch`；`branch` 返回解析后的有效跟踪分支。
   - `POST /api/tasks/generate` 与 `POST /api/tasks/generate-batch`：单仓单活跃分支护栏——`ref_name` 留空时自动取有效目标分支，显式传值必须与有效目标分支一致（兼容 `master` → `main` 回退），不匹配返 `400 INVALID_PARAM`。

---

## 8. ADR-08 增量接口契约（`M14`，新增 1 Endpoint，总计 70 Endpoints）

### 8.1 新增接口清单

| # | 方法 | 路由路径 | 鉴权级别 | 里程碑 | 业务功能摘要 | 审计 (`action / target / level`) |
| :-: | :-: | :--- | :-: | :-: | :--- | :--- |
| 70 | `GET` | `/api/engines` | `JWT` | M14 | 查询可用生成引擎清单、OpenCode CLI 版本状态与实时默认模型（CLI 探活内置 30s TTL 缓存，默认模型每次实时读取 DB） | - |

### 8.2 `GET /api/engines` (`JWT`, M14)

- **响应 (`200 OK`)**：
  ```json
  {
    "default_engine": "native",
    "engines": [
      {
        "id": "native",
        "label": "Native 直连引擎",
        "available": true,
        "version": "",
        "reason": "",
        "default_model": "DeepSeek-V3"
      },
      {
        "id": "opencode",
        "label": "OpenCode 深度 Agent",
        "available": true,
        "version": "1.18.21",
        "reason": "",
        "default_model": "DeepSeek-V3"
      }
    ]
  }
  ```
- **字段规则**：
  - `default_engine` 取自 `config.AppConfig.OpenCode.EffectiveDefaultEngine()`（若配置为 `"opencode"` 但 CLI 不可用，仍如实返回并配合 `engines[].available=false` 及 `reason` 供前端回退）。
  - `engines[1].available == false` 时，`reason` 返回中文说明（如 `"服务端已禁用 OpenCode 引擎"` 或 `"未在 PATH 及常见安装路径检测到 opencode 可执行文件"`）。

### 8.3 存量接口向后兼容扩展

1. **`POST /api/tasks/generate` (`ScopeWrite`, M14 单文档自动拆单与引擎选择)**：
   - **请求体扩展**：保留 `doc_types: string[]`，新增可选 `doc_type?: string`（两者按出现顺序合并去重，`len(dedupSlugs) >= 1`）与可选 `engine?: "native" | "opencode"`（留空自动取 `EffectiveDefaultEngine()`）。
   - **校验与容量保护**：`engine` 非法返回 `400 INVALID_PARAM`；指定 `opencode` 但不可用返回 `400 ENGINE_UNAVAILABLE`；调用 `TaskScheduler.ReserveSlots(Interactive, len(dedupSlugs))`，剩余容量不足返回 `429 QUEUE_FULL` 且 DB 创建 `0` 条记录。
   - **响应 (`202 Accepted` — `GenerateTaskSubmitResponse`)**：
     ```json
     {
       "id": 101,
       "repo_id": 167,
       "subdirectory": "",
       "ref_name": "main",
       "task_kind": "generate",
       "engine": "opencode",
       "priority": "interactive",
       "doc_types": ["system-architecture"],
       "status": "pending",
       "progress_percent": 0,
       "estimated_tokens": 0,
       "triggered_by": 2001,
       "created_at": "2026-11-22T21:40:00+08:00",
       "created_count": 3,
       "tasks": [
         { "id": 101, "engine": "opencode", "doc_types": ["system-architecture"], "status": "pending" },
         { "id": 102, "engine": "opencode", "doc_types": ["code-walkthrough"], "status": "pending" },
         { "id": 103, "engine": "opencode", "doc_types": ["agents-context"], "status": "pending" }
       ]
     }
     ```
     > 顶层匿名内嵌首任务 `tasks[0]`（使仅读取 `resp.id` 的存量客户端与单测 100% 兼容），同时返回 `created_count` 与完整单文档 `tasks[]` 数组。
2. **`POST /api/tasks/generate-batch` (`BatchScopeWrite`, M14)**：
   - 请求体新增可选 `engine?: "native" | "opencode"`（非法返 `400 INVALID_PARAM`，不可用前置返 `400 ENGINE_UNAVAILABLE`）；
   - 父任务与所有按 `(subdir, doc_type)` 拆分的单文档子任务均持久化并透传 `engine`，`budget_advice.module_breakdown` 仍按目标范围 `subdirectory` 聚合。
3. **`GET /api/tasks`、`GET /api/tasks/:task_id` 与 `GET /api/repos/:repo_id/scopes` (`JWT`, M14)**：
   - `GET /api/tasks` 新增可选 Query `?engine=all|native|opencode`（非法值返 `400 INVALID_PARAM`）；
   - `GenTaskDetailVO` 与 `GenTaskSummaryVO` 新增 `engine: "native" | "opencode"` 字段。
4. **`POST /api/documents/:id/incremental-refresh` (`ScopeWrite`, M14)**：
   - 请求体新增可选 `engine?: "native" | "opencode"`（留空默认取该文档最近一次生成任务使用的引擎 `GetLatestEngineByDocumentID`，无记录时回退系统默认引擎）；指定不可用引擎返 `400 ENGINE_UNAVAILABLE`。
5. **`GET` / `POST` / `PUT` `/api/admin/llm-configs` (`Admin`, M14)**：
   - 请求与响应新增可选字段 `opencode_model?: string`（最长 128 字符，禁止包含空白符与换行/控制字符，否则返 `400 INVALID_PARAM`）；`LLMConfigVO` 与 `LLMConfigAuditSnapshot` 同步暴露 `opencode_model`。

---

## 9. ADR-09 增量与演进接口契约（`M15`，零新增路由，保持 70 Endpoints）

### 9.1 `GET /api/arch-tree` (`JWT`, M15 冻结契约)

- **Query 参数与白名单校验**：
  - `search`、`department_id`、`subsystem_id`、`doc_type`、`status`（`all | approved | draft | drifted | missing`）、`scope_type`（`all | repo | subdir`）；
  - `arch_binding`：**仅允许 `"" | "all" | "bound" | "unbound"`**（`/api/arch-tree` 不接受 `unbound_repo`，传入 `unbound_repo` 或其他非法值统一返回 `400 INVALID_PARAM`）。
- **核心构造与聚合规则**：
  1. **全量活跃仓加载**：`GetArchTree` 内部调用 `ListAllActiveRepositories(ctx, nil)` 加载全量活跃仓库（不按 `department_id` 预过滤，确保跨部门绑定仓库不误判为 `repo_inactive` 且 `summary.unassigned_repo_count` 不受树筛选影响）；
  2. **非功能模块零挂仓**：`subsystem` 与 `group` 节点 `scopes` 固定为 `[]`、`scope` 为 `nil`、`binding_status` 为空，不再吸附整仓或未建模块的自动扫描目录；
  3. **功能模块 (`module`) 三态与实时兜底构造**：
     - `"bound"`：`el.RepoID != nil` 且目标仓库在活跃仓库集合中存在，`scope` 非空；若预构建行中未命中（如在 `archdoc_repo_modules` 中标为 `ignored` 或尚未执行扫描的新绑定目录），必须调用 `buildSingleScopeRow` 实时兜底构造该 `module` 的 `RepoScopeCoverageVO`（整仓模块 `normSub == ""` 仅认仓库级写权限，子目录模块遵循 `SC-12` 子目录隔离，普通用户严格过滤未发布草稿）；多模块指向同一 `(repo_id, subdir)` 时各自持有独立拷贝的 `Scope` 实例并独立计入聚合；
     - `"unbound"`：`el.RepoID == nil`，`scope = nil`；
     - `"repo_inactive"`：`el.RepoID != nil` 但仓库不存在或 `is_active == false`，`scope = nil`；
  4. **先筛选剪枝，后向上聚合**：
     - **无筛选**：保留全部节点（含 `0 模块` 空子系统/空功能组与 `unbound`/`repo_inactive` 缺口模块）；
     - **`search` 命中非叶节点**：若 `subsystem`/`group` 名称或 `identifier` 命中 `search`，置 `ancestorSearchMatched = true` 向下传递（后代模块视为命中 `search`，但仍接受 `status`/`department_id`/`arch_binding`/`scope_type` 过滤）；仅 `search` 且无其他非搜索限制时，命中的空非叶节点亦可保留；
     - **`status` / `department_id` / `scope_type`**：仅匹配 `binding_status == "bound"` 且满足条件的模块；
     - **`arch_binding`**：`bound` 仅保留 `binding_status == "bound"` 模块；`unbound` 仅保留 `binding_status != "bound"`（即 `unbound` 与 `repo_inactive`）；
     - **剪枝后聚合**：剪枝完成后自底向上递归计算 `ArchTreeStatsVO` 并汇总根节点产出 `ArchTreeSummaryVO`，确保父节点 `stats` 与顶部 `summary` 100% 等于可见 `module` 之和；
     - **`subsystems[]` 计数口径**：在剪枝前基于完整树统计每个根子系统子树下的全部 `module` 数（含 `unbound`/`repo_inactive`），写入 `module_count` 并镜像填充 `scope_count = module_count`。
- **响应 (`200 OK`)**：
```json
{
  "summary": {
    "total_modules": 4,
    "bound_modules": 2,
    "covered_modules": 1,
    "approved_docs": 1,
    "draft_docs": 1,
    "drifted_docs": 1,
    "unassigned_repo_count": 2
  },
  "subsystems": [
    {
      "id": 500,
      "identifier": "RND_AI",
      "name_cn": "AI辅助研发子系统",
      "name_en": "AI R&D Subsystem",
      "module_count": 4,
      "scope_count": 4
    }
  ],
  "nodes": [
    {
      "id": 500,
      "identifier": "RND_AI",
      "name_cn": "AI辅助研发子系统",
      "name_en": "AI R&D Subsystem",
      "type": "subsystem",
      "parent_id": null,
      "owner_id": 1001,
      "owner_name": "管理员",
      "repo_id": null,
      "subdirectory": "",
      "description": "",
      "stats": {
        "module_count": 4,
        "bound_module_count": 2,
        "covered_module_count": 1,
        "approved_doc_count": 1,
        "draft_doc_count": 1,
        "drifted_doc_count": 1,
        "blocked_doc_count": 0
      },
      "scopes": [],
      "children": [
        {
          "id": 501,
          "identifier": "BENCH",
          "name_cn": "门户功能组",
          "name_en": "Bench Group",
          "type": "group",
          "parent_id": 500,
          "owner_id": 1001,
          "owner_name": "管理员",
          "repo_id": null,
          "subdirectory": "",
          "description": "",
          "stats": {
            "module_count": 4,
            "bound_module_count": 2,
            "covered_module_count": 1,
            "approved_doc_count": 1,
            "draft_doc_count": 1,
            "drifted_doc_count": 1,
            "blocked_doc_count": 0
          },
          "scopes": [],
          "children": [
            {
              "id": 502,
              "identifier": "BENCH_FRONT",
              "name_cn": "门户前端",
              "name_en": "Bench Frontend",
              "type": "module",
              "parent_id": 501,
              "owner_id": 1001,
              "owner_name": "管理员",
              "repo_id": 101,
              "subdirectory": "frontend",
              "description": "",
              "binding_status": "bound",
              "scope": {
                "repo_id": 101,
                "repo_name": "code-bench",
                "subdirectory": "frontend",
                "scope_type": "subdir",
                "arch_element_id": 502,
                "branch": "develop",
                "is_branch_overridden": true,
                "can_write": true,
                "doc_statuses": {}
              },
              "stats": {
                "module_count": 1,
                "bound_module_count": 1,
                "covered_module_count": 1,
                "approved_doc_count": 1,
                "draft_doc_count": 1,
                "drifted_doc_count": 1,
                "blocked_doc_count": 0
              },
              "scopes": [],
              "children": []
            },
            {
              "id": 503,
              "identifier": "BENCH_AUTH",
              "name_cn": "权限中心",
              "name_en": "Bench Auth",
              "type": "module",
              "parent_id": 501,
              "owner_id": 1002,
              "owner_name": "张三",
              "repo_id": null,
              "subdirectory": "",
              "description": "",
              "binding_status": "unbound",
              "stats": {
                "module_count": 1,
                "bound_module_count": 0,
                "covered_module_count": 0,
                "approved_doc_count": 0,
                "draft_doc_count": 0,
                "drifted_doc_count": 0,
                "blocked_doc_count": 0
              },
              "scopes": [],
              "children": []
            }
          ]
        }
      ]
    }
  ],
  "unassigned_scopes": []
}
```

### 9.2 `GET /api/repos` (`JWT`, M15 `arch_binding=unbound_repo` 仓库级 MECE 判定)

- **合法枚举白名单**：`"" | "all" | "bound" | "unbound" | "unbound_repo"`；非法值返回 `400 INVALID_PARAM`。
- **仓库级 MECE 判定规则 (`arch_binding=unbound_repo`)**：
  1. 基于平台**全量** `architecture_elements`（不受 `department_id` 筛选截断）构建集合 `boundModuleRepoIDs = { *el.RepoID | el.Type == "module" && el.RepoID != nil && *el.RepoID > 0 }`；
  2. 过滤掉所有 `row.RepoID ∈ boundModuleRepoIDs` 的覆盖行（包含该仓库的整仓行与所有自动发现/历史子目录行，彻底消除 `models`/`templates` 等辅助目录噪声）；
  3. 仅设置 `repositories.service_group` 或仅被 `subsystem`/`group` 非叶节点引用的仓库，因其在 `/arch-tree` 中不承接代码仓渲染，按 MECE 原则保留在 `unbound_repo` 结果中，与 `ArchTreeSummaryVO.unassigned_repo_count`（无 `department_id` 等额外条件时）严格一致；
  4. 保留既有 `arch_binding=all | bound | unbound` 行为不变以兼容既有调用方；`/repos` 前端默认模式请求 `arch_binding=unbound_repo`，切换至「全部代码仓」时请求 `arch_binding=all` 并显式写入 URL。
