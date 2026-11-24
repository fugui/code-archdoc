import React, { useState } from 'react';
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Edit3,
  HelpCircle,
  Loader2,
  Send,
  ShieldAlert,
  Sparkles,
  Tag,
  Trash2,
  Wand2,
  X,
} from 'lucide-react';
import { StatusTag } from '@code/common';
import type {
  RefDocRefineMode,
  RefDocRefinePresetAction,
  RefDocRefineScopeType,
  RefineRefDocResponse,
  RefineRefDocSelectionPayload,
} from '../../types';
import {
  classifyRefDocDiffLine,
  type RefDocDisplaySection,
} from '../../utils/refDocStudioUtils';

export type RefDocProposalDisposition = 'pending' | 'adopted' | 'applied_to_draft' | 'dismissed';

export interface RefDocMemoryTurn {
  id: string;
  createdAt: string;
  scopeType: RefDocRefineScopeType;
  mode: RefDocRefineMode;
  presetAction?: RefDocRefinePresetAction;
  prompt: string;
  sectionLabel?: string;
  selection?: RefineRefDocSelectionPayload;
  response: RefineRefDocResponse;
  disposition: RefDocProposalDisposition;
}

export interface RefDocRefinePanelProps {
  canEdit: boolean;
  scopeType: RefDocRefineScopeType;
  mode: RefDocRefineMode;
  selectedSection: RefDocDisplaySection | null;
  selection: RefineRefDocSelectionPayload | null;
  prompt: string;
  submitting: boolean;
  savingAdoption: boolean;
  errorText: string;
  budgetHint: string;
  turns: RefDocMemoryTurn[];
  onChangeScopeType: (scope: RefDocRefineScopeType) => void;
  onChangeMode: (mode: RefDocRefineMode) => void;
  onChangePrompt: (prompt: string) => void;
  onClearSelection: () => void;
  onRunRefine: (presetAction?: RefDocRefinePresetAction) => Promise<void>;
  onAdoptAndSave: (
    turnId: string,
    options: { syncSummaryAndTerms: boolean; redactAndSave: boolean },
  ) => Promise<void>;
  onAdoptSummaryOnly: (turnId: string) => Promise<void>;
  onApplyToDraft: (turnId: string) => void;
  onDismissTurn: (turnId: string) => void;
  onClearTurns: () => void;
}

function dispositionBadge(status: RefDocProposalDisposition): {
  type: 'warning' | 'success' | 'neutral';
  label: string;
} {
  switch (status) {
    case 'adopted':
      return { type: 'success', label: '已保存新版' };
    case 'applied_to_draft':
      return { type: 'success', label: '已回填草稿' };
    case 'dismissed':
      return { type: 'neutral', label: '已忽略' };
    default:
      return { type: 'warning', label: '内存提案' };
  }
}

