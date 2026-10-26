package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
)

type scaffoldManifest struct {
	ManifestVersion int                                  `json:"manifestVersion"`
	ID              string                               `json:"id"`
	Name            string                               `json:"name"`
	Description     string                               `json:"description"`
	URL             string                               `json:"url"`
	Author          extensionmanifest.ManifestAuthor     `json:"author"`
	Version         string                               `json:"version"`
	Type            string                               `json:"type"`
	SForumVersion   string                               `json:"sforumVersion"`
	Permissions     []string                             `json:"permissions,omitempty"`
	Settings        *extensionmanifest.SettingsDocument  `json:"settings,omitempty"`
	Providers       []extensionmanifest.ManifestProvider `json:"providers,omitempty"`
	Backend         *extensionmanifest.ManifestBackend   `json:"backend,omitempty"`
	Admin           extensionmanifest.ManifestAdmin      `json:"admin,omitempty"`
	Includes        map[string]string                    `json:"includes,omitempty"`
}

func GenerateExtensionScaffold(opts makeOptions) (string, error) {
	opts = normalizeMakeOptions(opts)
	if err := validateMakeOptions(opts); err != nil {
		return "", err
	}
	target, err := resolveOutputDir(opts)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(target, extensionmanifest.ManifestFileName)); err == nil {
		return "", fmt.Errorf("extension scaffold already exists at %s", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}

	if opts.Complex && opts.Kind == extensionmanifest.TypePlugin {
		if err := writeComplexPluginScaffold(target, opts); err != nil {
			return "", err
		}
	} else {
		manifest := buildManifest(opts)
		if err := writeJSON(filepath.Join(target, extensionmanifest.ManifestFileName), manifest); err != nil {
			return "", err
		}
	}
	if err := writeFile(filepath.Join(target, "README.md"), readmeBody(opts), 0o644); err != nil {
		return "", err
	}
	if opts.PrebuiltSettings {
		if err := writePrebuiltSettingsFiles(target, opts); err != nil {
			return "", err
		}
	}
	if opts.VueAdminPage {
		if err := writeVueAdminPageFiles(target, opts); err != nil {
			return "", err
		}
	}
	if opts.Kind == extensionmanifest.TypePlugin {
		if err := writePluginFiles(target, opts); err != nil {
			return "", err
		}
	} else if err := writeThemeFiles(target, opts); err != nil {
		return "", err
	}
	// 公开页会创建/合并 theme.json，因此必须在 writeThemeFiles 之后写入。
	if err := writePublicPageFiles(target, opts); err != nil {
		return "", err
	}
	// view=schema 页同样合并 theme.json，且与公开页共用「重复即拒绝」规则。
	if err := writeSchemaPageFiles(target, opts); err != nil {
		return "", err
	}
	if err := writeRouteMiddlewareFiles(target, opts); err != nil {
		return "", err
	}
	if err := finalizeGeneratedManifest(target, opts); err != nil {
		return "", err
	}
	return target, nil
}

func normalizeMakeOptions(opts makeOptions) makeOptions {
	opts.Kind = strings.ToLower(strings.TrimSpace(opts.Kind))
	opts.ID = strings.ToLower(strings.TrimSpace(opts.ID))
	opts.Name = strings.TrimSpace(opts.Name)
	opts.Description = strings.TrimSpace(opts.Description)
	opts.URL = strings.TrimSpace(opts.URL)
	opts.AuthorName = strings.TrimSpace(opts.AuthorName)
	opts.AuthorURL = strings.TrimSpace(opts.AuthorURL)
	opts.AuthorEmail = strings.TrimSpace(opts.AuthorEmail)
	opts.Out = strings.TrimSpace(opts.Out)
	opts.ProviderSlot = strings.TrimSpace(opts.ProviderSlot)
	opts = normalizeWorkflowOptions(opts)
	return opts
}

func validateMakeOptions(opts makeOptions) error {
	if opts.Kind != extensionmanifest.TypePlugin && opts.Kind != extensionmanifest.TypeTheme {
		return errors.New("kind must be plugin or theme")
	}
	if opts.ID == "" || opts.Name == "" || opts.Description == "" || opts.URL == "" || opts.AuthorName == "" {
		return errors.New("id, name, description, url, and author-name are required")
	}
	if opts.ProviderSlot != "" && (opts.Kind != extensionmanifest.TypePlugin || !opts.Backend) {
		return errors.New("provider-slot requires a plugin scaffold with --backend")
	}
	if opts.VueAdminPage && opts.Kind != extensionmanifest.TypePlugin {
		return errors.New("vue-admin-page requires a plugin scaffold")
	}
	return validateWorkflowOptions(opts)
}

