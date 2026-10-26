package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

func TestPackageLocalComponentSSRPublishAndRender(t *testing.T) {
	root := t.TempDir()
	body := `<aside data-e2e="plugin-ssr">{{index .Props "title"}}</aside>`
	writePackageLocalTestFile(t, root, "templates/card.html", body)
	digest := sha256.Sum256([]byte(body))
	digestHex := hex.EncodeToString(digest[:])
	packageDigest := strings.Repeat("a", 64)
	extension := extensions.Extension{
		ID: "demo.ssr", Version: "1.0.0", Type: extensions.TypePlugin,
		PackageDigest: packageDigest, PackagePath: root,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "demo.ssr", Version: "1.0.0", Type: extensions.TypePlugin,
			Components: []extensions.ManifestComponent{{
				ID: "demo.ssr.component.card", ContractVersion: "demo.ssr.component.card@1",
				Action: extensionmanifest.ComponentActionBefore, TargetID: componentTestCoreTarget,
				TargetContractVersion: componentTestCoreContract, Priority: 10,
				SSRTemplate: "demo.ssr.template.card", PropsSchema: "demo.ssr.schema.props@1",
			}},
			Templates: []extensions.ManifestTemplate{{
				ID: "demo.ssr.template.card", ContractVersion: "demo.ssr.template.card@1",
				Action: "add", Path: "templates/card.html", Digest: digestHex,
				ViewModelSchema: "demo.ssr.schema.props@1",
			}},
		},
	}
	renderer := NewPackageLocalComponentSSRRenderer()
	if err := renderer.Publish(extension); err != nil {
		t.Fatal(err)
	}
	if renderer.Count() != 1 {
		t.Fatalf("count=%d", renderer.Count())
	}
	response, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Contribution: ComponentContribution{
			ID: "demo.ssr.component.card", Action: extensionmanifest.ComponentActionBefore,
			SSRTemplate: "demo.ssr.template.card",
			Artifact: HookArtifact{
				ExtensionID: "demo.ssr", ExtensionVersion: "1.0.0",
				PackageDigest: packageDigest, RuntimeInstanceID: "host-component-package:demo.ssr",
			},
		},
		Artifact: HookArtifact{
			ExtensionID: "demo.ssr", ExtensionVersion: "1.0.0",
			PackageDigest: packageDigest, RuntimeInstanceID: "host-component-package:demo.ssr",
		},
		Props: map[string]any{"title": "Hello SSR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Fragments) != 1 ||
		!strings.Contains(response.Fragments[0].ReviewedHTML, `data-e2e="plugin-ssr"`) ||
		!strings.Contains(response.Fragments[0].ReviewedHTML, "Hello SSR") {
		t.Fatalf("fragments=%#v", response.Fragments)
	}

	// 请求路径不再读包文件：删除源文件后仍可渲染。
	if err := os.Remove(filepath.Join(root, "templates/card.html")); err != nil {
		t.Fatal(err)
	}
	again, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget,
		Contribution: ComponentContribution{
			ID: "demo.ssr.component.card", Action: extensionmanifest.ComponentActionBefore,
			SSRTemplate: "demo.ssr.template.card",
			Artifact: HookArtifact{
				ExtensionID: "demo.ssr", PackageDigest: packageDigest,
				RuntimeInstanceID: "host-component-package:demo.ssr",
			},
		},
		Artifact: HookArtifact{
			ExtensionID: "demo.ssr", PackageDigest: packageDigest,
			RuntimeInstanceID: "host-component-package:demo.ssr",
		},
		Props: map[string]any{"title": "Cached"},
	})
	if err != nil || !strings.Contains(again.Fragments[0].ReviewedHTML, "Cached") {
		t.Fatalf("cached render err=%v fragments=%#v", err, again.Fragments)
	}

	// digest 漂移 fail closed。
	bad := extension
	bad.Manifest.Templates[0].Digest = strings.Repeat("b", 64)
	writePackageLocalTestFile(t, root, "templates/card.html", body)
	if err := renderer.Publish(bad); err == nil {
		t.Fatal("expected digest mismatch")
	}

	renderer.RemovePackage(packageDigest)
	if renderer.Count() != 0 {
		t.Fatalf("after remove count=%d", renderer.Count())
	}
	if _, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		Contribution: ComponentContribution{
			ID: "demo.ssr.component.card", SSRTemplate: "demo.ssr.template.card",
			Action:   extensionmanifest.ComponentActionBefore,
			Artifact: HookArtifact{ExtensionID: "demo.ssr", PackageDigest: packageDigest},
		},
		Artifact: HookArtifact{ExtensionID: "demo.ssr", PackageDigest: packageDigest},
		Props:    map[string]any{"title": "gone"},
	}); err == nil {
		t.Fatal("expected crash after remove")
	}
}

