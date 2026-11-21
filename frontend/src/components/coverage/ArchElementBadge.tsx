import React from 'react';
import type { RepoScopeCoverageVO } from '../../types';
import { formatArchElementTypeLabel } from '../../utils/docTypeUtils';

export interface ArchElementBadgeProps {
  row: RepoScopeCoverageVO;
}

function elementBadgeModifier(elemType?: string): string {
  switch (elemType) {
    case 'subsystem':
      return 'code-badge--primary';
    case 'group':
      return 'code-badge--orange';
    case 'module':
      return 'code-badge--success';
    default:
      return 'code-badge--muted';
  }
}

export const ArchElementBadge: React.FC<ArchElementBadgeProps> = ({ row }) => {
  if (row.arch_element_id) {
    const typeLabel = formatArchElementTypeLabel(row.arch_element_type);
    const pathParts = (row.arch_path ?? '')
      .split(' / ')
      .map((s) => s.trim())
      .filter(Boolean);
    const parentPath = pathParts.length > 1 ? pathParts.slice(0, -1).join(' › ') : '';

    return (
      <div className="code-archdoc-arch-cell">
        <div className="code-archdoc-arch-main">
          <span className={`code-badge ${elementBadgeModifier(row.arch_element_type)}`}>
            {typeLabel}
          </span>
          <span className="code-archdoc-arch-name" title={row.arch_path || row.arch_element_name}>
            {row.arch_element_name}
          </span>
        </div>
        {parentPath ? (
          <div className="code-archdoc-arch-path" title={row.arch_path}>
            {parentPath}
          </div>
        ) : (
          row.subsystem_name &&
          row.subsystem_name !== row.arch_element_name && (
            <div className="code-archdoc-arch-path">子系统：{row.subsystem_name}</div>
          )
        )}
      </div>
    );
  }

  return (
    <div className="code-archdoc-arch-cell">
      <span
        className="code-archdoc-arch-unbound"
        title="尚未在平台「架构管理」关联具体架构元素，可在平台维护映射以明确业务归属与责任人"
      >
        未关联架构元素
      </span>
      {row.subsystem_name && (
        <div className="code-archdoc-arch-path">子系统：{row.subsystem_name}</div>
      )}
    </div>
  );
};

export default ArchElementBadge;
