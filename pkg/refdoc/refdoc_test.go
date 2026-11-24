package refdoc

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEstimateTextTokensAndGate(t *testing.T) {
	if got := EstimateTextTokens(""); got != 0 {
		t.Fatalf("空串估算期望 0，实际 %d", got)
	}
	if got := EstimateTextTokens("abcdefg"); got != 2 {
		t.Fatalf("7 ASCII 字符估算期望 2，实际 %d", got)
	}
	if got := EstimateTextTokens("中文架构文档"); got != 4 {
		t.Fatalf("6 个汉字估算期望 4，实际 %d", got)
	}

	// 1. 非法 UTF-8 与空正文拦截
	badUTF8 := string([]byte{0xff, 0xfe, 0xfd})
	_, fail, err := ValidateAndParse(badUTF8, 524288, false, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "INVALID_PARAM" {
		t.Fatalf("期望非法 UTF-8 返回 INVALID_PARAM，实际 fail=%+v err=%v", fail, err)
	}
	_, fail, err = ValidateAndParse("   \n\t  ", 524288, false, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "INVALID_PARAM" {
		t.Fatalf("期望空白正文返回 INVALID_PARAM，实际 fail=%+v err=%v", fail, err)
	}

	// 2. 体积超限拦截
	oversized := strings.Repeat("a", 1025)
	_, fail, err = ValidateAndParse(oversized, 1024, false, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "PAYLOAD_TOO_LARGE" || fail.HTTPStatus != 413 {
		t.Fatalf("期望超体积返回 413 PAYLOAD_TOO_LARGE，实际 fail=%+v err=%v", fail, err)
	}

	// 3. > 4KB Base64 内嵌图拦截
	largeB64 := "![arch](data:image/png;base64," + strings.Repeat("A", 4097) + ")\n"
	_, fail, err = ValidateAndParse(largeB64, 524288, false, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "INLINE_BASE64_IMAGE" || fail.HTTPStatus != 422 {
		t.Fatalf("期望 >4KB Base64 图片返回 422 INLINE_BASE64_IMAGE，实际 fail=%+v err=%v", fail, err)
	}
	// <= 4KB Base64 允许通过
	smallB64 := "# 架构\n![icon](data:image/png;base64," + strings.Repeat("A", 128) + ")\n"
	parsedSmall, fail, err := ValidateAndParse(smallB64, 524288, false, "arch_design", nil)
	if err != nil || fail != nil || parsedSmall == nil {
		t.Fatalf("期望 <=4KB Base64 图片通过，实际 fail=%+v err=%v", fail, err)
	}

	// 4. 分级凭据门禁：硬凭据即使传 redact_and_save=true 仍无条件阻断
	hardPEM := "# 私钥\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234567890\n-----END RSA PRIVATE KEY-----\n"
	_, fail, err = ValidateAndParse(hardPEM, 524288, true, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "SECRET_DETECTED" || fail.HTTPStatus != 422 {
		t.Fatalf("期望 PEM 私钥即使 redact_and_save=true 仍阻断 SECRET_DETECTED，实际 fail=%+v err=%v", fail, err)
	}
	hardTok := "# Token\napi_token: \"sk-proj1234567890abcdef\"\n"
	_, fail, err = ValidateAndParse(hardTok, 524288, true, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "SECRET_DETECTED" {
		t.Fatalf("期望 Token 前缀即使 redact_and_save=true 仍阻断 SECRET_DETECTED，实际 fail=%+v err=%v", fail, err)
	}
	hardURL := "# DB\npostgres://admin:superpass123@10.0.0.5:5432/prod\n"
	_, fail, err = ValidateAndParse(hardURL, 524288, true, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "SECRET_DETECTED" {
		t.Fatalf("期望 URL 凭据即使 redact_and_save=true 仍阻断 SECRET_DETECTED，实际 fail=%+v err=%v", fail, err)
	}

	// 5. 软规则（key_blacklist / high_entropy）：默认返 SECRET_CONFIRM_REQUIRED，redact_and_save=true 脱敏入库
	softExample := "# 配置示例\n```yaml\npassword: \"demo_example_pass\"\n```\n"
	_, fail, err = ValidateAndParse(softExample, 524288, false, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "SECRET_CONFIRM_REQUIRED" || fail.HTTPStatus != 422 {
		t.Fatalf("期望软凭据默认返回 422 SECRET_CONFIRM_REQUIRED，实际 fail=%+v err=%v", fail, err)
	}
	parsedSoft, fail, err := ValidateAndParse(softExample, 524288, true, "arch_design", nil)
	if err != nil || fail != nil || parsedSoft == nil {
		t.Fatalf("期望传入 redact_and_save=true 后脱敏通过，实际 fail=%+v err=%v", fail, err)
	}
	if !parsedSoft.Redacted || strings.Contains(parsedSoft.Markdown, "demo_example_pass") || !strings.Contains(parsedSoft.Markdown, "[REDACTED]") {
		t.Fatalf("期望脱敏后正文已替换敏感值并标记 Redacted=true，实际: %+v", parsedSoft)
	}

	// 混合命中硬凭据 + 软凭据时，即使传 redact_and_save=true 仍必须硬阻断 SECRET_DETECTED
	mixedSecret := "# 混合\npassword: \"demo_pass\"\nauth: \"ghp_1234567890abcdef12345678\"\n"
	_, fail, err = ValidateAndParse(mixedSecret, 524288, true, "arch_design", nil)
	if err != nil || fail == nil || fail.Code != "SECRET_DETECTED" {
		t.Fatalf("期望硬+软混合命中仍硬阻断 SECRET_DETECTED，实际 fail=%+v err=%v", fail, err)
	}
}

func TestMarkdownSlicingFencedAndChunkingFallback(t *testing.T) {
	// 1. 围栏代码块内 # 不误判；ATX 标题正常切分
	mdATX := strings.Join([]string{
		"# 系统总体架构",
		"负责订单与支付协调。",
		"",
		"```bash",
		"# 这不是标题只是注释",
		"echo hello",
		"```",
		"",
		"## 1. 核心链路",
		"调用 `OrderGateway` 完成扣款。",
		"",
		"~~~python",
		"# 同样不是标题",
		"print(1)",
		"~~~",
	}, "\n")
	parsed, fail, err := ValidateAndParse(mdATX, 524288, false, "arch_design", []string{"OrderGateway", "src/order.go", "ns::Foo"})
	if err != nil || fail != nil {
		t.Fatalf("解析失败: fail=%+v err=%v", fail, err)
	}
	if len(parsed.TOC) != 2 {
		t.Fatalf("期望过滤围栏注释后仅 2 个标题，实际 %d: %+v", len(parsed.TOC), parsed.TOC)
	}
	if parsed.TOC[0].Title != "系统总体架构" || parsed.TOC[1].Title != "1. 核心链路" {
		t.Fatalf("标题解析不匹配: %+v", parsed.TOC)
	}
	// DeclaredTerms 自动剔除含 / 与 :: 的项
	if len(parsed.DeclaredTerms) != 1 || parsed.DeclaredTerms[0] != "OrderGateway" {
		t.Fatalf("期望 DeclaredTerms 仅保留 OrderGateway，实际: %v", parsed.DeclaredTerms)
	}

	// 2. Setext 与编号标题支持（全文无 ATX 标题时启用编号标题）
	mdNumbered := strings.Join([]string{
		"系统总览",
		"========",
		"这是系统概述段落。",
		"",
		"1.2 接口契约",
		"提供 gRPC 对外服务。",
		"",
		"二、容灾设计",
		"支持异地多活。",
	}, "\n")
	parsedNum, fail, err := ValidateAndParse(mdNumbered, 524288, false, "arch_design", nil)
	if err != nil || fail != nil {
		t.Fatalf("Setext/编号标题解析失败: fail=%+v err=%v", fail, err)
	}
	if len(parsedNum.TOC) != 3 {
		t.Fatalf("期望 Setext+编号标题产出 3 节，实际 %d: %+v", len(parsedNum.TOC), parsedNum.TOC)
	}

	// 3. 无标题长文本（>250行）按空行边界切分 sec-chunk-N，且单节 >250 行同样切分
	var lines []string
	for i := 1; i <= 380; i++ {
		lines = append(lines, fmt.Sprintf("第 %d 行业务说明内容，描述核心流程细节。", i))
		if i%20 == 0 {
			lines = append(lines, "")
		}
	}
	noHeadingMD := strings.Join(lines, "\n")
	parsedNoHead, fail, err := ValidateAndParse(noHeadingMD, 524288, false, "arch_design", nil)
	if err != nil || fail != nil {
		t.Fatalf("无标题长文档解析失败: fail=%+v err=%v", fail, err)
	}
	if len(parsedNoHead.TOC) < 2 {
		t.Fatalf("期望无标题 >250 行文档切分出多个虚拟分块，实际 %d", len(parsedNoHead.TOC))
	}
	for i, node := range parsedNoHead.TOC {
		if !node.IsChunk || !strings.HasPrefix(node.SectionID, "sec-chunk-") {
			t.Fatalf("第 %d 节应为 sec-chunk- 虚拟块: %+v", i, node)
		}
		if node.EndLine-node.StartLine+1 > 250 {
			t.Fatalf("虚拟分块行数超过 250 行上限: %+v", node)
		}
	}

	// 4. domain_glossary 才自动从列表/表格抽取术语，普通 arch_design 不自动抓取类名
	archWithCamel := "# 设计\n\n| 类名 | 说明 |\n| :--- | :--- |\n| `LegacyOrderMgr` | 旧类 |\n"
	parsedArch, _, _ := ValidateAndParse(archWithCamel, 524288, false, "arch_design", nil)
	if len(parsedArch.DeclaredTerms) != 0 {
		t.Fatalf("普通 arch_design 不应自动抓取类名进入 DeclaredTerms，实际: %v", parsedArch.DeclaredTerms)
	}
	glossaryMD := "# 术语表\n\n| 术语 | 说明 |\n| :--- | :--- |\n| RiskCenter | 风控中心 |\n- **SettlementEngine**: 结算引擎\n"
	parsedGloss, _, _ := ValidateAndParse(glossaryMD, 524288, false, "domain_glossary", nil)
	if len(parsedGloss.DeclaredTerms) < 2 {
		t.Fatalf("domain_glossary 应提取显式术语词条，实际: %v", parsedGloss.DeclaredTerms)
	}

	// 5. Fallback 摘要 <= 300 runes 且为有效 UTF-8
	summary := BuildFallbackSummary(mdATX, parsed.TOC, 300)
	if summary == "" || utf8.RuneCountInString(summary) > 300 || !utf8.ValidString(summary) {
		t.Fatalf("BuildFallbackSummary 结果异常: %q", summary)
	}
}

func TestReadSearchAndBudgetedExcerpts(t *testing.T) {
	var longSecLines []string
	longSecLines = append(longSecLines, "## 1. 订单状态机")
	for i := 1; i <= 260; i++ {
		longSecLines = append(longSecLines, fmt.Sprintf("状态流转规则第 %d 条：处理支付回调与超时关闭。", i))
	}
	longSecLines = append(longSecLines, "## 2. 风控校验", "调用 RiskCenter 验证黑名单与限额。")
	md := strings.Join(longSecLines, "\n")

	parsed, fail, err := ValidateAndParse(md, 524288, false, "arch_design", nil)
	if err != nil || fail != nil {
		t.Fatalf("解析失败: fail=%+v err=%v", fail, err)
	}
	if len(parsed.TOC) < 2 {
		t.Fatalf("期望至少 2 个章节节点，实际 %+v", parsed.TOC)
	}

	// ReadSection 上限 200 行与分页读取
	sec1 := parsed.TOC[0]
	excerpt, err := ReadSection(md, parsed.TOC, sec1.SectionID, 0, 200, 12*1024)
	if err != nil {
		t.Fatalf("ReadSection 失败: %v", err)
	}
	if excerpt.Content == "" || excerpt.EndLine < excerpt.StartLine {
		t.Fatalf("ReadSection 返回异常: %+v", excerpt)
	}

	// SearchSections
	hits := SearchSections([]SearchableDoc{{
		DocID:     11,
		Title:     "订单系统设计",
		VersionNo: 2,
		Markdown:  md,
		TOC:       parsed.TOC,
	}}, "RiskCenter", 0, 10)
	if len(hits) == 0 || hits[0].DocID != 11 {
		t.Fatalf("SearchSections 未命中 RiskCenter: %+v", hits)
	}

	// SelectBudgetedExcerptsForSlot 预算限制与相关性优先
	excerpts := SelectBudgetedExcerptsForSlot([]PromptDoc{{
		DocID:            11,
		Title:            "订单系统设计",
		DocKind:          "arch_design",
		VersionNo:        2,
		ScopeLevel:       "subsystem",
		ScopeRank:        2,
		SourceScopeLabel: "交易子系统",
		TOC:              parsed.TOC,
		Markdown:         md,
	}}, "风控校验与幂等边界", "说明 RiskCenter 限额校验策略", 1500)
	if len(excerpts) == 0 {
		t.Fatal("SelectBudgetedExcerptsForSlot 应命中相关章节")
	}
	totalTok := 0
	for _, ex := range excerpts {
		totalTok += ex.EstimatedTokens
	}
	if totalTok > 1500 {
		t.Fatalf("摘录预算超限: %d > 1500", totalTok)
	}
	if !strings.Contains(excerpts[0].Content, "RiskCenter") {
		t.Fatalf("期望优先选中含 RiskCenter 的高相关章节，实际: %+v", excerpts[0])
	}
}
