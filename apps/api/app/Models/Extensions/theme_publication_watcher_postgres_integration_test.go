package extensions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

func TestPostgresThemeRuntimeWatchersConvergeWithListenAndMissedNotificationPoll(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "two-node-watcher")
	defaultTheme := saveExactWatcherTheme(t, fixture, DefaultThemeID, "1.0.0", "/default")
	current := fixture.activeTheme()
	initial, err := fixture.store.ActivateThemeExact(
		fixture.ctx, defaultTheme.ID, exactThemeActivationInput(current, defaultTheme, 70, false),
	)
	if err != nil {
		t.Fatal(err)
	}

	newNode := func(identity ThemeRuntimeNodeIdentity) (*ThemeRuntimeWatcher, *pages.ThemeRuntimeRegistry, chan error) {
		registry := pages.NewRegistry(pages.NewMemoryStore())
		runtimeRegistry := pages.NewThemeRuntimeRegistry()
		adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
		service := NewServiceWithOptions(
			fixture.store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(adapter),
		)
		errorsCh := make(chan error, 16)
		watcher, watcherErr := NewThemeRuntimeWatcher(fixture.store, service, nil, ThemeRuntimeWatcherConfig{
			Identity: identity, LeaseDuration: time.Second, HeartbeatInterval: 100 * time.Millisecond,
			PollInterval: 25 * time.Millisecond,
			OnError: func(err error) {
				select {
				case errorsCh <- err:
				default:
				}
			},
		})
		if watcherErr != nil {
			t.Fatal(watcherErr)
		}
		return watcher, runtimeRegistry, errorsCh
	}

	listenIdentity := ThemeRuntimeNodeIdentity{NodeID: "api-listen", BootID: "boot-listen"}
	pollIdentity := ThemeRuntimeNodeIdentity{NodeID: "api-poll", BootID: "boot-poll"}
	listenWatcher, listenRuntime, listenErrors := newNode(listenIdentity)
	pollWatcher, pollRuntime, pollErrors := newNode(pollIdentity)
	ready := make(chan struct{})
	var readyOnce sync.Once
	listenerPIDs := make(chan uint32, 4)
	notificationErrors := make(chan error, 4)
	notifications := NewPostgresThemeRuntimeNotifications(fixture.store, 10*time.Millisecond, func(err error) {
		select {
		case notificationErrors <- err:
		default:
		}
	})
	notifications.ready = func() { readyOnce.Do(func() { close(ready) }) }
	notifications.connected = func(pid uint32) { listenerPIDs <- pid }
	listenWatcher.notifications = notifications
	// A long poll makes the LISTEN wakeup authoritative for this node. The other
	// node has no notification source and proves missed notifications recover.
	listenWatcher.config.PollInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- listenWatcher.Run(ctx) }()
	go func() { results <- pollWatcher.Run(ctx) }()
	waitThemeWatcherCondition(t, 8*time.Second, func() bool {
		return themeWatcherApplied(fixture, listenIdentity, initial.Publication.Revision) &&
			themeWatcherApplied(fixture, pollIdentity, initial.Publication.Revision) &&
			themeRuntimeArtifactIs(listenRuntime, defaultTheme.ID, defaultTheme.Version, defaultTheme.PackageDigest) &&
			themeRuntimeArtifactIs(pollRuntime, defaultTheme.ID, defaultTheme.Version, defaultTheme.PackageDigest)
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL LISTEN connection did not become ready")
	}
	firstListenerPID := waitThemeWatcherListenerPID(t, listenerPIDs, 5*time.Second)
	var terminated bool
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT pg_terminate_backend($1)`, firstListenerPID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate LISTEN backend pid=%d terminated=%t err=%v", firstListenerPID, terminated, err)
	}
	secondListenerPID := waitThemeWatcherListenerPID(t, listenerPIDs, 5*time.Second)
	if secondListenerPID == firstListenerPID {
		t.Fatalf("LISTEN reconnect reused terminated pid=%d", firstListenerPID)
	}
	select {
	case reconnectErr := <-notificationErrors:
		if reconnectErr == nil {
			t.Fatal("LISTEN disconnect reported a nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("LISTEN disconnect was not reported before reconnect")
	}

	target := saveExactWatcherTheme(t, fixture, fixture.prefix+".custom", "2.0.0", "/custom")
	activation, err := fixture.store.ActivateThemeExact(
		fixture.ctx, target.ID, exactThemeActivationInput(defaultTheme, target, 71, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	waitThemeWatcherCondition(t, 8*time.Second, func() bool {
		return themeWatcherApplied(fixture, listenIdentity, activation.Publication.Revision) &&
			themeWatcherApplied(fixture, pollIdentity, activation.Publication.Revision) &&
			themeRuntimeArtifactIs(listenRuntime, target.ID, target.Version, target.PackageDigest) &&
			themeRuntimeArtifactIs(pollRuntime, target.ID, target.Version, target.PackageDigest)
	})
	assertNoThemeWatcherError(t, listenErrors)
	assertNoThemeWatcherError(t, pollErrors)
	assertNoThemeWatcherError(t, notificationErrors)
	cancel()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("watcher shutdown error=%v", err)
		}
	}
}

func TestPostgresThemeRuntimeWatcherAppliesApprovedCoreReplacement(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "approved-watcher")
	defaultTheme := saveExactWatcherTheme(t, fixture, DefaultThemeID, "1.0.0", "/default")
	current := fixture.activeTheme()
	if _, err := fixture.store.ActivateThemeExact(
		fixture.ctx, defaultTheme.ID, exactThemeActivationInput(current, defaultTheme, 72, false),
	); err != nil {
		t.Fatal(err)
	}

	registryStore := pages.NewPostgresStore(fixture.pool)
	registry := pages.NewRegistry(registryStore)
	if err := registry.RestoreBindings(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	service := NewServiceWithOptions(
		fixture.store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(adapter),
	)
	identity := ThemeRuntimeNodeIdentity{NodeID: "api-approved", BootID: "boot-approved"}
	watcher, err := NewThemeRuntimeWatcher(fixture.store, service, nil, ThemeRuntimeWatcherConfig{
		Identity: identity, LeaseDuration: time.Second, HeartbeatInterval: 100 * time.Millisecond,
		PollInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Initialize(fixture.ctx); err != nil {
		t.Fatalf("initialize approved watcher: %v", err)
	}

	actorID := fixture.addUser("approved-watcher")
	target := saveExactWatcherTheme(t, fixture, fixture.prefix+".approved", "2.0.0", "/approved")
	input := exactThemeActivationInput(defaultTheme, target, actorID, true)
	activation, err := fixture.store.ActivateThemeExact(fixture.ctx, target.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.ReconcileOnce(fixture.ctx); err != nil {
		t.Fatalf("apply approved publication: %v", err)
	}
	if !themeWatcherApplied(fixture, identity, activation.Publication.Revision) ||
		!themeRuntimeArtifactIs(runtimeRegistry, target.ID, target.Version, target.PackageDigest) {
		t.Fatal("approved exact runtime did not converge")
	}
	resolved, err := registry.Resolve(fixture.ctx, "forum.home")
	if err != nil || resolved.Provider != target.ID || resolved.PackageDigest != target.PackageDigest {
		t.Fatalf("approved provider=%#v err=%v", resolved, err)
	}
	binding, ok, err := registryStore.GetBinding(fixture.ctx, "forum.home")
	if err != nil || !ok || binding.ExtensionID != target.ID || binding.ApprovedBy != actorID ||
		binding.PackageDigest != target.PackageDigest {
		t.Fatalf("approved binding=%#v ok=%t err=%v", binding, ok, err)
	}
}

func saveExactWatcherTheme(
	t *testing.T,
	fixture *themePublicationPGFixture,
	id string,
	version string,
	path string,
) Extension {
	t.Helper()
	packageFixture := exactThemeRuntimeExtensionFixture(t, id, path)
	manifest := Manifest{ID: id, Name: id, Version: version, Type: TypeTheme}
	if err := writeManifest(packageFixture.PackagePath, manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packageFixture.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	item, err := fixture.store.SaveInstalled(fixture.ctx, SaveInstalledInput{
		Manifest: manifest, PackagePath: packageFixture.PackagePath, PackageDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.extensionIDs = append(fixture.extensionIDs, id)
	return item
}

func waitThemeWatcherCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("theme watcher convergence timed out")
}

func waitThemeWatcherListenerPID(t *testing.T, pids <-chan uint32, timeout time.Duration) uint32 {
	t.Helper()
	select {
	case pid := <-pids:
		return pid
	case <-time.After(timeout):
		t.Fatal("theme watcher LISTEN reconnect timed out")
		return 0
	}
}

func themeWatcherApplied(fixture *themePublicationPGFixture, identity ThemeRuntimeNodeIdentity, revision int64) bool {
	node, err := fixture.store.GetThemeRuntimeNode(fixture.ctx, identity)
	return err == nil && node.LastAppliedRevision >= revision
}

func themeRuntimeArtifactIs(runtime *pages.ThemeRuntimeRegistry, id, version, digest string) bool {
	snapshot, _, ok := runtime.Active()
	if !ok {
		return false
	}
	artifact := snapshot.Artifact()
	return artifact.ExtensionID == id && artifact.ExtensionVersion == version && artifact.PackageDigest == digest
}

func assertNoThemeWatcherError(t *testing.T, errorsCh <-chan error) {
	t.Helper()
	select {
	case err := <-errorsCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("watcher reported error=%v", err)
		}
	default:
	}
}
