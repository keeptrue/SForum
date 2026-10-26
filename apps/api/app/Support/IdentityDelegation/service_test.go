package identitydelegation_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	identitydelegation "github.com/keeptrue/sforum/apps/api/app/Support/IdentityDelegation"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type memoryStore struct {
	mu   sync.Mutex
	rows map[string]identitydelegation.SubjectRecord
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rows: map[string]identitydelegation.SubjectRecord{}}
}

func (s *memoryStore) Put(_ context.Context, record identitydelegation.SubjectRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[record.Subject] = record
	return nil
}

func (s *memoryStore) Get(_ context.Context, binding identitydelegation.ArtifactBinding, subject string) (identitydelegation.SubjectRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.rows[subject]
	if !ok || record.Binding != binding {
		return identitydelegation.SubjectRecord{}, identitydelegation.ErrSubjectNotFound
	}
	return record, nil
}

func testBinding() identitydelegation.ArtifactBinding {
	return identitydelegation.ArtifactBinding{
		ExtensionID: "sforum.oauth-provider", ExtensionVersion: "1.0.0",
		PackageDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImpactDigest:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func TestIssueProjectsOnlyAllowlistedClaimsAndBindsExactArtifact(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	service := identitydelegation.NewService(newMemoryStore()).WithClock(fixedClock{now: now})
	identity, err := service.Issue(context.Background(), testBinding(), identitydelegation.ActorProjectionInput{
		UserID: 42, Status: identitydelegation.ActorStatusActive, Username: "alice", DisplayName: "Alice",
		Locale: "zh-CN", Email: "alice@example.test", EmailVerified: true,
	}, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject == "" || identity.Email != "alice@example.test" || !identity.EmailVerified || identity.AuthTime.IsZero() {
		t.Fatalf("projection=%#v", identity)
	}
	resolved, err := service.Resolve(context.Background(), testBinding(), identity.Subject)
	if err != nil || resolved != identity {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	stale := testBinding()
	stale.PackageDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := service.Resolve(context.Background(), stale, identity.Subject); !errors.Is(err, identitydelegation.ErrSubjectNotFound) {
		t.Fatalf("stale artifact err=%v", err)
	}
}

func TestIssueFailsClosedForInactiveActorAndUnverifiedEmail(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	service := identitydelegation.NewService(newMemoryStore()).WithClock(fixedClock{now: now})
	_, err := service.Issue(context.Background(), testBinding(), identitydelegation.ActorProjectionInput{
		UserID: 42, Status: "banned", Username: "alice", Email: "alice@example.test", EmailVerified: true,
	}, now)
	if !errors.Is(err, identitydelegation.ErrActorUnavailable) {
		t.Fatalf("inactive actor err=%v", err)
	}
	identity, err := service.Issue(context.Background(), testBinding(), identitydelegation.ActorProjectionInput{
		UserID: 42, Status: identitydelegation.ActorStatusActive, Username: "alice",
		Email: "alice@example.test", EmailVerified: false,
	}, now)
	if err != nil || identity.Email != "" || identity.EmailVerified {
		t.Fatalf("unverified email projection=%#v err=%v", identity, err)
	}
}
