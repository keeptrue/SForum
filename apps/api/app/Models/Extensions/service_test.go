package extensions

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestServiceInstallArchiveRequiresExtensionManagePermission(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "sample.zip",
		Data:     []byte("not a zip"),
	})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied before archive parsing, got %v", err)
	}
}

func TestServiceInstallArchiveValidatesManifestAndSafeZipPaths(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	actor := extensionManager()

	_, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "missing.zip",
		Data:     extensionArchive(t, "", zipFile{name: "README.md", body: "hello"}),
	})
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("expected missing manifest to be invalid archive, got %v", err)
	}

	unsafePaths := []string{
		"../escape.txt",
		"assets/../../escape.txt",
		"..\\escape.txt",
		"/absolute.txt",
		"\\\\server\\share\\escape.txt",
		"C:\\escape.txt",
		"assets/file..txt",
		"assets/invalid\x00name.txt",
	}
	for _, unsafePath := range unsafePaths {
		t.Run("unsafe path "+unsafePath, func(t *testing.T) {
			_, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
				FileName: "unsafe.zip",
				Data: extensionArchive(t, validManifest("demo.plugin", TypePlugin),
					zipFile{name: unsafePath, body: "oops"},
				),
			})
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("expected unsafe path %q to be invalid archive, got %v", unsafePath, err)
			}
		})
	}

	_, err = service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "bad-manifest.zip",
		Data:     extensionArchive(t, `{"id":"Bad Space","name":"Broken","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0"}`),
	})
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid manifest, got %v", err)
	}
}

func TestServiceInstallArchiveRejectsZipSymlink(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())

	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "symlink.zip",
		Data: extensionArchive(t, validManifest("demo.plugin", TypePlugin),
			zipFile{name: "backend/plugin", body: "../../outside", mode: os.ModeSymlink | 0o777},
		),
	})

	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("expected symlink entry to be an invalid archive, got %v", err)
	}
}

func TestServiceInstallArchiveRejectsAmbiguousZipEntries(t *testing.T) {
	tests := []struct {
		name  string
		files []zipFile
	}{
		{
			name: "duplicate normalized path",
			files: []zipFile{
				{name: "frontend//Cell.vue", body: "first"},
				{name: "frontend/Cell.vue", body: "second"},
			},
		},
		{
			name: "file and directory collision",
			files: []zipFile{
				{name: "assets", body: "file"},
				{name: "assets/icon.svg", body: "icon"},
			},
		},
		{
			name: "duplicate manifest",
			files: []zipFile{
				{name: ManifestFileName, body: validManifest("other.plugin", TypePlugin)},
			},
		},
		{
			name: "special file mode",
			files: []zipFile{
				{name: "frontend/channel", body: "special", mode: os.ModeNamedPipe | 0o644},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(&fakeExtensionStore{}, t.TempDir())
			_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
				FileName: "ambiguous.zip",
				Data:     extensionArchive(t, validManifest("demo.plugin", TypePlugin), test.files...),
			})
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("expected invalid archive, got %v", err)
			}
		})
	}
}

func TestServiceInstallArchiveStoresManifestPackageAndEvent(t *testing.T) {
	store := &fakeExtensionStore{}
	extensionRoot := t.TempDir()
	service := NewService(store, extensionRoot)

	installed, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "sample.zip",
		Data: extensionArchive(t, validManifest("demo.plugin", TypePlugin),
			zipFile{name: "backend/plugin", body: "#!/bin/sh\n"},
			zipFile{name: "README.md", body: "hello"},
		),
	})
	if err != nil {
		t.Fatalf("InstallArchive returned error: %v", err)
	}

	if installed.ID != "demo.plugin" || installed.Version != "1.0.0" || installed.Status != StatusInstalled {
		t.Fatalf("unexpected installed extension: %#v", installed)
	}
	if store.saved.Manifest.ID != "demo.plugin" || store.saved.Manifest.Type != TypePlugin {
		t.Fatalf("manifest was not saved: %#v", store.saved.Manifest)
	}
	if store.saved.PackagePath == "" || store.saved.PackageDigest == "" {
		t.Fatalf("expected snapshot path and digest to be stored: %#v", store.saved)
	}
	if installed.PackagePath != store.saved.PackagePath || installed.PackageDigest != store.saved.PackageDigest {
		t.Fatalf("installed extension did not propagate snapshot identity: installed=%#v saved=%#v", installed, store.saved)
	}
	packageInfo, err := os.Stat(installed.PackagePath)
	if err != nil || !packageInfo.IsDir() {
		t.Fatalf("expected package path to be a snapshot directory, info=%#v err=%v", packageInfo, err)
	}
	if _, err := os.Stat(filepath.Join(installed.PackagePath, ManifestFileName)); err != nil {
		t.Fatalf("expected canonical manifest in snapshot root: %v", err)
	}
	backendPath, ok := installedFilePath(installed, "backend/plugin")
	if !ok || backendPath != filepath.Join(installed.PackagePath, "backend", "plugin") {
		t.Fatalf("digest-backed extension path did not resolve from snapshot root: path=%q ok=%t", backendPath, ok)
	}
	if body, err := os.ReadFile(backendPath); err != nil || string(body) != "#!/bin/sh\n" {
		t.Fatalf("unexpected snapshotted backend: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(installed.PackagePath, "package.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot must not retain the uploaded ZIP wrapper, got %v", err)
	}
	if len(store.events) != 1 || store.events[0].Action != EventInstalled {
		t.Fatalf("expected install event, got %#v", store.events)
	}
}

func TestServiceInstallArchiveKeepsDifferentContentForSameIDAndVersion(t *testing.T) {
	store := &fakeExtensionStore{}
	service := NewService(store, t.TempDir())
	manifest := validManifest("changed.plugin", TypePlugin)

	first, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "first.zip",
		Data: extensionArchive(t, manifest,
			zipFile{name: "README.md", body: "first"},
		),
	})
	if err != nil {
		t.Fatalf("install first package: %v", err)
	}
	second, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "second.zip",
		Data: extensionArchive(t, manifest,
			zipFile{name: "README.md", body: "second"},
		),
	})
	if err != nil {
		t.Fatalf("install changed package: %v", err)
	}

	if first.PackageDigest == "" || second.StagedVersion == nil || second.StagedVersion.PackageDigest == "" {
		t.Fatalf("expected both package digests: first=%#v second=%#v", first, second)
	}
	if first.PackageDigest != second.PackageDigest || first.PackagePath != second.PackagePath {
		t.Fatalf("static upload replaced active snapshot: first=%#v second=%#v", first, second)
	}
	if first.PackageDigest == second.StagedVersion.PackageDigest || first.PackagePath == second.StagedVersion.PackagePath {
		t.Fatalf("different candidate content reused active snapshot: first=%#v staged=%#v", first, second.StagedVersion)
	}
	firstBody, err := os.ReadFile(filepath.Join(first.PackagePath, "README.md"))
	if err != nil {
		t.Fatalf("read first immutable package: %v", err)
	}
	secondBody, err := os.ReadFile(filepath.Join(second.StagedVersion.PackagePath, "README.md"))
	if err != nil {
		t.Fatalf("read second immutable package: %v", err)
	}
	if string(firstBody) != "first" || string(secondBody) != "second" {
		t.Fatalf("package snapshots were overwritten: first=%q second=%q", firstBody, secondBody)
	}
}

func TestServiceInstallArchiveRejectsReservedDefaultThemeID(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())

	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "default-theme.zip",
		Data:     extensionArchive(t, validManifest(DefaultThemeID, TypeTheme)),
	})
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected reserved default theme id to be rejected, got %v", err)
	}
}

func TestServiceInstallArchiveAllowsThemeSettingsAndAdminPages(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	actor := extensionManager()

	installed, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "starter-theme.zip",
		Data: extensionArchive(t, validThemeManifest("starter.theme"),
			zipFile{name: "theme.json", body: `{"schemaVersion":1,"styles":{"tokens":{}}}`},
		),
	})
	if err != nil {
		t.Fatalf("expected minimal theme manifest to install, got %v", err)
	}
	if installed.Type != TypeTheme {
		t.Fatalf("unexpected installed theme: %#v", installed)
	}

	themeWithAdmin, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "starter-theme-admin.zip",
		Data: extensionArchive(t, `{
			"manifestVersion":3,
			"id":"starter.admin.theme",
			"name":"Starter Admin Theme",
			"description":"Theme with admin settings.",
			"url":"https://example.com/starter-admin-theme",
			"author":{"name":"SForum Team","url":"https://example.com","email":"dev@example.com"},
			"version":"1.0.0",
			"type":"theme",
			"sforumVersion":">=0.1.0",
			"settings":[{"key":"theme.banner","label":"Banner","type":"text","default":"Welcome"}],
			"adminPages":[{"path":"/settings","label":"Settings","view":"settings","icon":"i-lucide-settings","order":10}]
		}`,
		),
	})
	if err != nil {
		t.Fatalf("expected theme settings/admin page manifest to install, got %v", err)
	}
	if len(themeWithAdmin.Manifest.Settings) != 1 || len(themeWithAdmin.Manifest.AdminPages) != 1 {
		t.Fatalf("expected theme settings and admin pages to be preserved, got %#v", themeWithAdmin.Manifest)
	}

	cases := []struct {
		name     string
		manifest string
	}{
		{
			name: "missing frontend layer",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0"
			}`,
		},
		{
			name: "theme declares permissions",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"permissions":["topic.create"],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares migrations",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"migrations":[{"path":"migrations/001.sql"}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares backend",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"backend":{"entry":"backend/plugin","rpc":"hashicorp-go-plugin"},"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares routes",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"routes":[{"path":"/hello","methods":["GET"]}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares hooks",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"hooks":[{"name":"topic.created"}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares events",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"events":[{"name":"topic.created","kind":"observe"}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares jobs",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"jobs":[{"name":"demo.sync"}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
		{
			name: "theme declares providers",
			manifest: `{
				"id":"bad.theme","name":"Bad Theme","version":"1.0.0","type":"theme","sforumVersion":">=0.1.0",
				"providers":[{"slot":"search.provider","label":"Search"}],"frontend":{"layer":"frontend/layer"}
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
				FileName: "bad-theme.zip",
				Data:     extensionArchive(t, tc.manifest),
			})
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected invalid theme manifest, got %v", err)
			}
		})
	}
}

func TestServiceInstallArchiveRejectsUnsafeThemeTemplateBeforeStore(t *testing.T) {
	store := &fakeExtensionStore{}
	root := t.TempDir()
	service := NewService(store, root)

	_, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "unsafe-theme.zip",
		Data: extensionArchive(t, validThemeManifest("unsafe.theme"),
			zipFile{name: "theme.json", body: `{"schemaVersion":1,"styles":{"tokens":{}}}`},
			zipFile{name: "templates/unused.html", body: `<img src="x" onerror="alert(1)">`},
		),
	})
	if !errors.Is(err, ErrInvalidManifest) || !errors.Is(err, themecompiler.ErrUnsafeStaticHTML) {
		t.Fatalf("expected install-time unsafe template rejection, got %v", err)
	}
	if store.saved.ID != "" || len(store.items) != 0 {
		t.Fatalf("unsafe theme reached Store: saved=%#v items=%#v", store.saved, store.items)
	}
	assertNoPublishedSnapshot(t, root, "unsafe.theme", "1.0.0")
}

func TestServiceListsContributionPointsAndEffectiveContributions(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{}}
	service := NewService(store, t.TempDir())
	store.items["beta.plugin"] = contributionTestPlugin("beta.plugin", StatusEnabled, []ManifestContribution{
		topicActionContribution(t, "beta.later", 200, "/topic-actions/later"),
	})
	store.items["alpha.plugin"] = contributionTestPlugin("alpha.plugin", StatusEnabled, []ManifestContribution{
		topicActionContribution(t, "alpha.first", 100, "/topic-actions/first"),
		topicActionContribution(t, "alpha.same", 200, "/topic-actions/same"),
	})
	store.items["disabled.plugin"] = contributionTestPlugin("disabled.plugin", StatusDisabled, []ManifestContribution{
		topicActionContribution(t, "disabled.hidden", 1, "/topic-actions/hidden"),
	})
	store.items["theme.demo"] = Extension{
		ID:      "theme.demo",
		Name:    "Theme Demo",
		Version: "1.0.0",
		Type:    TypeTheme,
		Status:  StatusEnabled,
		Manifest: Manifest{
			ID:            "theme.demo",
			Name:          "Theme Demo",
			Description:   "Theme demo.",
			URL:           "https://example.com/theme",
			Author:        ManifestAuthor{Name: "SForum Team"},
			Version:       "1.0.0",
			Type:          TypeTheme,
			SForumVersion: ">=0.1.0",
		},
	}

	points, err := service.ContributionPoints(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("ContributionPoints returned error: %v", err)
	}
	// 与 ExtensionManifest.ContributionPointDefinitions 生产目录对齐（含 F4.3 + E2 + 页面区域）。
	if len(points) != 11 || points[0].ID != "forum.topic.actions" {
		t.Fatalf("unexpected contribution points: %#v", points)
	}
	pointIDs := make(map[string]bool, len(points))
	for _, point := range points {
		pointIDs[point.ID] = true
	}
	for _, id := range []string{
		"forum.topic.sidebar",
		"forum.topic.badges",
		"forum.comment.actions",
		"forum.nav.items",
		"forum.topic.list.badges",
		"forum.composer.toolbar",
		"forum.profile.tabs",
		"admin.dashboard.widgets",
		"system.health.checks",
	} {
		if !pointIDs[id] {
			t.Fatalf("missing F4.3/E2 contribution point %q in %#v", id, points)
		}
	}

	contributions, err := service.Contributions(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("Contributions returned error: %v", err)
	}
	if got := contributionIDs(contributions); !slices.Equal(got, []string{"alpha.plugin:alpha.first", "alpha.plugin:alpha.same", "beta.plugin:beta.later"}) {
		t.Fatalf("unexpected contribution order: %#v", got)
	}
	if contributions[0].ExtensionName != "Alpha Plugin" || contributions[0].Point != "forum.topic.actions" {
		t.Fatalf("unexpected contribution metadata: %#v", contributions[0])
	}
}

