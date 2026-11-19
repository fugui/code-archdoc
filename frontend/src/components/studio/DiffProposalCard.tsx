import React, { useState } from 'react';
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Loader2,
  RefreshCw,
  ShieldAlert,
  X,
} from 'lucide-react';
import { StatusTag, type StatusType } from '@code/common';
import type { ChatVerifierPreviewVO, ProposalStatus } from '../../types';

export interface DiffProposalCardProps {
  messageId: number;
  slotId: string;
  slotTitle?: string;
  baseRevisionNo: number;
  currentRevisionNo: number;
  unifiedDiff: string;
  proposedAfterMD: string;
  verifierPreview?: ChatVerifierPreviewVO | null;
  status: ProposalStatus;
  canWrite: boolean;
  conflictMessage?: string;
  onAccept: (messageId: number) => Promise<void>;
  onReject: (messageId: number) => Promise<void>;
  onReloadDocument: () => Promise<void>;
}

function proposalStatusTag(status: ProposalStatus): { type: StatusType; label: string } {
  switch (status) {
    case 'accepted':
      return { type: 'success', label: '已采纳' };
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

export const DiffProposalCard: React.FC<DiffProposalCardProps> = ({
  messageId,
  slotId,
  slotTitle,
  baseRevisionNo,
  currentRevisionNo,
  unifiedDiff,
  proposedAfterMD,
  verifierPreview,
  status,
  canWrite,
  conflictMessage,
  onAccept,
  onReject,
  onReloadDocument,
}) => {
  const [submitting, setSubmitting] = useState<'accept' | 'reject' | null>(null);
  const [issuesExpanded, setIssuesExpanded] = useState<boolean>(false);

  const tag = proposalStatusTag(status);
  const isPending = status === 'pending';
  const isStale = isPending && baseRevisionNo > 0 && baseRevisionNo !== currentRevisionNo;
  const diffLines = unifiedDiff ? unifiedDiff.split('\n') : [];

  const handleAccept = async () => {
    if (!isPending || submitting) return;
    setSubmitting('accept');
    try {
      await onAccept(messageId);
    } finally {
      setSubmitting(null);
    }
  };

  const handleReject = async () => {
    if (!isPending || submitting) return;
    setSubmitting('reject');
    try {
      await onReject(messageId);
    } finally {
      setSubmitting(null);
    }
  };

  return (
    <div className="code-archdoc-diff-card code-archdoc-proposal-card">
      <div className="code-archdoc-diff-card-head">
        <div className="code-archdoc-proposal-head-left">
          <span className="code-archdoc-proposal-title">{slotTitle || slotId}</span>
          <code className="code-archdoc-mono">{slotId}</code>
          <span className="code-badge code-badge--muted">基准 v{baseRevisionNo}</span>
        </div>
        <StatusTag status={tag.type} text={tag.label} size="sm" />
      </div>

      {/* Verifier 预校验预警条 */}
      {verifierPreview && (
        <div className="code-archdoc-proposal-verifier">
          <div className="code-archdoc-proposal-verifier-row">
            {verifierPreview.blocker_count > 0 ? (
              <span className="code-badge code-badge--danger">
                <ShieldAlert size={12} />
                <span>阻断告警 {verifierPreview.blocker_count}</span>
              </span>
            ) : verifierPreview.error_count > 0 ? (
              <span className="code-badge code-badge--danger">
                <AlertTriangle size={12} />
                <span>校验错误 {verifierPreview.error_count}</span>
              </span>
            ) : verifierPreview.warning_count > 0 ? (
              <span className="code-badge code-badge--warning">
                <AlertTriangle size={12} />
                <span>校验提醒 {verifierPreview.warning_count}</span>
              </span>
            ) : (
              <span className="code-badge code-badge--success">
                <CheckCircle2 size={12} />
                <span>Verifier 预检全绿</span>
              </span>
            )}

            {verifierPreview.issues.length > 0 && (
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={() => setIssuesExpanded((prev) => !prev)}
              >
                {issuesExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <span>查看 {verifierPreview.issues.length} 条明细</span>
              </button>
            )}
          </div>

          {issuesExpanded && verifierPreview.issues.length > 0 && (
            <ul className="code-archdoc-proposal-issues">
              {verifierPreview.issues.map((issue, idx) => (
                <li
                  key={`${issue.kind}-${idx}`}
                  className={`code-archdoc-proposal-issue code-archdoc-proposal-issue--${issue.severity}`}
                >
                  <span className="code-archdoc-mono">[{issue.severity}]</span>
                  <span>{issue.message}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {/* 红绿结构化 Unified Diff 视图 */}
      <div className="code-archdoc-diff-lines">
        {diffLines.length > 0 ? (
          diffLines.map((line, idx) => (
            <div key={idx} className={classifyDiffLine(line)}>
              {line || ' '}
            </div>
          ))
        ) : (
          <div className="code-archdoc-diff-line">
            {proposedAfterMD ? '（章节内容与当前版本一致，无行级差异）' : '（无变更内容）'}
          </div>
        )}
      </div>

      {/* 乐观锁版本冲突引导条 */}
      {(conflictMessage || isStale) && isPending && (
        <div className="code-archdoc-proposal-conflict">
          <AlertTriangle size={14} />
          <span>
            {conflictMessage ||
              `文档已更新至 v${currentRevisionNo}（本提案基于 v${baseRevisionNo}），直接采纳可能触发 409 冲突。`}
          </span>
          <button
            type="button"
            className="btn btn-outline btn-small"
            onClick={() => void onReloadDocument()}
          >
            <RefreshCw size={12} />
            <span>刷新画布</span>
          </button>
        </div>
      )}

      {/* 审阅操作区 */}
      {isPending && canWrite && (
        <div className="code-archdoc-proposal-actions">
          <button
            type="button"
            className="btn btn-outline btn-small"
            disabled={submitting !== null}
            onClick={() => void handleReject()}
          >
            {submitting === 'reject' ? (
              <Loader2 size={13} className="code-archdoc-spin" />
            ) : (
              <X size={13} />
            )}
            <span>拒绝</span>
          </button>

          <button
            type="button"
            className="btn btn-primary btn-small"
            disabled={submitting !== null}
            onClick={() => void handleAccept()}
          >
            {submitting === 'accept' ? (
              <Loader2 size={13} className="code-archdoc-spin" />
            ) : (
              <Check size={13} />
            )}
            <span>采纳提案 (Accept)</span>
          </button>
        </div>
      )}
    </div>
  );
};

export default DiffProposalCard;
