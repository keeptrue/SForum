package ai_test

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// newEncryptedSecretService 返回具备加密能力的 Secret Store：Put 路径的
// allowed 用例必须用它，否则测不到真实写入行为。
func newEncryptedSecretService(t *testing.T) *secretstore.Service {
	t.Helper()
	cipher, err := cryptox.NewOptionCipher(hex.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	svc, err := secretstore.New(secretstore.NewMemoryStore(), cipher)
	if err != nil {
		t.Fatalf("create secret store: %v", err)
	}
	return svc
}

// 有加密密钥时必须写入成功：这是运营者配置完 APP_OPTION_ENC_KEY 后的正常路径。
func TestCredentialWriterPutSucceedsWithEncryption(t *testing.T) {
	writer := supportai.NewCredentialWriter(newEncryptedSecretService(t))
	version, err := writer.Put(context.Background(), supportai.SecretReferencePrefix+"core/ai.deepseek.api_key", "sk-test", "admin")
	if err != nil {
		t.Fatalf("put with encryption: %v", err)
	}
	if version <= 0 {
		t.Fatalf("expected a positive version, got %d", version)
	}
}

// 透明 cipher 下必须拒绝写入。运营者看到的是「缺少加密密钥」警告，若保存仍然
// 成功，密钥就会以明文落库而界面却显示已配置。
func TestCredentialWriterPutFailsClosedWithoutEncryption(t *testing.T) {
	transparent, err := cryptox.NewOptionCipher("")
	if err != nil {
		t.Fatalf("create transparent cipher: %v", err)
	}
	svc, err := secretstore.NewWithOptions(secretstore.Options{
		Store:            secretstore.NewMemoryStore(),
		Cipher:           transparent,
		AllowTransparent: true,
	})
	if err != nil {
		t.Fatalf("create transparent secret store: %v", err)
	}
	writer := supportai.NewCredentialWriter(svc)
	_, err = writer.Put(context.Background(), supportai.SecretReferencePrefix+"core/ai.deepseek.api_key", "sk-test", "admin")
	if !errors.Is(err, supportai.ErrCredentialEncryptionRequired) {
		t.Fatalf("expected ErrCredentialEncryptionRequired, got %v", err)
	}
}

// 空明文仍然报「缺少密钥」而不是「缺少加密」：调用方要能区分「没填」与
// 「部署不具备加密能力」这两种完全不同的处置方式。
func TestCredentialWriterEmptyPlaintextStaysCredentialMissing(t *testing.T) {
	writer := supportai.NewCredentialWriter(newEncryptedSecretService(t))
	_, err := writer.Put(context.Background(), supportai.SecretReferencePrefix+"core/ai.deepseek.api_key", "   ", "admin")
	if !errors.Is(err, supportai.ErrCredentialMissing) {
		t.Fatalf("expected ErrCredentialMissing, got %v", err)
	}
}
