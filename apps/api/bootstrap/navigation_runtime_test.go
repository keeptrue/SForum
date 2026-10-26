package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

func TestProductionNavigationRuntimeAdmitsExactDeclarativeContribution(t *testing.T) {
	source := newNavigationRuntimeStub(t, navigationRuntimeArtifact{
		extensionID: "plugin.nav.prod", version: "1.0.0", versionID: 7,
		digest: strings.Repeat("a", 64), instanceID: "runtime-nav-a",
	})
	runtime := newProductionNavigationRuntime(source)
	if runtime == nil {
		t.Fatal("runtime adapter is nil")
	}

	artifact := navigationregistry.Artifact{
		ExtensionID: "plugin.nav.prod", ExtensionVersion: "1.0.0", VersionID: 7,
		PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
		RuntimeInstanceID: "runtime-nav-a",
	}
	if !runtime.Available(artifact) {
		t.Fatal("exact active runtime should be available")
	}
	// 摘要漂移必须拒绝，防止升级窗口串包。
	stale := artifact
	stale.PackageDigest = strings.Repeat("c", 64)
	if runtime.Available(stale) {
		t.Fatal("stale package digest must be unavailable")
	}

	lease, err := runtime.Acquire(context.Background(), artifact)
	if err != nil || lease == nil || lease.RuntimeInstanceID() != artifact.RuntimeInstanceID {
		t.Fatalf("acquire exact runtime: lease=%#v err=%v", lease, err)
	}
	if lease.Context() == nil || lease.Context().Err() != nil {
		t.Fatal("lease context must be live")
	}
	lease.Release()

	// Handler 渲染尚未接入：必须 fail closed。
	if _, err := runtime.RenderNavigation(context.Background(), navigationregistry.RuntimeInvocation{
		Handler: "plugin.nav.prod.render", Artifact: artifact,
	}); !errors.Is(err, navigationregistry.ErrRuntimeUnavailable) {
		t.Fatalf("handler render should fail closed: %v", err)
	}
	if _, err := runtime.RenderRegion(context.Background(), navigationregistry.RuntimeInvocation{
		Handler: "plugin.nav.prod.render.region", Artifact: artifact,
	}); !errors.Is(err, navigationregistry.ErrRuntimeUnavailable) {
		t.Fatalf("region handler render should fail closed: %v", err)
	}
}

func TestProductionNavigationRuntimeComposesDeclarativePluginItemAndQuarantines(t *testing.T) {
	store := &navigationChromeTestStore{navs: map[int64]sitechrome.NavItem{}}
	service := sitechrome.NewService(store)
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry)

	artifactMeta := navigationRuntimeArtifact{
		extensionID: "plugin.nav.compose", version: "1.2.0", versionID: 3,
		digest: strings.Repeat("d", 64), instanceID: "runtime-compose",
	}
	source := newNavigationRuntimeStub(t, artifactMeta)
	runtime := newProductionNavigationRuntime(source)
	service.WithNavigationRuntime(runtime, runtime)

	publication := navigationregistry.Publication{
		Artifact: navigationregistry.Artifact{
			ExtensionID: artifactMeta.extensionID, ExtensionVersion: artifactMeta.version,
			PackageDigest: artifactMeta.digest, ImpactDigest: strings.Repeat("e", 64),
			VersionID: artifactMeta.versionID, RuntimeInstanceID: artifactMeta.instanceID,
		},
		Navigation: []navigationregistry.NavigationDeclaration{{
			ID: "plugin.nav.compose.item.docs", ContractVersion: "plugin.nav.compose.item.docs@1",
			Kind: navigationregistry.NavigationKindItem, Action: navigationregistry.ActionAdd,
			TargetID: navigationregistry.CorePrimaryMenuID, Label: "Docs",
			Labels: map[string]string{"en-US": "Docs", "zh-CN": "文档"}, Href: "/docs", Order: 20,
		}},
	}
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}

	composed, err := service.ComposePublicNavigation(context.Background(), identity.Actor{}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(composed.Menus) != 1 || !navigationHasNode(composed.Menus[0].Children, "plugin.nav.compose.item.docs") {
		t.Fatalf("declarative plugin item missing: %#v", composed.Menus)
	}

	// 运行时不可用后声明式贡献应消失，Core 菜单仍保留。
	source.setAvailable(false)
	quarantined, err := service.ComposePublicNavigation(context.Background(), identity.Actor{}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantined.Menus) != 1 {
		t.Fatalf("core menu missing after quarantine: %#v", quarantined.Menus)
	}
	if navigationHasNode(quarantined.Menus[0].Children, "plugin.nav.compose.item.docs") {
		t.Fatalf("unavailable runtime still exposed plugin item: %#v", quarantined.Menus)
	}
}

func TestProductionNavigationRuntimeNilSourceIsUnavailable(t *testing.T) {
	if runtime := newProductionNavigationRuntime(nil); runtime != nil {
		t.Fatal("nil source must not construct adapter")
	}
}

