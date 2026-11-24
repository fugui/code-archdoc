package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/opencode"
	"code-archdoc/pkg/refdoc"
	"code-archdoc/pkg/sandbox"
	"code-archdoc/pkg/verifier"
	commonModels "code-common/backend/models"

	"gorm.io/datatypes"
)

func formatOpenCodeModelLabel(label string) string {
	base := strings.TrimSpace(label)
	if base == "" {
		base = "default"
	}
	return truncateRunes("[opencode] "+base, 128)
}

func modelLabelForPlan(engine string, llmSet *resolvedLLMEndpoints, preferredModel string) string {
	if models.NormalizeEngine(engine) == models.EngineOpenCode {
		if oc := llmSet.openCodeModelFor(preferredModel); oc != "" {
			return formatOpenCodeModelLabel(oc)
		}
		return formatOpenCodeModelLabel(llmSet.labelFor(preferredModel))
	}
	return llmSet.labelFor(preferredModel)
}

func openCodeUsageFromResult(modelLabel string, res *opencode.DocGenResult) *llm.UsageMetrics {
	if res == nil {
		return &llm.UsageMetrics{ModelName: truncateRunes(modelLabel, 128)}
	}
	cached := res.CachedInputTokens
	if cached > res.InputTokens {
		cached = res.InputTokens
	}
	return &llm.UsageMetrics{
		ModelName:          truncateRunes(modelLabel, 128),
		PromptTokens:       res.InputTokens,
		CachedPromptTokens: cached,
		CompletionTokens:   res.OutputTokens,
		LatencyMs:          res.LatencyMs,
	}
}

func (o *GenerationOrchestrator) ensureSandboxReadyForOpenCode(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	branch string,
) (repoRoot string, workDir string, err error) {
	if o.snapSvc == nil {
		return "", "", fmt.Errorf("SnapshotService 未初始化")
	}
	repoRoot, scopeRoot, ok := o.snapSvc.ResolveSandboxScope(repo.ID, normSubdir)
	if !ok {
		syncedRoot, _, syncErr := o.snapSvc.EnsureSandboxSynced(ctx, repo, branch)
		if syncErr != nil {
			return "", "", fmt.Errorf("自动补拉 OpenCode 沙箱工作树失败: %w", syncErr)
		}
		repoRoot, scopeRoot, ok = o.snapSvc.ResolveSandboxScope(repo.ID, normSubdir)
		if !ok && strings.TrimSpace(syncedRoot) != "" {
			resolvedRepo, rErr := sandbox.SafeJoin(syncedRoot, ".")
			if rErr == nil {
				if st, stErr := os.Stat(resolvedRepo); stErr == nil && st.IsDir() {
					cleanSub, subErr := sandbox.NormalizeSubdir(normSubdir)
					if subErr == nil {
						if cleanSub == "" {
							return resolvedRepo, resolvedRepo, nil
						}
						if resolvedScope, sErr := sandbox.SafeJoin(resolvedRepo, cleanSub); sErr == nil {
							if stScope, scErr := os.Stat(resolvedScope); scErr == nil && stScope.IsDir() {
								return resolvedRepo, resolvedScope, nil
							}
						}
					}
				}
			}
		}
	}
	if !ok || strings.TrimSpace(scopeRoot) == "" {
		return "", "", fmt.Errorf("OpenCode 沙箱工作目录 %q 不可用", normSubdir)
	}
	return repoRoot, scopeRoot, nil
}

type docTypeExecPlan struct {
	slug             string
	asset            *doctype.AssetBundle
	revID            uint
	existingDoc      *models.Document
	priorCommitSHA   string
	priorOrdered     []models.DocumentSectionSnapshot
	priorSections    map[string]models.DocumentSectionSnapshot
	priorEvAnchors   map[string][]models.EvidenceAnchor
	priorEvRows      []models.SectionEvidence
	moduleDigests    []codeir.ModuleDigestInput
	crossModuleGraph []codeir.CrossModuleEdge
	reviewGuidance   map[string][]string
}

