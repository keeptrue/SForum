package extensionsruntime

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const MailProviderSlot = "mail.provider"

var ErrMailProviderUnavailable = errors.New("mail provider is unavailable")

type MailProviderSelection struct {
	ExtensionID string
	Label       string
}

type MailProviderStore interface {
	GetMailProviderExtension(context.Context, string) (extensions.Extension, error)
	SelectedMailProvider(context.Context) (string, error)
	SelectMailProvider(context.Context, string) error
	RestoreMailProvider(context.Context) error
}

type MailProviderRegistry struct{ store MailProviderStore }

func NewMailProviderRegistry(store MailProviderStore) *MailProviderRegistry {
	return &MailProviderRegistry{store: store}
}

func (r *MailProviderRegistry) Select(ctx context.Context, extensionID string) error {
	item, err := r.store.GetMailProviderExtension(ctx, extensionID)
	if err != nil || item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled {
		return ErrMailProviderUnavailable
	}
	for _, provider := range item.Manifest.Providers {
		if provider.Slot == MailProviderSlot {
			return r.store.SelectMailProvider(ctx, extensionID)
		}
	}
	return ErrMailProviderUnavailable
}

func (r *MailProviderRegistry) Selected(ctx context.Context) (MailProviderSelection, bool, error) {
	id, err := r.store.SelectedMailProvider(ctx)
	if err != nil || id == "" {
		return MailProviderSelection{}, false, err
	}
	item, err := r.store.GetMailProviderExtension(ctx, id)
	if err != nil || item.Status != extensions.StatusEnabled {
		return MailProviderSelection{}, false, nil
	}
	for _, provider := range item.Manifest.Providers {
		if provider.Slot == MailProviderSlot {
			return MailProviderSelection{ExtensionID: id, Label: provider.Label}, true, nil
		}
	}
	return MailProviderSelection{}, false, nil
}

func (r *MailProviderRegistry) RestoreDefault(ctx context.Context) error {
	return r.store.RestoreMailProvider(ctx)
}
