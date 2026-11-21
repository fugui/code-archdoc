import React, { useMemo, useState } from 'react';
import {
  AlertTriangle,
  HelpCircle,
  Loader2,
  Lock,
  MessageSquareText,
  RefreshCw,
  Send,
  Sparkles,
  Square,
  X,
} from 'lucide-react';
import { EmptyState } from '@code/common';
import type {
  ApplyProposalItemSelection,
  ChatIntentMode,
  ChatMessageVO,
  ChatPlanSummaryVO,
  ChatProposalItemVO,
  ChatScopeType,
  ChatSessionWithMessagesVO,
  ChatVerifierPreviewVO,
  DocumentDetailVO,
  ReviewCommentAnchor,
  SectionVO,
} from '../../types';
import type { ChatRefineStageName } from '../../types/sse';
import DiffProposalCard from './DiffProposalCard';
import ProposalGroupCard from './ProposalGroupCard';

export interface ActiveThreadStreamingState {
  sessionId?: number;
  messageId?: number;
  mode: ChatIntentMode;
  scopeType: ChatScopeType;
  slotId: string;
  baseRevisionNo: number;
  modelTier?: string;
  stage?: ChatRefineStageName;
  stageSlotId?: string;
  stageIndex?: number;
  stageTotal?: number;
  planSummary?: ChatPlanSummaryVO;
  toolSteps: string[];
  deltaText: string;
  proposalItems: ChatProposalItemVO[];
  diffProposal?: {
    proposalId: number;
    slotId: string;
    beforeMD: string;
    afterMD: string;
    unifiedDiff: string;
  };
  verifierPreview?: ChatVerifierPreviewVO;
  usageSummary?: string;
}

export interface ChatRefineThreadPanelProps {
  doc: DocumentDetailVO;
  regularSections: SectionVO[];
  sessions: ChatSessionWithMessagesVO[];
  activeSessionId?: number;
  loadingSessions: boolean;
  mode: ChatIntentMode;
  scopeType: ChatScopeType;
  selectedSlotId: string;
  inlineAnchor?: ReviewCommentAnchor | null;
  streaming: ActiveThreadStreamingState | null;
  conflictByMsgId: Record<number, string>;
  onChangeMode: (mode: ChatIntentMode) => void;
  onChangeScopeType: (scope: ChatScopeType) => void;
  onChangeSlotId: (slotId: string) => void;
  onClearInlineAnchor: () => void;
  onSelectSession: (sessionId?: number) => void;
  onReloadSessions: () => Promise<void>;
  onSendPrompt: (promptText: string) => void;
  onStopStreaming: () => void;
  onApplyItems: (messageId: number, items: ApplyProposalItemSelection[]) => Promise<void>;
  onAcceptLegacyProposal: (messageId: number) => Promise<void>;
  onRejectProposal: (messageId: number) => Promise<void>;
  onRejectProposalItem: (itemId: number) => Promise<void>;
  onReloadDocument: () => Promise<void>;
}

const STAGE_LABELS: Record<ChatRefineStageName, string> = {
  planning: '正在制定跨章节修订计划…',
  refining_slot: '正在阅读上下文并推演答复…',
  drafting_diff: '正在结构化重构修订正文…',
  verifying: '正在执行四重证据校验…',
};

