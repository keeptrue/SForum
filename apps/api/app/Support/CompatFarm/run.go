// Package compatfarm loads and executes the supported compatibility matrix.
// Every cell must start a real Host/plugin process and perform at least one RPC.
package compatfarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

// CellOutcome is the machine status of one executed cell.
type CellOutcome string

const (
	OutcomePass    CellOutcome = "pass"
	OutcomeFail    CellOutcome = "fail"
	OutcomeSkip    CellOutcome = "skip"
	OutcomeMissing CellOutcome = "missing"
)

// CellEvidence records start/request/response/shim proof for one cell.
// 缺少任一证据时门禁必须失败。
type CellEvidence struct {
	ProcessStarted bool   `json:"processStarted"`
	Request        string `json:"request,omitempty"`
	Response       string `json:"response,omitempty"`
	ShimCalls      uint64 `json:"shimCalls,omitempty"`
	PluginPath     string `json:"pluginPath,omitempty"`
	Protocol       int    `json:"protocol,omitempty"`
	DatabaseOK     bool   `json:"databaseOk,omitempty"`
}

// CellResult is one cell's execution report.
type CellResult struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"` // required|deprecated from matrix
	Outcome   CellOutcome   `json:"outcome"`
	Duration  time.Duration `json:"duration"`
	Message   string        `json:"message,omitempty"`
	ShimCalls uint64        `json:"shimCalls,omitempty"`
	Evidence  CellEvidence  `json:"evidence,omitempty"`
}

// RunResult is the full farm report.
type RunResult struct {
	SchemaVersion string       `json:"schemaVersion"`
	MatrixPath    string       `json:"matrixPath"`
	Cells         []CellResult `json:"cells"`
	// OK is true only when every required cell passed and no required cell was
	// skipped/missing/failed. Deprecated cells that fail also fail the gate.
	OK bool `json:"ok"`
}

// RunOptions controls farm execution.
type RunOptions struct {
	// RepoRoot is the monorepo root (contains tests/compat and extensions/).
	RepoRoot string
	// LTS is optional; when nil a fresh process registry is used for shim proof.
	LTS *apilts.Registry
	// BuiltinPluginPath overrides the default builtin plugin used for contracts.
	BuiltinPluginPath string
	// DatabaseURL for cells that declare database=postgres.
	DatabaseURL string
	// Now is injectable for tests.
	Now func() time.Time
	// WorkDir is where temporary plugin builds land (defaults to os.TempDir).
	WorkDir string
}

// RunMatrix loads path and executes every cell. Missing matrix, empty required
// set, skip, or fail makes OK false (CI must fail).
func RunMatrix(ctx context.Context, matrixPath string, opts RunOptions) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	matrixPath = strings.TrimSpace(matrixPath)
	if matrixPath == "" {
		return RunResult{OK: false}, fmt.Errorf("compat farm: matrix path is required")
	}
	if _, err := os.Stat(matrixPath); err != nil {
		return RunResult{
			SchemaVersion: SchemaVersion,
			MatrixPath:    matrixPath,
			OK:            false,
			Cells: []CellResult{{
				ID: "_matrix", Outcome: OutcomeMissing,
				Message: fmt.Sprintf("matrix file missing: %v", err),
			}},
		}, fmt.Errorf("compat farm: matrix missing: %w", err)
	}
	matrix, err := LoadMatrix(matrixPath)
	if err != nil {
		return RunResult{SchemaVersion: SchemaVersion, MatrixPath: matrixPath, OK: false}, err
	}
	if len(matrix.RequiredCells()) == 0 {
		return RunResult{
			SchemaVersion: SchemaVersion, MatrixPath: matrixPath, OK: false,
			Cells: []CellResult{{
				ID: "_required", Outcome: OutcomeFail,
				Message: "matrix has zero required cells",
			}},
		}, fmt.Errorf("compat farm: zero required cells")
	}

	root := strings.TrimSpace(opts.RepoRoot)
	if root == "" {
		root = guessRepoRoot(matrixPath)
	}
	lts := opts.LTS
	if lts == nil {
		lts = apilts.New()
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir == "" {
		workDir = os.TempDir()
	}
	dbURL := strings.TrimSpace(opts.DatabaseURL)
	if dbURL == "" {
		dbURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
		if dbURL == "" {
			dbURL = strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
		}
	}

	result := RunResult{
		SchemaVersion: SchemaVersion,
		MatrixPath:    matrixPath,
		OK:            true,
	}
	for _, cell := range matrix.Cells {
		if err := ctx.Err(); err != nil {
			result.OK = false
			result.Cells = append(result.Cells, CellResult{
				ID: cell.ID, Status: cell.Status, Outcome: OutcomeFail,
				Message: err.Error(),
			})
			return result, err
		}
		started := now()
		cr := executeCell(ctx, cell, root, lts, opts.BuiltinPluginPath, workDir, dbURL)
		cr.Duration = now().Sub(started)
		// 证据完整性：通过的 cell 必须具备 process + request + response。
		if cr.Outcome == OutcomePass && !evidenceComplete(cr) {
			cr.Outcome = OutcomeFail
			cr.Message = "missing evidence (process/request/response): " + cr.Message
		}
		if cr.Outcome != OutcomePass {
			result.OK = false
		}
		// 门禁：required 与 deprecated 均不得 skip/missing；skip 一律失败。
		if cr.Outcome == OutcomeSkip || cr.Outcome == OutcomeMissing {
			result.OK = false
		}
		result.Cells = append(result.Cells, cr)
	}
	return result, nil
}

