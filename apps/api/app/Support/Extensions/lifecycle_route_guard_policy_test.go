package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// M5 任务 6：路由/守卫发布必须整体切换。升级丢弃旧守卫绑定（不得残留旧授权），
// 回滚恢复**先前**的精确守卫绑定（id / kind / entry / digest / permissions 全同），
// 禁用或安全模式发布权威空集而不是保留半套路由。
func TestLifecycleRoutePolicyPublishesExactGuardBindingsAcrossUpgradeAndRollback(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	routeRegistry := routes.NewRegistry()
	routeSchemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routeRegistry, RouteSchemas: routeSchemas,
	})

	source := lifecycleGuardRouteExtension(
		t, "1.0.0", strings.Repeat("d", 64), 1301, "/guard-policy-source", "registry.demo.guard.owner",
	)
	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, false); err != nil {
		t.Fatal(err)
	}
	assertLifecycleGuardRouteBinding(
		t, routeRegistry, "GET", "/guard-policy-source/api",
		"registry.demo.guard.owner", source.Manifest.Guards[0].Digest,
	)
	if ids := lifecyclePublishedGuardIDs(routeRegistry); !equalStringSlice(ids, []string{"registry.demo.guard.owner"}) {
		t.Fatalf("source publication guards = %#v", ids)
	}

	// 升级：旧实例停止、目标版本成为唯一精确运行时后才发布。
	if err := manager.Stop(ctx, source); err != nil {
		t.Fatal(err)
	}
	target := lifecycleGuardRouteExtension(
		t, "2.0.0", strings.Repeat("f", 64), 1302, "/guard-policy-target", "registry.demo.guard.owner",
	)
	if err := manager.Start(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{target}, false); err != nil {
		t.Fatal(err)
	}
	assertLifecycleGuardRouteBinding(
		t, routeRegistry, "GET", "/guard-policy-target/api",
		"registry.demo.guard.owner", target.Manifest.Guards[0].Digest,
	)
	if _, err := routeRegistry.BuildExecutionPlan("GET", "/guard-policy-source/api"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("source route must be gone after upgrade: %v", err)
	}
	for _, step := range mustLifecycleGuardPlan(t, routeRegistry, "GET", "/guard-policy-target/api") {
		if step.PluginGuard.ID != "" && step.PluginGuard.Digest == source.Manifest.Guards[0].Digest {
			t.Fatalf("stale source guard digest survived the upgrade: %#v", step.PluginGuard)
		}
	}
	if source.Manifest.Guards[0].Digest == target.Manifest.Guards[0].Digest {
		t.Fatalf("guard fixture digests must differ per version")
	}

	// 回滚：把先前版本重新启动并发布，守卫绑定必须逐字段回到先前值。
	if err := manager.Stop(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, false); err != nil {
		t.Fatal(err)
	}
	assertLifecycleGuardRouteBinding(
		t, routeRegistry, "GET", "/guard-policy-source/api",
		"registry.demo.guard.owner", source.Manifest.Guards[0].Digest,
	)
	if _, err := routeRegistry.BuildExecutionPlan("GET", "/guard-policy-target/api"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("target route must be gone after rollback: %v", err)
	}

	// 禁用/安全模式：权威空发布，不得留下任何守卫授权。
	if err := boundary.RestoreRoutePublications(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	if ids := lifecyclePublishedGuardIDs(routeRegistry); len(ids) != 0 {
		t.Fatalf("disabled publication kept guard authority: %#v", ids)
	}
	if _, err := routeRegistry.BuildExecutionPlan("GET", "/guard-policy-source/api"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("route survived an authoritative empty publication: %v", err)
	}

	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, true); err != nil {
		t.Fatal(err)
	}
	if ids := lifecyclePublishedGuardIDs(routeRegistry); len(ids) != 0 {
		t.Fatalf("safe mode published guard authority: %#v", ids)
	}
	if _, err := routeRegistry.BuildExecutionPlan("GET", "/guard-policy-source/api"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("route survived safe mode: %v", err)
	}
}

