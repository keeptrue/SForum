package trustlevels_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件守的是这次改造的**核心不变量**（D-N1）：身份与序号分离。
//
// 档位的身份是不可变 id，序号是 rank（active 档位在数组里的位次，TL1 起）。
// 只要「插入一档 → 后面每档的 rank 顺移、身份与配置不动」成立，运营就能随意
// 插档/改名/停用而不会把已挣得权限、契约字段、审计记录指向错误的档位。

// policyConfig 把策略转成可编辑的 Config 结构（测试里当「后台读出来的配置」用）。
func policyConfig(t *testing.T, policy trustlevels.Policy) trustlevels.Config {
	t.Helper()
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	var config trustlevels.Config
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		t.Fatalf("反序列化 Config: %v", err)
	}
	return config
}

// decodeConfig 把 Config 结构编回 JSON 并严格解码（模拟一次「保存」）。
func decodeConfig(t *testing.T, config trustlevels.Config) trustlevels.Policy {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("序列化 Config: %v", err)
	}
	policy, err := trustlevels.DecodeConfig(string(encoded))
	if err != nil {
		t.Fatalf("DecodeConfig(%s): %v", encoded, err)
	}
	return policy
}

// newCustomTierConfig 造一条「新档」（id 省略，序号由服务端派生）。
func newCustomTierConfig(name string, requirement trustlevels.Requirements) trustlevels.TierConfig {
	return trustlevels.TierConfig{
		Name:         trustlevels.LocalizedName{Zh: name, En: name},
		Requirements: requirement,
		Limits:       json.RawMessage("null"),
		Earnable:     json.RawMessage(`["post.edit_own"]`),
	}
}

// TestInsertTierKeepsFollowingIdentities 是最重要的一条：在第 k 位插一档之后，
// 原来第 k 档的 requirements / limits / earnable 仍然属于**同一个 id**。
func TestInsertTierKeepsFollowingIdentities(t *testing.T) {
	// 先把默认策略走一遍「编码 → 解码」，让两侧的比较基准都是规范形状
	// （否则 nil 与 []string{} 的差别会伪装成「配置变了」）。
	base := decodeConfig(t, policyConfig(t, trustlevels.DefaultPolicy()))
	config := policyConfig(t, base)

	// 记住插入点之后每一档的「身份 + 全部配置」。
	type snapshot struct {
		id           string
		requirements trustlevels.Requirements
		limits       trustlevels.LevelLimits
		earnable     []string
	}
	var before []snapshot
	for rank := trustlevels.Level(2); rank <= base.LastRank(); rank++ {
		tier, ok := base.TierAt(rank)
		if !ok {
			t.Fatalf("前置条件失败：TL%d 不存在", rank)
		}
		before = append(before, snapshot{
			id:           tier.ID,
			requirements: tier.Requirements,
			limits:       tier.Limits,
			earnable:     tier.Earnable,
		})
	}

	// 在 index 1（原 TL2 之前）插入一档。
	inserted := newCustomTierConfig("插队档", trustlevels.Requirements{TopicCount: 7})
	config.Tiers = append(config.Tiers[:1], append([]trustlevels.TierConfig{inserted}, config.Tiers[1:]...)...)
	policy := decodeConfig(t, config)

	if got, want := policy.LastRank(), base.LastRank()+1; got != want {
		t.Fatalf("插档后 LastRank = %d，期望 %d", got, want)
	}
	// 新档拿到派生 id，并且落在 rank 2。
	if got := policy.TierID(2); got != "tier_6" {
		t.Fatalf("新档 id = %q，期望 tier_6（内置占 1..5）", got)
	}
	if got := policy.RequirementsOf(2).TopicCount; got != 7 {
		t.Fatalf("新档 topic_count = %d，期望 7", got)
	}
	// 原第 2..4 档顺移到 3..5，但身份与配置逐项不变。
	for offset, want := range before {
		rank := trustlevels.Level(offset + 3)
		tier, ok := policy.TierAt(rank)
		if !ok {
			t.Fatalf("插档后缺少 rank %d", rank)
		}
		if tier.ID != want.id {
			t.Fatalf("rank %d 的身份变成了 %q，期望 %q（插入不该改写身份）", rank, tier.ID, want.id)
		}
		if tier.Requirements != want.requirements {
			t.Fatalf("%s 的 requirements 变了：%+v → %+v", want.id, want.requirements, tier.Requirements)
		}
		if !reflect.DeepEqual(tier.Limits, want.limits) {
			t.Fatalf("%s 的 limits 变了", want.id)
		}
		if !reflect.DeepEqual(tier.Earnable, want.earnable) {
			t.Fatalf("%s 的 earnable 变了", want.id)
		}
	}
	// 已挣得映射也按 rank 跟着档位走，而不是留在原来的位置上。
	if got := policy.Earnable.Grants(2); len(got) != 1 || got[0] != "post.edit_own" {
		t.Fatalf("新档的已挣得 = %v，期望 [post.edit_own]", got)
	}
}

