package pluginv2

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	DefaultHostCacheTTL             = 5 * time.Minute
	DefaultHostCacheLockTTL         = 5 * time.Second
	DefaultHostCacheRememberWait    = 2 * time.Second
	DefaultHostCacheRememberBackoff = 25 * time.Millisecond

	hostCacheKeyMaxBytes        = 512
	hostCacheNamespaceMax       = 81
	hostCacheTagsMax            = 64
	hostCacheMinTTL             = time.Millisecond
	hostCacheMaxTTL             = 24 * time.Hour
	hostCacheMinLockTTL         = 100 * time.Millisecond
	hostCacheMaxLockTTL         = 30 * time.Second
	hostCacheMaxRememberWait    = 30 * time.Second
	hostCacheMinBackoff         = time.Millisecond
	hostCacheMaxBackoff         = time.Second
	hostCacheBackoffSoftCeiling = 250 * time.Millisecond
)

var (
	ErrHostCacheRemoteResponse  = errors.New("pluginv2: Host cache returned an error")
	ErrHostCacheResponseInvalid = errors.New("pluginv2: Host cache response is invalid")
	ErrHostCacheInvalidArgument = errors.New("pluginv2: Host cache argument is invalid")
	ErrHostCacheLockContended   = errors.New("pluginv2: Host cache lock is held by another caller")
	ErrHostCacheLockExpired     = errors.New("pluginv2: Host cache lock expired")
	ErrHostCacheLeaseConsumed   = errors.New("pluginv2: Host cache lock lease was already consumed")
)

// HostCacheError preserves the stable wire error without exposing transport or
// provider internals. errors.Is also recognizes cancellation/deadline codes.
type HostCacheError struct {
	Code      protocolwire.ErrorCode
	Reason    string
	Retryable bool
}

func (e *HostCacheError) Error() string {
	if e == nil {
		return ErrHostCacheRemoteResponse.Error()
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", ErrHostCacheRemoteResponse, e.Reason)
	}
	return ErrHostCacheRemoteResponse.Error()
}

func (e *HostCacheError) Is(target error) bool { return target == ErrHostCacheRemoteResponse }

