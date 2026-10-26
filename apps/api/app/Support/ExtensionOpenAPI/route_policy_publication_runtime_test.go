package extensionopenapi

import (
	"errors"
	"strings"
	"testing"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteSchemaPublicationResolvesOnlyExactArtifactPolicy(t *testing.T) {
	owner, err := NewRouteSchemaContractPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	empty := owner.PublicationSnapshot()
	options := defaultFixtureOptions("policy.runtime")
	options.method = "POST"
	options.requestSchema = "policy.runtime.catalog.request@1"
	options.requiredIdempotency = true
	options.omitPolicyMetadata = true
	fixture := buildFixture(t, options)
	fixture.Policies = nil
	if _, err := owner.Publish([]Artifact{fixture}); err != nil {
		t.Fatal(err)
	}
	step := routes.RouteExecutionStep{
		RouteID: fixture.Manifest.Routes[0].ID, ContractVersion: fixture.Manifest.Routes[0].ContractVersion,
		Method: options.method, Provider: routes.Provider{Kind: routes.ProviderPlugin, Artifact: routes.PluginArtifact{
			ExtensionID: fixture.ExtensionID, ExtensionVersion: fixture.Version,
			PackageDigest: fixture.PackageDigest, RuntimeInstanceID: "runtime-1",
		}},
	}
	policy, err := owner.ResolveRouteExecutionPolicy(step)
	if err != nil || policy.RateLimit != PolicyRateLimitIPWrite ||
		policy.Idempotency != PolicyIdempotencyRequired24h || !policy.IdempotencyRequired {
		t.Fatalf("policy = %#v, %v", policy, err)
	}

	stale := step
	stale.Provider.Artifact.PackageDigest = strings.Repeat("f", 64)
	if _, err := owner.ResolveRouteExecutionPolicy(stale); !errors.Is(err, routes.ErrRoutePolicyNotFound) {
		t.Fatalf("stale artifact error = %v", err)
	}
	if _, err := owner.Restore(empty, owner.Revision()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ResolveRouteExecutionPolicy(step); !errors.Is(err, routes.ErrRoutePolicyNotFound) {
		t.Fatalf("restored policy error = %v", err)
	}
}

func TestPreparedRouteSchemaPublicationResolvesCandidatePolicyBeforePublish(t *testing.T) {
	owner, err := NewRouteSchemaContractPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	options := defaultFixtureOptions("policy.prepared")
	options.method = "POST"
	options.requestSchema = "policy.prepared.catalog.request@1"
	options.requiredIdempotency = true
	options.omitPolicyMetadata = true
	fixture := buildFixture(t, options)
	fixture.Policies = nil
	prepared, err := owner.PrepareExtensionReplacement(fixture.ExtensionID, &fixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	step := routes.RouteExecutionStep{
		RouteID: fixture.Manifest.Routes[0].ID, ContractVersion: fixture.Manifest.Routes[0].ContractVersion,
		Method: options.method, Provider: routes.Provider{Kind: routes.ProviderPlugin, Artifact: routes.PluginArtifact{
			ExtensionID: fixture.ExtensionID, ExtensionVersion: fixture.Version,
			PackageDigest: fixture.PackageDigest, RuntimeInstanceID: "runtime-prepared",
		}},
	}
	policy, err := prepared.ResolveRouteExecutionPolicy(step)
	if err != nil || policy.RateLimit != PolicyRateLimitIPWrite ||
		policy.Idempotency != PolicyIdempotencyRequired24h || !policy.IdempotencyRequired {
		t.Fatalf("prepared policy=%#v error=%v", policy, err)
	}
	if _, err := owner.ResolveRouteExecutionPolicy(step); !errors.Is(err, routes.ErrRoutePolicyNotFound) {
		t.Fatalf("unpublished live policy error=%v", err)
	}
	if _, err := owner.PublishPrepared(prepared, owner.Revision()); err != nil {
		t.Fatal(err)
	}
	if policy, err := owner.ResolveRouteExecutionPolicy(step); err != nil || !policy.IdempotencyRequired {
		t.Fatalf("published policy=%#v error=%v", policy, err)
	}
}
