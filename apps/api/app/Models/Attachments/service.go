package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
	localization "github.com/keeptrue/sforum/apps/api/app/Support/Localization"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

const defaultSignedURLTTL = 5 * time.Minute

// StorageProviderCatalog 列出已启用且声明 attachment.storage.provider 的插件（E6.1）。
// 由 Extensions 服务实现；未注入时候选列表仅含 core 驱动。
type StorageProviderCatalog interface {
	ListStorageProviderCandidates(ctx context.Context) ([]storage.Candidate, error)
	// IsStorageProviderAvailable 校验 plugin 选择是否仍可用（启用 + 声明槽位）。
	IsStorageProviderAvailable(ctx context.Context, extensionID string) (bool, error)
	StorageProviderSchema(ctx context.Context, extensionID, locale string) (storage.ProviderSchema, error)
}

// StoragePluginRuntime 是插件存储适配器的稳定工厂边界（E6.2）。
type StoragePluginRuntime = extensionruntime.StorageAdapterFactory

type Service struct {
	store          Store
	options        *options.Service
	events         appevents.Publisher
	adapterFactory func(storage.Config) (storage.Adapter, error)
	// providers 可选：插件存储候选与可用性（E6.1）。
	providers StorageProviderCatalog
	// storageRuntime 可选：选中 plugin: 时构造 PluginStorageAdapter（E6.2）。
	storageRuntime StoragePluginRuntime
	// mediaRegistry 可选：已发布 MIME 策略时叠加拒绝；无策略时不介入。
	mediaRegistry *mediaregistry.Registry
	instanceStore StorageInstanceStore
	secrets       *secretstore.Service
	compression   interface {
		Schedule(context.Context, Attachment) error
	}
	uploadPolicy          *uploadpolicy.Service
	transportMaxBodyBytes int64
}

func NewService(store Store, optionsService *options.Service) *Service {
	return NewServiceWithEvents(store, optionsService, nil)
}

func NewServiceWithEvents(store Store, optionsService *options.Service, publisher appevents.Publisher) *Service {
	service := &Service{
		store:          store,
		options:        optionsService,
		events:         appevents.EnsurePublisher(publisher),
		adapterFactory: storage.NewAdapter,
	}
	if instances, ok := store.(StorageInstanceStore); ok {
		service.instanceStore = instances
	}
	return service
}

func (s *Service) WithSecretStore(secrets *secretstore.Service) *Service {
	if s != nil {
		s.secrets = secrets
	}
	return s
}

func NewServiceWithAdapterFactory(store Store, optionsService *options.Service, factory func(storage.Config) (storage.Adapter, error)) *Service {
	service := NewServiceWithEvents(store, optionsService, nil)
	if factory != nil {
		service.adapterFactory = factory
	}
	return service
}

// WithStorageProviderCatalog 注入扩展目录，用于候选列表与 plugin 选择校验。
func (s *Service) WithStorageProviderCatalog(catalog StorageProviderCatalog) *Service {
	if s != nil {
		s.providers = catalog
	}
	return s
}

// WithStoragePluginRuntime 注入扩展 runtime，使 plugin: 选择走分块 Storage RPC（E6.2）。
func (s *Service) WithStoragePluginRuntime(runtime StoragePluginRuntime) *Service {
	if s != nil {
		s.storageRuntime = runtime
	}
	return s
}

// WithEvents 补绑事件发布器（bootstrap 中 BridgePublisher 晚于附件服务创建）。
func (s *Service) WithEvents(publisher appevents.Publisher) *Service {
	if s != nil {
		s.events = appevents.EnsurePublisher(publisher)
	}
	return s
}

// WithMediaRegistry 注入 Media Pipeline Registry，使上传路径可执行插件 MIME 策略。
func (s *Service) WithMediaRegistry(registry *mediaregistry.Registry) *Service {
	if s != nil {
		s.mediaRegistry = registry
	}
	return s
}

func (s *Service) Get(ctx context.Context, actor identity.Actor, publicID string) (Attachment, error) {
	attachment, err := s.authorizedAttachment(ctx, actor, publicID)
	if err != nil {
		return Attachment{}, err
	}
	return s.decorateURL(ctx, attachment), nil
}

