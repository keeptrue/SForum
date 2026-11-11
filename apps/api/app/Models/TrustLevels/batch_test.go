package trustlevels_test

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// batchActivityFake 记录调用次数与入参 id，可选返回固定行/错误。
type batchActivityFake struct {
	calls  int
	lastID []int64
	rows   map[int64]trustlevels.Activity
	err    error
}

func (f *batchActivityFake) ActivitiesFor(_ context.Context, userIDs []int64) (map[int64]trustlevels.Activity, error) {
	f.calls++
	f.lastID = append([]int64(nil), userIDs...)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// batchAccountsFake 是 users 批量读入口的假实现。
type batchAccountsFake struct {
	calls int
	rows  map[int64]trustlevels.UserAccount
	err   error
}

func (f *batchAccountsFake) UserAccounts(_ context.Context, userIDs []int64) (map[int64]trustlevels.UserAccount, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// batchPolicyFake 是指定策略（或错误）的来源，并记录调用次数。
type batchPolicyFake struct {
	policy trustlevels.Policy
	err    error
	calls  int
}

func (f *batchPolicyFake) Policy(context.Context) (trustlevels.Policy, error) {
	f.calls++
	return f.policy, f.err
}

func fixedNow() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }

// TestBatchAuthorLevelsQueriesBatchOnce 是「禁止 N+1」的靶子：20 个重复 id 经去重后
// 只触发一次 users 查询 + 一次 activity 查询，并只产出一条等级。
func TestBatchAuthorLevelsQueriesBatchOnce(t *testing.T) {
	activity := &batchActivityFake{rows: map[int64]trustlevels.Activity{7: {DaysVisited: 30}}}
	accounts := &batchAccountsFake{rows: map[int64]trustlevels.UserAccount{
		7: {CreatedAt: fixedNow().Add(-400 * 24 * time.Hour), Kind: identity.UserKindHuman},
	}}
	policy := &batchPolicyFake{policy: trustlevels.DefaultPolicy()}
	resolver := trustlevels.NewBatchResolver(activity, accounts, policy).
		WithClock(fixedNow)

	ids := make([]int64, 0, 20)
	for i := 0; i < 20; i++ {
		ids = append(ids, 7)
	}
	levels, err := resolver.AuthorLevels(context.Background(), ids)
	if err != nil {
		t.Fatalf("AuthorLevels: %v", err)
	}
	if activity.calls != 1 || accounts.calls != 1 || policy.calls != 1 {
		t.Fatalf("查询次数 = activity:%d accounts:%d policy:%d，期望各 1（禁止 N+1）",
			activity.calls, accounts.calls, policy.calls)
	}
	if len(activity.lastID) != 1 || activity.lastID[0] != 7 {
		t.Fatalf("批量入参没有去重：%v", activity.lastID)
	}
	if len(levels) != 1 || levels[7].LevelSlug == "" || levels[7].Color == "" {
		t.Fatalf("等级结果形状不对：%+v", levels)
	}
}

// TestBatchAuthorLevelsOmitsUnavailableAuthors 覆盖逐条缺席：无账号行、无统计行、
// 机器人都不产出条目（契约：不渲染徽章，而不是渲染成 TL0）。
func TestBatchAuthorLevelsOmitsUnavailableAuthors(t *testing.T) {
	created := fixedNow().Add(-400 * 24 * time.Hour)
	activity := &batchActivityFake{rows: map[int64]trustlevels.Activity{
		1: {DaysVisited: 30},
		3: {DaysVisited: 30}, // 机器人有统计行，但也不该有等级
	}}
	accounts := &batchAccountsFake{rows: map[int64]trustlevels.UserAccount{
		1: {CreatedAt: created, Kind: identity.UserKindHuman},
		2: {CreatedAt: created, Kind: identity.UserKindHuman}, // 有账号、无统计行
		3: {CreatedAt: created, Kind: identity.UserKindBot},
		// 4 完全没有账号行（作者已删）
	}}
	resolver := trustlevels.NewBatchResolver(activity, accounts, &batchPolicyFake{policy: trustlevels.DefaultPolicy()}).
		WithClock(fixedNow)

	levels, err := resolver.AuthorLevels(context.Background(), []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatalf("AuthorLevels: %v", err)
	}
	if _, ok := levels[1]; !ok {
		t.Fatal("正常作者应有等级")
	}
	for _, id := range []int64{2, 3, 4} {
		if _, ok := levels[id]; ok {
			t.Fatalf("id=%d 不应产出等级（缺席语义）", id)
		}
	}
}

// TestBatchAuthorLevelsColorsAtRankBoundaries 锁定 rank 边界颜色：
// 新人（近期注册 + 无统计）TL0 = slate，满活动老账号顶档 TL8 = amber。
func TestBatchAuthorLevelsColorsAtRankBoundaries(t *testing.T) {
	policy := eightTierPolicy(t)
	now := fixedNow()
	activity := &batchActivityFake{rows: map[int64]trustlevels.Activity{
		1: {},
		2: {TopicCount: 500},
	}}
	accounts := &batchAccountsFake{rows: map[int64]trustlevels.UserAccount{
		1: {CreatedAt: now.Add(-24 * time.Hour), Kind: identity.UserKindHuman},
		2: {CreatedAt: now.Add(-400 * 24 * time.Hour), Kind: identity.UserKindHuman},
	}}
	resolver := trustlevels.NewBatchResolver(activity, accounts, &batchPolicyFake{policy: policy}).
		WithClock(func() time.Time { return now })

	levels, err := resolver.AuthorLevels(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatalf("AuthorLevels: %v", err)
	}
	if got := levels[1]; got.Level != trustlevels.LevelNewcomer || got.LevelSlug != "newcomer" || got.Color != "slate" {
		t.Fatalf("新人等级 = %+v，期望 TL0/newcomer/slate", got)
	}
	if got := levels[2]; got.Level != trustlevels.MaxLevel || got.Color != "amber" {
		t.Fatalf("顶档等级 = %+v，期望 TL%d/amber", got, trustlevels.MaxLevel)
	}
	if levels[2].LevelSlug == "" || levels[2].LevelSlug == "newcomer" {
		t.Fatalf("顶档 levelSlug = %q，期望实际档位 id", levels[2].LevelSlug)
	}
}

// TestBatchAuthorLevelsDisabledSkipsQueries 锁定开关关闭路径：策略返回 ErrDisabled 时
// 一次数据查询都不发生（开关关闭的请求不该为徽章多打两条 SQL）。
func TestBatchAuthorLevelsDisabledSkipsQueries(t *testing.T) {
	activity := &batchActivityFake{}
	accounts := &batchAccountsFake{}
	resolver := trustlevels.NewBatchResolver(activity, accounts,
		&batchPolicyFake{err: trustlevels.ErrDisabled})

	_, err := resolver.AuthorLevels(context.Background(), []int64{1, 2})
	if !errors.Is(err, trustlevels.ErrDisabled) {
		t.Fatalf("err = %v，期望 ErrDisabled", err)
	}
	if activity.calls != 0 || accounts.calls != 0 {
		t.Fatalf("关闭态仍查询：activity=%d accounts=%d", activity.calls, accounts.calls)
	}
}

// TestBatchAuthorLevelsPropagatesQueryErrors 保证查询失败原样上抛（由读面 fail-open）。
func TestBatchAuthorLevelsPropagatesQueryErrors(t *testing.T) {
	boom := errors.New("boom")
	accounts := &batchAccountsFake{err: boom}
	resolver := trustlevels.NewBatchResolver(&batchActivityFake{}, accounts,
		&batchPolicyFake{policy: trustlevels.DefaultPolicy()})
	if _, err := resolver.AuthorLevels(context.Background(), []int64{1}); !errors.Is(err, boom) {
		t.Fatalf("err = %v，期望透传查询错误", err)
	}
}

// eightTierPolicy 造一份 8 档策略（TL1 起阈值递增），让顶档真的能到 TL8。
func eightTierPolicy(t *testing.T) trustlevels.Policy {
	t.Helper()
	raw := `{"schemaVersion":2,"tiers":[
		{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{"topic_count":5},"limits":{},"earnable":[]},
		{"id":"resident","name":{"zh":"乙","en":"B"},"icon":"home","requirements":{"topic_count":10},"limits":{},"earnable":[]},
		{"id":"veteran","name":{"zh":"丙","en":"C"},"icon":"mountain","requirements":{"topic_count":20},"limits":{},"earnable":[]},
		{"id":"leader","name":{"zh":"丁","en":"D"},"icon":"crown","requirements":{"topic_count":40},"limits":{},"earnable":[]},
		{"id":"tier_6","name":{"zh":"戊","en":"E"},"icon":"star","requirements":{"topic_count":60},"limits":{},"earnable":[]},
		{"id":"tier_7","name":{"zh":"己","en":"F"},"icon":"sun","requirements":{"topic_count":80},"limits":{},"earnable":[]},
		{"id":"tier_8","name":{"zh":"庚","en":"G"},"icon":"moon","requirements":{"topic_count":100},"limits":{},"earnable":[]},
		{"id":"tier_9","name":{"zh":"辛","en":"H"},"icon":"flame","requirements":{"topic_count":200},"limits":{},"earnable":[]}]}`
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if policy.LastRank() != trustlevels.MaxLevel {
		t.Fatalf("前置条件失败：LastRank = %d", policy.LastRank())
	}
	return policy
}
