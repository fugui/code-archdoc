package services

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"code-archdoc/models"
	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/sandbox"
	"code-common/backend/gitsync"
)

const maxSourcePreviewBytes = 512 * 1024 // 512KB 截断阈值

var reCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// SourcePreviewRequest 对应 GET /api/documents/:id/source-preview 查询参数。
type SourcePreviewRequest struct {
	DocID     uint
	Commit    string
	File      string
	LineStart int
	LineEnd   int
	Symbol    string
	CanWrite  bool
}

// SourcePreviewVO 对应 GET /api/documents/:id/source-preview 响应（01-http-api-contract.md §3.5）。
type SourcePreviewVO struct {
	RepoID            uint   `json:"repo_id"`
	Subdirectory      string `json:"subdirectory"`
	CommitSHA         string `json:"commit_sha"`
	FilePath          string `json:"file_path"`
	Language          string `json:"language"`
	TotalLines        int    `json:"total_lines"`
	HighlightStart    int    `json:"highlight_start"`
	HighlightEnd      int    `json:"highlight_end"`
	RelocatedBySymbol bool   `json:"relocated_by_symbol"`
	Content           string `json:"content"`
}

// SourcePreviewService 负责受控沙箱内只读源码预览、目录穿越/符号链接拦截、缺失沙箱自动重拉与按符号重定位（F-08 / SC-06）。
type SourcePreviewService struct {
	docRepo    DocumentRepo
	masterRepo SharedMasterRepo
	gitSyncer  GitSyncer
	aggregator *codeir.Aggregator
	sandboxDir string
	leaseMgr   *ActiveLeaseManager
}

// WithLeaseManager 注入活跃租约管理器，在源码预览读取期间防止沙箱目录被并发回收（F-27）。
func (s *SourcePreviewService) WithLeaseManager(leaseMgr *ActiveLeaseManager) *SourcePreviewService {
	s.leaseMgr = leaseMgr
	return s
}

func NewSourcePreviewService(
	docRepo DocumentRepo,
	masterRepo SharedMasterRepo,
	gitSyncer GitSyncer,
	aggregator *codeir.Aggregator,
	sandboxDir string,
) *SourcePreviewService {
	if gitSyncer == nil {
		gitSyncer = DefaultGitSyncer{}
	}
	return &SourcePreviewService{
		docRepo:    docRepo,
		masterRepo: masterRepo,
		gitSyncer:  gitSyncer,
		aggregator: aggregator,
		sandboxDir: sandboxDir,
	}
}

