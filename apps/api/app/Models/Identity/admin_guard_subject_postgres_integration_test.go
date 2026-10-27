package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresAdminGuardSubjectObservesCrossNodeRoleChanges(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	schema := fmt.Sprintf("identity_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	for _, statement := range []string{
		`CREATE TABLE ` + quotedSchema + `.users (
			id BIGINT PRIMARY KEY,
			is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE
		)`,
		`CREATE TABLE ` + quotedSchema + `.roles (
			id BIGINT PRIMARY KEY,
			key TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE ` + quotedSchema + `.user_roles (
			user_id BIGINT NOT NULL REFERENCES ` + quotedSchema + `.users(id) ON DELETE CASCADE,
			role_id BIGINT NOT NULL REFERENCES ` + quotedSchema + `.roles(id) ON DELETE CASCADE,
			PRIMARY KEY (user_id, role_id)
		)`,
		`INSERT INTO ` + quotedSchema + `.roles (id, key) VALUES (1, 'member'), (2, 'super_admin')`,
		`INSERT INTO ` + quotedSchema + `.users (id, is_initial_super_admin) VALUES (10, false), (20, false), (30, true)`,
		`INSERT INTO ` + quotedSchema + `.user_roles (user_id, role_id) VALUES (10, 1), (20, 2), (30, 2)`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install identity guard fixture: %v\n%s", err, statement)
		}
	}

	nodeA := openIdentityGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openIdentityGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	store := NewPostgresStore(nodeA)

	tests := []struct {
		userID  int64
		initial bool
		super   bool
	}{
		{userID: 10},
		{userID: 20, super: true},
		{userID: 30, initial: true, super: true},
	}
	for _, test := range tests {
		subject, err := store.LoadAdminGuardSubject(ctx, test.userID)
		if err != nil || !subject.Exists || subject.UserID != test.userID ||
			subject.IsInitialSuperAdmin != test.initial || subject.IsSuperAdmin != test.super {
			t.Fatalf("user %d subject=%#v err=%v", test.userID, subject, err)
		}
	}
	if _, err := store.LoadAdminGuardSubject(ctx, 999); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user error = %v", err)
	}

	// node B 修改角色后，node A 的下一次权威读取必须立即看到新保护等级。
	if _, err := nodeB.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES (10, 2)`); err != nil {
		t.Fatal(err)
	}
	granted, err := store.LoadAdminGuardSubject(ctx, 10)
	if err != nil || !granted.IsSuperAdmin {
		t.Fatalf("cross-node grant subject=%#v err=%v", granted, err)
	}
	if _, err := nodeB.Exec(ctx, `DELETE FROM user_roles WHERE user_id = 10 AND role_id = 2`); err != nil {
		t.Fatal(err)
	}
	revoked, err := store.LoadAdminGuardSubject(ctx, 10)
	if err != nil || revoked.IsSuperAdmin {
		t.Fatalf("cross-node revoke subject=%#v err=%v", revoked, err)
	}
}

func openIdentityGuardNode(t *testing.T, ctx context.Context, databaseURL, schema string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
