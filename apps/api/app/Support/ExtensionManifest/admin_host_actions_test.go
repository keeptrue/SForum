package extensionmanifest

import (
	"errors"
	"strings"
	"testing"
)

// hostActionManifest builds a component page that declares host actions, with
// the package file the component contract requires.
func hostActionManifest(actions []string) Manifest {
	manifest := validBaseManifest()
	manifest.Admin = ManifestAdmin{
		Entry: "/catalog",
		Pages: []ManifestAdminPage{{
			Path:        "/catalog",
			Label:       "Catalog",
			View:        "component",
			Menu:        true,
			HostActions: actions,
			Component: &AdminComponent{
				ID: "catalog", APIVersion: 1,
				Entry: "frontend/admin/dist/catalog.mjs",
			},
		}},
	}
	manifest.PackageFiles = []ManifestPackageFile{{
		ID: "demo.plugin.file.catalog", Kind: "frontend",
		Path: "frontend/admin/dist/catalog.mjs", Digest: strings.Repeat("a", 64),
	}}
	return manifest
}

// TestAdminHostActionsVocabulary pins the wire ids and the disclosed order. The
// legacy id is a published contract: its value and meaning must not change when
// the vocabulary grows.
func TestAdminHostActionsVocabulary(t *testing.T) {
	if AdminHostActionExtensionInstall != "extension.install" {
		t.Fatalf("legacy host action id changed: %q", AdminHostActionExtensionInstall)
	}
	if AdminHostActionExtensionInstallEnable != "extension.install.enable" {
		t.Fatalf("install-and-enable host action id = %q", AdminHostActionExtensionInstallEnable)
	}
	want := []string{AdminHostActionExtensionInstall, AdminHostActionExtensionInstallEnable}
	actions := AdminHostActions()
	if len(actions) != len(want) {
		t.Fatalf("host action vocabulary = %#v, want %#v", actions, want)
	}
	for index := range want {
		if actions[index] != want[index] {
			t.Fatalf("host action %d = %q, want %q (stable order, legacy first)", index, actions[index], want[index])
		}
	}
	// 披露清单与校验入口必须同源：任何对外公布的 ID 都要能通过清单校验。
	for _, action := range actions {
		if !knownAdminHostAction(action) {
			t.Fatalf("disclosed host action %q is rejected by knownAdminHostAction", action)
		}
	}
	// 返回的是副本，调用方改写不能污染下一次披露。
	actions[0] = "mutated"
	if AdminHostActions()[0] != AdminHostActionExtensionInstall {
		t.Fatalf("AdminHostActions must return a fresh slice on every call")
	}
}

func TestAdminHostActionsNormalizeAndValidate(t *testing.T) {
	manifest := hostActionManifest([]string{" Extension.Install ", "extension.install", ""})
	if err := Validate(manifest); err != nil {
		t.Fatalf("component page host action should validate: %v", err)
	}
	page := EffectiveAdminPages(manifest)[0]
	if len(page.HostActions) != 1 || page.HostActions[0] != AdminHostActionExtensionInstall {
		t.Fatalf("host actions should be trimmed, lowercased and de-duplicated: %#v", page.HostActions)
	}
}

func TestAdminHostActionsDeclareBothDedupeAndKeepAuthorOrder(t *testing.T) {
	cases := []struct {
		name    string
		actions []string
		want    []string
	}{
		{
			name:    "legacy first",
			actions: []string{" Extension.Install ", "extension.install.enable", "extension.install"},
			want:    []string{AdminHostActionExtensionInstall, AdminHostActionExtensionInstallEnable},
		},
		{
			name:    "enable first",
			actions: []string{"EXTENSION.INSTALL.ENABLE", "extension.install", "Extension.Install.Enable"},
			want:    []string{AdminHostActionExtensionInstallEnable, AdminHostActionExtensionInstall},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := hostActionManifest(tc.actions)
			if err := Validate(manifest); err != nil {
				t.Fatalf("declaring both host actions should validate: %v", err)
			}
			got := EffectiveAdminPages(manifest)[0].HostActions
			if len(got) != len(tc.want) {
				t.Fatalf("host actions = %#v, want %#v (de-duplicated, author order kept)", got, tc.want)
			}
			for index := range tc.want {
				if got[index] != tc.want[index] {
					t.Fatalf("host actions = %#v, want %#v (de-duplicated, author order kept)", got, tc.want)
				}
			}
		})
	}
}

func TestAdminHostActionsRejectNonComponentPage(t *testing.T) {
	for _, action := range AdminHostActions() {
		manifest := hostActionManifest([]string{action})
		manifest.Admin.Pages[0].View = "about"
		manifest.Admin.Pages[0].Component = nil
		manifest.PackageFiles = nil
		if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("host action %q on a non-component page must be rejected, got %v", action, err)
		}
	}
}

func TestAdminHostActionsRejectUnknownAction(t *testing.T) {
	cases := [][]string{
		{"extensions.install"},
		{"extension.enable"},
		{"extension.install.enabled"},
		{AdminHostActionExtensionInstall, "extension.install.enabled"},
		{AdminHostActionExtensionInstallEnable, "extension.install.enable.v2"},
	}
	for _, actions := range cases {
		manifest := hostActionManifest(actions)
		if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("unknown host action in %#v must be rejected, got %v", actions, err)
		}
	}
}

func TestAdminHostActionsKeepOmittedForOrdinaryComponentPage(t *testing.T) {
	manifest := hostActionManifest(nil)
	if err := Validate(manifest); err != nil {
		t.Fatalf("a component page without host actions stays valid: %v", err)
	}
	if page := EffectiveAdminPages(manifest)[0]; page.HostActions != nil {
		t.Fatalf("omitted host actions must stay nil, got %#v", page.HostActions)
	}
}
