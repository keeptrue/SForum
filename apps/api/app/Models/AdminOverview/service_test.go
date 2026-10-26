package adminoverview

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestServiceRequiresAdminAccess(t *testing.T) {
	service := NewService(&fakeStore{}, StaticRuntimeProvider{})
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.Overview(context.Background(), actor)

	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}

	_, err = service.Resources(context.Background(), actor)
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected resources permission denied, got %v", err)
	}
}

func TestServiceBuildsOverviewFromStoreAndRuntime(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 30, 0, 0, time.UTC)
	service := NewService(&fakeStore{snapshot: StoreSnapshot{
		Community: CommunityStats{
			UserCount:        12,
			ActiveUserCount:  10,
			TopicCount:       31,
			ActiveTopicCount: 29,
			CommentCount:     88,
			PostCount:        119,
			PendingTagCount:  2,
		},
		Attachments: AttachmentStats{
			TotalCount:  24,
			OrphanCount: 3,
		},
		Moderation: ModerationStats{
			OpenCount:      4,
			ReviewingCount: 1,
		},
		Extensions: ExtensionStats{
			TotalCount:                  5,
			EnabledCount:                3,
			FailedEventCount:            2,
			InstalledPluginRuntimeCount: 2,
		},
		Trends: []TrendDay{
			{Date: "2026-07-08", TopicCount: 3, CommentCount: 9, UserCount: 1},
		},
		TopCategories: []CategoryActivity{
			{ID: 1, Slug: "general", Name: "综合讨论", TopicCount: 12, CommentCount: 33},
		},
	}}, StaticRuntimeProvider{Stats: RuntimeStats{
		StartedAt:        now.Add(-2 * time.Hour),
		UptimeSeconds:    7200,
		MemoryBytes:      128 * 1024 * 1024,
		HeapAllocBytes:   32 * 1024 * 1024,
		SysBytes:         256 * 1024 * 1024,
		PluginChildCount: 2,
		GoroutineCount:   19,
		GCCount:          7,
		Database:         DatabaseRuntimeStats{MaxConnections: 16, TotalConnections: 4, AcquiredConnections: 1, IdleConnections: 3},
	}}, WithClock(func() time.Time { return now }))

	overview, err := service.Overview(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Overview returned error: %v", err)
	}

	if overview.WindowDays != 7 {
		t.Fatalf("expected 7 day window, got %d", overview.WindowDays)
	}
	if !overview.GeneratedAt.Equal(now) {
		t.Fatalf("expected generatedAt from clock, got %s", overview.GeneratedAt)
	}
	if overview.Runtime.MemoryBytes == 0 || overview.Runtime.HeapAllocBytes == 0 || overview.Runtime.GoroutineCount == 0 {
		t.Fatalf("expected non-zero runtime stats, got %#v", overview.Runtime)
	}
	if overview.Community.TopicCount != 31 || overview.Community.UserCount != 12 {
		t.Fatalf("unexpected community stats: %#v", overview.Community)
	}
	if len(overview.Actions) != 4 {
		t.Fatalf("expected four actionable summaries, got %#v", overview.Actions)
	}
	if overview.Actions[0].Key != ActionModerationQueue || overview.Actions[0].Count != 5 || overview.Actions[0].Route != "/moderation" {
		t.Fatalf("expected moderation action first, got %#v", overview.Actions)
	}
}

