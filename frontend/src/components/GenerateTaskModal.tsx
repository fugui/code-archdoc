import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ExternalLink,
  Loader2,
  Play,
  RefreshCw,
  Square,
} from 'lucide-react';
import { Modal, StatusTag, useConfirm, useToast, type StatusType } from '@code/common';
import { cancelTask, getTaskDetail, retrySlotTask, triggerGenerateTask } from '../api/tasks';
import type {
  DocTypeSummaryVO,
  GenSlotTaskVO,
  GenTaskDetailVO,
  SlotTaskStatus,
  TaskStatus,
} from '../types';
import { formatApiError, formatTaskErrorCode } from '../utils/errorMessages';
import { shortSha } from '../utils/urlQuery';

export interface GenerateTaskScope {
  repoId: number;
  repoName: string;
  subdirectory: string;
  branch: string;
  canWrite: boolean;
}

export interface GenerateTaskModalProps {
  open: boolean;
  scope: GenerateTaskScope | null;
  docTypes: DocTypeSummaryVO[];
  initialDocTypeSlugs?: string[];
  initialMode?: 'generate' | 'regenerate';
  initialTaskId?: number | null;
  onClose: () => void;
  onTaskCompleted?: () => void;
  onOpenStudio?: (repoId: number, docId?: number, subdirectory?: string) => void;
}

const TERMINAL_STATUSES = new Set<TaskStatus>(['ready', 'failed', 'canceled']);

const STAGE_ORDER: Array<{ key: TaskStatus; label: string }> = [
  { key: 'pending', label: '1. 排队入队' },
  { key: 'extracting', label: '2. Code IR 抽取' },
  { key: 'generating', label: '3. LLM 槽位生成' },
  { key: 'verifying', label: '4. Verifier 校验' },
  { key: 'ready', label: '5. 生成就绪' },
];

function taskStatusMeta(status: TaskStatus): { label: string; tag: StatusType } {
  switch (status) {
    case 'pending':
      return { label: '排队中', tag: 'pending' };
    case 'extracting':
      return { label: 'Code IR 抽取中', tag: 'running' };
    case 'generating':
      return { label: 'LLM 生成中', tag: 'running' };
    case 'verifying':
      return { label: 'Verifier 校验中', tag: 'running' };
    case 'ready':
      return { label: '已完成', tag: 'success' };
    case 'failed':
      return { label: '执行失败', tag: 'failed' };
    case 'canceled':
      return { label: '已取消', tag: 'neutral' };
  }
}

function slotStatusMeta(status: SlotTaskStatus): { label: string; tag: StatusType } {
  switch (status) {
    case 'pending':
      return { label: '待执行', tag: 'pending' };
    case 'running':
      return { label: '生成中', tag: 'running' };
    case 'completed':
      return { label: '已完成', tag: 'success' };
    case 'failed':
      return { label: '失败', tag: 'failed' };
    case 'skipped_locked':
      return { label: '锁定豁免', tag: 'warning' };
    case 'canceled':
      return { label: '已取消', tag: 'neutral' };
  }
}

function stageIndex(status: TaskStatus): number {
  const idx = STAGE_ORDER.findIndex((s) => s.key === status);
  return idx >= 0 ? idx : 0;
}

