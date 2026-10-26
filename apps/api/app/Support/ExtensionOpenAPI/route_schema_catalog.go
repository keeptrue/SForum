package extensionopenapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

var (
	ErrRouteSchemaCatalogInvalid   = errors.New("extension openapi: invalid route schema catalog")
	ErrRouteSchemaMissing          = errors.New("extension openapi: route schema is missing")
	ErrRouteSchemaAmbiguous        = errors.New("extension openapi: route schema is ambiguous")
	ErrRouteSchemaDuplicate        = errors.New("extension openapi: route schema id is duplicated")
	ErrRouteSchemaArtifactMismatch = errors.New("extension openapi: route schema artifact mismatch")
	ErrRouteSchemaPayloadInvalid   = errors.New("extension openapi: route schema payload is invalid")
)

const (
	maxRouteSchemaBindings = 4096
	maxRouteSchemaBytes    = 16 << 20
	maxRoutePayloadBytes   = 8 << 20
	maxRoutePayloadDepth   = 128
	maxRoutePayloadNodes   = 100_000
	maxRoutePayloadItems   = 100_000
	maxRouteDocumentNodes  = 1_000_000
	maxRouteDocumentItems  = 1_000_000

	maxRouteSchemaNodes         = 100_000
	maxRouteSchemaDepth         = 128
	maxRouteSchemaBranches      = 4096
	maxRouteSchemaRefExpansions = 4096

	defaultRouteSchemaValidationSlots   = 16
	defaultRouteSchemaValidationTimeout = 2 * time.Second
)

type RouteSchemaDirection string

const (
	RouteSchemaRequest  RouteSchemaDirection = "request"
	RouteSchemaResponse RouteSchemaDirection = "response"
)

type RouteSchemaBinding struct {
	ExtensionID      string               `json:"extensionId"`
	ExtensionVersion string               `json:"extensionVersion"`
	PackageDigest    string               `json:"packageDigest"`
	SchemaID         string               `json:"schemaId"`
	Direction        RouteSchemaDirection `json:"direction"`
	RouteID          string               `json:"routeId"`
	Method           string               `json:"method"`
	ContractVersion  string               `json:"contractVersion"`
	OperationID      string               `json:"operationId"`
	Action           string               `json:"action"`
	MediaType        string               `json:"mediaType"`
	ResponseStatus   string               `json:"responseStatus,omitempty"`
	SchemaDigest     string               `json:"schemaDigest"`
}

type routeSchemaCatalogEntry struct {
	binding RouteSchemaBinding
	schema  *jsonschema.Schema
}

type routeSchemaCandidate struct {
	binding   RouteSchemaBinding
	value     any
	canonical []byte
}

type routeSchemaDefinition struct {
	variants []routeSchemaVariant
}

type routeSchemaVariant struct {
	value          any
	canonical      []byte
	mediaType      string
	responseStatus string
}

type routeSchemaComplexityLimits struct {
	nodes         int
	depth         int
	branches      int
	refExpansions int
}

// RouteSchemaCatalog is immutable after construction. Keys include the exact
// package digest, so an upgraded artifact cannot reuse a compiled old schema.
type RouteSchemaCatalog struct {
	revision          string
	entries           map[string]routeSchemaCatalogEntry
	lookups           map[string]string
	artifactIndex     map[string]map[string]struct{}
	bindings          []RouteSchemaBinding
	validationSlots   chan struct{}
	validationTimeout time.Duration
}