func (s *Service) authorizedAttachment(ctx context.Context, actor identity.Actor, publicID string) (Attachment, error) {
	attachment, err := s.store.GetByPublicID(ctx, publicID)
	if err != nil {
		return Attachment{}, err
	}
	if err := s.authorizeAttachmentView(ctx, actor, attachment); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func (s *Service) OpenContent(ctx context.Context, actor identity.Actor, publicID string) (Attachment, io.ReadCloser, error) {
	attachment, err := s.authorizedAttachment(ctx, actor, publicID)
	if err != nil {
		return Attachment{}, nil, err
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return Attachment{}, nil, err
	}
	adapter, err := s.adapterForSettings(ctx, settings, attachment.Provider)
	if err != nil {
		return Attachment{}, nil, ErrStorageUnavailable
	}
	reader, err := adapter.Open(ctx, attachment.ObjectKey)
	if err != nil {
		if isPluginStorageFailure(err) {
			return Attachment{}, nil, ErrStorageUnavailable
		}
		return Attachment{}, nil, err
	}
	return attachment, reader, nil
}

func (s *Service) List(ctx context.Context, actor identity.Actor, input AttachmentListInput) (AttachmentList, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return AttachmentList{}, identity.ErrPermissionDenied
	}
	// M6/L4：转义 LIKE/ILIKE 元字符，配合 store SQL 的 ESCAPE '\'，防止通配符失控匹配。
	input.Query = escapeLike(strings.TrimSpace(input.Query))
	input.ContentType = escapeLike(strings.TrimSpace(input.ContentType))
	list, err := s.store.List(ctx, input)
	if err != nil {
		return AttachmentList{}, err
	}
	for index := range list.Items {
		list.Items[index] = s.decorateURL(ctx, list.Items[index])
	}
	return list, nil
}

func (s *Service) Detail(ctx context.Context, actor identity.Actor, id int64) (AttachmentDetail, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return AttachmentDetail{}, identity.ErrPermissionDenied
	}
	attachment, err := s.store.GetByID(ctx, id)
	if err != nil {
		return AttachmentDetail{}, err
	}
	references, err := s.store.ListReferences(ctx, id)
	if err != nil {
		return AttachmentDetail{}, err
	}
	return AttachmentDetail{Attachment: s.decorateURL(ctx, attachment), References: references}, nil
}

func (s *Service) UpdateStatus(ctx context.Context, actor identity.Actor, id int64, status string) (Attachment, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return Attachment{}, identity.ErrPermissionDenied
	}
	status = strings.TrimSpace(status)
	switch status {
	case StatusActive, StatusDisabled:
		attachment, err := s.store.UpdateStatus(ctx, id, status, false)
		if err != nil {
			return Attachment{}, err
		}
		return s.decorateURL(ctx, attachment), nil
	default:
		return Attachment{}, ErrInvalidAttachment
	}
}

func (s *Service) Delete(ctx context.Context, actor identity.Actor, id int64) (Attachment, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return Attachment{}, identity.ErrPermissionDenied
	}
	attachment, err := s.store.GetByID(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	if attachment.ReferenceCount > 0 {
		return Attachment{}, ErrReferenced
	}
	updated, err := s.store.UpdateStatus(ctx, id, StatusDeleted, true)
	if err != nil {
		return Attachment{}, err
	}
	return s.decorateURL(ctx, updated), nil
}

func (s *Service) Settings(ctx context.Context, actor identity.Actor, locale string) (AttachmentSettings, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return AttachmentSettings{}, identity.ErrPermissionDenied
	}
	adminOptions, err := s.options.ListAdmin(ctx, actor)
	if err != nil {
		return AttachmentSettings{}, err
	}
	values := map[string]string{}
	secrets := map[string]bool{}
	for _, item := range adminOptions {
		values[item.Name] = item.Value
		if item.Secret {
			secrets[item.Name] = item.SecretSet
		}
	}
	return s.decorateUploadTransportLimit(s.decorateSettings(ctx, settingsFromValues(values, secrets), locale)), nil
}

