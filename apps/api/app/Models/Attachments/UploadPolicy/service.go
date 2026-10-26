package uploadpolicy

import (
	"context"
	"slices"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type Service struct {
	store  Store
	actors identity.ActorStore
}

func NewService(store Store, actors identity.ActorStore) *Service {
	return &Service{store: store, actors: actors}
}

func (s *Service) Resolve(ctx context.Context, actor identity.Actor, global GlobalPolicy) (EffectivePolicy, error) {
	policy := basePolicy(global)
	if !global.UploadEnabled {
		policy.Reason = ReasonUploadDisabled
		return policy, nil
	}
	if !actor.Can(identity.PermissionAttachmentUpload) {
		policy.Reason = ReasonPermissionDenied
		return policy, nil
	}

	policy.Allowed = true
	policy.Reason = ReasonAllowed
	if actor.IsSuperAdmin() {
		return policy, nil
	}

	userLimit, err := s.store.UserLimit(ctx, actor.ID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	if userLimit != nil {
		policy.Source = SourceUser
		policy.EffectiveMaxFileSizeBytes = minPositive(policy.EffectiveMaxFileSizeBytes, *userLimit)
		return policy, nil
	}

	roleLimits, err := s.store.RoleLimitsForUser(ctx, actor.ID)
	if err != nil {
		return EffectivePolicy{}, err
	}
	if len(roleLimits) == 0 {
		return policy, nil
	}
	policy.Source = SourceRole
	var roleMax int64
	for _, item := range roleLimits {
		if item.MaxFileSizeBytes == nil {
			roleMax = policy.SiteMaxFileSizeBytes
			break
		}
		if *item.MaxFileSizeBytes > roleMax {
			roleMax = *item.MaxFileSizeBytes
		}
	}
	policy.EffectiveMaxFileSizeBytes = minPositive(policy.EffectiveMaxFileSizeBytes, roleMax)
	return policy, nil
}

func (s *Service) ListRoles(ctx context.Context, actor identity.Actor, global GlobalPolicy) (RolePolicyCatalog, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) {
		return RolePolicyCatalog{}, identity.ErrPermissionDenied
	}
	stored, err := s.store.ListRolePolicies(ctx)
	if err != nil {
		return RolePolicyCatalog{}, err
	}
	base := basePolicy(global)
	items := make([]RolePolicy, 0, len(stored))
	for _, item := range stored {
		configured := bytesToMB(item.MaxFileSizeBytes)
		effective := base.EffectiveMaxFileSizeBytes
		if item.MaxFileSizeBytes != nil {
			effective = minPositive(effective, *item.MaxFileSizeBytes)
		}
		items = append(items, RolePolicy{
			RoleKey: item.RoleKey, Alias: item.Alias, Enabled: item.Enabled,
			GrantsUpload: item.GrantsUpload, Protected: item.RoleKey == identity.RoleSuperAdmin,
			ConfiguredMaxFileSizeMB: configured, EffectiveMaxFileSizeBytes: effective,
			UpdatedAt: item.UpdatedAt,
		})
	}
	return RolePolicyCatalog{
		UploadEnabled: global.UploadEnabled, SiteMaxFileSizeBytes: base.SiteMaxFileSizeBytes,
		TransportMaxFileSizeBytes: base.TransportMaxFileSizeBytes, Items: items,
	}, nil
}

func (s *Service) GetUser(ctx context.Context, actor identity.Actor, userID int64, global GlobalPolicy) (UserPolicy, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) || !actor.Can(identity.PermissionUserView) {
		return UserPolicy{}, identity.ErrPermissionDenied
	}
	return s.getUser(ctx, userID, global)
}

func (s *Service) SetRole(ctx context.Context, actor identity.Actor, roleKey string, input LimitInput, global GlobalPolicy) (RolePolicy, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) {
		return RolePolicy{}, identity.ErrPermissionDenied
	}
	if roleKey == identity.RoleSuperAdmin {
		return RolePolicy{}, ErrProtectedActor
	}
	maxBytes, err := validateLimit(input.MaxFileSizeMB, global)
	if err != nil {
		return RolePolicy{}, err
	}
	if err := s.store.SetRoleLimit(ctx, actor.ID, roleKey, maxBytes); err != nil {
		return RolePolicy{}, err
	}
	catalog, err := s.ListRoles(ctx, actor, global)
	if err != nil {
		return RolePolicy{}, err
	}
	for _, item := range catalog.Items {
		if item.RoleKey == roleKey {
			return item, nil
		}
	}
	return RolePolicy{}, ErrInvalidPolicy
}

