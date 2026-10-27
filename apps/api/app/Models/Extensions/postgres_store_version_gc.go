package extensions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// Version GC（扩展版本垃圾回收）：显式、可审计、fail-closed 的历史版本回收。
//
// 铁律（任务书 2026-10-17-extension-version-gc 阶段 3）：
//  1. 先删磁盘目录、成功后才删台账行；磁盘删除失败必须中止该条并报告，
//     绝不产生新的 artifact_missing 悬挂态。
//  2. 逐条锁定候选并在锁内复核判据（防 TOCTOU 与并发 enable/upgrade/rollback）。
//  3. 两个 append-only 发布账本都会永久钉住版本行，GC 从不删除发布历史
//     （运行时行为优先于任务书方案 A 的"过期修订删除"，见决策记录
//     2026-10-17-version-gc-publication-restrict-cooperation）：
//     - plugin_runtime_publication_members 对版本行是复合 FK ON DELETE
//       RESTRICT，且被 202607160027 的 append-only 触发器保护（UPDATE/DELETE
//       一律 RAISE），数据库层就是最后一道背线。
//     - theme_runtime_publications 的 source_* 三元组是裸 TEXT（迁移
//       202607150020，无 FK），数据库层挡不住删除；但 ThemeRuntimeWatcher
//       每次进程启动都要用**最新修订**的 source 三元组回查版本行
//       （themePublicationSource → GetExtensionVersion），行不在就
//       ErrExtensionVersionNotFound、apply 永久失败（2026-10-27 dev 库
//       revision 20 事故）。因此主题发布引用必须由应用层判据补齐。
//     被任一账本引用过的版本行都报告为 blocked: publication_history。
//  4. 主题发布按**全历史**钉住，不做"只钉最新修订"的优化：账本是 append-only
//     证据，任何修订引用的精确三元组都应保持可解析，这与插件账本的钉住语义
//     一致。代价是主题历史版本一旦成为某次激活的 source 就不再回收——这是
//     刻意的 fail-closed 取舍（见 knowledge/sessions/
//     2026-10-27-version-gc-theme-publication-pin.md）。
//  5. 开放的 extension_lifecycle_operations 绝不清理；它以不可变快照列引用
//     版本身份、从不钉住版本行，因此只作为判据挡板而非清理对象。

// VersionGCBlocked* 是 dry-run/清单里 blockedReason 的封闭词表。
const (
	// VersionGCBlockedPublicationHistory 版本行被 append-only 发布账本引用：
	// 插件运行时 desired 集（plugin_runtime_publication_members，DB 层
	// FK RESTRICT）或主题运行时 desired 状态（theme_runtime_publications 的
	// source_* 三元组，应用层判据）。两者共用同一个词表值，理由：
	//  1. 该值已是对外契约（contracts/openapi/schemas/extensions.yaml 的 enum +
	//     apps/web 的 AdminVersionGCBlockedReason union），新增值要同步改契约与
	//     前端，而"被发布历史钉住、永久不可回收"的运维含义完全一致；
	//  2. 具体是哪个账本钉住它是实现细节，CLI 与管理端不需要据此分流。
	VersionGCBlockedPublicationHistory = "publication_history"
	// VersionGCBlockedOpenOperation 有开放 lifecycle operation 以该版本为目标。
	VersionGCBlockedOpenOperation = "open_operation"
	// VersionGCBlockedWithinRetention 仍在保留窗口内（active + N 个最近历史 /
	// 最近 N 天）。
	VersionGCBlockedWithinRetention = "within_retention"
	// VersionGCBlockedDiskMissing 行在、盘缺：不在 GC 语义内，走缺失制品清理。
	VersionGCBlockedDiskMissing = "disk_missing"
)

const (
	// 默认策略：每 extension 保留 active + 1 个最近历史版本（任务书阶段 3）。
	versionGCDefaultRetain = 1
	// retainDays 上限（100 年）：只防 duration 溢出，不是产品约束。
	versionGCMaxRetainDays = 36500
	// 单次清单/执行的候选上限：GC 是显式运维动作，超大结果应分批加
	// --extension-id 处理，而不是一次吞掉。
	versionGCMaxCandidates = 1000
	// 磁盘暂存名后缀：先改名再删，保证"删盘失败可恢复原名"。
	versionGCStagingSuffix = ".gc-staging"
	// 确认 token 有效期。与 trust challenge 同为一次性语义；GC 的计划审阅
	// （dry-run → execute 两段）允许比 5 分钟更宽，取 15 分钟。
	versionGCConfirmationTTL = 15 * time.Minute
)

