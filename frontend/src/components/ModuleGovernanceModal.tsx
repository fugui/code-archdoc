import React, { useCallback, useEffect, useState } from 'react';
import { ArrowRightLeft, Eye, EyeOff, FolderTree, Loader2, RefreshCw, Save } from 'lucide-react';
import { EmptyState, Modal, StatusTag, useConfirm, useToast, type StatusType } from '@code/common';
import {
  listRepoModules,
  migrateRepoModule,
  refreshRepoModules,
  updateRepoModulePolicy,
  updateRepoModuleStatus,
} from '../api/repos';
import type { ModuleDiscoveryStrategy, ModuleStatus, RepoModuleVO } from '../types';
import { formatApiError } from '../utils/errorMessages';
import { shortSha } from '../utils/urlQuery';

export interface ModuleGovernanceTarget {
  repoId: number;
  repoName: string;
  branch: string;
  canWrite: boolean;
}

export interface ModuleGovernanceModalProps {
  open: boolean;
  target: ModuleGovernanceTarget | null;
  onClose: () => void;
  onChanged?: () => void;
}

const STRATEGY_OPTIONS: Array<{ value: ModuleDiscoveryStrategy; label: string }> = [
  { value: 'merged', label: '混合发现（架构元素 + 构建清单 + 一级目录）' },
  { value: 'arch_elements', label: '仅架构元素表 (architecture_elements)' },
  { value: 'build_manifests', label: '仅构建清单 (CMake/Maven/Gradle/pyproject)' },
  { value: 'first_level_dirs', label: '仅一级源码子目录' },
  { value: 'disabled', label: '关闭自动发现（仅整仓）' },
];

function moduleStatusMeta(status: ModuleStatus): { text: string; tag: StatusType } {
  switch (status) {
    case 'active':
      return { text: '活跃', tag: 'success' };
    case 'ignored':
      return { text: '已忽略', tag: 'neutral' };
    case 'missing':
      return { text: '目录缺失', tag: 'warning' };
  }
}

function moduleSourceLabel(source: string): string {
  switch (source) {
    case 'arch_element':
      return '架构元素';
    case 'build_manifest':
      return '构建清单';
    case 'first_level_dir':
      return '一级目录';
    case 'manual':
      return '手动维护';
    default:
      return source || '自动发现';
  }
}

