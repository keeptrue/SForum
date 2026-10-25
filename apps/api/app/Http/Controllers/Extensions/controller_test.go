package extensionscontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/zhuchunshu/sforum/apps/api/app/Http"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
	authsession "github.com/zhuchunshu/sforum/apps/api/app/Support/AuthSession"
	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

type testEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type testErrorData struct {
	Reason string `json:"reason"`
}

// Full Go suite 会并行编译参考插件；显式保留超时失败语义，避免 Fiber 默认一秒
// harness 上限把正常、有限的内存控制器请求误判为处理器阻塞。
var extensionControllerTestConfig = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

func TestMapExtensionSettingsRollbackFailure(t *testing.T) {
	mapped := mapExtensionError(errors.Join(
		extensions.ErrSettingsRollbackFailed,
		errors.New("restore database unavailable"),
	))
	fiberErr, ok := mapped.(*fiber.Error)
	if !ok {
		t.Fatalf("expected fiber error, got %T", mapped)
	}
	if fiberErr.Code != http.StatusServiceUnavailable || fiberErr.Message != extensions.CodeSettingsRollbackFailed {
		t.Fatalf("unexpected mapping: %#v", fiberErr)
	}
}

func TestMapExtensionSettingsRestartErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		reason string
	}{
		{err: extensions.ErrSettingsRevisionConflict, status: http.StatusConflict, reason: extensions.CodeSettingsRevisionConflict},
		{err: extensions.ErrSettingsRestartUnavailable, status: http.StatusServiceUnavailable, reason: extensions.CodeSettingsRestartUnavailable},
		{err: extensions.ErrSettingsRestartFailed, status: http.StatusServiceUnavailable, reason: extensions.CodeSettingsRestartFailed},
	}
	for _, test := range tests {
		mapped := mapExtensionError(errors.Join(test.err, errors.New("private runtime detail")))
		fiberErr, ok := mapped.(*fiber.Error)
		if !ok || fiberErr.Code != test.status || fiberErr.Message != test.reason {
			t.Fatalf("mapping %q = %#v", test.reason, mapped)
		}
	}
}

func TestMapLifecycleErrorDoesNotExposeInternalFailure(t *testing.T) {
	mapped := mapExtensionError(errors.Join(
		extensions.ErrLifecycleCoordinatorActionFailed,
		errors.New("private plugin SQL and process output"),
	))
	fiberErr, ok := mapped.(*fiber.Error)
	if !ok {
		t.Fatalf("expected fiber error, got %T", mapped)
	}
	if fiberErr.Code != http.StatusServiceUnavailable || fiberErr.Message != extensions.CodeLifecycleActionFailed {
		t.Fatalf("unexpected mapping: %#v", fiberErr)
	}
}

func TestMapLifecycleCleanupErrorDoesNotExposeInternalFailure(t *testing.T) {
	mapped := mapExtensionError(errors.Join(
		extensions.ErrLifecycleCleanupFinalization,
		errors.New("private purge path and SQL output"),
	))
	apiErr, ok := mapped.(*apphttp.APIError)
	if !ok {
		t.Fatalf("expected API error, got %T", mapped)
	}
	if apiErr.Status != http.StatusServiceUnavailable || apiErr.Reason != extensions.CodeLifecycleCleanupFailed || apiErr.Details != nil {
		t.Fatalf("unexpected mapping: %#v", apiErr)
	}
}

func TestMapLifecycleCleanupTypedFailureExposesStructuredDetails(t *testing.T) {
	mapped := mapExtensionError(&extensions.LifecycleCleanupFailure{
		OperationID: 703, Step: extensions.LifecycleCleanupStepPhysicalPurge, Retriable: true,
		Err: errors.Join(extensions.ErrLifecycleCleanupFinalization, errors.New("private purge path")),
	})
	apiErr, ok := mapped.(*apphttp.APIError)
	if !ok {
		t.Fatalf("expected API error, got %T", mapped)
	}
	if apiErr.Status != http.StatusServiceUnavailable || apiErr.Reason != extensions.CodeLifecycleCleanupFailed {
		t.Fatalf("unexpected mapping: %#v", apiErr)
	}
	if apiErr.Details["operationId"] != int64(703) ||
		apiErr.Details["failedStep"] != string(extensions.LifecycleCleanupStepPhysicalPurge) ||
		apiErr.Details["retriable"] != true {
		t.Fatalf("unexpected details: %#v", apiErr.Details)
	}
}

func TestControllerBindsLifecycleIdempotencyHeaderAndRequiresItForV2(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
	}}
	plugin := extensions.Extension{
		ID: "v2.controller", Name: "V2 Controller", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ID: "v2.controller", Name: "V2 Controller", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend:   extensions.ManifestBackend{ProtocolVersion: 2},
			Lifecycle: &extensions.ManifestLifecycle{ContractVersion: "v2.controller.lifecycle@1"},
		},
		ActiveVersionID: 11,
		IsDeletable:     true,
	}
	plugin.PackagePath, plugin.PackageDigest = controllerExecutablePackage(t, plugin.Manifest, "controller-v2")
	digest := plugin.PackageDigest
	store := &controllerFakeStore{items: map[string]extensions.Extension{plugin.ID: plugin}}
	runner := &controllerLifecycleRunner{}
	authority := extensions.LifecycleAuthoritySnapshot{
		SchemaVersion: extensions.LifecycleAuthoritySnapshotSchemaV1,
		AuthorityType: extensions.LifecycleAuthorityTrustGrant,
		ActorUserID:   1,
		Impact: extensions.TrustImpact{
			SchemaVersion: extensions.TrustImpactSchemaV2, Action: extensions.TrustActionEnable,
			ExtensionID: plugin.ID, ExtensionVersion: plugin.Version, ExtensionType: plugin.Type,
			Source: plugin.Source, PackageDigest: digest, Digest: "frozen-impact",
			ArtifactDigests: map[string]string{"package": digest},
		},
		Grant: &extensions.TrustGrant{
			ID: 41, ExtensionID: plugin.ID, ExtensionVersion: plugin.Version,
			PackageDigest: digest, Action: extensions.TrustActionEnable, ImpactDigest: "frozen-impact",
		},
	}
	service := extensions.NewServiceWithOptions(store, t.TempDir(), "", extensions.LocalRuntimeManager{},
		extensions.WithAuditor(controllerAuditIDWriter{}),
		extensions.WithLifecycleCoordinator(
			runner,
			func(context.Context, extensions.LifecycleMachineOperation, *extensions.Extension, extensions.Extension) error {
				return nil
			},
			controllerLifecycleAuthority{authority: authority},
		),
		extensions.WithLifecycleCleanupFinalizer(func(_ context.Context, operationID int64) (extensions.LifecycleCleanupFinalization, error) {
			return extensions.LifecycleCleanupFinalization{
				OperationID: operationID, Status: "finalized", PhysicalPurgeComplete: true,
			}, nil
		}),
	)
	controller := NewController(service, users, manager)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			_, err := manager.Start(c, 1)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	cookie := loginExtensionUser(t, app, manager, 1)

	missing := performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/v2.controller/disable", cookie)
	if missing.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing Idempotency-Key status = %d", missing.StatusCode)
	}
	missing.Body.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/extensions/v2.controller/disable", nil)
	req.AddCookie(cookie)
	req.Header.Set("Idempotency-Key", "controller-disable-1")
	response, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d", response.StatusCode)
	}
	if runner.key != "controller-disable-1" {
		t.Fatalf("ledger idempotency key = %q", runner.key)
	}

	uninstallRequest := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/admin/extensions/v2.controller",
		strings.NewReader(`{"removalMode":"complete_removal"}`),
	)
	uninstallRequest.AddCookie(cookie)
	uninstallRequest.Header.Set("Content-Type", "application/json")
	uninstallRequest.Header.Set("Idempotency-Key", "controller-uninstall-1")
	uninstallResponse, err := app.Test(uninstallRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer uninstallResponse.Body.Close()
	if uninstallResponse.StatusCode != http.StatusOK {
		t.Fatalf("uninstall status = %d: %s", uninstallResponse.StatusCode, responseBody(t, uninstallResponse))
	}
	var uninstallEnvelope testEnvelope[extensions.UninstallResult]
	if err := json.NewDecoder(uninstallResponse.Body).Decode(&uninstallEnvelope); err != nil {
		t.Fatal(err)
	}
	if runner.key != "controller-uninstall-1" || runner.operation != extensions.LifecycleOperationUninstall ||
		runner.removalMode != extensions.LifecycleRemovalComplete || !uninstallEnvelope.Data.Uninstalled ||
		uninstallEnvelope.Data.Cleanup == nil || !uninstallEnvelope.Data.Cleanup.PhysicalPurgeComplete {
		t.Fatalf("uninstall binding result=%#v runner=%#v", uninstallEnvelope.Data, runner)
	}
}