export const RefDocRefinePanel: React.FC<RefDocRefinePanelProps> = ({
  canEdit,
  scopeType,
  mode,
  selectedSection,
  selection,
  prompt,
  submitting,
  savingAdoption,
  errorText,
  budgetHint,
  turns,
  onChangeScopeType,
  onChangeMode,
  onChangePrompt,
  onClearSelection,
  onRunRefine,
  onAdoptAndSave,
  onAdoptSummaryOnly,
  onApplyToDraft,
  onDismissTurn,
  onClearTurns,
}) => {
  const [expandedPreflightIds, setExpandedPreflightIds] = useState<Record<string, boolean>>({});
  const [syncSummaryByTurn, setSyncSummaryByTurn] = useState<Record<string, boolean>>({});
  const [redactByTurn, setRedactByTurn] = useState<Record<string, boolean>>({});

  const togglePreflight = (id: string) => {
    setExpandedPreflightIds((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  const handleFormSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (submitting) return;
    void onRunRefine();
  };

  return (
    <div className="code-archdoc-refstudio-pane">
      {/* 控制表单 */}
      <form className="code-archdoc-refstudio-refine-form" onSubmit={handleFormSubmit}>
        <div className="code-archdoc-refstudio-control-row">
          <div className="code-archdoc-view-switcher" role="group" aria-label="精修执行模式">
            {canEdit && (
              <button
                type="button"
                className={`code-archdoc-view-pill ${mode === 'revise' ? 'code-archdoc-view-pill--active' : ''}`}
                onClick={() => onChangeMode('revise')}
              >
                <Sparkles size={12} />
                <span>修订提案</span>
              </button>
            )}
            <button
              type="button"
              className={`code-archdoc-view-pill ${mode === 'ask' ? 'code-archdoc-view-pill--active' : ''}`}
              onClick={() => onChangeMode('ask')}
            >
              <HelpCircle size={12} />
              <span>只读问答</span>
            </button>
          </div>

          <div className="code-archdoc-view-switcher" role="group" aria-label="作用范围">
            <button
              type="button"
              className={`code-archdoc-view-pill ${scopeType === 'selection' ? 'code-archdoc-view-pill--active' : ''}`}
              onClick={() => onChangeScopeType('selection')}
              disabled={!selection}
              title={selection ? '针对中栏划词选区' : '请先在中栏正文中划词选中片段'}
            >
              <span>选区</span>
            </button>
            <button
              type="button"
              className={`code-archdoc-view-pill ${scopeType === 'section' ? 'code-archdoc-view-pill--active' : ''}`}
              onClick={() => onChangeScopeType('section')}
              disabled={!selectedSection}
            >
              <span>本节</span>
            </button>
            <button
              type="button"
              className={`code-archdoc-view-pill ${scopeType === 'full' ? 'code-archdoc-view-pill--active' : ''}`}
              onClick={() => onChangeScopeType('full')}
            >
              <span>全文</span>
            </button>
          </div>
        </div>

        {/* 上下文定位胶囊 */}
        <div className="code-archdoc-refstudio-context-pill">
          {scopeType === 'selection' && selection ? (
            <div className="code-archdoc-refstudio-context-row">
              <span className="code-badge code-badge--info">
                选区 L{selection.start_line}-L{selection.end_line}
              </span>
              <span className="text-muted code-archdoc-truncate">
                {selection.quote_text || '已选中正文行区间'}
              </span>
              <button
                type="button"
                className="btn btn-text btn-small"
                onClick={onClearSelection}
                title="清除划词选区"
              >
                <X size={12} />
              </button>
            </div>
          ) : scopeType === 'section' && selectedSection ? (
            <div className="code-archdoc-refstudio-context-row">
              <span className="code-badge code-badge--primary">{selectedSection.section_id}</span>
              <span className="code-archdoc-truncate">{selectedSection.title}</span>
              <span className="code-archdoc-mono">
                L{selectedSection.start_line}-L{selectedSection.end_line}
              </span>
            </div>
          ) : (
            <div className="code-archdoc-refstudio-context-row">
              <span className="code-badge code-badge--muted">全文作用域</span>
              <span className="text-muted">
                {canEdit
                  ? '基于当前内存草稿全文分析（超大文档自动建议按节精修）'
                  : '只读模式：基于已落盘最新版本全文问答'}
              </span>
            </div>
          )}
        </div>

        {/* 快捷动作（仅可编辑且修订模式） */}
        {canEdit && mode === 'revise' && (
          <div className="code-archdoc-refstudio-preset-row">
            <button
              type="button"
              className="btn btn-secondary btn-small"
              disabled={submitting}
              onClick={() => void onRunRefine('polish')}
              title="规范化章节结构与表述"
            >
              <Wand2 size={12} />
              <span>润色规范化</span>
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-small"
              disabled={submitting}
              onClick={() => void onRunRefine('mermaid')}
              title="将流程与架构说明补全为 Mermaid 图表"
            >
              <Sparkles size={12} />
              <span>补全 Mermaid 图</span>
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-small"
              disabled={submitting}
              onClick={() => void onRunRefine('refresh_summary')}
              title="非破坏性提炼 L0 摘要与领域术语，不修改正文"
            >
              <Tag size={12} />
              <span>提炼摘要与术语</span>
            </button>
          </div>
        )}

        <textarea
          rows={3}
          className="code-textarea"
          value={prompt}
          onChange={(e) => onChangePrompt(e.target.value)}
          placeholder={
            mode === 'ask'
              ? '针对所选范围向 AI 追问设计约束或边界条件...'
              : '输入精修要求（如：补充幂等性与超时重试流程图）；也可直接点击上方快捷动作'
          }
          disabled={submitting}
        />

        {errorText && (
          <div className="code-archdoc-alert code-archdoc-alert--danger">
            <AlertTriangle size={15} />
            <div className="code-archdoc-alert-body">{errorText}</div>
          </div>
        )}

        {budgetHint && (
          <div className="code-archdoc-alert code-archdoc-alert--warning">
            <AlertTriangle size={15} />
            <div className="code-archdoc-alert-body">
              <div>{budgetHint}</div>
              {selectedSection && scopeType === 'full' && (
                <button
                  type="button"
                  className="btn btn-secondary btn-small"
                  onClick={() => onChangeScopeType('section')}
                >
                  切换为「本节 ({selectedSection.title})」
                </button>
              )}
            </div>
          </div>
        )}

        <div className="code-archdoc-refstudio-submit-row">
          <span className="text-muted">纯内存提案 · 采纳保存后原子升版</span>
          <div className="flex-center gap-xs">
            {turns.length > 0 && (
              <button
                type="button"
                className="btn btn-text btn-small"
                disabled={submitting}
                onClick={onClearTurns}
              >
                <Trash2 size={12} />
                <span>清空会话</span>
              </button>
            )}
            <button
              type="submit"
              className="btn btn-primary btn-small"
              disabled={submitting || !prompt.trim()}
            >
              {submitting ? <Loader2 size={13} className="code-archdoc-spin" /> : <Send size={13} />}
              <span>{submitting ? '推理中...' : mode === 'ask' ? '发送问答' : '生成精修提案'}</span>
            </button>
          </div>
        </div>
      </form>

      {/* 内存会话与提案卡片列表 */}
      <div className="code-archdoc-refstudio-turn-list">
        {turns.length === 0 ? (
          <div className="code-archdoc-studio-panel-empty">
            暂无本轮精修记录。您可以在中栏选中章节或划词片段，或直接点击快捷动作生成 AI 提案与 Unified Diff。
          </div>
        ) : (
          turns.map((turn) => {
            const resp = turn.response;
            const badge = dispositionBadge(turn.disposition);
            const isExpanded = Boolean(expandedPreflightIds[turn.id]);
            const diffLines = resp.unified_diff ? resp.unified_diff.split('\n') : [];
            const hasBodyProposal =
              resp.mode === 'revise' && Boolean(resp.proposed_fragment.trim());
            const hasSummarySuggestion = Boolean(
              resp.suggested_summary_md?.trim() ||
                (resp.suggested_declared_terms && resp.suggested_declared_terms.length > 0),
            );
            const syncSummary = syncSummaryByTurn[turn.id] ?? hasSummarySuggestion;
            const redactChecked = redactByTurn[turn.id] ?? false;
            const totalPreflightIssues =
              resp.preflight.secret_issues.length + resp.preflight.mermaid_issues.length;
            const adoptBlocked =
              !resp.preflight.can_save ||
              (resp.preflight.secret_confirm_required && !redactChecked);

            return (
              <article key={turn.id} className="code-archdoc-diff-card code-archdoc-proposal-card">
                <div className="code-archdoc-diff-card-head">
                  <div className="code-archdoc-proposal-head-left">
                    <span className="code-badge code-badge--muted">
                      {turn.mode === 'ask' ? '问答' : turn.presetAction || '精修'}
                    </span>
                    <span className="code-archdoc-mono">
                      {turn.sectionLabel || turn.scopeType}
                    </span>
                  </div>
                  <StatusTag status={badge.type} text={badge.label} size="sm" />
                </div>

                <div className="code-archdoc-refstudio-turn-prompt">
                  <span className="text-muted">指令：</span>
                  <span>{turn.prompt}</span>
                </div>

                {resp.answer_md && (
                  <div className="code-archdoc-refstudio-turn-answer">{resp.answer_md}</div>
                )}

                {/* 专属双门禁联合预检状态条 */}
                {resp.mode === 'revise' && (
                  <div className="code-archdoc-proposal-verifier">
                    <div className="code-archdoc-proposal-verifier-row">
                      {!resp.preflight.can_save ? (
                        <span className="code-badge code-badge--danger">
                          <ShieldAlert size={12} />
                          <span>
                            {resp.preflight.gate_error?.code === 'SECRET_DETECTED'
                              ? '硬凭据拦截 · 禁存'
                              : '预检阻断 · 禁存'}
                          </span>
                        </span>
                      ) : resp.preflight.secret_confirm_required ? (
                        <span className="code-badge code-badge--warning">
                          <AlertTriangle size={12} />
                          <span>疑似高熵示例 · 需确认脱敏</span>
                        </span>
                      ) : resp.preflight.mermaid_issues.length > 0 ? (
                        <span className="code-badge code-badge--warning">
                          <AlertTriangle size={12} />
                          <span>Mermaid 复杂度提醒</span>
                        </span>
                      ) : (
                        <span className="code-badge code-badge--success">
                          <CheckCircle2 size={12} />
                          <span>凭据 &amp; Mermaid 双门禁通过</span>
                        </span>
                      )}

                      {(totalPreflightIssues > 0 || resp.preflight.gate_error) && (
                        <button
                          type="button"
                          className="btn btn-text btn-small"
                          onClick={() => togglePreflight(turn.id)}
                        >
                          {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                          <span>明细 ({totalPreflightIssues})</span>
                        </button>
                      )}
                    </div>

                    {isExpanded && (
                      <ul className="code-archdoc-proposal-issues">
                        {resp.preflight.gate_error && (
                          <li className="code-archdoc-proposal-issue code-archdoc-proposal-issue--error">
                            <span className="code-archdoc-mono">[{resp.preflight.gate_error.code}]</span>
                            <span>{resp.preflight.gate_error.message}</span>
                          </li>
                        )}
                        {resp.preflight.secret_issues.map((si, idx) => (
                          <li
                            key={`sec-${idx}`}
                            className={`code-archdoc-proposal-issue code-archdoc-proposal-issue--${
                              si.severity === 'hard' ? 'error' : 'warning'
                            }`}
                          >
                            <span className="code-archdoc-mono">
                              [L{si.line_offset} · {si.rule_id}]
                            </span>
                            <span>{si.matched_mask}</span>
                          </li>
                        ))}
                        {resp.preflight.mermaid_issues.map((mi, idx) => (
                          <li
                            key={`mmd-${idx}`}
                            className={`code-archdoc-proposal-issue code-archdoc-proposal-issue--${mi.severity}`}
                          >
                            <span className="code-archdoc-mono">[{mi.issue_code}]</span>
                            <span>{mi.message}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                )}

                {/* Unified Diff 卡片 */}
                {diffLines.length > 0 && (
                  <div className="code-archdoc-diff-lines">
                    {diffLines.map((line, idx) => (
                      <div key={idx} className={classifyRefDocDiffLine(line)}>
                        {line || ' '}
                      </div>
                    ))}
                  </div>
                )}

                {/* 建议摘要与术语 */}
                {hasSummarySuggestion && (
                  <div className="code-archdoc-refstudio-summary-suggestion">
                    {resp.suggested_summary_md && (
                      <div>
                        <strong>建议 L0 摘要：</strong>
                        <span>{resp.suggested_summary_md}</span>
                      </div>
                    )}
                    {resp.suggested_declared_terms && resp.suggested_declared_terms.length > 0 && (
                      <div className="code-archdoc-refstudio-term-chips">
                        <strong>建议术语：</strong>
                        {resp.suggested_declared_terms.map((term) => (
                          <span key={term} className="code-badge code-badge--info">
                            {term}
                          </span>
                        ))}
                      </div>
                    )}
                  </div>
                )}

                {/* 4 种采纳处置动作 */}
                {canEdit && turn.disposition === 'pending' && resp.mode === 'revise' && (
                  <div className="code-archdoc-refstudio-adopt-box">
                    {hasBodyProposal && hasSummarySuggestion && (
                      <label className="code-archdoc-radio-label">
                        <input
                          type="checkbox"
                          checked={syncSummary}
                          onChange={(e) =>
                            setSyncSummaryByTurn((prev) => ({
                              ...prev,
                              [turn.id]: e.target.checked,
                            }))
                          }
                        />
                        <span>同步采纳建议摘要与术语</span>
                      </label>
                    )}

                    {hasBodyProposal && resp.preflight.secret_confirm_required && (
                      <label className="code-archdoc-radio-label">
                        <input
                          type="checkbox"
                          checked={redactChecked}
                          onChange={(e) =>
                            setRedactByTurn((prev) => ({
                              ...prev,
                              [turn.id]: e.target.checked,
                            }))
                          }
                        />
                        <span>自动脱敏疑似高熵示例为 [REDACTED] 并保存</span>
                      </label>
                    )}

                    <div className="code-archdoc-proposal-actions">
                      <button
                        type="button"
                        className="btn btn-outline btn-small"
                        disabled={savingAdoption}
                        onClick={() => onDismissTurn(turn.id)}
                      >
                        <X size={12} />
                        <span>忽略</span>
                      </button>

                      {hasBodyProposal && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-small"
                          disabled={savingAdoption}
                          onClick={() => onApplyToDraft(turn.id)}
                        >
                          <Edit3 size={12} />
                          <span>回填至草稿微调</span>
                        </button>
                      )}

                      {hasSummarySuggestion && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-small"
                          disabled={savingAdoption}
                          onClick={() => void onAdoptSummaryOnly(turn.id)}
                        >
                          <Tag size={12} />
                          <span>仅采纳摘要与术语</span>
                        </button>
                      )}

                      {hasBodyProposal && (
                        <button
                          type="button"
                          className="btn btn-primary btn-small"
                          disabled={savingAdoption || adoptBlocked}
                          title={
                            !resp.preflight.can_save
                              ? resp.preflight.gate_error?.message || '预检未通过，禁止直接保存'
                              : resp.preflight.secret_confirm_required && !redactChecked
                                ? '请先勾选自动脱敏开关，或点击回填至草稿手工清理'
                                : '保存并发布新版'
                          }
                          onClick={() =>
                            void onAdoptAndSave(turn.id, {
                              syncSummaryAndTerms: syncSummary,
                              redactAndSave: redactChecked,
                            })
                          }
                        >
                          {savingAdoption ? (
                            <Loader2 size={12} className="code-archdoc-spin" />
                          ) : (
                            <Check size={12} />
                          )}
                          <span>直接采纳并保存新版</span>
                        </button>
                      )}
                    </div>
                  </div>
                )}
              </article>
            );
          })
        )}
      </div>
    </div>
  );
};

export default RefDocRefinePanel;