func TestServiceEffectiveContributionsResolveWithoutAdminActor(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{}}
	service := NewService(store, t.TempDir())
	store.items["beta.plugin"] = contributionTestPlugin("beta.plugin", StatusEnabled, []ManifestContribution{
		topicActionContribution(t, "beta.later", 200, "/topic-actions/later"),
	})
	store.items["alpha.plugin"] = contributionTestPlugin("alpha.plugin", StatusEnabled, []ManifestContribution{
		topicActionContribution(t, "alpha.first", 100, "/topic-actions/first"),
	})
	store.items["disabled.plugin"] = contributionTestPlugin("disabled.plugin", StatusDisabled, []ManifestContribution{
		topicActionContribution(t, "disabled.hidden", 1, "/topic-actions/hidden"),
	})

	contributions, err := service.EffectiveContributions(context.Background())
	if err != nil {
		t.Fatalf("EffectiveContributions returned error: %v", err)
	}
	if got := contributionIDs(contributions); !slices.Equal(got, []string{"alpha.plugin:alpha.first", "beta.plugin:beta.later"}) {
		t.Fatalf("unexpected runtime contribution order: %#v", got)
	}
}

func TestServiceEffectiveContributionsRespectEnabledBySetting(t *testing.T) {
	store := &fakeExtensionStore{
		items:    map[string]Extension{},
		settings: map[string]map[string]string{},
	}
	service := NewService(store, t.TempDir())

	badgePayload, err := json.Marshal(map[string]string{"tone": "info", "href": "/guidelines"})
	if err != nil {
		t.Fatalf("marshal badge payload: %v", err)
	}
	plugin := contributionTestPlugin("policy.plugin", StatusEnabled, []ManifestContribution{
		{
			Point:            "forum.topic.badges",
			ID:               "content-policy-active",
			Order:            50,
			Label:            map[string]string{"zh-CN": "内容策略", "en-US": "Content policy"},
			Icon:             "i-lucide-shield-check",
			EnabledBySetting: "show_topic_badge",
			Payload:          badgePayload,
		},
		topicActionContribution(t, "always-on", 10, "/topic-actions/always"),
	})
	plugin.Manifest.Settings = []ManifestSetting{{
		Key: "show_topic_badge", Label: LocalizedText{Default: "Show badge"}, Type: "boolean", Default: "false",
	}}
	store.items[plugin.ID] = plugin

	// 默认 false：门控徽章不出现，无门控贡献仍在。
	contributions, err := service.EffectiveContributions(context.Background())
	if err != nil {
		t.Fatalf("EffectiveContributions: %v", err)
	}
	if got := contributionIDs(contributions); !slices.Equal(got, []string{"policy.plugin:always-on"}) {
		t.Fatalf("default-off badge should be hidden: %#v", got)
	}

	store.settings[plugin.ID] = map[string]string{"show_topic_badge": "true"}
	contributions, err = service.EffectiveContributions(context.Background())
	if err != nil {
		t.Fatalf("EffectiveContributions after enable: %v", err)
	}
	if got := contributionIDs(contributions); !slices.Equal(got, []string{"policy.plugin:always-on", "policy.plugin:content-policy-active"}) {
		t.Fatalf("enabled badge should appear: %#v", got)
	}
}

func TestServiceContributionInspectionRequiresExtensionManage(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	if _, err := service.ContributionPoints(context.Background(), actor); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for contribution points, got %v", err)
	}
	if _, err := service.Contributions(context.Background(), actor); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied for contributions, got %v", err)
	}
}

func TestServiceSyncBuiltinsPrunesRemovedBuiltinExtensions(t *testing.T) {
	builtinRoot := t.TempDir()
	themeRoot := filepath.Join(builtinRoot, "themes", "sforum-default")
	if err := os.MkdirAll(themeRoot, 0o755); err != nil {
		t.Fatalf("create builtin theme root: %v", err)
	}
	defaultTheme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	defaultTheme.PackagePath = themeRoot
	if err := writeManifest(themeRoot, defaultTheme.Manifest); err != nil {
		t.Fatalf("write builtin theme manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "theme.json"), []byte(`{"schemaVersion":1,"styles":{"tokens":{}}}`), 0o644); err != nil {
		t.Fatalf("write builtin theme contract: %v", err)
	}

	stalePlugin := protectedBuiltinExtension("sforum.default", TypePlugin)
	stalePlugin.Name = "SForum Default Plugin"
	stalePlugin.Manifest.Name = stalePlugin.Name
	stalePlugin.Manifest.Backend = ManifestBackend{RPC: "hashicorp-go-plugin"}
	stalePlugin.PackagePath = filepath.Join(builtinRoot, "plugins", "sforum-default")
	store := &fakeExtensionStore{items: map[string]Extension{
		stalePlugin.ID: stalePlugin,
	}}
	service := NewServiceWithBuiltins(store, t.TempDir(), builtinRoot)

	if _, err := service.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins returned error: %v", err)
	}
	if _, ok := store.items[stalePlugin.ID]; ok {
		t.Fatalf("expected stale builtin plugin to be pruned")
	}
	if item, ok := store.items[DefaultThemeID]; !ok || item.Source != SourceBuiltin {
		t.Fatalf("expected current builtin theme to remain, got %#v", item)
	}
}

func TestServiceSyncBuiltinsStoresImmutableSnapshotIdentity(t *testing.T) {
	builtinRoot := t.TempDir()
	extensionRoot := t.TempDir()
	themeRoot := filepath.Join(builtinRoot, "themes", "sforum-default")
	defaultTheme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	if err := os.MkdirAll(themeRoot, 0o755); err != nil {
		t.Fatalf("create builtin theme root: %v", err)
	}
	if err := writeManifest(themeRoot, defaultTheme.Manifest); err != nil {
		t.Fatalf("write builtin theme manifest: %v", err)
	}
	const sourceBody = `{"schemaVersion":1,"styles":{"tokens":{}}}`
	sourceTheme := filepath.Join(themeRoot, "theme.json")
	if err := os.WriteFile(sourceTheme, []byte(sourceBody), 0o644); err != nil {
		t.Fatalf("write builtin theme contract: %v", err)
	}
	store := &fakeExtensionStore{items: map[string]Extension{}}
	service := NewServiceWithBuiltins(store, extensionRoot, builtinRoot)

	if _, err := service.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins returned error: %v", err)
	}
	saved := store.items[DefaultThemeID]
	if saved.PackagePath == "" || saved.PackageDigest == "" {
		t.Fatalf("expected builtin snapshot identity, got %#v", saved)
	}
	if saved.PackagePath == themeRoot || !strings.HasPrefix(saved.PackagePath, filepath.Clean(extensionRoot)+string(os.PathSeparator)) {
		t.Fatalf("builtin package was not copied below extension root: source=%q saved=%q", themeRoot, saved.PackagePath)
	}
	snapshotTheme := filepath.Join(saved.PackagePath, "theme.json")
	if err := os.WriteFile(sourceTheme, []byte("changed after sync"), 0o644); err != nil {
		t.Fatalf("change builtin source after sync: %v", err)
	}
	body, err := os.ReadFile(snapshotTheme)
	if err != nil {
		t.Fatalf("read builtin snapshot layer: %v", err)
	}
	if string(body) != sourceBody {
		t.Fatalf("builtin snapshot changed with source tree: %q", body)
	}
}

func TestServiceSyncBuiltinsPreservesSelectedUploadedThemeAcrossRestart(t *testing.T) {
	builtinRoot := t.TempDir()
	themeRoot := filepath.Join(builtinRoot, "themes", "sforum-default")
	defaultTheme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	if err := os.MkdirAll(themeRoot, 0o755); err != nil {
		t.Fatalf("create builtin theme root: %v", err)
	}
	if err := writeManifest(themeRoot, defaultTheme.Manifest); err != nil {
		t.Fatalf("write builtin theme manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "theme.json"), []byte(`{"pages":[]}`), 0o644); err != nil {
		t.Fatalf("write builtin theme contract: %v", err)
	}

	selected := Extension{
		ID: "operator.theme", Name: "Operator Theme", Version: "2.0.0", Type: TypeTheme,
		Status: StatusEnabled, Source: SourceUploaded, PackageDigest: strings.Repeat("a", 64),
		Manifest: Manifest{
			ID: "operator.theme", Name: "Operator Theme", Version: "2.0.0",
			Type: TypeTheme, SForumVersion: ">=0.1.0",
		},
	}
	store := &fakeExtensionStore{
		items: map[string]Extension{selected.ID: selected}, activeThemeID: selected.ID,
	}
	service := NewServiceWithBuiltins(store, t.TempDir(), builtinRoot)

	if _, err := service.SyncBuiltins(context.Background()); err != nil {
		t.Fatalf("SyncBuiltins returned error: %v", err)
	}
	active, err := store.ActiveTheme(context.Background())
	if err != nil || active.ID != selected.ID || store.activeThemeID != selected.ID {
		t.Fatalf("selected theme changed during restart sync: active=%#v activeID=%q err=%v", active, store.activeThemeID, err)
	}
	if store.themePublicationRevision != 0 {
		t.Fatalf("restart sync published an unexpected activation revision: %d", store.themePublicationRevision)
	}
}

func TestServiceSyncBuiltinsRejectsUploadedIDCollision(t *testing.T) {
	builtinRoot := t.TempDir()
	id := "operator.future-builtin"
	packageRoot := filepath.Join(builtinRoot, "themes", id)
	builtin := protectedBuiltinExtension(id, TypeTheme)
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(packageRoot, builtin.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "theme.json"), []byte(`{"pages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	uploaded := builtin
	uploaded.Source = SourceUploaded
	uploaded.IsSystem = false
	uploaded.IsDeletable = true
	uploaded.PackagePath = t.TempDir()
	uploaded.PackageDigest = strings.Repeat("a", 64)
	store := &fakeExtensionStore{items: map[string]Extension{id: uploaded}, activeThemeID: id}
	service := NewServiceWithBuiltins(store, t.TempDir(), builtinRoot)

	if _, err := service.SyncBuiltins(t.Context()); !errors.Is(err, ErrNotDeletable) {
		t.Fatalf("builtin/uploaded collision error=%v", err)
	}
	after := store.items[id]
	if after.Source != SourceUploaded || after.IsSystem || !after.IsDeletable ||
		after.PackageDigest != uploaded.PackageDigest || after.PackagePath != uploaded.PackagePath {
		t.Fatalf("collision mutated uploaded extension: %#v", after)
	}
}

func TestServiceInstallArchiveValidatesRuntimeManifestDeclarations(t *testing.T) {
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	actor := extensionManager()

	valid := validManifest("runtime.plugin", TypePlugin)
	installed, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "runtime.zip",
		Data: extensionArchive(t, valid,
			zipFile{name: "backend/plugin", body: "#!/bin/sh\n"},
		),
	})
	if err != nil {
		t.Fatalf("expected runtime manifest to install, got %v", err)
	}
	if events := DeclaredManifestEvents(installed.Manifest); len(events) != 1 || events[0].Name != "topic.created" {
		t.Fatalf("expected hooks compatibility event declaration, got %#v", events)
	}

	eventManifest := `{
		"manifestVersion":3,
		"id":"event.plugin",
		"name":"Event Plugin",
		"description":"Event plugin.",
		"url":"https://example.com/event-plugin",
		"author":{"name":"SForum Team","url":"https://example.com","email":"dev@example.com"},
		"version":"1.0.0",
		"type":"plugin",
		"sforumVersion":">=0.1.0",
		"events":[{
			"id":"event.plugin.event.topic-before-create",
			"contractVersion":"event.plugin.event.topic-before-create@1",
			"name":"topic.before_create",
			"kind":"filter",
			"handler":"demo.topic-before-create",
			"inputSchema":"event.plugin.topic-before-create.input@1",
			"resultSchema":"event.plugin.topic-before-create.result@1",
			"timeoutMs":1000
		}]
	}`
	installed, err = service.InstallArchive(context.Background(), actor, ArchiveInput{
		FileName: "event.zip",
		Data:     extensionArchive(t, eventManifest),
	})
	if err != nil {
		t.Fatalf("expected event manifest to install, got %v", err)
	}
	if events := DeclaredManifestEvents(installed.Manifest); len(events) != 1 || events[0].Kind != "filter" {
		t.Fatalf("expected filter event declaration, got %#v", events)
	}

	cases := []struct {
		name     string
		manifest string
	}{
		{
			name: "unsafe route path",
			manifest: `{
				"id":"bad.route","name":"Bad Route","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"routes":[{"path":"../escape","methods":["GET"]}]
			}`,
		},
		{
			name: "public write route",
			manifest: `{
				"id":"bad.public","name":"Bad Public","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"routes":[{"path":"/write","methods":["POST"],"access":"public"}]
			}`,
		},
		{
			name: "unknown hook",
			manifest: `{
				"id":"bad.hook","name":"Bad Hook","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"hooks":[{"name":"topic.destroyed"}]
			}`,
		},
		{
			name: "unknown provider",
			manifest: `{
				"id":"bad.provider","name":"Bad Provider","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"providers":[{"slot":"unknown.provider","label":"Unknown"}]
			}`,
		},
		{
			name: "unknown event",
			manifest: `{
				"id":"bad.event","name":"Bad Event","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"events":[{"name":"topic.destroyed","kind":"observe"}]
			}`,
		},
		{
			name: "wrong event kind",
			manifest: `{
				"id":"bad.event.kind","name":"Bad Event Kind","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"events":[{"name":"topic.before_create","kind":"observe"}]
			}`,
		},
		{
			name: "permission route without manifest permission",
			manifest: `{
				"id":"bad.permission","name":"Bad Permission","version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0",
				"routes":[{"path":"/admin","methods":["POST"],"access":"permission","permission":"extension.bad.manage"}]
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.InstallArchive(context.Background(), actor, ArchiveInput{
				FileName: "bad.zip",
				Data:     extensionArchive(t, tc.manifest),
			})
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected invalid manifest, got %v", err)
			}
		})
	}
}

