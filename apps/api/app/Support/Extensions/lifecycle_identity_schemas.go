package extensionsruntime

import (
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func bindLifecycleIdentitySchemas(
	extension extensions.Extension,
	publication identityregistry.Publication,
) (identityregistry.Publication, error) {
	if extension.Manifest.Identity == nil {
		return publication, nil
	}
	loader := newLifecyclePackageSchemaLoader(extension)
	fieldBindings := make([]identityregistry.UserFieldSchemaBinding, 0, len(extension.Manifest.Identity.UserFields))
	operationBindings := make([]identityregistry.ProviderOperationSchemaBinding, 0)

	for _, field := range extension.Manifest.Identity.UserFields {
		if !lifecycleIdentityUserFieldRequiresExact(extension.Manifest.PackageFiles, field.Schema) {
			continue
		}
		schema, err := loader.Load(field.Schema)
		if err != nil || schema.WireReference == "" {
			return identityregistry.Publication{}, fmt.Errorf(
				"%w: identity user field %s Schema: %v",
				ErrLifecycleRegistryPublicationInvalid, field.ID, lifecycleIdentitySchemaError(err, schema.WireReference),
			)
		}
		fieldBindings = append(fieldBindings, identityregistry.UserFieldSchemaBinding{
			FieldID: field.ID, ContractVersion: field.ContractVersion,
			Artifact: publication.Artifact,
			Schema: identityregistry.JSONSchemaMaterial{
				Reference: field.Schema, WireReference: schema.WireReference,
				Digest: schema.Digest, Schema: schema.Body,
			},
		})
	}

	for _, provider := range extension.Manifest.Identity.Providers {
		for _, operation := range provider.Operations {
			input, err := loader.Load(operation.InputSchema)
			if err != nil || input.WireReference == "" {
				return identityregistry.Publication{}, fmt.Errorf(
					"%w: identity provider %s operation %s input Schema: %v",
					ErrLifecycleRegistryPublicationInvalid, provider.ID, operation.Name,
					lifecycleIdentitySchemaError(err, input.WireReference),
				)
			}
			output, err := loader.Load(operation.OutputSchema)
			if err != nil || output.WireReference == "" {
				return identityregistry.Publication{}, fmt.Errorf(
					"%w: identity provider %s operation %s output Schema: %v",
					ErrLifecycleRegistryPublicationInvalid, provider.ID, operation.Name,
					lifecycleIdentitySchemaError(err, output.WireReference),
				)
			}
			operationBindings = append(operationBindings, identityregistry.ProviderOperationSchemaBinding{
				ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
				Operation: operation.Name, Artifact: publication.Artifact,
				Input: identityregistry.JSONSchemaMaterial{
					Reference: operation.InputSchema, WireReference: input.WireReference,
					Digest: input.Digest, Schema: input.Body,
				},
				Output: identityregistry.JSONSchemaMaterial{
					Reference: operation.OutputSchema, WireReference: output.WireReference,
					Digest: output.Digest, Schema: output.Body,
				},
			})
		}
	}

	bound, err := identityregistry.BindJSONSchemas(publication, fieldBindings, operationBindings)
	if err != nil {
		return identityregistry.Publication{}, fmt.Errorf(
			"%w: bind exact Identity Schemas: %v", ErrLifecycleRegistryPublicationInvalid, err,
		)
	}
	return bound, nil
}

func lifecycleIdentityUserFieldRequiresExact(
	files []extensions.ManifestPackageFile,
	reference string,
) bool {
	// 兼容既有 Manifest 形状：只有 contract ref 且同包完全未声明该 Schema ID
	// 时保留 digestless catalog-only 语义。这不是按安装时间区分的 legacy adoption，
	// 且不能进入 Host 字段值读写；path 或任一同 ID packageFile 都是
	// exact opt-in，失败不得降级。
	schemaID, _, err := protocolV2SchemaRef(reference)
	if err != nil {
		return true
	}
	for _, file := range files {
		if strings.TrimSpace(file.ID) == schemaID {
			return true
		}
	}
	return false
}

func lifecycleIdentitySchemaError(err error, wireReference string) error {
	if err != nil {
		return err
	}
	if wireReference == "" {
		return fmt.Errorf("canonical wire reference is missing")
	}
	return nil
}
