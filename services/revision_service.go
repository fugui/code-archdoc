package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/drift"
	"code-archdoc/pkg/verifier"

	"gorm.io/datatypes"
)

var (
	// ErrDraftAccessDenied 普通用户尝试访问未发布草稿或修订历史（HTTP 403 DRAFT_ACCESS_DENIED）。
	ErrDraftAccessDenied = errors.New("无权访问未发布草稿或文档修订历史")
	// ErrInvalidRevisionParam 修订相关请求参数非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidRevisionParam = errors.New("修订请求参数非法")
	// ErrSectionNotOrphan 目标章节并非孤立章节（HTTP 400 INVALID_PARAM）。
	ErrSectionNotOrphan = errors.New("目标章节并非孤立章节，无需执行孤立处置")
)

// OptimisticLockConflictError 携带服务端当前版本号的乐观锁冲突错误，可被 errors.Is(..., models.ErrOptimisticLockConflict) 匹配。
type OptimisticLockConflictError struct {
	CurrentRevisionNo int
}

func (e *OptimisticLockConflictError) Error() string {
	return fmt.Sprintf("文档修订版本已过期（服务端当前版本为 v%d），请刷新后重试", e.CurrentRevisionNo)
}

func (e *OptimisticLockConflictError) Unwrap() error {
	return models.ErrOptimisticLockConflict
}

// ─── 视图对象 (VO) ───

type DocumentNavItemVO struct {
	ID                  uint      `json:"id"`
	RepoID              uint      `json:"repo_id"`
	Subdirectory        string    `json:"subdirectory"`
	RefName             string    `json:"ref_name"`
	DocTypeSlug         string    `json:"doc_type_slug"`
	DocTypeName         string    `json:"doc_type_name"`
	Title               string    `json:"title"`
	Status              string    `json:"status"`
	CurrentRevisionNo   int       `json:"current_revision_no"`
	PublishedRevisionNo int       `json:"published_revision_no"`
	VerifierPassed      bool      `json:"verifier_passed"`
	CanApprove          bool      `json:"can_approve"`
	HasCodeDrift        bool      `json:"has_code_drift"`
	HasSpecDrift        bool      `json:"has_spec_drift"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type DocumentListVO struct {
	CanWrite bool                `json:"can_write"`
	Items    []DocumentNavItemVO `json:"items"`
}

type SectionVO struct {
	SlotID           string                  `json:"slot_id"`
	SectionOrder     int                     `json:"section_order"`
	Title            string                  `json:"title"`
	Ownership        string                  `json:"ownership"`
	IsOrphan         bool                    `json:"is_orphan"`
	ManuallyEdited   bool                    `json:"manually_edited"`
	NeedsDriftReview bool                    `json:"needs_drift_review"`
	ContentSHA256    string                  `json:"content_sha256"`
	Markdown         string                  `json:"markdown"`
	Evidences        []models.EvidenceAnchor `json:"evidences"`
}

type ViewingRevisionVO struct {
	ID               uint                  `json:"id"`
	RevisionNo       int                   `json:"revision_no"`
	SnapshotID       uint                  `json:"snapshot_id"`
	CommitSHA        string                `json:"commit_sha"`
	Source           string                `json:"source"`
	SourceRefID      *uint                 `json:"source_ref_id,omitempty"`
	ChangeSummary    string                `json:"change_summary"`
	VerifierPassed   bool                  `json:"verifier_passed"`
	CanApprove       bool                  `json:"can_approve"`
	SlotCoverageRate float64               `json:"slot_coverage_rate"`
	VerifierReport   models.VerifierReport `json:"verifier_report"`
	CreatedBy        uint                  `json:"created_by"`
	CreatedAt        time.Time             `json:"created_at"`
}

type DocumentRevisionSummaryVO struct {
	ID                uint      `json:"id"`
	RevisionNo        int       `json:"revision_no"`
	SnapshotID        uint      `json:"snapshot_id"`
	CommitSHA         string    `json:"commit_sha"`
	DocTypeRevisionID uint      `json:"doc_type_revision_id"`
	Source            string    `json:"source"`
	SourceRefID       *uint     `json:"source_ref_id,omitempty"`
	ChangeSummary     string    `json:"change_summary"`
	VerifierPassed    bool      `json:"verifier_passed"`
	CanApprove        bool      `json:"can_approve"`
	SlotCoverageRate  float64   `json:"slot_coverage_rate"`
	CreatedBy         uint      `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
}

type DriftDimensionVO struct {
	HasDrift          bool                       `json:"has_drift"`
	Precision         string                     `json:"precision"`
	AffectedSlotCount int                        `json:"affected_slot_count"`
	AffectedSlots     []models.AffectedSlotDrift `json:"affected_slots"`
	DetectedAt        time.Time                  `json:"detected_at"`
}

type DocumentDriftDetailVO struct {
	CodeDrift *DriftDimensionVO `json:"code_drift"`
	SpecDrift *DriftDimensionVO `json:"spec_drift"`
}

type DocumentDetailVO struct {
	ID                  uint                  `json:"id"`
	RepoID              uint                  `json:"repo_id"`
	RepoName            string                `json:"repo_name"`
	Subdirectory        string                `json:"subdirectory"`
	RefName             string                `json:"ref_name"`
	DocTypeSlug         string                `json:"doc_type_slug"`
	DocTypeName         string                `json:"doc_type_name"`
	DocTypeRevisionID   uint                  `json:"doc_type_revision_id"`
	DocTypeRevisionNo   int                   `json:"doc_type_revision_no"`
	Title               string                `json:"title"`
	Status              string                `json:"status"`
	CanWrite            bool                  `json:"can_write"`
	CurrentRevisionNo   int                   `json:"current_revision_no"`
	PublishedRevisionNo int                   `json:"published_revision_no"`
	PublishedCommitSHA  string                `json:"published_commit_sha"`
	PublishedAt         *time.Time            `json:"published_at"`
	ViewingRevision     ViewingRevisionVO     `json:"viewing_revision"`
	Sections            []SectionVO           `json:"sections"`
	Drift               DocumentDriftDetailVO `json:"drift"`
}

type RevisionDiffVO struct {
	DocumentID   uint                    `json:"document_id"`
	FromRev      int                     `json:"from_rev"`
	ToRev        int                     `json:"to_rev"`
	SectionDiffs []drift.SectionDiffItem `json:"section_diffs"`
}

// ─── 请求入参 ───

type ApplySectionChangeInput struct {
	DocID          uint
	SlotID         string
	BaseRevisionNo int
	Markdown       string
	Ownership      string // 可选：不传则保持原值
	ChangeSummary  string
	Source         string // manual_edit | chat_refine
	SourceRefID    *uint
	CitedAnchors   []verifier.CitedAnchorCandidate
	OperatorID     uint
}

// SectionChangeItem 描述多章节原子更新中的单个槽位变更载荷。
type SectionChangeItem struct {
	SlotID       string
	Markdown     string
	Ownership    string // 可选：不传则保持原值
	CitedAnchors []verifier.CitedAnchorCandidate
}

