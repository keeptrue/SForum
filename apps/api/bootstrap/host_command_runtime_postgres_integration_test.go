package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

func TestPostgresProtocolV2AttachmentStatusMutatorUsesCallerTransaction(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for destructive attachment command integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("host_command_attachment_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `
		CREATE TABLE users (
		  id BIGINT PRIMARY KEY,
		  username TEXT NOT NULL,
		  display_name TEXT NOT NULL
		);
		CREATE TABLE attachments (
		  id BIGSERIAL PRIMARY KEY,
		  public_id TEXT NOT NULL UNIQUE,
		  owner_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
		  provider TEXT NOT NULL,
		  object_key TEXT NOT NULL,
		  original_name TEXT NOT NULL,
		  content_type TEXT NOT NULL,
		  extension TEXT NOT NULL DEFAULT '',
		  size_bytes BIGINT NOT NULL,
		  sha256 TEXT NOT NULL,
		  image_width INTEGER,
		  image_height INTEGER,
		  visibility TEXT NOT NULL,
		  status TEXT NOT NULL,
		  reference_count INTEGER NOT NULL DEFAULT 0,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp(),
		  deleted_at TIMESTAMPTZ
		);
		INSERT INTO users (id, username, display_name) VALUES (42, 'owner', 'Owner');
		INSERT INTO attachments (
		  id, public_id, owner_user_id, provider, object_key, original_name,
		  content_type, extension, size_bytes, sha256, visibility, status
		) VALUES (
		  7, 'attachment-7', 42, 'local', 'attachment-7', 'fixture.txt',
		  'text/plain', 'txt', 7, 'digest', 'private', 'active'
		);
	`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}

	mutator := protocolV2AttachmentStatusMutator{store: attachments.NewPostgresStore(pool)}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mutator.MutateProtocolV2AttachmentStatus(ctx, tx, 7, attachments.StatusDisabled)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if result.ID != 7 || result.Status != attachments.StatusDisabled || result.ReferenceCount != 0 || result.UpdatedAt.IsZero() {
		_ = tx.Rollback(ctx)
		t.Fatalf("attachment result = %#v", result)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertBootstrapAttachmentStatus(t, ctx, pool, attachments.StatusDisabled)

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.MutateProtocolV2AttachmentStatus(ctx, tx, 7, attachments.StatusActive); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertBootstrapAttachmentStatus(t, ctx, pool, attachments.StatusDisabled)

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mutator.MutateProtocolV2AttachmentStatus(ctx, tx, 999, attachments.StatusDisabled)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, hostapi.ErrProtocolV2AttachmentNotFound) {
		t.Fatalf("missing attachment error = %v", err)
	}
}

func assertBootstrapAttachmentStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM attachments WHERE id = 7`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("attachment status = %q, want %q", got, want)
	}
}
