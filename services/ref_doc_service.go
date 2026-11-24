package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/refdoc"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/verifier"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

const (
	maxWholeRepoRollupRefDocs = 12
	maxArchAncestorDepth      = 16
)

var (
	ErrInvalidRefDocParam     = errors.New("参考文档请求参数非法")
	ErrRefDocForbidden        = errors.New("无权操作目标架构作用域的参考文档")
	ErrRefDocArchivedReadOnly = errors.New("已归档参考文档不可新增消费绑定")
)

// RefDocGateError 包装 pkg/refdoc 返回的 400/413/422 校验错误。
type RefDocGateError struct {
	Failure *refdoc.GateFailure
}

func (e *RefDocGateError) Error() string {
	if e == nil || e.Failure == nil {
		return "参考文档内容校验未通过"
	}
	return e.Failure.Message
}

// RefDocVersionConflictError 携带服务端当前版本号用于返回 409 VERSION_CONFLICT。
type RefDocVersionConflictError struct {
	CurrentVersion int
}

func (e *RefDocVersionConflictError) Error() string {
	return fmt.Sprintf("参考文档版本冲突：服务端当前已更新至 v%d", e.CurrentVersion)
}

// RefDocBindingInput 描述创建或转移参考文档绑定时的入参。
type RefDocBindingInput struct {
	ScopeLevel        string   `json:"scope_level"`
	ArchElementID     *uint    `json:"arch_element_id"`
	RepoID            *uint    `json:"repo_id"`
	Subdirectory      string   `json:"subdirectory"`
	InheritToChildren *bool    `json:"inherit_to_children"`
	DocTypeSlugs      []string `json:"doc_type_slugs"`
	SortOrder         int      `json:"sort_order"`
}

// CreateRefDocRequest 创建参考文档入参。
type CreateRefDocRequest struct {
	Title          string             `json:"title"`
	DocKind        string             `json:"doc_kind"`
	Description    string             `json:"description"`
	SummaryMD      string             `json:"summary_md"`
	ContentMD      string             `json:"content_md"`
	DeclaredTerms  []string           `json:"declared_terms"`
	RedactAndSave  bool               `json:"redact_and_save"`
	InitialBinding RefDocBindingInput `json:"initial_binding"`
}

// UpdateRefDocRequest 乐观锁更新参考文档入参。
type UpdateRefDocRequest struct {
	ExpectedVersion int                 `json:"expected_version"`
	Title           *string             `json:"title"`
	DocKind         *string             `json:"doc_kind"`
	Description     *string             `json:"description"`
	SummaryMD       *string             `json:"summary_md"`
	ContentMD       *string             `json:"content_md"`
	DeclaredTerms   *[]string           `json:"declared_terms"`
	Status          *string             `json:"status"`
	RedactAndSave   bool                `json:"redact_and_save"`
	NewHomeBinding  *RefDocBindingInput `json:"new_home_binding"`
}

// UpdateRefDocBindingRequest 更新单条消费绑定配置入参。
type UpdateRefDocBindingRequest struct {
	InheritToChildren *bool     `json:"inherit_to_children"`
	DocTypeSlugs      *[]string `json:"doc_type_slugs"`
	SortOrder         *int      `json:"sort_order"`
}

// RefDocListQuery 分页检索参考文档库查询条件。
type RefDocListQuery struct {
	Search        string
	DocKind       string
	ScopeLevel    string
	ArchElementID *uint
	Status        string
	Page          int
	PageSize      int
}

