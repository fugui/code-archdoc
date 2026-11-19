package publisher

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// ErrUnsafeZipPath 表示 ZIP 条目路径存在绝对路径、目录穿越或控制字符等 Zip-Slip 风险。
var ErrUnsafeZipPath = errors.New("ZIP 条目路径非法：存在目录穿越或绝对路径风险")

// ZipDocItem 描述打包进导出 ZIP 的单篇文档条目。
type ZipDocItem struct {
	EntryPath    string
	Title        string
	RepoName     string
	Subdirectory string
	DocTypeName  string
	CommitSHA    string
	Content      []byte
}

// SanitizeZipEntryPath 严格校验并清洗 ZIP 条目路径，彻底阻断 Zip-Slip（../、绝对路径、反斜杠、控制字符、盘符）。
func SanitizeZipEntryPath(rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("%w: 空路径", ErrUnsafeZipPath)
	}
	for _, r := range rawPath {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: 包含控制字符", ErrUnsafeZipPath)
		}
	}
	if strings.Contains(rawPath, `\`) {
		return "", fmt.Errorf("%w: 禁止包含反斜杠", ErrUnsafeZipPath)
	}
	trimmed := strings.TrimSpace(rawPath)
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("%w: 禁止绝对路径 %q", ErrUnsafeZipPath, rawPath)
	}
	// 拦截 Windows 盘符前缀（如 C:/evil.md）
	if len(trimmed) >= 2 && trimmed[1] == ':' && ((trimmed[0] >= 'a' && trimmed[0] <= 'z') || (trimmed[0] >= 'A' && trimmed[0] <= 'Z')) {
		return "", fmt.Errorf("%w: 禁止盘符路径 %q", ErrUnsafeZipPath, rawPath)
	}

	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: 禁止 '..' 目录穿越 %q", ErrUnsafeZipPath, rawPath)
		}
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: 路径清洗后越界 %q", ErrUnsafeZipPath, rawPath)
	}
	return cleaned, nil
}

// BuildZipArchive 流式构建防 Zip-Slip 的多文档导出 ZIP（可选包含顶层 INDEX.md 索引）。
func BuildZipArchive(w io.Writer, items []ZipDocItem, includeIndex bool) error {
	zw := zip.NewWriter(w)

	type preparedItem struct {
		cleanPath string
		item      ZipDocItem
	}

	prepared := make([]preparedItem, 0, len(items))
	seen := make(map[string]bool, len(items)+1)
	if includeIndex {
		seen["INDEX.md"] = true
	}

	for _, it := range items {
		cleanPath, err := SanitizeZipEntryPath(it.EntryPath)
		if err != nil {
			_ = zw.Close()
			return err
		}
		// 防重名冲突
		uniquePath := cleanPath
		if seen[uniquePath] {
			ext := path.Ext(cleanPath)
			base := strings.TrimSuffix(cleanPath, ext)
			for idx := 2; ; idx++ {
				cand := fmt.Sprintf("%s-%d%s", base, idx, ext)
				if !seen[cand] {
					uniquePath = cand
					break
				}
			}
		}
		seen[uniquePath] = true
		prepared = append(prepared, preparedItem{cleanPath: uniquePath, item: it})
	}

	sort.Slice(prepared, func(i, j int) bool {
		return prepared[i].cleanPath < prepared[j].cleanPath
	})

	if includeIndex {
		var sb strings.Builder
		sb.WriteString("# 架构设计文档导出索引\n\n")
		sb.WriteString("| 仓库 | 范围 | 文档类型 | 文件路径 | 提交哈希 |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
		for _, p := range prepared {
			scope := "整仓"
			if p.item.Subdirectory != "" {
				scope = "`" + p.item.Subdirectory + "`"
			}
			shortSHA := p.item.CommitSHA
			if len(shortSHA) > 8 {
				shortSHA = shortSHA[:8]
			}
			title := p.item.Title
			if title == "" {
				title = p.cleanPath
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | [%s](./%s) | `%s` |\n",
				p.item.RepoName, scope, p.item.DocTypeName, title, p.cleanPath, shortSHA)
		}

		idxWriter, err := zw.Create("INDEX.md")
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := idxWriter.Write([]byte(sb.String())); err != nil {
			_ = zw.Close()
			return err
		}
	}

	for _, p := range prepared {
		entryWriter, err := zw.Create(p.cleanPath)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := entryWriter.Write(p.item.Content); err != nil {
			_ = zw.Close()
			return err
		}
	}

	return zw.Close()
}
