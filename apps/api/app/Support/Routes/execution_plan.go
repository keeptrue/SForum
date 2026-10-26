package routes

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var ErrInvalidExecutionPlan = errors.New("routes: invalid execution plan")

type RouteExecutionPhase string

const (
	RoutePhaseGlobal  RouteExecutionPhase = "global"
	RoutePhaseBefore  RouteExecutionPhase = "before"
	RoutePhaseFilter  RouteExecutionPhase = "filter"
	RoutePhaseWrap    RouteExecutionPhase = "wrap"
	RoutePhaseHandler RouteExecutionPhase = "handler"
	RoutePhaseAfter   RouteExecutionPhase = "after"
)

// RouteExecutionCommitState is owned by the future dispatcher. Any state past
// pristine proves that a response or side effect may have started, so another
// handler cannot safely become a second writer.
type RouteExecutionCommitState string

const (
	RouteCommitUnknown           RouteExecutionCommitState = ""
	RouteCommitPristine          RouteExecutionCommitState = "pristine"
	RouteCommitResponseStarted   RouteExecutionCommitState = "response_started"
	RouteCommitSideEffectStarted RouteExecutionCommitState = "side_effect_started"
	RouteCommitFinal             RouteExecutionCommitState = "committed"
)

type RouteExecutionStep struct {
	Phase                 RouteExecutionPhase
	Action                string
	RouteID               string
	ContractVersion       string
	TargetID              string
	Path                  string
	Method                string
	Provider              Provider
	Guard                 string
	Access                string
	Permission            string
	Mode                  string
	Destination           string
	StatusCode            int
	TargetPath            string
	Handler               string
	RequestSchema         string
	ResponseSchema        string
	MutableRequestFields  []string
	MutableResponseFields []string
	TimeoutMS             int
	Fallback              string
	Priority              int
	CoreGuard             CoreGuardDescriptor
	PluginGuard           PluginGuardBinding
}

// RouteExecutionPlan contains data only. Building it never invokes a handler,
// opens a runtime, or mutates the Registry snapshot.
type RouteExecutionPlan struct {
	revision      uint64
	method        string
	path          string
	params        map[string]string
	unsafeMethod  bool
	terminalIndex int
	chain         []RouteExecutionStep
	policy        RouteExecutionPolicy
	policyBound   bool
}

func (p RouteExecutionPlan) Revision() uint64   { return p.revision }
func (p RouteExecutionPlan) Method() string     { return p.method }
func (p RouteExecutionPlan) Path() string       { return p.path }
func (p RouteExecutionPlan) UnsafeMethod() bool { return p.unsafeMethod }

func (p RouteExecutionPlan) Valid() bool {
	return p.revision > 0 && p.method != "" && p.path != "" &&
		p.terminalIndex >= 0 && p.terminalIndex < len(p.chain) &&
		p.chain[p.terminalIndex].Phase == RoutePhaseHandler
}

func (p RouteExecutionPlan) Params() map[string]string {
	return cloneRouteExecutionParams(p.params)
}

func (p RouteExecutionPlan) Chain() []RouteExecutionStep {
	return cloneRouteExecutionSteps(p.chain)
}

// ExecutionPolicy returns the Host policy frozen with this exact route plan.
func (p RouteExecutionPlan) ExecutionPolicy() (RouteExecutionPolicy, bool) {
	return p.policy, p.policyBound
}

func (p RouteExecutionPlan) Terminal() RouteExecutionStep {
	if p.terminalIndex < 0 || p.terminalIndex >= len(p.chain) {
		return RouteExecutionStep{}
	}
	step := p.chain[p.terminalIndex]
	step.MutableRequestFields = append([]string(nil), step.MutableRequestFields...)
	step.MutableResponseFields = append([]string(nil), step.MutableResponseFields...)
	step.CoreGuard = cloneCoreGuardDescriptor(step.CoreGuard)
	step.PluginGuard = clonePluginGuardBinding(step.PluginGuard)
	return step
}

// AllowsFallback evaluates the declaration belonging to the failed step. A
// plugin may request not_found/readonly_core fallback, but only GET/HEAD while
// no response or side effect has begun can use it.
func (p RouteExecutionPlan) AllowsFallback(stepIndex int, state RouteExecutionCommitState) bool {
	if state != RouteCommitPristine || p.unsafeMethod || stepIndex < 0 || stepIndex >= len(p.chain) {
		return false
	}
	step := p.chain[stepIndex]
	if step.Provider.Kind != ProviderPlugin {
		return false
	}
	return step.Fallback == "not_found" || step.Fallback == "readonly_core"
}

