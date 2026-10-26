package extensions

import (
	"context"
	"errors"
	"testing"

	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
)

func TestRequireSuperAdminForUntrustedBackend(t *testing.T) {
	backend := Manifest{Backend: ManifestBackend{Entry: "backend/plugin"}}
	frontendOnly := Manifest{Backend: ManifestBackend{}}

	tech := techAdminPluginManager()
	super := extensionManager()

	if err := requireSuperAdminForUntrustedBackend(tech, SourceUploaded, backend); !errors.Is(err, ErrUntrustedBackendRestricted) {
		t.Fatalf("tech_admin + uploaded backend: want restricted, got %v", err)
	}
	if err := requireSuperAdminForUntrustedBackend(super, SourceUploaded, backend); err != nil {
		t.Fatalf("super_admin + uploaded backend: %v", err)
	}
	if err := requireSuperAdminForUntrustedBackend(tech, SourceBuiltin, backend); err != nil {
		t.Fatalf("tech_admin + builtin backend should be allowed: %v", err)
	}
	if err := requireSuperAdminForUntrustedBackend(tech, SourceUploaded, frontendOnly); err != nil {
		t.Fatalf("tech_admin + frontend-only should be allowed: %v", err)
	}
}

func TestTechAdminCannotInstallUploadedBackendPlugin(t *testing.T) {
	store := &fakeExtensionStore{}
	auditor := &recordingAuditor{}
	service := NewService(store, t.TempDir())
	WithAuditor(auditor)(service)

	_, err := service.InstallArchive(context.Background(), techAdminPluginManager(), ArchiveInput{
		FileName: "backend.zip",
		Data:     extensionArchive(t, validManifest("evil.plugin", TypePlugin)),
	})
	if !errors.Is(err, ErrUntrustedBackendRestricted) {
		t.Fatalf("expected untrusted backend restricted, got %v", err)
	}
	if store.saved.Manifest.ID != "" {
		t.Fatalf("install must not persist package on deny: %#v", store.saved)
	}
	if !auditor.hasAction(audit.ActionExtensionBackendDenied) {
		t.Fatalf("expected backend denied audit, got %#v", auditor.events)
	}
}

func TestSuperAdminCanInstallUploadedBackendPlugin(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	installed, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "backend.zip",
		Data:     extensionArchive(t, validManifest("ok.plugin", TypePlugin)),
	})
	if err != nil {
		t.Fatalf("super_admin install: %v", err)
	}
	if installed.ID != "ok.plugin" {
		t.Fatalf("unexpected id %s", installed.ID)
	}
}

func TestTechAdminCanInstallFrontendOnlyPlugin(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	// 无 backend.entry：纯前端/配置类插件仍可由 plugin.manage 安装。
	manifest := `{
		"manifestVersion": 3,
		"id": "ui.plugin",
		"name": "UI Plugin",
		"description": "Frontend only plugin.",
		"url": "https://example.com/ui",
		"author": {"name": "SForum Team"},
		"version": "1.0.0",
		"type": "plugin",
		"sforumVersion": ">=0.1.0"
	}`
	installed, err := service.InstallArchive(context.Background(), techAdminPluginManager(), ArchiveInput{
		FileName: "ui.zip",
		Data:     extensionArchive(t, manifest),
	})
	if err != nil {
		t.Fatalf("frontend-only install: %v", err)
	}
	if installed.ID != "ui.plugin" || hasExecutableBackend(installed.Manifest) {
		t.Fatalf("unexpected install: %#v", installed)
	}
}

func TestTechAdminCannotEnableUploadedBackendPlugin(t *testing.T) {
	item := withInstalledPackage(t, installedExtension("evil.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}))
	item.Source = SourceUploaded
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	auditor := &recordingAuditor{}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})
	WithAuditor(auditor)(service)

	_, err := service.Enable(context.Background(), techAdminPluginManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrUntrustedBackendRestricted) {
		t.Fatalf("expected restricted, got %v", err)
	}
	if store.enabledID != "" {
		t.Fatalf("enable must not run on deny, store.enabledID=%q", store.enabledID)
	}
	if !auditor.hasAction(audit.ActionExtensionBackendDenied) {
		t.Fatalf("expected audit deny event")
	}
}

func TestTechAdminCanEnableBuiltinBackendPlugin(t *testing.T) {
	item := protectedBuiltinExtension("sforum.demo", TypePlugin)
	item.Manifest = installedExtension(item.ID, TypePlugin, ManifestBackend{Entry: "backend/plugin"}).Manifest
	item.Status = StatusInstalled
	item = withInstalledPackage(t, item)
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	enabled, err := service.Enable(context.Background(), techAdminPluginManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if err != nil {
		t.Fatalf("builtin enable: %v", err)
	}
	if enabled.Status != StatusEnabled {
		t.Fatalf("status=%s", enabled.Status)
	}
}

func TestTechAdminCanDisableUploadedBackendPlugin(t *testing.T) {
	// 禁用不启动进程；tech_admin 仍可运维已装后端插件的停用。
	item := installedExtension("evil.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Source = SourceUploaded
	item.Status = StatusEnabled
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewService(store, t.TempDir())

	disabled, err := service.Disable(context.Background(), techAdminPluginManager(), item.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Status != StatusDisabled {
		t.Fatalf("status=%s", disabled.Status)
	}
}

func TestTechAdminCannotVerifyOrUninstallUploadedBackend(t *testing.T) {
	item := withInstalledPackage(t, installedExtension("evil.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}))
	item.Source = SourceUploaded
	item.IsDeletable = true
	item.Status = StatusDisabled
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	if _, err := service.VerifyExtension(context.Background(), techAdminPluginManager(), item.ID); !errors.Is(err, ErrUntrustedBackendRestricted) {
		t.Fatalf("verify: want restricted, got %v", err)
	}
	if err := service.Uninstall(context.Background(), techAdminPluginManager(), item.ID, UninstallInput{}); !errors.Is(err, ErrUntrustedBackendRestricted) {
		t.Fatalf("uninstall: want restricted, got %v", err)
	}
}

func TestSuperAdminCanEnableUploadedBackendPlugin(t *testing.T) {
	item := withInstalledPackage(t, installedExtension("ok.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}))
	item.Source = SourceUploaded
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	enabled, err := service.Enable(context.Background(), extensionManager(), item.ID, EnableInput{ConfirmCapabilities: true})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.Status != StatusEnabled {
		t.Fatalf("status=%s", enabled.Status)
	}
}

type recordingAuditor struct {
	events []audit.Event
}

func (r *recordingAuditor) Append(_ context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return nil
}

func (r *recordingAuditor) hasAction(action string) bool {
	for _, e := range r.events {
		if e.Action == action {
			return true
		}
	}
	return false
}
