import React from 'react';
import { Activity, CornerDownRight, FolderTree, Loader2 } from 'lucide-react';
import type { RepoScopeCoverageVO } from '../../types';
import { formatModuleSourceLabel } from '../../utils/moduleUtils';

export interface ScopeIdentityCellProps {
  row: RepoScopeCoverageVO;
  showSelection?: boolean;
  isSelected?: boolean;
  isDiscovering?: boolean;
  onToggleSelect?: () => void;
  onDiscoverModules?: () => void;
  onOpenModuleGovernance?: () => void;
  onOpenActiveTask?: () => void;
}

export const ScopeIdentityCell: React.FC<ScopeIdentityCellProps> = ({
  row,
  showSelection = false,
  isSelected = false,
  isDiscovering = false,
  onToggleSelect,
  onDiscoverModules,
  onOpenModuleGovernance,
  onOpenActiveTask,
}) => {
  const isSubdirRow = row.scope_type === 'subdir';
  const canSelectSubdir =
    showSelection && isSubdirRow && row.can_write && row.scope_status !== 'missing';
  const activeModuleCount = row.active_module_count ?? 0;
  const sourceLabel = formatModuleSourceLabel(
    row.scope_status === 'orphan' ? 'orphan' : row.module_source,
  );

  return (
    <div
      className={`code-archdoc-coverage-identity ${
        isSubdirRow ? 'code-archdoc-coverage-identity--subdir' : ''
      }`}
    >
      <div className="code-archdoc-coverage-repo-row">
        {canSelectSubdir && onToggleSelect && (
          <input
            type="checkbox"
            className="code-archdoc-coverage-checkbox"
            checked={isSelected}
            onChange={onToggleSelect}
            aria-label={`选择 ${row.repo_name} 子目录 ${row.subdirectory}`}
          />
        )}

        {isSubdirRow ? (
          <>
            <CornerDownRight size={14} className="code-archdoc-coverage-tree-icon" />
            <span className="code-badge code-badge--purple">{row.subdirectory}</span>
            <span className="code-archdoc-coverage-parent-repo">{row.repo_name}</span>
          </>
        ) : (
          <>
            <span className="code-archdoc-coverage-repo-name">{row.repo_name}</span>
            <span className="code-badge code-badge--muted">整仓</span>
            {!row.modules_scanned && activeModuleCount === 0 ? (
              row.can_write && onDiscoverModules ? (
                <button
                  type="button"
                  className="code-archdoc-coverage-discover-btn"
                  disabled={isDiscovering}
                  onClick={onDiscoverModules}
                  title="执行零 LLM 静态扫描，自动发现仓库一级业务目录"
                >
                  {isDiscovering ? (
                    <Loader2 size={12} className="code-archdoc-spin" />
                  ) : (
                    <FolderTree size={12} />
                  )}
                  <span>{isDiscovering ? '扫描中...' : '发现一级目录'}</span>
                </button>
              ) : (
                onOpenModuleGovernance && (
                  <button
                    type="button"
                    className="code-archdoc-coverage-module-pill"
                    onClick={onOpenModuleGovernance}
                    title="查看仓库模块发现索引"
                  >
                    <FolderTree size={12} />
                    <span>查看模块</span>
                  </button>
                )
              )
            ) : (
              onOpenModuleGovernance && (
                <button
                  type="button"
                  className="code-archdoc-coverage-module-pill"
                  onClick={onOpenModuleGovernance}
                  title="点击打开模块发现与子目录治理弹窗"
                >
                  <FolderTree size={12} />
                  <span>
                    {activeModuleCount > 0 ? `${activeModuleCount} 个子模块` : '单仓 · 模块治理'}
                  </span>
                </button>
              )
            )}
          </>
        )}

        {row.scope_status === 'missing' && (
          <span
            className="code-badge code-badge--warning"
            title="该子目录在最近提交中已缺失，可在模块治理中迁移文档"
          >
            目录已缺失
          </span>
        )}

        {row.scope_status === 'orphan' && (
          <span
            className="code-badge code-badge--warning"
            title="历史遗留子目录文档范围（未在当前模块索引中），建议在模块治理中迁移"
          >
            孤立范围
          </span>
        )}

        {isSubdirRow && sourceLabel && (
          <span className="code-badge code-badge--muted">{sourceLabel}</span>
        )}

        {row.active_task_id && onOpenActiveTask && (
          <button
            type="button"
            className="code-archdoc-coverage-active-task-btn"
            onClick={onOpenActiveTask}
            title="点击查看正在执行的反推生成任务进度"
          >
            <Activity size={12} />
            <span>任务 #{row.active_task_id}</span>
          </button>
        )}
      </div>

      <div className="code-archdoc-coverage-meta-row">
        {row.department_name && <span>{row.department_name}</span>}
        <span className="code-archdoc-mono">{row.branch || 'master'}</span>
        {typeof row.effective_files === 'number' && row.effective_files > 0 && (
          <span>
            · {row.effective_files} 文件
            {typeof row.total_loc === 'number' && row.total_loc > 0
              ? ` / ${row.total_loc.toLocaleString()} LOC`
              : ''}
          </span>
        )}
      </div>
    </div>
  );
};

export default ScopeIdentityCell;
