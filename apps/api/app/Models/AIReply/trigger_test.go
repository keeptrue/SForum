package aireply_test

import (
	"context"
	"errors"
	"testing"

	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
)

type fakeAccounts struct {
	account aireply.BotAccount
	ok      bool
	err     error
}

func (f *fakeAccounts) ReplyBotAccount(context.Context) (aireply.BotAccount, bool, error) {
	return f.account, f.ok, f.err
}

type fakeReader struct {
	comments map[int64]aireply.CommentContext
	topics   map[int64]aireply.TopicContext
	kinds    map[int64]identity.UserKind
	kindErr  error
}

func (f *fakeReader) CommentContext(_ context.Context, commentID int64) (aireply.CommentContext, error) {
	if snapshot, ok := f.comments[commentID]; ok {
		return snapshot, nil
	}
	return aireply.CommentContext{}, errors.New("comment not found")
}

func (f *fakeReader) TopicContext(_ context.Context, topicID int64) (aireply.TopicContext, error) {
	if snapshot, ok := f.topics[topicID]; ok {
		return snapshot, nil
	}
	return aireply.TopicContext{}, errors.New("topic not found")
}

func (f *fakeReader) UserKind(_ context.Context, userID int64) (identity.UserKind, error) {
	if f.kindErr != nil {
		return "", f.kindErr
	}
	if kind, ok := f.kinds[userID]; ok {
		return kind, nil
	}
	return identity.UserKindHuman, nil
}

type fakeEnqueuer struct {
	inputs []aireply.ReplyJobInput
	triage []aireply.TriageJobInput
	err    error
}

func (f *fakeEnqueuer) EnqueueReply(_ context.Context, input aireply.ReplyJobInput) error {
	f.inputs = append(f.inputs, input)
	return f.err
}

// EnqueueTriage 让 fake 满足 aireply.TriageEnqueuer：自主模式的测试走同一套
// 断言工具。
func (f *fakeEnqueuer) EnqueueTriage(_ context.Context, input aireply.TriageJobInput) error {
	f.triage = append(f.triage, input)
	return f.err
}

// fakeModes 是一个静态的触发模式来源。
type fakeModes struct{ mode string }

func (f fakeModes) ReplyTriggerMode(context.Context) (string, error) {
	return f.mode, nil
}

func commentCreatedEvent(commentID, topicID, authorUserID int64) appevents.Envelope {
	return appevents.Envelope{
		Name:        appevents.CommentCreated,
		Kind:        appevents.KindObserve,
		ActorUserID: authorUserID,
		Payload: map[string]any{
			"commentId":    commentID,
			"topicId":      topicID,
			"authorUserId": authorUserID,
		},
	}
}

func newTrigger(enqueuer *fakeEnqueuer, reader *fakeReader) *aireply.Trigger {
	return aireply.NewTrigger(
		&fakeAccounts{account: aireply.BotAccount{UserID: 77, Username: "sforum_ai"}, ok: true},
		reader,
		enqueuer,
	)
}

func withComment(snapshot aireply.CommentContext) *fakeReader {
	return &fakeReader{comments: map[int64]aireply.CommentContext{snapshot.CommentID: snapshot}}
}

func activeComment(raw string) aireply.CommentContext {
	return aireply.CommentContext{CommentID: 501, TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: raw}
}

func TestOrdinaryCommentDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	newTrigger(enqueuer, withComment(activeComment("普通讨论，没有提及任何人。"))).
		CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("an ordinary comment must not wake the bot: %+v", enqueuer.inputs)
	}
}

// 防循环是这里最重要的性质：机器人自己的发言永远不能再唤起一次生成。
func TestBotCommentNeverTriggersAnotherGeneration(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	reader := withComment(activeComment("@sforum_ai 帮我看看"))
	reader.kinds = map[int64]identity.UserKind{77: identity.UserKindBot}
	newTrigger(enqueuer, reader).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 77))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("a bot comment must never trigger generation: %+v", enqueuer.inputs)
	}
}

