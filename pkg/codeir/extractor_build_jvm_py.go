package codeir

import (
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"regexp"
	"strings"
)

// ─── Maven pom.xml 解析器 ───

type mavenProjectXML struct {
	GroupID      string               `xml:"groupId"`
	ArtifactID   string               `xml:"artifactId"`
	Version      string               `xml:"version"`
	Packaging    string               `xml:"packaging"`
	Modules      []string             `xml:"modules>module"`
	Properties   mavenPropertiesXML   `xml:"properties"`
	Dependencies []mavenDependencyXML `xml:"dependencies>dependency"`
}

type mavenPropertiesXML struct {
	Entries map[string]string
}

func (p *mavenPropertiesXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	p.Entries = make(map[string]string)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			var val string
			if decErr := d.DecodeElement(&val, &el); decErr != nil {
				return decErr
			}
			p.Entries[el.Name.Local] = strings.TrimSpace(val)
		case xml.EndElement:
			if el.Name == start.Name {
				return nil
			}
		}
	}
}

type mavenDependencyXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
}

func (e *BuildExtractor) parseMavenPom(filePath string, content []byte) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"maven"},
	}

	// Go encoding/xml 默认禁用外部实体与网络请求，显式清空 Entity 映射以防 XXE
	dec := xml.NewDecoder(bytes.NewReader(content))
	dec.Strict = false
	dec.Entity = map[string]string{}

	var proj mavenProjectXML
	if err := dec.Decode(&proj); err != nil {
		res.Diagnostics = append(res.Diagnostics, ParseDiagnostic{
			FilePath: filePath,
			Message:  "解析 pom.xml 失败: " + err.Error(),
		})
		return res
	}

	span := SourceSpan{FilePath: filePath, LineStart: 1, LineEnd: 1}
	if art := strings.TrimSpace(proj.ArtifactID); art != "" {
		kind := strings.TrimSpace(proj.Packaging)
		if kind == "" {
			kind = "jar"
		}
		res.Build = append(res.Build, BuildTarget{
			Name:        art,
			Kind:        kind,
			BuildSystem: "maven",
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               span,
				ContentFingerprint: fingerprintString("maven:" + proj.GroupID + ":" + art),
			},
		})
	}

	for _, mod := range proj.Modules {
		cleanMod := normalizeCMakeRelDir(filePath, mod)
		if cleanMod != "" {
			res.Subdirectories = append(res.Subdirectories, BuildSubdirFact{
				ParentBuildFile: filePath,
				Subdirectory:    cleanMod,
				BuildSystem:     "maven",
				Meta: FactMeta{
					Confidence:         ConfidenceExact,
					Span:               span,
					ContentFingerprint: fingerprintString("maven-mod:" + filePath + "->" + cleanMod),
				},
			})
		}
	}

	props := proj.Properties.Entries
	for k, v := range props {
		if strings.Contains(strings.ToLower(k), "java") || strings.Contains(strings.ToLower(k), "compiler") {
			res.CompileOptions = append(res.CompileOptions, CompileOption{
				Key:    k,
				Value:  v,
				Source: "maven",
				Meta: FactMeta{
					Confidence:         ConfidenceExact,
					Span:               span,
					ContentFingerprint: fingerprintString("maven-prop:" + k + "=" + v),
				},
			})
		}
	}

	for _, dep := range proj.Dependencies {
		group := strings.TrimSpace(dep.GroupID)
		artifact := strings.TrimSpace(dep.ArtifactID)
		if artifact == "" {
			continue
		}
		name := artifact
		if group != "" {
			name = group + ":" + artifact
		}
		version := resolveMavenProp(strings.TrimSpace(dep.Version), props)
		scope := normalizeJVMDepScope(strings.TrimSpace(dep.Scope))
		res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
			Name:       name,
			Version:    version,
			Scope:      scope,
			SourceFile: filePath,
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               span,
				ContentFingerprint: fingerprintString("maven-dep:" + name + ":" + version + ":" + scope),
			},
		})
	}

	return res
}

func resolveMavenProp(val string, props map[string]string) string {
	if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") && len(props) > 0 {
		key := strings.TrimSpace(val[2 : len(val)-1])
		if resolved, ok := props[key]; ok && resolved != "" {
			return resolved
		}
	}
	return val
}

func normalizeJVMDepScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "test", "testimplementation", "testruntimeonly", "testcompileonly":
		return "test"
	case "runtime", "runtimeonly":
		return "runtime"
	default:
		return "compile"
	}
}

// ─── Gradle build.gradle / build.gradle.kts 解析器 ───

