package identityregistry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPostgresPublicationStoreReconcileTxRollsBackWithCaller(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	artifact := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	publication := publicationStoreFixture(artifact, 1, nil)
	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state, reconcileErr := fixture.store.ReconcileTx(fixture.ctx, tx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &artifact, Desired: &publication,
		ActorUserID: fixture.actorID, AuditEventID: 8001,
	}); reconcileErr != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(reconcileErr)
	} else if validateErr := ValidateDurablePublication(state, publication); validateErr != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("transactional publication state: %v", validateErr)
	}
	if err := tx.Rollback(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDurablePublication(loaded, publication); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back publication remained durable: %v", err)
	}
}

func TestPostgresPublicationStoreEnableExactReplayAndNoImplicitGrant(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	artifact := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	publication := publicationStoreFixture(artifact, 1, []string{"member", "operator"})
	beforeRolePermissions := fixture.countRolePermissions(t)

	input := ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &artifact,
		Desired: &publication, ActorUserID: fixture.actorID, AuditEventID: 8101,
	}
	first, err := fixture.store.Reconcile(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicationDurableState(t, first, artifact, RegistryStateActive, 1, 3)
	if got := fixture.countRolePermissions(t); got != beforeRolePermissions {
		t.Fatalf("role_permissions changed during publication: got %d want %d", got, beforeRolePermissions)
	}

	var declarationCount, catalogCount, suggestionCount, nonPendingCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_identity_registry_declarations
	`).Scan(&declarationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_permission_catalog
	`).Scan(&catalogCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*), count(*) FILTER (WHERE approval_state <> 'pending')
		FROM extension_permission_role_suggestions
	`).Scan(&suggestionCount, &nonPendingCount); err != nil {
		t.Fatal(err)
	}
	if declarationCount != 3 || catalogCount != 1 || suggestionCount != 2 || nonPendingCount != 0 {
		t.Fatalf("declarations=%d catalog=%d suggestions=%d nonPending=%d",
			declarationCount, catalogCount, suggestionCount, nonPendingCount)
	}

	replayed, err := fixture.store.Reconcile(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("exact replay changed durable state\nfirst=%#v\nreplayed=%#v", first, replayed)
	}
	var replayDeclarationCount, replaySuggestionCount int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_identity_registry_declarations
	`).Scan(&replayDeclarationCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM extension_permission_role_suggestions
	`).Scan(&replaySuggestionCount); err != nil {
		t.Fatal(err)
	}
	if replayDeclarationCount != declarationCount || replaySuggestionCount != suggestionCount ||
		fixture.countRolePermissions(t) != beforeRolePermissions {
		t.Fatalf("replay declarations=%d suggestions=%d rolePermissions=%d",
			replayDeclarationCount, replaySuggestionCount, fixture.countRolePermissions(t))
	}

	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, first) {
		t.Fatalf("restart load differs from committed state\nloaded=%#v\ncommitted=%#v", loaded, first)
	}
	tombstones, err := DurableStateToTombstones(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(tombstones) != 3 {
		t.Fatalf("restored tombstones=%#v", tombstones)
	}
}

func TestPostgresPublicationStoreUpgradeDisableReactivatePreservesTerminalEvidence(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	p1 := publicationStoreFixture(v1, 1, []string{"member"})
	p1.Permissions[0].LabelLocales = map[string]string{"zh-CN": "访问资料"}
	p1.Permissions[0].DescriptionLocales = map[string]string{"zh-CN": "访问身份资料数据。"}
	if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &p1,
		ActorUserID: fixture.actorID, AuditEventID: 8201,
	}); err != nil {
		t.Fatal(err)
	}
	// R8-BE-06：批准者必须自己已持有被建议的权限，否则批准路径按 ErrUnauthorized 拒绝。
	fixture.grantReviewerPermission(t, fixturePermissionKey)

	suggestions, err := fixture.store.ListRoleSuggestions(fixture.ctx, RoleSuggestionFilter{
		PermissionKey: fixturePermissionKey, RoleKey: "member",
	})
	if err != nil || len(suggestions) != 1 {
		t.Fatalf("suggestions=%#v err=%v", suggestions, err)
	}
	approved, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestions[0].ID, ExpectedRevision: suggestions[0].Revision,
		ApprovalState: RoleSuggestionApproved, ActorUserID: fixture.actorID,
	})
	if err != nil || !approved.Applied {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	rolePermissionsAfterApproval := fixture.countRolePermissions(t)
	grantsAfterApproval := fixture.countGrants(t)

	insertPublicationStoreVersion(t, fixture, 102, "2.0.0", "b")
	v2 := publicationStoreArtifact(102, "2.0.0", "b", "runtime-v2")
	p2 := publicationStoreFixture(v2, 2, []string{"member", "operator"})
	p2.Permissions[0].Label = "Profile access v2"
	p2.Permissions[0].LabelLocales = map[string]string{"zh-CN": "访问资料 V2"}
	p2.Permissions[0].DescriptionLocales = map[string]string{"zh-CN": "访问身份资料数据 V2。"}
	upgraded, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v2,
		Desired: &p2, ActorUserID: fixture.actorID, AuditEventID: 8202,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPublicationDurableState(t, upgraded, v2, RegistryStateActive, 2, 3)
	assertPublicationAuthorityCounts(t, fixture, rolePermissionsAfterApproval, grantsAfterApproval)

	var catalogVersionID, catalogRevision int64
	var catalogContract, permissionLabel, permissionLabelZH, permissionDescriptionZH string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT catalog.extension_version_id, catalog.declaration_revision,
		       catalog.contract_version, permission.label,
		       permission.label_locales ->> 'zh-CN',
		       permission.description_locales ->> 'zh-CN'
		FROM extension_permission_catalog AS catalog
		JOIN permissions AS permission ON permission.key = catalog.permission_key
		WHERE catalog.permission_key = $1
	`, fixturePermissionKey).Scan(
		&catalogVersionID, &catalogRevision, &catalogContract,
		&permissionLabel, &permissionLabelZH, &permissionDescriptionZH,
	); err != nil {
		t.Fatal(err)
	}
	if catalogVersionID != 101 || catalogRevision != 1 || catalogContract != fixturePermissionKey+"@1" {
		t.Fatalf("immutable catalog moved: version=%d revision=%d contract=%q",
			catalogVersionID, catalogRevision, catalogContract)
	}
	if permissionLabel != "Profile access v2" || permissionLabelZH != "访问资料 V2" || permissionDescriptionZH != "访问身份资料数据 V2。" {
		t.Fatalf("localized permission presentation was not upgraded: label=%q zh=%q description=%q",
			permissionLabel, permissionLabelZH, permissionDescriptionZH)
	}

	allSuggestions, err := fixture.store.ListRoleSuggestions(fixture.ctx, RoleSuggestionFilter{
		PermissionKey: fixturePermissionKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var approvedCount, pendingCount int
	for _, suggestion := range allSuggestions {
		switch suggestion.ApprovalState {
		case RoleSuggestionApproved:
			approvedCount++
			if suggestion.ID != approved.ID || !suggestion.Applied {
				t.Fatalf("terminal evidence changed: %#v", suggestion)
			}
		case RoleSuggestionPending:
			pendingCount++
		default:
			t.Fatalf("unexpected suggestion state: %#v", suggestion)
		}
	}
	if approvedCount != 1 || pendingCount != 2 {
		t.Fatalf("approved=%d pending=%d suggestions=%#v", approvedCount, pendingCount, allSuggestions)
	}

	disabled, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedSource: &v2,
		ActorUserID: fixture.actorID, AuditEventID: 8203,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPublicationDurableState(t, disabled, v2, RegistryStateTombstone, 3, 3)
	assertPublicationAuthorityCounts(t, fixture, rolePermissionsAfterApproval, grantsAfterApproval)

	reactivated, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &v2, Desired: &p2,
		ActorUserID: fixture.actorID, AuditEventID: 8204,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPublicationDurableState(t, reactivated, v2, RegistryStateActive, 4, 3)
	assertPublicationAuthorityCounts(t, fixture, rolePermissionsAfterApproval, grantsAfterApproval)

	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, reactivated) {
		t.Fatalf("restart load differs after reactivation\nloaded=%#v\nwant=%#v", loaded, reactivated)
	}
}

