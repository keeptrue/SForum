package plugin

import (
	pluginsdkstorage "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
)

// Storage provider aliases preserve the root SDK API while new Protocol V2
// plugins can import sdk/plugin/storageprovider without pulling in the legacy
// Host SDK.
type (
	StorageObjectInfo             = pluginsdkstorage.StorageObjectInfo
	StorageBackend                = pluginsdkstorage.StorageBackend
	StorageBackendFactory         = pluginsdkstorage.StorageBackendFactory
	StorageInstanceBackendFactory = pluginsdkstorage.StorageInstanceBackendFactory
	StorageProviderV2             = pluginsdkstorage.StorageProviderV2
)

const StorageProviderSlot = pluginsdkstorage.StorageProviderSlot

// StorageProvider keeps the legacy Protocol surface source-compatible and
// delegates all storage behavior to the lightweight implementation.
type StorageProvider struct {
	Noop
	impl *pluginsdkstorage.StorageProvider
}

var _ Protocol = (*StorageProvider)(nil)

func NewStorageProvider(reasonPrefix string, factory StorageBackendFactory) *StorageProvider {
	return &StorageProvider{impl: pluginsdkstorage.NewStorageProvider(reasonPrefix, factory)}
}

func NewMultiStorageProvider(reasonPrefix string, factory StorageInstanceBackendFactory) *StorageProvider {
	return &StorageProvider{impl: pluginsdkstorage.NewMultiStorageProvider(reasonPrefix, factory)}
}

func NewStorageProviderV2(provider *StorageProvider) *StorageProviderV2 {
	if provider == nil {
		return pluginsdkstorage.NewStorageProviderV2(nil)
	}
	return pluginsdkstorage.NewStorageProviderV2(provider.impl)
}

func (p *StorageProvider) Health() (Health, error) {
	return Health{OK: true}, nil
}

func (p *StorageProvider) RouteTarget() (RouteTarget, error) {
	return RouteTarget{}, nil
}

func (p *StorageProvider) StorageProbe(request StorageProbeRequest) (StorageProbeResponse, error) {
	response, err := p.impl.StorageProbe(pluginsdkstorage.StorageProbeRequest(request))
	return StorageProbeResponse(response), err
}

func (p *StorageProvider) StoragePutBegin(request StoragePutBeginRequest) (StorageSessionResponse, error) {
	response, err := p.impl.StoragePutBegin(pluginsdkstorage.StoragePutBeginRequest(request))
	return StorageSessionResponse(response), err
}

func (p *StorageProvider) StoragePutChunk(request StoragePutChunkRequest) (StorageResult, error) {
	response, err := p.impl.StoragePutChunk(pluginsdkstorage.StoragePutChunkRequest(request))
	return StorageResult(response), err
}

func (p *StorageProvider) StorageOpen(request StorageOpenRequest) (StorageSessionResponse, error) {
	response, err := p.impl.StorageOpen(pluginsdkstorage.StorageOpenRequest(request))
	return StorageSessionResponse(response), err
}

func (p *StorageProvider) StorageGetChunk(request StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	response, err := p.impl.StorageGetChunk(pluginsdkstorage.StorageGetChunkRequest(request))
	return StorageGetChunkResponse(response), err
}

func (p *StorageProvider) StorageClose(request StorageCloseRequest) (StorageResult, error) {
	response, err := p.impl.StorageClose(pluginsdkstorage.StorageCloseRequest(request))
	return StorageResult(response), err
}

func (p *StorageProvider) StorageDelete(request StorageObjectRequest) (StorageResult, error) {
	response, err := p.impl.StorageDelete(pluginsdkstorage.StorageObjectRequest(request))
	return StorageResult(response), err
}

func (p *StorageProvider) StorageStat(request StorageStatRequest) (StorageStatResponse, error) {
	response, err := p.impl.StorageStat(pluginsdkstorage.StorageStatRequest(request))
	return StorageStatResponse(response), err
}

func (p *StorageProvider) StorageExists(request StorageExistsRequest) (StorageExistsResponse, error) {
	response, err := p.impl.StorageExists(pluginsdkstorage.StorageExistsRequest(request))
	return StorageExistsResponse(response), err
}

func (p *StorageProvider) StoragePublicURL(request StoragePublicURLRequest) (StorageURLResponse, error) {
	response, err := p.impl.StoragePublicURL(pluginsdkstorage.StoragePublicURLRequest(request))
	return StorageURLResponse(response), err
}

func (p *StorageProvider) StorageSignedURL(request StorageSignedURLRequest) (StorageURLResponse, error) {
	response, err := p.impl.StorageSignedURL(pluginsdkstorage.StorageSignedURLRequest(request))
	return StorageURLResponse(response), err
}

func (p *StorageProvider) StorageConfigureInstance(request StorageConfigureInstanceRequest) (StorageResult, error) {
	response, err := p.impl.StorageConfigureInstance(pluginsdkstorage.StorageConfigureInstanceRequest(request))
	return StorageResult(response), err
}

func (p *StorageProvider) StorageRemoveInstance(request StorageRemoveInstanceRequest) (StorageResult, error) {
	response, err := p.impl.StorageRemoveInstance(pluginsdkstorage.StorageRemoveInstanceRequest(request))
	return StorageResult(response), err
}

func (p *StorageProvider) StorageProbeConfig(request StorageProbeConfigRequest) (StorageProbeResponse, error) {
	response, err := p.impl.StorageProbeConfig(pluginsdkstorage.StorageProbeConfigRequest(request))
	return StorageProbeResponse(response), err
}

func ValidateStorageObjectKey(key string) error {
	return pluginsdkstorage.ValidateStorageObjectKey(key)
}

func JoinStorageRemotePath(root, key string) (string, error) {
	return pluginsdkstorage.JoinStorageRemotePath(root, key)
}

func JoinStoragePublicURL(base, key string) string {
	return pluginsdkstorage.JoinStoragePublicURL(base, key)
}