func TestServiceNavigationUsesOnlyExplicitMenuPagesFromEnabledPluginsAndActiveTheme(t *testing.T) {
	enabledPlugin := installedExtension("enabled.plugin", TypePlugin, ManifestBackend{})
	enabledPlugin.Status = StatusEnabled
	enabledPlugin.Manifest.Admin = ManifestAdmin{
		Entry: "/settings",
		Pages: []ManifestAdminPage{
			{Path: "/settings", Label: "Settings", View: "settings", Icon: "i-lucide-settings", Order: 20},
			{Path: "/dashboard", Label: "Dashboard", View: "about", Icon: "i-lucide-layout-dashboard", Order: 10, Menu: true},
		},
	}
	disabledPlugin := installedExtension("disabled.plugin", TypePlugin, ManifestBackend{})
	disabledPlugin.Status = StatusDisabled
	disabledPlugin.Manifest.Admin = ManifestAdmin{
		Pages: []ManifestAdminPage{{Path: "/hidden", Label: "Hidden", View: "about", Menu: true}},
	}
	activeTheme := installedExtension("active.theme", TypeTheme, ManifestBackend{})
	activeTheme.Status = StatusEnabled
	activeTheme.Manifest.Admin = ManifestAdmin{
		Pages: []ManifestAdminPage{{Path: "/theme", Label: "Theme", View: "about", Order: 30, Menu: true}},
	}
	store := &fakeExtensionStore{items: map[string]Extension{
		enabledPlugin.ID:  enabledPlugin,
		disabledPlugin.ID: disabledPlugin,
		activeTheme.ID:    activeTheme,
	}}
	service := NewService(store, t.TempDir())

	items, err := service.Navigation(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("Navigation returned error: %v", err)
	}
	if navigationContains(items, "enabled.plugin", "/about") || navigationContains(items, "enabled.plugin", "/settings") {
		t.Fatalf("generated and non-menu pages should not inject sidebar navigation: %#v", items)
	}
	if !navigationContains(items, "enabled.plugin", "/dashboard") {
		t.Fatalf("expected enabled plugin menu page, got %#v", items)
	}
	if !navigationContains(items, "active.theme", "/theme") {
		t.Fatalf("expected active theme explicit menu page, got %#v", items)
	}
	if navigationContains(items, "disabled.plugin", "/hidden") {
		t.Fatalf("disabled plugin should not inject sidebar navigation: %#v", items)
	}
}

func TestServicePublicActiveThemeSettingsOmitsSecrets(t *testing.T) {
	theme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	theme.Status = StatusEnabled
	theme.PackageDigest = strings.Repeat("a", 64)
	theme.Manifest.Settings = []ManifestSetting{
		{Key: "home.notice.zh-CN", Label: LocalizedText{Default: "Notice"}, Type: "text", Default: "默认提示"},
		{Key: "home.right_rail.enabled", Label: LocalizedText{Default: "Rail"}, Type: "boolean", Default: "true"},
		{Key: "secret.token", Label: LocalizedText{Default: "Secret"}, Type: "secret", Default: ""},
	}
	store := &fakeExtensionStore{
		items: map[string]Extension{
			DefaultThemeID: theme,
		},
		activeThemeID: DefaultThemeID,
		settings: map[string]map[string]string{
			DefaultThemeID: {
				"home.notice.zh-CN": "自定义提示",
				"secret.token":      "should-not-leak",
			},
		},
	}
	service := NewService(store, t.TempDir())

	got, err := service.PublicActiveThemeSettings(context.Background())
	if err != nil {
		t.Fatalf("PublicActiveThemeSettings: %v", err)
	}
	if got.ThemeID != DefaultThemeID {
		t.Fatalf("themeId=%q", got.ThemeID)
	}
	if got.Version != theme.Version || got.PackageDigest != theme.PackageDigest {
		t.Fatalf("theme identity version=%q digest=%q", got.Version, got.PackageDigest)
	}
	if got.Settings["home.notice.zh-CN"] != "自定义提示" {
		t.Fatalf("expected stored notice, got %#v", got.Settings)
	}
	if got.Settings["home.right_rail.enabled"] != "true" {
		t.Fatalf("expected default boolean, got %#v", got.Settings)
	}
	if _, ok := got.Settings["secret.token"]; ok {
		t.Fatalf("secret must not appear in public settings: %#v", got.Settings)
	}
}

func TestServiceThemeSettingsRequireThemeManagePermission(t *testing.T) {
	theme := protectedBuiltinExtension(DefaultThemeID, TypeTheme)
	theme.Status = StatusEnabled
	theme.Manifest.Settings = []ManifestSetting{
		{Key: "home.notice.zh-CN", Label: LocalizedText{Default: "Notice"}, Type: "text", Default: "默认"},
	}
	store := &fakeExtensionStore{
		items: map[string]Extension{DefaultThemeID: theme},
	}
	service := NewService(store, t.TempDir())

	themeActor := identity.Actor{
		ID:     2,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionThemeManage: true,
		},
	}
	if _, err := service.Settings(context.Background(), themeActor, DefaultThemeID, "zh-CN"); err != nil {
		t.Fatalf("theme manager should read settings: %v", err)
	}

	pluginOnly := identity.Actor{
		ID:     3,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}
	if _, err := service.Settings(context.Background(), pluginOnly, DefaultThemeID, "zh-CN"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("plugin manager must not manage theme settings, got %v", err)
	}
}

func TestServiceSettingsResolveUpdateAndResetDefaults(t *testing.T) {
	item := installedExtension("settings.plugin", TypePlugin, ManifestBackend{})
	// 设置读写仅对已启用扩展开放。
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "demo.enabled", Label: LocalizedText{Default: "Enabled"}, Type: "boolean", Default: "true"},
		{Key: "demo.title", Label: LocalizedText{Default: "Title"}, Type: "select", Default: "Hello", RecommendedValue: "Hello", Placeholder: LocalizedText{Default: "Choose"}, Group: LocalizedText{Default: "general"}, Options: []ManifestSettingOption{{Value: "Hello", Label: LocalizedText{Default: "Hello"}}, {Value: "World", Label: LocalizedText{Default: "World"}}}},
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewService(store, t.TempDir())

	settings, err := service.Settings(context.Background(), extensionManager(), item.ID, "zh-CN")
	if err != nil {
		t.Fatalf("Settings returned error: %v", err)
	}
	if settingValue(settings, "demo.title") != "Hello" {
		t.Fatalf("expected default setting value, got %#v", settings)
	}
	if settings.Items[1].RecommendedValue != "Hello" || settings.Items[1].Placeholder != "Choose" || settings.Items[1].Group != "general" || len(settings.Items[1].Options) != 2 {
		t.Fatalf("expected presentation metadata, got %#v", settings.Items[1])
	}

	updated, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{Values: map[string]string{"demo.title": "Updated"}}, "zh-CN")
	if err != nil {
		t.Fatalf("UpdateSettings returned error: %v", err)
	}
	if settingValue(updated, "demo.title") != "Updated" {
		t.Fatalf("expected updated setting value, got %#v", updated)
	}

	_, err = service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{Values: map[string]string{"unknown": "bad"}}, "zh-CN")
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid setting key, got %v", err)
	}

	reset, err := service.ResetSettings(context.Background(), extensionManager(), item.ID, "zh-CN")
	if err != nil {
		t.Fatalf("ResetSettings returned error: %v", err)
	}
	if settingValue(reset, "demo.title") != "Hello" {
		t.Fatalf("expected default after reset, got %#v", reset)
	}
}

func TestUpdateSettingsPreservesOmittedValues(t *testing.T) {
	item := installedExtension("partial.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "a", Label: LocalizedText{Default: "A"}, Type: "text", Default: "da"},
		{Key: "b", Label: LocalizedText{Default: "B"}, Type: "text", Default: "db"},
		{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"},
	}
	store := &fakeExtensionStore{
		items: map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{
			item.ID: {"a": "keep-a", "b": "keep-b", "token": "secret-value"},
		},
	}
	service := NewService(store, t.TempDir())

	// 只更新 b；a 与 token 必须保留。
	updated, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"b": "new-b"},
	}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if settingValue(updated, "b") != "new-b" {
		t.Fatalf("b=%q", settingValue(updated, "b"))
	}
	if store.settings[item.ID]["a"] != "keep-a" || store.settings[item.ID]["b"] != "new-b" {
		t.Fatalf("store=%#v", store.settings[item.ID])
	}
	if store.settings[item.ID]["token"] != "secret-value" {
		t.Fatalf("secret lost: %#v", store.settings[item.ID])
	}
	// 掩码
	if settingValue(updated, "token") != "" {
		t.Fatal("secret must stay masked in response")
	}
}

func TestUpdateSettingsInvalidKeyChangesNothing(t *testing.T) {
	item := installedExtension("bad.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "a", Label: LocalizedText{Default: "A"}, Type: "text", Default: "da"},
	}
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"a": "keep"}},
	}
	service := NewService(store, t.TempDir())
	_, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"a": "x", "unknown": "y"},
	}, "zh-CN")
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("got %v", err)
	}
	if store.settings[item.ID]["a"] != "keep" {
		t.Fatalf("store mutated: %#v", store.settings[item.ID])
	}
}

func TestServiceSettingsAllowConfigurationWhenExtensionDisabled(t *testing.T) {
	item := installedExtension("settings.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusDisabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "demo.title", Label: LocalizedText{Default: "Title"}, Type: "text", Default: "Hello"},
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewService(store, t.TempDir())
	actor := extensionManager()

	if _, err := service.Settings(context.Background(), actor, item.ID, "zh-CN"); err != nil {
		t.Fatalf("expected disabled settings read, got %v", err)
	}
	if _, err := service.UpdateSettings(context.Background(), actor, item.ID, UpdateSettingsInput{Values: map[string]string{"demo.title": "x"}}, "zh-CN"); err != nil {
		t.Fatalf("expected disabled settings update, got %v", err)
	}
	if _, err := service.ResetSettings(context.Background(), actor, item.ID, "zh-CN"); err != nil {
		t.Fatalf("expected disabled settings reset, got %v", err)
	}
}

func TestUpdateSettingsRestoresSnapshotWhenPluginRestartFails(t *testing.T) {
	item := installedExtension("settings-restart.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{{Key: "name", Label: LocalizedText{Default: "Name"}, Type: "text"}}
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"name": "before"}},
	}
	runtime := &fakeRuntimeManager{startErr: errors.New("start failed")}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if err == nil {
		t.Fatal("restart failure must be returned")
	}
	if got := store.settings[item.ID]["name"]; got != "before" {
		t.Fatalf("previous settings were not restored: %q", got)
	}
}

func TestResetSettingsRestoresSnapshotWhenPluginRestartFails(t *testing.T) {
	item := installedExtension("settings-reset.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{{Key: "name", Label: LocalizedText{Default: "Name"}, Type: "text"}}
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"name": "keep"}},
	}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{startErr: errors.New("start failed")})

	_, err := service.ResetSettings(context.Background(), extensionManager(), item.ID, "zh-CN")
	if err == nil {
		t.Fatal("restart failure must be returned")
	}
	if got := store.settings[item.ID]["name"]; got != "keep" {
		t.Fatalf("reset lost the previous settings: %q", got)
	}
}

