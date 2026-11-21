import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  CheckCircle2,
  FolderGit2,
  Layers,
  RefreshCw,
  RotateCcw,
} from 'lucide-react';
import { useDebounce, useToast } from '@code/common';
import { exportDocumentsZip } from '../api/documents';
import { getArchTree, listDocTypes } from '../api/repos';
import GenerateTaskModal, { type GenerateTaskScope } from '../components/GenerateTaskModal';
import ArchTreeView from '../components/coverage/ArchTreeView';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  ArchTreeResponse,
  DocTypeSummaryVO,
  RepoCoverageSummaryVO,
  RepoScopeCoverageVO,
  RepoStatusFilter,
} from '../types';
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
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  const [searchParams, setSearchParams] = useSearchParams();
  const filterState = useMemo(() => parseArchTreeFilterParams(searchParams), [searchParams]);

  const [searchInput, setSearchInput] = useState<string>(filterState.search);
  const debouncedSearch = useDebounce(searchInput, 300);

  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [archTreeData, setArchTreeData] = useState<ArchTreeResponse | null>(null);
  const [summary, setSummary] = useState<RepoCoverageSummaryVO>({
    total_scopes: 0,
    covered_scopes: 0,
    approved_docs: 0,
    drifted_docs: 0,
  });
  const [loading, setLoading] = useState<boolean>(true);
  const [exportingKey, setExportingKey] = useState<string | null>(null);
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
      navigate(buildStudioPath(isEmbedded, repoId, { docId, subdirectory }));
    },
    [isEmbedded, navigate],
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
          canWrite: row.can_write,
        },
        initialDocTypeSlugs: options?.initialDocTypeSlugs,
        initialTaskId: options?.initialTaskId ?? null,
      });
    },
    [],
  );

  const handleExportRowZip = useCallback(
    async (row: RepoScopeCoverageVO) => {
      const approvedDocIds = Object.values(row.doc_statuses ?? {})
        .filter((ds) => ds.status === 'approved')
        .map((ds) => ds.document_id);
      if (approvedDocIds.length === 0) {
        showToast('当前范围尚无已发布文档可供打包导出', 'warning');
        return;
      }

      const rowKey = `${row.repo_id}:${row.subdirectory}`;
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
  const hasActiveFilters = Boolean(
    filterState.search ||
      filterState.subsystemId ||
      filterState.docType ||
      filterState.status,
  );

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">架构文档全景</h1>
          <p className="code-archdoc-page-subtitle">
            按平台「子系统 → 功能组 → 功能模块」层级树聚合浏览架构资产文档覆盖度，一键直达工作台审校与 ZIP 导出。
          </p>
        </div>
        <div className="code-archdoc-page-actions">
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

      {/* 架构维度覆盖 KPI 摘要 */}
      <div className="grid-cols-4 gap-md">
        <div className="code-stat-card">
          <div className="code-stat-icon code-archdoc-stat-icon--primary">
            <FolderGit2 size={20} />
          </div>
          <div>
            <div className="code-stat-label">纳入范围总数</div>
            <div className="code-stat-value">{summary.total_scopes}</div>
          </div>
        </div>
        <div className="code-stat-card">
          <div className="code-stat-icon code-archdoc-stat-icon--purple">
            <Layers size={20} />
          </div>
          <div>
            <div className="code-stat-label">已覆盖范围</div>
            <div className="code-stat-value">{summary.covered_scopes}</div>
          </div>
        </div>
        <div className="code-stat-card">
          <div className="code-stat-icon code-archdoc-stat-icon--success">
            <CheckCircle2 size={20} />
          </div>
          <div>
            <div className="code-stat-label">已发布定稿文档</div>
            <div className="code-stat-value">{summary.approved_docs}</div>
          </div>
        </div>
        <div className="code-stat-card">
          <div className="code-stat-icon code-archdoc-stat-icon--warning">
            <AlertTriangle size={20} />
          </div>
          <div>
            <div className="code-stat-label">存在漂移文档</div>
            <div className="code-stat-value">{summary.drifted_docs}</div>
          </div>
        </div>
      </div>

      {/* 单行 4 项精简筛选栏 */}
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
                {sub.name_cn || sub.name_en || sub.identifier} ({sub.scope_count})
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
        </div>
      </div>

      {/* 架构树视图（聚焦查阅 / 审校 / 生成 / 导出） */}
      <ArchTreeView
        data={archTreeData}
        loading={loading}
        docTypes={docTypes}
        focusedDocTypeSlug={filterState.docType}
        expandedDocRowKeys={expandedRowKeys}
        exportingKey={exportingKey}
        hasActiveFilters={hasActiveFilters}
        onResetFilters={resetFilters}
        onToggleDocRowExpand={toggleExpandRow}
        onOpenActiveTask={(r) => openGenerateModal(r, { initialTaskId: r.active_task_id })}
        onOpenStudio={openStudio}
        onTriggerGenerate={(r, slug) =>
          openGenerateModal(r, slug ? { initialDocTypeSlugs: [slug] } : undefined)
        }
        onExportZip={(r) => void handleExportRowZip(r)}
      />

      <GenerateTaskModal
        open={taskModal.open}
        scope={taskModal.scope}
        docTypes={docTypes}
        initialDocTypeSlugs={taskModal.initialDocTypeSlugs}
        initialTaskId={taskModal.initialTaskId}
        onClose={() => setTaskModal((prev) => ({ ...prev, open: false }))}
        onTaskCompleted={() => void fetchArchTree()}
        onOpenStudio={openStudio}
        onOpenTaskCenter={() => navigate(buildModulePath(isEmbedded, '/tasks'))}
      />
    </div>
  );
};

export default ArchTreeCoveragePage;
