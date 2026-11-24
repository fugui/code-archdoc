import type {
  CreateRefDocPayload,
  EffectiveRefDocsQuery,
  EffectiveRefDocsResponse,
  PaginatedResponse,
  RefDocBindingPayload,
  RefDocBindingVO,
  RefDocDiffResponse,
  RefDocListQuery,
  RefDocVersionsResponse,
  ReferenceDocDetailVO,
  ReferenceDocSummaryVO,
  RefineRefDocPayload,
  RefineRefDocResponse,
  UpdateRefDocBindingPayload,
  UpdateRefDocPayload,
} from '../types';
import { apiRequest } from './client';

export async function listRefDocs(
  query: RefDocListQuery = {},
): Promise<PaginatedResponse<ReferenceDocSummaryVO>> {
  const qs = new URLSearchParams();
  if (query.page) qs.set('page', String(query.page));
  if (query.pageSize) qs.set('page_size', String(query.pageSize));
  if (query.search?.trim()) qs.set('search', query.search.trim());
  if (query.doc_kind?.trim()) qs.set('doc_kind', query.doc_kind.trim());
  if (query.scope_level?.trim()) qs.set('scope_level', query.scope_level.trim());
  if (query.arch_element_id) qs.set('arch_element_id', String(query.arch_element_id));
  if (query.status) qs.set('status', query.status);

  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return apiRequest<PaginatedResponse<ReferenceDocSummaryVO>>(`/ref-docs${suffix}`);
}

export async function getEffectiveRefDocs(
  query: EffectiveRefDocsQuery,
): Promise<EffectiveRefDocsResponse> {
  const qs = new URLSearchParams();
  if (query.arch_element_id) qs.set('arch_element_id', String(query.arch_element_id));
  if (query.repo_id) qs.set('repo_id', String(query.repo_id));
  if (query.subdirectory !== undefined && query.subdirectory !== '') {
    qs.set('subdirectory', query.subdirectory);
  }
  if (query.doc_type_slug?.trim()) qs.set('doc_type_slug', query.doc_type_slug.trim());

  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return apiRequest<EffectiveRefDocsResponse>(`/ref-docs/effective${suffix}`);
}

export async function getRefDocDetail(
  docId: number,
  version?: number,
): Promise<ReferenceDocDetailVO> {
  const suffix = version && version > 0 ? `?version=${version}` : '';
  return apiRequest<ReferenceDocDetailVO>(`/ref-docs/${docId}${suffix}`);
}

export async function createRefDoc(
  payload: CreateRefDocPayload,
  file?: File | null,
): Promise<ReferenceDocDetailVO> {
  if (file) {
    const formData = new FormData();
    formData.append('payload', JSON.stringify(payload));
    formData.append('file', file);
    return apiRequest<ReferenceDocDetailVO>('/ref-docs', {
      method: 'POST',
      body: formData,
    });
  }
  return apiRequest<ReferenceDocDetailVO>('/ref-docs', {
    method: 'POST',
    bodyData: payload,
  });
}

export async function updateRefDoc(
  docId: number,
  payload: UpdateRefDocPayload,
): Promise<ReferenceDocDetailVO> {
  return apiRequest<ReferenceDocDetailVO>(`/ref-docs/${docId}`, {
    method: 'PUT',
    bodyData: payload,
  });
}

export async function purgeRefDoc(docId: number): Promise<void> {
  await apiRequest<{ purged_id: number }>(`/admin/ref-docs/${docId}/purge`, {
    method: 'DELETE',
  });
}

export async function createRefDocBinding(
  docId: number,
  payload: RefDocBindingPayload,
): Promise<RefDocBindingVO> {
  return apiRequest<RefDocBindingVO>(`/ref-docs/${docId}/bindings`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function updateRefDocBinding(
  docId: number,
  bindingId: number,
  payload: UpdateRefDocBindingPayload,
): Promise<RefDocBindingVO> {
  return apiRequest<RefDocBindingVO>(`/ref-docs/${docId}/bindings/${bindingId}`, {
    method: 'PATCH',
    bodyData: payload,
  });
}

export async function deleteRefDocBinding(
  docId: number,
  bindingId: number,
): Promise<void> {
  await apiRequest<{ deleted: boolean }>(`/ref-docs/${docId}/bindings/${bindingId}`, {
    method: 'DELETE',
  });
}

export async function refineRefDoc(
  docId: number,
  payload: RefineRefDocPayload,
): Promise<RefineRefDocResponse> {
  return apiRequest<RefineRefDocResponse>(`/ref-docs/${docId}/refine`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function listRefDocVersions(docId: number): Promise<RefDocVersionsResponse> {
  return apiRequest<RefDocVersionsResponse>(`/ref-docs/${docId}/versions`);
}

export async function getRefDocDiff(
  docId: number,
  from?: number,
  to?: number,
): Promise<RefDocDiffResponse> {
  const qs = new URLSearchParams();
  if (from && from > 0) qs.set('from', String(from));
  if (to && to > 0) qs.set('to', String(to));
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return apiRequest<RefDocDiffResponse>(`/ref-docs/${docId}/diff${suffix}`);
}
