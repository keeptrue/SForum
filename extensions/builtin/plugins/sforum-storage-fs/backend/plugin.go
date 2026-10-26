package main

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
)

// fsStoragePlugin 是 attachment.storage.provider 的参考实现（E6.4）。
// 对象写入可配置根目录；分块会话在进程内维护。
type fsStoragePlugin struct {
	mu      sync.Mutex
	puts    map[string]*putSession
	reads   map[string]*readSession
	root    string
	public  string
	rootErr string
}

type putSession struct {
	key         string
	contentType string
	tmpPath     string
	file        *os.File
}

type readSession struct {
	file *os.File
}

func newFSStoragePlugin() *fsStoragePlugin {
	p := &fsStoragePlugin{
		puts:  map[string]*putSession{},
		reads: map[string]*readSession{},
	}
	p.reloadConfig()
	return p
}

func (p *fsStoragePlugin) reloadConfig() {
	root := strings.TrimSpace(os.Getenv("SFORUM_SETTING_ROOT_PATH"))
	public := strings.TrimSpace(os.Getenv("SFORUM_SETTING_PUBLIC_BASE_URL"))
	p.root = root
	p.public = public
	p.rootErr = ""
	if root == "" {
		p.rootErr = "root_path is required"
		return
	}
	if !filepath.IsAbs(root) {
		p.rootErr = "root_path must be an absolute path"
		return
	}
}

func (p *fsStoragePlugin) StorageProbe(pluginsdk.StorageProbeRequest) (pluginsdk.StorageProbeResponse, error) {
	p.reloadConfig()
	if p.rootErr != "" {
		return pluginsdk.StorageProbeResponse{
			Reason:  "storage.fs.config",
			Message: p.rootErr,
		}, nil
	}
	if err := os.MkdirAll(p.root, 0o750); err != nil {
		return pluginsdk.StorageProbeResponse{
			Reason:  "storage.fs.mkdir",
			Message: err.Error(),
		}, nil
	}
	// 写探测文件确认可写。
	probe := filepath.Join(p.root, ".sforum-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return pluginsdk.StorageProbeResponse{
			Reason:  "storage.fs.not_writable",
			Message: err.Error(),
		}, nil
	}
	_ = os.Remove(probe)
	return pluginsdk.StorageProbeResponse{OK: true, Reason: "storage.ok", Message: "ok"}, nil
}

func (p *fsStoragePlugin) StoragePutBegin(req pluginsdk.StoragePutBeginRequest) (pluginsdk.StorageSessionResponse, error) {
	p.reloadConfig()
	if p.rootErr != "" {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.config", Message: p.rootErr}, nil
	}
	target, err := p.objectPath(req.Key)
	if err != nil {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.invalid_key", Message: err.Error()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.mkdir", Message: err.Error()}, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".sforum-put-*")
	if err != nil {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.tmp", Message: err.Error()}, nil
	}
	id := sessionID()
	p.mu.Lock()
	p.puts[id] = &putSession{
		key:         req.Key,
		contentType: req.ContentType,
		tmpPath:     tmp.Name(),
		file:        tmp,
	}
	p.mu.Unlock()
	return pluginsdk.StorageSessionResponse{OK: true, SessionID: id}, nil
}

