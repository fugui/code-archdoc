import React from 'react';
import {
  AlertCircle,
  FileText,
  GitBranch,
  Lock,
  Sparkles,
  UserCog,
} from 'lucide-react';
import type { DocumentNavItemVO, SectionOwnership, SectionVO } from '../../types';

export interface LeftDocTreePanelProps {
  docs: DocumentNavItemVO[];
  activeDocId: number | null;
  sections: SectionVO[];
  activeSlotId: string;
  canWrite: boolean;
  onSelectDoc: (docId: number) => void;
  onSelectSlot: (slotId: string) => void;
  onTriggerGenerate: () => void;
}

function OwnershipIcon({ ownership }: { ownership: SectionOwnership }) {
  switch (ownership) {
    case 'locked':
      return <Lock size={12} className="code-archdoc-own-icon--locked" />;
    case 'hybrid':
      return <UserCog size={12} className="code-archdoc-own-icon--hybrid" />;
    default:
      return <Sparkles size={12} className="code-archdoc-own-icon--auto" />;
  }
}

export const LeftDocTreePanel: React.FC<LeftDocTreePanelProps> = ({
  docs,
  activeDocId,
  sections,
  activeSlotId,
  canWrite,
  onSelectDoc,
  onSelectSlot,
  onTriggerGenerate,
}) => {
  return (
    <aside className="code-archdoc-studio-left">
      {/* 1. 当前范围 DocType 文档切换列表 */}
      <div className="code-archdoc-studio-panel-section">
        <div className="code-archdoc-studio-panel-header">
          <span>文档类型 ({docs.length})</span>
          {canWrite && (
            <button
              type="button"
              className="btn btn-text btn-small"
              onClick={onTriggerGenerate}
              title="为当前范围生成更多文档类型"
            >
              + 生成
            </button>
          )}
        </div>

        {docs.length === 0 ? (
          <div className="code-archdoc-studio-panel-empty">
            暂无可浏览文档{canWrite ? '，可点击右上角生成' : '（等待负责人发布）'}
          </div>
        ) : (
          <ul className="code-archdoc-doc-tree">
            {docs.map((item) => {
              const isActive = item.id === activeDocId;
              const hasDrift = item.has_code_drift || item.has_spec_drift;
              return (
                <li key={item.id}>
                  <button
                    type="button"
                    className={`code-archdoc-doc-tree-item ${isActive ? 'code-archdoc-doc-tree-item--active' : ''}`}
                    onClick={() => onSelectDoc(item.id)}
                  >
                    <FileText size={14} />
                    <span className="code-archdoc-doc-tree-label text-truncate">
                      {item.doc_type_name || item.doc_type_slug}
                    </span>
                    <span className="code-archdoc-doc-tree-badges">
                      {!item.can_approve && (
                        <span title="存在 Verifier 门禁阻断">
                          <AlertCircle size={13} className="code-archdoc-text-danger" />
                        </span>
                      )}
                      {hasDrift && (
                        <span title="存在代码或模板漂移">
                          <GitBranch size={12} className="code-archdoc-text-warning" />
                        </span>
                      )}
                      <span
                        className={`code-archdoc-dot ${
                          item.status === 'approved'
                            ? 'code-archdoc-dot--success'
                            : 'code-archdoc-dot--warning'
                        }`}
                        title={item.status === 'approved' ? '已发布' : '草稿'}
                      />
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {/* 2. 当前文档章节槽位大纲 (TOC) */}
      <div className="code-archdoc-studio-panel-section code-archdoc-studio-panel-section--grow">
        <div className="code-archdoc-studio-panel-header">
          <span>章节大纲 ({sections.length})</span>
        </div>

        {sections.length === 0 ? (
          <div className="code-archdoc-studio-panel-empty">暂无章节内容</div>
        ) : (
          <ul className="code-archdoc-slot-toc">
            {sections.map((sec) => {
              const isActive = sec.slot_id === activeSlotId;
              return (
                <li key={sec.slot_id}>
                  <button
                    type="button"
                    className={`code-archdoc-slot-toc-item ${isActive ? 'code-archdoc-slot-toc-item--active' : ''}`}
                    onClick={() => onSelectSlot(sec.slot_id)}
                    title={`${sec.title} (${sec.slot_id})`}
                  >
                    <OwnershipIcon ownership={sec.ownership} />
                    <span className="text-truncate">{sec.title}</span>
                    {sec.is_orphan && (
                      <span className="code-badge code-badge--orange">孤立</span>
                    )}
                    {sec.needs_drift_review && (
                      <span
                        className="code-archdoc-dot code-archdoc-dot--danger"
                        title="代码已变更，建议核对该锁定章节"
                      />
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </aside>
  );
};

export default LeftDocTreePanel;
