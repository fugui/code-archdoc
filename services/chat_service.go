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
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/security"
	"code-archdoc/pkg/verifier"

	"gorm.io/datatypes"
)

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
	SessionID      *uint  `json:"session_id"`
	TargetSlotID   string `json:"target_slot_id"`
	BaseRevisionNo int    `json:"base_revision_no"`
	Prompt         string `json:"prompt"`
}

type AcceptChatProposalRequest struct {
	BaseRevisionNo int `json:"base_revision_no"`
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

type ChatMessageVO struct {
	ID                uint                   `json:"id"`
	SessionID         uint                   `json:"session_id"`
	Role              string                 `json:"role"`
	Content           string                 `json:"content"`
	TargetSlotID      string                 `json:"target_slot_id"`
	BaseRevisionNo    int                    `json:"base_revision_no"`
	ProposedBeforeMD  string                 `json:"proposed_before_md"`
	ProposedAfterMD   string                 `json:"proposed_after_md"`
	UnifiedDiff       string                 `json:"unified_diff"`
	VerifierPreview   *ChatVerifierPreviewVO `json:"verifier_preview,omitempty"`
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
	Doc            *models.Document
	CurrentRev     *models.DocumentRevision
	Sections       []models.DocumentSectionSnapshot
	SectionEvs     []models.SectionEvidence
	TargetIdx      int
	TargetSlot     doctype.SlotSpec
	DocTypeBundle  *doctype.AssetBundle
	IR             *codeir.CodeIRBundle
	Session        *models.ChatSession
	AssistantMsg   *models.ChatMessage
	ModelTier      string
	Endpoint       llm.EndpointConfig
	PromptMessages []llm.Message
	PromptNonce    string
	RepoRoot       string
	ScopeRoot      string
	EnableTools    bool
	OperatorID     uint
}

// ChatService 编排 Canvas Studio SSE Chat 局部精修、确定性 Verifier 预览、Accept/Reject 状态机与 F-26 采纳率指标。
type ChatService struct {
	chatRepo    ChatRepo
	docRepo     DocumentRepo
	docTypeRepo DocTypeRepo
	docTypeSvc  *DocTypeService
	govRepo     GovernanceRepo
	revSvc      *RevisionService
	snapSvc     *SnapshotService
	previewSvc  *SourcePreviewService
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

// ListSessions 获取文档精修会话列表与完整消息/提案历史（按 updated_at DESC 排序）。
func (s *ChatService) ListSessions(ctx context.Context, docID uint) ([]ChatSessionWithMessagesVO, error) {
	if _, err := s.docRepo.GetByID(ctx, docID); err != nil {
		return nil, err
	}
	sessions, msgMap, err := s.chatRepo.ListSessionsWithMessages(ctx, docID)
	if err != nil {
		return nil, err
	}

	out := make([]ChatSessionWithMessagesVO, 0, len(sessions))
	for _, sess := range sessions {
		rawMsgs := msgMap[sess.ID]
		msgs := make([]ChatMessageVO, 0, len(rawMsgs))
		for _, m := range rawMsgs {
			var preview *ChatVerifierPreviewVO
			if len(m.VerifierPreviewJSON) > 2 {
				var p ChatVerifierPreviewVO
				if json.Unmarshal(m.VerifierPreviewJSON, &p) == nil && (p.Issues != nil || p.CanApprove || p.BlockerCount > 0 || p.ErrorCount > 0 || p.WarningCount > 0) {
					if p.Issues == nil {
						p.Issues = []ChatVerifierIssueVO{}
					}
					preview = &p
				}
			}
			msgs = append(msgs, ChatMessageVO{
				ID:                m.ID,
				SessionID:         m.SessionID,
				Role:              m.Role,
				Content:           m.Content,
				TargetSlotID:      m.TargetSlotID,
				BaseRevisionNo:    m.BaseRevisionNo,
				ProposedBeforeMD:  m.ProposedBeforeMD,
				ProposedAfterMD:   m.ProposedAfterMD,
				UnifiedDiff:       m.UnifiedDiff,
				VerifierPreview:   preview,
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
	promptText := strings.TrimSpace(req.Prompt)
	if promptText == "" {
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

	targetSlotID := strings.TrimSpace(req.TargetSlotID)
	if targetSlotID == "" {
		targetSlotID = snaps[0].SlotID
	}

	targetIdx := -1
	for i := range snaps {
		if snaps[i].SlotID == targetSlotID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, models.ErrNotFound
	}

	// 锁定章节硬保护：locked 槽位拒绝 AI 局部精修，返回 409 CONFLICT
	if snaps[targetIdx].Ownership == models.OwnershipLocked {
		return nil, ErrSlotLocked
	}

	// 加载 DocType 资产（获取 refine_prompt.md、model_tier、SlotSpec、VerifyPolicy）
	var dtBundle *doctype.AssetBundle
	preferredTier := models.ModelTierFlagship
	if s.docTypeSvc != nil && s.docTypeSvc.Registry() != nil {
		if b, bErr := s.docTypeSvc.Registry().LoadOne(doc.DocTypeSlug); bErr == nil && b != nil {
			dtBundle = b
			if models.IsValidModelTier(b.Meta.ModelTier) {
				preferredTier = b.Meta.ModelTier
			}
		}
	}
	if dtBundle == nil && s.docTypeRepo != nil && curRev.DocTypeRevisionID > 0 {
		if dtRev, rErr := s.docTypeRepo.GetRevisionByID(ctx, curRev.DocTypeRevisionID); rErr == nil && dtRev != nil {
			var meta doctype.Meta
			if json.Unmarshal(dtRev.MetaJSON, &meta) == nil {
				dtBundle = &doctype.AssetBundle{
					Slug:               doc.DocTypeSlug,
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

	slotSpec := doctype.SlotSpec{
		SlotID:      snaps[targetIdx].SlotID,
		Title:       snaps[targetIdx].Title,
		Ownership:   snaps[targetIdx].Ownership,
		TokenBudget: 6000,
	}
	if dtBundle != nil {
		for _, sl := range dtBundle.Meta.Slots {
			if sl.SlotID == targetSlotID {
				slotSpec = sl
				break
			}
		}
	}

	// 解析可用的 LLM 端点配置（优先 DocType 档位，未启用则回退另一档位）
	endpointCfg, resolvedTier, err := s.resolveEndpoint(ctx, preferredTier)
	if err != nil {
		return nil, err
	}

	// 尽力加载快照 Code IR 切片（离线单测无快照时平滑降级为 nil）
	var irBundle *codeir.CodeIRBundle
	if s.snapSvc != nil && curRev.SnapshotID > 0 {
		if _, _, ir, irErr := s.snapSvc.LoadIRBundleBySnapshotID(ctx, curRev.SnapshotID); irErr == nil {
			irBundle = ir
		}
	}

	// 尽力读取目标章节关联 EvidenceAnchor 的脱敏源码片段（受限最多 4 个文件，避免上下文膨胀）
	evidenceSnippets := s.collectEvidenceSnippets(ctx, doc.ID, curRev.CommitSHA, targetSlotID, prevEvs)

	// 创建或复用精修会话，并收集最近 4 条历史消息
	sess, err := s.chatRepo.GetOrCreateSession(ctx, doc.ID, targetSlotID, operatorID, req.SessionID)
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
			if m.Role == "user" || m.Role == "assistant" {
				history = append(history, llm.Message{Role: m.Role, Content: m.Content})
			}
		}
	}

	// 写入用户提问消息与助手占位消息（proposal_status="none"，仅在完整生成后才置为 "pending"，中止时自动清理）
	userMsg := &models.ChatMessage{
		SessionID:      sess.ID,
		Role:           "user",
		Content:        promptText,
		TargetSlotID:   targetSlotID,
		BaseRevisionNo: baseRevNo,
		ProposalStatus: models.ProposalStatusNone,
		ModelTier:      resolvedTier,
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
		Content:             "",
		TargetSlotID:        targetSlotID,
		BaseRevisionNo:      baseRevNo,
		VerifierPreviewJSON: datatypes.JSON(`{}`),
		ProposalStatus:      models.ProposalStatusNone,
		ModelTier:           resolvedTier,
	}
	if err := s.chatRepo.CreateMessage(ctx, assistantMsg); err != nil {
		return nil, err
	}

	// 解析只读代码沙箱范围：若沙箱目录就绪或存在快照 Code IR，则为 Chat 精修开放受限只读源码工具
	var repoRoot, scopeRoot string
	hasFS := false
	if s.snapSvc != nil {
		repoRoot, scopeRoot, hasFS = s.snapSvc.ResolveSandboxScope(doc.RepoID, doc.Subdirectory)
	}
	enableTools := hasFS || irBundle != nil

	promptMsgs, promptNonce := llm.BuildChatRefineMessages(llm.ChatRefinePromptInput{
		DocType:           dtBundle,
		Slot:              slotSpec,
		CurrentMarkdown:   snaps[targetIdx].Markdown,
		IR:                irBundle,
		EvidenceSnippets:  evidenceSnippets,
		History:           history,
		UserPrompt:        promptText,
		EnableSourceTools: enableTools,
	})

	return &PreparedChatRefine{
		Doc:            doc,
		CurrentRev:     curRev,
		Sections:       snaps,
		SectionEvs:     prevEvs,
		TargetIdx:      targetIdx,
		TargetSlot:     slotSpec,
		DocTypeBundle:  dtBundle,
		IR:             irBundle,
		Session:        sess,
		AssistantMsg:   assistantMsg,
		ModelTier:      resolvedTier,
		Endpoint:       endpointCfg,
		PromptMessages: promptMsgs,
		PromptNonce:    promptNonce,
		RepoRoot:       repoRoot,
		ScopeRoot:      scopeRoot,
		EnableTools:    enableTools,
		OperatorID:     operatorID,
	}, nil
}

// ExecuteRefineStream 执行 SSE 流式精修：依次推送 meta -> delta* -> diff_proposal -> verifier_preview -> done（或 error）。
// 若客户端通过 AbortController 取消 ctx，立即中断上游 LLM 连接并清理占位消息，绝不落库无效 pending 提案。
func (s *ChatService) ExecuteRefineStream(
	ctx context.Context,
	prep *PreparedChatRefine,
	emit func(event string, payload any) error,
) {
	if err := emit("meta", map[string]any{
		"session_id":       prep.Session.ID,
		"message_id":       prep.AssistantMsg.ID,
		"slot_id":          prep.TargetSlot.SlotID,
		"base_revision_no": prep.CurrentRev.RevisionNo,
		"model_tier":       prep.ModelTier,
	}); err != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_DISCONNECTED")
		return
	}

	start := time.Now()
	var (
		resp *llm.ChatResponse
		err  error
	)
	onDelta := func(delta string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return emit("delta", map[string]any{"text": delta})
	}
	if prep.EnableTools {
		releaseLease := s.snapSvc.AcquireRepoLease(prep.Doc.RepoID)
		defer releaseLease()

		toolkit := llm.NewSandboxSourceToolkit(llm.SandboxToolkitConfig{
			ScopeRoot: prep.ScopeRoot,
			RepoRoot:  prep.RepoRoot,
			IR:        prep.IR,
			Redactor:  s.redactor,
		})
		if toolkit.HasTools() {
			maxRounds := prep.TargetSlot.MaxToolRounds
			if maxRounds <= 0 {
				maxRounds = 4
			}
			var loopRes *llm.ToolLoopResult
			loopRes, err = llm.RunToolLoopStream(ctx, s.llmClient, prep.Endpoint, llm.ChatRequest{
				Messages: prep.PromptMessages,
				Stream:   true,
			}, toolkit, llm.ToolLoopOptions{
				Nonce:     prep.PromptNonce,
				MaxRounds: maxRounds,
			}, llm.ToolLoopCallbacks{
				OnDelta: onDelta,
				OnToolEvent: func(ev llm.ToolTraceEvent) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return emit("tool_call", ev)
				},
			})
			if err == nil && loopRes != nil {
				resp = loopRes.Response
			}
		} else {
			resp, err = s.llmClient.ChatCompletionStream(ctx, prep.Endpoint, llm.ChatRequest{
				Messages: prep.PromptMessages,
				Stream:   true,
			}, onDelta)
		}
	} else {
		resp, err = s.llmClient.ChatCompletionStream(ctx, prep.Endpoint, llm.ChatRequest{
			Messages: prep.PromptMessages,
			Stream:   true,
		}, onDelta)
	}

	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
			return
		}
		// 非取消错误：清理占位消息，记录失败日志，推送 error 事件
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
			log.Printf("[Code-Archdoc] 清理失败占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
		}
		errCode := "LLM_ERROR"
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
			errCode = "LLM_TIMEOUT"
		}
		s.recordUsageLog(bgCtx, prep, &llm.UsageMetrics{
			ModelName: prep.Endpoint.ModelName,
			LatencyMs: latencyMs,
		}, "failed", errCode)
		if emitErr := emit("error", map[string]any{
			"code":    errCode,
			"message": "精修请求失败：" + err.Error(),
		}); emitErr != nil {
			log.Printf("[Code-Archdoc] 推送 SSE error 事件失败: %v", emitErr)
		}
		return
	}

	if ctx.Err() != nil {
		s.cleanupAbortedRefine(prep, "CLIENT_ABORTED")
		return
	}

	// 解析结构化提案与计算章节级 Unified Diff
	replyText, afterMD, citedCandidates := llm.ParseChatRefineOutput(resp.Content)
	if strings.TrimSpace(afterMD) == "" {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
			log.Printf("[Code-Archdoc] 清理空提案占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
		}
		if emitErr := emit("error", map[string]any{
			"code":    "EMPTY_PROPOSAL",
			"message": "模型未生成有效的章节修订内容，请重试",
		}); emitErr != nil {
			log.Printf("[Code-Archdoc] 推送 SSE 空提案错误事件失败: %v", emitErr)
		}
		return
	}

	beforeMD := prep.Sections[prep.TargetIdx].Markdown
	unifiedDiff, _, _ := drift.ComputeUnifiedDiff(beforeMD, afterMD)

	// 预先运行四重确定性 Verifier（仅替换目标章节）
	secInputs := make([]verifier.SectionInput, 0, len(prep.Sections))
	for i, sn := range prep.Sections {
		md := sn.Markdown
		var cited []verifier.CitedAnchorCandidate
		if i == prep.TargetIdx {
			md = afterMD
			cited = citedCandidates
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
	verifyRes := s.verifier.Verify(verifier.VerifyInput{
		CommitSHA: prep.CurrentRev.CommitSHA,
		IR:        prep.IR,
		Slots:     slots,
		Policy:    policy,
		Sections:  secInputs,
	})

	alignedAnchors := convertVerifierAnchors(verifyRes.SectionEvidences[prep.TargetSlot.SlotID])
	if alignedAnchors == nil {
		alignedAnchors = []models.EvidenceAnchor{}
	}
	previewVO := buildChatVerifierPreview(verifyRes.Report)
	previewBytes, _ := json.Marshal(previewVO)

	// 持久化完整 pending 提案到助手消息
	prep.AssistantMsg.Content = replyText
	prep.AssistantMsg.ProposedBeforeMD = beforeMD
	prep.AssistantMsg.ProposedAfterMD = afterMD
	prep.AssistantMsg.UnifiedDiff = unifiedDiff
	prep.AssistantMsg.VerifierPreviewJSON = datatypes.JSON(previewBytes)
	prep.AssistantMsg.ProposalStatus = models.ProposalStatusPending
	if err := s.chatRepo.UpdateMessage(ctx, prep.AssistantMsg); err != nil {
		if emitErr := emit("error", map[string]any{
			"code":    "PERSIST_FAILED",
			"message": "保存精修提案失败",
		}); emitErr != nil {
			log.Printf("[Code-Archdoc] 推送 SSE 持久化失败事件异常: %v", emitErr)
		}
		return
	}

	usage := resp.Usage
	if usage.LatencyMs <= 0 {
		usage.LatencyMs = latencyMs
	}
	s.recordUsageLog(ctx, prep, &usage, "success", "")

	// 依次推送 diff_proposal -> verifier_preview -> done
	if err := emit("diff_proposal", map[string]any{
		"proposal_id":   prep.AssistantMsg.ID,
		"slot_id":       prep.TargetSlot.SlotID,
		"before_md":     beforeMD,
		"after_md":      afterMD,
		"unified_diff":  unifiedDiff,
		"cited_anchors": alignedAnchors,
	}); err != nil {
		return
	}

	if err := emit("verifier_preview", previewVO); err != nil {
		return
	}

	if emitErr := emit("done", map[string]any{
		"message_id":           prep.AssistantMsg.ID,
		"prompt_tokens":        usage.PromptTokens,
		"cached_prompt_tokens": usage.CachedPromptTokens,
		"completion_tokens":    usage.CompletionTokens,
		"latency_ms":           usage.LatencyMs,
	}); emitErr != nil {
		log.Printf("[Code-Archdoc] 推送 SSE done 事件失败: %v", emitErr)
	}
}

// AcceptProposal 采纳 Chat Diff 提案：走统一 RevisionService.ApplySectionChange 校验乐观锁并保证非目标章节字节级不变，
// 标记旧 pending 提案为 superseded，并写入 F-26 采纳指标（chat_accepted=true）。
func (s *ChatService) AcceptProposal(
	ctx context.Context,
	docID, msgID uint,
	baseRevNo int,
	operatorID uint,
) (*DocumentDetailVO, *ChatProposalAuditSnapshot, error) {
	if baseRevNo <= 0 {
		return nil, nil, fmt.Errorf("%w: base_revision_no 必须为正整数", ErrInvalidChatParam)
	}

	msg, err := s.chatRepo.GetMessage(ctx, docID, msgID)
	if err != nil {
		return nil, nil, err
	}
	if msg.ProposalStatus != models.ProposalStatusPending {
		return nil, nil, ErrProposalNotPending
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, nil, err
	}

	detail, err := s.revSvc.ApplySectionChange(ctx, ApplySectionChangeInput{
		DocID:          docID,
		SlotID:         msg.TargetSlotID,
		BaseRevisionNo: baseRevNo,
		Markdown:       msg.ProposedAfterMD,
		ChangeSummary:  fmt.Sprintf("Chat 局部精修采纳章节 %s (提案 #%d)", msg.TargetSlotID, msg.ID),
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
	if supErr := s.chatRepo.SupersedePendingProposals(ctx, docID, msg.TargetSlotID, msg.ID); supErr != nil {
		log.Printf("[Code-Archdoc] 标记旧 pending 提案为 superseded 失败 (doc_id=%d, slot=%s): %v", docID, msg.TargetSlotID, supErr)
	}

	if s.govRepo != nil {
		accepted := true
		if mErr := s.govRepo.RecordQualityMetric(ctx, &models.QualityMetric{
			DocumentID:         doc.ID,
			RepoID:             doc.RepoID,
			DocTypeSlug:        doc.DocTypeSlug,
			DocTypeRevisionID:  doc.DocTypeRevisionID,
			MetricEvent:        models.MetricEventChatProposalResolved,
			ChatAccepted:       &accepted,
			SlotDiffRatiosJSON: datatypes.JSON(`{}`),
		}); mErr != nil {
			log.Printf("[Code-Archdoc] 记录 Chat 采纳指标失败 (doc_id=%d): %v", doc.ID, mErr)
		}
	}

	auditSnap := &ChatProposalAuditSnapshot{
		DocumentID:     docID,
		MessageID:      msg.ID,
		SlotID:         msg.TargetSlotID,
		BaseRevisionNo: baseRevNo,
		NewRevisionNo:  detail.CurrentRevisionNo,
		BeforeHash:     sha256Hex(msg.ProposedBeforeMD),
		AfterHash:      sha256Hex(msg.ProposedAfterMD),
	}
	return detail, auditSnap, nil
}

// RejectProposal 拒绝 Chat Diff 提案：仅更新提案状态并记录 F-26 指标（chat_accepted=false），文档修订零变更。
func (s *ChatService) RejectProposal(ctx context.Context, docID, msgID uint) (*ChatRejectResultVO, error) {
	msg, err := s.chatRepo.GetMessage(ctx, docID, msgID)
	if err != nil {
		return nil, err
	}
	if msg.ProposalStatus != models.ProposalStatusPending {
		return nil, ErrProposalNotPending
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	if err := s.chatRepo.UpdateProposalStatus(ctx, msg.ID, models.ProposalStatusRejected, nil); err != nil {
		return nil, err
	}

	if s.govRepo != nil {
		accepted := false
		if mErr := s.govRepo.RecordQualityMetric(ctx, &models.QualityMetric{
			DocumentID:         doc.ID,
			RepoID:             doc.RepoID,
			DocTypeSlug:        doc.DocTypeSlug,
			DocTypeRevisionID:  doc.DocTypeRevisionID,
			MetricEvent:        models.MetricEventChatProposalResolved,
			ChatAccepted:       &accepted,
			SlotDiffRatiosJSON: datatypes.JSON(`{}`),
		}); mErr != nil {
			log.Printf("[Code-Archdoc] 记录 Chat 拒绝指标失败 (doc_id=%d): %v", doc.ID, mErr)
		}
	}

	return &ChatRejectResultVO{
		MessageID:      msg.ID,
		ProposalStatus: models.ProposalStatusRejected,
	}, nil
}

func (s *ChatService) cleanupAbortedRefine(prep *PreparedChatRefine, errCode string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if delErr := s.chatRepo.DeleteMessage(bgCtx, prep.AssistantMsg.ID); delErr != nil {
		log.Printf("[Code-Archdoc] 清理中止精修占位消息(%d)失败: %v", prep.AssistantMsg.ID, delErr)
	}
	s.recordUsageLog(bgCtx, prep, &llm.UsageMetrics{ModelName: prep.Endpoint.ModelName}, "canceled", errCode)
}

func (s *ChatService) recordUsageLog(
	ctx context.Context,
	prep *PreparedChatRefine,
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
		SlotID:             prep.TargetSlot.SlotID,
		Stage:              "chat_refine",
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
		log.Printf("[Code-Archdoc] 记录 chat_refine LLM 用量日志失败 (doc_id=%d): %v", docID, logErr)
	}
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
