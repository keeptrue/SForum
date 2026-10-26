package extensions

import (
	"context"
	"testing"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func TestListStorageProviderCandidatesOnlyEnabledSlot(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{}}
	store.items["sforum.smtp"] = Extension{
		ID: "sforum.smtp", Type: TypePlugin, Status: StatusEnabled, Name: "SMTP",
		Manifest: Manifest{
			ID: "sforum.smtp", Type: TypePlugin, Name: "SMTP",
			Providers: []ManifestProvider{{Slot: "mail.provider", Label: "SMTP"}},
		},
	}
	store.items["acme.store"] = Extension{
		ID: "acme.store", Type: TypePlugin, Status: StatusEnabled, Name: "Acme",
		Manifest: Manifest{
			ID: "acme.store", Type: TypePlugin, Name: "Acme Store",
			Providers: []ManifestProvider{{Slot: storage.ProviderSlot, Label: "Acme Object Storage", MultiInstance: true}},
			Admin:     ManifestAdmin{Entry: "/settings"},
		},
	}
	store.items["disabled.store"] = Extension{
		ID: "disabled.store", Type: TypePlugin, Status: StatusDisabled, Name: "Off",
		Manifest: Manifest{
			ID: "disabled.store", Type: TypePlugin,
			Providers: []ManifestProvider{{Slot: storage.ProviderSlot, Label: "Off"}},
		},
	}
	service := NewService(store, t.TempDir())
	catalog := NewAttachmentStorageProviderCatalog(service)

	got, err := catalog.ListStorageProviderCandidates(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ExtensionID != "acme.store" || got[0].Value != "plugin:acme.store" {
		t.Fatalf("got %#v", got)
	}
	if got[0].SettingsPath != "/attachments/settings?provider=acme.store" {
		t.Fatalf("settings path=%q", got[0].SettingsPath)
	}
	if !got[0].MultiInstance || got[0].Schema == nil {
		t.Fatalf("expected multi-instance schema projection, got %#v", got[0])
	}

	ok, err := catalog.IsStorageProviderAvailable(context.Background(), "acme.store")
	if err != nil || !ok {
		t.Fatalf("available acme: %v %v", ok, err)
	}
	ok, err = catalog.IsStorageProviderAvailable(context.Background(), "disabled.store")
	if err != nil || ok {
		t.Fatalf("disabled should be unavailable: %v %v", ok, err)
	}
	ok, err = catalog.IsStorageProviderAvailable(context.Background(), "sforum.smtp")
	if err != nil || ok {
		t.Fatalf("mail plugin is not storage: %v %v", ok, err)
	}
}

func TestBuiltinStorageProviderStartsInstalled(t *testing.T) {
	manifest := Manifest{
		Type: TypePlugin,
		Providers: []ManifestProvider{{
			Slot: storage.ProviderSlot,
		}},
	}
	if got := builtinInitialStatus(manifest); got != StatusInstalled {
		t.Fatalf("storage provider initial status=%q", got)
	}
}

func TestStorageProviderSchemaResolvesLocaleAndRequired(t *testing.T) {
	item := Extension{
		ID: "acme.store",
		Manifest: Manifest{
			Name:      "Acme Storage",
			Providers: []ManifestProvider{{Slot: storage.ProviderSlot, Label: "Acme storage"}},
			Settings: []ManifestSetting{{
				Key: "bucket", Type: "string", Required: true,
				Label: LocalizedText{Default: "Bucket", ByLocale: map[string]string{"zh-CN": "存储桶"}},
			}},
		},
	}

	schema := storageProviderSchema(item, "zh-CN")
	if len(schema.Fields) != 1 || schema.Fields[0].Label != "存储桶" || !schema.Fields[0].Required {
		t.Fatalf("schema=%#v", schema)
	}
}
