package extensionscontroller

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensiondownload "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionDownload"
	"github.com/keeptrue/sforum/apps/api/config"
)

type fakeArchiveFetcher struct {
	archive extensiondownload.Archive
	err     error
	calls   int
	lastURL string
}

func (f *fakeArchiveFetcher) Fetch(_ context.Context, rawURL string) (extensiondownload.Archive, error) {
	f.calls++
	f.lastURL = rawURL
	return f.archive, f.err
}

type fakeArchiveInstaller struct {
	result extensions.InstallResult
	err    error
	calls  int
	input  extensions.ArchiveInput
}

func (f *fakeArchiveInstaller) InstallOrUpgradeArchive(_ context.Context, _ identity.Actor, input extensions.ArchiveInput) (extensions.InstallResult, error) {
	f.calls++
	f.input = input
	return f.result, f.err
}

func TestInstallFromURLRequiresPermissionBeforeFetch(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	fetcher := &fakeArchiveFetcher{archive: validRemoteArchive()}
	installer := &fakeArchiveInstaller{result: installedResult()}
	controller.remoteArchives = fetcher
	controller.installer = installer

	cookie := loginExtensionUser(t, app, manager, 2)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetcher called %d times for an unauthorized actor", fetcher.calls)
	}
	if installer.calls != 0 {
		t.Fatalf("installer called %d times for an unauthorized actor", installer.calls)
	}
}

func TestInstallFromURLInstallsArchiveWithProvenance(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	archive := validRemoteArchive()
	fetcher := &fakeArchiveFetcher{archive: archive}
	installer := &fakeArchiveInstaller{result: installedResult()}
	controller.remoteArchives = fetcher
	controller.installer = installer

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip","sha256":"`+archive.Digest+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, responseBody(t, resp))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", resp.Header.Get("Cache-Control"))
	}
	var body testEnvelope[extensions.InstallResult]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Extension.ID != "demo.plugin" {
		t.Fatalf("unexpected install result: %#v", body.Data)
	}
	if installer.calls != 1 {
		t.Fatalf("installer called %d times, want 1", installer.calls)
	}
	if installer.input.FileName != archive.FileName || string(installer.input.Data) != string(archive.Data) {
		t.Fatalf("installer input lost archive bytes: %#v", installer.input)
	}
	if installer.input.SourceURL != archive.SourceURL || installer.input.SourceDigest != archive.Digest {
		t.Fatalf("installer input lost provenance: %#v", installer.input)
	}
}

func TestInstallFromURLAllowsPluginOnlyActor(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.remoteArchives = &fakeArchiveFetcher{archive: validRemoteArchive()}
	controller.installer = &fakeArchiveInstaller{result: installedResult()}

	cookie := loginExtensionUser(t, app, manager, 3)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for extension.plugin.manage, got %d", resp.StatusCode)
	}
}

func TestInstallFromURLRejectsChecksumMismatch(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.remoteArchives = &fakeArchiveFetcher{archive: validRemoteArchive()}
	installer := &fakeArchiveInstaller{result: installedResult()}
	controller.installer = installer

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip","sha256":"`+strings.Repeat("0", 64)+`"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	assertErrorReason(t, resp, CodeRemoteDigestMismatch)
	if installer.calls != 0 {
		t.Fatalf("installer called %d times after a digest mismatch", installer.calls)
	}
}

func TestInstallFromURLRejectsMalformedChecksumBeforeFetch(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	fetcher := &fakeArchiveFetcher{archive: validRemoteArchive()}
	controller.remoteArchives = fetcher
	controller.installer = &fakeArchiveInstaller{result: installedResult()}

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip","sha256":"not-a-digest"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	assertErrorReason(t, resp, "validation.invalid")
	if fetcher.calls != 0 {
		t.Fatalf("fetcher called %d times for a malformed checksum", fetcher.calls)
	}
}

func TestInstallFromURLMapsFetchErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		reason string
	}{
		{name: "invalid url", err: extensiondownload.ErrInvalidURL, status: http.StatusUnprocessableEntity, reason: CodeRemoteURLInvalid},
		{name: "too large", err: extensiondownload.ErrTooLarge, status: http.StatusRequestEntityTooLarge, reason: CodeRemoteArchiveTooLarge},
		{name: "not archive", err: extensiondownload.ErrNotArchive, status: http.StatusUnprocessableEntity, reason: extensions.CodeInvalidArchive},
		{name: "fetch failed", err: extensiondownload.ErrFetchFailed, status: http.StatusBadGateway, reason: CodeRemoteFetchFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, manager, controller := newRemoteInstallTestApp(t)
			controller.remoteArchives = &fakeArchiveFetcher{err: test.err}
			controller.installer = &fakeArchiveInstaller{result: installedResult()}

			cookie := loginExtensionUser(t, app, manager, 1)
			resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip"}`)
			if resp.StatusCode != test.status {
				t.Fatalf("expected %d, got %d", test.status, resp.StatusCode)
			}
			assertErrorReason(t, resp, test.reason)
		})
	}
}

func TestInstallFromURLFailsClosedWithoutFetcher(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.installer = &fakeArchiveInstaller{result: installedResult()}

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	assertErrorReason(t, resp, CodeRemoteInstallUnavailable)
}

func TestInstallFromURLRejectsMalformedBody(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	fetcher := &fakeArchiveFetcher{archive: validRemoteArchive()}
	controller.remoteArchives = fetcher
	controller.installer = &fakeArchiveInstaller{result: installedResult()}

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	assertErrorReason(t, resp, "validation.invalid")
	if fetcher.calls != 0 {
		t.Fatalf("fetcher called %d times for a malformed body", fetcher.calls)
	}
}

