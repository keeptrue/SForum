package options

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestCommunityPolicyDefaultsPresent(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	items, err := service.ListAdmin(context.Background(), settingsActor())
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	want := map[string]string{
		NameIdentityRegistrationMode:        "open",
		NameIdentityUsernameMinLength:       "3",
		NameIdentityUsernameMaxLength:       "20",
		NameIdentityUsernameCharset:         "unicode_letters_numbers",
		NameIdentityLoginMaxFailures:        "10",
		NameIdentityLoginLockoutMinutes:     "15",
		NameTrustNewUserDays:                "7",
		NameTrustNewUserForbidOutboundLinks: enabledOptionValue(true),
		NameSiteMaintenanceEnabled:          enabledOptionValue(false),
		NameForumGuestRead:                  "public",
		NameForumListDefaultSort:            "active",
		NameForumMentionsEnabled:            enabledOptionValue(true),
		NameForumMentionsMaxPerPost:         "10",
	}
	for name, expected := range want {
		if got := adminValue(items, name); got != expected {
			t.Fatalf("%s default = %q, want %q", name, got, expected)
		}
	}
}

func TestCommunityPolicyRegistrationModeClosesOpenRegistration(t *testing.T) {
	store := &fakeStore{items: map[string]string{}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()

	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityRegistrationMode, Value: "invite"},
	}); err != nil {
		t.Fatalf("UpdateMany invite: %v", err)
	}
	enabled, err := service.RegistrationEnabled(context.Background())
	if err != nil {
		t.Fatalf("RegistrationEnabled: %v", err)
	}
	if enabled {
		t.Fatal("invite mode should disable open registration")
	}

	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityRegistrationMode, Value: "open"},
		{Name: NameIdentityRegistrationEnabled, Value: enabledOptionValue(true)},
	}); err != nil {
		t.Fatalf("UpdateMany open: %v", err)
	}
	enabled, err = service.RegistrationEnabled(context.Background())
	if err != nil || !enabled {
		t.Fatalf("open mode should allow registration, enabled=%v err=%v", enabled, err)
	}
}

func TestCommunityPolicyAcceptsAndRejectsValues(t *testing.T) {
	store := &fakeStore{items: map[string]string{}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := settingsActor()

	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityUsernameMinLength, Value: "4"},
		{Name: NameIdentityUsernameMaxLength, Value: "16"},
		{Name: NameIdentityUsernameCharset, Value: "ascii"},
		{Name: NameIdentityUsernameReserved, Value: " admin , Root "},
		{Name: NameForumGuestRead, Value: "login_required"},
		{Name: NameForumListDefaultSort, Value: "hot"},
		{Name: NameForumTopicsDuplicateTitlePolicy, Value: "block"},
		{Name: NameSiteMaintenanceEnabled, Value: "enabled"},
		{Name: NameSiteMaintenanceMessage, Value: "  升级中  "},
		{Name: NameTrustNewUserDays, Value: "14"},
	}); err != nil {
		t.Fatalf("valid UpdateMany: %v", err)
	}
	if got := store.items[NameIdentityUsernameReserved]; got != "admin,root" {
		t.Fatalf("reserved names = %q, want admin,root", got)
	}
	if store.items[NameForumGuestRead] != "login_required" {
		t.Fatalf("guest read not saved: %q", store.items[NameForumGuestRead])
	}
	if store.items[NameSiteMaintenanceMessage] != "升级中" {
		t.Fatalf("maintenance message not trimmed: %q", store.items[NameSiteMaintenanceMessage])
	}

	rejects := []UpdateInput{
		{Name: NameIdentityRegistrationMode, Value: "maybe"},
		{Name: NameIdentityUsernameMinLength, Value: "1"},
		{Name: NameIdentityUsernameMaxLength, Value: "2"}, // max < min after min was 4? use absolute invalid
		{Name: NameForumGuestRead, Value: "members_only"},
		{Name: NameForumListDefaultSort, Value: "random"},
		{Name: NameTrustNewUserDays, Value: "999"},
		{Name: NameForumMentionsMaxPerPost, Value: "51"},
	}
	// 先重置用户名范围为合法，再单独测 max < min
	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityUsernameMinLength, Value: "3"},
		{Name: NameIdentityUsernameMaxLength, Value: "20"},
	}); err != nil {
		t.Fatalf("reset username bounds: %v", err)
	}
	for _, input := range rejects {
		if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{input}); err == nil {
			t.Fatalf("expected rejection for %s=%q", input.Name, input.Value)
		}
	}
	// min > max
	if _, err := service.UpdateMany(context.Background(), actor, []UpdateInput{
		{Name: NameIdentityUsernameMinLength, Value: "20"},
		{Name: NameIdentityUsernameMaxLength, Value: "5"},
	}); err == nil {
		t.Fatal("expected rejection when username max < min")
	}
}

