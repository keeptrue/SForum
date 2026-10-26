package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	// Session evaluation only runs before Host issue or renew. Revocation is
	// Host-local and never consults a provider.
	SessionEvaluationPurposeIssue = "issue"
	SessionEvaluationPurposeRenew = "renew"

	SessionPolicyDispositionAllow  = "allow"
	SessionPolicyDispositionDeny   = "deny"
	SessionPolicyDispositionStepUp = "step_up"

	sessionEvaluateOperation = "session.evaluate"

	// The provider timeout bounds remote evaluation. This separate Host budget
	// bounds only the accepted session mutation after final authority admission,
	// even when the caller disconnects.
	sessionPolicyHostEffectTimeout = 10 * time.Second
)

var (
	ErrSessionPolicyEvaluationInvalid     = errors.New("identity: session policy evaluation input is invalid")
	ErrSessionPolicyEvaluationDenied      = errors.New("identity: session policy denied the session effect")
	ErrSessionPolicyEvaluationStepUp      = errors.New("identity: session policy requires step-up verification")
	ErrSessionPolicyEvaluationUnavailable = errors.New("identity: session policy evaluation is unavailable")
	ErrSessionPolicyEvaluationStale       = errors.New("identity: session policy evaluation became stale")
)

// SessionEvaluationInput is the Host-owned workflow claim for issue/renew.
// Plugins never receive passwords, raw cookies, session ids, or CSRF tokens.
type SessionEvaluationInput struct {
	UserID       int64
	TokenVersion int64
	Purpose      string
	// CorrelationID is an opaque Host correlation token, never a session secret.
	CorrelationID string
	// DeviceFingerprint is a Host-derived opaque device class, never a raw UA.
	DeviceFingerprint string
	// StepUpEvidenceToken is Host-minted one-use evidence from a prior step_up.
	// When set, the Host consumes it and must not re-invoke the provider.
	StepUpEvidenceToken string
}

// SessionEvaluationResult is the Host disposition after exact resolution and
// optional plugin evaluation. It is never authorization by itself: the Host
// still owns session creation, renewal, and revocation.
type SessionEvaluationResult struct {
	Disposition      string
	PolicyID         string
	Source           string
	Selection        *IdentitySessionPolicySelection
	Provider         *identityregistry.ProviderContribution
	RegistryRevision uint64
	RegistryDigest   string
	// SelectionRevision is the durable selection tip observed at resolution.
	// Host rechecks it immediately before the session effect.
	SelectionRevision int64
	Output            map[string]any
	// StepUpToken is plaintext one-use Host evidence returned only when the
	// provider disposition is step_up and no prior evidence was supplied.
	StepUpToken     string
	StepUpExpiresAt time.Time
}

// SessionPolicyProviderResolution freezes one coherent selected policy claim
// for InvokeExact. It is not final execution authority.
type SessionPolicyProviderResolution struct {
	IdentitySessionPolicyResolution
	// Operation is always session.evaluate for plugin sources; empty for Core.
	Operation string
}

// SessionPolicyEvaluateInvoker invokes one exact active session provider.
// Accept must call the returned fence exactly once before the Host session
// effect commits; the invoker retains exact runtime admission through Accept.
type SessionPolicyEvaluateInvoker interface {
	InvokeExact(
		ctx context.Context,
		provider identityregistry.ProviderContribution,
		operation string,
		actorUserID int64,
		input map[string]any,
		accept func(context.Context, map[string]any, func() error) error,
	) error
}

// SessionPolicyHostEffect is the Host-owned issue/renew mutation. For a plugin
// policy it runs inside the exact runtime admission callback after both the
// runtime fence and durable selection recheck.
type SessionPolicyHostEffect func(context.Context) error

// SessionPolicyEvaluator resolves the selected policy and, when required,
// invokes the exact session.evaluate provider before Host issue/renew.
type SessionPolicyEvaluator struct {
	store       IdentitySessionPolicyStore
	effectStore IdentitySessionPolicyEffectStore
	invoker     SessionPolicyEvaluateInvoker
	stepUp      SessionPolicyStepUpStore
}

