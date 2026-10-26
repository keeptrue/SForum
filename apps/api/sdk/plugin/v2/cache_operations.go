package pluginv2

import (
	"context"
	"strings"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/durationpb"
)

// CacheGetOptions identifies one typed value in a declared cache namespace.
type CacheGetOptions struct {
	Parent      *protocolwire.RequestContext
	Namespace   string
	Key         string
	ValueSchema string
}

// CacheGetResult includes the opaque revision required for conditional writes.
type CacheGetResult struct {
	Found    bool
	Value    *protocolwire.TypedDocument
	Revision string
}

// CacheSetOptions writes one typed value with an optional revision precondition.
type CacheSetOptions struct {
	Parent           *protocolwire.RequestContext
	Namespace        string
	Key              string
	Value            *protocolwire.TypedDocument
	TTL              time.Duration
	Tags             []string
	ExpectedRevision string
}

type CacheDeleteOptions struct {
	Parent    *protocolwire.RequestContext
	Namespace string
	Key       string
}

type CacheIncrementOptions struct {
	Parent    *protocolwire.RequestContext
	Namespace string
	Key       string
	Delta     int64
	TTL       time.Duration
}

type CacheInvalidateTagsOptions struct {
	Parent    *protocolwire.RequestContext
	Namespace string
	Tags      []string
}

// GetCache reads one typed value and its revision through the runtime-scoped Host broker.
func (h *Host) GetCache(ctx context.Context, options CacheGetOptions) (CacheGetResult, error) {
	if !h.cacheAvailable() {
		return CacheGetResult{}, ErrHostUnavailable
	}
	if ctx == nil {
		return CacheGetResult{}, ErrHostCacheInvalidArgument
	}
	options, schemaID, schemaVersion, err := normalizeCacheGetOptions(options)
	if err != nil {
		return CacheGetResult{}, err
	}
	return h.getCacheValue(ctx, options.Parent, options.Namespace, options.Key, schemaID, schemaVersion)
}

