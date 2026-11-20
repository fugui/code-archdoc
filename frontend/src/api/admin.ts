import type {
  CloneDocTypePayload,
  CreateDocTypePayload,
  DocTypeDeleteResultVO,
  DocTypeDetailVO,
  DocTypeRevisionSummaryVO,
  DocTypeSummaryVO,
  DocTypeSyncResultVO,
  DocTypeUpdateVO,
  ItemsResponse,
  LLMConfigVO,
  LLMProbeResultVO,
  ModelTier,
  ProbeLLMConfigPayload,
  ReapResultVO,
  StorageStatsVO,
  ToggleDocTypeStatusPayload,
  UpdateDocTypePayload,
  UpdateLLMConfigPayload,
} from '../types';
import { apiRequest } from './client';

export async function listLLMConfigs(): Promise<LLMConfigVO[]> {
  const res = await apiRequest<ItemsResponse<LLMConfigVO>>('/admin/llm-configs');
  return Array.isArray(res.items) ? res.items : [];
}

export async function updateLLMConfig(
  tier: ModelTier,
  payload: UpdateLLMConfigPayload,
): Promise<LLMConfigVO> {
  return apiRequest<LLMConfigVO>(`/admin/llm-configs/${tier}`, {
    method: 'PUT',
    bodyData: payload,
  });
}

export async function probeLLMConfig(
  tier: ModelTier,
  payload?: ProbeLLMConfigPayload,
): Promise<LLMProbeResultVO> {
  return apiRequest<LLMProbeResultVO>(`/admin/llm-configs/${tier}/probe`, {
    method: 'POST',
    bodyData: payload,
  });
}

// ─── DocType 管理员全生命周期 API (F-03 W2) ───

export async function listAdminDocTypes(activeOnly = false): Promise<DocTypeSummaryVO[]> {
  const query = activeOnly ? '?active_only=true' : '';
  const res = await apiRequest<ItemsResponse<DocTypeSummaryVO>>(`/doc-types${query}`);
  return Array.isArray(res.items) ? res.items : [];
}

export async function getDocTypeDetail(slug: string): Promise<DocTypeDetailVO> {
  return apiRequest<DocTypeDetailVO>(`/doc-types/${encodeURIComponent(slug)}`);
}

export async function listDocTypeRevisions(slug: string): Promise<DocTypeRevisionSummaryVO[]> {
  const res = await apiRequest<ItemsResponse<DocTypeRevisionSummaryVO>>(
    `/doc-types/${encodeURIComponent(slug)}/revisions`,
  );
  return Array.isArray(res.items) ? res.items : [];
}

export async function createDocType(payload: CreateDocTypePayload): Promise<DocTypeDetailVO> {
  return apiRequest<DocTypeDetailVO>('/admin/doc-types', {
    method: 'POST',
    bodyData: payload,
  });
}

export async function cloneDocType(
  slug: string,
  payload: CloneDocTypePayload,
): Promise<DocTypeDetailVO> {
  return apiRequest<DocTypeDetailVO>(`/admin/doc-types/${encodeURIComponent(slug)}/clone`, {
    method: 'POST',
    bodyData: payload,
  });
}

export async function updateDocType(
  slug: string,
  payload: UpdateDocTypePayload,
): Promise<DocTypeUpdateVO> {
  return apiRequest<DocTypeUpdateVO>(`/admin/doc-types/${encodeURIComponent(slug)}`, {
    method: 'PUT',
    bodyData: payload,
  });
}

export async function toggleDocTypeStatus(
  slug: string,
  payload: ToggleDocTypeStatusPayload,
): Promise<DocTypeDetailVO> {
  return apiRequest<DocTypeDetailVO>(`/admin/doc-types/${encodeURIComponent(slug)}/status`, {
    method: 'PATCH',
    bodyData: payload,
  });
}

export async function deleteDocType(slug: string): Promise<DocTypeDeleteResultVO> {
  return apiRequest<DocTypeDeleteResultVO>(`/admin/doc-types/${encodeURIComponent(slug)}`, {
    method: 'DELETE',
  });
}

export async function syncDocTypes(): Promise<DocTypeSyncResultVO> {
  return apiRequest<DocTypeSyncResultVO>('/admin/doc-types/sync', {
    method: 'POST',
  });
}

// ─── 沙箱磁盘配额、调度队列与成本观测 API (F-27 W2) ───

export async function getAdminStorageStats(): Promise<StorageStatsVO> {
  return apiRequest<StorageStatsVO>('/admin/storage/stats');
}

export async function triggerAdminStorageReap(): Promise<ReapResultVO> {
  return apiRequest<ReapResultVO>('/admin/storage/reap', {
    method: 'POST',
  });
}
