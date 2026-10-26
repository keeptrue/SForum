package aireply

import forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"

// mentionedUsernames 复用论坛层的提及解析：它已经正确处理了代码块与行内代码
// 不被当作提及，这里没有理由再实现一遍。
func mentionedUsernames(rawContent string) []string {
	return forum.MentionedUsernames(rawContent)
}
