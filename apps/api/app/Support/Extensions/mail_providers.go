package extensionsruntime

import (
	"context"
	"errors"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const (
	MailProviderSlot = "mail.provider"
	// builtinSMTPProviderID is the recommended default when no explicit
	// selection exists. It is a preference, not a hard-coded transport: any
	// enabled plugin declaring MailProviderSlot can still be selected.
	builtinSMTPProviderID = "sforum.smtp"
)

var ErrMailProviderUnavailable = errors.New("mail provider is unavailable")

// MailProviderState separates "nobody chose a provider" from "a provider was
// chosen but cannot run right now". Delivery treats the two differently:
// unconfigured is terminal, unavailable is retryable.
type MailProviderState string

const (
	MailProviderReady        MailProviderState = "ready"
	MailProviderUnavailable  MailProviderState = "unavailable"
	MailProviderUnconfigured MailProviderState = "unconfigured"
)

type MailProviderSelection struct {
	ExtensionID string `json:"extensionId"`
	Label       string `json:"label"`
}

type MailProviderStore interface {
	GetMailProviderExtension(context.Context, string) (extensions.Extension, error)
	List(context.Context) ([]extensions.Extension, error)
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
	if _, ok := mailProviderLabel(item); !ok {
		return ErrMailProviderUnavailable
	}
	return r.store.SelectMailProvider(ctx, extensionID)
}

// Resolve is the authoritative read used by delivery and the admin surface.
// It applies the recommended default only when no explicit row exists; a row
// that points at a disabled plugin stays a preference and reports unavailable.
func (r *MailProviderRegistry) Resolve(ctx context.Context) (MailProviderSelection, MailProviderState, error) {
	id, err := r.store.SelectedMailProvider(ctx)
	if err != nil {
		return MailProviderSelection{}, MailProviderUnconfigured, err
	}
	if id != "" {
		item, err := r.store.GetMailProviderExtension(ctx, id)
		if errors.Is(err, extensions.ErrExtensionNotFound) {
			return MailProviderSelection{}, MailProviderUnconfigured, nil
		}
		if err != nil {
			return MailProviderSelection{}, MailProviderUnconfigured, err
		}
		label, declared := mailProviderLabel(item)
		if strings.TrimSpace(label) == "" {
			label = strings.TrimSpace(item.Name)
		}
		if label == "" {
			label = item.ID
		}
		selection := MailProviderSelection{ExtensionID: id, Label: label}
		if item.Status == extensions.StatusEnabled && declared {
			return selection, MailProviderReady, nil
		}
		return selection, MailProviderUnavailable, nil
	}
	item, ok, err := r.defaultCandidate(ctx)
	if err != nil {
		return MailProviderSelection{}, MailProviderUnconfigured, err
	}
	if !ok {
		return MailProviderSelection{}, MailProviderUnconfigured, nil
	}
	label, _ := mailProviderLabel(item)
	return MailProviderSelection{ExtensionID: item.ID, Label: label}, MailProviderReady, nil
}

// Selected keeps the pre-default callers working. Delivery should use Resolve
// so a temporarily unavailable provider is retried instead of skipped.
func (r *MailProviderRegistry) Selected(ctx context.Context) (MailProviderSelection, bool, error) {
	selection, state, err := r.Resolve(ctx)
	return selection, state == MailProviderReady, err
}

// EnsureDefault persists the recommended provider when no explicit selection
// exists. It is idempotent and safe to run from both API and worker startup.
func (r *MailProviderRegistry) EnsureDefault(ctx context.Context) (string, error) {
	id, err := r.store.SelectedMailProvider(ctx)
	if err != nil || id != "" {
		return id, err
	}
	item, ok, err := r.defaultCandidate(ctx)
	if err != nil || !ok {
		return "", err
	}
	if err := r.store.SelectMailProvider(ctx, item.ID); err != nil {
		return "", err
	}
	return item.ID, nil
}

func (r *MailProviderRegistry) RestoreDefault(ctx context.Context) error {
	return r.store.RestoreMailProvider(ctx)
}

// defaultCandidate prefers the bundled SMTP provider, then a single enabled
// provider. Multiple third-party providers without SMTP stay ambiguous and
// require an explicit operator choice.
func (r *MailProviderRegistry) defaultCandidate(ctx context.Context) (extensions.Extension, bool, error) {
	items, err := r.store.List(ctx)
	if err != nil {
		return extensions.Extension{}, false, err
	}
	var single extensions.Extension
	count := 0
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled {
			continue
		}
		if _, ok := mailProviderLabel(item); !ok {
			continue
		}
		if item.ID == builtinSMTPProviderID {
			return item, true, nil
		}
		single = item
		count++
	}
	if count == 1 {
		return single, true, nil
	}
	return extensions.Extension{}, false, nil
}

func mailProviderLabel(item extensions.Extension) (string, bool) {
	for _, provider := range item.Manifest.Providers {
		if provider.Slot != MailProviderSlot {
			continue
		}
		label := strings.TrimSpace(provider.Label)
		if label == "" {
			label = item.ID
		}
		return label, true
	}
	return "", false
}
