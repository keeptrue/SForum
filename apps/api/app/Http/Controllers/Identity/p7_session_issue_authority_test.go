package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

type p7SessionIssueAcceptedContextKey struct{}
type p7SessionIssueEffectContextKey struct{}

type p7SessionIssueProbe struct {
	acceptActive atomic.Bool
	invokes      atomic.Int32
	accepts      atomic.Int32
	fences       atomic.Int32
	admissions   atomic.Int32
	effects      atomic.Int32
	actorUserID  atomic.Int64

	mu         sync.Mutex
	stages     map[string]int
	violations []string
	audits     []identity.LoginAudit
	sessions   []authsession.SessionRecordInput
	secondErr  error
}

func newP7SessionIssueProbe() *p7SessionIssueProbe {
	return &p7SessionIssueProbe{stages: map[string]int{}}
}

func (p *p7SessionIssueProbe) observe(ctx context.Context, stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stages[stage]++
	if !p.acceptActive.Load() {
		p.violations = append(p.violations, stage+": outside exact accept")
	}
	if ctx == nil || ctx.Value(p7SessionIssueAcceptedContextKey{}) != p {
		p.violations = append(p.violations, stage+": accepted context not propagated")
	}
	if ctx == nil || ctx.Value(p7SessionIssueEffectContextKey{}) != p {
		p.violations = append(p.violations, stage+": admitted effect context not propagated")
	}
}

func (p *p7SessionIssueProbe) stageCalls(stage string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stages[stage]
}

func (p *p7SessionIssueProbe) snapshot() ([]string, []identity.LoginAudit, []authsession.SessionRecordInput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.violations...),
		append([]identity.LoginAudit(nil), p.audits...),
		append([]authsession.SessionRecordInput(nil), p.sessions...),
		p.secondErr
}

// p7SessionIssueStore reuses the existing controller fixture while observing
// the three Host mutations that must remain inside exact policy admission.
type p7SessionIssueStore struct {
	*sessionTestStore
	probe           *p7SessionIssueProbe
	tokenVersionErr error
}

func (s *p7SessionIssueStore) GetUserTokenVersion(ctx context.Context, userID int64) (int64, error) {
	s.probe.observe(ctx, "begin")
	if s.tokenVersionErr != nil {
		return 0, s.tokenVersionErr
	}
	s.sessionTestStore.mu.Lock()
	defer s.sessionTestStore.mu.Unlock()
	current, ok := s.sessionTestStore.users[userID]
	if !ok {
		return 0, identity.ErrUserNotFound
	}
	return current.CurrentTokenVersion, nil
}

func (s *p7SessionIssueStore) RecordLoginAudit(ctx context.Context, input identity.LoginAudit) error {
	s.probe.observe(ctx, "audit")
	s.probe.mu.Lock()
	s.probe.audits = append(s.probe.audits, input)
	s.probe.mu.Unlock()
	return nil
}

func (s *p7SessionIssueStore) CreateSession(ctx context.Context, input authsession.SessionRecordInput) error {
	s.probe.observe(ctx, "save")
	s.probe.mu.Lock()
	s.probe.sessions = append(s.probe.sessions, input)
	s.probe.mu.Unlock()
	return s.sessionTestStore.CreateSession(ctx, input)
}

type p7SessionIssuePolicyStore struct {
	resolution identity.IdentitySessionPolicyResolution
	probe      *p7SessionIssueProbe
	stale      bool
}

func (s *p7SessionIssuePolicyStore) Current(context.Context) (identity.IdentitySessionPolicySelection, error) {
	return identity.IdentitySessionPolicySelection{}, errors.New("unused")
}

func (s *p7SessionIssuePolicyStore) Candidate(context.Context, string) (identity.IdentitySessionPolicyEvidence, error) {
	return identity.IdentitySessionPolicyEvidence{}, errors.New("unused")
}

func (s *p7SessionIssuePolicyStore) Resolve(context.Context) (identity.IdentitySessionPolicyResolution, error) {
	return s.resolution, nil
}

