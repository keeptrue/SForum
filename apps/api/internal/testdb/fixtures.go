package testdb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier 是 pgxpool.Pool 与 pgx.Tx 共有的最小执行面：夹具拿连接池还是拿
// 事务都能直接传进来。
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// InsertUserWithExplicitID 执行一条「显式指定 users.id」的插入，并把
// users_id_seq 对齐到表内 max(id)。
//
// 为什么必须走它：`users.id` 是 BIGSERIAL，序列只在「不给 id 的插入」时前进。
// 同一分片里的所有包共用一个测试库的 public schema，只要有一个夹具用显式 id
// 落了一行（id=42 之类）却没推进序列，下一个走默认 id 的包就会拿到那个已被
// 占用的值，撞 users_pkey 23505。2026-11-17 覆盖率门禁连跑两次、每次命中不同
// 的包（`app/Models/Notifications`、`app/Support/Extensions`），成因就是这个：
// 缺陷属于插入方（把序列留在 max(id) 后面），不属于受害者。
//
// `ON CONFLICT (id) DO NOTHING` 不能替代这一步：冲突被吞掉之后序列照样落后，
// 只是换一个包爆。真正重复的显式 id 仍然由主键拒绝，对齐的是编号簿记。
func InsertUserWithExplicitID(t testing.TB, ctx context.Context, q Querier, sql string, args ...any) {
	t.Helper()
	if _, err := q.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("insert explicit-id user: %v", err)
	}
	AlignSerial(t, ctx, q, "users")
}

// AlignSerial 把 table 的 BIGSERIAL 序列推进到表内 max(id)，只前进不回拨。
//
// 序列已经领先时保持不动：同库里别的包可能已经消费过更大的号，回拨会把号
// 重新发出去。表为空时什么都不做（空表不可能落后于序列）。
//
// table 可以写成 "users" 或 "public.users"；不带 schema 时按 search_path 解析，
// 因此私有 schema 的夹具也可以用它对齐自己那张表。
func AlignSerial(t testing.TB, ctx context.Context, q Querier, table string) {
	t.Helper()
	ident, err := sanitizeTable(table)
	if err != nil {
		t.Fatalf("align serial for %q: %v", table, err)
	}
	var sequence string
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(pg_get_serial_sequence($1, 'id'), '')`, ident,
	).Scan(&sequence); err != nil {
		t.Fatalf("resolve serial sequence for %s: %v", table, err)
	}
	if sequence == "" {
		t.Fatalf("%s.id 不是 SERIAL/BIGSERIAL，没有可对齐的序列", table)
	}
	var maxID int64
	if err := q.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(max(id), 0) FROM %s`, ident),
	).Scan(&maxID); err != nil {
		t.Fatalf("read max(id) of %s: %v", table, err)
	}
	if maxID <= 0 {
		return
	}
	var lastValue int64
	if err := q.QueryRow(ctx,
		`SELECT COALESCE(pg_sequence_last_value($1::regclass), 0)`, sequence,
	).Scan(&lastValue); err != nil {
		t.Fatalf("read last value of %s: %v", sequence, err)
	}
	if lastValue >= maxID {
		return
	}
	if _, err := q.Exec(ctx, `SELECT setval($1::regclass, $2, true)`, sequence, maxID); err != nil {
		t.Fatalf("advance %s to %d: %v", sequence, maxID, err)
	}
}

// sanitizeTable 把 "table" / "schema.table" 变成可以安全拼进 SQL 的标识符。
func sanitizeTable(table string) (string, error) {
	parts := strings.Split(table, ".")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("表名 %q 有空段", table)
		}
	}
	switch len(parts) {
	case 1:
		return pgx.Identifier{parts[0]}.Sanitize(), nil
	case 2:
		return pgx.Identifier{parts[0], parts[1]}.Sanitize(), nil
	default:
		return "", fmt.Errorf("表名 %q 只允许 table 或 schema.table", table)
	}
}
