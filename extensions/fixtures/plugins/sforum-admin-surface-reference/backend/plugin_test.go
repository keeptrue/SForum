package main

import (
	"testing"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestReferenceDeclarationsCoverEveryAdminSurfaceKind(t *testing.T) {
	wantKinds := map[string]bool{
		"navigation": false, "dashboard": false, "list_column": false, "list_filter": false,
		"row_action": false, "bulk_action": false, "form": false, "notice": false,
		"editor_panel": false, "detail_region": false, "importer": false, "exporter": false,
	}
	for _, declaration := range surfaceDeclarations {
		if _, expected := wantKinds[declaration.kind]; !expected {
			t.Fatalf("unexpected kind %q", declaration.kind)
		}
		wantKinds[declaration.kind] = true
	}
	for kind, covered := range wantKinds {
		if !covered {
			t.Fatalf("kind %q is not covered", kind)
		}
	}
}

func TestReferenceHandlersReturnSerializableTypedDescriptors(t *testing.T) {
	input := map[string]any{
		"selection": "active",
		"resources": []any{
			map[string]any{"id": "1", "attributes": map[string]any{"status": "active"}},
			map[string]any{"id": "2", "attributes": map[string]any{"status": "disabled"}},
		},
		"context": map[string]any{"resource": map[string]any{"id": float64(1), "status": "active", "displayName": "Admin"}},
	}
	request := &protocolwire.RequestContext{
		Actor:          &protocolwire.Actor{UserId: 42},
		IdempotencyKey: "reference-command-42",
	}
	for _, declaration := range surfaceDeclarations {
		result, err := renderAdminSurface(declaration, input, request)
		if err != nil {
			t.Fatalf("render %s: %v", declaration.id, err)
		}
		if len(result) == 0 {
			t.Fatalf("render %s returned no descriptor", declaration.id)
		}
		if _, err := structpb.NewStruct(result); err != nil {
			t.Fatalf("render %s is not a typed protobuf document: %v", declaration.id, err)
		}
	}
}

func TestReferenceCommandsRequireActorAndIdempotency(t *testing.T) {
	if err := validateCommandAuthority(&protocolwire.RequestContext{}); err == nil {
		t.Fatal("missing actor was accepted")
	}
	if err := validateCommandAuthority(&protocolwire.RequestContext{Actor: &protocolwire.Actor{UserId: 42}}); err == nil {
		t.Fatal("missing idempotency key was accepted")
	}
	if err := validateCommandAuthority(&protocolwire.RequestContext{
		Actor: &protocolwire.Actor{UserId: 42}, IdempotencyKey: "reference-command-42",
	}); err != nil {
		t.Fatalf("valid command authority: %v", err)
	}
}
