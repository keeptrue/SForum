package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	systemtier "github.com/keeptrue/sforum/apps/api/app/Support/SystemTier"
)

// system tier CLI is out-of-band recovery: connects to PostgreSQL only, never
// starts API/Nuxt/plugin runtime or loads system extension code.

type systemTierOptions struct {
	DatabaseURL string
	JSON        bool
}

func newExtensionSystemTierCommand() *cobra.Command {
	opts := systemTierOptions{}
	cmd := &cobra.Command{
		Use:   "system-tier",
		Short: "Manage system extension tier without starting SForum or plugin code",
		Long: `List, upsert, and disable operator-managed system-tier members.

Safe Mode always bypasses this tier before loading any system extension code.
This CLI only touches PostgreSQL system_tier_members and never imports package
backends — usable when API/Nuxt/plugin runtimes are down.`,
	}
	cmd.PersistentFlags().StringVar(&opts.DatabaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL)")
	cmd.PersistentFlags().BoolVar(&opts.JSON, "json", false, "JSON output")
	cmd.AddCommand(
		newSystemTierListCommand(&opts),
		newSystemTierUpsertCommand(&opts),
		newSystemTierDisableCommand(&opts),
	)
	return cmd
}

func newSystemTierListCommand(opts *systemTierOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List system tier members (including disabled)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSystemTierRegistry(cmd.Context(), opts.DatabaseURL, func(reg *systemtier.Registry) error {
				snap, err := reg.Snapshot(cmd.Context())
				if err != nil {
					return err
				}
				if opts.JSON {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(snap)
				}
				cmd.Printf("schema: %s safeModeBypass: %v\n", snap.SchemaVersion, snap.SafeModeBypass)
				cmd.Println("EXTENSION_ID\tROLE\tPRIORITY\tENABLED")
				for _, m := range snap.Members {
					cmd.Printf("%s\t%s\t%d\t%v\n", m.ExtensionID, m.Role, m.Priority, m.Enabled)
				}
				return nil
			})
		},
	}
}

func newSystemTierUpsertCommand(opts *systemTierOptions) *cobra.Command {
	var role string
	var priority int
	var enabled bool
	cmd := &cobra.Command{
		Use:   "upsert <extension-id>",
		Short: "Add or update a system tier member without loading package code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSystemTierRegistry(cmd.Context(), opts.DatabaseURL, func(reg *systemtier.Registry) error {
				member := systemtier.Member{
					ExtensionID: strings.TrimSpace(args[0]),
					Role:        role,
					Priority:    priority,
					Enabled:     enabled,
					UpdatedBy:   "cli",
					UpdatedAt:   time.Now().UTC(),
				}
				if err := reg.Upsert(cmd.Context(), member); err != nil {
					return err
				}
				cmd.Printf("upserted %s role=%s priority=%d enabled=%v\n",
					member.ExtensionID, member.Role, member.Priority, member.Enabled)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&role, "role", systemtier.RoleInfra, "auth|cache|storage|infra")
	cmd.Flags().IntVar(&priority, "priority", 100, "load order (lower first)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "whether the member is enabled")
	return cmd
}

func newSystemTierDisableCommand(opts *systemTierOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <extension-id>",
		Short: "Disable a system tier member out of band (no package code loaded)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSystemTierRegistry(cmd.Context(), opts.DatabaseURL, func(reg *systemtier.Registry) error {
				id := strings.TrimSpace(args[0])
				if err := reg.Disable(cmd.Context(), id, "cli"); err != nil {
					return err
				}
				cmd.Printf("disabled system-tier member %s\n", id)
				return nil
			})
		},
	}
}

func withSystemTierRegistry(ctx context.Context, databaseURL string, fn func(*systemtier.Registry) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	url := strings.TrimSpace(databaseURL)
	if url == "" {
		url = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if url == "" {
		return fmt.Errorf("system-tier: DATABASE_URL or --database-url is required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := systemtier.NewPostgresStore(pool)
	if err != nil {
		return err
	}
	return fn(systemtier.NewWithStore(store))
}