export const ChatRefineThreadPanel: React.FC<ChatRefineThreadPanelProps> = ({
  doc,
  regularSections,
  sessions,
  activeSessionId,
  loadingSessions,
  mode,
  scopeType,
  selectedSlotId,
  inlineAnchor,
  streaming,
  conflictByMsgId,
  onChangeMode,
  onChangeScopeType,
  onChangeSlotId,
  onClearInlineAnchor,
  onSelectSession,
  onReloadSessions,
  onSendPrompt,
  onStopStreaming,
  onApplyItems,
  onAcceptLegacyProposal,
  onRejectProposal,
  onRejectProposalItem,
  onReloadDocument,
}) => {
  const [prompt, setPrompt] = useState<string>('');

  const sectionsBySlot = useMemo(() => {
    const m = new Map<string, SectionVO>();
    for (const s of regularSections) {
      m.set(s.slot_id, s);
    }
    return m;
  }, [regularSections]);

  const currentSection = useMemo(
    () => regularSections.find((s) => s.slot_id === selectedSlotId) ?? null,
    [regularSections, selectedSlotId],
  );

  const activeSession = useMemo(
    () => sessions.find((s) => s.id === activeSessionId) ?? null,
    [sessions, activeSessionId],
  );

  const isTargetLocked =
    mode === 'revise' &&
    (scopeType === 'slot' || scopeType === 'inline') &&
    currentSection?.ownership === 'locked';

  const submitCurrentPrompt = () => {
    const text = prompt.trim();
    if (!text || isTargetLocked || Boolean(streaming)) return;
    setPrompt('');
    onSendPrompt(text);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault();
      submitCurrentPrompt();
    }
  };

  return (
    <div className="code-archdoc-chat-panel">
      <div className="code-archdoc-chat-controls">
        <div className="code-archdoc-review-mode-bar">
          <div className="code-archdoc-review-mode-switch" role="group" aria-label="对话模式">
            <button
              type="button"
              className={`code-archdoc-review-mode-btn ${
                mode === 'revise' ? 'code-archdoc-review-mode-btn--active' : ''
              }`}
              disabled={Boolean(streaming)}
              onClick={() => onChangeMode('revise')}
            >
              <Sparkles size={13} />
              <span>精修修订</span>
            </button>
            <button
              type="button"
              className={`code-archdoc-review-mode-btn ${
                mode === 'ask' ? 'code-archdoc-review-mode-btn--active' : ''
              }`}
              disabled={Boolean(streaming)}
              onClick={() => onChangeMode('ask')}
            >
              <HelpCircle size={13} />
              <span>Ask 只读问答</span>
            </button>
          </div>

          <select
            className="code-select"
            value={scopeType === 'review_batch' ? 'global' : scopeType}
            disabled={Boolean(streaming)}
            onChange={(e) => onChangeScopeType(e.target.value as ChatScopeType)}
            aria-label="提问或精修作用域"
          >
            <option value="slot">单章聚焦</option>
            {inlineAnchor && <option value="inline">划词选区</option>}
            <option value="global">全文档全局</option>
          </select>
        </div>

        {(scopeType === 'slot' || scopeType === 'inline') && (
          <div className="code-archdoc-chat-control-row">
            <select
              className="code-select"
              value={selectedSlotId}
              disabled={Boolean(streaming)}
              onChange={(e) => onChangeSlotId(e.target.value)}
              aria-label="选择目标章节槽位"
            >
              {regularSections.map((s) => (
                <option key={s.slot_id} value={s.slot_id}>
                  {s.title}
                  {s.ownership === 'locked' ? ' [已锁定]' : ''}
                </option>
              ))}
            </select>
          </div>
        )}

        {inlineAnchor?.quote && (
          <div className="code-archdoc-review-inline-pill">
            <span>聚焦原文：“{inlineAnchor.quote}”</span>
            <button
              type="button"
              className="btn btn-text btn-small"
              onClick={onClearInlineAnchor}
              aria-label="清除划词锚点"
            >
              <X size={12} />
            </button>
          </div>
        )}

        <div className="code-archdoc-chat-session-bar">
          <select
            className="code-select code-archdoc-chat-session-select"
            value={activeSessionId ?? ''}
            disabled={Boolean(streaming)}
            onChange={(e) => {
              const v = e.target.value;
              onSelectSession(v ? Number.parseInt(v, 10) : undefined);
            }}
            aria-label="历史精修会话"
          >
            <option value="">＋ 开启新对话（当前修订 v{doc.current_revision_no}）</option>
            {sessions.map((s) => (
              <option key={s.id} value={s.id}>
                {s.title || `会话 #${s.id}`} ({s.messages.length} 条)
              </option>
            ))}
          </select>
          <button
            type="button"
            className="btn btn-text btn-small"
            disabled={loadingSessions || Boolean(streaming)}
            onClick={() => void onReloadSessions()}
            title="刷新会话历史"
          >
            <RefreshCw size={13} className={loadingSessions ? 'code-archdoc-spin' : ''} />
          </button>
        </div>

        {isTargetLocked && (
          <div className="code-archdoc-chat-locked-notice">
            <Lock size={14} />
            <span>
              当前章节为 <strong>locked</strong> 状态，禁止精修写入；可切换为 <strong>Ask 只读问答</strong> 咨询实现细节，或先解锁章节。
            </span>
          </div>
        )}
      </div>

      <div className="code-archdoc-chat-messages">
        {!activeSession && !streaming ? (
          <EmptyState
            icon={<MessageSquareText size={32} />}
            title="局部精修 & 只读溯源问答"
            description="支持只读 Ask 追问（含锁定章节与全局视角）、单段划词微修，以及从左侧批注篮发起跨章节协同精修。"
          />
        ) : (
          <>
            {(activeSession?.messages ?? []).map((msg: ChatMessageVO) => {
              const hasGroupItems = Boolean(msg.proposal_items && msg.proposal_items.length > 0);
              const showLegacyCard =
                !hasGroupItems && msg.role === 'assistant' && msg.proposal_status !== 'none';

              return (
                <div
                  key={msg.id}
                  className={`code-archdoc-chat-bubble ${
                    msg.role === 'user'
                      ? 'code-archdoc-chat-bubble--user'
                      : 'code-archdoc-chat-bubble--assistant'
                  }`}
                >
                  <div className="code-archdoc-chat-bubble-meta">
                    <span>{msg.role === 'user' ? '负责人' : 'Archdoc AI'}</span>
                    {msg.intent === 'ask' && <span className="code-badge code-badge--info">Ask</span>}
                    {msg.intent === 'plan_refine' && (
                      <span className="code-badge code-badge--purple">协同规划</span>
                    )}
                    {msg.model_tier && <span className="code-archdoc-mono">{msg.model_tier}</span>}
                    {msg.base_revision_no > 0 && (
                      <span className="text-muted">base v{msg.base_revision_no}</span>
                    )}
                  </div>

                  {msg.content && (
                    <div className="code-archdoc-chat-bubble-text">{msg.content}</div>
                  )}

                  {hasGroupItems && msg.proposal_items && (
                    <ProposalGroupCard
                      messageId={msg.id}
                      baseRevisionNo={msg.base_revision_no}
                      currentRevisionNo={doc.current_revision_no}
                      proposalStatus={msg.proposal_status}
                      planSummary={msg.plan_summary}
                      items={msg.proposal_items}
                      sectionsBySlot={sectionsBySlot}
                      verifierPreview={msg.verifier_preview}
                      canWrite={doc.can_write}
                      conflictMessage={conflictByMsgId[msg.id]}
                      onApplyItems={onApplyItems}
                      onRejectItem={onRejectProposalItem}
                      onRejectAll={onRejectProposal}
                      onReloadDocument={onReloadDocument}
                    />
                  )}

                  {showLegacyCard && (
                    <DiffProposalCard
                      messageId={msg.id}
                      slotId={msg.target_slot_id}
                      slotTitle={sectionsBySlot.get(msg.target_slot_id)?.title}
                      baseRevisionNo={msg.base_revision_no}
                      currentRevisionNo={doc.current_revision_no}
                      unifiedDiff={msg.unified_diff}
                      proposedAfterMD={msg.proposed_after_md}
                      verifierPreview={msg.verifier_preview}
                      status={msg.proposal_status}
                      canWrite={doc.can_write}
                      conflictMessage={conflictByMsgId[msg.id]}
                      onAccept={onAcceptLegacyProposal}
                      onReject={onRejectProposal}
                      onReloadDocument={onReloadDocument}
                    />
                  )}
                </div>
              );
            })}

            {streaming && (
              <div className="code-archdoc-chat-bubble code-archdoc-chat-bubble--assistant code-archdoc-chat-bubble--streaming">
                <div className="code-archdoc-chat-bubble-meta">
                  <Loader2 size={13} className="code-archdoc-spin" />
                  <span>
                    {streaming.stage
                      ? streaming.stage === 'refining_slot' && streaming.stageTotal && streaming.stageTotal > 1
                        ? `正在精修第 ${streaming.stageIndex ?? 1}/${streaming.stageTotal} 节 (${sectionsBySlot.get(streaming.stageSlotId || '')?.title || streaming.stageSlotId})`
                        : STAGE_LABELS[streaming.stage]
                      : '正在连接模型…'}
                  </span>
                </div>

                {streaming.planSummary && (
                  <div className="code-archdoc-review-plan-live">
                    <div>
                      <strong>规划目标章节：</strong>
                      {(streaming.planSummary.target_slots ?? [])
                        .map((s) => s.title || s.slot_id)
                        .join('、') || '无'}
                    </div>
                    {streaming.planSummary.skipped_locked_slots &&
                      streaming.planSummary.skipped_locked_slots.length > 0 && (
                        <div className="text-muted">
                          已保护锁定章节：
                          {streaming.planSummary.skipped_locked_slots
                            .map((s) => s.title || s.slot_id)
                            .join('、')}
                        </div>
                      )}
                  </div>
                )}

                {streaming.toolSteps.length > 0 && (
                  <div className="code-archdoc-chat-tool-trace">
                    {streaming.toolSteps.map((step, idx) => (
                      <div key={idx} className="code-archdoc-chat-tool-step">
                        <code>{step}</code>
                      </div>
                    ))}
                  </div>
                )}

                {streaming.deltaText && (
                  <div className="code-archdoc-chat-bubble-text">{streaming.deltaText}</div>
                )}

                {streaming.proposalItems.length > 0 && (
                  <div className="code-archdoc-chat-usage">
                    已生成 {streaming.proposalItems.length} 节结构化 Diff 提案，即将落库…
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>

      {doc.can_write && (
        <div className="code-archdoc-chat-composer">
          <textarea
            className="code-input code-archdoc-chat-input"
            value={prompt}
            disabled={Boolean(streaming) || isTargetLocked}
            onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={
              mode === 'ask'
                ? '向 AI 提问该章节或全文档的代码依据、调用链路与边界约束…'
                : '输入精修修改指令（例：补充连接池重试策略与异常分支说明）…'
            }
          />
          <div className="code-archdoc-chat-composer-foot">
            <span className="code-archdoc-chat-shortcut">
              <AlertTriangle size={12} />
              <span>Ctrl/Cmd + Enter 发送</span>
            </span>

            {streaming ? (
              <button
                type="button"
                className="btn btn-danger btn-small"
                onClick={onStopStreaming}
              >
                <Square size={12} />
                <span>停止生成</span>
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-primary btn-small"
                disabled={!prompt.trim() || isTargetLocked}
                onClick={submitCurrentPrompt}
              >
                <Send size={13} />
                <span>{mode === 'ask' ? '发起问答' : '发送精修'}</span>
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default ChatRefineThreadPanel;
