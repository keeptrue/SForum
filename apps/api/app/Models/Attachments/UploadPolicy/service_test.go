package uploadpolicy_test

import (
	"context"
	"errors"
	"math"
	"testing"

	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const mib int64 = 1024 * 1024

type fakeStore struct {
	roleLimits []uploadpolicy.RoleLimit
	userLimit  *int64
}

func (s *fakeStore) RoleLimitsForUser(context.Context, int64) ([]uploadpolicy.RoleLimit, error) {
	return s.roleLimits, nil
}

func (s *fakeStore) UserLimit(context.Context, int64) (*int64, error) { return s.userLimit, nil }
func (s *fakeStore) ListRolePolicies(context.Context) ([]uploadpolicy.StoredRolePolicy, error) {
	return nil, nil
}
func (s *fakeStore) GetUserPolicy(context.Context, int64) (uploadpolicy.StoredUserPolicy, error) {
	return uploadpolicy.StoredUserPolicy{}, nil
}
func (s *fakeStore) SetRoleLimit(context.Context, int64, string, int64) error { return nil }
func (s *fakeStore) DeleteRoleLimit(context.Context, int64, string) error     { return nil }
func (s *fakeStore) SetUserLimit(context.Context, int64, int64, int64) error  { return nil }
func (s *fakeStore) DeleteUserLimit(context.Context, int64, int64) error      { return nil }

type fakeActors map[int64]identity.Actor

func (a fakeActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	actor, ok := a[userID]
	if !ok {
		return identity.Actor{}, errors.New("missing actor")
	}
	return actor, nil
}

func TestResolveUploadPolicyPrecedence(t *testing.T) {
	ctx := context.Background()
	global := uploadpolicy.GlobalPolicy{
		UploadEnabled: true, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 64 * mib,
	}
	uploadActor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentUpload: true},
	}

	t.Run("permission deny blocks every size policy", func(t *testing.T) {
		service := uploadpolicy.NewService(&fakeStore{}, fakeActors{})
		actor := uploadActor
		actor.Permissions = nil
		got, err := service.Resolve(ctx, actor, global)
		if err != nil || got.Allowed || got.Reason != uploadpolicy.ReasonPermissionDenied {
			t.Fatalf("policy = %#v, err = %v", got, err)
		}
	})

	t.Run("user limit replaces role union", func(t *testing.T) {
		userLimit := 2 * mib
		roleLimit := 15 * mib
		service := uploadpolicy.NewService(&fakeStore{
			userLimit:  &userLimit,
			roleLimits: []uploadpolicy.RoleLimit{{RoleKey: "trusted", MaxFileSizeBytes: &roleLimit}},
		}, fakeActors{})
		got, err := service.Resolve(ctx, uploadActor, global)
		if err != nil || !got.Allowed || got.Source != uploadpolicy.SourceUser || got.EffectiveMaxFileSizeBytes != 2*mib {
			t.Fatalf("policy = %#v, err = %v", got, err)
		}
	})

	t.Run("multiple roles use the largest grant", func(t *testing.T) {
		five := 5 * mib
		fifteen := 15 * mib
		service := uploadpolicy.NewService(&fakeStore{roleLimits: []uploadpolicy.RoleLimit{
			{RoleKey: "member", MaxFileSizeBytes: &five},
			{RoleKey: "trusted", MaxFileSizeBytes: &fifteen},
		}}, fakeActors{})
		got, err := service.Resolve(ctx, uploadActor, global)
		if err != nil || got.Source != uploadpolicy.SourceRole || got.EffectiveMaxFileSizeBytes != 15*mib {
			t.Fatalf("policy = %#v, err = %v", got, err)
		}
	})

	t.Run("an inheriting role receives the site cap", func(t *testing.T) {
		five := 5 * mib
		service := uploadpolicy.NewService(&fakeStore{roleLimits: []uploadpolicy.RoleLimit{
			{RoleKey: "member", MaxFileSizeBytes: &five},
			{RoleKey: "trusted", MaxFileSizeBytes: nil},
		}}, fakeActors{})
		got, err := service.Resolve(ctx, uploadActor, global)
		if err != nil || got.EffectiveMaxFileSizeBytes != 20*mib {
			t.Fatalf("policy = %#v, err = %v", got, err)
		}
	})
}

