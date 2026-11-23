import type {
  CanonicalFacetName,
  FacetName,
  SectionOwnership,
  SlotDefinition,
} from '../types/domain.ts';
import { parseStrictInt } from './numberInput.ts';

export const CANONICAL_FACET_NAMES: readonly CanonicalFacetName[] = [
  'tree',
  'build',
  'deps',
  'config_keys',
  'symbols',
  'idl_contracts',
  'http_routes',
  'data_models',
  'runtime_primitives',
] as const;

export type FacetWeight = 'light' | 'medium' | 'heavy';

export interface FacetCatalogItem {
  value: CanonicalFacetName;
  label: string;
  shortLabel: string;
  description: string;
  weight: FacetWeight;
  weightLabel: string;
}

export const FACET_CATALOG: readonly FacetCatalogItem[] = [
  {
    value: 'tree',
    label: 'tree (目录树)',
    shortLabel: '目录树',
    description: '文件清单、目录层级统计与语言分布',
    weight: 'light',
    weightLabel: '轻量',
  },
  {
    value: 'build',
    label: 'build (构建)',
    shortLabel: '构建',
    description: 'CMake/Makefile/Bazel/Maven/Gradle 等构建目标与编译开关',
    weight: 'light',
    weightLabel: '轻量',
  },
  {
    value: 'deps',
    label: 'deps (依赖)',
    shortLabel: '依赖',
    description: '外部三方库依赖、内部模块依赖边与引用事实',
    weight: 'medium',
    weightLabel: '中等',
  },
  {
    value: 'config_keys',
    label: 'config_keys (配置键)',
    shortLabel: '配置键',
    description: '脱敏配置文件键路径、类型、默认值与环境变量映射',
    weight: 'light',
    weightLabel: '轻量',
  },
  {
    value: 'symbols',
    label: 'symbols (代码符号)',
    shortLabel: '代码符号',
    description: '类、结构体、函数签名、继承关系与源码双坐标',
    weight: 'heavy',
    weightLabel: '较重',
  },
  {
    value: 'idl_contracts',
    label: 'idl_contracts (Proto/RPC)',
    shortLabel: 'Proto/RPC',
    description: '.proto 服务、RPC 方法、Message 与 Enum 枚举',
    weight: 'medium',
    weightLabel: '中等',
  },
  {
    value: 'http_routes',
    label: 'http_routes (HTTP 路由)',
    shortLabel: 'HTTP 路由',
    description: 'HTTP 方法、路径、Handler 符号与中间件',
    weight: 'medium',
    weightLabel: '中等',
  },
  {
    value: 'data_models',
    label: 'data_models (数据模型)',
    shortLabel: '数据模型',
    description: 'SQL 表、Redis/MQ/共享内存实体与索引约束',
    weight: 'medium',
    weightLabel: '中等',
  },
  {
    value: 'runtime_primitives',
    label: 'runtime_primitives (并发/调度)',
    shortLabel: '并发/调度',
    description: '线程、锁、原子操作、IPC、信号、定时器与错误码原语',
    weight: 'heavy',
    weightLabel: '较重',
  },
] as const;

export const LIGHT_CONTRACT_REPO_FACETS: readonly CanonicalFacetName[] = [
  'tree',
  'build',
  'deps',
  'config_keys',
  'idl_contracts',
  'http_routes',
] as const;

export type BlueprintHeadingLevel = 2 | 3 | 4;

export interface StaticBlueprintBlock {
  kind: 'static';
  /** 前端渲染稳定 React key（不写入磁盘或 DSL） */
  key: string;
  /** 静态 Markdown 段落（如 # 文档主标题、导语、免责声明或分割线） */
  markdown: string;
}

export interface SlotBlueprintBlock {
  kind: 'slot';
  /** 前端渲染稳定 React key */
  key: string;
  /** true 表示该槽位拥有紧邻上方的 ##~#### 标题行并由 slot.title 统一驱动 */
  emitHeading: boolean;
  /** 当 emitHeading 为 true 时输出到 template.md 的标题层级，默认 2 */
  headingLevel: BlueprintHeadingLevel;
  /** 完整的后端 SlotDefinition 契约对象 */
  slot: SlotDefinition;
  /** 仅反编译期使用：若存量 template.md 紧邻标题文本与 slot.title 不一致，记录原文本供 UI 温和提示 */
  divergedTemplateHeading?: string;
}

export type BlueprintBlock = StaticBlueprintBlock | SlotBlueprintBlock;

export interface DocBlueprint {
  blocks: BlueprintBlock[];
}

export interface BlueprintDiagnostic {
  line: number;
  severity: 'error' | 'warning';
  code: string;
  message: string;
}

