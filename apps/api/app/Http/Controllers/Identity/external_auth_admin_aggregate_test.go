package identitycontroller

import (
	"context"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// M3/T8B Host 聚合：discovered/trusted/enabled/configured/probed/publiclyActivated、
// 绝对 callbackUrl、label/icon、包目录 discovery、第二 fake provider、Safe Mode/reset。

type callbackURLTestOptions struct {
	siteURL string
}

func (o callbackURLTestOptions) SiteName(context.Context) (string, error)   { return "SForum", nil }
func (o callbackURLTestOptions) AdminEmail(context.Context) (string, error) { return "", nil }
func (o callbackURLTestOptions) PasswordPolicy(context.Context) (identity.PasswordPolicy, error) {
	return identity.PasswordPolicy{}, nil
}
func (o callbackURLTestOptions) WebOption(_ context.Context, name string) (string, error) {
	if name == options.NameSiteURL {
		return o.siteURL, nil
	}
	return "", nil
}

func TestAbsoluteCallbackURLPrefersSiteSettingThenAppURL(t *testing.T) {
	h := &Controller{
		options: callbackURLTestOptions{siteURL: "https://test.dalao.me"},
		appURL:  "http://127.0.0.1:3000",
		appEnv:  "development",
	}

	got, err := h.absoluteCallbackURL(context.Background(), "demo.auth")
	if err != nil || got != "https://test.dalao.me/auth/providers/demo.auth/callback" {
		t.Fatalf("site.url callback=%q err=%v", got, err)
	}

	h.options = callbackURLTestOptions{}
	got, err = h.absoluteCallbackURL(context.Background(), "demo.auth")
	if err != nil || got != "http://127.0.0.1:3000/auth/providers/demo.auth/callback" {
		t.Fatalf("APP_URL fallback callback=%q err=%v", got, err)
	}
}

func TestListAdminIdentityProviderItemsAggregateStates(t *testing.T) {
	store := identity.NewMemoryProviderActivationStore()
	digest := strings.Repeat("a", 64)
	registry := identityregistry.New()
	// provider.ID 必须以 ExtensionID + "." 为前缀；operations 无 schema 时保持空（目录元数据）。
	if _, err := registry.Publish(identityregistry.Publication{
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.github", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 9, RuntimeInstanceID: "rt-github",
		},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "ext.github.identity@1",
			Providers: []identityregistry.Provider{{
				ID: "ext.github.auth", ContractVersion: "ext.github.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "ext.github.identity",
				Label: "GitHub", LabelLocales: map[string]string{"zh-CN": "GitHub", "en-US": "GitHub"},
				Icon: "i-tabler-brand-github",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	configured := true
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(id string) (identityregistry.ProviderContribution, error) {
			return registry.ResolveProvider(id)
		},
		IsProviderConfigured: func(context.Context, string) (bool, error) {
			return configured, nil
		},
	})
	loginOn := true
	if _, err := store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID:         "ext.github.auth",
		OwnerExtensionID:   "ext.github",
		OwnerPackageDigest: digest,
		LoginEnabled:       &loginOn,
		ExpectedRevision:   0,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Controller{
		activationStore:     store,
		externalAuthService: svc,
		providerCatalog:     registry,
		appURL:              "https://forum.example.com",
		appEnv:              "production",
	}

	items, err := h.listAdminIdentityProviderItems(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	item := items[0]
	if item.ID != "ext.github.auth" {
		t.Fatalf("id=%s", item.ID)
	}
	if !item.Discovered || !item.Trusted || !item.Enabled || !item.Configured {
		t.Fatalf("state flags: discovered=%v trusted=%v enabled=%v configured=%v",
			item.Discovered, item.Trusted, item.Enabled, item.Configured)
	}
	if !item.Activated || !item.PubliclyActivated || !item.ArtifactBound {
		t.Fatalf("activation flags: activated=%v public=%v bound=%v",
			item.Activated, item.PubliclyActivated, item.ArtifactBound)
	}
	if item.CallbackPath != "/auth/providers/ext.github.auth/callback" || item.CallbackURL != "https://forum.example.com/auth/providers/ext.github.auth/callback" {
		t.Fatalf("callback path=%q url=%q", item.CallbackPath, item.CallbackURL)
	}
	if item.SettingsPath != "/extensions/ext.github/pages/settings" {
		t.Fatalf("settingsPath=%q", item.SettingsPath)
	}
	if item.OwnerExtensionVersion != "1.0.0" {
		t.Fatalf("version=%q", item.OwnerExtensionVersion)
	}
	if item.Label != "GitHub" || item.Icon != "i-tabler-brand-github" {
		t.Fatalf("presentation label=%q icon=%q", item.Label, item.Icon)
	}
	if item.Probed {
		t.Fatalf("unprobed must not report probed")
	}

	// 未配置 → publiclyActivated false
	configured = false
	items, err = h.listAdminIdentityProviderItems(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Configured || items[0].PubliclyActivated {
		t.Fatalf("unconfigured must not be publicly activated: %#v", items[0])
	}

	// artifact 漂移：live Registry 换新 digest，激活行仍绑定旧 digest → bound=false。
	configured = true
	registry2 := identityregistry.New()
	newDigest := strings.Repeat("c", 64)
	if _, err := registry2.Publish(identityregistry.Publication{
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.github", ExtensionVersion: "1.0.1",
			PackageDigest: newDigest, VersionID: 10, RuntimeInstanceID: "rt-github-2",
		},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "ext.github.identity@1",
			Providers: []identityregistry.Provider{{
				ID: "ext.github.auth", ContractVersion: "ext.github.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "ext.github.identity",
				Label: "GitHub", Icon: "i-tabler-brand-github",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.providerCatalog = registry2
	items, err = h.listAdminIdentityProviderItems(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if !items[0].Activated || items[0].ArtifactBound || items[0].PubliclyActivated {
		t.Fatalf("artifact drift: activated=%v bound=%v public=%v digest=%s",
			items[0].Activated, items[0].ArtifactBound, items[0].PubliclyActivated, items[0].OwnerPackageDigest)
	}
}

func TestT8B_AdminDirectoryFromPackageCatalogBeforeEnable(t *testing.T) {
	// 包目录 discovered，live Registry 空 → 仍可检查，enabled=false。
	h := &Controller{
		packageCatalog: identity.StaticAuthProviderPackageCatalog{
			Items: []identity.AuthProviderPackageCandidate{{
				ProviderID:            "sforum.auth-github.auth",
				Kind:                  "auth",
				ContractVersion:       "sforum.auth-github.auth@1",
				OwnerExtensionID:      "sforum.auth-github",
				OwnerExtensionVersion: "1.0.0",
				OwnerPackageDigest:    strings.Repeat("b", 64),
				Operations:            []string{"login.start", "provider.probe"},
				Label:                 "GitHub",
				LabelLocales:          map[string]string{"zh-CN": "GitHub", "en-US": "GitHub"},
				Icon:                  "i-tabler-brand-github",
				Trusted:               false,
				Enabled:               false,
				Status:                "installed",
				Source:                "builtin",
			}},
		},
		appURL: "https://forum.example.com",
		appEnv: "production",
	}
	items, err := h.listAdminIdentityProviderItems(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("pre-enable discovered-only must be filtered out (only enabled/live shown): items=%d", len(items))
	}
	// T8B 变更：仅显示已启用并存在的提供商；pre-enable discovered 不再展示在管理员列表中。
}

func TestT8B_AdminDirectorySecondFakeProviderAndDisabled(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	registry := identityregistry.New()
	// 仅 provider A live+enabled；B 仅在包目录（disabled）。
	if _, err := registry.Publish(identityregistry.Publication{
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.alpha", ExtensionVersion: "1.0.0",
			PackageDigest: digestA, VersionID: 1, RuntimeInstanceID: "rt-a",
		},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "ext.alpha.identity@1",
			Providers: []identityregistry.Provider{{
				ID: "ext.alpha.auth", ContractVersion: "ext.alpha.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "ext.alpha.identity",
				Label: "Alpha", Icon: "i-lucide-key-round",
				Priority: 20,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := identity.NewMemoryProviderActivationStore()
	loginOn := true
	if _, err := store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "ext.alpha.auth", OwnerExtensionID: "ext.alpha", OwnerPackageDigest: digestA,
		LoginEnabled: &loginOn, ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Controller{
		activationStore: store,
		providerCatalog: registry,
		packageCatalog: identity.StaticAuthProviderPackageCatalog{
			Items: []identity.AuthProviderPackageCandidate{
				{
					ProviderID: "ext.alpha.auth", Kind: "auth", OwnerExtensionID: "ext.alpha",
					OwnerPackageDigest: digestA, Label: "Alpha", Icon: "i-lucide-key-round",
					Trusted: true, Enabled: true, Status: "enabled",
				},
				{
					ProviderID: "ext.beta.auth", Kind: "auth", OwnerExtensionID: "ext.beta",
					OwnerExtensionVersion: "2.0.0", OwnerPackageDigest: digestB,
					Label: "Beta Social", LabelLocales: map[string]string{"en-US": "Beta Social", "zh-CN": "Beta 社交"},
					Icon: "i-lucide-shield", Trusted: true, Enabled: false, Status: "disabled",
					Operations: []string{"login.start", "provider.probe"},
				},
			},
		},
		externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{
			ActivationStore: store,
			IsProviderConfigured: func(_ context.Context, id string) (bool, error) {
				return id == "ext.alpha.auth", nil
			},
		}),
		appURL: "https://forum.example.com",
		appEnv: "development",
	}

	items, err := h.listAdminIdentityProviderItems(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 enabled provider, got %d", len(items))
	}
	alpha := items[0]
	if alpha.ID != "ext.alpha.auth" || !alpha.Enabled || !alpha.Discovered || alpha.Label != "Alpha" {
		t.Fatalf("alpha: %#v", alpha)
	}
	// T8B 变更：仅显示已启用并存在的提供商；disabled beta 不再在管理员列表中展示。
}

func TestT8B_AdminDirectorySafeModeAndResetFlags(t *testing.T) {
	digest := strings.Repeat("d", 64)
	// Safe Mode 下 live Registry 剥离第三方；包目录仍可检查 disabled/drifted 意图。
	registry := identityregistry.New()
	if _, err := registry.ReplaceAll(nil, true); err != nil {
		t.Fatal(err)
	}
	store := identity.NewMemoryProviderActivationStore()
	loginOn := true
	if _, err := store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "ext.safe.auth", OwnerExtensionID: "ext.safe", OwnerPackageDigest: digest,
		LoginEnabled: &loginOn, ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if err := store.RecordProbe(context.Background(), identity.ProviderActivationProbeResult{
		ProviderID: "ext.safe.auth", OK: true, Reason: "demo.probe_ok", At: at,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Controller{
		activationStore: store,
		providerCatalog: registry,
		packageCatalog: identity.StaticAuthProviderPackageCatalog{
			Items: []identity.AuthProviderPackageCandidate{{
				ProviderID: "ext.safe.auth", Kind: "auth", OwnerExtensionID: "ext.safe",
				OwnerPackageDigest: digest, Label: "Safe", Icon: "i-lucide-shield-check",
				Trusted: true, Enabled: true, Status: "enabled",
			}},
		},
		externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{
			ActivationStore: store,
			IsProviderConfigured: func(context.Context, string) (bool, error) {
				return true, nil
			},
		}),
		appURL: "https://forum.example.com",
		appEnv: "production",
	}
	items, err := h.listAdminIdentityProviderItems(context.Background(), "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("safe mode + empty live Registry → list empty (only enabled/live shown): items=%d", len(items))
	}
	// T8B 变更：仅显示已启用并存在的提供商；Safe Mode 下 live 剥离第三方，包目录-only 也不再展示在管理员列表中。
	// 激活/探针/重置仍通过 store 独立操作（无需列表项）。
}
