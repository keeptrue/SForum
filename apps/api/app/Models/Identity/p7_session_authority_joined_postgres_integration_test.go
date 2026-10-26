package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/jackc/pgx/v5/pgxpool"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type p7JoinedSessionFlow string

const (
	p7JoinedIssue p7JoinedSessionFlow = "issue"
	p7JoinedRenew p7JoinedSessionFlow = "renew"
)

type p7JoinedOrder string

const (
	p7JoinedEffectFirst p7JoinedOrder = "effect_first"
	p7JoinedWriterFirst p7JoinedOrder = "writer_first"
)

type p7JoinedAuthorityWriter struct {
	name       string
	newHash    string
	reason     string
	wantStatus UserStatus
	write      func(context.Context, *p7JoinedSessionHarness) error
}

// TestP7IdentitySessionAuthorityPostgresJoinedMatrix proves the production
// issue/renew boundary against every Host identity writer that advances session
// authority. Explicit channels freeze both legal orders; no scheduler delay is
// used as evidence.
func TestP7IdentitySessionAuthorityPostgresJoinedMatrix(t *testing.T) {
	writers := []p7JoinedAuthorityWriter{
		{
			name: "password_reset", newHash: "p7-password-reset-hash",
			reason: RevokeReasonPasswordReset, wantStatus: UserStatusActive,
			write: func(ctx context.Context, h *p7JoinedSessionHarness) error {
				_, err := h.identity.ConfirmPasswordResetAtomic(
					ctx, h.resetTokenHash, "p7-password-reset-hash", RevokeReasonPasswordReset,
				)
				return err
			},
		},
		{
			name: "admin_status", reason: "admin_status_changed", wantStatus: UserStatusDisabled,
			write: func(ctx context.Context, h *p7JoinedSessionHarness) error {
				status := UserStatusDisabled
				_, err := h.identity.UpdateAdminUser(
					ctx, h.fixture.adminUserID, h.fixture.targetUserID,
					AdminUpdateUserInput{Status: &status},
				)
				return err
			},
		},
		{
			name: "revoke_all", reason: "admin_revoke", wantStatus: UserStatusActive,
			write: func(ctx context.Context, h *p7JoinedSessionHarness) error {
				_, err := h.identity.RevokeUserSessions(ctx, h.fixture.targetUserID, "admin_revoke")
				return err
			},
		},
	}

	for _, writer := range writers {
		writer := writer
		for _, flow := range []p7JoinedSessionFlow{p7JoinedIssue, p7JoinedRenew} {
			flow := flow
			for _, order := range []p7JoinedOrder{p7JoinedEffectFirst, p7JoinedWriterFirst} {
				order := order
				t.Run(fmt.Sprintf("%s/%s/%s", writer.name, flow, order), func(t *testing.T) {
					harness := newP7JoinedSessionHarness(t, flow)
					cookie := harness.prepareSession(flow)
					baselineSessions := harness.sessionCount()

					var operation p7JoinedHTTPResult
					var writerErr error
					switch order {
					case p7JoinedEffectFirst:
						operationResult := harness.startOperation(flow, cookie)
						harness.effect.awaitEntered(t, "accepted "+string(flow)+" effect")

						writerResult := harness.startWriter(writer)
						harness.mutation.awaitCalled(t, writer.name+" writer")
						awaitIdentitySessionPolicyLocalWriter(t, harness.policy)
						harness.mutation.assertNotAdmitted(t)
						harness.assertMainPoolAvailable()

						// The writer may be released now; the real shared gate still
						// keeps its callback behind the accepted effect.
						harness.mutation.release()
						harness.effect.release()
						operation = awaitP7JoinedHTTPResult(t, operationResult, string(flow))
						writerErr = awaitP7JoinedError(t, writerResult, writer.name)
					case p7JoinedWriterFirst:
						writerResult := harness.startWriter(writer)
						harness.mutation.awaitAdmitted(t, writer.name+" writer")
						harness.assertMainPoolAvailable()

						operationResult := harness.startOperation(flow, cookie)
						harness.effect.awaitCalled(t, string(flow)+" policy effect")
						awaitIdentitySessionPolicyEffectSlots(t, harness.policy, 1)
						harness.effect.assertNotEntered(t)

						// If a regression admits the callback after the writer, do not
						// leave the request hanging; the assertions below still fail.
						harness.effect.release()
						harness.mutation.release()
						writerErr = awaitP7JoinedError(t, writerResult, writer.name)
						operation = awaitP7JoinedHTTPResult(t, operationResult, string(flow))
					default:
						t.Fatalf("unknown joined order %q", order)
					}

					if writerErr != nil {
						t.Fatalf("%s writer: %v", writer.name, writerErr)
					}
					harness.assertOperation(order, flow, cookie, operation)
					harness.assertWriterFacts(writer, order, flow, baselineSessions)
				})
			}
		}
	}
}

