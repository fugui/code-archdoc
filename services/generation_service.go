package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/security"
	"code-archdoc/pkg/verifier"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

var (
	// ErrDocTypeInactive 所选 DocType 不存在或已停用（HTTP 400 DOC_TYPE_INACTIVE）。
	ErrDocTypeInactive = errors.New("存在已停用或不存在的文档类型")
	// ErrInvalidTaskParam 任务参数非法（HTTP 400 INVALID_PARAM）。
	ErrInvalidTaskParam = errors.New("任务请求参数非法")
	// ErrTaskAlreadyTerminal 任务已处于终态不可取消（HTTP 409 CONFLICT）。
	ErrTaskAlreadyTerminal = errors.New("任务已处于终态，不可取消")
	// ErrSlotNotRetryable 槽位子任务非失败状态不可重试（HTTP 409 CONFLICT）。
	ErrSlotNotRetryable = errors.New("仅允许对失败状态的槽位子任务发起独立重试")
)

// GenerateTaskRequest 对应 POST /api/tasks/generate 入参。
type GenerateTaskRequest struct {
	RepoID       uint     `json:"repo_id"`
	Subdirectory string   `json:"subdirectory"`
	RefName      string   `json:"ref_name"`
	DocTypes     []string `json:"doc_types"`
	Mode         string   `json:"mode"` // generate | regenerate
}

// GenSlotTaskVO 对应任务详情中的单个槽位子任务视图。
type GenSlotTaskVO struct {
	ID                uint       `json:"id"`
	DocTypeSlug       string     `json:"doc_type_slug"`
	DocTypeRevisionID uint       `json:"doc_type_revision_id"`
	SlotID            string     `json:"slot_id"`
	DocumentID        *uint      `json:"document_id"`
	Status            string     `json:"status"`
	AttemptCount      int        `json:"attempt_count"`
	ModelTier         string     `json:"model_tier"`
	ErrorMessage      string     `json:"error_message"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

// GenTaskDetailVO 对应 POST /api/tasks/generate (202) 与 GET /api/tasks/:task_id (200) 响应。
type GenTaskDetailVO struct {
	ID               uint                 `json:"id"`
	RepoID           uint                 `json:"repo_id"`
	RepoName         string               `json:"repo_name,omitempty"`
	Subdirectory     string               `json:"subdirectory"`
	RefName          string               `json:"ref_name"`
	SnapshotID       *uint                `json:"snapshot_id,omitempty"`
	CommitSHA        string               `json:"commit_sha,omitempty"`
	TaskKind         string               `json:"task_kind"`
	Priority         string               `json:"priority"`
	DocTypes         []string             `json:"doc_types"`
	TargetDocumentID *uint                `json:"target_document_id,omitempty"`
	TargetSlots      []string             `json:"target_slots,omitempty"`
	Status           string               `json:"status"`
	ProgressPercent  int                  `json:"progress_percent"`
	EstimatedTokens  int                  `json:"estimated_tokens"`
	ErrorCode        string               `json:"error_code,omitempty"`
	ErrorMessage     string               `json:"error_message,omitempty"`
	BudgetAdvice     *models.BudgetAdvice `json:"budget_advice,omitempty"`
	SlotTasks        []GenSlotTaskVO      `json:"slot_tasks,omitempty"`
	TriggeredBy      uint                 `json:"triggered_by"`
	StartedAt        *time.Time           `json:"started_at"`
	FinishedAt       *time.Time           `json:"finished_at"`
	CreatedAt        time.Time            `json:"created_at"`
}

// TaskListResponse 对应 GET /api/tasks 分页响应。
type TaskListResponse struct {
	Items    []GenTaskDetailVO `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// CancelTaskVO 对应 POST /api/tasks/:task_id/cancel 响应。
type CancelTaskVO struct {
	ID     uint   `json:"id"`
	Status string `json:"status"`
}

// GenerationOrchestrator 编排双层任务状态机、预算熔断、共享术语表、槽位并发生成、取消与单槽位独立重试（F-05）。
type GenerationOrchestrator struct {
	masterRepo  SharedMasterRepo
	docTypeRepo DocTypeRepo
	docTypeSvc  *DocTypeService
	snapSvc     *SnapshotService
	taskRepo    TaskRepo
	docRepo     DocumentRepo
	govRepo     GovernanceRepo
	keyCipher   *security.Cipher
	llmClient   *llm.Client
	verifier    *verifier.Verifier
	leaseMgr    *ActiveLeaseManager
	scheduler   *TaskScheduler

	llmSem      chan struct{}
	baseCtx     context.Context
	baseCancel  context.CancelFunc
	cancelMu    sync.Mutex
	cancelFuncs map[uint]context.CancelFunc
	wg          sync.WaitGroup
}

// WithGovernance 注入活跃租约管理器与双优先级调度器（F-27）。
func (o *GenerationOrchestrator) WithGovernance(leaseMgr *ActiveLeaseManager, scheduler *TaskScheduler) *GenerationOrchestrator {
	o.leaseMgr = leaseMgr
	o.scheduler = scheduler
	return o
}

func (o *GenerationOrchestrator) acquireLLMSlot(ctx context.Context) (func(), error) {
	if o.scheduler != nil {
		return o.scheduler.AcquireLLMSlot(ctx)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case o.llmSem <- struct{}{}:
	}
	if ctx.Err() != nil {
		<-o.llmSem
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() { <-o.llmSem })
	}, nil
}

func (o *GenerationOrchestrator) dispatchJob(job ScheduledJob) error {
	wrapped := job.Run
	origDrop := job.OnDrop
	o.wg.Add(1)
	job.Run = func(runCtx context.Context) {
		defer o.wg.Done()
		wrapped(runCtx)
	}
	job.OnDrop = func() {
		defer o.wg.Done()
		if origDrop != nil {
			origDrop()
		}
	}
	if o.scheduler != nil {
		if err := o.scheduler.Enqueue(job); err != nil {
			o.wg.Done()
			return err
		}
		return nil
	}
	go job.Run(job.Ctx)
	return nil
}

func NewGenerationOrchestrator(
	masterRepo SharedMasterRepo,
	docTypeRepo DocTypeRepo,
	docTypeSvc *DocTypeService,
	snapSvc *SnapshotService,
	taskRepo TaskRepo,
	docRepo DocumentRepo,
	govRepo GovernanceRepo,
	keyCipher *security.Cipher,
	llmClient *llm.Client,
	maxLLMConcurrency int,
) *GenerationOrchestrator {
	if llmClient == nil {
		llmClient = llm.NewClient()
	}
	if maxLLMConcurrency <= 0 {
		maxLLMConcurrency = 8
	}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	return &GenerationOrchestrator{
		masterRepo:  masterRepo,
		docTypeRepo: docTypeRepo,
		docTypeSvc:  docTypeSvc,
		snapSvc:     snapSvc,
		taskRepo:    taskRepo,
		docRepo:     docRepo,
		govRepo:     govRepo,
		keyCipher:   keyCipher,
		llmClient:   llmClient,
		verifier:    verifier.New(),
		llmSem:      make(chan struct{}, maxLLMConcurrency),
		baseCtx:     baseCtx,
		baseCancel:  baseCancel,
		cancelFuncs: make(map[uint]context.CancelFunc),
	}
}

// CancelAll 立即取消所有运行中与排队中的生成/增量刷新/槽位重试任务（供优雅停机释放 Git 进程组与 LLM 请求）。
func (o *GenerationOrchestrator) CancelAll() {
	if o.baseCancel != nil {
		o.baseCancel()
	}
	o.cancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(o.cancelFuncs))
	for _, fn := range o.cancelFuncs {
		if fn != nil {
			cancels = append(cancels, fn)
		}
	}
	o.cancelMu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// WaitAll 等待所有运行中的后台生成协程退出（供单测与优雅停机使用）。
func (o *GenerationOrchestrator) WaitAll() {
	o.wg.Wait()
}

