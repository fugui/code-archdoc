package drift

import (
	"strings"
	"testing"
)

func TestComputeSectionDiffsAndUnifiedDiff(t *testing.T) {
	from := []SectionContent{
		{SlotID: "overview", Title: "1. 概览", Markdown: "line 1\nline 2\nline 3"},
		{SlotID: "flows", Title: "2. 核心链路", Markdown: "step A\nstep B\nstep C"},
	}
	to := []SectionContent{
		{SlotID: "overview", Title: "1. 概览", Markdown: "line 1\nline 2\nline 3"},
		{SlotID: "flows", Title: "2. 核心链路", Markdown: "step A\nstep B modified\nstep C\nstep D"},
	}

	diffs := ComputeSectionDiffs(from, to)
	if len(diffs) != 2 {
		t.Fatalf("期望 2 个章节 Diff，实际 %d", len(diffs))
	}
	if diffs[0].Changed || diffs[0].UnifiedDiff != "" || diffs[0].AddedLines != 0 || diffs[0].RemovedLines != 0 {
		t.Fatalf("未变更章节不应产出 diff: %+v", diffs[0])
	}
	if !diffs[1].Changed {
		t.Fatalf("已变更章节应标记 Changed=true")
	}
	if diffs[1].AddedLines != 2 || diffs[1].RemovedLines != 1 {
		t.Fatalf("期望 AddedLines=2, RemovedLines=1，实际 Added=%d, Removed=%d\ndiff:\n%s",
			diffs[1].AddedLines, diffs[1].RemovedLines, diffs[1].UnifiedDiff)
	}
	if !strings.Contains(diffs[1].UnifiedDiff, "@@ ") ||
		!strings.Contains(diffs[1].UnifiedDiff, "-step B") ||
		!strings.Contains(diffs[1].UnifiedDiff, "+step B modified") ||
		!strings.Contains(diffs[1].UnifiedDiff, "+step D") {
		t.Fatalf("UnifiedDiff 格式不符合预期:\n%s", diffs[1].UnifiedDiff)
	}
}

func TestComputeEditDiffRatio(t *testing.T) {
	draft := []SectionContent{
		{SlotID: "s1", Markdown: "abcdefghij"},
		{SlotID: "s2", Markdown: "1234567890"},
	}
	approved := []SectionContent{
		{SlotID: "s1", Markdown: "abcdefghij"}, // 0% changed
		{SlotID: "s2", Markdown: "12345XXXXX"}, // 50% changed
	}

	overall, perSlot := ComputeEditDiffRatio(draft, approved)
	if perSlot["s1"] != 0 {
		t.Fatalf("s1 未修改期望 ratio=0，实际 %v", perSlot["s1"])
	}
	if perSlot["s2"] != 0.5 {
		t.Fatalf("s2 修改一半期望 ratio=0.5，实际 %v", perSlot["s2"])
	}
	if overall != 0.25 {
		t.Fatalf("整篇期望 overall=0.25，实际 %v", overall)
	}
}
