package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// TestProtocolV2SMTPBuiltinMailProvider 证明默认 sforum.smtp 制品走 gRPC V2，
// known-slot probe 仍可用。
func TestProtocolV2SMTPBuiltinMailProvider(t *testing.T) {
	repositoryRoot := protocolTestRepositoryRoot(t)
	extension := buildProtocolV2SMTPBuiltin(t, repositoryRoot)
	host, port, wait := startSMTPProbeServer(t)
	settings := protocolTestSettings{
		"host": host, "port": strconv.Itoa(port), "encryption": "none",
		"from_address": "noreply@example.com", "from_name": "SForum",
	}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: settings,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatalf("start smtp v2: %v", err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	if target.BaseURL != "" {
		t.Fatalf("smtp v2 must not expose HTTP route target: %#v", target)
	}

	probe, err := starter.ProviderProbe(ctx, extension.ID, extensionsruntime.ProviderProbeRequest{Slot: "mail.provider"})
	if err != nil || !probe.OK || probe.Reason != "smtp.connection_ok" {
		t.Fatalf("SMTP v2 provider probe = %#v err=%v", probe, err)
	}
	wait(t)

	telemetry := starter.ProtocolTelemetry(extension.ID)
	if telemetry.ProtocolVersion != 2 || telemetry.Transport != "grpc" || telemetry.Deprecated {
		t.Fatalf("expected protocol v2 telemetry, got %#v", telemetry)
	}
	if telemetry.StartCount != 1 || telemetry.CallCount == 0 {
		t.Fatalf("unexpected call counters: %#v", telemetry)
	}
}

func buildProtocolV2SMTPBuiltin(t *testing.T, repositoryRoot string) extensions.Extension {
	t.Helper()
	packageName := "sforum-smtp"
	sourceRoot := filepath.Join(repositoryRoot, "extensions", "builtin", "plugins", packageName)
	moduleRoot := filepath.Join(sourceRoot, "backend")

	// 始终在临时包中构建，避免污染源码树 gitignored backend/plugin 与已提交 digest。
	packageRoot := filepath.Join(t.TempDir(), packageName)
	if err := copyTree(sourceRoot, packageRoot); err != nil {
		t.Fatalf("copy package: %v", err)
	}
	// 丢弃可能从源树拷入的本地产物。
	_ = os.Remove(filepath.Join(packageRoot, "backend", "plugin"))
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	command.Dir = moduleRoot
	command.Env = append(os.Environ(), "GOWORK="+temporaryPluginWorkspace(t, repositoryRoot, moduleRoot))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build smtp v2 binary: %v\n%s", err, output)
	}
	body, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	manifestPath := filepath.Join(packageRoot, extensions.ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, replaceSMTPDigests(raw, digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load smtp v2 package after digest rewrite: %v", err)
	}
	if manifest.Backend.ProtocolVersion != 2 {
		t.Fatalf("default smtp package must be protocol v2: %#v", manifest.Backend)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		Manifest: manifest, PackagePath: packageRoot, PackageDigest: digest,
	}
}

func replaceSMTPDigests(raw []byte, digest string) []byte {
	// 测试辅助：把 backend / packageFiles 可执行摘要改写为本次构建值。
	// 不得硬编码历史 digest；本地与 CI 的二进制字节会随工具链漂移。
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return raw
	}
	if backend, ok := root["backend"].(map[string]any); ok {
		backend["digest"] = digest
		root["backend"] = backend
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
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return raw
	}
	return append(encoded, '\n')
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