func NewSessionPolicyEvaluator(
	store IdentitySessionPolicyStore,
	invoker SessionPolicyEvaluateInvoker,
) (*SessionPolicyEvaluator, error) {
	if store == nil {
		return nil, ErrIdentitySessionPolicyStoreUnavailable
	}
	effectStore, _ := store.(IdentitySessionPolicyEffectStore)
	return &SessionPolicyEvaluator{store: store, effectStore: effectStore, invoker: invoker}, nil
}

// WithStepUpStore enables Host-owned one-use step_up evidence mint/consume.
// Without a store, step_up still denies the effect but cannot complete a retry.
func (e *SessionPolicyEvaluator) WithStepUpStore(store SessionPolicyStepUpStore) *SessionPolicyEvaluator {
	if e != nil {
		e.stepUp = store
	}
	return e
}

// ProviderResolution freezes the effective selected session policy and its
// exact executable provider claim. Core and Safe Mode resolve without a plugin.
func (e *SessionPolicyEvaluator) ProviderResolution(
	ctx context.Context,
) (SessionPolicyProviderResolution, error) {
	if e == nil || e.store == nil || ctx == nil {
		return SessionPolicyProviderResolution{}, ErrSessionPolicyEvaluationInvalid
	}
	if err := ctx.Err(); err != nil {
		return SessionPolicyProviderResolution{}, err
	}
	resolved, err := e.store.Resolve(ctx)
	if err != nil {
		return SessionPolicyProviderResolution{}, err
	}
	return freezeSessionPolicyProviderResolution(resolved)
}

// InvokeExact evaluates one already-resolved policy claim. Core and Safe Mode
// return allow without calling a plugin. Plugin sources require an invoker and
// fail closed on missing, malformed, timed-out, or non-allow dispositions.
func (e *SessionPolicyEvaluator) InvokeExact(
	ctx context.Context,
	resolution SessionPolicyProviderResolution,
	input SessionEvaluationInput,
) (SessionEvaluationResult, error) {
	return e.invokeExactWithEffect(ctx, resolution, input, nil)
}

func (e *SessionPolicyEvaluator) invokeExactWithEffect(
	ctx context.Context,
	resolution SessionPolicyProviderResolution,
	input SessionEvaluationInput,
	effect SessionPolicyHostEffect,
) (SessionEvaluationResult, error) {
	if e == nil || e.store == nil || ctx == nil {
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationInvalid
	}
	if err := ctx.Err(); err != nil {
		return SessionEvaluationResult{}, err
	}
	prepared, err := prepareSessionEvaluationInput(input)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	if err := validateSessionPolicyProviderResolution(resolution); err != nil {
		return SessionEvaluationResult{}, err
	}

	switch resolution.Source {
	case IdentitySessionPolicySourceCore, IdentitySessionPolicySourceSafeMode:
		result := coreSessionEvaluationResult(resolution, prepared)
		if effect != nil {
			if err := e.runEffectIfCurrent(
				ctx,
				resolution.IdentitySessionPolicyResolution,
				IdentitySessionAuthority{UserID: prepared.UserID, TokenVersion: prepared.TokenVersion},
				func(admittedCtx context.Context) error {
					effectCtx, cancelEffect := sessionPolicyEffectContext(admittedCtx)
					defer cancelEffect()
					return effect(effectCtx)
				},
			); err != nil {
				return result, err
			}
		} else if err := e.recheckSelectionBeforeEffect(ctx, result); err != nil {
			return SessionEvaluationResult{}, err
		}
		return result, nil
	case IdentitySessionPolicySourcePlugin:
		return e.invokePluginSessionEvaluate(ctx, resolution, prepared, effect)
	default:
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationUnavailable
	}
}

// Evaluate returns one fenced proposal without applying a Host mutation. The
// production issue/renew path uses RequireAllowAndRun so the effect remains in
// the exact admission callback. Revocation never calls either policy path.
func (e *SessionPolicyEvaluator) Evaluate(
	ctx context.Context,
	input SessionEvaluationInput,
) (SessionEvaluationResult, error) {
	resolution, err := e.ProviderResolution(ctx)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	return e.InvokeExact(ctx, resolution, input)
}

