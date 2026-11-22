import React, { useState } from 'react';
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  FileText,
  Lock,
  Trash2,
  Unlock,
} from 'lucide-react';
import { useConfirm } from '@code/common';
import IntegerInput from '../common/IntegerInput';
import type { FacetName, SectionOwnership, SlotDefinition } from '../../types';
import {
  FACET_CATALOG,
  type BlueprintBlock,
  type BlueprintHeadingLevel,
  type DocBlueprint,
  type SlotBlueprintBlock,
  type StaticBlueprintBlock,
} from '../../utils/docTypeBlueprint';

export interface BlueprintVisualEditorProps {
  blueprint: DocBlueprint;
  persistedSlotIds: ReadonlySet<string>;
  onChange: (next: DocBlueprint) => void;
  onStaticEdited?: (blockKey: string) => void;
  onConfirmSlotChange?: (slotId: string) => void;
}

const OWNERSHIP_OPTIONS: ReadonlyArray<{ value: SectionOwnership; label: string }> = [
  { value: 'auto', label: 'auto (AI 全托管)' },
  { value: 'hybrid', label: 'hybrid (人机融合)' },
  { value: 'locked', label: 'locked (人工锁定)' },
];

const HEADING_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: '2', label: 'H2 (##)' },
  { value: '3', label: 'H3 (###)' },
  { value: '4', label: 'H4 (####)' },
  { value: 'none', label: '无标题内嵌' },
];