var (
	reGradleDep     = regexp.MustCompile(`(?m)^[ \t]*(implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|testCompileOnly)(?:[ \t]*\([ \t]*|[ \t]+)['"]([^'"]+)['"][ \t]*\)?`)
	reGradleRootPrj = regexp.MustCompile(`(?m)^[ \t]*(?:rootProject\.name|archivesBaseName)[ \t]*=[ \t]*['"]([^'"]+)['"]`)
	reGradleJavaVer = regexp.MustCompile(`(?m)(?:sourceCompatibility|targetCompatibility|JavaLanguageVersion\.of)[ \t]*[=(][ \t]*['"]?([0-9._A-Za-z]+)['"]?`)
)

func (e *BuildExtractor) parseGradle(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"gradle"},
	}

	targetName := ""
	if m := reGradleRootPrj.FindStringSubmatch(src); len(m) == 2 {
		targetName = strings.TrimSpace(m[1])
	}
	if targetName == "" {
		dir := path.Dir(filePath)
		if dir == "." || dir == "" {
			targetName = "gradle-project"
		} else {
			targetName = path.Base(dir)
		}
	}
	res.Build = append(res.Build, BuildTarget{
		Name:        targetName,
		Kind:        "library",
		BuildSystem: "gradle",
		Meta: FactMeta{
			Confidence:         ConfidenceExact,
			Span:               SourceSpan{FilePath: filePath, LineStart: 1, LineEnd: 1},
			ContentFingerprint: fingerprintString("gradle:" + targetName),
		},
	})

	if jm := reGradleJavaVer.FindStringSubmatchIndex(src); len(jm) == 4 {
		line := 1 + strings.Count(src[:jm[0]], "\n")
		val := strings.TrimSpace(src[jm[2]:jm[3]])
		res.CompileOptions = append(res.CompileOptions, CompileOption{
			Key:    "java.version",
			Value:  val,
			Source: "gradle",
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               SourceSpan{FilePath: filePath, LineStart: line, LineEnd: line},
				ContentFingerprint: fingerprintString("gradle-java:" + val),
			},
		})
	}

	matches := reGradleDep.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range matches {
		cfgName := src[loc[2]:loc[3]]
		coord := strings.TrimSpace(src[loc[4]:loc[5]])
		line := 1 + strings.Count(src[:loc[0]], "\n")
		parts := strings.Split(coord, ":")
		name := coord
		version := ""
		if len(parts) >= 3 {
			name = parts[0] + ":" + parts[1]
			version = parts[2]
		} else if len(parts) == 2 {
			name = parts[0] + ":" + parts[1]
		}
		scope := normalizeJVMDepScope(cfgName)
		res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
			Name:       name,
			Version:    version,
			Scope:      scope,
			SourceFile: filePath,
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               SourceSpan{FilePath: filePath, LineStart: line, LineEnd: line},
				ContentFingerprint: fingerprintString("gradle-dep:" + name + ":" + version + ":" + scope),
			},
		})
	}

	return res
}

// ─── Python pyproject.toml (Poetry / PEP 621) 解析器 ───

var (
	rePepDepSpec = regexp.MustCompile(`^([A-Za-z0-9_.-]+)(?:\[[^\]]+\])?\s*([<>=!~^].*)?$`)
)

