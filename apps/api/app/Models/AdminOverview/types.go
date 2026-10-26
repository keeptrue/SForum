package adminoverview

import (
	"context"
	"time"

	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

const (
	WindowDays = 7

	ActionModerationQueue       = "moderation_queue"
	ActionPendingTags           = "pending_tags"
	ActionOrphanAttachments     = "orphan_attachments"
	ActionFailedExtensionEvents = "failed_extension_events"
)

type AdminOverview struct {
	GeneratedAt   time.Time          `json:"generatedAt"`
	WindowDays    int                `json:"windowDays"`
	Runtime       RuntimeStats       `json:"runtime"`
	Community     CommunityStats     `json:"community"`
	Attachments   AttachmentStats    `json:"attachments"`
	Moderation    ModerationStats    `json:"moderation"`
	Extensions    ExtensionStats     `json:"extensions"`
	Trends        TrendStats         `json:"trends"`
	TopCategories []CategoryActivity `json:"topCategories"`
	Actions       []OverviewAction   `json:"actions"`
	// ExtensionWidgets 来自 admin.dashboard.widgets（F4.3）；仅 host-owned admin 路由。
	ExtensionWidgets []ExtensionWidget `json:"extensionWidgets,omitempty"`
}

// AdminOverviewResources 是仪表盘资源卡片的轻量快照（无社区 KPI / 趋势查询）。
// 供高频轮询；采样失败时 resources / disk / loadAverage 可省略。
type AdminOverviewResources struct {
	GeneratedAt time.Time          `json:"generatedAt"`
	Resources   *RuntimeUsage      `json:"resources,omitempty"`
	Disk        *DiskRuntimeStats  `json:"disk,omitempty"`
	LoadAverage *SystemLoadAverage `json:"loadAverage,omitempty"`
}

// ExtensionWidget 是管理后台仪表盘扩展链接卡片。
type ExtensionWidget struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Order       int               `json:"order"`
	Label       map[string]string `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Route       string            `json:"route"`
	Severity    string            `json:"severity"`
}

// DashboardWidgetProvider 解析 admin.dashboard.widgets。
type DashboardWidgetProvider interface {
	DashboardWidgets(ctx context.Context) ([]ExtensionWidget, error)
}

type StoreSnapshot struct {
	Community     CommunityStats
	Attachments   AttachmentStats
	Moderation    ModerationStats
	Extensions    ExtensionStats
	Trends        []TrendDay
	TopCategories []CategoryActivity
}

type RuntimeStats struct {
	StartedAt     time.Time                 `json:"startedAt"`
	UptimeSeconds int64                     `json:"uptimeSeconds"`
	Build         platformversion.BuildInfo `json:"build"`
	// MemoryBytes 是 API 进程常驻内存（RSS，字节）。主 KPI；不再使用 Go MemStats.Sys。
	MemoryBytes uint64 `json:"memoryBytes"`
	// HeapAllocBytes 是当前存活堆对象（Go HeapAlloc）。
	HeapAllocBytes uint64 `json:"heapAllocBytes"`
	// HeapSysBytes 是堆向 OS 申请的虚拟量（Go HeapSys）。
	HeapSysBytes uint64 `json:"heapSysBytes"`
	// SysBytes 是 Go runtime.MemStats.Sys（诊断用，含未归还 arena，通常高于 RSS）。
	SysBytes uint64 `json:"sysBytes"`
	// FamilyMemoryBytes 是本 API 进程 RSS + 其直接拥有的 backend plugin 子进程 RSS。
	// 采样失败时省略；不含 PPID=1 孤儿或其它 API 的插件。
	FamilyMemoryBytes *uint64 `json:"familyMemoryBytes,omitempty"`
	// PluginChildCount 计入全家内存的 owned backend plugin 数量。
	PluginChildCount int `json:"pluginChildCount"`
	// Resources 是一次进程表采样得到的 API、Worker、插件资源分类；采样失败时省略。
	Resources *RuntimeUsage `json:"resources,omitempty"`
	// Disk 是 API 当前工作目录所在文件系统的容量快照；系统不支持时省略。
	Disk *DiskRuntimeStats `json:"disk,omitempty"`
	// LoadAverage 是宿主机 1/5/15 分钟运行队列平均值；系统不支持时省略。
	LoadAverage    *SystemLoadAverage   `json:"loadAverage,omitempty"`
	GoroutineCount int                  `json:"goroutineCount"`
	GCCount        uint32               `json:"gcCount"`
	LastGCPauseNs  uint64               `json:"lastGcPauseNs"`
	Database       DatabaseRuntimeStats `json:"database"`
	// Worker 心跳与队列积压（F1.2）；探测失败时字段可为空。
	Worker   *WorkerRuntimeStats `json:"worker,omitempty"`
	QueueLag *QueueLagStats      `json:"queueLag,omitempty"`
}

// DiskRuntimeStats 描述 API 当前工作目录所在文件系统的使用情况。
type DiskRuntimeStats struct {
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// SystemLoadAverage 是宿主机 1/5/15 分钟运行队列平均值，不是 CPU 百分比。
type SystemLoadAverage struct {
	OneMinute      float64 `json:"oneMinute"`
	FiveMinutes    float64 `json:"fiveMinutes"`
	FifteenMinutes float64 `json:"fifteenMinutes"`
}

// WorkerRuntimeStats 来自 Redis heartbeat（独立或嵌入 worker 共用 key）。
type WorkerRuntimeStats struct {
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`
	Stale      bool       `json:"stale"`
	// Status: ok | stale | unknown
	Status string `json:"status"`
}

