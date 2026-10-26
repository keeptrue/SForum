package aiderivedjobs_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	aiderivedjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/AIDerived"
	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	aidomain "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// 这一条覆盖 B3 的验收形态「手动触发一次，产物落成预期位置」：真实 River 客户端
// 入队 → 真实 worker 执行 → 真实 ai_derived_artifacts 落库。只有模型调用是桩，
// 因为测试不该花站点配额，也不该依赖供应商可用性（见任务书 D19）。
//
// 隔离策略：独立 schema + search_path + River 的 Schema 选项。River 的队列表也
// 建在这个 schema 里，因此不会碰到开发库的 river_job，也不会被开发库里的历史
// 任务干扰。
func TestDeriveJobEndToEndWritesDraftArtifact(t *testing.T) {
	ctx, pool, schema := newDerivedJobsHarness(t)

	store := aiderived.NewPostgresArtifactStore(pool)
	generator := aiderived.NewGenerator(
		stubTopics{topic: aidomain.TopicDetail{TopicSummary: aidomain.TopicSummary{ID: 42, Title: "连接池怎么配", Status: "active"}}},
		stubComments{items: aidomain.CommentList{}},
		&stubExecutor{result: supportai.CompletionResult{Text: "一句话结论：设 20。\n\n关键要点：\n- 按并发算"}},
		store,
		nil,
	)

	// 用生产的注册入口而不是手写一个 AddWorkerSafely：kind 覆盖必须与 bootstrap
	// 同源，否则测试会证明一个生产里不存在的注册组合。
	workers := river.NewWorkers()
	if err := aiderivedjobs.AddWorkers(workers, generator); err != nil {
		t.Fatalf("register workers: %v", err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Schema:  schema,
		Workers: workers,
		Queues:  map[string]river.QueueConfig{supportjobs.QueueAI: {MaxWorkers: 1}},
		// 测试里不要重试：一次执行就是我们想观察的那一次。
		FetchCooldown: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create river client: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := client.Start(runCtx); err != nil {
		t.Fatalf("start river client: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = client.Stop(stopCtx)
	})

	// 入队走真实的 Enqueuer：它同时验证「requestID 参与唯一键」与 queue/attempts。
	enqueuer := aiderivedjobs.NewEnqueuer(supportjobs.NewDispatcher(client))
	requestID, err := enqueuer.EnqueueDerive(ctx, aiderived.EnqueueInput{
		Purpose:     aiderived.PurposeDigest,
		TopicID:     42,
		RequestedBy: 1,
		RequestID:   "e2e-digest-1",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if requestID != "e2e-digest-1" {
		t.Fatalf("request id = %q", requestID)
	}

	artifact := waitForArtifact(t, ctx, pool, store, aiderived.PurposeDigest)
	if artifact.Status != aiderived.StatusDraft {
		t.Fatalf("status = %q, want draft (never auto-publish)", artifact.Status)
	}
	if artifact.TopicID != 42 {
		t.Fatalf("topic = %d", artifact.TopicID)
	}
	if artifact.PromptVer != supportai.CoreDigestPromptVersion {
		t.Fatalf("prompt version = %q", artifact.PromptVer)
	}
	if artifact.CreatedBy != 1 {
		t.Fatalf("requestedBy = %d", artifact.CreatedBy)
	}
	if !strings.Contains(artifact.Content, "关键要点") {
		t.Fatalf("content = %q", artifact.Content)
	}

	// 任务必须以 completed 收尾（不是 discarded/cancelled）：端到端成功一次。
	//
	// 这里必须轮询而不是立刻读：worker 在 Work 返回前后才把任务标记完成，而产物
	// 是在 Work 里写的——「产物已落库但任务仍是 running」是正常瞬间，不是缺陷。
	if state := waitForJobState(t, ctx, pool, "ai.forum_digest"); state != "completed" {
		t.Fatalf("job state = %q, want completed", state)
	}
}

// waitForJobState 轮询最近一条该 kind 的任务状态，直到离开 running。
func waitForJobState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM river_job WHERE kind = $1 ORDER BY id DESC LIMIT 1`, kind).Scan(&state); err != nil {
			t.Fatalf("read job state: %v", err)
		}
		if state != "running" && state != "available" && state != "pending" && state != "scheduled" {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("job stayed in %q for 20s", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 同一 requestID 重复投递被 River 去重：运营者的重试/双击不会产出两份草稿。
func TestDeriveJobDeduplicatesSameRequestID(t *testing.T) {
	ctx, pool, schema := newDerivedJobsHarness(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: schema})
	if err != nil {
		t.Fatalf("create river client: %v", err)
	}
	// 用真实的具体 args 类型入队：去重语义绑定的是 args 的序列化形状，
	// 用一个「载体」类型去测就测不到生产里真正入队的那份 JSON。
	args := aiderivedjobs.TourArgs{TopicID: 42, RequestID: "same-request"}
	options := args.EnqueueOptions().RiverInsertOpts()

	first, err := client.Insert(ctx, args, options)
	if err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if first.UniqueSkippedAsDuplicate {
		t.Fatal("the first insert must not be deduplicated")
	}
	second, err := client.Insert(ctx, args, options)
	if err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}
	if !second.UniqueSkippedAsDuplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("duplicate request must be deduplicated: first=%d second=%d skipped=%v",
			first.Job.ID, second.Job.ID, second.UniqueSkippedAsDuplicate)
	}

	// 换一个 requestID（运营者真的又点了一次）必须产生新任务——否则「重新生成
	// 一份」永远失败。
	other := aiderivedjobs.TourArgs{TopicID: 42, RequestID: "another-request"}
	third, err := client.Insert(ctx, other, other.EnqueueOptions().RiverInsertOpts())
	if err != nil {
		t.Fatalf("insert different request: %v", err)
	}
	if third.UniqueSkippedAsDuplicate || third.Job.ID == first.Job.ID {
		t.Fatalf("a new trigger must enqueue: first=%d third=%d skipped=%v",
			first.Job.ID, third.Job.ID, third.UniqueSkippedAsDuplicate)
	}
}

// waitForArtifact 轮询产物表直到任务落地，超时即失败并说明可能原因。
func waitForArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *aiderived.PostgresArtifactStore, purpose string) aiderived.Artifact {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		items, err := store.List(ctx, aiderived.ArtifactFilter{Purpose: purpose, Limit: 5})
		if err != nil {
			t.Fatalf("list artifacts: %v", err)
		}
		if len(items) > 0 {
			return items[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no artifact within 20s; river job errors: %s", riverJobErrors(ctx, pool))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// newDerivedJobsHarness 建隔离 schema，跑 River 迁移并建 ai_derived_artifacts。
//
// 它刻意用 rivermigrate 真跑 River 自己的迁移，而不是手抄队列表结构：手抄会与
// River 版本漂移，而这条测试的价值正是「真实队列能跑通」。
func newDerivedJobsHarness(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aiderived_jobs_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: schema})
	if err != nil {
		t.Fatalf("create river migrator: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("run river migrations: %v", err)
	}

	for _, statement := range []string{
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT NOT NULL)`,
		`INSERT INTO users (id, username) VALUES (1, 'alice')`,
		`CREATE TABLE ai_derived_artifacts (
			id BIGSERIAL PRIMARY KEY,
			purpose TEXT NOT NULL CHECK (purpose IN ('forum.digest', 'forum.tour')),
			topic_id BIGINT NOT NULL CHECK (topic_id > 0),
			run_id TEXT NOT NULL DEFAULT '',
			prompt_version TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'discarded')),
			content TEXT NOT NULL CHECK (content <> ''),
			created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup derived jobs schema: %v\n%s", err, statement)
		}
	}
	return ctx, pool, schema
}

// riverJobErrors 汇总本次运行里 river_job 的失败原因，让 e2e 失败能自我解释。
func riverJobErrors(ctx context.Context, pool *pgxpool.Pool) string {
	var summary string
	_ = pool.QueryRow(ctx, `
		SELECT COALESCE(string_agg(kind || ':' || state::text || ':' || COALESCE(errors::text, ''), ' | '), '(no jobs)')
		FROM river_job
	`).Scan(&summary)
	return summary
}
