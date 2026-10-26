package storageprovider

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	StorageProviderSlot            = "attachment.storage.provider"
	storageProviderContractVersion = "1"
	storageProviderSchemaPrefix    = "sforum.provider.attachment.storage.provider."
	storageProviderChunkLimit      = 1 << 20
)

// Protocol V2 storage payloads are owned by this lightweight SDK package.
// Their field layout matches the Host's legacy storage contract exactly.
type StorageResult struct {
	OK      bool
	Reason  string
	Message string
}

type StoragePutBeginRequest struct {
	InstanceID  string
	Key         string
	ContentType string
	Size        int64
}

type StorageSessionResponse struct {
	OK          bool
	SessionID   string
	Size        int64
	ContentType string
	Reason      string
	Message     string
}

type StoragePutChunkRequest struct {
	SessionID string
	Data      []byte
	Final     bool
}

type StorageOpenRequest struct {
	InstanceID string
	Key        string
}

type StorageGetChunkRequest struct {
	SessionID string
	MaxBytes  int
}

type StorageGetChunkResponse struct {
	OK      bool
	Data    []byte
	EOF     bool
	Reason  string
	Message string
}

type StorageCloseRequest struct {
	SessionID string
}

type StorageObjectRequest struct {
	InstanceID string
	Key        string
}

type StorageStatRequest struct {
	InstanceID string
	Key        string
}

type StorageStatResponse struct {
	OK           bool
	Exists       bool
	Size         int64
	ContentType  string
	ModifiedUnix int64
	Reason       string
	Message      string
}

type StorageExistsRequest struct {
	InstanceID string
	Key        string
}

type StorageExistsResponse struct {
	OK      bool
	Exists  bool
	Reason  string
	Message string
}

type StoragePublicURLRequest struct {
	InstanceID string
	Key        string
}

type StorageSignedURLRequest struct {
	InstanceID string
	Key        string
	TTLSeconds int64
}

type StorageURLResponse struct {
	OK      bool
	URL     string
	Reason  string
	Message string
}

type StorageProbeRequest struct {
	InstanceID string
}

type StorageProbeResponse struct {
	OK      bool
	Reason  string
	Message string
}

type StorageConfigureInstanceRequest struct {
	InstanceID string
	Settings   map[string]string
}

type StorageRemoveInstanceRequest struct {
	InstanceID string
}

type StorageProbeConfigRequest struct {
	Settings map[string]string
}

// StorageObjectInfo 是远程 provider 对象元数据的最小表示。
type StorageObjectInfo struct {
	Exists       bool
	Size         int64
	ContentType  string
	ModifiedUnix int64
}

// StorageBackend 只表达具体介质的对象操作。分块 RPC、临时写入与会话生命周期
// 由 StorageProvider 统一实现，插件无需复制协议胶水。
type StorageBackend interface {
	Probe() error
	Put(key, contentType string, reader io.Reader) error
	Open(key string) (io.ReadCloser, error)
	Delete(key string) error
	Stat(key string) (StorageObjectInfo, error)
	Exists(key string) (bool, error)
	PublicURL(key string) string
	SignedURL(key string, ttl time.Duration) (string, error)
}

type StorageBackendFactory func() (StorageBackend, error)

type StorageInstanceBackendFactory func(settings map[string]string) (StorageBackend, error)

// StorageProvider 将一个 StorageBackend 暴露为 attachment.storage.provider。
// 远程写入先落在短生命周期临时文件，避免把整个对象放进 RPC 或进程内存。
type StorageProvider struct {
	mu              sync.Mutex
	factory         StorageBackendFactory
	instanceFactory StorageInstanceBackendFactory
	backends        map[string]StorageBackend
	reasonPrefix    string
	puts            map[string]*storagePutSession
	reads           map[string]io.ReadCloser
}

type storagePutSession struct {
	key         string
	instanceID  string
	contentType string
	size        int64
	written     int64
	tempPath    string
	file        *os.File
}