func BuildRouteSchemaCatalog(input BuildInput) (*RouteSchemaCatalog, error) {
	if err := rejectRouteSchemaArtifactDrift(input.Artifacts); err != nil {
		return nil, err
	}
	aggregate, err := buildSchemaAggregate(input)
	if err != nil {
		return nil, err
	}
	document, err := decodeRouteSchemaJSON(aggregate.Document(), maxAggregateBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: aggregate JSON: %v", ErrRouteSchemaCatalogInvalid, err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: aggregate root", ErrRouteSchemaCatalogInvalid)
	}

	operations := aggregate.GeneratedClientOperations()
	catalog := &RouteSchemaCatalog{
		revision: aggregate.Revision(), entries: make(map[string]routeSchemaCatalogEntry),
		lookups: make(map[string]string), artifactIndex: make(map[string]map[string]struct{}),
		bindings:          make([]RouteSchemaBinding, 0, len(operations)*2),
		validationSlots:   make(chan struct{}, defaultRouteSchemaValidationSlots),
		validationTimeout: defaultRouteSchemaValidationTimeout,
	}
	sharedDefinitions := make(map[string]routeSchemaDefinition)
	operationDefinitions := make(map[string]routeSchemaDefinition)
	operationFallbacks := make(map[string]routeSchemaDefinition)
	ambiguousOperationFallbacks := make(map[string]bool)
	operationIDs := make(map[string]string)
	operationActions := make(map[string]string)
	operationModes := make(map[string]string)
	for _, artifactInput := range input.Artifacts {
		artifact := routes.PluginArtifact{
			ExtensionID: artifactInput.ExtensionID, ExtensionVersion: artifactInput.Version, PackageDigest: artifactInput.PackageDigest,
		}
		for _, route := range artifactInput.Manifest.Routes {
			for _, method := range route.Methods {
				key := routeSchemaOperationKey(artifact, route.ID, method, route.ContractVersion)
				operationActions[key] = route.Action
				operationModes[key] = route.Mode
			}
		}
	}
	for _, operation := range operations {
		operationValue, err := aggregateOperation(root, operation)
		if err != nil {
			return nil, err
		}
		artifact := routes.PluginArtifact{
			ExtensionID: operation.ExtensionID, ExtensionVersion: operation.ExtensionVersion, PackageDigest: operation.PackageDigest,
		}
		operationKey := routeSchemaOperationKey(artifact, operation.RouteID, operation.Method, operation.ContractVersion)
		action := operationActions[operationKey]
		if action == "" {
			return nil, fmt.Errorf("%w: operation %s route action", ErrRouteSchemaCatalogInvalid, operation.OperationID)
		}
		operationIDs[operationKey] = operation.OperationID
		if operationModes[operationKey] != extensionmanifest.RouteModeHTTP {
			// Non-HTTP bodies are opaque Protocol V2 chunks. Their OpenAPI media
			// contracts remain public metadata but cannot become JSON validators.
			continue
		}
		if operation.RequestSchema != "" {
			candidates, err := aggregateRequestSchemas(root, operationValue)
			if err != nil {
				return nil, fmt.Errorf("%w: %s request: %v", ErrRouteSchemaCatalogInvalid, operation.OperationID, err)
			}
			definitionKey := routeSchemaOperationDefinitionKey(artifact, operation.RouteID, operation.Method, operation.ContractVersion, action, operation.OperationID, operation.RequestSchema, RouteSchemaRequest)
			if err := addRouteSchemaDefinition(operationDefinitions, definitionKey, operation.RequestSchema, RouteSchemaRequest, candidates); err != nil {
				return nil, err
			}
			addRouteSchemaOperationFallback(
				operationFallbacks, ambiguousOperationFallbacks,
				routeSchemaDefinitionKey(artifact, operation.RequestSchema, RouteSchemaRequest), candidates,
			)
		}
		if operation.ResponseSchema != "" {
			candidates, err := aggregateResponseSchemas(root, operationValue)
			if err != nil {
				return nil, fmt.Errorf("%w: %s response: %v", ErrRouteSchemaCatalogInvalid, operation.OperationID, err)
			}
			definitionKey := routeSchemaOperationDefinitionKey(artifact, operation.RouteID, operation.Method, operation.ContractVersion, action, operation.OperationID, operation.ResponseSchema, RouteSchemaResponse)
			if err := addRouteSchemaDefinition(operationDefinitions, definitionKey, operation.ResponseSchema, RouteSchemaResponse, candidates); err != nil {
				return nil, err
			}
			addRouteSchemaOperationFallback(
				operationFallbacks, ambiguousOperationFallbacks,
				routeSchemaDefinitionKey(artifact, operation.ResponseSchema, RouteSchemaResponse), candidates,
			)
		}
	}

	candidates := make([]routeSchemaCandidate, 0, len(operations)*2)
	owners := make(map[string]string)
	for _, artifactInput := range input.Artifacts {
		artifact := routes.PluginArtifact{
			ExtensionID: artifactInput.ExtensionID, ExtensionVersion: artifactInput.Version, PackageDigest: artifactInput.PackageDigest,
		}
		if err := addComponentRouteSchemaDefinitions(root, sharedDefinitions, artifactInput, artifact); err != nil {
			return nil, err
		}
		for _, route := range artifactInput.Manifest.Routes {
			if route.Mode != extensionmanifest.RouteModeHTTP {
				continue
			}
			methods := append([]string(nil), route.Methods...)
			if route.Action == extensionmanifest.RouteActionGlobalMiddleware {
				methods = []string{"*"}
			}
			for _, method := range methods {
				operationID := operationIDs[routeSchemaOperationKey(artifact, route.ID, method, route.ContractVersion)]
				if operationID == "" {
					operationID = routeSchemaExecutionOperationID(route.ID, method, route.ContractVersion, route.Action)
				}
				for _, declaration := range []struct {
					direction RouteSchemaDirection
					reference string
				}{
					{RouteSchemaRequest, route.RequestSchema},
					{RouteSchemaResponse, route.ResponseSchema},
				} {
					if declaration.reference == "" {
						continue
					}
					definitionKey := routeSchemaOperationDefinitionKey(
						artifact, route.ID, method, route.ContractVersion, route.Action, operationID, declaration.reference, declaration.direction,
					)
					definition, err := routeSchemaDefinitionFor(
						root, operationDefinitions, sharedDefinitions, operationFallbacks, definitionKey,
						artifactInput, artifact, declaration.reference, declaration.direction,
					)
					if err != nil {
						return nil, fmt.Errorf("%w: %s %s %s: %v", ErrRouteSchemaCatalogInvalid, route.ID, method, declaration.direction, err)
					}
					for _, variant := range definition.variants {
						binding := RouteSchemaBinding{
							ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
							SchemaID: declaration.reference, Direction: declaration.direction, RouteID: route.ID, Method: method,
							ContractVersion: route.ContractVersion, OperationID: operationID, Action: route.Action,
							MediaType: variant.mediaType, ResponseStatus: variant.responseStatus,
						}
						binding.SchemaDigest = digestRouteSchema(variant.canonical)
						key := routeSchemaLookupKey(artifact, binding)
						owner := route.ID + "\x00" + method + "\x00" + string(declaration.direction)
						if previous, duplicate := owners[key]; duplicate {
							return nil, fmt.Errorf("%w: %s owned by %s and %s", ErrRouteSchemaDuplicate, declaration.reference, previous, owner)
						}
						owners[key] = owner
						candidates = append(candidates, routeSchemaCandidate{binding: binding, value: variant.value, canonical: variant.canonical})
					}
				}
			}
		}
	}
	if len(candidates) > maxRouteSchemaBindings {
		return nil, fmt.Errorf("%w: schema binding count exceeds %d", ErrResourceBudget, maxRouteSchemaBindings)
	}
	if _, err := routeSchemaClosureBytes(root, candidates, maxRouteSchemaBytes); err != nil {
		return nil, err
	}
	if err := enforceRouteSchemaComplexity(root, candidates, routeSchemaComplexityLimits{
		nodes: maxRouteSchemaNodes, depth: maxRouteSchemaDepth, branches: maxRouteSchemaBranches,
		refExpansions: maxRouteSchemaRefExpansions,
	}); err != nil {
		return nil, err
	}
	compiled, err := compileRouteSchemas(root, candidates, catalog.revision)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		artifact := routes.PluginArtifact{
			ExtensionID: candidate.binding.ExtensionID, ExtensionVersion: candidate.binding.ExtensionVersion,
			PackageDigest: candidate.binding.PackageDigest,
		}
		key := routeSchemaCatalogKey(artifact, candidate.binding)
		lookup := routeSchemaLookupKey(artifact, candidate.binding)
		catalog.entries[key] = routeSchemaCatalogEntry{binding: candidate.binding, schema: compiled[candidate.binding.SchemaDigest]}
		catalog.lookups[lookup] = key
		catalog.bindings = append(catalog.bindings, candidate.binding)
		versionKey := routeSchemaArtifactVersionKey(candidate.binding.ExtensionID, candidate.binding.ExtensionVersion)
		if catalog.artifactIndex[versionKey] == nil {
			catalog.artifactIndex[versionKey] = make(map[string]struct{})
		}
		catalog.artifactIndex[versionKey][candidate.binding.PackageDigest] = struct{}{}
	}
	sort.Slice(catalog.bindings, func(i, j int) bool {
		left, right := catalog.bindings[i], catalog.bindings[j]
		return routeSchemaBindingSortKey(left) < routeSchemaBindingSortKey(right)
	})
	return catalog, nil
}