var (
	ErrVersionGCInvalidInput           = errors.New("extensions: version gc invalid input")
	ErrVersionGCConfirmationInvalid    = errors.New("extensions: version gc confirmation token is missing, expired, replayed, or does not match the current plan")
	ErrVersionGCStagingRestoreConflict = errors.New("extensions: version gc could not restore the staging directory back to its original path")
)

// Version GC 管理端入口（阶段 5）的错误码；文案在 Support/Localization/messages.go。
const (
	// CodeVersionGCUnavailable 管理端路由在服务边界未装配时 fail-closed。
	CodeVersionGCUnavailable = "extension.version_gc_unavailable"
	// CodeVersionGCConfirmationInvalid token 缺失/过期/重放/与当前计划不一致。
	CodeVersionGCConfirmationInvalid = "extension.version_gc_confirmation_invalid"
)

// 审计 mode 词表：同一次删除在 audit_events.metadata.mode 与
// extension_version_gc_removals.requested_by_user_id 上区分来源。
const (
	versionGCAuditModeCLI = "cli"
	versionGCAuditModeAPI = "api"
)

// VersionGCOptions 是保留策略输入；Retain<=0 归一化为默认 1。
type VersionGCOptions struct {
	Retain      int    `json:"retain"`
	RetainDays  int    `json:"retainDays"`
	ExtensionID string `json:"extensionId,omitempty"`
}

// RetiredVersionCandidate 是一条历史版本行的回收评估结果；Reclaimable=false
// 时 BlockedReason 说明挡板（disk_missing 行指向缺失制品清理流程）。
type RetiredVersionCandidate struct {
	ExtensionID   string    `json:"extensionId"`
	ExtensionType string    `json:"extensionType"`
	ExtensionName string    `json:"extensionName"`
	VersionID     int64     `json:"versionId"`
	Version       string    `json:"version"`
	PackageDigest string    `json:"packageDigest"`
	PackagePath   string    `json:"packagePath"`
	InstalledAt   time.Time `json:"installedAt"`
	HistoryRank   int       `json:"historyRank"`
	Reclaimable   bool      `json:"reclaimable"`
	BlockedReason string    `json:"blockedReason,omitempty"`
}

