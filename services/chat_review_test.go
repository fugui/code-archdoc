package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

// ─── 离线内存仓储 Stub（无需 PostgreSQL 即可全量验证 ADR-06 编排与状态机） ───

type stubDocRepo struct {
	mu      sync.Mutex
	doc     *models.Document
	revs    map[int]*models.DocumentRevision
	evsByNo map[int][]models.SectionEvidence
	nextRev uint
}

func (r *stubDocRepo) GetByID(_ context.Context, id uint) (*models.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.doc == nil || r.doc.ID != id {
		return nil, models.ErrNotFound
	}
	cp := *r.doc
	return &cp, nil
}
func (r *stubDocRepo) GetOrCreateDocument(_ context.Context, d *models.Document) (*models.Document, error) {
	return d, nil
}
func (r *stubDocRepo) GetByScope(context.Context, uint, string, string, string) (*models.Document, error) {
	return nil, models.ErrNotFound
}
func (r *stubDocRepo) ListByScope(context.Context, uint, string, string, bool) ([]models.Document, error) {
	return nil, nil
}
func (r *stubDocRepo) ListByRepos(context.Context, []uint) ([]models.Document, error) {
	return nil, nil
}
func (r *stubDocRepo) ListRevisionsByIDs(context.Context, []uint) (map[uint]models.DocumentRevision, error) {
	return nil, nil
}
func (r *stubDocRepo) ListApprovedDocuments(context.Context) ([]models.Document, error) {
	return nil, nil
}
func (r *stubDocRepo) ListApprovedDocumentsByRepo(context.Context, uint) ([]models.Document, error) {
	return nil, nil
}
func (r *stubDocRepo) ListByIDs(context.Context, []uint, bool) ([]models.Document, error) {
	return nil, nil
}
func (r *stubDocRepo) GetRevisionWithSections(_ context.Context, _ uint, revisionNo int) (*models.DocumentRevision, []models.SectionEvidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rev, ok := r.revs[revisionNo]
	if !ok {
		return nil, nil, models.ErrNotFound
	}
	cp := *rev
	evs := append([]models.SectionEvidence(nil), r.evsByNo[revisionNo]...)
	return &cp, evs, nil
}
func (r *stubDocRepo) GetRevisionByIDWithSections(_ context.Context, _ uint, revID uint) (*models.DocumentRevision, []models.SectionEvidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for no, rev := range r.revs {
		if rev.ID == revID {
			cp := *rev
			return &cp, append([]models.SectionEvidence(nil), r.evsByNo[no]...), nil
		}
	}
	return nil, nil, models.ErrNotFound
}
func (r *stubDocRepo) GetFirstAIGenerateRevision(context.Context, uint) (*models.DocumentRevision, error) {
	return nil, models.ErrNotFound
}
func (r *stubDocRepo) ListRevisions(context.Context, uint) ([]models.DocumentRevision, error) {
	return nil, nil
}
func (r *stubDocRepo) AppendRevisionWithOptimisticLock(_ context.Context, doc *models.Document, baseRevNo int, rev *models.DocumentRevision, secs []models.SectionEvidence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.doc.CurrentRevisionNo != baseRevNo {
		return models.ErrOptimisticLockConflict
	}
	r.nextRev++
	newNo := baseRevNo + 1
	rev.ID = r.nextRev
	rev.DocumentID = doc.ID
	rev.RevisionNo = newNo
	cpRev := *rev
	r.revs[newNo] = &cpRev
	r.evsByNo[newNo] = append([]models.SectionEvidence(nil), secs...)
	r.doc.CurrentRevisionNo = newNo
	idCopy := cpRev.ID
	r.doc.CurrentRevisionID = &idCopy
	doc.CurrentRevisionNo = newNo
	doc.CurrentRevisionID = &idCopy
	return nil
}
func (r *stubDocRepo) UpdateSectionOwnership(context.Context, uint, uint, string, string, bool) error {
	return nil
}
func (r *stubDocRepo) MarkPublished(context.Context, uint, uint, string, string, uint, time.Time) error {
	return nil
}
func (r *stubDocRepo) MarkUnpublished(context.Context, uint) error { return nil }
func (r *stubDocRepo) MigrateScopeDocuments(context.Context, uint, string, string, func(string, string) string) ([]models.Document, error) {
	return nil, nil
}

type stubDocTypeRepo struct {
	dt  *models.DocType
	rev *models.DocTypeRevision
}

