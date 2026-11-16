package extensionmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestManifestV3CompleteApplicationPlugin(t *testing.T) {
	manifest := completeV3Manifest()
	if err := Validate(manifest); err != nil {
		t.Fatalf("complete V3 manifest should validate: %v", err)
	}

	manifest.Routes[0].Methods = []string{"PROPFIND"}
	manifest.Routes[0].Guard = GuardCorePublic
	if err := Validate(manifest); err != nil {
		t.Fatalf("V3 must support arbitrary declared methods and public writes: %v", err)
	}
}

func TestManifestV3TargetRoutesDefaultToInheritedGuard(t *testing.T) {
	targetManifest := func(action string) Manifest {
		manifest := completeV3Manifest()
		route := &manifest.Routes[0]
		route.Action = action
		route.TargetID = "core.route.demo.target"
		route.Guard = ""
		route.Access = ""
		route.Destination = ""
		switch action {
		case RouteActionAlias, RouteActionRewrite:
			route.Handler = ""
			route.RequestSchema = ""
			route.ResponseSchema = ""
		case RouteActionBefore, RouteActionAfter:
			route.RequestSchema = ""
			route.ResponseSchema = ""
		}
		return manifest
	}

	for _, action := range []string{
		RouteActionAlias, RouteActionRewrite, RouteActionBefore, RouteActionAfter,
		RouteActionFilter, RouteActionWrap, RouteActionReplace,
	} {
		t.Run(action, func(t *testing.T) {
			manifest := targetManifest(action)
			normalized := Normalize(manifest)
			if normalized.Routes[0].Guard != GuardCoreInherit {
				t.Fatalf("normalized guard = %q", normalized.Routes[0].Guard)
			}
			if err := Validate(manifest); err != nil {
				t.Fatalf("target route should inherit its guard: %v", err)
			}
		})
	}

	t.Run("explicit demo.v3.guard.owner", func(t *testing.T) {
		manifest := targetManifest(RouteActionReplace)
		manifest.Routes[0].Guard = "demo.v3.guard.owner"
		if normalized := Normalize(manifest).Routes[0].Guard; normalized != "demo.v3.guard.owner" {
			t.Fatalf("explicit guard normalized to %q", normalized)
		}
		if err := Validate(manifest); err != nil {
			t.Fatalf("explicit guard should remain valid: %v", err)
		}
	})

	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 不再作为路由守卫接受。
	// 规范化不得把它洗成别的守卫，校验必须拒绝并指向包内 kind=raw_request 守卫作为替代。
	t.Run("explicit "+GuardCoreRaw+" is refused", func(t *testing.T) {
		manifest := targetManifest(RouteActionReplace)
		manifest.Routes[0].Guard = GuardCoreRaw
		if normalized := Normalize(manifest).Routes[0].Guard; normalized != GuardCoreRaw {
			t.Fatalf("explicit guard normalized to %q", normalized)
		}
		assertCoreRawRouteGuardRefused(t, manifest)
	})
}

// assertCoreRawRouteGuardRefused 固定 R1 之后的拒绝契约：core.guard.raw_request 不再是
// 路由守卫，且拒绝信息必须写明替代方案（包内 kind=raw_request 守卫），否则作者只会看到
// 「id 写错了」而无从修好。
func assertCoreRawRouteGuardRefused(t *testing.T, manifest Manifest) {
	t.Helper()
	err := Validate(manifest)
	if err == nil {
		t.Fatalf("%s must no longer be accepted as a route guard", GuardCoreRaw)
	}
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("refusal must wrap ErrInvalidManifest: %v", err)
	}
	if !strings.Contains(err.Error(), GuardCoreRaw) || !strings.Contains(err.Error(), "kind="+GuardKindRawRequest) {
		t.Fatalf("refusal must name the %s replacement: %v", GuardKindRawRequest, err)
	}
}

