package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"code-archdoc/models"
	"code-archdoc/pkg/sandbox"
	commonAuth "code-common/backend/auth"
	commonModels "code-common/backend/models"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
)

// CurrentUserFromContext 从 Gin 上下文提取当前认证用户并组装为 commonModels.User（兼容 DB 模式与纯 Claims 模式）。
func CurrentUserFromContext(c *gin.Context) (*commonModels.User, bool) {
	uc := commonAuth.GetUserContext(c)
	if uc == nil || uc.UserID == 0 {
		return nil, false
	}

	rolesJSON, err := json.Marshal(uc.Roles)
	if err != nil {
		rolesJSON = []byte("[]")
	}

	u := &commonModels.User{
		ID:           uc.UserID,
		Username:     uc.Username,
		Name:         uc.Name,
		Email:        uc.Email,
		EmployeeID:   uc.EmployeeID,
		DepartmentID: uc.DepartmentID,
		Roles:        datatypes.JSON(rolesJSON),
		IsActive:     true,
	}
	u.IsAdmin = u.IsSuperAdmin()
	return u, true
}

// CheckScopeWriteOrAbort 在写接口处理器中执行三层 RBAC 校验，失败时直接输出标准 400/401/403/404 响应并返回 false。
func (h *AppHandlers) CheckScopeWriteOrAbort(c *gin.Context, repoID uint, rawSubdir string) (*commonModels.User, string, bool) {
	user, ok := CurrentUserFromContext(c)
	if !ok {
		WriteError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "未登录或会话已失效", nil)
		return nil, "", false
	}

	normSubdir, normErr := sandbox.NormalizeSubdir(rawSubdir)
	if normErr != nil {
		WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, normErr.Error(), nil)
		return nil, "", false
	}

	allowed, err := h.rbacSvc.CheckScopeWritePermission(c.Request.Context(), user, repoID, normSubdir)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			WriteError(c, http.StatusNotFound, ErrCodeNotFound, "目标代码仓库不存在", nil)
			return nil, "", false
		}
		if errors.Is(err, sandbox.ErrInvalidSubdir) {
			WriteError(c, http.StatusBadRequest, ErrCodeInvalidSubdir, err.Error(), nil)
			return nil, "", false
		}
		WriteError(c, http.StatusInternalServerError, ErrCodeInternalError, "权限校验失败", nil)
		return nil, "", false
	}
	if !allowed {
		WriteError(c, http.StatusForbidden, ErrCodeForbidden, "无权操作该代码仓或子目录范围", nil)
		return nil, "", false
	}

	return user, normSubdir, true
}
