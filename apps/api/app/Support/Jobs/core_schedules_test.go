package jobs

import (
	"testing"
	"time"

	"github.com/riverqueue/river"
)

// coreScheduleExpectedIDs 是注册表里必须存在的 schedule 常量清单（**逐项点名**）。
//
// 为什么不用 `len(reg.Views()) != N` 的裸计数：裸计数只把红推后一次——下一条
// schedule 落地时又会这么红一次，而红的时候只说「10 != 9」，不告诉任何人多了哪条。
// 逐项比对表达的是「注册表 ⇄ 常量目录」的对应关系本身：漏注册、多注册、改名都红，
// 并且指名道姓。
//
// 第 10 条是 M4b-2 的 trustlevels.sweep：24h、maintenance 队列、`mode=stale`
// （只碰账本缺失或 24h 没算过的人），兜住角色变更 / OAuth 解绑 / 徽章收回这类
// 没有活动事件、也就不会经过 6 个活动触发点的等级变化。
var coreScheduleExpectedIDs = []string{
	ScheduleIdentityCleanupSessions,
	ScheduleAttachmentsCleanupOrphans,
	ScheduleAttachmentsReconcileCompression,
	ScheduleAuditCleanupEvents,
	ScheduleForumAutoLockIdle,
	ScheduleForumBookmarkReminders,
	ScheduleForumFlushViewCounts,
	ScheduleSearchReconcile,
	ScheduleExtensionsFinalizeUninstallCleanup,
	ScheduleTrustLevelsSweep,
}

// assertCoreScheduleSet 把「注册表里的视图」与上面的清单按集合比对，双向报差异。
func assertCoreScheduleSet(t *testing.T, views []ScheduleView) {
	t.Helper()
	got := make(map[string]struct{}, len(views))
	for _, view := range views {
		got[view.ID] = struct{}{}
	}
	want := make(map[string]struct{}, len(coreScheduleExpectedIDs))
	for _, id := range coreScheduleExpectedIDs {
		want[id] = struct{}{}
		if _, exists := got[id]; !exists {
			t.Errorf("注册表缺少 schedule %q（常量在、目录里没有注册）", id)
		}
	}
	for id := range got {
		if _, exists := want[id]; !exists {
			t.Errorf("注册表多出 schedule %q（目录里没有对应常量，清单需要更新）", id)
		}
	}
	if len(views) != len(coreScheduleExpectedIDs) {
		t.Errorf("schedule 数=%d，清单=%d", len(views), len(coreScheduleExpectedIDs))
	}
}

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
	// 逐项比对，而不是裸计数：见 coreScheduleExpectedIDs 的注释。
	assertCoreScheduleSet(t, reg.Views())
	if reg.Len() != len(coreScheduleExpectedIDs) {
		t.Fatalf("registry Len=%d，清单=%d", reg.Len(), len(coreScheduleExpectedIDs))
	}
	jobs, err := reg.BuildPeriodicJobs()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 本用例只给 9 条注入了 constructor（没有 trustlevels.sweep），
	// 所以 periodic 数 = 注入数，而 views 数 = 目录数。
	if len(jobs) != len(coreScheduleExpectedIDs)-1 {
		t.Fatalf("river periodics=%d，期望 %d（清单减去未注入 constructor 的那条）", len(jobs), len(coreScheduleExpectedIDs)-1)
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
		ScheduleTrustLevelsSweep,
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
	// M4b-2 的信任等级 sweep：24h、maintenance 队列、owner=trustlevels。
	// 它跑的是 `mode=stale`（只碰账本缺失或 24h 没算过的人），不是全表重算。
	trustSweep := byID[ScheduleTrustLevelsSweep]
	if trustSweep.IntervalSeconds != int64((24*time.Hour)/time.Second) ||
		trustSweep.Queue != QueueMaintenance || trustSweep.Owner != "trustlevels" {
		t.Fatalf("trust level sweep schedule=%+v", trustSweep)
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
	assertCoreScheduleSet(t, reg.Views())
}
