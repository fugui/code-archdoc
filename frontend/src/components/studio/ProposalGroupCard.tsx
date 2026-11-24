import React, { useMemo, useState } from 'react';
import {
  AlertTriangle,
  BookOpen,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Edit3,
  Loader2,
  Lock,
  RefreshCw,
  ShieldAlert,
  X,
} from 'lucide-react';
import { StatusTag, type StatusType } from '@code/common';
import type {
  ApplyProposalItemSelection,
  ChatPlanSummaryVO,
  ChatProposalItemVO,
  ChatVerifierPreviewVO,
  ProposalStatus,
  RefDocCitationVO,
  SectionVO,
} from '../../types';
import { formatRefCitationBadgeLabel } from '../../utils/refDocUtils';
import SectionInlineEditor from './SectionInlineEditor';

export interface ProposalGroupCardProps {
  messageId: number;
  baseRevisionNo: number;
  currentRevisionNo: number;
  proposalStatus: ProposalStatus;
  planSummary?: ChatPlanSummaryVO | null;
  items: ChatProposalItemVO[];
  sectionsBySlot: ReadonlyMap<string, SectionVO>;
  verifierPreview?: ChatVerifierPreviewVO | null;
  canWrite: boolean;
  conflictMessage?: string;
  onApplyItems: (messageId: number, items: ApplyProposalItemSelection[]) => Promise<void>;
  onRejectItem: (itemId: number) => Promise<void>;
  onRejectAll?: (messageId: number) => Promise<void>;
  onReloadDocument: () => Promise<void>;
  onInspectRefCitation?: (citation: RefDocCitationVO) => void;
}

function statusMeta(status: ProposalStatus): { type: StatusType; label: string } {
  switch (status) {
    case 'accepted':
      return { type: 'success', label: '已采纳' };
    case 'partial_accepted':
      return { type: 'running', label: '部分采纳' };
    case 'rejected':
      return { type: 'failed', label: '已拒绝' };
    case 'superseded':
      return { type: 'neutral', label: '已被取代' };
    case 'pending':
      return { type: 'warning', label: '待审阅' };
    default:
      return { type: 'neutral', label: '无提案' };
  }
}

function classifyDiffLine(line: string): string {
  if (line.startsWith('@@')) return 'code-archdoc-diff-line code-archdoc-diff-line--hunk';
  if (line.startsWith('+')) return 'code-archdoc-diff-line code-archdoc-diff-line--add';
  if (line.startsWith('-')) return 'code-archdoc-diff-line code-archdoc-diff-line--del';
  return 'code-archdoc-diff-line';
}

