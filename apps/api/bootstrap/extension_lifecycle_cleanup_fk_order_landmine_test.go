package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这条测试钉住 2026-11-14 基线里两次打红的 users_pkey 23505。
//
// 触发机制：`users.id` 是 BIGSERIAL，序列只在「不给 id 的插入」时前进。
// 同一分片里的所有包共用一个测试库，其中不少夹具用显式 id 插 users
// （id=1/2/3/7/42…）。显式插入既不推进序列，也不删这些行，于是下一次
// 「不给 id 的插入」可能拿到一个已被占用的 id，撞 users_pkey。
//
// 这里把那个状态原样造出来：先消费掉一个序列值 n，再用显式 id=n+1 落一行，
// 一个依赖默认 id 的夹具下一次必然取到 n+1 —— 修复前就是 23505。
// 最后那条对照断言保证这测试不空转：真正重复的显式 id 仍然必须报 23505。
func TestLifecyclePurgeFixtureSurvivesExplicitIDUserLandmine(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var consumed int64
	if err := pool.QueryRow(ctx, `SELECT nextval('users_id_seq')`).Scan(&consumed); err != nil {
		t.Fatalf("consume one users_id_seq value: %v", err)
	}
	landmineID := consumed + 1
	landmineName := fmt.Sprintf("landmine_%d", landmineID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES ($1, $2, $2, $3, $3, 'Sequence Landmine')
	`, landmineID, landmineName, landmineName+"@example.test"); err != nil {
		t.Fatalf("seed explicit-id user at the value the sequence will hand out next (%d): %v", landmineID, err)
	}

	// 修复前：夹具内部 `INSERT INTO users (username, ...)` 取到 landmineID，t.Fatal 23505。
	newLifecyclePurgeFixture(t, true)

	var fixtureUserID int64
	if err := pool.QueryRow(ctx, `
		SELECT max(id) FROM users WHERE username LIKE 'bootstrap_purge_fk_%'
	`).Scan(&fixtureUserID); err != nil {
		t.Fatalf("read fixture user id: %v", err)
	}
	if fixtureUserID <= landmineID {
		t.Fatalf("fixture user id = %d, want an id above the explicit-id landmine %d "+
			"(the shared sequence must be repaired before the fixture inserts)", fixtureUserID, landmineID)
	}

	// 对照：显式重复 id 仍然必须被 users_pkey 拒绝 —— 修复的是序列簿记，不是冲突检测。
	_, duplicateErr := pool.Exec(ctx, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES ($1, $2, $2, $3, $3, 'Duplicate')
	`, landmineID, landmineName+"_dup", landmineName+"_dup@example.test")
	if duplicateErr == nil {
		t.Fatal("duplicate explicit user id was accepted; the fixture repair must not disable users_pkey")
	}
	var pgErr *pgconn.PgError
	if !errors.As(duplicateErr, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "users_pkey" {
		t.Fatalf("duplicate explicit user id: want users_pkey 23505, got %v", duplicateErr)
	}
	t.Logf("landmine id=%d; control duplicate insert rejected with code=%s constraint=%s detail=%q",
		landmineID, pgErr.Code, pgErr.ConstraintName, pgErr.Detail)
}
