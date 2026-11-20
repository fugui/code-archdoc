# 代码设计文档反推 (`code-archdoc`) 文档知识库 (Docs Hub)

本项目严格采用 **SDD (Spec-Driven Development，规范驱动开发) + TDD (Test-Driven Development，测试驱动开发)** 模式，所有提示词资产、需求边界、系统架构、数据表模型、API 契约、TDD 任务拆解与运维验证手册均在此目录中进行统一版本化管理。

---

## 目录分层架构

```text
docs/
├── 00-prompt/       # 提示词库：记录各阶段 SDD/TDD Prompt 资产与上下文演进指令
│   ├── 01-init-features-prompt.md         # 阶段一：特性矩阵 (Feature Matrix) 与需求边界生成提示词
│   ├── 02-design-prompt.md                # 阶段二：系统架构、GORM 模型与微前端交互设计提示词
│   ├── 03-api-and-tasks-prompt.md         # 阶段三：API 契约冻结与 TDD Milestone 任务拆解提示词
│   └── 04-tdd-implementation-prompt.md    # 阶段四：TDD 红-绿-重构编码实施与全链路交付验证提示词
├── 01-features/     # 特性与需求边界：特性清单矩阵 (Feature Matrix)、P0~P2 优先级与 Non-Goals
│   └── 01-feature-matrix.md               # 阶段一：需求范围、核心特性矩阵与验收场景定义
├── 02-design/       # 系统技术架构 (SDD)：五专篇系统架构、L1/L2 引擎、领域治理、微前端工作台与数据模型
│   ├── 01-system-architecture.md               # 【总览篇】双模拓扑、运行时五层架构、跨模块协同(D-01~D-04)与包分层防腐
│   ├── 02-extraction-and-generation-engine.md  # 【核心引擎篇】Git 沙箱降噪、Code IR 多语言 SPI、L2 编排熔断、Verifier 与资源调度
│   ├── 03-doctype-drift-and-publishing.md      # 【领域治理篇】DocType 目录 SSOT、章节锁定、publish_dir 落盘、双维漂移与质量度量
│   ├── 04-workbench-and-security.md            # 【应用与安全篇】Canvas Studio 组件树、双轨修订与 SSE 协议、三层 RBAC 与审计
│   └── 05-database-schema.md                   # 【数据模型篇】共享只读契约、全局 ER 图与 16 张 archdoc_* GORM 建模字典
├── 03-api/          # 接口与数据契约：RESTful & SSE API 签名、JSONB Schema、仓储接口与 TS 类型字典
│   ├── 01-http-api-contract.md            # 【API 契约】54 条 HTTP/SSE 接口字典、三层 RBAC 守卫、错误码与审计规范
│   └── 02-data-model-and-dto-contract.md  # 【数据契约】16 表索引不变量、12 组 JSONB Schema、状态机、仓储接口与 TS 类型
├── 04-tasks/        # TDD 实施规划与验收：Milestone 拆解、W1/W2 任务作战卡与自动化验收测试标准 (AC)
│   ├── 01-roadmap.md                           # 【工程路线图】W1(M1~M4) + W2(M5~M6) + 交付(M7) DAG 与质量门禁
│   ├── 02-m1-domain-model-and-api.md           # 【M1 作战卡】D-04 gitsync 底座、16 张领域模型、安全沙箱与 RBAC 基础 API
│   ├── 03-m2-codeir-doctype-and-generation.md  # 【M2 作战卡】Golden Sample、L1 C/C++/Proto IR、DocType 种子与 L2/Verifier 引擎
│   ├── 04-m3-revision-publish-and-bench-sync.md# 【M3 作战卡】手工直编、锁定/孤立保护、原子发布导出与 code-bench D-01~D-03
│   ├── 05-m4-w1-frontend-workbench.md          # 【M4 作战卡】W1 微前端核心工作台与 W1 Exit Gate 纵向闭环验收
│   ├── 06-m5-w2-java-python-doctype-and-chat.md# 【M5 作战卡】Java/Python 解析、DocType Web CRUD 与 SSE Chat 局部精修
│   ├── 07-m6-w2-drift-metrics-and-governance.md# 【M6 作战卡】F-25 巡检、F-12 双维漂移雷达、F-26 质量看板与 F-27 资源治理
│   └── 08-m7-portal-integration-and-delivery.md# 【M7 作战卡】CodeBench 门户联调、SC-01~SC-13 全量回归与单二进制交付
├── 05-adr/          # 架构决策记录 (ADR)：重大技术选型与权衡记录 (Architecture Decision Records)
│   ├── 01-microfrontend-and-table-prefix-isolation.md # ADR-01：微前端集成、共享库表前缀隔离与 TDD 测试策略
│   ├── 02-cpp-parser-and-code-ir-storage.md           # ADR-02：C/C++ tree-sitter 容错解析与 Code IR 压缩分片存储选型
│   ├── 03-evidence-anchor-and-publish-mount.md        # ADR-03：跨提交稳定证据锚点与 code-bench 符号链接挂载发布契约
│   └── 04-module-scope-discovery-and-hierarchical-docs.md # ADR-04（提议）：Monorepo 一层模块发现、整仓抽取按模块切片与层级文档汇总
└── 06-ops/          # 交付、运维与使用指南：双模联调、隔离测试、生产部署与模板配置手册
    ├── 01-verification-and-deployment-guide.md  # 联调验证、自动化回归测试与生产部署操作指南
    └── 02-code-ir-facets-subscription-guide.md  # Code IR 分片订阅 (ir_facets) 原理解析与配置使用指南
```

---

## 目录治理规则

1. **全链路双位编号递进**：
   - 一级子目录采用阶段双位数字前缀（`00-` ~ `06-`）；
   - 各阶段目录下的**所有 Markdown 文档必须带双位数字编号前缀**（如 `01-feature-matrix.md`、`01-system-architecture.md`、`01-http-api-contract.md`、`01-roadmap.md`），保证在文件树、IDE 及文档中心中保持严格时序与逻辑有序性。
2. **规范先行与测试驱动 (SDD + TDD)**：
   - 任何业务代码实施前，必须先在 `01-features/`、`02-design/`、`03-api/` 与 `04-tasks/` 中冻结设计契约与验收标准 (AC)；
   - 实施阶段遵循 **TDD（先写测试 -> 运行失败 Red -> 编写实现通过 Green -> 重构优化 Refactor）**，所有数据库单测必须通过 `testdb.SetupIsolatedDB` 实现物理隔离。
3. **决策可溯**：凡涉及关键架构选型、数据模型拆分、跨模块交互或性能权衡，须在 `05-adr/` 中以标准 ADR 模板记录背景与决策理由。
