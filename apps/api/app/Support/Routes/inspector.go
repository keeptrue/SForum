package routes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var ErrInspectorInvalid = errors.New("routes: invalid inspector request")

type InspectionResolution string

const (
	InspectionResolved  InspectionResolution = "resolved"
	InspectionNotFound  InspectionResolution = "not_found"
	InspectionAmbiguous InspectionResolution = "ambiguous"
	InspectionStale     InspectionResolution = "stale"
)

type InspectorProvider struct {
	Kind     ProviderKind    `json:"kind"`
	Artifact *PluginArtifact `json:"artifact,omitempty"`
}

type InspectorStep struct {
	Index                 int                 `json:"index"`
	Phase                 RouteExecutionPhase `json:"phase"`
	Action                string              `json:"action"`
	RouteID               string              `json:"routeId"`
	ContractVersion       string              `json:"contractVersion"`
	TargetRouteID         string              `json:"targetRouteId,omitempty"`
	Method                string              `json:"method"`
	Path                  string              `json:"path"`
	PathSignature         string              `json:"pathSignature"`
	Provider              InspectorProvider   `json:"provider"`
	Guard                 string              `json:"guard"`
	Access                string              `json:"access"`
	Permission            string              `json:"permission,omitempty"`
	Handler               string              `json:"handler,omitempty"`
	Destination           string              `json:"destination,omitempty"`
	StatusCode            int                 `json:"statusCode,omitempty"`
	RequestSchema         string              `json:"requestSchema,omitempty"`
	ResponseSchema        string              `json:"responseSchema,omitempty"`
	MutableRequestFields  []string            `json:"mutableRequestFields,omitempty"`
	MutableResponseFields []string            `json:"mutableResponseFields,omitempty"`
	Mode                  string              `json:"mode"`
	Fallback              string              `json:"fallback"`
	TimeoutMS             int                 `json:"timeoutMs"`
	Priority              int                 `json:"priority"`
	PluginGuard           *PluginGuardBinding `json:"pluginGuard,omitempty"`
	// GuardRisk 是授权归属分类：host_owned | extension_code | raw_request。
	GuardRisk          string `json:"guardRisk"`
	GuardRequiresTrust bool   `json:"guardRequiresTrust"`
}

type InspectorConflict struct {
	Kind            ConflictKind          `json:"kind"`
	RouteID         string                `json:"routeId,omitempty"`
	ContractVersion string                `json:"contractVersion,omitempty"`
	Method          string                `json:"method"`
	PathSignature   string                `json:"pathSignature"`
	Candidates      []InspectorStep       `json:"candidates"`
	SelectionKey    *ProviderSelectionKey `json:"selectionKey,omitempty"`
	Desired         *ProviderSelection    `json:"desired,omitempty"`
	SelectionStatus string                `json:"selectionStatus,omitempty"`
}

type InspectorProviderResolution struct {
	Status  string             `json:"status"`
	Desired *ProviderSelection `json:"desired,omitempty"`
	Live    *InspectorProvider `json:"live,omitempty"`
}

// RouteInspectorSnapshot is a caller-owned, detached capture. It never holds
// Registry slices, request data, response data, or process handles.
type RouteInspectorSnapshot struct {
	Revision   uint64                      `json:"revision"`
	SafeMode   bool                        `json:"safeMode"`
	Method     string                      `json:"method"`
	Resolution InspectionResolution        `json:"resolution"`
	Chain      []InspectorStep             `json:"chain"`
	Provider   InspectorProviderResolution `json:"provider"`
	Conflicts  []InspectorConflict         `json:"conflicts"`
	Traces     []RouteTraceRecord          `json:"traces"`
}

type Inspector struct {
	registry  *Registry
	providers *ProviderSelectionAPI
	traces    RouteTraceReader
}

func NewInspector(registry *Registry, providers *ProviderSelectionAPI, traces RouteTraceReader) *Inspector {
	return &Inspector{registry: registry, providers: providers, traces: traces}
}

// NewProviderSelectionInspector derives the Inspector from the already
// production-bound selection API, guaranteeing both surfaces observe the same
// immutable Registry rather than constructing a second snapshot owner.
func NewProviderSelectionInspector(providers *ProviderSelectionAPI, traces RouteTraceReader) *Inspector {
	if providers == nil {
		return nil
	}
	return NewInspector(providers.registry, providers, traces)
}

