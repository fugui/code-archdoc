package publisher

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"code-archdoc/pkg/sandbox"

	"github.com/google/uuid"
)

// PublishedDocEntry 表示单篇已发布文档的索引元数据，用于原子重建仓库/子目录/根级 README.md。
type PublishedDocEntry struct {
	RepoName     string
	Subdirectory string
	DocTypeSlug  string
	DocTypeName  string
	Title        string
	RelPath      string
	CommitSHA    string
	PublishedAt  string
}

// PublishSink 定义定稿文档发布落盘与撤回接口（ADR-03 §3.4：为 P1 跨机 HTTP 文档源解耦预留边界）。
type PublishSink interface {
	PublishDocument(ctx context.Context, relPath string, oldRelPath string, content []byte, allPublished []PublishedDocEntry) error
	UnpublishDocument(ctx context.Context, relPath string, remainingPublished []PublishedDocEntry) error
}

var reUnsafeSegChars = regexp.MustCompile(`[^a-zA-Z0-9._\-\x{4e00}-\x{9fa5}]+`)

// SanitizePublishSegment 规范化发布目录中的单个路径段（ADR-03 §3.1）：
// 1. 清除路径分隔符、控制字符与非法字符；
// 2. 防止以 "." 或 "_" 开头导致被 code-bench scanDocDir 静默隐藏（例如 "_internal" -> "dir_internal"）。
func SanitizePublishSegment(seg string) string {
	s := strings.TrimSpace(seg)
	s = strings.ReplaceAll(s, "..", "")
	s = reUnsafeSegChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" || s == "." || s == ".." {
		return "unnamed"
	}
	if strings.HasPrefix(s, "_") {
		return "dir" + s
	}
	if strings.HasPrefix(s, ".") {
		trimmed := strings.TrimLeft(s, ".")
		if trimmed == "" {
			return "dir_hidden"
		}
		if strings.HasPrefix(trimmed, "_") {
			return "dir" + trimmed
		}
		return "dir_" + trimmed
	}
	return s
}

// BuildPublishRelPath 生成符合 ADR-03 §3.1 布局的相对发布路径（POSIX 正斜杠）：
// - 整仓：<repo_slug>/<doc_type_slug>.md
// - 子目录：<repo_slug>/sub/<sanitized_subdir>/<doc_type_slug>.md
func BuildPublishRelPath(repoName, subdir, docTypeSlug string) string {
	repoSlug := SanitizePublishSegment(repoName)
	docFile := SanitizePublishSegment(docTypeSlug) + ".md"

	cleanSub := strings.Trim(strings.TrimSpace(subdir), "/")
	if cleanSub == "" {
		return path.Join(repoSlug, docFile)
	}

	rawParts := strings.Split(cleanSub, "/")
	safeParts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if strings.TrimSpace(p) == "" || p == "." || p == ".." {
			continue
		}
		safeParts = append(safeParts, SanitizePublishSegment(p))
	}
	if len(safeParts) == 0 {
		return path.Join(repoSlug, docFile)
	}
	elems := append([]string{repoSlug, "sub"}, safeParts...)
	elems = append(elems, docFile)
	return path.Join(elems...)
}

// BuildParentDocRelLink 计算子模块落盘文档 (<repo>/sub/<part1>/.../<partD>/<docType>.md) 指向父级整仓同类型文档 (<repo>/<docType>.md) 的动态深度相对路径（ADR-04 D-5）。
func BuildParentDocRelLink(subdir, docTypeSlug string) string {
	cleanSub := strings.Trim(strings.TrimSpace(subdir), "/")
	if cleanSub == "" {
		return ""
	}
	rawParts := strings.Split(cleanSub, "/")
	depth := 0
	for _, p := range rawParts {
		if strings.TrimSpace(p) == "" || p == "." || p == ".." {
			continue
		}
		depth++
	}
	if depth == 0 {
		return ""
	}
	docFile := SanitizePublishSegment(docTypeSlug) + ".md"
	return strings.Repeat("../", depth+1) + docFile
}

