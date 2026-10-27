package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const themeRestartE2EEnv = "SFORUM_THEME_RESTART_E2E"

func TestThemeSwitchSurvivesProductionAPIAndNitroRestartAndConcurrentActivation(t *testing.T) {
	if os.Getenv(themeRestartE2EEnv) != "1" {
		t.Skip(themeRestartE2EEnv + "=1 is required for the API/Nitro restart integration test")
	}
	databaseURL := testdb.RequireIsolatedURL(t)
	requireThemeE2ECommand(t, "go")
	requireThemeE2ECommand(t, "bun")
	requireThemeE2ECommand(t, "node")

	repositoryRoot := themeE2ERepositoryRoot(t)
	workspace := t.TempDir()
	apiPort := reserveThemeE2EPort(t)
	nitroPort := reserveThemeE2EPort(t)
	apiBaseURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1", apiPort)
	nitroBaseURL := fmt.Sprintf("http://127.0.0.1:%d", nitroPort)
	builtinRoot := filepath.Join(workspace, "builtin")
	extensionRoot := filepath.Join(workspace, "extensions")
	prepareThemeE2EBuiltins(t, repositoryRoot, builtinRoot)

	prebuiltAPI := strings.TrimSpace(os.Getenv("SFORUM_THEME_RESTART_API_BINARY"))
	apiBinary := prebuiltAPI
	if prebuiltAPI == "" {
		apiBinary = filepath.Join(workspace, "sforum-api")
	}
	prebuiltNitro := strings.TrimSpace(os.Getenv("SFORUM_THEME_RESTART_NITRO_OUTPUT"))
	nitroOutput := prebuiltNitro
	if prebuiltNitro == "" {
		nitroOutput = filepath.Join(workspace, "nitro")
	}
	buildThemeE2EArtifacts(
		t, repositoryRoot, apiBinary, nitroOutput, apiBaseURL, nitroBaseURL, workspace,
		prebuiltAPI == "", prebuiltNitro == "",
	)

	database := newThemeE2EDatabase(t, databaseURL)
	apiEnv := themeE2EEnvironment(map[string]string{
		"APP_ENV":                "testing",
		"APP_NAME":               "SForum P8 Restart Test",
		"APP_URL":                nitroBaseURL,
		"HTTP_HOST":              "127.0.0.1",
		"HTTP_PORT":              fmt.Sprint(apiPort),
		"DATABASE_URL":           database.url,
		"MIGRATE_ON_STARTUP":     "true",
		"EXTENSION_ROOT":         extensionRoot,
		"BUILTIN_EXTENSION_ROOT": builtinRoot,
		"SFORUM_SAFE_MODE":       "false",
		"CSRF_ENABLED":           "false",
		"LOG_LEVEL":              "warn",
	})
	nitroEnv := themeE2EEnvironment(map[string]string{
		"HOST":                           "127.0.0.1",
		"PORT":                           fmt.Sprint(nitroPort),
		"NITRO_HOST":                     "127.0.0.1",
		"NITRO_PORT":                     fmt.Sprint(nitroPort),
		"NUXT_API_INTERNAL_BASE_URL":     apiBaseURL,
		"NUXT_PUBLIC_API_BASE_URL":       "/api/v1",
		"NUXT_PUBLIC_I18N_BASE_URL":      nitroBaseURL,
		"NUXT_PUBLIC_ADMIN_ROUTE_PREFIX": "/admin",
	})

	api := startThemeE2EProcess(t, "api-1", filepath.Join(repositoryRoot, "apps/api"), apiEnv, apiBinary)
	waitForThemeE2EHTTP(t, api, apiBaseURL+"/health", 90*time.Second)

	pool, err := pgxpool.New(context.Background(), database.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := extensions.NewPostgresStore(pool)
	actorID := createThemeE2EActor(t, pool)
	defaultTheme := getThemeE2EExtension(t, store, extensions.DefaultThemeID)
	publicL2 := getThemeE2EExtension(t, store, "sforum.public-l2-e2e-theme")
	signalGarden := getThemeE2EExtension(t, store, "sforum.signal-garden")
	assertThemeE2EActive(t, store, defaultTheme)
	waitForThemeE2ESkin(t, api, pool, apiBaseURL, defaultTheme, 20*time.Second)

	nitro := startThemeE2EProcess(
		t, "nitro-1", filepath.Join(repositoryRoot, "apps/web"), nitroEnv,
		"node", filepath.Join(nitroOutput, "server/index.mjs"),
	)
	waitForThemeE2EHTTP(t, nitro, nitroBaseURL+"/", 45*time.Second)
	assertThemeE2EDefaultThemeRendered(t, nitroBaseURL)

	current := assertThemeE2EActive(t, store, defaultTheme)
	activateThemeE2EExact(t, store, current, signalGarden, actorID)
	waitForThemeE2ESkin(t, api, pool, apiBaseURL, signalGarden, 20*time.Second)
	assertThemeE2ERendered(t, nitroBaseURL, signalGarden)

	// The selected exact artifact must survive both process-local runtime rebuilds.
	nitro.stop(t)
	api.stop(t)
	api = startThemeE2EProcess(t, "api-2", filepath.Join(repositoryRoot, "apps/api"), apiEnv, apiBinary)
	waitForThemeE2EHTTP(t, api, apiBaseURL+"/health", 60*time.Second)
	waitForThemeE2ESkin(t, api, pool, apiBaseURL, signalGarden, 20*time.Second)
	assertThemeE2EActive(t, store, signalGarden)
	nitro = startThemeE2EProcess(
		t, "nitro-2", filepath.Join(repositoryRoot, "apps/web"), nitroEnv,
		"node", filepath.Join(nitroOutput, "server/index.mjs"),
	)
	waitForThemeE2EHTTP(t, nitro, nitroBaseURL+"/", 45*time.Second)
	assertThemeE2ERendered(t, nitroBaseURL, signalGarden)
	t.Logf("exact switch survived API and Nitro restart: %s@%s#%s",
		signalGarden.ID, signalGarden.Version, signalGarden.PackageDigest)

	winner := raceThemeE2EActivations(t, store, signalGarden, defaultTheme, publicL2, actorID)
	waitForThemeE2ESkin(t, api, pool, apiBaseURL, winner, 20*time.Second)
	assertThemeE2ERendered(t, nitroBaseURL, winner)
	t.Logf("concurrent exact activation winner: %s@%s#%s", winner.ID, winner.Version, winner.PackageDigest)

	// Restart once more after the race so a stale Nitro payload or startup default
	// cannot hide a lost concurrent winner.
	nitro.stop(t)
	api.stop(t)
	api = startThemeE2EProcess(t, "api-3", filepath.Join(repositoryRoot, "apps/api"), apiEnv, apiBinary)
	waitForThemeE2EHTTP(t, api, apiBaseURL+"/health", 60*time.Second)
	waitForThemeE2ESkin(t, api, pool, apiBaseURL, winner, 20*time.Second)
	assertThemeE2EActive(t, store, winner)
	nitro = startThemeE2EProcess(
		t, "nitro-3", filepath.Join(repositoryRoot, "apps/web"), nitroEnv,
		"node", filepath.Join(nitroOutput, "server/index.mjs"),
	)
	waitForThemeE2EHTTP(t, nitro, nitroBaseURL+"/", 45*time.Second)
	assertThemeE2ERendered(t, nitroBaseURL, winner)
	t.Logf("concurrent winner survived final API and Nitro restart: %s@%s#%s",
		winner.ID, winner.Version, winner.PackageDigest)
	if holdValue := strings.TrimSpace(os.Getenv("SFORUM_THEME_RESTART_BROWSER_HOLD")); holdValue != "" {
		hold, err := time.ParseDuration(holdValue)
		if err != nil || hold <= 0 {
			t.Fatalf("invalid SFORUM_THEME_RESTART_BROWSER_HOLD=%q", holdValue)
		}
		t.Logf("isolated browser QA ready at %s for %s", nitroBaseURL, hold)
		timer := time.NewTimer(hold)
		defer timer.Stop()
		<-timer.C
	}
}

func activateThemeE2EExact(
	t *testing.T,
	store *extensions.PostgresStore,
	current, target extensions.Extension,
	actorID int64,
) extensions.ThemeActivationResult {
	t.Helper()
	result, err := store.ActivateThemeExact(context.Background(), target.ID, themeE2EActivationInput(current, target, actorID))
	if err != nil {
		t.Fatalf("activate exact theme %s: %v", target.ID, err)
	}
	return result
}

func raceThemeE2EActivations(
	t *testing.T,
	store *extensions.PostgresStore,
	current, left, right extensions.Extension,
	actorID int64,
) extensions.Extension {
	t.Helper()
	type outcome struct {
		result extensions.ThemeActivationResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, target := range []extensions.Extension{left, right} {
		go func(candidate extensions.Extension) {
			<-start
			result, err := store.ActivateThemeExact(
				context.Background(), candidate.ID, themeE2EActivationInput(current, candidate, actorID),
			)
			results <- outcome{result: result, err: err}
		}(target)
	}
	close(start)

	var winner extensions.Extension
	var succeeded, stale int
	for range 2 {
		outcome := <-results
		switch {
		case outcome.err == nil:
			succeeded++
			winner = outcome.result.Extension
		case errors.Is(outcome.err, extensions.ErrThemePreviewStale):
			stale++
		default:
			t.Fatalf("concurrent exact activation: %v", outcome.err)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("concurrent exact activation outcomes: succeeded=%d stale=%d", succeeded, stale)
	}
	return assertThemeE2EActive(t, store, winner)
}

func themeE2EActivationInput(current, target extensions.Extension, actorID int64) extensions.ThemeActivationInput {
	return extensions.ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version,
		CurrentThemeDigest: current.PackageDigest, ActorUserID: actorID,
		ApproveCoreReplacements: true,
	}
}

func createThemeE2EActor(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	username := fmt.Sprintf("p8_theme_actor_%d", time.Now().UnixNano())
	var actorID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'P8 Theme Actor') RETURNING id
	`, username, username+"@example.test").Scan(&actorID); err != nil {
		t.Fatalf("create exact theme activation actor: %v", err)
	}
	return actorID
}

func getThemeE2EExtension(t *testing.T, store *extensions.PostgresStore, id string) extensions.Extension {
	t.Helper()
	extension, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("load theme %s: %v", id, err)
	}
	return extension
}

func assertThemeE2EActive(
	t *testing.T,
	store *extensions.PostgresStore,
	want extensions.Extension,
) extensions.Extension {
	t.Helper()
	active, err := store.ActiveTheme(context.Background())
	if err != nil || !sameThemeE2EArtifact(active, want) {
		t.Fatalf("active theme=%s@%s#%s, want %s@%s#%s: %v",
			active.ID, active.Version, active.PackageDigest,
			want.ID, want.Version, want.PackageDigest, err,
		)
	}
	return active
}

func waitForThemeE2ESkin(
	t *testing.T,
	api *themeE2EProcess,
	pool *pgxpool.Pool,
	apiBaseURL string,
	want extensions.Extension,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		skin, err := fetchThemeE2ESkin(apiBaseURL + "/site/active-theme/skin")
		if err == nil && skin.ExtensionID == want.ID && skin.Version == want.Version &&
			strings.EqualFold(skin.PackageDigest, want.PackageDigest) {
			return
		}
		last = fmt.Sprintf("skin=%#v err=%v", skin, err)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("theme runtime did not converge to %s@%s#%s: %s\n%s\n%s",
		want.ID, want.Version, want.PackageDigest, last,
		themeE2EPublicationDiagnostics(pool), api.logTail(),
	)
}

func themeE2EPublicationDiagnostics(pool *pgxpool.Pool) string {
	if pool == nil {
		return "theme publication diagnostics unavailable"
	}
	rows, err := pool.Query(context.Background(), `
		SELECT nodes.node_id, nodes.boot_id, nodes.last_applied_revision,
		       COALESCE(acks.status, ''), COALESCE(acks.attempt_count, 0),
		       COALESCE(acks.error_reason, '')
		FROM theme_runtime_nodes AS nodes
		LEFT JOIN LATERAL (
			SELECT status, attempt_count, error_reason
			FROM theme_runtime_publication_acks
			WHERE node_id = nodes.node_id AND boot_id = nodes.boot_id
			ORDER BY publication_revision DESC LIMIT 1
		) AS acks ON TRUE
		ORDER BY nodes.node_id, nodes.boot_id
	`)
	if err != nil {
		return "load theme publication diagnostics: " + err.Error()
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var nodeID, bootID, status, reason string
		var revision int64
		var attempts int
		if err := rows.Scan(&nodeID, &bootID, &revision, &status, &attempts, &reason); err != nil {
			return "scan theme publication diagnostics: " + err.Error()
		}
		lines = append(lines, fmt.Sprintf("node=%s boot=%s applied=%d status=%s attempts=%d failure=%s",
			nodeID, bootID, revision, status, attempts, reason,
		))
	}
	if err := rows.Err(); err != nil {
		return "iterate theme publication diagnostics: " + err.Error()
	}
	if len(lines) == 0 {
		return "no theme runtime nodes registered"
	}
	return strings.Join(lines, "\n")
}

func fetchThemeE2ESkin(endpoint string) (themeE2ESkin, error) {
	response, err := themeE2EHTTPClient.Get(endpoint)
	if err != nil {
		return themeE2ESkin{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return themeE2ESkin{}, fmt.Errorf("status %d", response.StatusCode)
	}
	var envelope struct {
		Data themeE2ESkin `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return themeE2ESkin{}, err
	}
	return envelope.Data, nil
}

