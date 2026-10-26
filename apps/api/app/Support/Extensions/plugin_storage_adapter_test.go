package extensionsruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

// memoryStorageRuntime 是进程内假存储后端，用于 PluginStorageAdapter 单测。
type memoryStorageRuntime struct {
	mu       sync.Mutex
	objects  map[string][]byte
	content  map[string]string
	puts     map[string]*putSession
	reads    map[string]*readSession
	chunkMax int
	probeOK  bool
	failPut  bool
}

type putSession struct {
	key         string
	contentType string
	buf         []byte
}

type readSession struct {
	data []byte
	off  int
}

func newMemoryStorageRuntime() *memoryStorageRuntime {
	return &memoryStorageRuntime{
		objects:  map[string][]byte{},
		content:  map[string]string{},
		puts:     map[string]*putSession{},
		reads:    map[string]*readSession{},
		chunkMax: 64, // 小块便于测多分片
		probeOK:  true,
	}
}

func (m *memoryStorageRuntime) newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (m *memoryStorageRuntime) StoragePutBegin(_ context.Context, _ string, req StoragePutBeginRequest) (StorageSessionResponse, error) {
	if m.failPut {
		return StorageSessionResponse{Reason: "storage.denied", Message: "put denied"}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.newID()
	m.puts[id] = &putSession{key: req.Key, contentType: req.ContentType}
	return StorageSessionResponse{OK: true, SessionID: id}, nil
}

func (m *memoryStorageRuntime) StoragePutChunk(_ context.Context, _ string, req StoragePutChunkRequest) (StorageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.puts[req.SessionID]
	if sess == nil {
		return StorageResult{Reason: "storage.session_missing"}, nil
	}
	sess.buf = append(sess.buf, req.Data...)
	if req.Final {
		data := make([]byte, len(sess.buf))
		copy(data, sess.buf)
		m.objects[sess.key] = data
		m.content[sess.key] = sess.contentType
		delete(m.puts, req.SessionID)
	}
	return StorageResult{OK: true}, nil
}

func (m *memoryStorageRuntime) StorageOpen(_ context.Context, _ string, req StorageOpenRequest) (StorageSessionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[req.Key]
	if !ok {
		return StorageSessionResponse{Reason: "storage.not_found", Message: "missing"}, nil
	}
	id := m.newID()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.reads[id] = &readSession{data: cp}
	return StorageSessionResponse{
		OK:          true,
		SessionID:   id,
		Size:        int64(len(cp)),
		ContentType: m.content[req.Key],
	}, nil
}

func (m *memoryStorageRuntime) StorageGetChunk(_ context.Context, _ string, req StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.reads[req.SessionID]
	if sess == nil {
		return StorageGetChunkResponse{Reason: "storage.session_missing"}, nil
	}
	max := req.MaxBytes
	if max <= 0 {
		max = m.chunkMax
	}
	if max > m.chunkMax {
		max = m.chunkMax
	}
	remain := len(sess.data) - sess.off
	if remain <= 0 {
		return StorageGetChunkResponse{OK: true, EOF: true}, nil
	}
	n := max
	if n > remain {
		n = remain
	}
	chunk := make([]byte, n)
	copy(chunk, sess.data[sess.off:sess.off+n])
	sess.off += n
	return StorageGetChunkResponse{OK: true, Data: chunk, EOF: sess.off >= len(sess.data)}, nil
}

func (m *memoryStorageRuntime) StorageClose(_ context.Context, _ string, req StorageCloseRequest) (StorageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.puts, req.SessionID)
	delete(m.reads, req.SessionID)
	return StorageResult{OK: true}, nil
}

func (m *memoryStorageRuntime) StorageDelete(_ context.Context, _ string, req StorageObjectRequest) (StorageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, req.Key)
	delete(m.content, req.Key)
	return StorageResult{OK: true}, nil
}

func (m *memoryStorageRuntime) StorageStat(_ context.Context, _ string, req StorageStatRequest) (StorageStatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[req.Key]
	if !ok {
		return StorageStatResponse{OK: true, Exists: false}, nil
	}
	return StorageStatResponse{
		OK: true, Exists: true, Size: int64(len(data)),
		ContentType: m.content[req.Key], ModifiedUnix: time.Now().Unix(),
	}, nil
}

func (m *memoryStorageRuntime) StorageExists(_ context.Context, _ string, req StorageExistsRequest) (StorageExistsResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[req.Key]
	return StorageExistsResponse{OK: true, Exists: ok}, nil
}

func (m *memoryStorageRuntime) StoragePublicURL(_ context.Context, _ string, req StoragePublicURLRequest) (StorageURLResponse, error) {
	return StorageURLResponse{OK: true, URL: "https://cdn.example/" + req.Key}, nil
}

func (m *memoryStorageRuntime) StorageSignedURL(_ context.Context, _ string, req StorageSignedURLRequest) (StorageURLResponse, error) {
	return StorageURLResponse{OK: true, URL: "https://cdn.example/signed/" + req.Key}, nil
}

