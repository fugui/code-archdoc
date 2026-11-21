package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/drift"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/security"
	"code-archdoc/pkg/verifier"

	"gorm.io/datatypes"
)

const globalChatSlotID = "__global__"

var (
	// ErrInvalidChatParam 表示 Chat 精修入参非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidChatParam = errors.New("Chat 精修请求参数非法")
	// ErrSlotLocked 表示目标槽位已被人工锁定，拒绝 AI 精修（HTTP 409 CONFLICT）。
	ErrSlotLocked = errors.New("目标章节已锁定，请先解锁或改用手工直编")
	// ErrProposalNotPending 表示目标提案已处置或已过期，不可重复操作（HTTP 409 CONFLICT）。
	ErrProposalNotPending = errors.New("该修订提案当前不处于待审阅状态")
	// ErrChatLLMNotConfigured 表示未配置可用的 LLM 端点（HTTP 400 INVALID_PARAM）。
	ErrChatLLMNotConfigured = errors.New("尚未配置可用的 LLM 推理模型，请先由管理员在 LLM 模型配置中完成设置")
)

// ─── 请求与响应 DTO ───

type ChatRefineRequest struct {
	SessionID      *uint                       `json:"session_id"`
	Mode           string                      `json:"mode"`       // revise (默认/兼容 refine) | ask
	ScopeType      string                      `json:"scope_type"` // slot (默认) | inline | global | review_batch
	TargetSlotID   string                      `json:"target_slot_id"`
	BaseRevisionNo int                         `json:"base_revision_no"`
	Prompt         string                      `json:"prompt"`
	CommentIDs     []uint                      `json:"comment_ids"`
	InlineAnchor   *models.ReviewCommentAnchor `json:"inline_anchor"`
}

type AcceptChatProposalRequest struct {
	BaseRevisionNo int `json:"base_revision_no"`
}

type ApplyProposalItemSelection struct {
	ItemID           uint   `json:"item_id"`
	SlotID           string `json:"slot_id"`
	OverrideMarkdown string `json:"override_markdown"`
}

type ApplyChatProposalPayload struct {
	BaseRevisionNo int                          `json:"base_revision_no"`
	ChangeSummary  string                       `json:"change_summary"`
	Items          []ApplyProposalItemSelection `json:"items"`
}

type RejectProposalItemResultVO struct {
	ItemID                uint   `json:"item_id"`
	MessageID             uint   `json:"message_id"`
	ItemStatus            string `json:"item_status"`
	MessageProposalStatus string `json:"message_proposal_status"`
}

type ChatVerifierIssueVO struct {
	Kind     string `json:"kind"` // unverified_ref | mermaid | secret | missing_slot
	Severity string `json:"severity"`
	Message  string `json:"message"`
	SlotID   string `json:"slot_id,omitempty"`
}

type ChatVerifierPreviewVO struct {
	CanApprove   bool                  `json:"can_approve"`
	BlockerCount int                   `json:"blocker_count"`
	ErrorCount   int                   `json:"error_count"`
	WarningCount int                   `json:"warning_count"`
	Issues       []ChatVerifierIssueVO `json:"issues"`
}

type ChatPlanTargetSlotVO struct {
	SlotID     string `json:"slot_id"`
	Title      string `json:"title"`
	CommentIDs []uint `json:"comment_ids,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ChatPlanLockedAdviceVO struct {
	SlotID     string `json:"slot_id"`
	Title      string `json:"title"`
	CommentIDs []uint `json:"comment_ids,omitempty"`
	Advice     string `json:"advice"`
}

type ChatPlanSummaryVO struct {
	Intent             string                   `json:"intent,omitempty"`
	Summary            string                   `json:"summary,omitempty"`
	TargetSlots        []ChatPlanTargetSlotVO   `json:"target_slots,omitempty"`
	SkippedLockedSlots []ChatPlanLockedAdviceVO `json:"skipped_locked_slots,omitempty"`
	TruncatedSlots     []string                 `json:"truncated_slots,omitempty"`
}

type ChatContextRefVO struct {
	ScopeType    string                      `json:"scope_type,omitempty"`
	SlotID       string                      `json:"slot_id,omitempty"`
	CommentIDs   []uint                      `json:"comment_ids,omitempty"`
	InlineAnchor *models.ReviewCommentAnchor `json:"inline_anchor,omitempty"`
}

type ChatProposalItemVO struct {
	ItemID              uint                    `json:"item_id"`
	MessageID           uint                    `json:"message_id"`
	SlotID              string                  `json:"slot_id"`
	SlotTitle           string                  `json:"slot_title,omitempty"`
	BaseRevisionNo      int                     `json:"base_revision_no"`
	BeforeMD            string                  `json:"before_md"`
	AfterMD             string                  `json:"after_md"`
	FinalAppliedMD      string                  `json:"final_applied_md,omitempty"`
	UnifiedDiff         string                  `json:"unified_diff"`
	CitedAnchors        []models.EvidenceAnchor `json:"cited_anchors"`
	VerifierPreview     *ChatVerifierPreviewVO  `json:"verifier_preview,omitempty"`
	AddressedCommentIDs []uint                  `json:"addressed_comment_ids"`
	Status              string                  `json:"status"`
	AppliedRevisionID   *uint                   `json:"applied_revision_id,omitempty"`
}

type ChatMessageVO struct {
	ID                uint                   `json:"id"`
	SessionID         uint                   `json:"session_id"`
	Role              string                 `json:"role"`
	Intent            string                 `json:"intent,omitempty"`
	ScopeType         string                 `json:"scope_type,omitempty"`
	Content           string                 `json:"content"`
	TargetSlotID      string                 `json:"target_slot_id"`
	BaseRevisionNo    int                    `json:"base_revision_no"`
	ProposedBeforeMD  string                 `json:"proposed_before_md"`
	ProposedAfterMD   string                 `json:"proposed_after_md"`
	UnifiedDiff       string                 `json:"unified_diff"`
	VerifierPreview   *ChatVerifierPreviewVO `json:"verifier_preview,omitempty"`
	PlanSummary       *ChatPlanSummaryVO     `json:"plan_summary,omitempty"`
	ContextRefs       []ChatContextRefVO     `json:"context_refs,omitempty"`
	ProposalItems     []ChatProposalItemVO   `json:"proposal_items,omitempty"`
	ProposalStatus    string                 `json:"proposal_status"`
	AppliedRevisionID *uint                  `json:"applied_revision_id,omitempty"`
	ModelTier         string                 `json:"model_tier"`
	CreatedAt         time.Time              `json:"created_at"`
}

type ChatSessionWithMessagesVO struct {
	ID           uint            `json:"id"`
	DocumentID   uint            `json:"document_id"`
	TargetSlotID string          `json:"target_slot_id"`
	Title        string          `json:"title"`
	CreatedBy    uint            `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Messages     []ChatMessageVO `json:"messages"`
}

type ChatRejectResultVO struct {
	MessageID      uint   `json:"message_id"`
	ProposalStatus string `json:"proposal_status"`
}

type ChatProposalAuditSnapshot struct {
	DocumentID     uint   `json:"document_id"`
	MessageID      uint   `json:"message_id"`
	SlotID         string `json:"slot_id"`
	BaseRevisionNo int    `json:"base_revision_no"`
	NewRevisionNo  int    `json:"new_revision_no"`
	BeforeHash     string `json:"before_hash"`
	AfterHash      string `json:"after_hash"`
}

// PreparedChatRefine 保存 SSE 流启动前校验完毕并准备好的全部上下文。
type PreparedChatRefine struct {
	Mode             string // revise | ask
	ScopeType        string // slot | inline | global | review_batch
	Doc              *models.Document
	CurrentRev       *models.DocumentRevision
	Sections         []models.DocumentSectionSnapshot
	SectionEvs       []models.SectionEvidence
	TargetIdx        int
	TargetSlot       doctype.SlotSpec
	SlotSpecMap      map[string]doctype.SlotSpec
	DocTypeBundle    *doctype.AssetBundle
	IR               *codeir.CodeIRBundle
	ModuleContext    *codeir.ModuleContextFacet
	ModuleDigests    []codeir.ModuleDigestInput
	CrossModuleGraph []codeir.CrossModuleEdge
	ReviewComments   []models.ReviewComment
	CommentIDs       []uint
	InlineAnchor     *models.ReviewCommentAnchor
	History          []llm.Message
	UserPrompt       string
	Session          *models.ChatSession
	AssistantMsg     *models.ChatMessage
	ModelTier        string
	Endpoint         llm.EndpointConfig
	PromptMessages   []llm.Message
	PromptNonce      string
	RepoRoot         string
	ScopeRoot        string
	EnableTools      bool
	OperatorID       uint
}

// ChatService 编排 Canvas Studio SSE Chat Ask 问答、单/多槽位流式协同精修、审阅批注批处理、确定性 Verifier 预览与采纳/拒绝状态机。
type ChatService struct {
	chatRepo    ChatRepo
	docRepo     DocumentRepo
	docTypeRepo DocTypeRepo
	docTypeSvc  *DocTypeService
	govRepo     GovernanceRepo
	revSvc      *RevisionService
	snapSvc     *SnapshotService
	previewSvc  *SourcePreviewService
	reviewRepo  ReviewCommentRepo
	moduleRepo  ModuleRepo
	masterRepo  SharedMasterRepo
	cipher      *security.Cipher
	llmClient   *llm.Client
	verifier    *verifier.Verifier
	redactor    *security.SecretRedactor
}

func NewChatService(
	chatRepo ChatRepo,
	docRepo DocumentRepo,
	docTypeRepo DocTypeRepo,
	docTypeSvc *DocTypeService,
	govRepo GovernanceRepo,
	revSvc *RevisionService,
	snapSvc *SnapshotService,
	previewSvc *SourcePreviewService,
	cipher *security.Cipher,
	llmClient *llm.Client,
) *ChatService {
	if llmClient == nil {
		llmClient = llm.NewClient()
	}
	return &ChatService{
		chatRepo:    chatRepo,
		docRepo:     docRepo,
		docTypeRepo: docTypeRepo,
		docTypeSvc:  docTypeSvc,
		govRepo:     govRepo,
		revSvc:      revSvc,
		snapSvc:     snapSvc,
		previewSvc:  previewSvc,
		cipher:      cipher,
		llmClient:   llmClient,
		verifier:    verifier.New(),
		redactor:    security.NewSecretRedactor(),
	}
}

// WithReviewCommentRepo 注入审阅批注仓储以支持批注篮批处理与状态闭环联动。
func (s *ChatService) WithReviewCommentRepo(reviewRepo ReviewCommentRepo) *ChatService {
	s.reviewRepo = reviewRepo
	return s
}

// WithModuleContext 注入模块索引与共享主数据仓储以对齐 ADR-04 整仓模式摘要与白名单验证。
func (s *ChatService) WithModuleContext(moduleRepo ModuleRepo, masterRepo SharedMasterRepo) *ChatService {
	s.moduleRepo = moduleRepo
	s.masterRepo = masterRepo
	return s
}

