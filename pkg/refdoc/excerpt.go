package refdoc

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	DefaultReadSectionMaxLines = 200
	DefaultReadSectionMaxBytes = 12 * 1024
)

// ReadSection 按 section_id 分页只读读取参考文档单个章节（单次硬上限 200 行 / 12 KB）。
func ReadSection(markdown string, toc []HeadingNode, sectionID string, offsetLine, maxLines, maxBytes int) (*SectionExcerpt, error) {
	if strings.TrimSpace(sectionID) == "" {
		return nil, fmt.Errorf("section_id 不能为空")
	}
	if maxLines <= 0 || maxLines > DefaultReadSectionMaxLines {
		maxLines = DefaultReadSectionMaxLines
	}
	if maxBytes <= 0 || maxBytes > DefaultReadSectionMaxBytes {
		maxBytes = DefaultReadSectionMaxBytes
	}

	var target *HeadingNode
	for i := range toc {
		if toc[i].SectionID == sectionID {
			target = &toc[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("章节 %q 不存在", sectionID)
	}

	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	secStart := target.StartLine
	if secStart < 1 {
		secStart = 1
	}
	secEnd := target.EndLine
	if secEnd > len(lines) {
		secEnd = len(lines)
	}
	if secEnd < secStart {
		secEnd = secStart
	}

	start := secStart
	if offsetLine > 0 {
		start = secStart + offsetLine
	}
	if start > secEnd {
		return &SectionExcerpt{
			SectionID:  target.SectionID,
			Title:      target.Title,
			StartLine:  secEnd,
			EndLine:    secEnd,
			TotalLines: secEnd - secStart + 1,
			Truncated:  false,
			Content:    "",
		}, nil
	}

	end := start + maxLines - 1
	if end > secEnd {
		end = secEnd
	}

	var buf strings.Builder
	actualEnd := start - 1
	truncated := end < secEnd
	for ln := start; ln <= end; ln++ {
		formatted := fmt.Sprintf("%d: %s\n", ln, lines[ln-1])
		if buf.Len()+len(formatted) > maxBytes && buf.Len() > 0 {
			truncated = true
			break
		}
		buf.WriteString(formatted)
		actualEnd = ln
	}
	if actualEnd < start {
		actualEnd = start
	}

	return &SectionExcerpt{
		SectionID:  target.SectionID,
		Title:      target.Title,
		StartLine:  start,
		EndLine:    actualEnd,
		TotalLines: secEnd - secStart + 1,
		Truncated:  truncated,
		Content:    strings.TrimRight(buf.String(), "\n"),
	}, nil
}

// SearchSections 在已授权参考文档集合中执行确定性字面量关键词检索。
func SearchSections(docs []SearchableDoc, query string, filterDocID uint, maxMatches int) []SearchHit {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	if maxMatches <= 0 {
		maxMatches = 20
	}
	if maxMatches > 50 {
		maxMatches = 50
	}
	qLower := strings.ToLower(q)

	var hits []SearchHit
	for _, doc := range docs {
		if filterDocID > 0 && doc.DocID != filterDocID {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(doc.Markdown, "\r\n", "\n"), "\n")
		for idx, rawLine := range lines {
			lineNo := idx + 1
			if !strings.Contains(strings.ToLower(rawLine), qLower) {
				continue
			}
			secID, secTitle := locateHeadingForLine(doc.TOC, lineNo)
			snippet := strings.TrimSpace(rawLine)
			runes := []rune(snippet)
			if len(runes) > 180 {
				snippet = string(runes[:180]) + "..."
			}
			hits = append(hits, SearchHit{
				DocID:        doc.DocID,
				DocTitle:     doc.Title,
				VersionNo:    doc.VersionNo,
				SectionID:    secID,
				SectionTitle: secTitle,
				LineNumber:   lineNo,
				Snippet:      snippet,
			})
			if len(hits) >= maxMatches {
				return hits
			}
		}
	}
	return hits
}

func locateHeadingForLine(toc []HeadingNode, lineNo int) (string, string) {
	for _, n := range toc {
		if lineNo >= n.StartLine && lineNo <= n.EndLine {
			return n.SectionID, n.Title
		}
	}
	if len(toc) > 0 {
		return toc[0].SectionID, toc[0].Title
	}
	return "", ""
}

// SelectBudgetedExcerptsForSlot 为未开启工具的常规槽位按相关性与 Token 预算选取 1~3 个高相关参考章节片段。
func SelectBudgetedExcerptsForSlot(docs []PromptDoc, slotTitle, slotHint string, maxTokens int) []SlotExcerpt {
	if len(docs) == 0 {
		return nil
	}
	if maxTokens <= 0 {
		maxTokens = 1500
	}

	keywords := extractQueryKeywords(slotTitle + " " + slotHint)

	type scoredCandidate struct {
		excerpt SlotExcerpt
		score   int
		rank    int
	}

	var candidates []scoredCandidate
	for _, doc := range docs {
		lines := strings.Split(strings.ReplaceAll(doc.Markdown, "\r\n", "\n"), "\n")
		if len(doc.TOC) == 0 {
			content := truncateToTokenBudget(doc.Markdown, minInt(maxTokens, 600))
			if strings.TrimSpace(content) == "" {
				continue
			}
			tok := EstimateTextTokens(content)
			candidates = append(candidates, scoredCandidate{
				excerpt: SlotExcerpt{
					DocID:            doc.DocID,
					DocTitle:         doc.Title,
					VersionNo:        doc.VersionNo,
					ScopeLevel:       doc.ScopeLevel,
					SourceScopeLabel: doc.SourceScopeLabel,
					SectionID:        "sec-1",
					SectionTitle:     doc.Title,
					StartLine:        1,
					EndLine:          len(lines),
					Content:          content,
					EstimatedTokens:  tok,
				},
				score: scoreTextKeywords(content+" "+doc.Title, keywords) + doc.ScopeRank*2,
				rank:  doc.ScopeRank,
			})
			continue
		}

		for _, node := range doc.TOC {
			start := maxInt(node.StartLine, 1)
			end := minInt(node.EndLine, len(lines))
			if end < start {
				continue
			}
			secText := strings.TrimSpace(strings.Join(lines[start-1:end], "\n"))
			if secText == "" {
				continue
			}
			sc := scoreTextKeywords(node.Title+" "+node.Title+" "+secText, keywords)*10 + doc.ScopeRank
			candidates = append(candidates, scoredCandidate{
				excerpt: SlotExcerpt{
					DocID:            doc.DocID,
					DocTitle:         doc.Title,
					VersionNo:        doc.VersionNo,
					ScopeLevel:       doc.ScopeLevel,
					SourceScopeLabel: doc.SourceScopeLabel,
					SectionID:        node.SectionID,
					SectionTitle:     node.Title,
					StartLine:        start,
					EndLine:          end,
					Content:          secText,
					EstimatedTokens:  EstimateTextTokens(secText),
				},
				score: sc,
				rank:  doc.ScopeRank,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank > candidates[j].rank
		}
		if candidates[i].excerpt.DocID != candidates[j].excerpt.DocID {
			return candidates[i].excerpt.DocID < candidates[j].excerpt.DocID
		}
		return candidates[i].excerpt.StartLine < candidates[j].excerpt.StartLine
	})

	var selected []SlotExcerpt
	usedTokens := 0
	for _, cand := range candidates {
		if len(selected) >= 3 {
			break
		}
		remaining := maxTokens - usedTokens
		if remaining <= 40 {
			break
		}
		ex := cand.excerpt
		if ex.EstimatedTokens > remaining {
			ex.Content = truncateToTokenBudget(ex.Content, remaining)
			if strings.TrimSpace(ex.Content) == "" {
				continue
			}
			ex.EstimatedTokens = EstimateTextTokens(ex.Content)
		}
		selected = append(selected, ex)
		usedTokens += ex.EstimatedTokens
	}
	return selected
}

func extractQueryKeywords(text string) []string {
	lower := strings.ToLower(text)
	seen := make(map[string]bool)
	var kws []string
	addKW := func(w string) {
		w = strings.TrimSpace(w)
		if len([]rune(w)) < 2 || seen[w] {
			return
		}
		seen[w] = true
		kws = append(kws, w)
	}

	// 1. 英文/数字词元
	var asciiBuf strings.Builder
	var cjkRunes []rune
	flushASCII := func() {
		if asciiBuf.Len() > 0 {
			addKW(asciiBuf.String())
			asciiBuf.Reset()
		}
	}
	flushCJK := func() {
		if len(cjkRunes) >= 2 {
			// 切出双字 bigram 以适配中文无空格分词
			for i := 0; i+1 < len(cjkRunes); i++ {
				addKW(string(cjkRunes[i : i+2]))
			}
		}
		cjkRunes = nil
	}

	for _, r := range lower {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			flushCJK()
			asciiBuf.WriteRune(r)
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjkRunes = append(cjkRunes, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return kws
}

func scoreTextKeywords(text string, keywords []string) int {
	if len(keywords) == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	score := 0
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}
	return score
}

func truncateToTokenBudget(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if EstimateTextTokens(text) <= maxTokens {
		return text
	}
	const truncSuffix = "\n...(已按预算截断)"
	suffixTok := EstimateTextTokens(truncSuffix)
	effectiveBudget := maxTokens - suffixTok
	if effectiveBudget <= 0 {
		effectiveBudget = maxTokens
	}
	lines := strings.Split(text, "\n")
	var out strings.Builder
	for _, ln := range lines {
		cand := ln
		if out.Len() > 0 {
			cand = "\n" + ln
		}
		if EstimateTextTokens(out.String()+cand) > effectiveBudget {
			break
		}
		out.WriteString(cand)
	}
	if out.Len() > 0 {
		if maxTokens > suffixTok {
			return out.String() + truncSuffix
		}
		return out.String()
	}
	// 单行超长时按 rune 截断
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if EstimateTextTokens(string(runes[:mid])) <= maxTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return ""
	}
	return string(runes[:lo])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
