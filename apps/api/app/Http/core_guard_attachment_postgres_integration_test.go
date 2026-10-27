package http

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresAttachmentReadGuardObservesCrossNodeResourceAuthority(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	schema := fmt.Sprintf("attachment_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	for _, statement := range []string{
		`CREATE TABLE ` + quotedSchema + `.categories (id BIGINT PRIMARY KEY, visibility TEXT NOT NULL)`,
		`CREATE TABLE ` + quotedSchema + `.topics (
			id BIGINT PRIMARY KEY, author_user_id BIGINT NOT NULL, status TEXT NOT NULL,
			category_id BIGINT NOT NULL, content_id BIGINT UNIQUE
		)`,
		`CREATE TABLE ` + quotedSchema + `.comments (
			id BIGINT PRIMARY KEY, author_user_id BIGINT NOT NULL, status TEXT NOT NULL,
			topic_id BIGINT NOT NULL, content_id BIGINT UNIQUE
		)`,
		// content_type 是 LoadReadGuardSubject 的读列之一（image/text 判定），
		// 缺了它查询会直接报错，而错误在守卫层被归一成「guard unavailable」——
		// 表现为一条与真正原因无关的失败信息（本次即由此发现该 fixture 早已漂移）。
		`CREATE TABLE ` + quotedSchema + `.attachments (
			id BIGINT PRIMARY KEY, public_id TEXT NOT NULL UNIQUE, owner_user_id BIGINT,
			status TEXT NOT NULL, visibility TEXT NOT NULL,
			content_type TEXT NOT NULL DEFAULT 'application/octet-stream'
		)`,
		`CREATE TABLE ` + quotedSchema + `.attachment_references (
			id BIGINT PRIMARY KEY, attachment_id BIGINT NOT NULL, resource_type TEXT NOT NULL,
			resource_id BIGINT NOT NULL, context TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL
		)`,
		`INSERT INTO ` + quotedSchema + `.categories VALUES (1, 'public')`,
		`INSERT INTO ` + quotedSchema + `.topics VALUES (1, 42, 'active', 1, 101)`,
		`INSERT INTO ` + quotedSchema + `.attachments VALUES (1, 'cross-node-public-id', 42, 'active', 'public')`,
		`INSERT INTO ` + quotedSchema + `.attachment_references VALUES (1, 1, 'topic', 1, 'inline', statement_timestamp())`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install attachment guard fixture: %v\n%s", err, statement)
		}
	}
	nodeA := openAttachmentGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openAttachmentGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	store := attachments.NewPostgresStore(nodeA)
	authorizer := attachmentReadAuthorizer(store, "public")
	plan, step := productionAttachmentReadPlan(t, "core.route.attachments.content", "cross-node-public-id")

	anonymous := routes.DispatchRequest{Method: plan.Method(), Path: plan.Path(), Params: plan.Params()}
	if err := authorizer.Authorize(ctx, plan, step, anonymous); err != nil {
		t.Fatalf("public forum attachment guard: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE categories SET visibility = 'private' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	member := productionGuardRequest()
	member.ActorID = 7
	member.Method, member.Path, member.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(ctx, plan, step, member); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("cross-node category visibility error=%v", err)
	}
	moderator := productionGuardRequest(identity.PermissionModerationReview)
	moderator.ActorID = 7
	moderator.Method, moderator.Path, moderator.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(ctx, plan, step, moderator); err != nil {
		t.Fatalf("moderator hidden-reference guard: %v", err)
	}

	if _, err := nodeB.Exec(ctx, `UPDATE categories SET visibility = 'public'; UPDATE topics SET status = 'pending' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	owner := productionGuardRequest()
	owner.ActorID = 42
	owner.Method, owner.Path, owner.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(ctx, plan, step, owner); err != nil {
		t.Fatalf("pending attachment author guard: %v", err)
	}
	if err := authorizer.Authorize(ctx, plan, step, member); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("pending foreign member error=%v", err)
	}

	if _, err := nodeB.Exec(ctx, `UPDATE attachments SET visibility = 'private' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, plan, step, owner); err != nil {
		t.Fatalf("private attachment owner guard: %v", err)
	}
	if err := authorizer.Authorize(ctx, plan, step, moderator); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("moderator bypassed private attachment: %v", err)
	}
	manager := productionGuardRequest(identity.PermissionAttachmentManage)
	manager.ActorID = 7
	manager.Method, manager.Path, manager.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(ctx, plan, step, manager); err != nil {
		t.Fatalf("attachment manager private guard: %v", err)
	}

	if _, err := nodeB.Exec(ctx, `
		UPDATE attachments SET status = 'active', visibility = 'public' WHERE id = 1;
		DELETE FROM attachment_references WHERE attachment_id = 1;
		INSERT INTO attachment_references VALUES (2, 1, 'site', 1, 'logo', statement_timestamp())
	`); err != nil {
		t.Fatal(err)
	}
	protectedAuthorizer := attachmentReadAuthorizer(store, "login_required")
	if err := protectedAuthorizer.Authorize(ctx, plan, step, anonymous); err != nil {
		t.Fatalf("site-public reference inherited forum guest policy: %v", err)
	}

	missingPlan, missingStep := productionAttachmentReadPlan(t, "core.route.attachments.get", "missing-public-id")
	missing := routes.DispatchRequest{Method: missingPlan.Method(), Path: missingPlan.Path(), Params: missingPlan.Params()}
	if err := protectedAuthorizer.Authorize(ctx, missingPlan, missingStep, missing); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("missing attachment error=%v", err)
	}
}

func openAttachmentGuardNode(t *testing.T, ctx context.Context, databaseURL, schema string) *pgxpool.Pool {
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
