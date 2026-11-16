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

// viewCreatorRevokeExemptions 是「规则生效之前」就已经建视图的迁移。
//
// 历史迁移一行未改，它们只能在后续迁移里补 REVOKE，所以这里显式登记、逐条复核
// 覆盖关系。任何**新增**的建视图迁移都不在表里，必须自带同文件 REVOKE —— 这条与
// 版本号无关，因此「版本号倒挂的新迁移」不能再钻 coveredLater 的空子：静态扫描
// 分不清「更晚版本」与「更晚执行」，而执行顺序由 goose 的待迁集合决定，不是文件
// 排序。把豁免变成显式白名单，等于把「谁在什么时候补的」从推断变成登记。
var viewCreatorRevokeExemptions = map[string]string{
	"202610280001_content_reactions.sql":        stableCoreViewsPublicRevokeMigration,
	"202611100001_content_bookmarks.sql":        stableCoreViewsPublicRevokeMigration,
	"202611100002_badges_and_user_activity.sql": stableCoreViewsPublicRevokeMigration,
	"202611120002_badge_rules.sql":              stableCoreViewsPublicRevokeMigration,
	"202611140002_user_trust_levels.sql":        stableCoreViewsPublicRevokeMigration,
}

var createStableCoreView = regexp.MustCompile(`create (?:or replace )?view sforum_core_v1\.[a-z0-9_]+`)

// viewMigration 是棘轮的最小输入：名字、版本与 Up 段（已去注释并压平）。
type viewMigration struct {
	name    string
	version int64
	up      string
}

// pinnedViewCreatorExemptions 是豁免表的**规模棘轮**：表本身可以被无限追加，
// 而追加一行就等于免掉一条新迁移的自带 REVOKE。所以名字集合必须写死在这里，
// 新增豁免必须同时改这份清单——那是一个可见的人工检查点。
var pinnedViewCreatorExemptions = []string{
	"202610280001_content_reactions.sql",
	"202611100001_content_bookmarks.sql",
	"202611100002_badges_and_user_activity.sql",
	"202611120002_badge_rules.sql",
	"202611140002_user_trust_levels.sql",
}

// checkStableCoreViewRevokes 判定「每个建视图的迁移都被一条 REVOKE 覆盖」。
//
// 顺序无关的规则：建视图的迁移必须**自己**在最后一个 CREATE VIEW 之后写 schema 级
// REVOKE；只有登记在 exemptions 里的历史迁移允许由后续迁移覆盖。这样无论新迁移的
// 版本号写成多少（含倒挂），缺 REVOKE 都会红。
func checkStableCoreViewRevokes(migrations []viewMigration, exemptions map[string]string) (int, error) {
	checked := 0
	for index, current := range migrations {
		matches := createStableCoreView.FindAllStringIndex(current.up, -1)
		if len(matches) == 0 {
			continue
		}
		checked += len(matches)
		revokeAt := strings.Index(current.up, strings.ToLower(schemaLevelViewRevoke))
		if revokeAt >= 0 && revokeAt > matches[len(matches)-1][0] {
			continue
		}
		covering, exempt := exemptions[current.name]
		if !exempt {
			return checked, fmt.Errorf(
				"%s creates sforum_core_v1 views without its own schema-level REVOKE (%s); add it after the last CREATE VIEW in that migration",
				current.name, schemaLevelViewRevoke,
			)
		}
		covered := false
		for _, later := range migrations[index+1:] {
			if later.name == covering && strings.Contains(later.up, strings.ToLower(schemaLevelViewRevoke)) {
				covered = true
				break
			}
		}
		if !covered {
			return checked, fmt.Errorf(
				"%s is a pinned legacy view creator but its declared covering migration %s does not run later with %s",
				current.name, covering, schemaLevelViewRevoke,
			)
		}
	}
	return checked, nil
}

