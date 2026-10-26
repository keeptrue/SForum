package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// StaleBuiltinRecoveryExpectation binds an out-of-band repair to the exact
// immutable desired set, rather than the mutable installed artifact.
type StaleBuiltinRecoveryExpectation struct {
	Revision  int64
	VersionID int64
	Version   string
	Digest    string
}

func newExtensionRecoveryStaleBuiltinCommand() *cobra.Command {
	var opts recoveryOptions
	var expected StaleBuiltinRecoveryExpectation
	cmd := &cobra.Command{
		Use:   "recover-stale-builtin <extension-id>",
		Short: "Remove a stale installed built-in from the exact plugin runtime publication",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if expected.Revision <= 0 || expected.VersionID <= 0 || strings.TrimSpace(expected.Version) == "" ||
				!recoveryDigestPattern.MatchString(expected.Digest) {
				return fmt.Errorf("--expect-revision, --expect-version-id, --expect-version and --expect-digest are required")
			}
			return withRecoveryRepository(cmd.Context(), opts.DatabaseURL, func(store recoveryRepository) error {
				item, err := store.RecoverStaleBuiltin(cmd.Context(), args[0], expected)
				if err != nil {
					return err
				}
				cmd.Printf("recovered stale built-in %s (published %s %s)\n", item.ID, expected.Version, expected.Digest)
				return nil
			})
		},
	}
	addRecoveryFlags(cmd, &opts, false)
	cmd.Flags().Int64Var(&expected.Revision, "expect-revision", 0, "Exact latest plugin runtime publication revision")
	cmd.Flags().Int64Var(&expected.VersionID, "expect-version-id", 0, "Exact published extension version row ID")
	cmd.Flags().StringVar(&expected.Version, "expect-version", "", "Exact published version")
	cmd.Flags().StringVar(&expected.Digest, "expect-digest", "", "Exact published package digest")
	return cmd
}

func (s *postgresRecoveryRepository) RecoverStaleBuiltin(
	ctx context.Context,
	extensionID string,
	expected StaleBuiltinRecoveryExpectation,
) (recoveryExtension, error) {
	if extensionID == "" || expected.Revision <= 0 || expected.VersionID <= 0 ||
		expected.Version == "" || !recoveryDigestPattern.MatchString(expected.Digest) {
		return recoveryExtension{}, errRecoveryArtifactMismatch
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return recoveryExtension{}, fmt.Errorf("begin stale built-in recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var item recoveryExtension
	publication, err := extensions.PublishProtectedPluginRuntimeQuarantineTx(ctx, tx, func() ([]string, error) {
		// The recovery helper already holds the desired-set lock here.
		var revision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM plugin_runtime_publications ORDER BY revision DESC LIMIT 1`).Scan(&revision); err != nil {
			return nil, err
		}
		if revision != expected.Revision {
			return nil, errRecoveryArtifactMismatch
		}
		var member extensions.PluginRuntimeMember
		err := tx.QueryRow(ctx, `
			SELECT extension_id, extension_version_id, extension_version, package_digest
			FROM plugin_runtime_publication_members
			WHERE publication_revision = $1 AND extension_id = $2
		`, revision, extensionID).Scan(
			&member.ExtensionID, &member.ExtensionVersionID, &member.ExtensionVersion, &member.PackageDigest,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errRecoveryArtifactMismatch
		}
		if err != nil {
			return nil, err
		}
		if member.ExtensionVersionID != expected.VersionID || member.ExtensionVersion != expected.Version ||
			member.PackageDigest != expected.Digest {
			return nil, errRecoveryArtifactMismatch
		}
		item, err = scanRecoveryExtension(tx.QueryRow(ctx,
			recoveryExtensionSelectSQL()+` WHERE extensions.id = $1 FOR UPDATE OF extensions`, extensionID,
		))
		if err != nil {
			return nil, err
		}
		if item.Source != "builtin" && !item.IsSystem {
			return nil, errRecoveryNotProtected
		}
		if item.Status != "installed" {
			return nil, errRecoveryNotEnabled
		}
		// Only reconcile an installed catalog row whose active artifact has
		// diverged from the immutable running member.
		if item.Version == expected.Version && item.PackageDigest == expected.Digest {
			return nil, errRecoveryArtifactMismatch
		}
		if err := disableRecoveryExtensions(ctx, tx, []recoveryExtension{item}, "stale_builtin_publication"); err != nil {
			return nil, err
		}
		return []string{item.ID}, nil
	})
	if err != nil {
		return recoveryExtension{}, err
	}
	if err := commitRecoveryPublication(ctx, tx, extensions.NewPostgresStore(s.pool), publication); err != nil {
		return recoveryExtension{}, fmt.Errorf("commit stale built-in recovery: %w", err)
	}
	item.Status = "disabled"
	return item, nil
}
