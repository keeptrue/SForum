package identity_test

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/internal/trustgate"
)

// zeroActivity 是「零活动」的行为统计源：本文件只关心等级闸与基线门，不关心指标。
type zeroActivity struct{}

func (zeroActivity) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return trustlevels.Activity{}, nil
}

// defaultPolicy 返回内置推荐阈值（NewUserDays=7，TL1 需 3 天访问 + 5 条回复）。
type defaultPolicy struct{}

func (defaultPolicy) Policy(context.Context) (trustlevels.Policy, error) {
	return trustlevels.DefaultPolicy(), nil
}

// disabledPolicy 模拟 features.trust_levels=disabled。
type disabledPolicy struct{}

func (disabledPolicy) Policy(context.Context) (trustlevels.Policy, error) {
	return trustlevels.Policy{}, trustlevels.ErrDisabled
}

func newGateResolver(policy trustlevels.PolicySource) *trustlevels.Resolver {
	return trustlevels.NewResolver(zeroActivity{}, policy)
}

func actorKeys(actor identity.Actor) []string {
	keys := make([]string, 0, len(actor.Permissions))
	for key := range actor.Permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestTrustGateDualLoaderKeySetsMatch 是 N-05：同一用户经两条 loader 得到的**权限键
// 集合逐字相等**（不是「都不为空」）。
//
// 两条路径今天各有一次「两份」风险：池路径合并 earned 并扣基线，事务路径曾经自己写
// 一份合并循环、且不读 created_at（本刀修复）。任何一侧漏改，这条就红。
func TestTrustGateDualLoaderKeySetsMatch(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()
	// 账龄 1 小时 + 零活动 ⇒ TL0。
	fixture.InsertMemberUser(t, 7, "human", time.Now().Add(-1*time.Hour))

	store := identity.NewPostgresStore(fixture.Pool).WithTrustLevelSource(newGateResolver(defaultPolicy{}))

	poolActor, err := store.LoadActor(ctx, 7)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	txActor, err := identity.LoadEffectiveActorTx(ctx, fixture.BeginTx(t), 7, newGateResolver(defaultPolicy{}))
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx: %v", err)
	}

	if !poolActor.TrustLevelResolved || poolActor.TrustLevel != 0 {
		t.Fatalf("池路径等级 = %d (resolved=%v)，期望 TL0", poolActor.TrustLevel, poolActor.TrustLevelResolved)
	}
	if !txActor.TrustLevelResolved || txActor.TrustLevel != 0 {
		t.Fatalf("事务路径等级 = %d (resolved=%v)，期望 TL0", txActor.TrustLevel, txActor.TrustLevelResolved)
	}
	if got, want := actorKeys(txActor), actorKeys(poolActor); !slices.Equal(got, want) {
		t.Fatalf("两条 loader 的权限键集合不一致：\n池路径 = %v\n事务路径 = %v", want, got)
	}
	// 门控本身：两个基线键都不在有效集里，而 post.create 仍在（TL0 仍能回帖）。
	for _, key := range trustlevels.BaselineGrants() {
		if poolActor.Can(key) {
			t.Fatalf("TL0 仍持有基线键 %q（池路径）", key)
		}
		if txActor.Can(key) {
			t.Fatalf("TL0 仍持有基线键 %q（事务路径）", key)
		}
	}
	if !poolActor.Can(identity.PermissionPostCreate) {
		t.Fatal("TL0 丢了 post.create：新用户将无法回帖（§1 死锁）")
	}
}