func TestP7IdentitySessionIssueAllowsExternalOnlyUserWithoutCredential(t *testing.T) {
	harness := newP7JoinedSessionHarness(t, p7JoinedIssue)
	if _, err := harness.pool.Exec(harness.fixture.ctx, `
		DELETE FROM user_credentials WHERE user_id = $1
	`, harness.fixture.targetUserID); err != nil {
		t.Fatalf("delete local credential: %v", err)
	}
	baselineSessions := harness.sessionCount()

	operationResult := harness.startOperation(p7JoinedIssue, nil)
	harness.effect.awaitEntered(t, "external-only accepted issue effect")
	harness.effect.release()
	operation := awaitP7JoinedHTTPResult(t, operationResult, "external-only issue")
	if operation.err != nil {
		t.Fatalf("external-only issue transport: %v", operation.err)
	}
	if operation.response == nil {
		t.Fatal("external-only issue returned no response")
	}
	defer operation.response.Body.Close()
	if operation.outcome.err != nil || operation.response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("external-only issue status=%d outcome=%v", operation.response.StatusCode, operation.outcome.err)
	}
	if cookies := operation.response.Cookies(); len(cookies) != 1 {
		t.Fatalf("external-only issue cookies=%#v", cookies)
	}
	if got := harness.sessionCount(); got != baselineSessions+1 {
		t.Fatalf("external-only issue sessions=%d want %d", got, baselineSessions+1)
	}
	var loginAudits int
	if err := harness.pool.QueryRow(harness.fixture.ctx, `
		SELECT count(*) FROM audit_events
		WHERE target_user_id = $1 AND action = $2
	`, harness.fixture.targetUserID, AuditActionLogin).Scan(&loginAudits); err != nil {
		t.Fatal(err)
	}
	if loginAudits != 1 {
		t.Fatalf("external-only issue login audits=%d want 1", loginAudits)
	}
}

type p7JoinedSessionHarness struct {
	t              *testing.T
	fixture        *identityPersistencePGFixture
	pool           *pgxpool.Pool
	policy         *PostgresIdentitySessionPolicyStore
	effect         *p7ControlledSessionEffectStore
	mutation       *p7ControlledAuthorityMutationGate
	identity       *PostgresStore
	evaluator      *SessionPolicyEvaluator
	manager        *authsession.Manager
	app            *fiber.App
	resetTokenHash string
	invocations    atomic.Int32
	renewEffects   atomic.Int32
	issueOutcome   chan p7JoinedRouteOutcome
	renewOutcome   chan p7JoinedRouteOutcome
	replayOutcome  chan p7JoinedRouteOutcome
}

type p7JoinedRouteOutcome struct {
	err    error
	userID int64
	ok     bool
}

type p7JoinedHTTPResult struct {
	response *http.Response
	err      error
	outcome  p7JoinedRouteOutcome
}

