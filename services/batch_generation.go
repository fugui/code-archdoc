package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/sandbox"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

// SubmitBatchGenerateTask 校验批量请求与目标范围写权限，创建 batch 父任务并启动独立协调协程（不占 TaskScheduler 槽位，避免死锁；ADR-04 D-5）。
func (o *GenerationOrchestrator) SubmitBatchGenerateTask(
	ctx context.Context,
	req BatchGenerateRequest,
	operator *commonModels.User,
) (*GenTaskDetailVO, error) {
	if req.RepoID == 0 {
		return nil, fmt.Errorf("%w: repo_id 必填", ErrInvalidTaskParam)
	}
	if len(req.DocTypes) == 0 {
		return nil, fmt.Errorf("%w: doc_types 不能为空", ErrInvalidTaskParam)
	}

	childMode := strings.TrimSpace(req.Mode)
	if childMode == "" {
		childMode = models.TaskKindGenerate
	}
	if childMode != models.TaskKindGenerate && childMode != models.TaskKindRegenerate {
		return nil, fmt.Errorf("%w: mode 仅支持 generate 或 regenerate", ErrInvalidTaskParam)
	}

	engine, engErr := o.CheckEngineAvailable(ctx, req.Engine)
	if engErr != nil {
		return nil, engErr
	}

	repo, err := o.masterRepo.GetRepository(ctx, req.RepoID)
	if err != nil {
		return nil, err
	}

	// 去重并校验全部勾选 DocType 均存在且启用中
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

	// 1. 解析目标子目录列表
	var targetSubdirs []string
	if req.AllModules {
		if o.moduleRepo != nil {
			mods, mErr := o.moduleRepo.ListModulesByRepo(ctx, repo.ID)
			if mErr != nil {
				return nil, mErr
			}
			for _, m := range mods {
				if m.Status == models.ModuleStatusActive && m.Subdirectory != "" {
					targetSubdirs = append(targetSubdirs, m.Subdirectory)
				}
			}
		}
		if len(targetSubdirs) == 0 && o.moduleSvc != nil {
			refreshed, rErr := o.moduleSvc.RefreshModules(ctx, repo.ID)
			if rErr != nil {
				return nil, fmt.Errorf("自动刷新仓库模块列表失败: %w", rErr)
			}
			for _, m := range refreshed.Modules {
				if m.Status == models.ModuleStatusActive && m.Subdirectory != "" {
					targetSubdirs = append(targetSubdirs, m.Subdirectory)
				}
			}
		}
		// 若模块索引仍为空，回退读取 architecture_elements 中的子目录
		if len(targetSubdirs) == 0 {
			if elems, eErr := o.masterRepo.ListArchElementsByRepo(ctx, repo.ID); eErr == nil {
				seenEl := make(map[string]bool)
				for _, el := range elems {
					if ns, nErr := sandbox.NormalizeSubdir(el.Subdirectory); nErr == nil && ns != "" && !seenEl[ns] {
						seenEl[ns] = true
						targetSubdirs = append(targetSubdirs, ns)
					}
				}
			}
		}
	} else {
		seenSub := make(map[string]bool, len(req.Subdirectories))
		for _, rawSub := range req.Subdirectories {
			normSub, subErr := sandbox.NormalizeSubdir(rawSub)
			if subErr != nil {
				return nil, subErr
			}
			if normSub == "" || seenSub[normSub] {
				continue
			}
			seenSub[normSub] = true
			targetSubdirs = append(targetSubdirs, normSub)
		}
	}

	if len(targetSubdirs) == 0 && !req.IncludeRepoScope {
		return nil, fmt.Errorf("%w: 目标模块列表为空且未勾选整仓范围", ErrInvalidTaskParam)
	}

	// 2. 全量三层 RBAC 写权限前置校验（任意一个目标范围无权即整体拒绝 403，零任务/零子任务落库）
	if o.rbacSvc != nil {
		var forbidden []string
		for _, sub := range targetSubdirs {
			allowed, pErr := o.rbacSvc.CheckScopeWritePermission(ctx, operator, repo.ID, sub)
			if pErr != nil {
				return nil, pErr
			}
			if !allowed {
				forbidden = append(forbidden, sub)
			}
		}
		if req.IncludeRepoScope {
			allowed, pErr := o.rbacSvc.CheckScopeWritePermission(ctx, operator, repo.ID, "")
			if pErr != nil {
				return nil, pErr
			}
			if !allowed {
				forbidden = append(forbidden, "(repo)")
			}
		}
		if len(forbidden) > 0 {
			return nil, &BatchForbiddenError{ForbiddenScopes: forbidden}
		}
	}

	refName, err := o.resolveAndValidateTaskBranch(ctx, repo, req.RefName)
	if err != nil {
		return nil, err
	}

	var operatorID uint
	if operator != nil {
		operatorID = operator.ID
	}

	docTypesJSON, _ := json.Marshal(dedupSlugs)
	parentTask := &models.GenTask{
		RepoID:          repo.ID,
		Subdirectory:    "",
		RefName:         refName,
		TaskKind:        models.TaskKindBatch,
		Engine:          engine,
		Priority:        models.TaskPriorityBatch,
		DocTypesJSON:    datatypes.JSON(docTypesJSON),
		Status:          models.TaskStatusPending,
		ProgressPercent: 0,
		TriggeredBy:     operatorID,
	}
	if err := o.taskRepo.CreateTask(ctx, parentTask); err != nil {
		return nil, fmt.Errorf("创建批量生成父任务失败: %w", err)
	}

	parentCtx, cancel := context.WithCancel(o.baseCtx)
	o.registerCancel(parentTask.ID, cancel)

	// 批量父协调协程运行在 TaskScheduler worker 槽位之外，防止 MaxUserConcurrency 下父等子死锁（ADR-04 D-5）
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer o.unregisterCancel(parentTask.ID)
		o.executeBatchGenerateTask(parentCtx, parentTask.ID, repo, targetSubdirs, req.IncludeRepoScope, dedupSlugs, childMode, engine, refName, req.ExcludeRefDocIDs, operatorID)
	}()

	vo := toGenTaskDetailVO(parentTask, repo.Name, "", nil)
	return &vo, nil
}

