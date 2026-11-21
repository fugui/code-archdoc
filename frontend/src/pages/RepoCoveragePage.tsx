import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  FolderGit2,
  Layers,
  Layers3,
  RefreshCw,
  RotateCcw,
  SlidersHorizontal,
  X,
} from 'lucide-react';
import {
  EmptyState,
  Pagination,
  useDebounce,
  usePagination,
  useToast,
} from '@code/common';
import { exportDocumentsZip } from '../api/documents';
import {
  listDepartments,
  listDocTypes,
  listRepos,
  refreshRepoModules,
} from '../api/repos';
import GenerateTaskModal, { type GenerateTaskScope } from '../components/GenerateTaskModal';
import ModuleGovernanceModal, {
  type ModuleGovernanceTarget,
} from '../components/ModuleGovernanceModal';
import ScopeCoverageTable from '../components/coverage/ScopeCoverageTable';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  ArchBindingFilter,
  ArchSubsystemOptionVO,
  DepartmentVO,
  DocTypeSummaryVO,
  RepoCoverageSummaryVO,
  RepoScopeCoverageVO,
  RepoScopeFilter,
  RepoStatusFilter,
} from '../types';
import { groupDocTypesByCategory } from '../utils/docTypeUtils';
import { formatApiError } from '../utils/errorMessages';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildModulePath,
  buildStudioPath,
  parseRepoFilterParams,
  serializeArchTreeFilterParams,
  serializeRepoFilterParams,
  shortSha,
} from '../utils/urlQuery';
import './RepoCoveragePage.css';

interface ModalLaunchState {
  open: boolean;
  scope: GenerateTaskScope | null;
  initialDocTypeSlugs?: string[];
  initialTaskId?: number | null;
  batchMode?: boolean;
  initialSelectedModules?: string[];
}

interface ModuleModalState {
  open: boolean;
  target: ModuleGovernanceTarget | null;
}

