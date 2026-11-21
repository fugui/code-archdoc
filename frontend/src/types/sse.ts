import type {
  ChatIntentMode,
  ChatPlanLockedAdviceVO,
  ChatPlanTargetSlotVO,
  ChatProposalItemVO,
  ChatScopeType,
  ChatVerifierPreviewVO,
  EvidenceAnchor,
  ModelTier,
} from './domain';

export type ChatRefineStageName = 'planning' | 'refining_slot' | 'drafting_diff' | 'verifying';

// Chat 精修 SSE 强类型辨识联合（10 事件，严格对齐 docs/03-api/02-data-model-and-dto-contract.md §9 & ADR-06 §4.3）
export type ChatRefineSSEEvent =
  | {
      event: 'meta';
      data: {
        session_id: number;
        message_id: number;
        slot_id: string;
        mode?: ChatIntentMode;
        scope_type?: ChatScopeType;
        base_revision_no: number;
        model_tier: ModelTier;
      };
    }
  | {
      event: 'plan';
      data: {
        target_slots: ChatPlanTargetSlotVO[];
        skipped_locked_slots: ChatPlanLockedAdviceVO[];
        truncated_slots?: string[];
      };
    }
  | {
      event: 'stage';
      data: {
        stage: ChatRefineStageName;
        slot_id?: string;
        index?: number;
        total?: number;
      };
    }
  | {
      event: 'tool_call';
      data: {
        round: number;
        tool_name: string;
        summary: string;
        is_error: boolean;
        duration_ms: number;
        slot_id?: string;
      };
    }
  | {
      event: 'delta';
      data: {
        text: string;
        slot_id?: string;
      };
    }
  | {
      event: 'proposal_item';
      data: ChatProposalItemVO;
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
