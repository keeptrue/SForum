package extensionopenapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestBuildForbidsPluginServersAtEveryLevel(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{"root", func(document string) string {
			return strings.Replace(document, "paths:\n", "servers: [{url: 'https://evil.example'}]\npaths:\n", 1)
		}},
		{"path", func(document string) string {
			return strings.Replace(document, "  /api/catalog/{id}:\n", "  /api/catalog/{id}:\n    servers: [{url: 'https://evil.example'}]\n", 1)
		}},
		{"operation", func(document string) string {
			return strings.Replace(document, "    get:\n", "    get:\n      servers: [{url: 'https://evil.example'}]\n", 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := defaultFixtureOptions("servers." + test.name)
			options.document = test.mutate(fixtureDocument(options))
			if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, options)}}); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBuildRejectsPluginSecurityDowngradeOrContradiction(t *testing.T) {
	authenticated := defaultFixtureOptions("security.authenticated")
	authenticated.guard = extensionmanifest.GuardCoreLogin
	authenticated.document = strings.Replace(fixtureDocument(authenticated), "    get:\n", "    get:\n      security: []\n", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, authenticated)}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("authenticated downgrade error = %v", err)
	}

	public := defaultFixtureOptions("security.public")
	public.document = strings.Replace(fixtureDocument(public), "    get:\n", "    get:\n      security: [{cookieAuth: []}]\n", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, public)}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("public contradiction error = %v", err)
	}

	rootSecurity := defaultFixtureOptions("security.root")
	rootSecurity.document = strings.Replace(fixtureDocument(rootSecurity), "paths:\n", "security: []\npaths:\n", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, rootSecurity)}}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("root security error = %v", err)
	}
}

func TestBuildGeneratesHostOwnedStandardSecurity(t *testing.T) {
	authenticated := defaultFixtureOptions("security.generated")
	authenticated.guard = extensionmanifest.GuardCorePermission
	authenticated.permission = "security.generated.manage"
	snapshot, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, authenticated)}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GeneratedClientOperations()[0].Security != SecurityAuthenticated {
		t.Fatalf("generated client security = %#v", snapshot.GeneratedClientOperations()[0])
	}
	var document map[string]any
	if err := json.Unmarshal(snapshot.Document(), &document); err != nil {
		t.Fatal(err)
	}
	operation := document["paths"].(map[string]any)["/api/catalog/{id}"].(map[string]any)["get"].(map[string]any)
	security := operation["security"].([]any)
	if len(security) != 2 || security[0].(map[string]any)["cookieAuth"] == nil || security[1].(map[string]any)["bearerAuth"] == nil {
		t.Fatalf("generated security = %#v", security)
	}
	components := document["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if len(components) != 2 {
		t.Fatalf("security schemes = %#v", components)
	}

	publicSnapshot, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, defaultFixtureOptions("security.generated-public"))}})
	if err != nil {
		t.Fatal(err)
	}
	var publicDocument map[string]any
	if err := json.Unmarshal(publicSnapshot.Document(), &publicDocument); err != nil {
		t.Fatal(err)
	}
	publicOperation := publicDocument["paths"].(map[string]any)["/api/catalog/{id}"].(map[string]any)["get"].(map[string]any)
	if publicSecurity, ok := publicOperation["security"].([]any); !ok || len(publicSecurity) != 0 {
		t.Fatalf("public security = %#v", publicOperation["security"])
	}
}
