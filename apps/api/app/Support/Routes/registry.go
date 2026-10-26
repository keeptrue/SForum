package routes

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	ErrInvalidRoute     = errors.New("routes: invalid route registry input")
	ErrRouteNotFound    = errors.New("routes: route not found")
	ErrAmbiguousRoute   = errors.New("routes: route resolution is ambiguous")
	ErrRevisionConflict = errors.New("routes: registry revision conflict")
)

type ProviderKind string

const (
	ProviderCore   ProviderKind = "core"
	ProviderPlugin ProviderKind = "plugin"
)

type ConflictKind string

const (
	ConflictPathMethod        ConflictKind = "path_method"
	ConflictRouteIdentity     ConflictKind = "route_identity"
	ConflictProviderSelection ConflictKind = "provider_selection"
)

// PluginArtifact pins every contribution to its immutable package and exact runtime instance.
type PluginArtifact struct {
	ExtensionID       string `json:"extensionId"`
	ExtensionVersion  string `json:"extensionVersion"`
	PackageDigest     string `json:"packageDigest"`
	RuntimeInstanceID string `json:"runtimeInstanceId"`
}

type Provider struct {
	Kind     ProviderKind
	Artifact PluginArtifact
}

// CoreRoute is the minimum executable identity supplied by the P0 Host catalog.
type CoreRoute struct {
	ID              string
	ContractVersion string
	Method          string
	Path            string
	Priority        int
	Guard           CoreGuardDescriptor
}

type PluginRouteSet struct {
	Artifact PluginArtifact
	Routes   []extensionmanifest.ManifestRoute
	Guards   []extensionmanifest.ManifestGuard
}

// Publication always replaces the complete active snapshot.
type Publication struct {
	Core     []CoreRoute
	Plugins  []PluginRouteSet
	Policies []RoutePolicyBinding
	SafeMode bool
}

// Route is method-expanded so matching and conflict inspection share one identity.
type Route struct {
	ID                    string
	ContractVersion       string
	Action                string
	TargetID              string
	Path                  string
	Method                string
	Guard                 string
	Permission            string
	Priority              int
	Fallback              string
	Mode                  string
	Destination           string
	StatusCode            int
	Handler               string
	RequestSchema         string
	ResponseSchema        string
	MutableRequestFields  []string
	MutableResponseFields []string
	TimeoutMS             int
	PathSignature         string
	Provider              Provider
	CoreGuard             CoreGuardDescriptor
	PluginGuard           PluginGuardBinding
}

type Conflict struct {
	Kind            ConflictKind
	RouteID         string
	ContractVersion string
	Method          string
	PathSignature   string
	Candidates      []Route
}

type Snapshot struct {
	Revision  uint64
	SafeMode  bool
	Routes    []Route
	Conflicts []Conflict
	Policies  []RoutePolicyBinding
}

type PublicationSnapshot struct {
	Revision    uint64
	Publication Publication
}

type Match struct {
	Revision      uint64
	Route         Route
	Params        map[string]string
	Contributions []Route
	Candidates    []Route
}

func equalRoute(left, right Route) bool {
	return left.ID == right.ID && left.ContractVersion == right.ContractVersion &&
		left.Action == right.Action && left.TargetID == right.TargetID && left.Path == right.Path &&
		left.Method == right.Method && left.Guard == right.Guard && left.Permission == right.Permission &&
		left.Priority == right.Priority && left.Fallback == right.Fallback && left.Mode == right.Mode &&
		left.Destination == right.Destination && left.StatusCode == right.StatusCode && left.Handler == right.Handler &&
		left.RequestSchema == right.RequestSchema && left.ResponseSchema == right.ResponseSchema &&
		slices.Equal(left.MutableRequestFields, right.MutableRequestFields) &&
		slices.Equal(left.MutableResponseFields, right.MutableResponseFields) &&
		left.TimeoutMS == right.TimeoutMS && left.PathSignature == right.PathSignature &&
		left.Provider == right.Provider && equalCoreGuardDescriptor(left.CoreGuard, right.CoreGuard) &&
		equalPluginGuardBinding(left.PluginGuard, right.PluginGuard)
}

