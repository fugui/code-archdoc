import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Check,
  ChevronDown,
  GitBranch,
  Loader2,
  Lock,
  RefreshCw,
  RotateCcw,
  Search,
} from 'lucide-react';
import { useConfirm, useOutsideClick, useToast } from '@code/common';
import { listRepoBranches, switchRepoBranch } from '../api/repos';
import type { RepoBranchItemVO, RepoBranchesVO, SwitchRepoBranchVO } from '../types';
import {
  buildBranchSwitchConfirmContent,
  formatBranchBadgeLabel,
  sortAndFilterBranches,
} from '../utils/branchUtils';
import { formatApiError } from '../utils/errorMessages';
import { shortSha } from '../utils/urlQuery';

export interface RepoBranchSelectorProps {
  repoId: number;
  repoName: string;
  currentBranch: string;
  defaultBranch?: string;
  isOverridden?: boolean;
  canSwitch: boolean;
  disabled?: boolean;
  size?: 'sm' | 'md';
  onSwitched?: (result: SwitchRepoBranchVO) => void;
}

export const RepoBranchSelector: React.FC<RepoBranchSelectorProps> = ({
  repoId,
  repoName,
  currentBranch,
  defaultBranch,
  isOverridden = false,
  canSwitch,
  disabled = false,
  size = 'md',
  onSwitched,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [open, setOpen] = useState<boolean>(false);
  const [loading, setLoading] = useState<boolean>(false);
  const [switching, setSwitching] = useState<boolean>(false);
  const [loadError, setLoadError] = useState<string>('');
  const [keyword, setKeyword] = useState<string>('');
  const [branchData, setBranchData] = useState<RepoBranchesVO | null>(null);

  const containerRef = useRef<HTMLDivElement | null>(null);

  const closePopover = useCallback(() => {
    setOpen(false);
  }, []);

  useOutsideClick(containerRef, closePopover);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [open]);

  // 切换仓库时清理旧分支下拉缓存
  useEffect(() => {
    setOpen(false);
    setBranchData(null);
    setLoadError('');
    setKeyword('');
  }, [repoId]);

  const effectiveDefaultBranch = (branchData?.default_branch || defaultBranch || '').trim() || 'master';
  const effectiveCurrentBranch = (currentBranch || '').trim() || effectiveDefaultBranch;
  const effectiveIsOverridden = Boolean(
    isOverridden || (effectiveDefaultBranch && effectiveCurrentBranch !== effectiveDefaultBranch),
  );

  const badgeMeta = useMemo(
    () => formatBranchBadgeLabel(effectiveIsOverridden, effectiveDefaultBranch),
    [effectiveIsOverridden, effectiveDefaultBranch],
  );

  const fetchBranches = useCallback(
    async (refresh: boolean) => {
      if (!repoId) return;
      setLoading(true);
      setLoadError('');
      try {
        const res = await listRepoBranches(repoId, refresh);
        setBranchData(res);
      } catch (err: unknown) {
        const formatted = formatApiError(err, '加载远端分支列表失败');
        setLoadError(formatted.message);
      } finally {
        setLoading(false);
      }
    },
    [repoId],
  );

  const handleToggleOpen = () => {
    if (!canSwitch || disabled || switching) return;
    const nextOpen = !open;
    setOpen(nextOpen);
    if (nextOpen && !branchData && !loading) {
      void fetchBranches(false);
    }
  };

  const handleSelectBranch = async (targetBranch: string, resetToDefault = false) => {
    const normalizedTarget = targetBranch.trim();
    if (!canSwitch || disabled || switching) return;
    if (!resetToDefault && normalizedTarget === effectiveCurrentBranch) {
      setOpen(false);
      return;
    }

    const confirmCfg = buildBranchSwitchConfirmContent({
      repoName,
      currentBranch: effectiveCurrentBranch,
      targetBranch: resetToDefault ? effectiveDefaultBranch : normalizedTarget,
      defaultBranch: effectiveDefaultBranch,
    });

    const ok = await confirm({
      title: confirmCfg.title,
      content: confirmCfg.content,
      confirmText: confirmCfg.confirmText,
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;

    setSwitching(true);
    try {
      const payloadBranch = resetToDefault ? '' : normalizedTarget;
      const result = await switchRepoBranch(repoId, { branch: payloadBranch });
      setOpen(false);
      setBranchData(null);

      const summaryParts: string[] = [
        `已切换跟踪分支至 ${result.current_branch}`,
        `重绑存量文档 ${result.rebound_documents} 篇`,
      ];
      if (result.drifted_documents > 0) {
        summaryParts.push(`检测到 ${result.drifted_documents} 篇已发布文档存在代码漂移`);
      }
      showToast(summaryParts.join('，'), 'success');
      onSwitched?.(result);
    } catch (err: unknown) {
      const formatted = formatApiError(err, '切换仓库跟踪分支失败');
      showToast(formatted.message, 'error');
    } finally {
      setSwitching(false);
    }
  };

  const visibleBranches = useMemo(() => {
    const raw: RepoBranchItemVO[] = (branchData?.branches || []).map((item) => ({
      ...item,
      is_default: item.name === effectiveDefaultBranch,
      is_current: item.name === effectiveCurrentBranch,
    }));
    return sortAndFilterBranches(raw, keyword);
  }, [branchData, effectiveDefaultBranch, effectiveCurrentBranch, keyword]);

  const readOnlyTitle = disabled
    ? '当前任务执行或查看状态下不可修改分支'
    : '仅仓库负责人或管理员可切换跟踪分支';

  return (
    <div
      ref={containerRef}
      className={`code-archdoc-branch-selector code-archdoc-branch-selector--${size}`}
    >
      {canSwitch && !disabled ? (
        <button
          type="button"
          className={`code-archdoc-branch-trigger ${
            effectiveIsOverridden ? 'code-archdoc-branch-trigger--overridden' : ''
          }`}
          onClick={handleToggleOpen}
          disabled={switching}
          aria-expanded={open}
          aria-haspopup="listbox"
          title={`${badgeMeta.title}（点击切换分支）`}
        >
          {switching ? (
            <Loader2 size={size === 'sm' ? 12 : 13} className="code-archdoc-spin" />
          ) : (
            <GitBranch size={size === 'sm' ? 12 : 13} />
          )}
          <span className="code-archdoc-branch-name code-archdoc-mono">
            {effectiveCurrentBranch}
          </span>
          <span
            className={`code-archdoc-branch-tag ${
              effectiveIsOverridden
                ? 'code-archdoc-branch-tag--custom'
                : 'code-archdoc-branch-tag--default'
            }`}
          >
            {badgeMeta.label}
          </span>
          <ChevronDown size={size === 'sm' ? 12 : 13} />
        </button>
      ) : (
        <span
          className={`code-archdoc-branch-readonly ${
            effectiveIsOverridden ? 'code-archdoc-branch-readonly--overridden' : ''
          }`}
          title={readOnlyTitle}
        >
          <GitBranch size={size === 'sm' ? 12 : 13} />
          <span className="code-archdoc-branch-name code-archdoc-mono">
            {effectiveCurrentBranch}
          </span>
          <span
            className={`code-archdoc-branch-tag ${
              effectiveIsOverridden
                ? 'code-archdoc-branch-tag--custom'
                : 'code-archdoc-branch-tag--default'
            }`}
          >
            {badgeMeta.label}
          </span>
          <Lock size={11} className="code-archdoc-branch-lock-icon" />
        </span>
      )}

      {open && canSwitch && !disabled && (
        <div
          className="code-archdoc-branch-popover"
          role="dialog"
          aria-label="切换仓库跟踪分支"
        >
          <div className="code-archdoc-branch-popover-head">
            <div className="code-archdoc-branch-search-wrap">
              <Search size={13} className="code-archdoc-branch-search-icon" />
              <input
                type="text"
                className="code-input code-archdoc-branch-search-input"
                placeholder="筛选远端分支名称..."
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                autoFocus
              />
            </div>
            <button
              type="button"
              className="btn btn-text btn-small"
              disabled={loading || switching}
              onClick={() => void fetchBranches(true)}
              title="强制从远端仓库重新拉取最新分支列表"
            >
              <RefreshCw size={13} className={loading ? 'code-archdoc-spin' : ''} />
            </button>
          </div>

          {effectiveIsOverridden && (
            <div className="code-archdoc-branch-reset-bar">
              <button
                type="button"
                className="code-archdoc-branch-reset-btn"
                disabled={switching}
                onClick={() => void handleSelectBranch(effectiveDefaultBranch, true)}
              >
                <RotateCcw size={12} />
                <span>
                  恢复默认分支 (<code className="code-archdoc-mono">{effectiveDefaultBranch}</code>)
                </span>
              </button>
            </div>
          )}

          <div className="code-archdoc-branch-list" role="listbox">
            {loading && !branchData && (
              <div className="code-archdoc-branch-state">
                <Loader2 size={14} className="code-archdoc-spin" />
                <span>正在拉取远端分支列表...</span>
              </div>
            )}

            {!loading && loadError && (
              <div className="code-archdoc-branch-error">
                <span>{loadError}</span>
                <button
                  type="button"
                  className="btn btn-outline btn-small"
                  onClick={() => void fetchBranches(true)}
                >
                  重试
                </button>
              </div>
            )}

            {!loadError && branchData && visibleBranches.length === 0 && (
              <div className="code-archdoc-branch-state">
                <span>未匹配到符合条件的远端分支</span>
              </div>
            )}

            {!loadError &&
              visibleBranches.map((item) => (
                <button
                  key={item.name}
                  type="button"
                  role="option"
                  aria-selected={item.is_current}
                  disabled={switching}
                  className={`code-archdoc-branch-option ${
                    item.is_current ? 'code-archdoc-branch-option--current' : ''
                  }`}
                  onClick={() =>
                    void handleSelectBranch(item.name, item.is_default && effectiveIsOverridden)
                  }
                >
                  <span className="code-archdoc-branch-option-main">
                    <span className="code-archdoc-branch-option-check">
                      {item.is_current && <Check size={13} />}
                    </span>
                    <span className="code-archdoc-branch-option-name code-archdoc-mono">
                      {item.name}
                    </span>
                    {item.is_default && (
                      <span className="code-archdoc-branch-tag code-archdoc-branch-tag--default">
                        默认
                      </span>
                    )}
                    {item.is_current && (
                      <span className="code-archdoc-branch-tag code-archdoc-branch-tag--active">
                        当前
                      </span>
                    )}
                  </span>
                  <code className="code-archdoc-branch-option-sha code-archdoc-mono">
                    {shortSha(item.commit_sha)}
                  </code>
                </button>
              ))}
          </div>
        </div>
      )}
    </div>
  );
};

export default RepoBranchSelector;
