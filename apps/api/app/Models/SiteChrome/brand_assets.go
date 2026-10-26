package sitechrome

import (
	"context"
	"errors"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	BrandAssetLogo           = "logo"
	BrandAssetFavicon        = "favicon"
	BrandAssetAppleTouchIcon = "apple-touch-icon"
)

var ErrInvalidBrandAsset = errors.New("site chrome: invalid brand asset")

type BrandAssetUploader interface {
	UploadSiteBrandImage(context.Context, identity.Actor, attachments.UploadInput) (attachments.Attachment, error)
}

type BrandAssetService struct {
	uploader BrandAssetUploader
}

func NewBrandAssetService(uploader BrandAssetUploader) *BrandAssetService {
	return &BrandAssetService{uploader: uploader}
}

func (s *BrandAssetService) Upload(ctx context.Context, actor identity.Actor, assetContext string, input attachments.UploadInput) (attachments.Attachment, error) {
	if !actor.Can(identity.PermissionSettingsSiteManage) {
		return attachments.Attachment{}, identity.ErrPermissionDenied
	}
	switch assetContext {
	case BrandAssetLogo, BrandAssetFavicon, BrandAssetAppleTouchIcon:
	default:
		return attachments.Attachment{}, ErrInvalidBrandAsset
	}
	if s == nil || s.uploader == nil {
		return attachments.Attachment{}, attachments.ErrStorageUnavailable
	}
	item, err := s.uploader.UploadSiteBrandImage(ctx, actor, input)
	if err != nil {
		return attachments.Attachment{}, err
	}
	if item.Status != attachments.StatusActive || item.Visibility != attachments.VisibilityPublic {
		return attachments.Attachment{}, ErrInvalidBrandAsset
	}
	return item, nil
}
