import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  Activity,
  AlertTriangle,
  Archive,
  BookOpen,
  CheckCircle2,
  FolderGit2,
  FolderTree,
  Layers,
  Layers3,
  RefreshCw,
  RotateCcw,
  Sparkles,
} from 'lucide-react';
import {
  EmptyState,
  Pagination,
  StatusTag,
  useDebounce,
  usePagination,
  useToast,
  type StatusType,
} from '@code/common';
import { exportDocumentsZip } from '../api/documents';
import { listDepartments, listDocTypes, listRepos } from '../api/repos';
import GenerateTaskModal, { type GenerateTaskScope } from '../components/GenerateTaskModal';
import ModuleGovernanceModal, {
  type ModuleGovernanceTarget,
} from '../components/ModuleGovernanceModal';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  DepartmentVO,
  DocStatusSummaryVO,
  DocTypeSummaryVO,
  RepoCoverageSummaryVO,
  RepoScopeCoverageVO,
  RepoScopeFilter,
  RepoStatusFilter,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildStudioPath,
  parseRepoFilterParams,
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
}

interface ModuleModalState {
  open: boolean;
  target: ModuleGovernanceTarget | null;
}

function moduleSourceShortLabel(source?: string): string {
  switch (source) {
    case 'arch_element':
      return '架构元素';
    case 'build_manifest':
      return '构建清单';
    case 'first_level_dir':
      return '自动发现';
    case 'manual':
      return '手动';
    default:
      return '';
  }
}

function docStatusTag(ds: DocStatusSummaryVO): { status: StatusType; label: string } {
  if (ds.status === 'approved') {
    if (ds.has_code_drift || ds.has_spec_drift) {
      return { status: 'warning', label: `已发布·漂移(${ds.affected_slot_count || 1})` };
    }
    return { status: 'success', label: `已发布 v${ds.published_revision_no || ds.current_revision_no}` };
  }
  if (!ds.can_approve) {
    return { status: 'failed', label: `草稿·阻断 v${ds.current_revision_no}` };
  }
  return { status: 'pending', label: `草稿 v${ds.current_revision_no}` };
}