// RequireAllow evaluates without applying a Host mutation. It is retained for
// inspection and compatibility; production issue/renew uses RequireAllowAndRun.
func (e *SessionPolicyEvaluator) RequireAllow(
	ctx context.Context,
	input SessionEvaluationInput,
) (SessionEvaluationResult, error) {
	result, err := e.Evaluate(ctx, input)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	return result, requireSessionPolicyAllow(result.Disposition)
}

// RequireAllowAndRun is the production issue/renew boundary. The effect runs
// exactly once only for an allowed, still-current policy. Revocation must not
// call this method.
func (e *SessionPolicyEvaluator) RequireAllowAndRun(
	ctx context.Context,
	input SessionEvaluationInput,
	effect SessionPolicyHostEffect,
) (SessionEvaluationResult, error) {
	if effect == nil {
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationInvalid
	}
	if strings.TrimSpace(input.StepUpEvidenceToken) != "" {
		return e.completeStepUpAndRun(ctx, input, effect)
	}
	resolution, err := e.ProviderResolution(ctx)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	result, err := e.invokeExactWithEffect(ctx, resolution, input, effect)
	if err != nil {
		return result, err
	}
	return result, requireSessionPolicyAllow(result.Disposition)
}

// completeStepUpAndRun consumes Host-owned one-use evidence and runs the Host
// effect without re-invoking the plugin provider for that claim.
func (e *SessionPolicyEvaluator) completeStepUpAndRun(
	ctx context.Context,
	input SessionEvaluationInput,
	effect SessionPolicyHostEffect,
) (SessionEvaluationResult, error) {
	if e == nil || e.stepUp == nil {
		return SessionEvaluationResult{}, ErrSessionPolicyStepUpStore
	}
	resolution, err := e.ProviderResolution(ctx)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	prepared, err := prepareSessionEvaluationInput(input)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	if err := validateSessionPolicyProviderResolution(resolution); err != nil {
		return SessionEvaluationResult{}, err
	}
	switch resolution.Source {
	case IdentitySessionPolicySourceCore, IdentitySessionPolicySourceSafeMode:
		// Core never issues step_up evidence; a presented token is invalid.
		return SessionEvaluationResult{}, ErrSessionPolicyStepUpInvalid
	case IdentitySessionPolicySourcePlugin:
	default:
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationUnavailable
	}

	result := SessionEvaluationResult{
		Disposition:       SessionPolicyDispositionAllow,
		PolicyID:          resolution.PolicyID,
		Source:            resolution.Source,
		Selection:         cloneSessionPolicySelection(resolution.Selection),
		Provider:          cloneSessionPolicyProvider(resolution.Provider),
		RegistryRevision:  resolution.RegistryRevision,
		RegistryDigest:    resolution.RegistryDigest,
		SelectionRevision: selectionRevision(resolution.Selection),
	}
	claim, err := normalizeSessionPolicyStepUpClaim(sessionPolicyStepUpClaimFromResult(result, prepared))
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	tokenHash, err := hashSessionPolicyStepUpToken(input.StepUpEvidenceToken)
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	if err := e.stepUp.ConsumeForEffect(ctx, tokenHash, claim); err != nil {
		return SessionEvaluationResult{}, err
	}
	if err := e.runEffectIfCurrent(
		ctx,
		resolution.IdentitySessionPolicyResolution,
		IdentitySessionAuthority{UserID: prepared.UserID, TokenVersion: prepared.TokenVersion},
		func(admittedCtx context.Context) error {
			effectCtx, cancelEffect := sessionPolicyEffectContext(admittedCtx)
			defer cancelEffect()
			return effect(effectCtx)
		},
	); err != nil {
		return result, err
	}
	return result, nil
}