func (r *stubDocTypeRepo) List(context.Context, bool) ([]models.DocType, error) { return nil, nil }
func (r *stubDocTypeRepo) GetBySlug(context.Context, string) (*models.DocType, *models.DocTypeRevision, error) {
	return r.dt, r.rev, nil
}
func (r *stubDocTypeRepo) ListRevisions(context.Context, string) ([]models.DocTypeRevision, error) {
	return nil, nil
}
func (r *stubDocTypeRepo) GetRevisionByID(_ context.Context, id uint) (*models.DocTypeRevision, error) {
	if r.rev != nil && r.rev.ID == id {
		return r.rev, nil
	}
	return nil, models.ErrNotFound
}
func (r *stubDocTypeRepo) UpsertFromDirectorySync(context.Context, *models.DocType, *models.DocTypeRevision) (bool, error) {
	return false, nil
}
func (r *stubDocTypeRepo) CountApprovedDocumentsBySlug(context.Context, string) (int64, error) {
	return 0, nil
}
func (r *stubDocTypeRepo) ListByPreferredModel(context.Context, string) ([]models.DocType, error) {
	return nil, nil
}
func (r *stubDocTypeRepo) DeleteIfUnused(context.Context, string) error { return nil }

type stubGovRepo struct {
	baseURL string
	metrics []models.QualityMetric
}

func (r *stubGovRepo) ListLLMConfigs(context.Context) ([]models.LLMConfig, error) { return nil, nil }
func (r *stubGovRepo) GetLLMConfigByID(context.Context, uint) (*models.LLMConfig, error) {
	return nil, models.ErrNotFound
}
func (r *stubGovRepo) GetLLMConfigByName(_ context.Context, name string) (*models.LLMConfig, error) {
	return &models.LLMConfig{
		ID:             1,
		Name:           name,
		BaseURL:        r.baseURL,
		ModelName:      "mock-model",
		TimeoutSeconds: 10,
		IsActive:       true,
	}, nil
}
func (r *stubGovRepo) GetDefaultLLMConfig(context.Context) (*models.LLMConfig, error) {
	return &models.LLMConfig{
		ID:             1,
		Name:           "DeepSeek-V3",
		BaseURL:        r.baseURL,
		ModelName:      "mock-model",
		TimeoutSeconds: 10,
		IsActive:       true,
		IsDefault:      true,
	}, nil
}
func (r *stubGovRepo) CreateLLMConfig(context.Context, *models.LLMConfig) error { return nil }
func (r *stubGovRepo) UpdateLLMConfig(context.Context, *models.LLMConfig) error { return nil }
func (r *stubGovRepo) SetDefaultLLMConfig(context.Context, uint) (*models.LLMConfig, error) {
	return nil, nil
}
func (r *stubGovRepo) DeleteLLMConfig(context.Context, uint) error { return nil }
func (r *stubGovRepo) UpdateLLMProbeResult(context.Context, uint, string, int64, time.Time) error {
	return nil
}
func (r *stubGovRepo) RecordLLMUsage(context.Context, *models.LLMUsageLog) error { return nil }
func (r *stubGovRepo) RecordQualityMetric(_ context.Context, m *models.QualityMetric) error {
	r.metrics = append(r.metrics, *m)
	return nil
}
func (r *stubGovRepo) ListQualityMetrics(context.Context, models.MetricsFilter) ([]models.QualityMetric, error) {
	return r.metrics, nil
}
func (r *stubGovRepo) ListLLMUsageLogs(context.Context, models.MetricsFilter) ([]models.LLMUsageLog, error) {
	return nil, nil
}
func (r *stubGovRepo) ListSectionVerifierSummaries(context.Context, string, *uint, time.Time) ([]models.SectionVerifierMetricRow, error) {
	return nil, nil
}

type stubChatRepo struct {
	mu       sync.Mutex
	nextID   uint
	sessions map[uint]*models.ChatSession
	msgs     map[uint]*models.ChatMessage
	items    map[uint]*models.ChatProposalItem
}

func newStubChatRepo() *stubChatRepo {
	return &stubChatRepo{
		sessions: make(map[uint]*models.ChatSession),
		msgs:     make(map[uint]*models.ChatMessage),
		items:    make(map[uint]*models.ChatProposalItem),
	}
}

