import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  BookOpen,
  Eye,
  Loader2,
  Plus,
  RefreshCw,
  RotateCcw,
  Square,
} from 'lucide-react';
import {
  EmptyState,
  Modal,
  Pagination,
  StatusTag,
  useConfirm,
  usePagination,
  useToast,
  type StatusType,
} from '@code/common';
import { getRepoScopes, listDocTypes, listRepos } from '../api/repos';
import { cancelTask, listTasks } from '../api/tasks';
import GenerateTaskModal, { type GenerateTaskScope } from '../components/GenerateTaskModal';
import OverviewMicroPills from '../components/OverviewMicroPills';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  DocTypeSummaryVO,
  GenTaskDetailVO,
  RepoScopeCoverageVO,
  TaskKind,
  TaskKindFilter,
  TaskStatus,
  TaskStatusFilter,
  TaskSummaryVO,
} from '../types';
import { getDocTypeShortName } from '../utils/docTypeUtils';
import { formatApiError } from '../utils/errorMessages';
import {
  DEFAULT_ARCHDOC_PAGE_SIZE,
  buildStudioPath,
  formatDateTime,
  parseTaskFilterParams,
  serializeTaskFilterParams,
} from '../utils/urlQuery';
import './TaskCenterPage.css';

const TERMINAL_STATUSES = new Set<TaskStatus>(['ready', 'failed', 'canceled']);

function taskStatusMeta(status: TaskStatus): { label: string; tag: StatusType } {
  switch (status) {
    case 'pending':
      return { label: '排队中', tag: 'pending' };
    case 'extracting':
      return { label: 'Code IR 抽取', tag: 'running' };
    case 'generating':
      return { label: 'LLM 生成中', tag: 'running' };
    case 'verifying':
      return { label: 'Verifier 校验', tag: 'running' };
    case 'ready':
      return { label: '已完成', tag: 'success' };
    case 'failed':
      return { label: '执行失败', tag: 'failed' };
    case 'canceled':
      return { label: '已取消', tag: 'neutral' };
  }
}

function taskKindLabel(kind: TaskKind): string {
  switch (kind) {
    case 'generate':
      return '常规生成';
    case 'regenerate':
      return '全量重生成';
    case 'incremental_refresh':
      return '增量刷新';
    case 'batch':
      return '批量分层';
  }
}