func newP7JoinedSessionHarness(t *testing.T, flow p7JoinedSessionFlow) *p7JoinedSessionHarness {
	t.Helper()
	fixture := newIdentityPersistencePGFixture(t)
	installP7JoinedIdentitySchema(t, fixture)

	pool := fixture.newPool("p7-session-authority-" + string(flow))
	policy, err := NewPostgresIdentitySessionPolicyStore(pool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	selectIdentitySessionPolicyStore(t, fixture, policy)

	effect := newP7ControlledSessionEffectStore(policy)
	mutation := newP7ControlledAuthorityMutationGate(policy)
	identity := NewPostgresStore(pool).WithAuthorityMutationGate(mutation)
	harness := &p7JoinedSessionHarness{
		t: t, fixture: fixture, pool: pool, policy: policy, effect: effect,
		mutation: mutation, identity: identity,
		resetTokenHash: "p7-reset-token-" + fixture.schema,
		issueOutcome:   make(chan p7JoinedRouteOutcome, 1),
		renewOutcome:   make(chan p7JoinedRouteOutcome, 1),
		replayOutcome:  make(chan p7JoinedRouteOutcome, 1),
	}

	evaluator, err := NewSessionPolicyEvaluator(effect, p7JoinedAllowInvoker{calls: &harness.invocations})
	if err != nil {
		t.Fatal(err)
	}
	harness.evaluator = evaluator
	harness.manager = authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			RenewalInterval: time.Nanosecond,
			HashSecret:      "p7-session-authority-secret",
			TokenVersion:    identity.GetUserTokenVersion,
			SessionStore:    identity,
			RenewalEffectGate: func(
				ctx context.Context,
				userID int64,
				tokenVersion int64,
				effect authsession.RenewalEffect,
			) error {
				_, evaluateErr := evaluator.RequireAllowAndRun(
					ctx,
					SessionEvaluationInput{
						UserID: userID, TokenVersion: tokenVersion,
						Purpose: SessionEvaluationPurposeRenew,
					},
					func(effectCtx context.Context) error {
						harness.renewEffects.Add(1)
						return effect(effectCtx)
					},
				)
				return evaluateErr
			},
		},
	)
	harness.seedWriterState()
	harness.registerRoutes()
	return harness
}

func installP7JoinedIdentitySchema(t *testing.T, fixture *identityPersistencePGFixture) {
	t.Helper()
	_, err := fixture.pool.Exec(fixture.ctx, `
		ALTER TABLE users
		  ADD COLUMN is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE,
		  ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp();
		CREATE TABLE user_credentials (
		  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		  password_hash TEXT NOT NULL,
		  -- T1D/M5：password 行 method 标记；external-only = 无 credential 行。
		  method TEXT NOT NULL DEFAULT 'password' CHECK (method IN ('password')),
		  password_changed_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp()
		);
		CREATE TABLE user_profiles (
		  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		  bio TEXT NOT NULL DEFAULT '',
		  signature TEXT NOT NULL DEFAULT '',
		  location TEXT NOT NULL DEFAULT '',
		  website_url TEXT NOT NULL DEFAULT '',
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp()
		);
		CREATE TABLE password_reset_tokens (
		  id BIGSERIAL PRIMARY KEY,
		  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		  token_hash TEXT NOT NULL UNIQUE,
		  expires_at TIMESTAMPTZ NOT NULL,
		  consumed_at TIMESTAMPTZ,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  request_ip_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE user_sessions (
		  id BIGSERIAL PRIMARY KEY,
		  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		  sid TEXT NOT NULL UNIQUE,
		  session_hash TEXT NOT NULL DEFAULT '',
		  device_name TEXT NOT NULL DEFAULT '',
		  browser TEXT NOT NULL DEFAULT '',
		  os TEXT NOT NULL DEFAULT '',
		  user_agent_raw TEXT NOT NULL DEFAULT '',
		  ip_prefix TEXT NOT NULL DEFAULT '',
		  ip_address TEXT NOT NULL DEFAULT '',
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  revoked_at TIMESTAMPTZ,
		  revoke_reason TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		t.Fatalf("install joined identity schema: %v", err)
	}
}

func (h *p7JoinedSessionHarness) seedWriterState() {
	h.t.Helper()
	if _, err := h.pool.Exec(h.fixture.ctx, `
		INSERT INTO user_credentials (user_id, password_hash)
		VALUES ($1, 'p7-original-password-hash')
	`, h.fixture.targetUserID); err != nil {
		h.t.Fatalf("seed joined credentials: %v", err)
	}
	if _, err := h.pool.Exec(h.fixture.ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, transaction_timestamp() + interval '1 hour')
	`, h.fixture.targetUserID, h.resetTokenHash); err != nil {
		h.t.Fatalf("seed joined reset token: %v", err)
	}
}

