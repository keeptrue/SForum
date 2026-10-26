package hostapi

import queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"

type queryRegistryTraceAdapter struct {
	sink QueryTraceSink
}

// NewQueryRegistryTraceAdapter lets the execution slice share the existing
// Host Query structured trace sink without exposing payloads or cache material.
func NewQueryRegistryTraceAdapter(sink QueryTraceSink) queryregistry.ExecutionTraceSink {
	return &queryRegistryTraceAdapter{sink: sink}
}

func (a *queryRegistryTraceAdapter) AppendExecutionTrace(trace queryregistry.ExecutionTrace) {
	if a == nil || a.sink == nil {
		return
	}
	outcome := QueryTraceError
	switch trace.Outcome {
	case queryregistry.TraceOutcomeAllowed:
		outcome = QueryTraceAllowed
	case queryregistry.TraceOutcomeDenied:
		outcome = QueryTraceDenied
	case queryregistry.TraceOutcomeSnapshotStale, queryregistry.TraceOutcomeRuntimeStale,
		queryregistry.TraceOutcomeProviderMissing, queryregistry.TraceOutcomeDependencyDenied:
		outcome = QueryTraceStale
	case queryregistry.TraceOutcomeCancelled:
		outcome = QueryTraceCancel
	case queryregistry.TraceOutcomeDeadline:
		outcome = QueryTraceDeadline
	}
	a.sink.RecordQueryTrace(boundedQueryTrace(QueryTrace{
		ExtensionID: trace.ExtensionID, ExtensionVersion: trace.ExtensionVersion,
		ArtifactDigest: trace.ArtifactDigest, QueryID: trace.QueryID, PlanVersion: trace.PlanVersion,
		ShapeDigest: trace.ShapeDigest, Duration: trace.Duration, Rows: trace.Rows, Outcome: outcome,
		Slow: trace.Duration >= ProtocolV2QueryDefaultSlowThreshold,
	}))
}
