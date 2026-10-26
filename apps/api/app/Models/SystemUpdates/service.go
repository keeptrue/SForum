package systemupdates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	semver "github.com/Masterminds/semver/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

const (
	defaultCacheTTL    = 5 * time.Minute
	defaultFailureTTL  = 5 * time.Minute
	requestTimeout     = 8 * time.Second
	maxResponseBytes   = 2 << 20
	maxRedirects       = 3
	officialAPIBaseURL = "https://api.github.com"
)

type Source interface {
	GitHubMirrorURL(context.Context) (string, error)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Option func(*Service)

type Service struct {
	source     Source
	client     HTTPDoer
	build      func() platformversion.BuildInfo
	now        func() time.Time
	cacheTTL   time.Duration
	failureTTL time.Duration
	logger     interface {
		WarnContext(context.Context, string, ...any)
	}

	mu        sync.Mutex
	cached    Status
	cacheKey  string
	expiresAt time.Time
}

func NewService(source Source, options ...Option) *Service {
	service := &Service{
		source:     source,
		client:     newSecureHTTPClient(),
		build:      platformversion.Get,
		now:        func() time.Time { return time.Now().UTC() },
		cacheTTL:   defaultCacheTTL,
		failureTTL: defaultFailureTTL,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func WithHTTPClient(client HTTPDoer) Option {
	return func(service *Service) {
		if client != nil {
			service.client = client
		}
	}
}

func WithBuildProvider(provider func() platformversion.BuildInfo) Option {
	return func(service *Service) {
		if provider != nil {
			service.build = provider
		}
	}
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

func WithCacheTTLs(success, failure time.Duration) Option {
	return func(service *Service) {
		if success > 0 {
			service.cacheTTL = success
		}
		if failure > 0 {
			service.failureTTL = failure
		}
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) {
		service.logger = logger
	}
}

// Status is available to every actor allowed into the admin area. The
// checker itself still owns this authorization so non-HTTP callers cannot
// accidentally turn it into a public probe.
func (s *Service) Status(ctx context.Context, actor identity.Actor) (Status, error) {
	if !actor.Can(identity.PermissionAdminAccess) {
		return Status{}, identity.ErrPermissionDenied
	}
	return s.check(ctx, false)
}

// CheckNow is reserved for site-settings managers because it performs an
// outbound request and deliberately bypasses the normal cache.
func (s *Service) CheckNow(ctx context.Context, actor identity.Actor) (Status, error) {
	if !actor.Can(identity.PermissionSettingsSiteManage) {
		return Status{}, identity.ErrPermissionDenied
	}
	return s.check(ctx, true)
}

func (s *Service) check(ctx context.Context, force bool) (Status, error) {
	if s == nil || s.source == nil {
		return Status{}, errors.New("system updates source is unavailable")
	}
	mirror, err := s.source.GitHubMirrorURL(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("read GitHub release source: %w", err)
	}
	build := s.build()
	key := strings.TrimSpace(build.Version) + "|" + strings.TrimSpace(mirror)
	now := s.now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.cacheKey == key && !s.expiresAt.IsZero() && now.Before(s.expiresAt) {
		return s.cached, nil
	}

	status := s.fetch(ctx, build, mirror, now)
	ttl := s.cacheTTL
	if status.State == StateUnavailable {
		ttl = s.failureTTL
	}
	s.cached = status
	s.cacheKey = key
	s.expiresAt = now.Add(ttl)
	return status, nil
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

func (s *Service) fetch(ctx context.Context, build platformversion.BuildInfo, mirror string, now time.Time) Status {
	status := Status{
		State:            StateUnavailable,
		CurrentVersion:   strings.TrimSpace(build.Version),
		CurrentCommit:    strings.TrimSpace(build.Commit),
		CheckedAt:        now,
		Source:           SourceOfficial,
		MirrorConfigured: strings.TrimSpace(mirror) != "",
	}
	if status.MirrorConfigured {
		status.Source = SourceMirror
	}

	current, ok := parseVersion(status.CurrentVersion)
	if !ok {
		status.State = StateDevelopment
		status.ErrorCode = ""
		return status
	}

	endpoint, err := releaseEndpoint(mirror)
	if err != nil {
		status.ErrorCode = ErrorInvalidSource
		return status
	}
	releases, err := s.fetchReleases(ctx, endpoint)
	if err != nil {
		status.ErrorCode = errorCode(err)
		if s.logger != nil {
			s.logger.WarnContext(ctx, "system update check failed", "error", err)
		}
		return status
	}
	includePrerelease := current.Prerelease() != ""
	latest, latestVersion, ok := chooseLatestRelease(releases, includePrerelease)
	if !ok {
		status.ErrorCode = ErrorNoRelease
		return status
	}
	status.LatestTag = latest.TagName
	status.LatestVersion = latestVersion.String()
	status.ReleaseName = strings.TrimSpace(latest.Name)
	status.ReleaseURL = safeReleaseURL(latest.HTMLURL, latest.TagName)
	status.PublishedAt = strings.TrimSpace(latest.PublishedAt)
	if latestVersion.GreaterThan(current) {
		status.State = StateUpdate
		status.UpdateAvailable = true
	} else {
		status.State = StateCurrent
	}
	return status
}

func (s *Service) fetchReleases(ctx context.Context, endpoint string) ([]githubRelease, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SForum-Version-Checker/1.0")
	response, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request release list: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("release endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read release response: %w", err)
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err == nil {
		return releases, nil
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil || strings.TrimSpace(release.TagName) == "" {
		return nil, fmt.Errorf("decode release response: %w", err)
	}
	return []githubRelease{release}, nil
}

func chooseLatestRelease(releases []githubRelease, includePrerelease bool) (githubRelease, *semver.Version, bool) {
	type candidate struct {
		release githubRelease
		version *semver.Version
	}
	items := make([]candidate, 0, len(releases))
	for _, release := range releases {
		if release.Draft {
			continue
		}
		version, ok := parseVersion(release.TagName)
		if !ok || (!includePrerelease && (release.Prerelease || version.Prerelease() != "")) {
			continue
		}
		items = append(items, candidate{release: release, version: version})
	}
	if len(items) == 0 {
		return githubRelease{}, nil, false
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].version.Equal(items[j].version) {
			return items[i].release.PublishedAt > items[j].release.PublishedAt
		}
		return items[i].version.GreaterThan(items[j].version)
	})
	return items[0].release, items[0].version, true
}

func parseVersion(value string) (*semver.Version, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
	if value == "" {
		return nil, false
	}
	parsed, err := semver.StrictNewVersion(value)
	return parsed, err == nil
}

func releaseEndpoint(mirror string) (string, error) {
	official := officialAPIBaseURL + "/repos/" + RepositoryOwner + "/" + RepositoryName + "/releases?per_page=30"
	original := strings.TrimSpace(mirror)
	if original == "" {
		return official, nil
	}
	template := strings.Contains(original, "{url}") || strings.Contains(original, "{url_encoded}")
	mirror = original
	if strings.Contains(mirror, "{url_encoded}") {
		mirror = strings.ReplaceAll(mirror, "{url_encoded}", url.QueryEscape(official))
	}
	if strings.Contains(mirror, "{url}") {
		mirror = strings.ReplaceAll(mirror, "{url}", official)
	}
	if strings.ContainsAny(mirror, "{}") {
		return "", errors.New("release source contains an unsupported placeholder")
	}
	parsed, err := url.Parse(mirror)
	if err != nil || parsed.Host == "" || parsed.User != nil || !strings.EqualFold(parsed.Scheme, "https") || !publicHost(parsed.Hostname()) {
		return "", errors.New("release source must be an HTTPS public host")
	}
	if !template {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/repos/" + RepositoryOwner + "/" + RepositoryName + "/releases"
		query := parsed.Query()
		query.Set("per_page", "30")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	return mirror, nil
}

func safeReleaseURL(raw, tag string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && parsed != nil && strings.EqualFold(parsed.Scheme, "https") && publicHost(parsed.Hostname()) {
		return parsed.String()
	}
	return "https://github.com/" + RepositoryOwner + "/" + RepositoryName + "/releases/tag/" + url.PathEscape(strings.TrimSpace(tag))
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "decode release response") {
		return ErrorResponse
	}
	return ErrorRequest
}

func publicHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	return true
}
