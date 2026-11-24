package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/drift"
	"code-common/backend/gitsync"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

// ErrNoRefreshableSlots 表示当前文档无受漂移影响且非锁定的可刷新槽位（HTTP 400 INVALID_PARAM）。
var ErrNoRefreshableSlots = errors.New("当前文档不存在需要刷新的非锁定受影响章节")

// DriftSummaryVO 对应 GET /api/drift/summary 响应（01-http-api-contract.md §3.8）。
type DriftSummaryVO struct {
	TotalDriftedDocs    int64      `json:"total_drifted_docs"`
	CodeDriftDocs       int64      `json:"code_drift_docs"`
	SpecDriftDocs       int64      `json:"spec_drift_docs"`
	BothDriftDocs       int64      `json:"both_drift_docs"`
	MonitoredRepos      int64      `json:"monitored_repos"`
	UnhealthyRepos      int64      `json:"unhealthy_repos"`
	LastPollAt          *time.Time `json:"last_poll_at"`
	PollIntervalMinutes int        `json:"poll_interval_minutes"`
}

// DriftDocumentVO 对应 GET /api/drift/documents 分页条目（01-http-api-contract.md §3.8）。
type DriftDocumentVO struct {
	DocumentID              uint              `json:"document_id"`
	RepoID                  uint              `json:"repo_id"`
	RepoName                string            `json:"repo_name"`
	Subdirectory            string            `json:"subdirectory"`
	DocTypeSlug             string            `json:"doc_type_slug"`
	DocTypeName             string            `json:"doc_type_name"`
	CanWrite                bool              `json:"can_write"`
	PublishedCommitSHA      string            `json:"published_commit_sha"`
	RemoteHeadSHA           string            `json:"remote_head_sha"`
	BoundDocTypeRevisionNo  int               `json:"bound_doc_type_revision_no"`
	LatestDocTypeRevisionNo int               `json:"latest_doc_type_revision_no"`
	CodeDrift               *DriftDimensionVO `json:"code_drift"`
	SpecDrift               *DriftDimensionVO `json:"spec_drift"`
}