// TestRetireThenRestoreKeepsRankAndConfig 锁定软停用：停用让档位从判定与编号里消失，
// 配置全量保留；恢复后回到原来的 rank、配置一项不丢。
func TestRetireThenRestoreKeepsRankAndConfig(t *testing.T) {
	base := decodeConfig(t, policyConfig(t, trustlevels.DefaultPolicy()))
	config := policyConfig(t, base)
	// 停用 resident（原 rank 2）。
	config.Tiers[1].Retired = true

	retired := decodeConfig(t, config)
	if got, want := retired.LastRank(), base.LastRank()-1; got != want {
		t.Fatalf("停用后 LastRank = %d，期望 %d", got, want)
	}
	if id := retired.TierID(2); id != "veteran" {
		t.Fatalf("停用 resident 后 rank 2 = %q，期望顺移上来的 veteran", id)
	}
	if _, ok := retired.TierAt(2); !ok {
		t.Fatal("停用后缺少顺移档位")
	}
	// 停用档位的配置仍然完整地留在数组里（不是被删掉）。
	var retiredTier trustlevels.Tier
	for _, tier := range retired.Tiers {
		if tier.ID == "resident" {
			retiredTier = tier
		}
	}
	if retiredTier.ID != "resident" || !retiredTier.Retired {
		t.Fatal("停用档位没有以 retired:true 保留")
	}
	if retiredTier.Requirements != base.Tiers[1].Requirements {
		t.Fatal("停用档位的 requirements 被清掉了")
	}

	// 恢复：把 retired 翻回 false，应当回到原 rank 且配置逐项不变。
	restoredConfig := policyConfig(t, retired)
	for index := range restoredConfig.Tiers {
		if restoredConfig.Tiers[index].ID == "resident" {
			restoredConfig.Tiers[index].Retired = false
		}
	}
	restored := decodeConfig(t, restoredConfig)
	if got, want := restored.LastRank(), base.LastRank(); got != want {
		t.Fatalf("恢复后 LastRank = %d，期望 %d", got, want)
	}
	restoredTier, ok := restored.TierAt(2)
	if !ok || restoredTier.ID != "resident" {
		t.Fatalf("恢复后 rank 2 = %+v，期望 resident", restoredTier)
	}
	if restoredTier.Requirements != base.Tiers[1].Requirements {
		t.Fatal("恢复后 requirements 与停用前不一致")
	}
}

// TestTierIDIsNeverReused 锁定 D-N6：只软删除，所以派生序号只前进、不复用。
func TestTierIDIsNeverReused(t *testing.T) {
	base := trustlevels.DefaultPolicy()
	config := policyConfig(t, base)
	// 循环两轮：每轮停用上一轮新增的档位，再追加一个新档。
	policy := base
	previousID := ""
	for round := 0; round < 2; round++ {
		config = policyConfig(t, policy)
		for index := range config.Tiers {
			if previousID != "" && config.Tiers[index].ID == previousID {
				config.Tiers[index].Retired = true
			}
		}
		config.Tiers = append(config.Tiers, newCustomTierConfig("新档", trustlevels.Requirements{DaysVisited: 5}))
		policy = decodeConfig(t, config)

		id := ""
		for _, tier := range policy.Tiers {
			if !tier.Retired && !trustlevels.IsBuiltinTierID(tier.ID) {
				id = tier.ID
			}
		}
		if id == "" {
			t.Fatalf("第 %d 轮没有找到新增档位", round+1)
		}
		if id == previousID {
			t.Fatalf("第 %d 轮的 id 复用了 %q", round+1, id)
		}
		if round == 0 && id != "tier_6" {
			t.Fatalf("第一个新档 id = %q，期望 tier_6", id)
		}
		if round == 1 && id != "tier_7" {
			t.Fatalf("第二个新档 id = %q，期望 tier_7（tier_6 停用后不复用）", id)
		}
		previousID = id
	}
}

