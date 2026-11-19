# Milestone 4 作战卡：W1 微前端核心工作台与 W1 纵向闭环总验收 (W1 Exit Gate)

- **文档编号**：`05-m4-w1-frontend-workbench.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**W1（最小可用纵向闭环 — 第 4 站 / W1 收口闸门）**
- **前置依赖**：`M1`~`M3` 全部 AC 通过（注：`T-M4-01`~`T-M4-02` 可在 `M1` 冻结后基于 `03-api` 契约提前并行）；[`docs/02-design/04-workbench-and-security.md`](../02-design/04-workbench-and-security.md)、[`docs/03-api/01-http-api-contract.md`](../03-api/01-http-api-contract.md)、[`02-data-model-and-dto-contract.md`](../03-api/02-data-model-and-dto-contract.md)
- **覆盖特性与场景**：`F-01`, `F-08`(W1), `F-09`(W1), `F-10`, `F-11`, `F-14`, `NFR-7`；**W1 Exit Gate 全链路纵向验收**

---

## 1. 实施目标与工程边界

完成 W1 用户可见的三大微前端工作台页面（严格复用 `@code/common`、原生 Vanilla CSS、`--color-*` 双模 Design Tokens 与 Flat BEM `.code-archdoc-*`，零重型组件库、零 `any`），并执行 **W1 Exit Gate** 强制闸门验收：
1. 替换脚手架示例路由与页面，落地正式 `./menu` 与 `./App` 联邦契约（含 `/docs/:docId/evidence` 门户深链直达入口）；
2. 交付强类型 `frontend/src/types/` 与 `frontend/src/api/` 客户端封装；
3. 交付 `RepoCoveragePage`（仓库与子目录覆盖矩阵 + URL 同步 5 页分页默认 25 条/页 + 生成任务进度/取消/单槽位重试弹窗）；
4. 交付 `CanvasStudioPage` W1 版（三段式工作台、Markdown + Mermaid 滚轮缩放/拖拽平移、Verifier 告警条、章节级手工直编与所有权切换、孤立章节处置区、审批发布/撤回/导出、右侧只读源码证据抽屉与修订历史 Diff/回滚）；
5. 交付 `AdminLLMConfigPage`（`fast`/`flagship` 分层配置、脱敏回显、预算阈值与连通性探活）。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M4-01`** | 搭建强类型契约层、API 客户端与双模联邦路由骨架 | `frontend/src/types/{domain,api,sse}.ts`<br>`frontend/src/api/{client,repos,tasks,documents,admin}.ts`<br>`frontend/src/menu.ts`<br>`frontend/src/App.tsx`<br>`frontend/src/styles/archdoc.css` | 清除脚手架示例页面；按 `02-data-model-and-dto-contract.md` §6 实现零 `any` TypeScript 接口；`client.ts` 统一解析 `{error, code, details}` 语义错误；`./menu` 暴露 W1 菜单（`/repos`, `/admin/llm`，W2 入口待 `M5`/`M6` 挂载）；`./App` 兼容嵌入式 `/archdoc/*` 与独立态相对路由，并接入 `/docs/:docId/evidence` 深链解析 | `M1` |
| **`T-M4-02`** | 实现 `RepoCoveragePage` 与生成任务弹窗 (`F-01`/`F-05`) | `frontend/src/pages/RepoCoveragePage.tsx`<br>`frontend/src/components/GenerateTaskModal.tsx`<br>`frontend/src/pages/RepoCoveragePage.css` | 复用 `@code/common` `Pagination` + `usePagination`（`useSearchParams` 双向同步 `page`、`pageSize=25`、`search`、`department_id`、`doc_type`、`status`）；每行展示仓库/子目录身份、各启用 `DocType` `StatusTag` 矩阵与按 `can_write` 控权的「触发生成」和「ZIP 导出」动作；`GenerateTaskModal` 勾选 1~N `DocType`，提交后轮询 `GET /api/tasks/:id` 实时展示阶段进度、预算熔断建议、取消与单槽位重试按钮 | `T-M4-01` |
| **`T-M4-03`** | 实现 `CanvasStudioPage` W1 主画布与手工直编/锁定/发布 (`F-08`~`F-11`) | `frontend/src/pages/CanvasStudioPage.tsx`<br>`frontend/src/components/studio/{StudioTopBar,LeftDocTreePanel,VerifierBanner,SectionCard,MermaidZoomViewer,SectionInlineEditor,OrphanSectionsZone}.tsx`<br>`frontend/src/pages/CanvasStudioPage.css` | CSS Grid 三段式自适应布局；`MermaidZoomViewer` 支持双主题（亮/暗随 `data-theme` 同步切换 Mermaid theme）+ 滚轮缩放与鼠标拖拽平移；`SectionCard` 支持证据标签点击打开右侧抽屉、所有权切换 (`auto`/`hybrid`/`locked`) 与手工直编（携带 `base_revision_no`，遇 `409` 提示冲突刷新）；`OrphanSectionsZone` 支持孤立章节保留/合并/删除；顶部栏提供审批发布（`can_approve==false` 时禁用并悬浮原因）、撤回与 Markdown 下载 | `T-M4-01`<br>`M3` |
| **`T-M4-04`** | 实现 Canvas Studio 右侧抽屉：只读源码证据查看器与修订历史 Diff (`F-08`/`F-09`) | `frontend/src/components/studio/{RightUtilityDrawer,EvidenceSourceTab,RevisionHistoryTab}.tsx` | `EvidenceSourceTab` 接收 `commit`/`file`/`symbol`/`lines`（支持来自 `/docs/:docId/evidence` URL 深链自动展开），渲染行号与精确高亮行区间并自动滚动至目标行；`RevisionHistoryTab` 展示不可变修订时间线、任意两版本按 `slot_id` 展示 Unified Diff 与 `ConfirmProvider` 二次确认一键回滚 | `T-M4-03` |
| **`T-M4-05`** | 实现 `AdminLLMConfigPage` 分层模型配置与探活台 (`F-14`) | `frontend/src/pages/AdminLLMConfigPage.tsx`<br>`frontend/src/pages/AdminLLMConfigPage.css` | `fast` / `flagship` 切换标签；`APIKey` 输入框 placeholder 展示 `api_key_masked`（留空提交保留既有密钥）；配置超时、重试、单任务 Token/文件预算熔断上限；点击「测试连通性」调用 `POST /api/admin/llm-configs/:tier/probe` 实时展示连通状态、延迟 ms 与模型回显 | `T-M4-01` |

