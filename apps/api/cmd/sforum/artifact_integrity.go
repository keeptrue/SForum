package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
)

// 退出码三态：0 一致/已修复，1 有缺陷或前提漂移（可操作状态），2 用法错误。
// main 通过 ExitCode() 识别，见 cli_exit.go。
const (
	artifactExitOK      = 0
	artifactExitDefect  = 1
	artifactExitUsage   = 2
	repairReasonMaxSize = 4096
)

type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }

func (e *exitCodeError) Unwrap() error { return e.err }

func (e *exitCodeError) ExitCode() int { return e.code }

func usageError(err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{code: artifactExitUsage, err: err}
}

func defectError(err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{code: artifactExitDefect, err: err}
}

// artifactMemberRow 是一份 publication 成员的完整定位信息。
type artifactMemberRow struct {
	Revision     int64  `json:"publicationRevision"`
	ExtensionID  string `json:"extensionId"`
	Version      string `json:"version"`
	Digest       string `json:"digest"`
	VersionID    int64  `json:"-"`
	PackagePath  string `json:"packagePath"`
	ArtifactPath string `json:"path,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

const artifactMemberQuery = `
	SELECT p.revision, m.extension_id, m.extension_version, m.package_digest,
	       m.extension_version_id, COALESCE(v.package_path, '')
	FROM plugin_runtime_publications p
	JOIN plugin_runtime_publication_members m ON m.publication_revision = p.revision
	LEFT JOIN extension_versions v ON v.id = m.extension_version_id
	WHERE p.revision = COALESCE(
		NULLIF($1::bigint, 0),
		(SELECT max(revision) FROM plugin_runtime_publications)
	)
	ORDER BY m.extension_id COLLATE "C"
`

func loadArtifactMembers(ctx context.Context, pool *pgxpool.Pool, revision int64) ([]artifactMemberRow, error) {
	rows, err := pool.Query(ctx, artifactMemberQuery, revision)
	if err != nil {
		return nil, fmt.Errorf("load plugin runtime publication members: %w", err)
	}
	defer rows.Close()
	members := []artifactMemberRow{}
	for rows.Next() {
		var member artifactMemberRow
		if err := rows.Scan(&member.Revision, &member.ExtensionID, &member.Version,
			&member.Digest, &member.VersionID, &member.PackagePath); err != nil {
			return nil, fmt.Errorf("scan plugin runtime publication member: %w", err)
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// resolveArtifactRoot 解析 durable 行里的制品路径。
//
// extension_versions.package_path 是写入时的相对路径（相对 API 进程工作目录
// apps/api），所以基准目录必须显式给出，不能依赖调用者的 cwd 恰好对。
func resolveArtifactRoot(root, packagePath string) string {
	if packagePath == "" || filepath.IsAbs(packagePath) {
		return packagePath
	}
	return filepath.Join(root, packagePath)
}

// inspectMember 用同一套 ArtifactIntegrity 判据填充缺陷字段。
func inspectMember(member *artifactMemberRow, root string) bool {
	err := artifactintegrity.Verify(resolveArtifactRoot(root, member.PackagePath), artifactintegrity.Member{
		ExtensionID: member.ExtensionID,
		Version:     member.Version,
		Digest:      member.Digest,
	})
	if err == nil {
		return true
	}
	if coordinates, ok := artifactintegrity.Coordinates(err); ok {
		member.ArtifactPath = coordinates.Path
		member.Reason = coordinates.Reason
		return false
	}
	member.Reason = "artifact is not loadable"
	return false
}

func withArtifactPool(ctx context.Context, databaseURL string, run func(*pgxpool.Pool) error) error {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		return usageError(errors.New("database url is empty: set DATABASE_URL or pass --database-url"))
	}
	pool, err := postgres.NewPool(ctx, databaseURL, 2)
	if err != nil {
		return fmt.Errorf("connect artifact verification database: %w", err)
	}
	defer pool.Close()
	return run(pool)
}

// newExtensionVerifyArtifactsCommand 是启动前/CI 的只读自检：只读 durable 行与
// 盘上制品，不启动 SForum、不加载插件、不写任何状态。
func newExtensionVerifyArtifactsCommand() *cobra.Command {
	var (
		databaseURL string
		revision    int64
		asJSON      bool
		root        string
	)
	cmd := &cobra.Command{
		Use:           "verify-artifacts",
		Short:         "Verify every artifact pinned by a plugin runtime publication (read-only)",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withArtifactPool(cmd.Context(), databaseURL, func(pool *pgxpool.Pool) error {
				members, err := loadArtifactMembers(cmd.Context(), pool, revision)
				if err != nil {
					return err
				}
				defects := make([]artifactMemberRow, 0)
				for index := range members {
					if !inspectMember(&members[index], root) {
						defects = append(defects, members[index])
					}
				}
				if asJSON {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					if err := encoder.Encode(map[string]any{
						"publicationRevision": members[0].RevisionOrZero(),
						"checked":             len(members),
						"defects":             defects,
					}); err != nil {
						return err
					}
				} else {
					cmd.Printf("checked %d artifact(s)\n", len(members))
					for _, defect := range defects {
						cmd.Printf("DEFECT %s@%s digest=%s path=%s reason=%s\n",
							defect.ExtensionID, defect.Version, defect.Digest,
							defect.ArtifactPath, defect.Reason)
					}
					if len(defects) == 0 {
						cmd.Println("all artifacts are loadable")
					}
				}
				if len(defects) > 0 {
					return defectError(fmt.Errorf("%d artifact(s) are not loadable", len(defects)))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL)")
	cmd.Flags().StringVar(&root, "root", ".", "Base directory for relative package paths recorded in durable rows")
	cmd.Flags().Int64Var(&revision, "publication-revision", 0, "Publication revision to verify (defaults to the latest)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	return cmd
}

// RevisionOrZero 让空结果也能输出修订号。
func (m artifactMemberRow) RevisionOrZero() int64 { return m.Revision }

// newRecoveryRepublishArtifactCommand 是「durable 行指向坏制品」的带外出口。
//
// 它必须自己看盘：现有 recheck 只比对 durable 行，坏制品会被原样重发一遍，
// 这正是本命令存在的理由。
func newRecoveryRepublishArtifactCommand() *cobra.Command {
	var (
		databaseURL  string
		reason       string
		confirmToken string
		asJSON       bool
		root         string
	)
	cmd := &cobra.Command{
		Use:           "republish-artifact <extension-id>",
		Short:         "Repoint a publication member at a self-consistent artifact of the same version",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			extensionID := strings.TrimSpace(args[0])
			reason = strings.TrimSpace(reason)
			if extensionID == "" {
				return usageError(errors.New("extension id is required"))
			}
			if reason == "" || len(reason) > repairReasonMaxSize {
				return usageError(fmt.Errorf("--reason is required and must be at most %d bytes", repairReasonMaxSize))
			}
			execute := strings.TrimSpace(confirmToken) != ""
			return withArtifactPool(cmd.Context(), databaseURL, func(pool *pgxpool.Pool) error {
				members, err := loadArtifactMembers(cmd.Context(), pool, 0)
				if err != nil {
					return err
				}
				if len(members) == 0 {
					return defectError(errors.New("no plugin runtime publication exists"))
				}
				revision := members[0].Revision
				var target *artifactMemberRow
				for index := range members {
					if members[index].ExtensionID == extensionID {
						target = &members[index]
						break
					}
				}
				if target == nil {
					return defectError(fmt.Errorf("%s is not a member of publication revision %d", extensionID, revision))
				}
				if inspectMember(target, root) {
					cmd.Printf("%s@%s artifact is loadable; nothing to republish\n", target.ExtensionID, target.Version)
					return nil
				}
				candidates, err := selfConsistentCandidates(cmd.Context(), pool, *target, root)
				if err != nil {
					return err
				}
				if asJSON {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					if err := encoder.Encode(map[string]any{
						"publicationRevision": revision,
						"defect":              target,
						"candidates":          candidates,
						"executed":            execute,
					}); err != nil {
						return err
					}
				} else {
					cmd.Printf("publication revision %d pins %s@%s digest=%s\n",
						revision, target.ExtensionID, target.Version, target.Digest)
					cmd.Printf("DEFECT path=%s reason=%s\n", target.ArtifactPath, target.Reason)
					if len(candidates) == 0 {
						cmd.Println("no self-consistent candidate for this version")
					}
					for _, candidate := range candidates {
						cmd.Printf("CANDIDATE versionId=%d digest=%s path=%s\n",
							candidate.VersionID, candidate.Digest, candidate.PackagePath)
					}
				}
				if len(candidates) == 0 {
					return defectError(fmt.Errorf(
						"%s@%s has no self-consistent artifact on disk; rebuild it before republishing",
						target.ExtensionID, target.Version))
				}
				if !execute {
					cmd.Println("dry run: re-run with --confirm-token <token> and --reason to execute")
					return defectError(errors.New("dry run: artifact defect reported"))
				}
				publication, err := executeArtifactRepair(cmd.Context(), pool, *target, candidates[0], reason)
				if err != nil {
					return err
				}
				cmd.Printf("republished %s@%s digest=%s as revision %d\n",
					target.ExtensionID, target.Version, candidates[0].Digest, publication.Revision)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&databaseURL, "database-url", "", "PostgreSQL URL (defaults to DATABASE_URL)")
	cmd.Flags().StringVar(&root, "root", ".", "Base directory for relative package paths recorded in durable rows")
	cmd.Flags().StringVar(&reason, "reason", "", "Why the republish is needed (required, at most 4096 bytes)")
	cmd.Flags().StringVar(&confirmToken, "confirm-token", "", "Confirmation token; without it the command only reports")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	return cmd
}

func selfConsistentCandidates(
	ctx context.Context,
	pool *pgxpool.Pool,
	target artifactMemberRow,
	root string,
) ([]artifactMemberRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, version, package_digest, COALESCE(package_path, '')
		FROM extension_versions
		WHERE extension_id = $1 AND version = $2 AND id <> $3
		ORDER BY id DESC
	`, target.ExtensionID, target.Version, target.VersionID)
	if err != nil {
		return nil, fmt.Errorf("load candidate artifacts: %w", err)
	}
	defer rows.Close()
	candidates := []artifactMemberRow{}
	for rows.Next() {
		candidate := artifactMemberRow{
			Revision:    target.Revision,
			ExtensionID: target.ExtensionID,
			Version:     target.Version,
		}
		if err := rows.Scan(&candidate.VersionID, &candidate.Version,
			&candidate.Digest, &candidate.PackagePath); err != nil {
			return nil, fmt.Errorf("scan candidate artifact: %w", err)
		}
		if inspectMember(&candidate, root) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, rows.Err()
}

func executeArtifactRepair(
	ctx context.Context,
	pool *pgxpool.Pool,
	target artifactMemberRow,
	candidate artifactMemberRow,
	reason string,
) (extensions.PluginRuntimePublication, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return extensions.PluginRuntimePublication{}, fmt.Errorf("begin artifact repair: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	publication, err := extensions.PublishPluginRuntimeArtifactRepairTx(ctx, tx, extensions.PluginRuntimeArtifactRepair{
		ExtensionID:        target.ExtensionID,
		CurrentVersionID:   target.VersionID,
		CandidateVersionID: candidate.VersionID,
		Reason:             reason,
	})
	if err != nil {
		if errors.Is(err, extensions.ErrPluginRuntimeArtifactRepairConflict) {
			return extensions.PluginRuntimePublication{}, defectError(err)
		}
		return extensions.PluginRuntimePublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return extensions.PluginRuntimePublication{}, fmt.Errorf("commit artifact repair: %w", err)
	}
	return publication, nil
}
