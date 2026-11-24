package refdoc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxSectionLinesBeforeChunk = 250
	targetChunkLines           = 160
)

var (
	reATXHeading      = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	reSetextUnderline = regexp.MustCompile(`^(=+|-+)\s*$`)
	reNumberedHeading = regexp.MustCompile(`^(?:\d+(?:\.\d+){0,3}\s+\S.*|[一二三四五六七八九十百]+[、.]\s*\S.*|第[一二三四五六七八九十百]+[章节条]\s+\S.*)$`)
	reGlossaryTable   = regexp.MustCompile(`^\|\s*` + "`?" + `([A-Za-z][A-Za-z0-9_-]{2,64})` + "`?" + `\s*\|`)
	reGlossaryBullet  = regexp.MustCompile(`^[-*+]\s+\*\*([A-Za-z][A-Za-z0-9_-]{2,64})\*\*\s*[:：-]`)
	reGlossaryBacktik = regexp.MustCompile("^[-*+]\\s+`([A-Za-z][A-Za-z0-9_-]{2,64})`\\s*[:：-]")
)

type rawHeading struct {
	title     string
	level     int
	startLine int // 1-based
}

// ValidateAndParse 执行入库门禁校验、多格式 Markdown 章节切片、无标题大节分块兜底及收窄 DeclaredTerms 提取。
func ValidateAndParse(markdown string, maxBytes int64, redactAndSave bool, docKind string, explicitTerms []string) (*ParsedDoc, *GateFailure, error) {
	cleanMD, redacted, gateFail := ValidateGate(markdown, maxBytes, redactAndSave)
	if gateFail != nil {
		return nil, gateFail, nil
	}

	lines := strings.Split(cleanMD, "\n")
	toc := parseTOC(lines)

	sum := sha256.Sum256([]byte(cleanMD))
	shaHex := hex.EncodeToString(sum[:])

	declared := buildDeclaredTerms(docKind, cleanMD, explicitTerms)

	return &ParsedDoc{
		Markdown:        cleanMD,
		ContentSHA256:   shaHex,
		ByteSize:        int64(len(cleanMD)),
		RuneCount:       utf8.RuneCountInString(cleanMD),
		LineCount:       len(lines),
		EstimatedTokens: EstimateTextTokens(cleanMD),
		TOC:             toc,
		DeclaredTerms:   declared,
		Redacted:        redacted,
	}, nil, nil
}