func (o *GenerationOrchestrator) executeOpenCodeDocPlans(
	ctx context.Context,
	dbCtx context.Context,
	taskID uint,
	repo *commonModels.Repository,
	task *models.GenTask,
	snapReady *SnapshotReady,
	glossary *codeir.GlossaryCache,
	plans []*docTypeExecPlan,
	llmSet *resolvedLLMEndpoints,
	slotTaskMap map[string]*models.GenSlotTask,
	activeSlots int,
	operatorID uint,
) (completedSlots int, failedSlots int) {
	ocCfg := config.AppConfig.OpenCode
	doneCount := 0
	_, refPromptDocs := o.resolveTaskPromptDocs(task)
	wholeRepoRollup := o.isWholeRepoRollup(dbCtx, repo.ID, task.Subdirectory)

	for _, p := range plans {
		var (
			activeSpecs []opencode.SlotTaskSpec
			activeRows  []*models.GenSlotTask
		)
		for _, sl := range p.asset.Meta.Slots {
			row := slotTaskMap[p.slug+"::"+sl.SlotID]
			if row == nil || row.Status == models.SlotTaskSkippedLocked {
				continue
			}
			filteredSpec := filterSlotSpecForScope(sl, task.Subdirectory, p.asset.Meta, len(p.moduleDigests) > 0)
			priorHybrid := ""
			if sl.Ownership == models.OwnershipHybrid {
				if prev, ok := p.priorSections[sl.SlotID]; ok && prev.ManuallyEdited {
					priorHybrid = prev.Markdown
				}
			}
			activeSpecs = append(activeSpecs, opencode.SlotTaskSpec{
				SlotID:          filteredSpec.SlotID,
				Title:           filteredSpec.Title,
				Ownership:       filteredSpec.Ownership,
				InstructionHint: filteredSpec.InstructionHint,
				IRFacets:        append([]string(nil), filteredSpec.IRFacets...),
				PriorHybridMD:   priorHybrid,
				ReviewGuidance:  guidanceForSlot(p.reviewGuidance, sl.SlotID),
			})
			activeRows = append(activeRows, row)
		}
		if len(activeRows) == 0 {
			continue
		}

		if ctx.Err() != nil {
			cancelAt := time.Now()
			for _, r := range activeRows {
				r.Status = models.SlotTaskCanceled
				r.FinishedAt = &cancelAt
				_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
			}
			continue
		}

		epCfg := llmSet.endpointFor(p.asset.Meta.PreferredModel)
		ocModel := llmSet.openCodeModelFor(p.asset.Meta.PreferredModel)
		modelLabel := modelLabelForPlan(models.EngineOpenCode, llmSet, p.asset.Meta.PreferredModel)

		releaseOC, ocSemErr := o.acquireOpenCodeSlot(ctx)
		if ocSemErr != nil {
			cancelAt := time.Now()
			for _, r := range activeRows {
				r.Status = models.SlotTaskCanceled
				r.FinishedAt = &cancelAt
				_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
			}
			continue
		}
		releaseLLM, llmSemErr := o.acquireLLMSlot(ctx)
		if llmSemErr != nil {
			releaseOC()
			cancelAt := time.Now()
			for _, r := range activeRows {
				r.Status = models.SlotTaskCanceled
				r.FinishedAt = &cancelAt
				_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
			}
			continue
		}

		startAt := time.Now()
		for _, r := range activeRows {
			r.Status = models.SlotTaskRunning
			r.AttemptCount++
			r.StartedAt = &startAt
			_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
		}

		var modCtxForCall *codeir.ModuleContextFacet
		if task.Subdirectory != "" && hasContextInput(p.asset.Meta.ContextInputs, "module_context") {
			modCtxForCall = snapReady.ModuleContext
		}

		res, callErr := o.runOpenCodeWithRepoLock(ctx, repo, task.Subdirectory, snapReady.Snapshot.RefName, opencode.DocGenRequest{
			Subdirectory:           task.Subdirectory,
			DocTypeSlug:            p.slug,
			DocTypeDisplayName:     p.asset.Meta.DisplayName,
			TemplateMD:             p.asset.TemplateMD,
			GenerationPromptMD:     p.asset.GenerationPromptMD,
			ActiveSlots:            activeSpecs,
			Glossary:               glossary,
			IR:                     snapReady.IR,
			ModuleContext:          modCtxForCall,
			ModuleDigests:          p.moduleDigests,
			CrossModuleGraph:       p.crossModuleGraph,
			RefDocs:                refPromptDocs,
			IsWholeRepoRollup:      wholeRepoRollup,
			ModelName:              epCfg.ModelName,
			BaseURL:                epCfg.BaseURL,
			APIKey:                 epCfg.APIKey,
			UserAgent:              epCfg.UserAgent,
			OpenCodeModelOverride:  ocModel,
			TimeoutSeconds:         ocCfg.EffectiveTimeoutSeconds(),
			IdleTimeoutSeconds:     ocCfg.EffectiveIdleTimeoutSeconds(),
			ContinuationEnabled:    ocCfg.IsContinuationEnabled(),
			ContinuationMaxSeconds: ocCfg.EffectiveContinuationMaxSeconds(),
		})
		releaseLLM()
		releaseOC()

		finAt := time.Now()
		if callErr != nil {
			errCode := "OPENCODE_" + strings.ToUpper(string(opencode.ClassifyError(callErr)))
			usageStatus := "failed"
			if ctx.Err() != nil {
				errCode = "TASK_CANCELED"
				usageStatus = "canceled"
			}
			o.recordUsageWithEngine(
				dbCtx, &taskID, nil, repo.ID, p.slug, &p.revID, "",
				models.EngineOpenCode, "doc_opencode", openCodeUsageFromResult(modelLabel, res), usageStatus, errCode, operatorID,
			)
			for _, r := range activeRows {
				r.FinishedAt = &finAt
				if ctx.Err() != nil {
					r.Status = models.SlotTaskCanceled
					r.ErrorMessage = "任务已取消"
				} else {
					r.Status = models.SlotTaskFailed
					r.ErrorMessage = callErr.Error()
					failedSlots++
				}
				_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
			}
			doneCount += len(activeRows)
			continue
		}

		o.recordUsageWithEngine(
			dbCtx, &taskID, nil, repo.ID, p.slug, &p.revID, "",
			models.EngineOpenCode, "doc_opencode", openCodeUsageFromResult(modelLabel, res), "success", "", operatorID,
		)

		for _, r := range activeRows {
			r.FinishedAt = &finAt
			out, ok := res.Slots[r.SlotID]
			if !ok || strings.TrimSpace(out.Markdown) == "" {
				r.Status = models.SlotTaskFailed
				r.ErrorMessage = fmt.Sprintf("OpenCode 未返回槽位 %s 的有效正文", r.SlotID)
				failedSlots++
			} else {
				anchorsJSON, _ := json.Marshal(out.CitedAnchors)
				r.Status = models.SlotTaskCompleted
				r.OutputMarkdown = out.Markdown
				r.CitedAnchorsJSON = datatypes.JSON(anchorsJSON)
				r.ErrorMessage = ""
				completedSlots++
			}
			_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
			doneCount++
		}

		if activeSlots > 0 {
			pct := 25 + int(float64(doneCount)/float64(activeSlots)*55)
			_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{"progress_percent": pct})
		}
	}
	return completedSlots, failedSlots
}

