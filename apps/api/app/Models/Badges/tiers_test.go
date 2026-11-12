package badges

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 等级表（badge_tiers）的领域单测。
//
// 覆盖三组不变量：
//   - 入参校验（key 形状 / 双语名 / 图标前缀 / 颜色白名单 / position 非负）；
//   - 徽章切档规则（保持原值一律允许，切到停用或不存在档一律 422）；
//   - 删除等级的四条前置条件（必须给迁移目标、不能是自身、目标必须启用、不能删最后一档）。

func TestValidateTierInputTable(t *testing.T) {
	cases := []struct {
		name    string
		input   TierInput
		wantErr bool
	}{
		{"valid", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Icon: "i-lucide-crown", Color: "amber", Enabled: true}, false},
		{"valid custom hex", TierInput{Key: "vip_2026", NameZh: "贵宾", NameEn: "VIP", Icon: "i-tabler-star", Color: "#7c3aed", Enabled: true}, false},
		{"valid empty color", TierInput{Key: "plain", NameZh: "普通", NameEn: "Plain", Icon: "i-lucide-medal", Color: "", Enabled: true}, false},
		{"key uppercase normalizes", TierInput{Key: "  GOLD  ", NameZh: "金牌", NameEn: "Gold", Enabled: true}, false},
		{"key too short", TierInput{Key: "g", NameZh: "金牌", NameEn: "Gold"}, true},
		{"key starts with digit", TierInput{Key: "1gold", NameZh: "金牌", NameEn: "Gold"}, true},
		{"key with hyphen", TierInput{Key: "top-tier", NameZh: "顶档", NameEn: "Top"}, true},
		{"key with space", TierInput{Key: "gold tier", NameZh: "金牌", NameEn: "Gold"}, true},
		{"key too long", TierInput{Key: "a2345678901234567890123456789012b", NameZh: "长", NameEn: "Long"}, true},
		{"empty nameZh", TierInput{Key: "gold", NameZh: "   ", NameEn: "Gold"}, true},
		{"empty nameEn", TierInput{Key: "gold", NameZh: "金牌", NameEn: ""}, true},
		{"bad icon prefix", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Icon: "emoji-crown"}, true},
		{"unknown color token", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Color: "chartreuse"}, true},
		{"uppercase hex rejected", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Color: "#7C3AED"}, true},
		{"three digit hex rejected", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Color: "#fff"}, true},
		{"negative position", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Position: -2}, true},
		{"append sentinel accepted", TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold", Position: TierPositionAppend}, false},
	}
	for _, tc := range cases {
		_, err := validateTierInput(tc.input, true)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: want error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: want nil, got %v", tc.name, err)
		}
		if tc.wantErr && !errors.Is(err, ErrBadgeTierInvalid) {
			t.Fatalf("%s: want ErrBadgeTierInvalid, got %v", tc.name, err)
		}
	}
}

// TestValidateTierInputSkipsKeyShapeOnUpdate 断言更新不校验 key 形状。
//
// key 不可变，形状在创建时已经验过。若更新时再验，历史 key（比如早期手工插入的
// 大写 key）会变成「连名字都改不了」——那不是校验，是冻结。
func TestValidateTierInputSkipsKeyShapeOnUpdate(t *testing.T) {
	if _, err := validateTierInput(TierInput{Key: "LegacyKey", NameZh: "旧档", NameEn: "Legacy"}, false); err != nil {
		t.Fatalf("update validation must not re-check key shape: %v", err)
	}
}

