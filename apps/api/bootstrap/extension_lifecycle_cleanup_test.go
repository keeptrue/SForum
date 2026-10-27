package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type lifecycleCleanupRuntimeInspector struct {
	snapshot extensionsruntime.RuntimeInstanceSnapshot
	err      error
}

func (i lifecycleCleanupRuntimeInspector) InspectRuntimeInstance(
	extensionsruntime.RuntimeInstanceIdentity,
) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	return i.snapshot, i.err
}

func productionLifecycleCleanupTestRequest(t *testing.T) extensionsruntime.LifecycleCleanupPurgeRequest {
	t.Helper()
	return extensionsruntime.LifecycleCleanupPurgeRequest{
		CleanupID: "lifecycle-cleanup-test", OperationID: 71,
		CleanupMode:         extensionsruntime.LifecycleBoundaryCleanupPreserve,
		RetainedExtensionID: "demo.cleanup", RetainedExtensionVersion: "1.2.3",
		RetainedPackageDigest: strings.Repeat("a", 64), RetainedVersionID: 72,
		RetainedPackagePath: t.TempDir(), RetentionMarker: "retained-data-test",
	}
}

func productionLifecycleDatabaseReceipt(
	t *testing.T,
	request extensionsruntime.LifecycleCleanupPurgeRequest,
	resourceExisted bool,
) extensionsruntime.ExtensionDatabaseDispositionReceipt {
	t.Helper()
	proof := json.RawMessage(`{"schema":"database-proof","receiptId":"database-receipt"}`)
	digest := sha256.Sum256(proof)
	receipt := extensionsruntime.ExtensionDatabaseDispositionReceipt{
		CleanupID: request.CleanupID, OperationID: request.OperationID,
		CleanupMode: request.CleanupMode,
		Artifact: extensionsruntime.ExtensionDatabaseArtifact{
			ExtensionID: request.RetainedExtensionID, Version: request.RetainedExtensionVersion,
			VersionID: request.RetainedVersionID, PackageDigest: request.RetainedPackageDigest,
		},
		ExportArtifactID: request.ExportArtifactID, ExportDigest: request.ExportDigest,
		CredentialRevoked: true, ResourceExisted: resourceExisted,
		ReceiptID: "database-receipt", Proof: proof, ProofDigest: hex.EncodeToString(digest[:]),
	}
	switch request.CleanupMode {
	case extensionsruntime.LifecycleBoundaryCleanupPreserve:
		receipt.DataDisposition = "preserved"
		receipt.SchemaRetained = resourceExisted
	case extensionsruntime.LifecycleBoundaryCleanupExport:
		receipt.DataDisposition = "exported_then_removed"
		receipt.RolesRemoved = resourceExisted
		receipt.ExportEvidenceDigest = strings.Repeat("b", 64)
	case extensionsruntime.LifecycleBoundaryCleanupComplete:
		receipt.DataDisposition = "removed"
		receipt.RolesRemoved = resourceExisted
	}
	return receipt
}

func TestProductionLifecycleCleanupValidatesEveryDatabaseDispositionReceipt(t *testing.T) {
	tests := []struct {
		name     string
		mode     extensionsruntime.LifecycleBoundaryCleanupMode
		resource bool
	}{
		{"preserve existing", extensionsruntime.LifecycleBoundaryCleanupPreserve, true},
		{"preserve absent", extensionsruntime.LifecycleBoundaryCleanupPreserve, false},
		{"export existing", extensionsruntime.LifecycleBoundaryCleanupExport, true},
		{"export absent", extensionsruntime.LifecycleBoundaryCleanupExport, false},
		{"complete existing", extensionsruntime.LifecycleBoundaryCleanupComplete, true},
		{"complete absent", extensionsruntime.LifecycleBoundaryCleanupComplete, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := productionLifecycleCleanupTestRequest(t)
			request.CleanupMode, request.RetentionMarker = test.mode, ""
			if test.mode == extensionsruntime.LifecycleBoundaryCleanupPreserve {
				request.RetentionMarker = "retained-data-test"
			}
			if test.mode == extensionsruntime.LifecycleBoundaryCleanupExport {
				request.ExportArtifactID = "export-test"
				request.ExportDigest = strings.Repeat("c", 64)
			}
			receipt := productionLifecycleDatabaseReceipt(t, request, test.resource)
			if err := validateProductionLifecycleDatabaseReceipt(request, receipt); err != nil {
				t.Fatalf("valid receipt rejected: %v", err)
			}
		})
	}
}