func buildManifest(opts makeOptions) scaffoldManifest {
	manifest := scaffoldManifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              opts.ID,
		Name:            opts.Name,
		Description:     opts.Description,
		URL:             opts.URL,
		Author:          extensionmanifest.ManifestAuthor{Name: opts.AuthorName, URL: opts.AuthorURL, Email: opts.AuthorEmail},
		Version:         "0.1.0",
		Type:            opts.Kind,
		SForumVersion:   ">=0.1.0",
		Settings: scaffoldSettingsDocument(opts, []extensionmanifest.ManifestSetting{{
			Key:         opts.ID + ".enabled",
			Label:       extensionmanifest.LocalizedText{Default: "Enabled"},
			Description: extensionmanifest.LocalizedText{Default: "Enable this extension's recommended behavior."},
			Type:        "boolean",
			Default:     "true",
			GroupID:     "general",
		}}),
		Admin: scaffoldAdmin(opts),
	}
	if opts.Kind == extensionmanifest.TypePlugin {
		manifest.Permissions = []string{opts.ID + ".manage"}
		if opts.Backend {
			manifest.Backend = &extensionmanifest.ManifestBackend{
				Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
			}
		}
		if opts.ProviderSlot != "" {
			manifest.Providers = []extensionmanifest.ManifestProvider{{Slot: opts.ProviderSlot, Label: opts.Name, TimeoutMS: 15000}}
		}
		return manifest
	}
	return manifest
}

func scaffoldSettingsDocument(opts makeOptions, fields []extensionmanifest.ManifestSetting) *extensionmanifest.SettingsDocument {
	document := &extensionmanifest.SettingsDocument{
		SchemaVersion: extensionmanifest.SettingsSchemaVersion,
		Explicit:      true,
		UI: extensionmanifest.SettingsUI{
			Mode:   extensionmanifest.SettingsUIModeSchema,
			Layout: extensionmanifest.SettingsLayoutTabs,
			Tabs: []extensionmanifest.SettingsTab{{
				ID: "general", Label: extensionmanifest.LocalizedText{Default: "General", ByLocale: map[string]string{"zh-CN": "常规", "en-US": "General"}}, Groups: []string{"general"},
			}},
			Groups: []extensionmanifest.SettingsGroup{{
				ID: "general", Label: extensionmanifest.LocalizedText{Default: "General", ByLocale: map[string]string{"zh-CN": "常规", "en-US": "General"}}, Columns: 1,
			}},
			Callouts: []extensionmanifest.SettingsCallout{{
				ID: "recommended", Tone: "info",
				Title: extensionmanifest.LocalizedText{Default: "Start with the recommended defaults", ByLocale: map[string]string{"zh-CN": "建议先使用推荐默认值", "en-US": "Start with the recommended defaults"}},
				Tab:   "general",
			}},
		},
		Fields: fields,
	}
	if opts.ProviderSlot != "" {
		keys := make([]string, 0, len(fields))
		for _, field := range fields {
			keys = append(keys, field.Key)
		}
		document.Actions = []extensionmanifest.SettingsAction{{
			ID: "probe", Kind: extensionmanifest.SettingsActionProviderProbe,
			Label:     extensionmanifest.LocalizedText{Default: "Test connection", ByLocale: map[string]string{"zh-CN": "测试连接", "en-US": "Test connection"}},
			Placement: "footer", UseDraftValues: true, Fields: keys,
		}}
	}
	if opts.PrebuiltSettings {
		document.UI.Mode = extensionmanifest.SettingsUIModeComponent
		document.UI.Component = &extensionmanifest.SettingsComponent{
			ID: "settings", APIVersion: extensionmanifest.AdminMicroFrontendAPIVersion,
			Entry: "frontend/admin/dist/settings.mjs", CSS: "frontend/admin/dist/settings.css",
		}
	}
	return document
}