// ReferenceDocSummaryVO 参考文档列表视图对象。
type ReferenceDocSummaryVO struct {
	ID              uint      `json:"id"`
	Title           string    `json:"title"`
	DocKind         string    `json:"doc_kind"`
	Description     string    `json:"description"`
	OwnerScopeKey   string    `json:"owner_scope_key"`
	OwnerScopeLevel string    `json:"owner_scope_level"`
	OwnerScopeLabel string    `json:"owner_scope_label"`
	CurrentVersion  int       `json:"current_version"`
	EstimatedTokens int       `json:"estimated_tokens"`
	Status          string    `json:"status"`
	BindingCount    int       `json:"binding_count"`
	CanEdit         bool      `json:"can_edit"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ReferenceDocListResponse 分页响应。
type ReferenceDocListResponse struct {
	Items    []ReferenceDocSummaryVO `json:"items"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"page_size"`
}

// RefDocBindingVO 参考文档绑定关系视图对象。
type RefDocBindingVO struct {
	ID                uint     `json:"id"`
	DocID             uint     `json:"doc_id"`
	ScopeKey          string   `json:"scope_key"`
	ScopeLevel        string   `json:"scope_level"`
	ScopeLabel        string   `json:"scope_label"`
	IsHome            bool     `json:"is_home"`
	ArchElementID     *uint    `json:"arch_element_id,omitempty"`
	RepoID            *uint    `json:"repo_id,omitempty"`
	Subdirectory      string   `json:"subdirectory"`
	InheritToChildren bool     `json:"inherit_to_children"`
	DocTypeSlugs      []string `json:"doc_type_slugs"`
	SortOrder         int      `json:"sort_order"`
	CanManageBinding  bool     `json:"can_manage_binding"`
}

// ReferenceDocDetailVO 单篇参考文档详情与正文视图对象。
type ReferenceDocDetailVO struct {
	ID                uint                 `json:"id"`
	Title             string               `json:"title"`
	DocKind           string               `json:"doc_kind"`
	Description       string               `json:"description"`
	Visibility        string               `json:"visibility"`
	OwnerScopeKey     string               `json:"owner_scope_key"`
	OwnerScopeLevel   string               `json:"owner_scope_level"`
	OwnerScopeLabel   string               `json:"owner_scope_label"`
	SummaryMD         string               `json:"summary_md"`
	SummarySource     string               `json:"summary_source"`
	SummaryStatus     string               `json:"summary_status"`
	SummaryForVersion int                  `json:"summary_for_version"`
	SummaryStale      bool                 `json:"summary_stale"`
	CurrentVersion    int                  `json:"current_version"`
	ViewingVersion    int                  `json:"viewing_version"`
	ContentSHA256     string               `json:"content_sha256"`
	ByteSize          int64                `json:"byte_size"`
	RuneCount         int                  `json:"rune_count"`
	EstimatedTokens   int                  `json:"estimated_tokens"`
	LineCount         int                  `json:"line_count"`
	Status            string               `json:"status"`
	TOC               []models.HeadingNode `json:"toc"`
	DeclaredTerms     []string             `json:"declared_terms"`
	ContentMD         string               `json:"content_md"`
	Bindings          []RefDocBindingVO    `json:"bindings"`
	CanEdit           bool                 `json:"can_edit"`
	CreatedBy         uint                 `json:"created_by"`
	UpdatedBy         uint                 `json:"updated_by"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// EffectiveRefDocItemVO 作用域解析后的单篇生效参考文档视图。
type EffectiveRefDocItemVO struct {
	DocID               uint                 `json:"doc_id"`
	BindingID           uint                 `json:"binding_id"`
	Title               string               `json:"title"`
	DocKind             string               `json:"doc_kind"`
	CurrentVersion      int                  `json:"current_version"`
	SummaryMD           string               `json:"summary_md"`
	SummaryStale        bool                 `json:"summary_stale"`
	TOC                 []models.HeadingNode `json:"toc"`
	EstimatedTokens     int                  `json:"estimated_tokens"`
	IsHome              bool                 `json:"is_home"`
	IsInherited         bool                 `json:"is_inherited"`
	EffectiveScopeLevel string               `json:"effective_scope_level"`
	SourceScopeLabel    string               `json:"source_scope_label"`
	InheritToChildren   bool                 `json:"inherit_to_children"`
	DocTypeSlugs        []string             `json:"doc_type_slugs"`
	CanEdit             bool                 `json:"can_edit"`
	CanUnbind           bool                 `json:"can_unbind"`
}

// EffectiveRefDocsResponse 解析生效参考文档列表响应。
type EffectiveRefDocsResponse struct {
	TargetScopeLabel     string                            `json:"target_scope_label"`
	TotalCount           int                               `json:"total_count"`
	TotalEstimatedTokens int                               `json:"total_estimated_tokens"`
	IsWholeRepoRollup    bool                              `json:"is_whole_repo_rollup,omitempty"`
	Items                []EffectiveRefDocItemVO           `json:"items"`
	Snapshots            []models.TaskReferenceDocSnapshot `json:"-"`
}

// ResolveEffectiveRefDocsRequest 解析目标作用域生效参考文档请求。
type ResolveEffectiveRefDocsRequest struct {
	ArchElementID *uint
	RepoID        *uint
	Subdirectory  string
	DocTypeSlug   string
	ExcludeDocIDs []uint
}

// RefDocAuditSnapshot 脱敏审计快照（严禁包含完整正文或脱敏前敏感片段）。
type RefDocAuditSnapshot struct {
	DocID          uint   `json:"doc_id"`
	Title          string `json:"title"`
	DocKind        string `json:"doc_kind"`
	OwnerScopeKey  string `json:"owner_scope_key"`
	CurrentVersion int    `json:"current_version"`
	ContentSHA256  string `json:"content_sha256"`
	Status         string `json:"status"`
	Redacted       bool   `json:"redacted,omitempty"`
}

// RefDocBindingAuditSnapshot 绑定操作审计快照。
type RefDocBindingAuditSnapshot struct {
	DocID             uint     `json:"doc_id"`
	BindingID         uint     `json:"binding_id"`
	ScopeKey          string   `json:"scope_key"`
	ScopeLevel        string   `json:"scope_level"`
	IsHome            bool     `json:"is_home"`
	InheritToChildren bool     `json:"inherit_to_children"`
	DocTypeSlugs      []string `json:"doc_type_slugs"`
}

// RefDocService 编排架构参考文档的门禁校验、乐观锁原子落盘、归属 RBAC 与四层继承解析。
type RefDocService struct {
	refDocRepo ReferenceDocRepo
	masterRepo SharedMasterRepo
	rbacSvc    *RBACService
	refDocsDir string
	maxBytes   int64
	docLocks   sync.Map // docID(uint) -> *sync.Mutex
}

func NewRefDocService(
	refDocRepo ReferenceDocRepo,
	masterRepo SharedMasterRepo,
	rbacSvc *RBACService,
	refDocsDir string,
	maxBytes int64,
) (*RefDocService, error) {
	if strings.TrimSpace(refDocsDir) == "" {
		return nil, fmt.Errorf("refDocsDir 不能为空")
	}
	absDir, err := filepath.Abs(refDocsDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o750); err != nil {
		return nil, fmt.Errorf("初始化 ref_docs 目录失败: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = refdoc.DefaultMaxDocBytes
	}
	return &RefDocService{
		refDocRepo: refDocRepo,
		masterRepo: masterRepo,
		rbacSvc:    rbacSvc,
		refDocsDir: absDir,
		maxBytes:   maxBytes,
	}, nil
}

func (s *RefDocService) lockDoc(docID uint) *sync.Mutex {
	mu, _ := s.docLocks.LoadOrStore(docID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

type resolvedBindingTarget struct {
	ScopeKey          string
	ScopeLevel        string
	ArchElementID     *uint
	RepoID            *uint
	Subdirectory      string
	InheritToChildren bool
	DocTypeSlugs      []string
	SortOrder         int
}

func (s *RefDocService) resolveAndAuthorizeBindingTarget(
	ctx context.Context,
	user *commonModels.User,
	in RefDocBindingInput,
) (*resolvedBindingTarget, error) {
	level := strings.TrimSpace(in.ScopeLevel)
	if !models.IsValidRefDocScopeLevel(level) {
		return nil, fmt.Errorf("%w: scope_level 必须为 system | subsystem | group | repo | module", ErrInvalidRefDocParam)
	}

	inherit := true
	if in.InheritToChildren != nil {
		inherit = *in.InheritToChildren
	}
	if level == models.RefDocScopeSystem && !inherit {
		return nil, fmt.Errorf("%w: 全系统级参考文档强制向下继承，inherit_to_children 不可为 false", ErrInvalidRefDocParam)
	}

	slugs := dedupeTrimmedStrings(in.DocTypeSlugs)

	var scopeKey string
	var archIDPtr *uint
	var repoIDPtr *uint
	var normSubdir string

	switch level {
	case models.RefDocScopeSystem:
		scopeKey = models.RefDocScopeSystem
	case models.RefDocScopeSubsystem, models.RefDocScopeGroup, models.RefDocScopeModule:
		if in.ArchElementID == nil || *in.ArchElementID == 0 {
			return nil, fmt.Errorf("%w: arch_element_id 必填", ErrInvalidRefDocParam)
		}
		el, err := s.masterRepo.GetArchElement(ctx, *in.ArchElementID)
		if err != nil {
			return nil, err
		}
		if el.Type != level {
			return nil, fmt.Errorf("%w: 架构节点 #%d 类型为 %s，与 scope_level=%s 不匹配", ErrInvalidRefDocParam, el.ID, el.Type, level)
		}
		idVal := el.ID
		archIDPtr = &idVal
		scopeKey = fmt.Sprintf("arch:%d", el.ID)
	case models.RefDocScopeRepo:
		if in.RepoID == nil || *in.RepoID == 0 {
			return nil, fmt.Errorf("%w: repo_id 必填", ErrInvalidRefDocParam)
		}
		var err error
		normSubdir, err = sandbox.NormalizeSubdir(in.Subdirectory)
		if err != nil {
			return nil, err
		}
		if _, err := s.masterRepo.GetRepository(ctx, *in.RepoID); err != nil {
			return nil, err
		}
		idVal := *in.RepoID
		repoIDPtr = &idVal
		scopeKey = fmt.Sprintf("repo:%d:%s", idVal, normSubdir)
	}

	allowed, err := s.rbacSvc.CheckArchScopeWritePermission(ctx, user, level, archIDPtr, repoIDPtr, normSubdir)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrRefDocForbidden
	}

	return &resolvedBindingTarget{
		ScopeKey:          scopeKey,
		ScopeLevel:        level,
		ArchElementID:     archIDPtr,
		RepoID:            repoIDPtr,
		Subdirectory:      normSubdir,
		InheritToChildren: inherit,
		DocTypeSlugs:      slugs,
		SortOrder:         in.SortOrder,
	}, nil
}

func (s *RefDocService) checkDocOwnerWritePermission(ctx context.Context, user *commonModels.User, doc *models.ReferenceDoc) (bool, error) {
	if user == nil || user.ID == 0 || doc == nil {
		return false, nil
	}
	if user.HasRole(config.AdminRole) {
		return true, nil
	}
	allowed, err := s.rbacSvc.CheckArchScopeWritePermission(
		ctx,
		user,
		doc.OwnerScopeLevel,
		doc.OwnerArchElementID,
		doc.OwnerRepoID,
		doc.OwnerSubdirectory,
	)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			// 原归属架构节点或仓库在主数据中已删除时，回退管理员或原作者
			return doc.CreatedBy == user.ID, nil
		}
		return false, err
	}
	return allowed, nil
}

func (s *RefDocService) checkBindingWritePermission(ctx context.Context, user *commonModels.User, b models.ReferenceDocBinding) (bool, error) {
	if user == nil || user.ID == 0 {
		return false, nil
	}
	allowed, err := s.rbacSvc.CheckArchScopeWritePermission(ctx, user, b.ScopeLevel, b.ArchElementID, b.RepoID, b.Subdirectory)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return user.HasRole(config.AdminRole), nil
		}
		return false, err
	}
	return allowed, nil
}

func (s *RefDocService) writeVersionFileAtomic(docID uint, versionNo int, content string) (string, string, error) {
	relDir := fmt.Sprintf("%d", docID)
	docDir, err := sandbox.SafeJoin(s.refDocsDir, relDir)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(docDir, 0o750); err != nil {
		return "", "", err
	}

	relFile := fmt.Sprintf("%d/v%d.md", docID, versionNo)
	finalPath, err := sandbox.SafeJoin(s.refDocsDir, relFile)
	if err != nil {
		return "", "", err
	}

	tmpFile, err := os.CreateTemp(docDir, fmt.Sprintf(".tmp-v%d-*", versionNo))
	if err != nil {
		return "", "", err
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(0o640); err != nil {
		_ = tmpFile.Close()
		return "", "", err
	}
	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()
		return "", "", err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return "", "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", "", err
	}
	cleanupTmp = false
	return relFile, finalPath, nil
}

// ReadVersionContent 安全读取指定参考文档版本的 Markdown 正文。
func (s *RefDocService) ReadVersionContent(docID uint, versionNo int) (string, error) {
	if docID == 0 || versionNo <= 0 {
		return "", models.ErrNotFound
	}
	relFile := fmt.Sprintf("%d/v%d.md", docID, versionNo)
	fullPath, err := sandbox.SafeJoin(s.refDocsDir, relFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", models.ErrNotFound
		}
		return "", err
	}
	return string(data), nil
}

