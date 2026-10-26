package identity

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestSessionPolicyEvaluatorCoreProviderResolutionAndInvokeExact(t *testing.T) {
	store := &fakeSessionPolicyStore{
		resolve: IdentitySessionPolicyResolution{
			PolicyID: IdentitySessionPolicyCoreDefault,
			Source:   IdentitySessionPolicySourceCore,
			Selection: &IdentitySessionPolicySelection{
				IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
					PolicyID: IdentitySessionPolicyCoreDefault,
				},
				Revision: 0,
				Implicit: true,
			},
			RegistryRevision: 3,
			RegistryDigest:   "core-digest",
		},
	}
	evaluator, err := NewSessionPolicyEvaluator(store, nil)
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}

	resolution, err := evaluator.ProviderResolution(t.Context())
	if err != nil || resolution.Source != IdentitySessionPolicySourceCore ||
		resolution.Operation != "" || resolution.Provider != nil {
		t.Fatalf("core resolution=%#v err=%v", resolution, err)
	}

	result, err := evaluator.InvokeExact(t.Context(), resolution, SessionEvaluationInput{
		UserID: 42, Purpose: SessionEvaluationPurposeIssue,
	})
	if err != nil || result.Disposition != SessionPolicyDispositionAllow ||
		result.Source != IdentitySessionPolicySourceCore || result.SelectionRevision != 0 {
		t.Fatalf("core invoke=%#v err=%v", result, err)
	}

	allowed, err := evaluator.RequireAllow(t.Context(), SessionEvaluationInput{
		UserID: 42, Purpose: SessionEvaluationPurposeRenew,
	})
	if err != nil || allowed.Disposition != SessionPolicyDispositionAllow {
		t.Fatalf("core require allow=%#v err=%v", allowed, err)
	}
	if store.resolveCalls.Load() < 3 {
		t.Fatalf("expected resolve+recheck calls, got %d", store.resolveCalls.Load())
	}
}

func TestSessionPolicyEvaluatorSafeModeNeverInvokesPlugin(t *testing.T) {
	invoker := &fakeSessionPolicyInvoker{}
	store := &fakeSessionPolicyStore{
		resolve: IdentitySessionPolicyResolution{
			PolicyID:         IdentitySessionPolicyCoreDefault,
			Source:           IdentitySessionPolicySourceSafeMode,
			RegistryRevision: 9,
			RegistryDigest:   "safe-digest",
		},
	}
	evaluator, err := NewSessionPolicyEvaluator(store, invoker)
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	result, err := evaluator.Evaluate(t.Context(), SessionEvaluationInput{
		UserID: 7, Purpose: SessionEvaluationPurposeIssue,
	})
	if err != nil || result.Disposition != SessionPolicyDispositionAllow ||
		result.Source != IdentitySessionPolicySourceSafeMode || invoker.calls.Load() != 0 {
		t.Fatalf("safe mode result=%#v invokerCalls=%d err=%v", result, invoker.calls.Load(), err)
	}
}

func TestSessionPolicyEvaluatorPluginInvokeExactDispositions(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID:                "plugin.session.policy",
			ProviderContractVersion: "1",
			OwnerExtensionID:        provider.Artifact.ExtensionID,
			OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
			OwnerPackageDigest:      provider.Artifact.PackageDigest,
			DeclarationRevision:     4,
		},
		Revision: 4,
	}
	base := IdentitySessionPolicyResolution{
		PolicyID:         "plugin.session.policy",
		Source:           IdentitySessionPolicySourcePlugin,
		Selection:        selection,
		Provider:         &provider,
		RegistryRevision: 11,
		RegistryDigest:   "plugin-digest",
	}

	for _, test := range []struct {
		name        string
		disposition string
		wantErr     error
	}{
		{name: "allow", disposition: SessionPolicyDispositionAllow},
		{name: "deny", disposition: SessionPolicyDispositionDeny, wantErr: ErrSessionPolicyEvaluationDenied},
		{name: "step_up", disposition: SessionPolicyDispositionStepUp, wantErr: ErrSessionPolicyEvaluationStepUp},
	} {
		t.Run(test.name, func(t *testing.T) {
			invoker := &fakeSessionPolicyInvoker{
				output: map[string]any{"disposition": test.disposition},
			}
			store := &fakeSessionPolicyStore{resolve: base}
			evaluator, err := NewSessionPolicyEvaluator(store, invoker)
			if err != nil {
				t.Fatalf("new evaluator: %v", err)
			}
			result, err := evaluator.RequireAllow(t.Context(), SessionEvaluationInput{
				UserID: 99, Purpose: SessionEvaluationPurposeIssue,
				CorrelationID: "corr-1", DeviceFingerprint: "device-a",
			})
			if test.wantErr == nil {
				if err != nil || result.Disposition != SessionPolicyDispositionAllow ||
					invoker.calls.Load() != 1 || invoker.lastOperation != sessionEvaluateOperation ||
					invoker.lastActor != 99 || invoker.fenceCalls.Load() != 1 {
					t.Fatalf("allow result=%#v invoker=%#v err=%v", result, invoker, err)
				}
				if invoker.lastInput["purpose"] != SessionEvaluationPurposeIssue ||
					invoker.lastInput["userId"] != int64(99) ||
					invoker.lastInput["correlationId"] != "corr-1" {
					t.Fatalf("plugin input = %#v", invoker.lastInput)
				}
				return
			}
			if !errors.Is(err, test.wantErr) || invoker.calls.Load() != 1 {
				t.Fatalf("want %v, got result=%#v err=%v calls=%d", test.wantErr, result, err, invoker.calls.Load())
			}
		})
	}
}

