import React, { useEffect, useMemo, useState } from 'react';
import {
  BookmarkCheck,
  CheckCircle2,
  Loader2,
  Plus,
  Send,
  Sparkles,
  Trash2,
  XCircle,
} from 'lucide-react';
import { EmptyState, StatusTag, useToast, type StatusType } from '@code/common';
import {
  createReviewComment,
  deleteReviewComment,
  updateReviewComment,
} from '../../api/reviewComments';
import type {
  CreateReviewCommentPayload,
  DocumentDetailVO,
  ReviewCategory,
  ReviewCommentAnchor,
  ReviewCommentStatus,
  ReviewCommentVO,
  ReviewScopeType,
} from '../../types';
import { formatApiError } from '../../utils/errorMessages';

export interface ReviewDraftContext {
  scopeType: ReviewScopeType;
  slotId?: string;
  anchor?: ReviewCommentAnchor;
  nonce: number;
}

export interface ReviewBasketPanelProps {
  doc: DocumentDetailVO;
  comments: ReviewCommentVO[];
  activeCommentId?: number | null;
  draftContext?: ReviewDraftContext | null;
  submittingBatch: boolean;
  onCommentsChanged: () => Promise<void>;
  onSelectCommentOnCanvas: (comment: ReviewCommentVO) => void;
  onSubmitBatchRefine: (commentIds: number[], extraPrompt: string) => void;
  onClearDraftContext: () => void;
}

type StatusFilter = 'open' | 'proposed' | 'resolved' | 'all';

const CATEGORY_OPTIONS: Array<{ value: ReviewCategory; label: string; hint: string }> = [
  { value: 'correction', label: '纠错修正', hint: '代码事实/逻辑有误，优先触发源码核查' },
  { value: 'supplement', label: '知识补充', hint: '业务背景/隐性约束与设计取舍说明' },
  { value: 'concise', label: '精简改写', hint: '篇幅压缩与结构优化，保留事实' },
  { value: 'global_rule', label: '全局规范', hint: '跨章节统一口径、术语或写作约束' },
  { value: 'question', label: '疑问待核', hint: '备忘或待负责人人工核实的问题' },
];

const STATUS_BADGE: Record<ReviewCommentStatus, { type: StatusType; label: string }> = {
  open: { type: 'warning', label: '待处理' },
  proposed: { type: 'running', label: '已出提案' },
  resolved: { type: 'success', label: '已解决' },
  dismissed: { type: 'neutral', label: '已忽略' },
};

