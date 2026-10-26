package extensionpackage_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestReadArchiveEnforcesInflatedByteLimit(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, body := range map[string]string{
		extensionmanifest.ManifestFileName: `{"manifestVersion":3,"id":"demo.plugin","name":"Demo","description":"Demo package.","url":"https://example.com","author":{"name":"Demo"},"version":"1.0.0","type":"plugin","sforumVersion":">=0.1.0"}`,
		"big.txt":                          strings.Repeat("A", 256),
	} {
		handle, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := handle.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}

	_, _, err := extensionpackage.ReadArchive(buffer.Bytes(), extensionpackage.ArchiveLimits{Entries: 2, Bytes: 256})
	if !errors.Is(err, extensionpackage.ErrInvalidArchive) {
		t.Fatalf("expected inflated bytes above the limit to be rejected, got %v", err)
	}
	_, files, err := extensionpackage.ReadArchive(buffer.Bytes(), extensionpackage.ArchiveLimits{Entries: 2, Bytes: 1024})
	if err != nil {
		t.Fatalf("expected archive below the limit to be accepted, got %v", err)
	}
	if len(files) != 1 || len(files[0].Body) != 256 {
		t.Fatalf("unexpected parsed files: %#v", files)
	}
}
