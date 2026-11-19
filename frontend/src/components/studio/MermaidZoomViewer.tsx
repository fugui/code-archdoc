import React, { useEffect, useRef, useState } from 'react';
import mermaid from 'mermaid';
import {
  AlertTriangle,
  Code,
  Eye,
  RotateCcw,
  ZoomIn,
  ZoomOut,
} from 'lucide-react';
import {
  ZOOM_STEP,
  formatZoomPercent,
  parseAndSanitizeSvg,
  zoomByStep,
  zoomByWheel,
  type Point2D,
} from '../../utils/svgSanitizer';

export interface MermaidZoomViewerProps {
  chart: string;
}

interface DragState {
  pointerId: number;
  startX: number;
  startY: number;
  originPan: Point2D;
}

// 串行渲染队列，避免同页多个 Mermaid 图表同时 initialize/render 产生竞态
let mermaidQueue: Promise<void> = Promise.resolve();
let mermaidSeq = 0;

function enqueueMermaid<T>(task: () => Promise<T>): Promise<T> {
  const next = mermaidQueue.then(task, task);
  mermaidQueue = next.then(
    () => undefined,
    () => undefined,
  );
  return next;
}

function detectLightTheme(): boolean {
  if (typeof document === 'undefined') return false;
  const root = document.documentElement;
  return root.getAttribute('data-theme') === 'light' || root.classList.contains('light-theme');
}

function cleanupMermaidArtifacts(renderId: string): void {
  document.getElementById(renderId)?.remove();
  document.getElementById(`d${renderId}`)?.remove();
}

