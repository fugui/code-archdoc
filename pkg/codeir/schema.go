package codeir

import "time"

const (
	// SchemaVersion 当前 Code IR 规范版本号（写入快照键与每个分片）
	SchemaVersion = "1.0"
)

// Confidence 事实可信度枚举
type Confidence string

const (
	ConfidenceExact     Confidence = "exact"
	ConfidenceHeuristic Confidence = "heuristic"
)

// 9 大 Canonical Facet 名称常量
const (
	FacetTree              = "tree"
	FacetBuild             = "build"
	FacetDeps              = "deps"
	FacetConfigKeys        = "config_keys"
	FacetSymbols           = "symbols"
	FacetIDLContracts      = "idl_contracts"
	FacetHTTPRoutes        = "http_routes"
	FacetDataModels        = "data_models"
	FacetRuntimePrimitives = "runtime_primitives"
)

// AllFacetNames 按固定顺序返回全部 9 大分片名称。
var AllFacetNames = []string{
	FacetTree,
	FacetBuild,
	FacetDeps,
	FacetConfigKeys,
	FacetSymbols,
	FacetIDLContracts,
	FacetHTTPRoutes,
	FacetDataModels,
	FacetRuntimePrimitives,
}

// SourceSpan 源码精确定位信息
type SourceSpan struct {
	FilePath      string `json:"file_path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Conditional   bool   `json:"conditional,omitempty"`
	ConditionExpr string `json:"condition_expr,omitempty"`
}

// FactMeta 每条 Code IR 事实的公共基座
type FactMeta struct {
	Confidence         Confidence `json:"confidence"`
	Span               SourceSpan `json:"span"`
	ContentFingerprint string     `json:"content_fingerprint,omitempty"`
}

// ─── 1. tree Facet ───

type LangStat struct {
	Files int   `json:"files"`
	LOC   int   `json:"loc"`
	Bytes int64 `json:"bytes"`
}

type FileSummary struct {
	RelPath  string `json:"rel_path"`
	Language string `json:"language"`
	Size     int64  `json:"size"`
	LOC      int    `json:"loc"`
}

type DirectoryStat struct {
	Path           string         `json:"path"`
	FileCount      int            `json:"file_count"`
	LOC            int            `json:"loc"`
	LanguageCounts map[string]int `json:"language_counts"`
	Representative []string       `json:"representative,omitempty"`
}

type TreeFacet struct {
	SchemaVersion       string              `json:"schema_version"`
	TotalFiles          int                 `json:"total_files"`
	TotalLOC            int                 `json:"total_loc"`
	LanguageStats       map[string]LangStat `json:"language_stats"`
	Files               []FileSummary       `json:"files"`
	Directories         []DirectoryStat     `json:"directories"`
	SkippedNoiseSummary map[string]int      `json:"skipped_noise_summary"`
}

// ─── 2. build Facet ───

type BuildTarget struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // executable | library | static_library | shared_library | target
	BuildSystem string   `json:"build_system"`
	Sources     []string `json:"sources,omitempty"`
	LinkLibs    []string `json:"link_libs,omitempty"`
	Meta        FactMeta `json:"meta"`
}

type CompileOption struct {
	Key    string   `json:"key"`
	Value  string   `json:"value"`
	Source string   `json:"source"` // cmake | compile_commands | makefile | bazel
	Meta   FactMeta `json:"meta"`
}

type BuildScript struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Meta    FactMeta `json:"meta"`
}

type BuildFacet struct {
	SchemaVersion  string          `json:"schema_version"`
	BuildSystems   []string        `json:"build_systems"`
	Targets        []BuildTarget   `json:"targets"`
	CompileOptions []CompileOption `json:"compile_options"`
	Scripts        []BuildScript   `json:"scripts,omitempty"`
}

// ─── 3. deps Facet ───

type ExternalDep struct {
	Name       string   `json:"name"`
	Version    string   `json:"version,omitempty"`
	Scope      string   `json:"scope"` // compile | runtime | test
	SourceFile string   `json:"source_file"`
	Meta       FactMeta `json:"meta"`
}

type InternalModuleEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Weight int    `json:"weight"`
}

type DepsFacet struct {
	SchemaVersion   string               `json:"schema_version"`
	ExternalDeps    []ExternalDep        `json:"external_deps"`
	InternalModules []InternalModuleEdge `json:"internal_modules"`
}

// ─── 4. config_keys Facet ───

type ConfigKeyFact struct {
	KeyPath        string   `json:"key_path"`
	ValueType      string   `json:"value_type"` // string | int | float | bool | list | object
	SafeDefault    string   `json:"safe_default"`
	IsSensitive    bool     `json:"is_sensitive"`
	RedactedReason string   `json:"redacted_reason,omitempty"`
	SourceFile     string   `json:"source_file"`
	EnvVarBinding  string   `json:"env_var_binding,omitempty"`
	Meta           FactMeta `json:"meta"`
}

type ConfigKeysFacet struct {
	SchemaVersion   string          `json:"schema_version"`
	Keys            []ConfigKeyFact `json:"keys"`
	RedactedSecrets int             `json:"redacted_secrets"`
}

// ─── 5. symbols Facet ───

type SymbolField struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Visibility string `json:"visibility,omitempty"`
}

type SymbolFact struct {
	FQSymbol          string        `json:"fq_symbol"`
	Kind              string        `json:"kind"` // namespace | class | struct | enum | function | method | macro_const
	Language          string        `json:"language"`
	Signature         string        `json:"signature"`
	Visibility        string        `json:"visibility,omitempty"`
	BaseTypes         []string      `json:"base_types,omitempty"`
	Fields            []SymbolField `json:"fields,omitempty"`
	DocCommentSummary string        `json:"doc_comment_summary,omitempty"`
	DeclSpan          *SourceSpan   `json:"decl_span,omitempty"` // .h 声明位置（归并 .h/.cpp 后保留双坐标）
	Meta              FactMeta      `json:"meta"`
}

type SymbolsFacet struct {
	SchemaVersion string       `json:"schema_version"`
	Symbols       []SymbolFact `json:"symbols"`
}

// ─── 6. idl_contracts Facet ───

type ProtoRPCFact struct {
	MethodName      string            `json:"method_name"`
	RequestType     string            `json:"request_type"`
	ResponseType    string            `json:"response_type"`
	ClientStreaming bool              `json:"client_streaming"`
	ServerStreaming bool              `json:"server_streaming"`
	CrosOptions     map[string]string `json:"cros_options,omitempty"`
	Comment         string            `json:"comment,omitempty"`
	Meta            FactMeta          `json:"meta"`
}

type ProtoServiceFact struct {
	Name        string            `json:"name"`
	FullName    string            `json:"full_name"`
	CrosOptions map[string]string `json:"cros_options,omitempty"`
	RPCs        []ProtoRPCFact    `json:"rpcs"`
	Comment     string            `json:"comment,omitempty"`
	Meta        FactMeta          `json:"meta"`
}

type ProtoFieldFact struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Tag      int    `json:"tag"`
	Repeated bool   `json:"repeated,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type ProtoMessageFact struct {
	Name     string           `json:"name"`
	FullName string           `json:"full_name"`
	Fields   []ProtoFieldFact `json:"fields"`
	Comment  string           `json:"comment,omitempty"`
	Meta     FactMeta         `json:"meta"`
}

