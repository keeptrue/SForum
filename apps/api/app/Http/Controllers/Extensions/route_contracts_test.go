package extensionscontroller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestRouteContractHTTPPublishesPermissionedExactSnapshotViews(t *testing.T) {
	digest := strings.Repeat("a", 64)
	aggregateRevision := "sha256:" + strings.Repeat("b", 64)
	catalog := routeContractTestCatalog{snapshot: extensionopenapi.PublishedContractSnapshot{
		Revision: 12, AggregateRevision: aggregateRevision,
		Artifacts: []extensionopenapi.PublishedRouteSchemaArtifact{{
			ExtensionID: "contract.demo", ExtensionVersion: "1.0.0", PackageDigest: digest,
		}},
		Sources: []extensionopenapi.SourceIdentity{{
			ExtensionID: "contract.demo", ExtensionVersion: "1.0.0", PackageDigest: digest,
			FragmentID: "contract.demo.openapi", ContractVersion: "contract.demo.openapi@1",
			Path: "openapi/routes.yaml", Digest: digest, Namespace: "contractDemo",
		}},
		Document: json.RawMessage(`{"openapi":"3.1.0","paths":{"/demo":{}}}`),
		GeneratedClientOperations: []extensionopenapi.GeneratedOperation{{
			OperationID: "contractDemo.create", RouteID: "contract.demo.create",
			ContractVersion: "contract.demo.create@1", Path: "/demo", Method: "POST",
			Action: "add", Mode: "http", Guard: "core.guard.public",
			RateLimit: "host.ip_write@1", RateLimitScope: "client_ip",
			Idempotency: "required.24h@1", IdempotencyRequired: true,
			IdempotencyHeader: "Idempotency-Key", IdempotencyKeyMaxLength: 128,
			IdempotencyTTLSeconds: 86400, Security: "public",
			ExtensionID: "contract.demo", ExtensionVersion: "1.0.0", PackageDigest: digest,
			FragmentID: "contract.demo.openapi", Namespace: "contractDemo",
		}},
	}}
	app := newRouteContractTestApp(t, &catalog)
	viewer := loginRouteContractUser(t, app, 1)
	legacyManager := loginRouteContractUser(t, app, 2)

	aggregate := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/openapi/aggregate", viewer)
	if aggregate.StatusCode != http.StatusOK || aggregate.Header.Get("ETag") != `"`+aggregateRevision+`"` ||
		aggregate.Header.Get("X-SForum-Route-Contract-Revision") != "12" {
		t.Fatalf("aggregate status=%d etag=%q revision=%q", aggregate.StatusCode, aggregate.Header.Get("ETag"), aggregate.Header.Get("X-SForum-Route-Contract-Revision"))
	}
	var aggregateEnvelope testEnvelope[routeOpenAPIAggregateView]
	if err := json.NewDecoder(aggregate.Body).Decode(&aggregateEnvelope); err != nil {
		t.Fatal(err)
	}
	aggregate.Body.Close()
	if aggregateEnvelope.Data.AggregateRevision != aggregateRevision || len(aggregateEnvelope.Data.Artifacts) != 1 ||
		len(aggregateEnvelope.Data.Sources) != 1 || !json.Valid(aggregateEnvelope.Data.Document) {
		t.Fatalf("aggregate view=%#v", aggregateEnvelope.Data)
	}

	generated := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/openapi/generated-client", legacyManager)
	if generated.StatusCode != http.StatusOK {
		t.Fatalf("generated status=%d body=%s", generated.StatusCode, responseBody(t, generated))
	}
	var generatedEnvelope testEnvelope[routeGeneratedClientView]
	if err := json.NewDecoder(generated.Body).Decode(&generatedEnvelope); err != nil {
		t.Fatal(err)
	}
	generated.Body.Close()
	if generatedEnvelope.Data.Revision != 12 || len(generatedEnvelope.Data.Operations) != 1 ||
		!generatedEnvelope.Data.Operations[0].IdempotencyRequired ||
		generatedEnvelope.Data.Operations[0].PackageDigest != digest {
		t.Fatalf("generated view=%#v", generatedEnvelope.Data)
	}
}

func TestRouteContractHTTPRequiresExtensionViewAndFailsClosedWithoutCatalog(t *testing.T) {
	app := newRouteContractTestApp(t, nil)
	viewer := loginRouteContractUser(t, app, 1)
	denied := loginRouteContractUser(t, app, 3)

	unauthenticated := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/openapi/aggregate", nil)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	forbidden := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/openapi/generated-client", denied)
	if forbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("forbidden status=%d", forbidden.StatusCode)
	}
	forbidden.Body.Close()

	unavailable := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/openapi/aggregate", viewer)
	if unavailable.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", unavailable.StatusCode, responseBody(t, unavailable))
	}
	var envelope testEnvelope[testErrorData]
	if err := json.NewDecoder(unavailable.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	unavailable.Body.Close()
	if envelope.Data.Reason != routeContractUnavailableReason {
		t.Fatalf("unavailable reason=%q", envelope.Data.Reason)
	}
}

type routeContractTestCatalog struct {
	snapshot extensionopenapi.PublishedContractSnapshot
}

func (c *routeContractTestCatalog) ContractSnapshot() extensionopenapi.PublishedContractSnapshot {
	return c.snapshot
}

func newRouteContractTestApp(t *testing.T, catalog RouteContractCatalog) *fiber.App {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	if catalog != nil {
		controller.WithRouteContractCatalog(catalog)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/route-contract-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	return apphttp.NewApp(
		config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false},
		slog.Default(),
		apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, login}},
	)
}

func loginRouteContractUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/route-contract-login/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login %d status=%d cookies=%d", userID, response.StatusCode, len(response.Cookies()))
	}
	return response.Cookies()[0]
}
