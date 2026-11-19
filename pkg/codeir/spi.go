package codeir

import "context"

// FileEntry 经沙箱安全校验与降噪后的待分析文件描述符
type FileEntry struct {
	RelPath  string
	AbsPath  string // 仅在沙箱读取器内使用，严禁写入 IR
	Language string // "c" | "cpp" | "proto" | "java" | "python" | "config" | "build" | "sql" | "other"
	Size     int64
}

// ExtractionResult 单语言提取器向汇聚器提交的分片增量
type ExtractionResult struct {
	BuildSystems      []string
	Build             []BuildTarget
	CompileOptions    []CompileOption
	Scripts           []BuildScript
	ExternalDeps      []ExternalDep
	InternalModules   []InternalModuleEdge
	ConfigKeys        []ConfigKeyFact
	Symbols           []SymbolFact
	IDLContracts      []ProtoFileFact
	HTTPRoutes        []HTTPRouteFact
	DataModels        []DataModelFact
	RuntimePrimitives []RuntimePrimitiveFact
	Diagnostics       []ParseDiagnostic
}

// LanguageExtractor 多语言静态提取插件 SPI（严格无状态、并发安全、零 DB/CGO 泄漏）
type LanguageExtractor interface {
	Name() string
	SupportedLanguages() []string
	ExtractFile(ctx context.Context, file FileEntry, content []byte) (*ExtractionResult, error)
}
