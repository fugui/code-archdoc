import React, { useState } from 'react';
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Lock,
  Plus,
  Trash2,
  Unlock,
} from 'lucide-react';
import { useConfirm } from '@code/common';
import type { FacetName, SectionOwnership, SlotDefinition } from '../../types';

export const ALL_FACET_OPTIONS: Array<{ value: FacetName; label: string }> = [
  { value: 'tree', label: 'tree (目录树)' },
  { value: 'build', label: 'build (构建)' },
  { value: 'deps', label: 'deps (依赖)' },
  { value: 'config_keys', label: 'config_keys (配置键)' },
  { value: 'symbols', label: 'symbols (代码符号)' },
  { value: 'idl_contracts', label: 'idl_contracts (Proto/RPC)' },
  { value: 'http_routes', label: 'http_routes (HTTP 路由)' },
  { value: 'data_models', label: 'data_models (数据模型)' },
  { value: 'runtime_primitives', label: 'runtime_primitives (并发/调度)' },
];

const OWNERSHIP_OPTIONS: Array<{ value: SectionOwnership; label: string }> = [
  { value: 'auto', label: 'auto (AI 全托管)' },
  { value: 'hybrid', label: 'hybrid (人机融合)' },
  { value: 'locked', label: 'locked (人工锁定)' },
];

export interface SlotsSchemaBuilderProps {
  slots: SlotDefinition[];
  /** 已落盘既有槽位 ID 集合：修改此类 slot_id 前必须弹出风险确认 */
  persistedSlotIds: Set<string>;
  onChange: (nextSlots: SlotDefinition[]) => void;
  onSyncTemplateMarkers?: () => void;
}

