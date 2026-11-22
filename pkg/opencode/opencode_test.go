package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func TestIsOpenCodeV2VersionParsing(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{version: "1.18.21", want: false},
		{version: "v1.99.0", want: false},
		{version: "", want: false},
		{version: "dev", want: false},
		{version: "2.0.0", want: true},
		{version: "v2.3.2", want: true},
		{version: "2.3.2-beta.1", want: true},
		{version: "3.1.0", want: true},
	}
	for _, tc := range cases {
		if got := isOpenCodeV2(tc.version); got != tc.want {
			t.Errorf("isOpenCodeV2(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestOpenCodeV2CLIArgsConfigBridgePermissionsAndContinuation(t *testing.T) {
	workDir := t.TempDir()
	binDir := t.TempDir()
	captureDir := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "calls.txt")

	secretKey := "sk-v2-secret-key-123456"
	fakeBin := writeFakeOpenCodeScript(t, binDir, fmt.Sprintf(`
if [[ "${1:-}" == "--version" ]]; then
  echo "opencode v2.3.2"
  exit 0
fi
echo "call" >> %q
COUNT=$(wc -l < %q)
printf '%%s\0' "$@" > %q/argv_${COUNT}.nul
env > %q/env_${COUNT}.txt
cp "$OPENCODE_CONFIG" %q/config_${COUNT}.json
if [[ "$COUNT" -eq 1 ]]; then
  printf '{"type":"step_start","sessionID":"ses_v2_1"}\n'
  printf '%%s\n' '{"type":"text","sessionID":"ses_v2_1","part":{"type":"text","messageID":"m1","text":"{\"slots\":{\"slot_a\":{\"markdown\":\"A正文\",\"cited_anchors\":[]}}}"}}'
  printf '{"type":"step_finish","sessionID":"ses_v2_1","part":{"tokens":{"input":100,"output":20}}}\n'
  exit 0
fi
if [[ "$COUNT" -eq 2 ]]; then
  printf '{"type":"step_start","sessionID":"ses_v2_1"}\n'
  printf '%%s\n' '{"type":"text","sessionID":"ses_v2_1","part":{"type":"text","messageID":"m2","text":"{\"slots\":{\"slot_b\":{\"markdown\":\"B续跑补齐\",\"cited_anchors\":[]}}}"}}'
  printf '{"type":"step_finish","sessionID":"ses_v2_1","part":{"tokens":{"input":50,"output":15}}}\n'
  exit 0
fi
printf '{"type":"step_start","sessionID":"ses_v2_extra"}\n'
printf '%%s\n' '{"type":"text","sessionID":"ses_v2_extra","part":{"type":"text","messageID":"m3","text":"{\"slots\":{\"slot_a\":{\"markdown\":\"额外调用ok\",\"cited_anchors\":[]}}}"}}'
printf '{"type":"step_finish","sessionID":"ses_v2_extra","part":{"tokens":{"input":30,"output":10}}}\n'
`, stateFile, stateFile, captureDir, captureDir, captureDir))

	runner := NewRunner(Config{
		Enabled:                true,
		BinaryPath:             fakeBin,
		TimeoutSeconds:         120,
		IdleTimeoutSeconds:     30,
		ContinuationEnabled:    true,
		ContinuationMaxSeconds: 90,
	})

	// 1. Mode A + v2 参数/环境变量/有序权限矩阵/断点续跑验证
	resA, err := runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:            workDir,
		RepoRoot:           workDir,
		DocTypeSlug:        "system-architecture",
		DocTypeDisplayName: "系统架构",
		ActiveSlots: []SlotTaskSpec{
			{SlotID: "slot_a", Title: "A", Ownership: "auto"},
			{SlotID: "slot_b", Title: "B", Ownership: "auto"},
		},
		ModelName:           "glm-5.3-flash",
		BaseURL:             "http://127.0.0.1:8000/v1",
		APIKey:              secretKey,
		UserAgent:           "opencode/2.3.2",
		ContinuationEnabled: true,
	})
	if err != nil {
		t.Fatalf("v2 Mode A 执行失败: %v", err)
	}
	if resA.Continuations != 1 || len(resA.MissingSlotIDs) != 0 ||
		resA.Slots["slot_a"].Markdown != "A正文" || resA.Slots["slot_b"].Markdown != "B续跑补齐" {
		t.Fatalf("v2 Mode A 续跑合并结果异常: %+v", resA)
	}

	argv1Bytes, _ := os.ReadFile(filepath.Join(captureDir, "argv_1.nul"))
	argv1Lines := strings.Split(strings.TrimSuffix(string(argv1Bytes), "\x00"), "\x00")
	argv1Str := strings.Join(argv1Lines, "\n")
	if strings.Contains(argv1Str, "--pure") || strings.Contains(argv1Str, "--dir") {
		t.Fatalf("v2 参数绝不能包含 --pure 或 --dir: %s", argv1Str)
	}
	if strings.Contains(argv1Str, secretKey) {
		t.Fatalf("安全违规：明文 APIKey 出现在 argv 中: %s", argv1Str)
	}
	for _, pair := range []string{
		"--standalone",
		"--archived",
		"--title\narchdoc-system-architecture",
		"--format\njson",
		"--agent\n" + ReadOnlyAgentName,
		"--model\narchdoc_ep/glm-5.3-flash",
		"--\n# Code-Archdoc",
	} {
		if !strings.Contains(argv1Str, pair) {
			t.Fatalf("v2 首轮启动参数缺失 %q: %s", pair, argv1Str)
		}
	}
	sanitized := SanitizeArgsForLog(argv1Lines, secretKey)
	if !strings.Contains(sanitized, "-- <prompt:") || strings.Contains(sanitized, secretKey) {
		t.Fatalf("SanitizeArgsForLog 未正确脱敏 v2 `-- <prompt>`: %s", sanitized)
	}

	env1Bytes, _ := os.ReadFile(filepath.Join(captureDir, "env_1.txt"))
	env1Str := string(env1Bytes)
	if strings.Contains(env1Str, secretKey) {
		t.Fatal("安全违规：明文 APIKey 出现在环境变量中")
	}
	for _, wantEnv := range []string{
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_CONFIG_PROJECT_DISABLE=1",
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
		"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_FILEWATCHER_DISABLE=1",
		"OPENCODE_DISABLE_FILEWATCHER=1",
		"PWD=" + workDir,
	} {
		if !strings.Contains(env1Str, wantEnv) {
			t.Fatalf("v2 隔离环境变量缺失 %s", wantEnv)
		}
	}

	cfg1Bytes, _ := os.ReadFile(filepath.Join(captureDir, "config_1.json"))
	var cfg1 map[string]any
	if err := json.Unmarshal(cfg1Bytes, &cfg1); err != nil {
		t.Fatalf("解析 v2 opencode.json 失败: %v", err)
	}
	if cfg1["update"] != "disable" {
		t.Fatalf("期望 v2 config update=disable，实际 %v", cfg1["update"])
	}
	if _, hasOldAgent := cfg1["agent"]; hasOldAgent {
		t.Fatal("v2 配置不应写入旧版 agent 键")
	}
	if _, hasOldProvider := cfg1["provider"]; hasOldProvider {
		t.Fatal("v2 配置不应写入旧版 provider 键")
	}
	provs := cfg1["providers"].(map[string]any)
	ep := provs["archdoc_ep"].(map[string]any)
	if ep["package"] != "aisdk:@ai-sdk/openai-compatible" {
		t.Fatalf("v2 provider package 异常: %v", ep["package"])
	}
	settings := ep["settings"].(map[string]any)
	if settings["baseURL"] != "http://127.0.0.1:8000/v1" || settings["apiKey"] != secretKey {
		t.Fatalf("v2 provider settings 异常: %+v", settings)
	}
	headers := ep["headers"].(map[string]any)
	if headers["User-Agent"] != "opencode/2.3.2" {
		t.Fatalf("v2 provider headers 异常: %+v", headers)
	}
	modelsMap := ep["models"].(map[string]any)
	if _, ok := modelsMap["glm-5.3-flash"]; !ok {
		t.Fatalf("v2 provider models 缺失 glm-5.3-flash: %+v", modelsMap)
	}

	// 验证有序 permissions 数组（v2 findLast 匹配规则：*:deny 必须在首位，allow 必须在后，*.env 覆盖必须在 read:* 之后）
	agentsMap := cfg1["agents"].(map[string]any)
	roAgent := agentsMap[ReadOnlyAgentName].(map[string]any)
	if roAgent["mode"] != "primary" {
		t.Fatalf("v2 agent mode 期望 primary，实际 %v", roAgent["mode"])
	}
	rawPerms, ok := roAgent["permissions"].([]any)
	if !ok || len(rawPerms) == 0 {
		t.Fatalf("v2 permissions 必须为非空数组: %+v", roAgent["permissions"])
	}
	type permRule struct{ action, resource, effect string }
	var perms []permRule
	for _, item := range rawPerms {
		m := item.(map[string]any)
		perms = append(perms, permRule{
			action:   m["action"].(string),
			resource: m["resource"].(string),
			effect:   m["effect"].(string),
		})
	}
	if perms[0] != (permRule{"*", "*", "deny"}) {
		t.Fatalf("v2 permissions 第 0 条必须为 *:deny，实际 %+v", perms[0])
	}
	findRuleIdx := func(action, resource, effect string) int {
		for i, r := range perms {
			if r.action == action && r.resource == resource && r.effect == effect {
				return i
			}
		}
		return -1
	}
	readAllowIdx := findRuleIdx("read", "*", "allow")
	if readAllowIdx <= 0 {
		t.Fatalf("缺失 read:*:allow 或索引异常: %d", readAllowIdx)
	}
	for _, act := range []string{"grep", "glob", "list"} {
		if idx := findRuleIdx(act, "*", "allow"); idx <= 0 {
			t.Fatalf("只读动作 %s:*:allow 索引必须 > 0，实际 %d", act, idx)
		}
	}
	envDenyIdx := findRuleIdx("read", "*.env", "deny")
	envWildDenyIdx := findRuleIdx("read", "*.env.*", "deny")
	envExampleAllowIdx := findRuleIdx("read", "*.env.example", "allow")
	if envDenyIdx <= readAllowIdx || envWildDenyIdx <= readAllowIdx || envExampleAllowIdx <= envWildDenyIdx {
		t.Fatalf(".env 规则顺序异常: read=%d env=%d env.*=%d env.example=%d",
			readAllowIdx, envDenyIdx, envWildDenyIdx, envExampleAllowIdx)
	}
	for _, deniedAct := range []string{"edit", "shell", "webfetch", "websearch", "subagent", "skill", "browser", "question", "external_directory"} {
		if idx := findRuleIdx(deniedAct, "*", "deny"); idx <= 0 {
			t.Fatalf("禁用动作 %s:*:deny 缺失或索引异常: %d", deniedAct, idx)
		}
	}

	// 验证续跑（call 2）携带 --standalone 与 --session ses_v2_1，且不携带 --pure/--dir/--archived/--title
	argv2Bytes, _ := os.ReadFile(filepath.Join(captureDir, "argv_2.nul"))
	argv2Lines := strings.Split(strings.TrimSuffix(string(argv2Bytes), "\x00"), "\x00")
	argv2Str := strings.Join(argv2Lines, "\n")
	if !strings.Contains(argv2Str, "--standalone") || !strings.Contains(argv2Str, "--session\nses_v2_1") {
		t.Fatalf("v2 续跑参数缺失 --standalone 或 --session ses_v2_1: %s", argv2Str)
	}
	for _, forbidden := range []string{"--pure", "--dir", "--archived", "--title"} {
		if strings.Contains(argv2Str, forbidden) {
			t.Fatalf("v2 续跑参数不应包含 %s: %s", forbidden, argv2Str)
		}
	}

	// 2. Mode B：仅精准桥接目标 provider，不泄露宿主机其他 provider/agents/plugins
	t.Run("ModeBHostProviderIsolation", func(t *testing.T) {
		fakeHostXDG := t.TempDir()
		hostOCDir := filepath.Join(fakeHostXDG, "opencode")
		if err := os.MkdirAll(hostOCDir, 0o700); err != nil {
			t.Fatal(err)
		}
		hostCfg := map[string]any{
			"providers": map[string]any{
				"zhipuai-coding-plan": map[string]any{
					"settings": map[string]any{"apiKey": "host-zhipu-key"},
				},
				"other-provider": map[string]any{
					"settings": map[string]any{"apiKey": "host-other-key"},
				},
			},
			"agents": map[string]any{
				"malicious-custom-agent": map[string]any{"mode": "primary"},
			},
			"plugins": []string{"evil-plugin"},
		}
		hostCfgBytes, _ := json.Marshal(hostCfg)
		if err := os.WriteFile(filepath.Join(hostOCDir, "opencode.json"), hostCfgBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", fakeHostXDG)

		_, err := runner.GenerateDocument(context.Background(), DocGenRequest{
			WorkDir:               workDir,
			RepoRoot:              workDir,
			DocTypeSlug:           "system-architecture",
			ActiveSlots:           []SlotTaskSpec{{SlotID: "slot_a", Title: "A", Ownership: "auto"}},
			OpenCodeModelOverride: "zhipuai-coding-plan/glm-5.3-flash",
		})
		if err != nil {
			t.Fatalf("v2 Mode B 执行失败: %v", err)
		}

		argv3Bytes, _ := os.ReadFile(filepath.Join(captureDir, "argv_3.nul"))
		argv3Lines := strings.Split(strings.TrimSuffix(string(argv3Bytes), "\x00"), "\x00")
		argv3Str := strings.Join(argv3Lines, "\n")
		if !strings.Contains(argv3Str, "--model\nzhipuai-coding-plan/glm-5.3-flash") ||
			strings.Contains(argv3Str, "--pure") || strings.Contains(argv3Str, "--dir") {
			t.Fatalf("v2 Mode B 参数异常: %s", argv3Str)
		}

		cfg3Bytes, _ := os.ReadFile(filepath.Join(captureDir, "config_3.json"))
		var cfg3 map[string]any
		if err := json.Unmarshal(cfg3Bytes, &cfg3); err != nil {
			t.Fatalf("解析 v2 Mode B 配置失败: %v", err)
		}
		provs3 := cfg3["providers"].(map[string]any)
		if _, ok := provs3["zhipuai-coding-plan"]; !ok {
			t.Fatalf("v2 Mode B 未桥接目标 provider zhipuai-coding-plan: %+v", provs3)
		}
		if _, leaked := provs3["other-provider"]; leaked {
			t.Fatalf("v2 Mode B 不应拷贝无关 provider: %+v", provs3)
		}
		agents3 := cfg3["agents"].(map[string]any)
		if len(agents3) != 1 || agents3[ReadOnlyAgentName] == nil {
			t.Fatalf("v2 Mode B 绝不能拷贝宿主机 agents: %+v", agents3)
		}
		if _, hasPlugins := cfg3["plugins"]; hasPlugins {
			t.Fatalf("v2 Mode B 绝不能拷贝宿主机 plugins: %+v", cfg3["plugins"])
		}
	})

	// 3. 大 Prompt (>64KB) 走 --file 且位于 -- 之前
	hugeHint := strings.Repeat("架构调用链路分析上下文-", 3000)
	_, err = runner.GenerateDocument(context.Background(), DocGenRequest{
		WorkDir:     workDir,
		RepoRoot:    workDir,
		DocTypeSlug: "code-walkthrough",
		ActiveSlots: []SlotTaskSpec{{SlotID: "slot_a", Title: "A", Ownership: "auto", InstructionHint: hugeHint}},
		ModelName:   "glm-5.3-flash",
		BaseURL:     "http://127.0.0.1:8000/v1",
	})
	if err != nil {
		t.Fatalf("v2 大 Prompt 执行失败: %v", err)
	}
	argv4Bytes, _ := os.ReadFile(filepath.Join(captureDir, "argv_4.nul"))
	argv4Lines := strings.Split(strings.TrimSuffix(string(argv4Bytes), "\x00"), "\x00")
	argv4Str := strings.Join(argv4Lines, "\n")
	fileIdx := strings.Index(argv4Str, "--file\n")
	sepIdx := strings.Index(argv4Str, "\n--\n")
	if fileIdx < 0 || sepIdx < 0 || fileIdx >= sepIdx || strings.Contains(argv4Str, "架构调用链路分析上下文") {
		t.Fatalf("v2 大 Prompt 应走 --file 且位于 -- 之前: %s", argv4Str)
	}
}

