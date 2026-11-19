import type { RepoScopeFilter, RepoStatusFilter } from '../types';

export const ALLOWED_PAGE_SIZES = [15, 25, 50, 100] as const;
export const DEFAULT_ARCHDOC_PAGE_SIZE = 25;

export interface RepoFilterState {
  page: number;
  pageSize: number;
  search: string;
  departmentId: string;
  docType: string;
  status: RepoStatusFilter;
  scopeType: RepoScopeFilter;
}

export interface EvidenceDeepLinkParams {
  commit?: string;
  file?: string;
  symbol?: string;
  lineStart?: number;
  lineEnd?: number;
}

const VALID_STATUS_SET = new Set<string>(['all', 'approved', 'draft', 'drifted', 'missing']);
const VALID_SCOPE_SET = new Set<string>(['all', 'repo', 'subdir']);
const COMMIT_SHA_RE = /^[0-9a-f]{7,40}$/i;

/**
 * 规范化分页条数，非法值自动回退至默认值 25。
 */
export function normalizePageSize(raw: string | number | null | undefined): number {
  const parsed = typeof raw === 'number' ? raw : Number.parseInt(String(raw ?? ''), 10);
  if (ALLOWED_PAGE_SIZES.includes(parsed as (typeof ALLOWED_PAGE_SIZES)[number])) {
    return parsed;
  }
  return DEFAULT_ARCHDOC_PAGE_SIZE;
}

/**
 * 规范化页码，非正整数回退至 1。
 */
export function normalizePage(raw: string | number | null | undefined): number {
  const parsed = typeof raw === 'number' ? raw : Number.parseInt(String(raw ?? ''), 10);
  if (!Number.isFinite(parsed) || parsed < 1) {
    return 1;
  }
  return Math.floor(parsed);
}

/**
 * 从 URLSearchParams 反序列化仓库覆盖页分页与筛选参数。
 */
export function parseRepoFilterParams(params: URLSearchParams): RepoFilterState {
  const page = normalizePage(params.get('page'));
  const pageSize = normalizePageSize(params.get('pageSize') ?? params.get('page_size'));
  const search = (params.get('search') ?? '').trim();

  const rawDept = (params.get('department_id') ?? '').trim();
  const departmentId = /^\d+$/.test(rawDept) && Number.parseInt(rawDept, 10) > 0 ? rawDept : '';

  const docType = (params.get('doc_type') ?? '').trim();

  const rawStatus = (params.get('status') ?? '').trim().toLowerCase();
  const status: RepoStatusFilter = VALID_STATUS_SET.has(rawStatus)
    ? (rawStatus as RepoStatusFilter)
    : '';

  const rawScope = (params.get('scope_type') ?? '').trim().toLowerCase();
  const scopeType: RepoScopeFilter = VALID_SCOPE_SET.has(rawScope)
    ? (rawScope as RepoScopeFilter)
    : '';

  return {
    page,
    pageSize,
    search,
    departmentId,
    docType,
    status,
    scopeType,
  };
}

/**
 * 将仓库覆盖页分页与筛选状态序列化回 URLSearchParams（支持浏览器前进/后退同步）。
 */
export function serializeRepoFilterParams(
  patch: Partial<RepoFilterState>,
  prev?: URLSearchParams,
): URLSearchParams {
  const current = parseRepoFilterParams(prev ?? new URLSearchParams());
  const nextState: RepoFilterState = {
    ...current,
    ...patch,
    page: normalizePage(patch.page !== undefined ? patch.page : current.page),
    pageSize: normalizePageSize(patch.pageSize !== undefined ? patch.pageSize : current.pageSize),
  };

  const out = new URLSearchParams();
  out.set('page', String(nextState.page));
  out.set('pageSize', String(nextState.pageSize));
  if (nextState.search.trim()) {
    out.set('search', nextState.search.trim());
  }
  if (nextState.departmentId.trim()) {
    out.set('department_id', nextState.departmentId.trim());
  }
  if (nextState.docType.trim()) {
    out.set('doc_type', nextState.docType.trim());
  }
  if (nextState.status && nextState.status !== 'all') {
    out.set('status', nextState.status);
  }
  if (nextState.scopeType && nextState.scopeType !== 'all') {
    out.set('scope_type', nextState.scopeType);
  }
  return out;
}

/**
 * 解析 lines="142-188" 或 lines="142" 行号区间，非法/越界/负数安全兜底返回 undefined，反序自动归一化。
 */
