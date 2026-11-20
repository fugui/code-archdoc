package treesitter

import (
	"context"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/security"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
)

// CExtractor 基于 tree-sitter-c（CGO 严格隔离）实现纯 C (.c) 语法树提取器（ADR-02）。
type CExtractor struct {
	redactor *security.SecretRedactor
}

func NewCExtractor() *CExtractor {
	return &CExtractor{
		redactor: security.NewSecretRedactor(),
	}
}

func (e *CExtractor) Name() string { return "treesitter-c" }

func (e *CExtractor) SupportedLanguages() []string { return []string{"c"} }

// parseBytesSafely 避开 go-tree-sitter v0.25 已废弃且存在 nil CancellationFlag 竞态 SIGSEGV 的 ParseCtx，
// 在解析前后检查 ctx 取消状态，使用无泄漏的同步 Parse 执行单文件（<=1MB）语法树解析。
func parseBytesSafely(ctx context.Context, parser *tree_sitter.Parser, content []byte) (*tree_sitter.Tree, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	tree := parser.Parse(content, nil)
	if ctx != nil && ctx.Err() != nil {
		if tree != nil {
			tree.Close()
		}
		return nil, ctx.Err()
	}
	return tree, nil
}

func (e *CExtractor) ExtractFile(ctx context.Context, file codeir.FileEntry, content []byte) (*codeir.ExtractionResult, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()

	lang := tree_sitter.NewLanguage(tree_sitter_c.Language())
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}

	tree, err := parseBytesSafely(ctx, parser, content)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return &codeir.ExtractionResult{
			Diagnostics: []codeir.ParseDiagnostic{{
				FilePath: file.RelPath,
				Message:  "tree-sitter-c 解析返回空语法树",
			}},
		}, nil
	}
	defer tree.Close()

	w := &cppWalker{
		src:      content,
		filePath: file.RelPath,
		langName: "c",
		redactor: e.redactor,
		res:      &codeir.ExtractionResult{},
	}
	w.walk(tree.RootNode(), walkState{})
	return w.res, nil
}

// DefaultExtractors 返回内置的 C/C++/Java/Python tree-sitter 提取器列表。
func DefaultExtractors() []codeir.LanguageExtractor {
	return []codeir.LanguageExtractor{
		NewCppExtractor(),
		NewCExtractor(),
		NewJavaExtractor(),
		NewPythonExtractor(),
	}
}
