package options

import (
	"context"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const systemUpdatesGitHubMirrorMaxRunes = 2048

func init() {
	optionDefinitions = append(optionDefinitions, optionDefinition{
		name:             NameSystemUpdatesGitHubMirrorURL,
		managePermission: identity.PermissionSettingsSiteManage,
	})
}

type systemUpdatesOptionReader interface {
	InternalValues(context.Context) (map[string]string, error)
}

type SystemUpdatesSource struct {
	options systemUpdatesOptionReader
}

func NewSystemUpdatesSource(options systemUpdatesOptionReader) *SystemUpdatesSource {
	return &SystemUpdatesSource{options: options}
}

// GitHubMirrorURL returns the normalized release source override. An empty
// value deliberately means the official GitHub API endpoint.
func (s *SystemUpdatesSource) GitHubMirrorURL(ctx context.Context) (string, error) {
	if s == nil || s.options == nil {
		return "", nil
	}
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return "", err
	}
	return values[NameSystemUpdatesGitHubMirrorURL], nil
}

func normalizeSystemUpdatesGitHubMirrorURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > systemUpdatesGitHubMirrorMaxRunes {
		return "", false
	}
	if strings.Count(value, "{url}") > 1 || strings.Count(value, "{url_encoded}") > 1 {
		return "", false
	}
	if strings.Contains(value, "{url}") && strings.Contains(value, "{url_encoded}") {
		return "", false
	}

	candidate := value
	const officialEndpoint = "https://api.github.com/repos/keeptrue/SForum/releases?per_page=30"
	if strings.Contains(candidate, "{url_encoded}") {
		candidate = strings.ReplaceAll(candidate, "{url_encoded}", url.QueryEscape(officialEndpoint))
	}
	if strings.Contains(candidate, "{url}") {
		candidate = strings.ReplaceAll(candidate, "{url}", officialEndpoint)
	}
	if strings.ContainsAny(candidate, "{}") {
		return "", false
	}

	parsed, err := url.Parse(candidate)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	if !isPublicMirrorHost(parsed.Hostname()) {
		return "", false
	}
	if parsed.Path == "" && parsed.RawQuery == "" {
		return strings.TrimRight(value, "/"), true
	}
	return value, true
}

func isPublicMirrorHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	return true
}