func TestManifestV3NoTargetRouteRequiresExplicitGuardOrAccess(t *testing.T) {
	noTargetManifest := func(action string) Manifest {
		manifest := completeV3Manifest()
		route := &manifest.Routes[0]
		route.Action = action
		route.TargetID = ""
		route.Guard = GuardCoreLogin
		route.Access = ""
		route.Destination = ""
		switch action {
		case RouteActionRedirect:
			route.Destination = "/api/v3/demo-new"
			route.Handler = ""
			route.RequestSchema = ""
			route.ResponseSchema = ""
		case RouteActionGlobalMiddleware:
			route.Path = ""
			route.Methods = nil
		}
		return manifest
	}

	for _, action := range []string{RouteActionAdd, RouteActionRedirect, RouteActionGlobalMiddleware} {
		t.Run(action, func(t *testing.T) {
			manifest := noTargetManifest(action)
			if err := Validate(manifest); err != nil {
				t.Fatalf("explicit guard baseline should validate: %v", err)
			}
			manifest.Routes[0].Guard = ""
			if guard := Normalize(manifest).Routes[0].Guard; guard != "" {
				t.Fatalf("no-target route silently acquired guard %q", guard)
			}
			if err := Validate(manifest); err == nil {
				t.Fatal("no-target route without guard/access must fail closed")
			}
		})
	}

	for _, test := range []struct {
		access     string
		permission string
		guard      string
	}{
		{access: RouteAccessPublic, guard: GuardCorePublic},
		{access: RouteAccessLogin, guard: GuardCoreLogin},
		{access: RouteAccessPermission, permission: "demo.v3.manage", guard: GuardCorePermission},
	} {
		t.Run("access "+test.access, func(t *testing.T) {
			manifest := noTargetManifest(RouteActionAdd)
			manifest.Routes[0].Guard = ""
			manifest.Routes[0].Access = test.access
			manifest.Routes[0].Permission = test.permission
			if guard := Normalize(manifest).Routes[0].Guard; guard != test.guard {
				t.Fatalf("access guard = %q, want %q", guard, test.guard)
			}
			if err := Validate(manifest); err != nil {
				t.Fatalf("explicit access should validate: %v", err)
			}
		})
	}

	invalid := noTargetManifest(RouteActionAdd)
	invalid.Routes[0].Guard = ""
	invalid.Routes[0].Access = "anonymous"
	if err := Validate(invalid); err == nil {
		t.Fatal("unknown access must not supply an implicit guard")
	}
	conflict := noTargetManifest(RouteActionAdd)
	conflict.Routes[0].Guard = GuardCorePublic
	conflict.Routes[0].Access = RouteAccessLogin
	if err := Validate(conflict); err == nil {
		t.Fatal("conflicting explicit guard and access must be rejected")
	}
}

func TestManifestV3JobPolicyDefaultsAreDeterministic(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.Jobs[0].MaxAttempts = 0
	manifest.Jobs[0].RetryDelaySeconds = 0
	manifest.Jobs[0].ConcurrencyLimit = 0

	normalized := Normalize(manifest)
	job := normalized.Jobs[0]
	if job.MaxAttempts != PluginJobDefaultBoundedAttempts ||
		job.RetryDelaySeconds != PluginJobDefaultRetryDelaySeconds ||
		job.ConcurrencyLimit != PluginJobDefaultConcurrencyLimit {
		t.Fatalf("normalized job policy = %#v", job)
	}
	if err := Validate(normalized); err != nil {
		t.Fatalf("defaulted job policy should validate: %v", err)
	}
}

func TestManifestV3CommandDefaultsAreDeterministic(t *testing.T) {
	manifest := completeV3Manifest()
	manifest.Commands[0].TimeoutMS = 0
	normalized := Normalize(manifest)
	if normalized.Commands[0].TimeoutMS != PluginCommandMaximumTimeoutMS || normalized.Commands[0].RecoverySafe {
		t.Fatalf("normalized command = %#v", normalized.Commands[0])
	}
	if err := Validate(normalized); err != nil {
		t.Fatalf("defaulted command should validate: %v", err)
	}
}