func TestProductionNavigationRuntimeWithoutBindingKeepsPluginUnavailable(t *testing.T) {
	// 生产默认：未绑定 runtime 时非 Core 贡献不可用（与 SiteChrome 注释一致）。
	store := &navigationChromeTestStore{navs: map[int64]sitechrome.NavItem{}}
	service := sitechrome.NewService(store)
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry)
	// 故意不调用 WithNavigationRuntime。

	publication := navigationregistry.Publication{
		Artifact: navigationregistry.Artifact{
			ExtensionID: "plugin.nav.unbound", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("f", 64), ImpactDigest: strings.Repeat("1", 64),
			VersionID: 1, RuntimeInstanceID: "runtime-unbound",
		},
		Navigation: []navigationregistry.NavigationDeclaration{{
			ID: "plugin.nav.unbound.item", ContractVersion: "plugin.nav.unbound.item@1",
			Kind: navigationregistry.NavigationKindItem, Action: navigationregistry.ActionAdd,
			TargetID: navigationregistry.CorePrimaryMenuID, Label: "Unbound", Href: "/unbound",
		}},
	}
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}
	composed, err := service.ComposePublicNavigation(context.Background(), identity.Actor{}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if navigationHasNode(composed.Menus[0].Children, "plugin.nav.unbound.item") {
		t.Fatalf("unbound runtime exposed plugin item: %#v", composed.Menus)
	}
}

type navigationRuntimeArtifact struct {
	extensionID string
	version     string
	versionID   int64
	digest      string
	instanceID  string
}

type navigationRuntimeStub struct {
	meta      navigationRuntimeArtifact
	available bool
	gate      *extensionsruntime.RuntimeAdmissionGate
}

func newNavigationRuntimeStub(t *testing.T, meta navigationRuntimeArtifact) *navigationRuntimeStub {
	t.Helper()
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: meta.extensionID, InstanceID: meta.instanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &navigationRuntimeStub{meta: meta, available: true, gate: gate}
}

func (s *navigationRuntimeStub) setAvailable(value bool) { s.available = value }

func (s *navigationRuntimeStub) RuntimeInstanceAvailable(identity extensionsruntime.RuntimeInstanceIdentity) bool {
	return s != nil && s.available &&
		identity.ExtensionID == s.meta.extensionID && identity.InstanceID == s.meta.instanceID
}

func (s *navigationRuntimeStub) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	if !s.RuntimeInstanceAvailable(identity) {
		return nil, extensionsruntime.ErrRuntimeInstanceNotActive
	}
	return s.gate.Acquire(ctx, class)
}

func (s *navigationRuntimeStub) InspectRuntimeInstance(
	identity extensionsruntime.RuntimeInstanceIdentity,
) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if !s.RuntimeInstanceAvailable(identity) {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return extensionsruntime.RuntimeInstanceSnapshot{
		Identity: identity, ExtensionVersion: s.meta.version, ArtifactDigest: s.meta.digest,
		VersionID: s.meta.versionID, Active: true,
	}, nil
}

func navigationHasNode(items []sitechrome.ChromeNodeViewModel, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

// navigationChromeTestStore 仅覆盖 ComposePublicNavigation 需要的 ListNavItems。
type navigationChromeTestStore struct {
	navs map[int64]sitechrome.NavItem
}

func (s *navigationChromeTestStore) ListNavItems(_ context.Context, enabledOnly bool) ([]sitechrome.NavItem, error) {
	out := make([]sitechrome.NavItem, 0, len(s.navs))
	for _, item := range s.navs {
		if enabledOnly && !item.Enabled {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *navigationChromeTestStore) CreateNavItem(_ context.Context, input sitechrome.CreateNavItemInput) (sitechrome.NavItem, error) {
	return sitechrome.NavItem{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) UpdateNavItem(_ context.Context, input sitechrome.UpdateNavItemInput) (sitechrome.NavItem, error) {
	return sitechrome.NavItem{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) DeleteNavItem(context.Context, int64) error {
	return errors.New("not implemented")
}
func (s *navigationChromeTestStore) ListFriendLinks(context.Context, bool) ([]sitechrome.FriendLink, error) {
	return nil, nil
}
func (s *navigationChromeTestStore) CreateFriendLink(_ context.Context, _ sitechrome.CreateFriendLinkInput) (sitechrome.FriendLink, error) {
	return sitechrome.FriendLink{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) UpdateFriendLink(_ context.Context, _ sitechrome.UpdateFriendLinkInput) (sitechrome.FriendLink, error) {
	return sitechrome.FriendLink{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) DeleteFriendLink(context.Context, int64) error {
	return errors.New("not implemented")
}
func (s *navigationChromeTestStore) ListAnnouncements(context.Context, bool, bool) ([]sitechrome.Announcement, error) {
	return nil, nil
}
func (s *navigationChromeTestStore) CreateAnnouncement(_ context.Context, _ sitechrome.CreateAnnouncementInput) (sitechrome.Announcement, error) {
	return sitechrome.Announcement{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) UpdateAnnouncement(_ context.Context, _ sitechrome.UpdateAnnouncementInput) (sitechrome.Announcement, error) {
	return sitechrome.Announcement{}, errors.New("not implemented")
}
func (s *navigationChromeTestStore) DeleteAnnouncement(context.Context, int64) error {
	return errors.New("not implemented")
}