func (s *p7SessionIssuePolicyStore) Select(context.Context, identity.SelectIdentitySessionPolicyInput) (identity.IdentitySessionPolicyMutation, error) {
	return identity.IdentitySessionPolicyMutation{}, errors.New("unused")
}

func (s *p7SessionIssuePolicyStore) Reset(context.Context, identity.ResetIdentitySessionPolicyInput) (identity.IdentitySessionPolicyMutation, error) {
	return identity.IdentitySessionPolicyMutation{}, errors.New("unused")
}

func (s *p7SessionIssuePolicyStore) ListEvents(context.Context, int) ([]identity.IdentitySessionPolicyEvent, error) {
	return nil, errors.New("unused")
}

func (s *p7SessionIssuePolicyStore) RunIfCurrent(
	ctx context.Context,
	expected identity.IdentitySessionPolicyResolution,
	authority identity.IdentitySessionAuthority,
	effect func(context.Context) error,
) error {
	s.probe.admissions.Add(1)
	if expected.PolicyID != s.resolution.PolicyID || expected.RegistryDigest != s.resolution.RegistryDigest ||
		expected.Selection == nil || s.resolution.Selection == nil ||
		expected.Selection.Revision != s.resolution.Selection.Revision ||
		expected.Provider == nil || s.resolution.Provider == nil ||
		expected.Provider.Artifact.RuntimeInstanceID != s.resolution.Provider.Artifact.RuntimeInstanceID {
		return identity.ErrIdentitySessionPolicyDeclarationStale
	}
	if authority.UserID != s.probe.actorUserID.Load() || authority.TokenVersion != 0 {
		return identity.ErrIdentitySessionPolicyDeclarationStale
	}
	if ctx == nil || ctx.Value(p7SessionIssueAcceptedContextKey{}) != s.probe || !s.probe.acceptActive.Load() {
		s.probe.mu.Lock()
		s.probe.violations = append(s.probe.violations, "admission: outside accepted callback context")
		s.probe.mu.Unlock()
		return identity.ErrIdentitySessionPolicyDeclarationStale
	}
	if s.stale {
		return identity.ErrIdentitySessionPolicyDeclarationStale
	}
	s.probe.effects.Add(1)
	return effect(context.WithValue(ctx, p7SessionIssueEffectContextKey{}, s.probe))
}

type p7SessionIssueInvoker struct {
	probe       *p7SessionIssueProbe
	disposition string
	duplicate   bool
}

func (i *p7SessionIssueInvoker) InvokeExact(
	ctx context.Context,
	provider identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	input map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	i.probe.invokes.Add(1)
	i.probe.actorUserID.Store(actorUserID)
	if operation != "session.evaluate" || provider.ID != "p7.session.provider" ||
		input["purpose"] != identity.SessionEvaluationPurposeIssue || input["userId"] != actorUserID {
		return errors.New("unexpected exact session policy invocation")
	}
	fence := func() error {
		i.probe.fences.Add(1)
		return nil
	}
	acceptedCtx := context.WithValue(ctx, p7SessionIssueAcceptedContextKey{}, i.probe)
	i.probe.acceptActive.Store(true)
	defer i.probe.acceptActive.Store(false)
	i.probe.accepts.Add(1)
	firstErr := accept(acceptedCtx, map[string]any{"disposition": i.disposition}, fence)
	if i.duplicate {
		i.probe.accepts.Add(1)
		secondErr := accept(acceptedCtx, map[string]any{"disposition": i.disposition}, fence)
		i.probe.mu.Lock()
		i.probe.secondErr = secondErr
		i.probe.mu.Unlock()
	}
	return firstErr
}

