package bootstrap

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPublicL2ProductionFixtureCompilesExactRuntime(t *testing.T) {
	repositoryRoot := themeE2ERepositoryRoot(t)
	fixtureRoot := filepath.Join(repositoryRoot, publicL2FixtureDir)
	assertPublicL2Fixture(t, fixtureRoot)
	manifest, err := extensionmanifest.LoadPackage(fixtureRoot)
	if err != nil {
		t.Fatalf("load exact public L2 fixture: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	extension := extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: extensions.StatusInstalled, Source: extensions.SourceUploaded, Manifest: manifest,
		PackagePath: fixtureRoot, PackageDigest: packageDigest,
	}
	if !extensions.RequiresExecutableTrust(extension) {
		t.Fatal("public L2 fixture did not request exact executable trust")
	}
	adapter := extensions.NewPageRegistryAdapter(pages.NewRegistry(pages.NewMemoryStore())).
		WithThemeRuntime(pages.NewThemeRuntimeRegistry(), "SForum", []string{"zh-CN", "en-US"})
	if err := adapter.PreflightThemePackage(context.Background(), extension, extensions.DefaultThemeID); err != nil {
		t.Fatalf("compile exact public L2 production runtime: %v", err)
	}
}

const (
	publicL2ProductionE2EEnv = "SFORUM_PUBLIC_L2_E2E"
	publicL2FixtureID        = "sforum.public-l2-e2e-theme"
	publicL2ComponentID      = publicL2FixtureID + ".component.card"
	publicL2FixtureDir       = "extensions/fixtures/themes/sforum-public-l2-e2e-theme"
	publicL2FallbackText     = "P9 public L2 SSR fallback"
)

// TestPublicL2UploadTrustActivateMountRestartAndRevokeFallback is intentionally
// opt-in: it builds the production Host once, then proves that an author-built
// package is uploaded and executed without any package-local build step.
func TestPublicL2UploadTrustActivateMountRestartAndRevokeFallback(t *testing.T) {
	if os.Getenv(publicL2ProductionE2EEnv) != "1" {
		t.Skip(publicL2ProductionE2EEnv + "=1 is required for the production browser integration test")
	}
	databaseURL := testdb.RequireIsolatedURL(t)
	for _, command := range []string{"go", "bun", "node", "npx"} {
		requireThemeE2ECommand(t, command)
	}

	repositoryRoot := themeE2ERepositoryRoot(t)
	fixtureRoot := filepath.Join(repositoryRoot, publicL2FixtureDir)
	assertPublicL2Fixture(t, fixtureRoot)
	workspace := t.TempDir()
	apiPort := reservePublicL2E2EPort(t)
	nitroPort := reservePublicL2E2EPortExcept(t, apiPort)
	apiBaseURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1", apiPort)
	nitroBaseURL := fmt.Sprintf("http://127.0.0.1:%d", nitroPort)
	builtinRoot := filepath.Join(workspace, "builtin")
	extensionRoot := filepath.Join(workspace, "extensions")
	prepareThemeE2EBuiltins(t, repositoryRoot, builtinRoot)

	prebuiltAPI := strings.TrimSpace(os.Getenv("SFORUM_THEME_RESTART_API_BINARY"))
	apiBinary := prebuiltAPI
	if apiBinary == "" {
		apiBinary = filepath.Join(workspace, "sforum-api")
	}
	prebuiltNitro := strings.TrimSpace(os.Getenv("SFORUM_THEME_RESTART_NITRO_OUTPUT"))
	nitroOutput := prebuiltNitro
	if nitroOutput == "" {
		nitroOutput = filepath.Join(workspace, "nitro")
	}
	buildThemeE2EArtifacts(
		t, repositoryRoot, apiBinary, nitroOutput, apiBaseURL, nitroBaseURL, workspace,
		prebuiltAPI == "", prebuiltNitro == "",
	)

	database := newThemeE2EDatabase(t, databaseURL)
	apiEnv := themeE2EEnvironment(map[string]string{
		"APP_ENV":                     "testing",
		"APP_NAME":                    "SForum P9 Public L2 Test",
		"APP_URL":                     nitroBaseURL,
		"HTTP_HOST":                   "127.0.0.1",
		"HTTP_PORT":                   fmt.Sprint(apiPort),
		"DATABASE_URL":                database.url,
		"MIGRATE_ON_STARTUP":          "true",
		"EXTENSION_ROOT":              extensionRoot,
		"BUILTIN_EXTENSION_ROOT":      builtinRoot,
		"SFORUM_SAFE_MODE":            "false",
		"SFORUM_V3_TRUST_CHALLENGES":  "true",
		"HUMAN_VERIFICATION_PROVIDER": "disabled",
		"CSRF_ENABLED":                "false",
		"LOG_LEVEL":                   "warn",
	})
	nitroEnv := themeE2EEnvironment(map[string]string{
		"APP_URL":                        nitroBaseURL,
		"HOST":                           "127.0.0.1",
		"PORT":                           fmt.Sprint(nitroPort),
		"NITRO_HOST":                     "127.0.0.1",
		"NITRO_PORT":                     fmt.Sprint(nitroPort),
		"NUXT_API_INTERNAL_BASE_URL":     apiBaseURL,
		"NUXT_PUBLIC_API_BASE_URL":       "/api/v1",
		"NUXT_PUBLIC_I18N_BASE_URL":      nitroBaseURL,
		"NUXT_PUBLIC_ADMIN_ROUTE_PREFIX": "/admin",
		"NUXT_PUBLIC_SITE_URL":           nitroBaseURL,
		"NUXT_SITE_URL":                  nitroBaseURL,
	})

	api := startThemeE2EProcess(t, "p9-api-1", filepath.Join(repositoryRoot, "apps/api"), apiEnv, apiBinary)
	waitForThemeE2EHTTP(t, api, apiBaseURL+"/health", 90*time.Second)
	client := newPublicL2AdminClient(t, apiBaseURL)
	client.failureLog = api.logTail
	client.registerInitialSuperAdmin(t)
	archive := publicL2FixtureArchive(t, fixtureRoot)
	installed := client.uploadTheme(t, archive)
	if installed.ID != publicL2FixtureID || installed.Source != extensions.SourceUploaded || installed.Status == extensions.StatusEnabled {
		t.Fatalf("uploaded public L2 fixture = %#v", installed)
	}
	preview := client.activationPreview(t, publicL2FixtureID)
	challenge := client.executableTrustChallenge(t, publicL2FixtureID)
	active := client.activateTheme(t, preview, challenge.Token)
	if active.ID != publicL2FixtureID || active.Status != extensions.StatusEnabled || active.PackageDigest != preview.PackageDigest {
		t.Fatalf("active public L2 fixture = %#v, preview=%#v", active, preview)
	}
	client.assertPublicDescriptor(t, publicL2FixtureID, publicL2ComponentID, http.StatusOK)

	// Start Nitro only after activation so no stale root payload can masquerade
	// as a successful exact theme publication.
	nitro := startThemeE2EProcess(
		t, "p9-nitro-1", filepath.Join(repositoryRoot, "apps/web"), nitroEnv,
		"node", filepath.Join(nitroOutput, "server/index.mjs"),
	)
	waitForThemeE2EHTTP(t, nitro, nitroBaseURL+"/health", 45*time.Second)
	assertPublicL2SSR(t, nitroBaseURL)

	browser := startPublicL2Browser(t, repositoryRoot, nitroBaseURL)
	browser.run(t, filepath.Join(repositoryRoot, "apps/api/bootstrap/testdata/public_l2_browser_mount.js"))

	// Exact trust and the active theme must survive replacement of both Host
	// processes. The browser itself stays alive so this also catches stale module
	// and stylesheet lease behavior across a real outage/reload.
	nitro.stop(t)
	api.stop(t)
	api = startThemeE2EProcess(t, "p9-api-2", filepath.Join(repositoryRoot, "apps/api"), apiEnv, apiBinary)
	waitForThemeE2EHTTP(t, api, apiBaseURL+"/health", 60*time.Second)
	client.failureLog = api.logTail
	client.assertPublicDescriptor(t, publicL2FixtureID, publicL2ComponentID, http.StatusOK)
	nitro = startThemeE2EProcess(
		t, "p9-nitro-2", filepath.Join(repositoryRoot, "apps/web"), nitroEnv,
		"node", filepath.Join(nitroOutput, "server/index.mjs"),
	)
	waitForThemeE2EHTTP(t, nitro, nitroBaseURL+"/health", 45*time.Second)
	assertPublicL2SSR(t, nitroBaseURL)
	browser.run(t, filepath.Join(repositoryRoot, "apps/api/bootstrap/testdata/public_l2_browser_mount.js"))

	client.revokeExecutableTrust(t, publicL2FixtureID)
	client.assertPublicDescriptor(t, publicL2FixtureID, publicL2ComponentID, http.StatusNotFound)
	assertPublicL2SSR(t, nitroBaseURL)
	browser.run(t, filepath.Join(repositoryRoot, "apps/api/bootstrap/testdata/public_l2_browser_fallback.js"))
	browser.close(t)
	t.Logf("P9 public L2 exact artifact passed upload, trust, mount, restart, and revoke fallback: %s@%s#%s",
		active.ID, active.Version, active.PackageDigest)
}

type publicL2APIEnvelope[T any] struct {
	Data T `json:"data"`
}

type publicL2ActivationPreview struct {
	Version                         string `json:"version"`
	PackageDigest                   string `json:"packageDigest"`
	CurrentThemeID                  string `json:"currentThemeId"`
	CurrentThemeVersion             string `json:"currentThemeVersion"`
	CurrentThemeDigest              string `json:"currentThemeDigest"`
	CanActivate                     bool   `json:"canActivate"`
	CanApproveCoreReplacements      bool   `json:"canApproveCoreReplacements"`
	RequiresCoreReplacementApproval bool   `json:"requiresCoreReplacementApproval"`
}

type publicL2AdminClient struct {
	baseURL    string
	client     *http.Client
	failureLog func() string
}

func newPublicL2AdminClient(t *testing.T, baseURL string) *publicL2AdminClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &publicL2AdminClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}
}

