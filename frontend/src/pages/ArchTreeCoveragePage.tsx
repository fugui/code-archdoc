import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { RefreshCw, RotateCcw } from 'lucide-react';
import { useDebounce, useToast } from '@code/common';
import { exportDocumentsZip } from '../api/documents';
import { getArchTree, listDocTypes } from '../api/repos';
import GenerateTaskModal, { type GenerateTaskScope } from '../components/GenerateTaskModal';
import OverviewMicroPills, { buildArchTreeMicroPills } from '../components/OverviewMicroPills';
import ArchTreeView from '../components/coverage/ArchTreeView';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  ArchTreeResponse,
  ArchTreeSummaryVO,
  DocTypeSummaryVO,
  RepoScopeCoverageVO,
  RepoStatusFilter,
} from '../types';
import {
  collectArchTreeExpandableIds,
  computeDefaultExpandedArchTreeIds,
} from '../utils/archTreeUtils';
import { groupDocTypesByCategory } from '../utils/docTypeUtils';
import { formatApiError } from '../utils/errorMessages';
import {
  buildModulePath,
  buildStudioPath,
  parseArchTreeFilterParams,
  serializeArchTreeFilterParams,
} from '../utils/urlQuery';
import './RepoCoveragePage.css';

interface ModalLaunchState {
  open: boolean;
  scope: GenerateTaskScope | null;
  initialDocTypeSlugs?: string[];
  initialTaskId?: number | null;
}

