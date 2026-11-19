package doctype_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"code-archdoc/pkg/doctype"
)

func sampleDocTypeMeta(slug, displayName string) doctype.Meta {
	return doctype.Meta{
		Name:           slug,
		DisplayName:    displayName,
		Description:    "用于安全架构评审",
		IsActive:       true,
		IsBuiltin:      false,
		ModelTier:      "flagship",
		TimeoutSeconds: 300,
		IRFacets:       []string{"tree", "http_routes"},
		Slots: []doctype.SlotSpec{
			{
				SlotID:      "attack_surface",
				Title:       "1. 外部入口与攻击面",
				Ownership:   "auto",
				Required:    true,
				IRFacets:    []string{"http_routes"},
				TokenBudget: 8000,
				MinChars:    40,
			},
		},
	}
}

func TestWriterCreateCloneUpdatePreservesExtraFieldsAndAtomicFiles(t *testing.T) {
	root := t.TempDir()
	reg, err := doctype.NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	meta := sampleDocTypeMeta("security-review", "安全架构评审报告")
	meta.ExtraFields = map[string]json.RawMessage{
		"custom_p1_owner": json.RawMessage(`"sec-team"`),
	}
	tpl := "## 1. 外部入口与攻击面\n<!-- slot:attack_surface -->\n"
	genPrompt := "你是应用安全架构师。"
	refPrompt := "精修时保持严谨。"

	// 1. CreateAsset 正常落盘 4 文件
	created, err := reg.CreateAsset("security-review", meta, tpl, genPrompt, refPrompt)
	if err != nil {
		t.Fatalf("CreateAsset 失败: %v", err)
	}
	if created.Slug != "security-review" || created.AggregateHash == "" {
		t.Fatalf("CreateAsset 返回异常: %+v", created)
	}
	for _, fn := range doctype.OrderedAssetFiles {
		if _, statErr := os.Stat(filepath.Join(root, "security-review", fn)); statErr != nil {
			t.Fatalf("磁盘缺少文件 %s: %v", fn, statErr)
		}
	}

	// 2. 重复创建返回 ErrDocTypeExists
	if _, err := reg.CreateAsset("security-review", meta, tpl, genPrompt, refPrompt); !errors.Is(err, doctype.ErrDocTypeExists) {
		t.Fatalf("重复创建应返回 ErrDocTypeExists，实际: %v", err)
	}

	// 3. 非法 slug 返回 ErrInvalidSlug
	if _, err := reg.CreateAsset("../evil", sampleDocTypeMeta("../evil", "Evil"), tpl, genPrompt, ""); !errors.Is(err, doctype.ErrInvalidSlug) {
		t.Fatalf("非法 slug 应返回 ErrInvalidSlug，实际: %v", err)
	}

	// 4. 模板缺少槽位标记返回 ErrInvalidTemplate
	if _, err := reg.UpdateAsset("security-review", meta, "## missing slot marker\n", genPrompt, refPrompt); !errors.Is(err, doctype.ErrInvalidTemplate) {
		t.Fatalf("缺少槽位标记应返回 ErrInvalidTemplate，实际: %v", err)
	}

	// 5. UpdateAsset 在未携带 ExtraFields 时无损保留磁盘既有未知扩展键
	updatedMeta := sampleDocTypeMeta("security-review", "安全架构评审报告 V2")
	updatedMeta.ExtraFields = nil
	updated, err := reg.UpdateAsset("security-review", updatedMeta, tpl, genPrompt+"\n补充 OWASP 校验。", refPrompt)
	if err != nil {
		t.Fatalf("UpdateAsset 失败: %v", err)
	}
	if string(updated.Meta.ExtraFields["custom_p1_owner"]) != `"sec-team"` {
		t.Fatalf("UpdateAsset 丢失了磁盘未知扩展字段: %+v", updated.Meta.ExtraFields)
	}
	if updated.AggregateHash == created.AggregateHash {
		t.Fatal("修改 generation_prompt.md 后 AggregateHash 应变化")
	}

	// 6. CloneAsset 克隆到新 slug 且保留扩展字段与资产内容
	cloned, err := reg.CloneAsset("security-review", "security-review-v2", "安全评审克隆版")
	if err != nil {
		t.Fatalf("CloneAsset 失败: %v", err)
	}
	if cloned.Slug != "security-review-v2" || cloned.Meta.DisplayName != "安全评审克隆版" || cloned.Meta.IsBuiltin {
		t.Fatalf("CloneAsset 元数据异常: %+v", cloned.Meta)
	}
	if string(cloned.Meta.ExtraFields["custom_p1_owner"]) != `"sec-team"` {
		t.Fatalf("CloneAsset 未保留未知扩展字段: %+v", cloned.Meta.ExtraFields)
	}

	// 7. UpdateStatus 仅切换 is_active，无损保留未知字段
	toggled, err := reg.UpdateStatus("security-review", false)
	if err != nil {
		t.Fatalf("UpdateStatus 失败: %v", err)
	}
	if toggled.Meta.IsActive {
		t.Fatal("UpdateStatus 后 is_active 应为 false")
	}
	if string(toggled.Meta.ExtraFields["custom_p1_owner"]) != `"sec-team"` {
		t.Fatalf("UpdateStatus 丢失了未知扩展字段: %+v", toggled.Meta.ExtraFields)
	}

	// 8. 确认无任何残留 .tmp-* 临时文件
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			return fmt.Errorf("发现残留临时文件: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriterConcurrentSlugLockNoCorruption(t *testing.T) {
	root := t.TempDir()
	reg, err := doctype.NewRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	tpl := "## 1. 入口\n<!-- slot:attack_surface -->\n"
	if _, err := reg.CreateAsset("concurrent-doc", sampleDocTypeMeta("concurrent-doc", "并发测试"), tpl, "init", ""); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 10)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for round := 0; round < 4; round++ {
				m := sampleDocTypeMeta("concurrent-doc", fmt.Sprintf("并发版本-%d-%d", idx, round))
				tMD := fmt.Sprintf("## 1. 入口-%d-%d\n<!-- slot:attack_surface -->\n", idx, round)
				if _, upErr := reg.UpdateAsset("concurrent-doc", m, tMD, fmt.Sprintf("prompt-%d-%d", idx, round), ""); upErr != nil {
					errCh <- upErr
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatalf("并发写入出现错误: %v", e)
	}

	// 验证最终磁盘文件完整可解析且无 .tmp-* 残留
	loaded, err := reg.LoadOne("concurrent-doc")
	if err != nil {
		t.Fatalf("并发写后 LoadOne 失败: %v", err)
	}
	if loaded.Slug != "concurrent-doc" || !strings.Contains(loaded.TemplateMD, "<!-- slot:attack_surface -->") {
		t.Fatalf("并发写后磁盘内容损坏: %+v", loaded)
	}
	entries, err := os.ReadDir(filepath.Join(root, "concurrent-doc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".tmp-") {
			t.Fatalf("并发写后残留临时文件: %s", ent.Name())
		}
	}
}
