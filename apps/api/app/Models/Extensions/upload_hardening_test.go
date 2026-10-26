package extensions

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestReadArchiveEnforcesCentralDirectoryEntryLimit(t *testing.T) {
	archive := func(entryCount int) []byte {
		t.Helper()
		files := make([]zipFile, 0, entryCount-1)
		for index := 0; index < entryCount-1; index++ {
			files = append(files, zipFile{name: fmt.Sprintf("empty/%04d/", index)})
		}
		return extensionArchive(t, validThemeManifest("entry-limit.theme"), files...)
	}

	exact := archive(maxArchiveEntries)
	reader, err := zip.NewReader(bytes.NewReader(exact), int64(len(exact)))
	if err != nil {
		t.Fatalf("open exact-limit archive: %v", err)
	}
	if len(reader.File) != maxArchiveEntries || reader.File[0].Flags&0x8 == 0 {
		t.Fatalf("boundary fixture must contain %d central entries and a data descriptor", maxArchiveEntries)
	}
	manifest, _, err := extensionpackage.ReadArchive(exact, extensionpackage.ArchiveLimits{
		Entries: maxArchiveEntries,
		Bytes:   maxArchiveBytes,
	})
	if err != nil {
		t.Fatalf("exact entry limit should be accepted: %v", err)
	}
	if manifest.ID != "entry-limit.theme" {
		t.Fatalf("unexpected manifest at exact entry limit: %#v", manifest)
	}
	if _, _, err := extensionpackage.ReadArchive(archive(maxArchiveEntries+1), extensionpackage.ArchiveLimits{
		Entries: maxArchiveEntries,
		Bytes:   maxArchiveBytes,
	}); !errors.Is(err, extensionpackage.ErrInvalidArchive) {
		t.Fatalf("entry limit + 1 should be rejected, got %v", err)
	}
}

func TestServiceInstallArchiveAuthorizesParsedManifestType(t *testing.T) {
	pluginManager := techAdminPluginManager()
	themeManager := identity.Actor{
		ID:     44,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionThemeManage: true,
		},
	}
	parentManager := identity.Actor{
		ID:     45,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionManage: true,
		},
	}
	pluginArchive := extensionArchive(t, validManifest("typed.plugin", TypePlugin))
	themeArchive := extensionArchive(t, validThemeManifest("typed.theme"),
		zipFile{name: "theme.json", body: `{"schemaVersion":1,"styles":{"tokens":{}}}`},
	)
	tests := []struct {
		name    string
		actor   identity.Actor
		archive []byte
		wantErr bool
	}{
		{name: "plugin manager uploads plugin", actor: pluginManager, archive: pluginArchive},
		{name: "plugin manager cannot upload theme", actor: pluginManager, archive: themeArchive, wantErr: true},
		{name: "theme manager uploads theme", actor: themeManager, archive: themeArchive},
		{name: "theme manager cannot upload plugin", actor: themeManager, archive: pluginArchive, wantErr: true},
		{name: "legacy parent manager uploads plugin", actor: parentManager, archive: pluginArchive},
		{name: "legacy parent manager uploads theme", actor: parentManager, archive: themeArchive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeExtensionStore{}
			root := t.TempDir()
			service := NewServiceWithOptions(store, root, "", nil, WithExecutableTrust(nil, true))
			_, err := service.InstallArchive(context.Background(), test.actor, ArchiveInput{FileName: "typed.zip", Data: test.archive})
			if test.wantErr {
				if !errors.Is(err, identity.ErrPermissionDenied) {
					t.Fatalf("expected type-specific permission denial, got %v", err)
				}
				if len(store.items) != 0 {
					t.Fatalf("denied archive reached Store: %#v", store.items)
				}
				if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
					t.Fatalf("denied archive wrote package bytes: entries=%#v err=%v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorized inert upload failed: %v", err)
			}
		})
	}
}