// ListSessions 获取文档精修会话列表与完整消息/提案项历史（按 updated_at DESC 排序）。
func (s *ChatService) ListSessions(ctx context.Context, docID uint) ([]ChatSessionWithMessagesVO, error) {
	if _, err := s.docRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}
	sessions, msgMap, err := s.chatRepo.ListSessionsWithMessages(ctx, docID)
	if err != nil {
		return nil, err
	}

	var allMsgIDs []uint
	for _, list := range msgMap {
		for _, m := range list {
			allMsgIDs = append(allMsgIDs, m.ID)
		}
	}
	itemsByMsg, err := s.chatRepo.ListProposalItemsByMessageIDs(ctx, allMsgIDs)
	if err != nil {
		itemsByMsg = map[uint][]models.ChatProposalItem{}
	}

	out := make([]ChatSessionWithMessagesVO, 0, len(sessions))
	for _, sess := range sessions {
		rawMsgs := msgMap[sess.ID]
		msgs := make([]ChatMessageVO, 0, len(rawMsgs))
		for _, m := range rawMsgs {
			preview := parseVerifierPreviewJSON(m.VerifierPreviewJSON)
			var planSummary *ChatPlanSummaryVO
			if len(m.PlanSummaryJSON) > 2 {
				var ps ChatPlanSummaryVO
				if json.Unmarshal(m.PlanSummaryJSON, &ps) == nil && (len(ps.TargetSlots) > 0 || len(ps.SkippedLockedSlots) > 0 || ps.Summary != "") {
					planSummary = &ps
				}
			}
			var ctxRefs []ChatContextRefVO
			if len(m.ContextRefsJSON) > 2 {
				_ = json.Unmarshal(m.ContextRefsJSON, &ctxRefs)
			}

			rawItems := itemsByMsg[m.ID]
			itemVOs := make([]ChatProposalItemVO, 0, len(rawItems))
			for _, it := range rawItems {
				itemVOs = append(itemVOs, toChatProposalItemVO(it, ""))
			}
			// 兼容存量未拆表 ChatProposalItem 的旧版单槽位提案消息
			if len(itemVOs) == 0 && m.Role == "assistant" && m.ProposalStatus != models.ProposalStatusNone && strings.TrimSpace(m.ProposedAfterMD) != "" {
				itemVOs = append(itemVOs, ChatProposalItemVO{
					ItemID:              m.ID,
					MessageID:           m.ID,
					SlotID:              m.TargetSlotID,
					BaseRevisionNo:      m.BaseRevisionNo,
					BeforeMD:            m.ProposedBeforeMD,
					AfterMD:             m.ProposedAfterMD,
					UnifiedDiff:         m.UnifiedDiff,
					CitedAnchors:        []models.EvidenceAnchor{},
					VerifierPreview:     preview,
					AddressedCommentIDs: []uint{},
					Status:              m.ProposalStatus,
					AppliedRevisionID:   m.AppliedRevisionID,
				})
			}

			msgs = append(msgs, ChatMessageVO{
				ID:                m.ID,
				SessionID:         m.SessionID,
				Role:              m.Role,
				Intent:            m.Intent,
				ScopeType:         m.ScopeType,
				Content:           m.Content,
				TargetSlotID:      m.TargetSlotID,
				BaseRevisionNo:    m.BaseRevisionNo,
				ProposedBeforeMD:  m.ProposedBeforeMD,
				ProposedAfterMD:   m.ProposedAfterMD,
				UnifiedDiff:       m.UnifiedDiff,
				VerifierPreview:   preview,
				PlanSummary:       planSummary,
				ContextRefs:       ctxRefs,
				ProposalItems:     itemVOs,
				ProposalStatus:    m.ProposalStatus,
				AppliedRevisionID: m.AppliedRevisionID,
				ModelTier:         m.ModelTier,
				CreatedAt:         m.CreatedAt,
			})
		}
		out = append(out, ChatSessionWithMessagesVO{
			ID:           sess.ID,
			DocumentID:   sess.DocumentID,
			TargetSlotID: sess.TargetSlotID,
			Title:        sess.Title,
			CreatedBy:    sess.CreatedBy,
			CreatedAt:    sess.CreatedAt,
			UpdatedAt:    sess.UpdatedAt,
			Messages:     msgs,
		})
	}
	return out, nil
}

