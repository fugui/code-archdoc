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
	"unicode/utf8"

	"code-archdoc/models"
	"code-archdoc/pkg/llm"
	"code-archdoc/pkg/security"
)

const llmProbeTimeout = 15 * time.Second

var (
	// ErrInvalidLLMParam 表示 LLM 配置或探活参数非法（对应 HTTP 400 INVALID_PARAM）。
	ErrInvalidLLMParam = errors.New("LLM 配置参数非法")
	// ErrLLMConfigConflict 表示模型名称重复或模型仍被引用/为非唯一默认模型（对应 HTTP 409 CONFLICT）。
	ErrLLMConfigConflict = errors.New("LLM 配置冲突")
)

// LLMConfigVO 对外暴露的脱敏模型配置视图（绝不含明文 APIKey）。
type LLMConfigVO struct {
	ID                 uint       `json:"id"`
	Name               string     `json:"name"`
	BaseURL            string     `json:"base_url"`
	ModelName          string     `json:"model_name"`
	APIKeyMasked       string     `json:"api_key_masked"`
	HasAPIKey          bool       `json:"has_api_key"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	MaxRetries         int        `json:"max_retries"`
	MaxTokensPerTask   int        `json:"max_tokens_per_task"`
	MaxFilesPerTask    int        `json:"max_files_per_task"`
	IsActive           bool       `json:"is_active"`
	IsDefault          bool       `json:"is_default"`
	LastProbeStatus    string     `json:"last_probe_status"`
	LastProbeLatencyMs int64      `json:"last_probe_latency_ms"`
	LastProbedAt       *time.Time `json:"last_probed_at"`
}

// LLMConfigAuditSnapshot 用于 commonAudit 字段级 Diff 的脱敏快照（强约束：严禁包含明文 APIKey）。
type LLMConfigAuditSnapshot struct {
	ID               uint   `json:"id"`
	Name             string `json:"name"`
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
	IsDefault        bool   `json:"is_default"`
}

// LLMConfigUpsertRequest 对应 POST /api/admin/llm-configs 与 PUT /api/admin/llm-configs/:id 入参。
type LLMConfigUpsertRequest struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	ModelName        string `json:"model_name"`
	APIKey           string `json:"api_key"`
	ClearAPIKey      bool   `json:"clear_api_key"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxRetries       int    `json:"max_retries"`
	MaxTokensPerTask int    `json:"max_tokens_per_task"`
	MaxFilesPerTask  int    `json:"max_files_per_task"`
	IsActive         *bool  `json:"is_active"`
	IsDefault        *bool  `json:"is_default"`
}

// ProbeLLMConfigRequest 对应 POST /api/admin/llm-configs/:id/probe 与 POST /api/admin/llm-configs/probe 入参。
type ProbeLLMConfigRequest struct {
	BaseURL   string `json:"base_url"`
	ModelName string `json:"model_name"`
	APIKey    string `json:"api_key"`
}

