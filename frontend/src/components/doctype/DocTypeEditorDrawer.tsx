import React, { useEffect, useMemo, useState } from 'react';
import {
  CheckCircle2,
  FileCode2,
  History,
  Layers,
  Loader2,
  Save,
  Settings2,
  XCircle,
} from 'lucide-react';
import { Drawer, useToast } from '@code/common';
import {
  createDocType,
  getDocTypeDetail,
  listDocTypeRevisions,
  updateDocType,
} from '../../api/admin';
import type {
  DocTypeDetailVO,
  DocTypeRevisionSummaryVO,
  FacetName,
  ModelTier,
  SlotDefinition,
} from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import { formatDateTime } from '../../utils/urlQuery';
import SlotsSchemaBuilder, { ALL_FACET_OPTIONS } from './SlotsSchemaBuilder';

export interface DocTypeEditorDrawerProps {
  open: boolean;
  /** null 表示新建模式，非空字符串表示编辑该 slug */
  editingSlug: string | null;
  onClose: () => void;
  onSaved: () => void;
}

type EditorMainTab = 'meta' | 'slots' | 'assets' | 'revisions';
type AssetSubTab = 'template' | 'generation_prompt' | 'refine_prompt';

const SLUG_REGEX = /^[a-z0-9][a-z0-9_-]{1,62}$/;
const SLOT_ID_REGEX = /^[a-z][a-z0-9_]{1,48}$/;

const DEFAULT_NEW_SLOTS: SlotDefinition[] = [
  {
    slot_id: 'overview_summary',
    title: '1. 核心概览与边界说明',
    ownership: 'auto',
    required: true,
    ir_facets: ['tree', 'deps'],
    token_budget: 8000,
    min_chars: 40,
    instruction_hint: '',
  },
];

const DEFAULT_TEMPLATE_MD =
  '## 1. 核心概览与边界说明\n<!-- slot:overview_summary -->\n';

const DEFAULT_GENERATION_PROMPT =
  '你是资深企业级软件架构师。请严格基于 L1 Code IR 事实撰写清晰、准确、零幻觉的技术设计文档。';

