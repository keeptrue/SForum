package trustlevels_test

import (
	"reflect"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 图标取值域 = 16 个默认短名 ∪ `i-<collection>-<name>` 全名（lucide / tabler）。
//
// 为什么放宽：外观弹窗的图标选择器用的是 lucide + tabler 全量目录，运营选到的名字
// 不可能落在那 16 项里；拿白名单当闸门只会把合法选择静默丢掉。形状校验负责挡住
// 真正会让前端渲染不出来的值（未知集合、大写前缀、缺 collection）。
//
// 本文件守的是**取值域的边界**，不是实现：短名集合仍是「默认图标序列 + 推荐」，
// 所以顺序断言也留在这里 —— 顺序 = rank，改顺序等于悄悄换掉每档的默认图标。

func TestIsTierIconAcceptsDefaultShortNames(t *testing.T) {
	// 16 个短名一个都不能掉：存量站点的 icon 字段里存的就是这些。
	for _, name := range trustlevels.TierIcons() {
		if !trustlevels.IsTierIcon(name) {
			t.Fatalf("默认短名 %q 被拒绝了", name)
		}
	}
	// 字面抽查：白名单首项与历史五档，别让「全部接受」把空表也测绿。
	for _, name := range []string{"sprout", "leaf", "home", "mountain", "crown"} {
		if !trustlevels.IsTierIcon(name) {
			t.Fatalf("历史默认短名 %q 被拒绝了", name)
		}
	}
}

func TestIsTierIconAcceptsHostFullNames(t *testing.T) {
	// 全名 = `i-<collection>-<name>`，name 允许连字符分段（arrow-up、arrow-up-circle）。
	accepted := []string{
		"i-lucide-sprout",
		"i-tabler-crown",
		"i-tabler-arrow-up",
		"i-lucide-arrow-up-circle",
		"i-tabler-device-desktop-analytics",
	}
	for _, name := range accepted {
		if !trustlevels.IsTierIcon(name) {
			t.Fatalf("合法的全名 %q 被拒绝了", name)
		}
	}
}

func TestIsTierIconRejectsMalformedNames(t *testing.T) {
	rejected := []struct {
		name   string
		reason string
	}{
		{"i-fa-solid-x", "fa 不是受支持的集合（前端无法静态枚举）"},
		{"I-LUCIDE-X", "大写前缀不在语法里：class 名区分大小写"},
		{"sprout-x", "缺 collection，既不是短名也不是全名"},
		{"", "空串是「没配」的语义，不该被算作合法图标"},
		{"i-lucide-", "collection 后必须有 name"},
		{"i-tabler-", "collection 后必须有 name"},
		{"i-lucide-Leaf", "name 段只接受小写"},
		{"i-lucide-arrow_up", "下划线不是 host 图标名的分段符"},
		{" i-lucide-sprout", "前导空格不是合法语法的一部分"},
		{"i-lucide-sprout ", "尾随空格不是合法语法的一部分"},
	}
	for _, testCase := range rejected {
		if trustlevels.IsTierIcon(testCase.name) {
			t.Fatalf("%q 被接受了：%s", testCase.name, testCase.reason)
		}
	}
}

// TestTierIconsKeepsDefaultOrder 锁死 16 项与顺序。
//
// 顺序不是排版：第 n 项就是 rank n 的默认图标（TL0 sprout / TL1 leaf / …），
// 前端 `DEFAULT_TIER_ICON_NAMES` 逐字抄这一份（trustLevelNameFallback.test.ts 会比对），
// 而且 `DefaultTierIcon` 直接按 rank 取 —— 插一项进中间等于把后面每档的默认图标都换掉。
func TestTierIconsKeepsDefaultOrder(t *testing.T) {
	want := []string{
		"sprout", "leaf", "home", "mountain", "crown",
		"star", "sun", "moon", "flame", "shield",
		"gem", "compass", "zap", "anchor", "medal", "trophy",
	}
	got := trustlevels.TierIcons()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("默认图标序列变了（顺序 = rank 语义）:\n got=%v\nwant=%v", got, want)
	}
	if len(want) != 16 {
		t.Fatalf("默认图标序列应为 16 项，实际 %d", len(want))
	}
}

func TestDefaultTierIconFollowsRank(t *testing.T) {
	if got := trustlevels.DefaultTierIcon(trustlevels.LevelNewcomer); got != "sprout" {
		t.Fatalf("TL0 默认图标 = %q，期望 sprout", got)
	}
	if got := trustlevels.DefaultTierIcon(trustlevels.LevelMember); got != "leaf" {
		t.Fatalf("TL1 默认图标 = %q，期望 leaf", got)
	}
	// 越界 rank（手搓 Policy / 满配站点）回退到首项，而不是空串：
	// 空串在 UI 上是「没有图标」，比回退更难排查。
	if got := trustlevels.DefaultTierIcon(trustlevels.Level(99)); got != "sprout" {
		t.Fatalf("越界 rank 的默认图标 = %q，期望回退 sprout", got)
	}
}

// TestDecodeConfigAcceptsHostIconFullName 走一次真实解码：只测谓词证明不了
// 「存进去能读出来」，而运营的路径永远是 DecodeConfig。
func TestDecodeConfigAcceptsHostIconFullName(t *testing.T) {
	for _, icon := range []string{"i-tabler-crown", "i-lucide-arrow-up-circle", "leaf"} {
		raw := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"` + icon +
			`","requirements":{},"limits":{},"earnable":[]}]}`
		policy, err := trustlevels.DecodeConfig(raw)
		if err != nil {
			t.Fatalf("DecodeConfig(%q): %v", icon, err)
		}
		tier, ok := policy.TierAt(trustlevels.LevelMember)
		if !ok || tier.Icon != icon {
			t.Fatalf("TL1 的 icon = %q，期望原样保留 %q", tier.Icon, icon)
		}
	}
}

// TestDecodeConfigRejectsMalformedIcon 与上面成对：放宽不等于敞开。
func TestDecodeConfigRejectsMalformedIcon(t *testing.T) {
	for _, icon := range []string{"i-fa-solid-x", "I-LUCIDE-X", "sprout-x"} {
		raw := `{"schemaVersion":2,"tiers":[{"id":"member","name":{"zh":"甲","en":"A"},"icon":"` + icon +
			`","requirements":{},"limits":{},"earnable":[]}]}`
		_, err := trustlevels.DecodeConfig(raw)
		if err == nil {
			t.Fatalf("非法图标 %q 被接受了", icon)
		}
		details := trustlevels.ConfigErrorDetails(err)
		if len(details) != 1 || details[0].Code != trustlevels.CodeIconNotWhitelisted || details[0].Field != "icon" {
			t.Fatalf("图标 %q 的明细定位不对：%+v", icon, details)
		}
		// 码名保持 icon_not_whitelisted（前端 i18n 的稳定键），语义已变成「不合法」。
		if got, _ := details[0].Params["icon"].(string); got != icon {
			t.Fatalf("params.icon = %+v，期望原样回报 %q", details[0].Params, icon)
		}
	}
}
