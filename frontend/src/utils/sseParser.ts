import type { ChatRefineSSEEvent } from '../types/sse';

/**
 * 解析单段标准 SSE 事件帧（event: xxx\ndata: {...}）为强类型 ChatRefineSSEEvent。
 */
export function parseChatSSEBlock(rawBlock: string): ChatRefineSSEEvent | null {
  const lines = rawBlock.split(/\r?\n/);
  let eventName = '';
  const dataLines: string[] = [];

  for (const line of lines) {
    if (!line || line.startsWith(':')) continue;
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trim());
    }
  }

  if (!eventName || dataLines.length === 0) {
    return null;
  }

  try {
    const parsed = JSON.parse(dataLines.join('\n')) as unknown;
    switch (eventName) {
      case 'meta':
      case 'plan':
      case 'stage':
      case 'tool_call':
      case 'delta':
      case 'proposal_item':
      case 'diff_proposal':
      case 'verifier_preview':
      case 'done':
      case 'error':
        return {
          event: eventName,
          data: parsed,
        } as ChatRefineSSEEvent;
      default:
        return null;
    }
  } catch {
    return null;
  }
}
