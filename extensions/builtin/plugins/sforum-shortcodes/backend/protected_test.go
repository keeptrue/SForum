package main

import (
	"encoding/json"
	"strings"
	"testing"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func TestRenderProtectedRequiresAllowedFragmentAndReturnsTypedSegments(t *testing.T) {
	plugin := &shortcodePlugin{}
	fragment := json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"accepted secret","marks":[{"type":"bold"}]}]}]}`)
	value, _ := json.Marshal(map[string]any{"decision": "allowed", "fragment": json.RawMessage(fragment)})
	result, err := plugin.renderProtected(&pluginv2.ContentCall{
		Target:   pluginv2.ContentDeclaration{ID: shortcodeLoginID, ContractVersion: shortcodeLoginVersion},
		Document: pluginv2.ContentEditorDocument{SchemaVersion: pluginv2.ContentEditorDocumentSchema, Value: value},
		Scope:    "protected", Locale: "en-US",
	})
	if err != nil || result.Render == nil || len(result.Render.Segments) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got := result.Render.Segments[0].HTML; got != "<p><strong>accepted secret</strong></p>" {
		t.Fatalf("html=%q", got)
	}
}

func TestRenderProtectedRejectsMalformedAndEmptyFragments(t *testing.T) {
	plugin := &shortcodePlugin{}
	for _, raw := range []string{
		`{"decision":"denied","fragment":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"secret"}]}]}}`,
		`{"decision":"allowed","fragment":{"type":"doc","content":[]}}`,
	} {
		result, err := plugin.renderProtected(&pluginv2.ContentCall{
			Target:   pluginv2.ContentDeclaration{ID: shortcodeReplyID, ContractVersion: shortcodeReplyVersion},
			Document: pluginv2.ContentEditorDocument{SchemaVersion: pluginv2.ContentEditorDocumentSchema, Value: []byte(raw)}, Scope: "protected",
		})
		if err == nil || result.Render != nil || strings.Contains(string(raw), "secret") && result.Render != nil {
			t.Fatalf("malformed fragment accepted: result=%#v err=%v", result, err)
		}
	}
}
