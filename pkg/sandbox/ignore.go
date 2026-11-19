package sandbox

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 降噪跳过原因枚举常量（对应 SnapshotStats.NoiseSkippedByRule）
const (
	RuleVCS             = "vcs"
	RuleThirdParty      = "third_party"
	RuleGenerated       = "generated"
	RuleBinary          = "binary"
	RuleOversize        = "oversize"
	RuleArchdocIgnore   = "archdocignore"
	RuleExternalSymlink = "external_symlink"
)

// IgnoreRule 表示一条兼容 .gitignore 语法的过滤规则。
type IgnoreRule struct {
	Raw      string
	Negated  bool
	DirOnly  bool
	Anchored bool
	Matcher  *regexp.Regexp
}

// IgnoreMatcher 封装仓库级 .archdocignore 规则集合（Layer 2）。
type IgnoreMatcher struct {
	rules       []IgnoreRule
	hasNegation bool
}

// ParseIgnoreRules 解析 .archdocignore 文件内容。
func ParseIgnoreRules(content string) *IgnoreMatcher {
	m := &IgnoreMatcher{}
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimPrefix(line, "!")
			if line == "" {
				continue
			}
		}
		dirOnly := strings.HasSuffix(line, "/")
		if dirOnly {
			line = strings.TrimSuffix(line, "/")
		}
		anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")

		re := compileGlobToRegexp(line, anchored, dirOnly)
		if re == nil {
			continue
		}
		if negated {
			m.hasNegation = true
		}
		m.rules = append(m.rules, IgnoreRule{
			Raw:      rawLine,
			Negated:  negated,
			DirOnly:  dirOnly,
			Anchored: anchored,
			Matcher:  re,
		})
	}
	return m
}

// HasNegation 返回规则集中是否包含 ! 取反放行规则。
func (m *IgnoreMatcher) HasNegation() bool {
	return m != nil && m.hasNegation
}

// Match 评估相对 POSIX 路径是否被 Layer 2 显式匹配：
// 返回 matched=true 且 ignored=true 表示排除；matched=true 且 ignored=false 表示被 ! 显式放行。
func (m *IgnoreMatcher) Match(relPOSIX string, isDir bool) (matched bool, ignored bool) {
	if m == nil || len(m.rules) == 0 {
		return false, false
	}
	relPOSIX = strings.TrimPrefix(path.Clean(relPOSIX), "./")
	for _, rule := range m.rules {
		if rule.DirOnly && !isDir {
			// 目录规则同时匹配该目录下的所有文件
			if matchDirPrefix(rule.Matcher, relPOSIX) {
				matched = true
				ignored = !rule.Negated
			}
			continue
		}
		if rule.Matcher.MatchString(relPOSIX) {
			matched = true
			ignored = !rule.Negated
		}
	}
	return matched, ignored
}

func matchDirPrefix(dirRe *regexp.Regexp, relPOSIX string) bool {
	parts := strings.Split(relPOSIX, "/")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if dirRe.MatchString(prefix) {
			return true
		}
	}
	return false
}

func compileGlobToRegexp(pattern string, anchored bool, dirOnly bool) *regexp.Regexp {
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("(^|.*/)")
	}

	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// ** 匹配跨目录任意字符
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}

	if dirOnly {
		b.WriteString("(/.*)?$")
	} else {
		b.WriteString("$")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// ─── Layer 1 系统内置默认黑名单 ───

var (
	hardProtectedDirs = map[string]bool{
		".git": true,
	}

	vcsAndIDEDirs = map[string]bool{
		".svn":    true,
		".hg":     true,
		".idea":   true,
		".vscode": true,
	}

	thirdPartyDirs = map[string]bool{
		"third_party":  true,
		"3rdparty":     true,
		"vendor":       true,
		"node_modules": true,
		"pods":         true,
		"target":       true,
		"build":        true,
		"dist":         true,
		"__pycache__":  true,
	}

	generatedSuffixes = []string{
		".pb.cc",
		".pb.h",
		".pb.go",
		"_pb2.py",
		"_pb2_grpc.py",
		".min.js",
		".min.css",
	}

	binaryExtensions = map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true, ".obj": true,
		".bin": true, ".class": true, ".jar": true, ".war": true, ".pyc": true, ".pyo": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true, ".bmp": true, ".pdf": true,
		".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true,
		".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
		".mp3": true, ".mp4": true, ".wav": true, ".avi": true, ".mov": true,
	}
)

