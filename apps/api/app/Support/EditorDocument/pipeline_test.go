package editordocument

import (
	"encoding/json"
	"strings"
	"testing"

	contentlinks "github.com/keeptrue/sforum/apps/api/app/Support/ContentLinks"
)

func TestAcceptNativeJSONRoundTripAndSanitizerCorpus(t *testing.T) {
	t.Parallel()
	native := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "Hello ",
						"marks": []any{
							map[string]any{"type": "bold"},
						},
					},
					map[string]any{
						"type": "text",
						"text": "world",
						"marks": []any{
							map[string]any{
								"type":  "link",
								"attrs": map[string]any{"href": "javascript:alert(1)"},
							},
						},
					},
					map[string]any{
						"type": "text",
						"text": " ",
					},
					map[string]any{
						"type": "text",
						"text": "safe",
						"marks": []any{
							map[string]any{
								"type":  "link",
								"attrs": map[string]any{"href": "https://example.com/a"},
							},
						},
					},
				},
			},
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "sforumEmoji",
						"attrs": map[string]any{
							"name": "sparkles", "label": "灵感", "native": "✨",
						},
					},
				},
			},
			map[string]any{
				"type": "evilScript",
				"content": []any{
					map[string]any{"type": "text", "text": "xss"},
				},
			},
		},
	}
	body, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := Accept(Input{NativeJSON: body, Schema: CoreSchema()})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.StorageVersion != StorageVersion || accepted.ContentHash == "" {
		t.Fatalf("accepted meta = %#v", accepted)
	}
	if !strings.Contains(accepted.HTMLSanitized, "<strong>Hello </strong>") {
		t.Fatalf("html missing bold: %s", accepted.HTMLSanitized)
	}
	if strings.Contains(accepted.HTMLSanitized, "javascript:") ||
		strings.Contains(accepted.HTMLSanitized, "<script") {
		t.Fatalf("html retained unsafe content: %s", accepted.HTMLSanitized)
	}
	// 站外链接被改写成站内中转页（Support/ContentLinks），目标地址以 base64url 承载。
	if !strings.Contains(accepted.HTMLSanitized, contentlinks.InterstitialHref("https://example.com/a")) {
		t.Fatalf("html missing wrapped safe link: %s", accepted.HTMLSanitized)
	}
	if !strings.Contains(accepted.HTMLSanitized, `target="_blank"`) {
		t.Fatalf("html missing new-tab target: %s", accepted.HTMLSanitized)
	}
	if !strings.Contains(accepted.HTMLSanitized, "data-sforum-emoji") {
		t.Fatalf("html missing emoji: %s", accepted.HTMLSanitized)
	}
	if !strings.Contains(accepted.PlainText, "Hello") || !strings.Contains(accepted.SearchText, "emoji:sparkles") {
		t.Fatalf("plain/search = %q / %q", accepted.PlainText, accepted.SearchText)
	}
	if len(accepted.Fallbacks) != 1 || accepted.Fallbacks[0] != "evilScript" {
		t.Fatalf("fallbacks = %#v", accepted.Fallbacks)
	}
	if !strings.Contains(accepted.Markdown, "**Hello **") && !strings.Contains(accepted.Markdown, "**Hello") {
		// bold export may glue spaces; ensure Hello present
		if !strings.Contains(accepted.Markdown, "Hello") {
			t.Fatalf("markdown = %q", accepted.Markdown)
		}
	}
}

