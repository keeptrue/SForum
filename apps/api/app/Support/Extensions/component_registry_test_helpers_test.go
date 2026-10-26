package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	componentTestCoreTarget   = "core.component.page.forum.home"
	componentTestCoreContract = "sforum.component.page.forum.home@1"
	componentTestPropsSchema  = `{"type":"object","required":["scope"],"properties":{"scope":{"type":"string"}},"additionalProperties":false}`
	componentTestResultSchema = `{"type":"object","required":["html"],"properties":{"html":{"type":"string"}},"additionalProperties":false}`
)

func componentTestExtension(
	t *testing.T,
	id string,
	extensionType string,
	components ...extensions.ManifestComponent,
) extensions.Extension {
	t.Helper()
	return componentTestExtensionWithSchemas(
		t, id, extensionType, componentTestPropsSchema, componentTestResultSchema, components...,
	)
}

func componentTestExtensionWithSchemas(
	t *testing.T,
	id string,
	extensionType string,
	propsSchema string,
	resultSchema string,
	components ...extensions.ManifestComponent,
) extensions.Extension {
	t.Helper()
	templateID := id + ".template.default"
	propsRef := id + ".schema.props@1"
	resultRef := id + ".schema.result@1"
	for index := range components {
		component := &components[index]
		if component.Action == extensionmanifest.ComponentActionHide {
			continue
		}
		if component.SSRTemplate == "" && component.L2Component == "" {
			component.SSRTemplate = templateID
		}
		if component.PropsSchema == "" {
			component.PropsSchema = propsRef
		}
		if componentActionNeedsResult(component.Action) && component.ResultSchema == "" {
			component.ResultSchema = resultRef
		}
	}
	extension := extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensionType, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64), PackagePath: t.TempDir(),
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensionType,
			Components: append([]extensions.ManifestComponent(nil), components...),
			Templates: []extensions.ManifestTemplate{{
				ID: templateID, ContractVersion: templateID + "@1", Action: "add",
				Path: "templates/default.html", Digest: strings.Repeat("1", 64),
				ViewModelSchema: propsRef,
			}},
		},
	}
	writeComponentTestSchema(t, &extension, propsRef, "schemas/props.json", propsSchema)
	writeComponentTestSchema(t, &extension, resultRef, "schemas/result.json", resultSchema)
	return extension
}

func writeComponentTestSchema(
	t *testing.T,
	extension *extensions.Extension,
	reference string,
	path string,
	body string,
) {
	t.Helper()
	fullPath := filepath.Join(extension.PackagePath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(body))
	id, version, found := strings.Cut(reference, "@")
	if !found {
		t.Fatalf("schema reference %q is not versioned", reference)
	}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: id, Kind: "schema", Path: path, Digest: hex.EncodeToString(digest[:]), Version: version,
	})
}

func componentTestContribution(
	extensionID string,
	suffix string,
	action string,
	priority int,
	targetID string,
	targetContract string,
) extensions.ManifestComponent {
	return extensions.ManifestComponent{
		ID:              extensionID + ".component." + suffix,
		ContractVersion: extensionID + ".component." + suffix + "@1",
		Action:          action, Priority: priority,
		TargetID: targetID, TargetContractVersion: targetContract,
	}
}

func componentTestFindContribution(
	t *testing.T,
	values []ComponentContribution,
	id string,
) ComponentContribution {
	t.Helper()
	for _, value := range values {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("component contribution %q is missing from %#v", id, values)
	return ComponentContribution{}
}

func componentTestFindTarget(
	t *testing.T,
	values []ComponentTarget,
	id string,
) ComponentTarget {
	t.Helper()
	for _, value := range values {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("component target %q is missing", id)
	return ComponentTarget{}
}
