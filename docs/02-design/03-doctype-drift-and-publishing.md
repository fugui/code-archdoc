# `DocType` 注册表、双维漂移雷达、发布挂载与质量度量设计

- **文档编号**：`03-doctype-drift-and-publishing.md`
- **文档版本**：v1.0.0 (Stage 2 SDD Baseline)
- **编制日期**：2026-11-19
- **归属阶段**：阶段二：系统架构、领域引擎与数据模型设计 (`02-design`)
- **关联基线**：`F-03`, `F-10`, `F-11`, `F-12`, `F-25`, `F-26`, `SC-05`, `SC-07`, `SC-08`, `SC-10`, `SC-11`, `Non-Goal 7`, [`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md)

---

## 1. `DocType` 目录即唯一事实源注册表（`F-03`）

对标 `code-shield/tasks` 架构模式，服务端受控目录 `<data_dir>/doc_types/<slug>/` 是所有文档类型资产的**唯一真实源（SSOT）**；PostgreSQL 仅存储元数据索引（`archdoc_doc_types`）与不可变修订快照（`archdoc_doc_type_revisions`），绝不维护与目录脱离的可编辑副本。遵循 `Non-Goal 7`，`DocType` 全量采用声明式配置，严禁嵌入任何可执行脚本。

### 1.1 目录资产规范与 `slug` 白名单校验

```text
<data_dir>/doc_types/
└── <slug>/                          # 强制匹配白名单正则: ^[a-z0-9][a-z0-9_-]{1,62}$
    ├── meta.json                    # 声明式元数据、范围裁剪、槽位定义与校验策略
    ├── template.md                  # 带 <!-- slot:<slot_id> --> 标记的 Markdown 骨架模板
    ├── generation_prompt.md         # 系统级生成指令与风格约束
    └── refine_prompt.md             # (可选) Chat 局部精修附加约束指令
```

- **路径安全校验**：所有 `slug` 入参必须通过正则 `^[a-z0-9][a-z0-9_-]{1,62}$` 校验（并额外通过 `sandbox.SafeJoin(docTypesRoot, slug)` 二次确认）；包含 `../`、`/`、大写字母、空格或非白名单字符的请求直接返回 `400 Bad Request`（满足 `SC-08`）。

### 1.2 `meta.json` 完整 JSON Schema 与未知扩展字段保留策略

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "ArchdocDocTypeMeta",
  "type": "object",
  "required": ["name", "display_name", "is_active", "ir_facets", "slots", "model_tier"],
  "additionalProperties": true,
  "properties": {
    "name": {
      "type": "string",
      "pattern": "^[a-z0-9][a-z0-9_-]{1,62}$",
      "description": "必须与所在目录名 slug 严格一致"
    },
    "display_name": { "type": "string", "minLength": 1, "maxLength": 120 },
    "description": { "type": "string", "maxLength": 1000 },
    "is_active": { "type": "boolean" },
    "is_builtin": { "type": "boolean", "default": false },
    "model_tier": { "type": "string", "enum": ["fast", "flagship"] },
    "timeout_seconds": { "type": "integer", "minimum": 30, "maximum": 1800, "default": 300 },
    "max_output_tokens": {
      "type": "integer",
      "minimum": 500,
      "maximum": 64000,
      "description": "可选全篇输出 Token 硬上限（例如 agents-context 默认设 4000）"
    },
    "scope_profile": {
      "type": "object",
      "properties": {
        "include_globs": { "type": "array", "items": { "type": "string" } },
        "exclude_globs": { "type": "array", "items": { "type": "string" } },
        "max_depth": { "type": "integer", "minimum": 1, "maximum": 32, "default": 12 },
        "include_tests": { "type": "boolean", "default": false },
        "include_legacy_docs": {
          "type": "boolean",
          "default": false,
          "description": "P1 扩展钩子：开启后注入仓内既有 docs/ 或上传历史文档供 legacy-gap-report 消费"
        }
      }
    },
    "ir_facets": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "string",
        "enum": [
          "tree", "build", "deps", "config_keys", "symbols",
          "idl_contracts", "http_routes", "data_models", "runtime_primitives"
        ]
      }
    },
    "slots": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "required": ["slot_id", "title", "ownership", "required", "ir_facets"],
        "properties": {
          "slot_id": {
            "type": "string",
            "pattern": "^[a-z][a-z0-9_]{1,48}$",
            "description": "跨修订保持稳定的章节槽位唯一标识"
          },
          "title": { "type": "string", "minLength": 1, "maxLength": 120 },
          "ownership": { "type": "string", "enum": ["auto", "hybrid", "locked"] },
          "required": { "type": "boolean" },
          "ir_facets": { "type": "array", "items": { "type": "string" } },
          "token_budget": { "type": "integer", "minimum": 500, "maximum": 64000, "default": 8000 },
          "min_chars": { "type": "integer", "minimum": 0, "default": 40 },
          "instruction_hint": { "type": "string", "maxLength": 1000 }
        }
      }
    },
    "verify_policy": {
      "type": "object",
      "properties": {
        "strict_symbol_check": { "type": "boolean", "default": false },
        "require_mermaid": { "type": "boolean", "default": false },
        "allowed_mermaid_types": {
          "type": "array",
          "items": { "type": "string" },
          "default": ["flowchart", "graph", "sequenceDiagram", "stateDiagram-v2", "classDiagram", "erDiagram"]
        },
        "max_mermaid_nodes": { "type": "integer", "minimum": 5, "maximum": 100, "default": 35 },
        "max_mermaid_edges": { "type": "integer", "minimum": 5, "maximum": 200, "default": 60 },
        "block_on_secret_leak": { "type": "boolean", "default": true }
      }
    }
  }
}
```

**未知扩展字段无损保留机制**：
- Go 解析 `meta.json` 时采用「强类型结构体 + `ExtraFields map[string]json.RawMessage`」双轨编解码（`UnmarshalJSON` 先解为 `map[string]json.RawMessage`，提取所有已知标准键，将剩余未知键完整留存于 `ExtraFields`；Web 编辑写回磁盘执行 `MarshalJSON` 时先合并 `ExtraFields` 再覆写已知键并按字典序格式化输出），确保未来 P1/P2 或手工在磁盘添加的扩展属性绝不因 Web 保存而丢失。

### 1.3 内置 5 类 `DocType` 标准槽位规划（直接承接 `F-26` Golden Sample）

服务启动时将编译期 `//go:embed` 内置的 5 类默认 `DocType` 幂等 Seed 至 `<data_dir>/doc_types/`（**仅当目标 `<slug>/` 目录不存在时写出，绝不覆盖管理员已修改的线上资产**）。每个槽位定义稳定 `slot_id`、所有权、必填性、数据源与 Token 预算，直接作为 `F-26` 人工标准样例与回归评测契约：

| `DocType` (`slug` / `model_tier`) | 槽位 `slot_id` | 章节标题 | 默认所有权 | 必填 | 订阅 `ir_facets` | `token_budget` |
| :--- | :--- | :--- | :---: | :---: | :--- | :---: |
| **`system-architecture`**<br>系统架构与核心链路<br>(`flagship`) | `overview_boundaries` | 1. 系统定位与模块分层边界 | `auto` | ✅ | `tree`, `deps`, `build` | 8000 |
| | `runtime_concurrency` | 2. 运行时并发、进程/线程、锁与共享内存模型 | `auto` | ✅ | `runtime_primitives`, `symbols` | 10000 |
| | `core_request_flows` | 3. 端到端核心业务链路与时序图 | `hybrid` | ✅ | `idl_contracts`, `http_routes`, `symbols`, `deps` | 12000 |
| | `fault_tolerance_deps` | 4. 外部依赖拓扑、容错机制与故障影响面矩阵 | `auto` | ✅ | `deps`, `idl_contracts`, `runtime_primitives`, `config_keys` | 10000 |
| | `adr_constraints_debt` | 5. 架构约束、关键 ADR 与已知技术债 | **`locked`** | ❌ | `tree`, `deps` *(仅首次生成提供初稿或占位)* | 4000 |
| **`data-and-contract`**<br>数据模型与接口契约<br>(`flagship`) | `service_interfaces` | 1. 对外接口契约（RPC / Proto / HTTP 签名） | `auto` | ✅ | `idl_contracts`, `http_routes` | 12000 |
| | `data_storage_models` | 2. 核心数据实体与存储/消息模型 | `auto` | ✅ | `data_models`, `idl_contracts`, `symbols` | 10000 |
| | `protocol_error_codes` | 3. 协议边界、版本兼容性与错误码体系 | `auto` | ✅ | `idl_contracts`, `runtime_primitives`, `symbols` | 8000 |
| | `contract_invariants` | 4. 契约演进约定与人工补充说明 | **`locked`** | ❌ | `idl_contracts` | 3000 |
| **`ops-and-config`**<br>配置、部署与研发上手<br>(`fast`) | `config_inventory` | 1. 脱敏配置项全量清单与环境变量映射 | `auto` | ✅ | `config_keys`, `tree` | 8000 |
| | `deploy_topology` | 2. 部署拓扑、运行依赖与端口/资源清单 | `hybrid` | ✅ | `build`, `deps`, `config_keys`, `runtime_primitives` | 8000 |
| | `local_build_dev` | 3. 本地构建、编译开关、单测与调试指南 | `auto` | ✅ | `build`, `tree` | 8000 |
| | `troubleshooting_logs` | 4. 关键日志、错误码与高频故障排障手册 | `hybrid` | ✅ | `runtime_primitives`, `config_keys`, `symbols` | 8000 |
| **`code-walkthrough`**<br>代码导读与改码指南<br>(`fast`) | `directory_map` | 1. 仓库目录职责与核心模块地图 | `auto` | ✅ | `tree`, `deps` | 8000 |
| | `call_chain_guide` | 2. 核心请求逐跳源码导航 | `auto` | ✅ | `idl_contracts`, `http_routes`, `symbols`, `deps` | 12000 |
| | `modification_recipes` | 3. 典型二次开发修改清单（新增接口/字段/配置） | `hybrid` | ✅ | `tree`, `idl_contracts`, `http_routes`, `data_models`, `build` | 10000 |
| **`agents-context`**<br>AI Coding Agent 上下文包<br>(`fast`, 全篇 `max_output_tokens=4000`) | `agent_module_summary` | 1. 模块边界与核心目录速览 | `auto` | ✅ | `tree`, `build` | 4000 |
| | `agent_hard_constraints` | 2. 不可违背的架构红线与构建测试命令 | **`locked`** | ✅ | `build`, `deps`, `config_keys` | 3000 |
| | `agent_change_checklist` | 3. 高频改码入口、关键符号与常见坑点 | `auto` | ✅ | `symbols`, `idl_contracts`, `http_routes`, `runtime_primitives` | 5000 |

### 1.4 确定性有序聚合哈希与目录 → DB 同步流水线

启动或管理写操作完成后触发 `DocTypeRegistry.SyncAll(ctx)`：
1. 扫描 `<data_dir>/doc_types/` 下所有合法 `<slug>/` 子目录；
2. 对单个 `<slug>/` 按**固定有序文件清单**计算 `aggregate_hash`：
   ```text
   ordered_files = ["meta.json", "template.md", "generation_prompt.md", "refine_prompt.md"]
   h = sha256.New()
   for name in ordered_files:
       if file exists:
           content = normalizeCRLFToLF(readFile(name))
           h.Write(name + "\x00" + sha256hex(content) + "\n")
   aggregate_hash = hex.EncodeToString(h.Sum(nil))
   ```
3. 比对 `archdoc_doc_types` 中当前 `latest_aggregate_hash`：
   - **完全一致**：不创建新修订，保持幂等（满足 `F-03` 验收指标 1 & 2）；
   - **不一致（新建或任一文件变动）**：在数据库事务内插入一条不可变 `archdoc_doc_type_revisions`（`revision_no = prev + 1`，快照保存 `meta_json`、`template_md`、`generation_prompt_md`、`refine_prompt_md`、`slots_snapshot_json` 与 `aggregate_hash`），并更新 `archdoc_doc_types.latest_revision_id` 与 `latest_aggregate_hash`。
4. **槽位稳定校验**：对比新旧修订的 `slots[]`，未修改的 `slot_id` 保持原标识不变，变更集合用于后续 `Spec Drift` 精确计算。

### 1.5 `W2` Web 在线 CRUD 安全写路径（`SC-08`）

管理员通过 Web 界面新建、克隆、修改、启停或删除 `DocType` 时，严格遵守「**先原子写目录、再重同步 DB**」单一事实源顺序：
1. **同 `slug` 写互斥锁**：进程内 `sync.Map` 维护 `slug -> *sync.Mutex`，串行化同一 `DocType` 的目录读写操作。
2. **新建/克隆重名拦截**：检查目录与 DB，若 `slug` 已存在直接返回 `409 Conflict`；自动写入合法默认骨架文件。
3. **原子落盘写回**：编辑任一资产文件时，先经 JSON Schema 与模板 `slot_id` 一致性校验，写入 `<doc_types>/<slug>/.tmp-<filename>-<uuid>` → `fsync` → 原子 `os.Rename` 覆盖目标文件，随后触发 `SyncOne(slug)` 刷新数据库索引与新 `DocTypeRevision`。
4. **引用删除保护**：执行物理删除前查询 `archdoc_documents` 是否存在 `doc_type_slug = ? AND status = 'approved'` 的已发布文档；若计数 > 0，拒绝物理删除并返回 **`409 Conflict`**（提示管理员改用「停用 `is_active = false`」替代，满足 `SC-08`）。

---

## 2. 章节所有权、人工锁定与模板升级保护（`F-10`）

### 2.1 三种章节所有权合并规则矩阵

每个文档的章节均绑定稳定 `slot_id`（存放在 `archdoc_section_evidences` 及修订快照 `sections_json` 中），支持三种所有权模式：

| 所有权模式 | 手工直编 / Chat 精修 | 整篇重新生成（新提交 / 新模板） | 漂移触发单章节增量刷新 |
| :--- | :--- | :--- | :--- |
| **`auto`**（代码托管） | 允许编辑；编辑后自动提示用户是否升级为 `hybrid` 或 `locked` | **覆盖重生成**为最新代码事实 | 若该章节受漂移影响则重生成；未受影响则**字节级不变** |
| **`hybrid`**（人机混合） | 允许编辑，保留人工补充段落 | 将旧章节中人工修改段落作为隔离参考上下文传给 L2 进行融合重生成，并标注「已融合待复核」 | 若该章节受漂移影响则融合重生成；未受影响则**字节级不变** |
| **`locked`**（人工锁定） | 仅负责人可手工直编或主动解锁后精修 | **严格豁免覆盖（字节级保留原内容）**；若检测到关联证据变化，仅附加 `needs_review_drift = true` 提示徽标 | **严格豁免覆盖（字节级不变）**，仅更新待核对提示 |

### 2.2 模板升级与「孤立章节（Orphan Sections）」保护机制

当管理员发布新的 `DocTypeRevision` 并移除了某个旧 `slot_id`（例如删除旧槽位 `legacy_notes`），而既有文档中该槽位处于 `locked`（或含人工编辑的 `hybrid`）状态时：
1. 重生成编排器在按新模板 `slots[]` 组装新修订章节后，扫描上一修订中**未出现在新模板中且 `ownership == "locked"` 或 `manually_edited == true`** 的章节；
2. 严禁丢弃此类章节！编排器将其以原 `slot_id`、`is_orphan = true` 标记挂载到文档末尾的专属区块 `## [孤立章节 - 待人工处置]`（对应数据库 `archdoc_section_evidences.is_orphan = true`）；
3. Canvas Studio 在画布底部高亮展示「孤立章节待处置区」，提供负责人三种显式动作：**① 复制/合并内容到其他有效章节、② 保留为自定义锁定章节、③ 确认人工删除**（满足 `F-10` 验收指标 2）。

---

## 3. 定稿发布、`publish_dir` 原子落盘、撤回与导出（`F-11`、[`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md)）

### 3.1 发布前强制门禁与渲染管线

负责人对草稿触发 `POST /api/documents/:id/publish` 时，服务端依次执行：
1. **Verifier 硬门禁复查**：验证目标修订的 `VerifierReport.CanApprove == true`（`blocker_count == 0`，即零明文凭据泄漏且无阻断性错误）；未通过直接返回 `422 Unprocessable Entity`（满足 `SC-04`）。
2. **YAML Frontmatter 注入（对齐 `D-02`）**：由 `pkg/publisher.RenderMarkdown` 在文档顶部写入标准 YAML Frontmatter（`title`、`repo`、`subdir`、`doc_type`、`doc_type_name`、`doc_type_revision`、`doc_id`、`revision_no`、`ref_name`、`commit_sha`、`published_at`、`published_by`、`source_module: "archdoc"`）。
3. **证据锚点转平台深链（对齐 `D-03`）**：将所有已验证的证据引用统一渲染为可点击 Markdown 链接：
   `[src/order/order_service.cpp:142-188](/archdoc/docs/<doc_id>/evidence?commit=<commit_sha>&file=src%2Forder%2Forder_service.cpp&symbol=order%3A%3AOrderService%3A%3APay&lines=142-188)`。
4. **跨文档相对链接校验器**：扫描文中指向同仓其他 `DocType` 的相对链接（如 `./data-and-contract.md`），若目标 `DocType` 尚未发布，自动降级为纯文本引用并附注「（尚未发布）」，杜绝 `code-bench` 中产生死链。

### 3.2 `publish_dir` 原子落盘与双向撤回流程（`SC-07`、`SC-10`）

- **原子落盘（Publish / Re-publish）**：
  1. 获取目标仓库发布路径互斥锁；
  2. 将渲染后的完整字节流写入 `<publish_dir>/.staging/tmp-<uuid>.md`，调用 `file.Sync()`；
  3. 确保目标目录 `<publish_dir>/<repo_slug>/[sub/<subdir>/]` 存在，执行同盘原子 `os.Rename(stagingPath, targetPath)`；
  4. 若历史 `published_rel_path` 非空且与新路径不同（仓库曾改名），删除旧路径文件；
  5. 原子重建该仓库 `README.md` 与根级 `<publish_dir>/README.md` 索引；
  6. 在 DB 事务中将 `archdoc_documents.status` 更新为 `approved`、回写 `published_revision_id`、`published_rel_path`、`published_at` 与 `published_by`。
- **一键撤回（Unpublish）**：
  1. 持锁删除 `<publish_dir>/<published_rel_path>`；
  2. 重建该范围的 `README.md`（若该范围下已无任何已发布文档，则删除 `README.md` 并自底向上删除空目录，确保 `code-bench` 侧边栏干净无残留）；
  3. DB 事务将文档 `status` 退回 `draft`、清空 `published_revision_id` 与 `published_rel_path`，记录 `commonAudit` 审计日志（满足 `SC-10`）。

### 3.3 单篇 Markdown 与多文档 ZIP 打包导出

- **单篇导出**：直接流式返回带 Frontmatter 的 Markdown 文件（`Content-Disposition: attachment; filename="<repo>-<doctype>.md"`）；普通用户仅允许下载 `status == "approved"` 的文档。
- **多文档 ZIP 导出**：支持按仓库/筛选集打包下载；服务端在内存中流式构建 ZIP，包含顶部 `INDEX.md`（目录索引与相对链接）及全部所选已发布文档。每个 ZIP 条目名强制经 `SanitizeZipEntryPath` 校验（禁止绝对路径、`..` 与控制字符），彻底防 Zip-Slip（满足 `F-11` 验收指标 3）。

---

## 4. 远端变更巡检与章节级双维漂移雷达（`F-25`、`F-12`）

### 4.1 远端变更巡检调度器（`F-25`，`W2`）

- **巡检对象收敛**：仅查询存在至少 1 篇 `status == "approved"` 已发布文档的活跃仓库（`repositories.is_active = true`），避免对未纳入文档治理的仓库浪费资源。
- **轻量 `git ls-remote` 协议**：后台 Ticker 按可配置周期（`drift.poll_interval_minutes`，默认 **15 分钟**）触发一轮巡检，工作池受限于独立有界并发信号量（默认并发 `4`，与交互任务隔离互不影响）。每个仓库仅调用 `gitsync.LsRemoteHead(ctx, repo.URL, repo.Branch)`（超时 20 秒，**零对象克隆**）。
- **指数退避自愈**：在 `archdoc_repo_remote_heads` 表中维护 `consecutive_failures` 与 `next_retry_at`。某仓库远端不可达或 SSH 超时时，退避间隔按 `15m -> 30m -> 1h -> 2h -> 4h`（上限 6 小时）指数增长，绝不阻塞同批次其他健康仓库巡检（满足 `F-25` 验收指标 2）。

### 4.2 章节级 `Code Drift` 两阶段定位算法（`F-12`、`SC-07`、`SC-11`）

当 `F-25` 发现某仓库 `remote_head_sha != last_checked_sha` 且与某篇已发布文档的 `published_commit_sha` 不一致时，触发轻量两阶段漂移定位（不执行完整代码克隆或 LLM 调用）：

```text
Algorithm DetectCodeDrift(doc, publishedCommitSHA, remoteHeadSHA):
  1. 快速路径：若 publishedCommitSHA == remoteHeadSHA -> 返回 无 Code Drift。
  2. 获取变更文件集（零工作区签出）：
     - 若本地沙箱 .git 存在：执行 gitsync.FetchCommitMetadata(localPath, repoURL, remoteHeadSHA)
       后运行 gitsync.DiffNameStatus(localPath, publishedCommitSHA, remoteHeadSHA) 得 ChangedFiles[]。
     - 若本地沙箱已被 LRU 回收：尝试极简裸元数据拉取 (--bare --filter=blob:none)；
       若远端不支持 partial clone，则记录 doc 级 drift_precision="repo_level"，待用户点击详情/刷新时同步。
  3. 构建文件变更索引：changedMap[file_path] = ChangedFile{Status, OldPath}。
  4. 按需符号级精确定位（仅解析与证据锚点交集的变更文件，数量通常为 1~10 个文件）：
     affectedSlots = map[slot_id][]DriftEvidenceDetail
     for each section in doc.PublishedSections:
       for each anchor in section.Evidences:
         cf, touched = changedMap[anchor.FilePath]
         if !touched:
           continue   // 未触及该证据文件 -> 零开销跳过
         if cf.Status == "D" || cf.Status == "R":
           affectedSlots[section.SlotID].append(anchor, reason="file_removed_or_renamed")
         else if anchor.SymbolKind == "file":
           affectedSlots[section.SlotID].append(anchor, reason="file_modified")
         else:
           // 仅对当前这 1 个文件读取 remoteHeadSHA 下的 blob，复用 L1 解析器抽取该文件符号
           blob = gitsync.ShowFileAtCommit(localPath, remoteHeadSHA, anchor.FilePath)
           newSymbols = extractSingleFileSymbols(anchor.FilePath, blob)
           sym, exists = newSymbols[anchor.FQSymbol]
           if !exists:
             affectedSlots[section.SlotID].append(anchor, reason="symbol_removed")
           else if sym.ContentFingerprint != anchor.ContentFingerprint:
             affectedSlots[section.SlotID].append(anchor, reason="symbol_modified")
           // 若 fingerprint 完全一致（仅行号上下平移）-> 判定无实质漂移，不误报！
  5. 落库 archdoc_document_drifts (drift_type="code", affected_slots_json, remote_head_sha)。
```

> **精准满足 `SC-11`**：当新提交仅修改 `OrderService::Pay` 函数实现时，只有锚定 `order::OrderService::Pay` 的第 3 章因 `content_fingerprint` 变化而被标记 `Code Drift`，同文件内其他未修改函数引用的章节不会误报。

### 4.3 槽位级 `Spec Drift` 定位算法（`F-12`、`SC-08`）

每轮巡检周期（及管理员更新 `DocType` 完成目录同步后立即异步触发），对所有绑定该 `doc_type_slug` 的已发布文档执行零网络开销的 `Spec Drift` 比对：

1. 比对 `doc.DocTypeRevisionID` 与 `docType.LatestRevisionID`：相等则 `Spec Drift = 0`。
2. 不等时，加载两份不可变 `DocTypeRevision` 的 `slots_snapshot_json`、`generation_prompt_md` 哈希与 `template_md` 哈希：
   - 按 `slot_id` 做集合差运算，精确输出：
     - `added_slots[]`：新修订新增的 `slot_id`；
     - `removed_slots[]`：新修订删除的 `slot_id`（提示若文档已锁定将转入孤立章节）；
     - `modified_slots[]`：槽位 `ir_facets`/`instruction_hint`/`title`/`required` 变更或全局 `generation_prompt_md` 变更影响到的全部非锁定槽位。
3. 写入 `archdoc_document_drifts`（`drift_type = "spec"`），确保模板修改后在**下一次巡检周期内（实际上近实时）**在漂移雷达标出 `Spec Drift`（满足 `F-12` 验收指标 3 与 `SC-08`）。

### 4.4 单篇文档章节级增量刷新编排

用户在漂移雷达点击「增量刷新受影响章节」时：
1. 取该文档 `Code Drift` 与 `Spec Drift` 受影响 `slot_id` 的并集 `TargetSlotIDs`；
2. 过滤掉其中 `ownership == "locked"` 的槽位（保持原字节不变，仅标记待人工核对）；
3. 若存在 `Code Drift`，执行 `gitsync.Sync` + `EnsureSnapshotIR`（更新至最新 `commit_sha`）；
4. 仅针对 `TargetSlotIDs \ LockedSlots` 创建 `archdoc_gen_slot_tasks` 并发起 L2 局部生成；
5. 将新生成的章节与旧修订中**未受影响的章节（字节级原样拷贝）**、**`locked` 章节（字节级保留）** 及必要的 **孤立章节** 合并，重跑 Verifier 并产出新的不可变 `Revision`（满足 `SC-11`）。

---

## 5. 生成质量评测基线与度量闭环（`F-26`）

### 5.1 Golden Sample 标准样例与自动化回归评测（`W1` 落地）

在仓库内建立标准化评测资产目录 `testdata/golden/`（不打入生产二进制）：

```text
testdata/golden/
├── fixtures/                         # 脱敏后的代表性 C/C++/Proto/Java/Python 夹具仓库（或子模块快照）
│   ├── cpp_order_service/            # 含 .proto/CROS、多线程锁、错误码、third_party/、*.pb.cc、模拟密钥
│   └── ...
├── samples/                          # 资深架构师针对 5 类内置 DocType 人工编写的标准文档
│   ├── system-architecture.golden.md
│   ├── data-and-contract.golden.md
│   ├── ops-and-config.golden.md
│   ├── code-walkthrough.golden.md
│   └── agents-context.golden.md
└── expectations/                     # 每个夹具的人工标注真值清单 (ground_truth.json)
    └── cpp_order_service.truth.json  # 标注预期符号、.proto RPC、并发原语、应过滤噪音与应脱敏密钥
```

**自动化回归评测脚本（`make eval-baseline`）**：
- **确定性 L1 评测（零 LLM，纳入 CI）**：对 `fixtures/` 运行 L1 抽取器并与 `ground_truth.json` 逐类比对，自动断言 `F-04` 全部指标（`.proto`/构建/目录 100%，C/C++ 声明召回 ≥ 95% / 精确 ≥ 98%，并发原语召回 ≥ 90%，注入明文密钥 0 残留）。
- **端到端槽位与门禁评测（配合内网 LLM 或录制回放 Mock）**：校验 5 类内置 `DocType` 的必填槽位覆盖率严格为 **100%**、Verifier 零阻断错误、以及与 Golden Sample 的结构化章节覆盖重合度。

### 5.2 在线质量度量指标模型（`W2` 落地）

所有指标持久化于 `archdoc_quality_metrics` 并结合 `archdoc_llm_usage_logs` 聚合，**全部支持按 `(doc_type_slug, doc_type_revision_id)` 纵向对比**，量化评估每次模板/Prompt 调优的真实收益：

| 指标名称 | 采集时机 | 精确计算公式 |
| :--- | :--- | :--- |
| **草稿→定稿章节级修改比例 (`edit_diff_ratio`)** | 文档首次审批发布（`draft -> approved`）时计算 | 基于首版 AI 生成修订（`source = 'ai_generate'`）与审批定稿修订的逐章节字符级编辑距离（Levenshtein / 行级 Myers Diff）：`changed_chars / max(draft_chars, approved_chars, 1)`，同时记录每个 `slot_id` 的分项修改率 |
| **Chat 修订采纳率 (`chat_accept_rate`)** | 每次 Chat Diff 被 `Accept` 或 `Reject` / 过期时更新 | `accepted_proposals / total_proposals`（按 `doc_type_revision_id` 聚合） |
| **Verifier 首次拦截率 (`verifier_block_rate`)** | 每次 AI 生成完成执行 Verifier 时记录 | `blocked_or_error_runs / total_ai_runs`（拆分符号未验证、Mermaid 错误、凭据拦截三类明细） |
| **必填槽位完备度 (`slot_coverage_rate`)** | 每次 Verifier 校验记录 | `completed_required_slots / total_required_slots` |
| **单篇平均 Token 成本 (`avg_tokens_per_doc`)** | 关联 `archdoc_llm_usage_logs` 聚合 | `SUM(prompt_tokens + completion_tokens) / COUNT(DISTINCT document_id)`，并附带 `avg_cache_hit_ratio = SUM(cached_prompt_tokens) / SUM(prompt_tokens)` |
