package codeir

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// BuildExtractor 解析 CMakeLists.txt / compile_commands.json / Makefile / BUILD(.bazel)。
type BuildExtractor struct{}

func NewBuildExtractor() *BuildExtractor {
	return &BuildExtractor{}
}

func (e *BuildExtractor) Name() string { return "build-system" }

func (e *BuildExtractor) SupportedLanguages() []string { return []string{"build"} }

func (e *BuildExtractor) CanHandle(relPath string) bool {
	base := strings.ToLower(path.Base(relPath))
	switch base {
	case "cmakelists.txt", "compile_commands.json", "makefile", "gnumakefile", "build", "build.bazel",
		"pom.xml", "build.gradle", "build.gradle.kts", "pyproject.toml", "requirements.txt":
		return true
	default:
		if strings.HasSuffix(base, ".cmake") {
			return true
		}
		return strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")
	}
}

func (e *BuildExtractor) ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	base := strings.ToLower(path.Base(file.RelPath))
	switch {
	case base == "cmakelists.txt" || strings.HasSuffix(base, ".cmake"):
		return e.parseCMake(file.RelPath, string(content)), nil
	case base == "compile_commands.json":
		return e.parseCompileCommands(file.RelPath, content), nil
	case base == "makefile" || base == "gnumakefile":
		return e.parseMakefile(file.RelPath, string(content)), nil
	case base == "build" || base == "build.bazel":
		return e.parseBazel(file.RelPath, string(content)), nil
	case base == "pom.xml":
		return e.parseMavenPom(file.RelPath, content), nil
	case base == "build.gradle" || base == "build.gradle.kts":
		return e.parseGradle(file.RelPath, string(content)), nil
	case base == "pyproject.toml":
		return e.parsePyProjectToml(file.RelPath, string(content)), nil
	case base == "requirements.txt" || (strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")):
		return e.parseRequirementsTxt(file.RelPath, string(content)), nil
	default:
		return &ExtractionResult{}, nil
	}
}