func TestControllerRequiresLoginAndExtensionManagePermission(t *testing.T) {
	app, manager, _ := newExtensionTestApp(t)

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	cookie := loginExtensionUser(t, app, manager, 2)
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions", cookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without extension.manage, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body testEnvelope[testErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Data.Reason != "permission.denied" {
		t.Fatalf("expected permission.denied, got %q", body.Data.Reason)
	}

	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.theme/verify", cookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 verifying theme without extension.manage, got %d", resp.StatusCode)
	}

	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.theme/activate", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 activating theme without session, got %d", resp.StatusCode)
	}
}

func TestControllerListsAndEnablesExtensionsForManager(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 list, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var listBody testEnvelope[[]extensions.Extension]
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.Data) != 2 || !extensionListContains(listBody.Data, "demo.plugin") || !extensionListContains(listBody.Data, "demo.theme") {
		t.Fatalf("unexpected extension list: %#v", listBody.Data)
	}

	filteredResponse := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions?id=demo.plugin", cookie)
	if filteredResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 filtered list, got %d", filteredResponse.StatusCode)
	}
	defer filteredResponse.Body.Close()
	var filteredBody testEnvelope[[]extensions.Extension]
	if err := json.NewDecoder(filteredResponse.Body).Decode(&filteredBody); err != nil {
		t.Fatalf("decode filtered list response: %v", err)
	}
	if len(filteredBody.Data) != 1 || filteredBody.Data[0].ID != "demo.plugin" {
		t.Fatalf("unexpected filtered extension list: %#v", filteredBody.Data)
	}

	missingResponse := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions?id=missing.plugin", cookie)
	if missingResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 missing filtered list, got %d", missingResponse.StatusCode)
	}
	defer missingResponse.Body.Close()
	var missingBody testEnvelope[[]extensions.Extension]
	if err := json.NewDecoder(missingResponse.Body).Decode(&missingBody); err != nil {
		t.Fatalf("decode missing filtered list response: %v", err)
	}
	if len(missingBody.Data) != 0 {
		t.Fatalf("expected empty missing filtered list, got %#v", missingBody.Data)
	}

	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/enable", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 enable, got %d", resp.StatusCode)
	}
	if store.enabledID != "demo.plugin" {
		t.Fatalf("expected store enable call for demo.plugin, got %q", store.enabledID)
	}
}

func TestControllerRestartBindsBodyAndIdempotencyHeader(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)
	item := store.items["demo.plugin"]
	item.Status = extensions.StatusEnabled
	store.items[item.ID] = item

	missingKey := performExtensionJSONRequest(
		t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/restart", cookie,
		`{"confirmCapabilities":true}`,
	)
	if missingKey.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing restart Idempotency-Key status=%d", missingKey.StatusCode)
	}
	missingKey.Body.Close()

	malformed := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/extensions/demo.plugin/restart",
		strings.NewReader(`{"confirmCapabilities":`),
	)
	malformed.AddCookie(cookie)
	malformed.Header.Set("Content-Type", "application/json")
	malformed.Header.Set("Idempotency-Key", "controller-restart-malformed")
	malformedResponse, err := app.Test(malformed, extensionControllerTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	if malformedResponse.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed restart body status=%d", malformedResponse.StatusCode)
	}
	malformedResponse.Body.Close()

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/extensions/demo.plugin/restart",
		strings.NewReader(`{"confirmCapabilities":true}`),
	)
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "controller-restart-valid")
	response, err := app.Test(request, extensionControllerTestConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	if store.disabledID != item.ID || store.enabledID != item.ID {
		t.Fatalf("restart lifecycle calls disabled=%q enabled=%q", store.disabledID, store.enabledID)
	}
}

