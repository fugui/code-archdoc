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
	}
	if totalSlots != 19 {
		t.Fatalf("期望 5 类内置 DocType 共计 19 个稳定槽位，实际 %d", totalSlots)
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
	if len(meta.ExtraFields) != 2 {
		t.Fatalf("期望保留 2 个未知扩展字段，实际 %d (%+v)", len(meta.ExtraFields), meta.ExtraFields)
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