// BuildChildModuleRelLink 计算整仓落盘文档 (<repo>/<docType>.md) 指向子模块同类型文档 (./sub/<sanitized_subdir>/<docType>.md) 的相对路径（ADR-04 D-5）。
func BuildChildModuleRelLink(subdir, docTypeSlug string) string {
	cleanSub := strings.Trim(strings.TrimSpace(subdir), "/")
	if cleanSub == "" {
		return ""
	}
	rawParts := strings.Split(cleanSub, "/")
	safeParts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if strings.TrimSpace(p) == "" || p == "." || p == ".." {
			continue
		}
		safeParts = append(safeParts, SanitizePublishSegment(p))
	}
	if len(safeParts) == 0 {
		return ""
	}
	docFile := SanitizePublishSegment(docTypeSlug) + ".md"
	return "./" + path.Join(append(append([]string{"sub"}, safeParts...), docFile)...)
}

// LocalFSPublishSink 基于本地受控 publish_dir 的原子落盘与索引重建实现（P0 同机符号链接挂载模式）。
type LocalFSPublishSink struct {
	rootDir string
	mu      sync.Mutex
}

func NewLocalFSPublishSink(rootDir string) (*LocalFSPublishSink, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, fmt.Errorf("publish_dir 不能为空")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 publish_dir 失败: %w", err)
	}
	return &LocalFSPublishSink{rootDir: rootDir}, nil
}

// RootDir 返回物理发布根目录路径。
func (s *LocalFSPublishSink) RootDir() string {
	return s.rootDir
}

// PublishDocument 持锁通过 .staging/tmp-<uuid>.md + fsync + 原子 rename 落盘文档并重建索引 README.md。
func (s *LocalFSPublishSink) PublishDocument(
	ctx context.Context,
	relPath string,
	oldRelPath string,
	content []byte,
	allPublished []PublishedDocEntry,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.atomicWriteFile(relPath, content); err != nil {
		return err
	}

	// 若仓库曾改名导致发布路径变化，先落盘新文件再安全移除旧路径并清理空目录
	if oldRelPath != "" && oldRelPath != relPath {
		if oldFull, err := sandbox.SafeJoin(s.rootDir, oldRelPath); err == nil {
			_ = os.Remove(oldFull)
			s.pruneEmptyDirsUpward(filepath.Dir(oldFull))
		}
	}

	return s.rebuildIndexes(allPublished, relPath, oldRelPath)
}

// UnpublishDocument 持锁删除目标落盘文件、重建或移除 README.md，并自底向上彻底清理空目录（SC-10）。
func (s *LocalFSPublishSink) UnpublishDocument(
	ctx context.Context,
	relPath string,
	remainingPublished []PublishedDocEntry,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(relPath) != "" {
		targetFull, err := sandbox.SafeJoin(s.rootDir, relPath)
		if err != nil {
			return fmt.Errorf("撤回路径安全校验失败: %w", err)
		}
		if rmErr := os.Remove(targetFull); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("删除已发布文档失败: %w", rmErr)
		}
	}

	if err := s.rebuildIndexes(remainingPublished, "", relPath); err != nil {
		return err
	}

	if strings.TrimSpace(relPath) != "" {
		if targetFull, err := sandbox.SafeJoin(s.rootDir, relPath); err == nil {
			s.pruneEmptyDirsUpward(filepath.Dir(targetFull))
		}
	}
	return nil
}

