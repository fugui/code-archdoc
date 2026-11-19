package doctype

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"code-archdoc/pkg/sandbox"
)

//go:embed builtin
var builtinFS embed.FS

// BuiltinSlugs 固定顺序的 5 类内置 DocType 标识
var BuiltinSlugs = []string{
	"system-architecture",
	"data-and-contract",
	"ops-and-config",
	"code-walkthrough",
	"agents-context",
}

// SeedBuiltinDocTypes 将编译期内嵌的 5 类默认 DocType 幂等初始化至 docTypesRoot。
// 强约束：仅当目标 <slug>/ 目录尚不存在时写出，绝不覆盖管理员已修改的目录资产。
func SeedBuiltinDocTypes(docTypesRoot string) ([]string, error) {
	if err := os.MkdirAll(docTypesRoot, 0750); err != nil {
		return nil, fmt.Errorf("创建 doc_types 根目录失败: %w", err)
	}

	var seeded []string
	for _, slug := range BuiltinSlugs {
		if err := ValidateSlug(slug); err != nil {
			return nil, err
		}
		targetDir, err := sandbox.SafeJoin(docTypesRoot, slug)
		if err != nil {
			return nil, err
		}
		if st, statErr := os.Stat(targetDir); statErr == nil && st.IsDir() {
			// 目标目录已存在：绝不覆盖管理员已修改资产
			continue
		}

		srcDir := path.Join("builtin", slug)
		entries, err := fs.ReadDir(builtinFS, srcDir)
		if err != nil {
			return nil, fmt.Errorf("读取内置 DocType %s 资产失败: %w", slug, err)
		}

		if err := os.MkdirAll(targetDir, 0750); err != nil {
			return nil, fmt.Errorf("创建 DocType 目录 %s 失败: %w", slug, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, readErr := fs.ReadFile(builtinFS, path.Join(srcDir, entry.Name()))
			if readErr != nil {
				return nil, fmt.Errorf("读取内置文件 %s/%s 失败: %w", slug, entry.Name(), readErr)
			}
			destPath := filepath.Join(targetDir, entry.Name())
			if writeErr := atomicWriteAssetFile(destPath, data); writeErr != nil {
				return nil, fmt.Errorf("写入内置文件 %s/%s 失败: %w", slug, entry.Name(), writeErr)
			}
		}
		seeded = append(seeded, slug)
	}

	sort.Strings(seeded)
	return seeded, nil
}