func evidenceComplete(cr CellResult) bool {
	if !cr.Evidence.ProcessStarted {
		return false
	}
	if strings.TrimSpace(cr.Evidence.Request) == "" || strings.TrimSpace(cr.Evidence.Response) == "" {
		return false
	}
	return true
}

func executeCell(
	ctx context.Context,
	cell Cell,
	repoRoot string,
	lts *apilts.Registry,
	builtinOverride string,
	workDir string,
	dbURL string,
) CellResult {
	base := CellResult{ID: cell.ID, Status: cell.Status}
	protocol := strings.ToLower(strings.TrimSpace(cell.Protocol))
	manifest := strings.ToLower(strings.TrimSpace(cell.Manifest))
	database := strings.ToLower(strings.TrimSpace(cell.Database))

	// database=postgres 必须实际连接。
	if database == "postgres" {
		if err := provePostgres(ctx, dbURL); err != nil {
			base.Outcome = OutcomeFail
			base.Message = "postgres proof failed: " + err.Error()
			return base
		}
		base.Evidence.DatabaseOK = true
	}

	switch {
	case cell.Command == "extension-test-builtin" || cell.ID == "manifest-v3-contract":
		return runManifestSDKWithProcess(ctx, base, repoRoot, builtinOverride, workDir, lts)
	case protocol == "v2" && (manifest == "v3" || manifest == "3"):
		return runProtocolV2RealProcessRPC(ctx, base, repoRoot, workDir, lts, builtinOverride)
	default:
		base.Outcome = OutcomeSkip
		base.Message = fmt.Sprintf("no executor for protocol=%s manifest=%s command=%s", cell.Protocol, cell.Manifest, cell.Command)
		return base
	}
}

