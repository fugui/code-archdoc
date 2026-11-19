/**
 * 安全纯函数 Markdown 块级与行内解析器（零 dangerouslySetInnerHTML，由 React JSX 安全渲染）。
 * 支持：标题、段落、Fenced 代码块（含 mermaid 识别）、GFM 表格、有序/无序列表、引用块、分割线、
 * 粗体、斜体、行内代码与安全协议链接（拦截 javascript:/data:/vbscript:）。
 */

export type MarkdownInlineNode =
  | { type: 'text'; value: string }
  | { type: 'code'; value: string }
  | { type: 'strong'; children: MarkdownInlineNode[] }
  | { type: 'em'; children: MarkdownInlineNode[] }
  | { type: 'link'; href: string; label: string };

export type MarkdownBlockNode =
  | { type: 'heading'; level: number; children: MarkdownInlineNode[] }
  | { type: 'paragraph'; children: MarkdownInlineNode[] }
  | { type: 'code'; language: string; code: string }
  | { type: 'mermaid'; chart: string }
  | { type: 'blockquote'; blocks: MarkdownBlockNode[] }
  | { type: 'list'; ordered: boolean; items: MarkdownInlineNode[][] }
  | { type: 'table'; headers: MarkdownInlineNode[][]; rows: MarkdownInlineNode[][][] }
  | { type: 'hr' };

/**
 * 仅允许安全跳转协议或相对路径，彻底阻断 javascript:/data:/vbscript: XSS 向量。
 */
export function isSafeLinkHref(rawHref: string): boolean {
  const href = rawHref.trim();
  if (!href) return false;
  if (href.startsWith('/') || href.startsWith('./') || href.startsWith('../') || href.startsWith('#')) {
    return true;
  }
  const lower = href.toLowerCase();
  return lower.startsWith('http://') || lower.startsWith('https://') || lower.startsWith('mailto:');
}

export function parseInlineMarkdown(text: string): MarkdownInlineNode[] {
  const nodes: MarkdownInlineNode[] = [];
  let cursor = 0;

  const pushText = (chunk: string) => {
    if (!chunk) return;
    const prev = nodes[nodes.length - 1];
    if (prev && prev.type === 'text') {
      prev.value += chunk;
    } else {
      nodes.push({ type: 'text', value: chunk });
    }
  };

  while (cursor < text.length) {
    // 1. 行内代码 `code`
    if (text[cursor] === '`') {
      const end = text.indexOf('`', cursor + 1);
      if (end > cursor + 1) {
        nodes.push({ type: 'code', value: text.slice(cursor + 1, end) });
        cursor = end + 1;
        continue;
      }
    }

    // 2. 链接 [label](href)
    if (text[cursor] === '[') {
      const closeBracket = text.indexOf(']', cursor + 1);
      if (closeBracket > cursor + 1 && text[closeBracket + 1] === '(') {
        const closeParen = findMatchingParen(text, closeBracket + 1);
        if (closeParen > closeBracket + 1) {
          const label = text.slice(cursor + 1, closeBracket);
          const href = text.slice(closeBracket + 2, closeParen).trim();
          if (isSafeLinkHref(href)) {
            nodes.push({ type: 'link', href, label });
          } else {
            pushText(label);
          }
          cursor = closeParen + 1;
          continue;
        }
      }
    }

    // 3. 粗体 **text**
    if (text.startsWith('**', cursor)) {
      const end = text.indexOf('**', cursor + 2);
      if (end > cursor + 2) {
        nodes.push({ type: 'strong', children: parseInlineMarkdown(text.slice(cursor + 2, end)) });
        cursor = end + 2;
        continue;
      }
    }

    // 4. 斜体 *text*
    if (text[cursor] === '*' && text[cursor + 1] !== '*') {
      const end = text.indexOf('*', cursor + 1);
      if (end > cursor + 1) {
        nodes.push({ type: 'em', children: parseInlineMarkdown(text.slice(cursor + 1, end)) });
        cursor = end + 1;
        continue;
      }
    }

    pushText(text[cursor]);
    cursor += 1;
  }

  return nodes;
}

