package extensions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

var (
	ErrFrontendTrustUnavailable = errors.New("extensions: frontend trust is unavailable")
	ErrFrontendPackageChanged   = errors.New("extensions: frontend package changed")
)

type FrontendExtensionReader interface {
	Get(context.Context, string) (Extension, error)
}

// PublicComponentAdmission is implemented by the Host-owned Component
// Registry. A manifest declaration alone must never authorize browser code:
// the exact artifact still has to be active in the current composition plan.
type PublicComponentAdmission interface {
	AdmitPublicComponent(Extension, ManifestComponent) bool
}

// FrontendService 管理管理端与公开前端的信任、挑战与精确制品发布。
// 公开 L2 没有独立开关：Safe Mode 与 exact-artifact trust 是仅有的两条关闭路径。
type FrontendService struct {
	extensions        FrontendExtensionReader
	trust             FrontendTrustStore
	executableTrust   *ExecutableTrustService
	v3TrustChallenges bool
	auditor           audit.Writer
	challenges        map[string]frontendTrustChallengeState
	mu                sync.Mutex
	safeMode          bool
	publicAssets      *assetregistry.Registry
	publicComponents  PublicComponentAdmission
}

type frontendTrustChallengeState struct {
	ActorUserID int64
	Challenge   FrontendTrustChallenge
}

func NewFrontendService(extensions FrontendExtensionReader, trust FrontendTrustStore) *FrontendService {
	return &FrontendService{
		extensions:   extensions,
		trust:        trust,
		challenges:   map[string]frontendTrustChallengeState{},
		publicAssets: assetregistry.New(),
	}
}

func (s *FrontendService) WithAuditor(writer audit.Writer) *FrontendService {
	s.auditor = writer
	return s
}

func (s *FrontendService) WithSafeMode(enabled bool) *FrontendService {
	s.safeMode = enabled
	return s
}

// WithPublicAssetRegistry injects the Host-owned shared Asset Registry. Production
// bootstrap must pass the same instance used by lifecycle restore and publication.
func (s *FrontendService) WithPublicAssetRegistry(registry *assetregistry.Registry) *FrontendService {
	if s != nil && registry != nil {
		s.publicAssets = registry
	}
	return s
}

// PublicAssetRegistry exposes the process-local shared registry for Host wiring tests.
func (s *FrontendService) PublicAssetRegistry() *assetregistry.Registry {
	if s == nil {
		return nil
	}
	return s.publicAssets
}

func (s *FrontendService) WithPublicComponentAdmission(admission PublicComponentAdmission) *FrontendService {
	s.publicComponents = admission
	return s
}

// WithExecutableTrust 让 V3 管理组件复用整包 exact-artifact grant；关闭开关时保留旧授权表。
func (s *FrontendService) WithExecutableTrust(service *ExecutableTrustService, enabled bool) *FrontendService {
	s.executableTrust = service
	s.v3TrustChallenges = enabled
	return s
}

func (s *FrontendService) Frontend(ctx context.Context, actor identity.Actor, extensionID string) (FrontendStatus, error) {
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendStatus{}, err
	}
	if !canViewExtensions(actor) && !canManagePlugins(actor) && !canManageThemes(actor) && !canManageExtensionSettings(actor, extension) {
		return FrontendStatus{}, identity.ErrPermissionDenied
	}
	return s.frontendStatus(ctx, extension)
}

func (s *FrontendService) Challenge(ctx context.Context, actor identity.Actor, extensionID string) (FrontendTrustChallenge, error) {
	if !actor.IsSuperAdmin() {
		return FrontendTrustChallenge{}, identity.ErrPermissionDenied
	}
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendTrustChallenge{}, err
	}
	component := primaryAdminComponent(extension)
	if component == nil || extension.Source != SourceUploaded {
		return FrontendTrustChallenge{}, ErrFrontendTrustUnavailable
	}
	random := make([]byte, 20)
	if _, err := rand.Read(random); err != nil {
		return FrontendTrustChallenge{}, err
	}
	now := time.Now().UTC()
	challenge := FrontendTrustChallenge{
		ChallengeID: hex.EncodeToString(random[:16]),
		Code:        fmt.Sprintf("%06d", (uint32(random[16])<<16|uint32(random[17])<<8|uint32(random[18]))%1000000),
		ExtensionID: extension.ID,
		Version:     extension.Version,
		Digest:      extension.AdminFrontendDigest,
		APIVersion:  component.APIVersion,
		ComponentID: component.ID,
		ExpiresAt:   now.Add(5 * time.Minute),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.challenges {
		if now.After(item.Challenge.ExpiresAt) {
			delete(s.challenges, id)
		}
	}
	s.challenges[challenge.ChallengeID] = frontendTrustChallengeState{ActorUserID: actor.ID, Challenge: challenge}
	return challenge, nil
}

