import type { ChatVerifierPreviewVO, EvidenceAnchor, ModelTier } from './domain';

// Chat 精修 SSE 强类型辨识联合（严格对齐 docs/03-api/02-data-model-and-dto-contract.md §6，W2 启用）
export type ChatRefineSSEEvent =
  | {
      event: 'meta';
      data: {
        session_id: number;
        message_id: number;
        slot_id: string;
        base_revision_no: number;
        model_tier: ModelTier;
      };
    }
  | {
      event: 'delta';
      data: {
        text: string;
      };
    }
  | {
      event: 'diff_proposal';
      data: {
        proposal_id: number;
        slot_id: string;
        before_md: string;
        after_md: string;
        unified_diff: string;
        cited_anchors: EvidenceAnchor[];
      };
    }
  | {
      event: 'verifier_preview';
      data: ChatVerifierPreviewVO;
    }
  | {
      event: 'done';
      data: {
        message_id: number;
        prompt_tokens: number;
        cached_prompt_tokens: number;
        completion_tokens: number;
        latency_ms: number;
      };
    }
  | {
      event: 'error';
      data: {
        code: string;
        message: string;
      };
    };