func (h *p7JoinedSessionHarness) registerRoutes() {
	h.app = fiber.New()
	h.app.Post("/issue", func(c fiber.Ctx) error {
		_, err := h.evaluator.RequireAllowAndRun(
			c.Context(),
			SessionEvaluationInput{
				UserID: h.fixture.targetUserID, TokenVersion: 0,
				Purpose: SessionEvaluationPurposeIssue,
			},
			func(effectCtx context.Context) error {
				pending, beginErr := h.manager.BeginWithAuthorityVersion(
					c, effectCtx, h.fixture.targetUserID, 0,
				)
				if beginErr != nil {
					return beginErr
				}
				pending.SetDeviceInfo(p7JoinedDeviceInfo(h.fixture.targetUserID))
				if auditErr := h.identity.RecordLoginAudit(effectCtx, LoginAudit{
					UserID: h.fixture.targetUserID, Action: AuditActionLogin,
					IPAddress: "127.0.0.1", UserAgent: "p7-joined-issue",
					SessionHash: pending.Info().Hash,
				}); auditErr != nil {
					return auditErr
				}
				return pending.SaveContext(effectCtx)
			},
		)
		h.issueOutcome <- p7JoinedRouteOutcome{err: err}
		if err != nil {
			return c.SendStatus(fiber.StatusConflict)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	h.app.Post("/seed", func(c fiber.Ctx) error {
		pending, err := h.manager.BeginWithAuthorityVersion(
			c, c.Context(), h.fixture.targetUserID, 0,
		)
		if err != nil {
			return err
		}
		pending.SetDeviceInfo(p7JoinedDeviceInfo(h.fixture.targetUserID))
		return pending.SaveContext(c.Context())
	})
	h.app.Get("/renew", func(c fiber.Ctx) error {
		userID, ok, err := h.manager.CurrentUserID(c)
		h.renewOutcome <- p7JoinedRouteOutcome{err: err, userID: userID, ok: ok}
		return c.SendStatus(fiber.StatusOK)
	})
	h.app.Get("/replay", func(c fiber.Ctx) error {
		userID, ok, err := h.manager.CurrentUserID(c)
		h.replayOutcome <- p7JoinedRouteOutcome{err: err, userID: userID, ok: ok}
		return c.SendStatus(fiber.StatusOK)
	})
}

func p7JoinedDeviceInfo(userID int64) authsession.SessionRecordInput {
	return authsession.SessionRecordInput{
		UserID: userID, DeviceName: "P7 joined browser", Browser: "Test",
		OS: "Test", UserAgentRaw: "p7-joined", IPAddress: "127.0.0.1", IPPrefix: "127.0.0.*",
	}
}

func (h *p7JoinedSessionHarness) prepareSession(flow p7JoinedSessionFlow) *http.Cookie {
	h.t.Helper()
	if flow == p7JoinedIssue {
		if _, err := h.pool.Exec(h.fixture.ctx, `
			INSERT INTO user_sessions (user_id, sid, session_hash, device_name)
			VALUES ($1, $2, $3, 'P7 sentinel')
		`, h.fixture.targetUserID, "p7-sentinel-"+h.fixture.schema, strings.Repeat("e", 64)); err != nil {
			h.t.Fatalf("seed sentinel session: %v", err)
		}
		return nil
	}
	response, err := h.app.Test(
		httptest.NewRequest(fiber.MethodPost, "/seed", nil),
		fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true},
	)
	if err != nil {
		h.t.Fatalf("seed browser session: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || len(response.Cookies()) != 1 {
		h.t.Fatalf("seed response status=%d cookies=%#v", response.StatusCode, response.Cookies())
	}
	return response.Cookies()[0]
}

func (h *p7JoinedSessionHarness) startOperation(
	flow p7JoinedSessionFlow,
	cookie *http.Cookie,
) <-chan p7JoinedHTTPResult {
	result := make(chan p7JoinedHTTPResult, 1)
	go func() {
		method, path := fiber.MethodPost, "/issue"
		outcome := h.issueOutcome
		if flow == p7JoinedRenew {
			method, path, outcome = fiber.MethodGet, "/renew", h.renewOutcome
		}
		request := httptest.NewRequest(method, path, nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := h.app.Test(
			request,
			fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true},
		)
		var routeOutcome p7JoinedRouteOutcome
		select {
		case routeOutcome = <-outcome:
		case <-time.After(10 * time.Second):
			err = errors.Join(err, errors.New("joined route outcome timed out"))
		}
		result <- p7JoinedHTTPResult{response: response, err: err, outcome: routeOutcome}
	}()
	return result
}

func (h *p7JoinedSessionHarness) startWriter(writer p7JoinedAuthorityWriter) <-chan error {
	result := make(chan error, 1)
	go func() { result <- writer.write(h.fixture.ctx, h) }()
	return result
}

func (h *p7JoinedSessionHarness) assertMainPoolAvailable() {
	h.t.Helper()
	if h.pool.Config().MaxConns != 1 {
		h.t.Fatalf("main pool MaxConns=%d", h.pool.Config().MaxConns)
	}
	if acquired := h.pool.Stat().AcquiredConns(); acquired != 0 {
		h.t.Fatalf("authority waiter borrowed %d main-pool connections before admission", acquired)
	}
}

func (h *p7JoinedSessionHarness) assertOperation(
	order p7JoinedOrder,
	flow p7JoinedSessionFlow,
	oldCookie *http.Cookie,
	result p7JoinedHTTPResult,
) {
	h.t.Helper()
	if result.err != nil {
		h.t.Fatalf("%s transport: %v", flow, result.err)
	}
	if result.response == nil {
		h.t.Fatalf("%s returned no response", flow)
	}
	defer result.response.Body.Close()
	cookies := result.response.Cookies()

	if order == p7JoinedWriterFirst {
		if h.effect.entered.Load() || h.renewEffects.Load() != 0 || len(cookies) != 0 {
			h.t.Fatalf(
				"writer-first %s entered=%t renewEffects=%d cookies=%#v",
				flow, h.effect.entered.Load(), h.renewEffects.Load(), cookies,
			)
		}
		if !errors.Is(h.effect.awaitResult(h.t), ErrIdentitySessionPolicyDeclarationStale) {
			h.t.Fatalf("writer-first %s did not reject stale authority", flow)
		}
		switch flow {
		case p7JoinedIssue:
			if result.response.StatusCode != fiber.StatusConflict ||
				!errors.Is(result.outcome.err, ErrSessionPolicyEvaluationStale) {
				h.t.Fatalf(
					"writer-first issue status=%d outcome=%v",
					result.response.StatusCode, result.outcome.err,
				)
			}
		case p7JoinedRenew:
			if result.response.StatusCode != fiber.StatusOK || result.outcome.err != nil ||
				result.outcome.ok || result.outcome.userID != 0 {
				h.t.Fatalf(
					"writer-first renew status=%d user=%d ok=%t err=%v",
					result.response.StatusCode, result.outcome.userID,
					result.outcome.ok, result.outcome.err,
				)
			}
		}
		return
	}

	if err := h.effect.awaitResult(h.t); err != nil {
		h.t.Fatalf("effect-first %s policy result: %v", flow, err)
	}
	if result.outcome.err != nil || len(cookies) != 1 {
		h.t.Fatalf("effect-first %s outcome=%v cookies=%#v", flow, result.outcome.err, cookies)
	}
	if flow == p7JoinedIssue && result.response.StatusCode != fiber.StatusNoContent {
		h.t.Fatalf("effect-first issue status=%d", result.response.StatusCode)
	}
	if flow == p7JoinedRenew {
		if result.response.StatusCode != fiber.StatusOK || !result.outcome.ok ||
			result.outcome.userID != h.fixture.targetUserID || h.renewEffects.Load() != 1 {
			h.t.Fatalf(
				"effect-first renew status=%d user=%d ok=%t effects=%d",
				result.response.StatusCode, result.outcome.userID,
				result.outcome.ok, h.renewEffects.Load(),
			)
		}
		if oldCookie == nil || cookies[0].Value == oldCookie.Value {
			h.t.Fatal("effect-first renew did not rotate the browser cookie")
		}
	}
	h.assertCookieCannotReplay(cookies[0])
}

func (h *p7JoinedSessionHarness) assertCookieCannotReplay(cookie *http.Cookie) {
	h.t.Helper()
	request := httptest.NewRequest(fiber.MethodGet, "/replay", nil)
	request.AddCookie(cookie)
	response, err := h.app.Test(
		request,
		fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true},
	)
	if err != nil {
		h.t.Fatalf("replay stale cookie: %v", err)
	}
	defer response.Body.Close()
	outcome := <-h.replayOutcome
	if response.StatusCode != fiber.StatusOK || outcome.err != nil || outcome.ok || outcome.userID != 0 {
		h.t.Fatalf(
			"stale cookie replay status=%d user=%d ok=%t err=%v",
			response.StatusCode, outcome.userID, outcome.ok, outcome.err,
		)
	}
}

func (h *p7JoinedSessionHarness) assertWriterFacts(
	writer p7JoinedAuthorityWriter,
	order p7JoinedOrder,
	flow p7JoinedSessionFlow,
	baselineSessions int,
) {
	h.t.Helper()
	var status UserStatus
	var tokenVersion int64
	if err := h.pool.QueryRow(h.fixture.ctx, `
		SELECT status, current_token_version FROM users WHERE id = $1
	`, h.fixture.targetUserID).Scan(&status, &tokenVersion); err != nil {
		h.t.Fatal(err)
	}
	if status != writer.wantStatus || tokenVersion != 1 {
		h.t.Fatalf("writer facts status=%q/%q tokenVersion=%d", status, writer.wantStatus, tokenVersion)
	}

	wantSessions := baselineSessions
	if order == p7JoinedEffectFirst && flow == p7JoinedIssue {
		wantSessions++
	}
	var total, revokedWithReason int
	if err := h.pool.QueryRow(h.fixture.ctx, `
		SELECT count(*), count(*) FILTER (
		  WHERE revoked_at IS NOT NULL AND revoke_reason = $2
		)
		FROM user_sessions WHERE user_id = $1
	`, h.fixture.targetUserID, writer.reason).Scan(&total, &revokedWithReason); err != nil {
		h.t.Fatal(err)
	}
	if total != wantSessions || revokedWithReason != wantSessions {
		h.t.Fatalf(
			"writer sessions total=%d/%d reason[%s]=%d",
			total, wantSessions, writer.reason, revokedWithReason,
		)
	}

	var loginAudits, adminAudits int
	if err := h.pool.QueryRow(h.fixture.ctx, `
		SELECT
		  count(*) FILTER (WHERE action = $2),
		  count(*) FILTER (WHERE action = 'user.admin_update')
		FROM audit_events WHERE target_user_id = $1
	`, h.fixture.targetUserID, AuditActionLogin).Scan(&loginAudits, &adminAudits); err != nil {
		h.t.Fatal(err)
	}
	wantLoginAudits := 0
	if order == p7JoinedEffectFirst && flow == p7JoinedIssue {
		wantLoginAudits = 1
	}
	wantAdminAudits := 0
	if writer.name == "admin_status" {
		wantAdminAudits = 1
	}
	if loginAudits != wantLoginAudits || adminAudits != wantAdminAudits {
		h.t.Fatalf(
			"writer audits login=%d/%d admin=%d/%d",
			loginAudits, wantLoginAudits, adminAudits, wantAdminAudits,
		)
	}

	if writer.name == "password_reset" {
		var passwordHash string
		var consumed bool
		if err := h.pool.QueryRow(h.fixture.ctx, `
			SELECT credentials.password_hash, reset.consumed_at IS NOT NULL
			FROM user_credentials credentials
			JOIN password_reset_tokens reset ON reset.user_id = credentials.user_id
			WHERE credentials.user_id = $1 AND reset.token_hash = $2
		`, h.fixture.targetUserID, h.resetTokenHash).Scan(&passwordHash, &consumed); err != nil {
			h.t.Fatal(err)
		}
		if passwordHash != writer.newHash || !consumed {
			h.t.Fatalf("password reset hash=%q consumed=%t", passwordHash, consumed)
		}
	}
	if h.invocations.Load() != 1 {
		h.t.Fatalf("session policy invocations=%d", h.invocations.Load())
	}
}

func (h *p7JoinedSessionHarness) sessionCount() int {
	h.t.Helper()
	var count int
	if err := h.pool.QueryRow(h.fixture.ctx, `
		SELECT count(*) FROM user_sessions WHERE user_id = $1
	`, h.fixture.targetUserID).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

type p7JoinedAllowInvoker struct {
	calls *atomic.Int32
}

func (i p7JoinedAllowInvoker) InvokeExact(
	ctx context.Context,
	_ identityregistry.ProviderContribution,
	_ string,
	_ int64,
	_ map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	i.calls.Add(1)
	return accept(ctx, map[string]any{"disposition": SessionPolicyDispositionAllow}, func() error { return nil })
}

// The controlled wrappers add observation only. The embedded production stores
// still own every lock, transaction, validation, and callback.
type p7ControlledSessionEffectStore struct {
	*PostgresIdentitySessionPolicyStore
	called      chan struct{}
	enteredCh   chan struct{}
	releaseCh   chan struct{}
	result      chan error
	calledOnce  sync.Once
	enteredOnce sync.Once
	releaseOnce sync.Once
	entered     atomic.Bool
}

func newP7ControlledSessionEffectStore(
	store *PostgresIdentitySessionPolicyStore,
) *p7ControlledSessionEffectStore {
	return &p7ControlledSessionEffectStore{
		PostgresIdentitySessionPolicyStore: store,
		called:                             make(chan struct{}), enteredCh: make(chan struct{}),
		releaseCh: make(chan struct{}), result: make(chan error, 1),
	}
}

func (s *p7ControlledSessionEffectStore) RunIfCurrent(
	ctx context.Context,
	expected IdentitySessionPolicyResolution,
	authority IdentitySessionAuthority,
	effect func(context.Context) error,
) error {
	s.calledOnce.Do(func() { close(s.called) })
	err := s.PostgresIdentitySessionPolicyStore.RunIfCurrent(ctx, expected, authority, func(effectCtx context.Context) error {
		s.entered.Store(true)
		s.enteredOnce.Do(func() { close(s.enteredCh) })
		select {
		case <-s.releaseCh:
			return effect(effectCtx)
		case <-effectCtx.Done():
			return effectCtx.Err()
		}
	})
	s.result <- err
	return err
}

func (s *p7ControlledSessionEffectStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseCh) })
}

