package options

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

func TestServiceReturnsDefaultSiteName(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	value, err := service.WebOption(context.Background(), NameSiteName)
	if err != nil {
		t.Fatalf("WebOption returned error: %v", err)
	}
	if value != "SForum" {
		t.Fatalf("expected default site name, got %q", value)
	}
}

func TestWelcomeMailDefaultsDisabledAndRequiresMailPermission(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, time.Minute)
	mailSettings := NewMailSettings(service)
	enabled, err := mailSettings.WelcomeMailEnabled(context.Background())
	if err != nil || enabled {
		t.Fatalf("welcome mail default enabled=%v err=%v", enabled, err)
	}
	noPermissionActor := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
	if _, err := service.Update(context.Background(), noPermissionActor, UpdateInput{Name: NameMailWelcomeEnabled, Value: "enabled"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("actor without settings authority should not manage welcome mail: %v", err)
	}
	mailActor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsMailManage: true}}
	if _, err := service.Update(context.Background(), mailActor, UpdateInput{Name: NameMailWelcomeEnabled, Value: "enabled"}); err != nil {
		t.Fatalf("mail settings actor should enable welcome mail: %v", err)
	}
	enabled, err = mailSettings.WelcomeMailEnabled(context.Background())
	if err != nil || !enabled {
		t.Fatalf("welcome mail enabled=%v err=%v", enabled, err)
	}
}

func TestMailSettingsSnapshotsConfiguredBrand(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{items: map[string]string{
		NameSiteName:        "蓝色论坛",
		NameSiteURL:         "https://forum.test",
		NameSiteLogoURL:     "/assets/logo.png",
		NameSiteFaviconURL:  "/assets/icon.png",
		NameAppearanceTheme: "ocean_blue",
	}}, time.Minute)
	brand, err := NewMailSettings(service).MailBrand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if brand.LogoURL != "https://forum.test/assets/logo.png" || brand.IconURL != "https://forum.test/assets/icon.png" || brand.AccentColor != "#2563eb" {
		t.Fatalf("mail brand = %#v", brand)
	}
}

func TestServiceListsOnlyPublicOptions(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameAltchaSecret: "secret",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	for _, item := range items {
		if item.Name == NameAltchaSecret {
			t.Fatalf("public list should not expose altcha secret: %#v", items)
		}
	}
	if adminValueFromPublic(items, NameHumanVerificationRegister) != "enabled" {
		t.Fatalf("expected public register verification scenario, got %#v", items)
	}
	if adminValueFromPublic(items, NameHumanVerificationPasswordReset) != "enabled" {
		t.Fatalf("expected public password reset verification scenario, got %#v", items)
	}
	if adminValueFromPublic(items, NameHumanVerificationEmailVerification) != "enabled" {
		t.Fatalf("expected public email verification delivery scenario, got %#v", items)
	}
	if adminValueFromPublic(items, NameHumanVerificationLoginRisk) != "disabled" {
		t.Fatalf("expected public login risk verification scenario, got %#v", items)
	}
	if adminValueFromPublic(items, NameHumanVerificationLoginAlways) != "disabled" {
		t.Fatalf("expected public login always trigger, got %#v", items)
	}
	for _, item := range items {
		switch item.Name {
		case NameHumanVerificationLoginNewDevice, NameHumanVerificationLoginPrivileged, NameHumanVerificationRateLimit:
			t.Fatalf("public list should not expose %s: %#v", item.Name, items)
		}
	}
	if adminValueFromPublic(items, NameSEOTwitterCard) != "summary_large_image" {
		t.Fatalf("expected public SEO option default, got %#v", items)
	}
}

func TestServiceForumOptionsArePublicWithRecommendedDefaults(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	if got := adminValueFromPublic(items, NameForumDefaultCategorySlug); got != "general" {
		t.Fatalf("expected public default category option, got %q", got)
	}
	if got := adminValueFromPublic(items, NameForumTagCreationMode); got != "controlled" {
		t.Fatalf("expected public tag creation mode option, got %q", got)
	}
	if got := adminValueFromPublic(items, NameForumTagPublicPages); got != "enabled" {
		t.Fatalf("expected public tag pages option, got %q", got)
	}
	if got := adminValueFromPublic(items, NameForumTagMaxPerTopic); got != "5" {
		t.Fatalf("expected public max tags option, got %q", got)
	}
	if got := adminValueFromPublic(items, NameForumTopicsPerPage); got != "20" {
		t.Fatalf("expected public topic page size option, got %q", got)
	}
	if got := adminValueFromPublic(items, NameForumCommentsPerPage); got != "20" {
		t.Fatalf("expected public comment page size option, got %q", got)
	}
}

func TestServiceForumPaginationOptionsValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	for _, name := range []string{NameForumTopicsPerPage, NameForumCommentsPerPage} {
		for _, value := range []string{"1", "100"} {
			if _, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: name, Value: value}); err != nil {
				t.Fatalf("expected %s=%s to be accepted: %v", name, value, err)
			}
		}
		for _, value := range []string{"0", "101"} {
			if _, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: name, Value: value}); !errors.Is(err, ErrInvalidOption) {
				t.Fatalf("expected %s=%s to be rejected, got %v", name, value, err)
			}
		}
		if _, err := service.Update(context.Background(), categoryManageActor(), UpdateInput{Name: name, Value: "30"}); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("expected category manager to be denied %s update, got %v", name, err)
		}
	}
}

func TestServicePasswordPolicyOptionsArePublicWithRecommendedDefaults(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	if got := adminValueFromPublic(items, NameIdentityPasswordMinLength); got != "12" {
		t.Fatalf("expected public min length default, got %q", got)
	}
	if got := adminValueFromPublic(items, NameIdentityPasswordMaxLength); got != "128" {
		t.Fatalf("expected public max length default, got %q", got)
	}
	if got := adminValueFromPublic(items, NameIdentityPasswordRequireUppercase); got != "disabled" {
		t.Fatalf("expected uppercase disabled default, got %q", got)
	}
}

