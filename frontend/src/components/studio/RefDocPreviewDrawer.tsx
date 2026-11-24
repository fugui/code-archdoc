import React, { useEffect, useState } from 'react';
import { AlertTriangle, BookOpen, Loader2 } from 'lucide-react';
import { Drawer } from '@code/common';
import { getRefDocDetail } from '../../api/refDocs';
import type { ReferenceDocDetailVO, RefDocCitationVO } from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import { formatRefDocKindLabel, formatRefDocScopeLabel } from '../../utils/refDocUtils';
import SectionMarkdownRenderer from './SectionMarkdownRenderer';

export interface RefDocPreviewTarget {
  docId: number;
  versionNo: number;
  sectionId?: string;
  sectionTitle?: string;
  docTitle?: string;
  stale?: boolean;
}

export interface RefDocPreviewDrawerProps {
  open: boolean;
  target: RefDocPreviewTarget | null;
  onClose: () => void;
}

export function citationToPreviewTarget(cit: RefDocCitationVO): RefDocPreviewTarget {
  return {
    docId: cit.doc_id,
    versionNo: cit.version_no,
    sectionId: cit.section_id,
    sectionTitle: cit.section_title,
    docTitle: cit.doc_title,
    stale: cit.stale,
  };
}

export const RefDocPreviewDrawer: React.FC<RefDocPreviewDrawerProps> = ({
  open,
  target,
  onClose,
}) => {
  const [loading, setLoading] = useState<boolean>(false);
  const [errorMsg, setErrorMsg] = useState<string>('');
  const [detail, setDetail] = useState<ReferenceDocDetailVO | null>(null);
  const [selectedSectionId, setSelectedSectionId] = useState<string>('');

  useEffect(() => {
    if (!open || !target) return;
    let active = true;
    setLoading(true);
    setErrorMsg('');
    setSelectedSectionId(target.sectionId || '');
    void getRefDocDetail(target.docId, target.versionNo > 0 ? target.versionNo : undefined)
      .then((res) => {
        if (!active) return;
        setDetail(res);
      })
      .catch((err: unknown) => {
        if (!active) return;
        setErrorMsg(formatApiError(err, '加载参考文档快照失败').message);
        setDetail(null);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [open, target]);

  if (!open || !target) {
    return null;
  }

  const activeTocNode =
    detail?.toc?.find((n) => n.section_id === selectedSectionId) ?? null;

  const displayMarkdown = (() => {
    if (!detail?.content_md) return '';
    if (!activeTocNode) return detail.content_md;
    const lines = detail.content_md.split('\n');
    const startIdx = Math.max(0, activeTocNode.start_line - 1);
    const endIdx = Math.min(lines.length, activeTocNode.end_line);
    if (startIdx >= lines.length || endIdx <= startIdx) {
      return detail.content_md;
    }
    return lines.slice(startIdx, endIdx).join('\n');
  })();

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width="min(640px, 92vw)"
      destroyOnClose
      bodyStyle={{ padding: 0, gap: 0 }}
      title={
        <div className="code-archdoc-refdoc-head-title">
          <BookOpen size={17} />
          <span>{detail?.title || target.docTitle || `参考文档 #${target.docId}`}</span>
          <span className="code-archdoc-mono">v{target.versionNo}</span>
          {detail && (
            <span className="code-badge code-badge--muted">
              {formatRefDocKindLabel(detail.doc_kind)}
            </span>
          )}
        </div>
      }
    >
      <div className="code-archdoc-refdoc-body">
          {target.stale && (
            <div className="code-archdoc-alert code-archdoc-alert--warning">
              <AlertTriangle size={16} />
              <div className="code-archdoc-alert-body">
                <div className="code-archdoc-alert-title">历史引用提示（已解绑或已归档）</div>
                <div>当前章节引用的该参考文档已从本作用域解绑或归档，此处展示历史锁定版本只读快照。</div>
              </div>
            </div>
          )}

          {loading ? (
            <div className="code-archdoc-loading">
              <Loader2 size={16} className="code-archdoc-spin" />
              <span>正在加载参考文档历史版本...</span>
            </div>
          ) : errorMsg ? (
            <div className="code-archdoc-alert code-archdoc-alert--danger">
              <AlertTriangle size={16} />
              <div className="code-archdoc-alert-body">{errorMsg}</div>
            </div>
          ) : detail ? (
            <div className="code-archdoc-refdoc-detail">
              <div className="code-archdoc-refdoc-detail-meta">
                <div className="text-muted">
                  归属作用域：{formatRefDocScopeLabel(detail.owner_scope_level)}（{detail.owner_scope_label}） · 当前版本 v{detail.current_version}
                </div>
                {detail.toc && detail.toc.length > 0 && (
                  <div className="code-archdoc-refdoc-toc-pills">
                    <button
                      type="button"
                      className={`code-archdoc-refdoc-toc-btn ${selectedSectionId === '' ? 'code-archdoc-refdoc-toc-btn--active' : ''}`}
                      onClick={() => setSelectedSectionId('')}
                    >
                      全文 ({detail.line_count} 行)
                    </button>
                    {detail.toc.map((node) => (
                      <button
                        key={node.section_id}
                        type="button"
                        className={`code-archdoc-refdoc-toc-btn ${selectedSectionId === node.section_id ? 'code-archdoc-refdoc-toc-btn--active' : ''}`}
                        onClick={() => setSelectedSectionId(node.section_id)}
                      >
                        {node.title}
                      </button>
                    ))}
                  </div>
                )}
              </div>

              <div className="code-archdoc-refdoc-markdown-view">
                <SectionMarkdownRenderer markdown={displayMarkdown} />
              </div>
            </div>
          ) : null}
        </div>
    </Drawer>
  );
};

export default RefDocPreviewDrawer;
