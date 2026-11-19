package codeir

import (
	"context"
	"path"
	"regexp"
	"strings"
)

// CommonExtractor 负责通用静态事实提取（SQL DDL 数据模型、C/C++ 本地 #include 内部依赖边、轻量 C++ HTTP 路由）。
type CommonExtractor struct{}

func NewCommonExtractor() *CommonExtractor {
	return &CommonExtractor{}
}

func (e *CommonExtractor) Name() string { return "common-static" }

func (e *CommonExtractor) SupportedLanguages() []string { return []string{"sql", "c", "cpp"} }

var (
	reSQLCreateTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[` + "`" + `"]?([A-Za-z0-9_.]+)[` + "`" + `"]?\s*\((.*?)\)\s*;`)
	reSQLCreateIndex = regexp.MustCompile(`(?is)CREATE\s+(UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[` + "`" + `"]?([A-Za-z0-9_]+)[` + "`" + `"]?\s+ON\s+[` + "`" + `"]?([A-Za-z0-9_.]+)[` + "`" + `"]?\s*\(([^)]+)\)`)
	reLocalInclude   = regexp.MustCompile(`(?m)^\s*#\s*include\s*"([^"]+)"`)
	reCppHTTPRoute   = regexp.MustCompile(`\b(?:router|svr|server|app)\s*(?:\.|->)\s*(Get|Post|Put|Delete|Patch|GET|POST|PUT|DELETE|PATCH)\s*\(\s*"(/[^"]*)"\s*(?:,\s*"([^"]+)")?`)
)

func (e *CommonExtractor) ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	switch file.Language {
	case "sql":
		return e.extractSQL(file.RelPath, string(content)), nil
	case "c", "cpp":
		return e.extractCppCommon(file.RelPath, string(content)), nil
	default:
		return &ExtractionResult{}, nil
	}
}

func (e *CommonExtractor) extractSQL(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{}
	tableIdx := make(map[string]int)

	for _, loc := range reSQLCreateTable.FindAllStringSubmatchIndex(src, -1) {
		tableName := strings.TrimSpace(src[loc[2]:loc[3]])
		body := src[loc[4]:loc[5]]
		startLine := 1 + strings.Count(src[:loc[0]], "\n")
		endLine := 1 + strings.Count(src[:loc[1]], "\n")

		var fields []DataModelField
		for _, rawCol := range strings.Split(body, ",") {
			colLine := strings.TrimSpace(rawCol)
			if colLine == "" {
				continue
			}
			upper := strings.ToUpper(colLine)
			if strings.HasPrefix(upper, "PRIMARY KEY") || strings.HasPrefix(upper, "FOREIGN KEY") ||
				strings.HasPrefix(upper, "UNIQUE ") || strings.HasPrefix(upper, "CONSTRAINT ") ||
				strings.HasPrefix(upper, "INDEX ") || strings.HasPrefix(upper, "KEY ") {
				continue
			}
			parts := strings.Fields(colLine)
			if len(parts) < 2 {
				continue
			}
			colName := strings.Trim(parts[0], "`\"")
			colType := parts[1]
			var constraints []string
			if len(parts) > 2 {
				rest := strings.ToUpper(strings.Join(parts[2:], " "))
				if strings.Contains(rest, "PRIMARY KEY") {
					constraints = append(constraints, "PRIMARY KEY")
				}
				if strings.Contains(rest, "NOT NULL") {
					constraints = append(constraints, "NOT NULL")
				}
				if strings.Contains(rest, "UNIQUE") {
					constraints = append(constraints, "UNIQUE")
				}
			}
			fields = append(fields, DataModelField{
				ColumnName:  colName,
				Type:        colType,
				Constraints: constraints,
			})
		}

		tableIdx[strings.ToLower(tableName)] = len(res.DataModels)
		res.DataModels = append(res.DataModels, DataModelFact{
			EntityName:        tableName,
			TableOrCollection: tableName,
			StorageKind:       "sql",
			Fields:            fields,
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               SourceSpan{FilePath: filePath, LineStart: startLine, LineEnd: endLine},
				ContentFingerprint: fingerprintString("sql:" + tableName + ":" + body),
			},
		})
	}

	for _, m := range reSQLCreateIndex.FindAllStringSubmatch(src, -1) {
		isUnique := strings.TrimSpace(m[1]) != ""
		idxName := strings.TrimSpace(m[2])
		tableName := strings.ToLower(strings.TrimSpace(m[3]))
		colsRaw := strings.Split(m[4], ",")
		var cols []string
		for _, c := range colsRaw {
			if tc := strings.Trim(strings.TrimSpace(c), "`\""); tc != "" {
				cols = append(cols, tc)
			}
		}
		if idx, ok := tableIdx[tableName]; ok {
			res.DataModels[idx].Indexes = append(res.DataModels[idx].Indexes, DataModelIndex{
				Name:    idxName,
				Columns: cols,
				Unique:  isUnique,
			})
		}
	}

	return res
}

func (e *CommonExtractor) extractCppCommon(filePath, src string) *ExtractionResult {
	res := &ExtractionResult{}
	fromMod := topModuleDir(filePath)

	for _, m := range reLocalInclude.FindAllStringSubmatch(src, -1) {
		incPath := strings.TrimSpace(m[1])
		toMod := path.Dir(incPath)
		if toMod == "." || toMod == "" {
			toMod = incPath
		}
		if fromMod != toMod {
			res.InternalModules = append(res.InternalModules, InternalModuleEdge{
				From:   fromMod,
				To:     toMod,
				Weight: 1,
			})
		}
	}

	for _, loc := range reCppHTTPRoute.FindAllStringSubmatchIndex(src, -1) {
		method := strings.ToUpper(src[loc[2]:loc[3]])
		routePath := src[loc[4]:loc[5]]
		handler := ""
		if loc[6] >= 0 && loc[7] >= 0 {
			handler = src[loc[6]:loc[7]]
		}
		lineNo := 1 + strings.Count(src[:loc[0]], "\n")
		res.HTTPRoutes = append(res.HTTPRoutes, HTTPRouteFact{
			Method:          method,
			Path:            routePath,
			HandlerFQSymbol: handler,
			Framework:       "cpp_http",
			Meta: FactMeta{
				Confidence:         ConfidenceExact,
				Span:               SourceSpan{FilePath: filePath, LineStart: lineNo, LineEnd: lineNo},
				ContentFingerprint: fingerprintString(method + ":" + routePath + ":" + handler),
			},
		})
	}

	return res
}

func topModuleDir(relPOSIX string) string {
	dir := path.Dir(relPOSIX)
	if dir == "." || dir == "" {
		return "root"
	}
	return dir
}
