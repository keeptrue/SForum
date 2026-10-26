package secretstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
)

var (
	namespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)
	secretIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// Service is the Host Secret Store runtime.
type Service struct {
	mu     sync.Mutex
	store  Store
	audit  AuditStore
	cipher *cryptox.OptionCipher

	puts, rotates, clears, resolves, denies, errors atomic.Uint64
	auditRing                                       []AuditEvent
	auditSeq                                        atomic.Uint64
	leaseSeq                                        atomic.Uint64
}

// Options configure Secret Store construction.
type Options struct {
	// Store is required.
	Store Store
	// Cipher encrypts values at rest. Required when RequireEncryption is true.
	Cipher *cryptox.OptionCipher
	// Audit is optional durable audit (Postgres). Process ring always kept.
	Audit AuditStore
	// RequireEncryption fails closed when Cipher is missing or transparent.
	// Production bootstrap must set this true.
	RequireEncryption bool
	// AllowTransparent explicitly permits transparent (dev/test) cipher mode.
	// Ignored when RequireEncryption is true.
	AllowTransparent bool
}

// New builds a Host Secret Store. Prefer NewWithOptions for production.
// Nil cipher with a non-nil store is transparent mode (tests/dev only).
func New(store Store, cipher *cryptox.OptionCipher) (*Service, error) {
	return NewWithOptions(Options{Store: store, Cipher: cipher, AllowTransparent: true})
}

// NewWithOptions builds a Secret Store with explicit production policy.
func NewWithOptions(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, ErrInvalid
	}
	cipher := opts.Cipher
	if cipher == nil {
		if opts.RequireEncryption {
			return nil, fmt.Errorf("%w: encryption key is required in production", ErrCipherRequired)
		}
		if !opts.AllowTransparent {
			return nil, fmt.Errorf("%w: transparent cipher not allowed", ErrCipherRequired)
		}
		var err error
		cipher, err = cryptox.NewOptionCipher("")
		if err != nil {
			return nil, err
		}
	}
	if opts.RequireEncryption && !cipher.Enabled() {
		return nil, fmt.Errorf("%w: encryption key is required in production", ErrCipherRequired)
	}
	if !cipher.Enabled() && !opts.AllowTransparent && !opts.RequireEncryption {
		return nil, fmt.Errorf("%w: transparent cipher requires AllowTransparent", ErrCipherRequired)
	}
	return &Service{store: opts.Store, audit: opts.Audit, cipher: cipher}, nil
}

// ShouldPreserve reports whether a submitted admin value must keep the prior secret.
func ShouldPreserve(submitted string) bool {
	return strings.TrimSpace(submitted) == ""
}

// FormatReference builds the opaque settings reference for a secret.
func FormatReference(namespace, secretID string) string {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	secretID = strings.ToLower(strings.TrimSpace(secretID))
	return ReferenceScheme + namespace + "/" + secretID
}

// ParseReference parses sforum.secret://namespace/secretId.
func ParseReference(ref string) (Ref, error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, ReferenceScheme) {
		return Ref{}, ErrInvalid
	}
	body := strings.TrimPrefix(ref, ReferenceScheme)
	parts := strings.Split(body, "/")
	if len(parts) != 2 {
		return Ref{}, ErrInvalid
	}
	return normalizeRef(Ref{Namespace: parts[0], SecretID: parts[1]})
}

