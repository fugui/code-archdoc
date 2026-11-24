import React, { useMemo } from 'react';
import {
  Activity,
  BookOpen,
  ChevronDown,
  ChevronRight,
  Cpu,
  FileCode,
  Folder,
  FolderOpen,
  Loader2,
} from 'lucide-react';
import { EmptyState } from '@code/common';
import type {
  ArchTreeNodeVO,
  ArchTreeResponse,
  DocTypeSummaryVO,
  RepoScopeCoverageVO,
} from '../../types';
import { flattenArchTreeRows } from '../../utils/archTreeUtils';
import { formatArchElementTypeLabel, summarizeScopeDocs } from '../../utils/docTypeUtils';
import { formatArchNodeRefDocBadgeText } from '../../utils/refDocUtils';
import { shortSha } from '../../utils/urlQuery';
import CoverageActionButtons from './CoverageActionButtons';
import ScopeDocCoverageCell from './ScopeDocCoverageCell';
import ScopeDocExpandedGrid from './ScopeDocExpandedGrid';

export interface ArchTreeViewProps {
  data: ArchTreeResponse | null;
  loading: boolean;
  docTypes: DocTypeSummaryVO[];
  focusedDocTypeSlug?: string;
  expandedNodeIds: Set<number>;
  expandedDocRowKeys: Set<string>;
  exportingKey?: string | null;
  hasActiveFilters: boolean;
  onResetFilters: () => void;
  onToggleNode: (nodeId: number) => void;
  onToggleDocRowExpand: (rowKey: string) => void;
  onOpenActiveTask: (row: RepoScopeCoverageVO) => void;
  onOpenStudio: (repoId: number, docId?: number, subdirectory?: string) => void;
  onTriggerGenerate: (row: RepoScopeCoverageVO, docTypeSlug?: string, archElementId?: number) => void;
  onExportZip: (row: RepoScopeCoverageVO, rowKey: string) => void;
  onOpenRefDocs?: (node: ArchTreeNodeVO) => void;
}

function ArchNodeIcon({ type, expanded }: { type: string; expanded: boolean }) {
  switch (type) {
    case 'subsystem':
      return <Cpu size={15} className="code-archdoc-tree-node-icon--subsystem" />;
    case 'group':
      return expanded ? (
        <FolderOpen size={15} className="code-archdoc-tree-node-icon--group" />
      ) : (
        <Folder size={15} className="code-archdoc-tree-node-icon--group" />
      );
    case 'module':
      return <FileCode size={15} className="code-archdoc-tree-node-icon--module" />;
    default:
      return <Folder size={15} className="code-archdoc-tree-node-icon--group" />;
  }
}

