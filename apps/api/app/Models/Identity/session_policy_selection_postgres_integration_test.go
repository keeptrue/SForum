package identity

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentitySessionPolicyPostgresSelectResetRuntimeRebindAndSafeMode(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	ctx := fixture.ctx

	current, err := fixture.sessionPolicy.Current(ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault ||
		current.Revision != 0 || !current.Implicit {
		t.Fatalf("implicit Core selection = %#v, %v", current, err)
	}
	fixture.assertSessionPolicyCounts(0, 0, 0)
	effective, err := fixture.sessionPolicy.Resolve(ctx)
	if err != nil || effective.PolicyID != IdentitySessionPolicyCoreDefault ||
		effective.Source != IdentitySessionPolicySourceCore || effective.Selection == nil ||
		effective.Selection.Revision != 0 || effective.Provider != nil {
		t.Fatalf("implicit Core resolution = %#v, %v", effective, err)
	}

	candidate, err := fixture.sessionPolicy.Candidate(ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatalf("resolve exact session candidate: %v", err)
	}
	if candidate.PolicyID != fixture.sessionProvider.ID ||
		candidate.ProviderContractVersion != fixture.sessionProvider.ContractVersion ||
		candidate.OwnerExtensionID != fixture.extensionID ||
		candidate.OwnerExtensionVersionID != fixture.versionID ||
		candidate.DeclarationRevision <= 0 {
		t.Fatalf("candidate = %#v", candidate)
	}

	selected, err := fixture.sessionPolicy.Select(ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	})
	if err != nil || !selected.Changed || selected.Event == nil ||
		selected.Selection.Revision != 1 || selected.Selection.Implicit ||
		selected.Selection.IdentitySessionPolicyEvidence != candidate ||
		selected.Event.PreviousSelection != nil || selected.Event.SelectedSelection == nil ||
		*selected.Event.SelectedSelection != candidate ||
		selected.Selection.SelectionAuditEventID != selected.Event.AuditEventID {
		t.Fatalf("select result = %#v, %v", selected, err)
	}
	fixture.assertSessionPolicyCounts(1, 1, 1)
	readback, found, err := fixture.sessionPolicy.readbackIdentitySessionPolicyMutation(ctx, selected)
	if err != nil || !found || !reflect.DeepEqual(readback.Selection, selected.Selection) ||
		readback.Event == nil || readback.Event.ID != selected.Event.ID {
		t.Fatalf("commit marker readback = %#v, found=%v, error=%v", readback, found, err)
	}
	effective, err = fixture.sessionPolicy.Resolve(ctx)
	if err != nil || effective.Source != IdentitySessionPolicySourcePlugin ||
		effective.Selection == nil || effective.Selection.Revision != 1 ||
		effective.Provider == nil || effective.Provider.ID != candidate.PolicyID ||
		effective.RegistryRevision == 0 || effective.RegistryDigest == "" {
		t.Fatalf("selected resolution = %#v, %v", effective, err)
	}
	fixture.assertSessionPolicyAuditMetadata(
		selected.Event.AuditEventID,
		identitySessionPolicyAuditMetadata{
			SelectedSelection: &candidate,
			SelectionRevision: 1,
		},
	)

	noChange, err := fixture.sessionPolicy.Select(ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 1, ActorUserID: fixture.adminUserID,
	})
	if err != nil || noChange.Changed || noChange.Event != nil || noChange.Selection.Revision != 1 {
		t.Fatalf("exact-current select = %#v, %v", noChange, err)
	}
	if _, err := fixture.sessionPolicy.Select(ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicyRevisionConflict) {
		t.Fatalf("stale identical select error = %v", err)
	}
	fixture.assertSessionPolicyCounts(1, 1, 1)

	reset, err := fixture.sessionPolicy.Reset(ctx, ResetIdentitySessionPolicyInput{
		ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "operator_reset",
	})
	if err != nil || !reset.Changed || reset.Event == nil || reset.Selection.Revision != 2 ||
		reset.Selection.PolicyID != IdentitySessionPolicyCoreDefault || reset.Selection.Implicit ||
		reset.Event.SelectedSelection != nil || reset.Event.PreviousSelection == nil ||
		*reset.Event.PreviousSelection != candidate {
		t.Fatalf("reset result = %#v, %v", reset, err)
	}
	fixture.assertSessionPolicyCounts(1, 2, 2)
	fixture.assertSessionPolicyAuditMetadata(
		reset.Event.AuditEventID,
		identitySessionPolicyAuditMetadata{
			PreviousSelection: &candidate,
			SelectionRevision: 2,
			ReasonCode:        "operator_reset",
		},
	)

	noChange, err = fixture.sessionPolicy.Reset(ctx, ResetIdentitySessionPolicyInput{
		ExpectedRevision: 2, ActorUserID: fixture.adminUserID, ReasonCode: "operator_reset",
	})
	if err != nil || noChange.Changed || noChange.Selection.Revision != 2 {
		t.Fatalf("exact-current Core reset = %#v, %v", noChange, err)
	}
	if _, err := fixture.sessionPolicy.Reset(ctx, ResetIdentitySessionPolicyInput{
		ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "operator_reset",
	}); !errors.Is(err, ErrIdentitySessionPolicyRevisionConflict) {
		t.Fatalf("stale Core reset error = %v", err)
	}

	reselected, err := fixture.sessionPolicy.Select(ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 2, ActorUserID: fixture.adminUserID,
	})
	if err != nil || !reselected.Changed || reselected.Event == nil ||
		reselected.Selection.Revision != 3 || reselected.Event.PreviousSelection == nil ||
		reselected.Event.PreviousSelection.PolicyID != IdentitySessionPolicyCoreDefault {
		t.Fatalf("reselect result = %#v, %v", reselected, err)
	}
	fixture.assertSessionPolicyCounts(1, 3, 3)

	restartPublication, err := identityPersistenceProviderPublication(
		fixture.extensionID,
		fixture.versionID,
		candidate.OwnerPackageDigest,
		"identity-runtime-restart",
	)
	if err != nil {
		t.Fatal(err)
	}
	restartRegistry := identityregistry.New()
	if _, err := restartRegistry.Publish(restartPublication); err != nil {
		t.Fatal(err)
	}
	restartStore, err := NewPostgresIdentitySessionPolicyStore(fixture.pool, restartRegistry)
	if err != nil {
		t.Fatal(err)
	}
	restartCandidate, err := restartStore.Candidate(ctx, candidate.PolicyID)
	if err != nil || restartCandidate != candidate {
		t.Fatalf("restart candidate = %#v, %v", restartCandidate, err)
	}
	restarted, err := restartStore.Current(ctx)
	if err != nil || restarted.Revision != 3 || restarted.IdentitySessionPolicyEvidence != candidate {
		t.Fatalf("restart selection = %#v, %v", restarted, err)
	}

	beforeSafeMode := restarted
	if _, err := restartRegistry.ReplaceAll([]identityregistry.Publication{restartPublication}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := restartStore.Candidate(ctx, candidate.PolicyID); !errors.Is(err, ErrIdentitySessionPolicySafeMode) {
		t.Fatalf("Safe Mode candidate error = %v", err)
	}
	if _, err := restartStore.Select(ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 3, ActorUserID: fixture.adminUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicySafeMode) {
		t.Fatalf("Safe Mode select error = %v", err)
	}
	afterSafeMode, err := restartStore.Current(ctx)
	if err != nil || !reflect.DeepEqual(afterSafeMode, beforeSafeMode) {
		t.Fatalf("Safe Mode mutated desired selection before=%#v after=%#v error=%v", beforeSafeMode, afterSafeMode, err)
	}
	effective, err = restartStore.Resolve(ctx)
	if err != nil || effective.PolicyID != IdentitySessionPolicyCoreDefault ||
		effective.Source != IdentitySessionPolicySourceSafeMode ||
		effective.Selection != nil || effective.Provider != nil {
		t.Fatalf("Safe Mode effective resolution = %#v, %v", effective, err)
	}
	fixture.assertSessionPolicyCounts(1, 3, 3)
	if _, err := restartRegistry.ReplaceAll([]identityregistry.Publication{restartPublication}, false); err != nil {
		t.Fatal(err)
	}
	effective, err = restartStore.Resolve(ctx)
	if err != nil || effective.Source != IdentitySessionPolicySourcePlugin ||
		effective.Selection == nil || effective.Selection.Revision != 3 || effective.Provider == nil {
		t.Fatalf("post-Safe-Mode resolution = %#v, %v", effective, err)
	}
	if _, err := restartRegistry.ReplaceAll([]identityregistry.Publication{restartPublication}, true); err != nil {
		t.Fatal(err)
	}
	// Explicit reset is a Host recovery action, not an automatic Safe Mode
	// side effect, and intentionally remains available while plugins are bypassed.
	safeModeReset, err := restartStore.Reset(ctx, ResetIdentitySessionPolicyInput{
		ExpectedRevision: 3, ActorUserID: fixture.adminUserID, ReasonCode: "operator_reset",
	})
	if err != nil || !safeModeReset.Changed || safeModeReset.Selection.Revision != 4 ||
		safeModeReset.Selection.PolicyID != IdentitySessionPolicyCoreDefault {
		t.Fatalf("Safe Mode explicit reset = %#v, %v", safeModeReset, err)
	}
	fixture.assertSessionPolicyCounts(1, 4, 4)
	if _, err := restartRegistry.ReplaceAll([]identityregistry.Publication{restartPublication}, false); err != nil {
		t.Fatal(err)
	}
	effective, err = restartStore.Resolve(ctx)
	if err != nil || effective.Source != IdentitySessionPolicySourceCore ||
		effective.Selection == nil || effective.Selection.Revision != 4 || effective.Provider != nil {
		t.Fatalf("explicit Core after Safe Mode = %#v, %v", effective, err)
	}

	events, err := restartStore.ListEvents(ctx, 100)
	if err != nil || len(events) != 4 ||
		events[0].SelectionRevision != 4 || events[1].SelectionRevision != 3 ||
		events[2].SelectionRevision != 2 || events[3].SelectionRevision != 1 {
		t.Fatalf("events = %#v, %v", events, err)
	}
}

