import type {
  ArchTreeQuery,
  ArchTreeResponse,
  DepartmentVO,
  DocTypeDetailVO,
  DocTypeSummaryVO,
  ItemsResponse,
  MigrateModuleResultVO,
  MigrateRepoModulePayload,
  RefreshModulesResultVO,
  RepoCoverageListResponse,
  RepoListQuery,
  RepoModulePolicyVO,
  RepoModuleVO,
  RepoModulesListVO,
  RepoScopesDetailResponse,
  UpdateRepoModulePolicyPayload,
  UpdateRepoModuleStatusPayload,
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
  if (query.subsystemId?.trim()) qs.set('subsystem_id', query.subsystemId.trim());
  if (query.archBinding && query.archBinding !== 'all') qs.set('arch_binding', query.archBinding);

  return apiRequest<RepoCoverageListResponse>(`/repos?${qs.toString()}`);
}

export async function getArchTree(query: ArchTreeQuery): Promise<ArchTreeResponse> {
  const qs = new URLSearchParams();
  if (query.search?.trim()) qs.set('search', query.search.trim());
  if (query.departmentId?.trim()) qs.set('department_id', query.departmentId.trim());
  if (query.docType?.trim()) qs.set('doc_type', query.docType.trim());
  if (query.status && query.status !== 'all') qs.set('status', query.status);
  if (query.scopeType && query.scopeType !== 'all') qs.set('scope_type', query.scopeType);
  if (query.subsystemId?.trim()) qs.set('subsystem_id', query.subsystemId.trim());
  if (query.archBinding && query.archBinding !== 'all') qs.set('arch_binding', query.archBinding);

  const suffix = qs.toString();
  return apiRequest<ArchTreeResponse>(suffix ? `/arch-tree?${suffix}` : '/arch-tree');
}

export async function getRepoScopes(repoId: number): Promise<RepoScopesDetailResponse> {
  return apiRequest<RepoScopesDetailResponse>(`/repos/${repoId}/scopes`);
}

export async function listRepoModules(repoId: number): Promise<RepoModulesListVO> {
  return apiRequest<RepoModulesListVO>(`/repos/${repoId}/modules`);
}

export async function getRepoModulePolicy(repoId: number): Promise<RepoModulePolicyVO> {
  return apiRequest<RepoModulePolicyVO>(`/repos/${repoId}/module-policy`);
}

export async function updateRepoModulePolicy(
  repoId: number,
  payload: UpdateRepoModulePolicyPayload,
): Promise<RepoModulePolicyVO> {
  return apiRequest<RepoModulePolicyVO>(`/repos/${repoId}/module-policy`, {
    method: 'PUT',
    bodyData: payload,
  });
}

export async function refreshRepoModules(repoId: number): Promise<RefreshModulesResultVO> {
  return apiRequest<RefreshModulesResultVO>(`/repos/${repoId}/modules/refresh`, {
    method: 'POST',
  });
}

export async function updateRepoModuleStatus(
  repoId: number,
  moduleId: number,
  payload: UpdateRepoModuleStatusPayload,
): Promise<RepoModuleVO> {
  return apiRequest<RepoModuleVO>(`/repos/${repoId}/modules/${moduleId}/status`, {
    method: 'PATCH',
    bodyData: payload,
  });
}

export async function migrateRepoModule(
  repoId: number,
  payload: MigrateRepoModulePayload,
): Promise<MigrateModuleResultVO> {
  return apiRequest<MigrateModuleResultVO>(`/repos/${repoId}/modules/migrate`, {
    method: 'POST',
    bodyData: payload,
  });
}
