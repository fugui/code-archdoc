import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import type { EngineInfoVO, GenTaskDetailVO, GenerateTaskSubmitResponse } from '../types/domain.ts';
import { ArchdocApiError, formatApiError } from './errorMessages.ts';
import {
  formatEngineLabel,
  getNonTerminalTaskIds,
  getSubmitButtonLabel,
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
