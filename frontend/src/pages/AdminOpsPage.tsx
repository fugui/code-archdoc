import React, { useCallback, useEffect, useState } from 'react';
import {
  Activity,
  CheckCircle2,
  HardDrive,
  Loader2,
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { EmptyState, useConfirm, useToast } from '@code/common';
import { getAdminStorageStats, triggerAdminStorageReap } from '../api/admin';
import { useModuleUser } from '../context/ModuleUserContext';
import type { ReapResultVO, StorageStatsVO } from '../types';
import { formatApiError } from '../utils/errorMessages';
import './AdminOpsPage.css';

function calcUsagePercent(usedMB: number, maxMB: number): number {
  if (maxMB <= 0) return 0;
  return Math.min(100, Math.round((usedMB / maxMB) * 100));
}

function progressModifierClass(pct: number): string {
  if (pct >= 90) return 'code-archdoc-ops-quota-fill--danger';
  if (pct >= 75) return 'code-archdoc-ops-quota-fill--warn';
  return '';
}

export const AdminOpsPage: React.FC = () => {
  const { user } = useModuleUser();
  const confirm = useConfirm();
  const { showToast } = useToast();

  const [loading, setLoading] = useState<boolean>(true);
  const [reaping, setReaping] = useState<boolean>(false);
  const [stats, setStats] = useState<StorageStatsVO | null>(null);
  const [lastReapResult, setLastReapResult] = useState<ReapResultVO | null>(null);

  const loadStats = useCallback(async () => {
    setLoading(true);
    try {
      const res = await getAdminStorageStats();
      setStats(res);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载运行与存储运维状态失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    if (!user?.is_module_admin) return;
    void loadStats();
  }, [user?.is_module_admin, loadStats]);

  const handleManualReap = async () => {
    const ok = await confirm({
      title: '执行存储空间 LRU 回收',
      content:
        '按最近最少使用顺序回收冗余 Git 沙箱与 Code IR 压缩分片，过程中将自动跳过存在活跃 Task 租约的工作目录。是否继续？',
      confirmText: '立即回收',
      type: 'warning',
    });
    if (!ok) return;

    setReaping(true);
    try {
      const res = await triggerAdminStorageReap();
      setLastReapResult(res);
      const freedMB = (res.freed_bytes / (1024 * 1024)).toFixed(1);
      showToast(
        `已完成 LRU 配额回收：沙箱 ${res.reaped_sandboxes} 个、IR 分片 ${res.reaped_ir_facets} 个，释放 ${freedMB} MB`,
        'success',
      );
      await loadStats();
    } catch (err: unknown) {
      showToast(formatApiError(err, '执行存储空间 LRU 回收失败').message, 'error');
    } finally {
      setReaping(false);
    }
  };

  if (!user?.is_module_admin) {
    return (
      <div className="code-archdoc-workspace">
        <EmptyState
          type="permission"
          title="需要模块管理员权限 (archdoc_admin)"
          description="本地存储配额治理与双优先级调度态势仅对 archdoc_admin 或 super_admin 管理员开放。"
        />
      </div>
    );
  }

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">运行与存储运维</h1>
          <p className="code-archdoc-page-subtitle">
            统一监控本地 Git 只读沙箱、Code IR 压缩分片缓存、已发布文档目录占用与双优先级调度队列实时态势。
          </p>
        </div>

        <div className="code-archdoc-ops-head-actions">
          <button
            type="button"
            className="btn btn-secondary"
            disabled={loading || reaping}
            onClick={() => void loadStats()}
          >
            <RefreshCw size={14} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新状态</span>
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={loading || reaping}
            onClick={() => void handleManualReap()}
          >
            {reaping ? (
              <Loader2 size={14} className="code-archdoc-spin" />
            ) : (
              <Trash2 size={14} />
            )}
            <span>{reaping ? '回收中...' : '执行 LRU 回收'}</span>
          </button>
        </div>
      </div>

      {lastReapResult && (
        <div className="code-archdoc-alert code-archdoc-alert--success">
          <CheckCircle2 size={16} />
          <span>
            最近一次手动回收完成：清理沙箱 {lastReapResult.reaped_sandboxes} 个、IR 分片{' '}
            {lastReapResult.reaped_ir_facets} 个，共释放{' '}
            {(lastReapResult.freed_bytes / (1024 * 1024)).toFixed(1)} MB（跳过受保护活跃租约{' '}
            {lastReapResult.skipped_active_leases} 个）。
          </span>
        </div>
      )}

      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在采集运行态配额与调度态势...</span>
        </div>
      ) : !stats ? (
        <EmptyState
          title="暂未获取到运维态指标"
          description="请点击右上角「刷新状态」重试。"
        />
      ) : (
        <div className="code-archdoc-ops-grid">
          <div className="code-card code-archdoc-ops-panel">
            <div className="code-archdoc-ops-panel-head">
              <h2 className="code-archdoc-ops-panel-title">
                <HardDrive size={17} />
                <span>受控存储配额治理</span>
              </h2>
              <span className="code-badge code-badge--muted">LRU 水位 90% → 75%</span>
            </div>

            <div className="code-archdoc-ops-quota-list">
              {(() => {
                const sbPct = calcUsagePercent(stats.sandbox.used_mb, stats.sandbox.max_mb);
                const irPct = calcUsagePercent(stats.ir_cache.used_mb, stats.ir_cache.max_mb);
                return (
                  <>
                    <div className="code-archdoc-ops-quota-item">
                      <div className="code-archdoc-ops-quota-top">
                        <span className="code-archdoc-ops-quota-name">
                          Git 只读沙箱 (sandbox_dir)
                        </span>
                        <span className="code-archdoc-ops-quota-num">
                          {stats.sandbox.used_mb} MB / {stats.sandbox.max_mb} MB ({sbPct}%)
                        </span>
                      </div>
                      <div className="code-archdoc-ops-quota-track">
                        <div
                          className={`code-archdoc-ops-quota-fill ${progressModifierClass(sbPct)}`}
                          style={{ width: `${Math.max(sbPct, 2)}%` }}
                        />
                      </div>
                      <div className="code-archdoc-ops-quota-meta">
                        <span>本地缓存仓库：{stats.sandbox.repo_count} 个</span>
                        <span>活跃租约保护：{stats.sandbox.active_leases} 个</span>
                      </div>
                    </div>

                    <div className="code-archdoc-ops-quota-item">
                      <div className="code-archdoc-ops-quota-top">
                        <span className="code-archdoc-ops-quota-name">
                          Code IR 压缩分片缓存 (ir_cache)
                        </span>
                        <span className="code-archdoc-ops-quota-num">
                          {stats.ir_cache.used_mb} MB / {stats.ir_cache.max_mb} MB ({irPct}%)
                        </span>
                      </div>
                      <div className="code-archdoc-ops-quota-track">
                        <div
                          className={`code-archdoc-ops-quota-fill ${progressModifierClass(irPct)}`}
                          style={{ width: `${Math.max(irPct, 2)}%` }}
                        />
                      </div>
                      <div className="code-archdoc-ops-quota-meta">
                        <span>活跃分片：{stats.ir_cache.facet_count} 个</span>
                        <span>已 LRU 回收：{stats.ir_cache.evicted_count} 个</span>
                      </div>
                    </div>

                    <div className="code-archdoc-ops-quota-item">
                      <div className="code-archdoc-ops-quota-top">
                        <span className="code-archdoc-ops-quota-name">
                          已发布 Markdown 落盘 (publish_dir)
                        </span>
                        <span className="code-archdoc-ops-quota-num">
                          {stats.publish_dir.published_files} 篇 · {stats.publish_dir.used_mb} MB
                        </span>
                      </div>
                      <div className="code-archdoc-ops-quota-meta">
                        <span>仅持久化已发布定稿快照与目录索引（staging 同文件系统原子替换）</span>
                      </div>
                    </div>
                  </>
                );
              })()}
            </div>
          </div>

          <div className="code-card code-archdoc-ops-panel">
            <div className="code-archdoc-ops-panel-head">
              <h2 className="code-archdoc-ops-panel-title">
                <Activity size={17} />
                <span>双优先级调度队列与并发态势</span>
              </h2>
              <span className="code-badge code-badge--primary">4:1 WFQ 防饥饿</span>
            </div>

            <div className="code-archdoc-ops-kv-grid">
              <div className="code-archdoc-ops-kv-cell">
                <span className="code-archdoc-ops-kv-label">运行中 LLM 并发调用</span>
                <span className="code-archdoc-ops-kv-val code-archdoc-mono">
                  {stats.scheduler.running_llm_calls} / {stats.scheduler.max_concurrency}
                </span>
                <span className="code-archdoc-ops-kv-hint">受全局并发信号量严格限流</span>
              </div>

              <div className="code-archdoc-ops-kv-cell">
                <span className="code-archdoc-ops-kv-label">活跃沙箱租约</span>
                <span className="code-archdoc-ops-kv-val code-archdoc-mono">
                  {stats.sandbox.active_leases}
                </span>
                <span className="code-archdoc-ops-kv-hint">租约期间免受 LRU 误删</span>
              </div>

              <div className="code-archdoc-ops-kv-cell">
                <span className="code-archdoc-ops-kv-label">高优交互队列 (HighQ)</span>
                <span className="code-archdoc-ops-kv-val code-archdoc-mono">
                  {stats.scheduler.interactive_queue_len} 排队中
                </span>
                <span className="code-archdoc-ops-kv-hint">承接前端手动生成与 Chat 精修</span>
              </div>

              <div className="code-archdoc-ops-kv-cell">
                <span className="code-archdoc-ops-kv-label">低优批量队列 (LowQ)</span>
                <span className="code-archdoc-ops-kv-val code-archdoc-mono">
                  {stats.scheduler.batch_queue_len} 排队中
                </span>
                <span className="code-archdoc-ops-kv-hint">承接后台轮询漂移保鲜任务</span>
              </div>
            </div>

            <div className="code-archdoc-ops-note">
              调度器按 <strong>HighQ : LowQ = 4 : 1</strong> 加权公平调度，确保后台仓库轮询保鲜任务不会阻塞开发者的交互式文档生成与局部精修；LRU 回收默认在容量达到配额 90% 时自动触发并回落至 75%。
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default AdminOpsPage;
