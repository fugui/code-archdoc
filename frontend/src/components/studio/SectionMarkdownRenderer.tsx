import React, { useMemo } from 'react';
import {
  parseMarkdownBlocks,
  type MarkdownBlockNode,
  type MarkdownInlineNode,
} from '../../utils/markdown';
import { parseEvidenceHref, type EvidenceDeepLinkParams } from '../../utils/urlQuery';
import MermaidZoomViewer from './MermaidZoomViewer';

export interface SectionMarkdownRendererProps {
  markdown: string;
  onOpenEvidenceLink?: (params: { docId: number } & EvidenceDeepLinkParams) => void;
}

export const SectionMarkdownRenderer: React.FC<SectionMarkdownRendererProps> = ({
  markdown,
  onOpenEvidenceLink,
}) => {
  const blocks = useMemo(() => parseMarkdownBlocks(markdown || ''), [markdown]);

  const renderInlines = (nodes: MarkdownInlineNode[], keyPrefix: string): React.ReactNode =>
    nodes.map((node, index) => {
      const key = `${keyPrefix}-${index}`;
      switch (node.type) {
        case 'text':
          return <React.Fragment key={key}>{node.value}</React.Fragment>;
        case 'code':
          return (
            <code key={key} className="code-archdoc-md-inline-code">
              {node.value}
            </code>
          );
        case 'strong':
          return <strong key={key}>{renderInlines(node.children, key)}</strong>;
        case 'em':
          return <em key={key}>{renderInlines(node.children, key)}</em>;
        case 'link': {
          const evidence = parseEvidenceHref(node.href);
          if (evidence && onOpenEvidenceLink) {
            return (
              <button
                key={key}
                type="button"
                className="code-archdoc-md-evidence-link"
                onClick={() => onOpenEvidenceLink(evidence)}
                title={`在右侧抽屉查看源码证据：${evidence.file || ''}`}
              >
                {node.label}
              </button>
            );
          }
          const isExternal = node.href.startsWith('http://') || node.href.startsWith('https://');
          return (
            <a
              key={key}
              href={node.href}
              className="code-archdoc-md-link"
              target={isExternal ? '_blank' : undefined}
              rel={isExternal ? 'noopener noreferrer' : undefined}
            >
              {node.label}
            </a>
          );
        }
      }
    });

  const renderBlock = (block: MarkdownBlockNode, idx: number): React.ReactNode => {
    const key = `b-${idx}`;
    switch (block.type) {
      case 'heading': {
        const level = Math.min(6, Math.max(1, block.level));
        const Tag = `h${level}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';
        return (
          <Tag key={key} className={`code-archdoc-md-h code-archdoc-md-h--${level}`}>
            {renderInlines(block.children, key)}
          </Tag>
        );
      }
      case 'paragraph':
        return (
          <p key={key} className="code-archdoc-md-p">
            {renderInlines(block.children, key)}
          </p>
        );
      case 'code':
        return (
          <div key={key} className="code-archdoc-md-code-wrap">
            {block.language && block.language !== 'text' && (
              <div className="code-archdoc-md-code-lang">{block.language}</div>
            )}
            <pre className="code-archdoc-code-block">
              <code>{block.code}</code>
            </pre>
          </div>
        );
      case 'mermaid':
        return <MermaidZoomViewer key={key} chart={block.chart} />;
      case 'blockquote':
        return (
          <blockquote key={key} className="code-archdoc-md-quote">
            {block.blocks.map((child, childIdx) => renderBlock(child, idx * 100 + childIdx))}
          </blockquote>
        );
      case 'list': {
        const ListTag = block.ordered ? 'ol' : 'ul';
        return (
          <ListTag key={key} className="code-archdoc-md-list">
            {block.items.map((item, itemIdx) => (
              <li key={`${key}-li-${itemIdx}`}>{renderInlines(item, `${key}-li-${itemIdx}`)}</li>
            ))}
          </ListTag>
        );
      }
      case 'table':
        return (
          <div key={key} className="code-archdoc-md-table-wrap">
            <table className="code-table">
              <thead>
                <tr>
                  {block.headers.map((header, hIdx) => (
                    <th key={`${key}-th-${hIdx}`}>{renderInlines(header, `${key}-th-${hIdx}`)}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {block.rows.map((row, rIdx) => (
                  <tr key={`${key}-tr-${rIdx}`}>
                    {row.map((cell, cIdx) => (
                      <td key={`${key}-td-${rIdx}-${cIdx}`}>
                        {renderInlines(cell, `${key}-td-${rIdx}-${cIdx}`)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      case 'hr':
        return <hr key={key} className="code-archdoc-md-hr" />;
    }
  };

  if (blocks.length === 0) {
    return <div className="text-muted">该章节暂无正文内容。</div>;
  }

  return <div className="code-archdoc-md">{blocks.map((block, i) => renderBlock(block, i))}</div>;
};

export default SectionMarkdownRenderer;
