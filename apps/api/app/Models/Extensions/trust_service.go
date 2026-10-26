package extensions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

const DefaultTrustChallengeTTL = 5 * time.Minute

type ExecutableTrustService struct {
	extensions   FrontendExtensionReader
	store        ExecutableTrustStore
	auditor      audit.Writer
	revocations  ExecutableTrustRevocationSink
	ttl          time.Duration
	now          func() time.Time
	random       io.Reader
	publicAssets *assetregistry.Registry
}

// ExecutableTrustRevocationSink owns the process-local linearization fence for
// one durable revoke. The callback must run before exact runtime/policy closure,
// while replacement publication is still blocked.
type ExecutableTrustRevocationSink interface {
	RevokeExecutableTrust(context.Context, string, string, func(context.Context) error) error
}

// executableTrustGrantReceipt 只在 Host 激活事务内部流转。普通调用者仍只看到
// ConfirmEnable 的 error 契约，不能按 grant id 主动撤销授权。
type executableTrustGrantReceipt struct {
	grant   TrustGrant
	impact  TrustImpact
	created bool
}

type executableTrustGrantRevoker interface {
	revokeExactGrant(context.Context, TrustGrant, int64, string) error
}

func NewExecutableTrustService(extensions FrontendExtensionReader, store ExecutableTrustStore) *ExecutableTrustService {
	return &ExecutableTrustService{
		extensions: extensions,
		store:      store,
		ttl:        DefaultTrustChallengeTTL,
		now:        func() time.Time { return time.Now().UTC() },
		random:     rand.Reader,
	}
}

func (s *ExecutableTrustService) WithAuditor(writer audit.Writer) *ExecutableTrustService {
	s.auditor = writer
	return s
}

func (s *ExecutableTrustService) WithTTL(ttl time.Duration) *ExecutableTrustService {
	if ttl > 0 && ttl <= DefaultTrustChallengeTTL {
		s.ttl = ttl
	}
	return s
}

// WithPublicAssetRegistry binds the same Host-owned registry used by lifecycle publication.
func (s *ExecutableTrustService) WithPublicAssetRegistry(registry *assetregistry.Registry) *ExecutableTrustService {
	if s != nil && registry != nil {
		s.publicAssets = registry
	}
	return s
}

func (s *ExecutableTrustService) WithRevocationSink(sink ExecutableTrustRevocationSink) *ExecutableTrustService {
	if s != nil && sink != nil {
		s.revocations = sink
	}
	return s
}

// capturePublicAssetArtifact 在 DB revoke 之前冻结 exact live artifact。
func (s *ExecutableTrustService) capturePublicAssetArtifact(extensionID string) (assetregistry.Artifact, bool) {
	if s == nil || s.publicAssets == nil {
		return assetregistry.Artifact{}, false
	}
	publication, found := s.publicAssets.SnapshotPublication(normalizeID(extensionID))
	if !found {
		return assetregistry.Artifact{}, false
	}
	return publication.Artifact, true
}

// quarantineCapturedPublicAsset 在 DB revoke 之后隔离 captured exact artifact。
// 陈旧 captured 不得擦除新发布物，Registry conflict 也必须向调用方返回。
func (s *ExecutableTrustService) quarantineCapturedPublicAsset(artifact assetregistry.Artifact, captured bool) error {
	if !captured || s == nil || s.publicAssets == nil {
		return nil
	}
	_, _, err := s.publicAssets.QuarantineExact(artifact)
	return err
}

func (s *ExecutableTrustService) Impact(ctx context.Context, actor identity.Actor, extensionID string) (TrustImpact, error) {
	_, impact, err := s.reviewImpact(ctx, actor, extensionID)
	return impact, err
}

func (s *ExecutableTrustService) reviewImpact(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
) (Extension, TrustImpact, error) {
	return s.reviewImpactForTarget(ctx, actor, extensionID, false)
}

func (s *ExecutableTrustService) reviewImpactForTarget(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	staged bool,
) (Extension, TrustImpact, error) {
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return Extension{}, TrustImpact{}, err
	}
	if !canViewExtensions(actor) && !canManagePlugins(actor) && !canManageThemes(actor) {
		return Extension{}, TrustImpact{}, identity.ErrPermissionDenied
	}
	if staged {
		candidate, ok := extension.StagedArtifact()
		if !ok {
			return Extension{}, TrustImpact{}, ErrStagedVersionNotFound
		}
		extension = candidate
	} else {
		extension = trustReviewArtifact(extension)
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return Extension{}, TrustImpact{}, err
	}
	return extension, impact, nil
}