func (s *p7ControlledSessionEffectStore) awaitCalled(t *testing.T, label string) {
	awaitP7JoinedSignal(t, s.called, label+" call")
}

func (s *p7ControlledSessionEffectStore) awaitEntered(t *testing.T, label string) {
	awaitP7JoinedSignal(t, s.enteredCh, label)
}

func (s *p7ControlledSessionEffectStore) assertNotEntered(t *testing.T) {
	t.Helper()
	select {
	case <-s.enteredCh:
		t.Fatal("session effect crossed an admitted authority writer")
	default:
	}
}

func (s *p7ControlledSessionEffectStore) awaitResult(t *testing.T) error {
	return awaitP7JoinedError(t, s.result, "session policy effect")
}

type p7ControlledAuthorityMutationGate struct {
	real         *PostgresIdentitySessionPolicyStore
	called       chan struct{}
	admitted     chan struct{}
	releaseCh    chan struct{}
	calledOnce   sync.Once
	admittedOnce sync.Once
	releaseOnce  sync.Once
}

func newP7ControlledAuthorityMutationGate(
	real *PostgresIdentitySessionPolicyStore,
) *p7ControlledAuthorityMutationGate {
	return &p7ControlledAuthorityMutationGate{
		real: real, called: make(chan struct{}), admitted: make(chan struct{}), releaseCh: make(chan struct{}),
	}
}