func p7SessionIssueResolution() identity.IdentitySessionPolicyResolution {
	provider := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID:              "p7.session.provider",
			ContractVersion: "1",
			Kind:            identityregistry.ProviderKindSession,
			Handler:         "session",
			Operations: []identityregistry.ProviderOperation{{
				Name:          "session.evaluate",
				InputSchema:   "schemas/session-input.json",
				OutputSchema:  "schemas/session-output.json",
				TimeoutMS:     1_000,
				FailurePolicy: identityregistry.ProviderFailureFailClosed,
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID:       "p7-session-test",
			ExtensionVersion:  "1.0.0",
			VersionID:         7,
			PackageDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RuntimeInstanceID: "p7-session-runtime",
		},
	}
	selection := &identity.IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: identity.IdentitySessionPolicyEvidence{
			PolicyID:                "p7.session.policy",
			ProviderContractVersion: "1",
			OwnerExtensionID:        provider.Artifact.ExtensionID,
			OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
			OwnerPackageDigest:      provider.Artifact.PackageDigest,
			DeclarationRevision:     3,
		},
		Revision: 3,
	}
	return identity.IdentitySessionPolicyResolution{
		PolicyID:         selection.PolicyID,
		Source:           identity.IdentitySessionPolicySourcePlugin,
		Selection:        selection,
		Provider:         &provider,
		RegistryRevision: 9,
		RegistryDigest:   "p7-session-registry-digest",
	}
}

type p7SessionIssueScenario struct {
	disposition   string
	stale         bool
	callbackErr   error
	duplicate     bool
	wantStatus    int
	wantReason    string
	wantAdmission int32
	wantEffect    int32
	wantBegin     int
	wantAudit     int
	wantSave      int
}

func newP7SessionIssueApp(
	t *testing.T,
	flow string,
	scenario p7SessionIssueScenario,
) (*fiber.App, *p7SessionIssueProbe) {
	t.Helper()
	probe := newP7SessionIssueProbe()
	store := &p7SessionIssueStore{
		sessionTestStore: newSessionTestStore(),
		probe:            probe,
		tokenVersionErr:  scenario.callbackErr,
	}
	service := identity.NewService(store)
	if flow == "login" {
		if _, err := service.Register(t.Context(), identity.RegisterInput{
			Username: "p7alice", Email: "p7alice@example.com", Password: "correct horse battery staple",
		}); err != nil {
			t.Fatalf("seed login user: %v", err)
		}
	}

	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "p7-session-test-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	policyStore := &p7SessionIssuePolicyStore{
		resolution: p7SessionIssueResolution(),
		probe:      probe,
		stale:      scenario.stale,
	}
	evaluator, err := identity.NewSessionPolicyEvaluator(policyStore, &p7SessionIssueInvoker{
		probe: probe, disposition: scenario.disposition, duplicate: scenario.duplicate,
	})
	if err != nil {
		t.Fatalf("new session policy evaluator: %v", err)
	}
	controller := NewControllerWithAuthSessions(service, manager, nil).WithSessionPolicyEvaluator(evaluator)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, probe
}

