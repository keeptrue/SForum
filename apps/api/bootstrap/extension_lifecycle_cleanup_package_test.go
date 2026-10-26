package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestProductionLifecyclePackagePurgesOnlyExactArtifactBelowRoot(t *testing.T) {
	root := t.TempDir()
	packagePath := filepath.Join(root, "demo.plugin", "artifact")
	if err := os.MkdirAll(packagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "plugin.json"), []byte("exact artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := inspectProductionLifecyclePackage(root, packagePath, digest)
	if err != nil || !artifact.present {
		t.Fatalf("inspect exact package: present=%v err=%v", artifact.present, err)
	}
	if err := artifact.purge(); err != nil {
		t.Fatalf("purge exact package: %v", err)
	}
	if _, err := os.Stat(packagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package still present: %v", err)
	}
	if err := artifact.purge(); err != nil {
		t.Fatalf("idempotent package purge: %v", err)
	}
}

func TestProductionLifecyclePackageRejectsEscapeDriftAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if _, err := inspectProductionLifecyclePackage(root, outside, "digest"); !errors.Is(err, errProductionLifecycleCleanupConflict) {
		t.Fatalf("outside root error = %v", err)
	}
	if _, err := inspectProductionLifecyclePackage(root, root, "digest"); !errors.Is(err, errProductionLifecycleCleanupConflict) {
		t.Fatalf("root package error = %v", err)
	}

	packagePath := filepath.Join(root, "demo.plugin", "artifact")
	if err := os.MkdirAll(packagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "plugin.json"), []byte("exact artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "plugin.json"), []byte("drifted artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectProductionLifecyclePackage(root, packagePath, digest); !errors.Is(err, errProductionLifecycleCleanupConflict) {
		t.Fatalf("digest drift error = %v", err)
	}

	symlinkPath := filepath.Join(root, "linked-package")
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectProductionLifecyclePackage(root, symlinkPath, digest); err == nil {
		t.Fatal("symlink package was accepted")
	}
}

func TestProductionLifecycleCleanupLockKeyIsStableAndScoped(t *testing.T) {
	first := productionLifecycleCleanupLockKey("cleanup-one")
	if first != productionLifecycleCleanupLockKey("cleanup-one") {
		t.Fatal("cleanup lock key is not deterministic")
	}
	if first == productionLifecycleCleanupLockKey("cleanup-two") {
		t.Fatal("distinct cleanup ids share a lock key")
	}
}
