import type { ReviewCommentAnchor } from '../types/domain';
import type { MarkdownBlockNode, MarkdownInlineNode } from './markdown';

export const MAX_REVIEW_QUOTE_LENGTH = 500;
export const MAX_REVIEW_CONTEXT_LENGTH = 48;

export type AnchorMatchLevel = 'exact' | 'relocated' | 'block' | 'detached';

export interface AnchorLocateResult {
  matchLevel: AnchorMatchLevel;
  blockIndex: number;
  startOffset?: number;
  endOffset?: number;
}

export interface TextHighlightRange {
  commentId: number;
  commentNo: number;
  startOffset: number;
  endOffset: number;
}

export interface TextHighlightSegment {
  text: string;
  activeComments: Array<{ commentId: number; commentNo: number }>;
  endedComments: Array<{ commentId: number; commentNo: number }>;
}

/**
 * 提取行内 AST 节点的纯文本表示，与 SectionMarkdownRenderer 的渲染字符流严格对齐。
 */
export function extractInlinePlainText(nodes: readonly MarkdownInlineNode[]): string {
  let out = '';
  for (const node of nodes) {
    switch (node.type) {
      case 'text':
      case 'code':
        out += node.value;
        break;
      case 'strong':
      case 'em':
        out += extractInlinePlainText(node.children);
        break;
      case 'link':
        out += node.label;
        break;
    }
  }
  return out;
}

/**
 * 提取顶层 Markdown 块的扁平化纯文本，用于划词偏移捕获与一级/二级文本锚定。
 */
export function extractBlockPlainText(block: MarkdownBlockNode): string {
  switch (block.type) {
    case 'heading':
    case 'paragraph':
      return extractInlinePlainText(block.children);
    case 'list':
      return block.items.map((item) => extractInlinePlainText(item)).join('\n');
    case 'table': {
      const headerRow = block.headers.map((cell) => extractInlinePlainText(cell)).join(' | ');
      const bodyRows = block.rows.map((row) =>
        row.map((cell) => extractInlinePlainText(cell)).join(' | '),
      );
      return [headerRow, ...bodyRows].filter(Boolean).join('\n');
    }
    case 'blockquote':
      return block.blocks.map((child) => extractBlockPlainText(child)).join('\n');
    case 'code':
      return block.code;
    case 'mermaid':
      return block.chart;
    case 'hr':
      return '';
  }
}

export function buildInlineAnchorFromText(
  blockText: string,
  startOffset: number,
  endOffset: number,
  blockIndex: number,
  blockType: string,
  sourceLineStart?: number,
  sourceLineEnd?: number,
): ReviewCommentAnchor {
  const safeStart = Math.max(0, Math.min(startOffset, blockText.length));
  const safeEnd = Math.max(safeStart, Math.min(endOffset, blockText.length));
  const rawQuote = blockText.slice(safeStart, safeEnd).trim();
  const quote =
    rawQuote.length > MAX_REVIEW_QUOTE_LENGTH
      ? rawQuote.slice(0, MAX_REVIEW_QUOTE_LENGTH)
      : rawQuote;
  const prefixStart = Math.max(0, safeStart - MAX_REVIEW_CONTEXT_LENGTH);
  const prefix = blockText.slice(prefixStart, safeStart);
  const suffix = blockText.slice(safeEnd, safeEnd + MAX_REVIEW_CONTEXT_LENGTH);

  return {
    block_index: blockIndex,
    block_type: blockType,
    quote,
    prefix: prefix || undefined,
    suffix: suffix || undefined,
    source_line_start: sourceLineStart,
    source_line_end: sourceLineEnd,
  };
}

