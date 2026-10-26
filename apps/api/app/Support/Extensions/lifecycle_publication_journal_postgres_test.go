package extensionsruntime

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecyclePublicationFenceAcceptsEveryPublicationOperation(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		position  int
		mode      LifecycleBoundaryPublicationMode
	}{
		{extensions.LifecycleMachineInstall, 8, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineEnable, 5, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineDisable, 3, LifecycleBoundaryDeactivate},
		{extensions.LifecycleMachineUpgrade, 8, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineRollback, 6, LifecycleBoundaryActivate},
		{extensions.LifecycleMachineUninstall, 3, LifecycleBoundaryDeactivate},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, test.operation, test.position)
			if test.mode == LifecycleBoundaryDeactivate {
				// Deactivation is fenced by the source runtime; target is an exact
				// artifact snapshot and does not have to own a live process.
				request.TargetBinding.RuntimeInstanceID = ""
			}
			fence, err := lifecyclePublicationFenceFor(request, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if fence.OperationID != request.OperationID || fence.StepID != request.StepID ||
				fence.Attempt != request.Attempt || fence.Target.PackageDigest != request.TargetExtension.PackageDigest {
				t.Fatalf("fence = %#v", fence)
			}
			if test.mode == LifecycleBoundaryDeactivate && !fence.Source.Present {
				t.Fatal("deactivation source artifact was not retained")
			}
		})
	}
}

func TestLifecyclePublicationJournalMapsFrozenRuntimeTransition(t *testing.T) {
	tests := []struct {
		operation extensions.LifecycleMachineOperation
		position  int
		mode      LifecycleBoundaryPublicationMode
		reason    extensions.PluginRuntimePublicationReason
	}{
		{extensions.LifecycleMachineInstall, 8, LifecycleBoundaryActivate, extensions.PluginRuntimePublicationEnable},
		{extensions.LifecycleMachineEnable, 5, LifecycleBoundaryActivate, extensions.PluginRuntimePublicationEnable},
		{extensions.LifecycleMachineUpgrade, 8, LifecycleBoundaryActivate, extensions.PluginRuntimePublicationUpgrade},
		{extensions.LifecycleMachineRollback, 6, LifecycleBoundaryActivate, extensions.PluginRuntimePublicationRollback},
		{extensions.LifecycleMachineDisable, 3, LifecycleBoundaryDeactivate, extensions.PluginRuntimePublicationDisable},
		{extensions.LifecycleMachineUninstall, 3, LifecycleBoundaryDeactivate, extensions.PluginRuntimePublicationUninstall},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			request := lifecyclePublicationTestRequest(t, test.operation, test.position)
			actorUserID := request.ActorUserID
			transition, err := lifecyclePluginRuntimePublicationTransition(request, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if transition.Reason != test.reason ||
				transition.Activate != (test.mode == LifecycleBoundaryActivate) ||
				transition.ActorUserID != actorUserID ||
				transition.Target.ID != request.TargetExtension.ID ||
				transition.Target.ActiveVersionID != request.TargetExtension.ActiveVersionID ||
				transition.Target.PackageDigest != request.TargetExtension.PackageDigest {
				t.Fatalf("transition = %#v", transition)
			}
			if transition.Target.Manifest.Lifecycle == request.TargetExtension.Manifest.Lifecycle {
				t.Fatal("target lifecycle manifest was not frozen")
			}
			if request.SourceExtension == nil {
				if transition.Source != nil {
					t.Fatalf("unexpected source = %#v", transition.Source)
				}
			} else {
				if transition.Source == nil || transition.Source == request.SourceExtension ||
					transition.Source.Manifest.Lifecycle == request.SourceExtension.Manifest.Lifecycle ||
					transition.Source.ID != request.SourceExtension.ID ||
					transition.Source.ActiveVersionID != request.SourceExtension.ActiveVersionID ||
					transition.Source.PackageDigest != request.SourceExtension.PackageDigest {
					t.Fatalf("source = %#v", transition.Source)
				}
				request.SourceExtension.Manifest.Lifecycle.ContractVersion = "mutated.lifecycle@9"
				if transition.Source.Manifest.Lifecycle.ContractVersion == "mutated.lifecycle@9" {
					t.Fatal("source transition changed with caller manifest")
				}
			}
			request.TargetExtension.Manifest.Lifecycle.ContractVersion = "mutated.lifecycle@9"
			request.ActorUserID++
			if transition.Target.Manifest.Lifecycle.ContractVersion == "mutated.lifecycle@9" ||
				transition.ActorUserID != actorUserID {
				t.Fatal("transition changed with caller request")
			}
		})
	}
}

