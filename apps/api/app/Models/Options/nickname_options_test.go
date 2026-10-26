package options

import (
	"context"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func nicknameOptionNameList() []string {
	return []string{
		NameProfileNicknameAllowUserChange,
		NameProfileNicknameCooldownMinutes,
		NameProfileNicknameMinLength,
		NameProfileNicknameMaxLength,
		NameProfileNicknameHistoryPublic,
	}
}

// 五个选项必须公开（资料设置页据此决定字段可编辑性与倒计时文案），
// 且由 settings.site.manage 管理，与同页的用户名策略保持一致。
func TestNicknameOptionsArePublicAndManageable(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	for _, name := range nicknameOptionNameList() {
		if _, ok := publicOptionValue(items, name); !ok {
			t.Fatalf("%s must be part of the public option projection", name)
		}
		definition, ok := optionDefinitionFor(name)
		if !ok {
			t.Fatalf("%s has no option definition", name)
		}
		if !definition.public {
			t.Fatalf("%s must be public", name)
		}
		if definition.managePermission != identity.PermissionSettingsSiteManage {
			t.Fatalf("%s manage permission = %q", name, definition.managePermission)
		}
	}
}

// 开箱默认：允许改、7 天冷却、2–24 字符、曾用名不公开。
func TestNicknameOptionDefaults(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	policy, err := NewNicknamePolicySource(service).NicknamePolicy(context.Background())
	if err != nil {
		t.Fatalf("NicknamePolicy returned error: %v", err)
	}
	want := NicknamePolicy{
		AllowUserChange: true,
		CooldownMinutes: 10080,
		MinLength:       2,
		MaxLength:       24,
		HistoryPublic:   false,
	}
	if policy != want {
		t.Fatalf("default nickname policy = %+v, want %+v", policy, want)
	}
}

func TestNicknameOptionNormalization(t *testing.T) {
	if value, ok := normalizeOptionValue(NameProfileNicknameCooldownMinutes, "0"); !ok || value != "0" {
		t.Fatalf("cooldown 0 must mean unlimited, got %q ok=%v", value, ok)
	}
	if _, ok := normalizeOptionValue(NameProfileNicknameCooldownMinutes, "-1"); ok {
		t.Fatal("negative cooldown must be rejected")
	}
	if _, ok := normalizeOptionValue(NameProfileNicknameCooldownMinutes, "525601"); ok {
		t.Fatal("cooldown above one year must be rejected")
	}
	if value, ok := normalizeOptionValue(NameProfileNicknameMinLength, "1"); !ok || value != "1" {
		t.Fatalf("min length 1 must be accepted, got %q ok=%v", value, ok)
	}
	if _, ok := normalizeOptionValue(NameProfileNicknameMaxLength, "1"); ok {
		t.Fatal("max length below 2 must be rejected")
	}
	if value, ok := normalizeOptionValue(NameProfileNicknameAllowUserChange, "off"); !ok || value != enabledOptionValue(false) {
		t.Fatalf("allow_user_change off normalized to %q ok=%v", value, ok)
	}
	if _, ok := normalizeOptionValue(NameProfileNicknameAllowUserChange, "maybe"); ok {
		t.Fatal("unknown boolean token must be rejected")
	}
}

// 脏数据与交叉约束都回退到推荐默认，而不是留下一个无人能通过的区间。
func TestNicknameOptionCoercionFallsBackToDefaults(t *testing.T) {
	defaults := nicknameRecommendedDefaults()
	values := map[string]string{
		NameProfileNicknameAllowUserChange: "nonsense",
		NameProfileNicknameCooldownMinutes: "-30",
		NameProfileNicknameMinLength:       "20",
		NameProfileNicknameMaxLength:       "4",
		NameProfileNicknameHistoryPublic:   "",
	}
	coerceNicknameOptions(values, defaults)

	if values[NameProfileNicknameAllowUserChange] != enabledOptionValue(true) {
		t.Fatalf("invalid boolean must fall back to enabled, got %q", values[NameProfileNicknameAllowUserChange])
	}
	if values[NameProfileNicknameCooldownMinutes] != "10080" {
		t.Fatalf("invalid cooldown must fall back to 10080, got %q", values[NameProfileNicknameCooldownMinutes])
	}
	if values[NameProfileNicknameMinLength] != "2" || values[NameProfileNicknameMaxLength] != "24" {
		t.Fatalf("inverted range must fall back to 2–24, got %q–%q",
			values[NameProfileNicknameMinLength], values[NameProfileNicknameMaxLength])
	}
	if values[NameProfileNicknameHistoryPublic] != enabledOptionValue(false) {
		t.Fatalf("empty history flag must fall back to disabled, got %q", values[NameProfileNicknameHistoryPublic])
	}
}

func TestNicknamePolicyReadsConfiguredValues(t *testing.T) {
	values := map[string]string{
		NameProfileNicknameAllowUserChange: enabledOptionValue(false),
		NameProfileNicknameCooldownMinutes: "90",
		NameProfileNicknameMinLength:       "1",
		NameProfileNicknameMaxLength:       "40",
		NameProfileNicknameHistoryPublic:   enabledOptionValue(true),
	}
	policy := nicknamePolicyFromValues(values)
	want := NicknamePolicy{
		AllowUserChange: false,
		CooldownMinutes: 90,
		MinLength:       1,
		MaxLength:       40,
		HistoryPublic:   true,
	}
	if policy != want {
		t.Fatalf("policy = %+v, want %+v", policy, want)
	}
}
