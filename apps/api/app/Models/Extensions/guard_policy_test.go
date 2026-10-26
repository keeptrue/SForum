package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestGuardPolicyCatalogPublishesExactArtifactsWithoutLookupIO(t *testing.T) {
	plugin := guardPolicyFixture("guard.plugin", TypePlugin, strings.Repeat("a", 64))
	plugin.Manifest.Backend.Entry = "bin/plugin"
	plugin.Manifest.Backend.ProtocolVersion = 2
	plugin.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "sforum.lifecycle@2"}
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.StagedVersion = &ExtensionVersion{
		Version: "2.0.0", Manifest: plugin.Manifest, PackageDigest: strings.Repeat("b", 64),
	}
	plugin.StagedVersion.Manifest.Version = "2.0.0"
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	trust := &guardPolicyTrustStub{trusted: map[string]bool{
		plugin.PackageDigest: false, plugin.StagedVersion.PackageDigest: true,
	}}
	catalog := NewGuardPolicyCatalog(source, trust, nil, GuardPolicyConfig{
		TrustChallengesEnabled: true, TTL: time.Minute,
	})
	if _, ok := catalog.Lookup(plugin.ID); ok {
		t.Fatal("unpublished catalog must be unavailable")
	}
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lookup, ok := catalog.Lookup(plugin.ID)
	if !ok || !lookup.Found || lookup.Revision == 0 {
		t.Fatalf("lookup = %#v, ok=%v", lookup, ok)
	}
	entry := lookup.Entry
	if entry.ExtensionType != TypePlugin || entry.Version != "1.0.0" ||
		entry.PackageDigest != plugin.PackageDigest || entry.CurrentArtifactTrusted ||
		!entry.HasStagedArtifact || entry.StagedVersion != "2.0.0" ||
		entry.StagedPackageDigest != plugin.StagedVersion.PackageDigest || !entry.StagedArtifactTrusted {
		t.Fatalf("entry = %#v", entry)
	}
	for range 100 {
		if _, ok := catalog.Lookup(plugin.ID); !ok {
			t.Fatal("published catalog disappeared")
		}
	}
	if source.calls != 1 || trust.calls != 2 {
		t.Fatalf("lookup performed I/O: source=%d trust=%d", source.calls, trust.calls)
	}
}

func TestGuardPolicyCatalogRejectsDriftAndEventuallyExpires(t *testing.T) {
	plugin := guardPolicyFixture("guard.plugin", TypePlugin, strings.Repeat("a", 64))
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(source, &guardPolicyTrustStub{}, nil, GuardPolicyConfig{TTL: 60 * time.Millisecond})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := catalog.Lookup(plugin.ID)

	drifted := plugin
	drifted.Manifest.Type = TypeTheme
	source.set([]Extension{drifted}, nil)
	if err := catalog.Refresh(context.Background()); err == nil {
		t.Fatal("manifest identity drift was accepted")
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || after.Revision != before.Revision {
		t.Fatalf("failed refresh replaced valid snapshot: before=%#v after=%#v", before, after)
	}

	time.Sleep(75 * time.Millisecond)
	if _, ok := catalog.Lookup(plugin.ID); ok {
		t.Fatal("failed refresh kept stale catalog indefinitely")
	}
}

func TestGuardPolicyCatalogSkipsOnlyInvalidDisabledArtifacts(t *testing.T) {
	valid := guardPolicyFixture("guard.valid", TypePlugin, strings.Repeat("a", 64))
	invalidDisabled := guardPolicyFixture("guard.disabled", TypePlugin, strings.Repeat("b", 64))
	invalidDisabled.Status = StatusDisabled
	invalidDisabled.Manifest.ID = "drifted.disabled"
	source := &guardPolicySourceStub{items: []Extension{valid, invalidDisabled}}
	catalog := NewGuardPolicyCatalog(source, &guardPolicyTrustStub{}, nil, GuardPolicyConfig{TTL: time.Minute})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatalf("disabled invalid artifact blocked recovery refresh: %v", err)
	}
	if lookup, ok := catalog.Lookup(valid.ID); !ok || !lookup.Found {
		t.Fatalf("valid artifact disappeared: lookup=%#v ok=%v", lookup, ok)
	}
	if lookup, ok := catalog.Lookup(invalidDisabled.ID); !ok || lookup.Found {
		t.Fatalf("invalid disabled artifact was published: lookup=%#v ok=%v", lookup, ok)
	}

	invalidDisabled.Status = StatusEnabled
	source.set([]Extension{valid, invalidDisabled}, nil)
	if err := catalog.Refresh(context.Background()); !errors.Is(err, errGuardPolicyArtifactInvalid) {
		t.Fatalf("enabled invalid artifact error = %v", err)
	}
}

