package aitools_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	aitools "github.com/keeptrue/sforum/apps/api/app/Models/AITools"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// B2 的三只新工具共用一套 fake：它们都以「提问者身份」读公开内容，因此 fake 必须
// 能看到 viewer 与 publicID 真的被传进去了——那是「可见性用 ViewerUserID」这条
// 硬约束唯一可断言的证据。

type fakeThreadReplies struct {
	byComment map[int64][]forum.Comment
	err       error
	viewer    identity.Actor
	calls     []int64
}

func (f *fakeThreadReplies) ListCommentRepliesForViewer(_ context.Context, commentID int64, viewer identity.Actor) ([]forum.Comment, error) {
	f.viewer = viewer
	f.calls = append(f.calls, commentID)
	if f.err != nil {
		return nil, f.err
	}
	return f.byComment[commentID], nil
}

type fakeActors struct {
	actor identity.Actor
	err   error
	asked int64
}

func (f *fakeActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	f.asked = userID
	if f.err != nil {
		return identity.Actor{}, f.err
	}
	return f.actor, nil
}

type trackedReadCloser struct {
	io.Reader
	closed *bool
}

func (t trackedReadCloser) Close() error {
	*t.closed = true
	return nil
}

type fakeAttachmentReader struct {
	attachment attachments.Attachment
	body       string
	err        error
	actor      identity.Actor
	publicID   string
	closed     bool
}

func (f *fakeAttachmentReader) OpenContent(_ context.Context, actor identity.Actor, publicID string) (attachments.Attachment, io.ReadCloser, error) {
	f.actor = actor
	f.publicID = publicID
	if f.err != nil {
		return attachments.Attachment{}, nil, f.err
	}
	return f.attachment, trackedReadCloser{Reader: strings.NewReader(f.body), closed: &f.closed}, nil
}

type fakeBotHistory struct {
	items      []forum.Comment
	err        error
	topicID    int64
	botUserID  int64
	askedLimit int
}

func (f *fakeBotHistory) ListBotComments(_ context.Context, topicID, botUserID int64, limit int) ([]forum.Comment, error) {
	f.topicID, f.botUserID, f.askedLimit = topicID, botUserID, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func comment(id, topicID, authorID int64, author, body string, createdAt time.Time) forum.Comment {
	item := forum.Comment{
		ID:           id,
		TopicID:      topicID,
		AuthorUserID: authorID,
		Status:       forum.CommentStatusActive,
		Content:      forum.PublicRenderedContent{PlainText: body},
		CreatedAt:    createdAt,
	}
	if author != "" {
		item.Author = &forum.UserSummary{ID: authorID, Username: author, DisplayName: author}
	}
	return item
}

func TestCommentThreadToolReturnsNestedRepliesWithLinks(t *testing.T) {
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	direct := comment(914, 42, 5, "bob", "我先复现一下。", base)
	direct.ReplyCount = 1
	replies := &fakeThreadReplies{byComment: map[int64][]forum.Comment{
		913: {direct},
		914: {comment(915, 42, 6, "carol", "复现步骤在附件里。", base.Add(time.Hour))},
	}}
	actors := &fakeActors{actor: identity.Actor{ID: 11, Status: identity.UserStatusActive}}
	tool := aitools.NewCommentThreadTool(replies, actors, testLinks())

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Name:      "forum-comment-thread",
		Arguments: `{"commentId":913,"depth":2,"limit":10}`,
	}, supportai.ToolContext{ViewerUserID: 11, TopicID: 42})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "我先复现一下") ||
		!strings.Contains(result.Content, "复现步骤在附件里") ||
		!strings.Contains(result.Content, "https://forum.test/t/42#comment-914") ||
		!strings.Contains(result.Content, "https://forum.test/t/42#comment-915") {
		t.Fatalf("content = %s", result.Content)
	}
	if !strings.Contains(result.Content, "↳") {
		t.Fatalf("second level must be indented: %s", result.Content)
	}
	// 读取必须发生在提问者身份上（硬约束 4），不是匿名也不是机器人。
	if actors.asked != 11 || replies.viewer.ID != 11 {
		t.Fatalf("viewer = %+v (asked %d)", replies.viewer, actors.asked)
	}
	if len(replies.calls) != 2 || replies.calls[0] != 913 || replies.calls[1] != 914 {
		t.Fatalf("calls = %v", replies.calls)
	}
}

