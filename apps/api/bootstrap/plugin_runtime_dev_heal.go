package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	devhygiene "github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	"github.com/keeptrue/sforum/apps/api/config"
)

// devSelfHealMaxMembers 是防御性重试上界，**不是**「能修好几个成员」的能力。
//
// 发布期闸门校验的是整份 full-set：只要 revision 里还有另一个成员不完整，单成员
// repair 追加的新 revision 就会被闸门拒绝，函数上抛并保持 fail-closed。因此现状是
// 「一次启动最多成功自愈一个成员」，且前提是同 revision 里没有别的坏成员；两个成员
// 同坏、只有一个有候选时，第一次 repair 就会被拒，healed=0。这个上限只用来防止
// 极端情况下反复尝试（例如候选行在并发里被换掉），不是多成员批量修复。
//
// 要覆盖多成员，正确方向是「一条 revision 批量替换所有可自愈成员」，不是加 reason
// 豁免或 verify 开关。
const devSelfHealMaxMembers = 8

// devSelfHealAuditAction 是自动修复的审计动作。与 CLI 的 extension.artifact_repair
// 分开：一个来自 host 进程的 dev 通道，一个来自操作员显式命令，复盘时要分得开。
const devSelfHealAuditAction = "extension.dev_self_heal"

// devHealIdentityStore 是解析 dev-auto 系统账号所需的最小身份读取边界。
type devHealIdentityStore interface {
	GetCurrentUserByEmail(ctx context.Context, email string) (identity.CurrentUser, error)
}

// pluginRuntimePublicationVerifier 是启动期只读自检的窄边界（*extensions.PostgresStore
// 实现），自愈完成后要再跑一次同一判据。
type pluginRuntimePublicationVerifier interface {
	VerifyPluginRuntimePublicationArtifacts(ctx context.Context, revision int64) error
}

// pluginRuntimeDevHealedMember 是一次自动修复的现场记录。
type pluginRuntimeDevHealedMember struct {
	ExtensionID string
	Version     string
	FromDigest  string
	ToDigest    string
	Revision    int64
}

type pluginRuntimeDevHealOutcome struct {
	Healed []pluginRuntimeDevHealedMember
}

// pluginRuntimePublicationHealer 是启动期 dev 自愈的窄边界。
type pluginRuntimePublicationHealer interface {
	HealPluginRuntimePublication(ctx context.Context) (pluginRuntimeDevHealOutcome, error)
}

type pluginRuntimeDevHealConfig struct {
	pool          *pgxpool.Pool
	store         *extensions.PostgresStore
	extensionRoot string
	builtinRoot   string
	externalRoots []string
	identities    devHealIdentityStore
	logger        *slog.Logger
	appEnv        string
}

type pluginRuntimeDevHealer struct {
	cfg pluginRuntimeDevHealConfig
}

func newPluginRuntimeDevHealer(cfg pluginRuntimeDevHealConfig) *pluginRuntimeDevHealer {
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return &pluginRuntimeDevHealer{cfg: cfg}
}