export const ModuleGovernanceModal: React.FC<ModuleGovernanceModalProps> = ({
  open,
  target,
  onClose,
  onChanged,
}) => {
  const { showToast } = useToast();
  const confirm = useConfirm();

  const [loading, setLoading] = useState<boolean>(false);
  const [savingPolicy, setSavingPolicy] = useState<boolean>(false);
  const [refreshing, setRefreshing] = useState<boolean>(false);
  const [togglingId, setTogglingId] = useState<number | null>(null);
  const [migrating, setMigrating] = useState<boolean>(false);

  const [strategy, setStrategy] = useState<ModuleDiscoveryStrategy>('merged');
  const [minFiles, setMinFiles] = useState<number>(3);
  const [excludeGlobsText, setExcludeGlobsText] = useState<string>('');
  const [modules, setModules] = useState<RepoModuleVO[]>([]);

  const [fromSubdir, setFromSubdir] = useState<string>('');
  const [toSubdir, setToSubdir] = useState<string>('');

  const loadModules = useCallback(async () => {
    if (!target) return;
    setLoading(true);
    try {
      const res = await listRepoModules(target.repoId);
      setStrategy(res.policy.strategy);
      setMinFiles(res.policy.min_effective_files);
      setExcludeGlobsText((res.policy.exclude_globs || []).join(', '));
      setModules(Array.isArray(res.modules) ? res.modules : []);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载仓库模块索引失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [target, showToast]);

  useEffect(() => {
    if (!open || !target) return;
    setFromSubdir('');
    setToSubdir('');
    void loadModules();
  }, [open, target, loadModules]);

  const handleSavePolicy = async () => {
    if (!target) return;
    const globs = excludeGlobsText
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
    setSavingPolicy(true);
    try {
      const updated = await updateRepoModulePolicy(target.repoId, {
        strategy,
        min_effective_files: Math.max(1, minFiles),
        exclude_globs: globs,
      });
      setStrategy(updated.strategy);
      setMinFiles(updated.min_effective_files);
      setExcludeGlobsText((updated.exclude_globs || []).join(', '));
      showToast('模块发现策略已更新，点击「重新扫描模块」可立即生效', 'success');
      onChanged?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '保存模块发现策略失败').message, 'error');
    } finally {
      setSavingPolicy(false);
    }
  };

  const handleRefreshModules = async () => {
    if (!target) return;
    setRefreshing(true);
    try {
      const res = await refreshRepoModules(target.repoId);
      setModules(Array.isArray(res.modules) ? res.modules : []);
      showToast(
        `已完成模块发现扫描（${shortSha(res.commit_sha)}），共发现 ${res.active_count} 个活跃子模块`,
        'success',
      );
      onChanged?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '刷新仓库模块索引失败').message, 'error');
    } finally {
      setRefreshing(false);
    }
  };

  const handleToggleStatus = async (mod: RepoModuleVO) => {
    if (!target) return;
    const nextStatus: 'active' | 'ignored' = mod.status === 'ignored' ? 'active' : 'ignored';
    setTogglingId(mod.id);
    try {
      const updated = await updateRepoModuleStatus(target.repoId, mod.id, { status: nextStatus });
      setModules((prev) => prev.map((m) => (m.id === mod.id ? updated : m)));
      showToast(
        nextStatus === 'ignored'
          ? `已忽略子模块 ${mod.subdirectory}`
          : `已恢复子模块 ${mod.subdirectory} 为活跃状态`,
        'success',
      );
      onChanged?.();
    } catch (err: unknown) {
      showToast(formatApiError(err, '切换模块状态失败').message, 'error');
    } finally {
      setTogglingId(null);
    }
  };

  const handleMigrate = async () => {
    if (!target) return;
    const src = fromSubdir.trim();
    const dst = toSubdir.trim();
    if (!src || !dst) {
      showToast('请同时填写源子目录与目标子目录路径', 'warning');
      return;
    }
    if (src === dst) {
      showToast('源子目录与目标子目录不能相同', 'warning');
      return;
    }
    const ok = await confirm({
      title: '确认迁移子目录文档',
      content: `即将把子目录「${src}」下的所有设计文档、修订历史与已发布文件原子迁移至「${dst}」，并自动清理旧发布路径与过时漂移记录，是否继续？`,
      confirmText: '确认迁移',
      cancelText: '取消',
      type: 'warning',
    });
    if (!ok) return;

    setMigrating(true);
    try {
      const res = await migrateRepoModule(target.repoId, {
        from_subdir: src,
        to_subdir: dst,
      });
      showToast(
        `迁移成功：共迁移 ${res.migrated_documents} 篇文档（重发布 ${res.republished_documents} 篇）`,
        'success',
      );
      setFromSubdir('');
      setToSubdir('');
      await loadModules();
      onChanged?.();
    } catch (err: unknown) {
      const formatted = formatApiError(err, '迁移子目录文档失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setMigrating(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={target ? `模块发现与子目录治理 · ${target.repoName}` : '模块发现与子目录治理'}
      subtitle={target ? `分支 ${target.branch} · 策略配置、自动模块扫描与重命名文档迁移` : undefined}
      width="lg"
      destroyOnClose
      footer={
        <div className="code-archdoc-modal-footer">
          <div className="code-archdoc-modal-footer-right">
            <button type="button" className="btn btn-secondary" onClick={onClose}>
              关闭
            </button>
          </div>
        </div>
      }
    >
      <div className="code-archdoc-module-modal">
        {/* 1. 模块发现策略表单 */}
        <section className="code-archdoc-module-section">
          <div className="code-archdoc-module-section-head">
            <div className="code-archdoc-module-section-title">
              <FolderTree size={15} />
              <span>模块发现策略</span>
            </div>
            {target?.canWrite && (
              <div className="code-archdoc-module-section-actions">
                <button
                  type="button"
                  className="btn btn-secondary btn-small"
                  disabled={savingPolicy || loading}
                  onClick={() => void handleSavePolicy()}
                >
                  {savingPolicy ? <Loader2 size={13} className="code-archdoc-spin" /> : <Save size={13} />}
                  <span>保存策略</span>
                </button>
                <button
                  type="button"
                  className="btn btn-primary btn-small"
                  disabled={refreshing || loading}
                  onClick={() => void handleRefreshModules()}
                >
                  <RefreshCw size={13} className={refreshing ? 'code-archdoc-spin' : ''} />
                  <span>{refreshing ? '扫描中...' : '重新扫描模块'}</span>
                </button>
              </div>
            )}
          </div>

          <div className="code-archdoc-module-policy-grid">
            <label className="code-archdoc-field">
              <span className="code-archdoc-field-label">发现策略</span>
              <select
                className="code-select"
                value={strategy}
                disabled={!target?.canWrite}
                onChange={(e) => setStrategy(e.target.value as ModuleDiscoveryStrategy)}
              >
                {STRATEGY_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>
            </label>

            <label className="code-archdoc-field">
              <span className="code-archdoc-field-label">最小有效源码文件数</span>
              <input
                type="number"
                min={1}
                max={1000}
                className="code-input"
                value={minFiles}
                disabled={!target?.canWrite}
                onChange={(e) => setMinFiles(Math.max(1, Number.parseInt(e.target.value || '1', 10) || 1))}
              />
            </label>

            <label className="code-archdoc-field code-archdoc-field--wide">
              <span className="code-archdoc-field-label">排除 Glob 规则（逗号分隔）</span>
              <input
                type="text"
                className="code-input"
                placeholder="例如: third_party/**, examples/**, test/**"
                value={excludeGlobsText}
                disabled={!target?.canWrite}
                onChange={(e) => setExcludeGlobsText(e.target.value)}
              />
            </label>
          </div>
        </section>

        {/* 2. 已发现模块索引清单 */}
        <section className="code-archdoc-module-section">
          <div className="code-archdoc-module-section-head">
            <div className="code-archdoc-module-section-title">
              <span>已发现模块清单 ({modules.length})</span>
            </div>
          </div>

          {loading ? (
            <div className="code-archdoc-loading">
              <Loader2 size={16} className="code-archdoc-spin" />
              <span>正在加载模块索引...</span>
            </div>
          ) : modules.length === 0 ? (
            <EmptyState
              compact
              title="尚未发现业务子模块"
              description="当前仓库可作为单仓整体生成，或点击上方「重新扫描模块」基于构建清单与目录结构自动发现子模块。"
            />
          ) : (
            <div className="code-archdoc-slot-table-wrap">
              <table className="code-table">
                <thead>
                  <tr>
                    <th>子目录路径</th>
                    <th>来源</th>
                    <th>状态</th>
                    <th>规模</th>
                    <th>最近发现提交</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {modules.map((mod) => {
                    const sm = moduleStatusMeta(mod.status);
                    return (
                      <tr key={mod.id}>
                        <td>
                          <div className="code-archdoc-module-path-cell">
                            <code className="code-archdoc-mono">{mod.subdirectory}</code>
                            {mod.arch_element_name && (
                              <span className="text-muted">{mod.arch_element_name}</span>
                            )}
                          </div>
                        </td>
                        <td>
                          <span className="code-badge code-badge--muted">
                            {moduleSourceLabel(mod.source)}
                          </span>
                        </td>
                        <td>
                          <StatusTag status={sm.tag} text={sm.text} size="sm" />
                        </td>
                        <td>
                          <span className="text-secondary">
                            {mod.effective_files} 文件 · {mod.total_loc.toLocaleString()} LOC
                          </span>
                        </td>
                        <td>
                          <code className="code-archdoc-mono">{shortSha(mod.last_seen_commit)}</code>
                        </td>
                        <td>
                          {target?.canWrite ? (
                            <div className="code-archdoc-coverage-actions">
                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                disabled={togglingId === mod.id}
                                onClick={() => void handleToggleStatus(mod)}
                              >
                                {mod.status === 'ignored' ? <Eye size={13} /> : <EyeOff size={13} />}
                                <span>{mod.status === 'ignored' ? '恢复' : '忽略'}</span>
                              </button>
                              <button
                                type="button"
                                className="btn btn-text btn-small"
                                onClick={() => setFromSubdir(mod.subdirectory)}
                                title="填入下方迁移源路径"
                              >
                                <ArrowRightLeft size={13} />
                                <span>迁移</span>
                              </button>
                            </div>
                          ) : (
                            <span className="text-muted">-</span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>

        {/* 3. 子目录重命名无损文档迁移 */}
        {target?.canWrite && (
          <section className="code-archdoc-module-section">
            <div className="code-archdoc-module-section-head">
              <div className="code-archdoc-module-section-title">
                <ArrowRightLeft size={15} />
                <span>子目录重命名文档迁移</span>
              </div>
            </div>
            <div className="code-archdoc-module-migrate-row">
              <input
                type="text"
                className="code-input"
                placeholder="源子目录，如 services/legacy-order"
                value={fromSubdir}
                onChange={(e) => setFromSubdir(e.target.value)}
              />
              <span className="text-muted">→</span>
              <input
                type="text"
                className="code-input"
                placeholder="目标子目录，如 services/order"
                value={toSubdir}
                onChange={(e) => setToSubdir(e.target.value)}
              />
              <button
                type="button"
                className="btn btn-secondary btn-small"
                disabled={migrating || !fromSubdir.trim() || !toSubdir.trim()}
                onClick={() => void handleMigrate()}
              >
                {migrating ? <Loader2 size={13} className="code-archdoc-spin" /> : <ArrowRightLeft size={13} />}
                <span>{migrating ? '迁移中...' : '执行迁移'}</span>
              </button>
            </div>
          </section>
        )}
      </div>
    </Modal>
  );
};

export default ModuleGovernanceModal;