export function buildBlockAnchor(
  block: MarkdownBlockNode,
  blockIndex: number,
): ReviewCommentAnchor {
  let quote = '[段落]';
  switch (block.type) {
    case 'mermaid':
      quote = '[Mermaid 图表]';
      break;
    case 'table': {
      const cols = block.headers
        .slice(0, 3)
        .map((h) => extractInlinePlainText(h).trim())
        .filter(Boolean);
      quote = cols.length > 0 ? `[表格: ${cols.join(' / ')}]` : '[表格]';
      break;
    }
    case 'code':
      quote = `[代码块: ${block.language || 'text'}]`;
      break;
    default: {
      const plain = extractBlockPlainText(block).trim().replace(/\s+/g, ' ');
      if (plain) {
        quote = plain.length > 80 ? `${plain.slice(0, 80)}...` : plain;
      }
      break;
    }
  }

  return {
    block_index: blockIndex,
    block_type: block.type,
    quote,
    source_line_start: block.startLine,
    source_line_end: block.endLine,
  };
}

function isBlockPlaceholderAnchor(anchor: ReviewCommentAnchor): boolean {
  const q = (anchor.quote ?? '').trim();
  if (!q) return true;
  return (
    q === '[Mermaid 图表]' ||
    q === '[表格]' ||
    q.startsWith('[表格:') ||
    q.startsWith('[代码块:') ||
    q === '[段落]'
  );
}

function findBestMatchInText(
  plainText: string,
  quote: string,
  prefix: string,
  suffix: string,
): { startOffset: number; endOffset: number; score: number } | null {
  if (!quote || !plainText) return null;
  const occurrences: number[] = [];
  let cursor = 0;
  while (cursor <= plainText.length - quote.length) {
    const found = plainText.indexOf(quote, cursor);
    if (found < 0) break;
    occurrences.push(found);
    cursor = found + 1;
  }
  if (occurrences.length === 0) return null;

  let best: { startOffset: number; endOffset: number; score: number } | null = null;
  for (const idx of occurrences) {
    let score = occurrences.length === 1 ? 10 : 1;
    if (prefix) {
      const before = plainText.slice(Math.max(0, idx - prefix.length), idx);
      if (before === prefix) {
        score += 100;
      } else if (before.endsWith(prefix.slice(-Math.min(12, prefix.length)))) {
        score += 30;
      }
    }
    if (suffix) {
      const afterStart = idx + quote.length;
      const after = plainText.slice(afterStart, afterStart + suffix.length);
      if (after === suffix) {
        score += 100;
      } else if (after.startsWith(suffix.slice(0, Math.min(12, suffix.length)))) {
        score += 30;
      }
    }
    if (!best || score > best.score) {
      best = {
        startOffset: idx,
        endOffset: idx + quote.length,
        score,
      };
    }
  }
  return best;
}

/**
 * 三级降级定位算法：
 * Level 1: 同 block_index 精确上下文命中；
 * Level 2: 跨块滑窗重排容错；
 * Level 3: 块级整体高亮或判定 detached。
 */