// writeComplexPluginScaffold 写出薄入口 + includes（langs 目录、settings 分片、admin）。
func writeComplexPluginScaffold(target string, opts makeOptions) error {
	root := scaffoldManifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              opts.ID,
		Name:            opts.Name,
		Description:     opts.Description,
		URL:             opts.URL,
		Author:          extensionmanifest.ManifestAuthor{Name: opts.AuthorName, URL: opts.AuthorURL, Email: opts.AuthorEmail},
		Version:         "0.1.0",
		Type:            extensionmanifest.TypePlugin,
		SForumVersion:   ">=0.1.0",
		Permissions:     []string{opts.ID + ".manage"},
		Includes: map[string]string{
			"langs":    "manifest/langs",
			"settings": "manifest/settings.json",
			"admin":    "manifest/admin.json",
		},
	}
	if opts.Backend {
		root.Backend = &extensionmanifest.ManifestBackend{
			Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: "sforum.host@2",
		}
	}
	if opts.ProviderSlot != "" {
		root.Providers = []extensionmanifest.ManifestProvider{{Slot: opts.ProviderSlot, Label: opts.Name, TimeoutMS: 15000}}
	}
	if err := writeJSON(filepath.Join(target, extensionmanifest.ManifestFileName), root); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(target, "manifest", "langs", "zh-CN.json"), map[string]any{
		"name":        opts.Name,
		"description": opts.Description,
		"author":      map[string]string{"name": opts.AuthorName},
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(target, "manifest", "langs", "en-US.json"), map[string]any{
		"name":        opts.Name,
		"description": opts.Description,
		"author":      map[string]string{"name": opts.AuthorName},
	}); err != nil {
		return err
	}
	settings := []extensionmanifest.ManifestSetting{{
		Key:         opts.ID + ".enabled",
		Label:       extensionmanifest.LocalizedText{Default: "Enabled", ByLocale: map[string]string{"zh-CN": "启用", "en-US": "Enabled"}},
		Description: extensionmanifest.LocalizedText{Default: "Enable this extension's recommended behavior.", ByLocale: map[string]string{"zh-CN": "启用此扩展的推荐行为。", "en-US": "Enable this extension's recommended behavior."}},
		Type:        "boolean",
		Default:     "true",
		GroupID:     "general",
	}, {
		Key:         opts.ID + ".debug",
		Label:       extensionmanifest.LocalizedText{Default: "Debug logging", ByLocale: map[string]string{"zh-CN": "调试日志", "en-US": "Debug logging"}},
		Description: extensionmanifest.LocalizedText{Default: "Write extra diagnostic logs.", ByLocale: map[string]string{"zh-CN": "输出额外诊断日志。", "en-US": "Write extra diagnostic logs."}},
		Type:        "boolean",
		Default:     "false",
		GroupID:     "general",
	}}
	if err := writeJSON(filepath.Join(target, "manifest", "settings.json"), scaffoldSettingsDocument(opts, settings)); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(target, "manifest", "admin.json"), scaffoldAdmin(opts)); err != nil {
		return err
	}
	// Prebuilt assets are written after the manifest shards. The finalizer
	// validates every scaffold once all exact-artifact files are present.
	if !opts.PrebuiltSettings && !opts.VueAdminPage {
		if _, err := extensionmanifest.LoadPackage(target); err != nil {
			return fmt.Errorf("generated complex package failed validation: %w", err)
		}
	}
	return nil
}

func resolveOutputDir(opts makeOptions) (string, error) {
	if opts.Out != "" {
		return filepath.Abs(opts.Out)
	}
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	source := "dev"
	if opts.Builtin {
		source = "builtin"
	}
	group := "plugins"
	if opts.Kind == extensionmanifest.TypeTheme {
		group = "themes"
	}
	return filepath.Join(root, "extensions", source, group, opts.ID), nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "extensions")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate SForum repository root")
		}
		dir = parent
	}
}

func writeJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return writeFile(path, string(body), 0o644)
}

func writeFile(path string, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), mode)
}

func writePluginFiles(target string, opts makeOptions) error {
	if !opts.Backend {
		return nil
	}
	if err := writeFile(filepath.Join(target, "backend", "README.md"), pluginBackendReadme(opts), 0o644); err != nil {
		return err
	}
	return writeFile(filepath.Join(target, "backend", "plugin"), "#!/usr/bin/env sh\nprintf 'Build the HashiCorp go-plugin server for "+opts.ID+" into this file.\\n'\n", 0o755)
}

