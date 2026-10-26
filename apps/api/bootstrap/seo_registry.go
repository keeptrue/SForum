package bootstrap

import (
	"fmt"
	"strings"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

type productionSEORegistry struct {
	Execution *seoregistry.ExecutionRuntime
	Trace     *seoregistry.ExecutionTraceRing
}

func bindProductionSEORegistry(
	registry *seoregistry.Registry,
	runtime *extensionsruntime.Manager,
	policy seoregistry.HostFinalPolicyConfig,
) (*productionSEORegistry, error) {
	if registry == nil || runtime == nil {
		return nil, fmt.Errorf("bootstrap: production SEO Registry dependency unavailable")
	}
	finalPolicy, err := seoregistry.NewHostFinalPolicy(policy)
	if err != nil {
		return nil, fmt.Errorf("create SEO Host final policy: %w", err)
	}
	trace := seoregistry.NewExecutionTraceRing(0)
	execution, err := seoregistry.NewExecutionRuntime(seoregistry.ExecutionConfig{
		Registry:    registry,
		Resolver:    extensionsruntime.NewProtocolV2SEOProviderResolver(runtime),
		Admission:   extensionsruntime.NewSEOExecutionAdmission(runtime),
		FinalPolicy: finalPolicy,
		Trace:       trace,
	})
	if err != nil {
		return nil, fmt.Errorf("create production SEO execution runtime: %w", err)
	}
	return &productionSEORegistry{Execution: execution, Trace: trace}, nil
}

func productionSEOEnabled(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "true", "1", "yes", "on":
		return true
	case "disabled", "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}