func TestSettingsRollbackFailureReturnsDiagnosticError(t *testing.T) {
	item := installedExtension("settings-rollback.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{{Key: "name", Label: LocalizedText{Default: "Name"}, Type: "text"}}
	store := &fakeExtensionStore{
		items:        map[string]Extension{item.ID: item},
		settings:     map[string]map[string]string{item.ID: {"name": "before"}},
		replaceErrAt: 2,
		replaceErr:   errors.New("database unavailable"),
	}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{startErr: errors.New("start failed")})

	_, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"name": "after"},
	}, "zh-CN")
	if !errors.Is(err, ErrSettingsRollbackFailed) {
		t.Fatalf("expected diagnostic rollback error, got %v", err)
	}
}

func TestServiceEnableRunsPluginPreflightBeforeStatusChange(t *testing.T) {
	expected := errors.New("rpc handshake failed")
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": withInstalledPackage(t, installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})),
	}}
	service := NewServiceWithHooks(store, t.TempDir(), fakeRuntime{err: expected})

	_, err := service.Enable(context.Background(), extensionManager(), "demo.plugin", EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrPreflightFailed) {
		t.Fatalf("expected preflight failure, got %v", err)
	}
	if store.enabledID != "" {
		t.Fatalf("extension status changed despite failed preflight: %q", store.enabledID)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventEnableFailed || last.Message == "" {
		t.Fatalf("expected enable failure event, got %#v", store.events)
	}

	service = NewServiceWithHooks(store, t.TempDir(), fakeRuntime{})
	enabled, err := service.Enable(context.Background(), extensionManager(), "demo.plugin", EnableInput{ConfirmCapabilities: true})
	if err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if enabled.Status != StatusEnabled || store.enabledID != "demo.plugin" {
		t.Fatalf("expected enabled plugin, got enabled=%#v store=%q", enabled, store.enabledID)
	}
}

func TestServiceEnableRejectsMissingInstalledPackage(t *testing.T) {
	// 生产 fail-closed：制品路径不存在时在 store 写入前返回 ErrArtifactMissing。
	missing := uploadedExtension("ghost.plugin", TypePlugin)
	missing.Manifest.Backend = ManifestBackend{}
	missing.PackagePath = filepath.Join(t.TempDir(), "ghost.plugin", "1.0.0", "package.zip")
	store := &fakeExtensionStore{items: map[string]Extension{
		missing.ID: missing,
	}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	_, err := service.Enable(context.Background(), extensionManager(), missing.ID, EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("expected missing artifact failure, got %v", err)
	}
	if store.enabledID != "" {
		t.Fatalf("missing package should not enable extension, got %q", store.enabledID)
	}
	// 缺失制品在启用入口直接 fail-closed，尚未进入 preflight/event 记录路径。
	if len(store.events) != 0 {
		t.Fatalf("missing artifact must not record enable events, got %#v", store.events)
	}
}
func TestServiceEnableRejectsTamperedDigestBackedPackage(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(t *testing.T, snapshotRoot string)
	}{
		{
			name: "changed file bytes",
			tamper: func(t *testing.T, snapshotRoot string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(snapshotRoot, "README.md"), []byte("tampered"), 0o644); err != nil {
					t.Fatalf("tamper snapshot file: %v", err)
				}
			},
		},
		{
			name: "snapshot root replaced by symlink",
			tamper: func(t *testing.T, snapshotRoot string) {
				t.Helper()
				realRoot := snapshotRoot + ".real"
				if err := os.Rename(snapshotRoot, realRoot); err != nil {
					t.Fatalf("move snapshot root: %v", err)
				}
				if err := os.Symlink(realRoot, snapshotRoot); err != nil {
					t.Fatalf("replace snapshot root with symlink: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeExtensionStore{}
			runtime := &fakeRuntimeManager{}
			service := NewServiceWithRuntime(store, t.TempDir(), runtime)
			installed, err := service.InstallArchive(context.Background(), extensionManager(), ArchiveInput{
				FileName: "digest.zip",
				Data: extensionArchive(t, validManifest("digest.plugin", TypePlugin),
					zipFile{name: "README.md", body: "approved"},
				),
			})
			if err != nil {
				t.Fatalf("install digest-backed package: %v", err)
			}
			test.tamper(t, installed.PackagePath)

			_, err = service.Enable(context.Background(), extensionManager(), installed.ID, EnableInput{ConfirmCapabilities: true})
			if !errors.Is(err, ErrPreflightFailed) {
				t.Fatalf("expected tampered package preflight failure, got %v", err)
			}
			if store.enabledID != "" || len(runtime.started) != 0 {
				t.Fatalf("tampered package reached runtime: enabled=%q started=%#v", store.enabledID, runtime.started)
			}
		})
	}
}

func TestServiceEnableStartsRuntimeAndRollsBackOnStartFailure(t *testing.T) {
	expected := errors.New("bind failed")
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": withInstalledPackage(t, installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})),
	}}
	runtime := &fakeRuntimeManager{startErr: expected}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	_, err := service.Enable(context.Background(), extensionManager(), "demo.plugin", EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrRuntimeFailed) {
		t.Fatalf("expected runtime failure, got %v", err)
	}
	if store.enabledID != "demo.plugin" || store.disabledID != "demo.plugin" {
		t.Fatalf("expected enable then rollback disable, enabled=%q disabled=%q", store.enabledID, store.disabledID)
	}
	if len(runtime.started) != 1 || runtime.started[0] != "demo.plugin" {
		t.Fatalf("expected runtime start attempt, got %#v", runtime.started)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventEnableFailed || last.Message == "" {
		t.Fatalf("expected enable failure event, got %#v", store.events)
	}
}

func TestServiceDisableStopsRuntimeAndListDecoratesRuntimeStatus(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}),
	}}
	store.items["demo.plugin"] = extensionWithStatus(store.items["demo.plugin"], StatusEnabled)
	runtime := &fakeRuntimeManager{statuses: map[string]RuntimeStatus{
		"demo.plugin": {State: RuntimeRunning, RouteCount: 1, HookCount: 1, ProviderCount: 1},
	}}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	items, err := service.List(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if items[0].Runtime == nil || items[0].Runtime.State != RuntimeRunning {
		t.Fatalf("expected decorated runtime status, got %#v", items[0].Runtime)
	}

	_, err = service.Disable(context.Background(), extensionManager(), "demo.plugin")
	if err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if len(runtime.stopped) != 1 || runtime.stopped[0] != "demo.plugin" {
		t.Fatalf("expected runtime stop, got %#v", runtime.stopped)
	}
}

func TestServiceListMarksMissingArtifactAndEnableFailsClosed(t *testing.T) {
	item := installedExtension("missing.plugin", TypePlugin, ManifestBackend{})
	item.PackagePath = filepath.Join(t.TempDir(), "missing")
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), nil)

	items, err := service.List(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ArtifactState != ArtifactMissing {
		t.Fatalf("artifact state: %#v", items)
	}

	if _, err := service.Enable(context.Background(), extensionManager(), item.ID, EnableInput{}); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("expected missing artifact error, got %v", err)
	}
}

func TestServiceListDecoratesPluginMemoryBytes(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": extensionWithStatus(
			installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}),
			StatusEnabled,
		),
		"other.plugin": extensionWithStatus(
			installedExtension("other.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}),
			StatusEnabled,
		),
	}}
	runtime := &fakeRuntimeManager{statuses: map[string]RuntimeStatus{
		"demo.plugin":  {State: RuntimeRunning, RouteCount: 1},
		"other.plugin": {State: RuntimeRunning},
	}}
	service := NewServiceWithOptions(store, t.TempDir(), "", runtime, WithPluginMemorySampler(func() map[string]uint64 {
		return map[string]uint64{"demo.plugin": 18 * 1024 * 1024}
	}))

	items, err := service.List(context.Background(), extensionManager())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var demo, other *Extension
	for i := range items {
		switch items[i].ID {
		case "demo.plugin":
			demo = &items[i]
		case "other.plugin":
			other = &items[i]
		}
	}
	if demo == nil || demo.Runtime == nil || demo.Runtime.MemoryBytes != 18*1024*1024 {
		t.Fatalf("demo memory: %#v", demo)
	}
	if other == nil || other.Runtime == nil || other.Runtime.MemoryBytes != 0 {
		t.Fatalf("other should omit memory, got %#v", other.Runtime)
	}

	detail, err := service.Detail(context.Background(), extensionManager(), "demo.plugin")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if detail.Runtime == nil || detail.Runtime.MemoryBytes != 18*1024*1024 {
		t.Fatalf("detail memory: %#v", detail.Runtime)
	}
}

func TestServiceDetailLoadsOnlyExactExtensionAndDecoratesRuntimeStatus(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": extensionWithStatus(
			installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}),
			StatusEnabled,
		),
		"other.plugin": installedExtension("other.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"}),
	}}
	runtime := &fakeRuntimeManager{statuses: map[string]RuntimeStatus{
		"demo.plugin": {State: RuntimeRunning, RouteCount: 2},
	}}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	item, err := service.Detail(context.Background(), extensionManager(), " DEMO.PLUGIN ")
	if err != nil {
		t.Fatalf("Detail returned error: %v", err)
	}
	if item.ID != "demo.plugin" || item.Runtime == nil || item.Runtime.State != RuntimeRunning || item.Runtime.RouteCount != 2 {
		t.Fatalf("unexpected exact extension detail: %#v", item)
	}
	if store.listCalls.Load() != 0 || store.getCalls.Load() != 1 {
		t.Fatalf("detail materialized the catalog: list=%d get=%d", store.listCalls.Load(), store.getCalls.Load())
	}

	_, err = service.Detail(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, "demo.plugin")
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceAdminPageBootstrapResolvesPagesAndSettingsByDeclaredView(t *testing.T) {
	item := installedExtension("bootstrap.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusEnabled
	item.Manifest.Admin = ManifestAdmin{
		Pages: []ManifestAdminPage{
			// 任意声明路径，不依赖 /settings 字面量。
			{Path: "/ops/config", Label: "Ops Config", View: "settings", Icon: "i-lucide-sliders", Order: 20},
			{Path: "/ops/dashboard", Label: "Ops Dashboard", View: "about", Order: 10, Menu: true},
		},
	}
	item.Manifest.Settings = []ManifestSetting{
		{
			Key: "demo.title",
			Label: LocalizedText{
				Default:  "Title",
				ByLocale: map[string]string{"zh-CN": "标题", "en-US": "Title EN"},
			},
			Type:    "text",
			Default: "Hello",
		},
		{Key: "demo.token", Label: LocalizedText{Default: "Token"}, Type: "secret", Default: ""},
	}
	store := &fakeExtensionStore{
		items: map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{
			item.ID: {"demo.title": "Stored", "demo.token": "super-secret"},
		},
	}
	runtime := &fakeRuntimeManager{statuses: map[string]RuntimeStatus{
		item.ID: {State: RuntimeRunning, RouteCount: 1},
	}}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)
	manager := extensionManager()

	settingsBootstrap, err := service.AdminPageBootstrap(context.Background(), manager, item.ID, "ops/config", "zh-CN")
	if err != nil {
		t.Fatalf("settings path bootstrap: %v", err)
	}
	if store.getCalls.Load() != 1 {
		t.Fatalf("expected single store.Get, got %d", store.getCalls.Load())
	}
	if settingsBootstrap.Extension.ID != item.ID || settingsBootstrap.Extension.Runtime == nil || settingsBootstrap.Extension.Runtime.State != RuntimeRunning {
		t.Fatalf("expected decorated extension, got %#v", settingsBootstrap.Extension)
	}
	if settingsBootstrap.Page == nil || settingsBootstrap.Page.Path != "/ops/config" || settingsBootstrap.Page.View != "settings" || settingsBootstrap.Page.Icon != "i-lucide-sliders" {
		t.Fatalf("expected normalized settings page, got %#v", settingsBootstrap.Page)
	}
	if settingsBootstrap.Settings == nil {
		t.Fatal("settings page must include settings payload")
	}
	if settingsBootstrap.Settings.ExtensionID != item.ID || settingValue(*settingsBootstrap.Settings, "demo.title") != "Stored" {
		t.Fatalf("unexpected settings values: %#v", settingsBootstrap.Settings)
	}
	if settingsBootstrap.Settings.Items[0].Label != "标题" {
		t.Fatalf("expected zh-CN localized label, got %q", settingsBootstrap.Settings.Items[0].Label)
	}
	if settingValue(*settingsBootstrap.Settings, "demo.token") != "" || !settingsBootstrap.Settings.Items[1].SecretSet {
		t.Fatalf("secret must be masked with secretSet: %#v", settingsBootstrap.Settings.Items[1])
	}
	if store.listSettingsCalls.Load() != 1 {
		t.Fatalf("settings page must read settings once, got %d", store.listSettingsCalls.Load())
	}

	// 再次调用：about 与未知 path 不加载 settings。
	aboutBootstrap, err := service.AdminPageBootstrap(context.Background(), manager, item.ID, "/about", "zh-CN")
	if err != nil {
		t.Fatalf("about bootstrap: %v", err)
	}
	if aboutBootstrap.Page == nil || aboutBootstrap.Page.Path != "/about" || aboutBootstrap.Page.View != "about" {
		t.Fatalf("expected host /about page, got %#v", aboutBootstrap.Page)
	}
	if aboutBootstrap.Settings != nil {
		t.Fatalf("about must not load settings: %#v", aboutBootstrap.Settings)
	}

	unknownBootstrap, err := service.AdminPageBootstrap(context.Background(), manager, item.ID, "/missing", "zh-CN")
	if err != nil {
		t.Fatalf("unknown path must not be an error: %v", err)
	}
	if unknownBootstrap.Page != nil || unknownBootstrap.Settings != nil {
		t.Fatalf("unknown path should return null page/settings: page=%#v settings=%#v", unknownBootstrap.Page, unknownBootstrap.Settings)
	}
	if unknownBootstrap.Extension.ID != item.ID {
		t.Fatalf("unknown path still returns extension: %#v", unknownBootstrap.Extension)
	}
	if store.listSettingsCalls.Load() != 1 {
		t.Fatalf("about/unknown pages must not read settings, got %d calls", store.listSettingsCalls.Load())
	}

	// 仅 extension.view：about 可读，settings 页拒绝。
	viewer := identity.Actor{
		ID:     8,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionView: true,
		},
	}
	viewAbout, err := service.AdminPageBootstrap(context.Background(), viewer, item.ID, "/about", "zh-CN")
	if err != nil {
		t.Fatalf("viewer about must be allowed: %v", err)
	}
	if viewAbout.Page == nil || viewAbout.Page.View != "about" || viewAbout.Settings != nil {
		t.Fatalf("viewer about payload unexpected: %#v", viewAbout)
	}
	_, err = service.AdminPageBootstrap(context.Background(), viewer, item.ID, "/ops/config", "zh-CN")
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer settings path must deny, got %v", err)
	}
	if store.listSettingsCalls.Load() != 1 {
		t.Fatalf("denied settings page must not read settings, got %d calls", store.listSettingsCalls.Load())
	}

	// 路径声明 view=about 时即便路径含 settings 字样也不加载设置。
	item.Manifest.Admin.Pages = append(item.Manifest.Admin.Pages, ManifestAdminPage{
		Path: "/legacy-settings-name", Label: "Legacy", View: "about",
	})
	store.items[item.ID] = item
	namedAbout, err := service.AdminPageBootstrap(context.Background(), manager, item.ID, "/legacy-settings-name", "zh-CN")
	if err != nil {
		t.Fatalf("path-name about: %v", err)
	}
	if namedAbout.Page == nil || namedAbout.Page.View != "about" || namedAbout.Settings != nil {
		t.Fatalf("must not infer settings from path text: %#v", namedAbout)
	}
}

