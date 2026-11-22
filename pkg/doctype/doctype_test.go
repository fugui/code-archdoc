package doctype_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/pkg/doctype"
)

func TestBuiltinSeedIdempotentAndNoOverwrite(t *testing.T) {
	root := t.TempDir()
	reg, err := doctype.NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 首次 Seed 应写出全部 5 类内置 DocType，且总槽位数严格为 19
	seeded1, err := reg.SeedBuiltin()
	if err != nil {
		t.Fatalf("首次 Seed 失败: %v", err)
	}
	if len(seeded1) != 5 {
		t.Fatalf("期望首次 Seed 5 类 DocType，实际 %d (%v)", len(seeded1), seeded1)
	}

	bundles1, err := reg.LoadAll()
	if err != nil {
		t.Fatalf("首次 LoadAll 失败: %v", err)
	}
	if len(bundles1) != 5 {
		t.Fatalf("期望加载 5 类 DocType，实际 %d", len(bundles1))
	}

	totalSlots := 0
	hashBefore := make(map[string]string, 5)
	for _, b := range bundles1 {
		totalSlots += len(b.Meta.Slots)
		hashBefore[b.Slug] = b.AggregateHash
		if len(b.AggregateHash) != 64 {
			t.Fatalf("%s aggregate_hash 长度异常: %q", b.Slug, b.AggregateHash)
		}
		if strings.TrimSpace(b.Meta.ShortName) == "" || strings.TrimSpace(b.Meta.Category) == "" {
			t.Fatalf("内置类型 %s 应包含非空 short_name 与 category，实际=%+v", b.Slug, b.Meta)
		}
	}
	if totalSlots != 19 {
		t.Fatalf("期望 5 类内置 DocType 共计 19 个稳定槽位，实际 %d", totalSlots)
	}

	// 验证核心流程与调用链槽位默认开启受限只读源码工具，其余槽位默认关闭
	toolEnabled := make(map[string]int)
	for _, b := range bundles1 {
		for _, sl := range b.Meta.Slots {
			if sl.EnableSourceTools {
				toolEnabled[b.Slug+"/"+sl.SlotID] = sl.MaxToolRounds
			}
		}
	}
	expectedEnabled := []string{
		"system-architecture/runtime_concurrency",
		"system-architecture/core_request_flows",
		"code-walkthrough/call_chain_guide",
	}
	if len(toolEnabled) != len(expectedEnabled) {
		t.Fatalf("期望仅 %d 个核心流程槽位开启只读源码工具，实际 %v", len(expectedEnabled), toolEnabled)
	}
	for _, key := range expectedEnabled {
		if toolEnabled[key] != 4 {
			t.Fatalf("槽位 %s 期望 max_tool_rounds=4，实际 %d", key, toolEnabled[key])
		}
	}

	// 验证超出安全上限的 max_tool_rounds 被 Meta.Validate 拦截
	badMeta := bundles1[0].Meta
	badMeta.Slots = append([]doctype.SlotSpec(nil), badMeta.Slots...)
	badMeta.Slots[0].MaxToolRounds = 9
	if err := badMeta.Validate(bundles1[0].Slug); err == nil {
		t.Fatal("max_tool_rounds=9 超出上限 6，Meta.Validate 应返回错误")
	}

	// 2. 再次 Seed 幂等，不重复写入且哈希完全不变
	seeded2, err := reg.SeedBuiltin()
	if err != nil {
		t.Fatalf("二次 Seed 失败: %v", err)
	}
	if len(seeded2) != 0 {
		t.Fatalf("期望二次 Seed 写入 0 条，实际 %v", seeded2)
	}
	bundles2, err := reg.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bundles2 {
		if b.AggregateHash != hashBefore[b.Slug] {
			t.Fatalf("未改动目录但 %s 哈希发生变化", b.Slug)
		}
	}

	// 3. 模拟管理员修改 system-architecture/generation_prompt.md 后重新加载与再次 Seed
	promptPath := filepath.Join(root, "system-architecture", "generation_prompt.md")
	origPrompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	customSuffix := "\n# Admin custom rule: include latency budget table.\n"
	if err := os.WriteFile(promptPath, append(origPrompt, []byte(customSuffix)...), 0640); err != nil {
		t.Fatal(err)
	}

	// 再次 Seed 绝不覆盖管理员修改
	if _, err := reg.SeedBuiltin(); err != nil {
		t.Fatal(err)
	}
	afterSeedPrompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterSeedPrompt), "Admin custom rule") {
		t.Fatal("SeedBuiltin 意外覆盖了管理员对目录资产的修改！")
	}

	bundles3, err := reg.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	changedCount := 0
	for _, b := range bundles3 {
		if b.AggregateHash != hashBefore[b.Slug] {
			changedCount++
			if b.Slug != "system-architecture" {
				t.Fatalf("非改动类型 %s 的哈希意外变化", b.Slug)
			}
		}
	}
	if changedCount != 1 {
		t.Fatalf("期望仅 1 个 DocType 哈希变化，实际 %d", changedCount)
	}
}

