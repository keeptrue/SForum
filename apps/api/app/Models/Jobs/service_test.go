package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestServiceEnforcesViewAndManagePermissionsBeforeDataAccess(t *testing.T) {
	service := NewService(nil, nil)
	actor := identity.Actor{ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
	if _, err := service.Overview(context.Background(), actor); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("overview permission: %v", err)
	}
	if _, err := service.List(context.Background(), actor, ListInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("list permission: %v", err)
	}
	if _, err := service.Detail(context.Background(), actor, 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("detail permission: %v", err)
	}
	if _, err := service.Schedules(context.Background(), actor); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("schedules permission: %v", err)
	}
	if _, err := service.SetScheduleEnabled(context.Background(), actor, "identity.cleanup_sessions", false); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("set enabled permission: %v", err)
	}
	if _, err := service.TriggerSchedule(context.Background(), actor, "identity.cleanup_sessions"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("trigger permission: %v", err)
	}
	if _, err := service.Retry(context.Background(), actor, 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("retry permission: %v", err)
	}
	if _, err := service.Cancel(context.Background(), actor, 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("cancel permission: %v", err)
	}
	if err := service.SetQueuePaused(context.Background(), actor, "default", true); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("pause permission: %v", err)
	}
}

// assertCoreScheduleCatalog 把服务返回的目录与**注册表投影**按集合比对，双向报差异。
//
// 钉的是「服务投影 ⇄ 注册表」这条真实关系：`Schedules()` 若按 owner 裁剪、只回
// enabled、按权限过滤、把两条合成一条、或漏写一条没进 out，这里就红并指名 ID。
// 所以本文件**故意不写成员清单**——新增/删除核心调度时这里零改动。
//
// 「目录成员变了、必须有人看一眼」的审查闸口**不在这里**，在
// `app/Support/Jobs/core_schedules_test.go` 的那份手写清单（注册表 ⇄ 常量）。
// 别把清单搬回来：成员表写两处正是 P2-6 / P3-9 连续两轮漏同步的根因——两个包共享
// 不了测试变量，镜像迟早再飘一次。这里要的是属性，不是抄写。
//
// 重复 ID 由末尾的条数比对兜住（集合会吞掉重复，条数不会）。
func assertCoreScheduleCatalog(t *testing.T, items []Schedule) {
	t.Helper()
	registry, err := supportjobs.NewCoreScheduleRegistry(nil)
	if err != nil {
		t.Fatalf("core schedule registry: %v", err)
	}
	views := registry.Views()
	got := make(map[string]struct{}, len(items))
	for _, item := range items {
		got[item.ID] = struct{}{}
	}
	want := make(map[string]struct{}, len(views))
	for _, view := range views {
		want[view.ID] = struct{}{}
		if _, exists := got[view.ID]; !exists {
			t.Errorf("服务目录缺少 schedule %q（注册表里有，Schedules() 没暴露：过滤/去重/漏项）", view.ID)
		}
	}
	for id := range got {
		if _, exists := want[id]; !exists {
			t.Errorf("服务目录多出 schedule %q（注册表里没有对应登记，投影凭空造了一条）", id)
		}
	}
	if len(items) != len(views) {
		t.Errorf("schedule 数=%d，注册表=%d（重复暴露也会落在这里）", len(items), len(views))
	}
}

func TestServiceSchedulesReturnsCoreCatalog(t *testing.T) {
	service := NewService(nil, nil)
	actor := identity.Actor{
		ID:     1,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionJobsView: true,
		},
	}
	items, err := service.Schedules(context.Background(), actor)
	if err != nil {
		t.Fatalf("schedules: %v", err)
	}
	assertCoreScheduleCatalog(t, items)
	for _, item := range items {
		if item.JobKind == "" || item.Owner == "" || item.IntervalSeconds <= 0 {
			t.Fatalf("incomplete schedule: %+v", item)
		}
		if !item.Enabled {
			t.Fatalf("catalog default should be enabled: %s", item.ID)
		}
		// 无历史时 next 应有粗估值
		if item.NextRunAt == nil {
			t.Fatalf("expected nextRunAt for enabled schedule %s", item.ID)
		}
	}
}

