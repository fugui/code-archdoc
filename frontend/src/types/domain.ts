// 领域核心类型与后端 VO 契约（严格对齐 docs/03-api/02-data-model-and-dto-contract.md §6 及 Go 服务 VO，严禁 any）

export type FacetName =
  | 'tree'
  | 'build'
  | 'deps'
  | 'config_keys'
  | 'symbols'
  | 'idl_contracts'
  | 'http_routes'
  | 'data_models'
  | 'runtime_primitives'
  | 'module_context';

export type CanonicalFacetName = Exclude<FacetName, 'module_context'>;
export type ContextInputName = 'code_ir' | 'module_context' | 'module_digests';

export interface ScopeProfileConfig {
  include_globs?: string[];
  exclude_globs?: string[];
  max_depth?: number;
  include_tests: boolean;
  include_legacy_docs?: boolean;
}

export interface VerifyPolicyConfig {
  strict_symbol_check: boolean;
  require_mermaid: boolean;
  allowed_mermaid_types?: string[];
  max_mermaid_nodes?: number;
  max_mermaid_edges?: number;
  block_on_secret_leak?: boolean;
}

export interface ModuleDigestSourceConfig {
  doc_type_slug: string;
  slot_id: string;
  max_chars?: number;
  fallback_order?: string[];
}

export interface DocTypeMeta {
  name?: string;
  display_name?: string;
  short_name?: string;
  category?: string;
  description?: string;
  is_active?: boolean;
  is_builtin?: boolean;
  preferred_model?: string;
  timeout_seconds?: number;
  max_output_tokens?: number;
  scope_profile?: ScopeProfileConfig;
  ir_facets?: FacetName[];
  context_inputs?: ContextInputName[];
  repo_ir_facets?: CanonicalFacetName[];
  module_digest_source?: ModuleDigestSourceConfig;
  slots?: SlotDefinition[];
  verify_policy?: VerifyPolicyConfig;
  [extraKey: string]: unknown;
}

export type SectionOwnership = 'auto' | 'hybrid' | 'locked';
export type DocumentStatus = 'draft' | 'approved';
export type RevisionSource =
  | 'ai_generate'
  | 'incremental_refresh'
  | 'manual_edit'
  | 'chat_refine'
  | 'rollback';

export type TaskKind = 'generate' | 'regenerate' | 'incremental_refresh' | 'batch';
export type TaskPriority = 'interactive' | 'batch';
export type TaskStatus =
  | 'pending'
  | 'extracting'
  | 'generating'
  | 'verifying'
  | 'ready'
  | 'failed'
  | 'canceled';
export type TaskStatusFilter = '' | 'all' | 'active' | TaskStatus;
export type TaskKindFilter = '' | 'all' | TaskKind;

export type SlotTaskStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'skipped_locked'
  | 'canceled';

export type ProposalStatus =
  | 'none'
  | 'pending'
  | 'partial_accepted'
  | 'accepted'
  | 'rejected'
  | 'superseded';
export type ReviewScopeType = 'inline' | 'section' | 'global';
export type ReviewCategory = 'correction' | 'supplement' | 'concise' | 'global_rule' | 'question';
export type ReviewCommentStatus = 'open' | 'proposed' | 'resolved' | 'dismissed';
export type ReviewOriginRole = 'owner' | 'reviewer';
export type ChatIntentMode = 'revise' | 'ask';
export type ChatScopeType = 'slot' | 'inline' | 'global' | 'review_batch';
export type DriftType = 'code' | 'spec';
export type DriftPrecision = 'section_level' | 'repo_level';
export type ScopeType = 'repo' | 'subdir';
export type RepoStatusFilter = '' | 'all' | 'approved' | 'draft' | 'drifted' | 'missing';
export type RepoScopeFilter = '' | 'all' | 'repo' | 'subdir';
export type CoverageViewMode = 'repo' | 'arch';
export type ArchBindingFilter = '' | 'all' | 'bound' | 'unbound' | 'unbound_repo';
export type ArchModuleBindingStatus = 'bound' | 'unbound' | 'repo_inactive';
export type ArchElementType = 'subsystem' | 'group' | 'module' | '';

