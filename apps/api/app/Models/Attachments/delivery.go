package attachments

import (
	"context"
	"io"
	"net/url"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

// ContentDelivery either redirects a browser to short-lived remote storage or
// streams bytes when the selected provider cannot safely issue such a URL.
type ContentDelivery struct {
	ContentType  string
	OriginalName string
	RedirectURL  string
	Reader       io.ReadCloser
}

type contentStorageSource struct {
	attachment Attachment
	adapter    storage.Adapter
}

func (s *Service) contentStorageSource(ctx context.Context, actor identity.Actor, publicID string) (contentStorageSource, error) {
	attachment, err := s.authorizedAttachment(ctx, actor, publicID)
	if err != nil {
		return contentStorageSource{}, err
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return contentStorageSource{}, err
	}
	adapter, err := s.adapterForSettings(ctx, settings, attachment.Provider)
	if err != nil {
		return contentStorageSource{}, ErrStorageUnavailable
	}
	return contentStorageSource{attachment: attachment, adapter: adapter}, nil
}

// ResolveContentDelivery authorizes an attachment before choosing its delivery
// mode. Core-local storage remains streamed; remote adapters may issue a
// short-lived URL so object bytes bypass the Host after authorization.
func (s *Service) ResolveContentDelivery(ctx context.Context, actor identity.Actor, publicID string) (ContentDelivery, error) {
	source, err := s.contentStorageSource(ctx, actor, publicID)
	if err != nil {
		return ContentDelivery{}, err
	}
	return storageContentDelivery(ctx, source.adapter, source.attachment.Provider, source.attachment.ObjectKey, source.attachment.ContentType, source.attachment.OriginalName)
}

func storageContentDelivery(ctx context.Context, adapter storage.Adapter, provider, objectKey, contentType, originalName string) (ContentDelivery, error) {
	// Only remote providers that actually issue signed URLs may bypass the Host.
	// Active content stays streamed so Host-owned response headers force download.
	if storage.ParseSelection(provider).Kind != storage.SelectionKindCore && !options.IsAttachmentActiveContentType(contentType) {
		if signedURL, err := adapter.SignedURL(ctx, objectKey, defaultSignedURLTTL); err == nil && isSafeRedirectURL(signedURL) {
			return ContentDelivery{ContentType: contentType, OriginalName: originalName, RedirectURL: signedURL}, nil
		}
	}
	reader, err := adapter.Open(ctx, objectKey)
	if err != nil {
		if isPluginStorageFailure(err) {
			return ContentDelivery{}, ErrStorageUnavailable
		}
		return ContentDelivery{}, err
	}
	return ContentDelivery{ContentType: contentType, OriginalName: originalName, Reader: reader}, nil
}

func isSafeRedirectURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "https" || parsed.Scheme == "http")
}
