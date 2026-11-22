# Milestone 13 作战卡：单仓目标分支切换与远端分支发现（`M13` / `ADR-07`）

- **文档编号**：`14-m13-single-repo-branch-switch.md`
- **归属阶段**：ADR-07 演进里程碑 M13（前置：M12 已完成）
- **覆盖特性与场景**：`KA-07`、`F-34`、`SC-21`、`T-ADR07-01` ~ `T-ADR07-12`
- **上游基线**：[`ADR-07`](../05-adr/07-single-repo-branch-switch.md)、[`01-feature-matrix.md`](../01-features/01-feature-matrix.md)、[`05-database-schema.md`](../02-design/05-database-schema.md)、[`01-http-api-contract.md`](../03-api/01-http-api-contract.md)、[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)

---

## 1. 交付范围与分阶段结构

1. **`code-common/backend/gitsync` 通用底座升级**：
   - 分支名白名单安全校验 `ValidateBranchName`；
   - 零克隆远端分支枚举 `ListRemoteBranches`（含 `HEAD` 符号引用识别）；
   - `ExecGitSyncWithOptions` 改用显式 refspec 拉取，彻底解决 `--depth=1 --single-branch` 浅克隆工作区切换其他分支失败问题。
2. **`code-archdoc` 后端领域模型、服务与 API**：
   - `archdoc_repo_module_policies` 新增 `target_branch` 列与 `ResolveEffectiveBranch` / `NormalizeRepoDefaultBranch` 统一解析；
   - 新增 `services/branch_service.go`（30s TTL 缓存、活跃任务阻断、远端分支校验、事务内文档无损重绑 `RebindRepoBranch`、远端 HEAD 替换与即时代码漂移对齐）；
   - 消除 `RepoService`、`ModuleService`、`GenerationOrchestrator`、`DriftStore.ListPollableRepos`、`DriftOrchestrator` 中的 `repo.Branch` 隐式直读，全链路消费有效目标分支；
   - 新增 `GET /api/repos/:repo_id/branches` 与 `PUT /api/repos/:repo_id/branch` 接口、仓库级 `RepoWrite` 守卫与 `P1` 审计日志。
3. **`code-archdoc` 前端体验闭环**：
   - 新增 `src/utils/branchUtils.ts` 纯函数与复用组件 `RepoBranchSelector`；
   - 在 `GenerateTaskModal`（核心触发弹窗）、`ModuleGovernanceModal`（模块治理弹窗）与 `ScopeIdentityCell`（覆盖列表行徽标）集成交互与状态展示，并在各页面透传仓库级分支与切分支权限上下文。

---

## 2. TDD 测试与验收清单 (`T-ADR07-01` ~ `T-ADR07-12`)

| 用例编号 | 归属层 | 验证场景与核心断言 |
| :--- | :---: | :--- |
| **`T-ADR07-01`** | `code-common/gitsync` | `ValidateBranchName` 阻断 `-flag`、`..`、`.lock`、`@{`、反斜杠、空白及超长字符，放行 `main`、`develop`、`release/v1.2.3`、`feat_abc` 等合法分支名。 |
| **`T-ADR07-02`** | `code-common/gitsync` | `ListRemoteBranches` 对本地 bare 多分支仓执行 `ls-remote --symref`，准确识别 `IsHead` 置顶排序并解析 40 位 SHA，输出无敏感信息泄漏。 |
| **`T-ADR07-03`** | `code-common/gitsync` | `Sync` 以 `Depth: 1` 浅克隆 `main` 后，同路径切至 `develop` 再切回 `main`，文件树与 `CommitSHA` 100% 匹配对应分支最新提交。 |
| **`T-ADR07-04`** | `handlers/branch_switch_test.go` | `GET /api/repos/:repo_id/branches` 返回 `default_branch`、`current_branch`、`is_overridden` 及排序列表；验证 30s 缓存命中与 `?refresh=true` 强制刷新。 |
| **`T-ADR07-05`** | `handlers/branch_switch_test.go` | `PUT /api/repos/:repo_id/branch` 成功切换分支：`archdoc_repo_module_policies.target_branch` 持久化、共享表 `repositories.branch` 零修改、存量 `archdoc_documents.ref_name` 无损重绑（保留修订历史与 `locked` 槽位）、`archdoc_repo_remote_heads` 清除旧分支并写入新 SHA。 |
| **`T-ADR07-06`** | `handlers/branch_switch_test.go` | 已发布文档切换至不同提交的新分支后即时标记 `has_drift=true` (`repo_level`)；切换至指向相同提交的分支时自动清零代码漂移。 |
| **`T-ADR07-07`** | `handlers/branch_switch_test.go` | RBAC 越权防护：普通用户与**仅子目录负责人**调用 `PUT /api/repos/:repo_id/branch` 返回 `403 FORBIDDEN`；仓库 `Owner` 与 `archdoc_admin` 成功执行并记录 `P1` 审计日志。 |
| **`T-ADR07-08`** | `handlers/branch_switch_test.go` | 安全与并发护栏：仓库存在运行中生成任务时返回 `409 CONFLICT`；切换远端不存在分支返回 `400 INVALID_PARAM` 且数据库零改动；非法分支名 `-bad` 返回 `400 INVALID_PARAM`。 |
| **`T-ADR07-09`** | `handlers/branch_switch_test.go` | 切回默认分支（`branch: ""` 或等于 `default_branch`）后，`target_branch` 清空、`is_branch_overridden` 归为 `false`。 |
| **`T-ADR07-10`** | `handlers/branch_switch_test.go` | 全链路有效分支联动：切换至 `develop` 后，`GET /api/repos`、`GET /api/repos/:id/scopes`、`POST /api/tasks/generate`（`ref_name` 留空）与 `DriftStore.ListPollableRepos` 全部生效使用 `develop`；向生成接口传不匹配 `ref_name` 返回 `400 INVALID_PARAM`。 |
| **`T-ADR07-11`** | `handlers/branch_switch_test.go` | 切换至仅在 `develop` 新增一级目录 `payments/` 的提交后，触发 `POST /api/repos/:id/modules/refresh` 准确发现 `payments` 模块。 |
| **`T-ADR07-12`** | `frontend` (`node --test`) | `branchUtils.ts` 纯函数单测：关键词过滤、合法性校验、分支徽标文案与确认弹窗内容格式化全部通过。 |

---

## 3. 质量门禁

1. `code-common/backend`：`go fmt ./gitsync/... && go vet ./gitsync/... && go test -v ./gitsync/...` 100% 通过；
2. `code-shield`：`go test ./...` 零回归；
3. `code-archdoc`：`make lint-arch` 零违规；`TEST_DB_DSN="..." make test` 全量物理隔离测试 100% 通过；`frontend` `npm test` + `make lint` 零报错；`make build` 成功产出单二进制并 `make clean`；
4. 代码自检无误后推送至 `code-common` 与 `code-archdoc` 远端 GitHub 仓库。
