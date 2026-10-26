package hostapi

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

const (
	HostCacheInspectorSchemaVersion = "sforum.cache-inspector@1"
	hostCacheInspectionDefaultLimit = 100
	hostCacheInspectionMaximumLimit = 200
)

var (
	ErrHostCacheInspectorInvalid  = errors.New("host cache inspector request is invalid")
	ErrHostCacheInspectorConflict = errors.New("host cache registry changed during inspection")
)

// HostCacheInspectionTrace is the metadata-only public view of one operation.
// Cache keys, values, tag names, lock tokens, and request payloads never enter it.
type HostCacheInspectionTrace struct {
	Sequence          uint64                `json:"sequence"`
	ExtensionID       string                `json:"extensionId,omitempty"`
	ExtensionVersion  string                `json:"extensionVersion,omitempty"`
	ArtifactDigest    string                `json:"artifactDigest,omitempty"`
	RuntimeInstanceID string                `json:"runtimeInstanceId,omitempty"`
	VersionID         int64                 `json:"versionId,omitempty"`
	CacheID           string                `json:"cacheId,omitempty"`
	ContractVersion   string                `json:"contractVersion,omitempty"`
	RegistryRevision  uint64                `json:"registryRevision,omitempty"`
	RegistryCurrent   bool                  `json:"registryCurrent"`
	ProviderRevision  uint64                `json:"providerRevision,omitempty"`
	ProviderID        string                `json:"providerId,omitempty"`
	ProviderExtension string                `json:"providerExtension,omitempty"`
	ProviderArtifact  string                `json:"providerArtifact,omitempty"`
	ProviderRuntime   string                `json:"providerRuntime,omitempty"`
	ProviderVersionID int64                 `json:"providerVersionId,omitempty"`
	Operation         string                `json:"operation"`
	TagDigest         string                `json:"tagDigest,omitempty"`
	TagCount          int                   `json:"tagCount,omitempty"`
	InvalidatorID     string                `json:"invalidatorId,omitempty"`
	Outcome           HostCacheTraceOutcome `json:"outcome"`
	DurationMicros    uint64                `json:"durationMicros"`
	Attempts          int                   `json:"attempts,omitempty"`
	Hit               bool                  `json:"hit,omitempty"`
	Affected          uint64                `json:"affected,omitempty"`
	Slow              bool                  `json:"slow,omitempty"`
}

type HostCacheInspectionMetrics struct {
	Operation             string `json:"operation,omitempty"`
	Samples               uint64 `json:"samples"`
	Hits                  uint64 `json:"hits"`
	Misses                uint64 `json:"misses"`
	Allowed               uint64 `json:"allowed"`
	Denied                uint64 `json:"denied"`
	Stale                 uint64 `json:"stale"`
	Conflicts             uint64 `json:"conflicts"`
	Errors                uint64 `json:"errors"`
	Canceled              uint64 `json:"canceled"`
	Deadlines             uint64 `json:"deadlines"`
	Slow                  uint64 `json:"slow"`
	Affected              uint64 `json:"affected"`
	TotalDurationMicros   uint64 `json:"totalDurationMicros"`
	AverageDurationMicros uint64 `json:"averageDurationMicros"`
	P95DurationMicros     uint64 `json:"p95DurationMicros"`
}

// HostCacheInspectionRegistrySnapshot is a redacted Registry disclosure. It
// deliberately has no field capable of carrying cache tag names.
type HostCacheInspectionRegistrySnapshot struct {
	SchemaVersion string                            `json:"schemaVersion"`
	Revision      uint64                            `json:"revision"`
	Digest        string                            `json:"digest"`
	SafeMode      bool                              `json:"safeMode"`
	Publications  []HostCacheInspectionPublication  `json:"publications"`
	Caches        []HostCacheInspectionContribution `json:"caches"`
}

type HostCacheInspectionArtifact struct {
	ExtensionID       string `json:"extensionId"`
	ExtensionVersion  string `json:"extensionVersion"`
	PackageDigest     string `json:"packageDigest"`
	VersionID         int64  `json:"versionId,omitempty"`
	RuntimeInstanceID string `json:"runtimeInstanceId,omitempty"`
	Core              bool   `json:"core,omitempty"`
}

type HostCacheInspectionDeclaration struct {
	ID              string   `json:"id"`
	ContractVersion string   `json:"contractVersion"`
	Namespace       string   `json:"namespace"`
	Policy          string   `json:"policy"`
	Provider        string   `json:"provider,omitempty"`
	Invalidators    []string `json:"invalidators,omitempty"`
}

