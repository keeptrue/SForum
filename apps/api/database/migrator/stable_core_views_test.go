package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestStableCoreViewsProductionMigrationContract(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	if err := Up(ctx, Config{DatabaseURL: databaseURL}); err != nil {
		t.Fatalf("apply stable core views migration: %v", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	assertStableCoreViewOwnershipAndACL(t, ctx, db)
	assertStableCoreViewContracts(t, ctx, db)
}

func assertStableCoreViewOwnershipAndACL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var databaseName string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	ownerRole, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	var schemaOwned, publicSchemaPrivileges bool
	if err := db.QueryRowContext(ctx, `
		SELECT namespace.nspowner = roles.oid,
		       EXISTS (
		         SELECT 1
		         FROM aclexplode(COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))) AS acl
		         WHERE acl.grantee = 0
		       )
		FROM pg_namespace AS namespace
		JOIN pg_roles AS roles ON roles.rolname = $1
		WHERE namespace.nspname = 'sforum_core_v1'
	`, ownerRole).Scan(&schemaOwned, &publicSchemaPrivileges); err != nil {
		t.Fatal(err)
	}
	if !schemaOwned || publicSchemaPrivileges {
		t.Fatalf("stable schema owner/public ACL = %v/%v", schemaOwned, publicSchemaPrivileges)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT class.relname,
		       owner.rolname = $1,
		       COALESCE(class.reloptions @> ARRAY['security_barrier=true'], FALSE),
		       COALESCE(class.reloptions @> ARRAY['security_invoker=false'], FALSE),
		       NOT EXISTS (
		         SELECT 1
		         FROM aclexplode(COALESCE(class.relacl, acldefault('r', class.relowner))) AS acl
		         WHERE acl.grantee = 0
		       )
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
		JOIN pg_roles AS owner ON owner.oid = class.relowner
		WHERE namespace.nspname = 'sforum_core_v1' AND class.relkind = 'v'
		ORDER BY class.relname
	`, ownerRole)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var name string
		var owned, barrier, definer, publicDenied bool
		if err := rows.Scan(&name, &owned, &barrier, &definer, &publicDenied); err != nil {
			t.Fatal(err)
		}
		if !owned || !barrier || !definer || !publicDenied {
			t.Fatalf("view %s owner/barrier/definer/public-denied = %v/%v/%v/%v",
				name, owned, barrier, definer, publicDenied)
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range stableCoreViewNames() {
		if !seen[name] {
			t.Fatalf("stable core view %s is missing", name)
		}
	}
}

func assertStableCoreViewContracts(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	expected := map[string]string{
		"safe_users":                 "id,username,display_name,created_at,updated_at",
		"forum_topics":               "id,category_id,category_slug,author_user_id,title,slug,status,is_pinned,comment_count,view_count,last_activity_at,created_at,updated_at,html_content,plain_text,source_format,render_version,content_hash",
		"forum_comments":             "id,topic_id,author_user_id,parent_comment_id,root_comment_id,path_key,depth,reply_count,created_at,updated_at,html_content,plain_text,source_format,render_version,content_hash",
		"public_entity_meta":         "entity_type,entity_id,field_key,value_type,value_text,owner_extension_id,updated_at",
		"public_attachment_metadata": "id,public_id,owner_user_id,original_name,content_type,extension,size_bytes,sha256,image_width,image_height,reference_count,created_at,updated_at",
	}
	for name, columns := range expected {
		var actual, updatable, insertable string
		if err := db.QueryRowContext(ctx, `
			SELECT string_agg(columns.column_name, ',' ORDER BY columns.ordinal_position),
			       views.is_updatable, views.is_insertable_into
			FROM information_schema.columns AS columns
			JOIN information_schema.views AS views
			  ON views.table_schema = columns.table_schema AND views.table_name = columns.table_name
			WHERE columns.table_schema = 'sforum_core_v1' AND columns.table_name = $1
			GROUP BY views.is_updatable, views.is_insertable_into
		`, name).Scan(&actual, &updatable, &insertable); err != nil {
			t.Fatal(err)
		}
		if actual != columns || updatable != "NO" || insertable != "NO" {
			t.Fatalf("view %s columns/updatable/insertable = %q/%q/%q", name, actual, updatable, insertable)
		}
	}
}

func stableCoreViewNames() []string {
	return []string{
		"forum_comments", "forum_topics", "public_attachment_metadata", "public_entity_meta", "safe_users",
	}
}