// VersionGCConfirmation 是 dry-run 签发的一次性执行确认；明文 Token 只在此
// 返回一次，库内只存 sha256 摘要。
type VersionGCConfirmation struct {
	Token          string    `json:"token"`
	PlanDigest     string    `json:"planDigest"`
	CandidateCount int       `json:"candidateCount"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// VersionGCSkip 是执行时按原因跳过（未删除）的候选。
type VersionGCSkip struct {
	Candidate RetiredVersionCandidate `json:"candidate"`
	Reason    string                  `json:"reason"`
}

// VersionGCItemError 是执行失败（台账保留、磁盘已恢复原名）的候选。
type VersionGCItemError struct {
	Candidate RetiredVersionCandidate `json:"candidate"`
	Error     string                  `json:"error"`
}

// VersionGCSweepResult 是一次执行的整体结果。
type VersionGCSweepResult struct {
	Removed        []RetiredVersionCandidate `json:"removed"`
	Skipped        []VersionGCSkip           `json:"skipped"`
	Failed         []VersionGCItemError      `json:"failed"`
	Warnings       []string                  `json:"warnings,omitempty"`
	BytesFreed     int64                     `json:"bytesFreed"`
	CandidateCount int                       `json:"candidateCount"`
}

// normalized 复制并归一化选项：Retain<=0 → 默认 1；RetainDays 负值归零、
// 超上限截断；ExtensionID trim。
func (o VersionGCOptions) normalized() VersionGCOptions {
	normalized := o
	if normalized.Retain <= 0 {
		normalized.Retain = versionGCDefaultRetain
	}
	if normalized.RetainDays < 0 {
		normalized.RetainDays = 0
	}
	if normalized.RetainDays > versionGCMaxRetainDays {
		normalized.RetainDays = versionGCMaxRetainDays
	}
	normalized.ExtensionID = strings.TrimSpace(normalized.ExtensionID)
	return normalized
}

// versionGCBlockedReason 是 GC 判据的纯函数核心：发布历史（插件/主题任一账本）
// > 开放操作 > 保留窗口；返回空串表示可回收（磁盘存在性由调用方用
// artifactState 判定，归一为 disk_missing）。
func versionGCBlockedReason(
	published, openOperation bool,
	historyRank, retain, retainDays int,
	installedAt, now time.Time,
) string {
	if published {
		return VersionGCBlockedPublicationHistory
	}
	if openOperation {
		return VersionGCBlockedOpenOperation
	}
	if historyRank <= retain {
		return VersionGCBlockedWithinRetention
	}
	if retainDays > 0 && installedAt.After(now.Add(-time.Duration(retainDays)*24*time.Hour)) {
		return VersionGCBlockedWithinRetention
	}
	return ""
}

// versionGCCandidatesSQL 枚举全部历史版本行（非 active、非 staged），携带
// 发布引用（插件账本 + 主题账本）与开放操作两个挡板位。保留窗口在 Go 侧按
// history_rank/installed_at 判定，便于 CLI 与未来管理端复用同一判据函数。
const versionGCCandidatesSQL = `
WITH historical AS (
	SELECT v.id, v.extension_id, v.version, v.package_digest, v.package_path,
	       v.installed_at,
	       row_number() OVER (
	         PARTITION BY v.extension_id
	         ORDER BY v.installed_at DESC, v.id DESC
	       ) AS history_rank
	FROM extension_versions AS v
	JOIN extensions AS e ON e.id = v.extension_id
	WHERE v.id IS DISTINCT FROM e.active_version_id
	  AND v.id IS DISTINCT FROM e.staged_version_id
	  AND ($1::text IS NULL OR v.extension_id = $1)
)
SELECT h.extension_id, e.type, e.name, h.id, h.version, h.package_digest,
       h.package_path, h.installed_at, h.history_rank,
       (
         EXISTS (
           SELECT 1 FROM plugin_runtime_publication_members m
           WHERE m.extension_id = h.extension_id
             AND m.extension_version_id = h.id
             AND m.extension_version = h.version
             AND m.package_digest = h.package_digest
         )
         OR EXISTS (
           SELECT 1 FROM theme_runtime_publications t
           WHERE t.source_theme_id = h.extension_id
             AND t.source_theme_version = h.version
             AND t.source_package_digest = h.package_digest
         )
       ) AS published,
       EXISTS (
         SELECT 1 FROM extension_lifecycle_operations o
         WHERE o.extension_id = h.extension_id
           AND o.extension_version = h.version
           AND o.package_digest = h.package_digest
           AND o.completed_at IS NULL
       ) AS open_operation
FROM historical AS h
JOIN extensions AS e ON e.id = h.extension_id
ORDER BY h.extension_id, h.installed_at DESC, h.id DESC
LIMIT $2
`

// ListVersionGCCandidates 输出回收候选清单（含 blocked 原因）。dry-run 与
// 执行共用同一查询与同一判据函数。
func (s *PostgresStore) ListVersionGCCandidates(ctx context.Context, options VersionGCOptions) ([]RetiredVersionCandidate, error) {
	if s == nil || s.pool == nil {
		return nil, ErrVersionGCInvalidInput
	}
	opts := options.normalized()
	rows, err := s.pool.Query(ctx, versionGCCandidatesSQL, nullableVersionGCExtensionID(opts.ExtensionID), versionGCMaxCandidates)
	if err != nil {
		return nil, fmt.Errorf("list version gc candidates: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	candidates := make([]RetiredVersionCandidate, 0)
	for rows.Next() {
		var candidate RetiredVersionCandidate
		var published, openOperation bool
		if err := rows.Scan(
			&candidate.ExtensionID, &candidate.ExtensionType, &candidate.ExtensionName,
			&candidate.VersionID, &candidate.Version, &candidate.PackageDigest,
			&candidate.PackagePath, &candidate.InstalledAt, &candidate.HistoryRank,
			&published, &openOperation,
		); err != nil {
			return nil, fmt.Errorf("scan version gc candidate: %w", err)
		}
		candidate.BlockedReason = versionGCBlockedReason(
			published, openOperation, candidate.HistoryRank,
			opts.Retain, opts.RetainDays, candidate.InstalledAt, now,
		)
		if candidate.BlockedReason == "" && artifactState(candidate.PackagePath) == ArtifactMissing {
			// 行在、盘缺不在 GC 语义内：指向缺失制品清理流程。
			candidate.BlockedReason = VersionGCBlockedDiskMissing
		}
		candidate.Reclaimable = candidate.BlockedReason == ""
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate version gc candidates: %w", err)
	}
	return candidates, nil
}

// listVersionGCReclaimable 是 CreateVersionGCConfirmation / ExecuteVersionGCSweep
// 共用的"可回收子集"视图：磁盘必须存在，否则不属于 GC 语义。
func (s *PostgresStore) listVersionGCReclaimable(ctx context.Context, opts VersionGCOptions) ([]RetiredVersionCandidate, error) {
	candidates, err := s.ListVersionGCCandidates(ctx, opts)
	if err != nil {
		return nil, err
	}
	reclaimable := make([]RetiredVersionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Reclaimable {
			reclaimable = append(reclaimable, candidate)
		}
	}
	return reclaimable, nil
}

// versionGCPlanDigest 把选项 + 精确候选集哈希成计划摘要。token 与计划摘要
// 绑定：dry-run 与 execute 之间任何判据/清单/选项漂移都会让 token 失效。
func versionGCPlanDigest(opts VersionGCOptions, candidates []RetiredVersionCandidate) string {
	ordered := append([]RetiredVersionCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ExtensionID != ordered[j].ExtensionID {
			return ordered[i].ExtensionID < ordered[j].ExtensionID
		}
		return ordered[i].VersionID < ordered[j].VersionID
	})
	digest := sha256.New()
	fmt.Fprintf(digest, "sforum.version-gc.plan@1\nretain=%d\nretainDays=%d\nextensionId=%s\ncount=%d\n",
		opts.Retain, opts.RetainDays, opts.ExtensionID, len(ordered))
	for _, candidate := range ordered {
		fmt.Fprintf(digest, "%d\x00%s\x00%s\x00%s\x00%s\n",
			candidate.VersionID, candidate.ExtensionID, candidate.Version,
			candidate.PackageDigest, candidate.PackagePath)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func versionGCTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func nullableVersionGCExtensionID(extensionID string) any {
	if extensionID == "" {
		return nil
	}
	return extensionID
}

func nullableVersionGCActor(actorUserID int64) any {
	if actorUserID <= 0 {
		return nil
	}
	return actorUserID
}

// VersionGCAdminService 是阶段 5 管理端入口的最小读写边界：在 store 判据之上
// 强制 super_admin（任务书硬约束 4：CLI 与 API 入口均 super_admin——守卫层是
// 粗门，这里是权威策略），并把 actor 绑进审计（mode=api +
// requested_by_user_id）。带外 CLI 不经过它，走 ExecuteVersionGCSweep。
type VersionGCAdminService struct {
	store *PostgresStore
}

func NewVersionGCAdminService(store *PostgresStore) *VersionGCAdminService {
	return &VersionGCAdminService{store: store}
}

func (s *VersionGCAdminService) authorize(actor identity.Actor) error {
	if s == nil || s.store == nil || s.store.pool == nil {
		return ErrVersionGCInvalidInput
	}
	if !actor.IsSuperAdmin() {
		return identity.ErrPermissionDenied
	}
	return nil
}

// Candidates 输出回收候选清单（含 blocked 原因），仅 super_admin。
func (s *VersionGCAdminService) Candidates(ctx context.Context, actor identity.Actor, options VersionGCOptions) ([]RetiredVersionCandidate, error) {
	if err := s.authorize(actor); err != nil {
		return nil, err
	}
	return s.store.ListVersionGCCandidates(ctx, options)
}

// CreateConfirmation 为当前计划签发一次性执行确认 token，仅 super_admin。
func (s *VersionGCAdminService) CreateConfirmation(ctx context.Context, actor identity.Actor, options VersionGCOptions) (VersionGCConfirmation, error) {
	if err := s.authorize(actor); err != nil {
		return VersionGCConfirmation{}, err
	}
	return s.store.CreateVersionGCConfirmation(ctx, options)
}

// Execute 消费一次性 token 并执行回收；审计 mode=api 且锚点携带操作者。
func (s *VersionGCAdminService) Execute(ctx context.Context, actor identity.Actor, options VersionGCOptions, token string) (VersionGCSweepResult, error) {
	if err := s.authorize(actor); err != nil {
		return VersionGCSweepResult{}, err
	}
	return s.store.executeVersionGCSweep(ctx, options, token, actor.ID, versionGCAuditModeAPI)
}

// CreateVersionGCConfirmation 为当前计划签发一次性确认 token。库内只存
// token 摘要、计划摘要与过期时间；明文 token 仅出现在本次返回值里。
func (s *PostgresStore) CreateVersionGCConfirmation(ctx context.Context, options VersionGCOptions) (VersionGCConfirmation, error) {
	if s == nil || s.pool == nil {
		return VersionGCConfirmation{}, ErrVersionGCInvalidInput
	}
	opts := options.normalized()
	reclaimable, err := s.listVersionGCReclaimable(ctx, opts)
	if err != nil {
		return VersionGCConfirmation{}, err
	}
	planDigest := versionGCPlanDigest(opts, reclaimable)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return VersionGCConfirmation{}, fmt.Errorf("create version gc token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(versionGCConfirmationTTL)
	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		return VersionGCConfirmation{}, fmt.Errorf("marshal version gc options: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO extension_gc_confirmations (
		  token_hash, plan_digest, candidate_count, options, expires_at
		) VALUES ($1, $2, $3, $4::jsonb, $5)
	`, versionGCTokenHash(token), planDigest, len(reclaimable), string(optionsJSON), expiresAt,
	); err != nil {
		return VersionGCConfirmation{}, fmt.Errorf("record version gc confirmation: %w", err)
	}
	return VersionGCConfirmation{
		Token: token, PlanDigest: planDigest,
		CandidateCount: len(reclaimable), ExpiresAt: expiresAt,
	}, nil
}