// QueueLagStats 廉价聚合：River 中等待执行的 job 数量（available+scheduled+retryable）。
type QueueLagStats struct {
	Waiting int64 `json:"waiting"`
	Running int64 `json:"running"`
	Failed  int64 `json:"failed"`
}

type DatabaseRuntimeStats struct {
	MaxConnections      int32 `json:"maxConnections"`
	TotalConnections    int32 `json:"totalConnections"`
	AcquiredConnections int32 `json:"acquiredConnections"`
	IdleConnections     int32 `json:"idleConnections"`
}

type CommunityStats struct {
	UserCount         int64 `json:"userCount"`
	ActiveUserCount   int64 `json:"activeUserCount"`
	DisabledUserCount int64 `json:"disabledUserCount"`
	BannedUserCount   int64 `json:"bannedUserCount"`
	TopicCount        int64 `json:"topicCount"`
	ActiveTopicCount  int64 `json:"activeTopicCount"`
	LockedTopicCount  int64 `json:"lockedTopicCount"`
	HiddenTopicCount  int64 `json:"hiddenTopicCount"`
	DeletedTopicCount int64 `json:"deletedTopicCount"`
	CommentCount      int64 `json:"commentCount"`
	PostCount         int64 `json:"postCount"`
	CategoryCount     int64 `json:"categoryCount"`
	TagCount          int64 `json:"tagCount"`
	PendingTagCount   int64 `json:"pendingTagCount"`
	TotalViews        int64 `json:"totalViews"`
}

type AttachmentStats struct {
	TotalCount    int64 `json:"totalCount"`
	ActiveCount   int64 `json:"activeCount"`
	DisabledCount int64 `json:"disabledCount"`
	DeletedCount  int64 `json:"deletedCount"`
	OrphanCount   int64 `json:"orphanCount"`
	TotalBytes    int64 `json:"totalBytes"`
}

type ModerationStats struct {
	OpenCount      int64 `json:"openCount"`
	ReviewingCount int64 `json:"reviewingCount"`
	ResolvedCount  int64 `json:"resolvedCount"`
	RejectedCount  int64 `json:"rejectedCount"`
}

type ExtensionStats struct {
	TotalCount                  int64 `json:"totalCount"`
	EnabledCount                int64 `json:"enabledCount"`
	PluginCount                 int64 `json:"pluginCount"`
	ThemeCount                  int64 `json:"themeCount"`
	InstalledPluginRuntimeCount int64 `json:"installedPluginRuntimeCount"`
	FailedEventCount            int64 `json:"failedEventCount"`
}

type TrendStats struct {
	Days []TrendDay `json:"days"`
}

type TrendDay struct {
	Date         string `json:"date"`
	TopicCount   int64  `json:"topicCount"`
	CommentCount int64  `json:"commentCount"`
	UserCount    int64  `json:"userCount"`
}

type CategoryActivity struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	TopicCount   int64  `json:"topicCount"`
	CommentCount int64  `json:"commentCount"`
}

type OverviewAction struct {
	Key      string `json:"key"`
	Count    int64  `json:"count"`
	Severity string `json:"severity"`
	Route    string `json:"route"`
}