func assertThemeE2ERendered(t *testing.T, nitroBaseURL string, want extensions.Extension) {
	t.Helper()
	html := fetchThemeE2EHTML(t, nitroBaseURL)
	markers := []string{`data-page="forum.home"`, `data-extension-id="` + want.ID + `"`}
	switch want.ID {
	case "sforum.signal-garden":
		markers = append(markers, "Signal Garden", `data-theme="signal-garden"`)
	}
	for _, marker := range markers {
		if !strings.Contains(html, marker) {
			t.Fatalf("Nitro home did not render %s; missing %q", want.ID, marker)
		}
	}
}

// 全新安装首启必须直接渲染 Host 自带的受保护默认主题：Host 在启动修复中
// 批准核心页替换，不需要任何管理员先点一次「重新激活」。
func assertThemeE2EDefaultThemeRendered(t *testing.T, nitroBaseURL string) {
	t.Helper()
	html := fetchThemeE2EHTML(t, nitroBaseURL)
	for _, marker := range []string{
		`data-page="forum.home"`,
		`data-provider="` + extensions.DefaultThemeID + `"`,
		`data-template="1"`,
	} {
		if !strings.Contains(html, marker) {
			t.Fatalf("initial Nitro home did not render the protected default theme; missing %q", marker)
		}
	}
}

func fetchThemeE2EHTML(t *testing.T, nitroBaseURL string) string {
	t.Helper()
	response, err := themeE2EHTTPClient.Get(nitroBaseURL + "/")
	if err != nil {
		t.Fatalf("render Nitro home: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("render Nitro home: status=%d err=%v", response.StatusCode, err)
	}
	return string(body)
}

func sameThemeE2EArtifact(left, right extensions.Extension) bool {
	return left.ID == right.ID && left.Version == right.Version &&
		strings.EqualFold(left.PackageDigest, right.PackageDigest)
}

type themeE2ESkin struct {
	ExtensionID   string `json:"extensionId"`
	Version       string `json:"version"`
	PackageDigest string `json:"packageDigest"`
}

var themeE2EHTTPClient = &http.Client{Timeout: 5 * time.Second}
