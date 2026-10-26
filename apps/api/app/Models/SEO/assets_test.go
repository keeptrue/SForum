package seo

import (
	"context"
	"errors"
	"testing"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestAssetServiceReplaceRequiresSEOManage(t *testing.T) {
	uploader := &fakeAssetUploader{item: attachments.Attachment{ID: 88, PublicID: "seo-image", ContentType: "image/png", Visibility: attachments.VisibilityPublic, Status: attachments.StatusActive, URL: "/api/v1/attachments/seo-image/content"}}
	references := &fakeAssetReferenceStore{}
	service := NewAssetService(uploader, references)

	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSEOManage: true}}
	asset, err := service.Replace(context.Background(), actor, "home-og-image", attachments.UploadInput{})
	if err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	if asset.URL == "" || references.context != "seo/home-og-image" || references.attachmentID != 88 {
		t.Fatalf("unexpected asset/reference: %#v, %#v", asset, references)
	}
}

func TestAssetServiceReplaceRejectsUnauthorizedActor(t *testing.T) {
	service := NewAssetService(&fakeAssetUploader{}, &fakeAssetReferenceStore{})
	_, err := service.Replace(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, "home-og-image", attachments.UploadInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestAssetServiceReplaceRejectsInvalidContext(t *testing.T) {
	service := NewAssetService(&fakeAssetUploader{}, &fakeAssetReferenceStore{})
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSEOManage: true}}
	_, err := service.Replace(context.Background(), actor, "../bad", attachments.UploadInput{})
	if !errors.Is(err, ErrInvalidAsset) {
		t.Fatalf("expected invalid asset context, got %v", err)
	}
}

type fakeAssetUploader struct {
	item attachments.Attachment
	err  error
}

func (f *fakeAssetUploader) UploadSEOImage(context.Context, identity.Actor, attachments.UploadInput) (attachments.Attachment, error) {
	return f.item, f.err
}

type fakeAssetReferenceStore struct {
	attachmentID int64
	context      string
}

func (f *fakeAssetReferenceStore) ReplaceSEOReference(_ context.Context, attachmentID int64, context string, _ int64) error {
	f.attachmentID = attachmentID
	f.context = context
	return nil
}