func TestServiceAdminPageBootstrapAllowsMatchingSettingsManagersWithoutView(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		permission string
		mail       bool
	}{
		{name: "plugin manager", kind: TypePlugin, permission: identity.PermissionExtensionPluginManage},
		{name: "theme manager", kind: TypeTheme, permission: identity.PermissionExtensionThemeManage},
		{name: "mail manager", kind: TypePlugin, permission: identity.PermissionSettingsMailManage, mail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := installedExtension("bootstrap."+strings.ReplaceAll(tt.name, " ", "-"), tt.kind, ManifestBackend{})
			item.Manifest.Admin = ManifestAdmin{Pages: []ManifestAdminPage{
				{Path: "/settings", Label: "Settings", View: "settings"},
				{Path: "/about", Label: "About", View: "about"},
			}}
			item.Manifest.Settings = []ManifestSetting{{Key: "demo.enabled", Label: LocalizedText{Default: "Enabled"}, Type: "boolean", Default: "true"}}
			if tt.mail {
				item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
			}
			store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
			service := NewService(store, t.TempDir())
			actor := identity.Actor{
				ID: 42, Status: identity.UserStatusActive,
				Permissions: map[string]bool{tt.permission: true},
			}

			bootstrap, err := service.AdminPageBootstrap(context.Background(), actor, item.ID, "/settings", "zh-CN")
			if err != nil {
				t.Fatalf("matching settings manager denied: %v", err)
			}
			if bootstrap.Settings == nil || bootstrap.Page == nil || bootstrap.Page.View != "settings" {
				t.Fatalf("unexpected settings bootstrap: %#v", bootstrap)
			}
			if store.getCalls.Load() != 1 || store.listSettingsCalls.Load() != 1 {
				t.Fatalf("settings bootstrap calls: get=%d settings=%d", store.getCalls.Load(), store.listSettingsCalls.Load())
			}

			_, err = service.AdminPageBootstrap(context.Background(), actor, item.ID, "/about", "zh-CN")
			if !errors.Is(err, identity.ErrPermissionDenied) {
				t.Fatalf("manage-only actor must not read metadata page, got %v", err)
			}
			if store.listSettingsCalls.Load() != 1 {
				t.Fatalf("metadata denial must not read settings, got %d", store.listSettingsCalls.Load())
			}
		})
	}
}

func TestServiceEmitsPluginLifecycleHooks(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": withInstalledPackage(t, installedExtension("demo.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})),
	}}
	runtime := &fakeRuntimeManager{}
	service := NewServiceWithRuntime(store, t.TempDir(), runtime)

	if _, err := service.Enable(context.Background(), extensionManager(), "demo.plugin", EnableInput{ConfirmCapabilities: true}); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if _, err := service.Disable(context.Background(), extensionManager(), "demo.plugin"); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	expected := []string{"extension.enabled", "extension.disabled"}
	if !slices.Equal(runtime.hooks, expected) {
		t.Fatalf("expected lifecycle hooks %#v, got %#v", expected, runtime.hooks)
	}
}

func TestServiceEnableRejectsThemesBecauseThemesUseActivation(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"starter.theme": installedExtension("starter.theme", TypeTheme, ManifestBackend{}),
	}}
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	_, err := service.Enable(context.Background(), extensionManager(), "starter.theme", EnableInput{ConfirmCapabilities: true})
	if !errors.Is(err, ErrThemeActivationRequired) {
		t.Fatalf("expected theme activation requirement, got %v", err)
	}
	if store.enabledID != "" {
		t.Fatalf("theme should not be enabled through plugin lifecycle, got %q", store.enabledID)
	}
}

func TestServiceVerifyThemeMissingPackageReturnsBuildFailed(t *testing.T) {
	missing := uploadedExtension("ghost.theme", TypeTheme)
	missing.PackagePath = filepath.Join(t.TempDir(), "ghost.theme", "1.0.0", "package.zip")
	store := &fakeExtensionStore{items: map[string]Extension{
		missing.ID: missing,
	}}
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	_, err := service.VerifyExtension(context.Background(), extensionManager(), missing.ID)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("expected missing theme package build failure, got %v", err)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventEnableFailed || last.Message == "" {
		t.Fatalf("expected verify failure event, got %#v", store.events)
	}
}

func TestServiceVerifyThemeMissingManifestReturnsBuildFailed(t *testing.T) {
	missing := uploadedExtension("manifestless.theme", TypeTheme)
	root := filepath.Join(t.TempDir(), missing.ID, missing.Version)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create package root: %v", err)
	}
	missing.PackagePath = filepath.Join(root, "package.zip")
	if err := os.WriteFile(missing.PackagePath, []byte("zip"), 0o600); err != nil {
		t.Fatalf("write package archive: %v", err)
	}
	store := &fakeExtensionStore{items: map[string]Extension{
		missing.ID: missing,
	}}
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	_, err := service.VerifyExtension(context.Background(), extensionManager(), missing.ID)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("expected missing theme manifest build failure, got %v", err)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventEnableFailed || last.Message == "" {
		t.Fatalf("expected verify failure event, got %#v", store.events)
	}
}

func TestServiceVerifyThemeMissingManifestLayerReturnsBuildFailed(t *testing.T) {
	// 无 layer 且无 theme.json/assets 时仍应失败。
	theme := withInstalledPackage(t, installedExtension("layerless.theme", TypeTheme, ManifestBackend{}))
	// 上传包：PackagePath 是 package.zip；manifest 在同级目录。
	root := filepath.Dir(theme.PackagePath)
	if err := writeManifest(root, theme.Manifest); err != nil {
		t.Fatalf("rewrite uploaded manifest: %v", err)
	}
	_ = os.Remove(filepath.Join(root, "theme.json"))
	_ = os.RemoveAll(filepath.Join(root, "assets"))
	_ = os.RemoveAll(filepath.Join(root, "files"))
	store := &fakeExtensionStore{items: map[string]Extension{
		theme.ID: theme,
	}}
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	_, err := service.VerifyExtension(context.Background(), extensionManager(), theme.ID)
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("expected missing theme package build failure, got %v", err)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventEnableFailed || last.Message == "" {
		t.Fatalf("expected verify failure event, got %#v", store.events)
	}
}

func TestServiceActivateThemeActivatesUploadedThemeImmediately(t *testing.T) {
	// Runtime Page Registry：上传主题同步激活，不触发 Nuxt 构建。
	store := newFakeExtensionStore(map[string]Extension{
		"starter.theme": withInstalledPackage(t, installedExtension("starter.theme", TypeTheme, ManifestBackend{})),
	})
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{})

	active, err := service.ActivateTheme(context.Background(), extensionManager(), "starter.theme")
	if err != nil {
		t.Fatalf("ActivateTheme returned error: %v", err)
	}
	if active.Status != StatusEnabled || store.activeThemeID != "starter.theme" {
		t.Fatalf("expected uploaded theme active immediately, got active=%#v activeID=%q", active, store.activeThemeID)
	}
}

func TestServiceActivateThemeRejectsStalePreviewArtifact(t *testing.T) {
	item := withInstalledPackage(t, installedExtension("preview.theme", TypeTheme, ManifestBackend{}))
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{})

	_, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), item.ID, ThemeActivationInput{
		Version: item.Version, PackageDigest: "stale-digest",
	})
	if !errors.Is(err, ErrThemePreviewStale) || store.activeThemeID != "" {
		t.Fatalf("err=%v active=%q", err, store.activeThemeID)
	}
}

func TestServiceActivateThemeRejectsChangedCurrentThemeTuple(t *testing.T) {
	current := withInstalledPackage(t, installedExtension("current.theme", TypeTheme, ManifestBackend{}))
	current.Status = StatusEnabled
	target := withInstalledPackage(t, installedExtension("target.theme", TypeTheme, ManifestBackend{}))
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{})

	_, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), target.ID, ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: "changed-digest",
	})
	if !errors.Is(err, ErrThemePreviewStale) || store.activeThemeID != current.ID {
		t.Fatalf("err=%v active=%q", err, store.activeThemeID)
	}
}

func TestServiceRequiresExplicitSuperAdminApprovalForCoreThemeReplacements(t *testing.T) {
	target := exactThemeRuntimeExtensionFixture(t, "approval.theme", "/approval")
	target.Status = StatusInstalled
	store := newFakeExtensionStore(map[string]Extension{target.ID: target})
	registry := &themeActivationApprovalRegistry{}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(registry))

	withoutApproval := ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
	}
	if _, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), target.ID, withoutApproval); err != nil {
		t.Fatal(err)
	}
	if registry.ordinary != 1 || registry.approved != 0 {
		t.Fatalf("ordinary=%d approved=%d", registry.ordinary, registry.approved)
	}

	withApproval := ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: target.ID, CurrentThemeVersion: target.Version, CurrentThemeDigest: target.PackageDigest,
		ApproveCoreReplacements: true,
	}
	if _, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), target.ID, withApproval); err != nil {
		t.Fatal(err)
	}
	if registry.ordinary != 1 || registry.approved != 1 || registry.approvedBy != extensionManager().ID {
		t.Fatalf("ordinary=%d approved=%d approvedBy=%d", registry.ordinary, registry.approved, registry.approvedBy)
	}

	themeManager := identity.Actor{
		ID: 84, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionExtensionThemeManage: true},
	}
	if _, err := service.ActivateThemeFromPreview(context.Background(), themeManager, target.ID, withApproval); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non-super-admin approval error = %v", err)
	}
}

func TestServiceRegistryFailureRestoresNoActiveThemeDatabaseState(t *testing.T) {
	target := exactThemeRuntimeExtensionFixture(t, "first.theme", "/first")
	target.Status = StatusInstalled
	store := newFakeExtensionStore(map[string]Extension{target.ID: target})
	injected := errors.New("registry publication failed")
	registry := &themeActivationApprovalRegistry{err: injected}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(registry))

	_, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), target.ID, ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
	})
	if !errors.Is(err, ErrBuildFailed) || store.activeThemeID != "" || store.items[target.ID].Status != StatusDisabled {
		t.Fatalf("error=%v active=%q target=%#v", err, store.activeThemeID, store.items[target.ID])
	}
}

