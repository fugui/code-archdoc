package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code-archdoc/config"
	"code-archdoc/models"
)

type stubGovernanceRepo struct {
	configs    map[uint]models.LLMConfig
	defaultCfg *models.LLMConfig
}

func (s *stubGovernanceRepo) ListLLMConfigs(context.Context) ([]models.LLMConfig, error) {
	return nil, nil
}

func (s *stubGovernanceRepo) GetLLMConfigByID(_ context.Context, id uint) (*models.LLMConfig, error) {
	if cfg, ok := s.configs[id]; ok {
		cp := cfg
		return &cp, nil
	}
	return nil, models.ErrNotFound
}

func (s *stubGovernanceRepo) GetLLMConfigByName(context.Context, string) (*models.LLMConfig, error) {
	return nil, models.ErrNotFound
}

func (s *stubGovernanceRepo) GetDefaultLLMConfig(context.Context) (*models.LLMConfig, error) {
	if s.defaultCfg == nil {
		return nil, models.ErrNotFound
	}
	cp := *s.defaultCfg
	return &cp, nil
}

func (s *stubGovernanceRepo) CreateLLMConfig(context.Context, *models.LLMConfig) error { return nil }
func (s *stubGovernanceRepo) UpdateLLMConfig(context.Context, *models.LLMConfig) error { return nil }
func (s *stubGovernanceRepo) SetDefaultLLMConfig(context.Context, uint) (*models.LLMConfig, error) {
	return nil, models.ErrNotFound
}
func (s *stubGovernanceRepo) DeleteLLMConfig(context.Context, uint) error { return nil }
func (s *stubGovernanceRepo) UpdateLLMProbeResult(context.Context, uint, string, int64, time.Time) error {
	return nil
}
func (s *stubGovernanceRepo) RecordLLMUsage(context.Context, *models.LLMUsageLog) error { return nil }
func (s *stubGovernanceRepo) RecordQualityMetric(context.Context, *models.QualityMetric) error {
	return nil
}
func (s *stubGovernanceRepo) ListQualityMetrics(context.Context, models.MetricsFilter) ([]models.QualityMetric, error) {
	return nil, nil
}
func (s *stubGovernanceRepo) ListLLMUsageLogs(context.Context, models.MetricsFilter) ([]models.LLMUsageLog, error) {
	return nil, nil
}
func (s *stubGovernanceRepo) ListSectionVerifierSummaries(context.Context, string, *uint, time.Time) ([]models.SectionVerifierMetricRow, error) {
	return nil, nil
}

func TestLLMConfigServiceUserAgentProbeAndResolve(t *testing.T) {
	origVersion := config.AppVersion
	origCfg := config.AppConfig
	t.Cleanup(func() {
		config.AppVersion = origVersion
		config.AppConfig = origCfg
	})

	uaCh := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uaCh <- r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"mock-v1","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	savedCfg := models.LLMConfig{
		ID:        1,
		Name:      "default-model",
		BaseURL:   srv.URL,
		ModelName: "mock-v1",
		IsActive:  true,
		IsDefault: true,
	}
	repo := &stubGovernanceRepo{
		configs:    map[uint]models.LLMConfig{1: savedCfg},
		defaultCfg: &savedCfg,
	}

	// 1. 未配置 llm.user_agent 时，使用 Code-Archdoc/<version>
	config.AppVersion = "v1.0.0"
	config.AppConfig.LLM.UserAgent = ""
	svc := NewLLMConfigService(repo)

	res, err := svc.ProbeAdhoc(context.Background(), ProbeLLMConfigRequest{
		BaseURL:   srv.URL,
		ModelName: "mock-v1",
	})
	if err != nil || !res.Reachable {
		t.Fatalf("ProbeAdhoc 失败: res=%+v err=%v", res, err)
	}
	if got := <-uaCh; got != "Code-Archdoc/1.0.0" {
		t.Fatalf("ProbeAdhoc 默认 User-Agent 期望 Code-Archdoc/1.0.0，实际 %q", got)
	}

	ep, _, err := ResolveLLMEndpoint(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("ResolveLLMEndpoint 失败: %v", err)
	}
	if ep.UserAgent != "Code-Archdoc/1.0.0" {
		t.Fatalf("ResolveLLMEndpoint 默认 User-Agent 期望 Code-Archdoc/1.0.0，实际 %q", ep.UserAgent)
	}

	// 2. 全局 config.AppConfig.LLM.UserAgent 配置生效
	config.AppConfig.LLM.UserAgent = "MyOrg-Archdoc/2.0"
	res, err = svc.ProbeConfig(context.Background(), 1, nil)
	if err != nil || !res.Reachable {
		t.Fatalf("ProbeConfig 失败: res=%+v err=%v", res, err)
	}
	if got := <-uaCh; got != "MyOrg-Archdoc/2.0" {
		t.Fatalf("ProbeConfig 期望全局 User-Agent MyOrg-Archdoc/2.0，实际 %q", got)
	}

	ep, _, err = ResolveLLMEndpoint(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("ResolveLLMEndpoint 失败: %v", err)
	}
	if ep.UserAgent != "MyOrg-Archdoc/2.0" {
		t.Fatalf("ResolveLLMEndpoint 期望 MyOrg-Archdoc/2.0，实际 %q", ep.UserAgent)
	}

	// 3. WithUserAgent 显式覆盖 Probe 请求头
	svcWithUA := NewLLMConfigService(repo).WithUserAgent("MyOrg-Probe/3.0")
	res, err = svcWithUA.ProbeAdhoc(context.Background(), ProbeLLMConfigRequest{
		BaseURL:   srv.URL,
		ModelName: "mock-v1",
	})
	if err != nil || !res.Reachable {
		t.Fatalf("WithUserAgent ProbeAdhoc 失败: res=%+v err=%v", res, err)
	}
	if got := <-uaCh; got != "MyOrg-Probe/3.0" {
		t.Fatalf("WithUserAgent 期望 MyOrg-Probe/3.0，实际 %q", got)
	}
}