export interface CurrentUserProfile {
  id: number;
  username: string;
  name: string;
  email?: string;
  employee_id?: string;
  department_id?: number | null;
  department_name?: string;
  roles: string[];
  is_super_admin: boolean;
  is_module_admin: boolean;
  required_ad_role: string;
}

export interface DepartmentVO {
  id: number;
  name: string;
  leader_id: number | null;
}

export interface PaginatedResponse<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface ApiErrorBody {
  error: string;
  code?: string;
  details?: Record<string, unknown>;
}

export interface EvidenceAnchor {
  anchor_id: string;
  anchor_key: string;
  scope?: 'module' | 'repo' | '';
  file_path: string;
  symbol_kind: string;
  fq_symbol: string;
  content_fingerprint: string;
  commit_sha: string;
  line_start: number;
  line_end: number;
  confidence: 'exact' | 'heuristic';
}

export interface UnverifiedRef {
  slot_id: string;
  ref_kind: string;
  value: string;
  severity: 'warning' | 'error';
}

export interface MermaidIssue {
  slot_id: string;
  block_index: number;
  diagram_type: string;
  issue_code: string;
  severity: 'error' | 'warning';
  node_count: number;
  edge_count: number;
  message: string;
}

export interface SecretFinding {
  slot_id: string;
  rule_id: string;
  matched_mask: string;
  line_offset: number;
  severity: 'blocker';
}

export interface VerifierReport {
  passed: boolean;
  can_approve: boolean;
  blocker_count: number;
  error_count: number;
  warning_count: number;
  slot_coverage_rate: number;
  missing_required_slots: string[];
  unverified_refs: UnverifiedRef[];
  mermaid_issues: MermaidIssue[];
  secret_findings: SecretFinding[];
}

export interface SectionVO {
  slot_id: string;
  section_order: number;
  title: string;
  ownership: SectionOwnership;
  is_orphan: boolean;
  manually_edited: boolean;
  needs_drift_review: boolean;
  content_sha256: string;
  markdown: string;
  evidences: EvidenceAnchor[];
  ref_citations?: RefDocCitationVO[];
}

export interface AffectedSlotDrift {
  slot_id: string;
  slot_title: string;
  ownership: SectionOwnership;
  reason: string;
  change_kind: 'added' | 'modified' | 'removed';
  changed_symbols?: string[];
  changed_files?: string[];
}

export interface DriftDimensionVO {
  has_drift: boolean;
  precision: DriftPrecision;
  affected_slot_count: number;
  affected_slots: AffectedSlotDrift[];
  detected_at: string;
}

export interface DocumentDriftDetailVO {
  code_drift: DriftDimensionVO | null;
  spec_drift: DriftDimensionVO | null;
}

export interface DocStatusSummaryVO {
  document_id: number;
  status: DocumentStatus;
  current_revision_no: number;
  published_revision_no: number;
  has_code_drift: boolean;
  has_spec_drift: boolean;
  affected_slot_count: number;
  verifier_passed: boolean;
  can_approve: boolean;
  updated_at: string;
}

export type ScopeStatus = 'active' | 'missing' | 'orphan';
export type ModuleDiscoveryStrategy = 'merged' | 'top_level_dirs' | 'arch_elements';
export type ModuleSource = 'arch_element' | 'discovered';
export type ModuleStatus = 'active' | 'ignored' | 'missing';

