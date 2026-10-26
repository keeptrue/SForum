package aitools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aitools "github.com/keeptrue/sforum/apps/api/app/Models/AITools"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

type fakeSearcher struct {
	result search.SearchResult
	err    error
	last   search.SearchInput
}

func (f *fakeSearcher) Search(_ context.Context, input search.SearchInput) (search.SearchResult, error) {
	f.last = input
	return f.result, f.err
}

type fakeTopics struct {
	detail forum.TopicDetail
	err    error
	lastID int64
}

func (f *fakeTopics) GetTopic(_ context.Context, topicID int64) (forum.TopicDetail, error) {
	f.lastID = topicID
	return f.detail, f.err
}

type fakeComments struct {
	list forum.CommentList
	err  error
	last forum.CommentListInput
}

func (f *fakeComments) ListComments(_ context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	f.last = input
	return f.list, f.err
}

type fakeLister struct {
	list forum.TopicList
	err  error
	last forum.TopicListInput
}

func (f *fakeLister) ListTopics(_ context.Context, input forum.TopicListInput) (forum.TopicList, error) {
	f.last = input
	return f.list, f.err
}

type fakeProfiles struct {
	profile  profile.PublicProfile
	err      error
	lastName string
	hits     []profile.UserSearchHit
	last     profile.SearchUsersInput
}

func (f *fakeProfiles) GetPublicProfile(_ context.Context, username string) (profile.PublicProfile, error) {
	f.lastName = username
	return f.profile, f.err
}

func (f *fakeProfiles) SearchPublicUsers(_ context.Context, input profile.SearchUsersInput) (profile.UserSearchResult, error) {
	f.last = input
	if f.err != nil {
		return profile.UserSearchResult{}, f.err
	}
	return profile.UserSearchResult{Items: f.hits}, nil
}

func testLinks() aitools.SiteLinks {
	return aitools.SiteLinks{SiteURL: func(context.Context) string { return "https://forum.test/" }}
}

func TestBuiltinRegistryRegistersAvailableTools(t *testing.T) {
	registry, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Search:   &fakeSearcher{},
		Topics:   &fakeTopics{},
		Comments: &fakeComments{},
		Lister:   &fakeLister{},
		Profiles: &fakeProfiles{},
		Users:    &fakeProfiles{},
		SiteURL:  testLinks().SiteURL,
	})
	if err != nil {
		t.Fatalf("registry failed: %v", err)
	}
	allowlist := []string{
		"forum-search", "forum-topic-read", "forum-topic-list", "user-profile-read", "user-search", "time-now",
	}
	descriptors := registry.Descriptors(allowlist)
	if len(descriptors) != 6 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	for _, descriptor := range descriptors {
		if len(descriptor.Parameters) == 0 {
			t.Fatalf("%s has no parameter schema", descriptor.Name)
		}
	}
	// 缺少依赖的工具不注册：登记表只声明「这个部署真的有什么」。
	empty, err := aitools.NewBuiltinRegistry(aitools.Deps{})
	if err != nil {
		t.Fatalf("registry failed: %v", err)
	}
	if got := empty.Descriptors(allowlist); len(got) != 1 || got[0].Name != "time-now" {
		t.Fatalf("empty registry descriptors = %+v", got)
	}
}

func TestSearchToolFormatsHitsWithLinks(t *testing.T) {
	searcher := &fakeSearcher{result: search.SearchResult{
		Total: 2,
		Items: []search.TopicSearchDoc{
			{ID: 42, Title: "Go 并发模式", CategorySlug: "backend", CommentCount: 12, ViewCount: 300, Excerpt: "channel 的常见用法…"},
			{ID: 43, Title: "另一个主题", CategorySlug: "backend"},
		},
	}}
	tool := aitools.NewSearchTool(searcher, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Name: "forum-search", Arguments: `{"query":"  并发  "}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "《Go 并发模式》") ||
		!strings.Contains(result.Content, "https://forum.test/t/42") ||
		!strings.Contains(result.Content, "channel 的常见用法") {
		t.Fatalf("content = %s", result.Content)
	}
	if searcher.last.Query != "并发" || searcher.last.PerPage != 8 || searcher.last.Page != 1 {
		t.Fatalf("search input = %+v", searcher.last)
	}
}

func TestSearchToolRejectsEmptyQuery(t *testing.T) {
	tool := aitools.NewSearchTool(&fakeSearcher{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"   "}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "query") {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchToolReportsUnavailableEngineAsModelFacingError(t *testing.T) {
	tool := aitools.NewSearchTool(&fakeSearcher{err: search.ErrEngineUnavailable}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"x"}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("engine unavailability must not fail the reply: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不可用") {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchToolPropagatesInternalFailures(t *testing.T) {
	tool := aitools.NewSearchTool(&fakeSearcher{err: errors.New("db down")}, testLinks())
	if _, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"x"}`}, supportai.ToolContext{}); err == nil {
		t.Fatal("internal failure must propagate so the orchestrator can log it")
	}
}