func (s *ExecutableTrustService) Status(ctx context.Context, actor identity.Actor, extensionID string) (ExecutableTrustStatus, error) {
	return s.statusForTarget(ctx, actor, extensionID, false)
}

func (s *ExecutableTrustService) StatusForStaged(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
) (ExecutableTrustStatus, error) {
	return s.statusForTarget(ctx, actor, extensionID, true)
}

func (s *ExecutableTrustService) statusForTarget(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	staged bool,
) (ExecutableTrustStatus, error) {
	extension, impact, err := s.reviewImpactForTarget(ctx, actor, extensionID, staged)
	if err != nil {
		return ExecutableTrustStatus{}, err
	}
	required := RequiresExecutableTrust(extension)
	trusted := !required
	if required {
		trusted, err = s.hasLiveGrant(ctx, impact)
		if err != nil {
			return ExecutableTrustStatus{}, err
		}
	}
	return ExecutableTrustStatus{
		Impact: impact, TrustRequired: required, Trusted: trusted,
		GuardAuthorization: GuardAuthorizationViews(impact),
	}, nil
}

// TrustedArtifact 供同一进程内的执行入口复用整包信任结论，避免后端与前端各自授权。
func (s *ExecutableTrustService) TrustedArtifact(ctx context.Context, extension Extension) (bool, error) {
	if !RequiresExecutableTrust(extension) {
		return true, nil
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return false, err
	}
	return s.hasLiveGrant(ctx, impact)
}

// RuntimeIdentity returns the exact live trust row used by a v2 subprocess.
// The handshake may disclose this identity, but only the host-side grant lookup
// is authoritative for execution.
//
// 注意：本方法会 buildTrustImpact / DigestTree，仅供 Host 启动恢复与授权路径使用。
// 公开请求路径必须改用 ValidatePublishedIdentity。
func (s *ExecutableTrustService) RuntimeIdentity(ctx context.Context, extension Extension) (RuntimeTrustIdentity, error) {
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return RuntimeTrustIdentity{}, err
	}
	if !RequiresExecutableTrust(extension) {
		return RuntimeTrustIdentity{TrustGrantID: "builtin", ImpactDigest: impact.Digest}, nil
	}
	if s == nil || s.store == nil {
		return RuntimeTrustIdentity{}, ErrTrustGrantNotFound
	}
	grant, err := s.store.LiveGrant(ctx, trustIdentity(impact))
	if err != nil {
		return RuntimeTrustIdentity{}, err
	}
	return RuntimeTrustIdentity{TrustGrantID: strconv.FormatInt(grant.ID, 10), ImpactDigest: grant.ImpactDigest}, nil
}

// ValidatePublishedIdentity 校验 Registry 已发布 artifact 与扩展当前元组/状态是否一致，
// 并对 uploaded 制品做 exact live grant 查询。
//
// 绝对禁止 buildTrustImpact、DigestTree 与包内文件扫描——请求路径热路径专用。
func (s *ExecutableTrustService) ValidatePublishedIdentity(
	ctx context.Context,
	extension Extension,
	artifact assetregistry.Artifact,
) error {
	if s == nil || ctx == nil {
		return ErrPublicFrontendUnavailable
	}
	artifactID := normalizeID(artifact.ExtensionID)
	artifactPackage := normalizedPublicDigest(artifact.PackageDigest)
	impactDigest := normalizedPublicDigest(artifact.ImpactDigest)
	if artifactID == "" || strings.TrimSpace(artifact.ExtensionVersion) == "" ||
		artifactPackage == "" || impactDigest == "" {
		return ErrPublicFrontendUnavailable
	}
	if artifact.OwnerKind == assetregistry.OwnerKindCore {
		if !artifact.Core || !strings.HasPrefix(artifactID, "core.") || extension.ID != "" {
			return ErrPublicFrontendUnavailable
		}
		return nil
	}
	if artifact.Core || strings.HasPrefix(artifactID, "core.") {
		return ErrPublicFrontendUnavailable
	}
	extensionID := normalizeID(extension.ID)
	packageDigest := normalizedPublicDigest(extension.PackageDigest)
	wantKind, ok := publicAssetOwnerKind(extension)
	if !ok || artifact.OwnerKind != wantKind || extensionID == "" || extensionID != artifactID ||
		strings.TrimSpace(extension.Version) != strings.TrimSpace(artifact.ExtensionVersion) ||
		packageDigest == "" || packageDigest != artifactPackage || extension.Status != StatusEnabled {
		return ErrPublicFrontendUnavailable
	}
	if extension.Source == SourceBuiltin {
		if !extension.IsSystem || extension.IsDeletable {
			return ErrPublicFrontendUnavailable
		}
		return nil
	}
	if !RequiresExecutableTrust(extension) || s.store == nil {
		return ErrTrustGrantNotFound
	}
	granted, err := s.store.HasLiveGrant(ctx, TrustIdentity{
		ExtensionID: extensionID, ExtensionVersion: strings.TrimSpace(extension.Version),
		PackageDigest: packageDigest, Action: TrustActionEnable, ImpactDigest: impactDigest,
	})
	if err != nil {
		return err
	}
	if !granted {
		return ErrTrustGrantNotFound
	}
	return nil
}