export interface RepoScopeCoverageVO {
  repo_id: number;
  repo_name: string;
  department_id: number;
  department_name: string;
  subdirectory: string;
  scope_type: ScopeType;
  scope_status?: ScopeStatus;
  module_source?: string;
  effective_files?: number;
  total_loc?: number;
  active_module_count?: number;
  modules_scanned?: boolean;
  arch_element_id: number | null;
  arch_element_name: string;
  arch_element_type?: ArchElementType;
  arch_path?: string;
  subsystem_id?: number | null;
  subsystem_name?: string;
  branch: string;
  default_branch?: string;
  is_branch_overridden?: boolean;
  can_switch_branch?: boolean;
  owner_id: number;
  owner_name: string;
  can_write: boolean;
  latest_commit_sha: string;
  remote_head_sha: string;
  active_task_id: number | null;
  doc_statuses: Record<string, DocStatusSummaryVO>;
}

export interface RepoModulePolicyVO {
  repo_id: number;
  target_branch?: string;
  strategy: ModuleDiscoveryStrategy;
  exclude_globs: string[];
  min_effective_files: number;
  updated_by?: number | null;
  updated_at?: string | null;
}

export interface RepoModuleVO {
  id: number;
  repo_id: number;
  subdirectory: string;
  source: string;
  status: ModuleStatus;
  arch_element_id: number | null;
  arch_element_name: string;
  owner_id: number;
  owner_name: string;
  effective_files: number;
  total_loc: number;
  last_seen_commit: string;
  last_seen_at: string;
}

export interface RepoModulesListVO {
  repo_id: number;
  policy: RepoModulePolicyVO;
  modules: RepoModuleVO[];
}

export interface RefreshModulesResultVO {
  repo_id: number;
  commit_sha: string;
  active_count: number;
  modules: RepoModuleVO[];
}

export interface MigrateModuleResultVO {
  repo_id: number;
  from_subdir: string;
  to_subdir: string;
  migrated_documents: number;
  republished_documents: number;
}

export interface ArchSubsystemOptionVO {
  id: number;
  identifier: string;
  name_cn: string;
  name_en: string;
  module_count?: number;
  scope_count: number;
}

export interface ArchTreeSummaryVO {
  total_modules: number;
  bound_modules: number;
  covered_modules: number;
  approved_docs: number;
  draft_docs: number;
  drifted_docs: number;
  unassigned_repo_count: number;
  system_ref_doc_count?: number;
}

export interface ArchTreeStatsVO {
  module_count: number;
  bound_module_count: number;
  covered_module_count: number;
  approved_doc_count: number;
  draft_doc_count: number;
  drifted_doc_count: number;
  blocked_doc_count: number;
}

export interface ArchTreeNodeVO {
  id: number;
  identifier: string;
  name_cn: string;
  name_en: string;
  type: 'subsystem' | 'group' | 'module' | string;
  parent_id: number | null;
  owner_id: number | null;
  owner_name: string;
  repo_id: number | null;
  subdirectory: string;
  description: string;
  binding_status?: ArchModuleBindingStatus;
  scope?: RepoScopeCoverageVO | null;
  stats: ArchTreeStatsVO;
  direct_ref_doc_count?: number;
  inherited_ref_doc_count?: number;
  /** @deprecated 由 scope 替代，固定返回 [] */
  scopes: RepoScopeCoverageVO[];
  children: ArchTreeNodeVO[];
}

export interface ArchTreeResponse {
  summary: ArchTreeSummaryVO;
  subsystems: ArchSubsystemOptionVO[];
  nodes: ArchTreeNodeVO[];
  /** @deprecated 由 /repos?arch_binding=unbound_repo 承接，固定返回 [] */
  unassigned_scopes: RepoScopeCoverageVO[];
}

export interface RepoCoverageSummaryVO {
  total_scopes: number;
  covered_scopes: number;
  approved_docs: number;
  drifted_docs: number;
}

export interface RepoCoverageListResponse extends PaginatedResponse<RepoScopeCoverageVO> {
  summary: RepoCoverageSummaryVO;
  subsystems?: ArchSubsystemOptionVO[];
}

export type GenEngine = 'native' | 'opencode';

export type TaskEngineFilter = 'all' | '' | GenEngine;

