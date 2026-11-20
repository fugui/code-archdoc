import React from 'react';
import {
  AlertCircle,
  FileText,
  Folder,
  FolderGit2,
  GitBranch,
  Lock,
  RefreshCw,
  Sparkles,
  UserCog,
} from 'lucide-react';
import type { DocumentNavItemVO, RepoModuleVO, SectionOwnership, SectionVO } from '../../types';

export interface LeftDocTreePanelProps {
  docs: DocumentNavItemVO[];
  activeDocId: number | null;
  sections: SectionVO[];
  activeSlotId: string;
  canWrite: boolean;
  repoModules?: RepoModuleVO[];
  activeSubdirectory?: string;
  loadingModules?: boolean;
  refreshingModules?: boolean;
  canManageModules?: boolean;
  onSelectSubdirectory?: (subdir: string) => void;
  onRefreshModules?: () => void;
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
  repoModules = [],
  activeSubdirectory = '',
  loadingModules = false,
  refreshingModules = false,
  canManageModules = false,
  onSelectSubdirectory,
  onRefreshModules,
  onSelectDoc,
  onSelectSlot,
  onTriggerGenerate,
}) => {
  const normalizedActiveSubdir = activeSubdirectory.trim();
  const hasActiveSubdirInModules =
    !normalizedActiveSubdir ||
    repoModules.some((mod) => mod.subdirectory === normalizedActiveSubdir);

  return (
    <aside className="code-archdoc-studio-left">
      {/* 0. 代码范围切换器（整仓总览 <-> 一级业务目录） */}
      {onSelectSubdirectory && (
        <div className="code-archdoc-studio-panel-section">
          <div className="code-archdoc-studio-panel-header">
            <span>代码范围 ({repoModules.length + 1})</span>
            {canManageModules && onRefreshModules && (
              <button
                type="button"
                className="btn btn-text btn-small"
                disabled={refreshingModules || loadingModules}
                onClick={onRefreshModules}
                title="执行零 LLM 静态扫描，发现仓库一级业务目录"
              >
                <RefreshCw
                  size={12}
                  className={refreshingModules ? 'code-archdoc-spin' : ''}
                />
                <span>{refreshingModules ? '扫描中' : '扫描目录'}</span>
              </button>
            )}
          </div>

          <ul className="code-archdoc-doc-tree code-archdoc-scope-list">
            <li>
              <button
                type="button"
                className={`code-archdoc-doc-tree-item ${
                  normalizedActiveSubdir === '' ? 'code-archdoc-doc-tree-item--active' : ''
                }`}
                onClick={() => onSelectSubdirectory('')}
                title="查看整仓总览文档"
              >
                <FolderGit2 size={14} />
                <span className="code-archdoc-doc-tree-label text-truncate">整仓总览 (Root)</span>
              </button>
            </li>

            {!hasActiveSubdirInModules && (
              <li>
                <button
                  type="button"
                  className="code-archdoc-doc-tree-item code-archdoc-doc-tree-item--active"
                  onClick={() => onSelectSubdirectory(normalizedActiveSubdir)}
                  title={normalizedActiveSubdir}
                >
                  <Folder size={14} />
                  <span className="code-archdoc-doc-tree-label text-truncate">
                    {normalizedActiveSubdir}
                  </span>
                  <span className="code-badge code-badge--muted">当前</span>
                </button>
              </li>
            )}

            {repoModules.map((mod) => {
              const isSelected = normalizedActiveSubdir === mod.subdirectory;
              return (
                <li key={mod.id}>
                  <button
                    type="button"
                    className={`code-archdoc-doc-tree-item ${
                      isSelected ? 'code-archdoc-doc-tree-item--active' : ''
                    }`}
                    onClick={() => onSelectSubdirectory(mod.subdirectory)}
                    title={
                      mod.arch_element_name
                        ? `${mod.subdirectory} · ${mod.arch_element_name}`
                        : mod.subdirectory
                    }
                  >
                    <Folder size={14} />
                    <span className="code-archdoc-doc-tree-label text-truncate">
                      {mod.subdirectory}
                    </span>
                    <span className="code-archdoc-doc-tree-badges">
                      {mod.status === 'missing' ? (
                        <span
                          className="code-archdoc-dot code-archdoc-dot--warning"
                          title="该目录在最新提交中已缺失"
                        />
                      ) : mod.effective_files > 0 ? (
                        <span className="code-archdoc-scope-meta">{mod.effective_files}</span>
                      ) : null}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>

          {!loadingModules && repoModules.length === 0 && (
            <div className="code-archdoc-studio-panel-empty">
              暂无已发现子模块{canManageModules ? '，可点击右上角「扫描目录」发现一级目录' : ''}
            </div>
          )}
        </div>
      )}

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
