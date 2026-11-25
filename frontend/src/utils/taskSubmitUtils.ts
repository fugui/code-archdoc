import type {
  EngineInfoVO,
  GenEngine,
  GenTaskDetailVO,
  GenerateTaskSubmitResponse,
  RetryTaskPayload,
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

export interface TaskRetryActionMeta {
  canRetry: boolean;
  isFailureRetry: boolean;
  label: string;
  modalLabel: string;
  buttonVariant: 'btn-primary' | 'btn-outline';
  tooltip: string;
  confirmTitle: string;
  confirmContent: string;
  confirmText: string;
  payload: RetryTaskPayload;
}

/**
 * 统一推导任务级「重试 / 重新生成」按钮文案、样式变体、二次确认提示及后端入参。
 */
export function getTaskRetryActionMeta(
  task: GenTaskDetailVO,
  canWrite?: boolean,
): TaskRetryActionMeta {
  const resolvedCanWrite = canWrite ?? Boolean(task.can_write);
  const isTerminal = TERMINAL_STATUS_SET.has(task.status);
  const abnormalChildren =
    (task.batch_summary?.failed_children ?? 0) + (task.batch_summary?.canceled_children ?? 0);
  const isBatchPartialFail = task.task_kind === 'batch' && abnormalChildren > 0;
  const isFailureRetry =
    task.status === 'failed' || task.status === 'canceled' || isBatchPartialFail;
  const canRetry = isTerminal && resolvedCanWrite;

  const repoLabel = task.repo_name || `仓库 #${task.repo_id}`;
  const scopeLabel = task.subdirectory || '整仓 (Root)';
  const engineLabel = formatEngineLabel(task.engine);

  if (isFailureRetry) {
    const payload: RetryTaskPayload = task.task_kind === 'batch' ? { only_failed: true } : {};
    const confirmContent = isBatchPartialFail
      ? `即将为任务 #${task.id}（${repoLabel} · ${engineLabel}）创建新批量任务，仅重试其中 ${abnormalChildren} 个失败/取消子任务（已完成子任务不受影响），是否继续？`
      : task.task_kind === 'incremental_refresh'
        ? `即将基于任务 #${task.id}（${repoLabel} / ${scopeLabel} · ${engineLabel}）创建新的增量刷新任务，仅刷新未锁定的受影响章节，是否继续？`
        : `即将基于任务 #${task.id}（${repoLabel} / ${scopeLabel} · ${engineLabel}）创建新任务重新执行，人工锁定章节将严格保留，是否继续？`;

    return {
      canRetry,
      isFailureRetry: true,
      label: '重试',
      modalLabel: isBatchPartialFail ? '重试失败子任务' : '重试任务',
      buttonVariant: task.status === 'failed' || isBatchPartialFail ? 'btn-primary' : 'btn-outline',
      tooltip: isBatchPartialFail
        ? `仅针对 ${abnormalChildren} 个失败/取消子任务发起重试`
        : '基于原任务配置创建新任务重新执行',
      confirmTitle: isBatchPartialFail ? '确认重试失败子任务' : '确认重试生成任务',
      confirmContent,
      confirmText: isBatchPartialFail ? '重试失败子任务' : '立即重试',
      payload,
    };
  }

  const payload: RetryTaskPayload =
    task.task_kind === 'batch'
      ? { mode: 'regenerate', only_failed: false }
      : task.task_kind === 'incremental_refresh'
        ? {}
        : { mode: 'regenerate' };

  const confirmContent =
    task.task_kind === 'batch'
      ? `即将基于任务 #${task.id}（${repoLabel} · ${engineLabel}）的批量范围发起全量重新生成，人工锁定章节将严格保留，是否继续？`
      : task.task_kind === 'incremental_refresh'
        ? `即将基于任务 #${task.id}（${repoLabel} / ${scopeLabel} · ${engineLabel}）再次发起章节级增量刷新，人工锁定章节将严格保留，是否继续？`
        : `即将基于任务 #${task.id}（${repoLabel} / ${scopeLabel} · ${engineLabel}）基于最新提交发起全量重新生成，人工锁定章节将严格保留，是否继续？`;

  return {
    canRetry,
    isFailureRetry: false,
    label: '重新生成',
    modalLabel: '重新生成',
    buttonVariant: 'btn-outline',
    tooltip: '基于最新代码提交创建新任务重新生成（保留人工锁定章节）',
    confirmTitle: '确认重新生成文档',
    confirmContent,
    confirmText: '确认重新生成',
    payload,
  };
}
