package attachments

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

type fakeStorageCatalog struct {
	candidates      []storage.Candidate
	available       map[string]bool
	schemaForLocale func(string) storage.ProviderSchema
}

func (f fakeStorageCatalog) ListStorageProviderCandidates(context.Context) ([]storage.Candidate, error) {
	return f.candidates, nil
}

func (f fakeStorageCatalog) IsStorageProviderAvailable(_ context.Context, extensionID string) (bool, error) {
	if f.available == nil {
		return false, nil
	}
	return f.available[extensionID], nil
}

func (f fakeStorageCatalog) StorageProviderSchema(_ context.Context, extensionID, locale string) (storage.ProviderSchema, error) {
	if f.schemaForLocale != nil {
		return f.schemaForLocale(locale), nil
	}
	return storage.ProviderSchema{ExtensionID: extensionID, Label: extensionID}, nil
}

func TestSettingsIncludesPluginCandidates(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{}}
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{
			candidates: []storage.Candidate{
				storage.PluginCandidate("acme.store", "Acme Store", "/extensions/acme.store/pages/settings"),
			},
		})

	settings, err := service.Settings(context.Background(), attachmentSettingsActor(), "zh-CN")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.ProviderSlot != storage.ProviderSlot {
		t.Fatalf("slot=%s", settings.ProviderSlot)
	}
	found := false
	for _, c := range settings.Candidates {
		if c.Value == "plugin:acme.store" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected plugin candidate, got %#v", settings.Candidates)
	}
	if len(settings.Drivers) != 1 {
		t.Fatalf("drivers=%d", len(settings.Drivers))
	}
}

func TestSettingsLocalizesMultiInstanceProviderSchema(t *testing.T) {
	candidate := storage.PluginCandidate("sforum.storage-s3", "S3-compatible storage", "")
	candidate.MultiInstance = true
	service := NewService(nil, options.NewServiceWithCacheTTL(&fakeOptionStore{items: map[string]string{}}, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{
			candidates: []storage.Candidate{candidate},
			schemaForLocale: func(locale string) storage.ProviderSchema {
				return storage.ProviderSchema{ExtensionID: candidate.ExtensionID, Label: map[string]string{"zh-CN": "S3 兼容存储", "en-US": "S3-compatible storage"}[locale]}
			},
		})

	settings, err := service.Settings(context.Background(), attachmentSettingsActor(), "zh-CN")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	for _, item := range settings.Candidates {
		if item.ExtensionID == candidate.ExtensionID {
			if item.Label != "S3 兼容存储" || item.Schema == nil || item.Schema.Label != "S3 兼容存储" {
				t.Fatalf("localized candidate=%#v", item)
			}
			return
		}
	}
	t.Fatalf("missing multi-instance candidate: %#v", settings.Candidates)
}

func TestUpdateSettingsRejectsUnavailablePlugin(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{}}
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{available: map[string]bool{}})

	input := settingsFromValues(map[string]string{}, nil)
	input.Provider = "plugin:missing.store"
	_, err := service.UpdateSettings(context.Background(), attachmentSettingsActor(), input, "zh-CN")
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("expected ErrStorageUnavailable, got %v", err)
	}
}

func TestUpdateSettingsRejectsMultiInstancePluginWithoutInstance(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{}}
	candidate := storage.PluginCandidate("sforum.storage-s3", "S3-compatible storage", "/attachments/settings?provider=sforum.storage-s3")
	candidate.Available = true
	candidate.MultiInstance = true
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{candidates: []storage.Candidate{candidate}})

	input := settingsFromValues(map[string]string{}, nil)
	input.Provider = "plugin:sforum.storage-s3"
	_, err := service.UpdateSettings(context.Background(), attachmentSettingsActor(), input, "zh-CN")
	if !errors.Is(err, storage.ErrInvalidConfig) {
		t.Fatalf("expected invalid config for multi-instance plugin selection, got %v", err)
	}
}

func TestAdapterForPluginSelectionFailClosedWithoutRuntime(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{
		options.NameAttachmentProvider: "plugin:acme.store",
	}}
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{candidates: []storage.Candidate{
			storage.PluginCandidate("acme.store", "Acme Store", "/extensions/acme.store/pages/settings"),
		}})

	settings, err := service.runtimeSettings(context.Background())
	if err != nil {
		t.Fatalf("runtimeSettings: %v", err)
	}
	if settings.Provider != "plugin:acme.store" {
		t.Fatalf("provider=%s", settings.Provider)
	}
	// 有 catalog 无 runtime：仍 fail-closed（E6.2 要求注入 StorageRuntime）。
	_, err = service.adapterForSettings(context.Background(), settings, settings.Provider)
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("plugin path without runtime must fail closed, got %v", err)
	}
}

func TestAdapterForPluginSelectionWithRuntime(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{
		options.NameAttachmentProvider: "plugin:acme.store",
	}}
	// 最小 stub：只要 NewPluginStorageAdapter 成功即可。
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{candidates: []storage.Candidate{
			storage.PluginCandidate("acme.store", "Acme Store", "/extensions/acme.store/pages/settings"),
		}}).
		WithStoragePluginRuntime(extensionsruntime.NewPluginStorageAdapterFactory(stubStorageRuntime{}, 0))

	settings, err := service.runtimeSettings(context.Background())
	if err != nil {
		t.Fatalf("runtimeSettings: %v", err)
	}
	adapter, err := service.adapterForSettings(context.Background(), settings, settings.Provider)
	if err != nil || adapter == nil {
		t.Fatalf("expected plugin adapter, err=%v adapter=%v", err, adapter)
	}
}

