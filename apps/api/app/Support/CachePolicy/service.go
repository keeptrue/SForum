package cachepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

// Service is the Host cache policy runtime. It plans isolation keys from the
// Cache Registry, executes get/set/delete against a selected Cache backend,
// tracks inspector metrics, and audits tag invalidation.
//
// Plugins never select providers or write raw Redis commands through this API.
type Service struct {
	mu        sync.Mutex
	registry  *cacheregistry.Registry
	backend   cache.Cache
	provider  ProviderSelection
	themeRev  atomic.Value // string
	pluginRev atomic.Value // string

	hits, misses, stores, deletes, bypasses, errors, invalidations atomic.Uint64
	getLatencyNanos, setLatencyNanos, getCount, setCount           atomic.Uint64

	// generations 是 tag 失效的**权威**（生产为 Redis）：条目带写入时的 generation，
	// 读取时比对，失效 = 递增。跨进程可见，重启不回退。
	generations GenerationStore
	// tagIndex 是**纯本地加速索引**，不是权威状态：它只记录本进程写过的 key，
	// 用来在失效时顺手删掉这些 key（尽快回收空间）。即使它为空、或记录的是别的
	// 副本写的 key，正确性也由 generations 保证（P3-11）。
	tagIndex map[string]map[string]struct{}
	// auditRing keeps recent invalidation evidence for the inspector.
	auditRing []InvalidateResult
	auditSeq  atomic.Uint64
}

const maxAuditRing = 64

// New builds a Host cache policy service. backend may be nil (noop until SetBackend).
func New(registry *cacheregistry.Registry, backend cache.Cache, provider string) (*Service, error) {
	if registry == nil {
		return nil, ErrInvalid
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = ProviderMemory
	}
	if !validProvider(provider) {
		return nil, ErrInvalid
	}
	if backend == nil {
		backend = cache.NoopCache{}
		provider = ProviderNoop
	}
	service := &Service{
		registry: registry,
		backend:  backend,
		provider: ProviderSelection{Provider: provider, SelectedAt: time.Now().UTC()},
		// generation 存储跟随 backend：Redis backend 即跨进程，Memory backend 即
		// 单进程（与 ProviderMemory 语义一致）。
		generations: NewCacheGenerations(backend),
		tagIndex:    make(map[string]map[string]struct{}),
	}
	service.themeRev.Store("")
	service.pluginRev.Store("")
	return service, nil
}

func validProvider(value string) bool {
	switch value {
	case ProviderMemory, ProviderRedis, ProviderNoop:
		return true
	default:
		return false
	}
}