func (c *RouteSchemaCatalog) Revision() string {
	if c == nil {
		return ""
	}
	return c.revision
}

func (c *RouteSchemaCatalog) Bindings() []RouteSchemaBinding {
	if c == nil {
		return nil
	}
	return append([]RouteSchemaBinding(nil), c.bindings...)
}

// ValidateRouteSchema directly satisfies app/Http.RouteSchemaCatalog without
// importing the HTTP package or coupling the catalog to Fiber.
func (c *RouteSchemaCatalog) ValidateRouteSchema(
	ctx context.Context,
	artifact routes.PluginArtifact,
	direction string,
	routeID string,
	method string,
	actualMethod string,
	contractVersion string,
	action string,
	reference string,
	mediaType string,
	responseStatus int,
	payload []byte,
) error {
	directionValue := RouteSchemaDirection(direction)
	mediaTypeValue, mediaErr := canonicalRouteSchemaMediaType(mediaType)
	if c == nil || ctx == nil || artifact.ExtensionID == "" || artifact.ExtensionVersion == "" ||
		len(artifact.PackageDigest) != 64 || reference == "" || reference != strings.TrimSpace(reference) ||
		routeID == "" || routeID != strings.TrimSpace(routeID) || contractVersion == "" || contractVersion != strings.TrimSpace(contractVersion) ||
		action == "" || action != strings.TrimSpace(action) || !validRouteSchemaLookupMethod(method) ||
		!validRouteSchemaLookupMethod(actualMethod) || actualMethod == "*" ||
		method != "*" && actualMethod != method && !(method == "GET" && actualMethod == "HEAD") ||
		(directionValue != RouteSchemaRequest && directionValue != RouteSchemaResponse) || mediaErr != nil || mediaTypeValue != mediaType ||
		(directionValue == RouteSchemaRequest && responseStatus != 0) ||
		(directionValue == RouteSchemaResponse && (responseStatus < 100 || responseStatus > 599)) {
		return ErrRouteSchemaCatalogInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	binding := RouteSchemaBinding{
		SchemaID: reference, Direction: directionValue, RouteID: routeID,
		Method: method, ContractVersion: contractVersion, Action: action, MediaType: mediaTypeValue,
	}
	entry, exists := c.lookupRouteSchema(artifact, binding, responseStatus)
	if !exists {
		artifactVersion := routeSchemaArtifactVersionKey(artifact.ExtensionID, artifact.ExtensionVersion)
		if digests := c.artifactIndex[artifactVersion]; len(digests) > 0 {
			if _, exact := digests[artifact.PackageDigest]; !exact {
				return ErrRouteSchemaArtifactMismatch
			}
		}
		return ErrRouteSchemaMissing
	}
	// HEAD inherits the selected GET operation's status and media contract, but
	// HTTP deliberately omits its representation body.
	if directionValue == RouteSchemaResponse && actualMethod == "HEAD" {
		return ctx.Err()
	}
	if err := validateRouteSchemaWithLimits(ctx, c.validationSlots, c.validationTimeout, func(validationCtx context.Context) error {
		value, err := decodeRouteSchemaJSONContext(
			validationCtx, payload, maxRoutePayloadBytes, maxRoutePayloadNodes, maxRoutePayloadItems,
		)
		if err != nil {
			return err
		}
		return entry.schema.Validate(value)
	}); err != nil {
		return fmt.Errorf("%w: %w", ErrRouteSchemaPayloadInvalid, err)
	}
	return ctx.Err()
}

func (c *RouteSchemaCatalog) lookupRouteSchema(
	artifact routes.PluginArtifact,
	binding RouteSchemaBinding,
	responseStatus int,
) (routeSchemaCatalogEntry, bool) {
	statuses := []string{""}
	if binding.Direction == RouteSchemaResponse {
		statuses = []string{
			strconv.Itoa(responseStatus), strconv.Itoa(responseStatus/100) + "XX", "default",
		}
	}
	for _, status := range statuses {
		binding.ResponseStatus = status
		entryKey, exists := c.lookups[routeSchemaLookupKey(artifact, binding)]
		if exists {
			return c.entries[entryKey], true
		}
	}
	return routeSchemaCatalogEntry{}, false
}

func addRouteSchemaDefinition(
	definitions map[string]routeSchemaDefinition,
	key string,
	schemaID string,
	direction RouteSchemaDirection,
	variants []routeSchemaVariant,
) error {
	definition := definitions[key]
	byStrategy := make(map[string]routeSchemaVariant, len(definition.variants)+len(variants))
	for _, variant := range definition.variants {
		byStrategy[routeSchemaVariantKey(variant)] = variant
	}
	for _, variant := range variants {
		strategy := routeSchemaVariantKey(variant)
		if previous, exists := byStrategy[strategy]; exists {
			if !bytes.Equal(previous.canonical, variant.canonical) {
				return fmt.Errorf("%w: %s has conflicting %s %s definitions", ErrRouteSchemaAmbiguous, schemaID, direction, strategy)
			}
			continue
		}
		byStrategy[strategy] = variant
	}
	strategies := make([]string, 0, len(byStrategy))
	for strategy := range byStrategy {
		strategies = append(strategies, strategy)
	}
	sort.Strings(strategies)
	definition.variants = make([]routeSchemaVariant, 0, len(strategies))
	for _, strategy := range strategies {
		definition.variants = append(definition.variants, byStrategy[strategy])
	}
	definitions[key] = definition
	return nil
}

// A non-addressable middleware may reuse an operation contract id only when
// every operation defines the exact same media/status/schema variants. Any
// route-specific difference removes the fallback instead of unioning variants.
func addRouteSchemaOperationFallback(
	fallbacks map[string]routeSchemaDefinition,
	ambiguous map[string]bool,
	key string,
	variants []routeSchemaVariant,
) {
	if ambiguous[key] {
		return
	}
	candidate := routeSchemaDefinition{variants: append([]routeSchemaVariant(nil), variants...)}
	previous, exists := fallbacks[key]
	if !exists {
		fallbacks[key] = candidate
		return
	}
	if !equalRouteSchemaDefinition(previous, candidate) {
		delete(fallbacks, key)
		ambiguous[key] = true
	}
}

func equalRouteSchemaDefinition(left, right routeSchemaDefinition) bool {
	if len(left.variants) != len(right.variants) {
		return false
	}
	for index := range left.variants {
		if routeSchemaVariantKey(left.variants[index]) != routeSchemaVariantKey(right.variants[index]) ||
			!bytes.Equal(left.variants[index].canonical, right.variants[index].canonical) {
			return false
		}
	}
	return true
}

func addComponentRouteSchemaDefinitions(
	root map[string]any,
	definitions map[string]routeSchemaDefinition,
	input Artifact,
	artifact routes.PluginArtifact,
) error {
	sources, ok := root["x-sforum-sources"].(map[string]any)
	if !ok {
		return fmt.Errorf("%w: aggregate sources", ErrRouteSchemaCatalogInvalid)
	}
	for _, fragment := range input.Manifest.OpenAPI {
		document, ok := sources[sourceKey(input, fragment.Path)].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: fragment source %s", ErrRouteSchemaCatalogInvalid, fragment.Path)
		}
		components, _ := document["components"].(map[string]any)
		schemas, _ := components["schemas"].(map[string]any)
		for schemaID, value := range schemas {
			canonical, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("%w: component schema %s", ErrRouteSchemaCatalogInvalid, schemaID)
			}
			if err := addRouteSchemaDefinition(definitions, routeSchemaDefinitionKey(artifact, schemaID, ""), schemaID, "", []routeSchemaVariant{{
				value: value, canonical: canonical, mediaType: "application/json", responseStatus: "default",
			}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func routeSchemaDefinitionFor(
	root map[string]any,
	operationDefinitions map[string]routeSchemaDefinition,
	sharedDefinitions map[string]routeSchemaDefinition,
	operationFallbacks map[string]routeSchemaDefinition,
	operationDefinitionKey string,
	input Artifact,
	artifact routes.PluginArtifact,
	reference string,
	direction RouteSchemaDirection,
) (routeSchemaDefinition, error) {
	if definition, exists := operationDefinitions[operationDefinitionKey]; exists {
		return definition, nil
	}
	if strings.HasSuffix(reference, ".json") {
		declared := false
		for _, file := range input.Manifest.PackageFiles {
			if file.Path == reference && file.Kind == "schema" {
				declared = true
				break
			}
		}
		if !declared {
			return routeSchemaDefinition{}, ErrRouteSchemaMissing
		}
		sources, _ := root["x-sforum-sources"].(map[string]any)
		value, exists := sources[sourceKey(input, reference)]
		if !exists {
			return routeSchemaDefinition{}, ErrRouteSchemaMissing
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return routeSchemaDefinition{}, ErrRouteSchemaCatalogInvalid
		}
		status := ""
		if direction == RouteSchemaResponse {
			status = "default"
		}
		return routeSchemaDefinition{variants: []routeSchemaVariant{{
			value: value, canonical: canonical, mediaType: "application/json", responseStatus: status,
		}}}, nil
	}
	if definition, exists := sharedDefinitions[routeSchemaDefinitionKey(artifact, reference, direction)]; exists {
		return definition, nil
	}
	if definition, exists := sharedDefinitions[routeSchemaDefinitionKey(artifact, reference, "")]; exists {
		variants := make([]routeSchemaVariant, len(definition.variants))
		copy(variants, definition.variants)
		for index := range variants {
			if direction == RouteSchemaRequest {
				variants[index].responseStatus = ""
			}
		}
		return routeSchemaDefinition{variants: variants}, nil
	}
	if definition, exists := operationFallbacks[routeSchemaDefinitionKey(artifact, reference, direction)]; exists {
		return definition, nil
	}
	return routeSchemaDefinition{}, ErrRouteSchemaMissing
}

func rejectRouteSchemaArtifactDrift(artifacts []Artifact) error {
	versions := make(map[string]string)
	for _, artifact := range artifacts {
		key := routeSchemaArtifactVersionKey(artifact.ExtensionID, artifact.Version)
		if digest, exists := versions[key]; exists && digest != artifact.PackageDigest {
			return fmt.Errorf("%w: %s@%s has multiple package digests", ErrRouteSchemaArtifactMismatch, artifact.ExtensionID, artifact.Version)
		}
		versions[key] = artifact.PackageDigest
	}
	return nil
}

func aggregateOperation(root map[string]any, operation GeneratedOperation) (map[string]any, error) {
	paths, ok := root["paths"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: aggregate paths", ErrRouteSchemaCatalogInvalid)
	}
	pathItem, ok := paths[operation.Path].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: operation path %s", ErrRouteSchemaMissing, operation.Path)
	}
	value, ok := pathItem[strings.ToLower(operation.Method)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: operation %s", ErrRouteSchemaMissing, operation.OperationID)
	}
	operationID, _ := value["operationId"].(string)
	if operationID != operation.OperationID {
		return nil, fmt.Errorf("%w: operation metadata drift", ErrRouteSchemaCatalogInvalid)
	}
	return value, nil
}

func aggregateRequestSchemas(root map[string]any, operation map[string]any) ([]routeSchemaVariant, error) {
	value, exists := operation["requestBody"]
	if !exists {
		return nil, ErrRouteSchemaMissing
	}
	body, err := resolveAggregateObject(root, value, 0)
	if err != nil {
		return nil, err
	}
	return aggregateContentSchemas(body["content"], "")
}

func aggregateResponseSchemas(root map[string]any, operation map[string]any) ([]routeSchemaVariant, error) {
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		return nil, ErrRouteSchemaMissing
	}
	statuses := sortedMapKeys(responses)
	result := make([]routeSchemaVariant, 0)
	for _, status := range statuses {
		response, err := resolveAggregateObject(root, responses[status], 0)
		if err != nil {
			return nil, err
		}
		if response["content"] == nil {
			continue
		}
		candidates, err := aggregateContentSchemas(response["content"], status)
		if err != nil {
			return nil, fmt.Errorf("response %s: %w", status, err)
		}
		result = append(result, candidates...)
	}
	if len(result) == 0 {
		return nil, ErrRouteSchemaMissing
	}
	return result, nil
}

func aggregateContentSchemas(value any, responseStatus string) ([]routeSchemaVariant, error) {
	content, ok := value.(map[string]any)
	if !ok || len(content) == 0 {
		return nil, ErrRouteSchemaMissing
	}
	mediaTypes := sortedMapKeys(content)
	result := make([]routeSchemaVariant, 0, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		normalized, err := canonicalRouteSchemaMediaType(mediaType)
		if err != nil {
			return nil, err
		}
		media, ok := content[mediaType].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("media type %s", mediaType)
		}
		if schema, exists := media["schema"]; exists {
			canonical, err := json.Marshal(schema)
			if err != nil {
				return nil, ErrRouteSchemaCatalogInvalid
			}
			result = append(result, routeSchemaVariant{
				value: schema, canonical: canonical, mediaType: normalized, responseStatus: responseStatus,
			})
		}
	}
	if len(result) == 0 {
		return nil, ErrRouteSchemaMissing
	}
	return result, nil
}

func canonicalRouteSchemaMediaType(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", ErrRouteSchemaCatalogInvalid
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	mediaType = strings.ToLower(mediaType)
	if err != nil || len(parameters) != 0 || strings.Contains(mediaType, "*") || !routeSchemaJSONMediaType(mediaType) {
		return "", fmt.Errorf("%w: invalid route schema media type %q", ErrRouteSchemaCatalogInvalid, value)
	}
	return mediaType, nil
}

func routeSchemaJSONMediaType(mediaType string) bool {
	if mediaType == "application/json" {
		return true
	}
	return strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json") &&
		len(mediaType) > len("application/+json")
}

func resolveAggregateObject(root map[string]any, value any, depth int) (map[string]any, error) {
	if depth > maxDocumentDepth {
		return nil, ErrResourceBudget
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrRouteSchemaCatalogInvalid
	}
	reference, referenced := object["$ref"]
	if !referenced {
		return object, nil
	}
	text, ok := reference.(string)
	if !ok || !strings.HasPrefix(text, "#/") {
		return nil, ErrUnsafeReference
	}
	target, err := resolveJSONPointer(root, strings.TrimPrefix(text, "#"))
	if err != nil {
		return nil, err
	}
	return resolveAggregateObject(root, target, depth+1)
}

func routeSchemaDefinitionKey(artifact routes.PluginArtifact, schemaID string, direction RouteSchemaDirection) string {
	return artifact.ExtensionID + "\x00" + artifact.ExtensionVersion + "\x00" + artifact.PackageDigest +
		"\x00" + string(direction) + "\x00" + schemaID
}

func routeSchemaLookupKey(artifact routes.PluginArtifact, binding RouteSchemaBinding) string {
	return artifact.ExtensionID + "\x00" + artifact.ExtensionVersion + "\x00" + artifact.PackageDigest +
		"\x00" + string(binding.Direction) + "\x00" + binding.RouteID + "\x00" + binding.Method +
		"\x00" + binding.ContractVersion + "\x00" + binding.Action + "\x00" + binding.SchemaID +
		"\x00" + binding.MediaType + "\x00" + binding.ResponseStatus
}

func routeSchemaCatalogKey(artifact routes.PluginArtifact, binding RouteSchemaBinding) string {
	return routeSchemaLookupKey(artifact, binding) + "\x00" + binding.OperationID
}

func routeSchemaOperationKey(artifact routes.PluginArtifact, routeID, method, contractVersion string) string {
	return artifact.ExtensionID + "\x00" + artifact.ExtensionVersion + "\x00" + artifact.PackageDigest +
		"\x00" + routeID + "\x00" + method + "\x00" + contractVersion
}

func routeSchemaOperationDefinitionKey(
	artifact routes.PluginArtifact,
	routeID, method, contractVersion, action, operationID, schemaID string,
	direction RouteSchemaDirection,
) string {
	return routeSchemaOperationKey(artifact, routeID, method, contractVersion) + "\x00" + action +
		"\x00" + operationID + "\x00" + string(direction) + "\x00" + schemaID
}

func routeSchemaExecutionOperationID(routeID, method, contractVersion, action string) string {
	return "runtime/" + action + "/" + routeID + "/" + method + "/" + contractVersion
}

func routeSchemaBindingSortKey(binding RouteSchemaBinding) string {
	return binding.ExtensionID + "\x00" + binding.ExtensionVersion + "\x00" + binding.PackageDigest +
		"\x00" + binding.RouteID + "\x00" + binding.Method + "\x00" + binding.ContractVersion +
		"\x00" + binding.Action + "\x00" + string(binding.Direction) + "\x00" + binding.SchemaID +
		"\x00" + binding.MediaType + "\x00" + binding.ResponseStatus
}

func routeSchemaVariantKey(variant routeSchemaVariant) string {
	return variant.mediaType + "\x00" + variant.responseStatus
}

func validRouteSchemaLookupMethod(method string) bool {
	if method == "*" {
		return true
	}
	if method == "" || method != strings.TrimSpace(method) || method != strings.ToUpper(method) {
		return false
	}
	for _, char := range method {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}

func routeSchemaArtifactVersionKey(extensionID, version string) string {
	return extensionID + "\x00" + version
}
