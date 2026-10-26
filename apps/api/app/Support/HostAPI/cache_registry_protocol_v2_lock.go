package hostapi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"golang.org/x/text/language"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	protocolV2CacheMaximumLeases         = 65_536
	protocolV2CacheNamespaceMaximumBytes = 81
	protocolV2CacheLocaleMaximumBytes    = 128
)

type protocolV2CacheLeaseBinding struct {
	identity  *protocolv2.ExtensionIdentity
	namespace string
	key       string
	locale    string
}

func newProtocolV2CacheLeaseBinding(
	identity *protocolv2.ExtensionIdentity,
	request *protocolv2.RequestContext,
	namespace string,
	key string,
) (protocolV2CacheLeaseBinding, error) {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	if identity == nil || request == nil ||
		!boundedHostCacheIdentity(namespace, protocolV2CacheNamespaceMaximumBytes) ||
		validateHostCacheKey(key) != nil {
		return protocolV2CacheLeaseBinding{}, ErrHostCacheInvalid
	}
	locale := strings.TrimSpace(request.GetLocale())
	if len(locale) > protocolV2CacheLocaleMaximumBytes {
		return protocolV2CacheLeaseBinding{}, ErrHostCacheInvalid
	}
	if locale == "" {
		locale = language.Und.String()
	} else {
		tag, err := language.Parse(locale)
		if err != nil {
			return protocolV2CacheLeaseBinding{}, ErrHostCacheInvalid
		}
		locale = tag.String()
	}
	return protocolV2CacheLeaseBinding{
		identity:  proto.Clone(identity).(*protocolv2.ExtensionIdentity),
		namespace: namespace,
		key:       key,
		locale:    locale,
	}, nil
}

func (b protocolV2CacheLeaseBinding) matches(other protocolV2CacheLeaseBinding) bool {
	return proto.Equal(b.identity, other.identity) && b.namespace == other.namespace &&
		b.key == other.key && b.locale == other.locale
}

type protocolV2CacheLease struct {
	token     string
	binding   protocolV2CacheLeaseBinding
	lock      *HostCacheLock
	cancel    context.CancelCauseFunc
	expiresAt time.Time
	timer     *time.Timer
}

type protocolV2CacheLeaseRegistry struct {
	mu      sync.Mutex
	entries map[string]*protocolV2CacheLease
}

func newProtocolV2CacheLeaseRegistry() *protocolV2CacheLeaseRegistry {
	return &protocolV2CacheLeaseRegistry{entries: make(map[string]*protocolV2CacheLease)}
}

func (r *protocolV2CacheLeaseRegistry) register(
	binding protocolV2CacheLeaseBinding,
	lock *HostCacheLock,
	cancel context.CancelCauseFunc,
) (*protocolV2CacheLease, error) {
	if r == nil || lock == nil || cancel == nil {
		return nil, ErrHostCacheProviderUnavailable
	}
	expiresAt, err := protocolV2CacheLockExpiry(lock)
	if err != nil {
		return nil, err
	}
	for range 8 {
		token, tokenErr := newHostCacheLockToken()
		if tokenErr != nil {
			return nil, ErrHostCacheProviderUnavailable
		}
		entry := &protocolV2CacheLease{
			token: token, binding: binding, lock: lock, cancel: cancel, expiresAt: expiresAt,
		}
		r.mu.Lock()
		if len(r.entries) >= protocolV2CacheMaximumLeases {
			r.mu.Unlock()
			return nil, ErrHostCacheProviderUnavailable
		}
		if _, exists := r.entries[token]; exists {
			r.mu.Unlock()
			continue
		}
		remaining := time.Until(expiresAt)
		if remaining <= 0 {
			r.mu.Unlock()
			return nil, ErrHostCacheLockNotOwned
		}
		r.entries[token] = entry
		entry.timer = time.AfterFunc(remaining, func() { r.expire(entry) })
		r.mu.Unlock()
		return entry, nil
	}
	return nil, ErrHostCacheProviderUnavailable
}