// PrepareRefine 在写出 SSE 响应头之前完成全部同步前置校验、锁定拦截、上下文装配与会话持久化。
func (s *ChatService) PrepareRefine(
	ctx context.Context,
	docID uint,
	req ChatRefineRequest,
	operatorID uint,
) (*PreparedChatRefine, error) {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" || mode == "refine" {
		mode = models.ChatModeReviseCompat()
	}
	if mode != models.ChatModeAsk && mode != "revise" {
		return nil, fmt.Errorf("%w: mode 必须为 revise 或 ask", ErrInvalidChatParam)
	}

	cleanCommentIDs := dedupeUintSlice(req.CommentIDs)
	scopeType := strings.TrimSpace(req.ScopeType)
	if scopeType == "" {
		switch {
		case len(cleanCommentIDs) > 0:
			scopeType = models.ChatScopeReviewBatch
		case req.InlineAnchor != nil:
			scopeType = models.ChatScopeInline
		case strings.TrimSpace(req.TargetSlotID) == "" && mode == models.ChatModeAsk:
			scopeType = models.ChatScopeGlobal
		default:
			scopeType = models.ChatScopeSlot
		}
	}
	switch scopeType {
	case models.ChatScopeSlot, models.ChatScopeInline, models.ChatScopeGlobal, models.ChatScopeReviewBatch:
	default:
		return nil, fmt.Errorf("%w: scope_type 非法", ErrInvalidChatParam)
	}

	promptText := strings.TrimSpace(req.Prompt)
	if scopeType == models.ChatScopeReviewBatch {
		if len(cleanCommentIDs) == 0 || len(cleanCommentIDs) > 20 {
			return nil, fmt.Errorf("%w: review_batch 模式需指定 1 至 20 条批注", ErrInvalidChatParam)
		}
		if promptText == "" {
			promptText = fmt.Sprintf("请依据选定的 %d 条审阅批注制定修订规划并完成对应章节精修。", len(cleanCommentIDs))
		}
	} else if promptText == "" {
		return nil, fmt.Errorf("%w: prompt 不能为空", ErrInvalidChatParam)
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo <= 0 {
		return nil, models.ErrNotFound
	}
	baseRevNo := req.BaseRevisionNo
	if baseRevNo <= 0 {
		baseRevNo = doc.CurrentRevisionNo
	}
	if baseRevNo != doc.CurrentRevisionNo {
		return nil, &OptimisticLockConflictError{CurrentRevisionNo: doc.CurrentRevisionNo}
	}

	curRev, prevEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, baseRevNo)
	if err != nil {
		return nil, err
	}
	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(curRev.SectionsJSON), prevEvs)
	if len(snaps) == 0 {
		return nil, models.ErrNotFound
	}

	// 加载批注（如指定 comment_ids）并强制校验归属当前文档
	var comments []models.ReviewComment
	if len(cleanCommentIDs) > 0 {
		if s.reviewRepo == nil {
			return nil, fmt.Errorf("%w: 审阅批注仓储未就绪", ErrInvalidChatParam)
		}
		comments, err = s.reviewRepo.ListByIDs(ctx, doc.ID, cleanCommentIDs)
		if err != nil {
			return nil, err
		}
		if len(comments) != len(cleanCommentIDs) {
			return nil, models.ErrNotFound
		}
	}

	var inlineCopy *models.ReviewCommentAnchor
	if req.InlineAnchor != nil {
		cp := *req.InlineAnchor
		cp.Quote = truncateRunes(cp.Quote, 500)
		inlineCopy = &cp
	}

	// 解析目标章节槽位（slot / inline 聚焦单章；global / review_batch 可不限单章）
	targetSlotID := strings.TrimSpace(req.TargetSlotID)
	targetIdx := -1
	if scopeType == models.ChatScopeSlot || scopeType == models.ChatScopeInline {
		if targetSlotID == "" {
			targetSlotID = snaps[0].SlotID
		}
		for i := range snaps {
			if snaps[i].SlotID == targetSlotID {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			return nil, models.ErrNotFound
		}
		// 仅精修写模式下拦截 locked 槽位；Ask 只读问答放行锁定章节
		if mode != models.ChatModeAsk && snaps[targetIdx].Ownership == models.OwnershipLocked {
			return nil, ErrSlotLocked
		}
	} else {
		if targetSlotID != "" {
			for i := range snaps {
				if snaps[i].SlotID == targetSlotID {
					targetIdx = i
					break
				}
			}
			if targetIdx < 0 {
				return nil, models.ErrNotFound
			}
		} else {
			targetSlotID = globalChatSlotID
		}
	}

	dtBundle, preferredTier := s.loadDocTypeBundle(ctx, doc.DocTypeSlug, curRev.DocTypeRevisionID)
	slotSpecMap := make(map[string]doctype.SlotSpec, len(snaps))
	for _, sn := range snaps {
		slotSpecMap[sn.SlotID] = doctype.SlotSpec{
			SlotID:      sn.SlotID,
			Title:       sn.Title,
			Ownership:   sn.Ownership,
			TokenBudget: 6000,
		}
	}
	if dtBundle != nil {
		for _, sl := range dtBundle.Meta.Slots {
			if existing, ok := slotSpecMap[sl.SlotID]; ok {
				sl.Ownership = existing.Ownership
			}
			slotSpecMap[sl.SlotID] = sl
		}
	}
	var targetSlot doctype.SlotSpec
	if targetIdx >= 0 {
		targetSlot = slotSpecMap[snaps[targetIdx].SlotID]
	} else {
		targetSlot = doctype.SlotSpec{SlotID: globalChatSlotID, Title: "全文档审阅"}
	}

	endpointCfg, resolvedTier, err := s.resolveEndpoint(ctx, preferredTier)
	if err != nil {
		return nil, err
	}

	// 加载快照 Code IR、ADR-04 module_context 以及整仓多模块摘要/跨模块依赖拓扑
	var (
		irBundle         *codeir.CodeIRBundle
		modCtx           *codeir.ModuleContextFacet
		moduleDigests    []codeir.ModuleDigestInput
		crossModuleGraph []codeir.CrossModuleEdge
	)
	if s.snapSvc != nil && curRev.SnapshotID > 0 {
		if snap, loc, ir, irErr := s.snapSvc.LoadIRBundleBySnapshotID(ctx, curRev.SnapshotID); irErr == nil {
			irBundle = ir
			modCtx = s.snapSvc.LoadModuleContext(ctx, snap, loc)
			if doc.Subdirectory == "" && s.moduleRepo != nil && dtBundle != nil && hasContextInput(dtBundle.Meta.ContextInputs, "module_digests") {
				glossary, _ := s.snapSvc.ReadCachedGlossary(ctx, snap, loc)
				digests, activeMods := buildModuleDigestsForRepo(ctx, s.moduleRepo, s.masterRepo, s.docRepo, doc.RepoID, doc.RefName, dtBundle, glossary)
				if len(digests) > 0 {
					moduleDigests = digests
					crossModuleGraph = codeir.BuildCrossModuleGraph(irBundle, activeMods)
				}
			}
		}
	}

	// ADR-04 整仓分片裁剪
	if dtBundle != nil && targetIdx >= 0 {
		targetSlot = filterSlotSpecForScope(targetSlot, doc.Subdirectory, dtBundle.Meta, len(moduleDigests) > 0)
	}

	// 准备会话及最近历史（最多 6 条）
	sessionSlotKey := targetSlotID
	sess, err := s.chatRepo.GetOrCreateSession(ctx, doc.ID, sessionSlotKey, operatorID, req.SessionID)
	if err != nil {
		return nil, err
	}
	var history []llm.Message
	if _, msgMap, listErr := s.chatRepo.ListSessionsWithMessages(ctx, doc.ID); listErr == nil {
		existingMsgs := msgMap[sess.ID]
		start := 0
		if len(existingMsgs) > 6 {
			start = len(existingMsgs) - 6
		}
		for _, m := range existingMsgs[start:] {
			if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Content) != "" {
				history = append(history, llm.Message{Role: m.Role, Content: m.Content})
			}
		}
	}

	intent := models.ChatIntentRevise
	if mode == models.ChatModeAsk {
		intent = models.ChatIntentAsk
	} else if scopeType == models.ChatScopeGlobal || scopeType == models.ChatScopeReviewBatch {
		intent = models.ChatIntentPlanRefine
	}

	ctxRefs, _ := json.Marshal([]ChatContextRefVO{{
		ScopeType:    scopeType,
		SlotID:       targetSlotID,
		CommentIDs:   cleanCommentIDs,
		InlineAnchor: inlineCopy,
	}})

	userMsg := &models.ChatMessage{
		SessionID:       sess.ID,
		Role:            "user",
		Intent:          intent,
		ScopeType:       scopeType,
		Content:         promptText,
		TargetSlotID:    targetSlotID,
		BaseRevisionNo:  baseRevNo,
		ContextRefsJSON: datatypes.JSON(ctxRefs),
		ProposalStatus:  models.ProposalStatusNone,
		ModelTier:       resolvedTier,
	}
	if err := s.chatRepo.CreateMessage(ctx, userMsg); err != nil {
		return nil, err
	}
	if sess.Title == "" {
		titleRunes := []rune(promptText)
		title := promptText
		if len(titleRunes) > 36 {
			title = string(titleRunes[:36]) + "..."
		}
		if touchErr := s.chatRepo.TouchSession(ctx, sess.ID, title); touchErr != nil {
			log.Printf("[Code-Archdoc] 更新 ChatSession(%d) 标题失败: %v", sess.ID, touchErr)
		}
		sess.Title = title
	} else if touchErr := s.chatRepo.TouchSession(ctx, sess.ID, ""); touchErr != nil {
		log.Printf("[Code-Archdoc] 更新 ChatSession(%d) 时间戳失败: %v", sess.ID, touchErr)
	}

	assistantMsg := &models.ChatMessage{
		SessionID:           sess.ID,
		Role:                "assistant",
		Intent:              intent,
		ScopeType:           scopeType,
		Content:             "",
		TargetSlotID:        targetSlotID,
		BaseRevisionNo:      baseRevNo,
		VerifierPreviewJSON: datatypes.JSON(`{}`),
		PlanSummaryJSON:     datatypes.JSON(`{}`),
		ContextRefsJSON:     datatypes.JSON(ctxRefs),
		ProposalStatus:      models.ProposalStatusNone,
		ModelTier:           resolvedTier,
	}
	if err := s.chatRepo.CreateMessage(ctx, assistantMsg); err != nil {
		return nil, err
	}

	var repoRoot, scopeRoot string
	hasFS := false
	if s.snapSvc != nil {
		repoRoot, scopeRoot, hasFS = s.snapSvc.ResolveSandboxScope(doc.RepoID, doc.Subdirectory)
	}
	enableTools := hasFS || irBundle != nil

	var promptMsgs []llm.Message
	var promptNonce string
	switch {
	case mode == models.ChatModeAsk:
		var slotPtr *doctype.SlotSpec
		var curMD string
		var evSnips map[string]string
		var allSec []llm.AskSectionSummary
		if targetIdx >= 0 {
			cp := targetSlot
			slotPtr = &cp
			curMD = snaps[targetIdx].Markdown
			evSnips = s.collectEvidenceSnippets(ctx, doc.ID, curRev.CommitSHA, snaps[targetIdx].SlotID, prevEvs)
		} else {
			allSec = make([]llm.AskSectionSummary, 0, len(snaps))
			for _, sn := range snaps {
				allSec = append(allSec, llm.AskSectionSummary{
					SlotID:    sn.SlotID,
					Title:     sn.Title,
					Ownership: sn.Ownership,
					Markdown:  sn.Markdown,
				})
			}
		}
		promptMsgs, promptNonce = llm.BuildChatAskMessages(llm.ChatAskPromptInput{
			DocType:           dtBundle,
			Slot:              slotPtr,
			CurrentMarkdown:   curMD,
			AllSections:       allSec,
			IR:                irBundle,
			ModuleContext:     modCtx,
			ModuleDigests:     moduleDigests,
			CrossModuleGraph:  crossModuleGraph,
			EvidenceSnippets:  evSnips,
			InlineAnchor:      toLLMInlineAnchor(inlineCopy),
			ReviewComments:    toLLMReviewComments(comments),
			History:           history,
			UserPrompt:        promptText,
			EnableSourceTools: enableTools,
		})
	case scopeType == models.ChatScopeSlot || scopeType == models.ChatScopeInline:
		evSnips := s.collectEvidenceSnippets(ctx, doc.ID, curRev.CommitSHA, snaps[targetIdx].SlotID, prevEvs)
		promptMsgs, promptNonce = llm.BuildChatRefineMessages(llm.ChatRefinePromptInput{
			DocType:           dtBundle,
			Slot:              targetSlot,
			CurrentMarkdown:   snaps[targetIdx].Markdown,
			IR:                irBundle,
			ModuleContext:     modCtx,
			ModuleDigests:     moduleDigests,
			CrossModuleGraph:  crossModuleGraph,
			EvidenceSnippets:  evSnips,
			InlineAnchor:      toLLMInlineAnchor(inlineCopy),
			ReviewComments:    toLLMReviewComments(comments),
			History:           history,
			UserPrompt:        promptText,
			EnableSourceTools: enableTools,
		})
	default:
		// global / review_batch: 预装配 Plan 阶段消息
		promptMsgs, promptNonce = llm.BuildReviewPlanMessages(llm.ReviewPlanPromptInput{
			DocType:    dtBundle,
			Sections:   buildPlanSectionSummaries(snaps),
			Comments:   buildPlanComments(comments),
			UserPrompt: promptText,
		})
	}

	return &PreparedChatRefine{
		Mode:             mode,
		ScopeType:        scopeType,
		Doc:              doc,
		CurrentRev:       curRev,
		Sections:         snaps,
		SectionEvs:       prevEvs,
		TargetIdx:        targetIdx,
		TargetSlot:       targetSlot,
		SlotSpecMap:      slotSpecMap,
		DocTypeBundle:    dtBundle,
		IR:               irBundle,
		ModuleContext:    modCtx,
		ModuleDigests:    moduleDigests,
		CrossModuleGraph: crossModuleGraph,
		ReviewComments:   comments,
		CommentIDs:       cleanCommentIDs,
		InlineAnchor:     inlineCopy,
		History:          history,
		UserPrompt:       promptText,
		Session:          sess,
		AssistantMsg:     assistantMsg,
		ModelTier:        resolvedTier,
		Endpoint:         endpointCfg,
		PromptMessages:   promptMsgs,
		PromptNonce:      promptNonce,
		RepoRoot:         repoRoot,
		ScopeRoot:        scopeRoot,
		EnableTools:      enableTools,
		OperatorID:       operatorID,
	}, nil
}

// ExecuteRefineStream 根据 Mode 与 ScopeType 分流三条执行路径：
// 1. Ask 只读问答（纯 Markdown 直出，不产生 ProposalItem）；
// 2. Refine 单章节/划词微修（StreamSplitFilter 双段过滤 + 联合 Verifier + 单 ProposalItem）；
// 3. Refine 全局/批量批注（Plan 规划守卫 -> 顺序孤立精修 -> 联合 Verifier -> 多槽位 ProposalItem 组）。
func (s *ChatService) ExecuteRefineStream(
	ctx context.Context,
	prep *PreparedChatRefine,
	emit func(event string, payload any) error,
) {
	var emitMu sync.Mutex
	safeEmit := func(event string, payload any) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return emit(event, payload)
	}

	if err := safeEmit("meta", map[string]any{
		"session_id":       prep.Session.ID,
		"message_id":       prep.AssistantMsg.ID,
		"slot_id":          prep.TargetSlot.SlotID,
		"mode":             prep.Mode,
		"scope_type":       prep.ScopeType,
		"base_revision_no": prep.CurrentRev.RevisionNo,
		"model_tier":       prep.ModelTier,
	}); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	// 获取只读沙箱源码工具（统一注入 ADR-04 跨模块外围白名单）
	var toolkit *llm.SandboxSourceToolkit
	if prep.EnableTools {
		toolkit = llm.NewSandboxSourceToolkit(llm.SandboxToolkitConfig{
			ScopeRoot:             prep.ScopeRoot,
			RepoRoot:              prep.RepoRoot,
			IR:                    prep.IR,
			ExternalFileWhitelist: extractExternalFileWhitelist(prep.ModuleContext),
			Redactor:              s.redactor,
		})
	}

	switch {
	case prep.Mode == models.ChatModeAsk:
		s.executeAskStream(ctx, prep, toolkit, safeEmit)
	case prep.ScopeType == models.ChatScopeSlot || prep.ScopeType == models.ChatScopeInline:
		s.executeSingleSlotRefineStream(ctx, prep, toolkit, safeEmit)
	default:
		s.executeBatchRefineStream(ctx, prep, toolkit, safeEmit)
	}
}

func (s *ChatService) invokeStreamWithOptionalTools(
	ctx context.Context,
	prep *PreparedChatRefine,
	toolkit *llm.SandboxSourceToolkit,
	slotID string,
	maxRounds int,
	msgs []llm.Message,
	callbacks llm.ToolLoopCallbacks,
) (*llm.ChatResponse, error) {
	if prep.EnableTools && toolkit != nil && toolkit.HasTools() {
		if maxRounds <= 0 {
			maxRounds = 4
		}
		res, err := llm.RunToolLoopStream(ctx, s.llmClient, prep.Endpoint, llm.ChatRequest{
			Messages: msgs,
			Stream:   true,
		}, toolkit, llm.ToolLoopOptions{
			Nonce:     prep.PromptNonce,
			MaxRounds: maxRounds,
		}, callbacks)
		if err != nil {
			return nil, err
		}
		return res.Response, nil
	}
	return s.llmClient.ChatCompletionStream(ctx, prep.Endpoint, llm.ChatRequest{
		Messages: msgs,
		Stream:   true,
	}, callbacks.OnDelta)
}

