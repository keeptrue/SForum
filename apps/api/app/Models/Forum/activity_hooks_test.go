package forum

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeActivityExecer 捕获 hook 实际发出的 SQL 与参数，用来逐字断言
// 「哪个指标、加多少」——这是新 hook 唯一能被单测证明的部分。
type fakeActivityExecer struct {
	sqls  []string
	args  [][]any
	err   error
	calls int
}

func (f *fakeActivityExecer) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.sqls = append(f.sqls, sql)
	f.args = append(f.args, arguments)
	return pgconn.CommandTag{}, f.err
}

// TestBumpUserActivityUsesWhitelistedColumns 断言每个指标都落到对应列，
// 且列名只来自白名单（用户输入永远不进 SQL 模板）。
func TestBumpUserActivityUsesWhitelistedColumns(t *testing.T) {
	cases := map[string]string{
		activityMetricTopicCount:        "topic_count",
		activityMetricCommentCount:      "comment_count",
		activityMetricLikeReceivedCount: "like_received_count",
		activityMetricLikeGivenCount:    "like_given_count",
		activityMetricBookmarkCount:     "bookmark_count",
	}
	for metric, column := range cases {
		exec := &fakeActivityExecer{}
		if err := bumpUserActivity(context.Background(), exec, 42, metric, 1); err != nil {
			t.Fatalf("bumpUserActivity(%q): %v", metric, err)
		}
		if exec.calls != 1 {
			t.Fatalf("metric %q: exec calls = %d, want 1", metric, exec.calls)
		}
		if !strings.Contains(exec.sqls[0], "INSERT INTO user_activity (user_id, "+column+")") {
			t.Fatalf("metric %q: SQL does not target column %q: %s", metric, column, exec.sqls[0])
		}
		if !strings.Contains(exec.sqls[0], "user_activity."+column+" + $2") {
			t.Fatalf("metric %q: SQL does not accumulate column %q", metric, column)
		}
		if len(exec.args[0]) != 2 || exec.args[0][0] != int64(42) || exec.args[0][1] != int64(1) {
			t.Fatalf("metric %q: args = %v, want [42 1]", metric, exec.args[0])
		}
	}
}

// TestBumpUserActivityNoOps 断言匿名/系统动作与零增量都不发 SQL。
func TestBumpUserActivityNoOps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		userID int64
		delta  int64
	}{
		{"anonymous", 0, 1},
		{"negative user", -1, 1},
		{"zero delta", 42, 0},
	} {
		exec := &fakeActivityExecer{}
		if err := bumpUserActivity(context.Background(), exec, tc.userID, activityMetricTopicCount, tc.delta); err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if exec.calls != 0 {
			t.Fatalf("%s: exec calls = %d, want 0", tc.name, exec.calls)
		}
	}
}

// TestBumpUserActivityRejectsUnknownMetric 断言拼错的指标名直接报错而不是静默跳过——
// 静默会让计数悄悄缺失，那比崩溃更难查。
func TestBumpUserActivityRejectsUnknownMetric(t *testing.T) {
	exec := &fakeActivityExecer{}
	if err := bumpUserActivity(context.Background(), exec, 42, "posts_read_count", 1); err == nil {
		t.Fatal("expected error for unknown metric")
	}
	if exec.calls != 0 {
		t.Fatalf("exec calls = %d, want 0", exec.calls)
	}
}

// TestBumpUserActivityClampsNegativeDelta 断言回退路径（取消点赞/取消收藏）发的是负 delta，
// 钳零由 SQL 的 GREATEST 负责，Go 侧不自己夹取——否则 SQL 与 Go 会出现两套语义。
func TestBumpUserActivityClampsNegativeDelta(t *testing.T) {
	exec := &fakeActivityExecer{}
	if err := bumpUserActivity(context.Background(), exec, 42, activityMetricBookmarkCount, -1); err != nil {
		t.Fatal(err)
	}
	if len(exec.args[0]) != 2 || exec.args[0][1] != int64(-1) {
		t.Fatalf("args = %v, want delta -1", exec.args[0])
	}
	if !strings.Contains(exec.sqls[0], "GREATEST(user_activity.bookmark_count + $2, 0)") {
		t.Fatalf("SQL must clamp negative delta at zero: %s", exec.sqls[0])
	}
}