func (s *LocalFSPublishSink) atomicWriteFile(relPath string, content []byte) error {
	targetFull, err := sandbox.SafeJoin(s.rootDir, relPath)
	if err != nil {
		return fmt.Errorf("发布路径非法: %w", err)
	}

	stagingDir := filepath.Join(s.rootDir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("创建 .staging 目录失败: %w", err)
	}

	tmpPath := filepath.Join(stagingDir, fmt.Sprintf("tmp-%s.md", uuid.NewString()))
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("创建暂存文件失败: %w", err)
	}

	writeOK := false
	defer func() {
		if !writeOK {
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("写入暂存文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("刷盘暂存文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭暂存文件失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(targetFull), 0o755); err != nil {
		return fmt.Errorf("创建发布目标目录失败: %w", err)
	}
	if err := os.Rename(tmpPath, targetFull); err != nil {
		return fmt.Errorf("原子落位发布文件失败: %w", err)
	}
	writeOK = true
	return nil
}

func (s *LocalFSPublishSink) rebuildIndexes(entries []PublishedDocEntry, touchedRelPath, removedRelPath string) error {
	// 1. 根级 README.md：全部撤回为空时直接移除，否则原子重建
	if len(entries) == 0 {
		rootReadme := filepath.Join(s.rootDir, "README.md")
		if rmErr := os.Remove(rootReadme); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("移除根级 README.md 失败: %w", rmErr)
		}
	} else {
		rootMD := renderRootIndexMarkdown(entries)
		if err := s.atomicWriteFile("README.md", []byte(rootMD)); err != nil {
			return err
		}
	}

	// 2. 按目录分组（范围目录 & 仓库根目录）
	byScopeDir := make(map[string][]PublishedDocEntry)
	byRepoDir := make(map[string][]PublishedDocEntry)
	for _, e := range entries {
		normRel := path.Clean(strings.TrimPrefix(e.RelPath, "/"))
		scopeDir := path.Dir(normRel)
		if scopeDir != "." && scopeDir != "" {
			byScopeDir[scopeDir] = append(byScopeDir[scopeDir], e)
		}
		parts := strings.Split(normRel, "/")
		if len(parts) >= 2 && parts[0] != "" {
			byRepoDir[parts[0]] = append(byRepoDir[parts[0]], e)
		}
	}

	// 3. 收集受影响的目录集合（含本次写入与撤回涉及的目录及其父仓库目录）
	affectedDirs := make(map[string]bool)
	for _, p := range []string{touchedRelPath, removedRelPath} {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		norm := path.Clean(strings.TrimPrefix(p, "/"))
		if dir := path.Dir(norm); dir != "." && dir != "" {
			affectedDirs[dir] = true
		}
		if parts := strings.Split(norm, "/"); len(parts) >= 2 && parts[0] != "" {
			affectedDirs[parts[0]] = true
		}
	}
	for dir := range byScopeDir {
		affectedDirs[dir] = true
	}
	for dir := range byRepoDir {
		affectedDirs[dir] = true
	}

	sortedDirs := make([]string, 0, len(affectedDirs))
	for dir := range affectedDirs {
		sortedDirs = append(sortedDirs, dir)
	}
	sort.Strings(sortedDirs)

	for _, dir := range sortedDirs {
		readmeRel := path.Join(dir, "README.md")
		if scopeDocs := byScopeDir[dir]; len(scopeDocs) > 0 {
			// 若该目录同时也是仓库根目录，把旗下子目录文档也一并纳入索引
			allUnderDir := scopeDocs
			if repoDocs := byRepoDir[dir]; len(repoDocs) > len(scopeDocs) {
				allUnderDir = repoDocs
			}
			md := renderDirIndexMarkdown(dir, allUnderDir)
			if err := s.atomicWriteFile(readmeRel, []byte(md)); err != nil {
				return err
			}
		} else if repoDocs := byRepoDir[dir]; len(repoDocs) > 0 {
			md := renderDirIndexMarkdown(dir, repoDocs)
			if err := s.atomicWriteFile(readmeRel, []byte(md)); err != nil {
				return err
			}
		} else {
			// 该目录已无任何已发布文档：删除 README.md 并清理空目录
			if fullReadme, err := sandbox.SafeJoin(s.rootDir, readmeRel); err == nil {
				if rmErr := os.Remove(fullReadme); rmErr != nil && !os.IsNotExist(rmErr) {
					return fmt.Errorf("移除范围 README.md 失败: %w", rmErr)
				}
				s.pruneEmptyDirsUpward(filepath.Dir(fullReadme))
			}
		}
	}

	return nil
}

// pruneEmptyDirsUpward 自底向上清理空目录，严格止步于 rootDir 之下且不触碰 .staging。
func (s *LocalFSPublishSink) pruneEmptyDirsUpward(startDir string) {
	realRoot, err := filepath.EvalSymlinks(s.rootDir)
	if err != nil {
		realRoot = filepath.Clean(s.rootDir)
	}
	cur := filepath.Clean(startDir)
	for {
		realCur, rErr := filepath.EvalSymlinks(cur)
		if rErr != nil {
			realCur = cur
		}
		if realCur == realRoot || !strings.HasPrefix(realCur, realRoot+string(os.PathSeparator)) {
			break
		}
		if filepath.Base(realCur) == ".staging" {
			break
		}
		entries, readErr := os.ReadDir(realCur)
		if readErr != nil || len(entries) > 0 {
			break
		}
		if rmErr := os.Remove(realCur); rmErr != nil {
			break
		}
		cur = filepath.Dir(realCur)
	}
}

