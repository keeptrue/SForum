package extensions

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
)

func TestAuthProviderSettingsConfigured(t *testing.T) {
	item := installedExtension("sforum.auth-github", TypePlugin, ManifestBackend{})
	item.Manifest.Settings = []ManifestSetting{
		{Key: "client_id", Type: "text", Default: ""},
		{Key: "client_secret", Type: "secret", Default: ""},
	}
	item.Manifest.Identity = &ManifestIdentity{
		ContractVersion: "sforum.auth-github.identity@1",
		Providers: []ManifestIdentityProvider{{
			ID: "sforum.auth-github.auth", Kind: "auth", Handler: "sforum.auth-github.identity",
		}},
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}, settings: map[string]map[string]string{
		item.ID: {},
	}}
	service := NewService(store, t.TempDir())

	ok, err := service.AuthProviderSettingsConfigured(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("configured check: %v", err)
	}
	if ok {
		t.Fatalf("empty settings must not be configured")
	}

	store.settings[item.ID] = map[string]string{
		"client_id":     "Iv1.example",
		"client_secret": "secret-value",
	}
	ok, err = service.AuthProviderSettingsConfigured(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("configured check after fill: %v", err)
	}
	if !ok {
		t.Fatalf("filled required settings must be configured")
	}
}

func TestCanManageExtensionSettingsAllowsIdentityProviderManageForAuthPlugin(t *testing.T) {
	authPlugin := installedExtension("sforum.auth-github", TypePlugin, ManifestBackend{})
	authPlugin.Manifest.Identity = &ManifestIdentity{
		Providers: []ManifestIdentityProvider{{ID: "sforum.auth-github.auth", Kind: "auth"}},
	}
	plainPlugin := installedExtension("demo.plain", TypePlugin, ManifestBackend{})
	plainPlugin.Manifest.Providers = []ManifestProvider{{Slot: "search.provider", Label: "Search"}}

	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionIdentityProviderManage: true},
	}
	if !canManageExtensionSettings(actor, authPlugin) {
		t.Fatalf("identity.provider.manage must manage auth provider plugin settings")
	}
	if canManageExtensionSettings(actor, plainPlugin) {
		t.Fatalf("identity.provider.manage must not manage unrelated plugin settings")
	}
}

func TestAdminPageBootstrapAllowsIdentityProviderManageForAuthSettings(t *testing.T) {
	item := installedExtension("sforum.auth-github", TypePlugin, ManifestBackend{})
	item.Manifest.Admin = ManifestAdmin{Pages: []ManifestAdminPage{
		{Path: "/settings", Label: "Settings", View: "settings"},
		{Path: "/about", Label: "About", View: "about"},
	}}
	item.Manifest.Settings = []ManifestSetting{{Key: "client_id", Label: LocalizedText{Default: "Client ID"}, Type: "text", Default: ""}}
	item.Manifest.Identity = &ManifestIdentity{
		Providers: []ManifestIdentityProvider{{ID: "sforum.auth-github.auth", Kind: "auth"}},
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewService(store, t.TempDir())
	actor := identity.Actor{
		ID: 42, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionIdentityProviderManage: true},
	}

	bootstrap, err := service.AdminPageBootstrap(context.Background(), actor, item.ID, "/settings", "zh-CN")
	if err != nil {
		t.Fatalf("auth settings bootstrap denied: %v", err)
	}
	if bootstrap.Settings == nil {
		t.Fatalf("expected settings document for identity.provider.manage")
	}

	_, err = service.AdminPageBootstrap(context.Background(), actor, item.ID, "/about", "zh-CN")
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("manage-only actor must not read about page, got %v", err)
	}
}

