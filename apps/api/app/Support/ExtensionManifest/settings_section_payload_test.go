package extensionmanifest

import (
	"regexp"
	"strings"
	"testing"
)

var entityMetaFieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,63}$`)

// 组合键必须落进 entity-meta 的长度与字符集；裸键 ↔ 组合键只能有一套换算。
func TestSettingsSurfaceFieldKeyStaysInsideEntityMetaLimits(t *testing.T) {
	longA := strings.Repeat("a", 80) + ".alpha"
	longB := strings.Repeat("a", 80) + ".beta"
	for _, extensionID := range []string{"demo.settings", "UPPER.Case.Id", longA, longB} {
		key := SettingsSurfaceFieldKey(extensionID, strings.Repeat("k", 41))
		if !entityMetaFieldKeyPattern.MatchString(key) {
			t.Fatalf("field key %q for extension %q violates the entity-meta pattern", key, extensionID)
		}
		if len(key) > 64 {
			t.Fatalf("field key %q is %d bytes (limit 64)", key, len(key))
		}
	}
	if SettingsSurfaceFieldKey(longA, "theme") == SettingsSurfaceFieldKey(longB, "theme") {
		t.Fatal("distinct long extension ids must not collapse onto the same field key")
	}
}

func TestSettingsSurfaceFieldKeyComposesAndSplits(t *testing.T) {
	key := SettingsSurfaceFieldKey("demo.settings", "theme")
	if !strings.HasPrefix(key, "x.demo-settin-") || !strings.HasSuffix(key, ".theme") {
		t.Fatalf("composed key = %q", key)
	}
	// 治理端点两种入参都必须接受：组合键与裸键。
	for _, input := range []string{key, "theme", "  theme  "} {
		if got := SettingsSurfaceBareFieldKey("demo.settings", input); got != "theme" {
			t.Fatalf("bare key for %q = %q", input, got)
		}
	}
	// 组合键是结构特征而不是归属判断：任何 x.<segment>.<key> 都按裸键取回
	// （治理端点拿不到扩展 id 时靠这条）。
	if got := SettingsSurfaceBareFieldKey("demo.settings", "x.other.theme"); got != "theme" {
		t.Fatalf("composite key must be stripped structurally, got %q", got)
	}
	// 结构不完整的值原样保留，交给后续声明校验拒绝。
	for _, input := range []string{"x.theme", "x..theme", "theme.x"} {
		if got := SettingsSurfaceBareFieldKey("demo.settings", input); got != input {
			t.Fatalf("malformed key %q must be preserved, got %q", input, got)
		}
	}
}

// 两条「只差分隔符」的扩展 id 绝不能落到同一行字段：压平 [a-z0-9_-] 之后
// sforum.smtp 与 sforum-smtp 完全同形，身份必须由哈希兜住。
func TestSettingsSurfaceFieldKeySeparatesSimilarExtensionIDs(t *testing.T) {
	pairs := [][2]string{
		{"sforum.smtp", "sforum-smtp"},
		{"demo.settings", "demo-settings"},
		{"a.b.c", "a-b-c"},
	}
	for _, pair := range pairs {
		left := SettingsSurfaceFieldKey(pair[0], "theme")
		right := SettingsSurfaceFieldKey(pair[1], "theme")
		if left == right {
			t.Fatalf("%q and %q collide on field key %q", pair[0], pair[1], left)
		}
	}
	// 短 id 也要带哈希：段结构固定为 <prefix>-<8 hex>。
	if got := SettingsSurfaceFieldKey("a", "theme"); got == "x.a.theme" {
		t.Fatalf("short extension id must still carry a hash, got %q", got)
	}
}
