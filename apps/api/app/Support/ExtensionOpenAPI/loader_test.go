package extensionopenapi

import (
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestManifestsEqualNormalizesCallerAndPackageSnapshots(t *testing.T) {
	caller := extensionmanifest.Manifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		AdminSurfaces: []extensionmanifest.ManifestAdminSurface{{
			Kind: "list",
		}},
	}
	packageSnapshot := extensionmanifest.Normalize(caller)
	if packageSnapshot.AdminSurfaces[0].Operation != extensionmanifest.AdminSurfaceOperationQuery {
		t.Fatalf("normalized operation = %q", packageSnapshot.AdminSurfaces[0].Operation)
	}
	if !manifestsEqual(packageSnapshot, caller) || !manifestsEqual(caller, packageSnapshot) {
		t.Fatal("deterministic manifest defaults changed exact-artifact equality")
	}
}
