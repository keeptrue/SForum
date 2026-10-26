package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

const productionLifecycleE2EHelperEnv = "SFORUM_PRODUCTION_LIFECYCLE_E2E_HELPER"

func TestProductionLifecycleStackUninstallsPreservedDataThroughRealRuntimeAndPostgres(t *testing.T) {
	ctx, pool := openProductionLifecycleE2EPool(t)
	fixture := newProductionLifecycleE2EFixture(t, ctx, pool, false)

	input := extensions.UninstallInput{
		IdempotencyKey: "p4-e2e-uninstall-preserve",
		RemovalMode:    extensions.LifecycleRemovalPreserve,
	}
	result, err := fixture.service.UninstallWithResult(ctx, fixture.actor, fixture.extension.ID, input)
	if err != nil {
		logProductionLifecycleE2EFailure(t, ctx, pool, fixture.extension.ID)
		t.Fatalf("production lifecycle uninstall: %v", err)
	}
	assertProductionLifecycleE2EResult(t, result, false)
	assertProductionLifecycleE2EDurablePurge(t, ctx, pool, fixture, result.OperationID)

	replayed, err := fixture.service.UninstallWithResult(ctx, fixture.actor, fixture.extension.ID, input)
	if err != nil {
		t.Fatalf("replay production lifecycle uninstall: %v", err)
	}
	assertProductionLifecycleE2EResult(t, replayed, true)
	if replayed.OperationID != result.OperationID {
		t.Fatalf("replay operation id = %d, want %d", replayed.OperationID, result.OperationID)
	}
}