// ApplyMultiSectionChangesInput 为单次 Revision 原子更新一个或多个章节的统一输入（手工直编 W1 + 单/批量 Chat 采纳 W2）。
type ApplyMultiSectionChangesInput struct {
	DocID                  uint
	BaseRevisionNo         int
	Changes                []SectionChangeItem
	ChangeSummary          string
	Source                 string // manual_edit | chat_refine
	SourceRefID            *uint
	SkipSupersedeMessageID uint // 可选：采纳本次提案消息自身时跳过将其标记为 superseded
	OperatorID             uint
}

type ResolveOrphanInput struct {
	DocID          uint
	SlotID         string
	BaseRevisionNo int
	Action         string // keep_locked | merge_into | delete
	TargetSlotID   string // 仅 merge_into 时必填
	OperatorID     uint
}

type RollbackRevisionInput struct {
	DocID            uint
	BaseRevisionNo   int
	TargetRevisionNo int
	ChangeSummary    string
	OperatorID       uint
}

// ─── 重生成锁定与孤立合并引擎入参/出参 ───

type GeneratedSlotPayload struct {
	Markdown     string
	CitedAnchors []verifier.CitedAnchorCandidate
}

type MergeGeneratedInput struct {
	NewSlots       []doctype.SlotSpec
	Generated      map[string]GeneratedSlotPayload
	PriorSections  []models.DocumentSectionSnapshot
	PriorEvidences map[string][]models.EvidenceAnchor
	PriorCommitSHA string
	NewCommitSHA   string
	NewIR          *codeir.CodeIRBundle
}

type MergeGeneratedResult struct {
	Sections      []models.DocumentSectionSnapshot
	SectionInputs []verifier.SectionInput
	SkippedLocked []string
	OrphanSlotIDs []string
}

// RegenerateMergeInput 面向服务层的高阶重生成合并持久化入参（支撑单元测试与增量/全量重生成）。
type RegenerateMergeInput struct {
	DocID           uint
	SnapshotID      uint
	CommitSHA       string
	DocTypeRevision *models.DocTypeRevision
	IR              *codeir.CodeIRBundle
	Generated       map[string]GeneratedSlotPayload
	Source          string
	SourceRefID     *uint
	ChangeSummary   string
	OperatorID      uint
}

// MergeGeneratedWithLockedAndOrphans 实现 F-10 章节所有权合并与模板升级孤立保护纯领域算法：
// 1. locked 章节严格豁免覆盖（Markdown 与 ContentSHA256 逐字节保留），提交或证据变化时标记 needs_drift_review=true；
// 2. hybrid 章节采用融合后的新正文，保留 hybrid 所有权并标记待复核；
// 3. auto 章节使用新生成正文刷新；
// 4. 新模板删除了旧槽位且该旧槽位为 locked 或 manually_edited（或已是孤立章节）时，严禁静默丢弃，挂载至文末 is_orphan=true。
func MergeGeneratedWithLockedAndOrphans(in MergeGeneratedInput) MergeGeneratedResult {
	priorMap := make(map[string]models.DocumentSectionSnapshot, len(in.PriorSections))
	for _, ps := range in.PriorSections {
		priorMap[ps.SlotID] = ps
	}

	var newIRIdx *verifier.IRIndex
	if in.NewIR != nil {
		newIRIdx = verifier.BuildIRIndex(in.NewIR)
	}

	commitChanged := in.PriorCommitSHA != "" && in.NewCommitSHA != "" && in.PriorCommitSHA != in.NewCommitSHA

	sections := make([]models.DocumentSectionSnapshot, 0, len(in.NewSlots))
	sectionInputs := make([]verifier.SectionInput, 0, len(in.NewSlots))
	var skippedLocked []string
	var orphanSlotIDs []string
	newSlotSet := make(map[string]bool, len(in.NewSlots))

	for _, sl := range in.NewSlots {
		newSlotSet[sl.SlotID] = true
		gen := in.Generated[sl.SlotID]
		prior, hasPrior := priorMap[sl.SlotID]

		if hasPrior && prior.Ownership == models.OwnershipLocked && strings.TrimSpace(prior.Markdown) != "" {
			// locked 章节字节级原样保留，绝不覆盖
			needsReview := prior.NeedsDriftReview || commitChanged || hasEvidenceDrifted(in.PriorEvidences[sl.SlotID], newIRIdx)
			sections = append(sections, models.DocumentSectionSnapshot{
				SlotID:           sl.SlotID,
				SectionOrder:     len(sections) + 1,
				Title:            sl.Title,
				Ownership:        models.OwnershipLocked,
				IsOrphan:         false,
				ManuallyEdited:   prior.ManuallyEdited,
				NeedsDriftReview: needsReview,
				Markdown:         prior.Markdown,
				ContentSHA256:    sha256Hex(prior.Markdown),
			})
			sectionInputs = append(sectionInputs, verifier.SectionInput{
				SlotID:   sl.SlotID,
				Title:    sl.Title,
				Markdown: prior.Markdown,
			})
			skippedLocked = append(skippedLocked, sl.SlotID)
			continue
		}

		ownership := sl.Ownership
		manuallyEdited := false
		needsReview := false
		if hasPrior && prior.Ownership == models.OwnershipHybrid {
			ownership = models.OwnershipHybrid
			manuallyEdited = prior.ManuallyEdited
			if prior.ManuallyEdited {
				needsReview = true
			}
		}

		sections = append(sections, models.DocumentSectionSnapshot{
			SlotID:           sl.SlotID,
			SectionOrder:     len(sections) + 1,
			Title:            sl.Title,
			Ownership:        ownership,
			IsOrphan:         false,
			ManuallyEdited:   manuallyEdited,
			NeedsDriftReview: needsReview,
			Markdown:         gen.Markdown,
			ContentSHA256:    sha256Hex(gen.Markdown),
		})
		sectionInputs = append(sectionInputs, verifier.SectionInput{
			SlotID:       sl.SlotID,
			Title:        sl.Title,
			Markdown:     gen.Markdown,
			CitedAnchors: gen.CitedAnchors,
		})
	}

	// 按上一修订原顺序扫描已被新模板移除的槽位，保护 locked / manually_edited / 已孤立 章节
	for _, prior := range in.PriorSections {
		if newSlotSet[prior.SlotID] {
			continue
		}
		if prior.Ownership == models.OwnershipLocked || prior.ManuallyEdited || prior.IsOrphan {
			needsReview := prior.NeedsDriftReview || commitChanged || hasEvidenceDrifted(in.PriorEvidences[prior.SlotID], newIRIdx)
			sections = append(sections, models.DocumentSectionSnapshot{
				SlotID:           prior.SlotID,
				SectionOrder:     len(sections) + 1,
				Title:            prior.Title,
				Ownership:        prior.Ownership,
				IsOrphan:         true,
				ManuallyEdited:   prior.ManuallyEdited,
				NeedsDriftReview: needsReview,
				Markdown:         prior.Markdown,
				ContentSHA256:    sha256Hex(prior.Markdown),
			})
			sectionInputs = append(sectionInputs, verifier.SectionInput{
				SlotID:   prior.SlotID,
				Title:    prior.Title,
				Markdown: prior.Markdown,
			})
			orphanSlotIDs = append(orphanSlotIDs, prior.SlotID)
		}
	}

	return MergeGeneratedResult{
		Sections:      sections,
		SectionInputs: sectionInputs,
		SkippedLocked: skippedLocked,
		OrphanSlotIDs: orphanSlotIDs,
	}
}

