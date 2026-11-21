import React, { useMemo } from 'react';
import { BookOpen, Sparkles } from 'lucide-react';
import { StatusTag, type StatusType } from '@code/common';
import type { DocStatusSummaryVO, DocTypeSummaryVO, RepoScopeCoverageVO } from '../../types';
import { getDocTypeShortName, groupDocTypesByCategory } from '../../utils/docTypeUtils';

export interface ScopeDocExpandedGridProps {
  row: RepoScopeCoverageVO;
  docTypes: DocTypeSummaryVO[];
  focusedDocTypeSlug?: string;
  onOpenStudio: (docId?: number) => void;
  onTriggerGenerate: (docTypeSlug: string) => void;
}

function expandedDocStatusMeta(ds?: DocStatusSummaryVO): { status: StatusType; label: string } {
  if (!ds) {
    return { status: 'neutral', label: '未生成' };
  }
  if (ds.status === 'approved') {
    if (ds.has_code_drift || ds.has_spec_drift) {
      return { status: 'warning', label: `已发布·漂移(${ds.affected_slot_count || 1})` };
    }
    return {
      status: 'success',
      label: `已发布 v${ds.published_revision_no || ds.current_revision_no}`,
    };
  }
  if (!ds.can_approve) {
    return { status: 'failed', label: `草稿·阻断 v${ds.current_revision_no}` };
  }
  return { status: 'pending', label: `草稿 v${ds.current_revision_no}` };
}

export const ScopeDocExpandedGrid: React.FC<ScopeDocExpandedGridProps> = ({
  row,
  docTypes,
  focusedDocTypeSlug,
  onOpenStudio,
  onTriggerGenerate,
}) => {
  const groups = useMemo(() => groupDocTypesByCategory(docTypes), [docTypes]);

  if (docTypes.length === 0) {
    return (
      <div className="code-archdoc-expanded-panel text-muted">暂无启用中的文档类型定义。</div>
    );
  }

  return (
    <div className="code-archdoc-expanded-panel">
      {groups.map((group) => (
        <div key={group.category} className="code-archdoc-expanded-group">
          <div className="code-archdoc-expanded-group-head">
            <span className="code-archdoc-expanded-group-title">{group.category}</span>
            <span className="code-badge code-badge--muted">{group.items.length} 类</span>
          </div>

          <div className="code-archdoc-expanded-grid">
            {group.items.map((dt) => {
              const ds = row.doc_statuses?.[dt.slug];
              const meta = expandedDocStatusMeta(ds);
              const shortName = getDocTypeShortName(dt);
              const isFocused = Boolean(focusedDocTypeSlug && focusedDocTypeSlug === dt.slug);

              return (
                <div
                  key={dt.slug}
                  className={`code-archdoc-expanded-card ${
                    isFocused ? 'code-archdoc-expanded-card--focused' : ''
                  }`}
                >
                  <div className="code-archdoc-expanded-card-head">
                    <div className="code-archdoc-expanded-card-title-wrap">
                      <span className="code-archdoc-expanded-card-title" title={dt.display_name}>
                        {dt.display_name}
                      </span>
                      {shortName && shortName !== dt.display_name && (
                        <span className="code-archdoc-expanded-card-short">({shortName})</span>
                      )}
                    </div>
                    <StatusTag status={meta.status} text={meta.label} size="sm" />
                  </div>

                  {dt.description && (
                    <div className="code-archdoc-expanded-card-desc" title={dt.description}>
                      {dt.description}
                    </div>
                  )}

                  <div className="code-archdoc-expanded-card-foot">
                    <div className="code-archdoc-expanded-card-meta">
                      <span className="code-badge code-badge--muted">{dt.model_tier}</span>
                      <span className="text-muted">{dt.slot_count} 槽位</span>
                    </div>

                    {ds ? (
                      <button
                        type="button"
                        className="code-archdoc-mini-btn code-archdoc-mini-btn--primary"
                        onClick={() => onOpenStudio(ds.document_id)}
                      >
                        <BookOpen size={12} />
                        <span>打开工作台</span>
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="code-archdoc-mini-btn"
                        disabled={!row.can_write}
                        title={
                          row.can_write
                            ? `触发生成《${dt.display_name}》`
                            : '仅仓库/子目录负责人或管理员可触发生成'
                        }
                        onClick={() => onTriggerGenerate(dt.slug)}
                      >
                        <Sparkles size={12} />
                        <span>立即生成</span>
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
};

export default ScopeDocExpandedGrid;
