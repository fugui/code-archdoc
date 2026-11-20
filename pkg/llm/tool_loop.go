package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	defaultToolMaxRounds        = 4
	hardToolMaxRounds           = 6
	defaultToolMaxCallsPerRound = 4
	hardToolMaxCallsPerRound    = 6
	defaultToolMaxTotalCalls    = 12
	hardToolMaxTotalCalls       = 20
	defaultToolTotalOutputBytes = 36000
)

// ToolLoopOptions 控制受限只读 Tool-Calling 执行循环的安全与预算护栏。
type ToolLoopOptions struct {
	// Nonce 本次会话使用的 128-bit 随机数据栅栏标识（用于包裹所有 role="tool" 返回结果，防提示词注入）。
	Nonce string
	// MaxRounds 允许的最大工具调用轮次（不含最终文本输出轮；默认 4，硬上限 6）。
	MaxRounds int
	// MaxCallsPerRound 单轮最多执行的工具调用数（默认 4，硬上限 6）。
	MaxCallsPerRound int
	// MaxTotalCalls 整个循环允许执行的累计工具调用上限（默认 12，硬上限 20）。
	MaxTotalCalls int
	// MaxTotalOutputBytes 所有工具输出累计写入上下文的字节硬上限（默认 36KB）。
	MaxTotalOutputBytes int
	// MaxTotalTokens 可选：累计 Prompt+Completion Token 预算护栏；超过后强制结束工具调用并收敛输出。
	MaxTotalTokens int
}

