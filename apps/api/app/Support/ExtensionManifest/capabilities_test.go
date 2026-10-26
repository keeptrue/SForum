package extensionmanifest

import (
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

func TestValidateRejectsUnknownCapability(t *testing.T) {
	manifest := validPluginManifest()
	manifest.Capabilities = []string{"evil.root"}
	if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid, got %v", err)
	}
}

func TestValidateAcceptsKnownCapabilities(t *testing.T) {
	manifest := validPluginManifest()
	manifest.Capabilities = []string{capabilities.NetOutbound, capabilities.HostAPI}
	if err := Validate(manifest); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
}

func TestThemeRejectsCapabilities(t *testing.T) {
	manifest := Manifest{
		ID:            "demo.theme",
		Name:          "Demo Theme",
		Description:   "Theme.",
		URL:           "https://example.com",
		Author:        ManifestAuthor{Name: "Demo"},
		Version:       "1.0.0",
		Type:          TypeTheme,
		SForumVersion: ">=0.1.0",
		Capabilities:  []string{capabilities.HostAPI},
	}
	if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected theme capabilities rejected, got %v", err)
	}
}

func TestResolvedCapabilitiesImpliesBackend(t *testing.T) {
	manifest := validPluginManifest()
	manifest.Backend.Entry = "backend/plugin"
	keys, implied := ResolvedCapabilities(manifest)
	set := capabilities.NewSet(keys)
	if !set.Has(capabilities.HostAPI) {
		t.Fatalf("expected host.api: %#v", keys)
	}
	if !implied[capabilities.HostAPI] {
		t.Fatal("host.api should be implied when not explicit")
	}
}

func validPluginManifest() Manifest {
	return Manifest{
		ManifestVersion: ManifestVersionV3,
		ID:              "demo.plugin",
		Name:            "Demo Plugin",
		Description:     "Demo plugin.",
		URL:             "https://example.com/demo",
		Author:          ManifestAuthor{Name: "Demo Studio", URL: "https://example.com"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
	}
}