func writePrebuiltSettingsFiles(target string, opts makeOptions) error {
	module := `export const apiVersion = 1

export function mount(target, bridge) {
  const root = document.createElement('section')
  root.className = 'sforum-extension-settings'
  const title = document.createElement('h3')
  title.textContent = bridge.locale.startsWith('zh') ? '自定义设置' : 'Custom settings'
  const input = document.createElement('input')
  input.type = 'checkbox'
  input.checked = bridge.settings.values()['` + opts.ID + `.enabled'] === 'true'
  const save = document.createElement('button')
  save.type = 'button'
  save.textContent = bridge.locale.startsWith('zh') ? '保存' : 'Save'
  const onChange = () => bridge.settings.updateValue('` + opts.ID + `.enabled', input.checked ? 'true' : 'false')
  const onSave = () => bridge.settings.save()
  input.addEventListener('change', onChange)
  save.addEventListener('click', onSave)
  root.append(title, input, save)
  target.append(root)
  return () => {
    input.removeEventListener('change', onChange)
    save.removeEventListener('click', onSave)
    root.remove()
  }
}
`
	css := `.sforum-extension-settings {
  display: grid;
  gap: 0.75rem;
  padding: 1rem;
  border: 1px solid color-mix(in srgb, var(--sf-accent) 24%, transparent);
}
`
	if err := writeFile(filepath.Join(target, "frontend", "admin", "dist", "settings.mjs"), module, 0o644); err != nil {
		return err
	}
	return writeFile(filepath.Join(target, "frontend", "admin", "dist", "settings.css"), css, 0o644)
}