func TestServiceMailResendPolicyDefaultsAreAdminOnly(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	policy, err := NewMailResendPolicyResolver(service).MailResendPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy != identity.RecommendedMailResendPolicy() {
		t.Fatalf("mail resend policy = %#v", policy)
	}
	adminItems, err := service.ListAdmin(context.Background(), settingsActor())
	if err != nil {
		t.Fatal(err)
	}
	if adminValue(adminItems, NameIdentityMailResendCooldownSeconds) != "30" ||
		adminValue(adminItems, NameIdentityMailResendWindowMinutes) != "60" ||
		adminValue(adminItems, NameIdentityMailResendMaxPerTarget) != "3" ||
		adminValue(adminItems, NameIdentityMailResendMaxPerIP) != "10" {
		t.Fatalf("unexpected admin mail resend defaults: %#v", adminItems)
	}
	publicItems, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range publicItems {
		if strings.HasPrefix(item.Name, "identity.mail_resend.") {
			t.Fatalf("mail resend option leaked through public options: %#v", item)
		}
	}
}

func TestServiceMailResendPolicyValidatesAndResolvesUpdates(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()
	updates := []UpdateInput{
		{Name: NameIdentityMailResendCooldownSeconds, Value: "0"},
		{Name: NameIdentityMailResendWindowMinutes, Value: "120"},
		{Name: NameIdentityMailResendMaxPerTarget, Value: "5"},
		{Name: NameIdentityMailResendMaxPerIP, Value: "25"},
	}
	if _, err := service.UpdateMany(context.Background(), actor, updates); err != nil {
		t.Fatal(err)
	}
	policy, err := NewMailResendPolicyResolver(service).MailResendPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Cooldown != 0 || policy.Window != 2*time.Hour || policy.MaxPerTarget != 5 || policy.MaxPerIP != 25 {
		t.Fatalf("updated mail resend policy = %#v", policy)
	}

	invalid := []UpdateInput{
		{Name: NameIdentityMailResendCooldownSeconds, Value: "3601"},
		{Name: NameIdentityMailResendWindowMinutes, Value: "0"},
		{Name: NameIdentityMailResendMaxPerTarget, Value: "101"},
		{Name: NameIdentityMailResendMaxPerIP, Value: "1001"},
	}
	for _, input := range invalid {
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid mail resend option %#v, got %v", input, err)
		}
	}
}

// TestServiceSessionsMaxDevicesDefaultNotPublic 验证：max_devices 默认值为推荐值 5，
// 且不暴露给公开 options 列表（仅后端登录时读取）。
func TestServiceSessionsMaxDevicesDefaultNotPublic(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	adminItems, err := service.ListAdmin(context.Background(), actor)
	if err != nil {
		t.Fatalf("ListAdmin returned error: %v", err)
	}
	if got := adminValue(adminItems, NameIdentitySessionsMaxDevices); got != "5" {
		t.Fatalf("expected max_devices default 5, got %q", got)
	}

	// 不应出现在公开 options 列表里。
	publicItems, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	for _, item := range publicItems {
		if item.Name == NameIdentitySessionsMaxDevices {
			t.Fatalf("expected max_devices to be non-public, but it appeared in public options")
		}
	}
}

// TestServiceSessionsMaxDevicesValidation 验证：合法值（1-20）可更新，越界值被拒绝。
func TestServiceSessionsMaxDevicesValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	// 合法值可更新。
	updated, err := service.Update(context.Background(), actor, UpdateInput{Name: NameIdentitySessionsMaxDevices, Value: "3"})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := updated.Value; got != "3" {
		t.Fatalf("expected max_devices 3, got %q", got)
	}

	// 越界值被拒绝。
	for _, invalid := range []string{"0", "-1", "21", "abc", ""} {
		if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameIdentitySessionsMaxDevices, Value: invalid}); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid max_devices %q to be rejected, got %v", invalid, err)
		}
	}
}

func TestServiceAvatarOptionsArePublicWithRecommendedDefaults(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	items, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	resolved, err := service.AvatarOptions(context.Background())
	if err != nil {
		t.Fatalf("AvatarOptions returned error: %v", err)
	}

	if got := adminValueFromPublic(items, NameAvatarAllowUpload); got != "enabled" {
		t.Fatalf("expected avatar uploads enabled by default, got %q", got)
	}
	if got := adminValueFromPublic(items, NameAvatarDefaultProvider); got != AvatarProviderInitials {
		t.Fatalf("expected initials default provider, got %q", got)
	}
	if got := adminValueFromPublic(items, NameAvatarGravatarHashAlgorithm); got != AvatarHashSHA256 {
		t.Fatalf("expected sha256 gravatar hash default, got %q", got)
	}
	if got := adminValueFromPublic(items, NameAvatarAllowGIF); got != "disabled" {
		t.Fatalf("expected GIF disabled by default, got %q", got)
	}
	if resolved.DefaultProvider != AvatarProviderInitials || resolved.GravatarHashAlgorithm != AvatarHashSHA256 {
		t.Fatalf("unexpected resolved avatar options: %#v", resolved)
	}
	if resolved.MaxSizeKB != 2048 || resolved.MaxDimension != 2048 || resolved.TargetDimension != 256 || resolved.CompressQuality != 85 {
		t.Fatalf("unexpected avatar numeric defaults: %#v", resolved)
	}
}

func TestServiceAvatarOptionsValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAvatarDefaultProvider, Value: "identicon"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected identicon provider to be invalid, got %v", err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAvatarGravatarHashAlgorithm, Value: "sha1"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid gravatar hash algorithm, got %v", err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAvatarGravatarBaseURL, Value: "javascript:alert(1)"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid gravatar base URL, got %v", err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAvatarDefaultProvider, Value: AvatarProviderStatic}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected static provider without default URL to be invalid, got %v", err)
	}

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameAvatarDefaultProvider, Value: AvatarProviderStatic},
		{Name: NameAvatarDefaultStaticURL, Value: "https://cdn.example.com/avatar.png"},
		{Name: NameAvatarGravatarHashAlgorithm, Value: AvatarHashMD5},
		{Name: NameAvatarAllowGIF, Value: "true"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameAvatarDefaultProvider); got != AvatarProviderStatic {
		t.Fatalf("expected static provider, got %q", got)
	}
	if got := adminValue(updated, NameAvatarGravatarHashAlgorithm); got != AvatarHashMD5 {
		t.Fatalf("expected md5 hash algorithm, got %q", got)
	}
	if got := adminValue(updated, NameAvatarAllowGIF); got != "enabled" {
		t.Fatalf("expected normalized GIF toggle, got %q", got)
	}
}

func TestServicePasswordPolicyOptionsValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityPasswordMinLength, Value: "14"},
		{Name: NameIdentityPasswordMaxLength, Value: "160"},
		{Name: NameIdentityPasswordRequireLowercase, Value: "true"},
		{Name: NameIdentityPasswordRequireNumber, Value: "enabled"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameIdentityPasswordMinLength); got != "14" {
		t.Fatalf("expected min length 14, got %q", got)
	}
	if got := adminValue(updated, NameIdentityPasswordRequireLowercase); got != "enabled" {
		t.Fatalf("expected lowercase enabled, got %q", got)
	}

	cases := []UpdateInput{
		{Name: NameIdentityPasswordMinLength, Value: "7"},
		{Name: NameIdentityPasswordMinLength, Value: "129"},
		{Name: NameIdentityPasswordMaxLength, Value: "63"},
		{Name: NameIdentityPasswordMaxLength, Value: "513"},
		{Name: NameIdentityPasswordRequireSymbol, Value: "sometimes"},
	}
	for _, input := range cases {
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid password option for %#v, got %v", input, err)
		}
	}

	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityPasswordMinLength, Value: "80"},
		{Name: NameIdentityPasswordMaxLength, Value: "64"},
	}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected max below min to be invalid, got %v", err)
	}
}

func TestServiceResolvesPasswordPolicy(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameIdentityPasswordMinLength:        "14",
		NameIdentityPasswordMaxLength:        "160",
		NameIdentityPasswordRequireUppercase: "enabled",
		NameIdentityPasswordRequireSymbol:    "enabled",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)

	policy, err := service.PasswordPolicy(context.Background())
	if err != nil {
		t.Fatalf("PasswordPolicy returned error: %v", err)
	}
	if policy.MinLength != 14 || policy.MaxLength != 160 || !policy.RequireUppercase || !policy.RequireSymbol {
		t.Fatalf("unexpected policy: %#v", policy)
	}
}

func TestServiceAdminListMasksSecrets(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameAltchaSecret: "secret",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()

	items, err := service.ListAdmin(context.Background(), actor)
	if err != nil {
		t.Fatalf("ListAdmin returned error: %v", err)
	}

	var secret optionsAdminItem
	for _, item := range items {
		if item.Name == NameAltchaSecret {
			secret = optionsAdminItem{Value: item.Value, Secret: item.Secret, SecretSet: item.SecretSet}
		}
	}
	if !secret.Secret || !secret.SecretSet || secret.Value != "" {
		t.Fatalf("expected masked configured secret, got %#v", secret)
	}
	if adminValue(items, NameSEOMetaDescription) != "" {
		t.Fatalf("settings actor should not see seo options: %#v", items)
	}
}

func TestServiceAdminListAllowsSEOManagersToSeeSEOOptionsOnly(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	items, err := service.ListAdmin(context.Background(), seoActor())
	if err != nil {
		t.Fatalf("ListAdmin returned error: %v", err)
	}

	if adminValue(items, NameSEOTwitterCard) != "summary_large_image" {
		t.Fatalf("expected SEO option default, got %#v", items)
	}
	if adminValue(items, NameSiteName) != "" {
		t.Fatalf("seo actor should not see settings options: %#v", items)
	}
}

func TestServiceUsesCacheUntilInvalidated(t *testing.T) {
	store := &fakeStore{items: map[string]string{NameSiteName: "First"}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	ctx := context.Background()

	first, err := service.WebOption(ctx, NameSiteName)
	if err != nil {
		t.Fatalf("first WebOption returned error: %v", err)
	}
	store.items[NameSiteName] = "Second"
	second, err := service.WebOption(ctx, NameSiteName)
	if err != nil {
		t.Fatalf("second WebOption returned error: %v", err)
	}
	service.Invalidate()
	third, err := service.WebOption(ctx, NameSiteName)
	if err != nil {
		t.Fatalf("third WebOption returned error: %v", err)
	}

	if first != "First" || second != "First" || third != "Second" {
		t.Fatalf("expected cache then invalidation, got %q %q %q", first, second, third)
	}
	if store.listCalls != 2 {
		t.Fatalf("expected 2 list calls, got %d", store.listCalls)
	}
}

func TestForumReadPolicySnapshotNeverReadsStore(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameForumGuestRead:                    "login_required",
		NameForumCommentsSoftDeleteVisibility: "staff_only",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	if _, _, _, ok := service.ForumReadPolicySnapshot(); ok {
		t.Fatal("unpublished policy must be unavailable")
	}
	if err := service.RefreshForumReadPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	guestRead, softDeleteVisibility, revision, ok := service.ForumReadPolicySnapshot()
	if !ok || guestRead != "login_required" || softDeleteVisibility != "staff_only" || revision == 0 {
		t.Fatalf("snapshot = %q %q %d %v", guestRead, softDeleteVisibility, revision, ok)
	}
	for range 100 {
		if _, _, _, ok := service.ForumReadPolicySnapshot(); !ok {
			t.Fatal("published policy disappeared")
		}
	}
	if store.listCalls != 1 {
		t.Fatalf("snapshot reads reached Store: calls=%d", store.listCalls)
	}
}

func TestForumReadPolicySnapshotInvalidatesAndRepublishesOnUpdate(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, time.Minute)
	ctx := context.Background()
	if err := service.RefreshForumReadPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	beforeGuest, beforeSoftDelete, beforeRevision, ok := service.ForumReadPolicySnapshot()
	if !ok || beforeGuest != "public" || beforeSoftDelete != "author_and_staff" {
		t.Fatalf("initial snapshot = %q %q %d %v", beforeGuest, beforeSoftDelete, beforeRevision, ok)
	}
	if _, err := service.Update(ctx, settingsActor(), UpdateInput{Name: NameForumGuestRead, Value: "login_required"}); err != nil {
		t.Fatal(err)
	}
	afterGuest, afterSoftDelete, afterRevision, ok := service.ForumReadPolicySnapshot()
	if !ok || afterGuest != "login_required" || afterSoftDelete != "author_and_staff" || afterRevision <= beforeRevision {
		t.Fatalf("updated snapshot = %q %q %d %v", afterGuest, afterSoftDelete, afterRevision, ok)
	}
	service.Invalidate()
	if _, _, _, ok := service.ForumReadPolicySnapshot(); ok {
		t.Fatal("invalidated policy remained available")
	}
}

