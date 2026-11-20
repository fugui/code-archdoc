import type {
  BatchGenerateTaskPayload,
  CancelTaskResponse,
  GenSlotTaskVO,
  GenTaskDetailVO,
  GenerateTaskPayload,
  PaginatedResponse,
  TaskListQuery,
} from '../types';
import { apiRequest } from './client';

export async function triggerGenerateTask(payload: GenerateTaskPayload): Promise<GenTaskDetailVO> {
  return apiRequest<GenTaskDetailVO>('/tasks/generate', {
    method: 'POST',
    bodyData: payload,
  });
}

export async function triggerBatchGenerateTask(
  payload: BatchGenerateTaskPayload,
): Promise<GenTaskDetailVO> {
  return apiRequest<GenTaskDetailVO>('/tasks/generate-batch', {
    method: 'POST',
    bodyData: payload,
  });
}

export async function listTasks(query: TaskListQuery = {}): Promise<PaginatedResponse<GenTaskDetailVO>> {
  const qs = new URLSearchParams();
  if (query.page) qs.set('page', String(query.page));
  if (query.pageSize) qs.set('page_size', String(query.pageSize));
  if (query.repoId) qs.set('repo_id', String(query.repoId));
  if (query.subdirectory !== undefined) qs.set('subdirectory', query.subdirectory);
  if (query.status) qs.set('status', query.status);

  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return apiRequest<PaginatedResponse<GenTaskDetailVO>>(`/tasks${suffix}`);
}

export async function getTaskDetail(taskId: number): Promise<GenTaskDetailVO> {
  return apiRequest<GenTaskDetailVO>(`/tasks/${taskId}`);
}

export async function cancelTask(taskId: number): Promise<CancelTaskResponse> {
  return apiRequest<CancelTaskResponse>(`/tasks/${taskId}/cancel`, {
    method: 'POST',
  });
}

export async function retrySlotTask(taskId: number, slotTaskId: number): Promise<GenSlotTaskVO> {
  return apiRequest<GenSlotTaskVO>(`/tasks/${taskId}/slots/${slotTaskId}/retry`, {
    method: 'POST',
  });
}
