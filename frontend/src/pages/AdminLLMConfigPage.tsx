import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Cpu,
  Eye,
  EyeOff,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Save,
  Star,
  Trash2,
  Zap,
} from 'lucide-react';
import { EmptyState, Modal, StatusTag, useConfirm, useToast, type StatusType } from '@code/common';
import {
  createLLMConfig,
  deleteLLMConfig,
  listLLMConfigs,
  probeAdhocLLMConfig,
  probeLLMConfig,
  setDefaultLLMConfig,
  updateLLMConfig,
} from '../api/admin';
import { useModuleUser } from '../context/ModuleUserContext';
import type { LLMConfigVO, LLMProbeResultVO } from '../types';
import { formatApiError } from '../utils/errorMessages';
import { MAX_PG_INT, validateHttpBaseUrl, validateRequiredInt } from '../utils/numberInput';
import { formatDateTime } from '../utils/urlQuery';
import './AdminLLMConfigPage.css';

interface LLMFormState {
  name: string;
  baseUrl: string;
  modelName: string;
  apiKey: string;
  clearApiKey: boolean;
  timeoutSeconds: string;
  maxRetries: string;
  maxTokensPerTask: string;
  maxFilesPerTask: string;
  isActive: boolean;
  isDefault: boolean;
}

const EMPTY_FORM: LLMFormState = {
  name: '',
  baseUrl: '',
  modelName: '',
  apiKey: '',
  clearApiKey: false,
  timeoutSeconds: '180',
  maxRetries: '3',
  maxTokensPerTask: '200000',
  maxFilesPerTask: '2000',
  isActive: true,
  isDefault: false,
};

