package models

import (
	"errors"
	"strings"
)

// 领域通用哨兵错误
var (
	ErrNotFound               = errors.New("目标记录不存在")
	ErrInvalidStateTransition = errors.New("非法的任务状态转移")
	ErrOptimisticLockConflict = errors.New("文档修订版本已过期，请刷新后重试")
	ErrDocTypeInUse           = errors.New("该文档类型已被已发布文档引用，禁止物理删除")
	ErrMigrationConflict      = errors.New("目标模块范围已存在同名文档或存在运行中任务，迁移已取消")
	ErrReviewCommentConflict  = errors.New("当前批注已关联提案或已采纳解决，仅允许撤回或删除草稿态批注")
)

// FacetName 枚举（9 大 Canonical Code IR 分片 + 第 10 个派生分片 module_context）
const (
	FacetTree              = "tree"
	FacetBuild             = "build"
	FacetDeps              = "deps"
	FacetConfigKeys        = "config_keys"
	FacetSymbols           = "symbols"
	FacetIDLContracts      = "idl_contracts"
	FacetHTTPRoutes        = "http_routes"
	FacetDataModels        = "data_models"
	FacetRuntimePrimitives = "runtime_primitives"
	FacetModuleContext     = "module_context"
)

func IsValidFacetName(v string) bool {
	switch v {
	case FacetTree, FacetBuild, FacetDeps, FacetConfigKeys, FacetSymbols,
		FacetIDLContracts, FacetHTTPRoutes, FacetDataModels, FacetRuntimePrimitives,
		FacetModuleContext:
		return true
	default:
		return false
	}
}

// SectionOwnership 枚举
const (
	OwnershipAuto   = "auto"
	OwnershipHybrid = "hybrid"
	OwnershipLocked = "locked"
)

func IsValidSectionOwnership(v string) bool {
	return v == OwnershipAuto || v == OwnershipHybrid || v == OwnershipLocked
}

// DocumentStatus 枚举
const (
	DocStatusDraft    = "draft"
	DocStatusApproved = "approved"
)

func IsValidDocumentStatus(v string) bool {
	return v == DocStatusDraft || v == DocStatusApproved
}

// RevisionSource 枚举
const (
	RevSourceAIGenerate         = "ai_generate"
	RevSourceIncrementalRefresh = "incremental_refresh"
	RevSourceManualEdit         = "manual_edit"
	RevSourceChatRefine         = "chat_refine"
	RevSourceRollback           = "rollback"
)

func IsValidRevisionSource(v string) bool {
	switch v {
	case RevSourceAIGenerate, RevSourceIncrementalRefresh, RevSourceManualEdit, RevSourceChatRefine, RevSourceRollback:
		return true
	default:
		return false
	}
}

// TaskKind 枚举
const (
	TaskKindGenerate           = "generate"
	TaskKindRegenerate         = "regenerate"
	TaskKindIncrementalRefresh = "incremental_refresh"
	TaskKindBatch              = "batch"
)

func IsValidTaskKind(v string) bool {
	return v == TaskKindGenerate || v == TaskKindRegenerate || v == TaskKindIncrementalRefresh || v == TaskKindBatch
}

// TaskPriority 枚举
const (
	TaskPriorityInteractive = "interactive"
	TaskPriorityBatch       = "batch"
)

func IsValidTaskPriority(v string) bool {
	return v == TaskPriorityInteractive || v == TaskPriorityBatch
}

// TaskStatus 枚举
const (
	TaskStatusPending    = "pending"
	TaskStatusExtracting = "extracting"
	TaskStatusGenerating = "generating"
	TaskStatusVerifying  = "verifying"
	TaskStatusReady      = "ready"
	TaskStatusFailed     = "failed"
	TaskStatusCanceled   = "canceled"
)

func IsValidTaskStatus(v string) bool {
	switch v {
	case TaskStatusPending, TaskStatusExtracting, TaskStatusGenerating, TaskStatusVerifying,
		TaskStatusReady, TaskStatusFailed, TaskStatusCanceled:
		return true
	default:
		return false
	}
}

// CanTransitionTaskStatus 校验主任务状态机转移是否合法（严格对齐契约表）。
func CanTransitionTaskStatus(from, to string) bool {
	switch from {
	case TaskStatusPending:
		return to == TaskStatusExtracting || to == TaskStatusCanceled || to == TaskStatusFailed
	case TaskStatusExtracting:
		return to == TaskStatusGenerating || to == TaskStatusFailed || to == TaskStatusCanceled
	case TaskStatusGenerating:
		return to == TaskStatusVerifying || to == TaskStatusFailed || to == TaskStatusCanceled
	case TaskStatusVerifying:
		return to == TaskStatusReady || to == TaskStatusFailed
	case TaskStatusReady, TaskStatusFailed, TaskStatusCanceled:
		return false
	default:
		return false
	}
}

// CanTransitionBatchTaskStatus 校验批次父任务（task_kind="batch"）状态转移（ADR-04 §7.2：允许 generating -> ready）。
func CanTransitionBatchTaskStatus(from, to string) bool {
	if CanTransitionTaskStatus(from, to) {
		return true
	}
	return from == TaskStatusGenerating && to == TaskStatusReady
}

// SlotTaskStatus 枚举
const (
	SlotTaskPending       = "pending"
	SlotTaskRunning       = "running"
	SlotTaskCompleted     = "completed"
	SlotTaskFailed        = "failed"
	SlotTaskSkippedLocked = "skipped_locked"
	SlotTaskCanceled      = "canceled"
)

