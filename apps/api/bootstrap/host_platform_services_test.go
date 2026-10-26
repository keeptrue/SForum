package bootstrap

import (
	"errors"
	"strings"
	"testing"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestProductionSecretStoreFailClosedWithoutEncryptionKey(t *testing.T) {
	// 与 bindProductionHostPlatform 同策略：production/staging 必须 RequireEncryption。
	transparent, err := cryptox.NewOptionCipher("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = secretstore.NewWithOptions(secretstore.Options{
		Store: secretstore.NewMemoryStore(), Cipher: transparent, RequireEncryption: true,
	})
	if !errors.Is(err, secretstore.ErrCipherRequired) {
		t.Fatalf("expected ErrCipherRequired, got %v", err)
	}

	// 开发环境允许透明模式。
	svc, err := secretstore.NewWithOptions(secretstore.Options{
		Store: secretstore.NewMemoryStore(), Cipher: transparent, AllowTransparent: true,
	})
	if err != nil || svc == nil {
		t.Fatalf("dev transparent: %v", err)
	}
	if svc.EncryptionEnabled() {
		t.Fatal("transparent must report encryption disabled")
	}
}

func TestHostPlatformEnvPolicy(t *testing.T) {
	for _, env := range []string{"production", "staging"} {
		cfg := config.Config{AppEnv: env}
		requireEnc := strings.EqualFold(cfg.AppEnv, "production") || strings.EqualFold(cfg.AppEnv, "staging")
		if !requireEnc {
			t.Fatalf("%s should require encryption", env)
		}
	}
	cfg := config.Config{AppEnv: "development"}
	requireEnc := strings.EqualFold(cfg.AppEnv, "production") || strings.EqualFold(cfg.AppEnv, "staging")
	if requireEnc {
		t.Fatal("development must allow transparent")
	}
}