func (s *ChatService) executeAskStream(
	ctx context.Context,
	prep *PreparedChatRefine,
	toolkit *llm.SandboxSourceToolkit,
	safeEmit func(string, any) error,
) {
	if err := safeEmit("stage", map[string]any{
		"stage":   "refining_slot",
		"slot_id": prep.TargetSlot.SlotID,
	}); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	if prep.EnableTools && toolkit != nil && toolkit.HasTools() && s.snapSvc != nil {
		releaseLease := s.snapSvc.AcquireRepoLease(prep.Doc.RepoID)
		defer releaseLease()
	}

	start := time.Now()
	resp, err := s.invokeStreamWithOptionalTools(
		ctx, prep, toolkit, prep.TargetSlot.SlotID, prep.TargetSlot.MaxToolRounds, prep.PromptMessages,
		llm.ToolLoopCallbacks{
			OnDelta: func(delta string) error {
				return safeEmit("delta", map[string]any{"text": delta, "slot_id": prep.TargetSlot.SlotID})
			},
			OnToolEvent: func(ev llm.ToolTraceEvent) error {
				return safeEmit("tool_call", ev)
			},
		},
	)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		s.handleStreamError(ctx, prep, err, latencyMs, safeEmit)
		return
	}
	if ctx.Err() != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}

	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		s.failEmptyProposal(prep, safeEmit, "模型未生成有效回答，请重试")
		return
	}

	prep.AssistantMsg.Content = answer
	prep.AssistantMsg.ProposalStatus = models.ProposalStatusNone
	if err := s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg); err != nil {
		_ = safeEmit("error", map[string]any{"code": "PERSIST_FAILED", "message": "保存问答消息失败"})
		return
	}

	usage := resp.Usage
	if usage.LatencyMs <= 0 {
		usage.LatencyMs = latencyMs
	}
	s.recordUsageLog(ctx, prep, prep.TargetSlot.SlotID, "chat_ask", &usage, "success", "")
	_ = safeEmit("done", map[string]any{
		"message_id":           prep.AssistantMsg.ID,
		"prompt_tokens":        usage.PromptTokens,
		"cached_prompt_tokens": usage.CachedPromptTokens,
		"completion_tokens":    usage.CompletionTokens,
		"latency_ms":           usage.LatencyMs,
	})
}

func (s *ChatService) executeSingleSlotRefineStream(
	ctx context.Context,
	prep *PreparedChatRefine,
	toolkit *llm.SandboxSourceToolkit,
	safeEmit func(string, any) error,
) {
	if err := safeEmit("stage", map[string]any{
		"stage":   "refining_slot",
		"slot_id": prep.TargetSlot.SlotID,
		"index":   1,
		"total":   1,
	}); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	if prep.EnableTools && toolkit != nil && toolkit.HasTools() && s.snapSvc != nil {
		releaseLease := s.snapSvc.AcquireRepoLease(prep.Doc.RepoID)
		defer releaseLease()
	}

	filter := llm.NewStreamSplitFilter(
		func(d string) error {
			return safeEmit("delta", map[string]any{"text": d, "slot_id": prep.TargetSlot.SlotID})
		},
		func() {
			_ = safeEmit("stage", map[string]any{"stage": "drafting_diff", "slot_id": prep.TargetSlot.SlotID})
		},
	)

	start := time.Now()
	resp, err := s.invokeStreamWithOptionalTools(
		ctx, prep, toolkit, prep.TargetSlot.SlotID, prep.TargetSlot.MaxToolRounds, prep.PromptMessages,
		llm.ToolLoopCallbacks{
			OnTurnStart: func(turn int) {
				if turn > 1 {
					filter.ResetTurn()
				}
			},
			OnDelta: filter.Push,
			OnToolEvent: func(ev llm.ToolTraceEvent) error {
				return safeEmit("tool_call", ev)
			},
		},
	)
	if err == nil {
		err = filter.Flush()
	}
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		s.handleStreamError(ctx, prep, err, latencyMs, safeEmit)
		return
	}
	if ctx.Err() != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}

	replyText, afterMD, citedCandidates := llm.ParseChatRefineOutput(resp.Content)
	if strings.TrimSpace(afterMD) == "" {
		s.failEmptyProposal(prep, safeEmit, "模型未生成有效的章节修订内容，请重试")
		return
	}
	if !filter.HasEmittedDelta() && strings.TrimSpace(replyText) != "" {
		_ = safeEmit("delta", map[string]any{"text": replyText, "slot_id": prep.TargetSlot.SlotID})
	}

	_ = safeEmit("stage", map[string]any{"stage": "verifying", "slot_id": prep.TargetSlot.SlotID})

	beforeMD := prep.Sections[prep.TargetIdx].Markdown
	unifiedDiff, _, _ := drift.ComputeUnifiedDiff(beforeMD, afterMD)

	changedMap := map[string]string{prep.TargetSlot.SlotID: afterMD}
	citedMap := map[string][]verifier.CitedAnchorCandidate{prep.TargetSlot.SlotID: citedCandidates}
	verifyRes := s.runJointVerifier(prep, changedMap, citedMap)

	alignedAnchors := convertVerifierAnchors(verifyRes.SectionEvidences[prep.TargetSlot.SlotID])
	persistedAnchors := alignedAnchors
	if len(persistedAnchors) == 0 && len(citedCandidates) > 0 {
		persistedAnchors = candidatesToEvidenceAnchors(citedCandidates)
	}
	if persistedAnchors == nil {
		persistedAnchors = []models.EvidenceAnchor{}
	}
	previewVO := buildChatVerifierPreview(verifyRes.Report)
	previewBytes, _ := json.Marshal(previewVO)

	citedBytes, _ := json.Marshal(persistedAnchors)
	addrBytes, _ := json.Marshal(prep.CommentIDs)
	propItem := &models.ChatProposalItem{
		MessageID:               prep.AssistantMsg.ID,
		DocumentID:              prep.Doc.ID,
		SlotID:                  prep.TargetSlot.SlotID,
		BaseRevisionNo:          prep.CurrentRev.RevisionNo,
		ProposedBeforeMD:        beforeMD,
		ProposedAfterMD:         afterMD,
		UnifiedDiff:             unifiedDiff,
		CitedAnchorsJSON:        datatypes.JSON(citedBytes),
		VerifierPreviewJSON:     datatypes.JSON(previewBytes),
		AddressedCommentIDsJSON: datatypes.JSON(addrBytes),
		Status:                  models.ProposalStatusPending,
	}
	if err := s.chatRepo.CreateProposalItem(ctx, propItem); err != nil {
		_ = safeEmit("error", map[string]any{"code": "PERSIST_FAILED", "message": "保存精修提案项失败"})
		return
	}

	prep.AssistantMsg.Content = replyText
	prep.AssistantMsg.ProposedBeforeMD = beforeMD
	prep.AssistantMsg.ProposedAfterMD = afterMD
	prep.AssistantMsg.UnifiedDiff = unifiedDiff
	prep.AssistantMsg.VerifierPreviewJSON = datatypes.JSON(previewBytes)
	prep.AssistantMsg.ProposalStatus = models.ProposalStatusPending
	if err := s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg); err != nil {
		_ = safeEmit("error", map[string]any{"code": "PERSIST_FAILED", "message": "保存精修提案失败"})
		return
	}
	if s.reviewRepo != nil && len(prep.CommentIDs) > 0 {
		if mErr := s.reviewRepo.MarkProposed(ctx, prep.Doc.ID, prep.CommentIDs, prep.AssistantMsg.ID); mErr != nil {
			log.Printf("[Code-Archdoc] 标记批注为 proposed 失败: %v", mErr)
		}
	}

	usage := resp.Usage
	if usage.LatencyMs <= 0 {
		usage.LatencyMs = latencyMs
	}
	s.recordUsageLog(ctx, prep, prep.TargetSlot.SlotID, "chat_refine", &usage, "success", "")

	itemVO := toChatProposalItemVO(*propItem, prep.TargetSlot.Title)
	itemVO.CitedAnchors = alignedAnchors
	itemVO.VerifierPreview = &previewVO

	if err := safeEmit("proposal_item", itemVO); err != nil {
		return
	}
	if err := safeEmit("diff_proposal", map[string]any{
		"proposal_id":   prep.AssistantMsg.ID,
		"slot_id":       prep.TargetSlot.SlotID,
		"before_md":     beforeMD,
		"after_md":      afterMD,
		"unified_diff":  unifiedDiff,
		"cited_anchors": alignedAnchors,
	}); err != nil {
		return
	}
	if err := safeEmit("verifier_preview", previewVO); err != nil {
		return
	}
	_ = safeEmit("done", map[string]any{
		"message_id":           prep.AssistantMsg.ID,
		"prompt_tokens":        usage.PromptTokens,
		"cached_prompt_tokens": usage.CachedPromptTokens,
		"completion_tokens":    usage.CompletionTokens,
		"latency_ms":           usage.LatencyMs,
	})
}