func TestPackageLocalFilterTemplatesTransformPropsAndResult(t *testing.T) {
	root := t.TempDir()
	propsBody := `{"scope":{{json (printf "%s-filtered" (index .Props "scope"))}}}`
	resultBody := `{"html":{{json (printf "%s-filtered" (index .Result "html"))}}}`
	writePackageLocalTestFile(t, root, "templates/filter-props.json.tmpl", propsBody)
	writePackageLocalTestFile(t, root, "templates/filter-result.json.tmpl", resultBody)
	propsDigest := sha256.Sum256([]byte(propsBody))
	resultDigest := sha256.Sum256([]byte(resultBody))
	packageDigest := strings.Repeat("e", 64)
	extension := extensions.Extension{
		ID: "demo.filter", Version: "1.0.0", Type: extensions.TypePlugin,
		PackageDigest: packageDigest, PackagePath: root,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "demo.filter", Version: "1.0.0", Type: extensions.TypePlugin,
			Components: []extensions.ManifestComponent{
				{
					ID: "demo.filter.component.props", ContractVersion: "demo.filter.component.props@1",
					Action: extensionmanifest.ComponentActionFilterProps, TargetID: componentTestCoreTarget,
					TargetContractVersion: componentTestCoreContract, Priority: 20,
					SSRTemplate: "demo.filter.template.props", PropsSchema: "demo.filter.schema.props@1",
				},
				{
					ID: "demo.filter.component.result", ContractVersion: "demo.filter.component.result@1",
					Action: extensionmanifest.ComponentActionFilterResult, TargetID: componentTestCoreTarget,
					TargetContractVersion: componentTestCoreContract, Priority: 10,
					SSRTemplate: "demo.filter.template.result", ResultSchema: "demo.filter.schema.result@1",
				},
			},
			Templates: []extensions.ManifestTemplate{
				{
					ID: "demo.filter.template.props", ContractVersion: "demo.filter.template.props@1",
					Action: "add", Path: "templates/filter-props.json.tmpl",
					Digest: hex.EncodeToString(propsDigest[:]), ViewModelSchema: "demo.filter.schema.props@1",
				},
				{
					ID: "demo.filter.template.result", ContractVersion: "demo.filter.template.result@1",
					Action: "add", Path: "templates/filter-result.json.tmpl",
					Digest: hex.EncodeToString(resultDigest[:]), ViewModelSchema: "demo.filter.schema.result@1",
				},
			},
		},
	}
	renderer := NewPackageLocalComponentSSRRenderer()
	if err := renderer.Publish(extension); err != nil {
		t.Fatal(err)
	}
	artifact := HookArtifact{
		ExtensionID: "demo.filter", ExtensionVersion: "1.0.0",
		PackageDigest: packageDigest, RuntimeInstanceID: "host-component-package:demo.filter",
	}
	propsOut, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget, Artifact: artifact,
		Contribution: ComponentContribution{
			ID: "demo.filter.component.props", Action: extensionmanifest.ComponentActionFilterProps,
			SSRTemplate: "demo.filter.template.props", Artifact: artifact,
		},
		Props: map[string]any{"scope": "home"},
	})
	if err != nil || propsOut.Document["scope"] != "home-filtered" || len(propsOut.Fragments) != 0 {
		t.Fatalf("filter props = %#v err=%v", propsOut, err)
	}
	resultOut, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget, Artifact: artifact,
		Contribution: ComponentContribution{
			ID: "demo.filter.component.result", Action: extensionmanifest.ComponentActionFilterResult,
			SSRTemplate: "demo.filter.template.result", Artifact: artifact,
		},
		Result: map[string]any{"html": "body"},
	})
	if err != nil || resultOut.Document["html"] != "body-filtered" || len(resultOut.Fragments) != 0 {
		t.Fatalf("filter result = %#v err=%v", resultOut, err)
	}
}