func TestMetaJSONPreserveUnknownExtraFields(t *testing.T) {
	raw := `{
  "name": "custom-audit",
  "display_name": "自定义评审",
  "is_active": true,
  "is_builtin": false,
  "model_tier": "fast",
  "ir_facets": ["tree", "symbols"],
  "slots": [
    {
      "slot_id": "summary_sec",
      "title": "1. 概览",
      "ownership": "auto",
      "required": true,
      "ir_facets": ["tree"]
    }
  ],
  "custom_p1_flag": {"k": 1},
  "future_p2_array": ["a", "b"]
}`

	var meta doctype.Meta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("Unmarshal meta.json 失败: %v", err)
	}
	// 旧 model_tier 字段应被 knownMetaKeys 静默吸收且不污染 ExtraFields
	if len(meta.ExtraFields) != 2 {
		t.Fatalf("期望保留 2 个未知扩展字段，实际 %d (%+v)", len(meta.ExtraFields), meta.ExtraFields)
	}
	if _, leaked := meta.ExtraFields["model_tier"]; leaked {
		t.Fatalf("旧 model_tier 字段不应进入 ExtraFields: %+v", meta.ExtraFields)
	}

	// 序列化写回后再反序列化为通用 map 验证未知键原样保留
	outBytes, err := meta.MarshalPrettyJSON()
	if err != nil {
		t.Fatalf("MarshalPrettyJSON 失败: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(outBytes, &decoded); err != nil {
		t.Fatalf("二次解析失败: %v", err)
	}
	flagObj, ok := decoded["custom_p1_flag"].(map[string]any)
	if !ok || flagObj["k"] != float64(1) {
		t.Fatalf("未知扩展字段 custom_p1_flag 丢失或被破坏: %s", string(outBytes))
	}
	arr, ok := decoded["future_p2_array"].([]any)
	if !ok || len(arr) != 2 || arr[0] != "a" || arr[1] != "b" {
		t.Fatalf("未知扩展字段 future_p2_array 丢失或被破坏: %s", string(outBytes))
	}
}

func TestSlugWhitelistValidation(t *testing.T) {
	invalid := []string{
		"../evil",
		"UpperCase",
		"has space",
		"a",
		"-starts-with-dash",
		"_starts_with_underscore",
		"foo/bar",
		"",
	}
	for _, s := range invalid {
		if err := doctype.ValidateSlug(s); err == nil {
			t.Errorf("非法 slug %q 应被拦截，但通过了校验", s)
		}
	}

	valid := []string{
		"system-architecture",
		"sec_review-2",
		"ab",
		"01-custom_doc",
	}
	for _, s := range valid {
		if err := doctype.ValidateSlug(s); err != nil {
			t.Errorf("合法 slug %q 不应被拦截，返回错误: %v", s, err)
		}
	}
}

func TestDocTypeShortNameAndCategoryValidationAndFallback(t *testing.T) {
	if got := doctype.EffectiveShortName("system-architecture", "", "系统架构与核心链路"); got != "系统架构" {
		t.Fatalf("内置短名兜底异常: %q", got)
	}
	if got := doctype.EffectiveShortName("custom-doc", "自定义短名", "很长的自定义文档名称"); got != "自定义短名" {
		t.Fatalf("显式短名应优先返回，实际: %q", got)
	}
	if got := doctype.EffectiveShortName("custom-doc", "", "自定义文档全称"); got != "自定义文档全称" {
		t.Fatalf("自定义类型无 short_name 时应回退 display_name，实际: %q", got)
	}
	if got := doctype.EffectiveCategory("code-walkthrough", ""); got != "研发指南" {
		t.Fatalf("内置分类兜底异常: %q", got)
	}
	if got := doctype.EffectiveCategory("custom-doc", "安全合规"); got != "安全合规" {
		t.Fatalf("显式分类应优先返回，实际: %q", got)
	}
	if got := doctype.EffectiveCategory("custom-doc", ""); got != "" {
		t.Fatalf("自定义无分类应返回空串，实际: %q", got)
	}

	m := doctype.Meta{
		Name:        "custom-doc",
		DisplayName: "自定义文档",
		ShortName:   strings.Repeat("字", 33),
		IRFacets:    []string{"tree"},
		Slots: []doctype.SlotSpec{
			{SlotID: "sec_one", Title: "1. 章节", Ownership: "auto", Required: true, IRFacets: []string{"tree"}},
		},
	}
	if err := m.Validate("custom-doc"); err == nil {
		t.Fatal("short_name 超过 32 个字符应被 Validate 拦截")
	}

	m.ShortName = "短标题"
	m.Category = strings.Repeat("类", 33)
	if err := m.Validate("custom-doc"); err == nil {
		t.Fatal("category 超过 32 个字符应被 Validate 拦截")
	}

	m.Category = "质量与安全"
	m.PreferredModel = strings.Repeat("m", 81)
	if err := m.Validate("custom-doc"); err == nil {
		t.Fatal("preferred_model 超过 80 个字符应被 Validate 拦截")
	}

	m.PreferredModel = "DeepSeek-V3"
	if err := m.Validate("custom-doc"); err != nil {
		t.Fatalf("合法 short_name/category/preferred_model 不应被拦截: %v", err)
	}
}
