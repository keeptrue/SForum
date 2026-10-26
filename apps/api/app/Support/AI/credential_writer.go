package ai

import (
	"context"
	"strings"

	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// CredentialWriter 把 provider 密钥写入 Host Secret Store。它是控制台唯一的密钥
// 入口：设置文档里只保留 sforum.secret:// 引用，明文从不落库、从不回传。
type CredentialWriter struct {
	Secrets *secretstore.Service
	// Purposes 是允许的解析用途；为空时使用网关的默认用途。
	Purposes []string
}

func NewCredentialWriter(secrets *secretstore.Service) *CredentialWriter {
	return &CredentialWriter{Secrets: secrets}
}

// Put 写入新版本并返回版本号。空明文被拒绝：清空密钥应走显式的撤销语义，
// 而不是把空串当成一个合法密钥写进去。
//
// 没有可用加密密钥时一律拒绝：Secret Store 在透明模式下会把明文原样落库，
// 那会让运营者以为凭证已加密。写入前做检查，覆盖所有调用路径而不只是 HTTP。
func (w *CredentialWriter) Put(ctx context.Context, reference, plaintext, actor string) (int64, error) {
	if w == nil || w.Secrets == nil {
		return 0, ErrCredentialMissing
	}
	if !w.Secrets.EncryptionEnabled() {
		return 0, ErrCredentialEncryptionRequired
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return 0, ErrCredentialMissing
	}
	ref, err := secretstore.ParseReference(reference)
	if err != nil {
		return 0, ErrCredentialMissing
	}
	purposes := w.Purposes
	if len(purposes) == 0 {
		purposes = []string{CredentialPurpose}
	}
	meta, err := w.Secrets.Put(ctx, ref, []byte(plaintext), secretstore.PutOptions{
		Actor:    strings.TrimSpace(actor),
		Purposes: purposes,
	})
	if err != nil {
		return 0, err
	}
	return meta.Version, nil
}

// Configured 报告引用是否已有可用密钥。控制台据此显示「已配置 / 未配置」，
// 而不是回传任何明文。
func (w *CredentialWriter) Configured(ctx context.Context, reference string) (bool, error) {
	if w == nil || w.Secrets == nil {
		return false, nil
	}
	ref, err := secretstore.ParseReference(reference)
	if err != nil {
		return false, nil
	}
	meta, err := w.Secrets.Meta(ctx, ref)
	if err != nil {
		return false, nil
	}
	return meta.SecretSet, nil
}