type memoryOptionStore struct {
	values map[string]string
}

func (m *memoryOptionStore) Get(_ context.Context, name string) (string, bool, error) {
	if m.values == nil {
		return "", false, nil
	}
	v, ok := m.values[name]
	return v, ok, nil
}

func (m *memoryOptionStore) Set(_ context.Context, name, value string) error {
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[name] = value
	return nil
}

type stubJobArgs struct{}

func (stubJobArgs) Kind() string { return "identity.cleanup_sessions" }

func TestServiceSetScheduleEnabledPersistsOption(t *testing.T) {
	store := &memoryOptionStore{}
	service := NewService(nil, nil).WithScheduleOptions(store, nil)
	actor := identity.Actor{
		ID:     1,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionJobsManage: true,
			identity.PermissionJobsView:   true,
		},
	}
	item, err := service.SetScheduleEnabled(context.Background(), actor, supportjobs.ScheduleIdentityCleanupSessions, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if item.Enabled {
		t.Fatal("expected disabled")
	}
	key := supportjobs.ScheduleEnabledOptionName(supportjobs.ScheduleIdentityCleanupSessions)
	if store.values[key] != "false" {
		t.Fatalf("option=%q", store.values[key])
	}
	// 列表应反映 option
	list, err := service.Schedules(context.Background(), actor)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, s := range list {
		if s.ID == supportjobs.ScheduleIdentityCleanupSessions {
			if s.Enabled {
				t.Fatal("list should show disabled")
			}
			if s.NextRunAt != nil {
				t.Fatal("disabled schedule should omit nextRunAt")
			}
			return
		}
	}
	t.Fatal("schedule missing from list")
}

func TestServiceSetScheduleEnabledUnknown(t *testing.T) {
	service := NewService(nil, nil).WithScheduleOptions(&memoryOptionStore{}, nil)
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionJobsManage: true},
	}
	if _, err := service.SetScheduleEnabled(context.Background(), actor, "not.a.schedule", false); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceTriggerScheduleDisabled(t *testing.T) {
	store := &memoryOptionStore{values: map[string]string{
		supportjobs.ScheduleEnabledOptionName(supportjobs.ScheduleIdentityCleanupSessions): "false",
	}}
	service := NewService(nil, nil).WithScheduleOptions(store, map[string]ScheduleConstructor{
		supportjobs.ScheduleIdentityCleanupSessions: func() (river.JobArgs, *river.InsertOpts) {
			return stubJobArgs{}, nil
		},
	})
	actor := identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionJobsManage: true},
	}
	if _, err := service.TriggerSchedule(context.Background(), actor, supportjobs.ScheduleIdentityCleanupSessions); !errors.Is(err, ErrScheduleDisabled) {
		t.Fatalf("err=%v", err)
	}
}

func TestEstimateNextRun(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour

	if got := estimateNextRun(false, interval, nil, now); got != nil {
		t.Fatal("disabled should have no next")
	}
	next := estimateNextRun(true, interval, nil, now)
	if next == nil || !next.Equal(now.Add(interval)) {
		t.Fatalf("no-last next=%v", next)
	}
	last := now.Add(-2 * time.Hour)
	next = estimateNextRun(true, interval, &last, now)
	// last+interval 在未来
	if next == nil || !next.Equal(last.Add(interval)) {
		t.Fatalf("future next=%v", next)
	}
	// last 很久以前：next 钳到 now
	old := now.Add(-48 * time.Hour)
	next = estimateNextRun(true, interval, &old, now)
	if next == nil || !next.Equal(now) {
		t.Fatalf("overdue next=%v", next)
	}
}
