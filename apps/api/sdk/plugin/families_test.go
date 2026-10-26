package plugin

import (
	"strings"
	"testing"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestFrozenFamilySurfacesCompleteAndOrdered(t *testing.T) {
	surfaces := FrozenFamilySurfaces()
	if len(surfaces) != 6 {
		t.Fatalf("want 6 frozen families, got %d", len(surfaces))
	}
	wantIDs := []string{
		FamilyCommands, FamilyHooks, FamilyJobs, FamilyProviderSlots, FamilySchedules, FamilyServices,
	}
	// FrozenFamilySurfaces is sorted by ID.
	for i, want := range wantIDs {
		if surfaces[i].ID != want {
			t.Fatalf("surfaces[%d]=%s want %s", i, surfaces[i].ID, want)
		}
		if surfaces[i].ManifestField == "" || surfaces[i].CatalogDoc == "" || surfaces[i].Boundary == "" {
			t.Fatalf("family %s missing required metadata", surfaces[i].ID)
		}
		if len(surfaces[i].SourceAuthorities) == 0 {
			t.Fatalf("family %s missing source authorities", surfaces[i].ID)
		}
	}
	// schedules must honestly report non-callable transport and no public helper.
	for _, surface := range surfaces {
		if surface.ID == FamilySchedules {
			if surface.CallableTransport {
				t.Fatal("schedules must not claim a callable transport")
			}
			if !strings.Contains(surface.Boundary, "does not register ScheduleService") &&
				!strings.Contains(surface.SDKAuthorEntry, "wire-only") {
				t.Fatal("schedules must deny a registered ScheduleService / List-Trigger helper")
			}
		}
		if surface.ID != FamilySchedules && !surface.CallableTransport {
			t.Fatalf("%s should document callable transport where Host/plugin RPCs exist", surface.ID)
		}
	}
}

func TestFrozenFamilyLimitsTrackSourceConstants(t *testing.T) {
	limits := FrozenFamilyLimits()
	if limits.HookMaximumTimeoutMS != extensionmanifest.HookMaximumTimeoutMS {
		t.Fatalf("hook timeout drifted: %d", limits.HookMaximumTimeoutMS)
	}
	if limits.ProviderSlotMaximumTimeoutMS != extensionmanifest.ProviderSlotMaximumTimeoutMS {
		t.Fatalf("provider timeout drifted: %d", limits.ProviderSlotMaximumTimeoutMS)
	}
	if limits.PluginCommandMaximumTimeoutMS != extensionmanifest.PluginCommandMaximumTimeoutMS {
		t.Fatalf("command timeout drifted: %d", limits.PluginCommandMaximumTimeoutMS)
	}
	if limits.PluginJobMaximumConcurrency != extensionmanifest.PluginJobMaximumConcurrencyLimit {
		t.Fatalf("job concurrency drifted: %d", limits.PluginJobMaximumConcurrency)
	}
	if limits.PluginJobMaximumAttempts != extensionmanifest.PluginJobMaximumAttempts {
		t.Fatalf("job attempts drifted: %d", limits.PluginJobMaximumAttempts)
	}
	if limits.DefaultSyncTimeoutMS != appevents.DefaultSyncTimeoutMS {
		t.Fatalf("sync timeout drifted: %d", limits.DefaultSyncTimeoutMS)
	}
	if limits.DefaultAsyncTimeoutMS != appevents.DefaultAsyncTimeoutMS {
		t.Fatalf("async timeout drifted: %d", limits.DefaultAsyncTimeoutMS)
	}
}

func TestFrozenEnumCatalogsTrackSource(t *testing.T) {
	if got := strings.Join(HookFailurePolicyValues(), ","); got != appevents.FailurePolicyFailClosed+","+appevents.FailurePolicyFailOpen {
		t.Fatalf("failure policies drifted: %s", got)
	}
	wantRetry := strings.Join([]string{
		supportjobs.PluginJobRetryNone, supportjobs.PluginJobRetryBounded, supportjobs.PluginJobRetryExponential,
	}, ",")
	if got := strings.Join(JobRetryPolicyValues(), ","); got != wantRetry {
		t.Fatalf("retry policies drifted: %s", got)
	}
	for _, kind := range HookKindValues() {
		switch kind {
		case "action", "filter", "observe":
		default:
			t.Fatalf("unexpected hook kind %q", kind)
		}
	}
}

func TestProviderSlotCatalogTracksKnownSlots(t *testing.T) {
	slots := KnownProviderSlots()
	catalog := ProviderSlotCatalog()
	if len(slots) != len(catalog) {
		t.Fatalf("provider slot catalog length mismatch: known=%d catalog=%d", len(slots), len(catalog))
	}
	for i, slot := range slots {
		if catalog[i].Slot != slot {
			t.Fatalf("provider slot order drifted at %d: %s != %s", i, catalog[i].Slot, slot)
		}
		if catalog[i].Description == "" {
			t.Fatalf("provider slot %s missing description", slot)
		}
	}
}

func TestCoreSchedulesParityWithJobsPackage(t *testing.T) {
	sdk := CoreSchedules()
	src := supportjobs.CoreScheduleDefinitions()
	if len(sdk) != len(src) {
		t.Fatalf("core schedule count drifted: sdk=%d src=%d", len(sdk), len(src))
	}
	for i := range src {
		if sdk[i].ID != src[i].ID || sdk[i].JobKind != src[i].JobKind || sdk[i].Interval != src[i].Interval {
			t.Fatalf("core schedule[%d] drifted: %#v vs %#v", i, sdk[i], src[i])
		}
	}
}

func TestEventCatalogParityWithEventsPackage(t *testing.T) {
	sdk := EventCatalog()
	src := appevents.Definitions()
	if len(sdk) != len(src) {
		t.Fatalf("event catalog count drifted: sdk=%d src=%d", len(sdk), len(src))
	}
	for i := range src {
		if sdk[i].Name != src[i].Name || sdk[i].Kind != src[i].Kind || sdk[i].TimeoutMS != src[i].TimeoutMS {
			t.Fatalf("event[%d] drifted: %#v vs %#v", i, sdk[i], src[i])
		}
	}
}