function archTypeBadgeClass(type: string): string {
  switch (type) {
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

export const ArchTreeView: React.FC<ArchTreeViewProps> = ({
  data,
  loading,
  docTypes,
  focusedDocTypeSlug,
  expandedNodeIds,
  expandedDocRowKeys,
  exportingKey = null,
  hasActiveFilters,
  onResetFilters,
  onToggleNode,
  onToggleDocRowExpand,
  onOpenActiveTask,
  onOpenStudio,
  onTriggerGenerate,
  onExportZip,
  onOpenRefDocs,
}) => {
  const nodes = useMemo(() => data?.nodes ?? [], [data?.nodes]);
  const flattenedRows = useMemo(
    () => flattenArchTreeRows(nodes, expandedNodeIds),
    [nodes, expandedNodeIds],
  );

  if (loading && !data) {
    return (
      <div className="code-card code-archdoc-loading">
        <Loader2 size={18} className="code-archdoc-spin" />
        <span>正在聚合平台架构树与功能模块文档覆盖状态...</span>
      </div>
    );
  }

  if (nodes.length === 0) {
    return (
      <div className="code-card">
        <EmptyState
          type={hasActiveFilters ? 'search' : 'data'}
          title={hasActiveFilters ? '未找到匹配的架构层级或功能模块' : '暂无平台架构元素'}
          description={
            hasActiveFilters
              ? '请调整搜索词或点击“重置筛选”查看完整架构树'
              : '请先在平台「架构管理」中维护子系统、功能组与功能模块'
          }
          action={
            hasActiveFilters ? (
              <button
                type="button"
                className="code-archdoc-ghost-btn"
                onClick={onResetFilters}
              >
                重置筛选
              </button>
            ) : undefined
          }
        />
      </div>
    );
  }

  return (
    <div className="code-card code-archdoc-coverage-table-card">
      <div className="code-archdoc-coverage-table-wrap">
        <table className="code-table code-archdoc-tree-table">
          <thead>
            <tr>
              <th>架构层级 / 关联源码</th>
              <th>负责人与基线</th>
              <th>文档覆盖（{docTypes.length} 类）</th>
              <th className="code-archdoc-coverage-actions-col">操作</th>
            </tr>
          </thead>
          <tbody>
            {flattenedRows.map(({ node, depth, hasChildren, isExpanded, rowKey }, idx) => {
              const displayTitle = node.name_cn || node.name_en || node.identifier;
              const indentStyle = { '--tree-depth': depth } as React.CSSProperties;

              if (node.type !== 'module') {
                const rowToneClass =
                  node.type === 'subsystem'
                    ? 'code-archdoc-tree-row--subsystem'
                    : 'code-archdoc-tree-row--group';
                const totalMods = node.stats.module_count;
                const coveredMods = node.stats.covered_module_count;
                const pct = totalMods > 0 ? Math.round((coveredMods / totalMods) * 100) : 0;

                return (
                  <tr key={rowKey} className={`code-archdoc-tree-row ${rowToneClass}`}>
                    <td>
                      <div
                        className="code-archdoc-tree-indent"
                        data-has-depth={depth > 0 ? 'true' : undefined}
                        style={indentStyle}
                      >
                        {hasChildren ? (
                          <button
                            type="button"
                            className="code-archdoc-tree-toggle"
                            onClick={() => onToggleNode(node.id)}
                            aria-label={
                              isExpanded ? `收起 ${displayTitle}` : `展开 ${displayTitle}`
                            }
                            aria-expanded={isExpanded}
                          >
                            {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
                          </button>
                        ) : (
                          <span className="code-archdoc-tree-toggle-placeholder" />
                        )}

                        <ArchNodeIcon type={node.type} expanded={isExpanded} />
                        <span className={`code-badge ${archTypeBadgeClass(node.type)}`}>
                          {formatArchElementTypeLabel(node.type)}
                        </span>
                        <span className="code-archdoc-tree-node-title">{displayTitle}</span>
                        {node.identifier && (
                          <code className="code-archdoc-mono">{node.identifier}</code>
                        )}
                        {onOpenRefDocs && (
                          <button
                            type="button"
                            className="code-archdoc-refdoc-badge-btn"
                            onClick={() => onOpenRefDocs(node)}
                            title="管理或查看本架构节点绑定的参考文档与继承概览"
                          >
                            <BookOpen size={12} />
                            <span>{formatArchNodeRefDocBadgeText(node) || '参考文档'}</span>
                          </button>
                        )}
                      </div>
                    </td>

                    <td>
                      <span className="code-archdoc-owner-name">{node.owner_name || '-'}</span>
                    </td>

                    <td>
                      {totalMods === 0 ? (
                        <span className="text-muted">0 模块</span>
                      ) : (
                        <div
                          className="code-archdoc-tree-agg-cell"
                          title={`已绑定活跃代码仓 ${node.stats.bound_module_count} / ${totalMods} 个模块`}
                        >
                          <span className="code-archdoc-cov-ratio">
                            模块覆盖 <strong>{coveredMods}</strong>
                            <span className="text-muted"> / {totalMods}</span>
                          </span>

                          <div className="code-archdoc-cov-bar-track" aria-hidden="true">
                            <div
                              className={`code-archdoc-cov-bar-fill ${
                                coveredMods === totalMods ? 'code-archdoc-cov-bar-fill--full' : ''
                              }`}
                              style={{ width: `${pct}%` }}
                            />
                          </div>

                          {node.stats.approved_doc_count > 0 && (
                            <span className="code-badge code-badge--success">
                              已发布 {node.stats.approved_doc_count}
                            </span>
                          )}
                          {node.stats.draft_doc_count > 0 && (
                            <span className="code-badge code-badge--primary">
                              草稿 {node.stats.draft_doc_count}
                            </span>
                          )}
                          {node.stats.drifted_doc_count > 0 && (
                            <span className="code-badge code-badge--warning">
                              漂移 {node.stats.drifted_doc_count}
                            </span>
                          )}
                          {node.stats.blocked_doc_count > 0 && (
                            <span className="code-badge code-badge--danger">
                              阻断 {node.stats.blocked_doc_count}
                            </span>
                          )}
                        </div>
                      )}
                    </td>

                    <td className="code-archdoc-coverage-actions-col">
                      <span className="code-archdoc-tree-dash">—</span>
                    </td>
                  </tr>
                );
              }

              const scope = node.scope ?? null;
              const isBound = node.binding_status === 'bound' && scope !== null;
              const isRepoInactive = node.binding_status === 'repo_inactive';

              if (!isBound) {
                return (
                  <tr
                    key={rowKey}
                    className="code-archdoc-tree-row code-archdoc-tree-row--module code-archdoc-tree-row--gap"
                  >
                    <td>
                      <div
                        className="code-archdoc-tree-indent"
                        data-has-depth={depth > 0 ? 'true' : undefined}
                        style={indentStyle}
                      >
                        <span className="code-archdoc-tree-toggle-placeholder" />
                        <div className="code-archdoc-tree-module-body">
                          <div className="code-archdoc-tree-module-main">
                            <ArchNodeIcon type="module" expanded={false} />
                            <span className="code-badge code-badge--success">功能模块</span>
                            <span className="code-archdoc-tree-node-title">{displayTitle}</span>
                            {node.identifier && (
                              <code className="code-archdoc-mono">{node.identifier}</code>
                            )}
                            {onOpenRefDocs && (
                              <button
                                type="button"
                                className="code-archdoc-refdoc-badge-btn"
                                onClick={() => onOpenRefDocs(node)}
                                title="管理或查看本功能模块绑定的参考文档"
                              >
                                <BookOpen size={12} />
                                <span>{formatArchNodeRefDocBadgeText(node) || '参考文档'}</span>
                              </button>
                            )}
                          </div>
                          <div
                            className={`code-archdoc-tree-status-note ${
                              isRepoInactive ? 'code-archdoc-tree-status-note--warning' : ''
                            }`}
                          >
                            {isRepoInactive ? '所绑仓库已停用' : '未关联代码仓'}
                          </div>
                        </div>
                      </div>
                    </td>

                    <td>
                      <span className="code-archdoc-owner-name">{node.owner_name || '-'}</span>
                    </td>

                    <td>
                      <span className="code-archdoc-tree-dash">—</span>
                    </td>

                    <td className="code-archdoc-coverage-actions-col">
                      <span
                        className={`code-archdoc-tree-action-muted ${
                          isRepoInactive ? 'code-archdoc-tree-action-muted--warning' : ''
                        }`}
                      >
                        {isRepoInactive ? '仓库已停用' : '未绑定源码'}
                      </span>
                    </td>
                  </tr>
                );
              }

              const docSummary = summarizeScopeDocs(scope, docTypes);
              const firstDoc = Object.values(scope.doc_statuses ?? {})[0];
              const expanded = expandedDocRowKeys.has(rowKey);

              return (
                <React.Fragment key={rowKey}>
                  <tr
                    className={`code-archdoc-tree-row code-archdoc-tree-row--module ${
                      expanded ? 'code-archdoc-coverage-row--expanded' : ''
                    }`}
                  >
                    <td>
                      <div
                        className="code-archdoc-tree-indent"
                        data-has-depth={depth > 0 ? 'true' : undefined}
                        style={indentStyle}
                      >
                        <span className="code-archdoc-tree-toggle-placeholder" />
                        <div className="code-archdoc-tree-module-body">
                          <div className="code-archdoc-tree-module-main">
                            <ArchNodeIcon type="module" expanded={false} />
                            <span className="code-badge code-badge--success">功能模块</span>
                            <span className="code-archdoc-tree-node-title">{displayTitle}</span>
                            {node.identifier && (
                              <code className="code-archdoc-mono">{node.identifier}</code>
                            )}
                            {scope.active_task_id && (
                              <button
                                type="button"
                                className="code-archdoc-coverage-active-task-btn"
                                onClick={() => onOpenActiveTask(scope)}
                                title="点击查看正在执行的反推生成任务进度"
                              >
                                <Activity size={12} />
                                <span>任务 #{scope.active_task_id}</span>
                              </button>
                            )}
                            {scope.scope_status === 'missing' && (
                              <span
                                className="code-badge code-badge--warning"
                                title="该子目录在最近提交中已缺失，请前往「未关联代码仓 → 全部代码仓」执行目录迁移"
                              >
                                目录已缺失
                              </span>
                            )}
                            {onOpenRefDocs && (
                              <button
                                type="button"
                                className="code-archdoc-refdoc-badge-btn"
                                onClick={() => onOpenRefDocs(node)}
                                title="管理或查看本功能模块绑定的参考文档与继承概览"
                              >
                                <BookOpen size={12} />
                                <span>{formatArchNodeRefDocBadgeText(node) || '参考文档'}</span>
                              </button>
                            )}
                          </div>

                          <div className="code-archdoc-tree-source-meta">
                            <span>
                              {scope.subdirectory
                                ? `${scope.repo_name} / ${scope.subdirectory}`
                                : scope.repo_name}
                            </span>
                            <span>·</span>
                            <span
                              className={`code-archdoc-mono ${
                                scope.is_branch_overridden
                                  ? 'code-archdoc-branch-pill--overridden'
                                  : ''
                              }`}
                              title={
                                scope.is_branch_overridden
                                  ? `已切换自定义跟踪分支 ${scope.branch}（仓库默认分支：${scope.default_branch || 'master'}）`
                                  : `正在跟踪仓库默认分支 ${scope.branch || 'master'}`
                              }
                            >
                              {scope.branch || 'master'}
                              {scope.is_branch_overridden ? ' · 自定义' : ''}
                            </span>
                            {typeof scope.effective_files === 'number' &&
                              scope.effective_files > 0 && (
                                <span>
                                  · {scope.effective_files} 文件
                                  {typeof scope.total_loc === 'number' && scope.total_loc > 0
                                    ? ` / ${scope.total_loc.toLocaleString()} LOC`
                                    : ''}
                                </span>
                              )}
                          </div>
                        </div>
                      </div>
                    </td>

                    <td>
                      <div className="code-archdoc-owner-name">
                        {node.owner_name || scope.owner_name || '-'}
                      </div>
                      <div className="code-archdoc-coverage-meta-row">
                        <span title={scope.latest_commit_sha || '暂无快照'}>
                          Commit:{' '}
                          <code className="code-archdoc-mono">
                            {shortSha(scope.latest_commit_sha)}
                          </code>
                        </span>
                      </div>
                    </td>

                    <td>
                      <ScopeDocCoverageCell
                        row={scope}
                        docTypes={docTypes}
                        focusedDocTypeSlug={focusedDocTypeSlug}
                        expanded={expanded}
                        onToggleExpand={() => onToggleDocRowExpand(rowKey)}
                        onOpenStudio={(docId) =>
                          onOpenStudio(scope.repo_id, docId, scope.subdirectory)
                        }
                        onTriggerGenerate={(slug) => onTriggerGenerate(scope, slug, node.id)}
                      />
                    </td>

                    <td className="code-archdoc-coverage-actions-col">
                      <CoverageActionButtons
                        row={scope}
                        exporting={exportingKey === rowKey}
                        approvedCount={docSummary.approvedCount}
                        preferGeneratePrimary={scope.can_write && docSummary.generatedCount === 0}
                        hideDisabledExport
                        tooltipPlacement={idx === 0 ? 'bottom' : 'top'}
                        onOpenStudio={() =>
                          onOpenStudio(scope.repo_id, firstDoc?.document_id, scope.subdirectory)
                        }
                        onTriggerGenerate={() => onTriggerGenerate(scope, undefined, node.id)}
                        onExportZip={() => onExportZip(scope, rowKey)}
                      />
                    </td>
                  </tr>

                  {expanded && (
                    <tr className="code-archdoc-coverage-expanded-row">
                      <td colSpan={4} className="code-archdoc-coverage-expanded-cell">
                        <ScopeDocExpandedGrid
                          row={scope}
                          docTypes={docTypes}
                          focusedDocTypeSlug={focusedDocTypeSlug}
                          onOpenStudio={(docId) =>
                            onOpenStudio(scope.repo_id, docId, scope.subdirectory)
                          }
                          onTriggerGenerate={(slug) => onTriggerGenerate(scope, slug, node.id)}
                        />
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
};

export default ArchTreeView;