// 这个回归必须从真实 enable publication 开始。旧夹具直接写 enabled，缺少
// lifecycle registry journal 的 source fence，无法覆盖正常 disable 的 exact
// publication 退役路径。
func TestProductionLifecycleStackDisablesExactAuthIdentityPublication(t *testing.T) {
	ctx, pool := openProductionLifecycleE2EPool(t)
	fixture := newProductionLifecycleE2EFixture(t, ctx, pool, true)

	disabled, err := fixture.service.DisableWithInput(ctx, fixture.actor, fixture.extension.ID, extensions.LifecycleRequestInput{
		IdempotencyKey: "p4-e2e-disable-auth-provider",
	})
	if err != nil {
		logProductionLifecycleE2EFailure(t, ctx, pool, fixture.extension.ID)
		t.Fatalf("production lifecycle disable: %v", err)
	}
	if disabled.Status != extensions.StatusDisabled {
		t.Fatalf("disabled extension status = %q", disabled.Status)
	}
	if _, err := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID); err == nil {
		t.Fatal("exact provider runtime remains active after disable")
	}
	if _, found := fixture.identityRegistry.SnapshotPublication(fixture.extension.ID); found {
		t.Fatal("live Identity Registry publication remains after disable")
	}

	durable, err := fixture.identityStore.LoadDurableState(ctx)
	if err != nil {
		t.Fatalf("load durable Identity Registry state: %v", err)
	}
	if err := identityregistry.ValidateDurableRetirement(durable, fixture.extension.ID); err != nil {
		t.Fatalf("durable Identity Registry retirement: %v", err)
	}
	var activeRootCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM (
			SELECT DISTINCT ON (owner_extension_id) registry_state
			FROM extension_identity_registry_publications
			WHERE owner_extension_id = $1
			ORDER BY owner_extension_id, revision DESC
		) current
		WHERE registry_state = 'active'
	`, fixture.extension.ID).Scan(&activeRootCount); err != nil {
		t.Fatal(err)
	}
	if activeRootCount != 0 {
		t.Fatalf("active durable identity roots remain after disable: %d", activeRootCount)
	}
}

func logProductionLifecycleE2EFailure(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID string,
) {
	t.Helper()
	repository := extensions.NewPostgresLifecycleRepository(pool)
	operations, err := repository.ListOperations(ctx, extensionID, 10)
	if err != nil {
		t.Logf("list failed lifecycle operations: %v", err)
		return
	}
	for _, operation := range operations {
		steps, stepsErr := repository.ListStepAttempts(ctx, operation.ID)
		t.Logf(
			"failed lifecycle operation id=%d operation=%s state=%s step=%s terminal=%s error=%s/%s",
			operation.ID, operation.Operation, operation.State, operation.CurrentStepID,
			operation.TerminalResult, operation.Error.Code, operation.Error.Reason,
		)
		for _, step := range steps {
			t.Logf(
				"failed lifecycle step id=%d step=%s attempt=%d status=%s actor=%d audit=%d error=%s/%s",
				step.ID, step.StepID, step.Attempt, step.Status, step.ActorUserID, step.AuditEventID,
				step.Error.Code, step.Error.Reason,
			)
		}
		if stepsErr != nil {
			t.Logf("list failed lifecycle steps: %v", stepsErr)
		}
	}
}

func TestProductionLifecycleE2EHelperProcess(t *testing.T) {
	if os.Getenv(productionLifecycleE2EHelperEnv) != "1" {
		return
	}
	server := pluginv2sdk.NewServer().WithFeatures(pluginv2sdk.IdentityRuntimeProtocolFeature()).WithRuntimeStreams(pluginv2sdk.RuntimeStreams{
		Lifecycle: func(_ context.Context, request *protocolwire.LifecycleRequest, progress *pluginv2sdk.ProgressStream) error {
			extensionID := request.GetContext().GetExtension().GetExtensionId()
			if !strings.HasPrefix(extensionID, "p4.e2e.") ||
				request.GetPlanVersion() != extensionID+".lifecycle@1" ||
				!strings.HasPrefix(request.GetStepId(), "lifecycle.") {
				return &pluginv2sdk.RuntimeStreamError{
					Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
					Reason: "p4.e2e.lifecycle_request_invalid", Message: "Unexpected lifecycle request.",
				}
			}
			if err := progress.Send(&protocolwire.ProgressUpdate{
				StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
				CompletedUnits: 0, TotalUnits: 1, Checkpoint: "running",
			}); err != nil {
				return err
			}
			value, err := structpb.NewStruct(map[string]any{"action": request.GetAction().String()})
			if err != nil {
				return err
			}
			return progress.Send(&protocolwire.ProgressUpdate{
				StepId: request.GetStepId(), State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
				CompletedUnits: 1, TotalUnits: 1, Checkpoint: "done",
				Result: &protocolwire.TypedDocument{
					SchemaId: extensionID + ".lifecycle.progress", SchemaVersion: "1", Value: value,
				},
			})
		},
	})
	pluginv2sdk.Serve(server)
	os.Exit(0)
}

type productionLifecycleE2EFixture struct {
	actor            identity.Actor
	extension        extensions.Extension
	manager          *extensionsruntime.Manager
	service          *extensions.Service
	identityRegistry *identityregistry.Registry
	identityStore    identityregistry.PublicationStore
	identifiers      extensionsruntime.ExtensionDatabaseIdentifiers
}

func newProductionLifecycleE2EFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	withAuthIdentity bool,
) productionLifecycleE2EFixture {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	extensionID := "p4.e2e.preserve." + suffix
	actor := insertProductionLifecycleE2EActor(t, ctx, pool, suffix)
	extensionRoot := t.TempDir()
	manifest, packagePath, packageDigest := writeProductionLifecycleE2EPackage(t, extensionRoot, extensionID, withAuthIdentity)
	store := extensions.NewPostgresStore(pool)
	extension, err := store.SaveInstalled(ctx, extensions.SaveInstalledInput{
		Manifest: manifest, PackagePath: packagePath, PackageDigest: packageDigest,
	})
	if err != nil {
		t.Fatalf("save production lifecycle fixture: %v", err)
	}
	if _, err := store.EnsureInitialPluginRuntimePublication(ctx); err != nil {
		t.Fatalf("ensure plugin runtime genesis before lifecycle fixture enable: %v", err)
	}
	assertPluginRuntimeGenesisHeader(t, ctx, pool)
	writer := audit.NewPostgresWriter(pool)
	trust := extensions.NewExecutableTrustService(store, extensions.NewPostgresExecutableTrustStore(pool)).WithAuditor(writer)

	provisionAuditID, err := writer.AppendReturningID(ctx, audit.Event{
		ActorUserID: actor.ID, Action: "extension.database_provision",
		Metadata: map[string]any{"extensionId": extensionID, "source": "p4_e2e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := extensionsruntime.ExtensionDatabaseArtifact{
		ExtensionID: extension.ID, Version: extension.Version,
		VersionID: extension.ActiveVersionID, PackageDigest: extension.PackageDigest,
	}
	registry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	if _, err := registry.ProvisionOwnSchema(ctx, extensionsruntime.ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: actor.ID, AuditEventID: provisionAuditID,
	}); err != nil {
		t.Fatalf("provision production lifecycle own schema: %v", err)
	}
	identifiers, err := extensionsruntime.ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: trust, DatabaseLeases: registry,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	pageRegistry := pages.NewRegistry(nil)
	stack, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: store, Features: lifecycleFeatureFacts{}, Trust: trust,
		Runtime: manager, Pages: pageRegistry, Services: hostapi.NewServiceRegistry(),
		Caches: cacheregistry.New(),
		River:  lifecycleRiverClient{}, ExtensionRoot: extensionRoot, QueryCursorSecret: bootstrapQueryCursorSecret(),
		MigrationEngine: extensionsruntime.NewPostgresLifecycleMigrationEngine(pool, nil),
		Database:        extensionsruntime.NewPostgresExtensionDatabaseDisposition(pool),
	})
	if err != nil {
		t.Fatalf("construct production lifecycle stack: %v", err)
	}
	service := extensions.NewServiceWithOptions(
		store, extensionRoot, "", manager,
		extensions.WithAuditor(writer), extensions.WithExecutableTrust(trust, true),
	)
	if err := stack.bindService(service); err != nil {
		t.Fatalf("bind production lifecycle service: %v", err)
	}
	if withAuthIdentity {
		challenge, challengeErr := trust.Challenge(ctx, actor, extensionID)
		if challengeErr != nil {
			t.Fatalf("issue exact trust challenge: %v", challengeErr)
		}
		enabled, enableErr := service.Enable(ctx, actor, extensionID, extensions.EnableInput{
			ConfirmCapabilities: true, ConfirmationToken: challenge.Token, IdempotencyKey: "p4-e2e-enable-exact-artifact",
		})
		if enableErr != nil {
			logProductionLifecycleE2EFailure(t, ctx, pool, extensionID)
			t.Fatalf("enable exact lifecycle fixture through production service: %v", enableErr)
		}
		if enabled.Status != extensions.StatusEnabled {
			t.Fatalf("enabled fixture status = %q", enabled.Status)
		}
		extension = enabled
	} else {
		if _, updateErr := pool.Exec(ctx, `UPDATE extensions SET status = 'enabled' WHERE id = $1`, extensionID); updateErr != nil {
			t.Fatalf("enable original uninstall fixture: %v", updateErr)
		}
		var loadErr error
		extension, loadErr = store.Get(ctx, extensionID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		challenge, challengeErr := trust.Challenge(ctx, actor, extensionID)
		if challengeErr != nil {
			t.Fatalf("issue original fixture trust challenge: %v", challengeErr)
		}
		authority, authorityErr := trust.ConfirmLifecycleAuthority(ctx, actor, extension, challenge.Token)
		if authorityErr != nil {
			t.Fatalf("confirm original fixture lifecycle authority: %v", authorityErr)
		}
		seedProductionLifecycleE2EAuthority(t, ctx, pool, writer, actor, extension, authority)
		if startErr := manager.Start(ctx, extension); startErr != nil {
			t.Fatalf("start original protocol-v2 fixture runtime: %v", startErr)
		}
	}

	fixture := productionLifecycleE2EFixture{
		actor: actor, extension: extension, manager: manager, service: service,
		identityRegistry: stack.IdentityRegistry, identityStore: stack.IdentityStore, identifiers: identifiers,
	}
	t.Cleanup(func() {
		_ = manager.Stop(context.Background(), extension)
		cleanupProductionLifecycleE2EFixture(pool, fixture)
	})
	return fixture
}

func openProductionLifecycleE2EPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func insertProductionLifecycleE2EActor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix string,
) identity.Actor {
	t.Helper()
	username := "p4_e2e_" + suffix
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'P4 lifecycle E2E')
		RETURNING id
	`, username, username+"@example.test").Scan(&actorID); err != nil {
		t.Fatalf("insert lifecycle E2E actor: %v", err)
	}
	return identity.Actor{
		ID: actorID, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin},
	}
}

