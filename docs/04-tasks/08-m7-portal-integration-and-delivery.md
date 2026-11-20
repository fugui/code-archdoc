# Milestone 7 作战卡：CodeBench 门户集成、全量回归与生产单二进制交付

- **文档编号**：`08-m7-portal-integration-and-delivery.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**最终集成交付（W2 收口与生产就绪）**
- **前置依赖**：`M5` 与 `M6` 全部 AC 通过；[`docs/06-ops/01-verification-and-deployment-guide.md`](../06-ops/01-verification-and-deployment-guide.md)、[`ADR-01`](../05-adr/01-microfrontend-and-table-prefix-isolation.md) ~ [`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md)
- **覆盖特性与场景**：`D-01`~`D-04` 联调收口；全量 P0 验收场景 **`SC-01`~`SC-13`**（除 P1 `SC-03`）

---

## 1. 实施目标与工程边界

完成 `code-archdoc` 与宿主门户 `code-bench` 的端到端集成联调、全量自动化回归门禁验证与运维交付文档更新，达成生产可上线标准：
1. 完成 `code-bench` 网关配置挂载（`/archdoc/* -> 127.0.0.1:8100`）与 `docs.path` 符号链接挂载联调（`D-01`~`D-03` 实机贯通）；
2. 补齐并运行跨全模块的端到端集成回归套件 `handlers/e2e_scenarios_test.go`，确保 `SC-01`、`SC-02`、`SC-04`~`SC-13` 100% 自动化固化进 CI；
3. 更新 `docs/06-ops/01-verification-and-deployment-guide.md` 与 `config.yaml.example`，明确生产环境变量（`ARCHDOC_ENCRYPTION_KEY`、`JWT_SECRET`）、符号链接挂载运维步骤、数据目录备份与故障排查手册；
4. 执行全套发布前门禁验证（`make lint-arch`、`make eval-baseline`、`TEST_DB_DSN=... make test`、`make lint`、`make build`），清理构建临时产物与测试进程并完成最终交付。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及工程 / 文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M7-01`** | 编写全量 P0 场景端到端回归套件 | `handlers/e2e_scenarios_test.go` | 在单个隔离 DB + `t.TempDir()` + 本地 bare Git 夹具 + `httptest` Mock LLM 环境下，串行验证 `SC-01`、`SC-02`、`SC-04`~`SC-13` 全部 12 个 P0 场景，并断言退出后 `sandbox_dir`/进程组无残留 | `M5`, `M6` |
| **`T-M7-02`** | `code-bench` 网关与符号链接挂载联调 (`D-01`~`D-03`) | `../code-bench/config.yaml.example`<br>`docs/06-ops/01-verification-and-deployment-guide.md` | 核验 `code-bench` `gateways.archdoc` 配置（`url: "http://127.0.0.1:8100"`）与 `ln -s <publish_dir> <docs.path>/archdoc` 挂载；验证门户嵌入态加载 `/archdoc/assets/remoteEntry.js`、JWT 透传、Frontmatter 元信息栏展示、以及从 `code-bench` 文档点击 `/archdoc/docs/:id/evidence?...` 直达只读源码抽屉 | `T-M7-01` |
| **`T-M7-03`** | 完善运维部署手册、配置模板与优雅停机核验 | `docs/06-ops/01-verification-and-deployment-guide.md`<br>`config.yaml.example`<br>`main.go` | 补全 `storage`/`security`/`scheduler`/`drift` 生产推荐参数、`ARCHDOC_ENCRYPTION_KEY` 密钥生成与轮换说明、`CGO_ENABLED=1` Linux 构建要求、同机符号链接权限约束、以及 `SIGINT`/`SIGTERM` 优雅停机（取消运行中任务清理 `git` 进程组 + flush `commonAudit`）验证 | `T-M7-02` |
| **`T-M7-04`** | 全量质量门禁自检、产物与端口卫生清理 | 全仓检视与构建验证 | 执行自检（错误处理无 `_ = err`、无 `models.DB` 越界、无硬编码颜色、无 `any`、无残留测试进程占 `:8100`/`:5182`），运行全套构建与测试门禁并清理临时产物 | `T-M7-03` |

---

## 3. 全量 P0 验收场景最终核验矩阵 (`SC-01`~`SC-13`)

| 场景 | 描述 | 自动化覆盖落点 | 状态 |
| :-: | :--- | :--- | :-: |
| **SC-01** | 免密探针与多级 RBAC 越权拦截 (`200`/`401`/`403`) | `handlers/rbac_repo_llm_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-02** | C/C++ Code IR 抽取精度与多 `DocType` 缓存/术语表复用 | `pkg/codeir/codeir_eval_test.go` + `handlers/task_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-03** | *(P1 过时旧文档 Gap 诊断 — 本期预留钩子 `include_legacy_docs`)* | P1 范围 | — |
| **SC-04** | Verifier 防幻觉、Mermaid 门禁与明文凭据 `422` 阻断 | `pkg/verifier/verifier_test.go` + `handlers/document_publish_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-05** | SSE Chat 局部修订、Diff 应用与章节锁定重生成保护 | `handlers/chat_refine_test.go` + `services/revision_service_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-06** | 子目录非法参数 `400` 与源码预览路径/符号链接穿越 `403` | `pkg/sandbox/sandbox_test.go` + `handlers/document_publish_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-07** | 定稿原子发布、`code-bench` 挂载、撤回与远端代码漂移感知 | `pkg/publisher/publisher_test.go` + `services/poller_and_drift_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-08** | `DocType` Web CRUD、非法名 `400`、被引用删除 `409` 与 `Spec Drift` 联动 | `handlers/admin_doctype_test.go` + `pkg/drift/drift_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-09** | 三层降噪、配置脱敏（0 残留）与首次 LLM 调用前预算熔断 | `pkg/sandbox/sandbox_test.go` + `pkg/llm/llm_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-10** | 手工直编不可变修订、默认 ADR 锁定与撤回双向不可见 | `services/revision_service_test.go` + `handlers/document_publish_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-11** | 章节级证据符号指纹漂移定位与非锁章节增量刷新 | `pkg/drift/drift_test.go` + `services/poller_and_drift_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-12** | Monorepo 子目录负责人精确权限边界（跨子目录/整仓返 `403`） | `handlers/rbac_repo_llm_test.go` + `e2e_scenarios_test.go` | ✅ |
| **SC-13** | 不可信源码提示词注入隔离（随机 `nonce` 数据栅栏 + Verifier 兜底） | `pkg/llm/llm_test.go` + `e2e_scenarios_test.go` | ✅ |

---

## 4. 最终上线交付自动化验收命令 (Final Acceptance Criteria)

- [x] **AC-M7-01**：`make lint-arch` 通过（0 架构防腐违规）。
- [x] **AC-M7-02**：`make eval-baseline` 通过（C/C++/Proto + Java + Python 准确率与 0 密钥残留全部达标）。
- [x] **AC-M7-03**：`TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=<PWD> dbname=postgres sslmode=disable" make test` 全量通过（覆盖全部 12 个 P0 `SC` 场景）。
- [x] **AC-M7-04**：`cd ../code-common/backend && go test ./...`、`cd ../code-shield && go test ./...`、`cd ../code-bench/frontend && npm run lint && npm run build` 均 100% 通过。
- [x] **AC-M7-05**：`make lint && make build && make clean` 零错误完成单二进制打包与清理；确认 `:8100` 与 `:5182` 无残留后台进程。
