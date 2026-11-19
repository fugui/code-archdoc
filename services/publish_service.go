package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/drift"
	"code-archdoc/pkg/publisher"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

// ErrVerifierBlocked 表示目标修订未通过 Verifier 硬门禁，禁止审批定稿发布（HTTP 422 VERIFIER_BLOCKED）。
var ErrVerifierBlocked = errors.New("文档未通过 Verifier 质量与安全门禁，禁止审批发布")

// VerifierBlockedError 携带完整 VerifierReport 的门禁阻断错误。
type VerifierBlockedError struct {
	Report models.VerifierReport
}

func (e *VerifierBlockedError) Error() string {
	if e.Report.BlockerCount > 0 {
		return fmt.Sprintf("发布被 Verifier 硬门禁拦截：检出 %d 项阻断性安全告警（如明文凭据泄漏），请先修复后发布", e.Report.BlockerCount)
	}
	return fmt.Sprintf("发布被 Verifier 门禁拦截：存在 %d 项未修复错误，请先完善后发布", e.Report.ErrorCount)
}

func (e *VerifierBlockedError) Unwrap() error {
	return ErrVerifierBlocked
}

// PublishResultVO 对应 POST /api/documents/:id/publish 响应（01-http-api-contract.md §3.7）。
type PublishResultVO struct {
	DocumentID            uint      `json:"document_id"`
	Status                string    `json:"status"`
	PublishedRevisionNo   int       `json:"published_revision_no"`
	PublishedCommitSHA    string    `json:"published_commit_sha"`
	PublishedRelPath      string    `json:"published_rel_path"`
	PublishedAt           time.Time `json:"published_at"`
	BrokenLinksDowngraded int       `json:"broken_links_downgraded"`
}

// UnpublishResultVO 对应 POST /api/documents/:id/unpublish 响应。
type UnpublishResultVO struct {
	DocumentID     uint   `json:"document_id"`
	Status         string `json:"status"`
	RemovedRelPath string `json:"removed_rel_path"`
}

// ExportZipRequest 对应 POST /api/exports/zip 请求体。
type ExportZipRequest struct {
	RepoIDs      []uint `json:"repo_ids"`
	DocumentIDs  []uint `json:"document_ids"`
	IncludeIndex *bool  `json:"include_index"`
}

// PublishService 编排定稿发布硬门禁、YAML Frontmatter & 深链渲染、原子落盘、撤回清理、单篇/ZIP 导出及修改率度量（F-11 / F-26）。
type PublishService struct {
	docRepo     DocumentRepo
	docTypeRepo DocTypeRepo
	masterRepo  SharedMasterRepo
	govRepo     GovernanceRepo
	sink        publisher.PublishSink

	publishMu sync.Mutex
}

func NewPublishService(
	docRepo DocumentRepo,
	docTypeRepo DocTypeRepo,
	masterRepo SharedMasterRepo,
	govRepo GovernanceRepo,
	sink publisher.PublishSink,
) *PublishService {
	return &PublishService{
		docRepo:     docRepo,
		docTypeRepo: docTypeRepo,
		masterRepo:  masterRepo,
		govRepo:     govRepo,
		sink:        sink,
	}
}

