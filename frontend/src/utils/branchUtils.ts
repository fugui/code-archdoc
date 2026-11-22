import type { RepoBranchItemVO } from '../types';

/**
 * 对远端分支列表按「当前跟踪分支置顶 -> 仓库默认分支次之 -> 其余按字典序」排序，
 * 并按关键字（大小写不敏感子串）过滤。
 */
export function sortAndFilterBranches(
  branches: RepoBranchItemVO[],
  keyword: string,
): RepoBranchItemVO[] {
  const normalizedKeyword = keyword.trim().toLowerCase();
  const filtered = normalizedKeyword
    ? branches.filter((item) => item.name.toLowerCase().includes(normalizedKeyword))
    : [...branches];

  filtered.sort((a, b) => {
    if (a.is_current !== b.is_current) {
      return a.is_current ? -1 : 1;
    }
    if (a.is_default !== b.is_default) {
      return a.is_default ? -1 : 1;
    }
    return a.name.localeCompare(b.name);
  });

  return filtered;
}

/**
 * 根据分支是否覆盖仓库默认分支，生成展示标签与悬浮提示文案。
 */
export function formatBranchBadgeLabel(
  isOverridden: boolean | undefined,
  defaultBranch: string | undefined,
): { label: string; title: string } {
  const normalizedDefault = (defaultBranch || '').trim() || 'master';
  if (isOverridden) {
    return {
      label: '自定义',
      title: `已切换为自定义跟踪分支（仓库默认分支为 ${normalizedDefault}）`,
    };
  }
  return {
    label: '默认',
    title: `正在跟随仓库默认分支 ${normalizedDefault}`,
  };
}

/**
 * 构造切换目标分支的二次确认弹窗文案，明确告知存量文档无损重绑与漂移重算影响。
 */
export function buildBranchSwitchConfirmContent(params: {
  repoName: string;
  currentBranch: string;
  targetBranch: string;
  defaultBranch?: string;
}): { title: string; content: string; confirmText: string } {
  const repoLabel = params.repoName.trim() || '当前仓库';
  const defaultBranch = (params.defaultBranch || '').trim();
  const target = params.targetBranch.trim();
  const isResettingToDefault =
    Boolean(defaultBranch) && (target === '' || target === defaultBranch);
  const displayTarget = target || defaultBranch || '默认分支';

  if (isResettingToDefault) {
    return {
      title: '确认恢复仓库默认分支',
      content:
        `确定将仓库「${repoLabel}」的跟踪分支从 ${params.currentBranch} 恢复为默认分支 ${displayTarget} 吗？` +
        ' 切换后，该仓库的全部存量设计文档（含已发布版本、修订历史与人工锁定章节）将自动绑定到默认分支，并立即与远端最新提交比对代码漂移状态。',
      confirmText: '确认恢复默认分支',
    };
  }

  return {
    title: '确认切换仓库跟踪分支',
    content:
      `确定将仓库「${repoLabel}」的跟踪分支从 ${params.currentBranch} 切换为 ${displayTarget} 吗？` +
      ' 切换后，仓库下所有存量文档与后续生成、模块扫描、远端漂移轮询均以新分支为准；已发布文档与人工锁定章节会无损保留并按新分支 HEAD 重新评估漂移状态。',
    confirmText: '确认切换分支',
  };
}
