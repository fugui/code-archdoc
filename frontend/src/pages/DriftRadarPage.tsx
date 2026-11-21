import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  ArrowRight,
  BookOpen,
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileText,
  Loader2,
  Lock,
  Radar,
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
import {
  getDriftSummary,
  listDriftedDocuments,
  triggerAdminDriftPoll,
  triggerIncrementalRefresh,
} from '../api/drift';
import { listDepartments, listDocTypes } from '../api/repos';
import OverviewMicroPills from '../components/OverviewMicroPills';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  AffectedSlotDrift,
  DepartmentVO,
  DocTypeSummaryVO,
  DriftDimensionVO,
  DriftDocumentVO,
  DriftFilterType,
  DriftSummaryVO,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildStudioPath,
  formatDateTime,
  parseDriftFilterParams,
  serializeDriftFilterParams,
  shortSha,
} from '../utils/urlQuery';
import './DriftRadarPage.css';

function driftReasonLabel(reason: string): { text: string; status: StatusType } {
  switch (reason) {
    case 'file_removed_or_renamed':
      return { text: '证据文件删除或重命名', status: 'failed' };
    case 'symbol_removed':
      return { text: '锚定符号已移除', status: 'failed' };
    case 'symbol_modified':
      return { text: '锚定符号语义变更', status: 'warning' };
    case 'file_modified':
      return { text: '证据文件内容变更', status: 'warning' };
    case 'external_contract_removed':
      return { text: '上游外部契约文件已移除', status: 'failed' };
    case 'external_contract_modified':
      return { text: '上游外部契约符号变更', status: 'warning' };
    case 'module_digest_changed':
      return { text: '子模块文档摘要已更新', status: 'warning' };
    case 'module_scope_changed':
      return { text: '仓库活跃模块范围变化', status: 'running' };
    case 'slot_added':
      return { text: '模板新增槽位', status: 'running' };
    case 'slot_removed':
      return { text: '模板移除槽位', status: 'failed' };
    case 'slot_modified':
      return { text: '槽位规范已变更', status: 'warning' };
    case 'prompt_changed':
      return { text: '全局模板/Prompt 变更', status: 'pending' };
    default:
      return { text: reason || '内容漂移', status: 'warning' };
  }
}