func TestServiceResourcesReturnsLightweightRuntimeOnly(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	usage := RuntimeUsage{
		APIMemoryBytes:   64 * 1024 * 1024,
		TotalMemoryBytes: 64 * 1024 * 1024,
		APICPUPercent:    3.5,
		TotalCPUPercent:  3.5,
	}
	disk := DiskRuntimeStats{
		TotalBytes:  1000,
		UsedBytes:   400,
		FreeBytes:   600,
		UsedPercent: 40,
	}
	loadAverage := SystemLoadAverage{OneMinute: 0.25, FiveMinutes: 0.5, FifteenMinutes: 0.75}
	store := &countingStore{}
	service := NewService(store, StaticRuntimeProvider{Stats: RuntimeStats{
		Resources:   &usage,
		Disk:        &disk,
		LoadAverage: &loadAverage,
	}}, WithClock(func() time.Time { return now }))

	payload, err := service.Resources(context.Background(), adminActor())
	if err != nil {
		t.Fatalf("Resources returned error: %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("expected zero store calls, got %d", store.calls)
	}
	if !payload.GeneratedAt.Equal(now) {
		t.Fatalf("generatedAt=%s", payload.GeneratedAt)
	}
	if payload.Resources == nil || payload.Resources.APIMemoryBytes != usage.APIMemoryBytes {
		t.Fatalf("resources=%#v", payload.Resources)
	}
	if payload.Disk == nil || payload.Disk.UsedPercent != 40 {
		t.Fatalf("disk=%#v", payload.Disk)
	}
	if payload.LoadAverage == nil || payload.LoadAverage.FiveMinutes != 0.5 {
		t.Fatalf("loadAverage=%#v", payload.LoadAverage)
	}
}

func TestRuntimeCollectorReturnsProcessStats(t *testing.T) {
	collector := NewRuntimeCollector(time.Now().Add(-time.Minute), nil)

	stats := collector.Snapshot()

	if stats.MemoryBytes == 0 {
		t.Fatal("expected memory bytes (RSS or fallback) to be non-zero")
	}
	if stats.HeapAllocBytes == 0 {
		t.Fatal("expected heap alloc bytes to be non-zero")
	}
	if stats.SysBytes == 0 {
		t.Fatal("expected Go Sys diagnostic bytes to be non-zero")
	}
	if stats.GoroutineCount == 0 {
		t.Fatal("expected goroutine count to be non-zero")
	}
	if stats.Build.Name != "SForum" || stats.Build.Version == "" || stats.Build.GoVersion == "" || stats.Build.Platform == "" {
		t.Fatalf("expected complete build identity, got %#v", stats.Build)
	}
	// 真实采样成功时 family >= parent；失败时 family 可省略。
	if stats.FamilyMemoryBytes != nil && *stats.FamilyMemoryBytes < stats.MemoryBytes {
		t.Fatalf("family RSS %d < parent RSS %d", *stats.FamilyMemoryBytes, stats.MemoryBytes)
	}
}

func TestRuntimeCollectorSampleResourcesSkipsWorkerAndQueue(t *testing.T) {
	collector := NewRuntimeCollector(time.Now().Add(-time.Minute), nil).WithHeartbeat(memoryHeartbeat{found: false})
	resources, disk, loadAverage := collector.SampleResources()
	// 不依赖真实 ps/磁盘是否成功，只保证方法可调用且不 panic。
	_ = resources
	_ = disk
	_ = loadAverage
	full := collector.Snapshot()
	if full.Worker == nil {
		t.Fatal("full snapshot should still include worker probe")
	}
}

type memoryHeartbeat struct {
	at    time.Time
	found bool
	err   error
}

func (m memoryHeartbeat) Touch(context.Context, time.Time) error { return nil }
func (m memoryHeartbeat) LastSeen(context.Context) (time.Time, bool, error) {
	return m.at, m.found, m.err
}

func TestRuntimeCollectorIncludesWorkerHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	collector := NewRuntimeCollector(now.Add(-time.Hour), nil).WithHeartbeat(memoryHeartbeat{
		at:    now.Add(-5 * time.Second),
		found: true,
	})
	collector.now = func() time.Time { return now }

	stats := collector.Snapshot()
	if stats.Worker == nil {
		t.Fatal("expected worker stats")
	}
	if stats.Worker.Stale || stats.Worker.Status != "ok" {
		t.Fatalf("worker=%#v", stats.Worker)
	}
}

func TestRuntimeCollectorMarksMissingHeartbeatUnknown(t *testing.T) {
	collector := NewRuntimeCollector(time.Now(), nil).WithHeartbeat(memoryHeartbeat{found: false})
	stats := collector.Snapshot()
	if stats.Worker == nil || stats.Worker.Status != "unknown" || !stats.Worker.Stale {
		t.Fatalf("worker=%#v", stats.Worker)
	}
}

func adminActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAdminAccess: true},
	}
}

type fakeStore struct {
	snapshot StoreSnapshot
}

func (s *fakeStore) Snapshot(context.Context, time.Time) (StoreSnapshot, error) {
	return s.snapshot, nil
}

// countingStore 用于确认 Resources 不触达社区 KPI 查询。
type countingStore struct {
	calls int
}

func (s *countingStore) Snapshot(context.Context, time.Time) (StoreSnapshot, error) {
	s.calls++
	return StoreSnapshot{}, errors.New("store should not be called for resources")
}
