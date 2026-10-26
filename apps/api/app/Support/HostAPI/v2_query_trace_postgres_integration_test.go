package hostapi

import (
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestPostgresProtocolV2QueryTraceRecordsBoundedExecutionMetadata(t *testing.T) {
	h := newPostgresQueryHarness(t)
	sink := &recordingQueryTraceSink{}
	h.engine.traceSink = sink
	h.engine.slowThreshold = time.Nanosecond
	request := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicTopicsList,
		PlanVersion: QueryStableCorePlanVersion, Fields: []string{"id", "title"},
		Page: &protocolv2.PageRequest{Limit: 2},
	}
	response := h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity), request)
	if response.GetError() != nil || len(response.GetRows()) != 2 {
		t.Fatalf("query response = %#v", response)
	}
	trace := sink.single(t)
	if trace.ExtensionID != h.identity.GetExtensionId() ||
		trace.ExtensionVersion != h.identity.GetExtensionVersion() ||
		trace.ArtifactDigest != h.identity.GetArtifactDigest() ||
		trace.QueryID != QueryPublicTopicsList || trace.PlanVersion != QueryStableCorePlanVersion ||
		len(trace.ShapeDigest) != 64 || trace.Rows != 2 || trace.Outcome != QueryTraceAllowed ||
		trace.Duration <= 0 || !trace.Slow {
		t.Fatalf("PostgreSQL query trace = %#v", trace)
	}
}
