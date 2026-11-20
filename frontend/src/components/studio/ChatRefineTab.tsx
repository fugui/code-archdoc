import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  Loader2,
  Lock,
  MessageSquareText,
  RefreshCw,
  Send,
  Sparkles,
  Square,
} from 'lucide-react';
import { EmptyState, useToast } from '@code/common';
import {
  acceptChatProposal,
  listChatSessions,
  rejectChatProposal,
  streamChatRefine,
} from '../../api/chat';
import type {
  ChatMessageVO,
  ChatSessionWithMessagesVO,
  ChatVerifierPreviewVO,
  DocumentDetailVO,
} from '../../types';
import { ArchdocApiError, formatApiError } from '../../utils/errorMessages';
import DiffProposalCard from './DiffProposalCard';

export interface ChatRefineTabProps {
  doc: DocumentDetailVO;
  initialSlotId?: string;
  onDocumentUpdated: (updatedDoc: DocumentDetailVO) => void;
  onReloadDocument: () => Promise<void>;
}

interface StreamingState {
  sessionId?: number;
  messageId?: number;
  slotId: string;
  baseRevisionNo: number;
  modelTier?: string;
  toolSteps: string[];
  deltaText: string;
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

export const ChatRefineTab: React.FC<ChatRefineTabProps> = ({
  doc,
  initialSlotId,
  onDocumentUpdated,
  onReloadDocument,
}) => {
  const { showToast } = useToast();

  const regularSections = useMemo(
    () => doc.sections.filter((s) => !s.is_orphan),
    [doc.sections],
  );

  const [selectedSlotId, setSelectedSlotId] = useState<string>(() => {
    if (initialSlotId && regularSections.some((s) => s.slot_id === initialSlotId)) {
      return initialSlotId;
    }
    const firstUnlocked = regularSections.find((s) => s.ownership !== 'locked');
    return firstUnlocked?.slot_id ?? regularSections[0]?.slot_id ?? '';
  });

  const [sessions, setSessions] = useState<ChatSessionWithMessagesVO[]>([]);
  const [activeSessionId, setActiveSessionId] = useState<number | undefined>(undefined);
  const [loadingSessions, setLoadingSessions] = useState<boolean>(false);
  const [prompt, setPrompt] = useState<string>('');
  const [streaming, setStreaming] = useState<StreamingState | null>(null);
  const [conflictByMsgId, setConflictByMsgId] = useState<Record<number, string>>({});

  const abortRef = useRef<AbortController | null>(null);
  const scrollEndRef = useRef<HTMLDivElement | null>(null);

  // 外部点击某章节的「Chat 精修」按钮时同步切换目标槽位
  useEffect(() => {
    if (initialSlotId && regularSections.some((s) => s.slot_id === initialSlotId)) {
      setSelectedSlotId(initialSlotId);
    }
  }, [initialSlotId, regularSections]);

  // 卸载时中止可能仍在进行的 SSE 流，防止内存泄漏
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

  const slotTitleMap = useMemo(() => {
    const m: Record<string, string> = {};
    for (const s of doc.sections) {
      m[s.slot_id] = s.title;
    }
    return m;
  }, [doc.sections]);

  const selectedSection = useMemo(
    () => regularSections.find((s) => s.slot_id === selectedSlotId) ?? null,
    [regularSections, selectedSlotId],
  );
  const isTargetLocked = selectedSection?.ownership === 'locked';

  const visibleMessages = useMemo<ChatMessageVO[]>(() => {
    const active = sessions.find((s) => s.id === activeSessionId);
    if (!active) return [];
    return active.messages;
  }, [sessions, activeSessionId]);

  useEffect(() => {
    scrollEndRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [visibleMessages.length, streaming?.deltaText]);

  const handleStopStream = () => {
    if (abortRef.current) {
      abortRef.current.abort();
      abortRef.current = null;
      setStreaming(null);
      showToast('已中断当前精修生成，未写入无效提案', 'info');
    }
  };

  const handleSendRefine = async () => {
    const trimmed = prompt.trim();
    if (!trimmed || streaming || !selectedSlotId || !doc.can_write) return;
    if (isTargetLocked) {
      showToast('目标章节已锁定，请先解锁或切换其他章节', 'warning');
      return;
    }

    const controller = new AbortController();
    abortRef.current = controller;

    const currentBaseRev = doc.current_revision_no;
    setPrompt('');
    setStreaming({
      sessionId: activeSessionId,
      slotId: selectedSlotId,
      baseRevisionNo: currentBaseRev,
      toolSteps: [],
      deltaText: '',
    });

    let receivedErrorEvent = false;
    let targetSessionId = activeSessionId;

    try {
      await streamChatRefine({
        docId: doc.id,
        payload: {
          session_id: activeSessionId,
          target_slot_id: selectedSlotId,
          base_revision_no: currentBaseRev,
          prompt: trimmed,
        },
        signal: controller.signal,
        onEvent: (ev) => {
          switch (ev.event) {
            case 'meta':
              targetSessionId = ev.data.session_id;
              setActiveSessionId(ev.data.session_id);
              setStreaming((prev) =>
                prev
                  ? {
                      ...prev,
                      sessionId: ev.data.session_id,
                      messageId: ev.data.message_id,
                      modelTier: ev.data.model_tier,
                    }
                  : prev,
              );
              break;
            case 'tool_call':
              setStreaming((prev) =>
                prev
                  ? {
                      ...prev,
                      toolSteps: [
                        ...prev.toolSteps,
                        `[R${ev.data.round}] ${ev.data.summary} (${ev.data.duration_ms}ms)`,
                      ],
                    }
                  : prev,
              );
              break;
            case 'delta':
              setStreaming((prev) =>
                prev ? { ...prev, deltaText: prev.deltaText + ev.data.text } : prev,
              );
              break;
            case 'diff_proposal':
              setStreaming((prev) =>
                prev
                  ? {
                      ...prev,
                      diffProposal: {
                        proposalId: ev.data.proposal_id,
                        slotId: ev.data.slot_id,
                        beforeMD: ev.data.before_md,
                        afterMD: ev.data.after_md,
                        unifiedDiff: ev.data.unified_diff,
                      },
                    }
                  : prev,
              );
              break;
            case 'verifier_preview':
              setStreaming((prev) =>
                prev ? { ...prev, verifierPreview: ev.data } : prev,
              );
              break;
            case 'done':
              setStreaming((prev) =>
                prev
                  ? {
                      ...prev,
                      usageSummary: `Prompt ${ev.data.prompt_tokens} / Completion ${ev.data.completion_tokens} tokens · ${ev.data.latency_ms}ms`,
                    }
                  : prev,
              );
              break;
            case 'error':
              receivedErrorEvent = true;
              showToast(ev.data.message || '精修生成失败', 'error');
              break;
          }
        },
      });

      if (!receivedErrorEvent) {
        await loadSessions();
        if (targetSessionId) {
          setActiveSessionId(targetSessionId);
        }
      }
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        // 用户主动中断，不重复弹错
      } else if (err instanceof ArchdocApiError && err.code === 'OPTIMISTIC_LOCK_CONFLICT') {
        showToast(formatApiError(err).message, 'warning');
        await onReloadDocument();
      } else {
        showToast(formatApiError(err, '发起局部精修失败').message, 'error');
      }
    } finally {
      abortRef.current = null;
      setStreaming(null);
    }
  };

  const handleAccept = async (messageId: number) => {
    setConflictByMsgId((prev) => {
      const next = { ...prev };
      delete next[messageId];
      return next;
    });
    try {
      const updatedDoc = await acceptChatProposal(doc.id, messageId, {
        base_revision_no: doc.current_revision_no,
      });
      onDocumentUpdated(updatedDoc);
      showToast(`已采纳提案并生成修订 v${updatedDoc.current_revision_no}`, 'success');
      await loadSessions();
    } catch (err: unknown) {
      if (err instanceof ArchdocApiError && err.code === 'OPTIMISTIC_LOCK_CONFLICT') {
        const msg = formatApiError(err).message;
        setConflictByMsgId((prev) => ({ ...prev, [messageId]: msg }));
        showToast(msg, 'warning');
        return;
      }
      showToast(formatApiError(err, '采纳精修提案失败').message, 'error');
    }
  };

  const handleReject = async (messageId: number) => {
    try {
      await rejectChatProposal(doc.id, messageId);
      setSessions((prev) =>
        prev.map((sess) => ({
          ...sess,
          messages: sess.messages.map((m) =>
            m.id === messageId ? { ...m, proposal_status: 'rejected' } : m,
          ),
        })),
      );
      showToast('已拒绝该修订提案，文档版本保持不变', 'info');
    } catch (err: unknown) {
      showToast(formatApiError(err, '拒绝精修提案失败').message, 'error');
    }
  };

  if (!doc.can_write) {
    return (
      <EmptyState
        icon={<MessageSquareText size={32} />}
        title="只读浏览模式"
        description="仅该仓库/子目录负责人或管理员可发起 AI Chat 局部协同精修。"
      />
    );
  }

  return (
    <div className="code-archdoc-chat-panel">
      {/* 目标槽位与会话选择栏 */}
      <div className="code-archdoc-chat-controls">
        <div className="code-archdoc-chat-control-row">
          <label className="code-archdoc-chat-label" htmlFor="archdoc-chat-slot">
            目标精修章节
          </label>
          <select
            id="archdoc-chat-slot"
            className="code-archdoc-select"
            value={selectedSlotId}
            disabled={streaming !== null}
            onChange={(e) => setSelectedSlotId(e.target.value)}
          >
            {regularSections.map((sec) => (
              <option key={sec.slot_id} value={sec.slot_id}>
                {sec.ownership === 'locked' ? '[已锁定] ' : ''}
                {sec.title} ({sec.slot_id})
              </option>
            ))}
          </select>
        </div>

        <div className="code-archdoc-chat-session-bar">
          <select
            className="code-archdoc-select code-archdoc-chat-session-select"
            value={activeSessionId ?? ''}
            disabled={streaming !== null}
            onChange={(e) => {
              const v = e.target.value;
              setActiveSessionId(v ? Number.parseInt(v, 10) : undefined);
            }}
            aria-label="选择精修会话"
          >
            <option value="">新建精修会话</option>
            {sessions.map((sess) => (
              <option key={sess.id} value={sess.id}>
                #{sess.id} {sess.title || sess.target_slot_id || '精修会话'}
              </option>
            ))}
          </select>

          <button
            type="button"
            className="btn btn-text btn-small"
            disabled={loadingSessions || streaming !== null}
            onClick={() => void loadSessions()}
            title="刷新精修会话历史"
          >
            <RefreshCw size={13} />
          </button>
        </div>

        {isTargetLocked && (
          <div className="code-archdoc-chat-locked-notice">
            <Lock size={14} />
            <span>
              当前选定章节为 <strong>locked</strong> 状态，不可执行 AI 精修。请切换章节或先在左侧画布解除锁定。
            </span>
          </div>
        )}
      </div>

      {/* 对话流与 Diff 提案卡片列表 */}
      <div className="code-archdoc-chat-messages">
        {loadingSessions && visibleMessages.length === 0 ? (
          <div className="code-archdoc-loading">
            <Loader2 size={16} className="code-archdoc-spin" />
            <span>正在加载精修记录...</span>
          </div>
        ) : visibleMessages.length === 0 && !streaming ? (
          <div className="code-archdoc-chat-empty">
            <Sparkles size={26} />
            <div className="code-archdoc-chat-empty-title">章节级局部精修助手</div>
            <p className="code-archdoc-chat-empty-desc">
              输入针对当前章节的补充或修订指令，模型将严格隔离于目标槽位并输出红绿 Diff 提案与 Verifier 预检结果，非目标章节保持字节级不变。
            </p>
          </div>
        ) : (
          <>
            {visibleMessages.map((msg) => (
              <div
                key={msg.id}
                className={`code-archdoc-chat-bubble code-archdoc-chat-bubble--${msg.role}`}
              >
                <div className="code-archdoc-chat-bubble-meta">
                  <span>{msg.role === 'user' ? '负责人指令' : 'AI 架构助手'}</span>
                  {msg.target_slot_id && (
                    <code className="code-archdoc-mono">{msg.target_slot_id}</code>
                  )}
                  {msg.model_tier && msg.role === 'assistant' && (
                    <span className="code-badge code-badge--muted">{msg.model_tier}</span>
                  )}
                </div>

                <div className="code-archdoc-chat-bubble-text">{msg.content}</div>

                {msg.role === 'assistant' && msg.proposal_status !== 'none' && (
                  <DiffProposalCard
                    messageId={msg.id}
                    slotId={msg.target_slot_id}
                    slotTitle={slotTitleMap[msg.target_slot_id]}
                    baseRevisionNo={msg.base_revision_no}
                    currentRevisionNo={doc.current_revision_no}
                    unifiedDiff={msg.unified_diff}
                    proposedAfterMD={msg.proposed_after_md}
                    verifierPreview={msg.verifier_preview}
                    status={msg.proposal_status}
                    canWrite={doc.can_write}
                    conflictMessage={conflictByMsgId[msg.id]}
                    onAccept={handleAccept}
                    onReject={handleReject}
                    onReloadDocument={onReloadDocument}
                  />
                )}
              </div>
            ))}

            {/* 实时 SSE 流式气泡 */}
            {streaming && (
              <div className="code-archdoc-chat-bubble code-archdoc-chat-bubble--assistant code-archdoc-chat-bubble--streaming">
                <div className="code-archdoc-chat-bubble-meta">
                  <Loader2 size={13} className="code-archdoc-spin" />
                  <span>正在流式精修 {streaming.slotId}...</span>
                  {streaming.modelTier && (
                    <span className="code-badge code-badge--primary">{streaming.modelTier}</span>
                  )}
                </div>
                {streaming.toolSteps.length > 0 && (
                  <div className="code-archdoc-chat-usage">
                    只读源码查阅：{streaming.toolSteps.join(' -> ')}
                  </div>
                )}
                <pre className="code-archdoc-chat-stream-pre">
                  {streaming.deltaText || '正在结合目标章节源码证据与 Code IR 推理...'}
                </pre>
                {streaming.usageSummary && (
                  <div className="code-archdoc-chat-usage">{streaming.usageSummary}</div>
                )}
              </div>
            )}
          </>
        )}
        <div ref={scrollEndRef} />
      </div>

      {/* 底部输入与中止控制栏 */}
      <div className="code-archdoc-chat-composer">
        <textarea
          className="code-archdoc-textarea code-archdoc-chat-input"
          rows={3}
          disabled={streaming !== null || isTargetLocked}
          value={prompt}
          placeholder={
            isTargetLocked
              ? '目标章节已锁定，请切换未锁定章节后再输入指令...'
              : '输入本章节精修指令（如：补充支付失败重试时序图与退避参数来源）...'
          }
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void handleSendRefine();
            }
          }}
        />
        <div className="code-archdoc-chat-composer-foot">
          <span className="code-archdoc-chat-shortcut">
            <AlertTriangle size={12} />
            <span>Ctrl+Enter 发送 · 仅改动选定槽位</span>
          </span>

          {streaming ? (
            <button
              type="button"
              className="btn btn-danger btn-small"
              onClick={handleStopStream}
            >
              <Square size={12} />
              <span>停止生成</span>
            </button>
          ) : (
            <button
              type="button"
              className="btn btn-primary btn-small"
              disabled={!prompt.trim() || !selectedSlotId || isTargetLocked}
              onClick={() => void handleSendRefine()}
            >
              <Send size={13} />
              <span>发送精修</span>
            </button>
          )}
        </div>
      </div>
    </div>
  );
};

export default ChatRefineTab;