// CreateRefDoc 创建参考文档并建立 is_home=true 归属绑定，原子落盘 v1.md。
func (s *RefDocService) CreateRefDoc(ctx context.Context, req CreateRefDocRequest, user *commonModels.User) (*ReferenceDocDetailVO, *RefDocAuditSnapshot, error) {
	if user == nil || user.ID == 0 {
		return nil, nil, ErrRefDocForbidden
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || len([]rune(title)) > 200 {
		return nil, nil, fmt.Errorf("%w: title 不能为空且不可超过 200 字符", ErrInvalidRefDocParam)
	}
	docKind := strings.TrimSpace(req.DocKind)
	if docKind == "" {
		docKind = models.RefDocKindArchDesign
	}
	if !models.IsValidRefDocKind(docKind) {
		return nil, nil, fmt.Errorf("%w: doc_kind 非法", ErrInvalidRefDocParam)
	}
	desc := strings.TrimSpace(req.Description)
	if len([]rune(desc)) > 500 {
		return nil, nil, fmt.Errorf("%w: description 不可超过 500 字符", ErrInvalidRefDocParam)
	}

	target, err := s.resolveAndAuthorizeBindingTarget(ctx, user, req.InitialBinding)
	if err != nil {
		return nil, nil, err
	}

	parsed, gateFail, err := refdoc.ValidateAndParse(req.ContentMD, s.maxBytes, req.RedactAndSave, docKind, req.DeclaredTerms)
	if err != nil {
		return nil, nil, err
	}
	if gateFail != nil {
		return nil, nil, &RefDocGateError{Failure: gateFail}
	}

	summaryMD := strings.TrimSpace(req.SummaryMD)
	summarySource := models.RefDocSummarySourceManual
	if summaryMD == "" {
		summaryMD = refdoc.BuildFallbackSummary(parsed.Markdown, parsed.TOC, 300)
		summarySource = models.RefDocSummarySourceFallback
	}

	tocJSON, _ := json.Marshal(parsed.TOC)
	termsJSON, _ := json.Marshal(parsed.DeclaredTerms)
	slugsJSON, _ := json.Marshal(target.DocTypeSlugs)

	doc := &models.ReferenceDoc{
		Title:              title,
		DocKind:            docKind,
		Description:        desc,
		Visibility:         models.RefDocVisibilityPublic,
		OwnerScopeKey:      target.ScopeKey,
		OwnerScopeLevel:    target.ScopeLevel,
		OwnerArchElementID: target.ArchElementID,
		OwnerRepoID:        target.RepoID,
		OwnerSubdirectory:  target.Subdirectory,
		SummaryMD:          summaryMD,
		SummarySource:      summarySource,
		SummaryStatus:      models.RefDocSummaryStatusReady,
		SummaryForVersion:  1,
		TOCJSON:            datatypes.JSON(tocJSON),
		DeclaredTerms:      datatypes.JSON(termsJSON),
		CurrentVersion:     1,
		StorageRelPath:     "",
		ContentSHA256:      parsed.ContentSHA256,
		ByteSize:           parsed.ByteSize,
		RuneCount:          parsed.RuneCount,
		EstimatedTokens:    parsed.EstimatedTokens,
		LineCount:          parsed.LineCount,
		Status:             models.RefDocStatusActive,
		CreatedBy:          user.ID,
		UpdatedBy:          user.ID,
	}
	homeBinding := &models.ReferenceDocBinding{
		ScopeKey:          target.ScopeKey,
		ScopeLevel:        target.ScopeLevel,
		IsHome:            true,
		ArchElementID:     target.ArchElementID,
		RepoID:            target.RepoID,
		Subdirectory:      target.Subdirectory,
		InheritToChildren: target.InheritToChildren,
		DocTypeSlugsJSON:  datatypes.JSON(slugsJSON),
		SortOrder:         target.SortOrder,
		CreatedBy:         user.ID,
	}

	if err := s.refDocRepo.CreateWithHomeBinding(ctx, doc, homeBinding); err != nil {
		return nil, nil, err
	}

	relPath, writtenPath, writeErr := s.writeVersionFileAtomic(doc.ID, 1, parsed.Markdown)
	if writeErr != nil {
		_ = s.refDocRepo.PurgeDoc(ctx, doc.ID)
		return nil, nil, writeErr
	}
	updatedDoc, updErr := s.refDocRepo.UpdateDocWithVersion(ctx, doc.ID, 1, map[string]any{
		"storage_rel_path": relPath,
	})
	if updErr != nil {
		_ = os.Remove(writtenPath)
		_ = s.refDocRepo.PurgeDoc(ctx, doc.ID)
		return nil, nil, updErr
	}

	detail, err := s.buildDetailVO(ctx, updatedDoc, 1, parsed.Markdown, user)
	if err != nil {
		return nil, nil, err
	}
	auditSnap := &RefDocAuditSnapshot{
		DocID:          updatedDoc.ID,
		Title:          updatedDoc.Title,
		DocKind:        updatedDoc.DocKind,
		OwnerScopeKey:  updatedDoc.OwnerScopeKey,
		CurrentVersion: updatedDoc.CurrentVersion,
		ContentSHA256:  updatedDoc.ContentSHA256,
		Status:         updatedDoc.Status,
		Redacted:       parsed.Redacted,
	}
	return detail, auditSnap, nil
}

// UpdateRefDoc 乐观锁更新参考文档元信息、发布新版本正文、归档/恢复或转移归属作用域。
// 严格遵循：校验 expected_version → 先临时文件+fsync+原子 rename 写 v<N+1>.md → 后 DB 条件事务更新（失败立删孤儿文件）。
func (s *RefDocService) UpdateRefDoc(
	ctx context.Context,
	docID uint,
	req UpdateRefDocRequest,
	user *commonModels.User,
) (*ReferenceDocDetailVO, *RefDocAuditSnapshot, *RefDocAuditSnapshot, error) {
	if req.ExpectedVersion <= 0 {
		return nil, nil, nil, fmt.Errorf("%w: expected_version 必填且必须大于 0", ErrInvalidRefDocParam)
	}
	existing, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, nil, nil, err
	}
	allowed, err := s.checkDocOwnerWritePermission(ctx, user, existing)
	if err != nil {
		return nil, nil, nil, err
	}
	if !allowed {
		return nil, nil, nil, ErrRefDocForbidden
	}

	var newHomeTarget *resolvedBindingTarget
	if req.NewHomeBinding != nil {
		newHomeTarget, err = s.resolveAndAuthorizeBindingTarget(ctx, user, *req.NewHomeBinding)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	mu := s.lockDoc(docID)
	mu.Lock()
	defer mu.Unlock()

	cur, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, nil, nil, err
	}
	if cur.CurrentVersion != req.ExpectedVersion {
		return nil, nil, nil, &RefDocVersionConflictError{CurrentVersion: cur.CurrentVersion}
	}

	beforeAudit := &RefDocAuditSnapshot{
		DocID:          cur.ID,
		Title:          cur.Title,
		DocKind:        cur.DocKind,
		OwnerScopeKey:  cur.OwnerScopeKey,
		CurrentVersion: cur.CurrentVersion,
		ContentSHA256:  cur.ContentSHA256,
		Status:         cur.Status,
	}

	nextTitle := cur.Title
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		if t == "" || len([]rune(t)) > 200 {
			return nil, nil, nil, fmt.Errorf("%w: title 不能为空且不可超过 200 字符", ErrInvalidRefDocParam)
		}
		nextTitle = t
	}
	nextKind := cur.DocKind
	if req.DocKind != nil {
		k := strings.TrimSpace(*req.DocKind)
		if !models.IsValidRefDocKind(k) {
			return nil, nil, nil, fmt.Errorf("%w: doc_kind 非法", ErrInvalidRefDocParam)
		}
		nextKind = k
	}
	nextDesc := cur.Description
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if len([]rune(d)) > 500 {
			return nil, nil, nil, fmt.Errorf("%w: description 不可超过 500 字符", ErrInvalidRefDocParam)
		}
		nextDesc = d
	}
	nextStatus := cur.Status
	if req.Status != nil {
		st := strings.TrimSpace(*req.Status)
		if !models.IsValidRefDocStatus(st) {
			return nil, nil, nil, fmt.Errorf("%w: status 必须为 active 或 archived", ErrInvalidRefDocParam)
		}
		nextStatus = st
	}

	var explicitTerms []string
	if req.DeclaredTerms != nil {
		explicitTerms = *req.DeclaredTerms
	} else {
		_ = json.Unmarshal(cur.DeclaredTerms, &explicitTerms)
	}

	updates := map[string]any{
		"title":       nextTitle,
		"doc_kind":    nextKind,
		"description": nextDesc,
		"status":      nextStatus,
		"updated_by":  user.ID,
	}

	nextVersion := cur.CurrentVersion
	var writtenFilePath string
	var effectiveContent string
	var redactedFlag bool

	if req.ContentMD != nil {
		parsed, gateFail, pErr := refdoc.ValidateAndParse(*req.ContentMD, s.maxBytes, req.RedactAndSave, nextKind, explicitTerms)
		if pErr != nil {
			return nil, nil, nil, pErr
		}
		if gateFail != nil {
			return nil, nil, nil, &RefDocGateError{Failure: gateFail}
		}
		redactedFlag = parsed.Redacted

		if parsed.ContentSHA256 != cur.ContentSHA256 {
			nextVersion = cur.CurrentVersion + 1
			relPath, fullPath, wErr := s.writeVersionFileAtomic(cur.ID, nextVersion, parsed.Markdown)
			if wErr != nil {
				return nil, nil, nil, wErr
			}
			writtenFilePath = fullPath
			effectiveContent = parsed.Markdown

			tocJSON, _ := json.Marshal(parsed.TOC)
			termsJSON, _ := json.Marshal(parsed.DeclaredTerms)
			updates["current_version"] = nextVersion
			updates["storage_rel_path"] = relPath
			updates["content_sha256"] = parsed.ContentSHA256
			updates["byte_size"] = parsed.ByteSize
			updates["rune_count"] = parsed.RuneCount
			updates["estimated_tokens"] = parsed.EstimatedTokens
			updates["line_count"] = parsed.LineCount
			updates["toc_json"] = datatypes.JSON(tocJSON)
			updates["declared_terms"] = datatypes.JSON(termsJSON)

			// 处理摘要更新与滞后检测
			if req.SummaryMD != nil {
				sVal := strings.TrimSpace(*req.SummaryMD)
				if sVal != "" {
					updates["summary_md"] = sVal
					updates["summary_source"] = models.RefDocSummarySourceManual
					updates["summary_status"] = models.RefDocSummaryStatusReady
					updates["summary_for_version"] = nextVersion
				} else {
					updates["summary_md"] = refdoc.BuildFallbackSummary(parsed.Markdown, parsed.TOC, 300)
					updates["summary_source"] = models.RefDocSummarySourceFallback
					updates["summary_status"] = models.RefDocSummaryStatusReady
					updates["summary_for_version"] = nextVersion
				}
			} else if cur.SummarySource != models.RefDocSummarySourceManual {
				updates["summary_md"] = refdoc.BuildFallbackSummary(parsed.Markdown, parsed.TOC, 300)
				updates["summary_source"] = models.RefDocSummarySourceFallback
				updates["summary_status"] = models.RefDocSummaryStatusReady
				updates["summary_for_version"] = nextVersion
			}
			// 若为手填摘要且本次仅更新正文，保留 SummaryForVersion 不变以触发 summary_stale 提示
		} else {
			effectiveContent = parsed.Markdown
			if req.DeclaredTerms != nil || nextKind != cur.DocKind {
				termsJSON, _ := json.Marshal(parsed.DeclaredTerms)
				updates["declared_terms"] = datatypes.JSON(termsJSON)
			}
		}
	} else if req.DeclaredTerms != nil || nextKind != cur.DocKind {
		curContent, rErr := s.ReadVersionContent(cur.ID, cur.CurrentVersion)
		if rErr == nil {
			effectiveContent = curContent
			if parsed, gf, _ := refdoc.ValidateAndParse(curContent, s.maxBytes, true, nextKind, explicitTerms); gf == nil && parsed != nil {
				termsJSON, _ := json.Marshal(parsed.DeclaredTerms)
				updates["declared_terms"] = datatypes.JSON(termsJSON)
			}
		}
	}

	if nextVersion == cur.CurrentVersion && req.SummaryMD != nil {
		sVal := strings.TrimSpace(*req.SummaryMD)
		if sVal != "" {
			updates["summary_md"] = sVal
			updates["summary_source"] = models.RefDocSummarySourceManual
			updates["summary_status"] = models.RefDocSummaryStatusReady
			updates["summary_for_version"] = cur.CurrentVersion
		} else {
			if effectiveContent == "" {
				effectiveContent, _ = s.ReadVersionContent(cur.ID, cur.CurrentVersion)
			}
			var toc []models.HeadingNode
			_ = json.Unmarshal(cur.TOCJSON, &toc)
			refTOC := toRefDocHeadingNodes(toc)
			updates["summary_md"] = refdoc.BuildFallbackSummary(effectiveContent, refTOC, 300)
			updates["summary_source"] = models.RefDocSummarySourceFallback
			updates["summary_status"] = models.RefDocSummaryStatusReady
			updates["summary_for_version"] = cur.CurrentVersion
		}
	}

	var updatedDoc *models.ReferenceDoc
	if newHomeTarget != nil {
		updates["owner_scope_key"] = newHomeTarget.ScopeKey
		updates["owner_scope_level"] = newHomeTarget.ScopeLevel
		updates["owner_arch_element_id"] = newHomeTarget.ArchElementID
		updates["owner_repo_id"] = newHomeTarget.RepoID
		updates["owner_subdirectory"] = newHomeTarget.Subdirectory

		slugsJSON, _ := json.Marshal(newHomeTarget.DocTypeSlugs)
		newHomeModel := &models.ReferenceDocBinding{
			ScopeKey:          newHomeTarget.ScopeKey,
			ScopeLevel:        newHomeTarget.ScopeLevel,
			IsHome:            true,
			ArchElementID:     newHomeTarget.ArchElementID,
			RepoID:            newHomeTarget.RepoID,
			Subdirectory:      newHomeTarget.Subdirectory,
			InheritToChildren: newHomeTarget.InheritToChildren,
			DocTypeSlugsJSON:  datatypes.JSON(slugsJSON),
			SortOrder:         newHomeTarget.SortOrder,
			CreatedBy:         user.ID,
		}
		updatedDoc, err = s.refDocRepo.UpdateDocAndTransferHomeWithVersion(ctx, cur.ID, req.ExpectedVersion, updates, newHomeModel)
	} else {
		updatedDoc, err = s.refDocRepo.UpdateDocWithVersion(ctx, cur.ID, req.ExpectedVersion, updates)
	}
	if err != nil {
		if writtenFilePath != "" {
			_ = os.Remove(writtenFilePath)
		}
		if errors.Is(err, models.ErrVersionConflict) {
			latest, gErr := s.refDocRepo.GetByID(ctx, cur.ID)
			if gErr == nil {
				return nil, nil, nil, &RefDocVersionConflictError{CurrentVersion: latest.CurrentVersion}
			}
			return nil, nil, nil, &RefDocVersionConflictError{CurrentVersion: cur.CurrentVersion}
		}
		return nil, nil, nil, err
	}

	if effectiveContent == "" {
		effectiveContent, _ = s.ReadVersionContent(updatedDoc.ID, updatedDoc.CurrentVersion)
	}
	detail, err := s.buildDetailVO(ctx, updatedDoc, updatedDoc.CurrentVersion, effectiveContent, user)
	if err != nil {
		return nil, nil, nil, err
	}
	afterAudit := &RefDocAuditSnapshot{
		DocID:          updatedDoc.ID,
		Title:          updatedDoc.Title,
		DocKind:        updatedDoc.DocKind,
		OwnerScopeKey:  updatedDoc.OwnerScopeKey,
		CurrentVersion: updatedDoc.CurrentVersion,
		ContentSHA256:  updatedDoc.ContentSHA256,
		Status:         updatedDoc.Status,
		Redacted:       redactedFlag,
	}
	return detail, beforeAudit, afterAudit, nil
}

