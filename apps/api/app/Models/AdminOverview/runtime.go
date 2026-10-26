package adminoverview

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	processmemory "github.com/keeptrue/sforum/apps/api/app/Support/ProcessMemory"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type RuntimeCollector struct {
	startedAt    time.Time
	pool         *pgxpool.Pool
	heartbeat    health.HeartbeatStore
	queueLagPool *pgxpool.Pool
	staleAfter   time.Duration
	now          func() time.Time
	// sampler 可注入；nil 时使用 defaultProcessSampler。
	sampler           ProcessSampler
	diskPath          string
	diskSampler       func(string) (DiskRuntimeStats, bool)
	loadSampler       func() (SystemLoadAverage, bool)
	usageWindow       *processmemory.UsageWindow
	workerEmbedded    bool
	workerConcurrency int
}

func NewRuntimeCollector(startedAt time.Time, pool *pgxpool.Pool) RuntimeCollector {
	return RuntimeCollector{
		startedAt: startedAt.UTC(), pool: pool, staleAfter: health.DefaultHeartbeatStaleAfter,
		now: time.Now, usageWindow: processmemory.NewUsageWindow(processmemory.DefaultUsageWindow),
	}
}

// WithHeartbeat 注入 worker last_seen 读取（Redis）。
func (c RuntimeCollector) WithHeartbeat(store health.HeartbeatStore) RuntimeCollector {
	c.heartbeat = store
	return c
}

// WithQueueLag 启用廉价 River 队列积压统计（共用 PG pool）。
func (c RuntimeCollector) WithQueueLag(pool *pgxpool.Pool) RuntimeCollector {
	c.queueLagPool = pool
	return c
}

// WithWorkerRuntime 记录 Worker 是否与 API 同进程，以及 River 配置的总并发槽。
// 内嵌模式不伪造独立内存，只为管理端提供可解释的运行状态。
func (c RuntimeCollector) WithWorkerRuntime(embedded bool, concurrency int) RuntimeCollector {
	c.workerEmbedded = embedded
	c.workerConcurrency = max(concurrency, 0)
	return c
}

// WithProcessSampler 注入进程内存采样（测试用）。
func (c RuntimeCollector) WithProcessSampler(sampler ProcessSampler) RuntimeCollector {
	c.sampler = sampler
	return c
}

// WithDiskPath 注入磁盘统计路径，生产环境默认使用 API 当前工作目录。
func (c RuntimeCollector) WithDiskPath(path string) RuntimeCollector {
	c.diskPath = path
	return c
}

// WithDiskSampler 注入磁盘采样器，供不依赖真实文件系统的测试使用。
func (c RuntimeCollector) WithDiskSampler(sampler func(string) (DiskRuntimeStats, bool)) RuntimeCollector {
	c.diskSampler = sampler
	return c
}

// WithLoadSampler 注入系统负载采样器，供不依赖宿主机状态的测试使用。
func (c RuntimeCollector) WithLoadSampler(sampler func() (SystemLoadAverage, bool)) RuntimeCollector {
	c.loadSampler = sampler
	return c
}

func (c RuntimeCollector) Snapshot() RuntimeStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	lastPause := uint64(0)
	if stats.NumGC > 0 {
		lastPause = stats.PauseNs[(stats.NumGC+255)%256]
	}

	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}

	resourcesPtr, diskPtr, loadPtr, memoryBytes, familyPtr, pluginChildren := c.sampleProcessDiskAndLoad(stats.Sys)

	return RuntimeStats{
		StartedAt:         c.startedAt,
		UptimeSeconds:     int64(time.Since(c.startedAt).Seconds()),
		Build:             platformversion.Get(),
		MemoryBytes:       memoryBytes,
		HeapAllocBytes:    stats.HeapAlloc,
		HeapSysBytes:      stats.HeapSys,
		SysBytes:          stats.Sys,
		FamilyMemoryBytes: familyPtr,
		PluginChildCount:  pluginChildren,
		Resources:         resourcesPtr,
		Disk:              diskPtr,
		LoadAverage:       loadPtr,
		GoroutineCount:    runtime.NumGoroutine(),
		GCCount:           stats.NumGC,
		LastGCPauseNs:     lastPause,
		Database:          c.databaseStats(),
		Worker:            c.workerStats(nowFn().UTC()),
		QueueLag:          c.queueLagStats(),
	}
}

