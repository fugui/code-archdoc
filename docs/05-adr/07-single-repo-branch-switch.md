# ADR-07: 单仓目标分支切换与远端分支发现架构

- **文档编号**：`07-single-repo-branch-switch.md`
- **日期**：2026-11-22
- **状态**：已采纳 (Accepted) —— 已同步更新阶段一~三 SDD 规范与 M13 作战卡，进入 TDD 实施阶段
- **关联议题**：[`01-feature-matrix.md`](../01-features/01-feature-matrix.md) (`KA-07`, `F-01`, `F-02`, `F-12`, `F-25`, `F-28`, `F-34`, `SC-21`, `Non-Goal 4`, `Non-Goal 8`)；[`05-database-schema.md`](../02-design/05-database-schema.md)；[`01-http-api-contract.md`](../03-api/01-http-api-contract.md)；[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)；[`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md)；[`ADR-04`](./04-module-scope-discovery-and-hierarchical-docs.md)

---

## 1. 背景与问题陈述 (Context & Problem Statement)

1. **现实痛点**：`code-bench` 共享表 `repositories.branch` 往往长期停留在 `master`/`main` 默认值，而不少纳管业务仓库的实际活跃研发主线或待反推基线位于 `develop`、`release/*` 或 `main`（与 `repositories.branch` 不一致）。目前 `code-archdoc` 前后端在触发生成弹窗（`GenerateTaskModal`）、模块治理弹窗（`ModuleGovernanceModal`）、覆盖矩阵（`RepoService`）和后台巡检（`RemoteHeadPoller`）中全部硬编码读取 `repositories.branch`（为空回退 `master`），导致用户无法在不修改 `code-bench` 主数据的前提下切换目标分支。
2. **现阶段产品边界（单仓换分支，非多分支并存）**：现阶段仅需要支持**「单仓单活跃目标分支切换」**，即每个仓库在 `code-archdoc` 中同一时刻仅跟踪 1 条活跃目标分支，切换后所有存量文档、后续生成/刷新、模块发现与远端漂移巡检全部切换至新分支，暂不引入同仓多分支并存发布树与多版本基线切换 UI（`F-20` 保留至 P1 演进）。
3. **现存隐藏缺陷**：
   - `code-common/backend/gitsync` 缺少轻量的远端分支枚举能力（仅支持单分支 `LsRemoteHead`）；
   - `gitsync.ExecGitSyncWithOptions` 对已有本地浅克隆工作区（`git clone --depth=1 -b <old_branch>` 生成的 `--single-branch` 配置）执行 `git fetch --all` 时，**无法拉取远端其他分支的 tracking ref**，导致切换分支时 `checkout -B <new_branch> origin/<new_branch>` 直接失败；
   - `RepoService`、`ModuleService`、`GenerationOrchestrator`、`DriftStore.ListPollableRepos` 与 `DriftOrchestrator` 存在各自读取 `repo.Branch` 或忽略分支过滤的问题，必须通过统一解析函数与联动重算消除状态撕裂。

---

## 2. 核心架构决策 (Architectural Decisions)

### 决策 1：自有表 `archdoc_repo_module_policies` 扩展 `target_branch`（零共享表 DDL）
- **决策**：严格遵守 [`ADR-01`](./01-microfrontend-and-table-prefix-isolation.md) 共享表只读红线，**禁止修改共享表 `repositories`**。在自有表 `archdoc_repo_module_policies`（已按 `repo_id` 建有唯一索引）新增列 `target_branch`（`varchar(128), not null, default ''`）。
- **分支解析不变量**：定义统一纯函数 `models.ResolveEffectiveBranch(repoBranch, targetBranch string) string`：
  1. 若 `target_branch` 非空，取 `target_branch`；
  2. 否则若共享表 `repositories.branch` 非空，取 `repositories.branch`；
  3. 否则回退 `"master"`。
- **默认值归一化约定**：当用户选择切换回与规范化默认分支（`NormalizeRepoDefaultBranch(repo.Branch)`）相同的分支或传入空字符串 `""` 时，持久化 `target_branch = ""`（对外表现为 `is_branch_overridden = false`）；仅当选择非默认分支时写入显式分支名（`is_branch_overridden = true`）。
- **向后兼容**：`UpdateTargetBranch` 使用 `OnConflict(repo_id)` 仅更新 `target_branch`、`updated_by`、`updated_at`，绝不覆写既有的 `strategy`、`exclude_globs`、`min_effective_files`。

### 决策 2：单仓单活跃分支不变量与无损重绑语义（`SwitchRepoBranch`）
切换仓库目标分支时执行以下原子化、无损的领域编排：
1. **活跃任务并发阻断**：调用 `TaskRepo.HasActiveTasksInRepo(ctx, repoID)` 检查是否存在处于 `pending | extracting | generating | verifying` 状态的生成/批量/增量刷新任务；存在时直接返回 `409 CONFLICT`，防止生成中途工作树切分支导致快照撕裂。
2. **远端真实性与 HEAD 查询**：在写库之前，持 20 秒超时调用 `gitsync.LsRemoteHead(ctx, repo.URL, requestedBranch)`（零对象克隆）：
   - 分支在远端不存在时，返回 `400 INVALID_PARAM`，数据库零改动；
   - 远端不可达或鉴权失败时，输出经 `gitsync.SanitizeGitOutput` 脱敏后返回 `502 REMOTE_BRANCH_FETCH_FAILED`。
3. **存量文档无损重绑（`DocumentStore.RebindRepoBranch`）**：
   - 在数据库事务内使用 `SELECT ... FOR UPDATE` 锁定该仓全部 `archdoc_documents` 行；
   - 前置检查新 `ref_name` 下 `(subdirectory, doc_type_slug)` 唯一性（防异常历史脏数据碰撞，若冲突则回滚并返回 `409 MIGRATION_CONFLICT`）；
   - 将全部记录的 `ref_name` 更新为 `effectiveBranch`。所有 `DocumentRevision`、`SectionEvidence`（含 `locked` 人工锁定章节）、`ReviewComment`（含 `persist_for_regen` 持久指导）、`ChatSession` 及已落盘 `publish_dir` 文件 100% 原样保留，不丢失人工成果也不破坏 `code-bench` 挂载路径。
4. **远端 HEAD 替换与即时漂移对齐**：
   - `DriftStore.ReplaceRepoRemoteHead` 在同一事务中清理该仓旧 `ref_name != effectiveBranch` 的 `archdoc_repo_remote_heads` 记录并写入新分支 `remoteHeadSHA`（重置退避计数），确保后台 `RemoteHeadPoller` 与前端覆盖列表不再受旧分支退避状态污染；
   - 若存在已发布（`approved`）文档，立即调用 `DriftOrchestrator.SyncCodeDriftAfterBranchSwitch`：
     - 若文档 `published_commit_sha == remoteHeadSHA`，清零 `has_drift`（整仓文档保留模块上卷漂移）；
     - 否则立即写入 `has_drift=true, precision='repo_level', base_commit_sha=published_commit_sha, remote_head_sha=remoteHeadSHA`，让用户直观看到当前已发布文档与新分支最新代码存在差异（后续可通过工作台重新生成或漂移雷达增量刷新对齐，下次巡检也会细化到章节级）。
5. **单分支生成护栏**：`SubmitGenerateTask` 与 `SubmitBatchGenerateTask` 校验 `req.RefName`：留空自动取 `effectiveBranch`；显式传入非空值时必须与 `effectiveBranch` 一致（允许 `effectiveBranch == "master" && req.RefName == "main"` 的历史回退兼容），否则返回 `400 INVALID_PARAM`，阻断并发多分支分裂数据产生。

### 决策 3：通用 `code-common/backend/gitsync` 能力扩展与浅克隆切分支修复（对齐 `Non-Goal 8`）
- **严禁耦合业务**：所有改造均为通用 Git 能力，落位 `code-common/backend/gitsync`，零引入 `archdoc` 概念。
- **安全分支名白名单 `ValidateBranchName`**：
  - 长度 `1..128`，匹配 `^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`；
  - 显式拒绝 `-` 前缀（阻断 Git 参数注入如 `--upload-pack`）、`..`、`//`、`@{`、反斜杠、空白/控制字符、结尾 `/` 或 `.`、以及以 `.lock` 结尾的路径段或以 `.` 开头的路径段。
- **轻量化远端分支列表 `ListRemoteBranches`**：
  - 单次执行 `git ls-remote --symref <repoURL> HEAD refs/heads/*`（零对象下载）；
  - 解析 `ref: refs/heads/<name>\tHEAD` 识别默认 `HEAD` 分支并置顶，其余分支按字典序排列返回 `[]RemoteBranch`（含 40 位 `CommitSHA` 与 `IsHead`）。
- **显式 Refspec 拉取修复浅克隆切分支**：
  - `ExecGitSyncWithOptions` 在已有仓库同步时，当存在 `origin` 远端，改用显式 refspec：
    `git -C <localPath> fetch [--depth=<depth>] origin +refs/heads/<branch>:refs/remotes/origin/<branch>`
  - 当 `branch == "master"` 拉取失败且非权限错误时，自动回退拉取 `+refs/heads/main:refs/remotes/origin/main`；
  - 随后执行 `checkout -B <branch> <targetRef>` + `reset --hard <targetRef>`，确保即便是 `--depth=1 --single-branch` 克隆出来的本地沙箱也能在任意远端分支间无损往返切换。

### 决策 4：权限与缓存边界
- **权限边界（RBAC）**：
  - `GET /api/repos/:repo_id/branches`：允许任意已登录用户（`JWT`）查看，便于只读浏览者了解仓库当前分支状态；
  - `PUT /api/repos/:repo_id/branch`：严格挂载仓库级写守卫 `CheckScopeWriteOrAbort(c, repoID, "")`（仅 `super_admin`、`archdoc_admin`、仓库 `OwnerID`、仓库 `RelatedMembers` 可操作）；**仅子目录负责人与普通用户一律返回 `403 FORBIDDEN`**（因为切换分支作用于整个仓库文档基线与全局漂移监控），并记录 `P1` 审计日志（`switch_branch / repo_branch`）。
- **短 TTL 分支清单缓存**：`BranchService` 在进程内维护 30 秒 TTL 的仓库分支列表缓存（并发安全锁保护），切换分支成功或请求携带 `?refresh=true` 时主动失效并重新拉取远端。

---

## 3. 安全合规与降级设计

1. **命令注入与路径穿越防护**：全部 `git` 命令均通过 `exec.Command` 参数数组执行（绝不经由 shell 解析），并在入参处经 `ValidateBranchName` 拦截所有 `-` 开头标志注入与控制字符。
2. **凭据零泄漏**：所有 Git 错误信息通过 `gitsync.SanitizeGitOutput` 剥离 URL 内嵌用户名/密码/Token 后再记日志或返回给前端。
3. **无前端 XSS 风险**：分支名通过 React JSX 自动转义渲染，禁止任何 `dangerouslySetInnerHTML` 或 `innerHTML`，分支切换确认框统一使用 `@code/common` 的 `useConfirm`。

---

## 4. 演进兼容性（面向 P1 `F-20` 多分支基线）

本 ADR 实施的所有改动对后续演进至 `F-20` 多分支基线完全兼容：
- `archdoc_documents`、`archdoc_repo_snapshots`、`archdoc_gen_tasks`、`archdoc_repo_remote_heads` 原有 `(..., ref_name, ...)` 唯一索引与字段结构保持原样不破坏；
- 未来开启多分支时，只需将 `archdoc_repo_module_policies.target_branch` 语义演进为「仓库默认主视图分支」，并放开 `RebindRepoBranch` 为按分支隔离生成即可，零迁移负担。
