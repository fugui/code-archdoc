import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  Cpu,
  FileCode,
  FileQuestion,
  Folder,
  FolderOpen,
  Loader2,
  User,
} from 'lucide-react';
import { EmptyState } from '@code/common';
import type {
  ArchTreeNodeVO,
  ArchTreeResponse,
  DocTypeSummaryVO,
  RepoScopeCoverageVO,
} from '../../types';
import { formatArchElementTypeLabel } from '../../utils/docTypeUtils';
import ScopeCoverageTable from './ScopeCoverageTable';

export interface ArchTreeViewProps {
  data: ArchTreeResponse | null;
  loading: boolean;
  docTypes: DocTypeSummaryVO[];
  focusedDocTypeSlug?: string;
  expandedDocRowKeys: Set<string>;
  discoveringRepoId: number | null;
  exportingKey: string | null;
  hasActiveFilters: boolean;
  onResetFilters: () => void;
  onToggleDocRowExpand: (rowKey: string) => void;
  onDiscoverModules: (row: RepoScopeCoverageVO) => void;
  onOpenModuleGovernance: (row: RepoScopeCoverageVO) => void;
  onOpenActiveTask: (row: RepoScopeCoverageVO) => void;
  onOpenStudio: (repoId: number, docId?: number, subdirectory?: string) => void;
  onTriggerGenerate: (row: RepoScopeCoverageVO, docTypeSlug?: string) => void;
  onBatchGenerate: (row: RepoScopeCoverageVO) => void;
  onExportZip: (row: RepoScopeCoverageVO) => void;
}

function collectExpandableNodeIds(nodes: readonly ArchTreeNodeVO[], depthLimit?: number): number[] {
  const out: number[] = [];
  const walk = (list: readonly ArchTreeNodeVO[], depth: number) => {
    for (const node of list) {
      const hasBody = node.children.length > 0 || node.scopes.length > 0;
      if (hasBody && (depthLimit === undefined || depth < depthLimit)) {
        out.push(node.id);
      }
      if (node.children.length > 0) {
        walk(node.children, depth + 1);
      }
    }
  };
  walk(nodes, 0);
  return out;
}

