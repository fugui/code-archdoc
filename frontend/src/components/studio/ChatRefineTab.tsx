import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useToast } from '@code/common';
import {
  acceptChatProposal,
  applyChatProposalItems,
  listChatSessions,
  rejectChatProposal,
  rejectChatProposalItem,
  streamChatRefine,
} from '../../api/chat';
import type {
  ApplyProposalItemSelection,
  ChatIntentMode,
  ChatScopeType,
  ChatSessionWithMessagesVO,
  DocumentDetailVO,
  ReviewCommentAnchor,
  ReviewCommentVO,
} from '../../types';
import { ArchdocApiError, formatApiError } from '../../utils/errorMessages';
import ChatRefineThreadPanel, { type ActiveThreadStreamingState } from './ChatRefineThreadPanel';
import ReviewBasketPanel, { type ReviewDraftContext } from './ReviewBasketPanel';

export interface ChatRefineTabProps {
  doc: DocumentDetailVO;
  initialSlotId?: string;
  initialSubTab?: 'basket' | 'chat';
  initialMode?: ChatIntentMode;
  initialInlineAnchor?: { slotId: string; anchor: ReviewCommentAnchor; nonce: number } | null;
  reviewComments?: ReviewCommentVO[];
  activeCommentId?: number | null;
  draftContext?: ReviewDraftContext | null;
  onChangeSubTab?: (sub: 'basket' | 'chat') => void;
  onCommentsChanged?: () => Promise<void>;
  onSelectCommentOnCanvas?: (comment: ReviewCommentVO) => void;
  onClearDraftContext?: () => void;
  onAppliedProposal?: (appliedSlots: string[], commentIds: number[]) => void;
  onDocumentUpdated: (updatedDoc: DocumentDetailVO) => void;
  onReloadDocument: () => Promise<void>;
}

