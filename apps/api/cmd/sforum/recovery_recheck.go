package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// recoveryRecheckReasonLimit 与 extension_lifecycle_recovery_decisions.reason
// 的 octet_length 上限一致，避免审计表拒绝后才在事务里失败。
const recoveryRecheckReasonLimit = 4096

type recoveryRecheckOptions struct {
	recoveryOptions
	Reason string
}

// recoveryRecheckResult 是成功路径的 --json 输出体。
type recoveryRecheckResult struct {
	Revision      int64  `json:"revision"`
	MemberCount   int    `json:"memberCount"`
	MembersDigest string `json:"membersDigest"`
	Reason        string `json:"reason"`
}

// newExtensionRecoveryRecheckCommand 是 recovery-only 的显式复核入口。
//
// 它只做三件事：读 durable 证据重建 desired full set、与最新 publication 精确比对、
// 一致时推进一条 reason='recovery' 的不可变 revision。它不启动 SForum、不加载插件
// 代码、不改任何 extensions 行——不一致时原样列出制品并非零退出，绝不"尽力修"。
//
// 当前阶段它不热恢复：推进 revision 只是把"该重新收敛了"写成 durable 事实，
// 生效点在进程下一次启动。热恢复需要先有能复现"完整 API 从 recovery-only 恢复到
// 200 且路由集/provider 集合与冷启动一致"的 e2e 夹具，见 decision record。
func newExtensionRecoveryRecheckCommand() *cobra.Command {
	opts := recoveryRecheckOptions{}
	cmd := &cobra.Command{
		Use:   "recheck",
		Short: "Rebuild the desired plugin runtime full set and publish a recovery revision",
		Long: "Rebuild the desired plugin runtime full set from durable rows and compare it with the\n" +
			"latest immutable publication. When they match exactly, a new reason=recovery revision is\n" +
			"published. When they differ, every offending artifact is printed and nothing is written.\n\n" +
			"This command only publishes a revision: it does not make the running process leave\n" +
			"recovery-only. The Host applies the revision at its next process start.\n\n" +
			"Nothing here starts SForum, loads plugin code, or exits recovery-only mode on its own:\n" +
			"recovery-only is a fail-closed observation surface, and only an explicit recheck may\n" +
			"publish the revision that lets a restarted Host try again.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reason, err := validateRecoveryRecheckReason(opts.Reason)
			if err != nil {
				return err
			}
			var result recoveryRecheckResult
			// 所有参数校验都在 withRecoveryPool 之前完成：非法输入不触库。
			err = withRecoveryPool(cmd.Context(), opts.DatabaseURL, func(pool *pgxpool.Pool) error {
				published, recheckErr := recheckPluginRuntimeRecovery(cmd.Context(), pool, reason)
				if recheckErr != nil {
					var mismatch *recoveryRecheckMismatch
					if asRecoveryRecheckMismatch(recheckErr, &mismatch) {
						printRecoveryRecheckArtifacts(cmd, mismatch.Artifacts, opts.JSON)
					}
					return recheckErr
				}
				result = published
				return nil
			})
			if err != nil {
				return err
			}
			if opts.JSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			cmd.Printf("published recovery revision %d (%d member(s), digest %s)\n",
				result.Revision, result.MemberCount, result.MembersDigest)
			cmd.Println("this command only advances the revision: the running process stays in recovery-only")
			cmd.Println("the Host applies it at the next process start")
			return nil
		},
	}
	addRecoveryFlags(cmd, &opts.recoveryOptions, true)
	cmd.Flags().StringVar(&opts.Reason, "reason", "", "Why the Host is being asked to re-converge (required, audited, <=4096 bytes)")
	return cmd
}

// validateRecoveryRecheckReason 只做纯参数校验，不触库。
func validateRecoveryRecheckReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return "", fmt.Errorf("--reason is required; it is written into the audit record")
	}
	if len([]byte(reason)) > recoveryRecheckReasonLimit {
		return "", fmt.Errorf("--reason must be at most %d bytes", recoveryRecheckReasonLimit)
	}
	return reason, nil
}

// recoveryRecheckMismatch 携带完整不一致清单，供 CLI 原样打印。
type recoveryRecheckMismatch struct {
	Artifacts []extensions.PluginRuntimeRecoveryArtifact
	Err       error
}

func (m *recoveryRecheckMismatch) Error() string {
	return fmt.Sprintf("%v: %d artifact(s) do not match durable evidence; nothing was written",
		m.Err, len(m.Artifacts))
}

func (m *recoveryRecheckMismatch) Unwrap() error { return m.Err }

func asRecoveryRecheckMismatch(err error, target **recoveryRecheckMismatch) bool {
	for err != nil {
		if mismatch, ok := err.(*recoveryRecheckMismatch); ok {
			*target = mismatch
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// recheckPluginRuntimeRecovery 在单事务内完成复核 + 审计 + 提交：
// 审计写失败就整笔回滚，绝不允许"revision 推进了但没有审计"。
func recheckPluginRuntimeRecovery(
	ctx context.Context,
	pool *pgxpool.Pool,
	reason string,
) (recoveryRecheckResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return recoveryRecheckResult{}, fmt.Errorf("begin plugin runtime recovery recheck: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	publication, artifacts, err := extensions.RecheckPluginRuntimeRecoveryTx(ctx, tx, 0)
	if err != nil {
		if len(artifacts) > 0 || isPluginRuntimeRecheckMismatch(err) {
			return recoveryRecheckResult{}, &recoveryRecheckMismatch{Artifacts: artifacts, Err: err}
		}
		return recoveryRecheckResult{}, err
	}

	metadata, err := json.Marshal(map[string]any{
		"mode":          "runtime_recheck",
		"actor":         "sforum-cli",
		"reason":        reason,
		"revision":      publication.Revision,
		"memberCount":   publication.MemberCount,
		"membersDigest": publication.MembersDigest,
	})
	if err != nil {
		return recoveryRecheckResult{}, fmt.Errorf("marshal plugin runtime recovery recheck audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
	`, audit.ActionExtensionCLIRecovery, string(metadata)); err != nil {
		return recoveryRecheckResult{}, fmt.Errorf("record plugin runtime recovery recheck audit: %w", err)
	}

	store := extensions.NewPostgresStore(pool)
	if err := commitRecoveryPublication(ctx, tx, store, publication); err != nil {
		return recoveryRecheckResult{}, fmt.Errorf("commit plugin runtime recovery recheck: %w", err)
	}
	return recoveryRecheckResult{
		Revision:      publication.Revision,
		MemberCount:   publication.MemberCount,
		MembersDigest: publication.MembersDigest,
		Reason:        string(publication.Reason),
	}, nil
}

func isPluginRuntimeRecheckMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), extensions.ErrPluginRuntimeRecoveryRecheckMismatch.Error())
}

func printRecoveryRecheckArtifacts(cmd *cobra.Command, artifacts []extensions.PluginRuntimeRecoveryArtifact, asJSON bool) {
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(map[string]any{
			"status":    "mismatch",
			"artifacts": artifacts,
		})
		return
	}
	if len(artifacts) == 0 {
		cmd.Println("no artifact could be compared: the durable plugin runtime publication is missing")
		return
	}
	cmd.Println("EXTENSION\tVERSION\tDIGEST\tPROBLEM")
	for _, artifact := range artifacts {
		version := artifact.Version
		if version == "" {
			version = "-"
		}
		digest := artifact.Digest
		if digest == "" {
			digest = "-"
		}
		cmd.Printf("%s\t%s\t%s\t%s\n", artifact.ExtensionID, version, digest, artifact.Problem)
	}
}