export function locateAnchorInBlocks(
  blocks: readonly MarkdownBlockNode[],
  anchor: ReviewCommentAnchor | undefined,
): AnchorLocateResult {
  if (!anchor || blocks.length === 0) {
    return { matchLevel: 'detached', blockIndex: -1 };
  }

  const blockIdx = typeof anchor.block_index === 'number' ? anchor.block_index : -1;
  const quote = (anchor.quote ?? '').trim();
  const prefix = anchor.prefix ?? '';
  const suffix = anchor.suffix ?? '';

  if (isBlockPlaceholderAnchor(anchor)) {
    if (
      blockIdx >= 0 &&
      blockIdx < blocks.length &&
      (!anchor.block_type || blocks[blockIdx].type === anchor.block_type)
    ) {
      return { matchLevel: 'block', blockIndex: blockIdx };
    }
    if (anchor.block_type) {
      const candidates = blocks
        .map((b, idx) => ({ b, idx }))
        .filter((item) => item.b.type === anchor.block_type);
      if (candidates.length === 1) {
        return { matchLevel: 'block', blockIndex: candidates[0].idx };
      }
      if (candidates.length > 1 && typeof anchor.source_line_start === 'number') {
        candidates.sort(
          (a, b) =>
            Math.abs((a.b.startLine ?? 0) - anchor.source_line_start!) -
            Math.abs((b.b.startLine ?? 0) - anchor.source_line_start!),
        );
        return { matchLevel: 'block', blockIndex: candidates[0].idx };
      }
    }
    return { matchLevel: 'detached', blockIndex: -1 };
  }

  // Level 1：同块精确上下文命中
  if (blockIdx >= 0 && blockIdx < blocks.length) {
    const plain = extractBlockPlainText(blocks[blockIdx]);
    const matched = findBestMatchInText(plain, quote, prefix, suffix);
    if (matched) {
      return {
        matchLevel: 'exact',
        blockIndex: blockIdx,
        startOffset: matched.startOffset,
        endOffset: matched.endOffset,
      };
    }
  }

  // Level 2：跨块重排滑窗容错
  let bestGlobal: { idx: number; startOffset: number; endOffset: number; score: number } | null = null;
  for (let i = 0; i < blocks.length; i += 1) {
    if (i === blockIdx) continue;
    const plain = extractBlockPlainText(blocks[i]);
    const matched = findBestMatchInText(plain, quote, prefix, suffix);
    if (!matched) continue;
    let totalScore = matched.score;
    if (anchor.block_type && blocks[i].type === anchor.block_type) {
      totalScore += 15;
    }
    if (blockIdx >= 0) {
      totalScore -= Math.min(10, Math.abs(i - blockIdx));
    }
    if (!bestGlobal || totalScore > bestGlobal.score) {
      bestGlobal = {
        idx: i,
        startOffset: matched.startOffset,
        endOffset: matched.endOffset,
        score: totalScore,
      };
    }
  }
  if (bestGlobal) {
    return {
      matchLevel: 'relocated',
      blockIndex: bestGlobal.idx,
      startOffset: bestGlobal.startOffset,
      endOffset: bestGlobal.endOffset,
    };
  }

  // Level 3：表格/代码块/Mermaid 无法文本匹配时若块类型一致可降级为块级，否则 detached
  if (
    blockIdx >= 0 &&
    blockIdx < blocks.length &&
    (blocks[blockIdx].type === 'mermaid' ||
      blocks[blockIdx].type === 'table' ||
      blocks[blockIdx].type === 'code') &&
    anchor.block_type === blocks[blockIdx].type
  ) {
    return { matchLevel: 'block', blockIndex: blockIdx };
  }

  return { matchLevel: 'detached', blockIndex: -1 };
}

/**
 * 将单个行内文本节点按全局块偏移高亮区间切分为带标注的 React 安全片段（支持跨 <code>/<strong> 选区）。
 */
export function splitTextWithHighlights(
  text: string,
  nodeStartOffset: number,
  ranges: readonly TextHighlightRange[],
): TextHighlightSegment[] {
  if (!text) return [];
  const nodeEndOffset = nodeStartOffset + text.length;
  const overlapping = ranges.filter(
    (r) => r.endOffset > nodeStartOffset && r.startOffset < nodeEndOffset,
  );
  if (overlapping.length === 0) {
    return [{ text, activeComments: [], endedComments: [] }];
  }

  const cutSet = new Set<number>([nodeStartOffset, nodeEndOffset]);
  for (const r of overlapping) {
    if (r.startOffset > nodeStartOffset && r.startOffset < nodeEndOffset) {
      cutSet.add(r.startOffset);
    }
    if (r.endOffset > nodeStartOffset && r.endOffset < nodeEndOffset) {
      cutSet.add(r.endOffset);
    }
  }

  const cuts = Array.from(cutSet).sort((a, b) => a - b);
  const segments: TextHighlightSegment[] = [];

  for (let i = 0; i < cuts.length - 1; i += 1) {
    const segStart = cuts[i];
    const segEnd = cuts[i + 1];
    if (segEnd <= segStart) continue;

    const slice = text.slice(segStart - nodeStartOffset, segEnd - nodeStartOffset);
    const activeComments = overlapping
      .filter((r) => r.startOffset <= segStart && r.endOffset >= segEnd)
      .map((r) => ({ commentId: r.commentId, commentNo: r.commentNo }));
    const endedComments = overlapping
      .filter((r) => r.endOffset === segEnd)
      .map((r) => ({ commentId: r.commentId, commentNo: r.commentNo }));

    segments.push({
      text: slice,
      activeComments,
      endedComments,
    });
  }

  return segments;
}
