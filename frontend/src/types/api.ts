import type {
  ArchBindingFilter,
  CanonicalFacetName,
  ChatIntentMode,
  ChatScopeType,
  ContextInputName,
  DriftFilterType,
  FacetName,
  ModuleDigestSourceConfig,
  ModuleDiscoveryStrategy,
  RepoScopeFilter,
  RepoStatusFilter,
  ReviewCategory,
  ReviewCommentAnchor,
  ReviewCommentStatus,
  ReviewScopeType,
  ScopeProfileConfig,
  SectionOwnership,
  SlotDefinition,
  TaskKindFilter,
  TaskStatusFilter,
  VerifyPolicyConfig,
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
  | 'MIGRATION_CONFLICT'
  | 'DOC_TYPE_IN_USE'
  | 'OPTIMISTIC_LOCK_CONFLICT'
  | 'VERIFIER_BLOCKED'
  | 'QUEUE_FULL'
  | 'TOKEN_BUDGET_EXCEEDED'
  | 'BATCH_TOKEN_BUDGET_EXCEEDED'
  | 'FILE_BUDGET_EXCEEDED'
  | 'REMOTE_BRANCH_FETCH_FAILED'
  | 'INTERNAL_ERROR';

export interface SwitchRepoBranchPayload {
  branch: string;
}

export interface RepoListQuery {
  page: number;
  pageSize: number;
  search?: string;
  departmentId?: string;
  docType?: string;
  status?: RepoStatusFilter;
  scopeType?: RepoScopeFilter;
  subsystemId?: string;
  archBinding?: ArchBindingFilter;
}

export interface ArchTreeQuery {
  search?: string;
  departmentId?: string;
  docType?: string;
  status?: RepoStatusFilter;
  scopeType?: RepoScopeFilter;
  subsystemId?: string;
  archBinding?: ArchBindingFilter;
}

export interface GenerateTaskPayload {
  repo_id: number;
  subdirectory: string;
  ref_name?: string;
  doc_types: string[];
  mode?: 'generate' | 'regenerate';
}

export interface BatchGenerateTaskPayload {
  repo_id: number;
  ref_name?: string;
  subdirectories?: string[];
  all_modules?: boolean;
  include_repo_scope?: boolean;
  doc_types: string[];
  mode?: 'generate' | 'regenerate';
}

export interface UpdateRepoModulePolicyPayload {
  strategy: ModuleDiscoveryStrategy;
  exclude_globs: string[];
  min_effective_files: number;
}

export interface UpdateRepoModuleStatusPayload {
  status: 'active' | 'ignored';
}

export interface MigrateRepoModulePayload {
  from_subdir: string;
  to_subdir: string;
}

export interface TaskListQuery {
  page?: number;
  pageSize?: number;
  repoId?: number;
  subdirectory?: string;
  status?: TaskStatusFilter;
  taskKind?: TaskKindFilter;
  mine?: boolean;
  topLevelOnly?: boolean;
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
  scope?: 'module' | 'repo';
}

export interface PublishDocumentPayload {
  revision_no?: number;
}

export interface ExportZipPayload {
  repo_ids?: number[];
  document_ids?: number[];
  include_index?: boolean;
}

export interface UpsertLLMConfigPayload {
  name: string;
  base_url: string;
  model_name: string;
  api_key: string;
  clear_api_key?: boolean;
  timeout_seconds: number;
  max_retries: number;
  max_tokens_per_task: number;
  max_files_per_task: number;
  is_active?: boolean;
  is_default?: boolean;
}

export interface ProbeLLMConfigPayload {
  base_url?: string;
  model_name?: string;
  api_key?: string;
}

export interface CreateDocTypePayload {
  slug: string;
  display_name: string;
  short_name?: string;
  category?: string;
  description: string;
  is_active?: boolean;
  preferred_model?: string;
  timeout_seconds: number;
  max_output_tokens?: number;
  scope_profile?: ScopeProfileConfig;
  ir_facets: FacetName[];
  repo_ir_facets?: CanonicalFacetName[];
  context_inputs?: ContextInputName[];
  module_digest_source?: ModuleDigestSourceConfig;
  verify_policy?: VerifyPolicyConfig;
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
  mode?: ChatIntentMode;
  scope_type?: ChatScopeType;
  target_slot_id?: string;
  base_revision_no: number;
  prompt: string;
  comment_ids?: number[];
  inline_anchor?: ReviewCommentAnchor;
}

export interface AcceptChatProposalPayload {
  base_revision_no: number;
}

export interface ApplyProposalItemSelection {
  item_id?: number;
  slot_id?: string;
  override_markdown?: string;
}

export interface ApplyChatProposalPayload {
  base_revision_no: number;
  change_summary?: string;
  items?: ApplyProposalItemSelection[];
}

export interface RejectProposalItemResultVO {
  item_id: number;
  message_id: number;
  item_status: 'rejected';
  message_proposal_status: string;
}

export interface CreateReviewCommentPayload {
  base_revision_no?: number;
  scope_type: ReviewScopeType;
  slot_id?: string;
  anchor?: ReviewCommentAnchor;
  category: ReviewCategory;
  content: string;
  persist_for_regen?: boolean;
}

export interface UpdateReviewCommentPayload {
  content?: string;
  category?: ReviewCategory;
  persist_for_regen?: boolean;
  status?: Extract<ReviewCommentStatus, 'open' | 'resolved' | 'dismissed'>;
  scope_type?: ReviewScopeType;
  slot_id?: string;
}

export interface ReviewCommentFilterQuery {
  status?: ReviewCommentStatus;
  scope_type?: ReviewScopeType;
  slot_id?: string;
  persist_for_regen?: boolean;
}

export interface DriftListQuery {
  page: number;
  pageSize: number;
  search?: string;
  departmentId?: string;
  driftType?: DriftFilterType;
  docType?: string;
}

export interface IncrementalRefreshPayload {
  slot_ids?: string[];
}

export interface MetricsOverviewQuery {
  days?: number;
  docType?: string;
  docTypeRevisionId?: number;
}

export interface RevisionsCompareQuery {
  docType: string;
  days?: number;
}

export interface SlotHeatmapQuery {
  docType: string;
  docTypeRevisionId?: number;
  days?: number;
}

export interface ItemsResponse<T> {
  items: T[];
}