func (c *publicL2AdminClient) registerInitialSuperAdmin(t *testing.T) {
	t.Helper()
	payload := map[string]any{
		"username":    "p9_l2_admin",
		"email":       "p9_l2_admin@example.test",
		"password":    "SForum-P9-E2E-Strong-Password!123",
		"displayName": "P9 Public L2",
		"locale":      "en-US",
	}
	c.doJSON(t, http.MethodPost, "/auth/register", payload, http.StatusCreated, &struct{}{})
	parsed, err := url.Parse(c.baseURL)
	if err != nil || len(c.client.Jar.Cookies(parsed)) == 0 {
		t.Fatalf("registration did not issue an admin session cookie: %v", err)
	}
}

func (c *publicL2AdminClient) uploadTheme(t *testing.T, archive []byte) extensions.Extension {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "sforum-public-l2-e2e-theme.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+"/admin/extensions", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	var envelope publicL2APIEnvelope[extensions.InstallResult]
	c.do(t, request, http.StatusCreated, &envelope)
	return envelope.Data.Extension
}

func (c *publicL2AdminClient) activationPreview(t *testing.T, extensionID string) publicL2ActivationPreview {
	t.Helper()
	var envelope publicL2APIEnvelope[publicL2ActivationPreview]
	c.doJSON(t, http.MethodGet, "/admin/pages/activate-preview/"+url.PathEscape(extensionID), nil, http.StatusOK, &envelope)
	if !envelope.Data.CanActivate || !envelope.Data.CanApproveCoreReplacements || envelope.Data.PackageDigest == "" {
		t.Fatalf("public L2 activation preview = %#v", envelope.Data)
	}
	return envelope.Data
}