func TestSessionPolicyEvaluatorPluginFailsClosedWithoutInvokerOrMalformedOutput(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID:                "plugin.session.policy",
			ProviderContractVersion: "1",
			OwnerExtensionID:        provider.Artifact.ExtensionID,
			OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
			OwnerPackageDigest:      provider.Artifact.PackageDigest,
			DeclarationRevision:     2,
		},
		Revision: 2,
	}
	resolution := IdentitySessionPolicyResolution{
		PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
		Selection: selection, Provider: &provider,
		RegistryRevision: 1, RegistryDigest: "d1",
	}

	t.Run("missing invoker", func(t *testing.T) {
		evaluator, err := NewSessionPolicyEvaluator(&fakeSessionPolicyStore{resolve: resolution}, nil)
		if err != nil {
			t.Fatalf("new evaluator: %v", err)
		}
		if _, err := evaluator.Evaluate(t.Context(), SessionEvaluationInput{
			UserID: 1, Purpose: SessionEvaluationPurposeIssue,
		}); !errors.Is(err, ErrSessionPolicyEvaluationUnavailable) {
			t.Fatalf("missing invoker err=%v", err)
		}
	})

	t.Run("malformed disposition", func(t *testing.T) {
		invoker := &fakeSessionPolicyInvoker{output: map[string]any{"disposition": "maybe"}}
		evaluator, err := NewSessionPolicyEvaluator(&fakeSessionPolicyStore{resolve: resolution}, invoker)
		if err != nil {
			t.Fatalf("new evaluator: %v", err)
		}
		if _, err := evaluator.Evaluate(t.Context(), SessionEvaluationInput{
			UserID: 1, Purpose: SessionEvaluationPurposeIssue,
		}); !errors.Is(err, ErrSessionPolicyEvaluationUnavailable) {
			t.Fatalf("malformed disposition err=%v", err)
		}
	})

	t.Run("invoker transport failure", func(t *testing.T) {
		invoker := &fakeSessionPolicyInvoker{err: errors.New("transport down")}
		evaluator, err := NewSessionPolicyEvaluator(&fakeSessionPolicyStore{resolve: resolution}, invoker)
		if err != nil {
			t.Fatalf("new evaluator: %v", err)
		}
		if _, err := evaluator.Evaluate(t.Context(), SessionEvaluationInput{
			UserID: 1, Purpose: SessionEvaluationPurposeRenew,
		}); !errors.Is(err, ErrSessionPolicyEvaluationUnavailable) {
			t.Fatalf("transport failure err=%v", err)
		}
	})
}

