package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
)

var (
	errRecoveryExtensionNotFound = errors.New("recovery: extension not found")
	errRecoveryProtected         = errors.New("recovery: built-in or system extension is protected")
	errRecoveryNotProtected      = errors.New("recovery: extension is not a built-in or system extension")
	errRecoveryArtifactMismatch  = errors.New("recovery: exact extension artifact does not match")
	errRecoveryNotEnabled        = errors.New("recovery: extension is not enabled")
	recoveryDigestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type recoveryExtension struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Status        string `json:"status"`
	Source        string `json:"source"`
	IsSystem      bool   `json:"isSystem"`
	Version       string `json:"version"`
	PackageDigest string `json:"packageDigest"`
}

type recoveryRepository interface {
	List(context.Context) ([]recoveryExtension, error)
	Disable(context.Context, string) (recoveryExtension, error)
	DisableAllThirdParty(context.Context) ([]recoveryExtension, error)
	QuarantineProtected(context.Context, string, string, string) (recoveryExtension, error)
	RecoverStaleBuiltin(context.Context, string, StaleBuiltinRecoveryExpectation) (recoveryExtension, error)
}

type postgresRecoveryRepository struct {
	pool *pgxpool.Pool
}

type recoveryOptions struct {
	DatabaseURL string
	JSON        bool
}

type recoveryQuarantineOptions struct {
	recoveryOptions
	ExpectedVersion string
	ExpectedDigest  string
}

func newExtensionRecoveryListCommand() *cobra.Command {
	opts := recoveryOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List extension recovery state without starting SForum or plugin code",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRecoveryRepository(cmd.Context(), opts.DatabaseURL, func(store recoveryRepository) error {
				items, err := store.List(cmd.Context())
				if err != nil {
					return err
				}
				if opts.JSON {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(items)
				}
				cmd.Println("ID\tTYPE\tSTATUS\tSOURCE\tVERSION\tDIGEST")
				for _, item := range items {
					digest := item.PackageDigest
					if len(digest) > 12 {
						digest = digest[:12]
					}
					cmd.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.Type, item.Status, item.Source, item.Version, digest)
				}
				return nil
			})
		},
	}
	addRecoveryFlags(cmd, &opts, true)
	return cmd
}

func newExtensionRecoveryDisableCommand() *cobra.Command {
	opts := recoveryOptions{}
	cmd := &cobra.Command{
		Use:   "disable <extension-id>",
		Short: "Disable one third-party extension out of band",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRecoveryRepository(cmd.Context(), opts.DatabaseURL, func(store recoveryRepository) error {
				item, err := store.Disable(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				cmd.Printf("disabled %s (%s %s)\n", item.ID, item.Type, item.Version)
				return nil
			})
		},
	}
	addRecoveryFlags(cmd, &opts, false)
	return cmd
}

func newExtensionRecoveryDisableAllCommand() *cobra.Command {
	opts := recoveryOptions{}
	cmd := &cobra.Command{
		Use:   "disable-all",
		Short: "Disable every third-party extension out of band",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withRecoveryRepository(cmd.Context(), opts.DatabaseURL, func(store recoveryRepository) error {
				items, err := store.DisableAllThirdParty(cmd.Context())
				if err != nil {
					return err
				}
				cmd.Printf("disabled %d third-party extension(s)\n", len(items))
				for _, item := range items {
					cmd.Printf("  %s\n", item.ID)
				}
				return nil
			})
		},
	}
	addRecoveryFlags(cmd, &opts, false)
	return cmd
}

func newExtensionRecoveryQuarantineCommand() *cobra.Command {
	opts := recoveryQuarantineOptions{}
	cmd := &cobra.Command{
		Use:   "quarantine <extension-id>",
		Short: "Quarantine one exact built-in or system artifact out of band",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionID := strings.TrimSpace(args[0])
			expectedVersion := strings.TrimSpace(opts.ExpectedVersion)
			expectedDigest := strings.TrimSpace(opts.ExpectedDigest)
			if expectedVersion == "" || expectedDigest == "" {
				return fmt.Errorf("--expect-version and --expect-digest are required")
			}
			if !recoveryDigestPattern.MatchString(expectedDigest) {
				return fmt.Errorf("--expect-digest must be a 64-character lowercase SHA-256 digest")
			}
			return withRecoveryRepository(cmd.Context(), opts.DatabaseURL, func(store recoveryRepository) error {
				item, err := store.QuarantineProtected(
					cmd.Context(), extensionID, expectedVersion, expectedDigest,
				)
				if err != nil {
					return err
				}
				cmd.Printf("quarantined %s (%s %s %s)\n", item.ID, item.Type, item.Version, item.PackageDigest)
				return nil
			})
		},
	}
	addRecoveryFlags(cmd, &opts.recoveryOptions, false)
	cmd.Flags().StringVar(&opts.ExpectedVersion, "expect-version", "", "Exact active version to quarantine")
	cmd.Flags().StringVar(&opts.ExpectedDigest, "expect-digest", "", "Exact active package digest to quarantine")
	return cmd
}

