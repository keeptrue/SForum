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

func TestCoreCompatibilityDeclaredDistinguishesFallbackFromRealVersion(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })

	derived := stubDevelopmentVersionProbe(t, "")

	// 1) dev + 探针为空：返回哨兵，但**不是**声明。
	Current = "dev"
	value, declared := CoreCompatibilityDeclared()
	if value != developmentCoreCompatibilityVersion || declared {
		t.Fatalf("untagged development declaration = %q/%t, want %q/false",
			value, declared, developmentCoreCompatibilityVersion)
	}
	if got := CoreCompatibilityVersion(); got != value {
		t.Fatalf("CoreCompatibilityVersion = %q, want the declared pair's value %q", got, value)
	}

	// 2) dev + 探针有值：派生值就是声明。
	*derived = "3.0.18+10.gd1153068c.dirty"
	value, declared = CoreCompatibilityDeclared()
	if value != "3.0.18+10.gd1153068c.dirty" || !declared {
		t.Fatalf("tagged development declaration = %q/%t", value, declared)
	}

	// 3) release：ldflags 注入值恒为声明，且不触发 git 探针。
	*derived = ""
	Current = " 2.8.0-beta.1 "
	value, declared = CoreCompatibilityDeclared()
	if value != "2.8.0-beta.1" || !declared {
		t.Fatalf("release declaration = %q/%t", value, declared)
	}
}

func TestRequireDeclaredCoreVersionFailsClosedOnlyInProduction(t *testing.T) {
	cases := []struct {
		name        string
		appEnv      string
		coreVersion string
		declared    bool
		wantErr     bool
		wantText    []string
	}{
		{"production undeclared", "production", "1.0.0", false, true, nil},
		{"production case variant", "Production", "1.0.0", false, true, nil},
		{"production declared", "production", "3.0.18", true, false, nil},
		{"production declared with build metadata", "production", "3.0.18+4.ga4e91267b", true, false, nil},
		// 照 tag 抄是最自然的写法：v 前缀在兼容版本解析处被剥掉，闸门放行。
		{"production v-prefixed tag", "production", "v3.0.18", true, false, nil},
		{"production padded tag", "production", " v3.0.18 ", true, false, nil},
		{"production bare v", "production", "v", true, true, nil},
		{"production not semver", "production", "not-semver", true, true, nil},
		{"production empty version", "production", "", true, true, nil},
		{"development undeclared", "development", "1.0.0", false, false, nil},
		{"development v-prefixed tag", "development", "v3.0.18", true, false, nil},
		{"staging undeclared", "staging", "1.0.0", false, false, nil},
		{"empty env undeclared", "", "1.0.0", false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireDeclaredCoreVersion(tc.appEnv, tc.coreVersion, tc.declared)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("gate error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("gate error = nil, want a refusal")
			}
			for _, want := range append([]string{"SFORUM_VERSION"}, tc.wantText...) {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("gate error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// 生产闸门必须真的读构建身份：无 tag 的 dev 构建过不去，声明过就放行。
func TestRequireDeclaredForProductionReadsTheBuildIdentity(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })
	derived := stubDevelopmentVersionProbe(t, "")

	Current = "dev"
	if err := RequireDeclaredForProduction("production"); err == nil {
		t.Fatal("untagged development build passed the production gate")
	}

	*derived = "3.0.18+10.gd1153068c.dirty"
	if err := RequireDeclaredForProduction("production"); err != nil {
		t.Fatalf("tag-derived development build was refused: %v", err)
	}

	Current = " 3.0.18 "
	if err := RequireDeclaredForProduction("production"); err != nil {
		t.Fatalf("release build was refused: %v", err)
	}

	Current = "v3.0.18"
	if err := RequireDeclaredForProduction("production"); err != nil {
		t.Fatalf("tag-shaped release build was refused: %v", err)
	}
}

// 部署侧沿用 tag 语义的 SFORUM_VERSION=v3.0.18：兼容版本去 v，展示标签保留原样。
func TestTagShapedBuildLabelStripsVPrefixOnlyFromCompatibilityVersion(t *testing.T) {
	originalCurrent := Current
	t.Cleanup(func() { Current = originalCurrent })

	Current = "v3.0.18"
	value, declared := CoreCompatibilityDeclared()
	if value != "3.0.18" || !declared {
		t.Fatalf("tag-shaped declaration = %q/%t, want 3.0.18/true", value, declared)
	}
	if got := CoreCompatibilityVersion(); got != "3.0.18" {
		t.Fatalf("CoreCompatibilityVersion = %q, want 3.0.18", got)
	}
	if got := Get().Version; got != "v3.0.18" {
		t.Fatalf("BuildInfo.Version = %q, want the raw v3.0.18", got)
	}

	Current = " v3.0.18 "
	if value, declared := CoreCompatibilityDeclared(); value != "3.0.18" || !declared {
		t.Fatalf("padded tag-shaped declaration = %q/%t", value, declared)
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
	if value, declared := CoreCompatibilityDeclared(); value != "3.0.16" || !declared {
		t.Fatalf("release declaration = %q/%t", value, declared)
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

	// 带 v 的标签是发行声明：兼容版本归一成 strict semver，展示标签保留原样。
	Current = "v3.0.16"
	identity = Identity()
	if identity.CoreVersion != "3.0.16" || identity.BuildVersion != "v3.0.16" || !identity.Valid {
		t.Fatalf("tag-shaped identity = %#v", identity)
	}
}
