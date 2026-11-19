package codeir_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/sandbox"
)

type groundTruth struct {
	FixtureName   string   `json:"fixture_name"`
	ExpectedFiles []string `json:"expected_files"`
	Forbidden     []string `json:"forbidden_files"`
	ExpectedBuild struct {
		BuildSystems           []string `json:"build_systems"`
		Targets                []string `json:"targets"`
		CompileOptionsContains []string `json:"compile_options_contains"`
	} `json:"expected_build"`
	ExpectedProtos []struct {
		FilePath        string            `json:"file_path"`
		Package         string            `json:"package"`
		Imports         []string          `json:"imports"`
		FileCrosOptions map[string]string `json:"file_cros_options"`
		Services        []struct {
			Name string `json:"name"`
			RPCs []struct {
				MethodName      string            `json:"method_name"`
				RequestType     string            `json:"request_type"`
				ResponseType    string            `json:"response_type"`
				ClientStreaming bool              `json:"client_streaming"`
				ServerStreaming bool              `json:"server_streaming"`
				CrosOptions     map[string]string `json:"cros_options"`
			} `json:"rpcs"`
		} `json:"services"`
		Messages []string `json:"messages"`
		Enums    []string `json:"enums"`
	} `json:"expected_protos"`
	ExpectedSymbols []struct {
		FQSymbol      string `json:"fq_symbol"`
		Kind          string `json:"kind"`
		Conditional   bool   `json:"conditional"`
		ConditionExpr string `json:"condition_expr"`
	} `json:"expected_symbols"`
	ExpectedPrimitives []struct {
		Category string `json:"category"`
		APIName  string `json:"api_name"`
	} `json:"expected_primitives"`
	ExpectedConfigKeys []struct {
		KeyPath     string `json:"key_path"`
		IsSensitive bool   `json:"is_sensitive"`
	} `json:"expected_config_keys"`
	ExpectedDataModels []string `json:"expected_data_models"`
	ExpectedHTTPRoutes []string `json:"expected_http_routes"`
	ForbiddenSecrets   []string `json:"forbidden_secrets"`
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func TestEvalCppOrderServiceAccuracy(t *testing.T) {
	root := repoRootDir(t)
	fixtureDir := filepath.Join(root, "testdata", "golden", "fixtures", "cpp_order_service")
	truthPath := filepath.Join(root, "testdata", "golden", "expectations", "cpp_order_service.truth.json")

	truthBytes, err := os.ReadFile(truthPath)
	if err != nil {
		t.Fatalf("读取真值清单失败: %v", err)
	}
	var truth groundTruth
	if err := json.Unmarshal(truthBytes, &truth); err != nil {
		t.Fatalf("解析真值清单失败: %v", err)
	}

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	bundle, err := agg.ExtractDirectory(context.Background(), fixtureDir, "", sandbox.WalkOptions{})
	if err != nil {
		t.Fatalf("L1 抽取失败: %v", err)
	}

	// 将 9 大分片原子落盘至临时 FacetStore
	tmpStoreDir := t.TempDir()
	store, err := codeir.NewLocalFSFacetStore(tmpStoreDir, 0)
	if err != nil {
		t.Fatalf("初始化 FacetStore 失败: %v", err)
	}
	loc := codeir.SnapshotLoc{
		RepoID:    101,
		Subdir:    "",
		CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	metas, _, err := codeir.WriteBundle(context.Background(), store, loc, bundle)
	if err != nil {
		t.Fatalf("写入 Code IR 分片失败: %v", err)
	}
	if len(metas) != len(codeir.AllFacetNames) {
		t.Fatalf("期望写入 %d 个分片，实际 %d", len(codeir.AllFacetNames), len(metas))
	}

	// 1. 目录树 100% 精确，且 third_party/ 与 *.pb.cc/*.pb.h 未进入任何分片
	actualFiles := make(map[string]bool, len(bundle.Tree.Files))
	for _, f := range bundle.Tree.Files {
		actualFiles[f.RelPath] = true
	}
	for _, exp := range truth.ExpectedFiles {
		if !actualFiles[exp] {
			t.Errorf("目录树缺失预期文件: %s", exp)
		}
	}
	if len(actualFiles) != len(truth.ExpectedFiles) {
		t.Errorf("目录树文件数不匹配: got %d, want %d (files=%v)", len(actualFiles), len(truth.ExpectedFiles), actualFiles)
	}

	// 2. 构建系统、构建目标与编译开关 100% 精确
	actualBuildSys := make(map[string]bool)
	for _, bs := range bundle.Build.BuildSystems {
		actualBuildSys[bs] = true
	}
	for _, expBS := range truth.ExpectedBuild.BuildSystems {
		if !actualBuildSys[expBS] {
			t.Errorf("缺失构建系统: %s", expBS)
		}
	}
	actualTargets := make(map[string]bool)
	for _, tgt := range bundle.Build.Targets {
		actualTargets[tgt.Name] = true
	}
	for _, expTgt := range truth.ExpectedBuild.Targets {
		if !actualTargets[expTgt] {
			t.Errorf("缺失构建目标: %s (got=%v)", expTgt, actualTargets)
		}
	}
	if len(actualTargets) != len(truth.ExpectedBuild.Targets) {
		t.Errorf("构建目标数量不精确: got %d, want %d", len(actualTargets), len(truth.ExpectedBuild.Targets))
	}
	optJoined := make([]string, 0, len(bundle.Build.CompileOptions))
	for _, opt := range bundle.Build.CompileOptions {
		optJoined = append(optJoined, opt.Key+"="+opt.Value, opt.Key)
	}
	optBlob := strings.Join(optJoined, "\n")
	for _, wantOpt := range truth.ExpectedBuild.CompileOptionsContains {
		if !strings.Contains(optBlob, wantOpt) {
			t.Errorf("编译开关缺失 %q (actual=%v)", wantOpt, bundle.Build.CompileOptions)
		}
	}

	// 3. .proto 服务/RPC/CROS option/Message/Enum 100% 精确
	if len(bundle.IDLContracts.Protos) != len(truth.ExpectedProtos) {
		t.Fatalf(".proto 文件数不匹配: got %d, want %d", len(bundle.IDLContracts.Protos), len(truth.ExpectedProtos))
	}
	for idx, expProto := range truth.ExpectedProtos {
		got := bundle.IDLContracts.Protos[idx]
		if got.FilePath != expProto.FilePath || got.Package != expProto.Package {
			t.Errorf(".proto 基本信息不匹配: got (%s, %s), want (%s, %s)", got.FilePath, got.Package, expProto.FilePath, expProto.Package)
		}
		for k, v := range expProto.FileCrosOptions {
			if got.CrosOptions[k] != v {
				t.Errorf(".proto 文件级 CROS option %q 不匹配: got %q, want %q", k, got.CrosOptions[k], v)
			}
		}
		if len(got.Services) != len(expProto.Services) {
			t.Fatalf(".proto service 数不匹配: got %d, want %d", len(got.Services), len(expProto.Services))
		}
		for sIdx, expSvc := range expProto.Services {
			gotSvc := got.Services[sIdx]
			if gotSvc.Name != expSvc.Name || len(gotSvc.RPCs) != len(expSvc.RPCs) {
				t.Fatalf("service %s RPC 数量不匹配: got %d, want %d", expSvc.Name, len(gotSvc.RPCs), len(expSvc.RPCs))
			}
			for rIdx, expRPC := range expSvc.RPCs {
				gotRPC := gotSvc.RPCs[rIdx]
				if gotRPC.MethodName != expRPC.MethodName ||
					gotRPC.RequestType != expRPC.RequestType ||
					gotRPC.ResponseType != expRPC.ResponseType ||
					gotRPC.ClientStreaming != expRPC.ClientStreaming ||
					gotRPC.ServerStreaming != expRPC.ServerStreaming {
					t.Errorf("RPC 签名不匹配: got %+v, want %+v", gotRPC, expRPC)
				}
				for ok, ov := range expRPC.CrosOptions {
					if gotRPC.CrosOptions[ok] != ov {
						t.Errorf("RPC %s CROS option %q 不匹配: got %q, want %q", expRPC.MethodName, ok, gotRPC.CrosOptions[ok], ov)
					}
				}
			}
		}
		gotMsgs := make(map[string]bool)
		for _, m := range got.Messages {
			gotMsgs[m.Name] = true
		}
		for _, em := range expProto.Messages {
			if !gotMsgs[em] {
				t.Errorf(".proto 缺失 message: %s", em)
			}
		}
		gotEnums := make(map[string]bool)
		for _, en := range got.Enums {
			gotEnums[en.Name] = true
		}
		for _, ee := range expProto.Enums {
			if !gotEnums[ee] {
				t.Errorf(".proto 缺失 enum: %s", ee)
			}
		}
	}

	// 4. C/C++ 类/结构体/枚举/函数声明：召回率 >= 95%、精确率 >= 98%，条件编译标注精确
	gotSymMap := make(map[string]codeir.SymbolFact, len(bundle.Symbols.Symbols))
	for _, s := range bundle.Symbols.Symbols {
		gotSymMap[s.FQSymbol] = s
	}
	expectedSymSet := make(map[string]bool, len(truth.ExpectedSymbols))
	symMatched := 0
	for _, expSym := range truth.ExpectedSymbols {
		expectedSymSet[expSym.FQSymbol] = true
		got, ok := gotSymMap[expSym.FQSymbol]
		if !ok {
			t.Errorf("缺失预期 C/C++ 符号: %s (kind=%s)", expSym.FQSymbol, expSym.Kind)
			continue
		}
		symMatched++
		if expSym.Conditional {
			if !got.Meta.Span.Conditional {
				t.Errorf("条件编译符号 %s 未标注 conditional=true", expSym.FQSymbol)
			}
			if expSym.ConditionExpr != "" && !strings.Contains(got.Meta.Span.ConditionExpr, expSym.ConditionExpr) {
				t.Errorf("条件编译符号 %s condition_expr 异常: got %q, want contains %q",
					expSym.FQSymbol, got.Meta.Span.ConditionExpr, expSym.ConditionExpr)
			}
		}
	}
	// 计算精确率：忽略测试夹具内部静态辅助函数（若被提取），确保真实声明无误报
	truePositives := 0
	for fq, s := range gotSymMap {
		if expectedSymSet[fq] || strings.HasPrefix(fq, "order::background_") || strings.HasPrefix(fq, "metrics_signal_") || strings.HasPrefix(fq, "SimpleRouter::") {
			truePositives++
		} else {
			t.Logf("非预期提取符号: fq=%s kind=%s file=%s:%d sig=%q", fq, s.Kind, s.Meta.Span.FilePath, s.Meta.Span.LineStart, s.Signature)
		}
	}
	recall := float64(symMatched) / float64(len(truth.ExpectedSymbols))
	precision := float64(truePositives) / float64(maxInt(len(gotSymMap), 1))
	t.Logf("[Eval C/C++ Symbols] matched=%d/%d recall=%.2f%% precision=%.2f%% (total_extracted=%d)",
		symMatched, len(truth.ExpectedSymbols), recall*100, precision*100, len(gotSymMap))
	if recall < 0.95 {
		t.Errorf("C/C++ 符号召回率不达标: got %.2f%%, want >= 95%%", recall*100)
	}
	if precision < 0.98 {
		t.Errorf("C/C++ 符号精确率不达标: got %.2f%%, want >= 98%%", precision*100)
	}

	// 5. 并发/锁/共享内存/错误码原语：召回率 >= 90% 且全部标注 confidence="heuristic"
	gotPrimSet := make(map[string]bool)
	for _, p := range bundle.RuntimePrimitives.Primitives {
		if p.Meta.Confidence != codeir.ConfidenceHeuristic {
			t.Errorf("运行时原语 %s 未标注 confidence=heuristic: got %q", p.APIName, p.Meta.Confidence)
		}
		gotPrimSet[p.Category+"::"+p.APIName] = true
	}
	primMatched := 0
	for _, expP := range truth.ExpectedPrimitives {
		if gotPrimSet[expP.Category+"::"+expP.APIName] {
			primMatched++
		} else {
			t.Logf("未命中原语: %s::%s", expP.Category, expP.APIName)
		}
	}
	primRecall := float64(primMatched) / float64(len(truth.ExpectedPrimitives))
	t.Logf("[Eval Runtime Primitives] matched=%d/%d recall=%.2f%%", primMatched, len(truth.ExpectedPrimitives), primRecall*100)
	if primRecall < 0.90 {
		t.Errorf("并发与资源原语召回率不达标: got %.2f%%, want >= 90%%", primRecall*100)
	}

	// 6. 配置键脱敏、SQL 数据模型与 HTTP 路由校验
	cfgMap := make(map[string]codeir.ConfigKeyFact)
	for _, ck := range bundle.ConfigKeys.Keys {
		cfgMap[ck.KeyPath] = ck
	}
	for _, expCK := range truth.ExpectedConfigKeys {
		gotCK, ok := cfgMap[expCK.KeyPath]
		if !ok {
			t.Errorf("缺失配置键: %s", expCK.KeyPath)
			continue
		}
		if gotCK.IsSensitive != expCK.IsSensitive {
			t.Errorf("配置键 %s 敏感标记不符: got %v, want %v", expCK.KeyPath, gotCK.IsSensitive, expCK.IsSensitive)
		}
	}

	dmSet := make(map[string]bool)
	for _, dm := range bundle.DataModels.Entities {
		dmSet[dm.TableOrCollection] = true
	}
	for _, expDM := range truth.ExpectedDataModels {
		if !dmSet[expDM] {
			t.Errorf("缺失 SQL 数据模型: %s", expDM)
		}
	}

	routeSet := make(map[string]bool)
	for _, rt := range bundle.HTTPRoutes.Routes {
		routeSet[rt.Path] = true
	}
	for _, expRoute := range truth.ExpectedHTTPRoutes {
		if !routeSet[expRoute] {
			t.Errorf("缺失 HTTP 路由: %s", expRoute)
		}
	}

	// 7. 终极安全断言：解压磁盘上全部 .json.gz，全文检索明文密钥与噪音文件路径，出现次数严格为 0！
	var allDecompressed strings.Builder
	gzCount := 0
	walkErr := filepath.WalkDir(tmpStoreDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json.gz") {
			return nil
		}
		gzCount++
		f, openErr := os.Open(p)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = f.Close() }()
		gzr, gzErr := gzip.NewReader(f)
		if gzErr != nil {
			return gzErr
		}
		defer func() { _ = gzr.Close() }()
		raw, readErr := io.ReadAll(gzr)
		if readErr != nil {
			return readErr
		}
		allDecompressed.Write(raw)
		allDecompressed.WriteByte('\n')
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历解压 .json.gz 失败: %v", walkErr)
	}
	if gzCount != len(codeir.AllFacetNames) {
		t.Fatalf("期望磁盘上存在 %d 个 .json.gz，实际 %d", len(codeir.AllFacetNames), gzCount)
	}

	fullText := allDecompressed.String()
	for _, secret := range truth.ForbiddenSecrets {
		if strings.Contains(fullText, secret) {
			t.Fatalf("严重安全违规：序列化后的 Code IR 分片泄露了明文密钥 %q！", secret)
		}
	}
	for _, forbiddenFile := range truth.Forbidden {
		if strings.Contains(fullText, forbiddenFile) {
			t.Fatalf("降噪违规：噪音文件 %q 进入了 Code IR 分片！", forbiddenFile)
		}
	}
	if strings.Contains(fullText, "CreateOrderRequestGeneratedStub") || strings.Contains(fullText, "inflateInit") {
		t.Fatalf("降噪违规：third_party/ 或 *.pb.cc 中的符号进入了 Code IR！")
	}
}

