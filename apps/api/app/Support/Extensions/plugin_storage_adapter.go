package extensionsruntime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

// DefaultPluginChunkSize 默认分块大小（E6.2 决策：1 MiB）。
const DefaultPluginChunkSize = 1 << 20

// PluginStorageAdapter 将 storage.Adapter 翻译为扩展 runtime 的分块存储 RPC。
// 放在 Extensions 包以避免 Support/Storage ↔ Extensions 的 import 环。
// 业务代码只依赖 storage.Adapter 接口。
type PluginStorageAdapter struct {
	extensionID string
	instanceID  string
	runtime     StorageRuntime
	chunkSize   int
}

// PluginStorageAdapterFactory keeps concrete runtime RPCs behind the stable
// ExtensionRuntime adapter-factory boundary used by product models.
type PluginStorageAdapterFactory struct {
	runtime   StorageRuntime
	instances StorageInstanceRuntime
	chunkSize int
}

func NewPluginStorageAdapterFactory(runtime StorageRuntime, chunkSize int) *PluginStorageAdapterFactory {
	if runtime == nil {
		return nil
	}
	instances, _ := runtime.(StorageInstanceRuntime)
	if manager, ok := runtime.(*Manager); ok {
		instances = &StorageInstanceInvoker{Manager: manager}
	}
	return &PluginStorageAdapterFactory{runtime: runtime, instances: instances, chunkSize: chunkSize}
}

func (f *PluginStorageAdapterFactory) NewStorageAdapter(extensionID string) (storage.Adapter, error) {
	if f == nil {
		return nil, storage.ErrInvalidConfig
	}
	return NewPluginStorageAdapter(extensionID, f.runtime, f.chunkSize)
}

func (f *PluginStorageAdapterFactory) NewStorageInstanceAdapter(ctx context.Context, extensionID, instanceID string, settings map[string]string) (storage.Adapter, error) {
	if f == nil || f.runtime == nil || strings.TrimSpace(instanceID) == "" {
		return nil, storage.ErrInvalidConfig
	}
	if f.instances == nil {
		return nil, storage.ErrInvalidConfig
	}
	result, err := f.instances.StorageConfigureInstance(ctx, extensionID, StorageConfigureInstanceRequest{InstanceID: instanceID, Settings: settings})
	if err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, storageRPCErr(result.Reason, result.Message)
	}
	return newPluginStorageAdapter(extensionID, instanceID, f.runtime, f.chunkSize)
}

func (f *PluginStorageAdapterFactory) ProbeStorageInstance(ctx context.Context, extensionID string, settings map[string]string) error {
	if f == nil || f.runtime == nil {
		return storage.ErrInvalidConfig
	}
	if f.instances == nil {
		return storage.ErrInvalidConfig
	}
	result, err := f.instances.StorageProbeConfig(ctx, extensionID, StorageProbeConfigRequest{Settings: settings})
	if err != nil {
		return err
	}
	if !result.OK {
		return storageRPCErr(result.Reason, result.Message)
	}
	return nil
}

func (f *PluginStorageAdapterFactory) RemoveStorageInstance(ctx context.Context, extensionID, instanceID string) error {
	if f == nil || f.runtime == nil {
		return storage.ErrInvalidConfig
	}
	if f.instances == nil {
		return storage.ErrInvalidConfig
	}
	result, err := f.instances.StorageRemoveInstance(ctx, extensionID, StorageRemoveInstanceRequest{InstanceID: instanceID})
	if err != nil {
		return err
	}
	if !result.OK {
		return storageRPCErr(result.Reason, result.Message)
	}
	return nil
}

// NewPluginStorageAdapter 构造插件后端适配器。chunkSize<=0 时用 1 MiB。
func NewPluginStorageAdapter(extensionID string, runtime StorageRuntime, chunkSize int) (*PluginStorageAdapter, error) {
	return newPluginStorageAdapter(extensionID, "", runtime, chunkSize)
}

func newPluginStorageAdapter(extensionID, instanceID string, runtime StorageRuntime, chunkSize int) (*PluginStorageAdapter, error) {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" || runtime == nil {
		return nil, storage.ErrInvalidConfig
	}
	if chunkSize <= 0 {
		chunkSize = DefaultPluginChunkSize
	}
	return &PluginStorageAdapter{
		extensionID: extensionID,
		instanceID:  strings.TrimSpace(instanceID),
		runtime:     runtime,
		chunkSize:   chunkSize,
	}, nil
}

// 编译期确认实现 storage.Adapter。
var _ storage.Adapter = (*PluginStorageAdapter)(nil)

