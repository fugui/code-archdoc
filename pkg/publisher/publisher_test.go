package publisher

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 1. TestRenderFrontmatterDeepLinkAndCrossDocDowngrade (F-11 / SC-07)
func TestRenderFrontmatterDeepLinkAndCrossDocDowngrade(t *testing.T) {
	body := `## 1. 核心链路

订单支付核心入口位于 ` + "`src/order/order_service.cpp:142-188`" + `，实现方法 ` + "`order::OrderService::Pay`" + `。

详见已发布文档 [数据契约](./data-and-contract.md) 与尚未发布的 [配置运维](./ops-and-config.md)。

` + "```cpp\n// 代码块内部不应被改写: `src/order/order_service.cpp:142-188`\n// [保留](./ops-and-config.md)\n```" + `
`

	out := RenderMarkdown(RenderInput{
		Frontmatter: FrontmatterData{
			Title:           "order-service 系统架构与核心链路",
			Repo:            "order-service",
			Subdir:          "svc/order",
			DocType:         "system-architecture",
			DocTypeName:     "系统架构与核心链路",
			DocTypeRevision: 3,
			DocID:           42,
			RevisionNo:      5,
			RefName:         "master",
			CommitSHA:       "a1b2c3d4e5f6789012345678901234567890abcd",
			PublishedAt:     "2026-11-19T15:30:00+08:00",
			PublishedBy:     "zhangsan",
			SourceModule:    "archdoc",
		},
		BodyMarkdown: body,
		Evidences: []EvidenceRef{
			{
				FilePath:   "src/order/order_service.cpp",
				SymbolKind: "cpp_func",
				FQSymbol:   "order::OrderService::Pay",
				CommitSHA:  "a1b2c3d4e5f6789012345678901234567890abcd",
				LineStart:  142,
				LineEnd:    188,
			},
		},
		PublishedDocTypes: map[string]bool{
			"system-architecture": true,
			"data-and-contract":   true,
		},
	})

	// 断言 YAML Frontmatter 完整
	if !strings.HasPrefix(out.Markdown, "---\n") {
		t.Fatalf("产出文档必须以 YAML Frontmatter 开头")
	}
	for _, want := range []string{
		`title: "order-service 系统架构与核心链路"`,
		`repo: "order-service"`,
		`subdir: "svc/order"`,
		`doc_type: "system-architecture"`,
		`doc_type_revision: 3`,
		`doc_id: 42`,
		`revision_no: 5`,
		`commit_sha: "a1b2c3d4e5f6789012345678901234567890abcd"`,
		`published_by: "zhangsan"`,
		`source_module: "archdoc"`,
	} {
		if !strings.Contains(out.Markdown, want) {
			t.Fatalf("Frontmatter 缺少预期字段 %q:\n%s", want, out.Markdown)
		}
	}

	// 断言证据锚点转换为 /archdoc/docs/42/evidence?... 平台深链
	wantDeepLink := "/archdoc/docs/42/evidence?commit=a1b2c3d4e5f6789012345678901234567890abcd&file=src%2Forder%2Forder_service.cpp&symbol=order%3A%3AOrderService%3A%3APay&lines=142-188"
	if !strings.Contains(out.Markdown, "[src/order/order_service.cpp:142-188]("+wantDeepLink+")") {
		t.Fatalf("证据锚点未正确转换为平台深链，期望包含 %s\n实际:\n%s", wantDeepLink, out.Markdown)
	}
	if !strings.Contains(out.Markdown, "[order::OrderService::Pay]("+wantDeepLink+")") {
		t.Fatalf("符号证据未正确转换为平台深链:\n%s", out.Markdown)
	}

	// 断言已发布跨文档链接保留，未发布 ./ops-and-config.md 自动降级为纯文本
	if !strings.Contains(out.Markdown, "[数据契约](./data-and-contract.md)") {
		t.Fatalf("已发布同仓文档链接不应被降级:\n%s", out.Markdown)
	}
	if !strings.Contains(out.Markdown, "配置运维（尚未发布）") {
		t.Fatalf("未发布 ./ops-and-config.md 应自动降级为纯文本:\n%s", out.Markdown)
	}
	if out.BrokenLinksDowngraded != 1 {
		t.Fatalf("期望 BrokenLinksDowngraded=1，实际 %d", out.BrokenLinksDowngraded)
	}
	// 围栏代码块内不应被改写
	if !strings.Contains(out.Markdown, "// [保留](./ops-and-config.md)") {
		t.Fatalf("围栏代码块内的文本不应被误改写:\n%s", out.Markdown)
	}
}

