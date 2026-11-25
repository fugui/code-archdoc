import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import type { EngineInfoVO, GenTaskDetailVO, GenerateTaskSubmitResponse } from '../types/domain.ts';
import { ArchdocApiError, formatApiError } from './errorMessages.ts';
import {
  formatEngineLabel,
  getNonTerminalTaskIds,
  getSubmitButtonLabel,
  getTaskRetryActionMeta,
  normalizeSubmittedTasks,
  resolvePreferredEngine,
  summarizeSplitTasks,
} from './taskSubmitUtils.ts';

function makeTask(id: number, docType: string, status: GenTaskDetailVO['status'], progress: number): GenTaskDetailVO {
  return {
    id,
    repo_id: 1,
    subdirectory: '',
    ref_name: 'main',
    task_kind: 'generate',
    engine: 'opencode',
    priority: 'interactive',
    doc_types: [docType],
    status,
    progress_percent: progress,
    estimated_tokens: 0,
    triggered_by: 1,
    started_at: null,
    finished_at: null,
    created_at: '2026-01-01T00:00:00Z',
  };
}

describe('T-ADR08-14：双引擎偏好记忆与动态提交按钮文案', () => {
  const bothEngines: EngineInfoVO[] = [
    { name: 'native', display_name: 'Native 内置引擎', available: true, default: true },
    { name: 'opencode', display_name: 'OpenCode 深度探索引擎', available: true, default: false, version: '1.18.21' },
  ];

  const onlyNativeEngines: EngineInfoVO[] = [
    { name: 'native', display_name: 'Native 内置引擎', available: true, default: true },
    { name: 'opencode', display_name: 'OpenCode 深度探索引擎', available: false, default: false, reason: 'CLI 未安装' },
  ];

  it('当 OpenCode 可用且本地记住偏好时优先选中 opencode，不可用时自动安全回退 native', () => {
    assert.equal(resolvePreferredEngine(bothEngines, 'native', 'opencode'), 'opencode');
    assert.equal(resolvePreferredEngine(onlyNativeEngines, 'native', 'opencode'), 'native');
    assert.equal(resolvePreferredEngine(bothEngines, 'opencode', null), 'opencode');
    assert.equal(resolvePreferredEngine(onlyNativeEngines, 'opencode', ''), 'native');
    assert.equal(resolvePreferredEngine(bothEngines, 'native', 'invalid'), 'native');
  });

  it('按单篇、多篇拆单、批量编排精确生成提交按钮文案', () => {
    assert.equal(getSubmitButtonLabel(false, 1), '开始生成 (1 篇)');
    assert.equal(getSubmitButtonLabel(false, 3), '创建 3 个文档生成任务');
    assert.equal(getSubmitButtonLabel(true, 3), '提交批量编排任务');
  });
});

describe('T-ADR08-15：多文档拆单规范化与增量非终态轮询汇聚', () => {
  it('优先拆解后端 tasks[]，缺失时兼容降级为单任务数组', () => {
    const t1 = makeTask(101, 'system-architecture', 'generating', 40);
    const t2 = makeTask(102, 'ops-and-config', 'ready', 100);
    const splitResp: GenerateTaskSubmitResponse = {
      ...t1,
      created_count: 2,
      tasks: [t1, t2],
    };
    const normalized = normalizeSubmittedTasks(splitResp);
    assert.equal(normalized.length, 2);
    assert.deepEqual(
      normalized.map((t) => t.id),
      [101, 102],
    );

    const legacyResp: GenerateTaskSubmitResponse = { ...t1 };
    const normalizedLegacy = normalizeSubmittedTasks(legacyResp);
    assert.equal(normalizedLegacy.length, 1);
    assert.equal(normalizedLegacy[0].id, 101);
  });

  it('仅过滤非终态任务 ID 并正确聚合拆单总体进度', () => {
    const tActive1 = makeTask(101, 'system-architecture', 'generating', 50);
    const tActive2 = makeTask(102, 'api-and-protocol', 'pending', 10);
    const tDone = makeTask(103, 'ops-and-config', 'ready', 100);
    const tFail = makeTask(104, 'agents-context', 'failed', 40);

    assert.deepEqual(getNonTerminalTaskIds([tActive1, tDone, tFail, tActive2]), [101, 102]);
    const summaryMid = summarizeSplitTasks([tActive1, tActive2, tDone, tFail]);
    assert.equal(summaryMid.total, 4);
    assert.equal(summaryMid.readyCount, 1);
    assert.equal(summaryMid.failedCount, 1);
    assert.equal(summaryMid.activeCount, 2);
    assert.equal(summaryMid.allTerminal, false);
    assert.equal(summaryMid.overallPercent, 65); // (50 + 10 + 100 + 100) / 4 = 65

    const summaryDone = summarizeSplitTasks([tDone, tFail]);
    assert.equal(summaryDone.allTerminal, true);
    assert.equal(summaryDone.overallPercent, 100);
  });
});