export const MermaidZoomViewer: React.FC<MermaidZoomViewerProps> = ({ chart }) => {
  const [isLightTheme, setIsLightTheme] = useState<boolean>(() => detectLightTheme());
  const [viewMode, setViewMode] = useState<'diagram' | 'code'>('diagram');
  const [svgMarkup, setSvgMarkup] = useState<string>('');
  const [renderError, setRenderError] = useState<string>('');
  const [rendering, setRendering] = useState<boolean>(true);
  const [zoom, setZoom] = useState<number>(1);
  const [pan, setPan] = useState<Point2D>({ x: 0, y: 0 });
  const [isPanning, setIsPanning] = useState<boolean>(false);

  const svgHostRef = useRef<HTMLDivElement | null>(null);
  const dragRef = useRef<DragState | null>(null);

  // 监听 documentElement data-theme / class 变化，实时同步 Mermaid 亮/暗主题
  useEffect(() => {
    if (typeof document === 'undefined' || typeof MutationObserver === 'undefined') return;
    const observer = new MutationObserver(() => {
      setIsLightTheme(detectLightTheme());
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme', 'class'],
    });
    return () => observer.disconnect();
  }, []);

  // 串行调用 Mermaid 安全渲染（securityLevel: 'strict'）
  useEffect(() => {
    let active = true;
    const renderId = `archdoc-mermaid-${++mermaidSeq}`;
    setRendering(true);
    setRenderError('');
    setSvgMarkup('');

    const run = async () => {
      try {
        const rendered = await enqueueMermaid(async () => {
          if (!active) return '';
          mermaid.initialize({
            startOnLoad: false,
            theme: isLightTheme ? 'default' : 'dark',
            securityLevel: 'strict',
          });
          const { svg } = await mermaid.render(renderId, chart.trim());
          return svg;
        });

        if (!active) {
          cleanupMermaidArtifacts(renderId);
          return;
        }
        setSvgMarkup(rendered);
        setRenderError('');
      } catch (err: unknown) {
        cleanupMermaidArtifacts(renderId);
        if (active) {
          const msg = err instanceof Error && err.message ? err.message : 'Mermaid 语法解析失败';
          setRenderError(msg);
          setSvgMarkup('');
        }
      } finally {
        if (active) {
          setRendering(false);
        }
      }
    };

    void run();

    return () => {
      active = false;
      cleanupMermaidArtifacts(renderId);
    };
  }, [chart, isLightTheme]);

  // 通过 DOMParser 清洗并挂载 SVG，零 innerHTML / dangerouslySetInnerHTML
  useEffect(() => {
    const host = svgHostRef.current;
    if (!host) return;
    host.replaceChildren();
    if (!svgMarkup || viewMode !== 'diagram' || renderError) return;

    const svgNode = parseAndSanitizeSvg(svgMarkup);
    if (svgNode) {
      host.appendChild(svgNode);
    }
  }, [svgMarkup, viewMode, renderError]);

  const handleWheel = (e: React.WheelEvent<HTMLDivElement>) => {
    e.preventDefault();
    setZoom((prev) => zoomByWheel(prev, e.deltaY));
  };

  const handlePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    dragRef.current = {
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      originPan: pan,
    };
    setIsPanning(true);
  };

  const handlePointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== e.pointerId) return;
    setPan({
      x: drag.originPan.x + (e.clientX - drag.startX),
      y: drag.originPan.y + (e.clientY - drag.startY),
    });
  };

  const handlePointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== e.pointerId) return;
    dragRef.current = null;
    setIsPanning(false);
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId);
    }
  };

  const handleResetView = () => {
    setZoom(1);
    setPan({ x: 0, y: 0 });
  };

  return (
    <div className="code-archdoc-mermaid">
      <div className="code-archdoc-mermaid-toolbar">
        <div className="flex-center gap-sm">
          <span className="code-badge code-badge--primary">Mermaid 图表</span>
          {renderError && (
            <span className="code-badge code-badge--danger">
              <AlertTriangle size={12} />
              <span>渲染异常</span>
            </span>
          )}
        </div>

        <div className="code-archdoc-mermaid-controls">
          <button
            type="button"
            className={`btn btn-text btn-small ${viewMode === 'diagram' ? 'code-archdoc-mermaid-tab--active' : ''}`}
            onClick={() => setViewMode('diagram')}
          >
            <Eye size={13} />
            <span>图表</span>
          </button>
          <button
            type="button"
            className={`btn btn-text btn-small ${viewMode === 'code' ? 'code-archdoc-mermaid-tab--active' : ''}`}
            onClick={() => setViewMode('code')}
          >
            <Code size={13} />
            <span>源码</span>
          </button>

          {viewMode === 'diagram' && !renderError && (
            <>
              <button
                type="button"
                className="btn btn-secondary btn-small"
                onClick={() => setZoom((z) => zoomByStep(z, -ZOOM_STEP))}
                title="缩小"
                aria-label="缩小图表"
              >
                <ZoomOut size={13} />
              </button>
              <span className="code-archdoc-mono">{formatZoomPercent(zoom)}</span>
              <button
                type="button"
                className="btn btn-secondary btn-small"
                onClick={() => setZoom((z) => zoomByStep(z, ZOOM_STEP))}
                title="放大"
                aria-label="放大图表"
              >
                <ZoomIn size={13} />
              </button>
              <button
                type="button"
                className="btn btn-secondary btn-small"
                onClick={handleResetView}
                title="重置缩放与位置"
                aria-label="重置图表视图"
              >
                <RotateCcw size={13} />
              </button>
            </>
          )}
        </div>
      </div>

      {viewMode === 'code' || renderError ? (
        <div className="code-archdoc-mermaid-code-wrap">
          {renderError && (
            <div className="code-archdoc-alert code-archdoc-alert--warning">
              <AlertTriangle size={15} />
              <div className="code-archdoc-alert-body">
                Mermaid 渲染失败（{renderError}），已降级展示图表源码：
              </div>
            </div>
          )}
          <pre className="code-archdoc-code-block">
            <code>{chart}</code>
          </pre>
        </div>
      ) : (
        <div
          className={`code-archdoc-mermaid-viewport ${isPanning ? 'code-archdoc-mermaid-viewport--panning' : ''}`}
          onWheel={handleWheel}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerUp}
          onPointerCancel={handlePointerUp}
          title="滚轮缩放 · 按住左键拖拽平移"
        >
          {rendering && <div className="code-archdoc-mermaid-loading">正在渲染 Mermaid 图表...</div>}
          <div
            ref={svgHostRef}
            className="code-archdoc-mermaid-stage"
            style={{
              transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`,
            }}
          />
        </div>
      )}
    </div>
  );
};

export default MermaidZoomViewer;
