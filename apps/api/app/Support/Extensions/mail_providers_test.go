package extensionsruntime

import (
	"context"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestMailProviderRegistryRejectsUnavailableProvider(t *testing.T) {
	store := &fakeMailProviderStore{items: map[string]extensions.Extension{
		"disabled": {ID: "disabled", Type: extensions.TypePlugin, Status: extensions.StatusDisabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "Disabled"}}}},
		"search":   {ID: "search", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: "search.provider", Label: "Search"}}}},
	}}
	registry := NewMailProviderRegistry(store)
	for _, id := range []string{"disabled", "search", "missing"} {
		if err := registry.Select(context.Background(), id); err == nil {
			t.Fatalf("expected %s to be rejected", id)
		}
	}
}

func TestMailProviderRegistrySelectsAndRestores(t *testing.T) {
	store := &fakeMailProviderStore{items: map[string]extensions.Extension{
		"smtp": {ID: "smtp", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "SMTP"}}}},
	}}
	registry := NewMailProviderRegistry(store)
	if err := registry.Select(context.Background(), "smtp"); err != nil {
		t.Fatal(err)
	}
	selected, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || selected.ExtensionID != "smtp" {
		t.Fatalf("unexpected selection: %#v %t %v", selected, ok, err)
	}
	if err := registry.RestoreDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, ok, err = registry.Selected(context.Background())
	if err != nil || ok {
		t.Fatalf("selection was not cleared: ok=%t err=%v", ok, err)
	}
}

type fakeMailProviderStore struct {
	items    map[string]extensions.Extension
	selected string
}

func (s *fakeMailProviderStore) GetMailProviderExtension(_ context.Context, id string) (extensions.Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	return item, nil
}
func (s *fakeMailProviderStore) SelectedMailProvider(context.Context) (string, error) {
	return s.selected, nil
}
func (s *fakeMailProviderStore) SelectMailProvider(_ context.Context, id string) error {
	s.selected = id
	return nil
}
func (s *fakeMailProviderStore) RestoreMailProvider(context.Context) error {
	s.selected = ""
	return nil
}
