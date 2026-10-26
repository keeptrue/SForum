package extensionopenapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	extRouteID         = "x-sforum-route-id"
	extContractVersion = "x-sforum-contract-version"
	extGuard           = "x-sforum-guard"
	extPermission      = "x-sforum-permission"
	extRequestSchema   = "x-sforum-request-schema"
	extResponseSchema  = "x-sforum-response-schema"
	extRateLimit       = "x-sforum-rate-limit"
	extIdempotency     = "x-sforum-idempotency"
)

type aggregateBuilder struct {
	paths           map[string]any
	sources         map[string]any
	sourceFiles     map[string]any
	sourceIdentity  []SourceIdentity
	operations      []GeneratedOperation
	pathMethods     map[string]GeneratedOperation
	operationIDs    map[string]string
	namespaces      map[string]string
	core            map[string]CoreOperation
	requirePolicies bool
}

func Build(input BuildInput) (Snapshot, error) {
	return buildAggregate(input, true)
}

// buildSchemaAggregate validates contract and schema identity without
// publishing plugin-declared policy metadata as Host authority.
func buildSchemaAggregate(input BuildInput) (Snapshot, error) {
	return buildAggregate(input, false)
}

func buildAggregate(input BuildInput, requirePolicies bool) (Snapshot, error) {
	budget := &resourceBudget{}
	builder := &aggregateBuilder{
		paths: make(map[string]any), sources: make(map[string]any), sourceFiles: make(map[string]any),
		pathMethods: make(map[string]GeneratedOperation), operationIDs: make(map[string]string),
		namespaces: make(map[string]string), core: make(map[string]CoreOperation), requirePolicies: requirePolicies,
	}
	if err := builder.addCore(input.Core); err != nil {
		return Snapshot{}, err
	}
	artifacts := append([]Artifact(nil), input.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool {
		left := artifacts[i].ExtensionID + "\x00" + artifacts[i].Version + "\x00" + artifacts[i].PackageDigest
		right := artifacts[j].ExtensionID + "\x00" + artifacts[j].Version + "\x00" + artifacts[j].PackageDigest
		return left < right
	})
	seenArtifacts := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		identity := artifact.ExtensionID + "\x00" + artifact.Version + "\x00" + artifact.PackageDigest
		if _, duplicate := seenArtifacts[identity]; duplicate {
			return Snapshot{}, fmt.Errorf("%w: duplicate artifact %s@%s", ErrInvalidArtifact, artifact.ExtensionID, artifact.Version)
		}
		seenArtifacts[identity] = struct{}{}
		loaded, err := loadArtifact(artifact, budget, input.Loader)
		if err != nil {
			return Snapshot{}, err
		}
		if err := builder.addArtifact(loaded); err != nil {
			return Snapshot{}, err
		}
	}
	return builder.snapshot()
}

func (b *aggregateBuilder) addCore(core []CoreOperation) error {
	items := append([]CoreOperation(nil), core...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].Path+"\x00"+items[i].Method+"\x00"+items[i].RouteID <
			items[j].Path+"\x00"+items[j].Method+"\x00"+items[j].RouteID
	})
	for _, item := range items {
		pathValue, signature, err := canonicalOpenAPIPath(item.Path)
		if err != nil || item.Path != strings.TrimSpace(item.Path) ||
			item.RouteID == "" || item.RouteID != strings.TrimSpace(item.RouteID) ||
			item.OperationID == "" || item.OperationID != strings.TrimSpace(item.OperationID) ||
			item.Method != strings.TrimSpace(item.Method) || item.Method != strings.ToUpper(item.Method) ||
			!validOpenAPIMethod(item.Method) {
			return fmt.Errorf("%w: invalid core operation %q", ErrInvalidDocument, item.RouteID)
		}
		item.Path = pathValue
		key := pathMethodKey(signature, item.Method)
		if previous, duplicate := b.core[key]; duplicate {
			return fmt.Errorf("%w: core %s conflicts with %s at %s %s", ErrCollision, item.RouteID, previous.RouteID, item.Method, pathValue)
		}
		if previous, duplicate := b.operationIDs[item.OperationID]; duplicate {
			return fmt.Errorf("%w: operationId %q already owned by %s", ErrCollision, item.OperationID, previous)
		}
		b.core[key] = item
		b.operationIDs[item.OperationID] = item.RouteID
	}
	return nil
}

