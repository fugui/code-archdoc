package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/verifier"
)

// ErrorClass 对 OpenCode 执行失败原因进行结构化分级，用于续跑判定与上层观测。
type ErrorClass string

const (
	ErrorClassNone             ErrorClass = ""
	ErrorClassCanceled         ErrorClass = "canceled"
	ErrorClassTimeout          ErrorClass = "timeout"
	ErrorClassIdleTimeout      ErrorClass = "idle_timeout"
	ErrorClassRateLimited      ErrorClass = "rate_limited"
	ErrorClassAuth             ErrorClass = "auth"
	ErrorClassNetworkTransient ErrorClass = "network_transient"
	ErrorClassContentFiltered  ErrorClass = "content_filtered"
	ErrorClassOutputMissing    ErrorClass = "output_missing"
	ErrorClassCLINotFound      ErrorClass = "cli_not_found"
	ErrorClassUnknown          ErrorClass = "unknown"
)

// ClassifiedError 携带结构化 ErrorClass 的 OpenCode 执行错误。
type ClassifiedError struct {
	Class   ErrorClass
	Message string
	Cause   error
}

func (e *ClassifiedError) Error() string {
	if e.Cause != nil && e.Message != "" {
		return fmt.Sprintf("[%s] %s: %v", e.Class, e.Message, e.Cause)
	}
	if e.Message != "" {
		return fmt.Sprintf("[%s] %s", e.Class, e.Message)
	}
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %v", e.Class, e.Cause)
	}
	return string(e.Class)
}

func (e *ClassifiedError) Unwrap() error { return e.Cause }

// ClassifyError 提取错误对应的 ErrorClass（未知错误默认返回 ErrorClassUnknown）。
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorClassNone
	}
	if errors.Is(err, context.Canceled) {
		return ErrorClassCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassTimeout
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) && ce.Class != "" {
		return ce.Class
	}
	return ErrorClassUnknown
}

// SlotTaskSpec 描述交由 OpenCode 单文档会话生成的单个非锁定槽位契约。
type SlotTaskSpec struct {
	SlotID          string
	Title           string
	Ownership       string
	InstructionHint string
	IRFacets        []string
	PriorHybridMD   string
	ReviewGuidance  []string
}

// DocGenRequest 定义单文档 OpenCode 会话执行的全部确定性输入。
type DocGenRequest struct {
	WorkDir                string
	RepoRoot               string
	Subdirectory           string
	DocTypeSlug            string
	DocTypeDisplayName     string
	TemplateMD             string
	GenerationPromptMD     string
	ActiveSlots            []SlotTaskSpec
	Glossary               *codeir.GlossaryCache
	IR                     *codeir.CodeIRBundle
	ModuleContext          *codeir.ModuleContextFacet
	ModuleDigests          []codeir.ModuleDigestInput
	CrossModuleGraph       []codeir.CrossModuleEdge
	ModelName              string
	BaseURL                string
	APIKey                 string // 仅写入 0600 临时配置文件，绝不进 argv/env/日志
	UserAgent              string
	OpenCodeModelOverride  string
	TimeoutSeconds         int
	IdleTimeoutSeconds     int
	ContinuationEnabled    bool
	ContinuationMaxSeconds int
}

// SlotOutput 表示 OpenCode 返回的单个章节槽位产物。
type SlotOutput struct {
	Markdown     string
	CitedAnchors []verifier.CitedAnchorCandidate
}

// DocGenResult 汇总单文档 OpenCode 会话产物、缺失槽位与归一化 Token 用量。
type DocGenResult struct {
	SessionID         string
	Slots             map[string]SlotOutput
	MissingSlotIDs    []string
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	LatencyMs         int64
	Continuations     int
	TerminationReason string
}