func (s *Service) UpdateSettings(ctx context.Context, actor identity.Actor, input AttachmentSettings, locale string) (AttachmentSettings, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return AttachmentSettings{}, identity.ErrPermissionDenied
	}
	if err := s.validateUploadSiteLimit(input); err != nil {
		return AttachmentSettings{}, err
	}
	// 保存前校验 plugin 选择仍可用，避免写入孤儿 provider。
	if err := s.ensureProviderSelectable(ctx, input.Provider); err != nil {
		return AttachmentSettings{}, err
	}
	updated, err := s.options.UpdateMany(ctx, actor, settingsUpdateInputs(input))
	if err != nil {
		return AttachmentSettings{}, err
	}
	values := map[string]string{}
	secrets := map[string]bool{}
	for _, item := range updated {
		values[item.Name] = item.Value
		if item.Secret {
			secrets[item.Name] = item.SecretSet
		}
	}
	return s.decorateUploadTransportLimit(s.decorateSettings(ctx, settingsFromValues(values, secrets), locale)), nil
}

func (s *Service) Probe(ctx context.Context, actor identity.Actor, locale string) (ProbeResult, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return ProbeResult{}, identity.ErrPermissionDenied
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	adapter, err := s.adapterForSettings(ctx, settings, settings.Provider)
	if err != nil {
		// 无 runtime / 插件不可用：对运营返回清晰 reason，勿暴露内部 error 字符串为主文案。
		return ProbeResult{
			Provider: settings.Provider,
			OK:       false,
			Reason:   CodeStorageUnavailable,
			Message:  localizedProbeMessage(locale, CodeStorageUnavailable),
		}, nil
	}
	if err := adapter.Probe(ctx); err != nil {
		reason := probeFailureReason(err)
		return ProbeResult{
			Provider: settings.Provider,
			OK:       false,
			Reason:   reason,
			Message:  localizedProbeMessage(locale, reason),
		}, nil
	}
	return ProbeResult{
		Provider: settings.Provider,
		OK:       true,
		Reason:   "storage.ok",
		Message:  localizedProbeMessage(locale, "storage.ok"),
	}, nil
}

// localizedProbeMessage 保留 RPC reason 作为稳定机器契约，展示给管理员的消息跟随请求语言。
func localizedProbeMessage(locale, reason string) string {
	message := localization.Message(locale, reason)
	if message != reason {
		return message
	}
	return localization.Message(locale, CodeStorageUnavailable)
}

func probeFailureReason(err error) string {
	if err == nil {
		return CodeStorageUnavailable
	}
	var rpc *extensionruntime.StorageRPCError
	if errors.As(err, &rpc) && strings.TrimSpace(rpc.Reason) != "" {
		return rpc.Reason
	}
	switch {
	case errors.Is(err, extensionruntime.ErrStorageCircuitOpen):
		return "extension.circuit_open"
	case errors.Is(err, extensionruntime.ErrStorageTimeout):
		return "extension.hook_timeout"
	case errors.Is(err, extensions.ErrRuntimeUnavailable), errors.Is(err, ErrStorageUnavailable):
		return CodeStorageUnavailable
	default:
		return CodeStorageUnavailable
	}
}

func probeFailureMessage(err error) string {
	if err == nil {
		return "Storage provider is unavailable."
	}
	var rpc *extensionruntime.StorageRPCError
	if errors.As(err, &rpc) {
		if msg := strings.TrimSpace(rpc.Message); msg != "" {
			return msg
		}
		if reason := strings.TrimSpace(rpc.Reason); reason != "" {
			return reason
		}
	}
	if msg := strings.TrimSpace(err.Error()); msg != "" {
		return msg
	}
	return "Storage provider is unavailable."
}

func (s *Service) runtimeSettings(ctx context.Context) (AttachmentSettings, error) {
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return AttachmentSettings{}, err
	}
	return settingsFromValues(values, nil), nil
}

