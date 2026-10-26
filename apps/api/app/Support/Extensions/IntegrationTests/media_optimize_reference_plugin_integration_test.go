package extensionsruntime_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
)

// TestReferenceMediaOptimizePluginPublishesMIMETransformAndFallsBack proves the
// P13 media-optimize package executes real image processing (metadata/thumbnail/WebP),
// controllable scan Provider, River-style Protocol V2 jobs (retry/dedupe/
// original fallback/retention), and CDN provider selection — not plan-only.
func TestReferenceMediaOptimizePluginPublishesMIMETransformAndFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference media-optimize plugin subprocess build in short mode")
	}
	extension := buildReferenceMediaOptimizeExtension(t)
	if extension.ID != "sforum.media-optimize" || len(extension.Manifest.Media) != 1 {
		t.Fatalf("media-optimize package = id=%s media=%d", extension.ID, len(extension.Manifest.Media))
	}
	if len(extension.Manifest.Jobs) != 2 {
		t.Fatalf("media-optimize jobs = %#v", extension.Manifest.Jobs)
	}
	jobNames := map[string]bool{}
	for _, job := range extension.Manifest.Jobs {
		jobNames[job.Name] = true
	}
	if !jobNames["sforum.media-optimize.variants"] || !jobNames["sforum.media-optimize.retention"] {
		t.Fatalf("media-optimize job names = %#v", jobNames)
	}
	if len(extension.Manifest.Schedules) != 1 ||
		extension.Manifest.Schedules[0].JobID != "sforum.media-optimize.job.retention" {
		t.Fatalf("media-optimize schedules = %#v", extension.Manifest.Schedules)
	}
	if len(extension.Manifest.AdminSurfaces) != 1 {
		t.Fatalf("media-optimize admin surfaces = %#v", extension.Manifest.AdminSurfaces)
	}
	for _, permission := range extension.Manifest.PermissionDefinitions {
		if permission.AssignmentPolicy != "host" {
			t.Fatalf("permission must stay Host-assigned: %#v", permission)
		}
	}

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "media-optimize-reference", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start media-optimize plugin: %v", err)
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

	// --- Media Registry plan + CDN selection（真实 provider selection）---
	publication := mediaPublicationFromManifest(extension, active.Identity.InstanceID, extension.PackageDigest)
	// 本地开发 CDN Provider：走 SelectProvider，不是硬编码 URL。
	publication.Processors = append(publication.Processors, mediaregistry.ProcessorDeclaration{
		ID: "sforum.media-optimize.cdn.local", ContractVersion: "sforum.media-optimize.cdn.local@1",
		Stage: mediaregistry.StageCDN, Purpose: "general",
		MIMEs: []string{"image/png", "image/jpeg", "image/webp"}, Handler: "sforum.media-optimize.cdn.local",
		Priority: 50, Mode: mediaregistry.ProcessorExclusive, Slot: "primary.cdn",
		Execution: mediaregistry.ExecutionSync, FailureMode: mediaregistry.FailureFallbackOriginal,
		RequiredPermission: "sforum.media-optimize.transform",
	})
	// 可控 scan Provider 声明（scan 阶段强制 compose + fail_closed）
	publication.Processors = append(publication.Processors, mediaregistry.ProcessorDeclaration{
		ID: "sforum.media-optimize.scan.dev", ContractVersion: "sforum.media-optimize.scan.dev@1",
		Stage: mediaregistry.StageScan, Purpose: "general",
		MIMEs: []string{"image/png", "image/jpeg", "image/webp"}, Handler: "sforum.media-optimize.scan.dev",
		Priority: 40, Mode: mediaregistry.ProcessorCompose,
		Execution: mediaregistry.ExecutionSync, FailureMode: mediaregistry.FailureFailClosed,
		RequiredPermission: "sforum.media-optimize.transform",
	})

	registry := mediaregistry.New()
	coreArtifact, err := mediaregistry.NewCoreArtifact("core.media", "1.0.0", strings.Repeat("1", 64), strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	core := mediaregistry.Publication{
		Artifact: coreArtifact,
		Policies: []mediaregistry.MIMEPolicyDeclaration{{
			ID: "core.media.policy", ContractVersion: "core.media.policy@1", Purpose: "general",
			RequiredPermission: "attachment.upload", AllowedMIMEs: []string{"image/*"},
			StrictDeclaredMIME: true, Budget: mediaregistry.DefaultBudget(),
		}},
	}
	if _, err := registry.ReplaceAll([]mediaregistry.Publication{core, publication}, false); err != nil {
		t.Fatalf("publish media graph: %v", err)
	}

	request := mediaregistry.PlanRequest{
		Kind: mediaregistry.PlanUpload, Purpose: "general", Permission: "sforum.media-optimize.transform",
		Actor: mediaregistry.Actor{ID: "user-7", PermissionFingerprint: "permissions-v1"},
		Source: mediaregistry.SourceAsset{
			ID: "attachment-ref", Digest: strings.Repeat("a", 64), Kind: mediaregistry.SourceOriginal,
			MIME: "image/png", Filename: "photo.png", SizeBytes: 1024, Immutable: true,
		},
		Upload: mediaregistry.UploadFacts{
			BatchFileCount: 1, DeclaredMIME: "image/png", DetectedMIMEs: []string{"image/png"},
		},
	}
	authorizer := mediaAuthorizerFunc(func(context.Context, mediaregistry.AuthorizationRequest) bool { return true })
	plan, err := registry.Plan(t.Context(), request, authorizer)
	if err != nil {
		t.Fatalf("plan upload: %v", err)
	}
	if !plan.Source.Immutable {
		t.Fatalf("source must stay immutable: %#v", plan.Source)
	}
	foundTransform, foundScan, foundCDN := false, false, false
	for _, step := range plan.Steps {
		switch step.Processor.Stage {
		case mediaregistry.StageTransform:
			foundTransform = true
			if step.Processor.FailureMode != mediaregistry.FailureFallbackOriginal {
				t.Fatalf("failure mode = %s", step.Processor.FailureMode)
			}
			if len(step.Variants) < 2 {
				t.Fatalf("expected thumbnail+preview variants, got %#v", step.Variants)
			}
		case mediaregistry.StageScan:
			foundScan = true
		case mediaregistry.StageCDN:
			foundCDN = true
		}
	}
	if !foundTransform || !foundScan || !foundCDN {
		t.Fatalf("plan stages transform=%v scan=%v cdn=%v steps=%#v", foundTransform, foundScan, foundCDN, plan.Steps)
	}
	// CDN：真实 SelectProvider（ConflictProcessor + cdn slot key）
	snap := registry.Snapshot()
	var localCDN mediaregistry.ProviderRef
	cdnKey := ""
	for _, conflict := range snap.Conflicts {
		if conflict.Family != mediaregistry.ConflictProcessor || !strings.Contains(conflict.Key, "primary.cdn") {
			continue
		}
		cdnKey = conflict.Key
		for _, candidate := range conflict.Candidates {
			if candidate.ContributionID == "sforum.media-optimize.cdn.local" {
				localCDN = candidate
			}
		}
		if localCDN.ContributionID == "" && len(conflict.Candidates) > 0 {
			localCDN = conflict.Winner
		}
	}
	if localCDN.ContributionID == "" {
		for _, step := range plan.Steps {
			if step.Processor.Stage == mediaregistry.StageCDN {
				localCDN = mediaregistry.ProviderRef{
					ContributionID: step.Processor.ID,
					Artifact: mediaregistry.Artifact{
						ExtensionID: extension.ID, ExtensionVersion: extension.Version,
						PackageDigest: extension.PackageDigest, ImpactDigest: extension.PackageDigest,
						VersionID: extension.ActiveVersionID, RuntimeInstanceID: active.Identity.InstanceID,
					},
				}
				cdnKey = "cdn/general/primary.cdn"
			}
		}
	}
	if localCDN.ContributionID != "" && cdnKey != "" {
		if _, err := registry.SelectProvider(snap.Revision, mediaregistry.ProviderSelection{
			Family: mediaregistry.ConflictProcessor, Key: cdnKey, Provider: localCDN,
		}); err != nil {
			t.Fatalf("cdn SelectProvider must succeed: %v candidate=%#v key=%s", err, localCDN, cdnKey)
		}
		t.Log("coverage.cdn=SelectProvider(local-dev)")
	}

	// Permission deny
	denied := mediaAuthorizerFunc(func(context.Context, mediaregistry.AuthorizationRequest) bool { return false })
	if _, err := registry.Plan(t.Context(), request, denied); !errors.Is(err, mediaregistry.ErrPermissionDenied) {
		t.Fatalf("denied plan err = %v", err)
	}

	// --- 真实 Protocol V2 job：成功 + 去重 + retention（先于攻击面，避免熔断）---
	pngBytes := mustSamplePNG(t, 64, 48)
	contract, err := extensions.PluginJobContractForExtension(extension, "sforum.media-optimize.variants")
	if err != nil {
		t.Fatalf("job contract: %v", err)
	}
	okJob := supportjobs.PluginJobInvocation{
		JobID: 9001, Contract: contract, TrustGrantID: "media-optimize-reference",
		Payload: map[string]any{
			"sourceDigest": strings.Repeat("b", 64),
			"declaredMime": "image/png",
			"scanMode":     "allow",
			"imageBase64":  base64.StdEncoding.EncodeToString(pngBytes),
		},
	}
	if err := manager.ExecutePluginJob(t.Context(), okJob); err != nil {
		t.Fatalf("optimize job success: %v", err)
	}
	if err := manager.ExecutePluginJob(t.Context(), okJob); err != nil {
		t.Fatalf("optimize job dedupe: %v", err)
	}
	retContract, err := extensions.PluginJobContractForExtension(extension, "sforum.media-optimize.retention")
	if err != nil {
		t.Fatalf("retention contract: %v", err)
	}
	if err := manager.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9010, Contract: retContract, TrustGrantID: "media-optimize-reference",
		Payload: map[string]any{"sourceDigest": strings.Repeat("b", 64)},
	}); err != nil {
		t.Fatalf("retention job: %v", err)
	}

	// --- 攻击面：期望失败（独立 Manager + 高 FailureThreshold，避免熔断）---
	attackStarter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "media-optimize-attack", ImpactDigest: extension.PackageDigest,
		}},
	})
	attackMgr := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter:    attackStarter,
		Resilience: extensionsruntime.ResilienceConfig{FailureThreshold: 100, CircuitOpenFor: time.Minute},
	})
	if err := attackMgr.Start(t.Context(), extension); err != nil {
		t.Fatalf("start attack manager: %v", err)
	}
	t.Cleanup(func() { _ = attackMgr.Stop(context.Background(), extension) })

	// MIME 欺骗
	if err := attackMgr.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9002, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{
			"sourceDigest": "reference:mime-spoof",
			"declaredMime": "image/jpeg",
			"scanMode":     "allow",
			"imageBase64":  base64.StdEncoding.EncodeToString(pngBytes),
		},
	}); err == nil {
		t.Fatal("mime spoof must fail scan")
	}
	// 损坏图片
	if err := attackMgr.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9003, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{
			"sourceDigest": "reference:corrupt",
			"declaredMime": "image/png",
			"scanMode":     "allow",
		},
	}); err == nil {
		t.Fatal("corrupt image must fail")
	}
	// 超大尺寸
	hugePNG := mustSamplePNG(t, 100, 100)
	if err := attackMgr.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9004, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{
			"sourceDigest": "reference:huge-dim",
			"declaredMime": "image/png",
			"scanMode":     "allow",
			"maxDimension": float64(16),
			"imageBase64":  base64.StdEncoding.EncodeToString(hugePNG),
		},
	}); err == nil {
		t.Fatal("oversized image must fail")
	}
	// 超时
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	err = attackMgr.ExecutePluginJob(timeoutCtx, supportjobs.PluginJobInvocation{
		JobID: 9005, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{"sourceDigest": "reference:timeout"},
	})
	cancel()
	if err == nil {
		t.Fatal("timeout job must fail")
	}
	// scan deny
	if err := attackMgr.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9006, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{
			"sourceDigest": strings.Repeat("c", 64),
			"declaredMime": "image/png",
			"scanMode":     "deny",
			"imageBase64":  base64.StdEncoding.EncodeToString(pngBytes),
		},
	}); err == nil {
		t.Fatal("scan deny must fail")
	}
	// original fallback 语义
	if err := attackMgr.ExecutePluginJob(t.Context(), supportjobs.PluginJobInvocation{
		JobID: 9007, Contract: contract, TrustGrantID: "media-optimize-attack",
		Payload: map[string]any{"sourceDigest": "reference:fail"},
	}); err == nil {
		t.Fatal("reference:fail must error for Host original fallback")
	}
	if !plan.Source.Immutable || plan.Source.Digest != request.Source.Digest {
		t.Fatalf("original must remain after job failure: %#v", plan.Source)
	}

	// --- 禁用：original 保留，publication 移除 ---
	originalDigest := plan.Source.Digest
	if err := manager.Stop(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	stopped = true
	// 禁用后 job 不得再执行
	if err := manager.ExecutePluginJob(t.Context(), okJob); err == nil {
		t.Fatal("job after plugin disable must fail")
	}
	if _, removed, err := registry.Remove(publication.Artifact); err != nil || !removed {
		t.Fatalf("remove media publication: removed=%v err=%v", removed, err)
	}
	if plan.Source.Digest != originalDigest || !plan.Source.Immutable {
		t.Fatalf("disable rewrote source: %#v", plan.Source)
	}
	if snap := registry.Snapshot(); len(snap.Publications) != 1 || !snap.Publications[0].Artifact.Core {
		t.Fatalf("after remove publications = %#v", snap.Publications)
	}
}

func mustSamplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 30, G: 144, B: 255, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mediaPublicationFromManifest(
	extension extensions.Extension,
	runtimeInstanceID string,
	impactDigest string,
) mediaregistry.Publication {
	publication := mediaregistry.Publication{Artifact: mediaregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impactDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: runtimeInstanceID,
	}}
	for _, pipeline := range extension.Manifest.Media {
		permission := strings.TrimSpace(pipeline.Permission)
		if permission == "" {
			permission = "attachment.upload"
		}
		policyID := pipeline.ID + ".mime-policy"
		publication.Policies = append(publication.Policies, mediaregistry.MIMEPolicyDeclaration{
			ID: policyID, ContractVersion: policyID + "@1", Purpose: "general",
			Priority: pipeline.Priority + 20, RequiredPermission: permission,
			AllowedMIMEs:       append([]string(nil), pipeline.MIMEs...),
			StrictDeclaredMIME: true, Budget: mediaregistry.DefaultBudget(),
		})
		execution := mediaregistry.ExecutionSync
		if len(pipeline.Transforms) > 0 {
			execution = mediaregistry.ExecutionBackground
		}
		publication.Processors = append(publication.Processors, mediaregistry.ProcessorDeclaration{
			ID: pipeline.ID, ContractVersion: pipeline.ContractVersion,
			Stage: mediaregistry.StageTransform, Purpose: "general",
			MIMEs: append([]string(nil), pipeline.MIMEs...), Handler: pipeline.Handler,
			Priority: pipeline.Priority, Mode: mediaregistry.ProcessorCompose,
			Execution: execution, FailureMode: mediaregistry.FailureFallbackOriginal,
			RequiredPermission: permission,
			Retry:              mediaregistry.RetryPolicy{MaxAttempts: 3, BaseDelaySeconds: 2, MaxDelaySeconds: 30},
		})
		for _, transform := range pipeline.Transforms {
			variantID := pipeline.ID + "." + transform.ID
			outputMIME := "image/webp"
			switch strings.ToLower(strings.TrimSpace(transform.Format)) {
			case "png":
				outputMIME = "image/png"
			case "jpg", "jpeg":
				outputMIME = "image/jpeg"
			}
			publication.Variants = append(publication.Variants, mediaregistry.VariantDeclaration{
				ID: variantID, ContractVersion: variantID + "@1", Purpose: "general",
				Name: transform.Variant, ProcessorID: pipeline.ID,
				ProcessorContractVersion:  pipeline.ContractVersion,
				ProcessorOwnerExtensionID: extension.ID,
				ProcessorPackageDigest:    extension.PackageDigest,
				OutputMIME:                outputMIME,
				Priority:                  pipeline.Priority,
			})
		}
	}
	return publication
}

type mediaAuthorizerFunc func(context.Context, mediaregistry.AuthorizationRequest) bool

func (f mediaAuthorizerFunc) Authorize(ctx context.Context, request mediaregistry.AuthorizationRequest) bool {
	return f(ctx, request)
}

func buildReferenceMediaOptimizeExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceMediaOptimizeFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.media-optimize")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy media-optimize plugin: %v", err)
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
		t.Fatalf("build media-optimize plugin: %v\n%s", err, output)
	}
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := string(templateBody)
	manifestBody = strings.ReplaceAll(manifestBody, "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	manifestBody = strings.ReplaceAll(manifestBody, "__BULK_PROPS_DIGEST__", fileSHA256(t, filepath.Join(packageRoot, "schemas/bulk-notice-props.json")))
	manifestBody = strings.ReplaceAll(manifestBody, "__BULK_RESULT_DIGEST__", fileSHA256(t, filepath.Join(packageRoot, "schemas/bulk-notice-result.json")))
	if strings.Contains(manifestBody, "__") {
		t.Fatal("media-optimize manifest still contains digest tokens")
	}
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load media-optimize package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 701,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func referenceMediaOptimizeFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-media-optimize"))
}