export const RepoCoveragePage: React.FC = () => {
  const navigate = useNavigate();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  const [searchParams, setSearchParams] = useSearchParams();
  const { page, pageSize } = usePagination({ defaultPageSize: DEFAULT_ARCHDOC_PAGE_SIZE });
  const filterState = useMemo(() => parseRepoFilterParams(searchParams), [searchParams]);

  const advancedFilterCount = useMemo(
    () =>
      [filterState.subsystemId, filterState.archBinding, filterState.scopeType].filter(Boolean)
        .length,
    [filterState.subsystemId, filterState.archBinding, filterState.scopeType],
  );

  const [searchInput, setSearchInput] = useState<string>(filterState.search);
  const debouncedSearch = useDebounce(searchInput, 300);
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(() => advancedFilterCount > 0);

  const [departments, setDepartments] = useState<DepartmentVO[]>([]);
  const [subsystems, setSubsystems] = useState<ArchSubsystemOptionVO[]>([]);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [rows, setRows] = useState<RepoScopeCoverageVO[]>([]);
  const [total, setTotal] = useState<number>(0);
  const [summary, setSummary] = useState<RepoCoverageSummaryVO>({
    total_scopes: 0,
    covered_scopes: 0,
    approved_docs: 0,
    drifted_docs: 0,
  });
  const [loading, setLoading] = useState<boolean>(true);
  const [exportingKey, setExportingKey] = useState<string | null>(null);
  const [discoveringRepoId, setDiscoveringRepoId] = useState<number | null>(null);
  const [selectedRepoId, setSelectedRepoId] = useState<number | null>(null);
  const [selectedRepoName, setSelectedRepoName] = useState<string>('');
  const [selectedSubdirs, setSelectedSubdirs] = useState<string[]>([]);
  const [expandedRowKeys, setExpandedRowKeys] = useState<Set<string>>(new Set());
  const [expandedSubdirRepos, setExpandedSubdirRepos] = useState<Set<number>>(new Set());

  const [taskModal, setTaskModal] = useState<ModalLaunchState>({
    open: false,
    scope: null,
    batchMode: false,
  });
  const [moduleModal, setModuleModal] = useState<ModuleModalState>({
    open: false,
    target: null,
  });

  // 兼容历史链接 /repos?view=arch：自动重定向至独立页面「架构文档全景」(/arch-tree)
  useEffect(() => {
    if (filterState.view !== 'arch') return;
    const qs = serializeArchTreeFilterParams({
      search: filterState.search,
      subsystemId: filterState.subsystemId,
      docType: filterState.docType,
      status: filterState.status,
    });
    const base = buildModulePath(isEmbedded, '/arch-tree');
    const target = qs.toString() ? `${base}?${qs.toString()}` : base;
    navigate(target, { replace: true });
  }, [
    filterState.view,
    filterState.search,
    filterState.subsystemId,
    filterState.docType,
    filterState.status,
    isEmbedded,
    navigate,
  ]);

  // 浏览器前进/后退时同步搜索输入框与高级筛选折叠态
  useEffect(() => {
    setSearchInput(filterState.search);
  }, [filterState.search]);

  useEffect(() => {
    if (advancedFilterCount > 0) {
      setAdvancedOpen(true);
    }
  }, [advancedFilterCount]);

  // 防抖搜索词回写 URL 并重置第 1 页
  useEffect(() => {
    if (filterState.view === 'arch') return;
    if (debouncedSearch.trim() === filterState.search) return;
    setSearchParams(
      (prev) => serializeRepoFilterParams({ search: debouncedSearch.trim(), page: 1 }, prev),
      { replace: true },
    );
  }, [debouncedSearch, filterState.search, filterState.view, setSearchParams]);

  // 拉取部门与启用中的 DocType 主数据
  useEffect(() => {
    let active = true;
    void Promise.all([listDepartments(), listDocTypes(true)])
      .then(([deptList, dtList]) => {
        if (!active) return;
        setDepartments(deptList);
        setDocTypes(dtList);
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载筛选项数据失败').message, 'error');
      });
    return () => {
      active = false;
    };
  }, [showToast]);

  const docTypeGroups = useMemo(() => groupDocTypesByCategory(docTypes), [docTypes]);

  const fetchCoverage = useCallback(async () => {
    if (filterState.view === 'arch') return;
    setLoading(true);
    try {
      const res = await listRepos({
        page,
        pageSize,
        search: filterState.search,
        departmentId: filterState.departmentId,
        docType: filterState.docType,
        status: filterState.status,
        scopeType: filterState.scopeType,
        subsystemId: filterState.subsystemId,
        archBinding: filterState.archBinding,
      });
      const nextRows = Array.isArray(res.items) ? res.items : [];
      setRows(nextRows);
      setSelectedSubdirs((prev) => {
        if (prev.length === 0) return prev;
        const validSet = new Set(
          nextRows
            .filter(
              (r) =>
                r.repo_id === selectedRepoId &&
                r.scope_type === 'subdir' &&
                r.can_write &&
                r.scope_status !== 'missing',
            )
            .map((r) => r.subdirectory),
        );
        const filtered = prev.filter((sub) => validSet.has(sub));
        if (filtered.length === 0) {
          setSelectedRepoId(null);
          setSelectedRepoName('');
        }
        return filtered;
      });
      setTotal(typeof res.total === 'number' ? res.total : 0);
      if (res.summary) {
        setSummary(res.summary);
      }
      if (Array.isArray(res.subsystems)) {
        setSubsystems(res.subsystems);
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载仓库文档覆盖矩阵失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    filterState.view,
    filterState.search,
    filterState.departmentId,
    filterState.docType,
    filterState.status,
    filterState.scopeType,
    filterState.subsystemId,
    filterState.archBinding,
    page,
    pageSize,
    selectedRepoId,
    showToast,
  ]);

  useEffect(() => {
    void fetchCoverage();
  }, [fetchCoverage]);

  const clearSubdirSelection = useCallback(() => {
    setSelectedRepoId(null);
    setSelectedRepoName('');
    setSelectedSubdirs([]);
  }, []);

  const updateFilter = (patch: {
    departmentId?: string;
    docType?: string;
    status?: RepoStatusFilter;
    scopeType?: RepoScopeFilter;
    subsystemId?: string;
    archBinding?: ArchBindingFilter;
  }) => {
    setSearchParams((prev) => serializeRepoFilterParams({ ...patch, page: 1 }, prev));
  };

  const resetFilters = () => {
    setSearchInput('');
    setSearchParams((prev) =>
      serializeRepoFilterParams(
        {
          page: 1,
          search: '',
          departmentId: '',
          docType: '',
          status: '',
          scopeType: '',
          subsystemId: '',
          archBinding: '',
        },
        prev,
      ),
    );
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

  const toggleSubdirRepo = useCallback((repoId: number) => {
    setExpandedSubdirRepos((prev) => {
      const next = new Set(prev);
      if (next.has(repoId)) {
        next.delete(repoId);
      } else {
        next.add(repoId);
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
        batchMode?: boolean;
        initialSelectedModules?: string[];
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
        batchMode: Boolean(options?.batchMode),
        initialSelectedModules: options?.initialSelectedModules,
      });
    },
    [],
  );

  const openModuleGovernanceModal = useCallback((row: RepoScopeCoverageVO) => {
    setModuleModal({
      open: true,
      target: {
        repoId: row.repo_id,
        repoName: row.repo_name,
        branch: row.branch || 'master',
        canWrite: row.can_write,
      },
    });
  }, []);

  const handleDiscoverRepoModules = useCallback(
    async (row: RepoScopeCoverageVO) => {
      setDiscoveringRepoId(row.repo_id);
      try {
        const res = await refreshRepoModules(row.repo_id);
        if (res.active_count > 0) {
          setExpandedSubdirRepos((prev) => {
            const next = new Set(prev);
            next.add(row.repo_id);
            return next;
          });
          showToast(
            `已完成仓库 ${row.repo_name} 一级目录扫描（${shortSha(res.commit_sha)}），发现 ${res.active_count} 个子模块`,
            'success',
          );
        } else {
          showToast(
            `已完成仓库 ${row.repo_name} 扫描（${shortSha(res.commit_sha)}），未发现独立一级业务子目录（可在模块治理调整策略或阈值）`,
            'info',
          );
        }
        await fetchCoverage();
      } catch (err: unknown) {
        showToast(formatApiError(err, '扫描仓库一级目录失败').message, 'error');
      } finally {
        setDiscoveringRepoId(null);
      }
    },
    [fetchCoverage, showToast],
  );

  const toggleSubdirRowSelection = useCallback(
    (row: RepoScopeCoverageVO) => {
      if (selectedRepoId !== row.repo_id) {
        setSelectedRepoId(row.repo_id);
        setSelectedRepoName(row.repo_name);
        setSelectedSubdirs([row.subdirectory]);
        return;
      }
      setSelectedSubdirs((prev) => {
        const next = prev.includes(row.subdirectory)
          ? prev.filter((item) => item !== row.subdirectory)
          : [...prev, row.subdirectory];
        if (next.length === 0) {
          setSelectedRepoId(null);
          setSelectedRepoName('');
        }
        return next;
      });
    },
    [selectedRepoId],
  );

  const handleBatchGenerateSelectedRows = () => {
    if (!selectedRepoId || selectedSubdirs.length === 0) return;
    const anchorRow =
      rows.find((r) => r.repo_id === selectedRepoId && r.scope_type === 'repo') ??
      rows.find((r) => r.repo_id === selectedRepoId);
    if (!anchorRow) return;
    openGenerateModal(anchorRow, {
      batchMode: true,
      initialSelectedModules: selectedSubdirs,
    });
  };

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

  const hasActiveFilters = Boolean(
    filterState.search ||
      filterState.departmentId ||
      filterState.docType ||
      filterState.status ||
      filterState.scopeType ||
      filterState.subsystemId ||
      filterState.archBinding,
  );

  // 当前页中存在整仓父行的仓库 ID 集合，以及按仓库聚合的子目录行数
  const repoRowIds = useMemo(
    () => new Set(rows.filter((r) => r.scope_type === 'repo').map((r) => r.repo_id)),
    [rows],
  );

  const subdirCountsByRepo = useMemo(() => {
    const counts = new Map<number, number>();
    for (const r of rows) {
      if (r.scope_type === 'subdir' && repoRowIds.has(r.repo_id)) {
        counts.set(r.repo_id, (counts.get(r.repo_id) ?? 0) + 1);
      }
    }
    return counts;
  }, [rows, repoRowIds]);

  const visibleRows = useMemo(() => {
    if (hasActiveFilters) {
      return rows;
    }
    return rows.filter(
      (r) =>
        !(
          r.scope_type === 'subdir' &&
          repoRowIds.has(r.repo_id) &&
          !expandedSubdirRepos.has(r.repo_id)
        ),
    );
  }, [rows, hasActiveFilters, repoRowIds, expandedSubdirRepos]);

  const collapsibleRepoIds = useMemo(() => Array.from(subdirCountsByRepo.keys()), [subdirCountsByRepo]);
  const allPageSubdirsExpanded =
    collapsibleRepoIds.length > 0 &&
    collapsibleRepoIds.every((id) => expandedSubdirRepos.has(id));

  const toggleAllPageSubdirs = () => {
    if (allPageSubdirsExpanded) {
      setExpandedSubdirRepos((prev) => {
        const next = new Set(prev);
        collapsibleRepoIds.forEach((id) => next.delete(id));
        return next;
      });
    } else {
      setExpandedSubdirRepos((prev) => {
        const next = new Set(prev);
        collapsibleRepoIds.forEach((id) => next.add(id));
        return next;
      });
    }
  };

  if (filterState.view === 'arch') {
    return null;
  }

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">仓库文档覆盖</h1>
          <p className="code-archdoc-page-subtitle">
            按代码仓库与 Monorepo 子目录追踪设计文档覆盖率、生成进度与定稿状态。
          </p>
        </div>
        <div className="code-archdoc-page-actions">
          <button
            type="button"
            className="code-archdoc-ghost-btn"
            onClick={() => void fetchCoverage()}
            disabled={loading}
          >
            <RefreshCw size={14} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新矩阵</span>
          </button>
        </div>
      </div>

      {/* 全局覆盖率 KPI 摘要 */}
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

      {/* 单行高频筛选 + 可折叠更多筛选 */}
      <div className="code-card code-archdoc-coverage-toolbar code-archdoc-coverage-toolbar-stack">
        <div className="code-archdoc-coverage-filters">
          <input
            type="text"
            className="code-input code-archdoc-coverage-search"
            placeholder="搜索仓库名、子目录路径或架构元素..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            aria-label="仓库关键词搜索"
          />

          <select
            className="code-select code-archdoc-coverage-select"
            value={filterState.departmentId}
            onChange={(e) => updateFilter({ departmentId: e.target.value })}
            aria-label="按所属部门筛选"
          >
            <option value="">全部部门</option>
            {departments.map((d) => (
              <option key={d.id} value={String(d.id)}>
                {d.name}
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

          <button
            type="button"
            className={`code-archdoc-ghost-btn ${
              advancedOpen || advancedFilterCount > 0 ? 'code-archdoc-ghost-btn--active' : ''
            }`}
            aria-expanded={advancedOpen}
            onClick={() => setAdvancedOpen((prev) => !prev)}
          >
            <SlidersHorizontal size={13} />
            <span>更多筛选</span>
            {advancedFilterCount > 0 && (
              <span className="code-archdoc-filter-badge">{advancedFilterCount}</span>
            )}
            {advancedOpen ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
          </button>

          {hasActiveFilters && (
            <button type="button" className="code-archdoc-ghost-btn" onClick={resetFilters}>
              <RotateCcw size={13} />
              <span>重置筛选</span>
            </button>
          )}
        </div>

        {advancedOpen && (
          <div className="code-archdoc-coverage-advanced-filters">
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
              value={filterState.archBinding}
              onChange={(e) => updateFilter({ archBinding: e.target.value as ArchBindingFilter })}
              aria-label="按架构关联状态筛选"
            >
              <option value="">全部架构关联</option>
              <option value="bound">已关联架构元素</option>
              <option value="unbound">未关联架构元素</option>
            </select>

            <select
              className="code-select code-archdoc-coverage-select"
              value={filterState.scopeType}
              onChange={(e) => updateFilter({ scopeType: e.target.value as RepoScopeFilter })}
              aria-label="按范围粒度筛选"
            >
              <option value="">全部粒度</option>
              <option value="repo">整仓</option>
              <option value="subdir">Monorepo 子目录</option>
            </select>
          </div>
        )}
      </div>

      {/* 同仓多选一级目录批量生成操作条 */}
      {selectedRepoId !== null && selectedSubdirs.length > 0 && (
        <div className="code-archdoc-coverage-selection-bar">
          <div className="code-archdoc-coverage-selection-info">
            <Layers3 size={15} />
            <span>
              已选择仓库 <strong>{selectedRepoName}</strong> 下的{' '}
              <strong>{selectedSubdirs.length}</strong> 个子目录
            </span>
          </div>
          <div className="code-archdoc-coverage-selection-actions">
            <button
              type="button"
              className="btn btn-primary btn-small"
              onClick={handleBatchGenerateSelectedRows}
            >
              <Layers3 size={13} />
              <span>批量生成所选模块</span>
            </button>
            <button
              type="button"
              className="code-archdoc-ghost-btn"
              onClick={clearSubdirSelection}
            >
              <X size={13} />
              <span>清空选择</span>
            </button>
          </div>
        </div>
      )}

      {/* 主表格区 */}
      <div className="code-card code-archdoc-coverage-table-card">
        {!loading && !hasActiveFilters && collapsibleRepoIds.length > 0 && (
          <div className="code-archdoc-coverage-table-bar">
            <span className="text-secondary">
              当前页含 {collapsibleRepoIds.length} 个多模块仓库，默认按仓库折叠子目录（点击「N 个子模块」即可展开）。
            </span>
            <button
              type="button"
              className="code-archdoc-ghost-btn"
              onClick={toggleAllPageSubdirs}
            >
              {allPageSubdirsExpanded ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
              <span>{allPageSubdirsExpanded ? '收起当前页子目录' : '展开当前页子目录'}</span>
            </button>
          </div>
        )}

        {loading ? (
          <table className="code-table">
            <thead>
              <tr>
                <th>仓库 / 子目录范围</th>
                <th>所属架构元素</th>
                <th>负责人与提交</th>
                <th>文档覆盖（{docTypes.length} 类）</th>
                <th className="code-archdoc-coverage-actions-col">操作</th>
              </tr>
            </thead>
            <tbody>
              <EmptyState
                inTable
                colSpan={5}
                compact
                title="正在加载仓库覆盖矩阵..."
                description="正在汇总各仓库与子目录的文档状态"
              />
            </tbody>
          </table>
        ) : visibleRows.length === 0 ? (
          <table className="code-table">
            <thead>
              <tr>
                <th>仓库 / 子目录范围</th>
                <th>所属架构元素</th>
                <th>负责人与提交</th>
                <th>文档覆盖（{docTypes.length} 类）</th>
                <th className="code-archdoc-coverage-actions-col">操作</th>
              </tr>
            </thead>
            <tbody>
              <EmptyState
                inTable
                colSpan={5}
                type={hasActiveFilters ? 'search' : 'data'}
                title={hasActiveFilters ? '未找到匹配当前筛选条件的范围' : '暂无可用代码仓库'}
                description={
                  hasActiveFilters
                    ? '请调整搜索词或点击“重置筛选”查看全部仓库'
                    : '请先在平台中纳管代码仓库或架构元素'
                }
                action={
                  hasActiveFilters ? (
                    <button
                      type="button"
                      className="code-archdoc-ghost-btn"
                      onClick={resetFilters}
                    >
                      重置筛选
                    </button>
                  ) : undefined
                }
              />
            </tbody>
          </table>
        ) : (
          <div className="code-archdoc-coverage-table-wrap">
            <ScopeCoverageTable
              rows={visibleRows}
              docTypes={docTypes}
              focusedDocTypeSlug={filterState.docType}
              expandedRowKeys={expandedRowKeys}
              showArchColumn
              showSelection
              selectedRepoId={selectedRepoId}
              selectedSubdirs={selectedSubdirs}
              discoveringRepoId={discoveringRepoId}
              exportingKey={exportingKey}
              subdirCountsByRepo={hasActiveFilters ? undefined : subdirCountsByRepo}
              expandedSubdirRepos={hasActiveFilters ? undefined : expandedSubdirRepos}
              onToggleSubdirRepo={hasActiveFilters ? undefined : toggleSubdirRepo}
              onToggleExpandRow={toggleExpandRow}
              onToggleSelectSubdir={toggleSubdirRowSelection}
              onDiscoverModules={(r) => void handleDiscoverRepoModules(r)}
              onOpenModuleGovernance={openModuleGovernanceModal}
              onOpenActiveTask={(r) => openGenerateModal(r, { initialTaskId: r.active_task_id })}
              onOpenStudio={openStudio}
              onTriggerGenerate={(r, slug) =>
                openGenerateModal(r, slug ? { initialDocTypeSlugs: [slug] } : undefined)
              }
              onBatchGenerate={(r) => openGenerateModal(r, { batchMode: true })}
              onExportZip={(r) => void handleExportRowZip(r)}
            />
          </div>
        )}

        {total > 0 && (
          <div className="code-archdoc-coverage-pagination">
            <Pagination
              totalItems={total}
              defaultPageSize={DEFAULT_ARCHDOC_PAGE_SIZE}
              pageSizeOptions={[15, 25, 50, 100]}
            />
          </div>
        )}
      </div>

      {/* 生成任务触发与实时进度监控弹窗（兼容单范围与批量分层编排） */}
      <GenerateTaskModal
        open={taskModal.open}
        scope={taskModal.scope}
        docTypes={docTypes}
        initialDocTypeSlugs={taskModal.initialDocTypeSlugs}
        initialTaskId={taskModal.initialTaskId}
        batchMode={taskModal.batchMode}
        initialSelectedModules={taskModal.initialSelectedModules}
        onClose={() => setTaskModal((prev) => ({ ...prev, open: false }))}
        onTaskCompleted={() => void fetchCoverage()}
        onModulesRefreshed={() => void fetchCoverage()}
        onOpenStudio={openStudio}
        onOpenTaskCenter={() => navigate(buildModulePath(isEmbedded, '/tasks'))}
      />

      {/* 仓库模块发现与子目录迁移治理弹窗 */}
      <ModuleGovernanceModal
        open={moduleModal.open}
        target={moduleModal.target}
        onClose={() => setModuleModal((prev) => ({ ...prev, open: false }))}
        onChanged={() => void fetchCoverage()}
        onGenerateModule={(subdir) => {
          const target = moduleModal.target;
          if (!target) return;
          setModuleModal((prev) => ({ ...prev, open: false }));
          setTaskModal({
            open: true,
            scope: {
              repoId: target.repoId,
              repoName: target.repoName,
              subdirectory: subdir,
              branch: target.branch,
              canWrite: target.canWrite,
            },
            batchMode: false,
          });
        }}
        onBatchGenerateModules={(subdirs) => {
          const target = moduleModal.target;
          if (!target) return;
          setModuleModal((prev) => ({ ...prev, open: false }));
          setTaskModal({
            open: true,
            scope: {
              repoId: target.repoId,
              repoName: target.repoName,
              subdirectory: '',
              branch: target.branch,
              canWrite: target.canWrite,
            },
            batchMode: true,
            initialSelectedModules: subdirs,
          });
        }}
        onOpenStudio={(repoId, subdir) => {
          setModuleModal((prev) => ({ ...prev, open: false }));
          openStudio(repoId, undefined, subdir);
        }}
      />
    </div>
  );
};

export default RepoCoveragePage;
