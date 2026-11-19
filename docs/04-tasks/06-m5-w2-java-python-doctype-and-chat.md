# Milestone 5 作战卡：Java/Python Code IR、`DocType` Web 全生命周期与 SSE Chat 局部精修

- **文档编号**：`06-m5-w2-java-python-doctype-and-chat.md`
- **文档版本**：v1.0.0 (Stage 3 Frozen Task Card)
- **归属波次**：**W2（P0 全量能力补齐 — 与 M6 正交并行）**
- **前置依赖**：**W1 Exit Gate (`M1`~`M4`) 100% 通过**；[`docs/02-design/02-extraction-and-generation-engine.md`](../02-design/02-extraction-and-generation-engine.md) §2.2、[`03-doctype-drift-and-publishing.md`](../02-design/03-doctype-drift-and-publishing.md) §1.5、[`04-workbench-and-security.md`](../02-design/04-workbench-and-security.md) §2.4 & §3.2
- **覆盖特性与场景**：`F-04`(W2 Java/Python), `F-03`(W2 Web 管理), `F-08`(W2 Chat 面板), `F-09`(W2 SSE Chat 精修)；`SC-05`, `SC-08`

---

## 1. 实施目标与工程边界

在 `W1` 闭环底座上补齐三大核心高价值能力：
1. **Java / Python L1 Code IR 解析器（`F-04` W2）**：在 `pkg/codeir/treesitter/` 内新增 `tree-sitter-java` 与 `tree-sitter-python` 提取器及 Maven/Gradle/Poetry/Pip 构建解析，实现路由签名、ORM 实体与调度注解 100% 精确抽取；
2. **`DocType` 管理员 Web 全生命周期管理（`F-03` W2）**：落地「同 `slug` 写锁 + 临时文件 `fsync` + 原子 `rename` → 目录重同步 DB → 异步触发 `Spec Drift`」安全写路径与 `AdminDocTypesPage` 可视化槽位构建器；
3. **SSE Chat 局部精修前后端闭环（`F-09` W2）**：落地流式精修 SSE 协议（`meta` → `delta` → `diff_proposal` → `verifier_preview` → `done`/`error`）、`Accept`/`Reject` 状态机（接入统一 `RevisionService` 保证非目标章节字节级不变，并写入 `F-26` 采纳率指标）以及 Canvas Studio 右侧 `ChatRefineTab`。

---

## 2. 开发任务分解 (Task Breakdown)

| 任务编号 | 任务名称 | 涉及文件路径 | 核心实现要点 | 依赖 |
| :--- | :--- | :--- | :--- | :---: |
| **`T-M5-01`** | 实现 Java / Python `tree-sitter` 与构建依赖提取器 (`F-04` W2) | `pkg/codeir/treesitter/{java_extractor,python_extractor}.go`<br>`pkg/codeir/extractor_build_jvm_py.go`<br>`testdata/golden/fixtures/{java_billing_svc,py_risk_api}/**` | `java_extractor`：提取类/方法、Spring `@RequestMapping`/`@GetMapping`/`@PostMapping` 类+方法拼接路由、JPA `@Entity`/`@Table` & MyBatis Mapper 实体、`@Scheduled` 调度；`python_extractor`：提取类/函数、FastAPI/Flask 装饰器路由、SQLAlchemy/Django ORM 模型、Celery/APScheduler 任务；解析 `pom.xml`/`build.gradle`/`pyproject.toml`/`requirements.txt`；补充 Java/Python 真值夹具纳入 `make eval-baseline` | `W1 Gate` |
| **`T-M5-02`** | 实现 `DocType` 原子写盘与管理员 CRUD API (`F-03` W2) | `pkg/doctype/writer.go`<br>`services/doctype_service.go`<br>`handlers/admin_doctype.go` | 实现进程内 `slug` 写互斥锁、JSON Schema 与 `template.md` `<!-- slot:<id> -->` 一致性校验、未知 `ExtraFields` 无损写回、`.tmp-<file>-<uuid>` + `fsync` + 原子 `rename`；交付 `POST /api/admin/doc-types`、`:slug/clone`、`PUT /api/admin/doc-types/:slug`、`PATCH .../:slug/status`、`DELETE .../:slug`（已发布引用拦截 `409 DOC_TYPE_IN_USE`）、`POST .../sync`；写操作成功后自动触发受影响文档 `Spec Drift` 计算钩子；全量 `P0` 审计 Diff | `W1 Gate` |
| **`T-M5-03`** | 实现 SSE Chat 局部精修与 `Accept`/`Reject` 后端 (`F-09` W2) | `pkg/llm/chat_refine.go`<br>`services/chat_service.go`<br>`handlers/chat.go` | 装配隔离数据块（目标章节正文 + 关联 `EvidenceAnchor` 源码 + 订阅 Code IR 切片 + 可选 `refine_prompt.md`）发起 OpenAI Stream；若目标槽位为 `locked` 返回 `409`；实时推送 `meta`/`delta`/`diff_proposal`/`verifier_preview`/`done`/`error` SSE 事件并支持 `ctx.Done()`（客户端 `AbortController`）立即中断 LLM；记录 `archdoc_llm_usage_logs(stage="chat_refine")`；`Accept` 调用 `RevisionService.ApplySectionChange` 校验乐观锁并标记旧 `pending` 提案为 `superseded`，写入 `QualityMetric(chat_proposal_resolved)` | `W1 Gate` |
| **`T-M5-04`** | 实现前端 `AdminDocTypesPage` (`F-03` W2) | `frontend/src/pages/AdminDocTypesPage.tsx`<br>`frontend/src/components/doctype/{DocTypeEditorDrawer,SlotsSchemaBuilder}.tsx`<br>`frontend/src/menu.ts` & `App.tsx` | 接入 `/admin/doc-types` 菜单与路由（`adminOnly`）；表格展示 `slug`、`display_name`、启停开关、当前 Revision、已发布引用数与新建/克隆/编辑/删除（被引用时禁用删除或提示 `409`）；`DocTypeEditorDrawer` 含元数据表单、`SlotsSchemaBuilder`（修改既有 `slot_id` 时弹出风险确认）、`template.md`/`generation_prompt.md`/`refine_prompt.md` 等宽编辑器及修订历史列表 | `T-M5-02` |
| **`T-M5-05`** | 实现前端 Canvas Studio `ChatRefineTab` (`F-08`/`F-09` W2) | `frontend/src/components/studio/{ChatRefineTab,DiffProposalCard}.tsx`<br>`frontend/src/api/chat.ts` | 使用 `fetch` + `ReadableStream` 解析标准 SSE 事件并支持 `AbortController` 随时停止生成；按目标 `slot_id` 展示流式气泡与结构化红绿 `DiffProposalCard`（内置 `verifier_preview` 预警标签）；点击 `Accept` 立即更新画布与修订历史，遇 `409` 引导刷新重试；点击 `Reject` 更新卡片状态 | `T-M5-03` |

