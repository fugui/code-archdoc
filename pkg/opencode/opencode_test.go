package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-archdoc/pkg/codeir"
)

func writeFakeOpenCodeScript(t *testing.T, dir, body string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, "opencode")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+body), 0o755); err != nil {
		t.Fatalf("写入 Fake opencode 脚本失败: %v", err)
	}
	return scriptPath
}

// T-ADR08-06：双模式配置桥接（Mode A 0600 临时配置 + 零凭据泄漏；Mode B --model 透传 + 全套 XDG 隔离）
func TestDualModeConfigBridgeAndZeroCredentialLeak(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	captureDir := t.TempDir()

	secretKey := "sk-super-secret-opencode-key-987654321"
	fakeBin := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "opencode 1.18.21"
  exit 0
fi
printf '%%s\n' "$@" > %q/argv.txt
env > %q/env.txt
cp "$OPENCODE_CONFIG" %q/captured_opencode.json
ls -ld "$(dirname "$OPENCODE_CONFIG")" > %q/dir_perm.txt
ls -l "$OPENCODE_CONFIG" > %q/file_perm.txt
printf '{"type":"step_start","sessionID":"ses_mode_ab"}\n'
printf '%%s\n' '{"type":"text","sessionID":"ses_mode_ab","part":{"type":"text","messageID":"msg_1","text":"{\"slots\":{\"overview\":{\"markdown\":\"架构正文\",\"cited_anchors\":[]}}}"}}'
printf '{"type":"step_finish","sessionID":"ses_mode_ab","part":{"tokens":{"input":120,"output":45,"cache":{"read":80}}}}\n'
`, captureDir, captureDir, captureDir, captureDir, captureDir))

	runner := NewRunner(Config{
		Enabled:             true,
		BinaryPath:          fakeBin,
		TimeoutSeconds:      30,
		IdleTimeoutSeconds:  10,
		ContinuationEnabled: true,
	})

	probe := runner.Probe(context.Background())
	if !probe.Available || probe.Version != "1.18.21" {
		t.Fatalf("Probe 期望 available=true version=1.18.21，实际 %+v", probe)
	}

	// 1. 模式 A：留空 OpenCodeModelOverride，自动生成 0700/0600 临时配置并桥接 @ai-sdk/openai-compatible
	resA, err := runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:            workDir,
		RepoRoot:           workDir,
		DocTypeSlug:        "system-architecture",
		DocTypeDisplayName: "系统架构",
		ActiveSlots:        []SlotTaskSpec{{SlotID: "overview", Title: "系统概览", Ownership: "auto"}},
		ModelName:          "deepseek-v3",
		BaseURL:            "http://127.0.0.1:8000/v1",
		APIKey:             secretKey,
		UserAgent:          "Code-Archdoc/1.0",
	})
	if err != nil {
		t.Fatalf("模式 A 执行失败: %v", err)
	}
	if resA.Slots["overview"].Markdown != "架构正文" || resA.CachedInputTokens != 80 || resA.InputTokens != 120 {
		t.Fatalf("模式 A 解析结果异常: %+v", resA)
	}

	argvBytes, _ := os.ReadFile(filepath.Join(captureDir, "argv.txt"))
	envBytes, _ := os.ReadFile(filepath.Join(captureDir, "env.txt"))
	cfgBytes, _ := os.ReadFile(filepath.Join(captureDir, "captured_opencode.json"))
	if strings.Contains(string(argvBytes), secretKey) || strings.Contains(string(envBytes), secretKey) {
		t.Fatalf("安全违规：明文 APIKey 严禁出现在 argv 或环境变量中！\nargv=%s\nenv=%s", string(argvBytes), string(envBytes))
	}
	if !strings.Contains(string(argvBytes), "--model\narchdoc_ep/deepseek-v3") || !strings.Contains(string(argvBytes), "--pure") {
		t.Fatalf("模式 A 启动参数缺失 --model archdoc_ep/deepseek-v3 或 --pure: %s", string(argvBytes))
	}
	if strings.Contains(SanitizeArgsForLog(strings.Split(strings.TrimSpace(string(argvBytes)), "\n"), secretKey), secretKey) {
		t.Fatal("SanitizeArgsForLog 未脱敏")
	}

	var capturedCfg map[string]any
	if err := json.Unmarshal(cfgBytes, &capturedCfg); err != nil {
		t.Fatalf("解析捕获 opencode.json 失败: %v", err)
	}
	provMap, _ := capturedCfg["provider"].(map[string]any)
	epMap, _ := provMap["archdoc_ep"].(map[string]any)
	if epMap["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatalf("模式 A 缺少 @ai-sdk/openai-compatible: %+v", capturedCfg)
	}

	// 验证隔离目录已销毁，XDG_* 与 CI/NO_COLOR/TERM 全部覆写
	var recordedCfgPath string
	for _, line := range strings.Split(string(envBytes), "\n") {
		if strings.HasPrefix(line, "OPENCODE_CONFIG=") {
			recordedCfgPath = strings.TrimPrefix(line, "OPENCODE_CONFIG=")
		}
	}
	if recordedCfgPath == "" {
		t.Fatal("未设置 OPENCODE_CONFIG")
	}
	if _, statErr := os.Stat(filepath.Dir(recordedCfgPath)); !os.IsNotExist(statErr) {
		t.Fatalf("调用结束后临时隔离目录 %s 必须彻底销毁", filepath.Dir(recordedCfgPath))
	}
	for _, wantEnv := range []string{"XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "XDG_STATE_HOME=", "XDG_CACHE_HOME=", "CI=true", "NO_COLOR=1", "TERM=dumb"} {
		if !strings.Contains(string(envBytes), wantEnv) {
			t.Fatalf("隔离环境变量缺失 %s", wantEnv)
		}
	}
	dirPerm, _ := os.ReadFile(filepath.Join(captureDir, "dir_perm.txt"))
	filePerm, _ := os.ReadFile(filepath.Join(captureDir, "file_perm.txt"))
	if !strings.HasPrefix(string(dirPerm), "drwx------") || !strings.HasPrefix(string(filePerm), "-rw-------") {
		t.Fatalf("隔离目录/配置文件权限不满足 0700/0600: dir=%s file=%s", string(dirPerm), string(filePerm))
	}

	// 2. 模式 B：显式配置 OpenCodeModelOverride 直接传 --model <provider/model>
	_, err = runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:               workDir,
		RepoRoot:              workDir,
		DocTypeSlug:           "system-architecture",
		DocTypeDisplayName:    "系统架构",
		ActiveSlots:           []SlotTaskSpec{{SlotID: "overview", Title: "系统概览", Ownership: "auto"}},
		ModelName:             "ignored",
		BaseURL:               "http://127.0.0.1:8000/v1",
		OpenCodeModelOverride: "deepseek/deepseek-reasoner",
	})
	if err != nil {
		t.Fatalf("模式 B 执行失败: %v", err)
	}
	argvB, _ := os.ReadFile(filepath.Join(captureDir, "argv.txt"))
	if !strings.Contains(string(argvB), "--model\ndeepseek/deepseek-reasoner") {
		t.Fatalf("模式 B 未传入 --model deepseek/deepseek-reasoner: %s", string(argvB))
	}
}

// T-ADR08-07：只读权限矩阵（禁写/禁 Shell/禁外网/禁越界）与 >64KB 大 Prompt 走 --file
func TestReadOnlyMatrixAndLargePromptFileTransport(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	captureDir := t.TempDir()

	fakeBin := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "opencode 1.18.21"
  exit 0
fi
printf '%%s\n' "$@" > %q/argv.txt
cp "$OPENCODE_CONFIG" %q/opencode.json
printf '{"type":"step_start","sessionID":"ses_large"}\n'
printf '%%s\n' '{"type":"text","sessionID":"ses_large","part":{"type":"text","messageID":"m1","text":"{\"slots\":{\"s1\":{\"markdown\":\"ok\",\"cited_anchors\":[]}}}"}}'
`, captureDir, captureDir))

	runner := NewRunner(Config{Enabled: true, BinaryPath: fakeBin, TimeoutSeconds: 20, IdleTimeoutSeconds: 10})

	// 构造超过 70 KB 的指令文本触发 --file 落盘传输
	hugeHint := strings.Repeat("架构调用链路分析上下文-", 3000)
	_, err := runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:            workDir,
		RepoRoot:           workDir,
		DocTypeSlug:        "code-walkthrough",
		DocTypeDisplayName: "源码走读",
		ActiveSlots:        []SlotTaskSpec{{SlotID: "s1", Title: "概览", Ownership: "auto", InstructionHint: hugeHint}},
		IR:                 &codeir.CodeIRBundle{SchemaVersion: codeir.SchemaVersion},
		ModelName:          "m",
		BaseURL:            "http://127.0.0.1:8000/v1",
	})
	if err != nil {
		t.Fatalf("大 Prompt --file 调用失败: %v", err)
	}

	argvBytes, _ := os.ReadFile(filepath.Join(captureDir, "argv.txt"))
	argvStr := string(argvBytes)
	if !strings.Contains(argvStr, "--file\n") || strings.Contains(argvStr, "架构调用链路分析上下文") {
		t.Fatalf("超过 64KB Prompt 应自动走 --file 传输而非塞入 argv: len(argv)=%d", len(argvStr))
	}

	cfgBytes, _ := os.ReadFile(filepath.Join(captureDir, "opencode.json"))
	var cfg map[string]any
	_ = json.Unmarshal(cfgBytes, &cfg)
	agentMap := cfg["agent"].(map[string]any)[ReadOnlyAgentName].(map[string]any)
	tools := agentMap["tools"].(map[string]any)
	perm := agentMap["permission"].(map[string]any)
	for _, deniedTool := range []string{"edit", "write", "patch", "bash", "webfetch"} {
		if tools[deniedTool] != false || perm[deniedTool] != "deny" {
			t.Fatalf("只读权限矩阵违规：工具 %s 必须全部禁用，实际 tools=%v perm=%v", deniedTool, tools[deniedTool], perm[deniedTool])
		}
	}
	if perm["*"] != "deny" || perm["external_directory"] != "deny" {
		t.Fatalf("permission 默认拒绝与外目录拒绝策略异常: %+v", perm)
	}
}