func TestMentioningTheBotTriggers(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	newTrigger(enqueuer, withComment(activeComment("请问 @sforum_ai 这个怎么配置？"))).
		CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("expected one enqueue, got %+v", enqueuer.inputs)
	}
	job := enqueuer.inputs[0]
	if job.TopicID != 9 || job.ParentCommentID != 501 || job.TriggerUserID != 11 || job.BotUserID != 77 {
		t.Fatalf("job = %+v", job)
	}
}

// 提及解析复用论坛层：代码块里的 @ 不算提及，这里确认这条规则确实生效。
func TestMentionInsideCodeBlockDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	fenced := "示例代码：" + "\n\n" + "```" + "\n" + "@sforum_ai" + "\n" + "```"
	newTrigger(enqueuer, withComment(activeComment(fenced))).
		CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("a mention inside a code block is not a mention: %+v", enqueuer.inputs)
	}
}

func TestReplyingToTheBotTriggers(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	snapshot := activeComment("那我再问一句。")
	snapshot.ParentID = 480
	snapshot.ParentAuthorUserID = 77
	newTrigger(enqueuer, withComment(snapshot)).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("replying to the bot must trigger: %+v", enqueuer.inputs)
	}
}

func TestReplyingToAnotherBotTriggers(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	snapshot := activeComment("另一个机器人的回答有问题。")
	snapshot.ParentID = 480
	snapshot.ParentAuthorUserID = 88
	reader := withComment(snapshot)
	reader.kinds = map[int64]identity.UserKind{88: identity.UserKindBot}
	newTrigger(enqueuer, reader).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("replying to any bot account should trigger: %+v", enqueuer.inputs)
	}
}

func TestReplyingToAHumanDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	snapshot := activeComment("同意你的看法。")
	snapshot.ParentID = 480
	snapshot.ParentAuthorUserID = 12
	newTrigger(enqueuer, withComment(snapshot)).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("replying to a human must not trigger: %+v", enqueuer.inputs)
	}
}

// 待审评论还没公开，不该让机器人先看到它。
func TestPendingCommentDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	snapshot := activeComment("@sforum_ai 帮我看看")
	snapshot.Status = "pending"
	newTrigger(enqueuer, withComment(snapshot)).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("pending content must not trigger: %+v", enqueuer.inputs)
	}
}

func TestNoBotAccountDisablesTheFeature(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	trigger := aireply.NewTrigger(&fakeAccounts{ok: false}, withComment(activeComment("@sforum_ai")), enqueuer)
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("without a bot account nothing should happen: %+v", enqueuer.inputs)
	}
}

func TestEnqueueFailureIsSwallowed(t *testing.T) {
	enqueuer := &fakeEnqueuer{err: errors.New("queue unavailable")}
	newTrigger(enqueuer, withComment(activeComment("@sforum_ai 在吗"))).
		CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatal("the attempt should still be recorded by the fake")
	}
}

func TestReaderFailureIsSwallowed(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	reader := &fakeReader{kindErr: errors.New("db down")}
	newTrigger(enqueuer, reader).CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatal("a reader failure must not enqueue anything")
	}
}

func topicCreatedEvent(topicID, authorUserID int64) appevents.Envelope {
	return appevents.Envelope{
		Name:        appevents.TopicCreated,
		Kind:        appevents.KindObserve,
		ActorUserID: authorUserID,
		Payload: map[string]any{
			"topicId":      topicID,
			"authorUserId": authorUserID,
		},
	}
}

func withTopic(snapshot aireply.TopicContext) *fakeReader {
	return &fakeReader{topics: map[int64]aireply.TopicContext{snapshot.TopicID: snapshot}}
}

// 发帖 @ 机器人与评论 @ 机器人是同一个用户预期：主题正文里的提及必须唤起回复。
func TestMentioningTheBotInANewTopicTriggers(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	topic := aireply.TopicContext{TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: "请问 @sforum_ai 这个怎么配置？"}
	newTrigger(enqueuer, withTopic(topic)).TopicCreated(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("expected one enqueue, got %+v", enqueuer.inputs)
	}
	job := enqueuer.inputs[0]
	// 主题触发没有父评论：回复直接挂在主题下，TriggerCommentID 为 0 表示
	// 提示词以上楼主正文为触发内容。
	if job.TopicID != 9 || job.ParentCommentID != 0 || job.TriggerCommentID != 0 || job.TriggerUserID != 11 || job.BotUserID != 77 {
		t.Fatalf("job = %+v", job)
	}
}

func TestOrdinaryTopicDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	topic := aireply.TopicContext{TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: "普通讨论，没有提及任何人。"}
	newTrigger(enqueuer, withTopic(topic)).TopicCreated(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("an ordinary topic must not wake the bot: %+v", enqueuer.inputs)
	}
}

func TestPendingTopicDoesNotTrigger(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	topic := aireply.TopicContext{TopicID: 9, AuthorUserID: 11, Status: "pending", RawContent: "@sforum_ai 帮我看看"}
	newTrigger(enqueuer, withTopic(topic)).TopicCreated(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("pending content must not trigger: %+v", enqueuer.inputs)
	}
}

// 机器人自己发帖（例如未来插件代发）同样不允许唤起新一轮生成。
func TestBotTopicNeverTriggersAnotherGeneration(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	reader := withTopic(aireply.TopicContext{TopicID: 9, AuthorUserID: 77, Status: "active", RawContent: "@sforum_ai"})
	reader.kinds = map[int64]identity.UserKind{77: identity.UserKindBot}
	newTrigger(enqueuer, reader).TopicCreated(context.Background(), topicCreatedEvent(9, 77))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("a bot topic must never trigger generation: %+v", enqueuer.inputs)
	}
}

// Subscriber 是发布路径上的一层包装：它必须原样返回原发布结果，且只对
// comment.created / topic.created 这两个事件做额外处理。
func TestSubscriberForwardsAndOnlyHandlesCommentCreated(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	inner := &recordingPublisher{result: appevents.Result{OK: true}}
	subscriber := aireply.NewSubscriber(inner, newTrigger(enqueuer, withComment(activeComment("@sforum_ai 在吗"))))

	if result := subscriber.Emit(context.Background(), commentCreatedEvent(501, 9, 11)); !result.OK {
		t.Fatalf("the inner result must be forwarded: %+v", result)
	}
	if len(inner.names) != 1 || inner.names[0] != appevents.CommentCreated {
		t.Fatalf("the original event must still be published: %+v", inner.names)
	}
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("comment.created should reach the trigger: %+v", enqueuer.inputs)
	}

	subscriber.Emit(context.Background(), appevents.Envelope{Name: appevents.TopicUpdated, Kind: appevents.KindObserve})
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("unrelated events must not reach the trigger: %+v", enqueuer.inputs)
	}
}

// topic.created 必须同样到达判定器：发帖 @ 机器人不回应用是这个订阅路径
// 曾经缺失时的外部表现。
func TestSubscriberForwardsTopicCreated(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	topic := aireply.TopicContext{TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: "@sforum_ai 在吗"}
	subscriber := aireply.NewSubscriber(&recordingPublisher{result: appevents.Result{OK: true}},
		newTrigger(enqueuer, withTopic(topic)))

	subscriber.Emit(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.inputs) != 1 {
		t.Fatalf("topic.created should reach the trigger: %+v", enqueuer.inputs)
	}
}

type recordingPublisher struct {
	result appevents.Result
	names  []string
}

func (p *recordingPublisher) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	p.names = append(p.names, envelope.Name)
	return p.result
}

// ===== 触发模式矩阵 =====
//
// mention（默认）：只有被 @ / 回复机器人才入队生成；
// autonomous：未被点名时先进判定，被点名仍然直达生成；
// off：一切都不入队，包括被 @。

func withModes(mode string) func(*aireply.Trigger) *aireply.Trigger {
	return func(t *aireply.Trigger) *aireply.Trigger { return t.WithTriggerModes(fakeModes{mode: mode}) }
}