func (b *aggregateBuilder) addArtifact(artifact *loadedArtifact) error {
	routes, err := routeContracts(artifact, b.requirePolicies)
	if err != nil {
		return err
	}
	fragments := append([]extensionmanifest.ManifestOpenAPIFragment(nil), artifact.manifest.OpenAPI...)
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].ID < fragments[j].ID })
	documented := make(map[string]struct{})
	for _, fragment := range fragments {
		owner := artifact.input.ExtensionID + ":" + fragment.ID
		if previous, duplicate := b.namespaces[fragment.Namespace]; duplicate {
			return fmt.Errorf("%w: namespace %q used by %s and %s", ErrCollision, fragment.Namespace, previous, owner)
		}
		b.namespaces[fragment.Namespace] = owner
		document, exists := artifact.documents[fragment.Path]
		if !exists {
			return fmt.Errorf("%w: missing fragment %s", ErrInvalidDocument, fragment.Path)
		}
		root, err := validateOpenAPIRoot(document, fragment)
		if err != nil {
			return err
		}
		identity := SourceIdentity{
			ExtensionID: artifact.input.ExtensionID, ExtensionVersion: artifact.input.Version,
			PackageDigest: artifact.input.PackageDigest, FragmentID: fragment.ID,
			ContractVersion: fragment.ContractVersion, Path: fragment.Path,
			Digest: fragment.Digest, Namespace: fragment.Namespace,
		}
		b.sourceIdentity = append(b.sourceIdentity, identity)
		if err := b.addFragmentOperations(artifact, fragment, identity, root, routes, documented); err != nil {
			return err
		}
	}
	if len(fragments) > 0 {
		for key, route := range routes {
			if route.addressable {
				if _, exists := documented[key]; !exists {
					return fmt.Errorf("%w: route %s %s is missing from OpenAPI fragments", ErrContractMismatch, route.route.ID, route.method)
				}
			}
		}
	}
	for _, sourcePath := range sortedDocumentPaths(artifact.documents) {
		sourceKey := artifact.sourceKeys[sourcePath]
		if _, duplicate := b.sources[sourceKey]; duplicate {
			return fmt.Errorf("%w: duplicate source identity", ErrCollision)
		}
		rewritten, err := rewriteReferences(artifact.documents[sourcePath], sourcePath, artifact.sourceKeys)
		if err != nil {
			return err
		}
		b.sources[sourceKey] = rewritten
		file := artifact.files[sourcePath]
		b.sourceFiles[sourceKey] = map[string]any{
			"extensionId": artifact.input.ExtensionID, "extensionVersion": artifact.input.Version,
			"packageDigest": artifact.input.PackageDigest, "path": sourcePath,
			"fileDigest": file.Digest, "kind": file.Kind,
		}
	}
	return nil
}

type routeContract struct {
	route       extensionmanifest.ManifestRoute
	method      string
	path        string
	signature   string
	policy      RoutePolicy
	addressable bool
}

