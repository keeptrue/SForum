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
