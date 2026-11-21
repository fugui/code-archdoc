import React, { useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  AlertTriangle,
  BookOpen,
  CheckCircle2,
  Plus,
  Wand2,
} from 'lucide-react';
import type { BlueprintDiagnostic } from '../../utils/docTypeBlueprint';

export interface BlueprintSourceEditorProps {
  source: string;
  diagnostics: BlueprintDiagnostic[];
  existingSlotIds: ReadonlySet<string>;
  onChange: (nextSource: string) => void;
  onFormat: () => void;
}

export const BlueprintSourceEditor: React.FC<BlueprintSourceEditorProps> = ({
  source,
  diagnostics,
  existingSlotIds,
  onChange,
  onFormat,
}) => {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const [showCheatsheet, setShowCheatsheet] = useState<boolean>(false);

  const errorCount = useMemo(
    () => diagnostics.filter((d) => d.severity === 'error').length,
    [diagnostics],
  );
  const warningCount = useMemo(
    () => diagnostics.filter((d) => d.severity === 'warning').length,
    [diagnostics],
  );

  const generateUniqueSlotId = (): string => {
    let idx = existingSlotIds.size + 1;
    let candidate = `custom_section_${idx}`;
    while (existingSlotIds.has(candidate) || source.includes(`slot ${candidate}`)) {
      idx += 1;
      candidate = `custom_section_${idx}`;
    }
    return candidate;
  };

  const handleInsertSnippet = () => {
    const slotId = generateUniqueSlotId();
    const snippet = [
      `## 新章节标题`,
      ``,
      `\`\`\`slot ${slotId}`,
      `ownership: auto`,
      `required: true`,
      `ir_facets: [tree, deps]`,
      `token_budget: 8000`,
      `min_chars: 40`,
      `enable_source_tools: false`,
      `hint: 请说明本章节需要梳理的核心架构事实。`,
      `\`\`\``,
    ].join('\n');

    const el = textareaRef.current;
    if (!el) {
      const prefix = source === '' || source.endsWith('\n\n') ? '' : source.endsWith('\n') ? '\n' : '\n\n';
      onChange(`${source}${prefix}${snippet}\n`);
      return;
    }

    const start = el.selectionStart ?? source.length;
    const end = el.selectionEnd ?? source.length;
    const before = source.slice(0, start);
    const after = source.slice(end);
    const needLeadNL = before !== '' && !before.endsWith('\n\n') ? (before.endsWith('\n') ? '\n' : '\n\n') : '';
    const needTailNL = after !== '' && !after.startsWith('\n\n') ? (after.startsWith('\n') ? '\n' : '\n\n') : '\n';
    const nextText = `${before}${needLeadNL}${snippet}${needTailNL}${after}`;
    onChange(nextText);

    const nextCaret = before.length + needLeadNL.length + snippet.length;
    window.requestAnimationFrame(() => {
      if (textareaRef.current) {
        textareaRef.current.focus();
        textareaRef.current.setSelectionRange(nextCaret, nextCaret);
      }
    });
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== 'Tab') return;
    e.preventDefault();
    const el = e.currentTarget;
    const start = el.selectionStart ?? 0;
    const end = el.selectionEnd ?? 0;
    const nextText = `${source.slice(0, start)}  ${source.slice(end)}`;
    onChange(nextText);
    window.requestAnimationFrame(() => {
      el.setSelectionRange(start + 2, start + 2);
    });
  };

  return (
    <div className="code-archdoc-blueprint-source-wrap">
      <div className="code-archdoc-blueprint-source-toolbar">
        <div className="code-archdoc-blueprint-source-actions">
          <button
            type="button"
            className="btn btn-outline btn-small"
            onClick={handleInsertSnippet}
          >
            <Plus size={13} />
            <span>插入槽位片段</span>
          </button>
          <button
            type="button"
            className="btn btn-outline btn-small"
            disabled={errorCount > 0}
            onClick={onFormat}
            title={errorCount > 0 ? '请先修复语法错误后再格式化' : '规范化 DSL 缩进与字段顺序'}
          >
            <Wand2 size={13} />
            <span>格式化源码</span>
          </button>
          <button
            type="button"
            className={`btn btn-small ${showCheatsheet ? 'btn-primary' : 'btn-secondary'}`}
            onClick={() => setShowCheatsheet((prev) => !prev)}
          >
            <BookOpen size={13} />
            <span>DSL 语法速查</span>
          </button>
        </div>

        <div className="code-archdoc-blueprint-diag-badges">
          {errorCount > 0 ? (
            <span className="code-badge code-badge--danger">
              <AlertCircle size={12} />
              <span>{errorCount} 个语法错误（阻断切换与保存）</span>
            </span>
          ) : warningCount > 0 ? (
            <span className="code-badge code-badge--warning">
              <AlertTriangle size={12} />
              <span>{warningCount} 条建议提醒</span>
            </span>
          ) : (
            <span className="code-badge code-badge--success">
              <CheckCircle2 size={12} />
              <span>DSL 语法校验通过</span>
            </span>
          )}
        </div>
      </div>

      {showCheatsheet && (
        <div className="code-archdoc-blueprint-cheatsheet">
          <div className="code-archdoc-blueprint-cheatsheet-title">
            Blueprint Source DSL v1 语法速查
          </div>
          <div className="code-archdoc-blueprint-cheatsheet-grid">
            <div>
              <strong>基本结构：</strong>
              上方写标准 Markdown 标题（<code>##</code>/<code>###</code>/<code>####</code>），下方紧接{' '}
              <code>```slot &lt;slot_id&gt;</code> 围栏块（<code>slot_id</code> 匹配{' '}
              <code>^[a-z][a-z0-9_]&#123;1,48&#125;$</code>）。普通代码围栏（如{' '}
              <code>```mermaid</code>）会自动透传。
            </div>
            <div>
              <strong>支持参数（含别名与默认值）：</strong>
              <code>ownership</code>（auto|hybrid|locked，默认 auto）、<code>required</code>（默认 true）、
              <code>ir_facets</code> / <code>facets</code>（9 大规范分片，拒绝 module_context）、
              <code>token_budget</code> / <code>budget</code>（500~64000，默认 8000）、
              <code>min_chars</code>（0~10000，默认 40）、<code>enable_source_tools</code> /{' '}
              <code>source_tools</code>（默认 false）、<code>max_tool_rounds</code> /{' '}
              <code>tool_rounds</code>（0~6）、<code>hint</code> / <code>instruction_hint</code>（支持{' '}
              <code>|</code> 多行缩进块，最多 1000 字）。无紧邻标题时须显式写 <code>title:</code>。
            </div>
          </div>
        </div>
      )}

      <textarea
        ref={textareaRef}
        className="code-archdoc-code-editor code-archdoc-blueprint-source-textarea"
        rows={20}
        spellCheck={false}
        value={source}
        onKeyDown={handleKeyDown}
        onChange={(e) => onChange(e.target.value)}
        placeholder={'# 文档主标题\n\n## 1. 章节标题\n\n```slot overview_summary\nir_facets: [tree, deps]\nhint: 结合目录树与依赖说明边界。\n```'}
      />

      {diagnostics.length > 0 && (
        <div className="code-archdoc-blueprint-diag-list" role="status">
          {diagnostics.map((diag, idx) => (
            <div
              key={`${diag.line}-${diag.code}-${idx}`}
              className={`code-archdoc-blueprint-diag-item code-archdoc-blueprint-diag-item--${diag.severity}`}
            >
              {diag.severity === 'error' ? <AlertCircle size={14} /> : <AlertTriangle size={14} />}
              <span>{diag.message}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

export default BlueprintSourceEditor;