export const ReviewBasketPanel: React.FC<ReviewBasketPanelProps> = ({
  doc,
  comments,
  activeCommentId,
  draftContext,
  submittingBatch,
  onCommentsChanged,
  onSelectCommentOnCanvas,
  onSubmitBatchRefine,
  onClearDraftContext,
}) => {
  const { showToast } = useToast();
  const [filter, setFilter] = useState<StatusFilter>('open');
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [extraPrompt, setExtraPrompt] = useState<string>('');

  // 新建批注表单状态
  const [composerOpen, setComposerOpen] = useState<boolean>(false);
  const [newScope, setNewScope] = useState<ReviewScopeType>('section');
  const [newSlotId, setNewSlotId] = useState<string>(doc.sections[0]?.slot_id ?? '');
  const [newCategory, setNewCategory] = useState<ReviewCategory>('correction');
  const [newContent, setNewContent] = useState<string>('');
  const [newPersist, setNewPersist] = useState<boolean>(false);
  const [savingNew, setSavingNew] = useState<boolean>(false);
  const [busyCommentId, setBusyCommentId] = useState<number | null>(null);

  useEffect(() => {
    if (!draftContext) return;
    setComposerOpen(true);
    setNewScope(draftContext.scopeType);
    if (draftContext.slotId) {
      setNewSlotId(draftContext.slotId);
    }
    if (draftContext.scopeType === 'global') {
      setNewCategory('global_rule');
    }
  }, [draftContext]);

  // 自动勾选新创建的 open 批注（上限 20 条）
  useEffect(() => {
    const openIds = comments.filter((c) => c.status === 'open').map((c) => c.id);
    setSelectedIds((prev) => {
      const kept = prev.filter((id) => openIds.includes(id));
      if (kept.length > 0) return kept;
      return openIds.slice(0, 20);
    });
  }, [comments]);

  const counts = useMemo(() => {
    let open = 0;
    let proposed = 0;
    let resolved = 0;
    for (const c of comments) {
      if (c.status === 'open') open++;
      else if (c.status === 'proposed') proposed++;
      else if (c.status === 'resolved') resolved++;
    }
    return { open, proposed, resolved, total: comments.length };
  }, [comments]);

  const filteredComments = useMemo(() => {
    if (filter === 'all') return comments;
    return comments.filter((c) => c.status === filter);
  }, [comments, filter]);

  const slotTitleMap = useMemo(() => {
    const m = new Map<string, string>();
    for (const sec of doc.sections) {
      m.set(sec.slot_id, sec.title);
    }
    return m;
  }, [doc.sections]);

  const toggleSelect = (id: number) => {
    setSelectedIds((prev) => {
      if (prev.includes(id)) return prev.filter((x) => x !== id);
      if (prev.length >= 20) {
        showToast('单次最多合并提交 20 条批注进行 AI 规划精修', 'warning');
        return prev;
      }
      return [...prev, id];
    });
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    const content = newContent.trim();
    if (!content) {
      showToast('请输入批注说明内容', 'warning');
      return;
    }
    setSavingNew(true);
    try {
      const payload: CreateReviewCommentPayload = {
        base_revision_no: doc.current_revision_no,
        scope_type: newScope,
        slot_id: newScope === 'global' ? '' : newSlotId,
        anchor: newScope === 'inline' ? draftContext?.anchor : undefined,
        category: newCategory,
        content,
        persist_for_regen: newPersist,
      };
      await createReviewComment(doc.id, payload);
      setNewContent('');
      setNewPersist(false);
      setComposerOpen(false);
      onClearDraftContext();
      await onCommentsChanged();
      setFilter('open');
      showToast('已加入审阅批注篮', 'success');
    } catch (err: unknown) {
      showToast(formatApiError(err, '新增批注失败').message, 'error');
    } finally {
      setSavingNew(false);
    }
  };

  const handleTogglePersist = async (c: ReviewCommentVO) => {
    setBusyCommentId(c.id);
    try {
      await updateReviewComment(doc.id, c.id, { persist_for_regen: !c.persist_for_regen });
      await onCommentsChanged();
    } catch (err: unknown) {
      showToast(formatApiError(err, '更新长期约束标志失败').message, 'error');
    } finally {
      setBusyCommentId(null);
    }
  };

  const handleTransition = async (
    c: ReviewCommentVO,
    status: 'open' | 'resolved' | 'dismissed',
  ) => {
    setBusyCommentId(c.id);
    try {
      await updateReviewComment(doc.id, c.id, { status });
      await onCommentsChanged();
    } catch (err: unknown) {
      showToast(formatApiError(err, '更新批注状态失败').message, 'error');
    } finally {
      setBusyCommentId(null);
    }
  };

  const handleDelete = async (c: ReviewCommentVO) => {
    setBusyCommentId(c.id);
    try {
      await deleteReviewComment(doc.id, c.id);
      await onCommentsChanged();
    } catch (err: unknown) {
      showToast(formatApiError(err, '删除批注失败').message, 'error');
    } finally {
      setBusyCommentId(null);
    }
  };

  return (
    <div className="code-archdoc-review-basket">
      <div className="code-archdoc-review-basket-toolbar">
        <div className="code-archdoc-review-filter-tabs" role="tablist">
          {(
            [
              { key: 'open', label: `待处理 (${counts.open})` },
              { key: 'proposed', label: `已出提案 (${counts.proposed})` },
              { key: 'resolved', label: `已解决 (${counts.resolved})` },
              { key: 'all', label: `全部 (${counts.total})` },
            ] as const
          ).map((tab) => (
            <button
              key={tab.key}
              type="button"
              role="tab"
              aria-selected={filter === tab.key}
              className={`code-archdoc-review-filter-tab ${
                filter === tab.key ? 'code-archdoc-review-filter-tab--active' : ''
              }`}
              onClick={() => setFilter(tab.key)}
            >
              {tab.label}
            </button>
          ))}
        </div>

        {doc.can_write && (
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => setComposerOpen((v) => !v)}
          >
            <Plus size={13} />
            <span>{composerOpen ? '收起表单' : '新增批注'}</span>
          </button>
        )}
      </div>

      {composerOpen && doc.can_write && (
        <form className="code-archdoc-review-composer" onSubmit={(e) => void handleCreate(e)}>
          <div className="code-archdoc-review-composer-row">
            <select
              className="code-select"
              value={newScope}
              onChange={(e) => setNewScope(e.target.value as ReviewScopeType)}
              aria-label="批注作用域"
            >
              <option value="section">整章批注</option>
              <option value="global">全局规则</option>
              {draftContext?.anchor && <option value="inline">划词/块级锚点</option>}
            </select>

            {newScope !== 'global' && (
              <select
                className="code-select"
                value={newSlotId}
                onChange={(e) => setNewSlotId(e.target.value)}
                aria-label="目标章节"
              >
                {doc.sections.map((s) => (
                  <option key={s.slot_id} value={s.slot_id}>
                    {s.title}
                  </option>
                ))}
              </select>
            )}

            <select
              className="code-select"
              value={newCategory}
              onChange={(e) => setNewCategory(e.target.value as ReviewCategory)}
              aria-label="批注类型"
            >
              {CATEGORY_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value} title={opt.hint}>
                  {opt.label}
                </option>
              ))}
            </select>
          </div>

          {newScope === 'inline' && draftContext?.anchor?.quote && (
            <div className="code-archdoc-review-quote-box">
              引用原文：“{draftContext.anchor.quote}”
            </div>
          )}

          <textarea
            className="code-input"
            rows={2}
            value={newContent}
            onChange={(e) => setNewContent(e.target.value)}
            placeholder="写下修改诉求、需要补充的业务约束或精简建议..."
          />

          <div className="code-archdoc-review-composer-foot">
            <label className="code-archdoc-review-checkbox">
              <input
                type="checkbox"
                checked={newPersist}
                onChange={(e) => setNewPersist(e.target.checked)}
              />
              <span>持久化为后续全量/增量重生成长期指导</span>
            </label>

            <button type="submit" className="btn btn-primary btn-small" disabled={savingNew}>
              {savingNew ? <Loader2 size={13} className="code-archdoc-spin" /> : <Plus size={13} />}
              <span>加入批注篮</span>
            </button>
          </div>
        </form>
      )}

      <div className="code-archdoc-review-list">
        {filteredComments.length === 0 ? (
          <EmptyState
            title="暂无符合条件的审阅批注"
            description="在左侧画布中划词选中文本或点击段落右上角「+ 批注」，即可边读边攒批注并一次性交由 AI 规划修订。"
          />
        ) : (
          filteredComments.map((c) => {
            const st = STATUS_BADGE[c.status];
            const canSelect = c.status === 'open';
            const checked = selectedIds.includes(c.id);
            const busy = busyCommentId === c.id;
            return (
              <div
                key={c.id}
                className={`code-archdoc-review-item ${
                  c.id === activeCommentId ? 'code-archdoc-review-item--active' : ''
                }`}
              >
                <div className="code-archdoc-review-item-head">
                  <div className="flex-center gap-xs">
                    {canSelect && (
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleSelect(c.id)}
                        aria-label={`选择批注 #${c.comment_no}`}
                      />
                    )}
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => onSelectCommentOnCanvas(c)}
                    >
                      #{c.comment_no} · {c.slot_id ? slotTitleMap.get(c.slot_id) || c.slot_id : '全局'}
                    </button>
                  </div>
                  <div className="flex-center gap-xs">
                    <StatusTag status={st.type} text={st.label} size="sm" />
                  </div>
                </div>

                {c.anchor?.quote && (
                  <div className="code-archdoc-review-quote-box" title={c.anchor.quote}>
                    “{c.anchor.quote}”
                  </div>
                )}

                <div className="code-archdoc-review-item-body">{c.content}</div>

                <div className="code-archdoc-review-item-foot">
                  <button
                    type="button"
                    className={`btn btn-text btn-small ${
                      c.persist_for_regen ? 'code-archdoc-review-persist--on' : ''
                    }`}
                    disabled={busy || !doc.can_write}
                    onClick={() => void handleTogglePersist(c)}
                    title="勾选后，未来触发全量/增量重生成时自动作为参考约束"
                  >
                    <BookmarkCheck size={12} />
                    <span>{c.persist_for_regen ? '已设长期约束' : '设为长期约束'}</span>
                  </button>

                  {doc.can_write && (
                    <div className="flex-center gap-xs">
                      {c.status !== 'resolved' && (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          disabled={busy}
                          onClick={() => void handleTransition(c, 'resolved')}
                          title="手动标记为已解决"
                        >
                          <CheckCircle2 size={12} />
                          <span>解决</span>
                        </button>
                      )}
                      {c.status === 'resolved' || c.status === 'dismissed' ? (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          disabled={busy}
                          onClick={() => void handleTransition(c, 'open')}
                        >
                          <span>重开</span>
                        </button>
                      ) : (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          disabled={busy}
                          onClick={() => void handleTransition(c, 'dismissed')}
                          title="忽略本条批注"
                        >
                          <XCircle size={12} />
                          <span>忽略</span>
                        </button>
                      )}
                      {(c.status === 'open' || c.status === 'dismissed') && (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          disabled={busy}
                          onClick={() => void handleDelete(c)}
                          aria-label={`删除批注 #${c.comment_no}`}
                        >
                          <Trash2 size={12} />
                        </button>
                      )}
                    </div>
                  )}
                </div>
              </div>
            );
          })
        )}
      </div>

      {doc.can_write && counts.open > 0 && (
        <div className="code-archdoc-review-batch-bar">
          <input
            type="text"
            className="code-input"
            value={extraPrompt}
            onChange={(e) => setExtraPrompt(e.target.value)}
            placeholder="补充整体修改口吻或优先顺序（可选，留空自动按批注规划）"
          />
          <button
            type="button"
            className="btn btn-primary btn-small"
            disabled={submittingBatch || selectedIds.length === 0}
            onClick={() => onSubmitBatchRefine(selectedIds, extraPrompt)}
          >
            {submittingBatch ? (
              <Loader2 size={14} className="code-archdoc-spin" />
            ) : (
              <Sparkles size={14} />
            )}
            <span>
              <Send size={12} /> 统一提交 AI 规划并精修 ({selectedIds.length} 条)
            </span>
          </button>
        </div>
      )}
    </div>
  );
};

export default ReviewBasketPanel;
