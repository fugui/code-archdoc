import React, { useState } from 'react';
import {
  ArrowRight,
  ChevronDown,
  ChevronRight,
  ShieldCheck,
  Sparkles,
} from 'lucide-react';
import type {
  CanonicalFacetName,
  ContextInputName,
  FacetName,
  LLMConfigVO,
  ModuleDigestSourceConfig,
  ScopeProfileConfig,
  VerifyPolicyConfig,
} from '../../types';
import { DOC_TYPE_CATEGORY_PRESETS } from '../../utils/docTypeUtils';
import {
  FACET_CATALOG,
  LIGHT_CONTRACT_REPO_FACETS,
} from '../../utils/docTypeBlueprint';
import IntegerInput from '../common/IntegerInput';

export interface DocTypePolicyPanelProps {
  isCreate: boolean;
  slug: string;
  displayName: string;
  shortName: string;
  category: string;
  description: string;
  preferredModel: string;
  llmConfigs: LLMConfigVO[];
  timeoutSeconds: number;
  maxOutputTokens: string;
  isActive: boolean;

  autoUnionFacets: CanonicalFacetName[];
  autoAggregateFacets: boolean;
  manualTopFacets: FacetName[];

  repoIRFacets: CanonicalFacetName[];
  contextInputs: ContextInputName[];
  moduleDigestSource: ModuleDigestSourceConfig;
  verifyPolicy: VerifyPolicyConfig;
  scopeProfile: ScopeProfileConfig;

  onSlugChange: (v: string) => void;
  onDisplayNameChange: (v: string) => void;
  onShortNameChange: (v: string) => void;
  onCategoryChange: (v: string) => void;
  onDescriptionChange: (v: string) => void;
  onPreferredModelChange: (v: string) => void;
  onTimeoutSecondsChange: (v: number) => void;
  onMaxOutputTokensChange: (v: string) => void;
  onIsActiveChange: (v: boolean) => void;

  onAutoAggregateFacetsChange: (auto: boolean) => void;
  onManualTopFacetsChange: (facets: FacetName[]) => void;
  onAlignTopFacetsWithUnion: () => void;

  onRepoIRFacetsChange: (facets: CanonicalFacetName[]) => void;
  onContextInputsChange: (inputs: ContextInputName[]) => void;
  onModuleDigestSourceChange: (src: ModuleDigestSourceConfig) => void;
  onVerifyPolicyChange: (policy: VerifyPolicyConfig) => void;
  onScopeProfileChange: (profile: ScopeProfileConfig) => void;

  onGoToBlueprint?: () => void;
}

const CONTEXT_INPUT_OPTIONS: ReadonlyArray<{
  value: ContextInputName;
  label: string;
  hint: string;
}> = [
  {
    value: 'code_ir',
    label: 'code_ir (基础静态代码 IR)',
    hint: '显式声明注入 L1 规范分片（默认均开启）',
  },
  {
    value: 'module_context',
    label: 'module_context (子模块跨模块上下游依赖)',
    hint: 'Monorepo 子模块生成时注入跨模块引用边',
  },
  {
    value: 'module_digests',
    label: 'module_digests (整仓汇总子模块摘要)',
    hint: '整仓范围生成时聚合各子模块已发布摘要',
  },
];

