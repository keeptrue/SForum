package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestBuildExtensionDatabaseMigrationTransitionsOrdersDownThenUp(t *testing.T) {
	source := extensionDatabaseDiscoveryArtifact("fixture.plugin", "1.0.0", []extensions.ManifestMigration{
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.shared", "a"),
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.old_one", "b"),
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.old_two", "c"),
	})
	target := extensionDatabaseDiscoveryArtifact("fixture.plugin", "2.0.0", []extensions.ManifestMigration{
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.shared", "a"),
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.new_one", "d"),
	})
	steps, err := buildExtensionDatabaseMigrationTransitions(&source, target)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, direction string }{
		{"fixture.plugin.migration.old_two", "down"},
		{"fixture.plugin.migration.old_one", "down"},
		{"fixture.plugin.migration.shared", "up"},
		{"fixture.plugin.migration.new_one", "up"},
	}
	if len(steps) != len(want) {
		t.Fatalf("steps=%#v", steps)
	}
	for index := range want {
		if steps[index].Position != index+1 || steps[index].Declaration.ID != want[index].id ||
			steps[index].Direction != want[index].direction {
			t.Fatalf("step %d=%#v want=%#v", index, steps[index], want[index])
		}
	}
}

func TestBuildExtensionDatabaseMigrationTransitionsRejectsChecksumDrift(t *testing.T) {
	source := extensionDatabaseDiscoveryArtifact("fixture.plugin", "1.0.0", []extensions.ManifestMigration{
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.shared", "a"),
	})
	target := extensionDatabaseDiscoveryArtifact("fixture.plugin", "2.0.0", []extensions.ManifestMigration{
		extensionDatabaseDiscoveryMigration("fixture.plugin.migration.shared", "b"),
	})
	if _, err := buildExtensionDatabaseMigrationTransitions(&source, target); !errors.Is(err, ErrExtensionDatabaseMigrationChecksumDrift) {
		t.Fatalf("checksum drift was accepted: %v", err)
	}
}

func TestReadExactExtensionDatabaseMigrationRejectsTamperAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("CREATE TABLE items (id BIGINT);\n")
	digest := sha256.Sum256(body)
	declaration := extensions.ManifestMigration{
		ID: "fixture.plugin.migration.initial", ContractVersion: "fixture.plugin.migration.initial@1",
		Path: "migrations/001.sql", Digest: hex.EncodeToString(digest[:]), Transaction: "required",
	}
	artifact := extensionDatabaseDiscoveryArtifact("fixture.plugin", "1.0.0", []extensions.ManifestMigration{declaration})
	artifact.PackagePath = root
	path := filepath.Join(root, filepath.FromSlash(declaration.Path))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if read, err := readExactExtensionDatabaseMigration(artifact, declaration); err != nil || string(read) != string(body) {
		t.Fatalf("read exact migration: body=%q err=%v", read, err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExactExtensionDatabaseMigration(artifact, declaration); !errors.Is(err, ErrExtensionDatabaseMigrationChecksumDrift) {
		t.Fatalf("tampered migration was accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.sql"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := readExactExtensionDatabaseMigration(artifact, declaration); !errors.Is(err, ErrExtensionDatabaseMigrationPackage) {
		t.Fatalf("symlink migration was accepted: %v", err)
	}
}

func extensionDatabaseDiscoveryArtifact(
	extensionID string,
	version string,
	migrations []extensions.ManifestMigration,
) extensionDatabaseExactMigrationArtifact {
	return extensionDatabaseExactMigrationArtifact{
		LifecycleMigrationArtifact: LifecycleMigrationArtifact{
			ExtensionID: extensionID, Version: version, VersionID: 1,
			PackageDigest: strings.Repeat("f", 64), Migrations: migrations,
		},
	}
}

func extensionDatabaseDiscoveryMigration(id string, seed string) extensions.ManifestMigration {
	digest := sha256.Sum256([]byte(seed))
	return extensions.ManifestMigration{
		ID: id, ContractVersion: id + "@1", Path: "migrations/" + seed + ".sql",
		Digest: hex.EncodeToString(digest[:]), Transaction: "required",
	}
}
