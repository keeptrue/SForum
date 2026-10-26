package extensions

import (
	"context"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	"github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

func TestEncryptSecretSettingsAtRest(t *testing.T) {
	cipher, err := crypto.NewOptionCipher(strings.Repeat("ab", 32)) // 32 bytes hex = 64 chars
	if err != nil {
		// 需要 64 hex chars
		cipher, err = crypto.NewOptionCipher(strings.Repeat("a", 64))
	}
	if err != nil {
		t.Fatal(err)
	}
	item := installedExtension("sec.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "api_token", Label: LocalizedText{Default: "Token"}, Type: "secret"},
		{Key: "label", Label: LocalizedText{Default: "Label"}, Type: "text"},
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}, settings: map[string]map[string]string{}}
	service := NewService(store, t.TempDir())
	WithCipher(cipher)(service)

	_, err = service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"api_token": "super-secret", "label": "shown"},
	}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	raw := store.settings[item.ID]
	if raw["api_token"] == "super-secret" || !crypto.IsEncrypted(raw["api_token"]) {
		t.Fatalf("secret must be ciphertext in store: %#v", raw)
	}
	if raw["label"] != "shown" {
		t.Fatalf("non-secret must stay plain: %#v", raw)
	}

	// API 视图掩码
	view, err := service.Settings(context.Background(), extensionManager(), item.ID, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range view.Items {
		if row.Key == "api_token" {
			if row.Value != "" || !row.SecretSet {
				t.Fatalf("secret view: %#v", row)
			}
		}
	}
}

func TestLegacyPlaintextSecretMigrates(t *testing.T) {
	cipher, err := crypto.NewOptionCipher(strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	item := installedExtension("legacy.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "password", Label: LocalizedText{Default: "P"}, Type: "secret"},
	}
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"password": "legacy-plain"}},
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipher)(service)

	values, err := service.listDecryptedSettings(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if values["password"] != "legacy-plain" {
		t.Fatalf("decrypt legacy: %q", values["password"])
	}
	if stored := store.settings[item.ID]["password"]; !crypto.IsEncrypted(stored) {
		t.Fatalf("expected migrate to ciphertext, got %q", stored)
	}
}

func TestLegacyPlaintextSecretMigrationDoesNotOverwriteConcurrentUpdate(t *testing.T) {
	cipher, _ := crypto.NewOptionCipher(strings.Repeat("9", 64))
	item := installedExtension("legacy-race.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"}}
	concurrent, _ := cipher.Encrypt("admin-updated")
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"token": "legacy-plain"}},
	}
	store.beforeCAS = func() {
		store.settings[item.ID]["token"] = concurrent
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipher)(service)

	values, err := service.listDecryptedSettings(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if values["token"] != "legacy-plain" {
		t.Fatalf("read snapshot changed unexpectedly: %#v", values)
	}
	if store.settings[item.ID]["token"] != concurrent {
		t.Fatal("lazy migration overwrote the concurrent administrator update")
	}
}

func TestWrongKeyDoesNotClearSecret(t *testing.T) {
	cipherA, _ := crypto.NewOptionCipher(strings.Repeat("c", 64))
	cipherB, _ := crypto.NewOptionCipher(strings.Repeat("d", 64))
	item := installedExtension("rot.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"},
	}
	enc, _ := cipherA.Encrypt("real-token")
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"token": enc}},
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipherB)(service)

	_, err := service.listDecryptedSettings(context.Background(), item)
	if err == nil {
		t.Fatal("wrong key must fail closed")
	}
	// 存储不得被清空
	if store.settings[item.ID]["token"] != enc {
		t.Fatalf("store mutated: %#v", store.settings[item.ID])
	}
}

func TestRuntimeAndHostAPISettingsUseDecryptedService(t *testing.T) {
	cipher, _ := crypto.NewOptionCipher(strings.Repeat("f", 64))
	item := installedExtension("runtime.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Capabilities = []string{capabilities.HostAPI, capabilities.SettingsOwn}
	item.Manifest.Settings = []ManifestSetting{
		{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"},
	}
	enc, _ := cipher.Encrypt("runtime-secret")
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"token": enc}},
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipher)(service)

	values, err := service.ListSettings(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if values["token"] != "runtime-secret" {
		t.Fatalf("runtime settings must be decrypted, got %#v", values)
	}

	host := hostapi.New(hostapi.Config{Capabilities: service, Settings: service})
	resp := host.Call(context.Background(), hostapi.Request{Method: hostapi.MethodGetSettings, ExtensionID: item.ID})
	if !resp.OK {
		t.Fatalf("host settings failed: %#v", resp)
	}
	settings, ok := resp.Data["settings"].(map[string]any)
	if !ok || settings["token"] != "runtime-secret" {
		t.Fatalf("host settings must be decrypted, got %#v", resp.Data)
	}
}

func TestHostAPISettingsWrongKeyFailsClosed(t *testing.T) {
	cipherA, _ := crypto.NewOptionCipher(strings.Repeat("1", 64))
	cipherB, _ := crypto.NewOptionCipher(strings.Repeat("2", 64))
	item := installedExtension("wrong-key.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Capabilities = []string{capabilities.HostAPI, capabilities.SettingsOwn}
	item.Manifest.Settings = []ManifestSetting{
		{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"},
	}
	enc, _ := cipherA.Encrypt("must-not-leak")
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"token": enc}},
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipherB)(service)

	host := hostapi.New(hostapi.Config{Capabilities: service, Settings: service})
	resp := host.Call(context.Background(), hostapi.Request{Method: hostapi.MethodGetSettings, ExtensionID: item.ID})
	if resp.OK || resp.Reason != "host.settings_failed" {
		t.Fatalf("wrong key must fail closed, got %#v", resp)
	}
	if resp.Data != nil || strings.Contains(resp.Message, enc) || strings.Contains(resp.Message, "must-not-leak") {
		t.Fatalf("wrong key response leaked settings: %#v", resp)
	}
}

func TestBlankSecretUpdatePreservesValue(t *testing.T) {
	cipher, _ := crypto.NewOptionCipher(strings.Repeat("e", 64))
	item := installedExtension("keep.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Manifest.Settings = []ManifestSetting{
		{Key: "token", Label: LocalizedText{Default: "T"}, Type: "secret"},
		{Key: "name", Label: LocalizedText{Default: "N"}, Type: "text"},
	}
	enc, _ := cipher.Encrypt("keep-me")
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"token": enc, "name": "old"}},
	}
	service := NewService(store, t.TempDir())
	WithCipher(cipher)(service)

	_, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"token": "", "name": "new"},
	}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	// 空白 secret 保留；name 更新
	if store.settings[item.ID]["name"] != "new" {
		t.Fatalf("name=%q", store.settings[item.ID]["name"])
	}
	// 仍能解密为 keep-me
	values, err := service.listDecryptedSettings(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if values["token"] != "keep-me" {
		t.Fatalf("token=%q", values["token"])
	}
}
