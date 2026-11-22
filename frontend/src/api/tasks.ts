import type {
  BatchGenerateTaskPayload,
  CancelTaskResponse,
  EnginesResponse,
  GenSlotTaskVO,
  GenTaskDetailVO,
  GenerateTaskPayload,
  GenerateTaskSubmitResponse,
  TaskListQuery,
  TaskListResponse,
} from '../types';
import { apiRequest } from './client';

export async function listEngines(): Promise<EnginesResponse> {
  return apiRequest<EnginesResponse>('/engines');
}

export async function triggerGenerateTask(
  payload: GenerateTaskPayload,
): Promise<GenerateTaskSubmitResponse> {
  return apiRequest<GenerateTaskSubmitResponse>('/tasks/generate', {
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

export async function listTasks(query: TaskListQuery = {}): Promise<TaskListResponse> {
  const qs = new URLSearchParams();
  if (query.page) qs.set('page', String(query.page));
  if (query.pageSize) qs.set('page_size', String(query.pageSize));
  if (query.repoId) qs.set('repo_id', String(query.repoId));
  if (query.subdirectory !== undefined) qs.set('subdirectory', query.subdirectory);
  if (query.status && query.status !== 'all') qs.set('status', query.status);
  if (query.taskKind && query.taskKind !== 'all') qs.set('task_kind', query.taskKind);
  if (query.engine && query.engine !== 'all') qs.set('engine', query.engine);
  if (query.mine) qs.set('mine', 'true');
  if (query.topLevelOnly) qs.set('top_level_only', 'true');

  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return apiRequest<TaskListResponse>(`/tasks${suffix}`);
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