func (s *ChatService) executeBatchRefineStream(
	ctx context.Context,
	prep *PreparedChatRefine,
	toolkit *llm.SandboxSourceToolkit,
	safeEmit func(string, any) error,
) {
	if err := safeEmit("stage", map[string]any{
		"stage": "planning",
	}); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	start := time.Now()
	planResp, err := s.llmClient.ChatCompletionStream(ctx, prep.Endpoint, llm.ChatRequest{
		Messages: prep.PromptMessages,
		Stream:   false,
	}, nil)
	if err != nil {
		s.handleStreamError(ctx, prep, err, time.Since(start).Milliseconds(), safeEmit)
		return
	}
	if ctx.Err() != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}

	plan := llm.ParseAndGuardReviewPlan(
		planResp.Content,
		buildPlanSectionSummaries(prep.Sections),
		buildPlanComments(prep.ReviewComments),
	)
	planVO := buildChatPlanSummaryVO(plan, prep.Sections)
	planBytes, _ := json.Marshal(planVO)
	prep.AssistantMsg.PlanSummaryJSON = datatypes.JSON(planBytes)
	_ = s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg)

	if err := safeEmit("plan", planVO); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	totalUsage := planResp.Usage

	// 若规划结果全部命中锁定章节（无可编辑目标槽位）：优雅输出只读建议与总结，不创建 ProposalItem，也不流转批注
	if len(plan.TargetSlots) == 0 {
		advisoryMD := buildAdvisoryOnlyReply(planVO)
		_ = safeEmit("delta", map[string]any{"text": advisoryMD})
		prep.AssistantMsg.Content = advisoryMD
		prep.AssistantMsg.ProposalStatus = models.ProposalStatusNone
		_ = s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg)

		totalUsage.LatencyMs = time.Since(start).Milliseconds()
		s.recordUsageLog(ctx, prep, globalChatSlotID, "chat_plan_refine", &totalUsage, "success", "")
		_ = safeEmit("done", map[string]any{
			"message_id":           prep.AssistantMsg.ID,
			"prompt_tokens":        totalUsage.PromptTokens,
			"cached_prompt_tokens": totalUsage.CachedPromptTokens,
			"completion_tokens":    totalUsage.CompletionTokens,
			"latency_ms":           totalUsage.LatencyMs,
		})
		return
	}

	// 单次全生命周期持有 1 个沙箱租约，按规划顺序孤立精修各目标章节
	if prep.EnableTools && toolkit != nil && toolkit.HasTools() && s.snapSvc != nil {
		releaseLease := s.snapSvc.AcquireRepoLease(prep.Doc.RepoID)
		defer releaseLease()
	}

	secBySlot := make(map[string]models.DocumentSectionSnapshot, len(prep.Sections))
	for _, sn := range prep.Sections {
		secBySlot[sn.SlotID] = sn
	}
	commentByID := make(map[uint]models.ReviewComment, len(prep.ReviewComments))
	for _, cm := range prep.ReviewComments {
		commentByID[cm.ID] = cm
	}

	type slotDraft struct {
		action          llm.ReviewPlanSlotAction
		slotTitle       string
		beforeMD        string
		afterMD         string
		unifiedDiff     string
		replyText       string
		citedCandidates []verifier.CitedAnchorCandidate
	}

	var (
		drafts      []slotDraft
		replyParts  []string
		failedSlots []string
	)
	totalSlots := len(plan.TargetSlots)
	var meta doctype.Meta
	if prep.DocTypeBundle != nil {
		meta = prep.DocTypeBundle.Meta
	}

	for idx, act := range plan.TargetSlots {
		if ctx.Err() != nil {
			s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
			return
		}
		sn, ok := secBySlot[act.SlotID]
		if !ok || sn.Ownership == models.OwnershipLocked {
			continue
		}
		_ = safeEmit("stage", map[string]any{
			"stage":   "refining_slot",
			"slot_id": act.SlotID,
			"index":   idx + 1,
			"total":   totalSlots,
		})

		slotSpec := prep.SlotSpecMap[act.SlotID]
		slotSpec = filterSlotSpecForScope(slotSpec, prep.Doc.Subdirectory, meta, len(prep.ModuleDigests) > 0)
		evSnips := s.collectEvidenceSnippets(ctx, prep.Doc.ID, prep.CurrentRev.CommitSHA, act.SlotID, prep.SectionEvs)

		var slotComments []models.ReviewComment
		for _, cid := range act.CommentIDs {
			if cm, exists := commentByID[cid]; exists {
				slotComments = append(slotComments, cm)
			}
		}
		slotInstruction := prep.UserPrompt
		if strings.TrimSpace(act.Reason) != "" {
			slotInstruction = fmt.Sprintf("%s\n\n本章节修订分工说明：%s", prep.UserPrompt, strings.TrimSpace(act.Reason))
		}
		slotMsgs, _ := llm.BuildChatRefineMessages(llm.ChatRefinePromptInput{
			DocType:           prep.DocTypeBundle,
			Slot:              slotSpec,
			CurrentMarkdown:   sn.Markdown,
			IR:                prep.IR,
			ModuleContext:     prep.ModuleContext,
			ModuleDigests:     prep.ModuleDigests,
			CrossModuleGraph:  prep.CrossModuleGraph,
			EvidenceSnippets:  evSnips,
			ReviewComments:    toLLMReviewComments(slotComments),
			History:           prep.History,
			UserPrompt:        slotInstruction,
			Nonce:             prep.PromptNonce,
			EnableSourceTools: prep.EnableTools,
		})

		prefixHeader := fmt.Sprintf("**[%s]** ", sn.Title)
		wrotePrefix := false
		emitSlotDelta := func(delta string) error {
			text := delta
			if !wrotePrefix {
				wrotePrefix = true
				if len(replyParts) > 0 || idx > 0 {
					text = "\n\n" + prefixHeader + text
				} else {
					text = prefixHeader + text
				}
			}
			return safeEmit("delta", map[string]any{"text": text, "slot_id": act.SlotID})
		}

		filter := llm.NewStreamSplitFilter(emitSlotDelta, nil)
		slotResp, slotErr := s.invokeStreamWithOptionalTools(
			ctx, prep, toolkit, act.SlotID, slotSpec.MaxToolRounds, slotMsgs,
			llm.ToolLoopCallbacks{
				OnTurnStart: func(turn int) {
					if turn > 1 {
						filter.ResetTurn()
					}
				},
				OnDelta: filter.Push,
				OnToolEvent: func(ev llm.ToolTraceEvent) error {
					evCopy := ev
					return safeEmit("tool_call", map[string]any{
						"round":       evCopy.Round,
						"tool_name":   evCopy.ToolName,
						"summary":     evCopy.Summary,
						"is_error":    evCopy.IsError,
						"duration_ms": evCopy.DurationMs,
						"slot_id":     act.SlotID,
					})
				},
			},
		)
		if slotErr == nil {
			slotErr = filter.Flush()
		}
		if slotErr != nil {
			if ctx.Err() != nil || errors.Is(slotErr, context.Canceled) {
				s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
				return
			}
			failedSlots = append(failedSlots, fmt.Sprintf("%s(%v)", act.SlotID, slotErr))
			continue
		}

		totalUsage.PromptTokens += slotResp.Usage.PromptTokens
		totalUsage.CachedPromptTokens += slotResp.Usage.CachedPromptTokens
		totalUsage.CompletionTokens += slotResp.Usage.CompletionTokens

		replyText, afterMD, citedCandidates := llm.ParseChatRefineOutput(slotResp.Content)
		if strings.TrimSpace(afterMD) == "" {
			failedSlots = append(failedSlots, act.SlotID+"(空内容)")
			continue
		}
		if !filter.HasEmittedDelta() && strings.TrimSpace(replyText) != "" {
			_ = emitSlotDelta(replyText)
		}
		diffText, _, _ := drift.ComputeUnifiedDiff(sn.Markdown, afterMD)
		replyParts = append(replyParts, fmt.Sprintf("- **%s**: %s", sn.Title, replyText))
		drafts = append(drafts, slotDraft{
			action:          act,
			slotTitle:       sn.Title,
			beforeMD:        sn.Markdown,
			afterMD:         afterMD,
			unifiedDiff:     diffText,
			replyText:       replyText,
			citedCandidates: citedCandidates,
		})
	}

	if ctx.Err() != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}
	if len(drafts) == 0 {
		s.failEmptyProposal(prep, safeEmit, "所有规划章节均未能生成有效修订内容，请重试")
		return
	}

	_ = safeEmit("stage", map[string]any{"stage": "verifying"})

	changedMap := make(map[string]string, len(drafts))
	citedMap := make(map[string][]verifier.CitedAnchorCandidate, len(drafts))
	for _, d := range drafts {
		changedMap[d.action.SlotID] = d.afterMD
		citedMap[d.action.SlotID] = d.citedCandidates
	}
	verifyRes := s.runJointVerifier(prep, changedMap, citedMap)
	previewVO := buildChatVerifierPreview(verifyRes.Report)
	previewBytes, _ := json.Marshal(previewVO)

	var coveredCommentIDs []uint
	var createdVOs []ChatProposalItemVO
	for _, d := range drafts {
		alignedAnchors := convertVerifierAnchors(verifyRes.SectionEvidences[d.action.SlotID])
		persistedAnchors := alignedAnchors
		if len(persistedAnchors) == 0 && len(d.citedCandidates) > 0 {
			persistedAnchors = candidatesToEvidenceAnchors(d.citedCandidates)
		}
		if persistedAnchors == nil {
			persistedAnchors = []models.EvidenceAnchor{}
		}
		citedBytes, _ := json.Marshal(persistedAnchors)
		addrBytes, _ := json.Marshal(d.action.CommentIDs)
		item := &models.ChatProposalItem{
			MessageID:               prep.AssistantMsg.ID,
			DocumentID:              prep.Doc.ID,
			SlotID:                  d.action.SlotID,
			BaseRevisionNo:          prep.CurrentRev.RevisionNo,
			ProposedBeforeMD:        d.beforeMD,
			ProposedAfterMD:         d.afterMD,
			UnifiedDiff:             d.unifiedDiff,
			CitedAnchorsJSON:        datatypes.JSON(citedBytes),
			VerifierPreviewJSON:     datatypes.JSON(previewBytes),
			AddressedCommentIDsJSON: datatypes.JSON(addrBytes),
			Status:                  models.ProposalStatusPending,
		}
		if err := s.chatRepo.CreateProposalItem(ctx, item); err != nil {
			s.cleanupAbortedRefine(prep, "PERSIST_FAILED")
			_ = safeEmit("error", map[string]any{"code": "PERSIST_FAILED", "message": "保存多槽位精修提案失败"})
			return
		}
		for _, cid := range d.action.CommentIDs {
			coveredCommentIDs = appendUniqueID(coveredCommentIDs, cid)
		}
		vo := toChatProposalItemVO(*item, d.slotTitle)
		vo.VerifierPreview = &previewVO
		createdVOs = append(createdVOs, vo)
	}

	summaryContent := strings.Join(replyParts, "\n")
	if len(failedSlots) > 0 {
		summaryContent += fmt.Sprintf("\n\n> 注意：以下章节本轮精修失败已跳过：%s", strings.Join(failedSlots, "、"))
	}
	prep.AssistantMsg.Content = summaryContent
	prep.AssistantMsg.TargetSlotID = drafts[0].action.SlotID
	prep.AssistantMsg.ProposedBeforeMD = drafts[0].beforeMD
	prep.AssistantMsg.ProposedAfterMD = drafts[0].afterMD
	prep.AssistantMsg.UnifiedDiff = drafts[0].unifiedDiff
	prep.AssistantMsg.VerifierPreviewJSON = datatypes.JSON(previewBytes)
	prep.AssistantMsg.ProposalStatus = models.ProposalStatusPending
	if err := s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg); err != nil {
		_ = safeEmit("error", map[string]any{"code": "PERSIST_FAILED", "message": "更新精修消息失败"})
		return
	}
	if s.reviewRepo != nil && len(coveredCommentIDs) > 0 {
		if mErr := s.reviewRepo.MarkProposed(ctx, prep.Doc.ID, coveredCommentIDs, prep.AssistantMsg.ID); mErr != nil {
			log.Printf("[Code-Archdoc] 批量标记批注为 proposed 失败: %v", mErr)
		}
	}

	totalUsage.LatencyMs = time.Since(start).Milliseconds()
	s.recordUsageLog(ctx, prep, globalChatSlotID, "chat_plan_refine", &totalUsage, "success", "")

	for _, vo := range createdVOs {
		if err := safeEmit("proposal_item", vo); err != nil {
			return
		}
	}
	if err := safeEmit("verifier_preview", previewVO); err != nil {
		return
	}
	_ = safeEmit("done", map[string]any{
		"message_id":           prep.AssistantMsg.ID,
		"prompt_tokens":        totalUsage.PromptTokens,
		"cached_prompt_tokens": totalUsage.CachedPromptTokens,
		"completion_tokens":    totalUsage.CompletionTokens,
		"latency_ms":           totalUsage.LatencyMs,
	})
}

func (s *ChatService) runJointVerifier(
	prep *PreparedChatRefine,
	changedMarkdownBySlot map[string]string,
	citedBySlot map[string][]verifier.CitedAnchorCandidate,
) *verifier.VerifyResult {
	prevAnchors := parsePrevAnchorsBySlot(prep.SectionEvs)
	secInputs := make([]verifier.SectionInput, 0, len(prep.Sections))
	for _, sn := range prep.Sections {
		md := sn.Markdown
		var cited []verifier.CitedAnchorCandidate
		if newMD, changed := changedMarkdownBySlot[sn.SlotID]; changed {
			md = newMD
			cited = citedBySlot[sn.SlotID]
		} else {
			cited = evidenceAnchorsToCandidates(prevAnchors[sn.SlotID])
		}
		secInputs = append(secInputs, verifier.SectionInput{
			SlotID:       sn.SlotID,
			Title:        sn.Title,
			Markdown:     md,
			CitedAnchors: cited,
		})
	}
	var slots []doctype.SlotSpec
	var policy *doctype.VerifyPolicy
	if prep.DocTypeBundle != nil {
		slots = prep.DocTypeBundle.Meta.Slots
		policy = prep.DocTypeBundle.Meta.VerifyPolicy
	}
	return s.verifier.Verify(verifier.VerifyInput{
		CommitSHA:     prep.CurrentRev.CommitSHA,
		IR:            prep.IR,
		ModuleContext: prep.ModuleContext,
		ModuleDigests: prep.ModuleDigests,
		Slots:         slots,
		Policy:        policy,
		Sections:      secInputs,
	})
}

