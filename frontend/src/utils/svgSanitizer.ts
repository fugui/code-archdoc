/**
 * Mermaid SVG 安全解析与缩放视口计算纯函数工具。
 * 通过 DOMParser 解析并移除 script/iframe/object/embed、on* 事件与 javascript: 协议，零 innerHTML 注入。
 */

export const MIN_ZOOM = 0.25;
export const MAX_ZOOM = 4;
export const ZOOM_STEP = 0.25;
export const WHEEL_ZOOM_FACTOR = 0.12;

const FORBIDDEN_TAGS = new Set(['script', 'iframe', 'object', 'embed']);

export interface Point2D {
  x: number;
  y: number;
}

export function clampZoom(scale: number): number {
  if (!Number.isFinite(scale)) return 1;
  const bounded = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, scale));
  return Math.round(bounded * 100) / 100;
}

export function zoomByStep(scale: number, delta: number): number {
  return clampZoom(scale + delta);
}

export function zoomByWheel(scale: number, deltaY: number): number {
  if (deltaY === 0) return clampZoom(scale);
  const factor = deltaY < 0 ? 1 + WHEEL_ZOOM_FACTOR : 1 - WHEEL_ZOOM_FACTOR;
  return clampZoom(scale * factor);
}

export function formatZoomPercent(scale: number): string {
  return `${Math.round(clampZoom(scale) * 100)}%`;
}

/**
 * 使用 DOMParser 将 Mermaid 渲染的 SVG 字符串安全解析为清洗后的 SVGSVGElement。
 */
export function parseAndSanitizeSvg(svgMarkup: string): SVGSVGElement | null {
  if (typeof DOMParser === 'undefined' || !svgMarkup.trim()) {
    return null;
  }
  const parser = new DOMParser();
  const parsed = parser.parseFromString(svgMarkup, 'image/svg+xml');
  if (parsed.querySelector('parsererror')) {
    return null;
  }
  const root = parsed.documentElement;
  if (!root || root.tagName.toLowerCase() !== 'svg') {
    return null;
  }

  const allNodes = [root, ...Array.from(root.querySelectorAll('*'))];
  for (const node of allNodes) {
    if (FORBIDDEN_TAGS.has(node.tagName.toLowerCase())) {
      node.remove();
      continue;
    }
    const attrs = Array.from(node.attributes);
    for (const attr of attrs) {
      const nameLower = attr.name.toLowerCase();
      const valLower = attr.value.trim().toLowerCase();
      if (nameLower.startsWith('on')) {
        node.removeAttribute(attr.name);
      } else if (
        (nameLower === 'href' || nameLower === 'xlink:href' || nameLower === 'src') &&
        (valLower.startsWith('javascript:') || valLower.startsWith('vbscript:') || valLower.startsWith('data:text/html'))
      ) {
        node.removeAttribute(attr.name);
      }
    }
  }

  return document.importNode(root, true) as unknown as SVGSVGElement;
}