type ProtoEnumValue struct {
	Name   string `json:"name"`
	Number int    `json:"number"`
}

type ProtoEnumFact struct {
	Name     string           `json:"name"`
	FullName string           `json:"full_name"`
	Values   []ProtoEnumValue `json:"values"`
	Comment  string           `json:"comment,omitempty"`
	Meta     FactMeta         `json:"meta"`
}

type ProtoFileFact struct {
	FilePath    string             `json:"file_path"`
	Syntax      string             `json:"syntax"`
	Package     string             `json:"package"`
	Imports     []string           `json:"imports,omitempty"`
	CrosOptions map[string]string  `json:"cros_options,omitempty"`
	Services    []ProtoServiceFact `json:"services,omitempty"`
	Messages    []ProtoMessageFact `json:"messages,omitempty"`
	Enums       []ProtoEnumFact    `json:"enums,omitempty"`
	Meta        FactMeta           `json:"meta"`
}

type IDLContractsFacet struct {
	SchemaVersion string          `json:"schema_version"`
	Protos        []ProtoFileFact `json:"protos"`
}

// ─── 7. http_routes Facet ───

type HTTPRouteFact struct {
	Method          string   `json:"method"`
	Path            string   `json:"path"`
	HandlerFQSymbol string   `json:"handler_fq_symbol"`
	RequestParams   []string `json:"request_params,omitempty"`
	ResponseType    string   `json:"response_type,omitempty"`
	Middlewares     []string `json:"middlewares,omitempty"`
	Framework       string   `json:"framework"` // cpp_http | spring | fastapi | flask
	Meta            FactMeta `json:"meta"`
}