func (s *FrontendService) Grant(ctx context.Context, actor identity.Actor, extensionID string, input GrantFrontendInput) (FrontendStatus, error) {
	if !actor.IsSuperAdmin() {
		return FrontendStatus{}, identity.ErrPermissionDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendStatus{}, err
	}
	component := primaryAdminComponent(extension)
	if component == nil || extension.Source != SourceUploaded {
		return FrontendStatus{}, ErrFrontendTrustUnavailable
	}
	if input.Digest != extension.AdminFrontendDigest {
		return FrontendStatus{}, fmt.Errorf("%w: requested digest does not match installed extension", ErrFrontendPackageChanged)
	}
	if err := s.consumePrebuiltConfirmation(actor, extension, *component, input.Confirmation); err != nil {
		return FrontendStatus{}, err
	}
	if err := verifyInstalledPackageIdentity(extension); err != nil {
		return FrontendStatus{}, err
	}
	if _, err := s.trust.CreateFrontendGrant(ctx, FrontendTrustGrantInput{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, AdminFrontendDigest: extension.AdminFrontendDigest,
		APIVersion: component.APIVersion, ComponentIDs: adminComponentIDs(extension), GrantedByUserID: actor.ID,
	}); err != nil {
		return FrontendStatus{}, err
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionFrontendGrant, extension)
	return prebuiltFrontendStatus(extension, *component, FrontendTrustTrusted), nil
}

func (s *FrontendService) Revoke(ctx context.Context, actor identity.Actor, extensionID string) (FrontendStatus, error) {
	if !actor.IsSuperAdmin() {
		return FrontendStatus{}, identity.ErrPermissionDenied
	}
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendStatus{}, err
	}
	component := primaryAdminComponent(extension)
	if component == nil || extension.Source != SourceUploaded {
		return FrontendStatus{}, ErrFrontendTrustUnavailable
	}
	if _, err := s.trust.RevokeFrontendGrant(ctx, FrontendRevocationInput{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		AdminFrontendDigest: extension.AdminFrontendDigest, RequestedByUserID: actor.ID,
	}); err != nil {
		return FrontendStatus{}, err
	}
	s.appendAudit(ctx, actor, audit.ActionExtensionFrontendRevoke, extension)
	return prebuiltFrontendStatus(extension, *component, FrontendTrustRevoked), nil
}

func (s *FrontendService) RevokeAllForExtension(ctx context.Context, extensionID string, actorUserID int64) error {
	if s == nil || s.trust == nil {
		return nil
	}
	return s.trust.RevokeAllFrontendGrants(ctx, strings.TrimSpace(extensionID), actorUserID)
}

func (s *FrontendService) Asset(ctx context.Context, actor identity.Actor, extensionID, digest, assetName string) (FrontendAsset, error) {
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendAsset{}, err
	}
	component := prebuiltSettingsComponent(extension)
	if component == nil {
		return FrontendAsset{}, ErrFrontendTrustUnavailable
	}
	return s.componentAsset(ctx, actor, extension, digest, component.ID, assetName)
}

func (s *FrontendService) ComponentAsset(ctx context.Context, actor identity.Actor, extensionID, digest, componentID, assetName string) (FrontendAsset, error) {
	extension, err := s.extension(ctx, extensionID)
	if err != nil {
		return FrontendAsset{}, err
	}
	return s.componentAsset(ctx, actor, extension, digest, componentID, assetName)
}

