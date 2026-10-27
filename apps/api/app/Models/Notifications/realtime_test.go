package notifications

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestRevisionHubMultiNodeWakeAndListenerReconnectPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stamp := time.Now().UnixNano()
	newNode := func(label string) (*pgxpool.Pool, *RevisionHub) {
		config, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		config.MaxConns = 1
		config.ConnConfig.RuntimeParams["application_name"] = fmt.Sprintf("sforum_notification_hub_%s_%d", label, stamp)
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		hub := NewRevisionHub(ctx, pool)
		t.Cleanup(hub.Close)
		return pool, hub
	}
	_, first := newNode("first")
	_, second := newNode("second")
	control, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.Close)

	userID := stamp%1_000_000_000 + 1
	firstWake, releaseFirst, err := first.Subscribe(userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseFirst)
	secondWake, releaseSecond, err := second.Subscribe(userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseSecond)

	wakeBoth := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		firstSeen, secondSeen := false, false
		for time.Now().Before(deadline) && (!firstSeen || !secondSeen) {
			if _, err := control.Exec(ctx, `SELECT pg_notify('sforum_notification_revision', $1)`, fmt.Sprint(userID)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-firstWake:
				firstSeen = true
			case <-time.After(40 * time.Millisecond):
			}
			select {
			case <-secondWake:
				secondSeen = true
			case <-time.After(40 * time.Millisecond):
			}
		}
		if !firstSeen || !secondSeen {
			t.Fatalf("multi-node wake first=%t second=%t", firstSeen, secondSeen)
		}
	}
	wakeBoth()

	var terminated bool
	if err := control.QueryRow(ctx, `SELECT pg_terminate_backend(pid)
FROM pg_stat_activity WHERE application_name=$1 ORDER BY backend_start DESC LIMIT 1`,
		fmt.Sprintf("sforum_notification_hub_first_%d", stamp)).Scan(&terminated); err != nil {
		t.Fatal(err)
	}
	if !terminated {
		t.Fatal("test listener was not terminated")
	}
	wakeBoth()
}

func TestRevisionHubEnforcesPerRecipientLimitAndReleases(t *testing.T) {
	hub := &RevisionHub{pool: &pgxpool.Pool{}, byUser: make(map[int64]map[uint64]chan struct{})}
	releases := make([]func(), 0, maxRevisionConnectionsPerUser)
	for range maxRevisionConnectionsPerUser {
		_, release, err := hub.Subscribe(42)
		if err != nil {
			t.Fatalf("subscribe below limit: %v", err)
		}
		releases = append(releases, release)
	}
	if _, _, err := hub.Subscribe(42); err != ErrRevisionConnectionLimit {
		t.Fatalf("fifth recipient connection error=%v", err)
	}

	releases[0]()
	releases[0]() // release is idempotent
	if _, release, err := hub.Subscribe(42); err != nil {
		t.Fatalf("subscribe after release: %v", err)
	} else {
		release()
	}
}

func TestRevisionHubCoalescesWakeHintsAndIsolatesRecipients(t *testing.T) {
	hub := &RevisionHub{pool: &pgxpool.Pool{}, byUser: make(map[int64]map[uint64]chan struct{})}
	userWake, releaseUser, err := hub.Subscribe(42)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseUser()
	otherWake, releaseOther, err := hub.Subscribe(99)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()

	hub.publish(42)
	hub.publish(42)
	if len(userWake) != 1 {
		t.Fatalf("coalesced wake count=%d want=1", len(userWake))
	}
	if len(otherWake) != 0 {
		t.Fatalf("wake leaked to another recipient")
	}
}

func TestRevisionHubEnforcesProcessConnectionLimit(t *testing.T) {
	hub := &RevisionHub{
		pool:   &pgxpool.Pool{},
		byUser: make(map[int64]map[uint64]chan struct{}),
		total:  maxRevisionConnections,
	}
	if _, _, err := hub.Subscribe(42); err != ErrRevisionConnectionLimit {
		t.Fatalf("process connection limit error=%v", err)
	}
}