type preparedRoute struct {
	route Route
	path  compiledPath
}

type registrySnapshot struct {
	revision    uint64
	safeMode    bool
	routes      []preparedRoute
	routeValues []Route
	conflicts   []Conflict
	policies    []RoutePolicyBinding
	policyIndex map[string]RouteExecutionPolicy
	publication Publication
}

// planningSnapshot is an internal read-only view of one atomic registry
// revision. Its slices must never escape through a public API.
type planningSnapshot struct {
	revision    uint64
	safeMode    bool
	routes      []Route
	conflicts   []Conflict
	policyIndex map[string]RouteExecutionPolicy
	admit       func(Route) bool
}

// Registry keeps readers lock-free while complete candidate sets are validated off-snapshot.
type Registry struct {
	writeMu     sync.Mutex
	snapshot    atomic.Pointer[registrySnapshot]
	admissionMu sync.RWMutex
	admission   func(PluginArtifact) bool
}

func NewRegistry() *Registry {
	registry := &Registry{}
	registry.snapshot.Store(&registrySnapshot{})
	return registry
}

// WithPluginAdmission binds resolution to the exact Manager runtime gate.
// Snapshot inspection remains available while staged/drained contributions are
// excluded from ordinary route resolution.
func (r *Registry) WithPluginAdmission(admission func(PluginArtifact) bool) *Registry {
	if r == nil {
		return r
	}
	r.admissionMu.Lock()
	r.admission = admission
	r.admissionMu.Unlock()
	return r
}

func (r *Registry) Publish(input Publication) (Snapshot, error) {
	return r.publish(input, nil)
}

// PublishIfRevision prevents independently prepared lifecycle publications from
// overwriting a newer complete snapshot. A failed comparison never publishes.
func (r *Registry) PublishIfRevision(input Publication, expectedRevision uint64) (Snapshot, error) {
	return r.publish(input, &expectedRevision)
}

func (r *Registry) publish(input Publication, expectedRevision *uint64) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, fmt.Errorf("%w: registry is nil", ErrInvalidRoute)
	}
	candidate, err := preparePublication(input)
	if err != nil {
		return Snapshot{}, err
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current := r.loadSnapshot()
	if expectedRevision != nil && current.revision != *expectedRevision {
		return snapshotView(current), fmt.Errorf(
			"%w: expected %d, current %d", ErrRevisionConflict, *expectedRevision, current.revision,
		)
	}
	candidate.revision = current.revision + 1
	r.snapshot.Store(candidate)
	return snapshotView(candidate), nil
}

func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	return snapshotView(r.loadSnapshot())
}

func (r *Registry) Revision() uint64 {
	if r == nil {
		return 0
	}
	return r.loadSnapshot().revision
}

func (r *Registry) PublicationSnapshot() PublicationSnapshot {
	if r == nil {
		return PublicationSnapshot{}
	}
	snapshot := r.loadSnapshot()
	return PublicationSnapshot{Revision: snapshot.revision, Publication: clonePublication(snapshot.publication)}
}

func (r *Registry) Conflicts() []Conflict {
	return r.Snapshot().Conflicts
}

// Resolve selects only an unambiguous endpoint. Ordered middleware/modifiers remain data;
// execution semantics are intentionally owned by later P6 slices.
func (r *Registry) Resolve(method, requestPath string) (Match, error) {
	if r == nil {
		return Match{}, fmt.Errorf("%w: registry is nil", ErrInvalidRoute)
	}
	_, match, err := r.resolveForPlanning(method, requestPath)
	return cloneMatch(match), err
}

