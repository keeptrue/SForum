package systemupdates_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	systemupdates "github.com/keeptrue/sforum/apps/api/app/Models/SystemUpdates"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

func TestStatusSelectsHighestStableReleaseAndCachesIt(t *testing.T) {
	client := &fakeHTTPClient{body: `[
		{"tag_name":"v1.1.0","name":"Older","html_url":"https://github.com/keeptrue/SForum/releases/tag/v1.1.0"},
		{"tag_name":"v1.3.0","name":"Newest","html_url":"https://github.com/keeptrue/SForum/releases/tag/v1.3.0","published_at":"2026-07-30T00:00:00Z"},
		{"tag_name":"v2.0.0-rc.1","prerelease":true}
	]`}
	service := systemupdates.NewService(
		fakeSource{},
		systemupdates.WithHTTPClient(client),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Name: "SForum", Version: "1.2.0"} }),
		systemupdates.WithClock(func() time.Time { return time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC) }),
	)

	status, err := service.Status(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.State != systemupdates.StateUpdate || !status.UpdateAvailable || status.LatestVersion != "1.3.0" || status.LatestTag != "v1.3.0" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Source != systemupdates.SourceOfficial || status.MirrorConfigured {
		t.Fatalf("unexpected source metadata: %#v", status)
	}
	if _, err := service.Status(context.Background(), adminActor()); err != nil {
		t.Fatalf("cached Status returned error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected one upstream request after cache hit, got %d", client.calls)
	}
}

func TestStatusRefreshesSuccessfulCacheAfterFiveMinutes(t *testing.T) {
	now := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	client := &fakeHTTPClient{body: `[{"tag_name":"v1.1.0"}]`}
	service := systemupdates.NewService(
		fakeSource{},
		systemupdates.WithHTTPClient(client),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "1.0.0"} }),
		systemupdates.WithClock(func() time.Time { return now }),
	)

	if _, err := service.Status(context.Background(), adminActor()); err != nil {
		t.Fatalf("initial Status returned error: %v", err)
	}
	now = now.Add(5*time.Minute - time.Second)
	if _, err := service.Status(context.Background(), adminActor()); err != nil {
		t.Fatalf("cached Status returned error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected successful status to remain cached before five minutes, got %d calls", client.calls)
	}

	now = now.Add(time.Second)
	if _, err := service.Status(context.Background(), adminActor()); err != nil {
		t.Fatalf("expired Status returned error: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("expected successful status to refresh at five minutes, got %d calls", client.calls)
	}
}

func TestStatusIncludesPrereleasesForPrereleaseBuilds(t *testing.T) {
	client := &fakeHTTPClient{body: `[
		{"tag_name":"v2.0.0","name":"Stable"},
		{"tag_name":"v2.1.0-alpha.2","name":"Preview","prerelease":true}
	]`}
	service := systemupdates.NewService(
		fakeSource{},
		systemupdates.WithHTTPClient(client),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "2.0.0-alpha.1"} }),
	)

	status, err := service.Status(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.LatestVersion != "2.1.0-alpha.2" || status.State != systemupdates.StateUpdate {
		t.Fatalf("expected preview release, got %#v", status)
	}
}

func TestStatusDoesNotCallUpstreamForDevelopmentBuild(t *testing.T) {
	client := &fakeHTTPClient{body: `[]`}
	service := systemupdates.NewService(
		fakeSource{},
		systemupdates.WithHTTPClient(client),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "dev-a1b2c"} }),
	)

	status, err := service.Status(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.State != systemupdates.StateDevelopment || status.UpdateAvailable || client.calls != 0 {
		t.Fatalf("unexpected development status: %#v, calls=%d", status, client.calls)
	}
}

func TestStatusUsesMirrorAndCheckNowBypassesCache(t *testing.T) {
	client := &fakeHTTPClient{body: `[{"tag_name":"v1.1.0","html_url":"https://github.com/keeptrue/SForum/releases/tag/v1.1.0"}]`}
	service := systemupdates.NewService(
		fakeSource{mirror: "https://mirror.example.com/api/"},
		systemupdates.WithHTTPClient(client),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "1.0.0"} }),
	)

	if _, err := service.Status(context.Background(), adminActor()); err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !strings.Contains(client.lastURL, "mirror.example.com/api/repos/keeptrue/SForum/releases") {
		t.Fatalf("unexpected mirror URL %q", client.lastURL)
	}
	if _, err := service.CheckNow(context.Background(), settingsActor()); err != nil {
		t.Fatalf("CheckNow returned error: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("expected CheckNow to bypass cache, got %d calls", client.calls)
	}
}

func TestStatusRejectsUnauthorizedActors(t *testing.T) {
	service := systemupdates.NewService(fakeSource{}, systemupdates.WithHTTPClient(&fakeHTTPClient{body: `[]`}))
	if _, err := service.Status(context.Background(), identity.Actor{ID: 1, Status: identity.UserStatusActive}); err != identity.ErrPermissionDenied {
		t.Fatalf("expected admin access denial, got %v", err)
	}
	if _, err := service.CheckNow(context.Background(), adminActor()); err != identity.ErrPermissionDenied {
		t.Fatalf("expected settings permission denial, got %v", err)
	}
}

func TestStatusReturnsSafeUnavailableStateForInvalidSource(t *testing.T) {
	service := systemupdates.NewService(
		fakeSource{mirror: "https://127.0.0.1:8080"},
		systemupdates.WithHTTPClient(&fakeHTTPClient{body: `[]`}),
		systemupdates.WithBuildProvider(func() platformversion.BuildInfo { return platformversion.BuildInfo{Version: "1.0.0"} }),
	)

	status, err := service.Status(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.State != systemupdates.StateUnavailable || status.ErrorCode != systemupdates.ErrorInvalidSource {
		t.Fatalf("unexpected unavailable status: %#v", status)
	}
}

type fakeSource struct{ mirror string }

func (s fakeSource) GitHubMirrorURL(context.Context) (string, error) { return s.mirror, nil }

type fakeHTTPClient struct {
	body    string
	calls   int
	lastURL string
}

func (c *fakeHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.calls++
	c.lastURL = request.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func adminActor() identity.Actor {
	return identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionAdminAccess: true}}
}

func settingsActor() identity.Actor {
	return identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}
}