// IsHardProtectedPath 判断路径是否位于不可被 Layer 2 放行的硬保护目录（如 .git/）。
func IsHardProtectedPath(relPOSIX string) bool {
	for _, seg := range strings.Split(relPOSIX, "/") {
		if hardProtectedDirs[seg] {
			return true
		}
	}
	return false
}

// MatchLayer1Builtin 检查路径是否命中 Layer 1 内置黑名单（返回是否命中与原因类别）。
func MatchLayer1Builtin(relPOSIX string, isDir bool) (bool, string) {
	segments := strings.Split(relPOSIX, "/")
	for _, seg := range segments {
		lowerSeg := strings.ToLower(seg)
		if hardProtectedDirs[lowerSeg] || vcsAndIDEDirs[lowerSeg] {
			return true, RuleVCS
		}
		if thirdPartyDirs[lowerSeg] {
			return true, RuleThirdParty
		}
	}

	if isDir {
		return false, ""
	}

	base := strings.ToLower(path.Base(relPOSIX))
	for _, suf := range generatedSuffixes {
		if strings.HasSuffix(base, suf) {
			return true, RuleGenerated
		}
	}
	if strings.Contains(base, ".generated.") {
		return true, RuleGenerated
	}

	ext := strings.ToLower(filepath.Ext(base))
	if binaryExtensions[ext] {
		return true, RuleBinary
	}

	return false, ""
}

// ─── Layer 3: DocType scope_profile 声明式按需过滤接口（供 L2 上下文装配使用） ───

// ScopeProfile 定义 DocType meta.json 中的 Layer 3 文件范围过滤策略。
type ScopeProfile struct {
	IncludeGlobs []string `json:"include_globs,omitempty"`
	ExcludeGlobs []string `json:"exclude_globs,omitempty"`
	MaxDepth     int      `json:"max_depth,omitempty"`
	IncludeTests bool     `json:"include_tests"`
}

// FilterByScopeProfile 按 Layer 3 ScopeProfile 对已发现文件列表执行二次裁剪，不修改原始列表。
func FilterByScopeProfile(files []WalkedFile, profile *ScopeProfile) []WalkedFile {
	if profile == nil {
		out := make([]WalkedFile, len(files))
		copy(out, files)
		return out
	}

	includeRes := compileProfileGlobs(profile.IncludeGlobs)
	excludeRes := compileProfileGlobs(profile.ExcludeGlobs)

	out := make([]WalkedFile, 0, len(files))
	for _, f := range files {
		if profile.MaxDepth > 0 {
			depth := strings.Count(f.RelPath, "/") + 1
			if depth > profile.MaxDepth {
				continue
			}
		}
		if !profile.IncludeTests && isLikelyTestFile(f.RelPath) {
			continue
		}
		if len(includeRes) > 0 && !matchesAnyRegexp(includeRes, f.RelPath) {
			continue
		}
		if len(excludeRes) > 0 && matchesAnyRegexp(excludeRes, f.RelPath) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func compileProfileGlobs(globs []string) []*regexp.Regexp {
	res := make([]*regexp.Regexp, 0, len(globs))
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		anchored := strings.Contains(g, "/")
		g = strings.TrimPrefix(g, "/")
		if re := compileGlobToRegexp(g, anchored, false); re != nil {
			res = append(res, re)
		}
	}
	return res
}

func matchesAnyRegexp(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func isLikelyTestFile(relPOSIX string) bool {
	lower := strings.ToLower(relPOSIX)
	base := path.Base(lower)
	if strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, "_test.cpp") ||
		strings.HasSuffix(base, "_test.cc") ||
		strings.HasSuffix(base, "_test.c") ||
		strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(base, "test_") ||
		strings.HasSuffix(base, "test.java") ||
		strings.HasSuffix(base, "tests.java") ||
		strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".test.ts") {
		return true
	}
	for _, seg := range strings.Split(lower, "/") {
		if seg == "test" || seg == "tests" || seg == "__tests__" {
			return true
		}
	}
	return false
}
