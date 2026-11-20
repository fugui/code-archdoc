package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/doctype"
	"code-archdoc/pkg/llm"
)

func TestResolveSandboxScopeAndExecuteSlotLLMRouting(t *testing.T) {
	sandboxRoot := t.TempDir()
	repoDir := filepath.Join(sandboxRoot, "repo_7")
	subDir := filepath.Join(repoDir, "svc", "order")
	if err := os.MkdirAll(subDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "pay.cpp"), []byte("int Pay() { return CheckLimit(); }\n"), 0640); err != nil {
		t.Fatal(err)
	}

	snapSvc := NewSnapshotService(nil, nil, nil, nil, sandboxRoot, 1048576)

	// 1. 合法仓库与 Monorepo 子目录解析
	gotRepo, gotScope, ok := snapSvc.ResolveSandboxScope(7, "svc/order")
	if !ok || gotRepo == "" || !strings.HasSuffix(gotScope, filepath.Join("repo_7", "svc", "order")) {
		t.Fatalf("ResolveSandboxScope 合法子目录失败: repo=%q scope=%q ok=%v", gotRepo, gotScope, ok)
	}
	// 非法越界子目录必须被拒绝
	if _, _, badOK := snapSvc.ResolveSandboxScope(7, "../../etc"); badOK {
		t.Fatal("ResolveSandboxScope 必须拒绝目录穿越子目录")
	}

	// 2. 验证 GenerationOrchestrator.executeSlotLLM 分级路由：
	//    - EnableSourceTools=false 槽位不发送 tools（保持零开销单轮）
	//    - EnableSourceTools=true 槽位下发受限只读 tools 并完成多轮源码核实
	var plainSawTools atomic.Bool
	var coreTurn atomic.Int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Messages []llm.Message        `json:"messages"`
			Tools    []llm.ToolDefinition `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		w.Header().Set("Content-Type", "application/json")

		isCoreSlot := len(reqBody.Messages) > 1 && strings.Contains(reqBody.Messages[1].Content, "core_request_flows")
		if !isCoreSlot {
			if len(reqBody.Tools) > 0 {
				plainSawTools.Store(true)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model":   "mock-fast",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"markdown":"目录概览","cited_anchors":[]}`}}},
				"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
			})
			return
		}

		n := coreTurn.Add(1)
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "mock-flagship",
				"choices": []map[string]any{{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{{
							"id":   "tc_pay",
							"type": "function",
							"function": map[string]any{
								"name":      llm.ToolNameReadFileLines,
								"arguments": `{"file_path":"pay.cpp","start_line":1,"end_line":10}`,
							},
						}},
					},
				}},
				"usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 25},
			})
			return
		}

		// 验证第 2 轮已带回 pay.cpp 实际源码内容
		sawPayBody := false
		for _, m := range reqBody.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "CheckLimit()") {
				sawPayBody = true
			}
		}
		if !sawPayBody {
			http.Error(w, "missing tool result CheckLimit()", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "mock-flagship",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"markdown":"Pay 调用 CheckLimit 校验限额","cited_anchors":[]}`}}},
			"usage":   map[string]any{"prompt_tokens": 350, "completion_tokens": 45},
		})
	}))
	defer mockLLM.Close()

	orch := &GenerationOrchestrator{
		snapSvc:   snapSvc,
		llmClient: llm.NewClientWithOptions(mockLLM.Client(), 10*time.Millisecond),
		leaseMgr:  NewActiveLeaseManager(),
	}
	asset := &doctype.AssetBundle{Slug: "system-architecture"}
	ep := llm.EndpointConfig{BaseURL: mockLLM.URL, ModelName: "mock-flagship"}
	ir := &codeir.CodeIRBundle{SchemaVersion: codeir.SchemaVersion}

	// 普通槽位：不开启工具
	respPlain, err := orch.executeSlotLLM(context.Background(), slotExecInput{
		RepoID:       7,
		Subdirectory: "svc/order",
		DocType:      asset,
		Slot:         doctype.SlotSpec{SlotID: "overview_boundaries", Title: "1. 概览", Ownership: "auto", EnableSourceTools: false},
		IR:           ir,
		Endpoint:     ep,
	})
	if err != nil {
		t.Fatalf("普通槽位调用失败: %v", err)
	}
	if plainSawTools.Load() || !strings.Contains(respPlain.Content, "目录概览") {
		t.Fatalf("普通槽位不应携带 tools: plainSawTools=%v content=%q", plainSawTools.Load(), respPlain.Content)
	}

	// 核心流程槽位：开启受限只读工具并自动读取 pay.cpp 源码
	respCore, err := orch.executeSlotLLM(context.Background(), slotExecInput{
		RepoID:       7,
		Subdirectory: "svc/order",
		DocType:      asset,
		Slot:         doctype.SlotSpec{SlotID: "core_request_flows", Title: "3. 核心链路", Ownership: "hybrid", EnableSourceTools: true, MaxToolRounds: 4},
		IR:           ir,
		Endpoint:     ep,
	})
	if err != nil {
		t.Fatalf("核心流程槽位 Tool-Calling 失败: %v", err)
	}
	if coreTurn.Load() != 2 || !strings.Contains(respCore.Content, "CheckLimit") {
		t.Fatalf("核心流程槽位未完成两轮源码核实: turns=%d content=%q", coreTurn.Load(), respCore.Content)
	}
	if respCore.Usage.PromptTokens != 550 || respCore.Usage.CompletionTokens != 70 {
		t.Fatalf("核心流程槽位多轮 Usage 累加异常: %+v", respCore.Usage)
	}
}
