import type {
  CreateReviewCommentPayload,
  ItemsResponse,
  ReviewCommentFilterQuery,
  ReviewCommentVO,
  UpdateReviewCommentPayload,
} from '../types';
import { apiRequest } from './client';

export async function listReviewComments(
  docId: number,
  filter: ReviewCommentFilterQuery = {},
): Promise<ReviewCommentVO[]> {
  const params = new URLSearchParams();
  if (filter.status) {
    params.set('status', filter.status);
  }
  if (filter.scope_type) {
    params.set('scope_type', filter.scope_type);
  }
  if (filter.slot_id) {
    params.set('slot_id', filter.slot_id);
  }
  if (typeof filter.persist_for_regen === 'boolean') {
    params.set('persist_for_regen', String(filter.persist_for_regen));
  }
  const qs = params.toString();
  const res = await apiRequest<ItemsResponse<ReviewCommentVO>>(
    `/documents/${docId}/review-comments${qs ? `?${qs}` : ''}`,
  );
  return Array.isArray(res.items) ? res.items : [];
}

export async function createReviewComment(
  docId: number,
  payload: CreateReviewCommentPayload,
): Promise<ReviewCommentVO> {
  return apiRequest<ReviewCommentVO>(`/documents/${docId}/review-comments`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function updateReviewComment(
  docId: number,
  commentId: number,
  payload: UpdateReviewCommentPayload,
): Promise<ReviewCommentVO> {
  return apiRequest<ReviewCommentVO>(`/documents/${docId}/review-comments/${commentId}`, {
    method: 'PATCH',
    bodyData: payload,
  });
}

export async function deleteReviewComment(
  docId: number,
  commentId: number,
): Promise<void> {
  await apiRequest<void>(`/documents/${docId}/review-comments/${commentId}`, {
    method: 'DELETE',
  });
}
