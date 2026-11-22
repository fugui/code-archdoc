package handlers

import "github.com/gin-gonic/gin"

// 阶段三冻结机器可读错误码常量表（01-http-api-contract.md §1.4）
const (
	ErrCodeInvalidParam            = "INVALID_PARAM"
	ErrCodeInvalidSubdir           = "INVALID_SUBDIR"
	ErrCodeInvalidSlug             = "INVALID_SLUG"
	ErrCodeDocTypeInactive         = "DOC_TYPE_INACTIVE"
	ErrCodeEngineUnavailable       = "ENGINE_UNAVAILABLE"
	ErrCodeUnauthorized            = "UNAUTHORIZED"
	ErrCodeForbidden               = "FORBIDDEN"
	ErrCodeDraftAccessDenied       = "DRAFT_ACCESS_DENIED"
	ErrCodePathEscape              = "PATH_ESCAPE"
	ErrCodeNotFound                = "NOT_FOUND"
	ErrCodeConflict                = "CONFLICT"
	ErrCodeMigrationConflict       = "MIGRATION_CONFLICT"
	ErrCodeDocTypeInUse            = "DOC_TYPE_IN_USE"
	ErrCodeOptimisticLockConflict  = "OPTIMISTIC_LOCK_CONFLICT"
	ErrCodeVerifierBlocked         = "VERIFIER_BLOCKED"
	ErrCodeQueueFull               = "QUEUE_FULL"
	ErrCodeRemoteBranchFetchFailed = "REMOTE_BRANCH_FETCH_FAILED"
	ErrCodeInternalError           = "INTERNAL_ERROR"
)

// WriteError 输出统一机器可读错误响应（向后兼容 error 字段并附带 code 与可选 details）。
func WriteError(c *gin.Context, status int, code, msg string, details any) {
	payload := gin.H{
		"error": msg,
		"code":  code,
	}
	if details != nil {
		payload["details"] = details
	}
	c.JSON(status, payload)
}