export const RepoCoveragePage: React.FC = () => {
  const navigate = useNavigate();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  const [searchParams, setSearchParams] = useSearchParams();
  const { page, pageSize } = usePagination({ defaultPageSize: DEFAULT_ARCHDOC_PAGE_SIZE });
  const filterState = useMemo(() => parseRepoFilterParams(searchParams), [searchParams]);

  const [searchInput, setSearchInput] = useState<string>(filterState.search);
  const debouncedSearch = useDebounce(searchInput, 300);

  const [departments, setDepartments] = useState<DepartmentVO[]>([]);
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

  const [taskModal, setTaskModal] = useState<ModalLaunchState>({
    open: false,
    scope: null,
    batchMode: false,
  });
  const [moduleModal, setModuleModal] = useState<ModuleModalState>({
    open: false,
    target: null,
  });

  // 浏览器前进/后退时同步搜索输入框
  useEffect(() => {
    setSearchInput(filterState.search);
  }, [filterState.search]);

  // 防抖搜索词回写 URL 并重置第 1 页
  useEffect(() => {
    if (debouncedSearch.trim() === filterState.search) return;
    setSearchParams(
      (prev) => serializeRepoFilterParams({ search: debouncedSearch.trim(), page: 1 }, prev),
      { replace: true },
    );
  }, [debouncedSearch, filterState.search, setSearchParams]);

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

  const fetchCoverage = useCallback(async () => {
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
      });
      setRows(Array.isArray(res.items) ? res.items : []);
      setTotal(typeof res.total === 'number' ? res.total : 0);
      if (res.summary) {
        setSummary(res.summary);
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载仓库文档覆盖矩阵失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    page,
    pageSize,
    filterState.search,
    filterState.departmentId,
    filterState.docType,
    filterState.status,
    filterState.scopeType,
    showToast,
  ]);

  useEffect(() => {
    void fetchCoverage();
  }, [fetchCoverage]);

  const updateFilter = (patch: {
    departmentId?: string;
    docType?: string;
    status?: RepoStatusFilter;
    scopeType?: RepoScopeFilter;
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
        },
        prev,
      ),
    );
  };

  const openStudio = useCallback(
    (repoId: number, docId?: number, subdirectory?: string) => {
      navigate(buildStudioPath(isEmbedded, repoId, { docId, subdirectory }));
    },
    [isEmbedded, navigate],
  );

  const openGenerateModal = (
    row: RepoScopeCoverageVO,
    options?: {
      initialDocTypeSlugs?: string[];
      initialTaskId?: number | null;
      batchMode?: boolean;
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
    });
  };

  const openModuleGovernanceModal = (row: RepoScopeCoverageVO) => {
    setModuleModal({
      open: true,
      target: {
        repoId: row.repo_id,
        repoName: row.repo_name,
        branch: row.branch || 'master',
        canWrite: row.can_write,
      },
    });
  };

  const handleExportRowZip = async (row: RepoScopeCoverageVO) => {
    const approvedDocIds = Object.values(row.doc_statuses)
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
  };

  const hasActiveFilters = Boolean(
    filterState.search ||
      filterState.departmentId ||
      filterState.docType ||
      filterState.status ||
      filterState.scopeType,
  );

  const visibleDocTypes = useMemo(() => docTypes.slice(0, 6), [docTypes]);
  const tableColSpan = 3 + visibleDocTypes.length;

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">仓库与文档覆盖矩阵</h1>
          <p className="code-archdoc-page-subtitle">
            按整仓或 Monorepo 架构子目录追踪各类设计文档的生成、审校、定稿发布与漂移覆盖状态。
          </p>
        </div>
        <div className="code-archdoc-page-actions">
          <button
            type="button"
            className="btn btn-secondary btn-small"
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

      {/* URL 双向同步筛选工具栏 */}
      <div className="code-card code-archdoc-coverage-toolbar">
        <div className="code-archdoc-coverage-filters">
          <input
            type="text"
            className="code-input code-archdoc-coverage-search"
            placeholder="搜索仓库名、子目录路径或架构元素名称..."
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
            {docTypes.map((dt) => (
              <option key={dt.slug} value={dt.slug}>
                {dt.display_name}
              </option>
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

          {hasActiveFilters && (
            <button type="button" className="btn btn-secondary btn-small" onClick={resetFilters}>
              <RotateCcw size={13} />
              <span>重置筛选</span>
            </button>
          )}
        </div>
      </div>

      {/* 仓库与子目录文档覆盖矩阵表格 */}
      <div className="code-card code-archdoc-coverage-table-card">
        <div className="code-archdoc-coverage-table-scroll">
          <table className="code-table">
            <thead>
              <tr>
                <th>仓库 / 子目录范围</th>
                <th>负责人与提交</th>
                {visibleDocTypes.map((dt) => (
                  <th key={dt.slug} title={dt.description}>
                    {dt.display_name}
                  </th>
                ))}
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <EmptyState
                  inTable
                  colSpan={tableColSpan}
                  compact
                  title="正在加载仓库覆盖矩阵..."
                  description="正在汇总各仓库与子目录的文档状态"
                />
              ) : rows.length === 0 ? (
                <EmptyState
                  inTable
                  colSpan={tableColSpan}
                  type={hasActiveFilters ? 'search' : 'data'}
                  title={hasActiveFilters ? '未找到匹配当前筛选条件的范围' : '暂无可用代码仓库'}
                  description={
                    hasActiveFilters
                      ? '请调整搜索词或点击“重置筛选”查看全部仓库'
                      : '请先在平台中纳管代码仓库或架构元素'
                  }
                  action={
                    hasActiveFilters ? (
                      <button type="button" className="btn btn-secondary btn-small" onClick={resetFilters}>
                        重置筛选
                      </button>
                    ) : undefined
                  }
                />
              ) : (
                rows.map((row) => {
                  const rowKey = `${row.repo_id}:${row.subdirectory}`;
                  const approvedCount = Object.values(row.doc_statuses).filter(
                    (d) => d.status === 'approved',
                  ).length;
                  const firstDoc = Object.values(row.doc_statuses)[0];

                  return (
                    <tr key={rowKey}>
                      <td>
                        <div className="code-archdoc-coverage-identity">
                          <div className="code-archdoc-coverage-repo-row">
                            <span className="code-archdoc-coverage-repo-name">{row.repo_name}</span>
                            {row.scope_type === 'subdir' ? (
                              <span className="code-badge code-badge--purple">{row.subdirectory}</span>
                            ) : (
                              <span className="code-badge code-badge--muted">整仓</span>
                            )}
                            {row.scope_status === 'missing' && (
                              <span className="code-badge code-badge--warning" title="该子目录在最近提交中已缺失，可在模块治理中迁移文档">
                                目录已缺失
                              </span>
                            )}
                            {row.scope_status === 'orphan' && (
                              <span className="code-badge code-badge--warning" title="历史遗留子目录文档范围（未在当前模块索引中），建议在模块治理中迁移">
                                孤立范围
                              </span>
                            )}
                            {row.module_source && moduleSourceShortLabel(row.module_source) && (
                              <span className="code-badge code-badge--muted">
                                {moduleSourceShortLabel(row.module_source)}
                              </span>
                            )}
                            {row.active_task_id && (
                              <button
                                type="button"
                                className="code-archdoc-coverage-active-task-btn"
                                onClick={() =>
                                  openGenerateModal(row, { initialTaskId: row.active_task_id })
                                }
                                title="点击查看正在执行的反推生成任务进度"
                              >
                                <Activity size={12} />
                                <span>任务 #{row.active_task_id}</span>
                              </button>
                            )}
                          </div>
                          <div className="code-archdoc-coverage-meta-row">
                            {row.department_name && <span>{row.department_name}</span>}
                            {row.arch_element_name && <span>· {row.arch_element_name}</span>}
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
                      </td>

                      <td>
                        <div>{row.owner_name || '-'}</div>
                        <div className="code-archdoc-coverage-meta-row">
                          <span title={row.latest_commit_sha || '暂无快照'}>
                            Commit: <code className="code-archdoc-mono">{shortSha(row.latest_commit_sha)}</code>
                          </span>
                        </div>
                      </td>

                      {visibleDocTypes.map((dt) => {
                        const ds = row.doc_statuses[dt.slug];
                        if (ds) {
                          const tagMeta = docStatusTag(ds);
                          return (
                            <td key={dt.slug}>
                              <button
                                type="button"
                                className="code-archdoc-coverage-doc-cell"
                                onClick={() => openStudio(row.repo_id, ds.document_id, row.subdirectory)}
                                title={`进入工作台查看《${dt.display_name}》`}
                              >
                                <StatusTag status={tagMeta.status} text={tagMeta.label} size="sm" />
                              </button>
                            </td>
                          );
                        }
                        return (
                          <td key={dt.slug}>
                            <button
                              type="button"
                              className="code-archdoc-coverage-doc-cell"
                              disabled={!row.can_write}
                              onClick={() =>
                                openGenerateModal(row, { initialDocTypeSlugs: [dt.slug] })
                              }
                              title={
                                row.can_write
                                  ? `点击为当前范围触发生成《${dt.display_name}》`
                                  : '尚未生成（仅仓库/子目录负责人或管理员可触发生成）'
                              }
                            >
                              <span className="code-archdoc-coverage-missing-pill">未生成</span>
                            </button>
                          </td>
                        );
                      })}

                      <td>
                        <div className="code-archdoc-coverage-actions">
                          <button
                            type="button"
                            className="btn btn-text btn-small"
                            onClick={() =>
                              openStudio(row.repo_id, firstDoc?.document_id, row.subdirectory)
                            }
                            title="进入 Canvas Studio 工作台"
                          >
                            <BookOpen size={14} />
                            <span>工作台</span>
                          </button>

                          <button
                            type="button"
                            className="btn btn-text btn-small"
                            disabled={!row.can_write}
                            onClick={() => openGenerateModal(row)}
                            title={
                              row.can_write
                                ? '触发 AI 设计文档反推生成'
                                : '权限不足：仅仓库/子目录负责人或管理员可触发生成'
                            }
                          >
                            <Sparkles size={14} />
                            <span>触发生成</span>
                          </button>

                          {row.scope_type === 'repo' && (
                            <>
                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                disabled={!row.can_write}
                                onClick={() => openGenerateModal(row, { batchMode: true })}
                                title={
                                  row.can_write
                                    ? '一键批量生成各活跃子模块与整仓分层文档'
                                    : '权限不足：仅仓库负责人或管理员可发起批量生成'
                                }
                              >
                                <Layers3 size={14} />
                                <span>批量生成</span>
                              </button>

                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                onClick={() => openModuleGovernanceModal(row)}
                                title="配置模块发现策略、扫描子模块或迁移重命名目录文档"
                              >
                                <FolderTree size={14} />
                                <span>模块治理</span>
                              </button>
                            </>
                          )}

                          <button
                            type="button"
                            className="btn btn-text btn-small"
                            disabled={approvedCount === 0 || exportingKey === rowKey}
                            onClick={() => void handleExportRowZip(row)}
                            title={
                              approvedCount > 0
                                ? `打包下载当前范围 ${approvedCount} 篇已发布文档 ZIP`
                                : '暂无已发布文档可导出'
                            }
                          >
                            <Archive size={14} />
                            <span>{exportingKey === rowKey ? '导出中...' : 'ZIP 导出'}</span>
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>

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
        onClose={() => setTaskModal((prev) => ({ ...prev, open: false }))}
        onTaskCompleted={() => void fetchCoverage()}
        onOpenStudio={openStudio}
      />

      {/* 仓库模块发现与子目录迁移治理弹窗 */}
      <ModuleGovernanceModal
        open={moduleModal.open}
        target={moduleModal.target}
        onClose={() => setModuleModal((prev) => ({ ...prev, open: false }))}
        onChanged={() => void fetchCoverage()}
      />
    </div>
  );
};

export default RepoCoveragePage;