func (o *GenerationOrchestrator) executeBatchGenerateTask(
	ctx context.Context,
	parentTaskID uint,
	repo *commonModels.Repository,
	moduleSubdirs []string,
	includeRepoScope bool,
	slugs []string,
	childMode string,
	engine string,
	refName string,
	excludeRefDocIDs []uint,
	operatorID uint,
) {
	dbCtx := context.Background()
	now := time.Now()

	if err := o.taskRepo.TransitionBatchTaskStatus(dbCtx, parentTaskID, models.TaskStatusPending, models.TaskStatusExtracting, map[string]any{
		"started_at":       now,
		"progress_percent": 5,
	}); err != nil {
		return
	}
	if ctx.Err() != nil {
		o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusExtracting)
		return
	}

	releaseRepoLease := o.leaseMgr.AcquireRepo(repo.ID)
	defer releaseRepoLease()

	// 1. 一次全仓基础快照同步与 L1 抽取，为所有子模块复用基础快照并计算批量总预算
	baseSnap, err := o.snapSvc.EnsureSnapshot(ctx, repo, "", refName)
	if err != nil {
		if ctx.Err() != nil {
			o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusExtracting)
			return
		}
		o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusExtracting, "EXTRACT_FAILED", err.Error(), nil, 0)
		return
	}
	baseSnapID := baseSnap.Snapshot.ID
	releaseSnapLease := o.leaseMgr.AcquireSnapshot(baseSnapID)
	defer releaseSnapLease()

	_ = o.taskRepo.UpdateTaskFields(dbCtx, parentTaskID, map[string]any{
		"snapshot_id":      baseSnapID,
		"progress_percent": 15,
	})

	// 2. 加载 DocType 资产并执行批前总预算熔断（SC-18：超限时 0 次 LLM 请求）
	assets := make([]*doctype.AssetBundle, 0, len(slugs))
	for _, slug := range slugs {
		dtModel, dtRev, dtErr := o.docTypeRepo.GetBySlug(dbCtx, slug)
		if dtErr != nil || dtModel == nil || dtRev == nil {
			o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusExtracting, "DOC_TYPE_NOT_FOUND", fmt.Sprintf("加载文档类型 %s 失败", slug), nil, 0)
			return
		}
		asset, loadErr := o.loadDocTypeAssetFromRevision(dtRev, slug)
		if loadErr != nil {
			o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusExtracting, "DOC_TYPE_INVALID", loadErr.Error(), nil, 0)
			return
		}
		assets = append(assets, asset)
	}

	if _, cfgErr := o.loadLLMEndpointsAndLimits(dbCtx, assets); cfgErr != nil {
		o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusExtracting, "LLM_CONFIG_INVALID", cfgErr.Error(), nil, 0)
		return
	}

	totalEstimated, breakdown := o.estimateBatchTokens(baseSnap, assets, moduleSubdirs, includeRepoScope)
	budgetLimit := o.maxTokensPerBatch
	if budgetLimit <= 0 {
		budgetLimit = 1000000
	}
	if totalEstimated > budgetLimit {
		advice := &models.BudgetAdvice{
			EstimatedTokens:  totalEstimated,
			MaxTokensPerTask: budgetLimit,
			EffectiveFiles:   baseSnap.IR.Tree.TotalFiles,
			ModuleBreakdown:  breakdown,
			Suggestions: []string{
				"请减少单次批量勾选的模块数量或文档类型，分批发起生成",
				"可在模块治理面板将非核心目录标记为 ignored 以缩小生成范围",
			},
		}
		o.markBatchTaskFailed(
			dbCtx, parentTaskID, models.TaskStatusExtracting,
			"BATCH_TOKEN_BUDGET_EXCEEDED",
			fmt.Sprintf("批量生成预估总 Token (%d) 超过批量预算上限 (%d)", totalEstimated, budgetLimit),
			advice, totalEstimated,
		)
		return
	}

	if ctx.Err() != nil {
		o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusExtracting)
		return
	}

	if err := o.taskRepo.TransitionBatchTaskStatus(dbCtx, parentTaskID, models.TaskStatusExtracting, models.TaskStatusGenerating, map[string]any{
		"snapshot_id":      baseSnapID,
		"estimated_tokens": totalEstimated,
		"progress_percent": 20,
	}); err != nil {
		return
	}

	totalExpected := len(moduleSubdirs) * len(slugs)
	if includeRepoScope {
		totalExpected += len(slugs)
	}
	if totalExpected == 0 {
		finNow := time.Now()
		_ = o.taskRepo.TransitionBatchTaskStatus(dbCtx, parentTaskID, models.TaskStatusGenerating, models.TaskStatusReady, map[string]any{
			"progress_percent": 100,
			"finished_at":      finNow,
		})
		return
	}

	// 3. 阶段一：按 (subdir, doc_type) 单文档粒度提交全部模块子任务（Priority=batch），全部通过 TaskScheduler 受控并发
	var stage1IDs []uint
	for _, sub := range moduleSubdirs {
		for _, slug := range slugs {
			if ctx.Err() != nil {
				o.cancelChildTasksByIDs(dbCtx, stage1IDs)
				o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusGenerating)
				return
			}
			childID, subErr := o.submitBatchChildTask(dbCtx, parentTaskID, repo, sub, baseSnap.Snapshot.RefName, []string{slug}, childMode, engine, excludeRefDocIDs, operatorID)
			if subErr != nil {
				// 队列满等错误记录在父任务并跳过后续创建
				o.cancelChildTasksByIDs(dbCtx, stage1IDs)
				o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusGenerating, "BATCH_CHILD_DISPATCH_FAILED", subErr.Error(), nil, totalEstimated)
				return
			}
			stage1IDs = append(stage1IDs, childID)
		}
	}

	// 等待阶段一全部模块子任务进入终态
	if len(stage1IDs) > 0 {
		if canceled := o.waitBatchChildren(ctx, dbCtx, parentTaskID, stage1IDs, 0, totalExpected); canceled {
			o.cancelChildTasksByIDs(dbCtx, stage1IDs)
			o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusGenerating)
			return
		}
	}

	// 4. 阶段二：若勾选 include_repo_scope 且父任务未取消，在所有模块子任务完成后再按单文档粒度提交整仓汇总子任务
	if includeRepoScope {
		var stage2IDs []uint
		for _, slug := range slugs {
			if ctx.Err() != nil {
				o.cancelChildTasksByIDs(dbCtx, stage2IDs)
				o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusGenerating)
				return
			}
			repoChildID, subErr := o.submitBatchChildTask(dbCtx, parentTaskID, repo, "", baseSnap.Snapshot.RefName, []string{slug}, childMode, engine, excludeRefDocIDs, operatorID)
			if subErr != nil {
				o.cancelChildTasksByIDs(dbCtx, stage2IDs)
				o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusGenerating, "BATCH_CHILD_DISPATCH_FAILED", subErr.Error(), nil, totalEstimated)
				return
			}
			stage2IDs = append(stage2IDs, repoChildID)
		}
		if len(stage2IDs) > 0 {
			if canceled := o.waitBatchChildren(ctx, dbCtx, parentTaskID, stage2IDs, len(stage1IDs), totalExpected); canceled {
				o.cancelChildTasksByIDs(dbCtx, stage2IDs)
				o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusGenerating)
				return
			}
		}
	}

	if ctx.Err() != nil {
		o.markBatchTaskCanceled(dbCtx, parentTaskID, models.TaskStatusGenerating)
		return
	}

	// 5. 汇总所有子任务终态决定父任务终态
	children, cErr := o.taskRepo.ListChildTasks(dbCtx, parentTaskID)
	if cErr != nil {
		o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusGenerating, "BATCH_SUMMARY_FAILED", cErr.Error(), nil, totalEstimated)
		return
	}
	failedCount := 0
	readyCount := 0
	for _, ch := range children {
		switch ch.Status {
		case models.TaskStatusReady:
			readyCount++
		case models.TaskStatusFailed:
			failedCount++
		}
	}
	if len(children) > 0 && readyCount == 0 && failedCount == len(children) {
		o.markBatchTaskFailed(dbCtx, parentTaskID, models.TaskStatusGenerating, "ALL_CHILDREN_FAILED", "全部批量子任务执行失败", nil, totalEstimated)
		return
	}

	finNow := time.Now()
	_ = o.taskRepo.TransitionBatchTaskStatus(dbCtx, parentTaskID, models.TaskStatusGenerating, models.TaskStatusReady, map[string]any{
		"progress_percent": 100,
		"finished_at":      finNow,
	})
}

