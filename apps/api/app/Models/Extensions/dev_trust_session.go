package extensions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	devhygiene "github.com/keeptrue/sforum/apps/api/app/Support/DevHygiene"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// DevTrustSession 是开发环境下 super_admin 一次性确认后授予的「会话级自动信任」：
// 之后同一 watch 循环内每个新 digest 由 GrantDevArtifact 自动写入 exact live grant。
// 仅 APP_ENV=development 且包位于 extensions/dev 时可用；其余情况一律 fail-closed
// 回到人工 trust challenge 路径。生产镜像没有 dev 包根，会话创建被 roots 校验拒绝。
type DevTrustSession struct {
	ID                string     `json:"id"`
	ExtensionID       string     `json:"extensionId"`
	PackageSlug       string     `json:"packageSlug"`
	RootPath          string     `json:"rootPath"`
	ActorUserID       int64      `json:"actorUserId"`
	GrantedAt         time.Time  `json:"grantedAt"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	LastPackageDigest string     `json:"lastPackageDigest,omitempty"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	RevokedReason     string     `json:"revokedReason,omitempty"`
}

// DevTrustSessionListInput 列表过滤；ExtensionID 为空表示全部（含已撤销）。
type DevTrustSessionListInput struct {
	ExtensionID    string
	IncludeRevoked bool
}

// DevTrustSessionGrantInput 创建会话：由携带确认 token 的 super_admin 请求发起。
type DevTrustSessionGrantInput struct {
	ExtensionID string
	PackageSlug string
	RootPath    string
	TTL         time.Duration
}

// DevTrustGrantInput 是 watch 循环每个新 digest 的自动信任写入请求。
type DevTrustGrantInput struct {
	ExtensionID   string
	PackageDigest string
	// ConfirmationToken 为空：自动路径依赖活跃会话而非一次性挑战。
}

var (
	ErrDevTrustDisabled        = errors.New("extensions: dev trust sessions require APP_ENV=development and a dev-root package")
	ErrDevTrustSessionNotFound = errors.New("extensions: dev trust session not found")
	ErrDevTrustSessionExists   = errors.New("extensions: an active dev trust session already exists for this package")
	ErrDevTrustSessionExpired  = errors.New("extensions: dev trust session expired")
	ErrDevTrustRootForbidden   = errors.New("extensions: dev trust root path is outside the allowed development roots")
)

const (
	CodeDevTrustDisabled        = "extension.dev_trust_disabled"
	CodeDevTrustSessionNotFound = "extension.dev_trust_session_not_found"
	CodeDevTrustSessionExists   = "extension.dev_trust_session_exists"
	CodeDevTrustRootForbidden   = "extension.dev_trust_root_forbidden"

	// DevTrustSessionDefaultTTL 是推荐默认值；配置可覆盖，UI 支持一键恢复默认。
	DevTrustSessionDefaultTTL = 8 * time.Hour
	// devTrustSessionMaxTTL 上限：会话是短命凭据，不允许配置成永久。
	devTrustSessionMaxTTL = 24 * time.Hour
)

// DevTrustSessionStore 持久化边界；生产实现为 Postgres（dev_trust_session_postgres.go）。
type DevTrustSessionStore interface {
	Create(ctx context.Context, session DevTrustSession) (DevTrustSession, error)
	List(ctx context.Context, input DevTrustSessionListInput) ([]DevTrustSession, error)
	Active(ctx context.Context, packageSlug string) (DevTrustSession, error)
	TouchDigest(ctx context.Context, sessionID, packageDigest string) error
	Revoke(ctx context.Context, sessionID string, actorUserID int64, reason string) error
	RevokeAllForExtension(ctx context.Context, extensionID string, actorUserID int64, reason string) error
	// RevokeAllActive 供 Safe Mode 启动时一次性吊销全部会话（开发装配专用）。
	RevokeAllActive(ctx context.Context, reason string) error
}