func TestPostgresPublicationStoreRejectsExactDriftForeignOwnerAndStaleArtifact(t *testing.T) {
	t.Run("exact declaration drift", func(t *testing.T) {
		fixture := newIdentityRegistryStoreFixture(t)
		v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
		publication := publicationStoreFixture(v1, 1, nil)
		if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
			ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &publication,
			ActorUserID: fixture.actorID, AuditEventID: 8301,
		}); err != nil {
			t.Fatal(err)
		}
		drift := publication
		drift.Permissions = append([]PermissionDefinition(nil), publication.Permissions...)
		drift.Permissions[0].Label = "Changed without artifact change"
		_, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
			ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v1,
			Desired: &drift, ActorUserID: fixture.actorID, AuditEventID: 8302,
		})
		if !errors.Is(err, ErrArtifactConflict) {
			t.Fatalf("drift error=%v", err)
		}
	})

	t.Run("foreign permanent owner", func(t *testing.T) {
		fixture := newIdentityRegistryStoreFixture(t)
		if _, err := fixture.pool.Exec(fixture.ctx, `
			INSERT INTO extensions (id, type, name, status)
			VALUES ('fixture', 'plugin', 'Foreign parent', 'enabled')
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(fixture.ctx, `
			INSERT INTO extension_identity_registry_owners (
				identity_kind, stable_id, owner_extension_id
			) VALUES ('permission', $1, 'fixture')
		`, fixturePermissionKey); err != nil {
			t.Fatal(err)
		}
		v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
		publication := publicationStoreFixture(v1, 1, nil)
		_, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
			ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &publication,
			ActorUserID: fixture.actorID, AuditEventID: 8311,
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("foreign owner error=%v", err)
		}
	})

	t.Run("stale source cannot replace winner", func(t *testing.T) {
		fixture := newIdentityRegistryStoreFixture(t)
		v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
		publication := publicationStoreFixture(v1, 1, nil)
		if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
			ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &publication,
			ActorUserID: fixture.actorID, AuditEventID: 8321,
		}); err != nil {
			t.Fatal(err)
		}
		insertPublicationStoreVersion(t, fixture, 102, "2.0.0", "b")
		insertPublicationStoreVersion(t, fixture, 103, "3.0.0", "c")
		v2 := publicationStoreArtifact(102, "2.0.0", "b", "runtime-v2")
		v3 := publicationStoreArtifact(103, "3.0.0", "c", "runtime-v3")
		p3 := publicationStoreFixture(v3, 3, nil)
		_, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
			ExtensionID: fixtureExtensionID, AllowedSource: &v2, AllowedTarget: &v3,
			Desired: &p3, ActorUserID: fixture.actorID, AuditEventID: 8322,
		})
		if !errors.Is(err, ErrArtifactConflict) {
			t.Fatalf("stale source error=%v", err)
		}
		loaded, loadErr := fixture.store.LoadDurableState(fixture.ctx)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		assertPublicationDurableState(t, loaded, v1, RegistryStateActive, 1, 3)
	})
}

func TestPostgresPublicationStoreConcurrentUpgradeHasOneCASWinner(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	p1 := publicationStoreFixture(v1, 1, []string{"member"})
	if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &p1,
		ActorUserID: fixture.actorID, AuditEventID: 8401,
	}); err != nil {
		t.Fatal(err)
	}
	insertPublicationStoreVersion(t, fixture, 102, "2.0.0", "b")
	insertPublicationStoreVersion(t, fixture, 103, "3.0.0", "c")
	v2 := publicationStoreArtifact(102, "2.0.0", "b", "runtime-v2")
	v3 := publicationStoreArtifact(103, "3.0.0", "c", "runtime-v3")
	p2 := publicationStoreFixture(v2, 2, []string{"member"})
	p3 := publicationStoreFixture(v3, 3, []string{"member"})

	inputs := []ReconcilePublicationInput{
		{ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v2,
			Desired: &p2, ActorUserID: fixture.actorID, AuditEventID: 8402},
		{ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v3,
			Desired: &p3, ActorUserID: fixture.actorID, AuditEventID: 8403},
	}
	start := make(chan struct{})
	errorsByWriter := make([]error, len(inputs))
	var wait sync.WaitGroup
	for index := range inputs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByWriter[index] = fixture.store.Reconcile(fixture.ctx, inputs[index])
		}(index)
	}
	close(start)
	wait.Wait()

	var winners, conflicts int
	for _, err := range errorsByWriter {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrArtifactConflict), errors.Is(err, ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected writer error=%v all=%v", err, errorsByWriter)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d errors=%v", winners, conflicts, errorsByWriter)
	}

	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tips) != 3 {
		t.Fatalf("tips=%#v", loaded.Tips)
	}
	winnerVersionID := loaded.Tips[0].ExtensionVersionID
	if winnerVersionID != v2.VersionID && winnerVersionID != v3.VersionID {
		t.Fatalf("winner version=%d tips=%#v", winnerVersionID, loaded.Tips)
	}
	for _, tip := range loaded.Tips {
		if tip.RegistryState != RegistryStateActive || tip.Revision != 2 ||
			tip.ExtensionVersionID != winnerVersionID {
			t.Fatalf("non-atomic winner tips=%#v", loaded.Tips)
		}
	}
}

func TestPostgresPublicationStoreRootOnlyLifecycleAndExactDrift(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	p1 := publicationStoreRootOnlyFixture(v1, 1)
	input := ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &p1,
		ActorUserID: fixture.actorID, AuditEventID: 8501,
	}
	first, err := fixture.store.Reconcile(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertRootOnlyDurableState(t, first, v1, RegistryStateActive, 1)
	if err := ValidateDurablePublication(first, p1); err != nil {
		t.Fatalf("validate root-only publication: %v", err)
	}
	if len(first.RootTips[0].PublicationJSON) == 0 ||
		strings.Contains(string(first.RootTips[0].PublicationJSON), "runtime-v1") {
		t.Fatalf("durable root leaked runtime identity: %s", first.RootTips[0].PublicationJSON)
	}

	replayed, err := fixture.store.Reconcile(fixture.ctx, input)
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("root-only replay changed state: err=%v\nfirst=%#v\nreplayed=%#v", err, first, replayed)
	}
	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("root-only restart load: err=%v\nloaded=%#v\nwant=%#v", err, loaded, first)
	}

	drift := p1
	drift.Identity = &IdentityDeclaration{
		ContractVersion: p1.Identity.ContractVersion,
		SessionPolicy:   p1.Identity.SessionPolicy,
		RiskHooks:       []string{"fixture.identity.risk.changed"},
	}
	if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v1,
		Desired: &drift, ActorUserID: fixture.actorID, AuditEventID: 8502,
	}); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("root-only exact drift error=%v", err)
	}

	insertPublicationStoreVersion(t, fixture, 102, "2.0.0", "b")
	v2 := publicationStoreArtifact(102, "2.0.0", "b", "runtime-v2")
	p2 := publicationStoreRootOnlyFixture(v2, 2)
	upgraded, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v2,
		Desired: &p2, ActorUserID: fixture.actorID, AuditEventID: 8503,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRootOnlyDurableState(t, upgraded, v2, RegistryStateActive, 2)
	if err := ValidateDurablePublication(upgraded, p2); err != nil {
		t.Fatalf("validate upgraded root-only publication: %v", err)
	}

	disabled, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedSource: &v2,
		ActorUserID: fixture.actorID, AuditEventID: 8504,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRootOnlyDurableState(t, disabled, v2, RegistryStateTombstone, 3)
	if err := ValidateDurableRetirement(disabled, fixtureExtensionID); err != nil {
		t.Fatalf("validate root-only retirement: %v", err)
	}
}

func TestPostgresPublicationStoreRootOnlyConcurrentUpgradeHasOneCASWinner(t *testing.T) {
	fixture := newIdentityRegistryStoreFixture(t)
	v1 := publicationStoreArtifact(101, "1.0.0", "a", "runtime-v1")
	p1 := publicationStoreRootOnlyFixture(v1, 1)
	if _, err := fixture.store.Reconcile(fixture.ctx, ReconcilePublicationInput{
		ExtensionID: fixtureExtensionID, AllowedTarget: &v1, Desired: &p1,
		ActorUserID: fixture.actorID, AuditEventID: 8601,
	}); err != nil {
		t.Fatal(err)
	}
	insertPublicationStoreVersion(t, fixture, 102, "2.0.0", "b")
	insertPublicationStoreVersion(t, fixture, 103, "3.0.0", "c")
	v2 := publicationStoreArtifact(102, "2.0.0", "b", "runtime-v2")
	v3 := publicationStoreArtifact(103, "3.0.0", "c", "runtime-v3")
	p2 := publicationStoreRootOnlyFixture(v2, 2)
	p3 := publicationStoreRootOnlyFixture(v3, 3)
	inputs := []ReconcilePublicationInput{
		{ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v2,
			Desired: &p2, ActorUserID: fixture.actorID, AuditEventID: 8602},
		{ExtensionID: fixtureExtensionID, AllowedSource: &v1, AllowedTarget: &v3,
			Desired: &p3, ActorUserID: fixture.actorID, AuditEventID: 8603},
	}
	start := make(chan struct{})
	errorsByWriter := make([]error, len(inputs))
	var wait sync.WaitGroup
	for index := range inputs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByWriter[index] = fixture.store.Reconcile(fixture.ctx, inputs[index])
		}(index)
	}
	close(start)
	wait.Wait()

	var winners, conflicts int
	for _, err := range errorsByWriter {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrArtifactConflict), errors.Is(err, ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected root-only writer error=%v all=%v", err, errorsByWriter)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("root-only winners=%d conflicts=%d errors=%v", winners, conflicts, errorsByWriter)
	}
	loaded, err := fixture.store.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.RootTips) != 1 || loaded.RootTips[0].Revision != 2 ||
		loaded.RootTips[0].RegistryState != RegistryStateActive || len(loaded.Tips) != 0 || len(loaded.Owners) != 0 {
		t.Fatalf("root-only atomic winner state=%#v", loaded)
	}
	winnerID := loaded.RootTips[0].ExtensionVersionID
	if winnerID == v2.VersionID {
		if err := ValidateDurablePublication(loaded, p2); err != nil {
			t.Fatalf("validate v2 root-only winner: %v", err)
		}
	} else if winnerID == v3.VersionID {
		if err := ValidateDurablePublication(loaded, p3); err != nil {
			t.Fatalf("validate v3 root-only winner: %v", err)
		}
	} else {
		t.Fatalf("unexpected root-only winner version=%d", winnerID)
	}
}

const (
	fixtureExtensionID   = "fixture.identity"
	fixturePermissionKey = "fixture.identity.profile"
)

func publicationStoreArtifact(versionID int64, version, digestByte, runtime string) Artifact {
	return Artifact{
		ExtensionID: fixtureExtensionID, ExtensionVersion: version,
		PackageDigest: strings.Repeat(digestByte, 64), VersionID: versionID,
		RuntimeInstanceID: runtime,
	}
}

func publicationStoreFixture(artifact Artifact, contract int, roles []string) Publication {
	permissionContract := fmt.Sprintf("%s@%d", fixturePermissionKey, contract)
	fieldContract := fmt.Sprintf("fixture.identity.nickname@%d", contract)
	providerContract := fmt.Sprintf("fixture.identity.login@%d", contract)
	return Publication{
		Artifact: artifact,
		Permissions: []PermissionDefinition{{
			Key: fixturePermissionKey, ContractVersion: permissionContract,
			Label: "Profile access", Description: "Access identity profile data",
			RecommendedRoles: append([]string(nil), roles...), AssignmentPolicy: "host",
		}},
		Identity: &IdentityDeclaration{
			ContractVersion: fmt.Sprintf("fixture.identity.contract@%d", contract),
			UserFields: []UserField{{
				ID: "fixture.identity.nickname", ContractVersion: fieldContract,
				Type: "string", Schema: fmt.Sprintf("fixture.identity.nickname-schema@%d", contract),
				ReadPermission: fixturePermissionKey, WritePermission: fixturePermissionKey,
			}},
			Providers: []Provider{{
				ID: "fixture.identity.login", ContractVersion: providerContract,
				Kind: ProviderKindAuth, Handler: "IdentityLogin", Priority: contract,
			}},
		},
	}
}

func publicationStoreRootOnlyFixture(artifact Artifact, contract int) Publication {
	return Publication{
		Artifact: artifact,
		Identity: &IdentityDeclaration{
			ContractVersion: fmt.Sprintf("fixture.identity.contract@%d", contract),
			SessionPolicy:   "core.session.default",
			RiskHooks:       []string{"fixture.identity.risk.login"},
		},
	}
}

func insertPublicationStoreVersion(
	t *testing.T,
	fixture *identityRegistryStoreFixture,
	versionID int64,
	version, digestByte string,
) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO extension_versions (
			id, extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, $2, $3, '{}'::jsonb, $4, $5)
	`, versionID, fixtureExtensionID, version, "/tmp/identity-v"+version,
		strings.Repeat(digestByte, 64)); err != nil {
		t.Fatal(err)
	}
}

func assertPublicationDurableState(
	t *testing.T,
	state DurableState,
	artifact Artifact,
	wantState string,
	wantRevision int64,
	wantCount int,
) {
	t.Helper()
	if len(state.Owners) != wantCount || len(state.Tips) != wantCount {
		t.Fatalf("owners=%#v tips=%#v", state.Owners, state.Tips)
	}
	if len(state.RootTips) != 1 {
		t.Fatalf("root tips=%#v", state.RootTips)
	}
	assertRootTip(t, state.RootTips[0], artifact, wantState, wantRevision)
	for _, tip := range state.Tips {
		if tip.OwnerExtensionID != artifact.ExtensionID ||
			tip.ExtensionVersionID != artifact.VersionID ||
			tip.ExtensionVersion != artifact.ExtensionVersion ||
			tip.PackageDigest != artifact.PackageDigest ||
			tip.RegistryState != wantState || tip.Revision != wantRevision {
			t.Fatalf("unexpected tip=%#v want artifact=%#v state=%s revision=%d",
				tip, artifact, wantState, wantRevision)
		}
	}
}

func assertRootOnlyDurableState(
	t *testing.T,
	state DurableState,
	artifact Artifact,
	wantState string,
	wantRevision int64,
) {
	t.Helper()
	if len(state.Owners) != 0 || len(state.Tips) != 0 || len(state.RootTips) != 1 {
		t.Fatalf("root-only durable state=%#v", state)
	}
	assertRootTip(t, state.RootTips[0], artifact, wantState, wantRevision)
}

func assertRootTip(
	t *testing.T,
	tip DurableRootPublicationTip,
	artifact Artifact,
	wantState string,
	wantRevision int64,
) {
	t.Helper()
	if tip.OwnerExtensionID != artifact.ExtensionID ||
		tip.ExtensionVersionID != artifact.VersionID ||
		tip.ExtensionVersion != artifact.ExtensionVersion ||
		tip.PackageDigest != artifact.PackageDigest ||
		tip.SchemaVersion != SchemaVersion || tip.PublicationDigest == "" || len(tip.PublicationJSON) == 0 ||
		tip.RegistryState != wantState || tip.Revision != wantRevision {
		t.Fatalf("unexpected root tip=%#v want artifact=%#v state=%s revision=%d",
			tip, artifact, wantState, wantRevision)
	}
}

func assertPublicationAuthorityCounts(
	t *testing.T,
	fixture *identityRegistryStoreFixture,
	wantRolePermissions int,
	wantGrants int,
) {
	t.Helper()
	if got := fixture.countRolePermissions(t); got != wantRolePermissions {
		t.Fatalf("role_permissions changed during lifecycle publication: got %d want %d",
			got, wantRolePermissions)
	}
	if got := fixture.countGrants(t); got != wantGrants {
		t.Fatalf("grant evidence changed during lifecycle publication: got %d want %d", got, wantGrants)
	}
}
