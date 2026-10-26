package main

import (
	"github.com/spf13/cobra"

	devhygiene "github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
)

func newDevCleanupOrphanPluginsCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "dev:cleanup-orphan-plugins",
		Short: "Stop reparented SForum extension backend plugin processes (safe for live sforum-api children)",
		Long: `Selects only extension backend/plugin processes that are no longer owned by a live
sforum-api (typically PPID=1 after air hot reload). Uses the same selection logic as
the development API reaper (apps/api/app/Support/DevHygiene).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := devhygiene.CleanupOrphanExtensionPlugins(devhygiene.CleanupOptions{DryRun: dryRun})
			if err != nil {
				return err
			}
			if len(result.Selected) == 0 {
				cmd.Println("no orphan extension backend plugins selected")
				return nil
			}
			for _, pid := range result.Selected {
				if dryRun {
					cmd.Printf("DRY_RUN would stop pid=%d\n", pid)
					continue
				}
				cmd.Printf("stopped orphan plugin pid=%d\n", pid)
			}
			if dryRun {
				cmd.Printf("selected %d orphan plugin(s) (dry-run)\n", len(result.Selected))
			} else {
				cmd.Printf("signaled %d / selected %d orphan plugin(s)\n", len(result.Signaled), len(result.Selected))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print selected PIDs without sending signals")
	return cmd
}