// AllowsWriter is deliberately stricter than HTTP method safety. It is the
// dispatcher fence that prevents a core/plugin fallback becoming a second
// writer after any observable response or side effect.
func (p RouteExecutionPlan) AllowsWriter(state RouteExecutionCommitState) bool {
	return p.Valid() && state == RouteCommitPristine
}

// BuildExecutionPlan resolves against one immutable revision. A concurrent
// publication retries resolution; the returned plan remains bound to its old
// revision even if a newer snapshot is published afterwards.
func (r *Registry) BuildExecutionPlan(method, requestPath string) (RouteExecutionPlan, error) {
	if r == nil {
		return RouteExecutionPlan{}, fmt.Errorf("%w: registry is nil", ErrInvalidExecutionPlan)
	}
	snapshot, match, err := r.resolveForPlanning(method, requestPath)
	if err != nil {
		return RouteExecutionPlan{}, err
	}
	return buildRouteExecutionPlanView(r.executionPlanningView(snapshot), match, method, requestPath)
}

// buildRouteExecutionPlan also accepts an already selected Match. This is kept
// package-private until the provider-selection registry can prove that a
// replacement winner was selected explicitly.
func buildRouteExecutionPlan(
	snapshot Snapshot,
	match Match,
	method string,
	requestPath string,
) (RouteExecutionPlan, error) {
	return buildRouteExecutionPlanView(publicPlanningView(snapshot), match, method, requestPath)
}

