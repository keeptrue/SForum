package extensions

import (
	"context"
	"errors"
	"testing"
)

func TestSafeModeClosesExecutableAndContributionSurfaces(t *testing.T) {
	plugin := uploadedExtension("broken.plugin", TypePlugin)
	plugin.Status = StatusEnabled
	plugin.Manifest.Backend = ManifestBackend{Entry: "missing/plugin"}
	plugin.Manifest.Routes = []ManifestRoute{{Path: "/break", Methods: []string{"GET"}}}
	plugin.Manifest.Contributions = []ManifestContribution{{Point: "forum.nav.items", ID: "break"}}
	plugin.Manifest.Providers = []ManifestProvider{{Slot: "attachment.storage.provider", Label: "Broken"}}
	plugin.Manifest.Jobs = []ManifestJob{{Name: "broken.job"}}
	plugin.Manifest.Admin.Pages = []ManifestAdminPage{{Path: "/broken", Label: "Broken", Menu: true}}
	defaultTheme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	uploadedTheme := uploadedExtension("broken.theme", TypeTheme)
	uploadedTheme.Status = StatusEnabled
	store := &fakeExtensionStore{
		items:         map[string]Extension{plugin.ID: plugin, defaultTheme.ID: defaultTheme, uploadedTheme.ID: uploadedTheme},
		activeThemeID: uploadedTheme.ID,
	}
	registry := &safeModePageRegistry{}
	service := NewServiceWithOptions(store, t.TempDir(), "", &countingRuntimeManager{}, WithSafeMode(true), WithPageRegistry(registry))

	for name, run := range map[string]func() error{
		"enable": func() error {
			_, err := service.Enable(context.Background(), extensionManager(), plugin.ID, EnableInput{})
			return err
		},
		"verify": func() error {
			_, err := service.VerifyExtension(context.Background(), extensionManager(), plugin.ID)
			return err
		},
		"activate": func() error {
			_, err := service.ActivateTheme(context.Background(), extensionManager(), uploadedTheme.ID)
			return err
		},
		"migrate": func() error {
			_, err := service.ApplyDeclaredMigrations(context.Background(), extensionManager(), plugin.ID)
			return err
		},
	} {
		if err := run(); !errors.Is(err, ErrSafeModeActive) {
			t.Fatalf("%s error=%v", name, err)
		}
	}
	if _, err := service.MatchRoute(context.Background(), plugin.ID, "GET", "/break"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("safe route match: %v", err)
	}
	if contributions, err := service.EffectiveContributions(context.Background()); err != nil || len(contributions) != 0 {
		t.Fatalf("safe contributions=%#v err=%v", contributions, err)
	}
	if navigation, err := service.Navigation(context.Background(), extensionManager()); err != nil || len(navigation) != 0 {
		t.Fatalf("safe navigation=%#v err=%v", navigation, err)
	}
	storageCatalog := NewAttachmentStorageProviderCatalog(service)
	if providers, err := storageCatalog.ListStorageProviderCandidates(context.Background()); err != nil || len(providers) != 0 {
		t.Fatalf("safe providers=%#v err=%v", providers, err)
	}
	if _, err := service.CapabilitiesFor(context.Background(), plugin.ID); !errors.Is(err, ErrExtensionDisabled) {
		t.Fatalf("safe capabilities: %v", err)
	}
	if jobs, err := service.DeclaredJobKinds(context.Background(), plugin.ID); err != nil || len(jobs) != 0 {
		t.Fatalf("safe jobs=%#v err=%v", jobs, err)
	}

	if err := service.RestoreSafeModeThemeRegistry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if registry.registered != DefaultThemeID || store.activeThemeID != uploadedTheme.ID {
		t.Fatalf("safe registry=%q active theme mutated=%q", registry.registered, store.activeThemeID)
	}
	if !registry.cleared[plugin.ID] || !registry.cleared[uploadedTheme.ID] {
		t.Fatalf("safe registry did not clear all extensions: %#v", registry.cleared)
	}
}

func TestFrontendSafeModeForcesSchemaFallback(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	frontend := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, &fakeFrontendTrustStore{grant: frontendGrantForExtension(extension)}, newMemoryChallengeStore()).
		WithSafeMode(true)
	status, err := frontend.Frontend(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustRequired {
		t.Fatalf("safe frontend trust=%q", status.TrustState)
	}
	if _, err := frontend.Asset(context.Background(), extensionManager(), extension.ID, extension.AdminFrontendDigest, "entry"); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("safe frontend asset: %v", err)
	}
}

type safeModePageRegistry struct {
	cleared    map[string]bool
	registered string
}

func (*safeModePageRegistry) PreflightThemePackage(context.Context, Extension, string) error {
	return nil
}
func (*safeModePageRegistry) RegisterPluginPackage(context.Context, Extension) error { return nil }
func (r *safeModePageRegistry) RegisterThemePackage(_ context.Context, extension Extension) error {
	r.registered = extension.ID
	return nil
}
func (*safeModePageRegistry) RegisterDefaultThemeFallback(context.Context, Extension) error {
	return nil
}
func (r *safeModePageRegistry) RegisterThemePackageRestoring(ctx context.Context, extension Extension, _ []string) error {
	return r.RegisterThemePackage(ctx, extension)
}
func (r *safeModePageRegistry) RegisterThemePackageReplacing(_ context.Context, extension Extension, _ string) error {
	r.registered = extension.ID
	return nil
}
func (r *safeModePageRegistry) RegisterThemePackageReplacingApproved(ctx context.Context, extension Extension, previous string, _ int64) error {
	return r.RegisterThemePackageReplacing(ctx, extension, previous)
}
func (r *safeModePageRegistry) RegisterThemePackageReplacingHostApproved(ctx context.Context, extension Extension, previous string) error {
	return r.RegisterThemePackageReplacing(ctx, extension, previous)
}
func (r *safeModePageRegistry) ClearExtension(extensionID string) {
	if r.cleared == nil {
		r.cleared = map[string]bool{}
	}
	r.cleared[extensionID] = true
}
