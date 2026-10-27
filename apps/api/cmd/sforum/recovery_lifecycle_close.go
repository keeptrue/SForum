package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
)

var (
	errLifecycleCloseNotFound      = errors.New("lifecycle-close: lifecycle operation not found")
	errLifecycleCloseAlreadyClosed = errors.New("lifecycle-close: lifecycle operation is already closed")
)

// errLifecycleCloseLeaseActive 是 fail-closed 信号：仍有未过期 lease 说明
// 进程可能只是慢而不是死；除非 --force，绝不越过它。
var errLifecycleCloseLeaseActive = errors.New(
	"lifecycle-close: a step still holds an unexpired lease; wait for it to expire or pass --force",
)

// lifecycleCloseOperation 是被关闭操作的取证摘要，也是 --json 的输出体。
type lifecycleCloseOperation struct {
	OperationID    int64  `json:"operationId"`
	ExtensionID    string `json:"extensionId"`
	Operation      string `json:"operation"`
	PreviousState  string `json:"previousState"`
	TerminalResult string `json:"terminalResult"`
	ErrorCode      string `json:"errorCode"`
	ClosedSteps    int64  `json:"closedSteps"`
	Forced         bool   `json:"forced"`
}

type lifecycleCloseOptions struct {
	recoveryOptions
	OperationID int64
	Reason      string
	Force       bool
}

func newExtensionRecoveryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Out-of-band extension recovery without starting SForum or plugin code",
	}
	cmd.AddCommand(newExtensionRecoveryLifecycleCloseCommand())
	cmd.AddCommand(newExtensionRecoveryActivationStateCommand())
	cmd.AddCommand(newExtensionRecoveryRecheckCommand())
	return cmd
}

func newExtensionRecoveryLifecycleCloseCommand() *cobra.Command {
	opts := lifecycleCloseOptions{}
	cmd := &cobra.Command{
		Use:   "lifecycle-close",
		Short: "Close one open lifecycle operation out of band (cancelled, audited)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.Reason = strings.TrimSpace(opts.Reason)
			if opts.OperationID <= 0 {
				return fmt.Errorf("--operation-id must be a positive integer")
			}
			if opts.Reason == "" {
				return fmt.Errorf("--reason is required; it is written into the audit record")
			}
			if len(opts.Reason) > 4096 {
				return fmt.Errorf("--reason must be at most 4096 characters")
			}
			var closed lifecycleCloseOperation
			err := withRecoveryPool(cmd.Context(), opts.DatabaseURL, func(pool *pgxpool.Pool) error {
				result, closeErr := closeLifecycleOperationOutOfBand(cmd.Context(), pool, opts)
				if closeErr != nil {
					return closeErr
				}
				closed = result
				return nil
			})
			if err != nil {
				return err
			}
			if opts.JSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(closed)
			}
			cmd.Printf("closed lifecycle operation %d (%s %s): %d open step(s) failed\n",
				closed.OperationID, closed.ExtensionID, closed.Operation, closed.ClosedSteps)
			cmd.Println("restart the API to resume normal plugin runtime convergence")
			return nil
		},
	}
	addRecoveryFlags(cmd, &opts.recoveryOptions, true)
	cmd.Flags().Int64Var(&opts.OperationID, "operation-id", 0, "Open lifecycle operation id (from extension_lifecycle_operations)")
	cmd.Flags().StringVar(&opts.Reason, "reason", "", "Why the operation is being closed (audited, required)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Close even when a step still holds an unexpired lease")
	return cmd
}

// lifecycleCloseAlreadyClosedError 说明「为什么已经是终态」。只回一句
// 「已关闭」会与现场观测到的 state=uninstalling 冲突：state 只是最后阶段
// （uninstall 收尾在 uninstalling、disable 收尾在 draining），completed_at 与
// terminal_result 才是终态判据。把两者一起打出来，运维才能对上 API 的 closed。
func lifecycleCloseAlreadyClosedError(
	operationID int64,
	summary lifecycleCloseOperation,
	terminal string,
	completedAt *time.Time,
) error {
	return fmt.Errorf("%w: operation %d (%s %s) state=%s terminal=%s completedAt=%s",
		errLifecycleCloseAlreadyClosed, operationID, summary.ExtensionID, summary.Operation,
		summary.PreviousState, terminal, completedAt.UTC().Format(time.RFC3339))
}

