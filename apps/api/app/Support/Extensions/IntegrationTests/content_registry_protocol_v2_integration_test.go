package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginv2sdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func TestContentRegistryProtocolV2ExactArtifactFailuresAndSanitization(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		plain      string
		assertHTML func(*testing.T, string)
	}{
		{name: "real subprocess", plain: "normal", assertHTML: func(t *testing.T, html string) {
			if !strings.Contains(html, "<p>host</p>") || !strings.Contains(html, "<strong>protocol-v2</strong>") {
				t.Fatalf("Protocol V2 output=%q", html)
			}
		}},
		{name: "unsafe HTML", plain: "xss", assertHTML: func(t *testing.T, html string) {
			if strings.Contains(html, "<script") || strings.Contains(html, "onerror") || !strings.Contains(html, "safe") {
				t.Fatalf("unsanitized output=%q", html)
			}
		}},
		{name: "invalid schema", plain: "invalid", assertHTML: assertHostIdentityHTML},
		{name: "oversized output", plain: "oversized", assertHTML: assertHostIdentityHTML},
		{name: "timeout", plain: "timeout", assertHTML: assertHostIdentityHTML},
		{name: "crash", plain: "crash", assertHTML: assertHostIdentityHTML},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newContentProtocolFixture(t)
			if !fixture.filter.HasFilterContributions() {
				t.Fatal("fixture publication has no active filter")
			}
			html, plain, err := fixture.filter.AfterHostRender(
				t.Context(), "<p>host</p>", scenario.plain, "topic", "42", "public",
			)
			if err != nil {
				t.Fatal(err)
			}
			if plain != scenario.plain {
				t.Fatalf("Host plain text drifted=%q", plain)
			}
			traces := fixture.filter.ContentTraces(32)
			if len(traces) == 0 {
				t.Fatalf("missing bounded execution trace; html=%q active=%#v publication=%#v", html, mustActiveRuntime(t, fixture), fixture.publication)
			}
			scenario.assertHTML(t, html)
			encoded, err := json.Marshal(traces)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), scenario.plain) || strings.Contains(string(encoded), "<p>host</p>") {
				t.Fatalf("trace leaked source=%s", encoded)
			}
		})
	}
}

func mustActiveRuntime(t *testing.T, fixture *contentProtocolFixture) extensionsruntime.RuntimeInstanceSnapshot {
	t.Helper()
	active, err := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func TestContentRegistryProtocolV2DisableSafeModeAndRestartRecovery(t *testing.T) {
	fixture := newContentProtocolFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	plain := "block:" + ready + ":" + release
	type result struct {
		html string
		err  error
	}
	completed := make(chan result, 1)
	go func() {
		html, _, err := fixture.filter.AfterHostRender(
			context.Background(), "<p>host</p>", plain, "comment", "9", "public",
		)
		completed <- result{html: html, err: err}
	}()
	awaitProtocolV2Marker(t, ready, 3*time.Second)
	active, err := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := fixture.registry.Remove(fixture.publication.Artifact); err != nil || !removed {
		t.Fatalf("disable publication removed=%t err=%v", removed, err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-completed:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		assertHostIdentityHTML(t, outcome.html)
	case <-time.After(3 * time.Second):
		t.Fatal("disable-during-request did not release")
	}

	core := mustForumContentCorePublication(t)
	snapshot := fixture.registry.Snapshot()
	if _, err := fixture.registry.ReplaceAllIfRevision(snapshot.Revision, []contentregistry.Publication{core}, true); err != nil {
		t.Fatal(err)
	}
	html, _, err := fixture.filter.AfterHostRender(t.Context(), "<p>safe-mode</p>", "safe", "topic", "42", "public")
	if err != nil {
		t.Fatal(err)
	}
	if html != "<p>safe-mode</p>" {
		t.Fatalf("Safe Mode identity=%q", html)
	}

	// API restart/recovery replays the sealed Core plus the exact active
	// publication. A new filter instance must not retain stale executor state.
	recoveredRegistry := contentregistry.New()
	if _, err := recoveredRegistry.ReplaceAll([]contentregistry.Publication{core, fixture.publication}, false); err != nil {
		t.Fatal(err)
	}
	recoveredFilter, err := contentregistry.NewProductionForumPostFilter(contentregistry.ForumPostFilterConfig{
		Registry: recoveredRegistry, Admission: hostapi.NewContentRegistryAdmission(fixture.runtime),
		Providers: fixture.runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The old exact process is draining, so replay cannot produce false success.
	html, _, err = recoveredFilter.AfterHostRender(t.Context(), "<p>restart</p>", "normal", "topic", "42", "public")
	if err != nil {
		t.Fatal(err)
	}
	if html != "<p>restart</p>" {
		t.Fatalf("stale restart runtime executed=%q", html)
	}

	newStarter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "content-test-restart", ImpactDigest: fixture.extension.PackageDigest,
		}},
	})
	newManager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: newStarter})
	if err := newManager.Start(t.Context(), fixture.extension); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newManager.Stop(context.Background(), fixture.extension) })
	newActive, err := newManager.ActiveRuntimeInstance(fixture.extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredPublication := fixture.publication
	recoveredPublication.Artifact.RuntimeInstanceID = newActive.Identity.InstanceID
	if _, err := recoveredRegistry.PublishIfArtifact(fixture.publication.Artifact, recoveredPublication); err != nil {
		t.Fatal(err)
	}
	newRuntime, err := extensionsruntime.NewContentRegistryProtocolRuntime(newManager)
	if err != nil {
		t.Fatal(err)
	}
	newFilter, err := contentregistry.NewProductionForumPostFilter(contentregistry.ForumPostFilterConfig{
		Registry: recoveredRegistry, Admission: hostapi.NewContentRegistryAdmission(newRuntime), Providers: newRuntime,
	})
	if err != nil {
		t.Fatal(err)
	}
	html, _, err = newFilter.AfterHostRender(t.Context(), "<p>restart</p>", "normal", "topic", "42", "public")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "protocol-v2") {
		t.Fatalf("restarted exact runtime did not recover=%q", html)
	}
}