// ListRefDocs 分页检索参考文档库。
func (s *RefDocService) ListRefDocs(ctx context.Context, query RefDocListQuery, user *commonModels.User) (*ReferenceDocListResponse, error) {
	page, pageSize := NormalizePagination(query.Page, query.PageSize)
	status := strings.TrimSpace(query.Status)
	if status == "" {
		status = models.RefDocStatusActive
	}
	docs, total, err := s.refDocRepo.ListDocs(ctx, models.RefDocListFilter{
		Search:        strings.TrimSpace(query.Search),
		DocKind:       strings.TrimSpace(query.DocKind),
		ScopeLevel:    strings.TrimSpace(query.ScopeLevel),
		ArchElementID: query.ArchElementID,
		Status:        status,
		Offset:        (page - 1) * pageSize,
		Limit:         pageSize,
	})
	if err != nil {
		return nil, err
	}

	docIDs := make([]uint, 0, len(docs))
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
	}
	bindingsMap, err := s.refDocRepo.ListBindingsByDocIDs(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	labels, err := s.buildScopeLabelResolver(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]ReferenceDocSummaryVO, 0, len(docs))
	for i := range docs {
		d := &docs[i]
		canEdit, _ := s.checkDocOwnerWritePermission(ctx, user, d)
		items = append(items, ReferenceDocSummaryVO{
			ID:              d.ID,
			Title:           d.Title,
			DocKind:         d.DocKind,
			Description:     d.Description,
			OwnerScopeKey:   d.OwnerScopeKey,
			OwnerScopeLevel: d.OwnerScopeLevel,
			OwnerScopeLabel: labels.resolve(d.OwnerScopeLevel, d.OwnerArchElementID, d.OwnerRepoID, d.OwnerSubdirectory),
			CurrentVersion:  d.CurrentVersion,
			EstimatedTokens: d.EstimatedTokens,
			Status:          d.Status,
			BindingCount:    len(bindingsMap[d.ID]),
			CanEdit:         canEdit,
			UpdatedAt:       d.UpdatedAt,
		})
	}

	return &ReferenceDocListResponse{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetRefDocDetail 获取单篇参考文档详情、绑定列表及指定版本 Markdown 正文。
func (s *RefDocService) GetRefDocDetail(ctx context.Context, docID uint, version int, user *commonModels.User) (*ReferenceDocDetailVO, error) {
	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	ver := version
	if ver <= 0 {
		ver = doc.CurrentVersion
	}
	if ver > doc.CurrentVersion {
		return nil, models.ErrNotFound
	}
	content, err := s.ReadVersionContent(doc.ID, ver)
	if err != nil {
		return nil, err
	}
	return s.buildDetailVO(ctx, doc, ver, content, user)
}

// CreateBinding 为活跃参考文档建立跨节点消费绑定（is_home=false）。
func (s *RefDocService) CreateBinding(ctx context.Context, docID uint, in RefDocBindingInput, user *commonModels.User) (*RefDocBindingVO, *RefDocBindingAuditSnapshot, error) {
	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, nil, err
	}
	if doc.Status != models.RefDocStatusActive {
		return nil, nil, ErrRefDocArchivedReadOnly
	}
	target, err := s.resolveAndAuthorizeBindingTarget(ctx, user, in)
	if err != nil {
		return nil, nil, err
	}

	slugsJSON, _ := json.Marshal(target.DocTypeSlugs)
	binding := &models.ReferenceDocBinding{
		DocID:             doc.ID,
		ScopeKey:          target.ScopeKey,
		ScopeLevel:        target.ScopeLevel,
		IsHome:            false,
		ArchElementID:     target.ArchElementID,
		RepoID:            target.RepoID,
		Subdirectory:      target.Subdirectory,
		InheritToChildren: target.InheritToChildren,
		DocTypeSlugsJSON:  datatypes.JSON(slugsJSON),
		SortOrder:         target.SortOrder,
		CreatedBy:         user.ID,
	}
	if err := s.refDocRepo.CreateBinding(ctx, binding); err != nil {
		return nil, nil, err
	}

	labels, _ := s.buildScopeLabelResolver(ctx)
	vo := &RefDocBindingVO{
		ID:                binding.ID,
		DocID:             binding.DocID,
		ScopeKey:          binding.ScopeKey,
		ScopeLevel:        binding.ScopeLevel,
		ScopeLabel:        labels.resolve(binding.ScopeLevel, binding.ArchElementID, binding.RepoID, binding.Subdirectory),
		IsHome:            binding.IsHome,
		ArchElementID:     binding.ArchElementID,
		RepoID:            binding.RepoID,
		Subdirectory:      binding.Subdirectory,
		InheritToChildren: binding.InheritToChildren,
		DocTypeSlugs:      target.DocTypeSlugs,
		SortOrder:         binding.SortOrder,
		CanManageBinding:  true,
	}
	auditSnap := &RefDocBindingAuditSnapshot{
		DocID:             binding.DocID,
		BindingID:         binding.ID,
		ScopeKey:          binding.ScopeKey,
		ScopeLevel:        binding.ScopeLevel,
		IsHome:            binding.IsHome,
		InheritToChildren: binding.InheritToChildren,
		DocTypeSlugs:      target.DocTypeSlugs,
	}
	return vo, auditSnap, nil
}

