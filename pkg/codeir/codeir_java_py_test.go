package codeir_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code-archdoc/pkg/codeir"
	"code-archdoc/pkg/codeir/treesitter"
	"code-archdoc/pkg/sandbox"
)

type jvmPyGroundTruth struct {
	FixtureName          string   `json:"fixture_name"`
	ExpectedFiles        []string `json:"expected_files"`
	ForbiddenFiles       []string `json:"forbidden_files"`
	ExpectedBuildSystems []string `json:"expected_build_systems"`
	ExpectedTargets      []string `json:"expected_targets"`
	ExpectedDeps         []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Scope   string `json:"scope"`
	} `json:"expected_deps"`
	ExpectedRoutes []struct {
		Method          string `json:"method"`
		Path            string `json:"path"`
		HandlerFQSymbol string `json:"handler_fq_symbol"`
		Framework       string `json:"framework"`
	} `json:"expected_routes"`
	ExpectedDataModels []struct {
		EntityName string   `json:"entity_name"`
		Table      string   `json:"table"`
		Fields     []string `json:"fields"`
	} `json:"expected_data_models"`
	ExpectedSchedulers []struct {
		Category          string `json:"category"`
		APIName           string `json:"api_name"`
		EnclosingFQSymbol string `json:"enclosing_fq_symbol"`
	} `json:"expected_schedulers"`
	ExpectedSymbols  []string `json:"expected_symbols"`
	ForbiddenSecrets []string `json:"forbidden_secrets"`
}