func TestIdentitySessionPolicyPostgresStaleDesiredFailsClosed(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); err != nil {
		t.Fatal(err)
	}
	// This intentionally bypasses the configured lifecycle transaction hook.
	// Retained historical callers still fail closed instead of invoking a stale
	// provider; the production Reconcile path is covered separately below.
	if _, err := identityregistry.NewPostgresStore(fixture.pool).Reconcile(
		fixture.ctx,
		identityregistry.ReconcilePublicationInput{
			ExtensionID: fixture.extensionID, AllowedSource: &fixture.publication.Artifact,
			ActorUserID: fixture.actorUserID, AuditEventID: 99,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sessionPolicy.Resolve(fixture.ctx); !errors.Is(err, ErrIdentitySessionPolicyDeclarationStale) {
		t.Fatalf("stale desired resolution error = %v", err)
	}
	desired, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || desired.IdentitySessionPolicyEvidence != candidate || desired.Revision != 1 {
		t.Fatalf("stale desired selection = %#v, %v", desired, err)
	}
	fixture.assertSessionPolicyCounts(1, 1, 1)
}

func TestIdentitySessionPolicyPostgresLifecycleReplayAndInvalidation(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	})
	if err != nil {
		t.Fatal(err)
	}

	beforeReplay, err := fixture.registryStore.LoadDurableState(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayedDurable, err := fixture.registryStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
		ExtensionID:   fixture.extensionID,
		AllowedSource: &fixture.publication.Artifact,
		AllowedTarget: &fixture.publication.Artifact,
		Desired:       &fixture.publication,
		ActorUserID:   fixture.actorUserID,
		AuditEventID:  1001,
	})
	if err != nil {
		t.Fatalf("exact lifecycle replay: %v", err)
	}
	if !reflect.DeepEqual(replayedDurable, beforeReplay) {
		t.Fatalf("exact lifecycle replay changed durable state\nbefore=%#v\nafter=%#v", beforeReplay, replayedDurable)
	}
	unchanged, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || !reflect.DeepEqual(unchanged, selected.Selection) {
		t.Fatalf("exact replay selection = %#v, want %#v, err=%v", unchanged, selected.Selection, err)
	}
	fixture.assertSessionPolicyCounts(1, 1, 1)

	fixture.installSessionPolicyCoreBeforeRegistryWriteGuard()
	if err := fixture.retireProvider(); err != nil {
		t.Fatalf("lifecycle retirement: %v", err)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault ||
		current.Revision != 2 || current.Implicit {
		t.Fatalf("invalidated selection = %#v, err=%v", current, err)
	}
	events, err := fixture.sessionPolicy.ListEvents(fixture.ctx, 10)
	if err != nil || len(events) != 2 || events[0].Action != IdentitySessionPolicyActionInvalidate ||
		events[0].SelectionRevision != 2 || events[0].PreviousSelection == nil ||
		*events[0].PreviousSelection != candidate || events[0].SelectedSelection != nil ||
		events[0].ReasonCode != identitySessionPolicyLifecycleInvalidationReason {
		t.Fatalf("invalidation events = %#v, err=%v", events, err)
	}
	fixture.assertSessionPolicyCounts(1, 2, 2)
	fixture.assertSessionPolicyAuditMetadata(
		events[0].AuditEventID,
		identitySessionPolicyAuditMetadata{
			PreviousSelection:     &candidate,
			SelectionRevision:     2,
			ReasonCode:            identitySessionPolicyLifecycleInvalidationReason,
			LifecycleAuditEventID: 99,
		},
	)
	assertIdentitySessionPolicyRegistryTipState(t, fixture, identityregistry.RegistryStateTombstone)
	retired, err := fixture.registryStore.LoadDurableState(fixture.ctx)
	if err != nil || identityregistry.ValidateDurableRetirement(retired, fixture.extensionID) != nil {
		t.Fatalf("durable retirement = %#v, err=%v", retired, err)
	}
}

