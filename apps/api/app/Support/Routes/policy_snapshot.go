package routes

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var ErrRoutePolicyComposition = errors.New("routes: bound route policy composition is invalid")

const (
	routePolicyDisabled               = "disabled"
	routePolicyRateLimitIPWrite       = "host.ip_write@1"
	routePolicyIdempotencyRequired24h = "required.24h@1"
)

// RoutePolicyBinding freezes one Host-enforced operation policy to the exact
// executable route artifact published in the same Registry revision.
type RoutePolicyBinding struct {
	Artifact        PluginArtifact
	RouteID         string
	ContractVersion string
	Method          string
	Policy          RouteExecutionPolicy
}

// BindRouteExecutionPolicies resolves policies from an immutable contract
// candidate and returns detached publication data ready for Registry.Publish.
// Existing bindings are rebuilt so a removed or upgraded artifact cannot keep
// a stale policy through lifecycle publication.
func BindRouteExecutionPolicies(input Publication, resolver RoutePolicyResolver) (Publication, error) {
	result := clonePublication(input)
	result.Policies = nil
	candidate, err := preparePublication(result)
	if err != nil {
		return Publication{}, err
	}
	if result.SafeMode {
		return result, nil
	}
	bindings := make([]RoutePolicyBinding, 0)
	for _, route := range candidate.routeValues {
		if route.Provider.Kind != ProviderPlugin || !terminalExecutionAction(route.Action) {
			continue
		}
		if resolver == nil {
			return Publication{}, fmt.Errorf("%w: route policy resolver is unavailable", ErrInvalidRoute)
		}
		policy, resolveErr := resolver.ResolveRouteExecutionPolicy(routePolicyLookupStep(route))
		if errors.Is(resolveErr, ErrRoutePolicyNotFound) {
			policy = RouteExecutionPolicy{RateLimit: routePolicyDisabled, Idempotency: routePolicyDisabled}
			resolveErr = nil
		}
		if resolveErr != nil {
			return Publication{}, fmt.Errorf("%w: resolve route %s policy: %v", ErrInvalidRoute, route.ID, resolveErr)
		}
		bindings = append(bindings, RoutePolicyBinding{
			Artifact: route.Provider.Artifact, RouteID: route.ID,
			ContractVersion: route.ContractVersion, Method: route.Method, Policy: policy,
		})
	}
	result.Policies = bindings
	if _, err := preparePublication(result); err != nil {
		return Publication{}, err
	}
	return result, nil
}

func prepareRoutePolicyBindings(
	routes []Route,
	bindings []RoutePolicyBinding,
	safeMode bool,
) ([]RoutePolicyBinding, map[string]RouteExecutionPolicy, error) {
	if safeMode {
		return nil, nil, nil
	}
	if bindings == nil {
		return nil, nil, nil
	}
	terminals := make(map[string]Route)
	for _, route := range routes {
		if route.Provider.Kind != ProviderPlugin || !terminalExecutionAction(route.Action) {
			continue
		}
		terminals[routePolicyKey(route.Provider.Artifact, route.ID, route.ContractVersion, route.Method)] = route
	}
	frozen := cloneRoutePolicyBindings(bindings)
	index := make(map[string]RouteExecutionPolicy, len(frozen))
	for _, binding := range frozen {
		key := routePolicyKey(binding.Artifact, binding.RouteID, binding.ContractVersion, binding.Method)
		route, exists := terminals[key]
		if !exists || !validRouteExecutionPolicy(route, binding.Policy) {
			return nil, nil, fmt.Errorf("%w: route policy binding is invalid", ErrInvalidRoute)
		}
		if _, duplicate := index[key]; duplicate {
			return nil, nil, fmt.Errorf("%w: route policy binding is duplicated", ErrInvalidRoute)
		}
		index[key] = binding.Policy
	}
	if len(index) != len(terminals) {
		return nil, nil, fmt.Errorf("%w: authoritative route policy set is incomplete", ErrInvalidRoute)
	}
	if err := validateBoundRoutePolicyComposition(routes, index); err != nil {
		return nil, nil, err
	}
	sort.Slice(frozen, func(left, right int) bool {
		return routePolicyBindingKey(frozen[left]) < routePolicyBindingKey(frozen[right])
	})
	return frozen, index, nil
}

func indexRoutePolicyBindings(bindings []RoutePolicyBinding) map[string]RouteExecutionPolicy {
	if bindings == nil {
		return nil
	}
	result := make(map[string]RouteExecutionPolicy, len(bindings))
	for _, binding := range bindings {
		result[routePolicyBindingKey(binding)] = binding.Policy
	}
	return result
}

