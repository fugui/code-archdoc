import React from 'react';
import { Code2, History, Maximize2, MessageSquareText, Minimize2, X } from 'lucide-react';
import type {
  ChatIntentMode,
  DocumentDetailVO,
  ReviewCommentAnchor,
  ReviewCommentVO,
} from '../../types';
import ChatRefineTab from './ChatRefineTab';
import EvidenceSourceTab, { type EvidenceInspectRequest } from './EvidenceSourceTab';
import type { ReviewDraftContext } from './ReviewBasketPanel';
import RevisionHistoryTab from './RevisionHistoryTab';

export type RightDrawerTab = 'evidence' | 'history' | 'chat';

export interface RightUtilityDrawerProps {
  activeTab: RightDrawerTab;
  doc: DocumentDetailVO | null;
  evidenceRequest: EvidenceInspectRequest | null;
  chatTargetSlotId?: string;
  chatSubTab?: 'basket' | 'chat';
  chatMode?: ChatIntentMode;
  chatInlineAnchor?: { slotId: string; anchor: ReviewCommentAnchor; nonce: number } | null;
  reviewComments?: ReviewCommentVO[];
  activeCommentId?: number | null;
  reviewDraftContext?: ReviewDraftContext | null;
  wideDrawer?: boolean;
  onToggleWide?: () => void;
  onChangeTab: (tab: RightDrawerTab) => void;
  onChangeChatSubTab?: (sub: 'basket' | 'chat') => void;
  onClose: () => void;
  onRolledBack: (updatedDoc: DocumentDetailVO) => void;
  onReloadDocument: () => Promise<void>;
  onCommentsChanged?: () => Promise<void>;
  onSelectCommentOnCanvas?: (comment: ReviewCommentVO) => void;
  onClearDraftContext?: () => void;
  onAppliedProposal?: (appliedSlots: string[], commentIds: number[]) => void;
}

export const RightUtilityDrawer: React.FC<RightUtilityDrawerProps> = ({
  activeTab,
  doc,
  evidenceRequest,
  chatTargetSlotId,
  chatSubTab,
  chatMode,
  chatInlineAnchor,
  reviewComments = [],
  activeCommentId,
  reviewDraftContext,
  wideDrawer = false,
  onToggleWide,
  onChangeTab,
  onChangeChatSubTab,
  onClose,
  onRolledBack,
  onReloadDocument,
  onCommentsChanged,
  onSelectCommentOnCanvas,
  onClearDraftContext,
  onAppliedProposal,
}) => {
  const openCount = reviewComments.filter((c) => c.status === 'open').length;

  return (
    <aside className="code-archdoc-studio-right">
      <div className="code-archdoc-right-header">
        <div className="code-archdoc-right-tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'evidence'}
            className={`code-archdoc-right-tab ${
              activeTab === 'evidence' ? 'code-archdoc-right-tab--active' : ''
            }`}
            onClick={() => onChangeTab('evidence')}
          >
            <Code2 size={14} />
            <span>源码证据</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'history'}
            className={`code-archdoc-right-tab ${
              activeTab === 'history' ? 'code-archdoc-right-tab--active' : ''
            }`}
            onClick={() => onChangeTab('history')}
          >
            <History size={14} />
            <span>修订历史</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'chat'}
            className={`code-archdoc-right-tab ${
              activeTab === 'chat' ? 'code-archdoc-right-tab--active' : ''
            }`}
            onClick={() => onChangeTab('chat')}
          >
            <MessageSquareText size={14} />
            <span>审阅 &amp; 精修{openCount > 0 ? ` (${openCount})` : ''}</span>
          </button>
        </div>

        <div className="flex-center gap-xs">
          {onToggleWide && (
            <button
              type="button"
              className="btn btn-text btn-small"
              onClick={onToggleWide}
              title={wideDrawer ? '恢复标准抽屉宽度 (440px)' : '加宽至工作台宽屏视图 (680px)'}
              aria-label="切换右侧抽屉宽度"
            >
              {wideDrawer ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
            </button>
          )}
          <button
            type="button"
            className="btn btn-text btn-small"
            onClick={onClose}
            title="折叠右侧辅助面板"
            aria-label="折叠右侧面板"
          >
            <X size={15} />
          </button>
        </div>
      </div>

      <div className="code-archdoc-right-body">
        {activeTab === 'evidence' ? (
          <EvidenceSourceTab request={evidenceRequest} />
        ) : activeTab === 'chat' ? (
          doc ? (
            <ChatRefineTab
              doc={doc}
              initialSlotId={chatTargetSlotId}
              initialSubTab={chatSubTab}
              initialMode={chatMode}
              initialInlineAnchor={chatInlineAnchor}
              reviewComments={reviewComments}
              activeCommentId={activeCommentId}
              draftContext={reviewDraftContext}
              onChangeSubTab={onChangeChatSubTab}
              onCommentsChanged={onCommentsChanged}
              onSelectCommentOnCanvas={onSelectCommentOnCanvas}
              onClearDraftContext={onClearDraftContext}
              onAppliedProposal={onAppliedProposal}
              onDocumentUpdated={onRolledBack}
              onReloadDocument={onReloadDocument}
            />
          ) : null
        ) : doc ? (
          <RevisionHistoryTab doc={doc} onRolledBack={onRolledBack} />
        ) : null}
      </div>
    </aside>
  );
};

export default RightUtilityDrawer;