// SelectProvider switches the Host backend. Plugins cannot call this.
func (s *Service) SelectProvider(provider string, backend cache.Cache) error {
	if s == nil {
		return ErrInvalid
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !validProvider(provider) || backend == nil {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = backend
	s.provider = ProviderSelection{Provider: provider, SelectedAt: time.Now().UTC()}
	// 换 provider 必须换 generation 存储：否则切换后失效会写到旧后端的键空间，
	// 新后端的条目永远等不到失效。
	s.generations = NewCacheGenerations(backend)
	return nil
}

// WithGenerationStore overrides the tag generation authority. Production uses the
// Redis-backed default derived from the selected backend; tests inject failures.
func (s *Service) WithGenerationStore(store GenerationStore) *Service {
	if s == nil || store == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generations = store
	return s
}

// SetRevisions updates theme/plugin revision fingerprints used in key material.
func (s *Service) SetRevisions(themeRevision, pluginRevision string) {
	if s == nil {
		return
	}
	s.themeRev.Store(strings.TrimSpace(themeRevision))
	s.pluginRev.Store(strings.TrimSpace(pluginRevision))
}

// PlanKey resolves a Cache Registry contribution into Host key material.
func (s *Service) PlanKey(request KeyRequest) (KeyPlan, error) {
	if s == nil || s.registry == nil {
		return KeyPlan{}, ErrInvalid
	}
	request.CacheID = strings.ToLower(strings.TrimSpace(request.CacheID))
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Directive = strings.ToLower(strings.TrimSpace(request.Directive))
	if request.Directive == "" {
		request.Directive = DirectiveStore
	}
	if request.CacheID == "" || request.Namespace == "" {
		return KeyPlan{}, ErrInvalid
	}
	switch request.Directive {
	case DirectiveStore, DirectiveBypass, DirectiveNoStore:
	default:
		return KeyPlan{}, ErrInvalid
	}
	// CacheRegistry.Plan accepts exactly one of CacheID or Namespace (XOR).
	// Policy service prefers CacheID and fences the caller's namespace pin.
	regPlan, err := s.registry.Plan(cacheregistry.PlanRequest{
		CacheID:               request.CacheID,
		ActorFingerprint:      request.ActorFingerprint,
		PermissionFingerprint: request.PermissionFingerprint,
		LocaleFingerprint:     request.LocaleFingerprint,
	})
	if err != nil {
		return KeyPlan{}, err
	}
	if !strings.EqualFold(request.Namespace, regPlan.Isolation.Namespace) {
		return KeyPlan{}, ErrNotFound
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL {
		ttl = MinTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	theme := loadString(s.themeRev)
	plugin := loadString(s.pluginRev)
	if request.ThemeRevision != "" {
		theme = request.ThemeRevision
	}
	if request.PluginRevision != "" {
		plugin = request.PluginRevision
	}
	material := strings.Join([]string{
		SchemaVersion,
		regPlan.Isolation.SegmentDigest,
		request.RouteID,
		request.PageID,
		request.EntityEvent,
		theme,
		plugin,
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	key := regPlan.Isolation.Namespace + ":" + hex.EncodeToString(sum[:16])
	return KeyPlan{
		SchemaVersion: SchemaVersion,
		CacheID:       regPlan.Cache.ID,
		Namespace:     regPlan.Isolation.Namespace,
		Key:           key,
		Directive:     request.Directive,
		TTL:           ttl,
		Provider:      s.provider.Provider,
		Tags:          append([]string(nil), regPlan.Cache.Tags...),
	}, nil
}

// Get reads a planned key. Bypass/no-store always miss without backend access.
func (s *Service) Get(ctx context.Context, plan KeyPlan) ([]byte, bool, error) {
	if s == nil {
		return nil, false, ErrInvalid
	}
	if plan.Directive == DirectiveBypass {
		s.bypasses.Add(1)
		return nil, false, ErrBypass
	}
	if plan.Directive == DirectiveNoStore {
		s.bypasses.Add(1)
		return nil, false, ErrNoStore
	}
	if plan.Key == "" {
		return nil, false, ErrInvalid
	}
	start := time.Now()
	value, found, err := s.backend.Get(ctx, plan.Key)
	s.observeGet(time.Since(start))
	if err != nil {
		s.errors.Add(1)
		return nil, false, err
	}
	if !found {
		s.misses.Add(1)
		return nil, false, nil
	}
	entry, decodeErr := decodeEntry(value)
	if decodeErr != nil {
		// 旧格式裸值 / 损坏条目：无法判断新鲜度，一律当 miss 并清掉，
		// 绝不让一份无法失效的值继续被当成命中。
		_ = s.backend.Delete(ctx, plan.Key)
		s.misses.Add(1)
		return nil, false, nil
	}
	if len(entry.Gens) > 0 {
		stamped := make([]string, 0, len(entry.Gens))
		for tag := range entry.Gens {
			stamped = append(stamped, tag)
		}
		current, genErr := s.generations.Current(ctx, stamped)
		if genErr != nil {
			// 读不到 generation 就无法证明新鲜度：fail closed 成 miss（不服务
			// 不可校验的条目）。此时后端通常也已不可用，调用方本来就要回源。
			s.errors.Add(1)
			return nil, false, genErr
		}
		for tag, at := range entry.Gens {
			if current[tag] != at {
				_ = s.backend.Delete(ctx, plan.Key)
				s.misses.Add(1)
				return nil, false, nil
			}
		}
	}
	s.hits.Add(1)
	return entry.Value, true, nil
}

// Set stores under a planned key and indexes declared tags.
func (s *Service) Set(ctx context.Context, plan KeyPlan, value []byte) error {
	if s == nil {
		return ErrInvalid
	}
	if plan.Directive == DirectiveBypass {
		s.bypasses.Add(1)
		return ErrBypass
	}
	if plan.Directive == DirectiveNoStore {
		s.bypasses.Add(1)
		return ErrNoStore
	}
	if plan.Key == "" {
		return ErrInvalid
	}
	var gens map[string]uint64
	if tags := normalizeTags(plan.Tags); len(tags) > 0 {
		current, genErr := s.generations.Current(ctx, tags)
		if genErr != nil {
			// 拿不到 generation 就不写：写进去也永远无法被 tag 失效，只会变成
			// 一份读不掉的脏条目。
			s.errors.Add(1)
			return genErr
		}
		gens = current
	}
	encoded, encodeErr := encodeEntry(value, gens)
	if encodeErr != nil {
		s.errors.Add(1)
		return encodeErr
	}
	start := time.Now()
	err := s.backend.Set(ctx, plan.Key, encoded, plan.TTL)
	s.observeSet(time.Since(start))
	if err != nil {
		s.errors.Add(1)
		return err
	}
	s.stores.Add(1)
	s.indexTags(plan.Tags, plan.Key)
	return nil
}

// Delete removes exact keys.
func (s *Service) Delete(ctx context.Context, keys ...string) error {
	if s == nil {
		return ErrInvalid
	}
	clean := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" {
			clean = append(clean, key)
		}
	}
	if len(clean) == 0 {
		return ErrInvalid
	}
	if err := s.backend.Delete(ctx, clean...); err != nil {
		s.errors.Add(1)
		return err
	}
	s.deletes.Add(uint64(len(clean)))
	s.dropKeysFromIndex(clean)
	return nil
}

// Invalidate deletes by keys and/or tags and records audit evidence.
func (s *Service) Invalidate(ctx context.Context, request InvalidateRequest) (InvalidateResult, error) {
	if s == nil {
		return InvalidateResult{}, ErrInvalid
	}
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Actor == "" {
		return InvalidateResult{}, ErrPermissionDenied
	}
	keys := make(map[string]struct{})
	for _, key := range request.Keys {
		key = strings.TrimSpace(key)
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	s.mu.Lock()
	for _, tag := range request.Tags {
		tag = strings.TrimSpace(tag)
		for key := range s.tagIndex[tag] {
			keys[key] = struct{}{}
		}
	}
	s.mu.Unlock()
	list := make([]string, 0, len(keys))
	for key := range keys {
		list = append(list, key)
	}
	sort.Strings(list)
	if len(list) > 0 {
		if err := s.backend.Delete(ctx, list...); err != nil {
			s.errors.Add(1)
			return InvalidateResult{}, err
		}
		s.deletes.Add(uint64(len(list)))
		s.dropKeysFromIndex(list)
	}
	// tag 失效的权威动作：递增 generation。条目自己会在下一次读取时发现自己过期，
	// 因此这条路径与「哪个副本写了缓存」「哪个副本收到失效」无关。
	if tags := normalizeTags(request.Tags); len(tags) > 0 {
		if _, err := s.generations.Bump(ctx, tags); err != nil {
			// fail closed：generation 没递增就不能声称失效成功，否则旧数据会被
			// 继续当成新鲜数据服务，而且没有任何信号。
			s.errors.Add(1)
			return InvalidateResult{}, err
		}
	}
	s.invalidations.Add(1)
	result := InvalidateResult{
		DeletedKeys: len(list),
		Tags:        append([]string(nil), request.Tags...),
		AuditID:     fmt.Sprintf("cache-inv-%d", s.auditSeq.Add(1)),
		Actor:       request.Actor,
		Reason:      strings.TrimSpace(request.Reason),
		At:          time.Now().UTC(),
	}
	s.pushAudit(result)
	return result, nil
}

// Inspector returns provider selection, metrics, and recent invalidation audit.
func (s *Service) Inspector() InspectorSnapshot {
	if s == nil {
		return InspectorSnapshot{SchemaVersion: SchemaVersion}
	}
	getCount := s.getCount.Load()
	setCount := s.setCount.Load()
	var avgGet, avgSet time.Duration
	if getCount > 0 {
		avgGet = time.Duration(s.getLatencyNanos.Load() / getCount)
	}
	if setCount > 0 {
		avgSet = time.Duration(s.setLatencyNanos.Load() / setCount)
	}
	s.mu.Lock()
	audit := append([]InvalidateResult(nil), s.auditRing...)
	provider := s.provider
	s.mu.Unlock()
	return InspectorSnapshot{
		SchemaVersion: SchemaVersion,
		Provider:      provider,
		Metrics: Metrics{
			Hits: s.hits.Load(), Misses: s.misses.Load(), Stores: s.stores.Load(),
			Deletes: s.deletes.Load(), Bypasses: s.bypasses.Load(), Errors: s.errors.Load(),
			Invalidations: s.invalidations.Load(),
			AvgGetLatency: avgGet, AvgSetLatency: avgSet,
			Provider:       provider.Provider,
			ThemeRevision:  loadString(s.themeRev),
			PluginRevision: loadString(s.pluginRev),
		},
		RecentAudit: audit,
	}
}

func (s *Service) observeGet(d time.Duration) {
	s.getCount.Add(1)
	s.getLatencyNanos.Add(uint64(d.Nanoseconds()))
}

func (s *Service) observeSet(d time.Duration) {
	s.setCount.Add(1)
	s.setLatencyNanos.Add(uint64(d.Nanoseconds()))
}

func (s *Service) indexTags(tags []string, key string) {
	if len(tags) == 0 || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		set, ok := s.tagIndex[tag]
		if !ok {
			set = make(map[string]struct{})
			s.tagIndex[tag] = set
		}
		set[key] = struct{}{}
	}
}

func (s *Service) dropKeysFromIndex(keys []string) {
	if len(keys) == 0 {
		return
	}
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for tag, set := range s.tagIndex {
		for key := range drop {
			delete(set, key)
		}
		if len(set) == 0 {
			delete(s.tagIndex, tag)
		}
	}
}

func (s *Service) pushAudit(result InvalidateResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditRing = append(s.auditRing, result)
	if len(s.auditRing) > maxAuditRing {
		s.auditRing = append([]InvalidateResult(nil), s.auditRing[len(s.auditRing)-maxAuditRing:]...)
	}
}

func loadString(v atomic.Value) string {
	if raw := v.Load(); raw != nil {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}
