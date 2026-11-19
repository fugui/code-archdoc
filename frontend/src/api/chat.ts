import { AUTH_TOKEN_KEY } from '@code/common';
import type {
  AcceptChatProposalPayload,
  ApiErrorBody,
  ChatRefinePayload,
  ChatRefineSSEEvent,
  ChatRejectResultVO,
  ChatSessionWithMessagesVO,
  DocumentDetailVO,
  ItemsResponse,
} from '../types';
import { ArchdocApiError } from '../utils/errorMessages';
import { parseChatSSEBlock } from '../utils/sseParser';
import { apiRequest, getApiBaseUrl } from './client';

export { parseChatSSEBlock };

export async function listChatSessions(docId: number): Promise<ChatSessionWithMessagesVO[]> {
  const res = await apiRequest<ItemsResponse<ChatSessionWithMessagesVO>>(
    `/documents/${docId}/chat/sessions`,
  );
  return Array.isArray(res.items) ? res.items : [];
}

export async function acceptChatProposal(
  docId: number,
  msgId: number,
  payload: AcceptChatProposalPayload,
): Promise<DocumentDetailVO> {
  return apiRequest<DocumentDetailVO>(`/documents/${docId}/chat/messages/${msgId}/accept`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function rejectChatProposal(
  docId: number,
  msgId: number,
): Promise<ChatRejectResultVO> {
  return apiRequest<ChatRejectResultVO>(`/documents/${docId}/chat/messages/${msgId}/reject`, {
    method: 'POST',
  });
}

export interface StreamChatRefineOptions {
  docId: number;
  payload: ChatRefinePayload;
  signal?: AbortSignal;
  onEvent: (ev: ChatRefineSSEEvent) => void;
}

/**
 * 使用 fetch + ReadableStream 消费标准 SSE 事件流，支持 AbortController 实时中断上游 LLM 推理。
 */
export async function streamChatRefine(options: StreamChatRefineOptions): Promise<void> {
  const { docId, payload, signal, onEvent } = options;

  // TODO(security): 复用 CodeBench 门户共享 JWT 键 (AUTH_TOKEN_KEY)，严禁通过 URL Query 传递 Token。
  const token = typeof localStorage !== 'undefined' ? localStorage.getItem(AUTH_TOKEN_KEY) : null;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'text/event-stream',
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  const response = await fetch(`${getApiBaseUrl()}/documents/${docId}/chat/refine`, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
    signal,
  });

  if (!response.ok) {
    let errBody: Partial<ApiErrorBody> = {};
    try {
      const raw = (await response.json()) as unknown;
      if (raw && typeof raw === 'object') {
        errBody = raw as Partial<ApiErrorBody>;
      }
    } catch {
      // 非 JSON 错误体按 HTTP 状态码回退
    }
    throw new ArchdocApiError(response.status, errBody);
  }

  if (!response.body) {
    throw new Error('浏览器未返回可读 SSE 响应流');
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8');
  let buffer = '';

  try {
    let streamDone = false;
    while (!streamDone) {
      const { value, done } = await reader.read();
      if (done) {
        streamDone = true;
        break;
      }
      buffer += decoder.decode(value, { stream: true });

      // 按标准空行分隔符（\n\n 或 \r\n\r\n）切片消费已完整帧
      const blocks = buffer.split(/\r?\n\r?\n/);
      buffer = blocks.pop() ?? '';

      for (const block of blocks) {
        const ev = parseChatSSEBlock(block);
        if (ev) {
          onEvent(ev);
        }
      }
    }

    buffer += decoder.decode();
    if (buffer.trim()) {
      const tailEv = parseChatSSEBlock(buffer);
      if (tailEv) {
        onEvent(tailEv);
      }
    }
  } finally {
    reader.releaseLock();
  }
}