func TestServiceSameThemePublicationFailureAppendsPriorApprovalCompensation(t *testing.T) {
	target := exactThemeRuntimeExtensionFixture(t, "same.theme", "/same")
	target.Status = StatusEnabled
	store := newFakeExtensionStore(map[string]Extension{target.ID: target})
	store.activeThemeID = target.ID
	store.themeApprovalBy = map[string]int64{target.ID: 42}
	registry := &themeActivationApprovalRegistry{err: errors.New("runtime publication failed")}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(registry))
	actor := extensionManager()
	actor.ID = 99

	_, err := service.ActivateThemeFromPreview(context.Background(), actor, target.ID, ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: target.ID, CurrentThemeVersion: target.Version, CurrentThemeDigest: target.PackageDigest,
		ApproveCoreReplacements: true,
	})
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("activation error = %v", err)
	}
	publication := store.latestThemePublication
	if publication.Revision != 2 || publication.Reason != ThemeRuntimePublicationCompensation ||
		publication.ThemeID != target.ID || !publication.CoreReplacementsApproved || publication.ActorUserID != 42 ||
		!publication.SourceCoreReplacementsApproved || publication.SourceActorUserID != 99 {
		t.Fatalf("same-theme compensation = %#v", publication)
	}
}

func TestServiceActivatesOnlyExactPreviewedStagedThemeArtifact(t *testing.T) {
	current := exactThemeRuntimeExtensionFixture(t, "staged.theme", "/current")
	current.Status = StatusEnabled
	staged := exactThemeRuntimeExtensionFixture(t, "staged.theme", "/candidate")
	staged.Version = "2.0.0"
	staged.Manifest.Version = staged.Version
	current.StagedVersion = &ExtensionVersion{
		ID: 2, Version: staged.Version, Manifest: staged.Manifest,
		PackageDigest: staged.PackageDigest, PackagePath: staged.PackagePath,
	}
	store := newFakeExtensionStore(map[string]Extension{current.ID: current})
	store.activeThemeID = current.ID
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{})

	result, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), current.ID, ThemeActivationInput{
		Version: staged.Version, PackageDigest: staged.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: current.PackageDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != staged.Version || result.PackageDigest != staged.PackageDigest || result.StagedVersion != nil {
		t.Fatalf("staged activation=%#v", result)
	}
}

type themeActivationApprovalRegistry struct {
	ordinary     int
	approved     int
	hostApproved int
	approvedBy   int64
	err          error
}

func (*themeActivationApprovalRegistry) PreflightThemePackage(context.Context, Extension, string) error {
	return nil
}
func (*themeActivationApprovalRegistry) RegisterThemePackage(context.Context, Extension) error {
	return nil
}
func (*themeActivationApprovalRegistry) RegisterThemePackageRestoring(context.Context, Extension, []string) error {
	return nil
}
func (*themeActivationApprovalRegistry) RegisterDefaultThemeFallback(context.Context, Extension) error {
	return nil
}
func (r *themeActivationApprovalRegistry) RegisterThemePackageReplacing(context.Context, Extension, string) error {
	r.ordinary++
	return r.err
}
func (r *themeActivationApprovalRegistry) RegisterThemePackageReplacingApproved(_ context.Context, _ Extension, _ string, approvedBy int64) error {
	r.approved++
	r.approvedBy = approvedBy
	return r.err
}
func (r *themeActivationApprovalRegistry) RegisterThemePackageReplacingHostApproved(context.Context, Extension, string) error {
	r.hostApproved++
	return r.err
}
func (*themeActivationApprovalRegistry) RegisterPluginPackage(context.Context, Extension) error {
	return nil
}
func (*themeActivationApprovalRegistry) ClearExtension(string) {}

func TestServiceSerializesConcurrentThemeCommitAndPublication(t *testing.T) {
	exactTheme := func(id string) Extension {
		item := withInstalledPackage(t, installedExtension(id, TypeTheme, ManifestBackend{}))
		item.PackagePath = filepath.Dir(item.PackagePath)
		digest, err := extensionpackage.DigestTree(item.PackagePath)
		if err != nil {
			t.Fatal(err)
		}
		item.PackageDigest = digest
		return item
	}
	current := exactTheme("current.theme")
	current.Status = StatusEnabled
	left := exactTheme("left.theme")
	right := exactTheme("right.theme")
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, left.ID: left, right.ID: right})
	store.activeThemeID = current.ID
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{})
	input := func(target Extension) ThemeActivationInput {
		return ThemeActivationInput{
			Version: target.Version, PackageDigest: target.PackageDigest,
			CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: current.PackageDigest,
		}
	}
	start := make(chan struct{})
	type activationResult struct {
		id  string
		err error
	}
	results := make(chan activationResult, 2)
	for _, target := range []Extension{left, right} {
		target := target
		go func() {
			<-start
			_, err := service.ActivateThemeFromPreview(context.Background(), extensionManager(), target.ID, input(target))
			results <- activationResult{id: target.ID, err: err}
		}()
	}
	close(start)
	var succeeded, stale int
	for range 2 {
		result := <-results
		err := result.err
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrThemePreviewStale):
			stale++
		default:
			t.Fatalf("unexpected activation error for %s: %v", result.id, err)
		}
	}
	if succeeded != 1 || stale != 1 || store.activeThemeID == current.ID {
		t.Fatalf("succeeded=%d stale=%d active=%q", succeeded, stale, store.activeThemeID)
	}
}

func TestServiceActivateThemeRestoresBuiltinDefaultThemeImmediately(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		DefaultThemeID: withInstalledPackage(t, protectedBuiltinExtension(DefaultThemeID, TypeTheme)),
	}}
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	active, err := service.ActivateTheme(context.Background(), extensionManager(), DefaultThemeID)
	if err != nil {
		t.Fatalf("ActivateTheme returned error: %v", err)
	}
	if active.Status != StatusEnabled || store.activeThemeID != DefaultThemeID {
		t.Fatalf("expected default theme active, got active=%#v activeID=%q", active, store.activeThemeID)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventThemeActivated {
		t.Fatalf("expected theme activated event, got %#v", store.events)
	}
}

func TestServiceEnsureDefaultThemeActiveRepairsUnsafeThemeState(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		DefaultThemeID: protectedBuiltinExtension(DefaultThemeID, TypeTheme),
		"starter.theme": {
			ID:      "starter.theme",
			Name:    "Starter Theme",
			Version: "1.0.0",
			Type:    TypeTheme,
			Status:  StatusEnabled,
			Source:  SourceUploaded,
			Manifest: Manifest{
				ID:            "starter.theme",
				Name:          "Starter Theme",
				Version:       "1.0.0",
				Type:          TypeTheme,
				SForumVersion: ">=0.1.0",
			},
			InstalledAt: time.Now(),
			UpdatedAt:   time.Now(),
		},
	}}
	store.activeThemeID = "starter.theme"
	service := NewServiceWithHooks(store, t.TempDir(), nil)

	active, err := service.EnsureDefaultThemeActive(context.Background())
	if err != nil {
		t.Fatalf("EnsureDefaultThemeActive returned error: %v", err)
	}
	if active.ID != DefaultThemeID || store.activeThemeID != DefaultThemeID {
		t.Fatalf("expected unsafe active theme repaired to default, got active=%#v activeID=%q", active, store.activeThemeID)
	}

	active, err = service.EnsureDefaultThemeActive(context.Background())
	if err != nil {
		t.Fatalf("EnsureDefaultThemeActive second call returned error: %v", err)
	}
	if active.ID != DefaultThemeID || store.activeThemeID != DefaultThemeID {
		t.Fatalf("expected idempotent default active theme, got active=%#v activeID=%q", active, store.activeThemeID)
	}
}

func TestServiceDisableRequiresPermissionAndRecordsEvent(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{
		"demo.plugin": installedExtension("demo.plugin", TypePlugin, ManifestBackend{}),
	}}
	service := NewService(store, t.TempDir())

	_, err := service.Disable(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, "demo.plugin")
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}

	disabled, err := service.Disable(context.Background(), extensionManager(), "demo.plugin")
	if err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if disabled.Status != StatusDisabled || store.disabledID != "demo.plugin" {
		t.Fatalf("expected disabled extension, got disabled=%#v store=%q", disabled, store.disabledID)
	}
	if last := store.events[len(store.events)-1]; last.Action != EventDisabled {
		t.Fatalf("expected disabled event, got %#v", store.events)
	}
}

// extensionManager 成功路径默认用 super_admin：含后端入口的上传包仅其可装/启。
func extensionManager() identity.Actor {
	return identity.Actor{
		ID:       42,
		Status:   identity.UserStatusActive,
		RoleKeys: []string{identity.RoleSuperAdmin},
	}
}

// techAdminPluginManager 模拟 tech_admin：有 plugin.manage，但非 super_admin。
func techAdminPluginManager() identity.Actor {
	return identity.Actor{
		ID:     43,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}
}

func validManifest(id string, extensionType string) string {
	return `{
		"manifestVersion": 3,
		"id": "` + id + `",
		"name": "Demo Extension",
		"description": "Demo extension for SForum tests.",
		"url": "https://example.com/demo-extension",
		"author": {"name": "SForum Team", "url": "https://example.com", "email": "dev@example.com"},
		"version": "1.0.0",
		"type": "` + extensionType + `",
		"sforumVersion": ">=0.1.0",
		"permissions": ["topic.create"],
		"migrations": [{
			"id": "` + id + `.migration.init",
			"contractVersion": "` + id + `.migration.init@1",
			"path": "migrations/001_init.sql",
			"digest": "17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a",
			"transaction": "required"
		}],
		"backend": {
			"entry": "backend/plugin",
			"rpc": "hashicorp-go-plugin",
			"protocolVersion": 2,
			"hostApiVersion": "sforum.host@2",
			"digest": "a8076d3d28d21e02012b20eaf7dbf75409a6277134439025f282e368e3305abf"
		},
		"routes": [{
			"id": "` + id + `.route.hello",
			"contractVersion": "` + id + `.route.hello@1",
			"action": "add",
			"path": "/hello",
			"methods": ["GET"],
			"guard": "core.guard.public",
			"fallback": "closed",
			"mode": "http",
			"handler": "demo.hello",
			"responseSchema": "` + id + `.route.hello.response@1"
		}],
		"hooks": [{
			"id": "` + id + `.hook.topic-created",
			"contractVersion": "` + id + `.hook.topic-created@1",
			"name": "topic.created",
			"kind": "observe",
			"handler": "demo.topic-created",
			"inputSchema": "` + id + `.hook.topic-created.input@1",
			"execution": "sync",
			"failurePolicy": "fail_open",
			"timeoutMs": 1000
		}],
		"jobs": [{
			"id": "` + id + `.job.sync",
			"contractVersion": "` + id + `.job.sync@1",
			"name": "demo.sync",
			"handler": "demo.sync",
			"payloadSchema": "` + id + `.job.sync.payload@1",
			"retryPolicy": "none",
			"maxAttempts": 1,
			"concurrencyLimit": 1
		}],
		"packageFiles": [
			{"id": "` + id + `.file.backend", "kind": "executable", "path": "backend/plugin", "digest": "a8076d3d28d21e02012b20eaf7dbf75409a6277134439025f282e368e3305abf"},
			{"id": "` + id + `.file.migration", "kind": "migration", "path": "migrations/001_init.sql", "digest": "17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a"}
		]
	}`
}

func validThemeManifest(id string) string {
	return `{
		"manifestVersion": 3,
		"id": "` + id + `",
		"name": "Demo Theme",
		"description": "Demo theme for SForum tests.",
		"url": "https://example.com/demo-theme",
		"author": {"name": "SForum Team", "url": "https://example.com", "email": "dev@example.com"},
		"version": "1.0.0",
		"type": "theme",
		"sforumVersion": ">=0.1.0"
	}`
}

type zipFile struct {
	name string
	body string
	mode os.FileMode
}

func extensionArchive(t *testing.T, manifest string, files ...zipFile) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if manifest != "" {
		writeZipFile(t, writer, zipFile{name: ManifestFileName, body: manifest})
	}
	present := make(map[string]bool, len(files))
	for _, file := range files {
		present[file.name] = true
	}
	if strings.Contains(manifest, `"path": "backend/plugin"`) && !present["backend/plugin"] {
		writeZipFile(t, writer, zipFile{name: "backend/plugin", body: "#!/bin/sh\n", mode: 0o755})
	}
	if strings.Contains(manifest, `"path": "migrations/001_init.sql"`) && !present["migrations/001_init.sql"] {
		writeZipFile(t, writer, zipFile{name: "migrations/001_init.sql", body: "SELECT 1;", mode: 0o644})
	}
	for _, file := range files {
		writeZipFile(t, writer, file)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

func writeZipFile(t *testing.T, writer *zip.Writer, file zipFile) {
	t.Helper()
	header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
	if file.mode != 0 {
		header.SetMode(file.mode)
	}
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create zip file %s: %v", file.name, err)
	}
	if _, err := io.WriteString(entry, file.body); err != nil {
		t.Fatalf("write zip file %s: %v", file.name, err)
	}
}