export const GenerateTaskModal: React.FC<GenerateTaskModalProps> = ({
  open,
  scope,
  docTypes,
  initialDocTypeSlugs,
  initialMode = 'generate',
  initialTaskId = null,
  onClose,
  onTaskCompleted,
  onOpenStudio,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const activeDocTypes = useMemo(() => docTypes.filter((d) => d.is_active), [docTypes]);

  const [selectedSlugs, setSelectedSlugs] = useState<string[]>([]);
  const [mode, setMode] = useState<'generate' | 'regenerate'>(initialMode);
  const [submitting, setSubmitting] = useState<boolean>(false);
  const [canceling, setCanceling] = useState<boolean>(false);
  const [retryingSlotId, setRetryingSlotId] = useState<number | null>(null);
  const [taskDetail, setTaskDetail] = useState<GenTaskDetailVO | null>(null);
  const [pollError, setPollError] = useState<string>('');

  const notifiedReadyRef = useRef<number | null>(null);

  // 打开弹窗时初始化选中状态或加载既有任务
  useEffect(() => {
    if (!open) return;
    setPollError('');
    setMode(initialMode);
    if (initialTaskId && initialTaskId > 0) {
      setTaskDetail(null);
      void getTaskDetail(initialTaskId)
        .then((detail) => setTaskDetail(detail))
        .catch((err: unknown) => {
          setPollError(formatApiError(err, '加载任务状态失败').message);
        });
      return;
    }

    setTaskDetail(null);
    if (initialDocTypeSlugs && initialDocTypeSlugs.length > 0) {
      setSelectedSlugs(initialDocTypeSlugs);
    } else if (activeDocTypes.length > 0) {
      setSelectedSlugs([activeDocTypes[0].slug]);
    } else {
      setSelectedSlugs([]);
    }
  }, [open, initialTaskId, initialDocTypeSlugs, initialMode, activeDocTypes]);

  // 轮询任务详情直至终态
  const activeTaskId = taskDetail?.id ?? initialTaskId ?? null;
  const isTerminal = taskDetail ? TERMINAL_STATUSES.has(taskDetail.status) : false;

  const refreshTask = useCallback(
    async (taskId: number) => {
      try {
        const latest = await getTaskDetail(taskId);
        setTaskDetail(latest);
        setPollError('');
        if (latest.status === 'ready' && notifiedReadyRef.current !== latest.id) {
          notifiedReadyRef.current = latest.id;
          onTaskCompleted?.();
        }
      } catch (err: unknown) {
        setPollError(formatApiError(err, '轮询任务状态失败').message);
      }
    },
    [onTaskCompleted],
  );

  useEffect(() => {
    if (!open || !activeTaskId || isTerminal) return;
    const timer = window.setInterval(() => {
      void refreshTask(activeTaskId);
    }, 1500);
    return () => window.clearInterval(timer);
  }, [open, activeTaskId, isTerminal, refreshTask]);

  const toggleSlug = (slug: string) => {
    setSelectedSlugs((prev) =>
      prev.includes(slug) ? prev.filter((item) => item !== slug) : [...prev, slug],
    );
  };

  const handleSubmitGenerate = async () => {
    if (!scope) return;
    if (selectedSlugs.length === 0) {
      showToast('请至少选择 1 个文档类型', 'warning');
      return;
    }

    setSubmitting(true);
    setPollError('');
    try {
      const created = await triggerGenerateTask({
        repo_id: scope.repoId,
        subdirectory: scope.subdirectory,
        ref_name: scope.branch,
        doc_types: selectedSlugs,
        mode,
      });
      notifiedReadyRef.current = null;
      setTaskDetail(created);
      showToast(`已提交文档反推任务 #${created.id}`, 'success');
    } catch (err: unknown) {
      const formatted = formatApiError(err, '提交生成任务失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const handleCancelTask = async () => {
    if (!taskDetail) return;
    const ok = await confirm({
      title: '确认取消生成任务',
      content: `即将取消任务 #${taskDetail.id}，系统将立即中断 LLM 请求与底层 Git 进程，是否继续？`,
      confirmText: '确认取消',
      cancelText: '继续运行',
      type: 'warning',
    });
    if (!ok) return;

    setCanceling(true);
    try {
      await cancelTask(taskDetail.id);
      showToast(`任务 #${taskDetail.id} 已取消`, 'info');
      await refreshTask(taskDetail.id);
      onTaskCompleted?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '取消任务失败').message, 'error');
    } finally {
      setCanceling(false);
    }
  };

  const handleRetrySlot = async (slot: GenSlotTaskVO) => {
    if (!taskDetail) return;
    setRetryingSlotId(slot.id);
    try {
      await retrySlotTask(taskDetail.id, slot.id);
      showToast(`已发起槽位「${slot.slot_id}」独立重试`, 'success');
      notifiedReadyRef.current = null;
      await refreshTask(taskDetail.id);
    } catch (err: unknown) {
      showToast(formatApiError(err, '触发单槽位重试失败').message, 'error');
    } finally {
      setRetryingSlotId(null);
    }
  };

  const firstGeneratedDocId = useMemo(() => {
    if (!taskDetail?.slot_tasks) return undefined;
    const found = taskDetail.slot_tasks.find((s) => typeof s.document_id === 'number' && s.document_id > 0);
    return found?.document_id ?? undefined;
  }, [taskDetail]);

  const progressPct = Math.max(0, Math.min(100, taskDetail?.progress_percent ?? 0));
  const currentStageIdx = taskDetail ? stageIndex(taskDetail.status) : 0;
  const taskErrorDisplay = taskDetail?.status === 'failed'
    ? formatTaskErrorCode(taskDetail.error_code, taskDetail.error_message)
    : null;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={taskDetail ? `生成任务监控 · #${taskDetail.id}` : '触发代码设计文档反推'}
      subtitle={
        scope
          ? `${scope.repoName}${scope.subdirectory ? ` / ${scope.subdirectory}` : ' (整仓)'} · 分支 ${scope.branch}`
          : undefined
      }
      width="lg"
      destroyOnClose
      footer={
        <div className="code-archdoc-modal-footer">
          {taskDetail && !isTerminal && scope?.canWrite && (
            <button
              type="button"
              className="btn btn-danger btn-small"
              onClick={() => void handleCancelTask()}
              disabled={canceling}
            >
              <Square size={14} />
              <span>{canceling ? '取消中...' : '取消任务'}</span>
            </button>
          )}

          <div className="code-archdoc-modal-footer-right">
            <button type="button" className="btn btn-secondary" onClick={onClose}>
              {taskDetail && !isTerminal ? '后台运行并关闭' : '关闭'}
            </button>

            {!taskDetail && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => void handleSubmitGenerate()}
                disabled={submitting || selectedSlugs.length === 0 || !scope?.canWrite}
              >
                {submitting ? <Loader2 size={15} className="code-archdoc-spin" /> : <Play size={15} />}
                <span>{submitting ? '提交中...' : `开始生成 (${selectedSlugs.length})`}</span>
              </button>
            )}

            {taskDetail && taskDetail.status === 'ready' && scope && onOpenStudio && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  onClose();
                  onOpenStudio(scope.repoId, firstGeneratedDocId, scope.subdirectory);
                }}
              >
                <ExternalLink size={15} />
                <span>进入工作台审校</span>
              </button>
            )}
          </div>
        </div>
      }
    >
      <div className="code-archdoc-task-modal">
        {scope && (
          <div className="code-archdoc-task-scope">
            <div>
              <span className="code-archdoc-task-scope-label">代码仓</span>
              <span className="code-archdoc-task-scope-val">{scope.repoName}</span>
            </div>
            <div>
              <span className="code-archdoc-task-scope-label">范围</span>
              <span className="code-archdoc-mono">{scope.subdirectory || '整仓 (Root)'}</span>
            </div>
            <div>
              <span className="code-archdoc-task-scope-label">分支</span>
              <span className="code-archdoc-mono">{scope.branch}</span>
            </div>
            {taskDetail?.commit_sha && (
              <div>
                <span className="code-archdoc-task-scope-label">Commit</span>
                <span className="code-archdoc-mono">{shortSha(taskDetail.commit_sha)}</span>
              </div>
            )}
          </div>
        )}

        {/* 配置态：选择 1~N DocType */}
        {!taskDetail && !initialTaskId && (
          <>
            <div className="code-archdoc-task-mode-row">
              <span className="text-secondary">生成策略：</span>
              <label className="code-archdoc-radio-label">
                <input
                  type="radio"
                  name="archdoc-gen-mode"
                  checked={mode === 'generate'}
                  onChange={() => setMode('generate')}
                />
                <span>常规生成（保留人工锁定章节）</span>
              </label>
              <label className="code-archdoc-radio-label">
                <input
                  type="radio"
                  name="archdoc-gen-mode"
                  checked={mode === 'regenerate'}
                  onChange={() => setMode('regenerate')}
                />
                <span>重新生成（基于最新提交全量刷新非锁槽位）</span>
              </label>
            </div>

            <div className="code-archdoc-doctype-grid">
              {activeDocTypes.map((dt) => {
                const checked = selectedSlugs.includes(dt.slug);
                return (
                  <label
                    key={dt.slug}
                    className={`code-archdoc-doctype-option ${checked ? 'code-archdoc-doctype-option--checked' : ''}`}
                  >
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => toggleSlug(dt.slug)}
                    />
                    <div className="code-archdoc-doctype-option-main">
                      <span className="code-archdoc-doctype-option-title">{dt.display_name}</span>
                      <span className="code-archdoc-doctype-option-desc">{dt.description}</span>
                      <div className="code-archdoc-doctype-option-meta">
                        <span className="code-badge code-badge--muted">{dt.slug}</span>
                        <span className="code-badge code-badge--primary">{dt.model_tier}</span>
                        <span className="code-badge code-badge--muted">{dt.slot_count} 个槽位</span>
                      </div>
                    </div>
                  </label>
                );
              })}
            </div>
          </>
        )}

        {/* 监控态：实时进度、阶段、预算熔断与槽位子任务 */}
        {taskDetail && (
          <>
            <div className="code-archdoc-progress-block">
              <div className="code-archdoc-progress-header">
                <div className="flex-center gap-sm">
                  <StatusTag
                    status={taskStatusMeta(taskDetail.status).tag}
                    text={taskStatusMeta(taskDetail.status).label}
                    size="sm"
                  />
                  {taskDetail.estimated_tokens > 0 && (
                    <span className="text-secondary">
                      预估消耗 {taskDetail.estimated_tokens.toLocaleString()} Tokens
                    </span>
                  )}
                </div>
                <span className="code-archdoc-mono">{progressPct}%</span>
              </div>

              <div className="code-archdoc-progress-track">
                <div
                  className={`code-archdoc-progress-fill ${
                    taskDetail.status === 'failed'
                      ? 'code-archdoc-progress-fill--failed'
                      : taskDetail.status === 'ready'
                        ? 'code-archdoc-progress-fill--ready'
                        : ''
                  }`}
                  style={{ width: `${progressPct}%` }}
                />
              </div>

              <div className="code-archdoc-stage-steps">
                {STAGE_ORDER.map((step, idx) => {
                  const isDone =
                    taskDetail.status === 'ready' ||
                    (taskDetail.status !== 'failed' && taskDetail.status !== 'canceled' && idx < currentStageIdx);
                  const isActive = taskDetail.status === step.key && taskDetail.status !== 'ready';
                  return (
                    <div
                      key={step.key}
                      className={`code-archdoc-stage-step ${
                        isDone
                          ? 'code-archdoc-stage-step--done'
                          : isActive
                            ? 'code-archdoc-stage-step--active'
                            : ''
                      }`}
                    >
                      {step.label}
                    </div>
                  );
                })}
              </div>
            </div>

            {pollError && (
              <div className="code-archdoc-alert code-archdoc-alert--warning">
                <AlertTriangle size={16} />
                <div className="code-archdoc-alert-body">{pollError}</div>
              </div>
            )}

            {taskErrorDisplay && (
              <div className="code-archdoc-alert code-archdoc-alert--danger">
                <AlertTriangle size={16} />
                <div className="code-archdoc-alert-body">
                  <div className="code-archdoc-alert-title">{taskErrorDisplay.title}</div>
                  <div>{taskErrorDisplay.message}</div>
                </div>
              </div>
            )}

            {/* 预算熔断结构化建议（SC-09） */}
            {taskDetail.budget_advice && (
              <div className="code-archdoc-budget-box">
                <div className="code-archdoc-alert-title">预算熔断分析与优化建议</div>
                <div className="code-archdoc-budget-metrics">
                  <span>
                    Token：{taskDetail.budget_advice.estimated_tokens.toLocaleString()} /{' '}
                    {taskDetail.budget_advice.max_tokens_per_task.toLocaleString()}
                  </span>
                  <span>
                    有效文件：{taskDetail.budget_advice.effective_files} /{' '}
                    {taskDetail.budget_advice.max_files_per_task}
                  </span>
                </div>
                {taskDetail.budget_advice.top_directories?.length > 0 && (
                  <div>
                    <div className="text-secondary">Top 消耗目录：</div>
                    <ul className="code-archdoc-budget-list">
                      {taskDetail.budget_advice.top_directories.map((dir) => (
                        <li key={dir.path}>
                          <code className="code-archdoc-mono">{dir.path}</code> — 约{' '}
                          {dir.estimated_tokens.toLocaleString()} Tokens（{dir.file_count} 文件）
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {taskDetail.budget_advice.suggestions?.length > 0 && (
                  <ul className="code-archdoc-budget-list">
                    {taskDetail.budget_advice.suggestions.map((tip) => (
                      <li key={tip}>{tip}</li>
                    ))}
                  </ul>
                )}
              </div>
            )}

            {/* 槽位子任务列表与单槽位独立重试 */}
            {taskDetail.slot_tasks && taskDetail.slot_tasks.length > 0 && (
              <div className="code-archdoc-slot-table-wrap">
                <table className="code-table">
                  <thead>
                    <tr>
                      <th>文档类型</th>
                      <th>章节槽位</th>
                      <th>档位</th>
                      <th>状态</th>
                      <th>重试次数</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {taskDetail.slot_tasks.map((slot) => {
                      const sm = slotStatusMeta(slot.status);
                      const canRetry = slot.status === 'failed' && Boolean(scope?.canWrite);
                      return (
                        <tr key={slot.id}>
                          <td>
                            <code className="code-archdoc-mono">{slot.doc_type_slug}</code>
                          </td>
                          <td>
                            <div>{slot.slot_id}</div>
                            {slot.error_message && (
                              <div className="text-muted">{slot.error_message}</div>
                            )}
                          </td>
                          <td>
                            <span className="code-badge code-badge--muted">{slot.model_tier}</span>
                          </td>
                          <td>
                            <StatusTag status={sm.tag} text={sm.label} size="sm" />
                          </td>
                          <td>{slot.attempt_count}</td>
                          <td>
                            {canRetry ? (
                              <button
                                type="button"
                                className="btn btn-outline btn-small"
                                disabled={retryingSlotId === slot.id}
                                onClick={() => void handleRetrySlot(slot)}
                              >
                                <RefreshCw
                                  size={13}
                                  className={retryingSlotId === slot.id ? 'code-archdoc-spin' : ''}
                                />
                                <span>重试槽位</span>
                              </button>
                            ) : (
                              <span className="text-muted">-</span>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </Modal>
  );
};

export default GenerateTaskModal;
