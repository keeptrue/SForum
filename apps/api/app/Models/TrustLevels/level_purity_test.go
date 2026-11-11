package trustlevels_test

import (
	"context"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// countingTrustSource 同时实现 ActivityReader 与 PolicySource，并记录调用次数。
type countingTrustSource struct {
	activity      trustlevels.Activity
	policy        trustlevels.Policy
	activityCalls int
	policyCalls   int
}

func (s *countingTrustSource) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	s.activityCalls++
	return s.activity, nil
}

func (s *countingTrustSource) Policy(context.Context) (trustlevels.Policy, error) {
	s.policyCalls++
	return s.policy, nil
}

// TestLevelOfIsPure 是 E5 的补测：等级判定只由「统计 + 账龄 + 策略」决定，
// 一次 store 调用都不能有。
//
// 结构上分三步，缺一步这条测试就是空断言：
//  1. 对照组：先用 Resolver 走一次真实路径，证明计数器活着；
//  2. 纯度：重复调用 LevelOf，计数器必须停在 0；
//  3. 输入性：换一份更严的策略必须掉档，证明策略是入参而不是从 store 读的。
func TestLevelOfIsPure(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	fixedNow := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	createdAt := fixedNow.Add(-days(policy.NewUserDays + 30))
	activity := activityMeeting(policy, trustlevels.LevelLeader)

	source := &countingTrustSource{activity: activity, policy: policy}
	resolver := trustlevels.NewResolver(source, source).WithClock(func() time.Time { return fixedNow })

	// 1. 对照组：计数器是活的。
	level, _, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, createdAt)
	if err != nil {
		t.Fatalf("Grants 返回错误：%v", err)
	}
	if level != int(trustlevels.LevelLeader) {
		t.Fatalf("满活动的成年账号等级 = TL%d，期望 TL%d", level, trustlevels.LevelLeader)
	}
	if source.activityCalls != 1 || source.policyCalls != 1 {
		t.Fatalf("对照组没打到计数器：activity=%d policy=%d", source.activityCalls, source.policyCalls)
	}
	source.activityCalls, source.policyCalls = 0, 0

	// 2. 纯度：重复调用不得触碰任何 store 侧依赖。
	age := fixedNow.Sub(createdAt)
	first := trustlevels.LevelOf(activity, age, policy)
	for attempt := 0; attempt < 3; attempt++ {
		if got := trustlevels.LevelOf(activity, age, policy); got != first {
			t.Fatalf("第 %d 次 LevelOf = TL%d，首次 = TL%d（同一输入必须同一输出）", attempt+2, got, first)
		}
	}
	if source.activityCalls != 0 || source.policyCalls != 0 {
		t.Fatalf("LevelOf 触碰了 store：activity=%d policy=%d", source.activityCalls, source.policyCalls)
	}

	// 3. 策略是入参：同一份统计 + 更严的阈值必须掉到地板档。
	strict := trustlevels.DefaultPolicy()
	for level := trustlevels.LevelMember; level < trustlevels.LevelCount; level++ {
		strict.Levels[level] = trustlevels.Requirements{TopicCount: 1 << 30}
	}
	if got := trustlevels.LevelOf(activity, age, strict); got >= first {
		t.Fatalf("换严策略后 LevelOf = TL%d，仍 ≥ TL%d（策略没被当成输入）", got, first)
	}
	if source.activityCalls != 0 || source.policyCalls != 0 {
		t.Fatalf("换策略的判定触碰了 store：activity=%d policy=%d", source.activityCalls, source.policyCalls)
	}
}