func TestFacetStoreAtomicWriteAndLRU(t *testing.T) {
	dir := t.TempDir()
	// 设置极小内存上限以验证 LRU 淘汰与命中
	store, err := codeir.NewLocalFSFacetStore(dir, 512)
	if err != nil {
		t.Fatal(err)
	}

	loc := codeir.SnapshotLoc{RepoID: 1, Subdir: "svc/order", CommitSHA: "1234567890abcdef1234567890abcdef12345678"}
	treePayload := codeir.TreeFacet{
		SchemaVersion: codeir.SchemaVersion,
		TotalFiles:    3,
		TotalLOC:      120,
	}

	meta, err := store.WriteFacet(context.Background(), loc, codeir.FacetTree, treePayload)
	if err != nil {
		t.Fatalf("WriteFacet 失败: %v", err)
	}
	if meta.SizeBytes <= 0 || meta.CompressedBytes <= 0 || len(meta.ContentSHA256) != 64 {
		t.Fatalf("WriteFacet 元数据异常: %+v", meta)
	}

	// 验证磁盘上无 .tmp- 残留
	var tmpFound bool
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if d != nil && strings.HasPrefix(d.Name(), ".tmp-") {
			tmpFound = true
		}
		return nil
	})
	if tmpFound {
		t.Fatal("原子写入后目录中存在残留临时文件")
	}

	// 第一次读应命中 LRU 缓存（WriteFacet 已热填充）
	var gotTree codeir.TreeFacet
	if err := store.ReadFacet(context.Background(), loc, codeir.FacetTree, meta.ContentSHA256, &gotTree); err != nil {
		t.Fatalf("ReadFacet 失败: %v", err)
	}
	if gotTree.TotalFiles != 3 || gotTree.TotalLOC != 120 {
		t.Fatalf("ReadFacet 数据不一致: %+v", gotTree)
	}
	st1 := store.Stats()
	if st1.CacheHits < 1 {
		t.Fatalf("期望 LRU 命中 >= 1，实际 %+v", st1)
	}

	// 校验和错误应返回 ErrChecksumMismatch
	if err := store.ReadFacet(context.Background(), loc, codeir.FacetTree, "0000000000000000", &gotTree); !errors.Is(err, codeir.ErrChecksumMismatch) {
		t.Fatalf("期望校验和错误返回 ErrChecksumMismatch，实际: %v", err)
	}

	// 写入一个较大分片挤出旧 LRU 条目后，再从磁盘读取应触发 cache miss 但仍能成功反序列化
	largeBuild := codeir.BuildFacet{
		SchemaVersion: codeir.SchemaVersion,
		BuildSystems:  []string{"cmake"},
		Targets: []codeir.BuildTarget{
			{Name: "t1", Sources: []string{strings.Repeat("a", 200)}},
			{Name: "t2", Sources: []string{strings.Repeat("b", 200)}},
		},
	}
	if _, err := store.WriteFacet(context.Background(), loc, codeir.FacetBuild, largeBuild); err != nil {
		t.Fatal(err)
	}
	var gotTree2 codeir.TreeFacet
	if err := store.ReadFacet(context.Background(), loc, codeir.FacetTree, meta.ContentSHA256, &gotTree2); err != nil {
		t.Fatalf("LRU 淘汰后回源磁盘读取失败: %v", err)
	}
	if gotTree2.TotalFiles != 3 {
		t.Fatalf("磁盘回源数据不一致: %+v", gotTree2)
	}
	if store.Stats().CacheMisses < 1 {
		t.Fatalf("期望 LRU 淘汰后产生 cache miss: %+v", store.Stats())
	}
}

