package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/handlers"
	"code-archdoc/models"
	"code-archdoc/pkg/opencode"
	"code-archdoc/services"

	"gorm.io/datatypes"
)

// T-ADR08-11：OpenCode 执行链闭环（Mock OpenCode：全量生成 + Verifier 门禁落库 + 单槽位增量刷新锁定字节不变 + 缺槽位部分落库单槽位重试 + Mode B Glossary 降级）
func TestOpenCodeExecutionPipelineVerifyLockPreserveRetryAndModeB(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	origOpenCodeCfg := config.AppConfig.OpenCode
	defer func() { config.AppConfig.OpenCode = origOpenCodeCfg }()

	workDir := t.TempDir()
	binDir := t.TempDir()
	modeFile := filepath.Join(t.TempDir(), "mode.txt")
	_ = os.WriteFile(modeFile, []byte("full"), 0o600)

	// Fake opencode：支持 --version；full 模式返回 overview+architecture；partial 仅返回 overview；retry 补齐 architecture；inc_refresh 仅刷新指定槽位
	fakeBin := filepath.Join(binDir, "opencode")
	scriptBody := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  echo "opencode 1.18.25"
  exit 0
fi
MODE=$(cat %q)
printf '{"type":"step_start","sessionID":"ses_e2e_1"}\n'
case "$MODE" in
  full)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_1","part":{"type":"text","messageID":"m1","text":"{\"slots\":{\"overview\":{\"markdown\":\"OpenCode 全量概览（OrderService 创建订单并流转支付状态）。\",\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::OrderService::Create\",\"scope\":\"module\"}]},\"architecture\":{\"markdown\":\"OpenCode 架构分层设计（包含核心订单状态机）。\",\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::OrderService::Pay\",\"scope\":\"module\"}]}}}"}}'
    ;;
  inc_refresh)
    # 故意仅返回 architecture，验证 locked 槽位零修改
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_2","part":{"type":"text","messageID":"m2","text":"{\"slots\":{\"architecture\":{\"markdown\":\"OpenCode 增量刷新后的最新架构段落（OrderService::Pay 幂等保障）。\",\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::OrderService::Pay\",\"scope\":\"module\"}]}}}"}}'
    ;;
  partial)
    # 仅产出 overview，刻意遗漏 architecture 触发部分落库
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_3","part":{"type":"text","messageID":"m3","text":"{\"slots\":{\"overview\":{\"markdown\":\"OpenCode 首轮概览部分落库内容。\",\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::OrderService::Create\",\"scope\":\"module\"}]}}}"}}'
    ;;
  retry_slot)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_4","part":{"type":"text","messageID":"m4","text":"{\"slots\":{\"architecture\":{\"markdown\":\"OpenCode 单槽位重试补齐 architecture 章节。\",\"cited_anchors\":[{\"file_path\":\"src/order.cpp\",\"fq_symbol\":\"order::OrderService::Pay\",\"scope\":\"module\"}]}}}"}}'
    ;;