func hasEvidenceDrifted(evidences []models.EvidenceAnchor, idx *verifier.IRIndex) bool {
	if idx == nil || len(evidences) == 0 {
		return false
	}
	for _, ev := range evidences {
		fp, found := idx.LookupAnchorFingerprint(ev.SymbolKind, ev.FilePath, ev.FQSymbol)
		if !found {
			return true
		}
		if ev.ContentFingerprint != "" && fp != "" && fp != ev.ContentFingerprint {
			return true
		}
	}
	return false
}

// ─── RevisionService ───

// RevisionService 统一收口双轨章节修订（手工直编/Chat Accept）、所有权切换、孤立章节处置、版本回滚与修订 Diff。
type RevisionService struct {
	docRepo     DocumentRepo
	docTypeRepo DocTypeRepo
	masterRepo  SharedMasterRepo
	moduleRepo  ModuleRepo
	driftRepo   DriftRepo
	chatRepo    ChatRepo
	reviewRepo  ReviewCommentRepo
	snapSvc     *SnapshotService
	verifier    *verifier.Verifier
}

func NewRevisionService(
	docRepo DocumentRepo,
	docTypeRepo DocTypeRepo,
	masterRepo SharedMasterRepo,
	driftRepo DriftRepo,
	snapSvc *SnapshotService,
) *RevisionService {
	return &RevisionService{
		docRepo:     docRepo,
		docTypeRepo: docTypeRepo,
		masterRepo:  masterRepo,
		driftRepo:   driftRepo,
		snapSvc:     snapSvc,
		verifier:    verifier.New(),
	}
}

// WithModuleContext 注入模块索引仓储，使整仓文档在单/多章节修订后重跑 Verifier 时具备 ModuleDigests（修复 P-7 ②）。
func (s *RevisionService) WithModuleContext(moduleRepo ModuleRepo) *RevisionService {
	s.moduleRepo = moduleRepo
	return s
}

// WithReviewHooks 注入 Chat 与审阅批注仓储，使内容覆盖型修订自动将过期 pending 提案标记为 superseded 并退回批注状态。
func (s *RevisionService) WithReviewHooks(chatRepo ChatRepo, reviewRepo ReviewCommentRepo) *RevisionService {
	s.chatRepo = chatRepo
	s.reviewRepo = reviewRepo
	return s
}

// GetDocumentScope 返回文档所属 (repo_id, subdirectory)，供 Handler 在执行 RBAC 判定前查询。
func (s *RevisionService) GetDocumentScope(ctx context.Context, docID uint) (*models.Document, error) {
	return s.docRepo.GetByID(ctx, docID)
}

// ListDocumentsByScope 查询指定 (repo_id, subdirectory, ref_name) 范围下的文档导航树；普通用户仅返回已发布文档。
func (s *RevisionService) ListDocumentsByScope(
	ctx context.Context,
	repoID uint,
	normSubdir string,
	refName string,
	canWrite bool,
) (*DocumentListVO, error) {
	docs, err := s.docRepo.ListByScope(ctx, repoID, normSubdir, refName, !canWrite)
	if err != nil {
		return nil, err
	}

	dtList, _ := s.docTypeRepo.List(ctx, false)
	dtNameBySlug := make(map[string]string, len(dtList))
	for _, dt := range dtList {
		dtNameBySlug[dt.Slug] = dt.DisplayName
	}

	docIDs := make([]uint, 0, len(docs))
	revIDs := make([]uint, 0, len(docs)*2)
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
		if d.CurrentRevisionID != nil {
			revIDs = append(revIDs, *d.CurrentRevisionID)
		}
		if d.PublishedRevisionID != nil {
			revIDs = append(revIDs, *d.PublishedRevisionID)
		}
	}

	revMap, _ := s.docRepo.ListRevisionsByIDs(ctx, revIDs)
	driftByDoc := make(map[uint]map[string]bool)
	if s.driftRepo != nil && len(docIDs) > 0 {
		if drifts, dErr := s.driftRepo.ListDriftsByDocIDs(ctx, docIDs); dErr == nil {
			for _, dr := range drifts {
				if driftByDoc[dr.DocumentID] == nil {
					driftByDoc[dr.DocumentID] = make(map[string]bool)
				}
				if dr.HasDrift {
					driftByDoc[dr.DocumentID][dr.DriftType] = true
				}
			}
		}
	}

	items := make([]DocumentNavItemVO, 0, len(docs))
	for _, d := range docs {
		pubRevNo := 0
		if d.PublishedRevisionID != nil {
			if pr, ok := revMap[*d.PublishedRevisionID]; ok {
				pubRevNo = pr.RevisionNo
			}
		}
		viewRevID := d.PublishedRevisionID
		curRevNo := pubRevNo
		if canWrite {
			viewRevID = d.CurrentRevisionID
			curRevNo = d.CurrentRevisionNo
		}
		verifierPassed := false
		canApprove := false
		if viewRevID != nil {
			if vr, ok := revMap[*viewRevID]; ok {
				verifierPassed = vr.VerifierPassed
				canApprove = vr.CanApprove
			}
		}

		dtName := dtNameBySlug[d.DocTypeSlug]
		if dtName == "" {
			dtName = d.DocTypeSlug
		}
		items = append(items, DocumentNavItemVO{
			ID:                  d.ID,
			RepoID:              d.RepoID,
			Subdirectory:        d.Subdirectory,
			RefName:             d.RefName,
			DocTypeSlug:         d.DocTypeSlug,
			DocTypeName:         dtName,
			Title:               d.Title,
			Status:              d.Status,
			CurrentRevisionNo:   curRevNo,
			PublishedRevisionNo: pubRevNo,
			VerifierPassed:      verifierPassed,
			CanApprove:          canApprove,
			HasCodeDrift:        driftByDoc[d.ID][models.DriftTypeCode],
			HasSpecDrift:        driftByDoc[d.ID][models.DriftTypeSpec],
			UpdatedAt:           d.UpdatedAt,
		})
	}

	return &DocumentListVO{
		CanWrite: canWrite,
		Items:    items,
	}, nil
}

// GetDocumentDetail 获取单篇文档详情；普通用户强制只看 published_revision_id 快照，访问草稿返回 ErrDraftAccessDenied。
func (s *RevisionService) GetDocumentDetail(
	ctx context.Context,
	docID uint,
	requestedRevNo *int,
	canWrite bool,
) (*DocumentDetailVO, error) {
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	var (
		rev      *models.DocumentRevision
		sections []models.SectionEvidence
	)

	if !canWrite {
		// 草稿不可见硬隔离：普通用户访问草稿返回 403 DRAFT_ACCESS_DENIED，已发布文档强制看 PublishedRevisionID
		if doc.Status != models.DocStatusApproved || doc.PublishedRevisionID == nil {
			return nil, ErrDraftAccessDenied
		}
		rev, sections, err = s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if err != nil {
			return nil, err
		}
	} else {
		targetRevNo := doc.CurrentRevisionNo
		if requestedRevNo != nil && *requestedRevNo > 0 {
			targetRevNo = *requestedRevNo
		}
		if targetRevNo <= 0 {
			return nil, models.ErrNotFound
		}
		rev, sections, err = s.docRepo.GetRevisionWithSections(ctx, doc.ID, targetRevNo)
		if err != nil {
			return nil, err
		}
	}

	return s.buildDocumentDetailVO(ctx, doc, rev, sections, canWrite)
}

