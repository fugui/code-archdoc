package treesitter

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// ComputeNodeFingerprint 对 CST 节点主体执行词法归一化哈希：
// 跳过所有注释节点（comment）与空白/行号差异，仅对有效叶子 token 序列计算 sha256 前 16 位。
// 保障在函数上方或内部插入空行/注释时指纹不变，修改任意逻辑 token 时指纹必变（ADR-03 §2.1）。
func ComputeNodeFingerprint(node *tree_sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	var tokens []string
	collectNormalizedTokens(node, src, &tokens)
	joined := strings.Join(tokens, " ")
	sum := sha256.Sum256([]byte(joined))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func collectNormalizedTokens(node *tree_sitter.Node, src []byte, out *[]string) {
	if node == nil {
		return
	}
	kind := node.Kind()
	if kind == "comment" {
		return
	}
	childCount := node.ChildCount()
	if childCount == 0 {
		tok := strings.TrimSpace(node.Utf8Text(src))
		if tok != "" {
			*out = append(*out, tok)
		}
		return
	}
	for i := uint(0); i < childCount; i++ {
		collectNormalizedTokens(node.Child(i), src, out)
	}
}