---

## 3. 前端验证与自检用例清单 (Verification Checklist)

1. **类型与静态门禁检查**：
   - `frontend/` 全量 `grep -rn ": any\b\|as any\b" src/` 结果为 `0`；
   - `frontend/` 全量 CSS 零硬编码 `#fff`/`#000`/`#111827`/`rgba(15, 23, 42, ...)`，全部颜色来自 `--color-*` Token，且无静态 JSX `style={{ ... }}` 与 `onMouseEnter`/`onMouseLeave` 伪类模拟；
   - 分页控件布局严格为 `[首页][上一页][5页连续滑动窗口][下一页][末页][条/页(15/25/50/100, 默认25)]` 并与浏览器前进/后退 URL 同步。
2. **关键交互逻辑单元测试（Vitest / 纯函数测试）**：
   - URL Search Params 分页与筛选序列化/反序列化测试；
   - 深链 `/docs/:docId/evidence?commit=...&file=...&symbol=...&lines=142-188` 参数解析与非法行号兜底测试；
   - API 错误码映射（`409 OPTIMISTIC_LOCK_CONFLICT`、`422 VERIFIER_BLOCKED`、`403 PATH_ESCAPE`、`TOKEN_BUDGET_EXCEEDED`）友好提示渲染测试。
3. **双模运行与构建检查**：
   - `cd frontend && npm run lint && npm run build` 零报错，校验 `dist/assets/remoteEntry.js` 成功暴露 `./App` 与 `./menu`；
   - 根目录 `make build` 生成内嵌前端产物的单二进制 `code-archdoc-server`。

---

## 4. W1 Exit Gate 总验收标准（进入 W2 的硬前置条件）

- [ ] **AC-M4-01**：`make lint`（含 `make lint-arch` + `tsc --noEmit` + `eslint`）100% 零报错通过。
- [ ] **AC-M4-02**：`TEST_DB_DSN=... make test` 与 `make eval-baseline` 100% 全绿。
- [ ] **AC-M4-03**：`make build` 成功产出单二进制 `code-archdoc-server`，启动验证独立态核心链路后已彻底关闭测试进程释放 `:8100` / `:5182`。
- [ ] **AC-M4-04（W1 Exit Gate 纵向闭环总验收）**：在本地隔离环境（Mock LLM 或内网测试模型）跑通完整纵向闭环：
  1. 选定 `cpp_order_service` 夹具仓 → 触发生成内置 `DocType` → 确认 Git 同步 & C/C++/Proto Code IR 抽取缓存生效；
  2. Verifier 校验生效（伪造符号标 `unverified`、明文凭据阻断发布返 `422`）；
  3. Canvas Studio 浏览章节、点击证据锚点高亮源码、手工直编章节并锁定 ADR、验证重生成不覆盖锁定节；
  4. 审批发布落盘至 `publish_dir`（含 YAML Frontmatter 与 `/archdoc/docs/:id/evidence?...` 深链）、ZIP 导出与一键撤回生效；
  5. `code-bench` 符号链接挂载浏览 Frontmatter 隐藏与深链路由跳转生效。
  > **闸门红线**：以上 5 项全部确认通过后，方可启动 `M5` / `M6`（波次 `W2`）。