func TestCommunityPolicyHelpers(t *testing.T) {
	store := &fakeStore{items: map[string]string{
		NameSiteMaintenanceEnabled:             "enabled",
		NameSiteMaintenanceMessage:             "维护中",
		NameIdentityRegistrationMode:           "closed",
		NameTrustNewUserDays:                   "7",
		NameTrustNewUserTopicCooldownSeconds:   "300",
		NameTrustNewUserCommentCooldownSeconds: "60",
		NameTrustNewUserDailyTopicLimit:        "3",
		NameTrustNewUserDailyCommentLimit:      "30",
		NameTrustNewUserForbidOutboundLinks:    "enabled",
		NameTrustNewUserForbidAttachments:      "disabled",
		NameIdentityUsernameMinLength:          "3",
		NameIdentityUsernameMaxLength:          "20",
		NameIdentityUsernameCharset:            "unicode_letters_numbers",
		NameIdentityUsernameReserved:           "admin,system",
		NameIdentityLoginMaxFailures:           "10",
		NameIdentityLoginLockoutMinutes:        "15",
	}}
	service := NewServiceWithCacheTTL(store, time.Minute)

	policy, err := service.MaintenancePolicy(context.Background())
	if err != nil || !policy.Enabled || policy.Message != "维护中" {
		t.Fatalf("MaintenancePolicy = %#v err=%v", policy, err)
	}
	mode, err := service.RegistrationMode(context.Background())
	if err != nil || mode != "closed" {
		t.Fatalf("RegistrationMode = %q err=%v", mode, err)
	}
	username, err := service.UsernamePolicy(context.Background())
	if err != nil {
		t.Fatalf("UsernamePolicy: %v", err)
	}
	if username.MinLength != 3 || username.MaxLength != 20 || username.Charset != "unicode_letters_numbers" {
		t.Fatalf("UsernamePolicy unexpected: %#v", username)
	}
	lockout, err := service.LoginLockoutPolicy(context.Background())
	if err != nil || lockout.MaxFailures != 10 || lockout.LockoutMinutes != 15 {
		t.Fatalf("LoginLockoutPolicy = %#v err=%v", lockout, err)
	}
}

func TestCommunityPolicyOutboundLinkOptions(t *testing.T) {
	store := &fakeStore{}
	service := NewServiceWithCacheTTL(store, time.Minute)
	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	// 推荐默认：中转开启、白名单为空。
	if got := store.items[NameForumLinksExternalInterstitial]; got != enabledOptionValue(true) {
		t.Fatalf("interstitial default = %q", got)
	}
	if got := store.items[NameForumLinksTrustedDomains]; got != "" {
		t.Fatalf("trusted domains default = %q", got)
	}

	// 白名单归一：裁掉 scheme/路径、小写、去重、保序。
	updated, err := service.UpdateMany(context.Background(), settingsActor(), []UpdateInput{
		{Name: NameForumLinksExternalInterstitial, Value: "DISABLED"},
		{Name: NameForumLinksTrustedDomains, Value: "https://GitHub.com/a/b\nExample.ORG:8443\n  github.com  \nexample.org:8443"},
	})
	if err != nil {
		t.Fatalf("UpdateMany: %v", err)
	}
	if got := adminValue(updated, NameForumLinksExternalInterstitial); got != enabledOptionValue(false) {
		t.Fatalf("interstitial normalized = %q", got)
	}
	if got := adminValue(updated, NameForumLinksTrustedDomains); got != "github.com\nexample.org:8443" {
		t.Fatalf("trusted domains normalized = %q", got)
	}

	// 非法条目整体拒绝（fail closed），不写入部分结果。
	for _, invalid := range []string{
		"not a host",
		"exa mple.com",
		"https://",
		"-.com",
	} {
		if _, err := service.Update(context.Background(), settingsActor(), UpdateInput{
			Name: NameForumLinksTrustedDomains, Value: invalid,
		}); err == nil {
			t.Fatalf("expected invalid trusted domain list to be rejected: %q", invalid)
		}
	}
}

func TestCommunityPolicyOutboundLinkOptionsRequireForumPermission(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	items, err := service.ListAdmin(context.Background(), settingsActor())
	if err != nil {
		t.Fatalf("ListAdmin: %v", err)
	}
	// 选项公开可读（前端在点击时判定），写入需要论坛设置权限：
	// settings.manage 按父权限继承可改，仅有分类管理权限的账号必须被拒。
	if adminValue(items, NameForumLinksExternalInterstitial) == "" {
		t.Fatal("expected interstitial option visible to settings actor")
	}
	if _, err := service.Update(context.Background(), categoryManageActor(), UpdateInput{
		Name: NameForumLinksExternalInterstitial, Value: enabledOptionValue(false),
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected category manager to be denied, got %v", err)
	}
	if _, err := service.Update(context.Background(), categoryManageActor(), UpdateInput{
		Name: NameForumLinksTrustedDomains, Value: "example.com",
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected category manager to be denied trusted domains, got %v", err)
	}
}
