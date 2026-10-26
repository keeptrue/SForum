package aireply_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
)

// 这一组是 B4/B5 的**端到端落库证据**，全部在真实 PostgreSQL 上跑，且**不依赖
// 模型是否遵守引用格式**：
//
//   - 补全输出由测试直接给定（含 [[cite:...]] 标记），因此「模型这次有没有写标记」
//     这个概率性因素被排除在验收之外。这正是运营者要的确定性：格式遵守率是一个
//     需要观测的指标，不是一条验收前提。
//   - 发布者（poster）是真实写库的一段 SQL：它插入一条 status='active' 的评论并
//     返回 id，从而让 ai_citations 的外键被真正满足、级联真正可验证。
//   - 引用存储与会话状态都是 PostgresCitationStore / PostgresTopicStateStore 的
//     真实实例，共用同一个池，与生产装配一致（providers.NewAIReplyGenerator）。
//
// 它补齐了「单测 + store 层集成」之外缺的那一段：Generate → 发布 → 落库 → 读回。

// recordingPGPoster 是真实写库的发布者。它只做 BotCommentPoster 要求的最小动作：
// 插一条评论并返回 id。审核/通知/事件不在本测试的关注面（那些由 forum.Service
// 保证），这里验证的是「生成器把什么写进了 B4/B5 的两张表」。
//
// 关键在于它是**真实插入**：ai_citations.source_comment_id 有外键指向 comments，
// 用内存 fake 返回的假 id 会让外键失败，从而无法证明落库链路真的通。
type recordingPGPoster struct {
	pool   *pgxpool.Pool
	inputs []aireply.BotCommentInput
	lastID int64
	fail   bool
}

func (p *recordingPGPoster) PostBotComment(ctx context.Context, botUserID int64, input aireply.BotCommentInput) (int64, error) {
	p.inputs = append(p.inputs, input)
	if p.fail {
		return 0, errPostRefused
	}
	var id int64
	if err := p.pool.QueryRow(ctx, `
		INSERT INTO comments (id, topic_id, content_id, author_user_id, status, created_at)
		VALUES (DEFAULT, $1, 102, $2, 'active', now())
		RETURNING id
	`, input.TopicID, botUserID).Scan(&id); err != nil {
		return 0, err
	}
	p.lastID = id
	return id, nil
}

var errPostRefused = errPostRefusedType{}

type errPostRefusedType struct{}

func (errPostRefusedType) Error() string { return "publish refused by test" }

// 端到端：一次带标记的生成 → ai_citations 有行（含 run_id 与来自数据库的引文）
// → ai_topic_state 有文档（含同一条事实）→ 第二次生成注入该事实。
func TestGenerateEndToEndPersistsCitationsAndStateInPostgres(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)
	stateStore := aireply.NewPostgresTopicStateStore(pool)

	poster := &recordingPGPoster{pool: pool}
	// 第一次：模型引用 500 楼（可见），正文里带标记。
	first := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "按 500 楼的说法，池子设 20 就够。[[cite:comment:500]]",
		RunID: "run-e2e-first",
	}}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, first, poster, nil,
	).WithCitations(store).WithTopicState(stateStore)
	if err := generator.Generate(ctx, jobInput()); err != nil {
		t.Fatalf("first Generate: %v", err)
	}

	// 1) 发布出去的正文字里不能残留标记，引用随发布传递。
	if len(poster.inputs) != 1 {
		t.Fatalf("posted = %d", len(poster.inputs))
	}
	if strings.Contains(poster.inputs[0].Content, "[[cite:") {
		t.Fatalf("markers must be stripped: %q", poster.inputs[0].Content)
	}
	if len(poster.inputs[0].Citations) != 1 {
		t.Fatalf("citations = %+v", poster.inputs[0].Citations)
	}
	producedCommentID := poster.lastID

	// 2) ai_citations 真的有行，且 run_id / 引文 / 归属全对。
	var runID, excerpt string
	var topicID, commentID int64
	if err := pool.QueryRow(ctx, `
		SELECT run_id, topic_id, comment_id, excerpt
		FROM ai_citations WHERE source_comment_id = $1 ORDER BY id LIMIT 1
	`, producedCommentID).Scan(&runID, &topicID, &commentID, &excerpt); err != nil {
		t.Fatalf("ai_citations has no row for the produced comment: %v", err)
	}
	if runID != "run-e2e-first" {
		t.Fatalf("run_id = %q", runID)
	}
	if topicID != 10 || commentID != 500 {
		t.Fatalf("citation target = topic %d comment %d", topicID, commentID)
	}
	// 引文来自被引用楼层的正文，不是模型写的那句「池子设 20 就够」。
	if excerpt != "池子设 20 就行" {
		t.Fatalf("excerpt must come from the stored floor, got %q", excerpt)
	}

	// 3) ai_topic_state 有文档，且事实来自同一次校验。
	loaded, ok := stateStore.Load(ctx, jobInput().TopicID, jobInput().BotUserID)
	if !ok {
		t.Fatal("ai_topic_state has no document for the topic")
	}
	if loaded.LastRunID != "run-e2e-first" {
		t.Fatalf("lastRunId = %q", loaded.LastRunID)
	}
	if len(loaded.VerifiedFacts) != 1 || loaded.VerifiedFacts[0].CommentID != 500 {
		t.Fatalf("verified facts = %+v", loaded.VerifiedFacts)
	}

	// 4) 第二次生成：提示词里必须含第一次的事实（走真实 PG 读回，不是内存残留）。
	second := &fakeCompleter{output: aireply.CompletionOutput{Text: "依旧建议 20。", RunID: "run-e2e-second"}}
	generator2 := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, second, &recordingPGPoster{pool: pool}, nil,
	).WithCitations(store).WithTopicState(stateStore)
	if err := generator2.Generate(ctx, jobInput()); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	user := second.inputs[0].User
	if !strings.Contains(user, "此前已确认过的事实") || !strings.Contains(user, "池子设 20 就行") {
		t.Fatalf("the second prompt must carry the persisted fact:\n%s", user)
	}
}