function findMatchingParen(text: string, openIndex: number): number {
  let depth = 0;
  for (let i = openIndex; i < text.length; i += 1) {
    if (text[i] === '(') depth += 1;
    else if (text[i] === ')') {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

function splitTableRow(line: string): string[] {
  const trimmed = line.trim().replace(/^\|/, '').replace(/\|$/, '');
  return trimmed.split('|').map((cell) => cell.trim());
}

function isTableDividerLine(line: string): boolean {
  const trimmed = line.trim();
  if (!trimmed.includes('|') || !trimmed.includes('-')) return false;
  const cells = splitTableRow(trimmed);
  return cells.length > 0 && cells.every((c) => /^:?-{3,}:?$/.test(c));
}

function isBlockStarter(line: string, nextLine: string | undefined): boolean {
  const trimmed = line.trim();
  if (!trimmed) return true;
  if (/^```/.test(trimmed)) return true;
  if (/^#{1,6}\s+/.test(trimmed)) return true;
  if (/^([-*_])\1{2,}$/.test(trimmed)) return true;
  if (/^>\s?/.test(trimmed)) return true;
  if (/^[-*+]\s+/.test(trimmed)) return true;
  if (/^\d+\.\s+/.test(trimmed)) return true;
  if (trimmed.includes('|') && nextLine && isTableDividerLine(nextLine)) return true;
  return false;
}

export function parseMarkdownBlocks(markdown: string): MarkdownBlockNode[] {
  const lines = markdown.replace(/\r\n/g, '\n').split('\n');
  const blocks: MarkdownBlockNode[] = [];
  let idx = 0;

  while (idx < lines.length) {
    const line = lines[idx];
    const trimmed = line.trim();

    if (!trimmed) {
      idx += 1;
      continue;
    }

    // 1. Fenced code block / mermaid
    const fenceMatch = /^```([^\s`]*)/.exec(trimmed);
    if (fenceMatch) {
      const lang = (fenceMatch[1] || '').toLowerCase();
      idx += 1;
      const codeLines: string[] = [];
      while (idx < lines.length && !/^```\s*$/.test(lines[idx].trim())) {
        codeLines.push(lines[idx]);
        idx += 1;
      }
      if (idx < lines.length) {
        idx += 1;
      }
      const code = codeLines.join('\n');
      if (lang === 'mermaid') {
        blocks.push({ type: 'mermaid', chart: code });
      } else {
        blocks.push({ type: 'code', language: lang || 'text', code });
      }
      continue;
    }

    // 2. Heading
    const headingMatch = /^(#{1,6})\s+(.*)$/.exec(trimmed);
    if (headingMatch) {
      blocks.push({
        type: 'heading',
        level: headingMatch[1].length,
        children: parseInlineMarkdown(headingMatch[2].trim()),
      });
      idx += 1;
      continue;
    }

    // 3. Horizontal rule
    if (/^([-*_])\1{2,}$/.test(trimmed)) {
      blocks.push({ type: 'hr' });
      idx += 1;
      continue;
    }

    // 4. GFM Table
    if (trimmed.includes('|') && idx + 1 < lines.length && isTableDividerLine(lines[idx + 1])) {
      const headers = splitTableRow(trimmed).map((cell) => parseInlineMarkdown(cell));
      idx += 2;
      const rows: MarkdownInlineNode[][][] = [];
      while (idx < lines.length && lines[idx].trim().includes('|')) {
        const rowCells = splitTableRow(lines[idx]).map((cell) => parseInlineMarkdown(cell));
        rows.push(rowCells);
        idx += 1;
      }
      blocks.push({ type: 'table', headers, rows });
      continue;
    }

    // 5. Blockquote
    if (/^>\s?/.test(trimmed)) {
      const quoteLines: string[] = [];
      while (idx < lines.length && /^>\s?/.test(lines[idx].trim())) {
        quoteLines.push(lines[idx].trim().replace(/^>\s?/, ''));
        idx += 1;
      }
      blocks.push({
        type: 'blockquote',
        blocks: parseMarkdownBlocks(quoteLines.join('\n')),
      });
      continue;
    }

    // 6. Unordered / Ordered list
    const ulMatch = /^[-*+]\s+(.*)$/.exec(trimmed);
    const olMatch = /^\d+\.\s+(.*)$/.exec(trimmed);
    if (ulMatch || olMatch) {
      const ordered = Boolean(olMatch);
      const items: MarkdownInlineNode[][] = [];
      const matcher = ordered ? /^\d+\.\s+(.*)$/ : /^[-*+]\s+(.*)$/;
      while (idx < lines.length) {
        const m = matcher.exec(lines[idx].trim());
        if (!m) break;
        items.push(parseInlineMarkdown(m[1]));
        idx += 1;
      }
      blocks.push({ type: 'list', ordered, items });
      continue;
    }

    // 7. Paragraph
    const paraLines: string[] = [trimmed];
    idx += 1;
    while (idx < lines.length && !isBlockStarter(lines[idx], lines[idx + 1])) {
      paraLines.push(lines[idx].trim());
      idx += 1;
    }
    blocks.push({
      type: 'paragraph',
      children: parseInlineMarkdown(paraLines.join(' ')),
    });
  }

  return blocks;
}