func (i *Inspector) Inspect(ctx context.Context, method, requestPath string) (RouteInspectorSnapshot, error) {
	if i == nil || i.registry == nil || ctx == nil {
		return RouteInspectorSnapshot{}, ErrInspectorInvalid
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	requestPath, err := normalizeRequestPath(requestPath)
	if err != nil || !validMethod(method) || method == "*" {
		return RouteInspectorSnapshot{}, ErrInspectorInvalid
	}
	for attempt := 0; attempt < 4; attempt++ {
		registrySnapshot := i.registry.Snapshot()
		result, inspectErr := i.inspectRevision(ctx, registrySnapshot, method, requestPath)
		if inspectErr != nil {
			return RouteInspectorSnapshot{}, inspectErr
		}
		if i.registry.Revision() == registrySnapshot.Revision {
			return result, nil
		}
	}
	return RouteInspectorSnapshot{}, fmt.Errorf("%w: registry revision changed during inspection", ErrRevisionConflict)
}

func (i *Inspector) inspectRevision(
	ctx context.Context,
	snapshot Snapshot,
	method string,
	requestPath string,
) (RouteInspectorSnapshot, error) {
	result := RouteInspectorSnapshot{
		Revision: snapshot.Revision, SafeMode: snapshot.SafeMode, Method: method,
		Chain: []InspectorStep{}, Provider: InspectorProviderResolution{Status: "not_required"},
		Conflicts: []InspectorConflict{}, Traces: []RouteTraceRecord{},
	}
	providerConflicts := map[string]ProviderSelectionConflict{}
	if i.providers != nil {
		items, err := i.providers.Conflicts(ctx)
		if err != nil {
			return RouteInspectorSnapshot{}, err
		}
		for _, item := range items {
			providerConflicts[inspectorConflictKey(item.Conflict)] = item
		}
	}
	for _, conflict := range snapshot.Conflicts {
		if !inspectorConflictMatches(conflict, method, requestPath) {
			continue
		}
		view := inspectorConflict(conflict)
		if selected, ok := providerConflicts[inspectorConflictKey(conflict)]; ok {
			key := selected.Key
			view.SelectionKey = &key
			view.SelectionStatus = selected.SelectionStatus
			view.Desired = cloneInspectorSelection(selected.Selection)
		}
		result.Conflicts = append(result.Conflicts, view)
	}

	var plan RouteExecutionPlan
	var err error
	if i.providers != nil {
		plan, err = i.providers.BuildExecutionPlan(ctx, method, requestPath)
	} else {
		plan, err = i.registry.BuildExecutionPlan(method, requestPath)
	}
	switch {
	case err == nil:
		result.Resolution = InspectionResolved
		result.Chain = inspectorChain(plan)
		terminal := plan.Terminal()
		live := inspectorProvider(terminal.Provider)
		result.Provider.Live = &live
		if terminal.Action == "replace" {
			result.Provider.Status = "selected"
			for _, conflict := range result.Conflicts {
				if conflict.Kind == ConflictProviderSelection && conflict.RouteID == terminal.TargetID &&
					methodsOverlap(conflict.Method, method) {
					result.Provider.Status = conflict.SelectionStatus
					result.Provider.Desired = cloneInspectorSelection(conflict.Desired)
					break
				}
			}
		}
	case errors.Is(err, ErrRouteNotFound):
		result.Resolution = InspectionNotFound
	case errors.Is(err, ErrAmbiguousRoute):
		result.Resolution = InspectionAmbiguous
		result.Provider = relevantInspectorProvider(result.Conflicts, method, requestPath)
	case errors.Is(err, ErrProviderSelectionStale):
		result.Resolution = InspectionStale
		result.Provider = relevantInspectorProvider(result.Conflicts, method, requestPath)
	default:
		return RouteInspectorSnapshot{}, err
	}
	if i.traces != nil {
		result.Traces = relevantInspectorTraces(i.traces.RouteTraces(50), result, method)
	}
	return result, nil
}

func inspectorConflictMatches(conflict Conflict, method, requestPath string) bool {
	if !methodsOverlap(conflict.Method, method) {
		return false
	}
	for _, candidate := range conflict.Candidates {
		compiled, err := compileRoutePath(candidate.Path)
		if err != nil {
			continue
		}
		if _, ok := compiled.match(requestPath); ok {
			return true
		}
	}
	return false
}

func relevantInspectorTraces(records []RouteTraceRecord, snapshot RouteInspectorSnapshot, method string) []RouteTraceRecord {
	routeIDs := make(map[string]struct{})
	for _, step := range snapshot.Chain {
		routeIDs[step.RouteID] = struct{}{}
	}
	for _, conflict := range snapshot.Conflicts {
		for _, candidate := range conflict.Candidates {
			routeIDs[candidate.RouteID] = struct{}{}
		}
	}
	result := make([]RouteTraceRecord, 0, len(records))
	for _, record := range records {
		if _, ok := routeIDs[record.RouteID]; !ok || record.Method != method {
			continue
		}
		result = append(result, record)
	}
	return result
}

func inspectorChain(plan RouteExecutionPlan) []InspectorStep {
	chain := plan.Chain()
	result := make([]InspectorStep, 0, len(chain))
	for index, step := range chain {
		result = append(result, inspectorExecutionStep(index, step, routeStepPathSignature(step)))
	}
	return result
}

func inspectorExecutionStep(index int, step RouteExecutionStep, signature string) InspectorStep {
	result := InspectorStep{
		Index: index, Phase: step.Phase, Action: step.Action, RouteID: step.RouteID,
		ContractVersion: step.ContractVersion, TargetRouteID: step.TargetID,
		Method: step.Method, Path: step.Path, PathSignature: signature,
		Provider: inspectorProvider(step.Provider), Guard: step.Guard, Access: step.Access,
		Permission: step.Permission, Handler: step.Handler, Destination: step.Destination, StatusCode: step.StatusCode,
		RequestSchema: step.RequestSchema, ResponseSchema: step.ResponseSchema,
		MutableRequestFields:  append([]string(nil), step.MutableRequestFields...),
		MutableResponseFields: append([]string(nil), step.MutableResponseFields...),
		Mode:                  step.Mode, Fallback: step.Fallback, TimeoutMS: step.TimeoutMS, Priority: step.Priority,
	}
	if step.PluginGuard.ID != "" {
		binding := clonePluginGuardBinding(step.PluginGuard)
		result.PluginGuard = &binding
	}
	// M5：检视器必须能回答「这条路由的授权由宿主还是某个精确插件制品拥有」。
	// 风险只由已发布的守卫绑定推导，不读取包内文件，也不暴露任何秘密。
	guardKind := ""
	if result.PluginGuard != nil {
		guardKind = result.PluginGuard.Kind
	}
	result.GuardRisk = extensionmanifest.GuardRuntimeRisk(step.Guard, guardKind)
	result.GuardRequiresTrust = extensionmanifest.GuardRequiresExactArtifactTrust(step.Guard, guardKind)
	return result
}

func inspectorConflict(conflict Conflict) InspectorConflict {
	result := InspectorConflict{
		Kind: conflict.Kind, RouteID: conflict.RouteID, ContractVersion: conflict.ContractVersion,
		Method: conflict.Method, PathSignature: conflict.PathSignature,
		Candidates: make([]InspectorStep, 0, len(conflict.Candidates)),
	}
	for index, candidate := range conflict.Candidates {
		step := routeExecutionStep(RoutePhaseHandler, candidate, candidate.CoreGuard)
		result.Candidates = append(result.Candidates, inspectorExecutionStep(index, step, candidate.PathSignature))
	}
	return result
}

func inspectorProvider(provider Provider) InspectorProvider {
	result := InspectorProvider{Kind: provider.Kind}
	if provider.Kind == ProviderPlugin {
		artifact := provider.Artifact
		result.Artifact = &artifact
	}
	return result
}

func relevantInspectorProvider(conflicts []InspectorConflict, method, requestPath string) InspectorProviderResolution {
	result := InspectorProviderResolution{Status: "unselected"}
	for _, conflict := range conflicts {
		if conflict.Kind != ConflictProviderSelection || !methodsOverlap(conflict.Method, method) {
			continue
		}
		matched := false
		for _, candidate := range conflict.Candidates {
			compiled, err := compileRoutePath(candidate.Path)
			if err == nil {
				if _, ok := compiled.match(requestPath); ok {
					matched = true
					break
				}
			}
		}
		if matched {
			result.Status = conflict.SelectionStatus
			result.Desired = cloneInspectorSelection(conflict.Desired)
			return result
		}
	}
	return result
}

func routeStepPathSignature(step RouteExecutionStep) string {
	if step.Path == "" {
		return ""
	}
	compiled, err := compileRoutePath(step.Path)
	if err != nil {
		return ""
	}
	return compiled.signature
}

func inspectorConflictKey(conflict Conflict) string {
	return string(conflict.Kind) + "\x00" + conflict.RouteID + "\x00" + conflict.Method + "\x00" + conflict.PathSignature
}

func cloneInspectorSelection(selection *ProviderSelection) *ProviderSelection {
	if selection == nil {
		return nil
	}
	copy := *selection
	return &copy
}
