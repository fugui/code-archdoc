import type { RefDocHeadingNode, RefineRefDocResponse } from '../types';

export interface RefDocDisplaySection {
  section_id: string;
  title: string;
  level: number;
  start_line: number;
  end_line: number;
  markdown: string;
  is_preamble?: boolean;
  is_chunk?: boolean;
}

const MAX_SECTION_LINES_BEFORE_CHUNK = 250;
const TARGET_CHUNK_LINES = 160;

const RE_ATX_HEADING = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const RE_SETEXT_UNDERLINE = /^(=+|-+)\s*$/;
const RE_NUMBERED_HEADING =
  /^(?:\d+(?:\.\d+){0,3}\s+\S.*|[一二三四五六七八九十百]+[、.]\s*\S.*|第[一二三四五六七八九十百]+[章节条]\s+\S.*)$/;

interface RawHeading {
  title: string;
  level: number;
  startLine: number;
}

function parseFenceOpen(trimmed: string): { ch: string; len: number } | null {
  if (trimmed.length < 3) return null;
  const ch = trimmed[0];
  if (ch !== '`' && ch !== '~') return null;
  let count = 0;
  while (count < trimmed.length && trimmed[count] === ch) {
    count += 1;
  }
  if (count < 3) return null;
  if (ch === '`' && trimmed.slice(count).includes('`')) {
    return null;
  }
  return { ch, len: count };
}

function isFenceClose(trimmed: string, fenceChar: string, minLen: number): boolean {
  if (trimmed.length < minLen || trimmed[0] !== fenceChar) return false;
  let count = 0;
  while (count < trimmed.length && trimmed[count] === fenceChar) {
    count += 1;
  }
  return count >= minLen && trimmed.slice(count).trim() === '';
}

function firstNonEmptySnippet(segLines: readonly string[], chunkNo: number): string {
  for (const ln of segLines) {
    let t = ln.trim();
    if (!t) continue;
    t = t.replace(/^[#\-*>`\s]+/, '').trim();
    if (!t) continue;
    const chars = Array.from(t);
    if (chars.length > 36) {
      return `${chars.slice(0, 36).join('')}...`;
    }
    return t;
  }
  return `文档分段 ${chunkNo}`;
}

function findBlankSplitLine(
  lines: readonly string[],
  startLine: number,
  targetLine: number,
  maxLine: number,
): number {
  for (let l = targetLine; l <= maxLine; l += 1) {
    if (lines[l - 1].trim() === '' && l > startLine) {
      return l;
    }
  }
  for (let l = targetLine; l > startLine + 40; l -= 1) {
    if (lines[l - 1].trim() === '') {
      return l;
    }
  }
  return targetLine;
}

function chunkLineRange(
  lines: readonly string[],
  rawStartLine: number,
  rawEndLine: number,
  firstChunkNo: number,
): RefDocHeadingNode[] {
  const startLine = Math.max(1, rawStartLine);
  const endLine = Math.min(lines.length, rawEndLine);
  if (endLine < startLine) return [];

  const out: RefDocHeadingNode[] = [];
  let cur = startLine;
  let chunkNo = firstChunkNo;
  while (cur <= endLine) {
    let limit = cur + TARGET_CHUNK_LINES - 1;
    if (limit >= endLine) {
      limit = endLine;
    } else {
      const best = findBlankSplitLine(
        lines,
        cur,
        limit,
        Math.min(cur + MAX_SECTION_LINES_BEFORE_CHUNK - 1, endLine),
      );
      if (best > cur) {
        limit = best;
      }
    }
    if (limit - cur + 1 > MAX_SECTION_LINES_BEFORE_CHUNK) {
      limit = Math.min(cur + MAX_SECTION_LINES_BEFORE_CHUNK - 1, endLine);
    }
    const segLines = lines.slice(cur - 1, limit);
    const body = segLines.join('\n');
    out.push({
      section_id: `sec-chunk-${chunkNo}`,
      title: firstNonEmptySnippet(segLines, chunkNo),
      level: 2,
      start_line: cur,
      end_line: limit,
      rune_count: Array.from(body).length,
      is_chunk: true,
    });
    chunkNo += 1;
    cur = limit + 1;
  }
  return out;
}

/**
 * 估算参考文档 Markdown 的 Token 数（对齐后端 pkg/refdoc/tokens.go 算法）。
 */
export function estimateRefDocTokens(text: string): number {
  if (!text) return 0;
  let asciiCount = 0;
  let nonAsciiCount = 0;
  for (const ch of text) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 127) {
      asciiCount += 1;
    } else {
      nonAsciiCount += 1;
    }
  }
  return Math.ceil(asciiCount / 3.5 + nonAsciiCount / 1.5);
}

/**
 * 纯函数本地解析参考文档目录（对齐后端 pkg/refdoc/parser.go 围栏状态机与 >250 行切分规则）。
 */
