package drift

import (
	"fmt"
	"math"
	"strings"
)

// SectionContent 表示用于章节级 Diff 与修改率计算的纯领域章节载荷（零数据库耦合）。
type SectionContent struct {
	SlotID   string `json:"slot_id"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// SectionDiffItem 对应 GET /api/documents/:id/revisions/diff 响应中的单个章节差异项。
type SectionDiffItem struct {
	SlotID       string `json:"slot_id"`
	Title        string `json:"title"`
	Changed      bool   `json:"changed"`
	UnifiedDiff  string `json:"unified_diff"`
	AddedLines   int    `json:"added_lines"`
	RemovedLines int    `json:"removed_lines"`
}

type diffOpKind byte

const (
	opEqual  diffOpKind = ' '
	opDelete diffOpKind = '-'
	opInsert diffOpKind = '+'
)

type diffOp struct {
	kind diffOpKind
	line string
}

// ComputeSectionDiffs 按稳定槽位顺序对比两版本文档的章节差异，生成标准 Unified Diff 与增删行数。
func ComputeSectionDiffs(fromSections, toSections []SectionContent) []SectionDiffItem {
	fromMap := make(map[string]SectionContent, len(fromSections))
	for _, s := range fromSections {
		fromMap[s.SlotID] = s
	}
	toMap := make(map[string]SectionContent, len(toSections))
	for _, s := range toSections {
		toMap[s.SlotID] = s
	}

	// 优先保留目标版本章节顺序，再补充仅存在于旧版本的已移除槽位
	orderedIDs := make([]string, 0, len(toSections)+len(fromSections))
	seen := make(map[string]bool, len(toSections)+len(fromSections))
	for _, s := range toSections {
		if !seen[s.SlotID] {
			seen[s.SlotID] = true
			orderedIDs = append(orderedIDs, s.SlotID)
		}
	}
	for _, s := range fromSections {
		if !seen[s.SlotID] {
			seen[s.SlotID] = true
			orderedIDs = append(orderedIDs, s.SlotID)
		}
	}

	items := make([]SectionDiffItem, 0, len(orderedIDs))
	for _, slotID := range orderedIDs {
		fromSec, hasFrom := fromMap[slotID]
		toSec, hasTo := toMap[slotID]

		title := toSec.Title
		if title == "" {
			title = fromSec.Title
		}

		fromMD := ""
		if hasFrom {
			fromMD = fromSec.Markdown
		}
		toMD := ""
		if hasTo {
			toMD = toSec.Markdown
		}

		if fromMD == toMD && hasFrom == hasTo {
			items = append(items, SectionDiffItem{
				SlotID:  slotID,
				Title:   title,
				Changed: false,
			})
			continue
		}

		udiff, added, removed := ComputeUnifiedDiff(fromMD, toMD)
		items = append(items, SectionDiffItem{
			SlotID:       slotID,
			Title:        title,
			Changed:      true,
			UnifiedDiff:  udiff,
			AddedLines:   added,
			RemovedLines: removed,
		})
	}
	return items
}

// ComputeUnifiedDiff 对两段文本执行确定性行级 Diff，生成带上下文的标准 Unified Diff 及增删行数。
func ComputeUnifiedDiff(fromText, toText string) (string, int, int) {
	if fromText == toText {
		return "", 0, 0
	}

	a := splitLines(fromText)
	b := splitLines(toText)
	ops := diffLines(a, b)

	added, removed := 0, 0
	for _, op := range ops {
		switch op.kind {
		case opInsert:
			added++
		case opDelete:
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return "", 0, 0
	}

	const contextLines = 3
	type hunkRange struct {
		start int
		end   int
	}

	var ranges []hunkRange
	for i, op := range ops {
		if op.kind == opEqual {
			continue
		}
		s := i - contextLines
		if s < 0 {
			s = 0
		}
		e := i + contextLines + 1
		if e > len(ops) {
			e = len(ops)
		}
		if len(ranges) > 0 && s <= ranges[len(ranges)-1].end {
			if e > ranges[len(ranges)-1].end {
				ranges[len(ranges)-1].end = e
			}
		} else {
			ranges = append(ranges, hunkRange{start: s, end: e})
		}
	}

	// 预先计算每条 op 之前的旧/新文件行号（1-based）
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	oLine, nLine := 1, 1
	for i, op := range ops {
		oldPos[i] = oLine
		newPos[i] = nLine
		switch op.kind {
		case opEqual:
			oLine++
			nLine++
		case opDelete:
			oLine++
		case opInsert:
			nLine++
		}
	}
	oldPos[len(ops)] = oLine
	newPos[len(ops)] = nLine

	var sb strings.Builder
	for _, r := range ranges {
		oldStart := oldPos[r.start]
		newStart := newPos[r.start]
		oldCount, newCount := 0, 0
		for i := r.start; i < r.end; i++ {
			switch ops[i].kind {
			case opEqual:
				oldCount++
				newCount++
			case opDelete:
				oldCount++
			case opInsert:
				newCount++
			}
		}
		if oldCount == 0 {
			oldStart = maxInt(oldStart-1, 0)
		}
		if newCount == 0 {
			newStart = maxInt(newStart-1, 0)
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for i := r.start; i < r.end; i++ {
			sb.WriteByte(byte(ops[i].kind))
			sb.WriteString(ops[i].line)
			sb.WriteByte('\n')
		}
	}

	return strings.TrimRight(sb.String(), "\n"), added, removed
}

// ComputeEditDiffRatio 计算首版 AI 草稿到审批定稿的整篇与逐章节修改比例（F-26，范围 [0, 1]）。
// 公式：changed_chars / max(draft_chars, approved_chars, 1)。
func ComputeEditDiffRatio(draftSections, approvedSections []SectionContent) (float64, map[string]float64) {
	draftMap := make(map[string]string, len(draftSections))
	for _, s := range draftSections {
		draftMap[s.SlotID] = s.Markdown
	}
	approvedMap := make(map[string]string, len(approvedSections))
	for _, s := range approvedSections {
		approvedMap[s.SlotID] = s.Markdown
	}

	seen := make(map[string]bool, len(draftSections)+len(approvedSections))
	slotIDs := make([]string, 0, len(draftSections)+len(approvedSections))
	for _, s := range approvedSections {
		if !seen[s.SlotID] {
			seen[s.SlotID] = true
			slotIDs = append(slotIDs, s.SlotID)
		}
	}
	for _, s := range draftSections {
		if !seen[s.SlotID] {
			seen[s.SlotID] = true
			slotIDs = append(slotIDs, s.SlotID)
		}
	}

	slotRatios := make(map[string]float64, len(slotIDs))
	totalChanged := 0
	totalDraftChars := 0
	totalApprovedChars := 0

	for _, slotID := range slotIDs {
		dText := draftMap[slotID]
		aText := approvedMap[slotID]
		dLen := len([]rune(dText))
		aLen := len([]rune(aText))
		changed := charEditDistance(dText, aText)

		denom := maxInt(maxInt(dLen, aLen), 1)
		ratio := clampRatio(float64(changed) / float64(denom))
		slotRatios[slotID] = round4(ratio)

		totalChanged += changed
		totalDraftChars += dLen
		totalApprovedChars += aLen
	}

	totalDenom := maxInt(maxInt(totalDraftChars, totalApprovedChars), 1)
	overall := round4(clampRatio(float64(totalChanged) / float64(totalDenom)))
	return overall, slotRatios
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	if normalized == "" {
		return nil
	}
	return strings.Split(normalized, "\n")
}

// diffLines 计算两行序列的最小编辑操作序列（去除公共前后缀后执行 LCS 动态规划）。
func diffLines(a, b []string) []diffOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	var ops []diffOp
	for i := 0; i < prefix; i++ {
		ops = append(ops, diffOp{kind: opEqual, line: a[i]})
	}

	// 防止极端大文本 O(M*N) 内存暴涨：当 M*N > 4_000_000 时退化为整块删+增
	if len(midA)*len(midB) > 4_000_000 {
		for _, l := range midA {
			ops = append(ops, diffOp{kind: opDelete, line: l})
		}
		for _, l := range midB {
			ops = append(ops, diffOp{kind: opInsert, line: l})
		}
	} else {
		ops = append(ops, lcsDiff(midA, midB)...)
	}

	for i := len(a) - suffix; i < len(a); i++ {
		ops = append(ops, diffOp{kind: opEqual, line: a[i]})
	}
	return ops
}

func lcsDiff(a, b []string) []diffOp {
	m, n := len(a), len(b)
	if m == 0 {
		ops := make([]diffOp, 0, n)
		for _, l := range b {
			ops = append(ops, diffOp{kind: opInsert, line: l})
		}
		return ops
	}
	if n == 0 {
		ops := make([]diffOp, 0, m)
		for _, l := range a {
			ops = append(ops, diffOp{kind: opDelete, line: l})
		}
		return ops
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < m && j < n {
		if a[i] == b[j] {
			ops = append(ops, diffOp{kind: opEqual, line: a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{kind: opDelete, line: a[i]})
			i++
		} else {
			ops = append(ops, diffOp{kind: opInsert, line: b[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, diffOp{kind: opDelete, line: a[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, diffOp{kind: opInsert, line: b[j]})
	}
	return ops
}

// charEditDistance 计算两段文本的字符级编辑距离（中等文本采用精确 Levenshtein，超大文本按行级 Diff 聚合）。
func charEditDistance(a, b string) int {
	if a == b {
		return 0
	}
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	// 去掉公共前后缀以缩小 DP 规模
	prefix := 0
	for prefix < len(ra) && prefix < len(rb) && ra[prefix] == rb[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(ra)-prefix && suffix < len(rb)-prefix && ra[len(ra)-1-suffix] == rb[len(rb)-1-suffix] {
		suffix++
	}
	ra = ra[prefix : len(ra)-suffix]
	rb = rb[prefix : len(rb)-suffix]
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	if len(ra)*len(rb) <= 2_000_000 {
		return levenshteinRunes(ra, rb)
	}

	// 超大文本回退为行级 Diff 的变更字符数估算
	ops := diffLines(splitLines(string(ra)), splitLines(string(rb)))
	delChars, insChars := 0, 0
	changed := 0
	flush := func() {
		changed += maxInt(delChars, insChars)
		delChars, insChars = 0, 0
	}
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			flush()
		case opDelete:
			delChars += len([]rune(op.line)) + 1
		case opInsert:
			insChars += len([]rune(op.line)) + 1
		}
	}
	flush()
	return changed
}

func levenshteinRunes(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = minInt3(del, ins, sub)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func clampRatio(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
