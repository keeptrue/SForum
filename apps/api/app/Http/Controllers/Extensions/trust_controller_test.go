package extensionscontroller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/zhuchunshu/sforum/apps/api/app/Http"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	authsession "github.com/zhuchunshu/sforum/apps/api/app/Support/AuthSession"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

func TestExecutableTrustHTTPRequiresExactActorBoundChallenge(t *testing.T) {
	app, sessions, store, _ := newExecutableTrustTestApp(t)
	superCookie := loginExecutableTrustUser(t, app, sessions, 1)
	managerCookie := loginExecutableTrustUser(t, app, sessions, 2)
	otherSuperCookie := loginExecutableTrustUser(t, app, sessions, 3)

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.trust/trust", managerCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delegated impact preview status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/trust/challenge", managerCookie)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("delegated challenge status=%d", response.StatusCode)
	}
	response.Body.Close()

	response = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", superCookie)
	assertExtensionReason(t, response, http.StatusConflict, extensions.CodeTrustChallengeRequired)

	response = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/trust/challenge", superCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issue challenge status=%d", response.StatusCode)
	}
	var challengeBody testEnvelope[extensions.TrustChallenge]
	if err := json.NewDecoder(response.Body).Decode(&challengeBody); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if challengeBody.Data.Token == "" || challengeBody.Data.Impact.ArtifactDigests["backend"] == "" {
		t.Fatalf("incomplete HTTP challenge: %#v", challengeBody.Data)
	}

	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", otherSuperCookie,
		`{"confirmationToken":"`+challengeBody.Data.Token+`"}`)
	assertExtensionReason(t, response, http.StatusForbidden, extensions.CodeTrustChallengeInvalid)

	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", superCookie,
		`{"confirmationToken":"`+challengeBody.Data.Token+`"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confirmed enable status=%d", response.StatusCode)
	}
	response.Body.Close()
	if store.enabledID != "demo.trust" {
		t.Fatalf("enabled extension=%q", store.enabledID)
	}

	response = performExtensionRequest(t, app, http.MethodDelete, "/api/v1/admin/extensions/demo.trust/trust", superCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/disable", managerCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", superCookie,
		`{"confirmationToken":"`+challengeBody.Data.Token+`"}`)
	assertExtensionReason(t, response, http.StatusConflict, extensions.CodeTrustChallengeReplayed)
}

func TestExecutableTrustHTTPRejectsExpiredAndStaleChallenges(t *testing.T) {
	app, sessions, store, trustStore := newExecutableTrustTestApp(t)
	superCookie := loginExecutableTrustUser(t, app, sessions, 1)

	response := performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/trust/challenge", superCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issue expiring challenge status=%d", response.StatusCode)
	}
	var expired testEnvelope[extensions.TrustChallenge]
	if err := json.NewDecoder(response.Body).Decode(&expired); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	trustStore.now = func() time.Time { return expired.Data.ExpiresAt.Add(time.Second) }
	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", superCookie,
		`{"confirmationToken":"`+expired.Data.Token+`"}`)
	assertExtensionReason(t, response, http.StatusConflict, extensions.CodeTrustChallengeExpired)

	trustStore.now = nil
	response = performExtensionRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/trust/challenge", superCookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issue stale challenge status=%d", response.StatusCode)
	}
	var stale testEnvelope[extensions.TrustChallenge]
	if err := json.NewDecoder(response.Body).Decode(&stale); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	changed := store.items["demo.trust"]
	changed.Manifest.Routes = append(changed.Manifest.Routes, extensions.ManifestRoute{
		Path: "/changed", Methods: []string{http.MethodPost}, Access: extensions.RouteAccessLogin,
	})
	store.items[changed.ID] = changed
	response = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.trust/enable", superCookie,
		`{"confirmationToken":"`+stale.Data.Token+`"}`)
	assertExtensionReason(t, response, http.StatusConflict, extensions.CodeTrustChallengeStale)
}

func newExecutableTrustTestApp(t *testing.T) (*fiber.App, *authsession.Manager, *controllerFakeStore, *controllerExecutableTrustStore) {
	t.Helper()
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionPluginManage: true}},
		3: {ID: 3, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
	}}
	extension := executableTrustHTTPFixture(t)
	store := &controllerFakeStore{items: map[string]extensions.Extension{extension.ID: extension}}
	trustStore := &controllerExecutableTrustStore{}
	trust := extensions.NewExecutableTrustService(store, trustStore)
	service := extensions.NewServiceWithOptions(store, t.TempDir(), "", nil, extensions.WithExecutableTrust(trust, true))
	controller := NewController(service, actors, sessions)
	login := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-trust-login/:id", func(c fiber.Ctx) error {
			id := int64(c.Params("id")[0] - '0')
			_, err := sessions.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, login}})
	return app, sessions, store, trustStore
}

func executableTrustHTTPFixture(t *testing.T) extensions.Extension {
	t.Helper()
	manifest := extensions.Manifest{
		ManifestVersion: 3,
		ID:              "demo.trust", Name: "Trust Demo", Description: "Trust HTTP fixture.",
		URL: "https://example.test/trust", Author: extensions.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
		Backend: extensions.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			HostAPIVersion: "sforum.host@2",
		},
	}
	binary := []byte("binary")
	binaryDigest := sha256.Sum256(binary)
	manifest.Backend.Digest = fmt.Sprintf("%x", binaryDigest)
	manifest.PackageFiles = []extensions.ManifestPackageFile{{
		ID: manifest.ID + ".file.backend", Kind: "executable", Path: manifest.Backend.Entry, Digest: manifest.Backend.Digest,
	}}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extensions.ManifestFileName), manifestBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend/plugin"), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Type: extensions.TypePlugin, Status: extensions.StatusInstalled,
		Source: extensions.SourceUploaded, IsDeletable: true, Manifest: manifest,
		PackagePath: root, PackageDigest: digest, InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func loginExecutableTrustUser(t *testing.T, app *fiber.App, _ *authsession.Manager, userID int64) *http.Cookie {
	t.Helper()
	response := performExtensionRequest(t, app, http.MethodPost, "/api/v1/test-trust-login/"+string(rune('0'+userID)), nil)
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login user %d status=%d", userID, response.StatusCode)
	}
	response.Body.Close()
	return response.Cookies()[0]
}

func assertExtensionReason(t *testing.T, response *http.Response, status int, reason string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d", response.StatusCode, status)
	}
	var body testEnvelope[testErrorData]
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Reason != reason {
		t.Fatalf("reason=%q want=%q", body.Data.Reason, reason)
	}
}

type controllerExecutableTrustStore struct {
	challenge extensions.TrustChallengeRecord
	consumed  bool
	granted   map[extensions.TrustIdentity]bool
	now       func() time.Time
}

func (s *controllerExecutableTrustStore) CreateChallenge(_ context.Context, input extensions.TrustChallengeRecord) error {
	s.challenge = input
	s.consumed = false
	return nil
}

func (s *controllerExecutableTrustStore) HasLiveGrant(_ context.Context, identity extensions.TrustIdentity) (bool, error) {
	return s.granted[identity], nil
}

func (s *controllerExecutableTrustStore) LiveGrant(_ context.Context, identity extensions.TrustIdentity) (extensions.TrustGrant, error) {
	if !s.granted[identity] {
		return extensions.TrustGrant{}, extensions.ErrTrustGrantNotFound
	}
	return extensions.TrustGrant{
		ID: 1, ExtensionID: identity.ExtensionID, ExtensionVersion: identity.ExtensionVersion,
		PackageDigest: identity.PackageDigest, Action: identity.Action, ImpactDigest: identity.ImpactDigest,
	}, nil
}

func (s *controllerExecutableTrustStore) ConsumeChallenge(_ context.Context, input extensions.TrustConsumeInput) (extensions.TrustGrant, error) {
	if input.TokenHash != s.challenge.TokenHash || input.ActorUserID != s.challenge.ActorUserID {
		return extensions.TrustGrant{}, extensions.ErrTrustChallengeInvalid
	}
	if s.consumed {
		return extensions.TrustGrant{}, extensions.ErrTrustChallengeReplayed
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now()
	}
	if !now.Before(s.challenge.ExpiresAt) {
		return extensions.TrustGrant{}, extensions.ErrTrustChallengeExpired
	}
	if input.Identity != s.challenge.Identity {
		return extensions.TrustGrant{}, extensions.ErrTrustChallengeStale
	}
	s.consumed = true
	if s.granted == nil {
		s.granted = map[extensions.TrustIdentity]bool{}
	}
	s.granted[input.Identity] = true
	return extensions.TrustGrant{ID: 1, ImpactDigest: input.Identity.ImpactDigest}, nil
}

func (s *controllerExecutableTrustStore) EnsureLiveGrant(_ context.Context, input extensions.TrustEnsureGrantInput) (extensions.TrustGrant, error) {
	if s.granted == nil {
		s.granted = map[extensions.TrustIdentity]bool{}
	}
	s.granted[input.Identity] = true
	return extensions.TrustGrant{
		ID: 1, ExtensionID: input.Identity.ExtensionID, ExtensionVersion: input.Identity.ExtensionVersion,
		PackageDigest: input.Identity.PackageDigest, Action: input.Identity.Action,
		ImpactDigest: input.Identity.ImpactDigest, GrantedByUserID: input.ActorUserID,
	}, nil
}

func (s *controllerExecutableTrustStore) RevokeAll(_ context.Context, extensionID string, _ int64, _ string) error {
	for identity := range s.granted {
		if identity.ExtensionID == extensionID {
			delete(s.granted, identity)
		}
	}
	return nil
}