function formatDuration(startedAt: string | null, finishedAt: string | null): string {
  if (!startedAt) return '-';
  const startMs = new Date(startedAt).getTime();
  if (Number.isNaN(startMs)) return '-';
  const endMs = finishedAt ? new Date(finishedAt).getTime() : Date.now();
  if (Number.isNaN(endMs) || endMs < startMs) return '-';
  const sec = Math.round((endMs - startMs) / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const rem = sec % 60;
  return `${min}m ${rem}s`;
}

export const TaskCenterPage: React.FC = () => {
  const navigate = useNavigate();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [searchParams, setSearchParams] = useSearchParams();
  const { page, pageSize } = usePagination({ defaultPageSize: DEFAULT_ARCHDOC_PAGE_SIZE });
  const filterState = useMemo(() => parseTaskFilterParams(searchParams), [searchParams]);

  const [repoOptions, setRepoOptions] = useState<RepoScopeCoverageVO[]>([]);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [rows, setRows] = useState<GenTaskDetailVO[]>([]);
  const [total, setTotal] = useState<number>(0);
  const [summary, setSummary] = useState<TaskSummaryVO>({
    total: 0,
    active: 0,
    ready: 0,
    failed: 0,
    canceled: 0,
  });
  const [loading, setLoading] = useState<boolean>(true);
  const [cancelingId, setCancelingId] = useState<number | null>(null);

  // 任务监控/发起弹窗状态
  const [taskModal, setTaskModal] = useState<{
    open: boolean;
    scope: GenerateTaskScope | null;
    initialTaskId: number | null;
    batchMode: boolean;
  }>({
    open: false,
    scope: null,
    initialTaskId: null,
    batchMode: false,
  });

  // 「新建生成任务」前置仓库/范围选择器弹窗状态
  const [launchPickerOpen, setLaunchPickerOpen] = useState<boolean>(false);
  const [launchRepoId, setLaunchRepoId] = useState<string>('');
  const [launchMode, setLaunchMode] = useState<'single' | 'batch'>('single');
  const [launchSubdir, setLaunchSubdir] = useState<string>('');
  const [launchScopes, setLaunchScopes] = useState<RepoScopeCoverageVO[]>([]);
  const [loadingLaunchScopes, setLoadingLaunchScopes] = useState<boolean>(false);

  // 加载仓库列表与 DocType 主数据
  useEffect(() => {
    let active = true;
    void Promise.all([
      listRepos({ page: 1, pageSize: 100, scopeType: 'repo' }),
      listDocTypes(true),
    ])
      .then(([repoRes, dtList]) => {
        if (!active) return;
        const items = Array.isArray(repoRes.items) ? repoRes.items : [];
        setRepoOptions(items);
        setDocTypes(dtList);
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载基础配置数据失败').message, 'error');
      });
    return () => {
      active = false;
    };
  }, [showToast]);

  const docTypeMap = useMemo(() => {
    const map = new Map<string, DocTypeSummaryVO>();
    for (const dt of docTypes) {
      map.set(dt.slug, dt);
    }
    return map;
  }, [docTypes]);

  const loadTasks = useCallback(
    async (silent = false) => {
      if (!silent) {
        setLoading(true);
      }
      try {
        const repoIdNum = filterState.repoId ? Number.parseInt(filterState.repoId, 10) : undefined;
        const res = await listTasks({
          page,
          pageSize,
          repoId: repoIdNum && repoIdNum > 0 ? repoIdNum : undefined,
          status: filterState.status || undefined,
          taskKind: filterState.taskKind || undefined,
          mine: filterState.mine,
          topLevelOnly: !filterState.includeChildren,
        });
        setRows(Array.isArray(res.items) ? res.items : []);
        setTotal(typeof res.total === 'number' ? res.total : 0);
        if (res.summary) {
          setSummary(res.summary);
        }
      } catch (err: unknown) {
        if (!silent) {
          showToast(formatApiError(err, '加载反推任务列表失败').message, 'error');
        }
      } finally {
        if (!silent) {
          setLoading(false);
        }
      }
    },
    [
      page,
      pageSize,
      filterState.repoId,
      filterState.status,
      filterState.taskKind,
      filterState.mine,
      filterState.includeChildren,
      showToast,
    ],
  );

  useEffect(() => {
    void loadTasks(false);
  }, [loadTasks]);

  const hasActiveTasks = useMemo(
    () => summary.active > 0 || rows.some((r) => !TERMINAL_STATUSES.has(r.status)),
    [summary.active, rows],
  );

  // 仅当存在排队或执行中任务时自动静默轮询
  useEffect(() => {
    if (!hasActiveTasks) return;
    const timer = window.setInterval(() => {
      void loadTasks(true);
    }, 4000);
    return () => window.clearInterval(timer);
  }, [hasActiveTasks, loadTasks]);

  // 合并任务列表与仓库列表中已知仓库名称用于筛选框
  const repoSelectOptions = useMemo(() => {
    const map = new Map<number, string>();
    for (const r of repoOptions) {
      map.set(r.repo_id, r.repo_name);
    }
    for (const t of rows) {
      if (t.repo_id && t.repo_name && !map.has(t.repo_id)) {
        map.set(t.repo_id, t.repo_name);
      }
    }
    return Array.from(map.entries()).map(([id, name]) => ({ id, name }));
  }, [repoOptions, rows]);

  const writableRepos = useMemo(
    () => repoOptions.filter((r) => r.can_write),
    [repoOptions],
  );

  // 新建生成弹窗选择仓库时，按需拉取其子目录作用域清单
  useEffect(() => {
    if (!launchPickerOpen || !launchRepoId) {
      setLaunchScopes([]);
      return;
    }
    const repoIdNum = Number.parseInt(launchRepoId, 10);
    if (!repoIdNum) return;
    let active = true;
    setLoadingLaunchScopes(true);
    void getRepoScopes(repoIdNum)
      .then((res) => {
        if (!active) return;
        setLaunchScopes(Array.isArray(res.scopes) ? res.scopes : []);
      })
      .catch(() => {
        if (active) setLaunchScopes([]);
      })
      .finally(() => {
        if (active) setLoadingLaunchScopes(false);
      });
    return () => {
      active = false;
    };
  }, [launchPickerOpen, launchRepoId]);

  const updateFilter = (patch: {
    repoId?: string;
    status?: TaskStatusFilter;
    taskKind?: TaskKindFilter;
    mine?: boolean;
    includeChildren?: boolean;
  }) => {
    setSearchParams((prev) => serializeTaskFilterParams({ ...patch, page: 1 }, prev));
  };

  const resetFilters = () => {
    setSearchParams((prev) =>
      serializeTaskFilterParams(
        {
          page: 1,
          repoId: '',
          status: '',
          taskKind: '',
          mine: false,
          includeChildren: false,
        },
        prev,
      ),
    );
  };

  const openTaskDetailModal = (task: GenTaskDetailVO) => {
    setTaskModal({
      open: true,
      scope: {
        repoId: task.repo_id,
        repoName: task.repo_name || `仓库 #${task.repo_id}`,
        subdirectory: task.subdirectory,
        branch: task.ref_name || 'master',
        canWrite: Boolean(task.can_write),
      },
      initialTaskId: task.id,
      batchMode: task.task_kind === 'batch',
    });
  };

  const openNewTaskPicker = () => {
    const defaultRepo =
      (filterState.repoId &&
        writableRepos.find((r) => String(r.repo_id) === filterState.repoId)) ||
      writableRepos[0];
    setLaunchRepoId(defaultRepo ? String(defaultRepo.repo_id) : '');
    setLaunchMode('single');
    setLaunchSubdir('');
    setLaunchPickerOpen(true);
  };

  const handleConfirmLaunchPicker = () => {
    const repoIdNum = Number.parseInt(launchRepoId, 10);
    const repo = writableRepos.find((r) => r.repo_id === repoIdNum);
    if (!repo) {
      showToast('请选择有写权限的目标代码仓库', 'warning');
      return;
    }
    const targetSubdir = launchMode === 'batch' ? '' : launchSubdir;
    setLaunchPickerOpen(false);
    setTaskModal({
      open: true,
      scope: {
        repoId: repo.repo_id,
        repoName: repo.repo_name,
        subdirectory: targetSubdir,
        branch: repo.branch || 'master',
        defaultBranch: repo.default_branch,
        isBranchOverridden: repo.is_branch_overridden,
        canSwitchBranch: repo.can_switch_branch ?? repo.can_write,
        canWrite: true,
      },
      initialTaskId: null,
      batchMode: launchMode === 'batch',
    });
  };

  const handleCancelRowTask = async (task: GenTaskDetailVO) => {
    const ok = await confirm({
      title: '确认取消生成任务',
      content: `即将中断任务 #${task.id}（${task.repo_name || `仓库 #${task.repo_id}`}），正在进行的 LLM 与 Git 子进程将被立即停止，是否继续？`,
      confirmText: '确认取消',
      cancelText: '继续运行',
      type: 'warning',
    });
    if (!ok) return;

    setCancelingId(task.id);
    try {
      await cancelTask(task.id);
      showToast(`任务 #${task.id} 已取消`, 'info');
      await loadTasks(false);
    } catch (err: unknown) {
      showToast(formatApiError(err, '取消任务失败').message, 'error');
    } finally {
      setCancelingId(null);
    }
  };

  const hasActiveFilters = Boolean(
    filterState.repoId ||
      filterState.status ||
      filterState.taskKind ||
      filterState.mine ||
      filterState.includeChildren,
  );

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">反推任务中心</h1>
          <p className="code-archdoc-page-subtitle">
            集中监控 AI 文档反推任务队列、批量分层编排进度、预算熔断建议与失败槽位独立重试。
          </p>
        </div>

        <div className="code-archdoc-page-actions">
          <OverviewMicroPills
            ariaLabel="任务状态快捷筛选"
            items={[
              {
                key: 'all',
                label: '全部',
                value: summary.total,
                active: filterState.status === '' || filterState.status === 'all',
                title: '查看全部反推生成任务',
                onClick: () => updateFilter({ status: '' }),
              },
              {
                key: 'active',
                label: '执行中',
                value: summary.active,
                tone: 'purple',
                active: filterState.status === 'active',
                title: '筛选执行中或排队中任务',
                onClick: () =>
                  updateFilter({ status: filterState.status === 'active' ? '' : 'active' }),
              },
              {
                key: 'ready',
                label: '已完成',
                value: summary.ready,
                tone: 'success',
                active: filterState.status === 'ready',
                title: '筛选已完成任务',
                onClick: () =>
                  updateFilter({ status: filterState.status === 'ready' ? '' : 'ready' }),
              },
              {
                key: 'failed',
                label: '失败',
                value: summary.failed,
                tone: summary.failed > 0 ? 'warning' : 'default',
                active: filterState.status === 'failed',
                title: '筛选失败待处理任务',
                onClick: () =>
                  updateFilter({ status: filterState.status === 'failed' ? '' : 'failed' }),
              },
            ]}
          />

          {hasActiveTasks && (
            <span className="code-archdoc-task-live-hint">
              <Loader2 size={14} className="code-archdoc-spin" />
              <span>存在活跃任务 · 自动刷新中</span>
            </span>
          )}

          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => void loadTasks(false)}
            disabled={loading}
          >
            <RefreshCw size={14} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-small"
            disabled={writableRepos.length === 0}
            title={
              writableRepos.length === 0
                ? '暂无可写代码仓权限，请联系仓库负责人授权'
                : '选择代码仓与范围发起新的设计文档生成任务'
            }
            onClick={openNewTaskPicker}
          >
            <Plus size={14} />
            <span>新建生成任务</span>
          </button>
        </div>
      </div>

      {/* 单行筛选工具栏 */}
      <div className="code-card code-archdoc-task-toolbar">
        <div className="code-archdoc-task-filters">
          <select
            className="code-select code-archdoc-task-select"
            value={filterState.repoId}
            onChange={(e) => updateFilter({ repoId: e.target.value })}
            aria-label="按代码仓筛选"
          >
            <option value="">全部代码仓</option>
            {repoSelectOptions.map((r) => (
              <option key={r.id} value={String(r.id)}>
                {r.name}
              </option>
            ))}
          </select>

          <select
            className="code-select code-archdoc-task-select"
            value={filterState.status}
            onChange={(e) => updateFilter({ status: e.target.value as TaskStatusFilter })}
            aria-label="按任务状态筛选"
          >
            <option value="">全部状态</option>
            <option value="active">执行中 / 排队</option>
            <option value="ready">已完成</option>
            <option value="failed">执行失败</option>
            <option value="canceled">已取消</option>
          </select>

          <select
            className="code-select code-archdoc-task-select"
            value={filterState.taskKind}
            onChange={(e) => updateFilter({ taskKind: e.target.value as TaskKindFilter })}
            aria-label="按任务类型筛选"
          >
            <option value="">全部任务类型</option>
            <option value="generate">常规生成</option>
            <option value="regenerate">全量重生成</option>
            <option value="incremental_refresh">增量刷新</option>
            <option value="batch">批量分层编排</option>
          </select>

          <label className="code-archdoc-task-toggle">
            <input
              type="checkbox"
              checked={filterState.mine}
              onChange={(e) => updateFilter({ mine: e.target.checked })}
            />
            <span>仅看我触发</span>
          </label>

          <label className="code-archdoc-task-toggle">
            <input
              type="checkbox"
              checked={filterState.includeChildren}
              onChange={(e) => updateFilter({ includeChildren: e.target.checked })}
            />
            <span>包含批量子任务</span>
          </label>

          {hasActiveFilters && (
            <button type="button" className="code-archdoc-ghost-btn" onClick={resetFilters}>
              <RotateCcw size={13} />
              <span>重置筛选</span>
            </button>
          )}
        </div>
      </div>

      {/* 任务执行表格 */}
      <div className="code-card code-archdoc-task-table-card">
        {loading ? (
          <div className="code-archdoc-loading">
            <Loader2 size={18} className="code-archdoc-spin" />
            <span>正在加载反推任务队列与执行记录...</span>
          </div>
        ) : rows.length === 0 ? (
          <EmptyState
            type={hasActiveFilters ? 'search' : 'data'}
            title={hasActiveFilters ? '未找到符合筛选条件的生成任务' : '暂无反推生成任务记录'}
            description={
              hasActiveFilters
                ? '请放宽任务状态或仓库筛选条件，或点击重置筛选'
                : '点击右上角「新建生成任务」或在仓库覆盖矩阵触发设计文档反推。'
            }
            action={
              hasActiveFilters ? (
                <button type="button" className="code-archdoc-ghost-btn" onClick={resetFilters}>
                  重置筛选
                </button>
              ) : undefined
            }
          />
        ) : (
          <>
            <div className="code-archdoc-task-table-wrap">
              <table className="code-table">
                <thead>
                  <tr>
                    <th>任务 ID & 类型</th>
                    <th>目标仓库与范围</th>
                    <th>文档类型</th>
                    <th>状态与进度</th>
                    <th>触发人与耗时</th>
                    <th className="code-archdoc-coverage-actions-col">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((task) => {
                    const sm = taskStatusMeta(task.status);
                    const isTerminal = TERMINAL_STATUSES.has(task.status);
                    const pct = Math.max(0, Math.min(100, task.progress_percent ?? 0));
                    const fillMod =
                      task.status === 'ready'
                        ? 'code-archdoc-task-progress-fill--ready'
                        : task.status === 'failed'
                          ? 'code-archdoc-task-progress-fill--failed'
                          : task.status === 'canceled'
                            ? 'code-archdoc-task-progress-fill--canceled'
                            : '';

                    const visibleTypes = (task.doc_types || []).slice(0, 2);
                    const overflowTypes = Math.max(0, (task.doc_types || []).length - 2);
                    const canOpenStudio =
                      Boolean(task.target_document_id) ||
                      (task.status === 'ready' && task.task_kind !== 'batch');

                    return (
                      <tr key={task.id}>
                        <td>
                          <div className="code-archdoc-task-id-cell">
                            <div className="code-archdoc-task-id-row">
                              <code className="code-archdoc-mono">#{task.id}</code>
                              <span className="code-badge code-badge--muted">
                                {taskKindLabel(task.task_kind)}
                              </span>
                            </div>
                            {task.parent_task_id && (
                              <span className="text-muted">
                                子任务 · 父 #{task.parent_task_id}
                              </span>
                            )}
                          </div>
                        </td>

                        <td>
                          <div className="code-archdoc-task-scope-cell">
                            <span className="code-archdoc-task-repo-name">
                              {task.repo_name || `仓库 #${task.repo_id}`}
                            </span>
                            <div className="code-archdoc-task-scope-meta">
                              <span className="code-badge code-badge--purple">
                                {task.subdirectory || '整仓 (Root)'}
                              </span>
                              <code className="code-archdoc-mono">{task.ref_name || 'master'}</code>
                            </div>
                          </div>
                        </td>

                        <td>
                          <div className="code-archdoc-task-doctypes">
                            {visibleTypes.map((slug) => {
                              const dt = docTypeMap.get(slug);
                              return (
                                <span
                                  key={slug}
                                  className="code-badge code-badge--primary"
                                  title={dt?.display_name || slug}
                                >
                                  {dt ? getDocTypeShortName(dt) : slug}
                                </span>
                              );
                            })}
                            {overflowTypes > 0 && (
                              <span className="code-badge code-badge--muted">
                                +{overflowTypes} 类
                              </span>
                            )}
                          </div>
                        </td>

                        <td>
                          <div className="code-archdoc-task-status-cell">
                            <div className="code-archdoc-task-progress-row">
                              <StatusTag status={sm.tag} text={sm.label} size="sm" />
                              <span className="code-archdoc-mono">{pct}%</span>
                            </div>

                            <div className="code-archdoc-task-progress-track" aria-hidden="true">
                              <div
                                className={`code-archdoc-task-progress-fill ${fillMod}`}
                                style={{ width: `${pct}%` }}
                              />
                            </div>

                            {task.batch_summary && (
                              <span className="code-archdoc-task-batch-snippet">
                                子任务 {task.batch_summary.completed_children}/
                                {task.batch_summary.total_children} 完成
                                {task.batch_summary.failed_children > 0
                                  ? ` · ${task.batch_summary.failed_children} 失败`
                                  : ''}
                              </span>
                            )}

                            {task.status === 'failed' && (task.error_message || task.error_code) && (
                              <span
                                className="code-archdoc-task-err-snippet"
                                title={task.error_message || task.error_code}
                              >
                                {task.error_code ? `[${task.error_code}] ` : ''}
                                {task.error_message || '执行失败'}
                              </span>
                            )}
                          </div>
                        </td>

                        <td>
                          <div className="code-archdoc-task-meta-cell">
                            <span className="code-archdoc-task-trigger-user">
                              {task.triggered_by_name || `用户 #${task.triggered_by}`}
                            </span>
                            <span className="text-muted">
                              {formatDateTime(task.created_at)}
                            </span>
                            <span className="text-secondary">
                              耗时 {formatDuration(task.started_at, task.finished_at)}
                              {task.estimated_tokens > 0
                                ? ` · ~${task.estimated_tokens.toLocaleString()} Tok`
                                : ''}
                            </span>
                          </div>
                        </td>

                        <td className="code-archdoc-coverage-actions-col">
                          <div className="code-archdoc-row-actions">
                            <button
                              type="button"
                              className="btn btn-secondary btn-small"
                              onClick={() => openTaskDetailModal(task)}
                              title="查看实时进度、槽位明细、预算建议或重试失败槽位"
                            >
                              <Eye size={13} />
                              <span>{isTerminal ? '详情' : '监控'}</span>
                            </button>

                            {!isTerminal && Boolean(task.can_write) && (
                              <button
                                type="button"
                                className="btn btn-text-danger btn-small"
                                disabled={cancelingId === task.id}
                                onClick={() => void handleCancelRowTask(task)}
                                title="中断当前任务并停止下游请求"
                              >
                                {cancelingId === task.id ? (
                                  <Loader2 size={13} className="code-archdoc-spin" />
                                ) : (
                                  <Square size={13} />
                                )}
                                <span>取消</span>
                              </button>
                            )}

                            {canOpenStudio && (
                              <button
                                type="button"
                                className="btn btn-primary btn-small"
                                onClick={() =>
                                  navigate(
                                    buildStudioPath(isEmbedded, task.repo_id, {
                                      docId: task.target_document_id ?? undefined,
                                      subdirectory: task.subdirectory,
                                    }),
                                  )
                                }
                                title="打开 Canvas Studio 审校产出文档"
                              >
                                <BookOpen size={13} />
                                <span>工作台</span>
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            {total > 0 && (
              <div className="code-archdoc-task-pagination">
                <Pagination
                  totalItems={total}
                  defaultPageSize={DEFAULT_ARCHDOC_PAGE_SIZE}
                  pageSizeOptions={[15, 25, 50, 100]}
                />
              </div>
            )}
          </>
        )}
      </div>

      {/* 复用任务进度监控 / 发起生成弹窗 */}
      <GenerateTaskModal
        open={taskModal.open}
        scope={taskModal.scope}
        docTypes={docTypes}
        initialTaskId={taskModal.initialTaskId}
        batchMode={taskModal.batchMode}
        onClose={() => setTaskModal((prev) => ({ ...prev, open: false }))}
        onTaskCompleted={() => void loadTasks(true)}
        onModulesRefreshed={() => void loadTasks(true)}
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
          setRepoOptions((prev) =>
            prev.map((item) =>
              item.repo_id === res.repo_id
                ? {
                    ...item,
                    branch: res.current_branch,
                    default_branch: res.default_branch,
                    is_branch_overridden: res.is_overridden,
                  }
                : item,
            ),
          );
        }}
        onOpenStudio={(rId, dId, sub) => {
          navigate(buildStudioPath(isEmbedded, rId, { docId: dId, subdirectory: sub }));
        }}
      />

      {/* 新建生成任务前置选择器弹窗 */}
      <Modal
        open={launchPickerOpen}
        onClose={() => setLaunchPickerOpen(false)}
        title="新建文档反推生成任务"
        subtitle="选择目标代码仓库与生成模式，下一步可配置文档类型与策略"
        width="md"
        destroyOnClose
        footer={
          <div className="code-archdoc-modal-footer">
            <div className="code-archdoc-modal-footer-right">
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setLaunchPickerOpen(false)}
              >
                取消
              </button>
              <button
                type="button"
                className="btn btn-primary"
                disabled={!launchRepoId}
                onClick={handleConfirmLaunchPicker}
              >
                下一步：选择文档与策略
              </button>
            </div>
          </div>
        }
      >
        <div className="code-archdoc-task-launch-form">
          <label className="code-archdoc-field">
            <span className="code-archdoc-field-label">目标代码仓库</span>
            <select
              className="code-select"
              value={launchRepoId}
              onChange={(e) => {
                setLaunchRepoId(e.target.value);
                setLaunchSubdir('');
              }}
            >
              {writableRepos.map((r) => (
                <option key={r.repo_id} value={String(r.repo_id)}>
                  {r.repo_name} ({r.branch || 'master'})
                </option>
              ))}
            </select>
          </label>

          <div className="code-archdoc-field">
            <span className="code-archdoc-field-label">生成模式</span>
            <div className="code-archdoc-task-launch-modes">
              <label
                className={`code-archdoc-task-launch-mode ${
                  launchMode === 'single' ? 'code-archdoc-task-launch-mode--active' : ''
                }`}
              >
                <input
                  type="radio"
                  name="archdoc-launch-mode"
                  checked={launchMode === 'single'}
                  onChange={() => setLaunchMode('single')}
                />
                <div className="code-archdoc-task-launch-mode-body">
                  <span className="code-archdoc-task-launch-mode-title">单范围生成</span>
                  <span className="code-archdoc-task-launch-mode-desc">
                    针对整仓或单个 Monorepo 子目录生成设计文档
                  </span>
                </div>
              </label>

              <label
                className={`code-archdoc-task-launch-mode ${
                  launchMode === 'batch' ? 'code-archdoc-task-launch-mode--active' : ''
                }`}
              >
                <input
                  type="radio"
                  name="archdoc-launch-mode"
                  checked={launchMode === 'batch'}
                  onChange={() => setLaunchMode('batch')}
                />
                <div className="code-archdoc-task-launch-mode-body">
                  <span className="code-archdoc-task-launch-mode-title">批量分层编排</span>
                  <span className="code-archdoc-task-launch-mode-desc">
                    并发生成多子模块文档，阶段二自动汇总整仓架构
                  </span>
                </div>
              </label>
            </div>
          </div>

          {launchMode === 'single' && (
            <label className="code-archdoc-field">
              <span className="code-archdoc-field-label">
                目标范围{loadingLaunchScopes ? '（正在加载子目录...）' : ''}
              </span>
              <select
                className="code-select"
                value={launchSubdir}
                disabled={loadingLaunchScopes}
                onChange={(e) => setLaunchSubdir(e.target.value)}
              >
                <option value="">整仓 (Root)</option>
                {launchScopes
                  .filter((sc) => sc.scope_type === 'subdir' && sc.scope_status !== 'missing')
                  .map((sc) => (
                    <option key={sc.subdirectory} value={sc.subdirectory}>
                      子目录：{sc.subdirectory}
                    </option>
                  ))}
              </select>
            </label>
          )}
        </div>
      </Modal>
    </div>
  );
};

export default TaskCenterPage;