// lifecycleGuardRouteExtension 构造一个声明自定义守卫的路由夹具。
// 与 lifecycleRegistryTestExtension 的差别只有三处：清单声明守卫与守卫权限、
// 路由绑定该守卫、OpenAPI 片段的 x-sforum-guard 同步为同一个守卫。
func lifecycleGuardRouteExtension(
	t *testing.T,
	version string,
	digest string,
	versionID int64,
	pagePath string,
	guardID string,
) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, digest, versionID, pagePath)
	// 守卫可执行文件是一个独立的精确制品：它必须与 packageFiles[] 中声明的
	// executable 路径与摘要一致（v3 校验会拒绝"指向别的可执行文件"的守卫）。
	guardBody := []byte("registry-demo-guard-" + version)
	guardDigest := sha256.Sum256(guardBody)
	guardDigestValue := hex.EncodeToString(guardDigest[:])
	guardPath := filepath.Join(extension.PackagePath, "bin", "guard")
	if err := os.MkdirAll(filepath.Dir(guardPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guardPath, guardBody, 0o600); err != nil {
		t.Fatal(err)
	}
	extension.Manifest.Permissions = []string{"registry.demo.manage"}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: "registry.demo.file.guard", Kind: "executable", Path: "bin/guard", Digest: guardDigestValue,
	})
	extension.Manifest.Guards = []extensions.ManifestGuard{{
		ID: guardID, ContractVersion: guardID + "@1", Kind: "custom",
		Entry: "bin/guard", Digest: guardDigestValue,
		Permissions: []string{"registry.demo.manage"},
	}}
	extension.Manifest.Routes[0].Guard = guardID

	openAPIBody := []byte(fmt.Sprintf(`openapi: 3.1.0
info:
  title: Registry demo guard
  version: %s
paths:
  %s/api:
    get:
      operationId: registry.demo.api.read
      x-sforum-route-id: %s
      x-sforum-contract-version: %s
      x-sforum-guard: %s
      x-sforum-response-schema: registry.demo.read.response@1
      x-sforum-rate-limit: disabled
      x-sforum-idempotency: disabled
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
`, version, pagePath, extension.Manifest.Routes[0].ID, extension.Manifest.Routes[0].ContractVersion, guardID))
	openAPIDigest := sha256.Sum256(openAPIBody)
	openAPIDigestValue := hex.EncodeToString(openAPIDigest[:])
	extension.Manifest.OpenAPI[0].Digest = openAPIDigestValue
	for index := range extension.Manifest.PackageFiles {
		file := &extension.Manifest.PackageFiles[index]
		if file.Path == extension.Manifest.OpenAPI[0].Path {
			file.Digest = openAPIDigestValue
		}
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, filepath.FromSlash(extension.Manifest.OpenAPI[0].Path)),
		openAPIBody,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(extension.PackagePath, extensions.ManifestFileName), manifestBody, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func mustLifecycleGuardPlan(
	t *testing.T,
	registry *routes.Registry,
	method string,
	path string,
) []routes.RouteExecutionStep {
	t.Helper()
	plan, err := registry.BuildExecutionPlan(method, path)
	if err != nil {
		t.Fatalf("execution plan %s %s: %v", method, path, err)
	}
	return plan.Chain()
}

func assertLifecycleGuardRouteBinding(
	t *testing.T,
	registry *routes.Registry,
	method string,
	path string,
	guardID string,
	wantDigest string,
) {
	t.Helper()
	var binding *routes.PluginGuardBinding
	for _, step := range mustLifecycleGuardPlan(t, registry, method, path) {
		if step.PluginGuard.ID == "" {
			continue
		}
		copied := step.PluginGuard
		binding = &copied
	}
	if binding == nil {
		t.Fatalf("route %s %s published no plugin guard binding", method, path)
	}
	if binding.ID != guardID || binding.Kind != "custom" || binding.Entry != "bin/guard" || binding.Digest != wantDigest {
		t.Fatalf("guard binding = %#v; want id=%s digest=%s", *binding, guardID, wantDigest)
	}
	if len(binding.Permissions) != 1 || binding.Permissions[0] != "registry.demo.manage" {
		t.Fatalf("guard permissions = %#v", binding.Permissions)
	}
}

// lifecyclePublishedGuardIDs 返回已发布集合里的全部守卫声明 id（排序），
// 用于断言「没有残留的旧守卫授权」。
func lifecyclePublishedGuardIDs(registry *routes.Registry) []string {
	publication := registry.PublicationSnapshot().Publication
	ids := make([]string, 0)
	for _, plugin := range publication.Plugins {
		for _, guard := range plugin.Guards {
			ids = append(ids, guard.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func equalStringSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