// withRecoveryPool 与 withRecoveryRepository 并列：lifecycle-close 需要裸
// pool 操作 lifecycle 账本，同样只连 PostgreSQL、不装载任何应用配置或插件代码。
func withRecoveryPool(ctx context.Context, databaseURL string, run func(*pgxpool.Pool) error) error {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		return fmt.Errorf("database url is empty: set DATABASE_URL or pass --database-url")
	}
	pool, err := postgres.NewPool(ctx, databaseURL, 2)
	if err != nil {
		return fmt.Errorf("connect recovery database: %w", err)
	}
	defer pool.Close()
	return run(pool)
}

// closeLifecycleOperationOutOfBand 在单事务内把一条开放 lifecycle operation
// 推进到 cancelled 终态：锁行复核开放性、拒绝活跃 lease（除非 --force）、
// 关闭开放 step、CAS 关闭操作并写审计。任何一步失败整体回滚。
func closeLifecycleOperationOutOfBand(ctx context.Context, pool *pgxpool.Pool, opts lifecycleCloseOptions) (lifecycleCloseOperation, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("begin lifecycle close: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var summary lifecycleCloseOperation
	summary.OperationID = opts.OperationID
	summary.TerminalResult = "cancelled"
	summary.ErrorCode = "lifecycle.cli_closed"
	summary.Forced = opts.Force
	var revision int64
	var completedAt *time.Time
	var previousTerminal string
	err = tx.QueryRow(ctx, `
		SELECT extension_id, operation, state, COALESCE(terminal_result, ''), revision, completed_at
		FROM extension_lifecycle_operations
		WHERE id = $1
		FOR UPDATE
	`, opts.OperationID).Scan(
		&summary.ExtensionID, &summary.Operation, &summary.PreviousState,
		&previousTerminal, &revision, &completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleCloseOperation{}, errLifecycleCloseNotFound
	}
	if err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("lock lifecycle operation for close: %w", err)
	}
	if completedAt != nil {
		return lifecycleCloseOperation{}, lifecycleCloseAlreadyClosedError(opts.OperationID, summary, previousTerminal, completedAt)
	}

	if !opts.Force {
		var activeLeases int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM extension_lifecycle_steps
			WHERE operation_id = $1
			  AND status IN ('planned', 'running', 'waiting')
			  AND lease_expires_at > statement_timestamp()
		`, opts.OperationID).Scan(&activeLeases); err != nil {
			return lifecycleCloseOperation{}, fmt.Errorf("check lifecycle active leases: %w", err)
		}
		if activeLeases > 0 {
			return lifecycleCloseOperation{}, errLifecycleCloseLeaseActive
		}
	}

	stepTag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'failed',
		    error_code = $2, error_reason = $2, error_message = $3,
		    error_retryable = false,
		    lease_owner_token = '', lease_expires_at = NULL, lease_heartbeat_at = NULL,
		    completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE operation_id = $1 AND status IN ('planned', 'running', 'waiting')
	`, opts.OperationID, "lifecycle.cli_closed", opts.Reason)
	if err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("close lifecycle steps: %w", err)
	}
	summary.ClosedSteps = stepTag.RowsAffected()

	// revision CAS 与仓库层 CompleteOperation 同型：并发关闭只成功一方。
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_operations
		SET state = 'failed', terminal_result = 'cancelled',
		    error_code = $2, error_reason = $2, error_message = $3,
		    error_retryable = false,
		    completed_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE id = $1 AND revision = $4 AND completed_at IS NULL
	`, opts.OperationID, "lifecycle.cli_closed", opts.Reason, revision)
	if err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("close lifecycle operation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return lifecycleCloseOperation{}, fmt.Errorf("%w: operation %d was closed concurrently",
			errLifecycleCloseAlreadyClosed, opts.OperationID)
	}

	metadata, err := json.Marshal(map[string]any{
		"mode":        "lifecycle_close",
		"operationId": opts.OperationID,
		"extensionId": summary.ExtensionID,
		"reason":      opts.Reason,
		"force":       opts.Force,
	})
	if err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("marshal lifecycle close audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
	`, audit.ActionExtensionCLIRecovery, string(metadata)); err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("record lifecycle close audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return lifecycleCloseOperation{}, fmt.Errorf("commit lifecycle close: %w", err)
	}
	return summary, nil
}
