package doctype

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/sandbox"
)

var (
	// ErrInvalidSlug 表示 DocType 目录名不满足白名单规范（对应 HTTP 400 INVALID_SLUG）。
	ErrInvalidSlug = errors.New("DocType slug 非法：须匹配 ^[a-z0-9][a-z0-9_-]{1,62}$")
	// ErrInvalidMeta 表示 meta.json 字段校验失败。
	ErrInvalidMeta = errors.New("DocType meta.json 校验失败")
	// ErrInvalidTemplate 表示 template.md 缺少声明槽位标记。
	ErrInvalidTemplate = errors.New("DocType template.md 槽位标记不一致")
	// ErrDocTypeNotFound 表示目标 DocType 目录不存在。
	ErrDocTypeNotFound = errors.New("DocType 目录不存在")
	// ErrDocTypeExists 表示目标 DocType 目录已存在（对应 HTTP 409 CONFLICT）。
	ErrDocTypeExists = errors.New("DocType slug 已存在")

	SlugPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,62}$`)
	SlotIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,48}$`)
)

// ValidateSlug 校验 DocType 目录名是否满足安全白名单正则。
func ValidateSlug(slug string) error {
	if !SlugPattern.MatchString(slug) {
		return fmt.Errorf("%w: %q", ErrInvalidSlug, slug)
	}
	return nil
}

// ScopeProfileConfig 对应 meta.json 中的 Layer 3 范围裁剪策略（扩展 P1 include_legacy_docs 钩子）。
type ScopeProfileConfig struct {
	IncludeGlobs      []string `json:"include_globs,omitempty"`
	ExcludeGlobs      []string `json:"exclude_globs,omitempty"`
	MaxDepth          int      `json:"max_depth,omitempty"`
	IncludeTests      bool     `json:"include_tests"`
	IncludeLegacyDocs bool     `json:"include_legacy_docs,omitempty"`
}

// ToSandboxProfile 转换为 pkg/sandbox.ScopeProfile 供文件过滤使用。
func (s *ScopeProfileConfig) ToSandboxProfile() *sandbox.ScopeProfile {
	if s == nil {
		return nil
	}
	return &sandbox.ScopeProfile{
		IncludeGlobs: s.IncludeGlobs,
		ExcludeGlobs: s.ExcludeGlobs,
		MaxDepth:     s.MaxDepth,
		IncludeTests: s.IncludeTests,
	}
}

// SlotSpec 对应 meta.json 中的单个稳定章节槽位定义。
type SlotSpec struct {
	SlotID            string   `json:"slot_id"`
	Title             string   `json:"title"`
	Ownership         string   `json:"ownership"` // auto | hybrid | locked
	Required          bool     `json:"required"`
	IRFacets          []string `json:"ir_facets"`
	TokenBudget       int      `json:"token_budget,omitempty"`
	MinChars          int      `json:"min_chars,omitempty"`
	InstructionHint   string   `json:"instruction_hint,omitempty"`
	EnableSourceTools bool     `json:"enable_source_tools,omitempty"`
	MaxToolRounds     int      `json:"max_tool_rounds,omitempty"`
}

// VerifyPolicy 对应 meta.json 中的 Verifier 四重门禁策略配置。
type VerifyPolicy struct {
	StrictSymbolCheck   bool     `json:"strict_symbol_check"`
	RequireMermaid      bool     `json:"require_mermaid"`
	AllowedMermaidTypes []string `json:"allowed_mermaid_types,omitempty"`
	MaxMermaidNodes     int      `json:"max_mermaid_nodes,omitempty"`
	MaxMermaidEdges     int      `json:"max_mermaid_edges,omitempty"`
	BlockOnSecretLeak   *bool    `json:"block_on_secret_leak,omitempty"`
}

