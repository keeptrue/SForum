// Package guard 提供开发库识别所需的数据库原语：开发库哨兵表、一次性测试
// 集群标记，以及开发库名的存在性检查。
//
// 这一层刻意不依赖 testing，因为开发启动脚本（scripts/api-dev.sh 经
// internal/testdb/cmd/devguard）也要用它写哨兵；把 testing 拉进开发进程会
// 平白注册测试 flag。测试侧的策略在 internal/testdb。
package guard

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	// SentinelTable 由开发启动脚本在开发库里幂等创建，用来声明"这是开发库"。
	// 它不进 migrations：测试库要跑完整迁移链，哨兵一旦成为迁移对象就会被
	// 迁移到一次性测试集群里，守卫立刻失效。
	SentinelTable = "public.sforum_dev_guard"
	// ClusterMarker 由 scripts/test-api-isolated.sh 写在一次性集群的 postgres
	// 库注释上，表示整个集群用完即删。它与
	// app/Support/Extensions/IntegrationTests 里的同名约定保持一致。
	ClusterMarker = "sforum.disposable-test-cluster.v1"
	// DevelopmentDatabaseName 是开发 compose 栈的库名。集群里出现它，说明
	// 这不是一个可丢弃的测试集群。
	DevelopmentDatabaseName = "sforum"
)

// createSentinel 是幂等 DDL：重复执行不报错，也不会改写已有行。
const createSentinel = `CREATE TABLE IF NOT EXISTS ` + SentinelTable + ` (
	guard TEXT PRIMARY KEY,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

const insertSentinel = `INSERT INTO ` + SentinelTable + ` (guard) VALUES ('development') ON CONFLICT DO NOTHING`

// State 是一次集群探测的结果。
type State struct {
	// Sentinel 为真表示当前库里存在开发库哨兵表。
	Sentinel bool
	// Disposable 为真表示整个集群带有一次性测试集群标记。
	Disposable bool
	// DevelopmentDatabase 为真表示集群里存在名为 sforum 的开发库。
	DevelopmentDatabase bool
}

// Open 打开到 url 的连接池，并确认真的能连上。
func Open(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Inspect 一次往返读出哨兵表、集群标记与开发库存在性。
func Inspect(ctx context.Context, db *sql.DB) (State, error) {
	var state State
	err := db.QueryRowContext(ctx, `
		SELECT to_regclass($1) IS NOT NULL,
		       EXISTS (SELECT 1 FROM pg_database WHERE shobj_description(oid, 'pg_database') = $2),
		       EXISTS (SELECT 1 FROM pg_database WHERE datname = $3)
	`, SentinelTable, ClusterMarker, DevelopmentDatabaseName).Scan(
		&state.Sentinel, &state.Disposable, &state.DevelopmentDatabase,
	)
	if err != nil {
		return State{}, fmt.Errorf("inspect database guard state: %w", err)
	}
	return state, nil
}

// EnsureSentinel 幂等写入开发库哨兵。
func EnsureSentinel(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createSentinel); err != nil {
		return fmt.Errorf("create development guard table: %w", err)
	}
	if _, err := db.ExecContext(ctx, insertSentinel); err != nil {
		return fmt.Errorf("mark development guard table: %w", err)
	}
	return nil
}

// Redact 去掉连接串里的口令，供错误信息使用。
func Redact(url string) string {
	at := strings.LastIndex(url, "@")
	if at < 0 {
		return url
	}
	scheme := strings.Index(url, "://")
	if scheme < 0 || scheme+3 > at {
		return url
	}
	return url[:scheme+3] + "***" + url[at:]
}
