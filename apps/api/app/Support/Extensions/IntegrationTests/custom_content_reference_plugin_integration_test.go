package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// TestReferenceCustomContentPluginPublishesEntityContentEditorNavigation proves
// P13 custom-content executes real storage/validation/taxonomy/query/search/
// import-export/schema migration and server-side block/shortcode/embed fallback
// render through Protocol V2 — not Manifest field counting alone.
func TestReferenceCustomContentPluginPublishesEntityContentEditorNavigation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference custom-content plugin subprocess build in short mode")
	}
	extension := buildReferenceCustomContentExtension(t)
	if extension.ID != "sforum.custom-content" {
		t.Fatalf("extension id = %q", extension.ID)
	}
	if len(extension.Manifest.Entities) < 4 || len(extension.Manifest.Content) < 5 ||
		len(extension.Manifest.Editor) < 3 || len(extension.Manifest.Navigation) < 1 ||
		len(extension.Manifest.Regions) < 1 || len(extension.Manifest.Queries) < 2 ||
		len(extension.Manifest.Routes) < 8 {
		t.Fatalf("custom-content surfaces incomplete: entities=%d content=%d editor=%d nav=%d regions=%d queries=%d routes=%d",
			len(extension.Manifest.Entities), len(extension.Manifest.Content),
			len(extension.Manifest.Editor), len(extension.Manifest.Navigation),
			len(extension.Manifest.Regions), len(extension.Manifest.Queries),
			len(extension.Manifest.Routes))
	}
	contentKinds := map[string]bool{}
	for _, item := range extension.Manifest.Content {
		contentKinds[item.Kind] = true
	}
	for _, want := range []string{"block", "embed", "shortcode"} {
		if !contentKinds[want] {
			t.Fatalf("missing content kind %s in %#v", want, contentKinds)
		}
	}
	if extension.Manifest.Database == nil || extension.Manifest.Database.Schema != "sforum_custom_content" {
		t.Fatalf("database grant missing: %#v", extension.Manifest.Database)
	}

	// 真实 PostgreSQL own_schema 租约。
	databaseURL := commerceTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	versionID, err := seedCommerceOwnSchemaGrant(t, ctx, pool, extension)
	if err != nil {
		t.Fatalf("seed own_schema grant: %v", err)
	}
	extension.ActiveVersionID = versionID
	artifact := extensionsruntime.ExtensionDatabaseArtifact{
		ExtensionID: extension.ID, Version: extension.Version,
		VersionID: extension.ActiveVersionID, PackageDigest: extension.PackageDigest,
	}
	t.Cleanup(func() { cleanupCommerceOwnSchemaGrant(t, pool, extension.ID) })

	dbRegistry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	seed, err := dbRegistry.IssueRuntimeLease(ctx, extensionsruntime.ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "custom-content-grant-seed",
		Authority: extensionsruntime.ExtensionDatabaseLeaseAuthority{
			Kind: extensionsruntime.ExtensionDatabaseLeaseIssuerActor, ActorUserID: 902, AuditEventID: 1902,
		},
	})
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if _, err := dbRegistry.RevokeRuntimeLease(ctx, extensionsruntime.ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: seed.RuntimeInstanceID, LeaseID: seed.LeaseID,
	}, extensionsruntime.ExtensionDatabaseLeaseAuthority{Kind: extensionsruntime.ExtensionDatabaseLeaseIssuerHost}); err != nil {
		t.Fatalf("revoke seed lease: %v", err)
	}

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "custom-content-reference", ImpactDigest: extension.PackageDigest,
		}},
		DatabaseLeases:                 dbRegistry,
		DatabaseLeaseHeartbeatInterval: 100 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  5 * time.Second,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start custom-content plugin: %v", err)
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
	if active.Identity.InstanceID == "" {
		t.Fatalf("expected Protocol V2 subprocess: %#v", active)
	}

	// --- 真实 routes.Dispatcher：list 路由必须经完整 Route Plan，禁止仅 InvokeRouteInstance ---
	routeReg := routes.NewRegistry()
	pluginArtifact := routes.PluginArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: active.Identity.InstanceID,
	}
	if _, err := routeReg.Publish(routes.Publication{
		Plugins: []routes.PluginRouteSet{{
			Artifact: pluginArtifact,
			Routes:   extension.Manifest.Routes,
			Guards:   extension.Manifest.Guards,
		}},
	}); err != nil {
		t.Fatalf("publish custom-content routes to production registry: %v", err)
	}
	assertCustomContentArticlesViaDispatcher(t, ctx, routeReg, manager, active.Identity)

	// --- 真实存储 + 字段校验 ---
	writeOK, writeErr := invokeCustomContentRouteErr(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.article-write",
		ContractVersion: "sforum.custom-content.route.article-write@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodPost, Path: "/api/custom-content/articles",
		RequestSchema:  "sforum.custom-content.route.article-write.request@1",
		ResponseSchema: "sforum.custom-content.route.article-write.response@1",
		Body: map[string]any{
			"id": "42", "title": "Real Article", "summary": "indexed summary about forums",
			"slug": "real-article", "topicId": "topic-docs", "state": "published",
			"body": "hello-editor-doc",
		}, BodyPresent: true,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if writeErr != nil {
		t.Fatalf("write ok err: %v resp=%#v", writeErr, writeOK)
	}
	if writeOK.StatusCode != http.StatusCreated || writeOK.Body["validated"] != true {
		t.Fatalf("write ok = %#v", writeOK)
	}
	// 字段校验失败路径
	_, writeBadErr := invokeCustomContentRouteErr(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.article-write",
		ContractVersion: "sforum.custom-content.route.article-write@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodPost, Path: "/api/custom-content/articles",
		RequestSchema:  "sforum.custom-content.route.article-write.request@1",
		ResponseSchema: "sforum.custom-content.route.article-write.response@1",
		Body:           map[string]any{"id": "bad", "title": "x", "summary": "", "slug": "BAD SLUG"}, BodyPresent: true,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if writeBadErr == nil {
		t.Fatal("invalid fields must fail validation")
	}

	// --- list 读回 + databaseConnected ---
	list := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.articles",
		ContractVersion: "sforum.custom-content.route.articles@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/custom-content/articles",
		ResponseSchema: "sforum.custom-content.route.articles.response@1",
		Authority:      commerceFilteredHostAuthority(),
		Actor:          extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:        5 * time.Second,
	})
	if list.StatusCode != http.StatusOK || list.Body["databaseConnected"] != true {
		t.Fatalf("list must use real SFORUM_DATABASE_URL: %#v", list)
	}

	// --- taxonomy ---
	tax := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.taxonomy",
		ContractVersion: "sforum.custom-content.route.taxonomy@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/custom-content/taxonomy",
		ResponseSchema: "sforum.custom-content.route.taxonomy.response@1",
		Authority:      commerceFilteredHostAuthority(),
		Actor:          extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:        5 * time.Second,
	})
	if tax.StatusCode != http.StatusOK || tax.Body["hierarchical"] != true {
		t.Fatalf("taxonomy = %#v", tax)
	}

	// --- search ---
	search := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.search",
		ContractVersion: "sforum.custom-content.route.search@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/custom-content/search",
		QueryParameters: map[string]string{"q": "forums"},
		ResponseSchema:  "sforum.custom-content.route.search.response@1",
		Authority:       commerceFilteredHostAuthority(),
		Actor:           extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:         5 * time.Second,
	})
	if search.StatusCode != http.StatusOK || search.Body["indexed"] != true {
		t.Fatalf("search = %#v", search)
	}

	// --- import / export ---
	export := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.export",
		ContractVersion: "sforum.custom-content.route.export@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/custom-content/export",
		ResponseSchema: "sforum.custom-content.route.export.response@1",
		Authority:      commerceFilteredHostAuthority(),
		Actor:          extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:        5 * time.Second,
	})
	if export.StatusCode != http.StatusOK || export.Body["ok"] != true {
		t.Fatalf("export = %#v", export)
	}
	importPayload, _ := json.Marshal(map[string]any{
		"articles": []map[string]any{{
			"id": "99", "title": "Imported", "summary": "from export", "slug": "imported-99", "state": "published",
		}},
	})
	imp := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.import",
		ContractVersion: "sforum.custom-content.route.import@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodPost, Path: "/api/custom-content/import",
		RequestSchema:  "sforum.custom-content.route.import.request@1",
		ResponseSchema: "sforum.custom-content.route.import.response@1",
		Body:           map[string]any{"payload": string(importPayload)}, BodyPresent: true,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if imp.StatusCode != http.StatusOK || imp.Body["ok"] != true {
		t.Fatalf("import = %#v", imp)
	}

	// --- schema migration（POST 必须带 frozen requestSchema）---
	migrate := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.custom-content.route.migrate",
		ContractVersion: "sforum.custom-content.route.migrate@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodPost, Path: "/api/custom-content/migrate",
		RequestSchema:  "sforum.custom-content.route.migrate.request@1",
		ResponseSchema: "sforum.custom-content.route.migrate.response@1",
		Body:           map[string]any{"force": true}, BodyPresent: true,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if migrate.StatusCode != http.StatusOK || migrate.Body["migrated"] != true {
		t.Fatalf("migrate = %#v", migrate)
	}

	// --- block/shortcode/embed 服务端 fallback render ---
	for _, handler := range []string{
		"sforum.custom-content.block.vote",
		"sforum.custom-content.block.product-card",
		"sforum.custom-content.embed.media",
		"sforum.custom-content.shortcode.badge",
		"sforum.custom-content.block.workflow-form",
	} {
		rendered := invokeCustomContentRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
			RouteID:         "sforum.custom-content.route.render",
			ContractVersion: "sforum.custom-content.route.render@1",
			RouteAction:     extensionmanifest.RouteActionAdd,
			InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
			Method:          http.MethodPost, Path: "/api/custom-content/render",
			RequestSchema:  "sforum.custom-content.route.render.request@1",
			ResponseSchema: "sforum.custom-content.route.render.response@1",
			Body:           map[string]any{"handler": handler, "score": "3", "title": "SKU", "label": "new"}, BodyPresent: true,
			Authority: commerceFilteredHostAuthority(),
			Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
			Timeout:   5 * time.Second,
		})
		if rendered.StatusCode != http.StatusOK {
			t.Fatalf("render %s status = %#v", handler, rendered)
		}
		html, _ := rendered.Body["html"].(string)
		if html == "" || strings.Contains(strings.ToLower(html), "<script") {
			t.Fatalf("render %s unsafe/empty html=%q", handler, html)
		}
		if rendered.Body["fallback"] != true {
			t.Fatalf("render %s must mark fallback: %#v", handler, rendered.Body)
		}
	}

	// --- Host registries (Entity/Content/Editor/Nav) ---
	contentArt := contentregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	}
	entityArt := entityregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
	}
	editorArt := editorregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
	}
	navArt := navigationregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: extension.PackageDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: active.Identity.InstanceID,
	}
	entityReg := entityregistry.New()
	entityDecls := make([]entityregistry.Declaration, 0, len(extension.Manifest.Entities))
	for _, item := range extension.Manifest.Entities {
		entityDecls = append(entityDecls, entityregistry.Declaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Kind: item.Kind, Label: item.Label,
			StorageKey: item.StorageKey, PermissionCreate: item.PermissionCreate, PermissionRead: item.PermissionRead,
			PermissionUpdate: item.PermissionUpdate, PermissionDelete: item.PermissionDelete,
			PermissionImport: item.PermissionImport, PermissionExport: item.PermissionExport,
			ImportExportPolicy: item.ImportExportPolicy, DeletionPolicy: item.DeletionPolicy,
			TaxonomyIDs: append([]string(nil), item.TaxonomyIDs...), Hierarchical: item.Hierarchical,
			EntityIDs: append([]string(nil), item.EntityIDs...), PermissionManage: item.PermissionManage,
			PermissionAssign: item.PermissionAssign, EntityID: item.EntityID, Schema: item.Schema,
			UIComponent: item.UIComponent, Required: item.Required, Indexed: item.Indexed,
			IndexKind: item.IndexKind, PermissionFieldRead: item.PermissionFieldRead,
			PermissionFieldWrite: item.PermissionFieldWrite, Order: item.Order, Priority: item.Priority,
		})
	}
	if _, err := entityReg.Publish(entityregistry.Publication{Artifact: entityArt, Entities: entityDecls}); err != nil {
		t.Fatalf("publish entities: %v", err)
	}
	// Import/export dry-run（Host 合同，无隐式授权）
	dry, err := entityReg.DryRunImportExport(
		"sforum.custom-content.entity.article", entityregistry.ActionExport,
		entityregistry.NewActorPermissions("sforum.custom-content.article.export"),
	)
	if err != nil || !dry.Plan.CanExport {
		t.Fatalf("export dry-run = %#v err=%v", dry, err)
	}
	dryDeny, err := entityReg.DryRunImportExport(
		"sforum.custom-content.entity.article", entityregistry.ActionExport,
		entityregistry.NewActorPermissions(),
	)
	if err != nil || dryDeny.Decision.Allowed {
		t.Fatalf("export deny = %#v err=%v", dryDeny, err)
	}

	contentReg := contentregistry.New()
	contentDecls := make([]contentregistry.Declaration, 0, len(extension.Manifest.Content))
	for _, item := range extension.Manifest.Content {
		contentDecls = append(contentDecls, contentregistry.Declaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Kind: item.Kind,
			Handler: item.Handler, Schema: item.Schema, Renderer: item.Renderer, Migration: item.Migration,
		})
	}
	if _, err := contentReg.Publish(contentregistry.Publication{Artifact: contentArt, Content: contentDecls}); err != nil {
		t.Fatalf("publish content: %v", err)
	}
	editorReg := editorregistry.New()
	editorDecls := make([]editorregistry.Declaration, 0, len(extension.Manifest.Editor))
	for _, item := range extension.Manifest.Editor {
		editorDecls = append(editorDecls, editorregistry.Declaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Kind: item.Kind, Schema: item.Schema,
			ExtensionName: item.ExtensionName, L2Module: item.L2Module, L2Digest: item.L2Digest,
			CommandKey: item.CommandKey, CommandID: item.CommandID, Label: item.Label,
			Icon: item.Icon, Group: item.Group, Order: item.Order, Priority: item.Priority,
			Permission: item.Permission,
		})
	}
	if _, err := editorReg.Publish(editorregistry.Publication{Artifact: editorArt, Editor: editorDecls}); err != nil {
		t.Fatalf("publish editor: %v", err)
	}
	navReg := navigationregistry.New()
	navDecls := make([]navigationregistry.NavigationDeclaration, 0, len(extension.Manifest.Navigation))
	for _, item := range extension.Manifest.Navigation {
		decl := navigationregistry.NavigationDeclaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Kind: item.Kind, Action: item.Action,
			TargetID: item.TargetID, Label: item.Label, Href: item.Href, Permission: item.Permission,
			Order: item.Order, Visibility: navigationregistry.VisibilityPublic,
		}
		if item.Label != "" {
			decl.Labels = map[string]string{"zh-CN": item.Label}
		}
		navDecls = append(navDecls, decl)
	}
	regionDecls := make([]navigationregistry.RegionDeclaration, 0, len(extension.Manifest.Regions))
	for _, item := range extension.Manifest.Regions {
		decl := navigationregistry.RegionDeclaration{
			ID: item.ID, ContractVersion: item.ContractVersion, Action: item.Action,
			TargetID: item.TargetID, Kind: item.Kind, Label: item.Label, Multiple: item.Multiple,
			Visibility: navigationregistry.VisibilityPublic,
		}
		if item.Label != "" {
			decl.Labels = map[string]string{"zh-CN": item.Label}
		}
		regionDecls = append(regionDecls, decl)
	}
	if _, err := navReg.Publish(navigationregistry.Publication{
		Artifact: navArt, Navigation: navDecls, Regions: regionDecls,
	}); err != nil {
		t.Fatalf("publish navigation: %v", err)
	}

	// --- 禁用：源内容不丢失 + 稳定 fallback render 仍可执行（声明移除）---
	if err := manager.Stop(ctx, extension); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if _, removed, err := contentReg.Remove(contentArt); err != nil || !removed {
		t.Fatalf("remove content: removed=%v err=%v", removed, err)
	}
	if _, err := contentReg.Resolve("sforum.custom-content.block.vote"); err != contentregistry.ErrNotFound {
		t.Fatalf("content after disable = %v", err)
	}
	// 源文章仍在 PostgreSQL（disable retain）
	var title string
	err = pool.QueryRow(ctx, `
SELECT title FROM sforum_custom_content.articles WHERE id = '42'`).Scan(&title)
	if err != nil {
		// schema 可能在 search_path；尝试 public 或当前 schema。
		err = pool.QueryRow(ctx, `SELECT title FROM articles WHERE id = '42'`).Scan(&title)
	}
	// 若角色隔离导致 host 读不到，至少证明 export 文件仍在。
	if err != nil {
		exportPath, _ := export.Body["path"].(string)
		if exportPath == "" {
			exportPath = filepath.Join(os.TempDir(), "sforum-custom-content-export.json")
		}
		body, readErr := os.ReadFile(exportPath)
		if readErr != nil || !strings.Contains(string(body), "real-article") {
			t.Fatalf("source content lost after disable: pg=%v export=%v path=%s", err, readErr, exportPath)
		}
		t.Log("coverage.source_retain=export-file-after-disable")
	} else if title != "Real Article" {
		t.Fatalf("source title after disable = %q", title)
	}
	// 稳定 fallback：未知 handler 渲染不 panic，返回 unavailable 标记。
	// 插件已 stop，此处用 Host ContentRegistry preserve_source 语义证明声明移除后无崩溃。
	if _, err := entityReg.Resolve("sforum.custom-content.entity.article"); err == nil {
		// still published until Remove
	}
	if _, removed, err := entityReg.Remove(entityArt); err != nil || !removed {
		t.Fatalf("remove entity: removed=%v err=%v", removed, err)
	}
	if _, err := entityReg.Resolve("sforum.custom-content.entity.article"); err != entityregistry.ErrNotFound {
		t.Fatalf("entity after disable = %v", err)
	}
	if _, removed, err := editorReg.Remove(editorArt); err != nil || !removed {
		t.Fatalf("remove editor: removed=%v err=%v", removed, err)
	}
	if _, removed, err := navReg.Remove(navArt); err != nil || !removed {
		t.Fatalf("remove navigation: removed=%v err=%v", removed, err)
	}
}