const SLOT_ID_PATTERN = /^[a-z][a-z0-9_]{1,48}$/;
const TEMPLATE_SLOT_MARKER_PATTERN = /^\s*<!--\s*slot:([a-z][a-z0-9_]{1,48})\s*-->\s*$/;
const BOUND_HEADING_PATTERN = /^(#{2,4})\s+(.+?)\s*$/;
const DSL_SLOT_OPEN_PATTERN = /^(`{3,})\s*slot(?:\s+(.*))?$/;
const DSL_NORMAL_FENCE_OPEN_PATTERN = /^(`{3,}|~{3,})(.*)$/;
const CANONICAL_FACET_SET = new Set<string>(CANONICAL_FACET_NAMES);

const KNOWN_DSL_KEYS = [
  'title',
  'ownership',
  'required',
  'ir_facets',
  'token_budget',
  'min_chars',
  'enable_source_tools',
  'max_tool_rounds',
  'hint',
] as const;

const DSL_KEY_ALIASES: Record<string, (typeof KNOWN_DSL_KEYS)[number]> = {
  title: 'title',
  ownership: 'ownership',
  required: 'required',
  ir_facets: 'ir_facets',
  facets: 'ir_facets',
  token_budget: 'token_budget',
  budget: 'token_budget',
  min_chars: 'min_chars',
  enable_source_tools: 'enable_source_tools',
  source_tools: 'enable_source_tools',
  max_tool_rounds: 'max_tool_rounds',
  tool_rounds: 'max_tool_rounds',
  hint: 'hint',
  instruction_hint: 'hint',
};

const ALL_ACCEPTED_DSL_KEY_NAMES = Object.keys(DSL_KEY_ALIASES);

let keySeqCounter = 0;

export function nextBlueprintKey(prefix: string): string {
  keySeqCounter += 1;
  return `${prefix}_${keySeqCounter}`;
}

export function createDefaultBlueprint(displayName: string): DocBlueprint {
  const titleText = displayName.trim() || '新文档类型';
  return {
    blocks: [
      {
        kind: 'static',
        key: nextBlueprintKey('static'),
        markdown: `# ${titleText}`,
      },
      {
        kind: 'slot',
        key: nextBlueprintKey('slot'),
        emitHeading: true,
        headingLevel: 2,
        slot: {
          slot_id: 'overview_summary',
          title: '1. 核心概览与边界说明',
          ownership: 'auto',
          required: true,
          ir_facets: ['tree', 'deps'],
          token_budget: 8000,
          min_chars: 40,
          instruction_hint: '',
        },
      },
    ],
  };
}

function normalizeLF(text: string): string {
  return text.replace(/\r\n/g, '\n');
}

function trimBlankEdges(lines: string[]): string {
  let start = 0;
  let end = lines.length - 1;
  while (start <= end && lines[start].trim() === '') {
    start += 1;
  }
  while (end >= start && lines[end].trim() === '') {
    end -= 1;
  }
  if (start > end) {
    return '';
  }
  return lines.slice(start, end + 1).join('\n');
}

function cloneSlotDefinition(slot: SlotDefinition): SlotDefinition {
  const cloned: SlotDefinition = {
    slot_id: slot.slot_id,
    title: slot.title,
    ownership: slot.ownership,
    required: slot.required,
    ir_facets: [...(slot.ir_facets ?? [])],
    token_budget: slot.token_budget,
    min_chars: slot.min_chars,
  };
  if (slot.instruction_hint !== undefined) {
    cloned.instruction_hint = slot.instruction_hint;
  }
  if (slot.enable_source_tools !== undefined) {
    cloned.enable_source_tools = slot.enable_source_tools;
  }
  if (slot.max_tool_rounds !== undefined) {
    cloned.max_tool_rounds = slot.max_tool_rounds;
  }
  return cloned;
}

function deduplicateFacets(facets: readonly FacetName[]): FacetName[] {
  const seen = new Set<FacetName>();
  const out: FacetName[] = [];
  for (const f of facets) {
    if (!seen.has(f)) {
      seen.add(f);
      out.push(f);
    }
  }
  return out;
}

function levenshtein(a: string, b: string): number {
  const m = a.length;
  const n = b.length;
  const dp: number[] = Array.from({ length: n + 1 }, (_, j) => j);
  for (let i = 1; i <= m; i += 1) {
    let prev = dp[0];
    dp[0] = i;
    for (let j = 1; j <= n; j += 1) {
      const temp = dp[j];
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      dp[j] = Math.min(dp[j] + 1, dp[j - 1] + 1, prev + cost);
      prev = temp;
    }
  }
  return dp[n];
}

function findClosestSuggestion(input: string, candidates: readonly string[], maxDist: number): string | null {
  let best: string | null = null;
  let bestDist = maxDist + 1;
  for (const cand of candidates) {
    const d = levenshtein(input, cand);
    if (d < bestDist) {
      bestDist = d;
      best = cand;
    }
  }
  return best;
}

/**
 * 将磁盘/服务端加载的 (template.md, slots[]) 确定性反编译为统一 DocBlueprint IR。
 */
export function decompileToBlueprint(
  templateMD: string,
  slots: readonly SlotDefinition[],
): { blueprint: DocBlueprint; diagnostics: BlueprintDiagnostic[] } {
  const normalized = normalizeLF(templateMD);
  const lines = normalized === '' ? [] : normalized.split('\n');
  const diagnostics: BlueprintDiagnostic[] = [];

  const slotMap = new Map<string, SlotDefinition>();
  for (const sl of slots) {
    slotMap.set(sl.slot_id, sl);
  }

  interface MatchedSlotHit {
    markerLine: number;
    startConsumeLine: number;
    emitHeading: boolean;
    headingLevel: BlueprintHeadingLevel;
    slot: SlotDefinition;
    divergedTemplateHeading?: string;
  }

  const hits: MatchedSlotHit[] = [];
  const seenInTemplate = new Set<string>();
  let lastConsumedLine = -1;

  for (let i = 0; i < lines.length; i += 1) {
    const m = lines[i].match(TEMPLATE_SLOT_MARKER_PATTERN);
    if (!m) continue;
    const slotId = m[1];
    const spec = slotMap.get(slotId);
    if (!spec) {
      diagnostics.push({
        line: i + 1,
        severity: 'warning',
        code: 'UNDECLARED_TEMPLATE_SLOT',
        message: `template.md 中的标记 <!-- slot:${slotId} --> 未在 slots[] 中声明，已作为静态文本保留。`,
      });
      continue;
    }
    if (seenInTemplate.has(slotId)) {
      diagnostics.push({
        line: i + 1,
        severity: 'warning',
        code: 'DUPLICATE_TEMPLATE_SLOT',
        message: `template.md 中存在重复的槽位标记 <!-- slot:${slotId} -->，多余标记已作为静态文本保留。`,
      });
      continue;
    }
    seenInTemplate.add(slotId);

    // 向上跳过纯空行查找紧邻上一非空行（不越过前一个已消费槽位）
    let emitHeading = false;
    let headingLevel: BlueprintHeadingLevel = 2;
    let startConsumeLine = i;
    let divergedTemplateHeading: string | undefined;

    for (let j = i - 1; j > lastConsumedLine; j -= 1) {
      if (lines[j].trim() === '') {
        continue;
      }
      const hm = lines[j].match(BOUND_HEADING_PATTERN);
      if (hm) {
        emitHeading = true;
        headingLevel = hm[1].length as BlueprintHeadingLevel;
        startConsumeLine = j;
        const capturedTitle = hm[2].trim();
        if (capturedTitle !== spec.title.trim()) {
          divergedTemplateHeading = capturedTitle;
        }
      }
      break;
    }

    lastConsumedLine = i;
    hits.push({
      markerLine: i,
      startConsumeLine,
      emitHeading,
      headingLevel,
      slot: cloneSlotDefinition(spec),
      ...(divergedTemplateHeading ? { divergedTemplateHeading } : {}),
    });
  }

  const blocks: BlueprintBlock[] = [];
  let cursor = 0;
  for (const hit of hits) {
    if (hit.startConsumeLine > cursor) {
      const staticText = trimBlankEdges(lines.slice(cursor, hit.startConsumeLine));
      if (staticText !== '') {
        blocks.push({
          kind: 'static',
          key: nextBlueprintKey('static'),
          markdown: staticText,
        });
      }
    }
    blocks.push({
      kind: 'slot',
      key: `slot_${hit.slot.slot_id}`,
      emitHeading: hit.emitHeading,
      headingLevel: hit.headingLevel,
      slot: hit.slot,
      ...(hit.divergedTemplateHeading
        ? { divergedTemplateHeading: hit.divergedTemplateHeading }
        : {}),
    });
    cursor = hit.markerLine + 1;
  }

  if (cursor < lines.length) {
    const tailText = trimBlankEdges(lines.slice(cursor));
    if (tailText !== '') {
      blocks.push({
        kind: 'static',
        key: nextBlueprintKey('static'),
        markdown: tailText,
      });
    }
  }

  // 缺漏标记兜底：将 slots[] 中未出现在 template.md 的槽位按原顺序追加到末尾
  for (const sl of slots) {
    if (!seenInTemplate.has(sl.slot_id)) {
      diagnostics.push({
        line: Math.max(1, lines.length),
        severity: 'warning',
        code: 'MISSING_TEMPLATE_SLOT',
        message: `slots[] 中的槽位 "${sl.slot_id}" 在 template.md 中缺少占位标记，已自动追加至蓝图末尾。`,
      });
      blocks.push({
        kind: 'slot',
        key: `slot_${sl.slot_id}`,
        emitHeading: true,
        headingLevel: 2,
        slot: cloneSlotDefinition(sl),
      });
    }
  }

  return {
    blueprint: { blocks },
    diagnostics,
  };
}

function cleanStaticMarkdown(markdown: string): string {
  return normalizeLF(markdown).replace(/^\n+|\n+$/g, '');
}

function formatSlotTemplateChunk(block: SlotBlueprintBlock): string {
  const marker = `<!-- slot:${block.slot.slot_id} -->`;
  if (block.emitHeading) {
    const hashes = '#'.repeat(block.headingLevel);
    return `${hashes} ${block.slot.title.trim()}\n${marker}`;
  }
  return marker;
}

function formatHintBlockquote(rawHint: string): string {
  const normalized = normalizeLF(rawHint).trim();
  if (normalized === '') {
    return '';
  }
  if (!normalized.includes('\n')) {
    return `> [写作指引] ${normalized}`;
  }
  const quotedLines = normalized
    .split('\n')
    .map((line) => (line.trim() === '' ? '>' : `> ${line}`));
  return ['> [写作指引]', ...quotedLines].join('\n');
}

/**
 * 将 DocBlueprint 确定性编译为写盘与接口提交所需的 { templateMD, slots }。
 */
export function compileFromBlueprint(blueprint: DocBlueprint): {
  templateMD: string;
  slots: SlotDefinition[];
} {
  const slots: SlotDefinition[] = [];
  const chunks: string[] = [];

  for (const block of blueprint.blocks) {
    if (block.kind === 'static') {
      const cleaned = cleanStaticMarkdown(block.markdown);
      if (cleaned !== '') {
        chunks.push(cleaned);
      }
      continue;
    }

    const slotCopy = cloneSlotDefinition(block.slot);
    slots.push(slotCopy);
    chunks.push(formatSlotTemplateChunk(block));
  }

  const templateMD = chunks.length > 0 ? `${chunks.join('\n\n')}\n` : '';
  return { templateMD, slots };
}

export interface BlueprintPreviewOptions {
  /** 是否在各槽位占位标记下方附加章节写作指引 (instruction_hint) 引用块 */
  includeHints?: boolean;
}

/**
 * 生成供蓝图预览视图展示的模板纯文本。
 * 当 includeHints 为 true 时仅在预览字符串中添加引用形式提示，不修改蓝图且不影响编译落盘的 template.md。
 */
export function formatBlueprintTemplatePreview(
  blueprint: DocBlueprint,
  options?: BlueprintPreviewOptions,
): string {
  if (!options?.includeHints) {
    return compileFromBlueprint(blueprint).templateMD;
  }

  const chunks: string[] = [];
  for (const block of blueprint.blocks) {
    if (block.kind === 'static') {
      const cleaned = cleanStaticMarkdown(block.markdown);
      if (cleaned !== '') {
        chunks.push(cleaned);
      }
      continue;
    }

    const baseChunk = formatSlotTemplateChunk(block);
    const hintBlock = formatHintBlockquote(block.slot.instruction_hint ?? '');
    chunks.push(hintBlock !== '' ? `${baseChunk}\n${hintBlock}` : baseChunk);
  }

  return chunks.length > 0 ? `${chunks.join('\n\n')}\n` : '';
}

function computeFenceForHint(hint: string): string {
  const matches = hint.match(/`+/g);
  let maxLen = 2;
  if (matches) {
    for (const seq of matches) {
      if (seq.length > maxLen) {
        maxLen = seq.length;
      }
    }
  }
  return '`'.repeat(Math.max(3, maxLen + 1));
}

/**
 * 将 DocBlueprint 序列化为规范化的参数化源码 DSL v1 文本（字节级幂等）。
 */
export function serializeBlueprintToDSL(blueprint: DocBlueprint): string {
  const chunks: string[] = [];

  for (const block of blueprint.blocks) {
    if (block.kind === 'static') {
      const cleaned = cleanStaticMarkdown(block.markdown);
      if (cleaned !== '') {
        chunks.push(cleaned);
      }
      continue;
    }

    const sl = block.slot;
    const hint = normalizeLF(sl.instruction_hint ?? '');
    const fence = computeFenceForHint(hint);
    const bodyLines: string[] = [];

    if (!block.emitHeading) {
      bodyLines.push(`title: ${sl.title.trim()}`);
    }
    bodyLines.push(`ownership: ${sl.ownership}`);
    bodyLines.push(`required: ${sl.required ? 'true' : 'false'}`);
    bodyLines.push(`ir_facets: [${deduplicateFacets(sl.ir_facets ?? []).join(', ')}]`);
    bodyLines.push(`token_budget: ${sl.token_budget}`);
    bodyLines.push(`min_chars: ${sl.min_chars}`);

    const sourceToolsEnabled = Boolean(sl.enable_source_tools);
    bodyLines.push(`enable_source_tools: ${sourceToolsEnabled ? 'true' : 'false'}`);
    if (sourceToolsEnabled) {
      const rounds =
        typeof sl.max_tool_rounds === 'number' && sl.max_tool_rounds > 0
          ? sl.max_tool_rounds
          : 4;
      bodyLines.push(`max_tool_rounds: ${rounds}`);
    }

    if (hint !== '') {
      const useMultiline =
        hint.includes('\n') ||
        hint.length > 80 ||
        /^\s|\s$/.test(hint) ||
        hint.startsWith('|');
      if (useMultiline) {
        bodyLines.push('hint: |');
        for (const hLine of hint.split('\n')) {
          bodyLines.push(`  ${hLine}`);
        }
      } else {
        bodyLines.push(`hint: ${hint}`);
      }
    }

    const slotBlockText = `${fence}slot ${sl.slot_id}\n${bodyLines.join('\n')}\n${fence}`;
    if (block.emitHeading) {
      const hashes = '#'.repeat(block.headingLevel);
      chunks.push(`${hashes} ${sl.title.trim()}\n\n${slotBlockText}`);
    } else {
      chunks.push(slotBlockText);
    }
  }

  return chunks.length > 0 ? `${chunks.join('\n\n')}\n` : '';
}

interface NormalFenceState {
  char: '`' | '~';
  length: number;
}

function checkNormalFenceOpen(line: string): NormalFenceState | null {
  const m = line.match(DSL_NORMAL_FENCE_OPEN_PATTERN);
  if (!m) return null;
  const marker = m[1];
  const char = marker[0] as '`' | '~';
  const info = m[2];
  // CommonMark: backtick fence info string cannot contain backticks
  if (char === '`' && info.includes('`')) {
    return null;
  }
  return { char, length: marker.length };
}

function isNormalFenceClose(line: string, state: NormalFenceState): boolean {
  const trimmed = line.trim();
  if (trimmed.length < state.length) return false;
  for (let i = 0; i < trimmed.length; i += 1) {
    if (trimmed[i] !== state.char) return false;
  }
  return true;
}

/**
 * 零第三方依赖严格解析参数化源码 DSL v1，输出 DocBlueprint 与精确到行号的诊断信息。
 */
export function parseDSLToBlueprint(source: string): {
  blueprint: DocBlueprint | null;
  diagnostics: BlueprintDiagnostic[];
} {
  const normalized = normalizeLF(source);
  const lines = normalized === '' ? [] : normalized.split('\n');
  const diagnostics: BlueprintDiagnostic[] = [];
  const blocks: BlueprintBlock[] = [];
  const seenSlotIds = new Set<string>();

  let staticBuffer: string[] = [];
  let normalFence: NormalFenceState | null = null;
  let slotCount = 0;

  const flushStaticBuffer = (buffer: string[]) => {
    const cleaned = trimBlankEdges(buffer);
    if (cleaned !== '') {
      blocks.push({
        kind: 'static',
        key: nextBlueprintKey('static'),
        markdown: cleaned,
      });
    }
  };

  let idx = 0;
  while (idx < lines.length) {
    const line = lines[idx];

    if (normalFence !== null) {
      staticBuffer.push(line);
      if (isNormalFenceClose(line, normalFence)) {
        normalFence = null;
      }
      idx += 1;
      continue;
    }

    const slotOpenMatch = line.match(DSL_SLOT_OPEN_PATTERN);
    if (slotOpenMatch) {
      const openLineNum = idx + 1;
      const fenceLen = slotOpenMatch[1].length;
      const rawSlotId = (slotOpenMatch[2] ?? '').trim();

      // 向上检查静态缓冲尾行是否为紧邻绑定的 ##~#### 标题行
      let emitHeading = false;
      let headingLevel: BlueprintHeadingLevel = 2;
      let headingTitle: string | undefined;
      let headingBufferIdx = -1;

      for (let bIdx = staticBuffer.length - 1; bIdx >= 0; bIdx -= 1) {
        if (staticBuffer[bIdx].trim() === '') {
          continue;
        }
        const hm = staticBuffer[bIdx].match(BOUND_HEADING_PATTERN);
        if (hm) {
          emitHeading = true;
          headingLevel = hm[1].length as BlueprintHeadingLevel;
          headingTitle = hm[2].trim();
          headingBufferIdx = bIdx;
        }
        break;
      }

      if (headingBufferIdx >= 0) {
        flushStaticBuffer(staticBuffer.slice(0, headingBufferIdx));
      } else {
        flushStaticBuffer(staticBuffer);
      }
      staticBuffer = [];

      // 查找闭合围栏
      let closeIdx = -1;
      for (let k = idx + 1; k < lines.length; k += 1) {
        const closeMatch = lines[k].match(/^(`{3,})\s*$/);
        if (closeMatch && closeMatch[1].length >= fenceLen) {
          closeIdx = k;
          break;
        }
      }

      if (closeIdx === -1) {
        diagnostics.push({
          line: openLineNum,
          severity: 'error',
          code: 'UNCLOSED_SLOT_FENCE',
          message: `第 ${openLineNum} 行：未闭合的 \`\`\`slot 围栏块（缺少不少于 ${fenceLen} 个反引号的闭合行）。`,
        });
        return { blueprint: null, diagnostics };
      }

      if (!rawSlotId) {
        diagnostics.push({
          line: openLineNum,
          severity: 'error',
          code: 'MISSING_SLOT_ID',
          message: `第 ${openLineNum} 行：\`\`\`slot 围栏头缺少 slot_id（须匹配 ^[a-z][a-z0-9_]{1,48}$）。`,
        });
      } else if (!SLOT_ID_PATTERN.test(rawSlotId)) {
        diagnostics.push({
          line: openLineNum,
          severity: 'error',
          code: 'INVALID_SLOT_ID',
          message: `第 ${openLineNum} 行：slot_id "${rawSlotId}" 非法，须匹配 ^[a-z][a-z0-9_]{1,48}$。`,
        });
      } else if (seenSlotIds.has(rawSlotId)) {
        diagnostics.push({
          line: openLineNum,
          severity: 'error',
          code: 'DUPLICATE_SLOT_ID',
          message: `第 ${openLineNum} 行：存在重复的 slot_id "${rawSlotId}"。`,
        });
      } else {
        seenSlotIds.add(rawSlotId);
      }

      const bodyLines = lines.slice(idx + 1, closeIdx);
      const parsedFields = parseSlotBody(bodyLines, idx + 2, diagnostics);

      const effectiveTitle = parsedFields.title ?? headingTitle ?? '';
      if (!effectiveTitle) {
        diagnostics.push({
          line: openLineNum,
          severity: 'error',
          code: 'MISSING_SLOT_TITLE',
          message: `第 ${openLineNum} 行：无紧邻 ##~#### 标题的自由内嵌槽位必须显式填写 title: 字段。`,
        });
      }

      const enableSourceTools = parsedFields.enableSourceTools ?? false;
      const maxToolRounds = enableSourceTools
        ? parsedFields.maxToolRounds !== undefined && parsedFields.maxToolRounds > 0
          ? parsedFields.maxToolRounds
          : 4
        : 0;

      const irFacets = parsedFields.irFacets ?? [];
      if (irFacets.length === 0 && !parsedFields.hadFacetError) {
        diagnostics.push({
          line: openLineNum,
          severity: 'warning',
          code: 'EMPTY_SLOT_FACETS',
          message: `第 ${openLineNum} 行：槽位 "${rawSlotId || '?'}" 未订阅任何 Code IR 分片，建议至少订阅 1 项。`,
        });
      }

      const slotObj: SlotDefinition = {
        slot_id: rawSlotId,
        title: effectiveTitle,
        ownership: parsedFields.ownership ?? 'auto',
        required: parsedFields.required ?? true,
        ir_facets: irFacets,
        token_budget: parsedFields.tokenBudget ?? 8000,
        min_chars: parsedFields.minChars ?? 40,
        enable_source_tools: enableSourceTools,
        max_tool_rounds: maxToolRounds,
        instruction_hint: parsedFields.hint ?? '',
      };

      blocks.push({
        kind: 'slot',
        key: rawSlotId ? `slot_${rawSlotId}` : nextBlueprintKey('slot'),
        emitHeading,
        headingLevel,
        slot: slotObj,
      });
      slotCount += 1;
      idx = closeIdx + 1;
      continue;
    }

    const normalOpen = checkNormalFenceOpen(line);
    if (normalOpen) {
      normalFence = normalOpen;
    }
    staticBuffer.push(line);
    idx += 1;
  }

  flushStaticBuffer(staticBuffer);

  if (slotCount === 0 && !diagnostics.some((d) => d.severity === 'error')) {
    diagnostics.push({
      line: 1,
      severity: 'error',
      code: 'NO_SLOTS_DEFINED',
      message: '第 1 行：文档蓝图中至少需要定义 1 个 ```slot <slot_id> 章节槽位。',
    });
  }

  if (diagnostics.some((d) => d.severity === 'error')) {
    return { blueprint: null, diagnostics };
  }

  return {
    blueprint: { blocks },
    diagnostics,
  };
}

interface ParsedSlotBodyResult {
  title?: string;
  ownership?: SectionOwnership;
  required?: boolean;
  irFacets?: FacetName[];
  hadFacetError: boolean;
  tokenBudget?: number;
  minChars?: number;
  enableSourceTools?: boolean;
  maxToolRounds?: number;
  hint?: string;
}

function parseSlotBody(
  bodyLines: string[],
  startLineNum: number,
  diagnostics: BlueprintDiagnostic[],
): ParsedSlotBodyResult {
  const result: ParsedSlotBodyResult = { hadFacetError: false };
  const seenKeys = new Set<string>();

  let i = 0;
  while (i < bodyLines.length) {
    const rawLine = bodyLines[i];
    const lineNum = startLineNum + i;
    if (rawLine.trim() === '') {
      i += 1;
      continue;
    }

    const kvMatch = rawLine.match(/^\s*([a-zA-Z0-9_-]+)\s*:(.*)$/);
    if (!kvMatch) {
      diagnostics.push({
        line: lineNum,
        severity: 'error',
        code: 'INVALID_DSL_SYNTAX',
        message: `第 ${lineNum} 行：无法识别的槽位配置语句 "${rawLine.trim()}"，预期为 "key: value" 格式。`,
      });
      i += 1;
      continue;
    }

    const rawKey = kvMatch[1];
    const rawVal = kvMatch[2].trim();
    const canonicalKey = DSL_KEY_ALIASES[rawKey];

    if (!canonicalKey) {
      const suggestion = findClosestSuggestion(rawKey, ALL_ACCEPTED_DSL_KEY_NAMES, 3);
      const hintSuffix = suggestion ? `，是否指 "${suggestion}"？` : '。';
      diagnostics.push({
        line: lineNum,
        severity: 'error',
        code: 'UNKNOWN_DSL_KEY',
        message: `第 ${lineNum} 行：未知键 "${rawKey}"${hintSuffix} 合法键为：${KNOWN_DSL_KEYS.join(', ')}。`,
      });
      i += 1;
      continue;
    }

    if (seenKeys.has(canonicalKey)) {
      diagnostics.push({
        line: lineNum,
        severity: 'error',
        code: 'DUPLICATE_DSL_KEY',
        message: `第 ${lineNum} 行：槽位内重复声明了键 "${rawKey}"。`,
      });
      i += 1;
      continue;
    }
    seenKeys.add(canonicalKey);

    switch (canonicalKey) {
      case 'title': {
        if (!rawVal) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_TITLE',
            message: `第 ${lineNum} 行：title 不能为空。`,
          });
        } else if (Array.from(rawVal).length > 120) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_TITLE',
            message: `第 ${lineNum} 行：title 长度不能超过 120 个字符。`,
          });
        } else {
          result.title = rawVal;
        }
        i += 1;
        break;
      }

      case 'ownership': {
        if (rawVal !== 'auto' && rawVal !== 'hybrid' && rawVal !== 'locked') {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_OWNERSHIP',
            message: `第 ${lineNum} 行：ownership "${rawVal}" 非法，仅支持 auto | hybrid | locked。`,
          });
        } else {
          result.ownership = rawVal;
        }
        i += 1;
        break;
      }

      case 'required': {
        const parsedBool = parseStrictBool(rawVal);
        if (parsedBool === null) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_BOOLEAN',
            message: `第 ${lineNum} 行：${rawKey} 必须为布尔值 true 或 false（当前为 "${rawVal}"）。`,
          });
        } else {
          result.required = parsedBool;
        }
        i += 1;
        break;
      }

      case 'enable_source_tools': {
        const parsedBool = parseStrictBool(rawVal);
        if (parsedBool === null) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_BOOLEAN',
            message: `第 ${lineNum} 行：${rawKey} 必须为布尔值 true 或 false（当前为 "${rawVal}"）。`,
          });
        } else {
          result.enableSourceTools = parsedBool;
        }
        i += 1;
        break;
      }

      case 'token_budget': {
        const num = parseStrictInt(rawVal);
        if (num === null || num < 500 || num > 64000) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_TOKEN_BUDGET',
            message: `第 ${lineNum} 行：${rawKey} 必须为 500 ~ 64000 之间的整数（当前为 "${rawVal}"）。`,
          });
        } else {
          result.tokenBudget = num;
        }
        i += 1;
        break;
      }

      case 'min_chars': {
        const num = parseStrictInt(rawVal);
        if (num === null || num < 0 || num > 10000) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_MIN_CHARS',
            message: `第 ${lineNum} 行：min_chars 必须为 0 ~ 10000 之间的整数（当前为 "${rawVal}"）。`,
          });
        } else {
          result.minChars = num;
        }
        i += 1;
        break;
      }

      case 'max_tool_rounds': {
        const num = parseStrictInt(rawVal);
        if (num === null || num < 0 || num > 6) {
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_MAX_TOOL_ROUNDS',
            message: `第 ${lineNum} 行：${rawKey} 必须为 0 ~ 6 之间的整数（当前为 "${rawVal}"）。`,
          });
        } else {
          result.maxToolRounds = num;
        }
        i += 1;
        break;
      }

      case 'ir_facets': {
        const collected: Array<{ token: string; line: number }> = [];
        if (rawVal === '') {
          // 多行 - item 列表
          let nextIdx = i + 1;
          while (nextIdx < bodyLines.length) {
            const sub = bodyLines[nextIdx];
            if (sub.trim() === '') {
              nextIdx += 1;
              continue;
            }
            const itemMatch = sub.match(/^\s*-\s*(.+?)\s*$/);
            if (!itemMatch) {
              break;
            }
            collected.push({ token: itemMatch[1].trim(), line: startLineNum + nextIdx });
            nextIdx += 1;
          }
          i = nextIdx;
        } else if (rawVal.startsWith('[') && rawVal.endsWith(']')) {
          const inner = rawVal.slice(1, -1).trim();
          if (inner !== '') {
            for (const part of inner.split(',')) {
              const token = part.trim();
              if (token !== '') {
                collected.push({ token, line: lineNum });
              } else {
                result.hadFacetError = true;
                diagnostics.push({
                  line: lineNum,
                  severity: 'error',
                  code: 'INVALID_FACET_SYNTAX',
                  message: `第 ${lineNum} 行：ir_facets 列表中存在空元素。`,
                });
              }
            }
          }
          i += 1;
        } else {
          result.hadFacetError = true;
          diagnostics.push({
            line: lineNum,
            severity: 'error',
            code: 'INVALID_FACET_SYNTAX',
            message: `第 ${lineNum} 行：ir_facets 须使用 [a, b] 行内数组或多行 "- item" 列表格式。`,
          });
          i += 1;
          break;
        }

        const validFacets: FacetName[] = [];
        const seenFacet = new Set<string>();
        for (const item of collected) {
          if (item.token === 'module_context') {
            result.hadFacetError = true;
            diagnostics.push({
              line: item.line,
              severity: 'error',
              code: 'DISALLOWED_FACET',
              message: `第 ${item.line} 行：禁止在槽位中订阅 "module_context"（仅内部派生使用，请改在策略面板配置 context_inputs）。`,
            });
            continue;
          }
          if (!CANONICAL_FACET_SET.has(item.token)) {
            result.hadFacetError = true;
            const suggestion = findClosestSuggestion(item.token, CANONICAL_FACET_NAMES, 3);
            const suggestText = suggestion ? `，是否指 "${suggestion}"？` : '';
            diagnostics.push({
              line: item.line,
              severity: 'error',
              code: 'UNKNOWN_FACET',
              message: `第 ${item.line} 行：未知分片 "${item.token}"${suggestText}`,
            });
            continue;
          }
          if (!seenFacet.has(item.token)) {
            seenFacet.add(item.token);
            validFacets.push(item.token as FacetName);
          }
        }
        result.irFacets = validFacets;
        break;
      }

      case 'hint': {
        if (rawVal === '|' || rawVal === '|-') {
          const blockLines: string[] = [];
          let blockIndent: number | null = null;
          let nextIdx = i + 1;

          while (nextIdx < bodyLines.length) {
            const sub = bodyLines[nextIdx];
            if (sub.trim() === '') {
              blockLines.push('');
              nextIdx += 1;
              continue;
            }
            const indentMatch = sub.match(/^(\s+)/);
            if (!indentMatch) {
              // 无缩进的非空行标志着多行缩进块结束
              break;
            }
            if (blockIndent === null) {
              blockIndent = Math.min(2, indentMatch[1].length);
              if (indentMatch[1].length >= 2) {
                blockIndent = indentMatch[1].length;
              }
            }
            const stripCount = Math.min(blockIndent, indentMatch[1].length);
            blockLines.push(sub.slice(stripCount));
            nextIdx += 1;
          }

          // 移除末尾由空行占位产生的多余尾行
          while (blockLines.length > 0 && blockLines[blockLines.length - 1] === '') {
            blockLines.pop();
          }
          const hintText = blockLines.join('\n');
          if (Array.from(hintText).length > 1000) {
            diagnostics.push({
              line: lineNum,
              severity: 'error',
              code: 'HINT_TOO_LONG',
              message: `第 ${lineNum} 行：instruction_hint 长度不能超过 1000 个字符（当前 ${Array.from(hintText).length} 字）。`,
            });
          } else {
            result.hint = hintText;
          }
          i = nextIdx;
        } else {
          if (Array.from(rawVal).length > 1000) {
            diagnostics.push({
              line: lineNum,
              severity: 'error',
              code: 'HINT_TOO_LONG',
              message: `第 ${lineNum} 行：instruction_hint 长度不能超过 1000 个字符（当前 ${Array.from(rawVal).length} 字）。`,
            });
          } else {
            result.hint = rawVal;
          }
          i += 1;
        }
        break;
      }
    }
  }

  return result;
}