func writeProductionLifecycleE2EPackage(
	t *testing.T,
	extensionRoot string,
	extensionID string,
	withAuthIdentity bool,
) (extensions.Manifest, string, string) {
	t.Helper()
	packagePath := filepath.Join(extensionRoot, extensionID, "1.0.0")
	if err := os.MkdirAll(filepath.Join(packagePath, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\n" + productionLifecycleE2EHelperEnv + "=1 exec " +
		productionLifecycleE2EShellQuote(binary) +
		" -test.run='^TestProductionLifecycleE2EHelperProcess$' -- \"$@\"\n"
	backendPath := filepath.Join(packagePath, "backend", "plugin")
	if err := os.WriteFile(backendPath, []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	backendHash := sha256.Sum256([]byte(launcher))
	backendDigest := hex.EncodeToString(backendHash[:])
	operation := func() *extensionmanifest.ManifestLifecycleOperation {
		return &extensionmanifest.ManifestLifecycleOperation{
			Plan: "lifecycle.operation.plan", Execute: "lifecycle.operation.execute",
			ProgressSchema:   extensionID + ".lifecycle.progress@1",
			CheckpointSchema: extensionID + ".lifecycle.checkpoint@1",
		}
	}
	manifest := extensions.Manifest{
		ManifestVersion: 3, ID: extensionID, Name: "P4 lifecycle E2E",
		Description: "Production lifecycle integration fixture.", URL: "https://example.test/p4-e2e",
		Author: extensions.ManifestAuthor{Name: "SForum tests"}, Version: "1.0.0",
		Type: extensions.TypePlugin, SForumVersion: ">=1.0.0",
		Backend: extensions.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: backendDigest, HostAPIVersion: "sforum.host@2",
		},
		Database: &extensions.ManifestDatabase{
			ContractVersion: extensionID + ".database@1", Authority: "own_schema",
			Schema: "logical_schema", Role: "logical_role",
			Retention: extensionmanifest.ManifestRetention{OnDisable: "retain", OnUninstall: "retain"},
		},
		Lifecycle: &extensions.ManifestLifecycle{
			ContractVersion: extensionID + ".lifecycle@1", Install: operation(), Enable: operation(), Disable: operation(), Uninstall: operation(),
		},
		PackageFiles: []extensions.ManifestPackageFile{{
			ID: extensionID + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: backendDigest,
		}},
	}
	if withAuthIdentity {
		manifest.Identity = &extensions.ManifestIdentity{
			ContractVersion: extensionID + ".identity@1",
			Providers: []extensions.ManifestIdentityProvider{{
				ID: extensionID + ".auth", ContractVersion: extensionID + ".auth@1", Kind: "auth",
				Handler: extensionID + ".identity", Priority: 100,
				Operations: []extensions.ManifestIdentityProviderOperation{{
					Name: "login.start", InputSchema: extensionID + ".start.input@1", OutputSchema: extensionID + ".start.output@1",
					TimeoutMS: 1000, FailurePolicy: "fail_closed",
				}},
			}},
		}
		writeProductionLifecycleE2ESchema(t, &manifest, packagePath, extensionID+".start.input", "schemas/start-input.json", `{"type":"object"}`)
		writeProductionLifecycleE2ESchema(t, &manifest, packagePath, extensionID+".start.output", "schemas/start-output.json", `{"type":"object"}`)
	}
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("validate lifecycle E2E manifest: %v", err)
	}
	manifestBody, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, extensionmanifest.ManifestFileName), manifestBody, 0o600); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, packagePath, packageDigest
}

