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
	SourceURL = "https://github.com/zhuchunshu/SForum"
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
	current := strings.TrimSpace(Current)
	if current == "dev" {
		if derived := strings.TrimSpace(developmentVersionProbe()); derived != "" {
			return derived
		}
		return developmentCoreCompatibilityVersion
	}
	return current
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
