package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// 外部认证回调状态与一次性身份续接票据。
//
// 安全约束（见 plans/2026-07-27-github-social-login-builtin-plugin.md）：
//   - OAuth 回调使用保留的 Core 路由，独立于 Route Registry；
//   - Core 创建高熵 state、correlation id、PKCE 材料；
//   - 共享 Redis 存储有限时回调事务，10 分钟 TTL，原子一次性消费；
//   - Redis/内存 key 使用不透明浏览器 token 的 SHA-256 hash，不落 raw material；
//   - state 绑定 provider、operation、linking actor/session evidence、
//     精确 artifact、安全本地返回路径、PKCE 材料、创建时间；
//   - 缺失、过期、重放、跨 provider、跨 operation、跨 actor、artifact 不匹配的回调一律 fail closed；
//   - 浏览器永远不接收 raw subject / digest；完成的注册断言只存放在 Host 生成的
//     不透明一次性 Redis 票据后面。

const (
	// CallbackStateDefaultTTL 回调事务默认 TTL（10 分钟）。
	CallbackStateDefaultTTL = 10 * time.Minute
	// RegistrationTicketDefaultTTL 身份续接票据默认 TTL（10 分钟）。
	RegistrationTicketDefaultTTL = 10 * time.Minute

	callbackStateKeyPrefix      = "sforum:auth:callback:"
	callbackStateUsedKeyPrefix  = "sforum:auth:callback:used:"
	registrationTicketKeyPrefix = "sforum:auth:reg-ticket:"
	registrationTicketUsedPref  = "sforum:auth:reg-ticket:used:"

	// callbackStateBytes state 随机字节；浏览器可见 state 是其 hex。
	callbackStateBytes = 32
	// callbackTokenBytes continueToken/票据 token 随机字节，浏览器可见为 base64url。
	callbackTokenBytes = 32
)

// ExternalAuthOperation 是受 Host 管控的 operation 名（与 AuthOperation* 一致）。
type ExternalAuthOperation string

const (
	ExternalAuthOperationLogin        ExternalAuthOperation = "login"
	ExternalAuthOperationRegistration ExternalAuthOperation = "registration"
	ExternalAuthOperationLink         ExternalAuthOperation = "link"
)

// ParseExternalAuthOperation 解析字符串；非法值返回错误。
func ParseExternalAuthOperation(value string) (ExternalAuthOperation, error) {
	switch ExternalAuthOperation(value) {
	case ExternalAuthOperationLogin, ExternalAuthOperationRegistration, ExternalAuthOperationLink:
		return ExternalAuthOperation(value), nil
	default:
		return "", fmt.Errorf("unknown external auth operation: %s", value)
	}
}

