package pluginv2

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sdkMemoryCacheClient struct {
	hostwire.CacheServiceClient

	mu sync.Mutex

	locked                bool
	token                 string
	expiresAt             time.Time
	found                 bool
	value                 *protocolwire.TypedDocument
	revision              string
	publishOnAcquire      *protocolwire.TypedDocument
	renewError            *protocolwire.ErrorDetail
	forgeResponseIdentity bool
	cleanupBounded        bool
	getDelayAfter         int
	getDelay              time.Duration
	acquireDelay          time.Duration
	renewDelay            time.Duration
	setDelay              time.Duration
	renewStarted          chan struct{}
	counter               int64
	tokenCounter          uint64
	revisionCounter       uint64

	getCalls        int
	directSetCalls  int
	deleteCalls     int
	incrementCalls  int
	invalidateCalls int
	acquireCalls    int
	renewCalls      int
	releaseCalls    int
	setCalls        int

	lastGet        *hostwire.CacheGetRequest
	lastDirectSet  *hostwire.CacheSetRequest
	lastDelete     *hostwire.CacheDeleteRequest
	lastIncrement  *hostwire.CacheIncrementRequest
	lastInvalidate *hostwire.CacheInvalidateRequest
	lastAcquire    *hostwire.CacheLockAcquireRequest
	lastRenew      *hostwire.CacheLockRenewRequest
	lastRelease    *hostwire.CacheLockReleaseRequest
	lastSet        *hostwire.CacheSetAndReleaseLockRequest
}

func newSDKMemoryCacheClient() *sdkMemoryCacheClient {
	return &sdkMemoryCacheClient{revision: strings.Repeat("d", 64), revisionCounter: 16}
}

