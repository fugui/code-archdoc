package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ToolCallFunction 表示 OpenAI Compatible 工具调用函数名与 JSON 参数串。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall 表示模型发起的一次函数工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolFunctionDefinition 表示提供给模型的只读函数工具 Schema。
type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolDefinition 表示 OpenAI Compatible tools 数组中的单个工具声明。
type ToolDefinition struct {
	Type     string                 `json:"type"` // "function"
	Function ToolFunctionDefinition `json:"function"`
}

// Message 表示 OpenAI Compatible 对话消息（支持 system | user | assistant | tool）。
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// EndpointConfig 单次调用的目标模型端点与重试配置。
type EndpointConfig struct {
	BaseURL        string
	ModelName      string
	APIKey         string
	TimeoutSeconds int
	MaxRetries     int
	ExtraHeaders   map[string]string
}

// ChatRequest 构造一次 Chat Completions 请求。
type ChatRequest struct {
	Messages    []Message
	Tools       []ToolDefinition
	ToolChoice  any // nil | "auto" | "none" | map[string]any
	MaxTokens   int
	Temperature *float64
	Stream      bool
}

// UsageMetrics 记录一次 LLM 调用的 Token 成本、KV Cache 命中与耗时。
type UsageMetrics struct {
	ModelName          string
	PromptTokens       int
	CachedPromptTokens int
	CompletionTokens   int
	LatencyMs          int64
}

// ChatResponse 封装一次 Chat Completions 响应。
type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        UsageMetrics
}

// HTTPStatusError 标识上游 LLM 网关返回非 2xx HTTP 状态码的结构化错误。
type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("LLM 上游返回 HTTP %d: %s", e.StatusCode, strings.TrimSpace(e.Body))
}

// IsToolUnsupportedError 判断上游报错是否因内网模型/网关不支持 OpenAI tools/function calling 参数导致。
func IsToolUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode != http.StatusBadRequest &&
		httpErr.StatusCode != http.StatusUnprocessableEntity &&
		httpErr.StatusCode != http.StatusNotImplemented {
		return false
	}
	lower := strings.ToLower(httpErr.Body)
	toolSignals := []string{
		"tool_choice",
		"tools",
		"function call",
		"function_call",
		"tool call",
		"auto-tool-choice",
		"tool-call-parser",
		"does not support tools",
		"not support tools",
		"unsupported parameter",
	}
	for _, sig := range toolSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

// Client 纯 Go OpenAI Compatible HTTP/SSE 客户端（支持 429/5xx 指数退避重试与 ctx.Done 秒停）。
type Client struct {
	httpClient  *http.Client
	baseBackoff time.Duration
}

// NewClient 创建默认客户端。
func NewClient() *Client {
	return &Client{
		httpClient:  &http.Client{},
		baseBackoff: 300 * time.Millisecond,
	}
}

// NewClientWithOptions 允许单测注入自定义 http.Client 与退避基准间隔。
func NewClientWithOptions(httpClient *http.Client, baseBackoff time.Duration) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if baseBackoff <= 0 {
		baseBackoff = 300 * time.Millisecond
	}
	return &Client{
		httpClient:  httpClient,
		baseBackoff: baseBackoff,
	}
}