func TestControllerExecutableTrustTargetBindsExactStagedArtifact(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {
			ID:       1,
			Status:   identity.UserStatusActive,
			RoleKeys: []string{identity.RoleSuperAdmin},
		},
	}}
	active := extensions.Extension{
		ID: "trust.controller", Name: "Trust Controller", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusDisabled, Source: extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              "trust.controller", Name: "Trust Controller", Version: "1.0.0",
			Type: extensions.TypePlugin, SForumVersion: "^1.0.0",
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				HostAPIVersion: "sforum.host@2",
			},
		},
		ActiveVersionID: 1, IsDeletable: true,
		InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
	active.PackagePath, active.PackageDigest = controllerExecutablePackage(t, active.Manifest, "active")
	stagedManifest := active.Manifest
	stagedManifest.Version = "2.0.0"
	stagedPath, stagedDigest := controllerExecutablePackage(t, stagedManifest, "staged")
	active.StagedVersion = &extensions.ExtensionVersion{
		ID: 2, Version: stagedManifest.Version, Manifest: stagedManifest,
		PackagePath: stagedPath, PackageDigest: stagedDigest, InstalledAt: time.Now(),
	}

	store := &controllerFakeStore{items: map[string]extensions.Extension{active.ID: active}}
	trustStore := &controllerExecutableTrustStore{}
	trust := extensions.NewExecutableTrustService(store, trustStore)
	service := extensions.NewServiceWithOptions(
		store, t.TempDir(), "", extensions.LocalRuntimeManager{},
		extensions.WithExecutableTrust(trust, true),
	)
	controller := NewController(service, users, manager)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			_, err := manager.Start(c, 1)
			return err
		})
	})
	app := apphttp.NewApp(
		config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false},
		slog.Default(),
		apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, loginProvider}},
	)
	cookie := loginExtensionUser(t, app, manager, 1)

	currentResponse := performExtensionRequest(
		t, app, http.MethodGet, "/api/v1/admin/extensions/trust.controller/trust", cookie,
	)
	defer currentResponse.Body.Close()
	if currentResponse.StatusCode != http.StatusOK {
		t.Fatalf("current trust status=%d body=%s", currentResponse.StatusCode, responseBody(t, currentResponse))
	}
	var current testEnvelope[extensions.ExecutableTrustStatus]
	if err := json.NewDecoder(currentResponse.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current.Data.Impact.ExtensionVersion != active.Version ||
		current.Data.Impact.PackageDigest != active.PackageDigest {
		t.Fatalf("current trust target=%#v", current.Data.Impact)
	}

	stagedResponse := performExtensionRequest(
		t, app, http.MethodGet, "/api/v1/admin/extensions/trust.controller/trust?target=staged", cookie,
	)
	defer stagedResponse.Body.Close()
	if stagedResponse.StatusCode != http.StatusOK {
		t.Fatalf("staged trust status=%d body=%s", stagedResponse.StatusCode, responseBody(t, stagedResponse))
	}
	var staged testEnvelope[extensions.ExecutableTrustStatus]
	if err := json.NewDecoder(stagedResponse.Body).Decode(&staged); err != nil {
		t.Fatal(err)
	}
	if staged.Data.Impact.ExtensionVersion != stagedManifest.Version ||
		staged.Data.Impact.PackageDigest != stagedDigest {
		t.Fatalf("staged trust target=%#v", staged.Data.Impact)
	}

	challengeResponse := performExtensionRequest(
		t, app, http.MethodPost, "/api/v1/admin/extensions/trust.controller/trust/challenge?target=staged", cookie,
	)
	defer challengeResponse.Body.Close()
	if challengeResponse.StatusCode != http.StatusOK {
		t.Fatalf("staged challenge status=%d body=%s", challengeResponse.StatusCode, responseBody(t, challengeResponse))
	}
	var challenge testEnvelope[extensions.TrustChallenge]
	if err := json.NewDecoder(challengeResponse.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Data.Impact.ExtensionVersion != stagedManifest.Version ||
		challenge.Data.Impact.PackageDigest != stagedDigest ||
		trustStore.challenge.Identity.ExtensionVersion != stagedManifest.Version {
		t.Fatalf("staged challenge=%#v stored=%#v", challenge.Data, trustStore.challenge)
	}

	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/admin/extensions/trust.controller/trust?target=latest"},
		{method: http.MethodPost, path: "/api/v1/admin/extensions/trust.controller/trust/challenge?target=latest"},
	} {
		response := performExtensionRequest(t, app, endpoint.method, endpoint.path, cookie)
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s invalid trust target status=%d", endpoint.method, response.StatusCode)
		}
		response.Body.Close()
	}
}

func TestControllerAdminPageBootstrapReturnsExtensionPageAndSettings(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {
			ID:     1,
			Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionManage: true,
			},
		},
		2: {
			ID:     2,
			Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionView: true,
			},
		},
		3: {
			ID:     3,
			Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionPluginManage: true,
			},
		},
	}}
	plugin := extensions.Extension{
		ID: "bootstrap.plugin", Name: "Bootstrap Plugin", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ID: "bootstrap.plugin", Name: "Bootstrap Plugin", Version: "1.0.0", Type: extensions.TypePlugin,
			SForumVersion: "^1.0.0",
			Admin: extensions.ManifestAdmin{
				Pages: []extensions.ManifestAdminPage{
					{Path: "/ops/config", Label: "Ops Config", View: "settings", Icon: "i-lucide-sliders", Order: 20},
					{Path: "/ops/dashboard", Label: "Ops Dashboard", View: "about", Order: 10, Menu: true},
				},
			},
			Settings: []extensions.ManifestSetting{
				{
					Key: "demo.title",
					Label: extensionmanifest.LocalizedText{
						Default:  "Title",
						ByLocale: map[string]string{"zh-CN": "标题", "en-US": "Title EN"},
					},
					Type: "text", Default: "Hello",
				},
				{Key: "demo.token", Label: extensionmanifest.LocalizedText{Default: "Token"}, Type: "secret"},
			},
		},
	}
	plugin.PackagePath = controllerInstalledPackage(t, plugin.Manifest)
	store := &controllerFakeStore{
		items: map[string]extensions.Extension{plugin.ID: plugin},
		settings: map[string]map[string]string{
			plugin.ID: {"demo.title": "Stored", "demo.token": "super-secret"},
		},
	}
	controller := NewController(extensions.NewService(store, t.TempDir()), users, manager)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			var id int64 = 1
			switch c.Params("id") {
			case "2":
				id = 2
			case "3":
				id = 3
			}
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, loginProvider}})

	managerCookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/ops/config", managerCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 page-bootstrap settings, got %d body=%s", resp.StatusCode, responseBody(t, resp))
	}
	var settingsBody testEnvelope[extensions.AdminPageBootstrap]
	if err := json.NewDecoder(resp.Body).Decode(&settingsBody); err != nil {
		t.Fatalf("decode settings bootstrap: %v", err)
	}
	resp.Body.Close()
	if settingsBody.Data.Extension.ID != plugin.ID {
		t.Fatalf("extension id=%q", settingsBody.Data.Extension.ID)
	}
	if settingsBody.Data.Page == nil || settingsBody.Data.Page.Path != "/ops/config" || settingsBody.Data.Page.View != "settings" {
		t.Fatalf("unexpected page: %#v", settingsBody.Data.Page)
	}
	if settingsBody.Data.Settings == nil || controllerSettingValue(*settingsBody.Data.Settings, "demo.title") != "Stored" {
		t.Fatalf("unexpected settings: %#v", settingsBody.Data.Settings)
	}
	if settingsBody.Data.Settings.Items[0].Label != "标题" {
		t.Fatalf("expected localized label, got %q", settingsBody.Data.Settings.Items[0].Label)
	}
	if controllerSettingValue(*settingsBody.Data.Settings, "demo.token") != "" {
		t.Fatalf("secret must stay masked: %#v", settingsBody.Data.Settings)
	}

	// 显式 null：用原始 JSON 断言 page/settings 缺失时不是 omit。
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/about", managerCookie)
	rawAbout := responseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 about bootstrap, got %d body=%s", resp.StatusCode, rawAbout)
	}
	var aboutBody testEnvelope[extensions.AdminPageBootstrap]
	if err := json.Unmarshal([]byte(rawAbout), &aboutBody); err != nil {
		t.Fatalf("decode about bootstrap: %v", err)
	}
	if aboutBody.Data.Page == nil || aboutBody.Data.Page.Path != "/about" || aboutBody.Data.Page.View != "about" {
		t.Fatalf("expected /about page, got %#v", aboutBody.Data.Page)
	}
	if aboutBody.Data.Settings != nil {
		t.Fatalf("about settings must be null: %#v", aboutBody.Data.Settings)
	}
	if !strings.Contains(rawAbout, `"settings":null`) {
		t.Fatalf("about response must encode settings as null: %s", rawAbout)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/missing", managerCookie)
	rawUnknown := responseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 unknown bootstrap, got %d body=%s", resp.StatusCode, rawUnknown)
	}
	if !strings.Contains(rawUnknown, `"page":null`) || !strings.Contains(rawUnknown, `"settings":null`) {
		t.Fatalf("unknown path must encode page/settings null: %s", rawUnknown)
	}
	var unknownBody testEnvelope[extensions.AdminPageBootstrap]
	if err := json.Unmarshal([]byte(rawUnknown), &unknownBody); err != nil {
		t.Fatalf("decode unknown bootstrap: %v", err)
	}
	if unknownBody.Data.Extension.ID != plugin.ID || unknownBody.Data.Page != nil || unknownBody.Data.Settings != nil {
		t.Fatalf("unexpected unknown payload: %#v", unknownBody.Data)
	}

	viewerCookie := loginExtensionUser(t, app, manager, 2)
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/about", viewerCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer about must remain allowed, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/ops/config", viewerCookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer settings path must be forbidden, got %d body=%s", resp.StatusCode, responseBody(t, resp))
	}

	pluginManagerCookie := loginExtensionUser(t, app, manager, 3)
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/ops/config", pluginManagerCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plugin manager settings path must be allowed, got %d body=%s", resp.StatusCode, responseBody(t, resp))
	}
	resp.Body.Close()
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/bootstrap.plugin/page-bootstrap?path=/about", pluginManagerCookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plugin manager without extension.view must not read about, got %d body=%s", resp.StatusCode, responseBody(t, resp))
	}
}