export const DocTypeEditorDrawer: React.FC<DocTypeEditorDrawerProps> = ({
  open,
  editingSlug,
  onClose,
  onSaved,
}) => {
  const { showToast } = useToast();
  const isCreate = editingSlug === null;

  const [loading, setLoading] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [mainTab, setMainTab] = useState<EditorMainTab>('meta');
  const [assetTab, setAssetTab] = useState<AssetSubTab>('template');

  // 表单状态
  const [slug, setSlug] = useState<string>('');
  const [displayName, setDisplayName] = useState<string>('');
  const [description, setDescription] = useState<string>('');
  const [modelTier, setModelTier] = useState<ModelTier>('flagship');
  const [timeoutSeconds, setTimeoutSeconds] = useState<number>(300);
  const [maxOutputTokens, setMaxOutputTokens] = useState<string>('');
  const [isActive, setIsActive] = useState<boolean>(true);
  const [irFacets, setIrFacets] = useState<FacetName[]>(['tree', 'deps', 'symbols']);
  const [slots, setSlots] = useState<SlotDefinition[]>(DEFAULT_NEW_SLOTS);
  const [templateMD, setTemplateMD] = useState<string>(DEFAULT_TEMPLATE_MD);
  const [genPromptMD, setGenPromptMD] = useState<string>(DEFAULT_GENERATION_PROMPT);
  const [refinePromptMD, setRefinePromptMD] = useState<string>('');

  // 编辑态原数据（保留未知 ExtraFields 与历史 Revision）
  const [loadedDetail, setLoadedDetail] = useState<DocTypeDetailVO | null>(null);
  const [revisions, setRevisions] = useState<DocTypeRevisionSummaryVO[]>([]);
  const [loadingRevs, setLoadingRevs] = useState<boolean>(false);

  const persistedSlotIds = useMemo(() => {
    if (!loadedDetail) return new Set<string>();
    return new Set(loadedDetail.slots.map((s) => s.slot_id));
  }, [loadedDetail]);

  // 实时校验 template.md 是否包含全部槽位占位标记
  const missingTemplateMarkers = useMemo(() => {
    return slots
      .filter((sl) => sl.slot_id && !templateMD.includes(`<!-- slot:${sl.slot_id} -->`))
      .map((sl) => sl.slot_id);
  }, [slots, templateMD]);

  useEffect(() => {
    if (!open) return;
    setMainTab('meta');
    setAssetTab('template');

    if (isCreate) {
      setLoadedDetail(null);
      setRevisions([]);
      setSlug('');
      setDisplayName('');
      setDescription('');
      setModelTier('flagship');
      setTimeoutSeconds(300);
      setMaxOutputTokens('');
      setIsActive(true);
      setIrFacets(['tree', 'deps', 'symbols']);
      setSlots(DEFAULT_NEW_SLOTS.map((s) => ({ ...s, ir_facets: [...s.ir_facets] })));
      setTemplateMD(DEFAULT_TEMPLATE_MD);
      setGenPromptMD(DEFAULT_GENERATION_PROMPT);
      setRefinePromptMD('');
      return;
    }

    let active = true;
    setLoading(true);
    void getDocTypeDetail(editingSlug)
      .then((detail) => {
        if (!active) return;
        setLoadedDetail(detail);
        setSlug(detail.slug);
        setDisplayName(detail.display_name);
        setDescription(detail.description || '');
        setModelTier(detail.model_tier);
        setTimeoutSeconds(detail.timeout_seconds || 300);
        const rawMaxOut = detail.meta?.max_output_tokens;
        setMaxOutputTokens(
          typeof rawMaxOut === 'number' && rawMaxOut > 0 ? String(rawMaxOut) : '',
        );
        setIsActive(detail.is_active);
        setIrFacets(Array.isArray(detail.ir_facets) ? [...detail.ir_facets] : ['tree']);
        setSlots(
          Array.isArray(detail.slots)
            ? detail.slots.map((s) => ({ ...s, ir_facets: [...(s.ir_facets || [])] }))
            : [],
        );
        setTemplateMD(detail.template_md || '');
        setGenPromptMD(detail.generation_prompt_md || '');
        setRefinePromptMD(detail.refine_prompt_md || '');
      })
      .catch((err: unknown) => {
        if (!active) return;
        showToast(formatApiError(err, '加载 DocType 详情失败').message, 'error');
        onClose();
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [open, editingSlug, isCreate, showToast, onClose]);

  useEffect(() => {
    if (!open || isCreate || mainTab !== 'revisions' || !editingSlug) return;
    let active = true;
    setLoadingRevs(true);
    void listDocTypeRevisions(editingSlug)
      .then((items) => {
        if (active) setRevisions(items);
      })
      .catch((err: unknown) => {
        if (active) showToast(formatApiError(err, '加载修订历史失败').message, 'error');
      })
      .finally(() => {
        if (active) setLoadingRevs(false);
      });
    return () => {
      active = false;
    };
  }, [open, isCreate, mainTab, editingSlug, showToast]);

  const handleToggleTopFacet = (facet: FacetName) => {
    setIrFacets((prev) =>
      prev.includes(facet) ? prev.filter((f) => f !== facet) : [...prev, facet],
    );
  };

  const handleSyncTemplateMarkers = () => {
    let next = templateMD;
    let appended = 0;
    for (const sl of slots) {
      if (!sl.slot_id) continue;
      const marker = `<!-- slot:${sl.slot_id} -->`;
      if (!next.includes(marker)) {
        const suffix = next.endsWith('\n') || next === '' ? '' : '\n';
        next += `${suffix}\n## ${sl.title || sl.slot_id}\n${marker}\n`;
        appended++;
      }
    }
    setTemplateMD(next);
    if (appended > 0) {
      showToast(`已自动补全 ${appended} 个槽位占位标记到 template.md`, 'success');
    } else {
      showToast('template.md 已包含全部槽位标记', 'info');
    }
  };

  const validateBeforeSave = (): string | null => {
    const trimmedSlug = slug.trim();
    if (isCreate && !SLUG_REGEX.test(trimmedSlug)) {
      return 'slug 格式非法：仅允许小写字母、数字、中划线和下划线，长度 2~63 位且以字母或数字开头';
    }
    if (!displayName.trim()) {
      return '展示名称 (display_name) 不能为空';
    }
    if (irFacets.length === 0) {
      return '请至少订阅 1 个顶层 Code IR 分片 (ir_facets)';
    }
    if (slots.length === 0) {
      return '至少需要定义 1 个章节槽位';
    }
    const seen = new Set<string>();
    for (const sl of slots) {
      if (!SLOT_ID_REGEX.test(sl.slot_id)) {
        return `槽位标识 "${sl.slot_id}" 非法：须匹配 ^[a-z][a-z0-9_]{1,48}$`;
      }
      if (seen.has(sl.slot_id)) {
        return `存在重复的槽位标识 "${sl.slot_id}"`;
      }
      seen.add(sl.slot_id);
      if (!sl.title.trim()) {
        return `槽位 "${sl.slot_id}" 缺少章节标题`;
      }
    }
    if (missingTemplateMarkers.length > 0) {
      return `template.md 缺少槽位标记：${missingTemplateMarkers.map((id) => `<!-- slot:${id} -->`).join('、')}`;
    }
    return null;
  };

  const handleSave = async () => {
    const errMsg = validateBeforeSave();
    if (errMsg) {
      showToast(errMsg, 'error');
      return;
    }

    const parsedMaxOut = maxOutputTokens.trim()
      ? Number.parseInt(maxOutputTokens.trim(), 10)
      : undefined;

    setSaving(true);
    try {
      if (isCreate) {
        await createDocType({
          slug: slug.trim(),
          display_name: displayName.trim(),
          description: description.trim(),
          is_active: isActive,
          model_tier: modelTier,
          timeout_seconds: timeoutSeconds,
          ...(parsedMaxOut && parsedMaxOut > 0 ? { max_output_tokens: parsedMaxOut } : {}),
          ir_facets: irFacets,
          slots,
          template_md: templateMD,
          generation_prompt_md: genPromptMD,
          refine_prompt_md: refinePromptMD,
        });
        showToast(`文档类型 "${slug.trim()}" 已创建并原子落盘`, 'success');
      } else {
        // 以原 meta 为基底合并更新，无损保留未知扩展字段 (ExtraFields)
        const baseMeta: Record<string, unknown> = {
          ...(loadedDetail?.meta ?? {}),
          name: editingSlug,
          display_name: displayName.trim(),
          description: description.trim(),
          is_active: isActive,
          model_tier: modelTier,
          timeout_seconds: timeoutSeconds,
          ir_facets: irFacets,
          slots,
        };
        if (parsedMaxOut && parsedMaxOut > 0) {
          baseMeta.max_output_tokens = parsedMaxOut;
        } else {
          delete baseMeta.max_output_tokens;
        }

        const updated = await updateDocType(editingSlug, {
          meta: baseMeta,
          template_md: templateMD,
          generation_prompt_md: genPromptMD,
          refine_prompt_md: refinePromptMD,
        });
        showToast(
          updated.revision_created
            ? `已保存并生成新修订 Rev v${updated.latest_revision_no}，后台已触发 Spec Drift 重算`
            : '内容无实质变更，保持当前修订版本',
          'success',
        );
      }
      onSaved();
      onClose();
    } catch (err: unknown) {
      showToast(formatApiError(err, '保存文档类型失败').message, 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width="xl"
      destroyOnClose
      title={isCreate ? '新建文档类型 (DocType)' : `编辑文档类型：${editingSlug}`}
      subtitle={
        isCreate
          ? '目录即唯一事实源：提交后原子落盘 meta.json / template.md / generation_prompt.md / refine_prompt.md'
          : `当前修订 Rev v${loadedDetail?.latest_revision_no ?? 1} · 未知扩展字段自动无损保留`
      }
      footer={
        <div className="code-archdoc-drawer-footer">
          <div className="code-archdoc-drawer-footer-status">
            {missingTemplateMarkers.length === 0 ? (
              <span className="code-badge code-badge--success">
                <CheckCircle2 size={12} />
                <span>模板槽位校验通过 ({slots.length} 个槽位)</span>
              </span>
            ) : (
              <span className="code-badge code-badge--danger">
                <XCircle size={12} />
                <span>template.md 缺 {missingTemplateMarkers.length} 个槽位标记</span>
              </span>
            )}
          </div>
          <div className="code-archdoc-drawer-footer-actions">
            <button type="button" className="btn btn-secondary" onClick={onClose} disabled={saving}>
              取消
            </button>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => void handleSave()}
              disabled={saving || loading}
            >
              {saving ? <Loader2 size={15} className="code-archdoc-spin" /> : <Save size={15} />}
              <span>{saving ? '保存落盘中...' : isCreate ? '创建并落盘' : '保存并同步修订'}</span>
            </button>
          </div>
        </div>
      }
    >
      {loading ? (
        <div className="code-archdoc-loading">
          <Loader2 size={18} className="code-archdoc-spin" />
          <span>正在加载文档类型资产...</span>
        </div>
      ) : (
        <div className="code-archdoc-editor-body">
          <div className="code-archdoc-editor-tabs" role="tablist">
            <button
              type="button"
              role="tab"
              aria-selected={mainTab === 'meta'}
              className={`code-archdoc-editor-tab ${
                mainTab === 'meta' ? 'code-archdoc-editor-tab--active' : ''
              }`}
              onClick={() => setMainTab('meta')}
            >
              <Settings2 size={14} />
              <span>基础元数据</span>
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mainTab === 'slots'}
              className={`code-archdoc-editor-tab ${
                mainTab === 'slots' ? 'code-archdoc-editor-tab--active' : ''
              }`}
              onClick={() => setMainTab('slots')}
            >
              <Layers size={14} />
              <span>槽位构建器 ({slots.length})</span>
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mainTab === 'assets'}
              className={`code-archdoc-editor-tab ${
                mainTab === 'assets' ? 'code-archdoc-editor-tab--active' : ''
              }`}
              onClick={() => setMainTab('assets')}
            >
              <FileCode2 size={14} />
              <span>模板与提示词资产</span>
            </button>
            {!isCreate && (
              <button
                type="button"
                role="tab"
                aria-selected={mainTab === 'revisions'}
                className={`code-archdoc-editor-tab ${
                  mainTab === 'revisions' ? 'code-archdoc-editor-tab--active' : ''
                }`}
                onClick={() => setMainTab('revisions')}
              >
                <History size={14} />
                <span>修订历史</span>
              </button>
            )}
          </div>

          {mainTab === 'meta' && (
            <div className="code-archdoc-editor-section">
              <div className="code-archdoc-form-grid">
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">目录标识 (slug)</label>
                  <input
                    type="text"
                    className="code-archdoc-input code-archdoc-mono-input"
                    value={slug}
                    disabled={!isCreate}
                    placeholder="例如: security-review"
                    onChange={(e) => setSlug(e.target.value.toLowerCase())}
                  />
                  <span className="code-archdoc-field-hint">
                    仅允许小写字母、数字、中划线与下划线，创建后不可修改。
                  </span>
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">中文展示名 (display_name)</label>
                  <input
                    type="text"
                    className="code-archdoc-input"
                    value={displayName}
                    placeholder="例如: 安全架构评审报告"
                    onChange={(e) => setDisplayName(e.target.value)}
                  />
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">默认模型档位 (model_tier)</label>
                  <select
                    className="code-archdoc-select"
                    value={modelTier}
                    onChange={(e) => setModelTier(e.target.value as ModelTier)}
                  >
                    <option value="flagship">flagship (旗舰推理模型)</option>
                    <option value="fast">fast (轻量高速模型)</option>
                  </select>
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">单任务超时 (秒)</label>
                  <input
                    type="number"
                    className="code-archdoc-input"
                    min={30}
                    max={1800}
                    value={timeoutSeconds}
                    onChange={(e) =>
                      setTimeoutSeconds(Number.parseInt(e.target.value, 10) || 300)
                    }
                  />
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">全篇输出 Token 上限（可选）</label>
                  <input
                    type="number"
                    className="code-archdoc-input"
                    min={500}
                    max={64000}
                    placeholder="留空不限制（例如 agents-context 设为 4000）"
                    value={maxOutputTokens}
                    onChange={(e) => setMaxOutputTokens(e.target.value)}
                  />
                </div>

                <div className="code-archdoc-form-field code-archdoc-form-field--checkbox">
                  <label className="code-archdoc-checkbox-label">
                    <input
                      type="checkbox"
                      checked={isActive}
                      onChange={(e) => setIsActive(e.target.checked)}
                    />
                    <span>启用状态（仅启用中的文档类型允许用户触发生成）</span>
                  </label>
                </div>
              </div>

              <div className="code-archdoc-form-field">
                <label className="code-archdoc-field-label">文档定位描述 (description)</label>
                <textarea
                  className="code-archdoc-textarea"
                  rows={2}
                  value={description}
                  placeholder="说明该文档类型的目标读者与覆盖范围..."
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>

              <div className="code-archdoc-form-field">
                <label className="code-archdoc-field-label">
                  顶层订阅 Code IR 分片 (ir_facets)
                </label>
                <div className="code-archdoc-facet-chips">
                  {ALL_FACET_OPTIONS.map((opt) => {
                    const active = irFacets.includes(opt.value);
                    return (
                      <button
                        key={opt.value}
                        type="button"
                        className={`code-archdoc-facet-chip ${
                          active ? 'code-archdoc-facet-chip--active' : ''
                        }`}
                        onClick={() => handleToggleTopFacet(opt.value)}
                      >
                        {opt.label}
                      </button>
                    );
                  })}
                </div>
              </div>
            </div>
          )}

          {mainTab === 'slots' && (
            <SlotsSchemaBuilder
              slots={slots}
              persistedSlotIds={persistedSlotIds}
              onChange={setSlots}
              onSyncTemplateMarkers={handleSyncTemplateMarkers}
            />
          )}

          {mainTab === 'assets' && (
            <div className="code-archdoc-assets-editor">
              <div className="code-archdoc-assets-subtabs">
                <button
                  type="button"
                  className={`code-archdoc-subtab ${
                    assetTab === 'template' ? 'code-archdoc-subtab--active' : ''
                  }`}
                  onClick={() => setAssetTab('template')}
                >
                  template.md
                </button>
                <button
                  type="button"
                  className={`code-archdoc-subtab ${
                    assetTab === 'generation_prompt' ? 'code-archdoc-subtab--active' : ''
                  }`}
                  onClick={() => setAssetTab('generation_prompt')}
                >
                  generation_prompt.md
                </button>
                <button
                  type="button"
                  className={`code-archdoc-subtab ${
                    assetTab === 'refine_prompt' ? 'code-archdoc-subtab--active' : ''
                  }`}
                  onClick={() => setAssetTab('refine_prompt')}
                >
                  refine_prompt.md (可选)
                </button>
                {assetTab === 'template' && (
                  <button
                    type="button"
                    className="btn btn-outline btn-small code-archdoc-assets-sync-btn"
                    onClick={handleSyncTemplateMarkers}
                  >
                    补全缺失槽位标记
                  </button>
                )}
              </div>

              {assetTab === 'template' && (
                <textarea
                  className="code-archdoc-code-editor"
                  rows={18}
                  spellCheck={false}
                  value={templateMD}
                  onChange={(e) => setTemplateMD(e.target.value)}
                  placeholder="## 1. 章节标题\n<!-- slot:slot_id -->"
                />
              )}
              {assetTab === 'generation_prompt' && (
                <textarea
                  className="code-archdoc-code-editor"
                  rows={18}
                  spellCheck={false}
                  value={genPromptMD}
                  onChange={(e) => setGenPromptMD(e.target.value)}
                  placeholder="系统级文档生成指令与风格约束..."
                />
              )}
              {assetTab === 'refine_prompt' && (
                <textarea
                  className="code-archdoc-code-editor"
                  rows={18}
                  spellCheck={false}
                  value={refinePromptMD}
                  onChange={(e) => setRefinePromptMD(e.target.value)}
                  placeholder="Canvas Studio Chat 局部精修附加约束（可选）..."
                />
              )}
            </div>
          )}

          {mainTab === 'revisions' && (
            <div className="code-archdoc-revisions-panel">
              {loadingRevs ? (
                <div className="code-archdoc-loading">
                  <Loader2 size={16} className="code-archdoc-spin" />
                  <span>正在加载修订快照历史...</span>
                </div>
              ) : revisions.length === 0 ? (
                <div className="code-archdoc-slots-empty">暂无历史修订记录。</div>
              ) : (
                <table className="code-archdoc-table">
                  <thead>
                    <tr>
                      <th>修订号</th>
                      <th>聚合哈希 (aggregate_hash)</th>
                      <th>指令哈希</th>
                      <th>槽位数</th>
                      <th>创建时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {revisions.map((rev) => (
                      <tr key={rev.id}>
                        <td>
                          <span className="code-badge code-badge--primary">
                            Rev v{rev.revision_no}
                          </span>
                        </td>
                        <td>
                          <code className="code-archdoc-mono" title={rev.aggregate_hash}>
                            {rev.aggregate_hash.slice(0, 16)}
                          </code>
                        </td>
                        <td>
                          <code className="code-archdoc-mono" title={rev.prompt_hash}>
                            {rev.prompt_hash.slice(0, 12)}
                          </code>
                        </td>
                        <td>{rev.slots_snapshot.length}</td>
                        <td className="text-secondary">{formatDateTime(rev.created_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
};

export default DocTypeEditorDrawer;
