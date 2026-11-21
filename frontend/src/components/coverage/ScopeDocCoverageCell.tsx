import React from 'react';
import { AlertTriangle, ChevronDown, ChevronUp, ShieldAlert } from 'lucide-react';
import type { DocStatusSummaryVO, DocTypeSummaryVO, RepoScopeCoverageVO } from '../../types';
import { getDocTypeShortName, summarizeScopeDocs } from '../../utils/docTypeUtils';

export interface ScopeDocCoverageCellProps {
  row: RepoScopeCoverageVO;
  docTypes: DocTypeSummaryVO[];
  focusedDocTypeSlug?: string;
  expanded: boolean;
  onToggleExpand: () => void;
  onOpenStudio: (docId?: number) => void;
  onTriggerGenerate: (docTypeSlug?: string) => void;
}

interface PillVisualMeta {
  tone: 'success' | 'warning' | 'danger' | 'info';
  suffix: string;
  detail: string;
}

const MAX_INLINE_PILLS = 3;

function docPillMeta(ds: DocStatusSummaryVO): PillVisualMeta {
  const rev = ds.published_revision_no || ds.current_revision_no || 1;
  if (ds.status === 'approved') {
    if (ds.has_code_drift || ds.has_spec_drift) {
      return {
        tone: 'warning',
        suffix: `漂移(${ds.affected_slot_count || 1})`,
        detail: `已发布 v${rev} · 存在漂移（${ds.affected_slot_count || 1} 处）`,
      };
    }
    return {
      tone: 'success',
      suffix: `v${rev}`,
      detail: `已发布定稿 v${rev}`,
    };
  }
  if (!ds.can_approve) {
    return {
      tone: 'danger',
      suffix: '阻断',
      detail: `草稿 v${ds.current_revision_no} · Verifier 阻断`,
    };
  }
  return {
    tone: 'info',
    suffix: `草稿 v${ds.current_revision_no}`,
    detail: `草稿 v${ds.current_revision_no}`,
  };
}

