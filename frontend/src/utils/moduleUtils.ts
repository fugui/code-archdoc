import type { ModuleDiscoveryStrategy } from '../types';

export interface ModuleStrategyOption {
  value: ModuleDiscoveryStrategy;
  label: string;
}

export const MODULE_STRATEGY_OPTIONS: ReadonlyArray<ModuleStrategyOption> = [
  { value: 'merged', label: '混合发现（架构元素 + 一级源码目录，推荐）' },
  { value: 'top_level_dirs', label: '仅一级源码子目录' },
  { value: 'arch_elements', label: '仅架构元素表 (architecture_elements)' },
];

const VALID_MODULE_STRATEGIES = new Set<string>(['merged', 'top_level_dirs', 'arch_elements']);

/**
 * 规范化模块发现策略，非法或历史别名安全兜底为 merged。
 */
export function normalizeModuleStrategy(raw?: string | null): ModuleDiscoveryStrategy {
  const trimmed = (raw ?? '').trim();
  if (VALID_MODULE_STRATEGIES.has(trimmed)) {
    return trimmed as ModuleDiscoveryStrategy;
  }
  return 'merged';
}

/**
 * 映射后端模块/范围来源枚举为中文展示标签。
 */
export function formatModuleSourceLabel(source?: string | null): string {
  switch ((source ?? '').trim()) {
    case 'arch_element':
      return '架构元素';
    case 'discovered':
      return '一级目录';
    case 'orphan':
      return '孤立范围';
    default:
      return '';
  }
}

/**
 * 判断生成任务类型是否为批量编排父任务（对齐后端 models.TaskKindBatch = "batch"）。
 */
export function isBatchTaskKind(kind?: string | null): boolean {
  return (kind ?? '').trim() === 'batch';
}
