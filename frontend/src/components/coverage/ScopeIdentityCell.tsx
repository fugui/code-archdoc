import React from 'react';
import { Activity, ChevronDown, ChevronRight, CornerDownRight, FolderTree, Loader2 } from 'lucide-react';
import type { RepoScopeCoverageVO } from '../../types';
import { formatModuleSourceLabel } from '../../utils/moduleUtils';

export interface ScopeIdentityCellProps {
  row: RepoScopeCoverageVO;
  showSelection?: boolean;
  isSelected?: boolean;
  isDiscovering?: boolean;
  subdirCount?: number;
  subdirsExpanded?: boolean;
  onToggleSubdirs?: () => void;
  onToggleSelect?: () => void;
  onDiscoverModules?: () => void;
  onOpenActiveTask?: () => void;
}

export const ScopeIdentityCell: React.FC<ScopeIdentityCellProps> = ({
  row,
  showSelection = false,
  isSelected = false,
  isDiscovering = false,
  subdirCount,
  subdirsExpanded = false,
  onToggleSubdirs,
  onToggleSelect,
  onDiscoverModules,
  onOpenActiveTask,
}) => {
  const isSubdirRow = row.scope_type === 'subdir';
  const canSelectSubdir =
    showSelection && isSubdirRow && row.can_write && row.scope_status !== 'missing';
  const activeModuleCount = row.active_module_count ?? 0;
  const sourceLabel = formatModuleSourceLabel(
    row.scope_status === 'orphan' ? 'orphan' : row.module_source,
  );
  const effectiveSubdirCount = typeof subdirCount === 'number' ? subdirCount : activeModuleCount;

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

            {typeof subdirCount === 'number' && subdirCount > 0 && onToggleSubdirs ? (
              <button
                type="button"
                className={`code-archdoc-coverage-module-pill ${
                  subdirsExpanded ? 'code-archdoc-coverage-module-pill--expanded' : ''
                }`}
                onClick={onToggleSubdirs}
                aria-expanded={subdirsExpanded}
                title={
                  subdirsExpanded
                    ? '点击收起该仓库下的一级子目录行'
                    : `点击展开该仓库下的 ${subdirCount} 个子目录覆盖行`
                }
              >
                {subdirsExpanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <span>{subdirCount} 个子模块</span>
              </button>
            ) : !row.modules_scanned && activeModuleCount === 0 ? (
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
              ) : null
            ) : (
              effectiveSubdirCount > 0 && (
                <span className="code-badge code-badge--purple">
                  {effectiveSubdirCount} 个子模块
                </span>
              )
            )}
          </>
        )}

        {row.scope_status === 'missing' && (
          <span
            className="code-badge code-badge--warning"
            title="该子目录在最近提交中已缺失，可在仓库更多操作「模块发现与目录迁移」中处理"
          >
            目录已缺失
          </span>
        )}

        {row.scope_status === 'orphan' && (
          <span
            className="code-badge code-badge--warning"
            title="历史遗留子目录文档范围（未在当前模块索引中），建议在模块发现与目录迁移中处理"
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
        <span
          className={`code-archdoc-mono ${
            row.is_branch_overridden ? 'code-archdoc-branch-pill--overridden' : ''
          }`}
          title={
            row.is_branch_overridden
              ? `已切换自定义跟踪分支 ${row.branch}（仓库默认分支：${row.default_branch || 'master'}）`
              : `正在跟踪仓库默认分支 ${row.branch || 'master'}`
          }
        >
          {row.branch || 'master'}
          {row.is_branch_overridden ? ' · 自定义' : ''}
        </span>
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