func (o *GenerationOrchestrator) submitBatchChildTask(
	ctx context.Context,
	parentTaskID uint,
	repo *commonModels.Repository,
	subdir string,
	refName string,
	slugs []string,
	childMode string,
	engine string,
	excludeRefDocIDs []uint,
	operatorID uint,
) (uint, error) {
	docTypesJSON, _ := json.Marshal(slugs)
	var refSnapJSON datatypes.JSON
	if o.refDocSvc != nil && len(slugs) > 0 {
		repoIDCopy := repo.ID
		if eff, effErr := o.refDocSvc.ResolveEffectiveRefDocs(ctx, ResolveEffectiveRefDocsRequest{
			RepoID:        &repoIDCopy,
			Subdirectory:  subdir,
			DocTypeSlug:   slugs[0],
			ExcludeDocIDs: excludeRefDocIDs,
		}, nil); effErr == nil && eff != nil && len(eff.Snapshots) > 0 {
			b, _ := json.Marshal(eff.Snapshots)
			refSnapJSON = datatypes.JSON(b)
		}
	}
	pID := parentTaskID
	child := &models.GenTask{
		ParentTaskID:             &pID,
		RepoID:                   repo.ID,
		Subdirectory:             subdir,
		RefName:                  refName,
		TaskKind:                 childMode,
		Engine:                   models.NormalizeEngine(engine),
		Priority:                 models.TaskPriorityBatch,
		DocTypesJSON:             datatypes.JSON(docTypesJSON),
		ReferenceDocSnapshotJSON: refSnapJSON,
		Status:                   models.TaskStatusPending,
		ProgressPercent:          0,
		TriggeredBy:              operatorID,
	}
	if err := o.taskRepo.CreateTask(ctx, child); err != nil {
		return 0, fmt.Errorf("创建批量子任务 (%s) 失败: %w", subdir, err)
	}

	childCtx, cancel := context.WithCancel(o.baseCtx)
	o.registerCancel(child.ID, cancel)

	if err := o.dispatchJob(ScheduledJob{
		ID:       fmt.Sprintf("batch-child-%d", child.ID),
		UserID:   operatorID,
		Priority: models.TaskPriorityBatch,
		Ctx:      childCtx,
		Run: func(runCtx context.Context) {
			defer o.unregisterCancel(child.ID)
			o.executeGenerateTask(runCtx, child.ID, repo, slugs, operatorID)
		},
		OnDrop: func() {
			o.unregisterCancel(child.ID)
			cancel()
			o.markTaskCanceled(context.Background(), child.ID, models.TaskStatusPending)
		},
	}); err != nil {
		o.unregisterCancel(child.ID)
		cancel()
		o.markTaskFailed(ctx, child.ID, models.TaskStatusPending, "QUEUE_FULL", err.Error(), nil, 0)
		return 0, err
	}
	return child.ID, nil
}

