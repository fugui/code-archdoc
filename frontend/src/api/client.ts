import { AUTH_TOKEN_KEY } from '@code/common';
import { MODULE_BASE_PATH } from '../constants';
import type { ApiErrorBody } from '../types';
import { ArchdocApiError } from '../utils/errorMessages';

export interface ArchdocRequestOptions extends Omit<RequestInit, 'body'> {
  bodyData?: unknown;
  body?: BodyInit | null;
}

/**
 * 动态识别当前运行环境：
 * - 门户嵌入态（window.__POWERED_BY_PORTAL__ === true）走 /archdoc/api；
 * - 独立调试态走 /api。
 */
export function getApiBaseUrl(): string {
  if (typeof window !== 'undefined' && Boolean(window.__POWERED_BY_PORTAL__)) {
    return `${MODULE_BASE_PATH}/api`;
  }
  return '/api';
}

function buildAuthHeaders(extra?: HeadersInit, hasJsonBody = false): Record<string, string> {
  // TODO(security): 平台微前端统一鉴权复用 CodeBench 门户共享 JWT 存储键 (AUTH_TOKEN_KEY)；若升级为全站 HttpOnly Cookie 会话请同步移除此处读取。
  const token = typeof localStorage !== 'undefined' ? localStorage.getItem(AUTH_TOKEN_KEY) : null;
  const headers: Record<string, string> = {};
  if (hasJsonBody) {
    headers['Content-Type'] = 'application/json';
  }
  if (extra) {
    const incoming = new Headers(extra);
    incoming.forEach((value, key) => {
      headers[key] = value;
    });
  }
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

async function parseErrorBody(response: Response): Promise<Partial<ApiErrorBody>> {
  try {
    const raw = (await response.json()) as unknown;
    if (raw && typeof raw === 'object') {
      return raw as Partial<ApiErrorBody>;
    }
  } catch {
    // 非 JSON 错误体按状态码回退
  }
  return {};
}

/**
 * 强类型模块 API 请求封装：统一解析 { error, code, details } 并抛出 ArchdocApiError。
 */
export async function apiRequest<T>(
  endpoint: string,
  options: ArchdocRequestOptions = {},
): Promise<T> {
  const { bodyData, headers, body, ...rest } = options;
  const hasJsonBody = bodyData !== undefined;

  const response = await fetch(`${getApiBaseUrl()}${endpoint}`, {
    ...rest,
    headers: buildAuthHeaders(headers, hasJsonBody),
    body: hasJsonBody ? JSON.stringify(bodyData) : body,
  });

  if (response.status === 204) {
    return undefined as T;
  }

  if (!response.ok) {
    const errBody = await parseErrorBody(response);
    throw new ArchdocApiError(response.status, errBody);
  }

  return (await response.json()) as T;
}

function extractFilenameFromDisposition(disposition: string | null, fallback: string): string {
  if (!disposition) return fallback;
  const utf8Match = /filename\*=UTF-8''([^;\s]+)/i.exec(disposition);
  if (utf8Match && utf8Match[1]) {
    try {
      return decodeURIComponent(utf8Match[1]);
    } catch {
      return fallback;
    }
  }
  const plainMatch = /filename="?([^";\r\n]+)"?/i.exec(disposition);
  if (plainMatch && plainMatch[1]) {
    return plainMatch[1].trim();
  }
  return fallback;
}

/**
 * 触发浏览器二进制文件下载（用于单篇 Markdown 导出与批量 ZIP 导出）。
 */
export async function apiDownloadFile(
  endpoint: string,
  fallbackFilename: string,
  options: ArchdocRequestOptions = {},
): Promise<string> {
  const { bodyData, headers, body, ...rest } = options;
  const hasJsonBody = bodyData !== undefined;

  const response = await fetch(`${getApiBaseUrl()}${endpoint}`, {
    ...rest,
    headers: buildAuthHeaders(headers, hasJsonBody),
    body: hasJsonBody ? JSON.stringify(bodyData) : body,
  });

  if (!response.ok) {
    const errBody = await parseErrorBody(response);
    throw new ArchdocApiError(response.status, errBody, '导出文件失败，请稍后重试');
  }

  const blob = await response.blob();
  const filename = extractFilenameFromDisposition(
    response.headers.get('Content-Disposition'),
    fallbackFilename,
  );

  const objectUrl = URL.createObjectURL(blob);
  try {
    const link = document.createElement('a');
    link.href = objectUrl;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
  } finally {
    window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1000);
  }

  return filename;
}