// PublishDocument 审批发布指定文档修订：
// 1. 严格校验 can_approve == true 且 blocker_count == 0，否则返回 422 VERIFIER_BLOCKED 且不落盘任何文件；
// 2. 渲染标准 YAML Frontmatter、平台深链与跨文档未发布降级；
// 3. 持发布互斥锁原子落盘至 publish_dir 并重建 README.md；
// 4. 更新 DB 发布状态并计算首版 AI 草稿 -> 定稿的章节级 edit_diff_ratio 写入 archdoc_quality_metrics。
func (s *PublishService) PublishDocument(
	ctx context.Context,
	docID uint,
	requestedRevNo *int,
	operator *commonModels.User,
) (*PublishResultVO, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	targetRevNo := doc.CurrentRevisionNo
	if requestedRevNo != nil && *requestedRevNo > 0 {
		targetRevNo = *requestedRevNo
	}
	if targetRevNo <= 0 {
		return nil, models.ErrNotFound
	}

	rev, secEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, targetRevNo)
	if err != nil {
		return nil, err
	}

	var vReport models.VerifierReport
	_ = json.Unmarshal(rev.VerifierReportJSON, &vReport)
	normalizeVerifierReportSlices(&vReport)

	// Verifier 硬门禁（SC-04）：未通过直接阻断，publish_dir 零写入
	if !rev.CanApprove || !vReport.CanApprove || vReport.BlockerCount > 0 {
		return nil, &VerifierBlockedError{Report: vReport}
	}

	repo, err := s.masterRepo.GetRepository(ctx, doc.RepoID)
	if err != nil {
		return nil, err
	}

	publishedByName := "system"
	var publishedByID uint
	if operator != nil {
		publishedByID = operator.ID
		if strings.TrimSpace(operator.Username) != "" {
			publishedByName = operator.Username
		} else if strings.TrimSpace(operator.Name) != "" {
			publishedByName = operator.Name
		}
	}

	now := time.Now()
	rendered, docTypeName, err := s.renderDocumentMarkdown(ctx, doc, repo, rev, secEvs, publishedByName, now, true)
	if err != nil {
		return nil, err
	}

	relPath := publisher.BuildPublishRelPath(repo.Name, doc.Subdirectory, doc.DocTypeSlug)

	// 汇总全部已发布文档列表（含当前新发布的文档）用于原子重建 README.md 索引
	entries, err := s.collectPublishedEntries(ctx, doc.ID, &publisher.PublishedDocEntry{
		RepoName:     repo.Name,
		Subdirectory: doc.Subdirectory,
		DocTypeSlug:  doc.DocTypeSlug,
		DocTypeName:  docTypeName,
		Title:        doc.Title,
		RelPath:      relPath,
		CommitSHA:    rev.CommitSHA,
		PublishedAt:  now.Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}

	if s.sink != nil {
		if err := s.sink.PublishDocument(ctx, relPath, doc.PublishedRelPath, []byte(rendered.Markdown), entries); err != nil {
			return nil, fmt.Errorf("原子落盘发布文档失败: %w", err)
		}
	}

	if err := s.docRepo.MarkPublished(ctx, doc.ID, rev.ID, rev.CommitSHA, relPath, publishedByID, now); err != nil {
		return nil, err
	}

	// 计算首版 AI 草稿 -> 定稿章节级 edit_diff_ratio 并落库 archdoc_quality_metrics（F-26）
	s.recordPublishedQualityMetric(ctx, doc, rev)

	return &PublishResultVO{
		DocumentID:            doc.ID,
		Status:                models.DocStatusApproved,
		PublishedRevisionNo:   rev.RevisionNo,
		PublishedCommitSHA:    rev.CommitSHA,
		PublishedRelPath:      relPath,
		PublishedAt:           now,
		BrokenLinksDowngraded: rendered.BrokenLinksDowngraded,
	}, nil
}

// UnpublishDocument 一键撤回已发布文档：删除落盘文件、重建或移除 README.md 与空目录、DB 退回 draft（SC-10）。
func (s *PublishService) UnpublishDocument(ctx context.Context, docID uint) (*UnpublishResultVO, error) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}

	removedRelPath := doc.PublishedRelPath
	remainingEntries, err := s.collectPublishedEntries(ctx, doc.ID, nil)
	if err != nil {
		return nil, err
	}

	if s.sink != nil && removedRelPath != "" {
		if err := s.sink.UnpublishDocument(ctx, removedRelPath, remainingEntries); err != nil {
			return nil, fmt.Errorf("清理已发布文档失败: %w", err)
		}
	}

	if err := s.docRepo.MarkUnpublished(ctx, doc.ID); err != nil {
		return nil, err
	}

	return &UnpublishResultVO{
		DocumentID:     doc.ID,
		Status:         models.DocStatusDraft,
		RemovedRelPath: removedRelPath,
	}, nil
}

