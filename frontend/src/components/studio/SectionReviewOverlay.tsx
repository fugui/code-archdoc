import React from 'react';
import { MessageSquare } from 'lucide-react';
import type { ReviewCategory, ReviewCommentVO } from '../../types';

export interface SectionReviewOverlayProps {
  comments: ReviewCommentVO[];
  activeCommentId?: number | null;
  onSelectComment: (comment: ReviewCommentVO) => void;
}

const CATEGORY_LABEL: Record<ReviewCategory, string> = {
  correction: '纠错',
  supplement: '补充',
  concise: '精简',
  global_rule: '规范',
  question: '待核',
};

export const SectionReviewOverlay: React.FC<SectionReviewOverlayProps> = ({
  comments,
  activeCommentId,
  onSelectComment,
}) => {
  const visible = comments.filter((c) => c.status !== 'dismissed');
  if (visible.length === 0) return null;

  return (
    <div className="code-archdoc-review-overlay" aria-label="章节审阅批注角标栏">
      <span className="code-archdoc-review-overlay-label">
        <MessageSquare size={12} />
        <span>审阅批注 ({visible.length})</span>
      </span>
      <div className="code-archdoc-review-overlay-list">
        {visible.map((c) => {
          const isActive = c.id === activeCommentId;
          return (
            <button
              key={c.id}
              type="button"
              className={`code-archdoc-review-badge code-archdoc-review-badge--${c.status} ${
                isActive ? 'code-archdoc-review-badge--active' : ''
              }`}
              onClick={() => onSelectComment(c)}
              title={`#${c.comment_no} [${CATEGORY_LABEL[c.category]}] ${c.content}`}
            >
              <span>#{c.comment_no}</span>
              <span>{CATEGORY_LABEL[c.category]}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
};

export default SectionReviewOverlay;