// resolveForPlanning keeps the matched route bound to the exact immutable
// snapshot used for resolution. Callers must not return match or snapshot data
// without cloning it first.
func (r *Registry) resolveForPlanning(method, requestPath string) (*registrySnapshot, Match, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if !validMethod(method) || method == "*" {
		return nil, Match{}, fmt.Errorf("%w: invalid request method", ErrInvalidRoute)
	}
	requestPath, err := normalizeRequestPath(requestPath)
	if err != nil {
		return nil, Match{}, err
	}
	snapshot := r.loadSnapshot()

	type candidate struct {
		prepared preparedRoute
		params   map[string]string
	}
	candidates := make([]candidate, 0)
	for _, item := range snapshot.routes {
		if !addressableAction(item.route.Action) || !routeMethodMatches(item.route, method) || !r.routeAdmitted(item.route) {
			continue
		}
		if params, ok := item.path.match(requestPath); ok {
			candidates = append(candidates, candidate{prepared: item, params: params})
		}
	}
	if len(candidates) == 0 {
		return snapshot, Match{Revision: snapshot.revision}, ErrRouteNotFound
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i].prepared, candidates[j].prepared
		if order := comparePathSpecificity(left.path, right.path); order != 0 {
			return order > 0
		}
		if requestMethodSpecificity(left.route, method) != requestMethodSpecificity(right.route, method) {
			return requestMethodSpecificity(left.route, method) > requestMethodSpecificity(right.route, method)
		}
		return routeLess(left, right)
	})
	best := candidates[0]
	top := make([]Route, 0, len(candidates))
	for _, item := range candidates {
		if comparePathSpecificity(item.prepared.path, best.prepared.path) != 0 ||
			requestMethodSpecificity(item.prepared.route, method) != requestMethodSpecificity(best.prepared.route, method) {
			break
		}
		top = append(top, item.prepared.route)
	}
	if len(top) > 1 {
		sortRoutes(top)
		return snapshot, Match{Revision: snapshot.revision, Candidates: top}, ErrAmbiguousRoute
	}
	for _, conflict := range snapshot.conflicts {
		if conflict.Kind == ConflictRouteIdentity && conflict.RouteID == best.prepared.route.ID &&
			methodsOverlap(conflict.Method, method) {
			return snapshot, Match{Revision: snapshot.revision, Candidates: conflict.Candidates}, ErrAmbiguousRoute
		}
	}

	replacements := make([]Route, 0)
	for _, item := range snapshot.routes {
		if item.route.Action != extensionmanifest.RouteActionReplace || item.route.TargetID != best.prepared.route.ID ||
			!routeMethodMatches(item.route, method) || !r.routeAdmitted(item.route) {
			continue
		}
		if _, ok := item.path.match(requestPath); ok {
			replacements = append(replacements, item.route)
		}
	}
	if len(replacements) > 0 {
		top = append([]Route{best.prepared.route}, replacements...)
		sortRoutes(top)
		return snapshot, Match{Revision: snapshot.revision, Candidates: top}, ErrAmbiguousRoute
	}

	contributions := make([]Route, 0)
	for _, item := range snapshot.routes {
		if item.route.Provider.Kind != ProviderPlugin {
			continue
		}
		if !r.routeAdmitted(item.route) {
			continue
		}
		if item.route.Action == extensionmanifest.RouteActionGlobalMiddleware {
			contributions = append(contributions, item.route)
			continue
		}
		if !compositionAction(item.route.Action) || item.route.TargetID != best.prepared.route.ID ||
			!routeMethodMatches(item.route, method) {
			continue
		}
		if _, ok := item.path.match(requestPath); ok {
			contributions = append(contributions, item.route)
		}
	}
	sortRoutes(contributions)
	return snapshot, Match{
		Revision: snapshot.revision, Route: best.prepared.route, Params: best.params,
		Contributions: contributions,
	}, nil
}

