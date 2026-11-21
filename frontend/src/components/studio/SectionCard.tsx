import React, { useRef, useState } from 'react';
import {
  AlertTriangle,
  Check,
  Code2,
  Edit3,
  Lock,
  MessageSquareText,
  Sparkles,
  UserCog,
} from 'lucide-react';
import type {
  EvidenceAnchor,
  ReviewCommentAnchor,
  ReviewCommentVO,
  SectionOwnership,
  SectionVO,
} from '../../types';
import { buildInlineAnchorFromText } from '../../utils/reviewAnchor';
import type { EvidenceDeepLinkParams } from '../../utils/urlQuery';
import SectionInlineEditor from './SectionInlineEditor';
import SectionMarkdownRenderer from './SectionMarkdownRenderer';
import SectionReviewOverlay from './SectionReviewOverlay';
import SelectionReviewPopover from './SelectionReviewPopover';

export interface SectionCardProps {
  section: SectionVO;
  baseRevisionNo: number;
  canWrite: boolean;
  reviewComments?: ReviewCommentVO[];
  activeCommentId?: number | null;
  onSaveSection: (
    slotId: string,
    payload: { markdown: string; ownership: SectionOwnership; changeSummary: string },
  ) => Promise<void>;
  onChangeOwnership: (
    slotId: string,
    ownership: SectionOwnership,
    clearDriftReview?: boolean,
  ) => Promise<void>;
  onReloadDocument: () => Promise<void>;
  onInspectEvidence: (anchor: EvidenceAnchor) => void;
  onInspectEvidenceLink: (params: { docId: number } & EvidenceDeepLinkParams) => void;
  onOpenChatRefine?: (slotId: string) => void;
  onAnnotateBlock?: (slotId: string, anchor: ReviewCommentAnchor) => void;
  onSelectReviewComment?: (comment: ReviewCommentVO) => void;
  onSelectionAction?: (
    action: 'comment' | 'refine' | 'ask',
    slotId: string,
    anchor: ReviewCommentAnchor,
  ) => void;
}

const OWNERSHIP_OPTIONS: Array<{ value: SectionOwnership; label: string; title: string }> = [
  { value: 'auto', label: 'AI 自动', title: '重生成时允许 AI 全量刷新本章节' },
  { value: 'hybrid', label: '人机协同', title: '保留人工修订意图并允许增量协同精修' },
  { value: 'locked', label: '人工锁定', title: '重生成与增量刷新时严格跳过并字节级保留本章节' },
];

