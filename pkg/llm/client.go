package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
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

const (
	// DefaultUserAgent 未显式配置时发往上游 LLM 网关的默认客户端标识。
	DefaultUserAgent = "Code-Archdoc/dev"

	defaultAttemptTimeoutSeconds   = 120
	defaultFirstByteTimeoutSeconds = 90
	defaultIdleTimeoutSeconds      = 60
	maxAllowedRetries              = 10
	maxRetryBackoff                = 30 * time.Second
)

// EndpointConfig 单次调用的目标模型端点与重试配置。
type EndpointConfig struct {
	BaseURL                 string
	ModelName               string
	APIKey                  string
	TimeoutSeconds          int
	FirstByteTimeoutSeconds int
	IdleTimeoutSeconds      int
	MaxRetries              int
	UserAgent               string
	ExtraHeaders            map[string]string
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

func isStreamOptionsUnsupportedError(err error) bool {
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
	return strings.Contains(lower, "stream_options") || strings.Contains(lower, "include_usage")
}

func isStreamUnsupportedError(err error) bool {
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
	return strings.Contains(lower, "stream") &&
		(strings.Contains(lower, "unsupported") ||
			strings.Contains(lower, "not support") ||
			strings.Contains(lower, "not_supported") ||
			strings.Contains(lower, "disabled") ||
			strings.Contains(lower, "invalid"))
}

// Client 纯 Go OpenAI Compatible HTTP/SSE 客户端（支持 0..10 次指数退避重试、三层超时看门狗与双模自适应解析）。
type Client struct {
	httpClient  *http.Client
	baseBackoff time.Duration
	userAgent   string
}

// NewClient 创建默认客户端（默认退避基准 1s）。
func NewClient() *Client {
	return &Client{
		httpClient:  &http.Client{},
		baseBackoff: 1 * time.Second,
		userAgent:   DefaultUserAgent,
	}
}

// NewClientWithOptions 允许单测注入自定义 http.Client 与退避基准间隔。
func NewClientWithOptions(httpClient *http.Client, baseBackoff time.Duration) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if baseBackoff <= 0 {
		baseBackoff = 1 * time.Second
	}
	return &Client{
		httpClient:  httpClient,
		baseBackoff: baseBackoff,
		userAgent:   DefaultUserAgent,
	}
}