func (r *stubChatRepo) GetOrCreateSession(_ context.Context, docID uint, slotID string, userID uint, sessionID *uint) (*models.ChatSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sessionID != nil && *sessionID > 0 {
		if s, ok := r.sessions[*sessionID]; ok {
			cp := *s
			return &cp, nil
		}
	}
	r.nextID++
	s := &models.ChatSession{ID: r.nextID, DocumentID: docID, TargetSlotID: slotID, CreatedBy: userID}
	r.sessions[s.ID] = s
	cp := *s
	return &cp, nil
}
func (r *stubChatRepo) ListSessionsWithMessages(_ context.Context, docID uint) ([]models.ChatSession, map[uint][]models.ChatMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sess []models.ChatSession
	msgMap := make(map[uint][]models.ChatMessage)
	for _, s := range r.sessions {
		if s.DocumentID == docID {
			sess = append(sess, *s)
		}
	}
	for _, m := range r.msgs {
		msgMap[m.SessionID] = append(msgMap[m.SessionID], *m)
	}
	return sess, msgMap, nil
}
func (r *stubChatRepo) CreateMessage(_ context.Context, msg *models.ChatMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	msg.ID = r.nextID
	cp := *msg
	r.msgs[cp.ID] = &cp
	return nil
}
func (r *stubChatRepo) UpdateMessage(_ context.Context, msg *models.ChatMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *msg
	r.msgs[cp.ID] = &cp
	return nil
}
func (r *stubChatRepo) DeleteMessage(_ context.Context, msgID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.msgs, msgID)
	return nil
}
func (r *stubChatRepo) TouchSession(_ context.Context, sessID uint, title string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[sessID]; ok && title != "" {
		s.Title = title
	}
	return nil
}
func (r *stubChatRepo) GetMessage(_ context.Context, _ uint, msgID uint) (*models.ChatMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.msgs[msgID]
	if !ok {
		return nil, models.ErrNotFound
	}
	cp := *m
	return &cp, nil
}
func (r *stubChatRepo) UpdateProposalStatus(_ context.Context, msgID uint, status string, revID *uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.msgs[msgID]; ok {
		m.ProposalStatus = status
		if revID != nil {
			v := *revID
			m.AppliedRevisionID = &v
		}
	}
	return nil
}
func (r *stubChatRepo) SupersedePendingProposals(context.Context, uint, string, uint) error {
	return nil
}
func (r *stubChatRepo) CreateProposalItem(_ context.Context, item *models.ChatProposalItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	item.ID = r.nextID
	cp := *item
	r.items[cp.ID] = &cp
	return nil
}
func (r *stubChatRepo) ListProposalItemsByMessageIDs(_ context.Context, msgIDs []uint) (map[uint][]models.ChatProposalItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := make(map[uint]bool, len(msgIDs))
	for _, id := range msgIDs {
		set[id] = true
	}
	out := make(map[uint][]models.ChatProposalItem)
	for _, it := range r.items {
		if set[it.MessageID] {
			out[it.MessageID] = append(out[it.MessageID], *it)
		}
	}
	return out, nil
}
func (r *stubChatRepo) ListProposalItemsByMessage(_ context.Context, _ uint, msgID uint) ([]models.ChatProposalItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []models.ChatProposalItem
	for _, it := range r.items {
		if it.MessageID == msgID {
			out = append(out, *it)
		}
	}
	return out, nil
}
func (r *stubChatRepo) GetProposalItem(_ context.Context, _ uint, itemID uint) (*models.ChatProposalItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[itemID]
	if !ok {
		return nil, models.ErrNotFound
	}
	cp := *it
	return &cp, nil
}
func (r *stubChatRepo) UpdateProposalItem(_ context.Context, item *models.ChatProposalItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *item
	r.items[cp.ID] = &cp
	return nil
}
func (r *stubChatRepo) DeleteProposalItemsByMessage(_ context.Context, msgID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, it := range r.items {
		if it.MessageID == msgID {
			delete(r.items, id)
		}
	}
	return nil
}
func (r *stubChatRepo) SupersedePendingProposalsBySlots(_ context.Context, docID uint, slotIDs []string, exceptMsgID uint) ([]uint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slotSet := make(map[string]bool, len(slotIDs))
	for _, s := range slotIDs {
		slotSet[s] = true
	}
	var affected []uint
	touchedMsg := make(map[uint]bool)
	for _, it := range r.items {
		if it.DocumentID == docID && it.MessageID != exceptMsgID && it.Status == models.ProposalStatusPending && slotSet[it.SlotID] {
			it.Status = models.ProposalStatusSuperseded
			touchedMsg[it.MessageID] = true
			var cids []uint
			_ = json.Unmarshal(it.AddressedCommentIDsJSON, &cids)
			affected = append(affected, cids...)
		}
	}
	for mid := range touchedMsg {
		hasPending := false
		for _, it := range r.items {
			if it.MessageID == mid && it.Status == models.ProposalStatusPending {
				hasPending = true
			}
		}
		if !hasPending && r.msgs[mid] != nil {
			r.msgs[mid].ProposalStatus = models.ProposalStatusSuperseded
		}
	}
	return affected, nil
}

type stubReviewRepo struct {
	mu       sync.Mutex
	comments map[uint]*models.ReviewComment
}