// TestActivityDeltaForReactionFollowsD3AndD4 固定点赞统计口径：
//   - D3 自赞：两个计数都不动（内容行 like_count 照常变，那是热度不是成就）；
//   - D4 取消点赞：两个计数一起回退。
func TestActivityDeltaForReactionFollowsD3AndD4(t *testing.T) {
	for _, tc := range []struct {
		name      string
		actor     int64
		recipient int64
		add       bool
		wantRecv  int64
		wantGiven int64
	}{
		{"like someone else", 1, 2, true, 1, 1},
		{"unlike someone else", 1, 2, false, -1, -1},
		{"self like ignored", 1, 1, true, 0, 0},
		{"self unlike ignored", 1, 1, false, 0, 0},
		{"unknown recipient", 1, 0, true, 0, 0},
		{"anonymous actor", 0, 2, true, 0, 0},
	} {
		received, given := activityDeltaForReaction(tc.actor, tc.recipient, tc.add)
		if received != tc.wantRecv || given != tc.wantGiven {
			t.Fatalf("%s: got (%d,%d), want (%d,%d)", tc.name, received, given, tc.wantRecv, tc.wantGiven)
		}
	}
}

// TestWritePathsCallActivityHooks 是挂载点的结构契约：四个写路径都必须真的调用
// bumpUserActivityTx，且指标与增量方向正确。行为正确性由 activity_hooks_test 的
// 单元断言 + M8 的集成验证共同保证，这里只防「有人把 hook 删了」。
func TestWritePathsCallActivityHooks(t *testing.T) {
	cases := []struct {
		file    string
		clauses []string
	}{
		{
			file: "postgres_store_comment_writes.go",
			clauses: []string{
				"bumpUserActivityTx(ctx, tx, input.AuthorUserID, activityMetricCommentCount, 1)",
			},
		},
		{
			file: "postgres_store_topic_mutations.go",
			clauses: []string{
				"bumpUserActivityTx(ctx, tx, input.AuthorUserID, activityMetricTopicCount, 1)",
			},
		},
		{
			file: "postgres_store_reactions.go",
			clauses: []string{
				"activityDeltaForReaction(input.ActorUserID, input.RecipientUserID, input.Add)",
				"bumpUserActivityTx(ctx, tx, input.RecipientUserID, activityMetricLikeReceivedCount, received)",
				"bumpUserActivityTx(ctx, tx, input.ActorUserID, activityMetricLikeGivenCount, given)",
			},
		},
		{
			file: "postgres_store_bookmarks.go",
			clauses: []string{
				"relationDelta: 1",
				"relationDelta: -1",
				"bumpUserActivityTx(ctx, tx, input.ActorUserID, activityMetricBookmarkCount, int64(applied.relationDelta))",
			},
		},
	}
	for _, tc := range cases {
		body, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		source := strings.Join(strings.Fields(string(body)), " ")
		for _, clause := range tc.clauses {
			if !strings.Contains(source, clause) {
				t.Fatalf("%s missing activity hook %q", tc.file, clause)
			}
		}
	}
}

// TestActivityHooksStayOutOfLegacyBaselineFile 断言统计 hook 没有长进
// postgres_store_ops.go：那个文件是 legacy 基线（cap 只减不增），
// 加一行就会把架构门禁顶红。
func TestActivityHooksStayOutOfLegacyBaselineFile(t *testing.T) {
	body, err := os.ReadFile("postgres_store_ops.go")
	if err != nil {
		t.Fatalf("read postgres_store_ops.go: %v", err)
	}
	for _, forbidden := range []string{"bumpUserActivity", "user_activity", "activityMetric"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("postgres_store_ops.go must not contain %q (legacy baseline, cap 只减不增)", forbidden)
		}
	}
}