func TestInstallFromURLPropagatesInstallerErrors(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.remoteArchives = &fakeArchiveFetcher{archive: validRemoteArchive()}
	controller.installer = &fakeArchiveInstaller{err: extensions.ErrNotDeletable}

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	assertErrorReason(t, resp, extensions.CodeNotDeletable)
}

func newRemoteInstallTestApp(t *testing.T) (*fiber.App, *authsession.Manager, *Controller) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {
			ID:          1,
			Status:      identity.UserStatusActive,
			Permissions: map[string]bool{identity.PermissionExtensionManage: true},
		},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		3: {
			ID:          3,
			Status:      identity.UserStatusActive,
			Permissions: map[string]bool{identity.PermissionExtensionPluginManage: true},
		},
	}}
	store := &controllerFakeStore{items: map[string]extensions.Extension{}}
	controller := NewControllerWithGateway(extensions.NewServiceWithHooks(store, t.TempDir(), nil), users, manager, controllerFakeGateway{})
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			id := int64(1)
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
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	return app, manager, controller
}

func validRemoteArchive() extensiondownload.Archive {
	data := []byte("PK\x03\x04demo-archive")
	return extensiondownload.Archive{
		FileName:  "demo.zip",
		Data:      data,
		SourceURL: "https://example.com/demo.zip",
		Digest:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func installedResult() extensions.InstallResult {
	return extensions.InstallResult{
		Extension: extensions.Extension{
			ID:      "demo.plugin",
			Name:    "Demo Plugin",
			Version: "1.0.0",
			Type:    extensions.TypePlugin,
			Status:  extensions.StatusInstalled,
			Source:  extensions.SourceUploaded,
		},
	}
}

func assertErrorReason(t *testing.T, resp *http.Response, reason string) {
	t.Helper()
	defer resp.Body.Close()
	var body testEnvelope[testErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Data.Reason != reason {
		t.Fatalf("error reason = %q, want %q", body.Data.Reason, reason)
	}
}

func TestInstallFromURLPassesDeclaredExpectations(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.remoteArchives = &fakeArchiveFetcher{archive: validRemoteArchive()}
	installer := &fakeArchiveInstaller{result: installedResult()}
	controller.installer = installer

	cookie := loginExtensionUser(t, app, manager, 1)
	body := `{"url":"https://example.com/demo.zip","expectedExtensionId":"Demo.Plugin","expectedVersion":"1.0.0","expectedType":"Plugin","source":"admin-page:sforum.store"}`
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, responseBody(t, resp))
	}
	if installer.input.ExpectedExtensionID != "demo.plugin" ||
		installer.input.ExpectedVersion != "1.0.0" ||
		installer.input.ExpectedType != extensions.TypePlugin {
		t.Fatalf("declared identity not forwarded: %#v", installer.input)
	}
	if installer.input.SourceLabel != "admin-page:sforum.store" {
		t.Fatalf("source label not forwarded: %q", installer.input.SourceLabel)
	}
}

func TestInstallFromURLRejectsInvalidExpectationsBeforeFetch(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown type", body: `{"url":"https://example.com/demo.zip","expectedType":"widget"}`},
		{name: "malformed id", body: `{"url":"https://example.com/demo.zip","expectedExtensionId":"Not An ID!"}`},
		{name: "malformed version", body: `{"url":"https://example.com/demo.zip","expectedVersion":"1.0.0/../x"}`},
		{name: "malformed source", body: `{"url":"https://example.com/demo.zip","source":"admin page"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, manager, controller := newRemoteInstallTestApp(t)
			fetcher := &fakeArchiveFetcher{archive: validRemoteArchive()}
			controller.remoteArchives = fetcher
			controller.installer = &fakeArchiveInstaller{result: installedResult()}

			cookie := loginExtensionUser(t, app, manager, 1)
			resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, test.body)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d", resp.StatusCode)
			}
			assertErrorReason(t, resp, "validation.invalid")
			if fetcher.calls != 0 {
				t.Fatalf("fetcher called %d times for an invalid expectation", fetcher.calls)
			}
		})
	}
}

func TestInstallFromURLMapsExpectationMismatch(t *testing.T) {
	app, manager, controller := newRemoteInstallTestApp(t)
	controller.remoteArchives = &fakeArchiveFetcher{archive: validRemoteArchive()}
	controller.installer = &fakeArchiveInstaller{err: &extensions.ArchiveExpectationMismatchError{
		ExpectedExtensionID: "demo.theme",
		ExpectedType:        extensions.TypeTheme,
		ActualExtensionID:   "demo.plugin",
		ActualVersion:       "1.0.0",
		ActualType:          extensions.TypePlugin,
		Fields:              []string{"extensionId", "type"},
	}}

	cookie := loginExtensionUser(t, app, manager, 1)
	resp := performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/install-from-url", cookie, `{"url":"https://example.com/demo.zip","expectedType":"theme"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body testEnvelope[testErrorDataWithFields]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Data.Reason != extensions.CodeExpectedMismatch {
		t.Fatalf("reason = %q, want %q", body.Data.Reason, extensions.CodeExpectedMismatch)
	}
	if got := body.Data.Fields["manifestId"]; len(got) != 1 || got[0] != "demo.plugin" {
		t.Fatalf("mismatch response should report the actual manifest id: %#v", body.Data.Fields)
	}
}

type testErrorDataWithFields struct {
	Reason string              `json:"reason"`
	Fields map[string][]string `json:"fields"`
}