func (e *BuildExtractor) parsePyProjectToml(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{}
	isPoetry := strings.Contains(src, "[tool.poetry")
	if isPoetry {
		res.BuildSystems = []string{"poetry"}
	} else {
		res.BuildSystems = []string{"pip"}
	}

	lines := strings.Split(src, "\n")
	section := ""
	inProjectDepsArray := false
	inOptDepsArray := false
	optDepsScope := "compile"
	projectName := ""
	projectLine := 1

	for i, raw := range lines {
		lineNo := i + 1
		line := stripHashComment(raw)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			inProjectDepsArray = false
			inOptDepsArray = false
			continue
		}

		// 处理 PEP 621 多行 dependencies = [ ... ]
		if inProjectDepsArray || inOptDepsArray {
			if strings.Contains(line, "]") {
				for _, spec := range extractQuotedStrings(line) {
					addPipDepSpec(res, filePath, lineNo, spec, optDepsScope)
				}
				inProjectDepsArray = false
				inOptDepsArray = false
				continue
			}
			for _, spec := range extractQuotedStrings(line) {
				addPipDepSpec(res, filePath, lineNo, spec, optDepsScope)
			}
			continue
		}

		eqIdx := strings.Index(line, "=")
		if eqIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eqIdx])
		val := strings.TrimSpace(line[eqIdx+1:])

		switch section {
		case "tool.poetry", "project":
			if key == "name" && projectName == "" {
				projectName = trimQuotes(val)
				projectLine = lineNo
			}
			if section == "project" && key == "dependencies" {
				optDepsScope = "compile"
				for _, spec := range extractQuotedStrings(val) {
					addPipDepSpec(res, filePath, lineNo, spec, "compile")
				}
				if strings.Contains(val, "[") && !strings.Contains(val, "]") {
					inProjectDepsArray = true
				}
			}
			if section == "project" && key == "requires-python" {
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    "python",
					Value:  trimQuotes(val),
					Source: "pip",
					Meta: FactMeta{
						Confidence:         ConfidenceExact,
						Span:               SourceSpan{FilePath: filePath, LineStart: lineNo, LineEnd: lineNo},
						ContentFingerprint: fingerprintString("py-req:" + trimQuotes(val)),
					},
				})
			}

		case "tool.poetry.dependencies", "tool.poetry.dev-dependencies",
			"tool.poetry.group.dev.dependencies", "tool.poetry.group.test.dependencies":
			scope := "compile"
			if strings.Contains(section, "dev") || strings.Contains(section, "test") {
				scope = "test"
			}
			ver := extractTomlDepVersion(val)
			if strings.EqualFold(key, "python") {
				res.CompileOptions = append(res.CompileOptions, CompileOption{
					Key:    "python",
					Value:  ver,
					Source: "poetry",
					Meta: FactMeta{
						Confidence:         ConfidenceExact,
						Span:               SourceSpan{FilePath: filePath, LineStart: lineNo, LineEnd: lineNo},
						ContentFingerprint: fingerprintString("poetry-py:" + ver),
					},
				})
				continue
			}
			res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
				Name:       strings.Trim(key, `"'`),
				Version:    ver,
				Scope:      scope,
				SourceFile: filePath,
				Meta: FactMeta{
					Confidence:         ConfidenceExact,
					Span:               SourceSpan{FilePath: filePath, LineStart: lineNo, LineEnd: lineNo},
					ContentFingerprint: fingerprintString("poetry-dep:" + key + ":" + ver + ":" + scope),
				},
			})

		case "project.optional-dependencies":
			scope := "compile"
			if strings.Contains(strings.ToLower(key), "dev") || strings.Contains(strings.ToLower(key), "test") {
				scope = "test"
			}
			optDepsScope = scope
			for _, spec := range extractQuotedStrings(val) {
				addPipDepSpec(res, filePath, lineNo, spec, scope)
			}
			if strings.Contains(val, "[") && !strings.Contains(val, "]") {
				inOptDepsArray = true
			}
		}
	}

	if projectName != "" {
		bs := "pip"
		if isPoetry {
			bs = "poetry"
		}
		res.Build = append(res.Build, BuildTarget{
			Name:        projectName,
			Kind:        "package",
			BuildSystem: bs,
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               SourceSpan{FilePath: filePath, LineStart: projectLine, LineEnd: projectLine},
				ContentFingerprint: fingerprintString("py-pkg:" + projectName),
			},
		})
	}

	return res
}

// ─── Python requirements.txt 解析器 ───

func (e *BuildExtractor) parseRequirementsTxt(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{
		BuildSystems: []string{"pip"},
	}

	base := strings.ToLower(path.Base(filePath))
	scope := "compile"
	if strings.Contains(base, "dev") || strings.Contains(base, "test") {
		scope = "test"
	}

	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(stripHashComment(raw))
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		// 去除环境变量 marker（如 `; python_version < "3.11"`）
		if semiIdx := strings.Index(line, ";"); semiIdx >= 0 {
			line = strings.TrimSpace(line[:semiIdx])
		}
		addPipDepSpec(res, filePath, i+1, line, scope)
	}
	return res
}

func addPipDepSpec(res *ExtractionResult, filePath string, lineNo int, spec, scope string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	m := rePepDepSpec.FindStringSubmatch(spec)
	if len(m) < 2 {
		return
	}
	name := strings.TrimSpace(m[1])
	ver := ""
	if len(m) >= 3 {
		ver = strings.TrimSpace(m[2])
	}
	res.ExternalDeps = append(res.ExternalDeps, ExternalDep{
		Name:       name,
		Version:    ver,
		Scope:      scope,
		SourceFile: filePath,
		Meta: FactMeta{
			Confidence:         ConfidenceExact,
			Span:               SourceSpan{FilePath: filePath, LineStart: lineNo, LineEnd: lineNo},
			ContentFingerprint: fingerprintString("pip-dep:" + name + ":" + ver + ":" + scope),
		},
	})
}

func stripHashComment(line string) string {
	inSingle := false
	inDouble := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

var reTomlInlineVersion = regexp.MustCompile(`version\s*=\s*["']([^"']+)["']`)

func extractTomlDepVersion(val string) string {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "{") {
		if m := reTomlInlineVersion.FindStringSubmatch(val); len(m) == 2 {
			return m[1]
		}
		return ""
	}
	return trimQuotes(val)
}

var reQuotedItem = regexp.MustCompile(`["']([^"']+)["']`)

func extractQuotedStrings(s string) []string {
	matches := reQuotedItem.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) == 2 && strings.TrimSpace(m[1]) != "" {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}