func (s *ExecutableTrustService) hasLiveGrant(ctx context.Context, impact TrustImpact) (bool, error) {
	if s == nil || s.store == nil {
		return false, ErrTrustChallengeInvalid
	}
	return s.store.HasLiveGrant(ctx, trustIdentity(impact))
}

func (s *ExecutableTrustService) Challenge(ctx context.Context, actor identity.Actor, extensionID string) (TrustChallenge, error) {
	return s.challengeForTarget(ctx, actor, extensionID, false)
}

func (s *ExecutableTrustService) ChallengeForStaged(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
) (TrustChallenge, error) {
	return s.challengeForTarget(ctx, actor, extensionID, true)
}

func (s *ExecutableTrustService) challengeForTarget(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	staged bool,
) (TrustChallenge, error) {
	if !actor.IsSuperAdmin() {
		return TrustChallenge{}, identity.ErrPermissionDenied
	}
	extension, impact, err := s.reviewImpactForTarget(ctx, actor, extensionID, staged)
	if err != nil {
		return TrustChallenge{}, err
	}
	if !RequiresExecutableTrust(extension) {
		return TrustChallenge{}, ErrTrustNotRequired
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(s.random, tokenBytes); err != nil {
		return TrustChallenge{}, fmt.Errorf("create trust challenge token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	expiresAt := s.now().Add(s.ttl)
	if err := s.store.CreateChallenge(ctx, TrustChallengeRecord{
		TokenHash: tokenHash(token), ActorUserID: actor.ID,
		Identity: trustIdentity(impact), ArtifactDigests: impact.ArtifactDigests,
		Impact: impact, ExpiresAt: expiresAt,
	}); err != nil {
		return TrustChallenge{}, err
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionTrustChallenge, impact, nil)
	return TrustChallenge{Token: token, Impact: impact, ExpiresAt: expiresAt}, nil
}

// ConfirmEnable 对同一摘要的已授权重启直接放行；新授权必须原子消费挑战。
func (s *ExecutableTrustService) ConfirmEnable(ctx context.Context, actor identity.Actor, extension Extension, token string) error {
	_, err := s.confirmEnable(ctx, actor, extension, token)
	return err
}

// EnsureCompatibilityGrant 供 V3 trust challenge 关闭时的 v1 启用路径使用：
// super_admin 已通过 confirmCapabilities 后，为 protocol v2 子进程写入 exact live grant。
// 已有 live grant 时幂等返回。builtin / 非可执行扩展直接放行。
// EnsureLiveGrantForDevAuto 是 dev 自动激活通道（DevAutoRuntime）的 grant 写入：
// 与人工路径相同的 exact-artifact identity 与审计动作，只是执行者是 dev-auto
// 系统账号（无人工确认）。仅 dev 装配可达；生产 fail closed（本服务随装配）。
func (s *ExecutableTrustService) EnsureLiveGrantForDevAuto(ctx context.Context, actor identity.Actor, extension Extension) error {
	if !RequiresExecutableTrust(extension) {
		return nil
	}
	if s == nil || s.store == nil {
		return ErrTrustGrantNotFound
	}
	if !actor.IsSuperAdmin() {
		return identity.ErrPermissionDenied
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return err
	}
	identityKey := trustIdentity(impact)
	granted, err := s.store.HasLiveGrant(ctx, identityKey)
	if err != nil {
		return err
	}
	if granted {
		return nil
	}
	if _, err := s.store.EnsureLiveGrant(ctx, TrustEnsureGrantInput{
		ActorUserID: actor.ID, Identity: identityKey,
		ArtifactDigests: impact.ArtifactDigests, Impact: impact,
	}); err != nil {
		s.appendAudit(ctx, actor, audit.ActionExtensionTrustDenied, impact, err)
		return err
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionTrustGrant, impact, nil)
	return nil
}

func (s *ExecutableTrustService) EnsureCompatibilityGrant(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
) error {
	if !RequiresExecutableTrust(extension) {
		return nil
	}
	if !actor.IsSuperAdmin() {
		return identity.ErrPermissionDenied
	}
	if s == nil || s.store == nil {
		return ErrTrustGrantNotFound
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return err
	}
	identityKey := trustIdentity(impact)
	granted, err := s.store.HasLiveGrant(ctx, identityKey)
	if err != nil {
		return err
	}
	if granted {
		return nil
	}
	grant, err := s.store.EnsureLiveGrant(ctx, TrustEnsureGrantInput{
		ActorUserID: actor.ID, Identity: identityKey,
		ArtifactDigests: impact.ArtifactDigests, Impact: impact,
	})
	if err != nil {
		s.appendAudit(ctx, actor, audit.ActionExtensionTrustDenied, impact, err)
		return err
	}
	if grant.created {
		s.appendAudit(ctx, actor, audit.ActionExtensionTrustGrant, impact, nil)
	}
	return nil
}

func (s *ExecutableTrustService) confirmEnable(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	token string,
) (executableTrustGrantReceipt, error) {
	if !RequiresExecutableTrust(extension) {
		return executableTrustGrantReceipt{}, nil
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return executableTrustGrantReceipt{}, err
	}
	identityKey := trustIdentity(impact)
	granted, err := s.store.HasLiveGrant(ctx, identityKey)
	if err != nil {
		return executableTrustGrantReceipt{}, err
	}
	if granted {
		return executableTrustGrantReceipt{impact: impact}, nil
	}
	if strings.TrimSpace(token) == "" {
		return executableTrustGrantReceipt{}, ErrTrustChallengeRequired
	}
	if !actor.IsSuperAdmin() {
		return executableTrustGrantReceipt{}, identity.ErrPermissionDenied
	}
	grant, err := s.store.ConsumeChallenge(ctx, TrustConsumeInput{
		TokenHash: tokenHash(strings.TrimSpace(token)), ActorUserID: actor.ID, Identity: identityKey,
	})
	if err != nil {
		s.appendAudit(ctx, actor, audit.ActionExtensionTrustDenied, impact, err)
		return executableTrustGrantReceipt{}, err
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionTrustGrant, impact, nil)
	return executableTrustGrantReceipt{grant: grant, impact: impact, created: grant.created}, nil
}

func (s *ExecutableTrustService) compensateEnable(
	ctx context.Context,
	actor identity.Actor,
	receipt executableTrustGrantReceipt,
	reason string,
) error {
	if !receipt.created {
		return nil
	}
	revoker, ok := s.store.(executableTrustGrantRevoker)
	if !ok {
		err := errors.New("extensions: exact executable trust compensation unavailable")
		s.appendCompensationAudit(ctx, actor, receipt, reason, err)
		return err
	}
	err := revoker.revokeExactGrant(ctx, receipt.grant, actor.ID, strings.TrimSpace(reason))
	s.appendCompensationAudit(ctx, actor, receipt, reason, err)
	return err
}

func (s *ExecutableTrustService) RevokeAllForExtension(ctx context.Context, extensionID string, actorUserID int64, reason string) error {
	if s == nil || s.store == nil {
		return nil
	}
	extensionID = normalizeID(extensionID)
	reason = strings.TrimSpace(reason)
	if s.extensions == nil {
		return ErrTrustChallengeInvalid
	}
	extension, err := s.extensions.Get(ctx, extensionID)
	if err == nil && extension.Source != SourceUploaded {
		return nil
	}
	if err != nil && !errors.Is(err, ErrExtensionNotFound) {
		return err
	}
	return s.revokeExecutableTrust(ctx, extensionID, actorUserID, reason)
}

func (s *ExecutableTrustService) Revoke(ctx context.Context, actor identity.Actor, extensionID string) (ExecutableTrustStatus, error) {
	if !actor.IsSuperAdmin() {
		return ExecutableTrustStatus{}, identity.ErrPermissionDenied
	}
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return ExecutableTrustStatus{}, err
	}
	var revokeErr error
	if extension.Source == SourceUploaded {
		revokeErr = s.revokeExecutableTrust(ctx, extension.ID, actor.ID, "operator_revoked")
	}
	extension = trustReviewArtifact(extension)
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		impact = TrustImpact{
			SchemaVersion: TrustImpactSchemaV2, Action: TrustActionEnable,
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			ExtensionType: extension.Type, Source: extension.Source,
			PackageDigest:    extension.PackageDigest,
			ManifestContract: extensionmanifest.ManifestContract(extension.Manifest),
		}
	}
	s.appendRevokeAudit(ctx, actor, impact, revokeErr)
	if revokeErr != nil {
		return ExecutableTrustStatus{}, revokeErr
	}
	required := RequiresExecutableTrust(extension)
	return ExecutableTrustStatus{Impact: impact, TrustRequired: required, Trusted: !required}, nil
}

func (s *ExecutableTrustService) revokeExecutableTrust(
	ctx context.Context,
	extensionID string,
	actorUserID int64,
	reason string,
) error {
	var assetErr error
	durable := func(revokeCtx context.Context) error {
		// Capture under the runtime-set fence so the local asset identity matches
		// the durable revoke order. Exact CAS preserves a later reauthorization.
		captured, found := s.capturePublicAssetArtifact(extensionID)
		revokeErr := s.store.RevokeAll(revokeCtx, extensionID, actorUserID, reason)
		if revokeErr == nil || errors.Is(revokeErr, ErrTrustRevocationCommitUnknown) {
			assetErr = s.quarantineCapturedPublicAsset(captured, found)
		}
		return revokeErr
	}
	if s.revocations != nil {
		return errors.Join(
			s.revocations.RevokeExecutableTrust(ctx, extensionID, reason, durable),
			assetErr,
		)
	}
	return errors.Join(durable(ctx), assetErr)
}

func (s *ExecutableTrustService) extension(ctx context.Context, extensionID string) (Extension, error) {
	if s == nil || s.extensions == nil || s.store == nil {
		return Extension{}, ErrTrustChallengeInvalid
	}
	return s.extensions.Get(ctx, normalizeID(extensionID))
}

func (s *ExecutableTrustService) appendAudit(ctx context.Context, actor identity.Actor, action string, impact TrustImpact, denied error) {
	if s == nil || s.auditor == nil {
		return
	}
	metadata := map[string]any{
		"extensionId":   impact.ExtensionID,
		"version":       impact.ExtensionVersion,
		"packageDigest": impact.PackageDigest,
		"impactDigest":  impact.Digest,
		"action":        impact.Action,
	}
	if denied != nil {
		metadata["reason"] = trustErrorCode(denied)
	}
	_ = s.auditor.Append(ctx, audit.Event{ActorUserID: actor.ID, Action: action, Metadata: metadata})
}

func (s *ExecutableTrustService) appendCompensationAudit(
	ctx context.Context,
	actor identity.Actor,
	receipt executableTrustGrantReceipt,
	reason string,
	compensationErr error,
) {
	if s == nil || s.auditor == nil {
		return
	}
	metadata := map[string]any{
		"extensionId":        receipt.impact.ExtensionID,
		"version":            receipt.impact.ExtensionVersion,
		"packageDigest":      receipt.impact.PackageDigest,
		"impactDigest":       receipt.impact.Digest,
		"action":             receipt.impact.Action,
		"grantId":            receipt.grant.ID,
		"compensation":       true,
		"compensationReason": strings.TrimSpace(reason),
		"succeeded":          compensationErr == nil,
	}
	if compensationErr != nil {
		metadata["error"] = compensationErr.Error()
	}
	_ = s.auditor.Append(ctx, audit.Event{
		ActorUserID: actor.ID,
		Action:      audit.ActionExtensionTrustRevoke,
		Metadata:    metadata,
	})
}

func (s *ExecutableTrustService) appendRevokeAudit(
	ctx context.Context,
	actor identity.Actor,
	impact TrustImpact,
	revokeErr error,
) {
	if s == nil || s.auditor == nil {
		return
	}
	outcome := "succeeded"
	if errors.Is(revokeErr, ErrTrustRevocationCommitUnknown) {
		outcome = "unknown"
	} else if revokeErr != nil {
		outcome = "failed"
	}
	metadata := map[string]any{
		"extensionId":   impact.ExtensionID,
		"version":       impact.ExtensionVersion,
		"packageDigest": impact.PackageDigest,
		"impactDigest":  impact.Digest,
		"action":        impact.Action,
		"succeeded":     revokeErr == nil,
		"outcome":       outcome,
	}
	if revokeErr != nil {
		metadata["error"] = revokeErr.Error()
	}
	_ = s.auditor.Append(ctx, audit.Event{
		ActorUserID: actor.ID,
		Action:      audit.ActionExtensionTrustRevoke,
		Metadata:    metadata,
	})
}

func RequiresExecutableTrust(extension Extension) bool {
	if extension.Source != SourceUploaded {
		return false
	}
	manifest := extension.Manifest
	return hasExecutableBackend(manifest) || strings.TrimSpace(extension.AdminFrontendDigest) != "" ||
		len(manifest.Migrations) > 0 || len(manifest.Guards) > 0 || hasL2Components(manifest) ||
		hasAssetRegistryDeclarations(manifest) ||
		requestsRawRequest(manifest) || requestsRawCoreDatabase(manifest) || hasExecutableLifecycle(manifest)
}

func buildTrustImpact(extension Extension, action string) (TrustImpact, error) {
	if err := verifyInstalledPackageIdentity(extension); err != nil {
		return TrustImpact{}, err
	}
	manifest := extension.Manifest
	normalizedDatabase := extensionmanifest.Normalize(manifest).Database
	artifacts := map[string]string{"package": extension.PackageDigest}
	binaries := []TrustArtifact{}
	packageFileSet := map[string]struct{}{}
	addPackageFile := func(path string) {
		if path = strings.TrimSpace(path); path != "" {
			packageFileSet[path] = struct{}{}
		}
	}
	addExecutableArtifact := func(key, kind, path, declaredDigest string) error {
		digest, err := digestInstalledFile(extension, path)
		if err != nil {
			return err
		}
		declaredDigest = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(declaredDigest)), "sha256:")
		if declaredDigest != "" && declaredDigest != digest {
			return fmt.Errorf("%w: executable digest changed for %s", ErrFrontendPackageChanged, path)
		}
		artifacts[key] = digest
		binaries = append(binaries, TrustArtifact{Kind: kind, Path: path, Digest: digest})
		addPackageFile(path)
		return nil
	}
	if entry := strings.TrimSpace(manifest.Backend.Entry); entry != "" {
		if err := addExecutableArtifact("backend", "backend", entry, manifest.Backend.Digest); err != nil {
			return TrustImpact{}, err
		}
	}
	if digest := strings.TrimSpace(extension.AdminFrontendDigest); digest != "" {
		artifacts["adminFrontend"] = digest
	}
	migrations := make([]TrustMigration, 0, len(manifest.Migrations))
	for _, migration := range manifest.Migrations {
		digest, err := digestInstalledFile(extension, migration.Path)
		if err != nil {
			return TrustImpact{}, err
		}
		declaredDigest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(migration.Digest)), "sha256:")
		if declaredDigest != "" && declaredDigest != digest {
			return TrustImpact{}, fmt.Errorf("%w: migration digest changed for %s", ErrFrontendPackageChanged, migration.Path)
		}
		artifacts["migration:"+migration.Path] = digest
		migrations = append(migrations, TrustMigration{Path: migration.Path, Digest: digest})
		addPackageFile(migration.Path)
	}
	for _, guard := range manifest.Guards {
		if err := addExecutableArtifact("guard:"+guard.ID, "guard", guard.Entry, guard.Digest); err != nil {
			return TrustImpact{}, err
		}
	}
	packageFilesByID := make(map[string]ManifestPackageFile, len(manifest.PackageFiles))
	for _, file := range manifest.PackageFiles {
		packageFilesByID[file.ID] = file
	}
	for _, component := range manifest.Components {
		if component.L2Component == "" {
			continue
		}
		file, ok := packageFilesByID[component.L2Component]
		if !ok {
			return TrustImpact{}, fmt.Errorf("%w: L2 package file %s is unavailable", ErrFrontendPackageChanged, component.L2Component)
		}
		if err := addExecutableArtifact("l2:"+file.ID, "l2", file.Path, file.Digest); err != nil {
			return TrustImpact{}, err
		}
	}
	guards := make([]TrustGuard, 0, len(manifest.Routes))
	for _, route := range manifest.Routes {
		guards = append(guards, TrustGuard{
			Path: route.Path, Methods: append([]string{}, route.Methods...),
			Access: route.Access, Permission: route.Permission,
		})
	}
	components := []SettingsComponent{}
	frontendAPI := ""
	for _, binding := range extensionmanifest.DeclaredAdminComponents(manifest) {
		component := binding.Component
		components = append(components, component)
		addPackageFile(component.Entry)
		if component.CSS != "" {
			addPackageFile(component.CSS)
		}
		if frontendAPI == "" {
			frontendAPI = fmt.Sprintf("sforum.admin-component@%d", component.APIVersion)
		}
	}
	capKeys, implied := extensionmanifest.ResolvedCapabilities(manifest)
	permissions := append([]string{}, manifest.Permissions...)
	requiredFeatures := append([]string{}, manifest.RequiresFeatures...)
	secrets := []string{}
	for _, field := range manifest.Settings {
		if field.Type == "secret" {
			secrets = append(secrets, field.Key)
		}
	}
	sort.Strings(permissions)
	sort.Strings(requiredFeatures)
	sort.Strings(secrets)
	packageFiles := make([]string, 0, len(packageFileSet))
	for path := range packageFileSet {
		packageFiles = append(packageFiles, path)
	}
	sort.Strings(packageFiles)
	hostAPI := strings.TrimSpace(manifest.Backend.HostAPIVersion)
	if hostAPI == "" {
		hostAPI = "sforum.host/v1"
	}
	impact := TrustImpact{
		SchemaVersion: TrustImpactSchemaV2, Action: action,
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		ExtensionType: extension.Type, Source: extension.Source, PackageDigest: extension.PackageDigest,
		ManifestContract: extensionmanifest.ManifestContract(manifest),
		ArtifactDigests:  artifacts, Binaries: binaries, Backend: manifest.Backend,
		Routes: append([]ManifestRoute{}, manifest.Routes...), Guards: guards,
		GuardDeclarations: append([]ManifestGuard{}, manifest.Guards...),
		Hooks:             append([]ManifestHook{}, manifest.Hooks...), Events: append([]ManifestEvent{}, manifest.Events...),
		Migrations: migrations, MigrationDeclarations: append([]ManifestMigration{}, manifest.Migrations...),
		Providers: append([]ManifestProvider{}, manifest.Providers...),
		Jobs:      append([]ManifestJob{}, manifest.Jobs...), Schedules: append([]ManifestSchedule{}, manifest.Schedules...),
		Components: components, RegistryComponents: append([]ManifestComponent{}, manifest.Components...),
		Templates: append([]ManifestTemplate{}, manifest.Templates...), Assets: append([]ManifestAsset{}, manifest.Assets...),
		Content: append([]ManifestContent{}, manifest.Content...), Database: normalizedDatabase,
		Cache: append([]ManifestCache{}, manifest.Cache...), SEO: append([]ManifestSEO{}, manifest.SEO...),
		Services: append([]ManifestService{}, manifest.Services...),
		Commands: append([]ManifestCommand{}, manifest.Commands...), AdminSurfaces: append([]ManifestAdminSurface{}, manifest.AdminSurfaces...),
		Queries:            append([]ManifestQuery{}, manifest.Queries...),
		QueryResultFilters: append([]ManifestQueryResultFilter{}, manifest.QueryResultFilters...), Identity: manifest.Identity,
		PermissionDefinitions: append([]ManifestPermissionDefinition{}, manifest.PermissionDefinitions...),
		Media:                 append([]ManifestMediaPipeline{}, manifest.Media...), Navigation: append([]ManifestNavigation{}, manifest.Navigation...),
		Regions:       append([]ManifestRegion{}, manifest.Regions...),
		Contributions: append([]ManifestContribution{}, manifest.Contributions...),
		Capabilities:  capabilities.GrantsFromKeys(capKeys, implied), Permissions: permissions,
		RequiredFeatures: requiredFeatures, Dependencies: append([]ManifestDependency{}, manifest.Dependencies...),
		Lifecycle: manifest.Lifecycle, OpenAPI: append([]ManifestOpenAPIFragment{}, manifest.OpenAPI...),
		PackageFiles: append([]ManifestPackageFile{}, manifest.PackageFiles...),
		RequestedAuthority: TrustAuthority{
			BackendExecution:       hasExecutableBackend(manifest),
			AdminFrontendExecution: strings.TrimSpace(extension.AdminFrontendDigest) != "",
			RawRequest:             requestsRawRequest(manifest),
			RawCoreDatabase:        requestsRawCoreDatabase(manifest),
			OutboundNetwork:        capabilities.NewSet(capKeys).Has(capabilities.NetOutbound),
			PackageFiles:           packageFiles, Secrets: secrets,
		},
		Contracts: TrustContracts{HostAPI: hostAPI, FrontendAPI: frontendAPI},
	}
	digest, err := canonicalTrustImpactDigest(impact)
	if err != nil {
		return TrustImpact{}, err
	}
	impact.Digest = digest
	return impact, nil
}

