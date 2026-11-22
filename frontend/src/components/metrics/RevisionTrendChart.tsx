import React, { useMemo, useState } from 'react';
import { EmptyState } from '@code/common';
import type { RevisionMetricPointVO } from '../../types';
import { formatDateTime } from '../../utils/urlQuery';
import './RevisionTrendChart.css';

interface RevisionTrendChartProps {
  docTypeSlug: string;
  revisions: RevisionMetricPointVO[];
}

const VIEW_WIDTH = 760;
const VIEW_HEIGHT = 270;
const PAD_LEFT = 52;
const PAD_RIGHT = 24;
const PAD_TOP = 22;
const PAD_BOTTOM = 44;
const PLOT_WIDTH = VIEW_WIDTH - PAD_LEFT - PAD_RIGHT;
const PLOT_HEIGHT = VIEW_HEIGHT - PAD_TOP - PAD_BOTTOM;

function formatPercent(v: number): string {
  return `${(v * 100).toFixed(1)}%`;
}

function buildPolylinePoints(coords: Array<{ x: number; y: number }>): string {
  return coords.map((pt) => `${pt.x.toFixed(1)},${pt.y.toFixed(1)}`).join(' ');
}

export const RevisionTrendChart: React.FC<RevisionTrendChartProps> = ({
  docTypeSlug,
  revisions,
}) => {
  const [selectedIdx, setSelectedIdx] = useState<number>(Math.max(revisions.length - 1, 0));

  const effectiveIdx = useMemo(() => {
    if (revisions.length === 0) return -1;
    if (selectedIdx >= 0 && selectedIdx < revisions.length) {
      return selectedIdx;
    }
    return revisions.length - 1;
  }, [revisions.length, selectedIdx]);

  const chartData = useMemo(() => {
    if (revisions.length === 0) {
      return null;
    }

    const stepX = revisions.length > 1 ? PLOT_WIDTH / (revisions.length - 1) : 0;
    const xAt = (idx: number): number =>
      revisions.length === 1 ? PAD_LEFT + PLOT_WIDTH / 2 : PAD_LEFT + idx * stepX;

    const yPct = (ratio: number): number => {
      const clamped = Math.max(0, Math.min(1, ratio));
      return PAD_TOP + (1 - clamped) * PLOT_HEIGHT;
    };

    const hitBandWidth = Math.min(48, Math.max(24, PLOT_WIDTH / Math.max(revisions.length * 2.5, 4)));

    const editPts = revisions.map((r, i) => ({ x: xAt(i), y: yPct(r.avg_edit_diff_ratio) }));
    const chatPts = revisions.map((r, i) => ({ x: xAt(i), y: yPct(r.chat_accept_rate) }));
    const blockPts = revisions.map((r, i) => ({ x: xAt(i), y: yPct(r.verifier_block_rate) }));

    return {
      hitBandWidth,
      xAt,
      yPct,
      editPts,
      chatPts,
      blockPts,
    };
  }, [revisions]);

  if (!chartData || revisions.length === 0) {
    return (
      <div className="code-card code-archdoc-trend-card">
        <div className="code-archdoc-trend-head">
          <div className="code-archdoc-trend-title-wrap">
            <h2 className="code-archdoc-trend-title">DocTypeRevision 质量演进趋势</h2>
            <p className="code-archdoc-trend-subtitle">
              按模板修订版本纵向追踪修改率下降与采纳率提升
            </p>
          </div>
        </div>
        <EmptyState
          title="暂无可对比的修订度量数据"
          description={`文档类型 ${docTypeSlug || '-'} 在所选时间窗口内尚无修订快照样本。`}
        />
      </div>
    );
  }

  const activeRev = effectiveIdx >= 0 ? revisions[effectiveIdx] : null;
  const yTicks = [0, 0.25, 0.5, 0.75, 1];

  return (
    <div className="code-card code-archdoc-trend-card">
      <div className="code-archdoc-trend-head">
        <div className="code-archdoc-trend-title-wrap">
          <h2 className="code-archdoc-trend-title">
            DocTypeRevision 质量演进趋势（{docTypeSlug}）
          </h2>
          <p className="code-archdoc-trend-subtitle">
            点击或悬浮各修订节点查看精确指标
          </p>
        </div>

        <div className="code-archdoc-trend-legend" aria-label="趋势图图例">
          <span className="code-archdoc-trend-legend-item">
            <span className="code-archdoc-trend-swatch code-archdoc-trend-swatch--edit" />
            <span>修改率</span>
          </span>
          <span className="code-archdoc-trend-legend-item">
            <span className="code-archdoc-trend-swatch code-archdoc-trend-swatch--chat" />
            <span>Chat 采纳率</span>
          </span>
          <span className="code-archdoc-trend-legend-item">
            <span className="code-archdoc-trend-swatch code-archdoc-trend-swatch--block" />
            <span>Verifier 拦截率</span>
          </span>
        </div>
      </div>

      <div className="code-archdoc-trend-svg-wrap">
        <svg
          className="code-archdoc-trend-svg"
          viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
          role="img"
          aria-label={`${docTypeSlug} 修订版本质量指标对比图`}
        >
          {/* 网格线与纵轴刻度 */}
          {yTicks.map((tick) => {
            const y = chartData.yPct(tick);
            return (
              <g key={tick}>
                <line
                  x1={PAD_LEFT}
                  y1={y}
                  x2={VIEW_WIDTH - PAD_RIGHT}
                  y2={y}
                  className={tick === 0 ? 'code-archdoc-trend-axis-line' : 'code-archdoc-trend-grid-line'}
                />
                <text
                  x={PAD_LEFT - 8}
                  y={y + 4}
                  textAnchor="end"
                  className="code-archdoc-trend-axis-label"
                >
                  {`${Math.round(tick * 100)}%`}
                </text>
              </g>
            );
          })}

          {/* 选中列高亮 */}
          {effectiveIdx >= 0 && (
            <rect
              x={chartData.xAt(effectiveIdx) - chartData.hitBandWidth * 0.65}
              y={PAD_TOP}
              width={chartData.hitBandWidth * 1.3}
              height={PLOT_HEIGHT}
              rx={6}
              className="code-archdoc-trend-highlight-col"
            />
          )}

          {/* 三条核心百分比折线 */}
          {revisions.length > 1 && (
            <>
              <polyline
                points={buildPolylinePoints(chartData.editPts)}
                className="code-archdoc-trend-line code-archdoc-trend-line--edit"
              />
              <polyline
                points={buildPolylinePoints(chartData.chatPts)}
                className="code-archdoc-trend-line code-archdoc-trend-line--chat"
              />
              <polyline
                points={buildPolylinePoints(chartData.blockPts)}
                className="code-archdoc-trend-line code-archdoc-trend-line--block"
              />
            </>
          )}

          {/* 节点圆点、X 轴标签与交互点击热区 */}
          {revisions.map((rev, idx) => {
            const cx = chartData.xAt(idx);
            const r = idx === effectiveIdx ? 5.5 : 4;
            return (
              <g key={`pt-${rev.doc_type_revision_id}`}>
                <circle
                  cx={cx}
                  cy={chartData.editPts[idx].y}
                  r={r}
                  className="code-archdoc-trend-dot code-archdoc-trend-dot--edit"
                />
                <circle
                  cx={cx}
                  cy={chartData.chatPts[idx].y}
                  r={r}
                  className="code-archdoc-trend-dot code-archdoc-trend-dot--chat"
                />
                <circle
                  cx={cx}
                  cy={chartData.blockPts[idx].y}
                  r={r}
                  className="code-archdoc-trend-dot code-archdoc-trend-dot--block"
                />
                <text
                  x={cx}
                  y={VIEW_HEIGHT - 16}
                  textAnchor="middle"
                  className="code-archdoc-trend-x-label"
                >
                  {`Rev ${rev.revision_no}`}
                </text>
                <rect
                  x={cx - chartData.hitBandWidth}
                  y={PAD_TOP}
                  width={chartData.hitBandWidth * 2}
                  height={PLOT_HEIGHT + 28}
                  className="code-archdoc-trend-hit"
                  onMouseEnter={() => setSelectedIdx(idx)}
                  onClick={() => setSelectedIdx(idx)}
                />
              </g>
            );
          })}
        </svg>
      </div>

      {/* 当前选中修订精确读数栏 */}
      {activeRev && (
        <div className="code-archdoc-trend-inspector">
          <div className="code-archdoc-trend-insp-cell">
            <span className="code-archdoc-trend-insp-label">当前检视修订</span>
            <span className="code-archdoc-trend-insp-val">
              Rev {activeRev.revision_no}（样本 {activeRev.sample_count}）
            </span>
          </div>
          <div className="code-archdoc-trend-insp-cell">
            <span className="code-archdoc-trend-insp-label">草稿→定稿修改率</span>
            <span className="code-archdoc-trend-insp-val">
              {formatPercent(activeRev.avg_edit_diff_ratio)}
            </span>
          </div>
          <div className="code-archdoc-trend-insp-cell">
            <span className="code-archdoc-trend-insp-label">Chat 采纳率</span>
            <span className="code-archdoc-trend-insp-val">
              {formatPercent(activeRev.chat_accept_rate)}
            </span>
          </div>
          <div className="code-archdoc-trend-insp-cell">
            <span className="code-archdoc-trend-insp-label">Verifier 拦截率</span>
            <span className="code-archdoc-trend-insp-val">
              {formatPercent(activeRev.verifier_block_rate)}
            </span>
          </div>
          <div className="code-archdoc-trend-insp-cell">
            <span className="code-archdoc-trend-insp-label">创建时间</span>
            <span className="code-archdoc-trend-insp-val">
              {formatDateTime(activeRev.created_at)}
            </span>
          </div>
        </div>
      )}
    </div>
  );
};

export default RevisionTrendChart;