func TestLifecycleMigrationModeForPublication(t *testing.T) {
	tests := []struct {
		name        string
		operation   extensions.LifecycleMachineOperation
		publication LifecycleBoundaryPublicationMode
		wantMode    LifecycleBoundaryMigrationMode
		wantProof   bool
	}{
		{"install activate", extensions.LifecycleMachineInstall, LifecycleBoundaryActivate, LifecycleBoundaryMigrationInstall, true},
		{"upgrade activate", extensions.LifecycleMachineUpgrade, LifecycleBoundaryActivate, LifecycleBoundaryMigrationUpgrade, true},
		{"rollback activate", extensions.LifecycleMachineRollback, LifecycleBoundaryActivate, LifecycleBoundaryMigrationRollback, true},
		{"enable activate", extensions.LifecycleMachineEnable, LifecycleBoundaryActivate, "", false},
		{"disable deactivate", extensions.LifecycleMachineDisable, LifecycleBoundaryDeactivate, "", false},
		{"uninstall deactivate", extensions.LifecycleMachineUninstall, LifecycleBoundaryDeactivate, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, required := lifecycleMigrationModeForPublication(test.operation, test.publication)
			if mode != test.wantMode || required != test.wantProof {
				t.Fatalf("migration mode = %q, %v want %q, %v", mode, required, test.wantMode, test.wantProof)
			}
		})
	}
}

func TestLifecycleMigrationPublicationProofReadyRequiresExactDurableEvidence(t *testing.T) {
	valid := lifecycleMigrationProofRecord{
		FirstAttempt: 1, LastAttempt: 1,
		Status: lifecycleMigrationStatusTargetReady, TargetReady: true,
		ProofKind:   sql.NullString{String: lifecycleMigrationProofP5, Valid: true},
		ProofID:     sql.NullString{String: "p5-proof:exact", Valid: true},
		ProofDigest: sql.NullString{String: strings.Repeat("a", 64), Valid: true},
	}
	if !lifecycleMigrationPublicationProofReady(valid) {
		t.Fatal("exact target-ready proof was rejected")
	}
	tests := map[string]func(*lifecycleMigrationProofRecord){
		"not ready":     func(proof *lifecycleMigrationProofRecord) { proof.TargetReady = false },
		"wrong status":  func(proof *lifecycleMigrationProofRecord) { proof.Status = lifecycleMigrationStatusBlocked },
		"attempt range": func(proof *lifecycleMigrationProofRecord) { proof.FirstAttempt = 2 },
		"unknown kind":  func(proof *lifecycleMigrationProofRecord) { proof.ProofKind.String = "external" },
		"missing kind":  func(proof *lifecycleMigrationProofRecord) { proof.ProofKind.Valid = false },
		"invalid id":    func(proof *lifecycleMigrationProofRecord) { proof.ProofID.String = "bad proof" },
		"invalid digest": func(proof *lifecycleMigrationProofRecord) {
			proof.ProofDigest.String = strings.Repeat("A", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proof := valid
			mutate(&proof)
			if lifecycleMigrationPublicationProofReady(proof) {
				t.Fatalf("invalid proof accepted: %#v", proof)
			}
		})
	}
}