// stubStorageRuntime 仅满足接口；本测试不调用 RPC。
type stubStorageRuntime struct{}

func (stubStorageRuntime) StoragePutBegin(context.Context, string, extensionsruntime.StoragePutBeginRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (stubStorageRuntime) StoragePutChunk(context.Context, string, extensionsruntime.StoragePutChunkRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (stubStorageRuntime) StorageOpen(context.Context, string, extensionsruntime.StorageOpenRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (stubStorageRuntime) StorageGetChunk(context.Context, string, extensionsruntime.StorageGetChunkRequest) (extensionsruntime.StorageGetChunkResponse, error) {
	return extensionsruntime.StorageGetChunkResponse{}, nil
}
func (stubStorageRuntime) StorageClose(context.Context, string, extensionsruntime.StorageCloseRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (stubStorageRuntime) StorageDelete(context.Context, string, extensionsruntime.StorageObjectRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (stubStorageRuntime) StorageStat(context.Context, string, extensionsruntime.StorageStatRequest) (extensionsruntime.StorageStatResponse, error) {
	return extensionsruntime.StorageStatResponse{}, nil
}
func (stubStorageRuntime) StorageExists(context.Context, string, extensionsruntime.StorageExistsRequest) (extensionsruntime.StorageExistsResponse, error) {
	return extensionsruntime.StorageExistsResponse{}, nil
}
func (stubStorageRuntime) StoragePublicURL(context.Context, string, extensionsruntime.StoragePublicURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (stubStorageRuntime) StorageSignedURL(context.Context, string, extensionsruntime.StorageSignedURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (stubStorageRuntime) StorageProbe(context.Context, string, extensionsruntime.StorageProbeRequest) (extensionsruntime.StorageProbeResponse, error) {
	return extensionsruntime.StorageProbeResponse{}, nil
}

func TestClearStorageProviderSelectionIfMatch(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{
		options.NameAttachmentProvider: "plugin:acme.store",
	}}
	// seed full defaults so UpdateMany validation passes after switch to local
	opts := options.NewServiceWithCacheTTL(optionStore, time.Minute)
	_ = opts.EnsureDefaults(context.Background())
	optionStore.items[options.NameAttachmentProvider] = "plugin:acme.store"

	service := NewService(nil, opts)
	if err := service.ClearStorageProviderSelectionIfMatch(context.Background(), "acme.store"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := optionStore.items[options.NameAttachmentProvider]; got != storage.ProviderLocal {
		t.Fatalf("expected local, got %q", got)
	}
	// 不匹配的扩展 id 不改写
	optionStore.items[options.NameAttachmentProvider] = "plugin:other.store"
	if err := service.ClearStorageProviderSelectionIfMatch(context.Background(), "acme.store"); err != nil {
		t.Fatalf("clear other: %v", err)
	}
	if got := optionStore.items[options.NameAttachmentProvider]; got != "plugin:other.store" {
		t.Fatalf("should keep other selection, got %q", got)
	}
}

func TestEnsureProviderSelectableCoreUnknown(t *testing.T) {
	service := NewService(nil, options.NewServiceWithCacheTTL(&fakeOptionStore{items: map[string]string{}}, time.Minute))
	err := service.ensureProviderSelectable(context.Background(), "not-a-driver")
	if !errors.Is(err, storage.ErrInvalidConfig) {
		t.Fatalf("got %v", err)
	}
	// 权限无关的 actor 仅用于编译
	_ = identity.Actor{}
}

func TestProbeMapsPluginFailureReason(t *testing.T) {
	optionStore := &fakeOptionStore{items: map[string]string{
		options.NameAttachmentProvider: "plugin:acme.store",
	}}
	service := NewService(nil, options.NewServiceWithCacheTTL(optionStore, time.Minute)).
		WithStorageProviderCatalog(fakeStorageCatalog{candidates: []storage.Candidate{
			storage.PluginCandidate("acme.store", "Acme Store", "/extensions/acme.store/pages/settings"),
		}}).
		WithStoragePluginRuntime(extensionsruntime.NewPluginStorageAdapterFactory(failingProbeRuntime{}, 0))

	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentSettings: true},
	}
	result, err := service.Probe(context.Background(), actor, "zh-CN")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result.OK || result.Reason != "storage.fs.config" {
		t.Fatalf("expected mapped reason, got %#v", result)
	}
	if result.Message != "附件存储暂时不可用，请检查存储配置。" {
		t.Fatalf("message=%q", result.Message)
	}

	result, err = service.Probe(context.Background(), actor, "en-US")
	if err != nil {
		t.Fatalf("Probe en-US: %v", err)
	}
	if result.Message != "Attachment storage is temporarily unavailable. Check the storage settings." {
		t.Fatalf("English message=%q", result.Message)
	}
}

type failingProbeRuntime struct {
	stubStorageRuntime
}

func (failingProbeRuntime) StorageProbe(context.Context, string, extensionsruntime.StorageProbeRequest) (extensionsruntime.StorageProbeResponse, error) {
	// 模拟插件 !OK；Adapter.Probe 转成 StorageRPCError。
	return extensionsruntime.StorageProbeResponse{}, &extensionsruntime.StorageRPCError{
		Reason:  "storage.fs.config",
		Message: "root missing",
	}
}