func TestCommentThreadToolHidesInvisibleComment(t *testing.T) {
	replies := &fakeThreadReplies{err: forum.ErrCommentNotFound}
	tool := aitools.NewCommentThreadTool(replies, &fakeActors{}, testLinks())

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"commentId":913}`,
	}, supportai.ToolContext{ViewerUserID: 11})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不存在") {
		t.Fatalf("result = %+v", result)
	}
	// 「不存在」必须是全部答案：任何作者、链接、楼层号都会把 commentId 变成探测面。
	if strings.Contains(result.Content, "https://") ||
		strings.Contains(result.Content, "bob") ||
		strings.ContainsAny(result.Content, "0123456789") {
		t.Fatalf("denial leaked resource metadata: %s", result.Content)
	}
}

func TestCommentThreadToolRejectsInvalidArguments(t *testing.T) {
	tool := aitools.NewCommentThreadTool(&fakeThreadReplies{}, &fakeActors{}, testLinks())
	cases := []struct {
		arguments string
		want      string
	}{
		{`{"commentId":0}`, "commentId"},
		{`{"commentId":-3}`, "commentId"},
		// 解析失败走 decodeArguments 的既有文案：说清「要 JSON 对象」，不让回复失败。
		{`not json`, "参数"},
	}
	for _, testCase := range cases {
		result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: testCase.arguments}, supportai.ToolContext{})
		if err != nil {
			t.Fatalf("%s must not fail the reply: %v", testCase.arguments, err)
		}
		if !result.IsError || !strings.Contains(result.Content, testCase.want) {
			t.Fatalf("%s result = %+v", testCase.arguments, result)
		}
	}
}

func TestCommentThreadToolClipsLongReplies(t *testing.T) {
	replies := &fakeThreadReplies{byComment: map[int64][]forum.Comment{
		913: {comment(914, 42, 5, "bob", strings.Repeat("长", 10_000), time.Now().UTC())},
	}}
	tool := aitools.NewCommentThreadTool(replies, &fakeActors{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"commentId":913}`,
	}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "…") {
		t.Fatal("long reply must be clipped with a marker")
	}
	if !utf8.ValidString(result.Content) {
		t.Fatal("clipped content must stay valid UTF-8")
	}
	if len([]rune(result.Content)) > 2_000 {
		t.Fatalf("content too long: %d runes", len([]rune(result.Content)))
	}
}

func TestAttachmentReadToolReturnsTextBody(t *testing.T) {
	reader := &fakeAttachmentReader{
		attachment: attachments.Attachment{
			PublicID: "att_public_1", OriginalName: "postgres.conf",
			ContentType: "text/plain", SizeBytes: 128, Extension: "conf",
		},
		body: "max_connections = 200\nshared_buffers = 4GB\n",
	}
	actors := &fakeActors{actor: identity.Actor{ID: 11, Status: identity.UserStatusActive}}
	tool := aitools.NewAttachmentReadTool(reader, actors)

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"attachmentId":"att_public_1"}`,
	}, supportai.ToolContext{ViewerUserID: 11})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "postgres.conf") ||
		!strings.Contains(result.Content, "max_connections = 200") ||
		!strings.Contains(result.Content, "128 字节") {
		t.Fatalf("content = %s", result.Content)
	}
	// 附件以提问者身份打开（与公开下载同一条授权链），publicId 原样透传。
	if reader.actor.ID != 11 || reader.publicID != "att_public_1" {
		t.Fatalf("read actor = %+v publicID = %q", reader.actor, reader.publicID)
	}
	if !reader.closed {
		t.Fatal("reader must be closed")
	}
}

func TestAttachmentReadToolReturnsMetadataOnlyForBinary(t *testing.T) {
	reader := &fakeAttachmentReader{
		attachment: attachments.Attachment{
			PublicID: "att_img", OriginalName: "screenshot.png",
			ContentType: "image/png", SizeBytes: 2048, Extension: "png",
		},
		body: "PNG-BYTES-SHOULD-NEVER-APPEAR",
	}
	tool := aitools.NewAttachmentReadTool(reader, &fakeActors{})
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"attachmentId":"att_img"}`,
	}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "screenshot.png") || !strings.Contains(result.Content, "二进制") {
		t.Fatalf("content = %s", result.Content)
	}
	if strings.Contains(result.Content, "PNG-BYTES") {
		t.Fatal("binary attachments must not return bytes")
	}
}