type openAIChatPayload struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  any              `json:"tool_choice,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
}

type openAIStreamToolCallDelta struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function,omitempty"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		Delta struct {
			Content   string                      `json:"content"`
			ToolCalls []openAIStreamToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
}

// ChatCompletion 执行非流式 Chat Completions 调用（带 429/5xx 指数退避重试与 ctx 取消即时中断）。
func (c *Client) ChatCompletion(ctx context.Context, cfg EndpointConfig, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	return c.executeWithRetry(ctx, cfg, req, nil)
}

// ChatCompletionStream 执行 SSE 流式 Chat Completions 调用。
func (c *Client) ChatCompletionStream(ctx context.Context, cfg EndpointConfig, req ChatRequest, onDelta func(delta string) error) (*ChatResponse, error) {
	req.Stream = true
	return c.executeWithRetry(ctx, cfg, req, onDelta)
}

func (c *Client) executeWithRetry(ctx context.Context, cfg EndpointConfig, req ChatRequest, onDelta func(string) error) (*ChatResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(cfg.ModelName) == "" {
		return nil, fmt.Errorf("LLM 端点配置不完整：base_url 与 model_name 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("LLM base_url 非法")
	}

	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 3 {
		maxRetries = 3
	}
	timeoutSec := cfg.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	payloadBytes, err := json.Marshal(openAIChatPayload{
		Model:       strings.TrimSpace(cfg.ModelName),
		Messages:    req.Messages,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
	})
	if err != nil {
		return nil, fmt.Errorf("序列化 LLM 请求体失败: %w", err)
	}

	endpoint := baseURL + "/chat/completions"
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		start := time.Now()
		resp, retryable, callErr := c.doOnce(reqCtx, endpoint, cfg, payloadBytes, req.Stream, onDelta)
		latencyMs := time.Since(start).Milliseconds()
		cancel()

		if callErr == nil {
			resp.Usage.LatencyMs = latencyMs
			if resp.Usage.ModelName == "" {
				resp.Usage.ModelName = cfg.ModelName
			}
			return resp, nil
		}

		lastErr = callErr
		if ctx.Err() != nil || !retryable || attempt == maxRetries {
			return nil, lastErr
		}

		backoff := c.baseBackoff * (1 << attempt)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}

	return nil, lastErr
}

func (c *Client) doOnce(
	ctx context.Context,
	endpoint string,
	cfg EndpointConfig,
	payloadBytes []byte,
	stream bool,
	onDelta func(string) error,
) (*ChatResponse, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))
	}
	for k, v := range cfg.ExtraHeaders {
		if !strings.EqualFold(k, "Authorization") {
			httpReq.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, isTimeoutErr(err) && ctx.Err() == nil, err
		}
		return nil, isRetryableNetErr(err), err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout ||
			resp.StatusCode == http.StatusInternalServerError
		return nil, retryable, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(errBody)),
		}
	}

	if stream {
		return parseSSEResponse(resp.Body, cfg.ModelName, onDelta)
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, isRetryableNetErr(err), err
	}
	var parsed openAIChatResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return nil, false, fmt.Errorf("解析 LLM 响应 JSON 失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, false, fmt.Errorf("LLM 响应缺少 choices")
	}

	cached := parsed.Usage.PromptTokensDetails.CachedTokens
	if cached == 0 && parsed.Usage.PromptCacheHitTokens > 0 {
		cached = parsed.Usage.PromptCacheHitTokens
	}
	return &ChatResponse{
		Content:      parsed.Choices[0].Message.Content,
		ToolCalls:    normalizeToolCalls(parsed.Choices[0].Message.ToolCalls),
		FinishReason: parsed.Choices[0].FinishReason,
		Usage: UsageMetrics{
			ModelName:          parsed.Model,
			PromptTokens:       parsed.Usage.PromptTokens,
			CachedPromptTokens: cached,
			CompletionTokens:   parsed.Usage.CompletionTokens,
		},
	}, false, nil
}

func parseSSEResponse(body io.Reader, defaultModel string, onDelta func(string) error) (*ChatResponse, bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var full strings.Builder
	usage := UsageMetrics{ModelName: defaultModel}
	var finishReason string
	var streamToolCalls []ToolCall

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			usage.ModelName = chunk.Model
		}
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.CompletionTokens = chunk.Usage.CompletionTokens
			cached := chunk.Usage.PromptTokensDetails.CachedTokens
			if cached == 0 {
				cached = chunk.Usage.PromptCacheHitTokens
			}
			usage.CachedPromptTokens = cached
		}
		if len(chunk.Choices) > 0 {
			ch0 := chunk.Choices[0]
			if ch0.FinishReason != "" {
				finishReason = ch0.FinishReason
			}
			// 聚合流式 delta.tool_calls（按 OpenAI index 分片拼接）
			for _, tcDelta := range ch0.Delta.ToolCalls {
				idx := tcDelta.Index
				if idx < 0 {
					idx = len(streamToolCalls)
				}
				for len(streamToolCalls) <= idx {
					streamToolCalls = append(streamToolCalls, ToolCall{Type: "function"})
				}
				if tcDelta.ID != "" {
					streamToolCalls[idx].ID = tcDelta.ID
				}
				if tcDelta.Type != "" {
					streamToolCalls[idx].Type = tcDelta.Type
				}
				if tcDelta.Function.Name != "" {
					streamToolCalls[idx].Function.Name += tcDelta.Function.Name
				}
				if tcDelta.Function.Arguments != "" {
					streamToolCalls[idx].Function.Arguments += tcDelta.Function.Arguments
				}
			}
			// 兼容部分兼容网关在单帧 message.tool_calls 中直接返回完整调用列表
			if len(ch0.Message.ToolCalls) > 0 && len(streamToolCalls) == 0 {
				streamToolCalls = append(streamToolCalls, ch0.Message.ToolCalls...)
			}

			delta := ch0.Delta.Content
			if delta == "" {
				delta = ch0.Message.Content
			}
			if delta != "" {
				full.WriteString(delta)
				if onDelta != nil {
					if cbErr := onDelta(delta); cbErr != nil {
						return nil, false, cbErr
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}

	return &ChatResponse{
		Content:      full.String(),
		ToolCalls:    normalizeToolCalls(streamToolCalls),
		FinishReason: finishReason,
		Usage:        usage,
	}, false, nil
}

func normalizeToolCalls(raw []ToolCall) []ToolCall {
	if len(raw) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(raw))
	for i, tc := range raw {
		name := strings.TrimSpace(tc.Function.Name)
		if name == "" {
			continue
		}
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		typ := strings.TrimSpace(tc.Type)
		if typ == "" {
			typ = "function"
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out = append(out, ToolCall{
			ID:   id,
			Type: typ,
			Function: ToolCallFunction{
				Name:      name,
				Arguments: args,
			},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isTimeoutErr(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutErr(err) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}