// EffectiveVerifyPolicy 返回填充全部默认值后的有效门禁策略。
func (p *VerifyPolicy) Effective() VerifyPolicy {
	out := VerifyPolicy{
		AllowedMermaidTypes: []string{
			"flowchart", "graph", "sequenceDiagram", "stateDiagram-v2", "stateDiagram", "classDiagram", "erDiagram",
		},
		MaxMermaidNodes: 35,
		MaxMermaidEdges: 60,
	}
	blockDefault := true
	out.BlockOnSecretLeak = &blockDefault

	if p == nil {
		return out
	}
	out.StrictSymbolCheck = p.StrictSymbolCheck
	out.RequireMermaid = p.RequireMermaid
	if len(p.AllowedMermaidTypes) > 0 {
		out.AllowedMermaidTypes = append([]string(nil), p.AllowedMermaidTypes...)
	}
	if p.MaxMermaidNodes > 0 {
		out.MaxMermaidNodes = p.MaxMermaidNodes
	}
	if p.MaxMermaidEdges > 0 {
		out.MaxMermaidEdges = p.MaxMermaidEdges
	}
	if p.BlockOnSecretLeak != nil {
		v := *p.BlockOnSecretLeak
		out.BlockOnSecretLeak = &v
	}
	return out
}

// ModuleDigestSourceSpec 声明整仓 Roll-up 时从各活跃子模块提取摘要的数据源规范（ADR-04 D-4）。
type ModuleDigestSourceSpec struct {
	DocTypeSlug   string   `json:"doc_type_slug"`
	SlotID        string   `json:"slot_id"`
	MaxChars      int      `json:"max_chars,omitempty"`
	FallbackOrder []string `json:"fallback_order,omitempty"` // 默认 ["approved", "draft", "glossary_fallback"]
}

// Meta 描述 DocType meta.json 完整声明式配置，并通过 ExtraFields 无损保留所有未知扩展键。
type Meta struct {
	Name               string                     `json:"name"`
	DisplayName        string                     `json:"display_name"`
	ShortName          string                     `json:"short_name,omitempty"`
	Category           string                     `json:"category,omitempty"`
	Description        string                     `json:"description,omitempty"`
	IsActive           bool                       `json:"is_active"`
	IsBuiltin          bool                       `json:"is_builtin"`
	PreferredModel     string                     `json:"preferred_model,omitempty"`
	TimeoutSeconds     int                        `json:"timeout_seconds,omitempty"`
	MaxOutputTokens    int                        `json:"max_output_tokens,omitempty"`
	ScopeProfile       *ScopeProfileConfig        `json:"scope_profile,omitempty"`
	IRFacets           []string                   `json:"ir_facets"`
	ContextInputs      []string                   `json:"context_inputs,omitempty"`
	RepoIRFacets       []string                   `json:"repo_ir_facets,omitempty"`
	ModuleDigestSource *ModuleDigestSourceSpec    `json:"module_digest_source,omitempty"`
	Slots              []SlotSpec                 `json:"slots"`
	VerifyPolicy       *VerifyPolicy              `json:"verify_policy,omitempty"`
	ExtraFields        map[string]json.RawMessage `json:"-"`
}

var knownMetaKeys = map[string]bool{
	"name":                 true,
	"display_name":         true,
	"short_name":           true,
	"category":             true,
	"description":          true,
	"is_active":            true,
	"is_builtin":           true,
	"model_tier":           true,
	"preferred_model":      true,
	"timeout_seconds":      true,
	"max_output_tokens":    true,
	"scope_profile":        true,
	"ir_facets":            true,
	"context_inputs":       true,
	"repo_ir_facets":       true,
	"module_digest_source": true,
	"slots":                true,
	"verify_policy":        true,
}

var builtinShortNames = map[string]string{
	"system-architecture": "系统架构",
	"data-and-contract":   "数据与契约",
	"code-walkthrough":    "代码导读",
	"ops-and-config":      "运维与配置",
	"agents-context":      "Agent 上下文",
}

var builtinCategories = map[string]string{
	"system-architecture": "架构设计",
	"data-and-contract":   "架构设计",
	"code-walkthrough":    "研发指南",
	"ops-and-config":      "运维交付",
	"agents-context":      "AI 上下文",
}

