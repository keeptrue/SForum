package apitokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

const (
	// TokenPrefix 明文前缀，便于识别与文档说明。
	TokenPrefix = "sft_"
	// MaxNameLength / MaxScopes 简单上限。
	MaxNameLength = 80
	MaxScopes     = 64
)

// ActorLoader 加载用户 Actor（含当前权限）。
type ActorLoader interface {
	LoadActor(ctx context.Context, userID int64) (identity.Actor, error)
}

// Service 管理 PAT 生命周期与校验。
type Service struct {
	store   Store
	actors  ActorLoader
	auditor audit.Writer
	now     func() time.Time
}

func NewService(store Store, actors ActorLoader) *Service {
	return &Service{store: store, actors: actors, now: time.Now}
}

func (s *Service) WithAuditor(w audit.Writer) *Service {
	s.auditor = w
	return s
}

func (s *Service) List(ctx context.Context, userID int64, includeRevoked bool) ([]Token, error) {
	records, err := s.store.ListByUser(ctx, userID, includeRevoked)
	if err != nil {
		return nil, err
	}
	out := make([]Token, 0, len(records))
	for _, rec := range records {
		out = append(out, toPublic(rec))
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, actor identity.Actor, input CreateInput) (CreatedToken, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > MaxNameLength {
		return CreatedToken{}, ErrInvalidInput
	}
	scopes, err := s.normalizeScopes(actor, input.Scopes)
	if err != nil {
		return CreatedToken{}, err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.now().UTC()) {
		return CreatedToken{}, ErrInvalidInput
	}

	publicID, secret, plaintext, err := generateToken()
	if err != nil {
		return CreatedToken{}, err
	}
	rec, err := s.store.Create(ctx, actor.ID, publicID, hashSecret(secret), name, scopes, input.ExpiresAt)
	if err != nil {
		return CreatedToken{}, err
	}
	s.audit(ctx, actor.ID, "api_token.create", map[string]any{
		"tokenId": rec.ID, "publicId": publicID, "scopes": scopes,
	})
	return CreatedToken{Token: toPublic(rec), Plaintext: plaintext}, nil
}

func (s *Service) Revoke(ctx context.Context, actor identity.Actor, id int64) error {
	if err := s.store.Revoke(ctx, actor.ID, id); err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "api_token.revoke", map[string]any{"tokenId": id})
	return nil
}

// Rotate 撤销旧令牌并签发同名/同 scopes 的新令牌。
func (s *Service) Rotate(ctx context.Context, actor identity.Actor, id int64) (CreatedToken, error) {
	old, err := s.store.GetByIDForUser(ctx, actor.ID, id)
	if err != nil {
		return CreatedToken{}, err
	}
	if old.RevokedAt != nil {
		return CreatedToken{}, ErrTokenRevoked
	}
	if err := s.store.Revoke(ctx, actor.ID, id); err != nil {
		return CreatedToken{}, err
	}
	created, err := s.Create(ctx, actor, CreateInput{Name: old.Name, Scopes: old.Scopes, ExpiresAt: old.ExpiresAt})
	if err != nil {
		return CreatedToken{}, err
	}
	s.audit(ctx, actor.ID, "api_token.rotate", map[string]any{
		"oldTokenId": id, "newTokenId": created.ID, "publicId": created.PublicID,
	})
	return created, nil
}

// Authenticated 是 Bearer 校验成功后的主体。
type Authenticated struct {
	UserID   int64
	TokenID  int64
	PublicID string
	Scopes   []string
}

// AuthenticatePlaintext 校验完整明文令牌。
func (s *Service) AuthenticatePlaintext(ctx context.Context, plaintext string) (Authenticated, error) {
	publicID, secret, ok := parseToken(plaintext)
	if !ok {
		return Authenticated{}, ErrTokenInvalid
	}
	rec, err := s.store.GetByPublicID(ctx, publicID)
	if err != nil {
		return Authenticated{}, ErrTokenInvalid
	}
	if rec.RevokedAt != nil {
		return Authenticated{}, ErrTokenRevoked
	}
	if rec.ExpiresAt != nil && !rec.ExpiresAt.After(s.now().UTC()) {
		return Authenticated{}, ErrTokenExpired
	}
	if subtle.ConstantTimeCompare([]byte(rec.TokenHash), []byte(hashSecret(secret))) != 1 {
		return Authenticated{}, ErrTokenInvalid
	}
	// PAT 不能脱离当前账号状态存活。用户不存在、被禁用/封禁或 Actor 源缺失时
	// 统一按无效 token 处理，避免只读取 userID 的控制器绕过账号停用。
	if s.actors == nil {
		return Authenticated{}, ErrTokenInvalid
	}
	actor, err := s.actors.LoadActor(ctx, rec.UserID)
	if err != nil || actor.ID != rec.UserID || actor.Status != identity.UserStatusActive {
		return Authenticated{}, ErrTokenInvalid
	}
	// last_used 更新失败不阻断鉴权。
	_ = s.store.TouchLastUsed(ctx, rec.ID)
	return Authenticated{
		UserID: rec.UserID, TokenID: rec.ID, PublicID: rec.PublicID, Scopes: append([]string{}, rec.Scopes...),
	}, nil
}

