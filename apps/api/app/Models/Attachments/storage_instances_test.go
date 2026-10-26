package attachments

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

type fakeStorageInstanceStore struct {
	*fakeAttachmentStore
	items map[string]StorageInstance
}

func newFakeStorageInstanceStore() *fakeStorageInstanceStore {
	return &fakeStorageInstanceStore{fakeAttachmentStore: &fakeAttachmentStore{}, items: map[string]StorageInstance{}}
}

func (s *fakeStorageInstanceStore) CreateStorageInstance(_ context.Context, input StorageInstanceCreate) (StorageInstance, error) {
	now := time.Now().UTC()
	item := StorageInstance{ID: input.ID, ExtensionID: input.ExtensionID, Name: input.Name, Values: cloneStringMap(input.Settings), ConfigRevision: 1, Status: "unverified", CreatedAt: now, UpdatedAt: now}
	s.items[item.ID] = item
	item.Values = cloneStringMap(item.Values)
	return item, nil
}

func (s *fakeStorageInstanceStore) UpdateStorageInstance(_ context.Context, id string, expectedRevision int64, name string, settings map[string]string) (StorageInstance, error) {
	item, ok := s.items[id]
	if !ok || item.ConfigRevision != expectedRevision {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	item.Name, item.Values, item.ConfigRevision, item.Status = name, cloneStringMap(settings), item.ConfigRevision+1, "unverified"
	s.items[id] = item
	item.Values = cloneStringMap(item.Values)
	return item, nil
}

func (s *fakeStorageInstanceStore) GetStorageInstance(_ context.Context, id string) (StorageInstance, error) {
	item, ok := s.items[id]
	if !ok {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	item.Values = cloneStringMap(item.Values)
	return item, nil
}

func (s *fakeStorageInstanceStore) ListStorageInstances(context.Context) ([]StorageInstance, error) {
	out := make([]StorageInstance, 0, len(s.items))
	for _, item := range s.items {
		item.Values = cloneStringMap(item.Values)
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeStorageInstanceStore) UpdateStorageInstanceProbe(_ context.Context, id, status, message string) error {
	item, ok := s.items[id]
	if !ok {
		return ErrStorageInstanceInvalid
	}
	item.LastProbeStatus, item.LastProbeMessage = status, message
	if status == "ok" {
		item.Status = "ready"
	} else {
		item.Status = "error"
	}
	s.items[id] = item
	return nil
}

func (s *fakeStorageInstanceStore) DeleteStorageInstance(_ context.Context, id string) error {
	item, ok := s.items[id]
	if !ok {
		return ErrStorageInstanceInvalid
	}
	if item.AttachmentCount > 0 {
		return ErrStorageInstanceReferenced
	}
	delete(s.items, id)
	return nil
}

type storageInstanceCatalog struct{}

func (storageInstanceCatalog) ListStorageProviderCandidates(context.Context) ([]storage.Candidate, error) {
	return []storage.Candidate{{ExtensionID: "sforum.storage-s3", Available: true, MultiInstance: true}}, nil
}

func (storageInstanceCatalog) IsStorageProviderAvailable(_ context.Context, extensionID string) (bool, error) {
	return extensionID == "sforum.storage-s3", nil
}

func (storageInstanceCatalog) StorageProviderSchema(_ context.Context, extensionID, _ string) (storage.ProviderSchema, error) {
	return storage.ProviderSchema{ExtensionID: extensionID, Label: "S3", Fields: []storage.ProviderField{
		{Key: "bucket", Type: "string"},
		{Key: "secret_access_key", Type: "secret"},
	}}, nil
}

type storageInstanceRuntime struct {
	probeErr error
	values   map[string]string
}

func TestValidateStorageFieldRejectsMissingRequiredValue(t *testing.T) {
	err := validateStorageField(storage.ProviderField{Key: "bucket", Type: "string", Required: true}, "  ")
	if !errors.Is(err, ErrStorageInstanceInvalid) {
		t.Fatalf("expected required field rejection, got %v", err)
	}
}

func (r *storageInstanceRuntime) NewStorageAdapter(string) (storage.Adapter, error) {
	return nil, storage.ErrInvalidConfig
}

func (r *storageInstanceRuntime) NewStorageInstanceAdapter(_ context.Context, _, _ string, values map[string]string) (storage.Adapter, error) {
	r.values = cloneStringMap(values)
	return nil, storage.ErrInvalidConfig
}

func (r *storageInstanceRuntime) ProbeStorageInstance(_ context.Context, _ string, values map[string]string) error {
	r.values = cloneStringMap(values)
	return r.probeErr
}

func (r *storageInstanceRuntime) RemoveStorageInstance(context.Context, string, string) error {
	return nil
}

func TestStorageInstanceSecretsStayOutOfInstanceDocument(t *testing.T) {
	store := newFakeStorageInstanceStore()
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, newAttachmentOptions(nil)).
		WithStorageProviderCatalog(storageInstanceCatalog{}).
		WithSecretStore(secrets)

	created, err := service.CreateStorageInstance(context.Background(), attachmentSettingsActor(), StorageInstanceInput{
		ExtensionID: "sforum.storage-s3",
		Name:        "Primary",
		Values: map[string]string{
			"bucket":            "uploads",
			"secret_access_key": "top-secret",
		},
	}, "zh-CN")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	persisted := store.items[created.ID].Values["secret_access_key"]
	if persisted == "top-secret" || persisted == "" {
		t.Fatalf("secret persisted incorrectly: %q", persisted)
	}
	if _, err := secretstore.ParseReference(persisted); err != nil {
		t.Fatalf("secret reference: %v", err)
	}
	if created.Values["secret_access_key"] != "" || !created.Schema.Fields[1].SecretSet {
		t.Fatalf("public secret projection: %#v", created)
	}
}

func TestActivateStorageInstanceProbesResolvedSecretsBeforeSwitch(t *testing.T) {
	store := newFakeStorageInstanceStore()
	optionStore := &fakeOptionStore{items: map[string]string{}}
	opts := options.NewServiceWithCacheTTL(optionStore, time.Minute)
	if err := opts.EnsureDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	secrets, _ := secretstore.New(secretstore.NewMemoryStore(), nil)
	runtime := &storageInstanceRuntime{}
	service := NewService(store, opts).
		WithStorageProviderCatalog(storageInstanceCatalog{}).
		WithStoragePluginRuntime(runtime).
		WithSecretStore(secrets)
	actor := attachmentSettingsActor()
	created, err := service.CreateStorageInstance(context.Background(), actor, StorageInstanceInput{
		ExtensionID: "sforum.storage-s3", Name: "Primary",
		Values: map[string]string{"bucket": "uploads", "secret_access_key": "top-secret"},
	}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivateStorageInstance(context.Background(), actor, created.ID, "zh-CN"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if runtime.values["secret_access_key"] != "top-secret" {
		t.Fatalf("runtime did not receive resolved secret: %#v", runtime.values)
	}
	if got := optionStore.items[options.NameAttachmentProvider]; got != storage.FormatInstanceSelection(created.ID) {
		t.Fatalf("active provider = %q", got)
	}

	runtime.probeErr = &extensionruntime.StorageRPCError{Reason: "storage.s3.probe", Message: "unreachable"}
	store.items[created.ID] = func() StorageInstance {
		item := store.items[created.ID]
		item.Name = "Still primary"
		return item
	}()
	optionStore.items[options.NameAttachmentProvider] = storage.ProviderLocal
	if _, err := service.ActivateStorageInstance(context.Background(), actor, created.ID, "zh-CN"); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("expected failed activation, got %v", err)
	}
	if got := optionStore.items[options.NameAttachmentProvider]; got != storage.ProviderLocal {
		t.Fatalf("failed probe changed provider to %q", got)
	}
}

func TestProbeStorageInstanceLocalizesResponseWithoutLosingStoredDiagnostic(t *testing.T) {
	store := newFakeStorageInstanceStore()
	secrets, _ := secretstore.New(secretstore.NewMemoryStore(), nil)
	runtime := &storageInstanceRuntime{probeErr: &extensionruntime.StorageRPCError{Reason: "storage.s3.probe", Message: "unreachable"}}
	service := NewService(store, newAttachmentOptions(nil)).
		WithStorageProviderCatalog(storageInstanceCatalog{}).
		WithStoragePluginRuntime(runtime).
		WithSecretStore(secrets)
	actor := attachmentSettingsActor()
	created, err := service.CreateStorageInstance(context.Background(), actor, StorageInstanceInput{
		ExtensionID: "sforum.storage-s3", Name: "Primary", Values: map[string]string{"bucket": "uploads", "secret_access_key": "top-secret"},
	}, "zh-CN")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := service.ProbeStorageInstance(context.Background(), actor, StorageInstanceProbeInput{InstanceID: created.ID}, "zh-CN")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Reason != "storage.s3.probe" || result.Message != "附件存储暂时不可用，请检查存储配置。" {
		t.Fatalf("localized result: %#v", result)
	}
	if got := store.items[created.ID].LastProbeMessage; got != "unreachable" {
		t.Fatalf("stored diagnostic=%q", got)
	}
}

func TestDeleteStorageInstanceRejectsHistoricalAttachments(t *testing.T) {
	store := newFakeStorageInstanceStore()
	store.items["instance-a"] = StorageInstance{ID: "instance-a", ExtensionID: "sforum.storage-s3", Name: "Archive", AttachmentCount: 2}
	service := NewService(store, newAttachmentOptions(nil))
	err := service.DeleteStorageInstance(context.Background(), identity.Actor{
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentSettings: true},
	}, "instance-a")
	if !errors.Is(err, ErrStorageInstanceReferenced) {
		t.Fatalf("delete referenced instance: %v", err)
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