// checkViewCreatorExemptions 保证豁免表不会腐烂：每条豁免都必须仍然存在、仍然建视图。
func checkViewCreatorExemptions(migrations []viewMigration, exemptions map[string]string) error {
	for name := range exemptions {
		found := false
		for _, migration := range migrations {
			if migration.name != name {
				continue
			}
			found = len(createStableCoreView.FindAllString(migration.up, -1)) > 0
			break
		}
		if !found {
			return fmt.Errorf("stale view-creator exemption %q; remove it from viewCreatorRevokeExemptions", name)
		}
	}
	return nil
}

// 豁免表只能缩短或原样保留：多一行就要在 pinnedViewCreatorExemptions 里显式确认。
func TestViewCreatorExemptionsArePinned(t *testing.T) {
	if len(viewCreatorRevokeExemptions) != len(pinnedViewCreatorExemptions) {
		t.Fatalf("exemptions = %d, pinned = %d; adding an exemption needs an explicit edit here",
			len(viewCreatorRevokeExemptions), len(pinnedViewCreatorExemptions))
	}
	for _, name := range pinnedViewCreatorExemptions {
		if _, ok := viewCreatorRevokeExemptions[name]; !ok {
			t.Fatalf("pinned exemption %q is missing from viewCreatorRevokeExemptions", name)
		}
	}
}

// TestStableCoreViewsMigrationRatchet 是扫全表的静态棘轮。
func TestStableCoreViewsMigrationRatchet(t *testing.T) {
	entries, err := fs.ReadDir(Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	migrations := make([]viewMigration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		version, ok := migrationVersion(name)
		if !ok {
			continue
		}
		up, _ := migrationText(t, name)
		migrations = append(migrations, viewMigration{name: name, version: version, up: statementSQL(up)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	checkedViews, err := checkStableCoreViewRevokes(migrations, viewCreatorRevokeExemptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkViewCreatorExemptions(migrations, viewCreatorRevokeExemptions); err != nil {
		t.Fatal(err)
	}
	if checkedViews < 10 {
		t.Fatalf("ratchet only saw %d CREATE VIEW statements; expected to scan the whole migration history", checkedViews)
	}
}

// 盲区回归：版本号倒挂（比补 REVOKE 的迁移更小）的新迁移，静态排序会把它排在
// REVOKE 之前；旧实现因此判它「已被覆盖」。新规则要求它自带 REVOKE。
func TestStableCoreViewRatchetRejectsBackdatedCreatorWithoutRevoke(t *testing.T) {
	migrations := []viewMigration{
		{name: "202501010001_backdated_views.sql", version: 202501010001, up: "create view sforum_core_v1.safe_users as select 1"},
		{name: stableCoreViewsPublicRevokeMigration, version: 202611150002, up: strings.ToLower(schemaLevelViewRevoke)},
	}
	if _, err := checkStableCoreViewRevokes(migrations, viewCreatorRevokeExemptions); err == nil {
		t.Fatal("a backdated view-creating migration without its own REVOKE must fail the ratchet")
	}
	// 同一份输入，只要它自带 REVOKE 就必须通过（正例，防止棘轮只会红不会绿）。
	migrations[0].up += "; " + strings.ToLower(schemaLevelViewRevoke)
	if _, err := checkStableCoreViewRevokes(migrations, viewCreatorRevokeExemptions); err != nil {
		t.Fatalf("self-contained REVOKE must pass: %v", err)
	}
}

// 豁免表腐烂会悄悄放宽规则：登记的文件被删/改名后必须报错，而不是静默失效。
func TestStableCoreViewRatchetRejectsStaleExemption(t *testing.T) {
	migrations := []viewMigration{
		{name: "202501010001_views.sql", version: 202501010001, up: "create view sforum_core_v1.safe_users as select 1; " + strings.ToLower(schemaLevelViewRevoke)},
	}
	exemptions := map[string]string{"202501010001_views.sql": stableCoreViewsPublicRevokeMigration}
	if err := checkViewCreatorExemptions(migrations, exemptions); err != nil {
		t.Fatalf("an exemption that still creates views must be accepted: %v", err)
	}
	if err := checkViewCreatorExemptions(migrations, map[string]string{"missing.sql": stableCoreViewsPublicRevokeMigration}); err == nil {
		t.Fatal("a stale exemption must fail")
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