// ApplyProposalItems 原子采纳提案组中选定的全部或部分章节（可选带人工 override_markdown 微调）：
// 单事务落盘 1 个不可变修订、同步更新被采纳子项与主消息状态（partial_accepted / accepted）、
// 自动标记关联批注为 resolved、失效同槽位旧 pending 提案并记录 F-26 采纳指标。
func (s *ChatService) ApplyProposalItems(
	ctx context.Context,
	docID, msgID uint,
	req ApplyChatProposalPayload,
	operatorID uint,
) (*DocumentDetailVO, *ChatProposalAuditSnapshot, error) {
	if req.BaseRevisionNo <= 0 {
		return nil, nil, fmt.Errorf("%w: base_revision_no 必须为正整数", ErrInvalidChatParam)
	}

	msg, err := s.chatRepo.GetMessage(ctx, docID, msgID)
	if err != nil {
		return nil, nil, err
	}
	if msg.ProposalStatus != models.ProposalStatusPending && msg.ProposalStatus != models.ProposalStatusPartialAccepted {
		return nil, nil, ErrProposalNotPending
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, nil, err
	}

	allItems, err := s.chatRepo.ListProposalItemsByMessage(ctx, docID, msg.ID)
	if err != nil {
		return nil, nil, err
	}

	// 兼容旧版未拆表 ChatProposalItem 的存量单槽位消息
	if len(allItems) == 0 {
		if msg.ProposalStatus != models.ProposalStatusPending || strings.TrimSpace(msg.TargetSlotID) == "" {
			return nil, nil, ErrProposalNotPending
		}
		afterMD := msg.ProposedAfterMD
		if len(req.Items) > 0 && strings.TrimSpace(req.Items[0].OverrideMarkdown) != "" {
			afterMD = req.Items[0].OverrideMarkdown
		}
		summary := strings.TrimSpace(req.ChangeSummary)
		if summary == "" {
			summary = fmt.Sprintf("Chat 局部精修采纳章节 %s (提案 #%d)", msg.TargetSlotID, msg.ID)
		}
		detail, err := s.revSvc.ApplySectionChange(ctx, ApplySectionChangeInput{
			DocID:          docID,
			SlotID:         msg.TargetSlotID,
			BaseRevisionNo: req.BaseRevisionNo,
			Markdown:       afterMD,
			ChangeSummary:  summary,
			Source:         models.RevSourceChatRefine,
			SourceRefID:    &msg.ID,
			OperatorID:     operatorID,
		})
		if err != nil {
			return nil, nil, err
		}
		appliedRevID := detail.ViewingRevision.ID
		if err := s.chatRepo.UpdateProposalStatus(ctx, msg.ID, models.ProposalStatusAccepted, &appliedRevID); err != nil {
			return nil, nil, err
		}
		s.recordChatAcceptMetric(ctx, doc, true)
		return detail, &ChatProposalAuditSnapshot{
			DocumentID:     docID,
			MessageID:      msg.ID,
			SlotID:         msg.TargetSlotID,
			BaseRevisionNo: req.BaseRevisionNo,
			NewRevisionNo:  detail.CurrentRevisionNo,
			BeforeHash:     sha256Hex(msg.ProposedBeforeMD),
			AfterHash:      sha256Hex(afterMD),
		}, nil
	}

	byID := make(map[uint]models.ChatProposalItem, len(allItems))
	bySlot := make(map[string]models.ChatProposalItem, len(allItems))
	for _, it := range allItems {
		byID[it.ID] = it
		bySlot[it.SlotID] = it
	}

	var selected []*models.ChatProposalItem
	overrides := make(map[uint]string)
	seenItemIDs := make(map[uint]bool)
	if len(req.Items) == 0 {
		for i := range allItems {
			if allItems[i].Status == models.ProposalStatusPending {
				it := allItems[i]
				selected = append(selected, &it)
			}
		}
	} else {
		for _, sel := range req.Items {
			var found models.ChatProposalItem
			var ok bool
			if sel.ItemID > 0 {
				found, ok = byID[sel.ItemID]
			} else if strings.TrimSpace(sel.SlotID) != "" {
				found, ok = bySlot[strings.TrimSpace(sel.SlotID)]
			} else {
				return nil, nil, fmt.Errorf("%w: items 项必须指定 item_id 或 slot_id", ErrInvalidChatParam)
			}
			if !ok {
				return nil, nil, models.ErrNotFound
			}
			if found.Status != models.ProposalStatusPending {
				return nil, nil, ErrProposalNotPending
			}
			if seenItemIDs[found.ID] {
				return nil, nil, fmt.Errorf("%w: items 存在重复提案项 #%d", ErrInvalidChatParam, found.ID)
			}
			seenItemIDs[found.ID] = true
			if sel.OverrideMarkdown != "" {
				overrides[found.ID] = sel.OverrideMarkdown
			}
			cp := found
			selected = append(selected, &cp)
		}
	}
	if len(selected) == 0 {
		return nil, nil, ErrProposalNotPending
	}

	changes := make([]SectionChangeItem, 0, len(selected))
	var appliedSlots []string
	var resolvedCommentIDs []uint
	var beforeAgg, afterAgg strings.Builder
	for _, it := range selected {
		finalMD := it.ProposedAfterMD
		if ov, hasOv := overrides[it.ID]; hasOv {
			finalMD = ov
		}
		var cited []verifier.CitedAnchorCandidate
		if len(it.CitedAnchorsJSON) > 2 {
			_ = json.Unmarshal(it.CitedAnchorsJSON, &cited)
		}
		changes = append(changes, SectionChangeItem{
			SlotID:       it.SlotID,
			Markdown:     finalMD,
			CitedAnchors: cited,
		})
		appliedSlots = append(appliedSlots, it.SlotID)
		beforeAgg.WriteString(it.ProposedBeforeMD)
		afterAgg.WriteString(finalMD)

		var cids []uint
		if len(it.AddressedCommentIDsJSON) > 2 {
			_ = json.Unmarshal(it.AddressedCommentIDsJSON, &cids)
		}
		for _, cid := range cids {
			resolvedCommentIDs = appendUniqueID(resolvedCommentIDs, cid)
		}
	}

	summary := strings.TrimSpace(req.ChangeSummary)
	if summary == "" {
		summary = fmt.Sprintf("采纳提案组 #%d (%s)", msg.ID, strings.Join(appliedSlots, ", "))
	}

	detail, err := s.revSvc.ApplyMultiSectionChanges(ctx, ApplyMultiSectionChangesInput{
		DocID:                  docID,
		BaseRevisionNo:         req.BaseRevisionNo,
		Changes:                changes,
		ChangeSummary:          summary,
		Source:                 models.RevSourceChatRefine,
		SourceRefID:            &msg.ID,
		SkipSupersedeMessageID: msg.ID,
		OperatorID:             operatorID,
	})
	if err != nil {
		return nil, nil, err
	}

	appliedRevID := detail.ViewingRevision.ID
	selectedMap := make(map[uint]bool, len(selected))
	for _, it := range selected {
		selectedMap[it.ID] = true
		finalMD := it.ProposedAfterMD
		if ov, hasOv := overrides[it.ID]; hasOv {
			finalMD = ov
		}
		it.Status = models.ProposalStatusAccepted
		it.FinalAppliedMD = finalMD
		it.AppliedRevisionID = &appliedRevID
		if uErr := s.chatRepo.UpdateProposalItem(ctx, it); uErr != nil {
			return nil, nil, uErr
		}
	}

	if s.reviewRepo != nil && len(resolvedCommentIDs) > 0 {
		if rErr := s.reviewRepo.MarkResolved(ctx, docID, resolvedCommentIDs, appliedRevID); rErr != nil {
			log.Printf("[Code-Archdoc] 标记批注 resolved 失败: %v", rErr)
		}
	}

	// 计算主消息状态收敛（partial_accepted 或 accepted）
	hasRemainingPending := false
	for _, it := range allItems {
		if !selectedMap[it.ID] && it.Status == models.ProposalStatusPending {
			hasRemainingPending = true
			break
		}
	}
	nextMsgStatus := models.ProposalStatusAccepted
	if hasRemainingPending {
		nextMsgStatus = models.ProposalStatusPartialAccepted
	}
	if err := s.chatRepo.UpdateProposalStatus(ctx, msg.ID, nextMsgStatus, &appliedRevID); err != nil {
		return nil, nil, err
	}

	s.recordChatAcceptMetric(ctx, doc, true)

	slotSummary := appliedSlots[0]
	if len(appliedSlots) > 1 {
		slotSummary = strings.Join(appliedSlots, ",")
	}
	return detail, &ChatProposalAuditSnapshot{
		DocumentID:     docID,
		MessageID:      msg.ID,
		SlotID:         slotSummary,
		BaseRevisionNo: req.BaseRevisionNo,
		NewRevisionNo:  detail.CurrentRevisionNo,
		BeforeHash:     sha256Hex(beforeAgg.String()),
		AfterHash:      sha256Hex(afterAgg.String()),
	}, nil
}

// AcceptProposal 采纳 Chat Diff 提案（向后兼容入口，内部委托给 ApplyProposalItems 采纳全部 pending 项）。
func (s *ChatService) AcceptProposal(
	ctx context.Context,
	docID, msgID uint,
	baseRevNo int,
	operatorID uint,
) (*DocumentDetailVO, *ChatProposalAuditSnapshot, error) {
	return s.ApplyProposalItems(ctx, docID, msgID, ApplyChatProposalPayload{
		BaseRevisionNo: baseRevNo,
	}, operatorID)
}