func TestManifestV3ThemePresentationContract(t *testing.T) {
	digest := strings.Repeat("b", 64)
	manifest := versionedTestManifest(ManifestVersionV3)
	manifest.ID = "demo.theme"
	manifest.Type = TypeTheme
	manifest.PackageFiles = []ManifestPackageFile{
		{ID: "demo.theme.file.template", Kind: "template", Path: "templates/home.html", Digest: digest},
		{ID: "demo.theme.file.frontend", Kind: "frontend", Path: "frontend/card.mjs", Digest: digest},
		{ID: "demo.theme.component.home.props", Kind: "schema", Path: "schemas/home-props.json", Digest: digest, Version: "1"},
	}
	manifest.Templates = []ManifestTemplate{{
		ID: "demo.theme.template.home", ContractVersion: "demo.theme.template.home@1",
		Action: "add", Path: "templates/home.html", Digest: digest,
		ViewModelSchema: "sforum.page.home@1", ThemeOverrideKey: "demo.theme.home",
	}}
	// 主题可以声明组件，但 props/result schema 必须是同包精确声明的本地 schema。
	// ssrTemplate 组件要求 PropsSchema 与模板 ViewModelSchema 逐字一致，而主题模板
	// 绑定的是宿主核心页契约（sforum.page.home@1），组件注册表又拒绝没有同包声明的
	// 引用，所以主题的 L1 面是 theme.json.pages[].template + templates[]；
	// 组件面只承载自带 schema 的 L2 组件。
	manifest.Components = []ManifestComponent{{
		ID: "demo.theme.component.home", ContractVersion: "demo.theme.component.home@1",
		Action: ComponentActionAdd, L2Component: "demo.theme.file.frontend",
		PropsSchema: "demo.theme.component.home.props@1",
	}}
	if err := Validate(manifest); err != nil {
		t.Fatalf("V3 presentation-only theme should validate: %v", err)
	}
	manifest.Commands = []ManifestCommand{{ID: "demo.theme.command.evil"}}
	if err := Validate(manifest); err == nil {
		t.Fatal("theme business/runtime declarations must be rejected")
	}
}

func TestManifestV3RejectsUnsafeContracts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Manifest)
	}{
		{name: "unknown guard", change: func(manifest *Manifest) { manifest.Routes[0].Guard = "missing.guard" }},
		{name: "inherited guard without target", change: func(manifest *Manifest) { manifest.Routes[0].Guard = GuardCoreInherit }},
		{name: "duplicate id", change: func(manifest *Manifest) {
			manifest.Components = append(manifest.Components, manifest.Components[0])
		}},
		{name: "missing contract", change: func(manifest *Manifest) { manifest.Commands[0].ContractVersion = "" }},
		{name: "reserved health path", change: func(manifest *Manifest) { manifest.Routes[0].Path = "/health" }},
		{name: "reserved notification stream", change: func(manifest *Manifest) { manifest.Routes[0].Path = "/api/v1/notifications/stream" }},
		{name: "reserved comment stream", change: func(manifest *Manifest) { manifest.Routes[0].Path = "/api/v1/topics/:topicID/comments/stream" }},
		{name: "missing digest", change: func(manifest *Manifest) { manifest.Assets[0].Digest = "" }},
		{name: "unsafe package path", change: func(manifest *Manifest) { manifest.OpenAPI[0].Path = "../openapi.yaml" }},
		{name: "plugin self grants permission", change: func(manifest *Manifest) {
			manifest.PermissionDefinitions[0].AssignmentPolicy = "plugin"
		}},
		{name: "job attempts exceed host bound", change: func(manifest *Manifest) { manifest.Jobs[0].MaxAttempts = 26 }},
		{name: "job concurrency exceeds host bound", change: func(manifest *Manifest) { manifest.Jobs[0].ConcurrencyLimit = 17 }},
		{name: "fixed delay with exponential retry", change: func(manifest *Manifest) {
			manifest.Jobs[0].RetryPolicy = "exponential"
			manifest.Jobs[0].RetryDelaySeconds = 30
		}},
		{name: "command timeout exceeds host bound", change: func(manifest *Manifest) { manifest.Commands[0].TimeoutMS = 5001 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := completeV3Manifest()
			test.change(&manifest)
			if err := Validate(manifest); err == nil {
				t.Fatal("invalid V3 declaration must be rejected")
			}
		})
	}
}

func TestLegacyManifestRejectsV3Declarations(t *testing.T) {
	manifest := versionedTestManifest(0)
	manifest.Dependencies = []ManifestDependency{{ID: "other.plugin", Version: "^1.0.0", Kind: "required"}}
	if err := Validate(manifest); err == nil {
		t.Fatal("V3 fields must not be accepted through an implicit V1 upgrade")
	}
}