func TestProductionComponentCompositionAppliesPackageLocalFilterMatrix(t *testing.T) {
	id := "production.package.filters"
	packageDigest := strings.Repeat("f", 64)
	// 分离 HTML before 与 filter 模板，避免 Publish 时 kind 冲突。
	propsFilter := componentTestContribution(
		id, "filter-props", extensionmanifest.ComponentActionFilterProps, 80,
		componentTestCoreTarget, componentTestCoreContract,
	)
	propsFilter.SSRTemplate = id + ".template.filter-props"
	resultFilter := componentTestContribution(
		id, "filter-result", extensionmanifest.ComponentActionFilterResult, 10,
		componentTestCoreTarget, componentTestCoreContract,
	)
	resultFilter.SSRTemplate = id + ".template.filter-result"
	before := componentTestContribution(
		id, "before", extensionmanifest.ComponentActionBefore, 50,
		componentTestCoreTarget, componentTestCoreContract,
	)
	before.SSRTemplate = id + ".template.before"
	extension := componentTestExtension(t, id, extensions.TypePlugin, propsFilter, before, resultFilter)
	extension.PackageDigest = packageDigest

	propsBody := `{"scope":{{json (printf "%s-filtered" (index .Props "scope"))}}}`
	resultBody := `{"html":{{json (printf "%s-filtered" (index .Result "html"))}}}`
	beforeBody := `<div class="plugin-before">{{index .Props "scope"}}</div>`
	writePackageLocalTestFile(t, extension.PackagePath, "templates/filter-props.json.tmpl", propsBody)
	writePackageLocalTestFile(t, extension.PackagePath, "templates/filter-result.json.tmpl", resultBody)
	writePackageLocalTestFile(t, extension.PackagePath, "templates/before.html", beforeBody)
	propsDigest := sha256.Sum256([]byte(propsBody))
	resultDigest := sha256.Sum256([]byte(resultBody))
	beforeDigest := sha256.Sum256([]byte(beforeBody))
	extension.Manifest.Templates = []extensions.ManifestTemplate{
		{
			ID: id + ".template.filter-props", ContractVersion: id + ".template.filter-props@1",
			Action: "add", Path: "templates/filter-props.json.tmpl",
			Digest: hex.EncodeToString(propsDigest[:]), ViewModelSchema: id + ".schema.props@1",
		},
		{
			ID: id + ".template.filter-result", ContractVersion: id + ".template.filter-result@1",
			Action: "add", Path: "templates/filter-result.json.tmpl",
			Digest: hex.EncodeToString(resultDigest[:]), ViewModelSchema: id + ".schema.result@1",
		},
		{
			ID: id + ".template.before", ContractVersion: id + ".template.before@1",
			Action: "add", Path: "templates/before.html",
			Digest: hex.EncodeToString(beforeDigest[:]), ViewModelSchema: id + ".schema.props@1",
		},
	}

	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, componentPackageRuntimeInstanceID(extension)); err != nil {
		t.Fatal(err)
	}
	service, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishPackageSSR(extension); err != nil {
		t.Fatal(err)
	}
	// 过滤器要求目标契约显式声明可写字段；生产页面目标由 Host binding 提供。
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps: allowAnyComponentDocument, ValidateResult: allowAnyComponentDocument,
			MutablePropsFields: []string{"scope"}, MutableResultFields: []string{"html"},
			RetainPrimaryContent: true,
		},
		Fallback: func(_ context.Context, _ ComponentFallbackCall) (ComponentRenderResponse, error) {
			return ComponentRenderResponse{
				Document:  map[string]any{"html": "core-body"},
				Fragments: []ComponentRenderFragment{{Text: "core", PrimaryContent: true}},
			}, nil
		},
	}
	input := map[string]any{"scope": "home"}
	result, err := service.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: input, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 输入不被就地修改；组合结果 props/result 经过 filter 变换。
	if input["scope"] != "home" {
		t.Fatalf("input mutated: %#v", input)
	}
	if result.Props["scope"] != "home-filtered" {
		t.Fatalf("filtered props = %#v", result.Props)
	}
	// Core primary 结果经 filter_result 追加后缀。
	html, _ := result.Result["html"].(string)
	if !strings.Contains(html, "filtered") {
		t.Fatalf("filtered result = %#v", result.Result)
	}
	joined := flattenComponentHTML(result.Segments)
	if !strings.Contains(joined, "plugin-before") || !strings.Contains(joined, "home-filtered") {
		t.Fatalf("composed HTML=%q", joined)
	}
}

