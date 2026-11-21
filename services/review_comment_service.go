package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"code-archdoc/models"

	"gorm.io/datatypes"
)

const (
	maxReviewQuoteRunes   = 500
	maxReviewContextRunes = 48
	maxReviewContentRunes = 4000
)

var (
	// ErrInvalidReviewParam 表示审阅批注请求参数非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidReviewParam = errors.New("审阅批注请求参数非法")
)

type ReviewCommentVO struct {
	ID                 uint                       `json:"id"`
	DocumentID         uint                       `json:"document_id"`
	CommentNo          int                        `json:"comment_no"`
	BaseRevisionNo     int                        `json:"base_revision_no"`
	AuthorID           uint                       `json:"author_id"`
	OriginRole         string                     `json:"origin_role"`
	ScopeType          string                     `json:"scope_type"`
	SlotID             string                     `json:"slot_id"`
	Anchor             models.ReviewCommentAnchor `json:"anchor"`
	Category           string                     `json:"category"`
	Content            string                     `json:"content"`
	PersistForRegen    bool                       `json:"persist_for_regen"`
	Status             string                     `json:"status"`
	ProposalGroupMsgID *uint                      `json:"proposal_group_msg_id,omitempty"`
	ResolvedRevisionID *uint                      `json:"resolved_revision_id,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

type CreateReviewCommentRequest struct {
	BaseRevisionNo  int                         `json:"base_revision_no"`
	ScopeType       string                      `json:"scope_type"`
	SlotID          string                      `json:"slot_id"`
	Anchor          *models.ReviewCommentAnchor `json:"anchor"`
	Category        string                      `json:"category"`
	Content         string                      `json:"content"`
	PersistForRegen bool                        `json:"persist_for_regen"`
}

type UpdateReviewCommentRequest struct {
	Content         *string `json:"content"`
	Category        *string `json:"category"`
	PersistForRegen *bool   `json:"persist_for_regen"`
	Status          *string `json:"status"`
	ScopeType       *string `json:"scope_type"`
	SlotID          *string `json:"slot_id"`
}

// ReviewCommentService 编排 Canvas Studio 审阅批注篮 CRUD 与文档内单调编号分配。
type ReviewCommentService struct {
	reviewRepo ReviewCommentRepo
	docRepo    DocumentRepo
}

func NewReviewCommentService(reviewRepo ReviewCommentRepo, docRepo DocumentRepo) *ReviewCommentService {
	return &ReviewCommentService{
		reviewRepo: reviewRepo,
		docRepo:    docRepo,
	}
}

func (s *ReviewCommentService) ListComments(
	ctx context.Context,
	docID uint,
	filter models.ReviewCommentFilter,
) ([]ReviewCommentVO, error) {
	if _, err := s.docRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}
	if filter.Status != "" && !models.IsValidReviewCommentStatus(filter.Status) {
		return nil, fmt.Errorf("%w: 非法 status %q", ErrInvalidReviewParam, filter.Status)
	}
	if filter.ScopeType != "" && !models.IsValidReviewScopeType(filter.ScopeType) {
		return nil, fmt.Errorf("%w: 非法 scope_type %q", ErrInvalidReviewParam, filter.ScopeType)
	}

	rows, err := s.reviewRepo.ListByDocument(ctx, docID, filter)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewCommentVO, 0, len(rows))
	for i := range rows {
		out = append(out, toReviewCommentVO(&rows[i]))
	}
	return out, nil
}

func (s *ReviewCommentService) CreateComment(
	ctx context.Context,
	docID uint,
	req CreateReviewCommentRequest,
	authorID uint,
) (*ReviewCommentVO, error) {
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, fmt.Errorf("%w: content 不能为空", ErrInvalidReviewParam)
	}
	if utf8.RuneCountInString(content) > maxReviewContentRunes {
		return nil, fmt.Errorf("%w: content 长度不能超过 %d 字符", ErrInvalidReviewParam, maxReviewContentRunes)
	}

	scopeType := strings.TrimSpace(req.ScopeType)
	if scopeType == "" {
		if strings.TrimSpace(req.SlotID) == "" {
			scopeType = models.ReviewScopeGlobal
		} else if req.Anchor != nil && (strings.TrimSpace(req.Anchor.Quote) != "" || req.Anchor.BlockType != "") {
			scopeType = models.ReviewScopeInline
		} else {
			scopeType = models.ReviewScopeSection
		}
	}
	if !models.IsValidReviewScopeType(scopeType) {
		return nil, fmt.Errorf("%w: scope_type 仅支持 inline、section 或 global", ErrInvalidReviewParam)
	}

	category := strings.TrimSpace(req.Category)
	if category == "" {
		if scopeType == models.ReviewScopeGlobal {
			category = models.ReviewCategoryGlobalRule
		} else {
			category = models.ReviewCategoryCorrection
		}
	}
	if !models.IsValidReviewCategory(category) {
		return nil, fmt.Errorf("%w: 非法 category %q", ErrInvalidReviewParam, category)
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo <= 0 {
		return nil, models.ErrNotFound
	}

	slotID := strings.TrimSpace(req.SlotID)
	if scopeType == models.ReviewScopeGlobal {
		slotID = ""
	} else {
		if slotID == "" {
			return nil, fmt.Errorf("%w: %s 批注必须指定 slot_id", ErrInvalidReviewParam, scopeType)
		}
		if err := s.validateSlotExists(ctx, doc.ID, doc.CurrentRevisionNo, slotID); err != nil {
			return nil, err
		}
	}

	anchor := models.ReviewCommentAnchor{}
	if req.Anchor != nil && scopeType != models.ReviewScopeGlobal {
		anchor = *req.Anchor
		anchor.Quote = strings.TrimSpace(anchor.Quote)
		if utf8.RuneCountInString(anchor.Quote) > maxReviewQuoteRunes {
			return nil, fmt.Errorf("%w: anchor.quote 长度不能超过 %d 字符", ErrInvalidReviewParam, maxReviewQuoteRunes)
		}
		anchor.Prefix = truncateRunes(anchor.Prefix, maxReviewContextRunes)
		anchor.Suffix = truncateRunes(anchor.Suffix, maxReviewContextRunes)
		anchor.BlockType = strings.TrimSpace(anchor.BlockType)
	}
	anchorBytes, _ := json.Marshal(anchor)

	baseRevNo := req.BaseRevisionNo
	if baseRevNo <= 0 {
		baseRevNo = doc.CurrentRevisionNo
	}

	rc := &models.ReviewComment{
		DocumentID:      doc.ID,
		BaseRevisionNo:  baseRevNo,
		AuthorID:        authorID,
		OriginRole:      models.ReviewOriginOwner,
		ScopeType:       scopeType,
		SlotID:          slotID,
		AnchorJSON:      datatypes.JSON(anchorBytes),
		Category:        category,
		Content:         content,
		PersistForRegen: req.PersistForRegen,
		Status:          models.ReviewStatusOpen,
	}
	if err := s.reviewRepo.Create(ctx, rc); err != nil {
		return nil, err
	}

	vo := toReviewCommentVO(rc)
	return &vo, nil
}

func (s *ReviewCommentService) UpdateComment(
	ctx context.Context,
	docID, commentID uint,
	req UpdateReviewCommentRequest,
) (*ReviewCommentVO, error) {
	rc, err := s.reviewRepo.GetByID(ctx, docID, commentID)
	if err != nil {
		return nil, err
	}

	if req.Content != nil {
		content := strings.TrimSpace(*req.Content)
		if content == "" {
			return nil, fmt.Errorf("%w: content 不能为空", ErrInvalidReviewParam)
		}
		if utf8.RuneCountInString(content) > maxReviewContentRunes {
			return nil, fmt.Errorf("%w: content 长度不能超过 %d 字符", ErrInvalidReviewParam, maxReviewContentRunes)
		}
		rc.Content = content
	}
	if req.Category != nil {
		cat := strings.TrimSpace(*req.Category)
		if !models.IsValidReviewCategory(cat) {
			return nil, fmt.Errorf("%w: 非法 category %q", ErrInvalidReviewParam, cat)
		}
		rc.Category = cat
	}
	if req.PersistForRegen != nil {
		rc.PersistForRegen = *req.PersistForRegen
	}
	if req.ScopeType != nil {
		st := strings.TrimSpace(*req.ScopeType)
		if !models.IsValidReviewScopeType(st) {
			return nil, fmt.Errorf("%w: 非法 scope_type %q", ErrInvalidReviewParam, st)
		}
		rc.ScopeType = st
		if st == models.ReviewScopeGlobal {
			rc.SlotID = ""
			rc.AnchorJSON = datatypes.JSON(`{}`)
		}
	}
	if req.SlotID != nil && rc.ScopeType != models.ReviewScopeGlobal {
		sid := strings.TrimSpace(*req.SlotID)
		if sid == "" {
			return nil, fmt.Errorf("%w: 非全局批注 slot_id 不能为空", ErrInvalidReviewParam)
		}
		doc, dErr := s.docRepo.GetByID(ctx, docID)
		if dErr != nil {
			return nil, dErr
		}
		if err := s.validateSlotExists(ctx, doc.ID, doc.CurrentRevisionNo, sid); err != nil {
			return nil, err
		}
		rc.SlotID = sid
	}
	if req.Status != nil {
		nextStatus := strings.TrimSpace(*req.Status)
		if nextStatus != models.ReviewStatusOpen &&
			nextStatus != models.ReviewStatusResolved &&
			nextStatus != models.ReviewStatusDismissed {
			return nil, fmt.Errorf("%w: 仅支持手动流转为 open、resolved 或 dismissed", ErrInvalidReviewParam)
		}
		rc.Status = nextStatus
		if nextStatus == models.ReviewStatusOpen {
			rc.ProposalGroupMsgID = nil
		}
	}

	if err := s.reviewRepo.Update(ctx, rc); err != nil {
		return nil, err
	}
	vo := toReviewCommentVO(rc)
	return &vo, nil
}

func (s *ReviewCommentService) DeleteComment(ctx context.Context, docID, commentID uint) error {
	return s.reviewRepo.DeleteDraft(ctx, docID, commentID)
}

func (s *ReviewCommentService) validateSlotExists(ctx context.Context, docID uint, revNo int, slotID string) error {
	rev, _, err := s.docRepo.GetRevisionWithSections(ctx, docID, revNo)
	if err != nil {
		return err
	}
	snaps := unmarshalSectionSnapshots(rev.SectionsJSON)
	for _, sn := range snaps {
		if sn.SlotID == slotID {
			return nil
		}
	}
	return fmt.Errorf("%w: 目标章节槽位 %q 不存在", ErrInvalidReviewParam, slotID)
}

func toReviewCommentVO(rc *models.ReviewComment) ReviewCommentVO {
	var anchor models.ReviewCommentAnchor
	if len(rc.AnchorJSON) > 2 {
		_ = json.Unmarshal(rc.AnchorJSON, &anchor)
	}
	return ReviewCommentVO{
		ID:                 rc.ID,
		DocumentID:         rc.DocumentID,
		CommentNo:          rc.CommentNo,
		BaseRevisionNo:     rc.BaseRevisionNo,
		AuthorID:           rc.AuthorID,
		OriginRole:         rc.OriginRole,
		ScopeType:          rc.ScopeType,
		SlotID:             rc.SlotID,
		Anchor:             anchor,
		Category:           rc.Category,
		Content:            rc.Content,
		PersistForRegen:    rc.PersistForRegen,
		Status:             rc.Status,
		ProposalGroupMsgID: rc.ProposalGroupMsgID,
		ResolvedRevisionID: rc.ResolvedRevisionID,
		CreatedAt:          rc.CreatedAt,
		UpdatedAt:          rc.UpdatedAt,
	}
}