func TestAttachmentReadToolHidesInvisibleAttachment(t *testing.T) {
	reader := &fakeAttachmentReader{err: identity.ErrPermissionDenied}
	tool := aitools.NewAttachmentReadTool(reader, &fakeActors{})

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"attachmentId":"att_secret"}`,
	}, supportai.ToolContext{ViewerUserID: 11})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不存在") {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Content, "att_secret") ||
		strings.Contains(result.Content, "text/plain") ||
		strings.ContainsAny(result.Content, "0123456789") {
		t.Fatalf("denial leaked resource metadata: %s", result.Content)
	}
}

func TestAttachmentReadToolRejectsInvalidArguments(t *testing.T) {
	tool := aitools.NewAttachmentReadTool(&fakeAttachmentReader{}, &fakeActors{})
	cases := []struct {
		arguments string
		want      string
	}{
		{`{"attachmentId":"   "}`, "attachmentId"},
		// 传数字而不是 publicId 字符串是最容易犯的错：这是类型错误，不是「找不到」。
		{`{"attachmentId":42}`, "参数"},
		{`nope`, "参数"},
	}
	for _, testCase := range cases {
		result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: testCase.arguments}, supportai.ToolContext{})
		if err != nil {
			t.Fatalf("%s must not fail the reply: %v", testCase.arguments, err)
		}
		if !result.IsError || !strings.Contains(result.Content, testCase.want) {
			t.Fatalf("%s result = %+v", testCase.arguments, result)
		}
	}
}

func TestAttachmentReadToolTruncatesAndStaysValidUTF8(t *testing.T) {
	body := strings.Repeat("日志内容很长的一行\n", 3_000) // ≈78 KiB，远超 16 KiB 读取上限
	reader := &fakeAttachmentReader{
		attachment: attachments.Attachment{
			PublicID: "att_log", OriginalName: "app.log",
			ContentType: "text/plain", SizeBytes: 10, Extension: "log",
		},
		body: body,
	}
	tool := aitools.NewAttachmentReadTool(reader, &fakeActors{})
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"attachmentId":"att_log"}`,
	}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "…") || !strings.Contains(result.Content, "上限") {
		t.Fatalf("truncation must be visible: %s", result.Content[:200])
	}
	if !utf8.ValidString(result.Content) {
		t.Fatal("truncated content must stay valid UTF-8")
	}
	// 读取边界由 io.LimitReader 强制，与 size_bytes（这里故意写成 10）无关。
	if len([]rune(result.Content)) > 4_000 {
		t.Fatalf("content too long: %d runes", len([]rune(result.Content)))
	}
}