func (o *GenerationOrchestrator) executeOpenCodeIncrementalRefresh(
	ctx context.Context,
	dbCtx context.Context,
	taskID uint,
	repo *commonModels.Repository,
	latestDoc *models.Document,
	snapReady *SnapshotReady,
	asset *doctype.AssetBundle,
	dtRevID uint,
	glossary *codeir.GlossaryCache,
	moduleDigests []codeir.ModuleDigestInput,
	crossModuleGraph []codeir.CrossModuleEdge,
	refPromptDocs []refdoc.PromptDoc,
	wholeRepoRollup bool,
	priorBySlot map[string]models.DocumentSectionSnapshot,
	reviewGuidanceMap map[string][]string,
	llmSet *resolvedLLMEndpoints,
	slotTaskByID map[string]*models.GenSlotTask,
	operatorID uint,
) (completedSlots int, failedSlots int) {
	ocCfg := config.AppConfig.OpenCode
	var (
		activeSpecs []opencode.SlotTaskSpec
		activeRows  []*models.GenSlotTask
	)
	for _, sl := range asset.Meta.Slots {
		row, needRun := slotTaskByID[sl.SlotID]
		if !needRun {
			continue
		}
		priorHybrid := ""
		if prev, ok := priorBySlot[sl.SlotID]; ok && (prev.Ownership == models.OwnershipHybrid || prev.ManuallyEdited) {
			priorHybrid = prev.Markdown
		}
		filteredSpec := filterSlotSpecForScope(sl, snapReady.Loc.Subdir, asset.Meta, len(moduleDigests) > 0)
		activeSpecs = append(activeSpecs, opencode.SlotTaskSpec{
			SlotID:          filteredSpec.SlotID,
			Title:           filteredSpec.Title,
			Ownership:       filteredSpec.Ownership,
			InstructionHint: filteredSpec.InstructionHint,
			IRFacets:        append([]string(nil), filteredSpec.IRFacets...),
			PriorHybridMD:   priorHybrid,
			ReviewGuidance:  guidanceForSlot(reviewGuidanceMap, sl.SlotID),
		})
		activeRows = append(activeRows, row)
	}
	if len(activeRows) == 0 {
		return 0, 0
	}

	releaseOC, ocErr := o.acquireOpenCodeSlot(ctx)
	if ocErr != nil {
		cancelAt := time.Now()
		for _, r := range activeRows {
			r.Status = models.SlotTaskCanceled
			r.FinishedAt = &cancelAt
			_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
		}
		return 0, 0
	}
	releaseLLM, llmErr := o.acquireLLMSlot(ctx)
	if llmErr != nil {
		releaseOC()
		cancelAt := time.Now()
		for _, r := range activeRows {
			r.Status = models.SlotTaskCanceled
			r.FinishedAt = &cancelAt
			_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
		}
		return 0, 0
	}

	startAt := time.Now()
	for _, r := range activeRows {
		r.Status = models.SlotTaskRunning
		r.AttemptCount++
		r.StartedAt = &startAt
		_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
	}

	epCfg := llmSet.endpointFor(asset.Meta.PreferredModel)
	ocModel := llmSet.openCodeModelFor(asset.Meta.PreferredModel)
	modelLabel := modelLabelForPlan(models.EngineOpenCode, llmSet, asset.Meta.PreferredModel)
	docIDVal := latestDoc.ID

	var modCtxForCall *codeir.ModuleContextFacet
	if snapReady.Loc.Subdir != "" && hasContextInput(asset.Meta.ContextInputs, "module_context") {
		modCtxForCall = snapReady.ModuleContext
	}

	res, callErr := o.runOpenCodeWithRepoLock(ctx, repo, snapReady.Loc.Subdir, snapReady.Snapshot.RefName, opencode.DocGenRequest{
		Subdirectory:           snapReady.Loc.Subdir,
		DocTypeSlug:            asset.Slug,
		DocTypeDisplayName:     asset.Meta.DisplayName,
		TemplateMD:             asset.TemplateMD,
		GenerationPromptMD:     asset.GenerationPromptMD,
		ActiveSlots:            activeSpecs,
		Glossary:               glossary,
		IR:                     snapReady.IR,
		ModuleContext:          modCtxForCall,
		ModuleDigests:          moduleDigests,
		CrossModuleGraph:       crossModuleGraph,
		RefDocs:                refPromptDocs,
		IsWholeRepoRollup:      wholeRepoRollup,
		ModelName:              epCfg.ModelName,
		BaseURL:                epCfg.BaseURL,
		APIKey:                 epCfg.APIKey,
		UserAgent:              epCfg.UserAgent,
		OpenCodeModelOverride:  ocModel,
		TimeoutSeconds:         ocCfg.EffectiveTimeoutSeconds(),
		IdleTimeoutSeconds:     ocCfg.EffectiveIdleTimeoutSeconds(),
		ContinuationEnabled:    ocCfg.IsContinuationEnabled(),
		ContinuationMaxSeconds: ocCfg.EffectiveContinuationMaxSeconds(),
	})
	releaseLLM()
	releaseOC()

	finAt := time.Now()
	if callErr != nil {
		errCode := "OPENCODE_" + strings.ToUpper(string(opencode.ClassifyError(callErr)))
		usageStatus := "failed"
		if ctx.Err() != nil {
			errCode = "TASK_CANCELED"
			usageStatus = "canceled"
		}
		o.recordUsageWithEngine(
			dbCtx, &taskID, &docIDVal, repo.ID, asset.Slug, &dtRevID, "",
			models.EngineOpenCode, "inc_refresh_opencode", openCodeUsageFromResult(modelLabel, res), usageStatus, errCode, operatorID,
		)
		for _, r := range activeRows {
			r.FinishedAt = &finAt
			if ctx.Err() != nil {
				r.Status = models.SlotTaskCanceled
				r.ErrorMessage = "任务已取消"
			} else {
				r.Status = models.SlotTaskFailed
				r.ErrorMessage = callErr.Error()
				failedSlots++
			}
			_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
		}
		return 0, failedSlots
	}

	o.recordUsageWithEngine(
		dbCtx, &taskID, &docIDVal, repo.ID, asset.Slug, &dtRevID, "",
		models.EngineOpenCode, "inc_refresh_opencode", openCodeUsageFromResult(modelLabel, res), "success", "", operatorID,
	)

	for _, r := range activeRows {
		r.FinishedAt = &finAt
		out, ok := res.Slots[r.SlotID]
		if !ok || strings.TrimSpace(out.Markdown) == "" {
			r.Status = models.SlotTaskFailed
			r.ErrorMessage = fmt.Sprintf("OpenCode 增量刷新未返回槽位 %s 的有效正文", r.SlotID)
			failedSlots++
		} else {
			anchorsJSON, _ := json.Marshal(out.CitedAnchors)
			r.Status = models.SlotTaskCompleted
			r.OutputMarkdown = out.Markdown
			r.CitedAnchorsJSON = datatypes.JSON(anchorsJSON)
			r.ErrorMessage = ""
			completedSlots++
		}
		_ = o.taskRepo.UpdateSlotTask(dbCtx, r)
	}
	_ = o.taskRepo.UpdateTaskFields(dbCtx, taskID, map[string]any{"progress_percent": 80})
	return completedSlots, failedSlots
}