// TestTrustGateTxLoaderReadsCreatedAt 钉住本刀修复的前置缺陷：
// `LoadEffectiveActorTx` 曾不读 users.created_at，把它恒零值传给等级解析，
// 于是「账龄闸必过」→ 事务路径的每个账号都不低于 TL1。
//
// 红态（修复前）：同一 6 天账龄用户，池路径给 0，事务路径给 1。
func TestTrustGateTxLoaderReadsCreatedAt(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()
	// 账龄 6 天：仍在 trust.new_user_days=7 的窗口内，零活动 ⇒ 必须 TL0。
	fixture.InsertMemberUser(t, 8, "human", time.Now().Add(-6*24*time.Hour))

	store := identity.NewPostgresStore(fixture.Pool).WithTrustLevelSource(newGateResolver(defaultPolicy{}))
	poolActor, err := store.LoadActor(ctx, 8)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	if poolActor.TrustLevel != 0 {
		t.Fatalf("池路径把 6 天账龄的零活动用户判成 TL%d，期望 TL0", poolActor.TrustLevel)
	}

	txActor, err := identity.LoadEffectiveActorTx(ctx, fixture.BeginTx(t), 8, newGateResolver(defaultPolicy{}))
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx: %v", err)
	}
	if txActor.CreatedAt.IsZero() {
		t.Fatal("LoadEffectiveActorTx 没有读出 users.created_at：账龄闸会恒过，事务路径永远不低于 TL1")
	}
	if txActor.TrustLevel != poolActor.TrustLevel {
		t.Fatalf("两条 loader 对同一用户给出不同等级：池=%d 事务=%d（created_at 未读入？）",
			poolActor.TrustLevel, txActor.TrustLevel)
	}
}

// TestTrustGateSkipsWhenTrustLevelsDisabled：关闭开关 = 不做减法，七键全在。
func TestTrustGateSkipsWhenTrustLevelsDisabled(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()
	fixture.InsertMemberUser(t, 9, "human", time.Now().Add(-1*time.Hour))

	store := identity.NewPostgresStore(fixture.Pool).WithTrustLevelSource(newGateResolver(disabledPolicy{}))
	actor, err := store.LoadActor(ctx, 9)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	if actor.TrustLevelResolved {
		t.Fatal("关闭态必须保持未解析（D9/F 组判据）")
	}
	for _, key := range identity.SeedMemberPermissions {
		if !actor.Can(key) {
			t.Fatalf("关闭开关后丢了基线键 %q：关闭 = 不做减法", key)
		}
	}
}

// TestTrustGateSkipsBots：机器人不进人类信任阶梯，不扣基线键。
func TestTrustGateSkipsBots(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()
	fixture.InsertMemberUser(t, 10, "bot", time.Now().Add(-1*time.Hour))

	store := identity.NewPostgresStore(fixture.Pool).WithTrustLevelSource(newGateResolver(defaultPolicy{}))
	actor, err := store.LoadActor(ctx, 10)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	for _, key := range trustlevels.BaselineGrants() {
		if !actor.Can(key) {
			t.Fatalf("bot 被扣了基线键 %q：机器人没有登录凭据，AI 回复依赖这些键", key)
		}
	}
}

// TestTrustGateHonoursAllowOverride：显式 allow 覆盖的键不扣。
func TestTrustGateHonoursAllowOverride(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()
	fixture.InsertMemberUser(t, 11, "human", time.Now().Add(-1*time.Hour))
	fixture.AllowOverride(t, 11, identity.PermissionTopicCreate)

	store := identity.NewPostgresStore(fixture.Pool).WithTrustLevelSource(newGateResolver(defaultPolicy{}))
	actor, err := store.LoadActor(ctx, 11)
	if err != nil {
		t.Fatalf("LoadActor: %v", err)
	}
	if !actor.Can(identity.PermissionTopicCreate) {
		t.Fatal("显式 allow 的 topic.create 被减法门扣掉了：运营「手动放开某人」被静默推翻")
	}
	// 另一条没被 allow 的基线键仍然扣掉——否则「豁免」会退化成「整条门失效」。
	if actor.Can(identity.PermissionAttachmentUpload) {
		t.Fatal("只有 allow 的那一个键应被豁免，attachment.upload 仍必须被扣")
	}
}
