import type {
  DepartmentVO,
  DocTypeDetailVO,
  DocTypeSummaryVO,
  ItemsResponse,
  RepoCoverageListResponse,
  RepoListQuery,
  RepoScopesDetailResponse,
} from '../types';
import { apiRequest } from './client';

export async function listDepartments(): Promise<DepartmentVO[]> {
  const res = await apiRequest<ItemsResponse<DepartmentVO>>('/departments');
  return Array.isArray(res.items) ? res.items : [];
}

export async function listDocTypes(activeOnly = true): Promise<DocTypeSummaryVO[]> {
  const qs = activeOnly ? '?active_only=true' : '';
  const res = await apiRequest<ItemsResponse<DocTypeSummaryVO>>(`/doc-types${qs}`);
  return Array.isArray(res.items) ? res.items : [];
}

export async function getDocType(slug: string): Promise<DocTypeDetailVO> {
  return apiRequest<DocTypeDetailVO>(`/doc-types/${encodeURIComponent(slug)}`);
}

export async function listRepos(query: RepoListQuery): Promise<RepoCoverageListResponse> {
  const qs = new URLSearchParams({
    page: String(query.page),
    page_size: String(query.pageSize),
  });
  if (query.search?.trim()) qs.set('search', query.search.trim());
  if (query.departmentId?.trim()) qs.set('department_id', query.departmentId.trim());
  if (query.docType?.trim()) qs.set('doc_type', query.docType.trim());
  if (query.status && query.status !== 'all') qs.set('status', query.status);
  if (query.scopeType && query.scopeType !== 'all') qs.set('scope_type', query.scopeType);

  return apiRequest<RepoCoverageListResponse>(`/repos?${qs.toString()}`);
}

export async function getRepoScopes(repoId: number): Promise<RepoScopesDetailResponse> {
  return apiRequest<RepoScopesDetailResponse>(`/repos/${repoId}/scopes`);
}
