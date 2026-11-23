import type { ArchTreeNodeVO, ArchTreeSummaryVO } from '../types';

export type MicroStatTone = 'default' | 'success' | 'warning' | 'purple' | 'info';

export interface MicroStatPillItem {
  key: string;
  label: string;
  value: string | number;
  tone?: MicroStatTone;
  active?: boolean;
  title?: string;
  onClick?: () => void;
}

export interface FlattenedArchTreeRow {
  node: ArchTreeNodeVO;
  depth: number;
  hasChildren: boolean;
  isExpanded: boolean;
  rowKey: string;
}

/**
 * 收集所有可展开的架构节点 ID；传入 depthLimit 时仅收集 depth < depthLimit 的节点。
 */
export function collectArchTreeExpandableIds(
  nodes: readonly ArchTreeNodeVO[],
  depthLimit?: number,
): number[] {
  const out: number[] = [];
  const walk = (list: readonly ArchTreeNodeVO[], depth: number) => {
    for (const node of list) {
      const hasChildren = node.children.length > 0;
      if (hasChildren && (depthLimit === undefined || depth < depthLimit)) {
        out.push(node.id);
      }
      if (hasChildren) {
        walk(node.children, depth + 1);
      }
    }
  };
  walk(nodes, 0);
  return out;
}

/**
 * 计算默认展开节点 ID 集合：筛选态或模块总数 <= 阈值(默认 50) 时全展开，超大树仅展开第 1 层。
 */
export function computeDefaultExpandedArchTreeIds(
  nodes: readonly ArchTreeNodeVO[],
  totalModules: number,
  hasActiveFilters: boolean,
  threshold = 50,
): number[] {
  if (hasActiveFilters || totalModules <= threshold) {
    return collectArchTreeExpandableIds(nodes);
  }
  return collectArchTreeExpandableIds(nodes, 1);
}

/**
 * 按受控展开集合将架构树拍平为单表渲染行，模块行 Key 统一为 module:${id} 防止同 Scope 碰撞。
 */
export function flattenArchTreeRows(
  nodes: readonly ArchTreeNodeVO[],
  expandedNodeIds: ReadonlySet<number>,
): FlattenedArchTreeRow[] {
  const rows: FlattenedArchTreeRow[] = [];
  const walk = (list: readonly ArchTreeNodeVO[], depth: number) => {
    for (const node of list) {
      const hasChildren = node.children.length > 0;
      const isExpanded = hasChildren && expandedNodeIds.has(node.id);
      const rowKey = node.type === 'module' ? `module:${node.id}` : `node:${node.id}`;
      rows.push({
        node,
        depth,
        hasChildren,
        isExpanded,
        rowKey,
      });
      if (isExpanded) {
        walk(node.children, depth + 1);
      }
    }
  };
  walk(nodes, 0);
  return rows;
}

/**
 * 构造「架构文档全景」(/arch-tree) 顶部 4 项极简摘要微标（模块口径，与 /repos 解耦）。
 */
export function buildArchTreeMicroPills(
  summary: ArchTreeSummaryVO,
  activeStatus: string,
  onToggleStatus: (nextStatus: 'approved' | 'drifted' | '') => void,
): MicroStatPillItem[] {
  return [
    {
      key: 'total_modules',
      label: '功能模块',
      value: summary.total_modules,
      title: `架构功能模块总数（已绑定活跃代码仓 ${summary.bound_modules} / ${summary.total_modules}）`,
    },
    {
      key: 'covered_modules',
      label: '已覆盖',
      value: summary.covered_modules,
      tone: 'purple',
      title: '已生成至少 1 篇设计文档的功能模块数',
    },
    {
      key: 'approved_docs',
      label: '已发布',
      value: summary.approved_docs,
      tone: 'success',
      active: activeStatus === 'approved',
      title: '点击按「已发布」筛选 / 取消筛选',
      onClick: () => onToggleStatus(activeStatus === 'approved' ? '' : 'approved'),
    },
    {
      key: 'drifted_docs',
      label: '漂移',
      value: summary.drifted_docs,
      tone: summary.drifted_docs > 0 ? 'warning' : 'default',
      active: activeStatus === 'drifted',
      title: '点击按「存在漂移」筛选 / 取消筛选',
      onClick: () => onToggleStatus(activeStatus === 'drifted' ? '' : 'drifted'),
    },
  ];
}