func (c *publicL2AdminClient) executableTrustChallenge(t *testing.T, extensionID string) extensions.TrustChallenge {
	t.Helper()
	var envelope publicL2APIEnvelope[extensions.TrustChallenge]
	c.doJSON(t, http.MethodPost, "/admin/extensions/"+url.PathEscape(extensionID)+"/trust/challenge", map[string]any{}, http.StatusOK, &envelope)
	if envelope.Data.Token == "" || envelope.Data.Impact.ExtensionID != extensionID || envelope.Data.Impact.Digest == "" {
		t.Fatalf("public L2 exact trust challenge = %#v", envelope.Data)
	}
	return envelope.Data
}

func (c *publicL2AdminClient) activateTheme(
	t *testing.T,
	preview publicL2ActivationPreview,
	confirmationToken string,
) extensions.Extension {
	t.Helper()
	payload := map[string]any{
		"version": preview.Version, "packageDigest": preview.PackageDigest,
		"currentThemeId": preview.CurrentThemeID, "currentThemeVersion": preview.CurrentThemeVersion,
		"currentThemeDigest": preview.CurrentThemeDigest, "confirmationToken": confirmationToken,
		"approveCoreReplacements": preview.RequiresCoreReplacementApproval && preview.CanApproveCoreReplacements,
	}
	var envelope publicL2APIEnvelope[extensions.Extension]
	c.doJSON(t, http.MethodPost, "/admin/extensions/"+url.PathEscape(publicL2FixtureID)+"/activate", payload, http.StatusOK, &envelope)
	return envelope.Data
}

