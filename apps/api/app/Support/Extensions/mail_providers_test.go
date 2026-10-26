package extensionsruntime

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestMailProviderSelectionSerializesCamelCase(t *testing.T) {
	body, err := json.Marshal(MailProviderSelection{ExtensionID: "sforum.smtp", Label: "SMTP"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"extensionId":"sforum.smtp","label":"SMTP"}` {
		t.Fatalf("unexpected selection JSON: %s", body)
	}
}

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
	// Restore clears the explicit row; the recommended provider is resolved as
	// the default instead of leaving mail unconfigured.
	selected, ok, err = registry.Selected(context.Background())
	if err != nil || !ok || selected.ExtensionID != "smtp" {
		t.Fatalf("restore did not resolve the default: %#v %t %v", selected, ok, err)
	}
}

func TestMailProviderRegistryResolvesDefaultWithoutSelection(t *testing.T) {
	store := &fakeMailProviderStore{items: map[string]extensions.Extension{
		"sforum.smtp": {ID: "sforum.smtp", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "SMTP"}}}},
	}}
	registry := NewMailProviderRegistry(store)
	selection, state, err := registry.Resolve(context.Background())
	if err != nil || state != MailProviderReady || selection.ExtensionID != "sforum.smtp" {
		t.Fatalf("unexpected default resolve: %#v %q %v", selection, state, err)
	}
	selected, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || selected.ExtensionID != "sforum.smtp" {
		t.Fatalf("Selected did not resolve the default: %#v %t %v", selected, ok, err)
	}
	id, err := registry.EnsureDefault(context.Background())
	if err != nil || id != "sforum.smtp" || store.selected != "sforum.smtp" {
		t.Fatalf("EnsureDefault id=%q stored=%q err=%v", id, store.selected, err)
	}
}

func TestMailProviderRegistryKeepsDisabledSelectionUnavailable(t *testing.T) {
	store := &fakeMailProviderStore{
		items: map[string]extensions.Extension{
			"sforum.smtp": {ID: "sforum.smtp", Type: extensions.TypePlugin, Status: extensions.StatusDisabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "SMTP"}}}},
		},
		selected: "sforum.smtp",
	}
	registry := NewMailProviderRegistry(store)
	selection, state, err := registry.Resolve(context.Background())
	if err != nil || state != MailProviderUnavailable || selection.ExtensionID != "sforum.smtp" {
		t.Fatalf("unexpected disabled resolve: %#v %q %v", selection, state, err)
	}
	if _, ok, err := registry.Selected(context.Background()); err != nil || ok {
		t.Fatalf("Selected must not report a disabled provider ready: ok=%t err=%v", ok, err)
	}
	if id, err := registry.EnsureDefault(context.Background()); err != nil || id != "sforum.smtp" {
		t.Fatalf("EnsureDefault must not overwrite an explicit preference: id=%q err=%v", id, err)
	}
}

func TestMailProviderRegistryDoesNotGuessAmongMultipleProviders(t *testing.T) {
	store := &fakeMailProviderStore{items: map[string]extensions.Extension{
		"a.mail": {ID: "a.mail", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "A"}}}},
		"b.mail": {ID: "b.mail", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "B"}}}},
	}}
	registry := NewMailProviderRegistry(store)
	selection, state, err := registry.Resolve(context.Background())
	if err != nil || state != MailProviderUnconfigured || selection.ExtensionID != "" {
		t.Fatalf("ambiguous providers must stay unconfigured: %#v %q %v", selection, state, err)
	}
	if id, err := registry.EnsureDefault(context.Background()); err != nil || id != "" || store.selected != "" {
		t.Fatalf("EnsureDefault must not pick among peers: id=%q stored=%q err=%v", id, store.selected, err)
	}
}

func TestMailProviderRegistryPrefersBuiltinSMTP(t *testing.T) {
	store := &fakeMailProviderStore{items: map[string]extensions.Extension{
		"a.mail":      {ID: "a.mail", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "A"}}}},
		"sforum.smtp": {ID: "sforum.smtp", Type: extensions.TypePlugin, Status: extensions.StatusEnabled, Manifest: extensions.Manifest{Providers: []extensions.ManifestProvider{{Slot: MailProviderSlot, Label: "SMTP"}}}},
	}}
	registry := NewMailProviderRegistry(store)
	selection, state, err := registry.Resolve(context.Background())
	if err != nil || state != MailProviderReady || selection.ExtensionID != "sforum.smtp" {
		t.Fatalf("builtin SMTP must win the default: %#v %q %v", selection, state, err)
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

func (s *fakeMailProviderStore) List(context.Context) ([]extensions.Extension, error) {
	items := make([]extensions.Extension, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
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