// ExportSingleDocument 流式导出单篇带 YAML Frontmatter 与平台深链的 Markdown 文件；普通用户仅允许导出已发布版本。
func (s *PublishService) ExportSingleDocument(ctx context.Context, docID uint, canWrite bool) (string, []byte, error) {
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return "", nil, err
	}

	var (
		rev    *models.DocumentRevision
		secEvs []models.SectionEvidence
	)
	if !canWrite {
		if doc.Status != models.DocStatusApproved || doc.PublishedRevisionID == nil {
			return "", nil, ErrDraftAccessDenied
		}
		rev, secEvs, err = s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if err != nil {
			return "", nil, err
		}
	} else if doc.Status == models.DocStatusApproved && doc.PublishedRevisionID != nil && (doc.CurrentRevisionID == nil || *doc.CurrentRevisionID == *doc.PublishedRevisionID) {
		rev, secEvs, err = s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if err != nil {
			return "", nil, err
		}
	} else {
		if doc.CurrentRevisionNo <= 0 {
			return "", nil, models.ErrNotFound
		}
		rev, secEvs, err = s.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
		if err != nil {
			return "", nil, err
		}
	}

	repo, err := s.masterRepo.GetRepository(ctx, doc.RepoID)
	if err != nil {
		return "", nil, err
	}

	pubAt := rev.CreatedAt
	if doc.PublishedAt != nil {
		pubAt = *doc.PublishedAt
	}
	pubBy := s.resolveUserDisplayName(ctx, doc.PublishedBy, rev.CreatedBy)

	rendered, _, err := s.renderDocumentMarkdown(ctx, doc, repo, rev, secEvs, pubBy, pubAt, false)
	if err != nil {
		return "", nil, err
	}

	repoSlug := publisher.SanitizePublishSegment(repo.Name)
	docSlug := publisher.SanitizePublishSegment(doc.DocTypeSlug)
	filename := fmt.Sprintf("%s-%s.md", repoSlug, docSlug)
	return filename, []byte(rendered.Markdown), nil
}

// ExportZipArchive 批量打包已发布文档为防 Zip-Slip 的 ZIP 归档（含可选 INDEX.md）。
func (s *PublishService) ExportZipArchive(ctx context.Context, req ExportZipRequest) (string, []byte, error) {
	if len(req.RepoIDs) == 0 && len(req.DocumentIDs) == 0 {
		return "", nil, fmt.Errorf("%w: repo_ids 与 document_ids 至少提供一个", ErrInvalidRevisionParam)
	}

	docMap := make(map[uint]models.Document)
	if len(req.DocumentIDs) > 0 {
		docs, err := s.docRepo.ListByIDs(ctx, req.DocumentIDs, true)
		if err != nil {
			return "", nil, err
		}
		for _, d := range docs {
			docMap[d.ID] = d
		}
	}
	if len(req.RepoIDs) > 0 {
		docs, err := s.docRepo.ListByRepos(ctx, req.RepoIDs)
		if err != nil {
			return "", nil, err
		}
		for _, d := range docs {
			if d.Status == models.DocStatusApproved && d.PublishedRevisionID != nil {
				docMap[d.ID] = d
			}
		}
	}

	if len(docMap) == 0 {
		return "", nil, models.ErrNotFound
	}

	repoCache := make(map[uint]*commonModels.Repository)
	items := make([]publisher.ZipDocItem, 0, len(docMap))
	for _, doc := range docMap {
		if doc.PublishedRevisionID == nil {
			continue
		}
		rev, secEvs, err := s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if err != nil {
			continue
		}
		repo, ok := repoCache[doc.RepoID]
		if !ok {
			r, rErr := s.masterRepo.GetRepository(ctx, doc.RepoID)
			if rErr != nil {
				continue
			}
			repo = r
			repoCache[doc.RepoID] = repo
		}

		pubAt := rev.CreatedAt
		if doc.PublishedAt != nil {
			pubAt = *doc.PublishedAt
		}
		pubBy := s.resolveUserDisplayName(ctx, doc.PublishedBy, rev.CreatedBy)
		rendered, docTypeName, err := s.renderDocumentMarkdown(ctx, &doc, repo, rev, secEvs, pubBy, pubAt, false)
		if err != nil {
			return "", nil, err
		}

		entryPath := publisher.BuildPublishRelPath(repo.Name, doc.Subdirectory, doc.DocTypeSlug)
		items = append(items, publisher.ZipDocItem{
			EntryPath:    entryPath,
			Title:        doc.Title,
			RepoName:     repo.Name,
			Subdirectory: doc.Subdirectory,
			DocTypeName:  docTypeName,
			CommitSHA:    rev.CommitSHA,
			Content:      []byte(rendered.Markdown),
		})
	}

	if len(items) == 0 {
		return "", nil, models.ErrNotFound
	}

	includeIndex := true
	if req.IncludeIndex != nil {
		includeIndex = *req.IncludeIndex
	}

	var buf bytes.Buffer
	if err := publisher.BuildZipArchive(&buf, items, includeIndex); err != nil {
		return "", nil, err
	}

	filename := fmt.Sprintf("archdoc-export-%s.zip", time.Now().Format("20060102"))
	return filename, buf.Bytes(), nil
}

