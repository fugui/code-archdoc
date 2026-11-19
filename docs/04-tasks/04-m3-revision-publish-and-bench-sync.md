# Milestone 3 作战卡：手工直编、章节锁定/孤立保护、定稿原子发布与 `code-bench` 挂载协同

- **文档编号**：`04-m3-revision-publish-and-bench-sync.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**W1（最小可用纵向闭环 — 第 3 站）**
- **前置依赖**：`M2` 全部 AC 通过；[`docs/02-design/01-system-architecture.md`](../02-design/01-system-architecture.md) §3.2、[`03-doctype-drift-and-publishing.md`](../02-design/03-doctype-drift-and-publishing.md) §2~§3、[`04-workbench-and-security.md`](../02-design/04-workbench-and-security.md) §3.1、[`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md)
- **覆盖特性与场景**：`F-08`(后端源码抽屉), `F-09`(W1 手工直编), `F-10`, `F-11`, `D-01`~`D-03`；`SC-06`, `SC-07`(W1), `SC-10`

---

## 1. 实施目标与工程边界

完成 W1 后端的「**人机审校修订 → 锁定/孤立保护 → 原子发布与导出**」完整闭环，并交付 `code-bench` 侧的通用文档增强改造（`D-01`~`D-03`）：
1. 交付 `services.RevisionService`：统一单槽位编辑管线（乐观锁 `409`、非目标章节字节级不变、重跑 Verifier、不可变修订追加）、任意版本章节级 Diff、一键回滚、`auto`/`hybrid`/`locked` 重生成合并保护与模板升级孤立章节 (`is_orphan=true`) 处置；
2. 交付文档浏览与只读源码证据预览 API（严格落实普通用户草稿不可见隔离、`SafeJoin` 越权 `403` 与沙箱缺失按 `commit_sha` 自动重拉）；
3. 交付 `pkg/publisher` 与 `services.PublishService`：发布门禁 (`422 VERIFIER_BLOCKED`)、YAML Frontmatter 渲染、平台深链转换、跨文档未发布相对链接降级、`.staging/` 原子落盘与 `README.md` 重建、一键撤回空目录清理、单篇 Markdown 与防 Zip-Slip ZIP 导出；
4. 交付 `code-bench` 协同改造（`D-02` 通用 Frontmatter 隐藏与元信息栏 + `D-03` 已挂载模块前缀走门户 SPA 路由），零 `archdoc` 专有逻辑耦合，附带 `code-bench` 前端单测零回归保障。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及工程 / 文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M3-01`** | 实现 `services.RevisionService` 双轨统一修订内核与锁定/孤立合并引擎 (`F-09`/`F-10`) | `services/revision_service.go`<br>`pkg/drift/section_diff.go` | 实现 `ApplySectionChange`（校验 `base_revision_no` 乐观锁、仅改目标 `slot_id`、其余章节字节级复制、同步重跑 `pkg/verifier`、原子追加不可变 `DocumentRevision` & `SectionEvidence`）；实现 `MergeGeneratedWithLockedAndOrphans`（重生成豁免 `locked` 并标 `needs_drift_review`；模板删槽位时将 `locked`/`manually_edited` 章节挂载至文末 `is_orphan=true` 区块）；实现 `ResolveOrphanSection`（`keep_locked`/`merge_into`/`delete`）、`RollbackRevision` 与两版本章节 Unified Diff | `M2` |
| **`T-M3-02`** | 实现文档审校与只读源码证据预览 API (`F-08`/`F-09`/`F-10`) | `services/source_preview_service.go`<br>`handlers/document.go` | 交付 `GET /api/documents`、`GET /api/documents/:id`（普通用户强制看 `published_revision_id`，草稿返 `403`）、`GET /api/documents/:id/revisions` & `/diff`、`PUT /api/documents/:id/sections/:slot_id`、`PATCH .../ownership`、`POST .../orphan-resolve`、`POST /api/documents/:id/rollback`、`GET /api/documents/:id/source-preview`（经 `SafeJoin` 防穿越 `403`，支持按 `commit` 读取或重拉、按 `symbol` 重定位）；全量写操作接入 `commonAudit` | `T-M3-01` |
| **`T-M3-03`** | 实现 `pkg/publisher` 渲染、原子落盘与防 Zip-Slip 导出 (`F-11`) | `pkg/publisher/{renderer,link_checker,fs_sink,zip_export}.go` | 定义 `PublishSink` 接口（为 P1 跨机部署解耦）与 `LocalFSPublishSink`；注入 `ADR-03` §3.2 标准 YAML Frontmatter；将证据锚点渲染为 `/archdoc/docs/:id/evidence?...` 深链；校验跨文档相对链接并自动降级未发布目标；`SanitizePublishSegment` 防 `.` / `_` 前缀误杀；`.staging/tmp-<uuid>.md` + `fsync` + 原子 `rename` 落盘与 `README.md` 重建；撤回删文件并自底向上清空气目录；`SanitizeZipEntryPath` 防 Zip-Slip 打包 | `M2` |
| **`T-M3-04`** | 实现 `services.PublishService` 与发布/撤回/导出 API (`F-11`) | `services/publish_service.go`<br>`handlers/publish.go` | 交付 `POST /api/documents/:id/publish`（校验 `can_approve==true` 否则返 `422`；持仓库发布路径互斥锁落盘；计算首版 AI 草稿→定稿章节级 `edit_diff_ratio` 写入 `archdoc_quality_metrics`）、`POST /api/documents/:id/unpublish`、`GET /api/documents/:id/export`、`POST /api/exports/zip`；`P0` 级 `commonAudit` 埋点 | `T-M3-02`<br>`T-M3-03` |
| **`T-M3-05`** | 实施 `code-bench` 通用协同改造 (`D-02` Frontmatter + `D-03` 平台深链) | `../code-bench/frontend/src/utils/frontmatter.ts`<br>`../code-bench/frontend/src/utils/frontmatter.test.ts`<br>`../code-bench/frontend/src/pages/DeveloperDocs.tsx`<br>`../code-bench/frontend/src/pages/DeveloperDocs.css` | `D-02`：通用 YAML Frontmatter 剥离器（仅首行 `---` 且有配对 `---` 生效，非法/无 Frontmatter 原样降级；TOC 基于剥离后正文提取；渲染轻量 `--color-*` 双主题通用元信息栏）；`D-03`：从 `fetchActiveModules()` 提取已挂载模块前缀集合，扩展 `resolveMarkdownUrl` 与 `handleContentClick` 使命中前缀的链接走门户 SPA `navigate(href)`；保持 `/docs/`、`/api/`、相对路径与外链 100% 零回归 | 无（可与 `T-M3-01` 并行） |

---

## 3. TDD 先行单元测试用例清单 (Test-First Specification)

### 3.1 `services/revision_service_test.go` (`F-09`, `F-10`, `SC-10`)
1. `TestManualEditByteLevelIsolationAndOptimisticLock`：修改第 2 槽位后，断言新修订号递增、第 1/3/4/5 槽位 `content_sha256` 逐字节完全不变、Verifier 已重跑；使用旧 `base_revision_no` 再次提交返回 `ErrOptimisticLockConflict`（`409`）。
2. `TestRollbackAppendsImmutableRevision`：从 Rev 3 回滚至 Rev 1，断言生成不可变 Rev 4（`source="rollback"`, `source_ref_id=Rev1.ID`），历史 Rev 1~3 完好无损。
3. `TestRegeneratePreservesLockedAndCreatesOrphanOnSlotRemoval`：
   - 将 `adr_constraints_debt` 设为 `locked` 并写入人工内容，触发新提交重新生成，断言 `locked` 章节原内容字节级保留且 `needs_drift_review` 正确标记，`auto` 章节按新代码刷新；
   - 模拟新 `DocTypeRevision` 删除 `adr_constraints_debt` 槽位后重新生成，断言原被锁内容未被静默丢弃而是以 `is_orphan=true` 挂载于文末孤立区；
   - 分别测试 `orphan-resolve` 的 `keep_locked`、`merge_into`、`delete` 三种处置动作。

### 3.2 `pkg/publisher/publisher_test.go` (`F-11`, `SC-07`, `SC-10`)
1. `TestRenderFrontmatterDeepLinkAndCrossDocDowngrade`：断言产出文档头部含完整 YAML Frontmatter、证据锚点转为合法 `/archdoc/docs/:id/evidence?...` 深链、指向未发布同仓文档的 `./ops-and-config.md` 自动降级为纯文本。
2. `TestAtomicPublishRenameAndUnpublishCleansEmptyDirs`：验证落盘后 `.staging/` 无残留临时文件、`README.md` 正确索引、`_internal` 子目录映射为 `dir_internal` 不被隐藏；撤回最后一篇文档后，仓库 `README.md` 与空子目录被彻底清理干净。
3. `TestZipExportContainsIndexAndBlocksZipSlip`：验证 ZIP 包含 `INDEX.md` 及全部已发布文档，且恶意路径（`../evil.md`、`/abs.md`）被 `SanitizeZipEntryPath` 拦截清洗。

### 3.3 `handlers/document_publish_test.go` (`SC-04`, `SC-06`, `SC-07`, `SC-10`, 基于 `testdb.SetupIsolatedDB`)
1. `TestDraftIsolationForNormalUser`：草稿文档被普通用户访问 `GET /api/documents/:id`、`GET .../revisions`、`GET .../export` 均返回 `403`；发布后普通用户可正常只读访问；负责人再编辑未发布的新草稿 Rev 2 时，普通用户仍严格只看到已发布 Rev 1 内容。
2. `TestSourcePreviewPathEscapeReturns403`（`SC-06`）：传入 `file=../../../etc/passwd` 与仓库内指向外部文件的符号链接，断言均返回 `403 PATH_ESCAPE`。
3. `TestPublishBlockedByVerifierSecretLeak`（`SC-04`）：草稿含明文口令导致 `can_approve=false`，调用 `POST /api/documents/:id/publish` 断言返回 `422 VERIFIER_BLOCKED` 且 `publish_dir` 中无任何文件生成。
4. `TestPublishAndUnpublishEndToEnd`（`SC-07`/`SC-10`）：正常草稿发布后验证 `status=="approved"`、磁盘文件存在、`archdoc_quality_metrics` 记录了 `doc_published` 修改率、`audit_logs` 记录 `publish`；撤回后磁盘文件消失、普通用户再次访问返回 `403`。

### 3.4 `../code-bench/frontend/src/utils/frontmatter.test.ts` (`D-02`, `D-03`)
1. `parseFrontmatter` 正常解析含多字段（含中文引号、冒号时间字符串）YAML Frontmatter 并返回干净 `body`；
2. 无 Frontmatter 或首行非 `---` 的存量文档（含正文内水平分割线 `---`）原样返回 `metadata: null`，正文 100% 不变；
3. `resolveMarkdownUrl`：`/archdoc/docs/42/evidence?commit=abc` 命中 `mountedPrefixes` 返回 `isPortalRoute: true`；`/docs/foo.md`、`/api/docs/raw`、`./rel.md`、`https://example.com` 行为与改造前 100% 一致。

---

## 4. 自动化验收标准与执行命令 (Acceptance Criteria)

- [x] **AC-M3-01**：`TEST_DB_DSN=... make test` 与 `make lint-arch` 100% 通过，`SC-06`、`SC-07`(W1)、`SC-10` 全绿。
- [x] **AC-M3-02**：`cd ../code-bench/frontend && npm run lint && npm run build`（及对应单测）100% 通过，且 `code-bench` 中零 `archdoc` 专有业务分支（遵守 `Non-Goal 8`）。
- [x] **AC-M3-03**：手工或测试脚本将 `publish_dir` 符号链接至本地 `code-bench` 测试文档目录验证时，含 Frontmatter 文档正文不显示 YAML 原文，点击 `/archdoc/...` 深链正确触发路由跳转。