export const ChatRefineTab: React.FC<ChatRefineTabProps> = ({
  doc,
  initialSlotId,
  initialSubTab = 'chat',
  initialMode = 'revise',
  initialInlineAnchor = null,
  reviewComments = [],
  activeCommentId = null,
  draftContext = null,
  onChangeSubTab,
  onCommentsChanged,
  onSelectCommentOnCanvas,
  onClearDraftContext,
  onAppliedProposal,
  onDocumentUpdated,
  onReloadDocument,
}) => {
  const { showToast } = useToast();

  const regularSections = useMemo(
    () => doc.sections.filter((s) => !s.is_orphan),
    [doc.sections],
  );

  const [subTab, setSubTab] = useState<'basket' | 'chat'>(initialSubTab);
  const [mode, setMode] = useState<ChatIntentMode>(initialMode);
  const [scopeType, setScopeType] = useState<ChatScopeType>('slot');
  const [selectedSlotId, setSelectedSlotId] = useState<string>(() => {
    if (initialSlotId && regularSections.some((s) => s.slot_id === initialSlotId)) {
      return initialSlotId;
    }
    const firstUnlocked = regularSections.find((s) => s.ownership !== 'locked');
    return firstUnlocked?.slot_id ?? regularSections[0]?.slot_id ?? '';
  });
  const [inlineAnchor, setInlineAnchor] = useState<ReviewCommentAnchor | null>(
    initialInlineAnchor?.anchor ?? null,
  );

  const [sessions, setSessions] = useState<ChatSessionWithMessagesVO[]>([]);
  const [activeSessionId, setActiveSessionId] = useState<number | undefined>(undefined);
  const [loadingSessions, setLoadingSessions] = useState<boolean>(false);
  const [streaming, setStreaming] = useState<ActiveThreadStreamingState | null>(null);
  const [conflictByMsgId, setConflictByMsgId] = useState<Record<number, string>>({});

  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    setSubTab(initialSubTab);
  }, [initialSubTab]);

  useEffect(() => {
    setMode(initialMode);
  }, [initialMode]);

  useEffect(() => {
    if (initialSlotId && regularSections.some((s) => s.slot_id === initialSlotId)) {
      setSelectedSlotId(initialSlotId);
    }
  }, [initialSlotId, regularSections]);

  useEffect(() => {
    if (!initialInlineAnchor) return;
    setSelectedSlotId(initialInlineAnchor.slotId);
    setInlineAnchor(initialInlineAnchor.anchor);
    setScopeType('inline');
  }, [initialInlineAnchor]);

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
    };
  }, []);

  const loadSessions = useCallback(async () => {
    if (!doc.can_write) return;
    setLoadingSessions(true);
    try {
      const list = await listChatSessions(doc.id);
      setSessions(list);
      if (list.length > 0) {
        setActiveSessionId((prev) =>
          prev && list.some((s) => s.id === prev) ? prev : list[0].id,
        );
      } else {
        setActiveSessionId(undefined);
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载精修对话历史失败').message, 'error');
    } finally {
      setLoadingSessions(false);
    }
  }, [doc.id, doc.can_write, showToast]);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  const switchSubTab = (next: 'basket' | 'chat') => {
    setSubTab(next);
    onChangeSubTab?.(next);
  };

  const runSSE = async (params: {
    reqMode: ChatIntentMode;
    reqScope: ChatScopeType;
    reqSlotId: string;
    reqPrompt: string;
    commentIds?: number[];
    sessionOverride?: number;
  }) => {
    if (streaming || !doc.can_write) return;
    const controller = new AbortController();
    abortRef.current = controller;

    setStreaming({
      sessionId: params.sessionOverride,
      mode: params.reqMode,
      scopeType: params.reqScope,
      slotId: params.reqSlotId,
      baseRevisionNo: doc.current_revision_no,
      toolSteps: [],
      deltaText: '',
      proposalItems: [],
    });

    try {
      await streamChatRefine({
        docId: doc.id,
        signal: controller.signal,
        payload: {
          session_id: params.sessionOverride,
          mode: params.reqMode,
          scope_type: params.reqScope,
          target_slot_id:
            params.reqScope === 'slot' || params.reqScope === 'inline' ? params.reqSlotId : '',
          base_revision_no: doc.current_revision_no,
          prompt: params.reqPrompt,
          comment_ids: params.commentIds,
          inline_anchor:
            params.reqScope === 'inline' && inlineAnchor ? inlineAnchor : undefined,
        },
        onEvent: (ev) => {
          setStreaming((prev) => {
            if (!prev) return prev;
            switch (ev.event) {
              case 'meta':
                return {
                  ...prev,
                  sessionId: ev.data.session_id,
                  messageId: ev.data.message_id,
                  baseRevisionNo: ev.data.base_revision_no,
                  modelTier: ev.data.model_tier,
                };
              case 'plan':
                return {
                  ...prev,
                  planSummary: ev.data,
                };
              case 'stage':
                return {
                  ...prev,
                  stage: ev.data.stage,
                  stageSlotId: ev.data.slot_id,
                  stageIndex: ev.data.index,
                  stageTotal: ev.data.total,
                };
              case 'tool_call':
                return {
                  ...prev,
                  toolSteps: [
                    ...prev.toolSteps,
                    `R${ev.data.round} · ${ev.data.tool_name} (${ev.data.summary})`,
                  ],
                };
              case 'delta':
                return {
                  ...prev,
                  deltaText: `${prev.deltaText}${ev.data.text}`,
                };
              case 'proposal_item':
                return {
                  ...prev,
                  proposalItems: [...prev.proposalItems, ev.data],
                };
              case 'diff_proposal':
                return {
                  ...prev,
                  diffProposal: {
                    proposalId: ev.data.proposal_id,
                    slotId: ev.data.slot_id,
                    beforeMD: ev.data.before_md,
                    afterMD: ev.data.after_md,
                    unifiedDiff: ev.data.unified_diff,
                  },
                };
              case 'verifier_preview':
                return {
                  ...prev,
                  verifierPreview: ev.data,
                };
              case 'done':
                setActiveSessionId(prev.sessionId);
                return {
                  ...prev,
                  usageSummary: `输入 ${ev.data.prompt_tokens} · 输出 ${ev.data.completion_tokens} tokens · ${ev.data.latency_ms}ms`,
                };
              case 'error':
                showToast(ev.data.message || '请求执行失败', 'error');
                return prev;
            }
          });
        },
      });
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        showToast('已取消当前 AI 推理连接', 'info');
      } else {
        showToast(formatApiError(err, 'SSE 流式请求失败').message, 'error');
      }
    } finally {
      abortRef.current = null;
      setStreaming(null);
      await loadSessions();
      if (onCommentsChanged) {
        await onCommentsChanged();
      }
    }
  };

  const handleSendPrompt = (promptText: string) => {
    void runSSE({
      reqMode: mode,
      reqScope: scopeType,
      reqSlotId: selectedSlotId,
      reqPrompt: promptText,
      sessionOverride: activeSessionId,
    });
  };

  const handleSubmitBatchRefine = (commentIds: number[], extraPrompt: string) => {
    if (commentIds.length === 0) return;
    switchSubTab('chat');
    setMode('revise');
    setScopeType('review_batch');
    void runSSE({
      reqMode: 'revise',
      reqScope: 'review_batch',
      reqSlotId: '',
      reqPrompt: extraPrompt.trim(),
      commentIds,
      sessionOverride: undefined,
    });
  };

  const handleApplyItems = async (
    messageId: number,
    items: ApplyProposalItemSelection[],
  ) => {
    try {
      const updated = await applyChatProposalItems(doc.id, messageId, {
        base_revision_no: doc.current_revision_no,
        items,
      });
      onDocumentUpdated(updated);
      setConflictByMsgId((prev) => {
        const next = { ...prev };
        delete next[messageId];
        return next;
      });
      showToast('已采纳所选提案并生成新修订版本', 'success');

      // 通知外层检查是否命中 auto 槽位 + supplement 批注，提示快捷切换为 hybrid
      const msg = sessions.flatMap((s) => s.messages).find((m) => m.id === messageId);
      const selectedSet = new Set(items.map((it) => it.item_id));
      const matchedItems = (msg?.proposal_items ?? []).filter((it) =>
        selectedSet.size === 0 ? it.status === 'pending' : selectedSet.has(it.item_id),
      );
      const appliedSlots = matchedItems.map((it) => it.slot_id);
      const commentIds = matchedItems.flatMap((it) => it.addressed_comment_ids || []);
      onAppliedProposal?.(appliedSlots, commentIds);

      await loadSessions();
      if (onCommentsChanged) {
        await onCommentsChanged();
      }
    } catch (err: unknown) {
      if (err instanceof ArchdocApiError && err.code === 'OPTIMISTIC_LOCK_CONFLICT') {
        setConflictByMsgId((prev) => ({ ...prev, [messageId]: err.message }));
      }
      showToast(formatApiError(err, '采纳提案失败').message, 'error');
      throw err;
    }
  };

  const handleAcceptLegacy = async (messageId: number) => {
    try {
      const updated = await acceptChatProposal(doc.id, messageId, {
        base_revision_no: doc.current_revision_no,
      });
      onDocumentUpdated(updated);
      showToast('已采纳提案并生成新修订版本', 'success');
      await loadSessions();
      if (onCommentsChanged) {
        await onCommentsChanged();
      }
    } catch (err: unknown) {
      if (err instanceof ArchdocApiError && err.code === 'OPTIMISTIC_LOCK_CONFLICT') {
        setConflictByMsgId((prev) => ({ ...prev, [messageId]: err.message }));
      }
      showToast(formatApiError(err, '采纳提案失败').message, 'error');
    }
  };

  const handleRejectAll = async (messageId: number) => {
    try {
      await rejectChatProposal(doc.id, messageId);
      showToast('已拒绝提案并将关联批注退回待处理', 'info');
      await loadSessions();
      if (onCommentsChanged) {
        await onCommentsChanged();
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '拒绝提案失败').message, 'error');
    }
  };

  const handleRejectItem = async (itemId: number) => {
    try {
      await rejectChatProposalItem(doc.id, itemId);
      showToast('已拒绝该槽位提案并将关联批注退回待处理', 'info');
      await loadSessions();
      if (onCommentsChanged) {
        await onCommentsChanged();
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '拒绝提案项失败').message, 'error');
    }
  };

  return (
    <div className="code-archdoc-review-drawer-inner">
      {subTab === 'basket' ? (
        <ReviewBasketPanel
          doc={doc}
          comments={reviewComments}
          activeCommentId={activeCommentId}
          draftContext={draftContext}
          submittingBatch={Boolean(streaming)}
          onCommentsChanged={async () => {
            if (onCommentsChanged) await onCommentsChanged();
          }}
          onSelectCommentOnCanvas={(c) => onSelectCommentOnCanvas?.(c)}
          onSubmitBatchRefine={handleSubmitBatchRefine}
          onClearDraftContext={() => onClearDraftContext?.()}
        />
      ) : (
        <ChatRefineThreadPanel
          doc={doc}
          regularSections={regularSections}
          sessions={sessions}
          activeSessionId={activeSessionId}
          loadingSessions={loadingSessions}
          mode={mode}
          scopeType={scopeType}
          selectedSlotId={selectedSlotId}
          inlineAnchor={inlineAnchor}
          streaming={streaming}
          conflictByMsgId={conflictByMsgId}
          onChangeMode={setMode}
          onChangeScopeType={(sc) => {
            setScopeType(sc);
            if (sc !== 'inline') {
              setInlineAnchor(null);
            }
          }}
          onChangeSlotId={setSelectedSlotId}
          onClearInlineAnchor={() => {
            setInlineAnchor(null);
            setScopeType('slot');
          }}
          onSelectSession={setActiveSessionId}
          onReloadSessions={loadSessions}
          onSendPrompt={handleSendPrompt}
          onStopStreaming={() => abortRef.current?.abort()}
          onApplyItems={handleApplyItems}
          onAcceptLegacyProposal={handleAcceptLegacy}
          onRejectProposal={handleRejectAll}
          onRejectProposalItem={handleRejectItem}
          onReloadDocument={onReloadDocument}
        />
      )}
    </div>
  );
};

export default ChatRefineTab;