func (e *SessionPolicyEvaluator) mintStepUpEvidence(
	ctx context.Context,
	result SessionEvaluationResult,
	input preparedSessionEvaluationInput,
) (SessionEvaluationResult, error) {
	if e == nil || e.stepUp == nil {
		return result, nil
	}
	claim, err := normalizeSessionPolicyStepUpClaim(sessionPolicyStepUpClaimFromResult(result, input))
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	plaintext, tokenHash, err := newSessionPolicyStepUpToken()
	if err != nil {
		return SessionEvaluationResult{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionPolicyStepUpTTL)
	if err := e.stepUp.Issue(ctx, claim, expiresAt, tokenHash); err != nil {
		return SessionEvaluationResult{}, err
	}
	result.StepUpToken = plaintext
	result.StepUpExpiresAt = expiresAt
	return result, nil
}

func requireSessionPolicyAllow(disposition string) error {
	switch disposition {
	case SessionPolicyDispositionAllow:
		return nil
	case SessionPolicyDispositionDeny:
		return ErrSessionPolicyEvaluationDenied
	case SessionPolicyDispositionStepUp:
		return ErrSessionPolicyEvaluationStepUp
	default:
		return ErrSessionPolicyEvaluationUnavailable
	}
}

func (e *SessionPolicyEvaluator) invokePluginSessionEvaluate(
	ctx context.Context,
	resolution SessionPolicyProviderResolution,
	input preparedSessionEvaluationInput,
	effect SessionPolicyHostEffect,
) (SessionEvaluationResult, error) {
	if e.invoker == nil || resolution.Provider == nil {
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationUnavailable
	}
	provider := *resolution.Provider
	if !identitySessionPolicyProviderHasEvaluate(provider) {
		return SessionEvaluationResult{}, ErrSessionPolicyEvaluationUnavailable
	}

	requestInput := map[string]any{
		"purpose": input.Purpose,
		"userId":  input.UserID,
	}
	if input.CorrelationID != "" {
		requestInput["correlationId"] = input.CorrelationID
	}
	if input.DeviceFingerprint != "" {
		requestInput["deviceFingerprint"] = input.DeviceFingerprint
	}

	var mu sync.Mutex
	open := true
	started := false
	var acceptedResult SessionEvaluationResult
	var callbackErr error
	var callbackPanic any
	done := make(chan struct{})

	accept := func(callCtx context.Context, proposal map[string]any, fence func() error) (resultErr error) {
		mu.Lock()
		if !open || started || ctx.Err() != nil {
			mu.Unlock()
			return ErrSessionPolicyEvaluationInvalid
		}
		started = true
		if callCtx == nil {
			callCtx = ctx
		}
		mu.Unlock()
		admissionCtx, cancelAdmission := joinSessionPolicyAdmissionContexts(ctx, callCtx)
		defer cancelAdmission()

		var result SessionEvaluationResult
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				resultErr = ErrSessionPolicyEvaluationInvalid
			}
			mu.Lock()
			acceptedResult = result
			callbackErr = resultErr
			callbackPanic = panicValue
			close(done)
			mu.Unlock()
		}()
		if cause := sessionPolicyAdmissionCause(ctx, callCtx, admissionCtx); cause != nil {
			return cause
		}
		if fence == nil {
			return ErrSessionPolicyEvaluationInvalid
		}
		cloned, err := cloneSessionEvaluationDocument(proposal)
		if err != nil {
			return err
		}
		disposition, err := parseSessionPolicyDisposition(cloned)
		if err != nil {
			return err
		}
		result = SessionEvaluationResult{
			Disposition:       disposition,
			PolicyID:          resolution.PolicyID,
			Source:            resolution.Source,
			Selection:         cloneSessionPolicySelection(resolution.Selection),
			Provider:          cloneSessionPolicyProvider(resolution.Provider),
			RegistryRevision:  resolution.RegistryRevision,
			RegistryDigest:    resolution.RegistryDigest,
			SelectionRevision: selectionRevision(resolution.Selection),
			Output:            cloned,
		}
		// The Manager fence is the acceptance linearization point. ForceDrain
		// before it rejects; ForceDrain after it is ordered after this accepted
		// effect while the lease remains counted until callback return.
		if err := fence(); err != nil {
			return err
		}
		if cause := sessionPolicyAdmissionCause(ctx, callCtx, admissionCtx); cause != nil {
			return cause
		}
		if effect != nil {
			if decisionErr := requireSessionPolicyAllow(disposition); decisionErr != nil {
				if err := e.recheckSelectionBeforeEffect(admissionCtx, result); err != nil {
					return err
				}
				// step_up mints Host-owned one-use evidence; deny does not.
				if disposition == SessionPolicyDispositionStepUp {
					minted, mintErr := e.mintStepUpEvidence(admissionCtx, result, input)
					if mintErr != nil {
						return mintErr
					}
					result = minted
				}
				return nil
			}
			if err := e.runEffectIfCurrent(
				admissionCtx,
				resolution.IdentitySessionPolicyResolution,
				IdentitySessionAuthority{UserID: input.UserID, TokenVersion: input.TokenVersion},
				func(admittedCtx context.Context) error {
					effectCtx, cancelEffect := sessionPolicyEffectContext(admittedCtx)
					defer cancelEffect()
					return effect(effectCtx)
				},
			); err != nil {
				return &sessionPolicyEffectError{cause: err}
			}
		} else if err := e.recheckSelectionBeforeEffect(admissionCtx, result); err != nil {
			return err
		}
		return nil
	}

	var invokeErr error
	var invokePanic any
	func() {
		defer func() { invokePanic = recover() }()
		invokeErr = e.invoker.InvokeExact(
			ctx,
			provider,
			sessionEvaluateOperation,
			input.UserID,
			requestInput,
			accept,
		)
	}()
	mu.Lock()
	open = false
	wasStarted := started
	mu.Unlock()
	if wasStarted {
		<-done
	}
	mu.Lock()
	result := acceptedResult
	resultErr := callbackErr
	panicValue := callbackPanic
	mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
	if invokePanic != nil {
		panic(invokePanic)
	}
	if !wasStarted {
		return SessionEvaluationResult{}, errors.Join(
			ErrSessionPolicyEvaluationUnavailable,
			ErrSessionPolicyEvaluationInvalid,
			invokeErr,
			ctx.Err(),
		)
	}
	if resultErr != nil {
		var effectErr *sessionPolicyEffectError
		if errors.As(resultErr, &effectErr) {
			return result, effectErr.cause
		}
		return SessionEvaluationResult{}, errors.Join(ErrSessionPolicyEvaluationUnavailable, resultErr)
	}
	// Once the accepted Host callback succeeds, its result is terminal. An
	// invoker cannot turn a committed issue/renew effect into a retryable error.
	return result, nil
}

