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
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

const notificationReferenceRecipientID = int64(17001)

func TestNotificationReferencePluginEmitsThroughRealBroker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping notification reference plugin subprocess build in short mode")
	}
	extension := buildReferenceFixtureExtension(
		t,
		"sforum-notification-reference",
		"sforum.notification-reference",
		804,
		map[string]string{"__PAYLOAD_SCHEMA_DIGEST__": "schemas/order-ready.json"},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := newNotificationReferenceDatabase(t, ctx)
	grantID := seedNotificationReferenceArtifact(t, ctx, pool, &extension)

	registry := notifications.NewPersistentRegistry(pool)
	owner := notifications.DescriptorOwner{
		ExtensionID: extension.ID, Version: extension.Version, ArtifactDigest: extension.PackageDigest,
	}
	if _, err := registry.Publish(ctx, owner, extension.Manifest.NotificationTypes, registry.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE notification_type_policies
		SET enabled=TRUE,recommended_enabled=TRUE
		WHERE type='sforum.notification-reference.order_ready' AND channel='in_app'`); err != nil {
		t.Fatal(err)
	}

	authority, err := hostapi.NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	commandRuntime, err := hostapi.NewPostgresProtocolV2CommandRuntime(hostapi.PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: authority,
		Jobs:                 supportjobs.NewDispatcher(notificationReferenceRiverClient{}),
		Moderation:           moderation.NewPostgresStore(pool),
		AttachmentStatuses:   protocolV2RouteAttachmentMutator{},
		AttachmentExtensions: protocolV2RouteAttachmentMutator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, _ := newProtocolV2HostGateway()
	if err := gateway.BindProtocolV2CommandRuntime(commandRuntime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: strconv.FormatInt(grantID, 10), ImpactDigest: extension.PackageDigest,
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	invoke := func(idempotencyKey string) extensionsruntime.HookResult {
		t.Helper()
		return starter.InvokeHook(ctx, extension, extensionsruntime.HookInput{
			DeclarationID:   "sforum.notification-reference.hook.emit",
			Name:            "sforum.notification-reference.emit-requested",
			Kind:            "observe",
			ContractVersion: "sforum.notification-reference.hook.emit@1",
			Timeout:         5 * time.Second,
			Payload: map[string]any{
				"recipientUserId": strconv.FormatInt(notificationReferenceRecipientID, 10),
				"orderId":         "order-real-broker-1",
				"idempotencyKey":  idempotencyKey,
			},
		})
	}

	if result := invoke("notification-reference-1"); !result.OK {
		t.Fatalf("real Broker notification emission = %#v", result)
	}
	assertNotificationReferenceCounts(t, ctx, pool, 1, 1)
	var payload, target []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload,target_meta FROM notifications
		WHERE type='sforum.notification-reference.order_ready' AND recipient_user_id=$1`, notificationReferenceRecipientID,
	).Scan(&payload, &target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "order-real-broker-1") ||
		!strings.Contains(string(target), "sforum.notification-reference.route.orders") {
		t.Fatalf("notification projection payload=%s target=%s", payload, target)
	}

	if result := invoke("notification-reference-1"); !result.OK {
		t.Fatalf("real Broker idempotency replay = %#v", result)
	}
	assertNotificationReferenceCounts(t, ctx, pool, 1, 1)

	if _, err := pool.Exec(ctx, `UPDATE extension_trust_grants SET revoked_at=statement_timestamp() WHERE id=$1`, grantID); err != nil {
		t.Fatal(err)
	}
	if result := invoke("notification-reference-after-revoke"); result.OK {
		t.Fatalf("revoked exact trust grant reached notification command: %#v", result)
	}
	assertNotificationReferenceCounts(t, ctx, pool, 1, 1)
}

func newNotificationReferenceDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := commerceTestDatabaseURL(t)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("notification_reference_%d", time.Now().UnixNano())
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
		// 在 schema 隔离 harness 下必然互相冲突，按命名约定排除。
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
			t.Fatal(err)
		}
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	return pool
}

func seedNotificationReferenceArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extension *extensions.Extension) int64 {
	t.Helper()
	manifest, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id,username,username_lower,email,email_lower,display_name,status)
		VALUES ($1,'notification-recipient','notification-recipient','notification-recipient@example.test','notification-recipient@example.test','Notification Recipient','active')`, notificationReferenceRecipientID); err != nil {
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

func assertNotificationReferenceCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, notificationsWant, receiptsWant int) {
	t.Helper()
	var notificationCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE type='sforum.notification-reference.order_ready'`).Scan(&notificationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_host_command_receipts
		WHERE extension_id='sforum.notification-reference' AND command_id='notifications.emit'`,
	).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if notificationCount != notificationsWant || receiptCount != receiptsWant {
		t.Fatalf("notification reference counts notifications=%d receipts=%d, want %d/%d", notificationCount, receiptCount, notificationsWant, receiptsWant)
	}
}

type notificationReferenceRiverClient struct{}

func (notificationReferenceRiverClient) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}
func (notificationReferenceRiverClient) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}
func (notificationReferenceRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, nil
}
