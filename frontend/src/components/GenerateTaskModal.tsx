import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ArrowLeft,
  BookOpen,
  Cpu,
  ExternalLink,
  FolderTree,
  Loader2,
  Play,
  RefreshCw,
  RotateCcw,
  Square,
} from 'lucide-react';
import { Modal, StatusTag, useConfirm, useToast, type StatusType } from '@code/common';
import { getEffectiveRefDocs } from '../api/refDocs';
import { listRepoModules, refreshRepoModules } from '../api/repos';
import {
  cancelTask,
  getTaskDetail,
  listEngines,
  retrySlotTask,
  retryTask,
  triggerBatchGenerateTask,
  triggerGenerateTask,
} from '../api/tasks';
import type {
  DocTypeSummaryVO,
  EffectiveRefDocVO,
  EngineInfoVO,
  GenEngine,
  GenSlotTaskVO,
  GenTaskDetailVO,
  RepoModuleVO,
  SlotTaskStatus,
  SwitchRepoBranchVO,
  TaskStatus,
} from '../types';
import { formatApiError, formatTaskErrorCode } from '../utils/errorMessages';
import { isBatchTaskKind } from '../utils/moduleUtils';
import {
  buildExcludedRefDocIds,
  computeIncludedRefDocStats,
  formatRefDocScopeLabel,
  formatRefDocTokenBadge,
  groupEffectiveRefDocsByScope,
} from '../utils/refDocUtils';
import {
  PREFERRED_ENGINE_STORAGE_KEY,
  formatEngineLabel,
  getNonTerminalTaskIds,
  getSubmitButtonLabel,
  getTaskRetryActionMeta,
  normalizeSubmittedTasks,
  resolvePreferredEngine,
  summarizeSplitTasks,
} from '../utils/taskSubmitUtils';
import { shortSha } from '../utils/urlQuery';
import RepoBranchSelector from './RepoBranchSelector';

export interface GenerateTaskScope {
  repoId: number;
  repoName: string;
  subdirectory: string;
  archElementId?: number;
  branch: string;
  defaultBranch?: string;
  isBranchOverridden?: boolean;
  canSwitchBranch?: boolean;
  canWrite: boolean;
}

