package trustlevels_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// restoreFixture 造一份「曾删过 tier_6、还留着 tier_6 墓碑」的当前策略。
func restoreFixture(t *testing.T) trustlevels.Policy {
	t.Helper()
	policy, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierJSON("member") + `,` + tierJSON("resident") + `],
"deletedTierIds":["tier_6"]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return policy
}

// TestRestoreDefaultsKeepsTombstones 是 B2 的靶子：恢复默认是整份覆盖，但**不能**
// 把删除墓碑一起清掉，否则「id 永不复用」这条契约会被一个按钮悄悄打破。
func TestRestoreDefaultsKeepsTombstones(t *testing.T) {
	previous := restoreFixture(t)
	restored := trustlevels.RestoreDefaults(previous)

	if len(restored.DeletedTierIDs) == 0 || restored.DeletedTierIDs[0] != "tier_6" {
		t.Fatalf("恢复默认丢了墓碑：%v", restored.DeletedTierIDs)
	}
	if got := restored.NextTierSeq(); got != 7 {
		t.Fatalf("恢复默认后 NextTierSeq = %d，期望 7（墓碑 tier_6 仍占位）", got)
	}
	// 恢复出来的档位本身仍是推荐默认（内置 4 档），墓碑不占 rank。
	if got := restored.LastRank(); got != 4 {
		t.Fatalf("恢复默认后 LastRank = %d，期望 4", got)
	}
	// 恢复默认不再携带运营自建的档位。
	for _, tier := range restored.Tiers {
		if strings.HasPrefix(tier.ID, "tier_") {
			t.Fatalf("恢复默认仍带着自建档位 %q", tier.ID)
		}
	}
}

// TestRestoreDefaultsTombstonesRemovedCustomTiers 覆盖「墓碑从被移除的档位里补」：
// 当前配置里的 tier_9 会被恢复默认删掉，但它的 id 必须转成墓碑。
func TestRestoreDefaultsTombstonesRemovedCustomTiers(t *testing.T) {
	previous, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierJSON("member") + `,` + tierJSON("tier_9") + `]}`)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	restored := trustlevels.RestoreDefaults(previous)
	if len(restored.DeletedTierIDs) != 1 || restored.DeletedTierIDs[0] != "tier_9" {
		t.Fatalf("被移除的自建档位没有转成墓碑：%v", restored.DeletedTierIDs)
	}
	if got := restored.NextTierSeq(); got != 10 {
		t.Fatalf("NextTierSeq = %d，期望 10（tier_9 的序号仍被占用）", got)
	}
}

// TestRestoreDefaultsSurvivesEncodeDecode 走完整链路：恢复 → 编码 → 解码 → 新建一档，
// 派生出的 id 必须跳过墓碑（tier_7，而不是复用的 tier_6）。
func TestRestoreDefaultsSurvivesEncodeDecode(t *testing.T) {
	restored := trustlevels.RestoreDefaults(restoreFixture(t))
	encoded, err := trustlevels.EncodeConfig(restored)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		t.Fatalf("恢复默认写出的不是合法 JSON：%v，%s", err, encoded)
	}
	if _, ok := raw["deletedTierIds"]; !ok {
		t.Fatalf("恢复默认写出的配置里没有墓碑：%s", encoded)
	}
	// 模拟「恢复后又新建一档」：把新档（id 空）追加进同一份配置再解码。
	withNewTier := strings.Replace(encoded, `"tiers":[`, `"tiers":[`+tierJSON("")+`,`, 1)
	policy, err := trustlevels.DecodeConfig(withNewTier)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	derived := ""
	for _, tier := range policy.Tiers {
		if strings.HasPrefix(tier.ID, "tier_") && tier.ID != "tier_6" {
			derived = tier.ID
		}
	}
	if derived != "tier_7" {
		t.Fatalf("新建档位拿到 id %q，期望 tier_7（tier_6 已被墓碑占用）", derived)
	}
}

// TestRestoreDefaultsWithoutPreviousIsPureRecommended 锁定「配置坏掉/首次安装」路径：
// 没有墓碑可继承时，恢复默认写出的就是纯推荐值（与 recommendedConfig 同形）。
func TestRestoreDefaultsWithoutPreviousIsPureRecommended(t *testing.T) {
	restored := trustlevels.RestoreDefaults(trustlevels.Policy{})
	if len(restored.DeletedTierIDs) != 0 {
		t.Fatalf("空策略不应凭空产生墓碑：%v", restored.DeletedTierIDs)
	}
	encoded, err := trustlevels.EncodeConfig(restored)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if strings.Contains(encoded, "deletedTierIds") {
		t.Fatalf("无墓碑时不应写出 deletedTierIds：%s", encoded)
	}
}
