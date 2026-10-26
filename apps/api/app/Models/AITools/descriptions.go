package aitools

import supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"

// 本文件为九个 Core 内置工具补上「仅控制台展示」的英文说明。
//
// 这些文案不参与任何一次模型调用：模型读的始终是 Definition().Description（会被
// RenderToolSection 渲染进 system），而 DisplayDescriptions 只被控制台的工具枚举
// 读取。两者刻意分开，翻译文案因此不会悄悄改变提示词、缓存键与成本口径。
//
// 只声明 en-US：站点默认语言是中文，而 Definition().Description 本身就是中文，
// 运营者在控制台看到的「英文说明」正好补上这份缺口；没有 zh-CN 条目不是遗漏，
// 而是不重复一份已经存在的中文文案。

const (
	// 文案是契约的一部分：控制台、插件清单示例与文档都引用同一份措辞，改动时
	// 应视为可见性说明的变更，而不是随手润色。
	displayENSearch         = "Search site topics and return titles, excerpts, and links. Use for site facts, earlier discussions, or a citable link."
	displayENTopicRead      = "Read a topic's first post and its comments (paginated). Use to understand a post, follow older discussion, or verify details."
	displayENTopicList      = "List public topics by order (latest / active / popular), optionally filtered by category or tag."
	displayENCommentThread  = "Read the replies under one comment (direct replies, optionally one level deeper). Use to answer whether a follow-up was answered."
	displayENAttachmentRead = "Read a site attachment. Text-like files return truncated content; binaries return metadata only."
	displayENSelfHistory    = "Read the replies you already posted in the current topic (newest first). Use to avoid repeating or contradicting yourself."
	displayENUserProfile    = "Read a member's public profile: join date, topic and comment counts, recent topics."
	displayENUserSearch     = "Find site members by nickname or username; returns the @handle and a profile link."
	displayENTime           = "Return the server's current time (including UTC). Use for relative times such as today, recently, or how long ago."
)

// 编译期断言：九个内置工具都必须满足控制台的展示文案契约。少实现一个不会让
// 编译失败（该接口是可选的），所以在这里把它变成编译期事实，测试再去断言内容。
var (
	_ supportai.ToolDisplayDescriptionProvider = (*SearchTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*TopicReadTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*TopicListTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*CommentThreadTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*AttachmentReadTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*SelfHistoryTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*UserProfileTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*UserSearchTool)(nil)
	_ supportai.ToolDisplayDescriptionProvider = (*TimeTool)(nil)
)

// displayDescriptions 每次返回一份新 map：调用方（控制台枚举、测试）改到它不会
// 污染下一次读取。
func displayDescriptions(english string) map[string]string {
	return map[string]string{"en-US": english}
}

func (t *SearchTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENSearch)
}

func (t *TopicReadTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENTopicRead)
}

func (t *TopicListTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENTopicList)
}

func (t *CommentThreadTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENCommentThread)
}

func (t *AttachmentReadTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENAttachmentRead)
}

func (t *SelfHistoryTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENSelfHistory)
}

func (t *UserProfileTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENUserProfile)
}

func (t *UserSearchTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENUserSearch)
}

func (t *TimeTool) DisplayDescriptions() map[string]string {
	return displayDescriptions(displayENTime)
}
