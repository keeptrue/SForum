package forum

import (
	"context"
	"errors"
	"testing"
)

// countingTrustLevelSource 是 AuthorTrustLevelSource 的计数假实现。
type countingTrustLevelSource struct {
	calls  int
	lastID []int64
	levels map[int64]AuthorTrustLevel
	err    error
}

func (s *countingTrustLevelSource) AuthorTrustLevels(_ context.Context, userIDs []int64) (map[int64]AuthorTrustLevel, error) {
	s.calls++
	s.lastID = append([]int64(nil), userIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return s.levels, nil
}

func newTrustLevelReadService(t *testing.T, source AuthorTrustLevelSource) *PublicReadService {
	t.Helper()
	return NewPublicReadService(NewService(ServiceConfig{})).WithAuthorTrustLevels(source)
}

// TestDecorateTopicListBatchesAuthors 是「20 条同一作者只查一次」的读面证据：
// 装饰层把整页作者 id 去重后一次交给来源，不是逐条调用。
func TestDecorateTopicListBatchesAuthors(t *testing.T) {
	source := &countingTrustLevelSource{levels: map[int64]AuthorTrustLevel{
		7: {Level: 3, LevelSlug: "veteran", Color: "sky"},
	}}
	service := newTrustLevelReadService(t, source)

	topics := make([]TopicSummary, 0, 20)
	for i := 0; i < 20; i++ {
		topics = append(topics, TopicSummary{ID: int64(i + 1), Author: &UserSummary{ID: 7}})
	}
	service.decorateTopicTrustLevels(context.Background(), topics)

	if source.calls != 1 {
		t.Fatalf("来源调用次数 = %d，期望 1（整页一次批量装载）", source.calls)
	}
	if len(source.lastID) != 1 || source.lastID[0] != 7 {
		t.Fatalf("批量入参没有去重：%v", source.lastID)
	}
	for index := range topics {
		if topics[index].AuthorTrustLevel == nil {
			t.Fatalf("第 %d 条没有 authorTrustLevel", index+1)
		}
		if got := *topics[index].AuthorTrustLevel; got != source.levels[7] {
			t.Fatalf("第 %d 条等级 = %+v，期望 %+v", index+1, got, source.levels[7])
		}
	}
}

// TestDecorateCommentListBatchesAuthors 覆盖评论列表的同一契约。
func TestDecorateCommentListBatchesAuthors(t *testing.T) {
	source := &countingTrustLevelSource{levels: map[int64]AuthorTrustLevel{
		9: {Level: 1, LevelSlug: "member", Color: "teal"},
	}}
	service := newTrustLevelReadService(t, source)

	comments := make([]Comment, 0, 20)
	for i := 0; i < 20; i++ {
		comments = append(comments, Comment{ID: int64(i + 1), Author: &UserSummary{ID: 9}})
	}
	// tree 视图会内联子树：子孙作者同样要有等级，且不能为它们多打一次查询。
	comments[0].Children = []Comment{{ID: 99, Author: &UserSummary{ID: 11}}}
	source.levels[11] = AuthorTrustLevel{Level: 2, LevelSlug: "resident", Color: "emerald"}
	service.decorateCommentTrustLevels(context.Background(), comments)

	if source.calls != 1 {
		t.Fatalf("评论装饰调用 = %d（ids=%v），期望 1 次批量", source.calls, source.lastID)
	}
	if len(source.lastID) != 2 {
		t.Fatalf("批量入参 = %v，期望去重后的两个作者 [9 11]", source.lastID)
	}
	for index := range comments {
		if comments[index].AuthorTrustLevel == nil || comments[index].AuthorTrustLevel.LevelSlug != "member" {
			t.Fatalf("第 %d 条评论作者缺等级：%+v", index+1, comments[index].AuthorTrustLevel)
		}
	}
	if child := comments[0].Children[0].AuthorTrustLevel; child == nil || child.LevelSlug != "resident" {
		t.Fatalf("嵌套子评论作者缺等级：%+v", child)
	}
}

// TestDecorateTrustLevelsFailOpen 锁定降级：来源失败时整页保持「无字段」，且装饰
// 不返回错误（读面 HTTP 仍是 200）。
func TestDecorateTrustLevelsFailOpen(t *testing.T) {
	service := newTrustLevelReadService(t, &countingTrustLevelSource{err: errors.New("boom")})
	topics := []TopicSummary{{ID: 1, Author: &UserSummary{ID: 7}}}
	service.decorateTopicTrustLevels(context.Background(), topics)
	if topics[0].AuthorTrustLevel != nil {
		t.Fatalf("失败时不应下发等级：%+v", topics[0].AuthorTrustLevel)
	}

	comments := []Comment{{ID: 1, Author: &UserSummary{ID: 7}}}
	service.decorateCommentTrustLevels(context.Background(), comments)
	if comments[0].AuthorTrustLevel != nil {
		t.Fatalf("失败时不应下发评论等级：%+v", comments[0].AuthorTrustLevel)
	}
}

// TestDecorateTrustLevelsWithoutSource 锁定未装配路径：无来源时不发字段、不报错。
func TestDecorateTrustLevelsWithoutSource(t *testing.T) {
	service := NewPublicReadService(NewService(ServiceConfig{}))
	topics := []TopicSummary{{ID: 1, Author: &UserSummary{ID: 7}}}
	service.decorateTopicTrustLevels(context.Background(), topics)
	if topics[0].AuthorTrustLevel != nil {
		t.Fatal("未装配来源时不应下发等级")
	}
}

// TestTopicAuthorIDsSkipsGuestsAndMissingAuthors 覆盖 id 收集：游客（无 Author）与
// 非正 id 不进入查询。
func TestTopicAuthorIDsSkipsGuestsAndMissingAuthors(t *testing.T) {
	topics := []TopicSummary{
		{Author: nil},
		{Author: &UserSummary{ID: 0}},
		{Author: &UserSummary{ID: 7}},
		{Author: &UserSummary{ID: 7}},
	}
	ids := uniqueAuthorIDs(topicAuthorIDs(topics))
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("收集到的作者 id = %v，期望 [7]", ids)
	}
}