// decorateSettings 填充 Candidates（core + 启用插件）。
func (s *Service) decorateSettings(ctx context.Context, settings AttachmentSettings, locale string) AttachmentSettings {
	settings.Candidates = s.listCandidates(ctx, locale)
	return settings
}

func (s *Service) listCandidates(ctx context.Context, locale string) []storage.Candidate {
	core := storage.CoreCandidates()
	if s.providers == nil {
		return core
	}
	plugins, err := s.providers.ListStorageProviderCandidates(ctx)
	if err != nil {
		return core
	}
	for i := range plugins {
		if !plugins[i].MultiInstance || plugins[i].ExtensionID == "" {
			continue
		}
		schema, schemaErr := s.providers.StorageProviderSchema(ctx, plugins[i].ExtensionID, locale)
		if schemaErr != nil {
			continue
		}
		plugins[i].Schema = &schema
		plugins[i].Label = schema.Label
	}
	out := storage.MergeCandidates(core, plugins)
	if s.instanceStore != nil {
		instances, listErr := s.instanceStore.ListStorageInstances(ctx)
		if listErr == nil {
			for _, instance := range instances {
				out = append(out, storage.Candidate{Value: storage.FormatInstanceSelection(instance.ID), Kind: storage.SelectionKindInstance, Label: instance.Name, ExtensionID: instance.ExtensionID, Available: true})
			}
		}
	}
	return out
}

// ensureProviderSelectable 校验要写入的 provider：core 已知驱动，或可用插件。
func (s *Service) ensureProviderSelectable(ctx context.Context, provider string) error {
	sel := storage.ParseSelection(provider)
	if sel.IsCoreDriverSelection() {
		if !storage.IsKnownDriver(sel.Driver) {
			return storage.ErrInvalidConfig
		}
		return nil
	}
	if sel.IsValidInstanceSelection() {
		if s.instanceStore == nil {
			return ErrStorageUnavailable
		}
		instance, err := s.instanceStore.GetStorageInstance(ctx, sel.InstanceID)
		if err != nil {
			return err
		}
		if s.providers == nil {
			return ErrStorageUnavailable
		}
		ok, err := s.providers.IsStorageProviderAvailable(ctx, instance.ExtensionID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStorageUnavailable
		}
		return nil
	}
	if !sel.IsValidPluginSelection() {
		return storage.ErrInvalidConfig
	}
	if s.providers == nil {
		// 无扩展目录时不允许选择插件（避免静默成功）。
		return ErrStorageUnavailable
	}
	candidates, err := s.providers.ListStorageProviderCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.ExtensionID != sel.ExtensionID || candidate.Available == false {
			continue
		}
		if candidate.MultiInstance {
			// 多实例插件本体没有一份可用于写入的全局配置，必须选择 instance:<uuid>。
			return storage.ErrInvalidConfig
		}
		return nil
	}
	return ErrStorageUnavailable
}

func (s *Service) adapterForSettings(ctx context.Context, settings AttachmentSettings, provider string) (storage.Adapter, error) {
	sel := storage.ParseSelection(provider)
	if sel.IsCoreDriverSelection() {
		config := storageConfig(settings)
		// 统一经 slot 语义解析驱动 id；未知驱动拒绝，避免静默落到 local。
		driver := storage.NormalizeProvider(sel.Driver)
		if !storage.IsKnownDriver(driver) {
			return nil, storage.ErrInvalidConfig
		}
		config.Provider = driver
		return s.adapterFactory(config)
	}
	if sel.IsValidInstanceSelection() {
		if s.instanceStore == nil || s.storageRuntime == nil {
			return nil, ErrStorageUnavailable
		}
		instance, err := s.instanceStore.GetStorageInstance(ctx, sel.InstanceID)
		if err != nil {
			return nil, ErrStorageUnavailable
		}
		if s.providers != nil {
			ok, availableErr := s.providers.IsStorageProviderAvailable(ctx, instance.ExtensionID)
			if availableErr != nil {
				return nil, availableErr
			}
			if !ok {
				return nil, ErrStorageUnavailable
			}
		}
		values, err := s.storageInstanceRuntimeValues(ctx, instance, "system")
		if err != nil {
			return nil, ErrStorageUnavailable
		}
		adapter, err := s.storageRuntime.NewStorageInstanceAdapter(ctx, instance.ExtensionID, instance.ID, values)
		if err != nil {
			return nil, ErrStorageUnavailable
		}
		return adapter, nil
	}
	// 插件路径：校验可用性后经 PluginStorageAdapter 走 RPC（E6.2）。
	if !sel.IsValidPluginSelection() {
		return nil, storage.ErrInvalidConfig
	}
	if err := s.ensureProviderSelectable(ctx, provider); err != nil {
		return nil, err
	}
	if s.storageRuntime == nil {
		// 无 runtime（如独立 worker 未注入）时 fail-closed，勿静默改用 local。
		return nil, ErrStorageUnavailable
	}
	adapter, err := s.storageRuntime.NewStorageAdapter(sel.ExtensionID)
	if err != nil {
		return nil, ErrStorageUnavailable
	}
	return adapter, nil
}

