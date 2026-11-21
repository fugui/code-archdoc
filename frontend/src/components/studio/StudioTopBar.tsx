import React from 'react';
import {
  ArrowLeft,
  CheckCircle2,
  Code2,
  Download,
  History,
  ListChecks,
  Loader2,
  MessageSquareText,
  RotateCcw,
  ShieldAlert,
  Sparkles,
  Undo2,
} from 'lucide-react';
import { StatusTag } from '@code/common';
import type { DocumentDetailVO } from '../../types';
import { shortSha } from '../../utils/urlQuery';
import type { RightDrawerTab } from './RightUtilityDrawer';

export interface StudioTopBarProps {
  doc: DocumentDetailVO | null;
  repoId: number;
  repoName?: string;
  subdirectory: string;
  drawerOpen: boolean;
  activeDrawerTab?: RightDrawerTab;
  activeChatSubTab?: 'basket' | 'chat';
  pendingReviewCount?: number;
  publishing: boolean;
  unpublishing: boolean;
  exporting: boolean;
  onBackToRepos: () => void;
  onToggleDrawer: () => void;
  onOpenEvidence?: () => void;
  onOpenHistory: () => void;
  onOpenChatRefine: () => void;
  onOpenReviewBasket?: () => void;
  onRegenerate: () => void;
  onPublish: () => void;
  onUnpublish: () => void;
  onExportMarkdown: () => void;
}

function getPublishDisabledReason(doc: DocumentDetailVO | null): string | undefined {
  if (!doc) return '尚未选定文档';
  if (!doc.can_write) return '权限不足：仅仓库/子目录负责人或管理员可审批发布';
  const vr = doc.viewing_revision.verifier_report;
  if (!doc.viewing_revision.can_approve) {
    if (vr.blocker_count > 0) {
      return `发布被 Verifier 硬门禁阻断：检出 ${vr.blocker_count} 项阻断性安全告警（如明文凭据泄漏），请先修复`;
    }
    if (vr.error_count > 0) {
      return `发布被 Verifier 门禁阻断：存在 ${vr.error_count} 项校验错误，请先修复`;
    }
    return '当前修订未通过 Verifier 审批门禁，禁止发布';
  }
  return undefined;
}