func cloneRoutePolicyBindings(bindings []RoutePolicyBinding) []RoutePolicyBinding {
	if bindings == nil {
		return nil
	}
	return append(make([]RoutePolicyBinding, 0, len(bindings)), bindings...)
}

func (s planningSnapshot) routePolicy(route Route) (RouteExecutionPolicy, bool) {
	if route.Provider.Kind != ProviderPlugin || s.policyIndex == nil {
		return RouteExecutionPolicy{}, false
	}
	policy, exists := s.policyIndex[routePolicyKey(
		route.Provider.Artifact, route.ID, route.ContractVersion, route.Method,
	)]
	return policy, exists
}

func routePolicyLookupStep(route Route) RouteExecutionStep {
	return RouteExecutionStep{
		Action: route.Action, RouteID: route.ID, ContractVersion: route.ContractVersion,
		Method: route.Method, Provider: route.Provider,
	}
}

func routePolicyBindingKey(binding RoutePolicyBinding) string {
	return routePolicyKey(binding.Artifact, binding.RouteID, binding.ContractVersion, binding.Method)
}

func routePolicyKey(artifact PluginArtifact, routeID, contractVersion, method string) string {
	return artifact.ExtensionID + "\x00" + artifact.ExtensionVersion + "\x00" +
		artifact.PackageDigest + "\x00" + artifact.RuntimeInstanceID + "\x00" +
		routeID + "\x00" + contractVersion + "\x00" + method
}

func validRouteExecutionPolicy(route Route, policy RouteExecutionPolicy) bool {
	if policy.RateLimit != strings.TrimSpace(policy.RateLimit) ||
		policy.Idempotency != strings.TrimSpace(policy.Idempotency) {
		return false
	}
	validRateLimit := policy.RateLimit == routePolicyDisabled || policy.RateLimit == routePolicyRateLimitIPWrite
	validIdempotency := policy.Idempotency == routePolicyDisabled ||
		policy.Idempotency == routePolicyIdempotencyRequired24h
	if !validRateLimit || !validIdempotency ||
		policy.IdempotencyRequired != (policy.Idempotency == routePolicyIdempotencyRequired24h) {
		return false
	}
	if policy.RateLimit == routePolicyRateLimitIPWrite && !routePolicyUnsafeMethod(route.Method) {
		return false
	}
	return !policy.IdempotencyRequired ||
		policy.RateLimit == routePolicyRateLimitIPWrite && routePolicyUnsafeMethod(route.Method) &&
			route.Mode == extensionmanifest.RouteModeHTTP
}

func validateBoundRoutePolicyComposition(routes []Route, policies map[string]RouteExecutionPolicy) error {
	for _, terminal := range routes {
		if terminal.Provider.Kind != ProviderPlugin || !terminalExecutionAction(terminal.Action) {
			continue
		}
		policy, exists := policies[routePolicyKey(
			terminal.Provider.Artifact, terminal.ID, terminal.ContractVersion, terminal.Method,
		)]
		if !exists || !policy.IdempotencyRequired {
			continue
		}
		for _, contribution := range routes {
			if routePolicyContributionApplies(terminal, contribution) &&
				routePolicyMutatesLiveCredentials(contribution) {
				return fmt.Errorf(
					"%w: %w: route %s requires replay with credential-mutating contribution %s",
					ErrInvalidRoute, ErrRoutePolicyComposition, terminal.ID, contribution.ID,
				)
			}
		}
	}
	return nil
}

func routePolicyContributionApplies(terminal, contribution Route) bool {
	if contribution.Provider.Kind != ProviderPlugin || !requestStageAction(contribution.Action) {
		return false
	}
	if contribution.Action == extensionmanifest.RouteActionGlobalMiddleware {
		return true
	}
	targetID := terminal.ID
	if terminal.Action == extensionmanifest.RouteActionReplace {
		targetID = terminal.TargetID
	}
	return contribution.TargetID == targetID && methodsOverlap(contribution.Method, terminal.Method) &&
		contribution.PathSignature == terminal.PathSignature
}

func routePolicyMutatesLiveCredentials(route Route) bool {
	for _, pointer := range route.MutableRequestFields {
		tokens, err := routeMutationPointerTokens(pointer)
		if err != nil || len(tokens) < 2 || tokens[0] != "headers" {
			continue
		}
		if routePolicyLiveCredentialHeader(tokens[1]) {
			return true
		}
	}
	return false
}

func routePolicyLiveCredentialHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cookie", "authorization", "proxy-authorization", "x-api-key", "x-auth-token",
		"x-csrf-token", "idempotency-key":
		return true
	default:
		return false
	}
}

func routePolicyUnsafeMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS", "*":
		return false
	default:
		return true
	}
}