// ListDocumentRevisions 查询文档不可变修订时间线（不含 full_markdown 大正文）。
func (s *RevisionService) ListDocumentRevisions(ctx context.Context, docID uint) ([]DocumentRevisionSummaryVO, error) {
	if _, err := s.docRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}
	revs, err := s.docRepo.ListRevisions(ctx, docID)
	if err != nil {
		return nil, err
	}
	items := make([]DocumentRevisionSummaryVO, 0, len(revs))
	for _, r := range revs {
		items = append(items, DocumentRevisionSummaryVO{
			ID:                r.ID,
			RevisionNo:        r.RevisionNo,
			SnapshotID:        r.SnapshotID,
			CommitSHA:         r.CommitSHA,
			DocTypeRevisionID: r.DocTypeRevisionID,
			Source:            r.Source,
			SourceRefID:       r.SourceRefID,
			ChangeSummary:     r.ChangeSummary,
			VerifierPassed:    r.VerifierPassed,
			CanApprove:        r.CanApprove,
			SlotCoverageRate:  r.SlotCoverageRate,
			CreatedBy:         r.CreatedBy,
			CreatedAt:         r.CreatedAt,
		})
	}
	return items, nil
}

// GetRevisionDiff 对比同一文档任意两个修订版本的章节级 Unified Diff。
func (s *RevisionService) GetRevisionDiff(ctx context.Context, docID uint, fromRevNo, toRevNo int) (*RevisionDiffVO, error) {
	if fromRevNo <= 0 || toRevNo <= 0 {
		return nil, fmt.Errorf("%w: from_rev 与 to_rev 必须为正整数", ErrInvalidRevisionParam)
	}
	if _, err := s.docRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}

	fromRev, _, err := s.docRepo.GetRevisionWithSections(ctx, docID, fromRevNo)
	if err != nil {
		return nil, err
	}
	toRev, _, err := s.docRepo.GetRevisionWithSections(ctx, docID, toRevNo)
	if err != nil {
		return nil, err
	}

	fromSnaps := unmarshalSectionSnapshots(fromRev.SectionsJSON)
	toSnaps := unmarshalSectionSnapshots(toRev.SectionsJSON)

	fromContents := make([]drift.SectionContent, 0, len(fromSnaps))
	for _, sn := range fromSnaps {
		fromContents = append(fromContents, drift.SectionContent{
			SlotID:   sn.SlotID,
			Title:    sn.Title,
			Markdown: sn.Markdown,
		})
	}
	toContents := make([]drift.SectionContent, 0, len(toSnaps))
	for _, sn := range toSnaps {
		toContents = append(toContents, drift.SectionContent{
			SlotID:   sn.SlotID,
			Title:    sn.Title,
			Markdown: sn.Markdown,
		})
	}

	return &RevisionDiffVO{
		DocumentID:   docID,
		FromRev:      fromRevNo,
		ToRev:        toRevNo,
		SectionDiffs: drift.ComputeSectionDiffs(fromContents, toContents),
	}, nil
}

// ApplySectionChange 统一双轨单槽位编辑管线（手工直编 W1 + Chat Accept W2）：
// 内部委托给 ApplyMultiSectionChanges，确保单双轨统一逻辑与证据锚点不丢失。
func (s *RevisionService) ApplySectionChange(ctx context.Context, req ApplySectionChangeInput) (*DocumentDetailVO, error) {
	if req.SlotID == "" {
		return nil, fmt.Errorf("%w: slot_id 不能为空", ErrInvalidRevisionParam)
	}
	var skipMsgID uint
	if req.Source == models.RevSourceChatRefine && req.SourceRefID != nil {
		skipMsgID = *req.SourceRefID
	}
	return s.ApplyMultiSectionChanges(ctx, ApplyMultiSectionChangesInput{
		DocID:          req.DocID,
		BaseRevisionNo: req.BaseRevisionNo,
		Changes: []SectionChangeItem{{
			SlotID:       req.SlotID,
			Markdown:     req.Markdown,
			Ownership:    req.Ownership,
			CitedAnchors: req.CitedAnchors,
		}},
		ChangeSummary:          req.ChangeSummary,
		Source:                 req.Source,
		SourceRefID:            req.SourceRefID,
		SkipSupersedeMessageID: skipMsgID,
		OperatorID:             req.OperatorID,
	})
}