func (o *GenerationOrchestrator) executeOpenCodeSlotRetry(
	ctx context.Context,
	task *models.GenTask,
	slotTask *models.GenSlotTask,
	snapReady *SnapshotReady,
	asset *doctype.AssetBundle,
	dtRevID uint,
	targetSlot doctype.SlotSpec,
	glossary *codeir.GlossaryCache,
	moduleDigests []codeir.ModuleDigestInput,
	crossModuleGraph []codeir.CrossModuleEdge,
	refPromptDocs []refdoc.PromptDoc,
	wholeRepoRollup bool,
	priorHybridMD string,
	reviewGuidance []string,
	epCfg llm.EndpointConfig,
	llmSet *resolvedLLMEndpoints,
	operatorID uint,
) (string, []verifier.CitedAnchorCandidate, error) {
	var repo *commonModels.Repository
	if o.masterRepo != nil {
		repo, _ = o.masterRepo.GetRepository(ctx, task.RepoID)
	}
	if repo == nil {
		repo = &commonModels.Repository{ID: task.RepoID, Name: fmt.Sprintf("repo-%d", task.RepoID)}
	}

	releaseOC, ocErr := o.acquireOpenCodeSlot(ctx)
	if ocErr != nil {
		return "", nil, fmt.Errorf("等待 OpenCode 并发槽位被取消: %w", ocErr)
	}
	defer releaseOC()

	releaseLLM, llmErr := o.acquireLLMSlot(ctx)
	if llmErr != nil {
		return "", nil, fmt.Errorf("等待 LLM 并发槽位被取消: %w", llmErr)
	}
	defer releaseLLM()

	ocCfg := config.AppConfig.OpenCode
	ocModel := llmSet.openCodeModelFor(asset.Meta.PreferredModel)
	modelLabel := modelLabelForPlan(models.EngineOpenCode, llmSet, asset.Meta.PreferredModel)

	var modCtxForCall *codeir.ModuleContextFacet
	if task.Subdirectory != "" && hasContextInput(asset.Meta.ContextInputs, "module_context") {
		modCtxForCall = snapReady.ModuleContext
	}

	res, callErr := o.runOpenCodeWithRepoLock(ctx, repo, task.Subdirectory, snapReady.Snapshot.RefName, opencode.DocGenRequest{
		Subdirectory:       task.Subdirectory,
		DocTypeSlug:        asset.Slug,
		DocTypeDisplayName: asset.Meta.DisplayName,
		TemplateMD:         asset.TemplateMD,
		GenerationPromptMD: asset.GenerationPromptMD,
		ActiveSlots: []opencode.SlotTaskSpec{{
			SlotID:          targetSlot.SlotID,
			Title:           targetSlot.Title,
			Ownership:       targetSlot.Ownership,
			InstructionHint: targetSlot.InstructionHint,
			IRFacets:        append([]string(nil), targetSlot.IRFacets...),
			PriorHybridMD:   priorHybridMD,
			ReviewGuidance:  reviewGuidance,
		}},
		Glossary:               glossary,
		IR:                     snapReady.IR,
		ModuleContext:          modCtxForCall,
		ModuleDigests:          moduleDigests,
		CrossModuleGraph:       crossModuleGraph,
		RefDocs:                refPromptDocs,
		IsWholeRepoRollup:      wholeRepoRollup,
		ModelName:              epCfg.ModelName,
		BaseURL:                epCfg.BaseURL,
		APIKey:                 epCfg.APIKey,
		UserAgent:              epCfg.UserAgent,
		OpenCodeModelOverride:  ocModel,
		TimeoutSeconds:         ocCfg.EffectiveTimeoutSeconds(),
		IdleTimeoutSeconds:     ocCfg.EffectiveIdleTimeoutSeconds(),
		ContinuationEnabled:    ocCfg.IsContinuationEnabled(),
		ContinuationMaxSeconds: ocCfg.EffectiveContinuationMaxSeconds(),
	})
	if callErr != nil {
		o.recordUsageWithEngine(
			ctx, &task.ID, slotTask.DocumentID, task.RepoID, asset.Slug, &dtRevID, targetSlot.SlotID,
			models.EngineOpenCode, "slot_retry_opencode", openCodeUsageFromResult(modelLabel, res), "failed", "OPENCODE_RETRY_FAILED", operatorID,
		)
		return "", nil, callErr
	}

	out, ok := res.Slots[targetSlot.SlotID]
	if !ok || strings.TrimSpace(out.Markdown) == "" {
		o.recordUsageWithEngine(
			ctx, &task.ID, slotTask.DocumentID, task.RepoID, asset.Slug, &dtRevID, targetSlot.SlotID,
			models.EngineOpenCode, "slot_retry_opencode", openCodeUsageFromResult(modelLabel, res), "failed", "OPENCODE_OUTPUT_MISSING", operatorID,
		)
		return "", nil, fmt.Errorf("OpenCode 单槽位重试未返回槽位 %s 内容", targetSlot.SlotID)
	}

	o.recordUsageWithEngine(
		ctx, &task.ID, slotTask.DocumentID, task.RepoID, asset.Slug, &dtRevID, targetSlot.SlotID,
		models.EngineOpenCode, "slot_retry_opencode", openCodeUsageFromResult(modelLabel, res), "success", "", operatorID,
	)
	return out.Markdown, out.CitedAnchors, nil
}

func (o *GenerationOrchestrator) runOpenCodeWithRepoLock(
	ctx context.Context,
	repo *commonModels.Repository,
	normSubdir string,
	branch string,
	req opencode.DocGenRequest,
) (*opencode.DocGenResult, error) {
	if o.opencodeRunner == nil {
		return nil, &opencode.ClassifiedError{Class: opencode.ErrorClassCLINotFound, Message: "OpenCode Runner 未初始化"}
	}
	repoRoot, workDir, err := o.ensureSandboxReadyForOpenCode(ctx, repo, normSubdir, branch)
	if err != nil {
		return nil, err
	}
	req.RepoRoot = repoRoot
	req.WorkDir = workDir

	var (
		res    *opencode.DocGenResult
		runErr error
	)
	lockErr := o.snapSvc.WithRepoReadLock(repo.ID, func() error {
		res, runErr = o.opencodeRunner.GenerateDocument(ctx, req)
		return nil
	})
	if lockErr != nil && runErr == nil {
		runErr = lockErr
	}
	return res, runErr
}
