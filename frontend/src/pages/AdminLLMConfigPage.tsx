import React, { useCallback, useEffect, useState } from 'react';
import {
  Activity,
  AlertTriangle,
  BarChart3,
  CheckCircle2,
  Cpu,
  Eye,
  EyeOff,
  HardDrive,
  Loader2,
  Save,
  ShieldCheck,
  Trash2,
  Zap,
} from 'lucide-react';
import { EmptyState, StatusTag, useToast, type StatusType } from '@code/common';
import {
  getAdminStorageStats,
  listLLMConfigs,
  probeLLMConfig,
  triggerAdminStorageReap,
  updateLLMConfig,
} from '../api/admin';
import { useModuleUser } from '../context/ModuleUserContext';
import type { LLMConfigVO, LLMProbeResultVO, ModelTier, StorageStatsVO } from '../types';
import { formatApiError } from '../utils/errorMessages';
import { formatDateTime } from '../utils/urlQuery';
import './AdminLLMConfigPage.css';

interface LLMFormState {
  baseUrl: string;
  modelName: string;
  apiKey: string;
  timeoutSeconds: number;
  maxRetries: number;
  maxTokensPerTask: number;
  maxFilesPerTask: number;
  isActive: boolean;
}

const DEFAULT_FORM: LLMFormState = {
  baseUrl: '',
  modelName: '',
  apiKey: '',
  timeoutSeconds: 180,
  maxRetries: 3,
  maxTokensPerTask: 200000,
  maxFilesPerTask: 2000,
  isActive: true,
};

const TIER_META: Record<ModelTier, { title: string; subtitle: string }> = {
  fast: {
    title: 'Fast 档位（轻量高速模型）',
    subtitle: '用于文件摘要、快速目录归纳与低复杂度文档生成场景',
  },
  flagship: {
    title: 'Flagship 档位（旗舰推理模型）',
    subtitle: '用于系统架构、核心链路、数据契约等高复杂度架构设计文档反推',
  },
};

function probeStatusMeta(status: LLMConfigVO['last_probe_status']): { tag: StatusType; label: string } {
  switch (status) {
    case 'ok':
      return { tag: 'success', label: '连通正常' };
    case 'failed':
      return { tag: 'failed', label: '探活失败' };
    default:
      return { tag: 'neutral', label: '尚未探活' };
  }
}