export interface EngineInfoVO {
  name: GenEngine;
  display_name: string;
  available: boolean;
  default: boolean;
  version?: string;
  reason?: string;
}

export interface EnginesResponse {
  engines: EngineInfoVO[];
  default_engine: GenEngine;
}

export interface GenTaskSummaryVO {
  id: number;
  repo_id: number;
  subdirectory: string;
  ref_name: string;
  task_kind: TaskKind;
  engine?: GenEngine;
  priority: TaskPriority;
  status: TaskStatus;
  progress_percent: number;
  triggered_by: number;
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
}

export interface RepoScopesDetailResponse {
  repo_id: number;
  repo_name: string;
  department_id: number;
  department_name: string;
  branch: string;
  default_branch?: string;
  is_branch_overridden?: boolean;
  can_switch_branch?: boolean;
  owner_id: number;
  owner_name: string;
  scopes: RepoScopeCoverageVO[];
  recent_tasks: GenTaskSummaryVO[];
}

export interface RepoBranchItemVO {
  name: string;
  commit_sha: string;
  is_head: boolean;
  is_default: boolean;
  is_current: boolean;
}

export interface RepoBranchesVO {
  repo_id: number;
  default_branch: string;
  current_branch: string;
  is_overridden: boolean;
  remote_head_branch: string;
  branches: RepoBranchItemVO[];
}

export interface SwitchRepoBranchVO {
  repo_id: number;
  previous_branch: string;
  current_branch: string;
  default_branch: string;
  is_overridden: boolean;
  remote_head_sha: string;
  rebound_documents: number;
  drifted_documents: number;
  changed: boolean;
}

export interface SlotDefinition {
  slot_id: string;
  title: string;
  ownership: SectionOwnership;
  required: boolean;
  ir_facets: FacetName[];
  token_budget: number;
  min_chars: number;
  instruction_hint?: string;
  enable_source_tools?: boolean;
  max_tool_rounds?: number;
}

export interface DocTypeSummaryVO {
  id: number;
  slug: string;
  display_name: string;
  short_name: string;
  category: string;
  description: string;
  is_builtin: boolean;
  is_active: boolean;
  preferred_model: string;
  timeout_seconds: number;
  ir_facets: FacetName[];
  slot_count: number;
  latest_revision_id: number | null;
  latest_revision_no: number;
  latest_aggregate_hash: string;
  published_doc_count: number;
  updated_at: string;
}

export interface DocTypeRevisionSummaryVO {
  id: number;
  revision_no: number;
  aggregate_hash: string;
  prompt_hash: string;
  slots_snapshot: SlotDefinition[];
  created_by: number | null;
  created_at: string;
}

export interface DocTypeDetailVO extends DocTypeSummaryVO {
  meta: DocTypeMeta;
  slots: SlotDefinition[];
  template_md: string;
  generation_prompt_md: string;
  refine_prompt_md: string;
  latest_revision: DocTypeRevisionSummaryVO | null;
}

export interface DocTypeUpdateVO extends DocTypeDetailVO {
  revision_created: boolean;
}

export interface DocTypeSyncResultVO {
  synced_count: number;
  new_revisions: number;
  slugs: string[];
}

export interface DocTypeDeleteResultVO {
  deleted: boolean;
  slug: string;
}

export interface ChatVerifierIssueVO {
  kind: string;
  severity: string;
  message: string;
  slot_id?: string;
}

export interface ChatVerifierPreviewVO {
  can_approve: boolean;
  blocker_count: number;
  error_count: number;
  warning_count: number;
  issues: ChatVerifierIssueVO[];
}

export interface ReviewCommentAnchor {
  block_index?: number;
  block_type?: string;
  quote?: string;
  prefix?: string;
  suffix?: string;
  source_line_start?: number;
  source_line_end?: number;
}

