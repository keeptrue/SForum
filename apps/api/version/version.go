// Package version exposes the SForum version and build identity.
package version

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	semver "github.com/Masterminds/semver/v3"
)

const (
	Name      = "SForum"
	SourceURL = "https://github.com/keeptrue/SForum"
)

// developmentCoreCompatibilityVersion is the last-resort fallback for
// development builds without a reachable git tag. It cannot be empty: the
// database migrator parses the compatibility version with StrictNewVersion
// (database/migrator/core_runtime_state.go, core_compatibility.go).
const developmentCoreCompatibilityVersion = "1.0.0"

// These values are replaced by release builds through -ldflags. Current is the
// shared application version and remains the authority for compatibility gates.
var (
	Current   = "dev"
	Commit    = ""
	BuildTime = ""

	developmentCommitOnce sync.Once
	developmentCommit     string

	// developmentVersionProbe runs at most once per process: git state cannot
	// change under a running build, and CoreCompatibilityVersion is called from
	// startup, handshakes, and health probes. Tests replace it directly.
	developmentVersionProbe = sync.OnceValue(probeDevelopmentCoreVersion)
)

// gitDescribePattern matches
// `git describe --tags --match 'v[0-9]*' --long --dirty --always`:
// v<semver>-<commits since tag>-g<short sha>[-dirty].
var gitDescribePattern = regexp.MustCompile(`^v(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)-(\d+)-g([0-9a-f]+)(-dirty)?$`)

type BuildInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuiltAt   string `json:"builtAt,omitempty"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"` // 构建目标 GOOS/GOARCH，用于管理端展示当前系统架构。
	Dirty     bool   `json:"dirty"`
	SourceURL string `json:"sourceUrl"`
}

func Get() BuildInfo {
	info := BuildInfo{
		Name:      Name,
		Version:   strings.TrimSpace(Current),
		Commit:    strings.TrimSpace(Commit),
		BuiltAt:   strings.TrimSpace(BuildTime),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		SourceURL: SourceURL,
	}
	build, ok := debug.ReadBuildInfo()
	if ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = strings.TrimSpace(setting.Value)
				}
			case "vcs.time":
				if info.BuiltAt == "" {
					info.BuiltAt = strings.TrimSpace(setting.Value)
				}
			case "vcs.modified":
				info.Dirty, _ = strconv.ParseBool(setting.Value)
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Version == "dev" && info.Commit == "" {
		info.Commit = resolveDevelopmentCommit()
	}
	if info.Version == "dev" && info.Commit != "" {
		info.Version += "-" + shortCommit(info.Commit, 5)
	}
	return info
}

// CoreCompatibilityVersion keeps the local build label separate from the
// strict semantic version used by database and high-risk extension fences.
//
// Development builds derive it from the nearest reachable git tag so a local
// checkout reports the release it is actually built on; without a tag the
// compatibility baseline applies.
func CoreCompatibilityVersion() string {
	value, _ := CoreCompatibilityDeclared()
	return value
}

// normalizeCompatibilityVersion turns a build label into the value the version
// fences parse. Deployment names a release with its git tag
// (SFORUM_VERSION=v3.0.18) and that same string reaches -ldflags through the
// build arg, while strict semver has no "v" prefix. Stripping it here keeps one
// variable on the deployment side instead of asking operators to maintain a
// second, differently spelled one.
//
// Display is a different contract: BuildInfo.Version (Get) keeps the raw label,
// so an operator still sees what the build was stamped with.
func normalizeCompatibilityVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// CoreCompatibilityDeclared reports the compatibility version together with
// whether it came from a real declaration (release ldflags or a reachable v*
// tag) instead of the untagged-development fallback sentinel.
//
// The boolean is the only way a caller can tell "this build does not know its
// release line" from "this build really is 1.0.0": both report the same string.
// Gates that compare versions must skip the comparison when it is false rather
// than read the sentinel as a real, lower intent: a shallow clone that hides
// every v* tag is a missing fact, not a downgrade request.
func CoreCompatibilityDeclared() (string, bool) {
	// Normalize before the "dev" comparison: a tag-shaped Current is a release
	// declaration, not an invalid identity.
	current := normalizeCompatibilityVersion(Current)
	if current == "dev" {
		if derived := strings.TrimSpace(developmentVersionProbe()); derived != "" {
			return derived, true
		}
		return developmentCoreCompatibilityVersion, false
	}
	// Release builds always declare: Current is injected through -ldflags and
	// the git probe is never consulted.
	return current, true
}

// RequireDeclaredForProduction fails closed when a production process cannot
// name its Core compatibility version.
//
// Both version fences (the Core downgrade fence and the raw-extension
// compatibility fence) compare that value, so an undeclared build would skip
// them and read the sentinel as intent. A version fence is a fail-closed
// surface on the same footing as safe mode: it may not run on a guess.
//
// Source-built images must pass the version explicitly
// (`docker build --build-arg SFORUM_VERSION=<semver>`, or set SFORUM_VERSION
// for docker compose); the published release images inject it through
// -ldflags.
func RequireDeclaredForProduction(appEnv string) error {
	if !isProductionAppEnv(appEnv) {
		// Resolving the build identity runs the git probe; non-production
		// processes must not pay for it.
		return nil
	}
	value, declared := CoreCompatibilityDeclared()
	return RequireDeclaredCoreVersion(appEnv, value, declared)
}

