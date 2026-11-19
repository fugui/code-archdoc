package doctype

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// OrderedAssetFiles 定义 DocType 目录确定性有序聚合哈希的文件清单（03-doctype-drift-and-publishing.md §1.4）。
var OrderedAssetFiles = []string{
	"meta.json",
	"template.md",
	"generation_prompt.md",
	"refine_prompt.md",
}

// NormalizeCRLFToLF 统一将 Windows CRLF 转为 LF，保障跨平台哈希一致。
func NormalizeCRLFToLF(in []byte) []byte {
	return []byte(strings.ReplaceAll(string(in), "\r\n", "\n"))
}

// ComputeAggregateHash 按固定有序文件清单计算 DocType 目录的确定性聚合 SHA-256 哈希。
func ComputeAggregateHash(files map[string][]byte) string {
	h := sha256.New()
	for _, name := range OrderedAssetFiles {
		raw, exists := files[name]
		if !exists {
			continue
		}
		normalized := NormalizeCRLFToLF(raw)
		fileSum := sha256.Sum256(normalized)
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(hex.EncodeToString(fileSum[:])))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComputePromptHash 计算生成与精修指令内容的 SHA-256 哈希（用于 Spec Drift 快速比对）。
func ComputePromptHash(genPrompt, refinePrompt string) string {
	h := sha256.New()
	h.Write(NormalizeCRLFToLF([]byte(genPrompt)))
	h.Write([]byte{0})
	h.Write(NormalizeCRLFToLF([]byte(refinePrompt)))
	return hex.EncodeToString(h.Sum(nil))
}
