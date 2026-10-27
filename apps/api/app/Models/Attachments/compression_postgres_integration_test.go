package attachments

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestCompressionTaskVariantStatsAndBackfillPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := NewPostgresStore(pool)
	publicID := "compression-it-" + uuid.NewString()
	var attachmentID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachments (
		  public_id, owner_user_id, provider, object_key, original_name, content_type,
		  extension, size_bytes, sha256, visibility, status
		) VALUES ($1, NULL, 'local', $2, 'integration.jpg', 'image/jpeg', '.jpg', $3, $4, 'public', 'active')
		RETURNING id
	`, publicID, "integration/"+publicID+".jpg", int64(2_000_000_000_000), strings.Repeat("a", 64)).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment (run migrations first): %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM attachments WHERE id=$1`, attachmentID)
	attachment, err := store.GetByID(ctx, attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	settings := (CompressionSettings{
		Enabled: true, Strength: 55, MaxDimension: 2560,
		MinSizeKB: 1_500_000_000, MinSavingsPercent: 8,
	}).normalized()

	before, err := store.CompressionStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	taskID, created, err := store.CreateCompressionTask(ctx, attachment, settings)
	if err != nil || !created {
		t.Fatalf("CreateCompressionTask id=%d created=%t err=%v", taskID, created, err)
	}
	duplicateID, duplicateCreated, err := store.CreateCompressionTask(ctx, attachment, settings)
	if err != nil || duplicateCreated || duplicateID != taskID {
		t.Fatalf("duplicate task id=%d created=%t err=%v", duplicateID, duplicateCreated, err)
	}
	claimed, err := store.ClaimCompressionTask(ctx, taskID)
	if err != nil {
		t.Fatalf("ClaimCompressionTask: %v", err)
	}
	if claimed.Attachment.Owner != nil || claimed.Attachment.ImageWidth != nil || claimed.Attachment.DeletedAt != nil || claimed.Attempts != 1 {
		t.Fatalf("nullable claim projection = %#v", claimed)
	}

	variant := AttachmentVariant{
		AttachmentID: attachmentID, Name: CompressionVariantDisplay, Provider: "local",
		ObjectKey: fmt.Sprintf("integration/%d-display.jpg", attachmentID), ContentType: "image/jpeg",
		SizeBytes: 1_000_000_000_000, SHA256: strings.Repeat("b", 64), ImageWidth: 1600, ImageHeight: 900,
		SourceSHA256: attachment.SHA256, PolicyDigest: settings.PolicyDigest, CompressionStrength: settings.Strength,
	}
	previous, err := store.CompleteCompressionTask(ctx, claimed, variant)
	if err != nil || previous != nil {
		t.Fatalf("CompleteCompressionTask previous=%#v err=%v", previous, err)
	}
	stored, err := store.GetAttachmentVariant(ctx, attachmentID, CompressionVariantDisplay)
	if err != nil || stored.ObjectKey != variant.ObjectKey {
		t.Fatalf("stored variant=%#v err=%v", stored, err)
	}
	after, err := store.CompressionStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReadyVariants != before.ReadyVariants+1 || after.SavedBytes < before.SavedBytes+1_000_000_000_000 {
		t.Fatalf("stats before=%#v after=%#v", before, after)
	}

	backfillSettings := settings
	backfillSettings.Strength = 56
	backfillSettings = backfillSettings.normalized()
	ids, err := store.BackfillCompressionTasks(ctx, backfillSettings, 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("BackfillCompressionTasks ids=%v err=%v", ids, err)
	}
	ids, err = store.BackfillCompressionTasks(ctx, backfillSettings, 10)
	if err != nil || len(ids) != 0 {
		t.Fatalf("deduplicated backfill ids=%v err=%v", ids, err)
	}
}

func TestCompressionTaskLeaseReclaimsAndExhaustsPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	publicID := "compression-lease-it-" + uuid.NewString()
	var attachmentID, taskID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachments (public_id, owner_user_id, provider, object_key, original_name, content_type, extension, size_bytes, sha256, visibility, status)
		VALUES ($1, NULL, 'local', $2, 'lease.jpg', 'image/jpeg', '.jpg', 300000, $3, 'public', 'active') RETURNING id
	`, publicID, "integration/"+publicID+".jpg", strings.Repeat("c", 64)).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment (run migrations first): %v", err)
	}
	defer pool.Exec(ctx, `DELETE FROM attachments WHERE id=$1`, attachmentID)
	if err := pool.QueryRow(ctx, `
		INSERT INTO attachment_compression_tasks (attachment_id, variant_name, source_sha256, policy_digest, compression_strength, status, attempts, started_at)
		VALUES ($1, 'display', $2, $3, 55, 'running', 1, $4) RETURNING id
	`, attachmentID, strings.Repeat("c", 64), strings.Repeat("d", 64), time.Now().Add(-compressionTaskLease-time.Minute)).Scan(&taskID); err != nil {
		t.Fatalf("insert compression task: %v", err)
	}
	store := NewPostgresStore(pool)
	ids, err := store.ListPendingCompressionTaskIDs(ctx, 100)
	if err != nil || !containsInt64(ids, taskID) {
		t.Fatalf("stale task ids=%v err=%v", ids, err)
	}
	claimed, err := store.ClaimCompressionTask(ctx, taskID)
	if err != nil || claimed.Attempts != 2 {
		t.Fatalf("reclaim task=%#v err=%v", claimed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE attachment_compression_tasks SET attempts=3, started_at=$2 WHERE id=$1`, taskID, time.Now().Add(-compressionTaskLease-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPendingCompressionTaskIDs(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status, error_code FROM attachment_compression_tasks WHERE id=$1`, taskID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != CompressionStatusFailed || code != "worker_timeout" {
		t.Fatalf("exhausted task status=%q code=%q", status, code)
	}
}

func containsInt64(values []int64, expected int64) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
