package seo

import (
	"context"
	"errors"
	"regexp"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

var (
	ErrInvalidAsset     = errors.New("seo: invalid asset")
	assetContextPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type AssetUploader interface {
	UploadSEOImage(context.Context, identity.Actor, attachments.UploadInput) (attachments.Attachment, error)
}

type AssetReferenceStore interface {
	ReplaceSEOReference(ctx context.Context, attachmentID int64, context string, actorUserID int64) error
}

type AssetService struct {
	uploader AssetUploader
	store    AssetReferenceStore
}

func NewAssetService(uploader AssetUploader, store AssetReferenceStore) *AssetService {
	return &AssetService{uploader: uploader, store: store}
}

func (s *AssetService) Replace(ctx context.Context, actor identity.Actor, assetContext string, input attachments.UploadInput) (attachments.Attachment, error) {
	if !actor.Can(identity.PermissionSEOManage) {
		return attachments.Attachment{}, identity.ErrPermissionDenied
	}
	if !assetContextPattern.MatchString(assetContext) {
		return attachments.Attachment{}, ErrInvalidAsset
	}
	item, err := s.uploader.UploadSEOImage(ctx, actor, input)
	if err != nil {
		return attachments.Attachment{}, err
	}
	if item.Status != attachments.StatusActive || item.Visibility != attachments.VisibilityPublic || !regexp.MustCompile(`^image/`).MatchString(item.ContentType) {
		return attachments.Attachment{}, ErrInvalidAsset
	}
	if err := s.store.ReplaceSEOReference(ctx, item.ID, "seo/"+assetContext, actor.ID); err != nil {
		return attachments.Attachment{}, err
	}
	return item, nil
}