type HostCacheInspectionPublication struct {
	Artifact HostCacheInspectionArtifact      `json:"artifact"`
	Caches   []HostCacheInspectionDeclaration `json:"caches"`
}

type HostCacheInspectionContribution struct {
	HostCacheInspectionDeclaration
	Artifact HostCacheInspectionArtifact `json:"artifact"`
}

type HostCacheInspectionSnapshot struct {
	SchemaVersion   string                              `json:"schemaVersion"`
	Registry        HostCacheInspectionRegistrySnapshot `json:"registry"`
	RetainedFrom    uint64                              `json:"retainedFromSequence,omitempty"`
	RetainedThrough uint64                              `json:"retainedThroughSequence,omitempty"`
	Metrics         HostCacheInspectionMetrics          `json:"metrics"`
	Operations      []HostCacheInspectionMetrics        `json:"operations"`
	Traces          []HostCacheInspectionTrace          `json:"traces"`
	Invalidations   []HostCacheInspectionTrace          `json:"invalidations"`
}

// Inspect returns a revision-consistent Registry and trace snapshot. Traces are
// newest-first; metrics cover the complete retained ring rather than the page.
func (i *HostCacheInspector) Inspect(
	registry *cacheregistry.Registry,
	limit int,
) (HostCacheInspectionSnapshot, error) {
	if i == nil || registry == nil || limit < 0 {
		return HostCacheInspectionSnapshot{}, ErrHostCacheInspectorInvalid
	}
	if limit == 0 {
		limit = hostCacheInspectionDefaultLimit
	}
	if limit > hostCacheInspectionMaximumLimit {
		limit = hostCacheInspectionMaximumLimit
	}
	for attempt := 0; attempt < 3; attempt++ {
		before := registry.Snapshot()
		traces := i.Snapshot()
		after := registry.Snapshot()
		if before.Revision != after.Revision || before.Digest != after.Digest || before.SafeMode != after.SafeMode {
			continue
		}
		return buildHostCacheInspection(after, traces, limit), nil
	}
	return HostCacheInspectionSnapshot{}, ErrHostCacheInspectorConflict
}