// isPluginStorageFailure 识别插件 RPC / 熔断 / 超时类错误，统一映射 storage_unavailable。
func isPluginStorageFailure(err error) bool {
	if err == nil {
		return false
	}
	var rpc *extensionruntime.StorageRPCError
	if errors.As(err, &rpc) {
		return true
	}
	return errors.Is(err, extensionruntime.ErrStorageCircuitOpen) ||
		errors.Is(err, extensionruntime.ErrStorageTimeout) ||
		errors.Is(err, extensions.ErrRuntimeUnavailable)
}

// SelectedStorageProviderExtension 返回当前 attachment.provider 选择指向的扩展 id。
// plugin: 选择直接返回扩展 id；instance: 选择通过实例目录解析所属扩展。
// 供 Host 启动自动更新判断“这个内置插件是否正在提供附件存储”。
func (s *Service) SelectedStorageProviderExtension(ctx context.Context) (string, error) {
	if s == nil || s.options == nil {
		return "", nil
	}
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return "", err
	}
	selection := storage.ParseSelection(values[options.NameAttachmentProvider])
	switch {
	case selection.IsValidPluginSelection():
		return selection.ExtensionID, nil
	case selection.IsValidInstanceSelection():
		if s.instanceStore == nil {
			return "", nil
		}
		instance, loadErr := s.instanceStore.GetStorageInstance(ctx, selection.InstanceID)
		if loadErr != nil {
			return "", loadErr
		}
		return instance.ExtensionID, nil
	default:
		return "", nil
	}
}

// ClearStorageProviderSelectionIfMatch 在禁用/卸载插件时，若当前选择指向该插件则恢复 local。
// 供 Extensions lifecycle 调用；失败时返回 error 以便调用方中止 drain。
func (s *Service) ClearStorageProviderSelectionIfMatch(ctx context.Context, extensionID string) error {
	if s == nil || s.options == nil {
		return nil
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return nil
	}
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return err
	}
	current := storage.ParseSelection(values[options.NameAttachmentProvider])
	if current.IsValidInstanceSelection() && s.instanceStore != nil {
		instance, loadErr := s.instanceStore.GetStorageInstance(ctx, current.InstanceID)
		if loadErr == nil && instance.ExtensionID == extensionID {
			// continue to local fallback
		} else {
			return nil
		}
	} else if !current.IsValidPluginSelection() || current.ExtensionID != extensionID {
		return nil
	}
	// 系统回落：写 local 并校验整组 attachment 选项仍合法。
	actor := identity.Actor{
		ID:          0,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionAttachmentSettings: true},
	}
	_, err = s.options.UpdateMany(ctx, actor, []options.UpdateInput{
		{Name: options.NameAttachmentProvider, Value: storage.ProviderLocal},
	})
	return err
}

type uploadMetadata struct {
	OriginalName string
	ContentType  string
	Extension    string
	ImageWidth   *int
	ImageHeight  *int
}

