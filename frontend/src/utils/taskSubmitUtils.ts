import type {
  EngineInfoVO,
  GenEngine,
  GenTaskDetailVO,
  GenerateTaskSubmitResponse,
  TaskStatus,
} from '../types';

export const PREFERRED_ENGINE_STORAGE_KEY = 'archdoc.preferred_gen_engine';

const TERMINAL_STATUS_SET = new Set<TaskStatus>(['ready', 'failed', 'canceled']);

export interface SplitTasksSummary {
  total: number;
  readyCount: number;
  failedCount: number;
  canceledCount: number;
  activeCount: number;
  allTerminal: boolean;
  overallPercent: number;
}

/**
 * 兼容解析单任务与拆单响应：当 tasks[] 非空时优先返回拆单列表，否则退化为单元素数组。
 */
export function normalizeSubmittedTasks(res: GenerateTaskSubmitResponse): GenTaskDetailVO[] {
  if (Array.isArray(res.tasks) && res.tasks.length > 0) {
    return res.tasks;
  }
  return [res];
}

/**
 * 根据本地记忆偏好、服务端可用矩阵与默认推荐引擎解析最终默认选中引擎。
 */
export function resolvePreferredEngine(
  engines: EngineInfoVO[],
  defaultEngine: GenEngine,
  savedEngine?: string | null,
): GenEngine {
  const saved = (savedEngine ?? '').trim().toLowerCase();
  if (saved === 'native' || saved === 'opencode') {
    const matched = engines.find((e) => e.name === saved && e.available);
    if (matched) {
      return matched.name;
    }
  }
  const matchedDefault = engines.find((e) => e.name === defaultEngine && e.available);
  if (matchedDefault) {
    return matchedDefault.name;
  }
  const firstAvailable = engines.find((e) => e.available);
  return firstAvailable ? firstAvailable.name : 'native';
}

/**
 * 筛选仍处于非终态（pending/extracting/generating/verifying）的任务 ID，用于增量局部轮询。
 */
export function getNonTerminalTaskIds(tasks: GenTaskDetailVO[]): number[] {
  return tasks.filter((t) => !TERMINAL_STATUS_SET.has(t.status)).map((t) => t.id);
}

/**
 * 汇总多文档拆单任务的完成统计与总体进度百分比（全部终态直接收敛至 100%）。
 */
export function summarizeSplitTasks(tasks: GenTaskDetailVO[]): SplitTasksSummary {
  const total = tasks.length;
  if (total === 0) {
    return {
      total: 0,
      readyCount: 0,
      failedCount: 0,
      canceledCount: 0,
      activeCount: 0,
      allTerminal: true,
      overallPercent: 0,
    };
  }

  let readyCount = 0;
  let failedCount = 0;
  let canceledCount = 0;
  let activeCount = 0;
  let pctSum = 0;

  for (const t of tasks) {
    switch (t.status) {
      case 'ready':
        readyCount += 1;
        pctSum += 100;
        break;
      case 'failed':
        failedCount += 1;
        pctSum += 100;
        break;
      case 'canceled':
        canceledCount += 1;
        pctSum += 100;
        break;
      default:
        activeCount += 1;
        pctSum += Math.max(0, Math.min(100, t.progress_percent ?? 0));
        break;
    }
  }

  const allTerminal = activeCount === 0;
  const overallPercent = allTerminal ? 100 : Math.round(pctSum / total);

  return {
    total,
    readyCount,
    failedCount,
    canceledCount,
    activeCount,
    allTerminal,
    overallPercent,
  };
}

/**
 * 根据是否批量编排与勾选文档数计算动态主提交按钮文案。
 */
export function getSubmitButtonLabel(batchMode: boolean, selectedDocTypeCount: number): string {
  if (batchMode) {
    return '提交批量编排任务';
  }
  if (selectedDocTypeCount <= 1) {
    return '开始生成 (1 篇)';
  }
  return `创建 ${selectedDocTypeCount} 个文档生成任务`;
}

/**
 * 归一化展示任务或记录所采用的生成引擎标签。
 */
export function formatEngineLabel(engine?: GenEngine | string | null): 'Native' | 'OpenCode' {
  return (engine ?? '').trim().toLowerCase() === 'opencode' ? 'OpenCode' : 'Native';
}