// TestRanksAreContiguous 锁定 rank 连续：1..LastRank 每档都能取到，再往上没有。
func TestRanksAreContiguous(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	active := policy.ActiveTiers()
	if len(active) == 0 {
		t.Fatal("前置条件失败：默认策略没有 active 档位")
	}
	if int(policy.LastRank()) != len(active) {
		t.Fatalf("LastRank = %d，active 档数 = %d", policy.LastRank(), len(active))
	}
	for rank := trustlevels.LevelMember; rank <= policy.LastRank(); rank++ {
		tier, ok := policy.TierAt(rank)
		if !ok {
			t.Fatalf("rank %d 取不到档位", rank)
		}
		if tier.ID != active[rank-1].ID {
			t.Fatalf("rank %d 的档位 = %q，ActiveTiers 顺序里是 %q", rank, tier.ID, active[rank-1].ID)
		}
	}
	if _, ok := policy.TierAt(policy.LastRank() + 1); ok {
		t.Fatal("LastRank+1 不该存在")
	}
}

// TestMaxTiersRanksAreContiguous 覆盖 8 档上限：1..8 连续、第 9 档取不到。
func TestMaxTiersRanksAreContiguous(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(newTiersJSON(trustlevels.MaxTiers))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if policy.LastRank() != trustlevels.MaxLevel {
		t.Fatalf("LastRank = %d，期望 %d", policy.LastRank(), trustlevels.MaxLevel)
	}
	for rank := trustlevels.LevelMember; rank <= trustlevels.MaxLevel; rank++ {
		if _, ok := policy.TierAt(rank); !ok {
			t.Fatalf("8 档配置缺少 rank %d", rank)
		}
	}
	if _, ok := policy.TierAt(trustlevels.MaxLevel + 1); ok {
		t.Fatal("第 9 档不该存在")
	}
}

// TestRankZeroIsOwnedByTL0 锁定 TL0 的保留位：它不占 Tiers，也不受配置影响。
func TestRankZeroIsOwnedByTL0(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	if _, ok := policy.TierAt(trustlevels.LevelNewcomer); ok {
		t.Fatal("TL0 不该出现在可配置档位里")
	}
	if got := policy.TierID(trustlevels.LevelNewcomer); got != trustlevels.NewcomerID {
		t.Fatalf("TL0 身份 = %q，期望 newcomer", got)
	}
	if got := policy.RequirementsOf(trustlevels.LevelNewcomer); !got.IsZero() {
		t.Fatalf("TL0 不该有阈值：%+v", got)
	}
	if got := policy.DisplayNameOf(trustlevels.LevelNewcomer); got.IsZero() {
		t.Fatal("TL0 必须有内置名（UI 要显示它）")
	}
	// 停用第一档也不会把 rank 0 让出来：rank 0 永远是 TL0。
	config := policyConfig(t, policy)
	config.Tiers[0].Retired = true
	retired := decodeConfig(t, config)
	if _, ok := retired.TierAt(trustlevels.LevelNewcomer); ok {
		t.Fatal("停用一档后 TL0 仍然不该是可配置档位")
	}
	if got := retired.TierID(trustlevels.LevelMember); got != "resident" {
		t.Fatalf("停用第一档后 rank 1 = %q，期望 resident", got)
	}
}

// TestEncodeRetiredTierKeepsEarnable 锁定「停用档位的 earnable 不会被 Encode 清空」：
// 它不在 rank 映射里，必须直接从 Tier.Earnable 写出，否则恢复时运营要重新勾一遍。
func TestEncodeRetiredTierKeepsEarnable(t *testing.T) {
	config := policyConfig(t, trustlevels.DefaultPolicy())
	config.Tiers[1].Retired = true
	config.Tiers[1].Earnable = json.RawMessage(`["topic.create"]`)
	retired := decodeConfig(t, config)

	encoded, err := trustlevels.EncodeConfig(retired)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	var roundTripped trustlevels.Config
	if err := json.Unmarshal([]byte(encoded), &roundTripped); err != nil {
		t.Fatalf("反序列化: %v", err)
	}
	for _, tier := range roundTripped.Tiers {
		if tier.ID != "resident" {
			continue
		}
		if string(tier.Earnable) != `["topic.create"]` {
			t.Fatalf("停用档位的 earnable 被改写成了 %s", tier.Earnable)
		}
		return
	}
	t.Fatal("编码结果里没有 resident 档")
}