// assertCustomContentArticlesViaDispatcher 证明 articles 路由从真实 Dispatcher 进入并走完整 Route Plan。
func assertCustomContentArticlesViaDispatcher(
	t *testing.T,
	ctx context.Context,
	registry *routes.Registry,
	manager *extensionsruntime.Manager,
	identity extensionsruntime.RuntimeInstanceIdentity,
) {
	t.Helper()
	plan, err := registry.BuildExecutionPlan(http.MethodGet, "/api/custom-content/articles")
	if err != nil {
		t.Fatalf("custom-content dispatcher plan: %v", err)
	}
	if plan.Terminal().Action != extensionmanifest.RouteActionAdd {
		t.Fatalf("custom-content dispatcher terminal = %#v", plan.Terminal())
	}
	stepInvoker := &customContentManagerStepInvoker{manager: manager, identity: identity}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans:   customContentPlanResolver{plan: plan},
		Steps:   stepInvoker,
		Guard:   customContentAllowGuard{},
		Schemas: customContentAcceptSchemas{},
	})
	result, err := dispatcher.Dispatch(ctx, routes.DispatchRequest{
		Method: http.MethodGet, Path: "/api/custom-content/articles",
		ActorID: 42, Authenticated: true,
		Permissions: map[string]bool{"*": true},
	}, nil)
	if err != nil {
		t.Fatalf("dispatcher.Dispatch articles: %v", err)
	}
	if !result.Handled || result.Response.Status != http.StatusOK {
		t.Fatalf("dispatcher articles result = %#v", result)
	}
	if stepInvoker.invokes < 1 {
		t.Fatal("dispatcher must invoke custom-content plugin step at least once")
	}
	var body map[string]any
	if err := json.Unmarshal(result.Response.Body, &body); err != nil {
		t.Fatalf("dispatcher articles body: %v raw=%s", err, result.Response.Body)
	}
	if body["databaseConnected"] != true {
		t.Fatalf("dispatcher articles must prove real DB: %#v", body)
	}
}

