package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultMaxFileBytes = 1048576 // 1 MB
	binarySniffBytes    = 512
)

// WalkOptions 控制文件遍历器的降噪与大小限制参数。
type WalkOptions struct {
	MaxFileBytes int64
}

// WalkedFile 描述通过安全校验与降噪过滤后的待分析文件。
type WalkedFile struct {
	RelPath  string // 相对于分析根（repoRoot 或 repoRoot/subdir）的 POSIX 相对路径
	AbsPath  string // 宿主机真实绝对路径（仅供沙箱内只读打开，严禁写入 IR）
	Size     int64
	Language string
}

// WalkStats 记录遍历与三层降噪过滤统计。
type WalkStats struct {
	EffectiveFiles int
	SkippedFiles   int
	SkippedByRule  map[string]int
}

// WalkResult 汇总一次沙箱文件遍历结果。
type WalkResult struct {
	RootDir string
	Files   []WalkedFile
	Stats   WalkStats
}

// FileWalker 实现沙箱边界防护与 Layer 1 + Layer 2 三层降噪文件发现器。
type FileWalker struct{}

func NewFileWalker() *FileWalker {
	return &FileWalker{}
}

// Walk 在 repoRoot（及可选 subdir）内安全遍历文件，自动阻止外部符号链接并叠加 Layer 1 + Layer 2 过滤。
func (w *FileWalker) Walk(ctx context.Context, repoRoot, subdir string, opts WalkOptions) (*WalkResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	normSubdir, err := NormalizeSubdir(subdir)
	if err != nil {
		return nil, err
	}

	scanRoot := repoRoot
	if normSubdir != "" {
		scanRoot, err = SafeJoin(repoRoot, normSubdir)
		if err != nil {
			return nil, err
		}
	} else {
		scanRoot, err = SafeJoin(repoRoot, ".")
		if err != nil {
			return nil, err
		}
	}

	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultMaxFileBytes
	}

	// 读取仓库根目录的 .archdocignore（Layer 2）
	var ignoreMatcher *IgnoreMatcher
	if ignorePath, joinErr := SafeJoin(repoRoot, ".archdocignore"); joinErr == nil {
		if data, readErr := os.ReadFile(ignorePath); readErr == nil {
			ignoreMatcher = ParseIgnoreRules(string(data))
		}
	}

	stats := WalkStats{
		SkippedByRule: make(map[string]int),
	}
	var files []WalkedFile

	walkErr := filepath.WalkDir(scanRoot, func(currentPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if currentPath == scanRoot {
			return nil
		}

		relOS, relErr := filepath.Rel(scanRoot, currentPath)
		if relErr != nil {
			return nil
		}
		relPOSIX := filepath.ToSlash(relOS)

		// .archdocignore 自身不纳入业务源码统计
		if relPOSIX == ".archdocignore" {
			return nil
		}

		// 符号链接安全检查：禁止跟随指向沙箱外的符号链接（SC-06）
		if d.Type()&os.ModeSymlink != 0 {
			realTarget, joinErr := SafeJoin(scanRoot, relPOSIX)
			if joinErr != nil {
				stats.SkippedFiles++
				stats.SkippedByRule[RuleExternalSymlink]++
				return nil
			}
			// 若是沙箱内目录符号链接，不递归以防环路
			if st, stErr := os.Stat(realTarget); stErr != nil || st.IsDir() {
				return nil
			}
		}

		// 硬保护目录（.git/）立即剪枝，严禁被 Layer 2 放行
		if IsHardProtectedPath(relPOSIX) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			stats.SkippedFiles++
			stats.SkippedByRule[RuleVCS]++
			return nil
		}

		// 处理目录剪枝：若 Layer 2 包含 ! 取反规则，允许深入 Layer 1 目录以匹配局部放行文件
		if d.IsDir() {
			if matched, ignored := ignoreMatcher.Match(relPOSIX, true); matched && ignored && !ignoreMatcher.HasNegation() {
				return filepath.SkipDir
			}
			if hitL1, _ := MatchLayer1Builtin(relPOSIX, true); hitL1 && !ignoreMatcher.HasNegation() {
				return filepath.SkipDir
			}
			return nil
		}

		// 文件级评估：先看 Layer 1，再由 Layer 2 (.archdocignore) 覆盖/局部放行
		l1Hit, l1Rule := MatchLayer1Builtin(relPOSIX, false)
		l2Matched, l2Ignored := ignoreMatcher.Match(relPOSIX, false)

		if l2Matched {
			if l2Ignored {
				stats.SkippedFiles++
				stats.SkippedByRule[RuleArchdocIgnore]++
				return nil
			}
			// l2Matched && !l2Ignored => 被 !pattern 显式放行，覆盖非硬保护的 Layer 1（二进制扩展名除外）
			if l1Hit && l1Rule == RuleBinary {
				stats.SkippedFiles++
				stats.SkippedByRule[RuleBinary]++
				return nil
			}
		} else if l1Hit {
			stats.SkippedFiles++
			stats.SkippedByRule[l1Rule]++
			return nil
		}

		// 最终安全解析真实物理路径（防止竞态符号链接替换）
		safeAbs, safeErr := SafeJoin(scanRoot, relPOSIX)
		if safeErr != nil {
			stats.SkippedFiles++
			stats.SkippedByRule[RuleExternalSymlink]++
			return nil
		}

		info, infoErr := os.Stat(safeAbs)
		if infoErr != nil || info.IsDir() {
			return nil
		}

		// 硬保护：单文件体积上限
		if info.Size() > maxFileBytes {
			stats.SkippedFiles++
			stats.SkippedByRule[RuleOversize]++
			return nil
		}

		// 硬保护：前 512 字节二进制 \x00 嗅探
		isBin, sniffErr := sniffBinaryFile(safeAbs)
		if sniffErr != nil {
			return nil
		}
		if isBin {
			stats.SkippedFiles++
			stats.SkippedByRule[RuleBinary]++
			return nil
		}

		files = append(files, WalkedFile{
			RelPath:  relPOSIX,
			AbsPath:  safeAbs,
			Size:     info.Size(),
			Language: detectLanguage(relPOSIX),
		})
		stats.EffectiveFiles++
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("遍历沙箱目录失败: %w", walkErr)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	return &WalkResult{
		RootDir: scanRoot,
		Files:   files,
		Stats:   stats,
	}, nil
}

func sniffBinaryFile(absPath string) (isBin bool, retErr error) {
	f, err := os.Open(absPath)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	buf := make([]byte, binarySniffBytes)
	n, readErr := f.Read(buf)
	if readErr != nil && readErr != io.EOF {
		return false, readErr
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

func detectLanguage(relPOSIX string) string {
	base := strings.ToLower(filepath.Base(relPOSIX))
	if base == "cmakelists.txt" || base == "makefile" || base == "build" || base == "build.bazel" || base == "pom.xml" || base == "build.gradle" {
		return "build"
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".h":
		return "cpp"
	case ".proto":
		return "proto"
	case ".java":
		return "java"
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".yaml", ".yml", ".json", ".toml", ".ini", ".properties", ".conf", ".env":
		return "config"
	case ".sql":
		return "sql"
	default:
		return "other"
	}
}