// maybeHealPluginRuntimePublication 是启动期自愈的**唯一**调用点。
//
// 开关与既有 dev 通道同一判据：devhygiene.ShouldEnableDevelopmentOrphanReaper(cfg.AppEnv)
// && !cfg.SafeMode。生产与 Safe Mode 在调用 healer 之前就短路，因此自愈函数在那两种
// 配置下根本没有被调用（测试用探针 healer 断言调用数为 0）。
//
// 只有「带四坐标的制品缺陷」才尝试自愈：数据库/事务类错误不属于这一族，原样上抛。
// 自愈失败或没有自洽候选时，返回原始缺陷让启动继续走既有 recovery-only，绝不回退
// 成「先发布后修」。
func maybeHealPluginRuntimePublication(
	ctx context.Context,
	cfg config.Config,
	healer pluginRuntimePublicationHealer,
	verifier pluginRuntimePublicationVerifier,
	logger *slog.Logger,
	verificationErr error,
) error {
	if verificationErr == nil {
		return nil
	}
	if !devhygiene.ShouldEnableDevelopmentOrphanReaper(cfg.AppEnv) || cfg.SafeMode {
		return verificationErr
	}
	if healer == nil || verifier == nil {
		return verificationErr
	}
	if _, ok := artifactintegrity.Coordinates(verificationErr); !ok {
		return verificationErr
	}
	if logger == nil {
		logger = slog.Default()
	}
	outcome, healErr := healer.HealPluginRuntimePublication(ctx)
	if healErr != nil {
		logger.WarnContext(ctx, "development plugin runtime self-heal failed; staying fail-closed",
			"error", healErr)
		return verificationErr
	}
	if len(outcome.Healed) == 0 {
		logger.InfoContext(ctx, "development plugin runtime self-heal found no self-consistent candidate; staying fail-closed",
			"defect", verificationErr)
		return verificationErr
	}
	// 自愈写完也要过同一只读自检：发布期闸门只保证「写进去的是好的」，
	// 这里确认「盘上现在这批成员都还能装载」。
	if verifyErr := verifier.VerifyPluginRuntimePublicationArtifacts(ctx, 0); verifyErr != nil {
		logger.WarnContext(ctx, "development plugin runtime self-heal did not restore a loadable desired set",
			"error", verifyErr,
			"healed", len(outcome.Healed),
		)
		return verifyErr
	}
	return nil
}

// HealPluginRuntimePublication 把坏成员重指向同 extensionId+version 的另一份自洽制品。
//
// 只做三件事：读出当前 full-set 找到坏成员、找到同版本自洽候选、追加一条
// reason=recovery 的 revision（走 PublishPluginRuntimeArtifactRepairTx，因此与
// lifecycle 共用 advisory lock，并且必须通过发布期完整性闸门）。
//
// 它绝不往盘上写任何文件，也绝不修改既有 extension_versions 行；候选只在
// 「盘上有、durable 行没有」时补登记一行（新 digest 才允许有新行）。
func (h *pluginRuntimeDevHealer) HealPluginRuntimePublication(
	ctx context.Context,
) (pluginRuntimeDevHealOutcome, error) {
	var outcome pluginRuntimeDevHealOutcome
	if h == nil || h.cfg.store == nil || h.cfg.pool == nil {
		return outcome, nil
	}
	// 防御性上界，不是多成员能力：每次 publish 都会重读 latest 并整份过闸门，
	// 只要还有别的成员不完整，第一次 publish 就会被拒并上抛。详见常量注释。
	for range devSelfHealMaxMembers {
		broken, ok, err := h.firstBrokenMember(ctx)
		if err != nil {
			return outcome, err
		}
		if !ok {
			return outcome, nil
		}
		candidateVersionID, ok, err := h.ensureCandidateVersionID(ctx, broken.Member)
		if err != nil {
			return outcome, err
		}
		if !ok {
			// 同版本没有自洽候选：停手。调用方保持 fail-closed。
			return outcome, nil
		}
		healed, err := h.publishRepair(ctx, broken.Member, candidateVersionID)
		if err != nil {
			return outcome, err
		}
		outcome.Healed = append(outcome.Healed, healed)
		h.cfg.logger.WarnContext(ctx, "development plugin runtime self-heal published recovery revision",
			"extensionId", healed.ExtensionID,
			"version", healed.Version,
			"fromDigest", healed.FromDigest,
			"toDigest", healed.ToDigest,
			"revision", healed.Revision,
			"appEnv", h.cfg.appEnv,
		)
	}
	return outcome, nil
}

func (h *pluginRuntimeDevHealer) firstBrokenMember(
	ctx context.Context,
) (extensions.PluginRuntimeArtifactMember, bool, error) {
	members, err := h.cfg.store.PluginRuntimeArtifactMembers(ctx, 0)
	if err != nil {
		return extensions.PluginRuntimeArtifactMember{}, false, fmt.Errorf(
			"load plugin runtime publication members for dev self-heal: %w", err)
	}
	for _, member := range members {
		if err := artifactintegrity.Verify(member.PackagePath, artifactintegrity.Member{
			ExtensionID: member.Member.ExtensionID,
			Version:     member.Member.ExtensionVersion,
			Digest:      member.Member.PackageDigest,
		}); err != nil {
			return member, true, nil
		}
	}
	return extensions.PluginRuntimeArtifactMember{}, false, nil
}