func TestIdentitySessionPolicyPostgresLifecycleAllowsDeletedActorProvenance(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM users WHERE id=$1`, fixture.actorUserID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.retireProvider(); err != nil {
		t.Fatalf("retire with deleted lifecycle actor: %v", err)
	}
	var selectedBy, eventActor, auditActor *int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT selection.selected_by_user_id, event.actor_user_id, audit.actor_user_id
		FROM identity_session_policy_selection AS selection
		JOIN identity_session_policy_selection_events AS event
		  ON event.selection_revision = selection.revision
		JOIN audit_events AS audit ON audit.id = event.audit_event_id
		WHERE selection.singleton = TRUE
	`).Scan(&selectedBy, &eventActor, &auditActor); err != nil {
		t.Fatal(err)
	}
	if selectedBy != nil || eventActor != nil || auditActor != nil {
		t.Fatalf("deleted lifecycle actor provenance selection=%v event=%v audit=%v", selectedBy, eventActor, auditActor)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault || current.Revision != 2 {
		t.Fatalf("deleted actor invalidation = %#v, err=%v", current, err)
	}
}

func TestIdentitySessionPolicyPostgresLifecycleUpgradeAssociationAndRollback(t *testing.T) {
	tests := []struct {
		name              string
		removeAssociation bool
	}{
		{name: "artifact replacement"},
		{name: "session association removal", removeAssociation: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityPersistencePGFixture(t)
			candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
				Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
			}); err != nil {
				t.Fatal(err)
			}

			target := fixture.insertSessionPolicyPublicationVersion("2.0.0", "b", "identity-runtime-v2")
			if test.removeAssociation {
				target.Identity.SessionPolicy = IdentitySessionPolicyCoreDefault
			}
			targetDurable, err := fixture.registryStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
				ExtensionID:   fixture.extensionID,
				AllowedSource: &fixture.publication.Artifact,
				AllowedTarget: &target.Artifact,
				Desired:       &target,
				ActorUserID:   fixture.actorUserID,
				AuditEventID:  1101,
			})
			if err != nil {
				t.Fatalf("reconcile target: %v", err)
			}
			if err := identityregistry.ValidateDurablePublication(targetDurable, target); err != nil {
				t.Fatalf("validate durable target: %v", err)
			}
			current, err := fixture.sessionPolicy.Current(fixture.ctx)
			if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault || current.Revision != 2 {
				t.Fatalf("target invalidation = %#v, err=%v", current, err)
			}
			fixture.assertSessionPolicyCounts(1, 2, 2)

			if test.removeAssociation {
				return
			}
			if _, err := fixture.pool.Exec(fixture.ctx, `
				UPDATE extensions SET active_version_id = $2 WHERE id = $1
			`, fixture.extensionID, target.Artifact.VersionID); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.registry.ReplaceAll([]identityregistry.Publication{target}, false); err != nil {
				t.Fatal(err)
			}
			targetCandidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, target.Identity.SessionPolicy)
			if err != nil {
				t.Fatal(err)
			}
			selectedTarget, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
				Candidate: targetCandidate, ExpectedRevision: 2, ActorUserID: fixture.adminUserID,
			})
			if err != nil {
				t.Fatal(err)
			}
			replayedTarget, err := fixture.registryStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
				ExtensionID:   fixture.extensionID,
				AllowedSource: &target.Artifact,
				AllowedTarget: &target.Artifact,
				Desired:       &target,
				ActorUserID:   fixture.actorUserID,
				AuditEventID:  1102,
			})
			if err != nil {
				t.Fatalf("exact target replay: %v", err)
			}
			if !reflect.DeepEqual(replayedTarget, targetDurable) {
				t.Fatalf("target replay changed durable state\nbefore=%#v\nafter=%#v", targetDurable, replayedTarget)
			}
			replayed, err := fixture.sessionPolicy.Current(fixture.ctx)
			if err != nil || !reflect.DeepEqual(replayed, selectedTarget.Selection) {
				t.Fatalf("target replay = %#v, want %#v, err=%v", replayed, selectedTarget.Selection, err)
			}

			// Lifecycle compensation republishes the source but never implicitly
			// reselects it; the explicit target selection is invalidated first.
			rolledBackDurable, err := fixture.registryStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
				ExtensionID:   fixture.extensionID,
				AllowedSource: &target.Artifact,
				AllowedTarget: &fixture.publication.Artifact,
				Desired:       &fixture.publication,
				ActorUserID:   fixture.actorUserID,
				AuditEventID:  1103,
			})
			if err != nil {
				t.Fatalf("rollback publication: %v", err)
			}
			if err := identityregistry.ValidateDurablePublication(rolledBackDurable, fixture.publication); err != nil {
				t.Fatalf("validate rollback publication: %v", err)
			}
			rolledBack, err := fixture.sessionPolicy.Current(fixture.ctx)
			if err != nil || rolledBack.PolicyID != IdentitySessionPolicyCoreDefault || rolledBack.Revision != 4 {
				t.Fatalf("rollback selection = %#v, err=%v", rolledBack, err)
			}
			fixture.assertSessionPolicyCounts(1, 4, 4)
		})
	}
}