func TestForumReadPolicySnapshotExpiresWithoutRequestTimeRefresh(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, 15*time.Millisecond)
	if err := service.RefreshForumReadPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, _, _, ok := service.ForumReadPolicySnapshot(); ok {
		t.Fatal("expired policy remained available")
	}
	if store.listCalls != 1 {
		t.Fatalf("snapshot expiry performed I/O: calls=%d", store.listCalls)
	}
	if err := service.RefreshForumReadPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := service.ForumReadPolicySnapshot(); !ok || store.listCalls != 2 {
		t.Fatalf("background-style refresh did not republish: ok=%v calls=%d", ok, store.listCalls)
	}
}

func TestForumReadPolicyRefreshExtendsSnapshotBeforeCacheExpiry(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, 80*time.Millisecond)
	ctx := context.Background()
	if err := service.RefreshForumReadPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := service.RefreshForumReadPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	if store.listCalls != 2 {
		t.Fatalf("policy refresh reused stale Options cache: calls=%d", store.listCalls)
	}

	// 已越过首次快照的有效期，但第二次成功刷新应保证策略连续可用。
	time.Sleep(50 * time.Millisecond)
	if _, _, _, ok := service.ForumReadPolicySnapshot(); !ok {
		t.Fatal("successful periodic refresh left an expiry gap")
	}
}

func TestForumReadPolicyRefreshFailureEventuallyFailsClosed(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, 60*time.Millisecond)
	ctx := context.Background()
	if err := service.RefreshForumReadPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	store.listErr = errors.New("database unavailable")
	if err := service.RefreshForumReadPolicy(ctx); !errors.Is(err, store.listErr) {
		t.Fatalf("refresh error = %v", err)
	}
	if _, _, _, ok := service.ForumReadPolicySnapshot(); !ok {
		t.Fatal("one failed refresh discarded a still-valid snapshot")
	}

	time.Sleep(50 * time.Millisecond)
	if _, _, _, ok := service.ForumReadPolicySnapshot(); ok {
		t.Fatal("snapshot remained available after refresh failure exceeded TTL")
	}
	if store.listCalls != 2 {
		t.Fatalf("snapshot reads reached Store after failure: calls=%d", store.listCalls)
	}
}

func TestServiceUpdateRequiresSettingsPermission(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.Update(context.Background(), actor, UpdateInput{Name: NameSiteName, Value: "Example"})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceUpdateInvalidatesCache(t *testing.T) {
	store := &fakeStore{items: map[string]string{NameSiteName: "First"}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	ctx := context.Background()
	actor := settingsActor()

	if _, err := service.WebOption(ctx, NameSiteName); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	updated, err := service.Update(ctx, actor, UpdateInput{Name: NameSiteName, Value: "  Example Forum  "})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	value, err := service.WebOption(ctx, NameSiteName)
	if err != nil {
		t.Fatalf("WebOption after update returned error: %v", err)
	}

	if updated.Value != "Example Forum" || value != "Example Forum" {
		t.Fatalf("expected updated value, got option=%q value=%q", updated.Value, value)
	}
}

func TestServiceUpdateManyValidatesMergedLocaleSettings(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	_, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameSiteSupportedLocales, Value: "en-US"},
	})
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected default locale outside supported locales to be invalid, got %v", err)
	}

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameSiteSupportedLocales, Value: "en-US"},
		{Name: NameSiteDefaultLocale, Value: "en"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if value := adminValue(updated, NameSiteDefaultLocale); value != "en-US" {
		t.Fatalf("expected normalized default locale, got %q", value)
	}
}

func TestServiceUpdateManyKeepsBlankSecretAndRequiresSecretForAltcha(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameAltchaSecret: "existing-secret",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameAltchaSecret, Value: "   "},
		{Name: NameHumanVerificationProvider, Value: "altcha"},
	})
	if err != nil {
		t.Fatalf("UpdateMany should keep existing secret: %v", err)
	}
	if store.items[NameAltchaSecret] != "existing-secret" {
		t.Fatalf("expected blank secret update to keep existing secret, got %q", store.items[NameAltchaSecret])
	}
	if item := adminSecret(updated, NameAltchaSecret); !item.SecretSet || item.Value != "" {
		t.Fatalf("expected masked existing secret after update, got %#v", item)
	}

	emptySecretService := NewServiceWithDefaultsAndCacheTTL(&fakeStore{}, Defaults{AltchaSecret: ""}, time.Minute)
	_, err = emptySecretService.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameHumanVerificationProvider, Value: "altcha"},
	})
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected altcha without secret to be invalid, got %v", err)
	}
}

