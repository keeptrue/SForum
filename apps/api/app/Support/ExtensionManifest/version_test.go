package extensionmanifest

import (
	"testing"
)

func TestManifestVersionRequiresV3(t *testing.T) {
	manifest := versionedTestManifest(ManifestVersionV3)
	if err := Validate(manifest); err != nil {
		t.Fatalf("Manifest V3 should validate: %v", err)
	}
	if got := EffectiveManifestVersion(manifest); got != ManifestVersionV3 {
		t.Fatalf("effective version = %d, want %d", got, ManifestVersionV3)
	}
	if got := ManifestContract(manifest); got != "sforum.manifest@3" {
		t.Fatalf("contract = %q", got)
	}
}

func TestManifestVersionRejectsEveryNonV3Value(t *testing.T) {
	for _, version := range []int{-1, 0, 1, 2, ManifestVersionLatest + 1} {
		if err := Validate(versionedTestManifest(version)); err == nil {
			t.Fatalf("manifest version %d must be rejected", version)
		}
	}
}

func versionedTestManifest(version int) Manifest {
	return Manifest{
		ManifestVersion: version,
		ID:              "version.test",
		Name:            "Version Test",
		Description:     "Manifest version compatibility fixture.",
		URL:             "https://example.com/version-test",
		Author:          ManifestAuthor{Name: "SForum"},
		Version:         "1.0.0",
		Type:            TypePlugin,
		SForumVersion:   ">=0.1.0",
	}
}
