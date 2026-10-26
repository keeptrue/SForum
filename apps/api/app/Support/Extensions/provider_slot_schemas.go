package extensionsruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const providerSchemaMaximumBytes = 1 << 20

type providerDocumentValidator interface {
	Validate(any) error
}

func compileProviderSlotSchemas(
	extension extensions.Extension,
	declaration extensions.ManifestProvider,
) (providerDocumentValidator, providerDocumentValidator, string, string, error) {
	// Registry unit fixtures may omit package material. The production broker
	// refuses those snapshots through HasCompiledSchemas.
	if strings.TrimSpace(extension.PackagePath) == "" {
		return nil, nil, "", "", nil
	}
	request, requestDigest, err := compileExactProviderSchema(extension, declaration.RequestSchema)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("request: %w", err)
	}
	response, responseDigest, err := compileExactProviderSchema(extension, declaration.ResponseSchema)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("response: %w", err)
	}
	return request, response, requestDigest, responseDigest, nil
}

func compileExactProviderSchema(extension extensions.Extension, reference string) (providerDocumentValidator, string, error) {
	schemaID, schemaVersion, err := protocolV2SchemaRef(reference)
	if err != nil {
		return nil, "", err
	}
	var declared *extensions.ManifestPackageFile
	for index := range extension.Manifest.PackageFiles {
		file := &extension.Manifest.PackageFiles[index]
		if file.Kind == "schema" && file.ID == schemaID && file.Version == schemaVersion {
			declared = file
			break
		}
	}
	if declared == nil || strings.TrimSpace(declared.Path) == "" || len(declared.Digest) != sha256.Size*2 {
		return nil, "", errors.New("exact package schema declaration is missing")
	}
	path, ok := extensions.InstalledFilePathForRuntime(extension, declared.Path)
	if !ok {
		return nil, "", errors.New("schema path escapes exact package")
	}
	realRoot, err := filepath.EvalSymlinks(extensions.PackageContentRoot(extension))
	if err != nil {
		return nil, "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	if realPath == realRoot || !strings.HasPrefix(realPath, realRoot+string(os.PathSeparator)) {
		return nil, "", errors.New("schema symlink escapes exact package")
	}
	body, err := os.ReadFile(realPath)
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 || len(body) > providerSchemaMaximumBytes {
		return nil, "", errors.New("schema exceeds Host bounds")
	}
	digest := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), declared.Digest) {
		return nil, "", errors.New("schema digest does not match exact package")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, "", err
	}
	if err := rejectExternalProviderSchemaRefs(document, 0); err != nil {
		return nil, "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("schema contains trailing JSON")
	}
	resource := "https://sforum.invalid/provider-schema/" + declared.Digest + ".json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, "", err
	}
	compiled, err := compiler.Compile(resource)
	return compiled, declared.Digest, err
}

func providerSchemaDigest(extension extensions.Extension, reference string) (string, bool) {
	schemaID, schemaVersion, err := protocolV2SchemaRef(reference)
	if err != nil {
		return "", false
	}
	for _, file := range extension.Manifest.PackageFiles {
		if file.Kind == "schema" && file.ID == schemaID && file.Version == schemaVersion && len(file.Digest) == sha256.Size*2 {
			return strings.ToLower(file.Digest), true
		}
	}
	return "", false
}

func rejectExternalProviderSchemaRefs(value any, depth int) error {
	if depth > 64 {
		return errors.New("schema nesting exceeds Host bounds")
	}
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#") {
					return errors.New("provider schema external references are not allowed")
				}
			}
			if err := rejectExternalProviderSchemaRefs(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range item {
			if err := rejectExternalProviderSchemaRefs(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *VersionedProviderSlotRegistry) HasCompiledSchemas(id, contractVersion string) bool {
	state := r.load()
	contract, ok := state.contractsByID[strings.TrimSpace(id)]
	if !ok {
		if target := state.contractBySlot[strings.TrimSpace(id)]; target != "" {
			contract, ok = state.contractsByID[target]
		}
	}
	return ok && contract.ContractVersion == strings.TrimSpace(contractVersion) &&
		contract.requestValidator != nil && contract.responseValidator != nil
}

func (r *VersionedProviderSlotRegistry) HasContract(id, contractVersion string) bool {
	state := r.load()
	contract, ok := state.contractsByID[strings.TrimSpace(id)]
	if !ok {
		if target := state.contractBySlot[strings.TrimSpace(id)]; target != "" {
			contract, ok = state.contractsByID[target]
		}
	}
	return ok && contract.ContractVersion == strings.TrimSpace(contractVersion)
}

func (r *VersionedProviderSlotRegistry) ValidateDocument(contractID, schema string, document map[string]any) error {
	state := r.load()
	contract, ok := state.contractsByID[strings.TrimSpace(contractID)]
	if !ok {
		return ErrProviderSlotNotFound
	}
	var validator providerDocumentValidator
	switch strings.TrimSpace(schema) {
	case contract.RequestSchema:
		validator = contract.requestValidator
	case contract.ResponseSchema:
		validator = contract.responseValidator
	default:
		return ErrProviderSlotInvalid
	}
	if validator == nil {
		return nil
	}
	if err := validator.Validate(document); err != nil {
		return fmt.Errorf("%w: %v", ErrProviderSlotInvalid, err)
	}
	return nil
}