// EffectiveShortName 解析文档类型短标题：优先显式 short_name，其次内置默认短名，最后回退 display_name。
func EffectiveShortName(slug, shortName, displayName string) string {
	if trimmed := strings.TrimSpace(shortName); trimmed != "" {
		return trimmed
	}
	if def, ok := builtinShortNames[strings.TrimSpace(slug)]; ok {
		return def
	}
	return strings.TrimSpace(displayName)
}

// EffectiveCategory 解析文档类型分组：优先显式 category，其次内置默认分组，否则返回空串。
func EffectiveCategory(slug, category string) string {
	if trimmed := strings.TrimSpace(category); trimmed != "" {
		return trimmed
	}
	if def, ok := builtinCategories[strings.TrimSpace(slug)]; ok {
		return def
	}
	return ""
}

type metaAlias Meta

// UnmarshalJSON 双轨解码：强类型解析已知标准键，其余未知扩展键完整留存于 ExtraFields。
func (m *Meta) UnmarshalJSON(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return err
	}

	var alias metaAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*m = Meta(alias)

	var extras map[string]json.RawMessage
	for k, v := range rawMap {
		if !knownMetaKeys[k] {
			if extras == nil {
				extras = make(map[string]json.RawMessage)
			}
			cp := make(json.RawMessage, len(v))
			copy(cp, v)
			extras[k] = cp
		}
	}
	m.ExtraFields = extras
	m.applyDefaults()
	return nil
}

