package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

type themeE2EDatabase struct {
	name        string
	url         string
	ownerRole   string
	sessionRole string
	adminConfig *pgx.ConnConfig
}

func newThemeE2EDatabase(t *testing.T, databaseURL string) *themeE2EDatabase {
	t.Helper()
	baseConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse SFORUM_TEST_DATABASE_URL: %v", err)
	}
	adminConfig := baseConfig.Copy()
	adminConfig.Database = "postgres"
	delete(adminConfig.RuntimeParams, "role")
	delete(adminConfig.RuntimeParams, "search_path")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect theme restart database administrator: %v", err)
	}
	defer admin.Close(context.Background())

	fixture := &themeE2EDatabase{
		name: fmt.Sprintf("sforum_p8_theme_%d", time.Now().UnixNano()), adminConfig: adminConfig,
	}
	if err := admin.QueryRow(ctx, `SELECT current_user`).Scan(&fixture.sessionRole); err != nil {
		t.Fatal(err)
	}
	fixture.ownerRole, err = coreauthority.OwnerRoleName(fixture.name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{fixture.name}.Sanitize()); err != nil {
		t.Fatalf("create isolated theme restart database: %v", err)
	}
	// Register cleanup before URL verification so every post-CREATE failure drops the database.
	t.Cleanup(func() { fixture.cleanup(t) })
	targetURL, err := url.Parse(databaseURL)
	if err != nil || targetURL.Scheme == "" {
		t.Fatalf("SFORUM_TEST_DATABASE_URL must be a PostgreSQL URL: %v", err)
	}
	targetURL.Path = "/" + fixture.name
	targetQuery := targetURL.Query()
	targetQuery.Del("role")
	targetQuery.Del("search_path")
	targetURL.RawQuery = targetQuery.Encode()
	fixture.url = targetURL.String()
	verification, err := pgx.Connect(ctx, fixture.url)
	if err != nil {
		t.Fatalf("connect isolated theme restart database: %v", err)
	}
	var currentDatabase string
	if err := verification.QueryRow(ctx, `SELECT current_database()`).Scan(&currentDatabase); err != nil {
		verification.Close(context.Background())
		t.Fatal(err)
	}
	verification.Close(context.Background())
	if currentDatabase != fixture.name {
		t.Fatalf("theme restart database=%q, want %q", currentDatabase, fixture.name)
	}
	return fixture
}

func (f *themeE2EDatabase) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, f.adminConfig)
	if err != nil {
		t.Errorf("connect theme restart database cleanup: %v", err)
		return
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{f.name}.Sanitize()+` WITH (FORCE)`); err != nil {
		t.Errorf("drop theme restart database: %v", err)
		return
	}
	owner := pgx.Identifier{f.ownerRole}.Sanitize()
	session := pgx.Identifier{f.sessionRole}.Sanitize()
	_, _ = admin.Exec(ctx, `REVOKE `+owner+` FROM `+session)
	if _, err := admin.Exec(ctx, `DROP ROLE IF EXISTS `+owner); err != nil {
		t.Errorf("drop theme restart Core owner role: %v", err)
	}
}

type themeE2EProcess struct {
	name     string
	command  *exec.Cmd
	done     chan error
	logFile  *os.File
	logPath  string
	exited   bool
	waitErr  error
	stopping bool
}