func (r *protocolV2CacheLeaseRegistry) take(
	token string,
	binding protocolV2CacheLeaseBinding,
) (*protocolV2CacheLease, error) {
	if r == nil || token != strings.TrimSpace(token) || validateHostCacheRevision(token, false) != nil {
		return nil, ErrHostCacheInvalid
	}
	r.mu.Lock()
	entry := r.entries[token]
	if entry == nil {
		r.mu.Unlock()
		return nil, ErrHostCacheLockNotOwned
	}
	if !entry.binding.matches(binding) {
		r.mu.Unlock()
		return nil, ErrHostCacheLockNotOwned
	}
	if !time.Now().Before(entry.expiresAt) {
		delete(r.entries, token)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		r.mu.Unlock()
		entry.cancel(ErrHostCacheLockNotOwned)
		return nil, ErrHostCacheLockNotOwned
	}
	if entry.lock == nil || entry.lock.ctx == nil || entry.lock.ctx.Err() != nil {
		delete(r.entries, token)
		if entry.timer != nil {
			entry.timer.Stop()
		}
		r.mu.Unlock()
		entry.cancel(ErrHostCacheStale)
		return nil, ErrHostCacheStale
	}
	delete(r.entries, token)
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	r.mu.Unlock()
	return entry, nil
}

func (r *protocolV2CacheLeaseRegistry) reinsert(entry *protocolV2CacheLease) (*protocolV2CacheLease, error) {
	if r == nil || entry == nil || entry.lock == nil {
		return nil, ErrHostCacheProviderUnavailable
	}
	expiresAt, err := protocolV2CacheLockExpiry(entry.lock)
	if err != nil {
		return nil, err
	}
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return nil, ErrHostCacheLockNotOwned
	}
	// Renew publishes a new generation. A stopped timer may already have queued
	// its callback; keeping the old entry pointer would let that callback match
	// and remove the freshly renewed lease.
	replacement := &protocolV2CacheLease{
		token: entry.token, binding: entry.binding, lock: entry.lock, cancel: entry.cancel, expiresAt: expiresAt,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) >= protocolV2CacheMaximumLeases || r.entries[entry.token] != nil {
		return nil, ErrHostCacheProviderUnavailable
	}
	r.entries[replacement.token] = replacement
	replacement.timer = time.AfterFunc(remaining, func() { r.expire(replacement) })
	return replacement, nil
}

func (r *protocolV2CacheLeaseRegistry) remove(entry *protocolV2CacheLease) {
	if r == nil || entry == nil {
		return
	}
	r.mu.Lock()
	if r.entries[entry.token] == entry {
		delete(r.entries, entry.token)
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
	}
	r.mu.Unlock()
}

func (r *protocolV2CacheLeaseRegistry) expire(entry *protocolV2CacheLease) {
	if r == nil || entry == nil {
		return
	}
	r.mu.Lock()
	if r.entries[entry.token] != entry {
		r.mu.Unlock()
		return
	}
	delete(r.entries, entry.token)
	entry.timer = nil
	r.mu.Unlock()
	entry.cancel(ErrHostCacheLockNotOwned)
}

func protocolV2CacheLockExpiry(lock *HostCacheLock) (time.Time, error) {
	if lock == nil {
		return time.Time{}, ErrHostCacheLockNotOwned
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released || lock.ctx == nil || !time.Now().Before(lock.expiresAt) {
		return time.Time{}, ErrHostCacheLockNotOwned
	}
	return lock.expiresAt, nil
}

func closeProtocolV2CacheLease(entry *protocolV2CacheLease, cause error) {
	if entry == nil {
		return
	}
	if entry.lock != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		_ = entry.lock.Release(cleanupCtx)
		cleanupCancel()
	}
	if entry.cancel != nil {
		entry.cancel(cause)
	}
}