func preparePublication(input Publication) (*registrySnapshot, error) {
	prepared := make([]preparedRoute, 0, len(input.Core))
	seen := make(map[string]struct{})
	for _, core := range input.Core {
		items, err := prepareCoreRoute(core)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if err := appendUniqueRoute(&prepared, seen, item); err != nil {
				return nil, err
			}
		}
	}

	// Safe Mode must boot from Host defaults even when an installed plugin snapshot is corrupt.
	if !input.SafeMode {
		seenExtensions := make(map[string]struct{}, len(input.Plugins))
		for _, plugin := range input.Plugins {
			if err := validatePluginArtifact(plugin.Artifact); err != nil {
				return nil, err
			}
			if _, duplicate := seenExtensions[plugin.Artifact.ExtensionID]; duplicate {
				return nil, fmt.Errorf("%w: extension %q appears more than once", ErrInvalidRoute, plugin.Artifact.ExtensionID)
			}
			seenExtensions[plugin.Artifact.ExtensionID] = struct{}{}
			guardBindings, err := preparePluginGuardBindings(plugin.Artifact, plugin.Guards)
			if err != nil {
				return nil, err
			}
			for _, declaration := range plugin.Routes {
				items, err := preparePluginRoute(plugin.Artifact, declaration, guardBindings)
				if err != nil {
					return nil, err
				}
				for _, item := range items {
					if err := appendUniqueRoute(&prepared, seen, item); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := validateTargets(prepared); err != nil {
		return nil, err
	}
	sort.SliceStable(prepared, func(i, j int) bool { return routeLess(prepared[i], prepared[j]) })
	routeValues := make([]Route, len(prepared))
	for index := range prepared {
		routeValues[index] = prepared[index].route
	}
	policies, policyIndex, err := prepareRoutePolicyBindings(routeValues, input.Policies, input.SafeMode)
	if err != nil {
		return nil, err
	}
	publication := clonePublication(input)
	if input.SafeMode {
		publication.Plugins = nil
		publication.Policies = nil
	}
	return &registrySnapshot{
		safeMode: input.SafeMode, routes: prepared, routeValues: routeValues,
		conflicts: inspectConflicts(prepared), policies: policies, policyIndex: policyIndex,
		publication: publication,
	}, nil
}

func (r *Registry) routeAdmitted(route Route) bool {
	if route.Provider.Kind != ProviderPlugin {
		return true
	}
	r.admissionMu.RLock()
	admission := r.admission
	r.admissionMu.RUnlock()
	return admission == nil || admission(route.Provider.Artifact)
}

func routeLess(left, right preparedRoute) bool {
	leftGlobal := left.route.Action == extensionmanifest.RouteActionGlobalMiddleware
	rightGlobal := right.route.Action == extensionmanifest.RouteActionGlobalMiddleware
	if leftGlobal != rightGlobal {
		return !leftGlobal
	}
	if !leftGlobal {
		if order := comparePathSpecificity(left.path, right.path); order != 0 {
			return order > 0
		}
		if left.path.pattern != right.path.pattern {
			return left.path.pattern < right.path.pattern
		}
		if methodSpecificity(left.route.Method) != methodSpecificity(right.route.Method) {
			return methodSpecificity(left.route.Method) > methodSpecificity(right.route.Method)
		}
	}
	if left.route.Priority != right.route.Priority {
		return left.route.Priority > right.route.Priority
	}
	if left.route.Action != right.route.Action {
		return left.route.Action < right.route.Action
	}
	if left.route.ID != right.route.ID {
		return left.route.ID < right.route.ID
	}
	if left.route.ContractVersion != right.route.ContractVersion {
		return left.route.ContractVersion < right.route.ContractVersion
	}
	if left.route.Provider.Kind != right.route.Provider.Kind {
		return left.route.Provider.Kind < right.route.Provider.Kind
	}
	return left.route.Provider.Artifact.ExtensionID < right.route.Provider.Artifact.ExtensionID
}

func sortRoutes(routes []Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		return routeSortLess(routes[i], routes[j])
	})
}

// routeSortLess 是注册表目录的稳定排序键：优先级降序，随后动作 / ID / 提供者。
func routeSortLess(left, right Route) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Action != right.Action {
		return left.Action < right.Action
	}
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	if left.Provider.Kind != right.Provider.Kind {
		return left.Provider.Kind < right.Provider.Kind
	}
	return left.Provider.Artifact.ExtensionID < right.Provider.Artifact.ExtensionID
}

func (r *Registry) loadSnapshot() *registrySnapshot {
	if snapshot := r.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return &registrySnapshot{}
}