// WaitAllWithContext 在给定截止时间内等待所有运行中生成协程退出，超时返回 ctx.Err()。
func (o *GenerationOrchestrator) WaitAllWithContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitGenerateTask 校验参数并异步入队生成任务（返回 202 视图）。
func (o *GenerationOrchestrator) SubmitGenerateTask(
	ctx context.Context,
	req GenerateTaskRequest,
	operatorID uint,
) (*GenTaskDetailVO, error) {
	if req.RepoID == 0 {
		return nil, fmt.Errorf("%w: repo_id 必填", ErrInvalidTaskParam)
	}
	normSubdir, err := sandbox.NormalizeSubdir(req.Subdirectory)
	if err != nil {
		return nil, err
	}
	if len(req.DocTypes) == 0 {
		return nil, fmt.Errorf("%w: doc_types 不能为空", ErrInvalidTaskParam)
	}

	taskKind := strings.TrimSpace(req.Mode)
	if taskKind == "" {
		taskKind = models.TaskKindGenerate
	}
	if taskKind != models.TaskKindGenerate && taskKind != models.TaskKindRegenerate {
		return nil, fmt.Errorf("%w: mode 仅支持 generate 或 regenerate", ErrInvalidTaskParam)
	}

	repo, err := o.masterRepo.GetRepository(ctx, req.RepoID)
	if err != nil {
		return nil, err
	}

	// 去重并校验全部勾选的 DocType 均存在且启用中
	seenSlugs := make(map[string]bool, len(req.DocTypes))
	dedupSlugs := make([]string, 0, len(req.DocTypes))
	for _, rawSlug := range req.DocTypes {
		slug := strings.TrimSpace(rawSlug)
		if slug == "" || seenSlugs[slug] {
			continue
		}
		if err := doctype.ValidateSlug(slug); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrDocTypeInactive, slug)
		}
		dt, rev, dtErr := o.docTypeRepo.GetBySlug(ctx, slug)
		if dtErr != nil || dt == nil || !dt.IsActive || rev == nil {
			return nil, fmt.Errorf("%w: %s", ErrDocTypeInactive, slug)
		}
		seenSlugs[slug] = true
		dedupSlugs = append(dedupSlugs, slug)
	}
	if len(dedupSlugs) == 0 {
		return nil, fmt.Errorf("%w: doc_types 不能为空", ErrInvalidTaskParam)
	}

	refName := strings.TrimSpace(req.RefName)
	if refName == "" {
		refName = strings.TrimSpace(repo.Branch)
	}
	if refName == "" {
		refName = "master"
	}

	docTypesJSON, _ := json.Marshal(dedupSlugs)
	task := &models.GenTask{
		RepoID:          repo.ID,
		Subdirectory:    normSubdir,
		RefName:         refName,
		TaskKind:        taskKind,
		Priority:        models.TaskPriorityInteractive,
		DocTypesJSON:    datatypes.JSON(docTypesJSON),
		Status:          models.TaskStatusPending,
		ProgressPercent: 0,
		TriggeredBy:     operatorID,
	}
	if err := o.taskRepo.CreateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("创建生成任务失败: %w", err)
	}

	// 预先注册可取消上下文（继承全局 baseCtx，确保停机时可一键取消），确保在异步执行前即可响应 CancelTask
	taskCtx, cancel := context.WithCancel(o.baseCtx)
	o.registerCancel(task.ID, cancel)

	if err := o.dispatchJob(ScheduledJob{
		ID:       fmt.Sprintf("gen-task-%d", task.ID),
		UserID:   operatorID,
		Priority: task.Priority,
		Ctx:      taskCtx,
		Run: func(runCtx context.Context) {
			defer o.unregisterCancel(task.ID)
			o.executeGenerateTask(runCtx, task.ID, repo, dedupSlugs, operatorID)
		},
		OnDrop: func() {
			o.unregisterCancel(task.ID)
			cancel()
			o.markTaskCanceled(context.Background(), task.ID, models.TaskStatusPending)
		},
	}); err != nil {
		o.unregisterCancel(task.ID)
		cancel()
		o.markTaskFailed(ctx, task.ID, models.TaskStatusPending, "QUEUE_FULL", err.Error(), nil, 0)
		return nil, err
	}

	vo := toGenTaskDetailVO(task, repo.Name, "", nil)
	return &vo, nil
}

// IncrementalRefreshTaskInput 定义单篇文档章节级增量刷新任务入参（F-12 / SC-11）。
type IncrementalRefreshTaskInput struct {
	Document           *models.Document
	TargetSlots        []string        // 仅含受影响且非 locked 的槽位
	DriftedLockedSlots map[string]bool // 受漂移影响但被锁定的槽位（保留原字节并标 needs_drift_review=true）
	NeedSyncCode       bool
	Priority           string
}

// SubmitIncrementalRefreshTask 发起单篇文档的章节级增量刷新任务（返回 202 视图）：
// 仅对 TargetSlots \ LockedSlots 调用 LLM，未受影响章节与 locked 章节 content_sha256 严格字节级不变。
func (o *GenerationOrchestrator) SubmitIncrementalRefreshTask(
	ctx context.Context,
	in IncrementalRefreshTaskInput,
	operatorID uint,
) (*GenTaskDetailVO, error) {
	if in.Document == nil || in.Document.ID == 0 {
		return nil, fmt.Errorf("%w: 文档不能为空", ErrInvalidTaskParam)
	}
	if len(in.TargetSlots) == 0 {
		return nil, fmt.Errorf("%w: 无可刷新的非锁定受影响槽位", ErrInvalidTaskParam)
	}
	repo, err := o.masterRepo.GetRepository(ctx, in.Document.RepoID)
	if err != nil {
		return nil, err
	}

	priority := in.Priority
	if !models.IsValidTaskPriority(priority) {
		priority = models.TaskPriorityInteractive
	}

	docTypesJSON, _ := json.Marshal([]string{in.Document.DocTypeSlug})
	targetSlotsJSON, _ := json.Marshal(in.TargetSlots)
	docID := in.Document.ID
	task := &models.GenTask{
		RepoID:           repo.ID,
		Subdirectory:     in.Document.Subdirectory,
		RefName:          in.Document.RefName,
		TaskKind:         models.TaskKindIncrementalRefresh,
		Priority:         priority,
		DocTypesJSON:     datatypes.JSON(docTypesJSON),
		TargetDocumentID: &docID,
		TargetSlotsJSON:  datatypes.JSON(targetSlotsJSON),
		Status:           models.TaskStatusPending,
		ProgressPercent:  0,
		TriggeredBy:      operatorID,
	}
	if err := o.taskRepo.CreateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("创建增量刷新任务失败: %w", err)
	}

	taskCtx, cancel := context.WithCancel(o.baseCtx)
	o.registerCancel(task.ID, cancel)

	docCopy := *in.Document
	targetSlotsCopy := append([]string(nil), in.TargetSlots...)
	lockedDriftCopy := make(map[string]bool, len(in.DriftedLockedSlots))
	for k, v := range in.DriftedLockedSlots {
		if v {
			lockedDriftCopy[k] = true
		}
	}

	if err := o.dispatchJob(ScheduledJob{
		ID:       fmt.Sprintf("inc-refresh-%d", task.ID),
		UserID:   operatorID,
		Priority: priority,
		Ctx:      taskCtx,
		Run: func(runCtx context.Context) {
			defer o.unregisterCancel(task.ID)
			o.executeIncrementalRefreshTask(runCtx, task.ID, repo, &docCopy, targetSlotsCopy, lockedDriftCopy, in.NeedSyncCode, operatorID)
		},
		OnDrop: func() {
			o.unregisterCancel(task.ID)
			cancel()
			o.markTaskCanceled(context.Background(), task.ID, models.TaskStatusPending)
		},
	}); err != nil {
		o.unregisterCancel(task.ID)
		cancel()
		o.markTaskFailed(ctx, task.ID, models.TaskStatusPending, "QUEUE_FULL", err.Error(), nil, 0)
		return nil, err
	}

	vo := toGenTaskDetailVO(task, repo.Name, "", nil)
	return &vo, nil
}

// GetTaskScope 返回任务所属 (repo_id, subdirectory)，供 Handler 在取消/重试前做三层 RBAC 校验。
func (o *GenerationOrchestrator) GetTaskScope(ctx context.Context, taskID uint) (uint, string, error) {
	task, _, err := o.taskRepo.GetTaskWithSlots(ctx, taskID)
	if err != nil {
		return 0, "", err
	}
	return task.RepoID, task.Subdirectory, nil
}

// GetTaskDetail 获取主任务详情、预算熔断建议及全部槽位子任务状态。
func (o *GenerationOrchestrator) GetTaskDetail(ctx context.Context, taskID uint) (*GenTaskDetailVO, error) {
	task, slots, err := o.taskRepo.GetTaskWithSlots(ctx, taskID)
	if err != nil {
		return nil, err
	}
	repoName := ""
	if repo, rErr := o.masterRepo.GetRepository(ctx, task.RepoID); rErr == nil && repo != nil {
		repoName = repo.Name
	}
	commitSHA := ""
	if task.SnapshotID != nil {
		if snap, _, _, sErr := o.snapSvc.LoadIRBundleBySnapshotID(ctx, *task.SnapshotID); sErr == nil && snap != nil {
			commitSHA = snap.CommitSHA
		}
	}
	vo := toGenTaskDetailVO(task, repoName, commitSHA, slots)
	return &vo, nil
}

// ListTasks 分页查询任务执行历史。
func (o *GenerationOrchestrator) ListTasks(ctx context.Context, filter models.TaskListFilter, page, pageSize int) (*TaskListResponse, error) {
	p, ps := NormalizePagination(page, pageSize)
	filter.Offset = (p - 1) * ps
	filter.Limit = ps

	tasks, total, err := o.taskRepo.ListTasks(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]GenTaskDetailVO, 0, len(tasks))
	for i := range tasks {
		vo := toGenTaskDetailVO(&tasks[i], "", "", nil)
		items = append(items, vo)
	}
	return &TaskListResponse{
		Items:    items,
		Total:    total,
		Page:     p,
		PageSize: ps,
	}, nil
}

// CancelTask 取消排队中或运行中的任务，立即终止后续 LLM 请求并清理未执行槽位。
func (o *GenerationOrchestrator) CancelTask(ctx context.Context, taskID uint) (*CancelTaskVO, error) {
	task, slots, err := o.taskRepo.GetTaskWithSlots(ctx, taskID)
	if err != nil {
		return nil, err
	}

	if task.Status == models.TaskStatusReady || task.Status == models.TaskStatusFailed || task.Status == models.TaskStatusCanceled {
		return nil, ErrTaskAlreadyTerminal
	}

	// 立即触发 ctx 取消（中止正在进行的 HTTP/Git 调用并阻断后续槽位发出 LLM 请求）
	o.triggerCancel(taskID)

	now := time.Now()
	// 尝试将主任务从当前活跃态原子流转至 canceled
	for _, fromStatus := range []string{task.Status, models.TaskStatusPending, models.TaskStatusExtracting, models.TaskStatusGenerating} {
		if transErr := o.taskRepo.TransitionTaskStatus(ctx, taskID, fromStatus, models.TaskStatusCanceled, map[string]any{
			"error_code":    "TASK_CANCELED",
			"error_message": "任务已被用户主动取消",
			"finished_at":   now,
		}); transErr == nil {
			break
		}
	}

	for i := range slots {
		if slots[i].Status == models.SlotTaskPending || slots[i].Status == models.SlotTaskRunning {
			slots[i].Status = models.SlotTaskCanceled
			slots[i].FinishedAt = &now
			_ = o.taskRepo.UpdateSlotTask(ctx, &slots[i])
		}
	}

	return &CancelTaskVO{
		ID:     taskID,
		Status: models.TaskStatusCanceled,
	}, nil
}