func joinSessionPolicyAdmissionContexts(root context.Context, call context.Context) (context.Context, context.CancelFunc) {
	if call == nil {
		call = root
	}
	joined, cancelJoined := context.WithCancelCause(call)
	stopRoot := context.AfterFunc(root, func() {
		cancelJoined(context.Cause(root))
	})
	// AfterFunc may not have run yet when root was already canceled.
	if cause := context.Cause(root); cause != nil {
		cancelJoined(cause)
	}
	return &sessionPolicyAdmissionContext{Context: joined, root: root, call: call}, func() {
		stopRoot()
		cancelJoined(nil)
	}
}

// sessionPolicyAdmissionContext keeps callCtx values while making Err and
// Deadline synchronously reflect either cancellation source. AfterFunc closes
// Done for root cancellation; the synchronous methods close its scheduling gap.
type sessionPolicyAdmissionContext struct {
	context.Context
	root context.Context
	call context.Context
}

func (c *sessionPolicyAdmissionContext) Deadline() (time.Time, bool) {
	rootDeadline, rootOK := c.root.Deadline()
	callDeadline, callOK := c.call.Deadline()
	if !rootOK {
		return callDeadline, callOK
	}
	if !callOK || rootDeadline.Before(callDeadline) {
		return rootDeadline, true
	}
	return callDeadline, true
}

func (c *sessionPolicyAdmissionContext) Err() error {
	if err := c.root.Err(); err != nil {
		return err
	}
	if err := c.call.Err(); err != nil {
		return err
	}
	return c.Context.Err()
}

func sessionPolicyAdmissionCause(root context.Context, call context.Context, joined context.Context) error {
	if cause := context.Cause(root); cause != nil {
		return cause
	}
	if cause := context.Cause(call); cause != nil {
		return cause
	}
	return context.Cause(joined)
}

func sessionPolicyEffectContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), sessionPolicyHostEffectTimeout)
}