// T-ADR08-08：8MB JSONL 超长单行、Usage 归一化（Cached<=Input）、防重 H2 标题剥离与多格式容错解析
func TestJSONL8MBUsageNormalizationHeadingStripAndFlexibleParsing(t *testing.T) {
	longBody := strings.Repeat("深入解析并发状态机与内存模型。", 6000) // >120 KB 单行 JSONL
	innerJSON := fmt.Sprintf(`{"slots":{"arch_overview":{"markdown":"## 1. 系统架构总览\n\n### 子架构\n%s","cited_anchors":[{"file_path":"src/main.cpp"}]}}}`, longBody)
	wrappedLineBytes, _ := json.Marshal(map[string]any{
		"type":      "text",
		"sessionID": "ses_8mb",
		"part": map[string]any{
			"type":      "text",
			"messageID": "m_final",
			"text":      "说明：以下是分析结论：\n```json\n" + innerJSON + "\n```",
		},
	})

	jsonlStream := strings.Join([]string{
		`{"type":"step_start","sessionID":"ses_8mb"}`,
		`{"type":"text","sessionID":"ses_8mb","part":{"type":"text","messageID":"m_thinking","text":"intermediate"}}`,
		string(wrappedLineBytes),
		// 模拟 Provider 分离计量导致 cache.read (900) > input (100)
		`{"type":"step_finish","sessionID":"ses_8mb","part":{"tokens":{"input":100,"output":350,"cache":{"read":900}}}}`,
	}, "\n")

	parsed := ParseEventStream([]byte(jsonlStream))
	if parsed.SessionID != "ses_8mb" {
		t.Fatalf("SessionID 解析错误: %q", parsed.SessionID)
	}
	if parsed.CachedInputTokens != 900 || parsed.InputTokens != 1000 || parsed.CachedInputTokens > parsed.InputTokens {
		t.Fatalf("Usage 归一化异常（要求 Cached<=Input）: input=%d cached=%d", parsed.InputTokens, parsed.CachedInputTokens)
	}

	slots, missing := ParseOpenCodeDocOutput(parsed.FinalText, []SlotTaskSpec{
		{SlotID: "arch_overview", Title: "系统架构总览"},
	})
	if len(missing) != 0 {
		t.Fatalf("期望无缺失槽位，实际 missing=%v", missing)
	}
	md := slots["arch_overview"].Markdown
	if strings.HasPrefix(md, "# ") || strings.HasPrefix(md, "## ") || !strings.HasPrefix(md, "### 子架构") {
		t.Fatalf("未剥离与 slot.Title 重复的 ## 标题: prefix=%q", md[:minIntTest(80, len(md))])
	}

	// 验证顶层直接映射结构（无 outer slots 包装）也可正常解析
	directOut, directMissing := ParseOpenCodeDocOutput(`{"s1":{"markdown":"# 模块概览\n正文细节","cited_anchors":[]}}`, []SlotTaskSpec{
		{SlotID: "s1", Title: "模块概览"},
	})
	if len(directMissing) != 0 || directOut["s1"].Markdown != "正文细节" {
		t.Fatalf("顶层直接槽位映射容错解析失败: out=%+v missing=%v", directOut, directMissing)
	}
}