// RetrySlotTask 对单个失败槽位发起独立重试（其余已完成槽位内容字节级不变，F-05）。
func (o *GenerationOrchestrator) RetrySlotTask(
	ctx context.Context,
	taskID, slotTaskID uint,
	operatorID uint,
) (*GenSlotTaskVO, error) {
	task, _, err := o.taskRepo.GetTaskWithSlots(ctx, taskID)
	if err != nil {
		return nil, err
	}
	slotTask, err := o.taskRepo.GetSlotTask(ctx, taskID, slotTaskID)
	if err != nil {
		return nil, err
	}
	if slotTask.Status != models.SlotTaskFailed {
		return nil, ErrSlotNotRetryable
	}
	if task.SnapshotID == nil {
		return nil, fmt.Errorf("%w: 任务尚未生成有效快照，请重新触发整篇生成", ErrInvalidTaskParam)
	}

	slotTask.Status = models.SlotTaskPending
	slotTask.AttemptCount++
	slotTask.ErrorMessage = ""
	slotTask.FinishedAt = nil
	if err := o.taskRepo.UpdateSlotTask(ctx, slotTask); err != nil {
		return nil, err
	}

	retrySlotID := slotTask.ID
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.executeSingleSlotRetry(o.baseCtx, task, retrySlotID, operatorID)
	}()

	vo := toGenSlotTaskVO(slotTask)
	return &vo, nil
}

func (o *GenerationOrchestrator) executeGenerateTask(
	ctx context.Context,
	taskID uint,
	repo *commonModels.Repository,
	slugs []string,
	operatorID uint,
) {
	dbCtx := context.Background()
	now := time.Now()

	// 1. pending -> extracting
	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusPending, models.TaskStatusExtracting, map[string]any{
		"started_at":       now,
		"progress_percent": 10,
	}); err != nil {
		return
	}

	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusExtracting)
		return
	}

	task, _, err := o.taskRepo.GetTaskWithSlots(dbCtx, taskID)
	if err != nil {
		return
	}

	// 2. Git 同步 + L1 Code IR 抽取（Singleflight 去重 + 缓存命中复用），期间持 repo/snapshot 租约
	releaseRepoLease := o.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	snapReady, err := o.snapSvc.EnsureSnapshot(ctx, repo, task.Subdirectory, task.RefName)
	if err != nil {
		if ctx.Err() != nil {
			o.markTaskCanceled(dbCtx, taskID, models.TaskStatusExtracting)
			return
		}
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "EXTRACT_FAILED", err.Error(), nil, 0)
		return
	}

	snapID := snapReady.Snapshot.ID
	releaseSnapLease := o.leaseMgr.AcquireSnapshot(snapID)
	defer releaseSnapLease()
	_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{
		"snapshot_id":      snapID,
		"progress_percent": 20,
	})

	// 3. 加载全部选中 DocType 的目录资产、DB 最新修订 ID 及既有文档的锁定槽位
	type docTypePlan struct {
		slug           string
		asset          *doctype.AssetBundle
		revID          uint
		existingDoc    *models.Document
		priorCommitSHA string
		priorOrdered   []models.DocumentSectionSnapshot
		priorSections  map[string]models.DocumentSectionSnapshot
		priorEvAnchors map[string][]models.EvidenceAnchor
	}

	plans := make([]*docTypePlan, 0, len(slugs))
	assetsForBudget := make([]*doctype.AssetBundle, 0, len(slugs))
	lockedByDoc := make(map[string]map[string]bool)

	for _, slug := range slugs {
		dtModel, dtRev, dtErr := o.docTypeRepo.GetBySlug(dbCtx, slug)
		if dtErr != nil || dtModel == nil || dtRev == nil {
			o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_TYPE_NOT_FOUND", fmt.Sprintf("加载文档类型 %s 失败", slug), nil, 0)
			return
		}
		asset, loadErr := o.loadDocTypeAssetFromRevision(dtRev, slug)
		if loadErr != nil {
			o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_TYPE_INVALID", loadErr.Error(), nil, 0)
			return
		}

		plan := &docTypePlan{
			slug:           slug,
			asset:          asset,
			revID:          dtRev.ID,
			priorSections:  make(map[string]models.DocumentSectionSnapshot),
			priorEvAnchors: make(map[string][]models.EvidenceAnchor),
		}

		// 检查已有文档的 locked 槽位（叠加 SectionEvidence 实时所有权）以便豁免覆盖并免计预算
		if exDoc, exErr := o.docRepo.GetByScope(dbCtx, repo.ID, task.Subdirectory, snapReady.Snapshot.RefName, slug); exErr == nil && exDoc != nil {
			plan.existingDoc = exDoc
			if exDoc.CurrentRevisionNo > 0 {
				if prevRev, prevEvs, pErr := o.docRepo.GetRevisionWithSections(dbCtx, exDoc.ID, exDoc.CurrentRevisionNo); pErr == nil && prevRev != nil {
					plan.priorCommitSHA = prevRev.CommitSHA
					snaps := overlaySectionMetadata(unmarshalSectionSnapshots(prevRev.SectionsJSON), prevEvs)
					plan.priorOrdered = snaps
					for _, sn := range snaps {
						plan.priorSections[sn.SlotID] = sn
						if sn.Ownership == models.OwnershipLocked && strings.TrimSpace(sn.Markdown) != "" {
							if lockedByDoc[slug] == nil {
								lockedByDoc[slug] = make(map[string]bool)
							}
							lockedByDoc[slug][sn.SlotID] = true
						}
					}
					for _, ev := range prevEvs {
						var anchors []models.EvidenceAnchor
						_ = json.Unmarshal(ev.EvidencesJSON, &anchors)
						plan.priorEvAnchors[ev.SlotID] = anchors
					}
				}
			}
		}

		plans = append(plans, plan)
		assetsForBudget = append(assetsForBudget, asset)
	}

	// 4. 加载 LLM 分层端点配置与预算上限
	endpoints, maxTokensLimit, maxFilesLimit, cfgErr := o.loadLLMEndpointsAndLimits(dbCtx, assetsForBudget)
	if cfgErr != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "LLM_CONFIG_INVALID", cfgErr.Error(), nil, 0)
		return
	}

	// 5. 生成前确定性预算熔断（SC-09：保证熔断时 0 次 LLM HTTP 请求发出！）
	budgetRes := llm.EstimateAndCheckBudget(llm.BudgetCheckInput{
		IR:               snapReady.IR,
		DocTypes:         assetsForBudget,
		GlossaryCached:   o.snapSvc.HasCachedGlossary(dbCtx, snapReady.Loc),
		MaxTokensPerTask: maxTokensLimit,
		MaxFilesPerTask:  maxFilesLimit,
		LockedSlotsByDoc: lockedByDoc,
	})
	if budgetRes.Exceeded {
		adviceModel := toModelBudgetAdvice(budgetRes.Advice)
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, budgetRes.ErrorCode, budgetRes.ErrorMessage, &adviceModel, budgetRes.Advice.EstimatedTokens)
		return
	}

	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusExtracting)
		return
	}

	// 6. extracting -> generating
	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusExtracting, models.TaskStatusGenerating, map[string]any{
		"snapshot_id":      snapID,
		"estimated_tokens": budgetRes.Advice.EstimatedTokens,
		"progress_percent": 25,
	}); err != nil {
		return
	}

	// 7. 生成或复用快照级共享术语表 glossary.json.gz（SC-02）
	fastCfg := endpoints[models.ModelTierFast]
	glossary, err := o.snapSvc.EnsureGlossary(ctx, snapReady.Snapshot, snapReady.Loc, snapReady.IR, o.llmClient, fastCfg, func(u *llm.UsageMetrics, st, errCode string) {
		o.recordUsage(dbCtx, &taskID, nil, repo.ID, "", nil, "", "glossary", models.ModelTierFast, u, st, errCode, operatorID)
	})
	if err != nil {
		if ctx.Err() != nil {
			o.markTaskCanceled(dbCtx, taskID, models.TaskStatusGenerating)
			return
		}
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "GLOSSARY_FAILED", err.Error(), nil, budgetRes.Advice.EstimatedTokens)
		return
	}

	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusGenerating)
		return
	}

	// 8. 创建槽位子任务记录（archdoc_gen_slot_tasks）
	var slotTasks []models.GenSlotTask
	for _, p := range plans {
		for _, sl := range p.asset.Meta.Slots {
			status := models.SlotTaskPending
			outMD := ""
			if lockedByDoc[p.slug] != nil && lockedByDoc[p.slug][sl.SlotID] {
				status = models.SlotTaskSkippedLocked
				outMD = p.priorSections[sl.SlotID].Markdown
			}
			slotTasks = append(slotTasks, models.GenSlotTask{
				TaskID:            taskID,
				DocTypeSlug:       p.slug,
				DocTypeRevisionID: p.revID,
				SlotID:            sl.SlotID,
				Status:            status,
				ModelTier:         p.asset.Meta.ModelTier,
				OutputMarkdown:    outMD,
			})
		}
	}
	if err := o.taskRepo.CreateSlotTasks(dbCtx, slotTasks); err != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "CREATE_SLOTS_FAILED", err.Error(), nil, budgetRes.Advice.EstimatedTokens)
		return
	}

	// 按 (slug, slot_id) 索引已创建的子任务（携带 DB ID）
	slotTaskMap := make(map[string]*models.GenSlotTask, len(slotTasks))
	for i := range slotTasks {
		key := slotTasks[i].DocTypeSlug + "::" + slotTasks[i].SlotID
		slotTaskMap[key] = &slotTasks[i]
	}

	// 9. 受全局信号量约束执行槽位生成（按 DocType 共享稳定随机 Nonce 以最大化 KV Cache 前缀命中）
	activeSlots := 0
	for i := range slotTasks {
		if slotTasks[i].Status != models.SlotTaskSkippedLocked {
			activeSlots++
		}
	}

	var (
		slotWG         sync.WaitGroup
		mu             sync.Mutex
		completedSlots int
		failedSlots    int
	)

	for _, p := range plans {
		batchNonce := llm.GenerateNonce()
		epCfg := endpoints[p.asset.Meta.ModelTier]
		for _, sl := range p.asset.Meta.Slots {
			st := slotTaskMap[p.slug+"::"+sl.SlotID]
			if st.Status == models.SlotTaskSkippedLocked {
				continue
			}

			// 按全局信号量槽位受控并发（若上下文已取消则不再发出任何新的 LLM 请求）
			releaseLLMSlot, semErr := o.acquireLLMSlot(ctx)
			if semErr != nil {
				cancelTime := time.Now()
				st.Status = models.SlotTaskCanceled
				st.FinishedAt = &cancelTime
				_ = o.taskRepo.UpdateSlotTask(dbCtx, st)
				continue
			}

			planCopy := p
			slotSpec := sl
			slotRow := st
			slotWG.Add(1)
			go func() {
				defer slotWG.Done()
				defer releaseLLMSlot()

				if ctx.Err() != nil {
					tNow := time.Now()
					slotRow.Status = models.SlotTaskCanceled
					slotRow.FinishedAt = &tNow
					_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)
					return
				}

				startAt := time.Now()
				slotRow.Status = models.SlotTaskRunning
				slotRow.AttemptCount++
				slotRow.StartedAt = &startAt
				_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)

				priorHybrid := ""
				if slotSpec.Ownership == models.OwnershipHybrid {
					if prev, ok := planCopy.priorSections[slotSpec.SlotID]; ok && prev.ManuallyEdited {
						priorHybrid = prev.Markdown
					}
				}

				maxOut := 0
				if planCopy.asset.Meta.MaxOutputTokens > 0 {
					perSlot := planCopy.asset.Meta.MaxOutputTokens / maxIntVal(len(planCopy.asset.Meta.Slots), 1)
					if perSlot >= 300 {
						maxOut = perSlot
					}
				}

				resp, callErr := o.executeSlotLLM(ctx, slotExecInput{
					RepoID:        repo.ID,
					Subdirectory:  task.Subdirectory,
					DocType:       planCopy.asset,
					Slot:          slotSpec,
					Glossary:      glossary,
					IR:            snapReady.IR,
					PriorHybridMD: priorHybrid,
					Nonce:         batchNonce,
					Endpoint:      epCfg,
					MaxTokens:     maxOut,
				})
				finAt := time.Now()
				slotRow.FinishedAt = &finAt

				if callErr != nil {
					if ctx.Err() != nil {
						slotRow.Status = models.SlotTaskCanceled
						slotRow.ErrorMessage = "任务已取消"
						o.recordUsage(dbCtx, &taskID, nil, repo.ID, planCopy.slug, &planCopy.revID, slotSpec.SlotID, "slot_gen", planCopy.asset.Meta.ModelTier, &llm.UsageMetrics{ModelName: epCfg.ModelName}, "canceled", "TASK_CANCELED", operatorID)
					} else {
						slotRow.Status = models.SlotTaskFailed
						slotRow.ErrorMessage = callErr.Error()
						mu.Lock()
						failedSlots++
						mu.Unlock()
						o.recordUsage(dbCtx, &taskID, nil, repo.ID, planCopy.slug, &planCopy.revID, slotSpec.SlotID, "slot_gen", planCopy.asset.Meta.ModelTier, &llm.UsageMetrics{ModelName: epCfg.ModelName}, "failed", "LLM_CALL_FAILED", operatorID)
					}
					_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)
					return
				}

				md, anchors := llm.ParseSlotLLMOutput(resp.Content)
				anchorsJSON, _ := json.Marshal(anchors)
				slotRow.Status = models.SlotTaskCompleted
				slotRow.OutputMarkdown = md
				slotRow.CitedAnchorsJSON = datatypes.JSON(anchorsJSON)
				slotRow.ErrorMessage = ""
				_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)

				o.recordUsage(dbCtx, &taskID, nil, repo.ID, planCopy.slug, &planCopy.revID, slotSpec.SlotID, "slot_gen", planCopy.asset.Meta.ModelTier, &resp.Usage, "success", "", operatorID)

				mu.Lock()
				completedSlots++
				done := completedSlots + failedSlots
				pct := 25
				if activeSlots > 0 {
					pct = 25 + int(float64(done)/float64(activeSlots)*55)
				}
				mu.Unlock()
				_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{"progress_percent": pct})
			}()
		}
	}
	slotWG.Wait()

	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusGenerating)
		return
	}

	if activeSlots > 0 && completedSlots == 0 && failedSlots == activeSlots {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "ALL_SLOTS_FAILED", "全部活跃槽位生成失败", nil, budgetRes.Advice.EstimatedTokens)
		return
	}

	// 10. generating -> verifying
	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusGenerating, models.TaskStatusVerifying, map[string]any{
		"progress_percent": 85,
	}); err != nil {
		return
	}

	// 11. 为每个 DocType 组装草稿章节、运行 Verifier 四重门禁并落库不可变 Revision
	for _, p := range plans {
		doc, docErr := o.persistGeneratedDocument(
			dbCtx, repo, task, snapReady, p.asset, p.revID, slotTaskMap, p.priorOrdered, p.priorEvAnchors, p.priorCommitSHA, operatorID,
		)
		if docErr != nil {
			o.markTaskFailed(dbCtx, taskID, models.TaskStatusVerifying, "VERIFY_PERSIST_FAILED", docErr.Error(), nil, budgetRes.Advice.EstimatedTokens)
			return
		}
		for _, sl := range p.asset.Meta.Slots {
			if st := slotTaskMap[p.slug+"::"+sl.SlotID]; st != nil {
				docID := doc.ID
				st.DocumentID = &docID
				_ = o.taskRepo.UpdateSlotTask(dbCtx, st)
			}
		}
	}

	// 12. verifying -> ready
	finNow := time.Now()
	_ = o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusVerifying, models.TaskStatusReady, map[string]any{
		"progress_percent": 100,
		"finished_at":      finNow,
	})
}