func (e *HostCacheError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case protocolwire.ErrorCode_ERROR_CODE_CANCELLED:
		return context.Canceled
	case protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// CacheLockOptions identifies one declared logical key and its bounded lock TTL.
type CacheLockOptions struct {
	Parent    *protocolwire.RequestContext
	Namespace string
	Key       string
	TTL       time.Duration
}

// CacheSetAndReleaseOptions is the typed value committed atomically with lease consumption.
type CacheSetAndReleaseOptions struct {
	Value            *protocolwire.TypedDocument
	TTL              time.Duration
	Tags             []string
	ExpectedRevision string
}

// CacheLockLease hides the opaque Host token and serializes local operations.
// One successful Release or SetAndRelease consumes the lease permanently.
type CacheLockLease struct {
	mu sync.Mutex

	host      *Host
	parent    *protocolwire.RequestContext
	namespace string
	key       string
	token     string
	expiresAt time.Time
	consumed  bool
}

// String deliberately omits the opaque capability token.
func (l *CacheLockLease) String() string {
	if l == nil {
		return "<nil>"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("CacheLockLease{namespace:%q key:%q expires_at:%q consumed:%t}",
		l.namespace, l.key, l.expiresAt.UTC().Format(time.RFC3339Nano), l.consumed)
}

// GoString keeps %#v formatting safe for diagnostics and test failures.
func (l *CacheLockLease) GoString() string { return l.String() }

// LogValue exposes useful lease state to slog without the capability token.
func (l *CacheLockLease) LogValue() slog.Value {
	if l == nil {
		return slog.StringValue("<nil>")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return slog.GroupValue(
		slog.String("namespace", l.namespace),
		slog.String("key", l.key),
		slog.Time("expires_at", l.expiresAt),
		slog.Bool("consumed", l.consumed),
	)
}

// ExpiresAt returns the latest conservative Host-reported lease deadline.
func (l *CacheLockLease) ExpiresAt() time.Time {
	if l == nil {
		return time.Time{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expiresAt
}

// AcquireCacheLock returns (nil, false, nil) for ordinary contention. The
// opaque wire token remains private inside CacheLockLease.
func (h *Host) AcquireCacheLock(
	ctx context.Context,
	options CacheLockOptions,
) (*CacheLockLease, bool, error) {
	if !h.cacheAvailable() {
		return nil, false, ErrHostUnavailable
	}
	if ctx == nil {
		return nil, false, ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheLockOptions(options)
	if err != nil {
		return nil, false, err
	}
	requestContext := h.RequestContext(options.Parent)
	response, err := h.Cache.AcquireLock(ctx, &hostwire.CacheLockAcquireRequest{
		Context: requestContext, Namespace: options.Namespace, Key: options.Key,
		Ttl: durationpb.New(options.TTL),
	})
	if err != nil {
		return nil, false, err
	}
	if response == nil {
		return nil, false, ErrHostCacheResponseInvalid
	}
	responseToken := strings.TrimSpace(response.GetLeaseToken())
	if !h.validCacheResponseContext(requestContext, response.GetContext()) {
		if validCacheOpaqueToken(responseToken) {
			h.releaseCacheTokenBestEffort(ctx, options.Parent, options.Namespace, options.Key, responseToken)
		}
		return nil, false, ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return nil, false, hostCacheErrorFromDetail(response.GetError())
	}
	if !response.GetAcquired() {
		if response.GetLeaseToken() != "" || response.GetExpiresAt() != nil {
			if validCacheOpaqueToken(responseToken) {
				h.releaseCacheTokenBestEffort(ctx, options.Parent, options.Namespace, options.Key, responseToken)
			}
			return nil, false, ErrHostCacheResponseInvalid
		}
		return nil, false, nil
	}
	token := responseToken
	expiresAt := response.GetExpiresAt()
	if !validCacheOpaqueToken(token) || expiresAt == nil || !expiresAt.IsValid() ||
		!time.Now().Before(expiresAt.AsTime()) {
		if validCacheOpaqueToken(token) {
			h.releaseCacheTokenBestEffort(ctx, options.Parent, options.Namespace, options.Key, token)
		}
		return nil, false, ErrHostCacheResponseInvalid
	}
	return &CacheLockLease{
		host: h, parent: cloneRequestContext(options.Parent), namespace: options.Namespace,
		key: options.Key, token: token, expiresAt: expiresAt.AsTime(),
	}, true, nil
}

// Renew extends an active lease through the Host. It never changes namespace or key binding.
func (l *CacheLockLease) Renew(ctx context.Context, ttl time.Duration) error {
	if l == nil || ctx == nil {
		return ErrHostCacheInvalidArgument
	}
	var err error
	if ttl, err = normalizeCacheLockTTL(ttl); err != nil {
		return err
	}
	l.mu.Lock()
	if l.consumed {
		l.mu.Unlock()
		return ErrHostCacheLeaseConsumed
	}
	if l.host == nil || !l.host.cacheAvailable() {
		l.mu.Unlock()
		return ErrHostUnavailable
	}
	if !time.Now().Before(l.expiresAt) {
		l.consumeLocked()
		l.mu.Unlock()
		return ErrHostCacheLockExpired
	}
	host, parent, namespace, key := l.host, cloneRequestContext(l.parent), l.namespace, l.key
	requestContext := host.RequestContext(parent)
	renewCtx, cancelRenew := context.WithDeadlineCause(ctx, l.expiresAt, ErrHostCacheLockExpired)
	response, err := host.Cache.RenewLock(renewCtx, &hostwire.CacheLockRenewRequest{
		Context: requestContext, Namespace: l.namespace, Key: l.key,
		LeaseToken: l.token, Ttl: durationpb.New(ttl),
	})
	cause := context.Cause(renewCtx)
	cancelRenew()
	fail := func(failure error) error {
		token := l.consumeLocked()
		l.mu.Unlock()
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return failure
	}
	if err != nil {
		if cause != nil {
			err = cause
		}
		return fail(err)
	}
	if response == nil || !host.validCacheResponseContext(requestContext, response.GetContext()) {
		return fail(ErrHostCacheResponseInvalid)
	}
	if response.GetError() != nil {
		return fail(hostCacheErrorFromDetail(response.GetError()))
	}
	if !response.GetRenewed() || response.GetExpiresAt() == nil || !response.GetExpiresAt().IsValid() ||
		!time.Now().Before(response.GetExpiresAt().AsTime()) {
		return fail(ErrHostCacheResponseInvalid)
	}
	l.expiresAt = response.GetExpiresAt().AsTime()
	l.mu.Unlock()
	return nil
}

// Release consumes an active lease without writing a value.
func (l *CacheLockLease) Release(ctx context.Context) error {
	if l == nil || ctx == nil {
		return ErrHostCacheInvalidArgument
	}
	l.mu.Lock()
	if l.consumed {
		l.mu.Unlock()
		return ErrHostCacheLeaseConsumed
	}
	if l.host == nil || !l.host.cacheAvailable() {
		l.mu.Unlock()
		return ErrHostUnavailable
	}
	host, parent, namespace, key := l.host, cloneRequestContext(l.parent), l.namespace, l.key
	token := l.consumeLocked()
	l.mu.Unlock()
	requestContext := host.RequestContext(parent)
	response, err := host.Cache.ReleaseLock(ctx, &hostwire.CacheLockReleaseRequest{
		Context: requestContext, Namespace: namespace, Key: key, LeaseToken: token,
	})
	if err != nil {
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return err
	}
	if response == nil || !host.validCacheResponseContext(requestContext, response.GetContext()) {
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return hostCacheErrorFromDetail(response.GetError())
	}
	if !response.GetReleased() {
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return ErrHostCacheResponseInvalid
	}
	return nil
}

// SetAndRelease atomically commits one typed value and consumes the lease.
func (l *CacheLockLease) SetAndRelease(
	ctx context.Context,
	options CacheSetAndReleaseOptions,
) (string, error) {
	if l == nil || ctx == nil {
		return "", ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheSetAndReleaseOptions(options)
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	if l.consumed {
		l.mu.Unlock()
		return "", ErrHostCacheLeaseConsumed
	}
	if l.host == nil || !l.host.cacheAvailable() {
		l.mu.Unlock()
		return "", ErrHostUnavailable
	}
	if !time.Now().Before(l.expiresAt) {
		l.consumeLocked()
		l.mu.Unlock()
		return "", ErrHostCacheLockExpired
	}
	host, parent, namespace, key := l.host, cloneRequestContext(l.parent), l.namespace, l.key
	expiresAt := l.expiresAt
	token := l.consumeLocked()
	l.mu.Unlock()
	requestContext := host.RequestContext(parent)
	commitCtx, cancelCommit := context.WithDeadlineCause(ctx, expiresAt, ErrHostCacheLockExpired)
	response, err := host.Cache.SetAndReleaseLock(commitCtx, &hostwire.CacheSetAndReleaseLockRequest{
		Context: requestContext, Namespace: namespace, Key: key, LeaseToken: token,
		Value: cloneTypedDocument(options.Value), Ttl: durationpb.New(options.TTL),
		Tags: append([]string(nil), options.Tags...), ExpectedRevision: options.ExpectedRevision,
	})
	cause := context.Cause(commitCtx)
	cancelCommit()
	if err != nil {
		if cause != nil {
			err = cause
		}
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return "", err
	}
	if response == nil || !host.validCacheResponseContext(requestContext, response.GetContext()) {
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return "", ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return "", hostCacheErrorFromDetail(response.GetError())
	}
	revision := strings.TrimSpace(response.GetRevision())
	if !validCacheOpaqueToken(revision) {
		host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
		return "", ErrHostCacheResponseInvalid
	}
	return revision, nil
}

// CacheRememberOptions bounds cache lifetime, lock lifetime, and contention waiting.
type CacheRememberOptions struct {
	Parent           *protocolwire.RequestContext
	Namespace        string
	Key              string
	ValueSchema      string
	TTL              time.Duration
	LockTTL          time.Duration
	Wait             time.Duration
	Backoff          time.Duration
	Tags             []string
	ExpectedRevision string
}

// CacheRememberResult reports the resolved value and whether this caller loaded it.
type CacheRememberResult struct {
	Value    *protocolwire.TypedDocument
	Revision string
	Loaded   bool
}

// CacheLoader runs only inside the plugin process while its Host lease is renewed.
type CacheLoader func(context.Context) (*protocolwire.TypedDocument, error)

// RememberCache implements cross-process single-flight through Host lock RPCs.
// The loader executes only in the plugin process; Host receives typed data.
func (h *Host) RememberCache(
	ctx context.Context,
	options CacheRememberOptions,
	loader CacheLoader,
) (CacheRememberResult, error) {
	if !h.cacheAvailable() {
		return CacheRememberResult{}, ErrHostUnavailable
	}
	if ctx == nil || loader == nil {
		return CacheRememberResult{}, ErrHostCacheInvalidArgument
	}
	options, schemaID, schemaVersion, err := normalizeCacheRememberOptions(options)
	if err != nil {
		return CacheRememberResult{}, err
	}
	waitCtx, cancelWait := context.WithTimeoutCause(ctx, options.Wait, ErrHostCacheLockContended)
	defer cancelWait()
	if cached, err := h.getCacheValue(waitCtx, options.Parent, options.Namespace, options.Key, schemaID, schemaVersion); err != nil {
		if cause := context.Cause(waitCtx); cause != nil {
			return CacheRememberResult{}, cause
		}
		return CacheRememberResult{}, err
	} else if cached.Found {
		return CacheRememberResult{Value: cached.Value, Revision: cached.Revision}, nil
	}

	backoff := options.Backoff
	for {
		if cause := context.Cause(waitCtx); cause != nil {
			return CacheRememberResult{}, cause
		}
		lease, acquired, err := h.AcquireCacheLock(waitCtx, CacheLockOptions{
			Parent: options.Parent, Namespace: options.Namespace, Key: options.Key, TTL: options.LockTTL,
		})
		if cause := context.Cause(waitCtx); cause != nil {
			if lease != nil {
				lease.releaseBestEffort(ctx)
			}
			return CacheRememberResult{}, cause
		}
		if err != nil {
			return CacheRememberResult{}, err
		}
		if acquired {
			cancelWait()
			return h.rememberCacheWithLease(ctx, options, schemaID, schemaVersion, lease, loader)
		}
		// The active owner may have committed between SET NX and this response.
		cached, err := h.getCacheValue(waitCtx, options.Parent, options.Namespace, options.Key, schemaID, schemaVersion)
		if cause := context.Cause(waitCtx); cause != nil {
			return CacheRememberResult{}, cause
		}
		if err != nil {
			return CacheRememberResult{}, err
		} else if cached.Found {
			return CacheRememberResult{Value: cached.Value, Revision: cached.Revision}, nil
		}
		delay := backoff
		if deadline, ok := waitCtx.Deadline(); ok {
			delay = min(delay, time.Until(deadline))
		}
		if delay <= 0 {
			if cause := context.Cause(waitCtx); cause != nil {
				return CacheRememberResult{}, cause
			}
			if cause := context.Cause(ctx); cause != nil {
				return CacheRememberResult{}, cause
			}
			return CacheRememberResult{}, ErrHostCacheLockContended
		}
		timer := time.NewTimer(delay)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return CacheRememberResult{}, context.Cause(waitCtx)
		case <-timer.C:
		}
		backoff = nextCacheRememberBackoff(backoff)
	}
}

func (h *Host) rememberCacheWithLease(
	ctx context.Context,
	options CacheRememberOptions,
	schemaID string,
	schemaVersion string,
	lease *CacheLockLease,
	loader CacheLoader,
) (CacheRememberResult, error) {
	defer lease.releaseBestEffort(ctx)
	leaseCtx, cancelLeaseRead, err := lease.contextUntilExpiry(ctx)
	if err != nil {
		return CacheRememberResult{}, err
	}
	cached, err := h.getCacheValue(leaseCtx, options.Parent, options.Namespace, options.Key, schemaID, schemaVersion)
	cause := context.Cause(leaseCtx)
	cancelLeaseRead()
	if err != nil {
		if cause != nil {
			return CacheRememberResult{}, cause
		}
		return CacheRememberResult{}, err
	}
	if cause != nil {
		return CacheRememberResult{}, cause
	}
	if err := lease.activeError(); err != nil {
		return CacheRememberResult{}, err
	}
	if cached.Found {
		return CacheRememberResult{Value: cached.Value, Revision: cached.Revision}, nil
	}
	loaded, err := runCacheLoaderWithRenewal(ctx, lease, options.LockTTL, loader)
	if err != nil {
		return CacheRememberResult{}, err
	}
	if !cacheDocumentMatches(loaded, schemaID, schemaVersion) {
		return CacheRememberResult{}, ErrHostCacheInvalidArgument
	}
	revision, err := lease.SetAndRelease(ctx, CacheSetAndReleaseOptions{
		Value: loaded, TTL: options.TTL, Tags: options.Tags, ExpectedRevision: options.ExpectedRevision,
	})
	if err != nil {
		return CacheRememberResult{}, err
	}
	return CacheRememberResult{Value: cloneTypedDocument(loaded), Revision: revision, Loaded: true}, nil
}

func runCacheLoaderWithRenewal(
	ctx context.Context,
	lease *CacheLockLease,
	lockTTL time.Duration,
	loader CacheLoader,
) (*protocolwire.TypedDocument, error) {
	loadCtx, cancelLoad := context.WithCancelCause(ctx)
	done := make(chan struct{})
	renewed := make(chan error, 1)
	go func() {
		renewed <- monitorCacheLease(loadCtx, cancelLoad, done, lease, lockTTL)
	}()
	var value *protocolwire.TypedDocument
	var loadErr error
	func() {
		defer close(done)
		value, loadErr = loader(loadCtx)
	}()
	renewErr := <-renewed
	cancelLoad(nil)
	if renewErr != nil && (loadErr == nil || errors.Is(loadErr, context.Canceled)) {
		return nil, renewErr
	}
	if loadErr != nil {
		return nil, loadErr
	}
	if renewErr != nil {
		return nil, renewErr
	}
	return cloneTypedDocument(value), nil
}

func monitorCacheLease(
	loadCtx context.Context,
	cancelLoad context.CancelCauseFunc,
	loaderDone <-chan struct{},
	lease *CacheLockLease,
	lockTTL time.Duration,
) error {
	done := loaderDone
	for {
		expiresAt := lease.ExpiresAt()
		remaining := time.Until(expiresAt)
		if remaining <= 0 {
			cancelLoad(ErrHostCacheLockExpired)
			return ErrHostCacheLockExpired
		}
		renewTimer := time.NewTimer(remaining / 3)
		expiryTimer := time.NewTimer(remaining)
		select {
		case <-done:
			stopCacheTimer(renewTimer)
			stopCacheTimer(expiryTimer)
			if !time.Now().Before(expiresAt) {
				cancelLoad(ErrHostCacheLockExpired)
				return ErrHostCacheLockExpired
			}
			return nil
		case <-loadCtx.Done():
			stopCacheTimer(renewTimer)
			stopCacheTimer(expiryTimer)
			return context.Cause(loadCtx)
		case <-expiryTimer.C:
			stopCacheTimer(renewTimer)
			cancelLoad(ErrHostCacheLockExpired)
			return ErrHostCacheLockExpired
		case <-renewTimer.C:
		}

		renewResult := make(chan error, 1)
		go func() { renewResult <- lease.Renew(loadCtx, lockTTL) }()
		loaderFinished := false
		for {
			select {
			case err := <-renewResult:
				stopCacheTimer(expiryTimer)
				if err != nil {
					cancelLoad(err)
					return err
				}
				if loaderFinished {
					return nil
				}
				goto renewed
			case <-done:
				done = nil
				loaderFinished = true
			case <-loadCtx.Done():
				stopCacheTimer(expiryTimer)
				return context.Cause(loadCtx)
			case <-expiryTimer.C:
				cancelLoad(ErrHostCacheLockExpired)
				return ErrHostCacheLockExpired
			}
		}
	renewed:
	}
}

func stopCacheTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

func (h *Host) getCacheValue(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	namespace string,
	key string,
	schemaID string,
	schemaVersion string,
) (CacheGetResult, error) {
	requestContext := h.RequestContext(parent)
	response, err := h.Cache.Get(ctx, &hostwire.CacheGetRequest{
		Context: requestContext, Namespace: namespace, Key: key,
		ValueSchemaId: schemaID, ValueSchemaVersion: schemaVersion,
	})
	if err != nil {
		return CacheGetResult{}, err
	}
	if response == nil || !h.validCacheResponseContext(requestContext, response.GetContext()) {
		return CacheGetResult{}, ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return CacheGetResult{}, hostCacheErrorFromDetail(response.GetError())
	}
	if !response.GetFound() {
		if response.GetValue() != nil || response.GetRevision() != "" {
			return CacheGetResult{}, ErrHostCacheResponseInvalid
		}
		return CacheGetResult{}, nil
	}
	revision := strings.TrimSpace(response.GetRevision())
	if !cacheDocumentMatches(response.GetValue(), schemaID, schemaVersion) || !validCacheOpaqueToken(revision) {
		return CacheGetResult{}, ErrHostCacheResponseInvalid
	}
	return CacheGetResult{Found: true, Value: cloneTypedDocument(response.GetValue()), Revision: revision}, nil
}

func (l *CacheLockLease) releaseBestEffort(ctx context.Context) {
	if l == nil || ctx == nil {
		return
	}
	l.mu.Lock()
	if l.consumed || l.host == nil {
		l.mu.Unlock()
		return
	}
	host, parent, namespace, key := l.host, cloneRequestContext(l.parent), l.namespace, l.key
	token := l.consumeLocked()
	l.mu.Unlock()
	host.releaseCacheTokenBestEffort(ctx, parent, namespace, key, token)
}

func (l *CacheLockLease) contextUntilExpiry(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if l == nil || ctx == nil {
		return nil, nil, ErrHostCacheInvalidArgument
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.consumed {
		return nil, nil, ErrHostCacheLeaseConsumed
	}
	if !time.Now().Before(l.expiresAt) {
		return nil, nil, ErrHostCacheLockExpired
	}
	bounded, cancel := context.WithDeadlineCause(ctx, l.expiresAt, ErrHostCacheLockExpired)
	return bounded, cancel, nil
}

func (l *CacheLockLease) activeError() error {
	if l == nil {
		return ErrHostCacheInvalidArgument
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.consumed {
		return ErrHostCacheLeaseConsumed
	}
	if !time.Now().Before(l.expiresAt) {
		return ErrHostCacheLockExpired
	}
	return nil
}

func (l *CacheLockLease) consumeLocked() string {
	token := l.token
	l.token = ""
	l.consumed = true
	return token
}

func (h *Host) releaseCacheTokenBestEffort(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	namespace string,
	key string,
	token string,
) {
	if !h.cacheAvailable() || ctx == nil || !validCacheOpaqueToken(token) {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	requestContext := h.RequestContext(parent)
	if deadline, ok := cleanupCtx.Deadline(); ok {
		requestContext.Deadline = timestamppb.New(deadline.UTC())
	}
	_, _ = h.Cache.ReleaseLock(cleanupCtx, &hostwire.CacheLockReleaseRequest{
		Context: requestContext, Namespace: namespace, Key: key, LeaseToken: token,
	})
}

func normalizeCacheLockOptions(options CacheLockOptions) (CacheLockOptions, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheLockOptions{}, err
	}
	options.Namespace = namespace
	options.Parent = cloneRequestContext(options.Parent)
	options.TTL, err = normalizeCacheLockTTL(options.TTL)
	if err != nil {
		return CacheLockOptions{}, err
	}
	return options, nil
}

func normalizeCacheSetAndReleaseOptions(options CacheSetAndReleaseOptions) (CacheSetAndReleaseOptions, error) {
	if options.TTL == 0 {
		options.TTL = DefaultHostCacheTTL
	}
	options.ExpectedRevision = strings.TrimSpace(options.ExpectedRevision)
	if options.Value == nil || options.Value.GetValue() == nil || strings.TrimSpace(options.Value.GetSchemaId()) == "" ||
		strings.TrimSpace(options.Value.GetSchemaVersion()) == "" || options.TTL < hostCacheMinTTL ||
		options.TTL > hostCacheMaxTTL || len(options.Tags) > hostCacheTagsMax ||
		(options.ExpectedRevision != "" && !validCacheOpaqueToken(options.ExpectedRevision)) {
		return CacheSetAndReleaseOptions{}, ErrHostCacheInvalidArgument
	}
	options.Value = cloneTypedDocument(options.Value)
	options.Tags = append([]string(nil), options.Tags...)
	return options, nil
}

func normalizeCacheRememberOptions(options CacheRememberOptions) (CacheRememberOptions, string, string, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheRememberOptions{}, "", "", err
	}
	options.Namespace = namespace
	options.Parent = cloneRequestContext(options.Parent)
	options.Tags = append([]string(nil), options.Tags...)
	options.ExpectedRevision = strings.TrimSpace(options.ExpectedRevision)
	if options.TTL == 0 {
		options.TTL = DefaultHostCacheTTL
	}
	if options.LockTTL == 0 {
		options.LockTTL = DefaultHostCacheLockTTL
	}
	if options.Wait == 0 {
		options.Wait = DefaultHostCacheRememberWait
	}
	if options.Backoff == 0 {
		options.Backoff = DefaultHostCacheRememberBackoff
	}
	schemaID, schemaVersion, ok := SplitSchemaRef(options.ValueSchema)
	if !ok || len(options.Tags) > hostCacheTagsMax || options.TTL < hostCacheMinTTL || options.TTL > hostCacheMaxTTL ||
		options.LockTTL < hostCacheMinLockTTL || options.LockTTL > hostCacheMaxLockTTL ||
		options.Wait <= 0 || options.Wait > hostCacheMaxRememberWait ||
		options.Backoff < hostCacheMinBackoff || options.Backoff > hostCacheMaxBackoff ||
		(options.ExpectedRevision != "" && !validCacheOpaqueToken(options.ExpectedRevision)) {
		return CacheRememberOptions{}, "", "", ErrHostCacheInvalidArgument
	}
	return options, schemaID, schemaVersion, nil
}

func normalizeCacheNamespaceKey(namespace string, key string) (string, error) {
	namespace, err := normalizeCacheNamespace(namespace)
	if err != nil || key == "" || len(key) > hostCacheKeyMaxBytes || !utf8.ValidString(key) {
		return "", ErrHostCacheInvalidArgument
	}
	for _, value := range key {
		if value < 0x20 || value == 0x7f {
			return "", ErrHostCacheInvalidArgument
		}
	}
	return namespace, nil
}

func normalizeCacheNamespace(namespace string) (string, error) {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	if namespace == "" || len(namespace) > hostCacheNamespaceMax || !utf8.ValidString(namespace) {
		return "", ErrHostCacheInvalidArgument
	}
	for _, value := range namespace {
		if value < 0x20 || value == 0x7f {
			return "", ErrHostCacheInvalidArgument
		}
	}
	return namespace, nil
}

func normalizeCacheLockTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		ttl = DefaultHostCacheLockTTL
	}
	if ttl < hostCacheMinLockTTL || ttl > hostCacheMaxLockTTL {
		return 0, ErrHostCacheInvalidArgument
	}
	return ttl, nil
}

func nextCacheRememberBackoff(current time.Duration) time.Duration {
	ceiling := max(hostCacheBackoffSoftCeiling, current)
	ceiling = min(ceiling, hostCacheMaxBackoff)
	if current >= ceiling {
		return ceiling
	}
	return min(current*2, ceiling)
}

func (h *Host) cacheAvailable() bool {
	return h != nil && h.Cache != nil && h.identity != nil &&
		strings.TrimSpace(h.identity.GetExtensionId()) != "" &&
		strings.TrimSpace(h.identity.GetExtensionVersion()) != "" &&
		strings.TrimSpace(h.identity.GetArtifactDigest()) != "" &&
		strings.TrimSpace(h.identity.GetInstanceId()) != ""
}

func (h *Host) validCacheResponseContext(
	request *protocolwire.RequestContext,
	response *protocolwire.ResponseContext,
) bool {
	return h != nil && request != nil && response != nil &&
		response.GetRequestId() == request.GetRequestId() && proto.Equal(response.GetExtension(), h.identity)
}

func cacheDocumentMatches(document *protocolwire.TypedDocument, schemaID, schemaVersion string) bool {
	return document != nil && document.GetValue() != nil &&
		document.GetSchemaId() == schemaID && document.GetSchemaVersion() == schemaVersion
}

func validCacheOpaqueToken(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hostCacheErrorFromDetail(detail *protocolwire.ErrorDetail) error {
	if detail == nil || detail.GetCode() == protocolwire.ErrorCode_ERROR_CODE_UNSPECIFIED {
		return ErrHostCacheResponseInvalid
	}
	return &HostCacheError{
		Code: detail.GetCode(), Reason: detail.GetReason(), Retryable: detail.GetRetryable(),
	}
}