func TestManifestV3LoadsEveryShardedDeclaration(t *testing.T) {
	manifest := completeExecutableQueryManifest()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	fields := []string{
		"guards", "schedules", "components", "templates", "assets", "content", "editor", "entities",
		"database", "cache", "seo", "services", "commands", "adminSurfaces", "queries", "queryResultFilters",
		"identity", "permissionDefinitions", "media", "navigation", "regions",
		"dependencies", "lifecycle", "openapi", "packageFiles",
	}
	files := FileMapFS{}
	includes := map[string]string{}
	for _, field := range fields {
		// omitempty leaves optional empty slices/objects out of the root JSON;
		// only shard fields that are actually present.
		body, ok := root[field]
		if !ok || len(body) == 0 {
			continue
		}
		shard := "manifest/" + field + ".json"
		files[shard] = body
		delete(root, field)
		includes[field] = shard
	}
	for _, file := range manifest.PackageFiles {
		files[file.Path] = v3FixtureBody()
	}
	root["includes"], err = json.Marshal(includes)
	if err != nil {
		t.Fatal(err)
	}
	files[ManifestFileName], err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPackageFS(files)
	if err != nil {
		t.Fatalf("load V3 shards: %v", err)
	}
	if loaded.Database == nil || loaded.Identity == nil || loaded.Lifecycle == nil || len(loaded.Routes) != 1 || len(loaded.Components) != 1 || len(loaded.Dependencies) != 2 {
		t.Fatalf("incomplete sharded manifest: %#v", loaded)
	}
}

func TestManifestV3PackageFileDigestsAreExact(t *testing.T) {
	manifest := completeV3Manifest()
	files := FileMapFS{}
	for _, file := range manifest.PackageFiles {
		files[file.Path] = v3FixtureBody()
	}
	if err := ValidatePackageFiles(manifest, files); err != nil {
		t.Fatalf("exact package files should validate: %v", err)
	}
	files[manifest.PackageFiles[0].Path] = []byte("changed")
	if err := ValidatePackageFiles(manifest, files); err == nil {
		t.Fatal("changed package bytes must be rejected")
	}
	delete(files, manifest.PackageFiles[0].Path)
	if err := ValidatePackageFiles(manifest, files); err == nil {
		t.Fatal("missing package bytes must be rejected")
	}
}

