package systemupdates

import "time"

const (
	RepositoryOwner = "keeptrue"
	RepositoryName  = "SForum"

	StateCurrent       = "current"
	StateUpdate        = "update_available"
	StateDevelopment   = "development"
	StateUnavailable   = "unavailable"
	SourceOfficial     = "official"
	SourceMirror       = "mirror"
	ErrorInvalidSource = "invalid_source"
	ErrorRequest       = "request_failed"
	ErrorResponse      = "invalid_response"
	ErrorNoRelease     = "no_release"
)

// Status is the safe, operator-facing result of a release check. It never
// includes raw upstream response bodies or network errors.
type Status struct {
	State            string    `json:"state"`
	UpdateAvailable  bool      `json:"updateAvailable"`
	CurrentVersion   string    `json:"currentVersion"`
	CurrentCommit    string    `json:"currentCommit,omitempty"`
	LatestVersion    string    `json:"latestVersion,omitempty"`
	LatestTag        string    `json:"latestTag,omitempty"`
	ReleaseName      string    `json:"releaseName,omitempty"`
	ReleaseURL       string    `json:"releaseUrl,omitempty"`
	PublishedAt      string    `json:"publishedAt,omitempty"`
	CheckedAt        time.Time `json:"checkedAt"`
	Source           string    `json:"source"`
	MirrorConfigured bool      `json:"mirrorConfigured"`
	ErrorCode        string    `json:"errorCode,omitempty"`
}
