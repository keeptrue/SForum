package forum

import (
	"context"
	"errors"
	"testing"
	"time"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/internal/trustgate"
)

// 信任等级基线门（Z3-b″）的验收门 N-01..N-04：全部走**真实**的 Forum.Service 与
// Attachments.Service，actor 由**真实**的 `LoadEffectiveActorTx` 装载。
// N-01/N-04 是 N-02/N-03 的对照组：没有它们，「一律扣除」也会让 N-02/N-03 变绿。

type gateZeroActivity struct{}

func (gateZeroActivity) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return trustlevels.Activity{}, nil
}

type gateDefaultPolicy struct{}

func (gateDefaultPolicy) Policy(context.Context) (trustlevels.Policy, error) {
	return trustlevels.DefaultPolicy(), nil
}

// gateOptionsStore 让 options.Service 用推荐默认值（不落库）。
type gateOptionsStore struct{}

func (gateOptionsStore) List(context.Context) ([]options.Option, error) { return nil, nil }
func (gateOptionsStore) InsertMissing(context.Context, options.UpdateInput) error {
	return nil
}
func (gateOptionsStore) Upsert(context.Context, options.UpdateInput) (options.Option, error) {
	return options.Option{}, nil
}

func gateResolver() *trustlevels.Resolver {
	return trustlevels.NewResolver(gateZeroActivity{}, gateDefaultPolicy{})
}

// loadGateActor 用真实 loader 装载指定用户。
func loadGateActor(t *testing.T, fixture *trustgate.Fixture, userID int64) identity.Actor {
	t.Helper()
	actor, err := identity.LoadEffectiveActorTx(context.Background(), fixture.BeginTx(t), userID, gateResolver())
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx: %v", err)
	}
	return actor
}

func gateCommentService() *Service {
	store := newServiceFakeStore()
	store.topicForComment = TopicSummary{ID: 42, Status: TopicStatusActive}
	return NewService(ServiceConfig{Store: store})
}

func gateCommentInput() CreateCommentInput {
	return CreateCommentInput{
		TopicID: 42,
		Content: ContentInput{
			RawContent:   "新用户的第一条回复",
			SourceFormat: SourceFormatMarkdown,
			EditorType:   EditorTypeMarkdown,
		},
	}
}

func gateTopicInput() CreateTopicInput {
	return CreateTopicInput{
		Title:        "新用户的第一条主题",
		CategorySlug: "general",
		Content:      validMarkdownContent("正文"),
	}
}

// N-01：默认安装、账龄 <7d、零活动的 TL0 会员**能发一条回复**。
// 这条是 N-02/N-03 的对照组：TL0 不是「什么都不许」，只是不能开主题/传附件。
func TestTrustGateN01NewcomerCanStillComment(t *testing.T) {
	fixture := trustgate.New(t)
	fixture.InsertMemberUser(t, 21, "human", time.Now().Add(-1*time.Hour))
	actor := loadGateActor(t, fixture, 21)

	if _, err := gateCommentService().CreateComment(context.Background(), actor, gateCommentInput()); err != nil {
		t.Fatalf("N-01 失败：TL0 会员发不了回复（err=%v）。这会让 comment_count 恒为 0，锁死信任阶梯", err)
	}
}

// N-02：同一个人**不能开新主题**（topic.create 被减法门扣掉）。
func TestTrustGateN02NewcomerCannotCreateTopic(t *testing.T) {
	fixture := trustgate.New(t)
	fixture.InsertMemberUser(t, 22, "human", time.Now().Add(-1*time.Hour))
	actor := loadGateActor(t, fixture, 22)

	_, err := gateCommentService().CreateTopic(context.Background(), actor, gateTopicInput())
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("N-02 失败：TL0 会员仍能开新主题（err=%v，期望 ErrPermissionDenied）", err)
	}
}

// N-03：同一个人**不能传附件**（attachment.upload 被扣）。
//
// 走两条真实路径：`UploadAvatar` 的第一句硬门，以及 HTTP 上传端点用的
// `UploadPolicy` 判定（`Reason == permission_denied`）。
func TestTrustGateN03NewcomerCannotUpload(t *testing.T) {
	fixture := trustgate.New(t)
	fixture.InsertMemberUser(t, 23, "human", time.Now().Add(-1*time.Hour))
	actor := loadGateActor(t, fixture, 23)

	// 先断言策略判定：它是 HTTP 上传端点真正消费的那条判定，且失败信息干净
	// （先调 UploadAvatar 的话，门被改坏时会一路走到 nil 依赖上 panic，红得没信息量）。
	policy, err := attachments.NewService(nil, options.NewService(gateOptionsStore{})).UploadPolicy(context.Background(), actor)
	if err != nil {
		t.Fatalf("UploadPolicy: %v", err)
	}
	if policy.Reason != uploadpolicy.ReasonPermissionDenied {
		t.Fatalf("N-03 失败：上传策略没有报 permission_denied，得到 reason=%q allowed=%v", policy.Reason, policy.Allowed)
	}
	// 再断言硬门本身（UploadAvatar 的第一句）。
	_, err = attachments.NewService(nil, nil).UploadAvatar(context.Background(), actor, attachments.UploadInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("N-03 失败：TL0 会员仍能上传头像/附件（err=%v，期望 ErrPermissionDenied）", err)
	}
}

// N-04：过了新人窗口（账龄 >7d，零活动 ⇒ TL1）**三个键全部回来**。
func TestTrustGateN04MemberRegainsAllKeys(t *testing.T) {
	fixture := trustgate.New(t)
	fixture.InsertMemberUser(t, 24, "human", time.Now().Add(-8*24*time.Hour))
	actor := loadGateActor(t, fixture, 24)

	if !actor.TrustLevelResolved || actor.TrustLevel < int(trustlevels.LevelMember) {
		t.Fatalf("N-04 前提失败：账龄 8 天的零活动用户应落 TL1+，得到 level=%d resolved=%v",
			actor.TrustLevel, actor.TrustLevelResolved)
	}
	// 三个键：回复、开主题、传附件。
	if _, err := gateCommentService().CreateComment(context.Background(), actor, gateCommentInput()); err != nil {
		t.Fatalf("N-04 失败：TL1 发不了回复（err=%v）", err)
	}
	if _, err := gateCommentService().CreateTopic(context.Background(), actor, gateTopicInput()); errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatal("N-04 失败：TL1 仍被拒开主题，减法门变成了永久剥夺")
	}
	policy, err := attachments.NewService(nil, options.NewService(gateOptionsStore{})).UploadPolicy(context.Background(), actor)
	if err != nil {
		t.Fatalf("UploadPolicy: %v", err)
	}
	if policy.Reason == uploadpolicy.ReasonPermissionDenied {
		t.Fatal("N-04 失败：TL1 仍被拒传附件，减法门变成了永久剥夺")
	}
}