// executeIncrementalRefreshTask 执行单篇文档章节级增量刷新：
// 仅对 targetSlots \ lockedSlots 发起 L2 LLM 调用；未受影响章节与 locked 章节 content_sha256 严格字节级不变；
// 受漂移影响的 locked 章节强制保留原内容并标记 needs_drift_review=true（SC-11）。
func (o *GenerationOrchestrator) executeIncrementalRefreshTask(
	ctx context.Context,
	taskID uint,
	repo *commonModels.Repository,
	doc *models.Document,
	targetSlots []string,
	driftedLockedSlots map[string]bool,
	needSyncCode bool,
	operatorID uint,
) {
	dbCtx := context.Background()
	now := time.Now()

	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusPending, models.TaskStatusExtracting, map[string]any{
		"started_at":       now,
		"progress_percent": 10,
	}); err != nil {
		return
	}
	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusExtracting)
		return
	}

	releaseRepoLease := o.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	// 加载当前最新文档修订及章节元数据（叠加 SectionEvidence 实时 ownership）
	latestDoc, err := o.docRepo.GetByID(dbCtx, doc.ID)
	if err != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_NOT_FOUND", err.Error(), nil, 0)
		return
	}
	if latestDoc.CurrentRevisionNo <= 0 {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_NO_REVISION", "目标文档尚无基础修订", nil, 0)
		return
	}
	prevRev, prevEvs, err := o.docRepo.GetRevisionWithSections(dbCtx, latestDoc.ID, latestDoc.CurrentRevisionNo)
	if err != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "REV_NOT_FOUND", err.Error(), nil, 0)
		return
	}

	priorOrdered := overlaySectionMetadata(unmarshalSectionSnapshots(prevRev.SectionsJSON), prevEvs)
	priorBySlot := make(map[string]models.DocumentSectionSnapshot, len(priorOrdered))
	for _, sn := range priorOrdered {
		priorBySlot[sn.SlotID] = sn
	}
	priorEvBySlot := make(map[string][]models.EvidenceAnchor, len(prevEvs))
	for _, ev := range prevEvs {
		var anchors []models.EvidenceAnchor
		_ = json.Unmarshal(ev.EvidencesJSON, &anchors)
		priorEvBySlot[ev.SlotID] = anchors
	}

	// 准备 Code IR 快照：存在 Code Drift 或旧快照不可用时同步最新快照，否则复用旧快照
	var snapReady *SnapshotReady
	if !needSyncCode && prevRev.SnapshotID > 0 {
		if snap, loc, irBundle, loadErr := o.snapSvc.LoadIRBundleBySnapshotID(dbCtx, prevRev.SnapshotID); loadErr == nil && snap != nil && irBundle != nil {
			snapReady = &SnapshotReady{Snapshot: snap, Loc: loc, IR: irBundle, Reused: true}
		}
	}
	if snapReady == nil {
		sr, syncErr := o.snapSvc.EnsureSnapshot(ctx, repo, latestDoc.Subdirectory, latestDoc.RefName)
		if syncErr != nil {
			if ctx.Err() != nil {
				o.markTaskCanceled(dbCtx, taskID, models.TaskStatusExtracting)
				return
			}
			o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "EXTRACT_FAILED", syncErr.Error(), nil, 0)
			return
		}
		snapReady = sr
	}

	snapID := snapReady.Snapshot.ID
	releaseSnapLease := o.leaseMgr.AcquireSnapshot(snapID)
	defer releaseSnapLease()

	_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{
		"snapshot_id":      snapID,
		"progress_percent": 20,
	})

	dtModel, dtRev, dtErr := o.docTypeRepo.GetBySlug(dbCtx, latestDoc.DocTypeSlug)
	if dtErr != nil || dtModel == nil || dtRev == nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_TYPE_NOT_FOUND", "加载文档类型修订失败", nil, 0)
		return
	}
	asset, loadErr := o.loadDocTypeAssetFromRevision(dtRev, latestDoc.DocTypeSlug)
	if loadErr != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "DOC_TYPE_INVALID", loadErr.Error(), nil, 0)
		return
	}

	// 防御性再次剔除 locked 槽位，确保 locked 章节绝对零 LLM 调用且字节级不变
	targetSet := make(map[string]bool, len(targetSlots))
	for _, sid := range targetSlots {
		if prev, ok := priorBySlot[sid]; ok && prev.Ownership == models.OwnershipLocked {
			driftedLockedSlots[sid] = true
			continue
		}
		targetSet[sid] = true
	}

	endpoints, _, _, cfgErr := o.loadLLMEndpointsAndLimits(dbCtx, []*doctype.AssetBundle{asset})
	if cfgErr != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusExtracting, "LLM_CONFIG_INVALID", cfgErr.Error(), nil, 0)
		return
	}

	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusExtracting, models.TaskStatusGenerating, map[string]any{
		"snapshot_id":      snapID,
		"progress_percent": 30,
	}); err != nil {
		return
	}

	// 增量刷新优先复用已有术语表缓存（不额外发起 Glossary LLM 调用，精确收敛单槽位调用次数）
	var glossary *codeir.GlossaryCache
	if cachedGlossary, gErr := o.snapSvc.facetStore.ReadGlossary(dbCtx, snapReady.Loc); gErr == nil && cachedGlossary != nil {
		glossary = cachedGlossary
	}

	// 仅对目标非锁定槽位创建活跃子任务并调用 LLM
	var slotTasks []models.GenSlotTask
	docIDVal := latestDoc.ID
	for _, sl := range asset.Meta.Slots {
		if !targetSet[sl.SlotID] {
			continue
		}
		slotTasks = append(slotTasks, models.GenSlotTask{
			TaskID:            taskID,
			DocTypeSlug:       asset.Slug,
			DocTypeRevisionID: dtRev.ID,
			SlotID:            sl.SlotID,
			DocumentID:        &docIDVal,
			Status:            models.SlotTaskPending,
			ModelTier:         asset.Meta.ModelTier,
		})
	}
	if len(slotTasks) == 0 {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "NO_REFRESHABLE_SLOTS", "未命中任何可刷新的非锁定槽位", nil, 0)
		return
	}
	if err := o.taskRepo.CreateSlotTasks(dbCtx, slotTasks); err != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "CREATE_SLOTS_FAILED", err.Error(), nil, 0)
		return
	}

	slotTaskByID := make(map[string]*models.GenSlotTask, len(slotTasks))
	for i := range slotTasks {
		slotTaskByID[slotTasks[i].SlotID] = &slotTasks[i]
	}

	epCfg := endpoints[asset.Meta.ModelTier]
	batchNonce := llm.GenerateNonce()

	var (
		slotWG         sync.WaitGroup
		mu             sync.Mutex
		completedSlots int
		failedSlots    int
	)
	activeSlots := len(slotTasks)

	for _, sl := range asset.Meta.Slots {
		st, needRun := slotTaskByID[sl.SlotID]
		if !needRun {
			continue
		}

		releaseSlot, semErr := o.acquireLLMSlot(ctx)
		if semErr != nil {
			cancelTime := time.Now()
			st.Status = models.SlotTaskCanceled
			st.FinishedAt = &cancelTime
			_ = o.taskRepo.UpdateSlotTask(dbCtx, st)
			continue
		}

		slotSpec := sl
		slotRow := st
		slotWG.Add(1)
		go func() {
			defer slotWG.Done()
			defer releaseSlot()

			if ctx.Err() != nil {
				tNow := time.Now()
				slotRow.Status = models.SlotTaskCanceled
				slotRow.FinishedAt = &tNow
				_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)
				return
			}

			startAt := time.Now()
			slotRow.Status = models.SlotTaskRunning
			slotRow.AttemptCount++
			slotRow.StartedAt = &startAt
			_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)

			priorHybrid := ""
			if prev, ok := priorBySlot[slotSpec.SlotID]; ok && (prev.Ownership == models.OwnershipHybrid || prev.ManuallyEdited) {
				priorHybrid = prev.Markdown
			}

			resp, callErr := o.executeSlotLLM(ctx, slotExecInput{
				RepoID:        repo.ID,
				Subdirectory:  snapReady.Loc.Subdir,
				DocType:       asset,
				Slot:          slotSpec,
				Glossary:      glossary,
				IR:            snapReady.IR,
				PriorHybridMD: priorHybrid,
				Nonce:         batchNonce,
				Endpoint:      epCfg,
			})
			finAt := time.Now()
			slotRow.FinishedAt = &finAt
			if callErr != nil {
				if ctx.Err() != nil {
					slotRow.Status = models.SlotTaskCanceled
					slotRow.ErrorMessage = "任务已取消"
					o.recordUsage(dbCtx, &taskID, &docIDVal, repo.ID, asset.Slug, &dtRev.ID, slotSpec.SlotID, "slot_gen", asset.Meta.ModelTier, &llm.UsageMetrics{ModelName: epCfg.ModelName}, "canceled", "TASK_CANCELED", operatorID)
				} else {
					slotRow.Status = models.SlotTaskFailed
					slotRow.ErrorMessage = callErr.Error()
					mu.Lock()
					failedSlots++
					mu.Unlock()
					o.recordUsage(dbCtx, &taskID, &docIDVal, repo.ID, asset.Slug, &dtRev.ID, slotSpec.SlotID, "slot_gen", asset.Meta.ModelTier, &llm.UsageMetrics{ModelName: epCfg.ModelName}, "failed", "LLM_CALL_FAILED", operatorID)
				}
				_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)
				return
			}

			md, anchors := llm.ParseSlotLLMOutput(resp.Content)
			anchorsJSON, _ := json.Marshal(anchors)
			slotRow.Status = models.SlotTaskCompleted
			slotRow.OutputMarkdown = md
			slotRow.CitedAnchorsJSON = datatypes.JSON(anchorsJSON)
			slotRow.ErrorMessage = ""
			_ = o.taskRepo.UpdateSlotTask(dbCtx, slotRow)

			o.recordUsage(dbCtx, &taskID, &docIDVal, repo.ID, asset.Slug, &dtRev.ID, slotSpec.SlotID, "slot_gen", asset.Meta.ModelTier, &resp.Usage, "success", "", operatorID)

			mu.Lock()
			completedSlots++
			done := completedSlots + failedSlots
			pct := 30 + int(float64(done)/float64(maxIntVal(activeSlots, 1))*50)
			mu.Unlock()
			_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{"progress_percent": pct})
		}()
	}
	slotWG.Wait()

	if ctx.Err() != nil {
		o.markTaskCanceled(dbCtx, taskID, models.TaskStatusGenerating)
		return
	}
	if completedSlots == 0 && failedSlots == activeSlots {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusGenerating, "ALL_SLOTS_FAILED", "全部待刷新槽位生成失败", nil, 0)
		return
	}

	if err := o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusGenerating, models.TaskStatusVerifying, map[string]any{
		"progress_percent": 85,
	}); err != nil {
		return
	}

	// 按新模板槽位顺序组装章节：
	// - 目标非锁定槽位且生成成功 -> 采用新生成 Markdown
	// - 未受影响槽位 & locked 槽位 -> 原样保留 Markdown 与 ContentSHA256（字节级不变）；受漂移影响的 locked 槽位标 needs_drift_review=true
	// - 被模板移除但旧版为 locked/manually_edited 的槽位 -> 转孤立章节保护挂载
	newSlotSet := make(map[string]bool, len(asset.Meta.Slots))
	mergedSections := make([]models.DocumentSectionSnapshot, 0, len(asset.Meta.Slots)+2)
	mergedInputs := make([]verifier.SectionInput, 0, len(asset.Meta.Slots)+2)
	orderIdx := 1

	for _, sl := range asset.Meta.Slots {
		newSlotSet[sl.SlotID] = true
		prev, existed := priorBySlot[sl.SlotID]

		if st, refreshed := slotTaskByID[sl.SlotID]; refreshed && st.Status == models.SlotTaskCompleted {
			var cited []verifier.CitedAnchorCandidate
			_ = json.Unmarshal(st.CitedAnchorsJSON, &cited)
			ownership := sl.Ownership
			if existed && prev.Ownership != "" {
				ownership = prev.Ownership
			}
			mergedSections = append(mergedSections, models.DocumentSectionSnapshot{
				SlotID:           sl.SlotID,
				SectionOrder:     orderIdx,
				Title:            sl.Title,
				Ownership:        ownership,
				IsOrphan:         false,
				ManuallyEdited:   false,
				NeedsDriftReview: false,
				Markdown:         st.OutputMarkdown,
				ContentSHA256:    sha256Hex(st.OutputMarkdown),
			})
			mergedInputs = append(mergedInputs, verifier.SectionInput{
				SlotID:       sl.SlotID,
				Title:        sl.Title,
				Markdown:     st.OutputMarkdown,
				CitedAnchors: cited,
			})
			orderIdx++
			continue
		}

		// 未受影响槽位或 locked 槽位：严格字节级保留原 Markdown 与 ContentSHA256
		if existed {
			needsReview := prev.NeedsDriftReview
			if driftedLockedSlots[sl.SlotID] {
				needsReview = true
			}
			sha := prev.ContentSHA256
			if sha == "" {
				sha = sha256Hex(prev.Markdown)
			}
			mergedSections = append(mergedSections, models.DocumentSectionSnapshot{
				SlotID:           prev.SlotID,
				SectionOrder:     orderIdx,
				Title:            prev.Title,
				Ownership:        prev.Ownership,
				IsOrphan:         prev.IsOrphan,
				ManuallyEdited:   prev.ManuallyEdited,
				NeedsDriftReview: needsReview,
				Markdown:         prev.Markdown,
				ContentSHA256:    sha,
			})
			var priorCited []verifier.CitedAnchorCandidate
			for _, a := range priorEvBySlot[prev.SlotID] {
				priorCited = append(priorCited, verifier.CitedAnchorCandidate{
					FilePath:   a.FilePath,
					SymbolKind: a.SymbolKind,
					FQSymbol:   a.FQSymbol,
				})
			}
			mergedInputs = append(mergedInputs, verifier.SectionInput{
				SlotID:       prev.SlotID,
				Title:        prev.Title,
				Markdown:     prev.Markdown,
				CitedAnchors: priorCited,
			})
			orderIdx++
		}
	}

	// 孤立章节保护：原文档中不在新模板且为 locked / manually_edited / 已是 orphan 的章节挂载文末
	for _, prev := range priorOrdered {
		if newSlotSet[prev.SlotID] {
			continue
		}
		if prev.Ownership != models.OwnershipLocked && !prev.ManuallyEdited && !prev.IsOrphan {
			continue
		}
		sha := prev.ContentSHA256
		if sha == "" {
			sha = sha256Hex(prev.Markdown)
		}
		mergedSections = append(mergedSections, models.DocumentSectionSnapshot{
			SlotID:           prev.SlotID,
			SectionOrder:     orderIdx,
			Title:            prev.Title,
			Ownership:        prev.Ownership,
			IsOrphan:         true,
			ManuallyEdited:   prev.ManuallyEdited,
			NeedsDriftReview: true,
			Markdown:         prev.Markdown,
			ContentSHA256:    sha,
		})
		mergedInputs = append(mergedInputs, verifier.SectionInput{
			SlotID:   prev.SlotID,
			Title:    prev.Title,
			Markdown: prev.Markdown,
		})
		orderIdx++
	}

	summary := fmt.Sprintf("章节级增量刷新（更新 %d 个受影响槽位）", completedSlots)
	if _, err := o.verifyAndAppendRevision(
		dbCtx, latestDoc, snapReady.Snapshot.ID, snapReady.Snapshot.CommitSHA, dtRev.ID,
		asset, snapReady.IR, mergedSections, mergedInputs,
		models.RevSourceIncrementalRefresh, &taskID, summary, operatorID,
	); err != nil {
		o.markTaskFailed(dbCtx, taskID, models.TaskStatusVerifying, "VERIFY_PERSIST_FAILED", err.Error(), nil, 0)
		return
	}

	finNow := time.Now()
	_ = o.taskRepo.TransitionTaskStatus(dbCtx, taskID, models.TaskStatusVerifying, models.TaskStatusReady, map[string]any{
		"progress_percent": 100,
		"finished_at":      finNow,
	})
}

