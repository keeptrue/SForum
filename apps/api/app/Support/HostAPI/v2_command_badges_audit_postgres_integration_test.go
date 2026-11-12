package hostapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真库用例：域层拒绝的审计**行**确实落库，且 action/reason 值级相等。
//
// 单测里用 fake auditor 只能证明「调了审计接口」；这里证明生产实现写出的行能被
// 运维查询命中——「有审计」与「审计里写着哪个 reason」是两件事。
func TestBadgeFactDeniedAuditLandsInAuditEvents(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("badge_fact_audit_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
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
	// 只建这一张表：审计实现只碰它，最小 schema 让失败点唯一。
	if _, err := pool.Exec(ctx, `CREATE TABLE audit_events (
		id BIGSERIAL PRIMARY KEY,
		actor_user_id BIGINT,
		action TEXT NOT NULL,
		metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("create audit_events: %v", err)
	}

	auditor := postgresBadgeFactAuditor{pool: pool}
	if err := auditor.AuditDenied(ctx, protocolV2BadgeFactDeniedAudit{
		ExtensionID: "acme.wiki",
		FactKey:     "acme.wiki.unlisted",
		Reason:      BadgeFactReasonKeyNotDeclared,
		UserID:      7,
	}); err != nil {
		t.Fatalf("审计写入失败：%v", err)
	}

	var action, reason, factKey, extensionID, userID string
	var actor *int64
	var metadata []byte
	if err := pool.QueryRow(ctx, `SELECT actor_user_id, action, metadata FROM audit_events`).
		Scan(&actor, &action, &metadata); err != nil {
		t.Fatalf("读审计行失败：%v", err)
	}
	// 两条分开断言：常量不得漂移（冻结字面量），行里的 action 必须等于该常量。
	if BadgeFactRecordDeniedAuditAction != "extension.badge_fact_record.denied" {
		t.Fatalf("审计 action 常量被改动：%q", BadgeFactRecordDeniedAuditAction)
	}
	if action != BadgeFactRecordDeniedAuditAction {
		t.Fatalf("action = %q，必须等于 %q", action, BadgeFactRecordDeniedAuditAction)
	}
	if actor != nil {
		t.Fatalf("拒绝审计不该伪造 actor，得到 %v", *actor)
	}
	var fields map[string]any
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatalf("metadata 不是 JSON：%v", err)
	}
	reason, _ = fields["reason"].(string)
	factKey, _ = fields["factKey"].(string)
	extensionID, _ = fields["extensionId"].(string)
	userID, _ = fields["userId"].(string)
	if reason != BadgeFactReasonKeyNotDeclared {
		t.Fatalf("metadata.reason = %q，want %q", reason, BadgeFactReasonKeyNotDeclared)
	}
	if factKey != "acme.wiki.unlisted" || extensionID != "acme.wiki" {
		t.Fatalf("metadata 必须带 extensionId/factKey，得到 %v", fields)
	}
	if userID != "7" {
		t.Fatalf("userId 必须是十进制字符串，得到 %q", userID)
	}
}