func (p *fsStoragePlugin) StoragePutChunk(req pluginsdk.StoragePutChunkRequest) (pluginsdk.StorageResult, error) {
	p.mu.Lock()
	sess := p.puts[req.SessionID]
	p.mu.Unlock()
	if sess == nil || sess.file == nil {
		return pluginsdk.StorageResult{Reason: "storage.fs.session_missing", Message: "put session not found"}, nil
	}
	if len(req.Data) > 0 {
		if _, err := sess.file.Write(req.Data); err != nil {
			p.abortPut(req.SessionID)
			return pluginsdk.StorageResult{Reason: "storage.fs.write", Message: err.Error()}, nil
		}
	}
	if !req.Final {
		return pluginsdk.StorageResult{OK: true}, nil
	}
	target, err := p.objectPath(sess.key)
	if err != nil {
		p.abortPut(req.SessionID)
		return pluginsdk.StorageResult{Reason: "storage.fs.invalid_key", Message: err.Error()}, nil
	}
	if err := sess.file.Close(); err != nil {
		p.abortPut(req.SessionID)
		return pluginsdk.StorageResult{Reason: "storage.fs.close", Message: err.Error()}, nil
	}
	sess.file = nil
	if err := os.Rename(sess.tmpPath, target); err != nil {
		_ = os.Remove(sess.tmpPath)
		p.mu.Lock()
		delete(p.puts, req.SessionID)
		p.mu.Unlock()
		return pluginsdk.StorageResult{Reason: "storage.fs.commit", Message: err.Error()}, nil
	}
	p.mu.Lock()
	delete(p.puts, req.SessionID)
	p.mu.Unlock()
	return pluginsdk.StorageResult{OK: true}, nil
}

func (p *fsStoragePlugin) StorageOpen(req pluginsdk.StorageOpenRequest) (pluginsdk.StorageSessionResponse, error) {
	p.reloadConfig()
	if p.rootErr != "" {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.config", Message: p.rootErr}, nil
	}
	target, err := p.objectPath(req.Key)
	if err != nil {
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.invalid_key", Message: err.Error()}, nil
	}
	file, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return pluginsdk.StorageSessionResponse{Reason: "storage.fs.not_found", Message: "object not found"}, nil
		}
		return pluginsdk.StorageSessionResponse{Reason: "storage.fs.open", Message: err.Error()}, nil
	}
	info, _ := file.Stat()
	id := sessionID()
	p.mu.Lock()
	p.reads[id] = &readSession{file: file}
	p.mu.Unlock()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return pluginsdk.StorageSessionResponse{OK: true, SessionID: id, Size: size}, nil
}

func (p *fsStoragePlugin) StorageGetChunk(req pluginsdk.StorageGetChunkRequest) (pluginsdk.StorageGetChunkResponse, error) {
	p.mu.Lock()
	sess := p.reads[req.SessionID]
	p.mu.Unlock()
	if sess == nil || sess.file == nil {
		return pluginsdk.StorageGetChunkResponse{Reason: "storage.fs.session_missing", Message: "read session not found"}, nil
	}
	max := req.MaxBytes
	if max <= 0 {
		max = 1 << 20
	}
	buf := make([]byte, max)
	n, err := sess.file.Read(buf)
	if n > 0 {
		return pluginsdk.StorageGetChunkResponse{
			OK:   true,
			Data: buf[:n],
			EOF:  err == io.EOF,
		}, nil
	}
	if err == io.EOF {
		return pluginsdk.StorageGetChunkResponse{OK: true, EOF: true}, nil
	}
	if err != nil {
		return pluginsdk.StorageGetChunkResponse{Reason: "storage.fs.read", Message: err.Error()}, nil
	}
	return pluginsdk.StorageGetChunkResponse{OK: true, EOF: true}, nil
}

func (p *fsStoragePlugin) StorageClose(req pluginsdk.StorageCloseRequest) (pluginsdk.StorageResult, error) {
	p.mu.Lock()
	if put := p.puts[req.SessionID]; put != nil {
		delete(p.puts, req.SessionID)
		p.mu.Unlock()
		if put.file != nil {
			_ = put.file.Close()
		}
		if put.tmpPath != "" {
			_ = os.Remove(put.tmpPath)
		}
		return pluginsdk.StorageResult{OK: true}, nil
	}
	if read := p.reads[req.SessionID]; read != nil {
		delete(p.reads, req.SessionID)
		p.mu.Unlock()
		if read.file != nil {
			_ = read.file.Close()
		}
		return pluginsdk.StorageResult{OK: true}, nil
	}
	p.mu.Unlock()
	return pluginsdk.StorageResult{OK: true}, nil
}

