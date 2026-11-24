import React from 'react';
import { AlertTriangle, Check, Loader2, RefreshCw } from 'lucide-react';
import { Modal } from '@code/common';

export interface RefDocConflictMergeModalProps {
  open: boolean;
  localVersion: number;
  serverVersion: number;
  serverContent: string;
  localDraftContent: string;
  loadingServer: boolean;
  submittingOverwrite: boolean;
  onOverwriteWithLatest: () => Promise<void>;
  onDiscardLocalAndLoadLatest: () => void;
  onClose: () => void;
}

export const RefDocConflictMergeModal: React.FC<RefDocConflictMergeModalProps> = ({
  open,
  localVersion,
  serverVersion,
  serverContent,
  localDraftContent,
  loadingServer,
  submittingOverwrite,
  onOverwriteWithLatest,
  onDiscardLocalAndLoadLatest,
  onClose,
}) => {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="检测到并发版本冲突 (409 VERSION_CONFLICT)"
      subtitle={`您的草稿基于 v${localVersion}，服务端已更新至 v${serverVersion}；本地草稿与内存提案已完整保留。`}
      width="xl"
      destroyOnClose
      footer={
        <div className="code-archdoc-refstudio-conflict-footer">
          <button
            type="button"
            className="btn btn-secondary"
            disabled={submittingOverwrite || loadingServer}
            onClick={onDiscardLocalAndLoadLatest}
          >
            <RefreshCw size={14} />
            <span>放弃本地改动并加载 v{serverVersion}</span>
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={submittingOverwrite || loadingServer}
            onClick={() => void onOverwriteWithLatest()}
          >
            {submittingOverwrite ? (
              <Loader2 size={14} className="code-archdoc-spin" />
            ) : (
              <Check size={14} />
            )}
            <span>以最新版本号 v{serverVersion} 覆盖提交</span>
          </button>
        </div>
      }
    >
      <div className="code-archdoc-refstudio-conflict-body">
        <div className="code-archdoc-alert code-archdoc-alert--warning">
          <AlertTriangle size={16} />
          <div className="code-archdoc-alert-body">
            请核对左右两栏内容差异后选择「以最新版本号覆盖提交」或「放弃本地改动加载新版」。
          </div>
        </div>

        {loadingServer ? (
          <div className="code-archdoc-loading">
            <Loader2 size={16} className="code-archdoc-spin" />
            <span>正在拉取服务端最新版 v{serverVersion}...</span>
          </div>
        ) : (
          <div className="code-archdoc-refstudio-conflict-grid">
            <div className="code-archdoc-refstudio-conflict-col">
              <div className="code-archdoc-refstudio-conflict-col-head">
                <span>服务端最新正文 (v{serverVersion})</span>
              </div>
              <textarea
                readOnly
                className="code-textarea code-archdoc-editor-textarea"
                rows={16}
                value={serverContent}
              />
            </div>
            <div className="code-archdoc-refstudio-conflict-col">
              <div className="code-archdoc-refstudio-conflict-col-head">
                <span>当前本地保留草稿（基于 v{localVersion}）</span>
              </div>
              <textarea
                readOnly
                className="code-textarea code-archdoc-editor-textarea"
                rows={16}
                value={localDraftContent}
              />
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
};

export default RefDocConflictMergeModal;
