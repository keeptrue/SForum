package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresExtensionDatabaseRegistryEnforcesRuntimeBudgets(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.budgets.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(t, ctx, pool, extensionID, hex.EncodeToString(digestBytes[:]))
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 304, AuditEventID: 404,
	})
	if err != nil {
		t.Fatalf("provision own-schema credential: %v", err)
	}
	assertExtensionDatabaseRuntimeLimits(t, ctx, pool, credential)

	connections := make([]*pgx.Conn, 0, extensionDatabaseRuntimeConnectionLimit)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close(context.Background())
		}
	}()
	for index := 0; index < extensionDatabaseRuntimeConnectionLimit; index++ {
		connection, connectErr := connectExtensionDatabaseCredential(ctx, pool, credential)
		if connectErr != nil {
			t.Fatalf("open budgeted connection %d: %v", index+1, connectErr)
		}
		connections = append(connections, connection)
	}
	overBudget, err := connectExtensionDatabaseCredential(ctx, pool, credential)
	if err == nil {
		_ = overBudget.Close(ctx)
		t.Fatal("runtime role opened a connection beyond its Host-owned budget")
	}
	if !isPostgresErrorCode(err, "53300") {
		t.Fatalf("connection budget error = %v, want SQLSTATE 53300", err)
	}

	startedAt := time.Now()
	_, err = connections[0].Exec(ctx, `SELECT pg_sleep(30)`)
	if !isPostgresErrorCode(err, "57014") {
		t.Fatalf("slow query error = %v, want SQLSTATE 57014", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 10*time.Second {
		t.Fatalf("slow query cancellation took %s", elapsed)
	}

	for _, connection := range connections {
		if err := connection.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	connections = connections[:0]
	rotated, err := registry.RotateOwnSchemaCredential(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 305, AuditEventID: 405,
	})
	if err != nil {
		t.Fatalf("rotate own-schema credential: %v", err)
	}
	assertExtensionDatabaseRuntimeLimits(t, ctx, pool, rotated)
}

func TestPostgresExtensionDatabaseRegistryRejectsPreexistingLooseRuntimeTimeout(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	extensionID := fmt.Sprintf("p5.loose-timeout.%d", time.Now().UnixNano())
	digestBytes := sha256.Sum256([]byte(extensionID))
	artifact := insertExtensionDatabaseRegistryFixture(t, ctx, pool, extensionID, hex.EncodeToString(digestBytes[:]))
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRole := pgx.Identifier{identifiers.RuntimeRole}.Sanitize()
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	database := pgx.Identifier{databaseName}.Sanitize()
	if _, err := pool.Exec(ctx, `CREATE ROLE `+runtimeRole+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER ROLE `+runtimeRole+` IN DATABASE `+database+` SET statement_timeout TO '1h'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `ALTER ROLE `+runtimeRole+` IN DATABASE `+database+` RESET ALL`)
		cleanupExtensionDatabaseRegistryFixture(t, pool, artifact, identifiers)
	}()

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	_, err = registry.ProvisionOwnSchema(ctx, ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: 306, AuditEventID: 406,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("preexisting loose timeout was not rejected: %v", err)
	}
	var setting string
	if err := pool.QueryRow(ctx, `
		SELECT setting
		FROM pg_db_role_setting
		CROSS JOIN LATERAL unnest(setconfig) AS setting
		WHERE setrole = (SELECT oid FROM pg_roles WHERE rolname = $1)
	`, identifiers.RuntimeRole).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != "statement_timeout=1h" {
		t.Fatalf("failed provision mutated the preexisting timeout: %q", setting)
	}
}

func assertExtensionDatabaseRuntimeLimits(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	credential ExtensionDatabaseCredential,
) {
	t.Helper()
	var connectionLimit int
	if err := pool.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, credential.RoleName).Scan(&connectionLimit); err != nil {
		t.Fatal(err)
	}
	if connectionLimit != extensionDatabaseRuntimeConnectionLimit {
		t.Fatalf("runtime connection limit = %d, want %d", connectionLimit, extensionDatabaseRuntimeConnectionLimit)
	}
	connection, err := connectExtensionDatabaseCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	var statementTimeout, idleTransactionTimeout string
	if err := connection.QueryRow(ctx, `SHOW statement_timeout`).Scan(&statementTimeout); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SHOW idle_in_transaction_session_timeout`).Scan(&idleTransactionTimeout); err != nil {
		t.Fatal(err)
	}
	if statementTimeout != extensionDatabaseRuntimeStatementTimeout ||
		idleTransactionTimeout != extensionDatabaseRuntimeIdleTransactionTimeout {
		t.Fatalf(
			"runtime timeouts = statement %q idle transaction %q, want %q and %q",
			statementTimeout, idleTransactionTimeout,
			extensionDatabaseRuntimeStatementTimeout, extensionDatabaseRuntimeIdleTransactionTimeout,
		)
	}
}
