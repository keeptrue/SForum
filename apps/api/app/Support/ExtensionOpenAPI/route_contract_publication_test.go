package extensionopenapi

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteSchemaContractPublicationPublishesAndRestoresGeneratedMetadata(t *testing.T) {
	owner, err := NewRouteSchemaContractPublication([]CoreOperation{{
		RouteID: "core.route.reserved", Path: "/api/reserved/:id", Method: "GET", OperationID: "core.route.reserved",
	}})
	if err != nil {
		t.Fatal(err)
	}
	initialPublication := owner.PublicationSnapshot()
	initialContract := owner.ContractSnapshot()
	if initialPublication.AggregateRevision == "" || initialContract.AggregateRevision != initialPublication.AggregateRevision ||
		len(initialContract.Document) == 0 || len(initialContract.Artifacts) != 0 || len(initialContract.GeneratedClientOperations) != 0 {
		t.Fatalf("initial publication = %#v contract = %#v", initialPublication, initialContract)
	}

	options := defaultFixtureOptions("publication.contract")
	options.method = "POST"
	options.guard = extensionmanifest.GuardCorePermission
	options.permission = "publication.contract.manage"
	options.requestSchema = "publication.contract.request@1"
	options.rateLimit, options.idempotency = PolicyDisabled, PolicyDisabled
	fixture := buildFixture(t, options)
	fixture.Policies = HostRoutePolicies(fixture.Manifest)
	prepared, err := owner.Prepare([]Artifact{fixture})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.CatalogRevision() == "" || prepared.AggregateRevision() == "" || owner.Revision() != 0 {
		t.Fatalf("prepared publication changed live state")
	}
	published, err := owner.PublishPrepared(prepared, 0)
	if err != nil {
		t.Fatal(err)
	}
	contract := owner.ContractSnapshot()
	if published.Revision != 1 || contract.Revision != published.Revision ||
		contract.AggregateRevision != published.AggregateRevision || len(contract.Artifacts) != 1 || len(contract.Sources) != 1 {
		t.Fatalf("published = %#v contract = %#v", published, contract)
	}
	operations := contract.GeneratedClientOperations
	if len(operations) != 1 {
		t.Fatalf("generated operations = %#v", operations)
	}
	operation := operations[0]
	if operation.RouteID != fixture.Manifest.Routes[0].ID || operation.Permission != options.permission ||
		operation.RequestSchema != options.requestSchema || operation.ResponseSchema != options.responseSchema ||
		operation.RateLimit != PolicyDisabled || operation.Idempotency != PolicyDisabled ||
		operation.Security != SecurityAuthenticated || operation.PackageDigest != fixture.PackageDigest {
		t.Fatalf("generated operation = %#v", operation)
	}
	var document map[string]any
	if err := json.Unmarshal(contract.Document, &document); err != nil {
		t.Fatal(err)
	}
	publishedOperation := document["paths"].(map[string]any)[options.path].(map[string]any)["post"].(map[string]any)
	if publishedOperation[extPermission] != options.permission || publishedOperation[extRateLimit] != PolicyDisabled ||
		publishedOperation[extIdempotency] != PolicyDisabled || publishedOperation[extSecurityOwner] != SecurityAuthenticated {
		t.Fatalf("published operation = %#v", publishedOperation)
	}

	contract.Artifacts[0].PackageDigest = "mutated"
	contract.Sources[0].PackageDigest = "mutated"
	contract.GeneratedClientOperations[0].RouteID = "mutated"
	contract.Document[0] = '!'
	detached := owner.ContractSnapshot()
	if detached.Artifacts[0].PackageDigest != fixture.PackageDigest || detached.Sources[0].PackageDigest != fixture.PackageDigest ||
		detached.GeneratedClientOperations[0].RouteID != fixture.Manifest.Routes[0].ID || detached.Document[0] == '!' {
		t.Fatal("contract snapshot is mutable through a getter")
	}

	restored, err := owner.Restore(initialPublication, 1)
	if err != nil {
		t.Fatal(err)
	}
	restoredContract := owner.ContractSnapshot()
	if restored.Revision != 2 || restored.AggregateRevision != initialContract.AggregateRevision ||
		restoredContract.AggregateRevision != initialContract.AggregateRevision || len(restoredContract.Artifacts) != 0 ||
		len(restoredContract.Sources) != 0 || len(restoredContract.GeneratedClientOperations) != 0 {
		t.Fatalf("restored = %#v contract = %#v", restored, restoredContract)
	}
}

func TestRouteSchemaContractPublicationRejectsCollisionWithoutPartialPublish(t *testing.T) {
	core := CoreOperation{
		RouteID: "core.route.catalog", Path: "/api/catalog/:id", Method: "GET", OperationID: "core.route.catalog",
	}
	owner, err := NewRouteSchemaContractPublication([]CoreOperation{core})
	if err != nil {
		t.Fatal(err)
	}
	beforePublication := owner.PublicationSnapshot()
	beforeContract := owner.ContractSnapshot()
	fixture := buildFixture(t, defaultFixtureOptions("publication.collision"))
	fixture.Policies = HostRoutePolicies(fixture.Manifest)
	if _, err := owner.Publish([]Artifact{fixture}); !errors.Is(err, ErrCollision) {
		t.Fatalf("collision error = %v", err)
	}
	afterPublication := owner.PublicationSnapshot()
	afterContract := owner.ContractSnapshot()
	if afterPublication.Revision != beforePublication.Revision || afterPublication.CatalogRevision != beforePublication.CatalogRevision ||
		afterPublication.AggregateRevision != beforePublication.AggregateRevision ||
		afterContract.AggregateRevision != beforeContract.AggregateRevision || len(afterContract.Artifacts) != 0 || len(owner.Bindings()) != 0 {
		t.Fatalf("failed publication changed live snapshots: %#v %#v", afterPublication, afterContract)
	}
}

func TestSchemaOnlyPublicationDoesNotExposePolicyAggregate(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	if contract := owner.ContractSnapshot(); contract.AggregateRevision != "" || len(contract.Document) != 0 {
		t.Fatalf("schema-only contract snapshot = %#v", contract)
	}
}

func TestRouteSchemaContractPublicationIsRaceSafeAcrossReadersAndRestore(t *testing.T) {
	owner, err := NewRouteSchemaContractPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := owner.PublicationSnapshot()
	options := defaultFixtureOptions("publication.contract-race")
	options.rateLimit, options.idempotency = PolicyDisabled, PolicyDisabled
	fixture := buildFixture(t, options)
	fixture.Policies = HostRoutePolicies(fixture.Manifest)
	if _, err := owner.Publish([]Artifact{fixture}); err != nil {
		t.Fatal(err)
	}
	active := owner.PublicationSnapshot()

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				snapshot := owner.ContractSnapshot()
				if snapshot.AggregateRevision == "" || len(snapshot.Document) == 0 ||
					len(snapshot.Artifacts) != len(snapshot.GeneratedClientOperations) || len(snapshot.Artifacts) > 1 {
					t.Errorf("incoherent contract snapshot = %#v", snapshot)
					return
				}
				snapshot.Document[0] = '!'
				if len(snapshot.GeneratedClientOperations) > 0 {
					snapshot.GeneratedClientOperations[0].RouteID = "mutated"
				}
			}
		}()
	}
	for index := range 100 {
		candidate := active
		if index%2 == 0 {
			candidate = empty
		}
		if _, err := owner.Restore(candidate, owner.Revision()); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}