func buildRouteExecutionPlanView(
	snapshot planningSnapshot,
	match Match,
	method string,
	requestPath string,
) (RouteExecutionPlan, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if !validMethod(method) || method == "*" {
		return RouteExecutionPlan{}, fmt.Errorf("%w: invalid request method", ErrInvalidExecutionPlan)
	}
	normalizedPath, err := normalizeRequestPath(requestPath)
	if err != nil {
		return RouteExecutionPlan{}, fmt.Errorf("%w: %v", ErrInvalidExecutionPlan, err)
	}
	if snapshot.revision == 0 || match.Revision != snapshot.revision || len(match.Candidates) != 0 {
		return RouteExecutionPlan{}, fmt.Errorf("%w: terminal route is unresolved or ambiguous", ErrInvalidExecutionPlan)
	}
	terminal := match.Route
	if !terminalExecutionAction(terminal.Action) || !routeMethodMatches(terminal, method) || !snapshot.routeAdmitted(terminal) ||
		!routeInExecutionSnapshot(snapshot.routes, terminal) {
		return RouteExecutionPlan{}, fmt.Errorf("%w: terminal route is not in the resolved snapshot", ErrInvalidExecutionPlan)
	}
	compiled, err := compileRoutePath(terminal.Path)
	if err != nil {
		return RouteExecutionPlan{}, fmt.Errorf("%w: invalid terminal path", ErrInvalidExecutionPlan)
	}
	params, matched := compiled.match(normalizedPath)
	if !matched || !equalRouteExecutionParams(params, match.Params) {
		return RouteExecutionPlan{}, fmt.Errorf("%w: terminal route does not match the request", ErrInvalidExecutionPlan)
	}
	if snapshot.safeMode && terminal.Provider.Kind == ProviderPlugin {
		return RouteExecutionPlan{}, fmt.Errorf("%w: plugin terminal is unavailable in safe mode", ErrInvalidExecutionPlan)
	}
	if err := validateExecutionTerminal(snapshot, terminal, method, normalizedPath); err != nil {
		return RouteExecutionPlan{}, err
	}

	groups := map[RouteExecutionPhase][]Route{
		RoutePhaseGlobal: {}, RoutePhaseBefore: {}, RoutePhaseFilter: {},
		RoutePhaseWrap: {}, RoutePhaseAfter: {},
	}
	seen := make([]Route, 0, len(match.Contributions))
	expectedTarget := terminal.ID
	if terminal.Action == extensionmanifest.RouteActionReplace {
		expectedTarget = terminal.TargetID
	}
	for _, contribution := range match.Contributions {
		if routeInExecutionSnapshot(seen, contribution) {
			return RouteExecutionPlan{}, fmt.Errorf("%w: duplicate route contribution", ErrInvalidExecutionPlan)
		}
		seen = append(seen, contribution)
		if !routeInExecutionSnapshot(snapshot.routes, contribution) || !snapshot.routeAdmitted(contribution) ||
			contribution.Provider.Kind != ProviderPlugin ||
			snapshot.safeMode || !routeMethodMatches(contribution, method) && contribution.Action != extensionmanifest.RouteActionGlobalMiddleware {
			return RouteExecutionPlan{}, fmt.Errorf("%w: contribution is not active in the resolved snapshot", ErrInvalidExecutionPlan)
		}
		phase, ok := executionPhaseForContribution(contribution.Action)
		if !ok {
			return RouteExecutionPlan{}, fmt.Errorf("%w: contribution action cannot enter the chain", ErrInvalidExecutionPlan)
		}
		if phase == RoutePhaseGlobal {
			if contribution.TargetID != "" || contribution.Path != "" || contribution.Method != "" {
				return RouteExecutionPlan{}, fmt.Errorf("%w: invalid global middleware", ErrInvalidExecutionPlan)
			}
		} else {
			if contribution.TargetID != expectedTarget {
				return RouteExecutionPlan{}, fmt.Errorf("%w: contribution targets a different route", ErrInvalidExecutionPlan)
			}
			path, compileErr := compileRoutePath(contribution.Path)
			if compileErr != nil {
				return RouteExecutionPlan{}, fmt.Errorf("%w: invalid contribution path", ErrInvalidExecutionPlan)
			}
			if _, ok := path.match(normalizedPath); !ok {
				return RouteExecutionPlan{}, fmt.Errorf("%w: contribution does not match the request", ErrInvalidExecutionPlan)
			}
		}
		groups[phase] = append(groups[phase], contribution)
	}

	chain := make([]RouteExecutionStep, 0, len(match.Contributions)+1)
	appendPhase := func(phase RouteExecutionPhase) error {
		routes := append([]Route(nil), groups[phase]...)
		sortExecutionRoutes(routes)
		for _, route := range routes {
			step, stepErr := executionStep(snapshot, phase, route, method)
			if stepErr != nil {
				return stepErr
			}
			chain = append(chain, step)
		}
		return nil
	}
	for _, phase := range []RouteExecutionPhase{RoutePhaseGlobal, RoutePhaseBefore, RoutePhaseFilter, RoutePhaseWrap} {
		if err := appendPhase(phase); err != nil {
			return RouteExecutionPlan{}, err
		}
	}
	terminalIndex := len(chain)
	terminalStep, err := executionStep(snapshot, RoutePhaseHandler, terminal, method)
	if err != nil {
		return RouteExecutionPlan{}, err
	}
	if (terminal.Action == extensionmanifest.RouteActionAlias || terminal.Action == extensionmanifest.RouteActionRedirect ||
		terminal.Action == extensionmanifest.RouteActionRewrite) && terminal.TargetID != "" {
		target, targetErr := resolveInheritedCoreRoute(snapshot, terminal, method)
		if targetErr != nil {
			return RouteExecutionPlan{}, targetErr
		}
		sourcePath, sourceErr := compileRoutePath(terminal.Path)
		targetPath, targetCompileErr := compileRoutePath(target.Path)
		if sourceErr != nil || targetCompileErr != nil {
			return RouteExecutionPlan{}, fmt.Errorf("%w: route mapping target path is invalid", ErrInvalidExecutionPlan)
		}
		if !routePathParametersCompatible(sourcePath, targetPath) {
			if terminal.Action == extensionmanifest.RouteActionRedirect {
				return RouteExecutionPlan{}, fmt.Errorf("%w: redirect target path is incompatible", ErrInvalidExecutionPlan)
			}
		} else {
			terminalStep.TargetPath, err = materializeTargetRoutePath(sourcePath, targetPath, params)
			if err != nil {
				return RouteExecutionPlan{}, err
			}
		}
	}
	chain = append(chain, terminalStep)
	if err := appendPhase(RoutePhaseAfter); err != nil {
		return RouteExecutionPlan{}, err
	}
	policy, policyBound := snapshot.routePolicy(terminal)
	return RouteExecutionPlan{
		revision: snapshot.revision, method: method, path: normalizedPath,
		params: cloneRouteExecutionParams(params), unsafeMethod: method != "GET" && method != "HEAD",
		terminalIndex: terminalIndex, chain: cloneRouteExecutionSteps(chain),
		policy: policy, policyBound: policyBound,
	}, nil
}