func (s *ProtocolV2CacheServiceServer) AcquireLock(
	ctx context.Context,
	request *hostv2.CacheLockAcquireRequest,
) (*hostv2.CacheLockAcquireResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheLockAcquireResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if s == nil || s.service == nil || s.leases == nil || request.GetTtl() == nil || request.GetTtl().CheckValid() != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	binding, err := newProtocolV2CacheLeaseBinding(identity, request.GetContext(), request.GetNamespace(), request.GetKey())
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		response.Error = protocolV2CacheFailure(context.Cause(ctx))
		return response, nil
	}
	// The RPC context may end as soon as this response is sent. Keep only its
	// cancellation while acquiring, then let lock TTL and exact-runtime drain own
	// the cross-RPC lifetime.
	leaseCtx, cancelLease := context.WithCancelCause(context.WithoutCancel(ctx))
	stopCaller := context.AfterFunc(ctx, func() { cancelLease(context.Cause(ctx)) })
	lock, acquired, err := s.service.AcquireLock(leaseCtx, HostCacheLockRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Key:                  request.GetKey(), TTL: request.GetTtl().AsDuration(),
	})
	if err != nil {
		stopCaller()
		cancelLease(err)
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if !acquired {
		stopCaller()
		cancelLease(nil)
		return response, nil
	}
	entry, err := s.leases.register(binding, lock, cancelLease)
	if err != nil {
		stopCaller()
		closeProtocolV2CacheLease(&protocolV2CacheLease{lock: lock, cancel: cancelLease}, err)
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if !stopCaller() || ctx.Err() != nil {
		s.leases.remove(entry)
		cause := context.Cause(ctx)
		if cause == nil {
			cause = context.Canceled
		}
		closeProtocolV2CacheLease(entry, cause)
		response.Error = protocolV2CacheFailure(cause)
		return response, nil
	}
	response.Acquired = true
	response.LeaseToken = entry.token
	response.ExpiresAt = timestamppb.New(entry.expiresAt)
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) RenewLock(
	ctx context.Context,
	request *hostv2.CacheLockRenewRequest,
) (*hostv2.CacheLockRenewResponse, error) {
	identity, _, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheLockRenewResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if s == nil || s.leases == nil || request.GetTtl() == nil || request.GetTtl().CheckValid() != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	binding, err := newProtocolV2CacheLeaseBinding(identity, request.GetContext(), request.GetNamespace(), request.GetKey())
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	entry, err := s.leases.take(request.GetLeaseToken(), binding)
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if err = entry.lock.Renew(ctx, request.GetTtl().AsDuration()); err != nil {
		closeProtocolV2CacheLease(entry, err)
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if entry, err = s.leases.reinsert(entry); err != nil {
		closeProtocolV2CacheLease(entry, err)
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Renewed = true
	response.ExpiresAt = timestamppb.New(entry.expiresAt)
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) ReleaseLock(
	ctx context.Context,
	request *hostv2.CacheLockReleaseRequest,
) (*hostv2.CacheLockReleaseResponse, error) {
	identity, _, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheLockReleaseResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if s == nil || s.leases == nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	binding, err := newProtocolV2CacheLeaseBinding(identity, request.GetContext(), request.GetNamespace(), request.GetKey())
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	entry, err := s.leases.take(request.GetLeaseToken(), binding)
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	err = entry.lock.Release(ctx)
	entry.cancel(err)
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Released = true
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) SetAndReleaseLock(
	ctx context.Context,
	request *hostv2.CacheSetAndReleaseLockRequest,
) (*hostv2.CacheSetAndReleaseLockResponse, error) {
	identity, _, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheSetAndReleaseLockResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if s == nil || s.leases == nil || request.GetValue() == nil || request.GetValue().GetValue() == nil ||
		request.GetTtl() == nil || request.GetTtl().CheckValid() != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	value, err := json.Marshal(request.GetValue().GetValue().AsMap())
	if err != nil || len(value) == 0 || len(value) > HostCacheMaximumValueBytes {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	binding, err := newProtocolV2CacheLeaseBinding(identity, request.GetContext(), request.GetNamespace(), request.GetKey())
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	entry, err := s.leases.take(request.GetLeaseToken(), binding)
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	revision, err := setAndReleaseProtocolV2CacheLock(ctx, entry.lock, HostCacheSetRequest{
		Key:    request.GetKey(),
		Schema: HostCacheSchema{ID: request.GetValue().GetSchemaId(), Version: request.GetValue().GetSchemaVersion()},
		Value:  value, TTL: request.GetTtl().AsDuration(), Tags: request.GetTags(),
		ExpectedRevision: request.GetExpectedRevision(),
	})
	entry.cancel(err)
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Revision = revision
	return response, nil
}

func setAndReleaseProtocolV2CacheLock(
	ctx context.Context,
	lock *HostCacheLock,
	request HostCacheSetRequest,
) (revision string, err error) {
	if lock == nil || lock.service == nil || ctx == nil || validateHostCacheKey(request.Key) != nil ||
		validateHostCacheSchema(request.Schema) != nil || validateHostCacheTTL(request.TTL) != nil ||
		len(request.Value) == 0 || len(request.Value) > HostCacheMaximumValueBytes ||
		validateHostCacheRevision(strings.TrimSpace(request.ExpectedRevision), true) != nil {
		return "", ErrHostCacheInvalid
	}
	request.ExpectedRevision = strings.TrimSpace(request.ExpectedRevision)
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return "", ErrHostCacheLockNotOwned
	}
	lock.released = true
	defer lock.releaseAdmissionsLocked()
	started := time.Now()
	defer func() {
		affected := uint64(0)
		if err == nil && revision != "" {
			affected = 1
		}
		lock.service.recordHostCacheTrace(started, lock.prepared, lock.candidate, "lock_set_release", err, false, affected, 1)
	}()
	lockOwned := true
	defer func() {
		if lockOwned {
			releaseHostCacheLockToken(lock.candidate, lock.provider, lock.ctx, lock.key, lock.token)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", context.Cause(ctx)
	}
	if err := lock.service.validateHostCacheExecution(lock.prepared, lock.provider, lock.ctx); err != nil {
		return "", err
	}
	if !time.Now().Before(lock.expiresAt) {
		return "", ErrHostCacheLockNotOwned
	}
	physicalTags := []string{}
	if len(request.Tags) > 0 {
		physicalTags, err = lock.prepared.physicalTags(request.Tags)
		if err != nil {
			return "", err
		}
	}
	lock.prepared.setTraceTags(physicalTags)
	revision, err = newHostCacheRevision()
	if err != nil {
		return "", ErrHostCacheProviderUnavailable
	}
	stored := HostCacheStoredValue{
		Value: slices.Clone(request.Value), SchemaID: strings.TrimSpace(request.Schema.ID),
		SchemaVersion: strings.TrimSpace(request.Schema.Version), Revision: revision, Tags: physicalTags,
	}
	operationCtx, finishOperation := hostCacheLockOperationContext(lock.ctx, ctx)
	commitCtx, cancelAtExpiry := context.WithDeadline(operationCtx, lock.expiresAt)
	defer func() {
		cancelAtExpiry()
		finishOperation()
	}()
	backendErr := lock.candidate.Backend.SetAndReleaseLock(
		commitCtx, lock.prepared.valueKey(request.Key), stored, request.TTL,
		request.ExpectedRevision, lock.prepared.tagPrefix, lock.key, lock.token,
	)
	if backendErr == nil {
		// Backend nil is the atomic commit point even if response delivery is later cancelled.
		lockOwned = false
		return revision, nil
	}
	if validateErr := lock.service.validateHostCacheExecution(lock.prepared, lock.provider, lock.ctx); validateErr != nil {
		return "", validateErr
	}
	if cancellationErr := hostCacheContextCancellation(operationCtx); cancellationErr != nil {
		return "", cancellationErr
	}
	return "", hostCacheBackendError(backendErr)
}

var _ hostv2.CacheServiceServer = (*ProtocolV2CacheServiceServer)(nil)
