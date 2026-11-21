import React from 'react';

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

interface OverviewMicroPillsProps {
  items: MicroStatPillItem[];
  ariaLabel?: string;
}

/**
 * 列表页顶部极简摘要微标组：零额外垂直行高，支持点击快捷筛选下钻。
 */
export const OverviewMicroPills: React.FC<OverviewMicroPillsProps> = ({
  items,
  ariaLabel = '快捷状态摘要',
}) => {
  if (items.length === 0) return null;

  return (
    <div className="code-archdoc-micro-stats" role="group" aria-label={ariaLabel}>
      {items.map((item) => {
        const toneClass =
          item.tone && item.tone !== 'default' ? `code-archdoc-micro-pill--${item.tone}` : '';
        const activeClass = item.active ? 'code-archdoc-micro-pill--active' : '';
        const interactiveClass = item.onClick ? 'code-archdoc-micro-pill--interactive' : '';
        const className = [
          'code-archdoc-micro-pill',
          toneClass,
          activeClass,
          interactiveClass,
        ]
          .filter(Boolean)
          .join(' ');

        const content = (
          <>
            <span>{item.label}</span>
            <span className="code-archdoc-micro-pill-val">{item.value}</span>
          </>
        );

        if (item.onClick) {
          return (
            <button
              key={item.key}
              type="button"
              className={className}
              onClick={item.onClick}
              aria-pressed={Boolean(item.active)}
              title={item.title}
            >
              {content}
            </button>
          );
        }

        return (
          <span key={item.key} className={className} title={item.title}>
            {content}
          </span>
        );
      })}
    </div>
  );
};

export default OverviewMicroPills;

/**
 * 构造「仓库文档覆盖」与「架构文档全景」共享的 4 项极简摘要微标配置。
 */
export function buildCoverageMicroPills(
  summary: {
    total_scopes: number;
    covered_scopes: number;
    approved_docs: number;
    drifted_docs: number;
  },
  activeStatus: string,
  onToggleStatus: (nextStatus: 'approved' | 'drifted' | '') => void,
): MicroStatPillItem[] {
  return [
    {
      key: 'total_scopes',
      label: '范围',
      value: summary.total_scopes,
      title: '纳管代码仓与子目录范围总数（详情见全局概览）',
    },
    {
      key: 'covered_scopes',
      label: '已覆盖',
      value: summary.covered_scopes,
      tone: 'purple',
      title: '已生成至少 1 篇设计文档的范围数',
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