// CallbackTransaction 绑定 provider/operation/actor/artifact/return path/PKCE 的一次性事务。
// raw subject/digest/PKCE verifier 永不出现在浏览器或日志；本结构只在 Host 进程内传递。
type CallbackTransaction struct {
	State                   string                `json:"state"`
	ProviderID              string                `json:"providerId"`
	ProviderContractVersion string                `json:"providerContractVersion"`
	OwnerExtensionID        string                `json:"ownerExtensionId"`
	OwnerExtensionVersion   string                `json:"ownerExtensionVersion"`
	OwnerPackageDigest      string                `json:"ownerPackageDigest"`
	Operation               ExternalAuthOperation `json:"operation"`
	CorrelationID           string                `json:"correlationId"`
	ActorUserID             int64                 `json:"actorUserId"`
	ClientClass             string                `json:"clientClass"`
	DeviceFingerprint       string                `json:"deviceFingerprint"`
	// BrowserBindingDigest 绑定 Host 设置的 HttpOnly 浏览器随机值，防止
	// callback/continuation bearer token 被复制到另一浏览器后触发账号绑定 CSRF。
	BrowserBindingDigest string `json:"browserBindingDigest"`
	RedirectPath         string `json:"redirectPath"`
	// AbsoluteCallbackURL 是 start 时由可信站点基址生成的绝对 callback URL；
	// complete 必须传入同一值，不可从请求 Host 重建。
	AbsoluteCallbackURL string    `json:"absoluteCallbackUrl"`
	CodeChallenge       string    `json:"codeChallenge"`
	CodeVerifier        string    `json:"codeVerifier"`
	CreatedAt           time.Time `json:"createdAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	// CompletionToken 由插件 start 生成；complete 时由 Host 带回插件。
	CompletionToken string `json:"completionToken,omitempty"`
}

// IsExpired 是否已过期。
func (t CallbackTransaction) IsExpired(now time.Time) bool {
	return t.ExpiresAt.IsZero() || now.After(t.ExpiresAt)
}

// normalizeTimestamps 填充/夹紧 CreatedAt 与 ExpiresAt，与 Redis TTL 语义一致。
func (t *CallbackTransaction) normalizeTimestamps(now time.Time, ttl time.Duration) {
	if ttl <= 0 {
		ttl = CallbackStateDefaultTTL
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	maxExpiry := now.Add(ttl)
	if t.ExpiresAt.IsZero() || t.ExpiresAt.After(maxExpiry) {
		t.ExpiresAt = maxExpiry
	}
}

// MatchesProvider 校验 provider/operation/owner digest 绑定（严格：digest 必须一致）。
func (t CallbackTransaction) MatchesProvider(providerID string, op ExternalAuthOperation, ownerDigest string) bool {
	return t.ProviderID == providerID &&
		t.Operation == op &&
		t.OwnerPackageDigest != "" &&
		t.OwnerPackageDigest == ownerDigest
}

// MatchesLiveArtifact 将事务与当前 live Registry 贡献比对：
// provider id、operation、owner extension/version、package digest 必须全部一致。
// 任一为空或漂移均 fail closed（artifact 变化 / 禁用 / 卸载）。
func (t CallbackTransaction) MatchesLiveArtifact(
	provider identityregistry.ProviderContribution,
	op ExternalAuthOperation,
) bool {
	if t.ProviderID == "" || t.OwnerPackageDigest == "" || t.OwnerExtensionID == "" {
		return false
	}
	if t.ProviderID != provider.ID || t.Operation != op {
		return false
	}
	if t.OwnerExtensionID != provider.Artifact.ExtensionID {
		return false
	}
	if t.OwnerExtensionVersion != "" && t.OwnerExtensionVersion != provider.Artifact.ExtensionVersion {
		return false
	}
	if t.OwnerPackageDigest != provider.Artifact.PackageDigest {
		return false
	}
	if t.ProviderContractVersion != "" && t.ProviderContractVersion != provider.ContractVersion {
		return false
	}
	return true
}

// MatchesActor 校验 actor 绑定；link 操作必须匹配，login/registration 必须为 0。
func (t CallbackTransaction) MatchesActor(op ExternalAuthOperation, actorUserID int64) bool {
	switch op {
	case ExternalAuthOperationLink:
		return t.ActorUserID != 0 && t.ActorUserID == actorUserID
	default:
		return t.ActorUserID == 0 && actorUserID == 0
	}
}

// CallbackStateStore 是回调事务的存储抽象。生产为 Redis，测试可为内存。
type CallbackStateStore interface {
	// Save 存入事务，TTL 由实现控制（或由 SaveTx 指定）。
	Save(ctx context.Context, tx CallbackTransaction) error
	// Consume 原子取出并删除；state 不存在/已消费/已过期返回对应稳定错误。
	// 一次性消费保证：再次 Consume 同一 state 必失败。
	Consume(ctx context.Context, state string) (CallbackTransaction, error)
	// Peek 不消费地读取（仅用于诊断/测试）。
	Peek(ctx context.Context, state string) (CallbackTransaction, error)
}

// RegistrationTicket 是完成 external identity 断言后的不透明续接票据。
// 浏览器只看到 ticket token；票据内才保存 providerSubject/displays 与 artifact 绑定。
// Owner* / ProviderContractVersion 在最终注册或已有账号绑定时与 live Registry 精确比对。
type RegistrationTicket struct {
	Token                   string                `json:"token"`
	ProviderID              string                `json:"providerId"`
	ProviderContractVersion string                `json:"providerContractVersion"`
	OwnerExtensionID        string                `json:"ownerExtensionId"`
	OwnerExtensionVersion   string                `json:"ownerExtensionVersion,omitempty"`
	OwnerPackageDigest      string                `json:"ownerPackageDigest"`
	Operation               ExternalAuthOperation `json:"operation"`
	SourceOperation         ExternalAuthOperation `json:"sourceOperation"`
	BrowserBindingDigest    string                `json:"browserBindingDigest"`
	ProviderSubject         string                `json:"providerSubject"` // raw subject，Core 校验后立即消费
	SubjectDigest           string                `json:"subjectDigest"`   // 兼容旧 fixture 路径；raw subject 为空时使用
	UsernameHint            string                `json:"usernameHint"`
	DisplayName             string                `json:"displayName"`
	EmailHint               string                `json:"emailHint"`
	EmailVerified           bool                  `json:"emailVerified"`
	CorrelationID           string                `json:"correlationId"`
	CreatedAt               time.Time             `json:"createdAt"`
	ExpiresAt               time.Time             `json:"expiresAt"`
}

// IsExpired 是否已过期；ExpiresAt 为零视为无效/过期（fail closed）。
func (t RegistrationTicket) IsExpired(now time.Time) bool {
	return t.ExpiresAt.IsZero() || now.After(t.ExpiresAt)
}

// ValidateBinding 强制 source operation/provider/artifact/browser 与时间戳绑定。
func (t RegistrationTicket) ValidateBinding() error {
	if strings.TrimSpace(t.Token) == "" ||
		strings.TrimSpace(t.ProviderID) == "" ||
		strings.TrimSpace(t.OwnerExtensionID) == "" ||
		strings.TrimSpace(t.OwnerPackageDigest) == "" {
		return ErrRegistrationTicketInvalid
	}
	if t.Operation != ExternalAuthOperationLogin && t.Operation != ExternalAuthOperationRegistration {
		return ErrRegistrationTicketInvalid
	}
	if t.SourceOperation != t.Operation {
		return ErrRegistrationTicketInvalid
	}
	// Existing registration tickets from an in-flight rolling deploy remain
	// consumable for account creation. Login continuations can bind an existing
	// account and therefore always require the anti-login-CSRF browser binding.
	if t.Operation == ExternalAuthOperationLogin && !validExternalIdentityDigest(t.BrowserBindingDigest) {
		return ErrRegistrationTicketInvalid
	}
	if t.BrowserBindingDigest != "" && !validExternalIdentityDigest(t.BrowserBindingDigest) {
		return ErrRegistrationTicketInvalid
	}
	if t.CreatedAt.IsZero() || t.ExpiresAt.IsZero() {
		return ErrRegistrationTicketInvalid
	}
	if strings.TrimSpace(t.ProviderSubject) == "" && strings.TrimSpace(t.SubjectDigest) == "" {
		return ErrRegistrationTicketInvalid
	}
	return nil
}

// normalizeTimestamps 填充/夹紧 CreatedAt 与 ExpiresAt，与 Redis TTL 语义一致。
func (t *RegistrationTicket) normalizeTimestamps(now time.Time, ttl time.Duration) {
	if ttl <= 0 {
		ttl = RegistrationTicketDefaultTTL
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	maxExpiry := now.Add(ttl)
	if t.ExpiresAt.IsZero() || t.ExpiresAt.After(maxExpiry) {
		t.ExpiresAt = maxExpiry
	}
}

// RegistrationTicketStore 是注册票据的存储抽象。
type RegistrationTicketStore interface {
	Save(ctx context.Context, ticket RegistrationTicket) error
	// Inspect validates and returns an active ticket without consuming it.
	Inspect(ctx context.Context, token string) (RegistrationTicket, error)
	// Consume 原子取出并删除；重放必失败。
	Consume(ctx context.Context, token string) (RegistrationTicket, error)
}

var (
	// ErrCallbackStateInvalid 回调 state 缺失/已消费/已过期/绑定不匹配。
	ErrCallbackStateInvalid = errors.New("auth.provider_callback_invalid")
	// ErrCallbackStateExpired 回调 state 显式过期。
	ErrCallbackStateExpired = errors.New("auth.provider_callback_expired")
	// ErrCallbackStateReplayed state 已被消费过（重放）。
	ErrCallbackStateReplayed = errors.New("auth.provider_callback_replayed")
	// ErrRegistrationTicketInvalid 注册票据缺失/已消费/绑定不匹配。
	ErrRegistrationTicketInvalid = errors.New("auth.external_registration_ticket_invalid")
	// ErrRegistrationTicketExpired 注册票据过期。
	ErrRegistrationTicketExpired = errors.New("auth.external_registration_ticket_expired")
)

// GenerateCallbackState 生成高熵 state（hex）。
func GenerateCallbackState() (string, error) {
	return randomHex(callbackStateBytes)
}

// GenerateOpaqueToken 生成不透明 token（base64url，无 padding）。
func GenerateOpaqueToken() (string, error) {
	return randomURL(callbackTokenBytes)
}

// ExternalAuthBrowserBindingDigest derives the non-reversible value stored in
// callback and continuation records. The raw browser cookie never enters Redis
// payloads, audit events, URLs, or logs.
func ExternalAuthBrowserBindingDigest(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// GeneratePKCE 生成 PKCE code_verifier 与 code_challenge(S256)。
func GeneratePKCE() (verifier, challenge string, err error) {
	verifier, err = randomURL(32) // 43~128 chars per RFC 7636
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func randomURL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// opaqueTokenHash 对浏览器可见的不透明 token 做 SHA-256 hex，作为存储 key 后缀。
// raw token 永不作为 Redis/内存 map key 落盘，避免日志/转储泄露。
func opaqueTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ExternalAuthCallbackPath 返回 provider 对外登记的站点回调路径。
// Web Host 将该路径桥接到保留的 Core API 回调，第三方提供商无需感知 API 命名空间。
func ExternalAuthCallbackPath(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	return "/auth/providers/" + providerID + "/callback"
}

// ExternalAuthCallbackURL 返回 provider 对外登记的回调相对路径。
// 插件 complete 必须使用 AbsoluteExternalAuthCallbackURL 生成的绝对 URL。
func ExternalAuthCallbackURL(providerID string) string {
	return ExternalAuthCallbackPath(providerID)
}

// AbsoluteExternalAuthCallbackURL 从可信站点基址生成绝对 callback URL。
// 绝不使用请求 Host。productionRequireHTTPS 为 true 时要求 scheme=https。
// appURL 非法、缺 scheme/host、或生产非 HTTPS 时返回错误。
func AbsoluteExternalAuthCallbackURL(appURL, providerID string, productionRequireHTTPS bool) (string, error) {
	appURL = strings.TrimSpace(appURL)
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if appURL == "" || providerID == "" {
		return "", fmt.Errorf("%w: app url and provider id required", ErrExternalAuthCallbackURLInvalid)
	}
	parsed, err := url.Parse(appURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: app url must be absolute with scheme and host", ErrExternalAuthCallbackURLInvalid)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: app url scheme must be http or https", ErrExternalAuthCallbackURLInvalid)
	}
	if productionRequireHTTPS && scheme != "https" {
		return "", fmt.Errorf("%w: production callback requires https app url", ErrExternalAuthCallbackURLInvalid)
	}
	// 仅使用 scheme://host（忽略站点基址路径），避免把站点子路径拼进 callback。
	base := &url.URL{Scheme: scheme, Host: parsed.Host}
	return base.String() + ExternalAuthCallbackPath(providerID), nil
}

// ValidateSafeRedirectPath 校验本地绝对路径：禁止外部、协议相对、/api/ 与空路径。
// 与前端 auth return 导航规则对齐；通过后才可存入 callback 事务或传给插件。
func ValidateSafeRedirectPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > 2000 {
		return false
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	if strings.HasPrefix(path, "/api/") {
		return false
	}
	return true
}

// ExternalRegistrationContinuationPath 固定 Host 注册 continuation 路由。
// 浏览器只携带 opaque ticket；safeRedirect 作为独立 query，不得改写注册路由本身。
func ExternalRegistrationContinuationPath(ticket, safeRedirect string) string {
	u := &url.URL{Path: "/register"}
	q := url.Values{}
	q.Set("ticket", ticket)
	if ValidateSafeRedirectPath(safeRedirect) {
		q.Set("redirect", safeRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ExternalAuthContinuationPath is the Host-owned choice page for an unlinked
// login assertion. The opaque ticket remains one-use and the redirect stays a
// separately validated local path.
func ExternalAuthContinuationPath(ticket, safeRedirect string) string {
	u := &url.URL{Path: "/auth/continue"}
	q := url.Values{}
	q.Set("ticket", ticket)
	if ValidateSafeRedirectPath(safeRedirect) {
		q.Set("redirect", safeRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ErrExternalAuthCallbackURLInvalid 无法从可信站点基址形成绝对 callback URL。
var ErrExternalAuthCallbackURLInvalid = errors.New("auth.provider_callback_url_invalid")

// --- 内存实现（并发安全 + 与 Redis 相同的 TTL 语义） ---

// NewInMemoryCallbackStateStore 返回并发安全的内存回调 state 存储（默认 10 分钟 TTL）。
func NewInMemoryCallbackStateStore() *InMemoryCallbackStateStore {
	return NewInMemoryCallbackStateStoreWithTTL(CallbackStateDefaultTTL)
}

// NewInMemoryCallbackStateStoreWithTTL 返回带自定义 TTL 的内存回调 state 存储。
func NewInMemoryCallbackStateStoreWithTTL(ttl time.Duration) *InMemoryCallbackStateStore {
	if ttl <= 0 {
		ttl = CallbackStateDefaultTTL
	}
	return &InMemoryCallbackStateStore{
		entries:  map[string]inMemoryCallbackEntry{},
		consumed: map[string]time.Time{},
		ttl:      ttl,
	}
}

type inMemoryCallbackEntry struct {
	tx CallbackTransaction
}

type InMemoryCallbackStateStore struct {
	mu       sync.Mutex
	entries  map[string]inMemoryCallbackEntry
	consumed map[string]time.Time // hash → 消费/过期标记过期时间（对齐 Redis tombstone TTL）
	ttl      time.Duration
}

func (s *InMemoryCallbackStateStore) storageKey(state string) string {
	return opaqueTokenHash(state)
}

func (s *InMemoryCallbackStateStore) Save(_ context.Context, tx CallbackTransaction) error {
	if strings.TrimSpace(tx.State) == "" {
		return fmt.Errorf("callback state required")
	}
	now := time.Now()
	tx.normalizeTimestamps(now, s.ttl)

	key := s.storageKey(tx.State)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = inMemoryCallbackEntry{tx: tx}
	delete(s.consumed, key)
	return nil
}

func (s *InMemoryCallbackStateStore) Consume(_ context.Context, state string) (CallbackTransaction, error) {
	if strings.TrimSpace(state) == "" {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	key := s.storageKey(state)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredConsumedLocked(now)

	entry, ok := s.entries[key]
	if !ok {
		if exp, seen := s.consumed[key]; seen && now.Before(exp) {
			return CallbackTransaction{}, ErrCallbackStateReplayed
		}
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	delete(s.entries, key)
	// 墓碑保留至原 ExpiresAt（与 Redis 剩余 TTL 对齐）；至少保留一小段以便立刻重放可识别。
	tombstoneUntil := entry.tx.ExpiresAt
	if tombstoneUntil.Before(now.Add(time.Minute)) {
		tombstoneUntil = now.Add(s.ttl)
	}
	s.consumed[key] = tombstoneUntil

	if entry.tx.IsExpired(now) {
		return CallbackTransaction{}, ErrCallbackStateExpired
	}
	return entry.tx, nil
}

func (s *InMemoryCallbackStateStore) Peek(_ context.Context, state string) (CallbackTransaction, error) {
	if strings.TrimSpace(state) == "" {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	key := s.storageKey(state)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	return entry.tx, nil
}

func (s *InMemoryCallbackStateStore) purgeExpiredConsumedLocked(now time.Time) {
	for k, exp := range s.consumed {
		if !now.Before(exp) {
			delete(s.consumed, k)
		}
	}
}

// NewInMemoryRegistrationTicketStore 返回并发安全的内存注册票据存储。
func NewInMemoryRegistrationTicketStore() *InMemoryRegistrationTicketStore {
	return NewInMemoryRegistrationTicketStoreWithTTL(RegistrationTicketDefaultTTL)
}

// NewInMemoryRegistrationTicketStoreWithTTL 返回带自定义 TTL 的内存注册票据存储。
func NewInMemoryRegistrationTicketStoreWithTTL(ttl time.Duration) *InMemoryRegistrationTicketStore {
	if ttl <= 0 {
		ttl = RegistrationTicketDefaultTTL
	}
	return &InMemoryRegistrationTicketStore{
		entries:  map[string]RegistrationTicket{},
		consumed: map[string]time.Time{},
		ttl:      ttl,
	}
}

type InMemoryRegistrationTicketStore struct {
	mu       sync.Mutex
	entries  map[string]RegistrationTicket
	consumed map[string]time.Time
	ttl      time.Duration
}

func (s *InMemoryRegistrationTicketStore) storageKey(token string) string {
	return opaqueTokenHash(token)
}

func (s *InMemoryRegistrationTicketStore) Save(_ context.Context, ticket RegistrationTicket) error {
	ticket.normalizeTimestamps(time.Now(), s.ttl)
	if err := ticket.ValidateBinding(); err != nil {
		return err
	}
	key := s.storageKey(ticket.Token)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = ticket
	delete(s.consumed, key)
	return nil
}

func (s *InMemoryRegistrationTicketStore) Consume(_ context.Context, token string) (RegistrationTicket, error) {
	if strings.TrimSpace(token) == "" {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	key := s.storageKey(token)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.consumed {
		if !now.Before(exp) {
			delete(s.consumed, k)
		}
	}

	ticket, ok := s.entries[key]
	if !ok {
		// 公开契约：重放与缺失统一为 invalid（无独立 ticket_replayed reason）。
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	delete(s.entries, key)
	tombstoneUntil := ticket.ExpiresAt
	if tombstoneUntil.Before(now.Add(time.Minute)) {
		tombstoneUntil = now.Add(s.ttl)
	}
	s.consumed[key] = tombstoneUntil

	if ticket.IsExpired(now) {
		return RegistrationTicket{}, ErrRegistrationTicketExpired
	}
	if err := ticket.ValidateBinding(); err != nil {
		return RegistrationTicket{}, err
	}
	return ticket, nil
}

func (s *InMemoryRegistrationTicketStore) Inspect(_ context.Context, token string) (RegistrationTicket, error) {
	if strings.TrimSpace(token) == "" {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	key := s.storageKey(token)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.entries[key]
	if !ok {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	if ticket.IsExpired(now) {
		return RegistrationTicket{}, ErrRegistrationTicketExpired
	}
	if err := ticket.ValidateBinding(); err != nil {
		return RegistrationTicket{}, err
	}
	return ticket, nil
}

// --- Redis 实现 ---

// RedisCallbackStateStore 使用 Redis SET + lua 原子 GETDEL + 消费墓碑。
// key = prefix + sha256(state)；raw state 不出现在 Redis key 中。
type RedisCallbackStateStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisCallbackStateStore(client *redis.Client, ttl time.Duration) *RedisCallbackStateStore {
	if ttl <= 0 {
		ttl = CallbackStateDefaultTTL
	}
	return &RedisCallbackStateStore{client: client, ttl: ttl}
}

func (s *RedisCallbackStateStore) activeKey(state string) string {
	return callbackStateKeyPrefix + opaqueTokenHash(state)
}

func (s *RedisCallbackStateStore) usedKey(state string) string {
	return callbackStateUsedKeyPrefix + opaqueTokenHash(state)
}

func (s *RedisCallbackStateStore) Save(ctx context.Context, tx CallbackTransaction) error {
	if strings.TrimSpace(tx.State) == "" {
		return fmt.Errorf("callback state required")
	}
	tx.normalizeTimestamps(time.Now(), s.ttl)
	payload, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("encode callback tx: %w", err)
	}
	// 与 payload.ExpiresAt 对齐的 Redis TTL。
	ttl := time.Until(tx.ExpiresAt)
	if ttl <= 0 {
		ttl = s.ttl
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, s.activeKey(tx.State), payload, ttl)
	pipe.Del(ctx, s.usedKey(tx.State))
	_, err = pipe.Exec(ctx)
	return err
}

// 一次性消费：原子取出 active 并写 used 墓碑；区分 missing / replayed。
// KEYS[1]=active KEYS[2]=used ARGV[1]=tombstone TTL 秒
const callbackConsumeScript = `
local v = redis.call("GET", KEYS[1])
if not v then
  if redis.call("EXISTS", KEYS[2]) == 1 then
    return "REPLAYED"
  end
  return false
end
redis.call("DEL", KEYS[1])
local ttl = tonumber(ARGV[1])
if ttl == nil or ttl < 1 then
  ttl = 600
end
redis.call("SET", KEYS[2], "1", "EX", ttl)
return v
`

func (s *RedisCallbackStateStore) Consume(ctx context.Context, state string) (CallbackTransaction, error) {
	if s.client == nil || strings.TrimSpace(state) == "" {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	// 墓碑 TTL 与默认事务 TTL 对齐，保证窗口内重放可识别。
	tombstoneTTL := int(s.ttl.Seconds())
	if tombstoneTTL < 1 {
		tombstoneTTL = int(CallbackStateDefaultTTL.Seconds())
	}
	raw, err := s.client.Eval(ctx, callbackConsumeScript, []string{s.activeKey(state), s.usedKey(state)}, tombstoneTTL).Result()
	if err != nil {
		// go-redis 将 Lua false/nil 映射为 redis.Nil（缺失 → invalid）。
		if errors.Is(err, redis.Nil) {
			return CallbackTransaction{}, ErrCallbackStateInvalid
		}
		// 不记录 raw state；仅包装错误类型。
		return CallbackTransaction{}, fmt.Errorf("consume callback state: %w", err)
	}
	if raw == nil || raw == false {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	str, ok := raw.(string)
	if !ok {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	if str == "REPLAYED" {
		return CallbackTransaction{}, ErrCallbackStateReplayed
	}
	var tx CallbackTransaction
	if err := json.Unmarshal([]byte(str), &tx); err != nil {
		return CallbackTransaction{}, fmt.Errorf("decode callback tx: %w", err)
	}
	if tx.IsExpired(time.Now()) {
		return CallbackTransaction{}, ErrCallbackStateExpired
	}
	return tx, nil
}

func (s *RedisCallbackStateStore) Peek(ctx context.Context, state string) (CallbackTransaction, error) {
	if strings.TrimSpace(state) == "" {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	raw, err := s.client.Get(ctx, s.activeKey(state)).Result()
	if err != nil {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	var tx CallbackTransaction
	if err := json.Unmarshal([]byte(raw), &tx); err != nil {
		return CallbackTransaction{}, ErrCallbackStateInvalid
	}
	return tx, nil
}

// RedisRegistrationTicketStore 一次性注册票据存储（hash key + 原子消费）。
type RedisRegistrationTicketStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisRegistrationTicketStore(client *redis.Client, ttl time.Duration) *RedisRegistrationTicketStore {
	if ttl <= 0 {
		ttl = RegistrationTicketDefaultTTL
	}
	return &RedisRegistrationTicketStore{client: client, ttl: ttl}
}

func (s *RedisRegistrationTicketStore) activeKey(token string) string {
	return registrationTicketKeyPrefix + opaqueTokenHash(token)
}

func (s *RedisRegistrationTicketStore) usedKey(token string) string {
	return registrationTicketUsedPref + opaqueTokenHash(token)
}

func (s *RedisRegistrationTicketStore) Save(ctx context.Context, ticket RegistrationTicket) error {
	ticket.normalizeTimestamps(time.Now(), s.ttl)
	if err := ticket.ValidateBinding(); err != nil {
		return err
	}
	payload, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("encode registration ticket: %w", err)
	}
	ttl := time.Until(ticket.ExpiresAt)
	if ttl <= 0 {
		ttl = s.ttl
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, s.activeKey(ticket.Token), payload, ttl)
	pipe.Del(ctx, s.usedKey(ticket.Token))
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisRegistrationTicketStore) Consume(ctx context.Context, token string) (RegistrationTicket, error) {
	if s.client == nil || strings.TrimSpace(token) == "" {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	tombstoneTTL := int(s.ttl.Seconds())
	if tombstoneTTL < 1 {
		tombstoneTTL = int(RegistrationTicketDefaultTTL.Seconds())
	}
	raw, err := s.client.Eval(ctx, callbackConsumeScript, []string{s.activeKey(token), s.usedKey(token)}, tombstoneTTL).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return RegistrationTicket{}, ErrRegistrationTicketInvalid
		}
		return RegistrationTicket{}, fmt.Errorf("consume registration ticket: %w", err)
	}
	if raw == nil || raw == false {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	str, ok := raw.(string)
	if !ok {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	// 公开契约：重放映射为 invalid（无 ticket_replayed reason）。
	if str == "REPLAYED" {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	var ticket RegistrationTicket
	if err := json.Unmarshal([]byte(str), &ticket); err != nil {
		return RegistrationTicket{}, fmt.Errorf("decode registration ticket: %w", err)
	}
	if ticket.IsExpired(time.Now()) {
		return RegistrationTicket{}, ErrRegistrationTicketExpired
	}
	if err := ticket.ValidateBinding(); err != nil {
		return RegistrationTicket{}, err
	}
	return ticket, nil
}

func (s *RedisRegistrationTicketStore) Inspect(ctx context.Context, token string) (RegistrationTicket, error) {
	if s.client == nil || strings.TrimSpace(token) == "" {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	raw, err := s.client.Get(ctx, s.activeKey(token)).Result()
	if err != nil {
		return RegistrationTicket{}, ErrRegistrationTicketInvalid
	}
	var ticket RegistrationTicket
	if err := json.Unmarshal([]byte(raw), &ticket); err != nil {
		return RegistrationTicket{}, fmt.Errorf("decode registration ticket: %w", err)
	}
	if ticket.IsExpired(time.Now()) {
		return RegistrationTicket{}, ErrRegistrationTicketExpired
	}
	if err := ticket.ValidateBinding(); err != nil {
		return RegistrationTicket{}, err
	}
	return ticket, nil
}