// GetSourcePreview 执行权限隔离、SafeJoin 沙箱边界校验、按 commit 读取或自动重拉以及按 symbol 重定位。
func (s *SourcePreviewService) GetSourcePreview(ctx context.Context, req SourcePreviewRequest) (*SourcePreviewVO, error) {
	fileRaw := strings.TrimSpace(req.File)
	if fileRaw == "" {
		return nil, fmt.Errorf("%w: file 参数不能为空", ErrInvalidRevisionParam)
	}

	// 前置词法拦截：即便沙箱尚未拉取，也要立刻以 403 PATH_ESCAPE 阻断目录穿越、空字节、反斜杠与绝对路径（SC-06）
	if isLexicalPathEscape(fileRaw) {
		return nil, sandbox.ErrPathEscape
	}

	commit := strings.TrimSpace(req.Commit)
	if commit != "" && !reCommitSHA.MatchString(commit) {
		return nil, fmt.Errorf("%w: commit 参数必须为 7~40 位十六进制哈希", ErrInvalidRevisionParam)
	}

	doc, err := s.docRepo.GetByID(ctx, req.DocID)
	if err != nil {
		return nil, err
	}

	// 草稿隔离硬边界：普通用户仅允许查看已发布文档的源码证据
	var viewRev *models.DocumentRevision
	if !req.CanWrite {
		if doc.Status != models.DocStatusApproved || doc.PublishedRevisionID == nil {
			return nil, ErrDraftAccessDenied
		}
		rev, _, rErr := s.docRepo.GetRevisionByIDWithSections(ctx, doc.ID, *doc.PublishedRevisionID)
		if rErr != nil {
			return nil, rErr
		}
		viewRev = rev
	} else if doc.CurrentRevisionNo > 0 {
		rev, _, rErr := s.docRepo.GetRevisionWithSections(ctx, doc.ID, doc.CurrentRevisionNo)
		if rErr == nil {
			viewRev = rev
		}
	}

	if commit == "" && viewRev != nil {
		commit = viewRev.CommitSHA
	}

	repo, _ := s.masterRepo.GetRepository(ctx, doc.RepoID)

	releaseRepoLease := s.leaseMgr.AcquireRepo(doc.RepoID)
	defer releaseRepoLease()

	// 定位本地沙箱目录；若已被 LRU 回收缺失，则按仓库配置自动重新拉取
	repoDir := filepath.Join(s.sandboxDir, fmt.Sprintf("repo_%d", doc.RepoID))
	if _, statErr := os.Stat(repoDir); os.IsNotExist(statErr) {
		if repo != nil && strings.TrimSpace(repo.URL) != "" && s.gitSyncer != nil {
			syncRes, syncErr := s.gitSyncer.Sync(ctx, gitsync.SyncOptions{
				RepoURL:        repo.URL,
				LocalPath:      repoDir,
				Branch:         doc.RefName,
				Depth:          1,
				AutoHealCache:  true,
				CleanUntracked: true,
			})
			if syncErr != nil {
				return nil, fmt.Errorf("自动重拉代码沙箱失败: %w", syncErr)
			}
			if syncRes != nil && syncRes.LocalPath != "" {
				repoDir = syncRes.LocalPath
			}
		} else {
			return nil, models.ErrNotFound
		}
	}

	scopeRoot := repoDir
	if doc.Subdirectory != "" {
		joinedScope, sErr := sandbox.SafeJoin(repoDir, doc.Subdirectory)
		if sErr != nil {
			return nil, sandbox.ErrPathEscape
		}
		scopeRoot = joinedScope
	}

	// 强制经 SafeJoin + EvalSymlinks 阻断目录穿越与仓库内指向沙箱外文件的符号链接逃逸（SC-06）
	targetPath, safeErr := sandbox.SafeJoin(scopeRoot, fileRaw)
	if safeErr != nil {
		return nil, sandbox.ErrPathEscape
	}

	cleanRelFile := path.Clean(strings.TrimPrefix(fileRaw, "./"))
	contentBytes, err := s.readSourceBytes(ctx, repoDir, repo, doc.Subdirectory, commit, cleanRelFile, targetPath)
	if err != nil {
		return nil, err
	}

	lang := detectSourceLanguage(cleanRelFile)
	normalizedContent := strings.ReplaceAll(string(contentBytes), "\r\n", "\n")
	allLines := strings.Split(normalizedContent, "\n")
	totalLines := len(allLines)

	hlStart := req.LineStart
	hlEnd := req.LineEnd
	relocated := false

	if symQuery := strings.TrimSpace(req.Symbol); symQuery != "" {
		if sStart, sEnd, found := s.relocateSymbol(ctx, cleanRelFile, lang, contentBytes, allLines, symQuery); found {
			if sStart != hlStart || sEnd != hlEnd {
				relocated = true
			}
			hlStart = sStart
			hlEnd = sEnd
		}
	}

	if hlStart < 0 {
		hlStart = 0
	}
	if hlStart > totalLines {
		hlStart = totalLines
	}
	if hlEnd < hlStart {
		hlEnd = hlStart
	}
	if hlEnd > totalLines {
		hlEnd = totalLines
	}

	displayContent := normalizedContent
	if len(displayContent) > maxSourcePreviewBytes {
		displayContent = truncateAroundWindow(allLines, hlStart, hlEnd)
	}

	return &SourcePreviewVO{
		RepoID:            doc.RepoID,
		Subdirectory:      doc.Subdirectory,
		CommitSHA:         commit,
		FilePath:          cleanRelFile,
		Language:          lang,
		TotalLines:        totalLines,
		HighlightStart:    hlStart,
		HighlightEnd:      hlEnd,
		RelocatedBySymbol: relocated,
		Content:           displayContent,
	}, nil
}

