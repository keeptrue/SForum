package jobs

import (
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/keeptrue/sforum/apps/api/config"
)

func TestFromAppConfigBuildsRiverQueues(t *testing.T) {
	cfg := FromAppConfig(config.Config{
		JobQueueCriticalWorkers:      1,
		JobQueueDefaultWorkers:       2,
		JobQueueSearchWorkers:        3,
		JobQueueMailWorkers:          4,
		JobQueueNotificationsWorkers: 5,
		JobQueueMaintenanceWorkers:   6,
	})

	queues := cfg.RiverQueues()

	assertWorkers(t, queues, QueueCritical, 1)
	assertWorkers(t, queues, QueueDefault, 2)
	assertWorkers(t, queues, QueueSearch, 3)
	assertWorkers(t, queues, QueueMail, 4)
	assertWorkers(t, queues, QueueNotifications, 5)
	assertWorkers(t, queues, QueueMaintenance, 6)
}

func TestFromAppConfigUsesSafeDefaults(t *testing.T) {
	cfg := FromAppConfig(config.Config{})

	queues := cfg.RiverQueues()

	assertWorkers(t, queues, QueueCritical, 4)
	assertWorkers(t, queues, QueueDefault, 8)
	assertWorkers(t, queues, QueueSearch, 6)
	assertWorkers(t, queues, QueueMail, 4)
	assertWorkers(t, queues, QueueNotifications, 6)
	assertWorkers(t, queues, QueueMaintenance, 2)
}

func TestEnqueueOptionsConvertToRiverInsertOpts(t *testing.T) {
	runAt := time.Now().UTC().Add(time.Hour)

	opts := EnqueueOptions{
		Queue:       QueueSearch,
		MaxAttempts: 3,
		ScheduledAt: runAt,
		Unique:      river.UniqueOpts{ByArgs: true},
	}.RiverInsertOpts()

	if opts.Queue != QueueSearch {
		t.Fatalf("expected search queue, got %q", opts.Queue)
	}
	if opts.MaxAttempts != 3 {
		t.Fatalf("expected max attempts 3, got %d", opts.MaxAttempts)
	}
	if !opts.ScheduledAt.Equal(runAt) {
		t.Fatalf("expected scheduled time %s, got %s", runAt, opts.ScheduledAt)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("expected unique by args")
	}
}

func assertWorkers(t *testing.T, queues map[string]river.QueueConfig, name string, expected int) {
	t.Helper()

	queue, ok := queues[name]
	if !ok {
		t.Fatalf("expected queue %q to exist", name)
	}
	if queue.MaxWorkers != expected {
		t.Fatalf("expected queue %q workers %d, got %d", name, expected, queue.MaxWorkers)
	}
}
