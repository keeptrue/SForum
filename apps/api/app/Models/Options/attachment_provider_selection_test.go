package options

import (
	"context"
	"testing"
	"time"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func TestNormalizeAttachmentProviderPluginSelection(t *testing.T) {
	got, ok := normalizeAttachmentProvider("plugin:acme.store")
	if !ok || got != "plugin:acme.store" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := normalizeAttachmentProvider("plugin:"); ok {
		t.Fatal("empty extension id must fail")
	}
	if _, ok := normalizeAttachmentProvider("plugin:Bad.ID"); ok {
		t.Fatal("uppercase must fail (manifest ids are lower)")
	}
	if _, ok := normalizeAttachmentProvider("plugin:has space"); ok {
		t.Fatal("spaces invalid")
	}
}

func TestUpdateAttachmentProviderPluginWithoutCloudSecrets(t *testing.T) {
	// plugin 选择不要求 aliyun/tencent 字段（凭证在 extension_settings）。
	store := &fakeStore{items: map[string]string{}}
	service := NewServiceWithCacheTTL(store, time.Minute)
	actor := attachmentSettingsActor()

	// 先确保默认值齐全
	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	_, err := service.Update(context.Background(), actor, UpdateInput{
		Name:  NameAttachmentProvider,
		Value: storage.FormatPluginSelection("acme.store"),
	})
	if err != nil {
		t.Fatalf("plugin provider update: %v", err)
	}
	if store.items[NameAttachmentProvider] != "plugin:acme.store" {
		t.Fatalf("stored %q", store.items[NameAttachmentProvider])
	}
}