function ArchNodeIcon({ type, expanded }: { type: string; expanded: boolean }) {
  switch (type) {
    case 'subsystem':
      return <Cpu size={16} className="code-archdoc-tree-node-icon--subsystem" />;
    case 'group':
      return expanded ? (
        <FolderOpen size={16} className="code-archdoc-tree-node-icon--group" />
      ) : (
        <Folder size={16} className="code-archdoc-tree-node-icon--group" />
      );
    case 'module':
      return <FileCode size={16} className="code-archdoc-tree-node-icon--module" />;
    default:
      return <Folder size={16} className="code-archdoc-tree-node-icon--group" />;
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
  expandedDocRowKeys,
  discoveringRepoId,
  exportingKey,
  hasActiveFilters,
  onResetFilters,
  onToggleDocRowExpand,
  onDiscoverModules,
  onOpenModuleGovernance,
  onOpenActiveTask,
  onOpenStudio,
  onTriggerGenerate,
  onBatchGenerate,
  onExportZip,
}) => {
  const [expandedNodeIds, setExpandedNodeIds] = useState<Set<number>>(new Set());

  const nodes = useMemo(() => data?.nodes ?? [], [data?.nodes]);
  const unassigned = useMemo(() => data?.unassigned_scopes ?? [], [data?.unassigned_scopes]);

  // 初始加载或筛选开启时默认展开带覆盖内容的层级，便于一眼看全
  useEffect(() => {
    if (nodes.length === 0) return;
    if (hasActiveFilters) {
      setExpandedNodeIds(new Set(collectExpandableNodeIds(nodes)));
    } else {
      setExpandedNodeIds((prev) => {
        if (prev.size > 0) return prev;
        return new Set(collectExpandableNodeIds(nodes, 2));
      });
    }
  }, [nodes, hasActiveFilters]);

  const toggleNode = useCallback((id: number) => {
    setExpandedNodeIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }, []);

  const expandAll = () => setExpandedNodeIds(new Set(collectExpandableNodeIds(nodes)));
  const collapseAll = () => setExpandedNodeIds(new Set());

  if (loading && !data) {
    return (
      <div className="code-card code-archdoc-loading">
        <Loader2 size={18} className="code-archdoc-spin" />
        <span>正在聚合平台架构树与文档覆盖状态...</span>
      </div>
    );
  }

  if (nodes.length === 0 && unassigned.length === 0) {
    return (
      <div className="code-card">
        <EmptyState
          type={hasActiveFilters ? 'search' : 'data'}
          title={hasActiveFilters ? '未找到匹配的架构元素或关联范围' : '暂无架构元素与仓库范围'}
          description={
            hasActiveFilters
              ? '请调整搜索词或点击“重置筛选”查看完整架构树'
              : '请先在平台「架构管理」与「代码仓管理」中维护架构元素和代码仓库'
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

  const renderTreeNode = (node: ArchTreeNodeVO, depth: number): React.ReactNode => {
    const hasBody = node.children.length > 0 || node.scopes.length > 0;
    const isExpanded = expandedNodeIds.has(node.id);
    const displayTitle = node.name_cn || node.name_en || node.identifier;

    return (
      <div
        key={node.id}
        className={`code-archdoc-tree-node ${depth === 0 ? 'code-archdoc-tree-node--root' : ''}`}
      >
        <div
          className={`code-archdoc-tree-node-head ${
            hasBody ? 'code-archdoc-tree-node-head--interactive' : ''
          }`}
          onClick={hasBody ? () => toggleNode(node.id) : undefined}
        >
          <div className="code-archdoc-tree-node-left">
            {hasBody ? (
              <button
                type="button"
                className="code-archdoc-tree-toggle"
                onClick={(e) => {
                  e.stopPropagation();
                  toggleNode(node.id);
                }}
                aria-label={isExpanded ? `收起 ${displayTitle}` : `展开 ${displayTitle}`}
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

            {node.owner_name && (
              <span className="code-archdoc-tree-node-owner">
                <User size={12} />
                <span>{node.owner_name}</span>
              </span>
            )}
          </div>

          <div className="code-archdoc-tree-node-stats">
            <span className="code-badge code-badge--muted">
              范围 {node.stats.covered_scope_count}/{node.stats.scope_count}
            </span>
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
        </div>

        {hasBody && isExpanded && (
          <div className="code-archdoc-tree-node-body">
            {node.scopes.length > 0 && (
              <div className="code-archdoc-tree-scopes-wrap">
                <ScopeCoverageTable
                  rows={node.scopes}
                  docTypes={docTypes}
                  focusedDocTypeSlug={focusedDocTypeSlug}
                  expandedRowKeys={expandedDocRowKeys}
                  showArchColumn={false}
                  showSelection={false}
                  discoveringRepoId={discoveringRepoId}
                  exportingKey={exportingKey}
                  compact
                  onToggleExpandRow={onToggleDocRowExpand}
                  onDiscoverModules={onDiscoverModules}
                  onOpenModuleGovernance={onOpenModuleGovernance}
                  onOpenActiveTask={onOpenActiveTask}
                  onOpenStudio={onOpenStudio}
                  onTriggerGenerate={onTriggerGenerate}
                  onBatchGenerate={onBatchGenerate}
                  onExportZip={onExportZip}
                />
              </div>
            )}

            {node.children.length > 0 && (
              <div className="code-archdoc-tree-children">
                {node.children.map((child) => renderTreeNode(child, depth + 1))}
              </div>
            )}
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="code-archdoc-tree-wrapper">
      {nodes.length > 0 && (
        <div className="code-card code-archdoc-tree-container">
          <div className="code-archdoc-tree-toolbar">
            <div className="code-archdoc-tree-legend">
              <span className="code-archdoc-tree-legend-item">
                <Cpu size={14} className="code-archdoc-tree-node-icon--subsystem" />
                <span>子系统</span>
              </span>
              <span className="code-archdoc-tree-legend-item">
                <Folder size={14} className="code-archdoc-tree-node-icon--group" />
                <span>功能组</span>
              </span>
              <span className="code-archdoc-tree-legend-item">
                <FileCode size={14} className="code-archdoc-tree-node-icon--module" />
                <span>功能模块</span>
              </span>
            </div>

            <div className="code-archdoc-tree-toolbar-actions">
              <button type="button" className="code-archdoc-ghost-btn" onClick={expandAll}>
                全部展开
              </button>
              <button type="button" className="code-archdoc-ghost-btn" onClick={collapseAll}>
                全部收起
              </button>
            </div>
          </div>

          <div className="code-archdoc-tree-list">
            {nodes.map((root) => renderTreeNode(root, 0))}
          </div>
        </div>
      )}

      {unassigned.length > 0 && (
        <div className="code-card code-archdoc-unassigned-card">
          <div className="code-archdoc-unassigned-head">
            <div className="code-archdoc-unassigned-title">
              <FileQuestion size={16} />
              <span>未归类代码仓与目录 ({unassigned.length})</span>
            </div>
            <span className="text-secondary">
              以下范围尚未在平台「架构管理」关联架构元素或归属子系统
            </span>
          </div>

          <ScopeCoverageTable
            rows={unassigned}
            docTypes={docTypes}
            focusedDocTypeSlug={focusedDocTypeSlug}
            expandedRowKeys={expandedDocRowKeys}
            showArchColumn
            showSelection={false}
            discoveringRepoId={discoveringRepoId}
            exportingKey={exportingKey}
            onToggleExpandRow={onToggleDocRowExpand}
            onDiscoverModules={onDiscoverModules}
            onOpenModuleGovernance={onOpenModuleGovernance}
            onOpenActiveTask={onOpenActiveTask}
            onOpenStudio={onOpenStudio}
            onTriggerGenerate={onTriggerGenerate}
            onBatchGenerate={onBatchGenerate}
            onExportZip={onExportZip}
          />
        </div>
      )}
    </div>
  );
};

export default ArchTreeView;