func TestProductionLifecycleCleanupRejectsDatabaseReceiptDrift(t *testing.T) {
	request := productionLifecycleCleanupTestRequest(t)
	base := productionLifecycleDatabaseReceipt(t, request, true)
	tests := []struct {
		name   string
		tamper func(*extensionsruntime.ExtensionDatabaseDispositionReceipt)
	}{
		{"cleanup", func(r *extensionsruntime.ExtensionDatabaseDispositionReceipt) { r.CleanupID = "other" }},
		{"artifact", func(r *extensionsruntime.ExtensionDatabaseDispositionReceipt) { r.Artifact.VersionID++ }},
		{"credential", func(r *extensionsruntime.ExtensionDatabaseDispositionReceipt) { r.CredentialRevoked = false }},
		{"schema", func(r *extensionsruntime.ExtensionDatabaseDispositionReceipt) { r.SchemaRetained = false }},
		{"proof", func(r *extensionsruntime.ExtensionDatabaseDispositionReceipt) {
			r.Proof = json.RawMessage(`{"drift":true}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt := base
			receipt.Proof = append(json.RawMessage(nil), base.Proof...)
			test.tamper(&receipt)
			if !errors.Is(validateProductionLifecycleDatabaseReceipt(request, receipt), errProductionLifecycleCleanupConflict) {
				t.Fatal("drifted database receipt was accepted")
			}
		})
	}
}

func TestProductionLifecycleCleanupReceiptIsDeterministicAndLinksDatabaseProof(t *testing.T) {
	request := productionLifecycleCleanupTestRequest(t)
	database := productionLifecycleDatabaseReceipt(t, request, true)
	first, err := productionLifecycleCleanupReceipt(request, database, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := productionLifecycleCleanupReceipt(request, database, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !first.IdentityPurged || !first.PackagePurged ||
		!first.RuntimeRecoveryPurged || first.DataDisposition != "preserved" {
		t.Fatalf("unexpected host receipt: %#v", first)
	}
	var proof struct {
		Schema              string          `json:"schema"`
		DatabaseReceiptID   string          `json:"databaseReceiptId"`
		DatabaseProofDigest string          `json:"databaseProofDigest"`
		DatabaseProof       json.RawMessage `json:"databaseProof"`
	}
	if err := json.Unmarshal(first.Proof, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.Schema != productionLifecycleCleanupProofSchema ||
		proof.DatabaseReceiptID != database.ReceiptID ||
		proof.DatabaseProofDigest != database.ProofDigest ||
		!bytesEqualJSON(proof.DatabaseProof, database.Proof) {
		t.Fatalf("host proof did not bind database proof: %#v", proof)
	}
}

func TestProductionLifecycleCleanupRequiresRuntimeToBeGone(t *testing.T) {
	purger := &productionLifecycleCleanupPurger{
		runtime: lifecycleCleanupRuntimeInspector{err: extensionsruntime.ErrRuntimeInstanceNotFound},
	}
	if err := purger.requireRuntimePurged("demo.cleanup", "runtime-old"); err != nil {
		t.Fatalf("not-found runtime rejected: %v", err)
	}
	purger.runtime = lifecycleCleanupRuntimeInspector{snapshot: extensionsruntime.RuntimeInstanceSnapshot{}}
	if !errors.Is(purger.requireRuntimePurged("demo.cleanup", "runtime-old"), errProductionLifecycleCleanupConflict) {
		t.Fatal("present runtime was accepted")
	}
}

func TestProductionLifecycleCleanupConstructorAndRequestFailClosed(t *testing.T) {
	if _, err := newProductionLifecycleCleanupPurger(nil, nil, "", nil); !errors.Is(err, errProductionLifecycleDependency) {
		t.Fatalf("missing dependency error = %v", err)
	}
	purger, err := newProductionLifecycleCleanupPurger(
		&pgxpool.Pool{},
		lifecycleCleanupRuntimeInspector{err: extensionsruntime.ErrRuntimeInstanceNotFound},
		t.TempDir(),
		lifecycleDatabaseDisposition{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := productionLifecycleCleanupTestRequest(t)
	if err := purger.validateRequest(context.Background(), request); err != nil {
		t.Fatalf("valid cleanup request rejected: %v", err)
	}
	request.RetainedPackageDigest = strings.Repeat("A", 64)
	if !errors.Is(purger.validateRequest(context.Background(), request), errProductionLifecycleCleanupConflict) {
		t.Fatal("uppercase digest was accepted")
	}
}

func bytesEqualJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