func TestServiceHumanVerificationScenarioOptions(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameAltchaSecret: "existing-secret",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameHumanVerificationProvider, Value: "altcha"},
		{Name: NameHumanVerificationRegister, Value: "disabled"},
		{Name: NameHumanVerificationEmailVerification, Value: "enabled"},
		{Name: NameHumanVerificationLoginRisk, Value: "true"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameHumanVerificationRegister); got != "disabled" {
		t.Fatalf("expected disabled register scenario, got %q", got)
	}
	if got := adminValue(updated, NameHumanVerificationLoginRisk); got != "enabled" {
		t.Fatalf("expected enabled login risk scenario, got %q", got)
	}

	cfg, err := service.HumanVerificationConfig(context.Background())
	if err != nil {
		t.Fatalf("HumanVerificationConfig returned error: %v", err)
	}
	if cfg.PurposeEnabled[humanverify.PurposeRegister] {
		t.Fatal("expected register purpose to be disabled")
	}
	if !cfg.PurposeEnabled[humanverify.PurposeLoginRisk] {
		t.Fatal("expected login risk purpose to be enabled")
	}
	if !cfg.PurposeEnabled[humanverify.PurposeEmailVerification] {
		t.Fatal("expected email verification delivery purpose to be enabled")
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameHumanVerificationPostRisk, Value: "sometimes"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid scenario value, got %v", err)
	}
}

func TestServiceHumanVerificationLoginTriggersAndRateLimit(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameAltchaSecret: "existing-secret",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()
	ctx := context.Background()

	policy, err := service.LoginVerificationPolicy(ctx)
	if err != nil {
		t.Fatalf("LoginVerificationPolicy returned error: %v", err)
	}
	if policy.Enabled || policy.Always || policy.NewDevice || policy.Privileged {
		t.Fatalf("expected all login triggers disabled by default, got %#v", policy)
	}
	cfg, err := service.HumanVerificationConfig(ctx)
	if err != nil {
		t.Fatalf("HumanVerificationConfig returned error: %v", err)
	}
	if cfg.RateLimit != humanVerificationRateLimitDefault {
		t.Fatalf("expected default rate limit %d, got %d", humanVerificationRateLimitDefault, cfg.RateLimit)
	}

	updated, err := service.UpdateMany(ctx, actor, []UpdateInput{
		{Name: NameHumanVerificationProvider, Value: "altcha"},
		{Name: NameHumanVerificationLoginRisk, Value: "enabled"},
		{Name: NameHumanVerificationLoginAlways, Value: "enabled"},
		{Name: NameHumanVerificationLoginNewDevice, Value: "true"},
		{Name: NameHumanVerificationRateLimit, Value: "120"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameHumanVerificationLoginNewDevice); got != "enabled" {
		t.Fatalf("expected normalized new device trigger, got %q", got)
	}
	policy, err = service.LoginVerificationPolicy(ctx)
	if err != nil {
		t.Fatalf("LoginVerificationPolicy returned error: %v", err)
	}
	if !policy.Enabled || !policy.Always || !policy.NewDevice || policy.Privileged {
		t.Fatalf("unexpected login trigger policy: %#v", policy)
	}
	cfg, err = service.HumanVerificationConfig(ctx)
	if err != nil {
		t.Fatalf("HumanVerificationConfig returned error: %v", err)
	}
	if cfg.RateLimit != 120 {
		t.Fatalf("expected configured rate limit 120, got %d", cfg.RateLimit)
	}

	if _, err := service.Update(ctx, actor, UpdateInput{Name: NameHumanVerificationRateLimit, Value: "0"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected out-of-range rate limit to be rejected, got %v", err)
	}
	if _, err := service.Update(ctx, actor, UpdateInput{Name: NameHumanVerificationLoginAlways, Value: "sometimes"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid trigger value to be rejected, got %v", err)
	}
}

func TestServiceAltchaWidgetOptions(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameAltchaWidgetType, Value: " switch "},
		{Name: NameAltchaWidgetAuto, Value: "ONFOCUS"},
		{Name: NameAltchaWidgetDisplay, Value: "floating"},
		{Name: NameAltchaWidgetHideLogo, Value: "false"},
		{Name: NameAltchaWidgetHideFooter, Value: "true"},
		{Name: NameAltchaWidgetWorkers, Value: "4"},
		{Name: NameAltchaWidgetMinDuration, Value: "1200"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameAltchaWidgetType); got != "switch" {
		t.Fatalf("expected normalized widget type, got %q", got)
	}
	if got := adminValue(updated, NameAltchaWidgetAuto); got != "onfocus" {
		t.Fatalf("expected normalized auto mode, got %q", got)
	}
	if got := adminValue(updated, NameAltchaWidgetHideLogo); got != "disabled" {
		t.Fatalf("expected hide logo boolean option, got %q", got)
	}
	if got := adminValue(updated, NameAltchaWidgetWorkers); got != "4" {
		t.Fatalf("expected normalized workers, got %q", got)
	}

	cases := []UpdateInput{
		{Name: NameAltchaWidgetType, Value: "button"},
		{Name: NameAltchaWidgetAuto, Value: "always"},
		{Name: NameAltchaWidgetDisplay, Value: "modal"},
		{Name: NameAltchaWidgetWorkers, Value: "0"},
		{Name: NameAltchaWidgetWorkers, Value: "17"},
		{Name: NameAltchaWidgetMinDuration, Value: "-1"},
		{Name: NameAltchaWidgetMinDuration, Value: "10001"},
	}
	for _, input := range cases {
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid widget option for %#v, got %v", input, err)
		}
	}
}

func TestServiceSEOOptionsDefaultsAndValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := seoActor()

	twitterCard, err := service.WebOption(context.Background(), NameSEOTwitterCard)
	if err != nil {
		t.Fatalf("default twitter card returned error: %v", err)
	}
	if twitterCard != "summary_large_image" {
		t.Fatalf("expected default summary_large_image card, got %q", twitterCard)
	}

	// seo.topic_url_mode 默认 id_slug。
	mode, err := service.WebOption(context.Background(), NameSEOTopicURLMode)
	if err != nil {
		t.Fatalf("default topic url mode returned error: %v", err)
	}
	if mode != "id_slug" {
		t.Fatalf("expected default topic url mode id_slug, got %q", mode)
	}

	updated, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameSEOMetaTitleTemplate, Value: "  {title} - {siteName}  "},
		{Name: NameSEOOGImageURL, Value: "https://example.com/og.png"},
		{Name: NameSEOTwitterCard, Value: "summary"},
		{Name: NameSEOTwitterSite, Value: "sforum_app"},
		{Name: NameSEORobotsExtraDisallow, Value: "/admin\n/private"},
		{Name: NameSEOBingVerification, Value: "bing-token"},
		// 帖子 URL 模式：合法枚举 slug 被接受，大小写归一。
		{Name: NameSEOTopicURLMode, Value: " SLUG "},
		// 后台 SEO 页默认 payload 会带上各内容类型的 schema_type（PascalCase）。
		{Name: seoContentOptionName("category", "schema_type"), Value: "CollectionPage"},
		{Name: seoContentOptionName("tag", "schema_type"), Value: "CollectionPage"},
		{Name: seoContentOptionName("topic", "schema_type"), Value: "DiscussionForumPosting"},
		{Name: seoContentOptionName("profile", "schema_type"), Value: "ProfilePage"},
		{Name: seoContentOptionName("static", "schema_type"), Value: "WebPage"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameSEOMetaTitleTemplate); got != "{title} - {siteName}" {
		t.Fatalf("expected normalized title template, got %q", got)
	}
	if got := adminValue(updated, NameSEOTwitterSite); got != "@sforum_app" {
		t.Fatalf("expected normalized twitter site, got %q", got)
	}
	if got := adminValue(updated, NameSEORobotsExtraDisallow); got != "/admin\n/private" {
		t.Fatalf("expected normalized robots paths, got %q", got)
	}
	if got := adminValue(updated, NameSEOTopicURLMode); got != "slug" {
		t.Fatalf("expected normalized topic url mode slug, got %q", got)
	}
	if got := adminValue(updated, seoContentOptionName("topic", "schema_type")); got != "DiscussionForumPosting" {
		t.Fatalf("expected topic schema_type DiscussionForumPosting, got %q", got)
	}

	cases := []UpdateInput{
		{Name: NameSEOOGImageURL, Value: "notaurl"},
		{Name: NameSEOTwitterCard, Value: "large"},
		{Name: NameSEORobotsExtraAllow, Value: "relative"},
		{Name: NameSEOGoogleVerification, Value: "<script>"},
		{Name: NameSEOMetaDescription, Value: stringsOfRunes("长", 321)},
		// 帖子 URL 模式必须是合法枚举之一。
		{Name: NameSEOTopicURLMode, Value: "category"},
		// 未知 Schema.org 类型拒绝。
		{Name: seoContentOptionName("topic", "schema_type"), Value: "Article"},
	}
	for _, input := range cases {
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid SEO option for %#v, got %v", input, err)
		}
	}
}

