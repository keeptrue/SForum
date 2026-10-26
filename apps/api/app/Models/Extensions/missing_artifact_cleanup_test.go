package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestServiceCleanupMissingArtifactsUsesPreserveByDefault(t *testing.T) {
	root := t.TempDir()
	store := &missingArtifactCleanupFakeStore{
		fakeExtensionStore: fakeExtensionStore{items: map[string]Extension{
			"missing.plugin": missingArtifactExtension("missing.plugin", TypePlugin, filepath.Join(root, "plugin")),
			"missing.theme":  missingArtifactExtension("missing.theme", TypeTheme, filepath.Join(root, "theme")),
		}},
	}
	service := NewService(store, root)

	result, err := service.CleanupMissingArtifacts(t.Context(), extensionManager(), MissingArtifactCleanupInput{
		ExtensionIDs: []string{"missing.theme", "missing.plugin", "missing.plugin"},
	})
	if err != nil {
		t.Fatalf("CleanupMissingArtifacts returned error: %v", err)
	}
	if store.actorUserID != extensionManager().ID || len(store.cleaned) != 2 || len(result.Removed) != 2 {
		t.Fatalf("unexpected cleanup batch: actor=%d stored=%#v result=%#v", store.actorUserID, store.cleaned, result)
	}
	for index, id := range []string{"missing.plugin", "missing.theme"} {
		item := result.Removed[index]
		if item.ExtensionID != id || item.DataMode != MissingArtifactDataPreserve ||
			!item.RetainSettings || !item.BusinessDataKept {
			t.Fatalf("unexpected cleanup item: %#v", item)
		}
	}
}

func TestServiceCleanupMissingArtifactsRequiresSuperAdmin(t *testing.T) {
	store := &missingArtifactCleanupFakeStore{fakeExtensionStore: fakeExtensionStore{items: map[string]Extension{
		"missing.plugin": missingArtifactExtension("missing.plugin", TypePlugin, filepath.Join(t.TempDir(), "missing")),
	}}}
	service := NewService(store, t.TempDir())

	_, err := service.CleanupMissingArtifacts(t.Context(), identity.Actor{
		ID:     9,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}, MissingArtifactCleanupInput{ExtensionIDs: []string{"missing.plugin"}})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
	if len(store.cleaned) != 0 {
		t.Fatalf("permission denial must not start cleanup: %#v", store.cleaned)
	}
}

func TestServiceCleanupMissingArtifactsRejectsWholeInvalidBatch(t *testing.T) {
	root := t.TempDir()
	availablePath := filepath.Join(root, "available")
	if err := os.MkdirAll(availablePath, 0o755); err != nil {
		t.Fatalf("create available artifact: %v", err)
	}
	tests := []struct {
		name    string
		invalid Extension
		want    error
	}{
		{
			name:    "available artifact",
			invalid: missingArtifactExtension("available.plugin", TypePlugin, availablePath),
			want:    ErrMissingArtifactCleanupInvalid,
		},
		{
			name: "enabled artifact",
			invalid: func() Extension {
				item := missingArtifactExtension("enabled.plugin", TypePlugin, filepath.Join(root, "enabled"))
				item.Status = StatusEnabled
				return item
			}(),
			want: ErrMustDisableFirst,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			valid := missingArtifactExtension("missing.theme", TypeTheme, filepath.Join(root, test.name, "missing"))
			store := &missingArtifactCleanupFakeStore{fakeExtensionStore: fakeExtensionStore{items: map[string]Extension{
				valid.ID:        valid,
				test.invalid.ID: test.invalid,
			}}}
			service := NewService(store, root)

			_, err := service.CleanupMissingArtifacts(t.Context(), extensionManager(), MissingArtifactCleanupInput{
				ExtensionIDs: []string{valid.ID, test.invalid.ID},
				DataMode:     MissingArtifactDataDiscardSettings,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if len(store.cleaned) != 0 {
				t.Fatalf("invalid batch must be atomic: %#v", store.cleaned)
			}
		})
	}
}

type missingArtifactCleanupFakeStore struct {
	fakeExtensionStore
	actorUserID int64
	cleaned     []MissingArtifactCleanupItem
}

func (s *missingArtifactCleanupFakeStore) CleanupMissingArtifacts(
	_ context.Context,
	actorUserID int64,
	items []MissingArtifactCleanupItem,
) error {
	s.actorUserID = actorUserID
	s.cleaned = append([]MissingArtifactCleanupItem(nil), items...)
	return nil
}

func missingArtifactExtension(id, extensionType, packagePath string) Extension {
	return Extension{
		ID:            id,
		Name:          id,
		Type:          extensionType,
		Status:        StatusDisabled,
		Source:        SourceUploaded,
		IsDeletable:   true,
		Version:       "1.0.0",
		PackageDigest: id + "-digest",
		PackagePath:   packagePath,
	}
}
