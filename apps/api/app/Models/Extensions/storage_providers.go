package extensions

import (
	"context"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

type AttachmentStorageProviderCatalog struct{ catalog *CatalogService }

func NewAttachmentStorageProviderCatalog(service *Service) *AttachmentStorageProviderCatalog {
	if service == nil {
		return nil
	}
	return &AttachmentStorageProviderCatalog{catalog: service.catalog}
}

// ListStorageProviderCandidates 返回已启用且声明 attachment.storage.provider 的插件候选（E6.1）。
// 不要求 actor：由附件设置 API 在已鉴权路径调用。
func (a *AttachmentStorageProviderCatalog) ListStorageProviderCandidates(ctx context.Context) ([]storage.Candidate, error) {
	if a == nil || a.catalog == nil {
		return nil, ErrExtensionNotFound
	}
	if a.catalog.safeMode {
		return []storage.Candidate{}, nil
	}
	items, err := a.catalog.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]storage.Candidate, 0)
	for _, item := range items {
		if item.Type != TypePlugin || item.Status != StatusEnabled {
			continue
		}
		label, ok := storageProviderLabel(item)
		if !ok {
			continue
		}
		settingsPath := ""
		if item.Manifest.Admin.Entry != "" {
			// 管理端扩展设置页固定命名空间。
			settingsPath = "/extensions/" + item.ID + "/pages/settings"
		}
		candidate := storage.PluginCandidate(item.ID, label, settingsPath)
		for _, provider := range item.Manifest.Providers {
			if strings.TrimSpace(provider.Slot) == storage.ProviderSlot {
				candidate.MultiInstance = provider.MultiInstance
				if provider.MultiInstance {
					candidate.SettingsPath = "/attachments/settings?provider=" + item.ID
					schema := storageProviderSchema(item, "")
					candidate.Schema = &schema
				}
				break
			}
		}
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ExtensionID < out[j].ExtensionID
	})
	return out, nil
}

// IsStorageProviderAvailable 判断扩展是否可作为存储提供方被选中。
func (a *AttachmentStorageProviderCatalog) IsStorageProviderAvailable(ctx context.Context, extensionID string) (bool, error) {
	if a == nil || a.catalog == nil {
		return false, ErrExtensionNotFound
	}
	if a.catalog.safeMode {
		return false, nil
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" {
		return false, nil
	}
	item, err := a.catalog.store.Get(ctx, extensionID)
	if err != nil {
		if err == ErrExtensionNotFound {
			return false, nil
		}
		return false, err
	}
	if item.Type != TypePlugin || item.Status != StatusEnabled {
		return false, nil
	}
	_, ok := storageProviderLabel(item)
	return ok, nil
}

func (a *AttachmentStorageProviderCatalog) StorageProviderSchema(ctx context.Context, extensionID, locale string) (storage.ProviderSchema, error) {
	if a == nil || a.catalog == nil {
		return storage.ProviderSchema{}, ErrExtensionNotFound
	}
	if a.catalog.safeMode {
		return storage.ProviderSchema{}, ErrSafeModeActive
	}
	item, err := a.catalog.store.Get(ctx, normalizeID(extensionID))
	if err != nil {
		return storage.ProviderSchema{}, err
	}
	_, ok := storageProviderLabel(item)
	if !ok {
		return storage.ProviderSchema{}, ErrInvalidManifest
	}
	return storageProviderSchema(item, locale), nil
}

func storageProviderSchema(item Extension, locale string) storage.ProviderSchema {
	label, _ := storageProviderLabel(item)
	if localizedName := strings.TrimSpace(extensionmanifest.LocalizedDisplay(item.Manifest, locale).Name); localizedName != "" {
		label = localizedName
	}
	fields := make([]storage.ProviderField, 0, len(item.Manifest.Settings))
	for _, field := range item.Manifest.Settings {
		options := make([]storage.ProviderOption, 0, len(field.Options))
		for _, option := range field.Options {
			options = append(options, storage.ProviderOption{Value: option.Value, Label: option.Label.Resolve(locale), Description: option.Description.Resolve(locale)})
		}
		fields = append(fields, storage.ProviderField{
			Key: field.Key, Label: field.Label.Resolve(locale), Description: field.Description.Resolve(locale),
			Type: field.Type, Default: field.Default, RecommendedValue: field.RecommendedValue,
			Placeholder: field.Placeholder.Resolve(locale), Required: field.Required, Options: options,
		})
	}
	return storage.ProviderSchema{ExtensionID: item.ID, Label: label, Fields: fields}
}

func storageProviderLabel(item Extension) (string, bool) {
	for _, provider := range item.Manifest.Providers {
		if strings.TrimSpace(provider.Slot) != storage.ProviderSlot {
			continue
		}
		label := strings.TrimSpace(provider.Label)
		if label == "" {
			label = item.Name
		}
		if label == "" {
			label = item.ID
		}
		return label, true
	}
	return "", false
}
