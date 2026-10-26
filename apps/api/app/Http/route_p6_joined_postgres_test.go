package http

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// TestP6JoinedPostgresBehaviorMatrix is the database-backed P6 gate. The
// ordinary joined matrix proves producers and adapters; this test proves that
// every recordable stream class crosses the production recorder and durable
// store without retaining request or response payloads.
func TestP6JoinedPostgresBehaviorMatrix(t *testing.T) {
	fixture := newRouteRuntimeIncidentPGFixture(t)
	runtime := &recordingRouteIncidentRuntime{}
	auditor := &recordingRouteFailureAuditor{}
	recorder, err := newRouteFailureRecorder(
		runtime, fixture.store, auditor, discardRouteFailureLogger(), 8, 5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	classes := []routes.RouteStreamFailureClass{
		routes.RouteStreamFailureRuntimeTransport,
		routes.RouteStreamFailureHostBudget,
		routes.RouteStreamFailureInvalidPreflight,
		routes.RouteStreamFailureMissingTerminal,
	}
	events := make(map[routes.RouteStreamFailureClass]routes.RouteStreamFailure, len(classes))
	for index, class := range classes {
		event := routeFailureRecorderStreamEvent(class)
		event.RouteID = fmt.Sprintf("failure.plugin.stream.%d", index+1)
		event.ContractVersion = event.RouteID + "@1"
		event.PathSignature = fmt.Sprintf("/s:stream/%d", index+1)
		event.ActorID = fixture.actorID
		event.Artifact = fixture.artifact
		events[class] = event
		recorder.RecordStreamFailure(context.Background(), event)
	}
	closeRouteFailureRecorderForTest(t, recorder)
	if recorder.IncidentPersistenceFailures() != 0 {
		t.Fatalf("incident persistence failures=%d", recorder.IncidentPersistenceFailures())
	}

	runtime.mu.Lock()
	quarantines := append([]recordedRouteIncident(nil), runtime.calls...)
	runtime.mu.Unlock()
	if len(quarantines) != len(classes) {
		t.Fatalf("quarantines=%#v", quarantines)
	}
	for index, quarantine := range quarantines {
		if quarantine.exact.ExtensionID != fixture.artifact.ExtensionID ||
			quarantine.exact.InstanceID != fixture.artifact.RuntimeInstanceID ||
			quarantine.exact.ExtensionVersion != fixture.artifact.ExtensionVersion ||
			quarantine.exact.ArtifactDigest != fixture.artifact.PackageDigest ||
			!errors.Is(quarantine.cause, extensionsruntime.ErrRuntimeRouteIncident) ||
			!strings.Contains(quarantine.cause.Error(), string(classes[index])) {
			t.Fatalf("quarantine[%d]=%#v cause=%v", index, quarantine.exact, quarantine.cause)
		}
	}
	auditor.mu.Lock()
	ordinaryAuditEvents := len(auditor.events)
	auditor.mu.Unlock()
	if ordinaryAuditEvents != 0 {
		t.Fatalf("stream incidents escaped to ordinary audit queue: %d", ordinaryAuditEvents)
	}

	rows, err := fixture.pool.Query(fixture.ctx, `
		SELECT cause_class, incident_key
		FROM extension_route_runtime_incidents
		WHERE extension_id = $1
		ORDER BY cause_class
	`, fixture.artifact.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	type durableClass struct {
		class routes.RouteStreamFailureClass
		key   string
	}
	durable := make([]durableClass, 0, len(classes))
	for rows.Next() {
		var class, key string
		if err := rows.Scan(&class, &key); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		durable = append(durable, durableClass{class: routes.RouteStreamFailureClass(class), key: key})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(durable) != len(classes) {
		t.Fatalf("durable classes=%#v", durable)
	}

	seen := make(map[routes.RouteStreamFailureClass]struct{}, len(classes))
	for _, item := range durable {
		event, ok := events[item.class]
		if !ok {
			t.Fatalf("unexpected durable class %q", item.class)
		}
		if _, duplicate := seen[item.class]; duplicate {
			t.Fatalf("duplicate durable class %q", item.class)
		}
		seen[item.class] = struct{}{}
		record, err := scanRouteRuntimeIncident(fixture.pool.QueryRow(
			fixture.ctx, routeRuntimeIncidentSelectByKey, item.key,
		))
		if err != nil {
			t.Fatal(err)
		}
		want := routeStreamFailureIncidentEvidence(event)
		want.IncidentKey = item.key
		if record.Evidence != want || record.ExtensionVersionID != fixture.versionID ||
			record.AuditEventID <= 0 || record.LocalResult != RouteRuntimeIncidentQuarantined ||
			record.CreatedAt.IsZero() || record.ResolvedAt == nil || record.ResolvedAt.Before(record.CreatedAt) {
			t.Fatalf("class=%q record=%#v want=%#v", item.class, record, want)
		}
		expectedMetadata, err := routeRuntimeIncidentAuditMetadata(want)
		if err != nil {
			t.Fatal(err)
		}
		var actorID int64
		var action string
		var metadataMatches bool
		if err := fixture.pool.QueryRow(fixture.ctx, `
			SELECT COALESCE(actor_user_id, 0), action, metadata = $2::jsonb
			FROM audit_events WHERE id = $1
		`, record.AuditEventID, expectedMetadata).Scan(&actorID, &action, &metadataMatches); err != nil {
			t.Fatal(err)
		}
		if actorID != fixture.actorID || action != audit.ActionRouteRuntimeIncident || !metadataMatches {
			t.Fatalf("class=%q actor=%d action=%q metadataMatches=%t", item.class, actorID, action, metadataMatches)
		}
	}
}
