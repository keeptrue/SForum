package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

type CompressionTaskEnqueuer func(context.Context, int64) error

type CompressionService struct {
	store       CompressionStore
	attachments *Service
	options     *options.Service
	enqueue     CompressionTaskEnqueuer
}

func NewCompressionService(store CompressionStore, attachmentService *Service, optionsService *options.Service, enqueue CompressionTaskEnqueuer) *CompressionService {
	return &CompressionService{store: store, attachments: attachmentService, options: optionsService, enqueue: enqueue}
}

func (s *Service) WithCompressionScheduler(scheduler interface {
	Schedule(context.Context, Attachment) error
}) *Service {
	if s != nil {
		s.compression = scheduler
	}
	return s
}

func (s *CompressionService) Settings(ctx context.Context, actor identity.Actor) (CompressionSettings, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return CompressionSettings{}, identity.ErrPermissionDenied
	}
	return s.runtimeSettings(ctx)
}

func (s *CompressionService) UpdateSettings(ctx context.Context, actor identity.Actor, input CompressionSettings) (CompressionSettings, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return CompressionSettings{}, identity.ErrPermissionDenied
	}
	if !input.validInput() {
		return CompressionSettings{}, ErrInvalidAttachment
	}
	input = input.normalized()
	_, err := s.options.UpdateMany(ctx, actor, []options.UpdateInput{
		{Name: options.NameAttachmentCompressionEnabled, Value: enabledValue(input.Enabled)},
		{Name: options.NameAttachmentCompressionStrength, Value: strconv.Itoa(input.Strength)},
		{Name: options.NameAttachmentCompressionMaxDimension, Value: strconv.Itoa(input.MaxDimension)},
		{Name: options.NameAttachmentCompressionMinSizeKB, Value: strconv.Itoa(input.MinSizeKB)},
		{Name: options.NameAttachmentCompressionMinSavingsPercent, Value: strconv.Itoa(input.MinSavingsPercent)},
	})
	if err != nil {
		return CompressionSettings{}, err
	}
	return s.runtimeSettings(ctx)
}

func (s *CompressionService) Stats(ctx context.Context, actor identity.Actor) (CompressionStats, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return CompressionStats{}, identity.ErrPermissionDenied
	}
	return s.store.CompressionStats(ctx)
}

func (s *CompressionService) Backfill(ctx context.Context, actor identity.Actor, limit int) (CompressionBackfillResult, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return CompressionBackfillResult{}, identity.ErrPermissionDenied
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return CompressionBackfillResult{}, err
	}
	if !settings.Enabled {
		return CompressionBackfillResult{}, ErrUploadDisabled
	}
	ids, err := s.store.BackfillCompressionTasks(ctx, settings, limit)
	if err != nil {
		return CompressionBackfillResult{}, err
	}
	if s.enqueue != nil {
		for _, id := range ids {
			_ = s.enqueue(ctx, id)
		}
	}
	return CompressionBackfillResult{Scheduled: int64(len(ids))}, nil
}

func (s *CompressionService) Schedule(ctx context.Context, attachment Attachment) error {
	settings, err := s.runtimeSettings(ctx)
	if err != nil || !settings.Enabled || !compressionEligible(attachment, settings) {
		return err
	}
	taskID, created, err := s.store.CreateCompressionTask(ctx, attachment, settings)
	if err != nil || !created || s.enqueue == nil {
		return err
	}
	// The durable task is authoritative. A reconcile job can recover this task if
	// River insertion fails after the attachment commit.
	_ = s.enqueue(ctx, taskID)
	return nil
}

