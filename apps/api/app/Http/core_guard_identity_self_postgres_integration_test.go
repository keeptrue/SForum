package http

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresIdentitySelfResourceGuardObservesCrossNodeOwnership(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	schema := fmt.Sprintf("identity_self_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	for _, statement := range []string{
		`CREATE TABLE ` + quotedSchema + `.user_sessions (
			sid TEXT PRIMARY KEY, user_id BIGINT NOT NULL, revoked_at TIMESTAMPTZ
		)`,
		`CREATE TABLE ` + quotedSchema + `.api_tokens (
			id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, revoked_at TIMESTAMPTZ
		)`,
		`INSERT INTO ` + quotedSchema + `.user_sessions (sid, user_id) VALUES ('session-7', 42)`,
		`INSERT INTO ` + quotedSchema + `.api_tokens (id, user_id) VALUES (7, 42)`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install self-resource fixture: %v\n%s", err, statement)
		}
	}

	nodeA := openIdentitySelfGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openIdentitySelfGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	sessions := identity.NewPostgresStore(nodeA)
	tokens := apitokens.NewPostgresStore(nodeA)
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{
		IdentitySessions: sessions, IdentityAPITokens: tokens,
	})

	sessionPlan, sessionStep := productionIdentitySelfResourcePlan(t, "core.route.identity.revoke_session")
	sessionRequest := productionGuardRequest()
	sessionRequest.Method, sessionRequest.Path, sessionRequest.Params, sessionRequest.CredentialSource =
		sessionPlan.Method(), sessionPlan.Path(), sessionPlan.Params(), routes.DispatchCredentialBearer
	if err := authorizer.Authorize(ctx, sessionPlan, sessionStep, sessionRequest); err != nil {
		t.Fatalf("initial session owner: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE user_sessions SET user_id = 99 WHERE sid = 'session-7'`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, sessionPlan, sessionStep, sessionRequest); err == nil {
		t.Fatal("node A authorized stale session ownership")
	}

	tokenPlan, tokenStep := productionIdentitySelfResourcePlan(t, "core.route.identity.rotate_apitoken")
	tokenRequest := productionGuardRequest()
	tokenRequest.Method, tokenRequest.Path, tokenRequest.Params, tokenRequest.CredentialSource =
		tokenPlan.Method(), tokenPlan.Path(), tokenPlan.Params(), routes.DispatchCredentialCookie
	if err := authorizer.Authorize(ctx, tokenPlan, tokenStep, tokenRequest); err != nil {
		t.Fatalf("initial token owner: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE api_tokens SET user_id = 99 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, tokenPlan, tokenStep, tokenRequest); err == nil {
		t.Fatal("node A authorized stale token ownership")
	}
}

func openIdentitySelfGuardNode(t *testing.T, ctx context.Context, databaseURL, schema string) *pgxpool.Pool {
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
