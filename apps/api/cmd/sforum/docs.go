package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

func newExtensionDocsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Generate or verify host catalog documentation",
	}
	cmd.AddCommand(newExtensionDocsGenerateCommand())
	return cmd
}

func newExtensionDocsGenerateCommand() *cobra.Command {
	var (
		out   string
		check bool
	)
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Write host catalog Markdown from Go catalogs (F4.2)",
		Long: `Generate docs/extensions/catalogs/*.md from the same catalogs used by
the plugin SDK and extension test.

  sforum extension docs generate
  sforum extension docs generate --out /tmp/catalogs
  sforum extension docs generate --check   # fail if committed docs drifted
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := findRepoRoot()
			if err != nil {
				return err
			}
			dir := pluginsdk.ResolveCatalogDocsDir(repoRoot, out)
			if check {
				ok, msg := pluginsdk.CheckCatalogDocs(dir)
				if !ok {
					return fmt.Errorf("catalog docs out of date under %s:\n%s\n\nRun: go run ./cmd/sforum extension docs generate", dir, msg)
				}
				cmd.Printf("OK  catalog docs in sync: %s\n", dir)
				return nil
			}
			if err := pluginsdk.WriteCatalogDocs(dir); err != nil {
				return err
			}
			// 打印相对路径便于复制。
			rel := dir
			if r, err := filepath.Rel(repoRoot, dir); err == nil {
				rel = r
			}
			cmd.Printf("Wrote host catalog docs under %s\n", rel)
			for _, name := range pluginsdk.DocFileNames() {
				cmd.Printf("  - %s\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "Output directory (default: <repo>/"+pluginsdk.DefaultCatalogDocsDir+")")
	cmd.Flags().BoolVar(&check, "check", false, "Verify existing docs match regeneration (no write)")
	return cmd
}