func TestPackageLocalPluginFragmentsNeverClaimPrimarySEOContent(t *testing.T) {
	// P9 SEO 围栏：包本地插件片段默认非 PrimaryContent，主题 L1 保留索引主体。
	root := t.TempDir()
	body := `<section data-plugin="seo">{{index .Props "scope"}}</section>`
	writePackageLocalTestFile(t, root, "templates/card.html", body)
	digest := sha256.Sum256([]byte(body))
	packageDigest := strings.Repeat("9", 64)
	extension := extensions.Extension{
		ID: "demo.seo.ssr", Version: "1.0.0", Type: extensions.TypePlugin,
		PackageDigest: packageDigest, PackagePath: root,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: "demo.seo.ssr", Version: "1.0.0", Type: extensions.TypePlugin,
			Components: []extensions.ManifestComponent{{
				ID: "demo.seo.ssr.component.replace", ContractVersion: "demo.seo.ssr.component.replace@1",
				Action: extensionmanifest.ComponentActionReplace, TargetID: componentTestCoreTarget,
				TargetContractVersion: componentTestCoreContract, Priority: 10,
				SSRTemplate: "demo.seo.ssr.template.card", ResultSchema: "demo.seo.ssr.schema.result@1",
			}},
			Templates: []extensions.ManifestTemplate{{
				ID: "demo.seo.ssr.template.card", ContractVersion: "demo.seo.ssr.template.card@1",
				Action: "add", Path: "templates/card.html", Digest: hex.EncodeToString(digest[:]),
			}},
		},
	}
	renderer := NewPackageLocalComponentSSRRenderer()
	if err := renderer.Publish(extension); err != nil {
		t.Fatal(err)
	}
	artifact := HookArtifact{
		ExtensionID: "demo.seo.ssr", ExtensionVersion: "1.0.0",
		PackageDigest: packageDigest, RuntimeInstanceID: "host-component-package:demo.seo.ssr",
	}
	response, err := renderer.RenderComponent(context.Background(), ComponentRenderCall{
		TargetID: componentTestCoreTarget, Artifact: artifact,
		Contribution: ComponentContribution{
			ID: "demo.seo.ssr.component.replace", Action: extensionmanifest.ComponentActionReplace,
			SSRTemplate: "demo.seo.ssr.template.card", Artifact: artifact,
		},
		Props: map[string]any{"scope": "topic"}, Result: map[string]any{"html": "core"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Fragments) != 1 || response.Fragments[0].PrimaryContent {
		t.Fatalf("plugin fragment claimed primary SEO content: %#v", response.Fragments)
	}
}

func TestProductionPageCompositionKeepsPrimaryThemeHTMLOnPluginReplaceAndFailure(t *testing.T) {
	id := "production.seo.primary"
	packageDigest := strings.Repeat("8", 64)
	replace := componentTestContribution(
		id, "replace", extensionmanifest.ComponentActionReplace, 50,
		componentTestCoreTarget, componentTestCoreContract,
	)
	replace.SSRTemplate = id + ".template.replace"
	extension := componentTestExtension(t, id, extensions.TypePlugin, replace)
	extension.PackageDigest = packageDigest
	body := `<aside class="plugin-replace">{{index .Props "scope"}}</aside>`
	writePackageLocalTestFile(t, extension.PackagePath, "templates/replace.html", body)
	digest := sha256.Sum256([]byte(body))
	extension.Manifest.Templates = []extensions.ManifestTemplate{{
		ID: id + ".template.replace", ContractVersion: id + ".template.replace@1",
		Action: "add", Path: "templates/replace.html", Digest: hex.EncodeToString(digest[:]),
		ViewModelSchema: id + ".schema.props@1",
	}}

	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, componentPackageRuntimeInstanceID(extension)); err != nil {
		t.Fatal(err)
	}
	service, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishPackageSSR(extension); err != nil {
		t.Fatal(err)
	}

	// 生产 Compose：插件 replace 不得声明 PrimaryContent；主题 L1 由 Merge 保留。
	composed, err := service.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !segmentsHavePrimaryContent(composed.Segments) {
		t.Fatalf("core primary missing after replace: %#v", composed.Segments)
	}
	pluginHTML := componentNonCoreHTMLSegments(composed.Segments)
	if len(pluginHTML) == 0 {
		// replace 成功时 winner 可能是插件，主内容来自 fallback Core 段。
		// 再走 ComposePageHTML 映射 forum.home → core target。
		pluginHTML, err = service.ComposePageHTML(context.Background(), "forum.home", map[string]any{"scope": "home"})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 主题 L1 主段始终保留，即使插件 replace 了 component target。
	primary := []string{`<main data-theme="l1"><h1>Home</h1><a href="/t/1">Topic</a></main>`}
	// 若插件 HTML 为空，仍验证 Merge 保留 primary（composition 失败 fail-open 路径）。
	merged := pages.MergeCompositionHTMLSegments(primary, pluginHTML)
	if merged[len(merged)-1] != primary[0] && (len(pluginHTML) == 0 && len(merged) != 1) {
		t.Fatalf("primary theme stripped: %#v", merged)
	}
	joined := strings.Join(merged, "")
	if !strings.Contains(joined, "Home") || !strings.Contains(joined, "/t/1") {
		t.Fatalf("indexable primary missing: %#v", merged)
	}
	// 插件片段若出现，也不得单独成为唯一主内容。
	for _, segment := range composed.Segments {
		if segment.OwnerID != "" && segment.OwnerID != "core" && segment.PrimaryContent {
			t.Fatalf("plugin segment claimed primary: %#v", segment)
		}
	}

	// 插件失败 fail-open：ApplyPageComposition 保留主题输出。
	page := pages.ThemeRenderedPage{HTMLSegments: append([]string(nil), primary...)}
	applied, err := pages.ApplyPageComposition(
		context.Background(), page,
		stubPageCompositionFail{err: errors.New("plugin crash")},
		"forum.home", map[string]any{"scope": "home"},
	)
	if err != nil || len(applied.HTMLSegments) != 1 || applied.HTMLSegments[0] != primary[0] {
		t.Fatalf("fail-open primary lost: %#v err=%v", applied, err)
	}
	// SEO 围栏错误仍 fail closed。
	if _, err := pages.ApplyPageComposition(
		context.Background(), page,
		stubPageCompositionFail{err: pages.ErrPageCompositionSEO},
		"forum.home", nil,
	); !errors.Is(err, pages.ErrPageCompositionSEO) {
		t.Fatalf("SEO fence: %v", err)
	}
}

// stubPageCompositionFail 仅用于 SEO fail-open/fail-closed 矩阵。
type stubPageCompositionFail struct {
	err error
}

func (s stubPageCompositionFail) ComposePageHTML(context.Context, string, map[string]any) ([]string, error) {
	return nil, s.err
}

func TestProductionComponentCompositionUsesPackageLocalSSRByDefault(t *testing.T) {
	id := "production.package.ssr"
	packageDigest := strings.Repeat("c", 64)
	extension := componentTestExtension(t, id, extensions.TypePlugin,
		componentTestContribution(
			id, "before", extensionmanifest.ComponentActionBefore, 10,
			componentTestCoreTarget, componentTestCoreContract,
		),
	)
	extension.PackageDigest = packageDigest
	// 在 extension 自己的 PackagePath 写入 SSR 模板并同步 digest。
	body := `<div class="plugin-before">{{index .Props "scope"}}</div>`
	writePackageLocalTestFile(t, extension.PackagePath, "templates/default.html", body)
	digest := sha256.Sum256([]byte(body))
	extension.Manifest.Templates[0].Path = "templates/default.html"
	extension.Manifest.Templates[0].Digest = hex.EncodeToString(digest[:])

	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(extension, componentPackageRuntimeInstanceID(extension)); err != nil {
		t.Fatal(err)
	}
	service, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishPackageSSR(extension); err != nil {
		t.Fatal(err)
	}
	result, err := service.Compose(context.Background(), ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 应包含插件 before 片段（HTML 已消毒）与 Core primary。
	joined := flattenComponentHTML(result.Segments)
	if !strings.Contains(joined, "plugin-before") || !strings.Contains(joined, "home") {
		t.Fatalf("composed HTML=%q segments=%#v", joined, result.Segments)
	}
}

func writePackageLocalTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func flattenComponentHTML(segments []ComponentRenderSegment) string {
	var builder strings.Builder
	var walk func([]ComponentRenderSegment)
	walk = func(items []ComponentRenderSegment) {
		for _, item := range items {
			builder.WriteString(item.HTML)
			walk(item.Children)
		}
	}
	walk(segments)
	return builder.String()
}
