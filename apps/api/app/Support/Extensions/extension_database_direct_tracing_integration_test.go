package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestOwnSchemaRoleCannotProvideHostSafeQueryTracing(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.direct-trace.%d", time.Now().UnixNano())
	digest := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(t, ctx, pool, extensionID, hex.EncodeToString(digest[:]))
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)
	credential, err := NewPostgresExtensionDatabaseRegistry(pool, nil).ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 307, AuditEventID: 407,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := connectExtensionDatabaseCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())

	const untrustedMarker = "plugin-controlled-sensitive-marker"
	if _, err := connection.Exec(ctx, `SELECT set_config('application_name', $1, false)`, untrustedMarker); err != nil {
		t.Fatalf("ordinary plugin role should control application_name: %v", err)
	}
	var backendPID int32
	if err := connection.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	var observedApplicationName string
	if err := pool.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE pid = $1`, backendPID).Scan(&observedApplicationName); err != nil {
		t.Fatal(err)
	}
	if observedApplicationName != untrustedMarker {
		t.Fatalf("application_name was not plugin-controlled: %q", observedApplicationName)
	}

	_, err = connection.Exec(ctx, `SELECT set_config('log_min_duration_statement', '100ms', false)`)
	if !isPostgresErrorCode(err, "42501") {
		t.Fatalf("ordinary plugin role changed server query logging, err=%v", err)
	}
}