describe('T-ADR08-16：ENGINE_UNAVAILABLE 错误映射与引擎标签格式化', () => {
  it('正确格式化 ENGINE_UNAVAILABLE 中文标题与原因并归一引擎显示名称', () => {
    const err = new ArchdocApiError(400, {
      code: 'ENGINE_UNAVAILABLE',
      error: '未检测到 opencode 二进制',
    });
    const formatted = formatApiError(err);
    assert.equal(formatted.title, '所选生成引擎不可用');
    assert.equal(formatted.message, '未检测到 opencode 二进制');
    assert.equal(formatted.code, 'ENGINE_UNAVAILABLE');

    assert.equal(formatEngineLabel('opencode'), 'OpenCode');
    assert.equal(formatEngineLabel('native'), 'Native');
    assert.equal(formatEngineLabel(undefined), 'Native');
  });
});

describe('任务中心重试与重新生成动作元数据推导 (getTaskRetryActionMeta)', () => {
  it('失败与取消任务展示「重试」，其中失败任务高亮主按钮，无写权限或非终态不可操作', () => {
    const failedTask: GenTaskDetailVO = {
      ...makeTask(201, 'system-architecture', 'failed', 40),
      repo_name: 'order-service',
      can_write: true,
    };
    const failMeta = getTaskRetryActionMeta(failedTask);
    assert.equal(failMeta.canRetry, true);
    assert.equal(failMeta.isFailureRetry, true);
    assert.equal(failMeta.label, '重试');
    assert.equal(failMeta.modalLabel, '重试任务');
    assert.equal(failMeta.buttonVariant, 'btn-primary');
    assert.deepEqual(failMeta.payload, {});

    const canceledMeta = getTaskRetryActionMeta({
      ...failedTask,
      status: 'canceled',
    });
    assert.equal(canceledMeta.canRetry, true);
    assert.equal(canceledMeta.isFailureRetry, true);
    assert.equal(canceledMeta.label, '重试');
    assert.equal(canceledMeta.buttonVariant, 'btn-outline');

    const noPermMeta = getTaskRetryActionMeta({ ...failedTask, can_write: false });
    assert.equal(noPermMeta.canRetry, false);

    const runningMeta = getTaskRetryActionMeta({
      ...failedTask,
      status: 'generating',
      can_write: true,
    });
    assert.equal(runningMeta.canRetry, false);
  });

  it('已完成单任务与增量刷新任务展示「重新生成」并推导对应请求负载', () => {
    const readyTask: GenTaskDetailVO = {
      ...makeTask(202, 'data-and-contract', 'ready', 100),
      repo_name: 'order-service',
      can_write: true,
    };
    const readyMeta = getTaskRetryActionMeta(readyTask);
    assert.equal(readyMeta.canRetry, true);
    assert.equal(readyMeta.isFailureRetry, false);
    assert.equal(readyMeta.label, '重新生成');
    assert.equal(readyMeta.buttonVariant, 'btn-outline');
    assert.deepEqual(readyMeta.payload, { mode: 'regenerate' });

    const incReadyMeta = getTaskRetryActionMeta({
      ...readyTask,
      task_kind: 'incremental_refresh',
    });
    assert.equal(incReadyMeta.canRetry, true);
    assert.equal(incReadyMeta.label, '重新生成');
    assert.deepEqual(incReadyMeta.payload, {});
  });

  it('批量任务存在失败或取消子任务时展示「重试」并传 only_failed=true，全成功时展示「重新生成」', () => {
    const batchPartialFail: GenTaskDetailVO = {
      ...makeTask(301, 'system-architecture', 'ready', 100),
      task_kind: 'batch',
      can_write: true,
      batch_summary: {
        total_children: 3,
        completed_children: 2,
        failed_children: 1,
        canceled_children: 0,
        running_children: 0,
      },
    };
    const partialMeta = getTaskRetryActionMeta(batchPartialFail);
    assert.equal(partialMeta.canRetry, true);
    assert.equal(partialMeta.isFailureRetry, true);
    assert.equal(partialMeta.label, '重试');
    assert.equal(partialMeta.modalLabel, '重试失败子任务');
    assert.equal(partialMeta.buttonVariant, 'btn-primary');
    assert.deepEqual(partialMeta.payload, { only_failed: true });

    const batchAllReady: GenTaskDetailVO = {
      ...batchPartialFail,
      batch_summary: {
        total_children: 3,
        completed_children: 3,
        failed_children: 0,
        canceled_children: 0,
        running_children: 0,
      },
    };
    const fullRegenMeta = getTaskRetryActionMeta(batchAllReady);
    assert.equal(fullRegenMeta.canRetry, true);
    assert.equal(fullRegenMeta.isFailureRetry, false);
    assert.equal(fullRegenMeta.label, '重新生成');
    assert.equal(fullRegenMeta.buttonVariant, 'btn-outline');
    assert.deepEqual(fullRegenMeta.payload, { mode: 'regenerate', only_failed: false });
  });
});
