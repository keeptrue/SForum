package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/bootstrap"
)

// R2（2026-10-16）：页面数据经 Protocol V2 精确实例调用取数。
//
// 历史：本文件此前 pin 的是「Page Loader Gateway 在 V2 下没有传输」——V2 明确拒绝
// loopback（插件不回报 BaseURL），而旧适配器只认 BaseURL，于是 dataSource=plugin 的
// 页面贡献恒为 Fallback+503。运维方拍板（决定 R2）给 loader 补一条 V2 取数路径：
// 复用已有的 `InvokeRouteInstance`（无需改冻结线协议），并把「可以调用哪些路由」收紧为
// **插件 manifest 声明的 `core.guard.public` 路由**——loader 不携带 actor 身份，因此
// 不能代评 login/permission，也不能代 custom/raw 守卫做授权判断。

const (
	pageLoaderDataPath    = "/page-data/article"
	pageLoaderDataRouteID = "runtime.v2.route.page-data"
)

// pageLoaderV2Extension 是真实现 Protocol V2 子进程的夹具，额外声明一条公开数据路由。
func pageLoaderV2Extension(t *testing.T) extensions.Extension {
	t.Helper()
	extension := protocolV2TestExtension(t, "v2")
	launcher := "#!/bin/sh\nSFORUM_PLUGIN_HELPER=" + protocolV2RouteHelperEnv + " exec " + shellQuote(os.Args[0]) +
		" -test.run=TestProtocolV2RouteHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "backend", "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	extension.Manifest.Events = nil
	extension.Manifest.Services = nil
	extension.Manifest.Guards = nil
	extension.Manifest.Routes = []extensions.ManifestRoute{{
		ID: pageLoaderDataRouteID, ContractVersion: pageLoaderDataRouteID + "@1",
		Action: extensionmanifest.RouteActionAdd, Path: pageLoaderDataPath,
		Methods: []string{http.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
		Handler: "route.page-data", ResponseSchema: "runtime.v2.route.response@1",
	}}
	return extension
}

// pageLoaderRouteStore 是最小的扩展记录来源（适配器只依赖 Get）。
type pageLoaderRouteStore struct {
	item extensions.Extension
	err  error
}

func (s pageLoaderRouteStore) Get(context.Context, string) (extensions.Extension, error) {
	return s.item, s.err
}

func pageLoaderContribution(extension extensions.Extension, instanceID string) pages.PageContribution {
	return pages.PageContribution{
		ID: "runtime.v2.page.article", Action: pages.ActionAdd, Path: "/v2-articles/:slug",
		Template: "templates/article.html", Contract: "runtime.v2.page.article@1", Access: pages.AccessPublic,
		DataSource: "plugin", DataRoute: pageLoaderDataPath, DataSchema: "",
		ExtensionID: extension.ID, Version: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: instanceID,
	}
}

// TestPageLoaderGatewayLoadsDataThroughProtocolV2Runtime 是允许侧：真实 V2 子进程 +
// 插件自己声明的公开数据路由 ⇒ loader 拿到插件返回的类型化文档（跨真实 gRPC 边界），
// 且 admission lease 被释放（第二次调用结论稳定）。
func TestPageLoaderGatewayLoadsDataThroughProtocolV2Runtime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping protocol v2 subprocess runtime in short mode")
	}
	extension := pageLoaderV2Extension(t)
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "41", ImpactDigest: "impact-41",
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start protocol v2 plugin: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Identity.InstanceID == "" {
		t.Fatalf("expected an exact runtime instance: %#v", active)
	}
	if active.Target.BaseURL != "" {
		t.Fatalf("protocol v2 must not expose a loopback target: %#v", active.Target)
	}

	loader := pages.NewLoaderGateway(
		pages.NewPageDataLoader(nil),
		bootstrap.NewPageRouteTargetSourceWithRoutes(manager, pageLoaderRouteStore{item: extension}),
	)
	contrib := pageLoaderContribution(extension, active.Identity.InstanceID)
	result := loader.LoadExactForContribution(ctx, contrib, map[string]string{"slug": "hello"}, "zh-CN", 7)
	if result.Fallback || result.Status != http.StatusOK || len(result.Data) == 0 {
		t.Fatalf("v2 loader result = %#v", result)
	}
	var document map[string]any
	if err := json.Unmarshal(result.Data, &document); err != nil {
		t.Fatalf("decode loader document: %v", err)
	}
	// 断言的是**插件返回的内容**真的穿过边界到达 gateway，而不是「接口返回 200」。
	if document["title"] != "loaded through protocol v2" || document["slug"] != "hello" ||
		document["locale"] != "zh-CN" || document["actorHint"] != "7" {
		t.Fatalf("loader document = %#v", document)
	}

	// lease 释放：重复调用结论一致（泄漏会以调用名额耗尽暴露）。
	second := loader.LoadExactForContribution(ctx, contrib, map[string]string{"slug": "hello"}, "zh-CN", 7)
	if second.Fallback || second.Status != http.StatusOK || string(second.Data) != string(result.Data) {
		t.Fatalf("second v2 loader result = %#v", second)
	}
}

// TestPageLoaderGatewayRefusesNonPublicDataRoutes 是拒绝侧：只有插件声明的
// `core.guard.public` 路由可被 loader 调用。
func TestPageLoaderGatewayRefusesNonPublicDataRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping protocol v2 subprocess runtime in short mode")
	}
	extension := pageLoaderV2Extension(t)
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "41", ImpactDigest: "impact-41",
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start protocol v2 plugin: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}

	loginGuarded := extension
	loginGuarded.Manifest.Routes = append([]extensions.ManifestRoute(nil), extension.Manifest.Routes...)
	loginGuarded.Manifest.Routes[0].Guard = extensionmanifest.GuardCoreLogin

	drifted := extension
	drifted.PackageDigest = strings.Repeat("f", 64)

	tests := []struct {
		name    string
		store   pageLoaderRouteStore
		contrib pages.PageContribution
	}{
		{
			name:  "undeclared data route",
			store: pageLoaderRouteStore{item: extension},
			contrib: func() pages.PageContribution {
				c := pageLoaderContribution(extension, active.Identity.InstanceID)
				c.DataRoute = "/page-data/undeclared"
				return c
			}(),
		},
		{
			name:  "non-public guard",
			store: pageLoaderRouteStore{item: loginGuarded},
			contrib: func() pages.PageContribution {
				return pageLoaderContribution(loginGuarded, active.Identity.InstanceID)
			}(),
		},
		{
			name:  "artifact digest drift",
			store: pageLoaderRouteStore{item: extension},
			contrib: func() pages.PageContribution {
				c := pageLoaderContribution(extension, active.Identity.InstanceID)
				c.PackageDigest = drifted.PackageDigest
				return c
			}(),
		},
		{
			name:    "no route source wired",
			store:   pageLoaderRouteStore{err: os.ErrNotExist},
			contrib: pageLoaderContribution(extension, active.Identity.InstanceID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loader := pages.NewLoaderGateway(
				pages.NewPageDataLoader(nil),
				bootstrap.NewPageRouteTargetSourceWithRoutes(manager, test.store),
			)
			result := loader.LoadExactForContribution(ctx, test.contrib, nil, "zh-CN", 0)
			if !result.Fallback || result.Status != http.StatusServiceUnavailable || len(result.Data) != 0 {
				t.Fatalf("expected fail-closed fallback, got %#v", result)
			}
		})
	}
}