func TestServiceSEOOptionsRequireSEOManagePermission(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	settings := settingsActor()
	seo := seoActor()

	if _, err := service.Update(context.Background(), settings, UpdateInput{Name: NameSEOMetaDescription, Value: "desc"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected settings actor to be denied SEO update, got %v", err)
	}
	if _, err := service.Update(context.Background(), seo, UpdateInput{Name: NameSiteName, Value: "Example"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected SEO actor to be denied settings update, got %v", err)
	}
	if _, err := service.UpdateMany(context.Background(), seo, []UpdateInput{
		{Name: NameSEOMetaDescription, Value: "desc"},
		{Name: NameSiteName, Value: "Example"},
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected mixed permission batch to be denied, got %v", err)
	}
	if _, err := service.Update(context.Background(), seo, UpdateInput{Name: NameSEOMetaDescription, Value: "desc"}); err != nil {
		t.Fatalf("expected SEO actor to update SEO option, got %v", err)
	}
}

func TestServiceAttachmentOptionsRequireAttachmentSettingsPermission(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	if _, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameAttachmentMaxFileSizeMB, Value: "50"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected settings actor to be denied attachment option update, got %v", err)
	}

	updated, err := service.Update(context.Background(), attachmentSettingsActor(), UpdateInput{Name: NameAttachmentMaxFileSizeMB, Value: "50"})
	if err != nil {
		t.Fatalf("expected attachment settings actor to update option: %v", err)
	}
	if updated.Value != "50" {
		t.Fatalf("expected normalized max file size 50, got %q", updated.Value)
	}
}

func TestServiceForumOptionsRequireForumPermissions(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	categoryItems, err := service.ListAdmin(context.Background(), categoryManageActor())
	if err != nil {
		t.Fatalf("category manager ListAdmin returned error: %v", err)
	}
	if got := adminValue(categoryItems, NameForumDefaultCategorySlug); got != "general" {
		t.Fatalf("expected category manager to see default category option, got %q", got)
	}
	if got := adminValue(categoryItems, NameForumTagCreationMode); got != "" {
		t.Fatalf("category manager should not see tag option, got %q", got)
	}

	tagItems, err := service.ListAdmin(context.Background(), tagManageActor())
	if err != nil {
		t.Fatalf("tag manager ListAdmin returned error: %v", err)
	}
	if got := adminValue(tagItems, NameForumTagCreationMode); got != "controlled" {
		t.Fatalf("expected tag manager to see creation mode option, got %q", got)
	}
	if got := adminValue(tagItems, NameForumTagPublicPages); got != "enabled" {
		t.Fatalf("expected tag manager to see public pages option, got %q", got)
	}
	if got := adminValue(tagItems, NameForumTagMaxPerTopic); got != "5" {
		t.Fatalf("expected tag manager to see max tags option, got %q", got)
	}
	if got := adminValue(tagItems, NameForumDefaultCategorySlug); got != "" {
		t.Fatalf("tag manager should not see default category option, got %q", got)
	}

	if _, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameForumDefaultCategorySlug, Value: "support"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected settings actor to be denied default category update, got %v", err)
	}
	if _, err := service.Update(context.Background(), categoryManageActor(), UpdateInput{Name: NameForumTagCreationMode, Value: "open"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected category manager to be denied tag option update, got %v", err)
	}
}

func TestServiceForumOptionsDefaultsAndValidation(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, time.Minute)

	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults returned error: %v", err)
	}
	expected := map[string]string{
		NameForumDefaultCategorySlug:    "general",
		NameForumTagCreationMode:        "controlled",
		NameForumTagPublicPages:         "enabled",
		NameForumTagMinPerTopic:         "0",
		NameForumTagMaxPerTopic:         "5",
		NameForumTopicTitleMinRunes:     "2",
		NameForumTopicTitleMaxRunes:     "100",
		NameForumCommentMaxNestingDepth: "20",
		NameForumExcerptRuneLimit:       "180",
	}
	for name, want := range expected {
		if got := store.items[name]; got != want {
			t.Fatalf("expected default %s=%q, got %q", name, want, got)
		}
	}

	updated, err := service.UpdateMany(context.Background(), tagManageActor(), []UpdateInput{
		{Name: NameForumTagCreationMode, Value: "OPEN"},
		{Name: NameForumTagPublicPages, Value: "false"},
		{Name: NameForumTagMaxPerTopic, Value: "0"},
	})
	if err != nil {
		t.Fatalf("UpdateMany returned error: %v", err)
	}
	if got := adminValue(updated, NameForumTagCreationMode); got != "open" {
		t.Fatalf("expected normalized open mode, got %q", got)
	}
	if got := adminValue(updated, NameForumTagPublicPages); got != "disabled" {
		t.Fatalf("expected disabled public pages, got %q", got)
	}
	if got := adminValue(updated, NameForumTagMaxPerTopic); got != "0" {
		t.Fatalf("expected normalized max tags 0, got %q", got)
	}

	cases := []UpdateInput{
		{Name: NameForumTagCreationMode, Value: "invite"},
		{Name: NameForumTagMaxPerTopic, Value: "-1"},
		{Name: NameForumTagMaxPerTopic, Value: "11"},
		{Name: NameForumDefaultCategorySlug, Value: ""},
		{Name: NameForumDefaultCategorySlug, Value: "不合法"},
	}
	for _, input := range cases {
		actor := tagManageActor()
		if input.Name == NameForumDefaultCategorySlug {
			actor = categoryManageActor()
		}
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid forum option for %#v, got %v", input, err)
		}
	}
}