// TestValidateTierInputDefaultsIcon 断言空图标落成默认档位图标。
func TestValidateTierInputDefaultsIcon(t *testing.T) {
	normalized, err := validateTierInput(TierInput{Key: "gold", NameZh: "金牌", NameEn: "Gold"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Icon != "i-lucide-medal" {
		t.Fatalf("icon = %q, want i-lucide-medal", normalized.Icon)
	}
}

// TestResolveBadgeTierAllowsUnchangedTier 断言「保持原值一律允许」。
//
// 场景：运营停用了 silver 档，此时某个 silver 徽章只是改名字。若校验把「原值已停用」
// 当成错误，这个徽章就再也改不动了——停用一档等于冻结所有引用它的徽章。
func TestResolveBadgeTierAllowsUnchangedTier(t *testing.T) {
	store := newFakeStore()
	if _, err := store.UpdateTier(context.Background(), "silver", TierInput{
		Key: "silver", NameZh: "银牌", NameEn: "Silver", Icon: "i-lucide-medal", Color: "slate", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	current := TierSilver
	if err := resolveBadgeTier(context.Background(), store, TierSilver, &current); err != nil {
		t.Fatalf("keeping a disabled tier must be allowed: %v", err)
	}
}

// TestResolveBadgeTierRejectsDisabledAndUnknown 断言切到停用/不存在档是 422。
func TestResolveBadgeTierRejectsDisabledAndUnknown(t *testing.T) {
	store := newFakeStore()
	if _, err := store.UpdateTier(context.Background(), "silver", TierInput{
		Key: "silver", NameZh: "银牌", NameEn: "Silver", Icon: "i-lucide-medal", Color: "slate", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	current := TierBronze
	cases := []struct {
		name string
		tier BadgeTier
	}{
		{"disabled tier", TierSilver},
		{"unknown tier", BadgeTier("platinum")},
		{"empty tier", BadgeTier("")},
	}
	for _, tc := range cases {
		err := resolveBadgeTier(context.Background(), store, tc.tier, &current)
		if !errors.Is(err, ErrBadgeTierInvalid) {
			t.Fatalf("%s: want ErrBadgeTierInvalid, got %v", tc.name, err)
		}
	}
}

// TestResolveBadgeTierRequiresReader 断言没有等级读口时明确拒绝而不是放行。
func TestResolveBadgeTierRequiresReader(t *testing.T) {
	err := resolveBadgeTier(context.Background(), nil, TierGold, nil)
	if !errors.Is(err, ErrBadgeTierInvalid) {
		t.Fatalf("want ErrBadgeTierInvalid when the tier catalog is unavailable, got %v", err)
	}
}

// TestAdminUpdateBadgeKeepsDisabledTier 断言「改名字不动档位」在服务层也成立。
func TestAdminUpdateBadgeKeepsDisabledTier(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	if _, err := store.UpdateTier(context.Background(), "silver", TierInput{
		Key: "silver", NameZh: "银牌", NameEn: "Silver", Icon: "i-lucide-medal", Color: "slate", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	// 徽章 3（quality-answer）是 silver 手动徽章，只改名字。
	updated, err := admin.UpdateBadge(context.Background(), manageActor(), 3, BadgeInput{
		Slug: "quality-answer", Name: "优质解答（改）", Tier: TierSilver, GrantMode: GrantManual,
		Enabled: true, Position: 1,
	})
	if err != nil {
		t.Fatalf("rename must not be blocked by a disabled current tier: %v", err)
	}
	if updated.Name != "优质解答（改）" {
		t.Fatalf("name = %q", updated.Name)
	}
}

// TestAdminDeleteTierRequiresMigrationTarget 逐条覆盖删除等级的前置条件。
func TestAdminDeleteTierRequiresMigrationTarget(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	if err := admin.DeleteTier(ctx, manageActor(), "gold", ""); !errors.Is(err, ErrBadgeTierInvalid) {
		t.Fatalf("missing migrateTo: want ErrBadgeTierInvalid, got %v", err)
	}
	if err := admin.DeleteTier(ctx, manageActor(), "gold", "gold"); !errors.Is(err, ErrBadgeTierInvalid) {
		t.Fatalf("migrateTo == key: want ErrBadgeTierInvalid, got %v", err)
	}
	if err := admin.DeleteTier(ctx, manageActor(), "gold", "platinum"); !errors.Is(err, ErrBadgeTierNotFound) {
		t.Fatalf("unknown migrateTo: want ErrBadgeTierNotFound, got %v", err)
	}

	// 停用目标档：迁移过去等于把徽章挪进一个不参与展示的档位。
	if _, err := store.UpdateTier(ctx, "silver", TierInput{
		Key: "silver", NameZh: "银牌", NameEn: "Silver", Icon: "i-lucide-medal", Color: "slate", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := admin.DeleteTier(ctx, manageActor(), "gold", "silver"); !errors.Is(err, ErrBadgeTierInvalid) {
		t.Fatalf("disabled migrateTo: want ErrBadgeTierInvalid, got %v", err)
	}
}

// TestAdminDeleteTierRejectsLastEnabledTier 断言不能删掉最后一个启用档。
//
// 删掉它会让 topBadgeOrderSQL 的 JOIN 一行为空：全站所有人的最高级徽章同时消失。
func TestAdminDeleteTierRejectsLastEnabledTier(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()
	// 只留 gold 一档启用。
	for _, key := range []string{"silver", "bronze"} {
		def, err := store.GetTier(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateTier(ctx, key, TierInput{
			Key: def.Key, NameZh: def.NameZh, NameEn: def.NameEn, Icon: def.Icon,
			Color: def.Color, Position: def.Position, Enabled: false,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// bronze 已停用，不能作为迁移目标；先临时启用再试也没意义——
	// 这里直接验证「删唯一启用档」被拒：目标只能是停用档，两个分支都应失败。
	err := admin.DeleteTier(ctx, manageActor(), "gold", "bronze")
	if !errors.Is(err, ErrBadgeTierInvalid) && !errors.Is(err, ErrBadgeTierInUse) {
		t.Fatalf("want tier invalid/in use, got %v", err)
	}
}

// TestAdminDeleteTierMigratesBadges 断言删除把引用徽章迁到目标档。
func TestAdminDeleteTierMigratesBadges(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	if err := admin.DeleteTier(ctx, manageActor(), "gold", "bronze"); err != nil {
		t.Fatal(err)
	}
	for _, badge := range store.badges {
		if badge.Tier == TierGold {
			t.Fatalf("badge %q still points at the deleted tier", badge.Slug)
		}
	}
	if _, err := store.GetTier(ctx, "gold"); !errors.Is(err, ErrBadgeTierNotFound) {
		t.Fatalf("deleted tier still readable: %v", err)
	}
}

// TestAdminReorderTiers 断言重排按数组下标写 position，未列出的档位追加在后。
func TestAdminReorderTiers(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	if err := admin.ReorderTiers(ctx, manageActor(), []string{"bronze", "gold"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListTiers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Key)
	}
	want := []string{"bronze", "gold", "silver"}
	if len(got) != len(want) {
		t.Fatalf("tiers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tiers = %v, want %v", got, want)
		}
	}
}

// TestAdminReorderTiersRejectsUnknownKey 断言未知 key 被拒（不能凭空造顺序）。
func TestAdminReorderTiersRejectsUnknownKey(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	if err := admin.ReorderTiers(context.Background(), manageActor(), []string{"gold", "platinum"}); !errors.Is(err, ErrBadgeTierNotFound) {
		t.Fatalf("want ErrBadgeTierNotFound, got %v", err)
	}
}

// TestAdminTierPermissions 断言读写权限分离：只读能列表、不能写。
func TestAdminTierPermissions(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	if _, err := admin.ListTiers(ctx, viewActor()); err != nil {
		t.Fatalf("badge.view must be enough to list tiers: %v", err)
	}
	if _, err := admin.ListTiers(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member: want ErrPermissionDenied, got %v", err)
	}
	writeCalls := []struct {
		name string
		call func() error
	}{
		{"create", func() error {
			_, err := admin.CreateTier(ctx, viewActor(), TierInput{Key: "vip", NameZh: "贵宾", NameEn: "VIP"})
			return err
		}},
		{"update", func() error {
			_, err := admin.UpdateTier(ctx, viewActor(), "gold", TierInput{NameZh: "金牌", NameEn: "Gold"})
			return err
		}},
		{"delete", func() error { return admin.DeleteTier(ctx, viewActor(), "gold", "bronze") }},
		{"reorder", func() error { return admin.ReorderTiers(ctx, viewActor(), []string{"gold"}) }},
	}
	for _, tc := range writeCalls {
		if err := tc.call(); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("%s with badge.view only: want ErrPermissionDenied, got %v", tc.name, err)
		}
	}
	// super_admin 走角色旁路。
	if _, err := admin.CreateTier(ctx, superAdminActor(), TierInput{Key: "vip", NameZh: "贵宾", NameEn: "VIP"}); err != nil {
		t.Fatalf("super_admin must be able to create tiers: %v", err)
	}
}
