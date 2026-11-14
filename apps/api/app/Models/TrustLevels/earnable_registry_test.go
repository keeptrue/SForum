package trustlevels_test

import (
	"context"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉住 M4a 的「可挣得白名单运行期化」：
// 候选 = 核心七键 ∪ 已启用插件声明；停用插件的键保留在配置里但不授予；从未声明的键
// 在写期被拒。三者对应三个不同的状态（valid / inactive / unknown，D22 的用词）。

// defaultTierEarnableRegistry 造一份声明：
//   - 核心键之外的 `acme.shop.orders` 由**已启用**插件声明 → valid；
//   - `acme.wiki.pages` 由**停用**插件声明 → inactive；
//   - `acme.gone.thing` 从未被声明 → unknown。
func defaultTierEarnableRegistry() trustlevels.EarnableRegistry {
	return trustlevels.NewEarnableRegistry([]trustlevels.EarnableDeclaration{
		{Key: "acme.shop.orders", ExtensionID: "acme.shop", Enabled: true},
		{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
	})
}

// TestEarnableRegistryThreeDenied 覆盖三条 denied + 核心七键的反向证据。
func TestEarnableRegistryThreeDenied(t *testing.T) {
	registry := defaultTierEarnableRegistry()

	t.Run("从未声明 → unknown 且不进候选", func(t *testing.T) {
		if state := registry.State("acme.gone.thing"); state != trustlevels.EarnableStateUnknown {
			t.Fatalf("state = %q（期望 unknown）", state)
		}
		if registry.Grantable("acme.gone.thing") {
			t.Fatal("从未声明的键不该可授予")
		}
		for _, key := range registry.Keys() {
			if key == "acme.gone.thing" {
				t.Fatal("未知键不该进候选列表")
			}
		}
	})

	t.Run("与核心键冲突 → 插件声明被忽略（核心说了算）", func(t *testing.T) {
		collided := trustlevels.NewEarnableRegistry([]trustlevels.EarnableDeclaration{
			{Key: "topic.create", ExtensionID: "topic", Enabled: true},
			{Key: "post.create", ExtensionID: "topic", Enabled: false},
		})
		keys := collided.Keys()
		occurrences := 0
		for _, key := range keys {
			if key == "topic.create" {
				occurrences++
			}
		}
		if occurrences != 1 {
			t.Fatalf("核心键出现了 %d 次（插件声明必须被忽略，不能重复登记）", occurrences)
		}
		for _, key := range keys {
			if key == "post.create" {
				// 核心键不会因为「插件停用」变成 inactive：核心永远有效。
				if state := collided.State(key); state != trustlevels.EarnableStateValid {
					t.Fatalf("核心键 %q 状态 = %q（期望 valid）", key, state)
				}
			}
		}
		if state := collided.State("post.create"); state != trustlevels.EarnableStateValid {
			t.Fatalf("停用插件对核心键的声明不该把它降级，得到 %q", state)
		}
	})

	t.Run("未声明为 true → 不进候选", func(t *testing.T) {
		// 声明列表里没有它，等价于 manifest 上 `trustLevelEarnable` 缺省 false。
		registry := trustlevels.NewEarnableRegistry([]trustlevels.EarnableDeclaration{})
		if state := registry.State("acme.shop.orders"); state != trustlevels.EarnableStateUnknown {
			t.Fatalf("state = %q（期望 unknown）", state)
		}
		if got := len(registry.Keys()); got != len(trustlevels.EarnableWhitelist()) {
			t.Fatalf("没有插件声明时候选应恰好是核心 %d 键，得到 %d", len(trustlevels.EarnableWhitelist()), got)
		}
	})

	t.Run("核心七键反向证据：仍 valid 且可授予", func(t *testing.T) {
		for _, key := range trustlevels.EarnableWhitelist() {
			if state := registry.State(key); state != trustlevels.EarnableStateValid {
				t.Fatalf("核心键 %q 状态 = %q（期望 valid）", key, state)
			}
			if !registry.Grantable(key) {
				t.Fatalf("核心键 %q 必须仍可授予", key)
			}
			if len(trustlevels.ValidateEarnableDeclared([]string{key}, registry)) != 0 {
				t.Fatalf("核心键 %q 不该被判未声明", key)
			}
		}
		if len(registry.Keys()) != len(trustlevels.EarnableWhitelist())+1 {
			t.Fatalf("候选 = 核心 %d + 1 条已声明，得到 %d：%v", len(trustlevels.EarnableWhitelist()), len(registry.Keys()), registry.Keys())
		}
	})
}

// TestGrantsDropsInactiveAndUnknown 钉住运行期惰性：配置保留、授予过滤、等级不变。
func TestGrantsDropsInactiveAndUnknown(t *testing.T) {
	policy := mustPolicy(t, tiersJSON(
		`{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["topic.create","acme.shop.orders","acme.wiki.pages","acme.gone.thing"]}`,
	))
	resolver := trustlevels.NewResolver(
		stubActivityReader{activity: trustlevels.Activity{DaysVisited: 100}},
		stubPolicySource{policy: policy},
	).WithEarnables(func(context.Context) ([]trustlevels.EarnableDeclaration, error) {
		return []trustlevels.EarnableDeclaration{
			{Key: "acme.shop.orders", ExtensionID: "acme.shop", Enabled: true},
			{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
		}, nil
	})

	level, granted, err := resolver.Grants(context.Background(), 7, identity.UserKindHuman, time.Now().Add(-400*24*time.Hour))
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if level != 1 {
		t.Fatalf("等级不该受可挣得过滤影响，得到 %d", level)
	}
	want := map[string]bool{"topic.create": true, "acme.shop.orders": true}
	for _, key := range granted {
		if !want[key] {
			t.Fatalf("授予了不该授予的键 %q（完整 %v）", key, granted)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("缺少应授予的键 %v（实际 %v）", want, granted)
	}
	// 配置本身不动：插件恢复后这些键自动重新生效。
	if got := policy.Earnable.Permissions(trustlevels.LevelMember); len(got) != 4 {
		t.Fatalf("配置被改写了：%v", got)
	}
}

// TestDisabledPluginKeepsConfigDecodable 是 D24 修订的核心场景：
// 插件停用后，整份配置仍能解码、仍能原样编码，只是那个键不授予、读面标 inactive。
func TestDisabledPluginKeepsConfigDecodable(t *testing.T) {
	raw := tiersJSON(
		`{"id":"member","name":{"zh":"甲","en":"A"},"icon":"leaf","requirements":{},"limits":{},"earnable":["acme.wiki.pages"]}`,
	)
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		t.Fatalf("插件停用不该让配置解码失败（否则全站等级掉默认策略）：%v", err)
	}
	registry := trustlevels.NewEarnableRegistry([]trustlevels.EarnableDeclaration{
		{Key: "acme.wiki.pages", ExtensionID: "acme.wiki", Enabled: false},
	})
	if state := registry.State("acme.wiki.pages"); state != trustlevels.EarnableStateInactive {
		t.Fatalf("state = %q（期望 inactive，不是 unknown——它曾被声明）", state)
	}
	// 写期允许保存（「先停用插件、稍后修配置」是合规路径）。
	if details := trustlevels.ValidateEarnableDeclared([]string{"acme.wiki.pages"}, registry); len(details) != 0 {
		t.Fatalf("inactive 的键不该让保存失败：%+v", details)
	}
	// 运行期不授予。
	if granted := registry.FilterGranted(policy.Earnable.Grants(trustlevels.LevelMember)); len(granted) != 0 {
		t.Fatalf("停用插件的键不该被授予，得到 %v", granted)
	}
	// 编码保留该键（运营重开插件后不需要重新勾）。
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !containsString(encoded, "acme.wiki.pages") {
		t.Fatalf("编码丢了停用插件的键：%s", encoded)
	}
}

func containsString(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
