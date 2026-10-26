package sitechrome_test

import (
	"context"
	"errors"
	"testing"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
)

type brandAssetUploaderStub struct {
	calls int
	item  attachments.Attachment
}

func (s *brandAssetUploaderStub) UploadSiteBrandImage(context.Context, identity.Actor, attachments.UploadInput) (attachments.Attachment, error) {
	s.calls++
	return s.item, nil
}

func TestBrandAssetServiceAuthorizesContextAndActor(t *testing.T) {
	uploader := &brandAssetUploaderStub{item: attachments.Attachment{
		ID: 9, PublicID: "brand-public-id", Status: attachments.StatusActive, Visibility: attachments.VisibilityPublic,
	}}
	service := sitechrome.NewBrandAssetService(uploader)
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true},
	}

	item, err := service.Upload(context.Background(), actor, sitechrome.BrandAssetLogo, attachments.UploadInput{})
	if err != nil || item.ID != 9 || uploader.calls != 1 {
		t.Fatalf("expected authorized upload, item=%#v calls=%d err=%v", item, uploader.calls, err)
	}

	_, err = service.Upload(context.Background(), actor, "unknown", attachments.UploadInput{})
	if !errors.Is(err, sitechrome.ErrInvalidBrandAsset) || uploader.calls != 1 {
		t.Fatalf("expected invalid context before upload, calls=%d err=%v", uploader.calls, err)
	}

	_, err = service.Upload(context.Background(), identity.Actor{ID: 8, Status: identity.UserStatusActive}, sitechrome.BrandAssetLogo, attachments.UploadInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) || uploader.calls != 1 {
		t.Fatalf("expected denied actor before upload, calls=%d err=%v", uploader.calls, err)
	}
}