// Put writes a new version. Empty plaintext with PreserveEmpty is a no-op success.
func (s *Service) Put(ctx context.Context, ref Ref, plaintext []byte, opts PutOptions) (Meta, error) {
	if s == nil {
		return Meta{}, ErrInvalid
	}
	if ctx == nil {
		return Meta{}, ErrInvalid
	}
	ref, err := normalizeRef(ref)
	if err != nil {
		return Meta{}, err
	}
	opts.Actor = strings.TrimSpace(opts.Actor)
	if opts.Actor == "" {
		s.denies.Add(1)
		s.pushAudit(ctx, AuditEvent{Action: "put", Namespace: ref.Namespace, SecretID: ref.SecretID, OK: false})
		return Meta{}, ErrPermissionDenied
	}
	if opts.PreserveEmpty && len(bytesTrimSpace(plaintext)) == 0 {
		// Preserve prior secret — return current meta if any.
		if meta, ok, metaErr := s.metaLatest(ctx, ref); metaErr != nil {
			return Meta{}, metaErr
		} else if ok {
			return meta, nil
		}
		return Meta{
			SchemaVersion: SchemaVersion, Namespace: ref.Namespace, SecretID: ref.SecretID,
			SecretSet: false, Reference: FormatReference(ref.Namespace, ref.SecretID),
		}, nil
	}
	if len(plaintext) == 0 || len(plaintext) > MaxPlaintextBytes {
		s.errors.Add(1)
		return Meta{}, ErrInvalid
	}
	purposes, err := normalizePurposes(opts.Purposes)
	if err != nil {
		return Meta{}, err
	}
	mediaType := strings.TrimSpace(opts.MediaType)
	if mediaType == "" {
		mediaType = "text/plain"
	}
	// 生产 cipher 必须写出 enc:: 密文；透明模式仅显式开发/测试。
	cipherText, err := s.cipher.Encrypt(string(plaintext))
	if err != nil {
		s.errors.Add(1)
		return Meta{}, fmt.Errorf("%w: %v", ErrCipher, err)
	}
	if s.cipher.Enabled() && !cryptox.IsEncrypted(cipherText) {
		s.errors.Add(1)
		return Meta{}, fmt.Errorf("%w: encrypt did not produce ciphertext", ErrCipher)
	}
	now := time.Now().UTC()
	row, err := s.store.Append(ctx, Row{
		Namespace: ref.Namespace, SecretID: ref.SecretID,
		Cipher: cipherText, MediaType: mediaType, Purposes: purposes,
		UpdatedAt: now, UpdatedBy: opts.Actor,
	})
	if err != nil {
		s.errors.Add(1)
		return Meta{}, err
	}
	s.puts.Add(1)
	s.pushAudit(ctx, AuditEvent{
		Action: "put", Namespace: ref.Namespace, SecretID: ref.SecretID,
		Version: row.Version, Actor: opts.Actor, OK: true, At: now,
	})
	return rowToMeta(row), nil
}

// Rotate appends a new version (same as Put without preserve). Alias for clarity.
func (s *Service) Rotate(ctx context.Context, ref Ref, plaintext []byte, actor string, purposes []string) (Meta, error) {
	meta, err := s.Put(ctx, ref, plaintext, PutOptions{Actor: actor, Purposes: purposes})
	if err == nil {
		s.rotates.Add(1)
		s.pushAudit(ctx, AuditEvent{
			Action: "rotate", Namespace: meta.Namespace, SecretID: meta.SecretID,
			Version: meta.Version, Actor: strings.TrimSpace(actor), OK: true, At: time.Now().UTC(),
		})
	}
	return meta, err
}

// Clear marks the latest version revoked (append revoke tombstone).
func (s *Service) Clear(ctx context.Context, ref Ref, actor string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrInvalid
	}
	if ctx == nil {
		return Meta{}, ErrInvalid
	}
	ref, err := normalizeRef(ref)
	if err != nil {
		return Meta{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		s.denies.Add(1)
		return Meta{}, ErrPermissionDenied
	}
	latest, ok, err := s.store.Latest(ctx, ref.Namespace, ref.SecretID, true)
	if err != nil {
		s.errors.Add(1)
		return Meta{}, err
	}
	if !ok {
		return Meta{}, ErrNotFound
	}
	if latest.Revoked {
		return rowToMeta(latest), nil
	}
	now := time.Now().UTC()
	tomb, err := s.store.Append(ctx, Row{
		Namespace: ref.Namespace, SecretID: ref.SecretID,
		Cipher: "", MediaType: latest.MediaType, Purposes: latest.Purposes,
		UpdatedAt: now, UpdatedBy: actor, Revoked: true,
	})
	if err != nil {
		s.errors.Add(1)
		return Meta{}, err
	}
	s.clears.Add(1)
	s.pushAudit(ctx, AuditEvent{
		Action: "clear", Namespace: ref.Namespace, SecretID: ref.SecretID,
		Version: tomb.Version, Actor: actor, OK: true, At: now,
	})
	return rowToMeta(tomb), nil
}