export const SectionCard: React.FC<SectionCardProps> = ({
  section,
  baseRevisionNo,
  canWrite,
  reviewComments = [],
  activeCommentId = null,
  onSaveSection,
  onChangeOwnership,
  onReloadDocument,
  onInspectEvidence,
  onInspectEvidenceLink,
  onOpenChatRefine,
  onAnnotateBlock,
  onSelectReviewComment,
  onSelectionAction,
}) => {
  const [editing, setEditing] = useState<boolean>(false);
  const [updatingOwn, setUpdatingOwn] = useState<boolean>(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const [selPopover, setSelPopover] = useState<{
    anchor: ReviewCommentAnchor;
    position: { left: number; top: number };
  } | null>(null);

  const handleMouseUpSelection = () => {
    if (!canWrite || editing || !onSelectionAction) return;
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) {
      setSelPopover(null);
      return;
    }
    const quoteText = sel.toString().trim();
    if (!quoteText) {
      setSelPopover(null);
      return;
    }
    const bodyEl = bodyRef.current;
    if (!bodyEl) return;
    const range = sel.getRangeAt(0);
    const commonNode =
      range.commonAncestorContainer.nodeType === Node.ELEMENT_NODE
        ? (range.commonAncestorContainer as Element)
        : range.commonAncestorContainer.parentElement;
    if (!commonNode || !bodyEl.contains(commonNode)) {
      setSelPopover(null);
      return;
    }
    const blockEl = commonNode.closest<HTMLElement>('.code-archdoc-md-block');
    const blockIndex = blockEl ? Number.parseInt(blockEl.dataset.blockIndex || '0', 10) : 0;
    const blockType = blockEl?.dataset.blockType || 'paragraph';
    const lineStart = blockEl?.dataset.lineStart
      ? Number.parseInt(blockEl.dataset.lineStart, 10)
      : undefined;
    const lineEnd = blockEl?.dataset.lineEnd
      ? Number.parseInt(blockEl.dataset.lineEnd, 10)
      : undefined;
    const plain = (blockEl?.textContent || quoteText).replace(/\+\s*批注$/, '');
    const foundIdx = plain.indexOf(quoteText);
    const startOff = foundIdx >= 0 ? foundIdx : 0;
    const anchor = buildInlineAnchorFromText(
      plain,
      startOff,
      startOff + quoteText.length,
      blockIndex,
      blockType,
      lineStart,
      lineEnd,
    );
    const rect = range.getBoundingClientRect();
    const bodyRect = bodyEl.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left - bodyRect.left, bodyRect.width - 280));
    const top = Math.max(4, rect.top - bodyRect.top - 42);
    setSelPopover({ anchor, position: { left, top } });
  };

  const handleOwnershipClick = async (nextOwnership: SectionOwnership) => {
    if (!canWrite || nextOwnership === section.ownership || updatingOwn) return;
    setUpdatingOwn(true);
    try {
      await onChangeOwnership(section.slot_id, nextOwnership, false);
    } finally {
      setUpdatingOwn(false);
    }
  };

  const handleClearDriftReview = async () => {
    if (!canWrite || updatingOwn) return;
    setUpdatingOwn(true);
    try {
      await onChangeOwnership(section.slot_id, section.ownership, true);
    } finally {
      setUpdatingOwn(false);
    }
  };

  return (
    <article
      id={`section-${section.slot_id}`}
      className={`code-card code-archdoc-section-card ${
        section.ownership === 'locked' ? 'code-archdoc-section-card--locked' : ''
      }`}
    >
      <div className="code-archdoc-section-header">
        <div className="code-archdoc-section-title-group">
          <h2 className="code-archdoc-section-title">{section.title}</h2>
          <code className="code-archdoc-mono">{section.slot_id}</code>
          {section.manually_edited && (
            <span className="code-badge code-badge--purple">已手工审校</span>
          )}
          {section.needs_drift_review && (
            <span className="code-badge code-badge--warning">
              <AlertTriangle size={12} />
              <span>关联代码已变更·待核对</span>
            </span>
          )}
        </div>

        <div className="code-archdoc-section-controls">
          {canWrite && section.needs_drift_review && (
            <button
              type="button"
              className="btn btn-outline btn-small"
              disabled={updatingOwn}
              onClick={() => void handleClearDriftReview()}
              title="确认当前锁定章节内容仍准确无误，清除待核对提醒"
            >
              <Check size={13} />
              <span>标记已核对</span>
            </button>
          )}

          {/* 所有权切换器 auto / hybrid / locked */}
          <div className="code-archdoc-ownership-switch" role="group" aria-label="章节所有权模式">
            {OWNERSHIP_OPTIONS.map((opt) => {
              const isActive = section.ownership === opt.value;
              return (
                <button
                  key={opt.value}
                  type="button"
                  className={`code-archdoc-ownership-btn ${
                    isActive ? `code-archdoc-ownership-btn--${opt.value}` : ''
                  }`}
                  disabled={!canWrite || updatingOwn}
                  onClick={() => void handleOwnershipClick(opt.value)}
                  title={canWrite ? opt.title : '只读模式：仅负责人可切换章节所有权'}
                >
                  {opt.value === 'locked' ? (
                    <Lock size={11} />
                  ) : opt.value === 'hybrid' ? (
                    <UserCog size={11} />
                  ) : (
                    <Sparkles size={11} />
                  )}
                  <span>{opt.label}</span>
                </button>
              );
            })}
          </div>

          {canWrite && !editing && onOpenChatRefine && (
            <button
              type="button"
              className="btn btn-outline btn-small"
              disabled={section.ownership === 'locked'}
              onClick={() => onOpenChatRefine(section.slot_id)}
              title={
                section.ownership === 'locked'
                  ? '已锁定章节不可执行 AI 精修，请先切换所有权或改用手工直编'
                  : '在右侧面板针对当前章节发起 SSE Chat 局部精修'
              }
            >
              <MessageSquareText size={13} />
              <span>Chat 精修</span>
            </button>
          )}

          {canWrite && !editing && (
            <button
              type="button"
              className="btn btn-secondary btn-small"
              onClick={() => setEditing(true)}
            >
              <Edit3 size={13} />
              <span>手工直编</span>
            </button>
          )}
        </div>
      </div>

      <div
        ref={bodyRef}
        className="code-archdoc-section-body"
        onMouseUp={handleMouseUpSelection}
      >
        {!editing && reviewComments.length > 0 && (
          <SectionReviewOverlay
            comments={reviewComments}
            activeCommentId={activeCommentId}
            onSelectComment={(cm) => onSelectReviewComment?.(cm)}
          />
        )}

        {editing ? (
          <SectionInlineEditor
            section={section}
            baseRevisionNo={baseRevisionNo}
            onSave={async (payload) => {
              await onSaveSection(section.slot_id, payload);
              setEditing(false);
            }}
            onCancel={() => setEditing(false)}
            onReloadDocument={async () => {
              await onReloadDocument();
              setEditing(false);
            }}
          />
        ) : (
          <SectionMarkdownRenderer
            markdown={section.markdown}
            canWrite={canWrite}
            reviewComments={reviewComments}
            activeCommentId={activeCommentId}
            onOpenEvidenceLink={onInspectEvidenceLink}
            onAnnotateBlock={
              onAnnotateBlock ? (anchor) => onAnnotateBlock(section.slot_id, anchor) : undefined
            }
            onSelectComment={onSelectReviewComment}
          />
        )}

        {selPopover && onSelectionAction && (
          <SelectionReviewPopover
            visible={true}
            position={selPopover.position}
            quote={selPopover.anchor.quote || ''}
            ownership={section.ownership}
            canWrite={canWrite}
            onAddComment={() => {
              const a = selPopover.anchor;
              setSelPopover(null);
              onSelectionAction('comment', section.slot_id, a);
            }}
            onQuickRefine={() => {
              const a = selPopover.anchor;
              setSelPopover(null);
              onSelectionAction('refine', section.slot_id, a);
            }}
            onAskAI={() => {
              const a = selPopover.anchor;
              setSelPopover(null);
              onSelectionAction('ask', section.slot_id, a);
            }}
            onClose={() => setSelPopover(null)}
          />
        )}
      </div>

      {/* 章节证据锚点标签栏 */}
      {section.evidences && section.evidences.length > 0 && (
        <div className="code-archdoc-evidence-bar">
          <span className="code-archdoc-evidence-bar-label">
            <Code2 size={13} />
            <span>源码证据 ({section.evidences.length})</span>
          </span>
          <div className="code-archdoc-evidence-chips">
            {section.evidences.map((ev) => (
              <button
                key={ev.anchor_id || `${ev.file_path}-${ev.line_start}-${ev.fq_symbol}`}
                type="button"
                className={`code-archdoc-evidence-chip ${
                  ev.confidence === 'exact'
                    ? 'code-archdoc-evidence-chip--exact'
                    : 'code-archdoc-evidence-chip--heuristic'
                }`}
                onClick={() => onInspectEvidence(ev)}
                title={`${ev.fq_symbol || ev.file_path} (${ev.file_path}:${ev.line_start}-${ev.line_end}) · ${
                  ev.confidence === 'exact' ? '精确 AST 定位' : '启发式定位'
                }`}
              >
                <span className="code-archdoc-evidence-chip-file">{ev.file_path}</span>
                {ev.line_start > 0 && (
                  <span className="code-archdoc-evidence-chip-lines">
                    L{ev.line_start}
                    {ev.line_end > ev.line_start ? `-${ev.line_end}` : ''}
                  </span>
                )}
                {ev.fq_symbol && (
                  <span className="code-archdoc-evidence-chip-sym">{ev.fq_symbol}</span>
                )}
              </button>
            ))}
          </div>
        </div>
      )}
    </article>
  );
};

export default SectionCard;
