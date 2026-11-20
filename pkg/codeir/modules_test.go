package codeir

import "testing"

// TestDiscoverModules_StrategiesAndFiltering 验证 T-ADR04-01：
// 1. 默认排除名单（docs/scripts/third_party 等）与自定义 ExcludeGlobs 过滤；
// 2. MinEffectiveFiles 阈值过滤（低于阈值跳过，但已登记在 architecture_elements 的子目录无条件保留）；
// 3. arch_elements / top_level_dirs / merged 三种策略行为；
// 4. merged 模式下同路径优先保留 source="arch_element" 且已声明嵌套子模块（如 services/order）时自动跳过直接父顶层目录（services）。
func TestDiscoverModules_StrategiesAndFiltering(t *testing.T) {
	tree := &TreeFacet{
		Files: []FileSummary{
			// 根目录散落文件（不应成为子模块）
			{RelPath: "CMakeLists.txt", Language: "cmake", LOC: 40},
			{RelPath: "README.md", Language: "markdown", LOC: 100},
			// common 模块：3 个有效文件
			{RelPath: "common/include/types.h", Language: "cpp", LOC: 80},
			{RelPath: "common/src/logger.cpp", Language: "cpp", LOC: 120},
			{RelPath: "common/src/utils.cpp", Language: "cpp", LOC: 90},
			// order 模块：4 个有效文件（同时也在 architecture_elements 中登记）
			{RelPath: "order/include/order_engine.h", Language: "cpp", LOC: 150},
			{RelPath: "order/src/order_engine.cpp", Language: "cpp", LOC: 320},
			{RelPath: "order/src/matcher.cpp", Language: "cpp", LOC: 210},
			{RelPath: "order/proto/order.proto", Language: "proto", LOC: 60},
			// gateway 模块：3 个有效文件
			{RelPath: "gateway/src/http_server.cpp", Language: "cpp", LOC: 200},
			{RelPath: "gateway/src/router.cpp", Language: "cpp", LOC: 180},
			{RelPath: "gateway/include/router.h", Language: "cpp", LOC: 70},
			// tiny 目录：仅 2 个文件（低于默认阈值 3）
			{RelPath: "tiny/a.cpp", Language: "cpp", LOC: 20},
			{RelPath: "tiny/b.cpp", Language: "cpp", LOC: 30},
			// 默认黑名单目录：即便有 >=3 个文件也必须被排除
			{RelPath: "docs/a.md", Language: "markdown", LOC: 10},
			{RelPath: "docs/b.md", Language: "markdown", LOC: 10},
			{RelPath: "docs/c.md", Language: "markdown", LOC: 10},
			{RelPath: "third_party/fmt/a.h", Language: "cpp", LOC: 500},
			{RelPath: "third_party/fmt/b.h", Language: "cpp", LOC: 500},
			{RelPath: "third_party/fmt/c.cc", Language: "cpp", LOC: 500},
			// 自定义 ExcludeGlobs 目标目录
			{RelPath: "legacy_v1/a.cpp", Language: "cpp", LOC: 100},
			{RelPath: "legacy_v1/b.cpp", Language: "cpp", LOC: 100},
			{RelPath: "legacy_v1/c.cpp", Language: "cpp", LOC: 100},
		},
	}

	archElems := []ArchElementScope{
		{ID: 101, Subdirectory: "order"},
	}

	// 1. merged 默认策略
	merged := DiscoverModules(tree, ModuleDiscoveryPolicy{
		Strategy:          "merged",
		ExcludeGlobs:      []string{"legacy_*"},
		MinEffectiveFiles: 3,
	}, archElems)

	if len(merged) != 3 {
		t.Fatalf("merged 策略预期发现 3 个模块 [common, gateway, order]，实际=%d: %+v", len(merged), merged)
	}
	expectedOrder := []struct {
		subdir string
		source string
		files  int
		loc    int
		elemID uint
	}{
		{subdir: "common", source: "discovered", files: 3, loc: 290, elemID: 0},
		{subdir: "gateway", source: "discovered", files: 3, loc: 450, elemID: 0},
		{subdir: "order", source: "arch_element", files: 4, loc: 740, elemID: 101},
	}
	for i, exp := range expectedOrder {
		got := merged[i]
		if got.Subdirectory != exp.subdir || got.Source != exp.source || got.EffectiveFiles != exp.files || got.TotalLOC != exp.loc {
			t.Fatalf("merged[%d] 不匹配: got=%+v want=%+v", i, got, exp)
		}
		if exp.elemID > 0 {
			if got.ArchElementID == nil || *got.ArchElementID != exp.elemID {
				t.Fatalf("merged[%d] ArchElementID 期望 %d，实际=%v", i, exp.elemID, got.ArchElementID)
			}
		} else if got.ArchElementID != nil {
			t.Fatalf("merged[%d] 期望 ArchElementID=nil，实际=%v", i, *got.ArchElementID)
		}
	}

	// 2. arch_elements 策略（仅返回架构元素中声明的子目录）
	onlyArch := DiscoverModules(tree, ModuleDiscoveryPolicy{
		Strategy:          "arch_elements",
		MinEffectiveFiles: 3,
	}, archElems)
	if len(onlyArch) != 1 || onlyArch[0].Subdirectory != "order" || onlyArch[0].Source != "arch_element" {
		t.Fatalf("arch_elements 策略结果异常: %+v", onlyArch)
	}

	// 3. top_level_dirs 策略（全部标记为 discovered）
	onlyTop := DiscoverModules(tree, ModuleDiscoveryPolicy{
		Strategy:          "top_level_dirs",
		ExcludeGlobs:      []string{"legacy_*"},
		MinEffectiveFiles: 3,
	}, archElems)
	if len(onlyTop) != 3 {
		t.Fatalf("top_level_dirs 策略预期 3 个模块，实际=%+v", onlyTop)
	}
	for _, m := range onlyTop {
		if m.Source != "discovered" {
			t.Fatalf("top_level_dirs 策略下模块 %s source 应为 discovered，实际=%s", m.Subdirectory, m.Source)
		}
	}

	// 4. 嵌套架构元素（如 services/order）在 merged 模式下应抑制父顶层目录 services
	nestedTree := &TreeFacet{
		Files: []FileSummary{
			{RelPath: "services/order/a.cpp", Language: "cpp", LOC: 100},
			{RelPath: "services/order/b.cpp", Language: "cpp", LOC: 100},
			{RelPath: "services/payment/c.cpp", Language: "cpp", LOC: 100},
			{RelPath: "common/x.cpp", Language: "cpp", LOC: 50},
			{RelPath: "common/y.cpp", Language: "cpp", LOC: 50},
			{RelPath: "common/z.cpp", Language: "cpp", LOC: 50},
		},
	}
	nestedRes := DiscoverModules(nestedTree, ModuleDiscoveryPolicy{
		Strategy:          "merged",
		MinEffectiveFiles: 3,
	}, []ArchElementScope{{ID: 202, Subdirectory: "services/order"}})

	if len(nestedRes) != 2 || nestedRes[0].Subdirectory != "common" || nestedRes[1].Subdirectory != "services/order" {
		t.Fatalf("嵌套架构元素父顶层目录抑制失败: %+v", nestedRes)
	}
}