// T-ADR08-09：--session 断点续跑与缺槽位补齐（至多 1 次续跑）
func TestSessionContinuationRecoversMissingSlots(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "calls.txt")

	fakeBin := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "1.18.21"
  exit 0
fi
echo "call" >> %q
COUNT=$(wc -l < %q)
if [[ "$COUNT" -eq 1 ]]; then
  # 首轮仅返回 slot_a，遗漏 slot_b
  printf '{"type":"step_start","sessionID":"ses_resume_1"}\n'
  printf '%%s\n' '{"type":"text","sessionID":"ses_resume_1","part":{"type":"text","messageID":"m1","text":"{\"slots\":{\"slot_a\":{\"markdown\":\"A正文\",\"cited_anchors\":[]}}}"}}'
  printf '{"type":"step_finish","sessionID":"ses_resume_1","part":{"tokens":{"input":100,"output":20}}}\n'
  exit 0
fi
# 续跑必须携带 --session ses_resume_1
if [[ "$*" != *"--session ses_resume_1"* ]]; then
  echo "missing --session" >&2
  exit 1
fi
printf '%%s\n' '{"type":"text","sessionID":"ses_resume_1","part":{"type":"text","messageID":"m2","text":"{\"slots\":{\"slot_b\":{\"markdown\":\"B补齐正文\",\"cited_anchors\":[]}}}"}}'
printf '{"type":"step_finish","sessionID":"ses_resume_1","part":{"tokens":{"input":50,"output":30}}}\n'
`, stateFile, stateFile))

	runner := NewRunner(Config{
		Enabled:                true,
		BinaryPath:             fakeBin,
		TimeoutSeconds:         120,
		IdleTimeoutSeconds:     30,
		ContinuationEnabled:    true,
		ContinuationMaxSeconds: 90,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := runner.GenerateDocument(ctx, DocGenRequest{
		WorkDir:            workDir,
		RepoRoot:           workDir,
		DocTypeSlug:        "system-architecture",
		DocTypeDisplayName: "系统架构",
		ActiveSlots: []SlotTaskSpec{
			{SlotID: "slot_a", Title: "A", Ownership: "auto"},
			{SlotID: "slot_b", Title: "B", Ownership: "auto"},
		},
		ModelName:           "m",
		BaseURL:             "http://127.0.0.1:8000/v1",
		ContinuationEnabled: true,
	})
	if err != nil {
		t.Fatalf("--session 续跑补齐失败: %v", err)
	}
	if res.Continuations != 1 || len(res.MissingSlotIDs) != 0 ||
		res.Slots["slot_a"].Markdown != "A正文" || res.Slots["slot_b"].Markdown != "B补齐正文" ||
		res.InputTokens != 150 || res.OutputTokens != 50 {
		t.Fatalf("--session 续跑合并结果异常: %+v", res)
	}
}

// T-ADR08-10：LockOSThread + Setpgid 进程组取消与 Idle 看门狗（1 秒内彻底回收孙进程）
func TestProcessGroupKillOnCancelAndIdleWatchdog(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	fakeBin := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "1.18.21"
  exit 0
fi
sleep 60 &
echo $! > %q
printf '{"type":"step_start","sessionID":"ses_hang"}\n'
wait
`, pidFile))

	runner := NewRunner(Config{
		Enabled:             true,
		BinaryPath:          fakeBin,
		TimeoutSeconds:      60,
		IdleTimeoutSeconds:  1,
		ContinuationEnabled: false,
	})

	start := time.Now()
	_, err := runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:            workDir,
		RepoRoot:           workDir,
		DocTypeSlug:        "system-architecture",
		DocTypeDisplayName: "系统架构",
		ActiveSlots:        []SlotTaskSpec{{SlotID: "s1", Title: "S1", Ownership: "auto"}},
		ModelName:          "m",
		BaseURL:            "http://127.0.0.1:8000/v1",
		IdleTimeoutSeconds: 1,
	})
	if ClassifyError(err) != ErrorClassIdleTimeout {
		t.Fatalf("期望命中 ErrorClassIdleTimeout，实际 err=%v class=%s", err, ClassifyError(err))
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Idle 看门狗触发过慢: %v", time.Since(start))
	}

	// 验证孙进程已经被 -pgid SIGKILL 彻底杀死（不存在或仅余待 init 回收的 Z 僵尸态）
	pidBytes, rErr := os.ReadFile(pidFile)
	if rErr != nil {
		t.Fatalf("读取孙进程 PID 文件失败: %v", rErr)
	}
	assertProcessDead(t, strings.TrimSpace(string(pidBytes)))

	// 同步测试父 ctx 取消即时秒杀进程组
	pidFile2 := filepath.Join(t.TempDir(), "grandchild2.pid")
	fakeBin2 := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "1.18.21"
  exit 0