func TestSearchToolRejectsMalformedArguments(t *testing.T) {
	tool := aitools.NewSearchTool(&fakeSearcher{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `not json`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v", result)
	}
}

func TestTopicReadToolFormatsTopicAndComments(t *testing.T) {
	topics := &fakeTopics{detail: forum.TopicDetail{
		TopicSummary: forum.TopicSummary{
			ID: 42, Title: "如何配置连接池", CategorySlug: "backend", Status: "active",
			CommentCount: 3, ViewCount: 120,
			Author: &forum.UserSummary{Username: "alice", DisplayName: "Alice"},
		},
		Content: forum.PublicRenderedContent{PlainText: "我们线上用的是 PgBouncer。"},
	}}
	comments := &fakeComments{list: forum.CommentList{
		Total: 3, Page: 1, PerPage: 20, HasMore: true,
		Items: []forum.Comment{
			{ID: 501, Author: &forum.UserSummary{Username: "bob"}, Content: forum.PublicRenderedContent{PlainText: "池子大小要看并发。"}},
			{ID: 502, Author: &forum.UserSummary{Username: "carol"}, Content: forum.PublicRenderedContent{PlainText: "我们用的是 20。"}},
		},
	}}
	tool := aitools.NewTopicReadTool(topics, comments, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":42}`}, supportai.ToolContext{TopicID: 42})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "《如何配置连接池》") ||
		!strings.Contains(result.Content, "PgBouncer") ||
		!strings.Contains(result.Content, "bob") ||
		!strings.Contains(result.Content, "https://forum.test/t/42") ||
		!strings.Contains(result.Content, "还有更多") {
		t.Fatalf("content = %s", result.Content)
	}
	if comments.last.View != "flat" || comments.last.Page != 1 || comments.last.PerPage != 20 {
		t.Fatalf("comment input = %+v", comments.last)
	}
}

func TestTopicReadToolReportsMissingTopic(t *testing.T) {
	tool := aitools.NewTopicReadTool(&fakeTopics{err: forum.ErrTopicNotFound}, &fakeComments{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":999}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "不存在") {
		t.Fatalf("result = %+v", result)
	}
}

func TestTopicReadToolTruncatesLongBody(t *testing.T) {
	topics := &fakeTopics{detail: forum.TopicDetail{
		TopicSummary: forum.TopicSummary{ID: 7, Title: "长帖", Status: "active"},
		Content:      forum.PublicRenderedContent{PlainText: strings.Repeat("长", 10_000)},
	}}
	tool := aitools.NewTopicReadTool(topics, &fakeComments{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"topicId":7}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "…") {
		t.Fatal("long body must be truncated with a marker")
	}
	if len([]rune(result.Content)) > 5_000 {
		t.Fatalf("content too long: %d runes", len([]rune(result.Content)))
	}
}

func TestTopicListToolFormatsAndCapsPageSize(t *testing.T) {
	lister := &fakeLister{list: forum.TopicList{
		Total: 12,
		Items: []forum.TopicSummary{
			{ID: 1, Title: "置顶公告", CategorySlug: "news", IsPinned: true, CommentCount: 4, LastActivityAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)},
			{ID: 2, Title: "普通主题", CategorySlug: "news", Status: "locked"},
		},
	}}
	tool := aitools.NewTopicListTool(lister, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"sort":"hot","perPage":999}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "置顶") || !strings.Contains(result.Content, "已锁定") ||
		!strings.Contains(result.Content, "https://forum.test/t/2") || !strings.Contains(result.Content, "热门") {
		t.Fatalf("content = %s", result.Content)
	}
	if lister.last.PerPage != 20 || lister.last.Sort != "hot" {
		t.Fatalf("list input = %+v", lister.last)
	}
}

func TestUserProfileToolFormatsAndStripsAtSign(t *testing.T) {
	profiles := &fakeProfiles{profile: profile.PublicProfile{
		Username: "alice", DisplayName: "Alice", TopicCount: 5, CommentCount: 30,
		JoinedAt: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		RecentTopics: []forum.TopicSummary{
			{ID: 1, Title: "一"}, {ID: 2, Title: "二"}, {ID: 3, Title: "三"},
			{ID: 4, Title: "四"}, {ID: 5, Title: "五"}, {ID: 6, Title: "六"},
		},
	}}
	tool := aitools.NewUserProfileTool(profiles, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"username":"@alice"}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if profiles.lastName != "alice" {
		t.Fatalf("username = %q", profiles.lastName)
	}
	if !strings.Contains(result.Content, "主题 5") || !strings.Contains(result.Content, "https://forum.test/u/alice") {
		t.Fatalf("content = %s", result.Content)
	}
	if strings.Contains(result.Content, "《六》") {
		t.Fatalf("recent topics must be capped: %s", result.Content)
	}
}

func TestUserProfileToolReportsMissingUser(t *testing.T) {
	tool := aitools.NewUserProfileTool(&fakeProfiles{err: profile.ErrProfileNotFound}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"username":"ghost"}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "没有这个用户") {
		t.Fatalf("result = %+v", result)
	}
}