func withInstalledPackage(t *testing.T, item Extension) Extension {
	t.Helper()
	root := filepath.Join(t.TempDir(), item.ID, item.Version)
	if item.Source == SourceBuiltin {
		item.PackagePath = root
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create builtin package root: %v", err)
		}
		if err := writeManifest(root, item.Manifest); err != nil {
			t.Fatalf("write builtin manifest: %v", err)
		}
		writeTestExecutablePackageFiles(t, root, item)
		writeTestThemeContract(t, root, item)
		return item
	}

	item.Source = SourceUploaded
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create uploaded package root: %v", err)
	}
	item.PackagePath = filepath.Join(root, "package.zip")
	if err := os.WriteFile(item.PackagePath, []byte("zip"), 0o600); err != nil {
		t.Fatalf("write uploaded package archive: %v", err)
	}
	if err := writeManifest(root, item.Manifest); err != nil {
		t.Fatalf("write uploaded manifest: %v", err)
	}
	writeTestExecutablePackageFiles(t, root, item)
	writeTestThemeContract(t, root, item)
	return item
}

func writeTestExecutablePackageFiles(t *testing.T, root string, item Extension) {
	t.Helper()
	if item.Manifest.Backend.Entry == "" {
		return
	}
	target := filepath.Join(root, filepath.FromSlash(item.Manifest.Backend.Entry))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create backend directory: %v", err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write backend fixture: %v", err)
	}
}

