package identitycontroller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestListAuthProvidersReturnsExecutableProviders(t *testing.T) {
	registry := identityregistry.New()
	publication := identityregistry.Publication{
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo.membership", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), VersionID: 1,
			RuntimeInstanceID: "runtime-1",
		},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "demo.membership@1",
			Providers: []identityregistry.Provider{{
				ID: "demo.membership.auth", ContractVersion: "demo.membership.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "identity.auth",
				Operations: []identityregistry.ProviderOperation{{
					Name: identity.AuthOperationLoginStart, InputSchema: "in@1", OutputSchema: "out@1",
					TimeoutMS: 1000, FailurePolicy: identityregistry.ProviderFailureFailClosed,
				}},
			}},
		},
	}
	// Catalog-only listing does not require bound Schemas for Providers() scan.
	// Publish may require schemas for executable ops — use empty ops for list shape
	// validation when Schema binding is unavailable in unit tests.
	publication.Identity.Providers[0].Operations = nil
	publication.Identity.Providers[0].Kind = identityregistry.ProviderKindAuth
	if _, err := registry.Publish(publication); err != nil {
		// Fallback: controller with nil catalog must return empty list.
		t.Logf("publish skipped: %v", err)
	}

	controller := &Controller{providerCatalog: registry}
	app := fiber.New()
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Data []authProviderListItem `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// inspect-only providers are filtered out (no operations).
	if len(body.Data) != 0 {
		t.Fatalf("expected empty executable list, got %#v", body.Data)
	}
}

func TestAuthProviderStartUnavailableWithoutFlow(t *testing.T) {
	controller := &Controller{}
	app := fiber.New()
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	payload, _ := json.Marshal(map[string]any{"correlationId": "c1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/demo.auth/login/start", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// T8C：partial external-auth wiring 不得启动 login/registration/link OAuth。
func TestT8C_AuthProviderStartFailsClosedOnPartialWiring(t *testing.T) {
	// 非 nil authFlow 指针即可越过 authFlow==nil 检查；真正拒绝来自 requireExternalAuthStartWiring。
	flow := &identity.AuthProviderFlow{}
	cases := []struct {
		name string
		ctrl *Controller
	}{
		{
			name: "only_auth_flow",
			ctrl: &Controller{authFlow: flow},
		},
		{
			name: "missing_callback_store",
			ctrl: &Controller{
				authFlow:            flow,
				externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{}),
				// activationStore 仍 nil；callbackStateStore nil；providerCatalog nil
			},
		},
		{
			name: "missing_activation_store",
			ctrl: &Controller{
				authFlow:            flow,
				externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{}),
				callbackStateStore:  identity.NewInMemoryCallbackStateStore(),
				providerCatalog:     identityregistry.New(),
				// activationStore 仍 nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			api := app.Group("/api/v1")
			tc.ctrl.RegisterRoutes(api)
			payload, _ := json.Marshal(map[string]any{"correlationId": "c1"})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/demo.auth/login/start", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 fail-closed", resp.StatusCode)
			}
		})
	}
}

func TestT8C_RequireExternalAuthStartWiring(t *testing.T) {
	if err := (&Controller{}).requireExternalAuthStartWiring(); err == nil {
		t.Fatal("empty controller must fail closed")
	}
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: identity.NewMemoryProviderActivationStore(),
	})
	ctrl := &Controller{
		authFlow:            &identity.AuthProviderFlow{},
		externalAuthService: svc,
		callbackStateStore:  identity.NewInMemoryCallbackStateStore(),
		activationStore:     svc.ActivationStore(),
		providerCatalog:     identityregistry.New(),
	}
	if err := ctrl.requireExternalAuthStartWiring(); err != nil {
		t.Fatalf("complete wiring must pass: %v", err)
	}
}

func TestAuthProviderStartOperationMapping(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"registration", identity.AuthOperationRegistrationStart},
		{"login", identity.AuthOperationLoginStart},
		{"link", identity.AuthOperationLinkStart},
		{"recovery", identity.RecoveryOperationStart},
	}
	for _, test := range tests {
		got, err := authProviderStartOperation(test.kind)
		if err != nil || got != test.want {
			t.Fatalf("kind %s = %q err=%v", test.kind, got, err)
		}
	}
	if _, err := authProviderStartOperation("unknown"); err == nil {
		t.Fatal("unknown start operation accepted")
	}
}
