package extensionopenapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestBuildDerivesHostPoliciesAndGeneratedClientMetadata(t *testing.T) {
	options := defaultFixtureOptions("policy.derived")
	options.method = "POST"
	options.requestSchema = "policy.derived.catalog.request@1"
	options.guard = extensionmanifest.GuardCorePermission
	options.permission = "policy.derived.create"
	options.requiredIdempotency = true
	options.omitPolicyMetadata = true
	fixture := buildFixture(t, options)
	fixture.Policies = nil

	snapshot, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	operations := snapshot.GeneratedClientOperations()
	if len(operations) != 1 {
		t.Fatalf("generated operations = %#v", operations)
	}
	operation := operations[0]
	if operation.Action != extensionmanifest.RouteActionAdd || operation.Mode != extensionmanifest.RouteModeHTTP ||
		operation.Permission != options.permission || operation.Security != SecurityAuthenticated ||
		operation.RateLimit != PolicyRateLimitIPWrite || operation.RateLimitScope != "client_ip" ||
		operation.Idempotency != PolicyIdempotencyRequired24h || !operation.IdempotencyRequired ||
		operation.IdempotencyHeader != idempotencyHeader || operation.IdempotencyKeyMaxLength != 128 ||
		operation.IdempotencyTTLSeconds != 24*60*60 {
		t.Fatalf("generated operation = %#v", operation)
	}

	var document map[string]any
	if err := json.Unmarshal(snapshot.Document(), &document); err != nil {
		t.Fatal(err)
	}
	published := document["paths"].(map[string]any)[options.path].(map[string]any)["post"].(map[string]any)
	if published[extRateLimit] != PolicyRateLimitIPWrite || published[extIdempotency] != PolicyIdempotencyRequired24h ||
		published[extPermission] != options.permission {
		t.Fatalf("published operation = %#v", published)
	}
}

func TestBuildDerivesDisabledIdempotencyForUndeclaredMutation(t *testing.T) {
	options := defaultFixtureOptions("policy.disabled")
	options.method = "DELETE"
	options.requestSchema = "policy.disabled.catalog.request@1"
	options.omitPolicyMetadata = true
	fixture := buildFixture(t, options)
	fixture.Policies = nil
	snapshot, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	operation := snapshot.GeneratedClientOperations()[0]
	if operation.RateLimit != PolicyRateLimitIPWrite || operation.Idempotency != PolicyDisabled ||
		operation.IdempotencyRequired || operation.IdempotencyHeader != "" || operation.IdempotencyTTLSeconds != 0 {
		t.Fatalf("generated operation = %#v", operation)
	}
}

func TestBuildRejectsRequiredIdempotencyOutsideBufferedMutation(t *testing.T) {
	tests := []struct {
		name   string
		method string
		mode   string
	}{
		{name: "safe method", method: "GET", mode: extensionmanifest.RouteModeHTTP},
		{name: "multipart stream", method: "POST", mode: extensionmanifest.RouteModeMultipart},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extensionID := "policy.reject." + strings.ReplaceAll(test.name, " ", "-")
			options := defaultFixtureOptions(extensionID)
			options.method, options.mode = test.method, test.mode
			if hostRateLimitedMethod(test.method) {
				options.requestSchema = extensionID + ".catalog.request@1"
			}
			options.requiredIdempotency = true
			options.omitPolicyMetadata = true
			fixture := buildFixture(t, options)
			fixture.Policies = nil
			if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBuildRejectsRequiredIdempotencyContractDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "oversized key schema",
			mutate: func(document string) string {
				return strings.Replace(document, "maxLength: 128", "maxLength: 129", 1)
			},
		},
		{
			name: "plugin policy mismatch",
			mutate: func(document string) string {
				return strings.Replace(
					document,
					"      parameters:\n",
					"      x-sforum-idempotency: disabled\n      parameters:\n",
					1,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extensionID := "policy.drift." + strings.ReplaceAll(test.name, " ", "-")
			options := defaultFixtureOptions(extensionID)
			options.method = "PATCH"
			options.requestSchema = extensionID + ".catalog.request@1"
			options.requiredIdempotency = true
			options.omitPolicyMetadata = true
			options.document = test.mutate(fixtureDocument(options))
			fixture := buildFixture(t, options)
			fixture.Policies = nil
			if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