func snapshotView(snapshot *registrySnapshot) Snapshot {
	view := Snapshot{Revision: snapshot.revision, SafeMode: snapshot.safeMode}
	view.Routes = cloneRoutes(snapshot.routeValues)
	view.Conflicts = cloneConflicts(snapshot.conflicts)
	view.Policies = cloneRoutePolicyBindings(snapshot.policies)
	return view
}

func planningView(snapshot *registrySnapshot) planningSnapshot {
	if snapshot == nil {
		return planningSnapshot{}
	}
	return planningSnapshot{
		revision: snapshot.revision, safeMode: snapshot.safeMode,
		routes: snapshot.routeValues, conflicts: snapshot.conflicts, policyIndex: snapshot.policyIndex,
	}
}

// executionPlanningView keeps the immutable catalog zero-copy and evaluates
// exact-runtime admission only for candidates touched by one request plan.
func (r *Registry) executionPlanningView(snapshot *registrySnapshot) planningSnapshot {
	view := planningView(snapshot)
	if r != nil {
		view.admit = r.routeAdmitted
	}
	return view
}

func (s planningSnapshot) routeAdmitted(route Route) bool {
	return route.Provider.Kind == ProviderCore || s.admit == nil || s.admit(route)
}

func publicPlanningView(snapshot Snapshot) planningSnapshot {
	return planningSnapshot{
		revision: snapshot.Revision, safeMode: snapshot.SafeMode,
		routes: snapshot.Routes, conflicts: snapshot.Conflicts,
		policyIndex: indexRoutePolicyBindings(snapshot.Policies),
	}
}

func cloneRoute(value Route) Route {
	value.MutableRequestFields = append([]string(nil), value.MutableRequestFields...)
	value.MutableResponseFields = append([]string(nil), value.MutableResponseFields...)
	value.CoreGuard = cloneCoreGuardDescriptor(value.CoreGuard)
	value.PluginGuard = clonePluginGuardBinding(value.PluginGuard)
	return value
}

func cloneRoutes(values []Route) []Route {
	result := append([]Route(nil), values...)
	for index := range result {
		result[index] = cloneRoute(result[index])
	}
	return result
}

func cloneConflicts(values []Conflict) []Conflict {
	result := make([]Conflict, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Candidates = cloneRoutes(value.Candidates)
	}
	return result
}

func cloneMatch(value Match) Match {
	value.Route = cloneRoute(value.Route)
	if value.Params != nil {
		value.Params = cloneRouteExecutionParams(value.Params)
	}
	value.Contributions = cloneRoutes(value.Contributions)
	value.Candidates = cloneRoutes(value.Candidates)
	return value
}

func clonePublication(value Publication) Publication {
	result := Publication{SafeMode: value.SafeMode}
	result.Core = append([]CoreRoute(nil), value.Core...)
	for index := range result.Core {
		result.Core[index].Guard = cloneCoreGuardDescriptor(result.Core[index].Guard)
	}
	result.Plugins = make([]PluginRouteSet, len(value.Plugins))
	for index, plugin := range value.Plugins {
		result.Plugins[index] = PluginRouteSet{
			Artifact: plugin.Artifact,
			Routes:   append([]extensionmanifest.ManifestRoute(nil), plugin.Routes...),
			Guards:   append([]extensionmanifest.ManifestGuard(nil), plugin.Guards...),
		}
		for routeIndex := range result.Plugins[index].Routes {
			result.Plugins[index].Routes[routeIndex].Methods = append(
				[]string(nil), plugin.Routes[routeIndex].Methods...,
			)
			result.Plugins[index].Routes[routeIndex].MutableRequestFields = append(
				[]string(nil), plugin.Routes[routeIndex].MutableRequestFields...,
			)
			result.Plugins[index].Routes[routeIndex].MutableResponseFields = append(
				[]string(nil), plugin.Routes[routeIndex].MutableResponseFields...,
			)
		}
		for guardIndex := range result.Plugins[index].Guards {
			result.Plugins[index].Guards[guardIndex].Permissions = append(
				[]string(nil), plugin.Guards[guardIndex].Permissions...,
			)
		}
	}
	result.Policies = cloneRoutePolicyBindings(value.Policies)
	return result
}