func (s *FrontendService) componentAsset(ctx context.Context, actor identity.Actor, extension Extension, digest, componentID, assetName string) (FrontendAsset, error) {
	if s.safeMode || digest == "" || digest != extension.AdminFrontendDigest {
		return FrontendAsset{}, ErrFrontendTrustUnavailable
	}
	surface, ok := findAdminComponentSurface(extension, componentID)
	if !ok {
		return FrontendAsset{}, ErrFrontendTrustUnavailable
	}
	if !canAccessAdminComponent(actor, extension, surface) {
		return FrontendAsset{}, identity.ErrPermissionDenied
	}
	component := surface.component
	if s.v3TrustChallenges {
		trusted, err := s.exactArtifactTrusted(ctx, extension)
		if err != nil || !trusted {
			return FrontendAsset{}, ErrFrontendTrustUnavailable
		}
	} else if !(extension.Source == SourceBuiltin && extension.IsSystem && !extension.IsDeletable) {
		grant, err := s.trust.FrontendGrant(ctx, extension.ID, extension.Version, extension.AdminFrontendDigest)
		if err != nil || grant.APIVersion != component.APIVersion || !slices.Contains(grant.ComponentIDs, component.ID) {
			return FrontendAsset{}, ErrFrontendTrustUnavailable
		}
	}
	material, err := readAdminFrontendMaterial(extension.Manifest, PackageContentRoot(extension))
	if err != nil || material.Digest != extension.AdminFrontendDigest {
		return FrontendAsset{}, fmt.Errorf("%w: component bytes changed", ErrFrontendPackageChanged)
	}
	current, ok := material.Components[component.ID]
	if !ok {
		return FrontendAsset{}, ErrFrontendTrustUnavailable
	}
	body := current.Entry
	contentType := "application/javascript; charset=utf-8"
	if assetName == "style" {
		if component.CSS == "" {
			return FrontendAsset{}, ErrFrontendTrustUnavailable
		}
		body = current.CSS
		contentType = "text/css; charset=utf-8"
	} else if assetName != "entry" {
		return FrontendAsset{}, ErrFrontendTrustUnavailable
	}
	etag := sha256.Sum256(body)
	return FrontendAsset{Body: body, ContentType: contentType, ETag: fmt.Sprintf("\"%x\"", etag[:])}, nil
}

func (s *FrontendService) extension(ctx context.Context, extensionID string) (Extension, error) {
	if s == nil || s.extensions == nil || s.trust == nil {
		return Extension{}, ErrFrontendTrustUnavailable
	}
	return s.extensions.Get(ctx, normalizeID(extensionID))
}

func (s *FrontendService) frontendStatus(ctx context.Context, extension Extension) (FrontendStatus, error) {
	component := primaryAdminComponent(extension)
	if component == nil {
		return FrontendStatus{ExtensionID: extension.ID, Kind: AdminFrontendKindNone, TrustState: FrontendTrustNone}, nil
	}
	if s.safeMode {
		return prebuiltFrontendStatus(extension, *component, FrontendTrustRequired), nil
	}
	if extension.Source == SourceBuiltin && extension.IsSystem && !extension.IsDeletable {
		return prebuiltFrontendStatus(extension, *component, FrontendTrustSourceTrusted), nil
	}
	if s.v3TrustChallenges {
		trusted, err := s.exactArtifactTrusted(ctx, extension)
		if err != nil {
			return FrontendStatus{}, err
		}
		if trusted {
			return prebuiltFrontendStatus(extension, *component, FrontendTrustTrusted), nil
		}
		return prebuiltFrontendStatus(extension, *component, FrontendTrustRequired), nil
	}
	grants, err := s.trust.LiveFrontendGrants(ctx, extension.ID)
	if err != nil {
		return FrontendStatus{}, err
	}
	for _, grant := range grants {
		if grant.ExtensionVersion == extension.Version && grant.AdminFrontendDigest == extension.AdminFrontendDigest &&
			grant.APIVersion == component.APIVersion && containsAllAdminComponents(grant.ComponentIDs, extension) {
			return prebuiltFrontendStatus(extension, *component, FrontendTrustTrusted), nil
		}
	}
	state := FrontendTrustRequired
	if len(grants) > 0 {
		state = FrontendTrustInvalidated
	}
	return prebuiltFrontendStatus(extension, *component, state), nil
}