func completeV3Manifest() Manifest {
	digest := v3FixtureDigest()
	manifest := versionedTestManifest(ManifestVersionV3)
	manifest.ID = "demo.v3"
	manifest.Permissions = []string{"demo.v3.manage"}
	manifest.Backend = ManifestBackend{
		Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
		Digest: digest, HostAPIVersion: "sforum.host-api@2",
	}
	manifest.Migrations = []ManifestMigration{{
		ID: "demo.v3.migration.initial", ContractVersion: "demo.v3.migration.initial@1",
		Path: "migrations/001.sql", Digest: digest, Transaction: "required",
	}}
	manifest.Guards = []ManifestGuard{{
		ID: "demo.v3.guard.owner", ContractVersion: "demo.v3.guard.owner@1",
		Kind: "custom", Entry: "backend/guard", Digest: digest, Permissions: []string{"demo.v3.manage"},
	}}
	manifest.Routes = []ManifestRoute{{
		ID: "demo.v3.route.write", ContractVersion: "demo.v3.route.write@1",
		Action: RouteActionAdd, Path: "/api/v3/demo", Methods: []string{"POST"},
		Guard: "demo.v3.guard.owner", Mode: RouteModeHTTP, Handler: "route.write",
		RequestSchema: "demo.v3.route.write.request@1", ResponseSchema: "demo.v3.route.write.response@1",
		Fallback: "closed", TimeoutMS: 1000,
	}}
	manifest.Hooks = []ManifestHook{{
		ID: "demo.v3.hook.listener", ContractVersion: "demo.v3.hook.listener@1",
		Name: "demo.v3.hook.changed", Kind: "action", Handler: "hook.changed",
		InputSchema: "demo.v3.hook.changed.input@1", ResultSchema: "demo.v3.hook.changed.result@1",
	}}
	manifest.Events = []ManifestEvent{{
		ID: "demo.v3.event.listener", ContractVersion: "demo.v3.event.listener@1",
		Name: "demo.v3.event.changed", Kind: "observe", Handler: "event.changed",
		InputSchema: "demo.v3.event.changed.input@1", TimeoutMS: 1000,
	}}
	manifest.Jobs = []ManifestJob{{
		ID: "demo.v3.job.refresh", ContractVersion: "demo.v3.job.refresh@1",
		Name: "demo.v3.refresh", Handler: "job.refresh", PayloadSchema: "demo.v3.job.refresh.payload@1", RetryPolicy: "bounded",
		MaxAttempts: 5, RetryDelaySeconds: 30, ConcurrencyLimit: 2,
	}}
	manifest.Schedules = []ManifestSchedule{{
		ID: "demo.v3.schedule.refresh", ContractVersion: "demo.v3.schedule.refresh@1",
		JobID: "demo.v3.job.refresh", Cron: "0 * * * *", Timezone: "UTC",
	}}
	manifest.Providers = []ManifestProvider{{
		ID: "demo.v3.provider.search", ContractVersion: "demo.v3.provider.search@1",
		Slot: "demo.v3.provider.slot", Label: "Demo", Handler: "provider.search", TimeoutMS: 1000,
		RequestSchema: "demo.v3.provider.request@1", ResponseSchema: "demo.v3.provider.response@1",
	}}
	manifest.PackageFiles = []ManifestPackageFile{
		{ID: "demo.v3.file.backend", Kind: "executable", Path: "backend/plugin", Digest: digest},
		{ID: "demo.v3.file.guard", Kind: "executable", Path: "backend/guard", Digest: digest},
		{ID: "demo.v3.file.migration", Kind: "migration", Path: "migrations/001.sql", Digest: digest},
		{ID: "demo.v3.file.template", Kind: "template", Path: "templates/card.html", Digest: digest},
		{ID: "demo.v3.file.asset", Kind: "asset", Path: "frontend/card.css", Digest: digest},
		{ID: "demo.v3.file.frontend", Kind: "frontend", Path: "frontend/card.mjs", Digest: digest},
		{ID: "demo.v3.file.openapi", Kind: "openapi", Path: "openapi/routes.yaml", Digest: digest},
		{ID: "demo.v3.file.locale", Kind: "locale", Path: "locales/zh-CN.json", Digest: digest, Locale: "zh-CN"},
		{ID: "demo.v3.file.database.query", Kind: "database_operation", Path: "database/items-query.sql", Digest: digest},
		{ID: "demo.v3.file.database.execute", Kind: "database_operation", Path: "database/items-insert.sql", Digest: digest},
		{ID: "demo.v3.provider.request", Kind: "schema", Path: "schemas/provider-request.json", Digest: digest, Version: "1"},
		{ID: "demo.v3.provider.response", Kind: "schema", Path: "schemas/provider-response.json", Digest: digest, Version: "1"},
		// 组件 props/result 与 provider 采用同一条精确声明规则：引用必须有同包 schema 声明，
		// 否则 CLI 会放行一个运行时组件注册表必然拒绝的包。
		{ID: "demo.v3.component.card.props", Kind: "schema", Path: "schemas/component-props.json", Digest: digest, Version: "1"},
		{ID: "demo.v3.component.card.result", Kind: "schema", Path: "schemas/component-result.json", Digest: digest, Version: "1"},
	}
	manifest.Templates = []ManifestTemplate{{
		ID: "demo.v3.template.card", ContractVersion: "demo.v3.template.card@1",
		Action: "add", Path: "templates/card.html", Digest: digest,
		// 与绑定组件的 PropsSchema 精确同一引用；组件 SSR 准入禁止隐式 schema 拓宽。
		ViewModelSchema: "demo.v3.component.card.props@1", ThemeOverrideKey: "demo.v3.card",
	}}
	manifest.Assets = []ManifestAsset{{
		Handle: "demo.v3.asset.card", ContractVersion: "demo.v3.asset.card@1",
		Type: "style", Path: "frontend/card.css", Digest: digest, Loading: "defer",
	}}
	manifest.Components = []ManifestComponent{{
		ID: "demo.v3.component.card", ContractVersion: "demo.v3.component.card@1",
		Action: ComponentActionAdd, SSRTemplate: "demo.v3.template.card", L2Component: "demo.v3.file.frontend",
		PropsSchema: "demo.v3.component.card.props@1", ResultSchema: "demo.v3.component.card.result@1", ThemeOverrideKey: "demo.v3.card",
	}}
	manifest.Content = []ManifestContent{{
		ID: "demo.v3.content.block", ContractVersion: "demo.v3.content.block@1",
		Kind: "block", Handler: "content.block", Schema: "demo.v3.content.block.schema@1", Renderer: "demo.v3.template.card",
	}}
	manifest.Database = &ManifestDatabase{
		ContractVersion: "demo.v3.database@1", Authority: "own_schema", Schema: "demo_v3", Role: "demo_v3",
		Backup:    ManifestBackupPolicy{Required: true, Strategy: "pg_dump"},
		Retention: ManifestRetention{OnDisable: "retain", OnUninstall: "export", Days: 30},
		Operations: []ManifestDatabaseOperation{
			{
				ID: "demo.v3.database.items.query", StatementVersion: "1", Kind: "query",
				Path: "database/items-query.sql", Digest: digest,
				Parameters: []ManifestDatabaseParameter{{
					Schema: "demo.v3.database.item-id@1", Field: "id", Kind: "int64", MaxBytes: 8,
				}},
				ResultSchema: "demo.v3.database.items.result@1",
				Columns:      []ManifestDatabaseColumn{{Name: "id"}, {Name: "name", Nullable: true}},
				MaxRows:      100,
				TimeoutMS:    3000,
			},
			{
				ID: "demo.v3.database.items.insert", StatementVersion: "1", Kind: "execute",
				Path: "database/items-insert.sql", Digest: digest,
				Parameters: []ManifestDatabaseParameter{{
					Schema: "demo.v3.database.item-name@1", Field: "name", Kind: "string", MaxBytes: 1024,
				}},
				ResultSchema:    "demo.v3.database.items.result@1",
				Columns:         []ManifestDatabaseColumn{{Name: "id"}, {Name: "name"}},
				MaxAffectedRows: 1,
				TimeoutMS:       3000,
			},
		},
	}
	manifest.Cache = []ManifestCache{{
		ID: "demo.v3.cache.results", ContractVersion: "demo.v3.cache.results@1",
		Namespace: "demo.v3.results", Policy: "actor", Tags: []string{"demo.v3.cache.tag"}, Invalidators: []string{"demo.v3.cache.invalidate"},
	}}
	manifest.SEO = []ManifestSEO{{
		ID: "demo.v3.seo.topic-title", ContractVersion: "demo.v3.seo.topic-title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter", Handler: "demo.v3.seo.topic-title",
		Priority: 100, FailurePolicy: "fallback", TimeoutMS: 500,
	}}
	manifest.Services = []ManifestService{{
		ID: "demo.v3.service.lookup", ContractVersion: "demo.v3.service.lookup@1", Action: "add",
		Handler: "service.lookup", RequestSchema: "demo.v3.service.lookup.request@1", ResponseSchema: "demo.v3.service.lookup.response@1",
	}}
	manifest.Commands = []ManifestCommand{{
		ID: "demo.v3.command.write", ContractVersion: "demo.v3.command.write@1", Handler: "command.write",
		Permission: "demo.v3.manage", InputSchema: "demo.v3.command.write.input@1", ResultSchema: "demo.v3.command.write.result@1",
		Description: "Write one demo record", RecoverySafe: false, TimeoutMS: 3000,
	}}
	manifest.AdminSurfaces = []ManifestAdminSurface{{
		ID: "demo.v3.admin.notice", ContractVersion: "demo.v3.admin.notice@1", Kind: "notice", Action: "add",
		PlacementID: "core.component.page.admin", PlacementContractVersion: "sforum.component.page.admin@1",
		Label: "Demo notice", Handler: "admin.notice", PropsSchema: "demo.v3.admin.notice.props@1",
		ResultSchema: "demo.v3.admin.notice.result@1", Operation: AdminSurfaceOperationQuery, Permission: "demo.v3.manage",
	}}
	manifest.Queries = []ManifestQuery{{
		ID: "demo.v3.query.items", ContractVersion: "demo.v3.query.items@1", Entity: "demo.v3.item",
		PlanVersion: "demo.v3.query.items.plan@1", Fields: []string{"id"}, Pagination: "cursor",
		ResultSchema: "demo.v3.query.items.result@1", PermissionPolicy: "demo.v3.manage", CacheTags: []string{"demo.v3.items"},
	}}
	manifest.PermissionDefinitions = []ManifestPermissionDefinition{{
		Key: "demo.v3.manage", ContractVersion: "demo.v3.permission.manage@1", Label: LocalizedText{Default: "Manage demo"},
		Description: LocalizedText{Default: "Manage the demo plugin."}, RecommendedRoles: []string{"administrator"}, AssignmentPolicy: "host",
	}}
	manifest.Identity = &ManifestIdentity{
		ContractVersion: "demo.v3.identity@1", SessionPolicy: "core.session.default",
		UserFields: []ManifestIdentityUserField{{
			ID: "demo.v3.identity.field", ContractVersion: "demo.v3.identity.field@1", Type: "string",
			Schema: "demo.v3.identity.field.schema@1", ReadPermission: "demo.v3.manage", WritePermission: "demo.v3.manage",
		}},
		Providers: []ManifestIdentityProvider{{
			ID: "demo.v3.identity.risk", ContractVersion: "demo.v3.identity.risk@1", Kind: "risk", Handler: "identity.risk",
		}},
	}
	manifest.Media = []ManifestMediaPipeline{{
		ID: "demo.v3.media.image", ContractVersion: "demo.v3.media.image@1", Action: "add",
		MIMEs: []string{"image/png"}, Handler: "media.image", Permission: "demo.v3.manage",
		Transforms: []ManifestMediaTransform{{ID: "thumbnail", Variant: "thumb", Width: 320, Height: 240}},
	}}
	manifest.Navigation = []ManifestNavigation{{
		ID: "demo.v3.navigation.item", ContractVersion: "demo.v3.navigation.item@1",
		Kind: "item", Action: "add", Label: "Demo", Href: "/demo", Order: 10,
	}}
	manifest.Regions = []ManifestRegion{{
		ID: "demo.v3.region.sidebar", ContractVersion: "demo.v3.region.sidebar@1",
		Action: "add", Kind: "sidebar", Label: "Demo sidebar", Multiple: true,
	}}
	manifest.Dependencies = []ManifestDependency{
		{ID: "other.plugin", Version: "^1.0.0", Kind: "required"},
		{Capability: "demo.v3.capability", Version: "1.0.0", Kind: "provides"},
	}
	manifest.Lifecycle = &ManifestLifecycle{
		ContractVersion: "demo.v3.lifecycle@1",
		Install: &ManifestLifecycleOperation{
			Plan: "lifecycle.install.plan", Execute: "lifecycle.install.execute",
			ProgressSchema: "demo.v3.lifecycle.progress@1", CheckpointSchema: "demo.v3.lifecycle.checkpoint@1",
		},
	}
	manifest.OpenAPI = []ManifestOpenAPIFragment{{
		ID: "demo.v3.openapi.routes", ContractVersion: "demo.v3.openapi.routes@1",
		Path: "openapi/routes.yaml", Digest: digest, Namespace: "demo.v3.api",
	}}
	return manifest
}