export const AdminLLMConfigPage: React.FC = () => {
  const { user } = useModuleUser();
  const { showToast } = useToast();

  const [configs, setConfigs] = useState<Partial<Record<ModelTier, LLMConfigVO>>>({});
  const [activeTier, setActiveTier] = useState<ModelTier>('flagship');
  const [form, setForm] = useState<LLMFormState>(DEFAULT_FORM);
  const [showApiKey, setShowApiKey] = useState<boolean>(false);

  const [loading, setLoading] = useState<boolean>(true);
  const [saving, setSaving] = useState<boolean>(false);
  const [probing, setProbing] = useState<boolean>(false);
  const [probeResult, setProbeResult] = useState<LLMProbeResultVO | null>(null);
  const [storageStats, setStorageStats] = useState<StorageStatsVO | null>(null);
  const [reaping, setReaping] = useState<boolean>(false);

  const syncFormFromConfig = useCallback((cfg: LLMConfigVO | undefined) => {
    if (!cfg) {
      setForm(DEFAULT_FORM);
      return;
    }
    setForm({
      baseUrl: cfg.base_url || '',
      modelName: cfg.model_name || '',
      apiKey: '',
      timeoutSeconds: cfg.timeout_seconds || 180,
      maxRetries: cfg.max_retries ?? 3,
      maxTokensPerTask: cfg.max_tokens_per_task || 200000,
      maxFilesPerTask: cfg.max_files_per_task || 2000,
      isActive: cfg.is_active,
    });
    setShowApiKey(false);
  }, []);

  const loadConfigs = useCallback(async () => {
    if (!user?.is_module_admin) {
      setLoading(false);
      return;
    }

    setLoading(true);
    try {
      const [items, stats] = await Promise.all([
        listLLMConfigs(),
        getAdminStorageStats().catch(() => null),
      ]);
      const byTier: Partial<Record<ModelTier, LLMConfigVO>> = {};
      for (const item of items) {
        byTier[item.tier] = item;
      }
      setConfigs(byTier);
      if (stats) {
        setStorageStats(stats);
      }
      syncFormFromConfig(byTier[activeTier]);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载 LLM 配置失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [user?.is_module_admin, activeTier, syncFormFromConfig, showToast]);

  useEffect(() => {
    void loadConfigs();
  }, [loadConfigs]);

  const handleSelectTier = (tier: ModelTier) => {
    setActiveTier(tier);
    setProbeResult(null);
    syncFormFromConfig(configs[tier]);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.baseUrl.trim() || !form.modelName.trim()) {
      showToast('请完整填写 OpenAI Compatible Base URL 与模型名称', 'warning');
      return;
    }

    setSaving(true);
    try {
      const updated = await updateLLMConfig(activeTier, {
        base_url: form.baseUrl.trim(),
        model_name: form.modelName.trim(),
        api_key: form.apiKey.trim(),
        timeout_seconds: form.timeoutSeconds,
        max_retries: form.maxRetries,
        max_tokens_per_task: form.maxTokensPerTask,
        max_files_per_task: form.maxFilesPerTask,
        is_active: form.isActive,
      });
      setConfigs((prev) => ({ ...prev, [activeTier]: updated }));
      syncFormFromConfig(updated);
      showToast(`已保存 ${activeTier} 档位模型配置（密钥已加密落库）`, 'success');
    } catch (err: unknown) {
      const formatted = formatApiError(err, '保存模型配置失败');
      showToast(`${formatted.title}：${formatted.message}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  const handleProbe = async () => {
    setProbing(true);
    try {
      const res = await probeLLMConfig(activeTier, {
        base_url: form.baseUrl.trim() || undefined,
        model_name: form.modelName.trim() || undefined,
        api_key: form.apiKey.trim() || undefined,
      });
      setProbeResult(res);
      if (res.reachable) {
        showToast(`模型连通性探活成功（延迟 ${res.latency_ms} ms）`, 'success');
      } else {
        showToast(`模型连通性探活失败：${res.error_message || '目标不可达'}`, 'error');
      }
      // 刷新最新落库的探活时间与状态
      const items = await listLLMConfigs();
      const byTier: Partial<Record<ModelTier, LLMConfigVO>> = {};
      for (const item of items) {
        byTier[item.tier] = item;
      }
      setConfigs(byTier);
    } catch (err: unknown) {
      showToast(formatApiError(err, '执行连通性探活失败').message, 'error');
    } finally {
      setProbing(false);
    }
  };

  const handleManualReap = async () => {
    setReaping(true);
    try {
      const res = await triggerAdminStorageReap();
      const freedMB = (res.freed_bytes / (1024 * 1024)).toFixed(1);
      showToast(
        `已完成 LRU 配额回收：回收沙箱 ${res.reaped_sandboxes} 个、IR 分片 ${res.reaped_ir_facets} 个，释放 ${freedMB} MB（保护活跃租约 ${res.skipped_active_leases} 个）`,
        'success',
      );
      const latestStats = await getAdminStorageStats().catch(() => null);
      if (latestStats) {
        setStorageStats(latestStats);
      }
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
          description="全局 LLM 分层模型配置与连通性探活仅对 archdoc_admin 或 super_admin 管理员开放。"
        />
      </div>
    );
  }

  const currentCfg = configs[activeTier];
  const probeMeta = probeStatusMeta(currentCfg?.last_probe_status ?? 'unknown');

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">分层 LLM 模型配置与探活台</h1>
          <p className="code-archdoc-page-subtitle">
            统一管理内网 OpenAI Compatible 兼容模型的 fast / flagship 档位路由、AES-256-GCM 加密凭据及单任务 Token/文件熔断预算上限。
          </p>
        </div>
      </div>

      {/* fast / flagship 切换卡片 */}
      <div className="code-archdoc-llm-tabs" role="tablist" aria-label="模型档位切换">
        {(['flagship', 'fast'] as ModelTier[]).map((tier) => {
          const cfg = configs[tier];
          const pm = probeStatusMeta(cfg?.last_probe_status ?? 'unknown');
          return (
            <button
              key={tier}
              type="button"
              role="tab"
              aria-selected={activeTier === tier}
              className={`code-archdoc-llm-tab ${
                activeTier === tier ? 'code-archdoc-llm-tab--active' : ''
              }`}
              onClick={() => handleSelectTier(tier)}
            >
              <div className="code-archdoc-llm-tab-meta">
                <span className="code-archdoc-llm-tab-title">{TIER_META[tier].title}</span>
                <span className="code-archdoc-llm-tab-desc">{TIER_META[tier].subtitle}</span>
                {cfg?.model_name && (
                  <code className="code-archdoc-mono">{cfg.model_name}</code>
                )}
              </div>
              <StatusTag status={pm.tag} text={pm.label} size="sm" />
            </button>
          );
        })}
      </div>

      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在加载模型配置...</span>
        </div>
      ) : (
        <div className="code-archdoc-llm-layout">
          {/* 左侧：配置表单 */}
          <div className="code-card code-archdoc-llm-panel">
            <h2 className="code-archdoc-llm-panel-title">
              <Cpu size={17} />
              <span>{activeTier === 'flagship' ? 'Flagship' : 'Fast'} 档位参数与预算熔断</span>
            </h2>

            <form className="code-archdoc-llm-form" onSubmit={(e) => void handleSave(e)}>
              <div className="code-archdoc-llm-field">
                <label className="code-archdoc-llm-label" htmlFor="llm-base-url">
                  OpenAI Compatible Base URL
                </label>
                <input
                  id="llm-base-url"
                  type="url"
                  className="code-input"
                  value={form.baseUrl}
                  onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
                  placeholder="例如 http://10.0.10.20:8000/v1"
                  required
                />
                <span className="code-archdoc-llm-hint">
                  支持 vLLM、Ollama、OneAPI、LiteLLM 等标准 OpenAI `/chat/completions` 接口。
                </span>
              </div>

              <div className="code-archdoc-llm-row">
                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-model-name">
                    模型名称 (Model Name)
                  </label>
                  <input
                    id="llm-model-name"
                    type="text"
                    className="code-input"
                    value={form.modelName}
                    onChange={(e) => setForm((prev) => ({ ...prev, modelName: e.target.value }))}
                    placeholder="例如 deepseek-v3 / qwen2.5-coder-32b"
                    required
                  />
                </div>

                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-api-key">
                    API Key（AES-256-GCM 加密存储）
                  </label>
                  <div className="code-archdoc-llm-key-wrap">
                    <input
                      id="llm-api-key"
                      type={showApiKey ? 'text' : 'password'}
                      className="code-input code-archdoc-llm-key-input"
                      value={form.apiKey}
                      onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
                      placeholder={
                        currentCfg?.has_api_key
                          ? currentCfg.api_key_masked || 'sk-****（留空保留既有密钥）'
                          : '输入 API Key（内网免密网关可留空）'
                      }
                      autoComplete="new-password"
                    />
                    <button
                      type="button"
                      className="btn btn-secondary btn-small"
                      onClick={() => setShowApiKey((v) => !v)}
                      title={showApiKey ? '隐藏密钥' : '显示输入'}
                      aria-label={showApiKey ? '隐藏密钥' : '显示密钥'}
                    >
                      {showApiKey ? <EyeOff size={14} /> : <Eye size={14} />}
                    </button>
                  </div>
                  <span className="code-archdoc-llm-hint">
                    {currentCfg?.has_api_key
                      ? `已配置脱敏密钥 ${currentCfg.api_key_masked}；留空提交将保留既有密文不变。`
                      : '当前未保存 API Key；密钥保存后永不回传明文且不写入审计日志。'}
                  </span>
                </div>
              </div>

              <div className="code-archdoc-llm-row">
                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-timeout">
                    单次调用超时（秒）
                  </label>
                  <input
                    id="llm-timeout"
                    type="number"
                    min={10}
                    max={900}
                    className="code-input"
                    value={form.timeoutSeconds}
                    onChange={(e) =>
                      setForm((prev) => ({
                        ...prev,
                        timeoutSeconds: Number.parseInt(e.target.value, 10) || 60,
                      }))
                    }
                  />
                </div>

                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-retries">
                    失败最大重试次数
                  </label>
                  <input
                    id="llm-retries"
                    type="number"
                    min={0}
                    max={10}
                    className="code-input"
                    value={form.maxRetries}
                    onChange={(e) =>
                      setForm((prev) => ({
                        ...prev,
                        maxRetries: Math.max(0, Number.parseInt(e.target.value, 10) || 0),
                      }))
                    }
                  />
                </div>
              </div>

              <div className="code-archdoc-llm-row">
                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-max-tokens">
                    单任务 Token 熔断上限
                  </label>
                  <input
                    id="llm-max-tokens"
                    type="number"
                    min={10000}
                    max={5000000}
                    step={10000}
                    className="code-input"
                    value={form.maxTokensPerTask}
                    onChange={(e) =>
                      setForm((prev) => ({
                        ...prev,
                        maxTokensPerTask: Number.parseInt(e.target.value, 10) || 200000,
                      }))
                    }
                  />
                  <span className="code-archdoc-llm-hint">
                    L2 发起前预估超出该阈值将触发零请求预算熔断并输出目录拆分建议。
                  </span>
                </div>

                <div className="code-archdoc-llm-field">
                  <label className="code-archdoc-llm-label" htmlFor="llm-max-files">
                    单任务有效源码文件数上限
                  </label>
                  <input
                    id="llm-max-files"
                    type="number"
                    min={10}
                    max={50000}
                    step={50}
                    className="code-input"
                    value={form.maxFilesPerTask}
                    onChange={(e) =>
                      setForm((prev) => ({
                        ...prev,
                        maxFilesPerTask: Number.parseInt(e.target.value, 10) || 2000,
                      }))
                    }
                  />
                </div>
              </div>

              <div className="code-archdoc-llm-switch-row">
                <div>
                  <div className="code-archdoc-llm-label">启用当前档位</div>
                  <div className="code-archdoc-llm-hint">
                    关闭后关联该档位的 DocType 生成任务将被暂停调度
                  </div>
                </div>
                <label className="code-archdoc-radio-label">
                  <input
                    type="checkbox"
                    checked={form.isActive}
                    onChange={(e) => setForm((prev) => ({ ...prev, isActive: e.target.checked }))}
                  />
                  <span>{form.isActive ? '已启用' : '已停用'}</span>
                </label>
              </div>

              <div className="code-archdoc-llm-form-actions">
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={probing || !form.baseUrl.trim() || !form.modelName.trim()}
                  onClick={() => void handleProbe()}
                >
                  {probing ? (
                    <Loader2 size={15} className="code-archdoc-spin" />
                  ) : (
                    <Zap size={15} />
                  )}
                  <span>{probing ? '探活测试中...' : '测试连通性'}</span>
                </button>

                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? (
                    <Loader2 size={15} className="code-archdoc-spin" />
                  ) : (
                    <Save size={15} />
                  )}
                  <span>{saving ? '保存中...' : '保存配置'}</span>
                </button>
              </div>
            </form>
          </div>

          {/* 右侧：连通性探活结果与安全审计说明 */}
          <div className="code-card code-archdoc-llm-panel">
            <h2 className="code-archdoc-llm-panel-title">
              <Activity size={17} />
              <span>连通性探活状态</span>
            </h2>

            {probeResult && (
              <div
                className={`code-archdoc-alert ${
                  probeResult.reachable
                    ? 'code-archdoc-alert--success'
                    : 'code-archdoc-alert--danger'
                }`}
              >
                {probeResult.reachable ? (
                  <CheckCircle2 size={17} />
                ) : (
                  <AlertTriangle size={17} />
                )}
                <div className="code-archdoc-alert-body">
                  <div className="code-archdoc-alert-title">
                    {probeResult.reachable ? '本次探活连通正常' : '本次探活连接失败'}
                  </div>
                  <div>
                    {probeResult.reachable
                      ? `响应延迟 ${probeResult.latency_ms} ms · 模型回显 ${probeResult.model_echo || form.modelName}`
                      : probeResult.error_message || '无法连接目标推理接口，请检查网络、路径或凭据'}
                  </div>
                </div>
              </div>
            )}

            <div className="code-archdoc-probe-kv">
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">最近探活结果</span>
                <div className="code-archdoc-probe-val">
                  <StatusTag status={probeMeta.tag} text={probeMeta.label} size="sm" />
                </div>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">最近延迟</span>
                <span className="code-archdoc-probe-val">
                  {currentCfg && currentCfg.last_probe_latency_ms > 0
                    ? `${currentCfg.last_probe_latency_ms} ms`
                    : '-'}
                </span>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">上次探活时间</span>
                <span className="code-archdoc-probe-val">
                  {formatDateTime(currentCfg?.last_probed_at)}
                </span>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">密钥状态</span>
                <span className="code-archdoc-probe-val">
                  {currentCfg?.has_api_key ? currentCfg.api_key_masked : '未配置'}
                </span>
              </div>
            </div>

            <div className="code-archdoc-alert code-archdoc-alert--info">
              <ShieldCheck size={17} />
              <div className="code-archdoc-alert-body">
                <div className="code-archdoc-alert-title">凭据防泄漏与 P0 级审计红线</div>
                <div>
                  API Key 在服务端经 AES-256-GCM 加密后落库；列表接口、日志与平台审计 Diff
                  均仅记录脱敏摘要（如 <code className="code-archdoc-mono">sk-****</code>），绝不暴露明文或密文。
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* W2: 沙箱存储配额、双优先级调度队列与近 30 天 Token 成本卡片 (F-27) */}
      {!loading && storageStats && (
        <div className="code-archdoc-llm-gov-grid">
          <div className="code-card code-archdoc-llm-panel">
            <div className="code-archdoc-llm-panel-head">
              <h2 className="code-archdoc-llm-panel-title">
                <HardDrive size={17} />
                <span>受控存储配额与双优先级调度治理 (F-27)</span>
              </h2>
              <button
                type="button"
                className="btn btn-secondary btn-small"
                disabled={reaping}
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

            <div className="code-archdoc-llm-quota-list">
              {(() => {
                const sbPct =
                  storageStats.sandbox.max_mb > 0
                    ? Math.min(100, Math.round((storageStats.sandbox.used_mb / storageStats.sandbox.max_mb) * 100))
                    : 0;
                const irPct =
                  storageStats.ir_cache.max_mb > 0
                    ? Math.min(100, Math.round((storageStats.ir_cache.used_mb / storageStats.ir_cache.max_mb) * 100))
                    : 0;
                return (
                  <>
                    <div className="code-archdoc-llm-quota-item">
                      <div className="code-archdoc-llm-quota-top">
                        <span className="code-archdoc-llm-quota-name">Git 只读沙箱 (sandbox_dir)</span>
                        <span className="code-archdoc-llm-quota-num">
                          {storageStats.sandbox.used_mb} MB / {storageStats.sandbox.max_mb} MB ({sbPct}%)
                        </span>
                      </div>
                      <div className="code-archdoc-llm-quota-track">
                        <div
                          className={`code-archdoc-llm-quota-fill ${
                            sbPct >= 90
                              ? 'code-archdoc-llm-quota-fill--danger'
                              : sbPct >= 75
                                ? 'code-archdoc-llm-quota-fill--warn'
                                : ''
                          }`}
                          style={{ width: `${Math.max(sbPct, 2)}%` }}
                        />
                      </div>
                      <div className="code-archdoc-llm-quota-meta">
                        <span>本地缓存仓库：{storageStats.sandbox.repo_count} 个</span>
                        <span>活跃租约保护：{storageStats.sandbox.active_leases} 个</span>
                      </div>
                    </div>

                    <div className="code-archdoc-llm-quota-item">
                      <div className="code-archdoc-llm-quota-top">
                        <span className="code-archdoc-llm-quota-name">Code IR 压缩分片缓存 (ir_cache)</span>
                        <span className="code-archdoc-llm-quota-num">
                          {storageStats.ir_cache.used_mb} MB / {storageStats.ir_cache.max_mb} MB ({irPct}%)
                        </span>
                      </div>
                      <div className="code-archdoc-llm-quota-track">
                        <div
                          className={`code-archdoc-llm-quota-fill ${
                            irPct >= 90
                              ? 'code-archdoc-llm-quota-fill--danger'
                              : irPct >= 75
                                ? 'code-archdoc-llm-quota-fill--warn'
                                : ''
                          }`}
                          style={{ width: `${Math.max(irPct, 2)}%` }}
                        />
                      </div>
                      <div className="code-archdoc-llm-quota-meta">
                        <span>活跃分片：{storageStats.ir_cache.facet_count} 个</span>
                        <span>已 LRU 回收：{storageStats.ir_cache.evicted_count} 个</span>
                        <span>
                          已发布落盘 (publish_dir)：{storageStats.publish_dir.published_files} 篇 ({storageStats.publish_dir.used_mb} MB)
                        </span>
                      </div>
                    </div>
                  </>
                );
              })()}
            </div>

            <div className="code-archdoc-probe-kv">
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">高优交互队列 (HighQ)</span>
                <span className="code-archdoc-probe-val">
                  {storageStats.scheduler.interactive_queue_len} 排队中
                </span>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">低优批量队列 (LowQ)</span>
                <span className="code-archdoc-probe-val">
                  {storageStats.scheduler.batch_queue_len} 排队中（4:1 防饥饿）
                </span>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">运行中 LLM 并发调用</span>
                <span className="code-archdoc-probe-val">
                  {storageStats.scheduler.running_llm_calls} / {storageStats.scheduler.max_concurrency}
                </span>
              </div>
              <div className="code-archdoc-probe-cell">
                <span className="code-archdoc-probe-label">LRU 水位策略</span>
                <span className="code-archdoc-probe-val">90% 触发 → 75% 停止</span>
              </div>
            </div>
          </div>

          <div className="code-card code-archdoc-llm-panel">
            <h2 className="code-archdoc-llm-panel-title">
              <BarChart3 size={17} />
              <span>最近 30 天 Token 消耗与 KV Cache 成本卡片</span>
            </h2>

            {storageStats.usage_last_30d.length === 0 ? (
              <EmptyState
                title="最近 30 天暂无模型调用记录"
                description="触发文档生成、增量刷新或 Chat 精修后，调用级 Token 与 Prefix KV Cache 命中率将自动汇总于此。"
              />
            ) : (
              <div className="code-archdoc-llm-usage-table-wrap">
                <table className="code-archdoc-llm-usage-table">
                  <thead>
                    <tr>
                      <th>模型档位</th>
                      <th>文档类型 (DocType)</th>
                      <th>调用次数</th>
                      <th>总消耗 Tokens</th>
                      <th>KV Cache 命中 Tokens</th>
                    </tr>
                  </thead>
                  <tbody>
                    {storageStats.usage_last_30d.map((item, idx) => (
                      <tr key={`${item.model_tier}-${item.doc_type_slug || 'glossary'}-${idx}`}>
                        <td>
                          <StatusTag
                            status={item.model_tier === 'flagship' ? 'running' : 'neutral'}
                            text={item.model_tier}
                            size="sm"
                          />
                        </td>
                        <td>
                          <code className="code-archdoc-mono">
                            {item.doc_type_slug || 'glossary (共享术语表)'}
                          </code>
                        </td>
                        <td>{item.call_count} 次</td>
                        <td>{item.total_tokens.toLocaleString()}</td>
                        <td>{item.cached_prompt_tokens.toLocaleString()}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default AdminLLMConfigPage;