func TestContentRegistryProtocolV2UpgradeAndRollbackUseOnlyExactArtifact(t *testing.T) {
	fixture := newContentProtocolFixture(t)
	upgraded := contentProtocolTestExtensionVersion(t, "1.1.0", 'd', 32)
	if err := fixture.manager.Start(t.Context(), upgraded); err != nil {
		t.Fatalf("upgrade runtime: %v", err)
	}
	upgradedActive, err := fixture.manager.ActiveRuntimeInstance(upgraded.ID)
	if err != nil {
		t.Fatal(err)
	}
	upgradedPublication := fixture.publication
	upgradedPublication.Artifact = contentregistry.Artifact{
		ExtensionID: upgraded.ID, ExtensionVersion: upgraded.Version,
		PackageDigest: upgraded.PackageDigest, VersionID: upgraded.ActiveVersionID,
		RuntimeInstanceID: upgradedActive.Identity.InstanceID,
	}
	if _, err := fixture.registry.PublishIfArtifact(fixture.publication.Artifact, upgradedPublication); err != nil {
		t.Fatalf("publish upgrade: %v", err)
	}
	html, _, err := fixture.filter.AfterHostRender(t.Context(), "<p>upgrade</p>", "normal", "topic", "42", "public")
	if err != nil || !strings.Contains(html, "protocol-v2") {
		t.Fatalf("upgrade exact dispatch=%q err=%v", html, err)
	}

	if err := fixture.manager.Start(t.Context(), fixture.extension); err != nil {
		t.Fatalf("rollback runtime: %v", err)
	}
	rollbackActive, err := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackPublication := fixture.publication
	rollbackPublication.Artifact.RuntimeInstanceID = rollbackActive.Identity.InstanceID
	if _, err := fixture.registry.PublishIfArtifact(upgradedPublication.Artifact, rollbackPublication); err != nil {
		t.Fatalf("publish rollback: %v", err)
	}
	html, _, err = fixture.filter.AfterHostRender(t.Context(), "<p>rollback</p>", "normal", "topic", "42", "public")
	if err != nil || !strings.Contains(html, "protocol-v2") {
		t.Fatalf("rollback exact dispatch=%q err=%v", html, err)
	}
}

func assertHostIdentityHTML(t *testing.T, html string) {
	t.Helper()
	if html != "<p>host</p>" {
		t.Fatalf("deterministic Host fallback=%q", html)
	}
}

type contentProtocolFixture struct {
	extension   extensions.Extension
	manager     *extensionsruntime.Manager
	registry    *contentregistry.Registry
	publication contentregistry.Publication
	runtime     *extensionsruntime.ContentRegistryProtocolRuntime
	filter      *contentregistry.ForumPostFilter
}

