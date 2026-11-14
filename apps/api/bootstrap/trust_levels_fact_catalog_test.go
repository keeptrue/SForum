package bootstrap

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 本文件钉住 M4A-2：事实候选必须**合并两个数组**。
//
// 只读 `badgeConditions` 的后果不是报错，而是「用新别名声明的插件在信任等级里永远查不到
// 自己的条件」——那个条件一直 unknown，运营只会觉得「插件装了但选项没出来」。

func pluginWithConditions(enabled bool, badge []extensionmanifest.ManifestBadgeCondition, userFact []extensionmanifest.ManifestBadgeCondition) extensions.Extension {
	status := extensions.StatusDisabled
	if enabled {
		status = extensions.StatusEnabled
	}
	return extensions.Extension{
		Status: status,
		Type:   extensions.TypePlugin,
		Manifest: extensionmanifest.Manifest{
			ID:                 "acme.wiki",
			BadgeConditions:    badge,
			UserFactConditions: userFact,
			PermissionDefinitions: []extensionmanifest.ManifestPermissionDefinition{
				{Key: "acme.wiki.pages.read", TrustLevelEarnable: true},
				{Key: "acme.wiki.pages.write", TrustLevelEarnable: false},
			},
		},
	}
}

func condition(key string) extensionmanifest.ManifestBadgeCondition {
	return extensionmanifest.ManifestBadgeCondition{
		Key:             key,
		ContractVersion: key + "@1",
		Label:           map[string]string{"zh-CN": "页面数", "en-US": "Pages"},
		Compare:         []string{"gte"},
	}
}

// TestFactCatalogMergesBothConditionArrays 断言两个数组都被读进候选。
func TestFactCatalogMergesBothConditionArrays(t *testing.T) {
	packages := []extensions.Extension{pluginWithConditions(true,
		[]extensionmanifest.ManifestBadgeCondition{condition("acme.wiki.legacy")},
		[]extensionmanifest.ManifestBadgeCondition{condition("acme.wiki.pages")},
	)}
	candidates, conflicts := mergeEnabledFactDeclarations(packages)
	if len(conflicts) != 0 {
		t.Fatalf("合法声明不该产生冲突：%+v", conflicts)
	}
	keys := conditions.FactKeys(candidates)
	if len(keys) != 2 {
		t.Fatalf("候选 = %v（期望 2 条：badgeConditions + userFactConditions 各一条）", keys)
	}
	found := map[string]bool{}
	for _, key := range keys {
		found[key] = true
	}
	if !found["acme.wiki.legacy"] || !found["acme.wiki.pages"] {
		t.Fatalf("两个数组必须都进候选，得到 %v", keys)
	}
}

// TestFactCatalogSkipsDisabledForCandidatesButKeepsEarnables 钉住两条不同口径：
// 事实候选只认**启用**插件；权限声明要**含停用**（区分 inactive 与 unknown）。
func TestFactCatalogSkipsDisabledForCandidatesButKeepsEarnables(t *testing.T) {
	packages := []extensions.Extension{pluginWithConditions(false,
		nil, []extensionmanifest.ManifestBadgeCondition{condition("acme.wiki.pages")},
	)}
	candidates, _ := mergeEnabledFactDeclarations(packages)
	if len(candidates) != 0 {
		t.Fatalf("停用插件的事实条件不该进候选，得到 %+v", candidates)
	}
	earnable := earnableDeclarationsFromPackages(packages)
	if len(earnable) != 1 {
		t.Fatalf("停用插件的 trustLevelEarnable 声明必须保留（用于标 inactive），得到 %+v", earnable)
	}
	if earnable[0].Enabled {
		t.Fatal("停用插件的声明必须标记 Enabled=false")
	}
	if earnable[0].Key != "acme.wiki.pages.read" {
		t.Fatalf("只该收 trustLevelEarnable=true 的键，得到 %q", earnable[0].Key)
	}
}