func (e *SessionPolicyEvaluator) runEffectIfCurrent(
	ctx context.Context,
	resolution IdentitySessionPolicyResolution,
	authority IdentitySessionAuthority,
	effect SessionPolicyHostEffect,
) error {
	if e == nil || e.effectStore == nil || effect == nil {
		return ErrSessionPolicyEvaluationUnavailable
	}
	err := e.effectStore.RunIfCurrent(ctx, resolution, authority, func(effectCtx context.Context) error {
		if err := effect(effectCtx); err != nil {
			return &sessionPolicyEffectError{cause: err}
		}
		return nil
	})
	var effectErr *sessionPolicyEffectError
	if errors.As(err, &effectErr) {
		return effectErr.cause
	}
	if err != nil {
		return errors.Join(ErrSessionPolicyEvaluationStale, err)
	}
	return nil
}

type sessionPolicyEffectError struct {
	cause error
}

func (e *sessionPolicyEffectError) Error() string {
	if e == nil || e.cause == nil {
		return "identity: session policy Host effect failed"
	}
	return e.cause.Error()
}

func (e *sessionPolicyEffectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *SessionPolicyEvaluator) recheckSelectionBeforeEffect(
	ctx context.Context,
	result SessionEvaluationResult,
) error {
	// Core/Safe Mode have no durable plugin selection tip to race against when
	// Safe Mode omitted Selection; still re-resolve to catch Safe Mode flips
	// and Core tip changes.
	current, err := e.store.Resolve(ctx)
	if err != nil {
		return errors.Join(ErrSessionPolicyEvaluationStale, err)
	}
	if current.PolicyID != result.PolicyID || current.Source != result.Source {
		return ErrSessionPolicyEvaluationStale
	}
	switch result.Source {
	case IdentitySessionPolicySourceCore, IdentitySessionPolicySourceSafeMode:
		if current.Provider != nil {
			return ErrSessionPolicyEvaluationStale
		}
		if result.Selection != nil {
			if current.Selection == nil ||
				current.Selection.Revision != result.SelectionRevision ||
				current.Selection.PolicyID != result.Selection.PolicyID {
				return ErrSessionPolicyEvaluationStale
			}
		}
		return nil
	case IdentitySessionPolicySourcePlugin:
		if current.Selection == nil || result.Selection == nil ||
			current.Selection.Revision != result.SelectionRevision ||
			current.Selection.IdentitySessionPolicyEvidence != result.Selection.IdentitySessionPolicyEvidence ||
			current.Provider == nil || result.Provider == nil ||
			!identitySessionPolicyProviderMatches(*current.Provider, *result.Provider) {
			return ErrSessionPolicyEvaluationStale
		}
		return nil
	default:
		return ErrSessionPolicyEvaluationStale
	}
}

type preparedSessionEvaluationInput struct {
	UserID            int64
	TokenVersion      int64
	Purpose           string
	CorrelationID     string
	DeviceFingerprint string
}

func prepareSessionEvaluationInput(
	input SessionEvaluationInput,
) (preparedSessionEvaluationInput, error) {
	prepared := preparedSessionEvaluationInput{
		UserID:            input.UserID,
		TokenVersion:      input.TokenVersion,
		Purpose:           strings.ToLower(strings.TrimSpace(input.Purpose)),
		CorrelationID:     strings.TrimSpace(input.CorrelationID),
		DeviceFingerprint: strings.TrimSpace(input.DeviceFingerprint),
	}
	if prepared.UserID <= 0 || prepared.TokenVersion < 0 {
		return preparedSessionEvaluationInput{}, ErrSessionPolicyEvaluationInvalid
	}
	switch prepared.Purpose {
	case SessionEvaluationPurposeIssue, SessionEvaluationPurposeRenew:
	default:
		return preparedSessionEvaluationInput{}, ErrSessionPolicyEvaluationInvalid
	}
	if len(prepared.CorrelationID) > 128 || len(prepared.DeviceFingerprint) > 128 {
		return preparedSessionEvaluationInput{}, ErrSessionPolicyEvaluationInvalid
	}
	return prepared, nil
}