func (s *FrontendService) exactArtifactTrusted(ctx context.Context, extension Extension) (bool, error) {
	if s == nil || s.executableTrust == nil {
		return false, ErrFrontendTrustUnavailable
	}
	return s.executableTrust.TrustedArtifact(ctx, extension)
}

func (s *FrontendService) consumePrebuiltConfirmation(actor identity.Actor, extension Extension, component AdminComponent, confirmation *FrontendTrustConfirmation) error {
	if confirmation == nil || !confirmation.Acknowledged ||
		confirmation.ExtensionID != extension.ID || confirmation.Version != extension.Version ||
		confirmation.Digest != extension.AdminFrontendDigest || confirmation.APIVersion != component.APIVersion ||
		confirmation.ComponentID != component.ID || strings.TrimSpace(confirmation.Phrase) != extension.ID {
		return fmt.Errorf("%w: explicit component trust confirmation is required", ErrFrontendTrustUnavailable)
	}
	state, exists := s.challenges[confirmation.ChallengeID]
	delete(s.challenges, confirmation.ChallengeID)
	if !exists || state.ActorUserID != actor.ID || time.Now().UTC().After(state.Challenge.ExpiresAt) ||
		state.Challenge.Code != confirmation.Code || state.Challenge.ExtensionID != extension.ID ||
		state.Challenge.Version != extension.Version || state.Challenge.Digest != extension.AdminFrontendDigest ||
		state.Challenge.APIVersion != component.APIVersion || state.Challenge.ComponentID != component.ID {
		return fmt.Errorf("%w: confirmation challenge is invalid or expired", ErrFrontendTrustUnavailable)
	}
	return nil
}

func (s *FrontendService) appendAudit(ctx context.Context, actor identity.Actor, action string, extension Extension) {
	if s == nil || s.auditor == nil {
		return
	}
	_ = s.auditor.Append(ctx, audit.Event{ActorUserID: actor.ID, Action: action, Metadata: map[string]any{
		"extensionId": extension.ID, "version": extension.Version, "adminFrontendDigest": extension.AdminFrontendDigest,
	}})
}

func prebuiltSettingsComponent(extension Extension) *SettingsComponent {
	component := extension.Manifest.SettingsDocument.UI.Component
	if extension.Manifest.SettingsDocument.UI.Mode != "component" || component == nil || component.Entry == "" {
		return nil
	}
	return component
}

func prebuiltFrontendStatus(extension Extension, component AdminComponent, state string) FrontendStatus {
	copy := component
	components := make([]AdminComponent, 0)
	for _, surface := range declaredAdminComponentSurfaces(extension.Manifest) {
		components = append(components, surface.component)
	}
	return FrontendStatus{
		ExtensionID: extension.ID,
		Kind:        AdminFrontendKindPrebuiltComponent,
		Component:   &copy,
		Components:  components,
		TrustState:  state,
		Digest:      extension.AdminFrontendDigest,
	}
}

func containsAllAdminComponents(granted []string, extension Extension) bool {
	for _, componentID := range adminComponentIDs(extension) {
		if !slices.Contains(granted, componentID) {
			return false
		}
	}
	return true
}

func verifyInstalledPackageIdentity(extension Extension) error {
	expected := strings.TrimSpace(extension.PackageDigest)
	if expected == "" || strings.TrimSpace(extension.PackagePath) == "" {
		return fmt.Errorf("%w: extension %s has no immutable package identity", ErrFrontendPackageChanged, extension.ID)
	}
	actual, err := extensionpackage.VerifiedTreeDigest(extension.PackagePath)
	if err != nil {
		return fmt.Errorf("%w: extension %s: %v", ErrFrontendPackageChanged, extension.ID, err)
	}
	if actual != expected {
		return fmt.Errorf("%w: extension %s digest expected %s, got %s", ErrFrontendPackageChanged, extension.ID, expected, actual)
	}
	return nil
}
