package attachments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func (s *Service) Upload(ctx context.Context, actor identity.Actor, input UploadInput) (Attachment, error) {
	policy, err := s.UploadPolicy(ctx, actor)
	if err != nil {
		return Attachment{}, err
	}
	if !policy.Allowed {
		if policy.Reason == CodeUploadDisabled {
			return Attachment{}, ErrUploadDisabled
		}
		return Attachment{}, identity.ErrPermissionDenied
	}
	if input.File == nil || input.SizeBytes <= 0 {
		return Attachment{}, ErrInvalidAttachment
	}
	if input.SizeBytes > policy.EffectiveMaxFileSizeBytes {
		return Attachment{}, &FileTooLargeError{ActualBytes: input.SizeBytes, MaxBytes: policy.EffectiveMaxFileSizeBytes}
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return Attachment{}, err
	}
	metadata, err := inspectUpload(input, settings)
	if err != nil {
		return Attachment{}, err
	}
	// purpose=general：通用附件上传；无已发布策略时 no-op。
	if err := s.applyMediaRegistryMIME("general", metadata); err != nil {
		return Attachment{}, err
	}
	created, err := s.storePreparedUpload(ctx, actor, settings, preparedUpload{
		Reader: input.File, SizeBytes: input.SizeBytes, Metadata: metadata,
		Visibility: settings.DefaultVisibility,
	})
	if err == nil && s.compression != nil {
		_ = s.compression.Schedule(ctx, created)
	}
	return created, err
}

func (s *Service) UploadAvatar(ctx context.Context, actor identity.Actor, input UploadInput) (Attachment, error) {
	if !actor.Can(identity.PermissionAttachmentUpload) {
		return Attachment{}, identity.ErrPermissionDenied
	}
	avatarOptions, err := s.options.AvatarOptions(ctx)
	if err != nil {
		return Attachment{}, err
	}
	if !avatarOptions.AllowUpload {
		return Attachment{}, ErrUploadDisabled
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return Attachment{}, err
	}
	prepared, err := prepareAvatarUpload(input, avatarOptions)
	if err != nil {
		return Attachment{}, err
	}
	// purpose=avatar：头像路径与 general 策略分离；未声明 avatar 时回退 *。
	if err := s.applyMediaRegistryMIME("avatar", prepared.Metadata); err != nil {
		return Attachment{}, err
	}
	prepared.Visibility = VisibilityPublic
	return s.storePreparedUpload(ctx, actor, settings, prepared)
}

// UploadSEOImage 复用附件存储链路，但授权由 SEO 专用权限控制。
func (s *Service) UploadSEOImage(ctx context.Context, actor identity.Actor, input UploadInput) (Attachment, error) {
	if !actor.Can(identity.PermissionSEOManage) {
		return Attachment{}, identity.ErrPermissionDenied
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return Attachment{}, err
	}
	if !settings.UploadEnabled {
		return Attachment{}, ErrUploadDisabled
	}
	if input.File == nil || input.SizeBytes <= 0 || input.SizeBytes > int64(settings.MaxFileSizeMB)*1024*1024 {
		return Attachment{}, ErrInvalidAttachment
	}
	metadata, err := inspectUpload(input, settings)
	if err != nil || !strings.HasPrefix(metadata.ContentType, "image/") {
		return Attachment{}, ErrInvalidAttachment
	}
	// purpose=seo：SEO 社交图路径；未声明 seo 时回退 *。
	if err := s.applyMediaRegistryMIME("seo", metadata); err != nil {
		return Attachment{}, err
	}
	return s.storePreparedUpload(ctx, actor, settings, preparedUpload{
		Reader: input.File, SizeBytes: input.SizeBytes, Metadata: metadata, Visibility: VisibilityPublic,
	})
}

type preparedUpload struct {
	Reader     io.Reader
	SizeBytes  int64
	Metadata   uploadMetadata
	Visibility string
}