func (o *GenerationOrchestrator) persistGeneratedDocument(
	ctx context.Context,
	repo *commonModels.Repository,
	task *models.GenTask,
	snapReady *SnapshotReady,
	asset *doctype.AssetBundle,
	docTypeRevID uint,
	slotTaskMap map[string]*models.GenSlotTask,
	priorOrdered []models.DocumentSectionSnapshot,
	priorEvAnchors map[string][]models.EvidenceAnchor,
	priorCommitSHA string,
	operatorID uint,
) (*models.Document, error) {
	title := buildDocumentTitle(repo.Name, task.Subdirectory, asset.Meta.DisplayName)
	doc, err := o.docRepo.GetOrCreateDocument(ctx, &models.Document{
		RepoID:            repo.ID,
		Subdirectory:      task.Subdirectory,
		RefName:           snapReady.Snapshot.RefName,
		DocTypeSlug:       asset.Slug,
		DocTypeRevisionID: docTypeRevID,
		Title:             title,
		Status:            models.DocStatusDraft,
	})
	if err != nil {
		return nil, err
	}

	generated := make(map[string]GeneratedSlotPayload, len(asset.Meta.Slots))
	for _, sl := range asset.Meta.Slots {
		if st := slotTaskMap[asset.Slug+"::"+sl.SlotID]; st != nil {
			var cited []verifier.CitedAnchorCandidate
			_ = json.Unmarshal(st.CitedAnchorsJSON, &cited)
			generated[sl.SlotID] = GeneratedSlotPayload{
				Markdown:     st.OutputMarkdown,
				CitedAnchors: cited,
			}
		}
	}

	merged := MergeGeneratedWithLockedAndOrphans(MergeGeneratedInput{
		NewSlots:       asset.Meta.Slots,
		Generated:      generated,
		PriorSections:  priorOrdered,
		PriorEvidences: priorEvAnchors,
		PriorCommitSHA: priorCommitSHA,
		NewCommitSHA:   snapReady.Snapshot.CommitSHA,
		NewIR:          snapReady.IR,
	})

	return o.verifyAndAppendRevision(ctx, doc, snapReady.Snapshot.ID, snapReady.Snapshot.CommitSHA, docTypeRevID, asset, snapReady.IR, merged.Sections, merged.SectionInputs, models.RevSourceAIGenerate, &task.ID, "AI 反推生成初稿", operatorID)
}

