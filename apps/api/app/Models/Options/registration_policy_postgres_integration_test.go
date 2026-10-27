package options

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestRegistrationEnabledTxReadsAuthoritativePolicyAndSerializesUpdates(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("options_registration_policy_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE web_options (name TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO web_options (name, value) VALUES
		  ('identity.registration.enabled', 'enabled'),
		  ('identity.registration.mode', 'open')
	`); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	service := NewService(store)
	updateAcquiredLock := make(chan struct{}, 1)
	store.registrationPolicyLockObserver = func() {
		updateAcquiredLock <- struct{}{}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	enabled, err := service.RegistrationEnabledTx(ctx, tx)
	if err != nil || !enabled {
		t.Fatalf("transactional policy enabled=%t err=%v", enabled, err)
	}

	updateStarted := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		close(updateStarted)
		_, updateErr := store.Upsert(ctx, UpdateInput{Name: NameIdentityRegistrationEnabled, Value: "disabled"})
		updateDone <- updateErr
	}()
	<-updateStarted
	select {
	case <-updateAcquiredLock:
		t.Fatal("policy update acquired the advisory transaction lock before the reader committed")
	default:
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updateAcquiredLock:
	case <-time.After(3 * time.Second):
		t.Fatal("policy update did not acquire the lock after transaction commit")
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("policy update remained blocked after transaction commit")
	}

	verifyTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyTx.Rollback(ctx)
	enabled, err = service.RegistrationEnabledTx(ctx, verifyTx)
	if err != nil || enabled {
		t.Fatalf("updated transactional policy enabled=%t err=%v", enabled, err)
	}
}
