package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const stableCoreViewsPublicRevokeMigration = "202611150002_stable_core_views_public_revoke.sql"

// EX2-07 的两条断言分工：
//
//  1. 静态棘轮扫**全部迁移**：任何 Up 段里的 `CREATE [OR REPLACE] VIEW
//     sforum_core_v1.*` 之后，都必须存在一条 schema 级 REVOKE（同一迁移里更后面的
//     位置，或版本号更大的迁移）。只盯本次补的 8 张视图会在第 9、10 张上重新破防。
//  2. 实库断言：每张 stable core view 对 PUBLIC 都真的没有写权限——文本匹配说明不了
//     ACL 现状，只有 has_table_privilege 能。

const schemaLevelViewRevoke = "REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC"

// migrationText 读一条迁移并拆出 Up / Down 两段（Down 标记不参与扫描）。
func migrationText(t *testing.T, name string) (up, down string) {
	t.Helper()
	body, err := fs.ReadFile(Files(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("%s has no Down section", name)
	}
	return parts[0], parts[1]
}

// migrationVersion 从 `202611150002_name.sql` 取出 goose 用的版本号。
func migrationVersion(name string) (int64, bool) {
	if !strings.HasSuffix(name, ".sql") {
		return 0, false
	}
	base := strings.TrimSuffix(name, ".sql")
	prefix, _, ok := strings.Cut(base, "_")
	// 仓库的 goose 版本号是 YYYYMMDDNNNN（12 位数字），这里不写死长度，只要求纯数字。
	if !ok || len(prefix) < 8 || len(prefix) > 14 {
		return 0, false
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, false
	}
	return version, true
}

func flattenedSQL(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// statementSQL 只保留可执行语句部分（去注释 + 压平 + 小写），用于「这条 SQL 真的
// 在迁移里吗」这类断言。复用本包的 stripSQLComments（迁移里到处是「我们本该
// REVOKE …」这类说明文字，带注释匹配会让断言假绿）。
func statementSQL(text string) string {
	return flattenedSQL(stripSQLComments(text))
}

func TestStableCoreViewsPublicRevokeMigrationIsOnlineSafe(t *testing.T) {
	up, down := migrationText(t, stableCoreViewsPublicRevokeMigration)
	if !strings.Contains(flattenedSQL(up), "-- +sforum onlinesafe") {
		t.Fatal("revoke migration must declare -- +sforum OnlineSafe")
	}
	sql := statementSQL(up)
	if !strings.Contains(sql, "set local lock_timeout") || !strings.Contains(sql, "set local statement_timeout") {
		t.Fatal("revoke migration must bound its own locks with SET LOCAL")
	}
	if !strings.Contains(sql, strings.ToLower(schemaLevelViewRevoke)) {
		t.Fatalf("revoke migration must contain the executable statement %q", schemaLevelViewRevoke)
	}
	if downSQL := statementSQL(down); !strings.Contains(downSQL, "select 1") || strings.Contains(downSQL, "grant") {
		t.Fatal("revoke migration Down must be an explicit no-op instead of restoring PUBLIC rights")
	}
}

// TestStableCoreViewsMigrationRatchet 是扫全表的静态棘轮。
func TestStableCoreViewsMigrationRatchet(t *testing.T) {
	entries, err := fs.ReadDir(Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	type migration struct {
		version int64
		name    string
		up      string
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		version, ok := migrationVersion(name)
		if !ok {
			continue
		}
		up, _ := migrationText(t, name)
		migrations = append(migrations, migration{version: version, name: name, up: statementSQL(up)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	createView := regexp.MustCompile(`create (?:or replace )?view sforum_core_v1\.[a-z0-9_]+`)
	checkedViews := 0
	for index, current := range migrations {
		matches := createView.FindAllStringIndex(current.up, -1)
		if len(matches) == 0 {
			continue
		}
		// 覆盖要求：同一条迁移里、最后一个 CREATE VIEW 之后有 schema 级 REVOKE，
		// 或者任何版本更大的迁移里有它。
		revokedHere := false
		revokeAt := strings.Index(current.up, strings.ToLower(schemaLevelViewRevoke))
		if revokeAt >= 0 && revokeAt > matches[len(matches)-1][0] {
			revokedHere = true
		}
		coveredLater := false
		if !revokedHere {
			for _, later := range migrations[index+1:] {
				if strings.Contains(later.up, strings.ToLower(schemaLevelViewRevoke)) {
					coveredLater = true
					break
				}
			}
		}
		if !revokedHere && !coveredLater {
			t.Fatalf(
				"%s creates sforum_core_v1 views without a later schema-level REVOKE (%s); add it to that migration or a newer one",
				current.name, schemaLevelViewRevoke,
			)
		}
		checkedViews += len(matches)
	}
	if checkedViews < 10 {
		t.Fatalf("ratchet only saw %d CREATE VIEW statements; expected to scan the whole migration history", checkedViews)
	}
}

// TestStableCoreViewsHaveNoPublicPrivileges 在实库里逐视图断言 PUBLIC 没有权限。
func TestStableCoreViewsHaveNoPublicPrivileges(t *testing.T) {
	ctx := context.Background()
	databaseURL := testdb.IsolatedURL(t)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT classes.relname
		FROM pg_class AS classes
		JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
		WHERE namespaces.nspname = 'sforum_core_v1' AND classes.relkind = 'v'
		ORDER BY classes.relname
	`)
	if err != nil {
		t.Fatal(err)
	}
	views := make([]string, 0, 32)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		views = append(views, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(views) < 10 {
		t.Fatalf("only %d stable core views found; expected the full catalog", len(views))
	}

	for _, view := range views {
		qualified := fmt.Sprintf("sforum_core_v1.%s", view)
		for _, privilege := range []string{
			"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER",
		} {
			var granted bool
			if err := pool.QueryRow(ctx,
				`SELECT has_table_privilege('public', $1, $2)`, qualified, privilege,
			).Scan(&granted); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", qualified, privilege, err)
			}
			if granted {
				t.Fatalf("PUBLIC still holds %s on %s", privilege, qualified)
			}
		}
		// 视图本身必须结构上不可写：stable core views 是只读契约。
		var updatable bool
		if err := pool.QueryRow(ctx,
			`SELECT pg_catalog.pg_relation_is_updatable($1::regclass, false) <> 0`, qualified,
		).Scan(&updatable); err != nil {
			t.Fatalf("pg_relation_is_updatable(%s): %v", qualified, err)
		}
		if updatable {
			t.Fatalf("%s is updatable; stable core views must be structurally read-only", qualified)
		}
	}
}
