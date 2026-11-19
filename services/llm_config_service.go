package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code-archdoc/models"
	"code-archdoc/pkg/security"
)

const llmProbeTimeout = 15 * time.Second

// ErrInvalidLLMParam 表示 LLM 配置或探活参数非法（对应 HTTP 400 INVALID_PARAM）。
var ErrInvalidLLMParam = errors.New("LLM 配置参数非法")

// LLMConfigVO 对外暴露的脱敏模型配置视图（绝不含明文 APIKey 或密文）。
type LLMConfigVO struct {
	ID                 uint       `json:"id"`
	Tier               string     `json:"tier"`
	BaseURL            string     `json:"base_url"`
	ModelName          string     `json:"model_name"`
	APIKeyMasked       string     `json:"api_key_masked"`
	HasAPIKey          bool       `json:"has_api_key"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	MaxRetries         int        `json:"max_retries"`
	MaxTokensPerTask   int        `json:"max_tokens_per_task"`
	MaxFilesPerTask    int        `json:"max_files_per_task"`
	IsActive           bool       `json:"is_active"`
	LastProbeStatus    string     `json:"last_probe_status"`
	LastProbeLatencyMs int64      `json:"last_probe_latency_ms"`
	LastProbedAt       *time.Time `json:"last_probed_at"`
}

// LLMConfigAuditSnapshot 用于 commonAudit 字段级 Diff 的脱敏快照（强约束：严禁包含明文或密文）。
type LLMConfigAuditSnapshot struct {
	Tier             string `json:"tier"`
	BaseURL          string `json:"base_url"`
	ModelName        string `json:"model_name"`
	APIKeyMasked     string `json:"api_key_masked"`
	HasAPIKey        bool   `json:"has_api_key"`
	APIKeyChanged    bool   `json:"api_key_changed"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxRetries       int    `json:"max_retries"`
	MaxTokensPerTask int    `json:"max_tokens_per_task"`
	MaxFilesPerTask  int    `json:"max_files_per_task"`
	IsActive         bool   `json:"is_active"`
}

