package extensionsruntime

import (
	"encoding/json"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentCompositionInspectorRedactsRuntimeArtifacts(t *testing.T) {
	registry := NewComponentRegistry()
	for _, id := range []string{"inspector.alpha", "inspector.beta"} {
		extension := componentTestExtension(t, id, extensions.TypePlugin,
			componentTestContribution(id, "replace", extensionmanifest.ComponentActionReplace, 10,
				componentTestCoreTarget, componentTestCoreContract),
		)
		if err := registry.ReplaceRuntime(extension, "runtime-"+id); err != nil {
			t.Fatal(err)
		}
	}
	composition, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewComponentCompositionInspector(registry, composition).Inspect(10)
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].CandidateCount != 2 {
		t.Fatalf("redacted conflicts = %#v", snapshot.Conflicts)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"runtime-inspector", "packageDigest", "candidates", "steps", "error"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("inspection payload leaked %q: %s", forbidden, payload)
		}
	}
}