func newStubReviewRepo(seed ...models.ReviewComment) *stubReviewRepo {
	m := make(map[uint]*models.ReviewComment, len(seed))
	for _, c := range seed {
		cp := c
		m[cp.ID] = &cp
	}
	return &stubReviewRepo{comments: m}
}

func (r *stubReviewRepo) Create(_ context.Context, c *models.ReviewComment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.comments[cp.ID] = &cp
	return nil
}
func (r *stubReviewRepo) ListByDocument(_ context.Context, docID uint, _ models.ReviewCommentFilter) ([]models.ReviewComment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []models.ReviewComment
	for _, c := range r.comments {
		if c.DocumentID == docID {
			out = append(out, *c)
		}
	}
	return out, nil
}
func (r *stubReviewRepo) GetByID(_ context.Context, docID, commentID uint) (*models.ReviewComment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.comments[commentID]
	if !ok || c.DocumentID != docID {
		return nil, models.ErrNotFound
	}
	cp := *c
	return &cp, nil
}
func (r *stubReviewRepo) ListByIDs(_ context.Context, docID uint, ids []uint) ([]models.ReviewComment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []models.ReviewComment
	for _, id := range ids {
		if c, ok := r.comments[id]; ok && c.DocumentID == docID {
			out = append(out, *c)
		}
	}
	return out, nil
}
func (r *stubReviewRepo) Update(_ context.Context, c *models.ReviewComment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.comments[cp.ID] = &cp
	return nil
}
func (r *stubReviewRepo) DeleteDraft(context.Context, uint, uint) error { return nil }
func (r *stubReviewRepo) MarkProposed(_ context.Context, docID uint, ids []uint, msgID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if c, ok := r.comments[id]; ok && c.DocumentID == docID && c.Status == models.ReviewStatusOpen {
			c.Status = models.ReviewStatusProposed
			mid := msgID
			c.ProposalGroupMsgID = &mid
		}
	}
	return nil
}
func (r *stubReviewRepo) MarkResolved(_ context.Context, docID uint, ids []uint, revID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if c, ok := r.comments[id]; ok && c.DocumentID == docID {
			c.Status = models.ReviewStatusResolved
			rid := revID
			c.ResolvedRevisionID = &rid
		}
	}
	return nil
}
func (r *stubReviewRepo) RevertProposedByMessageID(_ context.Context, docID, msgID uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.comments {
		if c.DocumentID == docID && c.Status == models.ReviewStatusProposed && c.ProposalGroupMsgID != nil && *c.ProposalGroupMsgID == msgID {
			c.Status = models.ReviewStatusOpen
			c.ProposalGroupMsgID = nil
		}
	}
	return nil
}
func (r *stubReviewRepo) RevertProposedByCommentIDs(_ context.Context, docID uint, ids []uint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if c, ok := r.comments[id]; ok && c.DocumentID == docID && c.Status == models.ReviewStatusProposed {
			c.Status = models.ReviewStatusOpen
			c.ProposalGroupMsgID = nil
		}
	}
	return nil
}
func (r *stubReviewRepo) ListPersistentByDocument(_ context.Context, docID uint) ([]models.ReviewComment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []models.ReviewComment
	for _, c := range r.comments {
		if c.DocumentID == docID && c.PersistForRegen && c.Status != models.ReviewStatusDismissed {
			out = append(out, *c)
		}
	}
	return out, nil
}

type chatReviewHarness struct {
	chatSvc    *ChatService
	revSvc     *RevisionService
	docRepo    *stubDocRepo
	chatRepo   *stubChatRepo
	reviewRepo *stubReviewRepo
	govRepo    *stubGovRepo
}

