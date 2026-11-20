# Milestone 10 作战卡：仓库级共享术语表、层级汇总 (`module_digests`)、双源漂移联动、模块导航与前端闭环

- **文档编号**：`11-m10-hierarchical-rollup-drift-and-navigation.md`
- **归属阶段**：ADR-04 演进里程碑 M10（前置：M9 完成）
- **覆盖特性与场景**：`F-31`、`SC-18`、`T-ADR04-03`（术语表共享）、`T-ADR04-09`

---

## 1. 任务拆解

1. **仓库级共享术语表（`services/snapshot_service.go`）**：
   - `EnsureGlossary` 针对切片快照统一解析至 `base_snapshot_id`（`SnapshotLoc{RepoID, Subdir: "", CommitSHA}`）并以基线 IR 调用 `llm.GenerateGlossary`，`singleflight` 键为 `glossary:<base_snapshot_id>`；
   - `HasCachedGlossary` 与 `ReadCachedGlossary` 统一支持基线回溯，同仓 N 模块仅调用 1 次术语表 LLM。
2. **`module_digests` 声明式汇总与 `module_doc` 锚点（`services/generation_service.go`、`pkg/llm/prompt.go`、`pkg/verifier/`）**：
   - 整仓生成（`subdir == ""`）且 `context_inputs` 含 `module_digests` 且仓库存在 `active` 模块时：
     - 按 `repo_ir_facets` 收敛整仓 IR；
     - 确定性装配 `module_digests`（已发布优先 → 最新草稿 → 术语表兜底，≤ 600 字）与跨模块依赖图注入 Prompt；
   - Verifier 识别并对齐 `symbol_kind = "module_doc"` 锚点（记录 `content_fingerprint = content_sha256`）。
3. **双源合并漂移联动（`services/drift_service.go` & `pkg/drift/`）**：
   - 模块文档发布/撤回时，`OnDocumentPublished` / `OnDocumentUnpublished` 比对同仓整仓文档的 `module_doc` 锚点指纹，变化记录 `module_digest_changed`；
   - 模块刷新发生增删/`missing` 时，`OnRepoModulesChanged` 记录 `module_scope_changed`；
   - `EvaluateRepoCodeDrift` 与模块漂移联动按槽位合并写入 `DriftTypeCode`，永不互相覆盖。
4. **发布模块导航与动态 `parent_doc` Frontmatter（`pkg/publisher/` & `services/publish_service.go`）**：
   - 整仓文档发布时确定性追加 `## 模块导航` 节；
   - Frontmatter 注入 `scope_type`（`repo` | `module`）、`module` 与动态深度 `parent_doc`（N 级目录为 N+1 个 `../`）；
   - 仓库级 `README.md` 升级为 `## 整仓文档` + `## 模块文档` 两级结构。
5. **前端微前端闭环（`frontend/src/`）**：
   - 更新 `types/domain.ts`、`types/api.ts`、`api/repos.ts`、`api/tasks.ts`、`api/documents.ts`、`utils/errorMessages.ts`；
   - `RepoCoveragePage` 展示 `scope_status`/`module_source` 徽标、支持模块多选批量生成与单仓「模块治理」抽屉（刷新/策略/忽略恢复/迁移）；
   - `GenerateTaskModal` 支持批次生成模式、子任务进度表与按模块分解预算建议；
   - `SourcePreviewDrawer` 与 `DriftRadarPage` 完整适配 `scope=repo` 深链预览与外部契约/模块摘要漂移徽标。

## 2. TDD 先行测试用例清单

| 编号 | 测试名称 | 核心断言 |
| :--- | :--- | :--- |
| **T-ADR04-03 (Glossary)** | `TestConcurrentFiveModulesSingleGlossaryCall` | 同仓 5 模块并发生成仅触发 1 次术语表 LLM 调用 |
| **T-ADR04-09** | `TestModulePublishTriggersRepoDocModuleDigestDrift` | 模块 `order` 发布新修订更新 `overview_boundaries` → 整仓仅引用 `order` 的章节记 `module_digest_changed`，且 `EvaluateRepoCodeDrift` 不覆盖 |
| **T-M10-01** | `TestPublisherModuleNavigationAndDynamicParentDoc` | 整仓发布含 `## 模块导航` 与两级 `README.md`；1 级与 2 级模块 `parent_doc` 相对层级准确 |

## 3. 验收门禁

```bash
make lint-arch && make eval-baseline && make test && make lint && make build && make clean
```
