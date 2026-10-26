package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
)

func newExtensionAPILTSCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "api-lts",
		Short: "Print Host/Frontend API LTS contracts and process-local shim telemetry",
		Long: `Print the seeded Host/Frontend API LTS policy and this process's shim usage.

The running API/worker process records remaining compatibility shims in its own
process-local counters (apilts.Process). This CLI process starts empty unless
you only need the published contract policy (status, RemoveAfter, replacement).

Deletion of sforum.theme.l1.request-time-loader requires CanRemoveWithZeroShim
true for a full LTS window — see
docs/extensions/v3/p13-migration-and-lts.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// CLI 默认展示策略种子；若需要本进程 Process 计数则使用同一 Process 单例。
			reg := apilts.Process()
			snap := reg.Snapshot()
			now := time.Now().UTC()
			report := struct {
				apilts.Snapshot
				ThemeRequestTimeLoaderCalls     uint64 `json:"themeRequestTimeLoaderCalls"`
				ThemeRequestTimeCanRemoveWindow bool   `json:"themeRequestTimeLoaderCanRemoveWindow"`
				ThemeRequestTimeCanRemoveZero   bool   `json:"themeRequestTimeLoaderCanRemoveWithZeroShim"`
				GeneratedAt                     string `json:"generatedAt"`
				Note                            string `json:"note"`
			}{
				Snapshot:                        snap,
				ThemeRequestTimeLoaderCalls:     reg.ShimCalls(apilts.ThemeRequestTimeLoaderContractID),
				ThemeRequestTimeCanRemoveWindow: reg.CanRemove(apilts.ThemeRequestTimeLoaderContractID, now),
				ThemeRequestTimeCanRemoveZero:   reg.CanRemoveWithZeroShim(apilts.ThemeRequestTimeLoaderContractID, now),
				GeneratedAt:                     now.Format(time.RFC3339),
				Note:                            "Live shim counters live in the API/worker process; this CLI process is usually zero.",
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			cmd.Printf("schema: %s\n", snap.SchemaVersion)
			cmd.Printf("minDeprecation: %s\n", snap.MinDeprecation)
			cmd.Printf("themeRequestTimeLoaderCalls: %d\n", report.ThemeRequestTimeLoaderCalls)
			cmd.Printf("themeRequestTimeLoaderCanRemoveWindow: %v\n", report.ThemeRequestTimeCanRemoveWindow)
			cmd.Printf("themeRequestTimeLoaderCanRemoveWithZeroShim: %v\n", report.ThemeRequestTimeCanRemoveZero)
			cmd.Printf("note: %s\n", report.Note)
			cmd.Println("contracts:")
			for _, c := range snap.Contracts {
				cmd.Printf("  - %s kind=%s status=%s shim=%v replacement=%s\n",
					c.ID, c.Kind, c.Status, c.ShimEnabled, c.Replacement)
			}
			if len(snap.ShimUsage) == 0 {
				cmd.Println("shimUsage: (empty)")
			} else {
				cmd.Println("shimUsage:")
				for _, row := range snap.ShimUsage {
					cmd.Printf("  - %s calls=%d\n", row.ContractID, row.Calls)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON snapshot")
	return cmd
}