// SampleResources 只采样进程内存/CPU、磁盘与系统负载，不访问 Redis / River / 社区 KPI。
// 供仪表盘资源卡片高频轮询。
func (c RuntimeCollector) SampleResources() (*RuntimeUsage, *DiskRuntimeStats, *SystemLoadAverage) {
	resources, disk, load, _, _, _ := c.sampleProcessDiskAndLoad(0)
	return resources, disk, load
}

func (c RuntimeCollector) sampleProcessDiskAndLoad(sysFallback uint64) (
	resourcesPtr *RuntimeUsage,
	diskPtr *DiskRuntimeStats,
	loadPtr *SystemLoadAverage,
	memoryBytes uint64,
	familyPtr *uint64,
	pluginChildren int,
) {
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultProcessSampler
	}

	if resources, ok := sampleRuntimeUsage(sampler); ok {
		resources.WorkerEmbedded = c.workerEmbedded
		resources.WorkerConcurrency = c.workerConcurrency
		if c.usageWindow != nil {
			resources = c.usageWindow.Observe(resources)
		}
		memoryBytes = resources.APIMemoryBytes
		pluginChildren = resources.APIOwnedPluginCount
		family := resources.APIMemoryBytes + resources.APIOwnedPluginMemoryBytes
		familyPtr = &family
		resourcesCopy := resources
		resourcesPtr = &resourcesCopy
	} else if fallback, ok := readSelfRSSFallback(); ok {
		// ps 失败时 Linux 仍可读 /proc/self/statm；全家内存省略。
		memoryBytes = fallback
	} else if sysFallback > 0 {
		// 最后回退：避免主 KPI 为 0；语义上仍暴露 Sys 诊断字段。
		memoryBytes = sysFallback
	}

	diskPath := c.diskPath
	if diskPath == "" {
		diskPath, _ = os.Getwd()
	}
	diskSampler := c.diskSampler
	if diskSampler == nil {
		diskSampler = sampleDiskUsage
	}
	if disk, ok := diskSampler(diskPath); ok {
		diskCopy := disk
		diskPtr = &diskCopy
	}

	loadSampler := c.loadSampler
	if loadSampler == nil {
		loadSampler = sampleSystemLoad
	}
	if average, ok := loadSampler(); ok {
		averageCopy := average
		loadPtr = &averageCopy
	}
	return resourcesPtr, diskPtr, loadPtr, memoryBytes, familyPtr, pluginChildren
}

func (c RuntimeCollector) databaseStats() DatabaseRuntimeStats {
	if c.pool == nil {
		return DatabaseRuntimeStats{}
	}
	stats := c.pool.Stat()
	return DatabaseRuntimeStats{
		MaxConnections:      stats.MaxConns(),
		TotalConnections:    stats.TotalConns(),
		AcquiredConnections: stats.AcquiredConns(),
		IdleConnections:     stats.IdleConns(),
	}
}

func (c RuntimeCollector) workerStats(now time.Time) *WorkerRuntimeStats {
	if c.heartbeat == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	lastSeen, found, err := c.heartbeat.LastSeen(ctx)
	if err != nil {
		// Redis 读失败：标 unknown，避免 overview 整体失败。
		return &WorkerRuntimeStats{Stale: true, Status: "unknown"}
	}
	observed := health.ObserveHeartbeat(lastSeen, found, now, c.staleAfter)
	return &WorkerRuntimeStats{
		LastSeenAt: observed.LastSeenAt,
		AgeSeconds: observed.AgeSeconds,
		Stale:      observed.Stale,
		Status:     observed.Status,
	}
}

func (c RuntimeCollector) queueLagStats() *QueueLagStats {
	pool := c.queueLagPool
	if pool == nil {
		pool = c.pool
	}
	if pool == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var waiting, running, failed int64
	// River 官方表；失败时静默返回 nil，不拖垮 overview。
	err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE state IN ('available', 'scheduled', 'retryable', 'pending')),
			count(*) FILTER (WHERE state = 'running'),
			count(*) FILTER (WHERE state = 'discarded')
		FROM river_job
	`).Scan(&waiting, &running, &failed)
	if err != nil {
		return nil
	}
	return &QueueLagStats{Waiting: waiting, Running: running, Failed: failed}
}