export const BlueprintVisualEditor: React.FC<BlueprintVisualEditorProps> = ({
  blueprint,
  persistedSlotIds,
  onChange,
  onStaticEdited,
  onConfirmSlotChange,
}) => {
  const confirm = useConfirm();
  const [collapsedKeys, setCollapsedKeys] = useState<Record<string, boolean>>({});
  const [unlockedKeys, setUnlockedKeys] = useState<Record<string, boolean>>({});

  const slotCount = blueprint.blocks.filter((b) => b.kind === 'slot').length;

  const updateBlock = (index: number, updater: (prev: BlueprintBlock) => BlueprintBlock) => {
    const nextBlocks = blueprint.blocks.map((blk, i) => (i === index ? updater(blk) : blk));
    onChange({ blocks: nextBlocks });
  };

  const handleMoveBlock = (index: number, dir: -1 | 1) => {
    const target = index + dir;
    if (target < 0 || target >= blueprint.blocks.length) return;
    const nextBlocks = [...blueprint.blocks];
    const [moved] = nextBlocks.splice(index, 1);
    nextBlocks.splice(target, 0, moved);
    onChange({ blocks: nextBlocks });
  };

  const handleDeleteBlock = async (index: number) => {
    const blk = blueprint.blocks[index];
    if (!blk) return;

    if (blk.kind === 'slot') {
      if (slotCount <= 1) return;
      if (persistedSlotIds.has(blk.slot.slot_id)) {
        const ok = await confirm({
          title: `确认移除既有槽位 "${blk.slot.slot_id}"？`,
          content:
            '移除已落盘的既有槽位将触发 Spec Drift；已发布文档中若该槽位被人工锁定，重生成时会自动保留至「孤立章节待处置区」。',
          confirmText: '确认移除',
          cancelText: '取消',
          type: 'warning',
        });
        if (!ok) return;
        onConfirmSlotChange?.(blk.slot.slot_id);
      }
    }

    onChange({
      blocks: blueprint.blocks.filter((_, i) => i !== index),
    });
  };

  const handleUnlockSlotId = async (blk: SlotBlueprintBlock) => {
    const ok = await confirm({
      title: `确认解锁修改既有 slot_id "${blk.slot.slot_id}"？`,
      content:
        'slot_id 是跨修订合并、人工锁定豁免与 Spec Drift 追踪的主键。修改既有 slot_id 会使历史已发布文档的原章节进入孤立章节区。确定要解锁修改吗？',
      confirmText: '了解风险，解锁修改',
      cancelText: '保持原标识',
      type: 'warning',
    });
    if (!ok) return;
    setUnlockedKeys((prev) => ({ ...prev, [blk.key]: true }));
    onConfirmSlotChange?.(blk.slot.slot_id);
  };

  const toggleCollapse = (key: string) => {
    setCollapsedKeys((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const updateSlotField = (
    index: number,
    patch: Partial<SlotDefinition>,
    blockPatch?: Partial<Omit<SlotBlueprintBlock, 'kind' | 'key' | 'slot'>>,
  ) => {
    updateBlock(index, (prev) => {
      if (prev.kind !== 'slot') return prev;
      return {
        ...prev,
        ...blockPatch,
        slot: {
          ...prev.slot,
          ...patch,
        },
      };
    });
  };

  const toggleSlotFacet = (index: number, facet: FacetName) => {
    const blk = blueprint.blocks[index];
    if (!blk || blk.kind !== 'slot') return;
    const current = blk.slot.ir_facets ?? [];
    const nextFacets = current.includes(facet)
      ? current.filter((f) => f !== facet)
      : [...current, facet];
    updateSlotField(index, { ir_facets: nextFacets });
  };

  if (blueprint.blocks.length === 0) {
    return (
      <div className="code-archdoc-slots-empty">
        当前文档蓝图为空，请点击上方工具栏「新增章节槽位」或「插入固定文本块」。
      </div>
    );
  }

  let slotSeq = 0;

  return (
    <div className="code-archdoc-blueprint-flow">
      {blueprint.blocks.map((blk, index) => {
        if (blk.kind === 'static') {
          const staticBlk: StaticBlueprintBlock = blk;
          const isLeadH1 = index === 0 && staticBlk.markdown.trimStart().startsWith('# ');
          return (
            <div key={staticBlk.key} className="code-archdoc-blueprint-static-card">
              <div className="code-archdoc-blueprint-card-head">
                <div className="code-archdoc-blueprint-card-title-group">
                  <FileText size={14} className="code-archdoc-blueprint-static-icon" />
                  <span className="code-badge code-badge--muted">
                    {isLeadH1 ? '文首主标题 / 导语（固定 Markdown）' : '固定 Markdown 说明块'}
                  </span>
                </div>
                <div className="code-archdoc-slot-item-ops">
                  <button
                    type="button"
                    className="btn btn-text btn-small"
                    disabled={index === 0}
                    onClick={() => handleMoveBlock(index, -1)}
                    title="上移"
                    aria-label="上移固定文本块"
                  >
                    <ArrowUp size={14} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-text btn-small"
                    disabled={index === blueprint.blocks.length - 1}
                    onClick={() => handleMoveBlock(index, 1)}
                    title="下移"
                    aria-label="下移固定文本块"
                  >
                    <ArrowDown size={14} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-text btn-small code-archdoc-btn-danger-text"
                    onClick={() => void handleDeleteBlock(index)}
                    title="删除此固定文本块"
                    aria-label="删除固定文本块"
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </div>
              <textarea
                className="code-archdoc-textarea code-archdoc-blueprint-static-input"
                rows={Math.min(8, Math.max(2, staticBlk.markdown.split('\n').length))}
                value={staticBlk.markdown}
                placeholder="# 文档标题 或 章节间固定声明 Markdown..."
                onChange={(e) => {
                  onStaticEdited?.(staticBlk.key);
                  const nextMarkdown = e.target.value;
                  updateBlock(index, (prev) =>
                    prev.kind === 'static' ? { ...prev, markdown: nextMarkdown } : prev,
                  );
                }}
              />
            </div>
          );
        }

        slotSeq += 1;
        const slotBlk: SlotBlueprintBlock = blk;
        const slot = slotBlk.slot;
        const isCollapsed = Boolean(collapsedKeys[slotBlk.key]);
        const isPersistedLocked =
          persistedSlotIds.has(slot.slot_id) && !unlockedKeys[slotBlk.key];
        const isPersistedUnlocked =
          persistedSlotIds.has(slot.slot_id) && Boolean(unlockedKeys[slotBlk.key]);
        const hasHeavyFacet =
          slot.ir_facets.includes('symbols') || slot.ir_facets.includes('runtime_primitives');
        const showHeavyBudgetTip = hasHeavyFacet && slot.token_budget < 10000;
        const sourceToolsEnabled = Boolean(slot.enable_source_tools);
        const hintLen = Array.from(slot.instruction_hint ?? '').length;

        return (
          <div key={slotBlk.key} className="code-archdoc-blueprint-slot-card">
            <div className="code-archdoc-blueprint-slot-head">
              <button
                type="button"
                className="code-archdoc-blueprint-collapse-btn"
                onClick={() => toggleCollapse(slotBlk.key)}
                title={isCollapsed ? '展开槽位详情' : '收起槽位详情'}
                aria-label={isCollapsed ? '展开槽位' : '收起槽位'}
              >
                {isCollapsed ? <ChevronRight size={15} /> : <ChevronDown size={15} />}
                <span className="code-badge code-badge--primary">#{slotSeq}</span>
              </button>

              <select
                className="code-archdoc-select code-archdoc-blueprint-heading-select"
                aria-label="章节标题层级"
                value={slotBlk.emitHeading ? String(slotBlk.headingLevel) : 'none'}
                onChange={(e) => {
                  const val = e.target.value;
                  updateBlock(index, (prev) => {
                    if (prev.kind !== 'slot') return prev;
                    if (val === 'none') {
                      return { ...prev, emitHeading: false };
                    }
                    const lvl = Number.parseInt(val, 10) as BlueprintHeadingLevel;
                    return { ...prev, emitHeading: true, headingLevel: lvl };
                  });
                }}
              >
                {HEADING_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>

              <input
                type="text"
                className="code-archdoc-input code-archdoc-blueprint-title-input"
                value={slot.title}
                placeholder="输入章节标题（将同步写入 template.md 与工作台大纲）"
                onChange={(e) =>
                  updateSlotField(
                    index,
                    { title: e.target.value },
                    { divergedTemplateHeading: undefined },
                  )
                }
              />

              <div className="code-archdoc-slot-item-ops">
                <button
                  type="button"
                  className="btn btn-text btn-small"
                  disabled={index === 0}
                  onClick={() => handleMoveBlock(index, -1)}
                  title="上移"
                  aria-label="上移槽位"
                >
                  <ArrowUp size={14} />
                </button>
                <button
                  type="button"
                  className="btn btn-text btn-small"
                  disabled={index === blueprint.blocks.length - 1}
                  onClick={() => handleMoveBlock(index, 1)}
                  title="下移"
                  aria-label="下移槽位"
                >
                  <ArrowDown size={14} />
                </button>
                <button
                  type="button"
                  className="btn btn-text btn-small code-archdoc-btn-danger-text"
                  disabled={slotCount <= 1}
                  onClick={() => void handleDeleteBlock(index)}
                  title={slotCount <= 1 ? '至少保留 1 个章节槽位' : '删除此槽位'}
                  aria-label="删除槽位"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            </div>

            {slotBlk.divergedTemplateHeading && (
              <div className="code-archdoc-blueprint-inline-tip code-archdoc-blueprint-inline-tip--info">
                <AlertTriangle size={13} />
                <span>
                  已对齐为槽位标题，保存后生效（原 template.md 标题：
                  <code>{slotBlk.divergedTemplateHeading}</code>）
                </span>
              </div>
            )}

            {isCollapsed ? (
              <div className="code-archdoc-blueprint-collapsed-summary">
                <code className="code-archdoc-mono">{slot.slot_id}</code>
                <span>· {slot.ownership}</span>
                <span>· {slot.required ? '必填' : '可选'}</span>
                <span>· 预算 {slot.token_budget}</span>
                <span>· 分片 {slot.ir_facets.length} 项</span>
                {sourceToolsEnabled && (
                  <span className="code-badge code-badge--purple">
                    源码工具 ≤ {slot.max_tool_rounds || 4} 轮
                  </span>
                )}
              </div>
            ) : (
              <div className="code-archdoc-blueprint-slot-body">
                {/* 参数胶囊条 */}
                <div className="code-archdoc-blueprint-param-grid">
                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">
                      槽位标识 (slot_id)
                      {isPersistedLocked && (
                        <button
                          type="button"
                          className="code-archdoc-slot-unlock-btn"
                          onClick={() => void handleUnlockSlotId(slotBlk)}
                          title="既有槽位标识默认锁定，点击确认风险后解锁"
                        >
                          <Lock size={11} />
                          <span>解锁修改</span>
                        </button>
                      )}
                      {isPersistedUnlocked && (
                        <span className="code-badge code-badge--warning">
                          <Unlock size={10} />
                          <span>已解锁（高风险）</span>
                        </span>
                      )}
                    </label>
                    <input
                      type="text"
                      className="code-archdoc-input code-archdoc-mono-input"
                      value={slot.slot_id}
                      disabled={isPersistedLocked}
                      placeholder="例如: core_request_flows"
                      onChange={(e) => updateSlotField(index, { slot_id: e.target.value.trim() })}
                    />
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">默认所有权 (ownership)</label>
                    <select
                      className="code-archdoc-select"
                      value={slot.ownership}
                      onChange={(e) =>
                        updateSlotField(index, { ownership: e.target.value as SectionOwnership })
                      }
                    >
                      {OWNERSHIP_OPTIONS.map((opt) => (
                        <option key={opt.value} value={opt.value}>
                          {opt.label}
                        </option>
                      ))}
                    </select>
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">Token 预算 (token_budget)</label>
                    <IntegerInput
                      className="code-archdoc-input"
                      min={500}
                      max={64000}
                      value={slot.token_budget}
                      onChange={(next) =>
                        updateSlotField(index, {
                          token_budget: next,
                        })
                      }
                    />
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">最少有效字符 (min_chars)</label>
                    <IntegerInput
                      className="code-archdoc-input"
                      min={0}
                      max={10000}
                      value={slot.min_chars}
                      onChange={(next) =>
                        updateSlotField(index, {
                          min_chars: next,
                        })
                      }
                    />
                  </div>

                  <div className="code-archdoc-form-field code-archdoc-form-field--checkbox">
                    <label className="code-archdoc-checkbox-label">
                      <input
                        type="checkbox"
                        checked={slot.required}
                        onChange={(e) => updateSlotField(index, { required: e.target.checked })}
                      />
                      <span>必填章节（Verifier 门禁）</span>
                    </label>
                  </div>

                  <div className="code-archdoc-form-field code-archdoc-blueprint-tools-field">
                    <label className="code-archdoc-checkbox-label">
                      <input
                        type="checkbox"
                        checked={sourceToolsEnabled}
                        onChange={(e) => {
                          const enabled = e.target.checked;
                          updateSlotField(index, {
                            enable_source_tools: enabled,
                            max_tool_rounds: enabled
                              ? slot.max_tool_rounds && slot.max_tool_rounds > 0
                                ? slot.max_tool_rounds
                                : 4
                              : 0,
                          });
                        }}
                      />
                      <span>开放只读源码工具</span>
                    </label>
                    {sourceToolsEnabled && (
                      <div className="code-archdoc-blueprint-rounds-wrap">
                        <span className="code-archdoc-field-hint">最大轮次</span>
                        <IntegerInput
                          className="code-archdoc-input code-archdoc-blueprint-rounds-input"
                          min={1}
                          max={6}
                          value={slot.max_tool_rounds ?? 4}
                          onChange={(next) =>
                            updateSlotField(index, {
                              max_tool_rounds: next,
                            })
                          }
                        />
                      </div>
                    )}
                  </div>
                </div>

                {/* 订阅 Code IR 分片 Chip 组 */}
                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">
                    订阅 Code IR 分片 (ir_facets)
                    {slot.ir_facets.length === 0 && (
                      <span className="code-badge code-badge--warning">建议至少订阅 1 项</span>
                    )}
                  </label>
                  <div className="code-archdoc-facet-chips">
                    {FACET_CATALOG.map((item) => {
                      const active = slot.ir_facets.includes(item.value);
                      return (
                        <button
                          key={item.value}
                          type="button"
                          title={item.description}
                          className={`code-archdoc-facet-chip ${
                            active ? 'code-archdoc-facet-chip--active' : ''
                          }`}
                          onClick={() => toggleSlotFacet(index, item.value)}
                        >
                          <span>{item.value}</span>
                          <span className="code-archdoc-blueprint-chip-sub">{item.shortLabel}</span>
                          <span
                            className={`code-archdoc-blueprint-weight code-archdoc-blueprint-weight--${item.weight}`}
                          >
                            {item.weightLabel}
                          </span>
                        </button>
                      );
                    })}
                  </div>
                  {showHeavyBudgetTip && (
                    <div className="code-archdoc-blueprint-inline-tip code-archdoc-blueprint-inline-tip--warning">
                      <AlertTriangle size={13} />
                      <span>
                        已订阅较重分片（symbols / runtime_primitives），建议将 Token 预算设为{' '}
                        <code>10000</code> 以上，避免 L2 上下文被截断。
                      </span>
                    </div>
                  )}
                </div>

                {/* 多行章节写作提示 */}
                <div className="code-archdoc-form-field">
                  <div className="code-archdoc-blueprint-hint-header">
                    <label className="code-archdoc-field-label">
                      章节写作提示 (instruction_hint)
                    </label>
                    <span className="code-archdoc-field-hint">{hintLen} / 1000</span>
                  </div>
                  <textarea
                    className="code-archdoc-textarea"
                    rows={3}
                    maxLength={1000}
                    value={slot.instruction_hint ?? ''}
                    placeholder="填写该章节给大模型的专属写作要点、必需 Mermaid 图类型或重点关注文件..."
                    onChange={(e) => updateSlotField(index, { instruction_hint: e.target.value })}
                  />
                </div>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
};

export default BlueprintVisualEditor;