func runEvalJvmPyFixture(t *testing.T, fixtureName string) {
	t.Helper()
	root := repoRootDir(t)
	fixtureDir := filepath.Join(root, "testdata", "golden", "fixtures", fixtureName)
	truthPath := filepath.Join(root, "testdata", "golden", "expectations", fixtureName+".truth.json")

	truthBytes, err := os.ReadFile(truthPath)
	if err != nil {
		t.Fatalf("读取真值清单失败: %v", err)
	}
	var truth jvmPyGroundTruth
	if err := json.Unmarshal(truthBytes, &truth); err != nil {
		t.Fatalf("解析真值清单失败: %v", err)
	}

	agg := codeir.NewAggregator(treesitter.DefaultExtractors()...)
	bundle, err := agg.ExtractDirectory(context.Background(), fixtureDir, "", sandbox.WalkOptions{})
	if err != nil {
		t.Fatalf("L1 抽取失败: %v", err)
	}

	// 1. 验证目录树与降噪
	actualFiles := make(map[string]bool, len(bundle.Tree.Files))
	for _, f := range bundle.Tree.Files {
		actualFiles[f.RelPath] = true
	}
	for _, exp := range truth.ExpectedFiles {
		if !actualFiles[exp] {
			t.Errorf("目录树缺失预期文件: %s (got=%v)", exp, actualFiles)
		}
	}
	if len(actualFiles) != len(truth.ExpectedFiles) {
		t.Errorf("目录树文件数不精确: got %d, want %d (files=%v)", len(actualFiles), len(truth.ExpectedFiles), actualFiles)
	}

	// 2. 验证构建系统、目标与依赖 100% 精确
	gotBS := make(map[string]bool)
	for _, bs := range bundle.Build.BuildSystems {
		gotBS[bs] = true
	}
	for _, expBS := range truth.ExpectedBuildSystems {
		if !gotBS[expBS] {
			t.Errorf("缺失构建系统: %s (got=%v)", expBS, bundle.Build.BuildSystems)
		}
	}
	gotTargets := make(map[string]bool)
	for _, tgt := range bundle.Build.Targets {
		gotTargets[tgt.Name] = true
	}
	for _, expTgt := range truth.ExpectedTargets {
		if !gotTargets[expTgt] {
			t.Errorf("缺失构建目标: %s (got=%v)", expTgt, bundle.Build.Targets)
		}
	}
	gotDepMap := make(map[string]codeir.ExternalDep)
	for _, d := range bundle.Deps.ExternalDeps {
		gotDepMap[d.Name] = d
	}
	for _, expD := range truth.ExpectedDeps {
		got, ok := gotDepMap[expD.Name]
		if !ok {
			t.Errorf("缺失外部依赖: %s (got=%+v)", expD.Name, bundle.Deps.ExternalDeps)
			continue
		}
		if expD.Version != "" && got.Version != expD.Version {
			t.Errorf("依赖 %s 版本不匹配: got %q, want %q", expD.Name, got.Version, expD.Version)
		}
		if expD.Scope != "" && got.Scope != expD.Scope {
			t.Errorf("依赖 %s scope 不匹配: got %q, want %q", expD.Name, got.Scope, expD.Scope)
		}
	}

	// 3. 验证 HTTP 路由 100% 精确（含类+方法拼接）
	gotRoutes := make(map[string]codeir.HTTPRouteFact, len(bundle.HTTPRoutes.Routes))
	for _, rt := range bundle.HTTPRoutes.Routes {
		key := rt.Method + " " + rt.Path
		gotRoutes[key] = rt
	}
	for _, expR := range truth.ExpectedRoutes {
		key := expR.Method + " " + expR.Path
		got, ok := gotRoutes[key]
		if !ok {
			t.Errorf("缺失 HTTP 路由 %q (actual=%+v)", key, bundle.HTTPRoutes.Routes)
			continue
		}
		if got.HandlerFQSymbol != expR.HandlerFQSymbol {
			t.Errorf("路由 %q HandlerFQSymbol 不匹配: got %q, want %q", key, got.HandlerFQSymbol, expR.HandlerFQSymbol)
		}
		if got.Framework != expR.Framework {
			t.Errorf("路由 %q Framework 不匹配: got %q, want %q", key, got.Framework, expR.Framework)
		}
		if got.Meta.Confidence != codeir.ConfidenceExact {
			t.Errorf("路由 %q 应标记 confidence=exact, got %q", key, got.Meta.Confidence)
		}
	}
	if len(bundle.HTTPRoutes.Routes) != len(truth.ExpectedRoutes) {
		t.Errorf("HTTP 路由总数不精确: got %d, want %d (%+v)", len(bundle.HTTPRoutes.Routes), len(truth.ExpectedRoutes), bundle.HTTPRoutes.Routes)
	}

	// 4. 验证 ORM 数据模型与字段 100% 精确
	gotModels := make(map[string]codeir.DataModelFact, len(bundle.DataModels.Entities))
	for _, m := range bundle.DataModels.Entities {
		gotModels[m.TableOrCollection] = m
	}
	for _, expM := range truth.ExpectedDataModels {
		got, ok := gotModels[expM.Table]
		if !ok {
			t.Errorf("缺失 ORM 数据模型表 %q (actual=%+v)", expM.Table, bundle.DataModels.Entities)
			continue
		}
		if got.EntityName != expM.EntityName {
			t.Errorf("表 %q EntityName 不匹配: got %q, want %q", expM.Table, got.EntityName, expM.EntityName)
		}
		gotFields := make(map[string]bool, len(got.Fields))
		for _, f := range got.Fields {
			gotFields[f.ColumnName] = true
		}
		for _, ef := range expM.Fields {
			if !gotFields[ef] {
				t.Errorf("实体 %s (%s) 缺失字段 %q (got=%+v)", expM.EntityName, expM.Table, ef, got.Fields)
			}
		}
	}

	// 5. 验证调度注解/任务 100% 精确
	gotScheds := make(map[string]codeir.RuntimePrimitiveFact)
	for _, p := range bundle.RuntimePrimitives.Primitives {
		if p.Category == "scheduler" {
			gotScheds[p.APIName+"::"+p.EnclosingFQSymbol] = p
		}
	}
	for _, expS := range truth.ExpectedSchedulers {
		key := expS.APIName + "::" + expS.EnclosingFQSymbol
		got, ok := gotScheds[key]
		if !ok {
			t.Errorf("缺失调度原语 %q (actual=%+v)", key, bundle.RuntimePrimitives.Primitives)
			continue
		}
		if got.Meta.Confidence != codeir.ConfidenceHeuristic {
			t.Errorf("调度原语 %q 应标记 confidence=heuristic", key)
		}
	}

	// 6. 验证核心符号 100% 覆盖
	gotSyms := make(map[string]bool, len(bundle.Symbols.Symbols))
	for _, s := range bundle.Symbols.Symbols {
		gotSyms[s.FQSymbol] = true
	}
	for _, expSym := range truth.ExpectedSymbols {
		if !gotSyms[expSym] {
			t.Errorf("缺失核心符号 %q (actual=%+v)", expSym, gotSyms)
		}
	}

	// 7. 终极安全门禁：压缩落盘全部 9 大分片并解压全文扫描，明文密钥与噪音文件出现次数严格为 0
	tmpStoreDir := t.TempDir()
	store, err := codeir.NewLocalFSFacetStore(tmpStoreDir, 0)
	if err != nil {
		t.Fatalf("初始化 FacetStore 失败: %v", err)
	}
	loc := codeir.SnapshotLoc{
		RepoID:    202,
		Subdir:    "",
		CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if _, _, err := codeir.WriteBundle(context.Background(), store, loc, bundle); err != nil {
		t.Fatalf("写入 Code IR 分片失败: %v", err)
	}

	var allDecompressed strings.Builder
	if err := filepath.WalkDir(tmpStoreDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json.gz") {
			return nil
		}
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
	}); err != nil {
		t.Fatalf("解压分片失败: %v", err)
	}

	fullText := allDecompressed.String()
	for _, secret := range truth.ForbiddenSecrets {
		if strings.Contains(fullText, secret) {
			t.Fatalf("严重安全违规：序列化后的 Code IR 分片泄露明文密钥 %q！", secret)
		}
	}
	for _, forbiddenFile := range truth.ForbiddenFiles {
		if strings.Contains(fullText, forbiddenFile) {
			t.Fatalf("降噪违规：噪音文件 %q 进入了 Code IR 分片！", forbiddenFile)
		}
	}
}

