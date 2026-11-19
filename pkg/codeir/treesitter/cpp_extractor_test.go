package treesitter

import (
	"context"
	"testing"

	"code-archdoc/pkg/codeir"
)

func TestCppExportMacroRecoveryAndLocalLocks(t *testing.T) {
	src := []byte(`
namespace order {
API_EXPORT class OrderService {
public:
    CROS_RPC_METHOD int Pay(int x);
#ifdef ENABLE_METRICS
    int GetOrderMetrics() const;
#endif
};
int OrderService::Pay(int x) {
    std::lock_guard<std::mutex> guard(m_);
    std::unique_lock<std::mutex> lock(m_);
    return x;
}
}
`)

	ext := NewCppExtractor()
	res, err := ext.ExtractFile(context.Background(), codeir.FileEntry{RelPath: "test.hpp", Language: "cpp"}, src)
	if err != nil {
		t.Fatal(err)
	}

	symMap := make(map[string]codeir.SymbolFact)
	for _, s := range res.Symbols {
		symMap[s.FQSymbol] = s
	}
	if s, ok := symMap["order::OrderService"]; !ok || s.Kind != "class" {
		t.Fatalf("导出宏修饰类恢复失败: %+v", symMap)
	}
	if _, ok := symMap["order::OrderService::Pay"]; !ok {
		t.Fatalf("缺失方法 order::OrderService::Pay: %+v", symMap)
	}
	if gm, ok := symMap["order::OrderService::GetOrderMetrics"]; !ok || !gm.Meta.Span.Conditional {
		t.Fatalf("条件编译方法未正确恢复或标注 conditional: %+v", symMap)
	}
	if _, bad := symMap["order::guard"]; bad {
		t.Fatalf("局部 std::lock_guard 变量被误报为函数符号: %+v", symMap)
	}

	prims := make(map[string]bool)
	for _, p := range res.RuntimePrimitives {
		prims[p.APIName] = true
	}
	if !prims["std::lock_guard"] || !prims["std::unique_lock"] {
		t.Fatalf("缺失局部锁原语提取: %+v", prims)
	}
}
