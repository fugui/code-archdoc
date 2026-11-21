import React from 'react';
import { Archive, BookOpen, FolderTree, Layers3, Loader2, Sparkles } from 'lucide-react';
import type { RepoScopeCoverageVO } from '../../types';

export interface CoverageActionButtonsProps {
  row: RepoScopeCoverageVO;
  exporting: boolean;
  approvedCount: number;
  tooltipPlacement?: 'top' | 'bottom';
  onOpenStudio: () => void;
  onTriggerGenerate: () => void;
  onBatchGenerate?: () => void;
  onOpenModuleGovernance?: () => void;
  onExportZip: () => void;
}

export const CoverageActionButtons: React.FC<CoverageActionButtonsProps> = ({
  row,
  exporting,
  approvedCount,
  tooltipPlacement = 'top',
  onOpenStudio,
  onTriggerGenerate,
  onBatchGenerate,
  onOpenModuleGovernance,
  onExportZip,
}) => {
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

  return (
    <div className="code-archdoc-icon-actions">
      <button
        type="button"
        className="code-archdoc-icon-btn code-archdoc-icon-btn--primary"
        data-tooltip="进入 Canvas Studio 工作台"
        data-tooltip-pos={tooltipPlacement}
        aria-label="进入 Canvas Studio 工作台"
        onClick={onOpenStudio}
      >
        <BookOpen size={15} />
      </button>

      <button
        type="button"
        className="code-archdoc-icon-btn"
        disabled={!row.can_write}
        data-tooltip={generateTip}
        data-tooltip-pos={tooltipPlacement}
        aria-label={generateTip}
        onClick={onTriggerGenerate}
      >
        <Sparkles size={15} />
      </button>

      {row.scope_type === 'repo' && onBatchGenerate && (
        <button
          type="button"
          className="code-archdoc-icon-btn"
          disabled={!row.can_write}
          data-tooltip={batchTip}
          data-tooltip-pos={tooltipPlacement}
          aria-label={batchTip}
          onClick={onBatchGenerate}
        >
          <Layers3 size={15} />
        </button>
      )}

      {row.scope_type === 'repo' && onOpenModuleGovernance && (
        <button
          type="button"
          className="code-archdoc-icon-btn"
          data-tooltip="模块发现策略、子目录扫描与文档迁移治理"
          data-tooltip-pos={tooltipPlacement}
          aria-label="模块发现策略、子目录扫描与文档迁移治理"
          onClick={onOpenModuleGovernance}
        >
          <FolderTree size={15} />
        </button>
      )}

      <button
        type="button"
        className="code-archdoc-icon-btn"
        disabled={approvedCount === 0 || exporting}
        data-tooltip={exportTip}
        data-tooltip-pos={tooltipPlacement}
        aria-label={exportTip}
        onClick={onExportZip}
      >
        {exporting ? <Loader2 size={15} className="code-archdoc-spin" /> : <Archive size={15} />}
      </button>
    </div>
  );
};

export default CoverageActionButtons;