func hasL2Components(manifest Manifest) bool {
	for _, component := range manifest.Components {
		if strings.TrimSpace(component.L2Component) != "" {
			return true
		}
	}
	return false
}

func requestsRawRequest(manifest Manifest) bool {
	for _, guard := range manifest.Guards {
		if guard.Kind == "raw_request" {
			return true
		}
	}
	for _, route := range manifest.Routes {
		if route.Guard == extensionmanifest.GuardCoreRaw {
			return true
		}
	}
	return false
}

func requestsRawCoreDatabase(manifest Manifest) bool {
	return extensionmanifest.HasDatabaseGrant(manifest.Database, extensionmanifest.DatabaseGrantRawCore) ||
		extensionmanifest.HasDatabaseGrant(manifest.Database, extensionmanifest.DatabaseGrantKernel)
}

func hasExecutableLifecycle(manifest Manifest) bool {
	if manifest.Lifecycle == nil {
		return false
	}
	for _, operation := range []*extensionmanifest.ManifestLifecycleOperation{
		manifest.Lifecycle.Install, manifest.Lifecycle.Enable, manifest.Lifecycle.Disable,
		manifest.Lifecycle.Upgrade, manifest.Lifecycle.Rollback, manifest.Lifecycle.Uninstall,
	} {
		if operation != nil && strings.TrimSpace(operation.Execute) != "" {
			return true
		}
	}
	return false
}

