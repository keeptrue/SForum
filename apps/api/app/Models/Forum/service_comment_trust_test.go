package forum

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// fakeTrustPolicy 是新人信任阶梯的最小假实现：只关心「是否禁止外链」。
type fakeTrustPolicy struct{ forbidLinks bool }

func (f fakeTrustPolicy) NewUserTrustDays(context.Context) (int, error) { return 30, nil }
func (f fakeTrustPolicy) NewUserTopicCooldownSeconds(context.Context) (int, error) {
	return 0, nil
}
func (f fakeTrustPolicy) NewUserCommentCooldownSeconds(context.Context) (int, error) {
	return 0, nil
}
func (f fakeTrustPolicy) NewUserDailyTopicLimit(context.Context) (int, error)   { return 0, nil }
func (f fakeTrustPolicy) NewUserDailyCommentLimit(context.Context) (int, error) { return 0, nil }
func (f fakeTrustPolicy) NewUserForbidOutboundLinks(context.Context) (bool, error) {
	return f.forbidLinks, nil
}

// TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots 固化两条规则：
// 新人（人类）带外链的评论被拒绝；机器人不进新人阶梯，因此可以带链接——回复
// 机器人引用站内内容必须给链接，这条豁免是它的能力前提。
func TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots(t *testing.T) {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 42, Status: TopicStatusActive}
	service := NewService(ServiceConfig{Store: store}).WithTrustPolicy(fakeTrustPolicy{forbidLinks: true})

	fresh := time.Now().UTC().Add(-time.Hour)
	human := commentCreator()
	human.CreatedAt = fresh
	if _, err := service.CreateComment(context.Background(), human, CreateCommentInput{
		TopicID: 42,
		Content: validMarkdownContent("看这个 https://example.com/post"),
	}); !errors.Is(err, ErrOutboundLinkForbidden) {
		t.Fatalf("new human with an outbound link must be rejected, got %v", err)
	}

	bot := commentCreator()
	bot.ID = 66
	bot.Kind = identity.UserKindBot
	bot.CreatedAt = fresh
	comment, err := service.CreateComment(context.Background(), bot, CreateCommentInput{
		TopicID: 42,
		Content: validMarkdownContent("看这个 https://example.com/post"),
	})
	if err != nil {
		t.Fatalf("bots must not enter the new-user trust ladder, got %v", err)
	}
	if comment.ID == 0 {
		t.Fatal("expected the bot comment to be created")
	}
}