export const StudioTopBar: React.FC<StudioTopBarProps> = ({
  doc,
  repoId,
  repoName,
  subdirectory,
  drawerOpen,
  activeDrawerTab = 'evidence',
  activeChatSubTab = 'chat',
  pendingReviewCount = 0,
  publishing,
  unpublishing,
  exporting,
  onBackToRepos,
  onToggleDrawer,
  onOpenEvidence,
  onOpenHistory,
  onOpenChatRefine,
  onOpenReviewBasket,
  onRegenerate,
  onPublish,
  onUnpublish,
  onExportMarkdown,
}) => {
  const publishDisabledReason = getPublishDisabledReason(doc);
  const canPublish = Boolean(doc && !publishDisabledReason && !publishing);
  const hasDrift = Boolean(doc?.drift?.code_drift?.has_drift || doc?.drift?.spec_drift?.has_drift);
  const isViewingPublished = Boolean(
    doc &&
      doc.status === 'approved' &&
      doc.published_revision_no === doc.viewing_revision.revision_no,
  );

  const isEvidenceActive = drawerOpen && activeDrawerTab === 'evidence';
  const isBasketActive = drawerOpen && activeDrawerTab === 'chat' && activeChatSubTab === 'basket';
  const isChatActive = drawerOpen && activeDrawerTab === 'chat' && activeChatSubTab !== 'basket';
  const isHistoryActive = drawerOpen && activeDrawerTab === 'history';

  const handleViewPillClick = (isActive: boolean, openView?: () => void) => {
    if (isActive) {
      onToggleDrawer();
      return;
    }
    if (openView) {
      openView();
    } else {
      onToggleDrawer();
    }
  };

  return (
    <header className="code-archdoc-studio-topbar">
      <div className="code-archdoc-studio-topbar-left">
        <button
          type="button"
          className="btn btn-secondary btn-small"
          onClick={onBackToRepos}
          title="返回仓库与文档覆盖矩阵"
        >
          <ArrowLeft size={14} />
          <span>返回列表</span>
        </button>

        <div className="code-archdoc-studio-breadcrumb">
          <span className="code-archdoc-studio-repo-name">
            {doc?.repo_name || repoName || `仓库 #${repoId}`}
          </span>
          {(doc?.subdirectory || subdirectory) && (
            <>
              <span className="text-muted">/</span>
              <span className="code-badge code-badge--purple">
                {doc?.subdirectory || subdirectory}
              </span>
            </>
          )}
          {doc && (
            <>
              <span className="text-muted">/</span>
              <span className="code-archdoc-studio-doc-name">{doc.doc_type_name}</span>
              <span className="code-archdoc-mono" title="分支">
                {doc.ref_name || 'master'}
              </span>
              <span className="code-archdoc-mono" title={`快照提交 ${doc.viewing_revision.commit_sha}`}>
                {shortSha(doc.viewing_revision.commit_sha)}
              </span>
              <span className="code-badge code-badge--muted" title="绑定的 DocType 修订号">
                模板 Rev v{doc.doc_type_revision_no}
              </span>
              <span className="code-badge code-badge--primary" title="当前查看的文档修订号">
                文档 v{doc.viewing_revision.revision_no}
              </span>
            </>
          )}
        </div>
      </div>

      <div className="code-archdoc-studio-topbar-right">
        {doc && (
          <div className="code-archdoc-studio-status-group">
            <StatusTag
              status={doc.status === 'approved' ? 'success' : 'pending'}
              text={doc.status === 'approved' ? '已发布定稿' : '草稿审校中'}
              size="sm"
            />
            {!doc.viewing_revision.can_approve && (
              <span
                className="code-badge code-badge--danger"
                title={publishDisabledReason}
              >
                <ShieldAlert size={12} />
                <span>门禁阻断</span>
              </span>
            )}
            {hasDrift && (
              <span className="code-badge code-badge--warning">
                <RotateCcw size={12} />
                <span>存在漂移</span>
              </span>
            )}
          </div>
        )}

        <div className="code-archdoc-studio-actions">
          {doc?.can_write && (
            <button
              type="button"
              className="btn btn-secondary btn-small code-archdoc-topbar-icon-btn"
              onClick={onRegenerate}
              title="基于最新代码提交重新生成（自动豁免已锁定章节）"
              aria-label="重新生成文档"
            >
              <Sparkles size={14} />
            </button>
          )}

          {doc && (
            <button
              type="button"
              className="btn btn-secondary btn-small code-archdoc-topbar-icon-btn"
              onClick={onExportMarkdown}
              disabled={exporting}
              title={
                exporting
                  ? '正在导出 Markdown...'
                  : '下载包含 YAML Frontmatter 与源码深链的 Markdown 文件'
              }
              aria-label="下载 Markdown"
            >
              {exporting ? <Loader2 size={14} className="code-archdoc-spin" /> : <Download size={14} />}
            </button>
          )}

          {doc?.can_write &&
            (isViewingPublished ? (
              <button
                type="button"
                className="btn btn-secondary btn-small code-archdoc-publish-btn code-archdoc-publish-btn--unpublish"
                onClick={onUnpublish}
                disabled={unpublishing}
                title="撤回已发布文档并清理发布目录落盘文件"
              >
                <Undo2 size={13} />
                <span>{unpublishing ? '撤回中' : '撤回'}</span>
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-secondary btn-small code-archdoc-publish-btn"
                onClick={onPublish}
                disabled={!canPublish}
                title={publishDisabledReason || '审批定稿并原子落盘至发布目录'}
              >
                <CheckCircle2 size={13} />
                <span>{publishing ? '发布中' : '发布'}</span>
              </button>
            ))}

          {doc && <span className="code-archdoc-topbar-divider" aria-hidden="true" />}

          <div className="code-archdoc-view-switcher" role="group" aria-label="右侧辅助视图">
            <button
              type="button"
              aria-pressed={isEvidenceActive}
              className={`code-archdoc-view-pill ${
                isEvidenceActive ? 'code-archdoc-view-pill--active' : ''
              }`}
              onClick={() => handleViewPillClick(isEvidenceActive, onOpenEvidence)}
              title={isEvidenceActive ? '收起源码证据面板' : '打开源码证据溯源面板'}
            >
              <Code2 size={13} />
              <span>证据</span>
            </button>

            {doc?.can_write && onOpenReviewBasket && (
              <button
                type="button"
                aria-pressed={isBasketActive}
                className={`code-archdoc-view-pill ${
                  isBasketActive ? 'code-archdoc-view-pill--active' : ''
                }`}
                onClick={() => handleViewPillClick(isBasketActive, onOpenReviewBasket)}
                title={isBasketActive ? '收起审阅批注篮' : '打开审阅批注篮，集中管理划词/段落批注并批量精修'}
              >
                <ListChecks size={13} />
                <span>批注</span>
                {pendingReviewCount > 0 && (
                  <span className="code-archdoc-view-pill-badge">{pendingReviewCount}</span>
                )}
              </button>
            )}

            {doc?.can_write && (
              <button
                type="button"
                aria-pressed={isChatActive}
                className={`code-archdoc-view-pill ${
                  isChatActive ? 'code-archdoc-view-pill--active' : ''
                }`}
                onClick={() => handleViewPillClick(isChatActive, onOpenChatRefine)}
                title={isChatActive ? '收起 AI 精修面板' : '打开 SSE Chat 局部精修与问答面板'}
              >
                <MessageSquareText size={13} />
                <span>精修</span>
              </button>
            )}

            {doc && (
              <button
                type="button"
                aria-pressed={isHistoryActive}
                className={`code-archdoc-view-pill ${
                  isHistoryActive ? 'code-archdoc-view-pill--active' : ''
                }`}
                onClick={() => handleViewPillClick(isHistoryActive, onOpenHistory)}
                title={isHistoryActive ? '收起修订历史面板' : '打开修订历史与版本 Diff 面板'}
              >
                <History size={13} />
                <span>修订</span>
              </button>
            )}
          </div>
        </div>
      </div>
    </header>
  );
};

export default StudioTopBar;