esac
printf '{"type":"step_finish","sessionID":"ses_e2e_1","part":{"tokens":{"input":200,"output":90,"cache":{"read":50}}}}\n'
`, modeFile)
	if err := os.WriteFile(fakeBin, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	ocEnabled := true
	ocCont := false
	config.AppConfig.OpenCode = config.OpenCodeSection{
		Enabled:             &ocEnabled,
		BinaryPath:          fakeBin,
		TimeoutSeconds:      60,
		IdleTimeoutSeconds:  20,
		MaxConcurrency:      2,
		ContinuationEnabled: &ocCont,
	}
	env.genSvc.WithOpenCodeRunner(opencode.NewRunner(opencode.Config{
		Enabled:             true,
		BinaryPath:          fakeBin,
		TimeoutSeconds:      60,
		IdleTimeoutSeconds:  20,
		ContinuationEnabled: false,
	}), 2)

	var glossaryCalls atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		glossaryCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"module_summaries\":[],\"terms\":[{\"term\":\"OrderService\",\"canonical_name\":\"order::OrderService\",\"category\":\"service\",\"explanation\":\"订单服务\"}]}"}}],"usage":{"prompt_tokens":30,"completion_tokens":20}}`))
	}))
	defer mockLLM.Close()

	if err := env.db.Create(&models.LLMConfig{
		Name: "DeepSeek-V3", BaseURL: mockLLM.URL, ModelName: "deepseek-v3",
		APIKey: "sk-native-glossary", APIKeyMasked: "sk-****",
		TimeoutSeconds: 30, MaxRetries: 0, MaxTokensPerTask: 500000, MaxFilesPerTask: 2000,
		IsActive: true, IsDefault: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 将 DocType system-architecture 简化为双槽位 (overview + architecture) 并绑定当前 testdata 沙箱真实路径
	dtDetail, err := env.docTypeSvc.GetDetail(context.Background(), "system-architecture")
	if err != nil {
		t.Fatal(err)
	}
	meta := dtDetail.Meta
	meta.Slots = meta.Slots[:2]
	if _, _, _, err := env.docTypeSvc.Update(context.Background(), "system-architecture", services.UpdateDocTypeRequest{
		Meta:               meta,
		TemplateMD:         "<!-- slot:" + meta.Slots[0].SlotID + " -->\n\n<!-- slot:" + meta.Slots[1].SlotID + " -->\n",
		GenerationPromptMD: dtDetail.GenerationPromptMD,
		RefinePromptMD:     dtDetail.RefinePromptMD,
	}, env.owner.ID); err != nil {
		t.Fatal(err)
	}
	_ = workDir

	// Step 1: GET /api/engines 报告 opencode available=true, version=1.18.25
	reqEng := httptest.NewRequest(http.MethodGet, "/api/engines", nil)
	reqEng.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recEng := httptest.NewRecorder()
	env.router.ServeHTTP(recEng, reqEng)
	if recEng.Code != http.StatusOK {
		t.Fatalf("GET /api/engines 失败: %d %s", recEng.Code, recEng.Body.String())
	}
	var engResp services.EnginesResponse
	_ = json.Unmarshal(recEng.Body.Bytes(), &engResp)
	var ocVO *services.EngineInfoVO
	for i := range engResp.Engines {
		if engResp.Engines[i].Name == models.EngineOpenCode {
			ocVO = &engResp.Engines[i]
		}
	}
	if ocVO == nil || !ocVO.Available || ocVO.Version != "1.18.25" {
		t.Fatalf("GET /api/engines 期望 opencode available=true version=1.18.25，实际 %+v", engResp)
	}

	// Step 2: 触发 OpenCode 全量生成（确保 SlotID 匹配测试夹具 overview & system-context/architecture）
	// 动态适配 system-architecture 真实前两个 slot IDs 并重写 Fake 脚本返回这两个 ID
	s1 := meta.Slots[0].SlotID
	s2 := meta.Slots[1].SlotID
	scriptBody2 := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  echo "opencode 1.18.25"
  exit 0
fi
MODE=$(cat %q)
printf '{"type":"step_start","sessionID":"ses_e2e_1"}\n'
case "$MODE" in
  full)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_1","part":{"type":"text","messageID":"m1","text":"{\"slots\":{\"%s\":{\"markdown\":\"OpenCode 全量概览（OrderService 创建订单）。\",\"cited_anchors\":[{\"file_path\":\"src/order_service.cpp\",\"fq_symbol\":\"order::OrderService::CreateOrder\",\"scope\":\"module\"}]},\"%s\":{\"markdown\":\"OpenCode 架构分层（OrderService::Pay）。\",\"cited_anchors\":[{\"file_path\":\"src/order_service.cpp\",\"fq_symbol\":\"order::OrderService::Pay\",\"scope\":\"module\"}]}}}"}}'
    ;;
  inc_refresh)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_2","part":{"type":"text","messageID":"m2","text":"{\"slots\":{\"%s\":{\"markdown\":\"OpenCode 增量刷新后的 %s 正文。\",\"cited_anchors\":[]}}}"}}'
    ;;
  partial)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_3","part":{"type":"text","messageID":"m3","text":"{\"slots\":{\"%s\":{\"markdown\":\"OpenCode 首轮部分落库正文。\",\"cited_anchors\":[]}}}"}}'
    ;;
  retry_slot)
    printf '%%s\n' '{"type":"text","sessionID":"ses_e2e_4","part":{"type":"text","messageID":"m4","text":"{\"slots\":{\"%s\":{\"markdown\":\"OpenCode 单槽位重试补齐正文。\",\"cited_anchors\":[]}}}"}}'
    ;;
