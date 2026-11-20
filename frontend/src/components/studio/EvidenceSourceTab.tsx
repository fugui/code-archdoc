import React, { useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, Code2, Crosshair, FileCode2, Loader2 } from 'lucide-react';
import { EmptyState } from '@code/common';
import { getSourcePreview } from '../../api/documents';
import type { SourcePreviewVO } from '../../types';
import { formatApiError } from '../../utils/errorMessages';
import { shortSha } from '../../utils/urlQuery';

export interface EvidenceInspectRequest {
  docId: number;
  commit?: string;
  file: string;
  symbol?: string;
  lineStart?: number;
  lineEnd?: number;
  confidence?: 'exact' | 'heuristic';
  scope?: 'module' | 'repo';
}

export interface EvidenceSourceTabProps {
  request: EvidenceInspectRequest | null;
}

export const EvidenceSourceTab: React.FC<EvidenceSourceTabProps> = ({ request }) => {
  const [loading, setLoading] = useState<boolean>(false);
  const [preview, setPreview] = useState<SourcePreviewVO | null>(null);
  const [errorTitle, setErrorTitle] = useState<string>('');
  const [errorMsg, setErrorMsg] = useState<string>('');

  const highlightAnchorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!request || !request.file) {
      setPreview(null);
      setErrorTitle('');
      setErrorMsg('');
      return;
    }

    let active = true;
    setLoading(true);
    setErrorTitle('');
    setErrorMsg('');

    void getSourcePreview(request.docId, {
      commit: request.commit,
      file: request.file,
      lineStart: request.lineStart,
      lineEnd: request.lineEnd,
      symbol: request.symbol,
      scope: request.scope,
    })
      .then((res) => {
        if (!active) return;
        setPreview(res);
      })
      .catch((err: unknown) => {
        if (!active) return;
        const formatted = formatApiError(err, '加载源码证据失败');
        setPreview(null);
        setErrorTitle(formatted.title);
        setErrorMsg(formatted.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
    };
  }, [request]);

  // 高亮目标行加载完毕后自动平滑滚动至视口中央
  useEffect(() => {
    if (!preview || loading) return;
    const node = highlightAnchorRef.current;
    if (node && typeof node.scrollIntoView === 'function') {
      node.scrollIntoView({ behavior: 'smooth', block: 'center' });
    }
  }, [preview, loading]);

  const codeLines = useMemo(() => {
    if (!preview) return [];
    return preview.content.replace(/\r\n/g, '\n').split('\n');
  }, [preview]);

  if (!request) {
    return (
      <EmptyState
        compact
        type="folder"
        icon={<Code2 size={32} />}
        title="点击正文中的源码证据标签"
        description="支持按快照 Commit 只读预览沙箱源码、精确高亮引用行区间及跨提交符号重定位。"
      />
    );
  }

  return (
    <div className="code-archdoc-evidence-tab">
      <div className="code-archdoc-evidence-meta">
        <div className="code-archdoc-evidence-path-row">
          <FileCode2 size={15} />
          <code className="code-archdoc-mono text-truncate" title={request.file}>
            {preview?.file_path || request.file}
          </code>
        </div>

        <div className="code-archdoc-evidence-tags">
          {request.scope === 'repo' && (
            <span className="code-badge code-badge--warning">跨模块契约 (scope=repo)</span>
          )}
          {(preview?.commit_sha || request.commit) && (
            <span className="code-badge code-badge--muted">
              Commit {shortSha(preview?.commit_sha || request.commit)}
            </span>
          )}
          {preview?.language && (
            <span className="code-badge code-badge--primary">{preview.language}</span>
          )}
          {request.symbol && (
            <span className="code-badge code-badge--purple" title={request.symbol}>
              {request.symbol}
            </span>
          )}
          {preview && preview.highlight_start > 0 && (
            <span className="code-badge code-badge--success">
              L{preview.highlight_start}
              {preview.highlight_end > preview.highlight_start ? `-${preview.highlight_end}` : ''}
            </span>
          )}
          {preview?.relocated_by_symbol && (
            <span className="code-badge code-badge--warning">
              <Crosshair size={11} />
              <span>已按符号重定位</span>
            </span>
          )}
        </div>
      </div>

      {loading && (
        <div className="code-archdoc-loading">
          <Loader2 size={16} className="code-archdoc-spin" />
          <span>正在读取只读源码快照...</span>
        </div>
      )}

      {!loading && errorMsg && (
        <div className="code-archdoc-alert code-archdoc-alert--danger">
          <AlertTriangle size={16} />
          <div className="code-archdoc-alert-body">
            <div className="code-archdoc-alert-title">{errorTitle || '无法加载源码证据'}</div>
            <div>{errorMsg}</div>
          </div>
        </div>
      )}

      {!loading && preview && (
        <div className="code-archdoc-source-viewer">
          {codeLines.map((lineText, idx) => {
            const lineNum = idx + 1;
            const isHighlighted =
              preview.highlight_start > 0 &&
              lineNum >= preview.highlight_start &&
              lineNum <= Math.max(preview.highlight_start, preview.highlight_end);
            const isFirstHighlight = preview.highlight_start > 0 && lineNum === preview.highlight_start;

            return (
              <div
                key={lineNum}
                ref={isFirstHighlight ? highlightAnchorRef : undefined}
                className={`code-archdoc-source-line ${
                  isHighlighted ? 'code-archdoc-source-line--highlight' : ''
                }`}
              >
                <span className="code-archdoc-source-lineno">{lineNum}</span>
                <span className="code-archdoc-source-code">{lineText || ' '}</span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};

export default EvidenceSourceTab;