func TestControllerListsNavigationAndManagesExtensionSettings(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)
	plugin := store.items["demo.plugin"]
	plugin.Status = extensions.StatusEnabled
	plugin.Manifest.Admin = extensions.ManifestAdmin{
		Entry: "/settings",
		Pages: []extensions.ManifestAdminPage{
			{Path: "/settings", Label: "Settings", View: "settings", Icon: "i-lucide-settings", Order: 10},
			{Path: "/dashboard", Label: "Dashboard", View: "about", Icon: "i-lucide-layout-dashboard", Order: 5, Menu: true, Group: "Demo Group"},
		},
	}
	plugin.Manifest.Settings = []extensions.ManifestSetting{{Key: "demo.title", Label: extensionmanifest.LocalizedText{Default: "Title"}, Type: "text", Default: "Hello"}}
	store.items[plugin.ID] = plugin
	theme := store.items["demo.theme"]
	theme.Status = extensions.StatusEnabled
	store.items[theme.ID] = theme

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/navigation", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 navigation, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var navigation testEnvelope[[]extensions.ExtensionAdminNavigationItem]
	if err := json.NewDecoder(resp.Body).Decode(&navigation); err != nil {
		t.Fatalf("decode navigation: %v", err)
	}
	if !controllerNavigationContains(navigation.Data, "demo.plugin", "/dashboard") || controllerNavigationContains(navigation.Data, "demo.plugin", "/settings") || controllerNavigationContains(navigation.Data, "demo.theme", "/about") {
		t.Fatalf("unexpected navigation items: %#v", navigation.Data)
	}
	if group := controllerNavigationGroup(navigation.Data, "demo.plugin", "/dashboard"); group != "Demo Group" {
		t.Fatalf("expected navigation item to carry declared group, got %q", group)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/settings", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 settings, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var settings testEnvelope[extensions.ExtensionSettings]
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if controllerSettingValue(settings.Data, "demo.title") != "Hello" {
		t.Fatalf("expected default setting, got %#v", settings.Data)
	}

	resp = performExtensionJSONRequest(t, app, http.MethodPut, "/api/v1/admin/extensions/demo.plugin/settings", cookie, `{"values":{"demo.title":"Updated"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 update settings, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	if controllerSettingValue(settings.Data, "demo.title") != "Updated" {
		t.Fatalf("expected updated setting, got %#v", settings.Data)
	}

	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/settings/reset", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 reset settings, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		t.Fatalf("decode reset settings: %v", err)
	}
	if controllerSettingValue(settings.Data, "demo.title") != "Hello" {
		t.Fatalf("expected default after reset, got %#v", settings.Data)
	}
}

func TestControllerProxiesOnlyDeclaredPluginRoutesAfterHostAuthorization(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	store.items["demo.plugin"] = extensions.Extension{
		ID:      "demo.plugin",
		Name:    "Demo Plugin",
		Version: "1.0.0",
		Type:    extensions.TypePlugin,
		Status:  extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              "demo.plugin",
			Name:            "Demo Plugin",
			Description:     "Demo plugin for controller tests.",
			URL:             "https://example.com/demo-plugin",
			Author:          extensions.ManifestAuthor{Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com"},
			Version:         "1.0.0",
			Type:            extensions.TypePlugin,
			SForumVersion:   "^1.0.0",
			Permissions:     []string{"extension.demo.manage"},
			Routes: []extensions.ManifestRoute{
				{Path: "/hello", Methods: []string{"GET"}, Access: extensions.RouteAccessPublic},
				{Path: "/profile", Methods: []string{"GET"}, Access: extensions.RouteAccessLogin},
				{Path: "/admin/reindex", Methods: []string{"POST"}, Access: extensions.RouteAccessPermission, Permission: "extension.demo.manage"},
			},
		},
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/extensions/demo.plugin/hello", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected public route 200, got %d", resp.StatusCode)
	}
	if body := responseBody(t, resp); body != "plugin-ok" {
		t.Fatalf("expected plugin proxy body, got %q", body)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/extensions/demo.plugin/profile", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected login route 401, got %d", resp.StatusCode)
	}

	ordinaryCookie := loginExtensionUser(t, app, manager, 2)
	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/extensions/demo.plugin/admin/reindex", ordinaryCookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected permission route 403, got %d", resp.StatusCode)
	}

	managerCookie := loginExtensionUser(t, app, manager, 1)
	resp = performExtensionRequest(t, app, http.MethodPost, "/api/v1/extensions/demo.plugin/admin/reindex", managerCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected permission route 200, got %d", resp.StatusCode)
	}
	if body := responseBody(t, resp); body != "plugin-ok" {
		t.Fatalf("expected plugin proxy body, got %q", body)
	}

	resp = performExtensionRequest(t, app, http.MethodDelete, "/api/v1/extensions/demo.plugin/hello", managerCookie)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected undeclared method 405, got %d", resp.StatusCode)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/extensions/demo.plugin/missing", managerCookie)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected undeclared path 404, got %d", resp.StatusCode)
	}
}

// TestProxyExtensionRouteDeniesBeforeGatewayDispatch 固定「守卫在代理之前」这条语义：
// legacy 前缀对协议 v2 插件回落派发之后，匿名与无权限请求仍然不能到达网关。
func TestProxyExtensionRouteDeniesBeforeGatewayDispatch(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	plugin := extensions.Extension{
		ID: "demo.plugin", Name: "Demo Plugin", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "demo.plugin", Name: "Demo Plugin",
			Description: "Demo plugin for guard ordering.", Version: "1.0.0", Type: extensions.TypePlugin,
			URL: "https://example.com/demo-plugin",
			Author: extensions.ManifestAuthor{
				Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com",
			},
			SForumVersion: "^1.0.0",
			Permissions:   []string{"extension.demo.manage"},
			Routes: []extensions.ManifestRoute{
				{ID: "demo.route.public", Path: "/hello", Methods: []string{http.MethodGet}, Access: extensions.RouteAccessPublic},
				{ID: "demo.route.login", Path: "/profile", Methods: []string{http.MethodGet}, Access: extensions.RouteAccessLogin},
				{
					ID: "demo.route.permission", Path: "/admin/reindex", Methods: []string{http.MethodPost},
					Access: extensions.RouteAccessPermission, Permission: "extension.demo.manage",
				},
			},
		},
	}
	store := &controllerFakeStore{items: map[string]extensions.Extension{plugin.ID: plugin}}
	gateway := &controllerRecordingGateway{}
	controller := NewControllerWithGateway(
		extensions.NewService(store, t.TempDir()),
		controllerActors{actors: map[int64]identity.Actor{
			2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		}},
		manager,
		gateway,
	)
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		controller,
		extensionRouteProviderFunc(func(api fiber.Router) {
			api.Post("/test-login/:id", func(c fiber.Ctx) error {
				var id int64 = 1
				if c.Params("id") == "2" {
					id = 2
				}
				_, err := manager.Start(c, id)
				return err
			})
		}),
	}})

	// 匿名：login 与 permission 路由都必须在网关之前被拦下。
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/extensions/demo.plugin/profile"},
		{http.MethodPost, "/api/v1/extensions/demo.plugin/admin/reindex"},
	} {
		resp := performExtensionRequest(t, app, probe.method, probe.path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s: expected 401, got %d", probe.method, probe.path, resp.StatusCode)
		}
	}
	if gateway.calls != 0 {
		t.Fatalf("anonymous requests reached the gateway %d times", gateway.calls)
	}

	// 已登录但无权限：403，且同样不能到达网关。
	ordinaryCookie := loginExtensionUser(t, app, manager, 2)
	resp := performExtensionRequest(t, app, http.MethodPost, "/api/v1/extensions/demo.plugin/admin/reindex", ordinaryCookie)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || gateway.calls != 0 {
		t.Fatalf("permission route: status=%d gatewayCalls=%d", resp.StatusCode, gateway.calls)
	}

	// RouteAccessPublic 仍然放行。
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/extensions/demo.plugin/hello", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gateway.calls != 1 {
		t.Fatalf("public route: status=%d gatewayCalls=%d", resp.StatusCode, gateway.calls)
	}
}

func TestControllerRejectsStalePublicFrontendBridgeBeforeRuntimeDispatch(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	packageDigest := strings.Repeat("a", 64)
	impactDigest := strings.Repeat("b", 64)
	componentID := "demo.plugin.component.card"
	plugin := extensions.Extension{
		ID: "demo.plugin", Name: "Demo Plugin", Version: "1.0.0",
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: packageDigest,
		Manifest: extensions.Manifest{
			ID: "demo.plugin", Name: "Demo Plugin", Version: "1.0.0", Type: extensions.TypePlugin,
			Routes: []extensions.ManifestRoute{{Path: "/hello", Methods: []string{"GET"}, Access: extensions.RouteAccessPublic}},
		},
	}
	store := &controllerFakeStore{items: map[string]extensions.Extension{plugin.ID: plugin}}
	gateway := &controllerRecordingGateway{}
	frontend := &fakeTrustedFrontendHTTPService{publicComponent: extensions.PublicFrontendComponent{
		ExtensionID: plugin.ID, ExtensionVersion: plugin.Version,
		PackageDigest: packageDigest, ImpactDigest: impactDigest, ComponentID: componentID,
	}}
	controller := NewControllerWithGateway(
		extensions.NewService(store, t.TempDir()), controllerActors{actors: map[int64]identity.Actor{}}, manager, gateway,
	).WithTrustedRuntime(frontend)
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller}})

	request := func(headers map[string]string) *http.Response {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/extensions/demo.plugin/hello", nil)
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	exactHeaders := map[string]string{
		PublicFrontendHeaderExtensionID: plugin.ID, PublicFrontendHeaderExtensionVersion: plugin.Version,
		PublicFrontendHeaderPackageDigest: packageDigest, PublicFrontendHeaderImpactDigest: impactDigest,
		PublicFrontendHeaderComponentID: componentID,
	}
	response := request(exactHeaders)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || gateway.calls != 1 || gateway.exact == nil ||
		gateway.exact.ImpactDigest != impactDigest {
		t.Fatalf("exact bridge status=%d calls=%d identity=%#v", response.StatusCode, gateway.calls, gateway.exact)
	}

	for name, stale := range map[string]string{
		PublicFrontendHeaderExtensionVersion: "2.0.0",
		PublicFrontendHeaderPackageDigest:    strings.Repeat("c", 64),
		PublicFrontendHeaderImpactDigest:     strings.Repeat("d", 64),
	} {
		headers := maps.Clone(exactHeaders)
		headers[name] = stale
		response = request(headers)
		response.Body.Close()
		if response.StatusCode != http.StatusPreconditionFailed || gateway.calls != 1 {
			t.Fatalf("stale %s status=%d calls=%d", name, response.StatusCode, gateway.calls)
		}
	}

	frontend.publicErr = extensions.ErrPublicFrontendUnavailable
	response = request(exactHeaders)
	response.Body.Close()
	if response.StatusCode != http.StatusPreconditionFailed || gateway.calls != 1 {
		t.Fatalf("revoked bridge status=%d calls=%d", response.StatusCode, gateway.calls)
	}
	frontend.publicErr = nil
	response = request(nil)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || gateway.calls != 2 {
		t.Fatalf("legacy route status=%d calls=%d", response.StatusCode, gateway.calls)
	}
}

func TestControllerVerifiesAndActivatesThemesForManager(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)

	resp := performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.theme/verify", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 verify, got %d", resp.StatusCode)
	}
	if store.verifiedID != "demo.theme" {
		t.Fatalf("expected store verify call for demo.theme, got %q", store.verifiedID)
	}

	resp = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.theme/activate", cookie,
		`{"version":"1.0.0","packageDigest":"`+strings.Repeat("f", 64)+`","currentThemeId":"","currentThemeVersion":"","currentThemeDigest":"","approveCoreReplacements":false}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected stale preview 409, got %d", resp.StatusCode)
	}

	theme := store.items["demo.theme"]
	resp = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.theme/activate", cookie,
		`{"version":"`+theme.Version+`","packageDigest":"`+theme.PackageDigest+`","currentThemeId":"","currentThemeVersion":"","currentThemeDigest":"","approveCoreReplacements":false}`)
	// Runtime Page Registry：主题激活同步完成，不触发 Nuxt 构建。
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 runtime theme activation, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body testEnvelope[extensions.Extension]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode activation response envelope: %v", err)
	}
	if body.Data.ID != "demo.theme" {
		t.Fatalf("expected demo.theme activated, got %#v", body.Data)
	}
}