// ApplyMultiSectionChanges 原子多章节编辑管线（单槽位编辑、批量审阅提案局部/全量采纳）：
// 校验乐观锁 -> 锁定章节禁止 AI 写入 -> 仅更新变更槽位且未修改槽位复用 prevEvs 锚点 -> 重跑 Verifier -> 原子追加单次 Revision -> 失效同槽位旧提案。
func (s *RevisionService) ApplyMultiSectionChanges(ctx context.Context, req ApplyMultiSectionChangesInput) (*DocumentDetailVO, error) {
	if req.BaseRevisionNo <= 0 {
		return nil, fmt.Errorf("%w: base_revision_no 必须为正整数", ErrInvalidRevisionParam)
	}
	if len(req.Changes) == 0 {
		return nil, fmt.Errorf("%w: changes 不能为空", ErrInvalidRevisionParam)
	}
	source := req.Source
	if source == "" {
		source = models.RevSourceManualEdit
	}
	if !models.IsValidRevisionSource(source) {
		return nil, fmt.Errorf("%w: 非法修订来源 %q", ErrInvalidRevisionParam, source)
	}

	seenSlots := make(map[string]bool, len(req.Changes))
	changeBySlot := make(map[string]SectionChangeItem, len(req.Changes))
	changedSlotIDs := make([]string, 0, len(req.Changes))
	for _, ch := range req.Changes {
		sid := strings.TrimSpace(ch.SlotID)
		if sid == "" {
			return nil, fmt.Errorf("%w: changes 中存在空 slot_id", ErrInvalidRevisionParam)
		}
		if seenSlots[sid] {
			return nil, fmt.Errorf("%w: changes 中存在重复 slot_id: %s", ErrInvalidRevisionParam, sid)
		}
		if ch.Ownership != "" && !models.IsValidSectionOwnership(ch.Ownership) {
			return nil, fmt.Errorf("%w: ownership 必须为 auto、hybrid 或 locked", ErrInvalidRevisionParam)
		}
		seenSlots[sid] = true
		ch.SlotID = sid
		changeBySlot[sid] = ch
		changedSlotIDs = append(changedSlotIDs, sid)
	}

	doc, err := s.docRepo.GetByID(ctx, req.DocID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo != req.BaseRevisionNo {
		return nil, &OptimisticLockConflictError{CurrentRevisionNo: doc.CurrentRevisionNo}
	}

	prevRev, prevEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, req.BaseRevisionNo)
	if err != nil {
		return nil, err
	}

	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(prevRev.SectionsJSON), prevEvs)
	slotIndex := make(map[string]int, len(snaps))
	for i := range snaps {
		slotIndex[snaps[i].SlotID] = i
	}
	for _, sid := range changedSlotIDs {
		idx, ok := slotIndex[sid]
		if !ok {
			return nil, models.ErrNotFound
		}
		if source == models.RevSourceChatRefine && snaps[idx].Ownership == models.OwnershipLocked {
			return nil, ErrSlotLocked
		}
	}

	updatedSnaps := make([]models.DocumentSectionSnapshot, len(snaps))
	copy(updatedSnaps, snaps)
	for _, sid := range changedSlotIDs {
		idx := slotIndex[sid]
		ch := changeBySlot[sid]
		updatedSnaps[idx].Markdown = ch.Markdown
		updatedSnaps[idx].ContentSHA256 = sha256Hex(ch.Markdown)
		updatedSnaps[idx].ManuallyEdited = true
		if ch.Ownership != "" {
			updatedSnaps[idx].Ownership = ch.Ownership
		}
	}

	prevAnchorsBySlot := parsePrevAnchorsBySlot(prevEvs)
	secInputs := make([]verifier.SectionInput, 0, len(updatedSnaps))
	for _, sn := range updatedSnaps {
		var cited []verifier.CitedAnchorCandidate
		if ch, modified := changeBySlot[sn.SlotID]; modified {
			cited = ch.CitedAnchors
		} else {
			// P-7 ① 修复：未修改章节复用上版已对齐证据锚点（含 module_doc / proto_rpc / http_route 等非内联锚点）
			cited = evidenceAnchorsToCandidates(prevAnchorsBySlot[sn.SlotID])
		}
		secInputs = append(secInputs, verifier.SectionInput{
			SlotID:       sn.SlotID,
			Title:        sn.Title,
			Markdown:     sn.Markdown,
			CitedAnchors: cited,
		})
	}

	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		if len(changedSlotIDs) == 1 {
			summary = fmt.Sprintf("编辑章节 %s", changedSlotIDs[0])
		} else {
			summary = fmt.Sprintf("批量更新 %d 个章节 (%s)", len(changedSlotIDs), strings.Join(changedSlotIDs, ", "))
		}
	}

	newRev, newEvs, err := s.verifyAndAppend(
		ctx, doc, req.BaseRevisionNo, prevRev.SnapshotID, prevRev.CommitSHA, prevRev.DocTypeRevisionID,
		nil, updatedSnaps, secInputs, prevEvs, source, req.SourceRefID, summary, req.OperatorID,
	)
	if err != nil {
		return nil, err
	}

	s.afterContentRevisionHooks(ctx, doc.ID, changedSlotIDs, req.SkipSupersedeMessageID)
	return s.buildDocumentDetailVO(ctx, doc, newRev, newEvs, true)
}

// UpdateSectionOwnership 切换当前修订目标槽位的所有权模式（auto/hybrid/locked）或清除待核对标记。
func (s *RevisionService) UpdateSectionOwnership(
	ctx context.Context,
	docID uint,
	slotID string,
	ownership string,
	clearDriftReview bool,
) (*SectionVO, error) {
	if !models.IsValidSectionOwnership(ownership) {
		return nil, fmt.Errorf("%w: ownership 必须为 auto、hybrid 或 locked", ErrInvalidRevisionParam)
	}
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionID == nil || doc.CurrentRevisionNo <= 0 {
		return nil, models.ErrNotFound
	}

	if err := s.docRepo.UpdateSectionOwnership(ctx, doc.ID, *doc.CurrentRevisionID, slotID, ownership, clearDriftReview); err != nil {
		return nil, err
	}

	rev, evs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
	if err != nil {
		return nil, err
	}
	sectionVOs := buildSectionVOs(rev, evs)
	for i := range sectionVOs {
		if sectionVOs[i].SlotID == slotID {
			return &sectionVOs[i], nil
		}
	}
	return nil, models.ErrNotFound
}

// ResolveOrphanSection 处置模板升级产生的孤立章节（keep_locked / merge_into / delete），生成新不可变修订。
func (s *RevisionService) ResolveOrphanSection(ctx context.Context, req ResolveOrphanInput) (*DocumentDetailVO, error) {
	if req.BaseRevisionNo <= 0 {
		return nil, fmt.Errorf("%w: base_revision_no 必须为正整数", ErrInvalidRevisionParam)
	}
	if req.Action != "keep_locked" && req.Action != "merge_into" && req.Action != "delete" {
		return nil, fmt.Errorf("%w: action 仅支持 keep_locked、merge_into 或 delete", ErrInvalidRevisionParam)
	}
	if req.Action == "merge_into" && strings.TrimSpace(req.TargetSlotID) == "" {
		return nil, fmt.Errorf("%w: merge_into 必须提供 target_slot_id", ErrInvalidRevisionParam)
	}

	doc, err := s.docRepo.GetByID(ctx, req.DocID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo != req.BaseRevisionNo {
		return nil, &OptimisticLockConflictError{CurrentRevisionNo: doc.CurrentRevisionNo}
	}

	prevRev, prevEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, req.BaseRevisionNo)
	if err != nil {
		return nil, err
	}
	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(prevRev.SectionsJSON), prevEvs)

	orphanIdx := -1
	for i := range snaps {
		if snaps[i].SlotID == req.SlotID {
			orphanIdx = i
			break
		}
	}
	if orphanIdx < 0 {
		return nil, models.ErrNotFound
	}
	if !snaps[orphanIdx].IsOrphan {
		return nil, ErrSectionNotOrphan
	}

	orphanSec := snaps[orphanIdx]
	var nextSnaps []models.DocumentSectionSnapshot
	var summary string

	switch req.Action {
	case "keep_locked":
		nextSnaps = make([]models.DocumentSectionSnapshot, len(snaps))
		copy(nextSnaps, snaps)
		nextSnaps[orphanIdx].IsOrphan = false
		nextSnaps[orphanIdx].Ownership = models.OwnershipLocked
		summary = fmt.Sprintf("孤立章节处置：保留 %s 为自定义锁定章节", req.SlotID)

	case "merge_into":
		if req.TargetSlotID == req.SlotID {
			return nil, fmt.Errorf("%w: 不能将孤立章节合并至自身", ErrInvalidRevisionParam)
		}
		targetFound := false
		for i, sn := range snaps {
			if i == orphanIdx {
				continue
			}
			cp := sn
			if cp.SlotID == req.TargetSlotID {
				if cp.IsOrphan {
					return nil, fmt.Errorf("%w: 目标合并章节 %s 不能为孤立章节", ErrInvalidRevisionParam, req.TargetSlotID)
				}
				mergedMD := strings.TrimRight(cp.Markdown, "\n") + "\n\n### " + orphanSec.Title + "\n\n" + strings.TrimSpace(orphanSec.Markdown)
				cp.Markdown = mergedMD
				cp.ContentSHA256 = sha256Hex(mergedMD)
				cp.ManuallyEdited = true
				targetFound = true
			}
			nextSnaps = append(nextSnaps, cp)
		}
		if !targetFound {
			return nil, models.ErrNotFound
		}
		summary = fmt.Sprintf("孤立章节处置：合并 %s 至 %s", req.SlotID, req.TargetSlotID)

	case "delete":
		for i, sn := range snaps {
			if i == orphanIdx {
				continue
			}
			nextSnaps = append(nextSnaps, sn)
		}
		summary = fmt.Sprintf("孤立章节处置：确认删除 %s", req.SlotID)
	}

	secInputs := make([]verifier.SectionInput, 0, len(nextSnaps))
	for i := range nextSnaps {
		nextSnaps[i].SectionOrder = i + 1
		secInputs = append(secInputs, verifier.SectionInput{
			SlotID:   nextSnaps[i].SlotID,
			Title:    nextSnaps[i].Title,
			Markdown: nextSnaps[i].Markdown,
		})
	}

	newRev, newEvs, err := s.verifyAndAppend(
		ctx, doc, req.BaseRevisionNo, prevRev.SnapshotID, prevRev.CommitSHA, prevRev.DocTypeRevisionID,
		nil, nextSnaps, secInputs, prevEvs, models.RevSourceManualEdit, nil, summary, req.OperatorID,
	)
	if err != nil {
		return nil, err
	}
	return s.buildDocumentDetailVO(ctx, doc, newRev, newEvs, true)
}