export function parseLocalRefDocTOC(markdown: string): RefDocHeadingNode[] {
  const normalized = (markdown || '').replace(/\r\n/g, '\n');
  if (!normalized) return [];
  const lines = normalized.split('\n');
  if (lines.length === 0) return [];

  const isFencedLine = new Array<boolean>(lines.length).fill(false);
  let inFence = false;
  let fenceChar = '';
  let fenceLen = 0;

  for (let i = 0; i < lines.length; i += 1) {
    const trimmed = lines[i].trim();
    if (!inFence) {
      const opened = parseFenceOpen(trimmed);
      if (opened) {
        inFence = true;
        fenceChar = opened.ch;
        fenceLen = opened.len;
        isFencedLine[i] = true;
      }
    } else {
      isFencedLine[i] = true;
      if (isFenceClose(trimmed, fenceChar, fenceLen)) {
        inFence = false;
        fenceChar = '';
        fenceLen = 0;
      }
    }
  }

  const headings: RawHeading[] = [];
  let hasATX = false;

  for (let i = 0; i < lines.length; i += 1) {
    if (isFencedLine[i]) continue;
    const trimmed = lines[i].trim();
    const atx = RE_ATX_HEADING.exec(trimmed);
    if (atx) {
      const title = atx[2].trim();
      if (title) {
        hasATX = true;
        headings.push({
          title,
          level: atx[1].length,
          startLine: i + 1,
        });
      }
      continue;
    }
    if (
      i + 1 < lines.length &&
      !isFencedLine[i + 1] &&
      trimmed !== '' &&
      !trimmed.startsWith('|') &&
      !trimmed.startsWith('>')
    ) {
      const nextTrimmed = lines[i + 1].trim();
      const setext = RE_SETEXT_UNDERLINE.exec(nextTrimmed);
      if (setext) {
        headings.push({
          title: trimmed,
          level: setext[1].startsWith('=') ? 1 : 2,
          startLine: i + 1,
        });
        i += 1;
      }
    }
  }

  if (!hasATX) {
    const existingLines = new Set<number>(headings.map((h) => h.startLine));
    for (let i = 0; i < lines.length; i += 1) {
      if (isFencedLine[i] || existingLines.has(i + 1)) continue;
      const trimmed = lines[i].trim();
      if (!trimmed || Array.from(trimmed).length > 80) continue;
      if (trimmed.endsWith('。') || trimmed.endsWith('.')) continue;
      if (RE_NUMBERED_HEADING.test(trimmed)) {
        const prevBlank = i === 0 || lines[i - 1].trim() === '';
        if (prevBlank) {
          headings.push({
            title: trimmed,
            level: 2,
            startLine: i + 1,
          });
        }
      }
    }
    headings.sort((a, b) => a.startLine - b.startLine);
  }

  if (headings.length === 0) {
    return chunkLineRange(lines, 1, lines.length, 1);
  }

  const toc: RefDocHeadingNode[] = [];
  let secIdx = 0;
  let chunkIdx = 0;

  if (headings[0].startLine > 1) {
    const introEnd = headings[0].startLine - 1;
    const introText = lines.slice(0, introEnd).join('\n').trim();
    if (introText && introEnd > 30) {
      const chunks = chunkLineRange(lines, 1, introEnd, chunkIdx + 1);
      chunkIdx += chunks.length;
      toc.push(...chunks);
    }
  }

  for (let i = 0; i < headings.length; i += 1) {
    const h = headings[i];
    let endLine = i + 1 < headings.length ? headings[i + 1].startLine - 1 : lines.length;
    if (endLine < h.startLine) endLine = h.startLine;

    const span = endLine - h.startLine + 1;
    if (span <= MAX_SECTION_LINES_BEFORE_CHUNK) {
      secIdx += 1;
      const body = lines.slice(h.startLine - 1, endLine).join('\n');
      toc.push({
        section_id: `sec-${secIdx}`,
        title: h.title,
        level: h.level,
        start_line: h.startLine,
        end_line: endLine,
        rune_count: Array.from(body).length,
      });
      continue;
    }

    const chunks = chunkLineRange(lines, h.startLine, endLine, chunkIdx + 1);
    chunks.forEach((ch, ci) => {
      if (ci === 0) {
        secIdx += 1;
        toc.push({
          ...ch,
          section_id: `sec-${secIdx}`,
          title: h.title,
          level: h.level,
          is_chunk: false,
        });
      } else {
        chunkIdx += 1;
        toc.push({
          ...ch,
          section_id: `sec-chunk-${chunkIdx}`,
          title: `${h.title}（续 ${ci}）`,
          level: h.level,
          is_chunk: true,
        });
      }
    });
  }

  return toc;
}

/**
 * 将 Markdown 与 TOC 切分为中栏显示章节；当首标题前 <=30 行存在导言时自动补齐 __preamble__ 导言节防止行丢失。
 */