func TestIdentitySessionPolicyPostgresLifecycleFailureRollsBackRegistryAndSelection(t *testing.T) {
	for _, failure := range []string{"audit", "event", "root"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newIdentityPersistencePGFixture(t)
			candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
				Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
			})
			if err != nil {
				t.Fatal(err)
			}
			beforeDurable, err := fixture.registryStore.LoadDurableState(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			fixture.installSessionPolicyLifecycleFailure(failure)
			if err := fixture.retireProvider(); err == nil {
				t.Fatal("forced lifecycle failure unexpectedly succeeded")
			}
			current, err := fixture.sessionPolicy.Current(fixture.ctx)
			if err != nil || !reflect.DeepEqual(current, selected.Selection) {
				t.Fatalf("rolled-back selection = %#v, want %#v, err=%v", current, selected.Selection, err)
			}
			fixture.assertSessionPolicyCounts(1, 1, 1)
			assertIdentitySessionPolicyRegistryTipState(t, fixture, identityregistry.RegistryStateActive)
			afterDurable, err := fixture.registryStore.LoadDurableState(fixture.ctx)
			if err != nil || !reflect.DeepEqual(afterDurable, beforeDurable) {
				t.Fatalf("failure changed durable Registry\nbefore=%#v\nafter=%#v\nerr=%v", beforeDurable, afterDurable, err)
			}
		})
	}
}