func (g *p7ControlledAuthorityMutationGate) RunSessionPolicyMutation(
	ctx context.Context,
	mutation func() error,
) error {
	g.calledOnce.Do(func() { close(g.called) })
	return g.real.RunSessionPolicyMutation(ctx, func() error {
		g.admittedOnce.Do(func() { close(g.admitted) })
		select {
		case <-g.releaseCh:
			return mutation()
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func (g *p7ControlledAuthorityMutationGate) release() {
	g.releaseOnce.Do(func() { close(g.releaseCh) })
}

func (g *p7ControlledAuthorityMutationGate) awaitCalled(t *testing.T, label string) {
	awaitP7JoinedSignal(t, g.called, label+" gate call")
}

func (g *p7ControlledAuthorityMutationGate) awaitAdmitted(t *testing.T, label string) {
	awaitP7JoinedSignal(t, g.admitted, label+" admission")
}

func (g *p7ControlledAuthorityMutationGate) assertNotAdmitted(t *testing.T) {
	t.Helper()
	select {
	case <-g.admitted:
		t.Fatal("authority writer crossed an accepted session effect")
	default:
	}
}

func awaitP7JoinedSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-signal:
			return
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", label)
		default:
			runtime.Gosched()
		}
	}
}

func awaitP7JoinedError(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func awaitP7JoinedHTTPResult(
	t *testing.T,
	result <-chan p7JoinedHTTPResult,
	label string,
) p7JoinedHTTPResult {
	t.Helper()
	select {
	case output := <-result:
		return output
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s response", label)
		return p7JoinedHTTPResult{}
	}
}
