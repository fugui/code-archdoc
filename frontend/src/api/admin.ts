import type {
  ItemsResponse,
  LLMConfigVO,
  LLMProbeResultVO,
  ModelTier,
  ProbeLLMConfigPayload,
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
