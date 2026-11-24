package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/drift"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/refdoc"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/verifier"
	commonModels "code-common/backend/models"
)

// RefDocRefineSelection 描述划词精修的全局 1-based 行区间与引文。
type RefDocRefineSelection struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	QuoteText string `json:"quote_text,omitempty"`
}

// RefineRefDocRequest 无状态参考文档 AI 精修或问答入参。
type RefineRefDocRequest struct {
	ScopeType      string                 `json:"scope_type"`              // selection | section | full
	Mode           string                 `json:"mode"`                    // revise | ask
	PresetAction   string                 `json:"preset_action,omitempty"` // polish | mermaid | refresh_summary
	Prompt         string                 `json:"prompt"`
	SectionID      string                 `json:"section_id,omitempty"`
	Selection      *RefDocRefineSelection `json:"selection,omitempty"`
	DraftContentMD string                 `json:"draft_content_md,omitempty"`
}

// RefDocRefineGateError 描述预检中触发硬阻断的门禁错误。
type RefDocRefineGateError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RefDocRefinePreflight 描述参考文档精修提案的专属双门禁联合预检报告（豁免 RunSymbolGate）。
type RefDocRefinePreflight struct {
	CanSave               bool                    `json:"can_save"`
	SecretConfirmRequired bool                    `json:"secret_confirm_required"`
	SecretIssues          []refdoc.GateFinding    `json:"secret_issues"`
	MermaidIssues         []verifier.MermaidIssue `json:"mermaid_issues"`
	GateError             *RefDocRefineGateError  `json:"gate_error,omitempty"`
}

// RefineRefDocResponse 无状态参考文档精修/问答响应体。
type RefineRefDocResponse struct {
	AnswerMD               string                `json:"answer_md"`
	Mode                   string                `json:"mode"`
	ScopeType              string                `json:"scope_type"`
	TargetSectionID        string                `json:"target_section_id,omitempty"`
	OriginalFragment       string                `json:"original_fragment"`
	ProposedFragment       string                `json:"proposed_fragment"`
	ProposedFullMarkdown   string                `json:"proposed_full_markdown"`
	UnifiedDiff            string                `json:"unified_diff"`
	Preflight              RefDocRefinePreflight `json:"preflight"`
	SuggestedSummaryMD     string                `json:"suggested_summary_md,omitempty"`
	SuggestedDeclaredTerms []string              `json:"suggested_declared_terms,omitempty"`
}

// RefDocVersionItemVO 描述单个历史/当前磁盘版本快照状态。
type RefDocVersionItemVO struct {
	VersionNo  int   `json:"version_no"`
	ByteSize   int64 `json:"byte_size"`
	IsCurrent  bool  `json:"is_current"`
	FileExists bool  `json:"file_exists"`
}

// RefDocVersionsResponse 历史版本枚举响应。
type RefDocVersionsResponse struct {
	DocID          uint                  `json:"doc_id"`
	CurrentVersion int                   `json:"current_version"`
	Versions       []RefDocVersionItemVO `json:"versions"`
}

// RefDocDiffResponse 两版本间 Unified Diff 响应。
type RefDocDiffResponse struct {
	DocID        uint   `json:"doc_id"`
	FromVersion  int    `json:"from_version"`
	ToVersion    int    `json:"to_version"`
	UnifiedDiff  string `json:"unified_diff"`
	AddedLines   int    `json:"added_lines"`
	RemovedLines int    `json:"removed_lines"`
}

// RefDocTokenBudgetExceededError 对应 HTTP 422 TOKEN_BUDGET_EXCEEDED。
type RefDocTokenBudgetExceededError struct {
	EstimatedTokens     int
	MaxFullRefineTokens int
}

func (e *RefDocTokenBudgetExceededError) Error() string {
	return fmt.Sprintf("全文精修预估 %d Tokens 超出单轮安全上限 %d Tokens，建议切换按节（section）或划词选区（selection）精修",
		e.EstimatedTokens, e.MaxFullRefineTokens)
}

