package extensionsruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestLifecycleCleanupFenceSupportsRetentionAndEveryUninstallMode(t *testing.T) {
	tests := []struct {
		name           string
		operation      extensions.LifecycleMachineOperation
		position       int
		mode           LifecycleBoundaryCleanupMode
		removalMode    string
		uninstall      bool
		retention      bool
		exportEvidence bool
	}{
		{"disable", extensions.LifecycleMachineDisable, 3, LifecycleBoundaryCleanupDisable, "", false, false, false},
		{"upgrade retired source", extensions.LifecycleMachineUpgrade, 10, LifecycleBoundaryCleanupRetiredSource, "", false, false, false},
		{"rollback retired source", extensions.LifecycleMachineRollback, 6, LifecycleBoundaryCleanupRetiredSource, "", false, false, false},
		{"uninstall preserve", extensions.LifecycleMachineUninstall, 6, LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve, true, true, false},
		{"uninstall export", extensions.LifecycleMachineUninstall, 6, LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove, true, false, true},
		{"uninstall complete", extensions.LifecycleMachineUninstall, 6, LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete, true, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := lifecycleCleanupTestRequest(t, test.operation, test.position)
			request.RemovalMode = test.removalMode
			if test.operation == extensions.LifecycleMachineDisable || test.operation == extensions.LifecycleMachineUninstall {
				// Deactivation is source-owned; there may be no target process.
				request.TargetBinding.RuntimeInstanceID = ""
			}
			if test.exportEvidence {
				request.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(fmt.Sprintf(
					`{"exportArtifactId":"export-41","exportDigest":%q}`,
					strings.Repeat("d", 64),
				))
			}
			fence, err := lifecycleCleanupFenceFor(request, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if fence.OperationID != request.OperationID || fence.StepID != request.StepID ||
				fence.Retained.RuntimeInstanceID == "" || fence.Target.RuntimeInstanceID != request.TargetBinding.RuntimeInstanceID {
				t.Fatalf("fence = %#v", fence)
			}
			if (fence.RecordKind == lifecycleCleanupRecordUninstall) != test.uninstall ||
				(fence.Status == lifecycleCleanupStatusPending) != test.uninstall {
				t.Fatalf("record kind/status = %q/%q", fence.RecordKind, fence.Status)
			}
			if (fence.RetentionMarker != "") != test.retention {
				t.Fatalf("retention marker = %q", fence.RetentionMarker)
			}
			if test.exportEvidence && (fence.ExportArtifactID != "export-41" ||
				fence.ExportDigest != strings.Repeat("d", 64) ||
				fence.ExportEvidenceAction != extensions.LifecycleMachineUninstallAfter ||
				!validLifecycleCleanupDigest(fence.ExportEvidenceDigest)) {
				t.Fatalf("export fence = %#v", fence)
			}
			if !json.Valid(fence.IdentitySnapshot) || !json.Valid(fence.PackageSnapshot) ||
				!json.Valid(fence.RuntimeRecoverySnapshot) || !json.Valid(fence.RuntimeAttempt) {
				t.Fatal("cleanup recovery snapshots are not valid JSON")
			}
		})
	}
}

