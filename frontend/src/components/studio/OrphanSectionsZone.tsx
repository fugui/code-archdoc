import React, { useState } from 'react';
import { AlertTriangle, GitMerge, Lock, Trash2 } from 'lucide-react';
import { useConfirm } from '@code/common';
import type { OrphanResolveAction, SectionVO } from '../../types';
import SectionMarkdownRenderer from './SectionMarkdownRenderer';

export interface OrphanSectionsZoneProps {
  orphanSections: SectionVO[];
  regularSections: SectionVO[];
  canWrite: boolean;
  onResolveOrphan: (
    slotId: string,
    action: OrphanResolveAction,
    targetSlotId?: string,
  ) => Promise<void>;
}

export const OrphanSectionsZone: React.FC<OrphanSectionsZoneProps> = ({
  orphanSections,
  regularSections,
  canWrite,
  onResolveOrphan,
}) => {
  const confirm = useConfirm();
  const [mergeTargets, setMergeTargets] = useState<Record<string, string>>({});
  const [busySlotId, setBusySlotId] = useState<string | null>(null);

  if (orphanSections.length === 0) return null;

  const defaultTarget = regularSections[0]?.slot_id ?? '';

  const runAction = async (slot: SectionVO, action: OrphanResolveAction) => {
    if (action === 'delete') {
      const ok = await confirm({
        title: '确认永久删除孤立章节',
        content: `即将删除因模板升级产生的孤立章节「${slot.title} (${slot.slot_id})」，操作将生成新修订记录。是否继续？`,
        confirmText: '确认删除',
        cancelText: '取消',
        type: 'danger',
      });
      if (!ok) return;
    }

    const targetSlotId = action === 'merge_into' ? mergeTargets[slot.slot_id] || defaultTarget : undefined;
    if (action === 'merge_into' && !targetSlotId) {
      return;
    }

    setBusySlotId(slot.slot_id);
    try {
      await onResolveOrphan(slot.slot_id, action, targetSlotId);
    } finally {
      setBusySlotId(null);
    }
  };

  return (
    <section className="code-archdoc-orphan-zone">
      <div className="code-archdoc-orphan-header">
        <div className="flex-center gap-sm">
          <AlertTriangle size={17} className="code-archdoc-text-warning" />
          <h3 className="code-archdoc-orphan-title">
            模板升级孤立章节保护区 ({orphanSections.length})
          </h3>
        </div>
        <p className="code-archdoc-orphan-desc">
          以下章节曾包含人工修订或锁定内容，但已被新版 DocType 模板移除。系统已自动保护避免静默丢弃，请负责人按需保留、合并或删除。
        </p>
      </div>

      <div className="code-archdoc-orphan-list">
        {orphanSections.map((sec) => {
          const isBusy = busySlotId === sec.slot_id;
          const selectedTarget = mergeTargets[sec.slot_id] || defaultTarget;

          return (
            <div key={sec.slot_id} id={`section-${sec.slot_id}`} className="code-card code-archdoc-orphan-card">
              <div className="code-archdoc-orphan-card-head">
                <div className="flex-center gap-sm">
                  <strong>{sec.title}</strong>
                  <code className="code-archdoc-mono">{sec.slot_id}</code>
                  <span className="code-badge code-badge--orange">孤立章节</span>
                </div>

                {canWrite && (
                  <div className="code-archdoc-orphan-actions">
                    <button
                      type="button"
                      className="btn btn-secondary btn-small"
                      disabled={isBusy}
                      onClick={() => void runAction(sec, 'keep_locked')}
                    >
                      <Lock size={13} />
                      <span>保留为锁定章节</span>
                    </button>

                    {regularSections.length > 0 && (
                      <div className="code-archdoc-orphan-merge">
                        <select
                          className="code-select code-archdoc-orphan-select"
                          value={selectedTarget}
                          onChange={(e) =>
                            setMergeTargets((prev) => ({ ...prev, [sec.slot_id]: e.target.value }))
                          }
                          aria-label="选择合并目标章节"
                        >
                          {regularSections.map((target) => (
                            <option key={target.slot_id} value={target.slot_id}>
                              合并至：{target.title}
                            </option>
                          ))}
                        </select>
                        <button
                          type="button"
                          className="btn btn-outline btn-small"
                          disabled={isBusy || !selectedTarget}
                          onClick={() => void runAction(sec, 'merge_into')}
                        >
                          <GitMerge size={13} />
                          <span>合并</span>
                        </button>
                      </div>
                    )}

                    <button
                      type="button"
                      className="btn btn-danger btn-small"
                      disabled={isBusy}
                      onClick={() => void runAction(sec, 'delete')}
                    >
                      <Trash2 size={13} />
                      <span>删除</span>
                    </button>
                  </div>
                )}
              </div>

              <div className="code-archdoc-section-body">
                <SectionMarkdownRenderer markdown={sec.markdown} />
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
};

export default OrphanSectionsZone;
