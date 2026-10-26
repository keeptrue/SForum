package extensionopenapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteSchemaPublicationPreparesPublishesAndRestoresExactCatalog(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := owner.PublicationSnapshot()
	fixture := buildFixture(t, defaultFixtureOptions("schema.publication"))
	fixture.Policies = nil
	prepared, err := owner.Prepare([]Artifact{fixture})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.CatalogRevision() == "" || owner.Revision() != 0 || len(owner.Bindings()) != 0 {
		t.Fatalf("prepared publication changed live state")
	}

	fixture.Manifest.Routes[0].ResponseSchema = "mutated.after.prepare@1"
	published, err := owner.PublishPrepared(prepared, 0)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != 1 || len(published.Artifacts) != 1 || len(owner.Bindings()) != 1 {
		t.Fatalf("published=%#v bindings=%#v", published, owner.Bindings())
	}
	binding := owner.Bindings()[0]
	if binding.SchemaID == "mutated.after.prepare@1" {
		t.Fatal("caller mutation changed prepared catalog")
	}
	if err := validatePublishedRouteSchema(owner, fixture, binding); err != nil {
		t.Fatal(err)
	}

	restored, err := owner.Restore(empty, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 2 || len(restored.Artifacts) != 0 || len(owner.Bindings()) != 0 {
		t.Fatalf("restored=%#v bindings=%#v", restored, owner.Bindings())
	}
	if err := validatePublishedRouteSchema(owner, fixture, binding); !errors.Is(err, ErrRouteSchemaMissing) {
		t.Fatalf("removed schema error=%v", err)
	}
}

func TestRouteSchemaPublicationFencesStaleAndForeignWriters(t *testing.T) {
	first, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := buildFixture(t, defaultFixtureOptions("schema.publication-fence"))
	fixture.Policies = nil
	prepared, err := first.Prepare([]Artifact{fixture})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.PublishPrepared(prepared, 0); !errors.Is(err, ErrRouteSchemaPublicationInvalid) {
		t.Fatalf("foreign prepared error=%v", err)
	}
	if _, err := first.PublishPrepared(prepared, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := first.PublishPrepared(prepared, 0); !errors.Is(err, ErrRouteSchemaRevisionConflict) || first.Revision() != 1 {
		t.Fatalf("stale publish error=%v revision=%d", err, first.Revision())
	}
	if _, err := second.Restore(first.PublicationSnapshot(), 0); !errors.Is(err, ErrRouteSchemaPublicationInvalid) {
		t.Fatalf("foreign restore error=%v", err)
	}
}

func TestRouteSchemaPublicationRejectsPreparedCandidateAfterAnotherWriterWins(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	first := buildFixture(t, defaultFixtureOptions("schema.publication-first"))
	first.Policies = nil
	second := buildFixture(t, defaultFixtureOptions("schema.publication-second"))
	second.Policies = nil
	stale, err := owner.Prepare([]Artifact{first})
	if err != nil {
		t.Fatal(err)
	}
	winner, err := owner.Prepare([]Artifact{second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PublishPrepared(winner, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PublishPrepared(stale, owner.Revision()); !errors.Is(err, ErrRouteSchemaRevisionConflict) {
		t.Fatalf("stale prepared publish error=%v", err)
	}
	bindings := owner.Bindings()
	if len(bindings) != 1 || bindings[0].ExtensionID != second.ExtensionID {
		t.Fatalf("stale candidate replaced winner: %#v", bindings)
	}
}

func TestRouteSchemaPublicationIsRaceSafeAcrossValidationAndReplacement(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := buildFixture(t, defaultFixtureOptions("schema.publication-race"))
	fixture.Policies = nil
	emptySnapshot := owner.PublicationSnapshot()
	active, err := owner.Prepare([]Artifact{fixture})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PublishPrepared(active, 0); err != nil {
		t.Fatal(err)
	}
	activeSnapshot := owner.PublicationSnapshot()
	binding := owner.Bindings()[0]

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				err := validatePublishedRouteSchema(owner, fixture, binding)
				if err != nil && !errors.Is(err, ErrRouteSchemaMissing) {
					t.Errorf("validation error=%v", err)
					return
				}
			}
		}()
	}
	for index := range 100 {
		candidate := activeSnapshot
		if index%2 == 0 {
			candidate = emptySnapshot
		}
		if _, err := owner.Restore(candidate, owner.Revision()); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}

func validatePublishedRouteSchema(
	owner *RouteSchemaPublication,
	fixture Artifact,
	binding RouteSchemaBinding,
) error {
	artifact := routeSchemaFixtureArtifact(fixture)
	artifact.PackageDigest = binding.PackageDigest
	return owner.ValidateRouteSchema(
		context.Background(), artifact, string(binding.Direction), binding.RouteID, binding.Method,
		fixtureActualMethod(binding.Method), binding.ContractVersion, binding.Action, binding.SchemaID,
		binding.MediaType, 200, []byte(`{"id":"42"}`),
	)
}

func TestRouteSchemaPublicationSnapshotIsDetached(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := buildFixture(t, defaultFixtureOptions("schema.publication-copy"))
	fixture.Policies = nil
	if _, err := owner.Publish([]Artifact{fixture}); err != nil {
		t.Fatal(err)
	}
	snapshot := owner.PublicationSnapshot()
	snapshot.Artifacts[0].PackageDigest = strings.Repeat("f", 64)
	if owner.PublicationSnapshot().Artifacts[0].PackageDigest == snapshot.Artifacts[0].PackageDigest {
		t.Fatal("inspection mutation changed active publication")
	}
}

func TestRouteSchemaPublicationReplacesOneExactArtifactWithoutExposingOthers(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	first := buildFixture(t, defaultFixtureOptions("schema.replace-first"))
	first.Policies = nil
	secondOptions := defaultFixtureOptions("schema.replace-second")
	secondOptions.path, secondOptions.manifestPath = "/api/second/{id}", "/api/second/:id"
	second := buildFixture(t, secondOptions)
	second.Policies = nil
	if _, err := owner.Publish([]Artifact{first, second}); err != nil {
		t.Fatal(err)
	}

	replacement := buildFixture(t, defaultFixtureOptions("schema.replace-first"))
	replacement = rebuildFixtureManifest(t, replacement, func(manifest *extensionmanifest.Manifest) {
		manifest.Version = "2.0.0"
	})
	replacement.Version = replacement.Manifest.Version
	allowed := []PublishedRouteSchemaArtifact{
		{ExtensionID: first.ExtensionID, ExtensionVersion: first.Version, PackageDigest: first.PackageDigest},
		{ExtensionID: replacement.ExtensionID, ExtensionVersion: replacement.Version, PackageDigest: replacement.PackageDigest},
	}
	before := owner.PublicationSnapshot()
	if err := owner.ValidateExtensionReplacement(first.ExtensionID, &replacement, allowed); err != nil {
		t.Fatal(err)
	}
	if owner.Revision() != before.Revision {
		t.Fatal("replacement validation mutated live publication")
	}
	published, err := owner.ReplaceExtensionIfRevision(first.ExtensionID, &replacement, allowed, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if published.Revision != before.Revision+1 || len(published.Artifacts) != 2 {
		t.Fatalf("replacement snapshot = %#v", published)
	}
	artifacts := map[string]PublishedRouteSchemaArtifact{}
	for _, artifact := range published.Artifacts {
		artifacts[artifact.ExtensionID] = artifact
	}
	if artifacts[first.ExtensionID].ExtensionVersion != replacement.Version ||
		artifacts[second.ExtensionID].PackageDigest != second.PackageDigest {
		t.Fatalf("replacement artifacts = %#v", artifacts)
	}
	if _, err := owner.ReplaceExtensionIfRevision(first.ExtensionID, nil, allowed, published.Revision); err != nil {
		t.Fatal(err)
	}
	if snapshot := owner.PublicationSnapshot(); len(snapshot.Artifacts) != 1 || snapshot.Artifacts[0].ExtensionID != second.ExtensionID {
		t.Fatalf("removal snapshot = %#v", snapshot)
	}
}

func TestRouteSchemaPublicationRejectsForeignExactArtifactAndStaleRevision(t *testing.T) {
	owner, err := NewRouteSchemaPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := buildFixture(t, defaultFixtureOptions("schema.foreign"))
	foreign.Policies = nil
	if _, err := owner.Publish([]Artifact{foreign}); err != nil {
		t.Fatal(err)
	}
	allowed := []PublishedRouteSchemaArtifact{{
		ExtensionID: foreign.ExtensionID, ExtensionVersion: foreign.Version,
		PackageDigest: strings.Repeat("f", 64),
	}}
	if _, err := owner.ReplaceExtensionIfRevision(
		foreign.ExtensionID, nil, allowed, owner.Revision(),
	); !errors.Is(err, ErrRouteSchemaArtifactConflict) {
		t.Fatalf("foreign artifact error = %v", err)
	}
	if _, err := owner.ReplaceExtensionIfRevision(
		foreign.ExtensionID, nil, []PublishedRouteSchemaArtifact{{
			ExtensionID: foreign.ExtensionID, ExtensionVersion: foreign.Version, PackageDigest: foreign.PackageDigest,
		}}, owner.Revision()-1,
	); !errors.Is(err, ErrRouteSchemaRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	if len(owner.PublicationSnapshot().Artifacts) != 1 {
		t.Fatal("failed replacement changed live publication")
	}
}