// RestrictActor 将 Actor 权限收窄为「用户当前权限 ∩ token scopes」。
// 创建时 scopes 是当时的上限；鉴权时必须再与当前用户权限求交，
// 避免用户被撤权后 PAT 仍保留过期能力。
// 求交使用原始 Actor（含 super_admin 全能）；结果始终去掉 super_admin 角色键，
// 强制 PAT 只能使用 allowed map 中的权限，不能等价于无限 cookie 会话。
func RestrictActor(actor identity.Actor, scopes []string) identity.Actor {
	// 先按当前身份求交（super_admin 的 Can 对任意 scope 为 true）。
	current := actor
	allowed := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if current.Can(scope) {
			allowed[scope] = true
		}
	}
	// PAT 结果禁止 super_admin 绕过。
	roleKeys := make([]string, 0, len(actor.RoleKeys))
	for _, key := range actor.RoleKeys {
		if key != identity.RoleSuperAdmin {
			roleKeys = append(roleKeys, key)
		}
	}
	actor.RoleKeys = roleKeys
	actor.Permissions = allowed
	return actor
}

// Context keys for Bearer PAT (request-scoped).
type contextKey string

const (
	ContextUserID  contextKey = "apitokens.user_id"
	ContextTokenID contextKey = "apitokens.token_id"
	ContextScopes  contextKey = "apitokens.scopes"
)

func WithAuth(ctx context.Context, auth Authenticated) context.Context {
	ctx = context.WithValue(ctx, ContextUserID, auth.UserID)
	ctx = context.WithValue(ctx, ContextTokenID, auth.TokenID)
	ctx = context.WithValue(ctx, ContextScopes, append([]string{}, auth.Scopes...))
	return ctx
}

func UserIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(ContextUserID).(int64)
	return id, ok && id > 0
}

func ScopesFromContext(ctx context.Context) []string {
	scopes, _ := ctx.Value(ContextScopes).([]string)
	return scopes
}

func TokenIDFromContext(ctx context.Context) int64 {
	id, _ := ctx.Value(ContextTokenID).(int64)
	return id
}

func (s *Service) normalizeScopes(actor identity.Actor, scopes []string) ([]string, error) {
	if len(scopes) == 0 || len(scopes) > MaxScopes {
		return nil, ErrInvalidInput
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, dup := seen[scope]; dup {
			continue
		}
		if !actor.Can(scope) {
			return nil, ErrScopeNotAllowed
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	if len(out) == 0 {
		return nil, ErrInvalidInput
	}
	return out, nil
}

func (s *Service) audit(ctx context.Context, actorUserID int64, action string, meta map[string]any) {
	if s.auditor == nil {
		return
	}
	_ = s.auditor.Append(ctx, audit.Event{ActorUserID: actorUserID, Action: action, Metadata: meta})
}

func toPublic(rec Record) Token {
	return Token{
		ID: rec.ID, PublicID: rec.PublicID, Name: rec.Name, Scopes: append([]string{}, rec.Scopes...),
		LastUsedAt: rec.LastUsedAt, ExpiresAt: rec.ExpiresAt, RevokedAt: rec.RevokedAt, CreatedAt: rec.CreatedAt,
		Prefix: TokenPrefix + rec.PublicID[:min(8, len(rec.PublicID))],
	}
}

func generateToken() (publicID, secret, plaintext string, err error) {
	pub := make([]byte, 12)
	sec := make([]byte, 24)
	if _, err = rand.Read(pub); err != nil {
		return "", "", "", err
	}
	if _, err = rand.Read(sec); err != nil {
		return "", "", "", err
	}
	publicID = base64.RawURLEncoding.EncodeToString(pub)
	secret = base64.RawURLEncoding.EncodeToString(sec)
	// 用 '.' 分隔：base64url 字符集含 '_'，不能用下划线切分。
	plaintext = TokenPrefix + publicID + "." + secret
	return publicID, secret, plaintext, nil
}

func parseToken(plaintext string) (publicID, secret string, ok bool) {
	plaintext = strings.TrimSpace(plaintext)
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(plaintext, TokenPrefix)
	idx := strings.IndexByte(rest, '.')
	if idx <= 0 || idx >= len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