// RollbackRevision 一键回滚至指定历史修订：绝不删除历史，以 source="rollback" 追加新的不可变修订。
func (s *RevisionService) RollbackRevision(ctx context.Context, req RollbackRevisionInput) (*DocumentDetailVO, error) {
	if req.BaseRevisionNo <= 0 || req.TargetRevisionNo <= 0 {
		return nil, fmt.Errorf("%w: base_revision_no 与 target_revision_no 必须为正整数", ErrInvalidRevisionParam)
	}
	doc, err := s.docRepo.GetByID(ctx, req.DocID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo != req.BaseRevisionNo {
		return nil, &OptimisticLockConflictError{CurrentRevisionNo: doc.CurrentRevisionNo}
	}

	targetRev, targetEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, req.TargetRevisionNo)
	if err != nil {
		return nil, err
	}

	snaps := unmarshalSectionSnapshots(targetRev.SectionsJSON)
	secInputs := make([]verifier.SectionInput, 0, len(snaps))
	for i := range snaps {
		snaps[i].SectionOrder = i + 1
		snaps[i].ContentSHA256 = sha256Hex(snaps[i].Markdown)
		secInputs = append(secInputs, verifier.SectionInput{
			SlotID:   snaps[i].SlotID,
			Title:    snaps[i].Title,
			Markdown: snaps[i].Markdown,
		})
	}

	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = fmt.Sprintf("回滚至修订版本 v%d", req.TargetRevisionNo)
	}
	refID := targetRev.ID

	newRev, newEvs, err := s.verifyAndAppend(
		ctx, doc, req.BaseRevisionNo, targetRev.SnapshotID, targetRev.CommitSHA, targetRev.DocTypeRevisionID,
		nil, snaps, secInputs, targetEvs, models.RevSourceRollback, &refID, summary, req.OperatorID,
	)
	if err != nil {
		return nil, err
	}
	allSlots := make([]string, 0, len(snaps))
	for _, sn := range snaps {
		allSlots = append(allSlots, sn.SlotID)
	}
	s.afterContentRevisionHooks(ctx, doc.ID, allSlots, 0)
	return s.buildDocumentDetailVO(ctx, doc, newRev, newEvs, true)
}

// RegenerateWithMerge 使用锁定保护与模板升级孤立保护合并新生成章节并落库不可变修订。
func (s *RevisionService) RegenerateWithMerge(ctx context.Context, req RegenerateMergeInput) (*DocumentDetailVO, error) {
	if req.DocTypeRevision == nil {
		return nil, fmt.Errorf("%w: doc_type_revision 不能为空", ErrInvalidRevisionParam)
	}
	doc, err := s.docRepo.GetByID(ctx, req.DocID)
	if err != nil {
		return nil, err
	}

	asset, err := parseAssetFromDocTypeRevision(req.DocTypeRevision, doc.DocTypeSlug)
	if err != nil {
		return nil, err
	}

	var (
		priorSections  []models.DocumentSectionSnapshot
		priorEvidences map[string][]models.EvidenceAnchor
		priorEvRows    []models.SectionEvidence
		priorCommitSHA string
	)
	if doc.CurrentRevisionNo > 0 {
		prevRev, prevEvs, pErr := s.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
		if pErr != nil {
			return nil, pErr
		}
		priorCommitSHA = prevRev.CommitSHA
		priorEvRows = prevEvs
		priorSections = overlaySectionMetadata(unmarshalSectionSnapshots(prevRev.SectionsJSON), prevEvs)
		priorEvidences = make(map[string][]models.EvidenceAnchor, len(prevEvs))
		for _, ev := range prevEvs {
			var list []models.EvidenceAnchor
			_ = json.Unmarshal(ev.EvidencesJSON, &list)
			priorEvidences[ev.SlotID] = list
		}
	}

	merged := MergeGeneratedWithLockedAndOrphans(MergeGeneratedInput{
		NewSlots:       asset.Meta.Slots,
		Generated:      req.Generated,
		PriorSections:  priorSections,
		PriorEvidences: priorEvidences,
		PriorCommitSHA: priorCommitSHA,
		NewCommitSHA:   req.CommitSHA,
		NewIR:          req.IR,
	})

	source := req.Source
	if source == "" {
		source = models.RevSourceAIGenerate
	}
	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = "重新生成文档并保留锁定章节"
	}

	newRev, newEvs, err := s.verifyAndAppend(
		ctx, doc, doc.CurrentRevisionNo, req.SnapshotID, req.CommitSHA, req.DocTypeRevision.ID,
		req.IR, merged.Sections, merged.SectionInputs, priorEvRows, source, req.SourceRefID, summary, req.OperatorID,
	)
	if err != nil {
		return nil, err
	}
	allSlots := make([]string, 0, len(merged.Sections))
	for _, sn := range merged.Sections {
		allSlots = append(allSlots, sn.SlotID)
	}
	s.afterContentRevisionHooks(ctx, doc.ID, allSlots, 0)
	return s.buildDocumentDetailVO(ctx, doc, newRev, newEvs, true)
}