func TestEvalJavaSpringAndORMAccuracy(t *testing.T) {
	runEvalJvmPyFixture(t, "java_billing_svc")
}

func TestEvalPythonFastAPIAndSQLAlchemyAccuracy(t *testing.T) {
	runEvalJvmPyFixture(t, "py_risk_api")
}

func TestGradleBuildExtractor(t *testing.T) {
	ext := codeir.NewBuildExtractor()
	if !ext.CanHandle("build.gradle") || !ext.CanHandle("sub/build.gradle.kts") {
		t.Fatal("BuildExtractor 应支持 build.gradle 与 build.gradle.kts")
	}
	gradleSrc := `
plugins {
    id 'java'
    id 'org.springframework.boot' version '3.3.2'
}

group = 'com.acme.ledger'
version = '2.1.0'

dependencies {
    implementation 'org.springframework.boot:spring-boot-starter-web:3.3.2'
    runtimeOnly("org.postgresql:postgresql:42.7.3")
    testImplementation "org.junit.jupiter:junit-jupiter:5.10.2"
}
`
	res, err := ext.ExtractFile(context.Background(), codeir.FileEntry{RelPath: "build.gradle", Language: "build"}, []byte(gradleSrc))
	if err != nil {
		t.Fatalf("解析 build.gradle 失败: %v", err)
	}
	if len(res.BuildSystems) != 1 || res.BuildSystems[0] != "gradle" {
		t.Fatalf("期望 build_systems=[gradle], got %v", res.BuildSystems)
	}
	if len(res.ExternalDeps) != 3 {
		t.Fatalf("期望提取 3 条 Gradle 依赖，实际 %d (%+v)", len(res.ExternalDeps), res.ExternalDeps)
	}
	depMap := make(map[string]codeir.ExternalDep)
	for _, d := range res.ExternalDeps {
		depMap[d.Name] = d
	}
	if depMap["org.springframework.boot:spring-boot-starter-web"].Scope != "compile" {
		t.Errorf("implementation 依赖应映射为 compile scope: %+v", depMap["org.springframework.boot:spring-boot-starter-web"])
	}
	if depMap["org.postgresql:postgresql"].Scope != "runtime" {
		t.Errorf("runtimeOnly 依赖应映射为 runtime scope: %+v", depMap["org.postgresql:postgresql"])
	}
	if depMap["org.junit.jupiter:junit-jupiter"].Scope != "test" {
		t.Errorf("testImplementation 依赖应映射为 test scope: %+v", depMap["org.junit.jupiter:junit-jupiter"])
	}
}
