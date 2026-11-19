package verifier

import (
	"fmt"
	"regexp"
	"strings"

	"code-archdoc/pkg/doctype"
)

// MermaidIssue 表示 Gate 3 检出的 Mermaid 语法、类型白名单或复杂度告警。
type MermaidIssue struct {
	SlotID      string `json:"slot_id"`
	BlockIndex  int    `json:"block_index"`
	DiagramType string `json:"diagram_type"`
	IssueCode   string `json:"issue_code"` // mermaid_syntax_error | mermaid_disallowed_type | mermaid_complexity_warning
	Severity    string `json:"severity"`   // error | warning
	NodeCount   int    `json:"node_count"`
	EdgeCount   int    `json:"edge_count"`
	Message     string `json:"message"`
}

var (
	reMermaidFence  = regexp.MustCompile("(?s)```mermaid[ \t]*\r?\n(.*?)```")
	reFlowEdge      = regexp.MustCompile(`(-->|---|-\.->|==>|-->\|[^|]*\||--[^-\n]+-->|<-->)`)
	reFlowEdgeSplit = regexp.MustCompile(`\s*(?:-->\|[^|]*\||--[^-\n]+-->|-->|---|-\.->|==>|<-->)\s*`)
	reSeqEdge       = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*(?:->>|-->>|->|-->|-x|--x|-\)|--\))\s*([A-Za-z0-9_.-]+)\s*:`)
	reSeqPart       = regexp.MustCompile(`^\s*(?:participant|actor)\s+([A-Za-z0-9_.-]+)`)
	reNodeIDPrefix  = regexp.MustCompile(`^([A-Za-z0-9_.-]+)`)
)

// RunMermaidGate 执行 Gate 3：Mermaid 图类型白名单、基础语法与节点/边数复杂度门禁。
func RunMermaidGate(slotID, markdown string, policy doctype.VerifyPolicy) []MermaidIssue {
	allowed := make(map[string]bool, len(policy.AllowedMermaidTypes))
	for _, t := range policy.AllowedMermaidTypes {
		allowed[strings.TrimSpace(t)] = true
	}
	maxNodes := policy.MaxMermaidNodes
	if maxNodes <= 0 {
		maxNodes = 35
	}
	maxEdges := policy.MaxMermaidEdges
	if maxEdges <= 0 {
		maxEdges = 60
	}

	matches := reMermaidFence.FindAllStringSubmatch(markdown, -1)
	if len(matches) == 0 {
		return nil
	}

	var issues []MermaidIssue
	for idx, m := range matches {
		body := m[1]
		blockIssues := validateMermaidBlock(slotID, idx, body, allowed, maxNodes, maxEdges)
		issues = append(issues, blockIssues...)
	}
	return issues
}

func validateMermaidBlock(slotID string, blockIdx int, body string, allowed map[string]bool, maxNodes, maxEdges int) []MermaidIssue {
	lines := strings.Split(body, "\n")
	diagramType := ""
	firstLineIdx := -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			diagramType = fields[0]
			firstLineIdx = i
			break
		}
	}

	if diagramType == "" {
		return []MermaidIssue{{
			SlotID:      slotID,
			BlockIndex:  blockIdx,
			DiagramType: "",
			IssueCode:   "mermaid_syntax_error",
			Severity:    "error",
			Message:     "Mermaid 代码块为空或缺少首行图类型声明",
		}}
	}

	if !allowed[diagramType] {
		return []MermaidIssue{{
			SlotID:      slotID,
			BlockIndex:  blockIdx,
			DiagramType: diagramType,
			IssueCode:   "mermaid_disallowed_type",
			Severity:    "error",
			Message:     fmt.Sprintf("不支持的 Mermaid 图类型 %q（不在白名单中）", diagramType),
		}}
	}

	// 语法校验：引号配对、括号配对（忽略引号内文字与箭头字符）、subgraph/块结构配对
	if synErr := checkMermaidSyntax(diagramType, lines[firstLineIdx+1:]); synErr != "" {
		return []MermaidIssue{{
			SlotID:      slotID,
			BlockIndex:  blockIdx,
			DiagramType: diagramType,
			IssueCode:   "mermaid_syntax_error",
			Severity:    "error",
			Message:     synErr,
		}}
	}

	nodes, edges := countMermaidGraphComplexity(diagramType, lines[firstLineIdx+1:])
	var issues []MermaidIssue
	if nodes > maxNodes || edges > maxEdges {
		issues = append(issues, MermaidIssue{
			SlotID:      slotID,
			BlockIndex:  blockIdx,
			DiagramType: diagramType,
			IssueCode:   "mermaid_complexity_warning",
			Severity:    "warning",
			NodeCount:   nodes,
			EdgeCount:   edges,
			Message:     fmt.Sprintf("Mermaid 图复杂度超限：节点 %d/%d，边 %d/%d，建议拆分为多张子图", nodes, maxNodes, edges, maxEdges),
		})
	}
	return issues
}

func checkMermaidSyntax(diagramType string, bodyLines []string) string {
	blockDepth := 0
	contentLines := 0

	for lineNo, raw := range bodyLines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		contentLines++

		// 检查双引号与括号配对
		if errMsg := checkLineDelimiters(line, lineNo+2); errMsg != "" {
			return errMsg
		}

		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "subgraph ") || lower == "subgraph":
			blockDepth++
		case diagramType == "sequenceDiagram" && (strings.HasPrefix(lower, "alt ") || strings.HasPrefix(lower, "opt ") ||
			strings.HasPrefix(lower, "loop ") || strings.HasPrefix(lower, "par ") || strings.HasPrefix(lower, "rect ") ||
			strings.HasPrefix(lower, "critical ")):
			blockDepth++
		case lower == "end":
			blockDepth--
			if blockDepth < 0 {
				return fmt.Sprintf("第 %d 行出现多余的 'end' 关键字", lineNo+2)
			}
		}
	}

	if contentLines == 0 {
		return "Mermaid 图表缺少任何节点或边定义"
	}
	if blockDepth != 0 {
		return fmt.Sprintf("Mermaid 块结构未闭合（缺少 %d 个 'end'）", blockDepth)
	}
	return ""
}

func checkLineDelimiters(line string, lineNo int) string {
	inDoubleQuote := false
	var stack []rune

	for i, ch := range line {
		if ch == '"' {
			inDoubleQuote = !inDoubleQuote
			continue
		}
		if inDoubleQuote {
			continue
		}
		// 忽略序列图异步箭头 -) / --)
		if ch == ')' && i > 0 && line[i-1] == '-' {
			continue
		}
		switch ch {
		case '(', '[', '{':
			stack = append(stack, ch)
		case ')', ']', '}':
			if len(stack) == 0 {
				return fmt.Sprintf("第 %d 行存在未匹配的闭合括号 %q", lineNo, ch)
			}
			top := stack[len(stack)-1]
			if !matchingPair(top, ch) {
				return fmt.Sprintf("第 %d 行括号不匹配：%q 与 %q", lineNo, top, ch)
			}
			stack = stack[:len(stack)-1]
		}
	}

	if inDoubleQuote {
		return fmt.Sprintf("第 %d 行存在未闭合的双引号", lineNo)
	}
	if len(stack) > 0 {
		return fmt.Sprintf("第 %d 行存在未闭合的括号 %q", lineNo, stack[len(stack)-1])
	}
	return ""
}

func matchingPair(open, close rune) bool {
	return (open == '(' && close == ')') ||
		(open == '[' && close == ']') ||
		(open == '{' && close == '}')
}

func countMermaidGraphComplexity(diagramType string, bodyLines []string) (int, int) {
	nodes := make(map[string]bool)
	edges := 0

	for _, raw := range bodyLines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "subgraph") || lower == "end" || strings.HasPrefix(lower, "classdef ") ||
			strings.HasPrefix(lower, "style ") || strings.HasPrefix(lower, "direction ") || strings.HasPrefix(lower, "note ") {
			continue
		}

		if diagramType == "sequenceDiagram" {
			if pm := reSeqPart.FindStringSubmatch(line); len(pm) == 2 {
				nodes[pm[1]] = true
				continue
			}
			if sm := reSeqEdge.FindStringSubmatch(line); len(sm) == 3 {
				nodes[sm[1]] = true
				nodes[sm[2]] = true
				edges++
			}
			continue
		}

		// flowchart / graph / stateDiagram / classDiagram / erDiagram
		edgeHits := reFlowEdge.FindAllString(line, -1)
		if len(edgeHits) > 0 {
			edges += len(edgeHits)
			parts := reFlowEdgeSplit.Split(line, -1)
			for _, p := range parts {
				if id := extractNodeID(p); id != "" {
					nodes[id] = true
				}
			}
		} else {
			if id := extractNodeID(line); id != "" {
				nodes[id] = true
			}
		}
	}
	return len(nodes), edges
}

func extractNodeID(expr string) string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" || strings.HasPrefix(trimmed, "[*]") {
		return ""
	}
	m := reNodeIDPrefix.FindStringSubmatch(trimmed)
	if len(m) < 2 {
		return ""
	}
	id := m[1]
	lower := strings.ToLower(id)
	if lower == "class" || lower == "state" || lower == "linkstyle" {
		return ""
	}
	return id
}
