import React from 'react';
import {
  ArrowLeft,
  CheckCircle2,
  Download,
  History,
  MessageSquareText,
  PanelRightClose,
  PanelRightOpen,
  RotateCcw,
  ShieldAlert,
  Sparkles,
  Undo2,
} from 'lucide-react';
import { StatusTag } from '@code/common';
import type { DocumentDetailVO } from '../../types';
import { shortSha } from '../../utils/urlQuery';

export interface StudioTopBarProps {
  doc: DocumentDetailVO | null;
  repoId: number;
  subdirectory: string;
  drawerOpen: boolean;
  publishing: boolean;
  unpublishing: boolean;
  exporting: boolean;
  onBackToRepos: () => void;
  onToggleDrawer: () => void;
  onOpenHistory: () => void;
  onOpenChatRefine: () => void;
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
  subdirectory,
  drawerOpen,
  publishing,
  unpublishing,
  exporting,
  onBackToRepos,
  onToggleDrawer,
  onOpenHistory,
  onOpenChatRefine,
  onRegenerate,
  onPublish,
  onUnpublish,
  onExportMarkdown,
}) => {
  const publishDisabledReason = getPublishDisabledReason(doc);
  const canPublish = Boolean(doc && !publishDisabledReason && !publishing);
  const hasDrift = Boolean(doc?.drift?.code_drift?.has_drift || doc?.drift?.spec_drift?.has_drift);

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
            {doc?.repo_name || `仓库 #${repoId}`}
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
              className="btn btn-secondary btn-small"
              onClick={onRegenerate}
              title="基于最新代码提交重新生成（自动豁免已锁定章节）"
            >
              <Sparkles size={14} />
              <span>重新生成</span>
            </button>
          )}

          {doc && (
            <button
              type="button"
              className="btn btn-secondary btn-small"
              onClick={onExportMarkdown}
              disabled={exporting}
              title="下载包含 YAML Frontmatter 与源码深链的 Markdown 文件"
            >
              <Download size={14} />
              <span>{exporting ? '导出中...' : '下载 MD'}</span>
            </button>
          )}

          {doc?.can_write && doc.status === 'approved' && (
            <button
              type="button"
              className="btn btn-outline btn-small"
              onClick={onUnpublish}
              disabled={unpublishing}
              title="撤回已发布文档并清理发布目录落盘文件"
            >
              <Undo2 size={14} />
              <span>{unpublishing ? '撤回中...' : '撤回发布'}</span>
            </button>
          )}

          {doc?.can_write && (
            <button
              type="button"
              className="btn btn-primary btn-small"
              onClick={onPublish}
              disabled={!canPublish}
              title={publishDisabledReason || '审批定稿并原子落盘至发布目录'}
            >
              <CheckCircle2 size={14} />
              <span>{publishing ? '发布中...' : '审批发布'}</span>
            </button>
          )}

          {doc?.can_write && (
            <button
              type="button"
              className="btn btn-secondary btn-small"
              onClick={onOpenChatRefine}
              title="打开右侧 SSE Chat 局部精修面板"
            >
              <MessageSquareText size={14} />
              <span>Chat 精修</span>
            </button>
          )}

          {doc && (
            <button
              type="button"
              className="btn btn-secondary btn-small"
              onClick={onOpenHistory}
              title="打开修订历史与版本 Diff 面板"
            >
              <History size={14} />
              <span>修订</span>
            </button>
          )}

          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={onToggleDrawer}
            title={drawerOpen ? '折叠右侧辅助面板' : '展开右侧辅助面板（源码证据 / 修订历史）'}
          >
            {drawerOpen ? <PanelRightClose size={14} /> : <PanelRightOpen size={14} />}
          </button>
        </div>
      </div>
    </header>
  );
};

export default StudioTopBar;
