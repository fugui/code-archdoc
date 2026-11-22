# 代码设计文档反推 (`code-archdoc`) 联调验证与生产部署手册

- **文档编号**：`01-verification-and-deployment-guide.md`
- **文档版本**：v1.0.0 (M7 Production Ready)
- **更新日期**：2026-11-20
- **归属阶段**：阶段四：交付验证与运维指南 (`06-ops`)
- **关联契约**：[`ADR-01`](../05-adr/01-microfrontend-and-table-prefix-isolation.md)、[`ADR-02`](../05-adr/02-cpp-parser-and-code-ir-storage.md)、[`ADR-03`](../05-adr/03-evidence-anchor-and-publish-mount.md)、[`config.yaml.example`](../../config.yaml.example)

---

## 1. 构建环境与单二进制编译 (`CGO_ENABLED=1`)

`code-archdoc` 通过官方 Go CGO 绑定静态内嵌 `tree-sitter`（C/C++/Java/Python 语法树解析器），并通过 `//go:embed all:frontend/dist` 将 React 微前端静态资产打包进单一可执行文件 `code-archdoc-server`。

### 1.1 Linux 构建环境前置依赖

| 依赖项 | 最低版本要求 | 用途说明 |
| :--- | :--- | :--- |
| **Go** | `>= 1.23` | 后端与 CGO 编译 |
| **C 编译器 (`gcc` / `cc`)** | GCC `>= 9` 或 Clang `>= 11` | `CGO_ENABLED=1` 静态编译 `tree-sitter` 语法库 |
| **Node.js / npm** | Node `>= 20` | 构建微前端 `frontend/dist` 与 `remoteEntry.js` |
| **Git & SSH Client** | Git `>= 2.25` | 运行时只读沙箱克隆与 `git ls-remote` 远端巡检 |
| **PostgreSQL** | `>= 14` | 与 `code-bench` 共享实例（16 张自有表统一 `archdoc_` 前缀） |

### 1.2 编译与打包命令

```bash
# 1. 校验 CGO 与本机 C 编译器就绪
make check-cgo

# 2. 执行完整打包（前端 Vite Federation 构建 + CGO_ENABLED=1 单二进制内嵌）
make build

# 3. 验证单二进制产出（零外部 CLI 依赖，仅依赖运行时 git 与共享 PG）
./code-archdoc-server -h

# 4. 清理临时构建产物
make clean
```

> **交叉编译注意事项**：因启用 `CGO_ENABLED=1`，生产二进制须在目标架构一致的 Linux 构建机（或容器镜像）中执行 `make build`；若 `go env CGO_ENABLED` 非 `1`，`make check-cgo` 会立即阻断报错。

---

## 2. 生产环境变量与配置管理

复制 [`config.yaml.example`](../../config.yaml.example) 为 `config.yaml`（已被 `.gitignore` 忽略），生产环境**强烈建议通过环境变量注入敏感密钥**（环境变量优先级高于 `config.yaml`）：

### 2.1 必配环境变量与密钥安全

| 环境变量 | 是否必填 | 格式与生成方式 | 说明 |
| :--- | :---: | :--- | :--- |
| **`JWT_SECRET`** | **生产必填** | 与 `code-bench/config.yaml` 中 `auth.jwt_secret` **严格一致** | 实现 CodeBench 门户嵌入态无感单点鉴权；切勿留空导致单实例随机密钥不互通 |

> **内网 LLM 配置说明**：服务启动后不再强制配置任何密钥加密环境变量，管理员登录后可在「模块治理 → LLM 模型配置」直接新增内网模型并手动设置唯一默认模型，所有接口返回与审计快照均对 API Key 执行脱敏保护。

### 2.2 核心生产推荐参数说明