var (
	reCMakeCall     = regexp.MustCompile(`(?is)\b(project|set|add_executable|add_library|find_package|target_link_libraries|add_compile_definitions|add_definitions|target_compile_definitions|add_subdirectory|include_directories|target_include_directories)\s*\(([^)]*)\)`)
	reMakefileTgt   = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*:(?:[^=]|$)`)
	reBazelRule     = regexp.MustCompile(`(?s)\b(cc_library|cc_binary|cc_test|proto_library)\s*\((.*?)\n\)`)
	reBazelName     = regexp.MustCompile(`name\s*=\s*["']([^"']+)["']`)
	reBazelQuoted   = regexp.MustCompile(`["']([^"']+)["']`)
	reBazelDepsList = regexp.MustCompile(`(?s)deps\s*=\s*\[(.*?)\]`)
	reBazelSrcsList = regexp.MustCompile(`(?s)srcs\s*=\s*\[(.*?)\]`)
)

func (e *BuildExtractor) parseCMake(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"cmake"},
	}

	// 按行剥离 CMake # 注释，但保留行号对应关系
	lines := strings.Split(src, "\n")
	cleanedLines := make([]string, len(lines))
	for i, line := range lines {
		if idx := strings.Index(line, "#"); idx >= 0 {
			cleanedLines[i] = line[:idx]
		} else {
			cleanedLines[i] = line
		}
	}
	cleaned := strings.Join(cleanedLines, "\n")

	targetIndex := make(map[string]int)

	matches := reCMakeCall.FindAllStringSubmatchIndex(cleaned, -1)
	for _, loc := range matches {
		cmd := strings.ToLower(cleaned[loc[2]:loc[3]])
		argsRaw := cleaned[loc[4]:loc[5]]
		startLine := 1 + strings.Count(cleaned[:loc[0]], "\n")
		endLine := 1 + strings.Count(cleaned[:loc[1]], "\n")
		tokens := tokenizeCMakeArgs(argsRaw)
		if len(tokens) == 0 {
			continue
		}

		span := SourceSpan{FilePath: filePath, LineStart: startLine, LineEnd: endLine}
		meta := FactMeta{Confidence: ConfidenceExact, Span: span, ContentFingerprint: fingerprintString(cmd + ":" + strings.Join(tokens, " "))}

		switch cmd {
		case "set":
			if len(tokens) >= 2 && strings.HasPrefix(strings.ToUpper(tokens[0]), "CMAKE_") {
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    tokens[0],
					Value:  tokens[1],
					Source: "cmake",
					Meta:   meta,
				})
			}
		case "add_compile_definitions", "add_definitions":
			for _, tok := range tokens {
				cleanTok := strings.TrimPrefix(tok, "-D")
				if cleanTok == "" {
					continue
				}
				k, v := splitKeyValue(cleanTok, "=")
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    k,
					Value:  v,
					Source: "cmake",
					Meta:   meta,
				})
			}
		case "target_compile_definitions":
			for i := 1; i < len(tokens); i++ {
				up := strings.ToUpper(tokens[i])
				if up == "PRIVATE" || up == "PUBLIC" || up == "INTERFACE" {
					continue
				}
				cleanTok := strings.TrimPrefix(tokens[i], "-D")
				k, v := splitKeyValue(cleanTok, "=")
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    k,
					Value:  v,
					Source: "cmake",
					Meta:   meta,
				})
			}
		case "find_package":
			pkgName := tokens[0]
			version := ""
			if len(tokens) >= 2 && isVersionLike(tokens[1]) {
				version = tokens[1]
			}
			res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
				Name:       pkgName,
				Version:    version,
				Scope:      "compile",
				SourceFile: filePath,
				Meta:       meta,
			})
		case "add_executable":
			name := tokens[0]
			var srcs []string
			for _, tok := range tokens[1:] {
				if isCMakeKeyword(tok) {
					continue
				}
				srcs = append(srcs, tok)
			}
			targetIndex[name] = len(res.Build)
			res.Build = append(res.Build, BuildTarget{
				Name:        name,
				Kind:        "executable",
				BuildSystem: "cmake",
				Sources:     srcs,
				Meta:        meta,
			})
		case "add_library":
			name := tokens[0]
			kind := "library"
			var srcs []string
			for _, tok := range tokens[1:] {
				switch strings.ToUpper(tok) {
				case "STATIC":
					kind = "static_library"
				case "SHARED":
					kind = "shared_library"
				case "INTERFACE", "OBJECT", "MODULE", "ALIAS":
					kind = strings.ToLower(tok) + "_library"
				default:
					if !isCMakeKeyword(tok) {
						srcs = append(srcs, tok)
					}
				}
			}
			targetIndex[name] = len(res.Build)
			res.Build = append(res.Build, BuildTarget{
				Name:        name,
				Kind:        kind,
				BuildSystem: "cmake",
				Sources:     srcs,
				Meta:        meta,
			})
		case "target_link_libraries":
			tgtName := tokens[0]
			var libs []string
			for _, tok := range tokens[1:] {
				up := strings.ToUpper(tok)
				if up == "PRIVATE" || up == "PUBLIC" || up == "INTERFACE" {
					continue
				}
				libs = append(libs, tok)
			}
			if idx, ok := targetIndex[tgtName]; ok {
				res.Build[idx].LinkLibs = append(res.Build[idx].LinkLibs, libs...)
			}
			for _, lib := range libs {
				if _, isLocalTarget := targetIndex[lib]; isLocalTarget {
					continue
				}
				res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
					Name:       lib,
					Scope:      "compile",
					SourceFile: filePath,
					Meta:       meta,
				})
			}
		case "add_subdirectory":
			subRaw := normalizeCMakeRelDir(filePath, tokens[0])
			if subRaw != "" {
				res.Subdirectories = append(res.Subdirectories, BuildSubdirFact{
					ParentBuildFile: filePath,
					Subdirectory:    subRaw,
					BuildSystem:     "cmake",
					Meta:            meta,
				})
			}
		case "include_directories":
			for _, tok := range tokens {
				up := strings.ToUpper(tok)
				if up == "AFTER" || up == "BEFORE" || up == "SYSTEM" {
					continue
				}
				if incDir := normalizeCMakeRelDir(filePath, tok); incDir != "" {
					res.IncludeDirs = append(res.IncludeDirs, BuildIncludeDirFact{
						BuildFile:  filePath,
						IncludeDir: incDir,
						Meta:       meta,
					})
				}
			}
		case "target_include_directories":
			tgtName := tokens[0]
			for _, tok := range tokens[1:] {
				up := strings.ToUpper(tok)
				if up == "PRIVATE" || up == "PUBLIC" || up == "INTERFACE" || up == "SYSTEM" || up == "BEFORE" || up == "AFTER" {
					continue
				}
				if incDir := normalizeCMakeRelDir(filePath, tok); incDir != "" {
					res.IncludeDirs = append(res.IncludeDirs, BuildIncludeDirFact{
						BuildFile:  filePath,
						Target:     tgtName,
						IncludeDir: incDir,
						Meta:       meta,
					})
				}
			}
		}
	}

	return res
}

type compileCommandEntry struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments"`
	Command   string   `json:"command"`
}