export const ProposalGroupCard: React.FC<ProposalGroupCardProps> = ({
  messageId,
  baseRevisionNo,
  currentRevisionNo,
  proposalStatus,
  planSummary,
  items,
  sectionsBySlot,
  verifierPreview,
  canWrite,
  conflictMessage,
  onApplyItems,
  onRejectItem,
  onRejectAll,
  onReloadDocument,
  onInspectRefCitation,
}) => {
  const pendingItems = useMemo(
    () => items.filter((it) => it.status === 'pending'),
    [items],
  );
  const [selectedIds, setSelectedIds] = useState<number[]>(() =>
    pendingItems.map((it) => it.item_id),
  );
  const [editingItemId, setEditingItemId] = useState<number | null>(null);
  const [issuesExpanded, setIssuesExpanded] = useState<boolean>(false);
  const [submitting, setSubmitting] = useState<string | null>(null);

  const effectiveSelected = useMemo(() => {
    const pendingSet = new Set(pendingItems.map((it) => it.item_id));
    const filtered = selectedIds.filter((id) => pendingSet.has(id));
    return filtered.length > 0 ? filtered : pendingItems.map((it) => it.item_id);
  }, [pendingItems, selectedIds]);

  const st = statusMeta(proposalStatus);
  const hasPending = pendingItems.length > 0;
  const isStale = hasPending && baseRevisionNo > 0 && baseRevisionNo !== currentRevisionNo;
  const issueCount = verifierPreview?.issues?.length ?? 0;

  const toggleItem = (id: number) => {
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  };

  const handleApplySelected = async () => {
    if (!hasPending || effectiveSelected.length === 0 || submitting) return;
    setSubmitting('apply');
    try {
      await onApplyItems(
        messageId,
        effectiveSelected.map((id) => {
          const item = items.find((it) => it.item_id === id);
          return { item_id: id, slot_id: item?.slot_id };
        }),
      );
    } finally {
      setSubmitting(null);
    }
  };

  const handleRejectOne = async (itemId: number) => {
    if (submitting) return;
    setSubmitting(`reject-${itemId}`);
    try {
      await onRejectItem(itemId);
    } finally {
      setSubmitting(null);
    }
  };

  const handleRejectWhole = async () => {
    if (!onRejectAll || submitting) return;
    setSubmitting('reject-all');
    try {
      await onRejectAll(messageId);
    } finally {
      setSubmitting(null);
    }
  };

  return (
    <div className="code-archdoc-review-proposal-group">
      <div className="code-archdoc-diff-card-head">
        <div className="code-archdoc-proposal-head-left">
          <span className="code-archdoc-proposal-title">
            结构化修订提案 ({items.length} 节)
          </span>
          <StatusTag status={st.type} text={st.label} size="sm" />
        </div>
        <span className="text-muted">base v{baseRevisionNo}</span>
      </div>

      {planSummary?.skipped_locked_slots && planSummary.skipped_locked_slots.length > 0 && (
        <div className="code-archdoc-review-advisory-box">
          <Lock size={13} />
          <div>
            <strong>已跳过锁定章节（仅提供建议）：</strong>
            {planSummary.skipped_locked_slots.map((s) => `${s.title || s.slot_id}（${s.advice}）`).join('；')}
          </div>
        </div>
      )}

      {planSummary?.truncated_slots && planSummary.truncated_slots.length > 0 && (
        <div className="code-archdoc-review-truncated-note">
          单次最多精修 6 个章节，已延后：{planSummary.truncated_slots.join('、')}
        </div>
      )}

      <div className="code-archdoc-review-proposal-items">
        {items.map((it) => {
          const sec = sectionsBySlot.get(it.slot_id);
          const itemSt = statusMeta(it.status);
          const isItemPending = it.status === 'pending';
          const isEditing = editingItemId === it.item_id;
          const diffLines = it.unified_diff ? it.unified_diff.split('\n') : [];

          const editorSection: SectionVO = sec
            ? { ...sec, markdown: it.after_md }
            : {
                slot_id: it.slot_id,
                section_order: 1,
                title: it.slot_title || it.slot_id,
                ownership: 'hybrid',
                is_orphan: false,
                manually_edited: false,
                needs_drift_review: false,
                content_sha256: '',
                markdown: it.after_md,
                evidences: it.cited_anchors || [],
              };

          return (
            <div key={it.item_id} className="code-archdoc-diff-card code-archdoc-proposal-card">
              <div className="code-archdoc-diff-card-head">
                <div className="code-archdoc-proposal-head-left">
                  {isItemPending && items.length > 1 && (
                    <input
                      type="checkbox"
                      checked={effectiveSelected.includes(it.item_id)}
                      onChange={() => toggleItem(it.item_id)}
                      aria-label={`选择应用 ${it.slot_title || it.slot_id}`}
                    />
                  )}
                  <span className="code-archdoc-proposal-title">
                    {it.slot_title || sec?.title || it.slot_id}
                  </span>
                  <StatusTag status={itemSt.type} text={itemSt.label} size="sm" />
                </div>

                {isItemPending && canWrite && !isStale && (
                  <div className="flex-center gap-xs">
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      disabled={Boolean(submitting)}
                      onClick={() => setEditingItemId(isEditing ? null : it.item_id)}
                      title="手工微调提案内容并立即生效"
                    >
                      <Edit3 size={12} />
                      <span>{isEditing ? '取消微调' : '微调后应用'}</span>
                    </button>
                    {items.length > 1 && (
                      <button
                        type="button"
                        className="btn btn-text btn-small"
                        disabled={Boolean(submitting)}
                        onClick={() => void handleRejectOne(it.item_id)}
                        title="拒绝此项并退回关联批注为待处理"
                      >
                        <X size={12} />
                        <span>拒绝此项</span>
                      </button>
                    )}
                  </div>
                )}
              </div>

              {it.addressed_comment_ids && it.addressed_comment_ids.length > 0 && (
                <div className="code-archdoc-review-proposal-tags">
                  响应批注：
                  {it.addressed_comment_ids.map((cid) => (
                    <span key={cid} className="code-archdoc-mono">
                      #{cid}
                    </span>
                  ))}
                </div>
              )}

              {it.ref_citations && it.ref_citations.length > 0 && (
                <div className="code-archdoc-review-proposal-tags">
                  参考依据：
                  {it.ref_citations.map((cit) => (
                    <button
                      key={cit.citation_key}
                      type="button"
                      className={`code-archdoc-refcitation-chip ${
                        cit.stale ? 'code-archdoc-refcitation-chip--stale' : ''
                      }`}
                      onClick={() => onInspectRefCitation?.(cit)}
                    >
                      <BookOpen size={11} />
                      <span>{formatRefCitationBadgeLabel(cit)}</span>
                    </button>
                  ))}
                </div>
              )}

              {isEditing ? (
                <div className="code-archdoc-review-override-wrap">
                  <SectionInlineEditor
                    section={editorSection}
                    baseRevisionNo={currentRevisionNo}
                    onSave={async (payload) => {
                      await onApplyItems(messageId, [
                        {
                          item_id: it.item_id,
                          slot_id: it.slot_id,
                          override_markdown: payload.markdown,
                        },
                      ]);
                      setEditingItemId(null);
                    }}
                    onCancel={() => setEditingItemId(null)}
                    onReloadDocument={onReloadDocument}
                  />
                </div>
              ) : (
                <div className="code-archdoc-diff-lines">
                  {diffLines.length === 0 ? (
                    <div className="code-archdoc-diff-line">（与当前正文无文本差异）</div>
                  ) : (
                    diffLines.map((line, idx) => (
                      <div key={idx} className={classifyDiffLine(line)}>
                        {line || ' '}
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {verifierPreview && (
        <div className="code-archdoc-proposal-verifier">
          <div className="code-archdoc-proposal-verifier-row">
            {verifierPreview.can_approve ? (
              <span className="code-badge code-badge--success">
                <CheckCircle2 size={12} />
                <span>Verifier 预检通过（无阻断告警）</span>
              </span>
            ) : (
              <span className="code-badge code-badge--danger">
                <ShieldAlert size={12} />
                <span>
                  阻断 {verifierPreview.blocker_count} · 错误 {verifierPreview.error_count}
                </span>
              </span>
            )}

            {issueCount > 0 && (
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => setIssuesExpanded((prev) => !prev)}
              >
                {issuesExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <span>告警明细 ({issueCount})</span>
              </button>
            )}
          </div>

          {issuesExpanded && issueCount > 0 && (
            <ul className="code-archdoc-proposal-issues">
              {verifierPreview.issues.map((issue, idx) => (
                <li
                  key={idx}
                  className={`code-archdoc-proposal-issue code-archdoc-proposal-issue--${issue.severity}`}
                >
                  <AlertTriangle size={12} />
                  <span>{issue.message}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {(conflictMessage || isStale) && (
        <div className="code-archdoc-proposal-conflict">
          <AlertTriangle size={14} />
          <span>
            {conflictMessage ||
              `文档已更新至 v${currentRevisionNo}，当前提案基于 v${baseRevisionNo} 生成，请刷新或重发精修。`}
          </span>
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => void onReloadDocument()}
          >
            <RefreshCw size={12} />
            <span>刷新最新文档</span>
          </button>
        </div>
      )}

      {hasPending && canWrite && (
        <div className="code-archdoc-proposal-actions">
          {onRejectAll && (
            <button
              type="button"
              className="btn btn-secondary btn-small"
              disabled={Boolean(submitting)}
              onClick={() => void handleRejectWhole()}
            >
              {submitting === 'reject-all' ? (
                <Loader2 size={13} className="code-archdoc-spin" />
              ) : (
                <X size={13} />
              )}
              <span>全部拒绝</span>
            </button>
          )}

          <button
            type="button"
            className="btn btn-primary btn-small"
            disabled={Boolean(submitting) || isStale || effectiveSelected.length === 0}
            onClick={() => void handleApplySelected()}
          >
            {submitting === 'apply' ? (
              <Loader2 size={13} className="code-archdoc-spin" />
            ) : (
              <Check size={13} />
            )}
            <span>
              {items.length > 1
                ? `采纳所选 (${effectiveSelected.length}/${pendingItems.length})`
                : '采纳并应用'}
            </span>
          </button>
        </div>
      )}
    </div>
  );
};

export default ProposalGroupCard;
