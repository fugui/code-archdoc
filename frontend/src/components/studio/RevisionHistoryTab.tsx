import React, { useCallback, useEffect, useState } from 'react';
import {
  AlertTriangle,
  GitCompare,
  History,
  Loader2,
  RotateCcw,
} from 'lucide-react';
import { EmptyState, StatusTag, useConfirm, useToast } from '@code/common';
import {
  diffDocumentRevisions,
  listDocumentRevisions,
  rollbackDocument,
} from '../../api/documents';
import type {
  DocumentDetailVO,
  DocumentRevisionSummaryVO,
  RevisionDiffVO,
  RevisionSource,
} from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import { formatDateTime, shortSha } from '../../utils/urlQuery';

export interface RevisionHistoryTabProps {
  doc: DocumentDetailVO;
  onRolledBack: (updatedDoc: DocumentDetailVO) => void;
}

const SOURCE_LABELS: Record<RevisionSource, string> = {
  ai_generate: 'AI 首次生成',
  incremental_refresh: '增量漂移刷新',
  manual_edit: '手工直编',
  chat_refine: 'Chat 精修采纳',
  rollback: '版本回滚',
};

export const RevisionHistoryTab: React.FC<RevisionHistoryTabProps> = ({ doc, onRolledBack }) => {
  const confirm = useConfirm();
  const { showToast } = useToast();

  const [revisions, setRevisions] = useState<DocumentRevisionSummaryVO[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [errorMsg, setErrorMsg] = useState<string>('');

  const [fromRev, setFromRev] = useState<number>(0);
  const [toRev, setToRev] = useState<number>(0);
  const [diffResult, setDiffResult] = useState<RevisionDiffVO | null>(null);
  const [diffLoading, setDiffLoading] = useState<boolean>(false);
  const [rollingBackRev, setRollingBackRev] = useState<number | null>(null);

  const loadRevisions = useCallback(async () => {
    if (!doc.can_write) {
      setLoading(false);
      setRevisions([]);
      return;
    }

    setLoading(true);
    setErrorMsg('');
    try {
      const list = await listDocumentRevisions(doc.id);
      setRevisions(list);
      if (list.length >= 2) {
        setToRev(list[0].revision_no);
        setFromRev(list[1].revision_no);
      } else if (list.length === 1) {
        setToRev(list[0].revision_no);
        setFromRev(list[0].revision_no);
      }
    } catch (err: unknown) {
      setErrorMsg(formatApiError(err, '加载修订历史失败').message);
    } finally {
      setLoading(false);
    }
  }, [doc.id, doc.can_write]);

  useEffect(() => {
    void loadRevisions();
  }, [loadRevisions, doc.current_revision_no]);

  const handleCompare = useCallback(
    async (leftRev: number, rightRev: number) => {
      if (!leftRev || !rightRev || leftRev === rightRev) {
        setDiffResult(null);
        return;
      }
      setDiffLoading(true);
      try {
        const res = await diffDocumentRevisions(doc.id, leftRev, rightRev);
        setDiffResult(res);
      } catch (err: unknown) {
        showToast(formatApiError(err, '计算版本 Diff 失败').message, 'error');
      } finally {
        setDiffLoading(false);
      }
    },
    [doc.id, showToast],
  );

  useEffect(() => {
    if (fromRev > 0 && toRev > 0 && fromRev !== toRev) {
      void handleCompare(fromRev, toRev);
    } else {
      setDiffResult(null);
    }
  }, [fromRev, toRev, handleCompare]);

  const handleRollback = async (targetRev: DocumentRevisionSummaryVO) => {
    const ok = await confirm({
      title: `确认回滚至修订 v${targetRev.revision_no}`,
      content: `回滚不会删除任何历史记录，而是将 v${targetRev.revision_no} 的内容作为全新不可变修订（v${doc.current_revision_no + 1}）落库并重跑 Verifier 门禁。是否继续？`,
      confirmText: '确认回滚',
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;

    setRollingBackRev(targetRev.revision_no);
    try {
      const updated = await rollbackDocument(doc.id, {
        base_revision_no: doc.current_revision_no,
        target_revision_no: targetRev.revision_no,
        change_summary: `一键回滚至历史修订 v${targetRev.revision_no}`,
      });
      showToast(
        `已回滚至 v${targetRev.revision_no}，生成新修订 v${updated.current_revision_no}`,
        'success',
      );
      onRolledBack(updated);
      await loadRevisions();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '版本回滚失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setRollingBackRev(null);
    }
  };

  if (!doc.can_write) {
    return (
      <EmptyState
        compact
        type="permission"
        title="修订历史仅对负责人开放"
        description="您当前处于已发布文档只读视角，仅仓库/子目录负责人或管理员可查看草稿修订时间线与执行回滚。"
      />
    );
  }

  if (loading) {
    return (
      <div className="code-archdoc-loading">
        <Loader2 size={16} className="code-archdoc-spin" />
        <span>加载修订时间线...</span>
      </div>
    );
  }

  if (errorMsg) {
    return (
      <div className="code-archdoc-alert code-archdoc-alert--danger">
        <AlertTriangle size={16} />
        <div className="code-archdoc-alert-body">{errorMsg}</div>
      </div>
    );
  }

  if (revisions.length === 0) {
    return (
      <EmptyState
        compact
        icon={<History size={30} />}
        title="暂无修订记录"
        description="生成或手工编辑文档后将自动记录不可变修订快照。"
      />
    );
  }

  return (
    <div className="code-archdoc-history-tab">
      {/* 1. 任意两版本章节 Diff 选择器 */}
      {revisions.length >= 2 && (
        <div className="code-archdoc-diff-toolbar">
          <div className="code-archdoc-diff-toolbar-title">
            <GitCompare size={14} />
            <span>章节级 Unified Diff 对比</span>
          </div>
          <div className="code-archdoc-diff-selectors">
            <select
              className="code-select code-archdoc-diff-select"
              value={fromRev}
              onChange={(e) => setFromRev(Number(e.target.value))}
              aria-label="基准版本"
            >
              {revisions.map((r) => (
                <option key={`from-${r.revision_no}`} value={r.revision_no}>
                  基准 v{r.revision_no}
                </option>
              ))}
            </select>
            <span className="text-muted">→</span>
            <select
              className="code-select code-archdoc-diff-select"
              value={toRev}
              onChange={(e) => setToRev(Number(e.target.value))}
              aria-label="目标版本"
            >
              {revisions.map((r) => (
                <option key={`to-${r.revision_no}`} value={r.revision_no}>
                  目标 v{r.revision_no}
                </option>
              ))}
            </select>
          </div>
        </div>
      )}

      {/* Diff 展示区 */}
      {diffLoading && (
        <div className="code-archdoc-loading">
          <Loader2 size={15} className="code-archdoc-spin" />
          <span>计算章节差异...</span>
        </div>
      )}

      {!diffLoading && diffResult && (
        <div className="code-archdoc-diff-list">
          {diffResult.section_diffs.filter((s) => s.changed).length === 0 ? (
            <div className="code-archdoc-alert code-archdoc-alert--info">
              <div className="code-archdoc-alert-body">
                v{diffResult.from_rev} 与 v{diffResult.to_rev} 之间所有章节正文完全一致。
              </div>
            </div>
          ) : (
            diffResult.section_diffs
              .filter((s) => s.changed)
              .map((sec) => (
                <div key={sec.slot_id} className="code-archdoc-diff-card">
                  <div className="code-archdoc-diff-card-head">
                    <span className="text-truncate" title={sec.title}>
                      {sec.title}
                    </span>
                    <div className="flex-center gap-xs">
                      <span className="code-badge code-badge--success">+{sec.added_lines}</span>
                      <span className="code-badge code-badge--danger">-{sec.removed_lines}</span>
                    </div>
                  </div>
                  <div className="code-archdoc-diff-lines">
                    {sec.unified_diff.split('\n').map((line, i) => {
                      const cls = line.startsWith('@@')
                        ? 'code-archdoc-diff-line--hunk'
                        : line.startsWith('+')
                          ? 'code-archdoc-diff-line--add'
                          : line.startsWith('-')
                            ? 'code-archdoc-diff-line--del'
                            : '';
                      return (
                        <div key={i} className={`code-archdoc-diff-line ${cls}`}>
                          {line || ' '}
                        </div>
                      );
                    })}
                  </div>
                </div>
              ))
          )}
        </div>
      )}

      {/* 2. 不可变修订时间线 */}
      <div className="code-archdoc-timeline">
        {revisions.map((rev) => {
          const isCurrent = rev.revision_no === doc.current_revision_no;
          const isPublished =
            doc.status === 'approved' && rev.revision_no === doc.published_revision_no;

          return (
            <div
              key={rev.id}
              className={`code-archdoc-timeline-item ${
                isCurrent ? 'code-archdoc-timeline-item--current' : ''
              }`}
            >
              <div className="code-archdoc-timeline-head">
                <div className="flex-center gap-xs flex-wrap">
                  <strong>v{rev.revision_no}</strong>
                  <span className="code-badge code-badge--muted">
                    {SOURCE_LABELS[rev.source] || rev.source}
                  </span>
                  {isCurrent && <span className="code-badge code-badge--primary">当前版本</span>}
                  {isPublished && <span className="code-badge code-badge--success">已发布</span>}
                </div>
                <StatusTag
                  status={rev.can_approve ? 'success' : 'failed'}
                  text={rev.can_approve ? '可发布' : '阻断'}
                  size="sm"
                />
              </div>

              {rev.change_summary && (
                <div className="code-archdoc-timeline-summary">{rev.change_summary}</div>
              )}

              <div className="code-archdoc-timeline-foot">
                <span className="text-muted">
                  <code className="code-archdoc-mono">{shortSha(rev.commit_sha)}</code> ·{' '}
                  {formatDateTime(rev.created_at)}
                </span>

                {!isCurrent && (
                  <button
                    type="button"
                    className="btn btn-outline btn-small"
                    disabled={rollingBackRev !== null}
                    onClick={() => void handleRollback(rev)}
                  >
                    <RotateCcw size={12} />
                    <span>{rollingBackRev === rev.revision_no ? '回滚中...' : '回滚至此版'}</span>
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

export default RevisionHistoryTab;
