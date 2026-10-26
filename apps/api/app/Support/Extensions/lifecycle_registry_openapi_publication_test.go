package extensionsruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleRegistryBootPublishesExactOpenAPIAndSafeModeRestoresCoreOnly(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routeRegistry, RouteSchemas: routeSchemas,
	})
	extension := lifecycleRegistryTestExtension(t, "1.0.0", strings.Repeat("a", 64), 71, "/openapi-boot")
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	publication := routeSchemas.PublicationSnapshot()
	contract := routeSchemas.ContractSnapshot()
	assertLifecycleOpenAPIContract(t, extension, publication, contract)

	contract.Artifacts[0].PackageDigest = "mutated"
	contract.GeneratedClientOperations[0].RouteID = "mutated"
	if current := routeSchemas.ContractSnapshot(); current.Artifacts[0].PackageDigest != extension.PackageDigest ||
		current.GeneratedClientOperations[0].RouteID != extension.Manifest.Routes[0].ID {
		t.Fatal("lifecycle contract snapshot was mutable")
	}

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	coreOnly := routeSchemas.ContractSnapshot()
	if coreOnly.Revision <= contract.Revision || coreOnly.AggregateRevision == "" || len(coreOnly.Document) == 0 ||
		len(coreOnly.Artifacts) != 0 || len(coreOnly.Sources) != 0 || len(coreOnly.GeneratedClientOperations) != 0 {
		t.Fatalf("safe-mode contract = %#v", coreOnly)
	}
}

func assertLifecycleOpenAPIContract(
	t *testing.T,
	extension extensions.Extension,
	publication extensionopenapi.RouteSchemaPublicationSnapshot,
	contract extensionopenapi.PublishedContractSnapshot,
) {
	t.Helper()
	if publication.AggregateRevision == "" || contract.AggregateRevision != publication.AggregateRevision ||
		contract.Revision != publication.Revision || len(contract.Artifacts) != 1 || len(contract.Sources) != 1 ||
		contract.Artifacts[0].ExtensionID != extension.ID || contract.Artifacts[0].PackageDigest != extension.PackageDigest {
		t.Fatalf("publication = %#v contract = %#v", publication, contract)
	}
	operations := contract.GeneratedClientOperations
	if len(operations) != 1 {
		t.Fatalf("generated operations = %#v", operations)
	}
	operation := operations[0]
	if operation.RouteID != extension.Manifest.Routes[0].ID || operation.ExtensionID != extension.ID ||
		operation.ExtensionVersion != extension.Version || operation.PackageDigest != extension.PackageDigest ||
		operation.RateLimit != extensionopenapi.PolicyDisabled || operation.Idempotency != extensionopenapi.PolicyDisabled ||
		operation.Security != extensionopenapi.SecurityPublic {
		t.Fatalf("generated operation = %#v", operation)
	}
	var document map[string]any
	if err := json.Unmarshal(contract.Document, &document); err != nil {
		t.Fatal(err)
	}
	published := document["paths"].(map[string]any)["/openapi-boot/api"].(map[string]any)["get"].(map[string]any)
	if published["x-sforum-rate-limit"] != extensionopenapi.PolicyDisabled ||
		published["x-sforum-idempotency"] != extensionopenapi.PolicyDisabled {
		t.Fatalf("published operation = %#v", published)
	}
}