func parseTOC(lines []string) []HeadingNode {
	if len(lines) == 0 {
		return nil
	}

	// 第 1 遍扫描：围栏状态机跳过代码块，识别 ATX 与 Setext 标题
	var headings []rawHeading
	hasATX := false
	inFence := false
	var fenceChar byte
	fenceLen := 0

	// 预先标记每行是否位于围栏代码块内部
	isFencedLine := make([]bool, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if fc, fl, ok := parseFenceOpen(trimmed); ok {
				inFence = true
				fenceChar = fc
				fenceLen = fl
				isFencedLine[i] = true
				continue
			}
		} else {
			isFencedLine[i] = true
			if isFenceClose(trimmed, fenceChar, fenceLen) {
				inFence = false
				fenceChar = 0
				fenceLen = 0
			}
			continue
		}
	}

	for i := 0; i < len(lines); i++ {
		if isFencedLine[i] {
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		if m := reATXHeading.FindStringSubmatch(trimmed); len(m) == 3 {
			title := strings.TrimSpace(m[2])
			if title != "" {
				hasATX = true
				headings = append(headings, rawHeading{
					title:     title,
					level:     len(m[1]),
					startLine: i + 1,
				})
			}
			continue
		}
		// Setext 标题：当前行非空，下一行同样非围栏且为 === / ---
		if i+1 < len(lines) && !isFencedLine[i+1] && trimmed != "" && !strings.HasPrefix(trimmed, "|") && !strings.HasPrefix(trimmed, ">") {
			nextTrimmed := strings.TrimSpace(lines[i+1])
			if m := reSetextUnderline.FindStringSubmatch(nextTrimmed); len(m) == 2 {
				lvl := 2
				if strings.HasPrefix(m[1], "=") {
					lvl = 1
				}
				headings = append(headings, rawHeading{
					title:     trimmed,
					level:     lvl,
					startLine: i + 1,
				})
				i++ // 跳过下划线行
			}
		}
	}

	// 当全文无 ATX 标题时，补充识别独立行编号章节标题（如 "1.2 接口契约"、"二、容灾设计"）
	if !hasATX {
		existingLines := make(map[int]bool, len(headings))
		for _, h := range headings {
			existingLines[h.startLine] = true
		}
		for i, line := range lines {
			if isFencedLine[i] || existingLines[i+1] {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || len([]rune(trimmed)) > 80 {
				continue
			}
			// 避免把普通带句号的长句或列表项误判为标题
			if strings.HasSuffix(trimmed, "。") || strings.HasSuffix(trimmed, ".") {
				continue
			}
			if reNumberedHeading.MatchString(trimmed) {
				prevBlank := i == 0 || strings.TrimSpace(lines[i-1]) == ""
				if prevBlank {
					headings = append(headings, rawHeading{
						title:     trimmed,
						level:     2,
						startLine: i + 1,
					})
				}
			}
		}
		// 按 startLine 排序
		sortRawHeadings(headings)
	}

	// 若完全没有标题，整篇按空行边界切分为确定性虚拟分块 sec-chunk-N
	if len(headings) == 0 {
		return chunkLineRange(lines, 1, len(lines), 1)
	}

	// 构建有界章节，并对 > 250 行的超大节按空行边界继续拆分为 sec-chunk-N
	var toc []HeadingNode
	secIdx := 0
	chunkIdx := 0

	// 若首个标题前有较长前置导言 (> 30 行)，为其保留一个导言块
	if headings[0].startLine > 1 {
		introEnd := headings[0].startLine - 1
		introText := strings.TrimSpace(strings.Join(lines[:introEnd], "\n"))
		if introText != "" && introEnd > 30 {
			chunks := chunkLineRange(lines, 1, introEnd, chunkIdx+1)
			chunkIdx += len(chunks)
			toc = append(toc, chunks...)
		}
	}

	for i, h := range headings {
		endLine := len(lines)
		if i+1 < len(headings) {
			endLine = headings[i+1].startLine - 1
		}
		if endLine < h.startLine {
			endLine = h.startLine
		}
		lineSpan := endLine - h.startLine + 1
		if lineSpan <= maxSectionLinesBeforeChunk {
			secIdx++
			body := strings.Join(lines[h.startLine-1:endLine], "\n")
			toc = append(toc, HeadingNode{
				SectionID: fmt.Sprintf("sec-%d", secIdx),
				Title:     h.title,
				Level:     h.level,
				StartLine: h.startLine,
				EndLine:   endLine,
				RuneCount: utf8.RuneCountInString(body),
			})
			continue
		}

		// 大节拆分：保留首个标题节起始，超出部分切分为 sec-chunk-N（或整节切分但首块继承原标题）
		chunks := chunkLineRange(lines, h.startLine, endLine, chunkIdx+1)
		for ci, ch := range chunks {
			if ci == 0 {
				secIdx++
				ch.SectionID = fmt.Sprintf("sec-%d", secIdx)
				ch.Title = h.title
				ch.Level = h.level
				ch.IsChunk = false
			} else {
				chunkIdx++
				ch.SectionID = fmt.Sprintf("sec-chunk-%d", chunkIdx)
				ch.Title = fmt.Sprintf("%s（续 %d）", h.title, ci)
				ch.Level = h.level
				ch.IsChunk = true
			}
			toc = append(toc, ch)
		}
	}

	return toc
}

func chunkLineRange(lines []string, startLine, endLine, firstChunkNo int) []HeadingNode {
	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		return nil
	}

	var out []HeadingNode
	cur := startLine
	chunkNo := firstChunkNo
	for cur <= endLine {
		limit := cur + targetChunkLines - 1
		if limit >= endLine {
			limit = endLine
		} else {
			// 在 targetChunkLines 附近寻找空行边界（最多向前或向后探 40 行）
			best := findBlankSplitLine(lines, cur, limit, minInt(cur+maxSectionLinesBeforeChunk-1, endLine))
			if best > cur {
				limit = best
			}
		}
		if limit-cur+1 > maxSectionLinesBeforeChunk {
			limit = cur + maxSectionLinesBeforeChunk - 1
			if limit > endLine {
				limit = endLine
			}
		}
		segLines := lines[cur-1 : limit]
		body := strings.Join(segLines, "\n")
		title := firstNonEmptySnippet(segLines, chunkNo)
		out = append(out, HeadingNode{
			SectionID: fmt.Sprintf("sec-chunk-%d", chunkNo),
			Title:     title,
			Level:     2,
			StartLine: cur,
			EndLine:   limit,
			RuneCount: utf8.RuneCountInString(body),
			IsChunk:   true,
		})
		chunkNo++
		cur = limit + 1
	}
	return out
}

func findBlankSplitLine(lines []string, startLine, targetLine, maxLine int) int {
	for l := targetLine; l <= maxLine; l++ {
		if strings.TrimSpace(lines[l-1]) == "" && l > startLine {
			return l
		}
	}
	for l := targetLine; l > startLine+40; l-- {
		if strings.TrimSpace(lines[l-1]) == "" {
			return l
		}
	}
	return targetLine
}

