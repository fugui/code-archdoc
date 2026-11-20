# Milestone 8 作战卡：一层模块发现与迁移、可降级工作区读写锁与批量生成

- **文档编号**：`09-m8-module-discovery-batch-and-worktree-lock.md`
- **归属阶段**：ADR-04 演进里程碑 M8（前置：M7 完成）
- **覆盖特性与场景**：`F-28`、`F-29`、`SC-14`、`SC-15`、`SC-16`（并发锁部分）、`T-ADR04-01`、`T-ADR04-02`、`T-ADR04-04`、`T-ADR04-10`、`T-ADR04-11`、`T-ADR04-12`

---

## 1. 任务拆解

1. **模型与配置演进（16 → 18 表，+2 列）**：
   - 新增 `models/module.go`：`RepoModulePolicy` (`archdoc_repo_module_policies`) 与 `RepoModule` (`archdoc_repo_modules`，严禁含 `OwnerID`)；
   - `models/snapshot.go` 新增 `BaseSnapshotID *uint`；`models/task.go` 新增 `ParentTaskID *uint`；
   - `models/enums.go` 新增 `TaskKindBatch`、`FacetModuleContext`、`SymbolKindModuleDoc`、`ModuleStrategy*`、`ModuleStatus*` 及批次状态机转移校验；
   - `models/json_types.go` 新增 `SnapshotStats.DegradedReason`、`BudgetAdvice.ModuleBreakdown`、`EvidenceAnchor.Scope`、`CitedAnchorCandidate.Scope`；
   - `config/config.go` 新增 `ExtractSection` (`max_repo_files`, `max_repo_duration_seconds`) 与 `SchedulerSection.MaxTokensPerBatch`；
   - `Makefile` `lint-arch` 增加 `models/module.go` 禁止 `OwnerID` 与 `handlers/`/`pkg/` 禁止直调 `gitsync.Sync` 检查。
2. **纯函数模块发现（`pkg/codeir/modules.go`）**：
   - 基于 `TreeFacet` 聚合一层目录 `EffectiveFiles` / `TotalLOC`，叠加默认排除名单 + `ExcludeGlobs` + `MinEffectiveFiles`；
   - 实现 `arch_elements` / `top_level_dirs` / `merged` 三种合并策略（`arch_element` 同路径优先且多级路径原样保留）。
3. **可降级工作区读写锁（`services/worktree_lock.go` & `SnapshotService`）**：
   - 实现无间隙 `DowngradableRWMutex`（`Lock` → `DowngradeToRLock` → `RUnlock`）；
   - `SnapshotService` 唯一持有每仓工作区锁；`SourcePreviewService` 与 `DriftOrchestrator` 经 `SnapshotService` 持读/写锁访问工作区。
4. **模块治理服务与覆盖视图升级（`services/module_service.go` & `services/repo_service.go`）**：
   - 实现策略读取/更新、零 LLM 整仓刷新（复用 `EnsureSnapshot("", branch)`）、忽略/恢复状态切换、范围原子迁移（含活跃任务/冲突拦截 `409`、重渲染重发布与旧路径零残留）；
   - `RepoService.buildCoverageRows` 统一融合 `RepoModule(active|missing) ∪ 未刷新仓 ArchElements ∪ DISTINCT(documents.subdirectory)`，输出 `scope_status`（`active`/`missing`/`orphan`）与 `module_source`。
5. **批量生成编排与路由（`services/generation_service.go`、`handlers/module.go`、`handlers/task.go`）**：
   - 实现 `POST /api/tasks/generate-batch`：全量范围前置 `CheckScopeWritePermission`（任一无权 `403` + `forbidden_scopes` 且零落库）；
   - 批次协调器不占 `TaskScheduler` 槽位，前置汇总批次预估 Token，超 `max_tokens_per_batch` 直接失败返回 `budget_advice.module_breakdown`（0 LLM 请求）；
   - 支持 `include_repo_scope=true` 等待模块子任务全部终态后再入队整仓子任务，以及级联取消 `CancelTask`。

## 2. TDD 先行测试用例清单

| 编号 | 测试名称 | 核心断言 |
| :--- | :--- | :--- |
| **T-ADR04-01** | `TestDiscoverModulesMergedStrategy` | `order/`、`pay/`、`svc/order` 入选；`docs/`、`third_party/` 排除 |
| **T-ADR04-02** | `TestCoverageViewExposesOrphanDocumentScope` | 未命中模块索引的 `legacy/` 文档在覆盖视图呈现且 `scope_status="orphan"` |
| **T-ADR04-04** | `TestWorktreeDowngradableRWLockPreventsCommitRace` | 模块 A 抽取持降级读锁期间，并发同步 B 阻塞至 A 释放，A 的 `commit_sha` 与抽取内容严格一致 |
| **T-ADR04-10** | `TestBatchGenerateBudgetPrecheckZeroLLMCalls` | 超 `max_tokens_per_batch` 时整批失败、返回 `module_breakdown`、Mock LLM 请求数 = 0 |
| **T-ADR04-11** | `TestBatchGenerateForbiddenScopeAtomicReject` | 子目录负责人提交含他人模块的批次返回 `403` + `forbidden_scopes`，DB 新增任务数 = 0 |
| **T-ADR04-12** | `TestModuleMigrateRewritesDocsAndRepublishesCleanly` | `order` → `trade/order` 后文档范围/标题更新、新发布文件含新 Frontmatter 且旧路径零残留、冲突返 `409` |

## 3. 验收门禁

```bash
make lint-arch && make test
```