// RejectProposalItem 拒绝提案组内单个槽位提案项并退回其关联 proposed 批注为 open，同时动态收敛父消息状态。
func (s *ChatService) RejectProposalItem(ctx context.Context, docID, itemID uint) (*RejectProposalItemResultVO, error) {
	item, err := s.chatRepo.GetProposalItem(ctx, docID, itemID)
	if err != nil {
		return nil, err
	}
	if item.Status != models.ProposalStatusPending {
		return nil, ErrProposalNotPending
	}
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	item.Status = models.ProposalStatusRejected
	if err := s.chatRepo.UpdateProposalItem(ctx, item); err != nil {
		return nil, err
	}

	if s.reviewRepo != nil && len(item.AddressedCommentIDsJSON) > 2 {
		var cids []uint
		if json.Unmarshal(item.AddressedCommentIDsJSON, &cids) == nil && len(cids) > 0 {
			if rErr := s.reviewRepo.RevertProposedByCommentIDs(ctx, docID, cids); rErr != nil {
				log.Printf("[Code-Archdoc] 拒绝提案项后退回批注状态失败: %v", rErr)
			}
		}
	}

	allItems, err := s.chatRepo.ListProposalItemsByMessage(ctx, docID, item.MessageID)
	if err != nil {
		return nil, err
	}
	hasPending, hasAccepted := false, false
	for _, sibling := range allItems {
		switch sibling.Status {
		case models.ProposalStatusPending:
			hasPending = true
		case models.ProposalStatusAccepted:
			hasAccepted = true
		}
	}
	msgStatus := models.ProposalStatusRejected
	if hasPending && hasAccepted {
		msgStatus = models.ProposalStatusPartialAccepted
	} else if hasPending {
		msgStatus = models.ProposalStatusPending
	} else if hasAccepted {
		msgStatus = models.ProposalStatusAccepted
	}
	if err := s.chatRepo.UpdateProposalStatus(ctx, item.MessageID, msgStatus, nil); err != nil {
		return nil, err
	}

	s.recordChatAcceptMetric(ctx, doc, false)
	return &RejectProposalItemResultVO{
		ItemID:                item.ID,
		MessageID:             item.MessageID,
		ItemStatus:            models.ProposalStatusRejected,
		MessageProposalStatus: msgStatus,
	}, nil
}

// RejectProposal 拒绝整个 Chat Diff 提案及其全部 pending 提案项，并将关联 proposed 批注退回 open。
func (s *ChatService) RejectProposal(ctx context.Context, docID, msgID uint) (*ChatRejectResultVO, error) {
	msg, err := s.chatRepo.GetMessage(ctx, docID, msgID)
	if err != nil {
		return nil, err
	}
	if msg.ProposalStatus != models.ProposalStatusPending && msg.ProposalStatus != models.ProposalStatusPartialAccepted {
		return nil, ErrProposalNotPending
	}
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	items, _ := s.chatRepo.ListProposalItemsByMessage(ctx, docID, msg.ID)
	var revertCIDs []uint
	hasAccepted := false
	for i := range items {
		if items[i].Status == models.ProposalStatusAccepted {
			hasAccepted = true
			continue
		}
		if items[i].Status == models.ProposalStatusPending {
			items[i].Status = models.ProposalStatusRejected
			_ = s.chatRepo.UpdateProposalItem(ctx, &items[i])
			var cids []uint
			if json.Unmarshal(items[i].AddressedCommentIDsJSON, &cids) == nil {
				for _, cid := range cids {
					revertCIDs = appendUniqueID(revertCIDs, cid)
				}
			}
		}
	}
	if s.reviewRepo != nil {
		if len(revertCIDs) > 0 {
			_ = s.reviewRepo.RevertProposedByCommentIDs(ctx, docID, revertCIDs)
		} else {
			_ = s.reviewRepo.RevertProposedByMessageID(ctx, docID, msg.ID)
		}
	}

	nextStatus := models.ProposalStatusRejected
	if hasAccepted {
		nextStatus = models.ProposalStatusPartialAccepted
	}
	if err := s.chatRepo.UpdateProposalStatus(ctx, msg.ID, nextStatus, nil); err != nil {
		return nil, err
	}
	s.recordChatAcceptMetric(ctx, doc, false)

	return &ChatRejectResultVO{
		MessageID:      msg.ID,
		ProposalStatus: nextStatus,
	}, nil
}

func (s *ChatService) handleStreamError(
	ctx context.Context,
	prep *PreparedChatRefine,
	err error,
	latencyMs int64,
	safeEmit func(string, any) error,
) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.chatRepo.DeleteProposalItemsByMessage(bgCtx, prep.AssistantMsg.ID)
	if s.reviewRepo != nil {
		_ = s.reviewRepo.RevertProposedByMessageID(bgCtx, prep.Doc.ID, prep.AssistantMsg.ID)
	}
	if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
		log.Printf("[Code-Archdoc] 清理失败占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
	}
	errCode := "LLM_ERROR"
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		errCode = "LLM_TIMEOUT"
	}
	s.recordUsageLog(bgCtx, prep, prep.TargetSlot.SlotID, "chat_refine", &llm.UsageMetrics{
		ModelName: prep.Endpoint.ModelName,
		LatencyMs: latencyMs,
	}, "failed", errCode)
	_ = safeEmit("error", map[string]any{
		"code":    errCode,
		"message": "精修请求失败：" + err.Error(),
	})
}

func (s *ChatService) failEmptyProposal(prep *PreparedChatRefine, safeEmit func(string, any) error, msg string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.chatRepo.DeleteProposalItemsByMessage(bgCtx, prep.AssistantMsg.ID)
	if s.reviewRepo != nil {
		_ = s.reviewRepo.RevertProposedByMessageID(bgCtx, prep.Doc.ID, prep.AssistantMsg.ID)
	}
	if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
		log.Printf("[Code-Archdoc] 清理空提案占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
	}
	_ = safeEmit("error", map[string]any{
		"code":    "EMPTY_PROPOSAL",
		"message": msg,
	})
}

func (s *ChatService) cleanupAbortedRefine(prep *PreparedChatRefine, errCode string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if delItemErr := s.chatRepo.DeleteProposalItemsByMessage(bgCtx, prep.AssistantMsg.ID); delItemErr != nil {
		log.Printf("[Code-Archdoc] 清理中止精修提案项(%d)失败: %v", prep.AssistantMsg.ID, delItemErr)
	}
	if s.reviewRepo != nil {
		if revErr := s.reviewRepo.RevertProposedByMessageID(bgCtx, prep.Doc.ID, prep.AssistantMsg.ID); revErr != nil {
			log.Printf("[Code-Archdoc] 退回中止精修批注(%d)失败: %v", prep.AssistantMsg.ID, revErr)
		}
	}
	if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
		log.Printf("[Code-Archdoc] 清理中止精修占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
	}
	s.recordUsageLog(bgCtx, prep, prep.TargetSlot.SlotID, "chat_refine", &llm.UsageMetrics{ModelName: prep.Endpoint.ModelName}, "canceled", errCode)
}

func (s *ChatService) recordChatAcceptMetric(ctx context.Context, doc *models.Document, accepted bool) {
	if s.govRepo == nil || doc == nil {
		return
	}
	if mErr := s.govRepo.RecordQualityMetric(ctx, &models.QualityMetric{
		DocumentID:         doc.ID,
		RepoID:             doc.RepoID,
		DocTypeSlug:        doc.DocTypeSlug,
		DocTypeRevisionID:  doc.DocTypeRevisionID,
		MetricEvent:        models.MetricEventChatProposalResolved,
		ChatAccepted:       &accepted,
		SlotDiffRatiosJSON: datatypes.JSON(`{}`),
	}); mErr != nil {
		log.Printf("[Code-Archdoc] 记录 Chat 提案处置指标失败 (doc_id=%d): %v", doc.ID, mErr)
	}
}

func (s *ChatService) recordUsageLog(
	ctx context.Context,
	prep *PreparedChatRefine,
	slotID string,
	stage string,
	usage *llm.UsageMetrics,
	status string,
	errCode string,
) {
	if s.govRepo == nil || prep == nil {
		return
	}
	docID := prep.Doc.ID
	var dtRevID *uint
	if prep.Doc.DocTypeRevisionID > 0 {
		v := prep.Doc.DocTypeRevisionID
		dtRevID = &v
	}
	modelName := prep.Endpoint.ModelName
	promptTokens, cachedTokens, compTokens := 0, 0, 0
	var latencyMs int64
	if usage != nil {
		if usage.ModelName != "" {
			modelName = usage.ModelName
		}
		promptTokens = usage.PromptTokens
		cachedTokens = usage.CachedPromptTokens
		compTokens = usage.CompletionTokens
		latencyMs = usage.LatencyMs
	}

	if logErr := s.govRepo.RecordLLMUsage(ctx, &models.LLMUsageLog{
		DocumentID:         &docID,
		RepoID:             prep.Doc.RepoID,
		DocTypeSlug:        prep.Doc.DocTypeSlug,
		DocTypeRevisionID:  dtRevID,
		SlotID:             slotID,
		Stage:              stage,
		ModelTier:          prep.ModelTier,
		ModelName:          modelName,
		PromptTokens:       promptTokens,
		CachedPromptTokens: cachedTokens,
		CompletionTokens:   compTokens,
		LatencyMs:          latencyMs,
		Status:             status,
		ErrorCode:          errCode,
		OperatorID:         prep.OperatorID,
	}); logErr != nil {
		log.Printf("[Code-Archdoc] 记录 %s LLM 用量日志失败 (doc_id=%d): %v", stage, docID, logErr)
	}
}

func (s *ChatService) loadDocTypeBundle(ctx context.Context, slug string, docTypeRevID uint) (*doctype.AssetBundle, string) {
	var dtBundle *doctype.AssetBundle
	preferredTier := models.ModelTierFlagship
	if s.docTypeSvc != nil && s.docTypeSvc.Registry() != nil {
		if b, bErr := s.docTypeSvc.Registry().LoadOne(slug); bErr == nil && b != nil {
			dtBundle = b
			if models.IsValidModelTier(b.Meta.ModelTier) {
				preferredTier = b.Meta.ModelTier
			}
		}
	}
	if dtBundle == nil && s.docTypeRepo != nil && docTypeRevID > 0 {
		if dtRev, rErr := s.docTypeRepo.GetRevisionByID(ctx, docTypeRevID); rErr == nil && dtRev != nil {
			var meta doctype.Meta
			if json.Unmarshal(dtRev.MetaJSON, &meta) == nil {
				dtBundle = &doctype.AssetBundle{
					Slug:               slug,
					Meta:               meta,
					TemplateMD:         dtRev.TemplateMD,
					GenerationPromptMD: dtRev.GenerationPromptMD,
					RefinePromptMD:     dtRev.RefinePromptMD,
				}
				if models.IsValidModelTier(meta.ModelTier) {
					preferredTier = meta.ModelTier
				}
			}
		}
	}
	return dtBundle, preferredTier
}

func (s *ChatService) resolveEndpoint(ctx context.Context, preferredTier string) (llm.EndpointConfig, string, error) {
	if s.govRepo == nil {
		return llm.EndpointConfig{}, "", ErrChatLLMNotConfigured
	}
	tiers := []string{preferredTier}
	if preferredTier == models.ModelTierFlagship {
		tiers = append(tiers, models.ModelTierFast)
	} else {
		tiers = append(tiers, models.ModelTierFlagship)
	}

	for _, tier := range tiers {
		cfg, err := s.govRepo.GetLLMConfigByTier(ctx, tier)
		if err != nil || cfg == nil {
			continue
		}
		if !cfg.IsActive || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ModelName) == "" {
			continue
		}
		var apiKey string
		if cfg.APIKeyCipher != "" && s.cipher != nil {
			dec, decErr := s.cipher.Decrypt(cfg.APIKeyCipher)
			if decErr != nil {
				return llm.EndpointConfig{}, "", fmt.Errorf("解密 %s 档位 APIKey 失败: %w", tier, decErr)
			}
			apiKey = dec
		}
		return llm.EndpointConfig{
			BaseURL:        cfg.BaseURL,
			ModelName:      cfg.ModelName,
			APIKey:         apiKey,
			TimeoutSeconds: cfg.TimeoutSeconds,
			MaxRetries:     cfg.MaxRetries,
		}, tier, nil
	}

	return llm.EndpointConfig{}, "", ErrChatLLMNotConfigured
}