func (s *SourcePreviewService) readSourceBytes(
	ctx context.Context,
	repoDir string,
	repo *models.Repository,
	subdir string,
	commitSHA string,
	cleanRelFile string,
	safeTargetPath string,
) ([]byte, error) {
	// 若沙箱包含 .git 且指定了 commitSHA，优先按精确提交快照读取（不切换工作区 HEAD）
	if commitSHA != "" {
		if _, gitErr := os.Stat(filepath.Join(repoDir, ".git")); gitErr == nil {
			gitRelPath := cleanRelFile
			if subdir != "" {
				gitRelPath = path.Join(subdir, cleanRelFile)
			}
			if data, err := gitsync.ShowFileAtCommit(ctx, repoDir, commitSHA, gitRelPath); err == nil {
				return data, nil
			}
			// 若浅克隆缺失历史提交，尝试按提交哈希拉取对象后重试
			if repo != nil && strings.TrimSpace(repo.URL) != "" {
				if fetchErr := gitsync.FetchCommitMetadata(ctx, repoDir, repo.URL, commitSHA); fetchErr == nil {
					if data, err := gitsync.ShowFileAtCommit(ctx, repoDir, commitSHA, gitRelPath); err == nil {
						return data, nil
					}
				}
			}
		}
	}

	// 回退读取经 SafeJoin 验证的工作区物理文件（兼容无 .git 的夹具目录与当前 HEAD 文件）
	info, err := os.Stat(safeTargetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: 目标路径为目录而非文件", ErrInvalidRevisionParam)
	}
	return os.ReadFile(safeTargetPath)
}

func (s *SourcePreviewService) relocateSymbol(
	ctx context.Context,
	relPath string,
	lang string,
	content []byte,
	lines []string,
	symbol string,
) (int, int, bool) {
	shortSym := symbol
	if idx := strings.LastIndex(shortSym, "::"); idx >= 0 {
		shortSym = shortSym[idx+2:]
	} else if idx := strings.LastIndex(shortSym, "/"); idx >= 0 {
		shortSym = shortSym[idx+1:]
	} else if idx := strings.LastIndex(shortSym, "."); idx >= 0 {
		shortSym = shortSym[idx+1:]
	}

	// 1. 优先复用 L1 AST 提取器精确重定位
	if s.aggregator != nil {
		syms := s.aggregator.ExtractSingleFileSymbols(ctx, relPath, lang, content)
		for _, sf := range syms {
			if sf.FQSymbol == symbol || sf.FQSymbol == shortSym ||
				strings.HasSuffix(sf.FQSymbol, "::"+shortSym) ||
				strings.HasSuffix(sf.FQSymbol, "."+shortSym) {
				if sf.Meta.Span.LineStart > 0 {
					end := sf.Meta.Span.LineEnd
					if end < sf.Meta.Span.LineStart {
						end = sf.Meta.Span.LineStart
					}
					return sf.Meta.Span.LineStart, end, true
				}
			}
		}
	}

	// 2. 轻量文本兜底定位（兼容 .proto / 配置键 / 非 C++ 源码）
	for i, line := range lines {
		if strings.Contains(line, symbol) || (shortSym != "" && strings.Contains(line, shortSym)) {
			return i + 1, i + 1, true
		}
	}
	return 0, 0, false
}

func isLexicalPathEscape(raw string) bool {
	if strings.ContainsRune(raw, '\x00') || strings.Contains(raw, `\`) {
		return true
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return true
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return true
		}
	}
	cleaned := path.Clean(raw)
	return cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/")
}

func detectSourceLanguage(relPath string) string {
	ext := strings.ToLower(path.Ext(relPath))
	switch ext {
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".h":
		return "cpp"
	case ".c":
		return "c"
	case ".proto":
		return "protobuf"
	case ".go":
		return "go"
	case ".java":
		return "java"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".sql":
		return "sql"
	case ".md", ".markdown":
		return "markdown"
	case ".sh", ".bash":
		return "bash"
	case ".toml":
		return "toml"
	case ".ini", ".conf", ".cfg":
		return "ini"
	default:
		base := strings.ToLower(path.Base(relPath))
		if base == "cmakelists.txt" {
			return "cmake"
		}
		if base == "makefile" {
			return "makefile"
		}
		return "plaintext"
	}
}

func truncateAroundWindow(lines []string, hlStart, hlEnd int) string {
	if len(lines) == 0 {
		return ""
	}
	center := hlStart
	if center <= 0 {
		center = 1
	}
	startIdx := center - 200
	if startIdx < 0 {
		startIdx = 0
	}
	endIdx := hlEnd + 200
	if hlEnd <= 0 {
		endIdx = startIdx + 400
	}
	if endIdx > len(lines) {
		endIdx = len(lines)
	}
	var sb strings.Builder
	if startIdx > 0 {
		fmt.Fprintf(&sb, "// ... 已省略前 %d 行（文件超过 512KB 自动截取上下文窗口） ...\n", startIdx)
	}
	sb.WriteString(strings.Join(lines[startIdx:endIdx], "\n"))
	if endIdx < len(lines) {
		fmt.Fprintf(&sb, "\n// ... 已省略后 %d 行 ...", len(lines)-endIdx)
	}
	return sb.String()
}
