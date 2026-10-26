package extensionopenapi

import (
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestHostPolicyDerivesCORSAndRequestSize(t *testing.T) {
	httpRoute := routeContract{
		route: extensionmanifest.ManifestRoute{
			ID: "demo.route.http", Mode: extensionmanifest.RouteModeHTTP, Guard: "public",
		},
		method: "POST",
	}
	policy, err := hostPolicyForOperation(httpRoute, map[string]any{}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if policy.CORSPolicy != PolicyCORSSameOrigin {
		t.Fatalf("cors = %q", policy.CORSPolicy)
	}
	if policy.RequestSizeBytes != DefaultRequestSizeBytes {
		t.Fatalf("request size = %d", policy.RequestSizeBytes)
	}

	uploadRoute := routeContract{
		route: extensionmanifest.ManifestRoute{
			ID: "demo.route.upload", Mode: "multipart_upload", Guard: "authenticated",
		},
		method: "POST",
	}
	uploadPolicy, err := hostPolicyForOperation(uploadRoute, map[string]any{}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if uploadPolicy.RequestSizeBytes != UploadRequestSizeBytes {
		t.Fatalf("upload size = %d", uploadPolicy.RequestSizeBytes)
	}
}