// ToolTraceEvent 描述单次只读工具调用的可观测轨迹（严禁包含源码明文或敏感凭据）。
type ToolTraceEvent struct {
	Round      int    `json:"round"`
	ToolName   string `json:"tool_name"`
	Summary    string `json:"summary"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
}

// ToolLoopCallbacks 为流式 Tool-Calling 提供增量内容与工具调用进度回调。
type ToolLoopCallbacks struct {
	OnDelta     func(delta string) error
	OnToolEvent func(ev ToolTraceEvent) error
}

// ToolLoopResult 封装受控多轮 Tool-Calling 的最终响应、聚合 Usage 指标与工具轨迹。
type ToolLoopResult struct {
	Response        *ChatResponse
	Rounds          int
	ToolCallsCount  int
	FallbackNoTools bool
	Trace           []ToolTraceEvent
}

const toolLoopFinalizeNotice = "只读源码工具探索配额已用尽。严禁继续调用任何工具，请立即基于已获取的 L1 Code IR 与上述源码事实，严格按 system 规定的 JSON 格式输出最终结果。"

// RunToolLoop 执行非流式受限只读 Tool-Calling 循环（供核心流程章节槽位生成使用）。
func RunToolLoop(
	ctx context.Context,
	client *Client,
	cfg EndpointConfig,
	req ChatRequest,
	executor ToolExecutor,
	opts ToolLoopOptions,
) (*ToolLoopResult, error) {
	return runToolLoopInternal(ctx, client, cfg, req, executor, opts, false, ToolLoopCallbacks{})
}

// RunToolLoopStream 执行 SSE 流式受限只读 Tool-Calling 循环（供 Canvas Studio Chat 局部精修使用）。
func RunToolLoopStream(
	ctx context.Context,
	client *Client,
	cfg EndpointConfig,
	req ChatRequest,
	executor ToolExecutor,
	opts ToolLoopOptions,
	callbacks ToolLoopCallbacks,
) (*ToolLoopResult, error) {
	return runToolLoopInternal(ctx, client, cfg, req, executor, opts, true, callbacks)
}

func runToolLoopInternal(
	ctx context.Context,
	client *Client,
	cfg EndpointConfig,
	req ChatRequest,
	executor ToolExecutor,
	opts ToolLoopOptions,
	stream bool,
	callbacks ToolLoopCallbacks,
) (*ToolLoopResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return nil, fmt.Errorf("LLM client 不能为空")
	}

	var toolDefs []ToolDefinition
	if executor != nil {
		toolDefs = executor.Definitions()
	}

	// 若未配置可用工具，直接退化为单轮普通调用（保持 100% 零开销兼容）
	if len(toolDefs) == 0 {
		resp, err := invokeSingleRound(ctx, client, cfg, req, stream, callbacks.OnDelta)
		if err != nil {
			return nil, err
		}
		return &ToolLoopResult{
			Response: resp,
			Rounds:   1,
		}, nil
	}

	nonce := strings.TrimSpace(opts.Nonce)
	if nonce == "" {
		nonce = GenerateNonce()
	}
	maxRounds := clampInt(opts.MaxRounds, defaultToolMaxRounds, 1, hardToolMaxRounds)
	maxCallsPerRound := clampInt(opts.MaxCallsPerRound, defaultToolMaxCallsPerRound, 1, hardToolMaxCallsPerRound)
	maxTotalCalls := clampInt(opts.MaxTotalCalls, defaultToolMaxTotalCalls, 1, hardToolMaxTotalCalls)
	maxTotalOutputBytes := opts.MaxTotalOutputBytes
	if maxTotalOutputBytes <= 0 {
		maxTotalOutputBytes = defaultToolTotalOutputBytes
	}

	history := make([]Message, len(req.Messages))
	copy(history, req.Messages)

	var (
		totalUsage       UsageMetrics
		trace            []ToolTraceEvent
		totalToolCalls   int
		totalOutputBytes int
		toolRoundsUsed   int
		totalTurns       int
	)

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		totalTurns++

		canUseTools := toolRoundsUsed < maxRounds &&
			totalToolCalls < maxTotalCalls &&
			totalOutputBytes < maxTotalOutputBytes &&
			(opts.MaxTotalTokens <= 0 || (totalUsage.PromptTokens+totalUsage.CompletionTokens) < opts.MaxTotalTokens)

		turnReq := ChatRequest{
			Messages:    history,
			MaxTokens:   req.MaxTokens,
			Temperature: req.Temperature,
			Stream:      stream,
		}
		if canUseTools {
			turnReq.Tools = toolDefs
			if req.ToolChoice != nil {
				turnReq.ToolChoice = req.ToolChoice
			} else {
				turnReq.ToolChoice = "auto"
			}
		}

		resp, err := invokeSingleRound(ctx, client, cfg, turnReq, stream, callbacks.OnDelta)
		if err != nil {
			// 首轮遭遇上游内网模型/网关不支持 tools 参数时，自动平滑降级为无工具单轮调用
			if totalTurns == 1 && canUseTools && IsToolUnsupportedError(err) {
				fallbackReq := ChatRequest{
					Messages:    req.Messages,
					MaxTokens:   req.MaxTokens,
					Temperature: req.Temperature,
					Stream:      stream,
				}
				fbResp, fbErr := invokeSingleRound(ctx, client, cfg, fallbackReq, stream, callbacks.OnDelta)
				if fbErr != nil {
					return nil, fbErr
				}
				return &ToolLoopResult{
					Response:        fbResp,
					Rounds:          1,
					FallbackNoTools: true,
				}, nil
			}
			return nil, err
		}

		accumulateUsage(&totalUsage, resp.Usage)

		// 无工具调用或本轮已处于强制收敛轮：直接结束循环并返回最终聚合响应
		if len(resp.ToolCalls) == 0 || !canUseTools {
			finalResp := *resp
			finalResp.Usage = totalUsage
			return &ToolLoopResult{
				Response:       &finalResp,
				Rounds:         totalTurns,
				ToolCallsCount: totalToolCalls,
				Trace:          trace,
			}, nil
		}

		toolRoundsUsed++

		// 将 assistant 的 tool_calls 意图记录进会话历史
		assistantCalls := resp.ToolCalls
		if len(assistantCalls) > maxCallsPerRound {
			assistantCalls = assistantCalls[:maxCallsPerRound]
		}
		history = append(history, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: assistantCalls,
		})

		// 依次执行只读沙箱工具，并用随机 nonce 数据栅栏包裹全部返回结果（SC-13 防间接注入）
		for _, tc := range assistantCalls {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			var toolOut, summary string
			var isErr bool
			tStart := time.Now()

			if totalToolCalls >= maxTotalCalls {
				toolOut = "已达到单次任务累计工具调用次数上限，跳过本次调用。"
				summary = fmt.Sprintf("%s (quota_exceeded)", tc.Function.Name)
				isErr = true
			} else if totalOutputBytes >= maxTotalOutputBytes {
				toolOut = "已达到累计源码读取字节上限，跳过本次调用。"
				summary = fmt.Sprintf("%s (output_cap_reached)", tc.Function.Name)
				isErr = true
			} else {
				toolOut, summary, isErr = executor.Execute(ctx, tc)
				totalToolCalls++
				remaining := maxTotalOutputBytes - totalOutputBytes
				if len(toolOut) > remaining {
					if remaining > 128 {
						toolOut = toolOut[:remaining] + "\n...(truncated_by_total_tool_budget)"
					} else {
						toolOut = "...(truncated_by_total_tool_budget)"
					}
				}
				totalOutputBytes += len(toolOut)
			}

			ev := ToolTraceEvent{
				Round:      toolRoundsUsed,
				ToolName:   tc.Function.Name,
				Summary:    summary,
				IsError:    isErr,
				DurationMs: time.Since(tStart).Milliseconds(),
			}
			trace = append(trace, ev)
			if callbacks.OnToolEvent != nil {
				if cbErr := callbacks.OnToolEvent(ev); cbErr != nil {
					return nil, cbErr
				}
			}

			wrapped := WrapUntrustedDataWithNonce(nonce, "tool_result:"+tc.Function.Name, toolOut)
			history = append(history, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    wrapped,
			})
		}

		// 若下一轮即将触发工具配额上限，注入收敛提示引导模型直接产出最终结构化 JSON
		nextCanUseTools := toolRoundsUsed < maxRounds &&
			totalToolCalls < maxTotalCalls &&
			totalOutputBytes < maxTotalOutputBytes &&
			(opts.MaxTotalTokens <= 0 || (totalUsage.PromptTokens+totalUsage.CompletionTokens) < opts.MaxTotalTokens)
		if !nextCanUseTools {
			history = append(history, Message{
				Role:    "user",
				Content: toolLoopFinalizeNotice,
			})
		}
	}
}

func invokeSingleRound(
	ctx context.Context,
	client *Client,
	cfg EndpointConfig,
	req ChatRequest,
	stream bool,
	onDelta func(string) error,
) (*ChatResponse, error) {
	if stream {
		return client.ChatCompletionStream(ctx, cfg, req, onDelta)
	}
	return client.ChatCompletion(ctx, cfg, req)
}

func accumulateUsage(dst *UsageMetrics, delta UsageMetrics) {
	if dst.ModelName == "" && delta.ModelName != "" {
		dst.ModelName = delta.ModelName
	}
	dst.PromptTokens += delta.PromptTokens
	dst.CachedPromptTokens += delta.CachedPromptTokens
	dst.CompletionTokens += delta.CompletionTokens
	dst.LatencyMs += delta.LatencyMs
}