// SetCache writes one typed value and returns its new opaque revision.
func (h *Host) SetCache(ctx context.Context, options CacheSetOptions) (string, error) {
	if !h.cacheAvailable() {
		return "", ErrHostUnavailable
	}
	if ctx == nil {
		return "", ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheSetOptions(options)
	if err != nil {
		return "", err
	}
	requestContext := h.RequestContext(options.Parent)
	response, err := h.Cache.Set(ctx, &hostwire.CacheSetRequest{
		Context: requestContext, Namespace: options.Namespace, Key: options.Key,
		Value: cloneTypedDocument(options.Value), Ttl: durationpb.New(options.TTL),
		Tags: append([]string(nil), options.Tags...), ExpectedRevision: options.ExpectedRevision,
	})
	if err != nil {
		return "", err
	}
	if response == nil || !h.validCacheResponseContext(requestContext, response.GetContext()) {
		return "", ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return "", hostCacheErrorFromDetail(response.GetError())
	}
	revision := strings.TrimSpace(response.GetRevision())
	if !validCacheOpaqueToken(revision) {
		return "", ErrHostCacheResponseInvalid
	}
	return revision, nil
}

// DeleteCache removes one logical key from its declared namespace.
func (h *Host) DeleteCache(ctx context.Context, options CacheDeleteOptions) (bool, error) {
	if !h.cacheAvailable() {
		return false, ErrHostUnavailable
	}
	if ctx == nil {
		return false, ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheDeleteOptions(options)
	if err != nil {
		return false, err
	}
	requestContext := h.RequestContext(options.Parent)
	response, err := h.Cache.Delete(ctx, &hostwire.CacheDeleteRequest{
		Context: requestContext, Namespace: options.Namespace, Key: options.Key,
	})
	if err != nil {
		return false, err
	}
	if response == nil || !h.validCacheResponseContext(requestContext, response.GetContext()) {
		return false, ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return false, hostCacheErrorFromDetail(response.GetError())
	}
	return response.GetDeleted(), nil
}

// IncrementCache atomically adjusts one counter. A zero delta uses the Host default of one.
func (h *Host) IncrementCache(ctx context.Context, options CacheIncrementOptions) (int64, error) {
	if !h.cacheAvailable() {
		return 0, ErrHostUnavailable
	}
	if ctx == nil {
		return 0, ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheIncrementOptions(options)
	if err != nil {
		return 0, err
	}
	requestContext := h.RequestContext(options.Parent)
	response, err := h.Cache.Increment(ctx, &hostwire.CacheIncrementRequest{
		Context: requestContext, Namespace: options.Namespace, Key: options.Key,
		Delta: options.Delta, Ttl: durationpb.New(options.TTL),
	})
	if err != nil {
		return 0, err
	}
	if response == nil || !h.validCacheResponseContext(requestContext, response.GetContext()) {
		return 0, ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return 0, hostCacheErrorFromDetail(response.GetError())
	}
	return response.GetValue(), nil
}

// InvalidateCacheTags evicts all entries associated with declared logical tags.
func (h *Host) InvalidateCacheTags(ctx context.Context, options CacheInvalidateTagsOptions) (uint64, error) {
	if !h.cacheAvailable() {
		return 0, ErrHostUnavailable
	}
	if ctx == nil {
		return 0, ErrHostCacheInvalidArgument
	}
	options, err := normalizeCacheInvalidateTagsOptions(options)
	if err != nil {
		return 0, err
	}
	requestContext := h.RequestContext(options.Parent)
	response, err := h.Cache.InvalidateTags(ctx, &hostwire.CacheInvalidateRequest{
		Context: requestContext, Namespace: options.Namespace, Tags: append([]string(nil), options.Tags...),
	})
	if err != nil {
		return 0, err
	}
	if response == nil || !h.validCacheResponseContext(requestContext, response.GetContext()) {
		return 0, ErrHostCacheResponseInvalid
	}
	if response.GetError() != nil {
		return 0, hostCacheErrorFromDetail(response.GetError())
	}
	return response.GetInvalidatedEntries(), nil
}

func normalizeCacheGetOptions(options CacheGetOptions) (CacheGetOptions, string, string, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheGetOptions{}, "", "", err
	}
	schemaID, schemaVersion, ok := SplitSchemaRef(options.ValueSchema)
	if !ok {
		return CacheGetOptions{}, "", "", ErrHostCacheInvalidArgument
	}
	options.Parent = cloneRequestContext(options.Parent)
	options.Namespace = namespace
	options.ValueSchema = schemaID + "@" + schemaVersion
	return options, schemaID, schemaVersion, nil
}

func normalizeCacheSetOptions(options CacheSetOptions) (CacheSetOptions, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheSetOptions{}, err
	}
	value, err := normalizeCacheSetAndReleaseOptions(CacheSetAndReleaseOptions{
		Value: options.Value, TTL: options.TTL, Tags: options.Tags, ExpectedRevision: options.ExpectedRevision,
	})
	if err != nil {
		return CacheSetOptions{}, err
	}
	options.Parent = cloneRequestContext(options.Parent)
	options.Namespace = namespace
	options.Value = value.Value
	options.TTL = value.TTL
	options.Tags = value.Tags
	options.ExpectedRevision = value.ExpectedRevision
	return options, nil
}

func normalizeCacheDeleteOptions(options CacheDeleteOptions) (CacheDeleteOptions, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheDeleteOptions{}, err
	}
	options.Parent = cloneRequestContext(options.Parent)
	options.Namespace = namespace
	return options, nil
}

func normalizeCacheIncrementOptions(options CacheIncrementOptions) (CacheIncrementOptions, error) {
	namespace, err := normalizeCacheNamespaceKey(options.Namespace, options.Key)
	if err != nil {
		return CacheIncrementOptions{}, err
	}
	if options.TTL == 0 {
		options.TTL = DefaultHostCacheTTL
	}
	if options.Delta == 0 {
		options.Delta = 1
	}
	if options.TTL < hostCacheMinTTL || options.TTL > hostCacheMaxTTL ||
		options.Delta < -1_000_000 || options.Delta > 1_000_000 {
		return CacheIncrementOptions{}, ErrHostCacheInvalidArgument
	}
	options.Parent = cloneRequestContext(options.Parent)
	options.Namespace = namespace
	return options, nil
}

func normalizeCacheInvalidateTagsOptions(options CacheInvalidateTagsOptions) (CacheInvalidateTagsOptions, error) {
	namespace, err := normalizeCacheNamespace(options.Namespace)
	if err != nil || len(options.Tags) == 0 || len(options.Tags) > hostCacheTagsMax {
		return CacheInvalidateTagsOptions{}, ErrHostCacheInvalidArgument
	}
	options.Parent = cloneRequestContext(options.Parent)
	options.Namespace = namespace
	options.Tags = append([]string(nil), options.Tags...)
	return options, nil
}