export interface GenerateTaskModalProps {
  open: boolean;
  scope: GenerateTaskScope | null;
  docTypes: DocTypeSummaryVO[];
  initialDocTypeSlugs?: string[];
  initialMode?: 'generate' | 'regenerate';
  initialTaskId?: number | null;
  batchMode?: boolean;
  initialSelectedModules?: string[];
  onClose: () => void;
  onTaskCompleted?: () => void;
  onModulesRefreshed?: () => void;
  onBranchChanged?: (res: SwitchRepoBranchVO) => void;
  onOpenStudio?: (repoId: number, docId?: number, subdirectory?: string) => void;
  onOpenTaskCenter?: () => void;
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

const DEFAULT_NATIVE_ENGINE: EngineInfoVO = {
  name: 'native',
  display_name: 'Native 内置引擎',
  available: true,
  default: true,
};

function safeReadSavedEngine(): string | null {
  try {
    return window.localStorage.getItem(PREFERRED_ENGINE_STORAGE_KEY);
  } catch {
    return null;
  }
}

function safeWriteSavedEngine(engine: GenEngine): void {
  try {
    window.localStorage.setItem(PREFERRED_ENGINE_STORAGE_KEY, engine);
  } catch {
    // 忽略受限环境下的 localStorage 写入异常
  }
}

export const GenerateTaskModal: React.FC<GenerateTaskModalProps> = ({
  open,
  scope,
  docTypes,
  initialDocTypeSlugs,
  initialMode = 'generate',
  initialTaskId = null,
  batchMode = false,
  initialSelectedModules,
  onClose,
  onTaskCompleted,
  onModulesRefreshed,
  onBranchChanged,
  onOpenStudio,
  onOpenTaskCenter,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const activeDocTypes = useMemo(() => docTypes.filter((d) => d.is_active), [docTypes]);
  const docTypeDisplayMap = useMemo(
    () => new Map(docTypes.map((dt) => [dt.slug, dt.display_name])),
    [docTypes],
  );

  const [selectedSlugs, setSelectedSlugs] = useState<string[]>([]);
  const [mode, setMode] = useState<'generate' | 'regenerate'>(initialMode);
  const [engines, setEngines] = useState<EngineInfoVO[]>([DEFAULT_NATIVE_ENGINE]);
  const [selectedEngine, setSelectedEngine] = useState<GenEngine>('native');
  const [submitting, setSubmitting] = useState<boolean>(false);
  const [canceling, setCanceling] = useState<boolean>(false);
  const [retryingSlotId, setRetryingSlotId] = useState<number | null>(null);
  const [retryingTaskId, setRetryingTaskId] = useState<number | null>(null);
  const [taskDetail, setTaskDetail] = useState<GenTaskDetailVO | null>(null);
  const [splitTasks, setSplitTasks] = useState<GenTaskDetailVO[]>([]);
  const [drilldownTaskId, setDrilldownTaskId] = useState<number | null>(null);
  const [pollError, setPollError] = useState<string>('');

  // 单仓活动分支状态（允许在配置态内直接切换，切换后立即更新后续提交入参）
  const [activeBranch, setActiveBranch] = useState<string>(scope?.branch || 'master');
  const [activeDefaultBranch, setActiveDefaultBranch] = useState<string | undefined>(scope?.defaultBranch);
  const [activeIsOverridden, setActiveIsOverridden] = useState<boolean>(Boolean(scope?.isBranchOverridden));
  const [branchRescanHint, setBranchRescanHint] = useState<boolean>(false);

  // 批量模式状态（F-31）
  const [repoModules, setRepoModules] = useState<RepoModuleVO[]>([]);
  const [loadingModules, setLoadingModules] = useState<boolean>(false);
  const [scanningModules, setScanningModules] = useState<boolean>(false);
  const [allModules, setAllModules] = useState<boolean>(true);
  const [selectedModules, setSelectedModules] = useState<string[]>([]);
  const [includeRepoScope, setIncludeRepoScope] = useState<boolean>(true);

  // ADR-10：生效架构参考文档预览与当次勾选排除
  const [effectiveRefDocs, setEffectiveRefDocs] = useState<EffectiveRefDocVO[]>([]);
  const [loadingRefDocs, setLoadingRefDocs] = useState<boolean>(false);
  const [checkedRefDocIds, setCheckedRefDocIds] = useState<Set<number>>(new Set());
  const [refDocsRollup, setRefDocsRollup] = useState<boolean>(false);

  const notifiedReadyRef = useRef<number | null>(null);
  const notifiedReadyIdsRef = useRef<Set<number>>(new Set());

  const scopeRepoId = scope?.repoId ?? 0;
  const scopeBranch = scope?.branch || 'master';
  const scopeDefaultBranch = scope?.defaultBranch;
  const scopeIsBranchOverridden = Boolean(scope?.isBranchOverridden);

  useEffect(() => {
    if (!open || !scopeRepoId) return;
    setActiveBranch(scopeBranch);
    setActiveDefaultBranch(scopeDefaultBranch);
    setActiveIsOverridden(scopeIsBranchOverridden);
  }, [open, scopeRepoId, scopeBranch, scopeDefaultBranch, scopeIsBranchOverridden]);

  useEffect(() => {
    if (!open) {
      setBranchRescanHint(false);
    }
  }, [open, scopeRepoId]);

  // 批量模式打开时加载活跃子模块清单（支持预选特定目录）
  useEffect(() => {
    if (!open || !batchMode || !scopeRepoId || initialTaskId) return;
    let active = true;
    const presetList = initialSelectedModules ? [...initialSelectedModules] : [];
    const hasPreset = presetList.length > 0;
    setLoadingModules(true);
    setAllModules(!hasPreset);
    setIncludeRepoScope(true);
    setSelectedModules(hasPreset ? presetList : []);
    void listRepoModules(scopeRepoId)
      .then((res) => {
        if (!active) return;
        const actives = (res.modules || []).filter((m) => m.status === 'active');
        setRepoModules(actives);
        if (hasPreset) {
          const activeSet = new Set(actives.map((m) => m.subdirectory));
          setSelectedModules(
            activeSet.size > 0 ? presetList.filter((sub) => activeSet.has(sub)) : presetList,
          );
        } else {
          setSelectedModules(actives.map((m) => m.subdirectory));
        }
      })
      .catch(() => {
        if (active) setRepoModules([]);
      })
      .finally(() => {
        if (active) setLoadingModules(false);
      });
    return () => {
      active = false;
    };
  }, [open, batchMode, scopeRepoId, initialTaskId, initialSelectedModules]);

  // 打开弹窗时初始化选中状态、双引擎探测或加载既有任务
  useEffect(() => {
    if (!open) return;
    setPollError('');
    setMode(initialMode);
    setSplitTasks([]);
    setDrilldownTaskId(null);
    notifiedReadyIdsRef.current.clear();
    notifiedReadyRef.current = null;

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

    let active = true;
    void listEngines()
      .then((res) => {
        if (!active) return;
        const list = Array.isArray(res.engines) && res.engines.length > 0 ? res.engines : [DEFAULT_NATIVE_ENGINE];
        setEngines(list);
        setSelectedEngine(resolvePreferredEngine(list, res.default_engine || 'native', safeReadSavedEngine()));
      })
      .catch(() => {
        if (!active) return;
        setEngines([DEFAULT_NATIVE_ENGINE]);
        setSelectedEngine('native');
      });
    return () => {
      active = false;
    };
  }, [open, initialTaskId, initialDocTypeSlugs, initialMode, activeDocTypes]);

  const scopeSubdir = scope?.subdirectory ?? '';
  const scopeArchElementId = scope?.archElementId;
  const singleDocTypeFilter = selectedSlugs.length === 1 ? selectedSlugs[0] : undefined;

  useEffect(() => {
    if (!open || initialTaskId || !scopeRepoId) {
      return;
    }
    let active = true;
    setLoadingRefDocs(true);
    void getEffectiveRefDocs({
      arch_element_id: scopeArchElementId,
      repo_id: scopeRepoId,
      subdirectory: scopeSubdir,
      doc_type_slug: singleDocTypeFilter,
    })
      .then((res) => {
        if (!active) return;
        const list = res.items ?? [];
        setEffectiveRefDocs(list);
        setRefDocsRollup(Boolean(res.is_whole_repo_rollup));
        setCheckedRefDocIds(new Set(list.map((d) => d.doc_id)));
      })
      .catch(() => {
        if (!active) return;
        setEffectiveRefDocs([]);
        setRefDocsRollup(false);
        setCheckedRefDocIds(new Set());
      })
      .finally(() => {
        if (active) setLoadingRefDocs(false);
      });
    return () => {
      active = false;
    };
  }, [open, initialTaskId, scopeRepoId, scopeSubdir, scopeArchElementId, singleDocTypeFilter]);

  // 轮询任务详情（支持单任务与多文档拆单增量局部轮询）
  const isSplitMonitor = splitTasks.length > 1 && drilldownTaskId === null;
  const splitSummary = useMemo(() => summarizeSplitTasks(splitTasks), [splitTasks]);
  const activeTaskId = taskDetail?.id ?? drilldownTaskId ?? initialTaskId ?? null;
  const isTerminal = taskDetail ? TERMINAL_STATUSES.has(taskDetail.status) : false;

  const refreshTask = useCallback(
    async (taskId: number) => {
      try {
        const latest = await getTaskDetail(taskId);
        setTaskDetail(latest);
        setSplitTasks((prev) =>
          prev.length > 0 ? prev.map((t) => (t.id === latest.id ? latest : t)) : prev,
        );
        setPollError('');
        if (latest.status === 'ready' && notifiedReadyRef.current !== latest.id) {
          notifiedReadyRef.current = latest.id;
          notifiedReadyIdsRef.current.add(latest.id);
          onTaskCompleted?.();
        }
      } catch (err: unknown) {
        setPollError(formatApiError(err, '轮询任务状态失败').message);
      }
    },
    [onTaskCompleted],
  );

  useEffect(() => {
    if (!open || isSplitMonitor || !activeTaskId || isTerminal) return;
    const timer = window.setInterval(() => {
      void refreshTask(activeTaskId);
    }, 1500);
    return () => window.clearInterval(timer);
  }, [open, isSplitMonitor, activeTaskId, isTerminal, refreshTask]);

  useEffect(() => {
    if (!open || !isSplitMonitor || splitSummary.activeCount === 0) return;
    const timer = window.setInterval(() => {
      const pendingIds = getNonTerminalTaskIds(splitTasks);
      if (pendingIds.length === 0) return;
      void Promise.allSettled(pendingIds.map((id) => getTaskDetail(id))).then((results) => {
        const updatedMap = new Map<number, GenTaskDetailVO>();
        results.forEach((result) => {
          if (result.status === 'fulfilled' && result.value) {
            updatedMap.set(result.value.id, result.value);
            if (result.value.status === 'ready' && !notifiedReadyIdsRef.current.has(result.value.id)) {
              notifiedReadyIdsRef.current.add(result.value.id);
              onTaskCompleted?.();
            }
          }
        });
        if (updatedMap.size > 0) {
          setSplitTasks((prev) => prev.map((item) => updatedMap.get(item.id) ?? item));
          setPollError('');
        }
      });
    }, 1500);
    return () => window.clearInterval(timer);
  }, [open, isSplitMonitor, splitSummary.activeCount, splitTasks, onTaskCompleted]);

  const toggleSlug = (slug: string) => {
    setSelectedSlugs((prev) =>
      prev.includes(slug) ? prev.filter((item) => item !== slug) : [...prev, slug],
    );
  };

  const toggleModuleSubdir = (subdir: string) => {
    setSelectedModules((prev) =>
      prev.includes(subdir) ? prev.filter((item) => item !== subdir) : [...prev, subdir],
    );
  };

  const handleScanModulesInBatch = async () => {
    if (!scope) return;
    setScanningModules(true);
    try {
      const res = await refreshRepoModules(scope.repoId);
      const actives = (res.modules || []).filter((m) => m.status === 'active');
      setRepoModules(actives);
      setSelectedModules(actives.map((m) => m.subdirectory));
      if (actives.length > 0) {
        setAllModules(false);
        showToast(
          `已完成静态扫描（${shortSha(res.commit_sha)}），发现 ${actives.length} 个一级目录模块，可自由勾选生成`,
          'success',
        );
      } else {
        showToast(
          `已完成静态扫描（${shortSha(res.commit_sha)}），未发现独立一级业务子目录，可直接勾选整仓总览生成`,
          'info',
        );
      }
      setBranchRescanHint(false);
      onModulesRefreshed?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '扫描仓库一级目录失败').message, 'error');
    } finally {
      setScanningModules(false);
    }
  };