func firstNonEmptySnippet(segLines []string, chunkNo int) string {
	for _, ln := range segLines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		t = strings.TrimLeft(t, "#-*>` ")
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		runes := []rune(t)
		if len(runes) > 36 {
			return string(runes[:36]) + "..."
		}
		return string(runes)
	}
	return fmt.Sprintf("文档分段 %d", chunkNo)
}

func parseFenceOpen(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 {
		return 0, 0, false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == ch {
		count++
	}
	if count < 3 {
		return 0, 0, false
	}
	if ch == '`' && strings.ContainsRune(trimmed[count:], '`') {
		return 0, 0, false
	}
	return ch, count, true
}

func isFenceClose(trimmed string, fenceChar byte, minLen int) bool {
	if len(trimmed) < minLen || trimmed[0] != fenceChar {
		return false
	}
	count := 0
	for count < len(trimmed) && trimmed[count] == fenceChar {
		count++
	}
	return count >= minLen && strings.TrimSpace(trimmed[count:]) == ""
}

func sortRawHeadings(h []rawHeading) {
	for i := 1; i < len(h); i++ {
		for j := i; j > 0 && h[j].startLine < h[j-1].startLine; j-- {
			h[j], h[j-1] = h[j-1], h[j]
		}
	}
}

func buildDeclaredTerms(docKind, cleanMD string, explicitTerms []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(term string) {
		t := strings.TrimSpace(strings.Trim(term, "`\"'"))
		if t == "" || len([]rune(t)) < 2 || len([]rune(t)) > 80 {
			return
		}
		// 严禁把文件路径或 C++ 限定符作为外部术语豁免（ADR-10 §3.3.4）
		if strings.Contains(t, "/") || strings.Contains(t, "\\") || strings.Contains(t, "::") {
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}

	for _, t := range explicitTerms {
		add(t)
	}

	// 仅在 domain_glossary 类型文档下，自动从术语表格首列或加粗/反引号列表项提取词条
	if strings.TrimSpace(docKind) == "domain_glossary" {
		lines := strings.Split(cleanMD, "\n")
		for _, ln := range lines {
			tr := strings.TrimSpace(ln)
			if tr == "" || strings.HasPrefix(tr, "| :") || strings.HasPrefix(tr, "|-") {
				continue
			}
			if m := reGlossaryTable.FindStringSubmatch(tr); len(m) == 2 {
				lower := strings.ToLower(m[1])
				if lower != "term" && lower != "name" && lower != "key" {
					add(m[1])
				}
				continue
			}
			if m := reGlossaryBullet.FindStringSubmatch(tr); len(m) == 2 {
				add(m[1])
				continue
			}
			if m := reGlossaryBacktik.FindStringSubmatch(tr); len(m) == 2 {
				add(m[1])
			}
		}
	}

	if out == nil {
		return []string{}
	}
	return out
}

// BuildFallbackSummary 生成确定性首段+大纲兜底摘要（严格 <= maxRunes，默认 300 字）。
func BuildFallbackSummary(markdown string, toc []HeadingNode, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 300
	}

	var paras []string
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	inFence := false
	var curPara []string
	flushPara := func() {
		if len(curPara) > 0 {
			p := strings.TrimSpace(strings.Join(curPara, " "))
			if p != "" {
				paras = append(paras, p)
			}
			curPara = nil
		}
	}
	for _, ln := range lines {
		tr := strings.TrimSpace(ln)
		if strings.HasPrefix(tr, "```") || strings.HasPrefix(tr, "~~~") {
			inFence = !inFence
			flushPara()
			continue
		}
		if inFence {
			continue
		}
		if tr == "" {
			flushPara()
			continue
		}
		if strings.HasPrefix(tr, "#") || strings.HasPrefix(tr, "===") || strings.HasPrefix(tr, "---") {
			flushPara()
			continue
		}
		curPara = append(curPara, tr)
		if len(paras) >= 2 {
			break
		}
	}
	flushPara()

	var sb strings.Builder
	if len(paras) > 0 {
		sb.WriteString(paras[0])
	}
	if len(toc) > 0 {
		var titles []string
		for i, node := range toc {
			if i >= 5 {
				break
			}
			if node.Title != "" {
				titles = append(titles, node.Title)
			}
		}
		if len(titles) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("；")
			}
			sb.WriteString("主要章节：")
			sb.WriteString(strings.Join(titles, "、"))
		}
	}

	res := strings.TrimSpace(sb.String())
	if res == "" {
		res = strings.TrimSpace(markdown)
	}
	runes := []rune(res)
	if len(runes) > maxRunes {
		if maxRunes > 3 {
			return string(runes[:maxRunes-3]) + "..."
		}
		return string(runes[:maxRunes])
	}
	return res
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
