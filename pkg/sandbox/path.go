package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	// ErrInvalidSubdir 表示子目录参数非法（含 ..、\x00、\ 或前导 /，对应 HTTP 400 INVALID_SUBDIR）。
	ErrInvalidSubdir = errors.New("子目录参数非法：禁止包含 '..'、控制字符、反斜杠或绝对路径前缀")
	// ErrPathEscape 表示目标路径企图逃逸出受控沙箱根目录（目录穿越或外部符号链接，对应 HTTP 403 PATH_ESCAPE）。
	ErrPathEscape = errors.New("目标路径越界或指向沙箱外符号链接")
)

// NormalizeSubdir 规范化 Monorepo 子目录参数。
// 规则：
// 1. 禁止空字节 \x00、反斜杠 \、绝对路径前缀 /、以及任何 ".." 路径段；
// 2. 去除首尾空白与多余斜杠，"" / "." / "./" 归一化为 ""（代表整仓根目录）。
func NormalizeSubdir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.ContainsRune(trimmed, '\x00') || strings.Contains(trimmed, "\\") {
		return "", ErrInvalidSubdir
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", ErrInvalidSubdir
	}

	// 逐段检查，防止 "svc/../../etc" 等在 path.Clean 后被掩盖或越界
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", ErrInvalidSubdir
		}
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == "" {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", ErrInvalidSubdir
	}
	return cleaned, nil
}

// SafeJoin 将用户或模型提供的相对路径安全解析至沙箱根内（三步防护：Clean -> Rel -> EvalSymlinks）。
// 彻底阻断 ../ 目录穿越、/sandbox-evil 兄弟前缀绕过以及仓库内指向外部文件/目录的符号链接逃逸。
func SafeJoin(sandboxRoot, relPath string) (string, error) {
	if strings.TrimSpace(sandboxRoot) == "" {
		return "", fmt.Errorf("%w: 沙箱根目录为空", ErrPathEscape)
	}
	if strings.ContainsRune(relPath, '\x00') || strings.Contains(relPath, "\\") {
		return "", ErrPathEscape
	}
	trimmedRel := strings.TrimSpace(relPath)
	if strings.HasPrefix(trimmedRel, "/") || filepath.IsAbs(trimmedRel) {
		return "", ErrPathEscape
	}

	absRoot, err := filepath.Abs(sandboxRoot)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathEscape, err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("%w: 沙箱根目录不可解析: %v", ErrPathEscape, err)
	}

	// 第 1 & 2 步：词法 Clean + Rel 边界校验
	candidate := filepath.Clean(filepath.Join(absRoot, filepath.FromSlash(trimmedRel)))
	if !isWithinBase(absRoot, candidate) {
		return "", ErrPathEscape
	}

	// 第 3 步：EvalSymlinks 真实物理路径校验（兼容目标尚未创建的场景：向上追溯最近已存在父目录）
	realTarget, err := resolveRealPathWithAncestor(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathEscape, err)
	}
	if !isWithinBase(realRoot, realTarget) {
		return "", ErrPathEscape
	}

	return realTarget, nil
}

func isWithinBase(baseDir, targetPath string) bool {
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func resolveRealPathWithAncestor(target string) (string, error) {
	// 尝试直接解析目标路径（若目标存在，自动展开所有符号链接）
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	// 若目标不存在，逐级向上找到最近的已存在祖先并展开其符号链接，再拼回剩余子路径
	var tailParts []string
	cur := target
	for {
		parent := filepath.Dir(cur)
		tailParts = append([]string{filepath.Base(cur)}, tailParts...)
		if parent == cur {
			return "", os.ErrNotExist
		}
		cur = parent
		resolvedParent, err := filepath.EvalSymlinks(cur)
		if err == nil {
			joined := append([]string{resolvedParent}, tailParts...)
			return filepath.Clean(filepath.Join(joined...)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
}
