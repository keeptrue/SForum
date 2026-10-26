package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// T1E：provider 隔离、Safe Mode、disable/trust-revoke/artifact upgrade、
// state/ticket 过期、actor/session 不匹配、未授权 link 零持久化、
// unlink race、有意义双 provider 操作。

type t1eCountingLinkStore struct {
	mu        sync.Mutex
	linkCalls int
	// digests maps link id → subject digest（struct 故意不暴露 digest）。
	digests map[int64]string
	byKey   map[string]int64 // provider|digest → id
	byID    map[int64]ExternalIdentityLink
	nextID  int64
}

func newT1ECountingLinkStore() *t1eCountingLinkStore {
	return &t1eCountingLinkStore{
		digests: map[int64]string{},
		byKey:   map[string]int64{},
		byID:    map[int64]ExternalIdentityLink{},
		nextID:  1,
	}
}

func (s *t1eCountingLinkStore) Link(_ context.Context, input LinkExternalIdentityInput, fence ExternalIdentityLinkCommitFence) (ExternalIdentityLinkMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linkCalls++
	if fence != nil {
		if err := fence(); err != nil {
			return ExternalIdentityLinkMutation{}, err
		}
	}
	digest := input.ProviderSubjectDigest
	key := input.Provider.ID + "|" + digest
	id := s.nextID
	s.nextID++
	link := ExternalIdentityLink{
		ID: id, UserID: input.UserID, ProviderID: input.Provider.ID,
		Status:           ExternalIdentityLinkStatusActive,
		OwnerExtensionID: input.Provider.Artifact.ExtensionID, Revision: 1,
	}
	s.digests[id] = digest
	s.byKey[key] = id
	s.byID[id] = link
	return ExternalIdentityLinkMutation{Link: link}, nil
}
func (s *t1eCountingLinkStore) Unlink(_ context.Context, input TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.byID[input.LinkID]
	if !ok || link.Status != ExternalIdentityLinkStatusActive {
		return ExternalIdentityLinkMutation{}, ErrExternalIdentityLinkNotFound
	}
	if input.ExpectedRevision > 0 && link.Revision != input.ExpectedRevision {
		return ExternalIdentityLinkMutation{}, ErrExternalIdentityLinkStateConflict
	}
	link.Status = ExternalIdentityLinkStatusUnlinked
	link.Revision++
	s.byID[link.ID] = link
	return ExternalIdentityLinkMutation{Link: link}, nil
}
func (s *t1eCountingLinkStore) Erase(context.Context, TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error) {
	return ExternalIdentityLinkMutation{}, errors.New("unused")
}
func (s *t1eCountingLinkStore) Get(_ context.Context, id int64) (ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.byID[id]; ok {
		return l, nil
	}
	return ExternalIdentityLink{}, ErrExternalIdentityLinkNotFound
}
func (s *t1eCountingLinkStore) FindActive(_ context.Context, providerID, digest string) (ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byKey[providerID+"|"+digest]
	if !ok {
		return ExternalIdentityLink{}, ErrExternalIdentityLinkNotFound
	}
	l := s.byID[id]
	if l.Status != ExternalIdentityLinkStatusActive {
		return ExternalIdentityLink{}, ErrExternalIdentityLinkNotFound
	}
	return l, nil
}
func (s *t1eCountingLinkStore) ListUser(_ context.Context, userID int64) ([]ExternalIdentityLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExternalIdentityLink, 0)
	for _, l := range s.byID {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	return out, nil
}

func t1eLive(providerID, digest string, ops ...string) identityregistry.ProviderContribution {
	operations := make([]identityregistry.ProviderOperation, 0, len(ops))
	for _, name := range ops {
		operations = append(operations, identityregistry.ProviderOperation{Name: name})
	}
	if len(operations) == 0 {
		operations = []identityregistry.ProviderOperation{
			{Name: AuthOperationLoginStart}, {Name: AuthOperationLoginComplete},
			{Name: AuthOperationLinkStart}, {Name: AuthOperationLinkComplete},
			{Name: AuthOperationRegistrationStart}, {Name: AuthOperationRegistrationComplete},
		}
	}
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: providerID, ContractVersion: providerID + "@1",
			Kind: identityregistry.ProviderKindAuth, Operations: operations,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext." + providerID, ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "rt-" + providerID,
		},
	}
}

