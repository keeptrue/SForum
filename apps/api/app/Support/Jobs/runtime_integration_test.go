package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type unregisteredIntegrationArgs struct {
	ID int64 `json:"id"`
}

func (unregisteredIntegrationArgs) Kind() string {
	return "test.unregistered_integration"
}

func TestInsertOnlyClientAllowsUnregisteredJobKinds(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		t.Fatalf("create river migrator: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("run river migrations: %v", err)
	}

	cfg := Config{
		CriticalWorkers:      1,
		DefaultWorkers:       1,
		SearchWorkers:        1,
		MailWorkers:          1,
		NotificationsWorkers: 1,
		MaintenanceWorkers:   1,
		AIWorkers:            1,
	}
	args := unregisteredIntegrationArgs{ID: time.Now().UnixNano()}
	checkedClient, err := NewClient(pool, cfg, river.NewWorkers())
	if err != nil {
		t.Fatalf("create checked client: %v", err)
	}
	_, err = NewDispatcher(checkedClient).Enqueue(ctx, args, EnqueueOptions{Queue: QueueMaintenance})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected empty workers bundle to reject unknown job kind, got %v", err)
	}

	insertOnlyClient, err := NewInsertOnlyClient(pool, cfg)
	if err != nil {
		t.Fatalf("create insert-only client: %v", err)
	}
	if _, err := NewDispatcher(insertOnlyClient).Enqueue(ctx, args, EnqueueOptions{Queue: QueueMaintenance}); err != nil {
		t.Fatalf("expected insert-only client to enqueue unregistered kind: %v", err)
	}
}
