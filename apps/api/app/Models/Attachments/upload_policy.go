package attachments

import (
	"context"

	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func (s *Service) WithUploadPolicy(policy *uploadpolicy.Service, transportMaxBodyBytes int64) *Service {
	if s != nil {
		s.uploadPolicy = policy
		s.transportMaxBodyBytes = transportMaxBodyBytes
	}
	return s
}

func (s *Service) UploadPolicy(ctx context.Context, actor identity.Actor) (uploadpolicy.EffectivePolicy, error) {
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.EffectivePolicy{}, err
	}
	global := s.uploadGlobalPolicy(settings)
	if s.uploadPolicy == nil {
		maxBytes := global.SiteMaxFileSizeBytes
		return uploadpolicy.EffectivePolicy{
			Allowed: actor.Can(identity.PermissionAttachmentUpload) && settings.UploadEnabled,
			Reason:  uploadPolicyReason(actor, settings.UploadEnabled), Source: uploadpolicy.SourceSite,
			EffectiveMaxFileSizeBytes: maxBytes, SiteMaxFileSizeBytes: maxBytes,
			TransportMaxFileSizeBytes: maxBytes,
		}, nil
	}
	return s.uploadPolicy.Resolve(ctx, actor, global)
}

func (s *Service) ListRoleUploadPolicies(ctx context.Context, actor identity.Actor) (uploadpolicy.RolePolicyCatalog, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.RolePolicyCatalog{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.RolePolicyCatalog{}, err
	}
	return s.uploadPolicy.ListRoles(ctx, actor, s.uploadGlobalPolicy(settings))
}

func (s *Service) GetUserUploadPolicy(ctx context.Context, actor identity.Actor, userID int64) (uploadpolicy.UserPolicy, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.UserPolicy{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.UserPolicy{}, err
	}
	return s.uploadPolicy.GetUser(ctx, actor, userID, s.uploadGlobalPolicy(settings))
}

func (s *Service) SetRoleUploadPolicy(ctx context.Context, actor identity.Actor, roleKey string, input uploadpolicy.LimitInput) (uploadpolicy.RolePolicy, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.RolePolicy{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.RolePolicy{}, err
	}
	return s.uploadPolicy.SetRole(ctx, actor, roleKey, input, s.uploadGlobalPolicy(settings))
}

func (s *Service) DeleteRoleUploadPolicy(ctx context.Context, actor identity.Actor, roleKey string) (uploadpolicy.RolePolicy, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.RolePolicy{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.RolePolicy{}, err
	}
	return s.uploadPolicy.DeleteRole(ctx, actor, roleKey, s.uploadGlobalPolicy(settings))
}

func (s *Service) SetUserUploadPolicy(ctx context.Context, actor identity.Actor, userID int64, input uploadpolicy.LimitInput) (uploadpolicy.UserPolicy, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.UserPolicy{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.UserPolicy{}, err
	}
	return s.uploadPolicy.SetUser(ctx, actor, userID, input, s.uploadGlobalPolicy(settings))
}

func (s *Service) DeleteUserUploadPolicy(ctx context.Context, actor identity.Actor, userID int64) (uploadpolicy.UserPolicy, error) {
	if s.uploadPolicy == nil {
		return uploadpolicy.UserPolicy{}, uploadpolicy.ErrInvalidPolicy
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return uploadpolicy.UserPolicy{}, err
	}
	return s.uploadPolicy.DeleteUser(ctx, actor, userID, s.uploadGlobalPolicy(settings))
}

func (s *Service) validateUploadSiteLimit(settings AttachmentSettings) error {
	if s.uploadPolicy == nil {
		return nil
	}
	return s.uploadPolicy.ValidateSiteMaxFileSizeMB(settings.MaxFileSizeMB, s.uploadGlobalPolicy(settings))
}

func (s *Service) decorateUploadTransportLimit(settings AttachmentSettings) AttachmentSettings {
	global := s.uploadGlobalPolicy(settings)
	settings.TransportMaxFileSizeBytes = global.TransportMaxBodyBytes - uploadpolicy.MultipartOverheadReserveBytes
	if settings.TransportMaxFileSizeBytes < 0 {
		settings.TransportMaxFileSizeBytes = 0
	}
	return settings
}

func (s *Service) uploadGlobalPolicy(settings AttachmentSettings) uploadpolicy.GlobalPolicy {
	transportMax := s.transportMaxBodyBytes
	if transportMax <= 0 {
		transportMax = int64(settings.MaxFileSizeMB)*1024*1024 + uploadpolicy.MultipartOverheadReserveBytes
	}
	return uploadpolicy.GlobalPolicy{
		UploadEnabled:         settings.UploadEnabled,
		SiteMaxFileSizeBytes:  int64(settings.MaxFileSizeMB) * 1024 * 1024,
		TransportMaxBodyBytes: transportMax,
	}
}

func uploadPolicyReason(actor identity.Actor, enabled bool) string {
	if !enabled {
		return uploadpolicy.ReasonUploadDisabled
	}
	if !actor.Can(identity.PermissionAttachmentUpload) {
		return uploadpolicy.ReasonPermissionDenied
	}
	return uploadpolicy.ReasonAllowed
}