func (o *GenerationOrchestrator) executeSingleSlotRetry(
	ctx context.Context,
	task *models.GenTask,
	slotTaskID uint,
	operatorID uint,
) {
	slotTask, err := o.taskRepo.GetSlotTask(ctx, task.ID, slotTaskID)
	if err != nil {
		return
	}

	startAt := time.Now()
	slotTask.Status = models.SlotTaskRunning
	slotTask.StartedAt = &startAt
	_ = o.taskRepo.UpdateSlotTask(ctx, slotTask)

	snap, loc, irBundle, err := o.snapSvc.LoadIRBundleBySnapshotID(ctx, *task.SnapshotID)
	if err != nil {
		o.failRetrySlot(ctx, slotTask, "加载快照 Code IR 失败: "+err.Error())
		return
	}
	dtRev, err := o.docTypeRepo.GetRevisionByID(ctx, slotTask.DocTypeRevisionID)
	if err != nil {
		o.failRetrySlot(ctx, slotTask, "加载 DocType 修订失败: "+err.Error())
		return
	}
	asset, err := o.loadDocTypeAssetFromRevision(dtRev, slotTask.DocTypeSlug)
	if err != nil {
		o.failRetrySlot(ctx, slotTask, err.Error())
		return
	}

	var targetSlot *doctype.SlotSpec
	for i := range asset.Meta.Slots {
		if asset.Meta.Slots[i].SlotID == slotTask.SlotID {
			targetSlot = &asset.Meta.Slots[i]
			break
		}
	}
	if targetSlot == nil {
		o.failRetrySlot(ctx, slotTask, "目标槽位不存在于当前 DocType 修订")
		return
	}

	endpoints, _, _, err := o.loadLLMEndpointsAndLimits(ctx, []*doctype.AssetBundle{asset})
	if err != nil {
		o.failRetrySlot(ctx, slotTask, err.Error())
		return
	}
	epCfg := endpoints[asset.Meta.ModelTier]

	glossary, _ := o.snapSvc.facetStore.ReadGlossary(ctx, loc)

	o.llmSem <- struct{}{}
	resp, callErr := o.executeSlotLLM(ctx, slotExecInput{
		RepoID:       task.RepoID,
		Subdirectory: task.Subdirectory,
		DocType:      asset,
		Slot:         *targetSlot,
		Glossary:     glossary,
		IR:           irBundle,
		Endpoint:     epCfg,
	})
	<-o.llmSem

	finAt := time.Now()
	slotTask.FinishedAt = &finAt
	if callErr != nil {
		o.recordUsage(ctx, &task.ID, slotTask.DocumentID, task.RepoID, asset.Slug, &dtRev.ID, targetSlot.SlotID, "slot_retry", asset.Meta.ModelTier, &llm.UsageMetrics{ModelName: epCfg.ModelName}, "failed", "LLM_RETRY_FAILED", operatorID)
		o.failRetrySlot(ctx, slotTask, callErr.Error())
		return
	}

	md, cited := llm.ParseSlotLLMOutput(resp.Content)
	citedJSON, _ := json.Marshal(cited)
	slotTask.Status = models.SlotTaskCompleted
	slotTask.OutputMarkdown = md
	slotTask.CitedAnchorsJSON = datatypes.JSON(citedJSON)
	slotTask.ErrorMessage = ""
	_ = o.taskRepo.UpdateSlotTask(ctx, slotTask)

	o.recordUsage(ctx, &task.ID, slotTask.DocumentID, task.RepoID, asset.Slug, &dtRev.ID, targetSlot.SlotID, "slot_retry", asset.Meta.ModelTier, &resp.Usage, "success", "", operatorID)

	// 加载目标文档最新修订，仅替换目标槽位 Markdown，其余槽位内容与 content_sha256 严格字节级不变！
	var doc *models.Document
	if slotTask.DocumentID != nil {
		doc, err = o.docRepo.GetByID(ctx, *slotTask.DocumentID)
	} else {
		doc, err = o.docRepo.GetByScope(ctx, task.RepoID, task.Subdirectory, snap.RefName, asset.Slug)
	}
	if err != nil || doc == nil {
		log.Printf("[%s] 单槽位重试后未找到目标文档 (task=%d slot=%s): %v", config.ServiceName, task.ID, slotTask.SlotID, err)
		return
	}

	prevRev, _, err := o.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
	if err != nil {
		log.Printf("[%s] 单槽位重试加载上一修订失败: %v", config.ServiceName, err)
		return
	}
	var sections []models.DocumentSectionSnapshot
	_ = json.Unmarshal(prevRev.SectionsJSON, &sections)

	sectionInputs := make([]verifier.SectionInput, 0, len(sections))
	replaced := false
	for i := range sections {
		if sections[i].SlotID == slotTask.SlotID {
			sections[i].Markdown = md
			sections[i].ContentSHA256 = sha256Hex(md)
			replaced = true
			sectionInputs = append(sectionInputs, verifier.SectionInput{
				SlotID:       sections[i].SlotID,
				Title:        sections[i].Title,
				Markdown:     md,
				CitedAnchors: cited,
			})
		} else {
			// 非目标槽位：原样保留 Markdown 与 ContentSHA256，字节级零变动
			sectionInputs = append(sectionInputs, verifier.SectionInput{
				SlotID:   sections[i].SlotID,
				Title:    sections[i].Title,
				Markdown: sections[i].Markdown,
			})
		}
	}
	if !replaced {
		sections = append(sections, models.DocumentSectionSnapshot{
			SlotID:        targetSlot.SlotID,
			SectionOrder:  len(sections) + 1,
			Title:         targetSlot.Title,
			Ownership:     targetSlot.Ownership,
			Markdown:      md,
			ContentSHA256: sha256Hex(md),
		})
		sectionInputs = append(sectionInputs, verifier.SectionInput{
			SlotID:       targetSlot.SlotID,
			Title:        targetSlot.Title,
			Markdown:     md,
			CitedAnchors: cited,
		})
	}

	if _, err := o.verifyAndAppendRevision(ctx, doc, snap.ID, snap.CommitSHA, dtRev.ID, asset, irBundle, sections, sectionInputs, models.RevSourceAIGenerate, &task.ID, fmt.Sprintf("单槽位独立重试补齐: %s", targetSlot.SlotID), operatorID); err != nil {
		log.Printf("[%s] 单槽位重试落库新修订失败: %v", config.ServiceName, err)
	}
}