func (m *memoryStorageRuntime) StorageProbe(context.Context, string, StorageProbeRequest) (StorageProbeResponse, error) {
	if !m.probeOK {
		return StorageProbeResponse{Reason: "storage.probe_failed", Message: "down"}, nil
	}
	return StorageProbeResponse{OK: true}, nil
}

func TestPluginStorageAdapterRoundTrip(t *testing.T) {
	rt := newMemoryStorageRuntime()
	adapter, err := NewPluginStorageAdapter("acme.store", rt, 32)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}

	payload := bytes.Repeat([]byte("hello-storage-"), 20) // > 32 字节，多块
	if err := adapter.Put(context.Background(), "a/b/file.bin", storage.PutInput{
		Reader:      bytes.NewReader(payload),
		Size:        int64(len(payload)),
		ContentType: "application/octet-stream",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ok, err := adapter.Exists(context.Background(), "a/b/file.bin")
	if err != nil || !ok {
		t.Fatalf("Exists: ok=%v err=%v", ok, err)
	}
	info, err := adapter.Stat(context.Background(), "a/b/file.bin")
	if err != nil || info.Size != int64(len(payload)) {
		t.Fatalf("Stat: %#v err=%v", info, err)
	}

	reader, err := adapter.Open(context.Background(), "a/b/file.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch len got=%d want=%d", len(got), len(payload))
	}

	if url := adapter.PublicURL("a/b/file.bin"); url == "" {
		t.Fatal("PublicURL empty")
	}
	if err := adapter.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := adapter.Delete(context.Background(), "a/b/file.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ok, _ = adapter.Exists(context.Background(), "a/b/file.bin")
	if ok {
		t.Fatal("expected deleted")
	}
}

func TestPluginStorageAdapterEmptyObject(t *testing.T) {
	rt := newMemoryStorageRuntime()
	adapter, err := NewPluginStorageAdapter("acme.store", rt, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Put(context.Background(), "empty.txt", storage.PutInput{
		Reader: bytes.NewReader(nil),
		Size:   0,
	}); err != nil {
		t.Fatalf("empty Put: %v", err)
	}
	reader, err := adapter.Open(context.Background(), "empty.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPluginStorageAdapterPutDenied(t *testing.T) {
	rt := newMemoryStorageRuntime()
	rt.failPut = true
	adapter, _ := NewPluginStorageAdapter("acme.store", rt, 0)
	err := adapter.Put(context.Background(), "x", storage.PutInput{Reader: bytes.NewReader([]byte("a"))})
	var rpc *StorageRPCError
	if err == nil || !errors.As(err, &rpc) {
		t.Fatalf("expected StorageRPCError, got %v", err)
	}
}

func TestNewPluginStorageAdapterRequiresRuntime(t *testing.T) {
	if _, err := NewPluginStorageAdapter("", newMemoryStorageRuntime(), 0); err == nil {
		t.Fatal("expected error for empty extension id")
	}
	if _, err := NewPluginStorageAdapter("x", nil, 0); err == nil {
		t.Fatal("expected error for nil runtime")
	}
}

type instanceRecordingRuntime struct {
	*memoryStorageRuntime
	configuredID string
	putID        string
}

func (r *instanceRecordingRuntime) StorageConfigureInstance(_ context.Context, _ string, req StorageConfigureInstanceRequest) (StorageResult, error) {
	r.configuredID = req.InstanceID
	return StorageResult{OK: true}, nil
}

func (r *instanceRecordingRuntime) StorageRemoveInstance(context.Context, string, StorageRemoveInstanceRequest) (StorageResult, error) {
	return StorageResult{OK: true}, nil
}

func (r *instanceRecordingRuntime) StorageProbeConfig(context.Context, string, StorageProbeConfigRequest) (StorageProbeResponse, error) {
	return StorageProbeResponse{OK: true}, nil
}

func (r *instanceRecordingRuntime) StoragePutBegin(ctx context.Context, extensionID string, req StoragePutBeginRequest) (StorageSessionResponse, error) {
	r.putID = req.InstanceID
	return r.memoryStorageRuntime.StoragePutBegin(ctx, extensionID, req)
}

func TestStorageInstanceAdapterCarriesInstanceID(t *testing.T) {
	runtime := &instanceRecordingRuntime{memoryStorageRuntime: newMemoryStorageRuntime()}
	factory := NewPluginStorageAdapterFactory(runtime, 32)
	adapter, err := factory.NewStorageInstanceAdapter(context.Background(), "acme.store", "instance-a", map[string]string{"bucket": "uploads"})
	if err != nil {
		t.Fatalf("instance adapter: %v", err)
	}
	if err := adapter.Put(context.Background(), "file.txt", storage.PutInput{Reader: bytes.NewReader([]byte("ok")), Size: 2}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if runtime.configuredID != "instance-a" || runtime.putID != "instance-a" {
		t.Fatalf("instance ids: configured=%q put=%q", runtime.configuredID, runtime.putID)
	}
}