| 配置节 | 参数键 | 生产推荐值 | 调优与安全说明 |
| :--- | :--- | :--- | :--- |
| **`auth`** | `standalone_mode` | `false` | 嵌入 CodeBench 生产部署时关闭独立本地登录，仅信任宿主透传 JWT |
| **`storage`** | `data_dir` | `/var/lib/code-archdoc` | 统一承载 `sandbox_dir/`、`ir_cache/`、`doc_types/`、`publish_dir/`；建议挂载独立大容量 SSD 分区 |
| **`storage`** | `publish_dir` | `""`（默认 `<data_dir>/publish_dir`） | 仅落盘已审批定稿 Markdown；`.staging/` 位于其内部以保证同文件系统原子 `rename` |
| **`storage`** | `sandbox_max_mb` | `20480`（20 GB） | 超出阈值自动触发受活跃租约保护的 LRU 回收，绝不删除运行中任务的工作区 |
| **`storage`** | `ir_cache_max_mb` | `10240`（10 GB） | 压缩分片 `.json.gz` 缓存上限；已发布文档引用快照享有更高保留优先级 |
| **`storage`** | `max_file_bytes` | `1048576`（1 MB） | L1 降噪单文件体积上限，自动跳过超大生成文件与二进制 |
| **`storage`** | `reap_interval_mins` | `10` | 后台配额巡检周期（分钟） |
| **`scheduler`** | `max_llm_concurrency` | `8` | 全局 LLM 并发信号量上限，按内网推理集群算力调整（推荐 `6~16`） |
| **`scheduler`** | `max_user_concurrency` | `2` | 单用户最大并发任务数，防单人占满队列 |
| **`scheduler`** | `interactive_queue_cap` / `batch_queue_cap` | `64` / `256` | 双队列 4:1 防饥饿调度（交互优先于批量），队列满返回 `429 QUEUE_FULL` |
| **`drift`** | `poll_enabled` | `true` | 仅对含已发布文档的活跃仓库执行轻量 `git ls-remote`，不拉取全量代码 |
| **`drift`** | `poll_interval_minutes` / `poll_concurrency` | `15` / `4` | 失败仓指数退避（`15m -> 30m -> 1h -> 4h -> 24h`），不阻塞健康仓 |

---

## 3. CodeBench 门户接入与 `docs.path` 符号链接挂载 (`D-01`~`D-03`)

### 3.1 配置宿主门户网关反向代理 (`code-bench`)

在 `../code-bench/config.yaml` 的 `gateways:` 节点下注册 `archdoc`（参考 `../code-bench/config.yaml.example`）：

```yaml
gateways:
  archdoc:
    url: "http://127.0.0.1:8100"
    title: "代码设计文档反推"
    icon: "ScrollText"
    description: "基于代码唯一事实源的反推架构文档与漂移治理工作台"
    order: 35
    default_pinned: true
```

重启 `code-bench` 后，宿主网关自动生效以下能力（无需重编译宿主）：
- `/archdoc/assets/remoteEntry.js` 及静态产物透明代理至 `127.0.0.1:8100`；
- `/archdoc/api/*` 反向代理并完整透传用户 `Authorization: Bearer <JWT>` 请求头；
- `GET /api/modules` 返回 `/archdoc` 前缀，`code-bench` 开发者文档中心据此启用 `D-03` 平台 SPA 深链跳转。

### 3.2 配置 `publish_dir` 同机符号链接挂载 (`D-01`)

P0 阶段 `code-archdoc` 与 `code-bench` **同机部署**（或挂载同一共享存储卷），在 `code-bench` 的 `docs.path` 目录下建立指向 `code-archdoc` 发布目录的符号链接：

```bash
# 1. 确保 publish_dir 存在且对 code-bench 运行用户具备只读遍历权限
sudo mkdir -p /var/lib/code-archdoc/publish_dir
sudo chmod 0755 /var/lib/code-archdoc /var/lib/code-archdoc/publish_dir

# 2. 在 code-bench 的 docs.path（例如 /var/lib/code-bench/docs）下建立符号链接
ln -s /var/lib/code-archdoc/publish_dir /var/lib/code-bench/docs/archdoc

# 3. 核验符号链接解析有效且 code-bench 运行用户可读
readlink -f /var/lib/code-bench/docs/archdoc
ls -la /var/lib/code-bench/docs/archdoc
```