func renderRootIndexMarkdown(entries []PublishedDocEntry) string {
	sorted := make([]PublishedDocEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].RepoName != sorted[j].RepoName {
			return sorted[i].RepoName < sorted[j].RepoName
		}
		if sorted[i].Subdirectory != sorted[j].Subdirectory {
			return sorted[i].Subdirectory < sorted[j].Subdirectory
		}
		return sorted[i].DocTypeSlug < sorted[j].DocTypeSlug
	})

	var sb strings.Builder
	sb.WriteString("# 代码设计文档索引\n\n")
	sb.WriteString("| 代码仓库 | 范围 | 文档类型 | 文档链接 | 提交哈希 |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
	for _, e := range sorted {
		scopeLabel := "整仓"
		if e.Subdirectory != "" {
			scopeLabel = "`" + e.Subdirectory + "`"
		}
		shortSHA := e.CommitSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		title := e.Title
		if title == "" {
			title = e.DocTypeName
		}
		if title == "" {
			title = e.DocTypeSlug
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | [%s](./%s) | `%s` |\n",
			e.RepoName, scopeLabel, e.DocTypeName, title, e.RelPath, shortSHA)
	}
	return sb.String()
}

func renderDirIndexMarkdown(dir string, entries []PublishedDocEntry) string {
	sorted := make([]PublishedDocEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Subdirectory != sorted[j].Subdirectory {
			return sorted[i].Subdirectory < sorted[j].Subdirectory
		}
		return sorted[i].RelPath < sorted[j].RelPath
	})

	isSubdirIndex := strings.Contains(dir, "/sub/")
	heading := dir
	if len(sorted) > 0 {
		heading = sorted[0].RepoName
		if isSubdirIndex && sorted[0].Subdirectory != "" {
			heading = fmt.Sprintf("%s (%s)", sorted[0].RepoName, sorted[0].Subdirectory)
		}
	}

	writeEntryBullet := func(sb *strings.Builder, e PublishedDocEntry) {
		relFromDir := strings.TrimPrefix(e.RelPath, dir+"/")
		if relFromDir == e.RelPath {
			relFromDir = path.Base(e.RelPath)
		}
		label := e.Title
		if label == "" {
			label = e.DocTypeName
		}
		if label == "" {
			label = e.DocTypeSlug
		}
		shortSHA := e.CommitSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		fmt.Fprintf(sb, "- [%s](./%s) (`%s`)\n", label, relFromDir, shortSHA)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s 文档导航\n\n", heading)

	if isSubdirIndex {
		for _, e := range sorted {
			writeEntryBullet(&sb, e)
		}
		return sb.String()
	}

	// 仓库根级 README.md 采用「整仓文档 + 模块文档」两级分组结构（ADR-04 D-5）
	var repoDocs []PublishedDocEntry
	var modDocs []PublishedDocEntry
	for _, e := range sorted {
		if strings.TrimSpace(e.Subdirectory) == "" {
			repoDocs = append(repoDocs, e)
		} else {
			modDocs = append(modDocs, e)
		}
	}

	if len(repoDocs) > 0 {
		sb.WriteString("## 整仓文档\n\n")
		for _, e := range repoDocs {
			writeEntryBullet(&sb, e)
		}
		if len(modDocs) > 0 {
			sb.WriteString("\n")
		}
	}

	if len(modDocs) > 0 {
		sb.WriteString("## 模块文档\n\n")
		curSub := ""
		for _, e := range modDocs {
			if e.Subdirectory != curSub {
				if curSub != "" {
					sb.WriteString("\n")
				}
				curSub = e.Subdirectory
				fmt.Fprintf(&sb, "### 模块：`%s`\n\n", curSub)
			}
			writeEntryBullet(&sb, e)
		}
	}

	return sb.String()
}