func (s *CompressionService) ProcessTask(ctx context.Context, taskID int64) error {
	task, err := s.store.ClaimCompressionTask(ctx, taskID)
	if errors.Is(err, ErrAttachmentNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return s.failTask(ctx, task.ID, "settings_unavailable", err)
	}
	if !settings.Enabled || settings.PolicyDigest != task.PolicyDigest || task.SourceSHA256 != task.Attachment.SHA256 {
		return s.store.FinishCompressionTask(ctx, task.ID, CompressionStatusSkipped, "policy_superseded")
	}
	attachmentSettings, err := s.attachments.runtimeSettings(ctx)
	if err != nil {
		return s.failTask(ctx, task.ID, "storage_settings_unavailable", err)
	}
	adapter, err := s.attachments.adapterForSettings(ctx, attachmentSettings, task.Attachment.Provider)
	if err != nil {
		return s.failTask(ctx, task.ID, "storage_unavailable", err)
	}
	reader, err := adapter.Open(ctx, task.Attachment.ObjectKey)
	if err != nil {
		return s.failTask(ctx, task.ID, "source_open_failed", err)
	}
	compressed, processErr := compressAttachmentImage(reader, task.Attachment.ContentType, settings, task.Attachment.SizeBytes)
	closeErr := reader.Close()
	if processErr != nil {
		if errors.Is(processErr, errCompressionUnsupported) || errors.Is(processErr, errCompressionTooLarge) || errors.Is(processErr, errCompressionNotUseful) {
			return s.store.FinishCompressionTask(ctx, task.ID, CompressionStatusSkipped, compressionErrorCode(processErr))
		}
		return s.failTask(ctx, task.ID, "transform_failed", processErr)
	}
	if closeErr != nil {
		return s.failTask(ctx, task.ID, "source_close_failed", closeErr)
	}
	objectKey := compressionObjectKey(task.Attachment.ObjectKey, task.VariantName, task.PolicyDigest, compressed.Extension)
	if err := adapter.Put(ctx, objectKey, storage.PutInput{
		Reader: bytes.NewReader(compressed.Bytes), Size: int64(len(compressed.Bytes)), ContentType: compressed.ContentType,
	}); err != nil {
		return s.failTask(ctx, task.ID, "variant_write_failed", err)
	}
	variant := AttachmentVariant{
		AttachmentID: task.Attachment.ID, Name: task.VariantName, Provider: task.Attachment.Provider,
		ObjectKey: objectKey, ContentType: compressed.ContentType, SizeBytes: int64(len(compressed.Bytes)),
		SHA256: compressed.SHA256, ImageWidth: compressed.Width, ImageHeight: compressed.Height,
		SourceSHA256: task.Attachment.SHA256, PolicyDigest: task.PolicyDigest,
		CompressionStrength: task.CompressionStrength,
	}
	previous, err := s.store.CompleteCompressionTask(ctx, task, variant)
	if err != nil {
		_ = adapter.Delete(ctx, objectKey)
		return err
	}
	if previous != nil && previous.ObjectKey != objectKey {
		_ = adapter.Delete(ctx, previous.ObjectKey)
	}
	return nil
}

type variantStorageSource struct {
	content   VariantContent
	provider  string
	objectKey string
	adapter   storage.Adapter
}

// ResolveVariantDelivery keeps the display-to-original fallback while allowing
// ready remote variants to be read directly from object storage after Host
// authorization succeeds.
func (s *CompressionService) ResolveVariantDelivery(ctx context.Context, actor identity.Actor, publicID, name string) (ContentDelivery, error) {
	source, fallback, err := s.variantSource(ctx, actor, publicID, name)
	if err != nil {
		return ContentDelivery{}, err
	}
	if fallback {
		return s.attachments.ResolveContentDelivery(ctx, actor, publicID)
	}
	delivery, err := storageContentDelivery(ctx, source.adapter, source.provider, source.objectKey, source.content.ContentType, source.content.OriginalName)
	if err != nil {
		return s.attachments.ResolveContentDelivery(ctx, actor, publicID)
	}
	return delivery, nil
}

func (s *CompressionService) OpenVariant(ctx context.Context, actor identity.Actor, publicID, name string) (VariantContent, io.ReadCloser, error) {
	source, fallback, err := s.variantSource(ctx, actor, publicID, name)
	if err != nil {
		return VariantContent{}, nil, err
	}
	if fallback {
		return s.openOriginal(ctx, actor, publicID)
	}
	reader, err := source.adapter.Open(ctx, source.objectKey)
	if err != nil {
		return s.openOriginal(ctx, actor, publicID)
	}
	return source.content, reader, nil
}

