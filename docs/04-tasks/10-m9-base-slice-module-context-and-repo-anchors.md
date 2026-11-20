# Milestone 9 作战卡：整仓基线抽取与纯函数切片、`module_context`、仓库级锚点与外部契约漂移

- **文档编号**：`10-m9-base-slice-module-context-and-repo-anchors.md`
- **归属阶段**：ADR-04 演进里程碑 M9（前置：M8 完成）
- **覆盖特性与场景**：`F-30`、`SC-16`（切片部分）、`SC-17`、`T-ADR04-03`、`T-ADR04-05`、`T-ADR04-06`、`T-ADR04-07`、`T-ADR04-08`

---

## 1. 任务拆解

1. **Code IR Schema `1.1` 增量提取（`pkg/codeir/`）**：
   - `SchemaVersion` 升级为 `"1.1"`；
   - 新增 `FileImportFact`、`BuildSubdirFact`、`BuildIncludeDirFact` 与第 10 分片 `ModuleContextFacet`；
   - 增强既有提取器（`CommonExtractor`、`BuildExtractor`、`ProtoExtractor`、`JavaExtractor`、`PythonExtractor`）采集文件级 import/include、CMake `add_subdirectory`/`target_include_directories`/`include_directories` 与 Maven `<modules>`。
2. **纯函数切片与跨模块上下文构建（`pkg/codeir/slice.go`）**：
   - 实现 `SliceByModule(base, subdir, modules)`：路径剥离重写、Python `FQSymbol` 模块前缀剥离、跨模块 `DeclSpan` 清理、`Tree`/`Deps.InternalModules` 重算；
   - 实现确定性依赖归属解析（C/C++ `#include` 含 `-I` 搜索、Java 包映射、Python 包路径、Proto import、构建目标链接），构建 `OutboundDeps`/`InboundDeps`/`ExportedSurface`/`ConsumedContracts`/`RepoBuildContext`（Top-20/Top-50/4K Token 截断）；
   - 实现 `BuildCrossModuleGraph` 汇总整仓跨模块依赖边。
3. **基线一次抽取 + 切片缓存 + 超大仓降级（`services/snapshot_service.go`）**：
   - `EnsureSnapshot` 分层 `singleflight`（`base:<repo>|<branch>` 与 `slice:<baseID>|<subdir>`）；
   - 超 `extract.max_repo_files` / `extract.max_repo_duration_seconds` 自动降级为按子目录独立抽取 + 复用既有提取器的轻量全仓依赖索引，写入 `StatsJSON.degraded_reason`；
   - 基线抽取完成后自动触发模块索引同步（如已注入回调）。
4. **Prompt 注入、外部工具白名单、`scope=repo` Verifier/预览与 Code Drift**：
   - `pkg/doctype/schema.go` 与内置 `meta.json` 支持 `context_inputs` / `repo_ir_facets` / `module_digest_source`；
   - `pkg/llm/prompt.go` 注入 `module_context` 数据栅栏块；`pkg/llm/source_tools.go` 增加 `ExternalFileWhitelist`；
   - `pkg/verifier/` 校验 `scope="repo"` 锚点必须命中 `module_context` 白名单，否则强制 `error` + `can_approve=false`；
   - `pkg/publisher/renderer.go` 为 `scope="repo"` 深链追加 `&scope=repo`；`SourcePreviewService` 校验 `scope=repo` 白名单防越权（`403`）；
   - `pkg/drift/code_drift.go` 对 `scope="repo"` 锚点直接匹配仓库相对路径并产出 `external_contract_modified`/`external_contract_removed`（优先级低于模块内原因）。

## 2. TDD 先行测试用例清单

| 编号 | 测试名称 | 核心断言 |
| :--- | :--- | :--- |
| **T-ADR04-03** | `TestConcurrentFiveModulesSingleSyncSingleExtract` | 同仓 5 模块并发：`GitSyncer.Sync` = 1、整仓 L1 抽取 = 1 |
| **T-ADR04-05** | `TestSliceByModuleGoldenEquivalence` | 切片路径全部以模块为根，与旧版按子目录抽取结果逐分片等价 |
| **T-ADR04-06** | `TestModuleContextIncludeResolutionWithIncludeDirs` | `order/a.cpp` `#include "pay/api.h"` 经 `-I` 解析命中：`order.OutboundDeps` 含 `pay(exact)`，`pay.InboundDeps` 含 `order` |
| **T-ADR04-07** | `TestCodeDriftExternalContractScopeRepo` | `pay/api.h::PayClient::Charge` 指纹变化触发 `order` 对应章节 `external_contract_modified` |
| **T-ADR04-08** | `TestVerifierAndPreviewRejectOutOfWhitelistRepoAnchor` | 非白名单 `scope=repo` 锚点被 Verifier 判 `error` (`can_approve=false`)；源码预览越界路径返 `403` |

## 3. 验收门禁

```bash
make lint-arch && make eval-baseline && make test
```
