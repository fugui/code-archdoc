import type {
  DriftDocumentVO,
  DriftListQuery,
  DriftSummaryVO,
  GenTaskDetailVO,
  IncrementalRefreshPayload,
  PaginatedResponse,
  TriggerDriftPollResponseVO,
} from '../types';
import { apiRequest } from './client';

export async function getDriftSummary(): Promise<DriftSummaryVO> {
  return apiRequest<DriftSummaryVO>('/drift/summary');
}

export async function listDriftedDocuments(
  query: DriftListQuery,
): Promise<PaginatedResponse<DriftDocumentVO>> {
  const params = new URLSearchParams();
  params.set('page', String(query.page));
  params.set('page_size', String(query.pageSize));
  if (query.search?.trim()) {
    params.set('search', query.search.trim());
  }
  if (query.departmentId?.trim()) {
    params.set('department_id', query.departmentId.trim());
  }
  if (query.driftType && query.driftType !== 'all') {
    params.set('drift_type', query.driftType);
  }
  if (query.docType?.trim()) {
    params.set('doc_type', query.docType.trim());
  }
  return apiRequest<PaginatedResponse<DriftDocumentVO>>(`/drift/documents?${params.toString()}`);
}

export async function triggerIncrementalRefresh(
  documentId: number,
  payload?: IncrementalRefreshPayload,
): Promise<GenTaskDetailVO> {
  return apiRequest<GenTaskDetailVO>(`/documents/${documentId}/incremental-refresh`, {
    method: 'POST',
    bodyData: payload ?? {},
  });
}

export async function triggerAdminDriftPoll(): Promise<TriggerDriftPollResponseVO> {
  return apiRequest<TriggerDriftPollResponseVO>('/admin/drift/poll', {
    method: 'POST',
  });
}
