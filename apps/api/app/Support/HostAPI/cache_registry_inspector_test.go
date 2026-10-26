package hostapi

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestHostCacheInspectorReturnsRevisionMetricsAndInvalidationAudit(t *testing.T) {
	registry := hostCacheInspectionRegistry(t)
	inspector := NewHostCacheInspector(8)
	revision := registry.Revision()
	for _, trace := range []HostCacheTrace{
		{Operation: "get", Outcome: HostCacheTraceHit, Hit: true, Duration: 10 * time.Microsecond, RegistryRevision: revision},
		{Operation: "get", Outcome: HostCacheTraceMiss, Duration: 20 * time.Microsecond, RegistryRevision: revision},
		{Operation: "set", Outcome: HostCacheTraceError, Duration: 30 * time.Microsecond, RegistryRevision: revision, Slow: true},
		{
			Operation: "invalidate_declared", Outcome: HostCacheTraceAllowed,
			Duration: 40 * time.Microsecond, RegistryRevision: revision,
			TagDigest: strings.Repeat("a", 64), TagCount: 2,
			InvalidatorID: "forum.topic.updated", Affected: 7,
		},
	} {
		inspector.RecordHostCacheTrace(trace)
	}

	snapshot, err := inspector.Inspect(registry, 2)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != HostCacheInspectorSchemaVersion || snapshot.Registry.Revision != revision ||
		snapshot.RetainedFrom != 1 || snapshot.RetainedThrough != 4 || len(snapshot.Traces) != 2 ||
		len(snapshot.Invalidations) != 1 || snapshot.Traces[0].Sequence != 4 || !snapshot.Traces[0].RegistryCurrent {
		t.Fatalf("inspection snapshot = %#v", snapshot)
	}
	if snapshot.Metrics.Samples != 4 || snapshot.Metrics.Hits != 1 || snapshot.Metrics.Misses != 1 ||
		snapshot.Metrics.Allowed != 1 || snapshot.Metrics.Errors != 1 || snapshot.Metrics.Slow != 1 ||
		snapshot.Metrics.Affected != 7 || snapshot.Metrics.TotalDurationMicros != 100 ||
		snapshot.Metrics.AverageDurationMicros != 25 || snapshot.Metrics.P95DurationMicros != 40 {
		t.Fatalf("inspection metrics = %#v", snapshot.Metrics)
	}
	if len(snapshot.Operations) != 3 || snapshot.Operations[0].Operation != "get" ||
		snapshot.Operations[0].Samples != 2 || snapshot.Operations[0].P95DurationMicros != 20 {
		t.Fatalf("operation metrics = %#v", snapshot.Operations)
	}
	invalidation := snapshot.Invalidations[0]
	if invalidation.TagDigest != strings.Repeat("a", 64) || invalidation.TagCount != 2 ||
		invalidation.InvalidatorID != "forum.topic.updated" || invalidation.Affected != 7 {
		t.Fatalf("invalidation audit = %#v", invalidation)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"tags"`) || strings.Contains(string(encoded), "core.inspect.secret_tag") {
		t.Fatalf("Inspector leaked Registry tag names: %s", encoded)
	}
}

func TestHostCacheInspectorMarksOldRevisionAndDetachesOutput(t *testing.T) {
	registry := hostCacheInspectionRegistry(t)
	inspector := NewHostCacheInspector(2)
	inspector.RecordHostCacheTrace(HostCacheTrace{
		ExtensionID: "core.inspect", Operation: "get", Outcome: HostCacheTraceHit,
		RegistryRevision: registry.Revision(),
	})
	current := registry.Snapshot()
	if _, err := registry.ReplaceAllIfRevision(current.Revision, current.Publications, true); err != nil {
		t.Fatal(err)
	}

	first, err := inspector.Inspect(registry, 999)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Traces) != 1 || first.Traces[0].RegistryCurrent {
		t.Fatalf("old revision trace = %#v", first.Traces)
	}
	first.Registry.Publications[0].Artifact.ExtensionID = "mutated"
	first.Traces[0].ExtensionID = "mutated"
	second, err := inspector.Inspect(registry, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.Registry.Publications[0].Artifact.ExtensionID != "core.inspect" ||
		second.Traces[0].ExtensionID != "core.inspect" {
		t.Fatal("caller mutation changed Registry or trace state")
	}
}

func TestHostCacheInspectorRejectsInvalidAndIsRaceSafe(t *testing.T) {
	registry := hostCacheInspectionRegistry(t)
	inspector := NewHostCacheInspector(64)
	if _, err := (*HostCacheInspector)(nil).Inspect(registry, 1); !errors.Is(err, ErrHostCacheInspectorInvalid) {
		t.Fatal("nil inspector was accepted")
	}
	if _, err := inspector.Inspect(nil, 1); !errors.Is(err, ErrHostCacheInspectorInvalid) {
		t.Fatal("nil registry was accepted")
	}
	if _, err := inspector.Inspect(registry, -1); !errors.Is(err, ErrHostCacheInspectorInvalid) {
		t.Fatal("negative limit was accepted")
	}

	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			inspector.RecordHostCacheTrace(HostCacheTrace{
				Operation: "get", Outcome: HostCacheTraceHit,
				Duration:         time.Duration(value+1) * time.Microsecond,
				RegistryRevision: registry.Revision(),
			})
			if _, err := inspector.Inspect(registry, 10); err != nil {
				t.Errorf("inspect: %v", err)
			}
		}(index)
	}
	group.Wait()
}

func hostCacheInspectionRegistry(t *testing.T) *cacheregistry.Registry {
	t.Helper()
	artifact, err := cacheregistry.NewCoreArtifact("core.inspect", "1.0.0", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	registry := cacheregistry.New()
	if _, err := registry.Publish(cacheregistry.Publication{
		Artifact: artifact,
		Caches: []cacheregistry.Declaration{{
			ID: "core.inspect.items", ContractVersion: "core.inspect.items@1",
			Namespace: "core.inspect.items", Policy: cacheregistry.PolicyPublic,
			Tags: []string{"core.inspect.secret_tag"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}