func TestSessionPolicyEvaluatorRecheckDetectsSelectionDrift(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID:                "plugin.session.policy",
			ProviderContractVersion: "1",
			OwnerExtensionID:        provider.Artifact.ExtensionID,
			OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
			OwnerPackageDigest:      provider.Artifact.PackageDigest,
			DeclarationRevision:     5,
		},
		Revision: 5,
	}
	store := &fakeSessionPolicyStore{
		resolve: IdentitySessionPolicyResolution{
			PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
			Selection: selection, Provider: &provider,
			RegistryRevision: 2, RegistryDigest: "before",
		},
	}
	invoker := &fakeSessionPolicyInvoker{
		output: map[string]any{"disposition": SessionPolicyDispositionAllow},
		afterInvoke: func() {
			// Simulate concurrent lifecycle reset to Core between invoke and effect.
			store.resolve = IdentitySessionPolicyResolution{
				PolicyID: IdentitySessionPolicyCoreDefault,
				Source:   IdentitySessionPolicySourceCore,
				Selection: &IdentitySessionPolicySelection{
					IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
						PolicyID: IdentitySessionPolicyCoreDefault,
					},
					Revision: 6,
				},
				RegistryRevision: 3,
				RegistryDigest:   "after",
			}
		},
	}
	evaluator, err := NewSessionPolicyEvaluator(store, invoker)
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	if _, err := evaluator.Evaluate(t.Context(), SessionEvaluationInput{
		UserID: 3, Purpose: SessionEvaluationPurposeIssue,
	}); !errors.Is(err, ErrSessionPolicyEvaluationStale) {
		t.Fatalf("selection drift err=%v", err)
	}
}

func TestSessionPolicyEvaluatorRejectsInvalidInput(t *testing.T) {
	evaluator, err := NewSessionPolicyEvaluator(&fakeSessionPolicyStore{
		resolve: IdentitySessionPolicyResolution{
			PolicyID: IdentitySessionPolicyCoreDefault,
			Source:   IdentitySessionPolicySourceCore,
		},
	}, nil)
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	for _, input := range []SessionEvaluationInput{
		{UserID: 0, Purpose: SessionEvaluationPurposeIssue},
		{UserID: 1, Purpose: "logout"},
		{UserID: 1, Purpose: SessionEvaluationPurposeIssue, CorrelationID: string(make([]byte, 129))},
	} {
		if _, err := evaluator.Evaluate(t.Context(), input); !errors.Is(err, ErrSessionPolicyEvaluationInvalid) {
			t.Fatalf("input %#v err=%v", input, err)
		}
	}
	if _, err := NewSessionPolicyEvaluator(nil, nil); !errors.Is(err, ErrIdentitySessionPolicyStoreUnavailable) {
		t.Fatalf("nil store err=%v", err)
	}
}

func TestSessionPolicyEvaluatorFenceMustRunBeforeEffect(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID:                "plugin.session.policy",
			ProviderContractVersion: "1",
			OwnerExtensionID:        provider.Artifact.ExtensionID,
			OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
			OwnerPackageDigest:      provider.Artifact.PackageDigest,
			DeclarationRevision:     1,
		},
		Revision: 1,
	}
	invoker := &fakeSessionPolicyInvoker{
		output:     map[string]any{"disposition": SessionPolicyDispositionAllow},
		skipFence:  true,
		fenceError: errors.New("admission lost"),
	}
	// When skipFence is true the invoker still requires Accept to call fence;
	// configure fenceError path instead: Accept calls fence, fence fails.
	invoker.skipFence = false
	evaluator, err := NewSessionPolicyEvaluator(&fakeSessionPolicyStore{
		resolve: IdentitySessionPolicyResolution{
			PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
			Selection: selection, Provider: &provider,
			RegistryRevision: 1, RegistryDigest: "d",
		},
	}, invoker)
	if err != nil {
		t.Fatalf("new evaluator: %v", err)
	}
	effectCalls := 0
	if _, err := evaluator.RequireAllowAndRun(
		t.Context(), SessionEvaluationInput{UserID: 1, Purpose: SessionEvaluationPurposeIssue},
		func(context.Context) error { effectCalls++; return nil },
	); !errors.Is(err, ErrSessionPolicyEvaluationUnavailable) || effectCalls != 0 {
		t.Fatalf("fence failure effects=%d err=%v", effectCalls, err)
	}
}