// UpdateBinding 更新单条消费绑定的 inherit_to_children / doc_type_slugs / sort_order。
func (s *RefDocService) UpdateBinding(
	ctx context.Context,
	docID, bindingID uint,
	req UpdateRefDocBindingRequest,
	user *commonModels.User,
) (*RefDocBindingVO, *RefDocBindingAuditSnapshot, *RefDocBindingAuditSnapshot, error) {
	if _, err := s.refDocRepo.GetByID(ctx, docID); err != nil {
		return nil, nil, nil, err
	}
	existing, err := s.refDocRepo.GetBindingByID(ctx, docID, bindingID)
	if err != nil {
		return nil, nil, nil, err
	}
	allowed, err := s.checkBindingWritePermission(ctx, user, *existing)
	if err != nil {
		return nil, nil, nil, err
	}
	if !allowed {
		return nil, nil, nil, ErrRefDocForbidden
	}

	var prevSlugs []string
	_ = json.Unmarshal(existing.DocTypeSlugsJSON, &prevSlugs)
	beforeAudit := &RefDocBindingAuditSnapshot{
		DocID:             existing.DocID,
		BindingID:         existing.ID,
		ScopeKey:          existing.ScopeKey,
		ScopeLevel:        existing.ScopeLevel,
		IsHome:            existing.IsHome,
		InheritToChildren: existing.InheritToChildren,
		DocTypeSlugs:      prevSlugs,
	}

	updates := map[string]any{}
	nextInherit := existing.InheritToChildren
	if req.InheritToChildren != nil {
		if existing.ScopeLevel == models.RefDocScopeSystem && !*req.InheritToChildren {
			return nil, nil, nil, fmt.Errorf("%w: 全系统级参考文档强制向下继承，inherit_to_children 不可为 false", ErrInvalidRefDocParam)
		}
		nextInherit = *req.InheritToChildren
		updates["inherit_to_children"] = nextInherit
	}
	nextSlugs := prevSlugs
	if req.DocTypeSlugs != nil {
		nextSlugs = dedupeTrimmedStrings(*req.DocTypeSlugs)
		b, _ := json.Marshal(nextSlugs)
		updates["doc_type_slugs_json"] = datatypes.JSON(b)
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		updates["sort_order"] = existing.SortOrder
	}

	updated, err := s.refDocRepo.UpdateBinding(ctx, docID, bindingID, updates)
	if err != nil {
		return nil, nil, nil, err
	}

	labels, _ := s.buildScopeLabelResolver(ctx)
	vo := &RefDocBindingVO{
		ID:                updated.ID,
		DocID:             updated.DocID,
		ScopeKey:          updated.ScopeKey,
		ScopeLevel:        updated.ScopeLevel,
		ScopeLabel:        labels.resolve(updated.ScopeLevel, updated.ArchElementID, updated.RepoID, updated.Subdirectory),
		IsHome:            updated.IsHome,
		ArchElementID:     updated.ArchElementID,
		RepoID:            updated.RepoID,
		Subdirectory:      updated.Subdirectory,
		InheritToChildren: updated.InheritToChildren,
		DocTypeSlugs:      nextSlugs,
		SortOrder:         updated.SortOrder,
		CanManageBinding:  true,
	}
	afterAudit := &RefDocBindingAuditSnapshot{
		DocID:             updated.DocID,
		BindingID:         updated.ID,
		ScopeKey:          updated.ScopeKey,
		ScopeLevel:        updated.ScopeLevel,
		IsHome:            updated.IsHome,
		InheritToChildren: updated.InheritToChildren,
		DocTypeSlugs:      nextSlugs,
	}
	return vo, beforeAudit, afterAudit, nil
}

// DeleteBinding 解除指定节点的消费绑定（active 下 is_home=true 受保护）。
func (s *RefDocService) DeleteBinding(ctx context.Context, docID, bindingID uint, user *commonModels.User) (*RefDocBindingAuditSnapshot, error) {
	if _, err := s.refDocRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}
	existing, err := s.refDocRepo.GetBindingByID(ctx, docID, bindingID)
	if err != nil {
		return nil, err
	}
	allowed, err := s.checkBindingWritePermission(ctx, user, *existing)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrRefDocForbidden
	}

	var slugs []string
	_ = json.Unmarshal(existing.DocTypeSlugsJSON, &slugs)
	beforeAudit := &RefDocBindingAuditSnapshot{
		DocID:             existing.DocID,
		BindingID:         existing.ID,
		ScopeKey:          existing.ScopeKey,
		ScopeLevel:        existing.ScopeLevel,
		IsHome:            existing.IsHome,
		InheritToChildren: existing.InheritToChildren,
		DocTypeSlugs:      slugs,
	}

	if err := s.refDocRepo.DeleteBinding(ctx, docID, bindingID); err != nil {
		return nil, err
	}
	return beforeAudit, nil
}