---

## 3. TDD 先行单元测试用例清单 (Test-First Specification)

### 3.1 `pkg/codeir/codeir_java_py_test.go` (`F-04` W2)
1. `TestEvalJavaSpringAndORMAccuracy`：对 `java_billing_svc` 夹具抽取，断言 Spring 类+方法拼接路由签名、JPA `@Table` 实体字段、`@Scheduled` 任务与 `pom.xml` 依赖均达到 **100%** 准确，明文密码 0 残留。
2. `TestEvalPythonFastAPIAndSQLAlchemyAccuracy`：对 `py_risk_api` 夹具抽取，断言 FastAPI/Flask 路由、SQLAlchemy `__tablename__` 实体与 Celery 任务 **100%** 准确。

### 3.2 `pkg/doctype/writer_test.go` & `handlers/admin_doctype_test.go` (`SC-08`, 基于 `testdb.SetupIsolatedDB`)
1. `TestDocTypeLifecycleCreateCloneEditToggleDelete`：
   - 管理员 `POST /api/admin/doc-types` 新建 `security-review` → 断言磁盘目录 4 个文件落盘、DB 索引与 `revision_no=1` 一致；
   - 重复新建同 `slug` 返回 `409`；传入 `../evil` 返回 `400 INVALID_SLUG`；非管理员调用返回 `403`；
   - 在线 `PUT` 修改 `generation_prompt_md` 且保持 `meta.json` 未知扩展键无损 → 断言生成 `revision_no=2` 且未改动槽位 `slot_id` 不变；
   - 创建一篇绑定 `security-review` 且 `status="approved"` 的已发布文档后调用 `DELETE`，断言返回 **`409 DOC_TYPE_IN_USE`** 且磁盘与 DB 均未被删除；撤回文档为 `draft` 后再次 `DELETE` 返回 `200` 且磁盘目录与 DB 同步清除。
2. `TestDocTypeConcurrentWriteSerializedBySlugLock`：并发 5 协程修改同一 `slug` 的模板文件，断言无临时文件损坏、无 JSON 截断，最终磁盘内容与 DB 最新 `DocTypeRevision` 100% 一致。

### 3.3 `handlers/chat_refine_test.go` (`SC-05`, 基于 `testdb.SetupIsolatedDB`)
1. `TestSSEChatRefineStreamAndAcceptPreservesOtherSlots`（`SC-05`）：
   - 用 `httptest.NewServer` 模拟 OpenAI 流式 SSE 响应，调用 `POST /api/documents/:id/chat/refine` 针对第 2 槽位精修，断言按序收到 `meta`、`delta`、`diff_proposal`、`verifier_preview`、`done` 事件；
   - 调用 `Accept` 后，断言仅第 2 槽位更新、其余槽位 `content_sha256` 字节级不变、新不可变 `Revision`（`source="chat_refine"`）落库、`archdoc_quality_metrics` 写入 `chat_accepted=true`、`archdoc_llm_usage_logs` 记录 Token 成本；
   - 锁定第 1 槽位后对该槽位调用 `chat/refine` 断言返回 `409`；对第 2 槽位发起另一提案后调用 `Reject`，断言文档修订号不变且写入 `chat_accepted=false`。
2. `TestSSEChatRefineClientAbortCancelsUpstream`：模拟上游 LLM 慢流，客户端提前取消请求 `ctx`，断言后端立即中止上游连接且不写入无效提案。

---

## 4. 自动化验收标准与执行命令 (Acceptance Criteria)

- [ ] **AC-M5-01**：`make eval-baseline` 覆盖 C/C++/Proto + Java + Python 全部夹具并 100% 达标。
- [ ] **AC-M5-02**：`TEST_DB_DSN=... make test` 与 `make lint-arch` 全绿，`SC-05`、`SC-08` 自动化测试全部通过。
- [ ] **AC-M5-03**：`make lint && make build` 零错误通过，前端 `AdminDocTypesPage` 与 `ChatRefineTab` 在亮/暗双主题下无硬编码色值、零 `any`。