func v3FixtureBody() []byte {
	return []byte("SForum Manifest V3 fixture\n")
}

func v3FixtureDigest() string {
	digest := sha256.Sum256(v3FixtureBody())
	return hex.EncodeToString(digest[:])
}

// Host 保留路由（health / ready / notification stream）的 TargetID 有两个入口：
// 普通路由（add/replace/…）与 global middleware 的精确作用域声明。global 分支此前
// 没有任何用例——一个只删掉 global 分支判定的改动可以全绿通过。
func TestManifestV3RejectsReservedRouteTargets(t *testing.T) {
	reserved := []string{
		"core.route.system.health",
		"core.route.system.ready",
		"core.route.notifications.stream",
	}
	globalRoute := func(target string) Manifest {
		manifest := completeV3Manifest()
		route := &manifest.Routes[0]
		route.Action = RouteActionGlobalMiddleware
		route.Path = ""
		route.Methods = nil
		route.TargetID = target
		return manifest
	}
	t.Run("global middleware", func(t *testing.T) {
		// 正例先钉住：空 target（只覆盖自己的 terminal）与普通 target 必须通过，
		// 否则下面的拒绝可能是别的规则造成的，变异就测不出 global 分支。
		for _, target := range []string{"", "demo.v3.route.other"} {
			if err := Validate(globalRoute(target)); err != nil {
				t.Fatalf("global middleware target %q should validate: %v", target, err)
			}
		}
		for _, target := range reserved {
			if err := Validate(globalRoute(target)); err == nil {
				t.Fatalf("global middleware must not scope itself to reserved route %q", target)
			}
		}
	})
	t.Run("add route", func(t *testing.T) {
		allowed := completeV3Manifest()
		allowed.Routes[0].TargetID = "demo.v3.route.other"
		if err := Validate(allowed); err != nil {
			t.Fatalf("non-reserved target on an add route should validate: %v", err)
		}
		for _, target := range reserved {
			manifest := completeV3Manifest()
			manifest.Routes[0].TargetID = target
			if err := Validate(manifest); err == nil {
				t.Fatalf("add route must not target reserved route %q", target)
			}
		}
	})
}