func newChatReviewHarness(llmURL string, seedComments ...models.ReviewComment) *chatReviewHarness {
	meta := doctype.Meta{
		Slots: []doctype.SlotSpec{
			{SlotID: "sec_locked", Title: "锁定章节", Ownership: models.OwnershipLocked},
			{SlotID: "sec_auto", Title: "核心架构", Ownership: models.OwnershipAuto},
			{SlotID: "sec_hybrid", Title: "调用链路", Ownership: models.OwnershipHybrid},
		},
	}
	metaJSON, _ := json.Marshal(meta)
	snaps := []models.DocumentSectionSnapshot{
		{SlotID: "sec_locked", SectionOrder: 1, Title: "锁定章节", Ownership: models.OwnershipLocked, Markdown: "Locked original content", ContentSHA256: sha256Hex("Locked original content")},
		{SlotID: "sec_auto", SectionOrder: 2, Title: "核心架构", Ownership: models.OwnershipAuto, Markdown: "Auto original content", ContentSHA256: sha256Hex("Auto original content")},
		{SlotID: "sec_hybrid", SectionOrder: 3, Title: "调用链路", Ownership: models.OwnershipHybrid, Markdown: "Hybrid original content", ContentSHA256: sha256Hex("Hybrid original content")},
	}
	snapsJSON, _ := json.Marshal(snaps)

	nonInlineAnc, _ := json.Marshal([]models.EvidenceAnchor{
		{AnchorID: "a1", Scope: "repo", FilePath: "orders", SymbolKind: models.SymbolKindModuleDoc, FQSymbol: "orders::system-architecture"},
	})
	initEvs := []models.SectionEvidence{
		{SlotID: "sec_locked", Ownership: models.OwnershipLocked, EvidencesJSON: datatypes.JSON(nonInlineAnc)},
		{SlotID: "sec_auto", Ownership: models.OwnershipAuto, EvidencesJSON: datatypes.JSON("[]")},
		{SlotID: "sec_hybrid", Ownership: models.OwnershipHybrid, EvidencesJSON: datatypes.JSON(nonInlineAnc)},
	}

	revID := uint(1)
	docRepo := &stubDocRepo{
		nextRev: 1,
		doc: &models.Document{
			ID:                10,
			RepoID:            1,
			DocTypeSlug:       "system-architecture",
			DocTypeRevisionID: 1,
			CurrentRevisionNo: 1,
			CurrentRevisionID: &revID,
		},
		revs: map[int]*models.DocumentRevision{
			1: {ID: 1, DocumentID: 10, RevisionNo: 1, CommitSHA: strings.Repeat("1", 40), DocTypeRevisionID: 1, SectionsJSON: datatypes.JSON(snapsJSON)},
		},
		evsByNo: map[int][]models.SectionEvidence{1: initEvs},
	}
	dtRepo := &stubDocTypeRepo{
		dt:  &models.DocType{Slug: "system-architecture", DisplayName: "系统架构"},
		rev: &models.DocTypeRevision{ID: 1, MetaJSON: datatypes.JSON(metaJSON), TemplateMD: "# Doc"},
	}
	govRepo := &stubGovRepo{baseURL: llmURL}
	chatRepo := newStubChatRepo()
	reviewRepo := newStubReviewRepo(seedComments...)
	revSvc := NewRevisionService(docRepo, dtRepo, nil, nil, nil).WithReviewHooks(chatRepo, reviewRepo)
	chatSvc := NewChatService(chatRepo, docRepo, dtRepo, nil, govRepo, revSvc, nil, nil, llm.NewClientWithOptions(&http.Client{}, 10*time.Millisecond)).
		WithReviewCommentRepo(reviewRepo)
	return &chatReviewHarness{chatSvc: chatSvc, revSvc: revSvc, docRepo: docRepo, chatRepo: chatRepo, reviewRepo: reviewRepo, govRepo: govRepo}
}

type recordedEvent struct {
	name string
	data map[string]any
}

func collectSSE(ctx context.Context, svc *ChatService, prep *PreparedChatRefine) []recordedEvent {
	var events []recordedEvent
	svc.ExecuteRefineStream(ctx, prep, func(ev string, payload any) error {
		b, _ := json.Marshal(payload)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		events = append(events, recordedEvent{name: ev, data: m})
		return nil
	})
	return events
}

