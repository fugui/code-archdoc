import React, { useMemo } from 'react';
import { MessageSquarePlus } from 'lucide-react';
import type { ReviewCommentAnchor, ReviewCommentVO } from '../../types';
import { parseMarkdownBlocks, type MarkdownBlockNode, type MarkdownInlineNode } from '../../utils/markdown';
import {
  buildBlockAnchor,
  locateAnchorInBlocks,
  splitTextWithHighlights,
  type TextHighlightRange,
} from '../../utils/reviewAnchor';
import { parseEvidenceHref, type EvidenceDeepLinkParams } from '../../utils/urlQuery';
import MermaidZoomViewer from './MermaidZoomViewer';

export interface SectionMarkdownRendererProps {
  markdown: string;
  canWrite?: boolean;
  reviewComments?: ReviewCommentVO[];
  activeCommentId?: number | null;
  onOpenEvidenceLink?: (params: { docId: number } & EvidenceDeepLinkParams) => void;
  onAnnotateBlock?: (anchor: ReviewCommentAnchor) => void;
  onSelectComment?: (comment: ReviewCommentVO) => void;
}

function isSafeLinkHref(href: string): boolean {
  const trimmed = href.trim().toLowerCase();
  if (!trimmed) return false;
  return !(
    trimmed.startsWith('javascript:') ||
    trimmed.startsWith('data:') ||
    trimmed.startsWith('vbscript:')
  );
}

interface BlockAnnotationBucket {
  inlineRanges: TextHighlightRange[];
  blockComments: ReviewCommentVO[];
}