// applyMediaRegistryMIME 在 Host allowlist 通过后叠加插件 MIME 策略。
// purpose 区分 general/avatar/seo 等路径；无已发布策略时 no-op。
// 拒绝映射为 ErrInvalidAttachment，保持对外错误面稳定。
func (s *Service) applyMediaRegistryMIME(purpose string, metadata uploadMetadata) error {
	if s == nil || s.mediaRegistry == nil {
		return nil
	}
	if strings.TrimSpace(purpose) == "" {
		purpose = "general"
	}
	ext := strings.TrimPrefix(strings.ToLower(metadata.Extension), ".")
	if err := s.mediaRegistry.CheckUploadMIME(purpose, metadata.ContentType, ext); err != nil {
		if errors.Is(err, mediaregistry.ErrMediaRejected) || errors.Is(err, mediaregistry.ErrInvalid) {
			return ErrInvalidAttachment
		}
		return err
	}
	return nil
}

func inspectUpload(input UploadInput, settings AttachmentSettings) (uploadMetadata, error) {
	name := strings.TrimSpace(filepath.Base(input.OriginalName))
	if name == "" || name == "." {
		return uploadMetadata{}, ErrInvalidAttachment
	}
	extension := strings.ToLower(filepath.Ext(name))
	if !contains(settings.AllowedExtensions, extension) {
		return uploadMetadata{}, ErrInvalidAttachment
	}

	var sniff [512]byte
	n, _ := io.ReadFull(input.File, sniff[:])
	// 始终以服务端嗅探为准，忽略客户端 Content-Type，防止声称 image/png 实际上传 HTML/SVG。
	contentType := strings.ToLower(strings.Split(http.DetectContentType(sniff[:n]), ";")[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// 主动内容类型（HTML/SVG/JS）硬拒绝，即使 allowlist 含 image/* 或 */*。
	if options.IsAttachmentActiveContentType(contentType) {
		return uploadMetadata{}, ErrInvalidAttachment
	}
	if !mimeAllowed(settings.AllowedMIMETypes, contentType) {
		return uploadMetadata{}, ErrInvalidAttachment
	}

	var width *int
	var height *int
	if strings.HasPrefix(contentType, "image/") {
		if _, err := input.File.Seek(0, io.SeekStart); err == nil {
			if config, _, err := image.DecodeConfig(io.LimitReader(input.File, 4*1024*1024)); err == nil {
				widthValue := config.Width
				heightValue := config.Height
				width = &widthValue
				height = &heightValue
			}
		}
	}
	if _, err := input.File.Seek(0, io.SeekStart); err != nil {
		return uploadMetadata{}, err
	}
	return uploadMetadata{
		OriginalName: name,
		ContentType:  contentType,
		Extension:    extension,
		ImageWidth:   width,
		ImageHeight:  height,
	}, nil
}

func prepareAvatarUpload(input UploadInput, avatarOptions options.AvatarOptions) (preparedUpload, error) {
	name := strings.TrimSpace(filepath.Base(input.OriginalName))
	if name == "" || name == "." || input.File == nil || input.SizeBytes <= 0 {
		return preparedUpload{}, ErrInvalidAttachment
	}
	maxBytes := int64(avatarOptions.MaxSizeKB) * 1024
	if maxBytes <= 0 || input.SizeBytes > maxBytes {
		return preparedUpload{}, ErrInvalidAttachment
	}

	config, format, err := decodeImageConfig(input.File)
	if err != nil {
		return preparedUpload{}, ErrInvalidAttachment
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > avatarOptions.MaxDimension || config.Height > avatarOptions.MaxDimension {
		return preparedUpload{}, ErrInvalidAttachment
	}

	width := config.Width
	height := config.Height
	contentType, extension, ok := avatarFormatMetadata(format)
	if !ok {
		return preparedUpload{}, ErrInvalidAttachment
	}
	if format == "gif" {
		if !avatarOptions.AllowGIF {
			return preparedUpload{}, ErrInvalidAttachment
		}
		if _, err := input.File.Seek(0, io.SeekStart); err != nil {
			return preparedUpload{}, err
		}
		return preparedUpload{
			Reader:    input.File,
			SizeBytes: input.SizeBytes,
			Metadata: uploadMetadata{
				OriginalName: normalizedAvatarName(name, extension),
				ContentType:  contentType,
				Extension:    extension,
				ImageWidth:   &width,
				ImageHeight:  &height,
			},
		}, nil
	}

	if !avatarOptions.CompressEnabled {
		if _, err := input.File.Seek(0, io.SeekStart); err != nil {
			return preparedUpload{}, err
		}
		return preparedUpload{
			Reader:    input.File,
			SizeBytes: input.SizeBytes,
			Metadata: uploadMetadata{
				OriginalName: normalizedAvatarName(name, extension),
				ContentType:  contentType,
				Extension:    extension,
				ImageWidth:   &width,
				ImageHeight:  &height,
			},
		}, nil
	}

	if _, err := input.File.Seek(0, io.SeekStart); err != nil {
		return preparedUpload{}, err
	}
	img, _, err := decodeAutoOrientedImage(input.File)
	if err != nil {
		return preparedUpload{}, ErrInvalidAttachment
	}
	target := avatarOptions.TargetDimension
	if target <= 0 {
		target = 256
	}
	processed := centerFillImage(img, target, target)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, processed, &jpeg.Options{Quality: avatarOptions.CompressQuality}); err != nil {
		return preparedUpload{}, err
	}
	if int64(buf.Len()) > maxBytes {
		return preparedUpload{}, ErrInvalidAttachment
	}
	width = target
	height = target
	extension = ".jpg"
	contentType = "image/jpeg"
	return preparedUpload{
		Reader:    bytes.NewReader(buf.Bytes()),
		SizeBytes: int64(buf.Len()),
		Metadata: uploadMetadata{
			OriginalName: normalizedAvatarName(name, extension),
			ContentType:  contentType,
			Extension:    extension,
			ImageWidth:   &width,
			ImageHeight:  &height,
		},
	}, nil
}

func decodeImageConfig(file ReadSeekCloser) (image.Config, string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return image.Config{}, "", err
	}
	config, format, err := image.DecodeConfig(io.LimitReader(file, 8*1024*1024))
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil && err == nil {
		err = seekErr
	}
	return config, strings.ToLower(format), err
}

func avatarFormatMetadata(format string) (string, string, bool) {
	switch strings.ToLower(format) {
	case "jpeg":
		return "image/jpeg", ".jpg", true
	case "png":
		return "image/png", ".png", true
	case "gif":
		return "image/gif", ".gif", true
	default:
		return "", "", false
	}
}

func normalizedAvatarName(name string, extension string) string {
	base := strings.TrimSuffix(strings.TrimSpace(filepath.Base(name)), filepath.Ext(name))
	if base == "" || base == "." {
		base = "avatar"
	}
	return base + extension
}

func canViewAttachment(actor identity.Actor, attachment Attachment) bool {
	if attachment.Status != StatusActive {
		return actor.Can(identity.PermissionAttachmentManage)
	}
	if attachment.Visibility == VisibilityPublic {
		return true
	}
	if attachment.Owner != nil && attachment.Owner.ID == actor.ID && actor.IsActive() {
		return true
	}
	return actor.Can(identity.PermissionAttachmentManage)
}

func settingsFromValues(values map[string]string, _ map[string]bool) AttachmentSettings {
	// provider 保留原始选择（含 plugin:）；Normalize 仅用于 core 空白→local。
	rawProvider := strings.TrimSpace(read(values, options.NameAttachmentProvider, storage.ProviderLocal))
	sel := storage.ParseSelection(rawProvider)
	provider := sel.Raw
	if provider == "" {
		provider = storage.ProviderLocal
	}
	return AttachmentSettings{
		// F3.5 / E6.1：host slot、core 驱动目录与 candidates（decorateSettings 填充插件）。
		ProviderSlot:           storage.ProviderSlot,
		Drivers:                storage.DriverCatalog(),
		Candidates:             storage.CoreCandidates(),
		Provider:               provider,
		UploadEnabled:          enabled(values, options.NameAttachmentUploadEnabled, true),
		PathTemplate:           read(values, options.NameAttachmentPathTemplate, "{yyyy}/{mm}/{dd}/{public_id}{ext}"),
		PublicBaseURL:          read(values, options.NameAttachmentPublicBaseURL, ""),
		MaxFileSizeMB:          readInt(values, options.NameAttachmentMaxFileSizeMB, 20),
		AllowedExtensions:      splitList(read(values, options.NameAttachmentAllowedExtensions, ".jpg,.jpeg,.png,.gif,.webp,.pdf,.txt,.zip")),
		AllowedMIMETypes:       splitList(read(values, options.NameAttachmentAllowedMIMETypes, "image/jpeg,image/png,image/gif,image/webp,application/pdf,text/plain,application/zip")),
		DefaultVisibility:      read(values, options.NameAttachmentDefaultVisibility, VisibilityPublic),
		CleanupOrphanAfterDays: readInt(values, options.NameAttachmentCleanupOrphanDays, 30),
		Local: LocalSettings{
			Root:         read(values, options.NameAttachmentLocalRoot, "storage/app/attachments"),
			PublicPrefix: read(values, options.NameAttachmentLocalPublicPrefix, ""),
		},
	}
}

func settingsUpdateInputs(settings AttachmentSettings) []options.UpdateInput {
	inputs := []options.UpdateInput{
		{Name: options.NameAttachmentProvider, Value: settings.Provider},
		{Name: options.NameAttachmentUploadEnabled, Value: enabledValue(settings.UploadEnabled)},
		{Name: options.NameAttachmentPathTemplate, Value: settings.PathTemplate},
		{Name: options.NameAttachmentPublicBaseURL, Value: settings.PublicBaseURL},
		{Name: options.NameAttachmentMaxFileSizeMB, Value: strconv.Itoa(settings.MaxFileSizeMB)},
		{Name: options.NameAttachmentAllowedExtensions, Value: strings.Join(settings.AllowedExtensions, ",")},
		{Name: options.NameAttachmentAllowedMIMETypes, Value: strings.Join(settings.AllowedMIMETypes, ",")},
		{Name: options.NameAttachmentDefaultVisibility, Value: settings.DefaultVisibility},
		{Name: options.NameAttachmentCleanupOrphanDays, Value: strconv.Itoa(settings.CleanupOrphanAfterDays)},
		{Name: options.NameAttachmentLocalRoot, Value: settings.Local.Root},
		{Name: options.NameAttachmentLocalPublicPrefix, Value: settings.Local.PublicPrefix},
	}
	return inputs
}

func storageConfig(settings AttachmentSettings) storage.Config {
	return storage.Config{
		Provider:      settings.Provider,
		LocalRoot:     settings.Local.Root,
		PublicBaseURL: settings.PublicBaseURL,
		Local: storage.LocalConfig{
			PublicPrefix: settings.Local.PublicPrefix,
		},
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func mimeAllowed(allowed []string, contentType string) bool {
	// 双重保险：主动内容即使误入 allowlist 也不匹配成功。
	if options.IsAttachmentActiveContentType(contentType) {
		return false
	}
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		// */* 过于宽泛且易与主动内容交叉，忽略（配置层也应拒绝）。
		if item == "*/*" {
			continue
		}
		if item == contentType {
			return true
		}
		if strings.HasSuffix(item, "/*") {
			prefix := strings.TrimSuffix(item, "*")
			if strings.HasPrefix(contentType, prefix) {
				return true
			}
		}
	}
	return false
}

func read(values map[string]string, name string, fallback string) string {
	if values == nil {
		return fallback
	}
	value := strings.TrimSpace(values[name])
	if value == "" {
		return fallback
	}
	return value
}

func readInt(values map[string]string, name string, fallback int) int {
	parsed, err := strconv.Atoi(read(values, name, ""))
	if err != nil {
		return fallback
	}
	return parsed
}

func enabled(values map[string]string, name string, fallback bool) bool {
	switch strings.ToLower(read(values, name, "")) {
	case "enabled", "true", "1", "yes", "on":
		return true
	case "disabled", "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

func enabledValue(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

func (s AttachmentSettings) String() string {
	return fmt.Sprintf("provider=%s upload=%t", s.Provider, s.UploadEnabled)
}