// canonicalTrustImpactDigest 必须覆盖 TrustImpact 的每个字段；P2 新增声明时由测试防止遗漏。
func canonicalTrustImpactDigest(impact TrustImpact) (string, error) {
	impact.Digest = ""
	body, err := json.Marshal(impact)
	if err != nil {
		return "", fmt.Errorf("marshal extension trust impact: %w", err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func digestInstalledFile(extension Extension, relative string) (string, error) {
	target, ok := installedFilePath(extension, relative)
	if !ok {
		return "", fmt.Errorf("%w: unsafe executable path %s", ErrFrontendPackageChanged, relative)
	}
	// 这里的「真实校验」仍是带 TOCTOU 防御的 digestStableExtensionFile；指纹未变
	// 时跳过重复读取，避免每次可信身份计算都重读数十 MB 的后端二进制。
	digest, err := extensionpackage.VerifiedFileDigestFunc(target, func() (string, error) {
		return digestStableExtensionFile(extension, relative, 0, false)
	})
	if err != nil {
		return "", fmt.Errorf("%w: executable path %s is unavailable: %v", ErrFrontendPackageChanged, relative, err)
	}
	return digest, nil
}

func trustIdentity(impact TrustImpact) TrustIdentity {
	return TrustIdentity{
		ExtensionID: impact.ExtensionID, ExtensionVersion: impact.ExtensionVersion,
		PackageDigest: impact.PackageDigest, Action: impact.Action, ImpactDigest: impact.Digest,
	}
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func trustErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTrustChallengeExpired):
		return CodeTrustChallengeExpired
	case errors.Is(err, ErrTrustChallengeReplayed):
		return CodeTrustChallengeReplayed
	case errors.Is(err, ErrTrustChallengeStale):
		return CodeTrustChallengeStale
	default:
		return CodeTrustChallengeInvalid
	}
}