func TestAuthProviderSettingsRestartEnabledLifecycleV2ExactArtifact(t *testing.T) {
	item := authProviderSettingsLifecycleV2Artifact(t, "sforum.auth-github")
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	actor := identity.Actor{
		ID: 77, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionIdentityProviderManage: true},
	}
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	frozenAuthority, err := trust.ConfirmLifecycleAuthority(t.Context(), extensionManager(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	operations := []LifecycleMachineOperation{}
	runner := &lifecycleV2RecordingRunner{beforeRun: func(input LifecycleCoordinatorRunInput) {
		operation := LifecycleMachineOperation(input.Acquire.Operation)
		operations = append(operations, operation)
		stored := store.items[item.ID]
		switch operation {
		case LifecycleMachineDisable:
			stored.Status = StatusDisabled
		case LifecycleMachineEnable:
			stored.Status = StatusEnabled
		}
		store.items[item.ID] = stored
	}}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 100}),
		WithExecutableTrust(trust, true),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error { return nil },
			lifecycleV2AuthorityStore{authority: frozenAuthority},
		),
	)
	settings := settingslifecycle.New(nil)
	service.BindSettingsLifecycle(settings)

	updated, err := service.UpdateSettings(t.Context(), actor, item.ID, UpdateSettingsInput{
		Values: map[string]string{"client_id": "Iv1.updated"},
	}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0] != LifecycleMachineDisable || operations[1] != LifecycleMachineEnable {
		t.Fatalf("settings restart operations = %v", operations)
	}
	if store.items[item.ID].Status != StatusEnabled || settingValue(updated, "client_id") != "Iv1.updated" {
		t.Fatalf("settings restart result: status=%q settings=%#v", store.items[item.ID].Status, updated)
	}
}

func TestAuthProviderSettingsLifecycleV2PreflightFailureDoesNotPersist(t *testing.T) {
	item := authProviderSettingsLifecycleV2Artifact(t, "sforum.auth-preflight")
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	actor := identity.Actor{
		ID: 78, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionIdentityProviderManage: true},
	}
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	frozenAuthority, err := trust.ConfirmLifecycleAuthority(t.Context(), extensionManager(), item, "")
	if err != nil {
		t.Fatal(err)
	}
	runner := &lifecycleV2RecordingRunner{}
	settings := settingslifecycle.New(nil)
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{},
		WithAuditor(&lifecycleV2AuditWriter{nextID: 200}),
		WithExecutableTrust(trust, true),
		WithLifecycleCoordinator(
			runner,
			func(context.Context, LifecycleMachineOperation, *Extension, Extension) error {
				return errors.New("static preflight unavailable")
			},
			lifecycleV2AuthorityStore{authority: frozenAuthority},
		),
	)
	service.BindSettingsLifecycle(settings)

	_, err = service.UpdateSettings(t.Context(), actor, item.ID, UpdateSettingsInput{
		Values: map[string]string{"client_id": "must-not-persist"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRestartUnavailable) || runner.calls != 0 {
		t.Fatalf("preflight result: err=%v runner=%d", err, runner.calls)
	}
	if _, getErr := settings.Get(t.Context(), item.ID); !errors.Is(getErr, settingslifecycle.ErrNotFound) {
		t.Fatalf("preflight failure persisted settings: %v", getErr)
	}
}

func authProviderSettingsLifecycleV2Artifact(t *testing.T, id string) Extension {
	t.Helper()
	item := completeV3TrustExtension(t, id)
	item.Status = StatusEnabled
	item.Source = SourceBuiltin
	item.IsSystem = true
	item.IsDeletable = false
	item.Manifest.Dependencies = nil
	item.Manifest.Settings = []ManifestSetting{{
		Key: "client_id", Label: LocalizedText{Default: "Client ID"}, Type: "text", Default: "",
	}}
	item.Manifest.Identity = &ManifestIdentity{
		ContractVersion: id + ".identity@1",
		Providers: []ManifestIdentityProvider{{
			ID: id + ".auth", ContractVersion: id + ".auth@1",
			Kind: "auth", Handler: id + ".identity",
		}},
	}
	item.Manifest = normalizeManifest(item.Manifest)
	if err := validateManifest(item.Manifest); err != nil {
		t.Fatalf("auth provider settings fixture manifest: %v", err)
	}
	refreshTrustPackageIdentity(t, &item)
	return item
}