func TestControllerListsExtensionEventDefinitionsAndDeliveries(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)
	store.deliveries = []extensions.ExtensionEventDelivery{{
		ID:            7,
		ExtensionID:   "demo.plugin",
		EventName:     appevents.TopicCreated,
		EventKind:     appevents.KindObserve,
		Status:        extensions.DeliverySucceeded,
		CorrelationID: "corr-1",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}}

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/event-definitions", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 event definitions, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var definitions testEnvelope[[]appevents.Definition]
	if err := json.NewDecoder(resp.Body).Decode(&definitions); err != nil {
		t.Fatalf("decode event definitions: %v", err)
	}
	if !eventDefinitionListContains(definitions.Data, appevents.TopicBeforeCreate) {
		t.Fatalf("expected topic.before_create definition, got %#v", definitions.Data)
	}
	if !eventDefinitionListContains(definitions.Data, appevents.CommentBeforeCreate) {
		t.Fatalf("expected comment.before_create definition, got %#v", definitions.Data)
	}
	if !eventDefinitionListContains(definitions.Data, appevents.TopicBeforeUpdate) {
		t.Fatalf("expected topic.before_update definition, got %#v", definitions.Data)
	}
	if !eventDefinitionListContains(definitions.Data, appevents.UserBeforeRegister) {
		t.Fatalf("expected user.before_register definition, got %#v", definitions.Data)
	}
	if !eventDefinitionListContains(definitions.Data, appevents.AttachmentBeforeUpload) {
		t.Fatalf("expected attachment.before_upload definition, got %#v", definitions.Data)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/event-deliveries?extensionId=demo.plugin", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 event deliveries, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var deliveries testEnvelope[[]extensions.ExtensionEventDelivery]
	if err := json.NewDecoder(resp.Body).Decode(&deliveries); err != nil {
		t.Fatalf("decode event deliveries: %v", err)
	}
	if len(deliveries.Data) != 1 || deliveries.Data[0].EventName != appevents.TopicCreated {
		t.Fatalf("unexpected deliveries: %#v", deliveries.Data)
	}
}