**同机符号链接权限与安全约束**：
1. **同文件系统原子性**：`<publish_dir>/.staging/` 必须保持在 `publish_dir` 内部同一挂载点，确保发布时的 `os.Rename()` 原子生效，且 `.staging` 因 `.` 前缀会被 `code-bench` `scanDocDir` 自动忽略；
2. **最小权限原则**：若 `code-archdoc` 与 `code-bench` 使用不同系统用户运行（如 `archdoc` 与 `codebench`），建议将两用户加入同一只读组（目录权限 `0755`、发布文档 `0644`），使 `code-bench` 仅可读 `publish_dir` 而不可篡改；
3. **联调核验要点**：
   - 在 `/archdoc` 工作台审批发布文档后，刷新 `code-bench`「开发者文档中心」，左侧目录树即刻出现 `archdoc/<repo_slug>/<doc_type>.md`；
   - 打开该文档时，YAML Frontmatter 原文自动隐藏并渲染为顶部「文档元信息栏」（代码仓、文档类型、提交哈希、发布时间，满足 `D-02`）；
   - 点击文档内的源码证据链接 `/archdoc/docs/:id/evidence?commit=...&file=...&symbol=...&lines=...`，门户 SPA 无刷新跳转至 `/archdoc` 并自动展开右侧只读源码抽屉高亮定位（满足 `D-03`）；
   - 执行「一键撤回发布」后，发布目录对应文件与空目录即时清除，`code-bench` 文档树与 `/archdoc` 普通用户视图同步不可见。

---

## 4. 优雅停机 (`SIGINT`/`SIGTERM`) 与进程组清理核验

`code-archdoc-server` 基于 `code-common/backend/server.Run` 监听 `SIGINT` 与 `SIGTERM`，收到停机信号后在 `OnShutdown` 钩子中严格按序执行：

1. **取消全局上下文与所有运行中任务**：触发 `bgCancel()` 与 `genSvc.CancelAll()`，所有正在执行的 `git clone/fetch` 通过 `code-common/backend/gitsync.ExecCommandWithProcessGroup` 向整个负进程组 (`-pgid`) 发送 `SIGKILL`，彻底杜绝孤儿 `git` / `ssh` 子进程泄漏；
2. **停止后台治理工作协程**：依次调用 `pollerSvc.Stop()`（远端变更巡检）、`reaperSvc.Stop()`（磁盘 LRU 回收器）与 `taskScheduler.Stop()`（排空未启动排队任务并等待活跃 worker 退出）；
3. **带截止时间等待与审计刷盘**：`genSvc.WaitAllWithContext(ctx)` + `docTypeSvc.WaitSpecDriftJobs()` + `commonAudit.Close(ctx)` 将异步审计队列全部 flush 写入 PostgreSQL `audit_logs` 后平滑退出。

### 4.1 生产停机核验命令

```bash
# 发送 SIGTERM 触发优雅停机
kill -TERM $(pgrep -f code-archdoc-server)

# 确认进程干净退出且无残留 :8100 监听或孤儿 git 进程
ss -tlnp | grep ':8100' || echo "port 8100 clean"
ps -ef | grep -E 'code-archdoc-server|git' | grep -v grep || echo "no orphan process"
```

---

## 5. 数据目录备份策略与故障排查手册

### 5.1 数据目录结构与备份分级

| 目录 / 存储 | 路径 | 备份优先级 | 说明与恢复策略 |
| :--- | :--- | :---: | :--- |
| **PostgreSQL 数据库** | 共享实例 `archdoc_*` 18 张表 | **P0 每日必备** | 存放文档、不可变修订历史、证据锚点、漂移记录、Chat 精修历史、质量度量与模型池配置 |
| **DocType 唯一事实源** | `<data_dir>/doc_types/` | **P0 每日必备** | 所有自定义与内置 `DocType` 的 `meta.json`/`template.md`/Prompt 文件（数据库仅存索引与快照） |
| **已发布静态产物** | `<publish_dir>/` | **P2 可选** | 源数据在 PG 中，可对已审批文档重新触发发布重建 |
| **Code IR 缓存** | `<ir_cache>/` | **无需备份** | 确定性静态抽取缓存，缺失时自动按 `commit_sha` 重抽 |
| **Git 沙箱工作区** | `<sandbox_dir>/` | **无需备份** | 只读临时克隆目录，受 LRU 配额自动治理，缺失时按需重拉 |

### 5.2 常见故障排查手册 (Troubleshooting)