// 三重失效第 3 条的端到端：被引用楼层转为不可见后，**存储里**那条事实也消失，
// 而不是只在注入时被跳过。
func TestGenerateEndToEndDropsHiddenFloorFromStoredState(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)
	stateStore := aireply.NewPostgresTopicStateStore(pool)

	first := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "依据 500 楼。[[cite:comment:500]]",
		RunID: "run-a",
	}}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, first, &recordingPGPoster{pool: pool}, nil,
	).WithCitations(store).WithTopicState(stateStore)
	if err := generator.Generate(ctx, jobInput()); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if loaded, ok := stateStore.Load(ctx, jobInput().TopicID, jobInput().BotUserID); !ok || len(loaded.VerifiedFacts) != 1 {
		t.Fatalf("the fact must be stored first: %+v ok=%v", loaded.VerifiedFacts, ok)
	}

	// 楼层被隐藏（等价于被删除/转不可见）。
	if _, err := pool.Exec(ctx, `UPDATE comments SET status = 'hidden' WHERE id = 500`); err != nil {
		t.Fatal(err)
	}

	second := &fakeCompleter{output: aireply.CompletionOutput{Text: "不知道了。", RunID: "run-b"}}
	generator2 := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, second, &recordingPGPoster{pool: pool}, nil,
	).WithCitations(store).WithTopicState(stateStore)
	if err := generator2.Generate(ctx, jobInput()); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if strings.Contains(second.inputs[0].User, "池子设 20") {
		t.Fatalf("a hidden floor must not be injected:\n%s", second.inputs[0].User)
	}
	// 关键：存储里也必须干净，否则下一轮又会读回来（只剔注入不剔存储）。
	final, ok := stateStore.Load(ctx, jobInput().TopicID, jobInput().BotUserID)
	if !ok {
		t.Fatal("the state document must survive a partial invalidation")
	}
	if len(final.VerifiedFacts) != 0 {
		t.Fatalf("the invalidated fact must be removed from storage, got %+v", final.VerifiedFacts)
	}
	if final.LastRunID != "run-b" {
		t.Fatalf("the state must still be updated by the second run: %q", final.LastRunID)
	}
}

// 发布失败 → 两张表都不许出现这个 run 的任何痕迹。
func TestGenerateEndToEndWritesNothingWhenPublishFails(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)
	stateStore := aireply.NewPostgresTopicStateStore(pool)

	completer := &fakeCompleter{output: aireply.CompletionOutput{
		Text:  "依据 500 楼。[[cite:comment:500]]",
		RunID: "run-failed",
	}}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, completer, &recordingPGPoster{pool: pool, fail: true}, nil,
	).WithCitations(store).WithTopicState(stateStore)
	if err := generator.Generate(ctx, jobInput()); err == nil {
		t.Fatal("a publish failure must be returned")
	}

	var citations int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_citations WHERE run_id = 'run-failed'`).Scan(&citations); err != nil {
		t.Fatal(err)
	}
	if citations != 0 {
		t.Fatalf("a failed publish must leave no citations: %d", citations)
	}
	if _, ok := stateStore.Load(ctx, jobInput().TopicID, jobInput().BotUserID); ok {
		t.Fatal("a failed publish must leave no topic state")
	}
}

// 文档形状：落库的 JSONB 能被下一次读取原样解析（字段名契约的运行时证据）。
func TestGenerateEndToEndStoresAParseableDocument(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	stateStore := aireply.NewPostgresTopicStateStore(pool)
	state := aireply.TopicState{
		LastRunID:     "run-shape",
		PromptVersion: "forum-reply@8",
		VerifiedFacts: []aireply.Fact{{
			TopicID: 10, CommentID: 500, Excerpt: "事实", VerifiedAt: time.Now().UTC(),
		}},
		LastReplies: []aireply.ReplyRef{{CommentID: 501, RunID: "run-shape", PostedAt: time.Now().UTC()}},
	}
	if err := stateStore.Save(ctx, 10, 77, state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT document FROM ai_topic_state WHERE topic_id = 10 AND bot_user_id = 77`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var decoded aireply.TopicState
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the stored document must be parseable: %v", err)
	}
	if decoded.LastRunID != "run-shape" || len(decoded.VerifiedFacts) != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if decoded.VerifiedFacts[0].Excerpt != "事实" {
		t.Fatalf("excerpt round-trip lost: %+v", decoded.VerifiedFacts[0])
	}
}