// PurgeRefDoc 管理员物理彻底清除参考文档全部磁盘版本文件与数据库记录。
func (s *RefDocService) PurgeRefDoc(ctx context.Context, docID uint) (*RefDocAuditSnapshot, error) {
	mu := s.lockDoc(docID)
	mu.Lock()
	defer mu.Unlock()

	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	auditSnap := &RefDocAuditSnapshot{
		DocID:          doc.ID,
		Title:          doc.Title,
		DocKind:        doc.DocKind,
		OwnerScopeKey:  doc.OwnerScopeKey,
		CurrentVersion: doc.CurrentVersion,
		ContentSHA256:  doc.ContentSHA256,
		Status:         doc.Status,
	}

	if err := s.refDocRepo.PurgeDoc(ctx, docID); err != nil {
		return nil, err
	}

	if docDir, joinErr := sandbox.SafeJoin(s.refDocsDir, fmt.Sprintf("%d", docID)); joinErr == nil {
		_ = os.RemoveAll(docDir)
	}
	return auditSnap, nil
}

// ResolveEffectiveRefDocs 严格按 ADR-10 §3.1.4 五步算法解析目标作用域最终生效的参考文档列表。
func (s *RefDocService) ResolveEffectiveRefDocs(
	ctx context.Context,
	req ResolveEffectiveRefDocsRequest,
	user *commonModels.User,
) (*EffectiveRefDocsResponse, error) {
	hasArchID := req.ArchElementID != nil && *req.ArchElementID > 0
	hasRepoID := req.RepoID != nil && *req.RepoID > 0
	if !hasArchID && !hasRepoID {
		return nil, fmt.Errorf("%w: 必须提供 arch_element_id 或 repo_id", ErrInvalidRefDocParam)
	}

	normSubdir, err := sandbox.NormalizeSubdir(req.Subdirectory)
	if err != nil {
		return nil, err
	}

	allElems, err := s.masterRepo.ListAllArchElements(ctx)
	if err != nil {
		return nil, err
	}
	elemByID := make(map[uint]models.ArchitectureElement, len(allElems))
	for _, el := range allElems {
		elemByID[el.ID] = el
	}

	directArchIDs := make(map[uint]bool)
	ancestorArchIDs := make(map[uint]bool)
	var targetRepoID uint
	targetSubdir := normSubdir
	var targetArchElem *models.ArchitectureElement

	if hasArchID {
		el, ok := elemByID[*req.ArchElementID]
		if !ok {
			return nil, models.ErrNotFound
		}
		targetArchElem = &el
		directArchIDs[el.ID] = true
		collectArchAncestors(el.ID, elemByID, ancestorArchIDs)
		if el.Type == models.RefDocScopeModule && el.RepoID != nil && *el.RepoID > 0 {
			targetRepoID = *el.RepoID
			if normSub, nErr := sandbox.NormalizeSubdir(el.Subdirectory); nErr == nil {
				targetSubdir = normSub
			}
		}
	} else {
		targetRepoID = *req.RepoID
		// 查找绑定到此 (repo_id, normSubdir) 的所有 module 架构节点（按 id ASC 兼容）
		for _, el := range allElems {
			if el.Type != models.RefDocScopeModule || el.RepoID == nil || *el.RepoID != targetRepoID {
				continue
			}
			elSub, nErr := sandbox.NormalizeSubdir(el.Subdirectory)
			if nErr != nil || elSub != normSubdir {
				continue
			}
			directArchIDs[el.ID] = true
			collectArchAncestors(el.ID, elemByID, ancestorArchIDs)
		}
	}

	// 检测 Monorepo 整仓 Roll-up 汇总模式：targetSubdir == "" 且非单模块绑定且仓内存在 >= 2 个活跃子模块
	isWholeRepoRollup := false
	if targetRepoID > 0 && targetSubdir == "" && !hasArchID && len(directArchIDs) == 0 {
		subModCount := 0
		for _, el := range allElems {
			if el.Type == models.RefDocScopeModule && el.RepoID != nil && *el.RepoID == targetRepoID {
				if sub, nErr := sandbox.NormalizeSubdir(el.Subdirectory); nErr == nil && sub != "" {
					subModCount++
					collectArchAncestors(el.ID, elemByID, ancestorArchIDs)
				}
			}
		}
		if subModCount >= 2 {
			isWholeRepoRollup = true
		}
	}

	docs, bindings, err := s.refDocRepo.ListAllActiveDocsAndBindings(ctx)
	if err != nil {
		return nil, err
	}
	docByID := make(map[uint]models.ReferenceDoc, len(docs))
	for _, d := range docs {
		docByID[d.ID] = d
	}

	excludeSet := make(map[uint]bool, len(req.ExcludeDocIDs))
	for _, id := range req.ExcludeDocIDs {
		if id > 0 {
			excludeSet[id] = true
		}
	}

	type candidateMatch struct {
		binding     models.ReferenceDocBinding
		rank        int
		isInherited bool
	}
	bestByDoc := make(map[uint]candidateMatch)

	consider := func(b models.ReferenceDocBinding, isInherited bool) {
		doc, ok := docByID[b.DocID]
		if !ok || doc.Status != models.RefDocStatusActive || excludeSet[b.DocID] {
			return
		}
		rank := models.RefDocScopeRank(b.ScopeLevel)
		if rank == 0 {
			return
		}
		prev, exists := bestByDoc[b.DocID]
		if !exists || rank > prev.rank || (rank == prev.rank && b.ID < prev.binding.ID) {
			bestByDoc[b.DocID] = candidateMatch{
				binding:     b,
				rank:        rank,
				isInherited: isInherited,
			}
		}
	}

	for _, b := range bindings {
		switch b.ScopeLevel {
		case models.RefDocScopeSystem:
			consider(b, true)
		case models.RefDocScopeSubsystem, models.RefDocScopeGroup, models.RefDocScopeModule:
			if b.ArchElementID == nil {
				continue
			}
			aid := *b.ArchElementID
			if directArchIDs[aid] {
				consider(b, false)
			} else if ancestorArchIDs[aid] && b.InheritToChildren {
				if isWholeRepoRollup && b.ScopeLevel == models.RefDocScopeModule {
					continue
				}
				consider(b, true)
			}
		case models.RefDocScopeRepo:
			if targetRepoID == 0 || b.RepoID == nil || *b.RepoID != targetRepoID {
				continue
			}
			bSub, nErr := sandbox.NormalizeSubdir(b.Subdirectory)
			if nErr != nil {
				continue
			}
			if bSub == targetSubdir {
				// 当请求是直接针对 module 节点时，来自 repo 的绑定视为继承（或补充）
				consider(b, hasArchID)
			} else if targetSubdir != "" && bSub == "" && b.InheritToChildren {
				consider(b, true)
			}
		}
	}

	targetDocType := strings.TrimSpace(req.DocTypeSlug)
	type resolvedPair struct {
		doc         models.ReferenceDoc
		binding     models.ReferenceDocBinding
		rank        int
		isInherited bool
		slugs       []string
	}
	var pairs []resolvedPair
	for docID, m := range bestByDoc {
		var slugs []string
		_ = json.Unmarshal(m.binding.DocTypeSlugsJSON, &slugs)
		if targetDocType != "" && len(slugs) > 0 && !containsString(slugs, targetDocType) {
			continue
		}
		pairs = append(pairs, resolvedPair{
			doc:         docByID[docID],
			binding:     m.binding,
			rank:        m.rank,
			isInherited: m.isInherited,
			slugs:       slugs,
		})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].rank != pairs[j].rank {
			return pairs[i].rank < pairs[j].rank
		}
		if pairs[i].binding.SortOrder != pairs[j].binding.SortOrder {
			return pairs[i].binding.SortOrder < pairs[j].binding.SortOrder
		}
		return pairs[i].doc.ID < pairs[j].doc.ID
	})

	if isWholeRepoRollup && len(pairs) > maxWholeRepoRollupRefDocs {
		pairs = pairs[:maxWholeRepoRollupRefDocs]
	}

	labels, err := s.buildScopeLabelResolver(ctx)
	if err != nil {
		return nil, err
	}

	targetLabel := "全系统"
	if targetArchElem != nil {
		targetLabel = labels.resolve(targetArchElem.Type, &targetArchElem.ID, targetArchElem.RepoID, targetArchElem.Subdirectory)
	} else if targetRepoID > 0 {
		targetLabel = labels.resolve(models.RefDocScopeRepo, nil, &targetRepoID, targetSubdir)
	}

	items := make([]EffectiveRefDocItemVO, 0, len(pairs))
	snapshots := make([]models.TaskReferenceDocSnapshot, 0, len(pairs))
	totalTokens := 0

	for i := range pairs {
		p := &pairs[i]
		var toc []models.HeadingNode
		_ = json.Unmarshal(p.doc.TOCJSON, &toc)
		if toc == nil {
			toc = []models.HeadingNode{}
		}
		var terms []string
		_ = json.Unmarshal(p.doc.DeclaredTerms, &terms)

		srcLabel := labels.resolve(p.binding.ScopeLevel, p.binding.ArchElementID, p.binding.RepoID, p.binding.Subdirectory)
		canEdit, _ := s.checkDocOwnerWritePermission(ctx, user, &p.doc)
		canUnbind := false
		if !p.isInherited && !(p.doc.Status == models.RefDocStatusActive && p.binding.IsHome) {
			canUnbind, _ = s.checkBindingWritePermission(ctx, user, p.binding)
		}

		items = append(items, EffectiveRefDocItemVO{
			DocID:               p.doc.ID,
			BindingID:           p.binding.ID,
			Title:               p.doc.Title,
			DocKind:             p.doc.DocKind,
			CurrentVersion:      p.doc.CurrentVersion,
			SummaryMD:           p.doc.SummaryMD,
			SummaryStale:        p.doc.SummaryForVersion < p.doc.CurrentVersion,
			TOC:                 toc,
			EstimatedTokens:     p.doc.EstimatedTokens,
			IsHome:              p.binding.IsHome,
			IsInherited:         p.isInherited,
			EffectiveScopeLevel: p.binding.ScopeLevel,
			SourceScopeLabel:    srcLabel,
			InheritToChildren:   p.binding.InheritToChildren,
			DocTypeSlugs:        nonNilStrings(p.slugs),
			CanEdit:             canEdit,
			CanUnbind:           canUnbind,
		})
		totalTokens += p.doc.EstimatedTokens

		sumHash := sha256Hex(p.doc.SummaryMD)
		snapshots = append(snapshots, models.TaskReferenceDocSnapshot{
			DocID:            p.doc.ID,
			Title:            p.doc.Title,
			DocKind:          p.doc.DocKind,
			VersionNo:        p.doc.CurrentVersion,
			ContentSHA256:    p.doc.ContentSHA256,
			SummaryMD:        p.doc.SummaryMD,
			SummarySHA256:    sumHash,
			TOC:              toc,
			DeclaredTerms:    terms,
			ScopeLevel:       p.binding.ScopeLevel,
			ScopeKey:         p.binding.ScopeKey,
			SourceScopeLabel: srcLabel,
			EstimatedTokens:  p.doc.EstimatedTokens,
			IsInherited:      p.isInherited,
		})
	}

	return &EffectiveRefDocsResponse{
		TargetScopeLabel:     targetLabel,
		TotalCount:           len(items),
		TotalEstimatedTokens: totalTokens,
		IsWholeRepoRollup:    isWholeRepoRollup,
		Items:                items,
		Snapshots:            snapshots,
	}, nil
}