esac
printf '{"type":"step_finish","sessionID":"ses_e2e_1","part":{"tokens":{"input":200,"output":90,"cache":{"read":50}}}}\n'
`, modeFile, s1, s2, s2, s2, s1, s2)
	if err := os.WriteFile(fakeBin, []byte(scriptBody2), 0o755); err != nil {
		t.Fatal(err)
	}

	genBody, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture"},
		"engine":    "opencode",
	})
	reqGen := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(genBody))
	reqGen.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqGen.Header.Set("Content-Type", "application/json")
	recGen := httptest.NewRecorder()
	env.router.ServeHTTP(recGen, reqGen)
	if recGen.Code != http.StatusAccepted {
		t.Fatalf("POST /api/tasks/generate (opencode) 失败: %d %s", recGen.Code, recGen.Body.String())
	}
	var genSub services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(recGen.Body.Bytes(), &genSub)
	if genSub.Engine != models.EngineOpenCode {
		t.Fatalf("期望返回 engine=opencode，实际 %q", genSub.Engine)
	}
	env.genSvc.WaitAll()

	detail1, err := env.genSvc.GetTaskDetail(context.Background(), genSub.ID)
	if err != nil || detail1.Status != models.TaskStatusReady {
		t.Fatalf("OpenCode 任务期望 ready，实际 status=%s errCode=%s msg=%s err=%v", detail1.Status, detail1.ErrorCode, detail1.ErrorMessage, err)
	}
	if len(detail1.SlotTasks) != 2 || !strings.HasPrefix(detail1.SlotTasks[0].ModelName, "[opencode]") {
		t.Fatalf("期望槽位 ModelName 带 [opencode] 前缀，实际 slots=%+v", detail1.SlotTasks)
	}

	// 验证 LLMUsageLog 记录了 engine=opencode, stage=doc_opencode
	var usageLogs []models.LLMUsageLog
	if err := env.db.Where("task_id = ?", genSub.ID).Find(&usageLogs).Error; err != nil {
		t.Fatal(err)
	}
	foundOCUsage := false
	for _, ul := range usageLogs {
		if ul.Engine == models.EngineOpenCode && ul.Stage == "doc_opencode" && ul.Status == "success" {
			foundOCUsage = true
		}
	}
	if !foundOCUsage {
		t.Fatalf("未记录 engine=opencode, stage=doc_opencode 的用量日志: %+v", usageLogs)
	}

	// Step 3: 锁定槽位 s1 后，触发增量刷新（engine 省略 -> 自动沿用最近一次 opencode）
	docID := *detail1.SlotTasks[0].DocumentID
	docStore := models.NewDocumentStore(env.db)
	docBefore, _ := docStore.GetByID(context.Background(), docID)
	revBefore, _, _ := docStore.GetRevisionWithSections(context.Background(), docID, docBefore.CurrentRevisionNo)
	if err = docStore.UpdateSectionOwnership(context.Background(), docID, revBefore.ID, s1, models.OwnershipLocked, false); err != nil {
		t.Fatal(err)
	}
	// 直接记录锁定前 s1 的 ContentSHA256
	var beforeSecs []models.DocumentSectionSnapshot
	_ = json.Unmarshal(revBefore.SectionsJSON, &beforeSecs)
	s1HashBefore := ""
	for _, sn := range beforeSecs {
		if sn.SlotID == s1 {
			sum := sha256.Sum256([]byte(sn.Markdown))
			s1HashBefore = hex.EncodeToString(sum[:])
		}
	}

	_ = os.WriteFile(modeFile, []byte("inc_refresh"), 0o600)
	incTaskVO, err := env.genSvc.SubmitIncrementalRefreshTask(context.Background(), services.IncrementalRefreshTaskInput{
		Document:    docBefore,
		TargetSlots: []string{s1, s2},
		Priority:    models.TaskPriorityInteractive,
	}, env.owner.ID)
	if err != nil {
		t.Fatalf("SubmitIncrementalRefreshTask 失败: %v", err)
	}
	if incTaskVO.Engine != models.EngineOpenCode {
		t.Fatalf("增量刷新省略 engine 应沿用最近 opencode 引擎，实际 %q", incTaskVO.Engine)
	}
	env.genSvc.WaitAll()

	docAfterInc, _ := docStore.GetByID(context.Background(), docID)
	revAfterInc, _, _ := docStore.GetRevisionWithSections(context.Background(), docID, docAfterInc.CurrentRevisionNo)
	var afterIncSecs []models.DocumentSectionSnapshot
	_ = json.Unmarshal(revAfterInc.SectionsJSON, &afterIncSecs)
	for _, sn := range afterIncSecs {
		if sn.SlotID == s1 {
			sum := sha256.Sum256([]byte(sn.Markdown))
			if hex.EncodeToString(sum[:]) != s1HashBefore {
				t.Fatalf("OpenCode 增量刷新违规改动了 locked 槽位 %s 的内容！", s1)
			}
		}
		if sn.SlotID == s2 && !strings.Contains(sn.Markdown, "增量刷新后") {
			t.Fatalf("OpenCode 增量刷新未更新目标槽位 %s: %s", s2, sn.Markdown)
		}
	}

	// Step 4: 测试 Mode B Glossary 降级（配置 opencode_model 且停用/损坏 default LLM Endpoint）+ 部分落库与单槽位重试
	_ = os.WriteFile(modeFile, []byte("partial"), 0o600)
	if err := env.db.Model(&models.LLMConfig{}).Where("name = ?", "DeepSeek-V3").Updates(map[string]any{
		"base_url":       "http://127.0.0.1:1/unreachable",
		"opencode_model": "deepseek/deepseek-chat",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 另选 ops-and-config 文档类型执行 Mode B + 部分落库
	opsDT, _ := env.docTypeSvc.GetDetail(context.Background(), "ops-and-config")
	opsMeta := opsDT.Meta
	opsMeta.Slots = opsMeta.Slots[:2]
	_, _, _, _ = env.docTypeSvc.Update(context.Background(), "ops-and-config", services.UpdateDocTypeRequest{
		Meta:               opsMeta,
		TemplateMD:         "<!-- slot:" + opsMeta.Slots[0].SlotID + " -->\n\n<!-- slot:" + opsMeta.Slots[1].SlotID + " -->\n",
		GenerationPromptMD: opsDT.GenerationPromptMD,
		RefinePromptMD:     opsDT.RefinePromptMD,
	}, env.owner.ID)

	// 更新 fakeBin 适配 opsMeta 槽位
	os1, os2 := opsMeta.Slots[0].SlotID, opsMeta.Slots[1].SlotID
	scriptBody3 := strings.ReplaceAll(strings.ReplaceAll(scriptBody2, s1, os1), s2, os2)
	if err := os.WriteFile(fakeBin, []byte(scriptBody3), 0o755); err != nil {
		t.Fatal(err)
	}

	partBody, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"ops-and-config"},
		"engine":    "opencode",
	})
	reqPart := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(partBody))
	reqPart.Header.Set("Authorization", "Bearer "+env.ownerTok)
	reqPart.Header.Set("Content-Type", "application/json")
	recPart := httptest.NewRecorder()
	env.router.ServeHTTP(recPart, reqPart)
	if recPart.Code != http.StatusAccepted {
		t.Fatalf("Mode B 部分落库任务提交失败: %d %s", recPart.Code, recPart.Body.String())
	}
	var partSub services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(recPart.Body.Bytes(), &partSub)
	env.genSvc.WaitAll()

	partDetail, _ := env.genSvc.GetTaskDetail(context.Background(), partSub.ID)
	if partDetail.Status != models.TaskStatusReady {
		t.Fatalf("Mode B + 1/2 槽位成功应部分落库并收敛为 ready（Glossary 应自动降级），实际 status=%s err=%s:%s", partDetail.Status, partDetail.ErrorCode, partDetail.ErrorMessage)
	}
	var failedSlotID uint
	for _, st := range partDetail.SlotTasks {
		if st.SlotID == os2 && st.Status == models.SlotTaskFailed {
			failedSlotID = st.ID
		}
	}
	if failedSlotID == 0 {
		t.Fatalf("期望 %s 为 failed 以待单槽位重试，实际 slots=%+v", os2, partDetail.SlotTasks)
	}

	// 单槽位 OpenCode 重试补齐 os2
	_ = os.WriteFile(modeFile, []byte("retry_slot"), 0o600)
	reqRetry := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tasks/%d/slots/%d/retry", partSub.ID, failedSlotID), nil)
	reqRetry.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recRetry := httptest.NewRecorder()
	env.router.ServeHTTP(recRetry, reqRetry)
	if recRetry.Code != http.StatusAccepted {
		t.Fatalf("OpenCode 单槽位重试期望 202，实际 %d %s", recRetry.Code, recRetry.Body.String())
	}
	env.genSvc.WaitAll()

	afterRetryDetail, _ := env.genSvc.GetTaskDetail(context.Background(), partSub.ID)
	for _, st := range afterRetryDetail.SlotTasks {
		if st.ID == failedSlotID && st.Status != models.SlotTaskCompleted {
			t.Fatalf("OpenCode 单槽位重试后应变为 completed，实际 %+v", st)
		}
	}
	var retryUsage models.LLMUsageLog
	if err := env.db.Where("task_id = ? AND stage = ?", partSub.ID, "slot_retry_opencode").First(&retryUsage).Error; err != nil {
		t.Fatalf("未记录 stage=slot_retry_opencode 用量日志: %v", err)
	}
}

// T-ADR08-12：双引擎不可用精准拦截（ENGINE_UNAVAILABLE 400）、任务 ?engine= 筛选与 opencode_model 配置闭环
func TestEngineUnavailableErrorsFilteringAndLLMConfigOpenCodeModel(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	origOpenCodeCfg := config.AppConfig.OpenCode
	defer func() { config.AppConfig.OpenCode = origOpenCodeCfg }()

	ocDisabled := false
	config.AppConfig.OpenCode = config.OpenCodeSection{Enabled: &ocDisabled}
	env.genSvc.WithOpenCodeRunner(opencode.NewRunner(opencode.Config{Enabled: false}), 2)

	// 1. 请求非法引擎名与禁用态 opencode 均返回 400 ENGINE_UNAVAILABLE
	for _, badEngine := range []string{"unknown-engine", "opencode"} {
		body, _ := json.Marshal(map[string]any{
			"repo_id":   env.repo.ID,
			"doc_types": []string{"system-architecture"},
			"engine":    badEngine,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+env.ownerTok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), handlers.ErrCodeEngineUnavailable) {
			t.Fatalf("引擎 %q 不可用期望 400 ENGINE_UNAVAILABLE，实际 %d %s", badEngine, rec.Code, rec.Body.String())
		}

		// Batch generate 也必须返回 400 ENGINE_UNAVAILABLE
		batchBody, _ := json.Marshal(map[string]any{
			"repo_id":            env.repo.ID,
			"include_repo_scope": true,
			"doc_types":          []string{"system-architecture"},
			"engine":             badEngine,
		})
		reqB := httptest.NewRequest(http.MethodPost, "/api/tasks/generate-batch", bytes.NewReader(batchBody))
		reqB.Header.Set("Authorization", "Bearer "+env.ownerTok)
		reqB.Header.Set("Content-Type", "application/json")
		recB := httptest.NewRecorder()
		env.router.ServeHTTP(recB, reqB)
		if recB.Code != http.StatusBadRequest || !strings.Contains(recB.Body.String(), handlers.ErrCodeEngineUnavailable) {
			t.Fatalf("批量生成引擎 %q 不可用期望 400 ENGINE_UNAVAILABLE，实际 %d %s", badEngine, recB.Code, recB.Body.String())
		}
	}

	// 2. 创建含不同 engine 的历史任务，校验 GET /api/tasks?engine=native|opencode & 非法 engine 400
	dtJSON := datatypes.JSON([]byte(`["system-architecture"]`))
	tNative := models.GenTask{RepoID: env.repo.ID, TaskKind: models.TaskKindGenerate, Engine: models.EngineNative, Priority: models.TaskPriorityInteractive, DocTypesJSON: dtJSON, Status: models.TaskStatusReady, TriggeredBy: env.owner.ID}
	tOC := models.GenTask{RepoID: env.repo.ID, TaskKind: models.TaskKindGenerate, Engine: models.EngineOpenCode, Priority: models.TaskPriorityInteractive, DocTypesJSON: dtJSON, Status: models.TaskStatusReady, TriggeredBy: env.owner.ID}
	if err := env.db.Create(&tNative).Error; err != nil {
		t.Fatal(err)
	}
	if err := env.db.Create(&tOC).Error; err != nil {
		t.Fatal(err)
	}

	reqListOC := httptest.NewRequest(http.MethodGet, "/api/tasks?engine=opencode", nil)
	reqListOC.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recListOC := httptest.NewRecorder()
	env.router.ServeHTTP(recListOC, reqListOC)
	if recListOC.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks?engine=opencode 失败: %d %s", recListOC.Code, recListOC.Body.String())
	}
	var listResp services.TaskListResponse
	_ = json.Unmarshal(recListOC.Body.Bytes(), &listResp)
	if len(listResp.Items) != 1 || listResp.Items[0].ID != tOC.ID || listResp.Items[0].Engine != models.EngineOpenCode {
		t.Fatalf("?engine=opencode 筛选结果异常: %+v", listResp.Items)
	}

	reqBadFilter := httptest.NewRequest(http.MethodGet, "/api/tasks?engine=foo", nil)
	reqBadFilter.Header.Set("Authorization", "Bearer "+env.ownerTok)
	recBadFilter := httptest.NewRecorder()
	env.router.ServeHTTP(recBadFilter, reqBadFilter)
	if recBadFilter.Code != http.StatusBadRequest || !strings.Contains(recBadFilter.Body.String(), handlers.ErrCodeInvalidParam) {
		t.Fatalf("非法 ?engine=foo 期望 400 INVALID_PARAM，实际 %d %s", recBadFilter.Code, recBadFilter.Body.String())
	}

	// 3. 管理员 LLM 配置 CRUD 对 opencode_model 持久化与非法空白校验
	adminUser := createTestUser(t, env.db, "oc_admin", "管理员", []string{"user", config.AdminRole}, nil)
	adminTok := issueTestToken(t, adminUser)
	badPayload, _ := json.Marshal(map[string]any{
		"name":           "bad-oc",
		"base_url":       "http://127.0.0.1:8000/v1",
		"model_name":     "m1",
		"opencode_model": "provider/bad model",
		"is_active":      true,
	})
	reqBadLLM := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs", bytes.NewReader(badPayload))
	reqBadLLM.Header.Set("Authorization", "Bearer "+adminTok)
	reqBadLLM.Header.Set("Content-Type", "application/json")
	recBadLLM := httptest.NewRecorder()
	env.router.ServeHTTP(recBadLLM, reqBadLLM)
	if recBadLLM.Code != http.StatusBadRequest {
		t.Fatalf("含空格的 opencode_model 期望 400，实际 %d %s", recBadLLM.Code, recBadLLM.Body.String())
	}

	okPayload, _ := json.Marshal(map[string]any{
		"name":           "oc-override-cfg",
		"base_url":       "http://127.0.0.1:8000/v1",
		"model_name":     "m1",
		"opencode_model": "deepseek/deepseek-chat",
		"is_active":      true,
		"is_default":     true,
	})
	reqOKLLM := httptest.NewRequest(http.MethodPost, "/api/admin/llm-configs", bytes.NewReader(okPayload))
	reqOKLLM.Header.Set("Authorization", "Bearer "+adminTok)
	reqOKLLM.Header.Set("Content-Type", "application/json")
	recOKLLM := httptest.NewRecorder()
	env.router.ServeHTTP(recOKLLM, reqOKLLM)
	if recOKLLM.Code != http.StatusCreated {
		t.Fatalf("创建带 opencode_model 配置失败: %d %s", recOKLLM.Code, recOKLLM.Body.String())
	}
	var createdLLM services.LLMConfigVO
	_ = json.Unmarshal(recOKLLM.Body.Bytes(), &createdLLM)
	if createdLLM.OpenCodeModel != "deepseek/deepseek-chat" {
		t.Fatalf("未返回持久化后的 opencode_model: %+v", createdLLM)
	}
}

type blockingOpenCodeRunner struct {
	maxObserved atomic.Int32
	current     atomic.Int32
	startedCh   chan struct{}
	releaseCh   chan struct{}
}

func (b *blockingOpenCodeRunner) Probe(_ context.Context) opencode.ProbeStatus {
	return opencode.ProbeStatus{Available: true, BinaryPath: "/usr/bin/opencode", Version: "1.18.25"}
}

func (b *blockingOpenCodeRunner) GenerateDocument(ctx context.Context, req opencode.DocGenRequest) (*opencode.DocGenResult, error) {
	cur := b.current.Add(1)
	for {
		prev := b.maxObserved.Load()
		if cur <= prev || b.maxObserved.CompareAndSwap(prev, cur) {
			break
		}
	}
	select {
	case b.startedCh <- struct{}{}:
	default:
	}
	select {
	case <-b.releaseCh:
	case <-ctx.Done():
		b.current.Add(-1)
		return nil, ctx.Err()
	}
	b.current.Add(-1)

	slots := make(map[string]opencode.SlotOutput, len(req.ActiveSlots))
	for _, s := range req.ActiveSlots {
		slots[s.SlotID] = opencode.SlotOutput{Markdown: "并发验证正文 " + s.SlotID}
	}
	return &opencode.DocGenResult{SessionID: "ses_conc", Slots: slots, InputTokens: 10, OutputTokens: 10}, nil
}

// T-ADR08-13：OpenCode 独立并发限流（opencodeSem <= MaxConcurrency=1）与工作树读锁/写锁互斥不竞态
func TestOpenCodeConcurrencyCapAndRepoWorktreeRWLock(t *testing.T) {
	env := setupM2TestEnv(t, 4)

	origOpenCodeCfg := config.AppConfig.OpenCode
	defer func() { config.AppConfig.OpenCode = origOpenCodeCfg }()

	ocEnabled := true
	config.AppConfig.OpenCode = config.OpenCodeSection{Enabled: &ocEnabled, MaxConcurrency: 1}

	runner := &blockingOpenCodeRunner{
		startedCh: make(chan struct{}, 4),
		releaseCh: make(chan struct{}),
	}
	env.genSvc.WithOpenCodeRunner(runner, 1)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"module_summaries\":[],\"terms\":[]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":10}}`))
	}))
	defer mockLLM.Close()

	if err := env.db.Create(&models.LLMConfig{
		Name: "DeepSeek-Conc", BaseURL: mockLLM.URL, ModelName: "ds-conc",
		APIKey: "sk-conc", APIKeyMasked: "sk-****", IsActive: true, IsDefault: true,
		TimeoutSeconds: 30, MaxTokensPerTask: 500000, MaxFilesPerTask: 2000,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 同时发起 2 个 OpenCode 任务（system-architecture + ops-and-config）
	body, _ := json.Marshal(map[string]any{
		"repo_id":   env.repo.ID,
		"doc_types": []string{"system-architecture", "ops-and-config"},
		"engine":    "opencode",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+env.ownerTok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("提交双任务失败: %d %s", rec.Code, rec.Body.String())
	}

	select {
	case <-runner.startedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("等待首个 OpenCode 任务进入执行超时")
	}

	// 给第二个任务足够时间尝试竞争，验证 maxConcurrency=1 时不会同时有 2 个进执行态
	time.Sleep(150 * time.Millisecond)
	if got := runner.maxObserved.Load(); got != 1 {
		close(runner.releaseCh)
		t.Fatalf("OpenCode MaxConcurrency=1 约束被突破: maxObserved=%d", got)
	}

	// 在 OpenCode 持工作树读锁期间发起独占写锁，确认写锁互斥阻塞直至读锁释放
	var writeAcquired atomic.Bool
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_ = env.snapSvc.WithRepoWriteLock(env.repo.ID, func() error {
			writeAcquired.Store(true)
			return nil
		})
	}()
	time.Sleep(80 * time.Millisecond)
	if writeAcquired.Load() {
		close(runner.releaseCh)
		t.Fatal("工作树读锁尚未释放前不应获取到独占写锁")
	}

	close(runner.releaseCh)
	env.genSvc.WaitAll()
	select {
	case <-writeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("读锁释放后等待独占写锁完成超时")
	}
	if !writeAcquired.Load() {
		t.Fatal("读锁释放后应成功获取独占写锁")
	}

	var submitResp services.GenerateTaskSubmitResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &submitResp)
	for _, tv := range submitResp.Tasks {
		d, _ := env.genSvc.GetTaskDetail(context.Background(), tv.ID)
		if d.Status != models.TaskStatusReady {
			t.Fatalf("并发限流任务 #%d 应全部完成 ready，实际 %s (%s)", tv.ID, d.Status, d.ErrorMessage)
		}
	}
}
