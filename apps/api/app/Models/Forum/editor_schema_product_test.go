package forum

import (
	"strings"
	"testing"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
)

// 产品路径：Service.renderContent 经 EditorRegistrySchemaBridge 接纳插件节点。
func TestServiceRenderContentAdmitsEditorRegistryPluginNode(t *testing.T) {
	t.Parallel()
	registry := editorregistry.New()
	digest := strings.Repeat("ab", 32)
	moduleDigest := strings.Repeat("cd", 32)
	if _, err := registry.Publish(editorregistry.Publication{
		Artifact: editorregistry.Artifact{
			ExtensionID: "demo.editor", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1,
		},
		Editor: []editorregistry.Declaration{
			{
				ID: "demo.editor.node.vote", ContractVersion: "demo.editor.node.vote@1",
				Kind: editorregistry.KindNode, Schema: "demo.editor.vote@1", ExtensionName: "demoVote",
				L2Module: "frontend/editor/vote.mjs", L2Digest: moduleDigest,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	service := NewService(ServiceConfig{Store: nil}).WithEditorDocumentSchema(EditorRegistrySchemaBridge{Registry: registry})
	native := `{"type":"doc","content":[{"type":"demoVote"},{"type":"paragraph","content":[{"type":"text","text":"body"}]}]}`
	rendered, err := service.renderContent(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
	}, defaultExcerptRuneLimit, "topic")
	if err != nil {
		t.Fatalf("renderContent: %v", err)
	}
	if !strings.Contains(rendered.RawContent, `"type":"demoVote"`) {
		t.Fatalf("plugin node stripped: %s", rendered.RawContent)
	}
	if rendered.RenderVersion != RenderVersionEditorDocument {
		t.Fatalf("render version = %q", rendered.RenderVersion)
	}
}

func TestServiceRenderContentWithoutSchemaFallsBackPluginNode(t *testing.T) {
	t.Parallel()
	service := NewService(ServiceConfig{Store: nil})
	native := `{"type":"doc","content":[{"type":"demoVote"},{"type":"paragraph","content":[{"type":"text","text":"body"}]}]}`
	rendered, err := service.renderContent(ContentInput{
		RawContent:   native,
		SourceFormat: SourceFormatEditorDocument,
	}, defaultExcerptRuneLimit, "topic")
	if err != nil {
		t.Fatalf("renderContent: %v", err)
	}
	if strings.Contains(rendered.RawContent, `"type":"demoVote"`) {
		t.Fatalf("unregistered node must fallback, raw=%s", rendered.RawContent)
	}
	if !strings.Contains(rendered.PlainText, "body") {
		t.Fatalf("plain = %q", rendered.PlainText)
	}
}

func TestEditorRegistrySchemaBridgeNilIsCore(t *testing.T) {
	t.Parallel()
	schema := EditorRegistrySchemaBridge{}.EditorDocumentSchema()
	if _, ok := schema.Nodes["doc"]; !ok {
		t.Fatal("expected core schema")
	}
	// 与 CoreSchema 一致：无插件节点
	if _, ok := schema.Nodes["demoVote"]; ok {
		t.Fatal("nil registry must not invent plugin nodes")
	}
	_ = editordocument.CoreSchema()
}