func writeThemeFiles(target string, opts makeOptions) error {
	// Runtime L0/L1 主题包：不再生成 Nuxt Layer。
	files := map[string]string{
		"theme.json": `{
  "pages": [
    {
      "id": "` + opts.ID + `.home",
      "action": "replace",
      "target": "forum.home",
      "template": "templates/home.html",
      "contract": "sforum.page.home@1"
    }
  ],
  "skin": {
    "css": ["assets/theme.css"],
    "tokens": "assets/tokens.css"
  }
}

`,
		"assets/theme.css": `/* ` + opts.Name + ` L0 skin */
:root {
  --sf-theme-radius: 0.75rem;
}
`,
		"assets/tokens.css": `/* Design tokens */
:root {}
`,
		"templates/home.html": `<!-- ` + opts.ID + ` home template (L1)

主题页面自己拥有站点 chrome：恰好一个 navbar + 一个 footer，正文放在两者之间。
不要在正文区重复 navbar/footer（每个渲染路径只有一个 chrome 拥有者），也不要在这里
引用插件正文岛——那是主题外壳模板（theme.json 的 shell）的职责。
注意：模板里的 HTML 注释同样会被岛扫描器读取，所以注释里不要写岛标签本身。
-->
<div class="sf-page sf-page--home" data-page="forum.home">
  <sf-navbar></sf-navbar>
  <main class="sf-page__body">
    <sf-home-page></sf-home-page>
  </main>
  <sf-footer></sf-footer>
</div>
`,
		"README.md": "# " + opts.Name + "\n\nRuntime theme package (L0 skin + L1 templates).\n\nActivate without rebuilding Nuxt. See `docs/extensions/page-catalog.md`.\n",
	}
	for rel, body := range files {
		if err := writeFile(filepath.Join(target, rel), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func finalizeGeneratedManifest(target string, opts makeOptions) error {
	manifestPath := filepath.Join(target, extensionmanifest.ManifestFileName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return err
	}
	root["manifestVersion"] = extensionmanifest.ManifestVersionV3
	packageFiles := make([]map[string]any, 0)
	// version 为空时不写 version 字段：模板/资源沿用既有形状，只有精确 schema 引用需要版本。
	addVersionedPackageFile := func(id string, kind string, relative string, version string) (string, error) {
		digest, err := digestScaffoldFile(filepath.Join(target, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		entry := map[string]any{
			"id": opts.ID + ".file." + id, "kind": kind, "path": relative, "digest": digest,
		}
		if version != "" {
			entry["version"] = version
		}
		packageFiles = append(packageFiles, entry)
		return digest, nil
	}
	addPackageFile := func(id string, kind string, relative string) (string, error) {
		return addVersionedPackageFile(id, kind, relative, "")
	}

	if backend, ok := root["backend"].(map[string]any); ok {
		if entry, _ := backend["entry"].(string); entry != "" {
			digest, err := addPackageFile("backend", "executable", entry)
			if err != nil {
				return err
			}
			backend["digest"] = digest
		}
	}
	if providers, ok := root["providers"].([]any); ok {
		for index, raw := range providers {
			provider, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id := opts.ID + ".provider." + fmt.Sprintf("%d", index+1)
			provider["id"] = id
			provider["contractVersion"] = id + "@1"
			provider["handler"] = "provider.handle"
		}
	}
	if permissions, ok := root["permissions"].([]any); ok {
		definitions := make([]map[string]any, 0, len(permissions))
		for _, raw := range permissions {
			permission, _ := raw.(string)
			if permission == "" {
				continue
			}
			definitions = append(definitions, map[string]any{
				"key": permission, "contractVersion": permission + "@1",
				"label": "Manage " + opts.Name, "description": "Manage this extension.",
				"recommendedRoles": []string{"administrator"}, "assignmentPolicy": "host",
			})
		}
		root["permissionDefinitions"] = definitions
	}
	if opts.PrebuiltSettings {
		if _, err := addPackageFile("admin-settings-entry", "frontend", "frontend/admin/dist/settings.mjs"); err != nil {
			return err
		}
		if _, err := addPackageFile("admin-settings-style", "asset", "frontend/admin/dist/settings.css"); err != nil {
			return err
		}
	}
	if opts.VueAdminPage {
		if _, err := addPackageFile("admin-dashboard-entry", "frontend", "frontend/admin/dist/dashboard.mjs"); err != nil {
			return err
		}
		if _, err := addPackageFile("admin-dashboard-style", "asset", "frontend/admin/dist/dashboard.css"); err != nil {
			return err
		}
	}
	if opts.Kind == extensionmanifest.TypeTheme {
		templateDigest, err := addPackageFile("template-home", "template", "templates/home.html")
		if err != nil {
			return err
		}
		themeDigest, err := addPackageFile("asset-theme", "asset", "assets/theme.css")
		if err != nil {
			return err
		}
		tokensDigest, err := addPackageFile("asset-tokens", "asset", "assets/tokens.css")
		if err != nil {
			return err
		}
		templateID := opts.ID + ".template.home"
		root["templates"] = []map[string]any{{
			"id": templateID, "contractVersion": templateID + "@1", "action": "add",
			"path": "templates/home.html", "digest": templateDigest,
			"viewModelSchema": "sforum.page.home@1", "themeOverrideKey": opts.ID + ".home",
		}}
		root["assets"] = []map[string]any{
			{"handle": opts.ID + ".asset.theme", "contractVersion": opts.ID + ".asset.theme@1", "type": "style", "path": "assets/theme.css", "digest": themeDigest},
			{"handle": opts.ID + ".asset.tokens", "contractVersion": opts.ID + ".asset.tokens@1", "type": "style", "path": "assets/tokens.css", "digest": tokensDigest},
		}
	}
	// M3 作者工作流声明（公开页模板 + before/after 中间件路由）。
	if err := applyWorkflowManifest(root, opts, addPackageFile, addVersionedPackageFile); err != nil {
		return err
	}
	root["packageFiles"] = packageFiles
	if err := writeJSON(manifestPath, root); err != nil {
		return err
	}
	if _, err := extensionmanifest.LoadPackage(target); err != nil {
		return fmt.Errorf("generated V3 package failed validation: %w", err)
	}
	return nil
}

func digestScaffoldFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func readmeBody(opts makeOptions) string {
	body := "# " + opts.Name + "\n\n" + opts.Description + "\n\n- ID: `" + opts.ID + "`\n- Type: `" + opts.Kind + "`\n- Manifest contract: `sforum.manifest@3`\n- Website: " + opts.URL + "\n"
	if opts.Complex && opts.Kind == extensionmanifest.TypePlugin {
		body += "\n## Multi-file manifest\n\n"
		body += "This package uses a thin `sforum.extension.json` plus `includes`:\n\n"
		body += "- `manifest/langs/{zh-CN,en-US}.json` — identity translations (filename = locale)\n"
		body += "- `manifest/settings.json` — versioned Settings Document (Schema UI by default)\n"
		body += "- `manifest/admin.json` — admin entry/pages\n\n"
		body += "Validate with:\n\n```bash\ncd apps/api && go run ./cmd/sforum extension validate " + filepath.ToSlash(opts.Out) + "\n```\n\n"
		body += "Identity langs and settings `LocalizedText` stay separate. Prebuilt components receive locale through the host bridge.\n"
	}
	if opts.PrebuiltSettings {
		body += "\n## Prebuilt settings component\n\n"
		body += "`frontend/admin/dist/settings.mjs` implements Admin Micro-frontend API v1 and is loaded only after exact digest trust. SForum never compiles source SFCs. Keep the Schema fields as the required fallback and build all component bytes before packaging.\n"
	}
	if opts.VueAdminPage {
		body += "\n## Vue admin page\n\n"
		body += "`frontend/admin/src/AdminDashboard.vue` uses `@sforum/plugin-ui` and compiles to the trusted page-body contract. The Host still owns the admin sidebar, topbar, tabs, heading, route guard, and permissions.\n\n"
		body += "```bash\nsforum extension build --allow-scaffold .\n```\n\n"
		body += "The author-side build command installs and builds `frontend/admin`, refreshes exact digests, validates the package, and runs contract tests. Use `--skip-install` after dependencies are already installed.\n\n"
		body += "Production packages load only `dist/dashboard.mjs` and `dist/dashboard.css`; SForum does not compile the Vue source or load this workspace as a Nuxt Layer.\n"
	}
	if opts.ProviderSlot != "" {
		body += "\n## Settings action\n\nThe manifest declares a host-rendered `provider_probe` for `" + opts.ProviderSlot + "`. Implement the SDK `ProviderProbe` method in the backend; the host enforces field/secret allowlists, timeout, audit, and disabled-plugin restricted execution.\n"
	}
	if opts.Kind == extensionmanifest.TypePlugin {
		body += "\n## Optional Contribution Example\n\n"
		body += "The generated manifest does not enable demo contributions by default. After you implement and declare the matching extension route, you can add a host-rendered topic action like this:\n\n"
		body += "```json\n"
		body += "{\n"
		body += "  \"routes\": [{\"id\": \"" + opts.ID + ".route.bookmark\", \"contractVersion\": \"" + opts.ID + ".route.bookmark@1\", \"action\": \"add\", \"path\": \"/topic-actions/bookmark\", \"methods\": [\"POST\"], \"guard\": \"core.guard.login\", \"fallback\": \"closed\", \"mode\": \"http\", \"handler\": \"route.bookmark\", \"requestSchema\": \"" + opts.ID + ".route.bookmark.request@1\", \"responseSchema\": \"" + opts.ID + ".route.bookmark.response@1\"}],\n"
		body += "  \"contributions\": [\n"
		body += "    {\n"
		body += "      \"point\": \"forum.topic.actions\",\n"
		body += "      \"id\": \"" + opts.ID + ".bookmark\",\n"
		body += "      \"order\": 200,\n"
		body += "      \"label\": {\"zh-CN\": \"收藏\", \"en-US\": \"Bookmark\"},\n"
		body += "      \"icon\": \"i-lucide-bookmark\",\n"
		body += "      \"payload\": {\"type\": \"extensionRoute\", \"method\": \"POST\", \"path\": \"/topic-actions/bookmark\", \"confirm\": true}\n"
		body += "    }\n"
		body += "  ]\n"
		body += "}\n"
		body += "```\n"
	}
	return body + workflowReadme(opts)
}

func pluginBackendReadme(opts makeOptions) string {
	return "# Backend Stub\n\nBuild a HashiCorp go-plugin compatible executable named `plugin` in this directory before enabling `" + opts.ID + "`.\n\n" +
		"Use the Protocol V2 public Go SDK:\n\n```go\npackage main\n\nimport pluginv2 \"github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2\"\n\nfunc main() { pluginv2.Serve(pluginv2.NewServer()) }\n```\n\n" +
		"After replacing the generated stub, refresh the exact file digests:\n\n```bash\ncd apps/api && go run ./cmd/sforum extension digest --write <package-root>\n```\n\n" +
		"Contract test (no binary required while scaffolding):\n\n```bash\ncd apps/api && go run ./cmd/sforum extension test --allow-scaffold <package-root>\n```\n"
}
