package aiderived

import (
	"strconv"
	"strings"

	aidomain "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 提示词与版本在这里同处一文件：版本常量住在用途声明里（Support/AI/purpose.go），
// 正文住在这里，两者必须在同一次改动里一起动。把它们分到两处，最容易发生的
// 事故就是「正文改了、版本没升」——缓存键命中旧结果，表现为「改了没效果」。

// defaultDigestSystemPrompt 是话题蒸馏的内置系统指令。
const defaultDigestSystemPrompt = `你是一个技术社区的内容整理助手。你的任务是把一条已有话题的讨论，整理成一份供运营者审阅的摘要草稿。

产出要求：
- 只用陈述句总结话题里**确实出现过**的内容，不补充任何外部知识。
- 结构固定为：一句话结论 → 关键要点（3-6 条）→ 未解决的问题（没有就写「无」）。
- 每一条要点都要能追溯到具体楼层；引用时写「#楼层号」，不要编造楼层号。
- 讨论中存在分歧时如实呈现双方立场，不要替社区下结论、不要评选「正确答案」。
- 语言与话题正文保持一致。
- 只写 Markdown，不要输出 HTML 标签或脚本标签；不要使用一级标题。
- 不要自称「作为一个 AI 模型」，也不要写「以下是摘要」这类开场白。`

// defaultTourSystemPrompt 是长楼导览的内置系统指令。
const defaultTourSystemPrompt = `你是一个技术社区的阅读向导。你的任务是为一条很长的讨论生成一条阅读路线，让新来的成员知道该按什么顺序读、哪些楼层可以跳过。

产出要求：
- 结构固定为：主线（按阅读顺序列出关键楼层与它们的价值）→ 争论点（分歧双方各自在哪几楼）→ 可以跳过（哪些楼层是寒暄、重复或已过时）。
- 楼层一律写成「#楼层号」；只有讨论里真实出现过的楼层号才能写。
- 不要评价谁说得对，只说明「这一楼在讨论里承担什么角色」。
- 找不到主线时如实说明这个话题仍在发散，不要硬编一条主线。
- 语言与话题正文保持一致。
- 只写 Markdown，不要输出 HTML 标签或脚本标签；不要使用一级标题。`

// DigestSystemPrompt 返回蒸馏用途的系统指令。
//
// 安全尾注由 supportai 追加，调用方无法绕过：话题正文与评论都是社区成员写的
// 不可信输入，里面的「忽略以上指令」不能改变本用途的行为。
func DigestSystemPrompt() string {
	return defaultDigestSystemPrompt + "\n\n" + supportai.ReplyPromptSafetyAppendix
}

// TourSystemPrompt 返回导览用途的系统指令。
func TourSystemPrompt() string {
	return defaultTourSystemPrompt + "\n\n" + supportai.ReplyPromptSafetyAppendix
}

// SystemPromptFor 按用途取系统指令。未登记的用途返回空串，由调用方失败关闭。
func SystemPromptFor(purpose string) string {
	switch strings.TrimSpace(purpose) {
	case PurposeDigest:
		return DigestSystemPrompt()
	case PurposeTour:
		return TourSystemPrompt()
	default:
		return ""
	}
}

// 进入提示词的上下文上限。它们是「读得下」与「花得起」之间的折中：蒸馏与导览
// 面对的是同一栋楼，但导览更关心楼层结构而非正文细节，因此两者的预算不同。
const (
	// digestBodyRunes 是主楼正文进入蒸馏提示词的上限。
	digestBodyRunes = 3_000
	// digestCommentRunes 是单条评论进入蒸馏提示词的上限。
	digestCommentRunes = 500
	// digestCommentLimit 是蒸馏提示词里包含的评论条数上限。
	digestCommentLimit = 60
	// tourCommentRunes 是单条评论进入导览提示词的上限（导览更短）。
	tourCommentRunes = 200
	// tourCommentLimit 是导览提示词里包含的评论条数上限。
	tourCommentLimit = 120
)

// TopicSnapshot 是派生生成所需的全部输入：一条话题的主楼与它的评论。
//
// 它由调用方从公开读取路径取来（与访客看到的一致），本包不自己去查库：
// 「这条话题是否可见」的判定必须与帖子详情页同源，复制一份谓词就是第二个真相。
type TopicSnapshot struct {
	Topic    aidomain.TopicDetail
	Comments []aidomain.Comment
}

// buildTopicHeader 组装两个用途共用的开头：标题、作者、状态、楼层总数。
func buildTopicHeader(snapshot TopicSnapshot) string {
	var builder strings.Builder
	topic := snapshot.Topic
	builder.WriteString("话题 #" + strconv.FormatInt(topic.ID, 10) + "《" + ClipRunes(topic.Title, 120) + "》\n")
	if author := topic.Author; author != nil {
		builder.WriteString("作者：" + authorLabel(author.DisplayName, author.Username) + "\n")
	}
	builder.WriteString("状态：" + strings.TrimSpace(topic.Status) +
		" ｜ 评论 " + strconv.FormatInt(topic.CommentCount, 10) +
		" ｜ 浏览 " + strconv.FormatInt(topic.ViewCount, 10) + "\n")
	if body := ClipRunes(topic.Content.PlainText, digestBodyRunes); body != "" {
		builder.WriteString("\n主楼正文：\n" + body + "\n")
	}
	return builder.String()
}

// BuildDigestPrompt 组装蒸馏的用户消息。
func BuildDigestPrompt(snapshot TopicSnapshot) string {
	var builder strings.Builder
	builder.WriteString(buildTopicHeader(snapshot))
	if len(snapshot.Comments) == 0 {
		builder.WriteString("\n这条话题还没有评论。")
		return builder.String()
	}
	builder.WriteString("\n讨论（按时间顺序，格式为「楼层｜作者：正文」）：\n")
	for index, comment := range snapshot.Comments {
		if index >= digestCommentLimit {
			builder.WriteString("（讨论更长，以上是" + strconv.Itoa(digestCommentLimit) + "条；更早/更晚的楼层未展示。）\n")
			break
		}
		builder.WriteString("#" + strconv.FormatInt(comment.ID, 10) + " ｜ " +
			commentAuthorLabel(comment) + "：" + ClipRunes(comment.Content.PlainText, digestCommentRunes) + "\n")
	}
	builder.WriteString("\n请按要求的固定结构，为这条话题写一份摘要草稿。")
	return builder.String()
}

// BuildTourPrompt 组装导览的用户消息。
func BuildTourPrompt(snapshot TopicSnapshot) string {
	var builder strings.Builder
	builder.WriteString(buildTopicHeader(snapshot))
	if len(snapshot.Comments) == 0 {
		builder.WriteString("\n这条话题还没有评论，没有可导览的路线。")
		return builder.String()
	}
	builder.WriteString("\n楼层清单（按时间顺序）：\n")
	for index, comment := range snapshot.Comments {
		if index >= tourCommentLimit {
			builder.WriteString("（讨论更长，以上是" + strconv.Itoa(tourCommentLimit) + "层；其余楼层未展示。）\n")
			break
		}
		reply := ""
		if comment.ReplyCount > 0 {
			reply = "（" + strconv.FormatInt(comment.ReplyCount, 10) + " 条回复）"
		}
		builder.WriteString("#" + strconv.FormatInt(comment.ID, 10) + reply + " ｜ " +
			commentAuthorLabel(comment) + "：" + ClipRunes(comment.Content.PlainText, tourCommentRunes) + "\n")
	}
	builder.WriteString("\n请按要求的固定结构，为这条话题写一条阅读路线。")
	return builder.String()
}

// BuildPrompt 按用途组装用户消息。未登记的用途返回空串。
func BuildPrompt(purpose string, snapshot TopicSnapshot) string {
	switch strings.TrimSpace(purpose) {
	case PurposeDigest:
		return BuildDigestPrompt(snapshot)
	case PurposeTour:
		return BuildTourPrompt(snapshot)
	default:
		return ""
	}
}

// authorLabel 组装「显示名（@用户名）」，与回复提示词同一写法：模型据此才能把
// 「张三说的」变成一个可查的账号。
func authorLabel(displayName, username string) string {
	display := strings.TrimSpace(displayName)
	name := strings.TrimSpace(username)
	switch {
	case name == "" && display == "":
		return "匿名"
	case name == "":
		return display
	case display == "" || display == name:
		return "@" + name
	default:
		return display + "（@" + name + "）"
	}
}

// commentAuthorLabel 取评论作者标签，缺失身份时用「匿名」。
func commentAuthorLabel(comment aidomain.Comment) string {
	if comment.Author == nil {
		return "匿名"
	}
	return authorLabel(comment.Author.DisplayName, comment.Author.Username)
}