func (c *publicL2AdminClient) revokeExecutableTrust(t *testing.T, extensionID string) {
	t.Helper()
	var envelope publicL2APIEnvelope[extensions.ExecutableTrustStatus]
	c.doJSON(t, http.MethodDelete, "/admin/extensions/"+url.PathEscape(extensionID)+"/trust", nil, http.StatusOK, &envelope)
	if envelope.Data.Trusted {
		t.Fatalf("public L2 exact trust remains live after revoke: %#v", envelope.Data)
	}
}

func (c *publicL2AdminClient) assertPublicDescriptor(
	t *testing.T,
	extensionID, componentID string,
	wantStatus int,
) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, c.baseURL+"/extensions/runtime/"+
		url.PathEscape(extensionID)+"/components/"+url.PathEscape(componentID), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("public L2 descriptor status=%d, want=%d body=%s", response.StatusCode, wantStatus, body)
	}
}

func (c *publicL2AdminClient) doJSON(
	t *testing.T,
	method, path string,
	payload any,
	wantStatus int,
	output any,
) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	c.do(t, request, wantStatus, output)
}

func (c *publicL2AdminClient) do(t *testing.T, request *http.Request, wantStatus int, output any) {
	t.Helper()
	response, err := c.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		log := ""
		if c.failureLog != nil {
			log = "\nAPI log tail:\n" + c.failureLog()
		}
		t.Fatalf("%s %s status=%d, want=%d body=%s%s", request.Method, request.URL, response.StatusCode, wantStatus, body, log)
	}
	if output != nil && len(body) > 0 {
		if err := json.Unmarshal(body, output); err != nil {
			t.Fatalf("decode %s %s: %v body=%s", request.Method, request.URL, err, body)
		}
	}
}

func publicL2FixtureArchive(t *testing.T, root string) []byte {
	t.Helper()
	entries := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range entries {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func assertPublicL2Fixture(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{
		"sforum.extension.json", "theme.json", "templates/home.html",
		"frontend/public/card.mjs", "frontend/public/chunk.mjs",
		"frontend/public/card.css", "frontend/public/nested.css",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("public L2 fixture file %s is unavailable: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "package.json")); !os.IsNotExist(err) {
		t.Fatal("public L2 fixture must be author-prebuilt and must not require package installation")
	}
}

func assertPublicL2SSR(t *testing.T, baseURL string) {
	t.Helper()
	response, err := (&http.Client{Timeout: 30 * time.Second}).Get(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(publicL2FallbackText)) ||
		bytes.Contains(body, []byte("data-public-l2-mounted")) {
		t.Fatalf("public L2 SSR status=%d fallback=%t mounted=%t body=%s",
			response.StatusCode, bytes.Contains(body, []byte(publicL2FallbackText)),
			bytes.Contains(body, []byte("data-public-l2-mounted")), truncatePublicL2Body(body))
	}
}

func reservePublicL2E2EPort(t *testing.T) int {
	t.Helper()
	for {
		port := reserveThemeE2EPort(t)
		if port != 3000 {
			return port
		}
	}
}

func reservePublicL2E2EPortExcept(t *testing.T, excluded int) int {
	t.Helper()
	for {
		port := reservePublicL2E2EPort(t)
		if port != excluded {
			return port
		}
	}
}

func truncatePublicL2Body(body []byte) string {
	const limit = 2 << 10
	if len(body) > limit {
		body = body[:limit]
	}
	return string(body)
}
