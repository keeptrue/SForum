package jobs

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
)

func TestCoreScheduleRegistryBuildsCorePeriodics(t *testing.T) {
	reg, err := NewCoreScheduleRegistry(map[string]river.PeriodicJobConstructor{
		ScheduleIdentityCleanupSessions: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleIdentityCleanupSessions}, nil
		},
		ScheduleAttachmentsCleanupOrphans: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleAttachmentsCleanupOrphans}, nil
		},
		ScheduleAttachmentsReconcileCompression: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleAttachmentsReconcileCompression}, nil
		},
		ScheduleAuditCleanupEvents: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleAuditCleanupEvents}, nil
		},
		ScheduleForumAutoLockIdle: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleForumAutoLockIdle}, nil
		},
		ScheduleForumFlushViewCounts: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleForumFlushViewCounts}, nil
		},
		ScheduleForumBookmarkReminders: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleForumBookmarkReminders}, nil
		},
		ScheduleSearchReconcile: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleSearchReconcile}, nil
		},
		ScheduleExtensionsFinalizeUninstallCleanup: func() (river.JobArgs, *river.InsertOpts) {
			return stubArgs{kind: ScheduleExtensionsFinalizeUninstallCleanup}, nil
		},
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if reg.Len() != 9 {
		t.Fatalf("expected 9 core schedules, got %d", reg.Len())
	}
	jobs, err := reg.BuildPeriodicJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(jobs) != 9 {
		t.Fatalf("expected 9 river periodics, got %d", len(jobs))
	}

	// 元数据完整性：daily、owner 明确
	views := reg.Views()
	byID := map[string]ScheduleView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	for _, id := range []string{
		ScheduleIdentityCleanupSessions,
		ScheduleAttachmentsCleanupOrphans,
		ScheduleAttachmentsReconcileCompression,
		ScheduleAuditCleanupEvents,
		ScheduleForumAutoLockIdle,
		ScheduleForumFlushViewCounts,
		ScheduleSearchReconcile,
		ScheduleExtensionsFinalizeUninstallCleanup,
	} {
		v, ok := byID[id]
		if !ok {
			t.Fatalf("missing schedule %s", id)
		}
		if !v.Enabled {
			t.Fatalf("%s should be enabled", id)
		}
		if v.JobKind == "" || v.Owner == "" {
			t.Fatalf("%s incomplete: %+v", id, v)
		}
	}
	if byID[ScheduleIdentityCleanupSessions].IntervalSeconds != int64((24*time.Hour)/time.Second) {
		t.Fatalf("session cleanup interval=%d", byID[ScheduleIdentityCleanupSessions].IntervalSeconds)
	}
	if byID[ScheduleForumFlushViewCounts].IntervalSeconds != 45 {
		t.Fatalf("flush views interval=%d", byID[ScheduleForumFlushViewCounts].IntervalSeconds)
	}
	if byID[ScheduleSearchReconcile].IntervalSeconds != int64((15*time.Minute)/time.Second) ||
		!byID[ScheduleSearchReconcile].RunOnStart {
		t.Fatalf("search reconcile schedule=%+v", byID[ScheduleSearchReconcile])
	}
	if byID[ScheduleAttachmentsReconcileCompression].IntervalSeconds != 60 ||
		!byID[ScheduleAttachmentsReconcileCompression].RunOnStart {
		t.Fatalf("attachment compression reconcile schedule=%+v", byID[ScheduleAttachmentsReconcileCompression])
	}
	if byID[ScheduleExtensionsFinalizeUninstallCleanup].IntervalSeconds != int64((5*time.Minute)/time.Second) ||
		!byID[ScheduleExtensionsFinalizeUninstallCleanup].RunOnStart {
		t.Fatalf("uninstall cleanup sweep schedule=%+v", byID[ScheduleExtensionsFinalizeUninstallCleanup])
	}
	if byID[ScheduleIdentityCleanupSessions].Queue != QueueDefault {
		t.Fatalf("session cleanup queue=%s", byID[ScheduleIdentityCleanupSessions].Queue)
	}
	if byID[ScheduleAttachmentsCleanupOrphans].Queue != QueueMaintenance {
		t.Fatalf("orphan cleanup queue=%s", byID[ScheduleAttachmentsCleanupOrphans].Queue)
	}
}

func TestCoreScheduleRegistryWithoutConstructorsIsCatalogOnly(t *testing.T) {
	reg, err := NewCoreScheduleRegistry(nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	jobs, err := reg.BuildPeriodicJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("catalog-only should not build periodics, got %d", len(jobs))
	}
	if len(reg.Views()) != 9 {
		t.Fatalf("views=%d", len(reg.Views()))
	}
}