func TestSelfHistoryToolListsBotCommentsNewestFirst(t *testing.T) {
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	history := &fakeBotHistory{items: []forum.Comment{
		comment(915, 42, 77, "assistant", "第一次回答", base),
		comment(920, 42, 77, "assistant", "第二次回答", base.Add(time.Hour)),
	}}
	tool := aitools.NewSelfHistoryTool(history, testLinks())

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{
		Arguments: `{"topicId":42,"limit":5}`,
	}, supportai.ToolContext{ViewerUserID: 11, BotUserID: 77, TopicID: 42})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "第一次回答") || !strings.Contains(result.Content, "第二次回答") {
		t.Fatalf("content = %s", result.Content)
	}
	// 新的在前：模型要判断「我是不是已经答过」，看的是最近那几条。
	if strings.Index(result.Content, "第二次回答") > strings.Index(result.Content, "第一次回答") {
		t.Fatalf("comments must be newest first: %s", result.Content)
	}
	if !strings.Contains(result.Content, "https://forum.test/t/42#comment-920") {
		t.Fatalf("link missing: %s", result.Content)
	}
	if history.topicID != 42 || history.botUserID != 77 || history.askedLimit != 5 {
		t.Fatalf("history input = %+v", history)
	}
}

func TestSelfHistoryToolHidesInvisibleTopic(t *testing.T) {
	history := &fakeBotHistory{err: forum.ErrTopicNotFound}
	tool := aitools.NewSelfHistoryTool(history, testLinks())

	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{}`},
		supportai.ToolContext{ViewerUserID: 11, BotUserID: 77, TopicID: 42})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不存在") {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Content, "https://") ||
		strings.Contains(result.Content, "assistant") ||
		strings.ContainsAny(result.Content, "0123456789") {
		t.Fatalf("denial leaked resource metadata: %s", result.Content)
	}
}

func TestSelfHistoryToolRejectsMissingTopicAndBotIdentity(t *testing.T) {
	history := &fakeBotHistory{}
	tool := aitools.NewSelfHistoryTool(history, testLinks())

	// 当前不在话题里，又没有显式 topicId：拒绝，且不触达存储。
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{}`},
		supportai.ToolContext{ViewerUserID: 11, BotUserID: 77})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "topicId") {
		t.Fatalf("result = %+v", result)
	}
	if history.topicID != 0 {
		t.Fatalf("an unresolvable topic must not reach the store: %+v", history)
	}

	// 没有机器人身份时不猜「我是谁」，也不退回「任意作者」。
	result, err = tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":42}`},
		supportai.ToolContext{ViewerUserID: 11, TopicID: 42})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "机器人身份") {
		t.Fatalf("result = %+v", result)
	}

	// limit 超出上限按上限收敛，而不是原样下发。
	result, err = tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":42,"limit":999}`},
		supportai.ToolContext{ViewerUserID: 11, BotUserID: 77})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError || history.askedLimit != 10 {
		t.Fatalf("limit must clamp to 10: %+v (asked %d)", result, history.askedLimit)
	}
}

func TestSelfHistoryToolClipsLongComments(t *testing.T) {
	history := &fakeBotHistory{items: []forum.Comment{
		comment(915, 42, 77, "assistant", strings.Repeat("答", 5_000), time.Now().UTC()),
	}}
	tool := aitools.NewSelfHistoryTool(history, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":42}`},
		supportai.ToolContext{ViewerUserID: 11, BotUserID: 77})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "…") {
		t.Fatal("long history entry must be clipped")
	}
	if !utf8.ValidString(result.Content) {
		t.Fatal("clipped content must stay valid UTF-8")
	}
	if len([]rune(result.Content)) > 2_000 {
		t.Fatalf("content too long: %d runes", len([]rune(result.Content)))
	}
}