func (s *PublishService) renderDocumentMarkdown(
	ctx context.Context,
	doc *models.Document,
	repo *commonModels.Repository,
	rev *models.DocumentRevision,
	secEvs []models.SectionEvidence,
	publishedBy string,
	publishedAt time.Time,
	includeSelfAsPublished bool,
) (publisher.RenderOutput, string, error) {
	docTypeName := doc.DocTypeSlug
	docTypeRevNo := 0
	if dt, _, dtErr := s.docTypeRepo.GetBySlug(ctx, doc.DocTypeSlug); dtErr == nil && dt != nil {
		docTypeName = dt.DisplayName
	}
	if dtRev, rErr := s.docTypeRepo.GetRevisionByID(ctx, rev.DocTypeRevisionID); rErr == nil && dtRev != nil {
		docTypeRevNo = dtRev.RevisionNo
	}

	// 收集同 (repo_id, subdirectory, ref_name) 下已发布的同仓 DocType 集合用于跨文档链接校验
	siblings, err := s.docRepo.ListByScope(ctx, doc.RepoID, doc.Subdirectory, doc.RefName, true)
	if err != nil {
		return publisher.RenderOutput{}, "", err
	}
	publishedSlugs := make(map[string]bool, len(siblings)+1)
	for _, sib := range siblings {
		publishedSlugs[sib.DocTypeSlug] = true
	}
	if includeSelfAsPublished {
		publishedSlugs[doc.DocTypeSlug] = true
	}

	// 汇总章节已验证的证据锚点供深链转换
	var evRefs []publisher.EvidenceRef
	for _, se := range secEvs {
		var anchors []models.EvidenceAnchor
		_ = json.Unmarshal(se.EvidencesJSON, &anchors)
		for _, a := range anchors {
			evRefs = append(evRefs, publisher.EvidenceRef{
				FilePath:   a.FilePath,
				SymbolKind: a.SymbolKind,
				FQSymbol:   a.FQSymbol,
				CommitSHA:  a.CommitSHA,
				LineStart:  a.LineStart,
				LineEnd:    a.LineEnd,
			})
		}
	}

	out := publisher.RenderMarkdown(publisher.RenderInput{
		Frontmatter: publisher.FrontmatterData{
			Title:           doc.Title,
			Repo:            repo.Name,
			Subdir:          doc.Subdirectory,
			DocType:         doc.DocTypeSlug,
			DocTypeName:     docTypeName,
			DocTypeRevision: docTypeRevNo,
			DocID:           doc.ID,
			RevisionNo:      rev.RevisionNo,
			RefName:         doc.RefName,
			CommitSHA:       rev.CommitSHA,
			PublishedAt:     publishedAt.Format(time.RFC3339),
			PublishedBy:     publishedBy,
			SourceModule:    config.ModuleKey,
		},
		BodyMarkdown:      rev.FullMarkdown,
		Evidences:         evRefs,
		PublishedDocTypes: publishedSlugs,
	})
	return out, docTypeName, nil
}