func isProductionAppEnv(appEnv string) bool {
	return strings.EqualFold(strings.TrimSpace(appEnv), "production")
}

// RequireDeclaredCoreVersion is the pure form of the production gate: it
// decides from an already-resolved build identity, so callers that cache the
// identity do not re-probe git and tests can exercise the policy directly.
//
// The production branch checks two things, not one: that the build declared a
// version, and that the declared version is usable. A declared-but-unparseable
// value — the common `SFORUM_VERSION=v3.0.18` copied from a git tag — would
// otherwise pass the gate and fail deep inside the migrator with
// `invalid target SForum version`, which is fail-closed at the wrong layer and
// impossible to diagnose from the startup log.
func RequireDeclaredCoreVersion(appEnv string, coreVersion string, declared bool) error {
	if !isProductionAppEnv(appEnv) {
		return nil
	}
	if !declared {
		return fmt.Errorf(
			"production build has no declared Core compatibility version (coreVersion=%q): rebuild with --build-arg SFORUM_VERSION=<semver> (docker compose: set SFORUM_VERSION), or deploy the published release image; the Core downgrade and raw-extension compatibility fences are fail-closed and may not run on a guess",
			coreVersion,
		)
	}
	if _, err := semver.StrictNewVersion(normalizeCompatibilityVersion(coreVersion)); err != nil {
		return fmt.Errorf(
			"production build declares an unusable Core compatibility version %q: the fences parse it with strict semver (for example SFORUM_VERSION=3.0.18, or 3.0.18-rc.1 for a pre-release); a leading \"v\" is stripped automatically, so v3.0.18 is accepted: %w",
			coreVersion, err,
		)
	}
	return nil
}

// HostIdentity is the frozen build identity the Host exposes to extensions.
//
// CoreVersion is the only compatibility authority: manifest sforumVersion
// constraints are evaluated against it. BuildVersion is display and
// troubleshooting only and must never be compared to a constraint. Valid
// reports whether CoreVersion is strict semver, so consumers can distinguish
// "unknown" from "incompatible" instead of guessing.
type HostIdentity struct {
	CoreVersion  string `json:"coreVersion"`
	BuildVersion string `json:"buildVersion"`
	Valid        bool   `json:"valid"`
}

// Identity composes the Host identity shared with plugin runtimes, admin
// pages, and public HTTP consumers. It never introduces a second version
// authority: CoreCompatibilityVersion stays the single source.
func Identity() HostIdentity {
	core := CoreCompatibilityVersion()
	_, err := semver.StrictNewVersion(core)
	return HostIdentity{
		CoreVersion:  core,
		BuildVersion: Get().Version,
		Valid:        err == nil,
	}
}

func resolveDevelopmentCommit() string {
	developmentCommitOnce.Do(func() {
		output, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err == nil {
			developmentCommit = strings.TrimSpace(string(output))
		}
	})
	return developmentCommit
}

// probeDevelopmentCoreVersion derives the compatibility version of a
// development build from git. Any failure (no git binary, no checkout, no
// reachable tag) returns "" so CoreCompatibilityVersion can fall back.
func probeDevelopmentCoreVersion() string {
	output, err := exec.Command(
		"git", "describe", "--tags", "--match", "v[0-9]*", "--long", "--dirty", "--always",
	).Output()
	if err != nil {
		return ""
	}
	return parseGitDescribe(string(output))
}

// parseGitDescribe turns `git describe --long` output into a strict semantic
// version. Sitting exactly on a clean tag reports the tag alone; anything else
// keeps traceability in build metadata, which compatibility constraints ignore.
func parseGitDescribe(describe string) string {
	matches := gitDescribePattern.FindStringSubmatch(strings.TrimSpace(describe))
	if matches == nil {
		return ""
	}
	version, commits, sha, dirty := matches[1], matches[2], matches[3], matches[4] != ""
	if commits != "0" || dirty {
		version += "+" + commits + ".g" + sha
		if dirty {
			version += ".dirty"
		}
	}
	if _, err := semver.StrictNewVersion(version); err != nil {
		return ""
	}
	return version
}

func Summary() string {
	info := Get()
	summary := fmt.Sprintf("%s %s", info.Name, info.Version)
	if info.Commit != "" && !strings.HasPrefix(info.Version, "dev-") {
		summary += " (" + shortCommit(info.Commit, 12) + ")"
	}
	if info.Dirty {
		summary += " dirty"
	}
	return summary
}

// PrintIfRequested handles the standard process-level --version flag without
// changing the normal argument behavior of long-running commands.
func PrintIfRequested(w io.Writer, args []string) bool {
	if len(args) != 1 || args[0] != "--version" {
		return false
	}
	_, _ = fmt.Fprintln(w, Summary())
	return true
}

func shortCommit(commit string, length int) string {
	if len(commit) <= length {
		return commit
	}
	return commit[:length]
}
