package extensionsruntime_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
)

// TestReferenceMembershipPluginJoinedGates exercises the real Protocol V2
// subprocess path for Identity auth/profile/recovery/session/risk consumers.
// Combined with Host-owned role mapping, automation Host Commands, production
// bootstrap consumer wiring, and the auth/profile HTTP surfaces, this gate
// closes the four remaining P7 identity rows.
func TestReferenceMembershipPluginJoinedGates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference membership plugin subprocess build in short mode")
	}
	extension := buildReferenceMembershipExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "membership-reference", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start reference membership plugin: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = manager.Stop(context.Background(), extension)
		}
	})
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := extensionsruntime.BuildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	})
	if err != nil || publication == nil {
		t.Fatalf("lifecycle identity publication: %#v %v", publication, err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatalf("publish identity: %v", err)
	}

	runtime, err := extensionsruntime.NewIdentityProviderRuntime(manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := extensionsruntime.NewIdentitySessionEvaluateInvoker(runtime)
	if err != nil {
		t.Fatal(err)
	}

	// --- Auth: login.start + login.complete ---
	authFlow, err := identity.NewAuthProviderFlow(
		identity.RegistryAuthProviderSource{Registry: registry},
		invoker,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	start, err := authFlow.Start(t.Context(), identity.AuthProviderStartInput{
		ProviderID:    "sforum.membership-reference.auth",
		Operation:     identity.AuthOperationLoginStart,
		CorrelationID: "login-ok",
	})
	if err != nil || start.Status != identity.AuthStartStatusContinue || start.ContinueToken == "" {
		t.Fatalf("login.start = %#v err=%v", start, err)
	}
	complete, err := authFlow.Complete(t.Context(), identity.AuthProviderCompleteInput{
		ProviderID:      "sforum.membership-reference.auth",
		Operation:       identity.AuthOperationLoginComplete,
		CorrelationID:   "login-ok",
		CompletionToken: "subject:member-1",
	})
	if err != nil || len(complete.SubjectDigest) != 64 {
		t.Fatalf("login.complete = %#v err=%v", complete, err)
	}
	// 失败无 Core/其他提供方回退。
	if _, err := authFlow.Start(t.Context(), identity.AuthProviderStartInput{
		ProviderID:    "sforum.membership-reference.auth",
		Operation:     identity.AuthOperationLoginStart,
		CorrelationID: "login-fail",
	}); err == nil {
		t.Fatal("login.start failure must fail closed")
	}

	// --- Profile: list + update + account ---
	profileComposer, err := identity.NewProfileProviderComposer(
		identity.RegistryProfileProviderSource{Registry: registry},
		invoker,
	)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := profileComposer.ListSections(t.Context(), 7, 7)
	if err != nil || len(sections) != 1 || sections[0].SectionID != "membership" {
		t.Fatalf("sections.list = %#v err=%v", sections, err)
	}
	updated, err := profileComposer.UpdateSection(
		t.Context(), "sforum.membership-reference.profile", "membership", 7, 7,
		map[string]any{"tierLabel": "plus"},
	)
	if err != nil || updated.Fields["tierLabel"] != "plus" {
		t.Fatalf("section.update = %#v err=%v", updated, err)
	}
	account, err := profileComposer.ReadAccount(t.Context(), "sforum.membership-reference.profile", 7, 7)
	if err != nil || account.Fields["tier"] != "standard" {
		t.Fatalf("account.read = %#v err=%v", account, err)
	}

	// --- Recovery ---
	recoveryFlow, err := identity.NewRecoveryProviderFlow(
		identity.RegistryRecoveryProviderSource{Registry: registry},
		invoker,
	)
	if err != nil {
		t.Fatal(err)
	}
	recoveryStart, err := recoveryFlow.Start(t.Context(), identity.RecoveryProviderStartInput{
		ProviderID: "sforum.membership-reference.recovery", CorrelationID: "recovery-ok",
	})
	if err != nil || recoveryStart.ContinueToken == "" {
		t.Fatalf("recovery.start = %#v err=%v", recoveryStart, err)
	}
	recoveryComplete, err := recoveryFlow.Complete(t.Context(), identity.RecoveryProviderCompleteInput{
		ProviderID:    "sforum.membership-reference.recovery",
		CorrelationID: "recovery-ok", CompletionToken: "code-1",
	})
	if err != nil || recoveryComplete.SubjectDigest == "" || recoveryComplete.UserHintID != 42 {
		t.Fatalf("recovery.complete = %#v err=%v", recoveryComplete, err)
	}

	// --- Risk composition ---
	riskEval, err := identity.NewRiskEvaluator(
		identity.RegistryRiskProviderSource{Registry: registry},
		invoker,
	)
	if err != nil {
		t.Fatal(err)
	}
	allow, err := riskEval.Evaluate(t.Context(), identity.RiskEvaluationInput{
		UserID: 7, Purpose: "login", CorrelationID: "risk-ok",
	})
	if err != nil || allow.Disposition != identity.RiskDispositionAllow {
		t.Fatalf("risk allow = %#v err=%v", allow, err)
	}
	deny, err := riskEval.Evaluate(t.Context(), identity.RiskEvaluationInput{
		UserID: 7, Purpose: "login", CorrelationID: "risk-deny",
		DeviceFingerprint: "risk-deny-device",
	})
	if err != nil || deny.Disposition != identity.RiskDispositionDeny {
		t.Fatalf("risk deny = %#v err=%v", deny, err)
	}

	// --- Session evaluate via exact provider invoker (selection store not required) ---
	sessionProvider, err := registry.ResolveProvider("sforum.membership-reference.session")
	if err != nil {
		t.Fatal(err)
	}
	var sessionDisposition string
	if err := invoker.InvokeExact(
		t.Context(), sessionProvider, "session.evaluate", 7,
		map[string]any{"purpose": "issue", "userId": int64(7)},
		func(_ context.Context, output map[string]any, fence func() error) error {
			raw, _ := output["disposition"].(string)
			sessionDisposition = raw
			if fence != nil {
				return fence()
			}
			return nil
		},
	); err != nil || sessionDisposition != "allow" {
		t.Fatalf("session.evaluate disposition=%q err=%v", sessionDisposition, err)
	}
	if err := invoker.InvokeExact(
		t.Context(), sessionProvider, "session.evaluate", 7,
		map[string]any{"purpose": "issue", "userId": int64(7), "deviceFingerprint": "deny-device"},
		func(_ context.Context, output map[string]any, fence func() error) error {
			raw, _ := output["disposition"].(string)
			sessionDisposition = raw
			if fence != nil {
				return fence()
			}
			return nil
		},
	); err != nil || sessionDisposition != "deny" {
		t.Fatalf("session.evaluate deny disposition=%q err=%v", sessionDisposition, err)
	}

	// --- Permission catalog present, no implicit grant ---
	permissions := registry.Snapshot().Permissions
	foundPermission := false
	for _, permission := range permissions {
		if permission.Key == "sforum.membership-reference.manage" {
			foundPermission = true
			if len(permission.RecommendedRoles) == 0 {
				t.Fatal("permission recommendation missing recommended roles")
			}
			// 推荐角色不是授权；Registry 本身从不写入 users_roles。
			break
		}
	}
	if !foundPermission {
		t.Fatal("membership permission definition missing from registry")
	}

	// --- Capability grants declared on extension ---
	granted := make([]string, 0, len(extension.CapabilityGrants))
	for _, grant := range extension.CapabilityGrants {
		granted = append(granted, grant.Key)
	}
	capSet := capabilities.NewSet(granted)
	for _, key := range []string{
		capabilities.HostAPI, capabilities.ExtensionsRead,
		capabilities.ExtensionsCall, capabilities.ExtensionsManage,
		capabilities.AuditAppend,
	} {
		if !capSet.Has(key) {
			t.Fatalf("missing capability grant %s", key)
		}
	}
	if len(extension.Manifest.AdminSurfaces) != 1 ||
		extension.Manifest.AdminSurfaces[0].Kind != "notice" {
		t.Fatalf("membership admin surfaces = %#v", extension.Manifest.AdminSurfaces)
	}
	// Manifest capability catalog must disclose audit.append for operator trust UI.
	hasAuditCapability := false
	for _, key := range extension.Manifest.Capabilities {
		if key == capabilities.AuditAppend {
			hasAuditCapability = true
			break
		}
	}
	if !hasAuditCapability {
		t.Fatalf("membership capabilities missing audit.append: %#v", extension.Manifest.Capabilities)
	}

	// --- Privacy export/erase hooks (Host Privacy registry; no implicit grant) ---
	privacyReg := privacy.New()
	if err := privacyReg.Register(privacy.Contribution{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
		Inventory: []privacy.InventoryItem{{
			ID: "sforum.membership-reference.profile", Kind: privacy.KindPersonalData,
			Description: "Membership reference profile tier and section data",
		}},
		SupportsExport: true, SupportsErase: true,
	}, func(ctx context.Context, userID string) (privacy.ExportArtifact, error) {
		return privacy.ExportArtifact{
			ExtensionID: extension.ID, MediaType: "application/json",
			Body: []byte(`{"userId":"` + userID + `","tier":"reference"}`),
		}, nil
	}, func(ctx context.Context, userID string) (privacy.EraseResult, error) {
		return privacy.EraseResult{ExtensionID: extension.ID, Erased: true}, nil
	}); err != nil {
		t.Fatalf("privacy register: %v", err)
	}
	bundle, err := privacyReg.ExportUser(t.Context(), "super_admin", "member-1")
	if err != nil || len(bundle.Artifacts) == 0 {
		t.Fatalf("privacy export: %#v err=%v", bundle, err)
	}
	erase, err := privacyReg.EraseUser(t.Context(), "super_admin", "member-1")
	if err != nil || len(erase.Results) == 0 || !erase.Results[0].Erased {
		t.Fatalf("privacy erase: %#v err=%v", erase, err)
	}

	// --- Safe Mode: third-party identity execution denied ---
	if _, err := registry.ReplaceAll(nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := authFlow.Start(t.Context(), identity.AuthProviderStartInput{
		ProviderID:    "sforum.membership-reference.auth",
		Operation:     identity.AuthOperationLoginStart,
		CorrelationID: "safe-mode",
	}); !errors.Is(err, identity.ErrAuthProviderNotFound) &&
		!errors.Is(err, identity.ErrAuthProviderFlowUnavailable) {
		t.Fatalf("safe mode auth = %v", err)
	}

	// --- Disable runtime: subsequent calls fail closed ---
	if _, err := registry.ReplaceAll([]identityregistry.Publication{*publication}, false); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if err := manager.Stop(context.Background(), extension); err != nil {
		t.Fatalf("stop membership plugin: %v", err)
	}
	if _, err := authFlow.Start(t.Context(), identity.AuthProviderStartInput{
		ProviderID:    "sforum.membership-reference.auth",
		Operation:     identity.AuthOperationLoginStart,
		CorrelationID: "after-stop",
	}); err == nil {
		t.Fatal("stopped runtime must fail closed")
	}
}

func buildReferenceMembershipExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceMembershipFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.membership-reference")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy reference membership plugin: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(fixtureRoot, "../../../.."))
	goModPath := filepath.Join(packageRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repositoryRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(packageRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reference membership plugin: %v\n%s", err, output)
	}
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := string(templateBody)
	manifestBody = strings.ReplaceAll(manifestBody, "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	schemaNames := []string{
		"start.input", "start.output", "complete.input", "auth.complete.output",
		"recovery.complete.output", "profile.list.input", "profile.list.output",
		"profile.section.input", "profile.section.output", "profile.account.input",
		"profile.account.output", "session.evaluate.input", "session.evaluate.output",
		"risk.evaluate.input", "risk.evaluate.output", "user-field.tier",
		"admin-notice-props", "admin-notice-result",
	}
	for _, name := range schemaNames {
		path := filepath.Join(packageRoot, "schemas", name+".json")
		manifestBody = strings.ReplaceAll(manifestBody, "__DIGEST_"+name+"__", fileSHA256(t, path))
	}
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact membership reference package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 801,
		CapabilityGrants: []extensions.CapabilityGrant{
			{Key: capabilities.HostAPI, Risk: capabilities.RiskLow},
			{Key: capabilities.ExtensionsRead, Risk: capabilities.RiskMedium},
			{Key: capabilities.ExtensionsCall, Risk: capabilities.RiskHigh},
			{Key: capabilities.ExtensionsManage, Risk: capabilities.RiskHigh},
			{Key: capabilities.AuditAppend, Risk: capabilities.RiskMedium},
		},
	}
}

func referenceMembershipFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-membership-reference"))
}