func (s *ChatService) collectEvidenceSnippets(
	ctx context.Context,
	docID uint,
	commitSHA string,
	slotID string,
	evs []models.SectionEvidence,
) map[string]string {
	if s.previewSvc == nil {
		return nil
	}
	var targetEv *models.SectionEvidence
	for i := range evs {
		if evs[i].SlotID == slotID {
			targetEv = &evs[i]
			break
		}
	}
	if targetEv == nil {
		return nil
	}
	var anchors []models.EvidenceAnchor
	if err := json.Unmarshal(targetEv.EvidencesJSON, &anchors); err != nil || len(anchors) == 0 {
		return nil
	}

	out := make(map[string]string)
	for _, an := range anchors {
		if len(out) >= 4 {
			break
		}
		if an.FilePath == "" {
			continue
		}
		if _, dup := out[an.FilePath]; dup {
			continue
		}
		prev, err := s.previewSvc.GetSourcePreview(ctx, SourcePreviewRequest{
			DocID:     docID,
			Commit:    commitSHA,
			File:      an.FilePath,
			Scope:     an.Scope,
			LineStart: an.LineStart,
			LineEnd:   an.LineEnd,
			Symbol:    an.FQSymbol,
			CanWrite:  true,
		})
		if err != nil || prev == nil || strings.TrimSpace(prev.Content) == "" {
			continue
		}
		snippet := prev.Content
		if len(snippet) > 4000 {
			snippet = snippet[:4000] + "\n...(truncated)"
		}
		if s.redactor != nil {
			snippet, _ = s.redactor.RedactText(snippet)
		}
		out[an.FilePath] = snippet
	}
	return out
}

func parseVerifierPreviewJSON(raw datatypes.JSON) *ChatVerifierPreviewVO {
	if len(raw) <= 2 {
		return nil
	}
	var p ChatVerifierPreviewVO
	if json.Unmarshal(raw, &p) == nil && (p.Issues != nil || p.CanApprove || p.BlockerCount > 0 || p.ErrorCount > 0 || p.WarningCount > 0) {
		if p.Issues == nil {
			p.Issues = []ChatVerifierIssueVO{}
		}
		return &p
	}
	return nil
}

func toChatProposalItemVO(it models.ChatProposalItem, slotTitle string) ChatProposalItemVO {
	var anchors []models.EvidenceAnchor
	if len(it.CitedAnchorsJSON) > 2 {
		_ = json.Unmarshal(it.CitedAnchorsJSON, &anchors)
	}
	if anchors == nil {
		anchors = []models.EvidenceAnchor{}
	}
	var cids []uint
	if len(it.AddressedCommentIDsJSON) > 2 {
		_ = json.Unmarshal(it.AddressedCommentIDsJSON, &cids)
	}
	if cids == nil {
		cids = []uint{}
	}
	return ChatProposalItemVO{
		ItemID:              it.ID,
		MessageID:           it.MessageID,
		SlotID:              it.SlotID,
		SlotTitle:           slotTitle,
		BaseRevisionNo:      it.BaseRevisionNo,
		BeforeMD:            it.ProposedBeforeMD,
		AfterMD:             it.ProposedAfterMD,
		FinalAppliedMD:      it.FinalAppliedMD,
		UnifiedDiff:         it.UnifiedDiff,
		CitedAnchors:        anchors,
		VerifierPreview:     parseVerifierPreviewJSON(it.VerifierPreviewJSON),
		AddressedCommentIDs: cids,
		Status:              it.Status,
		AppliedRevisionID:   it.AppliedRevisionID,
	}
}

func buildPlanSectionSummaries(snaps []models.DocumentSectionSnapshot) []llm.ReviewPlanSectionSummary {
	out := make([]llm.ReviewPlanSectionSummary, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, llm.ReviewPlanSectionSummary{
			SlotID:          sn.SlotID,
			Title:           sn.Title,
			Ownership:       sn.Ownership,
			MarkdownPreview: truncateRunes(strings.TrimSpace(sn.Markdown), 220),
		})
	}
	return out
}

func buildPlanComments(comments []models.ReviewComment) []llm.ReviewPlanComment {
	out := make([]llm.ReviewPlanComment, 0, len(comments))
	for _, cm := range comments {
		var an models.ReviewCommentAnchor
		_ = json.Unmarshal(cm.AnchorJSON, &an)
		out = append(out, llm.ReviewPlanComment{
			CommentID: cm.ID,
			CommentNo: cm.CommentNo,
			SlotID:    cm.SlotID,
			Action:    cm.Category,
			Quote:     an.Quote,
			Body:      cm.Content,
			StartLine: an.SourceLineStart,
			EndLine:   an.SourceLineEnd,
		})
	}
	return out
}

func buildChatPlanSummaryVO(plan *llm.ReviewPlanResult, snaps []models.DocumentSectionSnapshot) ChatPlanSummaryVO {
	titleBySlot := make(map[string]string, len(snaps))
	for _, sn := range snaps {
		titleBySlot[sn.SlotID] = sn.Title
	}
	targets := make([]ChatPlanTargetSlotVO, 0, len(plan.TargetSlots))
	for _, t := range plan.TargetSlots {
		targets = append(targets, ChatPlanTargetSlotVO{
			SlotID:     t.SlotID,
			Title:      titleBySlot[t.SlotID],
			CommentIDs: t.CommentIDs,
			Reason:     t.Reason,
		})
	}
	locked := make([]ChatPlanLockedAdviceVO, 0, len(plan.AdvisoryLocked))
	for _, a := range plan.AdvisoryLocked {
		locked = append(locked, ChatPlanLockedAdviceVO{
			SlotID:     a.SlotID,
			Title:      titleBySlot[a.SlotID],
			CommentIDs: a.CommentIDs,
			Advice:     a.Reason,
		})
	}
	return ChatPlanSummaryVO{
		Intent:             plan.Intent,
		Summary:            plan.Summary,
		TargetSlots:        targets,
		SkippedLockedSlots: locked,
		TruncatedSlots:     plan.TruncatedSlots,
	}
}

func buildAdvisoryOnlyReply(planVO ChatPlanSummaryVO) string {
	var b strings.Builder
	b.WriteString(planVO.Summary)
	if len(planVO.SkippedLockedSlots) > 0 {
		b.WriteString("\n\n以下目标章节已处于 **locked（锁定）** 状态，遵循“人工锁定铁律”，本次仅输出只读修改建议而不修改原文：\n")
		for _, a := range planVO.SkippedLockedSlots {
			title := a.Title
			if title == "" {
				title = a.SlotID
			}
			b.WriteString(fmt.Sprintf("- **%s (`%s`)**: %s\n", title, a.SlotID, a.Advice))
		}
	}
	return b.String()
}

func toLLMInlineAnchor(in *models.ReviewCommentAnchor) *llm.RefineInlineAnchorInput {
	if in == nil {
		return nil
	}
	return &llm.RefineInlineAnchorInput{
		StartLine: in.SourceLineStart,
		EndLine:   in.SourceLineEnd,
		Quote:     in.Quote,
	}
}

func toLLMReviewComments(comments []models.ReviewComment) []llm.RefineReviewCommentInput {
	if len(comments) == 0 {
		return nil
	}
	out := make([]llm.RefineReviewCommentInput, 0, len(comments))
	for _, cm := range comments {
		var an models.ReviewCommentAnchor
		_ = json.Unmarshal(cm.AnchorJSON, &an)
		out = append(out, llm.RefineReviewCommentInput{
			CommentNo: cm.CommentNo,
			Action:    cm.Category,
			Quote:     an.Quote,
			Body:      cm.Content,
			StartLine: an.SourceLineStart,
			EndLine:   an.SourceLineEnd,
		})
	}
	return out
}

func dedupeUintSlice(ids []uint) []uint {
	var out []uint
	seen := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func appendUniqueID(slice []uint, v uint) []uint {
	for _, x := range slice {
		if x == v {
			return slice
		}
	}
	return append(slice, v)
}

func buildChatVerifierPreview(r verifier.VerifierReport) ChatVerifierPreviewVO {
	issues := make([]ChatVerifierIssueVO, 0, len(r.SecretFindings)+len(r.MermaidIssues)+len(r.UnverifiedRefs)+len(r.MissingRequiredSlots))
	for _, sf := range r.SecretFindings {
		issues = append(issues, ChatVerifierIssueVO{
			Kind:     "secret",
			Severity: "blocker",
			Message:  fmt.Sprintf("命中疑似明文凭据 (%s): %s", sf.RuleID, sf.MatchedMask),
			SlotID:   sf.SlotID,
		})
	}
	for _, mi := range r.MermaidIssues {
		issues = append(issues, ChatVerifierIssueVO{
			Kind:     "mermaid",
			Severity: mi.Severity,
			Message:  fmt.Sprintf("Mermaid 图表问题 (%s): %s", mi.IssueCode, mi.Message),
			SlotID:   mi.SlotID,
		})
	}
	for _, ur := range r.UnverifiedRefs {
		issues = append(issues, ChatVerifierIssueVO{
			Kind:     "unverified_ref",
			Severity: ur.Severity,
			Message:  fmt.Sprintf("未验证的 %s 引用: %s", ur.RefKind, ur.Value),
			SlotID:   ur.SlotID,
		})
	}
	for _, ms := range r.MissingRequiredSlots {
		issues = append(issues, ChatVerifierIssueVO{
			Kind:     "missing_slot",
			Severity: "error",
			Message:  fmt.Sprintf("必填槽位缺失或字数不足: %s", ms),
			SlotID:   ms,
		})
	}
	return ChatVerifierPreviewVO{
		CanApprove:   r.CanApprove,
		BlockerCount: r.BlockerCount,
		ErrorCount:   r.ErrorCount,
		WarningCount: r.WarningCount,
		Issues:       issues,
	}
}

func convertVerifierAnchors(in []verifier.EvidenceAnchor) []models.EvidenceAnchor {
	out := make([]models.EvidenceAnchor, 0, len(in))
	for _, a := range in {
		out = append(out, models.EvidenceAnchor{
			AnchorID:           a.AnchorID,
			AnchorKey:          a.AnchorKey,
			Scope:              a.Scope,
			FilePath:           a.FilePath,
			SymbolKind:         a.SymbolKind,
			FQSymbol:           a.FQSymbol,
			ContentFingerprint: a.ContentFingerprint,
			CommitSHA:          a.CommitSHA,
			LineStart:          a.LineStart,
			LineEnd:            a.LineEnd,
			Confidence:         a.Confidence,
		})
	}
	return out
}

func candidatesToEvidenceAnchors(in []verifier.CitedAnchorCandidate) []models.EvidenceAnchor {
	out := make([]models.EvidenceAnchor, 0, len(in))
	for _, c := range in {
		if strings.TrimSpace(c.FilePath) == "" && strings.TrimSpace(c.FQSymbol) == "" {
			continue
		}
		out = append(out, models.EvidenceAnchor{
			Scope:      c.Scope,
			FilePath:   c.FilePath,
			SymbolKind: c.SymbolKind,
			FQSymbol:   c.FQSymbol,
		})
	}
	return out
}