export function splitRefDocIntoDisplaySections(
  markdown: string,
  toc: readonly RefDocHeadingNode[],
): RefDocDisplaySection[] {
  const normalized = (markdown || '').replace(/\r\n/g, '\n');
  const lines = normalized.split('\n');
  if (lines.length === 0 || (lines.length === 1 && lines[0] === '')) {
    return [];
  }

  if (toc.length === 0) {
    return [
      {
        section_id: 'sec-full',
        title: '文档正文',
        level: 1,
        start_line: 1,
        end_line: lines.length,
        markdown: normalized,
      },
    ];
  }

  const out: RefDocDisplaySection[] = [];
  if (toc[0].start_line > 1) {
    const preEnd = Math.min(toc[0].start_line - 1, lines.length);
    out.push({
      section_id: '__preamble__',
      title: '文档导言',
      level: 1,
      start_line: 1,
      end_line: preEnd,
      markdown: lines.slice(0, preEnd).join('\n'),
      is_preamble: true,
    });
  }

  for (const node of toc) {
    const start = Math.max(1, node.start_line);
    const end = Math.min(lines.length, Math.max(start, node.end_line));
    out.push({
      section_id: node.section_id,
      title: node.title,
      level: node.level,
      start_line: start,
      end_line: end,
      markdown: lines.slice(start - 1, end).join('\n'),
      is_chunk: node.is_chunk,
    });
  }

  return out;
}

/**
 * 对本地草稿执行 1-based 闭区间 [startLine, endLine] 行替换，区间外正文保持逐行不变。
 */
export function replaceLinesInDraft(
  fullMarkdown: string,
  startLine: number,
  endLine: number,
  replacement: string,
): string {
  const normFull = (fullMarkdown || '').replace(/\r\n/g, '\n');
  const lines = normFull.split('\n');
  if (startLine < 1 || endLine < startLine || endLine > lines.length) {
    return normFull;
  }

  const normRepl = (replacement || '').replace(/\r\n/g, '\n');
  const replLines = normRepl.split('\n');
  if (replLines.length > 1 && replLines[replLines.length - 1] === '' && endLine < lines.length) {
    if (lines[endLine - 1] !== '') {
      replLines.pop();
    }
  }

  return [
    ...lines.slice(0, startLine - 1),
    ...replLines,
    ...lines.slice(endLine),
  ].join('\n');
}

/**
 * 将单节渲染器内相对行号映射为文档全局 1-based 绝对行号，并安全钳制在当前章节行区间内。
 */
export function computeAbsoluteSelectionLines(
  sectionStartLine: number,
  relStartLine: number | undefined,
  relEndLine: number | undefined,
  sectionEndLine: number,
): { startLine: number; endLine: number } {
  const safeStart = Math.max(1, sectionStartLine);
  const safeEnd = Math.max(safeStart, sectionEndLine);

  if (relStartLine === undefined || !Number.isFinite(relStartLine) || relStartLine <= 0) {
    return { startLine: safeStart, endLine: safeEnd };
  }

  const mappedStart = Math.min(safeEnd, Math.max(safeStart, safeStart + Math.floor(relStartLine) - 1));
  const rawRelEnd =
    relEndLine !== undefined && Number.isFinite(relEndLine) && relEndLine >= relStartLine
      ? Math.floor(relEndLine)
      : Math.floor(relStartLine);
  const mappedEnd = Math.min(safeEnd, Math.max(mappedStart, safeStart + rawRelEnd - 1));
  return { startLine: mappedStart, endLine: mappedEnd };
}

/**
 * 判断本地草稿相对服务端已加载正文是否存在未保存改动（忽略 CRLF/LF 差异）。
 */
export function hasUnsavedRefDocDraft(savedContent: string, draftContent: string): boolean {
  const normSaved = (savedContent || '').replace(/\r\n/g, '\n');
  const normDraft = (draftContent || '').replace(/\r\n/g, '\n');
  return normSaved !== normDraft;
}

/**
 * 将 AI 精修提案应用到当前内存草稿（ask 或 refresh_summary 无正文变更时原样返回）。
 */
export function mergeRefDocRefineProposal(
  currentDraft: string,
  proposal: RefineRefDocResponse,
): string {
  if (proposal.mode !== 'revise' || !proposal.proposed_fragment.trim()) {
    return currentDraft;
  }
  return proposal.proposed_full_markdown || currentDraft;
}

/**
 * 将 Unified Diff 单行映射为复用 Flat BEM CSS 类名。
 */
export function classifyRefDocDiffLine(line: string): string {
  if (line.startsWith('@@')) {
    return 'code-archdoc-diff-line code-archdoc-diff-line--hunk';
  }
  if (line.startsWith('+')) {
    return 'code-archdoc-diff-line code-archdoc-diff-line--add';
  }
  if (line.startsWith('-')) {
    return 'code-archdoc-diff-line code-archdoc-diff-line--del';
  }
  return 'code-archdoc-diff-line';
}
