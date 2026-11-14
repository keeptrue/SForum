package bootstrap

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	badgesjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Badges"
	trustlevelsjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/TrustLevels"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// recordingRiverClient 记录每次 Insert 的 args，并可按 kind 注入失败。
type recordingRiverClient struct {
	kinds    []string
	inserts  int
	failKind string
}

func (c *recordingRiverClient) Insert(_ context.Context, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c.inserts++
	c.kinds = append(c.kinds, args.Kind())
	if c.failKind != "" && args.Kind() == c.failKind {
		return nil, context.DeadlineExceeded
	}
	return &rivertype.JobInsertResult{}, nil
}

func (c *recordingRiverClient) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, context.DeadlineExceeded
}

func (c *recordingRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, context.DeadlineExceeded
}

// TestEnqueueUserEvaluationsEnqueuesBothJobs 钉住「一次触发 = 两个派生各一次」。
//
// 这个断言存在的理由：6 个调用点表达的是「用户状态变了」。若这里只入队徽章，
// 信任等级变更事件就再也不会发——而那是静默失效（账本还在动，事件没了）。
func TestEnqueueUserEvaluationsEnqueuesBothJobs(t *testing.T) {
	client := &recordingRiverClient{}
	enqueuer := newUserEvaluationEnqueuer(supportjobs.NewDispatcher(client), nil)

	enqueuer.EnqueueUserEvaluations(context.Background(), 42)

	if len(client.kinds) != 2 {
		t.Fatalf("入队 %d 次（%v），期望徽章与信任等级各一次", len(client.kinds), client.kinds)
	}
	if client.kinds[0] != badgesjobs.EvaluateUserKind {
		t.Fatalf("第 1 个 job = %q, want %q", client.kinds[0], badgesjobs.EvaluateUserKind)
	}
	if client.kinds[1] != trustlevelsjobs.RecomputeUserKind {
		t.Fatalf("第 2 个 job = %q, want %q", client.kinds[1], trustlevelsjobs.RecomputeUserKind)
	}
}

// TestEnqueueUserEvaluationsKeepsJobsIndependent 钉住「一个失败不吞另一个」。
//
// 两条去重维度不同、队列也不同：徽章那条抖动没有理由让等级重算一起丢。
func TestEnqueueUserEvaluationsKeepsJobsIndependent(t *testing.T) {
	badgeFails := &recordingRiverClient{failKind: badgesjobs.EvaluateUserKind}
	newUserEvaluationEnqueuer(supportjobs.NewDispatcher(badgeFails), nil).
		EnqueueUserEvaluations(context.Background(), 42)
	if badgeFails.inserts != 2 {
		t.Fatalf("徽章入队失败后仍必须尝试信任等级，inserts = %d", badgeFails.inserts)
	}
	if badgeFails.kinds[1] != trustlevelsjobs.RecomputeUserKind {
		t.Fatalf("第二次入队 = %q, want trustlevels", badgeFails.kinds[1])
	}

	trustFails := &recordingRiverClient{failKind: trustlevelsjobs.RecomputeUserKind}
	newUserEvaluationEnqueuer(supportjobs.NewDispatcher(trustFails), nil).
		EnqueueUserEvaluations(context.Background(), 42)
	if trustFails.inserts != 2 {
		t.Fatalf("信任等级入队失败后仍必须先试徽章，inserts = %d", trustFails.inserts)
	}
	if trustFails.kinds[0] != badgesjobs.EvaluateUserKind {
		t.Fatalf("第一次入队 = %q, want badges", trustFails.kinds[0])
	}
}
