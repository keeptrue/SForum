package options

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func publicOptionValue(items []Option, name string) (string, bool) {
	for _, item := range items {
		if item.Name == name {
			return item.Value, true
		}
	}
	return "", false
}

func clientVersionOptionNames() []string {
	return []string{NameClientMinimumVersion, NameClientRecommendedVersion, NameClientUpdateNotice}
}

// 默认必须「不限制」且对客户端可见：空最低版本不会挡住任何已发布客户端。
func TestClientVersionOptionsArePublicAndUnrestrictedByDefault(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	ctx := context.Background()

	items, err := service.List(ctx)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	for _, name := range clientVersionOptionNames() {
		value, ok := publicOptionValue(items, name)
		if !ok {
			t.Fatalf("%s must be part of the public option projection", name)
		}
		if value != "" {
			t.Fatalf("%s default must be empty (no restriction), got %q", name, value)
		}
	}

	for _, name := range clientVersionOptionNames() {
		definition, ok := optionDefinitionFor(name)
		if !ok {
			t.Fatalf("%s has no option definition", name)
		}
		if !definition.public {
			t.Fatalf("%s must be public so clients can read the version policy", name)
		}
		if definition.managePermission != identity.PermissionSettingsSiteManage {
			t.Fatalf("%s manage permission = %q", name, definition.managePermission)
		}
	}
}

func TestClientVersionOptionNormalization(t *testing.T) {
	valid := []string{"", "1", "1.2", "1.2.3", "1.2.3.4", "1.2.3-beta.1", "10.20.30+build7"}
	for _, value := range valid {
		normalized, ok := normalizeClientVersion(value)
		if !ok {
			t.Fatalf("%q should be accepted", value)
		}
		if normalized != strings.TrimSpace(value) {
			t.Fatalf("%q normalized to %q", value, normalized)
		}
	}

	invalid := []string{"abc", "v1.2.3", "1.2.3.4.5", "1..2", "-1.2", strings.Repeat("1", clientVersionMaxRunes+1)}
	for _, value := range invalid {
		if _, ok := normalizeClientVersion(value); ok {
			t.Fatalf("%q should be rejected", value)
		}
	}

	if _, ok := normalizeClientUpdateNotice(strings.Repeat("升", clientUpdateNoticeMaxRunes)); !ok {
		t.Fatal("notice at the rune limit must be accepted")
	}
	if _, ok := normalizeClientUpdateNotice(strings.Repeat("升", clientUpdateNoticeMaxRunes+1)); ok {
		t.Fatal("notice over the rune limit must be rejected")
	}
}

func TestClientVersionOptionsWriteAuthorityAndValidation(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	ctx := context.Background()

	denied := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
	if _, err := service.Update(ctx, denied, UpdateInput{Name: NameClientMinimumVersion, Value: "2.0.0"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("actor without settings.site.manage must be denied, got %v", err)
	}

	allowed := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}
	if _, err := service.Update(ctx, allowed, UpdateInput{Name: NameClientMinimumVersion, Value: " 1.4.0 "}); err != nil {
		t.Fatalf("site settings actor should set the minimum version: %v", err)
	}
	value, err := service.WebOption(ctx, NameClientMinimumVersion)
	if err != nil {
		t.Fatal(err)
	}
	if value != "1.4.0" {
		t.Fatalf("stored minimum version = %q, want trimmed 1.4.0", value)
	}

	if _, err := service.Update(ctx, allowed, UpdateInput{Name: NameClientMinimumVersion, Value: "not-a-version"}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("malformed version must be rejected, got %v", err)
	}
	if _, err := service.Update(ctx, allowed, UpdateInput{Name: NameClientUpdateNotice, Value: strings.Repeat("升", clientUpdateNoticeMaxRunes+1)}); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("over-long notice must be rejected, got %v", err)
	}
	// 写入失败不得改变已存值。
	value, err = service.WebOption(ctx, NameClientMinimumVersion)
	if err != nil || value != "1.4.0" {
		t.Fatalf("rejected write must keep the previous value, got %q err=%v", value, err)
	}
}

// 脏数据回退到「不限制」，避免运营误写把客户端全部锁死或阻断启动。
func TestClientVersionOptionsCoerceDirtyValues(t *testing.T) {
	defaults := clientVersionRecommendedDefaults()
	values := map[string]string{
		NameClientMinimumVersion:     "oops",
		NameClientRecommendedVersion: "1.2.3.4.5",
		NameClientUpdateNotice:       strings.Repeat("升", clientUpdateNoticeMaxRunes+1),
	}
	coerceClientVersionOptions(values, defaults)
	for _, name := range clientVersionOptionNames() {
		if values[name] != defaults[name] {
			t.Fatalf("%s coerced to %q, want default %q", name, values[name], defaults[name])
		}
	}
	if !isValidClientVersionOptions(map[string]string{
		NameClientMinimumVersion:     "1.0.0",
		NameClientRecommendedVersion: "1.2.0",
		NameClientUpdateNotice:       "请升级到最新版本。",
	}) {
		t.Fatal("well-formed client version policy must validate")
	}
	if isValidClientVersionOptions(map[string]string{NameClientMinimumVersion: "bad"}) {
		t.Fatal("malformed policy must not validate")
	}
}