func (e *BuildExtractor) parseCompileCommands(filePath string, content []byte) *ExtractionResult {
	res := &ExtractionResult{}
	var entries []compileCommandEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		res.Diagnostics = append(res.Diagnostics, ParseDiagnostic{
			FilePath: filePath,
			Message:  "解析 compile_commands.json 失败: " + err.Error(),
		})
		return res
	}

	seen := make(map[string]bool)
	for _, entry := range entries {
		args := entry.Arguments
		if len(args) == 0 && entry.Command != "" {
			args = strings.Fields(entry.Command)
		}
		for i := 0; i < len(args); i++ {
			arg := strings.TrimSpace(args[i])
			switch {
			case strings.HasPrefix(arg, "-D") && len(arg) > 2:
				if seen[arg] {
					continue
				}
				seen[arg] = true
				k, v := splitKeyValue(strings.TrimPrefix(arg, "-D"), "=")
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    arg,
					Value:  v,
					Source: "compile_commands",
					Meta: FactMeta{
						Confidence:         ConfidenceExact,
						Span:               SourceSpan{FilePath: filePath, LineStart: 1, LineEnd: 1},
						ContentFingerprint: fingerprintString("cc:" + k + "=" + v),
					},
				})
			case strings.HasPrefix(arg, "-std="):
				if seen[arg] {
					continue
				}
				seen[arg] = true
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    "std",
					Value:  strings.TrimPrefix(arg, "-std="),
					Source: "compile_commands",
					Meta: FactMeta{
						Confidence:         ConfidenceExact,
						Span:               SourceSpan{FilePath: filePath, LineStart: 1, LineEnd: 1},
						ContentFingerprint: fingerprintString("cc:" + arg),
					},
				})
			case strings.HasPrefix(arg, "-I") && len(arg) > 2:
				if seen[arg] {
					continue
				}
				seen[arg] = true
				incRaw := strings.TrimPrefix(arg, "-I")
				meta := FactMeta{
					Confidence:         ConfidenceExact,
					Span:               SourceSpan{FilePath: filePath, LineStart: 1, LineEnd: 1},
					ContentFingerprint: fingerprintString("cc:" + arg),
				}
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    "include_dir",
					Value:  incRaw,
					Source: "compile_commands",
					Meta:   meta,
				})
				if incClean := normalizeCMakeRelDir(filePath, incRaw); incClean != "" {
					res.IncludeDirs = append(res.IncludeDirs, BuildIncludeDirFact{
						BuildFile:  filePath,
						IncludeDir: incClean,
						Meta:       meta,
					})
				}
			}
		}
	}
	return res
}