// MarshalJSON 双轨编码：先注入 ExtraFields，再覆写标准已知键，按字典序稳定序列化输出。
func (m Meta) MarshalJSON() ([]byte, error) {
	m.applyDefaults()

	merged := make(map[string]json.RawMessage, len(m.ExtraFields)+12)
	for k, v := range m.ExtraFields {
		if !knownMetaKeys[k] {
			merged[k] = v
		}
	}

	alias := metaAlias(m)
	knownBytes, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	var knownMap map[string]json.RawMessage
	if err := json.Unmarshal(knownBytes, &knownMap); err != nil {
		return nil, err
	}
	for k, v := range knownMap {
		merged[k] = v
	}

	// 按字典序稳定编码，确保同内容跨平台产生完全一致的字节与哈希
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyBytes, _ := json.Marshal(k)
		buf.Write(keyBytes)
		buf.WriteByte(':')
		buf.Write(merged[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// MarshalPrettyJSON 序列化为带 2 空格缩进的稳定格式化 JSON（用于写盘）。
func (m Meta) MarshalPrettyJSON() ([]byte, error) {
	compact, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func (m *Meta) applyDefaults() {
	if m.TimeoutSeconds <= 0 {
		m.TimeoutSeconds = 300
	}
	for i := range m.Slots {
		if m.Slots[i].TokenBudget <= 0 {
			m.Slots[i].TokenBudget = 8000
		}
		if m.Slots[i].MinChars <= 0 {
			m.Slots[i].MinChars = 40
		}
	}
}

// Validate 校验 meta.json 的必填字段、枚举白名单、槽位唯一性与目录名一致性。
func (m *Meta) Validate(expectedSlug string) error {
	if err := ValidateSlug(m.Name); err != nil {
		return err
	}
	if expectedSlug != "" && m.Name != expectedSlug {
		return fmt.Errorf("%w: meta.name (%q) 必须与目录 slug (%q) 一致", ErrInvalidMeta, m.Name, expectedSlug)
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		return fmt.Errorf("%w: display_name 不能为空", ErrInvalidMeta)
	}
	if utf8.RuneCountInString(strings.TrimSpace(m.ShortName)) > 32 {
		return fmt.Errorf("%w: short_name 长度不能超过 32 个字符", ErrInvalidMeta)
	}
	if utf8.RuneCountInString(strings.TrimSpace(m.Category)) > 32 {
		return fmt.Errorf("%w: category 长度不能超过 32 个字符", ErrInvalidMeta)
	}
	if utf8.RuneCountInString(strings.TrimSpace(m.PreferredModel)) > 80 {
		return fmt.Errorf("%w: preferred_model 长度不能超过 80 个字符", ErrInvalidMeta)
	}
	if len(m.IRFacets) == 0 {
		return fmt.Errorf("%w: ir_facets 不能为空", ErrInvalidMeta)
	}
	validFacets := make(map[string]bool, len(codeir.AllFacetNames))
	for _, fn := range codeir.AllFacetNames {
		validFacets[fn] = true
	}
	for _, f := range m.IRFacets {
		if !validFacets[f] {
			return fmt.Errorf("%w: 非法 ir_facet %q", ErrInvalidMeta, f)
		}
	}
	for _, rf := range m.RepoIRFacets {
		if !validFacets[rf] {
			return fmt.Errorf("%w: 非法 repo_ir_facet %q", ErrInvalidMeta, rf)
		}
	}
	for _, ci := range m.ContextInputs {
		if ci != "code_ir" && ci != "module_context" && ci != "module_digests" {
			return fmt.Errorf("%w: 非法 context_input %q（仅支持 code_ir、module_context 或 module_digests）", ErrInvalidMeta, ci)
		}
	}
	if m.ModuleDigestSource != nil {
		if strings.TrimSpace(m.ModuleDigestSource.DocTypeSlug) != "" {
			if err := ValidateSlug(m.ModuleDigestSource.DocTypeSlug); err != nil {
				return fmt.Errorf("%w: module_digest_source.doc_type_slug 非法: %v", ErrInvalidMeta, err)
			}
		}
		if strings.TrimSpace(m.ModuleDigestSource.SlotID) != "" && !SlotIDPattern.MatchString(m.ModuleDigestSource.SlotID) {
			return fmt.Errorf("%w: module_digest_source.slot_id 非法 (%q)", ErrInvalidMeta, m.ModuleDigestSource.SlotID)
		}
	}
	if len(m.Slots) == 0 {
		return fmt.Errorf("%w: slots 不能为空", ErrInvalidMeta)
	}
	seenSlots := make(map[string]bool, len(m.Slots))
	for _, sl := range m.Slots {
		if !SlotIDPattern.MatchString(sl.SlotID) {
			return fmt.Errorf("%w: 非法 slot_id %q", ErrInvalidMeta, sl.SlotID)
		}
		if seenSlots[sl.SlotID] {
			return fmt.Errorf("%w: 重复的 slot_id %q", ErrInvalidMeta, sl.SlotID)
		}
		seenSlots[sl.SlotID] = true
		if strings.TrimSpace(sl.Title) == "" {
			return fmt.Errorf("%w: slot %q 缺少 title", ErrInvalidMeta, sl.SlotID)
		}
		if sl.Ownership != "auto" && sl.Ownership != "hybrid" && sl.Ownership != "locked" {
			return fmt.Errorf("%w: slot %q ownership 非法 (%q)", ErrInvalidMeta, sl.SlotID, sl.Ownership)
		}
		if sl.MaxToolRounds < 0 || sl.MaxToolRounds > 6 {
			return fmt.Errorf("%w: slot %q max_tool_rounds 须在 0~6 之间 (%d)", ErrInvalidMeta, sl.SlotID, sl.MaxToolRounds)
		}
		for _, sf := range sl.IRFacets {
			if !validFacets[sf] {
				return fmt.Errorf("%w: slot %q 包含非法 ir_facet %q", ErrInvalidMeta, sl.SlotID, sf)
			}
		}
	}
	return nil
}

// ValidateTemplateSlots 校验 template.md 中包含所有已声明槽位的 <!-- slot:<slot_id> --> 占位标记。
func ValidateTemplateSlots(templateMD string, slots []SlotSpec) error {
	for _, sl := range slots {
		marker := fmt.Sprintf("<!-- slot:%s -->", sl.SlotID)
		if !strings.Contains(templateMD, marker) {
			return fmt.Errorf("%w: template.md 缺失槽位标记 %s", ErrInvalidTemplate, marker)
		}
	}
	return nil
}
