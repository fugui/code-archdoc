import React, { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  BarChart3,
  CheckCircle2,
  Edit3,
  FileCode2,
  FileText,
  FolderGit2,
  Layers,
  ListChecks,
  Loader2,
  MessageSquareCode,
  Radar,
  RefreshCw,
} from 'lucide-react';
import { EmptyState, StatusTag, useToast, type StatusType } from '@code/common';
import { listDocTypeRevisions } from '../api/admin';
import { getDriftSummary } from '../api/drift';
import {
  compareMetricsRevisions,
  getMetricsOverview,
  getMetricsSlotHeatmap,
} from '../api/metrics';
import { getArchTree, listDocTypes } from '../api/repos';
import { listTasks } from '../api/tasks';
import RevisionTrendChart from '../components/metrics/RevisionTrendChart';
import { useModuleUser } from '../context/ModuleUserContext';
import type {
  ArchTreeSummaryVO,
  DocTypeRevisionSummaryVO,
  DocTypeSummaryVO,
  DriftSummaryVO,
  MetricsOverviewVO,
  RevisionMetricPointVO,
  SlotHeatmapItemVO,
  TaskSummaryVO,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import {
  buildModulePath,
  buildOverviewDrilldownPath,
  formatCoverageRate,
  formatDateTime,
} from '../utils/urlQuery';
import './QualityMetricsPage.css';

type OverviewTargetPath = '/repos' | '/arch-tree' | '/tasks' | '/drift';
type OverviewIconTone = 'primary' | 'purple' | 'success' | 'warning';

interface OverviewCardSpec {
  key: string;
  label: string;
  value: string | number;
  sub: string;
  icon: React.ReactNode;
  iconTone: OverviewIconTone;
  targetPath: OverviewTargetPath;
  query?: Record<string, string>;
}

const OverviewCardGrid: React.FC<{
  cards: OverviewCardSpec[];
  onDrilldown: (targetPath: OverviewTargetPath, query?: Record<string, string>) => void;
}> = ({ cards, onDrilldown }) => (
  <div className="code-archdoc-overview-grid-4">
    {cards.map((card) => (
      <button
        key={card.key}
        type="button"
        className="code-card code-archdoc-overview-card code-archdoc-overview-card--clickable"
        onClick={() => onDrilldown(card.targetPath, card.query)}
        title={`点击前往查看「${card.label}」明细`}
      >
        <div
          className={`code-archdoc-overview-card-icon code-archdoc-stat-icon--${card.iconTone}`}
        >
          {card.icon}
        </div>
        <div className="code-archdoc-overview-card-body">
          <div className="code-archdoc-overview-card-top">
            <span className="code-archdoc-overview-card-label">{card.label}</span>
            <ArrowRight size={13} className="code-archdoc-overview-card-arrow" />
          </div>
          <span className="code-archdoc-overview-card-val">{card.value}</span>
          <span className="code-archdoc-overview-card-sub">{card.sub}</span>
        </div>
      </button>
    ))}
  </div>
);

function formatPercent(v: number): string {
  return `${(v * 100).toFixed(1)}%`;
}

function slotHeatLevel(ratio: number): {
  barClass: string;
  tagStatus: StatusType;
  advice: string;
} {
  if (ratio >= 0.35) {
    return {
      barClass: 'code-archdoc-metrics-heat-fill--high',
      tagStatus: 'failed',
      advice: '高频大幅改写 · 建议优先调优 Prompt',
    };
  }
  if (ratio >= 0.18) {
    return {
      barClass: 'code-archdoc-metrics-heat-fill--medium',
      tagStatus: 'warning',
      advice: '中等人工补充 · 可按需微调指令提示',
    };
  }
  return {
    barClass: 'code-archdoc-metrics-heat-fill--low',
    tagStatus: 'success',
    advice: '初稿采纳度高 · 质量稳定',
  };
}

export const QualityMetricsPage: React.FC = () => {
  const navigate = useNavigate();
  const { isEmbedded } = useModuleUser();
  const { showToast } = useToast();

  // 三大宏观态势摘要状态
  const [summaryLoading, setSummaryLoading] = useState<boolean>(true);
  const [coverageSummary, setCoverageSummary] = useState<ArchTreeSummaryVO>({
    total_modules: 0,
    bound_modules: 0,
    covered_modules: 0,
    approved_docs: 0,
    draft_docs: 0,
    drifted_docs: 0,
    unassigned_repo_count: 0,
  });
  const [driftSummary, setDriftSummary] = useState<DriftSummaryVO>({
    total_drifted_docs: 0,
    code_drift_docs: 0,
    spec_drift_docs: 0,
    both_drift_docs: 0,
    monitored_repos: 0,
    unhealthy_repos: 0,
    last_poll_at: null,
    poll_interval_minutes: 15,
  });
  const [taskSummary, setTaskSummary] = useState<TaskSummaryVO>({
    total: 0,
    active: 0,
    ready: 0,
    failed: 0,
    canceled: 0,
  });

  // 第四分区：生成质量度量与修订纵向对比状态
  const [days, setDays] = useState<number>(30);
  const [docTypes, setDocTypes] = useState<DocTypeSummaryVO[]>([]);
  const [selectedDocType, setSelectedDocType] = useState<string>('system-architecture');
  const [revisions, setRevisions] = useState<DocTypeRevisionSummaryVO[]>([]);
  const [selectedRevId, setSelectedRevId] = useState<string>('');

  const [overview, setOverview] = useState<MetricsOverviewVO>({
    window_days: 30,
    avg_edit_diff_ratio: 0,
    chat_accept_rate: 0,
    verifier_first_pass_rate: 0,
    verifier_block_rate: 0,
    avg_slot_coverage_rate: 0,
    avg_tokens_per_doc: 0,
    avg_cache_hit_ratio: 0,
    sample_doc_count: 0,
  });
  const [trendPoints, setTrendPoints] = useState<RevisionMetricPointVO[]>([]);
  const [heatmapSlots, setHeatmapSlots] = useState<SlotHeatmapItemVO[]>([]);
  const [loading, setLoading] = useState<boolean>(true);

  const handleDrilldown = useCallback(
    (targetPath: OverviewTargetPath, query?: Record<string, string>) => {
      navigate(buildOverviewDrilldownPath(isEmbedded, targetPath, query));
    },
    [isEmbedded, navigate],
  );

  // 加载三大宏观态势摘要（覆盖率 / 漂移 / 任务队列）
  const loadGlobalSummaries = useCallback(async () => {
    setSummaryLoading(true);
    try {
      const [archTreeRes, driftRes, taskRes] = await Promise.allSettled([
        getArchTree({}),
        getDriftSummary(),
        listTasks({ page: 1, pageSize: 15, topLevelOnly: true }),
      ]);
      if (archTreeRes.status === 'fulfilled' && archTreeRes.value.summary) {
        setCoverageSummary(archTreeRes.value.summary);
      }
      if (driftRes.status === 'fulfilled') {
        setDriftSummary(driftRes.value);
      }
      if (taskRes.status === 'fulfilled' && taskRes.value.summary) {
        setTaskSummary(taskRes.value.summary);
      }
      const failedCount = [archTreeRes, driftRes, taskRes].filter(
        (res) => res.status === 'rejected',
      ).length;
      if (failedCount > 0) {
        showToast(
          `部分全局汇总接口加载失败（${failedCount} 项），其余分区已正常展示`,
          'warning',
        );
      }
    } finally {
      setSummaryLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    void loadGlobalSummaries();
  }, [loadGlobalSummaries]);

  // 初始化加载 DocType 列表
  useEffect(() => {
    let active = true;
    void listDocTypes(false)
      .then((list) => {
        if (!active) return;
        setDocTypes(list);
        if (list.length > 0 && !list.some((d) => d.slug === selectedDocType)) {
          setSelectedDocType(list[0].slug);
        }
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载文档类型失败').message, 'error');
      });
    return () => {
      active = false;
    };
  }, [selectedDocType, showToast]);

  // 切换 DocType 时拉取其修订历史供版本过滤
  useEffect(() => {
    if (!selectedDocType) {
      setRevisions([]);
      return;
    }
    let active = true;
    void listDocTypeRevisions(selectedDocType)
      .then((revs) => {
        if (!active) return;
        setRevisions(revs);
      })
      .catch(() => {
        if (!active) return;
        setRevisions([]);
      });
    return () => {
      active = false;
    };
  }, [selectedDocType]);

  const loadMetrics = useCallback(async () => {
    if (!selectedDocType) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const revIdNum = selectedRevId ? Number.parseInt(selectedRevId, 10) : undefined;
      const [ov, cmp, hm] = await Promise.all([
        getMetricsOverview({
          days,
          docType: selectedDocType,
          docTypeRevisionId: revIdNum,
        }),
        compareMetricsRevisions({
          docType: selectedDocType,
          days: Math.max(days, 90),
        }),
        getMetricsSlotHeatmap({
          docType: selectedDocType,
          docTypeRevisionId: revIdNum,
          days,
        }),
      ]);
      setOverview(ov);
      setTrendPoints(Array.isArray(cmp.revisions) ? cmp.revisions : []);
      setHeatmapSlots(Array.isArray(hm.slots) ? hm.slots : []);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载在线质量度量数据失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [days, selectedDocType, selectedRevId, showToast]);

  useEffect(() => {
    void loadMetrics();
  }, [loadMetrics]);

  const handleRefreshAll = () => {
    void Promise.all([loadGlobalSummaries(), loadMetrics()]);
  };

  const coverageRateText = formatCoverageRate(
    coverageSummary.covered_modules,
    coverageSummary.total_modules,
  );

  const coverageCards: OverviewCardSpec[] = [
    {
      key: 'total_modules',
      label: '架构功能模块总数',
      value: coverageSummary.total_modules,
      sub: `总体覆盖率 ${coverageRateText} · 已绑源码 ${coverageSummary.bound_modules} / ${coverageSummary.total_modules}`,
      icon: <FolderGit2 size={20} />,
      iconTone: 'primary',
      targetPath: '/arch-tree',
    },
    {
      key: 'covered_modules',
      label: '已覆盖功能模块',
      value: coverageSummary.covered_modules,
      sub: '已产出至少 1 篇设计文档的架构功能模块',
      icon: <Layers size={20} />,
      iconTone: 'purple',
      targetPath: '/arch-tree',
    },
    {
      key: 'approved_docs',
      label: '已发布架构文档',
      value: coverageSummary.approved_docs,
      sub: '架构功能模块已发布定稿，并挂载至门户知识库',
      icon: <CheckCircle2 size={20} />,
      iconTone: 'success',
      targetPath: '/arch-tree',
      query: { status: 'approved' },
    },
    {
      key: 'drifted_docs',
      label: '架构漂移文档',
      value: coverageSummary.drifted_docs,
      sub: '架构功能模块待刷新；全平台含独立仓见下方漂移区',
      icon: <AlertTriangle size={20} />,
      iconTone: 'warning',
      targetPath: '/arch-tree',
      query: { status: 'drifted' },
    },
  ];

  const driftCards: OverviewCardSpec[] = [
    {
      key: 'total_drifted',
      label: '待治理漂移文档',
      value: driftSummary.total_drifted_docs,
      sub: `全平台已发布文档（含独立未关联仓）· 双维漂移 ${driftSummary.both_drift_docs} 篇`,
      icon: <AlertTriangle size={20} />,
      iconTone: 'warning',
      targetPath: '/drift',
    },
    {
      key: 'code_drift',
      label: 'Code Drift（代码语义变更）',
      value: driftSummary.code_drift_docs,
      sub: '基于 AST 指纹，行号平移 0 误报',
      icon: <FileCode2 size={20} />,
      iconTone: 'primary',
      targetPath: '/drift',
      query: { drift_type: 'code' },
    },
    {
      key: 'spec_drift',
      label: 'Spec Drift（模板规范升级）',
      value: driftSummary.spec_drift_docs,
      sub: 'DocType 新修订槽位/Prompt 差异',
      icon: <FileText size={20} />,
      iconTone: 'purple',
      targetPath: '/drift',
      query: { drift_type: 'spec' },
    },
    {
      key: 'poll_status',
      label: '远端轻量巡检状态',
      value: `${driftSummary.monitored_repos} 仓`,
      sub: `${
        driftSummary.unhealthy_repos > 0 ? `${driftSummary.unhealthy_repos} 仓退避重试中 · ` : ''
      }每 ${driftSummary.poll_interval_minutes}m · 最近 ${formatDateTime(driftSummary.last_poll_at)}`,
      icon: <Radar size={20} />,
      iconTone: 'success',
      targetPath: '/drift',
    },
  ];

  const taskCards: OverviewCardSpec[] = [
    {
      key: 'task_total',
      label: '任务总数',
      value: taskSummary.total,
      sub: '累计反推生成与批量分层编排任务',
      icon: <ListChecks size={20} />,
      iconTone: 'primary',
      targetPath: '/tasks',
    },
    {
      key: 'task_active',
      label: '执行中 / 排队',
      value: taskSummary.active,
      sub: '正在抽取 Code IR 或执行 LLM 并发生成',
      icon: <Activity size={20} />,
      iconTone: 'purple',
      targetPath: '/tasks',
      query: { status: 'active' },
    },
    {
      key: 'task_ready',
      label: '已完成',
      value: taskSummary.ready,
      sub: '已产出修订草稿并等待工作台审校定稿',
      icon: <CheckCircle2 size={20} />,
      iconTone: 'success',
      targetPath: '/tasks',
      query: { status: 'ready' },
    },
    {
      key: 'task_failed',
      label: '失败 / 需处理',
      value: taskSummary.failed,
      sub: `已取消 ${taskSummary.canceled} 个 · 支持单槽位重试`,
      icon: <AlertTriangle size={20} />,
      iconTone: 'warning',
      targetPath: '/tasks',
      query: { status: 'failed' },
    },
  ];

  const isRefreshing = loading || summaryLoading;

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">全局概览 · 文档治理与质量度量大盘</h1>
          <p className="code-archdoc-page-subtitle">
            一站式总览仓库/架构文档资产覆盖率、章节级双维漂移健康度、AI 反推任务队列态势及各 DocType 修订质量度量，点击任意指标卡片即可直达明细处理。
          </p>
        </div>

        <div className="code-archdoc-page-actions">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={handleRefreshAll}
            disabled={isRefreshing}
          >
            <RefreshCw size={15} className={isRefreshing ? 'code-archdoc-spin' : ''} />
            <span>刷新大盘</span>
          </button>
        </div>
      </div>

      {/* 分区 1：文档资产与覆盖态势 */}
      <section className="code-archdoc-overview-section" aria-label="文档资产与覆盖态势">
        <div className="code-archdoc-overview-section-head">
          <div className="code-archdoc-overview-section-title-group">
            <h2 className="code-archdoc-overview-section-title">
              <FolderGit2 size={17} />
              <span>文档资产与覆盖态势</span>
            </h2>
            <p className="code-archdoc-overview-section-sub">
              聚合代码仓整仓、Monorepo 子目录与架构树节点的设计文档沉淀规模及定稿进展
            </p>
          </div>

          <div className="code-archdoc-overview-section-links">
            <button
              type="button"
              className="code-archdoc-overview-section-link code-archdoc-overview-section-link--primary"
              onClick={() => navigate(buildModulePath(isEmbedded, '/arch-tree'))}
            >
              <span>架构文档全景</span>
              <ArrowRight size={13} />
            </button>
            <button
              type="button"
              className="code-archdoc-overview-section-link"
              onClick={() => navigate(buildModulePath(isEmbedded, '/repos'))}
            >
              <span>未关联代码仓</span>
              {coverageSummary.unassigned_repo_count > 0 && (
                <span
                  className="code-archdoc-overview-count-badge"
                  title={`${coverageSummary.unassigned_repo_count} 个活跃代码仓尚未关联任何架构功能模块`}
                >
                  {coverageSummary.unassigned_repo_count}
                </span>
              )}
              <ArrowRight size={13} />
            </button>
          </div>
        </div>

        <OverviewCardGrid cards={coverageCards} onDrilldown={handleDrilldown} />
      </section>

      {/* 分区 2：双维漂移与保鲜健康度 */}
      <section className="code-archdoc-overview-section" aria-label="双维漂移与保鲜健康度">
        <div className="code-archdoc-overview-section-head">
          <div className="code-archdoc-overview-section-title-group">
            <h2 className="code-archdoc-overview-section-title">
              <Radar size={17} />
              <span>双维漂移与保鲜健康度</span>
            </h2>
            <p className="code-archdoc-overview-section-sub">
              基于 AST 语义指纹与 DocType 修订对比，实时追踪已发布文档的保鲜水位与远端巡检状态
            </p>
          </div>

          <div className="code-archdoc-overview-section-links">
            <button
              type="button"
              className="code-archdoc-overview-section-link"
              onClick={() => navigate(buildModulePath(isEmbedded, '/drift'))}
            >
              <span>进入双维漂移雷达</span>
              <ArrowRight size={13} />
            </button>
          </div>
        </div>

        <OverviewCardGrid cards={driftCards} onDrilldown={handleDrilldown} />
      </section>

      {/* 分区 3：反推生产任务态势 */}
      <section className="code-archdoc-overview-section" aria-label="反推生产任务态势">
        <div className="code-archdoc-overview-section-head">
          <div className="code-archdoc-overview-section-title-group">
            <h2 className="code-archdoc-overview-section-title">
              <ListChecks size={17} />
              <span>反推生产任务态势</span>
            </h2>
            <p className="code-archdoc-overview-section-sub">
              集中观测 AI 反推队列运行负荷、批量分层编排吞吐及待重试异常任务
            </p>
          </div>

          <div className="code-archdoc-overview-section-links">
            <button
              type="button"
              className="code-archdoc-overview-section-link"
              onClick={() => navigate(buildModulePath(isEmbedded, '/tasks'))}
            >
              <span>进入反推任务中心</span>
              <ArrowRight size={13} />
            </button>
          </div>
        </div>

        <OverviewCardGrid cards={taskCards} onDrilldown={handleDrilldown} />
      </section>

      {/* 分区 4：生成质量度量与修订纵向对比 */}
      <section className="code-archdoc-overview-section" aria-label="生成质量度量与修订纵向对比">
        <div className="code-archdoc-overview-section-head">
          <div className="code-archdoc-overview-section-title-group">
            <h2 className="code-archdoc-overview-section-title">
              <BarChart3 size={17} />
              <span>生成质量度量与修订纵向对比</span>
            </h2>
            <p className="code-archdoc-overview-section-sub">
              闭环度量首版 AI 草稿→定稿章节级修改率、Chat 局部精修采纳率及 Verifier 门禁拦截率
            </p>
          </div>

          <div className="code-archdoc-overview-quality-filters">
            <div className="code-archdoc-metrics-filter-group">
              <label className="code-archdoc-metrics-filter-label" htmlFor="metrics-doctype-select">
                文档类型
              </label>
              <select
                id="metrics-doctype-select"
                className="code-select code-archdoc-metrics-select"
                value={selectedDocType}
                onChange={(e) => {
                  setSelectedDocType(e.target.value);
                  setSelectedRevId('');
                }}
              >
                {docTypes.map((dt) => (
                  <option key={dt.slug} value={dt.slug}>
                    {dt.display_name} ({dt.slug})
                  </option>
                ))}
              </select>
            </div>

            <div className="code-archdoc-metrics-filter-group">
              <label className="code-archdoc-metrics-filter-label" htmlFor="metrics-rev-select">
                修订版本
              </label>
              <select
                id="metrics-rev-select"
                className="code-select code-archdoc-metrics-select"
                value={selectedRevId}
                onChange={(e) => setSelectedRevId(e.target.value)}
              >
                <option value="">全部修订聚合</option>
                {revisions.map((r) => (
                  <option key={r.id} value={String(r.id)}>
                    Rev {r.revision_no} ({r.aggregate_hash.slice(0, 8)})
                  </option>
                ))}
              </select>
            </div>

            <div className="code-archdoc-metrics-filter-group">
              <label className="code-archdoc-metrics-filter-label" htmlFor="metrics-days-select">
                统计窗口
              </label>
              <select
                id="metrics-days-select"
                className="code-select"
                value={days}
                onChange={(e) => setDays(Number.parseInt(e.target.value, 10) || 30)}
              >
                <option value={7}>最近 7 天</option>
                <option value={30}>最近 30 天</option>
                <option value={90}>最近 90 天</option>
                <option value={180}>最近 180 天</option>
                <option value={365}>最近 365 天</option>
              </select>
            </div>
          </div>
        </div>

        {loading ? (
          <div className="code-archdoc-loading">
            <Loader2 size={18} className="code-archdoc-spin" />
            <span>正在聚合质量度量指标...</span>
          </div>
        ) : (
          <>
            {/* 4 大核心 KPI 卡片 */}
            <div className="code-archdoc-metrics-kpis">
              <div className="code-card code-archdoc-metrics-kpi">
                <div className="code-archdoc-metrics-kpi-top">
                  <span className="code-archdoc-metrics-kpi-label">草稿→定稿修改率</span>
                  <div className="code-archdoc-metrics-kpi-icon code-archdoc-stat-icon--warning">
                    <Edit3 size={16} />
                  </div>
                </div>
                <span className="code-archdoc-metrics-kpi-val">
                  {formatPercent(overview.avg_edit_diff_ratio)}
                </span>
                <span className="code-archdoc-metrics-kpi-sub">
                  越低代表 AI 初稿越贴近定稿（样本 {overview.sample_doc_count} 篇）
                </span>
              </div>

              <div className="code-card code-archdoc-metrics-kpi">
                <div className="code-archdoc-metrics-kpi-top">
                  <span className="code-archdoc-metrics-kpi-label">Chat 精修采纳率</span>
                  <div className="code-archdoc-metrics-kpi-icon code-archdoc-stat-icon--success">
                    <MessageSquareCode size={16} />
                  </div>
                </div>
                <span className="code-archdoc-metrics-kpi-val">
                  {formatPercent(overview.chat_accept_rate)}
                </span>
                <span className="code-archdoc-metrics-kpi-sub">
                  Accepted 提案 / 已决提案总数
                </span>
              </div>

              <div className="code-card code-archdoc-metrics-kpi">
                <div className="code-archdoc-metrics-kpi-top">
                  <span className="code-archdoc-metrics-kpi-label">Verifier 首次通过率</span>
                  <div className="code-archdoc-metrics-kpi-icon code-archdoc-stat-icon--primary">
                    <CheckCircle2 size={16} />
                  </div>
                </div>
                <span className="code-archdoc-metrics-kpi-val">
                  {formatPercent(overview.verifier_first_pass_rate)}
                </span>
                <span className="code-archdoc-metrics-kpi-sub">
                  阻断拦截率 {formatPercent(overview.verifier_block_rate)}
                </span>
              </div>

              <div className="code-card code-archdoc-metrics-kpi">
                <div className="code-archdoc-metrics-kpi-top">
                  <span className="code-archdoc-metrics-kpi-label">必填槽位完备度</span>
                  <div className="code-archdoc-metrics-kpi-icon code-archdoc-stat-icon--purple">
                    <Layers size={16} />
                  </div>
                </div>
                <span className="code-archdoc-metrics-kpi-val">
                  {formatPercent(overview.avg_slot_coverage_rate)}
                </span>
                <span className="code-archdoc-metrics-kpi-sub">
                  Gate 2 必填章节有效字符达标率
                </span>
              </div>
            </div>

            {/* 原生轻量双主题 SVG 趋势图 */}
            <RevisionTrendChart docTypeSlug={selectedDocType} revisions={trendPoints} />

            {/* 槽位级修改率与告警热力表 */}
            <div className="code-card code-archdoc-metrics-heatmap-card">
              <div className="code-archdoc-metrics-heatmap-head">
                <div>
                  <h3 className="code-archdoc-metrics-heatmap-title">
                    <BarChart3 size={17} /> 章节槽位修改率热力表（{selectedDocType}）
                  </h3>
                  <p className="code-archdoc-metrics-heatmap-sub">
                    按历史平均人工修改比例降序排列，快速识别需要补充{' '}
                    <code className="code-archdoc-mono">instruction_hint</code> 或优化{' '}
                    <code className="code-archdoc-mono">ir_facets</code> 的薄弱槽位
                  </p>
                </div>
              </div>

              {heatmapSlots.length === 0 ? (
                <EmptyState
                  title="暂无章节槽位热力数据"
                  description="当文档完成首次定稿发布后，系统将自动计算各章节 Levenshtein / Myers 修改率并生成热力排行。"
                />
              ) : (
                <div className="code-archdoc-metrics-table-wrap">
                  <table className="code-archdoc-metrics-table">
                    <thead>
                      <tr>
                        <th>章节标题</th>
                        <th>槽位标识 (slot_id)</th>
                        <th>统计样本</th>
                        <th>平均草稿→定稿修改率</th>
                        <th>Verifier 告警率</th>
                        <th>Prompt 治理诊断</th>
                      </tr>
                    </thead>
                    <tbody>
                      {heatmapSlots.map((slot) => {
                        const heat = slotHeatLevel(slot.avg_edit_diff_ratio);
                        const widthPct = Math.min(
                          100,
                          Math.max(4, Math.round(slot.avg_edit_diff_ratio * 100)),
                        );
                        return (
                          <tr key={slot.slot_id}>
                            <td>
                              <strong>{slot.title}</strong>
                            </td>
                            <td>
                              <code className="code-archdoc-mono">{slot.slot_id}</code>
                            </td>
                            <td>{slot.sample_count} 篇</td>
                            <td>
                              <div className="code-archdoc-metrics-heat-cell">
                                <div className="code-archdoc-metrics-heat-track">
                                  <div
                                    className={`code-archdoc-metrics-heat-fill ${heat.barClass}`}
                                    style={{ width: `${widthPct}%` }}
                                  />
                                </div>
                                <span className="code-archdoc-metrics-heat-val">
                                  {formatPercent(slot.avg_edit_diff_ratio)}
                                </span>
                              </div>
                            </td>
                            <td>{formatPercent(slot.verifier_warning_rate)}</td>
                            <td>
                              <StatusTag status={heat.tagStatus} text={heat.advice} size="sm" />
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </>
        )}
      </section>
    </div>
  );
};

export default QualityMetricsPage;