func (o *GenerationOrchestrator) verifyAndAppendRevision(
	ctx context.Context,
	doc *models.Document,
	snapshotID uint,
	commitSHA string,
	docTypeRevID uint,
	asset *doctype.AssetBundle,
	irBundle *codeir.CodeIRBundle,
	sectionSnapshots []models.DocumentSectionSnapshot,
	sectionInputs []verifier.SectionInput,
	source string,
	sourceRefID *uint,
	changeSummary string,
	operatorID uint,
) (*models.Document, error) {
	vRes := o.verifier.Verify(verifier.VerifyInput{
		CommitSHA: commitSHA,
		IR:        irBundle,
		Slots:     asset.Meta.Slots,
		Policy:    asset.Meta.VerifyPolicy,
		Sections:  sectionInputs,
	})

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
		if evList == nil {
			evList = []verifier.EvidenceAnchor{}
		}
		evJSON, _ := json.Marshal(evList)
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
			EvidencesJSON:       datatypes.JSON(evJSON),
		})
	}

	if err := o.docRepo.AppendRevisionWithOptimisticLock(ctx, doc, doc.CurrentRevisionNo, rev, secEvidences); err != nil {
		return nil, err
	}

	_ = o.govRepo.RecordQualityMetric(ctx, &models.QualityMetric{
		DocumentID:        doc.ID,
		RepoID:            doc.RepoID,
		DocTypeSlug:       doc.DocTypeSlug,
		DocTypeRevisionID: docTypeRevID,
		MetricEvent:       models.MetricEventAIDraftVerified,
		SlotCoverageRate:  vRes.Report.SlotCoverageRate,
		VerifierBlocked:   !vRes.Report.CanApprove,
	})

	return doc, nil
}

func (o *GenerationOrchestrator) loadLLMEndpointsAndLimits(
	ctx context.Context,
	assets []*doctype.AssetBundle,
) (map[string]llm.EndpointConfig, int, int, error) {
	configs, err := o.govRepo.ListLLMConfigs(ctx)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("读取 LLM 配置失败: %w", err)
	}

	byTier := make(map[string]models.LLMConfig, len(configs))
	for _, c := range configs {
		byTier[c.Tier] = c
	}

	needTiers := map[string]bool{models.ModelTierFast: true}
	for _, a := range assets {
		if a != nil && a.Meta.ModelTier != "" {
			needTiers[a.Meta.ModelTier] = true
		}
	}

	endpoints := make(map[string]llm.EndpointConfig, 2)
	minTokens := 0
	minFiles := 0

	for tier := range needTiers {
		cfg, ok := byTier[tier]
		if !ok || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ModelName) == "" {
			// 若 flagship 未单独配置，则回退复用已配置的 fast（或反之），保障单模型内网环境可用
			fallbackTier := models.ModelTierFast
			if tier == models.ModelTierFast {
				fallbackTier = models.ModelTierFlagship
			}
			if fb, fbOK := byTier[fallbackTier]; fbOK && strings.TrimSpace(fb.BaseURL) != "" && strings.TrimSpace(fb.ModelName) != "" {
				cfg = fb
			} else {
				return nil, 0, 0, fmt.Errorf("未配置可用的 %s 档位 LLM 模型，请先在管理员配置中心完成配置", tier)
			}
		}

		apiKey := ""
		if cfg.APIKeyCipher != "" && o.keyCipher != nil {
			dec, decErr := o.keyCipher.Decrypt(cfg.APIKeyCipher)
			if decErr != nil {
				return nil, 0, 0, fmt.Errorf("解密 %s 档位 APIKey 失败: %w", tier, decErr)
			}
			apiKey = dec
		}

		endpoints[tier] = llm.EndpointConfig{
			BaseURL:        cfg.BaseURL,
			ModelName:      cfg.ModelName,
			APIKey:         apiKey,
			TimeoutSeconds: cfg.TimeoutSeconds,
			MaxRetries:     cfg.MaxRetries,
		}

		if cfg.MaxTokensPerTask > 0 && (minTokens == 0 || cfg.MaxTokensPerTask < minTokens) {
			minTokens = cfg.MaxTokensPerTask
		}
		if cfg.MaxFilesPerTask > 0 && (minFiles == 0 || cfg.MaxFilesPerTask < minFiles) {
			minFiles = cfg.MaxFilesPerTask
		}
	}

	return endpoints, minTokens, minFiles, nil
}

