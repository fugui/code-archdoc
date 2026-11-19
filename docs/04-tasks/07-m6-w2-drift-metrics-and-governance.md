# Milestone 6 作战卡：远端巡检、章节级双维漂移雷达、质量度量看板与资源调度治理

- **文档编号**：`07-m6-w2-drift-metrics-and-governance.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**W2（P0 全量能力补齐 — 与 M5 正交并行）**
- **前置依赖**：**W1 Exit Gate (`M1`~`M4`) 100% 通过**；[`docs/02-design/02-extraction-and-generation-engine.md`](../02-design/02-extraction-and-generation-engine.md) §5、[`03-doctype-drift-and-publishing.md`](../02-design/03-doctype-drift-and-publishing.md) §4~§5、[`04-workbench-and-security.md`](../02-design/04-workbench-and-security.md) §2.3
- **覆盖特性与场景**：`F-25`, `F-12`, `F-26`(W2 度量看板), `F-27`；`SC-07`(W2), `SC-08`(Spec Drift 联动), `SC-11`

---

## 1. 实施目标与工程边界

补齐 P0 运维保鲜、质量闭环与规模化运行治理能力：
1. **远端变更巡检调度器（`F-25`）**：后台定时仅对含已发布文档的活跃仓库执行有界并发、零对象克隆的 `gitsync.LsRemoteHead`，支持失败指数退避（`15m -> 30m -> 1h -> 2h -> 4h -> 6h`）与管理员手动触发；
2. **章节级双维漂移雷达与增量刷新（`F-12`）**：实现两阶段 `Code Drift`（`DiffNameStatus` + 仅解析触及证据锚点的变更文件比对 `content_fingerprint`，零行号误报）与槽位级 `Spec Drift`（比对已发布修订与最新 `DocTypeRevision`），并支持单篇文档仅刷新受影响非锁章节；
3. **在线质量度量看板（`F-26` W2）**：落地草稿→定稿章节级修改率、Chat 采纳率、Verifier 拦截率、必填槽位完备度与 Token/KV Cache 命中率聚合查询，支持按 `DocTypeRevision` 纵向趋势对比与槽位修改率热力表；
4. **沙箱回收与双优先级资源调度（`F-27`）**：实现 `ActiveLeaseManager` 租约保护下的 `sandbox_dir`/`ir_cache` 双级 LRU 配额回收，以及 `High(Interactive):Low(Batch) = 4:1` 防饥饿调度队列、单用户限流与全局 LLM 并发信号量。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M6-01`** | 实现 `pkg/drift` 双维漂移纯计算引擎 (`F-12`) | `pkg/drift/{code_drift,spec_drift}.go` | `DetectCodeDrift`：快速路径比较 SHA → 比对 `ChangedFiles` 过滤未触及文件 → 仅对触及文件读取新版 blob 运行单文件 L1 提取器，按 `fq_symbol` + `content_fingerprint` 精准区分 `file_removed_or_renamed`/`symbol_removed`/`symbol_modified`，纯行号平移判定无漂移（`SC-11`）；`DetectSpecDrift`：按 `slot_id` 集合差与 `prompt_hash` 计算 `added_slots`/`removed_slots`/`modified_slots` | `W1 Gate` |
| **`T-M6-02`** | 实现 `services.RemoteHeadPoller`、`DriftOrchestrator` 与漂移/增量刷新 API (`F-25`/`F-12`) | `services/{poller_service,drift_service}.go`<br>`handlers/drift.go` | `RemoteHeadPoller`：按 `drift.poll_interval_minutes`（默认 15m）调度，独立并发信号量（默认 4），仅巡检有 `approved` 文档的活跃仓，失败指数退避不阻塞其他仓；`DriftOrchestrator`：衔接 `F-25` 与 `DocType` 变更事件实时刷新 `archdoc_document_drifts`；实现增量刷新（取 `Code Drift ∪ Spec Drift \ LockedSlots` 仅重生成目标槽位，未受影响章节与 `locked` 章节字节不变）；交付 `GET /api/drift/summary`、`GET /api/drift/documents`、`POST /api/documents/:id/incremental-refresh`、`POST /api/admin/drift/poll` | `T-M6-01` |
| **`T-M6-03`** | 实现 `services.ResourceReaper` 与 `TaskScheduler` 双优先级调度器 (`F-27`) | `services/{lease_manager,reaper_service,scheduler_service}.go`<br>`handlers/admin_storage.go` | `ActiveLeaseManager` 维护 `repo_id`/`snapshot_id` 活跃引用计数；`ResourceReaper` 按 90%→75% 水位清理无租约的 `sandbox_dir` 与分级清理 `ir_cache`（先清理未发布旧草稿分片，仍超限时再按 LRU 清理并标 `evicted_at`，回收前先 `os.Remove`）；升级 `TaskScheduler` 为 `HighQ(Interactive)` / `LowQ(Batch)` 4:1 防饥饿调度 + 单用户并发限流 + 全局 LLM 并发信号量（队列满返 `429`，入队任务排队不失败）；交付 `GET /api/admin/storage/stats`、`POST /api/admin/storage/reap` | `W1 Gate` |
| **`T-M6-04`** | 实现 `services.QualityMetricsService` 与质量度量 API (`F-26` W2) | `services/metrics_service.go`<br>`handlers/metrics.go` | 完善发布时章节级 Myers/Levenshtein `edit_diff_ratio` 计算；聚合 `archdoc_quality_metrics` 与 `archdoc_llm_usage_logs`（含 `avg_cache_hit_ratio`）；交付 `GET /api/metrics/overview`、`GET /api/metrics/revisions-compare`、`GET /api/metrics/slot-heatmap` | `W1 Gate` |
| **`T-M6-05`** | 实现前端 `DriftRadarPage`、`QualityMetricsPage` 与存储成本观测卡片 | `frontend/src/pages/{DriftRadarPage,QualityMetricsPage}.tsx`<br>`frontend/src/components/metrics/RevisionTrendChart.tsx`<br>`frontend/src/pages/AdminLLMConfigPage.tsx`(增补成本卡片)<br>`frontend/src/menu.ts` & `App.tsx` | 接入 `/drift`、`/metrics` 菜单与路由；`DriftRadarPage`（汇总 KPI、URL 同步筛选与 5 页分页、展开受影响槽位与变更符号明细、负责人一键增量刷新、管理员手动触发巡检）；`QualityMetricsPage`（KPI 卡片、原生轻量 SVG 修订趋势图严格用 `--color-*` Token、槽位修改率热力表）；`AdminLLMConfigPage` 集成 W2 Token 成本与磁盘配额展示卡片 | `T-M6-02`<br>`T-M6-03`<br>`T-M6-04` |