func routeContracts(artifact *loadedArtifact, requirePolicies bool) (map[string]routeContract, error) {
	result := make(map[string]routeContract)
	legacyPolicies := len(artifact.policies) != 0
	if requirePolicies && len(artifact.manifest.OpenAPI) == 0 && legacyPolicies {
		return nil, fmt.Errorf("%w: policies exist without documented routes", ErrContractMismatch)
	}
	for _, route := range artifact.manifest.Routes {
		addressable := route.Action == extensionmanifest.RouteActionAdd || route.Action == extensionmanifest.RouteActionAlias ||
			route.Action == extensionmanifest.RouteActionRedirect || route.Action == extensionmanifest.RouteActionRewrite ||
			route.Action == extensionmanifest.RouteActionReplace
		if !addressable {
			continue
		}
		pathValue, signature, err := canonicalOpenAPIPath(route.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: route %s path: %v", ErrContractMismatch, route.ID, err)
		}
		for _, methodValue := range route.Methods {
			method := strings.ToUpper(methodValue)
			if !validOpenAPIMethod(method) {
				return nil, fmt.Errorf("%w: route %s method %s cannot be represented by OpenAPI 3.1", ErrContractMismatch, route.ID, method)
			}
			key := routeMethodKey(route.ID, method)
			policy, exists := artifact.policies[key]
			if requirePolicies && legacyPolicies && !exists && len(artifact.manifest.OpenAPI) > 0 {
				return nil, fmt.Errorf("%w: missing authoritative policy for %s %s", ErrContractMismatch, route.ID, method)
			}
			if requirePolicies && legacyPolicies && exists {
				if expected := securityForGuard(route.Guard); expected != "" && policy.Security != expected {
					return nil, fmt.Errorf("%w: security policy %q contradicts guard %q for %s %s", ErrContractMismatch, policy.Security, route.Guard, route.ID, method)
				}
			}
			if _, duplicate := result[key]; duplicate {
				return nil, fmt.Errorf("%w: duplicate route method %s %s", ErrContractMismatch, route.ID, method)
			}
			result[key] = routeContract{route: route, method: method, path: pathValue, signature: signature, policy: policy, addressable: true}
		}
	}
	if requirePolicies && legacyPolicies && len(artifact.manifest.OpenAPI) > 0 {
		if len(artifact.policies) != len(result) {
			return nil, fmt.Errorf("%w: route policy set is not exact", ErrContractMismatch)
		}
		for key := range artifact.policies {
			if _, exists := result[key]; !exists {
				return nil, fmt.Errorf("%w: stale or extra route policy %q", ErrContractMismatch, key)
			}
		}
	}
	return result, nil
}

func securityForGuard(guard string) string {
	switch guard {
	case extensionmanifest.GuardCorePublic, extensionmanifest.GuardCoreGuest:
		return SecurityPublic
	case extensionmanifest.GuardCoreLogin, extensionmanifest.GuardCorePermission:
		return SecurityAuthenticated
	default:
		return ""
	}
}

