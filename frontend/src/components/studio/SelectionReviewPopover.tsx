import React from 'react';
import { HelpCircle, MessageSquarePlus, Sparkles, X } from 'lucide-react';
import type { SectionOwnership } from '../../types';

export interface SelectionReviewPopoverProps {
  visible: boolean;
  position: { left: number; top: number };
  quote: string;
  ownership?: SectionOwnership;
  canWrite: boolean;
  showCommentButton?: boolean;
  canAsk?: boolean;
  onAddComment?: () => void;
  onQuickRefine: () => void;
  onAskAI: () => void;
  onClose: () => void;
}

export const SelectionReviewPopover: React.FC<SelectionReviewPopoverProps> = ({
  visible,
  position,
  quote,
  ownership = 'auto',
  canWrite,
  showCommentButton = true,
  canAsk,
  onAddComment,
  onQuickRefine,
  onAskAI,
  onClose,
}) => {
  if (!visible) return null;
  const isLocked = ownership === 'locked';
  const effectiveCanAsk = canAsk ?? canWrite;

  return (
    <div
      className="code-archdoc-review-popover"
      style={{ left: `${position.left}px`, top: `${position.top}px` }}
      role="toolbar"
      aria-label="选区审阅快捷操作"
      onMouseDown={(e) => e.preventDefault()}
    >
      <span className="code-archdoc-review-popover-quote" title={quote}>
        “{quote.length > 26 ? `${quote.slice(0, 26)}…` : quote}”
      </span>

      {showCommentButton && canWrite && onAddComment && (
        <button
          type="button"
          className="btn btn-secondary btn-small"
          onClick={onAddComment}
          title="加入右侧审阅批注篮，统一调度多章节精修"
        >
          <MessageSquarePlus size={13} />
          <span>批注修改</span>
        </button>
      )}

      {canWrite && !isLocked && (
        <button
          type="button"
          className="btn btn-primary btn-small"
          onClick={onQuickRefine}
          title="针对该选区立刻发起局部精修"
        >
          <Sparkles size={13} />
          <span>立刻精修</span>
        </button>
      )}

      {effectiveCanAsk && (
        <button
          type="button"
          className="btn btn-secondary btn-small"
          onClick={onAskAI}
          title="只读追问文档细节（不修改正文）"
        >
          <HelpCircle size={13} />
          <span>问 AI</span>
        </button>
      )}

      <button
        type="button"
        className="btn btn-text btn-small"
        onClick={onClose}
        aria-label="关闭选区操作条"
      >
        <X size={13} />
      </button>
    </div>
  );
};

export default SelectionReviewPopover;
