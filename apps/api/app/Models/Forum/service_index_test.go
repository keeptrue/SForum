package forum

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// fakeIndexer 记录 EnqueueIndex/EnqueueDelete 调用，用于断言业务流触发搜索调度。
type fakeIndexer struct {
	indexedIDs []int64
	deletedIDs []int64
}

func (f *fakeIndexer) EnqueueIndex(_ context.Context, topicID int64) error {
	f.indexedIDs = append(f.indexedIDs, topicID)
	return nil
}

func (f *fakeIndexer) EnqueueDelete(_ context.Context, topicID int64) error {
	f.deletedIDs = append(f.deletedIDs, topicID)
	return nil
}

// failingIndexer 让调度失败，用于验证降级（不中断主流程）。
type failingIndexer struct{}

func (failingIndexer) EnqueueIndex(_ context.Context, _ int64) error {
	return errors.New("index unavailable")
}
func (failingIndexer) EnqueueDelete(_ context.Context, _ int64) error {
	return errors.New("index unavailable")
}

type staticPublicationPolicy struct {
	decision PublicationDecision
}

func (policy staticPublicationPolicy) EvaluatePublication(context.Context, PublicationInput) (PublicationDecision, error) {
	return policy.decision, nil
}

func newServiceWithIndexerForTest(indexer TopicSearchIndexer) *Service {
	store := newServiceFakeStore()
	// 使用 staticSettingsResolver（返回 defaultForumSettings，含有效 tagMaxPerTopic），
	// 避免 fakeSettingsResolver 默认空 settings 触发 ErrInvalidSettings。
	svc := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: appevents.NoopPublisher{}, Indexer: indexer})
	return svc
}

func TestCreateTopicDispatchesIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	svc := newServiceWithIndexerForTest(idx)

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	_, err := svc.CreateTopic(ctx, actor, CreateTopicInput{
		Title:   "新主题",
		Content: ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}
	// nextID 从 1 开始，首个创建的主题 ID 应为 1。
	if len(idx.indexedIDs) != 1 || idx.indexedIDs[0] != 1 {
		t.Fatalf("expected topic 1 indexed, got %v", idx.indexedIDs)
	}
}

func TestCreateTopicKeepsPendingTopicOutOfPublicIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	store := newServiceFakeStore()
	svc := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: appevents.NoopPublisher{}, Indexer: idx, PublicationPolicy: staticPublicationPolicy{decision: PublicationDecision{Pending: true, Triggers: []string{"new_user"}}}})

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	topic, err := svc.CreateTopic(ctx, actor, CreateTopicInput{
		Title:   "待审主题",
		Content: ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("create pending topic: %v", err)
	}
	if topic.Status != TopicStatusPending {
		t.Fatalf("expected pending status, got %q", topic.Status)
	}
	if len(idx.indexedIDs) != 0 {
		t.Fatalf("pending topic must not be indexed, got %v", idx.indexedIDs)
	}
	if len(store.createdTopic.ModerationTriggers) != 1 || store.createdTopic.ModerationTriggers[0] != "new_user" {
		t.Fatalf("expected moderation trigger snapshot, got %v", store.createdTopic.ModerationTriggers)
	}
}

func TestCreateCommentKeepsPendingCommentOutOfPublicIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	store := newServiceFakeStore()
	svc := NewService(ServiceConfig{Store: store, Settings: staticSettingsResolver{}, Publisher: appevents.NoopPublisher{}, Indexer: idx, PublicationPolicy: staticPublicationPolicy{decision: PublicationDecision{Pending: true, Triggers: []string{"external_link"}}}})

	actor := identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostCreate: true}}
	comment, err := svc.CreateComment(ctx, actor, CreateCommentInput{
		TopicID: 1,
		Content: ContentInput{RawContent: "https://outside.test", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("create pending comment: %v", err)
	}
	if comment.Status != CommentStatusPending {
		t.Fatalf("expected pending status, got %q", comment.Status)
	}
	if len(idx.indexedIDs) != 0 {
		t.Fatalf("pending comment must not re-index topic, got %v", idx.indexedIDs)
	}
}

func TestDeleteTopicDispatchesDeleteIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	svc := newServiceWithIndexerForTest(idx)

	actor := identity.Actor{ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicDeleteOwn: true}}
	_, err := svc.DeleteTopic(ctx, actor, 7)
	if err != nil {
		t.Fatalf("delete topic: %v", err)
	}
	if len(idx.deletedIDs) != 1 || idx.deletedIDs[0] != 7 {
		t.Fatalf("expected topic 7 index deleted, got %v", idx.deletedIDs)
	}
}

func TestCreateCommentDispatchesTopicIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	svc := newServiceWithIndexerForTest(idx)

	actor := identity.Actor{ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostCreate: true}}
	_, err := svc.CreateComment(ctx, actor, CreateCommentInput{
		TopicID: 1,
		Content: ContentInput{RawContent: "评论正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("create comment: %v", err)
	}
	// 评论更新主题 last_activity_at，应触发主题重索引。
	if len(idx.indexedIDs) != 1 || idx.indexedIDs[0] != 1 {
		t.Fatalf("expected topic 1 re-indexed, got %v", idx.indexedIDs)
	}
}

func TestApplyTopicActionHideDispatchesDeleteIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	svc := newServiceWithIndexerForTest(idx)

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicDeleteAny: true}}
	_, err := svc.ApplyTopicAction(ctx, actor, TopicLifecycleInput{TopicID: 7, Action: TopicActionHide})
	if err != nil {
		t.Fatalf("hide topic: %v", err)
	}
	if len(idx.deletedIDs) != 1 || idx.deletedIDs[0] != 7 {
		t.Fatalf("expected hide to delete index for 7, got %v", idx.deletedIDs)
	}
}

func TestApplyTopicActionPinDispatchesIndex(t *testing.T) {
	ctx := context.Background()
	idx := &fakeIndexer{}
	svc := newServiceWithIndexerForTest(idx)

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicPin: true}}
	_, err := svc.ApplyTopicAction(ctx, actor, TopicLifecycleInput{TopicID: 7, Action: TopicActionPin})
	if err != nil {
		t.Fatalf("pin topic: %v", err)
	}
	if len(idx.indexedIDs) != 1 || idx.indexedIDs[0] != 7 {
		t.Fatalf("expected pin to re-index 7, got %v", idx.indexedIDs)
	}
}

func TestIndexerFailureDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	// 失败的 indexer 不应阻断主题创建。
	svc := newServiceWithIndexerForTest(failingIndexer{})

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	_, err := svc.CreateTopic(ctx, actor, CreateTopicInput{
		Title:   "降级测试",
		Content: ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	})
	if err != nil {
		t.Fatalf("create topic should succeed despite indexer failure, got: %v", err)
	}
}

func TestNilIndexerIsSafe(t *testing.T) {
	ctx := context.Background()
	// indexer 为 nil 时应正常工作（降级为不索引）。
	store := newServiceFakeStore()
	svc := NewService(ServiceConfig{Store: store}) // staticSettingsResolver + nil indexer

	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	if _, err := svc.CreateTopic(ctx, actor, CreateTopicInput{
		Title:   "无索引",
		Content: ContentInput{RawContent: "正文", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
	}); err != nil {
		t.Fatalf("create topic with nil indexer: %v", err)
	}
}

func TestListTopicsRejectsQuery(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWithIndexerForTest(nil)

	_, err := svc.ListTopics(ctx, TopicListInput{Query: "关键词"})
	if !errors.Is(err, ErrUseSearchEndpoint) {
		t.Fatalf("expected ErrUseSearchEndpoint, got %v", err)
	}
}

func TestNormalizePageClampsDeepPagination(t *testing.T) {
	cases := []struct {
		page, perPage int
		wantPage      int
	}{
		{0, 0, 1},
		{-5, 0, 1},
		{1, 20, 1},
		{100, 50, 100},
		{200, 20, 200},
		{201, 20, 200}, // 超上限 clamp
		{10000, 20, 200},
	}
	for _, tc := range cases {
		page, _ := normalizePage(tc.page, tc.perPage)
		if page != tc.wantPage {
			t.Errorf("normalizePage(%d, %d) page = %d, want %d", tc.page, tc.perPage, page, tc.wantPage)
		}
	}
}