export const ArchTreeCoveragePage: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  const [searchParams, setSearchParams] = useSearchParams();
  const filterState = useMemo(() => parseArchTreeFilterParams(searchParams), [searchParams]);

  const [searchInput, setSearchInput] = useState<string>(filterState.search);
  const debouncedSearch = useDebounce(searchInput, 300);

  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [archTreeData, setArchTreeData] = useState<ArchTreeResponse | null>(null);
  const [summary, setSummary] = useState<ArchTreeSummaryVO>({
    total_modules: 0,
    bound_modules: 0,
    covered_modules: 0,
    approved_docs: 0,
    draft_docs: 0,
    drifted_docs: 0,
    unassigned_repo_count: 0,
  });
  const [loading, setLoading] = useState<boolean>(true);
  const [exportingKey, setExportingKey] = useState<string | null>(null);
  const [expandedNodeIds, setExpandedNodeIds] = useState<Set<number>>(new Set());
  const [expandedRowKeys, setExpandedRowKeys] = useState<Set<string>>(new Set());

  const [taskModal, setTaskModal] = useState<ModalLaunchState>({
    open: false,
    scope: null,
  });

  useEffect(() => {
    setSearchInput(filterState.search);
  }, [filterState.search]);

  useEffect(() => {
    if (debouncedSearch.trim() === filterState.search) return;
    setSearchParams(
      (prev) => serializeArchTreeFilterParams({ search: debouncedSearch.trim() }, prev),
      { replace: true },
    );
  }, [debouncedSearch, filterState.search, setSearchParams]);

  useEffect(() => {
    let active = true;
    void listDocTypes(true)
      .then((dtList) => {
        if (!active) return;
        setDocTypes(dtList);
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载文档类型失败').message, 'error');
      });
    return () => {
      active = false;
    };
  }, [showToast]);

  const docTypeGroups = useMemo(() => groupDocTypesByCategory(docTypes), [docTypes]);

  const hasActiveFilters = Boolean(
    filterState.search ||
      filterState.subsystemId ||
      filterState.docType ||
      filterState.status,
  );

  const fetchArchTree = useCallback(async () => {
    setLoading(true);
    try {
      const res = await getArchTree({
        search: filterState.search,
        subsystemId: filterState.subsystemId,
        docType: filterState.docType,
        status: filterState.status,
      });
      setArchTreeData(res);
      if (res.summary) {
        setSummary(res.summary);
      }
      const defaultExpanded = computeDefaultExpandedArchTreeIds(
        res.nodes ?? [],
        res.summary?.total_modules ?? 0,
        hasActiveFilters,
      );
      setExpandedNodeIds(new Set(defaultExpanded));
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载架构文档全景失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    filterState.search,
    filterState.subsystemId,
    filterState.docType,
    filterState.status,
    hasActiveFilters,
    showToast,
  ]);

  useEffect(() => {
    void fetchArchTree();
  }, [fetchArchTree]);

  const updateFilter = (patch: {
    subsystemId?: string;
    docType?: string;
    status?: RepoStatusFilter;
  }) => {
    setSearchParams((prev) => serializeArchTreeFilterParams(patch, prev));
  };

  const resetFilters = () => {
    setSearchInput('');
    setSearchParams(new URLSearchParams());
  };

  const toggleTreeNode = useCallback((nodeId: number) => {
    setExpandedNodeIds((prev) => {
      const next = new Set(prev);
      if (next.has(nodeId)) {
        next.delete(nodeId);
      } else {
        next.add(nodeId);
      }
      return next;
    });
  }, []);

  const expandAllNodes = useCallback(() => {
    const nodes = archTreeData?.nodes ?? [];
    setExpandedNodeIds(new Set(collectArchTreeExpandableIds(nodes)));
  }, [archTreeData?.nodes]);

  const collapseAllNodes = useCallback(() => {
    setExpandedNodeIds(new Set());
  }, []);

  const toggleExpandRow = useCallback((rowKey: string) => {
    setExpandedRowKeys((prev) => {
      const next = new Set(prev);
      if (next.has(rowKey)) {
        next.delete(rowKey);
      } else {
        next.add(rowKey);
      }
      return next;
    });
  }, []);

  const openStudio = useCallback(
    (repoId: number, docId?: number, subdirectory?: string) => {
      navigate(buildStudioPath(isEmbedded, repoId, { docId, subdirectory }), {
        state: {
          from: `${location.pathname}${location.search}`,
          fromLabel: '架构文档全景',
        },
      });
    },
    [isEmbedded, location.pathname, location.search, navigate],
  );

  const openGenerateModal = useCallback(
    (
      row: RepoScopeCoverageVO,
      options?: {
        initialDocTypeSlugs?: string[];
        initialTaskId?: number | null;
      },
    ) => {
      setTaskModal({
        open: true,
        scope: {
          repoId: row.repo_id,
          repoName: row.repo_name,
          subdirectory: row.subdirectory,
          branch: row.branch || 'master',
          defaultBranch: row.default_branch,
          isBranchOverridden: row.is_branch_overridden,
          canSwitchBranch: row.can_switch_branch ?? row.can_write,
          canWrite: row.can_write,
        },
        initialDocTypeSlugs: options?.initialDocTypeSlugs,
        initialTaskId: options?.initialTaskId ?? null,
      });
    },
    [],
  );

  const handleExportRowZip = useCallback(
    async (row: RepoScopeCoverageVO, rowKey: string) => {
      const approvedDocIds = Object.values(row.doc_statuses ?? {})
        .filter((ds) => ds.status === 'approved')
        .map((ds) => ds.document_id);
      if (approvedDocIds.length === 0) {
        showToast('当前范围尚无已发布文档可供打包导出', 'warning');
        return;
      }

      setExportingKey(rowKey);
      try {
        const filename = await exportDocumentsZip(
          { document_ids: approvedDocIds, include_index: true },
          `${row.repo_name}-archdocs.zip`,
        );
        showToast(`已导出压缩包：${filename}`, 'success');
      } catch (err: unknown) {
        showToast(formatApiError(err, 'ZIP 打包导出失败').message, 'error');
      } finally {
        setExportingKey(null);
      }
    },
    [showToast],
  );

  const subsystems = archTreeData?.subsystems ?? [];
  const hasTreeNodes = (archTreeData?.nodes?.length ?? 0) > 0;

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">架构文档全景</h1>
          <p className="code-archdoc-page-subtitle">
            按平台「子系统 → 功能组 → 功能模块」单树表聚合浏览功能模块文档覆盖度，一键直达工作台审校与反推生成。
          </p>
        </div>
        <div className="code-archdoc-page-actions">
          <OverviewMicroPills
            ariaLabel="架构覆盖快捷摘要"
            items={buildArchTreeMicroPills(summary, filterState.status, (nextStatus) =>
              updateFilter({ status: nextStatus }),
            )}
          />
          <button
            type="button"
            className="code-archdoc-ghost-btn"
            onClick={() => void fetchArchTree()}
            disabled={loading}
          >
            <RefreshCw size={14} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新全景</span>
          </button>
        </div>
      </div>

      {/* 单行筛选栏 + 树节点全部展开/收起控制 */}
      <div className="code-card code-archdoc-coverage-toolbar">
        <div className="code-archdoc-coverage-filters">
          <input
            type="text"
            className="code-input code-archdoc-coverage-search"
            placeholder="搜索架构节点、代码仓或子目录..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            aria-label="架构全景关键词搜索"
          />

          <select
            className="code-select code-archdoc-coverage-select"
            value={filterState.subsystemId}
            onChange={(e) => updateFilter({ subsystemId: e.target.value })}
            aria-label="按架构子系统筛选"
          >
            <option value="">全部子系统</option>
            {subsystems.map((sub) => (
              <option key={sub.id} value={String(sub.id)}>
                {sub.name_cn || sub.name_en || sub.identifier} ({sub.module_count ?? sub.scope_count})
              </option>
            ))}
          </select>

          <select
            className="code-select code-archdoc-coverage-select"
            value={filterState.docType}
            onChange={(e) => updateFilter({ docType: e.target.value })}
            aria-label="按文档类型筛选"
          >
            <option value="">全部文档类型</option>
            {docTypeGroups.map((group) => (
              <optgroup key={group.category} label={group.category}>
                {group.items.map((dt) => (
                  <option key={dt.slug} value={dt.slug}>
                    {dt.display_name}
                  </option>
                ))}
              </optgroup>
            ))}
          </select>

          <select
            className="code-select code-archdoc-coverage-select"
            value={filterState.status}
            onChange={(e) => updateFilter({ status: e.target.value as RepoStatusFilter })}
            aria-label="按文档状态筛选"
          >
            <option value="">全部状态</option>
            <option value="approved">已发布</option>
            <option value="draft">草稿中</option>
            <option value="drifted">存在漂移</option>
            <option value="missing">未生成</option>
          </select>

          {hasActiveFilters && (
            <button type="button" className="code-archdoc-ghost-btn" onClick={resetFilters}>
              <RotateCcw size={13} />
              <span>重置筛选</span>
            </button>
          )}

          <div className="code-archdoc-tree-filter-actions">
            <button
              type="button"
              className="code-archdoc-ghost-btn"
              onClick={expandAllNodes}
              disabled={!hasTreeNodes}
            >
              全部展开
            </button>
            <button
              type="button"
              className="code-archdoc-ghost-btn"
              onClick={collapseAllNodes}
              disabled={!hasTreeNodes}
            >
              全部收起
            </button>
          </div>
        </div>
      </div>

      {/* 单一 4 列树形表格 */}
      <ArchTreeView
        data={archTreeData}
        loading={loading}
        docTypes={docTypes}
        focusedDocTypeSlug={filterState.docType}
        expandedNodeIds={expandedNodeIds}
        expandedDocRowKeys={expandedRowKeys}
        exportingKey={exportingKey}
        hasActiveFilters={hasActiveFilters}
        onResetFilters={resetFilters}
        onToggleNode={toggleTreeNode}
        onToggleDocRowExpand={toggleExpandRow}
        onOpenActiveTask={(r) => openGenerateModal(r, { initialTaskId: r.active_task_id })}
        onOpenStudio={openStudio}
        onTriggerGenerate={(r, slug) =>
          openGenerateModal(r, slug ? { initialDocTypeSlugs: [slug] } : undefined)
        }
        onExportZip={(r, rowKey) => void handleExportRowZip(r, rowKey)}
      />

      <GenerateTaskModal
        open={taskModal.open}
        scope={taskModal.scope}
        docTypes={docTypes}
        initialDocTypeSlugs={taskModal.initialDocTypeSlugs}
        initialTaskId={taskModal.initialTaskId}
        onClose={() => setTaskModal((prev) => ({ ...prev, open: false }))}
        onTaskCompleted={() => void fetchArchTree()}
        onBranchChanged={(res) => {
          setTaskModal((prev) =>
            prev.scope
              ? {
                  ...prev,
                  scope: {
                    ...prev.scope,
                    branch: res.current_branch,
                    defaultBranch: res.default_branch,
                    isBranchOverridden: res.is_overridden,
                  },
                }
              : prev,
          );
          void fetchArchTree();
        }}
        onOpenStudio={openStudio}
        onOpenTaskCenter={() => navigate(buildModulePath(isEmbedded, '/tasks'))}
      />
    </div>
  );
};

export default ArchTreeCoveragePage;