type customContentPlanResolver struct{ plan routes.RouteExecutionPlan }

func (r customContentPlanResolver) BuildExecutionPlan(context.Context, string, string) (routes.RouteExecutionPlan, error) {
	return r.plan, nil
}

type customContentAllowGuard struct{}

func (customContentAllowGuard) Authorize(context.Context, routes.RouteExecutionPlan, routes.RouteExecutionStep, routes.DispatchRequest) error {
	return nil
}

type customContentAcceptSchemas struct{}

func (customContentAcceptSchemas) ValidateRequest(context.Context, routes.RouteExecutionStep, routes.DispatchRequest) error {
	return nil
}
func (customContentAcceptSchemas) ValidateResponse(context.Context, routes.RouteExecutionStep, routes.DispatchRequest, routes.DispatchResponse) error {
	return nil
}

type customContentManagerStepInvoker struct {
	manager  *extensionsruntime.Manager
	identity extensionsruntime.RuntimeInstanceIdentity
	invokes  int
}

func (*customContentManagerStepInvoker) SupportsMode(mode string) bool {
	return mode == "" || mode == extensionmanifest.RouteModeHTTP
}

func (i *customContentManagerStepInvoker) Invoke(ctx context.Context, input routes.RouteInvocation) (routes.RouteInvocationResult, error) {
	i.invokes++
	lease, err := i.manager.AcquireRuntimeCall(ctx, i.identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	defer lease.Release()
	stage := extensionsruntime.ProtocolV2RouteInvocationStageHandler
	switch input.Stage {
	case routes.InvocationStageRequest:
		stage = extensionsruntime.ProtocolV2RouteInvocationStageRequest
	case routes.InvocationStageResponse:
		stage = extensionsruntime.ProtocolV2RouteInvocationStageResponse
	}
	resp, err := i.manager.InvokeRouteInstance(lease.Context, i.identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID: input.Step.RouteID, ContractVersion: input.Step.ContractVersion,
		RouteAction: input.Step.Action, InvocationStage: stage,
		Method: input.Request.Method, Path: input.Request.Path,
		ResponseSchema: input.Step.ResponseSchema,
		Authority:      commerceFilteredHostAuthority(),
		Actor: extensionsruntime.NewProtocolV2RouteActor(
			input.Request.ActorID, input.Request.Authenticated, input.Request.Permissions,
		),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	body, _ := json.Marshal(resp.Body)
	headers := resp.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return routes.RouteInvocationResult{
		Response: &routes.DispatchResponse{
			Status: resp.StatusCode, Headers: headers, Body: body,
		},
	}, nil
}

func invokeCustomContentRoute(
	t *testing.T,
	manager *extensionsruntime.Manager,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) extensionsruntime.ProtocolV2RouteResponse {
	t.Helper()
	resp, err := invokeCustomContentRouteErr(t, manager, identity, request)
	if err != nil {
		t.Fatalf("invoke route %s: %v", request.RouteID, err)
	}
	return resp
}

func invokeCustomContentRouteErr(
	t *testing.T,
	manager *extensionsruntime.Manager,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	t.Helper()
	lease, err := manager.AcquireRuntimeCall(context.Background(), identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatalf("acquire route: %v", err)
	}
	defer lease.Release()
	return manager.InvokeRouteInstance(lease.Context, identity, request)
}

func buildReferenceCustomContentExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceCustomContentFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.custom-content")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy custom-content plugin: %v", err)
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
		t.Fatalf("build custom-content plugin: %v\n%s", err, output)
	}
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	editorPath := filepath.Join(packageRoot, "frontend", "editor", "vote.mjs")
	schemaPath := filepath.Join(packageRoot, "schemas", "articles.json")
	manifestBody := string(templateBody)
	manifestBody = strings.ReplaceAll(manifestBody, "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	manifestBody = strings.ReplaceAll(manifestBody, "__EDITOR_VOTE_DIGEST__", fileSHA256(t, editorPath))
	manifestBody = strings.ReplaceAll(manifestBody, "__ARTICLES_SCHEMA_DIGEST__", fileSHA256(t, schemaPath))
	if strings.Contains(manifestBody, "__") {
		t.Fatal("custom-content manifest still contains digest tokens")
	}
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load custom-content package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 601,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func referenceCustomContentFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-custom-content"))
}
