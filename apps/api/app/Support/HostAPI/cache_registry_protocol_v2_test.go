package hostapi

import (
	"context"
	"strings"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2CacheServiceUsesAttestedRuntimeAndCurrentContract(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "protocol.cache", "public", "")
	server, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.artifact.ExtensionID, ExtensionVersion: fixture.artifact.ExtensionVersion,
		ArtifactDigest: fixture.artifact.PackageDigest, InstanceId: fixture.artifact.RuntimeInstanceID,
	}
	requestContext := &protocolv2.RequestContext{RequestId: "cache-request", Extension: identity, Locale: "zh-CN"}
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	value, err := structpb.NewStruct(map[string]any{"title": "cached"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := server.Set(ctx, &hostv2.CacheSetRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "post:42",
		Value: &protocolv2.TypedDocument{SchemaId: fixture.schema.ID, SchemaVersion: fixture.schema.Version, Value: value},
		Ttl:   durationpb.New(time.Minute), Tags: []string{fixture.cache.Tags[0]},
	})
	if err != nil || set.GetError() != nil || set.GetRevision() == "" ||
		set.GetContext().GetExtension().GetInstanceId() != identity.GetInstanceId() {
		t.Fatalf("set=%#v err=%v", set, err)
	}
	get, err := server.Get(ctx, &hostv2.CacheGetRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "post:42",
		ValueSchemaId: fixture.schema.ID, ValueSchemaVersion: fixture.schema.Version,
	})
	if err != nil || get.GetError() != nil || !get.GetFound() || get.GetRevision() != set.GetRevision() ||
		get.GetValue().GetValue().AsMap()["title"] != "cached" {
		t.Fatalf("get=%#v err=%v", get, err)
	}

	forged := proto.Clone(requestContext).(*protocolv2.RequestContext)
	forged.Extension = &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.artifact.ExtensionID, ExtensionVersion: fixture.artifact.ExtensionVersion,
		ArtifactDigest: strings.Repeat("f", 64), InstanceId: fixture.artifact.RuntimeInstanceID,
	}
	response, err := server.Get(ctx, &hostv2.CacheGetRequest{Context: forged, Namespace: fixture.cache.Namespace, Key: "post:42"})
	if err != nil || response.GetError().GetReason() != "host.cache_runtime_stale" {
		t.Fatalf("forged identity response=%#v err=%v", response, err)
	}
}

func TestProtocolV2CacheServiceIncrementUsesExactNamespaceAndTTL(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "protocol-counter.cache", "public", "")
	server, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.artifact.ExtensionID, ExtensionVersion: fixture.artifact.ExtensionVersion,
		ArtifactDigest: fixture.artifact.PackageDigest, InstanceId: fixture.artifact.RuntimeInstanceID,
	}
	requestContext := &protocolv2.RequestContext{RequestId: "cache-increment", Extension: identity, Locale: "zh-CN"}
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)

	first, err := server.Increment(ctx, &hostv2.CacheIncrementRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "views:42",
		Delta: 2, Ttl: durationpb.New(time.Minute),
	})
	if err != nil || first.GetError() != nil || first.GetValue() != 2 {
		t.Fatalf("first increment=%#v err=%v", first, err)
	}
	second, err := server.Increment(ctx, &hostv2.CacheIncrementRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "views:42",
		Delta: -1, Ttl: durationpb.New(time.Minute),
	})
	if err != nil || second.GetError() != nil || second.GetValue() != 1 {
		t.Fatalf("second increment=%#v err=%v", second, err)
	}

	invalid, err := server.Increment(ctx, &hostv2.CacheIncrementRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "views:42",
	})
	if err != nil || invalid.GetError().GetReason() != "host.cache_request_invalid" {
		t.Fatalf("invalid increment=%#v err=%v", invalid, err)
	}
}

func TestProtocolV2CacheServiceFailsClosedWithoutActorScopeContract(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "actor-protocol.cache", "actor", "")
	server, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.artifact.ExtensionID, ExtensionVersion: fixture.artifact.ExtensionVersion,
		ArtifactDigest: fixture.artifact.PackageDigest, InstanceId: fixture.artifact.RuntimeInstanceID,
	}
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
	requestContext := &protocolv2.RequestContext{RequestId: "actor-cache", Extension: identity, Locale: "zh-CN"}
	response, err := server.Get(ctx, &hostv2.CacheGetRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "feed",
		ValueSchemaId: fixture.schema.ID, ValueSchemaVersion: fixture.schema.Version,
	})
	if err != nil || response.GetError().GetReason() != "host.cache_scope_unattested" {
		t.Fatalf("actorless response=%#v err=%v", response, err)
	}

	requestContext.Actor = &protocolv2.Actor{UserId: 42}
	response, err = server.Get(ctx, &hostv2.CacheGetRequest{
		Context: requestContext, Namespace: fixture.cache.Namespace, Key: "feed",
		ValueSchemaId: fixture.schema.ID, ValueSchemaVersion: fixture.schema.Version,
	})
	if err != nil || response.GetError().GetReason() != "host.cache_scope_unattested" {
		t.Fatalf("unattested actor response=%#v err=%v", response, err)
	}
}

func TestProtocolV2CacheFailureNeverExposesBackendErrors(t *testing.T) {
	detail := protocolV2CacheFailure(context.Canceled)
	if strings.Contains(detail.GetMessage(), "provider") || len(detail.GetMessage()) > 160 {
		t.Fatalf("cache error leaked internals = %#v", detail)
	}
	secret := strings.Repeat("redis-password-secret", 20)
	detail = protocolV2CacheFailure(&secretHostCacheError{text: secret})
	if strings.Contains(detail.GetMessage(), "secret") || strings.Contains(detail.GetReason(), "secret") {
		t.Fatalf("cache error leaked secret = %#v", detail)
	}
}

type secretHostCacheError struct{ text string }

func (e *secretHostCacheError) Error() string { return e.text }
