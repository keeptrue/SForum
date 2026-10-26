package extensionscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestProviderSlotManagementHTTPAuthoritySelectionResetProbeAndEvents(t *testing.T) {
	registry, contractID, candidateID := providerSlotControllerRegistry(t)
	store := &providerSlotControllerStore{}
	api := extensionsruntime.NewProviderSlotSelectionAPI(registry, store)
	prober := &providerSlotControllerProber{}
	auditor := &routeProviderControllerAuditor{}
	app := newProviderSlotManagementTestApp(t, api, prober, auditor)
	viewer := loginProviderSlotInspectorUser(t, app, 1)
	superAdmin := loginProviderSlotInspectorUser(t, app, 4)

	response := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/provider-slots/selection", viewer,
		`{"contractId":"`+contractID+`","candidateId":"`+candidateID+`","expectedRevision":0}`)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer select status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()

	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/provider-slots/selection", superAdmin,
		`{"contractId":"`+contractID+`","candidateId":"`+candidateID+`","expectedRevision":0}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("select status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var selected testEnvelope[extensionsruntime.ProviderSlotSelection]
	if err := json.NewDecoder(response.Body).Decode(&selected); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if selected.Data.CandidateID != candidateID || selected.Data.Revision != 1 || selected.Data.SelectedByUserID != 4 {
		t.Fatalf("selection = %#v", selected.Data)
	}

	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/provider-slots/probe", superAdmin,
		`{"contractId":"`+contractID+`","candidateId":"`+candidateID+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("probe status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
	if prober.contractID != contractID || prober.candidateID != candidateID {
		t.Fatalf("probe target = %s/%s", prober.contractID, prober.candidateID)
	}

	response = performExtensionRequest(t, app, http.MethodGet,
		"/api/v1/admin/extensions/provider-slots/events?contractId="+contractID, viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()

	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/provider-slots/selection/reset", superAdmin,
		`{"contractId":"`+contractID+`","expectedRevision":1,"reasonCode":"operator_reset"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
	if len(auditor.events) != 3 || auditor.events[0].Action != "providers.slot_select" ||
		auditor.events[1].Action != "providers.slot_probe" || auditor.events[2].Action != "providers.slot_reset" {
		t.Fatalf("audit events = %#v", auditor.events)
	}
}

func providerSlotControllerRegistry(t *testing.T) (*extensionsruntime.VersionedProviderSlotRegistry, string, string) {
	t.Helper()
	const contractID = "demo.owner.delivery"
	owner := extensions.Extension{
		ID: "demo.owner", Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64), Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "demo.owner", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			Providers: []extensions.ManifestProvider{{
				ID: contractID, Slot: "demo.owner.delivery.slot", Label: "Delivery", Handler: "provider.delivery",
				ContractVersion: contractID + "@1", RequestSchema: contractID + ".request@1",
				ResponseSchema: contractID + ".response@1", Fallback: "next", TimeoutMS: 500, Priority: 10,
			}},
		},
	}
	registry := extensionsruntime.NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	return registry, contractID, contractID
}

func newProviderSlotManagementTestApp(t *testing.T, api *extensionsruntime.ProviderSlotSelectionAPI,
	prober ProviderSlotProber, auditor *routeProviderControllerAuditor) *fiber.App {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		4: {ID: 4, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
	}}
	controller := NewController(extensions.NewService(nil, t.TempDir()), actors, manager).
		WithProviderSlotSelection(api, prober, auditor)
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

type providerSlotControllerStore struct {
	mu      sync.Mutex
	current *extensionsruntime.ProviderSlotSelection
	events  []extensionsruntime.ProviderSlotSelectionEvent
}

func (s *providerSlotControllerStore) Desired(_ context.Context, _ string) (extensionsruntime.ProviderSlotSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return extensionsruntime.ProviderSlotSelection{}, extensionsruntime.ErrProviderSlotSelectionNotFound
	}
	return *s.current, nil
}

func (s *providerSlotControllerStore) Selected(ctx context.Context, id string) (extensionsruntime.ProviderSlotSelection, error) {
	return s.Desired(ctx, id)
}

func (s *providerSlotControllerStore) Select(_ context.Context, request extensionsruntime.SelectProviderSlotRequest) (extensionsruntime.ProviderSlotSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil || request.ExpectedRevision != 0 {
		return extensionsruntime.ProviderSlotSelection{}, extensionsruntime.ErrProviderSlotSelectionRevisionConflict
	}
	now := time.Now().UTC()
	selection := extensionsruntime.ProviderSlotSelection{
		ContractID: request.Contract.ID, ContractVersion: request.Contract.ContractVersion, Slot: request.Contract.Slot,
		ContractArtifact: request.Contract.Artifact, CandidateID: request.Candidate.ID, ProviderArtifact: request.Candidate.Artifact,
		SelectedByUserID: request.ActorUserID, SelectionAuditID: request.AuditEventID,
		Revision: 1, SelectedAt: now, UpdatedAt: now,
	}
	s.current = &selection
	s.events = append(s.events, extensionsruntime.ProviderSlotSelectionEvent{
		ContractID: selection.ContractID, Action: "select", SelectedSelection: &selection,
		SelectionRevision: 1, CreatedAt: now,
	})
	return selection, nil
}

func (s *providerSlotControllerStore) Reset(_ context.Context, request extensionsruntime.ResetProviderSlotRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return extensionsruntime.ErrProviderSlotSelectionNotFound
	}
	if s.current.Revision != request.ExpectedRevision {
		return extensionsruntime.ErrProviderSlotSelectionRevisionConflict
	}
	s.current = nil
	return nil
}

func (*providerSlotControllerStore) InvalidateExtension(context.Context, extensionsruntime.InvalidateProviderSlotRequest) (int64, error) {
	return 0, nil
}

func (s *providerSlotControllerStore) ListEvents(context.Context, string, int) ([]extensionsruntime.ProviderSlotSelectionEvent, error) {
	return append([]extensionsruntime.ProviderSlotSelectionEvent(nil), s.events...), nil
}

type providerSlotControllerProber struct {
	contractID  string
	candidateID string
}

func (p *providerSlotControllerProber) ProbeProviderSlotCandidate(_ context.Context, contractID, candidateID string) (extensionsruntime.ProviderSlotProbeResult, error) {
	p.contractID, p.candidateID = contractID, candidateID
	if contractID == "" || candidateID == "" {
		return extensionsruntime.ProviderSlotProbeResult{}, errors.New("missing target")
	}
	return extensionsruntime.ProviderSlotProbeResult{ContractID: contractID, CandidateID: candidateID, OK: true, Reason: "provider.ready"}, nil
}

var _ extensionsruntime.ProviderSlotSelectionStore = (*providerSlotControllerStore)(nil)