func TestControllerListsContributionPointsAndContributions(t *testing.T) {
	app, manager, store := newExtensionTestApp(t)
	cookie := loginExtensionUser(t, app, manager, 1)
	plugin := store.items["demo.plugin"]
	plugin.Status = extensions.StatusEnabled
	plugin.Manifest.Contributions = []extensions.ManifestContribution{{
		Point: "forum.topic.actions",
		ID:    "demo.bookmark",
		Order: 100,
		Label: map[string]string{
			"zh-CN": "收藏",
			"en-US": "Bookmark",
		},
		Icon:    "i-lucide-bookmark",
		Payload: json.RawMessage(`{"type":"extensionRoute","method":"POST","path":"/topic-actions/bookmark"}`),
	}}
	store.items[plugin.ID] = plugin

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/contribution-points", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	ordinaryCookie := loginExtensionUser(t, app, manager, 2)
	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/contributions", ordinaryCookie)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without extension.manage, got %d", resp.StatusCode)
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/contribution-points", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 contribution points, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var points testEnvelope[[]extensions.ContributionPointDefinition]
	if err := json.NewDecoder(resp.Body).Decode(&points); err != nil {
		t.Fatalf("decode contribution points: %v", err)
	}
	pointIDs := make(map[string]bool, len(points.Data))
	for _, point := range points.Data {
		pointIDs[point.ID] = true
	}
	// F4.3 + E2 公开贡献点 + jobs + extension settings。
	requiredPoints := []string{
		"forum.topic.actions",
		"forum.topic.sidebar",
		"forum.topic.badges",
		"forum.comment.actions",
		"forum.nav.items",
		"forum.topic.list.badges",
		"forum.composer.toolbar",
		"forum.profile.tabs",
		"admin.dashboard.widgets",
		"system.health.checks",
		"forum.page.regions",
	}
	if len(points.Data) != len(requiredPoints) {
		t.Fatalf("unexpected contribution points count %d: %#v", len(points.Data), points.Data)
	}
	for _, id := range requiredPoints {
		if !pointIDs[id] {
			t.Fatalf("missing contribution point %q in %#v", id, points.Data)
		}
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/contributions", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 contributions, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var contributions testEnvelope[[]extensions.EffectiveContribution]
	if err := json.NewDecoder(resp.Body).Decode(&contributions); err != nil {
		t.Fatalf("decode contributions: %v", err)
	}
	if len(contributions.Data) != 1 || contributions.Data[0].ExtensionID != "demo.plugin" || contributions.Data[0].ID != "demo.bookmark" {
		t.Fatalf("unexpected contributions: %#v", contributions.Data)
	}
}