| 故障现象 | 常见原因 | 排查与处置步骤 |
| :--- | :--- | :--- |
| **`make build` 报 `CGO Check Failed`** | 未开启 `CGO_ENABLED=1` 或缺少 `gcc`/`cc` | 安装 `build-essential` / `gcc`，执行 `CGO_ENABLED=1 make build` |
| **触发生成或精修提示无可用默认模型** | 模型池为空或默认模型已停用 / 指定模型名称不存在 | 管理员进入「模块治理 → LLM 模型配置」新增或启用模型并手动「设为默认」 |
| **Git 同步 30 秒内报鉴权失败** | 宿主机运行用户缺少只读 SSH Key 或 `known_hosts` 未信任 | 切换至服务运行用户执行 `ssh -T -o BatchMode=yes git@<host>` 验证免密连通性 |
| **任务立即失败 `TOKEN_BUDGET_EXCEEDED`** | 仓库有效文件数或预估 Token 超过 `max_tokens_per_task`/`max_files_per_task` | 查看任务详情 `budget_advice` 裁剪建议：添加 `.archdocignore`、拆分 Monorepo 子目录或由管理员调大预算 |
| **点击发布返回 `422 VERIFIER_BLOCKED`** | 草稿命中 Verifier 明文凭据/高熵密钥阻断规则 | 在 Canvas Studio 顶部 Verifier 告警条定位具体章节，手工直编移除明文凭据后重新保存并发布 |
| **`code-bench` 文档树看不到 `archdoc` 目录** | 1) 尚无已发布文档（空目录被隐藏）；2) 符号链接损坏或权限不足 | 确认至少有 1 篇 `Approved` 文档已发布；检查 `ls -la <docs.path>/archdoc` 及目录 `0755` 读权限 |
| **门户加载 `archdoc` 白屏或 `401`** | `JWT_SECRET` 与 `code-bench` 不一致，或 `gateways.archdoc.url` 不可达 | 核对两端 `jwt_secret` 完全一致；执行 `curl http://127.0.0.1:8100/api/health` 确认后端存活 |
| **删除 `DocType` 返回 `409 DOC_TYPE_IN_USE`** | 存在绑定该类型的已发布文档 | 先在界面将该类型切换为「停用」状态，或撤回所有关联已发布文档后再删除 |

---

## 6. 本地开发、物理隔离全量测试与上线前必过门禁

### 6.1 独立调试模式启动

```bash
# 终端 1：编译并启动后端（监听 :8100）
cp config.yaml.example config.yaml
make build && ./code-archdoc-server

# 终端 2（可选，前端热更新调试，监听 :5182）
cd frontend && npm run dev
```
> 调试完成后务必主动关闭后端与 Vite 进程，确保 `:8100` 与 `:5182` 端口完全释放。

### 6.2 上线前自动化门禁命令清单 (Pre-Release Gates)

```bash
# 1. 架构防腐静态门禁（5 条红线 0 违规）
make lint-arch

# 2. F-26 Golden Sample 量化基线评测（C/C++/Proto + Java + Python 准确率 + 0 密钥残留 + Verifier 门禁）
make eval-baseline

# 3. 物理隔离临时数据库全量单测 + SC-01~SC-13 全量 E2E 回归（dbname 必须为 postgres 或 *_test）
TEST_DB_DSN="host=127.0.0.1 port=5432 user=postgres password=YOUR_PASSWORD dbname=postgres sslmode=disable" make test

# 4. 跨模块零回归验证（code-common / code-shield / code-bench）
(cd ../code-common/backend && go test ./...)
(cd ../code-shield && go test ./...)
(cd ../code-bench && go test ./...)
(cd ../code-bench/frontend && npm run test && npm run lint && npm run build)

# 5. 前端类型/规范校验 + 单二进制打包验证 + 清理临时产物
make lint && make build && make clean
```

### 6.3 上线前最终自检清单

- [x] `make lint-arch` 通过（`pkg/*` 零 DB/Gin 依赖、`services/` 零 `models.DB`、CGO 严格封闭于 `pkg/codeir/treesitter/`、16 张自有表均以 `archdoc_` 开头）；
- [x] `make eval-baseline` 通过（C/C++/Proto + Java + Python 抽取准确率与 0 密钥残留全部达标）；
- [x] 全部 12 个 P0 验收场景（`SC-01`、`SC-02`、`SC-04`~`SC-13`）在物理隔离库中全量通过；
- [x] 所有写操作接口接入 `commonAudit` 字段级审计，管理员接口挂载 `RequireAdmin("archdoc_admin")`，`APIKey` 全链路接口/日志/审计均脱敏为 `sk-****`；
- [x] 前端全页面通过亮/暗双模主题自检，无硬编码色值、无 `any`、URL 同步 5 页滑动分页默认 25 条/页；
- [x] `make lint && make build && make clean` 零错误完成，`:8100` 与 `:5182` 无残留后台进程。
