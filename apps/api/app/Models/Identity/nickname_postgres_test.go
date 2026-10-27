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

// 昵称写入的约束只有真库能证明：行锁下的冷却判定、历史与审计同事务、
// 重名统计只算活跃用户、撞名检查排除自己。
func TestPostgresNicknameStoreChangeAndHistory(t *testing.T) {
	ctx, store := newNicknamePostgresHarness(t)

	t.Run("changes the nickname and records history plus audit", func(t *testing.T) {
		snapshot, err := store.ChangeSelfNickname(ctx, ChangeNicknameInput{
			UserID: 1, ActorID: 1, DisplayName: "爱丽丝", CooldownMinutes: 10080,
		})
		if err != nil {
			t.Fatalf("change nickname: %v", err)
		}
		if snapshot.DisplayName != "爱丽丝" || snapshot.ChangedAt == nil {
			t.Fatalf("snapshot = %+v", snapshot)
		}
		history, err := store.ListNicknameHistory(ctx, 1, 10)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(history) != 1 || history[0].OldName != "Alice" || history[0].NewName != "爱丽丝" || history[0].Source != NicknameSourceSelf {
			t.Fatalf("history = %+v", history)
		}
		var audits int
		if err := store.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM audit_events WHERE target_user_id = 1 AND action = $1
		`, AuditActionNicknameChange).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if audits != 1 {
			t.Fatalf("nickname audit count = %d", audits)
		}
	})

	t.Run("enforces the cooldown under the row lock", func(t *testing.T) {
		if _, err := store.pool.Exec(ctx, `
			UPDATE users SET nickname_changed_at = now() - interval '1 hour' WHERE id = 2
		`); err != nil {
			t.Fatal(err)
		}
		_, err := store.ChangeSelfNickname(ctx, ChangeNicknameInput{
			UserID: 2, ActorID: 2, DisplayName: "Bob Renamed", CooldownMinutes: 1440,
		})
		var cooldown *NicknameCooldownError
		if !errors.As(err, &cooldown) {
			t.Fatalf("cooldown error = %v", err)
		}
		if time.Until(cooldown.RetryAt) < 22*time.Hour {
			t.Fatalf("retryAt = %v, want roughly 23 hours out", cooldown.RetryAt)
		}
		var name string
		if err := store.pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id = 2`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Bob" {
			t.Fatalf("cooldown path must not write, display_name = %q", name)
		}
	})

	t.Run("no-op change does not write history or move the clock", func(t *testing.T) {
		before, err := store.NicknameSnapshot(ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		after, err := store.ChangeSelfNickname(ctx, ChangeNicknameInput{
			UserID: 3, ActorID: 3, DisplayName: "Carol", CooldownMinutes: 10080,
		})
		if err != nil {
			t.Fatalf("no-op change: %v", err)
		}
		if after.ChangedAt != nil {
			t.Fatalf("no-op change must not set nickname_changed_at, got %v", after.ChangedAt)
		}
		if before.DisplayName != after.DisplayName {
			t.Fatalf("no-op changed the name: %q -> %q", before.DisplayName, after.DisplayName)
		}
		history, err := store.ListNicknameHistory(ctx, 3, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 0 {
			t.Fatalf("no-op history = %+v", history)
		}
	})
}

func TestPostgresNicknameStoreLookups(t *testing.T) {
	ctx, store := newNicknamePostgresHarness(t)

	duplicates, err := store.NicknameDuplicates(ctx, 1, "alice")
	if err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	// 自己（1）与被封禁的 4 号都不算；只有活跃的 5 号（大小写不同）算同名。
	if duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1 active duplicate (case-insensitive)", duplicates)
	}

	taken, err := store.UsernameOwnedByOther(ctx, 1, "BOB")
	if err != nil {
		t.Fatalf("username collision: %v", err)
	}
	if !taken {
		t.Fatal("BOB must collide with bob's username case-insensitively")
	}
	taken, err = store.UsernameOwnedByOther(ctx, 1, "alice")
	if err != nil {
		t.Fatalf("username collision: %v", err)
	}
	if taken {
		t.Fatal("a user's own username must not count as a collision")
	}
}

func newNicknamePostgresHarness(t *testing.T) (context.Context, *PostgresNicknameStore) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_nickname_%d", time.Now().UnixNano())
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
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	statements := []string{
		`CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			nickname_changed_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE user_nickname_changes (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			old_name TEXT NOT NULL,
			new_name TEXT NOT NULL,
			source TEXT NOT NULL CHECK (source IN ('self', 'admin')),
			changed_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			actor_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			target_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`INSERT INTO users (id, username, username_lower, display_name, status) VALUES
			(1, 'alice', 'alice', 'Alice', 'active'),
			(2, 'bob', 'bob', 'Bob', 'active'),
			(3, 'carol', 'carol', 'Carol', 'active'),
			(4, 'dave', 'dave', 'Alice', 'banned'),
			(5, 'erin', 'erin', 'ALICE', 'active')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup nickname schema: %v\n%s", err, statement)
		}
	}
	return ctx, NewPostgresNicknameStore(pool)
}