func TestLifecyclePublicationFenceRejectsIncompleteOrCrossModeIdentity(t *testing.T) {
	base := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 8)
	tests := []struct {
		name   string
		mode   LifecycleBoundaryPublicationMode
		mutate func(*LifecycleBoundaryRequest)
	}{
		{"wrong mode", LifecycleBoundaryDeactivate, func(*LifecycleBoundaryRequest) {}},
		{"missing operation", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) { r.OperationID = 0 }},
		{"unstable step", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) { r.StepID = " step " }},
		{"missing attempt", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) { r.Attempt = 0 }},
		{"missing target runtime", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) { r.TargetBinding.RuntimeInstanceID = "" }},
		{"target version id", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) {
			r.TargetExtension.ActiveVersionID = 0
			r.TargetBinding.VersionID = 0
		}},
		{"target digest", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) {
			r.TargetExtension.PackageDigest = strings.Repeat("B", 64)
			r.TargetBinding.PackageDigest = r.TargetExtension.PackageDigest
		}},
		{"missing source", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) {
			r.SourceExtension = nil
			r.SourceBinding = extensions.LifecycleRuntimeBinding{}
		}},
		{"source runtime", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) { r.SourceBinding.RuntimeInstanceID = "" }},
		{"foreign source", LifecycleBoundaryActivate, func(r *LifecycleBoundaryRequest) {
			r.SourceExtension.ID = "other.extension"
			r.SourceExtension.Manifest.ID = "other.extension"
			r.SourceBinding.ExtensionID = "other.extension"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneLifecycleBoundaryRequest(base)
			test.mutate(&request)
			if _, err := lifecyclePublicationFenceFor(request, test.mode); !errors.Is(err, ErrLifecyclePublicationJournalInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLifecyclePublicationCommittedTreatsAbsentMarkerAsUncommitted(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	fence, err := lifecyclePublicationFenceFor(request, LifecycleBoundaryActivate)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := lifecyclePublicationCommittedResult(lifecyclePublicationRecord{}, pgx.ErrNoRows, fence, true)
	if err != nil || committed {
		t.Fatalf("absent marker = %v, %v", committed, err)
	}

	record := lifecyclePublicationRecord{Fence: fence, LastAttempt: fence.Attempt, CommitMarker: true}
	committed, err = lifecyclePublicationCommittedResult(record, nil, fence, true)
	if err != nil || !committed {
		t.Fatalf("committed marker = %v, %v", committed, err)
	}
	record.Fence.Target.RuntimeInstanceID = "other-runtime"
	if _, err := lifecyclePublicationCommittedResult(record, nil, fence, true); !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("conflicting marker error = %v", err)
	}
}

func TestLifecyclePublicationOperationInspectionIgnoresOnlyPerStepAttempt(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineUpgrade, 8)
	fence, err := lifecyclePublicationFenceFor(request, LifecycleBoundaryActivate)
	if err != nil {
		t.Fatal(err)
	}
	record := lifecyclePublicationRecord{Fence: fence, LastAttempt: 1, CommitMarker: true}
	earlyGateFence := fence
	earlyGateFence.Attempt = 9
	committed, err := lifecyclePublicationCommittedResult(record, nil, earlyGateFence, false)
	if err != nil || !committed {
		t.Fatalf("operation marker = %v, %v", committed, err)
	}
	if _, err := lifecyclePublicationCommittedResult(record, nil, earlyGateFence, true); !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("strict attempt error = %v", err)
	}
	earlyGateFence.Target.RuntimeInstanceID = "foreign-target"
	if committed, err := lifecyclePublicationCommittedResult(record, nil, earlyGateFence, false); err != nil || !committed {
		t.Fatalf("restarted runtime marker = %v, %v", committed, err)
	}
	earlyGateFence.Target.VersionID++
	if _, err := lifecyclePublicationCommittedResult(record, nil, earlyGateFence, false); !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("operation artifact fence error = %v", err)
	}
}

func TestPostgresLifecyclePublicationJournalRejectsMissingPool(t *testing.T) {
	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	journal := NewPostgresLifecycleBoundaryPublicationJournal(nil)
	if err := journal.PrepareLifecyclePublication(t.Context(), request, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalInvalid) {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := journal.LifecyclePublicationCommitted(t.Context(), request, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalInvalid) {
		t.Fatalf("inspect error = %v", err)
	}
	if _, err := journal.LifecyclePublicationCommittedForOperation(t.Context(), request, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalInvalid) {
		t.Fatalf("operation inspect error = %v", err)
	}
	if err := journal.CommitLifecyclePublication(t.Context(), request, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalInvalid) {
		t.Fatalf("commit error = %v", err)
	}
}

func lifecyclePublicationTestRequest(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	position int,
) LifecycleBoundaryRequest {
	t.Helper()
	gate := lifecycleHostTestRequest(t, operation, position)
	makeManifestValid := func(extension *extensions.Extension) {
		extension.Name = "Lifecycle Publication Fixture"
		extension.Status = extensions.StatusEnabled
		extension.Manifest.Name = extension.Name
		extension.Manifest.Description = "Exact lifecycle publication fixture."
		extension.Manifest.URL = "https://example.com/lifecycle-publication"
		extension.Manifest.Author = extensions.ManifestAuthor{Name: "SForum"}
		extension.Manifest.SForumVersion = ">=0.1.0"
		extension.Manifest.Backend = extensions.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: extension.PackageDigest, HostAPIVersion: "sforum.host@2",
		}
		extension.Manifest.PackageFiles = []extensions.ManifestPackageFile{{
			ID: extension.ID + ".backend", Kind: "executable",
			Path: extension.Manifest.Backend.Entry, Digest: extension.PackageDigest,
		}}
		if err := extensionmanifest.Validate(extension.Manifest); err != nil {
			t.Fatalf("fixture manifest %s: %v", extension.Version, err)
		}
	}
	makeManifestValid(&gate.TargetExtension)
	gate.Extension = gate.TargetExtension
	if gate.SourceExtension != nil {
		makeManifestValid(gate.SourceExtension)
	}
	gate.ActionResults = make(map[extensions.LifecycleMachineAction]json.RawMessage)
	for _, action := range lifecycleBoundaryAllowedActions(operation, position) {
		gate.ActionResults[action] = json.RawMessage(fmt.Sprintf(`{"action":%q}`, action))
	}
	request, err := newLifecycleBoundaryRequest(gate)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