func newContentProtocolFixture(t *testing.T) *contentProtocolFixture {
	t.Helper()
	extension := contentProtocolTestExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "content-test", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start content Protocol V2 fixture: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication := contentregistry.Publication{Artifact: contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	}, Content: []contentregistry.Declaration{{
		ID: "runtime.content.filter", ContractVersion: "runtime.content.filter@1",
		Kind: contentregistry.KindRenderFilter, Handler: "runtime.content.filter",
		Schema: "runtime.content.filter.schema@1",
	}}}
	registry := contentregistry.New()
	if _, err := registry.ReplaceAll([]contentregistry.Publication{mustForumContentCorePublication(t), publication}, false); err != nil {
		t.Fatal(err)
	}
	runtime, err := extensionsruntime.NewContentRegistryProtocolRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := contentregistry.NewProductionForumPostFilter(contentregistry.ForumPostFilterConfig{
		Registry: registry, Admission: hostapi.NewContentRegistryAdmission(runtime), Providers: runtime,
		Limits: contentregistry.ExecutionLimits{CallTimeout: 500 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &contentProtocolFixture{
		extension: extension, manager: manager, registry: registry,
		publication: publication, runtime: runtime, filter: filter,
	}
}

func mustForumContentCorePublication(t *testing.T) contentregistry.Publication {
	t.Helper()
	publication, err := contentregistry.ForumPostBodyCorePublication()
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func contentProtocolTestExtension(t *testing.T) extensions.Extension {
	return contentProtocolTestExtensionVersion(t, "1.0.0", 'c', 31)
}

func contentProtocolTestExtensionVersion(t *testing.T, version string, digestByte byte, versionID int64) extensions.Extension {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "runtime.content", version)
	backendRoot := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=content-v2 exec " + shellQuote(os.Args[0]) +
		" -test.run=TestContentProtocolV2HelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(backendRoot, "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: "runtime.content", Name: "Content Runtime", Version: version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat(string(digestByte), 64), PackagePath: packageRoot, ActiveVersionID: versionID,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "runtime.content", Version: version, Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				HostAPIVersion: "sforum.host@2",
			},
			Content: []extensions.ManifestContent{{
				ID: "runtime.content.filter", ContractVersion: "runtime.content.filter@1",
				Kind: contentregistry.KindRenderFilter, Handler: "runtime.content.filter",
				Schema: "runtime.content.filter.schema@1",
			}},
		},
	}
}

func TestContentProtocolV2HelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != "content-v2" {
		return
	}
	registry, err := pluginv2sdk.NewContentRegistry(pluginv2sdk.ContentDefinition{
		ID: "runtime.content.filter", ContractVersion: "runtime.content.filter@1",
		Kind: contentregistry.KindRenderFilter, Handler: "runtime.content.filter",
		Schema: "runtime.content.filter.schema@1", Execute: contentProtocolFilter,
	})
	if err != nil {
		panic(err)
	}
	pluginv2sdk.Serve(pluginv2sdk.NewServer().
		WithFeatures(pluginv2sdk.ContentRuntimeProtocolFeature()).
		WithContentRegistry(registry))
	os.Exit(0)
}

func contentProtocolFilter(ctx context.Context, call *pluginv2sdk.ContentCall) (pluginv2sdk.ContentResult, error) {
	var source struct {
		HTML  string `json:"html"`
		Plain string `json:"plain"`
	}
	if err := json.Unmarshal(call.Document.Value, &source); err != nil {
		return pluginv2sdk.ContentResult{}, err
	}
	result := pluginv2sdk.ContentRenderSegments{
		SchemaVersion: pluginv2sdk.ContentRenderSegmentsSchema,
		ContentID:     call.Target.ID, ContractVersion: call.Target.ContractVersion,
	}
	switch {
	case source.Plain == "timeout":
		<-ctx.Done()
		return pluginv2sdk.ContentResult{}, ctx.Err()
	case source.Plain == "crash":
		os.Exit(29)
	case source.Plain == "invalid":
		result.ContentID = "runtime.content.invalid"
		result.Segments = []pluginv2sdk.ContentRenderSegment{{Kind: pluginv2sdk.ContentSegmentHTML, HTML: "<p>invalid</p>"}}
	case source.Plain == "oversized":
		result.Segments = []pluginv2sdk.ContentRenderSegment{{Kind: pluginv2sdk.ContentSegmentText, Text: strings.Repeat("x", 3<<20)}}
	case source.Plain == "xss":
		result.Segments = []pluginv2sdk.ContentRenderSegment{{Kind: pluginv2sdk.ContentSegmentHTML, HTML: `<script>alert(1)</script><img src=x onerror=alert(1)><strong>safe</strong>`}}
	case strings.HasPrefix(source.Plain, "block:"):
		parts := strings.SplitN(source.Plain, ":", 3)
		if len(parts) != 3 {
			return pluginv2sdk.ContentResult{}, errors.New("invalid block fixture")
		}
		if err := os.WriteFile(parts[1], []byte("ready"), 0o600); err != nil {
			return pluginv2sdk.ContentResult{}, err
		}
		for {
			if _, err := os.Stat(parts[2]); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return pluginv2sdk.ContentResult{}, ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		fallthrough
	default:
		result.Segments = append(result.Segments, call.Render.Segments...)
		result.Segments = append(result.Segments, pluginv2sdk.ContentRenderSegment{
			Kind: pluginv2sdk.ContentSegmentHTML, HTML: "<strong>protocol-v2</strong>",
		})
	}
	return pluginv2sdk.ContentResult{Render: &result}, nil
}