// ensureCandidateVersionID 返回同版本自洽制品对应的版本行 id。
//
// 先查 durable 行（CLI republish 的同一口径），再扫盘上根目录；盘上有、行没有时
// 补登记一行。候选必须与当前钉住的 digest 不同，否则修复没有意义。
func (h *pluginRuntimeDevHealer) ensureCandidateVersionID(
	ctx context.Context,
	member extensions.PluginRuntimeMember,
) (int64, bool, error) {
	if versionID, ok, err := h.candidateVersionRow(ctx, member); err != nil || ok {
		return versionID, ok, err
	}
	candidate, ok := devhygiene.FindSelfConsistentArtifact(
		member.ExtensionID, member.ExtensionVersion, member.PackageDigest, h.artifactRoots(),
	)
	if !ok {
		return 0, false, nil
	}
	// 只登记新 digest；已存在的同 digest 行不改路径（既有 durable 事实不动）。
	var versionID int64
	err := h.cfg.pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, $2, $3::jsonb, $4, $5)
		ON CONFLICT (extension_id, version, package_digest) DO NOTHING
		RETURNING id
	`, candidate.ExtensionID, candidate.Version, string(candidate.Manifest),
		candidate.Path, candidate.Digest).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 行已存在（并发或历史遗留）：只有它自己的路径也自洽才算候选。
		existing, path, ok, lookupErr := h.existingCandidateRow(ctx, candidate)
		if lookupErr != nil || !ok {
			return 0, false, lookupErr
		}
		if err := artifactintegrity.Verify(path, artifactintegrity.Member{
			ExtensionID: candidate.ExtensionID,
			Version:     candidate.Version,
			Digest:      candidate.Digest,
		}); err != nil {
			return 0, false, nil
		}
		return existing, true, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("register self-consistent artifact for dev self-heal: %w", err)
	}
	return versionID, true, nil
}

func (h *pluginRuntimeDevHealer) candidateVersionRow(
	ctx context.Context,
	member extensions.PluginRuntimeMember,
) (int64, bool, error) {
	rows, err := h.cfg.pool.Query(ctx, `
		SELECT id, COALESCE(package_path, ''), package_digest
		FROM extension_versions
		WHERE extension_id = $1
		  AND version = $2
		  AND id <> $3
		  AND package_digest <> $4
		ORDER BY installed_at DESC, id DESC
	`, member.ExtensionID, member.ExtensionVersion, member.ExtensionVersionID, member.PackageDigest)
	if err != nil {
		return 0, false, fmt.Errorf("load dev self-heal candidate versions: %w", err)
	}
	defer rows.Close()
	type row struct {
		id     int64
		path   string
		digest string
	}
	var candidates []row
	for rows.Next() {
		var candidate row
		if err := rows.Scan(&candidate.id, &candidate.path, &candidate.digest); err != nil {
			return 0, false, fmt.Errorf("scan dev self-heal candidate version: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("read dev self-heal candidate versions: %w", err)
	}
	for _, candidate := range candidates {
		if err := artifactintegrity.Verify(candidate.path, artifactintegrity.Member{
			ExtensionID: member.ExtensionID,
			Version:     member.ExtensionVersion,
			Digest:      candidate.digest,
		}); err == nil {
			return candidate.id, true, nil
		}
	}
	return 0, false, nil
}

func (h *pluginRuntimeDevHealer) existingCandidateRow(
	ctx context.Context,
	candidate devhygiene.ArtifactCandidate,
) (int64, string, bool, error) {
	var (
		versionID int64
		path      string
	)
	err := h.cfg.pool.QueryRow(ctx, `
		SELECT id, COALESCE(package_path, '')
		FROM extension_versions
		WHERE extension_id = $1 AND version = $2 AND package_digest = $3
	`, candidate.ExtensionID, candidate.Version, candidate.Digest).Scan(&versionID, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("load existing dev self-heal candidate row: %w", err)
	}
	return versionID, path, true, nil
}

// artifactRoots 给出候选搜索的根目录：已安装制品根、内置源根（storage/builtin-dev）、
// 以及每个外部源根。内置源必须在列：本次事故坏的就是内置插件。
func (h *pluginRuntimeDevHealer) artifactRoots() []devhygiene.ArtifactRoot {
	roots := make([]devhygiene.ArtifactRoot, 0, 2+len(h.cfg.externalRoots))
	if root := strings.TrimSpace(h.cfg.extensionRoot); root != "" {
		roots = append(roots, devhygiene.ArtifactRoot{Path: root, Layout: devhygiene.ArtifactRootDigests})
	}
	if root := strings.TrimSpace(h.cfg.builtinRoot); root != "" {
		roots = append(roots, devhygiene.ArtifactRoot{Path: root, Layout: devhygiene.ArtifactRootBuiltin})
	}
	for _, root := range h.cfg.externalRoots {
		if root = strings.TrimSpace(root); root != "" {
			roots = append(roots, devhygiene.ArtifactRoot{Path: root, Layout: devhygiene.ArtifactRootPackage})
		}
	}
	return roots
}

func (h *pluginRuntimeDevHealer) publishRepair(
	ctx context.Context,
	member extensions.PluginRuntimeMember,
	candidateVersionID int64,
) (pluginRuntimeDevHealedMember, error) {
	actorUserID := h.devAutoUserID(ctx)
	reason := fmt.Sprintf(
		"development self-heal: pinned artifact %s@%s digest %s is incomplete",
		member.ExtensionID, member.ExtensionVersion, member.PackageDigest,
	)
	tx, err := h.cfg.pool.Begin(ctx)
	if err != nil {
		return pluginRuntimeDevHealedMember{}, fmt.Errorf("begin development self-heal publication: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	publication, err := extensions.PublishPluginRuntimeArtifactRepairTx(ctx, tx, extensions.PluginRuntimeArtifactRepair{
		ExtensionID:        member.ExtensionID,
		CurrentVersionID:   member.ExtensionVersionID,
		CandidateVersionID: candidateVersionID,
		Reason:             reason,
		ActorUserID:        actorUserID,
	})
	if err != nil {
		return pluginRuntimeDevHealedMember{}, fmt.Errorf("publish development self-heal revision: %w", err)
	}
	healed := pluginRuntimeDevHealedMember{
		ExtensionID: member.ExtensionID,
		Version:     member.ExtensionVersion,
		FromDigest:  member.PackageDigest,
		Revision:    publication.Revision,
	}
	for _, repaired := range publication.Members {
		if repaired.ExtensionID == member.ExtensionID {
			healed.ToDigest = repaired.PackageDigest
		}
	}
	metadata, err := json.Marshal(map[string]any{
		"extensionId":         healed.ExtensionID,
		"version":             healed.Version,
		"fromVersionId":       member.ExtensionVersionID,
		"toVersionId":         candidateVersionID,
		"fromDigest":          healed.FromDigest,
		"toDigest":            healed.ToDigest,
		"publicationRevision": healed.Revision,
		"appEnv":              h.cfg.appEnv,
		"mode":                "development_self_heal",
	})
	if err != nil {
		return pluginRuntimeDevHealedMember{}, fmt.Errorf("encode development self-heal audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, $2, $3::jsonb)
	`, nullableDevHealActor(actorUserID), devSelfHealAuditAction, string(metadata)); err != nil {
		return pluginRuntimeDevHealedMember{}, fmt.Errorf("record development self-heal audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return pluginRuntimeDevHealedMember{}, fmt.Errorf("commit development self-heal publication: %w", err)
	}
	return healed, nil
}

// devAutoUserID 解析 dev 系统账号（dev-auto）的 id 作为审计归因；不存在时返回 0
// （audit.actor_user_id 为 NULL）。
//
// 只查不建：管理端删除/禁用 dev-auto 是关闭 dev 自动通道的显式动作，恢复路径
// 不能把它偷偷建回来。
func (h *pluginRuntimeDevHealer) devAutoUserID(ctx context.Context) int64 {
	if h.cfg.identities == nil {
		return 0
	}
	user, err := h.cfg.identities.GetCurrentUserByEmail(ctx, extensions.DevAutoEmail)
	if err != nil {
		return 0
	}
	return user.ID
}

func nullableDevHealActor(actorUserID int64) any {
	if actorUserID <= 0 {
		return nil
	}
	return actorUserID
}