function parseStrictBool(raw: string): boolean | null {
  if (raw === 'true') return true;
  if (raw === 'false') return false;
  return null;
}

/**
 * 按 9 大规范分片标准顺序去重汇总所有槽位的 ir_facets 并集（排除内部派生分片 module_context）。
 */
export function computeCanonicalFacetsUnion(
  slots: readonly SlotDefinition[],
): CanonicalFacetName[] {
  const present = new Set<string>();
  for (const sl of slots) {
    for (const f of sl.ir_facets ?? []) {
      if (CANONICAL_FACET_SET.has(f)) {
        present.add(f);
      }
    }
  }
  return CANONICAL_FACET_NAMES.filter((f) => present.has(f));
}

/**
 * 判断存量 DocType 是否应默认采用自动并集模式：
 * 若 meta.ir_facets 为空，或与所有槽位分片并集等价（顺序无关），返回 true；
 * 若存在显式手工定制差异且非空，返回 false 以保留存量配置。
 */
export function shouldDefaultAutoFacetsUnion(
  metaFacets: readonly string[] | undefined,
  slots: readonly SlotDefinition[],
): boolean {
  if (!metaFacets || metaFacets.length === 0) {
    return true;
  }
  const union = computeCanonicalFacetsUnion(slots);
  const normalizedMeta = CANONICAL_FACET_NAMES.filter((f) => metaFacets.includes(f));
  if (normalizedMeta.length !== metaFacets.length) {
    return false;
  }
  if (normalizedMeta.length !== union.length) {
    return false;
  }
  return normalizedMeta.every((f, idx) => f === union[idx]);
}

/**
 * 检测当前槽位列表中缺失（被删或改名）且尚未确认的既有 persistedSlotIds。
 */
export function detectUnconfirmedMissingSlotIds(
  persistedSlotIds: ReadonlySet<string>,
  currentSlots: readonly SlotDefinition[],
  confirmedSlotIds?: ReadonlySet<string>,
): string[] {
  const currentSet = new Set(currentSlots.map((s) => s.slot_id));
  const missing: string[] = [];
  for (const pid of persistedSlotIds) {
    if (!currentSet.has(pid) && !(confirmedSlotIds?.has(pid) ?? false)) {
      missing.push(pid);
    }
  }
  return missing;
}
