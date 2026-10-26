package ai

import (
	"context"
	"strings"
	"time"

	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// AI 网关解析 provider 密钥时声明的用途。Secret Store 会把它写进审计流水，
// 使「谁在什么时候取用过 AI 密钥」可查。
const CredentialPurpose = "ai.provider.credential"

// DefaultCredentialTTL 是密钥租约时长。租约到期即失效，调用方不得长期缓存明文。
const DefaultCredentialTTL = 2 * time.Minute

// SecretStoreResolver 用 Host Secret Store 实现 CredentialResolver。它只在一次
// 请求构造期间持有明文，不做任何跨调用缓存。
type SecretStoreResolver struct {
	Secrets *secretstore.Service
	Purpose string
	TTL     time.Duration
}

func NewSecretStoreResolver(secrets *secretstore.Service) *SecretStoreResolver {
	return &SecretStoreResolver{Secrets: secrets}
}

func (r *SecretStoreResolver) ResolveAIKey(ctx context.Context, reference string) (string, error) {
	if r == nil || r.Secrets == nil {
		return "", ErrCredentialMissing
	}
	ref, err := secretstore.ParseReference(reference)
	if err != nil {
		return "", ErrCredentialMissing
	}
	purpose := strings.TrimSpace(r.Purpose)
	if purpose == "" {
		purpose = CredentialPurpose
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = DefaultCredentialTTL
	}
	// 空 Caller 表示 Host 自身：它有权解析 core 命名空间，插件无权。
	lease, err := r.Secrets.Resolve(ctx, secretstore.Caller{}, ref, purpose, ttl)
	if err != nil {
		return "", ErrCredentialMissing
	}
	value := strings.TrimSpace(string(lease.Value))
	if value == "" {
		return "", ErrCredentialMissing
	}
	return value, nil
}
