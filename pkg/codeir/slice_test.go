package codeir_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/sandbox"
)

// TestSliceByModule_GoldenEquivalenceAndModuleContext 验证 T-ADR04-05：
// 1. 从全仓基础 CodeIRBundle 切出的子模块 9 大分片，与直接对子目录运行 ExtractDirectory(repoRoot, subdir) 的结果 Golden 等价；
// 2. Python 符号与路由 HandlerFQSymbol 自动剥离 <subdir>. 模块前缀；跨模块外部 .h DeclSpan 自动清理以保持模块边界纯净；
// 3. 第 10 分片 ModuleContextFacet 零 I/O 确定性推导出上游模块、下游模块、跨模块被引用外部符号签名、父级构建挂载指令与白名单文件集合。
func TestSliceByModule_GoldenEquivalenceAndModuleContext(t *testing.T) {
	repoRoot := t.TempDir()
	files := map[string]string{
		"CMakeLists.txt":        "cmake_minimum_required(VERSION 3.16)\nproject(trade_monorepo)\ninclude_directories(${CMAKE_SOURCE_DIR})\nadd_subdirectory(common)\nadd_subdirectory(order)\n",
		"common/CMakeLists.txt": "add_library(common_lib STATIC src/logger.cpp)\n",
		"common/include/types.h": `#pragma once
namespace common {
struct RpcResult {
    int code;
};
}
`,
		"common/src/logger.cpp": `#include "common/include/types.h"
namespace common {
int WriteLog(int level) { return level; }
}
`,
		"order/CMakeLists.txt": "add_executable(order_svc src/order_service.cpp)\ntarget_link_libraries(order_svc PRIVATE common_lib pthread)\n",
		"order/include/order_service.h": `#pragma once
#include "common/include/types.h"
namespace order {
class OrderService {
public:
    int CreateOrder(int uid);
};
}
`,
		"order/src/order_service.cpp": `#include "include/order_service.h"
#include "common/include/types.h"
namespace order {
int OrderService::CreateOrder(int uid) {
    return uid + 1;
}
}
`,
		"order/api/routes.py": `from fastapi import FastAPI
app = FastAPI()

@app.get("/orders")
def list_orders():
    return []
`,
	}

	for rel, content := range files {
		abs := filepath.Join(repoRoot, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	ctx := context.Background()

	// 1. 全仓基础抽取 + SliceByModule("order")
	baseBundle, err := agg.ExtractDirectory(ctx, repoRoot, "", sandbox.WalkOptions{MaxFileBytes: 1048576})
	if err != nil {
		t.Fatalf("全仓基础抽取失败: %v", err)
	}
	slicedOrder, orderCtx, err := codeir.SliceByModule(baseBundle, "order", []string{"common", "order"})
	if err != nil {
		t.Fatalf("SliceByModule(order) 失败: %v", err)
	}

	// 2. 直接对子目录 order 独立抽取
	directOrder, err := agg.ExtractDirectory(ctx, repoRoot, "order", sandbox.WalkOptions{MaxFileBytes: 1048576})
	if err != nil {
		t.Fatalf("独立抽取 order 子目录失败: %v", err)
	}

	// 3. 断言 9 大分片 Golden 等价（忽略 SkippedNoiseSummary 和 ExtractDurationMs）
	slicedOrder.Tree.SkippedNoiseSummary = nil
	directOrder.Tree.SkippedNoiseSummary = nil
	slicedOrder.ExtractDurationMs = 0
	directOrder.ExtractDurationMs = 0

	assertJSONEqual := func(name string, got, want any) {
		t.Helper()
		bGot, _ := json.Marshal(got)
		bWant, _ := json.Marshal(want)
		if string(bGot) != string(bWant) && !reflect.DeepEqual(got, want) {
			t.Fatalf("分片 %s 在 SliceByModule 与独立子目录抽取之间不一致:\ngot:  %s\nwant: %s", name, string(bGot), string(bWant))
		}
	}

	assertJSONEqual("tree", slicedOrder.Tree, directOrder.Tree)
	assertJSONEqual("build", slicedOrder.Build, directOrder.Build)
	assertJSONEqual("deps", slicedOrder.Deps, directOrder.Deps)
	assertJSONEqual("config_keys", slicedOrder.ConfigKeys, directOrder.ConfigKeys)
	assertJSONEqual("symbols", slicedOrder.Symbols, directOrder.Symbols)
	assertJSONEqual("idl_contracts", slicedOrder.IDLContracts, directOrder.IDLContracts)
	assertJSONEqual("http_routes", slicedOrder.HTTPRoutes, directOrder.HTTPRoutes)
	assertJSONEqual("data_models", slicedOrder.DataModels, directOrder.DataModels)
	assertJSONEqual("runtime_primitives", slicedOrder.RuntimePrimitives, directOrder.RuntimePrimitives)

	// 4. 断言第 10 分片 ModuleContextFacet 准确捕获跨模块依赖与外部符号
	if orderCtx == nil || orderCtx.ModulePath != "order" {
		t.Fatalf("orderCtx 缺失或 ModulePath 错误: %+v", orderCtx)
	}
	if len(orderCtx.UpstreamModules) == 0 || orderCtx.UpstreamModules[0].TargetModule != "common" {
		t.Fatalf("order 模块 UpstreamModules 应包含 common，实际: %+v", orderCtx.UpstreamModules)
	}
	foundRpcResult := false
	for _, es := range orderCtx.ReferencedExtSymbols {
		if es.FQSymbol == "common::RpcResult" && es.FilePath == "common/include/types.h" && es.OwnerModule == "common" {
			foundRpcResult = true
			break
		}
	}
	if !foundRpcResult {
		t.Fatalf("order 模块 ReferencedExtSymbols 应包含 common::RpcResult (common/include/types.h)，实际: %+v", orderCtx.ReferencedExtSymbols)
	}
	if len(orderCtx.RepoBuildContext) == 0 || orderCtx.RepoBuildContext[0].Directive != "add_subdirectory(order)" {
		t.Fatalf("order 模块 RepoBuildContext 应包含父 CMakeLists.txt 的 add_subdirectory(order)，实际: %+v", orderCtx.RepoBuildContext)
	}

	// 5. 切分 common 模块，验证其 DownstreamModules 包含 order
	_, commonCtx, err := codeir.SliceByModule(baseBundle, "common", []string{"common", "order"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commonCtx.DownstreamModules) == 0 || commonCtx.DownstreamModules[0].TargetModule != "order" {
		t.Fatalf("common 模块 DownstreamModules 应包含下游调用方 order，实际: %+v", commonCtx.DownstreamModules)
	}
}

// TestModuleContext_4KTokenBudgetTruncation 验证 ModuleContextFacet 超过 12KB (~4K Token) 时自动裁剪并标记 Truncated=true。
func TestModuleContext_4KTokenBudgetTruncation(t *testing.T) {
	base := &codeir.CodeIRBundle{
		SchemaVersion: codeir.SchemaVersion,
		Tree: codeir.TreeFacet{
			Files: []codeir.FileSummary{
				{RelPath: "order/src/main.cpp", Language: "cpp", LOC: 50},
				{RelPath: "common/include/big.h", Language: "cpp", LOC: 500},
			},
		},
		Deps: codeir.DepsFacet{
			FileImports: []codeir.FileImportFact{
				{SourceFile: "order/src/main.cpp", RawTarget: "common/include/big.h", Kind: "cpp_include"},
			},
		},
	}
	for i := 0; i < 120; i++ {
		base.Symbols.Symbols = append(base.Symbols.Symbols, codeir.SymbolFact{
			FQSymbol:  "common::VeryLongExternalContractClassName_" + string(rune('A'+(i%26))) + "_" + filepath.Base(filepath.Join("x", string(rune('0'+(i%10))))),
			Kind:      "class",
			Language:  "cpp",
			Signature: "class VeryLongExternalContractClassName_WithDetailedSignatureAndParameters_" + string(make([]byte, 120)),
			Meta: codeir.FactMeta{
				Confidence: codeir.ConfidenceExact,
				Span:       codeir.SourceSpan{FilePath: "common/include/big.h", LineStart: i + 1, LineEnd: i + 5},
			},
		})
	}

	_, modCtx, err := codeir.SliceByModule(base, "order", []string{"common", "order"})
	if err != nil {
		t.Fatal(err)
	}
	if !modCtx.Truncated {
		t.Fatalf("超过 50 个外部符号或 12KB 时应触发裁剪并标记 Truncated=true")
	}
	raw, _ := json.Marshal(modCtx)
	if len(raw) > 12000 {
		t.Fatalf("ModuleContextFacet JSON 字节数必须控制在 12000 以内，实际=%d", len(raw))
	}
}