func TestResolveUploadPolicyHonorsGlobalAndTransportCaps(t *testing.T) {
	ctx := context.Background()
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin},
	}
	service := uploadpolicy.NewService(&fakeStore{}, fakeActors{})
	got, err := service.Resolve(ctx, actor, uploadpolicy.GlobalPolicy{
		UploadEnabled: true, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 10 * mib,
	})
	if err != nil || !got.Allowed || got.EffectiveMaxFileSizeBytes != 9*mib || got.Source != uploadpolicy.SourceSite {
		t.Fatalf("policy = %#v, err = %v", got, err)
	}

	disabled, err := service.Resolve(ctx, actor, uploadpolicy.GlobalPolicy{
		UploadEnabled: false, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 64 * mib,
	})
	if err != nil || disabled.Allowed || disabled.Reason != uploadpolicy.ReasonUploadDisabled {
		t.Fatalf("disabled policy = %#v, err = %v", disabled, err)
	}
}

func TestValidateSiteLimitRejectsTransportMismatch(t *testing.T) {
	service := uploadpolicy.NewService(&fakeStore{}, fakeActors{})
	global := uploadpolicy.GlobalPolicy{
		UploadEnabled: true, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 10 * mib,
	}
	if err := service.ValidateSiteMaxFileSizeMB(9, global); err != nil {
		t.Fatalf("expected 9 MB to fit transport budget: %v", err)
	}
	if err := service.ValidateSiteMaxFileSizeMB(10, global); !errors.Is(err, uploadpolicy.ErrInvalidPolicy) {
		t.Fatalf("expected transport mismatch, got %v", err)
	}
	if err := service.ValidateSiteMaxFileSizeMB(math.MaxInt, global); !errors.Is(err, uploadpolicy.ErrInvalidPolicy) {
		t.Fatalf("expected oversized integer to fail before byte conversion, got %v", err)
	}
}

func TestUploadPolicyManagementRequiresDedicatedAuthority(t *testing.T) {
	ctx := context.Background()
	global := uploadpolicy.GlobalPolicy{
		UploadEnabled: true, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 64 * mib,
	}
	store := &fakeStore{}
	service := uploadpolicy.NewService(store, fakeActors{})
	plainActor := identity.Actor{ID: 10, Status: identity.UserStatusActive}
	manager := identity.Actor{
		ID: 11, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentUploadPolicyManage: true},
	}
	managerWithUserView := manager
	managerWithUserView.Permissions = map[string]bool{
		identity.PermissionAttachmentUploadPolicyManage: true,
		identity.PermissionUserView:                     true,
	}

	if _, err := service.SetRole(ctx, plainActor, "member", uploadpolicy.LimitInput{MaxFileSizeMB: 5}, global); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("role mutation without policy authority = %v", err)
	}
	if _, err := service.SetUser(ctx, manager, 7, uploadpolicy.LimitInput{MaxFileSizeMB: 5}, global); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("user mutation without user.view = %v", err)
	}
	if _, err := service.SetUser(ctx, managerWithUserView, 7, uploadpolicy.LimitInput{MaxFileSizeMB: 5}, global); err == nil {
		t.Fatal("user mutation must load the target actor before writing")
	}
}

func TestUploadPolicyManagementProtectsSuperAdminTargets(t *testing.T) {
	ctx := context.Background()
	global := uploadpolicy.GlobalPolicy{
		UploadEnabled: true, SiteMaxFileSizeBytes: 20 * mib,
		TransportMaxBodyBytes: 64 * mib,
	}
	manager := identity.Actor{
		ID: 11, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionAttachmentUploadPolicyManage: true,
			identity.PermissionUserView:                     true,
		},
	}
	target := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		RoleKeys: []string{identity.RoleSuperAdmin},
	}
	service := uploadpolicy.NewService(&fakeStore{}, fakeActors{7: target})

	if _, err := service.SetRole(ctx, manager, identity.RoleSuperAdmin, uploadpolicy.LimitInput{MaxFileSizeMB: 5}, global); !errors.Is(err, uploadpolicy.ErrProtectedActor) {
		t.Fatalf("super-admin role mutation = %v", err)
	}
	if _, err := service.SetUser(ctx, manager, 7, uploadpolicy.LimitInput{MaxFileSizeMB: 5}, global); !errors.Is(err, uploadpolicy.ErrProtectedActor) {
		t.Fatalf("super-admin user mutation = %v", err)
	}
}
