import type {
  DocStatusSummaryVO,
  DocTypeSummaryVO,
  RepoScopeCoverageVO,
} from '../types';

export const DOC_TYPE_CATEGORY_PRESETS = [
  '架构设计',
  '研发指南',
  '运维交付',
  'AI 上下文',
  '质量与安全',
] as const;

export const FALLBACK_DOC_CATEGORY = '其他文档';

export interface DocTypeCategoryGroup<T> {
  category: string;
  items: T[];
}

export interface ScopeDocSummaryItem {
  docType: DocTypeSummaryVO;
  statusSummary: DocStatusSummaryVO;
}

export interface ScopeDocsSummary {
  totalTypes: number;
  generatedCount: number;
  approvedCount: number;
  draftCount: number;
  driftedCount: number;
  blockedCount: number;
  missingCount: number;
  generatedItems: ScopeDocSummaryItem[];
  missingItems: DocTypeSummaryVO[];
}

/**
 * 获取文档类型紧凑短标题，缺省时回退至 display_name。
 */
export function getDocTypeShortName(
  dt: Pick<DocTypeSummaryVO, 'short_name' | 'display_name'>,
): string {
  const shortName = (dt.short_name ?? '').trim();
  return shortName || (dt.display_name ?? '').trim();
}

/**
 * 获取文档类型分组名称，缺省时回退至「其他文档」。
 */
export function getDocTypeCategory(dt: Pick<DocTypeSummaryVO, 'category'>): string {
  const cat = (dt.category ?? '').trim();
  return cat || FALLBACK_DOC_CATEGORY;
}

/**
 * 按预设顺序优先、其余分类按首次出现顺序、兜底分类置尾的原则对文档类型列表分组。
 */
export function groupDocTypesByCategory<T extends Pick<DocTypeSummaryVO, 'category'>>(
  docTypes: readonly T[],
): Array<DocTypeCategoryGroup<T>> {
  const bucketMap = new Map<string, T[]>();
  for (const dt of docTypes) {
    const category = getDocTypeCategory(dt);
    const list = bucketMap.get(category);
    if (list) {
      list.push(dt);
    } else {
      bucketMap.set(category, [dt]);
    }
  }

  const orderedGroups: Array<DocTypeCategoryGroup<T>> = [];
  const consumed = new Set<string>();

  for (const preset of DOC_TYPE_CATEGORY_PRESETS) {
    const items = bucketMap.get(preset);
    if (items && items.length > 0) {
      orderedGroups.push({ category: preset, items });
      consumed.add(preset);
    }
  }

  for (const [category, items] of bucketMap.entries()) {
    if (consumed.has(category) || category === FALLBACK_DOC_CATEGORY) {
      continue;
    }
    orderedGroups.push({ category, items });
    consumed.add(category);
  }

  const fallbackItems = bucketMap.get(FALLBACK_DOC_CATEGORY);
  if (fallbackItems && fallbackItems.length > 0) {
    orderedGroups.push({ category: FALLBACK_DOC_CATEGORY, items: fallbackItems });
  }

  return orderedGroups;
}

/**
 * 汇总单个范围（整仓/子目录）在当前启用文档类型集合下的覆盖与健康状态（方案 A1 核心计算）。
 */
export function summarizeScopeDocs(
  row: Pick<RepoScopeCoverageVO, 'doc_statuses'>,
  docTypes: readonly DocTypeSummaryVO[],
): ScopeDocsSummary {
  const docStatuses = row.doc_statuses ?? {};
  const generatedItems: ScopeDocSummaryItem[] = [];
  const missingItems: DocTypeSummaryVO[] = [];

  let approvedCount = 0;
  let draftCount = 0;
  let driftedCount = 0;
  let blockedCount = 0;

  for (const dt of docTypes) {
    const ds = docStatuses[dt.slug];
    if (!ds) {
      missingItems.push(dt);
      continue;
    }
    generatedItems.push({ docType: dt, statusSummary: ds });
    if (ds.status === 'approved') {
      approvedCount++;
      if (ds.has_code_drift || ds.has_spec_drift) {
        driftedCount++;
      }
    } else {
      draftCount++;
      if (!ds.can_approve) {
        blockedCount++;
      }
    }
  }

  return {
    totalTypes: docTypes.length,
    generatedCount: generatedItems.length,
    approvedCount,
    draftCount,
    driftedCount,
    blockedCount,
    missingCount: missingItems.length,
    generatedItems,
    missingItems,
  };
}

/**
 * 将架构元素类型枚举映射为中文标签。
 */
export function formatArchElementTypeLabel(type?: string | null): string {
  switch ((type ?? '').trim()) {
    case 'subsystem':
      return '子系统';
    case 'group':
      return '功能组';
    case 'module':
      return '功能模块';
    default:
      return '架构节点';
  }
}