func addRecoveryFlags(cmd *cobra.Command, opts *recoveryOptions, allowJSON bool) {
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL; no other app config is loaded)")
	if allowJSON {
		cmd.Flags().BoolVar(&opts.JSON, "json", false, "Print machine-readable JSON")
	}
}

func withRecoveryRepository(ctx context.Context, databaseURL string, run func(recoveryRepository) error) error {
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
	return run(&postgresRecoveryRepository{pool: pool})
}

func (s *postgresRecoveryRepository) List(ctx context.Context) ([]recoveryExtension, error) {
	rows, err := s.pool.Query(ctx, recoveryExtensionSelectSQL()+` ORDER BY extensions.type, extensions.id`)
	if err != nil {
		return nil, fmt.Errorf("list extension recovery state: %w", err)
	}
	defer rows.Close()
	items := []recoveryExtension{}
	for rows.Next() {
		item, err := scanRecoveryExtension(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *postgresRecoveryRepository) Disable(ctx context.Context, extensionID string) (recoveryExtension, error) {
	if extensionID == "" {
		return recoveryExtension{}, errRecoveryExtensionNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return recoveryExtension{}, fmt.Errorf("begin extension recovery disable: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var item recoveryExtension
	publication, err := extensions.PublishPluginRuntimeRecoveryTx(ctx, tx, func() ([]string, error) {
		var scanErr error
		item, scanErr = scanRecoveryExtension(tx.QueryRow(
			ctx,
			recoveryExtensionSelectSQL()+` WHERE extensions.id = $1 FOR UPDATE OF extensions`,
			extensionID,
		))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, errRecoveryExtensionNotFound
		}
		if scanErr != nil {
			return nil, scanErr
		}
		if item.Source == "builtin" || item.IsSystem {
			return nil, errRecoveryProtected
		}
		if err := disableRecoveryExtensions(ctx, tx, []recoveryExtension{item}, "disable_one"); err != nil {
			return nil, err
		}
		return []string{item.ID}, nil
	})
	if err != nil {
		return recoveryExtension{}, err
	}
	if err := commitRecoveryPublication(
		ctx, tx, extensions.NewPostgresStore(s.pool), publication,
	); err != nil {
		return recoveryExtension{}, fmt.Errorf("commit extension recovery disable: %w", err)
	}
	item.Status = "disabled"
	return item, nil
}

func (s *postgresRecoveryRepository) DisableAllThirdParty(ctx context.Context) ([]recoveryExtension, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin extension recovery disable all: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	items := []recoveryExtension{}
	publication, err := extensions.PublishPluginRuntimeRecoveryTx(ctx, tx, func() ([]string, error) {
		rows, queryErr := tx.Query(ctx, recoveryExtensionSelectSQL()+`
			WHERE extensions.source <> 'builtin' AND extensions.is_system = false
			ORDER BY extensions.id FOR UPDATE OF extensions
		`)
		if queryErr != nil {
			return nil, fmt.Errorf("list third-party extensions for recovery: %w", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanRecoveryExtension(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rows.Close()
		if err := disableRecoveryExtensions(ctx, tx, items, "disable_all"); err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		return ids, nil
	})
	if err != nil {
		return nil, err
	}
	if err := commitRecoveryPublication(
		ctx, tx, extensions.NewPostgresStore(s.pool), publication,
	); err != nil {
		return nil, fmt.Errorf("commit extension recovery disable all: %w", err)
	}
	for index := range items {
		items[index].Status = "disabled"
	}
	return items, nil
}

func (s *postgresRecoveryRepository) QuarantineProtected(
	ctx context.Context,
	extensionID string,
	expectedVersion string,
	expectedDigest string,
) (recoveryExtension, error) {
	if extensionID == "" {
		return recoveryExtension{}, errRecoveryExtensionNotFound
	}
	if expectedVersion == "" || !recoveryDigestPattern.MatchString(expectedDigest) {
		return recoveryExtension{}, errRecoveryArtifactMismatch
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return recoveryExtension{}, fmt.Errorf("begin protected extension quarantine: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var item recoveryExtension
	publication, err := extensions.PublishProtectedPluginRuntimeQuarantineTx(ctx, tx, func() ([]string, error) {
		var scanErr error
		item, scanErr = scanRecoveryExtension(tx.QueryRow(
			ctx,
			recoveryExtensionSelectSQL()+` WHERE extensions.id = $1 FOR UPDATE OF extensions`,
			extensionID,
		))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, errRecoveryExtensionNotFound
		}
		if scanErr != nil {
			return nil, scanErr
		}
		if item.Source != "builtin" && !item.IsSystem {
			return nil, errRecoveryNotProtected
		}
		if item.Status != "enabled" {
			return nil, errRecoveryNotEnabled
		}
		if item.Version != expectedVersion || item.PackageDigest != expectedDigest {
			return nil, fmt.Errorf(
				"%w: active=%s@%s requested=%s@%s",
				errRecoveryArtifactMismatch,
				item.Version,
				item.PackageDigest,
				expectedVersion,
				expectedDigest,
			)
		}
		if err := disableRecoveryExtensions(ctx, tx, []recoveryExtension{item}, "quarantine_protected_exact"); err != nil {
			return nil, err
		}
		return []string{item.ID}, nil
	})
	if err != nil {
		return recoveryExtension{}, err
	}
	if err := commitRecoveryPublication(
		ctx, tx, extensions.NewPostgresStore(s.pool), publication,
	); err != nil {
		return recoveryExtension{}, fmt.Errorf("commit protected extension quarantine: %w", err)
	}
	item.Status = "disabled"
	return item, nil
}

func commitRecoveryPublication(
	ctx context.Context,
	tx recoveryCommitter,
	reader recoveryPublicationReader,
	expected extensions.PluginRuntimePublication,
) error {
	if err := tx.Commit(ctx); err != nil {
		commitErr := err
		// A transport failure can leave COMMIT outcome unknown. The immutable
		// revision is transaction-local evidence that every recovery write won.
		verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		stored, verifyErr := reader.PluginRuntimePublicationByRevision(verifyCtx, expected.Revision)
		if verifyErr == nil && sameRecoveryPublication(stored, expected) {
			return nil
		}
		if verifyErr == nil {
			verifyErr = errors.New("committed recovery publication differs from expected evidence")
		} else {
			verifyErr = fmt.Errorf("verify recovery publication commit: %w", verifyErr)
		}
		return errors.Join(commitErr, verifyErr)
	}
	return nil
}

type recoveryCommitter interface {
	Commit(context.Context) error
}

type recoveryPublicationReader interface {
	PluginRuntimePublicationByRevision(
		context.Context,
		int64,
	) (extensions.PluginRuntimePublication, error)
}

func sameRecoveryPublication(left, right extensions.PluginRuntimePublication) bool {
	return left.Revision == right.Revision &&
		left.MemberCount == right.MemberCount &&
		left.MembersDigest == right.MembersDigest &&
		left.Reason == right.Reason &&
		left.ActorUserID == right.ActorUserID &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		slices.Equal(left.Members, right.Members)
}

func disableRecoveryExtensions(ctx context.Context, tx pgx.Tx, items []recoveryExtension, mode string) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE extensions SET status = 'disabled', updated_at = now() WHERE id = ANY($1::text[])`, ids); err != nil {
			return fmt.Errorf("disable recovery extensions: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mail_provider_selection WHERE extension_id = ANY($1::text[])`, ids); err != nil {
			return fmt.Errorf("clear recovery mail providers: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_events (extension_id, action, message)
			SELECT unnest($1::text[]), 'disabled', 'Disabled by out-of-band CLI recovery.'
		`, ids); err != nil {
			return fmt.Errorf("record recovery extension events: %w", err)
		}
	}
	metadata, err := json.Marshal(map[string]any{"mode": mode, "extensionIds": ids, "count": len(ids)})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (action, metadata) VALUES ('extension.cli_recovery', $1::jsonb)`, string(metadata)); err != nil {
		return fmt.Errorf("record CLI recovery audit: %w", err)
	}
	return nil
}

func recoveryExtensionSelectSQL() string {
	return `
		SELECT extensions.id, extensions.name, extensions.type, extensions.status,
		       extensions.source, extensions.is_system,
		       COALESCE(extension_versions.version, ''),
		       COALESCE(extension_versions.package_digest, '')
		FROM extensions
		LEFT JOIN extension_versions ON extension_versions.id = extensions.active_version_id
	`
}

type recoveryScanner interface {
	Scan(...any) error
}

func scanRecoveryExtension(scanner recoveryScanner) (recoveryExtension, error) {
	var item recoveryExtension
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.Type, &item.Status, &item.Source,
		&item.IsSystem, &item.Version, &item.PackageDigest,
	); err != nil {
		return recoveryExtension{}, err
	}
	return item, nil
}
