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

// Message 表示 OpenAI Compatible 对话消息。
type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
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
	Content string
	Usage   UsageMetrics
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
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
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
		return nil, retryable, fmt.Errorf("LLM 上游返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
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
		Content: parsed.Choices[0].Message.Content,
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
			delta := chunk.Choices[0].Delta.Content
			if delta == "" {
				delta = chunk.Choices[0].Message.Content
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
		Content: full.String(),
		Usage:   usage,
	}, false, nil
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