export interface ReviewCommentVO {
  id: number;
  document_id: number;
  comment_no: number;
  base_revision_no: number;
  author_id: number;
  origin_role: ReviewOriginRole;
  scope_type: ReviewScopeType;
  slot_id: string;
  anchor: ReviewCommentAnchor;
  category: ReviewCategory;
  content: string;
  persist_for_regen: boolean;
  status: ReviewCommentStatus;
  proposal_group_msg_id?: number | null;
  resolved_revision_id?: number | null;
  created_at: string;
  updated_at: string;
}

export interface ChatPlanTargetSlotVO {
  slot_id: string;
  title: string;
  comment_ids?: number[];
  reason?: string;
}

export interface ChatPlanLockedAdviceVO {
  slot_id: string;
  title: string;
  comment_ids?: number[];
  advice: string;
}

export interface ChatPlanSummaryVO {
  target_slots?: ChatPlanTargetSlotVO[];
  skipped_locked_slots?: ChatPlanLockedAdviceVO[];
  truncated_slots?: string[];
}

export interface ChatContextRefVO {
  scope_type?: string;
  slot_id?: string;
  comment_ids?: number[];
  inline_anchor?: ReviewCommentAnchor;
  ref_docs?: TaskReferenceDocSnapshotVO[];
}

export interface ChatProposalItemVO {
  item_id: number;
  message_id: number;
  slot_id: string;
  slot_title?: string;
  base_revision_no: number;
  before_md: string;
  after_md: string;
  final_applied_md?: string;
  unified_diff: string;
  cited_anchors: EvidenceAnchor[];
  ref_citations?: RefDocCitationVO[];
  verifier_preview?: ChatVerifierPreviewVO | null;
  addressed_comment_ids: number[];
  status: ProposalStatus;
  applied_revision_id?: number | null;
}

export interface ChatMessageVO {
  id: number;
  session_id: number;
  role: 'user' | 'assistant';
  intent?: ChatIntentMode | 'plan_refine';
  scope_type?: ChatScopeType;
  content: string;
  target_slot_id: string;
  base_revision_no: number;
  proposed_before_md: string;
  proposed_after_md: string;
  unified_diff: string;
  verifier_preview?: ChatVerifierPreviewVO | null;
  plan_summary?: ChatPlanSummaryVO | null;
  context_refs?: ChatContextRefVO[];
  proposal_items?: ChatProposalItemVO[];
  proposal_status: ProposalStatus;
  applied_revision_id?: number | null;
  model_name: string;
  created_at: string;
}

export interface ChatSessionWithMessagesVO {
  id: number;
  document_id: number;
  target_slot_id: string;
  title: string;
  created_by: number;
  created_at: string;
  updated_at: string;
  messages: ChatMessageVO[];
}

export interface ChatRejectResultVO {
  message_id: number;
  proposal_status: ProposalStatus;
}

export interface BudgetDirectoryItem {
  path: string;
  estimated_tokens: number;
  file_count: number;
}

export interface BudgetDocTypeItem {
  slug: string;
  estimated_tokens: number;
}

export interface BudgetModuleItem {
  subdirectory: string;
  estimated_tokens: number;
  effective_files: number;
}

export interface BudgetAdvice {
  estimated_tokens: number;
  max_tokens_per_task: number;
  effective_files: number;
  max_files_per_task: number;
  top_directories: BudgetDirectoryItem[];
  top_doc_types: BudgetDocTypeItem[];
  module_breakdown?: BudgetModuleItem[];
  suggestions: string[];
}

export interface BatchSummaryVO {
  total_children: number;
  completed_children: number;
  failed_children: number;
  canceled_children: number;
  running_children: number;
}