// ExecuteVersionGCSweep 执行一次带外 CLI 版本回收：原子消费确认 token（一次性、
// 与当前计划摘要绑定），随后逐条"先删盘（改名暂存）→ 事务内锁行复核 → 删台账
// → 锚点 + 审计 → 移除暂存目录"。任一条失败只影响该条：台账保留、磁盘尽力
// 恢复原路径，错误可读地进入结果。审计 mode=cli、无 actor。
func (s *PostgresStore) ExecuteVersionGCSweep(ctx context.Context, options VersionGCOptions, token string) (VersionGCSweepResult, error) {
	return s.executeVersionGCSweep(ctx, options, token, 0, versionGCAuditModeCLI)
}

func (s *PostgresStore) executeVersionGCSweep(ctx context.Context, options VersionGCOptions, token string, actorUserID int64, auditMode string) (VersionGCSweepResult, error) {
	if s == nil || s.pool == nil {
		return VersionGCSweepResult{}, ErrVersionGCInvalidInput
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return VersionGCSweepResult{}, ErrVersionGCInvalidInput
	}
	opts := options.normalized()
	reclaimable, err := s.listVersionGCReclaimable(ctx, opts)
	if err != nil {
		return VersionGCSweepResult{}, err
	}
	planDigest := versionGCPlanDigest(opts, reclaimable)
	tag, err := s.pool.Exec(ctx, `
		UPDATE extension_gc_confirmations
		SET consumed_at = statement_timestamp()
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND expires_at > statement_timestamp()
		  AND plan_digest = $2
	`, versionGCTokenHash(token), planDigest)
	if err != nil {
		return VersionGCSweepResult{}, fmt.Errorf("consume version gc confirmation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return VersionGCSweepResult{}, ErrVersionGCConfirmationInvalid
	}

	// 三个结果数组显式初始化为空切片：nil 切片会序列化成 JSON null，违反
	// OpenAPI 的数组契约（真实事故：管理端对 null.length 抛 TypeError 误报失败）。
	result := VersionGCSweepResult{
		Removed:        []RetiredVersionCandidate{},
		Skipped:        []VersionGCSkip{},
		Failed:         []VersionGCItemError{},
		CandidateCount: len(reclaimable),
	}
	for _, candidate := range reclaimable {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome := s.reclaimOneVersion(ctx, candidate, opts, actorUserID, auditMode)
		switch {
		case outcome.err != nil:
			result.Failed = append(result.Failed, VersionGCItemError{Candidate: candidate, Error: outcome.err.Error()})
		case outcome.skipped:
			result.Skipped = append(result.Skipped, VersionGCSkip{Candidate: candidate, Reason: outcome.reason})
		default:
			result.Removed = append(result.Removed, candidate)
			result.BytesFreed += outcome.bytesFreed
			if outcome.warning != "" {
				result.Warnings = append(result.Warnings, outcome.warning)
			}
		}
	}
	return result, nil
}

type versionGCItemOutcome struct {
	removed    bool
	skipped    bool
	reason     string
	bytesFreed int64
	warning    string
	err        error
}

// errVersionGCTargetDrift 标记"锁内复核发现判据漂移"：该条按 skip 报告，
// 而不是失败。
type versionGCTargetDriftError struct{ reason string }

func (e *versionGCTargetDriftError) Error() string {
	return fmt.Sprintf("version gc target drifted: %s", e.reason)
}

// reclaimOneVersion 执行单条回收。磁盘与台账的失败方向：
//   - 改名失败 / 体积统计失败：该条中止，台账未动，磁盘未动；
//   - 事务失败：事务回滚（台账保留），暂存目录恢复原路径；恢复失败时错误
//     里显式携带暂存路径（绝不静默丢失目录）；
//   - 暂存目录删除失败：台账已正确提交，警告报告残留路径。
func (s *PostgresStore) reclaimOneVersion(ctx context.Context, candidate RetiredVersionCandidate, opts VersionGCOptions, actorUserID int64, auditMode string) versionGCItemOutcome {
	packagePath := candidate.PackagePath
	if artifactState(packagePath) == ArtifactMissing {
		// 清单与执行之间盘没了：不在 GC 语义内，留给缺失制品清理。
		return versionGCItemOutcome{skipped: true, reason: VersionGCBlockedDiskMissing}
	}
	bytesFreed, err := gcDirectorySize(packagePath)
	if err != nil {
		return versionGCItemOutcome{err: fmt.Errorf("measure package size: %w", err)}
	}
	stagingPath := packagePath + versionGCStagingSuffix
	if err := os.Rename(packagePath, stagingPath); err != nil {
		return versionGCItemOutcome{err: fmt.Errorf("stage package directory for removal: %w", err)}
	}
	if err := s.deleteReclaimableVersionRow(ctx, candidate, bytesFreed, actorUserID, auditMode); err != nil {
		if restoreErr := os.Rename(stagingPath, packagePath); restoreErr != nil {
			return versionGCItemOutcome{err: fmt.Errorf("%v; additionally the staged directory could not be restored: %v (path: %s)",
				err, restoreErr, stagingPath)}
		}
		var drift *versionGCTargetDriftError
		if errors.As(err, &drift) {
			return versionGCItemOutcome{skipped: true, reason: drift.reason}
		}
		return versionGCItemOutcome{err: err}
	}
	if err := os.RemoveAll(stagingPath); err != nil {
		return versionGCItemOutcome{
			removed: true, bytesFreed: bytesFreed,
			warning: fmt.Sprintf("version %s removed from the ledger but staging bytes remain at %s: %v",
				candidate.Version, stagingPath, err),
		}
	}
	return versionGCItemOutcome{removed: true, bytesFreed: bytesFreed}
}

// deleteReclaimableVersionRow 在单事务内锁行复核全部判据后删除版本行，并写
// 回收锚点与审计事件。插件发布引用的复合 FK RESTRICT 会让 DELETE 失败并整体
// 回滚——那是插件侧的最后一道 fail-closed 防线；主题发布账本没有 FK，下面的
// 锁内复核就是它唯一的防线。
func (s *PostgresStore) deleteReclaimableVersionRow(ctx context.Context, candidate RetiredVersionCandidate, bytesFreed int64, actorUserID int64, auditMode string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin version gc removal: %w", err)
	}
	defer tx.Rollback(ctx)

	var activeVersionID, stagedVersionID *int64
	err = tx.QueryRow(ctx, `
		SELECT e.active_version_id, e.staged_version_id
		FROM extension_versions AS v
		JOIN extensions AS e ON e.id = v.extension_id
		WHERE v.id = $1 AND v.extension_id = $2
		  AND v.version = $3 AND v.package_digest = $4
		FOR UPDATE OF v
	`, candidate.VersionID, candidate.ExtensionID, candidate.Version, candidate.PackageDigest,
	).Scan(&activeVersionID, &stagedVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &versionGCTargetDriftError{reason: "version row no longer exists"}
	}
	if err != nil {
		return fmt.Errorf("lock version row for gc: %w", err)
	}
	if (activeVersionID != nil && *activeVersionID == candidate.VersionID) ||
		(stagedVersionID != nil && *stagedVersionID == candidate.VersionID) {
		return &versionGCTargetDriftError{reason: "version became active or staged"}
	}

	var openOperations int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM extension_lifecycle_operations
		WHERE extension_id = $1 AND extension_version = $2
		  AND package_digest = $3 AND completed_at IS NULL
	`, candidate.ExtensionID, candidate.Version, candidate.PackageDigest,
	).Scan(&openOperations); err != nil {
		return fmt.Errorf("recheck open lifecycle operations: %w", err)
	}
	if openOperations > 0 {
		return &versionGCTargetDriftError{reason: VersionGCBlockedOpenOperation}
	}

	var publishedMembers int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM plugin_runtime_publication_members
		WHERE extension_id = $1 AND extension_version_id = $2
		  AND extension_version = $3 AND package_digest = $4
	`, candidate.ExtensionID, candidate.VersionID, candidate.Version, candidate.PackageDigest,
	).Scan(&publishedMembers); err != nil {
		return fmt.Errorf("recheck publication members: %w", err)
	}
	if publishedMembers > 0 {
		return &versionGCTargetDriftError{reason: VersionGCBlockedPublicationHistory}
	}

	// 主题发布账本没有 FK 背线：激活事务只锁 extensions 行、不锁 source 版本行，
	// 所以删除前必须在锁内重新读一次账本（与上面插件复核同源判据）。
	var themePublications int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM theme_runtime_publications
		WHERE source_theme_id = $1 AND source_theme_version = $2
		  AND source_package_digest = $3
	`, candidate.ExtensionID, candidate.Version, candidate.PackageDigest,
	).Scan(&themePublications); err != nil {
		return fmt.Errorf("recheck theme publication sources: %w", err)
	}
	if themePublications > 0 {
		return &versionGCTargetDriftError{reason: VersionGCBlockedPublicationHistory}
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM extension_versions
		WHERE id = $1 AND extension_id = $2 AND version = $3 AND package_digest = $4
	`, candidate.VersionID, candidate.ExtensionID, candidate.Version, candidate.PackageDigest)
	if err != nil {
		return fmt.Errorf("delete version row: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return &versionGCTargetDriftError{reason: "version row no longer exists"}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO extension_version_gc_removals (
		  extension_id, extension_version, package_digest, package_path,
		  bytes_freed, requested_by_user_id
		) VALUES ($1, $2, $3, $4, $5, $6)
	`, candidate.ExtensionID, candidate.Version, candidate.PackageDigest,
		candidate.PackagePath, bytesFreed, nullableVersionGCActor(actorUserID),
	); err != nil {
		return fmt.Errorf("record version gc removal anchor: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"extensionId":   candidate.ExtensionID,
		"version":       candidate.Version,
		"packageDigest": candidate.PackageDigest,
		"packagePath":   candidate.PackagePath,
		"bytesFreed":    bytesFreed,
		"mode":          auditMode,
	})
	if err != nil {
		return fmt.Errorf("marshal version gc audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ($1, $2::jsonb)
	`, audit.ActionExtensionVersionGC, string(metadata)); err != nil {
		return fmt.Errorf("record version gc audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version gc removal: %w", err)
	}
	return nil
}

// gcDirectorySize 统计目录字节数（回收前审计用）。
func gcDirectorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
