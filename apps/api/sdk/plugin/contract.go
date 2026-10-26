package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// Check 是一次契约检查的结果项。
type Check struct {
	// Code 稳定机器码，例如 "manifest.ok" / "event.unknown"。
	Code string `json:"code"`
	// Level: ok | warn | error
	Level string `json:"level"`
	// Message 人类可读说明。
	Message string `json:"message"`
	// Path 可选：相关文件或字段路径。
	Path string `json:"path,omitempty"`
}

// Report 是 extension test / contract 的完整报告。
type Report struct {
	Root     string                     `json:"root"`
	Manifest extensionmanifest.Manifest `json:"manifest"`
	Checks   []Check                    `json:"checks"`
	OK       bool                       `json:"ok"`
	Errors   int                        `json:"errors"`
	Warnings int                        `json:"warnings"`
}

// Options 控制契约检查深度。
type Options struct {
	// SkipBackendBinary 为 true 时不检查 backend 可执行文件是否存在。
	// 开发脚手架常只有 shell stub，CI fixture 应关闭此选项或提供真实二进制。
	SkipBackendBinary bool
	// RequireBackendBinary 在声明 backend 时要求 entry 文件存在且可执行。
	// 默认 true；与 SkipBackendBinary 互斥时 Skip 优先。
	RequireBackendBinary bool
}

// LoadAndTest 加载扩展包并运行契约检查。
func LoadAndTest(root string, opts Options) (Report, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	manifest, err := extensionmanifest.LoadPackage(abs)
	if err != nil {
		return Report{
			Root:   abs,
			OK:     false,
			Errors: 1,
			Checks: []Check{{
				Code:    "manifest.load_failed",
				Level:   "error",
				Message: err.Error(),
				Path:    abs,
			}},
		}, nil
	}
	return TestManifest(abs, manifest, opts), nil
}

