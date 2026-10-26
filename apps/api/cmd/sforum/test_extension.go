package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

func newExtensionTestCommand() *cobra.Command {
	var (
		asJSON            bool
		skipBackendBinary bool
		allowScaffoldStub bool
	)
	cmd := &cobra.Command{
		Use:   "test [path]",
		Short: "Run host contract checks on an extension package",
		Long: `Load the package, validate the manifest against host catalogs, and report
capability resolution, events, contributions, providers, jobs, and backend entry.

Exit code 1 if any error-level check fails. Warnings (scaffold stubs, unknown
provider slots) do not fail by default.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			opts := pluginsdk.Options{
				SkipBackendBinary: skipBackendBinary,
			}
			// allowScaffoldStub 仅跳过「二进制缺失」错误，仍会 warn scaffold stub。
			if allowScaffoldStub {
				opts.SkipBackendBinary = true
			}
			return runExtensionTest(cmd, root, opts, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable report JSON")
	cmd.Flags().BoolVar(&skipBackendBinary, "skip-backend-binary", false, "Do not require backend entry file on disk")
	cmd.Flags().BoolVar(&allowScaffoldStub, "allow-scaffold", false, "Alias of --skip-backend-binary for scaffold packages")
	return cmd
}

func runExtensionTest(cmd *cobra.Command, root string, opts pluginsdk.Options, asJSON bool) error {
	abs, err := resolveExtensionPackageRoot(root)
	if err != nil {
		return err
	}
	report, err := pluginsdk.LoadAndTest(abs, opts)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		// 不序列化完整 Manifest 以免过大；只输出摘要字段。
		out := map[string]any{
			"root":             report.Root,
			"ok":               report.OK,
			"errors":           report.Errors,
			"warnings":         report.Warnings,
			"id":               report.Manifest.ID,
			"type":             report.Manifest.Type,
			"version":          report.Manifest.Version,
			"manifestContract": extensionmanifest.ManifestContract(report.Manifest),
			"checks":           report.Checks,
		}
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		printTestReport(cmd, report)
	}
	if !report.OK {
		return fmt.Errorf("extension test failed: %d error(s), %d warning(s)", report.Errors, report.Warnings)
	}
	return nil
}

func printTestReport(cmd *cobra.Command, report pluginsdk.Report) {
	status := "PASS"
	if !report.OK {
		status = "FAIL"
	}
	cmd.Printf("%s  %s\n", status, report.Root)
	if report.Manifest.ID != "" {
		cmd.Printf("  id:      %s\n", report.Manifest.ID)
		cmd.Printf("  type:    %s\n", report.Manifest.Type)
		cmd.Printf("  version: %s\n", report.Manifest.Version)
		cmd.Printf("  contract: %s\n", extensionmanifest.ManifestContract(report.Manifest))
	}
	cmd.Printf("  errors:  %d  warnings: %d  checks: %d\n", report.Errors, report.Warnings, len(report.Checks))
	for _, check := range report.Checks {
		mark := "·"
		switch check.Level {
		case "error":
			mark = "✗"
		case "warn":
			mark = "!"
		case "ok":
			mark = "✓"
		}
		path := ""
		if check.Path != "" {
			path = " [" + check.Path + "]"
		}
		cmd.Printf("  %s %s%s — %s\n", mark, check.Code, path, check.Message)
	}
}
