package options

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestSystemUpdatesMirrorDefaultsToOfficialSource(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	source := NewSystemUpdatesSource(service)

	value, err := source.GitHubMirrorURL(context.Background())
	if err != nil {
		t.Fatalf("GitHubMirrorURL returned error: %v", err)
	}
	if value != "" {
		t.Fatalf("expected the official source sentinel to be empty, got %q", value)
	}
}

func TestSystemUpdatesMirrorAcceptsAPIBaseAndURLTemplates(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()
	for _, value := range []string{
		"https://mirror.example.com/github-api/",
		"https://proxy.example.com/{url}",
		"https://proxy.example.com/?target={url_encoded}",
	} {
		updated, err := service.Update(context.Background(), actor, UpdateInput{Name: NameSystemUpdatesGitHubMirrorURL, Value: value})
		if err != nil {
			t.Fatalf("expected mirror %q to be accepted: %v", value, err)
		}
		if updated.Value == "" {
			t.Fatalf("expected mirror %q to be retained", value)
		}
	}
}

func TestSystemUpdatesMirrorRejectsUnsafeOrMalformedSources(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := settingsActor()
	for _, value := range []string{
		"http://mirror.example.com",
		"https://localhost:8080",
		"https://127.0.0.1:8080",
		"https://mirror.example.com/{unknown}",
		"https://mirror.example.com/{url}/{url}",
	} {
		if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameSystemUpdatesGitHubMirrorURL, Value: value}); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("expected mirror %q to be rejected, got %v", value, err)
		}
	}
}

func TestSystemUpdatesMirrorRequiresSiteSettingsPermission(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)
	actor := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
	if _, err := service.Update(context.Background(), actor, UpdateInput{Name: NameSystemUpdatesGitHubMirrorURL, Value: "https://mirror.example.com"}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected a denied mirror update, got %v", err)
	}
}