func TestAcceptOrderedListStartPreservedInSanitizedHTML(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name               string
		start              any
		expectedStart      string
		expectedNormalized int
		hasStart           bool
	}{
		{name: "positive", start: 3, expectedStart: ` start="3"`, expectedNormalized: 3, hasStart: true},
		{name: "default", start: 1},
		{name: "zero", start: 0, expectedStart: ` start="0"`, hasStart: true},
		{name: "negative", start: -4, expectedStart: ` start="-4"`, expectedNormalized: -4, hasStart: true},
		{name: "fractional", start: 3.5},
		{name: "string", start: "7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, _ := json.Marshal(map[string]any{
				"type": "doc",
				"content": []any{
					map[string]any{
						"type":  "orderedList",
						"attrs": map[string]any{"start": test.start},
						"content": []any{
							map[string]any{
								"type": "listItem",
								"content": []any{
									map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "item"}}},
								},
							},
						},
					},
				},
			})
			accepted, err := Accept(Input{NativeJSON: body, Schema: CoreSchema()})
			if err != nil {
				t.Fatalf("Accept(start=%v): %v", test.start, err)
			}
			if !strings.Contains(accepted.HTMLSanitized, `<li><p>item</p></li>`) {
				t.Fatalf("html lost list item: %s", accepted.HTMLSanitized)
			}
			if test.expectedStart == "" {
				if strings.Contains(accepted.HTMLSanitized, "start=") {
					t.Fatalf("start=%v must normalize to the default: %s", test.start, accepted.HTMLSanitized)
				}
			} else if !strings.Contains(accepted.HTMLSanitized, `<ol`+test.expectedStart+`>`) {
				t.Fatalf("start=%v was not preserved: %s", test.start, accepted.HTMLSanitized)
			}
			actualStart, exists := accepted.Native.Content[0].Attrs["start"]
			if !test.hasStart && exists {
				t.Fatalf("start=%v must be omitted from normalized native JSON: %#v", test.start, actualStart)
			}
			if test.hasStart && actualStart != test.expectedNormalized {
				t.Fatalf("normalized start = %#v, want %d", actualStart, test.expectedNormalized)
			}
		})
	}
}

func TestAcceptNestedListsKeepStructure(t *testing.T) {
	t.Parallel()
	native := []byte(`{"type":"doc","content":[
		{"type":"bulletList","content":[
			{"type":"listItem","content":[
				{"type":"paragraph","content":[{"type":"text","text":"alpha"}]},
				{"type":"orderedList","attrs":{"start":2},"content":[
					{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"beta"}]}]}
				]}
			]}
		]},
		{"type":"orderedList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"gamma"}]}]}
		]}
	]}`)
	accepted, err := Accept(Input{NativeJSON: native, Schema: CoreSchema()})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	for _, fragment := range []string{
		"<ul>", "<ol start=\"2\">", "<li><p>alpha</p><ol start=\"2\">", "<li><p>beta</p></li>", "<li><p>gamma</p></li>", "</ol>", "</ul>",
	} {
		if !strings.Contains(accepted.HTMLSanitized, fragment) {
			t.Fatalf("html missing %q: %s", fragment, accepted.HTMLSanitized)
		}
	}
}

func TestAcceptRejectsEmptyAndOversized(t *testing.T) {
	t.Parallel()
	if _, err := Accept(Input{}); err == nil {
		t.Fatal("expected empty reject")
	}
	huge := make([]byte, maxDocumentBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if _, err := Accept(Input{NativeJSON: huge}); err == nil {
		t.Fatal("expected oversized reject")
	}
}

func TestAcceptMarkdownCompatibilityPath(t *testing.T) {
	t.Parallel()
	accepted, err := Accept(Input{Markdown: "line one\n\nline two", Schema: CoreSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accepted.HTMLSanitized, "line one") || !strings.Contains(accepted.PlainText, "line two") {
		t.Fatalf("accepted = %#v", accepted)
	}
}

func TestAcceptNormalizesImagePresentationAndRendersOriginalViewerLink(t *testing.T) {
	t.Parallel()
	native := []byte(`{"type":"doc","content":[{"type":"image","attrs":{"src":"/media/attachments/0123456789abcdef0123456789abcdef","alt":"long screenshot","attachmentId":42,"attachmentPublicId":"0123456789abcdef0123456789abcdef","width":1200,"height":3200,"displaySize":"wide"}}]}`)
	accepted, err := Accept(Input{NativeJSON: native, Schema: CoreSchema()})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	for _, fragment := range []string{
		`href="/media/attachments/0123456789abcdef0123456789abcdef/original"`,
		`data-sforum-image-viewer="1"`,
		`data-sforum-image-size="wide"`,
		`width="1200" height="3200"`,
		`data-sforum-image-long="1"`,
	} {
		if !strings.Contains(accepted.HTMLSanitized, fragment) {
			t.Fatalf("html missing %q: %s", fragment, accepted.HTMLSanitized)
		}
	}
}

func TestAcceptFallsBackToStandardImagePresentationAndDropsPartialDimensions(t *testing.T) {
	t.Parallel()
	native := []byte(`{"type":"doc","content":[{"type":"image","attrs":{"src":"https://example.com/image.png","width":1200,"displaySize":"oversized"}}]}`)
	accepted, err := Accept(Input{NativeJSON: native, Schema: CoreSchema()})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	image := accepted.Native.Content[0]
	if image.Attrs["displaySize"] != "standard" {
		t.Fatalf("displaySize = %#v", image.Attrs["displaySize"])
	}
	if _, ok := image.Attrs["width"]; ok {
		t.Fatalf("partial width retained: %#v", image.Attrs)
	}
	if strings.Contains(accepted.HTMLSanitized, ` width=`) || strings.Contains(accepted.HTMLSanitized, ` height=`) {
		t.Fatalf("partial dimensions rendered: %s", accepted.HTMLSanitized)
	}
}

func TestMigrateStorageReacceptsUnderCurrentSchema(t *testing.T) {
	t.Parallel()
	body, _ := json.Marshal(map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "stable"},
				},
			},
		},
	})
	accepted, err := Accept(Input{NativeJSON: body, Schema: CoreSchema()})
	if err != nil {
		t.Fatal(err)
	}
	// Pretend older storage version.
	accepted.StorageVersion = "sforum.editor-document@0"
	migrated, err := MigrateStorage(accepted, StorageVersion, CoreSchema())
	if err != nil {
		t.Fatal(err)
	}
	if migrated.StorageVersion != StorageVersion || !strings.Contains(migrated.PlainText, "stable") {
		t.Fatalf("migrated = %#v", migrated)
	}
	if _, err := MigrateStorage(accepted, "sforum.editor-document@99", CoreSchema()); err != ErrStorageVersion {
		t.Fatalf("future version err = %v", err)
	}
}