// DevSourceSyncer 是 dev 信任刷新前的外部源重扫描边界（仅开发装配注入）。
// Service.SyncExternalSources 把外部根（EXTERNAL_EXTENSION_ROOTS）下重建后的
// 新摘要快照并暂存为候选——没有这一步，dev 循环构建出的新 digest 只存在于
// 源码目录，运行中的 API 在下次重启前根本看不到它。
type DevSourceSyncer interface {
	SyncExternalSources(ctx context.Context) (ExternalSyncResult, error)
}

// DevTrustService 组合会话存储与 executable trust 写入路径。
// 独立小服务而不是塞回 Service/Manager God 对象（架构基线禁止增长）。
type DevTrustService struct {
	sessions DevTrustSessionStore
	trust    *ExecutableTrustService
	// devEnabled 与 watch loop / DevHygiene reaper 同一谓词。
	devEnabled bool
	// devSourceRoots 是允许的开发源码集合根（EXTERNAL_EXTENSION_ROOTS，
	// 仅 development 注入）；脚手架默认 extensions/dev 树始终允许。
	devSourceRoots []string
	// sourceSyncer 在 GrantDevArtifact 前重扫外部源完成暂存；nil 时跳过
	// （外部根之外的开发包仍依赖启动期扫描）。
	sourceSyncer DevSourceSyncer
	// safeMode 打开时 Create/Grant 全部 fail-closed（服务层权威，UI 只是辅助）。
	safeMode bool
	now      func() time.Time
}

func NewDevTrustService(sessions DevTrustSessionStore, trust *ExecutableTrustService, appEnv string) *DevTrustService {
	return &DevTrustService{
		sessions:   sessions,
		trust:      trust,
		devEnabled: devhygiene.ShouldEnableDevelopmentOrphanReaper(appEnv),
		now:        time.Now,
	}
}

func (s *DevTrustService) WithClock(now func() time.Time) *DevTrustService {
	s.now = now
	return s
}

// WithSafeMode 注入 Host Safe Mode；打开时一切会话操作 fail-closed。
func (s *DevTrustService) WithSafeMode(enabled bool) *DevTrustService {
	s.safeMode = enabled
	return s
}

// WithSourceSyncer 注入外部源重扫描边界；仅开发装配调用。
func (s *DevTrustService) WithSourceSyncer(syncer DevSourceSyncer) *DevTrustService {
	s.sourceSyncer = syncer
	return s
}

// WithDevSourceRoots 注入允许的开发源码集合根（EXTERNAL_EXTENSION_ROOTS）。
// 仅 development 装配调用；空白与相对路径项被忽略（与 Service 的外部根同语义）。
func (s *DevTrustService) WithDevSourceRoots(roots []string) *DevTrustService {
	seen := make(map[string]bool)
	s.devSourceRoots = nil
	for _, root := range roots {
		cleaned := filepath.Clean(strings.TrimSpace(root))
		if cleaned == "." || cleaned == "" || !filepath.IsAbs(cleaned) || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		s.devSourceRoots = append(s.devSourceRoots, cleaned)
	}
	return s
}

// RevokeAllOnSafeMode 在 Safe Mode 启动时一次性吊销全部会话，
// 确保 Safe Mode 结束后旧会话不会复活（逐包审计由调用方记录）。
func (s *DevTrustService) RevokeAllOnSafeMode(ctx context.Context) error {
	if s == nil || s.sessions == nil {
		return nil
	}
	return s.sessions.RevokeAllActive(ctx, "safe mode startup")
}

func newDevTrustSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate dev trust session id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Create 在 super_admin 确认后建立会话。rootPath 必须位于允许的开发源码根
// （配置的 EXTERNAL_EXTENSION_ROOTS 或脚手架默认 extensions/dev）之内，
// 且该根下 manifest 的 ID 必须与 extensionID 一致，否则拒绝。
func (s *DevTrustService) Create(
	ctx context.Context,
	actor identity.Actor,
	input DevTrustSessionGrantInput,
) (DevTrustSession, error) {
	if err := s.requireDevTrustEligible(actor, input.RootPath); err != nil {
		return DevTrustSession{}, err
	}
	extension, err := s.trust.extension(ctx, input.ExtensionID)
	if err != nil {
		return DevTrustSession{}, err
	}
	// 服务端读取源根下的 manifest 验证身份：客户端声称的扩展 ID 必须与
	// 该目录真实声明的 ID 一致。读不到（不同机/未挂载）一律 fail-closed。
	manifest, err := extensionmanifest.LoadPackage(strings.TrimSpace(input.RootPath))
	if err != nil {
		return DevTrustSession{}, fmt.Errorf("%w: read package manifest at %s: %v",
			ErrDevTrustRootForbidden, strings.TrimSpace(input.RootPath), err)
	}
	if manifest.ID != extension.ID {
		return DevTrustSession{}, fmt.Errorf("%w: manifest id %q does not match extension %q",
			ErrDevTrustRootForbidden, manifest.ID, extension.ID)
	}
	ttl := input.TTL
	if ttl <= 0 {
		ttl = DevTrustSessionDefaultTTL
	}
	if ttl > devTrustSessionMaxTTL {
		return DevTrustSession{}, ErrDevTrustDisabled
	}
	now := s.now()
	id, err := newDevTrustSessionID()
	if err != nil {
		return DevTrustSession{}, err
	}
	session := DevTrustSession{
		ID: id, ExtensionID: extension.ID,
		// 会话唯一键是扩展 ID；rootPath 仅为审计元数据（来源描述）。
		PackageSlug: extension.ID,
		RootPath:    strings.TrimSpace(input.RootPath),
		ActorUserID: actor.ID, GrantedAt: now, ExpiresAt: now.Add(ttl),
	}
	created, err := s.sessions.Create(ctx, session)
	if err != nil {
		return DevTrustSession{}, err
	}
	return created, nil
}

// List 暴露给 admin UI：活跃/历史会话可见，可一键撤销。
func (s *DevTrustService) List(ctx context.Context, actor identity.Actor, input DevTrustSessionListInput) ([]DevTrustSession, error) {
	if !canViewExtensions(actor) && !canManagePlugins(actor) {
		return nil, identity.ErrPermissionDenied
	}
	return s.sessions.List(ctx, input)
}

// Revoke 只允许创建者本人或 super_admin。
func (s *DevTrustService) Revoke(ctx context.Context, actor identity.Actor, sessionID string) error {
	sessions, err := s.sessions.List(ctx, DevTrustSessionListInput{IncludeRevoked: true})
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID != sessionID {
			continue
		}
		if !actor.IsSuperAdmin() && session.ActorUserID != actor.ID {
			return identity.ErrPermissionDenied
		}
		return s.sessions.Revoke(ctx, sessionID, actor.ID, "revoked by operator")
	}
	return ErrDevTrustSessionNotFound
}