func TestSessionPolicyEvaluatorRequireAllowAndRunKeepsEffectInsideExactAccept(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID: "plugin.session.policy", ProviderContractVersion: "1",
			OwnerExtensionID: provider.Artifact.ExtensionID, OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion: provider.Artifact.ExtensionVersion, OwnerPackageDigest: provider.Artifact.PackageDigest,
			DeclarationRevision: 3,
		},
		Revision: 3,
	}
	store := &fakeSessionPolicyStore{resolve: IdentitySessionPolicyResolution{
		PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
		Selection: selection, Provider: &provider, RegistryRevision: 7, RegistryDigest: "digest",
	}}
	invoker := &fakeSessionPolicyInvoker{output: map[string]any{"disposition": SessionPolicyDispositionAllow}}
	evaluator, err := NewSessionPolicyEvaluator(store, invoker)
	if err != nil {
		t.Fatal(err)
	}
	effectCalls := 0
	result, err := evaluator.RequireAllowAndRun(
		t.Context(),
		SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeIssue},
		func(effectCtx context.Context) error {
			effectCalls++
			if !invoker.insideAccept.Load() || invoker.fenceCalls.Load() != 1 || store.resolveCalls.Load() != 2 {
				t.Fatalf("effect outside exact accept: inside=%t fences=%d resolves=%d", invoker.insideAccept.Load(), invoker.fenceCalls.Load(), store.resolveCalls.Load())
			}
			deadline, ok := effectCtx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > sessionPolicyHostEffectTimeout {
				t.Fatalf("effect context deadline=%v ok=%t", deadline, ok)
			}
			return nil
		},
	)
	if err != nil || result.Disposition != SessionPolicyDispositionAllow || effectCalls != 1 {
		t.Fatalf("result=%#v effects=%d err=%v", result, effectCalls, err)
	}

	invoker.output = map[string]any{"disposition": SessionPolicyDispositionDeny}
	effectCalls = 0
	if _, err := evaluator.RequireAllowAndRun(
		t.Context(),
		SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeRenew},
		func(context.Context) error { effectCalls++; return nil },
	); !errors.Is(err, ErrSessionPolicyEvaluationDenied) || effectCalls != 0 {
		t.Fatalf("deny effects=%d err=%v", effectCalls, err)
	}

	invoker.output = map[string]any{"disposition": SessionPolicyDispositionStepUp}
	if _, err := evaluator.RequireAllowAndRun(
		t.Context(),
		SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeRenew},
		func(context.Context) error { effectCalls++; return nil },
	); !errors.Is(err, ErrSessionPolicyEvaluationStepUp) || effectCalls != 0 {
		t.Fatalf("step-up effects=%d err=%v", effectCalls, err)
	}

	invoker.output = map[string]any{"disposition": SessionPolicyDispositionAllow}
	effectFailure := errors.New("session storage failed")
	if _, err := evaluator.RequireAllowAndRun(
		t.Context(),
		SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeIssue},
		func(context.Context) error { effectCalls++; return effectFailure },
	); !errors.Is(err, effectFailure) || effectCalls != 1 || errors.Is(err, ErrSessionPolicyEvaluationUnavailable) {
		t.Fatalf("effect failure effects=%d err=%v", effectCalls, err)
	}
}