func buildHostCacheInspection(
	registry cacheregistry.Snapshot,
	traces []HostCacheTrace,
	limit int,
) HostCacheInspectionSnapshot {
	result := HostCacheInspectionSnapshot{
		SchemaVersion: HostCacheInspectorSchemaVersion,
		Registry:      hostCacheInspectionRegistrySnapshot(registry),
		Metrics:       aggregateHostCacheInspectionMetrics("", traces),
		Operations:    []HostCacheInspectionMetrics{},
		Traces:        []HostCacheInspectionTrace{}, Invalidations: []HostCacheInspectionTrace{},
	}
	if len(traces) > 0 {
		result.RetainedFrom = traces[0].Sequence
		result.RetainedThrough = traces[len(traces)-1].Sequence
	}
	byOperation := make(map[string][]HostCacheTrace)
	for _, trace := range traces {
		byOperation[trace.Operation] = append(byOperation[trace.Operation], trace)
	}
	operations := make([]string, 0, len(byOperation))
	for operation := range byOperation {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	for _, operation := range operations {
		result.Operations = append(result.Operations,
			aggregateHostCacheInspectionMetrics(operation, byOperation[operation]))
	}
	for index := len(traces) - 1; index >= 0; index-- {
		view := hostCacheInspectionTrace(traces[index], registry.Revision)
		if len(result.Traces) < limit {
			result.Traces = append(result.Traces, view)
		}
		if strings.HasPrefix(traces[index].Operation, "invalidate_") && len(result.Invalidations) < limit {
			result.Invalidations = append(result.Invalidations, view)
		}
	}
	return result
}

func hostCacheInspectionRegistrySnapshot(snapshot cacheregistry.Snapshot) HostCacheInspectionRegistrySnapshot {
	view := HostCacheInspectionRegistrySnapshot{
		SchemaVersion: snapshot.SchemaVersion, Revision: snapshot.Revision,
		Digest: snapshot.Digest, SafeMode: snapshot.SafeMode,
		Publications: make([]HostCacheInspectionPublication, 0, len(snapshot.Publications)),
		Caches:       make([]HostCacheInspectionContribution, 0, len(snapshot.Caches)),
	}
	for _, publication := range snapshot.Publications {
		item := HostCacheInspectionPublication{
			Artifact: hostCacheInspectionArtifact(publication.Artifact),
			Caches:   make([]HostCacheInspectionDeclaration, 0, len(publication.Caches)),
		}
		for _, declaration := range publication.Caches {
			item.Caches = append(item.Caches, hostCacheInspectionDeclaration(declaration))
		}
		view.Publications = append(view.Publications, item)
	}
	for _, contribution := range snapshot.Caches {
		view.Caches = append(view.Caches, HostCacheInspectionContribution{
			HostCacheInspectionDeclaration: hostCacheInspectionDeclaration(contribution.Declaration),
			Artifact:                       hostCacheInspectionArtifact(contribution.Artifact),
		})
	}
	return view
}

func hostCacheInspectionArtifact(artifact cacheregistry.Artifact) HostCacheInspectionArtifact {
	return HostCacheInspectionArtifact{
		ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
		PackageDigest: artifact.PackageDigest, VersionID: artifact.VersionID,
		RuntimeInstanceID: artifact.RuntimeInstanceID, Core: artifact.Core,
	}
}

func hostCacheInspectionDeclaration(declaration cacheregistry.Declaration) HostCacheInspectionDeclaration {
	return HostCacheInspectionDeclaration{
		ID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Namespace: declaration.Namespace, Policy: declaration.Policy,
		Provider:     declaration.Provider,
		Invalidators: append([]string(nil), declaration.Invalidators...),
	}
}

func aggregateHostCacheInspectionMetrics(
	operation string,
	traces []HostCacheTrace,
) HostCacheInspectionMetrics {
	result := HostCacheInspectionMetrics{Operation: operation, Samples: uint64(len(traces))}
	durations := make([]uint64, 0, len(traces))
	for _, trace := range traces {
		duration := uint64(max(trace.Duration/time.Microsecond, 0))
		durations = append(durations, duration)
		result.TotalDurationMicros += duration
		result.Affected += trace.Affected
		if trace.Slow {
			result.Slow++
		}
		switch trace.Outcome {
		case HostCacheTraceHit:
			result.Hits++
		case HostCacheTraceMiss:
			result.Misses++
		case HostCacheTraceAllowed:
			result.Allowed++
		case HostCacheTraceDenied:
			result.Denied++
		case HostCacheTraceStale:
			result.Stale++
		case HostCacheTraceConflict:
			result.Conflicts++
		case HostCacheTraceCancel:
			result.Canceled++
		case HostCacheTraceDeadline:
			result.Deadlines++
		default:
			result.Errors++
		}
	}
	if result.Samples == 0 {
		return result
	}
	result.AverageDurationMicros = result.TotalDurationMicros / result.Samples
	slices.Sort(durations)
	index := (len(durations)*95+99)/100 - 1
	result.P95DurationMicros = durations[index]
	return result
}

func hostCacheInspectionTrace(trace HostCacheTrace, currentRevision uint64) HostCacheInspectionTrace {
	trace = boundedHostCacheTrace(trace)
	return HostCacheInspectionTrace{
		Sequence:    trace.Sequence,
		ExtensionID: trace.ExtensionID, ExtensionVersion: trace.ExtensionVersion,
		ArtifactDigest: trace.ArtifactDigest, RuntimeInstanceID: trace.RuntimeInstanceID,
		VersionID: trace.VersionID, CacheID: trace.CacheID, ContractVersion: trace.ContractVersion,
		RegistryRevision: trace.RegistryRevision,
		RegistryCurrent:  trace.RegistryRevision != 0 && trace.RegistryRevision == currentRevision,
		ProviderRevision: trace.ProviderRevision, ProviderID: trace.ProviderID,
		ProviderExtension: trace.ProviderExtension, ProviderArtifact: trace.ProviderArtifact,
		ProviderRuntime: trace.ProviderRuntime, ProviderVersionID: trace.ProviderVersionID,
		Operation: trace.Operation, TagDigest: trace.TagDigest, TagCount: trace.TagCount,
		InvalidatorID: trace.InvalidatorID, Outcome: trace.Outcome,
		DurationMicros: uint64(max(trace.Duration/time.Microsecond, 0)),
		Attempts:       trace.Attempts, Hit: trace.Hit, Affected: trace.Affected, Slow: trace.Slow,
	}
}