func (s *Service) DeleteRole(ctx context.Context, actor identity.Actor, roleKey string, global GlobalPolicy) (RolePolicy, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) {
		return RolePolicy{}, identity.ErrPermissionDenied
	}
	if roleKey == identity.RoleSuperAdmin {
		return RolePolicy{}, ErrProtectedActor
	}
	if err := s.store.DeleteRoleLimit(ctx, actor.ID, roleKey); err != nil {
		return RolePolicy{}, err
	}
	catalog, err := s.ListRoles(ctx, actor, global)
	if err != nil {
		return RolePolicy{}, err
	}
	for _, item := range catalog.Items {
		if item.RoleKey == roleKey {
			return item, nil
		}
	}
	return RolePolicy{}, ErrInvalidPolicy
}

func (s *Service) SetUser(ctx context.Context, actor identity.Actor, userID int64, input LimitInput, global GlobalPolicy) (UserPolicy, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) || !actor.Can(identity.PermissionUserView) {
		return UserPolicy{}, identity.ErrPermissionDenied
	}
	target, err := s.actors.LoadActor(ctx, userID)
	if err != nil {
		return UserPolicy{}, err
	}
	if target.IsSuperAdmin() {
		return UserPolicy{}, ErrProtectedActor
	}
	maxBytes, err := validateLimit(input.MaxFileSizeMB, global)
	if err != nil {
		return UserPolicy{}, err
	}
	if err := s.store.SetUserLimit(ctx, actor.ID, userID, maxBytes); err != nil {
		return UserPolicy{}, err
	}
	return s.getUser(ctx, userID, global)
}

func (s *Service) DeleteUser(ctx context.Context, actor identity.Actor, userID int64, global GlobalPolicy) (UserPolicy, error) {
	if !actor.Can(identity.PermissionAttachmentUploadPolicyManage) || !actor.Can(identity.PermissionUserView) {
		return UserPolicy{}, identity.ErrPermissionDenied
	}
	target, err := s.actors.LoadActor(ctx, userID)
	if err != nil {
		return UserPolicy{}, err
	}
	if target.IsSuperAdmin() {
		return UserPolicy{}, ErrProtectedActor
	}
	if err := s.store.DeleteUserLimit(ctx, actor.ID, userID); err != nil {
		return UserPolicy{}, err
	}
	return s.getUser(ctx, userID, global)
}

func (s *Service) ValidateSiteMaxFileSizeMB(value int, global GlobalPolicy) error {
	_, err := validateLimit(value, global)
	return err
}

func (s *Service) getUser(ctx context.Context, userID int64, global GlobalPolicy) (UserPolicy, error) {
	stored, err := s.store.GetUserPolicy(ctx, userID)
	if err != nil {
		return UserPolicy{}, err
	}
	target, err := s.actors.LoadActor(ctx, userID)
	if err != nil {
		return UserPolicy{}, err
	}
	effective, err := s.Resolve(ctx, target, global)
	if err != nil {
		return UserPolicy{}, err
	}
	return UserPolicy{
		UserID: stored.UserID, Username: stored.Username, DisplayName: stored.DisplayName,
		Status: stored.Status, RoleKeys: slices.Clone(target.RoleKeys), CanUpload: effective.Allowed,
		Protected: target.IsSuperAdmin(), ConfiguredMaxFileSizeMB: bytesToMB(stored.MaxFileSizeBytes),
		EffectiveMaxFileSizeBytes: effective.EffectiveMaxFileSizeBytes,
		Source:                    effective.Source, Reason: effective.Reason, UpdatedAt: stored.UpdatedAt,
	}, nil
}

func basePolicy(global GlobalPolicy) EffectivePolicy {
	transportMax := global.TransportMaxBodyBytes - MultipartOverheadReserveBytes
	if transportMax < 0 {
		transportMax = 0
	}
	effective := minPositive(global.SiteMaxFileSizeBytes, transportMax)
	return EffectivePolicy{
		Reason: ReasonPermissionDenied, Source: SourceSite,
		EffectiveMaxFileSizeBytes: effective,
		SiteMaxFileSizeBytes:      global.SiteMaxFileSizeBytes,
		TransportMaxFileSizeBytes: transportMax,
	}
}

func validateLimit(maxFileSizeMB int, global GlobalPolicy) (int64, error) {
	if maxFileSizeMB <= 0 {
		return 0, ErrInvalidPolicy
	}
	base := basePolicy(global)
	const bytesPerMB int64 = 1024 * 1024
	if base.EffectiveMaxFileSizeBytes <= 0 || int64(maxFileSizeMB) > base.EffectiveMaxFileSizeBytes/bytesPerMB {
		return 0, ErrInvalidPolicy
	}
	maxBytes := int64(maxFileSizeMB) * bytesPerMB
	return maxBytes, nil
}

func bytesToMB(value *int64) *int {
	if value == nil {
		return nil
	}
	mb := int(*value / (1024 * 1024))
	return &mb
}

func minPositive(left, right int64) int64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left < right {
		return left
	}
	return right
}