func TestSessionPolicyEvaluatorAcceptCallbackContract(t *testing.T) {
	newEvaluator := func(t *testing.T, invoker SessionPolicyEvaluateInvoker) *SessionPolicyEvaluator {
		t.Helper()
		evaluator, err := NewSessionPolicyEvaluator(
			&fakeSessionPolicyStore{resolve: sessionEvaluationTestResolution()},
			invoker,
		)
		if err != nil {
			t.Fatal(err)
		}
		return evaluator
	}
	input := SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeIssue}
	output := map[string]any{"disposition": SessionPolicyDispositionAllow}
	fence := func() error { return nil }

	t.Run("zero and late callback", func(t *testing.T) {
		var escaped func(context.Context, map[string]any, func() error) error
		evaluator := newEvaluator(t, sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			escaped = accept
			return nil
		}))
		if _, err := evaluator.RequireAllowAndRun(t.Context(), input, func(context.Context) error {
			t.Fatal("late callback ran Host effect")
			return nil
		}); !errors.Is(err, ErrSessionPolicyEvaluationUnavailable) || escaped == nil {
			t.Fatalf("zero callback err=%v escaped=%t", err, escaped != nil)
		}
		if err := escaped(t.Context(), output, fence); !errors.Is(err, ErrSessionPolicyEvaluationInvalid) {
			t.Fatalf("late callback err=%v", err)
		}
	})

	t.Run("concurrent duplicate executes once", func(t *testing.T) {
		var effects atomic.Int32
		evaluator := newEvaluator(t, sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			start := make(chan struct{})
			results := make(chan error, 2)
			for range 2 {
				go func() {
					<-start
					results <- accept(t.Context(), output, fence)
				}()
			}
			close(start)
			return errors.Join(<-results, <-results)
		}))
		result, err := evaluator.RequireAllowAndRun(t.Context(), input, func(context.Context) error {
			effects.Add(1)
			return nil
		})
		if err != nil || result.Disposition != SessionPolicyDispositionAllow || effects.Load() != 1 {
			t.Fatalf("result=%#v effects=%d err=%v", result, effects.Load(), err)
		}
	})

	t.Run("drains inflight callback", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseEffect := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(releaseEffect)
		evaluator := newEvaluator(t, sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			go func() { _ = accept(t.Context(), output, fence) }()
			<-entered
			return nil
		}))
		result := make(chan error, 1)
		go func() {
			_, err := evaluator.RequireAllowAndRun(t.Context(), input, func(context.Context) error {
				close(entered)
				<-release
				return nil
			})
			result <- err
		}()
		<-entered
		select {
		case err := <-result:
			t.Fatalf("returned before inflight callback drained: %v", err)
		default:
		}
		releaseEffect()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("post-accept invoker error cannot reject effect", func(t *testing.T) {
		postErr := errors.New("invoker failed after acceptance")
		var effects atomic.Int32
		evaluator := newEvaluator(t, sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			if err := accept(t.Context(), output, fence); err != nil {
				return err
			}
			return postErr
		}))
		result, err := evaluator.RequireAllowAndRun(t.Context(), input, func(context.Context) error {
			effects.Add(1)
			return nil
		})
		if err != nil || result.Disposition != SessionPolicyDispositionAllow || effects.Load() != 1 {
			t.Fatalf("result=%#v effects=%d err=%v", result, effects.Load(), err)
		}
	})

	t.Run("canceled admission runs no effect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var effects atomic.Int32
		evaluator := newEvaluator(t, sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			return accept(context.Background(), output, fence)
		}))
		if _, err := evaluator.RequireAllowAndRun(ctx, input, func(context.Context) error {
			effects.Add(1)
			return nil
		}); !errors.Is(err, context.Canceled) || effects.Load() != 0 {
			t.Fatalf("effects=%d err=%v", effects.Load(), err)
		}
	})
}