func TestServiceInstallArchiveRetainsReusedSnapshotAfterPreflightFailure(t *testing.T) {
	root := t.TempDir()
	manifest := validThemeManifest("reused-unsafe.theme")
	files := unsafeThemeSnapshotFiles()
	seed, err := extensionpackage.SnapshotUploaded(root, []byte(manifest), files)
	if err != nil {
		t.Fatalf("seed reused snapshot: %v", err)
	}
	service := NewService(&fakeExtensionStore{}, root)
	_, err = service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "reused-unsafe.zip",
		Data:     unsafeThemeArchive(t, manifest, files),
	})
	if !errors.Is(err, themecompiler.ErrUnsafeStaticHTML) {
		t.Fatalf("expected unsafe template failure, got %v", err)
	}
	if _, err := os.Stat(seed.Root); err != nil {
		t.Fatalf("reused snapshot was removed: %v", err)
	}
}

func TestServiceInstallArchiveRetainsStoreReferencedSnapshotAfterPreflightFailure(t *testing.T) {
	manifest := validThemeManifest("referenced-unsafe.theme")
	files := unsafeThemeSnapshotFiles()
	probe, err := extensionpackage.SnapshotUploaded(t.TempDir(), []byte(manifest), files)
	if err != nil {
		t.Fatalf("calculate referenced snapshot digest: %v", err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "referenced-unsafe.theme", "1.0.0", probe.Digest)
	store := &fakeExtensionStore{items: map[string]Extension{
		"referenced-unsafe.theme": {
			ID: "referenced-unsafe.theme", Type: TypeTheme, Version: "1.0.0",
			Source: SourceUploaded, IsDeletable: true, PackagePath: target,
		},
	}}
	service := NewService(store, root)
	_, err = service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "referenced-unsafe.zip",
		Data:     unsafeThemeArchive(t, manifest, files),
	})
	if !errors.Is(err, themecompiler.ErrUnsafeStaticHTML) {
		t.Fatalf("expected unsafe template failure, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("Store-referenced snapshot was removed: %v", err)
	}
}

func TestServiceInstallArchiveRetainsExactHistoricalPackageReference(t *testing.T) {
	manifest := validThemeManifest("historical-unsafe.theme")
	files := unsafeThemeSnapshotFiles()
	probe, err := extensionpackage.SnapshotUploaded(t.TempDir(), []byte(manifest), files)
	if err != nil {
		t.Fatalf("calculate historical snapshot digest: %v", err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "historical-unsafe.theme", "1.0.0", probe.Digest)
	store := &exactPackageReferenceStore{fakeExtensionStore: &fakeExtensionStore{}, referencedPath: target}
	service := NewService(store, root)
	_, err = service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "historical-unsafe.zip",
		Data:     unsafeThemeArchive(t, manifest, files),
	})
	if !errors.Is(err, themecompiler.ErrUnsafeStaticHTML) {
		t.Fatalf("expected unsafe template failure, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("historically referenced snapshot was removed: %v", err)
	}
}

func TestServiceInstallArchiveRetainsSnapshotWhenReferenceCheckFails(t *testing.T) {
	manifest := validThemeManifest("reference-error.theme")
	files := unsafeThemeSnapshotFiles()
	probe, err := extensionpackage.SnapshotUploaded(t.TempDir(), []byte(manifest), files)
	if err != nil {
		t.Fatalf("calculate reference-error snapshot digest: %v", err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "reference-error.theme", "1.0.0", probe.Digest)
	store := &exactPackageReferenceStore{
		fakeExtensionStore: &fakeExtensionStore{},
		referencedPath:     target,
		err:                errors.New("reference lookup unavailable"),
	}
	service := NewService(store, root)
	_, err = service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "reference-error.zip",
		Data:     unsafeThemeArchive(t, manifest, files),
	})
	if !errors.Is(err, themecompiler.ErrUnsafeStaticHTML) {
		t.Fatalf("expected unsafe template failure, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("snapshot was removed after uncertain reference lookup: %v", err)
	}
}

func TestServiceInstallArchiveRetainsSnapshotAfterAmbiguousStoreError(t *testing.T) {
	base := &fakeExtensionStore{}
	sentinel := errors.New("ambiguous post-commit read failure")
	store := &ambiguousInstallStore{fakeExtensionStore: base, err: sentinel}
	service := NewService(store, t.TempDir())

	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "ambiguous.zip",
		Data:     extensionArchive(t, validManifest("ambiguous.plugin", TypePlugin)),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected ambiguous Store error, got %v", err)
	}
	item := base.items["ambiguous.plugin"]
	if item.PackagePath == "" {
		t.Fatalf("fake Store did not persist before returning ambiguity: %#v", base.items)
	}
	if _, err := os.Stat(item.PackagePath); err != nil {
		t.Fatalf("ambiguously persisted snapshot was removed: %v", err)
	}
}