func (s *RevisionService) verifyAndAppend(
	ctx context.Context,
	doc *models.Document,
	baseRevisionNo int,
	snapshotID uint,
	commitSHA string,
	docTypeRevID uint,
	providedIR *codeir.CodeIRBundle,
	sectionSnapshots []models.DocumentSectionSnapshot,
	sectionInputs []verifier.SectionInput,
	priorEvRows []models.SectionEvidence,
	source string,
	sourceRefID *uint,
	changeSummary string,
	operatorID uint,
) (*models.DocumentRevision, []models.SectionEvidence, error) {
	dtRev, err := s.docTypeRepo.GetRevisionByID(ctx, docTypeRevID)
	if err != nil {
		return nil, nil, err
	}
	asset, err := parseAssetFromDocTypeRevision(dtRev, doc.DocTypeSlug)
	if err != nil {
		return nil, nil, err
	}

	irBundle := providedIR
	var (
		modCtx        *codeir.ModuleContextFacet
		moduleDigests []codeir.ModuleDigestInput
	)
	if s.snapSvc != nil && snapshotID > 0 {
		if snap, loc, loadedIR, lErr := s.snapSvc.LoadIRBundleBySnapshotID(ctx, snapshotID); lErr == nil {
			if irBundle == nil {
				irBundle = loadedIR
			}
			modCtx = s.snapSvc.LoadModuleContext(ctx, snap, loc)
			if doc.Subdirectory == "" && s.moduleRepo != nil {
				glossary, _ := s.snapSvc.ReadCachedGlossary(ctx, snap, loc)
				moduleDigests, _ = buildModuleDigestsForRepo(ctx, s.moduleRepo, s.masterRepo, s.docRepo, doc.RepoID, doc.RefName, asset, glossary)
			}
		}
	}

	vRes := s.verifier.Verify(verifier.VerifyInput{
		CommitSHA:     commitSHA,
		IR:            irBundle,
		ModuleContext: modCtx,
		ModuleDigests: moduleDigests,
		Slots:         asset.Meta.Slots,
		Policy:        asset.Meta.VerifyPolicy,
		Sections:      sectionInputs,
	})

	// 当快照 IR 已被回收导致本次未加载 IR 时，保留旧修订已对齐的证据锚点以免展示空白
	priorEvMap := make(map[string]datatypes.JSON, len(priorEvRows))
	for _, pe := range priorEvRows {
		priorEvMap[pe.SlotID] = pe.EvidencesJSON
	}

	fullMD := assembleFullMarkdown(asset.TemplateMD, sectionSnapshots)
	sectionsJSON, _ := json.Marshal(sectionSnapshots)
	reportJSON, _ := json.Marshal(vRes.Report)

	rev := &models.DocumentRevision{
		SnapshotID:         snapshotID,
		CommitSHA:          commitSHA,
		DocTypeRevisionID:  docTypeRevID,
		Source:             source,
		SourceRefID:        sourceRefID,
		ChangeSummary:      changeSummary,
		SectionsJSON:       datatypes.JSON(sectionsJSON),
		FullMarkdown:       fullMD,
		VerifierPassed:     vRes.Report.Passed,
		CanApprove:         vRes.Report.CanApprove,
		SlotCoverageRate:   vRes.Report.SlotCoverageRate,
		VerifierReportJSON: datatypes.JSON(reportJSON),
		CreatedBy:          operatorID,
	}

	secEvidences := make([]models.SectionEvidence, 0, len(sectionSnapshots))
	for _, sn := range sectionSnapshots {
		evList := vRes.SectionEvidences[sn.SlotID]
		var evJSON datatypes.JSON
		if len(evList) > 0 {
			raw, _ := json.Marshal(evList)
			evJSON = datatypes.JSON(raw)
		} else if len(priorEvMap[sn.SlotID]) > 2 {
			evJSON = priorEvMap[sn.SlotID]
		} else {
			evJSON = datatypes.JSON("[]")
		}
		sumJSON, _ := json.Marshal(vRes.SectionSummaries[sn.SlotID])
		secEvidences = append(secEvidences, models.SectionEvidence{
			SlotID:              sn.SlotID,
			SectionOrder:        sn.SectionOrder,
			Title:               sn.Title,
			Ownership:           sn.Ownership,
			IsOrphan:            sn.IsOrphan,
			ManuallyEdited:      sn.ManuallyEdited,
			NeedsDriftReview:    sn.NeedsDriftReview,
			ContentSHA256:       sn.ContentSHA256,
			VerifierSummaryJSON: datatypes.JSON(sumJSON),
			EvidencesJSON:       evJSON,
		})
	}

	if err := s.docRepo.AppendRevisionWithOptimisticLock(ctx, doc, baseRevisionNo, rev, secEvidences); err != nil {
		if errors.Is(err, models.ErrOptimisticLockConflict) {
			curNo := doc.CurrentRevisionNo
			if fresh, fErr := s.docRepo.GetByID(ctx, doc.ID); fErr == nil && fresh != nil {
				curNo = fresh.CurrentRevisionNo
			}
			return nil, nil, &OptimisticLockConflictError{CurrentRevisionNo: curNo}
		}
		return nil, nil, err
	}

	return rev, secEvidences, nil
}

func (s *RevisionService) afterContentRevisionHooks(ctx context.Context, docID uint, changedSlotIDs []string, skipMsgID uint) {
	if s.chatRepo == nil || len(changedSlotIDs) == 0 {
		return
	}
	affectedCommentIDs, err := s.chatRepo.SupersedePendingProposalsBySlots(ctx, docID, changedSlotIDs, skipMsgID)
	if err != nil {
		log.Printf("[Code-Archdoc] 标记同槽位过期 pending 提案失败 (doc_id=%d): %v", docID, err)
		return
	}
	if s.reviewRepo != nil && len(affectedCommentIDs) > 0 {
		if rErr := s.reviewRepo.RevertProposedByCommentIDs(ctx, docID, affectedCommentIDs); rErr != nil {
			log.Printf("[Code-Archdoc] 退回失效提案关联批注状态失败 (doc_id=%d): %v", docID, rErr)
		}
	}
}

func parsePrevAnchorsBySlot(prevEvRows []models.SectionEvidence) map[string][]models.EvidenceAnchor {
	out := make(map[string][]models.EvidenceAnchor, len(prevEvRows))
	for _, pe := range prevEvRows {
		if len(pe.EvidencesJSON) <= 2 {
			continue
		}
		var list []models.EvidenceAnchor
		if json.Unmarshal(pe.EvidencesJSON, &list) == nil && len(list) > 0 {
			out[pe.SlotID] = list
		}
	}
	return out
}

func evidenceAnchorsToCandidates(anchors []models.EvidenceAnchor) []verifier.CitedAnchorCandidate {
	if len(anchors) == 0 {
		return nil
	}
	out := make([]verifier.CitedAnchorCandidate, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, verifier.CitedAnchorCandidate{
			FilePath:   a.FilePath,
			FQSymbol:   a.FQSymbol,
			SymbolKind: a.SymbolKind,
			Scope:      a.Scope,
		})
	}
	return out
}