func writeTestThemeContract(t *testing.T, root string, item Extension) {
	t.Helper()
	if item.Type != TypeTheme {
		return
	}
	if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"schemaVersion":1,"styles":{"tokens":{}}}`), 0o644); err != nil {
		t.Fatalf("write theme contract: %v", err)
	}
}

func installedExtension(id string, extensionType string, backend ManifestBackend) Extension {
	packageFiles := []ManifestPackageFile(nil)
	if backend.Entry != "" {
		backend.RPC = "hashicorp-go-plugin"
		backend.ProtocolVersion = 2
		backend.HostAPIVersion = "sforum.host@2"
		backend.Digest = "a8076d3d28d21e02012b20eaf7dbf75409a6277134439025f282e368e3305abf"
		packageFiles = []ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: backend.Entry, Digest: backend.Digest,
		}}
	}
	return Extension{
		ID:      id,
		Name:    "Demo Extension",
		Version: "1.0.0",
		Type:    extensionType,
		Status:  StatusInstalled,
		Manifest: Manifest{
			ManifestVersion: 3,
			ID:              id,
			Name:            "Demo Extension",
			Description:     "Demo extension for SForum tests.",
			URL:             "https://example.com/demo-extension",
			Author:          ManifestAuthor{Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com"},
			Version:         "1.0.0",
			Type:            extensionType,
			SForumVersion:   ">=0.1.0",
			Backend:         backend,
			PackageFiles:    packageFiles,
		},
		// 通用夹具表示制品存在；缺失制品用例会显式覆盖为不存在的路径。
		PackagePath: os.TempDir(),
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func protectedBuiltinExtension(id string, extensionType string) Extension {
	item := installedExtension(id, extensionType, ManifestBackend{})
	item.Status = StatusEnabled
	item.Source = SourceBuiltin
	item.IsSystem = true
	item.IsDeletable = false
	return item
}

func uploadedExtension(id string, extensionType string) Extension {
	item := installedExtension(id, extensionType, ManifestBackend{})
	item.Source = SourceUploaded
	item.IsSystem = false
	item.IsDeletable = true
	return item
}

func contributionTestPlugin(id string, status string, contributions []ManifestContribution) Extension {
	name := "Demo Plugin"
	if id == "alpha.plugin" {
		name = "Alpha Plugin"
	}
	if id == "beta.plugin" {
		name = "Beta Plugin"
	}
	return Extension{
		ID:      id,
		Name:    name,
		Version: "1.0.0",
		Type:    TypePlugin,
		Status:  status,
		Manifest: Manifest{
			ID:            id,
			Name:          name,
			Description:   "Contribution test plugin.",
			URL:           "https://example.com/" + id,
			Author:        ManifestAuthor{Name: "SForum Team"},
			Version:       "1.0.0",
			Type:          TypePlugin,
			SForumVersion: ">=0.1.0",
			Contributions: contributions,
		},
		// 通用夹具表示制品存在；缺失制品用例会显式覆盖为不存在的路径。
		PackagePath: os.TempDir(),
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func topicActionContribution(t *testing.T, id string, order int, routePath string) ManifestContribution {
	t.Helper()
	payload, err := json.Marshal(TopicActionContributionPayload{
		Type:   "extensionRoute",
		Method: "POST",
		Path:   routePath,
	})
	if err != nil {
		t.Fatalf("marshal topic action payload: %v", err)
	}
	return ManifestContribution{
		Point:   "forum.topic.actions",
		ID:      id,
		Order:   order,
		Label:   map[string]string{"zh-CN": "动作", "en-US": "Action"},
		Icon:    "i-lucide-bookmark",
		Payload: payload,
	}
}

func contributionIDs(items []EffectiveContribution) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ExtensionID+":"+item.ID)
	}
	return ids
}

func extensionWithStatus(item Extension, status string) Extension {
	item.Status = status
	return item
}

func navigationContains(items []ExtensionAdminNavigationItem, extensionID string, pagePath string) bool {
	for _, item := range items {
		if item.ExtensionID == extensionID && item.Path == pagePath {
			return true
		}
	}
	return false
}

func settingValue(settings ExtensionSettings, key string) string {
	for _, item := range settings.Items {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}

type fakeRuntime struct {
	err error
}

func (r fakeRuntime) Check(context.Context, Extension) error {
	return r.err
}

type fakeRuntimeManager struct {
	err      error
	startErr error
	started  []string
	stopped  []string
	hooks    []string
	statuses map[string]RuntimeStatus
}

func (r *fakeRuntimeManager) Check(context.Context, Extension) error {
	return r.err
}

func (r *fakeRuntimeManager) Start(_ context.Context, extension Extension) error {
	r.started = append(r.started, extension.ID)
	return r.startErr
}

func (r *fakeRuntimeManager) Stop(_ context.Context, extension Extension) error {
	r.stopped = append(r.stopped, extension.ID)
	return nil
}

func (r *fakeRuntimeManager) Status(_ context.Context, extension Extension) RuntimeStatus {
	if r.statuses != nil {
		if status, ok := r.statuses[extension.ID]; ok {
			return status
		}
	}
	return RuntimeStatus{State: RuntimeStopped}
}

func (r *fakeRuntimeManager) EmitHook(_ context.Context, name string, _ map[string]any) {
	r.hooks = append(r.hooks, name)
}

type fakeExtensionStore struct {
	items                    map[string]Extension
	listCalls                atomic.Int64
	getCalls                 atomic.Int64
	listSettingsCalls        atomic.Int64
	saved                    Extension
	nextVersionID            int64
	enabledID                string
	disabledID               string
	activeThemeID            string
	activateThemeExactCalls  int
	activateThemeExactErr    error
	afterActivateThemeExact  func()
	themePublicationRevision int64
	latestThemePublication   ThemeRuntimePublication
	themeApprovalBy          map[string]int64
	settings                 map[string]map[string]string
	replaceCalls             int
	replaceErrAt             int
	replaceErr               error
	beforeCAS                func()
	events                   []ExtensionEvent
	deliveries               []ExtensionEventDelivery
	// migrations 按 extension_id 存账本（F2.4）。
	migrations map[string][]MigrationRecord
}

func newFakeExtensionStore(items map[string]Extension) *fakeExtensionStore {
	return &fakeExtensionStore{items: items}
}

func (s *fakeExtensionStore) List(context.Context) ([]Extension, error) {
	s.listCalls.Add(1)
	items := make([]Extension, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items, nil
}

func (s *fakeExtensionStore) Get(_ context.Context, id string) (Extension, error) {
	s.getCalls.Add(1)
	if item, ok := s.items[id]; ok {
		return item, nil
	}
	return Extension{}, ErrExtensionNotFound
}

func (s *fakeExtensionStore) SaveInstalled(_ context.Context, input SaveInstalledInput) (Extension, error) {
	now := time.Now()
	if existing, ok := s.items[input.Manifest.ID]; ok {
		if existing.Type != input.Manifest.Type {
			return Extension{}, ErrInvalidManifest
		}
		if existing.Source == SourceBuiltin || existing.IsSystem {
			return Extension{}, ErrNotDeletable
		}
		if existing.Version == input.Manifest.Version && existing.PackageDigest == input.PackageDigest {
			// 与 PostgresStore.ensureExtensionVersion 同语义：摘要未变即同一件
			// 制品，只把 package_path 修到本次校验过的路径。
			existing.PackagePath = input.PackagePath
			s.items[existing.ID] = existing
			return existing, nil
		}
		var candidate *ExtensionVersion
		if existing.StagedVersion != nil && existing.StagedVersion.Version == input.Manifest.Version &&
			existing.StagedVersion.PackageDigest == input.PackageDigest {
			candidate = existing.StagedVersion
			candidate.PackagePath = input.PackagePath
		} else {
			s.nextVersionID++
			candidate = &ExtensionVersion{
				ID: s.nextVersionID, Version: input.Manifest.Version, Manifest: input.Manifest,
				PackageDigest: input.PackageDigest, AdminFrontendDigest: input.AdminFrontendDigest,
				PackagePath: input.PackagePath, InstalledAt: now,
			}
		}
		existing.StagedVersion = candidate
		existing.UpdatedAt = now
		s.items[existing.ID] = existing
		s.saved = Extension{
			ID: input.Manifest.ID, Name: input.Manifest.Name, Version: input.Manifest.Version,
			Type: input.Manifest.Type, Status: StatusInstalled, Source: SourceUploaded,
			IsDeletable: true, Manifest: input.Manifest, PackageDigest: input.PackageDigest,
			AdminFrontendDigest: input.AdminFrontendDigest, PackagePath: input.PackagePath,
			InstalledAt: now, UpdatedAt: now,
		}
		return existing, nil
	}
	s.nextVersionID++
	item := Extension{
		ID:                  input.Manifest.ID,
		Name:                input.Manifest.Name,
		Version:             input.Manifest.Version,
		Type:                input.Manifest.Type,
		Status:              StatusInstalled,
		Source:              SourceUploaded,
		IsDeletable:         true,
		Manifest:            input.Manifest,
		PackageDigest:       input.PackageDigest,
		AdminFrontendDigest: input.AdminFrontendDigest,
		PackagePath:         input.PackagePath,
		ActiveVersionID:     s.nextVersionID,
		InstalledAt:         now,
		UpdatedAt:           now,
	}
	s.saved = item
	if s.items == nil {
		s.items = map[string]Extension{}
	}
	s.items[item.ID] = item
	return item, nil
}

func (s *fakeExtensionStore) PromoteStagedVersion(_ context.Context, input StagedVersionCASInput) (Extension, error) {
	if err := validateStagedVersionCASInput(input); err != nil {
		return Extension{}, err
	}
	item, ok := s.items[input.ExtensionID]
	if !ok {
		return Extension{}, ErrExtensionNotFound
	}
	if item.StagedVersion == nil {
		return Extension{}, ErrStagedVersionNotFound
	}
	staged := item.StagedVersion
	if staged.ID != input.ExpectedStagedVersionID || staged.PackageDigest != input.ExpectedPackageDigest {
		return Extension{}, ErrStagedVersionConflict
	}
	item.Version = staged.Version
	item.Manifest = staged.Manifest
	item.PackageDigest = staged.PackageDigest
	item.AdminFrontendDigest = staged.AdminFrontendDigest
	item.PackagePath = staged.PackagePath
	item.ActiveVersionID = staged.ID
	item.InstalledAt = staged.InstalledAt
	item.StagedVersion = nil
	item.UpdatedAt = time.Now()
	s.items[item.ID] = item
	return item, nil
}

func (s *fakeExtensionStore) DiscardStagedVersion(_ context.Context, input StagedVersionCASInput) (Extension, error) {
	if err := validateStagedVersionCASInput(input); err != nil {
		return Extension{}, err
	}
	item, ok := s.items[input.ExtensionID]
	if !ok {
		return Extension{}, ErrExtensionNotFound
	}
	if item.StagedVersion == nil {
		return Extension{}, ErrStagedVersionNotFound
	}
	if item.StagedVersion.ID != input.ExpectedStagedVersionID || item.StagedVersion.PackageDigest != input.ExpectedPackageDigest {
		return Extension{}, ErrStagedVersionConflict
	}
	item.StagedVersion = nil
	item.UpdatedAt = time.Now()
	s.items[item.ID] = item
	return item, nil
}

func (s *fakeExtensionStore) SaveBuiltin(_ context.Context, input SaveBuiltinInput) (Extension, error) {
	if existing, ok := s.items[input.Manifest.ID]; ok && existing.Source != SourceBuiltin {
		return Extension{}, ErrNotDeletable
	}
	item := Extension{
		ID:                  input.Manifest.ID,
		Name:                input.Manifest.Name,
		Version:             input.Manifest.Version,
		Type:                input.Manifest.Type,
		Status:              builtinInitialStatus(input.Manifest),
		Source:              SourceBuiltin,
		IsSystem:            true,
		IsDeletable:         false,
		Manifest:            input.Manifest,
		PackageDigest:       input.PackageDigest,
		AdminFrontendDigest: input.AdminFrontendDigest,
		PackagePath:         input.PackagePath,
		InstalledAt:         time.Now(),
		UpdatedAt:           time.Now(),
	}
	if s.items == nil {
		s.items = map[string]Extension{}
	}
	s.items[item.ID] = item
	return item, nil
}

func (s *fakeExtensionStore) PruneMissingBuiltins(_ context.Context, activeIDs []string) (BuiltinPruneResult, error) {
	active := map[string]bool{}
	for _, id := range activeIDs {
		active[id] = true
	}
	result := BuiltinPruneResult{}
	for id, item := range s.items {
		if item.Source == SourceBuiltin && !active[id] {
			if item.Type == TypePlugin {
				result.DisabledPluginIDs = append(result.DisabledPluginIDs, id)
			}
			delete(s.items, id)
		}
	}
	slices.Sort(result.DisabledPluginIDs)
	return result, nil
}

func (s *fakeExtensionStore) Enable(_ context.Context, id string, extensionType string) (Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return Extension{}, ErrExtensionNotFound
	}
	item.Status = StatusEnabled
	item.UpdatedAt = time.Now()
	s.items[id] = item
	s.enabledID = id
	if extensionType == TypeTheme {
		s.activeThemeID = id
	}
	return item, nil
}

func (s *fakeExtensionStore) ActivateTheme(ctx context.Context, id string) (ThemeActivationResult, error) {
	current, _ := s.ActiveTheme(ctx)
	item, err := s.setActiveTheme(id)
	if err != nil {
		return ThemeActivationResult{}, err
	}
	publication := s.publishThemeRuntime(ThemeRuntimePublication{
		DesiredState: ThemeRuntimePublicationActive,
		ThemeID:      item.ID, ThemeVersion: item.Version, PackageDigest: item.PackageDigest,
		SourceThemeID: current.ID, SourceThemeVersion: current.Version, SourcePackageDigest: current.PackageDigest,
		SourceCoreReplacementsApproved: s.themeApprovalBy[current.ID] > 0,
		SourceActorUserID:              s.themeApprovalBy[current.ID],
		Reason:                         ThemeRuntimePublicationStartupRepair,
	})
	return ThemeActivationResult{Extension: item, Publication: publication}, nil
}

func (s *fakeExtensionStore) setActiveTheme(id string) (Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return Extension{}, ErrExtensionNotFound
	}
	for currentID, current := range s.items {
		if current.Type == TypeTheme && current.ID != id && current.Status == StatusEnabled {
			current.Status = StatusDisabled
			current.UpdatedAt = time.Now()
			s.items[currentID] = current
		}
	}
	item.Status = StatusEnabled
	item.UpdatedAt = time.Now()
	s.items[id] = item
	s.activeThemeID = id
	return item, nil
}

func (s *fakeExtensionStore) ActivateThemeExact(ctx context.Context, id string, expected ThemeActivationInput) (ThemeActivationResult, error) {
	s.activateThemeExactCalls++
	if s.activateThemeExactErr != nil {
		return ThemeActivationResult{}, s.activateThemeExactErr
	}
	target, ok := s.items[id]
	if !ok {
		return ThemeActivationResult{}, ErrExtensionNotFound
	}
	current, currentErr := s.ActiveTheme(ctx)
	if errors.Is(currentErr, ErrExtensionNotFound) {
		current = Extension{}
	} else if currentErr != nil {
		return ThemeActivationResult{}, currentErr
	}
	activationTarget := target
	if staged, hasStaged := target.StagedArtifact(); hasStaged {
		activationTarget = staged
	}
	if activationTarget.Version != expected.Version || !strings.EqualFold(activationTarget.PackageDigest, expected.PackageDigest) ||
		current.ID != expected.CurrentThemeID || current.Version != expected.CurrentThemeVersion ||
		!strings.EqualFold(current.PackageDigest, expected.CurrentThemeDigest) {
		return ThemeActivationResult{}, ErrThemePreviewStale
	}
	activationTarget.StagedVersion = nil
	s.items[id] = activationTarget
	item, err := s.setActiveTheme(id)
	if err != nil {
		return ThemeActivationResult{}, err
	}
	publication := s.publishThemeRuntime(ThemeRuntimePublication{
		DesiredState: ThemeRuntimePublicationActive,
		ThemeID:      item.ID, ThemeVersion: item.Version, PackageDigest: item.PackageDigest,
		SourceThemeID: current.ID, SourceThemeVersion: current.Version, SourcePackageDigest: current.PackageDigest,
		SourceCoreReplacementsApproved: s.themeApprovalBy[current.ID] > 0,
		SourceActorUserID:              s.themeApprovalBy[current.ID],
		CoreReplacementsApproved:       expected.ApproveCoreReplacements,
		ActorUserID:                    expected.ActorUserID, Reason: ThemeRuntimePublicationActivation,
	})
	if s.afterActivateThemeExact != nil {
		after := s.afterActivateThemeExact
		s.afterActivateThemeExact = nil
		after()
	}
	return ThemeActivationResult{Extension: item, Publication: publication}, nil
}

func (s *fakeExtensionStore) CompensateThemeActivation(_ context.Context, failed ThemeRuntimePublication, previous *Extension) (ThemeActivationResult, error) {
	if !sameThemeRuntimePublication(s.latestThemePublication, failed) {
		return ThemeActivationResult{}, ErrThemePublicationConflict
	}
	publication := ThemeRuntimePublication{
		SourceThemeID: failed.ThemeID, SourceThemeVersion: failed.ThemeVersion, SourcePackageDigest: failed.PackageDigest,
		SourceCoreReplacementsApproved: failed.CoreReplacementsApproved,
		SourceActorUserID:              failed.ActorUserID,
		ActorUserID:                    failed.ActorUserID, Reason: ThemeRuntimePublicationCompensation,
	}
	result := ThemeActivationResult{}
	if previous == nil {
		item := s.items[failed.ThemeID]
		item.Status = StatusDisabled
		s.items[failed.ThemeID] = item
		s.activeThemeID = ""
		publication.DesiredState = ThemeRuntimePublicationNone
	} else {
		item, err := s.setActiveTheme(previous.ID)
		if err != nil {
			return ThemeActivationResult{}, err
		}
		result.Extension = item
		publication.DesiredState = ThemeRuntimePublicationActive
		publication.ThemeID, publication.ThemeVersion, publication.PackageDigest = item.ID, item.Version, item.PackageDigest
		publication.CoreReplacementsApproved = failed.SourceCoreReplacementsApproved
		if publication.CoreReplacementsApproved {
			publication.ActorUserID = failed.SourceActorUserID
		}
	}
	result.Publication = s.publishThemeRuntime(publication)
	return result, nil
}

func (s *fakeExtensionStore) publishThemeRuntime(publication ThemeRuntimePublication) ThemeRuntimePublication {
	s.themePublicationRevision++
	publication.Revision = s.themePublicationRevision
	publication.CreatedAt = time.Now().UTC()
	s.latestThemePublication = publication
	return publication
}

func (s *fakeExtensionStore) ActiveTheme(context.Context) (Extension, error) {
	if s.activeThemeID != "" {
		if item, ok := s.items[s.activeThemeID]; ok {
			return item, nil
		}
	}
	for _, item := range s.items {
		if item.Type == TypeTheme && item.Status == StatusEnabled {
			return item, nil
		}
	}
	return Extension{}, ErrExtensionNotFound
}

func (s *fakeExtensionStore) Disable(_ context.Context, id string) (Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return Extension{}, ErrExtensionNotFound
	}
	item.Status = StatusDisabled
	item.UpdatedAt = time.Now()
	s.items[id] = item
	s.disabledID = id
	if s.activeThemeID == id {
		s.activeThemeID = ""
	}
	return item, nil
}

func (s *fakeExtensionStore) CreateEvent(_ context.Context, input EventInput) (ExtensionEvent, error) {
	event := ExtensionEvent{
		ID:          int64(len(s.events) + 1),
		ExtensionID: input.ExtensionID,
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		Message:     input.Message,
		CreatedAt:   time.Now(),
	}
	s.events = append(s.events, event)
	return event, nil
}

func (s *fakeExtensionStore) ListEvents(context.Context, string, int) ([]ExtensionEvent, error) {
	return s.events, nil
}

func (s *fakeExtensionStore) ListSettings(_ context.Context, extensionID string) (map[string]string, error) {
	s.listSettingsCalls.Add(1)
	if s.settings == nil {
		return map[string]string{}, nil
	}
	values := map[string]string{}
	for key, value := range s.settings[extensionID] {
		values[key] = value
	}
	return values, nil
}

func (s *fakeExtensionStore) ReplaceSettings(_ context.Context, extensionID string, values map[string]string) error {
	s.replaceCalls++
	if s.replaceErrAt == s.replaceCalls {
		if s.replaceErr != nil {
			return s.replaceErr
		}
		return errors.New("replace settings failed")
	}
	if s.settings == nil {
		s.settings = map[string]map[string]string{}
	}
	next := map[string]string{}
	for key, value := range values {
		next[key] = value
	}
	s.settings[extensionID] = next
	return nil
}

func (s *fakeExtensionStore) CompareAndSwapSetting(_ context.Context, extensionID, name, oldValue, newValue string) (bool, error) {
	if s.beforeCAS != nil {
		before := s.beforeCAS
		s.beforeCAS = nil
		before()
	}
	if s.settings == nil || s.settings[extensionID][name] != oldValue {
		return false, nil
	}
	s.settings[extensionID][name] = newValue
	return true, nil
}

func (s *fakeExtensionStore) ResetSettings(_ context.Context, extensionID string) error {
	if s.settings != nil {
		delete(s.settings, extensionID)
	}
	return nil
}

func (s *fakeExtensionStore) Delete(_ context.Context, id string) error {
	if _, ok := s.items[id]; !ok {
		return ErrExtensionNotFound
	}
	delete(s.items, id)
	if s.settings != nil {
		delete(s.settings, id)
	}
	if s.migrations != nil {
		delete(s.migrations, id)
	}
	return nil
}

func (s *fakeExtensionStore) ListMigrationLedger(_ context.Context, extensionID string) ([]MigrationRecord, error) {
	if s.migrations == nil {
		return []MigrationRecord{}, nil
	}
	items := s.migrations[extensionID]
	if items == nil {
		return []MigrationRecord{}, nil
	}
	out := make([]MigrationRecord, len(items))
	copy(out, items)
	return out, nil
}

func (s *fakeExtensionStore) RecordMigration(_ context.Context, extensionID string, record MigrationRecord) error {
	if s.migrations == nil {
		s.migrations = map[string][]MigrationRecord{}
	}
	if record.AppliedAt.IsZero() {
		record.AppliedAt = time.Now()
	}
	list := s.migrations[extensionID]
	for i := range list {
		if list[i].Path == record.Path {
			list[i] = record
			s.migrations[extensionID] = list
			return nil
		}
	}
	s.migrations[extensionID] = append(list, record)
	return nil
}

func (s *fakeExtensionStore) CreateEventDelivery(_ context.Context, input EventDeliveryInput) (ExtensionEventDelivery, error) {
	delivery := ExtensionEventDelivery{
		ID:            int64(len(s.deliveries) + 1),
		ExtensionID:   input.ExtensionID,
		EventName:     input.EventName,
		EventKind:     input.EventKind,
		Status:        input.Status,
		Reason:        input.Reason,
		Message:       input.Message,
		CorrelationID: input.CorrelationID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	s.deliveries = append(s.deliveries, delivery)
	return delivery, nil
}

func (s *fakeExtensionStore) UpdateEventDelivery(_ context.Context, input EventDeliveryUpdateInput) error {
	for index := range s.deliveries {
		if s.deliveries[index].ID == input.ID {
			s.deliveries[index].Status = input.Status
			s.deliveries[index].Reason = input.Reason
			s.deliveries[index].Message = input.Message
			s.deliveries[index].AttemptCount = input.AttemptCount
			s.deliveries[index].UpdatedAt = time.Now()
			if input.Completed {
				completedAt := time.Now()
				s.deliveries[index].CompletedAt = &completedAt
			}
			return nil
		}
	}
	return ErrExtensionNotFound
}

func (s *fakeExtensionStore) ListEventDeliveries(_ context.Context, input EventDeliveryListInput) ([]ExtensionEventDelivery, error) {
	items := []ExtensionEventDelivery{}
	for _, delivery := range s.deliveries {
		if input.ExtensionID != "" && delivery.ExtensionID != input.ExtensionID {
			continue
		}
		if input.EventName != "" && delivery.EventName != input.EventName {
			continue
		}
		if input.Status != "" && delivery.Status != input.Status {
			continue
		}
		items = append(items, delivery)
	}
	return items, nil
}
