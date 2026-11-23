import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Archive,
  BookOpen,
  FolderTree,
  Layers3,
  Loader2,
  MoreHorizontal,
  Sparkles,
} from 'lucide-react';
import { useOutsideClick } from '@code/common';
import type { RepoScopeCoverageVO } from '../../types';

export interface CoverageActionButtonsProps {
  row: RepoScopeCoverageVO;
  exporting: boolean;
  approvedCount: number;
  preferGeneratePrimary?: boolean;
  hideDisabledExport?: boolean;
  tooltipPlacement?: 'top' | 'bottom';
  onOpenStudio: () => void;
  onTriggerGenerate: () => void;
  onBatchGenerate?: () => void;
  onOpenModuleGovernance?: () => void;
  onExportZip: () => void;
}

interface SecondaryActionItem {
  key: string;
  label: string;
  title: string;
  icon: React.ReactNode;
  disabled: boolean;
  onClick: () => void;
}

export const CoverageActionButtons: React.FC<CoverageActionButtonsProps> = ({
  row,
  exporting,
  approvedCount,
  preferGeneratePrimary = false,
  hideDisabledExport = false,
  tooltipPlacement = 'top',
  onOpenStudio,
  onTriggerGenerate,
  onBatchGenerate,
  onOpenModuleGovernance,
  onExportZip,
}) => {
  const [menuOpen, setMenuOpen] = useState<boolean>(false);
  const menuContainerRef = useRef<HTMLDivElement | null>(null);

  const closeMenu = useCallback(() => {
    setMenuOpen(false);
  }, []);

  useOutsideClick(menuContainerRef, closeMenu);

  useEffect(() => {
    if (!menuOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuOpen(false);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [menuOpen]);

  const generateTip = row.can_write
    ? '触发 AI 设计文档反推生成'
    : '权限不足：仅仓库/子目录负责人或管理员可触发生成';
  const batchTip = row.can_write
    ? '批量生成活跃子模块与整仓分层文档'
    : '权限不足：仅仓库负责人或管理员可发起批量生成';
  const exportTip = exporting
    ? '正在打包导出 ZIP...'
    : approvedCount > 0
      ? `打包导出当前范围 ${approvedCount} 篇已发布文档 ZIP`
      : '当前范围暂无已发布定稿文档可导出';

  const secondaryActions: SecondaryActionItem[] = [];
  if (row.scope_type === 'repo' && onBatchGenerate) {
    secondaryActions.push({
      key: 'batch',
      label: '批量生成子模块',
      title: batchTip,
      icon: <Layers3 size={14} />,
      disabled: !row.can_write,
      onClick: onBatchGenerate,
    });
  }
  if (row.scope_type === 'repo' && onOpenModuleGovernance) {
    secondaryActions.push({
      key: 'governance',
      label: '模块发现与目录迁移',
      title: '配置模块发现策略、扫描子目录或迁移重命名文档',
      icon: <FolderTree size={14} />,
      disabled: false,
      onClick: onOpenModuleGovernance,
    });
  }
  secondaryActions.push({
    key: 'export',
    label: exporting ? '正在打包 ZIP...' : '导出已发布 ZIP',
    title: exportTip,
    icon: exporting ? <Loader2 size={14} className="code-archdoc-spin" /> : <Archive size={14} />,
    disabled: approvedCount === 0 || exporting,
    onClick: onExportZip,
  });

  const singleSecondary = secondaryActions.length === 1 ? secondaryActions[0] : null;
  const shouldHideSingleSecondary = Boolean(
    hideDisabledExport && singleSecondary?.key === 'export' && singleSecondary.disabled,
  );
  const primaryGenerate = Boolean(preferGeneratePrimary && row.can_write);

  const studioButton = (
    <button
      key="studio"
      type="button"
      className={`code-archdoc-row-btn ${!primaryGenerate ? 'code-archdoc-row-btn--primary' : ''}`}
      title="进入 Canvas Studio 工作台"
      onClick={onOpenStudio}
    >
      <BookOpen size={14} />
      <span>工作台</span>
    </button>
  );

  const generateButton = (
    <button
      key="generate"
      type="button"
      className={`code-archdoc-row-btn ${primaryGenerate ? 'code-archdoc-row-btn--primary' : ''}`}
      disabled={!row.can_write}
      title={generateTip}
      onClick={onTriggerGenerate}
    >
      <Sparkles size={14} />
      <span>生成</span>
    </button>
  );

  return (
    <div className="code-archdoc-row-actions">
      {primaryGenerate ? (
        <>
          {generateButton}
          {studioButton}
        </>
      ) : (
        <>
          {studioButton}
          {generateButton}
        </>
      )}

      {singleSecondary ? (
        shouldHideSingleSecondary ? null : (
          <button
            type="button"
            className="code-archdoc-icon-btn"
            disabled={singleSecondary.disabled}
            data-tooltip={singleSecondary.title}
            data-tooltip-pos={tooltipPlacement}
            aria-label={singleSecondary.title}
            onClick={singleSecondary.onClick}
          >
            {singleSecondary.icon}
          </button>
        )
      ) : (
        <div className="code-archdoc-action-menu-wrap" ref={menuContainerRef}>
          <button
            type="button"
            className={`code-archdoc-icon-btn ${menuOpen ? 'code-archdoc-icon-btn--active' : ''}`}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-label="更多操作"
            title="更多操作（批量生成、模块治理与导出）"
            onClick={() => setMenuOpen((prev) => !prev)}
          >
            <MoreHorizontal size={15} />
          </button>

          {menuOpen && (
            <div
              className={`code-archdoc-action-menu ${
                tooltipPlacement === 'bottom' ? 'code-archdoc-action-menu--down' : ''
              }`}
              role="menu"
              aria-label="仓库更多操作"
            >
              {secondaryActions.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  role="menuitem"
                  className="code-archdoc-action-menu-item"
                  disabled={item.disabled}
                  title={item.title}
                  onClick={() => {
                    setMenuOpen(false);
                    item.onClick();
                  }}
                >
                  {item.icon}
                  <span>{item.label}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default CoverageActionButtons;
