package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// extensionGCOptions 承载 sforum extension gc 的全部输入。dry-run 是默认态：
// 只有显式给出 --confirm-token 才会执行回收。
type extensionGCOptions struct {
	DatabaseURL  string
	JSON         bool
	DryRun       bool
	ConfirmToken string
	Retain       int
	RetainDays   int
	ExtensionID  string
}

// extensionGCDryRunReport 是 dry-run 的稳定 JSON 输出 schema。
type extensionGCDryRunReport struct {
	Mode             string                               `json:"mode"`
	Options          extensions.VersionGCOptions          `json:"options"`
	Candidates       []extensions.RetiredVersionCandidate `json:"candidates"`
	ReclaimableCount int                                  `json:"reclaimableCount"`
	BlockedCounts    map[string]int                       `json:"blockedCounts,omitempty"`
	Confirmation     extensions.VersionGCConfirmation     `json:"confirmation"`
}

// extensionGCExecuteReport 是执行的稳定 JSON 输出 schema。
type extensionGCExecuteReport struct {
	Mode   string                          `json:"mode"`
	Result extensions.VersionGCSweepResult `json:"result"`
}

func newExtensionGCCommand() *cobra.Command {
	opts := extensionGCOptions{Retain: 1}
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Preview (default) or execute extension version garbage collection (audited, fail-closed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ExtensionID = strings.TrimSpace(opts.ExtensionID)
			opts.ConfirmToken = strings.TrimSpace(opts.ConfirmToken)
			if opts.Retain < 0 {
				return fmt.Errorf("--retain must be >= 0")
			}
			if opts.RetainDays < 0 {
				return fmt.Errorf("--retain-days must be >= 0")
			}
			if opts.ConfirmToken != "" && opts.DryRun {
				return fmt.Errorf("--dry-run and --confirm-token are mutually exclusive")
			}
			return runExtensionGC(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "",
		"PostgreSQL URL (defaults to DATABASE_URL; no other app config is loaded)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Print machine-readable JSON")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"Explicit preview mode; it is also the default when --confirm-token is absent")
	cmd.Flags().StringVar(&opts.ConfirmToken, "confirm-token", "",
		"One-time token from the matching dry-run; its presence executes the sweep")
	cmd.Flags().IntVar(&opts.Retain, "retain", 1,
		"Historical versions to retain per extension besides the active one")
	cmd.Flags().IntVar(&opts.RetainDays, "retain-days", 0,
		"Also retain versions installed within the last N days (0 = disabled)")
	cmd.Flags().StringVar(&opts.ExtensionID, "extension-id", "", "Limit the sweep to one extension id")
	return cmd
}

func runExtensionGC(cmd *cobra.Command, opts extensionGCOptions) error {
	options := extensions.VersionGCOptions{
		Retain: opts.Retain, RetainDays: opts.RetainDays, ExtensionID: opts.ExtensionID,
	}
	return withRecoveryPool(cmd.Context(), opts.DatabaseURL, func(pool *pgxpool.Pool) error {
		store := extensions.NewPostgresStore(pool)
		if opts.ConfirmToken == "" {
			return runExtensionGCDryRun(cmd.Context(), cmd, store, options, opts.JSON)
		}
		return runExtensionGCExecute(cmd.Context(), cmd, store, options, opts.ConfirmToken, opts.JSON)
	})
}

// runExtensionGCDryRun 输出候选清单 + 保留/阻塞原因，并签发一次性确认 token
// （库内只存摘要；token 与当前计划精确绑定）。除确认记录外零副作用。
func runExtensionGCDryRun(ctx context.Context, cmd *cobra.Command, store *extensions.PostgresStore, options extensions.VersionGCOptions, asJSON bool) error {
	candidates, err := store.ListVersionGCCandidates(ctx, options)
	if err != nil {
		return err
	}
	confirmation, err := store.CreateVersionGCConfirmation(ctx, options)
	if err != nil {
		return err
	}
	report := extensionGCDryRunReport{
		Mode: "dry-run", Options: options, Candidates: candidates,
		ReclaimableCount: confirmation.CandidateCount, Confirmation: confirmation,
	}
	blocked := make(map[string]int)
	for _, candidate := range candidates {
		if candidate.BlockedReason != "" {
			blocked[candidate.BlockedReason]++
		}
	}
	if len(blocked) > 0 {
		report.BlockedCounts = blocked
	}
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	cmd.Printf("version GC plan (dry-run): retain=%d retainDays=%d extensionId=%q\n",
		report.Options.Retain, report.Options.RetainDays, report.Options.ExtensionID)
	for _, candidate := range candidates {
		if candidate.Reclaimable {
			cmd.Printf("  reclaimable  %-28s %-8s digest=%s installed=%s\n    path=%s\n",
				candidate.ExtensionID, candidate.Version, candidate.PackageDigest,
				candidate.InstalledAt.Format("2006-01-02 15:04:05"), candidate.PackagePath)
			continue
		}
		cmd.Printf("  blocked      %-28s %-8s reason=%s\n", candidate.ExtensionID, candidate.Version, candidate.BlockedReason)
		if candidate.BlockedReason == extensions.VersionGCBlockedDiskMissing {
			cmd.Println("    (disk_missing rows belong to the missing-artifact cleanup flow, not version GC)")
		}
	}
	cmd.Printf("reclaimable: %d\n", report.ReclaimableCount)
	cmd.Printf("confirmation token (one-time, binds this exact plan, expires %s):\n  %s\n",
		confirmation.ExpiresAt.Format("2006-01-02 15:04:05"), confirmation.Token)
	cmd.Println("execute: sforum extension gc --confirm-token <token> (repeat the same --retain/--retain-days/--extension-id options)")
	return nil
}

// runExtensionGCExecute 消费一次性 token 并逐条执行两段回收（先盘后账）。
// 任一条失败只影响该条；存在失败时命令以非零退出，但结果已完整打印。
func runExtensionGCExecute(ctx context.Context, cmd *cobra.Command, store *extensions.PostgresStore, options extensions.VersionGCOptions, token string, asJSON bool) error {
	result, err := store.ExecuteVersionGCSweep(ctx, options, token)
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(extensionGCExecuteReport{Mode: "execute", Result: result}); err != nil {
			return err
		}
	} else {
		cmd.Printf("version GC executed: removed=%d bytesFreed=%d\n", len(result.Removed), result.BytesFreed)
		for _, removed := range result.Removed {
			cmd.Printf("  removed  %-28s %-8s digest=%s\n", removed.ExtensionID, removed.Version, removed.PackageDigest)
		}
		for _, skipped := range result.Skipped {
			cmd.Printf("  skipped  %-28s %-8s reason=%s\n", skipped.Candidate.ExtensionID, skipped.Candidate.Version, skipped.Reason)
		}
		for _, failed := range result.Failed {
			cmd.Printf("  failed   %-28s %-8s: %s\n", failed.Candidate.ExtensionID, failed.Candidate.Version, failed.Error)
		}
		for _, warning := range result.Warnings {
			cmd.Printf("  warning  %s\n", warning)
		}
	}
	if len(result.Failed) > 0 {
		return fmt.Errorf("%d candidate(s) failed; their ledger rows and directories were preserved", len(result.Failed))
	}
	return nil
}
