package extensionopenapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	"gopkg.in/yaml.v3"
)

type loadedArtifact struct {
	input      Artifact
	manifest   extensionmanifest.Manifest
	files      map[string]extensionmanifest.ManifestPackageFile
	documents  map[string]any
	sourceKeys map[string]string
	policies   map[string]RoutePolicy
	budget     *resourceBudget
}

func loadArtifact(input Artifact, budget *resourceBudget, loader *ArtifactLoader) (*loadedArtifact, error) {
	if cached, ok := loader.get(input); ok {
		return cached, nil
	}
	if strings.TrimSpace(input.Root) == "" || input.ExtensionID == "" || input.Version == "" ||
		len(input.PackageDigest) != 64 || input.PackageDigest != strings.ToLower(input.PackageDigest) {
		return nil, fmt.Errorf("%w: incomplete identity", ErrInvalidArtifact)
	}
	// 有界读取只用来挡住超大根清单；清单内容必须走 LoadPackage，
	// 否则带 includes 的**作者树**（settings/langs 分片）在这里会读到未合并的入口 JSON，
	// 与调用方传入的已合并快照不等，报成 "supplied manifest does not match snapshot"。
	// 已安装制品（存储副本已展开 includes）两侧结果一致，行为不变。
	if _, err := readLimitedRegularFile(pathOnDisk(input.Root, extensionmanifest.ManifestFileName), maxManifestBytes, budget); err != nil {
		return nil, fmt.Errorf("%w: read manifest: %w", ErrInvalidArtifact, err)
	}
	manifest, err := extensionmanifest.LoadPackage(input.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: load manifest: %v", ErrInvalidArtifact, err)
	}
	manifest = extensionmanifest.Normalize(manifest)
	if err := extensionmanifest.Validate(manifest); err != nil {
		return nil, fmt.Errorf("%w: validate manifest: %v", ErrInvalidArtifact, err)
	}
	if manifest.ID != input.ExtensionID || manifest.Version != input.Version ||
		extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 {
		return nil, fmt.Errorf("%w: manifest identity mismatch", ErrInvalidArtifact)
	}
	if !manifestsEqual(manifest, input.Manifest) {
		return nil, fmt.Errorf("%w: supplied manifest does not match snapshot", ErrInvalidArtifact)
	}

	loaded := &loadedArtifact{
		input: input, manifest: manifest,
		files:     make(map[string]extensionmanifest.ManifestPackageFile, len(manifest.PackageFiles)),
		documents: make(map[string]any), sourceKeys: make(map[string]string),
		policies: make(map[string]RoutePolicy, len(input.Policies)),
		budget:   budget,
	}
	for _, file := range manifest.PackageFiles {
		loaded.files[file.Path] = file
	}
	routeSecurity := make(map[string]string)
	for _, route := range manifest.Routes {
		if !openAPIAddressableRouteAction(route.Action) {
			continue
		}
		for _, method := range route.Methods {
			routeSecurity[routeMethodKey(route.ID, strings.ToUpper(method))] = securityForDeclaredGuard(route.Guard)
		}
	}
	for _, policy := range input.Policies {
		if policy.RouteID != strings.TrimSpace(policy.RouteID) || policy.Method != strings.TrimSpace(policy.Method) ||
			policy.Method != strings.ToUpper(policy.Method) {
			return nil, fmt.Errorf("%w: non-canonical route policy", ErrContractMismatch)
		}
		key := routeMethodKey(policy.RouteID, policy.Method)
		if policy.RouteID == "" || !validHTTPMethod(policy.Method) ||
			!validPolicyName(policy.RateLimit) || !validPolicyName(policy.Idempotency) || !validSecurityPolicy(policy.Security) {
			return nil, fmt.Errorf("%w: invalid route policy %q", ErrContractMismatch, key)
		}
		if _, duplicate := loaded.policies[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate route policy %q", ErrContractMismatch, key)
		}
		if expected, exists := routeSecurity[key]; exists && policy.Security != expected {
			return nil, fmt.Errorf(
				"%w: security policy %q contradicts guard ownership %q for %s",
				ErrContractMismatch, policy.Security, expected, key,
			)
		}
		loaded.policies[key] = policy
	}

	for _, fragment := range manifest.OpenAPI {
		if err := loaded.loadDocumentClosure(fragment.Path); err != nil {
			return nil, err
		}
	}
	// Route contracts may bind middleware schemas directly to a package-local
	// JSON schema without exposing a synthetic OpenAPI endpoint.
	for _, route := range manifest.Routes {
		for _, reference := range []string{route.RequestSchema, route.ResponseSchema} {
			if strings.HasSuffix(reference, ".json") {
				if err := loaded.loadDocumentClosure(reference); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := loaded.validateReferences(); err != nil {
		return nil, err
	}
	actualDigest, err := extensionpackage.VerifiedTreeDigest(input.Root)
	if err != nil || actualDigest != input.PackageDigest {
		return nil, fmt.Errorf("%w: package digest mismatch after static inspection", ErrInvalidArtifact)
	}
	loader.put(input, loaded)
	return loaded, nil
}

func manifestsEqual(left, right extensionmanifest.Manifest) bool {
	// 调用方可能持有尚未展开兼容默认值的精确清单快照。两侧统一归一化，
	// 避免新增确定性默认值后把同一包误判为 artifact 漂移。
	left = extensionmanifest.Normalize(left)
	right = extensionmanifest.Normalize(right)
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	var leftValue, rightValue any
	if json.Unmarshal(leftJSON, &leftValue) != nil || json.Unmarshal(rightJSON, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func (a *loadedArtifact) loadDocumentClosure(start string) error {
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, loaded := a.documents[current]; loaded {
			continue
		}
		file, declared := a.files[current]
		if !declared || (file.Kind != "openapi" && file.Kind != "schema") {
			return fmt.Errorf("%w: %s is not a declared OpenAPI/schema file", ErrUnsafeReference, current)
		}
		body, err := readLimitedRegularFile(pathOnDisk(a.input.Root, current), maxDocumentBytes, a.budget)
		if err != nil {
			return fmt.Errorf("%w: read %s: %w", ErrInvalidArtifact, current, err)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != file.Digest {
			return fmt.Errorf("%w: file digest mismatch for %s", ErrInvalidArtifact, current)
		}
		value, err := decodeYAMLOrJSON(body)
		if err != nil {
			return fmt.Errorf("%w: parse %s: %w", ErrInvalidDocument, current, err)
		}
		a.documents[current] = value
		a.sourceKeys[current] = sourceKey(a.input, current)
		refs, err := collectReferences(value)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrUnsafeReference, current, err)
		}
		if err := a.budget.reserveReferences(len(refs)); err != nil {
			return err
		}
		for _, reference := range refs {
			target, _, err := resolvePackageReference(current, reference)
			if err != nil {
				return err
			}
			if target != current {
				queue = append(queue, target)
			}
		}
	}
	return nil
}

func decodeYAMLOrJSON(body []byte) (any, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return nil, err
	}
	if len(node.Content) != 1 {
		return nil, fmt.Errorf("document must contain exactly one root")
	}
	value, err := yamlNodeValue(node.Content[0], 0)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("document root must be an object")
	}
	return value, nil
}

func yamlNodeValue(node *yaml.Node, depth int) (any, error) {
	if depth > maxDocumentDepth {
		return nil, fmt.Errorf("%w: YAML/JSON nesting exceeds %d", ErrResourceBudget, maxDocumentDepth)
	}
	if node.Alias != nil || node.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("YAML aliases are not allowed")
	}
	switch node.Kind {
	case yaml.MappingNode:
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" || keyNode.Value == "<<" {
				return nil, fmt.Errorf("object keys must be ordinary strings")
			}
			if _, duplicate := result[keyNode.Value]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", keyNode.Value)
			}
			value, err := yamlNodeValue(node.Content[index+1], depth+1)
			if err != nil {
				return nil, err
			}
			result[keyNode.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, len(node.Content))
		for index, child := range node.Content {
			value, err := yamlNodeValue(child, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = value
		}
		return result, nil
	case yaml.ScalarNode:
		var value any
		switch node.Tag {
		case "!!str":
			return node.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool", "!!int", "!!float":
			if err := node.Decode(&value); err != nil {
				return nil, err
			}
			return value, nil
		default:
			return nil, fmt.Errorf("unsupported YAML scalar tag %q", node.Tag)
		}
	default:
		return nil, fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

func sourceKey(artifact Artifact, file string) string {
	digest := sha256.Sum256([]byte(artifact.ExtensionID + "\x00" + artifact.PackageDigest + "\x00" + file))
	return "source_" + hex.EncodeToString(digest[:12])
}

func pathOnDisk(root, relative string) string {
	return filepath.Join(root, filepath.FromSlash(relative))
}

func validPolicyName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("._:@/-", char) {
			continue
		}
		return false
	}
	return true
}

func validSecurityPolicy(value string) bool {
	return value == SecurityPublic || value == SecurityAuthenticated ||
		value == SecurityHostInherited || value == SecurityPluginOwned
}

func readLimitedRegularFile(target string, limit int64, budget *resourceBudget) ([]byte, error) {
	before, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular: %s", target)
	}
	if before.Size() < 0 || before.Size() > limit {
		return nil, fmt.Errorf("%w: file %s size %d exceeds %d", ErrResourceBudget, target, before.Size(), limit)
	}
	if budget != nil {
		if err := budget.reserveDocument(before.Size()); err != nil {
			return nil, err
		}
	}
	handle, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("file changed while opening: %s", target)
	}
	body, err := io.ReadAll(io.LimitReader(handle, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit || int64(len(body)) != opened.Size() {
		return nil, fmt.Errorf("%w: file changed or exceeded read limit: %s", ErrResourceBudget, target)
	}
	after, err := handle.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != int64(len(body)) {
		return nil, fmt.Errorf("file changed while reading: %s", target)
	}
	return body, nil
}

func routeMethodKey(routeID, method string) string { return routeID + "\x00" + method }

func sortedDocumentPaths(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
