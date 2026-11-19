import React, { useCallback, useEffect, useState } from 'react';
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  Cpu,
  Eye,
  EyeOff,
  Loader2,
  Save,
  ShieldCheck,
  Zap,
} from 'lucide-react';
import { EmptyState, StatusTag, useToast, type StatusType } from '@code/common';
import { listLLMConfigs, probeLLMConfig, updateLLMConfig } from '../api/admin';
import { useModuleUser } from '../context/ModuleUserContext';
import type { LLMConfigVO, LLMProbeResultVO, ModelTier } from '../types';
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
      const items = await listLLMConfigs();
      const byTier: Partial<Record<ModelTier, LLMConfigVO>> = {};
      for (const item of items) {
        byTier[item.tier] = item;
      }
      setConfigs(byTier);
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
    </div>
  );
};

export default AdminLLMConfigPage;