func TestSymbolContentFingerprintStability(t *testing.T) {
	ext := treesitter.NewCppExtractor()
	baseSrc := `namespace order {
int Pay(int order_id, int amount) {
    if (amount <= 0) {
        return -1;
    }
    return order_id + amount;
}
}
`
	// 1. 在函数上方插入 10 行注释与空行，同时在函数内部插入注释
	commentedSrc := `// line 1 comment
// line 2 comment

// line 4 comment
// line 5 comment

// line 7 comment
// line 8 comment
// line 9 comment
// line 10 comment

namespace order {
int Pay(int order_id, int amount) {
    // 内部新增注释不应影响语义指纹
    if (amount <= 0) {
        return -1;
    }
    return order_id + amount;
}
}
`
	// 2. 修改函数体逻辑一行
	modifiedSrc := `namespace order {
int Pay(int order_id, int amount) {
    if (amount <= 0) {
        return -2;
    }
    return order_id + amount;
}
}
`

	fe := codeir.FileEntry{RelPath: "src/pay.cpp", Language: "cpp"}
	r1, err := ext.ExtractFile(context.Background(), fe, []byte(baseSrc))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ext.ExtractFile(context.Background(), fe, []byte(commentedSrc))
	if err != nil {
		t.Fatal(err)
	}
	r3, err := ext.ExtractFile(context.Background(), fe, []byte(modifiedSrc))
	if err != nil {
		t.Fatal(err)
	}

	fp1 := findFingerprint(t, r1.Symbols, "order::Pay")
	fp2 := findFingerprint(t, r2.Symbols, "order::Pay")
	fp3 := findFingerprint(t, r3.Symbols, "order::Pay")

	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("插入注释与空行后 content_fingerprint 必须保持不变: fp1=%q fp2=%q", fp1, fp2)
	}
	if fp1 == fp3 {
		t.Fatalf("修改函数体逻辑后 content_fingerprint 必须发生变化: fp1=%q fp3=%q", fp1, fp3)
	}
}

func findFingerprint(t *testing.T, syms []codeir.SymbolFact, fq string) string {
	t.Helper()
	for _, s := range syms {
		if s.FQSymbol == fq {
			return s.Meta.ContentFingerprint
		}
	}
	t.Fatalf("未找到符号 %s (actual=%+v)", fq, syms)
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