func TestOpenCodeRealV2EndToEndWithMockServer(t *testing.T) {
	if os.Getenv("OPENCODE_REAL_E2E") != "1" {
		t.Skip("设置 OPENCODE_REAL_E2E=1 以运行真实 OpenCode v2 CLI 端到端测试")
	}

	probeRunner := NewRunner(Config{Enabled: true})
	probe := probeRunner.Probe(context.Background())
	if !probe.Available || !isOpenCodeV2(probe.Version) {
		t.Fatalf("期望本机存在可用的 OpenCode v2+ CLI，实际 probe=%+v", probe)
	}

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "hello.txt"), []byte("SECRET_SANDBOX_TOKEN_42\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		mu          sync.Mutex
		calls       atomic.Int32
		toolResults []string
		sawAuth     bool
		sawUA       bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var reqBody struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		mu.Lock()
		if r.Header.Get("Authorization") == "Bearer sk-real-e2e-test" {
			sawAuth = true
		}
		if ua := r.Header.Get("User-Agent"); ua == "Code-Archdoc-E2E/1.0" || strings.HasPrefix(ua, "opencode/") {
			sawUA = true
		}
		for _, m := range reqBody.Messages {
			if m.Role == "tool" {
				b, _ := json.Marshal(m.Content)
				toolResults = append(toolResults, string(b))
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		writeChunk := func(payload any) {
			b, _ := json.Marshal(payload)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		}

		switch n {
		case 1:
			writeChunk(map[string]any{
				"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{
								"index": 0, "id": "tc_local", "type": "function",
								"function": map[string]any{"name": "read", "arguments": `{"path":"hello.txt"}`},
							},
							{
								"index": 1, "id": "tc_ext", "type": "function",
								"function": map[string]any{"name": "read", "arguments": `{"path":"/etc/hostname"}`},
							},
						},
					},
					"finish_reason": nil,
				}},
			})
			writeChunk(map[string]any{
				"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
				"usage":   map[string]any{"prompt_tokens": 90, "completion_tokens": 20, "total_tokens": 110},
			})
		case 2:
			text := `{"slots":{"slot_a":{"markdown":"### 核心概览\n已核实 hello.txt","cited_anchors":[{"file_path":"hello.txt"}]}}}`
			writeChunk(map[string]any{
				"id": "c2", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil,
				}},
			})
			writeChunk(map[string]any{
				"id": "c2", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage": map[string]any{
					"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150,
					"prompt_tokens_details": map[string]any{"cached_tokens": 40},
				},
			})
		default:
			text := `{"slots":{"slot_b":{"markdown":"### 补齐章节\n续跑补齐内容","cited_anchors":[]}}}`
			writeChunk(map[string]any{
				"id": "c3", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{
					"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil,
				}},
			})
			writeChunk(map[string]any{
				"id": "c3", "object": "chat.completion.chunk", "created": 1, "model": reqBody.Model,
				"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 80, "completion_tokens": 25, "total_tokens": 105},
			})
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	runner := NewRunner(Config{
		Enabled:                true,
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
			{SlotID: "slot_a", Title: "概览", Ownership: "auto"},
			{SlotID: "slot_b", Title: "补齐", Ownership: "auto"},
		},
		ModelName:           "glm-5.3-flash",
		BaseURL:             srv.URL + "/v1",
		APIKey:              "sk-real-e2e-test",
		UserAgent:           "Code-Archdoc-E2E/1.0",
		ContinuationEnabled: true,
	})
	if err != nil {
		t.Fatalf("真实 OpenCode v2 E2E 执行失败: %v", err)
	}

	if res.Continuations != 1 || len(res.MissingSlotIDs) != 0 || res.SessionID == "" {
		t.Fatalf("真实 OpenCode v2 续跑结果异常: %+v", res)
	}
	if !strings.Contains(res.Slots["slot_a"].Markdown, "已核实 hello.txt") ||
		len(res.Slots["slot_a"].CitedAnchors) != 1 ||
		res.Slots["slot_a"].CitedAnchors[0].FilePath != "hello.txt" ||
		!strings.Contains(res.Slots["slot_b"].Markdown, "续跑补齐内容") {
		t.Fatalf("真实 OpenCode v2 解析槽位内容异常: %+v", res.Slots)
	}
	if res.InputTokens == 0 || res.OutputTokens == 0 || res.CachedInputTokens > res.InputTokens {
		t.Fatalf("真实 OpenCode v2 Usage 归一化异常: %+v", res)
	}

	mu.Lock()
	defer mu.Unlock()
	joinedTools := strings.Join(toolResults, "\n")
	if !sawAuth || !sawUA {
		t.Fatalf("桥接认证头或 User-Agent 未透传: sawAuth=%v sawUA=%v", sawAuth, sawUA)
	}
	if !strings.Contains(joinedTools, "SECRET_SANDBOX_TOKEN_42") {
		t.Fatalf("沙箱内 read hello.txt 未成功放行: %s", joinedTools)
	}
	if !strings.Contains(joinedTools, "external_directory") {
		t.Fatalf("越界读取 /etc/hostname 未被 external_directory 拦截: %s", joinedTools)
	}
}