// 适配器只能读到「机器人自己的发言」，并且必须从末页回溯而不是拉全表。
func TestBotHistoryReaderWalksFromLastPage(t *testing.T) {
	comments := &pagedCommentReader{
		total: 120, // 每页 50 → 末页是第 3 页
		pages: map[int][]forum.Comment{
			1: {comment(1, 42, 11, "alice", "人类的早期发言", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))},
			3: {
				comment(101, 42, 11, "alice", "人类发言", time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)),
				comment(102, 42, 77, "assistant", "机器人发言 A", time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)),
				comment(103, 42, 77, "assistant", "机器人发言 B", time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC)),
			},
		},
	}
	reader := aitools.NewBotHistoryReader(comments)
	if reader == nil {
		t.Fatal("adapter must exist when a comment reader is provided")
	}
	items, err := reader.ListBotComments(context.Background(), 42, 77, 5)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(items) != 2 || items[0].ID != 102 || items[1].ID != 103 {
		t.Fatalf("items = %+v", items)
	}
	// 第 1 页只用来取 Total，随后必须直接跳到末页；不得顺序扫描。
	if len(comments.requested) < 2 || comments.requested[0] != 1 || comments.requested[1] != 3 {
		t.Fatalf("requested pages = %v", comments.requested)
	}
	if comments.lastInput.View != "flat" {
		t.Fatalf("flat view required for created_at ordering: %+v", comments.lastInput)
	}
	// nil 读取器必须返回接口 nil，否则登记表会注册一个必然失败的工具。
	if aitools.NewBotHistoryReader(nil) != nil {
		t.Fatal("nil comment reader must produce a nil BotHistoryReader")
	}
}

type pagedCommentReader struct {
	total     int64
	pages     map[int][]forum.Comment
	requested []int
	lastInput forum.CommentListInput
}

func (p *pagedCommentReader) ListComments(_ context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	p.lastInput = input
	p.requested = append(p.requested, input.Page)
	return forum.CommentList{
		Items:   p.pages[input.Page],
		Total:   p.total,
		Page:    input.Page,
		PerPage: input.PerPage,
		View:    input.View,
	}, nil
}

// 三只新工具在依赖齐全时全部登记，缺依赖时一个都不登记（易错点 3）。
func TestBuiltinRegistryRegistersCapabilityTools(t *testing.T) {
	full, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Search:      &fakeSearcher{},
		Topics:      &fakeTopics{},
		Comments:    &fakeComments{},
		Lister:      &fakeLister{},
		Profiles:    &fakeProfiles{},
		Users:       &fakeProfiles{},
		Replies:     &fakeThreadReplies{},
		Attachments: &fakeAttachmentReader{},
		BotHistory:  &fakeBotHistory{},
		Actors:      &fakeActors{},
		SiteURL:     testLinks().SiteURL,
	})
	if err != nil {
		t.Fatalf("registry failed: %v", err)
	}
	allowlist := append([]string(nil), supportai.CoreReplyToolAllowlist...)
	descriptors := full.Descriptors(allowlist)
	if len(descriptors) != len(allowlist) {
		t.Fatalf("all allowlisted tools must be registered: got %d of %d", len(descriptors), len(allowlist))
	}
	names := make(map[string]bool, len(descriptors))
	for _, descriptor := range descriptors {
		names[descriptor.Name] = true
		if len(descriptor.Parameters) == 0 {
			t.Fatalf("%s has no parameter schema", descriptor.Name)
		}
	}
	for _, name := range []string{"forum-comment-thread", "forum-attachment-read", "ai-self-history"} {
		if !names[name] {
			t.Fatalf("%s must be registered when its dependency is present", name)
		}
	}

	partial, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Comments: &fakeComments{}, Replies: &fakeThreadReplies{},
	})
	if err != nil {
		t.Fatalf("registry failed: %v", err)
	}
	for _, descriptor := range partial.Descriptors(allowlist) {
		if descriptor.Name == "forum-attachment-read" || descriptor.Name == "ai-self-history" {
			t.Fatalf("%s must not be registered without its dependency", descriptor.Name)
		}
	}
}

// 工具名必须落在用途白名单里，否则模型永远看不到它——登记表存在不等于暴露。
func TestCapabilityToolsAreAllowlisted(t *testing.T) {
	allowed := make(map[string]bool, len(supportai.CoreReplyToolAllowlist))
	for _, name := range supportai.CoreReplyToolAllowlist {
		allowed[name] = true
	}
	for _, name := range []string{"forum-comment-thread", "forum-attachment-read", "ai-self-history"} {
		if !allowed[name] {
			t.Fatalf("%s missing from CoreReplyToolAllowlist: %s", name, fmt.Sprint(supportai.CoreReplyToolAllowlist))
		}
	}
}
