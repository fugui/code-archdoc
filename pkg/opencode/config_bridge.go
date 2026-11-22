package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadOnlyAgentName OpenCode 隔离配置文件中声明的强制只读文档反推 Agent 名称。
const ReadOnlyAgentName = "archdoc-readonly-docgen"

type sessionEnv struct {
	RootDir  string
	ModelArg string
	EnvList  []string
}

func (s *sessionEnv) Cleanup() {
	if s != nil && s.RootDir != "" {
		_ = os.RemoveAll(s.RootDir)
	}
}

func prepareSessionEnv(req DocGenRequest) (*sessionEnv, error) {
	rootDir, err := os.MkdirTemp("", "archdoc-oc-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时隔离目录失败: %w", err)
	}
	if chmodErr := os.Chmod(rootDir, 0o700); chmodErr != nil {
		_ = os.RemoveAll(rootDir)
		return nil, fmt.Errorf("设置隔离目录权限 0700 失败: %w", chmodErr)
	}

	cleanupOnErr := func() {
		_ = os.RemoveAll(rootDir)
	}

	xdgConfig := filepath.Join(rootDir, "xdg_config")
	xdgData := filepath.Join(rootDir, "xdg_data")
	xdgState := filepath.Join(rootDir, "xdg_state")
	xdgCache := filepath.Join(rootDir, "xdg_cache")
	for _, dir := range []string{
		filepath.Join(xdgConfig, "opencode"),
		filepath.Join(xdgData, "opencode"),
		xdgState,
		xdgCache,
	} {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			cleanupOnErr()
			return nil, fmt.Errorf("创建 XDG 子目录 %s 失败: %w", dir, mkErr)
		}
	}

	overrideModel := strings.TrimSpace(req.OpenCodeModelOverride)
	modelArg := overrideModel
	isModeA := overrideModel == ""

	if isModeA {
		mName := strings.TrimSpace(req.ModelName)
		bURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
		if mName == "" || bURL == "" {
			cleanupOnErr()
			return nil, fmt.Errorf("模式 A 桥接配置缺少 BaseURL 或 ModelName")
		}
		modelArg = "archdoc_ep/" + mName
	} else {
		// 模式 B：若宿主机存在 auth.json，以 0600 权限拷贝进隔离 XDG_DATA_HOME（不修改宿主机）
		copyHostOpenCodeAuth(filepath.Join(xdgData, "opencode", "auth.json"))
	}

	cfgBytes, err := buildOpenCodeConfigJSON(req, isModeA)
	if err != nil {
		cleanupOnErr()
		return nil, err
	}

	cfgPath := filepath.Join(rootDir, "opencode.json")
	if err := writeSecureFile(cfgPath, cfgBytes, 0o600); err != nil {
		cleanupOnErr()
		return nil, fmt.Errorf("写入隔离 opencode.json 失败: %w", err)
	}

	envList := buildIsolatedEnv(cfgPath, xdgConfig, xdgData, xdgState, xdgCache)
	return &sessionEnv{
		RootDir:  rootDir,
		ModelArg: modelArg,
		EnvList:  envList,
	}, nil
}

func buildOpenCodeConfigJSON(req DocGenRequest, isModeA bool) ([]byte, error) {
	root := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"agent": map[string]any{
			ReadOnlyAgentName: map[string]any{
				"description": "Code-Archdoc Read-Only Documentation Reverse-Engineering Agent",
				"tools": map[string]bool{
					"read":     true,
					"grep":     true,
					"glob":     true,
					"list":     true,
					"edit":     false,
					"write":    false,
					"patch":    false,
					"bash":     false,
					"webfetch": false,
				},
				"permission": map[string]string{
					"*":                  "deny",
					"read":               "allow",
					"grep":               "allow",
					"glob":               "allow",
					"list":               "allow",
					"edit":               "deny",
					"write":              "deny",
					"patch":              "deny",
					"bash":               "deny",
					"webfetch":           "deny",
					"external_directory": "deny",
				},
			},
		},
	}

	if isModeA {
		mName := strings.TrimSpace(req.ModelName)
		bURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
		options := map[string]any{
			"baseURL": bURL,
		}
		if key := strings.TrimSpace(req.APIKey); key != "" {
			options["apiKey"] = key
		}
		if ua := strings.TrimSpace(req.UserAgent); ua != "" {
			options["headers"] = map[string]string{
				"User-Agent": ua,
			}
		}
		root["provider"] = map[string]any{
			"archdoc_ep": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"name":    "Code-Archdoc Internal Endpoint",
				"options": options,
				"models": map[string]any{
					mName: map[string]any{
						"name": mName,
					},
				},
			},
		}
	}

	return json.MarshalIndent(root, "", "  ")
}

func copyHostOpenCodeAuth(destPath string) {
	var srcPath string
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		cand := filepath.Join(xdgData, "opencode", "auth.json")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			srcPath = cand
		}
	}
	if srcPath == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			cand := filepath.Join(home, ".local", "share", "opencode", "auth.json")
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				srcPath = cand
			}
		}
	}
	if srcPath == "" {
		return
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return
	}
	_ = writeSecureFile(destPath, data, 0o600)
}

func writeSecureFile(path string, content []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if chmodErr := f.Chmod(perm); chmodErr != nil {
		_ = f.Close()
		return chmodErr
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

var baseEnvWhitelist = []string{
	"PATH",
	"HOME",
	"USER",
	"LANG",
	"LC_ALL",
	"TMPDIR",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

func buildProbeEnv() []string {
	env := make([]string, 0, len(baseEnvWhitelist)+3)
	for _, k := range baseEnvWhitelist {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "CI=true", "NO_COLOR=1", "TERM=dumb")
	return env
}

func buildIsolatedEnv(cfgPath, xdgConfig, xdgData, xdgState, xdgCache string) []string {
	env := make([]string, 0, len(baseEnvWhitelist)+9)
	for _, k := range baseEnvWhitelist {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env,
		"OPENCODE_CONFIG="+cfgPath,
		"OPENCODE_CONFIG_DIR="+filepath.Join(xdgConfig, "opencode"),
		"XDG_CONFIG_HOME="+xdgConfig,
		"XDG_DATA_HOME="+xdgData,
		"XDG_STATE_HOME="+xdgState,
		"XDG_CACHE_HOME="+xdgCache,
		"CI=true",
		"NO_COLOR=1",
		"TERM=dumb",
	)
	return env
}

// SanitizeArgsForLog 脱敏 OpenCode 命令行参数与可选凭据文本，严禁在日志中输出明文 APIKey 或超长 Prompt。
func SanitizeArgsForLog(args []string, apiKey string) string {
	out := make([]string, len(args))
	copy(out, args)
	for i := range out {
		if i > 0 && out[i-1] == "run" && len(out[i]) > 120 {
			out[i] = fmt.Sprintf("<prompt:%d-bytes>", len(out[i]))
		}
		if apiKey != "" && strings.Contains(out[i], apiKey) {
			out[i] = strings.ReplaceAll(out[i], apiKey, "sk-****")
		}
	}
	return strings.Join(out, " ")
}