// GrantDevArtifact 是 watch 循环每个新 digest 的自动信任写入：
// 活跃会话 + 同 actor + 未过期 + 未撤销时幂等写 exact live grant（审计标记 dev_session），
// 否则返回 ErrTrustChallengeRequired，与人工路径完全一致。
func (s *DevTrustService) GrantDevArtifact(
	ctx context.Context,
	actor identity.Actor,
	input DevTrustGrantInput,
) error {
	if !actor.IsSuperAdmin() {
		return identity.ErrPermissionDenied
	}
	if s == nil || s.sessions == nil {
		return ErrTrustChallengeRequired
	}
	// 先重扫外部源：dev 循环刚构建出的新 digest 只有经过 SyncExternalSources
	// 暂存，运行中的 API 才能在授权与 upgrade 时取到同一制品。同步失败
	// 直接返回真实原因，不折叠成 trust challenge（否则作者无从排查）。
	if s.devEnabled && s.sourceSyncer != nil {
		if _, err := s.sourceSyncer.SyncExternalSources(ctx); err != nil {
			return fmt.Errorf("dev source resync failed: %w", err)
		}
	}
	extension, err := s.trust.extension(ctx, normalizeID(input.ExtensionID))
	if err != nil {
		return err
	}
	// Safe Mode 下一律回到人工路径（人工路径本身也会拒绝执行）。
	if s.safeMode || !s.devEnabled || extension.Source == SourceBuiltin {
		return ErrTrustChallengeRequired
	}
	if !RequiresExecutableTrust(extension) {
		return nil
	}
	// 会话唯一键是扩展 ID（服务端派生，忽略客户端声称值）。
	session, err := s.sessions.Active(ctx, extension.ID)
	if err != nil {
		if errors.Is(err, ErrDevTrustSessionNotFound) {
			return ErrTrustChallengeRequired
		}
		return err
	}
	if session.ActorUserID != actor.ID || !s.now().Before(session.ExpiresAt) {
		return ErrTrustChallengeRequired
	}
	// staged 优先：dev 循环构建后新摘要只进入 staged（sync/upload 暂存），
	// active 仍是旧摘要。授权目标与后续 upgrade 消费的制品保持一致。
	target := extension
	if staged, ok := extension.StagedArtifact(); ok && extension.Status != StatusDisabled {
		target = staged
	}
	if target.PackageDigest != strings.TrimSpace(input.PackageDigest) {
		// 摘要与落库制品（staged 或 active）不一致：staging 未完成或竞态，回到人工确认。
		return ErrTrustChallengeRequired
	}
	impact, err := buildTrustImpact(target, TrustActionEnable)
	if err != nil {
		return err
	}
	granted, err := s.trust.store.HasLiveGrant(ctx, trustIdentity(impact))
	if err != nil {
		return err
	}
	if !granted {
		if _, err := s.trust.store.EnsureLiveGrant(ctx, TrustEnsureGrantInput{
			ActorUserID: actor.ID, Identity: trustIdentity(impact),
			ArtifactDigests: impact.ArtifactDigests, Impact: impact,
		}); err != nil {
			s.trust.appendAudit(ctx, actor, audit.ActionExtensionTrustDenied, impact, err)
			return err
		}
		s.trust.appendAudit(ctx, actor, audit.ActionExtensionTrustGrant, impact, nil)
	}
	if err := s.sessions.TouchDigest(ctx, session.ID, target.PackageDigest); err != nil {
		return err
	}
	return nil
}

func (s *DevTrustService) requireDevTrustEligible(actor identity.Actor, rootPath string) error {
	if !actor.IsSuperAdmin() {
		return identity.ErrPermissionDenied
	}
	if s.safeMode {
		return ErrSafeModeActive
	}
	if !s.devEnabled {
		return ErrDevTrustDisabled
	}
	root := strings.TrimSpace(rootPath)
	if root == "" || !s.devSourceRootAllowed(root) {
		return ErrDevTrustRootForbidden
	}
	return nil
}

// devSourceRootAllowed 判断源码根是否位于允许的开发源码集合内：
// 配置注入的 EXTERNAL_EXTENSION_ROOTS（外部插件仓库等）或脚手架默认
// extensions/dev 树。只做词法路径判断（filepath.Rel），不解析符号链接；
// 快照管线本就拒绝 symlink，因此 symlink 不是可用的绕过面。
func (s *DevTrustService) devSourceRootAllowed(root string) bool {
	normalized := filepath.Clean(strings.ReplaceAll(root, "\\", "/"))
	if !filepath.IsAbs(normalized) {
		return false
	}
	for _, allowed := range s.devSourceRoots {
		allowed = filepath.Clean(strings.ReplaceAll(allowed, "\\", "/"))
		if allowed == "" || !filepath.IsAbs(allowed) {
			continue
		}
		if normalized == allowed {
			return true
		}
		if relative, err := filepath.Rel(allowed, normalized); err == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	// 脚手架默认根（仓库内 extensions/dev 树）始终允许。
	return strings.Contains(normalized+"/", "/extensions/dev/")
}