func TestGuardPolicyCatalogSkipsMissingDisabledPackageButFailsClosedWhenEnabled(t *testing.T) {
	missing := guardPolicyFixture("guard.missing", TypePlugin, strings.Repeat("b", 64))
	missing.Status = StatusDisabled
	missing.Source = SourceUploaded
	missing.IsSystem = false
	missing.IsDeletable = true
	missing.Manifest.Backend.Entry = "backend/plugin"
	source := &guardPolicySourceStub{items: []Extension{missing}}
	catalog := NewGuardPolicyCatalog(
		source,
		guardPolicyTrustErrorStub{err: fmt.Errorf("verify exact package: %w", ErrFrontendPackageChanged)},
		nil,
		GuardPolicyConfig{TTL: time.Minute},
	)
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatalf("disabled missing package blocked recovery refresh: %v", err)
	}
	if lookup, ok := catalog.Lookup(missing.ID); !ok || lookup.Found {
		t.Fatalf("disabled missing package was published: lookup=%#v ok=%v", lookup, ok)
	}

	missing.Status = StatusEnabled
	source.set([]Extension{missing}, nil)
	if err := catalog.Refresh(context.Background()); !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("enabled missing package error = %v", err)
	}

	trustUnavailable := errors.New("trust database unavailable")
	missing.Status = StatusDisabled
	source.set([]Extension{missing}, nil)
	closed := NewGuardPolicyCatalog(
		source,
		guardPolicyTrustErrorStub{err: trustUnavailable},
		nil,
		GuardPolicyConfig{TTL: time.Minute},
	)
	if err := closed.Refresh(context.Background()); !errors.Is(err, trustUnavailable) {
		t.Fatalf("disabled trust source failure was hidden: %v", err)
	}
}

func TestGuardPolicyCatalogRefreshFailurePreservesThenCloses(t *testing.T) {
	plugin := guardPolicyFixture("guard.plugin", TypePlugin, strings.Repeat("a", 64))
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(source, &guardPolicyTrustStub{}, nil, GuardPolicyConfig{TTL: 80 * time.Millisecond})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	source.set(nil, errors.New("database unavailable"))
	if err := catalog.Refresh(context.Background()); err == nil {
		t.Fatal("refresh failure was hidden")
	}
	if _, ok := catalog.Lookup(plugin.ID); !ok {
		t.Fatal("one failed refresh discarded a still-valid snapshot")
	}
	time.Sleep(75 * time.Millisecond)
	if _, ok := catalog.Lookup(plugin.ID); ok {
		t.Fatal("catalog remained available after refresh failures exceeded TTL")
	}
}

func TestGuardPolicyCatalogConcurrentRefreshAndLookup(t *testing.T) {
	plugin := guardPolicyFixture("guard.plugin", TypePlugin, strings.Repeat("a", 64))
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(source, &guardPolicyTrustStub{}, nil, GuardPolicyConfig{TTL: time.Minute})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 200 {
				lookup, ok := catalog.Lookup(plugin.ID)
				if !ok || !lookup.Found || lookup.Entry.PackageDigest != plugin.PackageDigest {
					t.Errorf("lookup = %#v, ok=%v", lookup, ok)
					return
				}
			}
		}()
	}
	for range 20 {
		if err := catalog.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
}