func IsValidSlotTaskStatus(v string) bool {
	switch v {
	case SlotTaskPending, SlotTaskRunning, SlotTaskCompleted, SlotTaskFailed, SlotTaskSkippedLocked, SlotTaskCanceled:
		return true
	default:
		return false
	}
}

// ProposalStatus 枚举
const (
	ProposalStatusNone            = "none"
	ProposalStatusPending         = "pending"
	ProposalStatusPartialAccepted = "partial_accepted"
	ProposalStatusAccepted        = "accepted"
	ProposalStatusRejected        = "rejected"
	ProposalStatusSuperseded      = "superseded"
)

func IsValidProposalStatus(v string) bool {
	switch v {
	case ProposalStatusNone, ProposalStatusPending, ProposalStatusPartialAccepted,
		ProposalStatusAccepted, ProposalStatusRejected, ProposalStatusSuperseded:
		return true
	default:
		return false
	}
}

// ChatRefineMode / ChatScopeType / ChatIntent 枚举
const (
	ChatModeRevise       = "revise"
	ChatModeAsk          = "ask"
	ChatIntentRevise     = "revise"
	ChatIntentAsk        = "ask"
	ChatIntentPlanRefine = "plan_refine"
	ChatScopeSlot        = "slot"
	ChatScopeInline      = "inline"
	ChatScopeGlobal      = "global"
	ChatScopeReviewBatch = "review_batch"
)

// ChatModeReviseCompat 返回默认精修模式标识（同时兼容旧前端/测试传入 refine）。
func ChatModeReviseCompat() string { return ChatModeRevise }

// DriftType 枚举
const (
	DriftTypeCode = "code"
	DriftTypeSpec = "spec"
)

func IsValidDriftType(v string) bool {
	return v == DriftTypeCode || v == DriftTypeSpec
}

// DriftPrecision 枚举
const (
	DriftPrecisionSectionLevel = "section_level"
	DriftPrecisionRepoLevel    = "repo_level"
)

func IsValidDriftPrecision(v string) bool {
	return v == DriftPrecisionSectionLevel || v == DriftPrecisionRepoLevel
}

// MetricEvent 枚举
const (
	MetricEventAIDraftVerified      = "ai_draft_verified"
	MetricEventDocPublished         = "doc_published"
	MetricEventChatProposalResolved = "chat_proposal_resolved"
)

func IsValidMetricEvent(v string) bool {
	return v == MetricEventAIDraftVerified || v == MetricEventDocPublished || v == MetricEventChatProposalResolved
}

// SymbolKind 枚举（ADR-03 证据锚点符号类别）
const (
	SymbolKindFile             = "file"
	SymbolKindConfigKey        = "config_key"
	SymbolKindProtoRPC         = "proto_rpc"
	SymbolKindHTTPRoute        = "http_route"
	SymbolKindDataModel        = "data_model"
	SymbolKindCppClass         = "cpp_class"
	SymbolKindCppStruct        = "cpp_struct"
	SymbolKindCppFunc          = "cpp_func"
	SymbolKindJavaClass        = "java_class"
	SymbolKindJavaMethod       = "java_method"
	SymbolKindPyClass          = "py_class"
	SymbolKindPyFunc           = "py_func"
	SymbolKindRuntimePrimitive = "runtime_primitive"
	SymbolKindModuleDoc        = "module_doc"
)

func IsValidSymbolKind(v string) bool {
	switch v {
	case SymbolKindFile, SymbolKindConfigKey, SymbolKindProtoRPC, SymbolKindHTTPRoute,
		SymbolKindDataModel, SymbolKindCppClass, SymbolKindCppStruct, SymbolKindCppFunc,
		SymbolKindJavaClass, SymbolKindJavaMethod, SymbolKindPyClass, SymbolKindPyFunc,
		SymbolKindRuntimePrimitive, SymbolKindModuleDoc:
		return true
	default:
		return false
	}
}

// ModuleStrategy 枚举（ADR-04 D-1 仓库模块发现策略）
const (
	ModuleStrategyArchElements = "arch_elements"
	ModuleStrategyTopLevelDirs = "top_level_dirs"
	ModuleStrategyMerged       = "merged"
)

func IsValidModuleStrategy(v string) bool {
	return v == ModuleStrategyArchElements || v == ModuleStrategyTopLevelDirs || v == ModuleStrategyMerged
}

// ModuleSource 枚举（ADR-04 D-1 模块来源）
const (
	ModuleSourceArchElement = "arch_element"
	ModuleSourceDiscovered  = "discovered"
)

// ModuleStatus 枚举（ADR-04 D-1 模块状态）
const (
	ModuleStatusActive  = "active"
	ModuleStatusMissing = "missing"
	ModuleStatusIgnored = "ignored"
)

func IsValidModuleStatus(v string) bool {
	return v == ModuleStatusActive || v == ModuleStatusMissing || v == ModuleStatusIgnored
}

// AnchorScope 枚举（ADR-04 D-3 证据锚点作用域）
const (
	AnchorScopeModule = "module"
	AnchorScopeRepo   = "repo"
)

// GenEngine 枚举（ADR-08 双引擎文档生成）
const (
	EngineNative   = "native"
	EngineOpenCode = "opencode"
)

// IsValidEngine 校验生成引擎标识是否合法。
func IsValidEngine(v string) bool {
	return v == EngineNative || v == EngineOpenCode
}

// NormalizeEngine 归一化生成引擎标识（空串与非法值统一回退至 native）。
func NormalizeEngine(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if IsValidEngine(v) {
		return v
	}
	return EngineNative
}