func startThemeE2EProcess(
	t *testing.T,
	name, workdir string,
	environment []string,
	command string,
	args ...string,
) *themeE2EProcess {
	t.Helper()
	logFile, err := os.Create(filepath.Join(t.TempDir(), name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	process := &themeE2EProcess{
		name: name, done: make(chan error, 1), logFile: logFile, logPath: logFile.Name(),
	}
	process.command = exec.Command(command, args...)
	process.command.Dir = workdir
	process.command.Env = environment
	process.command.Stdout = logFile
	process.command.Stderr = logFile
	if err := process.command.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	go func() { process.done <- process.command.Wait() }()
	t.Cleanup(func() { process.stop(t) })
	return process
}

func (p *themeE2EProcess) pollExit() (bool, error) {
	if p.exited {
		return true, p.waitErr
	}
	select {
	case p.waitErr = <-p.done:
		p.exited = true
		_ = p.logFile.Close()
		return true, p.waitErr
	default:
		return false, nil
	}
}

func (p *themeE2EProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.stopping {
		return
	}
	p.stopping = true
	if exited, _ := p.pollExit(); !exited {
		_ = p.command.Process.Signal(syscall.SIGTERM)
		select {
		case p.waitErr = <-p.done:
			p.exited = true
		case <-time.After(10 * time.Second):
			_ = p.command.Process.Kill()
			p.waitErr = <-p.done
			p.exited = true
		}
	}
	_ = p.logFile.Close()
}

func waitForThemeE2EHTTP(t *testing.T, process *themeE2EProcess, endpoint string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if exited, err := process.pollExit(); exited {
			t.Fatalf("%s exited before %s was ready: %v\n%s", process.name, endpoint, err, process.logTail())
		}
		response, err := themeE2EHTTPClient.Get(endpoint)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			err = fmt.Errorf("status %d", response.StatusCode)
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready at %s: %v\n%s", process.name, endpoint, lastErr, process.logTail())
}

func (p *themeE2EProcess) logTail() string {
	body, err := os.ReadFile(p.logPath)
	if err != nil {
		return "read process log: " + err.Error()
	}
	const limit = 12 << 10
	if len(body) > limit {
		body = body[len(body)-limit:]
	}
	return string(body)
}

func buildThemeE2EArtifacts(
	t *testing.T,
	repositoryRoot, apiBinary, nitroOutput, apiBaseURL, nitroBaseURL, workspace string,
	buildAPI, buildNitro bool,
) {
	t.Helper()
	if buildAPI {
		runThemeE2ECommand(
			t, filepath.Join(repositoryRoot, "apps/api"), nil,
			2*time.Minute, "go", "build", "-o", apiBinary, "./cmd/api",
		)
	}
	if buildNitro {
		buildEnv := themeE2EEnvironment(map[string]string{
			"APP_ENV":                        "testing",
			"APP_URL":                        nitroBaseURL,
			"NUXT_BUILD_DIR":                 filepath.Join(workspace, "nuxt-build"),
			"SFORUM_NITRO_OUTPUT_DIR":        nitroOutput,
			"NUXT_API_INTERNAL_BASE_URL":     apiBaseURL,
			"NUXT_PUBLIC_API_BASE_URL":       "/api/v1",
			"NUXT_PUBLIC_I18N_BASE_URL":      nitroBaseURL,
			"NUXT_PUBLIC_ADMIN_ROUTE_PREFIX": "/admin",
		})
		runThemeE2ECommand(
			t, filepath.Join(repositoryRoot, "apps/web"), buildEnv,
			5*time.Minute, "bun", "run", "build",
		)
	}
	if _, err := os.Stat(apiBinary); err != nil {
		t.Fatalf("API integration binary is unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nitroOutput, "server/index.mjs")); err != nil {
		t.Fatalf("Nitro integration build is unavailable: %v", err)
	}
}

func runThemeE2ECommand(
	t *testing.T,
	workdir string,
	environment []string,
	timeout time.Duration,
	command string,
	args ...string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workdir
	if environment != nil {
		cmd.Env = environment
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		const limit = 20 << 10
		if len(output) > limit {
			output = output[len(output)-limit:]
		}
		t.Fatalf("run %s %s: %v\n%s", command, strings.Join(args, " "), err, output)
	}
}

func prepareThemeE2EBuiltins(t *testing.T, repositoryRoot, builtinRoot string) {
	t.Helper()
	if err := os.CopyFS(builtinRoot, os.DirFS(filepath.Join(repositoryRoot, "extensions/builtin"))); err != nil {
		t.Fatalf("copy protected built-in fixtures: %v", err)
	}
	signalGarden := filepath.Join(builtinRoot, "themes", "sforum-signal-garden")
	if err := os.CopyFS(
		signalGarden,
		os.DirFS(filepath.Join(repositoryRoot, "tests/fixtures/themes/sforum-signal-garden")),
	); err != nil {
		t.Fatalf("copy Signal Garden fixture: %v", err)
	}
	publicL2 := filepath.Join(builtinRoot, "themes", "sforum-public-l2-e2e-theme")
	if err := os.CopyFS(
		publicL2,
		os.DirFS(filepath.Join(repositoryRoot, "extensions/fixtures/themes/sforum-public-l2-e2e-theme")),
	); err != nil {
		t.Fatalf("copy public L2 theme fixture: %v", err)
	}
}

func themeE2EEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func reserveThemeE2EPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func themeE2ERepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve theme restart test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func requireThemeE2ECommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required for the API/Nitro restart integration test", name)
	}
}