// 成员检索存在的理由：提示词里的发言人只有昵称，模型必须能把它换成 @用户名，
// 否则 user-profile-read 永远打不开。
func TestUserSearchToolFormatsHitsWithHandlesAndLinks(t *testing.T) {
	profiles := &fakeProfiles{hits: []profile.UserSearchHit{
		{UserID: 7, Username: "zhangsan", DisplayName: "张三"},
		{UserID: 8, Username: "zhang-san"},
	}}
	tool := aitools.NewUserSearchTool(profiles, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Name: "user-search", Arguments: `{"query":"  张  "}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}
	if !strings.Contains(result.Content, "张三（@zhangsan）") ||
		!strings.Contains(result.Content, "https://forum.test/u/zhangsan") ||
		!strings.Contains(result.Content, "（@zhang-san）") {
		t.Fatalf("content = %s", result.Content)
	}
	if profiles.last.Query != "张" || profiles.last.Limit != 8 {
		t.Fatalf("search input = %+v", profiles.last)
	}
}

func TestUserSearchToolRejectsEmptyQuery(t *testing.T) {
	profiles := &fakeProfiles{}
	tool := aitools.NewUserSearchTool(profiles, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"   "}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "query") {
		t.Fatalf("result = %+v", result)
	}
	// 空查询必须止步于工具：它等于「列出全站成员」。
	if profiles.last.Query != "" || profiles.last.Limit != 0 {
		t.Fatalf("an empty query must not reach the directory: %+v", profiles.last)
	}
}

func TestUserSearchToolReportsNoMatch(t *testing.T) {
	tool := aitools.NewUserSearchTool(&fakeProfiles{}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"ghost"}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "没有找到") {
		t.Fatalf("result = %+v", result)
	}
}

func TestUserSearchToolReportsInvalidQueryAsModelFacingError(t *testing.T) {
	tool := aitools.NewUserSearchTool(&fakeProfiles{err: profile.ErrProfileInvalid}, testLinks())
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"x"}`}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("an invalid query must not fail the reply: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "关键词") {
		t.Fatalf("result = %+v", result)
	}
}

func TestUserSearchToolPropagatesInternalFailures(t *testing.T) {
	tool := aitools.NewUserSearchTool(&fakeProfiles{err: errors.New("db down")}, testLinks())
	if _, err := tool.Invoke(context.Background(), supportai.ToolCall{Arguments: `{"query":"alice"}`}, supportai.ToolContext{}); err == nil {
		t.Fatal("internal failure must propagate so the orchestrator can log it")
	}
}

func TestTimeToolFormatsServerClock(t *testing.T) {
	fixed := time.Date(2026, 10, 3, 14, 30, 0, 0, time.FixedZone("CST", 8*3600))
	tool := aitools.NewTimeTool(func() time.Time { return fixed })
	result, err := tool.Invoke(context.Background(), supportai.ToolCall{}, supportai.ToolContext{})
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	if !strings.Contains(result.Content, "2026-10-03T14:30:00+08:00") || !strings.Contains(result.Content, "星期六") {
		t.Fatalf("content = %s", result.Content)
	}
	// 零值工具也必须可用：时钟缺省就是 time.Now。
	zero := &aitools.TimeTool{}
	if _, err := zero.Invoke(context.Background(), supportai.ToolCall{}, supportai.ToolContext{}); err != nil {
		t.Fatalf("zero-value tool failed: %v", err)
	}
}