func TestIdentitySessionPolicyPostgresRejectsAuthorityAndRollsBack(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.actorUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicyPermissionDenied) {
		t.Fatalf("non-super-admin select error = %v", err)
	}
	forged := candidate
	forged.DeclarationRevision++
	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: forged, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicyDeclarationStale) {
		t.Fatalf("forged exact claim error = %v", err)
	}
	fixture.assertSessionPolicyCounts(0, 0, 0)

	if _, err := fixture.pool.Exec(fixture.ctx, `
		CREATE FUNCTION reject_session_policy_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced session-policy event failure';
		END;
		$$;
		CREATE TRIGGER reject_session_policy_event
		BEFORE INSERT ON identity_session_policy_selection_events
		FOR EACH ROW EXECUTE FUNCTION reject_session_policy_event();
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicyStoreUnavailable) {
		t.Fatalf("forced event rollback error = %v", err)
	}
	fixture.assertSessionPolicyCounts(0, 0, 0)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		DROP TRIGGER reject_session_policy_event ON identity_session_policy_selection_events;
		DROP FUNCTION reject_session_policy_event();
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE users SET status='disabled' WHERE id=$1`, fixture.adminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); !errors.Is(err, ErrIdentitySessionPolicyPermissionDenied) {
		t.Fatalf("inactive super-admin select error = %v", err)
	}
	fixture.assertSessionPolicyCounts(0, 0, 0)
}