func TestSessionPolicyEvaluatorCancellationBeforeFinalEffectAdmission(t *testing.T) {
	testCases := []struct {
		name     string
		contexts func(*testing.T) (context.Context, context.Context, func(error))
	}{
		{
			name: "root request canceled after fence",
			contexts: func(t *testing.T) (context.Context, context.Context, func(error)) {
				root, cancel := context.WithCancelCause(t.Context())
				return root, context.Background(), cancel
			},
		},
		{
			name: "runtime call context canceled after fence",
			contexts: func(t *testing.T) (context.Context, context.Context, func(error)) {
				callCtx, cancel := context.WithCancelCause(context.Background())
				return t.Context(), callCtx, cancel
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rootCtx, callCtx, cancel := testCase.contexts(t)
			cause := errors.New(testCase.name)
			store := &fakeSessionPolicyStore{resolve: sessionEvaluationTestResolution()}
			var effects atomic.Int32
			evaluator, err := NewSessionPolicyEvaluator(store, sessionPolicyEvaluateInvokerFunc(func(
				_ context.Context,
				_ identityregistry.ProviderContribution,
				_ string,
				_ int64,
				_ map[string]any,
				accept func(context.Context, map[string]any, func() error) error,
			) error {
				return accept(
					callCtx,
					map[string]any{"disposition": SessionPolicyDispositionAllow},
					func() error {
						cancel(cause)
						return nil
					},
				)
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = evaluator.RequireAllowAndRun(
				rootCtx,
				SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeIssue},
				func(context.Context) error {
					effects.Add(1)
					return nil
				},
			)
			if !errors.Is(err, cause) || effects.Load() != 0 || store.runIfCurrentCalls.Load() != 0 {
				t.Fatalf(
					"effects=%d admissions=%d err=%v",
					effects.Load(), store.runIfCurrentCalls.Load(), err,
				)
			}
		})
	}
}

func TestSessionPolicyEvaluatorTransfersAsyncCallbackPanic(t *testing.T) {
	panicValue := "session policy async callback panic"
	entered := make(chan struct{})
	callbackResult := make(chan error, 1)
	evaluator, err := NewSessionPolicyEvaluator(
		&fakeSessionPolicyStore{resolve: sessionEvaluationTestResolution()},
		sessionPolicyEvaluateInvokerFunc(func(
			_ context.Context,
			_ identityregistry.ProviderContribution,
			_ string,
			_ int64,
			_ map[string]any,
			accept func(context.Context, map[string]any, func() error) error,
		) error {
			go func() {
				callbackResult <- accept(
					t.Context(),
					map[string]any{"disposition": SessionPolicyDispositionAllow},
					func() error { return nil },
				)
			}()
			<-entered
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recovered := recover(); recovered != panicValue {
			t.Fatalf("recovered=%#v", recovered)
		}
		if err := <-callbackResult; !errors.Is(err, ErrSessionPolicyEvaluationInvalid) {
			t.Fatalf("callback err=%v", err)
		}
	}()
	_, _ = evaluator.RequireAllowAndRun(
		t.Context(),
		SessionEvaluationInput{UserID: 9, Purpose: SessionEvaluationPurposeIssue},
		func(context.Context) error {
			close(entered)
			panic(panicValue)
		},
	)
}

func TestSessionPolicyProviderResolutionAndOutputDoNotAlias(t *testing.T) {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID: "plugin.session.policy", ProviderContractVersion: "1",
			OwnerExtensionID: provider.Artifact.ExtensionID, OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion: provider.Artifact.ExtensionVersion, OwnerPackageDigest: provider.Artifact.PackageDigest,
			DeclarationRevision: 1,
		},
		Revision: 1,
	}
	nested := map[string]any{"source": "original"}
	store := &fakeSessionPolicyStore{resolve: IdentitySessionPolicyResolution{
		PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
		Selection: selection, Provider: &provider, RegistryRevision: 1, RegistryDigest: "digest",
	}}
	invoker := &fakeSessionPolicyInvoker{output: map[string]any{
		"disposition": SessionPolicyDispositionAllow,
		"metadata":    nested,
	}}
	evaluator, err := NewSessionPolicyEvaluator(store, invoker)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := evaluator.ProviderResolution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	selection.PolicyID = "mutated"
	provider.Operations[0].Name = "mutated"
	if resolution.Selection.PolicyID != "plugin.session.policy" ||
		resolution.Provider.Operations[0].Name != sessionEvaluateOperation {
		t.Fatalf("resolution retained Store aliases: %#v", resolution)
	}
	selection.PolicyID = "plugin.session.policy"
	provider.Operations[0].Name = sessionEvaluateOperation
	result, err := evaluator.InvokeExact(
		t.Context(), resolution,
		SessionEvaluationInput{UserID: 4, Purpose: SessionEvaluationPurposeIssue},
	)
	if err != nil {
		t.Fatal(err)
	}
	nested["source"] = "mutated"
	metadata, _ := result.Output["metadata"].(map[string]any)
	if metadata["source"] != "original" {
		t.Fatalf("result retained nested output alias: %#v", result.Output)
	}
}

func sessionEvaluationTestProvider() identityregistry.ProviderContribution {
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID:              "plugin.session.provider",
			ContractVersion: "1",
			Kind:            identityregistry.ProviderKindSession,
			Handler:         "session",
			Priority:        10,
			Operations: []identityregistry.ProviderOperation{{
				Name: sessionEvaluateOperation, InputSchema: "schemas/session-input.json",
				OutputSchema: "schemas/session-output.json", TimeoutMS: 1_000,
				FailurePolicy: identityregistry.ProviderFailureFailClosed,
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "plugin.membership", ExtensionVersion: "1.0.0",
			VersionID: 8, PackageDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RuntimeInstanceID: "instance-1",
		},
	}
}

func sessionEvaluationTestResolution() IdentitySessionPolicyResolution {
	provider := sessionEvaluationTestProvider()
	selection := &IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{
			PolicyID: "plugin.session.policy", ProviderContractVersion: "1",
			OwnerExtensionID: provider.Artifact.ExtensionID, OwnerExtensionVersionID: provider.Artifact.VersionID,
			OwnerExtensionVersion: provider.Artifact.ExtensionVersion, OwnerPackageDigest: provider.Artifact.PackageDigest,
			DeclarationRevision: 3,
		},
		Revision: 3,
	}
	return IdentitySessionPolicyResolution{
		PolicyID: "plugin.session.policy", Source: IdentitySessionPolicySourcePlugin,
		Selection: selection, Provider: &provider, RegistryRevision: 7, RegistryDigest: "digest",
	}
}

type sessionPolicyEvaluateInvokerFunc func(
	context.Context,
	identityregistry.ProviderContribution,
	string,
	int64,
	map[string]any,
	func(context.Context, map[string]any, func() error) error,
) error

func (f sessionPolicyEvaluateInvokerFunc) InvokeExact(
	ctx context.Context,
	provider identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	input map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	return f(ctx, provider, operation, actorUserID, input, accept)
}

type fakeSessionPolicyStore struct {
	resolve           IdentitySessionPolicyResolution
	resolveErr        error
	resolveCalls      atomic.Int32
	runIfCurrentCalls atomic.Int32
}

func (s *fakeSessionPolicyStore) Current(context.Context) (IdentitySessionPolicySelection, error) {
	return IdentitySessionPolicySelection{}, errors.New("unused")
}
func (s *fakeSessionPolicyStore) Candidate(context.Context, string) (IdentitySessionPolicyEvidence, error) {
	return IdentitySessionPolicyEvidence{}, errors.New("unused")
}
func (s *fakeSessionPolicyStore) Resolve(context.Context) (IdentitySessionPolicyResolution, error) {
	s.resolveCalls.Add(1)
	if s.resolveErr != nil {
		return IdentitySessionPolicyResolution{}, s.resolveErr
	}
	return s.resolve, nil
}
func (s *fakeSessionPolicyStore) Select(context.Context, SelectIdentitySessionPolicyInput) (IdentitySessionPolicyMutation, error) {
	return IdentitySessionPolicyMutation{}, errors.New("unused")
}
func (s *fakeSessionPolicyStore) Reset(context.Context, ResetIdentitySessionPolicyInput) (IdentitySessionPolicyMutation, error) {
	return IdentitySessionPolicyMutation{}, errors.New("unused")
}
func (s *fakeSessionPolicyStore) ListEvents(context.Context, int) ([]IdentitySessionPolicyEvent, error) {
	return nil, errors.New("unused")
}
func (s *fakeSessionPolicyStore) RunIfCurrent(
	ctx context.Context,
	expected IdentitySessionPolicyResolution,
	_ IdentitySessionAuthority,
	effect func(context.Context) error,
) error {
	s.runIfCurrentCalls.Add(1)
	current, err := s.Resolve(ctx)
	if err != nil {
		return err
	}
	if current.PolicyID != expected.PolicyID || current.Source != expected.Source ||
		(current.Selection == nil) != (expected.Selection == nil) ||
		(current.Provider == nil) != (expected.Provider == nil) {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	if current.Selection != nil && (current.Selection.Revision != expected.Selection.Revision ||
		current.Selection.IdentitySessionPolicyEvidence != expected.Selection.IdentitySessionPolicyEvidence) {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	if current.Provider != nil && !identitySessionPolicyProviderMatches(*current.Provider, *expected.Provider) {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	return effect(ctx)
}

type fakeSessionPolicyInvoker struct {
	output        map[string]any
	err           error
	fenceError    error
	skipFence     bool
	afterInvoke   func()
	calls         atomic.Int32
	fenceCalls    atomic.Int32
	insideAccept  atomic.Bool
	lastOperation string
	lastActor     int64
	lastInput     map[string]any
}

func (i *fakeSessionPolicyInvoker) InvokeExact(
	_ context.Context,
	_ identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	input map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	i.calls.Add(1)
	i.lastOperation = operation
	i.lastActor = actorUserID
	i.lastInput = input
	if i.err != nil {
		return i.err
	}
	if i.afterInvoke != nil {
		i.afterInvoke()
	}
	fence := func() error {
		i.fenceCalls.Add(1)
		return i.fenceError
	}
	if i.skipFence {
		i.insideAccept.Store(true)
		defer i.insideAccept.Store(false)
		return accept(context.Background(), i.output, nil)
	}
	i.insideAccept.Store(true)
	defer i.insideAccept.Store(false)
	return accept(context.Background(), i.output, fence)
}
