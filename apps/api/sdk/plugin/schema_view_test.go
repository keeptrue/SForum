package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// view=schema 公开页的预检契约：作者（CLI）与运行时激活共用
// PreflightExactTemplateRuntime，因此这里同时钉住「精确制品」与「有界描述符」两条边界。
func TestPreflightExactTemplateRuntimeAcceptsBoundedSchemaViewPage(t *testing.T) {
	root := writeSchemaViewPackage(t, schemaViewPackageOptions{})
	manifest, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("LoadPackage: %v", err)
	}
	if err := PreflightExactTemplateRuntime(root, manifest); err != nil {
		t.Fatalf("bounded schema view page was rejected: %v", err)
	}
}

func TestPreflightExactTemplateRuntimeRejectsSchemaViewViolations(t *testing.T) {
	cases := []struct {
		name    string
		options schemaViewPackageOptions
		want    string
	}{
		{
			name:    "descriptor is not a declared schema file",
			options: schemaViewPackageOptions{dropDescriptorDeclaration: true},
			want:    "kind=schema",
		},
		{
			name:    "descriptor violates the declared contract",
			options: schemaViewPackageOptions{descriptorBody: `{"kind":42}`},
			want:    "does not satisfy its contract",
		},
		{
			name:    "descriptor kind is outside the bounded set",
			options: schemaViewPackageOptions{descriptorBody: `{"kind":"dashboard"}`},
			want:    "not one of list|detail|form|notice",
		},
		{
			name: "too many fields",
			options: schemaViewPackageOptions{
				descriptorBody: manyFieldsDescriptor(schemaViewMaximumFields + 1),
			},
			want: "fields exceed",
		},
		{
			name: "field type is unsupported",
			options: schemaViewPackageOptions{
				descriptorBody: `{"kind":"form","fields":[{"key":"a","label":"A","type":"richtext"}]}`,
			},
			want: "unsupported type",
		},
		{
			name:    "contract reference is malformed",
			options: schemaViewPackageOptions{contractRef: "demo.theme.schema.page"},
			want:    "<schemaId>@<version>",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := writeSchemaViewPackage(t, testCase.options)
			manifest, err := extensionmanifest.LoadPackage(root)
			if err != nil {
				t.Fatalf("LoadPackage: %v", err)
			}
			preflightErr := PreflightExactTemplateRuntime(root, manifest)
			if preflightErr == nil {
				t.Fatal("violating schema view page was accepted")
			}
			if !strings.Contains(preflightErr.Error(), testCase.want) {
				t.Fatalf("error %q does not mention %q", preflightErr, testCase.want)
			}
		})
	}
}

type schemaViewPackageOptions struct {
	descriptorBody            string
	dropDescriptorDeclaration bool
	contractRef               string
}

const schemaViewDescriptorBody = `{
  "kind": "list",
  "title": "Recent orders",
  "tone": "neutral",
  "items": [{"label": "Order 1", "value": "paid"}]
}`

// 默认契约只约束 kind 的形状，让「有界规则」的失败用例能走到有界校验而不是先撞契约。
const schemaViewContractBody = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["kind"],
  "properties": {
    "kind": {"type": "string"},
    "title": {"type": "string"},
    "tone": {"type": "string"},
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["label"],
        "properties": {"label": {"type": "string"}, "value": {"type": "string"}}
      }
    }
  },
  "additionalProperties": true
}`

func writeSchemaViewPackage(t *testing.T, options schemaViewPackageOptions) string {
	t.Helper()
	root := t.TempDir()

	descriptorBody := options.descriptorBody
	if descriptorBody == "" {
		descriptorBody = schemaViewDescriptorBody
	}
	descriptorDigest := sha256Hex(descriptorBody)
	contractDigest := sha256Hex(schemaViewContractBody)
	writePackageFile(t, root, "schemas/page-list.json", descriptorBody)
	writePackageFile(t, root, "schemas/page-list-contract.json", schemaViewContractBody)
	writePackageFile(t, root, "assets/theme.css", `:root { --sf-probe: 1; }`)
	cssDigest := sha256Hex(`:root { --sf-probe: 1; }`)

	packageFiles := []map[string]any{
		{"id": "demo.theme.file.css", "kind": "asset", "path": "assets/theme.css", "digest": cssDigest},
		{"id": "demo.theme.file.page.list", "kind": "schema", "version": "1",
			"path": "schemas/page-list.json", "digest": descriptorDigest},
		{"id": "demo.theme.schema.page.list", "kind": "schema", "version": "1",
			"path": "schemas/page-list-contract.json", "digest": contractDigest},
	}
	if options.dropDescriptorDeclaration {
		packageFiles = packageFiles[:1]
		packageFiles = append(packageFiles, map[string]any{
			"id": "demo.theme.schema.page.list", "kind": "schema", "version": "1",
			"path": "schemas/page-list-contract.json", "digest": contractDigest,
		})
	}

	contractRef := options.contractRef
	if contractRef == "" {
		contractRef = "demo.theme.schema.page.list@1"
	}
	themeJSON, err := json.Marshal(map[string]any{
		"pages": []map[string]any{{
			"id": "demo.theme.page.orders", "action": "add", "path": "/orders",
			"layout": "site", "view": "schema",
			"schema": map[string]string{
				"descriptor": "demo.theme.file.page.list", "contract": contractRef,
			},
		}},
		"skin": map[string]any{"css": []string{"assets/theme.css"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writePackageFile(t, root, "theme.json", string(themeJSON))

	manifest := map[string]any{
		"manifestVersion": 3, "id": "demo.theme", "name": "Demo Theme",
		"description": "V3 theme package for schema view preflight.",
		"url":         "https://example.com/demo-theme",
		"author":      map[string]any{"name": "Demo"},
		"version":     "1.0.0", "type": "theme", "sforumVersion": ">=0.1.0",
		"packageFiles": packageFiles,
		"assets": []map[string]any{{
			"handle": "demo.theme.asset.theme", "contractVersion": "demo.theme.asset.theme@1",
			"type": "style", "path": "assets/theme.css", "digest": cssDigest,
		}},
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writePackageFile(t, root, "sforum.extension.json", string(manifestBody))
	return root
}

func manyFieldsDescriptor(count int) string {
	fields := make([]map[string]string, 0, count)
	for index := 0; index < count; index++ {
		fields = append(fields, map[string]string{
			"key":   "field" + string(rune('a'+index%26)) + string(rune('0'+index/26)),
			"label": "Field",
			"type":  "text",
		})
	}
	body, _ := json.Marshal(map[string]any{"kind": "form", "fields": fields})
	return string(body)
}