func writeProductionLifecycleE2ESchema(
	t *testing.T,
	manifest *extensions.Manifest,
	packagePath string,
	id string,
	path string,
	body string,
) {
	t.Helper()
	fullPath := filepath.Join(packagePath, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(body)
	if err := os.WriteFile(fullPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	manifest.PackageFiles = append(manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: id, Kind: "schema", Path: path, Version: "1", Digest: hex.EncodeToString(digest[:]),
	})
}

func seedProductionLifecycleE2EAuthority(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	writer *audit.PostgresWriter,
	actor identity.Actor,
	extension extensions.Extension,
	authority extensions.LifecycleAuthoritySnapshot,
) {
	t.Helper()
	auditID, err := writer.AppendReturningID(ctx, audit.Event{
		ActorUserID: actor.ID, Action: audit.ActionExtensionEnable,
		Metadata: map[string]any{"extensionId": extension.ID, "source": "p4_e2e_authority_seed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := extensions.BuildLifecycleCoordinatorRunInput(
		extension, actor, authority,
		extensions.LifecycleOperationIntent{
			Operation: extensions.LifecycleMachineEnable, IdempotencyKey: "p4-e2e-authority-seed",
			AuditEventID: auditID,
		},
	)
	if err != nil {
		t.Fatalf("build lifecycle authority seed: %v", err)
	}
	repository := extensions.NewPostgresLifecycleRepository(pool)
	acquired, err := repository.AcquireOperation(ctx, input.Acquire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteOperation(ctx, extensions.CompleteLifecycleOperationInput{
		OperationID: acquired.Operation.ID, ExpectedRevision: acquired.Operation.Revision,
		ExpectedState: acquired.Operation.State, State: extensions.LifecycleStateEnabled,
		TerminalResult: extensions.LifecycleTerminalSucceeded,
	}); err != nil {
		t.Fatalf("complete lifecycle authority seed: %v", err)
	}
}

func assertProductionLifecycleE2EResult(
	t *testing.T,
	result extensions.UninstallResult,
	replayed bool,
) {
	t.Helper()
	if !result.Uninstalled || result.OperationID <= 0 || result.RemovalMode != extensions.LifecycleRemovalPreserve ||
		result.Replayed != replayed || result.Cleanup == nil || result.Cleanup.Status != "finalized" ||
		!result.Cleanup.PhysicalPurgeComplete {
		t.Fatalf("production lifecycle result = %#v", result)
	}
}

func assertProductionLifecycleE2EDurablePurge(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture productionLifecycleE2EFixture,
	operationID int64,
) {
	t.Helper()
	if _, err := os.Stat(fixture.extension.PackagePath); !os.IsNotExist(err) {
		t.Fatalf("package was not physically purged: %v", err)
	}
	var extensionCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM extensions WHERE id = $1`, fixture.extension.ID).Scan(&extensionCount); err != nil || extensionCount != 0 {
		t.Fatalf("extension identity count=%d err=%v", extensionCount, err)
	}
	var operationState, terminalResult, removalMode string
	var operationActorID, auditEventID int64
	if err := pool.QueryRow(ctx, `
		SELECT state, terminal_result, removal_mode,
		       COALESCE(requested_by_user_id, 0), COALESCE(audit_event_id, 0)
		FROM extension_lifecycle_operations WHERE id = $1
	`, operationID).Scan(
		&operationState, &terminalResult, &removalMode, &operationActorID, &auditEventID,
	); err != nil {
		t.Fatal(err)
	}
	if operationState != extensions.LifecycleStateUninstalling ||
		terminalResult != extensions.LifecycleTerminalSucceeded ||
		removalMode != extensions.LifecycleRemovalPreserve ||
		operationActorID != fixture.actor.ID || auditEventID <= 0 {
		t.Fatalf(
			"retained operation state=%q terminal=%q mode=%q actor=%d audit=%d",
			operationState, terminalResult, removalMode, operationActorID, auditEventID,
		)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE id = $1 AND actor_user_id = $2 AND action = $3
		  AND metadata->>'extensionId' = $4
		  AND metadata->>'removalMode' = $5
	`, auditEventID, fixture.actor.ID, audit.ActionExtensionUninstalled,
		fixture.extension.ID, extensions.LifecycleRemovalPreserve).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("retained lifecycle audit count=%d", auditCount)
	}
	var cleanupStatus, purgeReceiptID, purgeProofDigest string
	var purgeProof []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, purge_receipt_id, purge_proof, purge_proof_digest
		FROM extension_lifecycle_cleanup_records WHERE operation_id = $1
	`, operationID).Scan(&cleanupStatus, &purgeReceiptID, &purgeProof, &purgeProofDigest); err != nil {
		t.Fatal(err)
	}
	if cleanupStatus != "finalized" || !strings.HasPrefix(purgeReceiptID, "host-purge-") ||
		!productionLifecycleE2EValidDigest(purgeProofDigest) {
		t.Fatalf("cleanup receipt status=%q id=%q proofDigest=%q", cleanupStatus, purgeReceiptID, purgeProofDigest)
	}
	var cleanupProof struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(purgeProof, &cleanupProof); err != nil || cleanupProof.Schema != "sforum.lifecycle.cleanup-purge-proof@1" {
		t.Fatalf("cleanup proof=%s err=%v", purgeProof, err)
	}

	var dispositionStatus, disposition, receiptID, proofDigest string
	var credentialRevoked, schemaRetained, rolesRemoved, resourceExisted bool
	var proof []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, data_disposition, credential_revoked, schema_retained, roles_removed,
		       resource_existed, receipt_id, proof, proof_digest
		FROM extension_database_dispositions WHERE operation_id = $1
	`, operationID).Scan(
		&dispositionStatus, &disposition, &credentialRevoked, &schemaRetained, &rolesRemoved,
		&resourceExisted, &receiptID, &proof, &proofDigest,
	); err != nil {
		t.Fatal(err)
	}
	if dispositionStatus != "applied" || disposition != "preserved" || !credentialRevoked ||
		!schemaRetained || rolesRemoved || !resourceExisted ||
		!strings.HasPrefix(receiptID, "database-disposition-") || !productionLifecycleE2EValidDigest(proofDigest) {
		t.Fatalf("database disposition status=%q outcome=%q receipt=%q proof=%q", dispositionStatus, disposition, receiptID, proofDigest)
	}
	var dispositionProof struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(proof, &dispositionProof); err != nil || dispositionProof.Schema != "sforum.extension-database-disposition-proof@1" {
		t.Fatalf("database proof=%s err=%v", proof, err)
	}
	var schemaPresent, runtimeRolePresent, ownerRolePresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, fixture.identifiers.Schema).Scan(&schemaPresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, fixture.identifiers.RuntimeRole).Scan(&runtimeRolePresent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, fixture.identifiers.OwnerRole).Scan(&ownerRolePresent); err != nil {
		t.Fatal(err)
	}
	if !schemaPresent || runtimeRolePresent || !ownerRolePresent {
		t.Fatalf("preserved database schema=%t runtimeRole=%t ownerRole=%t", schemaPresent, runtimeRolePresent, ownerRolePresent)
	}
}

func cleanupProductionLifecycleE2EFixture(pool *pgxpool.Pool, fixture productionLifecycleE2EFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, fixture.identifiers.RuntimeRole)
	_, _ = pool.Exec(ctx, `DELETE FROM page_provider_bindings WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_frontend_trust_grants WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_trust_challenges WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_trust_grants WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_dispositions WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_credentials WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_grants WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_resources WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_lifecycle_operations WHERE extension_id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, fixture.extension.ID)
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{fixture.identifiers.Schema}.Sanitize()+` CASCADE`)
	for _, role := range []string{fixture.identifiers.RuntimeRole, fixture.identifiers.OwnerRole} {
		quoted := pgx.Identifier{role}.Sanitize()
		_, _ = pool.Exec(ctx, `DROP OWNED BY `+quoted)
		_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS `+quoted)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_user_id = $1`, fixture.actor.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.actor.ID)
}

func productionLifecycleE2EValidDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func productionLifecycleE2EShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