func newExtensionTestApp(t *testing.T) (*fiber.App, *authsession.Manager, *controllerFakeStore) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {
			ID:          1,
			Status:      identity.UserStatusActive,
			Permissions: map[string]bool{identity.PermissionExtensionManage: true, "extension.demo.manage": true},
		},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
	}}
	plugin := extensions.Extension{
		ID:      "demo.plugin",
		Name:    "Demo Plugin",
		Version: "1.0.0",
		Type:    extensions.TypePlugin,
		Status:  extensions.StatusInstalled,
		Source:  extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              "demo.plugin",
			Name:            "Demo Plugin",
			Description:     "Demo plugin for route tests.",
			URL:             "https://example.com/demo-plugin",
			Author:          extensions.ManifestAuthor{Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com"},
			Version:         "1.0.0",
			Type:            extensions.TypePlugin,
			SForumVersion:   "^1.0.0",
		},
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
	plugin.PackagePath = controllerInstalledPackage(t, plugin.Manifest)
	theme := extensions.Extension{
		ID:      "demo.theme",
		Name:    "Demo Theme",
		Version: "1.0.0",
		Type:    extensions.TypeTheme,
		Status:  extensions.StatusInstalled,
		Source:  extensions.SourceUploaded,
		Manifest: extensions.Manifest{
			ManifestVersion: 3,
			ID:              "demo.theme",
			Name:            "Demo Theme",
			Description:     "Demo theme for controller tests.",
			URL:             "https://example.com/demo-theme",
			Author:          extensions.ManifestAuthor{Name: "SForum Team", URL: "https://example.com", Email: "dev@example.com"},
			Version:         "1.0.0",
			Type:            extensions.TypeTheme,
			SForumVersion:   "^1.0.0",
		},
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
	theme.PackagePath = filepath.Dir(controllerInstalledPackage(t, theme.Manifest))
	digest, digestErr := extensionpackage.DigestTree(theme.PackagePath)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	theme.PackageDigest = digest
	store := &controllerFakeStore{items: map[string]extensions.Extension{
		plugin.ID: plugin,
		theme.ID:  theme,
	}}
	controller := NewControllerWithGateway(extensions.NewServiceWithHooks(store, "storage/extensions", nil), users, manager, controllerFakeGateway{})
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			var id int64 = 1
			if c.Params("id") == "2" {
				id = 2
			}
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	return app, manager, store
}

func controllerInstalledPackage(t *testing.T, manifest extensions.Manifest) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), manifest.ID, manifest.Version)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create extension package root: %v", err)
	}
	packagePath := filepath.Join(root, "package.zip")
	if err := os.WriteFile(packagePath, []byte("zip"), 0o600); err != nil {
		t.Fatalf("write extension archive: %v", err)
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal extension manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, extensions.ManifestFileName), body, 0o600); err != nil {
		t.Fatalf("write extension manifest: %v", err)
	}
	if manifest.Type == extensions.TypeTheme {
		if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"schemaVersion":1,"styles":{"tokens":{}}}`), 0o600); err != nil {
			t.Fatalf("write theme contract: %v", err)
		}
	}
	return packagePath
}

func controllerExecutablePackage(t *testing.T, manifest extensions.Manifest, binary string) (string, string) {
	t.Helper()
	manifest.ManifestVersion = 3
	if manifest.SForumVersion == "" {
		manifest.SForumVersion = "^1.0.0"
	}
	if manifest.Backend.Entry != "" {
		digest := sha256.Sum256([]byte(binary))
		manifest.Backend.RPC = "hashicorp-go-plugin"
		manifest.Backend.ProtocolVersion = 2
		manifest.Backend.HostAPIVersion = "sforum.host@2"
		manifest.Backend.Digest = fmt.Sprintf("%x", digest)
		manifest.PackageFiles = []extensions.ManifestPackageFile{{
			ID: manifest.ID + ".file.backend", Kind: "executable", Path: manifest.Backend.Entry, Digest: manifest.Backend.Digest,
		}}
	}
	root := filepath.Join(t.TempDir(), manifest.ID, manifest.Version)
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend", "plugin"), []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extensions.ManifestFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, digest
}

func loginExtensionUser(t *testing.T, app *fiber.App, _ *authsession.Manager, userID int64) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test-login/1", nil)
	if userID == 2 {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/test-login/2", nil)
	} else if userID == 3 {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/test-login/3", nil)
	}
	resp, err := app.Test(req, extensionControllerTestConfig)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected login cookie")
	}
	return resp.Cookies()[0]
}

func performExtensionRequest(t *testing.T, app *fiber.App, method string, path string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req, extensionControllerTestConfig)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

func performExtensionJSONRequest(t *testing.T, app *fiber.App, method string, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req, extensionControllerTestConfig)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

func responseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func extensionListContains(items []extensions.Extension, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func eventDefinitionListContains(items []appevents.Definition, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func controllerNavigationContains(items []extensions.ExtensionAdminNavigationItem, extensionID string, pagePath string) bool {
	for _, item := range items {
		if item.ExtensionID == extensionID && item.Path == pagePath {
			return true
		}
	}
	return false
}

func controllerNavigationGroup(items []extensions.ExtensionAdminNavigationItem, extensionID string, pagePath string) string {
	for _, item := range items {
		if item.ExtensionID == extensionID && item.Path == pagePath {
			return item.Group
		}
	}
	return ""
}

func controllerSettingValue(settings extensions.ExtensionSettings, key string) string {
	for _, item := range settings.Items {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}

type controllerActors struct {
	actors map[int64]identity.Actor
}

func (s controllerActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	return s.actors[userID], nil
}

type extensionRouteProviderFunc func(api fiber.Router)

func (f extensionRouteProviderFunc) RegisterRoutes(api fiber.Router) {
	f(api)
}

type controllerFakeGateway struct{}

func (controllerFakeGateway) Proxy(c fiber.Ctx, input ProxyInput) error {
	c.Status(http.StatusOK)
	return c.SendString("plugin-ok")
}

type controllerRecordingGateway struct {
	calls int
	exact *PublicFrontendBridgeIdentity
}

func (g *controllerRecordingGateway) Proxy(c fiber.Ctx, input ProxyInput) error {
	g.calls++
	g.exact = input.PublicFrontendExact
	c.Status(http.StatusOK)
	return c.SendString("plugin-ok")
}

type controllerLifecycleRunner struct {
	key         string
	operation   string
	removalMode string
}

func (r *controllerLifecycleRunner) Run(_ context.Context, input extensions.LifecycleCoordinatorRunInput) (extensions.LifecycleCoordinatorRunResult, error) {
	r.key = input.Acquire.IdempotencyKey
	r.operation = input.Acquire.Operation
	r.removalMode = input.Acquire.RemovalMode
	completedAt := time.Now().UTC()
	return extensions.LifecycleCoordinatorRunResult{
		Operation: extensions.LifecycleOperation{
			ID: 61, ExtensionID: input.Acquire.ExtensionID, ExtensionVersion: input.Acquire.ExtensionVersion,
			PackageDigest: input.Acquire.PackageDigest, Operation: input.Acquire.Operation,
			RemovalMode: input.Acquire.RemovalMode, TerminalResult: extensions.LifecycleTerminalSucceeded,
			CompletedAt: &completedAt,
		},
	}, nil
}

type controllerLifecycleAuthority struct {
	authority extensions.LifecycleAuthoritySnapshot
}

func (r controllerLifecycleAuthority) LastSuccessfulLifecycleAuthority(context.Context, extensions.ExactExtensionVersionInput) (extensions.LifecycleAuthoritySnapshot, error) {
	return r.authority, nil
}

func (r controllerLifecycleAuthority) OperationByIdempotencyKey(context.Context, string, string) (extensions.LifecycleOperation, error) {
	return extensions.LifecycleOperation{}, extensions.ErrLifecycleOperationNotFound
}

type controllerAuditIDWriter struct{}

func (controllerAuditIDWriter) Append(context.Context, audit.Event) error { return nil }

func (controllerAuditIDWriter) AppendReturningID(context.Context, audit.Event) (int64, error) {
	return 51, nil
}

type controllerFakeStore struct {
	items      map[string]extensions.Extension
	enabledID  string
	disabledID string
	verifiedID string
	settings   map[string]map[string]string
	events     []extensions.ExtensionEvent
	deliveries []extensions.ExtensionEventDelivery
}

func (s *controllerFakeStore) List(context.Context) ([]extensions.Extension, error) {
	items := make([]extensions.Extension, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items, nil
}

func (s *controllerFakeStore) Get(_ context.Context, id string) (extensions.Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	return item, nil
}

func (s *controllerFakeStore) SaveInstalled(_ context.Context, input extensions.SaveInstalledInput) (extensions.Extension, error) {
	item := extensions.Extension{
		ID:          input.Manifest.ID,
		Name:        input.Manifest.Name,
		Version:     input.Manifest.Version,
		Type:        input.Manifest.Type,
		Status:      extensions.StatusInstalled,
		Source:      extensions.SourceUploaded,
		IsDeletable: true,
		Manifest:    input.Manifest,
		PackagePath: input.PackagePath,
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.items[item.ID] = item
	return item, nil
}

func (s *controllerFakeStore) PromoteStagedVersion(_ context.Context, input extensions.StagedVersionCASInput) (extensions.Extension, error) {
	item, ok := s.items[input.ExtensionID]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	if item.StagedVersion == nil {
		return extensions.Extension{}, extensions.ErrStagedVersionNotFound
	}
	if item.StagedVersion.ID != input.ExpectedStagedVersionID || item.StagedVersion.PackageDigest != input.ExpectedPackageDigest {
		return extensions.Extension{}, extensions.ErrStagedVersionConflict
	}
	staged := item.StagedVersion
	item.Version, item.Manifest = staged.Version, staged.Manifest
	item.PackageDigest, item.AdminFrontendDigest = staged.PackageDigest, staged.AdminFrontendDigest
	item.PackagePath, item.ActiveVersionID = staged.PackagePath, staged.ID
	item.InstalledAt, item.StagedVersion = staged.InstalledAt, nil
	s.items[item.ID] = item
	return item, nil
}

func (s *controllerFakeStore) DiscardStagedVersion(_ context.Context, input extensions.StagedVersionCASInput) (extensions.Extension, error) {
	item, ok := s.items[input.ExtensionID]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	if item.StagedVersion == nil {
		return extensions.Extension{}, extensions.ErrStagedVersionNotFound
	}
	if item.StagedVersion.ID != input.ExpectedStagedVersionID || item.StagedVersion.PackageDigest != input.ExpectedPackageDigest {
		return extensions.Extension{}, extensions.ErrStagedVersionConflict
	}
	item.StagedVersion = nil
	s.items[item.ID] = item
	return item, nil
}

func (s *controllerFakeStore) SaveBuiltin(_ context.Context, input extensions.SaveBuiltinInput) (extensions.Extension, error) {
	item := extensions.Extension{
		ID:          input.Manifest.ID,
		Name:        input.Manifest.Name,
		Version:     input.Manifest.Version,
		Type:        input.Manifest.Type,
		Status:      extensions.StatusEnabled,
		Source:      extensions.SourceBuiltin,
		IsSystem:    true,
		IsDeletable: false,
		Manifest:    input.Manifest,
		PackagePath: input.PackagePath,
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.items[item.ID] = item
	return item, nil
}

func (s *controllerFakeStore) PruneMissingBuiltins(_ context.Context, activeIDs []string) (extensions.BuiltinPruneResult, error) {
	active := map[string]bool{}
	for _, id := range activeIDs {
		active[id] = true
	}
	for id, item := range s.items {
		if item.Source == extensions.SourceBuiltin && !active[id] {
			delete(s.items, id)
		}
	}
	return extensions.BuiltinPruneResult{}, nil
}

func (s *controllerFakeStore) Enable(_ context.Context, id string, _ string) (extensions.Extension, error) {
	item := s.items[id]
	item.Status = extensions.StatusEnabled
	s.items[id] = item
	s.enabledID = id
	return item, nil
}

func (s *controllerFakeStore) Disable(_ context.Context, id string) (extensions.Extension, error) {
	item := s.items[id]
	item.Status = extensions.StatusDisabled
	s.items[id] = item
	s.disabledID = id
	return item, nil
}

func (s *controllerFakeStore) ActivateTheme(_ context.Context, id string) (extensions.ThemeActivationResult, error) {
	item := s.items[id]
	item.Status = extensions.StatusEnabled
	s.items[id] = item
	return extensions.ThemeActivationResult{Extension: item}, nil
}

func (s *controllerFakeStore) ActivateThemeExact(ctx context.Context, id string, expected extensions.ThemeActivationInput) (extensions.ThemeActivationResult, error) {
	target, ok := s.items[id]
	if !ok {
		return extensions.ThemeActivationResult{}, extensions.ErrExtensionNotFound
	}
	current, err := s.ActiveTheme(ctx)
	if errors.Is(err, extensions.ErrExtensionNotFound) {
		current = extensions.Extension{}
	} else if err != nil {
		return extensions.ThemeActivationResult{}, err
	}
	if target.Version != expected.Version || !strings.EqualFold(target.PackageDigest, expected.PackageDigest) ||
		current.ID != expected.CurrentThemeID || current.Version != expected.CurrentThemeVersion ||
		!strings.EqualFold(current.PackageDigest, expected.CurrentThemeDigest) {
		return extensions.ThemeActivationResult{}, extensions.ErrThemePreviewStale
	}
	return s.ActivateTheme(ctx, id)
}

func (s *controllerFakeStore) CompensateThemeActivation(_ context.Context, _ extensions.ThemeRuntimePublication, previous *extensions.Extension) (extensions.ThemeActivationResult, error) {
	if previous == nil {
		return extensions.ThemeActivationResult{}, nil
	}
	return extensions.ThemeActivationResult{Extension: *previous}, nil
}

func (s *controllerFakeStore) ActiveTheme(context.Context) (extensions.Extension, error) {
	for _, item := range s.items {
		if item.Type == extensions.TypeTheme && item.Status == extensions.StatusEnabled {
			return item, nil
		}
	}
	return extensions.Extension{}, extensions.ErrExtensionNotFound
}

func (s *controllerFakeStore) CreateEvent(_ context.Context, input extensions.EventInput) (extensions.ExtensionEvent, error) {
	if input.Action == extensions.EventVerified {
		s.verifiedID = input.ExtensionID
	}
	event := extensions.ExtensionEvent{ID: int64(len(s.events) + 1), ExtensionID: input.ExtensionID, ActorUserID: input.ActorUserID, Action: input.Action, Message: input.Message, CreatedAt: time.Now()}
	s.events = append(s.events, event)
	return event, nil
}

func (s *controllerFakeStore) ListEvents(context.Context, string, int) ([]extensions.ExtensionEvent, error) {
	return s.events, nil
}

func (s *controllerFakeStore) ListSettings(_ context.Context, extensionID string) (map[string]string, error) {
	if s.settings == nil {
		return map[string]string{}, nil
	}
	values := map[string]string{}
	for key, value := range s.settings[extensionID] {
		values[key] = value
	}
	return values, nil
}

func (s *controllerFakeStore) ReplaceSettings(_ context.Context, extensionID string, values map[string]string) error {
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

func (s *controllerFakeStore) CompareAndSwapSetting(_ context.Context, extensionID, name, oldValue, newValue string) (bool, error) {
	if s.settings == nil || s.settings[extensionID][name] != oldValue {
		return false, nil
	}
	s.settings[extensionID][name] = newValue
	return true, nil
}

func (s *controllerFakeStore) ResetSettings(_ context.Context, extensionID string) error {
	if s.settings != nil {
		delete(s.settings, extensionID)
	}
	return nil
}

func (s *controllerFakeStore) Delete(_ context.Context, id string) error {
	if _, ok := s.items[id]; !ok {
		return extensions.ErrExtensionNotFound
	}
	delete(s.items, id)
	if s.settings != nil {
		delete(s.settings, id)
	}
	return nil
}

func (s *controllerFakeStore) ListMigrationLedger(context.Context, string) ([]extensions.MigrationRecord, error) {
	return []extensions.MigrationRecord{}, nil
}

func (s *controllerFakeStore) RecordMigration(context.Context, string, extensions.MigrationRecord) error {
	return nil
}

func (s *controllerFakeStore) CreateEventDelivery(_ context.Context, input extensions.EventDeliveryInput) (extensions.ExtensionEventDelivery, error) {
	delivery := extensions.ExtensionEventDelivery{
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

func (s *controllerFakeStore) UpdateEventDelivery(_ context.Context, input extensions.EventDeliveryUpdateInput) error {
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
	return extensions.ErrExtensionNotFound
}

func (s *controllerFakeStore) ListEventDeliveries(_ context.Context, input extensions.EventDeliveryListInput) ([]extensions.ExtensionEventDelivery, error) {
	items := []extensions.ExtensionEventDelivery{}
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
