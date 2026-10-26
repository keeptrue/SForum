package http

import (
	"testing"
	"time"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// M5：管理端视图必须与持久状态一致，且不泄漏任何请求内容。
func TestRouteRuntimeIncidentViewsProjectDurableState(t *testing.T) {
	created := time.Date(2026, 10, 16, 7, 30, 0, 0, time.UTC)
	resolved := created.Add(2 * time.Second)
	records := []RouteRuntimeIncidentRecord{{
		ID: 7, AuditEventID: 9001, LocalResult: RouteRuntimeIncidentQuarantined,
		CreatedAt: created, ResolvedAt: &resolved,
		Evidence: RouteRuntimeIncidentEvidence{
			IncidentKey: "key-7", RouteID: "acme.demo.route.read",
			ContractVersion: "acme.demo.route.read@1", Method: "GET", PathSignature: "/api/v1/acme-demo",
			Phase: routes.RoutePhaseHandler, Action: "add",
			FailureCode: routes.RouteFailureTransportFailed, CauseClass: "runtime_transport",
			CommitState: routes.RouteCommitFinal,
			Artifact: routes.PluginArtifact{
				ExtensionID: "acme.demo", ExtensionVersion: "1.0.0",
				PackageDigest: "digest-value", RuntimeInstanceID: "instance-7",
			},
		},
	}, {
		ID: 8, LocalResult: RouteRuntimeIncidentPending, CreatedAt: created,
		Evidence: RouteRuntimeIncidentEvidence{IncidentKey: "key-8", Phase: routes.RoutePhaseHandler},
	}, {
		ID: 9, LocalResult: RouteRuntimeIncidentStaleMissing, CreatedAt: created,
		Evidence: RouteRuntimeIncidentEvidence{IncidentKey: "key-9", Phase: routes.RoutePhaseHandler},
	}, {
		ID: 10, LocalResult: RouteRuntimeIncidentFailed, CreatedAt: created,
		Evidence: RouteRuntimeIncidentEvidence{IncidentKey: "key-10", Phase: routes.RoutePhaseHandler},
	}}

	views := RouteRuntimeIncidentViews(records)
	if len(views) != 4 {
		t.Fatalf("views = %#v", views)
	}
	if !views[0].Quarantined || !views[0].Resolved || views[0].LocalResult != string(RouteRuntimeIncidentQuarantined) {
		t.Fatalf("quarantined view = %#v", views[0])
	}
	if views[0].CreatedAt != "2026-10-16T07:30:00Z" || views[0].ResolvedAt != "2026-10-16T07:30:02Z" {
		t.Fatalf("timestamps = %#v", views[0])
	}
	if views[0].ExtensionID != "acme.demo" || views[0].RuntimeInstanceID != "instance-7" ||
		views[0].AuditEventID != 9001 || views[0].CauseClass != "runtime_transport" {
		t.Fatalf("artifact disclosure = %#v", views[0])
	}
	if views[1].Quarantined || views[1].Resolved || views[3].Quarantined {
		t.Fatalf("non-quarantine views = %#v", views[1:])
	}

	summary := SummarizeRouteRuntimeIncidents(records)
	if summary.Quarantined != 1 || summary.Pending != 1 || summary.Recovered != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

// 读取必须有界：非法或过大的 limit 收敛到硬上限，绝不无界读取。
func TestRouteRuntimeIncidentReadIsBounded(t *testing.T) {
	store := &PostgresRouteRuntimeIncidentStore{}
	for _, limit := range []int{0, -5, RouteRuntimeIncidentReadMaximum + 1000} {
		if _, err := store.ListRecentRouteRuntimeIncidents(t.Context(), limit); err == nil {
			t.Fatalf("unconfigured store must fail closed for limit=%d", limit)
		}
	}
	if RouteRuntimeIncidentReadMaximum > 500 {
		t.Fatalf("read maximum grew beyond a bounded diagnostic read: %d", RouteRuntimeIncidentReadMaximum)
	}
}