// runProtocolV2RealProcessRPC 启动真实 V2 插件子进程并执行至少一次 RPC。
func runProtocolV2RealProcessRPC(
	ctx context.Context,
	base CellResult,
	repoRoot, workDir string,
	lts *apilts.Registry,
	builtinOverride string,
) CellResult {
	// LTS 种子必须声明 current Host / protocol v2 / manifest v3。
	if err := assertLTSCurrent(lts); err != nil {
		base.Outcome = OutcomeFail
		base.Message = err.Error()
		return base
	}

	extension, pluginPath, err := buildBuiltinPlugin(ctx, repoRoot, workDir, "sforum-smtp", builtinOverride)
	if err != nil {
		base.Outcome = OutcomeFail
		base.Message = "build v2 plugin: " + err.Error()
		return base
	}
	base.Evidence.PluginPath = pluginPath
	base.Evidence.Protocol = 2

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: fixedSettings(map[string]string{
			"host": "127.0.0.1", "port": "25", "encryption": "none",
			"from_address": "noreply@example.com", "from_name": "SForum",
		}),
	})
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	base.Evidence.Request = "Start+ProviderProbe(mail.provider)"
	target, err := starter.Start(callCtx, extension)
	if err != nil {
		base.Outcome = OutcomeFail
		base.Message = "start v2 process: " + err.Error()
		return base
	}
	base.Evidence.ProcessStarted = true
	defer func() { _ = starter.Stop(context.Background(), extension) }()

	probe, err := starter.ProviderProbe(callCtx, extension.ID, extensionsruntime.ProviderProbeRequest{Slot: "mail.provider"})
	if err != nil {
		base.Evidence.Response = "probe_err=" + err.Error()
		base.Outcome = OutcomeFail
		base.Message = "provider probe RPC failed: " + err.Error()
		return base
	} else {
		base.Evidence.Response = fmt.Sprintf("probe ok=%v reason=%s instance=%s", probe.OK, probe.Reason, target.InstanceID)
	}
	tel := starter.ProtocolTelemetry(extension.ID)
	if tel.ProtocolVersion != 2 || tel.Transport != "grpc" {
		base.Outcome = OutcomeFail
		base.Message = fmt.Sprintf("expected protocol v2 grpc telemetry, got %#v", tel)
		return base
	}
	if tel.StartCount < 1 {
		base.Outcome = OutcomeFail
		base.Message = "v2 process start not recorded in telemetry"
		return base
	}
	if tel.CallCount < 1 {
		base.Outcome = OutcomeFail
		base.Message = "v2 RPC call not recorded in telemetry"
		return base
	}
	base.Outcome = OutcomePass
	base.Message = fmt.Sprintf("host.v2+protocol.v2 process rpc ok package=%s", extension.ID)
	return base
}

// runManifestSDKWithProcess 要求 backend 二进制存在，并启动进程做一次 RPC。
func runManifestSDKWithProcess(
	ctx context.Context,
	base CellResult,
	repoRoot string,
	builtinOverride, workDir string,
	lts *apilts.Registry,
) CellResult {
	// 先构建真实二进制（禁止 SkipBackendBinary）。
	extension, pluginPath, err := buildBuiltinPlugin(ctx, repoRoot, workDir, "sforum-search-site", builtinOverride)
	if err != nil {
		// search-site 可能无 backend；回退 smtp。
		extension, pluginPath, err = buildBuiltinPlugin(ctx, repoRoot, workDir, "sforum-smtp", builtinOverride)
		if err != nil {
			base.Outcome = OutcomeFail
			base.Message = "build plugin for sdk contract: " + err.Error()
			return base
		}
	}
	// SDK 契约：RequireBackendBinary，禁止 Skip。
	report, err := pluginsdk.LoadAndTest(pluginPath, pluginsdk.Options{RequireBackendBinary: true})
	if err != nil {
		base.Outcome = OutcomeFail
		base.Message = "sdk LoadAndTest: " + err.Error()
		return base
	}
	if !report.OK {
		base.Outcome = OutcomeFail
		base.Message = fmt.Sprintf("sdk contract failed errors=%d", report.Errors)
		return base
	}
	if extensionmanifest.EffectiveManifestVersion(report.Manifest) < 3 {
		base.Outcome = OutcomeFail
		base.Message = "builtin package is not Manifest V3"
		return base
	}

	// 再实际启动进程 + RPC。
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: fixedSettings(map[string]string{}),
	})
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	base.Evidence.Request = "Start+ProviderProbe"
	base.Evidence.PluginPath = pluginPath
	target, err := starter.Start(callCtx, extension)
	if err != nil {
		base.Outcome = OutcomeFail
		base.Message = "start process for sdk cell: " + err.Error()
		return base
	}
	base.Evidence.ProcessStarted = true
	defer func() { _ = starter.Stop(context.Background(), extension) }()
	slot := "search.provider"
	if extension.ID == "sforum.smtp" {
		slot = "mail.provider"
	}
	probe, err := starter.ProviderProbe(callCtx, extension.ID, extensionsruntime.ProviderProbeRequest{Slot: slot})
	if err != nil {
		base.Evidence.Response = "probe_err=" + err.Error()
		base.Outcome = OutcomeFail
		base.Message = "SDK provider probe RPC failed: " + err.Error()
		return base
	}
	tel := starter.ProtocolTelemetry(extension.ID)
	base.Evidence.Response = fmt.Sprintf("probe ok=%v reason=%s instance=%s", probe.OK, probe.Reason, target.InstanceID)
	base.Evidence.Protocol = tel.ProtocolVersion
	if tel.StartCount < 1 {
		base.Outcome = OutcomeFail
		base.Message = "process start not recorded"
		return base
	}
	if tel.CallCount < 1 {
		base.Outcome = OutcomeFail
		base.Message = "SDK provider probe RPC not recorded in telemetry"
		return base
	}
	base.Outcome = OutcomePass
	base.Message = fmt.Sprintf("manifest/sdk+process ok id=%s", report.Manifest.ID)
	return base
}