func (a *PluginStorageAdapter) Put(ctx context.Context, key string, input storage.PutInput) error {
	if input.Reader == nil {
		return fmt.Errorf("%w: empty reader", storage.ErrInvalidConfig)
	}
	begin, err := a.runtime.StoragePutBegin(ctx, a.extensionID, StoragePutBeginRequest{
		InstanceID:  a.instanceID,
		Key:         key,
		ContentType: input.ContentType,
		Size:        input.Size,
	})
	if err != nil {
		return err
	}
	if !begin.OK || begin.SessionID == "" {
		return storageRPCErr(begin.Reason, begin.Message)
	}
	sessionID := begin.SessionID
	committed := false
	defer func() {
		if !committed {
			_, _ = a.runtime.StorageClose(context.WithoutCancel(ctx), a.extensionID, StorageCloseRequest{
				SessionID: sessionID,
			})
		}
	}()

	buf := make([]byte, a.chunkSize)
	for {
		n, readErr := input.Reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			final := readErr == io.EOF
			result, putErr := a.runtime.StoragePutChunk(ctx, a.extensionID, StoragePutChunkRequest{
				SessionID: sessionID,
				Data:      chunk,
				Final:     final,
			})
			if putErr != nil {
				return putErr
			}
			if !result.OK {
				return storageRPCErr(result.Reason, result.Message)
			}
			if final {
				committed = true
				return nil
			}
		}
		if readErr == io.EOF {
			if n == 0 {
				result, putErr := a.runtime.StoragePutChunk(ctx, a.extensionID, StoragePutChunkRequest{
					SessionID: sessionID,
					Data:      nil,
					Final:     true,
				})
				if putErr != nil {
					return putErr
				}
				if !result.OK {
					return storageRPCErr(result.Reason, result.Message)
				}
				committed = true
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (a *PluginStorageAdapter) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	open, err := a.runtime.StorageOpen(ctx, a.extensionID, StorageOpenRequest{InstanceID: a.instanceID, Key: key})
	if err != nil {
		return nil, err
	}
	if !open.OK || open.SessionID == "" {
		return nil, storageRPCErr(open.Reason, open.Message)
	}
	return &pluginObjectReader{
		ctx:         ctx,
		extensionID: a.extensionID,
		sessionID:   open.SessionID,
		runtime:     a.runtime,
		chunkSize:   a.chunkSize,
	}, nil
}

func (a *PluginStorageAdapter) Delete(ctx context.Context, key string) error {
	result, err := a.runtime.StorageDelete(ctx, a.extensionID, StorageObjectRequest{InstanceID: a.instanceID, Key: key})
	if err != nil {
		return err
	}
	if !result.OK {
		return storageRPCErr(result.Reason, result.Message)
	}
	return nil
}

func (a *PluginStorageAdapter) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	resp, err := a.runtime.StorageStat(ctx, a.extensionID, StorageStatRequest{InstanceID: a.instanceID, Key: key})
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if !resp.OK {
		return storage.ObjectInfo{}, storageRPCErr(resp.Reason, resp.Message)
	}
	if !resp.Exists {
		return storage.ObjectInfo{}, fmt.Errorf("storage: object not found")
	}
	info := storage.ObjectInfo{
		Key:         key,
		Size:        resp.Size,
		ContentType: resp.ContentType,
	}
	if resp.ModifiedUnix > 0 {
		info.ModifiedAt = time.Unix(resp.ModifiedUnix, 0).UTC()
	}
	return info, nil
}

func (a *PluginStorageAdapter) Exists(ctx context.Context, key string) (bool, error) {
	resp, err := a.runtime.StorageExists(ctx, a.extensionID, StorageExistsRequest{InstanceID: a.instanceID, Key: key})
	if err != nil {
		return false, err
	}
	if !resp.OK {
		return false, storageRPCErr(resp.Reason, resp.Message)
	}
	return resp.Exists, nil
}

func (a *PluginStorageAdapter) PublicURL(key string) string {
	resp, err := a.runtime.StoragePublicURL(context.Background(), a.extensionID, StoragePublicURLRequest{InstanceID: a.instanceID, Key: key})
	if err != nil || !resp.OK {
		return ""
	}
	return resp.URL
}

func (a *PluginStorageAdapter) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	seconds := int64(ttl / time.Second)
	if seconds <= 0 {
		seconds = 300
	}
	resp, err := a.runtime.StorageSignedURL(ctx, a.extensionID, StorageSignedURLRequest{
		InstanceID: a.instanceID,
		Key:        key,
		TTLSeconds: seconds,
	})
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", storageRPCErr(resp.Reason, resp.Message)
	}
	return resp.URL, nil
}

func (a *PluginStorageAdapter) Probe(ctx context.Context) error {
	resp, err := a.runtime.StorageProbe(ctx, a.extensionID, StorageProbeRequest{InstanceID: a.instanceID})
	if err != nil {
		return err
	}
	if !resp.OK {
		return storageRPCErr(resp.Reason, resp.Message)
	}
	return nil
}

type pluginObjectReader struct {
	ctx         context.Context
	extensionID string
	sessionID   string
	runtime     StorageRuntime
	chunkSize   int
	buf         []byte
	eof         bool
	closed      bool
}

func (r *pluginObjectReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.buf) == 0 && !r.eof {
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	if len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		return 0, nil
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *pluginObjectReader) fill() error {
	max := r.chunkSize
	if max <= 0 {
		max = DefaultPluginChunkSize
	}
	resp, err := r.runtime.StorageGetChunk(r.ctx, r.extensionID, StorageGetChunkRequest{
		SessionID: r.sessionID,
		MaxBytes:  max,
	})
	if err != nil {
		return err
	}
	if !resp.OK {
		return storageRPCErr(resp.Reason, resp.Message)
	}
	if len(resp.Data) > 0 {
		r.buf = append(r.buf[:0], resp.Data...)
	}
	if resp.EOF {
		r.eof = true
	}
	return nil
}

func (r *pluginObjectReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	_, err := r.runtime.StorageClose(context.WithoutCancel(r.ctx), r.extensionID, StorageCloseRequest{
		SessionID: r.sessionID,
	})
	return err
}
