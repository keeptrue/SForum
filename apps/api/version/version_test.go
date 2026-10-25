package version

import (
	"bytes"
	"strings"
	"testing"
)

func TestGetExposesProjectAndInjectedBuildIdentity(t *testing.T) {
	originalCurrent, originalCommit, originalBuildTime := Current, Commit, BuildTime
	t.Cleanup(func() {
		Current, Commit, BuildTime = originalCurrent, originalCommit, originalBuildTime
	})
	Current = "2.8.0"
	Commit = "0123456789abcdef"
	BuildTime = "2026-07-29T10:00:00+08:00"

	info := Get()
	if info.Name != Name || info.Version != "2.8.0" || info.Commit != Commit || info.BuiltAt != BuildTime {
		t.Fatalf("unexpected build info: %#v", info)
	}
	if info.GoVersion == "" || info.SourceURL != SourceURL {
		t.Fatalf("missing runtime project identity: %#v", info)
	}
}

func TestPrintIfRequestedIsExactAndCompact(t *testing.T) {
	originalCurrent, originalCommit, originalBuildTime := Current, Commit, BuildTime
	t.Cleanup(func() {
		Current, Commit, BuildTime = originalCurrent, originalCommit, originalBuildTime
	})
	Current = "2.8.0"
	Commit = "0123456789abcdef"
	BuildTime = ""

	var output bytes.Buffer
	if !PrintIfRequested(&output, []string{"--version"}) {
		t.Fatal("expected --version to be handled")
	}
	if got := strings.TrimSpace(output.String()); got != "SForum 2.8.0 (0123456789ab)" {
		t.Fatalf("unexpected version output %q", got)
	}
	if PrintIfRequested(&output, []string{"serve", "--version"}) {
		t.Fatal("expected non-exact arguments to continue to the command")
	}
}

func TestDevelopmentVersionIncludesShortCommitOnce(t *testing.T) {
	originalCurrent, originalCommit, originalBuildTime := Current, Commit, BuildTime
	t.Cleanup(func() {
		Current, Commit, BuildTime = originalCurrent, originalCommit, originalBuildTime
	})
	Current = "dev"
	Commit = "0123456789abcdef"
	BuildTime = ""

	info := Get()
	if info.Version != "dev-01234" {
		t.Fatalf("development version = %q", info.Version)
	}
	if summary := Summary(); !strings.HasPrefix(summary, "SForum dev-01234") || strings.Contains(summary, "(0123456789ab)") {
		t.Fatalf("development summary = %q", summary)
	}
}

func TestCoreCompatibilityVersionSeparatesDevelopmentBuildIdentity(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })

	Current = "dev"
	if got := CoreCompatibilityVersion(); got != "1.0.0" {
		t.Fatalf("development Core compatibility version = %q", got)
	}

	Current = " 2.8.0-beta.1 "
	if got := CoreCompatibilityVersion(); got != "2.8.0-beta.1" {
		t.Fatalf("injected Core compatibility version = %q", got)
	}

	Current = "not-semver"
	if got := CoreCompatibilityVersion(); got != "not-semver" {
		t.Fatalf("invalid injected version was rewritten to %q", got)
	}
}

func TestIdentityFreezesCompatibilityAndBuildLabels(t *testing.T) {
	originalCurrent, originalCommit := Current, Commit
	t.Cleanup(func() { Current, Commit = originalCurrent, originalCommit })

	Current = "2.8.0"
	Commit = "0123456789abcdef"
	identity := Identity()
	if identity.CoreVersion != "2.8.0" || identity.BuildVersion != "2.8.0" || !identity.Valid {
		t.Fatalf("release identity = %#v", identity)
	}

	Current = "dev"
	identity = Identity()
	if identity.CoreVersion != "1.0.0" || identity.BuildVersion != "dev-01234" || !identity.Valid {
		t.Fatalf("development identity = %#v", identity)
	}

	// A tag-shaped build label is not strict semver: compatibility must be
	// reported as unknown instead of being silently rewritten.
	Current = "v3.0.16"
	identity = Identity()
	if identity.CoreVersion != "v3.0.16" || identity.Valid {
		t.Fatalf("invalid identity = %#v", identity)
	}
}