func freezeSessionPolicyProviderResolution(
	resolved IdentitySessionPolicyResolution,
) (SessionPolicyProviderResolution, error) {
	resolved.Selection = cloneSessionPolicySelection(resolved.Selection)
	resolved.Provider = cloneSessionPolicyProvider(resolved.Provider)
	switch resolved.Source {
	case IdentitySessionPolicySourceCore, IdentitySessionPolicySourceSafeMode:
		if resolved.PolicyID != IdentitySessionPolicyCoreDefault || resolved.Provider != nil {
			return SessionPolicyProviderResolution{}, ErrSessionPolicyEvaluationUnavailable
		}
		return SessionPolicyProviderResolution{
			IdentitySessionPolicyResolution: resolved,
		}, nil
	case IdentitySessionPolicySourcePlugin:
		if resolved.PolicyID == "" || resolved.PolicyID == IdentitySessionPolicyCoreDefault ||
			resolved.Provider == nil || resolved.Selection == nil ||
			!identitySessionPolicyProviderHasEvaluate(*resolved.Provider) {
			return SessionPolicyProviderResolution{}, ErrSessionPolicyEvaluationUnavailable
		}
		return SessionPolicyProviderResolution{
			IdentitySessionPolicyResolution: resolved,
			Operation:                       sessionEvaluateOperation,
		}, nil
	default:
		return SessionPolicyProviderResolution{}, ErrSessionPolicyEvaluationUnavailable
	}
}

func validateSessionPolicyProviderResolution(
	resolution SessionPolicyProviderResolution,
) error {
	switch resolution.Source {
	case IdentitySessionPolicySourceCore, IdentitySessionPolicySourceSafeMode:
		if resolution.PolicyID != IdentitySessionPolicyCoreDefault ||
			resolution.Provider != nil || resolution.Operation != "" {
			return ErrSessionPolicyEvaluationUnavailable
		}
		return nil
	case IdentitySessionPolicySourcePlugin:
		if resolution.Operation != sessionEvaluateOperation ||
			resolution.Provider == nil || resolution.Selection == nil ||
			!identitySessionPolicyProviderHasEvaluate(*resolution.Provider) {
			return ErrSessionPolicyEvaluationUnavailable
		}
		return nil
	default:
		return ErrSessionPolicyEvaluationUnavailable
	}
}

func coreSessionEvaluationResult(
	resolution SessionPolicyProviderResolution,
	input preparedSessionEvaluationInput,
) SessionEvaluationResult {
	_ = input
	return SessionEvaluationResult{
		Disposition:       SessionPolicyDispositionAllow,
		PolicyID:          resolution.PolicyID,
		Source:            resolution.Source,
		Selection:         cloneSessionPolicySelection(resolution.Selection),
		RegistryRevision:  resolution.RegistryRevision,
		RegistryDigest:    resolution.RegistryDigest,
		SelectionRevision: selectionRevision(resolution.Selection),
		Output: map[string]any{
			"disposition": SessionPolicyDispositionAllow,
			"source":      resolution.Source,
		},
	}
}

func parseSessionPolicyDisposition(output map[string]any) (string, error) {
	if output == nil {
		return "", ErrSessionPolicyEvaluationUnavailable
	}
	raw, ok := output["disposition"].(string)
	if !ok {
		return "", ErrSessionPolicyEvaluationUnavailable
	}
	disposition := strings.ToLower(strings.TrimSpace(raw))
	switch disposition {
	case SessionPolicyDispositionAllow, SessionPolicyDispositionDeny, SessionPolicyDispositionStepUp:
		return disposition, nil
	default:
		return "", fmt.Errorf("%w: disposition %q", ErrSessionPolicyEvaluationUnavailable, disposition)
	}
}

func selectionRevision(selection *IdentitySessionPolicySelection) int64 {
	if selection == nil {
		return 0
	}
	return selection.Revision
}

func cloneSessionPolicySelection(
	input *IdentitySessionPolicySelection,
) *IdentitySessionPolicySelection {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func cloneSessionPolicyProvider(
	input *identityregistry.ProviderContribution,
) *identityregistry.ProviderContribution {
	if input == nil {
		return nil
	}
	cloned := *input
	cloned.Operations = append([]identityregistry.ProviderOperation(nil), input.Operations...)
	return &cloned
}

func cloneSessionEvaluationDocument(input map[string]any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrSessionPolicyEvaluationUnavailable
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, ErrSessionPolicyEvaluationUnavailable
	}
	return result, nil
}
