package models

import "errors"

// 领域通用哨兵错误
var (
	ErrNotFound               = errors.New("目标记录不存在")
	ErrInvalidStateTransition = errors.New("非法的任务状态转移")
	ErrOptimisticLockConflict = errors.New("文档修订版本已过期，请刷新后重试")
	ErrDocTypeInUse           = errors.New("该文档类型已被已发布文档引用，禁止物理删除")
)

// ModelTier 枚举
const (
	ModelTierFast     = "fast"
	ModelTierFlagship = "flagship"
)

func IsValidModelTier(v string) bool {
	return v == ModelTierFast || v == ModelTierFlagship
}

// FacetName 枚举（9 大 Canonical Code IR 分片）
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
)

func IsValidFacetName(v string) bool {
	switch v {
	case FacetTree, FacetBuild, FacetDeps, FacetConfigKeys, FacetSymbols,
		FacetIDLContracts, FacetHTTPRoutes, FacetDataModels, FacetRuntimePrimitives:
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
)

func IsValidTaskKind(v string) bool {
	return v == TaskKindGenerate || v == TaskKindRegenerate || v == TaskKindIncrementalRefresh
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
	ProposalStatusNone       = "none"
	ProposalStatusPending    = "pending"
	ProposalStatusAccepted   = "accepted"
	ProposalStatusRejected   = "rejected"
	ProposalStatusSuperseded = "superseded"
)

func IsValidProposalStatus(v string) bool {
	switch v {
	case ProposalStatusNone, ProposalStatusPending, ProposalStatusAccepted, ProposalStatusRejected, ProposalStatusSuperseded:
		return true
	default:
		return false
	}
}

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
)

func IsValidSymbolKind(v string) bool {
	switch v {
	case SymbolKindFile, SymbolKindConfigKey, SymbolKindProtoRPC, SymbolKindHTTPRoute,
		SymbolKindDataModel, SymbolKindCppClass, SymbolKindCppStruct, SymbolKindCppFunc,
		SymbolKindJavaClass, SymbolKindJavaMethod, SymbolKindPyClass, SymbolKindPyFunc,
		SymbolKindRuntimePrimitive:
		return true
	default:
		return false
	}
}
