package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

// newMigratedFixtureDatabase 在一次性 schema 上应用完整迁移，并返回绑定该 schema
// 的连接池。reference fixture 的集成测试都需要它：真实表结构 + 真实约束，跑完即
// 丢弃，绝不触碰任何被继承的开发数据库。
func newMigratedFixtureDatabase(t *testing.T, ctx context.Context, prefix string) *pgxpool.Pool {
	t.Helper()
	databaseURL := commerceTestDatabaseURL(t)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
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
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	db := stdlib.OpenDB(*config.ConnConfig)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		// sforum_core_v1 是全局 schema：稳定核心视图迁移（*_stable_core_views）
		// 在 schema 隔离 harness 下必然互相冲突，按命名约定排除；真实 dev/生产
		// 库由启动迁移负责交付这些视图。
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") && !strings.Contains(entry.Name(), "_stable_core_views") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		versionText, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %s has no version", name)
		}
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return pool
}

// seedReferenceArtifact 写入扩展行、活动版本行与精确制品信任授权，并回填
// ActiveVersionID。返回的 grant id 是运行期握手要用的信任依据。
func seedReferenceArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extension *extensions.Extension) int64 {
	t.Helper()
	manifest, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id,type,name,status,source,is_system,is_deletable)
		VALUES ($1,'plugin',$2,'enabled','uploaded',FALSE,TRUE)`, extension.ID, extension.Name); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id,version,manifest,package_path,package_digest)
		VALUES ($1,$2,$3::jsonb,$4,$5) RETURNING id`, extension.ID, extension.Version, manifest, extension.PackagePath, extension.PackageDigest,
	).Scan(&extension.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id=$2 WHERE id=$1`, extension.ID, extension.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (
		  extension_id,extension_version,package_digest,action,artifact_digests,impact_document,impact_digest
		) VALUES ($1,$2,$3,'enable','{}'::jsonb,$4::jsonb,$5)
		RETURNING id`, extension.ID, extension.Version, extension.PackageDigest, manifest, extension.PackageDigest,
	).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	return grantID
}
