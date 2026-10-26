package extensionsruntime

import (
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// 回归：forum 写路径把 ContentInput / []string 直接塞进 hook payload，
// Protocol V2 必须能编码，否则 fail_closed filter 会以 extension.hook_failed 拒帖。
func TestProtocolV2DocumentNormalizesHostNativeTypes(t *testing.T) {
	attachments := []int64{1, 2}
	doc, err := protocolV2Document("sforum.content-policy.hook-input", "1", map[string]any{
		"actorUserId":  int64(42),
		"topicId":      int64(7),
		"categorySlug": "general",
		"tagSlugs":     []string{"news", "ops"},
		"title":        "hello",
		"content": forum.ContentInput{
			RawContent:    "<p>body</p>",
			SourceFormat:  "html",
			EditorType:    "tiptap",
			EditorVersion: "1",
			AttachmentIDs: &attachments,
		},
	})
	if err != nil {
		t.Fatalf("protocolV2Document: %v", err)
	}
	if doc.GetSchemaId() != "sforum.content-policy.hook-input" || doc.GetSchemaVersion() != "1" {
		t.Fatalf("schema identity: %#v", doc)
	}
	values := protocolV2Values(doc)
	if values["title"] != "hello" {
		t.Fatalf("title: %#v", values["title"])
	}
	// []string → []any
	tags, ok := values["tagSlugs"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "news" || tags[1] != "ops" {
		t.Fatalf("tagSlugs: %#v", values["tagSlugs"])
	}
	// 具名 struct → map
	content, ok := values["content"].(map[string]any)
	if !ok {
		t.Fatalf("content should be object, got %#v", values["content"])
	}
	if content["rawContent"] != "<p>body</p>" || content["sourceFormat"] != "html" {
		t.Fatalf("content fields: %#v", content)
	}
}

func TestProtocolV2DocumentNilPayload(t *testing.T) {
	doc, err := protocolV2Document("demo.schema", "1", nil)
	if err != nil {
		t.Fatalf("nil payload: %v", err)
	}
	if len(protocolV2Values(doc)) != 0 {
		t.Fatalf("expected empty map, got %#v", protocolV2Values(doc))
	}
}
