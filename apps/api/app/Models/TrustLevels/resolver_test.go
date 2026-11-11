package trustlevels_test

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

type stubActivityReader struct {
	activity trustlevels.Activity
	err      error
}

func (s stubActivityReader) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return s.activity, s.err
}

type stubPolicySource struct {
	policy trustlevels.Policy
	err    error
}

func (s stubPolicySource) Policy(context.Context) (trustlevels.Policy, error) {
	return s.policy, s.err
}

// TestResolverFailClosed 是 E 组断言：任何一步失败都必须返回错误，由装载方把
// Actor 标成「未解析」——绝不在失败时按高等级放行。
func TestResolverFailClosed(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	readErr := errors.New("user_activity timeout")
	createdAt := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		activity   trustlevels.ActivityReader
		policy     trustlevels.PolicySource
		wantIsKind error
	}{
		{
			name:     "统计读失败",
			activity: stubActivityReader{err: readErr},
			policy:   stubPolicySource{policy: policy},
		},
		{
			name:     "策略读失败",
			activity: stubActivityReader{},
			policy:   stubPolicySource{err: readErr},
		},
		{
			name:       "产品面关闭",
			activity:   stubActivityReader{},
			policy:     stubPolicySource{err: trustlevels.ErrDisabled},
			wantIsKind: trustlevels.ErrDisabled,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := trustlevels.NewResolver(testCase.activity, testCase.policy)
			level, earned, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, createdAt)
			if err == nil {
				t.Fatalf("期望错误，得到 level=%d earned=%v", level, earned)
			}
			if level != 0 || len(earned) != 0 {
				t.Fatalf("失败时必须返回零值，得到 level=%d earned=%v", level, earned)
			}
			if testCase.wantIsKind != nil && !errors.Is(err, testCase.wantIsKind) {
				t.Fatalf("期望 %v，得到 %v", testCase.wantIsKind, err)
			}
		})
	}
}

// TestResolverSkipsNonHumanAccounts 是 D25 的断言：机器人不进等级判定。
//
// 论坛限额层对 bot 有 IsBot() 旁路；如果 bot 也能挣到 TL4，就会出现
// 「不受限额 + 最高等级」的组合。
func TestResolverSkipsNonHumanAccounts(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	policy.Earnable = trustlevels.Earnable{trustlevels.Level(2): {"topic.create"}}
	activity := activityMeeting(policy, policy.LastRank())
	createdAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: activity},
		stubPolicySource{policy: policy},
	)
	for _, kind := range []identity.UserKind{identity.UserKindBot} {
		level, earned, err := resolver.Grants(context.Background(), 7, kind, createdAt)
		if err != nil {
			t.Fatalf("bot 不应报错：%v", err)
		}
		if level != 0 || len(earned) != 0 {
			t.Fatalf("bot 拿到了 level=%d earned=%v", level, earned)
		}
	}
	// 对照组：同样的输入，人类账号必须拿到最高等级（T10：守卫测试要先证明会误报）。
	level, _, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, createdAt)
	if err != nil || level != int(policy.LastRank()) {
		t.Fatalf("人类账号 level=%d err=%v，期望最高 rank %d", level, err, policy.LastRank())
	}
}

// TestResolverGrants 覆盖成功路径：等级来自纯函数，已挣得来自配置。
func TestResolverGrants(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	policy.Earnable = trustlevels.Earnable{
		trustlevels.Level(2): {"topic.create"},
	}
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		createdAt time.Time
		activity  trustlevels.Activity
		wantLevel int
		wantKeys  int
	}{
		{
			name:      "新人窗口内 = TL0",
			createdAt: now.Add(-24 * time.Hour),
			activity:  trustlevels.Activity{},
			wantLevel: 0,
		},
		{
			name:      "过了窗口 = 至少 TL1",
			createdAt: now.Add(-30 * 24 * time.Hour),
			activity:  trustlevels.Activity{},
			wantLevel: 1,
		},
		{
			// 用策略本身构造「刚好满足 TL4」的活动，阈值改动后测试字面量不会漂。
			name:      "满活动 + 老账号 = 最高 rank 且解锁配置的键",
			createdAt: now.Add(-365 * 24 * time.Hour),
			activity:  activityMeeting(policy, policy.LastRank()),
			wantLevel: int(policy.LastRank()),
			wantKeys:  1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := trustlevels.NewResolver(
				stubActivityReader{activity: testCase.activity},
				stubPolicySource{policy: policy},
			).WithClock(func() time.Time { return now })
			level, earned, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, testCase.createdAt)
			if err != nil {
				t.Fatalf("Grants: %v", err)
			}
			if level != testCase.wantLevel {
				t.Fatalf("level = %d，期望 %d", level, testCase.wantLevel)
			}
			if len(earned) != testCase.wantKeys {
				t.Fatalf("earned = %v，期望 %d 个", earned, testCase.wantKeys)
			}
		})
	}
}

// TestResolverNilDependenciesFailClosed 锁定「装配漏了」也不会放行。
func TestResolverNilDependenciesFailClosed(t *testing.T) {
	resolver := trustlevels.NewResolver(nil, nil)
	if _, _, err := resolver.Grants(context.Background(), 1, identity.UserKindHuman, time.Now()); !errors.Is(err, trustlevels.ErrDisabled) {
		t.Fatalf("空依赖应返回 ErrDisabled，得到 %v", err)
	}
	var nilResolver *trustlevels.Resolver
	if _, _, err := nilResolver.Grants(context.Background(), 1, identity.UserKindHuman, time.Now()); err == nil {
		t.Fatal("nil resolver 必须报错")
	}
}
