package trustlevels_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// legacyV1DefaultConfig 是 v1 形状的**推荐默认值**字面量（与定长五档时代的
// DefaultPolicy 逐项一致）。它代表「存量站点库里的值」，不是从当前代码生成的，
// 所以能真正测到「旧数据读得进来」。
const legacyV1DefaultConfig = `{"schemaVersion":1,"levels":[
	{"levelSlug":"member","requirements":{"days_visited":3,"topic_count":0,"comment_count":5,"like_received_count":0,"like_given_count":0,"bookmark_count":0},"limits":{},"earnable":[]},
	{"levelSlug":"resident","requirements":{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3,"bookmark_count":0},"limits":{},"earnable":[]},
	{"levelSlug":"veteran","requirements":{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5},"limits":{},"earnable":[]},
	{"levelSlug":"leader","requirements":{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20},"limits":{},"earnable":[]}]}`

// TestLegacyV1UpgradesInMemory 锁定 D-N5：v1 必须**无损**读入并在内存里升级成 v2。
//
// 不做这条兼容，升级到本版本的那一次部署会让所有存量站点「开机即丢配置」——而且
// 因为解码失败整体拒绝，运营看到的是等级判定退回未解析，不是某个阈值变了。
func TestLegacyV1UpgradesInMemory(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(legacyV1DefaultConfig)
	if err != nil {
		t.Fatalf("v1 配置读不进来：%v", err)
	}
	if got := policy.LastRank(); got != 4 {
		t.Fatalf("LastRank = %d，期望 4", got)
	}
	// 内置四档的 id 与 v1 的 levelSlug 逐字相同：对外契约不变。
	for rank, wantID := range map[trustlevels.Level]string{1: "member", 2: "resident", 3: "veteran", 4: "leader"} {
		if got := policy.TierID(rank); got != wantID {
			t.Fatalf("TL%d 身份 = %q，期望 %q", rank, got, wantID)
		}
		tier, ok := policy.TierAt(rank)
		if !ok {
			t.Fatalf("TL%d 不存在", rank)
		}
		// 升级必须补上 name/icon：v1 没有这两个字段，缺了 UI 就没东西可显示。
		if tier.Name.IsZero() {
			t.Fatalf("%s 升级后没有名称", wantID)
		}
		if !trustlevels.IsTierIcon(tier.Icon) {
			t.Fatalf("%s 升级后的图标 %q 不在白名单内", wantID, tier.Icon)
		}
	}
	// 图标走默认序列：TL1 = leaf（芽/叶/屋/山/冠 的第二个位置是 TL1）。
	if got := policy.IconOf(1); got != "leaf" {
		t.Fatalf("TL1 默认图标 = %q，期望 leaf", got)
	}
}

// TestLegacyV1EncodesAsV2 锁定「EncodeConfig 只写 v2」：读入 v1 不是为了继续写 v1。
func TestLegacyV1EncodesAsV2(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(legacyV1DefaultConfig)
	if err != nil {
		t.Fatalf("v1 配置读不进来：%v", err)
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if !strings.Contains(encoded, `"schemaVersion":2`) {
		t.Fatalf("编码结果不是 v2：%s", encoded)
	}
	if !strings.Contains(encoded, `"tiers"`) || !strings.Contains(encoded, `"retired":false`) {
		t.Fatalf("编码结果缺少 v2 字段：%s", encoded)
	}
	for _, forbidden := range []string{`"levels"`, `"levelSlug"`} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("编码结果仍写旧字段 %s：%s", forbidden, encoded)
		}
	}
	// 再读回来必须与第一次解码等价（否则「保存一次」就会改变策略）。
	roundTripped, err := trustlevels.DecodeConfig(encoded)
	if err != nil {
		t.Fatalf("v2 结果读不回来：%v", err)
	}
	if roundTripped.LastRank() != policy.LastRank() {
		t.Fatalf("往返后档数变了：%d → %d", policy.LastRank(), roundTripped.LastRank())
	}
	for rank := trustlevels.LevelMember; rank <= policy.LastRank(); rank++ {
		if roundTripped.RequirementsOf(rank) != policy.RequirementsOf(rank) {
			t.Fatalf("往返后 TL%d 阈值变了", rank)
		}
		if roundTripped.TierID(rank) != policy.TierID(rank) {
			t.Fatalf("往返后 TL%d 身份变了", rank)
		}
	}
}

