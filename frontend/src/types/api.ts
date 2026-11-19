import type {
  RepoScopeFilter,
  RepoStatusFilter,
  SectionOwnership,
} from './domain';

export type ArchdocErrorCode =
  | 'INVALID_PARAM'
  | 'INVALID_SUBDIR'
  | 'INVALID_SLUG'
  | 'DOC_TYPE_INACTIVE'
  | 'UNAUTHORIZED'
  | 'FORBIDDEN'
  | 'DRAFT_ACCESS_DENIED'
  | 'PATH_ESCAPE'
  | 'NOT_FOUND'
  | 'CONFLICT'
  | 'DOC_TYPE_IN_USE'
  | 'OPTIMISTIC_LOCK_CONFLICT'
  | 'VERIFIER_BLOCKED'
  | 'QUEUE_FULL'
  | 'TOKEN_BUDGET_EXCEEDED'
  | 'FILE_BUDGET_EXCEEDED'
  | 'INTERNAL_ERROR';

export interface RepoListQuery {
  page: number;
  pageSize: number;
  search?: string;
  departmentId?: string;
  docType?: string;
  status?: RepoStatusFilter;
  scopeType?: RepoScopeFilter;
}

export interface GenerateTaskPayload {
  repo_id: number;
  subdirectory: string;
  ref_name?: string;
  doc_types: string[];
  mode?: 'generate' | 'regenerate';
}

export interface TaskListQuery {
  page?: number;
  pageSize?: number;
  repoId?: number;
  subdirectory?: string;
  status?: string;
}

export interface UpdateSectionPayload {
  base_revision_no: number;
  markdown: string;
  ownership?: SectionOwnership;
  change_summary?: string;
}

export interface UpdateOwnershipPayload {
  ownership: SectionOwnership;
  clear_drift_review?: boolean;
}

export type OrphanResolveAction = 'keep_locked' | 'merge_into' | 'delete';

export interface ResolveOrphanPayload {
  base_revision_no: number;
  action: OrphanResolveAction;
  target_slot_id?: string;
}

export interface RollbackDocumentPayload {
  base_revision_no: number;
  target_revision_no: number;
  change_summary?: string;
}

export interface SourcePreviewQuery {
  commit?: string;
  file: string;
  lineStart?: number;
  lineEnd?: number;
  symbol?: string;
}

export interface PublishDocumentPayload {
  revision_no?: number;
}

export interface ExportZipPayload {
  repo_ids?: number[];
  document_ids?: number[];
  include_index?: boolean;
}

export interface UpdateLLMConfigPayload {
  base_url: string;
  model_name: string;
  api_key: string;
  timeout_seconds: number;
  max_retries: number;
  max_tokens_per_task: number;
  max_files_per_task: number;
  is_active: boolean;
}

export interface ProbeLLMConfigPayload {
  base_url?: string;
  model_name?: string;
  api_key?: string;
}

export interface ItemsResponse<T> {
  items: T[];
}