func TestAutonomousModeTriagesOrdinaryComments(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	trigger := withModes(supportai.TriggerModeAutonomous)(newTrigger(enqueuer, withComment(activeComment("普通讨论，没有提及任何人。"))))
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 {
		t.Fatalf("autonomous mode must not enqueue generation directly: %+v", enqueuer.inputs)
	}
	if len(enqueuer.triage) != 1 {
		t.Fatalf("expected one triage enqueue, got %+v", enqueuer.triage)
	}
	job := enqueuer.triage[0]
	if job.TopicID != 9 || job.TriggerCommentID != 501 || job.SubjectUserID != 11 {
		t.Fatalf("triage job = %+v", job)
	}
}

func TestAutonomousModeStillDirectlyRepliesWhenMentioned(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	trigger := withModes(supportai.TriggerModeAutonomous)(newTrigger(enqueuer, withComment(activeComment("请问 @sforum_ai 这个怎么配置？"))))
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 1 || len(enqueuer.triage) != 0 {
		t.Fatalf("a mention must bypass triage: replies=%+v triage=%+v", enqueuer.inputs, enqueuer.triage)
	}
}

func TestAutonomousModeTriagesOrdinaryTopics(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	topic := aireply.TopicContext{TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: "普通新帖，没有提及任何人。"}
	trigger := withModes(supportai.TriggerModeAutonomous)(newTrigger(enqueuer, withTopic(topic)))
	trigger.TopicCreated(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.triage) != 1 || len(enqueuer.inputs) != 0 {
		t.Fatalf("ordinary topic must go to triage: replies=%+v triage=%+v", enqueuer.inputs, enqueuer.triage)
	}
	if job := enqueuer.triage[0]; job.TriggerCommentID != 0 {
		t.Fatalf("topic triage has no trigger comment: %+v", job)
	}
}

func TestOffModeSuppressesEverything(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	reader := withComment(activeComment("请问 @sforum_ai 这个怎么配置？"))
	reader.topics = map[int64]aireply.TopicContext{9: {TopicID: 9, AuthorUserID: 11, Status: "active", RawContent: "@sforum_ai"}}
	trigger := withModes(supportai.TriggerModeOff)(newTrigger(enqueuer, reader))
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	trigger.TopicCreated(context.Background(), topicCreatedEvent(9, 11))
	if len(enqueuer.inputs) != 0 || len(enqueuer.triage) != 0 {
		t.Fatalf("off mode must suppress all triggers: replies=%+v triage=%+v", enqueuer.inputs, enqueuer.triage)
	}
}

// 未装配模式来源（旧装配路径 / 读法缺失）时行为必须与 mention 完全一致。
func TestMissingModeSourceBehavesAsMention(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	newTrigger(enqueuer, withComment(activeComment("普通讨论，没有提及任何人。"))).
		CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 || len(enqueuer.triage) != 0 {
		t.Fatalf("no mode source must mean mention: replies=%+v triage=%+v", enqueuer.inputs, enqueuer.triage)
	}
}

// 模式读法故障时回到 mention：这是故障时最保守的行为，绝不能变成放开自主参与。
func TestModeSourceFailureFallsBackToMention(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	failing := failingModes{}
	trigger := newTrigger(enqueuer, withComment(activeComment("普通讨论，没有提及任何人。"))).WithTriggerModes(failing)
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.inputs) != 0 || len(enqueuer.triage) != 0 {
		t.Fatalf("mode failure must fall back to mention: replies=%+v triage=%+v", enqueuer.inputs, enqueuer.triage)
	}
}

type failingModes struct{}

func (failingModes) ReplyTriggerMode(context.Context) (string, error) {
	return "", errors.New("settings unavailable")
}

// 未知模式值同样归一到 mention：NormalizedTriggerMode 的兜底语义。
func TestUnknownModeValueFallsBackToMention(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	trigger := withModes("yolo")(newTrigger(enqueuer, withComment(activeComment("普通讨论，没有提及任何人。"))))
	trigger.CommentCreated(context.Background(), commentCreatedEvent(501, 9, 11))
	if len(enqueuer.triage) != 0 {
		t.Fatalf("unknown mode must not enable autonomous: %+v", enqueuer.triage)
	}
}
