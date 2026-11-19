package codeir

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"code-archdoc/pkg/security"

	"gopkg.in/yaml.v3"
)

// ConfigExtractor 解析 YAML/JSON/INI/TOML/ENV/Properties 配置键并强制通过 SecretRedactor 三级脱敏。
type ConfigExtractor struct {
	redactor *security.SecretRedactor
}

func NewConfigExtractor() *ConfigExtractor {
	return &ConfigExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *ConfigExtractor) Name() string { return "config-redactor" }

func (e *ConfigExtractor) SupportedLanguages() []string { return []string{"config"} }

func (e *ConfigExtractor) ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	base := strings.ToLower(path.Base(file.RelPath))
	if base == "compile_commands.json" {
		return &ExtractionResult{}, nil
	}

	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".yaml", ".yml":
		return e.extractYAML(file.RelPath, content), nil
	case ".json":
		return e.extractJSON(file.RelPath, content), nil
	case ".env", ".properties", ".ini", ".conf", ".toml":
		return e.extractKeyValueLines(file.RelPath, string(content)), nil
	default:
		return &ExtractionResult{}, nil
	}
}

func (e *ConfigExtractor) extractYAML(filePath string, content []byte) *ExtractionResult {
	res := &ExtractionResult{}
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		res.Diagnostics = append(res.Diagnostics, ParseDiagnostic{
			FilePath: filePath,
			Message:  fmt.Sprintf("解析 YAML 配置失败: %v", err),
		})
		return res
	}
	if len(root.Content) == 0 {
		return res
	}
	e.walkYAMLNode(filePath, "", root.Content[0], &res.ConfigKeys)
	sort.Slice(res.ConfigKeys, func(i, j int) bool {
		return res.ConfigKeys[i].KeyPath < res.ConfigKeys[j].KeyPath
	})
	return res
}

func (e *ConfigExtractor) walkYAMLNode(filePath, prefix string, node *yaml.Node, out *[]ConfigKeyFact) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			kNode := node.Content[i]
			vNode := node.Content[i+1]
			keyName := strings.TrimSpace(kNode.Value)
			fullKey := keyName
			if prefix != "" {
				fullKey = prefix + "." + keyName
			}
			if vNode.Kind == yaml.MappingNode {
				e.walkYAMLNode(filePath, fullKey, vNode, out)
			} else if vNode.Kind == yaml.SequenceNode {
				*out = append(*out, e.buildConfigFact(filePath, fullKey, "list", fmt.Sprintf("[%d items]", len(vNode.Content)), kNode.Line))
			} else {
				valType := inferYAMLType(vNode)
				*out = append(*out, e.buildConfigFact(filePath, fullKey, valType, vNode.Value, kNode.Line))
			}
		}
	case yaml.DocumentNode:
		for _, child := range node.Content {
			e.walkYAMLNode(filePath, prefix, child, out)
		}
	}
}

func (e *ConfigExtractor) extractJSON(filePath string, content []byte) *ExtractionResult {
	res := &ExtractionResult{}
	var obj map[string]any
	if err := json.Unmarshal(content, &obj); err != nil {
		// 非对象顶层（例如 JSON 数组）跳过不报错
		return res
	}
	e.walkMap(filePath, "", obj, &res.ConfigKeys)
	sort.Slice(res.ConfigKeys, func(i, j int) bool {
		return res.ConfigKeys[i].KeyPath < res.ConfigKeys[j].KeyPath
	})
	return res
}

func (e *ConfigExtractor) walkMap(filePath, prefix string, m map[string]any, out *[]ConfigKeyFact) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}
		v := m[k]
		switch tv := v.(type) {
		case map[string]any:
			e.walkMap(filePath, fullKey, tv, out)
		case []any:
			*out = append(*out, e.buildConfigFact(filePath, fullKey, "list", fmt.Sprintf("[%d items]", len(tv)), 1))
		case bool:
			*out = append(*out, e.buildConfigFact(filePath, fullKey, "bool", strconv.FormatBool(tv), 1))
		case float64:
			valType := "float"
			if tv == float64(int64(tv)) {
				valType = "int"
			}
			*out = append(*out, e.buildConfigFact(filePath, fullKey, valType, strconv.FormatFloat(tv, 'f', -1, 64), 1))
		case string:
			*out = append(*out, e.buildConfigFact(filePath, fullKey, "string", tv, 1))
		case nil:
			*out = append(*out, e.buildConfigFact(filePath, fullKey, "string", "", 1))
		}
	}
}

func (e *ConfigExtractor) extractKeyValueLines(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{}
	section := ""
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		sepIdx := strings.IndexAny(line, "=:")
		if sepIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:sepIdx])
		val := strings.TrimSpace(line[sepIdx+1:])
		val = strings.Trim(val, `"'`)
		fullKey := key
		if section != "" {
			fullKey = section + "." + key
		}
		envBinding := ""
		if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
			envBinding = strings.TrimSuffix(strings.TrimPrefix(val, "${"), "}")
		}
		fact := e.buildConfigFact(filePath, fullKey, inferScalarType(val), val, i+1)
		fact.EnvVarBinding = envBinding
		res.ConfigKeys = append(res.ConfigKeys, fact)
	}
	return res
}

func (e *ConfigExtractor) buildConfigFact(filePath, keyPath, valType, rawVal string, line int) ConfigKeyFact {
	if line <= 0 {
		line = 1
	}
	safeVal, isSensitive, reason := e.redactor.RedactConfigValue(keyPath, rawVal)
	// 二次防御：对安全值也运行文本扫描，彻底保证 0 密钥残留
	safeVal, findings := e.redactor.RedactText(safeVal)
	if len(findings) > 0 {
		isSensitive = true
		if reason == "" {
			reason = findings[0].RuleID
		}
	}

	envBinding := ""
	trimmedRaw := strings.TrimSpace(rawVal)
	if strings.HasPrefix(trimmedRaw, "${") && strings.HasSuffix(trimmedRaw, "}") {
		envBinding = strings.TrimSuffix(strings.TrimPrefix(trimmedRaw, "${"), "}")
	}

	return ConfigKeyFact{
		KeyPath:        keyPath,
		ValueType:      valType,
		SafeDefault:    safeVal,
		IsSensitive:    isSensitive,
		RedactedReason: reason,
		SourceFile:     filePath,
		EnvVarBinding:  envBinding,
		Meta: FactMeta{
			Confidence: ConfidenceExact,
			Span: SourceSpan{
				FilePath:  filePath,
				LineStart: line,
				LineEnd:   line,
			},
			ContentFingerprint: fingerprintString(keyPath + ":" + valType + ":" + safeVal),
		},
	}
}

func inferYAMLType(n *yaml.Node) string {
	switch n.Tag {
	case "!!int":
		return "int"
	case "!!float":
		return "float"
	case "!!bool":
		return "bool"
	default:
		return inferScalarType(n.Value)
	}
}

func inferScalarType(val string) string {
	low := strings.ToLower(strings.TrimSpace(val))
	if low == "true" || low == "false" {
		return "bool"
	}
	if _, err := strconv.ParseInt(low, 10, 64); err == nil {
		return "int"
	}
	if _, err := strconv.ParseFloat(low, 64); err == nil {
		return "float"
	}
	return "string"
}