// LLMProbeResult 对应连通性探活结果。
type LLMProbeResult struct {
	ID           uint   `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Reachable    bool   `json:"reachable"`
	LatencyMs    int64  `json:"latency_ms"`
	ModelEcho    string `json:"model_echo"`
	ErrorMessage string `json:"error_message"`
}

// LLMConfigService 负责平等模型池管理、唯一默认模型切换、脱敏视图构建与连通性探活。
type LLMConfigService struct {
	govRepo     GovernanceRepo
	docTypeRepo DocTypeRepo
	httpClient  *http.Client
}

func NewLLMConfigService(govRepo GovernanceRepo) *LLMConfigService {
	return &LLMConfigService{
		govRepo: govRepo,
		httpClient: &http.Client{
			Timeout: llmProbeTimeout,
		},
	}
}

// WithDocTypeRepo 注入 DocType 仓储，用于在删除模型前拦截仍被显式指定的配置。
func (s *LLMConfigService) WithDocTypeRepo(docTypeRepo DocTypeRepo) *LLMConfigService {
	s.docTypeRepo = docTypeRepo
	return s
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

type normalizedLLMParams struct {
	name             string
	baseURL          string
	modelName        string
	timeoutSeconds   int
	maxRetries       int
	maxTokensPerTask int
	maxFilesPerTask  int
}

func normalizeAndValidateLLMParams(req LLMConfigUpsertRequest) (normalizedLLMParams, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return normalizedLLMParams{}, fmt.Errorf("%w: 模型名称标识不能为空且不超过 80 个字符", ErrInvalidLLMParam)
	}

	baseURL, err := validateProbeBaseURL(req.BaseURL)
	if err != nil {
		return normalizedLLMParams{}, err
	}

	modelName := strings.TrimSpace(req.ModelName)
	if modelName == "" || utf8.RuneCountInString(modelName) > 128 {
		return normalizedLLMParams{}, fmt.Errorf("%w: Model Name 不能为空且不超过 128 个字符", ErrInvalidLLMParam)
	}

	timeoutSec := req.TimeoutSeconds
	if timeoutSec == 0 {
		timeoutSec = 180
	}
	if timeoutSec < 5 || timeoutSec > 900 {
		return normalizedLLMParams{}, fmt.Errorf("%w: timeout_seconds 须在 5~900 之间", ErrInvalidLLMParam)
	}

	if req.MaxRetries < 0 || req.MaxRetries > 10 {
		return normalizedLLMParams{}, fmt.Errorf("%w: max_retries 须在 0~10 之间", ErrInvalidLLMParam)
	}

	maxTokens := req.MaxTokensPerTask
	if maxTokens == 0 {
		maxTokens = 200000
	}
	if maxTokens < 0 {
		return normalizedLLMParams{}, fmt.Errorf("%w: max_tokens_per_task 必须大于 0", ErrInvalidLLMParam)
	}

	maxFiles := req.MaxFilesPerTask
	if maxFiles == 0 {
		maxFiles = 2000
	}
	if maxFiles < 0 {
		return normalizedLLMParams{}, fmt.Errorf("%w: max_files_per_task 必须大于 0", ErrInvalidLLMParam)
	}

	return normalizedLLMParams{
		name:             name,
		baseURL:          baseURL,
		modelName:        modelName,
		timeoutSeconds:   timeoutSec,
		maxRetries:       req.MaxRetries,
		maxTokensPerTask: maxTokens,
		maxFilesPerTask:  maxFiles,
	}, nil
}

func validateProbeBaseURL(raw string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if baseURL == "" {
		return "", fmt.Errorf("%w: base_url 不能为空", ErrInvalidLLMParam)
	}
	u, parseErr := url.Parse(baseURL)
	if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%w: base_url 必须为合法的 http/https 地址且不可内嵌凭据", ErrInvalidLLMParam)
	}
	return baseURL, nil
}

// CreateConfig 新增内网 OpenAI Compatible 模型配置。
// 若当前模型池为空且新模型处于启用状态，自动设为默认模型。
func (s *LLMConfigService) CreateConfig(
	ctx context.Context,
	req LLMConfigUpsertRequest,
	operatorID uint,
) (*LLMConfigVO, LLMConfigAuditSnapshot, error) {
	p, err := normalizeAndValidateLLMParams(req)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, err
	}

	if _, lookupErr := s.govRepo.GetLLMConfigByName(ctx, p.name); lookupErr == nil {
		return nil, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 模型名称 %q 已存在", ErrLLMConfigConflict, p.name)
	} else if !errors.Is(lookupErr, models.ErrNotFound) {
		return nil, LLMConfigAuditSnapshot{}, lookupErr
	}

	existing, err := s.govRepo.ListLLMConfigs(ctx)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, err
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	isDefault := false
	if req.IsDefault != nil {
		isDefault = *req.IsDefault
	} else if len(existing) == 0 && isActive {
		isDefault = true
	}
	if isDefault && !isActive {
		return nil, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 默认模型必须处于启用状态", ErrInvalidLLMParam)
	}

	trimmedKey := strings.TrimSpace(req.APIKey)
	opID := operatorID
	cfg := &models.LLMConfig{
		Name:             p.name,
		BaseURL:          p.baseURL,
		ModelName:        p.modelName,
		APIKey:           trimmedKey,
		APIKeyMasked:     security.MaskAPIKey(trimmedKey),
		TimeoutSeconds:   p.timeoutSeconds,
		MaxRetries:       p.maxRetries,
		MaxTokensPerTask: p.maxTokensPerTask,
		MaxFilesPerTask:  p.maxFilesPerTask,
		IsActive:         isActive,
		IsDefault:        isDefault,
		LastProbeStatus:  "unknown",
		UpdatedBy:        &opID,
	}

	if err := s.govRepo.CreateLLMConfig(ctx, cfg); err != nil {
		return nil, LLMConfigAuditSnapshot{}, err
	}

	vo := toLLMConfigVO(cfg)
	auditSnap := toLLMAuditSnapshot(cfg, trimmedKey != "")
	return &vo, auditSnap, nil
}

// UpdateConfig 更新指定模型配置，返回更新后的脱敏视图与用于审计 Diff 的新旧脱敏快照。
func (s *LLMConfigService) UpdateConfig(
	ctx context.Context,
	id uint,
	req LLMConfigUpsertRequest,
	operatorID uint,
) (*LLMConfigVO, LLMConfigAuditSnapshot, LLMConfigAuditSnapshot, error) {
	if id == 0 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 模型 ID 非法", ErrInvalidLLMParam)
	}

	p, err := normalizeAndValidateLLMParams(req)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}

	cfg, err := s.govRepo.GetLLMConfigByID(ctx, id)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}

	if other, lookupErr := s.govRepo.GetLLMConfigByName(ctx, p.name); lookupErr == nil && other.ID != cfg.ID {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 模型名称 %q 已被其他配置占用", ErrLLMConfigConflict, p.name)
	} else if lookupErr != nil && !errors.Is(lookupErr, models.ErrNotFound) {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, lookupErr
	}

	if p.name != cfg.Name && s.docTypeRepo != nil {
		refs, refErr := s.docTypeRepo.ListByPreferredModel(ctx, cfg.Name)
		if refErr != nil {
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, refErr
		}
		if len(refs) > 0 {
			slugs := make([]string, 0, len(refs))
			for _, dt := range refs {
				slugs = append(slugs, dt.Slug)
			}
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 模型名称正被文档类型 [%s] 显式指定，请先解除指定后再重命名", ErrLLMConfigConflict, strings.Join(slugs, ", "))
		}
	}

	nextActive := cfg.IsActive
	if req.IsActive != nil {
		nextActive = *req.IsActive
	}

	nextDefault := cfg.IsDefault
	if req.IsDefault != nil {
		if cfg.IsDefault && !*req.IsDefault {
			return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 不能直接取消默认模型标记，请将其他已启用模型设为默认", ErrInvalidLLMParam)
		}
		if *req.IsDefault {
			nextDefault = true
		}
	}

	if cfg.IsDefault && !nextActive {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 当前模型为系统默认模型，请先将其他模型设为默认后再停用", ErrInvalidLLMParam)
	}
	if nextDefault && !nextActive {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 默认模型必须处于启用状态", ErrInvalidLLMParam)
	}

	beforeAudit := toLLMAuditSnapshot(cfg, false)

	cfg.Name = p.name
	cfg.BaseURL = p.baseURL
	cfg.ModelName = p.modelName
	cfg.TimeoutSeconds = p.timeoutSeconds
	cfg.MaxRetries = p.maxRetries
	cfg.MaxTokensPerTask = p.maxTokensPerTask
	cfg.MaxFilesPerTask = p.maxFilesPerTask
	cfg.IsActive = nextActive
	cfg.IsDefault = nextDefault
	opID := operatorID
	cfg.UpdatedBy = &opID

	apiKeyChanged := false
	trimmedKey := strings.TrimSpace(req.APIKey)
	if req.ClearAPIKey {
		if cfg.APIKey != "" || cfg.APIKeyMasked != "" {
			apiKeyChanged = true
		}
		cfg.APIKey = ""
		cfg.APIKeyMasked = ""
	} else if trimmedKey != "" {
		cfg.APIKey = trimmedKey
		cfg.APIKeyMasked = security.MaskAPIKey(trimmedKey)
		apiKeyChanged = true
	}

	if err := s.govRepo.UpdateLLMConfig(ctx, cfg); err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}

	afterAudit := toLLMAuditSnapshot(cfg, apiKeyChanged)
	vo := toLLMConfigVO(cfg)
	return &vo, beforeAudit, afterAudit, nil
}

// SetDefaultConfig 手动将指定已启用模型切换为全局唯一默认模型。
func (s *LLMConfigService) SetDefaultConfig(
	ctx context.Context,
	id uint,
	operatorID uint,
) (*LLMConfigVO, LLMConfigAuditSnapshot, LLMConfigAuditSnapshot, error) {
	if id == 0 {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 模型 ID 非法", ErrInvalidLLMParam)
	}

	cfg, err := s.govRepo.GetLLMConfigByID(ctx, id)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}
	if !cfg.IsActive || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ModelName) == "" {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, fmt.Errorf("%w: 仅允许将已启用且配置完整的模型设为默认模型", ErrInvalidLLMParam)
	}

	beforeAudit := toLLMAuditSnapshot(cfg, false)
	updated, err := s.govRepo.SetDefaultLLMConfig(ctx, id)
	if err != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, err
	}
	opID := operatorID
	updated.UpdatedBy = &opID
	if saveErr := s.govRepo.UpdateLLMConfig(ctx, updated); saveErr != nil {
		return nil, LLMConfigAuditSnapshot{}, LLMConfigAuditSnapshot{}, saveErr
	}

	afterAudit := toLLMAuditSnapshot(updated, false)
	vo := toLLMConfigVO(updated)
	return &vo, beforeAudit, afterAudit, nil
}

// DeleteConfig 删除非唯一默认且未被 DocType 显式绑定的模型配置。
func (s *LLMConfigService) DeleteConfig(ctx context.Context, id uint) (*LLMConfigAuditSnapshot, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: 模型 ID 非法", ErrInvalidLLMParam)
	}

	cfg, err := s.govRepo.GetLLMConfigByID(ctx, id)
	if err != nil {
		return nil, err
	}

	all, err := s.govRepo.ListLLMConfigs(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.IsDefault && len(all) > 1 {
		return nil, fmt.Errorf("%w: 默认模型不可直接删除，请先将其他模型设为默认", ErrLLMConfigConflict)
	}

	if s.docTypeRepo != nil {
		refs, refErr := s.docTypeRepo.ListByPreferredModel(ctx, cfg.Name)
		if refErr != nil {
			return nil, refErr
		}
		if len(refs) > 0 {
			slugs := make([]string, 0, len(refs))
			for _, dt := range refs {
				slugs = append(slugs, dt.Slug)
			}
			return nil, fmt.Errorf("%w: 该模型正被文档类型 [%s] 显式指定，请先解除指定后再删除", ErrLLMConfigConflict, strings.Join(slugs, ", "))
		}
	}

	beforeAudit := toLLMAuditSnapshot(cfg, false)
	if err := s.govRepo.DeleteLLMConfig(ctx, id); err != nil {
		return nil, err
	}
	return &beforeAudit, nil
}

// ProbeConfig 对指定已保存模型发起 OpenAI Compatible /chat/completions 轻量探活。
func (s *LLMConfigService) ProbeConfig(ctx context.Context, id uint, override *ProbeLLMConfigRequest) (*LLMProbeResult, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: 模型 ID 非法", ErrInvalidLLMParam)
	}

	saved, err := s.govRepo.GetLLMConfigByID(ctx, id)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(saved.BaseURL), "/")
	modelName := strings.TrimSpace(saved.ModelName)
	apiKey := saved.APIKey
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

	return s.executeProbe(ctx, saved.ID, saved.Name, baseURL, modelName, apiKey, !isOverride)
}

// ProbeAdhoc 在表单新建或临时编辑尚未保存时执行连通性预探活。
func (s *LLMConfigService) ProbeAdhoc(ctx context.Context, req ProbeLLMConfigRequest) (*LLMProbeResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	modelName := strings.TrimSpace(req.ModelName)
	apiKey := strings.TrimSpace(req.APIKey)
	return s.executeProbe(ctx, 0, "", baseURL, modelName, apiKey, false)
}

func (s *LLMConfigService) executeProbe(
	ctx context.Context,
	modelID uint,
	label string,
	baseURL string,
	modelName string,
	apiKey string,
	persist bool,
) (*LLMProbeResult, error) {
	if baseURL == "" || modelName == "" {
		return &LLMProbeResult{
			ID:           modelID,
			Name:         label,
			Reachable:    false,
			ErrorMessage: "请先配置合法的 BaseURL 与模型名称",
		}, nil
	}

	if _, err := validateProbeBaseURL(baseURL); err != nil {
		return &LLMProbeResult{
			ID:           modelID,
			Name:         label,
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
		ID:        modelID,
		Name:      label,
		LatencyMs: latencyMs,
	}

	now := time.Now()
	if reqErr != nil {
		result.Reachable = false
		result.ErrorMessage = fmt.Sprintf("模型服务不可达: %v", reqErr)
		if persist && modelID > 0 {
			s.recordProbeStatus(ctx, modelID, "failed", latencyMs, now)
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
		if persist && modelID > 0 {
			s.recordProbeStatus(ctx, modelID, "failed", latencyMs, now)
		}
		return result, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Reachable = false
		result.ErrorMessage = fmt.Sprintf("模型接口返回 HTTP %d", resp.StatusCode)
		if persist && modelID > 0 {
			s.recordProbeStatus(ctx, modelID, "failed", latencyMs, now)
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
	if persist && modelID > 0 {
		s.recordProbeStatus(ctx, modelID, "ok", latencyMs, now)
	}
	return result, nil
}

func (s *LLMConfigService) recordProbeStatus(ctx context.Context, id uint, status string, latencyMs int64, probedAt time.Time) {
	if err := s.govRepo.UpdateLLMProbeResult(ctx, id, status, latencyMs, probedAt); err != nil {
		log.Printf("[Code-Archdoc/LLM] 更新探活状态失败 (id=%d, status=%s): %v", id, status, err)
	}
}

// ResolveLLMEndpoint 严格按「显式指定 preferredModel -> 全局默认模型」解析唯一可用端点，不做静默自动回退。
func ResolveLLMEndpoint(ctx context.Context, govRepo GovernanceRepo, preferredModel string) (llm.EndpointConfig, *models.LLMConfig, error) {
	if govRepo == nil {
		return llm.EndpointConfig{}, nil, ErrChatLLMNotConfigured
	}

	pref := strings.TrimSpace(preferredModel)
	var (
		cfg *models.LLMConfig
		err error
	)
	if pref != "" {
		cfg, err = govRepo.GetLLMConfigByName(ctx, pref)
		if errors.Is(err, models.ErrNotFound) {
			return llm.EndpointConfig{}, nil, fmt.Errorf("%w: 文档类型指定的模型 %q 不存在，请管理员在「LLM 模型配置」中核对", ErrChatLLMNotConfigured, pref)
		}
		if err != nil {
			return llm.EndpointConfig{}, nil, fmt.Errorf("读取指定 LLM 模型 %q 配置失败: %w", pref, err)
		}
		if !cfg.IsActive || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ModelName) == "" {
			return llm.EndpointConfig{}, nil, fmt.Errorf("%w: 文档类型指定的模型 %q 已停用或配置不完整", ErrChatLLMNotConfigured, pref)
		}
	} else {
		cfg, err = govRepo.GetDefaultLLMConfig(ctx)
		if errors.Is(err, models.ErrNotFound) {
			return llm.EndpointConfig{}, nil, fmt.Errorf("%w: 尚未配置系统默认 LLM 模型，请先在「LLM 模型配置」中新增并设为默认", ErrChatLLMNotConfigured)
		}
		if err != nil {
			return llm.EndpointConfig{}, nil, fmt.Errorf("读取默认 LLM 模型配置失败: %w", err)
		}
		if !cfg.IsActive || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ModelName) == "" {
			return llm.EndpointConfig{}, nil, fmt.Errorf("%w: 当前系统默认模型 %q 已停用或配置不完整", ErrChatLLMNotConfigured, DisplayModelLabel(cfg))
		}
	}

	return llm.EndpointConfig{
		BaseURL:        cfg.BaseURL,
		ModelName:      cfg.ModelName,
		APIKey:         cfg.APIKey,
		TimeoutSeconds: cfg.TimeoutSeconds,
		MaxRetries:     cfg.MaxRetries,
	}, cfg, nil
}

// DisplayModelLabel 返回模型展示标识（优先自定义名称，回退底层 model_name）。
func DisplayModelLabel(cfg *models.LLMConfig) string {
	if cfg == nil {
		return ""
	}
	if name := strings.TrimSpace(cfg.Name); name != "" {
		return name
	}
	return strings.TrimSpace(cfg.ModelName)
}

func toLLMConfigVO(cfg *models.LLMConfig) LLMConfigVO {
	return LLMConfigVO{
		ID:                 cfg.ID,
		Name:               cfg.Name,
		BaseURL:            cfg.BaseURL,
		ModelName:          cfg.ModelName,
		APIKeyMasked:       cfg.APIKeyMasked,
		HasAPIKey:          cfg.APIKey != "",
		TimeoutSeconds:     cfg.TimeoutSeconds,
		MaxRetries:         cfg.MaxRetries,
		MaxTokensPerTask:   cfg.MaxTokensPerTask,
		MaxFilesPerTask:    cfg.MaxFilesPerTask,
		IsActive:           cfg.IsActive,
		IsDefault:          cfg.IsDefault,
		LastProbeStatus:    cfg.LastProbeStatus,
		LastProbeLatencyMs: cfg.LastProbeLatencyMs,
		LastProbedAt:       cfg.LastProbedAt,
	}
}

func toLLMAuditSnapshot(cfg *models.LLMConfig, apiKeyChanged bool) LLMConfigAuditSnapshot {
	return LLMConfigAuditSnapshot{
		ID:               cfg.ID,
		Name:             cfg.Name,
		BaseURL:          cfg.BaseURL,
		ModelName:        cfg.ModelName,
		APIKeyMasked:     cfg.APIKeyMasked,
		HasAPIKey:        cfg.APIKey != "",
		APIKeyChanged:    apiKeyChanged,
		TimeoutSeconds:   cfg.TimeoutSeconds,
		MaxRetries:       cfg.MaxRetries,
		MaxTokensPerTask: cfg.MaxTokensPerTask,
		MaxFilesPerTask:  cfg.MaxFilesPerTask,
		IsActive:         cfg.IsActive,
		IsDefault:        cfg.IsDefault,
	}
}