export interface GenSlotTaskVO {
  id: number;
  doc_type_slug: string;
  doc_type_revision_id: number;
  slot_id: string;
  document_id: number | null;
  status: SlotTaskStatus;
  attempt_count: number;
  model_name: string;
  error_message: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface GenTaskDetailVO {
  id: number;
  parent_task_id?: number | null;
  repo_id: number;
  repo_name?: string;
  subdirectory: string;
  ref_name: string;
  snapshot_id?: number | null;
  commit_sha?: string;
  task_kind: TaskKind;
  engine?: GenEngine;
  priority: TaskPriority;
  doc_types: string[];
  target_document_id?: number | null;
  target_slots?: string[];
  status: TaskStatus;
  progress_percent: number;
  estimated_tokens: number;
  error_code?: string;
  error_message?: string;
  budget_advice?: BudgetAdvice | null;
  slot_tasks?: GenSlotTaskVO[];
  child_tasks?: GenTaskDetailVO[];
  batch_summary?: BatchSummaryVO | null;
  reference_docs?: TaskReferenceDocSnapshotVO[];
  triggered_by: number;
  triggered_by_name?: string;
  can_write?: boolean;
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
}

export interface GenerateTaskSubmitResponse extends GenTaskDetailVO {
  created_count?: number;
  tasks?: GenTaskDetailVO[];
}

export interface TaskSummaryVO {
  total: number;
  active: number;
  ready: number;
  failed: number;
  canceled: number;
}

export interface TaskListResponse extends PaginatedResponse<GenTaskDetailVO> {
  summary?: TaskSummaryVO;
}

export interface CancelTaskResponse {
  id: number;
  status: TaskStatus;
}

export interface DocumentNavItemVO {
  id: number;
  repo_id: number;
  subdirectory: string;
  ref_name: string;
  doc_type_slug: string;
  doc_type_name: string;
  title: string;
  status: DocumentStatus;
  current_revision_no: number;
  published_revision_no: number;
  verifier_passed: boolean;
  can_approve: boolean;
  has_code_drift: boolean;
  has_spec_drift: boolean;
  updated_at: string;
}

export interface DocumentListVO {
  can_write: boolean;
  items: DocumentNavItemVO[];
}

export interface ViewingRevisionVO {
  id: number;
  revision_no: number;
  snapshot_id: number;
  commit_sha: string;
  source: RevisionSource;
  source_ref_id?: number | null;
  change_summary: string;
  verifier_passed: boolean;
  can_approve: boolean;
  slot_coverage_rate: number;
  verifier_report: VerifierReport;
  created_by: number;
  created_at: string;
}

export interface DocumentRevisionSummaryVO {
  id: number;
  revision_no: number;
  snapshot_id: number;
  commit_sha: string;
  doc_type_revision_id: number;
  source: RevisionSource;
  source_ref_id?: number | null;
  change_summary: string;
  verifier_passed: boolean;
  can_approve: boolean;
  slot_coverage_rate: number;
  created_by: number;
  created_at: string;
}

export interface DocumentDetailVO {
  id: number;
  repo_id: number;
  repo_name: string;
  subdirectory: string;
  ref_name: string;
  doc_type_slug: string;
  doc_type_name: string;
  doc_type_revision_id: number;
  doc_type_revision_no: number;
  title: string;
  status: DocumentStatus;
  can_write: boolean;
  current_revision_no: number;
  published_revision_no: number;
  published_commit_sha: string;
  published_at: string | null;
  viewing_revision: ViewingRevisionVO;
  sections: SectionVO[];
  drift: DocumentDriftDetailVO;
}

export interface SectionDiffItemVO {
  slot_id: string;
  title: string;
  changed: boolean;
  unified_diff: string;
  added_lines: number;
  removed_lines: number;
}

export interface RevisionDiffVO {
  document_id: number;
  from_rev: number;
  to_rev: number;
  section_diffs: SectionDiffItemVO[];
}

export interface SourcePreviewVO {
  repo_id: number;
  subdirectory: string;
  commit_sha: string;
  file_path: string;
  language: string;
  total_lines: number;
  highlight_start: number;
  highlight_end: number;
  relocated_by_symbol: boolean;
  content: string;
}

export interface PublishResultVO {
  document_id: number;
  status: DocumentStatus;
  published_revision_no: number;
  published_commit_sha: string;
  published_rel_path: string;
  published_at: string;
  broken_links_downgraded: number;
}

export interface UnpublishResultVO {
  document_id: number;
  status: DocumentStatus;
  removed_rel_path: string;
}

export interface LLMConfigVO {
  id: number;
  name: string;
  base_url: string;
  model_name: string;
  opencode_model?: string;
  api_key_masked: string;
  has_api_key: boolean;
  timeout_seconds: number;
  max_retries: number;
  max_tokens_per_task: number;
  max_files_per_task: number;
  is_active: boolean;
  is_default: boolean;
  last_probe_status: 'unknown' | 'ok' | 'failed';
  last_probe_latency_ms: number;
  last_probed_at: string | null;
}

export interface LLMProbeResultVO {
  id?: number;
  name?: string;
  reachable: boolean;
  latency_ms: number;
  model_echo: string;
  error_message: string;
}

// ─── M6: 双维漂移雷达 (F-12 / F-25) ───

export type DriftFilterType = 'all' | 'code' | 'spec' | 'both';

export interface DriftSummaryVO {
  total_drifted_docs: number;
  code_drift_docs: number;
  spec_drift_docs: number;
  both_drift_docs: number;
  monitored_repos: number;
  unhealthy_repos: number;
  last_poll_at: string | null;
  poll_interval_minutes: number;
}

export interface DriftDocumentVO {
  document_id: number;
  repo_id: number;
  repo_name: string;
  subdirectory: string;
  doc_type_slug: string;
  doc_type_name: string;
  can_write: boolean;
  published_commit_sha: string;
  remote_head_sha: string;
  bound_doc_type_revision_no: number;
  latest_doc_type_revision_no: number;
  code_drift: DriftDimensionVO | null;
  spec_drift: DriftDimensionVO | null;
}

export interface TriggerDriftPollResponseVO {
  triggered: boolean;
  monitored_repos: number;
  message: string;
}

// ─── M6: 在线质量度量看板 (F-26 W2) ───

export interface MetricsOverviewVO {
  window_days: number;
  avg_edit_diff_ratio: number;
  chat_accept_rate: number;
  verifier_first_pass_rate: number;
  verifier_block_rate: number;
  avg_slot_coverage_rate: number;
  avg_tokens_per_doc: number;
  avg_cache_hit_ratio: number;
  sample_doc_count: number;
}

export interface RevisionMetricPointVO {
  doc_type_revision_id: number;
  revision_no: number;
  created_at: string;
  avg_edit_diff_ratio: number;
  chat_accept_rate: number;
  verifier_block_rate: number;
  avg_slot_coverage_rate: number;
  avg_tokens_per_doc: number;
  avg_cache_hit_ratio: number;
  sample_count: number;
}

export interface RevisionsCompareVO {
  doc_type_slug: string;
  revisions: RevisionMetricPointVO[];
}

export interface SlotHeatmapItemVO {
  slot_id: string;
  title: string;
  avg_edit_diff_ratio: number;
  verifier_warning_rate: number;
  sample_count: number;
}

export interface SlotHeatmapVO {
  doc_type_slug: string;
  slots: SlotHeatmapItemVO[];
}

// ─── M6: 受控本地存储配额与双优先级调度态势 (F-27 W2) ───

export interface SandboxStorageStatVO {
  used_mb: number;
  max_mb: number;
  repo_count: number;
  active_leases: number;
}

export interface IRCacheStorageStatVO {
  used_mb: number;
  max_mb: number;
  facet_count: number;
  evicted_count: number;
}

export interface PublishStorageStatVO {
  used_mb: number;
  published_files: number;
}

export interface SchedulerStatsVO {
  interactive_queue_len: number;
  batch_queue_len: number;
  running_llm_calls: number;
  max_concurrency: number;
}

export interface StorageStatsVO {
  sandbox: SandboxStorageStatVO;
  ir_cache: IRCacheStorageStatVO;
  publish_dir: PublishStorageStatVO;
  scheduler: SchedulerStatsVO;
}

export interface ReapResultVO {
  reaped_sandboxes: number;
  reaped_ir_facets: number;
  freed_bytes: number;
  skipped_active_leases: number;
}

// ─── ADR-10: 架构参考文档与上下文注入 ───

export type RefDocKind =
  | 'arch_design'
  | 'domain_glossary'
  | 'interface_spec'
  | 'adr_constraint'
  | 'other';

export type RefDocStatus = 'active' | 'archived';

export type RefDocScopeLevel = 'system' | 'subsystem' | 'group' | 'module' | 'repo';

export interface RefDocHeadingNode {
  section_id: string;
  title: string;
  level: number;
  start_line: number;
  end_line: number;
  rune_count: number;
  is_chunk?: boolean;
}

export interface RefDocBindingVO {
  id: number;
  doc_id: number;
  scope_key: string;
  scope_level: RefDocScopeLevel;
  scope_label: string;
  is_home: boolean;
  arch_element_id?: number | null;
  repo_id?: number | null;
  subdirectory: string;
  inherit_to_children: boolean;
  doc_type_slugs: string[];
  sort_order: number;
  can_manage_binding?: boolean;
}

export interface ReferenceDocSummaryVO {
  id: number;
  title: string;
  doc_kind: RefDocKind;
  description: string;
  owner_scope_key: string;
  owner_scope_level: RefDocScopeLevel;
  owner_scope_label: string;
  summary_md: string;
  summary_stale: boolean;
  current_version: number;
  estimated_tokens: number;
  status: RefDocStatus;
  binding_count: number;
  can_edit: boolean;
  updated_at: string;
}

export interface ReferenceDocDetailVO {
  id: number;
  title: string;
  doc_kind: RefDocKind;
  description: string;
  visibility?: string;
  owner_scope_key: string;
  owner_scope_level: RefDocScopeLevel;
  owner_scope_label: string;
  summary_md: string;
  summary_source: string;
  summary_status: string;
  summary_for_version: number;
  summary_stale: boolean;
  current_version: number;
  viewing_version: number;
  content_sha256: string;
  byte_size: number;
  rune_count: number;
  estimated_tokens: number;
  line_count: number;
  status: RefDocStatus;
  toc: RefDocHeadingNode[];
  declared_terms: string[];
  content_md: string;
  bindings: RefDocBindingVO[];
  can_edit: boolean;
  created_by: number;
  updated_by: number;
  created_at: string;
  updated_at: string;
}

export interface EffectiveRefDocVO {
  doc_id: number;
  binding_id: number;
  title: string;
  doc_kind: RefDocKind;
  current_version: number;
  summary_md: string;
  summary_stale: boolean;
  toc: RefDocHeadingNode[];
  estimated_tokens: number;
  is_home: boolean;
  is_inherited: boolean;
  effective_scope_level: RefDocScopeLevel;
  source_scope_label: string;
  inherit_to_children: boolean;
  doc_type_slugs: string[];
  can_edit: boolean;
  can_unbind: boolean;
}

export type EffectiveRefDocItemVO = EffectiveRefDocVO;

export interface EffectiveRefDocsResponse {
  target_scope_label: string;
  total_count: number;
  total_estimated_tokens: number;
  is_whole_repo_rollup: boolean;
  items: EffectiveRefDocVO[];
}

export interface RefDocCitationVO {
  citation_key: string;
  doc_id: number;
  version_no: number;
  section_id?: string;
  section_title?: string;
  doc_title: string;
  scope_level: RefDocScopeLevel;
  stale: boolean;
}

export interface TaskReferenceDocSnapshotVO {
  doc_id: number;
  title: string;
  doc_kind: RefDocKind;
  version_no: number;
  summary_md: string;
  scope_level: RefDocScopeLevel;
  source_scope_label: string;
  estimated_tokens: number;
  is_inherited: boolean;
}