// Meta returns operator metadata without decrypting.
func (s *Service) Meta(ctx context.Context, ref Ref) (Meta, error) {
	if s == nil {
		return Meta{}, ErrInvalid
	}
	if ctx == nil {
		return Meta{}, ErrInvalid
	}
	ref, err := normalizeRef(ref)
	if err != nil {
		return Meta{}, err
	}
	if ref.Version > 0 {
		row, ok, err := s.store.GetVersion(ctx, ref.Namespace, ref.SecretID, ref.Version)
		if err != nil {
			return Meta{}, err
		}
		if !ok {
			return Meta{}, ErrNotFound
		}
		return rowToMeta(row), nil
	}
	meta, ok, err := s.metaLatest(ctx, ref)
	if err != nil {
		return Meta{}, err
	}
	if !ok {
		return Meta{}, ErrNotFound
	}
	return meta, nil
}

// ListMeta lists latest non-revoked secrets in a namespace.
func (s *Service) ListMeta(ctx context.Context, namespace string) ([]Meta, error) {
	if s == nil {
		return nil, ErrInvalid
	}
	if ctx == nil {
		return nil, ErrInvalid
	}
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToMeta(row))
	}
	return out, nil
}

// Resolve decrypts a secret for an admitted caller and purpose.
// Callers may only resolve secrets in their own extension namespace, or Host
// callers (empty ExtensionID) may resolve any namespace.
func (s *Service) Resolve(ctx context.Context, caller Caller, ref Ref, purpose string, ttl time.Duration) (Lease, error) {
	if s == nil {
		return Lease{}, ErrInvalid
	}
	if ctx == nil {
		return Lease{}, ErrInvalid
	}
	ref, err := normalizeRef(ref)
	if err != nil {
		s.errors.Add(1)
		return Lease{}, err
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "" || len(purpose) > MaxPurposeLen {
		s.errors.Add(1)
		return Lease{}, ErrInvalid
	}
	if !admitNamespace(caller, ref.Namespace) {
		s.denies.Add(1)
		s.pushAudit(ctx, AuditEvent{
			Action: "resolve", Namespace: ref.Namespace, SecretID: ref.SecretID,
			Actor: strings.TrimSpace(caller.Actor), Purpose: purpose, OK: false, At: time.Now().UTC(),
		})
		return Lease{}, ErrNamespaceDenied
	}
	if ttl <= 0 {
		ttl = DefaultResolveTTL
	}
	if ttl < MinResolveTTL {
		ttl = MinResolveTTL
	}
	if ttl > MaxResolveTTL {
		ttl = MaxResolveTTL
	}

	var row Row
	var ok bool
	if ref.Version > 0 {
		row, ok, err = s.store.GetVersion(ctx, ref.Namespace, ref.SecretID, ref.Version)
	} else {
		// Tip version is authoritative: a revoke tombstone hides prior versions
		// from unscoped Resolve while still allowing version-pinned reads.
		row, ok, err = s.store.Latest(ctx, ref.Namespace, ref.SecretID, true)
	}
	if err != nil {
		s.errors.Add(1)
		return Lease{}, err
	}
	if !ok {
		s.errors.Add(1)
		return Lease{}, ErrNotFound
	}
	if row.Revoked {
		s.denies.Add(1)
		return Lease{}, ErrRevoked
	}
	if len(row.Purposes) > 0 && !purposeAllowed(row.Purposes, purpose) {
		s.denies.Add(1)
		s.pushAudit(ctx, AuditEvent{
			Action: "resolve", Namespace: ref.Namespace, SecretID: ref.SecretID,
			Version: row.Version, Actor: strings.TrimSpace(caller.Actor), Purpose: purpose, OK: false,
			At: time.Now().UTC(),
		})
		return Lease{}, ErrPurposeDenied
	}
	plain, err := s.cipher.Decrypt(row.Cipher)
	if err != nil {
		s.errors.Add(1)
		return Lease{}, fmt.Errorf("%w: %v", ErrCipher, err)
	}
	now := time.Now().UTC()
	lease := Lease{
		LeaseID:   fmt.Sprintf("sec-lease-%d-%s", s.leaseSeq.Add(1), randomHex(4)),
		Namespace: row.Namespace, SecretID: row.SecretID, Version: row.Version,
		Value: []byte(plain), MediaType: row.MediaType, ExpiresAt: now.Add(ttl), Purpose: purpose,
	}
	s.resolves.Add(1)
	s.pushAudit(ctx, AuditEvent{
		Action: "resolve", Namespace: row.Namespace, SecretID: row.SecretID,
		Version: row.Version, Actor: strings.TrimSpace(caller.Actor), Purpose: purpose, OK: true, At: now,
	})
	return lease, nil
}

// Inspector returns metrics and recent audit events (never secret values).
func (s *Service) Inspector() InspectorSnapshot {
	if s == nil {
		return InspectorSnapshot{SchemaVersion: SchemaVersion}
	}
	s.mu.Lock()
	audit := append([]AuditEvent(nil), s.auditRing...)
	s.mu.Unlock()
	// Prefer durable audit when available for cross-process visibility.
	if s.audit != nil {
		if durable, err := s.audit.ListRecentAudit(context.Background(), MaxAuditRing); err == nil && len(durable) > 0 {
			audit = durable
		}
	}
	return InspectorSnapshot{
		SchemaVersion: SchemaVersion,
		Puts:          s.puts.Load(),
		Rotates:       s.rotates.Load(),
		Clears:        s.clears.Load(),
		Resolves:      s.resolves.Load(),
		Denies:        s.denies.Load(),
		Errors:        s.errors.Load(),
		RecentAudit:   audit,
	}
}

// EncryptionEnabled reports whether values are encrypted at rest.
func (s *Service) EncryptionEnabled() bool {
	return s != nil && s.cipher != nil && s.cipher.Enabled()
}

func (s *Service) metaLatest(ctx context.Context, ref Ref) (Meta, bool, error) {
	row, ok, err := s.store.Latest(ctx, ref.Namespace, ref.SecretID, true)
	if err != nil {
		return Meta{}, false, err
	}
	if !ok {
		return Meta{}, false, nil
	}
	return rowToMeta(row), true, nil
}

func (s *Service) pushAudit(ctx context.Context, event AuditEvent) {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if event.AuditID == "" {
		event.AuditID = fmt.Sprintf("sec-audit-%d", s.auditSeq.Add(1))
	}
	// 审计字段不得包含明文密钥；Action 元数据仅有 namespace/id/version。
	s.mu.Lock()
	s.auditRing = append(s.auditRing, event)
	if len(s.auditRing) > MaxAuditRing {
		s.auditRing = append([]AuditEvent(nil), s.auditRing[len(s.auditRing)-MaxAuditRing:]...)
	}
	s.mu.Unlock()
	if s.audit != nil && ctx != nil {
		_ = s.audit.AppendAudit(ctx, event)
	}
}

func admitNamespace(caller Caller, namespace string) bool {
	ext := strings.ToLower(strings.TrimSpace(caller.ExtensionID))
	if ext == "" {
		// Host / system caller.
		return true
	}
	return ext == namespace
}

func purposeAllowed(allowlist []string, purpose string) bool {
	purpose = strings.ToLower(strings.TrimSpace(purpose))
	for _, allowed := range allowlist {
		if strings.ToLower(allowed) == purpose {
			return true
		}
	}
	return false
}

func normalizeRef(ref Ref) (Ref, error) {
	ns, err := normalizeNamespace(ref.Namespace)
	if err != nil {
		return Ref{}, err
	}
	id := strings.ToLower(strings.TrimSpace(ref.SecretID))
	if id == "" || len(id) > MaxSecretIDLen || !secretIDPattern.MatchString(id) {
		return Ref{}, ErrInvalid
	}
	if ref.Version < 0 {
		return Ref{}, ErrInvalid
	}
	return Ref{Namespace: ns, SecretID: id, Version: ref.Version}, nil
}

func normalizeNamespace(namespace string) (string, error) {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	if namespace == "" || len(namespace) > MaxNamespaceLen || !namespacePattern.MatchString(namespace) {
		return "", ErrInvalid
	}
	return namespace, nil
}

func normalizePurposes(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, raw := range input {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" || len(value) > MaxPurposeLen {
			return nil, ErrInvalid
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func rowToMeta(row Row) Meta {
	return Meta{
		SchemaVersion: SchemaVersion,
		Namespace:     row.Namespace,
		SecretID:      row.SecretID,
		Version:       row.Version,
		SecretSet:     !row.Revoked && row.Cipher != "",
		MediaType:     row.MediaType,
		Purposes:      append([]string(nil), row.Purposes...),
		UpdatedAt:     row.UpdatedAt,
		UpdatedBy:     row.UpdatedBy,
		Revoked:       row.Revoked,
		Reference:     FormatReference(row.Namespace, row.SecretID),
	}
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "0"
	}
	return hex.EncodeToString(buf)
}