func TestGuardPolicyCatalogInvalidatesExecutableTrustWithoutLookupIO(t *testing.T) {
	plugin := guardPolicyFixture("guard.revoked", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	plugin.Manifest.Routes = []ManifestRoute{{
		Path: "/public", Methods: []string{"GET"}, Access: RouteAccessPublic,
	}}
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	trust := &guardPolicyTrustStub{trusted: map[string]bool{plugin.PackageDigest: true}}
	catalog := NewGuardPolicyCatalog(source, trust, nil, GuardPolicyConfig{
		TrustChallengesEnabled: true, TTL: time.Minute,
	})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, ok := catalog.Lookup(plugin.ID)
	if !ok || !before.Entry.CurrentArtifactTrusted {
		t.Fatalf("before=%#v ok=%t", before, ok)
	}
	if _, ok := catalog.LookupDeclaredRoute(plugin.ID, "GET", "/public"); !ok {
		t.Fatal("trusted declared route was unavailable")
	}
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, before.Entry) {
		t.Fatal("published artifact was not invalidated")
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || after.Revision != before.Revision+1 || after.Entry.CurrentArtifactTrusted ||
		after.Entry.FrontendArtifactTrusted {
		t.Fatalf("after=%#v ok=%t", after, ok)
	}
	if _, ok := catalog.LookupDeclaredRoute(plugin.ID, "GET", "/public"); ok {
		t.Fatal("revoked declared route remained available")
	}
	if source.calls != 1 || trust.calls != 1 {
		t.Fatalf("invalidation performed I/O: source=%d trust=%d", source.calls, trust.calls)
	}
}

func TestGuardPolicyCatalogInvalidatesCapturedCurrentReviewAndStagedSlots(t *testing.T) {
	plugin := guardPolicyFixture("guard.revoked-slots", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	plugin.StagedVersion = &ExtensionVersion{
		Version: "2.0.0", Manifest: plugin.Manifest, PackageDigest: strings.Repeat("b", 64),
	}
	plugin.StagedVersion.Manifest.Version = "2.0.0"
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	trust := &guardPolicyTrustStub{trusted: map[string]bool{
		plugin.PackageDigest: true, plugin.StagedVersion.PackageDigest: true,
	}}
	catalog := NewGuardPolicyCatalog(source, trust, nil, GuardPolicyConfig{
		TrustChallengesEnabled: true, TTL: time.Minute,
	})
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, ok := catalog.Lookup(plugin.ID)
	if !ok || !captured.Entry.CurrentArtifactTrusted || !captured.Entry.ReviewArtifactTrusted ||
		!captured.Entry.StagedArtifactTrusted {
		t.Fatalf("captured=%#v ok=%t", captured, ok)
	}
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, captured.Entry) {
		t.Fatal("captured executable trust slots were not invalidated")
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || after.Entry.CurrentArtifactTrusted || after.Entry.FrontendArtifactTrusted ||
		after.Entry.ReviewArtifactTrusted || after.Entry.StagedArtifactTrusted {
		t.Fatalf("invalidated slots=%#v ok=%t", after, ok)
	}
}

func TestGuardPolicyCatalogExecutableTrustCASPreservesConcurrentArtifacts(t *testing.T) {
	plugin := guardPolicyFixture("guard.cas", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	plugin.StagedVersion = &ExtensionVersion{
		Version: "2.0.0", Manifest: plugin.Manifest, PackageDigest: strings.Repeat("b", 64),
	}
	plugin.StagedVersion.Manifest.Version = "2.0.0"
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	trust := &guardPolicyTrustStub{trusted: map[string]bool{
		plugin.PackageDigest: true, plugin.StagedVersion.PackageDigest: true,
	}}
	catalog := NewGuardPolicyCatalog(source, trust, nil, GuardPolicyConfig{
		TrustChallengesEnabled: true, TTL: time.Minute,
	})
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, ok := catalog.Lookup(plugin.ID)
	if !ok || !captured.Entry.CurrentArtifactTrusted || !captured.Entry.StagedArtifactTrusted {
		t.Fatalf("captured=%#v ok=%t", captured, ok)
	}

	// Current stays on v1, while a concurrently authorized upgrade replaces the
	// review/staged slots. The stale revoke may close only the captured v1 slot.
	drifted := plugin
	drifted.StagedVersion = &ExtensionVersion{
		Version: "3.0.0", Manifest: plugin.Manifest, PackageDigest: strings.Repeat("c", 64),
	}
	drifted.StagedVersion.Manifest.Version = "3.0.0"
	trust.trusted[drifted.StagedVersion.PackageDigest] = true
	source.set([]Extension{drifted}, nil)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	beforeCAS, _ := catalog.Lookup(plugin.ID)
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, captured.Entry) {
		t.Fatal("captured current slot was not invalidated")
	}
	afterCAS, ok := catalog.Lookup(plugin.ID)
	if !ok || afterCAS.Revision != beforeCAS.Revision+1 || afterCAS.Entry.CurrentArtifactTrusted ||
		afterCAS.Entry.FrontendArtifactTrusted || !afterCAS.Entry.ReviewArtifactTrusted ||
		!afterCAS.Entry.StagedArtifactTrusted || afterCAS.Entry.StagedVersion != "3.0.0" {
		t.Fatalf("partial CAS result=%#v", afterCAS)
	}

	// When every slot moved, stale work is a true no-op and must not churn the
	// immutable catalog revision.
	fullyDrifted := drifted
	fullyDrifted.Version = "4.0.0"
	fullyDrifted.Manifest.Version = "4.0.0"
	fullyDrifted.PackageDigest = strings.Repeat("d", 64)
	fullyDrifted.StagedVersion = &ExtensionVersion{
		Version: "5.0.0", Manifest: fullyDrifted.Manifest, PackageDigest: strings.Repeat("e", 64),
	}
	fullyDrifted.StagedVersion.Manifest.Version = "5.0.0"
	trust.trusted[fullyDrifted.PackageDigest] = true
	trust.trusted[fullyDrifted.StagedVersion.PackageDigest] = true
	source.set([]Extension{fullyDrifted}, nil)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	beforeNoop, _ := catalog.Lookup(plugin.ID)
	if catalog.InvalidateExecutableTrustExact(plugin.ID, captured.Entry) {
		t.Fatal("stale capture invalidated fully replaced artifacts")
	}
	afterNoop, _ := catalog.Lookup(plugin.ID)
	if afterNoop.Revision != beforeNoop.Revision || !afterNoop.Entry.CurrentArtifactTrusted ||
		!afterNoop.Entry.ReviewArtifactTrusted || !afterNoop.Entry.StagedArtifactTrusted {
		t.Fatalf("stale no-op changed catalog: before=%#v after=%#v", beforeNoop, afterNoop)
	}
}

func TestGuardPolicyCatalogStaleRefreshCannotRepublishRevokedTrust(t *testing.T) {
	plugin := guardPolicyFixture("guard.refresh-revoke", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(
		source,
		&guardPolicyTrustStub{trusted: map[string]bool{plugin.PackageDigest: true}},
		nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, ok := catalog.Lookup(plugin.ID)
	if !ok || !captured.Entry.CurrentArtifactTrusted {
		t.Fatalf("captured=%#v ok=%t", captured, ok)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	catalog.executableTrust = &blockingGuardPolicyTrustStub{started: started, release: release}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- catalog.Refresh(t.Context()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stale refresh did not reach trust read")
	}
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, captured.Entry) {
		t.Fatal("captured trust was not invalidated")
	}
	close(release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale refresh did not finish")
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || after.Entry.CurrentArtifactTrusted || after.Entry.FrontendArtifactTrusted {
		t.Fatalf("stale refresh republished revoked trust: %#v ok=%t", after, ok)
	}
}

func TestGuardPolicyCatalogCaptureReadsExpiredExactEntryAndFencesRefresh(t *testing.T) {
	plugin := guardPolicyFixture("guard.expired-capture", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(
		source,
		&guardPolicyTrustStub{trusted: map[string]bool{plugin.PackageDigest: true}},
		nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, ok := catalog.Lookup(plugin.ID)
	if !ok || !before.Entry.CurrentArtifactTrusted {
		t.Fatalf("before=%#v ok=%t", before, ok)
	}
	catalog.mu.Lock()
	catalog.snapshot.expiresAt = time.Now().Add(-time.Second)
	catalog.mu.Unlock()
	if _, ok := catalog.Lookup(plugin.ID); ok {
		t.Fatal("expired policy remained available to request lookup")
	}

	captured, found := catalog.CaptureExecutableTrustExact(plugin.ID)
	if !found || captured.ExtensionID != plugin.ID || captured.Version != plugin.Version ||
		captured.PackageDigest != plugin.PackageDigest || !captured.CurrentTrustRequired ||
		!captured.CurrentArtifactTrusted {
		t.Fatalf("expired exact capture=%#v found=%t", captured, found)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	catalog.executableTrust = &blockingGuardPolicyTrustStub{started: started, release: release}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- catalog.Refresh(t.Context()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not enter durable-revoke window")
	}
	close(release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish inside durable-revoke window")
	}
	if catalog.revision != before.Revision {
		t.Fatalf("refresh published during pending revoke: revision=%d want=%d", catalog.revision, before.Revision)
	}
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, captured) {
		t.Fatal("expired exact capture was not invalidated")
	}
	// The durable source still says trusted, modeling an ambiguous COMMIT that
	// rolled back. A normal refresh must retain the exact fail-closed tombstone.
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || after.Entry.CurrentArtifactTrusted || after.Entry.FrontendArtifactTrusted {
		t.Fatalf("ordinary refresh resurrected expired captured trust: %#v ok=%t", after, ok)
	}
}

func TestGuardPolicyCatalogReleasesCaptureAfterDefiniteRollback(t *testing.T) {
	plugin := guardPolicyFixture("guard.capture-release", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	catalog := NewGuardPolicyCatalog(
		&guardPolicySourceStub{items: []Extension{plugin}},
		&guardPolicyTrustStub{trusted: map[string]bool{plugin.PackageDigest: true}},
		nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, found := catalog.CaptureExecutableTrustExact(plugin.ID)
	if !found {
		t.Fatal("exact capture was unavailable")
	}
	if !catalog.ReleaseExecutableTrustCaptureExact(plugin.ID, captured) {
		t.Fatal("definite rollback did not release exact capture")
	}
	if catalog.ReleaseExecutableTrustCaptureExact(plugin.ID, captured) {
		t.Fatal("stale release removed a nonexistent/newer capture")
	}
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, ok := catalog.Lookup(plugin.ID)
	if !ok || !after.Entry.CurrentArtifactTrusted {
		t.Fatalf("definite rollback remained fenced: %#v ok=%t", after, ok)
	}
}

func TestGuardPolicyCatalogUnknownCommitTombstoneUsesGrantGeneration(t *testing.T) {
	plugin := guardPolicyFixture("guard.unknown-generation", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	trust := &guardPolicyGenerationTrustStub{grantID: "41"}
	catalog := NewGuardPolicyCatalog(
		&guardPolicySourceStub{items: []Extension{plugin}}, trust, nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, found := catalog.CaptureExecutableTrustExact(plugin.ID)
	if !found || captured.currentTrustGrantID != "41" {
		t.Fatalf("capture=%#v found=%t", captured, found)
	}
	if !catalog.InvalidateExecutableTrustExact(plugin.ID, captured) {
		t.Fatal("grant generation 41 was not invalidated")
	}
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	rolledBack, ok := catalog.Lookup(plugin.ID)
	if !ok || rolledBack.Entry.CurrentArtifactTrusted {
		t.Fatalf("old live grant generation was resurrected: %#v ok=%t", rolledBack, ok)
	}

	// A new grant row for the same package is an explicit reauthorization and
	// must not be erased by the old generation's tombstone.
	trust.setGrant("42")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	reauthorized, ok := catalog.Lookup(plugin.ID)
	if !ok || !reauthorized.Entry.CurrentArtifactTrusted || reauthorized.Entry.currentTrustGrantID != "42" {
		t.Fatalf("new grant generation stayed fenced: %#v ok=%t", reauthorized, ok)
	}

	secondCapture, found := catalog.CaptureExecutableTrustExact(plugin.ID)
	if !found || !catalog.InvalidateExecutableTrustExact(plugin.ID, secondCapture) {
		t.Fatal("second exact generation was not invalidated")
	}
	trust.setGrant("")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	trust.setGrant("43")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	resolved, ok := catalog.Lookup(plugin.ID)
	if !ok || !resolved.Entry.CurrentArtifactTrusted || resolved.Entry.currentTrustGrantID != "43" {
		t.Fatalf("durable negative read did not resolve tombstone: %#v ok=%t", resolved, ok)
	}
}

func TestGuardPolicyCatalogUnknownGenerationStaysClosedUntilDurableNegative(t *testing.T) {
	plugin := guardPolicyFixture("guard.unknown-wildcard", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	trust := &guardPolicyGenerationTrustStub{}
	catalog := NewGuardPolicyCatalog(
		&guardPolicySourceStub{items: []Extension{plugin}}, trust, nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, found := catalog.CaptureExecutableTrustExact(plugin.ID)
	if !found || !captured.CurrentTrustRequired || captured.CurrentArtifactTrusted ||
		captured.currentTrustGrantID != "" {
		t.Fatalf("pre-grant capture=%#v found=%t", captured, found)
	}
	// The grant became live after the captured policy snapshot. An ambiguous
	// revoke cannot distinguish this still-live grant from a later generation.
	trust.setGrant("41")
	if catalog.InvalidateExecutableTrustExact(plugin.ID, captured) {
		t.Fatal("unpublished trust unexpectedly changed the snapshot revision")
	}
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldGrant, ok := catalog.Lookup(plugin.ID)
	if !ok || oldGrant.Entry.CurrentArtifactTrusted {
		t.Fatalf("unknown generation reopened old grant: %#v ok=%t", oldGrant, ok)
	}

	trust.setGrant("")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	trust.setGrant("42")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	reauthorized, ok := catalog.Lookup(plugin.ID)
	if !ok || !reauthorized.Entry.CurrentArtifactTrusted ||
		reauthorized.Entry.currentTrustGrantID != "42" {
		t.Fatalf("durable negative did not release wildcard: %#v ok=%t", reauthorized, ok)
	}
}

func TestGuardPolicyCatalogMissingSnapshotUsesRuntimeFallbackTombstone(t *testing.T) {
	plugin := guardPolicyFixture("guard.missing-fallback", TypePlugin, strings.Repeat("a", 64))
	plugin.Source, plugin.IsSystem, plugin.IsDeletable = SourceUploaded, false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	trust := &guardPolicyGenerationTrustStub{grantID: "41"}
	catalog := NewGuardPolicyCatalog(
		&guardPolicySourceStub{items: []Extension{plugin}}, trust, nil,
		GuardPolicyConfig{TrustChallengesEnabled: true, TTL: time.Minute},
	)
	fallback := GuardPolicyEntry{
		ExtensionID: plugin.ID, Version: plugin.Version, PackageDigest: plugin.PackageDigest,
		CurrentTrustRequired: true, CurrentArtifactTrusted: true,
	}
	captured, found := catalog.CaptureExecutableTrustExactWithFallback(plugin.ID, fallback)
	if found || captured != fallback {
		t.Fatalf("fallback capture=%#v found=%t", captured, found)
	}
	if catalog.InvalidateExecutableTrustExact(plugin.ID, captured) {
		t.Fatal("missing snapshot unexpectedly published an invalidation revision")
	}
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldGrant, ok := catalog.Lookup(plugin.ID)
	if !ok || oldGrant.Entry.CurrentArtifactTrusted {
		t.Fatalf("runtime fallback reopened old grant: %#v ok=%t", oldGrant, ok)
	}

	trust.setGrant("")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	trust.setGrant("42")
	if err := catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	reauthorized, ok := catalog.Lookup(plugin.ID)
	if !ok || !reauthorized.Entry.CurrentArtifactTrusted ||
		reauthorized.Entry.currentTrustGrantID != "42" {
		t.Fatalf("fallback tombstone did not resolve: %#v ok=%t", reauthorized, ok)
	}
}

func TestGuardPolicyCatalogFreezesOnlySimpleDeclaredRouteGuards(t *testing.T) {
	plugin := guardPolicyFixture("guard.plugin", TypePlugin, strings.Repeat("a", 64))
	plugin.Manifest.Routes = []ManifestRoute{
		{Path: "/public", Methods: []string{"GET"}, Access: RouteAccessPublic},
		{Path: "/login", Methods: []string{"POST"}, Access: RouteAccessLogin},
		{Path: "/manage", Methods: []string{"POST"}, Access: RouteAccessPermission, Permission: "topic.create"},
		{Path: "/raw", Methods: []string{"POST"}, Guard: extensionmanifest.GuardCoreRaw},
		{Path: "/inherit", Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreInherit},
		{Path: "/guest", Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreGuest},
	}
	source := &guardPolicySourceStub{items: []Extension{plugin}}
	catalog := NewGuardPolicyCatalog(source, &guardPolicyTrustStub{}, nil, GuardPolicyConfig{TTL: time.Minute})
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, method, access, permission string
	}{
		{path: "/public", method: "GET", access: RouteAccessPublic},
		{path: "/login", method: "post", access: RouteAccessLogin},
		{path: "/manage", method: "POST", access: RouteAccessPermission, permission: "topic.create"},
	} {
		lookup, ok := catalog.LookupDeclaredRoute(plugin.ID, test.method, test.path)
		if !ok || lookup.Revision == 0 || lookup.ExtensionID != plugin.ID ||
			lookup.ExtensionVersion != plugin.Version || lookup.PackageDigest != plugin.PackageDigest ||
			lookup.Access != test.access || lookup.Permission != test.permission {
			t.Fatalf("%s lookup = %#v, ok=%v", test.path, lookup, ok)
		}
	}
	for _, test := range []struct{ path, method string }{
		{path: "/raw", method: "POST"},
		{path: "/inherit", method: "GET"},
		{path: "/guest", method: "GET"},
		{path: "/missing", method: "GET"},
	} {
		if _, ok := catalog.LookupDeclaredRoute(plugin.ID, test.method, test.path); ok {
			t.Fatalf("unsafe route %s was published", test.path)
		}
	}
	for range 100 {
		if _, ok := catalog.LookupDeclaredRoute(plugin.ID, "GET", "/public"); !ok {
			t.Fatal("published route disappeared")
		}
	}
	if source.calls != 1 {
		t.Fatalf("declared route hot path reached Store: calls=%d", source.calls)
	}
	plugin.Source = SourceUploaded
	plugin.IsSystem, plugin.IsDeletable = false, true
	plugin.Manifest.Backend.Entry = "bin/plugin"
	source.set([]Extension{plugin}, nil)
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.LookupDeclaredRoute(plugin.ID, "GET", "/public"); ok {
		t.Fatal("untrusted executable route remained published")
	}
}

func guardPolicyFixture(id, extensionType, digest string) Extension {
	manifest := Manifest{ID: id, Name: id, Version: "1.0.0", Type: extensionType}
	return Extension{
		ID: id, Name: id, Version: "1.0.0", Type: extensionType,
		Status: StatusEnabled, Source: SourceBuiltin, IsSystem: true,
		Manifest: manifest, PackageDigest: digest,
	}
}

type guardPolicySourceStub struct {
	mu    sync.Mutex
	items []Extension
	err   error
	calls int
}

func (s *guardPolicySourceStub) List(context.Context) ([]Extension, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return append([]Extension(nil), s.items...), s.err
}

func (s *guardPolicySourceStub) set(items []Extension, err error) {
	s.mu.Lock()
	s.items, s.err = items, err
	s.mu.Unlock()
}

type guardPolicyTrustStub struct {
	mu      sync.Mutex
	trusted map[string]bool
	calls   int
}

type guardPolicyTrustErrorStub struct {
	err error
}

func (s guardPolicyTrustErrorStub) TrustedArtifact(context.Context, Extension) (bool, error) {
	return false, s.err
}

type blockingGuardPolicyTrustStub struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type guardPolicyGenerationTrustStub struct {
	mu      sync.Mutex
	grantID string
}

func (s *blockingGuardPolicyTrustStub) TrustedArtifact(ctx context.Context, _ Extension) (bool, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-s.release:
		return true, nil
	}
}

func (s *guardPolicyGenerationTrustStub) TrustedArtifact(context.Context, Extension) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grantID != "", nil
}

func (s *guardPolicyGenerationTrustStub) RuntimeIdentity(context.Context, Extension) (RuntimeTrustIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grantID == "" {
		return RuntimeTrustIdentity{}, ErrTrustGrantNotFound
	}
	return RuntimeTrustIdentity{TrustGrantID: s.grantID, ImpactDigest: strings.Repeat("f", 64)}, nil
}

func (s *guardPolicyGenerationTrustStub) setGrant(grantID string) {
	s.mu.Lock()
	s.grantID = grantID
	s.mu.Unlock()
}

func (s *guardPolicyTrustStub) TrustedArtifact(_ context.Context, extension Extension) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.trusted[extension.PackageDigest], nil
}