---

## 3. TDD 先行单元测试用例清单 (Test-First Specification)

### 3.1 `pkg/drift/drift_test.go` (`F-12`, `SC-07`, `SC-08`, `SC-11`)
1. `TestCodeDriftZeroFalsePositiveOnLineShift`（`SC-11`）：
   - 已发布文档第 1 章锚定 `OrderService::Create`，第 3 章锚定 `OrderService::Pay`（位于同一 `.cpp` 文件）；
   - 新提交仅在文件顶部插入注释（两函数行号均下移 10 行）并修改 `OrderService::Pay` 函数体；
   - 断言 **仅第 3 章** 被标为 `Code Drift (reason="symbol_modified", changed_symbols=["order::OrderService::Pay"])`，第 1 章漂移为 0！
2. `TestCodeDriftNoNewCommitReturnsZero`：`publishedCommitSHA == remoteHeadSHA` 时直接返回 `has_drift=false`，零文件解析。
3. `TestSpecDriftDetectsAddedModifiedRemovedSlots`（`SC-08`）：对比新旧两版 `DocTypeRevision`，准确输出新增/修改/删除的 `slot_id` 集合；无模板改动时返回 `has_drift=false`。

### 3.2 `services/poller_and_drift_test.go` (`F-25`, `F-12`, `SC-11`, 基于 `testdb.SetupIsolatedDB`)
1. `TestRemoteHeadPollerBackoffDoesNotBlockHealthyRepos`（`F-25`）：构造 1 个无效远端仓 + 2 个本地 bare 夹具仓，运行巡检后断言无效仓 `consecutive_failures == 1` 且设置退避 `next_retry_at`，同时 2 个健康仓正常更新 `remote_head_sha` 并产生预期 `Code Drift`；未含已发布文档的仓库不被巡检。
2. `TestIncrementalRefreshOnlyRegeneratesAffectedNonLockedSlots`（`SC-11`）：文档共 5 槽位，第 2 槽位 `locked` 且受漂移影响、第 3 槽位 `auto` 且受漂移影响、第 1/4/5 槽位未受影响；执行 `POST /api/documents/:id/incremental-refresh` 完成后，断言仅第 3 槽位调用了 Mock LLM，第 1/2/4/5 槽位 `content_sha256` 字节级不变且第 2 槽位标记 `needs_drift_review=true`。

### 3.3 `services/reaper_and_scheduler_test.go` (`F-27`, 基于 `testdb.SetupIsolatedDB`)
1. `TestResourceReaperProtectsActiveLeasesAndEnforcesQuota`：设置极小配额阈值，持有一个活跃快照租约 + 多个非活跃旧草稿/已发布分片；触发 `Reap()` 后断言活跃租约目录与分片**完好无损**，非活跃旧草稿分片优先被 `os.Remove` 并标 `evicted_at`，磁盘占用降至低水位以下。
2. `TestSchedulerInteractivePreemptsBatchAndQueuesOverConcurrency`：设置 `max_concurrency = 2`；先塞入 10 个 `batch` 低优任务占满执行槽，再提交 2 个 `interactive` 高优任务，断言高优任务在下一空闲槽**优先于剩余 8 个批量任务**执行，且所有入队任务全部排队完成无一失败。

### 3.4 `handlers/metrics_test.go` (`F-26`, 基于 `testdb.SetupIsolatedDB`)
1. `TestQualityMetricsCompareAcrossDocTypeRevisions`：写入同一 `DocType` Rev 1（高修改率 0.40、低采纳率 0.50）与 Rev 2（低修改率 0.12、高采纳率 0.85）的发布与 Chat 指标及 `LLMUsageLog`（含 `cached_prompt_tokens`），断言 `/api/metrics/overview`、`/api/metrics/revisions-compare`、`/api/metrics/slot-heatmap` 聚合结果小数精度与分组完全准确。

---

## 4. 自动化验收标准与执行命令 (Acceptance Criteria)

- [ ] **AC-M6-01**：`TEST_DB_DSN=... make test` 与 `make lint-arch` 全绿，`SC-07`(W2)、`SC-08`(Spec Drift)、`SC-11`、`F-25`/`F-26`/`F-27` 全部测试通过。
- [ ] **AC-M6-02**：`make lint && make build` 零报错通过，`DriftRadarPage` 与 `QualityMetricsPage` 满足 URL 分页（默认 25 条/页）、原生 SVG 双主题与零 `any` 规范。
- [ ] **AC-M6-03**：优雅停机（`OnShutdown`）测试确认后台 `RemoteHeadPoller`、`ResourceReaper` 与 `TaskScheduler` 协程全部在 `ctx` 取消后干净退出，无 goroutine 泄漏。