func (p *fsStoragePlugin) StorageDelete(req pluginsdk.StorageObjectRequest) (pluginsdk.StorageResult, error) {
	p.reloadConfig()
	if p.rootErr != "" {
		return pluginsdk.StorageResult{Reason: "storage.fs.config", Message: p.rootErr}, nil
	}
	target, err := p.objectPath(req.Key)
	if err != nil {
		return pluginsdk.StorageResult{Reason: "storage.fs.invalid_key", Message: err.Error()}, nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return pluginsdk.StorageResult{Reason: "storage.fs.delete", Message: err.Error()}, nil
	}
	return pluginsdk.StorageResult{OK: true}, nil
}

func (p *fsStoragePlugin) StorageStat(req pluginsdk.StorageStatRequest) (pluginsdk.StorageStatResponse, error) {
	p.reloadConfig()
	if p.rootErr != "" {
		return pluginsdk.StorageStatResponse{Reason: "storage.fs.config", Message: p.rootErr}, nil
	}
	target, err := p.objectPath(req.Key)
	if err != nil {
		return pluginsdk.StorageStatResponse{Reason: "storage.fs.invalid_key", Message: err.Error()}, nil
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return pluginsdk.StorageStatResponse{OK: true, Exists: false}, nil
		}
		return pluginsdk.StorageStatResponse{Reason: "storage.fs.stat", Message: err.Error()}, nil
	}
	return pluginsdk.StorageStatResponse{
		OK:           true,
		Exists:       true,
		Size:         info.Size(),
		ModifiedUnix: info.ModTime().UTC().Unix(),
	}, nil
}

func (p *fsStoragePlugin) StorageExists(req pluginsdk.StorageExistsRequest) (pluginsdk.StorageExistsResponse, error) {
	stat, err := p.StorageStat(pluginsdk.StorageStatRequest{Key: req.Key})
	if err != nil {
		return pluginsdk.StorageExistsResponse{}, err
	}
	if !stat.OK && stat.Reason != "" {
		return pluginsdk.StorageExistsResponse{Reason: stat.Reason, Message: stat.Message}, nil
	}
	return pluginsdk.StorageExistsResponse{OK: true, Exists: stat.Exists}, nil
}

func (p *fsStoragePlugin) StoragePublicURL(req pluginsdk.StoragePublicURLRequest) (pluginsdk.StorageURLResponse, error) {
	p.reloadConfig()
	base := strings.TrimRight(p.public, "/")
	if base == "" {
		return pluginsdk.StorageURLResponse{OK: true, URL: ""}, nil
	}
	key := strings.TrimPrefix(strings.ReplaceAll(req.Key, "\\", "/"), "/")
	return pluginsdk.StorageURLResponse{OK: true, URL: base + "/" + key}, nil
}

func (p *fsStoragePlugin) StorageSignedURL(req pluginsdk.StorageSignedURLRequest) (pluginsdk.StorageURLResponse, error) {
	// 文件系统后端没有时间受限的能力 URL，必须由宿主流式返回。
	_ = req.TTLSeconds
	return pluginsdk.StorageURLResponse{OK: true, URL: ""}, nil
}

func (p *fsStoragePlugin) abortPut(sessionID string) {
	p.mu.Lock()
	sess := p.puts[sessionID]
	delete(p.puts, sessionID)
	p.mu.Unlock()
	if sess == nil {
		return
	}
	if sess.file != nil {
		_ = sess.file.Close()
	}
	if sess.tmpPath != "" {
		_ = os.Remove(sess.tmpPath)
	}
}

func (p *fsStoragePlugin) objectPath(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") {
		return "", errInvalidKey
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errInvalidKey
		}
	}
	cleaned := filepath.Clean(filepath.Join(p.root, filepath.FromSlash(key)))
	rootClean := filepath.Clean(p.root)
	rel, err := filepath.Rel(rootClean, cleaned)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errInvalidKey
	}
	return cleaned, nil
}

var errInvalidKey = &simpleError{msg: "invalid object key"}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

func sessionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "-" + time.Now().UTC().Format("150405")
}