func TestLifecycleCleanupFenceRejectsIncompleteOrCrossModeRequests(t *testing.T) {
	base := lifecycleCleanupTestRequest(t, extensions.LifecycleMachineUninstall, 6)
	base.RemovalMode = extensions.LifecycleRemovalExportThenRemove
	base.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(fmt.Sprintf(
		`{"exportArtifactId":"export-41","exportDigest":%q}`,
		strings.Repeat("e", 64),
	))
	tests := []struct {
		name   string
		mode   LifecycleBoundaryCleanupMode
		mutate func(*LifecycleBoundaryRequest)
		want   error
	}{
		{"missing operation", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.OperationID = 0 }, ErrLifecycleCleanupInvalid},
		{"wrong position", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.Position = 5 }, ErrLifecycleCleanupInvalid},
		{"wrong stable step", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.StepID += ".other" }, ErrLifecycleCleanupInvalid},
		{"wrong mode", LifecycleBoundaryCleanupComplete, func(*LifecycleBoundaryRequest) {}, ErrLifecycleCleanupInvalid},
		{"missing source", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.SourceExtension = nil }, ErrLifecycleCleanupInvalid},
		{"missing source runtime", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.SourceBinding.RuntimeInstanceID = "" }, ErrLifecycleCleanupInvalid},
		{"missing package path", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) { r.SourceExtension.PackagePath = "" }, ErrLifecycleCleanupInvalid},
		{"foreign source", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) {
			r.SourceExtension.ID = "other.extension"
			r.SourceExtension.Manifest.ID = "other.extension"
			r.SourceBinding.ExtensionID = "other.extension"
		}, ErrLifecycleCleanupInvalid},
		{"missing export evidence", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) {
			delete(r.actionResults, extensions.LifecycleMachineUninstallAfter)
		}, ErrLifecycleCleanupExportEvidenceMissing},
		{"partial export evidence", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) {
			r.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(`{"exportArtifactId":"export-41"}`)
		}, ErrLifecycleCleanupInvalid},
		{"uppercase export digest", LifecycleBoundaryCleanupExport, func(r *LifecycleBoundaryRequest) {
			r.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(fmt.Sprintf(
				`{"exportArtifactId":"export-41","exportDigest":%q}`,
				strings.Repeat("A", 64),
			))
		}, ErrLifecycleCleanupInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneLifecycleBoundaryRequest(base)
			test.mutate(&request)
			if _, err := lifecycleCleanupFenceFor(request, test.mode); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLifecycleCleanupExportEvidenceUsesLatestDurableExecutionResult(t *testing.T) {
	request := lifecycleCleanupTestRequest(t, extensions.LifecycleMachineUninstall, 6)
	request.actionResults[extensions.LifecycleMachineUninstallStep] = json.RawMessage(fmt.Sprintf(
		`{"exportArtifactId":"from-uninstall","exportDigest":%q}`,
		strings.Repeat("a", 64),
	))
	request.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(fmt.Sprintf(
		`{"exportArtifactId":"from-after","exportDigest":%q,"secret":"must-not-persist"}`,
		strings.Repeat("b", 64),
	))
	artifactID, digest, action, document, evidenceDigest, err := lifecycleCleanupExportEvidence(request)
	if err != nil {
		t.Fatal(err)
	}
	if artifactID != "from-after" || digest != strings.Repeat("b", 64) ||
		action != extensions.LifecycleMachineUninstallAfter || !json.Valid(document) ||
		!validLifecycleCleanupDigest(evidenceDigest) {
		t.Fatalf("export evidence = %q %q %q %s %q", artifactID, digest, action, document, evidenceDigest)
	}
	if strings.Contains(string(document), "must-not-persist") {
		t.Fatalf("cleanup evidence persisted an unallowlisted plugin result field: %s", document)
	}

	mutated := append([]byte(nil), document...)
	mutated[0] = '['
	if string(request.actionResults[extensions.LifecycleMachineUninstallAfter]) == string(mutated) {
		t.Fatal("returned evidence aliases request action results")
	}
}

func TestLifecycleCleanupStableIDIsDeterministicAndBounded(t *testing.T) {
	first := lifecycleCleanupStableID(41, strings.Repeat("step", 100), LifecycleBoundaryCleanupPreserve)
	second := lifecycleCleanupStableID(41, strings.Repeat("step", 100), LifecycleBoundaryCleanupPreserve)
	other := lifecycleCleanupStableID(41, strings.Repeat("step", 100), LifecycleBoundaryCleanupComplete)
	if first != second || first == other || !validLifecycleCleanupReference(first) {
		t.Fatalf("cleanup ids = %q %q %q", first, second, other)
	}
}

func TestLifecycleCleanupPurgeReceiptRequiresExactPhysicalCompletion(t *testing.T) {
	request := LifecycleCleanupPurgeRequest{
		CleanupID: "cleanup-41", OperationID: 41, CleanupMode: LifecycleBoundaryCleanupExport,
		RetainedPackageDigest: strings.Repeat("a", 64), ExportArtifactID: "export-41",
		ExportDigest: strings.Repeat("b", 64),
	}
	receipt := LifecycleCleanupPurgeReceipt{
		CleanupID: request.CleanupID, OperationID: request.OperationID, CleanupMode: request.CleanupMode,
		RetainedPackageDigest: request.RetainedPackageDigest, ReceiptID: "purge-41",
		IdentityPurged: true, PackagePurged: true, RuntimeRecoveryPurged: true,
		DataDisposition: "exported_then_removed", Proof: json.RawMessage(`{"storage":"verified","secret":"must-not-persist"}`),
	}
	proof, digest, err := ValidateLifecycleCleanupPurgeReceipt(request, receipt)
	if err != nil || !json.Valid(proof) || !validLifecycleCleanupDigest(digest) {
		t.Fatalf("valid receipt = %s %q %v", proof, digest, err)
	}
	if strings.Contains(string(proof), "must-not-persist") || !strings.Contains(string(proof), "providerProofDigest") {
		t.Fatalf("canonical purge proof leaked provider data or lost its digest: %s", proof)
	}

	tests := []struct {
		name   string
		mutate func(*LifecycleCleanupPurgeReceipt)
	}{
		{"foreign cleanup", func(r *LifecycleCleanupPurgeReceipt) { r.CleanupID = "other" }},
		{"identity not purged", func(r *LifecycleCleanupPurgeReceipt) { r.IdentityPurged = false }},
		{"package not purged", func(r *LifecycleCleanupPurgeReceipt) { r.PackagePurged = false }},
		{"runtime not purged", func(r *LifecycleCleanupPurgeReceipt) { r.RuntimeRecoveryPurged = false }},
		{"wrong disposition", func(r *LifecycleCleanupPurgeReceipt) { r.DataDisposition = "removed" }},
		{"empty proof", func(r *LifecycleCleanupPurgeReceipt) { r.Proof = nil }},
		{"non-object proof", func(r *LifecycleCleanupPurgeReceipt) { r.Proof = json.RawMessage(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := receipt
			test.mutate(&actual)
			if _, _, err := ValidateLifecycleCleanupPurgeReceipt(request, actual); !errors.Is(err, ErrLifecycleCleanupPurgeInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPostgresLifecycleCleanupRejectsMissingDependencies(t *testing.T) {
	request := lifecycleCleanupTestRequest(t, extensions.LifecycleMachineDisable, 3)
	cleanup := NewPostgresLifecycleBoundaryCleanup(nil)
	if _, err := cleanup.StageLifecycleHostCleanup(t.Context(), request, LifecycleBoundaryCleanupDisable); !errors.Is(err, ErrLifecycleCleanupInvalid) {
		t.Fatalf("stage error = %v", err)
	}
	finalizer := NewPostgresLifecycleBoundaryCleanupFinalizer(nil, nil)
	if _, err := finalizer.FinalizeLifecycleHostCleanup(t.Context(), 41); !errors.Is(err, ErrLifecycleCleanupInvalid) {
		t.Fatalf("finalizer error = %v", err)
	}
}

func lifecycleCleanupTestRequest(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	position int,
) LifecycleBoundaryRequest {
	t.Helper()
	gate := lifecycleHostTestRequest(t, operation, position)
	gate.ActionResults = make(map[extensions.LifecycleMachineAction]json.RawMessage)
	for _, action := range lifecycleBoundaryAllowedActions(operation, position) {
		gate.ActionResults[action] = json.RawMessage(fmt.Sprintf(`{"action":%q}`, action))
	}
	request, err := newLifecycleBoundaryRequest(gate)
	if err != nil {
		t.Fatal(err)
	}
	request.TargetExtension.PackagePath = "/retained/packages/target"
	if request.SourceExtension != nil {
		request.SourceExtension.PackagePath = "/retained/packages/source"
		if request.SourceExtension.PackageDigest == request.TargetExtension.PackageDigest {
			request.SourceExtension.PackagePath = request.TargetExtension.PackagePath
		}
	}
	return request
}
