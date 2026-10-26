package jobs

import (
	"github.com/riverqueue/river"

	"github.com/keeptrue/sforum/apps/api/config"
)

type Config struct {
	CriticalWorkers      int
	DefaultWorkers       int
	SearchWorkers        int
	MailWorkers          int
	NotificationsWorkers int
	MaintenanceWorkers   int
	// AIWorkers 是 AI 生成队列的并发上限。调用慢且会命中供应商限流，
	// 因此默认值远低于其它队列。
	AIWorkers int
}

func FromAppConfig(cfg config.Config) Config {
	return Config{
		CriticalWorkers:      positiveOrDefault(cfg.JobQueueCriticalWorkers, 4),
		DefaultWorkers:       positiveOrDefault(cfg.JobQueueDefaultWorkers, 8),
		SearchWorkers:        positiveOrDefault(cfg.JobQueueSearchWorkers, 6),
		MailWorkers:          positiveOrDefault(cfg.JobQueueMailWorkers, 4),
		NotificationsWorkers: positiveOrDefault(cfg.JobQueueNotificationsWorkers, 6),
		MaintenanceWorkers:   positiveOrDefault(cfg.JobQueueMaintenanceWorkers, 2),
		AIWorkers:            positiveOrDefault(cfg.JobQueueAIWorkers, 2),
	}
}

func (cfg Config) RiverQueues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		QueueCritical:      {MaxWorkers: cfg.CriticalWorkers},
		QueueDefault:       {MaxWorkers: cfg.DefaultWorkers},
		QueueSearch:        {MaxWorkers: cfg.SearchWorkers},
		QueueMail:          {MaxWorkers: cfg.MailWorkers},
		QueueNotifications: {MaxWorkers: cfg.NotificationsWorkers},
		QueueMaintenance:   {MaxWorkers: cfg.MaintenanceWorkers},
		QueueAI:            {MaxWorkers: cfg.AIWorkers},
	}
}

func positiveOrDefault(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
