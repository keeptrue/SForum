package testdb_test

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

// TestInsertUserWithExplicitIDKeepsSequenceAhead 钉住 2026-11-17 覆盖率门禁
// 连跑两次、每次命中不同包的那个 users_pkey 23505。
//
// 机制：`users.id` 是 BIGSERIAL，序列只在「不给 id 的插入」时前进。显式 id
// 插入不推进序列，跨包共享 public schema 时，下一个走默认 id 的包会拿到那个
// 已被占用的号。helper 的责任就是把这条簿记补上。
//
// 两侧都要断言：
//  1. 显式 id 落行之后，走默认 id 的插入必须拿到更大的 id（不撞）；
//  2. 真正重复的显式 id 仍然被 users_pkey 拒绝 —— 对齐的是编号分配，不是
//     冲突检测。
func TestInsertUserWithExplicitIDKeepsSequenceAhead(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("连接测试库失败：%v", err)
	}
	defer pool.Close()

	suffix := time.Now().UnixNano()
	username := func(tag string) string { return fmt.Sprintf("testdb_align_%s_%d", tag, suffix) }

	// 先把序列消费到 n，再用显式 id=n+1 落一行：修复前，下一个默认 id 插入
	// 必然取到 n+1 并撞主键。
	var consumed int64
	if err := pool.QueryRow(ctx, `SELECT nextval('users_id_seq')`).Scan(&consumed); err != nil {
		t.Fatalf("consume users_id_seq: %v", err)
	}
	landmineID := consumed + 1
	landmineName := username("landmine")
	testdb.InsertUserWithExplicitID(t, ctx, pool, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES ($1, $2, $2, $3, $3, 'Sequence Align')
	`, landmineID, landmineName, landmineName+"@example.test")

	var defaultID int64
	defaultName := username("default")
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Default Id')
		RETURNING id`, defaultName, defaultName+"@example.test").Scan(&defaultID); err != nil {
		t.Fatalf("默认 id 插入撞上了显式 id 的行（序列没对齐）：%v", err)
	}
	if defaultID <= landmineID {
		t.Fatalf("默认 id 插入得到 %d，必须大于显式 id %d", defaultID, landmineID)
	}

	// 对照：重复的显式 id 仍然必须被主键拒绝。
	duplicateName := username("dup")
	_, duplicateErr := pool.Exec(ctx, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES ($1, $2, $2, $3, $3, 'Duplicate')
	`, landmineID, duplicateName, duplicateName+"@example.test")
	var pgErr *pgconn.PgError
	if !errors.As(duplicateErr, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "users_pkey" {
		t.Fatalf("重复显式 id：期望 users_pkey 23505，得到 %v", duplicateErr)
	}
}