func (s *Service) storePreparedUpload(ctx context.Context, actor identity.Actor, settings AttachmentSettings, prepared preparedUpload) (Attachment, error) {
	// E1.4：MIME/大小策略与 inspect 之后、真正写存储之前；仅元数据，无文件字节。
	if err := s.applyAttachmentBeforeUpload(ctx, actor, prepared); err != nil {
		return Attachment{}, err
	}

	publicID, err := randomPublicID()
	if err != nil {
		return Attachment{}, err
	}
	objectKey, err := renderObjectKey(settings.PathTemplate, publicID, prepared.Metadata.Extension, time.Now())
	if err != nil {
		return Attachment{}, err
	}
	adapter, err := s.adapterForSettings(ctx, settings, settings.Provider)
	if err != nil {
		return Attachment{}, ErrStorageUnavailable
	}

	hash := sha256.New()
	reader := io.TeeReader(prepared.Reader, hash)
	if err := adapter.Put(ctx, objectKey, storage.PutInput{
		Reader: reader, Size: prepared.SizeBytes, ContentType: prepared.Metadata.ContentType,
	}); err != nil {
		if isPluginStorageFailure(err) {
			return Attachment{}, ErrStorageUnavailable
		}
		return Attachment{}, err
	}

	created, err := s.store.Create(ctx, CreateAttachmentInput{
		PublicID: publicID, OwnerUserID: actor.ID, Provider: settings.Provider, ObjectKey: objectKey,
		OriginalName: prepared.Metadata.OriginalName, ContentType: prepared.Metadata.ContentType,
		Extension: prepared.Metadata.Extension, SizeBytes: prepared.SizeBytes,
		SHA256: hex.EncodeToString(hash.Sum(nil)), ImageWidth: prepared.Metadata.ImageWidth,
		ImageHeight: prepared.Metadata.ImageHeight, Visibility: prepared.Visibility,
	})
	if err != nil {
		_ = adapter.Delete(ctx, objectKey)
		return Attachment{}, err
	}
	s.events.Emit(ctx, appevents.Envelope{
		Name: appevents.AttachmentUploaded, Kind: appevents.KindObserve, ActorUserID: actor.ID,
		ResourceType: "attachment", ResourceID: strconv.FormatInt(created.ID, 10),
		CorrelationID: appevents.NewID(), OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"attachmentId": created.ID, "publicId": created.PublicID, "ownerUserId": actor.ID,
			"provider": created.Provider, "contentType": created.ContentType, "sizeBytes": created.SizeBytes,
		},
	})
	return s.decorateURL(ctx, created), nil
}

func (s *Service) applyAttachmentBeforeUpload(ctx context.Context, actor identity.Actor, prepared preparedUpload) error {
	envelope := appevents.NewEnvelope(appevents.AttachmentBeforeUpload, map[string]any{
		"actorUserId": actor.ID, "contentType": prepared.Metadata.ContentType,
		"sizeBytes": prepared.SizeBytes, "filename": prepared.Metadata.OriginalName,
	})
	envelope.ActorUserID = actor.ID
	envelope.ResourceType = "attachment"
	result := s.events.Emit(ctx, envelope)
	if !result.OK {
		return appevents.Reject(result)
	}
	return nil
}

func renderObjectKey(template string, publicID string, extension string, now time.Time) (string, error) {
	replacements := map[string]string{
		"{yyyy}": now.Format("2006"), "{mm}": now.Format("01"), "{dd}": now.Format("02"),
		"{public_id}": publicID, "{ext}": extension,
	}
	key := template
	for placeholder, value := range replacements {
		key = strings.ReplaceAll(key, placeholder, value)
	}
	key = path.Clean(strings.TrimPrefix(strings.ReplaceAll(key, "\\", "/"), "/"))
	if key == "." || key == "" || strings.HasPrefix(key, "../") || strings.Contains(key, "/../") {
		return "", ErrInvalidAttachment
	}
	return key, nil
}

func randomPublicID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
