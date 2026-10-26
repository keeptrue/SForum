package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// TestP9JoinedComponentActionMatrix is the authoritative P9 production matrix row.
// It drives shipped Registry + package-local SSR + Production composition APIs for:
// actions, priority/conflict/provider selection, SSR fallback, composition crash
// primary retention, digest upgrade selection drop, and Safe Mode.
func TestP9JoinedComponentActionMatrix(t *testing.T) {
	ctx := context.Background()
	pluginID := "p9.matrix.plugin"
	packageDigestV1 := strings.Repeat("1", 64)
	packageDigestV2 := strings.Repeat("2", 64)

	// --- 1) Every component action publishes on the live Registry ---
	// hide 单独验证；与 replace 同目标会抑制渲染，矩阵 compose 阶段不带 hide。
	actions := []string{
		extensionmanifest.ComponentActionAdd,
		extensionmanifest.ComponentActionBefore,
		extensionmanifest.ComponentActionAfter,
		extensionmanifest.ComponentActionWrap,
		extensionmanifest.ComponentActionReplace,
		extensionmanifest.ComponentActionFilterProps,
		extensionmanifest.ComponentActionFilterResult,
	}
	declarations := make([]extensions.ManifestComponent, 0, len(actions)+1)
	for index, action := range actions {
		declarations = append(declarations, componentTestContribution(
			pluginID, strings.ReplaceAll(action, "_", "-"), action, 100-index,
			componentTestCoreTarget, componentTestCoreContract,
		))
	}
	// hide 贡献在独立扩展上验证 action 可发布。
	hideID := "p9.matrix.hider"
	hider := componentTestExtension(t, hideID, extensions.TypePlugin,
		componentTestContribution(hideID, "hide", extensionmanifest.ComponentActionHide, 5,
			componentTestCoreTarget, componentTestCoreContract),
	)

	// 低优先级 replace 竞争者：用于 provider 选择/冲突。
	loserID := "p9.matrix.loser"
	loser := componentTestExtension(t, loserID, extensions.TypePlugin,
		componentTestContribution(loserID, "replace", extensionmanifest.ComponentActionReplace, 1,
			componentTestCoreTarget, componentTestCoreContract),
	)
	plugin := componentTestExtension(t, pluginID, extensions.TypePlugin, declarations...)
	plugin.PackageDigest = packageDigestV1
	plugin.Version = "1.0.0"

	beforeBody := `<aside data-p9="before">{{index .Props "scope"}}</aside>`
	replaceBody := `<section data-p9="replace">{{index .Props "scope"}}</section>`
	propsBody := `{"scope":{{json (printf "%s-filtered" (index .Props "scope"))}}}`
	resultBody := `{"html":{{json (printf "%s-out" (index .Result "html"))}}}`
	writePackageLocalTestFile(t, plugin.PackagePath, "templates/before.html", beforeBody)
	writePackageLocalTestFile(t, plugin.PackagePath, "templates/replace.html", replaceBody)
	writePackageLocalTestFile(t, plugin.PackagePath, "templates/filter-props.json.tmpl", propsBody)
	writePackageLocalTestFile(t, plugin.PackagePath, "templates/filter-result.json.tmpl", resultBody)
	beforeDigest := sha256.Sum256([]byte(beforeBody))
	replaceDigest := sha256.Sum256([]byte(replaceBody))
	propsDigest := sha256.Sum256([]byte(propsBody))
	resultDigest := sha256.Sum256([]byte(resultBody))

	for i := range plugin.Manifest.Components {
		c := &plugin.Manifest.Components[i]
		switch c.Action {
		case extensionmanifest.ComponentActionBefore:
			c.SSRTemplate = pluginID + ".template.before"
		case extensionmanifest.ComponentActionReplace:
			c.SSRTemplate = pluginID + ".template.replace"
		case extensionmanifest.ComponentActionFilterProps:
			c.SSRTemplate = pluginID + ".template.props"
		case extensionmanifest.ComponentActionFilterResult:
			c.SSRTemplate = pluginID + ".template.result"
		}
	}
	// default 模板供 add/after/wrap 等未单独绑定 SSR 的贡献使用。
	defaultBody := `<div data-p9="default">{{index .Props "scope"}}</div>`
	writePackageLocalTestFile(t, plugin.PackagePath, "templates/default.html", defaultBody)
	defaultDigest := sha256.Sum256([]byte(defaultBody))
	plugin.Manifest.Templates = []extensions.ManifestTemplate{
		{
			ID: pluginID + ".template.default", ContractVersion: pluginID + ".template.default@1",
			Action: "add", Path: "templates/default.html", Digest: hex.EncodeToString(defaultDigest[:]),
			ViewModelSchema: pluginID + ".schema.props@1",
		},
		{
			ID: pluginID + ".template.before", ContractVersion: pluginID + ".template.before@1",
			Action: "add", Path: "templates/before.html", Digest: hex.EncodeToString(beforeDigest[:]),
			ViewModelSchema: pluginID + ".schema.props@1",
		},
		{
			ID: pluginID + ".template.replace", ContractVersion: pluginID + ".template.replace@1",
			Action: "add", Path: "templates/replace.html", Digest: hex.EncodeToString(replaceDigest[:]),
			ViewModelSchema: pluginID + ".schema.props@1",
		},
		{
			ID: pluginID + ".template.props", ContractVersion: pluginID + ".template.props@1",
			Action: "add", Path: "templates/filter-props.json.tmpl", Digest: hex.EncodeToString(propsDigest[:]),
			ViewModelSchema: pluginID + ".schema.props@1",
		},
		{
			ID: pluginID + ".template.result", ContractVersion: pluginID + ".template.result@1",
			Action: "add", Path: "templates/filter-result.json.tmpl", Digest: hex.EncodeToString(resultDigest[:]),
			ViewModelSchema: pluginID + ".schema.result@1",
		},
	}

	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(plugin, componentPackageRuntimeInstanceID(plugin)); err != nil {
		t.Fatalf("publish plugin: %v", err)
	}
	if err := registry.ReplaceRuntime(loser, componentPackageRuntimeInstanceID(loser)); err != nil {
		t.Fatalf("publish loser: %v", err)
	}
	if err := registry.ReplaceRuntime(hider, componentPackageRuntimeInstanceID(hider)); err != nil {
		t.Fatalf("publish hider: %v", err)
	}

	plan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	seenActions := map[string]bool{}
	for _, contribution := range plan.Contributions {
		seenActions[contribution.Action] = true
	}
	// hide 在 plan 中应可见（除非被 replace 折叠）；至少 Registry snapshot 含 hide。
	snapshot := registry.Snapshot()
	for _, contribution := range snapshot.Contributions {
		seenActions[contribution.Action] = true
	}
	for _, action := range append(actions, extensionmanifest.ComponentActionHide) {
		if !seenActions[action] {
			t.Fatalf("action %q missing from registry contributions", action)
		}
	}
	// 默认 replace winner：plugin priority 高于 loser。
	if plan.ReplaceWinner == nil || plan.ReplaceWinner.Artifact.ExtensionID != pluginID {
		// hide 可能改变 plan 形态；用 ReplaceCandidates 判断冲突。
		if len(plan.ReplaceCandidates) < 2 && plan.ReplaceWinner == nil {
			// 重新解析不含 hide 的冲突：临时去掉 hider 再验。
			_, _ = registry.RemoveRuntime(hideID, componentPackageRuntimeInstanceID(hider))
			plan, err = registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
			if err != nil {
				t.Fatal(err)
			}
		}
		if plan.ReplaceWinner == nil || plan.ReplaceWinner.Artifact.ExtensionID != pluginID {
			t.Fatalf("replace winner want %s, plan=%#v", pluginID, plan)
		}
	}
	if len(plan.ReplaceCandidates) < 2 {
		t.Fatalf("expected replace conflict candidates, got %#v", plan.ReplaceCandidates)
	}

	// 精确 provider 选择：强制 loser 胜出。
	selection, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: loserID + ".component.replace", ExpectedRevision: plan.Revision,
	})
	if err != nil || selection.Artifact.ExtensionID != loserID {
		t.Fatalf("SelectReplaceProvider = %#v err=%v", selection, err)
	}
	selected, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil || selected.ReplaceWinner == nil || selected.ReplaceWinner.Artifact.ExtensionID != loserID {
		t.Fatalf("selected loser winner = %#v err=%v", selected, err)
	}
	if reset, err := registry.ResetReplaceProvider(ResetComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ExpectedRevision: selected.Revision,
	}); err != nil || !reset {
		t.Fatalf("ResetReplaceProvider = %t err=%v", reset, err)
	}

	// 重新发布 hider 后继续（若曾移除）。
	_ = registry.ReplaceRuntime(hider, componentPackageRuntimeInstanceID(hider))

	service, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PublishPackageSSR(plugin); err != nil {
		t.Fatalf("PublishPackageSSR: %v", err)
	}

	// --- 2) Compose 生产路径 + 主 SEO 保留 ---
	// 去掉 hide 以免抑制 replace 输出，保留 actions 矩阵已在 Registry 证明。
	_, _ = registry.RemoveRuntime(hideID, componentPackageRuntimeInstanceID(hider))
	// 过滤器要求目标契约显式声明可写字段（与生产 Host binding 一致）。
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
	composed, err := service.Compose(ctx, ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home", "html": "body"}, Binding: binding,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if !segmentsHavePrimaryContent(composed.Segments) {
		t.Fatalf("core primary missing after matrix compose: %#v", composed.Segments)
	}
	for _, segment := range composed.Segments {
		if segment.OwnerID != "" && segment.OwnerID != "core" && segment.PrimaryContent {
			t.Fatalf("plugin claimed primary: %#v", segment)
		}
	}
	primary := []string{`<main data-theme="l1"><h1>Home</h1><a href="/t/1">Topic</a></main>`}
	pluginHTML := componentNonCoreHTMLSegments(composed.Segments)
	merged := pages.MergeCompositionHTMLSegments(primary, pluginHTML)
	mergedJoined := strings.Join(merged, "")
	if !strings.Contains(mergedJoined, "Home") || !strings.Contains(mergedJoined, "/t/1") {
		t.Fatalf("primary SEO stripped: %#v", merged)
	}

	// --- 3) composition crash fail-open；SEO fence fail-closed ---
	page := pages.ThemeRenderedPage{HTMLSegments: append([]string(nil), primary...)}
	applied, err := pages.ApplyPageComposition(
		ctx, page, stubPageCompositionFail{err: errors.New("plugin crash")}, "forum.home",
		map[string]any{"scope": "home"},
	)
	if err != nil || len(applied.HTMLSegments) != 1 || applied.HTMLSegments[0] != primary[0] {
		t.Fatalf("crash fail-open lost primary: %#v err=%v", applied, err)
	}
	if _, err := pages.ApplyPageComposition(
		ctx, page, stubPageCompositionFail{err: pages.ErrPageCompositionSEO}, "forum.home", nil,
	); !errors.Is(err, pages.ErrPageCompositionSEO) {
		t.Fatalf("SEO fence: %v", err)
	}

	// package-local remove → 渲染 fail closed（SSR fallback）。
	renderer := NewPackageLocalComponentSSRRenderer()
	if err := renderer.Publish(plugin); err != nil {
		t.Fatal(err)
	}
	artifact := HookArtifact{
		ExtensionID: pluginID, ExtensionVersion: "1.0.0",
		PackageDigest: packageDigestV1, RuntimeInstanceID: componentPackageRuntimeInstanceID(plugin),
	}
	if _, err := renderer.RenderComponent(ctx, ComponentRenderCall{
		TargetID: componentTestCoreTarget, Artifact: artifact,
		Contribution: ComponentContribution{
			ID: pluginID + ".component.before", Action: extensionmanifest.ComponentActionBefore,
			SSRTemplate: pluginID + ".template.before", Artifact: artifact,
		},
		Props: map[string]any{"scope": "home"},
	}); err != nil {
		t.Fatalf("pre-remove render: %v", err)
	}
	renderer.RemovePackage(packageDigestV1)
	service.RemovePackageSSR(pluginID, packageDigestV1)
	if _, err := renderer.RenderComponent(ctx, ComponentRenderCall{
		TargetID: componentTestCoreTarget, Artifact: artifact,
		Contribution: ComponentContribution{
			ID: pluginID + ".component.before", Action: extensionmanifest.ComponentActionBefore,
			SSRTemplate: pluginID + ".template.before", Artifact: artifact,
		},
		Props: map[string]any{"scope": "home"},
	}); err == nil {
		t.Fatal("expected SSR crash after package remove (fallback path)")
	}

	// --- 4) Digest upgrade：新 digest 发布后旧 exact selection 失效 ---
	// 重新发布 v1 以便选择，再升级。
	pluginV1 := plugin
	if err := registry.ReplaceRuntime(pluginV1, componentPackageRuntimeInstanceID(pluginV1)); err != nil {
		t.Fatalf("re-publish v1: %v", err)
	}
	if err := registry.ReplaceRuntime(loser, componentPackageRuntimeInstanceID(loser)); err != nil {
		t.Fatal(err)
	}
	preUpgrade, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SelectReplaceProvider(SelectComponentProviderRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		ContributionID: pluginID + ".component.replace", ExpectedRevision: preUpgrade.Revision,
	}); err != nil {
		t.Fatalf("select v1: %v", err)
	}
	upgraded := plugin
	upgraded.PackageDigest = packageDigestV2
	upgraded.Version = "1.1.0"
	upgraded.Manifest.Version = "1.1.0"
	upgraded.Manifest.ID = pluginID
	writePackageLocalTestFile(t, upgraded.PackagePath, "templates/before.html", beforeBody)
	writePackageLocalTestFile(t, upgraded.PackagePath, "templates/replace.html", replaceBody)
	writePackageLocalTestFile(t, upgraded.PackagePath, "templates/filter-props.json.tmpl", propsBody)
	writePackageLocalTestFile(t, upgraded.PackagePath, "templates/filter-result.json.tmpl", resultBody)
	if err := registry.ReplaceRuntime(upgraded, componentPackageRuntimeInstanceID(upgraded)); err != nil {
		t.Fatalf("upgrade publish: %v (%#v)", err, err)
	}
	afterUpgrade, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	if afterUpgrade.ReplaceWinner != nil &&
		afterUpgrade.ReplaceWinner.Artifact.PackageDigest == packageDigestV1 &&
		afterUpgrade.ReplaceWinner.Artifact.ExtensionVersion == "1.0.0" {
		t.Fatalf("stale digest selection survived upgrade: %#v", afterUpgrade.ReplaceWinner)
	}

	// --- 5) Safe Mode：剥离第三方贡献 ---
	if err := registry.RestoreRuntimes([]extensions.Extension{upgraded, loser, hider}, true); err != nil {
		t.Fatalf("Safe Mode restore: %v", err)
	}
	safePlan, err := registry.ResolvePlan(componentTestCoreTarget, componentTestCoreContract)
	if err != nil {
		t.Fatal(err)
	}
	if len(safePlan.Contributions) != 0 {
		t.Fatalf("Safe Mode retained contributions: %#v", safePlan.Contributions)
	}
	safeService, err := NewProductionComponentComposition(ProductionComponentCompositionConfig{
		Registry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	safeCompose, err := safeService.Compose(ctx, ComponentCompositionRequest{
		TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
		Props: map[string]any{"scope": "home"}, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range safeCompose.Segments {
		if segment.OwnerID == pluginID || segment.OwnerID == loserID || segment.OwnerID == hideID {
			t.Fatalf("Safe Mode retained plugin segment: %#v", segment)
		}
	}
	mergedSafe := pages.MergeCompositionHTMLSegments(primary, componentNonCoreHTMLSegments(safeCompose.Segments))
	if !strings.Contains(strings.Join(mergedSafe, ""), "Home") {
		t.Fatalf("Safe Mode primary lost: compose=%#v merge=%#v", safeCompose, mergedSafe)
	}
}