func TestT1E_TwoProvidersExecuteIndependentLinkAndLogin(t *testing.T) {
	// 有意义双 provider：分别完成 login（已绑定）与 link+login，而非仅 catalog 元数据。
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	digestA := strings.Repeat("1", 64)
	digestB := strings.Repeat("2", 64)
	liveA := t1eLive(multiProviderA, digestA)
	liveB := t1eLive(multiProviderB, digestB)
	links := newT1ECountingLinkStore()
	subjectA := "42"
	subjDigestA, err := ComputeSubjectDigest(multiProviderA, subjectA)
	if err != nil {
		t.Fatal(err)
	}
	links.byID[1] = ExternalIdentityLink{
		ID: 1, UserID: 10, ProviderID: multiProviderA,
		Status: ExternalIdentityLinkStatusActive, Revision: 1,
	}
	links.digests[1] = subjDigestA
	links.byKey[multiProviderA+"|"+subjDigestA] = 1
	links.nextID = 2

	activation := &fakeActivationStoreLite{items: map[string]ProviderActivation{
		multiProviderA: {
			ProviderID: multiProviderA, OwnerExtensionID: liveA.Artifact.ExtensionID,
			OwnerPackageDigest: digestA, LoginEnabled: true, LinkEnabled: true,
		},
		multiProviderB: {
			ProviderID: multiProviderB, OwnerExtensionID: liveB.Artifact.ExtensionID,
			OwnerPackageDigest: digestB, LoginEnabled: true, LinkEnabled: true,
		},
	}}
	users := map[int64]CurrentUser{
		10: {ID: 10, Username: "u10", Status: UserStatusActive, CurrentTokenVersion: 1},
		11: {ID: 11, Username: "u11", Status: UserStatusActive, CurrentTokenVersion: 1},
	}
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore: links, ActivationStore: activation,
		RecentAuth: fixedRecentAuth{ok: true},
		ProviderContribution: func(id string) (identityregistry.ProviderContribution, error) {
			switch id {
			case multiProviderA:
				return liveA, nil
			case multiProviderB:
				return liveB, nil
			default:
				return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
			}
		},
	}).WithCurrentUserLoader(func(_ context.Context, id int64) (CurrentUser, error) {
		if u, ok := users[id]; ok {
			return u, nil
		}
		return CurrentUser{}, ErrUserNotFound
	})
	ctx := context.Background()

	loginA, err := svc.CompleteLogin(ctx, ExternalAuthAssertion{
		ProviderID: multiProviderA, Operation: ExternalAuthOperationLogin,
		ProviderSubject: subjectA, OwnerPackageDigest: digestA,
		OwnerExtensionID: liveA.Artifact.ExtensionID,
	})
	if err != nil {
		t.Fatalf("login A: %v", err)
	}
	if loginA.User.ID != 10 {
		t.Fatalf("login A user=%d", loginA.User.ID)
	}

	_, err = svc.CompleteLogin(ctx, ExternalAuthAssertion{
		ProviderID: multiProviderB, Operation: ExternalAuthOperationLogin,
		ProviderSubject: "99", OwnerPackageDigest: digestB,
		OwnerExtensionID: liveB.Artifact.ExtensionID,
	})
	if !errors.Is(err, ErrExternalIdentityUnlinked) {
		t.Fatalf("login B unlinked: %v", err)
	}

	fp := SessionFingerprint("sid-b-link")
	linkB, err := svc.CompleteLink(ctx, ExternalAuthAssertion{
		ProviderID: multiProviderB, Operation: ExternalAuthOperationLink,
		ProviderSubject: "99", OwnerPackageDigest: digestB,
		OwnerExtensionID: liveB.Artifact.ExtensionID, CorrelationID: "c-b",
	}, 11, fp)
	if err != nil {
		t.Fatalf("link B: %v", err)
	}
	if linkB.LinkID == 0 || linkB.User.ID != 11 {
		t.Fatalf("link B result=%+v", linkB)
	}

	loginB, err := svc.CompleteLogin(ctx, ExternalAuthAssertion{
		ProviderID: multiProviderB, Operation: ExternalAuthOperationLogin,
		ProviderSubject: "99", OwnerPackageDigest: digestB,
		OwnerExtensionID: liveB.Artifact.ExtensionID,
	})
	if err != nil {
		t.Fatalf("login B after link: %v", err)
	}
	if loginB.User.ID != 11 {
		t.Fatalf("login B user=%d", loginB.User.ID)
	}

	// 关闭 A 不影响 B 的 login。
	activation.items[multiProviderA] = ProviderActivation{
		ProviderID: multiProviderA, OwnerExtensionID: liveA.Artifact.ExtensionID,
		OwnerPackageDigest: digestA, LoginEnabled: false, LinkEnabled: false,
	}
	if err := svc.RequireActivated(ctx, multiProviderA, ExternalAuthOperationLogin); !errors.Is(err, ErrExternalAuthOperationNotActivated) {
		t.Fatalf("A disabled: %v", err)
	}
	if err := svc.RequireActivated(ctx, multiProviderB, ExternalAuthOperationLogin); err != nil {
		t.Fatalf("B must remain available: %v", err)
	}
}