export const DocTypePolicyPanel: React.FC<DocTypePolicyPanelProps> = ({
  isCreate,
  slug,
  displayName,
  shortName,
  category,
  description,
  preferredModel,
  llmConfigs,
  timeoutSeconds,
  maxOutputTokens,
  isActive,
  autoUnionFacets,
  autoAggregateFacets,
  manualTopFacets,
  repoIRFacets,
  contextInputs,
  moduleDigestSource,
  verifyPolicy,
  scopeProfile,
  onSlugChange,
  onDisplayNameChange,
  onShortNameChange,
  onCategoryChange,
  onDescriptionChange,
  onPreferredModelChange,
  onTimeoutSecondsChange,
  onMaxOutputTokensChange,
  onIsActiveChange,
  onAutoAggregateFacetsChange,
  onManualTopFacetsChange,
  onAlignTopFacetsWithUnion,
  onRepoIRFacetsChange,
  onContextInputsChange,
  onModuleDigestSourceChange,
  onVerifyPolicyChange,
  onScopeProfileChange,
  onGoToBlueprint,
}) => {
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(false);

  const toggleManualTopFacet = (facet: FacetName) => {
    const next = manualTopFacets.includes(facet)
      ? manualTopFacets.filter((f) => f !== facet)
      : [...manualTopFacets, facet];
    onManualTopFacetsChange(next);
  };

  const toggleRepoFacet = (facet: CanonicalFacetName) => {
    const next = repoIRFacets.includes(facet)
      ? repoIRFacets.filter((f) => f !== facet)
      : [...repoIRFacets, facet];
    onRepoIRFacetsChange(next);
  };

  const toggleContextInput = (input: ContextInputName) => {
    const next = contextInputs.includes(input)
      ? contextInputs.filter((x) => x !== input)
      : [...contextInputs, input];
    onContextInputsChange(next);
  };

  const hasModuleDigests = contextInputs.includes('module_digests');

  return (
    <div className="code-archdoc-editor-section">
      {/* 基础身份与运行参数 */}
      <div className="code-archdoc-form-grid">
        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">目录标识 (slug)</label>
          <input
            type="text"
            className="code-archdoc-input code-archdoc-mono-input"
            value={slug}
            disabled={!isCreate}
            placeholder="例如: security-review"
            onChange={(e) => onSlugChange(e.target.value.toLowerCase())}
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
            onChange={(e) => onDisplayNameChange(e.target.value)}
          />
        </div>

        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">短标题 (short_name，可选)</label>
          <input
            type="text"
            className="code-archdoc-input"
            value={shortName}
            maxLength={32}
            placeholder="例如: 安全评审（用于覆盖矩阵紧凑胶囊）"
            onChange={(e) => onShortNameChange(e.target.value)}
          />
        </div>

        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">文档分组 (category，可选)</label>
          <input
            type="text"
            list="archdoc-category-presets"
            className="code-archdoc-input"
            value={category}
            maxLength={32}
            placeholder="例如: 架构设计 / 质量与安全"
            onChange={(e) => onCategoryChange(e.target.value)}
          />
          <datalist id="archdoc-category-presets">
            {DOC_TYPE_CATEGORY_PRESETS.map((preset) => (
              <option key={preset} value={preset} />
            ))}
          </datalist>
        </div>

        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">指定生成模型（可选）</label>
          <select
            className="code-archdoc-select"
            value={preferredModel}
            onChange={(e) => onPreferredModelChange(e.target.value)}
          >
            <option value="">跟随系统默认模型</option>
            {llmConfigs.map((cfg) => (
              <option key={cfg.id} value={cfg.name}>
                {cfg.name}
                {cfg.is_default ? '（当前默认）' : ''}
                {!cfg.is_active ? ' [已停用]' : ''}
              </option>
            ))}
            {preferredModel && !llmConfigs.some((cfg) => cfg.name === preferredModel) && (
              <option value={preferredModel}>{preferredModel}（未在模型池中找到）</option>
            )}
          </select>
        </div>

        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">单任务超时 (秒)</label>
          <IntegerInput
            className="code-archdoc-input"
            min={30}
            max={1800}
            value={timeoutSeconds}
            onChange={onTimeoutSecondsChange}
          />
        </div>

        <div className="code-archdoc-form-field">
          <label className="code-archdoc-field-label">全篇输出 Token 上限（可选）</label>
          <input
            type="number"
            inputMode="numeric"
            step={1}
            className="code-archdoc-input"
            min={500}
            max={64000}
            placeholder="留空不限制（例如 agents-context 设为 4000）"
            value={maxOutputTokens}
            onChange={(e) => onMaxOutputTokensChange(e.target.value)}
          />
        </div>

        <div className="code-archdoc-form-field code-archdoc-form-field--checkbox">
          <label className="code-archdoc-checkbox-label">
            <input
              type="checkbox"
              checked={isActive}
              onChange={(e) => onIsActiveChange(e.target.checked)}
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
          onChange={(e) => onDescriptionChange(e.target.value)}
        />
      </div>

      {isCreate && onGoToBlueprint && (
        <div className="code-archdoc-blueprint-next-banner">
          <span>填完基础信息后，前往「文档蓝图」编排章节标题、分片订阅与写作提示。</span>
          <button
            type="button"
            className="btn btn-primary btn-small"
            onClick={onGoToBlueprint}
          >
            <span>进入文档蓝图编排</span>
            <ArrowRight size={13} />
          </button>
        </div>
      )}

      {/* 顶层 ir_facets 自动并集 / 手动覆写 */}
      <div className="code-archdoc-blueprint-policy-card">
        <div className="code-archdoc-blueprint-policy-head">
          <div>
            <div className="code-archdoc-field-label">顶层 Code IR 分片总声明 (meta.ir_facets)</div>
            <div className="code-archdoc-field-hint">
              运行时 Prompt 注入取决于各章节槽位订阅；此处建议保持为全部章节槽位的自动并集。
            </div>
          </div>
          <div className="code-archdoc-blueprint-policy-mode-ops">
            <label className="code-archdoc-checkbox-label">
              <input
                type="checkbox"
                checked={autoAggregateFacets}
                onChange={(e) => onAutoAggregateFacetsChange(e.target.checked)}
              />
              <span>自动聚合自全部章节槽位（推荐）</span>
            </label>
            {!autoAggregateFacets && (
              <button
                type="button"
                className="btn btn-outline btn-small"
                onClick={onAlignTopFacetsWithUnion}
              >
                一键对齐并集
              </button>
            )}
          </div>
        </div>

        {autoAggregateFacets ? (
          <div className="code-archdoc-facet-chips">
            {autoUnionFacets.length === 0 ? (
              <span className="code-archdoc-field-hint">暂无槽位订阅分片，请先在蓝图中勾选分片。</span>
            ) : (
              autoUnionFacets.map((facet) => (
                <span key={facet} className="code-badge code-badge--primary">
                  {facet}
                </span>
              ))
            )}
          </div>
        ) : (
          <div className="code-archdoc-facet-chips">
            {FACET_CATALOG.map((item) => {
              const active = manualTopFacets.includes(item.value);
              return (
                <button
                  key={item.value}
                  type="button"
                  className={`code-archdoc-facet-chip ${
                    active ? 'code-archdoc-facet-chip--active' : ''
                  }`}
                  onClick={() => toggleManualTopFacet(item.value)}
                >
                  {item.label}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* 折叠面板：高级治理策略（可选） */}
      <div className="code-archdoc-blueprint-policy-card">
        <button
          type="button"
          className="code-archdoc-blueprint-accordion-trigger"
          onClick={() => setAdvancedOpen((prev) => !prev)}
          aria-expanded={advancedOpen}
        >
          <span className="code-archdoc-blueprint-accordion-title">
            {advancedOpen ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
            <ShieldCheck size={15} />
            <span>高级治理策略（可选：整仓裁剪 / 跨模块上下文 / Verifier 门禁 / 范围过滤）</span>
          </span>
          <span className="code-badge code-badge--muted">
            {repoIRFacets.length > 0 ? `整仓白名单 ${repoIRFacets.length} 项` : '使用默认策略'}
          </span>
        </button>

        {advancedOpen && (
          <div className="code-archdoc-blueprint-advanced-body">
            {/* 1. repo_ir_facets */}
            <div className="code-archdoc-blueprint-subsection">
              <div className="code-archdoc-blueprint-policy-head">
                <div>
                  <div className="code-archdoc-field-label">
                    多模块整仓汇总裁剪白名单 (repo_ir_facets)
                  </div>
                  <div className="code-archdoc-field-hint">
                    大仓整仓汇总且开启 module_digests 时，槽位分片将与此白名单取交集以防撑爆上下文。
                  </div>
                </div>
                <div className="code-archdoc-blueprint-source-actions">
                  <button
                    type="button"
                    className="btn btn-outline btn-small"
                    onClick={() => onRepoIRFacetsChange([...LIGHT_CONTRACT_REPO_FACETS])}
                  >
                    <Sparkles size={12} />
                    <span>一键应用轻量与契约分片预设</span>
                  </button>
                  {repoIRFacets.length > 0 && (
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      onClick={() => onRepoIRFacetsChange([])}
                    >
                      清空
                    </button>
                  )}
                </div>
              </div>
              <div className="code-archdoc-facet-chips">
                {FACET_CATALOG.map((item) => {
                  const active = repoIRFacets.includes(item.value);
                  return (
                    <button
                      key={item.value}
                      type="button"
                      className={`code-archdoc-facet-chip ${
                        active ? 'code-archdoc-facet-chip--active' : ''
                      }`}
                      onClick={() => toggleRepoFacet(item.value)}
                    >
                      <span>{item.value}</span>
                      <span
                        className={`code-archdoc-blueprint-weight code-archdoc-blueprint-weight--${item.weight}`}
                      >
                        {item.weightLabel}
                      </span>
                    </button>
                  );
                })}
              </div>
            </div>

            {/* 2. context_inputs & module_digest_source */}
            <div className="code-archdoc-blueprint-subsection">
              <div className="code-archdoc-field-label">多模块上下文输入 (context_inputs)</div>
              <div className="code-archdoc-blueprint-checkbox-grid">
                {CONTEXT_INPUT_OPTIONS.map((opt) => (
                  <label
                    key={opt.value}
                    className="code-archdoc-checkbox-label"
                    title={opt.hint}
                  >
                    <input
                      type="checkbox"
                      checked={contextInputs.includes(opt.value)}
                      onChange={() => toggleContextInput(opt.value)}
                    />
                    <span>{opt.label}</span>
                  </label>
                ))}
              </div>

              {hasModuleDigests && (
                <div className="code-archdoc-form-grid code-archdoc-blueprint-digest-grid">
                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">摘要来源 DocType Slug</label>
                    <input
                      type="text"
                      className="code-archdoc-input code-archdoc-mono-input"
                      value={moduleDigestSource.doc_type_slug}
                      placeholder={slug || '例如: system-architecture'}
                      onChange={(e) =>
                        onModuleDigestSourceChange({
                          ...moduleDigestSource,
                          doc_type_slug: e.target.value.trim(),
                        })
                      }
                    />
                  </div>
                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">摘要来源 Slot ID</label>
                    <input
                      type="text"
                      className="code-archdoc-input code-archdoc-mono-input"
                      value={moduleDigestSource.slot_id}
                      placeholder="例如: overview_boundaries"
                      onChange={(e) =>
                        onModuleDigestSourceChange({
                          ...moduleDigestSource,
                          slot_id: e.target.value.trim(),
                        })
                      }
                    />
                  </div>
                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">单模块摘要最大字符 (max_chars)</label>
                    <IntegerInput
                      className="code-archdoc-input"
                      min={100}
                      max={4000}
                      value={moduleDigestSource.max_chars ?? 600}
                      onChange={(next) =>
                        onModuleDigestSourceChange({
                          ...moduleDigestSource,
                          max_chars: next,
                        })
                      }
                    />
                  </div>
                </div>
              )}
            </div>

            {/* 3. verify_policy */}
            <div className="code-archdoc-blueprint-subsection">
              <div className="code-archdoc-field-label">Verifier 四重门禁策略 (verify_policy)</div>
              <div className="code-archdoc-blueprint-checkbox-grid">
                <label className="code-archdoc-checkbox-label">
                  <input
                    type="checkbox"
                    checked={verifyPolicy.strict_symbol_check}
                    onChange={(e) =>
                      onVerifyPolicyChange({
                        ...verifyPolicy,
                        strict_symbol_check: e.target.checked,
                      })
                    }
                  />
                  <span>严格符号校验 (strict_symbol_check)</span>
                </label>
                <label className="code-archdoc-checkbox-label">
                  <input
                    type="checkbox"
                    checked={verifyPolicy.require_mermaid}
                    onChange={(e) =>
                      onVerifyPolicyChange({
                        ...verifyPolicy,
                        require_mermaid: e.target.checked,
                      })
                    }
                  />
                  <span>强制包含 Mermaid 图 (require_mermaid)</span>
                </label>
                <label className="code-archdoc-checkbox-label">
                  <input
                    type="checkbox"
                    checked={verifyPolicy.block_on_secret_leak !== false}
                    onChange={(e) =>
                      onVerifyPolicyChange({
                        ...verifyPolicy,
                        block_on_secret_leak: e.target.checked,
                      })
                    }
                  />
                  <span>明文凭据阻断发布 (block_on_secret_leak)</span>
                </label>
              </div>
              <div className="code-archdoc-form-grid">
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">Mermaid 节点上限 (max_mermaid_nodes)</label>
                  <IntegerInput
                    className="code-archdoc-input"
                    min={5}
                    max={100}
                    value={verifyPolicy.max_mermaid_nodes ?? 35}
                    onChange={(next) =>
                      onVerifyPolicyChange({
                        ...verifyPolicy,
                        max_mermaid_nodes: next,
                      })
                    }
                  />
                </div>
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">Mermaid 连边上限 (max_mermaid_edges)</label>
                  <IntegerInput
                    className="code-archdoc-input"
                    min={5}
                    max={200}
                    value={verifyPolicy.max_mermaid_edges ?? 60}
                    onChange={(next) =>
                      onVerifyPolicyChange({
                        ...verifyPolicy,
                        max_mermaid_edges: next,
                      })
                    }
                  />
                </div>
              </div>
            </div>

            {/* 4. scope_profile */}
            <div className="code-archdoc-blueprint-subsection">
              <div className="code-archdoc-field-label">沙箱扫描范围裁剪 (scope_profile)</div>
              <div className="code-archdoc-form-grid">
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">最大目录深度 (max_depth)</label>
                  <IntegerInput
                    className="code-archdoc-input"
                    min={1}
                    max={32}
                    value={scopeProfile.max_depth ?? 12}
                    onChange={(next) =>
                      onScopeProfileChange({
                        ...scopeProfile,
                        max_depth: next,
                      })
                    }
                  />
                </div>
                <div className="code-archdoc-form-field code-archdoc-form-field--checkbox">
                  <div className="code-archdoc-blueprint-checkbox-grid">
                    <label className="code-archdoc-checkbox-label">
                      <input
                        type="checkbox"
                        checked={scopeProfile.include_tests}
                        onChange={(e) =>
                          onScopeProfileChange({
                            ...scopeProfile,
                            include_tests: e.target.checked,
                          })
                        }
                      />
                      <span>包含测试文件 (include_tests)</span>
                    </label>
                    <label className="code-archdoc-checkbox-label">
                      <input
                        type="checkbox"
                        checked={Boolean(scopeProfile.include_legacy_docs)}
                        onChange={(e) =>
                          onScopeProfileChange({
                            ...scopeProfile,
                            include_legacy_docs: e.target.checked,
                          })
                        }
                      />
                      <span>包含历史文档 (include_legacy_docs)</span>
                    </label>
                  </div>
                </div>
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">包含 Glob 规则（每行一条，可选）</label>
                  <textarea
                    className="code-archdoc-textarea code-archdoc-mono-input"
                    rows={2}
                    value={(scopeProfile.include_globs ?? []).join('\n')}
                    placeholder="例如: src/**"
                    onChange={(e) => {
                      const globs = e.target.value
                        .split('\n')
                        .map((s) => s.trim())
                        .filter(Boolean);
                      onScopeProfileChange({ ...scopeProfile, include_globs: globs });
                    }}
                  />
                </div>
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">排除 Glob 规则（每行一条，可选）</label>
                  <textarea
                    className="code-archdoc-textarea code-archdoc-mono-input"
                    rows={2}
                    value={(scopeProfile.exclude_globs ?? []).join('\n')}
                    placeholder="例如: third_party/**"
                    onChange={(e) => {
                      const globs = e.target.value
                        .split('\n')
                        .map((s) => s.trim())
                        .filter(Boolean);
                      onScopeProfileChange({ ...scopeProfile, exclude_globs: globs });
                    }}
                  />
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default DocTypePolicyPanel;