func (e *BuildExtractor) parseMakefile(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"makefile"},
	}
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "CXXFLAGS") || strings.HasPrefix(line, "CFLAGS") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(strings.TrimRight(parts[0], "+:?"))
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    key,
					Value:  strings.TrimSpace(parts[1]),
					Source: "makefile",
					Meta: FactMeta{
						Confidence: ConfidenceExact,
						Span:       SourceSpan{FilePath: filePath, LineStart: i + 1, LineEnd: i + 1},
					},
				})
			}
			continue
		}
		if m := reMakefileTgt.FindStringSubmatch(raw); len(m) == 2 {
			tgt := m[1]
			if strings.HasPrefix(tgt, ".") {
				continue
			}
			res.Build = append(res.Build, BuildTarget{
				Name:        tgt,
				Kind:        "target",
				BuildSystem: "makefile",
				Meta: FactMeta{
					Confidence: ConfidenceExact,
					Span:       SourceSpan{FilePath: filePath, LineStart: i + 1, LineEnd: i + 1},
				},
			})
		}
	}
	return res
}

func (e *BuildExtractor) parseBazel(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"bazel"},
	}
	matches := reBazelRule.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range matches {
		ruleKind := src[loc[2]:loc[3]]
		body := src[loc[4]:loc[5]]
		startLine := 1 + strings.Count(src[:loc[0]], "\n")
		endLine := 1 + strings.Count(src[:loc[1]], "\n")

		nameM := reBazelName.FindStringSubmatch(body)
		if len(nameM) < 2 {
			continue
		}
		name := nameM[1]
		var srcs, deps []string
		if sm := reBazelSrcsList.FindStringSubmatch(body); len(sm) == 2 {
			for _, q := range reBazelQuoted.FindAllStringSubmatch(sm[1], -1) {
				srcs = append(srcs, q[1])
			}
		}
		if dm := reBazelDepsList.FindStringSubmatch(body); len(dm) == 2 {
			for _, q := range reBazelQuoted.FindAllStringSubmatch(dm[1], -1) {
				deps = append(deps, q[1])
			}
		}
		kind := "library"
		if ruleKind == "cc_binary" {
			kind = "executable"
		}
		res.Build = append(res.Build, BuildTarget{
			Name:        name,
			Kind:        kind,
			BuildSystem: "bazel",
			Sources:     srcs,
			LinkLibs:    deps,
			Meta: FactMeta{
				Confidence: ConfidenceExact,
				Span:       SourceSpan{FilePath: filePath, LineStart: startLine, LineEnd: endLine},
			},
		})
	}
	return res
}

func tokenizeCMakeArgs(raw string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func splitKeyValue(s, sep string) (string, string) {
	parts := strings.SplitN(s, sep, 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(s), "1"
}

func isVersionLike(s string) bool {
	if len(s) == 0 {
		return false
	}
	return s[0] >= '0' && s[0] <= '9'
}

func isCMakeKeyword(tok string) bool {
	switch strings.ToUpper(tok) {
	case "WIN32", "MACOSX_BUNDLE", "EXCLUDE_FROM_ALL", "IMPORTED", "GLOBAL", "REQUIRED", "QUIET", "CONFIG", "MODULE":
		return true
	default:
		return false
	}
}

func normalizeCMakeRelDir(buildFilePath, raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	baseDir := path.Dir(buildFilePath)
	if baseDir == "." {
		baseDir = ""
	}
	for _, v := range []string{"${CMAKE_SOURCE_DIR}", "${PROJECT_SOURCE_DIR}"} {
		if strings.HasPrefix(s, v) {
			rest := strings.TrimPrefix(s[len(v):], "/")
			cleaned := path.Clean(rest)
			if cleaned == "." || strings.HasPrefix(cleaned, "..") {
				return ""
			}
			return cleaned
		}
	}
	for _, v := range []string{"${CMAKE_CURRENT_SOURCE_DIR}", "${CMAKE_CURRENT_LIST_DIR}"} {
		if strings.HasPrefix(s, v) {
			s = strings.TrimPrefix(s[len(v):], "/")
			break
		}
	}
	if strings.Contains(s, "${") || strings.HasPrefix(s, "/") {
		return ""
	}
	joined := s
	if baseDir != "" {
		joined = path.Join(baseDir, s)
	}
	cleaned := path.Clean(joined)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return ""
	}
	return cleaned
}