// LoadPromptDocsFromSnapshots 从任务/会话快照精确加载锁定版本 v<ver>.md 正文构造 refdoc.PromptDoc 列表。
func (s *RefDocService) LoadPromptDocsFromSnapshots(snapshots []models.TaskReferenceDocSnapshot) []refdoc.PromptDoc {
	if s == nil || len(snapshots) == 0 {
		return nil
	}
	out := make([]refdoc.PromptDoc, 0, len(snapshots))
	for _, snap := range snapshots {
		content, err := s.ReadVersionContent(snap.DocID, snap.VersionNo)
		if err != nil {
			continue
		}
		out = append(out, refdoc.PromptDoc{
			DocID:            snap.DocID,
			Title:            snap.Title,
			DocKind:          snap.DocKind,
			VersionNo:        snap.VersionNo,
			ScopeLevel:       snap.ScopeLevel,
			ScopeRank:        models.RefDocScopeRank(snap.ScopeLevel),
			SourceScopeLabel: snap.SourceScopeLabel,
			IsInherited:      snap.IsInherited,
			SummaryMD:        snap.SummaryMD,
			TOC:              toRefDocHeadingNodes(snap.TOC),
			DeclaredTerms:    snap.DeclaredTerms,
			EstimatedTokens:  snap.EstimatedTokens,
			Markdown:         content,
		})
	}
	return out
}

func collectArchAncestors(startID uint, elemByID map[uint]models.ArchitectureElement, out map[uint]bool) {
	visited := map[uint]bool{startID: true}
	cur, ok := elemByID[startID]
	if !ok {
		return
	}
	for depth := 0; depth < maxArchAncestorDepth; depth++ {
		if cur.ParentID == nil || *cur.ParentID == 0 {
			break
		}
		pid := *cur.ParentID
		if visited[pid] {
			break
		}
		visited[pid] = true
		parent, exists := elemByID[pid]
		if !exists {
			break
		}
		out[pid] = true
		cur = parent
	}
}