func TestSchemaFromEditorNamesMergesWithoutOverridingCore(t *testing.T) {
	t.Parallel()
	schema := SchemaFromEditorNames([]string{"demoVote", "paragraph"}, []string{"demoHighlight"})
	if _, ok := schema.Nodes["demoVote"]; !ok {
		t.Fatal("missing plugin node")
	}
	// Core paragraph must remain non-atom.
	if schema.Nodes["paragraph"].Atom {
		t.Fatal("plugin must not override core paragraph")
	}
	if _, ok := schema.Marks["demoHighlight"]; !ok {
		t.Fatal("missing plugin mark")
	}
}

func TestOrderedPipelineRunsStableStageSequence(t *testing.T) {
	t.Parallel()
	stages := OrderedStages()
	if len(stages) != 8 || stages[0] != StageParse || stages[len(stages)-1] != StageSEO {
		t.Fatalf("stages = %#v", stages)
	}
	body, _ := json.Marshal(map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "pipeline"},
				},
			},
		},
	})
	accepted, results, err := RunOrderedPipeline(Input{NativeJSON: body, Schema: CoreSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(accepted.PlainText, "pipeline") {
		t.Fatalf("accepted = %#v", accepted)
	}
	if len(results) != 8 {
		t.Fatalf("results = %#v", results)
	}
	for index, stage := range OrderedStages() {
		if results[index].Stage != stage || !results[index].OK {
			t.Fatalf("result[%d] = %#v", index, results[index])
		}
	}
}

func TestDisabledPluginNodeFallsBackWithoutLosingSurroundingText(t *testing.T) {
	t.Parallel()
	// First accept with plugin node admitted.
	withPlugin := SchemaFromEditorNames([]string{"demoVote"}, nil)
	body, _ := json.Marshal(map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "before "},
					map[string]any{"type": "demoVote", "attrs": map[string]any{"id": "1"}},
					map[string]any{"type": "text", "text": " after"},
				},
			},
		},
	})
	accepted, err := Accept(Input{NativeJSON: body, Schema: withPlugin})
	if err != nil {
		t.Fatal(err)
	}
	// Re-accept stored native under core-only schema (plugin disabled).
	native, _ := json.Marshal(accepted.Native)
	// Force original plugin node back into document to simulate stored history.
	native = []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"before "},{"type":"demoVote","attrs":{"id":"1"}},{"type":"text","text":" after"}]}]}`)
	disabled, err := Accept(Input{NativeJSON: native, Schema: CoreSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(disabled.PlainText, "before") || !strings.Contains(disabled.PlainText, "after") {
		t.Fatalf("lost surrounding text: %q", disabled.PlainText)
	}
	if len(disabled.Fallbacks) != 1 || disabled.Fallbacks[0] != "demoVote" {
		t.Fatalf("fallbacks = %#v", disabled.Fallbacks)
	}
}