type fixedSettings map[string]string

func (f fixedSettings) ListSettings(_ context.Context, _ string) (map[string]string, error) {
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out, nil
}

func assertLTSCurrent(lts *apilts.Registry) error {
	snap := lts.Snapshot()
	need := map[string]string{
		"sforum.host.v2":     "current",
		"sforum.protocol.v2": "current",
		"sforum.manifest.v3": "current",
	}
	found := map[string]string{}
	for _, c := range snap.Contracts {
		if want, ok := need[c.ID]; ok {
			found[c.ID] = c.Status
			if c.Status != want {
				return fmt.Errorf("LTS contract %s status=%s want=%s", c.ID, c.Status, want)
			}
		}
	}
	for id := range need {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("missing LTS contract %s", id)
		}
	}
	return nil
}

func provePostgres(ctx context.Context, databaseURL string) error {
	if strings.TrimSpace(databaseURL) == "" {
		return fmt.Errorf("DATABASE_URL / SFORUM_TEST_DATABASE_URL required for postgres cells")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return err
	}
	if one != 1 {
		return fmt.Errorf("unexpected SELECT 1 result %d", one)
	}
	return nil
}

// buildBuiltinPlugin 复制 builtin 包、构建 backend 二进制、返回可启动的 Extension。
// 禁止 SkipBackendBinary：二进制必须真实存在。
func buildBuiltinPlugin(
	ctx context.Context,
	repoRoot, workDir, packageName, override string,
) (extensions.Extension, string, error) {
	_ = ctx
	sourceRoot := strings.TrimSpace(override)
	if sourceRoot == "" {
		sourceRoot = filepath.Join(repoRoot, "extensions", "builtin", "plugins", packageName)
	}
	info, err := os.Stat(sourceRoot)
	if err != nil || !info.IsDir() {
		return extensions.Extension{}, "", fmt.Errorf("plugin source missing: %s", sourceRoot)
	}
	packageRoot := filepath.Join(workDir, "compat-farm-"+packageName+"-"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err := copyDir(sourceRoot, packageRoot); err != nil {
		return extensions.Extension{}, "", err
	}
	// 丢弃源树可能拷入的旧二进制；必须先构建再 LoadPackage（digest 校验需要文件）。
	_ = os.Remove(filepath.Join(packageRoot, "backend", "plugin"))

	// 先读 entry 路径（不完整 Validate）：只解析 JSON。
	rawManifest, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json"))
	if err != nil {
		return extensions.Extension{}, "", fmt.Errorf("read manifest: %w", err)
	}
	entry := backendEntryFromManifestJSON(rawManifest)
	if entry == "" {
		entry = "backend/plugin"
	}
	binaryPath := filepath.Join(packageRoot, filepath.FromSlash(entry))
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		return extensions.Extension{}, "", err
	}
	args := []string{"build", "-trimpath", "-buildvcs=false", "-o", binaryPath, "."}
	cmd := exec.Command("go", args...)
	// 必须在源 backend 构建：go.mod replace 指向 monorepo 相对路径，临时拷贝会断链。
	moduleRoot := filepath.Join(sourceRoot, "backend")
	cmd.Dir = moduleRoot
	// 临时 go.work 绑定 apps/api + 插件 module，避免根 go.work 干扰。
	workFile, workErr := writePluginWorkspace(repoRoot, moduleRoot)
	if workErr != nil {
		return extensions.Extension{}, "", workErr
	}
	defer os.Remove(workFile)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK="+workFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return extensions.Extension{}, "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	if _, err := os.Stat(binaryPath); err != nil {
		return extensions.Extension{}, "", fmt.Errorf("backend binary missing after build: %w", err)
	}
	// 刷新 backend + packageFiles 可执行摘要后完整 LoadPackage（含 ValidatePackageFiles）。
	body, err := os.ReadFile(binaryPath)
	if err != nil {
		return extensions.Extension{}, "", err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	manifestPath := filepath.Join(packageRoot, "sforum.extension.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return extensions.Extension{}, "", err
	}
	if err := os.WriteFile(manifestPath, rewriteExecutableDigests(raw, digest), 0o644); err != nil {
		return extensions.Extension{}, "", err
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		return extensions.Extension{}, "", fmt.Errorf("load manifest: %w", err)
	}
	manifest.Backend.Digest = digest

	ext := extensions.Extension{
		ID:            manifest.ID,
		Name:          manifest.Name,
		Version:       manifest.Version,
		Type:          extensions.TypePlugin,
		Status:        extensions.StatusEnabled,
		Source:        extensions.SourceBuiltin,
		PackagePath:   packageRoot,
		PackageDigest: digest,
		Manifest:      manifest,
	}
	return ext, packageRoot, nil
}

func backendEntryFromManifestJSON(raw []byte) string {
	// 轻量解析，避免完整 Validate 在二进制缺失时失败。
	const key = `"entry"`
	s := string(raw)
	idx := strings.Index(s, key)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(key):]
	q1 := strings.Index(rest, `"`)
	if q1 < 0 {
		return ""
	}
	q2 := strings.Index(rest[q1+1:], `"`)
	if q2 < 0 {
		return ""
	}
	return rest[q1+1 : q1+1+q2]
}