func (s *RefDocService) buildDetailVO(
	ctx context.Context,
	doc *models.ReferenceDoc,
	viewingVersion int,
	contentMD string,
	user *commonModels.User,
) (*ReferenceDocDetailVO, error) {
	bindings, err := s.refDocRepo.ListBindingsByDocID(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	labels, err := s.buildScopeLabelResolver(ctx)
	if err != nil {
		return nil, err
	}

	var toc []models.HeadingNode
	_ = json.Unmarshal(doc.TOCJSON, &toc)
	if toc == nil {
		toc = []models.HeadingNode{}
	}
	var terms []string
	_ = json.Unmarshal(doc.DeclaredTerms, &terms)
	if terms == nil {
		terms = []string{}
	}

	canEdit, _ := s.checkDocOwnerWritePermission(ctx, user, doc)
	bindingVOs := make([]RefDocBindingVO, 0, len(bindings))
	for _, b := range bindings {
		var slugs []string
		_ = json.Unmarshal(b.DocTypeSlugsJSON, &slugs)
		canManage, _ := s.checkBindingWritePermission(ctx, user, b)
		bindingVOs = append(bindingVOs, RefDocBindingVO{
			ID:                b.ID,
			DocID:             b.DocID,
			ScopeKey:          b.ScopeKey,
			ScopeLevel:        b.ScopeLevel,
			ScopeLabel:        labels.resolve(b.ScopeLevel, b.ArchElementID, b.RepoID, b.Subdirectory),
			IsHome:            b.IsHome,
			ArchElementID:     b.ArchElementID,
			RepoID:            b.RepoID,
			Subdirectory:      b.Subdirectory,
			InheritToChildren: b.InheritToChildren,
			DocTypeSlugs:      nonNilStrings(slugs),
			SortOrder:         b.SortOrder,
			CanManageBinding:  canManage,
		})
	}

	return &ReferenceDocDetailVO{
		ID:                doc.ID,
		Title:             doc.Title,
		DocKind:           doc.DocKind,
		Description:       doc.Description,
		Visibility:        doc.Visibility,
		OwnerScopeKey:     doc.OwnerScopeKey,
		OwnerScopeLevel:   doc.OwnerScopeLevel,
		OwnerScopeLabel:   labels.resolve(doc.OwnerScopeLevel, doc.OwnerArchElementID, doc.OwnerRepoID, doc.OwnerSubdirectory),
		SummaryMD:         doc.SummaryMD,
		SummarySource:     doc.SummarySource,
		SummaryStatus:     doc.SummaryStatus,
		SummaryForVersion: doc.SummaryForVersion,
		SummaryStale:      doc.SummaryForVersion < doc.CurrentVersion,
		CurrentVersion:    doc.CurrentVersion,
		ViewingVersion:    viewingVersion,
		ContentSHA256:     doc.ContentSHA256,
		ByteSize:          doc.ByteSize,
		RuneCount:         doc.RuneCount,
		EstimatedTokens:   doc.EstimatedTokens,
		LineCount:         doc.LineCount,
		Status:            doc.Status,
		TOC:               toc,
		DeclaredTerms:     terms,
		ContentMD:         contentMD,
		Bindings:          bindingVOs,
		CanEdit:           canEdit,
		CreatedBy:         doc.CreatedBy,
		UpdatedBy:         doc.UpdatedBy,
		CreatedAt:         doc.CreatedAt,
		UpdatedAt:         doc.UpdatedAt,
	}, nil
}

type scopeLabelResolver struct {
	elemByID map[uint]models.ArchitectureElement
	repoByID map[uint]commonModels.Repository
}

func (s *RefDocService) buildScopeLabelResolver(ctx context.Context) (*scopeLabelResolver, error) {
	allElems, err := s.masterRepo.ListAllArchElements(ctx)
	if err != nil {
		return nil, err
	}
	elemByID := make(map[uint]models.ArchitectureElement, len(allElems))
	repoIDSet := make(map[uint]struct{})
	for _, el := range allElems {
		elemByID[el.ID] = el
		if el.RepoID != nil && *el.RepoID > 0 {
			repoIDSet[*el.RepoID] = struct{}{}
		}
	}
	repos, err := s.masterRepo.ListAllActiveRepositories(ctx, nil)
	if err != nil {
		return nil, err
	}
	repoByID := make(map[uint]commonModels.Repository, len(repos))
	for _, r := range repos {
		repoByID[r.ID] = r
		delete(repoIDSet, r.ID)
	}
	if len(repoIDSet) > 0 {
		extraIDs := make([]uint, 0, len(repoIDSet))
		for id := range repoIDSet {
			extraIDs = append(extraIDs, id)
		}
		if extraMap, eErr := s.masterRepo.GetRepositoriesByIDs(ctx, extraIDs); eErr == nil {
			for id, r := range extraMap {
				repoByID[id] = r
			}
		}
	}
	return &scopeLabelResolver{
		elemByID: elemByID,
		repoByID: repoByID,
	}, nil
}

func (r *scopeLabelResolver) resolve(level string, archID, repoID *uint, subdir string) string {
	switch level {
	case models.RefDocScopeSystem:
		return "全系统"
	case models.RefDocScopeSubsystem, models.RefDocScopeGroup, models.RefDocScopeModule:
		if archID == nil {
			return level
		}
		el, ok := r.elemByID[*archID]
		if !ok {
			return fmt.Sprintf("架构节点 #%d", *archID)
		}
		name := archElementDisplayName(el)
		if level == models.RefDocScopeModule && el.RepoID != nil && *el.RepoID > 0 {
			if repo, rOK := r.repoByID[*el.RepoID]; rOK {
				if strings.TrimSpace(el.Subdirectory) != "" {
					return fmt.Sprintf("%s (%s / %s)", name, repo.Name, el.Subdirectory)
				}
				return fmt.Sprintf("%s (%s)", name, repo.Name)
			}
		}
		return name
	case models.RefDocScopeRepo:
		if repoID == nil {
			return "代码仓"
		}
		repoName := fmt.Sprintf("仓库 #%d", *repoID)
		if repo, ok := r.repoByID[*repoID]; ok {
			repoName = repo.Name
		}
		if strings.TrimSpace(subdir) != "" {
			return fmt.Sprintf("%s / %s", repoName, subdir)
		}
		return repoName
	default:
		return level
	}
}

func toRefDocHeadingNodes(in []models.HeadingNode) []refdoc.HeadingNode {
	if len(in) == 0 {
		return nil
	}
	out := make([]refdoc.HeadingNode, len(in))
	for i, n := range in {
		out[i] = refdoc.HeadingNode{
			SectionID: n.SectionID,
			Title:     n.Title,
			Level:     n.Level,
			StartLine: n.StartLine,
			EndLine:   n.EndLine,
			RuneCount: n.RuneCount,
			IsChunk:   n.IsChunk,
		}
	}
	return out
}

func dedupeTrimmedStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		t := strings.TrimSpace(s)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func unmarshalTaskRefDocSnapshots(raw datatypes.JSON) []models.TaskReferenceDocSnapshot {
	if len(raw) <= 2 {
		return nil
	}
	var out []models.TaskReferenceDocSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func snapshotsToVerifierSpecs(snaps []models.TaskReferenceDocSnapshot) []verifier.RefDocSpec {
	if len(snaps) == 0 {
		return nil
	}
	out := make([]verifier.RefDocSpec, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, verifier.RefDocSpec{
			DocID:         sn.DocID,
			Title:         sn.Title,
			VersionNo:     sn.VersionNo,
			ScopeLevel:    sn.ScopeLevel,
			TOC:           toRefDocHeadingNodes(sn.TOC),
			DeclaredTerms: sn.DeclaredTerms,
		})
	}
	return out
}

func promptDocsToVerifierSpecs(docs []refdoc.PromptDoc) []verifier.RefDocSpec {
	if len(docs) == 0 {
		return nil
	}
	out := make([]verifier.RefDocSpec, 0, len(docs))
	for _, d := range docs {
		out = append(out, verifier.RefDocSpec{
			DocID:         d.DocID,
			Title:         d.Title,
			VersionNo:     d.VersionNo,
			ScopeLevel:    d.ScopeLevel,
			TOC:           d.TOC,
			DeclaredTerms: d.DeclaredTerms,
		})
	}
	return out
}

func verifierRefCitationsToModels(in []verifier.RefDocCitation) []models.RefDocCitation {
	out := make([]models.RefDocCitation, 0, len(in))
	for _, c := range in {
		out = append(out, models.RefDocCitation{
			CitationKey:  c.CitationKey,
			DocID:        c.DocID,
			VersionNo:    c.VersionNo,
			SectionID:    c.SectionID,
			SectionTitle: c.SectionTitle,
			DocTitle:     c.DocTitle,
			ScopeLevel:   c.ScopeLevel,
			Stale:        c.Stale,
		})
	}
	return out
}

func modelsRefCitationsToCandidates(in []models.RefDocCitation) []verifier.CitedAnchorCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]verifier.CitedAnchorCandidate, 0, len(in))
	for _, c := range in {
		out = append(out, verifier.CitedAnchorCandidate{
			Scope:      "ref_doc",
			SymbolKind: "ref_doc",
			FilePath:   fmt.Sprintf("ref_doc:%d", c.DocID),
			FQSymbol:   c.SectionID,
		})
	}
	return out
}

func collectPriorRefCitationsBySlot(priorEvRows []models.SectionEvidence) (map[string][]models.RefDocCitation, map[string]datatypes.JSON) {
	bySlot := make(map[string][]models.RefDocCitation, len(priorEvRows))
	rawBySlot := make(map[string]datatypes.JSON, len(priorEvRows))
	for _, pe := range priorEvRows {
		if len(pe.RefCitationsJSON) <= 2 {
			continue
		}
		var list []models.RefDocCitation
		if json.Unmarshal(pe.RefCitationsJSON, &list) == nil && len(list) > 0 {
			bySlot[pe.SlotID] = list
			rawBySlot[pe.SlotID] = pe.RefCitationsJSON
		}
	}
	return bySlot, rawBySlot
}

func buildHistoricalRefDocSpecs(
	ctx context.Context,
	refDocSvc *RefDocService,
	priorEvRows []models.SectionEvidence,
	activeSpecs []verifier.RefDocSpec,
) []verifier.RefDocSpec {
	if len(priorEvRows) == 0 {
		return nil
	}
	activeIDs := make(map[uint]bool, len(activeSpecs))
	for _, a := range activeSpecs {
		activeIDs[a.DocID] = true
	}
	byDocID := make(map[uint]*verifier.RefDocSpec)
	seenSec := make(map[uint]map[string]bool)
	for _, pe := range priorEvRows {
		if len(pe.RefCitationsJSON) <= 2 {
			continue
		}
		var list []models.RefDocCitation
		if json.Unmarshal(pe.RefCitationsJSON, &list) != nil {
			continue
		}
		for _, c := range list {
			if c.DocID == 0 || activeIDs[c.DocID] {
				continue
			}
			spec, exists := byDocID[c.DocID]
			if !exists {
				spec = &verifier.RefDocSpec{
					DocID:      c.DocID,
					Title:      c.DocTitle,
					VersionNo:  c.VersionNo,
					ScopeLevel: c.ScopeLevel,
				}
				if refDocSvc != nil && refDocSvc.refDocRepo != nil {
					if d, err := refDocSvc.refDocRepo.GetByID(ctx, c.DocID); err == nil && d != nil {
						if spec.Title == "" {
							spec.Title = d.Title
						}
						if spec.VersionNo <= 0 {
							spec.VersionNo = d.CurrentVersion
						}
						if spec.ScopeLevel == "" {
							spec.ScopeLevel = d.OwnerScopeLevel
						}
						var toc []models.HeadingNode
						if json.Unmarshal(d.TOCJSON, &toc) == nil && len(toc) > 0 {
							spec.TOC = toRefDocHeadingNodes(toc)
						}
					}
				}
				byDocID[c.DocID] = spec
				seenSec[c.DocID] = make(map[string]bool)
				for _, node := range spec.TOC {
					seenSec[c.DocID][node.SectionID] = true
				}
			}
			if c.SectionID != "" && !seenSec[c.DocID][c.SectionID] {
				seenSec[c.DocID][c.SectionID] = true
				spec.TOC = append(spec.TOC, refdoc.HeadingNode{
					SectionID: c.SectionID,
					Title:     c.SectionTitle,
				})
			}
		}
	}
	if len(byDocID) == 0 {
		return nil
	}
	out := make([]verifier.RefDocSpec, 0, len(byDocID))
	for _, spec := range byDocID {
		out = append(out, *spec)
	}
	return out
}