func (o *GenerationOrchestrator) waitBatchChildren(
	ctx context.Context,
	dbCtx context.Context,
	parentTaskID uint,
	watchIDs []uint,
	priorCompleted int,
	totalExpected int,
) bool {
	watchSet := make(map[uint]bool, len(watchIDs))
	for _, id := range watchIDs {
		watchSet[id] = true
	}

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		if ctx.Err() != nil {
			return true
		}
		children, err := o.taskRepo.ListChildTasks(dbCtx, parentTaskID)
		if err == nil {
			doneInStage := 0
			for _, ch := range children {
				if !watchSet[ch.ID] {
					continue
				}
				if ch.Status == models.TaskStatusReady || ch.Status == models.TaskStatusFailed || ch.Status == models.TaskStatusCanceled {
					doneInStage++
				}
			}
			pct := 20
			if totalExpected > 0 {
				pct = 20 + int(float64(priorCompleted+doneInStage)/float64(totalExpected)*75)
			}
			_ = o.taskRepo.UpdateTaskFields(dbCtx, parentTaskID, map[string]any{"progress_percent": pct})
			if doneInStage >= len(watchIDs) {
				return false
			}
		}

		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
		}
	}
}

func (o *GenerationOrchestrator) cancelChildTasksByIDs(ctx context.Context, ids []uint) {
	for _, id := range ids {
		_, _ = o.CancelTask(ctx, id)
	}
}