func TestServiceAttachmentLocalRootDefaultsAndValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := attachmentSettingsActor()

	items, err := service.ListAdmin(context.Background(), actor)
	if err != nil {
		t.Fatalf("ListAdmin returned error: %v", err)
	}
	if got := adminValue(items, NameAttachmentLocalRoot); got != "storage/app/attachments" {
		t.Fatalf("expected default attachment local root, got %q", got)
	}

	updated, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAttachmentLocalRoot, Value: " storage/forum-uploads "})
	if err != nil {
		t.Fatalf("expected relative attachment local root to be valid: %v", err)
	}
	if updated.Value != "storage/forum-uploads" {
		t.Fatalf("expected normalized relative root, got %q", updated.Value)
	}

	updated, err = service.Update(context.Background(), actor, UpdateInput{Name: NameAttachmentLocalRoot, Value: "/srv/sforum/uploads"})
	if err != nil {
		t.Fatalf("expected absolute attachment local root to be valid: %v", err)
	}
	if updated.Value != "/srv/sforum/uploads" {
		t.Fatalf("expected normalized absolute root, got %q", updated.Value)
	}

	cases := []string{
		"",
		"/",
		"../uploads",
		"storage/../uploads",
		"storage/uploads\nnext",
		"storage/<uploads>",
	}
	for _, value := range cases {
		if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAttachmentLocalRoot, Value: value}); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid attachment local root %q, got %v", value, err)
		}
	}
}

func TestServiceAttachmentOptionsValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := attachmentSettingsActor()

	cases := []UpdateInput{
		{Name: NameAttachmentPathTemplate, Value: "../{public_id}{ext}"},
		{Name: NameAttachmentPathTemplate, Value: "uploads/{ext}"},
		{Name: NameAttachmentAllowedExtensions, Value: ".jpg,../sh"},
		{Name: NameAttachmentAllowedMIMETypes, Value: "not-a-mime"},
		{Name: NameAttachmentMaxFileSizeMB, Value: "0"},
	}
	for _, input := range cases {
		if _, err := service.Update(context.Background(), actor, input); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid attachment option for %#v, got %v", input, err)
		}
	}

	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAttachmentProvider, Value: "aliyun_oss"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected removed core provider to be invalid, got %v", err)
	}
}

func TestServicePersonalizationDefaultsAndValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	theme, err := service.WebOption(context.Background(), NameAppearanceTheme)
	if err != nil {
		t.Fatalf("default theme returned error: %v", err)
	}
	if theme != "pine_teal" {
		t.Fatalf("expected default pine teal theme, got %q", theme)
	}
	background, err := service.WebOption(context.Background(), NameAppearanceLightBackground)
	if err != nil || background != "pure_white" {
		t.Fatalf("expected default pure white background, got %q err=%v", background, err)
	}
	footerCopyright, err := service.WebOption(context.Background(), NameFooterCopyrightZHCN)
	if err != nil {
		t.Fatalf("default footer copyright returned error: %v", err)
	}
	if footerCopyright != "© {year} {siteName}。保留所有权利。" {
		t.Fatalf("expected recommended footer copyright, got %q", footerCopyright)
	}
	footerLinks, err := service.WebOption(context.Background(), NameFooterLinks)
	if err != nil {
		t.Fatalf("default footer links returned error: %v", err)
	}
	var links []footerLinkOption
	if err := json.Unmarshal([]byte(footerLinks), &links); err != nil {
		t.Fatalf("default footer links are invalid JSON: %v", err)
	}
	if len(links) != 3 || links[0].Key != "terms" || links[0].Labels.ZHCN != "服务条款" {
		t.Fatalf("expected recommended footer links, got %#v", links)
	}

	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceTheme, Value: "neon"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid custom theme, got %v", err)
	}
	customTheme, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceTheme, Value: "custom:#4F46E5"})
	if err != nil {
		t.Fatalf("expected valid custom theme color, got %v", err)
	}
	if customTheme.Value != "custom:#4f46e5" {
		t.Fatalf("expected normalized custom theme color, got %q", customTheme.Value)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceTheme, Value: "custom:not-a-color"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid custom theme color, got %v", err)
	}
	backgroundOption, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceLightBackground, Value: " PAPER "})
	if err != nil || backgroundOption.Value != "paper" {
		t.Fatalf("expected normalized paper background, got %#v err=%v", backgroundOption, err)
	}
	backgroundOption, err = service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceLightBackground, Value: " MORNING_APRICOT "})
	if err != nil || backgroundOption.Value != "morning_apricot" {
		t.Fatalf("expected normalized morning apricot background, got %#v err=%v", backgroundOption, err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameAppearanceLightBackground, Value: "night_blue"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected unknown light background to be invalid, got %v", err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameFooterCopyrightZHCN, Value: stringsOfRunes("长", 201)}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected oversized footer copyright to be invalid, got %v", err)
	}
}

