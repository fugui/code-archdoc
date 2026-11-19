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

	tree := parser.ParseCtx(ctx, content, nil)
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