func TestServiceInstallArchiveCleansSnapshotAfterAdminDigestFailure(t *testing.T) {
	manifest := `{
		"id":"missing-admin.plugin",
		"name":"Missing Admin Asset",
		"description":"Exercises static admin digest cleanup.",
		"url":"https://example.com/missing-admin",
		"author":{"name":"SForum Test"},
		"version":"1.0.0",
		"type":"plugin",
		"sforumVersion":">=0.1.0",
		"settings":{
			"schemaVersion":1,
			"ui":{
				"mode":"component",
				"layout":"form",
				"component":{
					"id":"settings",
					"apiVersion":1,
					"entry":"frontend/admin/dist/missing.mjs"
				}
			},
			"fields":[{"key":"enabled","label":"Enabled","type":"boolean","default":"true"}]
		}
	}`
	root := t.TempDir()
	store := &fakeExtensionStore{}
	service := NewService(store, root)
	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "missing-admin.zip",
		Data:     extensionArchive(t, manifest),
	})
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected missing admin asset failure, got %v", err)
	}
	if len(store.items) != 0 {
		t.Fatalf("missing admin asset reached Store: %#v", store.items)
	}
	assertNoPublishedSnapshot(t, root, "missing-admin.plugin", "1.0.0")
}

func TestServiceInstallArchiveStopsOnInitialStoreLookupFailure(t *testing.T) {
	sentinel := errors.New("initial Store lookup unavailable")
	store := &lookupFailingInstallStore{fakeExtensionStore: &fakeExtensionStore{}, err: sentinel}
	root := t.TempDir()
	service := NewService(store, root)

	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "lookup-failure.zip",
		Data:     extensionArchive(t, validManifest("lookup-failure.plugin", TypePlugin)),
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected initial Store lookup error, got %v", err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("Store lookup failure wrote package bytes: entries=%#v err=%v", entries, readErr)
	}
}

func unsafeThemeSnapshotFiles() []extensionpackage.File {
	return []extensionpackage.File{
		{Path: "theme.json", Mode: 0o644, Body: []byte(`{"schemaVersion":1,"styles":{"tokens":{}}}`)},
		{Path: "templates/unused.html", Mode: 0o644, Body: []byte(`<img src="x" onerror="alert(1)">`)},
	}
}

func unsafeThemeArchive(t *testing.T, manifest string, files []extensionpackage.File) []byte {
	t.Helper()
	return extensionArchive(t, manifest,
		zipFile{name: files[0].Path, body: string(files[0].Body), mode: files[0].Mode},
		zipFile{name: files[1].Path, body: string(files[1].Body), mode: files[1].Mode},
	)
}

func assertNoPublishedSnapshot(t *testing.T, root string, id string, version string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, id, version))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read snapshot version root: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != ".locks" && !strings.HasPrefix(entry.Name(), ".snapshot-") {
			t.Fatalf("unexpected published snapshot after failure: %s", entry.Name())
		}
	}
}

type ambiguousInstallStore struct {
	*fakeExtensionStore
	err error
}

type lookupFailingInstallStore struct {
	*fakeExtensionStore
	err error
}

func (s *lookupFailingInstallStore) Get(context.Context, string) (Extension, error) {
	return Extension{}, s.err
}

func (s *ambiguousInstallStore) SaveInstalled(ctx context.Context, input SaveInstalledInput) (Extension, error) {
	if _, err := s.fakeExtensionStore.SaveInstalled(ctx, input); err != nil {
		return Extension{}, err
	}
	return Extension{}, s.err
}

type exactPackageReferenceStore struct {
	*fakeExtensionStore
	referencedPath string
	err            error
}

func (s *exactPackageReferenceStore) PackagePathReferenced(_ context.Context, packagePath string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return samePackagePath(packagePath, s.referencedPath), nil
}
