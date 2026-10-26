package routes

import (
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func inspectConflicts(routes []preparedRoute) []Conflict {
	conflicts := make([]Conflict, 0)
	pathGroups := make(map[string][]preparedRoute)
	identityGroups := make(map[string][]preparedRoute)
	replacements := make(map[string][]preparedRoute)
	baseByID := make(map[string][]preparedRoute)
	for _, item := range routes {
		if addressableAction(item.route.Action) {
			pathGroups[item.path.signature] = append(pathGroups[item.path.signature], item)
			identityGroups[item.route.ID] = append(identityGroups[item.route.ID], item)
			baseByID[item.route.ID] = append(baseByID[item.route.ID], item)
		}
		if item.route.Action == extensionmanifest.RouteActionReplace {
			replacements[item.route.TargetID+"\x00"+item.path.signature] = append(replacements[item.route.TargetID+"\x00"+item.path.signature], item)
		}
	}
	for _, group := range pathGroups {
		for _, overlap := range overlappingMethodGroups(group) {
			conflicts = append(conflicts, conflictFromPrepared(ConflictPathMethod, "", overlap.method, overlap.items))
		}
	}
	for _, group := range identityGroups {
		for _, overlap := range overlappingMethodGroups(group) {
			conflicts = append(conflicts, conflictFromPrepared(ConflictRouteIdentity, group[0].route.ID, overlap.method, overlap.items))
		}
	}
	for _, group := range replacements {
		candidates := append([]preparedRoute(nil), group...)
		for _, base := range baseByID[group[0].route.TargetID] {
			if group[0].path.signature == base.path.signature {
				candidates = append(candidates, base)
			}
		}
		for _, overlap := range overlappingMethodGroups(candidates) {
			hasReplacement := false
			for _, item := range overlap.items {
				if item.route.Action == extensionmanifest.RouteActionReplace {
					hasReplacement = true
					break
				}
			}
			if !hasReplacement {
				continue
			}
			conflicts = append(conflicts, conflictFromPrepared(
				ConflictProviderSelection, group[0].route.TargetID, overlap.method, overlap.items,
			))
		}
	}
	sort.SliceStable(conflicts, func(i, j int) bool {
		left, right := conflicts[i], conflicts[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.RouteID != right.RouteID {
			return left.RouteID < right.RouteID
		}
		if left.Method != right.Method {
			return left.Method < right.Method
		}
		return left.PathSignature < right.PathSignature
	})
	return conflicts
}

type methodConflictGroup struct {
	method string
	items  []preparedRoute
}

// overlappingMethodGroups expands wildcard methods against the concrete
// request methods they can claim. Candidate-set de-duplication also collapses
// the symmetric GET/HEAD overlap into one inspectable conflict.
func overlappingMethodGroups(items []preparedRoute) []methodConflictGroup {
	methods := make(map[string]struct{})
	for _, item := range items {
		if item.route.Method != "*" {
			methods[item.route.Method] = struct{}{}
		}
	}
	if len(methods) == 0 {
		methods["*"] = struct{}{}
	}
	orderedMethods := make([]string, 0, len(methods))
	for method := range methods {
		orderedMethods = append(orderedMethods, method)
	}
	sort.Strings(orderedMethods)

	result := make([]methodConflictGroup, 0, len(orderedMethods))
	seen := make(map[string]struct{}, len(orderedMethods))
	for _, method := range orderedMethods {
		group := make([]preparedRoute, 0, len(items))
		for _, item := range items {
			if methodsOverlap(item.route.Method, method) {
				group = append(group, item)
			}
		}
		if len(group) < 2 {
			continue
		}
		key := conflictCandidateSetKey(group)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, methodConflictGroup{method: method, items: group})
	}
	return result
}

func conflictCandidateSetKey(items []preparedRoute) string {
	routes := make([]Route, 0, len(items))
	for _, item := range items {
		routes = append(routes, item.route)
	}
	sortRoutes(routes)
	parts := make([]string, 0, len(routes))
	for _, route := range routes {
		parts = append(parts, strings.Join([]string{
			route.ID, route.ContractVersion, route.Action, route.TargetID, route.Method,
			route.PathSignature, string(route.Provider.Kind), route.Provider.Artifact.ExtensionID,
			route.Provider.Artifact.ExtensionVersion, route.Provider.Artifact.PackageDigest,
			route.Provider.Artifact.RuntimeInstanceID,
		}, "\x00"))
	}
	return strings.Join(parts, "\x01")
}

func conflictFromPrepared(kind ConflictKind, routeID, method string, items []preparedRoute) Conflict {
	routes := make([]Route, 0, len(items))
	for _, item := range items {
		routes = append(routes, item.route)
	}
	sortRoutes(routes)
	first := routes[0]
	contract := ""
	if routeID != "" {
		for _, route := range routes {
			if route.ID == routeID {
				contract = route.ContractVersion
				break
			}
		}
	}
	return Conflict{
		Kind: kind, RouteID: routeID, ContractVersion: contract, Method: method,
		PathSignature: first.PathSignature, Candidates: cloneRoutes(routes),
	}
}
