package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

func newExtensionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extension",
		Short: "Extension package helpers",
	}
	cmd.AddCommand(newExtensionValidateCommand())
	cmd.AddCommand(newExtensionDevCommand())
	cmd.AddCommand(newExtensionDevTrustCommand())
	cmd.AddCommand(newExtensionBuiltinsCommand())
	cmd.AddCommand(newExtensionBuildCommand())
	cmd.AddCommand(newExtensionDigestCommand())
	cmd.AddCommand(newExtensionTestCommand())
	cmd.AddCommand(newExtensionPackageCommand())
	cmd.AddCommand(newExtensionBrandCommand())
	cmd.AddCommand(newExtensionDocsCommand())
	cmd.AddCommand(newExtensionRecoveryListCommand())
	cmd.AddCommand(newExtensionRecoveryDisableCommand())
	cmd.AddCommand(newExtensionRecoveryDisableAllCommand())
	cmd.AddCommand(newExtensionRecoveryQuarantineCommand())
	cmd.AddCommand(newExtensionRecoveryCommand())
	cmd.AddCommand(newExtensionGCCommand())
	cmd.AddCommand(newPluginCommandCommand())
	cmd.AddCommand(newExtensionAPILTSCommand())
	cmd.AddCommand(newExtensionSystemTierCommand())
	return cmd
}

func newExtensionValidateCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Load and validate an extension package (resolves includes)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runExtensionValidate(cmd, root, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print merged manifest JSON")
	return cmd
}

func resolveExtensionPackageRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}

func loadValidatedExtensionPackage(root string) (string, extensionmanifest.Manifest, error) {
	abs, err := resolveExtensionPackageRoot(root)
	if err != nil {
		return "", extensionmanifest.Manifest{}, err
	}
	manifest, err := extensionmanifest.LoadPackage(abs)
	if err != nil {
		return "", extensionmanifest.Manifest{}, fmt.Errorf("invalid package at %s: %w", abs, err)
	}
	// 显式 V3 包复用生产预检，拒绝激活时会失败的 page template。
	if err := pluginsdk.PreflightExactTemplateRuntime(abs, manifest); err != nil {
		return "", extensionmanifest.Manifest{}, fmt.Errorf("invalid package at %s: %w", abs, err)
	}
	// 路由 schema 目录：启动/启用期会为扩展构建它，片段不完整会让 API **启动失败**。
	// 若 CLI 不构建，作者看到的是「validate/test 全绿、装上去 API 起不来」——
	// 2026-10-16 实测踩到过：路由声明 responseSchema 但 OpenAPI 片段无法解析该 schema 时，
	// dev API 直接起不来。这里补上同一套校验，让作者在 CLI 阶段就拿到可理解的失败。
	if err := preflightRouteSchemaCatalog(abs, manifest); err != nil {
		return "", extensionmanifest.Manifest{}, fmt.Errorf("invalid package at %s: %w", abs, err)
	}
	return abs, manifest, nil
}

// preflightRouteSchemaCatalog 构建扩展的路由 schema 目录，与启动期同一实现。
// 只有声明了 OpenAPI 片段或路由的包才需要检查；其余包没有任何 schema 可解析。
func preflightRouteSchemaCatalog(abs string, manifest extensionmanifest.Manifest) error {
	if len(manifest.OpenAPI) == 0 && len(manifest.Routes) == 0 {
		return nil
	}
	digest, err := extensionpackage.DigestAuthorTree(abs)
	if err != nil {
		return fmt.Errorf("package digest: %w", err)
	}
	if _, err := extensionopenapi.BuildRouteSchemaCatalog(extensionopenapi.BuildInput{
		Artifacts: []extensionopenapi.Artifact{{
			Root: abs, ExtensionID: manifest.ID, Version: manifest.Version,
			PackageDigest: digest, Manifest: manifest,
		}},
	}); err != nil {
		return fmt.Errorf("route schema catalog: %w", err)
	}
	return nil
}

func runExtensionValidate(cmd *cobra.Command, root string, asJSON bool) error {
	abs, manifest, err := loadValidatedExtensionPackage(root)
	if err != nil {
		return err
	}
	if asJSON {
		// 打印合并后的完整 Manifest，便于审查与调试 includes。
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(manifest)
	}
	printValidateSummary(cmd, abs, manifest)
	return nil
}

func printValidateSummary(cmd *cobra.Command, root string, manifest extensionmanifest.Manifest) {
	cmd.Printf("OK  %s\n", root)
	cmd.Printf("  id:      %s\n", manifest.ID)
	cmd.Printf("  type:    %s\n", manifest.Type)
	cmd.Printf("  version: %s\n", manifest.Version)
	cmd.Printf("  contract: %s\n", extensionmanifest.ManifestContract(manifest))
	cmd.Printf("  name:    %s\n", manifest.Name)

	locales := make([]string, 0, len(manifest.Langs))
	for key := range manifest.Langs {
		locales = append(locales, key)
	}
	sort.Strings(locales)
	if len(locales) == 0 {
		cmd.Printf("  langs:   (none; using root identity defaults)\n")
	} else {
		cmd.Printf("  langs:   %s\n", strings.Join(locales, ", "))
	}

	cmd.Printf("  settings:       %d\n", len(manifest.Settings))
	cmd.Printf("  settings.ui:    mode=%s layout=%s tabs=%d actions=%d\n",
		manifest.SettingsDocument.UI.Mode,
		manifest.SettingsDocument.UI.Layout,
		len(manifest.SettingsDocument.UI.Tabs),
		len(manifest.SettingsDocument.Actions),
	)
	if component := manifest.SettingsDocument.UI.Component; component != nil {
		cmd.Printf("  settings.ui.component: id=%s apiVersion=%d entry=%s\n", component.ID, component.APIVersion, component.Entry)
	}
	cmd.Printf("  contributions:  %d\n", len(manifest.Contributions))
	cmd.Printf("  routes:         %d\n", len(manifest.Routes))
	cmd.Printf("  events:         %d\n", len(manifest.Events))
	cmd.Printf("  providers:      %d\n", len(manifest.Providers))
	cmd.Printf("  permissions:    %d\n", len(manifest.Permissions))
	if extensionmanifest.EffectiveManifestVersion(manifest) == extensionmanifest.ManifestVersionV3 {
		cmd.Printf("  v3.packageFiles: %d\n", len(manifest.PackageFiles))
		cmd.Printf("  v3.registries:   guards=%d schedules=%d components=%d templates=%d assets=%d services=%d commands=%d\n",
			len(manifest.Guards), len(manifest.Schedules), len(manifest.Components), len(manifest.Templates), len(manifest.Assets), len(manifest.Services), len(manifest.Commands))
		cmd.Printf("  v3.platform:     admin=%d queries=%d permissions=%d media=%d navigation=%d regions=%d dependencies=%d\n",
			len(manifest.AdminSurfaces), len(manifest.Queries), len(manifest.PermissionDefinitions), len(manifest.Media), len(manifest.Navigation), len(manifest.Regions), len(manifest.Dependencies))
	}
	if strings.TrimSpace(manifest.Backend.Entry) != "" {
		cmd.Printf("  backend:        %s (%s)\n", manifest.Backend.Entry, manifest.Backend.RPC)
	}
	if strings.TrimSpace(manifest.Admin.Entry) != "" || len(manifest.Admin.Pages) > 0 {
		cmd.Printf("  admin:          entry=%s pages=%d\n", manifest.Admin.Entry, len(manifest.Admin.Pages))
	}
}