func (o *GenerationOrchestrator) estimateBatchTokens(
	baseSnap *SnapshotReady,
	assets []*doctype.AssetBundle,
	moduleSubdirs []string,
	includeRepoScope bool,
) (int, []models.BudgetModuleItem) {
	glossaryCached := o.snapSvc.HasCachedGlossary(context.Background(), baseSnap.Loc)
	perAssetBase := make([]int, len(assets))
	for i, asset := range assets {
		b := llm.EstimateAndCheckBudget(llm.BudgetCheckInput{
			IR:               baseSnap.IR,
			DocTypes:         []*doctype.AssetBundle{asset},
			GlossaryCached:   glossaryCached || i > 0,
			MaxTokensPerTask: 0,
			MaxFilesPerTask:  0,
		})
		perAssetBase[i] = maxIntVal(b.Advice.EstimatedTokens, 1000)
	}
	totalFiles := maxIntVal(baseSnap.IR.Tree.TotalFiles, 1)

	breakdown := make([]models.BudgetModuleItem, 0, len(moduleSubdirs)+1)
	sumTokens := 0

	for _, sub := range moduleSubdirs {
		prefix := sub + "/"
		modFiles := 0
		for _, f := range baseSnap.IR.Tree.Files {
			if strings.HasPrefix(f.RelPath, prefix) {
				modFiles++
			}
		}
		ratio := float64(maxIntVal(modFiles, 1)) / float64(totalFiles)
		if ratio > 1 {
			ratio = 1
		}
		modSum := 0
		for _, baseTok := range perAssetBase {
			est := int(float64(baseTok) * ratio)
			if est < 500 {
				est = 500
			}
			modSum += est
		}
		sumTokens += modSum
		breakdown = append(breakdown, models.BudgetModuleItem{
			Subdirectory:    sub,
			EstimatedTokens: modSum,
			EffectiveFiles:  modFiles,
		})
	}

	if includeRepoScope {
		repoSum := 0
		for _, baseTok := range perAssetBase {
			// 整仓汇总基于模块摘要（轻量预算，约为全仓基线的 25% 或至少 2000）
			repoEst := baseTok / 4
			if len(moduleSubdirs) == 0 {
				repoEst = baseTok
			}
			if repoEst < 2000 {
				repoEst = 2000
			}
			repoSum += repoEst
		}
		sumTokens += repoSum
		breakdown = append(breakdown, models.BudgetModuleItem{
			Subdirectory:    "",
			EstimatedTokens: repoSum,
			EffectiveFiles:  baseSnap.IR.Tree.TotalFiles,
		})
	}

	return sumTokens, breakdown
}

func (o *GenerationOrchestrator) markBatchTaskFailed(ctx context.Context, taskID uint, fromStatus, errCode, errMsg string, advice *models.BudgetAdvice, estTokens int) {
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
	_ = o.taskRepo.TransitionBatchTaskStatus(ctx, taskID, fromStatus, models.TaskStatusFailed, updates)
}

func (o *GenerationOrchestrator) markBatchTaskCanceled(ctx context.Context, taskID uint, fromStatus string) {
	now := time.Now()
	_ = o.taskRepo.TransitionBatchTaskStatus(ctx, taskID, fromStatus, models.TaskStatusCanceled, map[string]any{
		"error_code":    "TASK_CANCELED",
		"error_message": "批量任务已被取消",
		"finished_at":   now,
	})
}
