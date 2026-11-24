import type {
  ArchTreeNodeVO,
  EffectiveRefDocVO,
  RefDocCitationVO,
  RefDocKind,
  RefDocScopeLevel,
} from '../types';

export const REF_DOC_KIND_LABELS: Record<RefDocKind, string> = {
  arch_design: '架构设计',
  domain_glossary: '领域术语',
  interface_spec: '接口契约',
  adr_constraint: 'ADR 约束',
  other: '其他参考',
};

export const REF_DOC_SCOPE_LABELS: Record<RefDocScopeLevel, string> = {
  system: '全系统',
  subsystem: '子系统',
  group: '功能组',
  module: '功能模块',
  repo: '代码仓',
};

const SCOPE_ORDER: readonly RefDocScopeLevel[] = [
  'module',
  'repo',
  'group',
  'subsystem',
  'system',
];

export function formatRefDocKindLabel(kind: string): string {
  return REF_DOC_KIND_LABELS[kind as RefDocKind] ?? kind;
}

export function formatRefDocScopeLabel(scopeLevel: string): string {
  return REF_DOC_SCOPE_LABELS[scopeLevel as RefDocScopeLevel] ?? scopeLevel;
}

export function formatRefDocTokenBadge(tokens: number): string {
  if (!Number.isFinite(tokens) || tokens <= 0) {
    return '0 T';
  }
  if (tokens >= 1000) {
    return `${(tokens / 1000).toFixed(1)}k T`;
  }
  return `${Math.round(tokens)} T`;
}

export function groupEffectiveRefDocsByScope(
  docs: readonly EffectiveRefDocVO[],
): { level: RefDocScopeLevel; label: string; items: EffectiveRefDocVO[] }[] {
  if (docs.length === 0) {
    return [];
  }
  const buckets = new Map<RefDocScopeLevel, EffectiveRefDocVO[]>();
  for (const doc of docs) {
    const level = doc.effective_scope_level;
    const list = buckets.get(level);
    if (list) {
      list.push(doc);
    } else {
      buckets.set(level, [doc]);
    }
  }
  const result: { level: RefDocScopeLevel; label: string; items: EffectiveRefDocVO[] }[] = [];
  for (const level of SCOPE_ORDER) {
    const items = buckets.get(level);
    if (items && items.length > 0) {
      result.push({
        level,
        label: formatRefDocScopeLabel(level),
        items,
      });
    }
  }
  return result;
}

export function buildExcludedRefDocIds(
  docs: readonly EffectiveRefDocVO[],
  checkedDocIds: ReadonlySet<number>,
): number[] {
  if (docs.length === 0) {
    return [];
  }
  const excluded: number[] = [];
  for (const doc of docs) {
    if (!checkedDocIds.has(doc.doc_id)) {
      excluded.push(doc.doc_id);
    }
  }
  return excluded;
}

export function computeIncludedRefDocStats(
  docs: readonly EffectiveRefDocVO[],
  checkedDocIds: ReadonlySet<number>,
): { includedCount: number; includedTokens: number; totalCount: number } {
  let includedCount = 0;
  let includedTokens = 0;
  for (const doc of docs) {
    if (checkedDocIds.has(doc.doc_id)) {
      includedCount += 1;
      includedTokens += Math.max(0, doc.estimated_tokens || 0);
    }
  }
  return {
    includedCount,
    includedTokens,
    totalCount: docs.length,
  };
}

export function formatRefDocSummaryStaleHint(
  summaryForVersion: number,
  currentVersion: number,
): string {
  if (summaryForVersion >= currentVersion) {
    return '';
  }
  return `摘要滞后于当前正文（摘要基于 v${summaryForVersion}，当前正文 v${currentVersion}）`;
}

export function formatArchNodeRefDocBadgeText(
  node: Pick<ArchTreeNodeVO, 'direct_ref_doc_count' | 'inherited_ref_doc_count'>,
): string {
  const direct = Math.max(0, node.direct_ref_doc_count ?? 0);
  const inherited = Math.max(0, node.inherited_ref_doc_count ?? 0);
  const total = direct + inherited;
  if (total === 0) {
    return '';
  }
  if (inherited === 0) {
    return `参考 ${direct}`;
  }
  return `参考 ${direct}+${inherited}`;
}

export function formatRefCitationBadgeLabel(citation: RefDocCitationVO): string {
  const title = citation.doc_title || `参考#${citation.doc_id}`;
  const secPart = citation.section_title ? ` · ${citation.section_title}` : '';
  const stalePart = citation.stale ? ' (已过期)' : '';
  return `${title} v${citation.version_no}${secPart}${stalePart}`;
}