// TestChatReviewStudioPaths_T_ADR06_06_to_11 离线全量覆盖 T-ADR06-06 ~ T-ADR06-11 核心交互状态机与 P-7 ① 修复。
func TestChatReviewStudioPaths_T_ADR06_06_to_11(t *testing.T) {
	ctx := context.Background()

	// T-ADR06-06: Ask 只读模式放行 locked 章节与 global，不写 ChatProposalItem，不返回 JSON/EMPTY_PROPOSAL
	t.Run("T-ADR06-06 Ask on locked and global", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": "核心原因在于使用了分片锁。"}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		}))
		defer srv.Close()

		h := newChatReviewHarness(srv.URL)
		for _, tc := range []ChatRefineRequest{
			{Mode: models.ChatModeAsk, ScopeType: models.ChatScopeSlot, TargetSlotID: "sec_locked", BaseRevisionNo: 1, Prompt: "为何这样设计？"},
			{Mode: models.ChatModeAsk, ScopeType: models.ChatScopeGlobal, BaseRevisionNo: 1, Prompt: "全局瓶颈在哪？"},
		} {
			prep, err := h.chatSvc.PrepareRefine(ctx, 10, tc, 7)
			if err != nil {
				t.Fatalf("Ask mode PrepareRefine failed (%+v): %v", tc.ScopeType, err)
			}
			evs := collectSSE(ctx, h.chatSvc, prep)
			var deltaText strings.Builder
			for _, ev := range evs {
				if ev.name == "proposal_item" || ev.name == "diff_proposal" || ev.name == "error" {
					t.Fatalf("Ask mode must not emit %s: %+v", ev.name, ev.data)
				}
				if ev.name == "delta" {
					deltaText.WriteString(ev.data["text"].(string))
				}
			}
			if !strings.Contains(deltaText.String(), "分片锁") {
				t.Fatalf("expected markdown answer in Ask delta, got %q", deltaText.String())
			}
			if len(h.chatRepo.items) != 0 {
				t.Fatalf("Ask must create 0 ChatProposalItems")
			}
		}
	})

	// T-ADR06-07: inline 双段精修过滤 + proposal_item + diff_proposal
	t.Run("T-ADR06-07 Inline dual-segment refine", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": llm.MarkerReply + "\n补充了队列超时说明。\n" + llm.MarkerPayloadJSON + "\n{\"reply\":\"r\",\"markdown\":\"Auto updated inline\"}"}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		}))
		defer srv.Close()

		h := newChatReviewHarness(srv.URL)
		prep, err := h.chatSvc.PrepareRefine(ctx, 10, ChatRefineRequest{
			Mode:           models.ChatModeRevise,
			ScopeType:      models.ChatScopeInline,
			TargetSlotID:   "sec_auto",
			BaseRevisionNo: 1,
			Prompt:         "补充超时机制",
			InlineAnchor:   &models.ReviewCommentAnchor{Quote: "original", SourceLineStart: 1, SourceLineEnd: 1},
		}, 7)
		if err != nil {
			t.Fatalf("PrepareRefine failed: %v", err)
		}
		evs := collectSSE(ctx, h.chatSvc, prep)
		seen := map[string]bool{}
		for _, ev := range evs {
			seen[ev.name] = true
			if ev.name == "delta" && strings.Contains(ev.data["text"].(string), "{") {
				t.Fatalf("delta leaked JSON: %v", ev.data)
			}
		}
		for _, reqEv := range []string{"meta", "stage", "delta", "proposal_item", "diff_proposal", "verifier_preview", "done"} {
			if !seen[reqEv] {
				t.Fatalf("missing SSE event %q in %+v", reqEv, evs)
			}
		}
	})

	// T-ADR06-08 & T-ADR06-10 & T-ADR06-11: review_batch Plan -> Refine + subset Apply + P-7① + Reject revert
	t.Run("T-ADR06-08_10_11 Batch Plan Refine Apply Reject Supersede", func(t *testing.T) {
		var mu sync.Mutex
		callNo := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody map[string]any
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			stream, _ := reqBody["stream"].(bool)

			mu.Lock()
			callNo++
			n := callNo
			mu.Unlock()

			if !stream {
				// Stage A Plan: 包含一个 locked 误判和两个可编辑章节
				planResp, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{
						"message": map[string]any{
							"content": `{"intent":"review_batch","summary":"计划精修","target_slots":[{"slot_id":"sec_locked","action":"edit","comment_ids":[1]},{"slot_id":"sec_auto","action":"edit","comment_ids":[2]},{"slot_id":"sec_hybrid","action":"edit","comment_ids":[3]}]}`,
						},
					}},
				})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(planResp)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			md := fmt.Sprintf("Refined section round %d", n)
			text := llm.MarkerReply + "\n修订说明\n" + llm.MarkerPayloadJSON + fmt.Sprintf("\n{\"reply\":\"修订说明\",\"markdown\":%q}", md)
			chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": text}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		}))
		defer srv.Close()

		h := newChatReviewHarness(srv.URL,
			models.ReviewComment{ID: 1, DocumentID: 10, CommentNo: 1, SlotID: "sec_locked", Category: models.ReviewCategoryCorrection, Content: "改锁定章", Status: models.ReviewStatusOpen},
			models.ReviewComment{ID: 2, DocumentID: 10, CommentNo: 2, SlotID: "sec_auto", Category: models.ReviewCategorySupplement, Content: "补充架构", Status: models.ReviewStatusOpen},
			models.ReviewComment{ID: 3, DocumentID: 10, CommentNo: 3, SlotID: "sec_hybrid", Category: models.ReviewCategoryConcise, Content: "精简链路", Status: models.ReviewStatusOpen},
		)

		prep, err := h.chatSvc.PrepareRefine(ctx, 10, ChatRefineRequest{
			ScopeType:      models.ChatScopeReviewBatch,
			BaseRevisionNo: 1,
			CommentIDs:     []uint{1, 2, 3},
		}, 7)
		if err != nil {
			t.Fatalf("PrepareRefine review_batch failed: %v", err)
		}
		evs := collectSSE(ctx, h.chatSvc, prep)

		var propItems []ChatProposalItemVO
		for _, ev := range evs {
			if ev.name == "proposal_item" {
				b, _ := json.Marshal(ev.data)
				var it ChatProposalItemVO
				_ = json.Unmarshal(b, &it)
				propItems = append(propItems, it)
			}
		}
		if len(propItems) != 2 {
			t.Fatalf("expected 2 editable proposal items (sec_locked excluded), got %d", len(propItems))
		}
		// 仅覆盖成功槽位的批注 2、3 转为 proposed；锁定章批注 1 仍保持 open
		c1, _ := h.reviewRepo.GetByID(ctx, 10, 1)
		c2, _ := h.reviewRepo.GetByID(ctx, 10, 2)
		c3, _ := h.reviewRepo.GetByID(ctx, 10, 3)
		if c1.Status != models.ReviewStatusOpen || c2.Status != models.ReviewStatusProposed || c3.Status != models.ReviewStatusProposed {
			t.Fatalf("unexpected comment statuses after MarkProposed: c1=%s c2=%s c3=%s", c1.Status, c2.Status, c3.Status)
		}

		// 子集采纳 sec_auto（带 override_markdown） -> 主消息变 partial_accepted，sec_hybrid 未改且其 module_doc 非内联证据无损保留 (P-7①)！
		var autoItem, hybridItem ChatProposalItemVO
		for _, it := range propItems {
			if it.SlotID == "sec_auto" {
				autoItem = it
			} else if it.SlotID == "sec_hybrid" {
				hybridItem = it
			}
		}
		detail, _, err := h.chatSvc.ApplyProposalItems(ctx, 10, prep.AssistantMsg.ID, ApplyChatProposalPayload{
			BaseRevisionNo: 1,
			Items:          []ApplyProposalItemSelection{{ItemID: autoItem.ItemID, OverrideMarkdown: "User tweaked auto MD"}},
		}, 7)
		if err != nil {
			t.Fatalf("ApplyProposalItems failed: %v", err)
		}
		if detail.CurrentRevisionNo != 2 {
			t.Fatalf("expected rev 2, got %d", detail.CurrentRevisionNo)
		}
		msgAfterPartial, _ := h.chatRepo.GetMessage(ctx, 10, prep.AssistantMsg.ID)
		if msgAfterPartial.ProposalStatus != models.ProposalStatusPartialAccepted {
			t.Fatalf("expected partial_accepted, got %s", msgAfterPartial.ProposalStatus)
		}
		c2After, _ := h.reviewRepo.GetByID(ctx, 10, 2)
		if c2After.Status != models.ReviewStatusResolved {
			t.Fatalf("expected comment 2 resolved, got %s", c2After.Status)
		}
		// 断言未改动章节 sec_hybrid 非内联证据锚点 module_doc 无损保留
		for _, sec := range detail.Sections {
			if sec.SlotID == "sec_hybrid" && len(sec.Evidences) != 1 {
				t.Fatalf("P-7① regression: expected sec_hybrid non-inline evidence preserved, got %+v", sec.Evidences)
			}
		}

		// T-ADR06-11: 拒绝剩余 hybrid 提案项 -> c3 退回 open，无剩余 pending 时主消息收敛为 accepted
		rejRes, err := h.chatSvc.RejectProposalItem(ctx, 10, hybridItem.ItemID)
		if err != nil {
			t.Fatalf("RejectProposalItem failed: %v", err)
		}
		if rejRes.MessageProposalStatus != models.ProposalStatusAccepted {
			t.Fatalf("expected accepted when no pending items remain and one accepted, got %s", rejRes.MessageProposalStatus)
		}
		c3After, _ := h.reviewRepo.GetByID(ctx, 10, 3)
		if c3After.Status != models.ReviewStatusOpen {
			t.Fatalf("expected comment 3 reverted to open, got %s", c3After.Status)
		}

		// 验证手工直编触发 SupersedePendingProposalsBySlots 会取代同槽位 pending 提案并退回关联批注
		prepSup, err := h.chatSvc.PrepareRefine(ctx, 10, ChatRefineRequest{
			Mode:           models.ChatModeRevise,
			ScopeType:      models.ChatScopeReviewBatch,
			BaseRevisionNo: 2,
			CommentIDs:     []uint{3},
		}, 7)
		if err != nil {
			t.Fatalf("PrepareRefine for supersede test failed: %v", err)
		}
		collectSSE(ctx, h.chatSvc, prepSup)
		c3BeforeEdit, _ := h.reviewRepo.GetByID(ctx, 10, 3)
		if c3BeforeEdit.Status != models.ReviewStatusProposed {
			t.Fatalf("expected comment 3 proposed before manual edit, got %s", c3BeforeEdit.Status)
		}
		_, err = h.revSvc.ApplySectionChange(ctx, ApplySectionChangeInput{
			DocID:          10,
			SlotID:         "sec_hybrid",
			BaseRevisionNo: 2,
			Markdown:       "Manual overwrite triggers supersede",
			Source:         models.RevSourceManualEdit,
			OperatorID:     7,
		})
		if err != nil {
			t.Fatalf("ApplySectionChange manual edit failed: %v", err)
		}
		supMsg, _ := h.chatRepo.GetMessage(ctx, 10, prepSup.AssistantMsg.ID)
		if supMsg.ProposalStatus != models.ProposalStatusSuperseded {
			t.Fatalf("expected pending message superseded on manual edit, got %s", supMsg.ProposalStatus)
		}
		c3AfterSup, _ := h.reviewRepo.GetByID(ctx, 10, 3)
		if c3AfterSup.Status != models.ReviewStatusOpen {
			t.Fatalf("expected comment 3 reverted to open after supersede, got %s", c3AfterSup.Status)
		}
	})

	// T-ADR06-09: Abort 取消时清理 ProposalItem、Message 并退回批注状态
	t.Run("T-ADR06-09 Abort cleanup", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			w.WriteHeader(http.StatusRequestTimeout)
		}))
		defer srv.Close()

		h := newChatReviewHarness(srv.URL,
			models.ReviewComment{ID: 9, DocumentID: 10, CommentNo: 1, SlotID: "sec_auto", Category: models.ReviewCategoryCorrection, Content: "x", Status: models.ReviewStatusOpen},
		)
		prep, err := h.chatSvc.PrepareRefine(ctx, 10, ChatRefineRequest{
			ScopeType:      models.ChatScopeSlot,
			TargetSlotID:   "sec_auto",
			BaseRevisionNo: 1,
			Prompt:         "修订",
			CommentIDs:     []uint{9},
		}, 7)
		if err != nil {
			t.Fatalf("PrepareRefine failed: %v", err)
		}
		h.chatSvc.ExecuteRefineStream(cancelCtx, prep, func(string, any) error { return nil })

		if _, err := h.chatRepo.GetMessage(ctx, 10, prep.AssistantMsg.ID); err == nil {
			t.Fatalf("aborted assistant message must be deleted")
		}
		c9, _ := h.reviewRepo.GetByID(ctx, 10, 9)
		if c9.Status != models.ReviewStatusOpen {
			t.Fatalf("expected comment 9 to remain open after abort, got %s", c9.Status)
		}
	})

	// T-ADR06-08b: 仅涉及 locked 章节的批量审阅 -> 优雅输出只读建议且零 ProposalItem / 批注维持 open
	t.Run("T-ADR06-08b Advisory locked only graceful completion", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"content": `{"intent":"review_batch","summary":"目标均为锁定章节","target_slots":[{"slot_id":"sec_locked","action":"edit","comment_ids":[1]}]}`,
					},
				}},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}))
		defer srv.Close()

		h := newChatReviewHarness(srv.URL,
			models.ReviewComment{ID: 1, DocumentID: 10, CommentNo: 1, SlotID: "sec_locked", Category: models.ReviewCategoryCorrection, Content: "建议调整锁定章", Status: models.ReviewStatusOpen},
		)
		prep, err := h.chatSvc.PrepareRefine(ctx, 10, ChatRefineRequest{
			ScopeType:      models.ChatScopeReviewBatch,
			BaseRevisionNo: 1,
			CommentIDs:     []uint{1},
		}, 7)
		if err != nil {
			t.Fatalf("PrepareRefine failed: %v", err)
		}
		evs := collectSSE(ctx, h.chatSvc, prep)
		var sawPlan, sawDelta, sawDone, sawError bool
		for _, ev := range evs {
			switch ev.name {
			case "plan":
				sawPlan = true
			case "delta":
				sawDelta = true
			case "done":
				sawDone = true
			case "error", "proposal_item":
				sawError = true
			}
		}
		if !sawPlan || !sawDelta || !sawDone || sawError {
			t.Fatalf("unexpected events for advisory_locked-only: %+v", evs)
		}
		c1, _ := h.reviewRepo.GetByID(ctx, 10, 1)
		if c1.Status != models.ReviewStatusOpen {
			t.Fatalf("locked-only batch must leave comment open, got %s", c1.Status)
		}
	})
}

func commonTestRepo(id uint) *commonModels.Repository {
	return &commonModels.Repository{ID: id, Name: "demo"}
}