function probeStatusMeta(status: LLMConfigVO['last_probe_status']): {
  tag: StatusType;
  label: string;
} {
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
  const confirm = useConfirm();
  const { showToast } = useToast();

  const [loading, setLoading] = useState<boolean>(true);
  const [configs, setConfigs] = useState<LLMConfigVO[]>([]);
  const [settingDefaultId, setSettingDefaultId] = useState<number | null>(null);
  const [probingId, setProbingId] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);

  // 新增 / 编辑弹窗状态
  const [modalOpen, setModalOpen] = useState<boolean>(false);
  const [editingConfig, setEditingConfig] = useState<LLMConfigVO | null>(null);
  const [form, setForm] = useState<LLMFormState>(EMPTY_FORM);
  const [showKey, setShowKey] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [modalProbing, setModalProbing] = useState<boolean>(false);
  const [modalProbeResult, setModalProbeResult] = useState<LLMProbeResultVO | null>(null);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const list = await listLLMConfigs();
      setConfigs(list);
    } catch (err: unknown) {
      showToast(formatApiError(err, '加载 LLM 模型池配置失败').message, 'error');
    } finally {
      setLoading(false);
    }
  }, [showToast]);

  useEffect(() => {
    if (!user?.is_module_admin) return;
    void loadData();
  }, [user?.is_module_admin, loadData]);

  const hasActiveDefault = useMemo(
    () => configs.some((cfg) => cfg.is_default && cfg.is_active),
    [configs],
  );

  const openCreateModal = () => {
    setEditingConfig(null);
    setForm({
      ...EMPTY_FORM,
      isDefault: configs.length === 0,
    });
    setShowKey(false);
    setModalProbeResult(null);
    setModalOpen(true);
  };

  const openEditModal = (cfg: LLMConfigVO) => {
    setEditingConfig(cfg);
    setForm({
      name: cfg.name,
      baseUrl: cfg.base_url,
      modelName: cfg.model_name,
      apiKey: '',
      clearApiKey: false,
      timeoutSeconds: String(cfg.timeout_seconds),
      maxRetries: String(cfg.max_retries),
      maxTokensPerTask: String(cfg.max_tokens_per_task),
      maxFilesPerTask: String(cfg.max_files_per_task),
      isActive: cfg.is_active,
      isDefault: cfg.is_default,
    });
    setShowKey(false);
    setModalProbeResult(null);
    setModalOpen(true);
  };

  const closeModal = () => {
    if (saving || modalProbing) return;
    setModalOpen(false);
  };

  const handleSetDefault = async (cfg: LLMConfigVO) => {
    if (cfg.is_default || !cfg.is_active) return;
    setSettingDefaultId(cfg.id);
    try {
      const updated = await setDefaultLLMConfig(cfg.id);
      showToast(`已手动将 "${updated.name}" 设为系统默认模型`, 'success');
      await loadData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '切换默认模型失败').message, 'error');
    } finally {
      setSettingDefaultId(null);
    }
  };

  const handleCardProbe = async (cfg: LLMConfigVO) => {
    setProbingId(cfg.id);
    try {
      const res = await probeLLMConfig(cfg.id);
      if (res.reachable) {
        showToast(`模型 "${cfg.name}" 探活成功（延迟 ${res.latency_ms} ms）`, 'success');
      } else {
        showToast(
          `模型 "${cfg.name}" 探活失败：${res.error_message || '目标不可达'}`,
          'error',
        );
      }
      await loadData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '执行连通性探活失败').message, 'error');
    } finally {
      setProbingId(null);
    }
  };

  const handleDelete = async (cfg: LLMConfigVO) => {
    if (cfg.is_default && configs.length > 1) {
      showToast('默认模型不可直接删除，请先将其他模型设为默认', 'error');
      return;
    }

    const ok = await confirm({
      title: `删除模型配置：${cfg.name}`,
      content:
        '确认从内网模型池移除该配置？若仍有文档类型显式指定此模型，删除将被服务端拦截并提示。',
      confirmText: '确认删除',
      type: 'danger',
    });
    if (!ok) return;

    setDeletingId(cfg.id);
    try {
      await deleteLLMConfig(cfg.id);
      showToast(`模型 "${cfg.name}" 已删除`, 'success');
      await loadData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '删除模型配置失败').message, 'error');
    } finally {
      setDeletingId(null);
    }
  };

  const handleModalProbe = async () => {
    const trimmedURL = form.baseUrl.trim();
    const trimmedModel = form.modelName.trim();
    if (!trimmedURL || !trimmedModel) {
      showToast('请先填写 Base URL 与 Model Name 后再测试连通性', 'error');
      return;
    }

    setModalProbing(true);
    setModalProbeResult(null);
    try {
      const res =
        editingConfig && !form.clearApiKey
          ? await probeLLMConfig(editingConfig.id, {
              base_url: trimmedURL,
              model_name: trimmedModel,
              ...(form.apiKey.trim() ? { api_key: form.apiKey.trim() } : {}),
            })
          : await probeAdhocLLMConfig({
              base_url: trimmedURL,
              model_name: trimmedModel,
              api_key: form.apiKey.trim(),
            });

      setModalProbeResult(res);
      if (res.reachable) {
        showToast(`连通性验证通过（延迟 ${res.latency_ms} ms）`, 'success');
      } else {
        showToast(`连通性验证失败：${res.error_message || '目标不可达'}`, 'error');
      }
    } catch (err: unknown) {
      showToast(formatApiError(err, '执行连通性测试失败').message, 'error');
    } finally {
      setModalProbing(false);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmedName = form.name.trim();
    const trimmedURL = form.baseUrl.trim();
    const trimmedModel = form.modelName.trim();

    if (!trimmedName) {
      showToast('模型名称标识不能为空', 'error');
      return;
    }
    const baseUrlError = validateHttpBaseUrl(form.baseUrl, 'OpenAI Compatible Base URL');
    if (baseUrlError) {
      showToast(baseUrlError, 'error');
      return;
    }
    if (!trimmedModel) {
      showToast('Model Name 不能为空', 'error');
      return;
    }

    const timeoutRes = validateRequiredInt(form.timeoutSeconds, {
      label: '单次调用超时（秒）',
      min: 5,
      max: 900,
    });
    if (!timeoutRes.ok) {
      showToast(timeoutRes.error, 'error');
      return;
    }
    const retriesRes = validateRequiredInt(form.maxRetries, {
      label: '失败最大重试次数',
      min: 0,
      max: 10,
    });
    if (!retriesRes.ok) {
      showToast(retriesRes.error, 'error');
      return;
    }
    const maxTokensRes = validateRequiredInt(form.maxTokensPerTask, {
      label: '单任务 Token 熔断上限',
      min: 1000,
      max: MAX_PG_INT,
    });
    if (!maxTokensRes.ok) {
      showToast(maxTokensRes.error, 'error');
      return;
    }
    const maxFilesRes = validateRequiredInt(form.maxFilesPerTask, {
      label: '单任务有效源码文件数上限',
      min: 1,
      max: MAX_PG_INT,
    });
    if (!maxFilesRes.ok) {
      showToast(maxFilesRes.error, 'error');
      return;
    }

    setSaving(true);
    try {
      const payload = {
        name: trimmedName,
        base_url: trimmedURL,
        model_name: trimmedModel,
        api_key: form.clearApiKey ? '' : form.apiKey.trim(),
        clear_api_key: editingConfig ? form.clearApiKey : false,
        timeout_seconds: timeoutRes.value,
        max_retries: retriesRes.value,
        max_tokens_per_task: maxTokensRes.value,
        max_files_per_task: maxFilesRes.value,
        is_active: form.isActive,
        is_default: editingConfig?.is_default ? true : form.isDefault,
      };

      if (editingConfig) {
        const updated = await updateLLMConfig(editingConfig.id, payload);
        showToast(`模型 "${updated.name}" 配置已更新`, 'success');
      } else {
        const created = await createLLMConfig(payload);
        showToast(`已新增模型 "${created.name}"`, 'success');
      }
      setModalOpen(false);
      await loadData();
    } catch (err: unknown) {
      showToast(formatApiError(err, '保存模型配置失败').message, 'error');
    } finally {
      setSaving(false);
    }
  };

  if (!user?.is_module_admin) {
    return (
      <div className="code-archdoc-workspace">
        <EmptyState
          type="permission"
          title="需要模块管理员权限 (archdoc_admin)"
          description="内网 LLM 模型池配置与默认模型切换仅对 archdoc_admin 或 super_admin 管理员开放。"
        />
      </div>
    );
  }

  return (
    <div className="code-archdoc-workspace">
      <div className="code-archdoc-page-header">
        <div>
          <h1 className="code-archdoc-page-title">LLM 模型配置</h1>
          <p className="code-archdoc-page-subtitle">
            管理内网平等的 OpenAI Compatible 模型池（如 DeepSeek、GLM、Kimi 等），支持手动切换全局默认模型及单任务预算限制（不做静默自动降级）。
          </p>
        </div>

        <div className="code-archdoc-llm-head-actions">
          <button
            type="button"
            className="btn btn-secondary"
            disabled={loading}
            onClick={() => void loadData()}
          >
            <RefreshCw size={14} className={loading ? 'code-archdoc-spin' : ''} />
            <span>刷新</span>
          </button>
          <button type="button" className="btn btn-primary" onClick={openCreateModal}>
            <Plus size={15} />
            <span>新增模型</span>
          </button>
        </div>
      </div>

      {!loading && configs.length > 0 && !hasActiveDefault && (
        <div className="code-archdoc-alert code-archdoc-alert--warning">
          <AlertTriangle size={16} />
          <span>
            当前尚未设置处于启用状态的系统默认模型；未显式指定模型的文档类型将无法发起生成或精修请求。请点击某张已启用模型卡片上的「设为默认」。
          </span>
        </div>
      )}

      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在加载内网模型池配置...</span>
        </div>
      ) : configs.length === 0 ? (
        <div className="code-card code-archdoc-llm-empty-card">
          <EmptyState
            title="暂未配置内网 LLM 模型"
            description="添加首个 OpenAI Compatible 模型端点后，系统将自动将其设为默认模型以支撑文档生成与 Chat 局部精修。"
          />
          <button type="button" className="btn btn-primary" onClick={openCreateModal}>
            <Plus size={15} />
            <span>立即新增模型</span>
          </button>
        </div>
      ) : (
        <div className="code-archdoc-llm-grid">
          {configs.map((cfg) => {
            const probeMeta = probeStatusMeta(cfg.last_probe_status);
            const isProbing = probingId === cfg.id;
            const isSettingDefault = settingDefaultId === cfg.id;
            const isDeleting = deletingId === cfg.id;
            const cannotDeleteDefault = cfg.is_default && configs.length > 1;

            return (
              <div
                key={cfg.id}
                className={`code-card code-archdoc-llm-card ${
                  cfg.is_default ? 'code-archdoc-llm-card--default' : ''
                }`}
              >
                <div className="code-archdoc-llm-card-head">
                  <div className="code-archdoc-llm-card-title-group">
                    <Cpu size={17} />
                    <h2 className="code-archdoc-llm-card-name">{cfg.name}</h2>
                    {cfg.is_default && (
                      <span className="code-badge code-badge--primary">默认模型</span>
                    )}
                  </div>

                  <div className="code-archdoc-llm-card-statuses">
                    <StatusTag
                      status={cfg.is_active ? 'success' : 'neutral'}
                      text={cfg.is_active ? '已启用' : '已停用'}
                      size="sm"
                    />
                    <StatusTag
                      status={probeMeta.tag}
                      text={
                        cfg.last_probe_status === 'ok'
                          ? `${probeMeta.label} (${cfg.last_probe_latency_ms} ms)`
                          : probeMeta.label
                      }
                      size="sm"
                    />
                  </div>
                </div>

                <div className="code-archdoc-llm-kv">
                  <div className="code-archdoc-llm-kv-item code-archdoc-llm-kv-item--full">
                    <span className="code-archdoc-llm-kv-label">Base URL</span>
                    <span className="code-archdoc-llm-kv-val code-archdoc-mono">
                      {cfg.base_url}
                    </span>
                  </div>

                  <div className="code-archdoc-llm-kv-item">
                    <span className="code-archdoc-llm-kv-label">Model Name</span>
                    <span className="code-archdoc-llm-kv-val code-archdoc-mono">
                      {cfg.model_name}
                    </span>
                  </div>

                  <div className="code-archdoc-llm-kv-item">
                    <span className="code-archdoc-llm-kv-label">API Key</span>
                    <span className="code-archdoc-llm-kv-val code-archdoc-mono">
                      {cfg.has_api_key ? cfg.api_key_masked : '未配置（免密调用）'}
                    </span>
                  </div>

                  <div className="code-archdoc-llm-kv-item">
                    <span className="code-archdoc-llm-kv-label">超时 / 重试</span>
                    <span className="code-archdoc-llm-kv-val">
                      {cfg.timeout_seconds}s · 最多 {cfg.max_retries} 次
                    </span>
                  </div>

                  <div className="code-archdoc-llm-kv-item">
                    <span className="code-archdoc-llm-kv-label">单任务熔断预算</span>
                    <span className="code-archdoc-llm-kv-val">
                      {cfg.max_tokens_per_task.toLocaleString()} tok / {cfg.max_files_per_task} 文件
                    </span>
                  </div>

                  <div className="code-archdoc-llm-kv-item code-archdoc-llm-kv-item--full">
                    <span className="code-archdoc-llm-kv-label">上次探活时间</span>
                    <span className="code-archdoc-llm-kv-val">
                      {cfg.last_probed_at ? formatDateTime(cfg.last_probed_at) : '-'}
                    </span>
                  </div>
                </div>

                <div className="code-archdoc-llm-card-actions">
                  {!cfg.is_default && (
                    <button
                      type="button"
                      className="btn btn-secondary btn-small"
                      disabled={!cfg.is_active || isSettingDefault}
                      title={
                        !cfg.is_active
                          ? '请先编辑并启用该模型后，再设为默认模型'
                          : '手动将该模型切换为全局唯一默认模型'
                      }
                      onClick={() => void handleSetDefault(cfg)}
                    >
                      {isSettingDefault ? (
                        <Loader2 size={13} className="code-archdoc-spin" />
                      ) : (
                        <Star size={13} />
                      )}
                      <span>设为默认</span>
                    </button>
                  )}

                  <button
                    type="button"
                    className="btn btn-secondary btn-small"
                    disabled={isProbing}
                    onClick={() => void handleCardProbe(cfg)}
                  >
                    {isProbing ? (
                      <Loader2 size={13} className="code-archdoc-spin" />
                    ) : (
                      <Zap size={13} />
                    )}
                    <span>{isProbing ? '探活中...' : '测试连通性'}</span>
                  </button>

                  <button
                    type="button"
                    className="btn btn-secondary btn-small"
                    onClick={() => openEditModal(cfg)}
                  >
                    <Pencil size={13} />
                    <span>编辑</span>
                  </button>

                  <button
                    type="button"
                    className="btn btn-danger btn-small"
                    disabled={cannotDeleteDefault || isDeleting}
                    title={
                      cannotDeleteDefault
                        ? '默认模型不可直接删除，请先将其他模型设为默认'
                        : '删除该模型配置'
                    }
                    onClick={() => void handleDelete(cfg)}
                  >
                    {isDeleting ? (
                      <Loader2 size={13} className="code-archdoc-spin" />
                    ) : (
                      <Trash2 size={13} />
                    )}
                    <span>删除</span>
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <Modal
        open={modalOpen}
        onClose={closeModal}
        width="md"
        destroyOnClose
        title={editingConfig ? `编辑模型：${editingConfig.name}` : '新增内网模型配置'}
        subtitle="填写内网 OpenAI Compatible 端点信息；保存前可先测试连通性。"
        footer={
          <div className="code-archdoc-llm-modal-footer">
            <button
              type="button"
              className="btn btn-secondary"
              disabled={modalProbing || saving}
              onClick={() => void handleModalProbe()}
            >
              {modalProbing ? (
                <Loader2 size={14} className="code-archdoc-spin" />
              ) : (
                <Zap size={14} />
              )}
              <span>{modalProbing ? '探活中...' : '测试连通性'}</span>
            </button>

            <div className="code-archdoc-llm-modal-footer-right">
              <button
                type="button"
                className="btn btn-secondary"
                disabled={saving || modalProbing}
                onClick={closeModal}
              >
                取消
              </button>
              <button
                type="submit"
                form="archdoc-llm-upsert-form"
                className="btn btn-primary"
                disabled={saving || modalProbing}
              >
                {saving ? (
                  <Loader2 size={14} className="code-archdoc-spin" />
                ) : (
                  <Save size={14} />
                )}
                <span>{saving ? '保存中...' : '保存配置'}</span>
              </button>
            </div>
          </div>
        }
      >
        <form
          id="archdoc-llm-upsert-form"
          className="code-archdoc-llm-form"
          noValidate
          onSubmit={(e) => void handleSave(e)}
        >
          <div className="code-archdoc-llm-row">
            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-name">
                模型名称标识（唯一）
              </label>
              <input
                id="llm-form-name"
                type="text"
                className="code-archdoc-input"
                maxLength={80}
                placeholder="例如：DeepSeek-V3 / GLM-4 / Kimi"
                value={form.name}
                onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
              />
            </div>

            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-model-name">
                Model Name（请求参数值）
              </label>
              <input
                id="llm-form-model-name"
                type="text"
                className="code-archdoc-input code-archdoc-mono"
                maxLength={128}
                placeholder="例如：deepseek-chat / glm-4-plus"
                value={form.modelName}
                onChange={(e) => setForm((p) => ({ ...p, modelName: e.target.value }))}
              />
            </div>
          </div>

          <div className="code-archdoc-form-field">
            <label className="code-archdoc-field-label" htmlFor="llm-form-base-url">
              OpenAI Compatible Base URL
            </label>
            <input
              id="llm-form-base-url"
              type="url"
              className="code-archdoc-input code-archdoc-mono"
              placeholder="例如：http://192.168.56.10:8000/v1"
              value={form.baseUrl}
              onChange={(e) => setForm((p) => ({ ...p, baseUrl: e.target.value }))}
            />
            <span className="code-archdoc-field-hint">
              支持 vLLM、Ollama、OneAPI、LiteLLM 等标准 OpenAI `/chat/completions` 接口。
            </span>
          </div>

          <div className="code-archdoc-form-field">
            <label className="code-archdoc-field-label" htmlFor="llm-form-api-key">
              API Key（可选，接口返回值自动脱敏）
            </label>
            <div className="code-archdoc-llm-secret-wrap">
              <input
                id="llm-form-api-key"
                type={showKey ? 'text' : 'password'}
                className="code-archdoc-input code-archdoc-mono"
                disabled={form.clearApiKey}
                placeholder={
                  editingConfig?.has_api_key
                    ? `已配置 ${editingConfig.api_key_masked}（留空则保留原密钥）`
                    : '内网免鉴权模型可直接留空'
                }
                value={form.apiKey}
                onChange={(e) => setForm((p) => ({ ...p, apiKey: e.target.value }))}
                autoComplete="new-password"
              />
              <button
                type="button"
                className="btn btn-secondary btn-small"
                disabled={form.clearApiKey}
                onClick={() => setShowKey((v) => !v)}
                aria-label={showKey ? '隐藏 API Key' : '显示 API Key'}
              >
                {showKey ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
            {editingConfig?.has_api_key && (
              <label className="code-archdoc-llm-clear-key">
                <input
                  type="checkbox"
                  checked={form.clearApiKey}
                  onChange={(e) =>
                    setForm((p) => ({
                      ...p,
                      clearApiKey: e.target.checked,
                      apiKey: e.target.checked ? '' : p.apiKey,
                    }))
                  }
                />
                <span>清空已保存的 API Key（切换为免密调用）</span>
              </label>
            )}
          </div>

          <div className="code-archdoc-llm-row">
            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-timeout">
                单次调用超时（秒）
              </label>
              <input
                id="llm-form-timeout"
                type="number"
                inputMode="numeric"
                className="code-archdoc-input"
                min={5}
                max={900}
                step={1}
                value={form.timeoutSeconds}
                onChange={(e) =>
                  setForm((p) => ({
                    ...p,
                    timeoutSeconds: e.target.value,
                  }))
                }
              />
            </div>

            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-retries">
                失败最大重试次数
              </label>
              <input
                id="llm-form-retries"
                type="number"
                inputMode="numeric"
                className="code-archdoc-input"
                min={0}
                max={10}
                step={1}
                value={form.maxRetries}
                onChange={(e) =>
                  setForm((p) => ({
                    ...p,
                    maxRetries: e.target.value,
                  }))
                }
              />
            </div>
          </div>

          <div className="code-archdoc-llm-row">
            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-max-tokens">
                单任务 Token 熔断上限
              </label>
              <input
                id="llm-form-max-tokens"
                type="number"
                inputMode="numeric"
                className="code-archdoc-input"
                min={1000}
                step={1}
                value={form.maxTokensPerTask}
                onChange={(e) =>
                  setForm((p) => ({
                    ...p,
                    maxTokensPerTask: e.target.value,
                  }))
                }
              />
            </div>

            <div className="code-archdoc-form-field">
              <label className="code-archdoc-field-label" htmlFor="llm-form-max-files">
                单任务有效源码文件数上限
              </label>
              <input
                id="llm-form-max-files"
                type="number"
                inputMode="numeric"
                className="code-archdoc-input"
                min={1}
                step={1}
                value={form.maxFilesPerTask}
                onChange={(e) =>
                  setForm((p) => ({
                    ...p,
                    maxFilesPerTask: e.target.value,
                  }))
                }
              />
            </div>
          </div>

          <div className="code-archdoc-llm-toggle-grid">
            <label className="code-archdoc-llm-toggle-card">
              <div>
                <div className="code-archdoc-llm-toggle-title">启用该模型</div>
                <div className="code-archdoc-llm-toggle-desc">
                  {editingConfig?.is_default
                    ? '当前模型为系统默认模型，请先将其他模型设为默认后再停用'
                    : '停用后，指定该模型的文档类型将拒绝触发任务'}
                </div>
              </div>
              <input
                type="checkbox"
                checked={form.isActive}
                disabled={Boolean(editingConfig?.is_default)}
                onChange={(e) =>
                  setForm((p) => ({
                    ...p,
                    isActive: e.target.checked,
                    isDefault: e.target.checked ? p.isDefault : false,
                  }))
                }
              />
            </label>

            <label className="code-archdoc-llm-toggle-card">
              <div>
                <div className="code-archdoc-llm-toggle-title">设为系统默认模型</div>
                <div className="code-archdoc-llm-toggle-desc">
                  所有未显式指定模型的文档类型均使用系统默认模型
                </div>
              </div>
              <input
                type="checkbox"
                checked={form.isDefault}
                disabled={!form.isActive || Boolean(editingConfig?.is_default)}
                onChange={(e) => setForm((p) => ({ ...p, isDefault: e.target.checked }))}
              />
            </label>
          </div>

          {modalProbeResult && (
            <div
              className={`code-archdoc-alert ${
                modalProbeResult.reachable
                  ? 'code-archdoc-alert--success'
                  : 'code-archdoc-alert--error'
              }`}
            >
              {modalProbeResult.reachable ? (
                <CheckCircle2 size={16} />
              ) : (
                <AlertTriangle size={16} />
              )}
              <span>
                {modalProbeResult.reachable
                  ? `探活成功：返回模型 ${modalProbeResult.model_echo}，延迟 ${modalProbeResult.latency_ms} ms`
                  : `探活失败：${modalProbeResult.error_message || '目标端点不可达'}（延迟 ${modalProbeResult.latency_ms} ms）`}
              </span>
            </div>
          )}
        </form>
      </Modal>
    </div>
  );
};

export default AdminLLMConfigPage;