// DriftDocumentListResponse 对应 GET /api/drift/documents 分页响应。
type DriftDocumentListResponse struct {
	Items    []DriftDocumentVO `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// IncrementalRefreshRequest 对应 POST /api/documents/:id/incremental-refresh 入参。
type IncrementalRefreshRequest struct {
	SlotIDs          []string `json:"slot_ids"`
	Engine           string   `json:"engine"`
	ExcludeRefDocIDs []uint   `json:"exclude_ref_doc_ids,omitempty"`
}

// DriftOrchestrator 衔接 F-25 远端巡检、DocType 变更事件、章节级双维漂移定位与单篇增量刷新（F-12）。
type DriftOrchestrator struct {
	masterRepo          SharedMasterRepo
	docRepo             DocumentRepo
	docTypeRepo         DocTypeRepo
	docTypeSvc          *DocTypeService
	driftRepo           DriftRepo
	moduleRepo          ModuleRepo
	rbacSvc             *RBACService
	genSvc              *GenerationOrchestrator
	snapSvc             *SnapshotService
	aggregator          *codeir.Aggregator
	leaseMgr            *ActiveLeaseManager
	sandboxDir          string
	pollIntervalMinutes int
}

// WithSnapshotService 注入快照服务以共享按仓库粒度的工作树读锁（消除与 Git 同步的并发竞态）。
func (s *DriftOrchestrator) WithSnapshotService(snapSvc *SnapshotService) *DriftOrchestrator {
	s.snapSvc = snapSvc
	return s
}

// WithModuleRepo 注入模块索引仓储以支持整仓文档的模块范围/摘要漂移评估（ADR-04 D-4）。
func (s *DriftOrchestrator) WithModuleRepo(moduleRepo ModuleRepo) *DriftOrchestrator {
	s.moduleRepo = moduleRepo
	return s
}

// NewDriftOrchestrator 构造双维漂移雷达编排服务。
func NewDriftOrchestrator(
	masterRepo SharedMasterRepo,
	docRepo DocumentRepo,
	docTypeRepo DocTypeRepo,
	docTypeSvc *DocTypeService,
	driftRepo DriftRepo,
	rbacSvc *RBACService,
	genSvc *GenerationOrchestrator,
	aggregator *codeir.Aggregator,
	leaseMgr *ActiveLeaseManager,
	sandboxDir string,
	pollIntervalMinutes int,
) *DriftOrchestrator {
	if pollIntervalMinutes <= 0 {
		pollIntervalMinutes = 15
	}
	return &DriftOrchestrator{
		masterRepo:          masterRepo,
		docRepo:             docRepo,
		docTypeRepo:         docTypeRepo,
		docTypeSvc:          docTypeSvc,
		driftRepo:           driftRepo,
		rbacSvc:             rbacSvc,
		genSvc:              genSvc,
		aggregator:          aggregator,
		leaseMgr:            leaseMgr,
		sandboxDir:          sandboxDir,
		pollIntervalMinutes: pollIntervalMinutes,
	}
}

// EvaluateRepoCodeDrift 在远端 HEAD 变更后，对指定仓库全部已发布文档执行两阶段章节级 Code Drift 精确定位（F-12 / SC-11）。
func (s *DriftOrchestrator) EvaluateRepoCodeDrift(
	ctx context.Context,
	repo models.PollableRepoRecord,
	remoteHeadSHA string,
	resolvedBranch string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	docs, err := s.docRepo.ListApprovedDocumentsByRepo(ctx, repo.RepoID)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}

	releaseRepoLease := s.leaseMgr.AcquireRepo(repo.RepoID)
	defer releaseRepoLease()

	evalFn := func() error {
		return s.evaluateRepoCodeDriftLocked(ctx, repo, docs, remoteHeadSHA, resolvedBranch)
	}
	if s.snapSvc != nil {
		return s.snapSvc.WithRepoReadLock(repo.RepoID, evalFn)
	}
	return evalFn()
}

func (s *DriftOrchestrator) evaluateRepoCodeDriftLocked(
	ctx context.Context,
	repo models.PollableRepoRecord,
	docs []models.Document,
	remoteHeadSHA string,
	resolvedBranch string,
) error {
	localPath := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", repo.RepoID))
	gitDir, cleanupTmp, repoReady := s.ensureMetadataGitDir(ctx, localPath, repo.RepoURL, remoteHeadSHA)
	if cleanupTmp != nil {
		defer cleanupTmp()
	}

	// 按 publishedCommitSHA 缓存 DiffNameStatus，避免同仓库多 DocType 重复调用 git diff
	diffCache := make(map[string][]gitsync.ChangedFile)
	diffErrCache := make(map[string]bool)

	now := time.Now()
	for _, doc := range docs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if doc.PublishedRevisionID == nil {
			continue
		}
		// 严格按当前有效跟踪分支过滤，避免旧分支或其它分支文档误报漂移
		if resolvedBranch != "" && doc.RefName != "" && doc.RefName != resolvedBranch {
			if !(doc.RefName == "master" && resolvedBranch == "main") {
				continue
			}
		}

		pubSHA := strings.TrimSpace(doc.PublishedCommitSHA)
		// 快速路径：无新提交 -> 零 Git 解析，但整仓文档仍需保留/合并模块级上卷漂移
		if pubSHA != "" && pubSHA == remoteHeadSHA {
			var modSlots []drift.AffectedSlot
			if doc.Subdirectory == "" {
				modSlots = s.computeModuleDriftForRepoDoc(ctx, &doc, nil, nil, false)
			}
			merged := toModelAffectedSlots(drift.MergeCodeAndModuleDriftSlots(nil, modSlots))
			if err := s.upsertCodeDrift(ctx, doc.ID, len(merged) > 0, models.DriftPrecisionSectionLevel, pubSHA, remoteHeadSHA, merged, now); err != nil {
				return err
			}
			continue
		}

		if !repoReady || pubSHA == "" {
			// 本地沙箱缺失且远端不支持 partial clone 时降级记录 repo_level 漂移
			if err := s.upsertCodeDrift(ctx, doc.ID, true, models.DriftPrecisionRepoLevel, pubSHA, remoteHeadSHA, []models.AffectedSlotDrift{}, now); err != nil {
				return err
			}
			continue
		}

		changedFiles, cached := diffCache[pubSHA]
		if !cached && !diffErrCache[pubSHA] {
			// 确保 base 与 head 两个提交对象都存在后再做零签出 diff
			if fetchErr := gitsync.FetchCommitMetadata(ctx, gitDir, repo.RepoURL, pubSHA); fetchErr != nil {
				log.Printf("[%s] 预拉取基线提交 %s 失败（后续仍尝试 diff）: %v", config.ServiceName, pubSHA, fetchErr)
			}
			if fetchErr := gitsync.FetchCommitMetadata(ctx, gitDir, repo.RepoURL, remoteHeadSHA); fetchErr != nil {
				log.Printf("[%s] 预拉取远端提交 %s 失败（后续仍尝试 diff）: %v", config.ServiceName, remoteHeadSHA, fetchErr)
			}
			cfs, dErr := gitsync.DiffNameStatus(ctx, gitDir, pubSHA, remoteHeadSHA)
			if dErr != nil {
				log.Printf("[%s] 计算仓库 #%d (%s..%s) DiffNameStatus 失败，降级为 repo_level: %v",
					config.ServiceName, repo.RepoID, pubSHA, remoteHeadSHA, dErr)
				diffErrCache[pubSHA] = true
			} else {
				changedFiles = cfs
				diffCache[pubSHA] = cfs
			}
		}

		if diffErrCache[pubSHA] {
			if err := s.upsertCodeDrift(ctx, doc.ID, true, models.DriftPrecisionRepoLevel, pubSHA, remoteHeadSHA, []models.AffectedSlotDrift{}, now); err != nil {
				return err
			}
			continue
		}

		pubRev, secEvs, rErr := s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if rErr != nil || pubRev == nil {
			continue
		}

		sectionsInput := buildSectionEvidenceInputs(pubRev, secEvs)
		result := drift.DetectCodeDrift(ctx, drift.CodeDriftInput{
			PublishedCommitSHA: pubSHA,
			RemoteHeadSHA:      remoteHeadSHA,
			Subdirectory:       doc.Subdirectory,
			ChangedFiles:       changedFiles,
			Sections:           sectionsInput,
			ReadBlob: func(rCtx context.Context, commitSHA, repoRelPath string) ([]byte, error) {
				return gitsync.ShowFileAtCommit(rCtx, gitDir, commitSHA, repoRelPath)
			},
			ExtractSymbols: s.extractSingleFileFingerprints,
		})

		affectedSlots := result.AffectedSlots
		if doc.Subdirectory == "" {
			modSlots := s.computeModuleDriftForRepoDoc(ctx, &doc, pubRev, secEvs, false)
			affectedSlots = drift.MergeCodeAndModuleDriftSlots(affectedSlots, modSlots)
		}
		affectedModels := toModelAffectedSlots(affectedSlots)
		hasDrift := result.HasDrift || len(affectedModels) > 0

		if err := s.upsertCodeDrift(ctx, doc.ID, hasDrift, result.Precision, pubSHA, remoteHeadSHA, affectedModels, now); err != nil {
			return err
		}
	}

	return nil
}

// RecomputeAllSpecDrifts 对全部已注册 DocType 执行一次 Spec Drift 兜底重算（供巡检周期末尾调用）。
func (s *DriftOrchestrator) RecomputeAllSpecDrifts(ctx context.Context) error {
	if s.docTypeSvc == nil || s.docTypeRepo == nil {
		return nil
	}
	dts, err := s.docTypeRepo.List(ctx, false)
	if err != nil {
		return err
	}
	for _, dt := range dts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.docTypeSvc.RecomputeSpecDriftForSlug(ctx, dt.Slug); err != nil {
			log.Printf("[%s] 重算 DocType(%s) Spec Drift 失败: %v", config.ServiceName, dt.Slug, err)
		}
	}
	return nil
}

// OnDocumentPublished 在文档发布/重新发布后即时同步刷新该文档的 Code Drift 与 Spec Drift 状态，
// 并在子模块文档发布时联动重算同仓库整仓已发布文档的 module_digest_changed / module_scope_changed 漂移（ADR-04 D-4）。
func (s *DriftOrchestrator) OnDocumentPublished(ctx context.Context, docID uint) {
	if s == nil || s.docRepo == nil || s.driftRepo == nil {
		return
	}
	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil || doc == nil || doc.Status != models.DocStatusApproved {
		return
	}
	if s.docTypeSvc != nil {
		if specErr := s.docTypeSvc.RecomputeSpecDriftForSlug(ctx, doc.DocTypeSlug); specErr != nil {
			log.Printf("[%s] 发布后联动刷新 Spec Drift 失败 (doc=%d): %v", config.ServiceName, doc.ID, specErr)
		}
	}
	heads, err := s.driftRepo.ListRemoteHeadsByRepos(ctx, []uint{doc.RepoID})
	if err == nil {
		for _, h := range heads {
			if h.RemoteHeadSHA != "" && h.RemoteHeadSHA == doc.PublishedCommitSHA {
				var modSlots []drift.AffectedSlot
				if doc.Subdirectory == "" {
					modSlots = s.computeModuleDriftForRepoDoc(ctx, doc, nil, nil, false)
				}
				merged := toModelAffectedSlots(drift.MergeCodeAndModuleDriftSlots(nil, modSlots))
				if clearErr := s.upsertCodeDrift(ctx, doc.ID, len(merged) > 0, models.DriftPrecisionSectionLevel, doc.PublishedCommitSHA, h.RemoteHeadSHA, merged, time.Now()); clearErr != nil {
					log.Printf("[%s] 发布后同步 Code Drift 失败 (doc=%d): %v", config.ServiceName, doc.ID, clearErr)
				}
				break
			}
		}
	}
	if doc.Subdirectory != "" {
		s.recomputeRepoModuleDrift(ctx, doc.RepoID, false)
	} else {
		s.recomputeSingleRepoDocModuleDrift(ctx, doc, false)
	}
}

// OnDocumentUnpublished 在文档撤回发布后清理其漂移状态记录，并在子模块撤回时联动重算整仓模块漂移。
func (s *DriftOrchestrator) OnDocumentUnpublished(ctx context.Context, docID uint) {
	if s == nil || s.driftRepo == nil {
		return
	}
	var doc *models.Document
	if s.docRepo != nil {
		doc, _ = s.docRepo.GetByID(ctx, docID)
	}
	if err := s.driftRepo.ClearDocumentDrifts(ctx, docID); err != nil {
		log.Printf("[%s] 撤回发布后清理漂移状态失败 (doc=%d): %v", config.ServiceName, docID, err)
	}
	if doc != nil && doc.Subdirectory != "" {
		s.recomputeRepoModuleDrift(ctx, doc.RepoID, false)
	}
}

// OnRepoModulesChanged 在仓库模块索引刷新、状态变更或迁移后触发整仓已发布文档的模块范围漂移评估（ADR-04 D-4）。
func (s *DriftOrchestrator) OnRepoModulesChanged(ctx context.Context, repoID uint) {
	if s == nil || s.docRepo == nil || s.driftRepo == nil || repoID == 0 {
		return
	}
	s.recomputeRepoModuleDrift(ctx, repoID, true)
}

// SyncCodeDriftAfterBranchSwitch 在单仓切换目标分支后，立即依据新分支远端 HEAD 对已发布文档同步代码漂移态（ADR-07）。
func (s *DriftOrchestrator) SyncCodeDriftAfterBranchSwitch(ctx context.Context, repoID uint, newBranch, remoteHeadSHA string) int {
	if s == nil || s.docRepo == nil || s.driftRepo == nil || repoID == 0 || strings.TrimSpace(remoteHeadSHA) == "" {
		return 0
	}
	docs, err := s.docRepo.ListApprovedDocumentsByRepo(ctx, repoID)
	if err != nil || len(docs) == 0 {
		return 0
	}

	now := time.Now()
	driftedCount := 0
	for i := range docs {
		doc := &docs[i]
		if doc.PublishedRevisionID == nil {
			continue
		}
		if newBranch != "" && doc.RefName != "" && doc.RefName != newBranch {
			continue
		}

		pubSHA := strings.TrimSpace(doc.PublishedCommitSHA)
		var modSlots []drift.AffectedSlot
		if doc.Subdirectory == "" {
			modSlots = s.computeModuleDriftForRepoDoc(ctx, doc, nil, nil, false)
		}
		merged := toModelAffectedSlots(drift.MergeCodeAndModuleDriftSlots(nil, modSlots))

		if pubSHA != "" && pubSHA == remoteHeadSHA {
			hasDrift := len(merged) > 0
			if hasDrift {
				driftedCount++
			}
			if uErr := s.upsertCodeDrift(ctx, doc.ID, hasDrift, models.DriftPrecisionSectionLevel, pubSHA, remoteHeadSHA, merged, now); uErr != nil {
				log.Printf("[%s] 切分支后清零代码漂移失败 (doc=%d): %v", config.ServiceName, doc.ID, uErr)
			}
			continue
		}

		driftedCount++
		if uErr := s.upsertCodeDrift(ctx, doc.ID, true, models.DriftPrecisionRepoLevel, pubSHA, remoteHeadSHA, merged, now); uErr != nil {
			log.Printf("[%s] 切分支后标记代码漂移失败 (doc=%d): %v", config.ServiceName, doc.ID, uErr)
		}
	}
	return driftedCount
}

func (s *DriftOrchestrator) recomputeRepoModuleDrift(ctx context.Context, repoID uint, forceScopeCheck bool) {
	if s == nil || s.docRepo == nil || s.driftRepo == nil || repoID == 0 {
		return
	}
	docs, err := s.docRepo.ListApprovedDocumentsByRepo(ctx, repoID)
	if err != nil {
		return
	}
	for i := range docs {
		if docs[i].Subdirectory == "" && docs[i].PublishedRevisionID != nil {
			s.recomputeSingleRepoDocModuleDrift(ctx, &docs[i], forceScopeCheck)
		}
	}
}

func (s *DriftOrchestrator) recomputeSingleRepoDocModuleDrift(ctx context.Context, doc *models.Document, forceScopeCheck bool) {
	if doc == nil || doc.Subdirectory != "" || doc.PublishedRevisionID == nil {
		return
	}
	existingDrifts, _ := s.driftRepo.ListDriftsByDocIDs(ctx, []uint{doc.ID})
	var existingCode *models.DocumentDrift
	var gitSlots []drift.AffectedSlot
	for i := range existingDrifts {
		if existingDrifts[i].DriftType == models.DriftTypeCode {
			existingCode = &existingDrifts[i]
			var prevSlots []models.AffectedSlotDrift
			_ = json.Unmarshal(existingDrifts[i].AffectedSlotsJSON, &prevSlots)
			gitSlots, _ = drift.SplitGitAndModuleDriftSlots(fromModelAffectedSlots(prevSlots))
			break
		}
	}

	modSlots := s.computeModuleDriftForRepoDoc(ctx, doc, nil, nil, forceScopeCheck)
	merged := toModelAffectedSlots(drift.MergeCodeAndModuleDriftSlots(gitSlots, modSlots))

	remoteSHA := doc.PublishedCommitSHA
	precision := models.DriftPrecisionSectionLevel
	repoLevelDrift := false
	if existingCode != nil {
		if existingCode.RemoteHeadSHA != "" {
			remoteSHA = existingCode.RemoteHeadSHA
		}
		if existingCode.Precision == models.DriftPrecisionRepoLevel && existingCode.HasDrift {
			repoLevelDrift = true
			if len(merged) == 0 {
				precision = models.DriftPrecisionRepoLevel
			}
		}
	} else if heads, hErr := s.driftRepo.ListRemoteHeadsByRepos(ctx, []uint{doc.RepoID}); hErr == nil && len(heads) > 0 && heads[0].RemoteHeadSHA != "" {
		remoteSHA = heads[0].RemoteHeadSHA
	}

	hasDrift := len(merged) > 0 || repoLevelDrift
	if err := s.upsertCodeDrift(ctx, doc.ID, hasDrift, precision, doc.PublishedCommitSHA, remoteSHA, merged, time.Now()); err != nil {
		log.Printf("[%s] 重算整仓文档模块漂移失败 (doc=%d): %v", config.ServiceName, doc.ID, err)
	}
}

func (s *DriftOrchestrator) computeModuleDriftForRepoDoc(
	ctx context.Context,
	doc *models.Document,
	pubRev *models.DocumentRevision,
	secEvs []models.SectionEvidence,
	forceScopeCheck bool,
) []drift.AffectedSlot {
	if doc == nil || doc.Subdirectory != "" || doc.PublishedRevisionID == nil {
		return nil
	}
	if pubRev == nil {
		rev, evs, err := s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if err != nil || rev == nil {
			return nil
		}
		pubRev = rev
		secEvs = evs
	}

	srcSlug := doc.DocTypeSlug
	srcSlot := "overview_boundaries"
	maxChars := 600
	if s.docTypeRepo != nil && pubRev.DocTypeRevisionID > 0 {
		if dtRev, rErr := s.docTypeRepo.GetRevisionByID(ctx, pubRev.DocTypeRevisionID); rErr == nil && dtRev != nil {
			var meta doctype.Meta
			if jErr := json.Unmarshal(dtRev.MetaJSON, &meta); jErr == nil {
				if len(meta.Slots) > 0 && meta.Slots[0].SlotID != "" {
					srcSlot = meta.Slots[0].SlotID
				}
				if meta.ModuleDigestSource != nil {
					if strings.TrimSpace(meta.ModuleDigestSource.DocTypeSlug) != "" {
						srcSlug = strings.TrimSpace(meta.ModuleDigestSource.DocTypeSlug)
					}
					if strings.TrimSpace(meta.ModuleDigestSource.SlotID) != "" {
						srcSlot = strings.TrimSpace(meta.ModuleDigestSource.SlotID)
					}
					if meta.ModuleDigestSource.MaxChars > 0 {
						maxChars = meta.ModuleDigestSource.MaxChars
					}
				}
			}
		}
	}

	var activeSubdirs []string
	if s.moduleRepo != nil {
		if mods, mErr := s.moduleRepo.ListModulesByRepo(ctx, doc.RepoID); mErr == nil {
			for _, m := range mods {
				if m.Status != models.ModuleStatusActive {
					continue
				}
				sub := strings.Trim(strings.TrimSpace(m.Subdirectory), "/")
				if sub != "" {
					activeSubdirs = append(activeSubdirs, sub)
				}
			}
		}
	}

	sectionsInput := buildSectionEvidenceInputs(pubRev, secEvs)
	candidateSubs := make(map[string]bool, len(activeSubdirs))
	for _, sub := range activeSubdirs {
		candidateSubs[sub] = true
	}
	for _, sec := range sectionsInput {
		for _, ev := range sec.Evidences {
			if ev.SymbolKind == "module_doc" {
				modPath := strings.Trim(strings.TrimSpace(ev.FQSymbol), "/")
				if modPath != "" {
					candidateSubs[modPath] = true
				}
			}
		}
	}
	if len(candidateSubs) == 0 {
		return nil
	}

	currentDigests := make(map[string]string, len(candidateSubs))
	for sub := range candidateSubs {
		if fp := s.resolveChildModuleDigestFingerprint(ctx, doc.RepoID, sub, doc.RefName, srcSlug, srcSlot, maxChars); fp != "" {
			currentDigests[sub] = fp
		}
	}

	return drift.DetectModuleDocDrift(drift.ModuleDocDriftInput{
		Sections:        sectionsInput,
		CurrentDigests:  currentDigests,
		ActiveModules:   activeSubdirs,
		OverviewSlotID:  srcSlot,
		ForceScopeCheck: forceScopeCheck,
	})
}

func (s *DriftOrchestrator) resolveChildModuleDigestFingerprint(
	ctx context.Context,
	repoID uint,
	subdir string,
	refName string,
	srcSlug string,
	srcSlot string,
	maxChars int,
) string {
	if s.docRepo == nil {
		return ""
	}
	modDoc, err := s.docRepo.GetByScope(ctx, repoID, subdir, refName, srcSlug)
	if err != nil || modDoc == nil {
		return ""
	}
	var targetRev *models.DocumentRevision
	if modDoc.Status == models.DocStatusApproved && modDoc.PublishedRevisionID != nil {
		if rev, _, rErr := s.docRepo.GetRevisionByIDWithSections(ctx, modDoc.ID, *modDoc.PublishedRevisionID); rErr == nil && rev != nil {
			targetRev = rev
		}
	}
	if targetRev == nil && modDoc.CurrentRevisionNo > 0 {
		if rev, _, rErr := s.docRepo.GetRevisionWithSections(ctx, modDoc.ID, modDoc.CurrentRevisionNo); rErr == nil && rev != nil {
			targetRev = rev
		}
	}
	if targetRev == nil {
		return ""
	}
	snaps := unmarshalSectionSnapshots(targetRev.SectionsJSON)
	var picked *models.DocumentSectionSnapshot
	for i := range snaps {
		if snaps[i].SlotID == srcSlot {
			picked = &snaps[i]
			break
		}
	}
	if picked == nil && len(snaps) > 0 {
		picked = &snaps[0]
	}
	if picked == nil || strings.TrimSpace(picked.Markdown) == "" {
		return ""
	}
	if picked.ContentSHA256 != "" {
		return picked.ContentSHA256
	}
	return sha256Hex(truncateRunes(strings.TrimSpace(picked.Markdown), maxChars))
}

// GetSummary 返回双维漂移汇总 KPI（GET /api/drift/summary）。
func (s *DriftOrchestrator) GetSummary(ctx context.Context) (*DriftSummaryVO, error) {
	rec, err := s.driftRepo.GetDriftSummary(ctx)
	if err != nil {
		return nil, err
	}
	return &DriftSummaryVO{
		TotalDriftedDocs:    rec.TotalDriftedDocs,
		CodeDriftDocs:       rec.CodeDriftDocs,
		SpecDriftDocs:       rec.SpecDriftDocs,
		BothDriftDocs:       rec.BothDriftDocs,
		MonitoredRepos:      rec.MonitoredRepos,
		UnhealthyRepos:      rec.UnhealthyRepos,
		LastPollAt:          rec.LastPollAt,
		PollIntervalMinutes: s.pollIntervalMinutes,
	}, nil
}

// ListDriftedDocuments 分页返回漂移文档明细与受影响槽位列表（GET /api/drift/documents）。
func (s *DriftOrchestrator) ListDriftedDocuments(
	ctx context.Context,
	filter models.DriftQueryFilter,
	page, pageSize int,
	user *commonModels.User,
) (*DriftDocumentListResponse, error) {
	p, ps := NormalizePagination(page, pageSize)
	filter.Offset = (p - 1) * ps
	filter.Limit = ps

	records, total, err := s.driftRepo.ListDriftedDocuments(ctx, filter)
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return &DriftDocumentListResponse{
			Items:    []DriftDocumentVO{},
			Total:    total,
			Page:     p,
			PageSize: ps,
		}, nil
	}

	// 批量补齐仓库名、远端 HEAD、DocType 展示名与修订号
	repoIDSet := make(map[uint]bool)
	repoIDs := make([]uint, 0, len(records))
	pubRevIDs := make([]uint, 0, len(records))
	for _, rec := range records {
		if !repoIDSet[rec.Document.RepoID] {
			repoIDSet[rec.Document.RepoID] = true
			repoIDs = append(repoIDs, rec.Document.RepoID)
		}
		if rec.Document.PublishedRevisionID != nil {
			pubRevIDs = append(pubRevIDs, *rec.Document.PublishedRevisionID)
		}
	}

	repoMap := make(map[uint]*commonModels.Repository, len(repoIDs))
	for _, rid := range repoIDs {
		if r, rErr := s.masterRepo.GetRepository(ctx, rid); rErr == nil && r != nil {
			repoMap[rid] = r
		}
	}

	heads, _ := s.driftRepo.ListRemoteHeadsByRepos(ctx, repoIDs)
	headByRepo := make(map[uint]string, len(heads))
	for _, h := range heads {
		if h.RemoteHeadSHA != "" {
			headByRepo[h.RepoID] = h.RemoteHeadSHA
		}
	}

	dts, _ := s.docTypeRepo.List(ctx, false)
	dtBySlug := make(map[string]models.DocType, len(dts))
	for _, dt := range dts {
		dtBySlug[dt.Slug] = dt
	}

	pubRevMap, _ := s.docRepo.ListRevisionsByIDs(ctx, pubRevIDs)
	dtRevNoCache := make(map[uint]int)
	getDTRevNo := func(revID uint) int {
		if revID == 0 {
			return 0
		}
		if no, ok := dtRevNoCache[revID]; ok {
			return no
		}
		if rev, rErr := s.docTypeRepo.GetRevisionByID(ctx, revID); rErr == nil && rev != nil {
			dtRevNoCache[revID] = rev.RevisionNo
			return rev.RevisionNo
		}
		return 0
	}

	items := make([]DriftDocumentVO, 0, len(records))
	for _, rec := range records {
		doc := rec.Document
		repoName := fmt.Sprintf("repo-%d", doc.RepoID)
		if r, ok := repoMap[doc.RepoID]; ok && r != nil {
			repoName = r.Name
		}

		dtName := doc.DocTypeSlug
		latestDTRevNo := 0
		if dt, ok := dtBySlug[doc.DocTypeSlug]; ok {
			dtName = dt.DisplayName
			latestDTRevNo = dt.LatestRevisionNo
		}

		boundDTRevID := doc.DocTypeRevisionID
		if doc.PublishedRevisionID != nil {
			if pubRev, ok := pubRevMap[*doc.PublishedRevisionID]; ok && pubRev.DocTypeRevisionID > 0 {
				boundDTRevID = pubRev.DocTypeRevisionID
			}
		}
		boundDTRevNo := getDTRevNo(boundDTRevID)

		remoteSHA := headByRepo[doc.RepoID]
		if remoteSHA == "" && rec.CodeDrift != nil && rec.CodeDrift.RemoteHeadSHA != "" {
			remoteSHA = rec.CodeDrift.RemoteHeadSHA
		}

		canWrite := false
		if user != nil && s.rbacSvc != nil {
			allowed, permErr := s.rbacSvc.CheckScopeWritePermission(ctx, user, doc.RepoID, doc.Subdirectory)
			if permErr == nil {
				canWrite = allowed
			}
		}

		var codeVO *DriftDimensionVO
		if rec.CodeDrift != nil && rec.CodeDrift.HasDrift {
			codeVO = toDriftDimensionVO(rec.CodeDrift)
		}
		var specVO *DriftDimensionVO
		if rec.SpecDrift != nil && rec.SpecDrift.HasDrift {
			specVO = toDriftDimensionVO(rec.SpecDrift)
		}

		items = append(items, DriftDocumentVO{
			DocumentID:              doc.ID,
			RepoID:                  doc.RepoID,
			RepoName:                repoName,
			Subdirectory:            doc.Subdirectory,
			DocTypeSlug:             doc.DocTypeSlug,
			DocTypeName:             dtName,
			CanWrite:                canWrite,
			PublishedCommitSHA:      doc.PublishedCommitSHA,
			RemoteHeadSHA:           remoteSHA,
			BoundDocTypeRevisionNo:  boundDTRevNo,
			LatestDocTypeRevisionNo: latestDTRevNo,
			CodeDrift:               codeVO,
			SpecDrift:               specVO,
		})
	}

	return &DriftDocumentListResponse{
		Items:    items,
		Total:    total,
		Page:     p,
		PageSize: ps,
	}, nil
}

// TriggerIncrementalRefresh 计算受影响非锁槽位（Code Drift ∪ Spec Drift \ LockedSlots）并发起单篇增量刷新任务（SC-11）。
func (s *DriftOrchestrator) TriggerIncrementalRefresh(
	ctx context.Context,
	docID uint,
	req IncrementalRefreshRequest,
	operatorID uint,
) (*GenTaskDetailVO, error) {
	if s.genSvc == nil {
		return nil, fmt.Errorf("生成编排服务未就绪")
	}

	doc, err := s.docRepo.GetByID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if doc.CurrentRevisionNo <= 0 {
		return nil, fmt.Errorf("%w: 文档尚未生成初始修订", ErrInvalidTaskParam)
	}

	curRev, curEvs, err := s.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
	if err != nil {
		return nil, err
	}
	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(curRev.SectionsJSON), curEvs)
	ownershipBySlot := make(map[string]string, len(snaps))
	for _, sn := range snaps {
		ownershipBySlot[sn.SlotID] = sn.Ownership
	}

	drifts, err := s.driftRepo.ListDriftsByDocIDs(ctx, []uint{doc.ID})
	if err != nil {
		return nil, err
	}

	hasCodeDrift := false
	driftedSlotOrder := make([]string, 0)
	driftedSeen := make(map[string]bool)
	for _, dr := range drifts {
		if !dr.HasDrift {
			continue
		}
		if dr.DriftType == models.DriftTypeCode {
			hasCodeDrift = true
		}
		var affected []models.AffectedSlotDrift
		_ = json.Unmarshal(dr.AffectedSlotsJSON, &affected)
		for _, a := range affected {
			if a.ChangeKind == "removed" {
				// 被新模板移除的槽位不进入重生成列表（在合并阶段自动走孤立保护或删除）
				continue
			}
			if a.SlotID != "" && !driftedSeen[a.SlotID] {
				driftedSeen[a.SlotID] = true
				driftedSlotOrder = append(driftedSlotOrder, a.SlotID)
			}
		}
		// 若为 repo_level 降级漂移（无细粒度受影响槽位列表），则覆盖全部非锁定槽位
		if dr.DriftType == models.DriftTypeCode && dr.Precision == models.DriftPrecisionRepoLevel && len(affected) == 0 {
			for _, sn := range snaps {
				if !sn.IsOrphan && !driftedSeen[sn.SlotID] {
					driftedSeen[sn.SlotID] = true
					driftedSlotOrder = append(driftedSlotOrder, sn.SlotID)
				}
			}
		}
	}

	candidateSlots := driftedSlotOrder
	if len(req.SlotIDs) > 0 {
		candidateSlots = make([]string, 0, len(req.SlotIDs))
		reqSeen := make(map[string]bool)
		for _, raw := range req.SlotIDs {
			sid := strings.TrimSpace(raw)
			if sid != "" && !reqSeen[sid] {
				reqSeen[sid] = true
				candidateSlots = append(candidateSlots, sid)
			}
		}
	}

	refreshable := make([]string, 0, len(candidateSlots))
	driftedLocked := make(map[string]bool)
	for _, sid := range candidateSlots {
		if ownershipBySlot[sid] == models.OwnershipLocked {
			driftedLocked[sid] = true
			continue
		}
		refreshable = append(refreshable, sid)
	}

	if len(refreshable) == 0 {
		return nil, ErrNoRefreshableSlots
	}

	return s.genSvc.SubmitIncrementalRefreshTask(ctx, IncrementalRefreshTaskInput{
		Document:           doc,
		TargetSlots:        refreshable,
		DriftedLockedSlots: driftedLocked,
		NeedSyncCode:       hasCodeDrift,
		Priority:           models.TaskPriorityInteractive,
		Engine:             req.Engine,
		ExcludeRefDocIDs:   req.ExcludeRefDocIDs,
	}, operatorID)
}

func (s *DriftOrchestrator) ensureMetadataGitDir(ctx context.Context, localPath, repoURL, remoteHeadSHA string) (string, func(), bool) {
	if _, err := os.Stat(filepath.Join(localPath, ".git")); err == nil {
		return localPath, nil, true
	}
	if _, err := os.Stat(filepath.Join(localPath, "HEAD")); err == nil {
		return localPath, nil, true
	}
	if strings.TrimSpace(repoURL) == "" {
		return "", nil, false
	}

	// 本地沙箱已被 LRU 回收时尝试极简裸元数据拉取 (--bare --filter=blob:none)
	tmpDir, err := os.MkdirTemp("", "archdoc-drift-meta-*")
	if err != nil {
		return "", nil, false
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	cloneCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	if _, _, cloneErr := gitsync.ExecCommandWithProcessGroup(cloneCtx, "", "git", "clone", "--bare", "--filter=blob:none", repoURL, tmpDir); cloneErr == nil {
		if fetchErr := gitsync.FetchCommitMetadata(cloneCtx, tmpDir, repoURL, remoteHeadSHA); fetchErr != nil {
			log.Printf("[%s] 裸仓拉取提交元数据 %s 失败: %v", config.ServiceName, remoteHeadSHA, fetchErr)
		}
		return tmpDir, cleanup, true
	}

	cleanup()
	return "", nil, false
}

func (s *DriftOrchestrator) upsertCodeDrift(
	ctx context.Context,
	docID uint,
	hasDrift bool,
	precision string,
	baseSHA, remoteSHA string,
	affected []models.AffectedSlotDrift,
	detectedAt time.Time,
) error {
	if affected == nil {
		affected = []models.AffectedSlotDrift{}
	}
	affectedJSON, err := json.Marshal(affected)
	if err != nil {
		return err
	}
	return s.driftRepo.UpsertDocumentDrift(ctx, &models.DocumentDrift{
		DocumentID:        docID,
		DriftType:         models.DriftTypeCode,
		HasDrift:          hasDrift,
		Precision:         precision,
		BaseCommitSHA:     baseSHA,
		RemoteHeadSHA:     remoteSHA,
		AffectedSlotCount: len(affected),
		AffectedSlotsJSON: datatypes.JSON(affectedJSON),
		DetectedAt:        detectedAt,
	})
}

func (s *DriftOrchestrator) extractSingleFileFingerprints(ctx context.Context, relPath string, content []byte) (map[string]string, error) {
	if s.aggregator == nil {
		return nil, fmt.Errorf("Code IR Aggregator 未初始化")
	}
	lang := detectDriftFileLanguage(relPath)
	out := make(map[string]string)

	// 1. 多语言 tree-sitter 符号（C/C++/Java/Python）
	if lang != "" {
		syms := s.aggregator.ExtractSingleFileSymbols(ctx, relPath, lang, content)
		for _, sym := range syms {
			if sym.FQSymbol != "" && sym.Meta.ContentFingerprint != "" {
				out[sym.FQSymbol] = sym.Meta.ContentFingerprint
			}
		}
	}

	// 2. Protobuf / CROS RPC 提取
	if lang == "proto" {
		if res, err := codeir.NewProtoExtractor().ExtractFile(ctx, codeir.FileEntry{RelPath: relPath, Language: "proto", Size: int64(len(content))}, content); err == nil && res != nil {
			for _, pf := range res.IDLContracts {
				for _, svc := range pf.Services {
					for _, rpc := range svc.RPCs {
						fullKey := svc.FullName + "/" + rpc.MethodName
						out[fullKey] = rpc.Meta.ContentFingerprint
						out[svc.Name+"/"+rpc.MethodName] = rpc.Meta.ContentFingerprint
					}
				}
			}
		}
	}

	// 3. 配置键提取
	if lang == "config" {
		if res, err := codeir.NewConfigExtractor().ExtractFile(ctx, codeir.FileEntry{RelPath: relPath, Language: "config", Size: int64(len(content))}, content); err == nil && res != nil {
			for _, ck := range res.ConfigKeys {
				out[ck.KeyPath] = ck.Meta.ContentFingerprint
			}
		}
	}

	// 4. SQL DDL / 轻量 C++ HTTP 路由
	if lang == "sql" || lang == "c" || lang == "cpp" {
		if res, err := codeir.NewCommonExtractor().ExtractFile(ctx, codeir.FileEntry{RelPath: relPath, Language: lang, Size: int64(len(content))}, content); err == nil && res != nil {
			for _, dm := range res.DataModels {
				out[dm.TableOrCollection] = dm.Meta.ContentFingerprint
				out[dm.EntityName] = dm.Meta.ContentFingerprint
			}
			for _, rt := range res.HTTPRoutes {
				out[strings.ToUpper(rt.Method)+" "+rt.Path] = rt.Meta.ContentFingerprint
			}
		}
	}

	return out, nil
}

func buildSectionEvidenceInputs(rev *models.DocumentRevision, secEvs []models.SectionEvidence) []drift.SectionEvidenceInput {
	snaps := overlaySectionMetadata(unmarshalSectionSnapshots(rev.SectionsJSON), secEvs)
	evBySlot := make(map[string][]models.EvidenceAnchor, len(secEvs))
	for _, se := range secEvs {
		var anchors []models.EvidenceAnchor
		_ = json.Unmarshal(se.EvidencesJSON, &anchors)
		evBySlot[se.SlotID] = anchors
	}

	out := make([]drift.SectionEvidenceInput, 0, len(snaps))
	for _, sn := range snaps {
		rawAnchors := evBySlot[sn.SlotID]
		anchors := make([]drift.AnchorEvidenceInput, 0, len(rawAnchors))
		for _, a := range rawAnchors {
			anchors = append(anchors, drift.AnchorEvidenceInput{
				Scope:              a.Scope,
				FilePath:           a.FilePath,
				SymbolKind:         a.SymbolKind,
				FQSymbol:           a.FQSymbol,
				ContentFingerprint: a.ContentFingerprint,
				LineStart:          a.LineStart,
				LineEnd:            a.LineEnd,
			})
		}
		out = append(out, drift.SectionEvidenceInput{
			SlotID:    sn.SlotID,
			SlotTitle: sn.Title,
			Ownership: sn.Ownership,
			Evidences: anchors,
		})
	}
	return out
}

func toDriftDimensionVO(dr *models.DocumentDrift) *DriftDimensionVO {
	if dr == nil {
		return nil
	}
	var affected []models.AffectedSlotDrift
	_ = json.Unmarshal(dr.AffectedSlotsJSON, &affected)
	if affected == nil {
		affected = []models.AffectedSlotDrift{}
	}
	return &DriftDimensionVO{
		HasDrift:          dr.HasDrift,
		Precision:         dr.Precision,
		AffectedSlotCount: dr.AffectedSlotCount,
		AffectedSlots:     affected,
		DetectedAt:        dr.DetectedAt,
	}
}

func toModelAffectedSlots(in []drift.AffectedSlot) []models.AffectedSlotDrift {
	out := make([]models.AffectedSlotDrift, 0, len(in))
	for _, sl := range in {
		out = append(out, models.AffectedSlotDrift{
			SlotID:         sl.SlotID,
			SlotTitle:      sl.SlotTitle,
			Ownership:      sl.Ownership,
			Reason:         sl.Reason,
			ChangeKind:     sl.ChangeKind,
			ChangedSymbols: sl.ChangedSymbols,
			ChangedFiles:   sl.ChangedFiles,
		})
	}
	return out
}

func fromModelAffectedSlots(in []models.AffectedSlotDrift) []drift.AffectedSlot {
	out := make([]drift.AffectedSlot, 0, len(in))
	for _, sl := range in {
		out = append(out, drift.AffectedSlot{
			SlotID:         sl.SlotID,
			SlotTitle:      sl.SlotTitle,
			Ownership:      sl.Ownership,
			Reason:         sl.Reason,
			ChangeKind:     sl.ChangeKind,
			ChangedSymbols: sl.ChangedSymbols,
			ChangedFiles:   sl.ChangedFiles,
		})
	}
	return out
}

func detectDriftFileLanguage(relPath string) string {
	ext := strings.ToLower(path.Ext(relPath))
	switch ext {
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".h":
		return "cpp"
	case ".c":
		return "c"
	case ".java":
		return "java"
	case ".py":
		return "python"
	case ".proto":
		return "proto"
	case ".sql":
		return "sql"
	case ".yaml", ".yml", ".json", ".toml", ".ini", ".conf", ".cfg", ".properties", ".env":
		return "config"
	default:
		return ""
	}
}