func (f *identityPersistencePGFixture) assertSessionPolicyCounts(selections, events, audits int) {
	f.t.Helper()
	var gotSelections, gotEvents, gotAudits int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_session_policy_selection`).Scan(&gotSelections); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_session_policy_selection_events`).Scan(&gotEvents); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events WHERE action LIKE 'identity.session_policy.%'
	`).Scan(&gotAudits); err != nil {
		f.t.Fatal(err)
	}
	if gotSelections != selections || gotEvents != events || gotAudits != audits {
		f.t.Fatalf(
			"session policy counts selections=%d/%d events=%d/%d audits=%d/%d",
			gotSelections,
			selections,
			gotEvents,
			events,
			gotAudits,
			audits,
		)
	}
}

func (f *identityPersistencePGFixture) assertSessionPolicyAuditMetadata(
	auditID int64,
	want identitySessionPolicyAuditMetadata,
) {
	f.t.Helper()
	var action string
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `
		SELECT action, metadata FROM audit_events WHERE id=$1
	`, auditID).Scan(&action, &raw); err != nil {
		f.t.Fatal(err)
	}
	if action != "identity.session_policy."+identitySessionPolicyActionForAudit(want) {
		f.t.Fatalf("audit action = %q", action)
	}
	var got identitySessionPolicyAuditMetadata
	if err := json.Unmarshal(raw, &got); err != nil {
		f.t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		f.t.Fatalf("audit metadata = %#v, want %#v", got, want)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		f.t.Fatal(err)
	}
	gotKeys := make([]string, 0, len(keys))
	for key := range keys {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{"previousSelection", "selectedSelection", "selectionRevision"}
	if want.ReasonCode != "" {
		wantKeys = append(wantKeys, "reasonCode")
		sort.Strings(wantKeys)
	}
	if want.LifecycleAuditEventID > 0 {
		wantKeys = append(wantKeys, "lifecycleAuditEventId")
		sort.Strings(wantKeys)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		f.t.Fatalf("audit metadata keys = %v, want %v", gotKeys, wantKeys)
	}
}

func identitySessionPolicyActionForAudit(metadata identitySessionPolicyAuditMetadata) string {
	if metadata.LifecycleAuditEventID > 0 {
		return IdentitySessionPolicyActionInvalidate
	}
	if metadata.SelectedSelection == nil {
		return IdentitySessionPolicyActionReset
	}
	return IdentitySessionPolicyActionSelect
}

func assertIdentitySessionPolicyRegistryTipState(
	t *testing.T,
	fixture *identityPersistencePGFixture,
	want string,
) {
	t.Helper()
	var rootState, providerState string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT registry_state
		FROM extension_identity_registry_publications
		WHERE owner_extension_id = $1
		ORDER BY revision DESC LIMIT 1
	`, fixture.extensionID).Scan(&rootState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT registry_state
		FROM extension_identity_registry_declarations
		WHERE identity_kind = 'provider' AND stable_id = $1
		ORDER BY revision DESC LIMIT 1
	`, fixture.sessionProvider.ID).Scan(&providerState); err != nil {
		t.Fatal(err)
	}
	if rootState != want || providerState != want {
		t.Fatalf("registry states root=%q provider=%q want=%q", rootState, providerState, want)
	}
}
