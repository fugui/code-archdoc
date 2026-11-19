import type {
  FacetName,
  ModelTier,
  RepoScopeFilter,
  RepoStatusFilter,
  SectionOwnership,
  SlotDefinition,
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

export interface CreateDocTypePayload {
  slug: string;
  display_name: string;
  description: string;
  is_active?: boolean;
  model_tier: ModelTier;
  timeout_seconds: number;
  max_output_tokens?: number;
  ir_facets: FacetName[];
  slots: SlotDefinition[];
  template_md: string;
  generation_prompt_md: string;
  refine_prompt_md: string;
}

export interface CloneDocTypePayload {
  new_slug: string;
  display_name: string;
}

export interface UpdateDocTypePayload {
  meta: Record<string, unknown>;
  template_md: string;
  generation_prompt_md: string;
  refine_prompt_md: string;
}

export interface ToggleDocTypeStatusPayload {
  is_active: boolean;
}

export interface ChatRefinePayload {
  session_id?: number;
  target_slot_id: string;
  base_revision_no: number;
  prompt: string;
}

export interface AcceptChatProposalPayload {
  base_revision_no: number;
}

export interface ItemsResponse<T> {
  items: T[];
}