// ProbeStatus 描述 OpenCode CLI 的可用性探活状态。
type ProbeStatus struct {
	Available  bool   `json:"available"`
	BinaryPath string `json:"binary_path,omitempty"`
	Version    string `json:"version,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Config 定义 pkg/opencode 的运行时配置（零导入 config/，由上层注入）。
type Config struct {
	Enabled                bool
	BinaryPath             string
	TimeoutSeconds         int
	IdleTimeoutSeconds     int
	ContinuationEnabled    bool
	ContinuationMaxSeconds int
}

// DocGenerator 抽象 OpenCode 文档生成引擎接口（便于 services 层与单测注入 Mock）。
type DocGenerator interface {
	Probe(ctx context.Context) ProbeStatus
	GenerateDocument(ctx context.Context, req DocGenRequest) (*DocGenResult, error)
}

const (
	defaultProbeTTL               = 30 * time.Second
	defaultOpenCodeTimeoutSec     = 600
	defaultOpenCodeIdleSec        = 120
	defaultOpenCodeContMaxSec     = 180
	minOpenCodeContinuationWindow = 60 * time.Second
)

var reVersionToken = regexp.MustCompile(`v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)`)

// Runner 管理 OpenCode CLI 探活缓存与单文档只读会话执行。
type Runner struct {
	staticCfg  Config
	cfgGetter  func() Config
	probeTTL   time.Duration
	probeMu    sync.Mutex
	cachedAt   time.Time
	cachedBin  string
	cachedEn   bool
	cachedProb ProbeStatus
}

// NewRunner 构造使用静态配置的 OpenCode Runner。
func NewRunner(cfg Config) *Runner {
	return &Runner{
		staticCfg: cfg,
		probeTTL:  defaultProbeTTL,
	}
}

// NewRunnerWithConfigGetter 构造动态读取配置的 OpenCode Runner（每次调用同步最新 Config）。
func NewRunnerWithConfigGetter(getter func() Config) *Runner {
	return &Runner{
		cfgGetter: getter,
		probeTTL:  defaultProbeTTL,
	}
}

// InvalidateProbeCache 清空 30s 探活缓存（供单测或配置热更新调用）。
func (r *Runner) InvalidateProbeCache() {
	r.probeMu.Lock()
	r.cachedAt = time.Time{}
	r.probeMu.Unlock()
}

func (r *Runner) currentConfig() Config {
	if r.cfgGetter != nil {
		return r.cfgGetter()
	}
	return r.staticCfg
}

// Probe 带 30s TTL 缓存探测 OpenCode CLI 是否启用、可执行并提取版本号。
func (r *Runner) Probe(ctx context.Context) ProbeStatus {
	cfg := r.currentConfig()
	if !cfg.Enabled {
		return ProbeStatus{
			Available: false,
			Reason:    "服务端已禁用 OpenCode 引擎",
		}
	}

	binCfg := strings.TrimSpace(cfg.BinaryPath)

	r.probeMu.Lock()
	if !r.cachedAt.IsZero() && time.Since(r.cachedAt) < r.probeTTL && r.cachedEn == cfg.Enabled && r.cachedBin == binCfg {
		st := r.cachedProb
		r.probeMu.Unlock()
		return st
	}
	r.probeMu.Unlock()

	st := probeOpenCodeBinary(ctx, binCfg)

	r.probeMu.Lock()
	r.cachedAt = time.Now()
	r.cachedBin = binCfg
	r.cachedEn = cfg.Enabled
	r.cachedProb = st
	r.probeMu.Unlock()
	return st
}

func probeOpenCodeBinary(ctx context.Context, explicitPath string) ProbeStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	binPath, err := resolveBinaryPath(explicitPath)
	if err != nil {
		return ProbeStatus{
			Available: false,
			Reason:    err.Error(),
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, binPath, "--version")
	cmd.Env = buildProbeEnv()
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return ProbeStatus{
			Available:  false,
			BinaryPath: binPath,
			Reason:     fmt.Sprintf("执行 opencode --version 失败: %v", runErr),
		}
	}

	version := parseOpenCodeVersion(string(out))
	return ProbeStatus{
		Available:  true,
		BinaryPath: binPath,
		Version:    version,
	}
}

func resolveBinaryPath(explicitPath string) (string, error) {
	if explicit := strings.TrimSpace(explicitPath); explicit != "" {
		st, err := os.Stat(explicit)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			return "", fmt.Errorf("显式配置的 opencode 路径 %q 不存在或不可执行", explicit)
		}
		return explicit, nil
	}

	if p, err := exec.LookPath("opencode"); err == nil && p != "" {
		return p, nil
	}

	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".opencode", "bin", "opencode"))
	}
	candidates = append(candidates, "/usr/local/bin/opencode")

	for _, cand := range candidates {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("未在 PATH 及常见安装路径检测到 opencode 可执行文件")
}

func parseOpenCodeVersion(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if m := reVersionToken.FindStringSubmatch(trimmed); len(m) == 2 {
		return m[1]
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		first = strings.TrimPrefix(first, "opencode ")
		first = strings.TrimPrefix(first, "v")
		return strings.TrimSpace(first)
	}
	return ""
}

// isOpenCodeV2 判断探测到的 OpenCode CLI 主版本号是否 >= 2。
// v2.x 移除了 --pure / --dir，并要求 --standalone 与新版 agents.permissions/providers 结构；空串或非数字版本按 v1 处理以保兼容。
func isOpenCodeV2(version string) bool {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return false
	}
	if idx := strings.IndexByte(v, '.'); idx >= 0 {
		v = v[:idx]
	}
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major, err := strconv.Atoi(v[:end])
	return err == nil && major >= 2
}

// GenerateDocument 在受限只读沙箱工作目录内执行单文档 OpenCode 会话（含至多 1 次 --session 断点续跑）。
func (r *Runner) GenerateDocument(ctx context.Context, req DocGenRequest) (*DocGenResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, &ClassifiedError{Class: ErrorClassCanceled, Message: "上下文已取消", Cause: ctx.Err()}
	}
	if len(req.ActiveSlots) == 0 {
		return nil, &ClassifiedError{Class: ErrorClassUnknown, Message: "无待生成的非锁定槽位"}
	}
	if strings.TrimSpace(req.WorkDir) == "" {
		return nil, &ClassifiedError{Class: ErrorClassUnknown, Message: "WorkDir 不能为空"}
	}

	probe := r.Probe(ctx)
	if !probe.Available || probe.BinaryPath == "" {
		return nil, &ClassifiedError{Class: ErrorClassCLINotFound, Message: probe.Reason}
	}
	isV2 := isOpenCodeV2(probe.Version)

	cfg := r.currentConfig()
	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = cfg.TimeoutSeconds
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultOpenCodeTimeoutSec
	}
	idleSec := req.IdleTimeoutSeconds
	if idleSec <= 0 {
		idleSec = cfg.IdleTimeoutSeconds
	}
	if idleSec <= 0 {
		idleSec = defaultOpenCodeIdleSec
	}
	contMaxSec := req.ContinuationMaxSeconds
	if contMaxSec <= 0 {
		contMaxSec = cfg.ContinuationMaxSeconds
	}
	if contMaxSec <= 0 {
		contMaxSec = defaultOpenCodeContMaxSec
	}

	// 在 0700 独立隔离根目录下创建本次会话的 XDG 与 0600 opencode.json，退出时彻底销毁
	sessEnv, err := prepareSessionEnv(req, isV2)
	if err != nil {
		return nil, &ClassifiedError{Class: ErrorClassUnknown, Message: "初始化 OpenCode 隔离会话环境失败", Cause: err}
	}
	defer sessEnv.Cleanup()

	sessionTitle := "archdoc-docgen"
	if slug := strings.TrimSpace(req.DocTypeSlug); slug != "" {
		sessionTitle = "archdoc-" + slug
	}

	startedAt := time.Now()
	promptText := BuildDocumentPrompt(req)

	outcome1 := runOpenCodeProcess(ctx, processRunSpec{
		BinaryPath:   probe.BinaryPath,
		WorkDir:      req.WorkDir,
		ModelArg:     sessEnv.ModelArg,
		PromptText:   promptText,
		PromptName:   "prompt-attempt1.md",
		SessionTitle: sessionTitle,
		IsV2:         isV2,
		Env:          sessEnv.EnvList,
		SessionDir:   sessEnv.RootDir,
		Timeout:      time.Duration(timeoutSec) * time.Second,
		IdleTimeout:  time.Duration(idleSec) * time.Second,
	})

	parsed1 := ParseEventStream(outcome1.Stdout)
	result := &DocGenResult{
		SessionID:         parsed1.SessionID,
		Slots:             make(map[string]SlotOutput, len(req.ActiveSlots)),
		InputTokens:       parsed1.InputTokens,
		CachedInputTokens: parsed1.CachedInputTokens,
		OutputTokens:      parsed1.OutputTokens,
		TerminationReason: outcome1.TerminationReason,
	}

	var firstErr error
	if outcome1.Err != nil {
		firstErr = classifyProcessFailure(outcome1, parsed1)
	} else {
		slotMap, missing := ParseOpenCodeDocOutput(parsed1.FinalText, req.ActiveSlots)
		for k, v := range slotMap {
			result.Slots[k] = v
		}
		result.MissingSlotIDs = missing
		if len(missing) > 0 {
			firstErr = &ClassifiedError{
				Class:   ErrorClassOutputMissing,
				Message: fmt.Sprintf("首轮会话遗漏 %d 个槽位输出: %v", len(missing), missing),
			}
		}
	}

	if firstErr == nil {
		result.LatencyMs = time.Since(startedAt).Milliseconds()
		return result, nil
	}

	// 判断是否满足 --session 至多 1 次断点续跑条件
	canResume, contBudget := shouldContinueSession(ctx, req.ContinuationEnabled, firstErr, result.SessionID, timeoutSec, contMaxSec, startedAt)
	if !canResume {
		if len(result.Slots) > 0 {
			// 首轮已产出部分槽位且不可续跑时保留部分产物供编排层部分落库与单槽位重试
			result.LatencyMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}
		return nil, firstErr
	}

	if ClassifyError(firstErr) == ErrorClassRateLimited {
		timer := time.NewTimer(2500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &ClassifiedError{Class: ErrorClassCanceled, Message: "限流退避期间被取消", Cause: ctx.Err()}
		case <-timer.C:
		}
	}

	var targetSlots []SlotTaskSpec
	if len(result.MissingSlotIDs) > 0 {
		missingSet := make(map[string]bool, len(result.MissingSlotIDs))
		for _, id := range result.MissingSlotIDs {
			missingSet[id] = true
		}
		for _, s := range req.ActiveSlots {
			if missingSet[s.SlotID] {
				targetSlots = append(targetSlots, s)
			}
		}
	} else {
		targetSlots = req.ActiveSlots
	}

	contPrompt := BuildContinuationPrompt(targetSlots)
	result.Continuations = 1

	outcome2 := runOpenCodeProcess(ctx, processRunSpec{
		BinaryPath:   probe.BinaryPath,
		WorkDir:      req.WorkDir,
		ModelArg:     sessEnv.ModelArg,
		PromptText:   contPrompt,
		PromptName:   "prompt-attempt2.md",
		SessionID:    result.SessionID,
		SessionTitle: sessionTitle,
		IsV2:         isV2,
		Env:          sessEnv.EnvList,
		SessionDir:   sessEnv.RootDir,
		Timeout:      contBudget,
		IdleTimeout:  time.Duration(idleSec) * time.Second,
	})

	parsed2 := ParseEventStream(outcome2.Stdout)
	if parsed2.SessionID != "" {
		result.SessionID = parsed2.SessionID
	}
	result.InputTokens += parsed2.InputTokens
	result.CachedInputTokens += parsed2.CachedInputTokens
	if result.CachedInputTokens > result.InputTokens {
		result.CachedInputTokens = result.InputTokens
	}
	result.OutputTokens += parsed2.OutputTokens
	if outcome2.TerminationReason != "" {
		result.TerminationReason = outcome2.TerminationReason
	}

	if outcome2.Err != nil {
		if len(result.Slots) > 0 {
			result.LatencyMs = time.Since(startedAt).Milliseconds()
			return result, nil
		}
		contErr := classifyProcessFailure(outcome2, parsed2)
		return nil, &ClassifiedError{
			Class:   ClassifyError(contErr),
			Message: fmt.Sprintf("OpenCode --session 续跑失败: %v (首轮错误: %v)", contErr, firstErr),
			Cause:   contErr,
		}
	}

	slotMap2, _ := ParseOpenCodeDocOutput(parsed2.FinalText, targetSlots)
	for k, v := range slotMap2 {
		result.Slots[k] = v
	}

	// 重新核算最终缺失槽位集合
	var finalMissing []string
	for _, s := range req.ActiveSlots {
		if out, ok := result.Slots[s.SlotID]; !ok || strings.TrimSpace(out.Markdown) == "" {
			finalMissing = append(finalMissing, s.SlotID)
		}
	}
	result.MissingSlotIDs = finalMissing
	result.LatencyMs = time.Since(startedAt).Milliseconds()

	if len(result.Slots) == 0 {
		return nil, &ClassifiedError{
			Class:   ErrorClassOutputMissing,
			Message: "OpenCode 续跑后仍未解析到任何有效槽位 Markdown",
		}
	}
	return result, nil
}

func shouldContinueSession(
	ctx context.Context,
	enabled bool,
	firstErr error,
	sessionID string,
	timeoutSec int,
	contMaxSec int,
	startedAt time.Time,
) (bool, time.Duration) {
	if !enabled || strings.TrimSpace(sessionID) == "" || ctx.Err() != nil {
		return false, 0
	}

	cls := ClassifyError(firstErr)
	switch cls {
	case ErrorClassCanceled, ErrorClassAuth, ErrorClassContentFiltered, ErrorClassCLINotFound:
		return false, 0
	case ErrorClassTimeout, ErrorClassIdleTimeout, ErrorClassRateLimited, ErrorClassNetworkTransient, ErrorClassOutputMissing:
	default:
		return false, 0
	}

	budget := time.Duration(contMaxSec) * time.Second
	reqBudget := time.Duration(timeoutSec) * time.Second
	if remainingReq := reqBudget - time.Since(startedAt); remainingReq > budget {
		budget = remainingReq
	}
	budget = budget.Truncate(time.Second)

	if deadline, ok := ctx.Deadline(); ok {
		remainingParent := time.Until(deadline)
		if remainingParent <= 0 {
			return false, 0
		}
		if remainingParent < budget {
			budget = remainingParent
		}
	}

	if budget < minOpenCodeContinuationWindow {
		return false, 0
	}
	return true, budget
}
