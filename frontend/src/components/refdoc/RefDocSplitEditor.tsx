import React, { useState } from 'react';
import { Check, Columns, Eye, FileEdit, X } from 'lucide-react';
import SectionMarkdownRenderer from '../studio/SectionMarkdownRenderer';

export interface RefDocSplitEditorProps {
  initialMarkdown: string;
  title: string;
  saveLabel?: string;
  helperText?: string;
  rows?: number;
  onApply: (markdown: string) => void;
  onCancel: () => void;
}

type EditorViewMode = 'split' | 'edit' | 'preview';

export const RefDocSplitEditor: React.FC<RefDocSplitEditorProps> = ({
  initialMarkdown,
  title,
  saveLabel = '应用至本地草稿',
  helperText = '应用后将实时更新章节大纲与 Token 估算，确认无误后请在顶部保存发布新版。',
  rows = 16,
  onApply,
  onCancel,
}) => {
  const [markdown, setMarkdown] = useState<string>(initialMarkdown);
  const [viewMode, setViewMode] = useState<EditorViewMode>('split');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    onApply(markdown);
  };

  return (
    <form className="code-archdoc-inline-editor" onSubmit={handleSubmit}>
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
        </div>
        <span className="code-archdoc-mono">{title}</span>
      </div>

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
            rows={rows}
            aria-label={`编辑 ${title} Markdown 正文`}
          />
        )}

        {(viewMode === 'split' || viewMode === 'preview') && (
          <div className="code-archdoc-editor-preview">
            <SectionMarkdownRenderer markdown={markdown} />
          </div>
        )}
      </div>

      <div className="code-archdoc-inline-editor-footer">
        <span className="text-muted">{helperText}</span>
        <div className="flex-center gap-sm">
          <button type="button" className="btn btn-secondary btn-small" onClick={onCancel}>
            <X size={14} />
            <span>取消</span>
          </button>
          <button type="submit" className="btn btn-primary btn-small">
            <Check size={14} />
            <span>{saveLabel}</span>
          </button>
        </div>
      </div>
    </form>
  );
};

export default RefDocSplitEditor;