func validateExecutionTerminal(snapshot planningSnapshot, terminal Route, method, requestPath string) error {
	for _, conflict := range snapshot.conflicts {
		if !methodsOverlap(conflict.Method, method) ||
			!activeConflictContainsExecutionRoute(snapshot, conflict, terminal) {
			continue
		}
		if activeConflictCandidateCount(snapshot, conflict) < 2 {
			continue
		}
		if conflict.Kind != ConflictProviderSelection || terminal.Action != extensionmanifest.RouteActionReplace {
			return fmt.Errorf("%w: terminal route has an unresolved conflict", ErrInvalidExecutionPlan)
		}
	}
	if terminal.Action != extensionmanifest.RouteActionReplace {
		return nil
	}
	if terminal.Provider.Kind != ProviderPlugin || terminal.TargetID == "" {
		return fmt.Errorf("%w: replacement terminal is invalid", ErrInvalidExecutionPlan)
	}
	replacements := 0
	targetFound := false
	for _, route := range snapshot.routes {
		if route.Action == extensionmanifest.RouteActionReplace && route.TargetID == terminal.TargetID &&
			routeMethodMatches(route, method) {
			if !snapshot.routeAdmitted(route) {
				continue
			}
			compiled, err := compileRoutePath(route.Path)
			if err == nil {
				if _, ok := compiled.match(requestPath); ok {
					replacements++
				}
			}
		}
		if addressableAction(route.Action) && route.ID == terminal.TargetID && routeMethodMatches(route, method) {
			if snapshot.routeAdmitted(route) {
				targetFound = true
			}
		}
	}
	if !targetFound || replacements != 1 {
		return fmt.Errorf("%w: replacement provider is missing or ambiguous", ErrInvalidExecutionPlan)
	}
	return nil
}

func terminalExecutionAction(action string) bool {
	return addressableAction(action) || action == extensionmanifest.RouteActionReplace
}

func executionPhaseForContribution(action string) (RouteExecutionPhase, bool) {
	switch action {
	case extensionmanifest.RouteActionGlobalMiddleware:
		return RoutePhaseGlobal, true
	case extensionmanifest.RouteActionBefore:
		return RoutePhaseBefore, true
	case extensionmanifest.RouteActionFilter:
		return RoutePhaseFilter, true
	case extensionmanifest.RouteActionWrap:
		return RoutePhaseWrap, true
	case extensionmanifest.RouteActionAfter:
		return RoutePhaseAfter, true
	default:
		return "", false
	}
}

func executionStep(snapshot planningSnapshot, phase RouteExecutionPhase, route Route, requestMethod string) (RouteExecutionStep, error) {
	descriptor := cloneCoreGuardDescriptor(route.CoreGuard)
	if route.Provider.Kind == ProviderPlugin && route.Guard == extensionmanifest.GuardCoreInherit {
		var err error
		descriptor, err = resolveInheritedCoreGuard(snapshot, route, requestMethod)
		if err != nil {
			return RouteExecutionStep{}, err
		}
	}
	return routeExecutionStep(phase, route, descriptor), nil
}

func routeExecutionStep(phase RouteExecutionPhase, route Route, descriptor CoreGuardDescriptor) RouteExecutionStep {
	return RouteExecutionStep{
		Phase: phase, Action: route.Action, RouteID: route.ID, ContractVersion: route.ContractVersion,
		TargetID: route.TargetID, Path: route.Path, Method: route.Method, Provider: route.Provider,
		Guard: route.Guard, Access: routeExecutionAccess(route.Guard, route.Provider.Kind),
		Permission: route.Permission, Mode: route.Mode, Destination: route.Destination, StatusCode: route.StatusCode,
		Handler:       route.Handler,
		RequestSchema: route.RequestSchema, ResponseSchema: route.ResponseSchema,
		MutableRequestFields:  append([]string(nil), route.MutableRequestFields...),
		MutableResponseFields: append([]string(nil), route.MutableResponseFields...),
		TimeoutMS:             route.TimeoutMS, Fallback: route.Fallback, Priority: route.Priority,
		CoreGuard: cloneCoreGuardDescriptor(descriptor), PluginGuard: clonePluginGuardBinding(route.PluginGuard),
	}
}

