package attachments

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func TestUploadSiteBrandImageUsesSiteSettingsPermissionAndPublicVisibility(t *testing.T) {
	store := &fakeAttachmentStore{}
	service := NewServiceWithAdapterFactory(store, newAttachmentOptions(nil), func(storage.Config) (storage.Adapter, error) {
		return &fakeStorageAdapter{}, nil
	})
	imageBody := testJPEG(160, 80)
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true},
	}

	item, err := service.UploadSiteBrandImage(context.Background(), actor, UploadInput{
		OriginalName: "logo.jpg", ContentType: "image/jpeg", SizeBytes: int64(len(imageBody)), File: newReadSeekCloserBytes(imageBody),
	})
	if err != nil {
		t.Fatalf("UploadSiteBrandImage returned error: %v", err)
	}
	if item.Visibility != VisibilityPublic || len(store.creates) != 1 || store.creates[0].Visibility != VisibilityPublic {
		t.Fatalf("expected public site brand image, got %#v", item)
	}
}

func TestUploadSiteBrandImageDoesNotAcceptAttachmentUploadPermissionAlone(t *testing.T) {
	service := NewServiceWithAdapterFactory(&fakeAttachmentStore{}, newAttachmentOptions(nil), func(storage.Config) (storage.Adapter, error) {
		return &fakeStorageAdapter{}, nil
	})
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentUpload: true},
	}

	_, err := service.UploadSiteBrandImage(context.Background(), actor, UploadInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestUploadSiteBrandImageRasterizesSVGToSafePNG(t *testing.T) {
	store := &fakeAttachmentStore{}
	adapter := &fakeStorageAdapter{}
	service := NewServiceWithAdapterFactory(store, newAttachmentOptions(nil), func(storage.Config) (storage.Adapter, error) {
		return adapter, nil
	})
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true},
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 60"><script>alert(1)</script><rect width="120" height="60" fill="#2463eb"/></svg>`

	item, err := service.UploadSiteBrandImage(context.Background(), actor, UploadInput{
		OriginalName: "brand.svg", ContentType: "image/svg+xml", SizeBytes: int64(len(svg)), File: newReadSeekCloser(svg),
	})
	if err != nil {
		t.Fatalf("UploadSiteBrandImage SVG returned error: %v", err)
	}
	if item.ContentType != "image/png" || item.Extension != ".png" || item.OriginalName != "brand.png" {
		t.Fatalf("expected rasterized PNG metadata, got %#v", item)
	}
	config, err := png.DecodeConfig(bytes.NewReader(adapter.putBytes))
	if err != nil || config.Width != 256 || config.Height != 128 {
		t.Fatalf("expected 256x128 PNG output, config=%#v err=%v", config, err)
	}
	if bytes.Contains(adapter.putBytes, []byte("script")) || bytes.Contains(adapter.putBytes, []byte("alert")) {
		t.Fatal("rasterized output must not retain SVG executable source")
	}
}

func TestUploadSiteBrandImageRejectsSVGWithoutDrawablePaths(t *testing.T) {
	service := NewServiceWithAdapterFactory(&fakeAttachmentStore{}, newAttachmentOptions(nil), func(storage.Config) (storage.Adapter, error) {
		return &fakeStorageAdapter{}, nil
	})
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true},
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><script>alert(1)</script></svg>`

	_, err := service.UploadSiteBrandImage(context.Background(), actor, UploadInput{
		OriginalName: "empty.svg", ContentType: "image/svg+xml", SizeBytes: int64(len(svg)), File: newReadSeekCloser(svg),
	})
	if !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("expected non-drawable SVG rejection, got %v", err)
	}
}