// RefineRefDoc 执行参考文档无状态 AI 精修提案或问答，含动态草稿 TOC、只读防滥用、专属双门禁预检与用量记账。
func (s *RefDocService) RefineRefDoc(
	ctx context.Context,
	docID uint,
	req RefineRefDocRequest,
	user *commonModels.User,
) (*RefineRefDocResponse, error) {
	if user == nil || user.ID == 0 {
		return nil, ErrRefDocForbidden
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = refdoc.RefineModeRevise
	}
	if !refdoc.IsValidRefineMode(mode) {
		return nil, fmt.Errorf("%w: mode 必须为 revise 或 ask", ErrInvalidRefDocParam)
	}

	scopeType := strings.TrimSpace(req.ScopeType)
	if !refdoc.IsValidRefineScope(scopeType) {
		return nil, fmt.Errorf("%w: scope_type 必须为 selection、section 或 full", ErrInvalidRefDocParam)
	}

	presetAction := strings.TrimSpace(req.PresetAction)
	if !refdoc.IsValidRefinePreset(presetAction) {
		return nil, fmt.Errorf("%w: preset_action 必须为 polish、mermaid 或 refresh_summary", ErrInvalidRefDocParam)
	}

	userPrompt := strings.TrimSpace(req.Prompt)
	if userPrompt == "" {
		if presetAction == "" {
			return nil, fmt.Errorf("%w: prompt 与 preset_action 不可同时为空", ErrInvalidRefDocParam)
		}
		userPrompt = refdoc.DefaultRefinePresetPrompt(presetAction)
	}

	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	canEdit, err := s.checkDocOwnerWritePermission(ctx, user, doc)
	if err != nil {
		return nil, err
	}
	if mode == refdoc.RefineModeRevise && !canEdit {
		return nil, ErrRefDocForbidden
	}

	// 只读用户发起 ask 时强制忽略 draft_content_md，仅基于磁盘已落盘版作答
	var baseMD string
	if canEdit && strings.TrimSpace(req.DraftContentMD) != "" {
		if !utf8.ValidString(req.DraftContentMD) {
			return nil, fmt.Errorf("%w: draft_content_md 必须为合法 UTF-8 文本", ErrInvalidRefDocParam)
		}
		normDraft := strings.ReplaceAll(req.DraftContentMD, "\r\n", "\n")
		if int64(len(normDraft)) > s.maxBytes {
			return nil, &RefDocGateError{
				Failure: &refdoc.GateFailure{
					HTTPStatus: http.StatusRequestEntityTooLarge,
					Code:       "PAYLOAD_TOO_LARGE",
					Message:    fmt.Sprintf("草稿正文体积 %d 字节超出上限 %d 字节", len(normDraft), s.maxBytes),
					Details: map[string]any{
						"max_bytes":    s.maxBytes,
						"actual_bytes": len(normDraft),
					},
				},
			}
		}
		baseMD = normDraft
	} else {
		diskMD, readErr := s.ReadVersionContent(doc.ID, doc.CurrentVersion)
		if readErr != nil {
			return nil, readErr
		}
		baseMD = strings.ReplaceAll(diskMD, "\r\n", "\n")
	}

	toc := refdoc.ParseTOC(baseMD)
	lines := strings.Split(baseMD, "\n")
	lineCount := len(lines)
	if lineCount == 0 {
		return nil, fmt.Errorf("%w: 参考文档正文为空", ErrInvalidRefDocParam)
	}

	startLine, endLine := 1, lineCount
	targetSectionID := ""
	var targetSection *refdoc.HeadingNode
	var selRange *refdoc.RefineSelectionRange

	switch scopeType {
	case refdoc.RefineScopeSection:
		secID := strings.TrimSpace(req.SectionID)
		if secID == "" {
			return nil, fmt.Errorf("%w: section 作用域必须指定 section_id", ErrInvalidRefDocParam)
		}
		if secID == "__preamble__" && len(toc) > 0 && toc[0].StartLine > 1 {
			pre := refdoc.HeadingNode{
				SectionID: "__preamble__",
				Title:     "文档导言",
				Level:     1,
				StartLine: 1,
				EndLine:   toc[0].StartLine - 1,
			}
			targetSection = &pre
		} else {
			for i := range toc {
				if toc[i].SectionID == secID {
					nodeCopy := toc[i]
					targetSection = &nodeCopy
					break
				}
			}
		}
		if targetSection == nil {
			return nil, fmt.Errorf("%w: 未在当前文档目录中找到章节 %q", ErrInvalidRefDocParam, secID)
		}
		targetSectionID = targetSection.SectionID
		startLine = max(1, targetSection.StartLine)
		endLine = min(lineCount, targetSection.EndLine)
		if startLine > endLine {
			return nil, fmt.Errorf("%w: 章节 %q 行区间异常", ErrInvalidRefDocParam, secID)
		}
	case refdoc.RefineScopeSelection:
		if req.Selection == nil {
			return nil, fmt.Errorf("%w: selection 作用域必须提供行区间", ErrInvalidRefDocParam)
		}
		if req.Selection.StartLine < 1 || req.Selection.EndLine < req.Selection.StartLine || req.Selection.EndLine > lineCount {
			return nil, fmt.Errorf("%w: 选区行号 [%d, %d] 超出正文范围 [1, %d]",
				ErrInvalidRefDocParam, req.Selection.StartLine, req.Selection.EndLine, lineCount)
		}
		startLine = req.Selection.StartLine
		endLine = req.Selection.EndLine
		selRange = &refdoc.RefineSelectionRange{
			StartLine: startLine,
			EndLine:   endLine,
			QuoteText: strings.TrimSpace(req.Selection.QuoteText),
		}
		if len(toc) > 0 && startLine < toc[0].StartLine {
			targetSectionID = "__preamble__"
		} else {
			for i := range toc {
				if startLine >= toc[i].StartLine && startLine <= toc[i].EndLine {
					nodeCopy := toc[i]
					targetSection = &nodeCopy
					targetSectionID = nodeCopy.SectionID
					break
				}
			}
		}
	case refdoc.RefineScopeFull:
		startLine = 1
		endLine = lineCount
	}

	originalFragment := strings.Join(lines[startLine-1:endLine], "\n")

	if s.llmClient == nil || s.govRepo == nil {
		return nil, ErrChatLLMNotConfigured
	}
	ep, cfg, err := ResolveLLMEndpoint(ctx, s.govRepo, "")
	if err != nil {
		return nil, err
	}

	if scopeType == refdoc.RefineScopeFull && mode == refdoc.RefineModeRevise && presetAction != refdoc.RefinePresetRefreshSummary {
		dynamicLimit := min(max(cfg.MaxTokensPerTask/2, 4000), refdoc.DefaultFullRefineMaxTokens)
		estTokens, effLimit, exceeded := refdoc.CheckFullRefineBudget(baseMD, dynamicLimit)
		if exceeded {
			return nil, &RefDocTokenBudgetExceededError{
				EstimatedTokens:     estTokens,
				MaxFullRefineTokens: effLimit,
			}
		}
	}

	var currentTerms []string
	if len(doc.DeclaredTerms) > 0 {
		if unmarshalErr := json.Unmarshal(doc.DeclaredTerms, &currentTerms); unmarshalErr != nil {
			currentTerms = nil
		}
	}

	promptMsgs, _, err := refdoc.BuildRefinePrompt(refdoc.RefinePromptInput{
		Title:           doc.Title,
		DocKind:         doc.DocKind,
		TOC:             toc,
		FullMarkdown:    baseMD,
		ScopeType:       scopeType,
		Mode:            mode,
		PresetAction:    presetAction,
		UserPrompt:      userPrompt,
		TargetSection:   targetSection,
		TargetFragment:  originalFragment,
		Selection:       selRange,
		CurrentTerms:    currentTerms,
		MaxContextBytes: s.maxBytes,
	})
	if err != nil {
		return nil, err
	}

	llmMsgs := make([]llm.Message, 0, len(promptMsgs))
	for _, m := range promptMsgs {
		llmMsgs = append(llmMsgs, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	chatResp, llmErr := s.llmClient.ChatCompletion(ctx, ep, llm.ChatRequest{
		Messages: llmMsgs,
	})

	s.recordRefDocLLMUsage(ctx, doc, mode, user.ID, DisplayModelLabel(cfg), chatResp, llmErr)
	if llmErr != nil {
		return nil, llmErr
	}

	parsedOut, err := refdoc.ParseRefineLLMOutput(chatResp.Content, mode, presetAction)
	if err != nil {
		return nil, err
	}

	if mode == refdoc.RefineModeAsk {
		return &RefineRefDocResponse{
			AnswerMD:             parsedOut.AnswerMD,
			Mode:                 mode,
			ScopeType:            scopeType,
			TargetSectionID:      targetSectionID,
			OriginalFragment:     originalFragment,
			ProposedFragment:     "",
			ProposedFullMarkdown: baseMD,
			UnifiedDiff:          "",
			Preflight: RefDocRefinePreflight{
				CanSave:       true,
				SecretIssues:  []refdoc.GateFinding{},
				MermaidIssues: []verifier.MermaidIssue{},
			},
		}, nil
	}

	if presetAction == refdoc.RefinePresetRefreshSummary {
		return &RefineRefDocResponse{
			AnswerMD:             parsedOut.AnswerMD,
			Mode:                 mode,
			ScopeType:            scopeType,
			TargetSectionID:      targetSectionID,
			OriginalFragment:     originalFragment,
			ProposedFragment:     "",
			ProposedFullMarkdown: baseMD,
			UnifiedDiff:          "",
			Preflight: RefDocRefinePreflight{
				CanSave:       true,
				SecretIssues:  []refdoc.GateFinding{},
				MermaidIssues: []verifier.MermaidIssue{},
			},
			SuggestedSummaryMD:     clampSummaryRunes(parsedOut.SuggestedSummaryMD, 300),
			SuggestedDeclaredTerms: parsedOut.SuggestedDeclaredTerms,
		}, nil
	}

	proposedFragment := strings.ReplaceAll(parsedOut.RevisedMarkdown, "\r\n", "\n")
	if strings.TrimSpace(proposedFragment) == "" {
		proposedFragment = originalFragment
	}

	var proposedFullMD string
	if scopeType == refdoc.RefineScopeFull {
		proposedFullMD = proposedFragment
	} else {
		proposedFullMD, err = refdoc.ApplyLineRangeReplacement(baseMD, startLine, endLine, proposedFragment)
		if err != nil {
			return nil, err
		}
	}

	unifiedDiff, _, _ := drift.ComputeUnifiedDiff(baseMD, proposedFullMD)
	slotLabel := targetSectionID
	if slotLabel == "" {
		slotLabel = scopeType
	}
	preflight := runRefinePreflight(slotLabel, proposedFullMD, s.maxBytes, doc.DocKind)

	return &RefineRefDocResponse{
		AnswerMD:               parsedOut.AnswerMD,
		Mode:                   mode,
		ScopeType:              scopeType,
		TargetSectionID:        targetSectionID,
		OriginalFragment:       originalFragment,
		ProposedFragment:       proposedFragment,
		ProposedFullMarkdown:   proposedFullMD,
		UnifiedDiff:            unifiedDiff,
		Preflight:              preflight,
		SuggestedSummaryMD:     clampSummaryRunes(parsedOut.SuggestedSummaryMD, 300),
		SuggestedDeclaredTerms: parsedOut.SuggestedDeclaredTerms,
	}, nil
}

// ListRefDocVersions 倒序枚举 current_version..1 的磁盘版本文件状态。
func (s *RefDocService) ListRefDocVersions(ctx context.Context, docID uint) (*RefDocVersionsResponse, error) {
	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	versions := make([]RefDocVersionItemVO, 0, max(doc.CurrentVersion, 0))
	for v := doc.CurrentVersion; v >= 1; v-- {
		relFile := fmt.Sprintf("%d/v%d.md", doc.ID, v)
		var byteSize int64
		fileExists := false
		if fullPath, joinErr := sandbox.SafeJoin(s.refDocsDir, relFile); joinErr == nil {
			if st, statErr := os.Stat(fullPath); statErr == nil && !st.IsDir() {
				fileExists = true
				byteSize = st.Size()
			}
		}
		versions = append(versions, RefDocVersionItemVO{
			VersionNo:  v,
			ByteSize:   byteSize,
			IsCurrent:  v == doc.CurrentVersion,
			FileExists: fileExists,
		})
	}

	return &RefDocVersionsResponse{
		DocID:          doc.ID,
		CurrentVersion: doc.CurrentVersion,
		Versions:       versions,
	}, nil
}

// DiffRefDocVersions 计算同一参考文档任意两历史/当前磁盘版本间的 Unified Diff。
func (s *RefDocService) DiffRefDocVersions(ctx context.Context, docID uint, fromVer, toVer int) (*RefDocDiffResponse, error) {
	doc, err := s.refDocRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentVersion <= 0 {
		return nil, models.ErrNotFound
	}

	if toVer == 0 {
		toVer = doc.CurrentVersion
	}
	if fromVer == 0 {
		fromVer = max(1, toVer-1)
	}
	if fromVer <= 0 || toVer <= 0 || fromVer > doc.CurrentVersion || toVer > doc.CurrentVersion {
		return nil, fmt.Errorf("%w: 版本号必须在 1..%d 之间", ErrInvalidRefDocParam, doc.CurrentVersion)
	}

	fromMD, err := s.ReadVersionContent(doc.ID, fromVer)
	if err != nil {
		return nil, err
	}
	toMD, err := s.ReadVersionContent(doc.ID, toVer)
	if err != nil {
		return nil, err
	}

	udiff, added, removed := drift.ComputeUnifiedDiff(fromMD, toMD)
	return &RefDocDiffResponse{
		DocID:        doc.ID,
		FromVersion:  fromVer,
		ToVersion:    toVer,
		UnifiedDiff:  udiff,
		AddedLines:   added,
		RemovedLines: removed,
	}, nil
}

// runRefinePreflight 独立跑完参考文档专用双门禁（ValidateAndParse + RunMermaidGate），明确豁免 RunSymbolGate。
func runRefinePreflight(slotLabel, proposedFullMD string, maxBytes int64, docKind string) RefDocRefinePreflight {
	pf := RefDocRefinePreflight{
		CanSave:       true,
		SecretIssues:  []refdoc.GateFinding{},
		MermaidIssues: []verifier.MermaidIssue{},
	}

	_, gateFail, parseErr := refdoc.ValidateAndParse(proposedFullMD, maxBytes, false, docKind, nil)
	if parseErr != nil {
		pf.CanSave = false
		pf.GateError = &RefDocRefineGateError{
			Code:    "INTERNAL_ERROR",
			Message: parseErr.Error(),
		}
	} else if gateFail != nil {
		if rawFindings, ok := gateFail.Details["findings"]; ok {
			if typed, okTyped := rawFindings.([]refdoc.GateFinding); okTyped {
				pf.SecretIssues = typed
			}
		}
		if gateFail.Code == "SECRET_CONFIRM_REQUIRED" {
			pf.SecretConfirmRequired = true
		} else {
			pf.CanSave = false
			pf.GateError = &RefDocRefineGateError{
				Code:    gateFail.Code,
				Message: gateFail.Message,
			}
		}
	}

	// 即使凭据门禁命中也继续完成 Mermaid 语法校验，一次返回完整预检明细
	mermaidPolicy := (&doctype.VerifyPolicy{}).Effective()
	if mIssues := verifier.RunMermaidGate(slotLabel, proposedFullMD, mermaidPolicy); len(mIssues) > 0 {
		pf.MermaidIssues = mIssues
		for _, issue := range mIssues {
			if issue.Severity == "error" {
				pf.CanSave = false
				break
			}
		}
	}

	return pf
}

func (s *RefDocService) recordRefDocLLMUsage(
	ctx context.Context,
	doc *models.ReferenceDoc,
	mode string,
	operatorID uint,
	defaultModelLabel string,
	chatResp *llm.ChatResponse,
	llmErr error,
) {
	if s.govRepo == nil || doc == nil {
		return
	}
	stage := "ref_doc_refine"
	if mode == refdoc.RefineModeAsk {
		stage = "ref_doc_ask"
	}
	var repoID uint
	if doc.OwnerRepoID != nil {
		repoID = *doc.OwnerRepoID
	}

	status := "ok"
	errCode := ""
	if llmErr != nil {
		status = "failed"
		var httpErr *llm.HTTPStatusError
		if errors.As(llmErr, &httpErr) {
			errCode = fmt.Sprintf("HTTP_%d", httpErr.StatusCode)
		} else {
			errCode = "LLM_ERROR"
		}
	}

	modelName := defaultModelLabel
	var promptTokens, cachedTokens, compTokens int
	var latencyMs int64
	if chatResp != nil {
		if strings.TrimSpace(chatResp.Usage.ModelName) != "" {
			modelName = chatResp.Usage.ModelName
		}
		promptTokens = chatResp.Usage.PromptTokens
		cachedTokens = chatResp.Usage.CachedPromptTokens
		compTokens = chatResp.Usage.CompletionTokens
		latencyMs = chatResp.Usage.LatencyMs
	}

	if logErr := s.govRepo.RecordLLMUsage(ctx, &models.LLMUsageLog{
		TaskID:             nil,
		DocumentID:         nil,
		RepoID:             repoID,
		SlotID:             fmt.Sprintf("ref_doc:%d", doc.ID),
		Engine:             models.EngineNative,
		Stage:              stage,
		ModelName:          modelName,
		PromptTokens:       promptTokens,
		CachedPromptTokens: cachedTokens,
		CompletionTokens:   compTokens,
		LatencyMs:          latencyMs,
		Status:             status,
		ErrorCode:          errCode,
		OperatorID:         operatorID,
	}); logErr != nil {
		log.Printf("[%s] 记录 %s LLM 用量日志失败 (ref_doc_id=%d): %v", config.ServiceName, stage, doc.ID, logErr)
	}
}

func clampSummaryRunes(summary string, maxRunes int) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" || maxRunes <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return trimmed
	}
	if maxRunes > 3 {
		return string(runes[:maxRunes-3]) + "..."
	}
	return string(runes[:maxRunes])
}