export function parseLineRange(raw: string | null | undefined): { lineStart: number; lineEnd: number } | undefined {
  if (!raw) return undefined;
  const cleaned = raw.trim().replace(/^L/i, '');
  if (!cleaned) return undefined;

  const rangeMatch = /^(\d+)(?:\s*-\s*L?(\d+))?$/.exec(cleaned);
  if (!rangeMatch) {
    return undefined;
  }

  const start = Number.parseInt(rangeMatch[1], 10);
  const end = rangeMatch[2] ? Number.parseInt(rangeMatch[2], 10) : start;
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start <= 0 || end <= 0) {
    return undefined;
  }
  return start <= end ? { lineStart: start, lineEnd: end } : { lineStart: end, lineEnd: start };
}

/**
 * 解析深链 /docs/:docId/evidence?commit=...&file=...&symbol=...&lines=142-188 参数。
 * 兼容 lines=a-b 与显式 line_start/line_end，对非法参数执行安全兜底。
 */
export function parseEvidenceDeepLinkParams(params: URLSearchParams): EvidenceDeepLinkParams {
  const rawCommit = (params.get('commit') ?? '').trim();
  const commit = COMMIT_SHA_RE.test(rawCommit) ? rawCommit : undefined;

  const rawFile = (params.get('file') ?? '').trim();
  const file = rawFile ? rawFile : undefined;

  const rawSymbol = (params.get('symbol') ?? '').trim();
  const symbol = rawSymbol ? rawSymbol : undefined;

  const parsedLines = parseLineRange(params.get('lines'));
  let lineStart = parsedLines?.lineStart;
  let lineEnd = parsedLines?.lineEnd;

  if (lineStart === undefined) {
    const startParam = parsePositiveInt(params.get('line_start'));
    const endParam = parsePositiveInt(params.get('line_end'));
    if (startParam !== undefined) {
      if (endParam !== undefined) {
        lineStart = Math.min(startParam, endParam);
        lineEnd = Math.max(startParam, endParam);
      } else {
        lineStart = startParam;
        lineEnd = startParam;
      }
    }
  }

  return {
    commit,
    file,
    symbol,
    lineStart,
    lineEnd,
  };
}

function parsePositiveInt(raw: string | null): number | undefined {
  if (!raw) return undefined;
  const trimmed = raw.trim();
  if (!/^\d+$/.test(trimmed)) return undefined;
  const value = Number.parseInt(trimmed, 10);
  return Number.isSafeInteger(value) && value > 0 ? value : undefined;
}

/**
 * 解析 Markdown 内部的证据深链（如 /archdoc/docs/42/evidence?commit=...&file=...&lines=10-20）
 */
export function parseEvidenceHref(href: string): ({ docId: number } & EvidenceDeepLinkParams) | null {
  const trimmed = href.trim();
  const match = /^(?:\/archdoc)?\/docs\/(\d+)\/evidence(?:\?(.*))?$/.exec(trimmed);
  if (!match) return null;
  const docId = Number.parseInt(match[1], 10);
  if (!Number.isSafeInteger(docId) || docId <= 0) return null;
  const query = new URLSearchParams(match[2] ?? '');
  return {
    docId,
    ...parseEvidenceDeepLinkParams(query),
  };
}

/**
 * 生成兼顾独立态与门户嵌入态的 Canvas Studio 路由路径。
 */
export function buildStudioPath(
  isEmbedded: boolean,
  repoId: number,
  options?: { docId?: number; subdirectory?: string },
): string {
  const prefix = isEmbedded ? '/archdoc' : '';
  const base = options?.docId
    ? `${prefix}/studio/${repoId}/docs/${options.docId}`
    : `${prefix}/studio/${repoId}`;
  const subdir = options?.subdirectory?.trim();
  if (subdir) {
    const qs = new URLSearchParams({ subdir });
    return `${base}?${qs.toString()}`;
  }
  return base;
}

/**
 * 格式化时间字符串（中文 24 小时制）
 */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '-';
  const dt = new Date(iso);
  if (Number.isNaN(dt.getTime())) return iso;
  return dt.toLocaleString('zh-CN', { hour12: false });
}

/**
 * 截断 Commit SHA 展示短码
 */
export function shortSha(sha: string | null | undefined): string {
  if (!sha) return '-';
  return sha.trim().slice(0, 8);
}
