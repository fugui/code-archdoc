import React, { useState } from 'react';
import { AlertTriangle, Check, Columns, Eye, FileEdit, RefreshCw, X } from 'lucide-react';
import type { SectionOwnership, SectionVO } from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import SectionMarkdownRenderer from './SectionMarkdownRenderer';

export interface SectionInlineEditorProps {
  section: SectionVO;
  baseRevisionNo: number;
  onSave: (payload: {
    markdown: string;
    ownership: SectionOwnership;
    changeSummary: string;
  }) => Promise<void>;
  onCancel: () => void;
  onReloadDocument: () => Promise<void>;
}

type EditorViewMode = 'split' | 'edit' | 'preview';

export const SectionInlineEditor: React.FC<SectionInlineEditorProps> = ({
  section,
  baseRevisionNo,
  onSave,
  onCancel,
  onReloadDocument,
}) => {
  const [markdown, setMarkdown] = useState<string>(section.markdown);
  const [ownership, setOwnership] = useState<SectionOwnership>(section.ownership);
  const [changeSummary, setChangeSummary] = useState<string>('');
  const [viewMode, setViewMode] = useState<EditorViewMode>('split');
  const [saving, setSaving] = useState<boolean>(false);
  const [conflictMessage, setConflictMessage] = useState<string>('');
  const [saveError, setSaveError] = useState<string>('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setConflictMessage('');
    setSaveError('');
    try {
      await onSave({
        markdown,
        ownership,
        changeSummary: changeSummary.trim() || `手工直编章节 ${section.slot_id}`,
      });
    } catch (err: unknown) {
      const formatted = formatApiError(err, '保存章节修订失败');
      if (formatted.code === 'OPTIMISTIC_LOCK_CONFLICT') {
        setConflictMessage(formatted.message);
      } else {
        setSaveError(`${formatted.title}：${formatted.message}`);
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="code-archdoc-inline-editor" onSubmit={(e) => void handleSubmit(e)}>
      <div className="code-archdoc-inline-editor-bar">
        <div className="flex-center gap-xs">
          <button
            type="button"
            className={`btn btn-text btn-small ${viewMode === 'split' ? 'code-archdoc-editor-tab--active' : ''}`}
            onClick={() => setViewMode('split')}
          >
            <Columns size={13} />
            <span>分栏</span>
          </button>
          <button
            type="button"
            className={`btn btn-text btn-small ${viewMode === 'edit' ? 'code-archdoc-editor-tab--active' : ''}`}
            onClick={() => setViewMode('edit')}
          >
            <FileEdit size={13} />
            <span>源码</span>
          </button>
          <button
            type="button"
            className={`btn btn-text btn-small ${viewMode === 'preview' ? 'code-archdoc-editor-tab--active' : ''}`}
            onClick={() => setViewMode('preview')}
          >
            <Eye size={13} />
            <span>预览</span>
          </button>
          <span className="code-archdoc-mono" title="提交时携带乐观并发锁版本号">
            base v{baseRevisionNo}
          </span>
        </div>

        <div className="code-archdoc-inline-editor-meta">
          <select
            className="code-select code-archdoc-editor-select"
            value={ownership}
            onChange={(e) => setOwnership(e.target.value as SectionOwnership)}
            aria-label="章节所有权模式"
          >
            <option value="auto">AI 自动托管 (auto)</option>
            <option value="hybrid">人机协同 (hybrid)</option>
            <option value="locked">人工锁定 (locked)</option>
          </select>

          <input
            type="text"
            className="code-input code-archdoc-editor-summary"
            placeholder="变更说明（可选，将写入修订时间线）"
            value={changeSummary}
            onChange={(e) => setChangeSummary(e.target.value)}
            maxLength={120}
          />
        </div>
      </div>

      {/* 409 乐观锁冲突横幅 */}
      {conflictMessage && (
        <div className="code-archdoc-alert code-archdoc-alert--warning">
          <AlertTriangle size={16} />
          <div className="code-archdoc-alert-body">
            <div className="code-archdoc-alert-title">检测到并发修订冲突 (409)</div>
            <div>{conflictMessage}</div>
          </div>
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={() => void onReloadDocument()}
          >
            <RefreshCw size={13} />
            <span>刷新最新版本</span>
          </button>
        </div>
      )}

      {saveError && (
        <div className="code-archdoc-alert code-archdoc-alert--danger">
          <AlertTriangle size={16} />
          <div className="code-archdoc-alert-body">{saveError}</div>
        </div>
      )}

      <div
        className={`code-archdoc-editor-body ${
          viewMode === 'split' ? 'code-archdoc-editor-body--split' : ''
        }`}
      >
        {(viewMode === 'split' || viewMode === 'edit') && (
          <textarea
            className="code-textarea code-archdoc-editor-textarea"
            value={markdown}
            onChange={(e) => setMarkdown(e.target.value)}
            rows={14}
            aria-label={`编辑章节 ${section.title} Markdown 正文`}
          />
        )}

        {(viewMode === 'split' || viewMode === 'preview') && (
          <div className="code-archdoc-editor-preview">
            <SectionMarkdownRenderer markdown={markdown} />
          </div>
        )}
      </div>

      <div className="code-archdoc-inline-editor-footer">
        <span className="text-muted">
          保存后仅替换当前槽位，其余章节保持字节级不变并自动重跑 Verifier。
        </span>
        <div className="flex-center gap-sm">
          <button
            type="button"
            className="btn btn-secondary btn-small"
            onClick={onCancel}
            disabled={saving}
          >
            <X size={14} />
            <span>取消</span>
          </button>
          <button type="submit" className="btn btn-primary btn-small" disabled={saving}>
            <Check size={14} />
            <span>{saving ? '保存并校验中...' : '保存为新修订'}</span>
          </button>
        </div>
      </div>
    </form>
  );
};

export default SectionInlineEditor;