func (s *PublishService) collectPublishedEntries(
	ctx context.Context,
	overrideDocID uint,
	overrideEntry *publisher.PublishedDocEntry,
) ([]publisher.PublishedDocEntry, error) {
	approvedDocs, err := s.docRepo.ListApprovedDocuments(ctx)
	if err != nil {
		return nil, err
	}

	dtList, _ := s.docTypeRepo.List(ctx, false)
	dtNameBySlug := make(map[string]string, len(dtList))
	for _, dt := range dtList {
		dtNameBySlug[dt.Slug] = dt.DisplayName
	}

	repoCache := make(map[uint]string)
	entries := make([]publisher.PublishedDocEntry, 0, len(approvedDocs)+1)
	for _, d := range approvedDocs {
		if d.ID == overrideDocID {
			continue
		}
		if strings.TrimSpace(d.PublishedRelPath) == "" {
			continue
		}
		repoName, ok := repoCache[d.RepoID]
		if !ok {
			if r, rErr := s.masterRepo.GetRepository(ctx, d.RepoID); rErr == nil && r != nil {
				repoName = r.Name
			} else {
				repoName = fmt.Sprintf("repo-%d", d.RepoID)
			}
			repoCache[d.RepoID] = repoName
		}
		pubAtStr := ""
		if d.PublishedAt != nil {
			pubAtStr = d.PublishedAt.Format(time.RFC3339)
		}
		dtName := dtNameBySlug[d.DocTypeSlug]
		if dtName == "" {
			dtName = d.DocTypeSlug
		}
		entries = append(entries, publisher.PublishedDocEntry{
			RepoName:     repoName,
			Subdirectory: d.Subdirectory,
			DocTypeSlug:  d.DocTypeSlug,
			DocTypeName:  dtName,
			Title:        d.Title,
			RelPath:      d.PublishedRelPath,
			CommitSHA:    d.PublishedCommitSHA,
			PublishedAt:  pubAtStr,
		})
	}
	if overrideEntry != nil {
		entries = append(entries, *overrideEntry)
	}
	return entries, nil
}

func (s *PublishService) recordPublishedQualityMetric(ctx context.Context, doc *models.Document, publishedRev *models.DocumentRevision) {
	if s.govRepo == nil {
		return
	}

	approvedSnaps := unmarshalSectionSnapshots(publishedRev.SectionsJSON)
	approvedContents := make([]drift.SectionContent, 0, len(approvedSnaps))
	for _, sn := range approvedSnaps {
		approvedContents = append(approvedContents, drift.SectionContent{
			SlotID:   sn.SlotID,
			Title:    sn.Title,
			Markdown: sn.Markdown,
		})
	}

	draftContents := approvedContents
	if firstAIRev, err := s.docRepo.GetFirstAIGenerateRevision(ctx, doc.ID); err == nil && firstAIRev != nil {
		draftSnaps := unmarshalSectionSnapshots(firstAIRev.SectionsJSON)
		draftContents = make([]drift.SectionContent, 0, len(draftSnaps))
		for _, sn := range draftSnaps {
			draftContents = append(draftContents, drift.SectionContent{
				SlotID:   sn.SlotID,
				Title:    sn.Title,
				Markdown: sn.Markdown,
			})
		}
	}

	overallRatio, slotRatios := drift.ComputeEditDiffRatio(draftContents, approvedContents)
	slotRatiosJSON, err := json.Marshal(slotRatios)
	if err != nil {
		slotRatiosJSON = []byte("{}")
	}
	if err := s.govRepo.RecordQualityMetric(ctx, &models.QualityMetric{
		DocumentID:         doc.ID,
		RepoID:             doc.RepoID,
		DocTypeSlug:        doc.DocTypeSlug,
		DocTypeRevisionID:  publishedRev.DocTypeRevisionID,
		MetricEvent:        models.MetricEventDocPublished,
		EditDiffRatio:      overallRatio,
		SlotDiffRatiosJSON: datatypes.JSON(slotRatiosJSON),
		SlotCoverageRate:   publishedRev.SlotCoverageRate,
		VerifierBlocked:    false,
	}); err != nil {
		log.Printf("[%s] 记录发布质量指标失败 (doc_id=%d): %v", config.ServiceName, doc.ID, err)
	}
}

func (s *PublishService) resolveUserDisplayName(ctx context.Context, preferredID *uint, fallbackID uint) string {
	uid := fallbackID
	if preferredID != nil && *preferredID > 0 {
		uid = *preferredID
	}
	if uid == 0 || s.masterRepo == nil {
		return "system"
	}
	users, err := s.masterRepo.GetUsersByIDs(ctx, []uint{uid})
	if err != nil {
		return "system"
	}
	if u, ok := users[uid]; ok {
		if strings.TrimSpace(u.Username) != "" {
			return u.Username
		}
		if strings.TrimSpace(u.Name) != "" {
			return u.Name
		}
	}
	return "system"
}