export const ScopeDocCoverageCell: React.FC<ScopeDocCoverageCellProps> = ({
  row,
  docTypes,
  focusedDocTypeSlug,
  expanded,
  onToggleExpand,
  onOpenStudio,
  onTriggerGenerate,
}) => {
  const summary = summarizeScopeDocs(row, docTypes);
  const progressPct =
    summary.totalTypes > 0 ? Math.round((summary.generatedCount / summary.totalTypes) * 100) : 0;

  const focusedSlug = (focusedDocTypeSlug ?? '').trim();
  const focusedDocType = focusedSlug
    ? docTypes.find((dt) => dt.slug === focusedSlug)
    : undefined;
  const focusedStatus = focusedDocType ? row.doc_statuses?.[focusedDocType.slug] : undefined;

  const visibleGenerated = summary.generatedItems.slice(0, MAX_INLINE_PILLS);
  const overflowGeneratedCount = Math.max(0, summary.generatedItems.length - MAX_INLINE_PILLS);
  const totalCollapsedExtra = overflowGeneratedCount + summary.missingCount;

  return (
    <div className="code-archdoc-cov-cell">
      <div className="code-archdoc-cov-summary">
        <span className="code-archdoc-cov-ratio">
          <strong>{summary.generatedCount}</strong>
          <span className="text-muted"> / {summary.totalTypes}</span>
        </span>

        <div className="code-archdoc-cov-bar-track" aria-hidden="true">
          <div
            className={`code-archdoc-cov-bar-fill ${
              summary.generatedCount === summary.totalTypes && summary.totalTypes > 0
                ? 'code-archdoc-cov-bar-fill--full'
                : ''
            }`}
            style={{ width: `${progressPct}%` }}
          />
        </div>

        {summary.driftedCount > 0 && (
          <span
            className="code-archdoc-cov-alert code-archdoc-cov-alert--warning"
            title={`${summary.driftedCount} 篇已发布文档存在代码或模板漂移`}
          >
            <AlertTriangle size={11} />
            <span>{summary.driftedCount} 漂移</span>
          </span>
        )}

        {summary.blockedCount > 0 && (
          <span
            className="code-archdoc-cov-alert code-archdoc-cov-alert--danger"
            title={`${summary.blockedCount} 篇草稿被 Verifier 安全或结构门禁阻断`}
          >
            <ShieldAlert size={11} />
            <span>{summary.blockedCount} 阻断</span>
          </span>
        )}
      </div>

      <div className="code-archdoc-cov-pills">
        {focusedDocType ? (
          <>
            {focusedStatus ? (
              (() => {
                const meta = docPillMeta(focusedStatus);
                return (
                  <button
                    type="button"
                    className={`code-archdoc-doc-pill code-archdoc-doc-pill--${meta.tone}`}
                    onClick={() => onOpenStudio(focusedStatus.document_id)}
                    title={`${focusedDocType.display_name}：${meta.detail} — 点击进入工作台`}
                  >
                    <span className="code-archdoc-doc-pill-name">
                      {getDocTypeShortName(focusedDocType)}
                    </span>
                    <span className="code-archdoc-doc-pill-state">{meta.suffix}</span>
                  </button>
                );
              })()
            ) : (
              <button
                type="button"
                className="code-archdoc-doc-pill code-archdoc-doc-pill--missing"
                disabled={!row.can_write}
                onClick={() => onTriggerGenerate(focusedDocType.slug)}
                title={
                  row.can_write
                    ? `点击为当前范围触发生成《${focusedDocType.display_name}》`
                    : '尚未生成（仅仓库/子目录负责人或管理员可触发生成）'
                }
              >
                <span className="code-archdoc-doc-pill-name">
                  {getDocTypeShortName(focusedDocType)}
                </span>
                <span className="code-archdoc-doc-pill-state">未生成</span>
              </button>
            )}

            <button
              type="button"
              className={`code-archdoc-doc-expand-btn ${
                expanded ? 'code-archdoc-doc-expand-btn--active' : ''
              }`}
              onClick={onToggleExpand}
              aria-expanded={expanded}
            >
              <span>{expanded ? '收起全部' : `全部 ${summary.totalTypes} 类`}</span>
              {expanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
            </button>
          </>
        ) : (
          <>
            {visibleGenerated.map(({ docType, statusSummary }) => {
              const meta = docPillMeta(statusSummary);
              return (
                <button
                  key={docType.slug}
                  type="button"
                  className={`code-archdoc-doc-pill code-archdoc-doc-pill--${meta.tone}`}
                  onClick={() => onOpenStudio(statusSummary.document_id)}
                  title={`${docType.display_name}（${meta.detail}）— 点击进入工作台`}
                >
                  <span className="code-archdoc-doc-pill-name">
                    {getDocTypeShortName(docType)}
                  </span>
                  <span className="code-archdoc-doc-pill-state">{meta.suffix}</span>
                </button>
              );
            })}

            {summary.totalTypes > 0 && (
              <button
                type="button"
                className={`code-archdoc-doc-expand-btn ${
                  expanded ? 'code-archdoc-doc-expand-btn--active' : ''
                }`}
                onClick={onToggleExpand}
                aria-expanded={expanded}
                title={expanded ? '收起全量文档类型详情' : '展开查看全部文档类型与快捷生成入口'}
              >
                <span>
                  {expanded
                    ? '收起详情'
                    : summary.generatedCount === 0
                      ? `+${summary.missingCount} 待生成`
                      : totalCollapsedExtra > 0
                        ? `+${totalCollapsedExtra} ${summary.missingCount > 0 ? '待生成/更多' : '篇'}`
                        : '全部详情'}
                </span>
                {expanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
              </button>
            )}
          </>
        )}
      </div>
    </div>
  );
};

export default ScopeDocCoverageCell;
