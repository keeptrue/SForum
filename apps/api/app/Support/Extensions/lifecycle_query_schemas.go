package extensionsruntime

import (
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func bindLifecycleQuerySchemas(
	extension extensions.Extension,
	publication queryregistry.Publication,
) (queryregistry.Publication, error) {
	loader := newLifecyclePackageSchemaLoader(extension)
	bindings := make([]queryregistry.JSONResultSchemaBinding, 0, len(extension.Manifest.Queries))
	for _, query := range extension.Manifest.Queries {
		// handlerless legacy Query 仅 inspect/plan；不要求 package Schema 文件。
		if strings.TrimSpace(query.Handler) == "" {
			continue
		}
		schema, err := loader.Load(query.ResultSchema)
		if err != nil {
			return queryregistry.Publication{}, fmt.Errorf("%w: query %s result Schema: %v",
				ErrLifecycleRegistryPublicationInvalid, query.ID, err)
		}
		bindings = append(bindings, queryregistry.JSONResultSchemaBinding{
			QueryID: query.ID, ContractVersion: query.ContractVersion,
			PlanVersion: query.PlanVersion, ResultSchema: query.ResultSchema,
			Artifact: publication.Artifact, SchemaDigest: schema.Digest, Schema: schema.Body,
		})
	}
	bound, err := queryregistry.BindResultSchemas(publication, bindings)
	if err != nil {
		return queryregistry.Publication{}, fmt.Errorf("%w: bind exact query result Schemas: %v",
			ErrLifecycleRegistryPublicationInvalid, err)
	}
	return bound, nil
}
