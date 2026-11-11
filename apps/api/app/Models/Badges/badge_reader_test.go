package badges

import (
	"context"
	"testing"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// countingStore 记录 TopBadgesFor 被调用了几次：批量装饰的核心契约是
// 「整页一次查询」，不是「每个作者一次」。写成 N+1 也能出正确结果，
// 所以只能靠调用次数断言抓住它。
type countingStore struct {
	*fakeStore
	calls int
	last  []int64
}

func (c *countingStore) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	c.calls++
	c.last = append([]int64(nil), userIDs...)
	return c.fakeStore.TopBadgesFor(ctx, userIDs)
}

func newBadgeReaderFixture() (*Reader, *countingStore) {
	store := &countingStore{fakeStore: newFakeStore()}
	store.badges = []Badge{
		{ID: 1, Slug: "veteran", Name: "元老", Tier: TierGold, Icon: "i-lucide-hourglass", Enabled: true},
		{ID: 2, Slug: "first-topic", Name: "初次发帖", Tier: TierBronze, Icon: "i-lucide-sprout", Enabled: true},
	}
	store.grants[7] = map[int64]time.Time{1: {}}
	store.top[7] = 1
	store.grants[8] = map[int64]time.Time{2: {}}
	store.top[8] = 2
	return NewReader(NewService(store, newFakeActivity())), store
}

// TestDecorateTopicsFillsTopBadgeInOneQuery 断言整页主题只查一次，
// 且作者与最近回复作者都被填上。
func TestDecorateTopicsFillsTopBadgeInOneQuery(t *testing.T) {
	reader, store := newBadgeReaderFixture()
	topics := []forum.TopicSummary{
		{ID: 1, Author: &forum.UserSummary{ID: 7}},
		{ID: 2, Author: &forum.UserSummary{ID: 8}, LastReplyAuthor: &forum.UserSummary{ID: 7}},
		{ID: 3, Author: &forum.UserSummary{ID: 99}}, // 没有徽章
	}
	if err := reader.DecorateTopics(context.Background(), topics); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("TopBadgesFor calls = %d, want 1 (整页一次查询，禁止 N+1)", store.calls)
	}
	if topics[0].Author.TopBadge == nil || topics[0].Author.TopBadge.Slug != "veteran" {
		t.Fatalf("topic 1 author badge = %+v", topics[0].Author.TopBadge)
	}
	if topics[1].LastReplyAuthor.TopBadge == nil || topics[1].LastReplyAuthor.TopBadge.Slug != "veteran" {
		t.Fatalf("topic 2 last reply author badge = %+v", topics[1].LastReplyAuthor.TopBadge)
	}
	// 没有徽章必须是 nil 而不是空结构：空结构会渲染出一个空 chip。
	if topics[2].Author.TopBadge != nil {
		t.Fatalf("author without badge must be nil, got %+v", topics[2].Author.TopBadge)
	}
}

// TestDecorateCommentsFillsTopBadge 断言评论作者同样批量补齐。
func TestDecorateCommentsFillsTopBadge(t *testing.T) {
	reader, store := newBadgeReaderFixture()
	comments := []forum.Comment{
		{ID: 1, Author: &forum.UserSummary{ID: 8}},
		{ID: 2, Author: &forum.UserSummary{ID: 7}},
		{ID: 3}, // 作者被删除
	}
	if err := reader.DecorateComments(context.Background(), comments); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("TopBadgesFor calls = %d, want 1", store.calls)
	}
	if comments[0].Author.TopBadge == nil || comments[0].Author.TopBadge.Slug != "first-topic" {
		t.Fatalf("comment 1 badge = %+v", comments[0].Author.TopBadge)
	}
	if comments[1].Author.TopBadge == nil || comments[1].Author.TopBadge.Tier != string(TierGold) {
		t.Fatalf("comment 2 badge = %+v", comments[1].Author.TopBadge)
	}
}

// TestDecorateIsActorAgnostic 断言同一批输入对「谁在看」没有依赖：
// 装饰结果只由作者决定。这条钉住的是「徽章可以进缓存」的设计判断——
// 一旦有人把 viewer 传进装饰路径，这里就会失效。
func TestDecorateIsActorAgnostic(t *testing.T) {
	reader, _ := newBadgeReaderFixture()
	first := []forum.TopicSummary{{ID: 1, Author: &forum.UserSummary{ID: 7}}}
	second := []forum.TopicSummary{{ID: 1, Author: &forum.UserSummary{ID: 7}}}

	if err := reader.DecorateTopics(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := reader.DecorateTopics(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if first[0].Author.TopBadge == nil || second[0].Author.TopBadge == nil {
		t.Fatal("both runs must fill the badge")
	}
	if first[0].Author.TopBadge.Slug != second[0].Author.TopBadge.Slug {
		t.Fatalf("decorate is viewer-dependent: %q vs %q",
			first[0].Author.TopBadge.Slug, second[0].Author.TopBadge.Slug)
	}
}

// TestDecorateSkipsEmptyInput 断言空输入不发查询、nil 安全。
func TestDecorateSkipsEmptyInput(t *testing.T) {
	reader, store := newBadgeReaderFixture()
	if err := reader.DecorateTopics(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := reader.DecorateComments(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if store.calls != 0 {
		t.Fatalf("empty input must not query, got %d calls", store.calls)
	}

	var nilReader *Reader
	if err := nilReader.DecorateTopics(context.Background(), []forum.TopicSummary{{ID: 1}}); err != nil {
		t.Fatalf("nil reader must be a no-op, got %v", err)
	}
}