// writePluginWorkspace 生成仅含 apps/api 与插件 backend 的临时 go.work。
func writePluginWorkspace(repoRoot, pluginModuleRoot string) (string, error) {
	goVersion := strings.TrimPrefix(runtime.Version(), "go")
	if goVersion == runtime.Version() || strings.ContainsAny(goVersion, " \t\r\n") {
		return "", fmt.Errorf("unsupported Go runtime version %q", runtime.Version())
	}
	body := fmt.Sprintf("go %s\n\nuse (\n\t%s\n\t%s\n)\n",
		goVersion,
		strconv.Quote(filepath.ToSlash(filepath.Join(repoRoot, "apps/api"))),
		strconv.Quote(filepath.ToSlash(pluginModuleRoot)),
	)
	path := filepath.Join(os.TempDir(), fmt.Sprintf("sforum-compat-farm-%d.work", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write plugin go.work: %w", err)
	}
	return path, nil
}

// rewriteExecutableDigests 把 backend.digest 与 packageFiles 中 executable/backend/plugin
// 的摘要改写为本次构建值。V3 ValidatePackageFiles 会逐文件校验；仅改 backend 会失败。
func rewriteExecutableDigests(raw []byte, digest string) []byte {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return raw
	}
	if backend, ok := root["backend"].(map[string]any); ok {
		// V1 fixture 可无 digest 字段；有则必须同步。
		if _, has := backend["digest"]; has || backend["protocolVersion"] == float64(2) || backend["protocolVersion"] == 2 {
			backend["digest"] = digest
			root["backend"] = backend
		}
	}
	if files, ok := root["packageFiles"].([]any); ok {
		for _, item := range files {
			file, ok := item.(map[string]any)
			if !ok {
				continue
			}
			path, _ := file["path"].(string)
			kind, _ := file["kind"].(string)
			if path == "backend/plugin" || kind == "executable" {
				file["digest"] = digest
			}
		}
		root["packageFiles"] = files
	}
	if _, has := root["packageDigest"]; has {
		root["packageDigest"] = digest
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return raw
	}
	return append(encoded, '\n')
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		// 跳过已构建二进制与模块缓存。
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules") {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode())
	})
}

func guessRepoRoot(matrixPath string) string {
	// tests/compat/matrix.yaml -> repo root
	dir := filepath.Dir(matrixPath)
	if filepath.Base(dir) == "compat" {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == "tests" {
			return filepath.Dir(parent)
		}
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}