func (o *GenerationOrchestrator) loadDocTypeAssetFromRevision(rev *models.DocTypeRevision, slug string) (*doctype.AssetBundle, error) {
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

func (o *GenerationOrchestrator) failRetrySlot(ctx context.Context, st *models.GenSlotTask, msg string) {
	now := time.Now()
	st.Status = models.SlotTaskFailed
	st.ErrorMessage = msg
	st.FinishedAt = &now
	_ = o.taskRepo.UpdateSlotTask(ctx, st)
}

func (o *GenerationOrchestrator) markTaskFailed(ctx context.Context, taskID uint, fromStatus, errCode, errMsg string, advice *models.BudgetAdvice, estTokens int) {
	now := time.Now()
	updates := map[string]any{
		"error_code":       errCode,
		"error_message":    errMsg,
		"estimated_tokens": estTokens,
		"finished_at":      now,
	}
	if advice != nil {
		b, _ := json.Marshal(advice)
		updates["budget_advice_json"] = datatypes.JSON(b)
	}
	_ = o.taskRepo.TransitionTaskStatus(ctx, taskID, fromStatus, models.TaskStatusFailed, updates)
}

func (o *GenerationOrchestrator) markTaskCanceled(ctx context.Context, taskID uint, fromStatus string) {
	now := time.Now()
	_ = o.taskRepo.TransitionTaskStatus(ctx, taskID, fromStatus, models.TaskStatusCanceled, map[string]any{
		"error_code":    "TASK_CANCELED",
		"error_message": "任务已被取消",
		"finished_at":   now,
	})
}

func (o *GenerationOrchestrator) recordUsage(
	ctx context.Context,
	taskID *uint,
	docID *uint,
	repoID uint,
	docTypeSlug string,
	docTypeRevID *uint,
	slotID string,
	stage string,
	modelTier string,
	usage *llm.UsageMetrics,
	status string,
	errCode string,
	operatorID uint,
) {
	if usage == nil {
		usage = &llm.UsageMetrics{}
	}
	_ = o.govRepo.RecordLLMUsage(ctx, &models.LLMUsageLog{
		TaskID:             taskID,
		DocumentID:         docID,
		RepoID:             repoID,
		DocTypeSlug:        docTypeSlug,
		DocTypeRevisionID:  docTypeRevID,
		SlotID:             slotID,
		Stage:              stage,
		ModelTier:          modelTier,
		ModelName:          usage.ModelName,
		PromptTokens:       usage.PromptTokens,
		CachedPromptTokens: usage.CachedPromptTokens,
		CompletionTokens:   usage.CompletionTokens,
		LatencyMs:          usage.LatencyMs,
		Status:             status,
		ErrorCode:          errCode,
		OperatorID:         operatorID,
	})
}

func (o *GenerationOrchestrator) registerCancel(taskID uint, cancel context.CancelFunc) {
	o.cancelMu.Lock()
	defer o.cancelMu.Unlock()
	o.cancelFuncs[taskID] = cancel
}

func (o *GenerationOrchestrator) triggerCancel(taskID uint) {
	o.cancelMu.Lock()
	cancel, ok := o.cancelFuncs[taskID]
	o.cancelMu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (o *GenerationOrchestrator) unregisterCancel(taskID uint) {
	o.cancelMu.Lock()
	defer o.cancelMu.Unlock()
	delete(o.cancelFuncs, taskID)
}

func assembleFullMarkdown(templateMD string, sections []models.DocumentSectionSnapshot) string {
	out := templateMD
	var orphans []models.DocumentSectionSnapshot
	for _, s := range sections {
		if s.IsOrphan {
			orphans = append(orphans, s)
			continue
		}
		marker := fmt.Sprintf("<!-- slot:%s -->", s.SlotID)
		replacement := marker + "\n" + strings.TrimSpace(s.Markdown) + "\n"
		if strings.Contains(out, marker) {
			out = strings.Replace(out, marker, replacement, 1)
		} else {
			out += fmt.Sprintf("\n## %s\n%s\n%s\n", s.Title, marker, strings.TrimSpace(s.Markdown))
		}
	}
	if len(orphans) > 0 {
		out += "\n## [孤立章节 - 待人工处置]\n"
		for _, oSec := range orphans {
			out += fmt.Sprintf("\n### %s (`%s`)\n%s\n", oSec.Title, oSec.SlotID, strings.TrimSpace(oSec.Markdown))
		}
	}
	return out
}

func buildDocumentTitle(repoName, subdir, displayName string) string {
	if subdir != "" {
		return fmt.Sprintf("%s (%s) %s", repoName, subdir, displayName)
	}
	return fmt.Sprintf("%s %s", repoName, displayName)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func toModelBudgetAdvice(a llm.BudgetAdvice) models.BudgetAdvice {
	dirs := make([]models.BudgetDirectoryItem, 0, len(a.TopDirectories))
	for _, d := range a.TopDirectories {
		dirs = append(dirs, models.BudgetDirectoryItem{
			Path:            d.Path,
			EstimatedTokens: d.EstimatedTokens,
			FileCount:       d.FileCount,
		})
	}
	dts := make([]models.BudgetDocTypeItem, 0, len(a.TopDocTypes))
	for _, dt := range a.TopDocTypes {
		dts = append(dts, models.BudgetDocTypeItem{
			Slug:            dt.Slug,
			EstimatedTokens: dt.EstimatedTokens,
		})
	}
	return models.BudgetAdvice{
		EstimatedTokens:  a.EstimatedTokens,
		MaxTokensPerTask: a.MaxTokensPerTask,
		EffectiveFiles:   a.EffectiveFiles,
		MaxFilesPerTask:  a.MaxFilesPerTask,
		TopDirectories:   dirs,
		TopDocTypes:      dts,
		Suggestions:      a.Suggestions,
	}
}

func toGenTaskDetailVO(t *models.GenTask, repoName, commitSHA string, slots []models.GenSlotTask) GenTaskDetailVO {
	var docTypes []string
	_ = json.Unmarshal(t.DocTypesJSON, &docTypes)
	if docTypes == nil {
		docTypes = []string{}
	}

	var advicePtr *models.BudgetAdvice
	if len(t.BudgetAdviceJSON) > 2 {
		var adv models.BudgetAdvice
		if json.Unmarshal(t.BudgetAdviceJSON, &adv) == nil && (adv.EstimatedTokens > 0 || adv.EffectiveFiles > 0 || len(adv.Suggestions) > 0) {
			advicePtr = &adv
		}
	}

	var slotVOs []GenSlotTaskVO
	if len(slots) > 0 {
		slotVOs = make([]GenSlotTaskVO, 0, len(slots))
		for i := range slots {
			slotVOs = append(slotVOs, toGenSlotTaskVO(&slots[i]))
		}
	}

	var targetSlots []string
	if len(t.TargetSlotsJSON) > 2 {
		_ = json.Unmarshal(t.TargetSlotsJSON, &targetSlots)
	}

	return GenTaskDetailVO{
		ID:               t.ID,
		RepoID:           t.RepoID,
		RepoName:         repoName,
		Subdirectory:     t.Subdirectory,
		RefName:          t.RefName,
		SnapshotID:       t.SnapshotID,
		CommitSHA:        commitSHA,
		TaskKind:         t.TaskKind,
		Priority:         t.Priority,
		DocTypes:         docTypes,
		TargetDocumentID: t.TargetDocumentID,
		TargetSlots:      targetSlots,
		Status:           t.Status,
		ProgressPercent:  t.ProgressPercent,
		EstimatedTokens:  t.EstimatedTokens,
		ErrorCode:        t.ErrorCode,
		ErrorMessage:     t.ErrorMessage,
		BudgetAdvice:     advicePtr,
		SlotTasks:        slotVOs,
		TriggeredBy:      t.TriggeredBy,
		StartedAt:        t.StartedAt,
		FinishedAt:       t.FinishedAt,
		CreatedAt:        t.CreatedAt,
	}
}

func toGenSlotTaskVO(s *models.GenSlotTask) GenSlotTaskVO {
	return GenSlotTaskVO{
		ID:                s.ID,
		DocTypeSlug:       s.DocTypeSlug,
		DocTypeRevisionID: s.DocTypeRevisionID,
		SlotID:            s.SlotID,
		DocumentID:        s.DocumentID,
		Status:            s.Status,
		AttemptCount:      s.AttemptCount,
		ModelTier:         s.ModelTier,
		ErrorMessage:      s.ErrorMessage,
		StartedAt:         s.StartedAt,
		FinishedAt:        s.FinishedAt,
	}
}

type slotExecInput struct {
	RepoID        uint
	Subdirectory  string
	DocType       *doctype.AssetBundle
	Slot          doctype.SlotSpec
	Glossary      *codeir.GlossaryCache
	IR            *codeir.CodeIRBundle
	PriorHybridMD string
	Nonce         string
	Endpoint      llm.EndpointConfig
	MaxTokens     int
}

// executeSlotLLM 统一执行单槽位 L2 推理：
// - 普通清单/结构槽位：保持单轮确定性 L1 Code IR 直出（最大化 KV Cache 命中与低成本）；
// - 声明 enable_source_tools=true 的核心流程/并发/调用链槽位：开启受限只读沙箱 Tool-Calling，按需核实函数实现与跨文件调用链，并保留降级兜底与活跃租约保护。
func (o *GenerationOrchestrator) executeSlotLLM(ctx context.Context, in slotExecInput) (*llm.ChatResponse, error) {
	var toolkit *llm.SandboxSourceToolkit
	useTools := false

	if in.Slot.EnableSourceTools {
		var repoRoot, scopeRoot string
		if o.snapSvc != nil {
			repoRoot, scopeRoot, _ = o.snapSvc.ResolveSandboxScope(in.RepoID, in.Subdirectory)
		}
		tk := llm.NewSandboxSourceToolkit(llm.SandboxToolkitConfig{
			ScopeRoot: scopeRoot,
			RepoRoot:  repoRoot,
			IR:        in.IR,
		})
		if tk.HasTools() {
			toolkit = tk
			useTools = true
		}
	}

	msgs, nonce := llm.BuildSlotMessages(llm.SlotPromptInput{
		DocType:           in.DocType,
		Slot:              in.Slot,
		Glossary:          in.Glossary,
		IR:                in.IR,
		PriorHybridMD:     in.PriorHybridMD,
		Nonce:             in.Nonce,
		EnableSourceTools: useTools,
	})

	chatReq := llm.ChatRequest{
		Messages:  msgs,
		MaxTokens: in.MaxTokens,
	}

	if !useTools || toolkit == nil {
		return o.llmClient.ChatCompletion(ctx, in.Endpoint, chatReq)
	}

	releaseLease := o.leaseMgr.AcquireRepo(in.RepoID)
	defer releaseLease()

	maxRounds := in.Slot.MaxToolRounds
	if maxRounds <= 0 {
		maxRounds = 4
	}
	perSlotTokenCap := 0
	if in.Slot.TokenBudget > 0 {
		// 为多轮受限工具探索预留合理上限（槽位基础预算 * 4，最低 32K），超出即强制收敛输出
		perSlotTokenCap = maxIntVal(in.Slot.TokenBudget*4, 32000)
	}

	loopRes, err := llm.RunToolLoop(ctx, o.llmClient, in.Endpoint, chatReq, toolkit, llm.ToolLoopOptions{
		Nonce:          nonce,
		MaxRounds:      maxRounds,
		MaxTotalTokens: perSlotTokenCap,
	})
	if err != nil {
		return nil, err
	}
	if loopRes.ToolCallsCount > 0 || loopRes.FallbackNoTools {
		log.Printf("[%s] 槽位 %s/%s Tool-Calling 完成: rounds=%d tool_calls=%d fallback_no_tools=%v prompt_tokens=%d completion_tokens=%d",
			config.ServiceName, in.DocType.Slug, in.Slot.SlotID, loopRes.Rounds, loopRes.ToolCallsCount, loopRes.FallbackNoTools,
			loopRes.Response.Usage.PromptTokens, loopRes.Response.Usage.CompletionTokens)
	}
	return loopRes.Response, nil
}

func maxIntVal(a, b int) int {
	if a > b {
		return a
	}
	return b
}