// UpdateLLMConfigRequest 对应 PUT /api/admin/llm-configs/:tier 入参。
type UpdateLLMConfigRequest struct {
	BaseURL          string `json:"base_url"`
	ModelName        string `json:"model_name"`
	APIKey           string `json:"api_key"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxRetries       int    `json:"max_retries"`
	MaxTokensPerTask int    `json:"max_tokens_per_task"`
	MaxFilesPerTask  int    `json:"max_files_per_task"`
	IsActive         bool   `json:"is_active"`
}

// ProbeLLMConfigRequest 对应 POST /api/admin/llm-configs/:tier/probe 可选预测试入参。
type ProbeLLMConfigRequest struct {
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
}

// LLMProbeResult 对应连通性探活结果。
type LLMProbeResult struct {
	Tier         string `json:"tier"`
	Reachable    bool   `json:"reachable"`
	LatencyMs    int64  `json:"latency_ms"`
	ModelEcho    string `json:"model_echo"`
	ErrorMessage string `json:"error_message"`
}

// LLMConfigService 负责分层模型配置加解密持久化、脱敏视图构建与连通性探活（F-14）。
type LLMConfigService struct {
	govRepo    GovernanceRepo
	cipher     *security.Cipher
	httpClient *http.Client
}

func NewLLMConfigService(govRepo GovernanceRepo, cipher *security.Cipher) *LLMConfigService {
	return &LLMConfigService{
		govRepo: govRepo,
		cipher:  cipher,
		httpClient: &http.Client{
			Timeout: llmProbeTimeout,
		},
	}
}

func (s *LLMConfigService) ListConfigs(ctx context.Context) ([]LLMConfigVO, error) {
	list, err := s.govRepo.ListLLMConfigs(ctx)
	if err != nil {
		return nil, err
	}
	res := make([]LLMConfigVO, 0, len(list))
	for i := range list {
		res = append(res, toLLMConfigVO(&list[i]))
	}
	return res, nil
}

// UpdateConfig 更新指定档位的模型配置，返回更新后的脱敏视图以及用于审计 Diff 的新旧脱敏快照。
func (s *LLMConfigService) UpdateConfig(
	ctx context.Context,
	tier string,
	req UpdateLLMConfigRequest,
	operatorID uint,
) (*LLMConfigVO, LLMConfigAuditSnapshot, LLMConfigAuditSnapshot, error) {
	if !models.IsValidModelTier(tier) {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: tier 必须为 fast 或 flagship", ErrInvalidLLMParam)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL != "" {
		u, parseErr := url.Parse(baseURL)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: base_url 必须为合法的 http/https 地址且不可内嵌凭据", ErrInvalidLLMParam)
		}
	}

	if req.TimeoutSeconds < 5 || req.TimeoutSeconds > 600 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: timeout_seconds 须在 5~600 之间", ErrInvalidLLMParam)
	}
	if req.MaxRetries < 0 || req.MaxRetries > 10 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: max_retries 须在 0~10 之间", ErrInvalidLLMParam)
	}
	if req.MaxTokensPerTask <= 0 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: max_tokens_per_task 必须大于 0", ErrInvalidLLMParam)
	}
	if req.MaxFilesPerTask <= 0 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: max_files_per_task 必须大于 0", ErrInvalidLLMParam)
	}

	cfg, err := s.govRepo.GetLLMConfigByTier(ctx, tier)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}

	beforeAudit := toLLMAuditSnapshot(cfg, false)

	cfg.BaseURL = baseURL
	cfg.ModelName = strings.TrimSpace(req.ModelName)
	cfg.TimeoutSeconds = req.TimeoutSeconds
	cfg.MaxRetries = req.MaxRetries
	cfg.MaxTokensPerTask = req.MaxTokensPerTask
	cfg.MaxFilesPerTask = req.MaxFilesPerTask
	cfg.IsActive = req.IsActive
	opID := operatorID
	cfg.UpdatedBy = &opID

	apiKeyChanged := false
	trimmedKey := strings.TrimSpace(req.APIKey)
	if trimmedKey != "" {
		if s.cipher == nil {
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("服务端加密器未初始化，拒绝持久化明文 APIKey")
		}
		encrypted, encErr := s.cipher.Encrypt(trimmedKey)
		if encErr != nil {
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("加密 APIKey 失败: %w", encErr)
		}
		cfg.APIKeyCipher = encrypted
		cfg.APIKeyMasked = security.MaskAPIKey(trimmedKey)
		apiKeyChanged = true
	}

	if err := s.govRepo.SaveLLMConfig(ctx, cfg); err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}

	afterAudit := toLLMAuditSnapshot(cfg, apiKeyChanged)
	vo := toLLMConfigVO(cfg)
	return &vo, beforeAudit, afterAudit, nil
}

// ProbeConfig 发起 OpenAI Compatible /chat/completions 轻量探活（max_tokens=1, 15s 超时）。
func (s *LLMConfigService) ProbeConfig(ctx context.Context, tier string, override *ProbeLLMConfigRequest) (*LLMProbeResult, error) {
	if !models.IsValidModelTier(tier) {
		return nil, fmt.Errorf("%w: tier 必须为 fast 或 flagship", ErrInvalidLLMParam)
	}

	saved, err := s.govRepo.GetLLMConfigByTier(ctx, tier)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(saved.BaseURL), "/")
	modelName := strings.TrimSpace(saved.ModelName)
	var apiKey string
	isOverride := false

	if override != nil {
		if u := strings.TrimRight(strings.TrimSpace(override.BaseURL), "/"); u != "" {
			baseURL = u
			isOverride = true
		}
		if m := strings.TrimSpace(override.ModelName); m != "" {
			modelName = m
			isOverride = true
		}
		if k := strings.TrimSpace(override.APIKey); k != "" {
			apiKey = k
			isOverride = true
		}
	}

	if apiKey == "" && saved.APIKeyCipher != "" {
		if s.cipher == nil {
			return &LLMProbeResult{
				Tier:         tier,
				Reachable:    false,
				ErrorMessage: "服务端加密器未初始化，无法解密已保存凭据",
			}, nil
		}
		decrypted, decErr := s.cipher.Decrypt(saved.APIKeyCipher)
		if decErr != nil {
			return &LLMProbeResult{
				Tier:         tier,
				Reachable:    false,
				ErrorMessage: "解密已保存 APIKey 失败，请重新配置密钥",
			}, nil
		}
		apiKey = decrypted
	}

	if baseURL == "" || modelName == "" {
		return &LLMProbeResult{
			Tier:         tier,
			Reachable:    false,
			ErrorMessage: "请先配置合法的 BaseURL 与模型名称",
		}, nil
	}

	u, parseErr := url.Parse(baseURL)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return &LLMProbeResult{
			Tier:         tier,
			Reachable:    false,
			ErrorMessage: "BaseURL 格式非法，仅支持 http/https 协议且不可内嵌凭据",
		}, nil
	}

	endpoint := baseURL + "/chat/completions"
	payloadBytes, err := json.Marshal(map[string]any{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	if err != nil {
		return nil, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, llmProbeTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, reqErr := s.httpClient.Do(httpReq)
	latencyMs := time.Since(start).Milliseconds()

	result := &LLMProbeResult{
		Tier:      tier,
		LatencyMs: latencyMs,
	}

	now := time.Now()
	if reqErr != nil {
		result.Reachable = false
		result.ErrorMessage = fmt.Sprintf("模型服务不可达: %v", reqErr)
		if !isOverride {
			s.recordProbeStatus(ctx, tier, "failed", latencyMs, now)
		}
		return result, nil
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("[Code-Archdoc/LLM] 关闭探活响应体告警: %v", closeErr)
		}
	}()

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if readErr != nil {
		result.Reachable = false
		result.ErrorMessage = fmt.Sprintf("读取模型探活响应失败: %v", readErr)
		if !isOverride {
			s.recordProbeStatus(ctx, tier, "failed", latencyMs, now)
		}
		return result, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Reachable = false
		result.ErrorMessage = fmt.Sprintf("模型接口返回 HTTP %d", resp.StatusCode)
		if !isOverride {
			s.recordProbeStatus(ctx, tier, "failed", latencyMs, now)
		}
		return result, nil
	}

	var chatResp struct {
		Model string `json:"model"`
	}
	if unmarshalErr := json.Unmarshal(bodyBytes, &chatResp); unmarshalErr == nil && chatResp.Model != "" {
		result.ModelEcho = chatResp.Model
	} else {
		result.ModelEcho = modelName
	}

	result.Reachable = true
	if !isOverride {
		s.recordProbeStatus(ctx, tier, "ok", latencyMs, now)
	}
	return result, nil
}

func (s *LLMConfigService) recordProbeStatus(ctx context.Context, tier, status string, latencyMs int64, probedAt time.Time) {
	if err := s.govRepo.UpdateLLMProbeResult(ctx, tier, status, latencyMs, probedAt); err != nil {
		log.Printf("[Code-Archdoc/LLM] 更新探活状态失败 (tier=%s, status=%s): %v", tier, status, err)
	}
}

func toLLMConfigVO(cfg *models.LLMConfig) LLMConfigVO {
	return LLMConfigVO{
		ID:                 cfg.ID,
		Tier:               cfg.Tier,
		BaseURL:            cfg.BaseURL,
		ModelName:          cfg.ModelName,
		APIKeyMasked:       cfg.APIKeyMasked,
		HasAPIKey:          cfg.APIKeyCipher != "",
		TimeoutSeconds:     cfg.TimeoutSeconds,
		MaxRetries:         cfg.MaxRetries,
		MaxTokensPerTask:   cfg.MaxTokensPerTask,
		MaxFilesPerTask:    cfg.MaxFilesPerTask,
		IsActive:           cfg.IsActive,
		LastProbeStatus:    cfg.LastProbeStatus,
		LastProbeLatencyMs: cfg.LastProbeLatencyMs,
		LastProbedAt:       cfg.LastProbedAt,
	}
}

func toLLMAuditSnapshot(cfg *models.LLMConfig, apiKeyChanged bool) LLMConfigAuditSnapshot {
	return LLMConfigAuditSnapshot{
		Tier:             cfg.Tier,
		BaseURL:          cfg.BaseURL,
		ModelName:        cfg.ModelName,
		APIKeyMasked:     cfg.APIKeyMasked,
		HasAPIKey:        cfg.APIKeyCipher != "",
		APIKeyChanged:    apiKeyChanged,
		TimeoutSeconds:   cfg.TimeoutSeconds,
		MaxRetries:       cfg.MaxRetries,
		MaxTokensPerTask: cfg.MaxTokensPerTask,
		MaxFilesPerTask:  cfg.MaxFilesPerTask,
		IsActive:         cfg.IsActive,
	}
}
