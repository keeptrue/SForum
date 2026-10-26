package consentbridge_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	consentbridge "github.com/keeptrue/sforum/apps/api/app/Support/ConsentBridge"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type memoryStore struct {
	mu   sync.Mutex
	rows map[string]consentbridge.TransactionRecord
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rows: map[string]consentbridge.TransactionRecord{}}
}

func (s *memoryStore) Put(_ context.Context, row consentbridge.TransactionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.Frame.TransactionID] = row
	return nil
}
func (s *memoryStore) Get(_ context.Context, id string) (consentbridge.TransactionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return consentbridge.TransactionRecord{}, consentbridge.ErrInvalid
	}
	return row, nil
}
func (s *memoryStore) Consume(_ context.Context, id string, when time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return consentbridge.ErrInvalid
	}
	if row.UsedAt != nil {
		return consentbridge.ErrReplay
	}
	row.UsedAt = &when
	s.rows[id] = row
	return nil
}

func testBinding() consentbridge.ArtifactBinding {
	return consentbridge.ArtifactBinding{ExtensionID: "sforum.oauth-provider", ExtensionVersion: "1.0.0", PackageDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ImpactDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
}

func TestConsentBindsActorSessionArtifactAndCSRFAndIsOneUse(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	service := consentbridge.NewService(store).WithClock(fixedClock{now: now})
	frame, err := service.Begin(context.Background(), consentbridge.BeginInput{
		ActorUserID: 42, SessionFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Binding: testBinding(),
		Client: consentbridge.ClientDescriptor{ClientID: "client-1", Name: "Demo App"}, RedirectURI: "https://client.example/callback", Scopes: []string{"email", "openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := service.Decide(context.Background(), 43, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", testBinding(), frame.TransactionID, frame.CSRFToken, consentbridge.Decision{Approved: true, Scopes: []string{"email", "openid"}})
	if !errors.Is(wrong, consentbridge.ErrActorMismatch) {
		t.Fatalf("wrong actor err=%v", wrong)
	}
	if err := service.Decide(context.Background(), 42, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", testBinding(), frame.TransactionID, frame.CSRFToken, consentbridge.Decision{Approved: true, Scopes: []string{"email", "openid"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Decide(context.Background(), 42, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", testBinding(), frame.TransactionID, frame.CSRFToken, consentbridge.Decision{Approved: true, Scopes: []string{"email", "openid"}}); !errors.Is(err, consentbridge.ErrReplay) {
		t.Fatalf("replay err=%v", err)
	}
}

func TestConsentRequiresRecentAuthWhenRequested(t *testing.T) {
	service := consentbridge.NewService(newMemoryStore())
	_, err := service.Begin(context.Background(), consentbridge.BeginInput{ActorUserID: 42, SessionFingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Binding: testBinding(), Client: consentbridge.ClientDescriptor{ClientID: "client-1", Name: "Demo App"}, RedirectURI: "https://client.example/callback", Scopes: []string{"openid"}, RequireRecentAuth: true})
	if !errors.Is(err, consentbridge.ErrRecentAuth) {
		t.Fatalf("recent auth err=%v", err)
	}
}
