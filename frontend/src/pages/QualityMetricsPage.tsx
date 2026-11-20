import React, { useCallback, useEffect, useState } from 'react';
import {
  BarChart3,
  CheckCircle2,
  Cpu,
  Edit3,
  Layers,
  Loader2,
  MessageSquareCode,
  RefreshCw,
} from 'lucide-react';
import { EmptyState, StatusTag, useToast, type StatusType } from '@code/common';
import { listDocTypeRevisions } from '../api/admin';
import {
  compareMetricsRevisions,
  getMetricsOverview,
  getMetricsSlotHeatmap,
} from '../api/metrics';
import { listDocTypes } from '../api/repos';
import RevisionTrendChart from '../components/metrics/RevisionTrendChart';
import type {
  DocTypeRevisionSummaryVO,
  DocTypeSummaryVO,
  MetricsOverviewVO,
  RevisionMetricPointVO,
  SlotHeatmapItemVO,
} from '../types';
import { formatApiError } from '../utils/errorMessages';
import './QualityMetricsPage.css';

function formatPercent(v: number): string {
  return `${(v * 100).toFixed(1)}%`;
}

function formatTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return String(v);
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
  const { showToast } = useToast();

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

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">生成质量度量与修订纵向对比看板</h1>
          <p className="code-archdoc-page-subtitle">
            闭环度量首版 AI 草稿→定稿章节级修改率、Chat 局部精修采纳率、Verifier 四重门禁拦截率及 KV Cache 命中率，量化评估每次 DocType 模板调优收益。
          </p>
        </div>

        <div className="code-archdoc-page-actions">
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void loadMetrics()}
            disabled={loading}
          >
            <RefreshCw size={15} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新指标</span>
          </button>
        </div>
      </div>

      {/* 顶部筛选栏 */}
      <div className="code-card code-archdoc-metrics-filter">
        <div className="code-archdoc-metrics-filter-group">
          <label className="code-archdoc-metrics-filter-label" htmlFor="metrics-doctype-select">
            文档类型 (DocType)
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
            聚焦修订版本
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

      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在聚合质量度量与 Token 缓存指标...</span>
        </div>
      ) : (
        <>
          {/* 5 大核心 KPI 卡片 */}
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

            <div className="code-card code-archdoc-metrics-kpi">
              <div className="code-archdoc-metrics-kpi-top">
                <span className="code-archdoc-metrics-kpi-label">单篇 Token & KV Cache</span>
                <div className="code-archdoc-metrics-kpi-icon code-archdoc-stat-icon--primary">
                  <Cpu size={16} />
                </div>
              </div>
              <span className="code-archdoc-metrics-kpi-val">
                {formatTokens(overview.avg_tokens_per_doc)}
              </span>
              <span className="code-archdoc-metrics-kpi-sub">
                Prefix KV Cache 命中率 {formatPercent(overview.avg_cache_hit_ratio)}
              </span>
            </div>
          </div>

          {/* 原生轻量双主题 SVG 趋势图 */}
          <RevisionTrendChart docTypeSlug={selectedDocType} revisions={trendPoints} />

          {/* 槽位级修改率与告警热力表 */}
          <div className="code-card code-archdoc-metrics-heatmap-card">
            <div className="code-archdoc-metrics-heatmap-head">
              <div>
                <h2 className="code-archdoc-metrics-heatmap-title">
                  <BarChart3 size={17} /> 章节槽位修改率热力表（{selectedDocType}）
                </h2>
                <p className="code-archdoc-metrics-heatmap-sub">
                  按历史平均人工修改比例降序排列，快速识别需要补充 <code className="code-archdoc-mono">instruction_hint</code> 或优化 <code className="code-archdoc-mono">ir_facets</code> 的薄弱槽位
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
                      const widthPct = Math.min(100, Math.max(4, Math.round(slot.avg_edit_diff_ratio * 100)));
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
    </div>
  );
};

export default QualityMetricsPage;