// WithUserAgent 配置客户端默认发送的 User-Agent（空串保持现有值）。
func (c *Client) WithUserAgent(ua string) *Client {
	if trimmed := strings.TrimSpace(ua); trimmed != "" {
		c.userAgent = trimmed
	}
	return c
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatPayload struct {
	Model         string               `json:"model"`
	Messages      []Message            `json:"messages"`
	Tools         []ToolDefinition     `json:"tools,omitempty"`
	ToolChoice    any                  `json:"tool_choice,omitempty"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
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

// ChatCompletion 执行非流式 Chat Completions 调用（带 429/5xx/超时退避重试与 ctx 取消即时中断）。
func (c *Client) ChatCompletion(ctx context.Context, cfg EndpointConfig, req ChatRequest) (*ChatResponse, error) {
	req.Stream = false
	return c.executeWithRetry(ctx, cfg, req, nil)
}

// ChatCompletionStream 执行 SSE 流式 Chat Completions 调用（兼容 Content-Type 双模响应与已吐流防重复保护）。
func (c *Client) ChatCompletionStream(ctx context.Context, cfg EndpointConfig, req ChatRequest, onDelta func(delta string) error) (*ChatResponse, error) {
	req.Stream = true
	return c.executeWithRetry(ctx, cfg, req, onDelta)
}

func resolveEffectiveTimeouts(cfg EndpointConfig) (attemptTimeout, firstByteTimeout, idleTimeout time.Duration) {
	timeoutSec := cfg.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultAttemptTimeoutSeconds
	}
	attemptTimeout = time.Duration(timeoutSec) * time.Second

	fbSec := cfg.FirstByteTimeoutSeconds
	if fbSec <= 0 {
		fbSec = defaultFirstByteTimeoutSeconds
	}
	firstByteTimeout = time.Duration(fbSec) * time.Second
	if firstByteTimeout > attemptTimeout {
		firstByteTimeout = attemptTimeout
	}

	idleSec := cfg.IdleTimeoutSeconds
	if idleSec <= 0 {
		idleSec = defaultIdleTimeoutSeconds
	}
	idleTimeout = time.Duration(idleSec) * time.Second
	if idleTimeout > attemptTimeout {
		idleTimeout = attemptTimeout
	}
	return attemptTimeout, firstByteTimeout, idleTimeout
}

func (c *Client) executeWithRetry(parentCtx context.Context, cfg EndpointConfig, req ChatRequest, onDelta func(string) error) (*ChatResponse, error) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if parentCtx.Err() != nil {
		return nil, parentCtx.Err()
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
	if maxRetries > maxAllowedRetries {
		maxRetries = maxAllowedRetries
	}

	attemptTimeout, firstByteTimeout, idleTimeout := resolveEffectiveTimeouts(cfg)
	endpoint := baseURL + "/chat/completions"

	useStream := req.Stream
	includeStreamOpts := req.Stream

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if parentCtx.Err() != nil {
			return nil, parentCtx.Err()
		}

		start := time.Now()
		resp, retryable, retryAfter, callErr := c.doAttemptWithStreamFallback(
			parentCtx, endpoint, cfg, req, attemptTimeout, firstByteTimeout, idleTimeout,
			&useStream, &includeStreamOpts, onDelta,
		)
		latencyMs := time.Since(start).Milliseconds()

		if callErr == nil {
			resp.Usage.LatencyMs = latencyMs
			if resp.Usage.ModelName == "" {
				resp.Usage.ModelName = cfg.ModelName
			}
			return resp, nil
		}

		lastErr = callErr
		if parentCtx.Err() != nil || !retryable || attempt == maxRetries {
			return nil, lastErr
		}

		backoff := computeBackoff(c.baseBackoff, attempt, retryAfter)
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-parentCtx.Done():
				timer.Stop()
				return nil, parentCtx.Err()
			case <-timer.C:
			}
		}
	}

	return nil, lastErr
}

func marshalChatPayload(cfg EndpointConfig, req ChatRequest, useStream, includeStreamOpts bool) ([]byte, error) {
	payload := openAIChatPayload{
		Model:       strings.TrimSpace(cfg.ModelName),
		Messages:    req.Messages,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      useStream,
	}
	if useStream && includeStreamOpts {
		payload.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 LLM 请求体失败: %w", err)
	}
	return b, nil
}

func (c *Client) doAttemptWithStreamFallback(
	parentCtx context.Context,
	endpoint string,
	cfg EndpointConfig,
	req ChatRequest,
	attemptTimeout, firstByteTimeout, idleTimeout time.Duration,
	useStream, includeStreamOpts *bool,
	onDelta func(string) error,
) (*ChatResponse, bool, time.Duration, error) {
	for downgradeTry := 0; downgradeTry < 3; downgradeTry++ {
		payloadBytes, err := marshalChatPayload(cfg, req, *useStream, *includeStreamOpts)
		if err != nil {
			return nil, false, 0, err
		}

		attemptCtx, cancelAttempt := context.WithTimeout(parentCtx, attemptTimeout)
		resp, retryable, retryAfter, callErr := c.doOnce(
			parentCtx, attemptCtx, cancelAttempt, endpoint, cfg, payloadBytes,
			*useStream, firstByteTimeout, idleTimeout, onDelta,
		)
		cancelAttempt()

		if callErr == nil {
			return resp, false, 0, nil
		}
		if *useStream && *includeStreamOpts && isStreamOptionsUnsupportedError(callErr) {
			*includeStreamOpts = false
			continue
		}
		if *useStream && isStreamUnsupportedError(callErr) {
			*useStream = false
			*includeStreamOpts = false
			continue
		}
		return nil, retryable, retryAfter, callErr
	}
	return nil, false, 0, fmt.Errorf("LLM 流式参数降级重试超出上限")
}

func (c *Client) doOnce(
	parentCtx context.Context,
	attemptCtx context.Context,
	cancelAttempt context.CancelFunc,
	endpoint string,
	cfg EndpointConfig,
	payloadBytes []byte,
	stream bool,
	firstByteTimeout time.Duration,
	idleTimeout time.Duration,
	onDelta func(string) error,
) (*ChatResponse, bool, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, false, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = strings.TrimSpace(c.userAgent)
	}
	if ua == "" {
		ua = DefaultUserAgent
	}
	httpReq.Header.Set("User-Agent", ua)
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

	var firstByteTimedOut atomic.Bool
	fbTimer := time.AfterFunc(firstByteTimeout, func() {
		firstByteTimedOut.Store(true)
		cancelAttempt()
	})
	defer fbTimer.Stop()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		fbTimer.Stop()
		if parentCtx.Err() != nil {
			return nil, false, 0, parentCtx.Err()
		}
		if firstByteTimedOut.Load() || attemptCtx.Err() != nil || isRetryableNetErr(err) {
			return nil, true, 0, err
		}
		return nil, false, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fbTimer.Stop()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout ||
			resp.StatusCode == http.StatusInternalServerError
		var retryAfter time.Duration
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return nil, retryable, retryAfter, &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(errBody)),
		}
	}

	br := bufio.NewReaderSize(resp.Body, 64*1024)
	firstNonSpace, err := peekFirstNonSpaceByte(br)
	fbTimer.Stop()
	if err != nil {
		if parentCtx.Err() != nil {
			return nil, false, 0, parentCtx.Err()
		}
		if firstByteTimedOut.Load() || attemptCtx.Err() != nil || isRetryableNetErr(err) {
			return nil, true, 0, err
		}
		return nil, false, 0, err
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	isSSE := false
	if stream {
		switch {
		case strings.Contains(contentType, "text/event-stream"):
			isSSE = true
		case strings.Contains(contentType, "application/json"):
			isSSE = false
		case firstNonSpace == '{':
			isSSE = false
		default:
			isSSE = true
		}
	}

	if isSSE {
		chatResp, retryable, sseErr := parseSSEResponse(
			parentCtx, attemptCtx, cancelAttempt, br, cfg.ModelName, idleTimeout, onDelta,
		)
		return chatResp, retryable, 0, sseErr
	}

	rawBody, err := io.ReadAll(io.LimitReader(br, 8*1024*1024))
	if err != nil {
		if parentCtx.Err() != nil {
			return nil, false, 0, parentCtx.Err()
		}
		return nil, attemptCtx.Err() != nil || isRetryableNetErr(err), 0, err
	}
	var parsed openAIChatResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return nil, false, 0, fmt.Errorf("解析 LLM 响应 JSON 失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, false, 0, fmt.Errorf("LLM 响应缺少 choices")
	}

	content := parsed.Choices[0].Message.Content
	if onDelta != nil && content != "" {
		if cbErr := onDelta(content); cbErr != nil {
			return nil, false, 0, cbErr
		}
	}

	cached := parsed.Usage.PromptTokensDetails.CachedTokens
	if cached == 0 && parsed.Usage.PromptCacheHitTokens > 0 {
		cached = parsed.Usage.PromptCacheHitTokens
	}
	return &ChatResponse{
		Content:      content,
		ToolCalls:    normalizeToolCalls(parsed.Choices[0].Message.ToolCalls),
		FinishReason: parsed.Choices[0].FinishReason,
		Usage: UsageMetrics{
			ModelName:          parsed.Model,
			PromptTokens:       parsed.Usage.PromptTokens,
			CachedPromptTokens: cached,
			CompletionTokens:   parsed.Usage.CompletionTokens,
		},
	}, false, 0, nil
}

func peekFirstNonSpaceByte(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			if unreadErr := br.UnreadByte(); unreadErr != nil {
				return 0, unreadErr
			}
			return b, nil
		}
	}
}

type idleWatchReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (w *idleWatchReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 && w.timer != nil {
		w.timer.Reset(w.idle)
	}
	return n, err
}

func parseSSEResponse(
	parentCtx context.Context,
	attemptCtx context.Context,
	cancelAttempt context.CancelFunc,
	body io.Reader,
	defaultModel string,
	idleTimeout time.Duration,
	onDelta func(string) error,
) (*ChatResponse, bool, error) {
	var idleTimedOut atomic.Bool
	var idleTimer *time.Timer
	if idleTimeout > 0 && cancelAttempt != nil {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			idleTimedOut.Store(true)
			cancelAttempt()
		})
		defer idleTimer.Stop()
	}

	watched := &idleWatchReader{r: body, timer: idleTimer, idle: idleTimeout}
	scanner := bufio.NewScanner(watched)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var full strings.Builder
	usage := UsageMetrics{ModelName: defaultModel}
	var finishReason string
	var streamToolCalls []ToolCall
	emittedAny := false

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
					emittedAny = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if parentCtx != nil && parentCtx.Err() != nil {
			return nil, false, parentCtx.Err()
		}
		// 已吐流防重复护栏：当向调用方推送过非空 delta 后发生断流或空闲超时，严禁重放防 UI 重复文本
		if onDelta != nil && emittedAny {
			return nil, false, err
		}
		if idleTimedOut.Load() || (attemptCtx != nil && attemptCtx.Err() != nil) || isRetryableNetErr(err) {
			return nil, true, err
		}
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

func parseRetryAfter(raw string, now time.Time) time.Duration {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > maxRetryBackoff {
			return maxRetryBackoff
		}
		return d
	}
	if ts, err := http.ParseTime(v); err == nil {
		d := ts.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > maxRetryBackoff {
			return maxRetryBackoff
		}
		return d
	}
	return 0
}

func computeBackoff(base time.Duration, attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > maxRetryBackoff {
			return maxRetryBackoff
		}
		return retryAfter
	}
	if base <= 0 {
		base = 1 * time.Second
	}
	shift := attempt
	if shift < 0 {
		shift = 0
	}
	if shift > 10 {
		shift = 10
	}
	d := base * (1 << shift)
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	jitter := 0.8 + rand.Float64()*0.4
	d = time.Duration(float64(d) * jitter)
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	if d < 0 {
		return 0
	}
	return d
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutErr(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "unexpected eof") ||
		strings.Contains(lower, "client.timeout")
}