func performP7SessionIssue(t *testing.T, app *fiber.App, flow string) *nethttp.Response {
	t.Helper()
	path := "/api/v1/auth/" + flow
	body := map[string]any{
		"username": "p7alice",
		"email":    "p7alice@example.com",
		"login":    "p7alice",
		"password": "correct horse battery staple",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(nethttp.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("%s request: %v", flow, err)
	}
	return response
}

func TestP7ControllerSessionIssueAuthorityMatrix(t *testing.T) {
	scenarios := map[string]p7SessionIssueScenario{
		"allow": {
			disposition: identity.SessionPolicyDispositionAllow, duplicate: true,
			wantAdmission: 1, wantEffect: 1, wantBegin: 1, wantAudit: 1, wantSave: 1,
		},
		"deny": {
			disposition: identity.SessionPolicyDispositionDeny,
			wantStatus:  nethttp.StatusForbidden, wantReason: identity.CodeSessionPolicyDenied,
		},
		"step_up": {
			disposition: identity.SessionPolicyDispositionStepUp,
			wantStatus:  nethttp.StatusUnprocessableEntity, wantReason: "human_verification.required",
		},
		"stale": {
			disposition: identity.SessionPolicyDispositionAllow, stale: true,
			wantStatus: nethttp.StatusServiceUnavailable, wantReason: identity.CodeSessionUnavailable,
			wantAdmission: 1,
		},
		"callback_failure": {
			disposition: identity.SessionPolicyDispositionAllow,
			callbackErr: errors.New("token version lookup failed inside accepted callback"),
			wantStatus:  nethttp.StatusServiceUnavailable, wantReason: identity.CodeSessionUnavailable,
			wantAdmission: 1, wantEffect: 1, wantBegin: 1,
		},
	}

	for _, flow := range []string{"register", "login"} {
		flow := flow
		for name, scenario := range scenarios {
			name, scenario := name, scenario
			t.Run(flow+"/"+name, func(t *testing.T) {
				app, probe := newP7SessionIssueApp(t, flow, scenario)
				response := performP7SessionIssue(t, app, flow)
				defer response.Body.Close()

				wantStatus := scenario.wantStatus
				if name == "allow" {
					if flow == "register" {
						wantStatus = nethttp.StatusCreated
					} else {
						wantStatus = nethttp.StatusOK
					}
				}
				if response.StatusCode != wantStatus {
					t.Fatalf("status=%d want=%d", response.StatusCode, wantStatus)
				}
				if name == "allow" {
					if cookies := response.Cookies(); len(cookies) != 1 || cookies[0].Value == "" || cookies[0].MaxAge < 0 {
						t.Fatalf("allow cookies=%#v", cookies)
					}
				} else {
					if cookies := response.Cookies(); len(cookies) != 0 {
						t.Fatalf("failed issue signed cookies=%#v", cookies)
					}
					var envelope struct {
						Data struct {
							Reason string `json:"reason"`
						} `json:"data"`
					}
					if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
						t.Fatalf("decode error response: %v", err)
					}
					if envelope.Data.Reason != scenario.wantReason {
						t.Fatalf("reason=%q want=%q", envelope.Data.Reason, scenario.wantReason)
					}
				}

				if got := probe.invokes.Load(); got != 1 {
					t.Fatalf("exact invokes=%d want=1", got)
				}
				wantAccepts := int32(1)
				if scenario.duplicate {
					wantAccepts = 2
				}
				if got := probe.accepts.Load(); got != wantAccepts {
					t.Fatalf("accept attempts=%d want=%d", got, wantAccepts)
				}
				if got := probe.fences.Load(); got != 1 {
					t.Fatalf("exact fences=%d want=1", got)
				}
				if got := probe.admissions.Load(); got != scenario.wantAdmission {
					t.Fatalf("authority admissions=%d want=%d", got, scenario.wantAdmission)
				}
				if got := probe.effects.Load(); got != scenario.wantEffect {
					t.Fatalf("Host callbacks=%d want=%d", got, scenario.wantEffect)
				}
				for stage, want := range map[string]int{
					"begin": scenario.wantBegin,
					"audit": scenario.wantAudit,
					"save":  scenario.wantSave,
				} {
					if got := probe.stageCalls(stage); got != want {
						t.Fatalf("%s calls=%d want=%d", stage, got, want)
					}
				}

				violations, audits, sessions, secondErr := probe.snapshot()
				if len(violations) != 0 {
					t.Fatalf("exact callback violations=%v", violations)
				}
				if scenario.duplicate && !errors.Is(secondErr, identity.ErrSessionPolicyEvaluationInvalid) {
					t.Fatalf("duplicate accept error=%v", secondErr)
				}
				if name == "allow" {
					wantAction := identity.AuditActionLogin
					if flow == "register" {
						wantAction = identity.AuditActionRegister
					}
					if len(audits) != 1 || len(sessions) != 1 || audits[0].Action != wantAction ||
						audits[0].UserID != sessions[0].UserID || audits[0].SessionHash == "" ||
						audits[0].SessionHash != sessions[0].SessionHash {
						t.Fatalf("audit=%#v sessions=%#v", audits, sessions)
					}
				}
			})
		}
	}
}