// TestLegacyV1EqualsDefaultPolicyDecoded 是「v1 与内置默认等价」的对拍：
// 存量站点没改过配置时，读出来的策略必须与当前 DefaultPolicy 逐项相同。
//
// 两边的比较基准都先走一遍 编码→解码，避免 nil 与 []string{} 的差别伪装成差异。
func TestLegacyV1EqualsDefaultPolicyDecoded(t *testing.T) {
	fromLegacy, err := trustlevels.DecodeConfig(legacyV1DefaultConfig)
	if err != nil {
		t.Fatalf("v1 配置读不进来：%v", err)
	}
	defaultEncoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatalf("EncodeConfig(DefaultPolicy): %v", err)
	}
	fromDefault, err := trustlevels.DecodeConfig(defaultEncoded)
	if err != nil {
		t.Fatalf("默认值读不回来：%v", err)
	}
	if fromLegacy.LastRank() != fromDefault.LastRank() {
		t.Fatalf("档数不一致：v1=%d 默认=%d", fromLegacy.LastRank(), fromDefault.LastRank())
	}
	for rank := trustlevels.LevelMember; rank <= fromDefault.LastRank(); rank++ {
		if fromLegacy.RequirementsOf(rank) != fromDefault.RequirementsOf(rank) {
			t.Fatalf("TL%d 阈值不一致：v1=%+v 默认=%+v",
				rank, fromLegacy.RequirementsOf(rank), fromDefault.RequirementsOf(rank))
		}
		if fromLegacy.TierID(rank) != fromDefault.TierID(rank) {
			t.Fatalf("TL%d 身份不一致", rank)
		}
		if fromLegacy.DisplayNameOf(rank) != fromDefault.DisplayNameOf(rank) {
			t.Fatalf("TL%d 名称不一致", rank)
		}
		if fromLegacy.IconOf(rank) != fromDefault.IconOf(rank) {
			t.Fatalf("TL%d 图标不一致", rank)
		}
		if !reflect.DeepEqual(fromLegacy.Earnable.Grants(rank), fromDefault.Earnable.Grants(rank)) {
			t.Fatalf("TL%d 已挣得不一致", rank)
		}
	}
	if fromLegacy.NewUserDays != fromDefault.NewUserDays {
		t.Fatalf("NewUserDays 不一致：%d vs %d", fromLegacy.NewUserDays, fromDefault.NewUserDays)
	}
}

// TestLegacyV1RejectsInvalid 锁定 v1 的判据与 v1 时代一致（不是「什么都收」）。
func TestLegacyV1RejectsInvalid(t *testing.T) {
	valid := `{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
		{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}`
	cases := map[string]string{
		"缺少等级": `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]}]}`,
		"等级重复": `{"schemaVersion":1,"levels":[
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"member","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`,
		"TL0 出现在 levels 里": `{"schemaVersion":1,"levels":[
			{"levelSlug":"newcomer","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`,
		"未知等级": `{"schemaVersion":1,"levels":[
			{"levelSlug":"tier_6","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"resident","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"veteran","requirements":{},"limits":{},"earnable":[]},
			{"levelSlug":"leader","requirements":{},"limits":{},"earnable":[]}]}`,
		"缺少 levels 字段": `{"schemaVersion":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := trustlevels.DecodeConfig(raw); err == nil {
				t.Fatal("非法 v1 配置被接受了")
			}
		})
	}
	// 对照组：合法 v1 必须被接受，否则上面全票通过也没有意义。
	if _, err := trustlevels.DecodeConfig(`{"schemaVersion":1,"levels":[` + valid + `]}`); err != nil {
		t.Fatalf("合法 v1 被拒绝：%v", err)
	}
}

// TestLegacyV1EncodeIsStableAcrossSaves 锁定「保存一次 = 升级一次」且此后稳定：
// 第二、第三次保存（v2 → v2）必须逐字节相同，否则每次保存都会写脏审计/缓存。
func TestLegacyV1EncodeIsStableAcrossSaves(t *testing.T) {
	policy, err := trustlevels.DecodeConfig(legacyV1DefaultConfig)
	if err != nil {
		t.Fatalf("v1 配置读不进来：%v", err)
	}
	first, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	second, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if first != second {
		t.Fatalf("同一份策略两次编码不一致：\n%s\n%s", first, second)
	}
	// 且两份编码都是合法 JSON。
	for _, encoded := range []string{first, second} {
		var probe map[string]any
		if err := json.Unmarshal([]byte(encoded), &probe); err != nil {
			t.Fatalf("编码结果不是合法 JSON：%v", err)
		}
	}
}