func (s *CompressionService) variantSource(ctx context.Context, actor identity.Actor, publicID, name string) (variantStorageSource, bool, error) {
	if strings.TrimSpace(name) != CompressionVariantDisplay {
		return variantStorageSource{}, true, nil
	}
	attachment, err := s.attachments.authorizedAttachment(ctx, actor, publicID)
	if err != nil {
		return variantStorageSource{}, false, err
	}
	variant, err := s.store.GetAttachmentVariant(ctx, attachment.ID, CompressionVariantDisplay)
	if err != nil || variant.SourceSHA256 != attachment.SHA256 {
		return variantStorageSource{}, true, nil
	}
	compressionSettings, err := s.runtimeSettings(ctx)
	if err != nil || !compressionSettings.Enabled || variant.PolicyDigest != compressionSettings.PolicyDigest {
		return variantStorageSource{}, true, nil
	}
	settings, err := s.attachments.runtimeSettings(ctx)
	if err != nil {
		return variantStorageSource{}, false, err
	}
	adapter, err := s.attachments.adapterForSettings(ctx, settings, variant.Provider)
	if err != nil {
		return variantStorageSource{}, true, nil
	}
	exists, err := adapter.Exists(ctx, variant.ObjectKey)
	if err != nil || !exists {
		return variantStorageSource{}, true, nil
	}
	return variantStorageSource{
		content:  VariantContent{ContentType: variant.ContentType, OriginalName: variantFilename(attachment.OriginalName, variant.ContentType)},
		provider: variant.Provider, objectKey: variant.ObjectKey, adapter: adapter,
	}, false, nil
}

func (s *CompressionService) openOriginal(ctx context.Context, actor identity.Actor, publicID string) (VariantContent, io.ReadCloser, error) {
	attachment, reader, err := s.attachments.OpenContent(ctx, actor, publicID)
	if err != nil {
		return VariantContent{}, nil, err
	}
	return VariantContent{ContentType: attachment.ContentType, OriginalName: attachment.OriginalName}, reader, nil
}

func (s *CompressionService) runtimeSettings(ctx context.Context) (CompressionSettings, error) {
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return CompressionSettings{}, err
	}
	return CompressionSettings{
		Enabled:           enabled(values, options.NameAttachmentCompressionEnabled, true),
		Strength:          readInt(values, options.NameAttachmentCompressionStrength, RecommendedCompressionStrength),
		MaxDimension:      readInt(values, options.NameAttachmentCompressionMaxDimension, RecommendedCompressionMaxDimension),
		MinSizeKB:         readInt(values, options.NameAttachmentCompressionMinSizeKB, RecommendedCompressionMinSizeKB),
		MinSavingsPercent: readInt(values, options.NameAttachmentCompressionMinSavingsPercent, RecommendedCompressionMinSavingsPercent),
	}.normalized(), nil
}

func (s *CompressionService) failTask(ctx context.Context, taskID int64, code string, cause error) error {
	_ = s.store.FinishCompressionTask(ctx, taskID, CompressionStatusFailed, code)
	return cause
}

func compressionEligible(attachment Attachment, settings CompressionSettings) bool {
	if attachment.Status != StatusActive || (attachment.ContentType != "image/jpeg" && attachment.ContentType != "image/png") {
		return false
	}
	largeDimension := attachment.ImageWidth != nil && attachment.ImageHeight != nil &&
		(*attachment.ImageWidth > settings.MaxDimension || *attachment.ImageHeight > settings.MaxDimension)
	return attachment.SizeBytes >= int64(settings.MinSizeKB)*1024 || largeDimension
}

func compressionObjectKey(sourceKey, variantName, policyDigest, extension string) string {
	return sourceKey + ".variants/" + variantName + "-" + policyDigest[:12] + extension
}

func compressionErrorCode(err error) string {
	switch {
	case errors.Is(err, errCompressionUnsupported):
		return "unsupported_format"
	case errors.Is(err, errCompressionTooLarge):
		return "image_budget_exceeded"
	default:
		return "not_smaller"
	}
}

func variantFilename(originalName, contentType string) string {
	extension := ".jpg"
	if contentType == "image/png" {
		extension = ".png"
	}
	base := strings.TrimSuffix(filepath.Base(originalName), filepath.Ext(originalName))
	return base + "-display" + extension
}

func (s CompressionSettings) String() string {
	return fmt.Sprintf("enabled=%t strength=%d maxDimension=%d", s.Enabled, s.Strength, s.MaxDimension)
}
