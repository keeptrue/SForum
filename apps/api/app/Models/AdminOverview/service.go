package adminoverview

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type Store interface {
	Snapshot(ctx context.Context, since time.Time) (StoreSnapshot, error)
}

type RuntimeProvider interface {
	Snapshot() RuntimeStats
}

// ResourceSampler 是可选的轻量资源采样；存在时 Resources 不走完整 Snapshot。
type ResourceSampler interface {
	SampleResources() (resources *RuntimeUsage, disk *DiskRuntimeStats, loadAverage *SystemLoadAverage)
}

type Option func(*Service)

type Service struct {
	store   Store
	runtime RuntimeProvider
	widgets DashboardWidgetProvider
	clock   func() time.Time
}

func NewService(store Store, runtime RuntimeProvider, options ...Option) *Service {
	service := &Service{
		store:   store,
		runtime: runtime,
		clock: func() time.Time {
			return time.Now().UTC()
		},
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

// WithDashboardWidgets 注入扩展仪表盘小部件（F4.3）。
func WithDashboardWidgets(provider DashboardWidgetProvider) Option {
	return func(service *Service) {
		service.widgets = provider
	}
}

// Overview 与 Resources 是同一个读面，门都是 admin.access（RV-46 裁决：保留）。
//
// 依据是「与外壳一致」而不是「与内容敏感度一致」：后台仪表盘路由壳本身只要求
// admin.access，Overview 是它的首屏数据，Resources 是它的实时面板
// （useAdminOverviewLive 轮询 /admin/overview/resources）。把其中一条收窄，会得到
// 「页面 200、面板 403」的半坏页面，而不是更安全的页面。
//
// 更细的现成候选只有 settings.site.manage，那是权限倒挂：把宿主机内存/CPU/磁盘/
// 负载交给非技术岗位（站点运营持该键），同时把技术运维（tech_admin 持 jobs/
// database/extension，无 settings.site.manage）挡在外面。真要按敏感度细分就得新增
// 独立权限键并同步种子角色与迁移，那是产品能力变更，不在本条口径内。
func (s *Service) Overview(ctx context.Context, actor identity.Actor) (AdminOverview, error) {
	if !actor.Can(identity.PermissionAdminAccess) {
		return AdminOverview{}, identity.ErrPermissionDenied
	}

	now := s.clock().UTC()
	since := now.AddDate(0, 0, -(WindowDays - 1))
	snapshot, err := s.store.Snapshot(ctx, since)
	if err != nil {
		return AdminOverview{}, err
	}

	widgets, err := s.listWidgets(ctx)
	if err != nil {
		return AdminOverview{}, err
	}

	return AdminOverview{
		GeneratedAt:      now,
		WindowDays:       WindowDays,
		Runtime:          s.runtimeSnapshot(),
		Community:        snapshot.Community,
		Attachments:      snapshot.Attachments,
		Moderation:       snapshot.Moderation,
		Extensions:       snapshot.Extensions,
		Trends:           TrendStats{Days: snapshot.Trends},
		TopCategories:    snapshot.TopCategories,
		Actions:          overviewActions(snapshot),
		ExtensionWidgets: widgets,
	}, nil
}

// Resources 返回进程内存/CPU、磁盘与系统负载快照，不查询社区 KPI 或扩展小部件。
//
// 门与 Overview 同键（admin.access），理由见 Overview 上方的读面说明：它是同一张
// 仪表盘上的实时面板，不是独立的技术运维入口。
func (s *Service) Resources(_ context.Context, actor identity.Actor) (AdminOverviewResources, error) {
	if !actor.Can(identity.PermissionAdminAccess) {
		return AdminOverviewResources{}, identity.ErrPermissionDenied
	}

	now := s.clock().UTC()
	resources, disk, loadAverage := s.sampleResources()
	return AdminOverviewResources{
		GeneratedAt: now,
		Resources:   resources,
		Disk:        disk,
		LoadAverage: loadAverage,
	}, nil
}

func (s *Service) sampleResources() (*RuntimeUsage, *DiskRuntimeStats, *SystemLoadAverage) {
	if s == nil || s.runtime == nil {
		return nil, nil, nil
	}
	if sampler, ok := s.runtime.(ResourceSampler); ok {
		return sampler.SampleResources()
	}
	stats := s.runtime.Snapshot()
	return stats.Resources, stats.Disk, stats.LoadAverage
}

func (s *Service) listWidgets(ctx context.Context) ([]ExtensionWidget, error) {
	if s == nil || s.widgets == nil {
		return nil, nil
	}
	return s.widgets.DashboardWidgets(ctx)
}

func (s *Service) runtimeSnapshot() RuntimeStats {
	if s.runtime == nil {
		return RuntimeStats{Build: platformversion.Get()}
	}
	stats := s.runtime.Snapshot()
	if stats.Build.Name == "" {
		stats.Build = platformversion.Get()
	}
	return stats
}

func overviewActions(snapshot StoreSnapshot) []OverviewAction {
	return []OverviewAction{
		{
			Key:      ActionModerationQueue,
			Count:    snapshot.Moderation.OpenCount + snapshot.Moderation.ReviewingCount,
			Severity: severityForCount(snapshot.Moderation.OpenCount+snapshot.Moderation.ReviewingCount, "warning"),
			Route:    "/moderation",
		},
		{
			Key:      ActionPendingTags,
			Count:    snapshot.Community.PendingTagCount,
			Severity: severityForCount(snapshot.Community.PendingTagCount, "warning"),
			Route:    "/forum/tags",
		},
		{
			Key:      ActionOrphanAttachments,
			Count:    snapshot.Attachments.OrphanCount,
			Severity: severityForCount(snapshot.Attachments.OrphanCount, "info"),
			Route:    "/attachments",
			// 孤儿附件超过宽限期会被自动清理，不是需要人工处理的积压。
			Advisory: true,
		},
		{
			Key:      ActionFailedExtensionEvents,
			Count:    snapshot.Extensions.FailedEventCount,
			Severity: severityForCount(snapshot.Extensions.FailedEventCount, "danger"),
			Route:    "/extensions/events",
		},
	}
}

func severityForCount(count int64, nonZero string) string {
	if count <= 0 {
		return "success"
	}
	return nonZero
}

type StaticRuntimeProvider struct {
	Stats RuntimeStats
}

func (p StaticRuntimeProvider) Snapshot() RuntimeStats {
	return p.Stats
}

func (p StaticRuntimeProvider) SampleResources() (*RuntimeUsage, *DiskRuntimeStats, *SystemLoadAverage) {
	return p.Stats.Resources, p.Stats.Disk, p.Stats.LoadAverage
}
