package extensionsruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// TestProtocolV2StorageBuiltinRoundTrip 证明默认 sforum.storage-fs 制品走 gRPC V2，
// known-slot 分块 Put/Open/Delete 可用。
func TestProtocolV2StorageBuiltinRoundTrip(t *testing.T) {
	repositoryRoot := protocolTestRepositoryRoot(t)
	extension := buildProtocolV2StorageBuiltin(t, repositoryRoot)
	root := filepath.Join(t.TempDir(), "objects")
	settings := protocolTestSettings{
		"root_path": root, "public_base_url": "https://cdn.example.test/files",
	}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Settings: settings,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatalf("start storage-fs v2: %v", err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	if target.BaseURL != "" {
		t.Fatalf("storage-fs v2 must not expose HTTP route target: %#v", target)
	}

	probe, err := starter.StorageProbe(ctx, extension.ID, extensionsruntime.StorageProbeRequest{})
	if err != nil || !probe.OK || probe.Reason != "storage.ok" {
		t.Fatalf("storage v2 probe = %#v err=%v", probe, err)
	}

	payload := []byte("protocol-v2-storage-round-trip-payload")
	begin, err := starter.StoragePutBegin(ctx, extension.ID, extensionsruntime.StoragePutBeginRequest{
		Key: "v2/compatibility/p13.bin", ContentType: "application/octet-stream", Size: int64(len(payload)),
	})
	if err != nil || !begin.OK || begin.SessionID == "" {
		t.Fatalf("storage v2 put begin = %#v err=%v", begin, err)
	}
	// 小块写入，验证 base64 分块路径。
	const chunkSize = 11
	for off := 0; off < len(payload); off += chunkSize {
		end := off + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		written, err := starter.StoragePutChunk(ctx, extension.ID, extensionsruntime.StoragePutChunkRequest{
			SessionID: begin.SessionID, Data: payload[off:end], Final: end == len(payload),
		})
		if err != nil || !written.OK {
			t.Fatalf("storage v2 put chunk = %#v err=%v off=%d", written, err, off)
		}
	}

	exists, err := starter.StorageExists(ctx, extension.ID, extensionsruntime.StorageExistsRequest{Key: "v2/compatibility/p13.bin"})
	if err != nil || !exists.OK || !exists.Exists {
		t.Fatalf("storage v2 exists = %#v err=%v", exists, err)
	}
	stat, err := starter.StorageStat(ctx, extension.ID, extensionsruntime.StorageStatRequest{Key: "v2/compatibility/p13.bin"})
	if err != nil || !stat.OK || !stat.Exists || stat.Size != int64(len(payload)) {
		t.Fatalf("storage v2 stat = %#v err=%v", stat, err)
	}

	opened, err := starter.StorageOpen(ctx, extension.ID, extensionsruntime.StorageOpenRequest{Key: "v2/compatibility/p13.bin"})
	if err != nil || !opened.OK || opened.Size != int64(len(payload)) {
		t.Fatalf("storage v2 open = %#v err=%v", opened, err)
	}
	var result bytes.Buffer
	for {
		chunk, err := starter.StorageGetChunk(ctx, extension.ID, extensionsruntime.StorageGetChunkRequest{
			SessionID: opened.SessionID, MaxBytes: 9,
		})
		if err != nil || !chunk.OK {
			t.Fatalf("storage v2 get chunk = %#v err=%v", chunk, err)
		}
		_, _ = result.Write(chunk.Data)
		if chunk.EOF {
			break
		}
	}
	if !bytes.Equal(result.Bytes(), payload) {
		t.Fatalf("storage v2 payload = %q, want %q", result.Bytes(), payload)
	}
	if closed, err := starter.StorageClose(ctx, extension.ID, extensionsruntime.StorageCloseRequest{SessionID: opened.SessionID}); err != nil || !closed.OK {
		t.Fatalf("storage v2 close = %#v err=%v", closed, err)
	}

	public, err := starter.StoragePublicURL(ctx, extension.ID, extensionsruntime.StoragePublicURLRequest{Key: "v2/compatibility/p13.bin"})
	if err != nil || !public.OK || public.URL != "https://cdn.example.test/files/v2/compatibility/p13.bin" {
		t.Fatalf("storage v2 public url = %#v err=%v", public, err)
	}
	if deleted, err := starter.StorageDelete(ctx, extension.ID, extensionsruntime.StorageObjectRequest{Key: "v2/compatibility/p13.bin"}); err != nil || !deleted.OK {
		t.Fatalf("storage v2 delete = %#v err=%v", deleted, err)
	}

	telemetry := starter.ProtocolTelemetry(extension.ID)
	if telemetry.ProtocolVersion != 2 || telemetry.Transport != "grpc" || telemetry.Deprecated {
		t.Fatalf("expected protocol v2 telemetry, got %#v", telemetry)
	}
	if telemetry.StartCount != 1 || telemetry.CallCount == 0 {
		t.Fatalf("unexpected call counters: %#v", telemetry)
	}
}

func buildProtocolV2StorageBuiltin(t *testing.T, repositoryRoot string) extensions.Extension {
	t.Helper()
	packageName := "sforum-storage-fs"
	sourceRoot := filepath.Join(repositoryRoot, "extensions", "builtin", "plugins", packageName)
	moduleRoot := filepath.Join(sourceRoot, "backend")

	// 始终在临时包中构建，避免污染源码树 gitignored backend/plugin。
	packageRoot := filepath.Join(t.TempDir(), packageName)
	if err := copyTree(sourceRoot, packageRoot); err != nil {
		t.Fatalf("copy package: %v", err)
	}
	_ = os.Remove(filepath.Join(packageRoot, "backend", "plugin"))
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	command.Dir = moduleRoot
	command.Env = append(os.Environ(), "GOWORK="+temporaryPluginWorkspace(t, repositoryRoot, moduleRoot))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build storage-fs v2 binary: %v\n%s", err, output)
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
	// 复用 JSON 级 digest 改写（与 SMTP 相同，不硬编码历史摘要）。
	if err := os.WriteFile(manifestPath, replaceSMTPDigests(raw, digest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load storage-fs v2 package after digest rewrite: %v", err)
	}
	if manifest.Backend.ProtocolVersion != 2 {
		t.Fatalf("default storage-fs package must be protocol v2: %#v", manifest.Backend)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: extensions.StatusEnabled, Source: extensions.SourceBuiltin,
		Manifest: manifest, PackagePath: packageRoot, PackageDigest: digest,
	}
}