func NewStorageProvider(reasonPrefix string, factory StorageBackendFactory) *StorageProvider {
	return &StorageProvider{
		factory:      factory,
		reasonPrefix: strings.Trim(strings.TrimSpace(reasonPrefix), "."),
		puts:         map[string]*storagePutSession{},
		reads:        map[string]io.ReadCloser{},
		backends:     map[string]StorageBackend{},
	}
}

// NewMultiStorageProvider creates a provider whose concrete backends are
// configured by Host-owned instance documents at runtime.
func NewMultiStorageProvider(reasonPrefix string, factory StorageInstanceBackendFactory) *StorageProvider {
	p := NewStorageProvider(reasonPrefix, nil)
	p.instanceFactory = factory
	return p
}

func (p *StorageProvider) StorageProbe(req StorageProbeRequest) (StorageProbeResponse, error) {
	backend, err := p.backend(req.InstanceID)
	if err != nil {
		return StorageProbeResponse{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	if err := backend.Probe(); err != nil {
		return StorageProbeResponse{Reason: p.reason("probe"), Message: err.Error()}, nil
	}
	return StorageProbeResponse{OK: true, Reason: "storage.ok", Message: "ok"}, nil
}

func (p *StorageProvider) StoragePutBegin(req StoragePutBeginRequest) (StorageSessionResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageSessionResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	if req.Size < 0 {
		return StorageSessionResponse{Reason: p.reason("invalid_size"), Message: "object size must not be negative"}, nil
	}
	if _, err := p.backend(req.InstanceID); err != nil {
		return StorageSessionResponse{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	file, err := os.CreateTemp("", "sforum-storage-put-*")
	if err != nil {
		return StorageSessionResponse{Reason: p.reason("temporary_file"), Message: err.Error()}, nil
	}
	id := storageProviderSessionID()
	p.mu.Lock()
	p.puts[id] = &storagePutSession{instanceID: req.InstanceID, key: req.Key, contentType: req.ContentType, size: req.Size, tempPath: file.Name(), file: file}
	p.mu.Unlock()
	return StorageSessionResponse{OK: true, SessionID: id}, nil
}

func (p *StorageProvider) StoragePutChunk(req StoragePutChunkRequest) (StorageResult, error) {
	p.mu.Lock()
	session := p.puts[req.SessionID]
	p.mu.Unlock()
	if session == nil || session.file == nil {
		return StorageResult{Reason: p.reason("session_missing"), Message: "put session not found"}, nil
	}
	if session.written+int64(len(req.Data)) > session.size {
		p.abortPut(req.SessionID)
		return StorageResult{Reason: p.reason("size_mismatch"), Message: "received more bytes than declared"}, nil
	}
	if len(req.Data) > 0 {
		n, err := session.file.Write(req.Data)
		session.written += int64(n)
		if err != nil {
			p.abortPut(req.SessionID)
			return StorageResult{Reason: p.reason("temporary_write"), Message: err.Error()}, nil
		}
	}
	if !req.Final {
		return StorageResult{OK: true}, nil
	}
	if session.written != session.size {
		p.abortPut(req.SessionID)
		return StorageResult{Reason: p.reason("size_mismatch"), Message: "received bytes do not match declared size"}, nil
	}
	if err := session.file.Close(); err != nil {
		p.abortPut(req.SessionID)
		return StorageResult{Reason: p.reason("temporary_close"), Message: err.Error()}, nil
	}
	session.file = nil
	reader, err := os.Open(sessionTempPath(session))
	if err != nil {
		p.abortPut(req.SessionID)
		return StorageResult{Reason: p.reason("temporary_open"), Message: err.Error()}, nil
	}
	backend, err := p.backend(session.instanceID)
	if err == nil {
		err = backend.Put(session.key, session.contentType, reader)
	}
	closeErr := reader.Close()
	p.removePut(req.SessionID)
	if err != nil {
		return StorageResult{Reason: p.reason("put"), Message: err.Error()}, nil
	}
	if closeErr != nil {
		return StorageResult{Reason: p.reason("temporary_close"), Message: closeErr.Error()}, nil
	}
	return StorageResult{OK: true}, nil
}

func (p *StorageProvider) StorageOpen(req StorageOpenRequest) (StorageSessionResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageSessionResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err != nil {
		return StorageSessionResponse{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	reader, err := backend.Open(req.Key)
	if err != nil {
		return StorageSessionResponse{Reason: p.reason("open"), Message: err.Error()}, nil
	}
	info, _ := backend.Stat(req.Key)
	id := storageProviderSessionID()
	p.mu.Lock()
	p.reads[id] = reader
	p.mu.Unlock()
	return StorageSessionResponse{OK: true, SessionID: id, Size: info.Size, ContentType: info.ContentType}, nil
}

func (p *StorageProvider) StorageGetChunk(req StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	p.mu.Lock()
	reader := p.reads[req.SessionID]
	p.mu.Unlock()
	if reader == nil {
		return StorageGetChunkResponse{Reason: p.reason("session_missing"), Message: "read session not found"}, nil
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 || maxBytes > storageProviderChunkLimit {
		maxBytes = storageProviderChunkLimit
	}
	buffer := make([]byte, maxBytes)
	n, err := reader.Read(buffer)
	if n > 0 {
		return StorageGetChunkResponse{OK: true, Data: buffer[:n], EOF: err == io.EOF}, nil
	}
	if err == io.EOF {
		return StorageGetChunkResponse{OK: true, EOF: true}, nil
	}
	if err != nil {
		return StorageGetChunkResponse{Reason: p.reason("read"), Message: err.Error()}, nil
	}
	return StorageGetChunkResponse{OK: true, EOF: true}, nil
}

func (p *StorageProvider) StorageClose(req StorageCloseRequest) (StorageResult, error) {
	p.mu.Lock()
	if session := p.puts[req.SessionID]; session != nil {
		delete(p.puts, req.SessionID)
		p.mu.Unlock()
		return StorageResult{OK: true}, closePutSession(session)
	}
	if reader := p.reads[req.SessionID]; reader != nil {
		delete(p.reads, req.SessionID)
		p.mu.Unlock()
		if err := reader.Close(); err != nil {
			return StorageResult{Reason: p.reason("close"), Message: err.Error()}, nil
		}
		return StorageResult{OK: true}, nil
	}
	p.mu.Unlock()
	return StorageResult{OK: true}, nil
}

func (p *StorageProvider) StorageDelete(req StorageObjectRequest) (StorageResult, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageResult{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err == nil {
		err = backend.Delete(req.Key)
	}
	if err != nil {
		return StorageResult{Reason: p.reason("delete"), Message: err.Error()}, nil
	}
	return StorageResult{OK: true}, nil
}

func (p *StorageProvider) StorageStat(req StorageStatRequest) (StorageStatResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageStatResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err == nil {
		var info StorageObjectInfo
		info, err = backend.Stat(req.Key)
		if err == nil {
			return StorageStatResponse{OK: true, Exists: info.Exists, Size: info.Size, ContentType: info.ContentType, ModifiedUnix: info.ModifiedUnix}, nil
		}
	}
	return StorageStatResponse{Reason: p.reason("stat"), Message: err.Error()}, nil
}

func (p *StorageProvider) StorageExists(req StorageExistsRequest) (StorageExistsResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageExistsResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err == nil {
		var exists bool
		exists, err = backend.Exists(req.Key)
		if err == nil {
			return StorageExistsResponse{OK: true, Exists: exists}, nil
		}
	}
	return StorageExistsResponse{Reason: p.reason("exists"), Message: err.Error()}, nil
}

func (p *StorageProvider) StoragePublicURL(req StoragePublicURLRequest) (StorageURLResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageURLResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err != nil {
		return StorageURLResponse{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	return StorageURLResponse{OK: true, URL: backend.PublicURL(req.Key)}, nil
}

func (p *StorageProvider) StorageSignedURL(req StorageSignedURLRequest) (StorageURLResponse, error) {
	if err := ValidateStorageObjectKey(req.Key); err != nil {
		return StorageURLResponse{Reason: p.reason("invalid_key"), Message: err.Error()}, nil
	}
	backend, err := p.backend(req.InstanceID)
	if err != nil {
		return StorageURLResponse{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	url, err := backend.SignedURL(req.Key, ttl)
	if err != nil {
		return StorageURLResponse{Reason: p.reason("signed_url"), Message: err.Error()}, nil
	}
	return StorageURLResponse{OK: true, URL: url}, nil
}

func (p *StorageProvider) backend(instanceID string) (StorageBackend, error) {
	if p == nil {
		return nil, fmt.Errorf("storage backend is not configured")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID != "" {
		p.mu.Lock()
		backend := p.backends[instanceID]
		p.mu.Unlock()
		if backend == nil {
			return nil, fmt.Errorf("storage instance is not configured")
		}
		return backend, nil
	}
	if p.factory == nil {
		return nil, fmt.Errorf("storage backend is not configured")
	}
	return p.factory()
}

func (p *StorageProvider) StorageConfigureInstance(req StorageConfigureInstanceRequest) (StorageResult, error) {
	id := strings.TrimSpace(req.InstanceID)
	if id == "" || p == nil || p.instanceFactory == nil {
		return StorageResult{Reason: p.reason("config"), Message: "storage instance configuration is unsupported"}, nil
	}
	backend, err := p.instanceFactory(cloneStorageSettings(req.Settings))
	if err != nil {
		return StorageResult{Reason: p.reason("config"), Message: err.Error()}, nil
	}
	p.mu.Lock()
	p.backends[id] = backend
	p.mu.Unlock()
	return StorageResult{OK: true}, nil
}

func (p *StorageProvider) StorageRemoveInstance(req StorageRemoveInstanceRequest) (StorageResult, error) {
	id := strings.TrimSpace(req.InstanceID)
	if id == "" {
		return StorageResult{Reason: p.reason("config"), Message: "storage instance id is required"}, nil
	}
	p.mu.Lock()
	delete(p.backends, id)
	p.mu.Unlock()
	return StorageResult{OK: true}, nil
}

func (p *StorageProvider) StorageProbeConfig(req StorageProbeConfigRequest) (StorageProbeResponse, error) {
	if p == nil || p.instanceFactory == nil {
		return StorageProbeResponse{Reason: p.reason("config"), Message: "storage instance configuration is unsupported"}, nil
	}
	backend, err := p.instanceFactory(cloneStorageSettings(req.Settings))
	if err == nil {
		err = backend.Probe()
	}
	if err != nil {
		return StorageProbeResponse{Reason: p.reason("probe"), Message: err.Error()}, nil
	}
	return StorageProbeResponse{OK: true, Reason: "storage.ok", Message: "ok"}, nil
}

func cloneStorageSettings(settings map[string]string) map[string]string {
	out := make(map[string]string, len(settings))
	for key, value := range settings {
		out[key] = value
	}
	return out
}

func (p *StorageProvider) reason(operation string) string {
	if p == nil || p.reasonPrefix == "" {
		return "storage." + operation
	}
	return p.reasonPrefix + "." + operation
}

func (p *StorageProvider) abortPut(id string) {
	p.mu.Lock()
	session := p.puts[id]
	delete(p.puts, id)
	p.mu.Unlock()
	_ = closePutSession(session)
}

func (p *StorageProvider) removePut(id string) {
	p.mu.Lock()
	session := p.puts[id]
	delete(p.puts, id)
	p.mu.Unlock()
	if session != nil && session.file != nil {
		_ = session.file.Close()
	}
	if session != nil {
		_ = os.Remove(sessionTempPath(session))
	}
}

func closePutSession(session *storagePutSession) error {
	if session == nil {
		return nil
	}
	var err error
	if session.file != nil {
		err = session.file.Close()
	}
	if removeErr := os.Remove(sessionTempPath(session)); err == nil && removeErr != nil && !os.IsNotExist(removeErr) {
		err = removeErr
	}
	return err
}

func sessionTempPath(session *storagePutSession) string {
	if session == nil {
		return ""
	}
	return session.tempPath
}

func storageProviderSessionID() string {
	var token [12]byte
	_, _ = rand.Read(token[:])
	return hex.EncodeToString(token[:]) + "-" + time.Now().UTC().Format("150405")
}

// ValidateStorageObjectKey 保持 Host 生成对象键的相对路径约束，避免 provider
// 将绝对路径或 traversal 传入远程根目录。
func ValidateStorageObjectKey(key string) error {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") {
		return fmt.Errorf("invalid object key")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid object key")
		}
	}
	return nil
}

func JoinStorageRemotePath(root, key string) (string, error) {
	if err := ValidateStorageObjectKey(key); err != nil {
		return "", err
	}
	root = strings.TrimSpace(strings.ReplaceAll(root, "\\", "/"))
	if strings.Contains(root, "..") {
		return "", fmt.Errorf("invalid remote root path")
	}
	return path.Join("/", root, key), nil
}

func JoinStoragePublicURL(base, key string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/" + strings.TrimPrefix(strings.ReplaceAll(key, "\\", "/"), "/")
}

// StorageProviderV2 将同一 provider 映射到 Protocol V2 known-slot 调用。
type StorageProviderV2 struct {
	*pluginv2.Server
	impl *StorageProvider
}

func NewStorageProviderV2(impl *StorageProvider) *StorageProviderV2 {
	return &StorageProviderV2{Server: pluginv2.NewServer(), impl: impl}
}

func (p *StorageProviderV2) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: request.GetContext().GetExtension()}}
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.GetSlotId() != StorageProviderSlot {
		response.Error = storageProviderError(protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, "provider.slot_unsupported", "unsupported provider slot")
		return response, nil
	}
	if request.GetContractVersion() != "" && request.GetContractVersion() != storageProviderContractVersion {
		response.Error = storageProviderError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "provider.contract_mismatch", "attachment.storage.provider contract must be version 1")
		return response, nil
	}
	values := pluginv2.TypedDocumentValues(request.GetInput())
	return p.call(response, request.GetOperation(), values)
}

func (p *StorageProviderV2) call(response *pluginwire.ProviderCallResponse, operation string, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	var output map[string]any
	var err error
	switch operation {
	case "probe":
		result, callErr := p.impl.StorageProbe(StorageProbeRequest{InstanceID: storageString(values, "instanceId")})
		err = callErr
		output = map[string]any{"ok": result.OK, "reason": result.Reason, "message": result.Message}
	case "put_begin":
		result, callErr := p.impl.StoragePutBegin(StoragePutBeginRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key"), ContentType: storageString(values, "contentType"), Size: storageInt64(values, "size")})
		err = callErr
		output = storageSessionOutput(result)
	case "put_chunk":
		data, decodeErr := base64.StdEncoding.DecodeString(storageString(values, "data"))
		if decodeErr != nil {
			output = map[string]any{"ok": false, "reason": "storage.v2.invalid_chunk_encoding", "message": decodeErr.Error()}
			break
		}
		result, callErr := p.impl.StoragePutChunk(StoragePutChunkRequest{SessionID: storageString(values, "sessionId"), Data: data, Final: storageBool(values, "final")})
		err = callErr
		output = storageResultOutput(result)
	case "open":
		result, callErr := p.impl.StorageOpen(StorageOpenRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key")})
		err = callErr
		output = storageSessionOutput(result)
	case "get_chunk":
		result, callErr := p.impl.StorageGetChunk(StorageGetChunkRequest{
			SessionID: storageString(values, "sessionId"),
			MaxBytes:  normalizeStorageChunkSize(storageInt64(values, "maxBytes")),
		})
		err = callErr
		output = map[string]any{"ok": result.OK, "data": base64.StdEncoding.EncodeToString(result.Data), "eof": result.EOF, "reason": result.Reason, "message": result.Message}
	case "close":
		result, callErr := p.impl.StorageClose(StorageCloseRequest{SessionID: storageString(values, "sessionId")})
		err = callErr
		output = storageResultOutput(result)
	case "delete":
		result, callErr := p.impl.StorageDelete(StorageObjectRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key")})
		err = callErr
		output = storageResultOutput(result)
	case "stat":
		result, callErr := p.impl.StorageStat(StorageStatRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key")})
		err = callErr
		output = map[string]any{"ok": result.OK, "exists": result.Exists, "size": result.Size, "contentType": result.ContentType, "modifiedUnix": result.ModifiedUnix, "reason": result.Reason, "message": result.Message}
	case "exists":
		result, callErr := p.impl.StorageExists(StorageExistsRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key")})
		err = callErr
		output = map[string]any{"ok": result.OK, "exists": result.Exists, "reason": result.Reason, "message": result.Message}
	case "public_url":
		result, callErr := p.impl.StoragePublicURL(StoragePublicURLRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key")})
		err = callErr
		output = storageURLOutput(result)
	case "signed_url":
		result, callErr := p.impl.StorageSignedURL(StorageSignedURLRequest{InstanceID: storageString(values, "instanceId"), Key: storageString(values, "key"), TTLSeconds: storageInt64(values, "ttlSeconds")})
		err = callErr
		output = storageURLOutput(result)
	case "configure_instance":
		result, callErr := p.impl.StorageConfigureInstance(StorageConfigureInstanceRequest{InstanceID: storageString(values, "instanceId"), Settings: storageStringMap(values, "settings")})
		err = callErr
		output = storageResultOutput(result)
	case "remove_instance":
		result, callErr := p.impl.StorageRemoveInstance(StorageRemoveInstanceRequest{InstanceID: storageString(values, "instanceId")})
		err = callErr
		output = storageResultOutput(result)
	case "probe_config":
		result, callErr := p.impl.StorageProbeConfig(StorageProbeConfigRequest{Settings: storageStringMap(values, "settings")})
		err = callErr
		output = map[string]any{"ok": result.OK, "reason": result.Reason, "message": result.Message}
	default:
		response.Error = storageProviderError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "provider.operation_unsupported", "attachment.storage.provider operation is not supported")
		return response, nil
	}
	if err != nil {
		return nil, err
	}
	document, err := pluginv2.NewTypedDocument(storageProviderSchemaPrefix+operation+".response@1", output)
	if err != nil {
		return nil, err
	}
	response.Output = document
	return response, nil
}

func storageProviderError(code protocolwire.ErrorCode, reason, message string) *protocolwire.ErrorDetail {
	return &protocolwire.ErrorDetail{Code: code, Reason: reason, Message: message}
}

func storageSessionOutput(result StorageSessionResponse) map[string]any {
	return map[string]any{"ok": result.OK, "sessionId": result.SessionID, "size": result.Size, "contentType": result.ContentType, "reason": result.Reason, "message": result.Message}
}

func storageResultOutput(result StorageResult) map[string]any {
	return map[string]any{"ok": result.OK, "reason": result.Reason, "message": result.Message}
}

func storageURLOutput(result StorageURLResponse) map[string]any {
	return map[string]any{"ok": result.OK, "url": result.URL, "reason": result.Reason, "message": result.Message}
}

func storageString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func storageStringMap(values map[string]any, key string) map[string]string {
	out := map[string]string{}
	if values == nil {
		return out
	}
	raw, _ := values[key].(map[string]any)
	for name, value := range raw {
		if text, ok := value.(string); ok {
			out[name] = text
		}
	}
	return out
}

func storageBool(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func storageInt64(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}

func normalizeStorageChunkSize(requested int64) int {
	if requested <= 0 || requested > int64(storageProviderChunkLimit) {
		return storageProviderChunkLimit
	}
	return int(requested)
}