export const SlotsSchemaBuilder: React.FC<SlotsSchemaBuilderProps> = ({
  slots,
  persistedSlotIds,
  onChange,
  onSyncTemplateMarkers,
}) => {
  const confirm = useConfirm();
  const [unlockedSlotIndices, setUnlockedSlotIndices] = useState<Record<number, boolean>>({});

  const handleAddSlot = () => {
    const idx = slots.length + 1;
    const nextSlot: SlotDefinition = {
      slot_id: `custom_section_${idx}`,
      title: `${idx}. 新章节标题`,
      ownership: 'auto',
      required: true,
      ir_facets: ['tree'],
      token_budget: 8000,
      min_chars: 40,
      instruction_hint: '',
    };
    onChange([...slots, nextSlot]);
  };

  const handleUpdateSlot = (index: number, patch: Partial<SlotDefinition>) => {
    const next = slots.map((item, i) => (i === index ? { ...item, ...patch } : item));
    onChange(next);
  };

  const handleToggleFacet = (index: number, facet: FacetName) => {
    const target = slots[index];
    if (!target) return;
    const has = target.ir_facets.includes(facet);
    const nextFacets = has
      ? target.ir_facets.filter((f) => f !== facet)
      : [...target.ir_facets, facet];
    handleUpdateSlot(index, { ir_facets: nextFacets });
  };

  const handleUnlockSlotId = async (index: number, currentSlotId: string) => {
    const ok = await confirm({
      title: '确认修改既有槽位标识 (slot_id)？',
      content: `修改既有槽位 "${currentSlotId}" 的标识会破坏跨修订章节映射，可能导致已发布文档产生 Spec Drift 或将原锁定章节转入「孤立章节待处置区」。确定要解锁编辑该 slot_id 吗？`,
      confirmText: '了解风险，解锁修改',
      cancelText: '保持原标识',
      type: 'warning',
    });
    if (ok) {
      setUnlockedSlotIndices((prev) => ({ ...prev, [index]: true }));
    }
  };

  const handleRemoveSlot = async (index: number) => {
    const target = slots[index];
    if (!target) return;
    if (persistedSlotIds.has(target.slot_id)) {
      const ok = await confirm({
        title: `确认移除既有槽位 ${target.slot_id}？`,
        content:
          '移除已发布模板中的既有槽位会触发 Spec Drift；既有文档中该槽位若已被人工锁定，重生成时将自动转入孤立章节保护区。',
        confirmText: '确认移除',
        cancelText: '取消',
        type: 'warning',
      });
      if (!ok) return;
    }
    onChange(slots.filter((_, i) => i !== index));
  };

  const handleMove = (index: number, dir: -1 | 1) => {
    const nextIdx = index + dir;
    if (nextIdx < 0 || nextIdx >= slots.length) return;
    const next = [...slots];
    const [moved] = next.splice(index, 1);
    next.splice(nextIdx, 0, moved);
    onChange(next);
  };

  return (
    <div className="code-archdoc-slots-builder">
      <div className="code-archdoc-slots-toolbar">
        <div className="code-archdoc-slots-hint">
          <AlertTriangle size={14} />
          <span>
            每个槽位的 <code>slot_id</code> 须匹配 <code>^[a-z][a-z0-9_]&#123;1,48&#125;$</code>{' '}
            且在 <code>template.md</code> 中存在对应 <code>&lt;!-- slot:id --&gt;</code> 标记。
          </span>
        </div>

        <div className="code-archdoc-slots-toolbar-actions">
          {onSyncTemplateMarkers && (
            <button
              type="button"
              className="btn btn-outline btn-small"
              onClick={onSyncTemplateMarkers}
              title="自动将缺失的槽位占位标记追加至 template.md 末尾"
            >
              <span>补全模板占位标记</span>
            </button>
          )}
          <button type="button" className="btn btn-primary btn-small" onClick={handleAddSlot}>
            <Plus size={14} />
            <span>新增章节槽位</span>
          </button>
        </div>
      </div>

      {slots.length === 0 ? (
        <div className="code-archdoc-slots-empty">
          至少需要定义 1 个章节槽位，请点击右上角「新增章节槽位」。
        </div>
      ) : (
        <div className="code-archdoc-slots-list">
          {slots.map((slot, index) => {
            const isPersistedLocked =
              persistedSlotIds.has(slot.slot_id) && !unlockedSlotIndices[index];
            return (
              <div key={`${index}-${slot.slot_id}`} className="code-archdoc-slot-item">
                <div className="code-archdoc-slot-item-head">
                  <span className="code-badge code-badge--primary">槽位 #{index + 1}</span>
                  <div className="code-archdoc-slot-item-ops">
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      disabled={index === 0}
                      onClick={() => handleMove(index, -1)}
                      title="上移"
                      aria-label="上移槽位"
                    >
                      <ArrowUp size={14} />
                    </button>
                    <button
                      type="button"
                      className="btn btn-text btn-small"
                      disabled={index === slots.length - 1}
                      onClick={() => handleMove(index, 1)}
                      title="下移"
                      aria-label="下移槽位"
                    >
                      <ArrowDown size={14} />
                    </button>
                    <button
                      type="button"
                      className="btn btn-text btn-small code-archdoc-btn-danger-text"
                      disabled={slots.length <= 1}
                      onClick={() => void handleRemoveSlot(index)}
                      title={slots.length <= 1 ? '至少保留 1 个槽位' : '删除此槽位'}
                      aria-label="删除槽位"
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                </div>

                <div className="code-archdoc-slot-grid">
                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">
                      槽位标识 (slot_id)
                      {isPersistedLocked && (
                        <button
                          type="button"
                          className="code-archdoc-slot-unlock-btn"
                          onClick={() => void handleUnlockSlotId(index, slot.slot_id)}
                          title="既有槽位标识默认锁定以防误改，点击确认风险后解锁"
                        >
                          <Lock size={11} />
                          <span>解锁修改</span>
                        </button>
                      )}
                      {persistedSlotIds.has(slot.slot_id) && unlockedSlotIndices[index] && (
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
                      onChange={(e) => handleUpdateSlot(index, { slot_id: e.target.value.trim() })}
                    />
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">章节标题</label>
                    <input
                      type="text"
                      className="code-archdoc-input"
                      value={slot.title}
                      placeholder="例如: 1. 核心业务链路与时序图"
                      onChange={(e) => handleUpdateSlot(index, { title: e.target.value })}
                    />
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">默认所有权</label>
                    <select
                      className="code-archdoc-select"
                      value={slot.ownership}
                      onChange={(e) =>
                        handleUpdateSlot(index, { ownership: e.target.value as SectionOwnership })
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
                    <label className="code-archdoc-field-label">Token 预算</label>
                    <input
                      type="number"
                      className="code-archdoc-input"
                      min={500}
                      max={64000}
                      value={slot.token_budget}
                      onChange={(e) =>
                        handleUpdateSlot(index, {
                          token_budget: Number.parseInt(e.target.value, 10) || 8000,
                        })
                      }
                    />
                  </div>

                  <div className="code-archdoc-form-field">
                    <label className="code-archdoc-field-label">最少有效字符数</label>
                    <input
                      type="number"
                      className="code-archdoc-input"
                      min={0}
                      max={10000}
                      value={slot.min_chars}
                      onChange={(e) =>
                        handleUpdateSlot(index, {
                          min_chars: Number.parseInt(e.target.value, 10) || 0,
                        })
                      }
                    />
                  </div>

                  <div className="code-archdoc-form-field code-archdoc-form-field--checkbox">
                    <label className="code-archdoc-checkbox-label">
                      <input
                        type="checkbox"
                        checked={slot.required}
                        onChange={(e) => handleUpdateSlot(index, { required: e.target.checked })}
                      />
                      <span>必填章节（纳入 Verifier 完备度门禁）</span>
                    </label>
                  </div>
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">订阅 Code IR 分片 (ir_facets)</label>
                  <div className="code-archdoc-facet-chips">
                    {ALL_FACET_OPTIONS.map((opt) => {
                      const active = slot.ir_facets.includes(opt.value);
                      return (
                        <button
                          key={opt.value}
                          type="button"
                          className={`code-archdoc-facet-chip ${
                            active ? 'code-archdoc-facet-chip--active' : ''
                          }`}
                          onClick={() => handleToggleFacet(index, opt.value)}
                        >
                          {opt.label}
                        </button>
                      );
                    })}
                  </div>
                </div>

                <div className="code-archdoc-form-field">
                  <label className="code-archdoc-field-label">槽位写作要点提示（可选）</label>
                  <input
                    type="text"
                    className="code-archdoc-input"
                    value={slot.instruction_hint ?? ''}
                    placeholder="例如：必须输出 Mermaid sequenceDiagram 并标注超时参数来源文件"
                    onChange={(e) => handleUpdateSlot(index, { instruction_hint: e.target.value })}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};

export default SlotsSchemaBuilder;