  const handleSelectEngine = (engine: GenEngine) => {
    setSelectedEngine(engine);
    safeWriteSavedEngine(engine);
  };

  const handleSubmitGenerate = async () => {
    if (!scope) return;
    if (selectedSlugs.length === 0) {
      showToast('请至少选择 1 个文档类型', 'warning');
      return;
    }

    if (batchMode && !allModules && selectedModules.length === 0 && !includeRepoScope) {
      showToast('请至少选择 1 个子模块或勾选整仓总览文档', 'warning');
      return;
    }

    setSubmitting(true);
    setPollError('');
    try {
      const excludeRefDocIds = buildExcludedRefDocIds(effectiveRefDocs, checkedRefDocIds);
      if (batchMode) {
        const created = await triggerBatchGenerateTask({
          repo_id: scope.repoId,
          ref_name: activeBranch,
          all_modules: allModules,
          subdirectories: allModules ? undefined : selectedModules,
          include_repo_scope: includeRepoScope,
          doc_types: selectedSlugs,
          mode,
          engine: selectedEngine,
          exclude_ref_doc_ids: excludeRefDocIds.length > 0 ? excludeRefDocIds : undefined,
        });
        notifiedReadyRef.current = null;
        setSplitTasks([]);
        setDrilldownTaskId(null);
        setTaskDetail(created);
        showToast(`已提交批量文档生成编排任务 #${created.id}`, 'success');
      } else {
        const submitResp = await triggerGenerateTask({
          repo_id: scope.repoId,
          subdirectory: scope.subdirectory,
          ref_name: activeBranch,
          doc_types: selectedSlugs,
          mode,
          engine: selectedEngine,
          arch_element_id: scope.archElementId,
          exclude_ref_doc_ids: excludeRefDocIds.length > 0 ? excludeRefDocIds : undefined,
        });
        const tasks = normalizeSubmittedTasks(submitResp);
        notifiedReadyRef.current = null;
        notifiedReadyIdsRef.current.clear();
        if (tasks.length > 1) {
          setSplitTasks(tasks);
          setDrilldownTaskId(null);
          setTaskDetail(null);
          showToast(`已创建 ${tasks.length} 个独立文档生成任务`, 'success');
        } else {
          setSplitTasks([]);
          setDrilldownTaskId(null);
          setTaskDetail(tasks[0]);
          showToast(`已提交文档反推任务 #${tasks[0].id}`, 'success');
        }
      }
    } catch (err: unknown) {
      const formatted = formatApiError(err, '提交生成任务失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const handleDrilldownSplitTask = (taskId: number) => {
    const cached = splitTasks.find((item) => item.id === taskId) ?? null;
    setDrilldownTaskId(taskId);
    setTaskDetail(cached);
    void refreshTask(taskId);
  };

  const handleCancelSplitRow = async (taskId: number) => {
    const ok = await confirm({
      title: '确认取消生成任务',
      content: `即将取消子文档任务 #${taskId}，系统将立即中断当前模型调用，是否继续？`,
      confirmText: '确认取消',
      cancelText: '继续运行',
      type: 'warning',
    });
    if (!ok) return;
    try {
      await cancelTask(taskId);
      showToast(`任务 #${taskId} 已取消`, 'info');
      const latest = await getTaskDetail(taskId);
      setSplitTasks((prev) => prev.map((item) => (item.id === taskId ? latest : item)));
      onTaskCompleted?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '取消任务失败').message, 'error');
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

  const handleRetryTaskAction = async (
    target: GenTaskDetailVO,
    options?: { replaceInSplitList?: boolean },
  ) => {
    const meta = getTaskRetryActionMeta(target, scope?.canWrite);
    if (!meta.canRetry) return;
    const ok = await confirm({
      title: meta.confirmTitle,
      content: meta.confirmContent,
      confirmText: meta.confirmText,
      cancelText: '取消',
      type: 'info',
    });
    if (!ok) return;

    setRetryingTaskId(target.id);
    setPollError('');
    try {
      const created = await retryTask(target.id, meta.payload);
      const newTask = normalizeSubmittedTasks(created)[0] ?? created;
      showToast(
        `已提交${meta.isFailureRetry ? '重试' : '重新生成'}任务 #${newTask.id}`,
        'success',
      );
      notifiedReadyRef.current = null;
      notifiedReadyIdsRef.current.delete(newTask.id);
      if (options?.replaceInSplitList) {
        setSplitTasks((prev) => prev.map((item) => (item.id === target.id ? newTask : item)));
      } else {
        setSplitTasks([]);
        setDrilldownTaskId(null);
        setTaskDetail(newTask);
      }
      onTaskCompleted?.();
    } catch (err: unknown) {
      const formatted = formatApiError(err, `发起${meta.label}失败`);
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setRetryingTaskId(null);
    }
  };

  const taskRetryMeta = useMemo(
    () => (taskDetail ? getTaskRetryActionMeta(taskDetail, scope?.canWrite) : null),
    [taskDetail, scope?.canWrite],
  );

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
      title={
        isSplitMonitor
          ? `多文档独立反推监控 · ${splitTasks.length} 篇`
          : taskDetail
            ? `${isBatchTaskKind(taskDetail.task_kind) ? '批量任务监控' : '生成任务监控'} · #${taskDetail.id}`
            : batchMode
              ? '批量生成多模块 + 整仓分层文档'
              : '触发代码设计文档反推'
      }
      subtitle={
        scope
          ? `${scope.repoName}${scope.subdirectory ? ` / ${scope.subdirectory}` : ' (整仓)'} · 分支 ${activeBranch}`
          : undefined
      }
      width="lg"
      destroyOnClose
      footer={
        <div className="code-archdoc-modal-footer">
          {!isSplitMonitor && taskDetail && !isTerminal && scope?.canWrite && (
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
            {splitTasks.length > 1 && drilldownTaskId !== null && (
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => {
                  setDrilldownTaskId(null);
                  setTaskDetail(null);
                }}
              >
                <ArrowLeft size={14} />
                <span>返回多文档进度列表</span>
              </button>
            )}

            {(taskDetail || isSplitMonitor) && onOpenTaskCenter && (
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => {
                  onClose();
                  onOpenTaskCenter();
                }}
              >
                前往任务中心
              </button>
            )}

            {!isSplitMonitor && taskDetail?.parent_task_id && (
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => void refreshTask(taskDetail.parent_task_id!)}
              >
                返回批量父任务 #{taskDetail.parent_task_id}
              </button>
            )}

            <button type="button" className="btn btn-secondary" onClick={onClose}>
              {(isSplitMonitor && !splitSummary.allTerminal) || (taskDetail && !isTerminal)
                ? '后台运行并关闭'
                : '关闭'}
            </button>

            {!isSplitMonitor && !taskDetail && !initialTaskId && (
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => void handleSubmitGenerate()}
                disabled={submitting || selectedSlugs.length === 0 || !scope?.canWrite}
              >
                {submitting ? <Loader2 size={15} className="code-archdoc-spin" /> : <Play size={15} />}
                <span>
                  {submitting ? '提交中...' : getSubmitButtonLabel(batchMode, selectedSlugs.length)}
                </span>
              </button>
            )}

            {!isSplitMonitor && taskDetail && taskRetryMeta?.canRetry && (
              <button
                type="button"
                className={`btn ${taskRetryMeta.buttonVariant}`}
                disabled={retryingTaskId === taskDetail.id}
                onClick={() => void handleRetryTaskAction(taskDetail)}
                title={taskRetryMeta.tooltip}
              >
                {retryingTaskId === taskDetail.id ? (
                  <Loader2 size={15} className="code-archdoc-spin" />
                ) : (
                  <RotateCcw size={15} />
                )}
                <span>{retryingTaskId === taskDetail.id ? '提交中...' : taskRetryMeta.modalLabel}</span>
              </button>
            )}

            {!isSplitMonitor && taskDetail && taskDetail.status === 'ready' && scope && onOpenStudio && (
              <button
                type="button"
                className={`btn ${
                  taskRetryMeta?.canRetry && taskRetryMeta.buttonVariant === 'btn-primary'
                    ? 'btn-secondary'
                    : 'btn-primary'
                }`}
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
              {!taskDetail && !initialTaskId ? (
                <RepoBranchSelector
                  repoId={scope.repoId}
                  repoName={scope.repoName}
                  currentBranch={activeBranch}
                  defaultBranch={activeDefaultBranch}
                  isOverridden={activeIsOverridden}
                  canSwitch={Boolean(scope.canSwitchBranch ?? scope.canWrite)}
                  disabled={submitting}
                  size="sm"
                  onSwitched={(res) => {
                    setActiveBranch(res.current_branch);
                    setActiveDefaultBranch(res.default_branch);
                    setActiveIsOverridden(res.is_overridden);
                    if (batchMode) {
                      setBranchRescanHint(true);
                    }
                    onBranchChanged?.(res);
                  }}
                />
              ) : (
                <span className="code-archdoc-mono">
                  {taskDetail?.ref_name || activeBranch}
                </span>
              )}
            </div>
            {taskDetail?.commit_sha && (
              <div>
                <span className="code-archdoc-task-scope-label">Commit</span>
                <span className="code-archdoc-mono">{shortSha(taskDetail.commit_sha)}</span>
              </div>
            )}
          </div>
        )}

        {/* 配置态：批量范围选择 + 双引擎选择 + 策略 + 1~N DocType */}
        {!isSplitMonitor && !taskDetail && !initialTaskId && (
          <>
            {batchMode && branchRescanHint && (
              <div className="code-archdoc-branch-hint-banner">
                <span>
                  已切换至分支 <code className="code-archdoc-mono">{activeBranch}</code>，建议重新扫描一次一级目录模块以匹配新分支源码结构。
                </span>
                {scope?.canWrite && (
                  <button
                    type="button"
                    className="btn btn-secondary btn-small"
                    disabled={scanningModules}
                    onClick={() => void handleScanModulesInBatch()}
                  >
                    {scanningModules ? (
                      <Loader2 size={12} className="code-archdoc-spin" />
                    ) : (
                      <RefreshCw size={12} />
                    )}
                    <span>{scanningModules ? '扫描中...' : '立即重新扫描'}</span>
                  </button>
                )}
              </div>
            )}
            {batchMode && (
              <div className="code-archdoc-batch-panel">
                <div className="code-archdoc-batch-panel-head">
                  <span className="code-archdoc-field-label">批量目标范围（阶段 1 子模块并发 → 阶段 2 整仓上卷）</span>
                  <div className="code-archdoc-batch-toggles">
                    <label className="code-archdoc-radio-label">
                      <input
                        type="checkbox"
                        checked={allModules}
                        onChange={(e) => setAllModules(e.target.checked)}
                      />
                      <span>全部活跃子模块 ({repoModules.length})</span>
                    </label>
                    <label className="code-archdoc-radio-label">
                      <input
                        type="checkbox"
                        checked={includeRepoScope}
                        onChange={(e) => setIncludeRepoScope(e.target.checked)}
                      />
                      <span>阶段 2 同步生成整仓总览文档</span>
                    </label>
                  </div>
                </div>
                {loadingModules ? (
                  <div className="text-secondary">正在读取活跃模块清单...</div>
                ) : repoModules.length === 0 ? (
                  <div className="code-archdoc-batch-empty">
                    <div className="code-archdoc-batch-empty-text">
                      <span>尚未扫描出该仓库的一级目录模块。</span>
                      <span className="text-muted">
                        保持勾选「全部活跃子模块」提交时后端将自动扫描；也可先立即扫描预览并自由勾选目录。
                      </span>
                    </div>
                    {scope?.canWrite && (
                      <button
                        type="button"
                        className="btn btn-secondary btn-small"
                        disabled={scanningModules}
                        onClick={() => void handleScanModulesInBatch()}
                      >
                        {scanningModules ? (
                          <Loader2 size={13} className="code-archdoc-spin" />
                        ) : (
                          <FolderTree size={13} />
                        )}
                        <span>{scanningModules ? '扫描中...' : '立即扫描一级目录'}</span>
                      </button>
                    )}
                  </div>
                ) : allModules ? (
                  <div className="code-archdoc-batch-preview-list">
                    <span className="text-muted">即将包含：</span>
                    {repoModules.map((mod) => (
                      <span key={mod.id} className="code-badge code-badge--muted">
                        {mod.subdirectory}
                      </span>
                    ))}
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => {
                        setAllModules(false);
                        setSelectedModules(repoModules.map((m) => m.subdirectory));
                      }}
                    >
                      按目录自选
                    </button>
                  </div>
                ) : (
                  <div className="code-archdoc-batch-select-wrap">
                    <div className="code-archdoc-batch-select-actions">
                      <button
                        type="button"
                        className="btn btn-text btn-small"
                        onClick={() => setSelectedModules(repoModules.map((m) => m.subdirectory))}
                      >
                        全选 ({repoModules.length})
                      </button>
                      <button
                        type="button"
                        className="btn btn-text btn-small"
                        onClick={() => setSelectedModules([])}
                      >
                        清空
                      </button>
                    </div>
                    <div className="code-archdoc-batch-module-chips">
                      {repoModules.map((mod) => {
                        const checked = selectedModules.includes(mod.subdirectory);
                        return (
                          <label
                            key={mod.id}
                            className={`code-archdoc-batch-chip ${
                              checked ? 'code-archdoc-batch-chip--checked' : ''
                            }`}
                          >
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={() => toggleModuleSubdir(mod.subdirectory)}
                            />
                            <code className="code-archdoc-mono">{mod.subdirectory}</code>
                            <span className="text-muted">({mod.effective_files} 文件)</span>
                          </label>
                        );
                      })}
                    </div>
                  </div>
                )}
              </div>
            )}

            <div className="code-archdoc-engine-panel">
              <div className="code-archdoc-engine-selector-row">
                <span className="text-secondary">
                  <Cpu size={14} className="code-archdoc-inline-icon" /> 生成引擎：
                </span>
                <div className="code-archdoc-engine-selector" role="radiogroup" aria-label="文档生成引擎">
                  {engines.map((eng) => {
                    const active = selectedEngine === eng.name;
                    const disabledReason = !eng.available ? eng.reason || '当前不可用' : undefined;
                    return (
                      <button
                        key={eng.name}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        disabled={!eng.available || submitting}
                        title={disabledReason || eng.display_name}
                        className={`code-archdoc-engine-option ${
                          active ? 'code-archdoc-engine-option--active' : ''
                        }`}
                        onClick={() => handleSelectEngine(eng.name)}
                      >
                        <span>{eng.display_name}</span>
                        {eng.version && (
                          <span className="code-archdoc-engine-ver code-archdoc-mono">
                            v{eng.version}
                          </span>
                        )}
                      </button>
                    );
                  })}
                </div>
              </div>

              {selectedEngine === 'opencode' && (
                <div className="code-archdoc-engine-hint">
                  OpenCode 引擎将在严格只读沙箱中自主查阅源码上下文，适合复杂业务梳理；单篇耗时通常长于 Native 引擎，并保留 Verifier 四重门禁与锁定章节保护。
                </div>
              )}
            </div>

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

            <div className="code-archdoc-refdoc-panel">
              <div className="code-archdoc-refdoc-panel-head">
                <span className="code-archdoc-field-label">
                  <BookOpen size={13} className="code-archdoc-inline-icon" /> 生效架构参考文档（背景注入）
                </span>
                {loadingRefDocs ? (
                  <span className="text-muted">加载中...</span>
                ) : (
                  <span className="code-badge code-badge--info">
                    已选 {computeIncludedRefDocStats(effectiveRefDocs, checkedRefDocIds).includedCount}/
                    {effectiveRefDocs.length} 篇 · 约{' '}
                    {formatRefDocTokenBadge(
                      computeIncludedRefDocStats(effectiveRefDocs, checkedRefDocIds).includedTokens,
                    )}
                  </span>
                )}
              </div>
              {refDocsRollup && (
                <div className="text-muted">
                  整仓汇总模式下仅保留系统/子系统/功能组/仓库级上位背景（上限 12 篇）。
                </div>
              )}
              {!loadingRefDocs && effectiveRefDocs.length === 0 ? (
                <div className="text-muted">当前作用域未绑定生效架构参考文档</div>
              ) : (
                <div className="code-archdoc-refdoc-groups">
                  {groupEffectiveRefDocsByScope(effectiveRefDocs).map((grp) => (
                    <div key={grp.level} className="code-archdoc-refdoc-group">
                      <div className="code-archdoc-refdoc-group-label">{grp.label}</div>
                      <div className="code-archdoc-refdoc-group-items">
                        {grp.items.map((item) => {
                          const checked = checkedRefDocIds.has(item.doc_id);
                          return (
                            <label key={item.doc_id} className="code-archdoc-refdoc-check-item">
                              <input
                                type="checkbox"
                                checked={checked}
                                onChange={() =>
                                  setCheckedRefDocIds((prev) => {
                                    const next = new Set(prev);
                                    if (checked) {
                                      next.delete(item.doc_id);
                                    } else {
                                      next.add(item.doc_id);
                                    }
                                    return next;
                                  })
                                }
                              />
                              <span>{item.title}</span>
                              <span className="code-archdoc-mono">v{item.current_version}</span>
                              <span className="text-muted">
                                ({formatRefDocScopeLabel(item.effective_scope_level)} ·{' '}
                                {formatRefDocTokenBadge(item.estimated_tokens)})
                              </span>
                            </label>
                          );
                        })}
                      </div>
                    </div>
                  ))}
                </div>
              )}
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
                        {dt.category && (
                          <span className="code-badge code-badge--purple">{dt.category}</span>
                        )}
                        <span className="code-badge code-badge--muted">{dt.slug}</span>
                        <span
                          className={`code-badge ${
                            dt.preferred_model ? 'code-badge--primary' : 'code-badge--muted'
                          }`}
                        >
                          {dt.preferred_model || '默认模型'}
                        </span>
                        <span className="code-badge code-badge--muted">{dt.slot_count} 个槽位</span>
                      </div>
                    </div>
                  </label>
                );
              })}
            </div>
          </>
        )}

        {/* 多文档拆单聚合监控视图 */}
        {isSplitMonitor && (
          <div className="code-archdoc-split-monitor">
            <div className="code-archdoc-progress-block">
              <div className="code-archdoc-progress-header">
                <div className="flex-center gap-sm">
                  <span
                    className={`code-archdoc-engine-badge code-archdoc-engine-badge--${
                      splitTasks[0]?.engine === 'opencode' ? 'opencode' : 'native'
                    }`}
                  >
                    {formatEngineLabel(splitTasks[0]?.engine)}
                  </span>
                  <span className="text-secondary">
                    已完成 {splitSummary.readyCount}/{splitSummary.total} 篇
                    {splitSummary.failedCount > 0 ? ` · ${splitSummary.failedCount} 篇失败` : ''}
                  </span>
                </div>
                <span className="code-archdoc-mono">{splitSummary.overallPercent}%</span>
              </div>
              <div className="code-archdoc-progress-track">
                <div
                  className={`code-archdoc-progress-fill ${
                    splitSummary.failedCount > 0 && splitSummary.allTerminal
                      ? 'code-archdoc-progress-fill--failed'
                      : splitSummary.allTerminal
                        ? 'code-archdoc-progress-fill--ready'
                        : ''
                  }`}
                  style={{ width: `${splitSummary.overallPercent}%` }}
                />
              </div>
            </div>

            <div className="code-archdoc-split-list">
              {splitTasks.map((item) => {
                const sm = taskStatusMeta(item.status);
                const slug = item.doc_types?.[0] || '';
                const docDisplayName = docTypeDisplayMap.get(slug) || slug || `任务 #${item.id}`;
                const itemTerminal = TERMINAL_STATUSES.has(item.status);
                const itemDocId = item.slot_tasks?.find((s) => s.document_id)?.document_id ?? undefined;
                return (
                  <div key={item.id} className="code-archdoc-split-row">
                    <div className="code-archdoc-split-row-main">
                      <div className="code-archdoc-split-row-title">
                        <code className="code-archdoc-mono">#{item.id}</code>
                        <span>{docDisplayName}</span>
                        <span className="code-badge code-badge--muted">{slug}</span>
                      </div>
                      {item.error_message && (
                        <div className="code-archdoc-split-row-err">{item.error_message}</div>
                      )}
                    </div>
                    <div className="code-archdoc-split-row-meta">
                      <StatusTag status={sm.tag} text={sm.label} size="sm" />
                      <span className="code-archdoc-mono">{item.progress_percent}%</span>
                      <button
                        type="button"
                        className="btn btn-text btn-small"
                        onClick={() => handleDrilldownSplitTask(item.id)}
                      >
                        查看槽位
                      </button>
                      {item.status === 'ready' && scope && onOpenStudio && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-small"
                          onClick={() => {
                            onClose();
                            onOpenStudio(scope.repoId, itemDocId, scope.subdirectory);
                          }}
                        >
                          进入 Studio
                        </button>
                      )}
                      {!itemTerminal && scope?.canWrite && (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          onClick={() => void handleCancelSplitRow(item.id)}
                        >
                          取消
                        </button>
                      )}
                      {(item.status === 'failed' || item.status === 'canceled') && scope?.canWrite && (
                        <button
                          type="button"
                          className="btn btn-outline btn-small"
                          disabled={retryingTaskId === item.id}
                          onClick={() => void handleRetryTaskAction(item, { replaceInSplitList: true })}
                          title="基于原配置重新提交该失败文档任务"
                        >
                          {retryingTaskId === item.id ? (
                            <Loader2 size={13} className="code-archdoc-spin" />
                          ) : (
                            <RotateCcw size={13} />
                          )}
                          <span>重试</span>
                        </button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* 单任务 / 下钻详情监控态：实时进度、阶段、预算熔断与槽位子任务 */}
        {!isSplitMonitor && taskDetail && (
          <>
            <div className="code-archdoc-progress-block">
              <div className="code-archdoc-progress-header">
                <div className="flex-center gap-sm">
                  <StatusTag
                    status={taskStatusMeta(taskDetail.status).tag}
                    text={taskStatusMeta(taskDetail.status).label}
                    size="sm"
                  />
                  <span
                    className={`code-archdoc-engine-badge code-archdoc-engine-badge--${
                      taskDetail.engine === 'opencode' ? 'opencode' : 'native'
                    }`}
                  >
                    {formatEngineLabel(taskDetail.engine)}
                  </span>
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
                {taskDetail.budget_advice.module_breakdown &&
                  taskDetail.budget_advice.module_breakdown.length > 0 && (
                    <div>
                      <div className="text-secondary">各模块预估预算拆解：</div>
                      <ul className="code-archdoc-budget-list">
                        {taskDetail.budget_advice.module_breakdown.map((mb) => (
                          <li key={mb.subdirectory || 'root'}>
                            <code className="code-archdoc-mono">{mb.subdirectory || '整仓 (Root)'}</code> — 约{' '}
                            {mb.estimated_tokens.toLocaleString()} Tokens（{mb.effective_files} 文件）
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

            {taskDetail.reference_docs && taskDetail.reference_docs.length > 0 && (
              <div className="code-archdoc-refdoc-snapshot-bar">
                <BookOpen size={13} />
                <span className="text-secondary">本次冻结参考文档：</span>
                {taskDetail.reference_docs.map((rd) => (
                  <span key={`${rd.doc_id}-${rd.version_no}`} className="code-badge code-badge--info">
                    {rd.title} v{rd.version_no}
                  </span>
                ))}
              </div>
            )}

            {/* 批量父任务聚合摘要与子任务列表（F-31） */}
            {taskDetail.batch_summary && (
              <div className="code-archdoc-batch-summary">
                <span className="code-badge code-badge--muted">
                  总子任务 {taskDetail.batch_summary.total_children}
                </span>
                <span className="code-badge code-badge--success">
                  已完成 {taskDetail.batch_summary.completed_children}
                </span>
                <span className="code-badge code-badge--primary">
                  执行中 {taskDetail.batch_summary.running_children}
                </span>
                {taskDetail.batch_summary.failed_children > 0 && (
                  <span className="code-badge code-badge--danger">
                    失败 {taskDetail.batch_summary.failed_children}
                  </span>
                )}
                {taskDetail.batch_summary.canceled_children > 0 && (
                  <span className="code-badge code-badge--muted">
                    已取消 {taskDetail.batch_summary.canceled_children}
                  </span>
                )}
              </div>
            )}

            {taskDetail.child_tasks && taskDetail.child_tasks.length > 0 && (
              <div className="code-archdoc-slot-table-wrap">
                <table className="code-table">
                  <thead>
                    <tr>
                      <th>子任务</th>
                      <th>范围</th>
                      <th>状态</th>
                      <th>进度</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {taskDetail.child_tasks.map((child) => {
                      const cm = taskStatusMeta(child.status);
                      return (
                        <tr key={child.id}>
                          <td>
                            <div className="code-archdoc-split-row-title">
                              <code className="code-archdoc-mono">#{child.id}</code>
                              <span
                                className={`code-archdoc-engine-badge code-archdoc-engine-badge--${
                                  child.engine === 'opencode' ? 'opencode' : 'native'
                                }`}
                              >
                                {formatEngineLabel(child.engine)}
                              </span>
                            </div>
                          </td>
                          <td>
                            <code className="code-archdoc-mono">
                              {child.subdirectory || '整仓 (Root)'}
                            </code>
                            {child.doc_types?.[0] && (
                              <span className="code-badge code-badge--primary code-archdoc-child-doctype-badge">
                                {docTypeDisplayMap.get(child.doc_types[0]) || child.doc_types[0]}
                              </span>
                            )}
                            {child.error_message && (
                              <div className="text-muted">{child.error_message}</div>
                            )}
                          </td>
                          <td>
                            <StatusTag status={cm.tag} text={cm.label} size="sm" />
                          </td>
                          <td>
                            <span className="code-archdoc-mono">{child.progress_percent}%</span>
                          </td>
                          <td>
                            <div className="code-archdoc-row-actions">
                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                onClick={() => void refreshTask(child.id)}
                              >
                                查看明细
                              </button>
                              {(child.status === 'failed' || child.status === 'canceled') &&
                                Boolean(scope?.canWrite) && (
                                  <button
                                    type="button"
                                    className="btn btn-outline btn-small"
                                    disabled={retryingTaskId === child.id}
                                    onClick={() => void handleRetryTaskAction(child)}
                                    title="重试该失败子任务"
                                  >
                                    {retryingTaskId === child.id ? (
                                      <Loader2 size={13} className="code-archdoc-spin" />
                                    ) : (
                                      <RotateCcw size={13} />
                                    )}
                                    <span>重试</span>
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
            )}

            {/* 槽位子任务列表与单槽位独立重试 */}
            {taskDetail.slot_tasks && taskDetail.slot_tasks.length > 0 && (
              <div className="code-archdoc-slot-table-wrap">
                <table className="code-table">
                  <thead>
                    <tr>
                      <th>文档类型</th>
                      <th>章节槽位</th>
                      <th>模型</th>
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
                            <span className="code-badge code-badge--muted">{slot.model_name || '-'}</span>
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