export const SectionMarkdownRenderer: React.FC<SectionMarkdownRendererProps> = ({
  markdown,
  canWrite = false,
  reviewComments = [],
  activeCommentId = null,
  onOpenEvidenceLink,
  onAnnotateBlock,
  onSelectComment,
}) => {
  const blocks = useMemo(() => parseMarkdownBlocks(markdown || ''), [markdown]);

  const commentById = useMemo(() => {
    const m = new Map<number, ReviewCommentVO>();
    for (const c of reviewComments) {
      m.set(c.id, c);
    }
    return m;
  }, [reviewComments]);

  const blockAnnotations = useMemo(() => {
    const byBlock = new Map<number, BlockAnnotationBucket>();
    const ensure = (idx: number): BlockAnnotationBucket => {
      let b = byBlock.get(idx);
      if (!b) {
        b = { inlineRanges: [], blockComments: [] };
        byBlock.set(idx, b);
      }
      return b;
    };

    for (const cm of reviewComments) {
      if (cm.status === 'dismissed' || cm.scope_type === 'global') continue;
      const loc = locateAnchorInBlocks(blocks, cm.anchor);
      if (loc.blockIndex < 0) continue;
      const bucket = ensure(loc.blockIndex);
      if (
        loc.startOffset !== undefined &&
        loc.endOffset !== undefined &&
        loc.endOffset > loc.startOffset
      ) {
        bucket.inlineRanges.push({
          commentId: cm.id,
          commentNo: cm.comment_no,
          startOffset: loc.startOffset,
          endOffset: loc.endOffset,
        });
      } else {
        bucket.blockComments.push(cm);
      }
    }
    return byBlock;
  }, [blocks, reviewComments]);

  const renderInlinesWithRanges = (
    nodes: MarkdownInlineNode[],
    keyPrefix: string,
    ranges: readonly TextHighlightRange[],
    cursor: { offset: number },
  ): React.ReactNode =>
    nodes.map((node, index) => {
      const key = `${keyPrefix}-${index}`;
      switch (node.type) {
        case 'text': {
          const start = cursor.offset;
          cursor.offset += node.value.length;
          if (ranges.length === 0) {
            return <React.Fragment key={key}>{node.value}</React.Fragment>;
          }
          const segs = splitTextWithHighlights(node.value, start, ranges);
          return (
            <React.Fragment key={key}>
              {segs.map((seg, sIdx) => {
                const isHl = seg.activeComments.length > 0;
                const isActiveHl = seg.activeComments.some((a) => a.commentId === activeCommentId);
                return (
                  <React.Fragment key={`${key}-s-${sIdx}`}>
                    {isHl ? (
                      <mark
                        className={`code-archdoc-review-mark ${
                          isActiveHl ? 'code-archdoc-review-mark--active' : ''
                        }`}
                      >
                        {seg.text}
                      </mark>
                    ) : (
                      seg.text
                    )}
                    {seg.endedComments.map((endCm) => {
                      const full = commentById.get(endCm.commentId);
                      return (
                        <button
                          key={`tag-${endCm.commentId}`}
                          type="button"
                          className="code-archdoc-review-inline-tag"
                          onClick={() => full && onSelectComment?.(full)}
                          title={full ? `#${full.comment_no}: ${full.content}` : `#${endCm.commentNo}`}
                        >
                          #{endCm.commentNo}
                        </button>
                      );
                    })}
                  </React.Fragment>
                );
              })}
            </React.Fragment>
          );
        }
        case 'code': {
          const start = cursor.offset;
          cursor.offset += node.value.length;
          const active = ranges.some((r) => r.endOffset > start && r.startOffset < cursor.offset);
          return (
            <code
              key={key}
              className={`code-archdoc-md-inline-code ${active ? 'code-archdoc-review-mark' : ''}`}
            >
              {node.value}
            </code>
          );
        }
        case 'strong':
          return <strong key={key}>{renderInlinesWithRanges(node.children, key, ranges, cursor)}</strong>;
        case 'em':
          return <em key={key}>{renderInlinesWithRanges(node.children, key, ranges, cursor)}</em>;
        case 'link': {
          cursor.offset += node.label.length;
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
          if (!isSafeLinkHref(node.href)) {
            return <span key={key}>{node.label}</span>;
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

  const renderBlockContent = (
    block: MarkdownBlockNode,
    idx: number,
    ranges: readonly TextHighlightRange[],
  ): React.ReactNode => {
    const key = `b-${idx}`;
    const cursor = { offset: 0 };
    switch (block.type) {
      case 'heading': {
        const level = Math.min(6, Math.max(1, block.level));
        const Tag = `h${level}` as 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';
        return (
          <Tag key={key} className={`code-archdoc-md-h code-archdoc-md-h--${level}`}>
            {renderInlinesWithRanges(block.children, key, ranges, cursor)}
          </Tag>
        );
      }
      case 'paragraph':
        return (
          <p key={key} className="code-archdoc-md-p">
            {renderInlinesWithRanges(block.children, key, ranges, cursor)}
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
            {block.blocks.map((child, childIdx) =>
              renderBlockContent(child, idx * 100 + childIdx, []),
            )}
          </blockquote>
        );
      case 'list': {
        const ListTag = block.ordered ? 'ol' : 'ul';
        return (
          <ListTag key={key} className="code-archdoc-md-list">
            {block.items.map((item, itemIdx) => {
              if (itemIdx > 0) cursor.offset += 1; // 对齐 extractBlockPlainText 中的 '\n' 拼接
              return (
                <li key={`${key}-li-${itemIdx}`}>
                  {renderInlinesWithRanges(item, `${key}-li-${itemIdx}`, ranges, cursor)}
                </li>
              );
            })}
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
                    <th key={`${key}-th-${hIdx}`}>
                      {renderInlinesWithRanges(header, `${key}-th-${hIdx}`, [], { offset: 0 })}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {block.rows.map((row, rIdx) => (
                  <tr key={`${key}-tr-${rIdx}`}>
                    {row.map((cell, cIdx) => (
                      <td key={`${key}-td-${rIdx}-${cIdx}`}>
                        {renderInlinesWithRanges(cell, `${key}-td-${rIdx}-${cIdx}`, [], {
                          offset: 0,
                        })}
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

  return (
    <div className="code-archdoc-md">
      {blocks.map((block, idx) => {
        const bucket = blockAnnotations.get(idx);
        const ranges = bucket?.inlineRanges ?? [];
        const hasBlockHl = (bucket?.blockComments.length ?? 0) > 0;
        const hasActiveBlockHl = Boolean(
          bucket?.blockComments.some((c) => c.id === activeCommentId),
        );

        return (
          <div
            key={`blk-${idx}`}
            className={`code-archdoc-md-block ${
              hasBlockHl ? 'code-archdoc-md-block--highlighted' : ''
            } ${hasActiveBlockHl ? 'code-archdoc-md-block--active' : ''}`}
            data-block-index={idx}
            data-block-type={block.type}
            data-line-start={block.startLine}
            data-line-end={block.endLine}
          >
            {renderBlockContent(block, idx, ranges)}
            {canWrite && onAnnotateBlock && block.type !== 'hr' && (
              <button
                type="button"
                className="code-archdoc-review-block-btn"
                onClick={() => onAnnotateBlock(buildBlockAnchor(block, idx))}
                title="为此段落/图表添加审阅批注"
              >
                <MessageSquarePlus size={12} />
                <span>+ 批注</span>
              </button>
            )}
          </div>
        );
      })}
    </div>
  );
};

export default SectionMarkdownRenderer;
