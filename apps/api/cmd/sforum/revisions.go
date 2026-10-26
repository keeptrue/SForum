package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	"github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	"github.com/keeptrue/sforum/apps/api/config"
)

type revisionBackfillOptions struct {
	DatabaseURL string
	BatchSize   int
	Loop        bool
}

func newRevisionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revisions",
		Short: "Forum content revision maintenance commands",
	}
	cmd.AddCommand(newRevisionsBackfillCommand())
	return cmd
}

func newRevisionsBackfillCommand() *cobra.Command {
	opts := revisionBackfillOptions{BatchSize: 100}
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Backfill forum content revision ledger in bounded batches",
		Long: `Backfill posts.current_revision and post_revisions accepted-version rows.

The command is recoverable and safe to rerun. Each batch claims posts with
FOR UPDATE SKIP LOCKED, preserves legacy source snapshots in stable order, then
adds exactly one current snapshot before setting posts.current_revision.

Run once for a bounded batch:

  sforum revisions backfill --batch=100

Loop until pending reaches zero:

  sforum revisions backfill --batch=100 --loop
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRevisionsBackfillCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL；空则用环境变量")
	cmd.Flags().IntVar(&opts.BatchSize, "batch", 100, "每批处理的 posts 数量")
	cmd.Flags().BoolVar(&opts.Loop, "loop", false, "持续分批执行直到 pending=0")
	return cmd
}

func runRevisionsBackfillCommand(ctx context.Context, opts revisionBackfillOptions, cmd *cobra.Command) error {
	databaseURL := opts.DatabaseURL
	if databaseURL == "" {
		databaseURL = config.Load().DatabaseURL
	}
	if databaseURL == "" {
		return fmt.Errorf("database url is empty: set DATABASE_URL or pass --database-url")
	}
	pool, err := postgres.NewPool(ctx, databaseURL, 5)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	store := forum.NewPostgresStore(pool)
	for {
		result, err := store.BackfillContentRevisions(ctx, forum.RevisionBackfillOptions{BatchSize: opts.BatchSize})
		if err != nil {
			return err
		}
		cmd.Printf("revision backfill: claimed=%d completed=%d pending=%d\n", result.Claimed, result.Completed, result.Pending)
		if !opts.Loop || result.Pending == 0 || result.Claimed == 0 {
			return nil
		}
	}
}