type HTTPRoutesFacet struct {
	SchemaVersion string          `json:"schema_version"`
	Routes        []HTTPRouteFact `json:"routes"`
}

// ─── 8. data_models Facet ───

type DataModelField struct {
	ColumnName  string   `json:"column_name"`
	Type        string   `json:"type"`
	Constraints []string `json:"constraints,omitempty"`
}

type DataModelIndex struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

type DataModelFact struct {
	EntityName        string           `json:"entity_name"`
	TableOrCollection string           `json:"table_or_collection"`
	StorageKind       string           `json:"storage_kind"` // sql | redis | mq | shm
	Fields            []DataModelField `json:"fields"`
	Indexes           []DataModelIndex `json:"indexes,omitempty"`
	Meta              FactMeta         `json:"meta"`
}

type DataModelsFacet struct {
	SchemaVersion string          `json:"schema_version"`
	Entities      []DataModelFact `json:"entities"`
}

// ─── 9. runtime_primitives Facet ───

type RuntimePrimitiveFact struct {
	Category          string   `json:"category"` // thread | lock | shm_ipc | signal | timer | event_loop | scheduler | error_code
	APIName           string   `json:"api_name"`
	EnclosingFQSymbol string   `json:"enclosing_fq_symbol,omitempty"`
	ContextSnippet    string   `json:"context_snippet,omitempty"`
	Meta              FactMeta `json:"meta"`
}

type RuntimePrimitivesFacet struct {
	SchemaVersion string                 `json:"schema_version"`
	Primitives    []RuntimePrimitiveFact `json:"primitives"`
}

// ─── 聚合产物与诊断 ───

type ParseDiagnostic struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

// FacetSummaryRecord 每个分片的轻量元数据统计（与 models.FacetSummary 对齐，无循环依赖）
type FacetSummaryRecord struct {
	FacetName       string         `json:"facet_name"`
	ExactCount      int            `json:"exact_count"`
	HeuristicCount  int            `json:"heuristic_count"`
	RedactedSecrets int            `json:"redacted_secrets,omitempty"`
	ExtraCounts     map[string]int `json:"extra_counts,omitempty"`
}

// CodeIRBundle 汇聚一次快照抽取的全部 9 大分片与统计信息
type CodeIRBundle struct {
	SchemaVersion     string                 `json:"schema_version"`
	Tree              TreeFacet              `json:"tree"`
	Build             BuildFacet             `json:"build"`
	Deps              DepsFacet              `json:"deps"`
	ConfigKeys        ConfigKeysFacet        `json:"config_keys"`
	Symbols           SymbolsFacet           `json:"symbols"`
	IDLContracts      IDLContractsFacet      `json:"idl_contracts"`
	HTTPRoutes        HTTPRoutesFacet        `json:"http_routes"`
	DataModels        DataModelsFacet        `json:"data_models"`
	RuntimePrimitives RuntimePrimitivesFacet `json:"runtime_primitives"`
	Diagnostics       []ParseDiagnostic      `json:"diagnostics,omitempty"`
	ExtractDurationMs int64                  `json:"extract_duration_ms"`
}

// GlossaryCache 快照级 L2 模块职责摘要与共享术语表缓存结构（glossary.json.gz）
type GlossaryCache struct {
	SchemaVersion   string               `json:"schema_version"`
	ModelName       string               `json:"model_name"`
	GeneratedAt     time.Time            `json:"generated_at"`
	ModuleSummaries []ModuleSummaryEntry `json:"module_summaries"`
	Terms           []GlossaryTerm       `json:"terms"`
}

type ModuleSummaryEntry struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

type GlossaryTerm struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
}
