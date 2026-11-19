package publisher

import (
	"regexp"
	"strings"
)

var (
	reCrossDocLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reSiblingDocPath = regexp.MustCompile(`^(?:\./)?([a-z0-9][a-z0-9_-]{1,62})\.md(?:#[^\s)]*)?$`)
)

// CheckAndDowngradeCrossDocLinks 校验同仓跨文档相对链接（如 ./ops-and-config.md）。
// 若目标 DocType 尚未发布，自动降级为纯文本引用并附注"（尚未发布）"，杜绝门户文档中心出现死链（F-11）。
func CheckAndDowngradeCrossDocLinks(markdown string, publishedSlugs map[string]bool) (string, int) {
	lines := strings.Split(markdown, "\n")
	inFence := false
	downgraded := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = reCrossDocLink.ReplaceAllStringFunc(line, func(full string) string {
			// 跳过图片语法 ![alt](url) 的误伤（前置字符判定在调用端不截断；此处用 submatch 校验目标即可）
			sub := reCrossDocLink.FindStringSubmatch(full)
			if len(sub) != 3 {
				return full
			}
			label := sub[1]
			target := strings.TrimSpace(sub[2])

			// 仅处理同仓相对文档链接（跳过外链、绝对路径、锚点、上级目录链接）
			if strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "/") ||
				strings.HasPrefix(target, "#") ||
				strings.HasPrefix(target, "../") {
				return full
			}

			m := reSiblingDocPath.FindStringSubmatch(target)
			if len(m) != 2 {
				return full
			}
			targetSlug := m[1]
			if strings.EqualFold(targetSlug, "readme") {
				return full
			}
			if publishedSlugs != nil && publishedSlugs[targetSlug] {
				return full
			}

			downgraded++
			return label + "（尚未发布）"
		})
	}

	return strings.Join(lines, "\n"), downgraded
}
