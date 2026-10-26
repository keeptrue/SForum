package identity

import (
	"context"
	"errors"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type fakeRiskSource struct {
	providers []identityregistry.ProviderContribution
	err       error
}

func (f fakeRiskSource) RiskProviders(context.Context) ([]identityregistry.ProviderContribution, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]identityregistry.ProviderContribution(nil), f.providers...), nil
}

type fakeRiskInvoker struct {
	// dispositions[providerID] = disposition string or "error"
	dispositions map[string]string
	order        []string
}

func (f *fakeRiskInvoker) InvokeExact(
	_ context.Context,
	provider identityregistry.ProviderContribution,
	operation string,
	_ int64,
	_ map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	f.order = append(f.order, provider.ID)
	if operation != riskEvaluateOperation {
		return errors.New("wrong operation")
	}
	value := f.dispositions[provider.ID]
	if value == "error" {
		return errors.New("provider failed")
	}
	if value == "" {
		value = RiskDispositionAllow
	}
	return accept(context.Background(), map[string]any{"disposition": value}, func() error { return nil })
}

func riskProvider(id string, priority int) identityregistry.ProviderContribution {
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: id, Kind: identityregistry.ProviderKindRisk, Priority: priority,
			Operations: []identityregistry.ProviderOperation{{Name: riskEvaluateOperation}},
		},
	}
}

func TestRiskEvaluatorAllowsWhenNoProviders(t *testing.T) {
	evaluator, err := NewRiskEvaluator(fakeRiskSource{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.RequireAllow(context.Background(), RiskEvaluationInput{
		UserID: 1, Purpose: "login",
	})
	if err != nil || result.Disposition != RiskDispositionAllow {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestRiskEvaluatorComposesDenyOverStepUpOverAllow(t *testing.T) {
	// Providers 已按 priority 降序：high 先执行。
	source := fakeRiskSource{providers: []identityregistry.ProviderContribution{
		riskProvider("high.risk", 100),
		riskProvider("mid.risk", 50),
		riskProvider("low.risk", 10),
	}}
	invoker := &fakeRiskInvoker{dispositions: map[string]string{
		"high.risk": RiskDispositionAllow,
		"mid.risk":  RiskDispositionStepUp,
		"low.risk":  RiskDispositionDeny,
	}}
	evaluator, err := NewRiskEvaluator(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(context.Background(), RiskEvaluationInput{
		UserID: 7, Purpose: "login",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != RiskDispositionDeny || result.DenyingProvider != "low.risk" {
		t.Fatalf("result=%#v", result)
	}
	if len(result.ProviderIDs) != 3 || invoker.order[0] != "high.risk" {
		t.Fatalf("order=%#v ids=%#v", invoker.order, result.ProviderIDs)
	}
	if err := requireRiskAllow(result.Disposition); !errors.Is(err, ErrRiskEvaluationDenied) {
		t.Fatalf("require=%v", err)
	}
}

func TestRiskEvaluatorFailsClosedOnProviderError(t *testing.T) {
	source := fakeRiskSource{providers: []identityregistry.ProviderContribution{
		riskProvider("broken.risk", 1),
	}}
	invoker := &fakeRiskInvoker{dispositions: map[string]string{"broken.risk": "error"}}
	evaluator, err := NewRiskEvaluator(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(context.Background(), RiskEvaluationInput{
		UserID: 1, Purpose: "login",
	}); !errors.Is(err, ErrRiskEvaluationUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestRiskEvaluatorRejectsInvalidInputAndMissingEvaluate(t *testing.T) {
	if _, err := NewRiskEvaluator(nil, nil); !errors.Is(err, ErrRiskEvaluationUnavailable) {
		t.Fatalf("nil source: %v", err)
	}
	evaluator, err := NewRiskEvaluator(fakeRiskSource{providers: []identityregistry.ProviderContribution{{
		Provider: identityregistry.Provider{ID: "no-op", Kind: identityregistry.ProviderKindRisk},
	}}}, &fakeRiskInvoker{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(context.Background(), RiskEvaluationInput{
		UserID: 1, Purpose: "login",
	}); !errors.Is(err, ErrRiskEvaluationUnavailable) {
		t.Fatalf("missing evaluate: %v", err)
	}
	if _, err := evaluator.Evaluate(context.Background(), RiskEvaluationInput{
		UserID: 0, Purpose: "login",
	}); !errors.Is(err, ErrRiskEvaluationInvalid) {
		t.Fatalf("invalid user: %v", err)
	}
}

func TestComposeRiskDisposition(t *testing.T) {
	if got := composeRiskDisposition(RiskDispositionAllow, RiskDispositionStepUp); got != RiskDispositionStepUp {
		t.Fatalf("got=%s", got)
	}
	if got := composeRiskDisposition(RiskDispositionStepUp, RiskDispositionDeny); got != RiskDispositionDeny {
		t.Fatalf("got=%s", got)
	}
	if got := composeRiskDisposition(RiskDispositionDeny, RiskDispositionAllow); got != RiskDispositionDeny {
		t.Fatalf("got=%s", got)
	}
}