func resolveInheritedCoreGuard(snapshot planningSnapshot, route Route, requestMethod string) (CoreGuardDescriptor, error) {
	if route.Provider.Kind != ProviderPlugin || route.Guard != extensionmanifest.GuardCoreInherit || route.TargetID == "" {
		return CoreGuardDescriptor{}, fmt.Errorf("%w: inherited guard declaration is invalid", ErrInvalidExecutionPlan)
	}
	target, err := resolveInheritedCoreRoute(snapshot, route, requestMethod)
	if err != nil {
		return CoreGuardDescriptor{}, err
	}
	descriptor := target.CoreGuard
	if descriptor.RouteID != target.ID || descriptor.ContractVersion != target.ContractVersion || descriptor.Method != target.Method {
		return CoreGuardDescriptor{}, fmt.Errorf("%w: inherited core guard target drifted", ErrInvalidExecutionPlan)
	}
	return cloneCoreGuardDescriptor(descriptor), nil
}

func resolveInheritedCoreRoute(snapshot planningSnapshot, route Route, requestMethod string) (Route, error) {
	if route.Provider.Kind != ProviderPlugin || route.TargetID == "" {
		return Route{}, fmt.Errorf("%w: inherited core route target is invalid", ErrInvalidExecutionPlan)
	}
	bestSpecificity := -1
	var matched []Route
	for _, target := range snapshot.routes {
		if target.Provider.Kind != ProviderCore || !addressableAction(target.Action) ||
			target.ID != route.TargetID || !routeMethodMatches(target, requestMethod) {
			continue
		}
		specificity := requestMethodSpecificity(target, requestMethod)
		if specificity > bestSpecificity {
			bestSpecificity = specificity
			matched = matched[:0]
		}
		if specificity == bestSpecificity {
			matched = append(matched, target)
		}
	}
	if len(matched) != 1 {
		return Route{}, fmt.Errorf("%w: inherited core route target is missing or ambiguous", ErrInvalidExecutionPlan)
	}
	return cloneRoute(matched[0]), nil
}

func routeExecutionAccess(guard string, provider ProviderKind) string {
	if provider == ProviderCore && guard == "" {
		return "core"
	}
	switch guard {
	case extensionmanifest.GuardCorePublic:
		return "public"
	case extensionmanifest.GuardCoreLogin:
		return "login"
	case extensionmanifest.GuardCorePermission:
		return "permission"
	case extensionmanifest.GuardCoreGuest:
		return "guest"
	case extensionmanifest.GuardCoreInherit:
		return "inherit"
	case extensionmanifest.GuardCoreRaw:
		return "raw_request"
	default:
		return "custom"
	}
}

func sortExecutionRoutes(routes []Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Priority != routes[j].Priority {
			return routes[i].Priority > routes[j].Priority
		}
		if routes[i].ID != routes[j].ID {
			return routes[i].ID < routes[j].ID
		}
		if routes[i].ContractVersion != routes[j].ContractVersion {
			return routes[i].ContractVersion < routes[j].ContractVersion
		}
		if routes[i].Provider.Artifact.ExtensionID != routes[j].Provider.Artifact.ExtensionID {
			return routes[i].Provider.Artifact.ExtensionID < routes[j].Provider.Artifact.ExtensionID
		}
		return routes[i].Provider.Artifact.RuntimeInstanceID < routes[j].Provider.Artifact.RuntimeInstanceID
	})
}

func routeInExecutionSnapshot(routes []Route, wanted Route) bool {
	for _, route := range routes {
		if equalRoute(route, wanted) {
			return true
		}
	}
	return false
}

func activeConflictCandidateCount(snapshot planningSnapshot, conflict Conflict) int {
	count := 0
	for _, route := range conflict.Candidates {
		if snapshot.routeAdmitted(route) {
			count++
		}
	}
	return count
}

func activeConflictContainsExecutionRoute(snapshot planningSnapshot, conflict Conflict, wanted Route) bool {
	for _, route := range conflict.Candidates {
		if equalRoute(route, wanted) && snapshot.routeAdmitted(route) {
			return true
		}
	}
	return false
}

func cloneRouteExecutionParams(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func equalRouteExecutionParams(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func cloneRouteExecutionSteps(source []RouteExecutionStep) []RouteExecutionStep {
	result := append([]RouteExecutionStep(nil), source...)
	for index := range result {
		result[index].MutableRequestFields = append([]string(nil), result[index].MutableRequestFields...)
		result[index].MutableResponseFields = append([]string(nil), result[index].MutableResponseFields...)
		result[index].CoreGuard = cloneCoreGuardDescriptor(result[index].CoreGuard)
		result[index].PluginGuard = clonePluginGuardBinding(result[index].PluginGuard)
	}
	return result
}