func TestT1E_SafeModeDisableArtifactAndRevoke(t *testing.T) {
	digest := strings.Repeat("3", 64)
	live := t1eLive("p.safe", digest)
	store := NewMemoryProviderActivationStore()
	ctx := context.Background()
	login := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p.safe", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, LoginEnabled: &login,
	}); err != nil {
		t.Fatal(err)
	}

	// Safe Mode
	safeSvc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		SafeMode:        func() bool { return true },
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	av, err := safeSvc.EvaluateOperationAvailability(ctx, "p.safe", ExternalAuthOperationLogin)
	if err != nil {
		t.Fatal(err)
	}
	if av.Available {
		t.Fatalf("Safe Mode must remove availability")
	}
	entries, err := safeSvc.ListEffectivePublicCatalog(ctx, identityregistry.New(), "zh-CN")
	if err != nil || len(entries) != 0 {
		t.Fatalf("Safe Mode catalog must be empty: %#v err=%v", entries, err)
	}

	// disable
	off := false
	act, _ := store.Get(ctx, "p.safe")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p.safe", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, ExpectedRevision: act.Revision, LoginEnabled: &off,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatal(err)
	}
	offSvc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	av, err = offSvc.EvaluateOperationAvailability(ctx, "p.safe", ExternalAuthOperationLogin)
	if err != nil {
		t.Fatal(err)
	}
	if av.Available {
		t.Fatalf("disabled login must not be available")
	}

	// re-enable then artifact upgrade
	on := true
	act, _ = store.Get(ctx, "p.safe")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p.safe", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, ExpectedRevision: act.Revision, LoginEnabled: &on,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatal(err)
	}
	// live digest 变了，activation 仍绑定旧 digest
	upgraded := live
	upgraded.Artifact.PackageDigest = strings.Repeat("9", 64)
	driftSvc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return upgraded, nil
		},
	})
	av, err = driftSvc.EvaluateOperationAvailability(ctx, "p.safe", ExternalAuthOperationLogin)
	if err != nil {
		t.Fatal(err)
	}
	if av.Available {
		t.Fatalf("artifact upgrade must remove availability until reactivation")
	}

	// trust revoke / Registry 不可见
	revokedSvc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		},
	})
	tx := CallbackTransaction{
		ProviderID: "p.safe", Operation: ExternalAuthOperationLogin,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerPackageDigest: digest,
		OwnerExtensionVersion: "1.0.0", ProviderContractVersion: "p.safe@1",
	}
	if _, err := revokedSvc.ValidateCallbackBeforeEffect(ctx, tx, "p.safe"); !errors.Is(err, ErrExternalAuthProviderUnavailable) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestT1E_StateAndTicketExpiry(t *testing.T) {
	ctx := context.Background()
	cb := NewInMemoryCallbackStateStore()
	if err := cb.Save(ctx, CallbackTransaction{
		State: "expired-state", ProviderID: "p1", Operation: ExternalAuthOperationLogin,
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Consume(ctx, "expired-state"); !errors.Is(err, ErrCallbackStateExpired) {
		t.Fatalf("state expiry: %v", err)
	}

	tickets := NewInMemoryRegistrationTicketStore()
	if err := tickets.Save(ctx, validRegistrationTicket("expired-ticket", time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ctx, "expired-ticket"); !errors.Is(err, ErrRegistrationTicketExpired) {
		t.Fatalf("ticket expiry: %v", err)
	}
}

func TestT1E_ActorSessionMismatchAndUnauthorizedLinkZeroWrite(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	digest := strings.Repeat("4", 64)
	providerID := "p.link"
	live := t1eLive(providerID, digest)
	links := newT1ECountingLinkStore()
	activation := &fakeActivationStoreLite{items: map[string]ProviderActivation{
		providerID: {
			ProviderID: providerID, OwnerExtensionID: live.Artifact.ExtensionID,
			OwnerPackageDigest: digest, LinkEnabled: true,
		},
	}}
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore: links, ActivationStore: activation,
		RecentAuth: fixedRecentAuth{ok: false},
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	tx := CallbackTransaction{
		ProviderID: providerID, Operation: ExternalAuthOperationLink, ActorUserID: 5,
		OwnerPackageDigest: digest, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerExtensionVersion: "1.0.0", ProviderContractVersion: providerID + "@1",
	}
	ctx := context.Background()
	if err := svc.AuthorizeLinkBeforePersist(ctx, tx, 5, SessionFingerprint("s1")); !errors.Is(err, ErrExternalAuthRecentAuthRequired) {
		t.Fatalf("recent-auth: %v", err)
	}
	if err := svc.AuthorizeLinkBeforePersist(ctx, tx, 9, SessionFingerprint("s1")); !errors.Is(err, ErrExternalAuthActorMismatch) {
		t.Fatalf("actor mismatch: %v", err)
	}
	if links.linkCalls != 0 {
		t.Fatalf("unauthorized path wrote links: %d", links.linkCalls)
	}
}

func TestT1E_UnlinkRaceRevisionConflict(t *testing.T) {
	links := newT1ECountingLinkStore()
	links.byID[7] = ExternalIdentityLink{
		ID: 7, UserID: 3, ProviderID: "p1",
		Status: ExternalIdentityLinkStatusActive, Revision: 1,
	}
	links.digests[7] = strings.Repeat("5", 64)
	links.byKey["p1|"+strings.Repeat("5", 64)] = 7
	// ExternalAuthStore=nil → CanUnlink 放行（测试路径）；仍校验 revision race。
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore:  links,
		RecentAuth: fixedRecentAuth{ok: true},
	})
	ctx := context.Background()
	fp := SessionFingerprint("unlink-sid")

	var firstErr, secondErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, firstErr = svc.Unlink(ctx, UnlinkExternalIdentityInput{
			UserID: 3, LinkID: 7, ExpectedRevision: 1, SessionFingerprint: fp, RequestID: "r1",
		})
	}()
	go func() {
		defer wg.Done()
		_, secondErr = svc.Unlink(ctx, UnlinkExternalIdentityInput{
			UserID: 3, LinkID: 7, ExpectedRevision: 1, SessionFingerprint: fp, RequestID: "r2",
		})
	}()
	wg.Wait()

	okCount := 0
	for _, err := range []error{firstErr, secondErr} {
		if err == nil {
			okCount++
			continue
		}
		if errors.Is(err, ErrExternalIdentityLinkStateConflict) ||
			errors.Is(err, ErrExternalIdentityLinkNotFound) {
			continue
		}
		t.Fatalf("unexpected unlink err: %v", err)
	}
	if okCount < 1 {
		t.Fatalf("expected at least one unlink success; first=%v second=%v", firstErr, secondErr)
	}
	got, _ := links.Get(ctx, 7)
	if got.Status == ExternalIdentityLinkStatusActive {
		t.Fatalf("link still active after concurrent unlink")
	}
}

func TestT1E_RegistrationTicketConsumeIsOneUse(t *testing.T) {
	// registration rollback 的 Host 侧边界：ticket 一次性；失败后不得重放断言。
	ctx := context.Background()
	tickets := NewInMemoryRegistrationTicketStore()
	tok := "reg-ticket-once"
	if err := tickets.Save(ctx, validRegistrationTicket(tok, time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ctx, tok); err == nil {
		t.Fatalf("ticket replay must fail")
	}
}