func TestServiceNormalizesFooterLinks(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()
	value := `[
		{"key":"privacy","labels":{"zh-CN":"隐私","en-US":"Privacy"},"url":"/privacy"},
		{"key":"guidelines","labels":{"zh-CN":"指南","en-US":"Guidelines"},"url":""},
		{"key":"terms","labels":{"zh-CN":"条款","en-US":"Terms"},"url":"https://example.com/terms"}
	]`

	updated, err := service.Update(context.Background(), actor, UpdateInput{Name: NameFooterLinks, Value: value})
	if err != nil {
		t.Fatalf("Update footer links returned error: %v", err)
	}

	got := updated.Value
	want := `[{"key":"terms","labels":{"zh-CN":"条款","en-US":"Terms"},"url":"https://example.com/terms"},{"key":"privacy","labels":{"zh-CN":"隐私","en-US":"Privacy"},"url":"/privacy"},{"key":"guidelines","labels":{"zh-CN":"指南","en-US":"Guidelines"},"url":""}]`
	if got != want {
		t.Fatalf("expected normalized footer links\nwant: %s\n got: %s", want, got)
	}
}

func TestServiceRejectsInvalidFooterLinks(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	cases := []string{
		`not-json`,
		`[{"key":"terms","labels":{"zh-CN":"条款","en-US":"Terms"},"url":"mailto:test@example.com"},{"key":"privacy","labels":{"zh-CN":"隐私","en-US":"Privacy"},"url":"#"},{"key":"guidelines","labels":{"zh-CN":"指南","en-US":"Guidelines"},"url":"#"}]`,
		`[{"key":"terms","labels":{"zh-CN":"","en-US":"Terms"},"url":"#"},{"key":"privacy","labels":{"zh-CN":"隐私","en-US":"Privacy"},"url":"#"},{"key":"guidelines","labels":{"zh-CN":"指南","en-US":"Guidelines"},"url":"#"}]`,
		`[{"key":"terms","labels":{"zh-CN":"条款","en-US":"Terms"},"url":"#"},{"key":"terms","labels":{"zh-CN":"隐私","en-US":"Privacy"},"url":"#"},{"key":"guidelines","labels":{"zh-CN":"指南","en-US":"Guidelines"},"url":"#"}]`,
	}

	for _, value := range cases {
		if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameFooterLinks, Value: value}); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected invalid footer links for %s, got %v", value, err)
		}
	}
}

func TestServiceRejectsUnknownOrEmptyOption(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()

	if _, err := service.Get(context.Background(), "missing"); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid option for unknown get, got %v", err)
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameSiteName, Value: ""}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("expected invalid option for empty update, got %v", err)
	}
}

func TestServiceEnsureDefaultsDoesNotOverwriteExistingOptions(t *testing.T) {
	store := &fakeStore{items: map[string]string{NameSiteName: "Existing"}}
	service := NewServiceWithDefaultsAndCacheTTL(store, Defaults{SiteName: "Default"}, time.Minute)

	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults returned error: %v", err)
	}
	if got := store.items[NameSiteName]; got != "Existing" {
		t.Fatalf("expected existing site name to be kept, got %q", got)
	}
	if _, ok := store.items[NameSiteURL]; ok {
		t.Fatalf("missing site URL must remain unset so APP_URL can stay authoritative")
	}
}

type optionsAdminItem struct {
	Value     string
	Secret    bool
	SecretSet bool
}

func settingsActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsManage: true},
	}
}

func seoActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSEOManage: true},
	}
}

func attachmentSettingsActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentSettings: true},
	}
}

func categoryManageActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionCategoryManage: true},
	}
}

func tagManageActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTagManage: true},
	}
}

func adminValue(items []AdminOption, name string) string {
	for _, item := range items {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

func adminValueFromPublic(items []Option, name string) string {
	for _, item := range items {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}

func adminSecret(items []AdminOption, name string) AdminOption {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return AdminOption{}
}

func stringsOfRunes(value string, count int) string {
	var builder strings.Builder
	for i := 0; i < count; i++ {
		builder.WriteString(value)
	}
	return builder.String()
}

type fakeStore struct {
	items     map[string]string
	listCalls int
	listErr   error
}

func (s *fakeStore) List(context.Context) ([]Option, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	options := make([]Option, 0, len(s.items))
	for name, value := range s.items {
		options = append(options, Option{Name: name, Value: value})
	}
	return options, nil
}

func (s *fakeStore) InsertMissing(_ context.Context, input UpdateInput) error {
	if s.items == nil {
		s.items = map[string]string{}
	}
	if _, ok := s.items[input.Name]; !ok {
		s.items[input.Name] = input.Value
	}
	return nil
}

func (s *fakeStore) Upsert(_ context.Context, input UpdateInput) (Option, error) {
	if s.items == nil {
		s.items = map[string]string{}
	}
	s.items[input.Name] = input.Value
	return Option{Name: input.Name, Value: input.Value}, nil
}