fi
sleep 60 &
echo $! > %q
wait
`, pidFile2))
	runner2 := NewRunner(Config{Enabled: true, BinaryPath: fakeBin2, TimeoutSeconds: 60, IdleTimeoutSeconds: 30})
	ctxCancel, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err2 := runner2.GenerateDocument(ctxCancel, DocGenRequest{
		WorkDir:     workDir,
		RepoRoot:    workDir,
		DocTypeSlug: "system-architecture",
		ActiveSlots: []SlotTaskSpec{{SlotID: "s1", Title: "S1", Ownership: "auto"}},
		ModelName:   "m",
		BaseURL:     "http://127.0.0.1:8000/v1",
	})
	if ClassifyError(err2) != ErrorClassCanceled && ClassifyError(err2) != ErrorClassTimeout {
		t.Fatalf("父 ctx 取消期望 ErrorClassCanceled/Timeout，实际 err=%v", err2)
	}
	pid2Bytes, _ := os.ReadFile(pidFile2)
	pid2 := strings.TrimSpace(string(pid2Bytes))
	if pid2 != "" {
		assertProcessDead(t, pid2)
	}
}

func assertProcessDead(t *testing.T, pid string) {
	t.Helper()
	if pid == "" {
		return
	}
	deadline := time.Now().Add(800 * time.Millisecond)
	for {
		statBytes, err := os.ReadFile("/proc/" + pid + "/stat")
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			if rp := strings.LastIndexByte(string(statBytes), ')'); rp >= 0 && len(statBytes) > rp+2 {
				state := statBytes[rp+2]
				if state == 'Z' || state == 'X' || state == 'x' {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("进程组强杀后孙进程 PID %s 仍存活（stat=%q）", pid, strings.TrimSpace(string(statBytes)))
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func minIntTest(a, b int) int {
	if a < b {
		return a
	}
	return b
}