func (b *aggregateBuilder) addFragmentOperations(
	artifact *loadedArtifact,
	fragment extensionmanifest.ManifestOpenAPIFragment,
	identity SourceIdentity,
	root map[string]any,
	routes map[string]routeContract,
	documented map[string]struct{},
) error {
	paths := root["paths"].(map[string]any)
	pathNames := sortedMapKeys(paths)
	for _, pathName := range pathNames {
		if strings.HasPrefix(strings.ToLower(pathName), "x-") {
			continue
		}
		pathValue, signature, err := canonicalOpenAPIPath(pathName)
		if err != nil || pathValue != pathName {
			return fmt.Errorf("%w: non-canonical OpenAPI path %q", ErrInvalidDocument, pathName)
		}
		pathItem, ok := paths[pathName].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: path item %s must be an object", ErrInvalidDocument, pathName)
		}
		if _, indirect := pathItem["$ref"]; indirect {
			return fmt.Errorf("%w: path-level $ref is not allowed for routed operations", ErrInvalidDocument)
		}
		if err := validatePathItemKeys(pathItem); err != nil {
			return fmt.Errorf("%w: path %s: %v", ErrInvalidDocument, pathName, err)
		}
		for _, method := range openAPIMethodOrder {
			operationValue, exists := pathItem[strings.ToLower(method)]
			if !exists {
				continue
			}
			operation, ok := operationValue.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: %s %s operation must be an object", ErrInvalidDocument, method, pathName)
			}
			effective, err := effectiveOperation(pathItem, operation)
			if err != nil {
				return err
			}
			generated, route, err := validateOperation(
				effective, pathValue, method, fragment, identity, routes, artifact, fragment.Path, b.requirePolicies,
			)
			if err != nil {
				return err
			}
			routeKey := routeMethodKey(route.route.ID, method)
			if _, duplicate := documented[routeKey]; duplicate {
				return fmt.Errorf("%w: route %s %s documented more than once", ErrCollision, route.route.ID, method)
			}
			documented[routeKey] = struct{}{}
			collisionKey := pathMethodKey(signature, method)
			if core, collision := b.core[collisionKey]; collision &&
				(route.route.Action != extensionmanifest.RouteActionReplace || route.route.TargetID != core.RouteID) {
				return fmt.Errorf("%w: plugin route %s collides with core route %s at %s %s", ErrCollision, route.route.ID, core.RouteID, method, pathValue)
			}
			if previous, collision := b.pathMethods[collisionKey]; collision {
				return fmt.Errorf("%w: %s conflicts with %s at %s %s", ErrCollision, route.route.ID, previous.RouteID, method, pathValue)
			}
			if previous, collision := b.operationIDs[generated.OperationID]; collision {
				if core, replacing := b.core[collisionKey]; !replacing || route.route.Action != extensionmanifest.RouteActionReplace ||
					route.route.TargetID != core.RouteID || previous != core.RouteID {
					return fmt.Errorf("%w: operationId %q already owned by %s", ErrCollision, generated.OperationID, previous)
				}
			}
			b.operationIDs[generated.OperationID] = generated.RouteID
			b.pathMethods[collisionKey] = generated
			rewritten, err := rewriteReferences(effective, fragment.Path, artifact.sourceKeys)
			if err != nil {
				return err
			}
			pathAggregate, _ := b.paths[pathValue].(map[string]any)
			if pathAggregate == nil {
				pathAggregate = make(map[string]any)
				b.paths[pathValue] = pathAggregate
			}
			pathAggregate[strings.ToLower(method)] = rewritten
			b.operations = append(b.operations, generated)
		}
	}
	return nil
}

func (b *aggregateBuilder) snapshot() (Snapshot, error) {
	sort.Slice(b.sourceIdentity, func(i, j int) bool {
		return b.sourceIdentity[i].ExtensionID+"\x00"+b.sourceIdentity[i].FragmentID <
			b.sourceIdentity[j].ExtensionID+"\x00"+b.sourceIdentity[j].FragmentID
	})
	sort.Slice(b.operations, func(i, j int) bool {
		return b.operations[i].Path+"\x00"+b.operations[i].Method+"\x00"+b.operations[i].OperationID <
			b.operations[j].Path+"\x00"+b.operations[j].Method+"\x00"+b.operations[j].OperationID
	})
	document := map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "SForum Extension OpenAPI Aggregate", "version": "1"},
		"components": map[string]any{"securitySchemes": map[string]any{
			"cookieAuth": map[string]any{"type": "apiKey", "in": "cookie", "name": "sforum_session"},
			"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"},
		}},
		"paths":                     b.paths,
		"x-sforum-sources":          b.sources,
		"x-sforum-source-files":     b.sourceFiles,
		"x-sforum-source-artifacts": b.sourceIdentity,
		"x-sforum-generated-client": map[string]any{"version": "1", "operations": b.operations},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: encode aggregate: %v", ErrInvalidDocument, err)
	}
	if len(encoded) > maxAggregateBytes {
		return Snapshot{}, fmt.Errorf("%w: encoded aggregate exceeds %d bytes", ErrResourceBudget, maxAggregateBytes)
	}
	digest := sha256.Sum256(encoded)
	return Snapshot{
		revision: "sha256:" + hex.EncodeToString(digest[:]), document: encoded,
		sources:    append([]SourceIdentity(nil), b.sourceIdentity...),
		operations: append([]GeneratedOperation(nil), b.operations...),
	}, nil
}

func sortedMapKeys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