func (s *RevisionService) buildDocumentDetailVO(
	ctx context.Context,
	doc *models.Document,
	rev *models.DocumentRevision,
	evidences []models.SectionEvidence,
	canWrite bool,
) (*DocumentDetailVO, error) {
	repoName := ""
	if s.masterRepo != nil {
		if repo, rErr := s.masterRepo.GetRepository(ctx, doc.RepoID); rErr == nil && repo != nil {
			repoName = repo.Name
		}
	}

	docTypeName := doc.DocTypeSlug
	docTypeRevNo := 0
	if dt, _, dtErr := s.docTypeRepo.GetBySlug(ctx, doc.DocTypeSlug); dtErr == nil && dt != nil {
		docTypeName = dt.DisplayName
	}
	if dtRev, rErr := s.docTypeRepo.GetRevisionByID(ctx, rev.DocTypeRevisionID); rErr == nil && dtRev != nil {
		docTypeRevNo = dtRev.RevisionNo
	}

	pubRevNo := 0
	if doc.PublishedRevisionID != nil {
		if *doc.PublishedRevisionID == rev.ID {
			pubRevNo = rev.RevisionNo
		} else if revMap, err := s.docRepo.ListRevisionsByIDs(ctx, []uint{*doc.PublishedRevisionID}); err == nil {
			if pr, ok := revMap[*doc.PublishedRevisionID]; ok {
				pubRevNo = pr.RevisionNo
			}
		}
	}

	var vReport models.VerifierReport
	_ = json.Unmarshal(rev.VerifierReportJSON, &vReport)
	normalizeVerifierReportSlices(&vReport)

	var driftVO DocumentDriftDetailVO
	if s.driftRepo != nil {
		if drifts, dErr := s.driftRepo.ListDriftsByDocIDs(ctx, []uint{doc.ID}); dErr == nil {
			for _, dr := range drifts {
				var affected []models.AffectedSlotDrift
				_ = json.Unmarshal(dr.AffectedSlotsJSON, &affected)
				if affected == nil {
					affected = []models.AffectedSlotDrift{}
				}
				dim := &DriftDimensionVO{
					HasDrift:          dr.HasDrift,
					Precision:         dr.Precision,
					AffectedSlotCount: dr.AffectedSlotCount,
					AffectedSlots:     affected,
					DetectedAt:        dr.DetectedAt,
				}
				if dr.DriftType == models.DriftTypeCode {
					driftVO.CodeDrift = dim
				} else if dr.DriftType == models.DriftTypeSpec {
					driftVO.SpecDrift = dim
				}
			}
		}
	}

	curRevNo := doc.CurrentRevisionNo
	if !canWrite {
		// 普通用户视角避免暴露未发布草稿版本号
		curRevNo = pubRevNo
	}

	return &DocumentDetailVO{
		ID:                  doc.ID,
		RepoID:              doc.RepoID,
		RepoName:            repoName,
		Subdirectory:        doc.Subdirectory,
		RefName:             doc.RefName,
		DocTypeSlug:         doc.DocTypeSlug,
		DocTypeName:         docTypeName,
		DocTypeRevisionID:   rev.DocTypeRevisionID,
		DocTypeRevisionNo:   docTypeRevNo,
		Title:               doc.Title,
		Status:              doc.Status,
		CanWrite:            canWrite,
		CurrentRevisionNo:   curRevNo,
		PublishedRevisionNo: pubRevNo,
		PublishedCommitSHA:  doc.PublishedCommitSHA,
		PublishedAt:         doc.PublishedAt,
		ViewingRevision: ViewingRevisionVO{
			ID:               rev.ID,
			RevisionNo:       rev.RevisionNo,
			SnapshotID:       rev.SnapshotID,
			CommitSHA:        rev.CommitSHA,
			Source:           rev.Source,
			SourceRefID:      rev.SourceRefID,
			ChangeSummary:    rev.ChangeSummary,
			VerifierPassed:   rev.VerifierPassed,
			CanApprove:       rev.CanApprove,
			SlotCoverageRate: rev.SlotCoverageRate,
			VerifierReport:   vReport,
			CreatedBy:        rev.CreatedBy,
			CreatedAt:        rev.CreatedAt,
		},
		Sections: buildSectionVOs(rev, evidences),
		Drift:    driftVO,
	}, nil
}

func buildSectionVOs(rev *models.DocumentRevision, evidences []models.SectionEvidence) []SectionVO {
	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(rev.SectionsJSON), evidences)
	evBySlot := make(map[string][]models.EvidenceAnchor, len(evidences))
	for _, ev := range evidences {
		var anchors []models.EvidenceAnchor
		_ = json.Unmarshal(ev.EvidencesJSON, &anchors)
		if anchors == nil {
			anchors = []models.EvidenceAnchor{}
		}
		evBySlot[ev.SlotID] = anchors
	}

	out := make([]SectionVO, 0, len(snaps))
	for _, sn := range snaps {
		anchors := evBySlot[sn.SlotID]
		if anchors == nil {
			anchors = []models.EvidenceAnchor{}
		}
		out = append(out, SectionVO{
			SlotID:           sn.SlotID,
			SectionOrder:     sn.SectionOrder,
			Title:            sn.Title,
			Ownership:        sn.Ownership,
			IsOrphan:         sn.IsOrphan,
			ManuallyEdited:   sn.ManuallyEdited,
			NeedsDriftReview: sn.NeedsDriftReview,
			ContentSHA256:    sn.ContentSHA256,
			Markdown:         sn.Markdown,
			Evidences:        anchors,
		})
	}
	return out
}

func unmarshalSectionSnapshots(raw datatypes.JSON) []models.DocumentSectionSnapshot {
	var snaps []models.DocumentSectionSnapshot
	_ = json.Unmarshal(raw, &snaps)
	return snaps
}

// overlaySectionMetadata 将 SectionEvidence 中实时更新的 ownership 与 needs_drift_review 叠加到不可变快照上。
func overlaySectionMetadata(snaps []models.DocumentSectionSnapshot, evidences []models.SectionEvidence) []models.DocumentSectionSnapshot {
	if len(evidences) == 0 {
		return snaps
	}
	evMap := make(map[string]models.SectionEvidence, len(evidences))
	for _, ev := range evidences {
		evMap[ev.SlotID] = ev
	}
	out := make([]models.DocumentSectionSnapshot, len(snaps))
	for i, sn := range snaps {
		if ev, ok := evMap[sn.SlotID]; ok {
			sn.Ownership = ev.Ownership
			sn.NeedsDriftReview = ev.NeedsDriftReview
			sn.IsOrphan = ev.IsOrphan
			sn.ManuallyEdited = ev.ManuallyEdited
		}
		out[i] = sn
	}
	return out
}

func parseAssetFromDocTypeRevision(rev *models.DocTypeRevision, slug string) (*doctype.AssetBundle, error) {
	var meta doctype.Meta
	if err := json.Unmarshal(rev.MetaJSON, &meta); err != nil {
		return nil, fmt.Errorf("解析 DocType %s 修订元数据失败: %w", slug, err)
	}
	return &doctype.AssetBundle{
		Slug:               slug,
		Meta:               meta,
		RawMetaJSON:        rev.MetaJSON,
		TemplateMD:         rev.TemplateMD,
		GenerationPromptMD: rev.GenerationPromptMD,
		RefinePromptMD:     rev.RefinePromptMD,
		AggregateHash:      rev.AggregateHash,
		PromptHash:         rev.PromptHash,
	}, nil
}

func normalizeVerifierReportSlices(r *models.VerifierReport) {
	if r.MissingRequiredSlots == nil {
		r.MissingRequiredSlots = []string{}
	}
	if r.UnverifiedRefs == nil {
		r.UnverifiedRefs = []models.UnverifiedRef{}
	}
	if r.MermaidIssues == nil {
		r.MermaidIssues = []models.MermaidIssue{}
	}
	if r.SecretFindings == nil {
		r.SecretFindings = []models.SecretFinding{}
	}
}
