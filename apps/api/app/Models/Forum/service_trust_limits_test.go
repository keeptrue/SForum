package forum

import (
	"context"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// trustLadderPolicy 是可配置的信任策略 fake，用于覆盖五道判据与分档限额。
type trustLadderPolicy struct {
	days          int
	topicCooldown int
	dailyTopic    int
	levelLimits   map[int]TrustLevelLimits
}

func (p trustLadderPolicy) NewUserTrustDays(context.Context) (int, error) { return p.days, nil }
func (p trustLadderPolicy) NewUserTopicCooldownSeconds(context.Context) (int, error) {
	return p.topicCooldown, nil
}
func (p trustLadderPolicy) NewUserCommentCooldownSeconds(context.Context) (int, error) {
	return 0, nil
}
func (p trustLadderPolicy) NewUserDailyTopicLimit(context.Context) (int, error) { return p.dailyTopic, nil }
func (p trustLadderPolicy) NewUserDailyCommentLimit(context.Context) (int, error) {
	return 0, nil
}
func (p trustLadderPolicy) NewUserForbidOutboundLinks(context.Context) (bool, error) {
	return true, nil
}
func (p trustLadderPolicy) LevelLimits(_ context.Context, level int) (TrustLevelLimits, error) {
	return p.levelLimits[level], nil
}



// TestLimitsForActorKeepsFiveCriteria 是 T15 的收编断言：五道判据逐条保留。
//
// 少任何一条都会造出「昨天注册的新版主被新人限流」「bot 被冷却卡住」这类事故。
func TestLimitsForActorKeepsFiveCriteria(t *testing.T) {
	ctx := context.Background()
	policy := trustLadderPolicy{days: 7, topicCooldown: 300, dailyTopic: 3}
	trust := TrustPolicyResolver(policy)
	fresh := time.Now().Add(-time.Hour)

	cases := []struct {
		name    string
		actor   identity.Actor
		wantOn  bool
		comment string
	}{
		{
			name:    "判据 1：零值 CreatedAt 跳过",
			actor:   identity.Actor{ID: 1, Status: identity.UserStatusActive},
			wantOn:  false,
			comment: "未知注册时间不能当成新人",
		},
		{
			name: "判据 2：bot 跳过",
			actor: identity.Actor{
				ID: 2, Status: identity.UserStatusActive, Kind: identity.UserKindBot,
				CreatedAt: fresh, TrustLevel: 0, TrustLevelResolved: true,
			},
			wantOn:  false,
			comment: "bot 没有登录凭据，AI 配额与审核闸门在管它",
		},
		{
			name: "判据 3：员工豁免",
			actor: identity.Actor{
				ID: 3, Status: identity.UserStatusActive, CreatedAt: fresh,
				Permissions: map[string]bool{identity.PermissionSettingsManage: true},
				TrustLevel:  0, TrustLevelResolved: true,
			},
			wantOn:  false,
			comment: "昨天注册的版主不能被新人限流",
		},
		{
			name: "判据 4：TL0 用新人七键",
			actor: identity.Actor{
				ID: 4, Status: identity.UserStatusActive, CreatedAt: fresh,
				TrustLevel: 0, TrustLevelResolved: true,
			},
			wantOn:  true,
			comment: "已解析的 TL0 就是新成员",
		},
		{
			name: "判据 4：TL1 用站点默认",
			actor: identity.Actor{
				ID: 5, Status: identity.UserStatusActive, CreatedAt: fresh,
				TrustLevel: 1, TrustLevelResolved: true,
			},
			wantOn:  true,
			comment: "TL1 起不再受新人收紧值约束",
		},
		{
			name: "判据 5：未解析且年龄在窗口内 = 新人",
			actor: identity.Actor{
				ID: 6, Status: identity.UserStatusActive, CreatedAt: fresh,
			},
			wantOn:  true,
			comment: "开关关闭时回落到本刀之前的年龄两态",
		},
		{
			name: "判据 5：未解析且年龄超出窗口 = 不受限",
			actor: identity.Actor{
				ID: 7, Status: identity.UserStatusActive, CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
			},
			wantOn:  false,
			comment: "过了 trust.new_user_days 就不再是新人",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			limits := limitsForActor(ctx, trust, testCase.actor)
			if limits.active != testCase.wantOn {
				t.Fatalf("active = %v，期望 %v（%s）", limits.active, testCase.wantOn, testCase.comment)
			}
		})
	}
}

// TestLimitsForActorDaysZeroDisablesLadder 锁定判据 5 的 days<=0 分支：
// 0 表示「关掉整条新人阶梯」。
func TestLimitsForActorDaysZeroDisablesLadder(t *testing.T) {
	trust := TrustPolicyResolver(trustLadderPolicy{days: 0, topicCooldown: 300, dailyTopic: 3})
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, CreatedAt: time.Now().Add(-time.Hour)}
	if limits := limitsForActor(context.Background(), trust, actor); limits.active {
		t.Fatal("days=0 时新人阶梯应整体关闭")
	}
}

// TestLimitsForActorLevelOneHasNoNewUserTightening 锁定 D2：
// TL1 不再带新人收紧值，否则「升级」等于没升。
func TestLimitsForActorLevelOneHasNoNewUserTightening(t *testing.T) {
	trust := TrustPolicyResolver(trustLadderPolicy{days: 7, topicCooldown: 300, dailyTopic: 3})
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive, CreatedAt: time.Now().Add(-time.Hour),
		TrustLevel: 1, TrustLevelResolved: true,
	}
	limits := limitsForActor(context.Background(), trust, actor)
	if !limits.active {
		t.Fatal("TL1 应当 active（用于应用分档覆盖）")
	}
	if limits.topicCooldown != 0 || limits.dailyTopic != 0 {
		t.Fatalf("TL1 仍带新人收紧值：cooldown=%d daily=%d", limits.topicCooldown, limits.dailyTopic)
	}
	if limits.overrideTopicCooldown != nil {
		t.Fatal("未配置的档位不应有覆盖值")
	}
}

// TestResolveLimit 锁定 D27 的优先级：显式覆盖 > 新人收紧 > 站点默认。
func TestResolveLimit(t *testing.T) {
	zero := 0
	relaxed := 600
	cases := []struct {
		name     string
		base     int
		tighten  int
		override *int
		want     int
	}{
		{"都没有 → 站点默认", 60, 0, nil, 60},
		{"新人更严 → 取新人", 60, 300, nil, 300},
		{"新人更松 → 不生效（只能更严）", 300, 60, nil, 300},
		{"站点默认关闭 + 新人收紧 → 取新人", 0, 300, nil, 300},
		{"显式 0 = 免冷却，压过新人收紧", 60, 300, &zero, 0},
		{"显式放宽压过新人收紧", 60, 300, &relaxed, 600},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolveLimit(testCase.base, testCase.tighten, testCase.override); got != testCase.want {
				t.Fatalf("resolveLimit = %d，期望 %d", got, testCase.want)
			}
		})
	}
}
