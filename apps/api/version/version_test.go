package version

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"

	semver "github.com/Masterminds/semver/v3"
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
	if want := runtime.GOOS + "/" + runtime.GOARCH; info.Platform != want {
		t.Fatalf("unexpected platform %q, want %q", info.Platform, want)
	}
	// 管理端运行状态卡直接消费该 JSON 字段，确保契约里的 platform 真实下发。
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal build info: %v", err)
	}
	if !strings.Contains(string(encoded), `"platform":"`+info.Platform+`"`) {
		t.Fatalf("build info JSON missing platform: %s", encoded)
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

// stubDevelopmentVersionProbe replaces the git probe and returns a pointer the
// test can mutate between assertions.
func stubDevelopmentVersionProbe(t *testing.T, derived string) *string {
	t.Helper()
	original := developmentVersionProbe
	value := derived
	developmentVersionProbe = func() string { return value }
	t.Cleanup(func() { developmentVersionProbe = original })
	return &value
}

func TestCoreCompatibilityVersionSeparatesDevelopmentBuildIdentity(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })

	derived := stubDevelopmentVersionProbe(t, "")

	Current = "dev"
	if got := CoreCompatibilityVersion(); got != "1.0.0" {
		t.Fatalf("untagged development Core compatibility version = %q", got)
	}

	*derived = "3.0.16"
	if got := CoreCompatibilityVersion(); got != "3.0.16" {
		t.Fatalf("tagged development Core compatibility version = %q", got)
	}

	*derived = "3.0.16+13.g6f94488ad"
	if got := CoreCompatibilityVersion(); got != "3.0.16+13.g6f94488ad" {
		t.Fatalf("development Core compatibility version with build metadata = %q", got)
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

func TestReleaseBuildsNeverConsultTheGitProbe(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })
	t.Cleanup(func() {
		developmentVersionProbe = sync.OnceValue(probeDevelopmentCoreVersion)
	})

	developmentVersionProbe = func() string {
		t.Fatal("release build must not probe git for its compatibility version")
		return ""
	}

	Current = "3.0.16"
	if got := CoreCompatibilityVersion(); got != "3.0.16" {
		t.Fatalf("release Core compatibility version = %q", got)
	}
}

func TestParseGitDescribeProducesStrictSemver(t *testing.T) {
	cases := []struct {
		name     string
		describe string
		want     string
	}{
		{"exact tag", "v3.0.16-0-gabc12", "3.0.16"},
		{"commits after tag", "v3.0.16-3-gabc12", "3.0.16+3.gabc12"},
		{"dirty exact tag", "v3.0.16-0-gabc12-dirty", "3.0.16+0.gabc12.dirty"},
		{"dirty after tag", "v3.0.16-13-gabc12-dirty", "3.0.16+13.gabc12.dirty"},
		{"prerelease tag", "v3.0.13-alpha.2-3-gabc12", "3.0.13-alpha.2+3.gabc12"},
		{"prerelease exact tag", "v3.0.13-alpha.2-0-gabc12", "3.0.13-alpha.2"},
		{"trailing newline", "v3.0.16-0-gabc12\n", "3.0.16"},
		{"bare commit", "6f94488ad", ""},
		{"empty", "", ""},
		{"whitespace only", "  \n", ""},
		{"short describe without tag distance", "v3.0.16-gabc12", ""},
		{"tag without patch component", "v3.0-3-gabc12", ""},
		{"tag carrying build metadata", "v3.0.16+ci.5-0-gabc12", ""},
		{"uppercase short sha", "v3.0.16-3-gABC12", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGitDescribe(tc.describe)
			if got != tc.want {
				t.Fatalf("parseGitDescribe(%q) = %q, want %q", tc.describe, got, tc.want)
			}
			if got == "" {
				return
			}
			if _, err := semver.StrictNewVersion(got); err != nil {
				t.Fatalf("parseGitDescribe(%q) = %q is not strict semver: %v", tc.describe, got, err)
			}
		})
	}
}

// TestProbeDevelopmentCoreVersionIsStrictOrEmpty guards the probe wiring
// against a broken git invocation. An empty result is legitimate outside a git
// checkout (source archives, CI containers), so only a non-empty value is
// asserted.
func TestProbeDevelopmentCoreVersionIsStrictOrEmpty(t *testing.T) {
	got := probeDevelopmentCoreVersion()
	t.Logf("derived development coreVersion = %q", got)
	if got == "" {
		t.Skip("no reachable v* git tag in this checkout")
	}
	if _, err := semver.StrictNewVersion(got); err != nil {
		t.Fatalf("probe returned %q, which is not strict semver: %v", got, err)
	}
}

// Build metadata must stay invisible to constraint evaluation: a development
// build sitting on top of the current tag has to satisfy the same manifest
// constraints as the tag itself.
func TestDerivedVersionSatisfiesReleaseConstraints(t *testing.T) {
	for _, derived := range []string{
		"3.0.16",
		"3.0.16+13.gabc12",
		"3.0.16+13.gabc12.dirty",
	} {
		version, err := semver.StrictNewVersion(derived)
		if err != nil {
			t.Fatalf("%q is not strict semver: %v", derived, err)
		}
		for _, required := range []string{">=3.0.12-alpha.7", ">=3.0.16", "^3.0.0"} {
			constraint, err := semver.NewConstraint(required)
			if err != nil {
				t.Fatalf("invalid constraint %q: %v", required, err)
			}
			if !constraint.Check(version) {
				t.Fatalf("%q does not satisfy %q", derived, required)
			}
		}
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

	derived := stubDevelopmentVersionProbe(t, "")
	Current = "dev"
	identity = Identity()
	if identity.CoreVersion != "1.0.0" || identity.BuildVersion != "dev-01234" || !identity.Valid {
		t.Fatalf("untagged development identity = %#v", identity)
	}

	*derived = "3.0.16"
	identity = Identity()
	if identity.CoreVersion != "3.0.16" || identity.BuildVersion != "dev-01234" || !identity.Valid {
		t.Fatalf("tagged development identity = %#v", identity)
	}

	// A tag-shaped build label is not strict semver: compatibility must be
	// reported as unknown instead of being silently rewritten.
	Current = "v3.0.16"
	identity = Identity()
	if identity.CoreVersion != "v3.0.16" || identity.Valid {
		t.Fatalf("invalid identity = %#v", identity)
	}
}