// TestManifest 对已加载的 manifest 运行宿主契约检查（不重新读盘，除 backend 文件）。
func TestManifest(root string, manifest extensionmanifest.Manifest, opts Options) Report {
	// 默认要求 backend 二进制存在（可 Skip）。
	if !opts.SkipBackendBinary && !opts.RequireBackendBinary {
		opts.RequireBackendBinary = true
	}

	report := Report{
		Root:     root,
		Manifest: manifest,
		OK:       true,
		Checks:   []Check{},
	}
	add := func(level, code, message, path string) {
		report.Checks = append(report.Checks, Check{
			Code: code, Level: level, Message: message, Path: path,
		})
		switch level {
		case "error":
			report.Errors++
			report.OK = false
		case "warn":
			report.Warnings++
		}
	}

	add("ok", "manifest.ok", fmt.Sprintf("manifest valid: %s@%s (%s)", manifest.ID, manifest.Version, manifest.Type), extensionmanifest.ManifestFileName)
	settings := manifest.SettingsDocument
	add("ok", "settings.renderer",
		fmt.Sprintf("settings renderer: mode=%s layout=%s fields=%d tabs=%d actions=%d", settings.UI.Mode, settings.UI.Layout, len(settings.Fields), len(settings.UI.Tabs), len(settings.Actions)),
		"settings")
	if component := settings.UI.Component; component != nil {
		add("ok", "settings.component",
			fmt.Sprintf("settings component: %s (apiVersion=%d, entry=%s)", component.ID, component.APIVersion, component.Entry),
			"settings.ui.component")
	}
	for _, binding := range extensionmanifest.DeclaredAdminComponents(manifest) {
		if binding.Surface == "settings" {
			continue
		}
		component := binding.Component
		add("ok", "admin.page.component",
			fmt.Sprintf("admin page component: %s (apiVersion=%d, entry=%s)", component.ID, component.APIVersion, component.Entry),
			"admin.pages"+binding.PagePath)
	}

	// 能力：Validate 已校验 key；此处报告解析后的有效集（含推断）。
	keys, implied := extensionmanifest.ResolvedCapabilities(manifest)
	if len(keys) > 0 {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if implied[key] {
				parts = append(parts, key+" (implied)")
			} else {
				parts = append(parts, key)
			}
		}
		add("ok", "capabilities.resolved", "effective capabilities: "+strings.Join(parts, ", "), "capabilities")
	} else if manifest.Type == extensionmanifest.TypePlugin {
		add("ok", "capabilities.none", "no capabilities declared or implied", "capabilities")
	}

	// 事件 / hooks 必须落在宿主目录。
	for _, event := range extensionmanifest.DeclaredEvents(manifest) {
		def, ok := appevents.FindDefinition(event.Name)
		if !ok {
			add("error", "event.unknown", fmt.Sprintf("event %q is not in the host catalog", event.Name), "events")
			continue
		}
		if event.Kind != "" && event.Kind != def.Kind {
			add("error", "event.kind_mismatch",
				fmt.Sprintf("event %q declares kind %q but host catalog has %q", event.Name, event.Kind, def.Kind),
				"events")
			continue
		}
		add("ok", "event.known",
			fmt.Sprintf("event %q (%s, timeoutMs=%d, failurePolicy=%s)", event.Name, def.Kind, def.TimeoutMS, def.FailurePolicy),
			"events")
	}

	// 贡献点必须是宿主已知点（Validate 已查；再给可读报告）。
	pointIDs := map[string]struct{}{}
	for _, p := range extensionmanifest.ContributionPointDefinitions() {
		pointIDs[p.ID] = struct{}{}
	}
	for _, c := range manifest.Contributions {
		if _, ok := pointIDs[c.Point]; !ok {
			add("error", "contribution.unknown_point",
				fmt.Sprintf("contribution point %q is not registered on the host", c.Point),
				"contributions")
			continue
		}
		add("ok", "contribution.point_ok",
			fmt.Sprintf("contribution %q → %s", c.ID, c.Point),
			"contributions")
	}

	// Provider 槽位：未知槽位告警（宿主可能后续注册；mail 等为已知）。
	knownSlots := map[string]struct{}{}
	for _, slot := range KnownProviderSlots() {
		knownSlots[slot] = struct{}{}
	}
	for _, provider := range manifest.Providers {
		if _, ok := knownSlots[provider.Slot]; !ok {
			add("warn", "provider.unknown_slot",
				fmt.Sprintf("provider slot %q is not in the published host slot list", provider.Slot),
				"providers")
			continue
		}
		add("ok", "provider.slot_ok",
			fmt.Sprintf("provider %q on slot %s", provider.Label, provider.Slot),
			"providers")
	}

	// Jobs：仅语法/非空名（运行时再校验 EnqueueOwnJob）。
	for _, job := range manifest.Jobs {
		name := strings.TrimSpace(job.Name)
		if name == "" {
			add("error", "job.empty_name", "job entry has empty name", "jobs")
			continue
		}
		add("ok", "job.declared", "job kind declared: "+name, "jobs")
	}

	// Backend 入口文件。
	entry := strings.TrimSpace(manifest.Backend.Entry)
	if entry != "" {
		if manifest.Backend.RPC != "" && manifest.Backend.RPC != "hashicorp-go-plugin" {
			add("error", "backend.unsupported_rpc",
				fmt.Sprintf("backend rpc %q is not supported (want hashicorp-go-plugin)", manifest.Backend.RPC),
				"backend")
		} else {
			add("ok", "backend.rpc_ok", "backend rpc: hashicorp-go-plugin", "backend")
		}
		// 解析 entry 相对包根路径（安装时会放在 files/ 下，开发包直接相对根）。
		candidates := []string{
			filepath.Join(root, entry),
			filepath.Join(root, "files", entry),
		}
		var found string
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				found = candidate
				break
			}
		}
		if found == "" {
			if opts.SkipBackendBinary {
				add("warn", "backend.binary_missing",
					fmt.Sprintf("backend entry %q not found on disk (skipped as required)", entry),
					"backend.entry")
			} else if opts.RequireBackendBinary {
				add("error", "backend.binary_missing",
					fmt.Sprintf("backend entry %q not found under package root or files/", entry),
					"backend.entry")
			}
		} else {
			add("ok", "backend.binary_present", "backend entry found: "+relPath(root, found), "backend.entry")
			// 脚手架 shell stub 不是真正的 go-plugin 二进制；仅提示。
			if isScaffoldShellStub(found) {
				add("warn", "backend.scaffold_stub",
					"backend entry looks like a scaffold shell stub; build a real go-plugin binary before enable",
					"backend.entry")
			}
		}
	}

	// 公开主题必须提供免构建运行时契约；不再接受 Nuxt Layer。
	if manifest.Type == extensionmanifest.TypeTheme {
		hasContract := regularFile(filepath.Join(root, "theme.json")) || regularFile(filepath.Join(root, "files", "theme.json"))
		hasAssets := directoryExists(filepath.Join(root, "assets")) || directoryExists(filepath.Join(root, "files", "assets"))
		if !hasContract && !hasAssets {
			add("error", "theme.runtime_missing", "theme requires theme.json or assets/", "theme.json")
		} else {
			add("ok", "theme.runtime_present", "runtime theme contract found", "theme.json")
		}
	}

	// 显式 Manifest V3：package-local theme.json page template 必须能通过
	// 生产 BuildThemeRuntimeSnapshot 精确声明/摘要/契约预检（激活同路径）。
	if err := PreflightExactTemplateRuntime(root, manifest); err != nil {
		add("error", "template.runtime_exact", err.Error(), "theme.json")
	} else if extensionmanifest.EffectiveManifestVersion(manifest) == extensionmanifest.ManifestVersionV3 {
		contentRoot := resolveThemeContentRoot(root)
		if pkg, loadErr := pages.LoadThemePackage(contentRoot); loadErr == nil && themePackageHasPageTemplates(pkg) {
			add("ok", "template.runtime_exact", "exact template runtime preflight passed", "theme.json")
		}
	}

	// 稳定排序：error 先，再 warn，再 ok；同级按 code。
	sort.SliceStable(report.Checks, func(i, j int) bool {
		rank := func(level string) int {
			switch level {
			case "error":
				return 0
			case "warn":
				return 1
			default:
				return 2
			}
		}
		if rank(report.Checks[i].Level) != rank(report.Checks[j].Level) {
			return rank(report.Checks[i].Level) < rank(report.Checks[j].Level)
		}
		return report.Checks[i].Code < report.Checks[j].Code
	})

	return report
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func relPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func isScaffoldShellStub(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 4096 {
		return false
	}
	text := string(body)
	return strings.HasPrefix(text, "#!") &&
		(strings.Contains(text, "Build the HashiCorp go-plugin") ||
			strings.Contains(text, "printf "))
}

// 编译期引用：避免误删 mail 槽常量依赖时无感。
var _ = extensionsruntime.MailProviderSlot