func (c *sdkMemoryCacheClient) Get(
	ctx context.Context,
	request *hostwire.CacheGetRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheGetResponse, error) {
	c.mu.Lock()
	c.getCalls++
	call := c.getCalls
	c.lastGet = proto.Clone(request).(*hostwire.CacheGetRequest)
	delay, delayAfter := c.getDelay, c.getDelayAfter
	c.mu.Unlock()
	if call > delayAfter {
		if err := waitSDKCacheClient(ctx, delay); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheGetResponse{Context: c.responseContext(request.GetContext())}
	if c.found {
		response.Found = true
		response.Value = cloneTypedDocument(c.value)
		response.Revision = c.revision
	}
	return response, nil
}

func (c *sdkMemoryCacheClient) Set(
	ctx context.Context,
	request *hostwire.CacheSetRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheSetResponse, error) {
	c.mu.Lock()
	c.directSetCalls++
	c.lastDirectSet = proto.Clone(request).(*hostwire.CacheSetRequest)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheSetResponse{Context: c.responseContext(request.GetContext())}
	if c.revisionConflict(request.GetExpectedRevision()) {
		response.Error = sdkCacheRevisionConflictError()
		return response, nil
	}
	c.value = cloneTypedDocument(request.GetValue())
	c.found = true
	c.revision = c.nextRevisionLocked()
	response.Revision = c.revision
	return response, nil
}

func (c *sdkMemoryCacheClient) Delete(
	ctx context.Context,
	request *hostwire.CacheDeleteRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheDeleteResponse, error) {
	c.mu.Lock()
	c.deleteCalls++
	c.lastDelete = proto.Clone(request).(*hostwire.CacheDeleteRequest)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deleted := c.found
	c.found = false
	c.value = nil
	c.revision = ""
	return &hostwire.CacheDeleteResponse{
		Context: c.responseContext(request.GetContext()), Deleted: deleted,
	}, nil
}

func (c *sdkMemoryCacheClient) Increment(
	ctx context.Context,
	request *hostwire.CacheIncrementRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheIncrementResponse, error) {
	c.mu.Lock()
	c.incrementCalls++
	c.lastIncrement = proto.Clone(request).(*hostwire.CacheIncrementRequest)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter += request.GetDelta()
	return &hostwire.CacheIncrementResponse{
		Context: c.responseContext(request.GetContext()), Value: c.counter,
	}, nil
}

func (c *sdkMemoryCacheClient) InvalidateTags(
	ctx context.Context,
	request *hostwire.CacheInvalidateRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheInvalidateResponse, error) {
	c.mu.Lock()
	c.invalidateCalls++
	c.lastInvalidate = proto.Clone(request).(*hostwire.CacheInvalidateRequest)
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var invalidated uint64
	if c.found {
		invalidated = 1
	}
	c.found = false
	c.value = nil
	c.revision = ""
	return &hostwire.CacheInvalidateResponse{
		Context: c.responseContext(request.GetContext()), InvalidatedEntries: invalidated,
	}, nil
}

func (c *sdkMemoryCacheClient) AcquireLock(
	ctx context.Context,
	request *hostwire.CacheLockAcquireRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheLockAcquireResponse, error) {
	c.mu.Lock()
	c.acquireCalls++
	c.lastAcquire = proto.Clone(request).(*hostwire.CacheLockAcquireRequest)
	delay := c.acquireDelay
	c.mu.Unlock()
	if err := waitSDKCacheClient(ctx, delay); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheLockAcquireResponse{Context: c.responseContext(request.GetContext())}
	if c.locked && !c.expiresAt.IsZero() && !time.Now().Before(c.expiresAt) {
		c.locked = false
	}
	if c.locked {
		return response, nil
	}
	c.locked = true
	c.tokenCounter++
	c.token = fmt.Sprintf("%064x", c.tokenCounter)
	c.expiresAt = time.Now().Add(request.GetTtl().AsDuration())
	if c.publishOnAcquire != nil {
		c.found = true
		c.value = cloneTypedDocument(c.publishOnAcquire)
	}
	response.Acquired = true
	response.LeaseToken = c.token
	response.ExpiresAt = timestamppb.New(c.expiresAt)
	return response, nil
}

func (c *sdkMemoryCacheClient) RenewLock(
	ctx context.Context,
	request *hostwire.CacheLockRenewRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheLockRenewResponse, error) {
	c.mu.Lock()
	c.renewCalls++
	c.lastRenew = proto.Clone(request).(*hostwire.CacheLockRenewRequest)
	delay, started := c.renewDelay, c.renewStarted
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if err := waitSDKCacheClient(ctx, delay); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheLockRenewResponse{Context: c.responseContext(request.GetContext())}
	if c.renewError != nil {
		response.Error = proto.Clone(c.renewError).(*protocolwire.ErrorDetail)
		c.locked = false
		return response, nil
	}
	if !c.locked || request.GetLeaseToken() != c.token || !time.Now().Before(c.expiresAt) {
		response.Error = sdkCacheLockNotOwnedError()
		if request.GetLeaseToken() == c.token {
			c.locked = false
		}
		return response, nil
	}
	c.expiresAt = time.Now().Add(request.GetTtl().AsDuration())
	response.Renewed = true
	response.ExpiresAt = timestamppb.New(c.expiresAt)
	return response, nil
}

func (c *sdkMemoryCacheClient) ReleaseLock(
	ctx context.Context,
	request *hostwire.CacheLockReleaseRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheLockReleaseResponse, error) {
	c.mu.Lock()
	c.releaseCalls++
	c.lastRelease = proto.Clone(request).(*hostwire.CacheLockReleaseRequest)
	if deadline, ok := ctx.Deadline(); ok && ctx.Err() == nil {
		remaining := time.Until(deadline)
		c.cleanupBounded = remaining > 0 && remaining <= time.Second
	}
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheLockReleaseResponse{Context: c.responseContext(request.GetContext())}
	if !c.locked || request.GetLeaseToken() != c.token {
		response.Error = sdkCacheLockNotOwnedError()
		return response, nil
	}
	c.locked = false
	response.Released = true
	return response, nil
}

func (c *sdkMemoryCacheClient) SetAndReleaseLock(
	ctx context.Context,
	request *hostwire.CacheSetAndReleaseLockRequest,
	_ ...grpc.CallOption,
) (*hostwire.CacheSetAndReleaseLockResponse, error) {
	c.mu.Lock()
	c.setCalls++
	c.lastSet = proto.Clone(request).(*hostwire.CacheSetAndReleaseLockRequest)
	delay := c.setDelay
	c.mu.Unlock()
	if err := waitSDKCacheClient(ctx, delay); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	response := &hostwire.CacheSetAndReleaseLockResponse{Context: c.responseContext(request.GetContext())}
	if !c.locked || request.GetLeaseToken() != c.token || !time.Now().Before(c.expiresAt) {
		response.Error = sdkCacheLockNotOwnedError()
		if request.GetLeaseToken() == c.token {
			c.locked = false
		}
		return response, nil
	}
	if c.revisionConflict(request.GetExpectedRevision()) {
		c.locked = false
		response.Error = sdkCacheRevisionConflictError()
		return response, nil
	}
	c.value = cloneTypedDocument(request.GetValue())
	c.found = true
	c.locked = false
	c.revision = c.nextRevisionLocked()
	response.Revision = c.revision
	return response, nil
}

func waitSDKCacheClient(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *sdkMemoryCacheClient) responseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	response := &protocolwire.ResponseContext{RequestId: request.GetRequestId(), Extension: cloneIdentity(request.GetExtension())}
	if c.forgeResponseIdentity {
		response.Extension.InstanceId = "forged-instance"
	}
	return response
}

func (c *sdkMemoryCacheClient) revisionConflict(expected string) bool {
	expected = strings.TrimSpace(expected)
	return expected != "" && (!c.found || expected != c.revision)
}

func (c *sdkMemoryCacheClient) nextRevisionLocked() string {
	c.revisionCounter++
	return fmt.Sprintf("%064x", c.revisionCounter)
}

func newSDKCacheHost(client hostwire.CacheServiceClient) *Host {
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.cache", ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("c", 64),
		TrustGrantId: "grant-cache", RuntimeEpoch: 9, InstanceId: "runtime-cache-9",
	}
	return &Host{Cache: client, identity: identity, instance: identity.GetInstanceId()}
}

func sdkCacheDocument(t *testing.T, values map[string]any) *protocolwire.TypedDocument {
	t.Helper()
	document, err := NewTypedDocument(sdkCacheSchemaRef, values)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func sdkCacheLockNotOwnedError() *protocolwire.ErrorDetail {
	return &protocolwire.ErrorDetail{
		Code:   protocolwire.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
		Reason: "host.cache_lock_not_owned", Message: "The cache lock is not owned.",
	}
}

func sdkCacheRevisionConflictError() *protocolwire.ErrorDetail {
	return &protocolwire.ErrorDetail{
		Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "host.cache_revision_conflict",
		Message: "The cached value changed before this write.",
	}
}

var _ hostwire.CacheServiceClient = (*sdkMemoryCacheClient)(nil)
