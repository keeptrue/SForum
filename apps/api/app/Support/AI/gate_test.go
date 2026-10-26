package ai_test

import (
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func gateSettings() ai.GateSettings {
	return ai.GateSettings{
		RateLimitPerMinute:  60,
		ExtensionDailyQuota: 500,
		UserDailyQuota:      20,
		MonthlyBudgetMicros: 20 * ai.MicroPerUnit,
	}
}

func TestEvaluateGatesRejectsWhenGatewayDisabled(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{Enabled: false, Gates: gateSettings()})
	if decision.Allowed || decision.Reason != ai.GateReasonDisabled {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestEvaluateGatesAllowsUnderAllLimits(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled:   true,
		Gates:     gateSettings(),
		Site:      ai.UsageSnapshot{MinuteCalls: 1, MonthSpendMicros: ai.MicroPerUnit},
		Extension: ai.UsageSnapshot{DayCalls: 2}, ExtensionBound: true,
		User: ai.UsageSnapshot{DayCalls: 1}, UserBound: true,
	})
	if !decision.Allowed {
		t.Fatalf("expected allow, got %+v", decision)
	}
}

func TestEvaluateGatesBudgetBeatsNarrowerScopes(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled: true,
		Gates:   gateSettings(),
		Site:    ai.UsageSnapshot{MonthSpendMicros: 20 * ai.MicroPerUnit},
	})
	if decision.Allowed || decision.Reason != ai.GateReasonBudgetExceeded || decision.Scope != ai.GateScopeSite {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestEvaluateGatesRateLimitAppliesToSiteOnly(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled: true,
		Gates:   gateSettings(),
		Site:    ai.UsageSnapshot{MinuteCalls: 60},
	})
	if decision.Allowed || decision.Reason != ai.GateReasonRateLimited {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestEvaluateGatesQuotaStaysInsideItsScope(t *testing.T) {
	exhausted := ai.UsageSnapshot{DayCalls: 500}
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled: true, Gates: gateSettings(),
		Extension: exhausted, ExtensionBound: true,
	})
	if decision.Allowed || decision.Scope != ai.GateScopeExtension {
		t.Fatalf("decision = %+v", decision)
	}
	// 同一份用量在没有发起方归属时不得牵连其他调用。
	decision = ai.EvaluateGates(ai.GateInput{
		Enabled: true, Gates: gateSettings(),
		Extension: exhausted, ExtensionBound: false,
	})
	if !decision.Allowed {
		t.Fatalf("unbound usage must not gate, got %+v", decision)
	}
}

func TestEvaluateGatesUserQuotaOnlyWhenBound(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled: true, Gates: gateSettings(),
		User: ai.UsageSnapshot{DayCalls: 20}, UserBound: true,
	})
	if decision.Allowed || decision.Scope != ai.GateScopeUser {
		t.Fatalf("decision = %+v", decision)
	}
	decision = ai.EvaluateGates(ai.GateInput{
		Enabled: true, Gates: gateSettings(),
		User: ai.UsageSnapshot{DayCalls: 10_000}, UserBound: false,
	})
	if !decision.Allowed {
		t.Fatalf("unbound user usage must not gate, got %+v", decision)
	}
}

func TestEvaluateGatesZeroThresholdDisablesThatGate(t *testing.T) {
	decision := ai.EvaluateGates(ai.GateInput{
		Enabled: true,
		Gates:   ai.GateSettings{},
		Site:    ai.UsageSnapshot{MinuteCalls: 100_000, MonthSpendMicros: 1 << 40},
	})
	if !decision.Allowed {
		t.Fatalf("zero thresholds should disable their gates, got %+v", decision)
	}
}