const DriftDimensionDetailCard: React.FC<{
  title: string;
  icon: React.ReactNode;
  dim: DriftDimensionVO;
}> = ({ title, icon, dim }) => (
  <div className="code-archdoc-drift-dim-card">
    <div className="code-archdoc-drift-dim-head">
      <span className="code-archdoc-drift-dim-title">
        {icon}
        <span>{title}</span>
      </span>
      <StatusTag
        status={dim.precision === 'section_level' ? 'running' : 'warning'}
        text={
          dim.precision === 'section_level'
            ? `章节级精准定位 · ${dim.affected_slot_count} 槽位`
            : '仓库级降级定位'
        }
        size="sm"
      />
    </div>

    {dim.affected_slots.length === 0 ? (
      <div className="code-archdoc-drift-kpi-sub">
        本地沙箱已回收或处于仓库级降级检测，触发增量刷新将自动同步最新源码并定位受影响章节。
      </div>
    ) : (
      <div className="code-archdoc-drift-slot-list">
        {dim.affected_slots.map((slot: AffectedSlotDrift) => {
          const reasonMeta = driftReasonLabel(slot.reason);
          return (
            <div key={`${slot.slot_id}-${slot.reason}`} className="code-archdoc-drift-slot-item">
              <div className="code-archdoc-drift-slot-top">
                <span className="code-archdoc-drift-slot-title">
                  {slot.slot_title || slot.slot_id}{' '}
                  <code className="code-archdoc-mono">{slot.slot_id}</code>
                </span>
                <div className="code-archdoc-drift-slot-tags">
                  {slot.ownership === 'locked' && (
                    <StatusTag status="neutral" text="已锁定（刷新跳过）" size="sm" />
                  )}
                  <StatusTag status={reasonMeta.status} text={reasonMeta.text} size="sm" />
                </div>
              </div>

              {slot.changed_symbols && slot.changed_symbols.length > 0 && (
                <div className="code-archdoc-drift-symbols">
                  <span>变更符号：</span>
                  {slot.changed_symbols.map((sym) => (
                    <code key={sym} className="code-archdoc-mono">
                      {sym}
                    </code>
                  ))}
                </div>
              )}

              {slot.changed_files && slot.changed_files.length > 0 && (
                <div className="code-archdoc-drift-symbols">
                  <span>涉及文件：</span>
                  {slot.changed_files.map((fp) => (
                    <code key={fp} className="code-archdoc-mono">
                      {fp}
                    </code>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    )}
  </div>
);

export const DriftRadarPage: React.FC = () => {
  const navigate = useNavigate();
  const { user, isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  const [searchParams, setSearchParams] = useSearchParams();
  const { page, pageSize } = usePagination({ defaultPageSize: DEFAULT_ARCHDOC_PAGE_SIZE });
  const filterState = useMemo(() => parseDriftFilterParams(searchParams), [searchParams]);

  const [searchInput, setSearchInput] = useState<string>(filterState.search);
  const debouncedSearch = useDebounce(searchInput, 300);

  const [departments, setDepartments] = useState<DepartmentVO[]>([]);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [summary, setSummary] = useState<DriftSummaryVO>({
    total_drifted_docs: 0,
    code_drift_docs: 0,
    spec_drift_docs: 0,
    both_drift_docs: 0,
    monitored_repos: 0,
    unhealthy_repos: 0,
    last_poll_at: null,
    poll_interval_minutes: 15,
  });
  const [rows, setRows] = useState<DriftDocumentVO[]>([]);
  const [total, setTotal] = useState<number>(0);
  const [loading, setLoading] = useState<boolean>(true);
  const [polling, setPolling] = useState<boolean>(false);
  const [refreshingDocId, setRefreshingDocId] = useState<number | null>(null);
  const [expandedIds, setExpandedIds] = useState<Record<number, boolean>>({});

  useEffect(() => {
    setSearchInput(filterState.search);
  }, [filterState.search]);

  useEffect(() => {
    if (debouncedSearch.trim() === filterState.search) return;
    setSearchParams(
      (prev) => serializeDriftFilterParams({ search: debouncedSearch.trim(), page: 1 }, prev),
      { replace: true },
    );
  }, [debouncedSearch, filterState.search, setSearchParams]);

  useEffect(() => {
    let active = true;
    void Promise.all([listDepartments(), listDocTypes(false)])
      .then(([depts, dts]) => {
        if (!active) return;
        setDepartments(depts);
        setDocTypes(dts);
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载筛选项失败').message, 'error');
      });
    return () => {
      active = false;
    };
  }, [showToast]);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [sumRes, listRes] = await Promise.all([
        getDriftSummary(),
        listDriftedDocuments({
          page,
          pageSize,
          search: filterState.search,
          departmentId: filterState.departmentId,
          driftType: filterState.driftType,
          docType: filterState.docType,
        }),
      ]);
      setSummary(sumRes);
      setRows(Array.isArray(listRes.items) ? listRes.items : []);
      setTotal(typeof listRes.total === 'number' ? listRes.total : 0);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载双维漂移雷达失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [
    page,
    pageSize,
    filterState.search,
    filterState.departmentId,
    filterState.driftType,
    filterState.docType,
    showToast,
  ]);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  const updateFilter = (patch: {
    departmentId?: string;
    driftType?: DriftFilterType;
    docType?: string;
  }) => {
    setSearchParams((prev) => serializeDriftFilterParams({ ...patch, page: 1 }, prev));
  };

  const resetFilters = () => {
    setSearchInput('');
    setSearchParams((prev) =>
      serializeDriftFilterParams(
        {
          page: 1,
          search: '',
          departmentId: '',
          driftType: 'all',
          docType: '',
        },
        prev,
      ),
    );
  };

  const toggleExpand = (docId: number) => {
    setExpandedIds((prev) => ({ ...prev, [docId]: !prev[docId] }));
  };

  const handleManualPoll = async () => {
    setPolling(true);
    try {
      const res = await triggerAdminDriftPoll();
      showToast(`${res.message}（共监控 ${res.monitored_repos} 个活跃仓）`, 'success');
      await loadData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '触发远端变更巡检失败').message, 'error');
    } finally {
      setPolling(false);
    }
  };

  const handleIncrementalRefresh = async (row: DriftDocumentVO) => {
    setRefreshingDocId(row.document_id);
    try {
      const task = await triggerIncrementalRefresh(row.document_id, {});
      showToast(
        `已启动增量刷新任务 #${task.id}，仅重生成 ${task.target_slots?.length ?? 1} 个受影响非锁槽位（未受影响与锁定章节字节不变）`,
        'success',
      );
      navigate(
        buildStudioPath(isEmbedded, row.repo_id, {
          docId: row.document_id,
          subdirectory: row.subdirectory,
        }),
      );
    } catch (err: unknown) {
      showToast(formatApiError(err, '提交增量刷新任务失败').message, 'error');
    } finally {
      setRefreshingDocId(null);
    }
  };

  const hasActiveFilters = Boolean(
    filterState.search ||
      filterState.departmentId ||
      filterState.docType ||
      filterState.driftType !== 'all',
  );

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">章节级双维漂移雷达</h1>
          <p className="code-archdoc-page-subtitle">
            后台轻量 <code className="code-archdoc-mono">git ls-remote</code> 自动巡检已发布仓库，精准定位符号级 Code Drift（零行号误报）与模板槽位级 Spec Drift，支持单篇受影响非锁章节一键增量刷新。
          </p>
        </div>

        <div className="code-archdoc-page-actions">
          <OverviewMicroPills
            ariaLabel="漂移态势快捷筛选"
            items={[
              {
                key: 'total_drifted',
                label: '漂移',
                value: summary.total_drifted_docs,
                tone: summary.total_drifted_docs > 0 ? 'warning' : 'default',
                active: filterState.driftType === 'all',
                title: `含双维漂移 ${summary.both_drift_docs} 篇 · 点击显示全部漂移`,
                onClick: () => updateFilter({ driftType: 'all' }),
              },
              {
                key: 'code_drift',
                label: 'Code',
                value: summary.code_drift_docs,
                tone: 'info',
                active: filterState.driftType === 'code',
                title: '点击仅筛选 Code Drift（代码语义变更）',
                onClick: () =>
                  updateFilter({
                    driftType: filterState.driftType === 'code' ? 'all' : 'code',
                  }),
              },
              {
                key: 'spec_drift',
                label: 'Spec',
                value: summary.spec_drift_docs,
                tone: 'purple',
                active: filterState.driftType === 'spec',
                title: '点击仅筛选 Spec Drift（模板规范升级）',
                onClick: () =>
                  updateFilter({
                    driftType: filterState.driftType === 'spec' ? 'all' : 'spec',
                  }),
              },
              {
                key: 'monitored_repos',
                label: '巡检',
                value: `${summary.monitored_repos}仓`,
                tone: summary.unhealthy_repos > 0 ? 'warning' : 'success',
                title: `${
                  summary.unhealthy_repos > 0
                    ? `${summary.unhealthy_repos} 仓退避重试中 · `
                    : ''
                }每 ${summary.poll_interval_minutes}m · 最近 ${formatDateTime(summary.last_poll_at)}`,
              },
            ]}
          />

          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void loadData()}
            disabled={loading}
          >
            <RefreshCw size={15} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新看板</span>
          </button>

          {user?.is_module_admin && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => void handleManualPoll()}
              disabled={polling}
            >
              {polling ? <Loader2 size={15} className="code-archdoc-spin" /> : <Radar size={15} />}
              <span>{polling ? '巡检启动中...' : '立即触发全量巡检'}</span>
            </button>
          )}
        </div>
      </div>

      {/* URL 同步筛选条 */}
      <div className="code-card code-archdoc-drift-filter">
        <input
          type="search"
          className="code-input code-archdoc-drift-search"
          placeholder="搜索仓库名称、子目录或文档标题..."
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          aria-label="搜索漂移文档"
        />

        <select
          className="code-select code-archdoc-drift-select"
          value={filterState.departmentId}
          onChange={(e) => updateFilter({ departmentId: e.target.value })}
          aria-label="按部门筛选"
        >
          <option value="">全部部门</option>
          {departments.map((d) => (
            <option key={d.id} value={String(d.id)}>
              {d.name}
            </option>
          ))}
        </select>

        <select
          className="code-select code-archdoc-drift-select"
          value={filterState.driftType}
          onChange={(e) => updateFilter({ driftType: e.target.value as DriftFilterType })}
          aria-label="按漂移维度筛选"
        >
          <option value="all">全部漂移维度</option>
          <option value="code">仅 Code Drift</option>
          <option value="spec">仅 Spec Drift</option>
          <option value="both">Code + Spec 双漂移</option>
        </select>

        <select
          className="code-select code-archdoc-drift-select"
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

        {hasActiveFilters && (
          <button type="button" className="btn btn-secondary btn-small" onClick={resetFilters}>
            <RotateCcw size={14} />
            <span>重置筛选</span>
          </button>
        )}
      </div>

      {/* 漂移文档可展开表格 */}
      <div className="code-card code-archdoc-drift-table-card">
        {loading ? (
          <div className="code-archdoc-loading">
            <Loader2 size={18} className="code-archdoc-spin" />
            <span>正在加载章节级双维漂移明细...</span>
          </div>
        ) : rows.length === 0 ? (
          <EmptyState
            title="当前无待处置的文档漂移"
            description="所有已发布架构文档均与最新远端代码提交及最新 DocType 规范修订保持一致。"
          />
        ) : (
          <>
            <div className="code-archdoc-drift-table-wrap">
              <table className="code-archdoc-drift-table">
                <thead>
                  <tr>
                    <th aria-label="展开控制" />
                    <th>代码仓 / 子目录</th>
                    <th>文档类型</th>
                    <th>代码基线对比</th>
                    <th>模板修订对比</th>
                    <th>漂移维度与受影响槽位</th>
                    <th className="text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => {
                    const isExpanded = Boolean(expandedIds[row.document_id]);
                    const totalSlots =
                      (row.code_drift?.affected_slot_count ?? 0) +
                      (row.spec_drift?.affected_slot_count ?? 0);
                    const allLocked =
                      totalSlots > 0 &&
                      [
                        ...(row.code_drift?.affected_slots ?? []),
                        ...(row.spec_drift?.affected_slots ?? []),
                      ].every((s) => s.ownership === 'locked');

                    return (
                      <React.Fragment key={row.document_id}>
                        <tr className="code-archdoc-drift-row">
                          <td>
                            <button
                              type="button"
                              className="code-archdoc-drift-expand-btn"
                              onClick={() => toggleExpand(row.document_id)}
                              aria-expanded={isExpanded}
                              aria-label={isExpanded ? '收起受影响槽位明细' : '展开受影响槽位明细'}
                            >
                              {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
                            </button>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-repo-cell">
                              <span className="code-archdoc-drift-repo-name">{row.repo_name}</span>
                              <span className="code-archdoc-drift-subdir">
                                {row.subdirectory ? `sub/${row.subdirectory}` : '整仓根范围'}
                              </span>
                            </div>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-repo-cell">
                              <span>{row.doc_type_name}</span>
                              <code className="code-archdoc-mono">{row.doc_type_slug}</code>
                            </div>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-diff-pair">
                              <code className="code-archdoc-mono">
                                {shortSha(row.published_commit_sha)}
                              </code>
                              <ArrowRight size={13} />
                              <code className="code-archdoc-mono">
                                {shortSha(row.remote_head_sha)}
                              </code>
                            </div>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-diff-pair">
                              <code className="code-archdoc-mono">
                                Rev {row.bound_doc_type_revision_no}
                              </code>
                              <ArrowRight size={13} />
                              <code className="code-archdoc-mono">
                                Rev {row.latest_doc_type_revision_no}
                              </code>
                            </div>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-badges">
                              {row.code_drift?.has_drift && (
                                <StatusTag
                                  status="warning"
                                  text={`Code Drift (${row.code_drift.affected_slot_count || '仓级'})`}
                                  size="sm"
                                />
                              )}
                              {row.spec_drift?.has_drift && (
                                <StatusTag
                                  status="running"
                                  text={`Spec Drift (${row.spec_drift.affected_slot_count})`}
                                  size="sm"
                                />
                              )}
                              {allLocked && (
                                <StatusTag status="neutral" text="全为锁定章" size="sm" />
                              )}
                            </div>
                          </td>
                          <td>
                            <div className="code-archdoc-drift-actions">
                              <button
                                type="button"
                                className="btn btn-secondary btn-small"
                                onClick={() =>
                                  navigate(
                                    buildStudioPath(isEmbedded, row.repo_id, {
                                      docId: row.document_id,
                                      subdirectory: row.subdirectory,
                                    }),
                                  )
                                }
                              >
                                <BookOpen size={14} />
                                <span>审校</span>
                              </button>

                              {row.can_write && (
                                <button
                                  type="button"
                                  className="btn btn-primary btn-small"
                                  disabled={refreshingDocId === row.document_id || allLocked}
                                  title={
                                    allLocked
                                      ? '受影响章节均为 locked，请进入 Studio 人工核对或解锁'
                                      : '仅重生成受影响的非锁定槽位，其余章节字节不变'
                                  }
                                  onClick={() => void handleIncrementalRefresh(row)}
                                >
                                  {refreshingDocId === row.document_id ? (
                                    <Loader2 size={14} className="code-archdoc-spin" />
                                  ) : allLocked ? (
                                    <Lock size={14} />
                                  ) : (
                                    <Sparkles size={14} />
                                  )}
                                  <span>
                                    {refreshingDocId === row.document_id
                                      ? '提交中...'
                                      : '增量刷新'}
                                  </span>
                                </button>
                              )}
                            </div>
                          </td>
                        </tr>

                        {isExpanded && (
                          <tr className="code-archdoc-drift-detail-row">
                            <td colSpan={7}>
                              <div className="code-archdoc-drift-detail-grid">
                                {row.code_drift?.has_drift && (
                                  <DriftDimensionDetailCard
                                    title="Code Drift 受影响章节与变更符号"
                                    icon={<FileCode2 size={15} />}
                                    dim={row.code_drift}
                                  />
                                )}
                                {row.spec_drift?.has_drift && (
                                  <DriftDimensionDetailCard
                                    title="Spec Drift 模板/Prompt 受影响槽位"
                                    icon={<FileText size={15} />}
                                    dim={row.spec_drift}
                                  />
                                )}
                              </div>
                            </td>
                          </tr>
                        )}
                      </React.Fragment>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div className="code-archdoc-drift-pagination">
              <Pagination
                totalItems={total}
                defaultPageSize={DEFAULT_ARCHDOC_PAGE_SIZE}
                pageSizeOptions={[15, 25, 50, 100]}
              />
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default DriftRadarPage;
