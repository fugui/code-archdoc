package codeir

import (
	"path"
	"sort"
	"strings"

	"code-archdoc/pkg/sandbox"
)

// DefaultModuleExcludeDirs 默认排除的非业务顶层目录名单（与 ADR-04 D-1 对齐）。
var DefaultModuleExcludeDirs = []string{
	"docs", "doc", "scripts", "tools", "test", "tests", "examples", "demo",
	"third_party", "vendor", "build", "dist", "node_modules", "cmake", ".github", ".gitlab", ".vscode",
}

// ModuleDiscoveryPolicy 仓库级模块发现策略（纯值对象，零 DB 依赖）。
type ModuleDiscoveryPolicy struct {
	Strategy          string   // arch_elements | top_level_dirs | merged（默认 merged）
	ExcludeGlobs      []string // 叠加在 DefaultModuleExcludeDirs 之上的额外排除模式
	MinEffectiveFiles int      // 降噪后有效文件数下限（默认 3，<=0 时回退 3）
}

// ArchElementScope 传入发现引擎的架构元素子目录声明（零 DB 模型依赖）。
type ArchElementScope struct {
	ID           uint
	Subdirectory string
}

// DiscoveredModule 模块发现纯函数的确定性输出条目。
type DiscoveredModule struct {
	Subdirectory   string
	Source         string // arch_element | discovered
	ArchElementID  *uint
	EffectiveFiles int
	TotalLOC       int
}

// DiscoverModules 基于全仓降噪后的 TreeFacet 与策略、架构元素集合，确定性推导仓库下所有业务模块。
// 纯函数、零 I/O、零数据库依赖，严格遵守 ADR-04 D-1 合并与去重语义：
// 1. 统计每个候选子目录下的降噪后有效文件数与总代码行；
// 2. arch_elements 策略仅返回架构元素中声明且非空的子目录（即便文件数少于阈值亦保留）；
// 3. top_level_dirs 策略仅返回排除默认/自定义黑名单且有效文件数 >= MinEffectiveFiles 的顶层目录；
// 4. merged 策略取两者并集，同路径优先保留 source="arch_element" 并关联 ArchElementID；
// 5. 若某顶层目录已被更深层 arch_element 子目录显式细分（如 services/order），自动跳过其直接父顶层目录（services），避免重叠重复生成。
func DiscoverModules(tree *TreeFacet, policy ModuleDiscoveryPolicy, archElements []ArchElementScope) []DiscoveredModule {
	strategy := strings.TrimSpace(policy.Strategy)
	if strategy == "" {
		strategy = "merged"
	}
	minFiles := policy.MinEffectiveFiles
	if minFiles <= 0 {
		minFiles = 3
	}

	// 汇总所有文件路径的统计信息，同时构建顶层目录文件数与 LOC 统计
	topStats := make(map[string]struct {
		files int
		loc   int
	})
	var files []FileSummary
	if tree != nil {
		files = tree.Files
	}
	for _, f := range files {
		clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(f.RelPath, "\\", "/")), "./")
		if clean == "" || clean == "." || !strings.Contains(clean, "/") {
			continue
		}
		top := strings.SplitN(clean, "/", 2)[0]
		if top == "" || strings.HasPrefix(top, ".") {
			continue
		}
		st := topStats[top]
		st.files++
		st.loc += f.LOC
		topStats[top] = st
	}

	// 计算任意子目录前缀的精确文件数与 LOC
	countSubdirStats := func(subdir string) (int, int) {
		if !strings.Contains(subdir, "/") {
			st := topStats[subdir]
			return st.files, st.loc
		}
		prefix := subdir + "/"
		fc, loc := 0, 0
		for _, f := range files {
			clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(f.RelPath, "\\", "/")), "./")
			if strings.HasPrefix(clean, prefix) {
				fc++
				loc += f.LOC
			}
		}
		return fc, loc
	}

	resultBySub := make(map[string]DiscoveredModule)
	coveredTopDirs := make(map[string]bool)

	// 1. 吸收 architecture_elements 中已声明的规范化子目录
	if strategy == "arch_elements" || strategy == "merged" {
		for _, el := range archElements {
			normSub, err := sandbox.NormalizeSubdir(el.Subdirectory)
			if err != nil || normSub == "" {
				continue
			}
			if _, exists := resultBySub[normSub]; exists {
				continue
			}
			fc, loc := countSubdirStats(normSub)
			idCopy := el.ID
			var idPtr *uint
			if idCopy > 0 {
				idPtr = &idCopy
			}
			resultBySub[normSub] = DiscoveredModule{
				Subdirectory:   normSub,
				Source:         "arch_element",
				ArchElementID:  idPtr,
				EffectiveFiles: fc,
				TotalLOC:       loc,
			}
			// 记录该架构元素对应的顶层目录，防止 merged 模式下顶层目录与嵌套子模块同时产出重叠范围
			top := strings.SplitN(normSub, "/", 2)[0]
			if top != "" {
				coveredTopDirs[top] = true
			}
		}
	}

	// 2. 扫描顶层目录发现业务模块
	if strategy == "top_level_dirs" || strategy == "merged" {
		excludeSet := make(map[string]bool, len(DefaultModuleExcludeDirs))
		for _, d := range DefaultModuleExcludeDirs {
			excludeSet[strings.ToLower(strings.TrimSpace(d))] = true
		}

		for top, st := range topStats {
			if st.files < minFiles {
				continue
			}
			if isExcludedModuleDir(top, excludeSet, policy.ExcludeGlobs) {
				continue
			}
			// merged 模式下若架构元素已精确声明该顶层目录或其下嵌套子模块，不再重复添加顶层目录
			if strategy == "merged" {
				if _, exists := resultBySub[top]; exists {
					continue
				}
				if coveredTopDirs[top] {
					continue
				}
			}
			if _, exists := resultBySub[top]; exists {
				continue
			}
			resultBySub[top] = DiscoveredModule{
				Subdirectory:   top,
				Source:         "discovered",
				ArchElementID:  nil,
				EffectiveFiles: st.files,
				TotalLOC:       st.loc,
			}
		}
	}

	out := make([]DiscoveredModule, 0, len(resultBySub))
	for _, m := range resultBySub {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Subdirectory < out[j].Subdirectory
	})
	return out
}

func isExcludedModuleDir(dir string, defaultExclude map[string]bool, customGlobs []string) bool {
	lower := strings.ToLower(strings.TrimSpace(dir))
	if lower == "" || strings.HasPrefix(lower, ".") {
		return true
	}
	if defaultExclude[lower] {
		return true
	}
	for _, rawGlob := range customGlobs {
		pattern := strings.TrimSpace(rawGlob)
		pattern = strings.TrimSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/**")
		pattern = strings.TrimSuffix(pattern, "/*")
		if pattern == "" {
			continue
		}
		if strings.EqualFold(dir, pattern) {
			return true
		}
		if matched, err := path.Match(strings.ToLower(pattern), lower); err == nil && matched {
			return true
		}
	}
	return false
}
