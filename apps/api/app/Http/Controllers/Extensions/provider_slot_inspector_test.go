package extensionscontroller

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestProviderSlotInspectorHTTPPermissionsAndUnavailableRuntime(t *testing.T) {
	want := extensions.ProviderSlotInspection{
		Revision: 9,
		Slots: []extensions.ProviderSlotInspectionItem{{
			Contract: extensions.ProviderSlotContractInspection{
				ID: "demo.delivery", Slot: "demo.delivery.slot", ContractVersion: "demo.delivery@1",
				RequestSchema: "demo.delivery.request@1", ResponseSchema: "demo.delivery.response@1",
				Fallback: "next", TimeoutMS: 500, ContractRuntimeAvailable: true,
				Artifact: extensions.ProviderSlotArtifactInspection{
					ExtensionID: "demo.owner", ExtensionVersion: "1.0.0",
					PackageDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					RuntimeInstanceID: "owner-runtime",
				},
			},
			Candidates:   []extensions.ProviderSlotCandidateInspection{},
			Conflicts:    []extensions.ProviderSlotConflictInspection{},
			Availability: "unavailable", UnavailabilityReason: "no_candidates",
		}},
	}
	app := newProviderSlotInspectorTestApp(t, &want)
	viewer := loginProviderSlotInspectorUser(t, app, 1)
	manager := loginProviderSlotInspectorUser(t, app, 2)
	denied := loginProviderSlotInspectorUser(t, app, 3)

	for _, cookie := range []*http.Cookie{viewer, manager} {
		response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/provider-slots", cookie)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("allowed status=%d body=%s", response.StatusCode, responseBody(t, response))
		}
		var body testEnvelope[extensions.ProviderSlotInspection]
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if body.Data.Revision != want.Revision || len(body.Data.Slots) != 1 ||
			body.Data.Slots[0].Contract.Artifact.RuntimeInstanceID != "owner-runtime" {
			t.Fatalf("inspection body = %#v", body.Data)
		}
	}

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/provider-slots", denied)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("denied status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()

	unavailableApp := newProviderSlotInspectorTestApp(t, nil)
	unavailableViewer := loginProviderSlotInspectorUser(t, unavailableApp, 1)
	response = performExtensionRequest(t, unavailableApp, http.MethodGet, "/api/v1/admin/extensions/provider-slots", unavailableViewer)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
}

func newProviderSlotInspectorTestApp(t *testing.T, inspection *extensions.ProviderSlotInspection) *fiber.App {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	service := extensions.NewService(nil, t.TempDir())
	if inspection != nil {
		service = extensions.NewServiceWithRuntime(nil, t.TempDir(), providerSlotControllerRuntime{
			LocalRuntimeManager: extensions.LocalRuntimeManager{}, inspection: *inspection,
		})
	}
	controller := NewController(service, actors, manager)
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/provider-slot-inspector-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	return apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
}

func loginProviderSlotInspectorUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/provider-slot-inspector-login/"+strconv.FormatInt(userID, 10), nil)
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

type providerSlotControllerRuntime struct {
	extensions.LocalRuntimeManager
	inspection extensions.ProviderSlotInspection
}

func (r providerSlotControllerRuntime) ProviderSlotInspection(context.Context) (extensions.ProviderSlotInspection, error) {
	return r.inspection, nil
}

var _ extensions.RuntimeManager = providerSlotControllerRuntime{}
var _ extensions.ProviderSlotInspectionSource = providerSlotControllerRuntime{}