// 2. TestAtomicPublishRenameAndUnpublishCleansEmptyDirs (F-11 / SC-10)
func TestAtomicPublishRenameAndUnpublishCleansEmptyDirs(t *testing.T) {
	ctx := context.Background()
	publishRoot := t.TempDir()
	sink, err := NewLocalFSPublishSink(publishRoot)
	if err != nil {
		t.Fatal(err)
	}

	// 验证 _internal 映射为 dir_internal 不被 code-bench 隐藏
	relPath := BuildPublishRelPath("order-service", "_internal/core", "system-architecture")
	wantRel := "order-service/sub/dir_internal/core/system-architecture.md"
	if relPath != wantRel {
		t.Fatalf("期望发布路径 %q，实际 %q", wantRel, relPath)
	}

	entry := PublishedDocEntry{
		RepoName:     "order-service",
		Subdirectory: "_internal/core",
		DocTypeSlug:  "system-architecture",
		DocTypeName:  "系统架构与核心链路",
		Title:        "order-service (_internal/core) 系统架构与核心链路",
		RelPath:      relPath,
		CommitSHA:    "abcdef1234567890abcdef1234567890abcdef12",
		PublishedAt:  "2026-11-19T16:00:00+08:00",
	}

	content := []byte("---\ntitle: \"test\"\n---\n\n# Body\n")
	if err := sink.PublishDocument(ctx, relPath, "", content, []PublishedDocEntry{entry}); err != nil {
		t.Fatalf("PublishDocument 失败: %v", err)
	}

	// 验证目标文件已原子落盘
	fullTarget := filepath.Join(publishRoot, filepath.FromSlash(relPath))
	gotBytes, err := os.ReadFile(fullTarget)
	if err != nil {
		t.Fatalf("读取已发布文件失败: %v", err)
	}
	if !bytes.Equal(gotBytes, content) {
		t.Fatalf("落盘内容不一致")
	}

	// 验证 .staging/ 无任何残留临时文件
	stagingEntries, err := os.ReadDir(filepath.Join(publishRoot, ".staging"))
	if err != nil {
		t.Fatalf("读取 .staging 目录失败: %v", err)
	}
	if len(stagingEntries) != 0 {
		t.Fatalf(".staging 不应存在残留临时文件，实际 %d 个", len(stagingEntries))
	}

	// 验证根 README.md、仓库 README.md 与子目录 README.md 正确索引
	for _, readmeRel := range []string{
		"README.md",
		"order-service/README.md",
		"order-service/sub/dir_internal/core/README.md",
	} {
		b, rErr := os.ReadFile(filepath.Join(publishRoot, filepath.FromSlash(readmeRel)))
		if rErr != nil {
			t.Fatalf("期望存在索引 %s: %v", readmeRel, rErr)
		}
		if !strings.Contains(string(b), "system-architecture.md") {
			t.Fatalf("索引 %s 缺少文档链接:\n%s", readmeRel, string(b))
		}
	}

	// 撤回最后一篇文档 -> 目标文件、所有 README.md 及空子目录均被彻底清理干净
	if err := sink.UnpublishDocument(ctx, relPath, nil); err != nil {
		t.Fatalf("UnpublishDocument 失败: %v", err)
	}
	if _, err := os.Stat(fullTarget); !os.IsNotExist(err) {
		t.Fatalf("撤回后目标文档应不存在，实际 err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(publishRoot, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("撤回最后一篇文档后根 README.md 应被移除")
	}
	if _, err := os.Stat(filepath.Join(publishRoot, "order-service")); !os.IsNotExist(err) {
		t.Fatalf("撤回最后一篇文档后空仓库目录 order-service 应被彻底清理")
	}
}

// 3. TestZipExportContainsIndexAndBlocksZipSlip (F-11)
func TestZipExportContainsIndexAndBlocksZipSlip(t *testing.T) {
	// 验证 SanitizeZipEntryPath 拦截全部恶意路径
	badPaths := []string{
		"../evil.md",
		"/abs.md",
		"order/../../etc/passwd",
		`order\evil.md`,
		"C:/Windows/evil.md",
		"doc\x00.md",
		"",
	}
	for _, bp := range badPaths {
		if _, err := SanitizeZipEntryPath(bp); !errors.Is(err, ErrUnsafeZipPath) {
			t.Fatalf("恶意路径 %q 应被 SanitizeZipEntryPath 拦截，实际 err=%v", bp, err)
		}
	}

	// 验证 BuildZipArchive 拒绝包含 Zip-Slip 路径的打包请求
	var badBuf bytes.Buffer
	if err := BuildZipArchive(&badBuf, []ZipDocItem{
		{EntryPath: "../evil.md", Content: []byte("evil")},
	}, true); !errors.Is(err, ErrUnsafeZipPath) {
		t.Fatalf("BuildZipArchive 应拒绝恶意 Zip-Slip 条目，实际 err=%v", err)
	}

	// 验证合法多文档 ZIP 包含 INDEX.md 及全部文档
	var goodBuf bytes.Buffer
	items := []ZipDocItem{
		{
			EntryPath:   "order-service/system-architecture.md",
			Title:       "order-service 系统架构",
			RepoName:    "order-service",
			DocTypeName: "系统架构与核心链路",
			CommitSHA:   "1111222233334444555566667777888899990000",
			Content:     []byte("# Arch\n"),
		},
		{
			EntryPath:    "order-service/sub/svc/order/data-and-contract.md",
			Title:        "order-service (svc/order) 数据契约",
			RepoName:     "order-service",
			Subdirectory: "svc/order",
			DocTypeName:  "数据模型与接口契约",
			CommitSHA:    "1111222233334444555566667777888899990000",
			Content:      []byte("# Data\n"),
		},
	}
	if err := BuildZipArchive(&goodBuf, items, true); err != nil {
		t.Fatalf("BuildZipArchive 失败: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(goodBuf.Bytes()), int64(goodBuf.Len()))
	if err != nil {
		t.Fatalf("解析生成 ZIP 失败: %v", err)
	}

	files := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, oErr := f.Open()
		if oErr != nil {
			t.Fatal(oErr)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(rc)
		_ = rc.Close()
		files[f.Name] = b.String()
	}

	if _, ok := files["INDEX.md"]; !ok {
		t.Fatalf("ZIP 缺少 INDEX.md")
	}
	if !strings.Contains(files["INDEX.md"], "order-service/system-architecture.md") ||
		!strings.Contains(files["INDEX.md"], "order-service/sub/svc/order/data-and-contract.md") {
		t.Fatalf("INDEX.md 缺少文档索引条目:\n%s", files["INDEX.md"])
	}
	if files["order-service/system-architecture.md"] != "# Arch\n" {
		t.Fatalf("文档 1 内容不匹配")
	}
	if files["order-service/sub/svc/order/data-and-contract.md"] != "# Data\n" {
		t.Fatalf("文档 2 内容不匹配")
	}
}
