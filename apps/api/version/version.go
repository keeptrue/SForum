// Package version exposes the SForum version and build identity.
package version

import (
	"fmt"
	"io"
	"os/exec"
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

const developmentCoreCompatibilityVersion = "1.0.0"

// These values are replaced by release builds through -ldflags. Current is the
// shared application version and remains the authority for compatibility gates.
var (
	Current   = "dev"
	Commit    = ""
	BuildTime = ""

	developmentCommitOnce sync.Once
	developmentCommit     string
)

type BuildInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuiltAt   string `json:"builtAt,omitempty"`
	GoVersion string `json:"goVersion"`
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
func CoreCompatibilityVersion() string {
	current := strings.TrimSpace(Current)
	if current == "dev" {
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
