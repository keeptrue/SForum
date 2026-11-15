// Package idempotency 为选定写路由提供 Idempotency-Key 去重（F3.2）。
//
// 存储键：actor + method + route + key；TTL 内重复请求回放首次成功响应。
// 失败响应不缓存，允许客户端用同一 key 重试。
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// HeaderName 客户端提交的幂等键头。
	HeaderName = "Idempotency-Key"
	// ReplayedHeader 回放响应时由服务端设置，便于调试与测试。
	ReplayedHeader = "Idempotency-Replayed"

	// DefaultTTL 推荐保留 24h，覆盖常见客户端重试窗口。
	DefaultTTL = 24 * time.Hour
	// DefaultPendingTTL 是 in-flight 租约：覆盖一次请求的正常处理时间，
	// 而不是客户端重试窗口。两者混用会让崩溃的 Begin 把 key 锁 24h。
	DefaultPendingTTL = 90 * time.Second
	// MaxKeyLength 限制 key 长度，避免超大 Redis key。
	MaxKeyLength = 128

	statePending   = "pending"
	stateCompleted = "completed"
)

// Record 是缓存中的完整响应快照。
type Record struct {
	State  string `json:"state"`
	Status int    `json:"status,omitempty"`
	Body   []byte `json:"body,omitempty"`
}

// Backend 最小 KV 能力；生产用 Redis，测试用 Memory。
type Backend interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	// SetNX 仅当 key 不存在时写入，返回 true 表示抢到锁。
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	// CompareAndSwap 只允许当前 lease 完成或释放自己的 pending 记录。
	// replacement 为 nil 时删除；否则以 ttl 写入新值。
	CompareAndSwap(ctx context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error)
}

// Store 在 Backend 之上编码业务记录。
type Store struct {
	backend    Backend
	ttl        time.Duration
	pendingTTL time.Duration
	prefix     string
	// replayCipher 可选：启用后回放响应必须能解密。
	replayCipher *RequiredReplayCipher
}

func (s *Store) WithRequiredReplayCipher(cipher *RequiredReplayCipher) *Store {
	if s != nil {
		s.replayCipher = cipher
	}
	return s
}

func (s *Store) RequiredReplayCipherEnabled() bool {
	return s != nil && s.replayCipher.Enabled()
}

func NewStore(backend Backend, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{backend: backend, ttl: ttl, pendingTTL: DefaultPendingTTL, prefix: "idempotency:v1:"}
}

// StorageKey 规范化 actor+route+key。
func StorageKey(actorID int64, method, route, key string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	route = strings.TrimSpace(route)
	key = strings.TrimSpace(key)
	// 对 key 做短哈希，避免原始 key 含特殊字符污染 Redis key。
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%d:%s:%s:%s", actorID, method, route, hex.EncodeToString(sum[:16]))
}

func (s *Store) fullKey(storageKey string) string {
	return s.prefix + storageKey
}

func (s *Store) Get(ctx context.Context, storageKey string) (Record, bool, error) {
	if s == nil || s.backend == nil {
		return Record{}, false, nil
	}
	raw, found, err := s.backend.Get(ctx, s.fullKey(storageKey))
	if err != nil || !found {
		return Record{}, found, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

// Begin 尝试登记 in-flight。若已有 completed 返回其记录；若 pending 返回 conflict。
// started=true 表示本请求获得执行权。
func (s *Store) Begin(ctx context.Context, storageKey string) (rec Record, started bool, conflict bool, err error) {
	if s == nil || s.backend == nil {
		return Record{}, true, false, nil
	}
	existing, found, err := s.Get(ctx, storageKey)
	if err != nil {
		return Record{}, false, false, err
	}
	if found {
		if existing.State == stateCompleted {
			return existing, false, false, nil
		}
		if existing.State == statePending {
			return Record{}, false, true, nil
		}
	}
	pending, _ := json.Marshal(Record{State: statePending})
	// pending 用独立短租约：完成后的响应快照才用 s.ttl 保留 24h。
	ok, err := s.backend.SetNX(ctx, s.fullKey(storageKey), pending, s.pendingTTL)
	if err != nil {
		return Record{}, false, false, err
	}
	if ok {
		return Record{State: statePending}, true, false, nil
	}
	// 竞态：再次读取
	existing, found, err = s.Get(ctx, storageKey)
	if err != nil {
		return Record{}, false, false, err
	}
	if found && existing.State == stateCompleted {
		return existing, false, false, nil
	}
	return Record{}, false, true, nil
}

// Complete 写入成功响应快照。
func (s *Store) Complete(ctx context.Context, storageKey string, status int, body []byte) error {
	if s == nil || s.backend == nil {
		return nil
	}
	raw, err := json.Marshal(Record{State: stateCompleted, Status: status, Body: body})
	if err != nil {
		return err
	}
	return s.backend.Set(ctx, s.fullKey(storageKey), raw, s.ttl)
}

// Abort 释放 pending，允许同一 key 重试（用于非 2xx 或处理失败）。
func (s *Store) Abort(ctx context.Context, storageKey string) error {
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.Delete(ctx, s.fullKey(storageKey))
}

// MemoryBackend 进程内实现，供单测使用。
type MemoryBackend struct {
	mu    sync.Mutex
	items map[string]memoryEntry
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{items: map[string]memoryEntry{}}
}

func (b *MemoryBackend) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.items[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(b.items, key)
		return nil, false, nil
	}
	out := make([]byte, len(entry.value))
	copy(out, entry.value)
	return out, true, nil
}

func (b *MemoryBackend) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.items[key]; ok && time.Now().Before(entry.expiresAt) {
		return false, nil
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	b.items[key] = memoryEntry{value: stored, expiresAt: time.Now().Add(ttl)}
	return true, nil
}

func (b *MemoryBackend) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	stored := make([]byte, len(value))
	copy(stored, value)
	b.items[key] = memoryEntry{value: stored, expiresAt: time.Now().Add(ttl)}
	return nil
}

func (b *MemoryBackend) Delete(_ context.Context, keys ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, key := range keys {
		delete(b.items, key)
	}
	return nil
}

func (b *MemoryBackend) CompareAndSwap(_ context.Context, key string, expected, replacement []byte, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.items[key]
	if !ok || time.Now().After(entry.expiresAt) || !bytes.Equal(entry.value, expected) {
		if ok && time.Now().After(entry.expiresAt) {
			delete(b.items, key)
		}
		return false, nil
	}
	if replacement == nil {
		delete(b.items, key)
		return true, nil
	}
	stored := append([]byte(nil), replacement...)
	b.items[key] = memoryEntry{value: stored, expiresAt: time.Now().Add(ttl)}
	return true, nil
}
