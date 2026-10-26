package main

import (
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	lifecyclerecovery "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/LifecycleRecovery"
)

type activationStateRecoveryOptions struct {
	recoveryOptions
	Limit int
}

// newExtensionRecoveryActivationStateCommand 是中断 activation 修复的带外入口。
// 它和 API 启动修复调用同一个 collaborator：只按 durable 证据修复 state 事务
// 已提交 target、publication 未提交的 operation，条件漂移即 no-op。
func newExtensionRecoveryActivationStateCommand() *cobra.Command {
	opts := activationStateRecoveryOptions{}
	cmd := &cobra.Command{
		Use:   "activation-state",
		Short: "Restore interrupted activation publications to their exact source artifact",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Limit < 0 {
				return fmt.Errorf("--limit must not be negative")
			}
			return withRecoveryPool(cmd.Context(), opts.DatabaseURL, func(pool *pgxpool.Pool) error {
				recovered, err := lifecyclerecovery.NewPostgresInterruptedActivationRecovery(pool).
					RecoverInterruptedActivations(cmd.Context(), opts.Limit)
				if err != nil {
					return err
				}
				if opts.JSON {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(recovered)
				}
				if len(recovered) == 0 {
					cmd.Println("no interrupted activation publication matched the exact recovery evidence")
					return nil
				}
				cmd.Println("OPERATION\tEXTENSION\tSTATE_PUBLICATION\tFROM\tTO\tAUDIT")
				for _, item := range recovered {
					cmd.Printf("%d\t%s\t%d\t%d/%s\t%d/%s\t%d\n",
						item.OperationID, item.ExtensionID, item.StatePublicationID,
						item.FromVersionID, item.FromVersion, item.ToVersionID, item.ToVersion, item.AuditEventID,
					)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "PostgreSQL connection string (defaults to DATABASE_URL)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "print the recovered activations as JSON")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "maximum candidates to inspect (0 = default)")
	return cmd
}
